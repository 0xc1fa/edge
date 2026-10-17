// Package api 提供 REST 与前端静态托管。
//
// 健康理念沿用 obs：进程活着不等于健康。/api/health 会带上
// 「最近一次成功对账距今多久」，超过容忍窗口就返回 503。
package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"neo/internal/auth"
	"neo/internal/catalog"
	"neo/internal/incus"
	"neo/internal/limit"
	"neo/internal/recon"
	"neo/internal/store"
)

// Config 是 API 层依赖。
type Config struct {
	Version string
	Auth    *auth.Service
	// Users 是成员/凭证/会话台账。鉴权走 Auth，读写台账走它：
	// Auth 负责「能不能进」，这里负责「进来的是谁、能不能改」。
	Users   *store.Access
	Recon   *recon.Loop
	Catalog catalog.Catalog
	Apps    *store.AppStore
	// Owners 是实例归属台账：哪台机器归谁。为空时归属只能从申请台账推导，
	// 于是宿主上手工建的实例对租户而言永远点不动。
	Owners *store.OwnerStore
	// GPUS 是 GPU 预约台账；Incus 是执行挂载/摘卡/重启的写通道。
	// 两者成对出现：没有台账的写操作是不留痕的，没有写通道的台账只是一张愿望清单。
	GPUS  *store.GPUStore
	Incus *incus.Client
	// GPUPCI 是可分配的显卡地址（形如 0000:c4:00.0）。
	// 必须显式配置：容器里没有 nvidia-smi，探测不到卡在哪条总线上，
	// 而「通配挂载」会把宿主全部卡都交给一个实例 —— 那不是分配，那是意外。
	GPUPCI []string
	// ApplyLimit 限制「成功提交」的单数，保护管理员待办不被灌水
	ApplyLimit *limit.Limiter
	// ApplyFailLimit 限制失败尝试次数，防止脚本反复试探主机名是否存在。
	// 两者分开是因为语义不同：单据额度不该被填错表单消耗掉，
	// 而失败也必须有限额，否则 /api/applications 就成了主机名枚举器。
	ApplyFailLimit *limit.Limiter
	GPUTotal       int
	WebDir         string
	DataDir        string
	MaxAge         time.Duration // 判定健康的最大对账延迟
	// SecureCookie 只有前面架了 TLS 才该打开：明文 HTTP 下发带 Secure 的 cookie，
	// 浏览器会直接丢弃，结果是谁也登不进来。NEO_SECURE_COOKIE=1 显式开启。
	SecureCookie bool
	// TrustProxy 决定是否采信 X-Forwarded-For。默认关：
	// 直曝部署下这个头是客户端自己填的，登录限流的 key 与台账里的来源 IP 会一起失真。
	TrustProxy bool
}

// Server 是 HTTP 入口。
type Server struct {
	cfg Config
	mux *http.ServeMux
}

// New 组装路由。
func New(cfg Config) *Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/auth/redeem", s.handleRedeem)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/session", s.handleSession)

	// 公开区：产品页要能在未登录状态下读目录、提交申请
	s.mux.HandleFunc("GET /api/catalog", s.handleCatalog)
	s.mux.HandleFunc("POST /api/applications", s.handleApply)
	s.mux.HandleFunc("GET /api/applications/{id}", s.handleApplicationGet)

	// 登录可见区：实例与容量属于「平台在跑什么」，成员也能看
	s.mux.HandleFunc("GET /api/instances", s.handleInstances)
	s.mux.HandleFunc("GET /api/instances/{name}", s.handleInstance)
	s.mux.HandleFunc("GET /api/series/{name}", s.handleSeries)
	s.mux.HandleFunc("GET /api/capacity", s.handleCapacity)

	// 对自己名下的实例能做的事：网页终端、改口令、开关机重启。
	// 登录即可到达，但「这台机器是不是你的」在 handler 里判 ——
	// 归属是业务口径，路由层看不出来。
	s.mux.HandleFunc("GET /api/instances/{name}/shell", s.handleInstanceShell)
	s.mux.HandleFunc("POST /api/instances/{name}/power", s.handleInstancePower)
	s.mux.HandleFunc("POST /api/instances/{name}/password", s.handleInstancePassword)

	// 分配归属只属于管理员：它在发「谁能动哪台机器」的钥匙
	s.mux.HandleFunc("POST /api/instances/{name}/assign", s.handleInstanceAssign)
	s.mux.HandleFunc("DELETE /api/instances/{name}/assign", s.handleInstanceUnassign)
	s.mux.HandleFunc("GET /api/instance-owners", s.handleInstanceOwners)

	// 管理员区：处理申请，以及管人 —— 成员、登录凭证、会话都在这里
	s.mux.HandleFunc("GET /api/applications", s.handleApplications)
	s.mux.HandleFunc("POST /api/applications/{id}/decision", s.handleDecision)

	// GPU 预约：谁都能看排期、都能申请；审批与挂载只有管理员
	s.mux.HandleFunc("GET /api/gpu-bookings", s.handleGPUList)
	s.mux.HandleFunc("POST /api/gpu-bookings", s.handleGPUApply)
	s.mux.HandleFunc("POST /api/gpu-bookings/{id}/decision", s.handleGPUDecide)
	s.mux.HandleFunc("POST /api/gpu-bookings/{id}/attach", s.handleGPUAttach)
	s.mux.HandleFunc("POST /api/gpu-bookings/{id}/detach", s.handleGPUDetach)
	s.mux.HandleFunc("POST /api/gpu-bookings/{id}/cancel", s.handleGPUCancel)
	// 删记录是管理员动作：它把一条留痕彻底抹掉，比改状态更需要收口
	s.mux.HandleFunc("DELETE /api/gpu-bookings/{id}", s.handleGPUDelete)
	s.mux.HandleFunc("POST /api/instances/{name}/restart", s.handleInstanceRestart)
	s.mux.HandleFunc("GET /api/users", s.handleUsers)
	s.mux.HandleFunc("POST /api/users", s.handleUserCreate)
	s.mux.HandleFunc("PATCH /api/users/{id}", s.handleUserUpdate)
	s.mux.HandleFunc("DELETE /api/users/{id}", s.handleUserDelete)
	s.mux.HandleFunc("POST /api/users/{id}/invite", s.handleInviteCreate)
	s.mux.HandleFunc("POST /api/users/{id}/revoke-sessions", s.handleUserRevokeSessions)
	s.mux.HandleFunc("GET /api/invites", s.handleInvites)
	s.mux.HandleFunc("POST /api/invites", s.handleInviteIssue)
	s.mux.HandleFunc("POST /api/invites/{token}/revoke", s.handleInviteRevoke)
	s.mux.HandleFunc("GET /api/sessions", s.handleSessions)
	s.mux.HandleFunc("POST /api/sessions/{id}/revoke", s.handleSessionRevoke)
	s.mux.HandleFunc("DELETE /api/sessions/{id}", s.handleSessionDelete)

	s.mux.HandleFunc("/", s.handleStatic)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if !s.gate(w, r) {
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

// level 是接口的访问门槛，逐递进：每一级隐含比它低的那一级。
type level int

const (
	levelAny   level = iota // 公开，不校验
	levelUser               // 有有效会话即可
	levelAdmin              // 必须管理员
)

// gate 是唯一一道访问控制关卡。通过返回 true，否则已经写好响应并返回 false。
//
// 判定粒度是「方法 + 路径 + 角色」而不是只看路径：
// GET /api/applications 是管理员看全量、POST 才是匿名提交，同路径不同门槛。
func (s *Server) gate(w http.ResponseWriter, r *http.Request) bool {
	need := requiredLevel(r.Method, r.URL.Path)
	if need == levelAny {
		return true
	}
	view, state := s.current(r)
	if state != store.SessionValid {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": state.String()})
		return false
	}
	if need == levelAdmin && view.Role != store.RoleAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "需要管理员权限"})
		return false
	}
	return true
}

// current 取出本次请求的会话。顺带完成心跳 —— 见 store.Access.Resolve。
func (s *Server) current(r *http.Request) (store.SessionView, store.SessionState) {
	c, err := r.Cookie(auth.CookieName)
	if err != nil {
		return store.SessionView{}, store.SessionMissing
	}
	return s.cfg.Auth.Resolve(c.Value)
}

// isPublic 列出无需鉴权的接口，是 requiredLevel 的第一道分支。
// 保留这个函数是因为它有专门的收口测试，任何人误放开一个写动作都会被拦下。
func isPublic(method, p string) bool {
	switch p {
	case "/api/health", "/api/session":
		return true
	// 两条登录入口都必须匿名可调：此刻还没有会话可言
	case "/api/auth/redeem", "/api/auth/login":
		return method == http.MethodPost
	case "/api/catalog":
		return method == http.MethodGet
	case "/api/applications":
		return method == http.MethodPost
	}
	// 凭回执号自助查询进度，但不含 /decision 这类写动作
	if strings.HasPrefix(p, "/api/applications/") {
		return method == http.MethodGet && !strings.HasSuffix(p, "/decision")
	}
	return false
}

// requiredLevel 在 isPublic 之上再分「登录即可」与「必须管理员」。
// 管人、发证、踢会话天然是管理员动作；看实例与查容量，成员同享。
func requiredLevel(method, p string) level {
	if isPublic(method, p) {
		return levelAny
	}
	switch {
	case strings.HasSuffix(p, "/decision"):
		return levelAdmin
	case strings.HasPrefix(p, "/api/users"),
		strings.HasPrefix(p, "/api/invites"),
		strings.HasPrefix(p, "/api/sessions"):
		return levelAdmin
	case p == "/api/applications":
		return levelAdmin
	// 动宿主的动作一律只有管理员：挂卡、摘卡、审批、重启
	case strings.HasPrefix(p, "/api/instances/") && strings.HasSuffix(p, "/restart"):
		return levelAdmin
	// 自己实例的电源、口令与网页终端登录即可到达，
	// 但「这台机器是不是你的」在 handler 里判 —— 归属是业务口径，路由层看不出来。
	// 这里显式写出来而不是让它落到默认分支，是为了让「刻意放开」这件事
	// 在代码里看得见：默认分支是兜底，不是决策。
	case strings.HasPrefix(p, "/api/instances/") &&
		(strings.HasSuffix(p, "/power") ||
			strings.HasSuffix(p, "/password") ||
			strings.HasSuffix(p, "/shell")):
		return levelUser
	// 分配归属是管理员动作：它决定谁能动哪台机器
	case p == "/api/instance-owners":
		return levelAdmin
	case strings.HasPrefix(p, "/api/instances/") && strings.HasSuffix(p, "/assign"):
		return levelAdmin
	// 预约本体分两级：看排期与提交申请是登录即可，
	// 审批与挂载摘卡必须是管理员 —— 这与前端隐藏按钮是两道独立的关卡
	case strings.HasPrefix(p, "/api/gpu-bookings/") &&
		(strings.HasSuffix(p, "/decision") ||
			strings.HasSuffix(p, "/attach") ||
			strings.HasSuffix(p, "/detach")):
		return levelAdmin
	// 抹掉一条预约：会连留痕一起抹，只能管理员做。
	// 这里按 method 判而不是按后缀 —— DELETE 的路径与 POST /cancel 长得几乎一样
	case method == http.MethodDelete && strings.HasPrefix(p, "/api/gpu-bookings/"):
		return levelAdmin
	}
	return levelUser
}

// ---------------------------------------------------------------- 健康

type healthResp struct {
	OK                 bool   `json:"ok"`
	Version            string `json:"version"`
	IncusReachable     bool   `json:"incus_reachable"`
	InstanceCount      int    `json:"instance_count"`
	LastReconcileAgeMs int64  `json:"last_reconcile_age_ms"`
	Error              string `json:"error,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	views := s.cfg.Recon.Views()
	lastOK := s.cfg.Recon.LastOK()
	lastErr := s.cfg.Recon.LastErr()

	var age int64 = -1
	if !lastOK.IsZero() {
		age = time.Since(lastOK).Milliseconds()
	}
	reachable := lastErr == "" && !lastOK.IsZero() && age >= 0 && age < s.cfg.MaxAge.Milliseconds()

	resp := healthResp{
		OK:                 reachable,
		Version:            s.cfg.Version,
		IncusReachable:     reachable,
		InstanceCount:      len(views),
		LastReconcileAgeMs: age,
		Error:              lastErr,
	}
	status := http.StatusOK
	if !reachable {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, resp)
}

// ---------------------------------------------------------------- 认证

// redeemRequest 是登录页提交的兑换请求。
// device 由浏览器指纹折算而来，用途只有一个：让管理员在会话台账里分清哪台是哪台。
type redeemRequest struct {
	Token  string `json:"token"`
	Device string `json:"device"`
}

// loginRequest 是管理员口令登录的请求体。
// 口令不 trim：前后空格本身可能是口令的一部分，
// 但空串与纯空格不该放行，那通常意味着前端没接到输入。
type loginRequest struct {
	Password string `json:"password"`
	Device   string `json:"device"`
}

type userBrief struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type sessionBrief struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	MaxIdle    string `json:"max_idle"`
	RemainSec  int64  `json:"remain_seconds"`
	Device     string `json:"device"`
}

type defaultsBrief struct {
	TokenTTL string `json:"token_ttl"`
	MaxIdle  string `json:"max_idle"`
}

// handleRedeem 把一次性凭证换成会话。
//
// 用 POST 而不是 GET：凭证在 URL 里会被浏览器预加载、写进历史与 Referer，
// 转发给同事的链接等于把凭据送给了每一个中间节点。流程因此分成两步 ——
// 链接把使用者送到登录页，页面再主动 POST 兑换。
func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	var req redeemRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少登录凭证"})
		return
	}

	ip := s.clientIP(r)
	user, sess, err := s.cfg.Auth.Redeem(req.Token, ip, clamp(r.UserAgent(), 240), clamp(req.Device, 64))
	if err != nil {
		var th *auth.Throttled
		if errors.As(err, &th) {
			w.Header().Set("Retry-After", strconv.Itoa(int(th.Retry.Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
		// 失败原因不分流：凭证不存在、已用过、已过期、成员被停用，
		// 对外一律是同一句「无效」，否则这里就成了探测凭证有效性的地方。
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "登录凭证无效或已失效"})
		return
	}

	s.setSessionCookie(w, sess)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":    userBrief{ID: user.ID, Name: user.Name, Role: user.Role},
		"session": briefOf(sess),
	})
}

// handleLogin 是管理员的口令登录。
//
// 控制台只认这一种口令：能用口令进来的只有管理员。
// 其他人（租户、同事）没有口令，他们只能拿管理员签发的链接进来（handleRedeem）。
// 因此这里不做「按口令查出是哪个人」这一步 —— 口令只对应管理员这一个身份，
// 而具体是谁在用，靠会话台账上的设备与时间分辨。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}
	if req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少口令"})
		return
	}

	ip := s.clientIP(r)
	user, sess, err := s.cfg.Auth.Login(req.Password, ip, clamp(r.UserAgent(), 240), clamp(req.Device, 64))
	if err != nil {
		var th *auth.Throttled
		if errors.As(err, &th) {
			w.Header().Set("Retry-After", strconv.Itoa(int(th.Retry.Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	s.setSessionCookie(w, sess)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":    userBrief{ID: user.ID, Name: user.Name, Role: user.Role},
		"session": briefOf(sess),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		s.cfg.Auth.Logout(c.Value)
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "已退出"})
}

// handleSession 是前端每次进入页面都要问的一句「我是谁」。
//
// 它是公开的：未登录也要能拿到应答，才会知道该显示登录页。
// 附带效果是心跳 —— 登录着的人每隔一段时间拉它，会话的最后活跃时间就跟着走，
// 会话台账上的「在线 / 离线」因此不需要额外的心跳接口。
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	ttl, idle := s.cfg.Auth.Defaults()
	resp := map[string]any{
		"authenticated": false,
		"role":          "",
		"defaults":      defaultsBrief{TokenTTL: durationText(ttl), MaxIdle: durationText(idle)},
	}
	view, state := s.current(r)
	if state != store.SessionValid {
		if state != store.SessionMissing {
			resp["reason"] = state.String()
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["authenticated"] = true
	resp["role"] = view.Role
	resp["user"] = userBrief{ID: view.UserID, Name: view.UserName, Role: view.Role}
	resp["session"] = sessionBrief{
		ID:         view.ID,
		CreatedAt:  view.CreatedAt,
		LastSeenAt: view.LastSeenAt,
		ExpiresAt:  view.ExpiresAt,
		MaxIdle:    view.MaxIdle,
		RemainSec:  view.RemainSec,
		Device:     view.Device,
	}
	writeJSON(w, http.StatusOK, resp)
}

// setSessionCookie 把会话 ID 写进 cookie。
// MaxAge 与服务端记录的到期时间对齐：浏览器不必保留一条到时间已经作废的 cookie。
func (s *Server) setSessionCookie(w http.ResponseWriter, sess store.Session) {
	age := int(time.Until(parseRFC3339(sess.ExpiresAt)).Seconds())
	if age < 0 {
		age = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    sess.ID,
		Path:     "/",
		MaxAge:   age,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure 只在 HTTPS 下打开：本机直曝的场景还要能登得进去。
		// 一旦前面挂了 TLS，务必设 NEO_SECURE_COOKIE=1 —— 明文 HTTP 下发会话等于把它半公开。
		Secure: s.cfg.SecureCookie,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.SecureCookie,
	})
}

func briefOf(s store.Session) sessionBrief {
	// -1 表示长期有效，与 0（此刻到期）必须是两个值：
	// 前端要能把「没有到期这回事」和「马上到期」区分开。
	remain := int64(-1)
	if exp := parseRFC3339(s.ExpiresAt); !exp.IsZero() {
		remain = int64(time.Until(exp).Seconds())
		if remain < 0 {
			remain = 0
		}
	}
	return sessionBrief{
		ID:         s.ID,
		CreatedAt:  s.CreatedAt,
		LastSeenAt: s.LastSeenAt,
		ExpiresAt:  s.ExpiresAt,
		MaxIdle:    s.MaxIdle,
		RemainSec:  remain,
		Device:     s.Device,
	}
}

// clamp 截断来自客户端的自由文本：UA 与设备标签都要显示进台账，
// 不设上限的话，一条超长字符串就能把表格撑坏。
func clamp(v string, n int) string {
	v = strings.TrimSpace(v)
	if len([]rune(v)) > n {
		return string([]rune(v)[:n])
	}
	return v
}

// ---------------------------------------------------------------- 实例

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	views := s.cfg.Recon.Views()
	// 每台实例附上「登记在谁名下」。没有它，租户点一台别人的机器被拒时
	// 只看到一句「只能操作自己名下的实例」，却看不出这台机器归谁 ——
	// 那种拒绝看起来像 bug。而宿主上确实存在台账之外手工建的实例，
	// 它们的属主是空的，页面上要敢把它显示成「未登记」。
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		item := map[string]any{}
		raw, err := json.Marshal(v)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "序列化实例失败"})
			return
		}
		_ = json.Unmarshal(raw, &item)
		item["owner"] = s.instanceOwner(v.Name)
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func (s *Server) handleInstance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	for _, v := range s.cfg.Recon.Views() {
		if v.Name == name {
			writeJSON(w, http.StatusOK, map[string]any{
				"instance": v,
				"series":   s.cfg.Recon.Series(name),
			})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "实例不存在"})
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	writeJSON(w, http.StatusOK, map[string]any{"series": s.cfg.Recon.Series(name)})
}

// ---------------------------------------------------------------- 容量

type capacityResp struct {
	Instances int `json:"instances"`
	Running   int `json:"running"`
	CPU       struct {
		Total     int `json:"total"`
		Allocated int `json:"allocated"`
	} `json:"cpu"`
	Memory struct {
		Total     int64 `json:"total"`
		Used      int64 `json:"used"`
		Allocated int64 `json:"allocated"`
	} `json:"memory"`
	Disk struct {
		Allocated int64 `json:"allocated"`
	} `json:"disk"`
}

func (s *Server) handleCapacity(w http.ResponseWriter, r *http.Request) {
	views := s.cfg.Recon.Views()
	var resp capacityResp
	for _, v := range views {
		resp.Instances++
		if v.Running {
			resp.Running++
		}
		resp.CPU.Allocated += v.QuotaCPUNum()
		resp.Memory.Allocated += v.QuotaMemoryBytes()
		resp.Disk.Allocated += v.QuotaDiskBytes()
	}
	if res := s.cfg.Recon.Resources(); res != nil {
		resp.CPU.Total = res.CPU.Total
		resp.Memory.Total = res.Memory.Total
		resp.Memory.Used = res.Memory.Used
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------- 静态资源

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "接口不存在"})
		return
	}
	// 前端产物未构建时给出可操作的提示，而不是白屏
	if s.cfg.WebDir == "" {
		writeHTML(w, http.StatusOK, waitingPage())
		return
	}

	clean := path.Clean("/" + r.URL.Path)
	root := filepath.Clean(s.cfg.WebDir)
	full := filepath.Join(root, clean)
	// 目录穿越防护：必须落在 web 目录内部或就是它本身
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "非法路径"})
		return
	}
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		// 带 hash 的产物可以长缓存，其余一律不缓存以免升级后拿到旧页面
		if strings.HasPrefix(clean, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		http.ServeFile(w, r, full)
		return
	}

	// SPA 兜底：未命中的路径回落到首页，前端加路由不需要改后端
	index := filepath.Join(s.cfg.WebDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		writeHTML(w, http.StatusOK, waitingPage())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, index)
}

// waitingPage 在前端产物缺失时给出明确的下一步，而不是让用户对着白屏猜。
func waitingPage() string {
	return `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<title>轻量云主机控制台</title>
<style>
 body{font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;
      display:flex;align-items:center;justify-content:center;height:100vh;margin:0;
      background:#0f1216;color:#e6e9ef}
 .box{max-width:34rem;padding:2rem 2.5rem;border:1px solid #2a3138;border-radius:12px;background:#151a20}
 code{background:#1e242b;padding:.15rem .4rem;border-radius:4px;color:#7fd1d3}
 h1{font-size:1.1rem;margin:0 0 1rem}
 p{line-height:1.7;color:#9aa4af;font-size:.9rem}
</style></head><body><div class="box">
<h1>控制台后端已就绪，前端产物未构建</h1>
<p>服务正在运行，只是找不到 <code>web/dist</code>。在项目目录执行：</p>
<p><code>make web</code></p>
<p>然后重新构建镜像即可看到控制台页面。</p>
</div></body></html>`
}

// ---------------------------------------------------------------- 工具

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTML(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// clientIP 取请求来源。只有显式声明了信任反向代理时才读 X-Forwarded-For，
// 否则一律以 TCP 连接的对端为准 —— 会话台账上的 IP 是管理员做判断的依据，
// 它必须是可以拿去问「这台机器是谁」的那种地址，而不是客户端随口说的一个字符串。
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			return strings.TrimSpace(strings.Split(fwd, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func parseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
