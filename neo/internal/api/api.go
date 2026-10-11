// Package api 提供 REST 与前端静态托管。
//
// 健康理念沿用 obs：进程活着不等于健康。/api/health 会带上
// 「最近一次成功对账距今多久」，超过容忍窗口就返回 503。
package api

import (
	"encoding/json"
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
	"neo/internal/limit"
	"neo/internal/recon"
	"neo/internal/store"
)

// Config 是 API 层依赖。
type Config struct {
	Version    string
	Auth       *auth.Auth
	Recon      *recon.Loop
	Catalog    catalog.Catalog
	Apps       *store.AppStore
	// ApplyLimit 限制「成功提交」的单数，保护管理员待办不被灌水
	ApplyLimit *limit.Limiter
	// ApplyFailLimit 限制失败尝试次数，防止脚本反复试探主机名是否存在。
	// 两者分开是因为语义不同：单据额度不该被填错表单消耗掉，
	// 而失败也必须有限额，否则 /api/applications 就成了主机名枚举器。
	ApplyFailLimit *limit.Limiter
	GPUTotal       int
	WebDir     string
	DataDir    string
	SessionTTL time.Duration
	MaxAge     time.Duration // 判定健康的最大对账延迟
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
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/session", s.handleSession)

	// 公开区：产品页要能在未登录状态下读目录、提交申请
	s.mux.HandleFunc("GET /api/catalog", s.handleCatalog)
	s.mux.HandleFunc("POST /api/applications", s.handleApply)
	s.mux.HandleFunc("GET /api/applications/{id}", s.handleApplicationGet)

	// 管理员区：查看全部申请与处理动作
	s.mux.HandleFunc("GET /api/applications", s.handleApplications)
	s.mux.HandleFunc("POST /api/applications/{id}/decision", s.handleDecision)

	s.mux.HandleFunc("GET /api/instances", s.handleInstances)
	s.mux.HandleFunc("GET /api/instances/{name}", s.handleInstance)
	s.mux.HandleFunc("GET /api/series/{name}", s.handleSeries)
	s.mux.HandleFunc("GET /api/capacity", s.handleCapacity)
	s.mux.HandleFunc("/", s.handleStatic)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if !isPublic(r.Method, r.URL.Path) && !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

// isPublic 列出无需鉴权的接口。判定粒度细到方法而非路径：
// GET /api/applications 是管理员看全量，POST 才是匿名提交，同路径不同权限。
func isPublic(method, p string) bool {
	switch p {
	case "/api/health", "/api/session":
		return true
	case "/api/auth/login":
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

func (s *Server) authorized(r *http.Request) bool {
	c, err := r.Cookie(auth.CookieName)
	if err != nil || c.Value == "" {
		return false
	}
	claims, ok := s.cfg.Auth.Verify(c.Value)
	return ok && claims.Role == auth.RoleAdmin
}

// ---------------------------------------------------------------- 健康

type healthResp struct {
	OK               bool   `json:"ok"`
	Version          string `json:"version"`
	IncusReachable   bool   `json:"incus_reachable"`
	InstanceCount    int    `json:"instance_count"`
	LastReconcileAgeMs int64 `json:"last_reconcile_age_ms"`
	Error            string `json:"error,omitempty"`
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

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	src := clientIP(r)
	if ok, retry := s.cfg.Auth.Limiter().Allow(src); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "尝试过于频繁，请稍后再试"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		s.cfg.Auth.Limiter().Record(src)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}
	if req.Password == "" || !s.cfg.Auth.CheckPassword(req.Password) {
		s.cfg.Auth.Limiter().Record(src)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "口令不正确"})
		return
	}

	tok, err := s.cfg.Auth.Issue(auth.RoleAdmin, s.cfg.SessionTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "签发会话失败"})
		return
	}
	s.cfg.Auth.Limiter().Reset(src)
	// 明文口令文件不再在登录时销毁：口令文件的留存与删除交由管理员自行处理，
	// 方便其在需要时随时查看，而不是首次登录就被动销毁。

	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    tok,
		Path:     "/",
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"role": auth.RoleAdmin,
		"ttl":  int(s.cfg.SessionTTL.Seconds()),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "已退出"})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	authed := s.authorized(r)
	resp := map[string]any{"authenticated": authed, "role": ""}
	if authed {
		resp["role"] = auth.RoleAdmin
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------- 实例

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"instances": s.cfg.Recon.Views(),
	})
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
		Total      int64 `json:"total"`
		Used       int64 `json:"used"`
		Allocated  int64 `json:"allocated"`
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

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
