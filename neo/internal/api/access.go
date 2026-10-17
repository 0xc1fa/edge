// 访问控制相关接口：成员、一次性登录凭证、会话台账。
//
// 这一组接口把「谁能进来」这件事从后台的一把口令变成可以在页面上管理的对象，
// 并且全都只认管理员（见 requiredLevel）。
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"neo/internal/store"
)

// ---------------------------------------------------------------- 成员

type userView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	Note        string `json:"note"`
	TokenTTL    string `json:"token_ttl"`
	MaxIdle     string `json:"max_idle"`
	EffTokenTTL string `json:"effective_token_ttl"`
	EffMaxIdle  string `json:"effective_max_idle"`
	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at"`
	// 下面三个计数是为了让列表本身就回答「这个人现在占着几个会话」，
	// 管理员不必点进会话台账去数
	Sessions       int `json:"sessions"`
	Online         int `json:"online"`
	PendingInvites int `json:"pending_invites"`
}

// durationText 把时限说成人读的话。0 是长期有效，直说「长期」，
// 而不是丢一个「0s」让人怀疑是不是配错了。
func durationText(d time.Duration) string {
	if d <= 0 {
		return "长期"
	}
	return d.String()
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	ttl, idle := s.cfg.Auth.Defaults()
	users := s.cfg.Users.ListUsers()
	sessions := s.cfg.Users.ListSessions()
	invites := s.cfg.Users.ListInvites()

	out := make([]userView, 0, len(users))
	for _, u := range users {
		out = append(out, s.userToView(u, sessions, invites))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"users": out,
		"defaults": defaultsBrief{
			TokenTTL: durationText(ttl),
			MaxIdle:  durationText(idle),
		},
	})
}

// userToView 拼出一个成员的视图。
// 会话数从会话台账实时统计而不是缓存：人数个位数，每帧算一遍的代价可以忽略，
// 换来的却是「改了时限、踢了会话之后列表立刻同步」。
func (s *Server) userToView(u store.User, sessions []store.SessionView, invites []store.Invite) userView {
	v := userView{
		ID:          u.ID,
		Name:        u.Name,
		Role:        u.Role,
		Status:      u.Status,
		Note:        u.Note,
		TokenTTL:    u.TokenTTL,
		MaxIdle:     u.MaxIdle,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
	// 生效时限按这个人自己来算（含回落到全局默认的部分）。
	// 之前这里返回的是全局默认，于是「张三设了 8 小时」在列表上会显示成
	// 和别人一样的 12 小时 —— 管理员改完看不到结果，就会以为没改成功。
	ttl, idle := s.cfg.Auth.Effective(u)
	v.EffTokenTTL, v.EffMaxIdle = durationText(ttl), durationText(idle)
	now := time.Now()
	for _, sess := range sessions {
		if sess.UserID != u.ID {
			continue
		}
		if sess.Expired {
			continue
		}
		v.Sessions++
		if sess.Online {
			v.Online++
		}
	}
	for _, inv := range invites {
		pending := inv.UserID == u.ID && inv.UsedAt == "" && inv.RevokedAt == "" && now.Before(parseRFC3339(inv.ExpiresAt))
		if pending {
			v.PendingInvites++
		}
	}
	return v
}

type userCreateRequest struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Note     string `json:"note"`
	TokenTTL string `json:"token_ttl"`
	MaxIdle  string `json:"max_idle"`
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var req userCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}
	u, err := s.cfg.Users.CreateUser(store.UserInput{
		Name:     clamp(req.Name, 32),
		Role:     req.Role,
		Note:     clamp(req.Note, 200),
		TokenTTL: strings.TrimSpace(req.TokenTTL),
		MaxIdle:  strings.TrimSpace(req.MaxIdle),
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.userToView(u, nil, s.cfg.Users.ListInvites())})
}

// userUpdateRequest 里的语义要注意：role / token_ttl / max_idle 三者在
// 结构里保留了是否传值的区分能力 —— 时限可以清空（回到跟随全局），
// 而 role 不传表示不改。所以这里用指针。
type userUpdateRequest struct {
	Name     *string `json:"name"`
	Role     *string `json:"role"`
	Note     *string `json:"note"`
	TokenTTL *string `json:"token_ttl"`
	MaxIdle  *string `json:"max_idle"`
	Status   *string `json:"status"`
	// ApplyNow 为 true 时，新的时限也会重算该成员名下已在线会话的到期时间。
	// 默认不这么做：只影响后续登录是多数管理员想要的语义，
	// 而「把某人立刻踢下线」应该是一次明确的操作。
	ApplyNow *bool `json:"apply_now"`
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.cfg.Users.GetUser(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "成员不存在"})
		return
	}

	var req userUpdateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不正确"})
		return
	}

	// 停用 / 启用单独走 SetUserStatus：它要连带吊销会话与凭证，
	// 不是一次普通的字段更新。
	if req.Status != nil {
		// 不能停用自己的账号：这一按下去自己也在下一跳被踢出去，
		// 而且此时页面已经进不来，没人能把自己重新启用回来（只剩终端那条后路）。
		if me, state := s.current(r); state == store.SessionValid && me.UserID == id {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不能停用自己的账号"})
			return
		}
		u, err := s.cfg.Users.SetUserStatus(id, *req.Status)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": s.userToView(u, s.cfg.Users.ListSessions(), s.cfg.Users.ListInvites())})
		return
	}

	in := store.UserInput{}
	if req.Name != nil {
		in.Name = clamp(*req.Name, 32)
	}
	if req.Role != nil {
		in.Role = *req.Role
	}
	if req.Note != nil {
		in.Note = clamp(*req.Note, 200)
	}
	// UpdateUser 认为空串是「不改」，所以真正的清理动作必须走 UpdateUserTTL
	if req.TokenTTL != nil || req.MaxIdle != nil {
		ttl, idle := "", ""
		if req.TokenTTL != nil {
			ttl = strings.TrimSpace(*req.TokenTTL)
		}
		if req.MaxIdle != nil {
			idle = strings.TrimSpace(*req.MaxIdle)
		}
		u, err := s.cfg.Users.UpdateUserTTL(id, ttl, idle)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		applied := 0
		if req.ApplyNow != nil && *req.ApplyNow {
			applied = s.cfg.Auth.ApplyTTL(u)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"user":    s.userToView(u, s.cfg.Users.ListSessions(), s.cfg.Users.ListInvites()),
			"applied": applied,
		})
		return
	}

	u, err := s.cfg.Users.UpdateUser(id, in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.userToView(u, s.cfg.Users.ListSessions(), s.cfg.Users.ListInvites())})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// 不允许删掉自己：否则页面会在下一次请求上把自己锁在门外，
	// 而且没有任何提示能解释发生了什么。
	if view, state := s.current(r); state == store.SessionValid && view.UserID == id {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "不能删除当前登录的成员"})
		return
	}
	if err := s.cfg.Users.DeleteUser(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "已删除"})
}

// handleUserRevokeSessions 一次性踢掉某成员名下全部会话。
// 单独成一个动作而不是藏在改资料里：踢人下线要能被明确地按下一次。
func (s *Server) handleUserRevokeSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.cfg.Users.GetUser(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "成员不存在"})
		return
	}
	n := s.cfg.Auth.RevokeUserSessions(id)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// ---------------------------------------------------------------- 登录凭证

type inviteView struct {
	Token     string `json:"token"`
	Masked    bool   `json:"masked"`
	UserID    string `json:"user_id"`
	UserName  string `json:"user_name"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	RemainSec int64  `json:"remain_seconds"`
	Status    string `json:"status"`
}

// 凭证状态。四种里只有 valid 能够兑换。
const (
	inviteValid   = "valid"
	inviteUsed    = "used"
	inviteRevoked = "revoked"
	inviteExpired = "expired"
)

// inviteToView 组装凭证视图。
//
// 失去兑换资格的凭证一律只吐前 6 位：台账文件里存着明文 token，
// 是为了让管理员能在签发之后回头再看一眼；而一旦它已经用掉，
// 这份明文就只剩下风险，于是接口上不再给出完整值。
func (s *Server) inviteToView(inv store.Invite, now time.Time) inviteView {
	v := inviteView{
		Token:     inv.Token,
		UserID:    inv.UserID,
		CreatedBy: inv.CreatedBy,
		CreatedAt: inv.CreatedAt,
		ExpiresAt: inv.ExpiresAt,
		Status:    inviteValid,
	}
	if u, ok := s.cfg.Users.GetUser(inv.UserID); ok {
		v.UserName = u.Name
	}
	v.RemainSec = int64(parseRFC3339(inv.ExpiresAt).Sub(now).Seconds())
	if v.RemainSec < 0 {
		v.RemainSec = 0
	}
	switch {
	case inv.UsedAt != "":
		v.Status = inviteUsed
	case inv.RevokedAt != "":
		v.Status = inviteRevoked
	case now.After(parseRFC3339(inv.ExpiresAt)):
		v.Status = inviteExpired
	}
	if v.Status != inviteValid {
		v.Masked = true
		if len(inv.Token) > 6 {
			v.Token = inv.Token[:6] + "…"
		}
	}
	return v
}

type inviteCreateRequest struct {
	TTLSeconds int `json:"ttl_seconds"`
}

// inviteIssueRequest 是「按名字签发」的请求体：名字必填，时长可省（取默认）
type inviteIssueRequest struct {
	Name       string `json:"name"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// handleInviteCreate 签发凭证。
//
// 返回体里带上 users（用于创建者显示）与 token —— 管理员要在页面上复制整条链接，
// 这是它唯一一次、也是最后一次以完整形态出现。
func (s *Server) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, ok := s.cfg.Users.GetUser(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "成员不存在"})
		return
	}
	if u.Status != store.UserStatusActive {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该成员已停用，先启用再签发"})
		return
	}

	var req inviteCreateRequest
	// 没有 body 也算合法：直接按默认时长签发，避免为一个可选参数多一次往返
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req)

	var createdBy string
	if view, state := s.current(r); state == store.SessionValid {
		createdBy = view.UserID
	}
	inv, err := s.cfg.Auth.IssueInvite(id, createdBy, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"invite": s.inviteToView(inv, time.Now()),
		"user":   userBrief{ID: u.ID, Name: u.Name, Role: u.Role},
	})
}

// handleInviteIssue 按名字签发一条访问链接。
//
// 控制台上不再摆成员管理，所以「让某人进来」这个动作收敛成这里的一次填写：
// 管理员写下使用者的名字，拿到链接，发过去。名字只是台账上的一个标签，
// 用来回答「这条会话是谁的」，不是账号，也不参与任何判定。
//
// 因此租户侧始终没有口令：他能不能进来，取决于管理员有没有批过这一条。
func (s *Server) handleInviteIssue(w http.ResponseWriter, r *http.Request) {
	var req inviteIssueRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req)
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请填写使用者的名字"})
		return
	}

	u, err := s.cfg.Users.EnsureMember(req.Name, store.RoleMember)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var createdBy string
	if view, state := s.current(r); state == store.SessionValid {
		createdBy = view.UserID
	}
	inv, err := s.cfg.Auth.IssueInvite(u.ID, createdBy, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"invite": s.inviteToView(inv, time.Now()),
		"user":   userBrief{ID: u.ID, Name: u.Name, Role: u.Role},
	})
}

func (s *Server) handleInvites(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	items := s.cfg.Users.ListInvites()
	out := make([]inviteView, 0, len(items))
	valid := 0
	for _, inv := range items {
		v := s.inviteToView(inv, now)
		if v.Status == inviteValid {
			valid++
		}
		// 只回最近 50 条：凭证是消耗品，
		// 台账的作用是让人处理「还没用的」和「刚用掉的」，不是做历史审计
		if len(out) < 50 {
			out = append(out, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out, "valid": valid})
}

func (s *Server) handleInviteRevoke(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if err := s.cfg.Users.RevokeInvite(token); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "已吊销"})
}

// ---------------------------------------------------------------- 会话

// handleSessions 返回实时会话台账。
//
// 「实时」靠两件事：一是 this 接口被前端短间隔轮询，二是每次 /api/session
// 都会刷新会话的最后活跃时间（心跳）。因此这里看到的 online 是真实的心跳结果，
// 而不是「这条 cookie 还没过期」。
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	items := s.cfg.Users.ListSessions()
	me, state := s.current(r)
	online := 0
	for _, v := range items {
		if v.Online && !v.Expired {
			online++
		}
	}
	resp := map[string]any{
		"sessions": items,
		"online":   online,
		"total":    len(items),
	}
	if state == store.SessionValid {
		resp["current"] = me.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.cfg.Auth.RevokeSession(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "已下线"})
}

// handleSessionDelete 把会话从台账里抹掉。
//
// 这里刻意不挑状态：失效的、所属成员已删除的、三个月前的都能删。
// 台账回答的是「现在谁还登着」，堆着一堆已经不能用的记录只会让人看错重点。
// 删除自己正在用的那条，等价于把自己注销 —— 下一跳就会落到登录页。
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.cfg.Auth.DeleteSession(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "已删除"})
}
