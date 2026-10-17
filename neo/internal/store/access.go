// 访问与身份台账：用户、一次性登录凭证、会话。
//
// 为什么没有口令字段：口令是长期秘密，围绕它的全部成本（遗忘、复用、泄露后
// 重置、还要担心被撞库）正是这一版要消灭的东西。替代它的是「一次性凭证」：
// 动态签发、单次使用、自带失效时间、可单独吊销。使用者不需要记住任何东西，
// 管理员不需要替人重置 —— 忘了就再发一条。
//
// 为什么会话必须落服务端：「管理员能改 Token 时限」「能实时看到谁在哪台机器上」
// 这两件事都要求会话是可列举、可修改、可吊销的对象。自签名的无状态 token
// 一旦签发就改不了寿命、踢不掉、也数不出来，所以这里换成随机 ID + 服务端台账。
// 代价是多一张表，收益是把会话的管理权交还给管理员。
package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// usersFile 是用户与凭证台账，改动低频，每次变更立即落盘。
// sessionsFile 是会话台账，心跳会高频刷新它，因此延迟落盘（见 Loop）。
const (
	usersFile    = "users.json"
	sessionsFile = "sessions.json"
	// BootstrapFile 是首发凭证的落盘文件。用法与当年的 bootstrap-password.txt 一致：
	// 写一份明文到数据目录，由管理员 cat 出来用，用完删掉。
	// 区别是它兑换一次即废，不必长期替一个秘密担心被翻出来。
	BootstrapFile = "bootstrap-invite.txt"
)

// WriteBootstrapInvite 把首发凭证的文本写到数据目录，权限 0600。
func WriteBootstrapInvite(dir, content string) error {
	return os.WriteFile(filepath.Join(dir, BootstrapFile), []byte(content+"\n"), 0o600)
}

// 角色只有两级：管理员拥有全部权限；成员只读与查看，改动作留给管理员。
// 这里刻意不做租户：同租户内的资源隔离要等数据模型稳定，先把身份与会话跑通。
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// 用户状态。停用是「立刻生效」的：存量会话在同一时刻全部失效。
const (
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
)

// OnlineWindow 判定会话是否在线。
// 心跳间隔远小于它，于是翻页、轮询都算在线；只有真的离开一阵才转离线。
const OnlineWindow = 90 * time.Second

// User 是一个访问主体。它没有凭据字段 —— 登录靠刷Alerts一次性凭证。
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	// Status 为 disabled 时，其名下的会话在下一次校验时立刻失效
	Status string `json:"status"`
	Note   string `json:"note"`
	// TokenTTL / MaxIdle 是 Go 的 duration 串（如 "8h"、"30m"）。
	// 留空表示跟随全局默认值，这样单人调时限不必全员跟着改。
	TokenTTL string `json:"token_ttl"`
	MaxIdle  string `json:"max_idle"`

	CreatedAt   string `json:"created_at"`
	LastLoginAt string `json:"last_login_at"`
}

// Invite 是一次性登录凭证。
//
// token 明文落盘是为了让管理员能在签发之后回头再看一眼 —— 它被人 IM 转发后就丢了。
// 为了控制这份明文的价值：凭证单次使用、有时限、可吊销；
// 一旦用过，列表接口就不再吐完整 token（见 InviteView）。
type Invite struct {
	Token     string `json:"token"`
	UserID    string `json:"user_id"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	UsedAt    string `json:"used_at,omitempty"`
	UsedIP    string `json:"used_ip,omitempty"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

// Session 是一次登录在服务端留下的痕迹。
type Session struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// CreatedAt 是签发时刻；ExpiresAt = CreatedAt + 该用户的 TokenTTL。
	//
	// ExpiresAt 为空表示长期有效 —— 这是默认形态。
	//
	// 默认给「长期」而不是给「12 小时」，是因为内部控制台的访问者就那么几个人：
	// 到期把人踢出去带来的麻烦比它挡住的风险更多，而页面上的会话台账是可读的 ——
	// 谁在台上、什么时候来的、要不要清掉，管理员随时能看见也能动手。
	// 换句话说：把「什么时候失效」交给管理员决定，而不是替他决定。
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	// MaxIdle 为空表示不设空闲上限。空与非空要能分清：
	// 「没设」是长期，「设了到期」才有秒数，零值永远不代表「立刻过期」。
	MaxIdle   string `json:"max_idle"`
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
	// Device 是浏览器指纹折算出的设备标签，只用于让管理员分辨「哪一台」，
	// 它从不参与身份判定：指纹会漂移也会撞车，拿它认人迟早出事。
	Device    string `json:"device"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

// UserInput 是管理员提交的用户信息。
type UserInput struct {
	Name     string
	Role     string
	Note     string
	TokenTTL string
	MaxIdle  string
}

// Access 是用户、凭证、会话的统一入口。
type Access struct {
	mu       sync.Mutex
	dir      string
	users    []User
	invites  []Invite
	sessions []Session
	// sessionsDirty 标记会话表被改过但还没落盘。
	// 心跳每几秒就来一次，逐次重写整个文件没必要：内存才是权威，
	// 落盘只为了重启后不必所有人重新登录。
	sessionsDirty bool
}

// NewAccess 载入数据目录里的用户与会话台账。
func NewAccess(dir string) (*Access, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	a := &Access{dir: dir}
	if err := a.loadUsers(); err != nil {
		return nil, err
	}
	if err := a.loadSessions(); err != nil {
		return nil, err
	}
	return a, nil
}

// usersFileDoc 是台账文件的形状：用户与凭证同源、同生命周期，放一个文件里，
// 管理员手工排障时不必在两个文件之间对账。
type usersFileDoc struct {
	Users   []User   `json:"users"`
	Invites []Invite `json:"invites"`
}

func (a *Access) loadUsers() error {
	p := filepath.Join(a.dir, usersFile)
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取用户台账失败: %w", err)
	}
	var doc usersFileDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("用户台账解析失败，请人工核对 %s: %w", p, err)
	}
	a.users, a.invites = doc.Users, doc.Invites
	return nil
}

func (a *Access) loadSessions() error {
	p := filepath.Join(a.dir, sessionsFile)
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取会话台账失败: %w", err)
	}
	if err := json.Unmarshal(raw, &a.sessions); err != nil {
		// 会话只是缓存性质的东西：文件坏了不该让服务起不来，
		// 丢了最多是所有人都重新登一次。所以这里容忍，但要留下确凿的日志。
		fmt.Fprintf(os.Stderr, "会话台账损坏已忽略（所有存活会话将失效）: %v\n", err)
		a.sessions = nil
	}
	return nil
}

// ---------------------------------------------------------------- 用户

// ListUsers 返回全部用户，管理员在前，其余按名字排。
func (a *Access) ListUsers() []User {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]User, len(a.users))
	copy(out, a.users)
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := out[i].Role == RoleAdmin, out[j].Role == RoleAdmin; ri != rj {
			return ri
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// CountUsers 供引导流程判断「还是不是一张白纸」。
func (a *Access) CountUsers() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.users)
}

// GetUser 按 ID 查。
func (a *Access) GetUser(id string) (User, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range a.users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

// ErrNameTaken 是重名错误，调用方据此给出「换个名字」的提示。
var ErrNameTaken = errors.New("同名成员已存在")

// CreateUser 建立用户并立即落盘。
func (a *Access) CreateUser(in UserInput) (User, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return User{}, errors.New("成员名称不能为空")
	}
	if len([]rune(name)) > 32 {
		return User{}, errors.New("成员名称不超过 32 个字符")
	}
	role := in.Role
	if role != RoleAdmin && role != RoleMember {
		role = RoleMember
	}
	if msg := checkDurations(in.TokenTTL, in.MaxIdle); msg != "" {
		return User{}, errors.New(msg)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range a.users {
		if strings.EqualFold(strings.TrimSpace(u.Name), name) {
			return User{}, ErrNameTaken
		}
	}
	u := User{
		ID:        newID("u"),
		Name:      name,
		Role:      role,
		Status:    UserStatusActive,
		Note:      strings.TrimSpace(in.Note),
		TokenTTL:  strings.TrimSpace(in.TokenTTL),
		MaxIdle:   strings.TrimSpace(in.MaxIdle),
		CreatedAt: nowISO(),
	}
	a.users = append(a.users, u)
	if err := a.saveUsersLocked(); err != nil {
		a.users = a.users[:len(a.users)-1]
		return User{}, err
	}
	return u, nil
}

// EnsureMember 按名字取成员，没有就新建一个。
//
// 界面上不再摆成员管理之后，签发访问链接只需要一个名字：
// 同名复用同一条记录，不同名就顺手建一个。
// 管理员不该为了「让某人进来」先去某个页面建档、再回另一个页面发证 ——
// 那两步之间的那道门槛，正是他每次都要多花的力气。
//
// 名字在这里只是台账上的一个标签，用来回答「这条会话是谁的」，
// 它不是身份凭证，也不参与任何判定。
func (a *Access) EnsureMember(name string, role string) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return User{}, errors.New("请填写使用者的名字")
	}
	if len([]rune(name)) > 32 {
		return User{}, errors.New("名字不超过 32 个字符")
	}
	if role != RoleAdmin && role != RoleMember {
		role = RoleMember
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range a.users {
		if !strings.EqualFold(strings.TrimSpace(u.Name), name) {
			continue
		}
		if u.Status != UserStatusActive {
			return User{}, errors.New("该成员已停用")
		}
		return u, nil
	}
	u := User{
		ID:        newID("u"),
		Name:      name,
		Role:      role,
		Status:    UserStatusActive,
		CreatedAt: nowISO(),
	}
	a.users = append(a.users, u)
	if err := a.saveUsersLocked(); err != nil {
		a.users = a.users[:len(a.users)-1]
		return User{}, err
	}
	return u, nil
}

// UpdateUser 局部更新用户资料。空串表示「这一项不改」，
// 所以「把名字清空」是不可能出现的意图；需要清空备注或时限请走下面两个专用方法。
func (a *Access) UpdateUser(id string, in UserInput) (User, error) {
	if msg := checkDurations(in.TokenTTL, in.MaxIdle); msg != "" {
		return User{}, errors.New(msg)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.users {
		if a.users[i].ID != id {
			continue
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = a.users[i].Name
		}
		for j, u := range a.users {
			if j != i && strings.EqualFold(strings.TrimSpace(u.Name), name) {
				return User{}, ErrNameTaken
			}
		}
		prev := a.users[i]
		if name != "" {
			a.users[i].Name = name
		}
		if in.Role == RoleAdmin || in.Role == RoleMember {
			a.users[i].Role = in.Role
		}
		if in.Note != "" {
			a.users[i].Note = strings.TrimSpace(in.Note)
		}
		// TTL 与空闲上限允许清成空串（回到全局默认），所以这里看的是「有没有传」，
		// 由调用方用 UpdateUserTTL 单独表达清理意图。
		if in.TokenTTL != "" {
			a.users[i].TokenTTL = strings.TrimSpace(in.TokenTTL)
		}
		if in.MaxIdle != "" {
			a.users[i].MaxIdle = strings.TrimSpace(in.MaxIdle)
		}
		if err := a.saveUsersLocked(); err != nil {
			a.users[i] = prev
			return User{}, err
		}
		return a.users[i], nil
	}
	return User{}, errors.New("成员不存在")
}

// UpdateUserTTL 专职改时限。两个字段留空串表示「回到跟随全局默认」，
// 所以这里能表达清空 —— UpdateUser 做不到这件事。
func (a *Access) UpdateUserTTL(id, tokenTTL, maxIdle string) (User, error) {
	if msg := checkDurations(tokenTTL, maxIdle); msg != "" {
		return User{}, errors.New(msg)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.users {
		if a.users[i].ID != id {
			continue
		}
		prev := a.users[i]
		a.users[i].TokenTTL = strings.TrimSpace(tokenTTL)
		a.users[i].MaxIdle = strings.TrimSpace(maxIdle)
		if err := a.saveUsersLocked(); err != nil {
			a.users[i] = prev
			return User{}, err
		}
		return a.users[i], nil
	}
	return User{}, errors.New("成员不存在")
}

// SetUserStatus 启用/停用。停用同时吊销其名下全部会话与凭证 ——
// 「停用」如果是软性的，管理员在台账上点这一下就没有任何意义。
func (a *Access) SetUserStatus(id, status string) (User, error) {
	if status != UserStatusActive && status != UserStatusDisabled {
		return User{}, fmt.Errorf("未知的成员状态: %s", status)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.users {
		if a.users[i].ID != id {
			continue
		}
		prev := a.users[i]
		a.users[i].Status = status
		revoked := nowISO()
		if status == UserStatusDisabled {
			a.revokeUserLocked(id, revoked)
		}
		if err := a.saveUsersLocked(); err != nil {
			a.users[i] = prev
			return User{}, err
		}
		a.flushSessionsLocked()
		return a.users[i], nil
	}
	return User{}, errors.New("成员不存在")
}

// DeleteUser 删除成员，连带其会话与凭证一并作废。
func (a *Access) DeleteUser(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.users {
		if a.users[i].ID != id {
			continue
		}
		if len(a.users) == 1 {
			return errors.New("至少要保留一名成员，否则没人能再进来")
		}
		a.users = append(a.users[:i], a.users[i+1:]...)
		a.revokeUserLocked(id, nowISO())
		if err := a.saveUsersLocked(); err != nil {
			return err
		}
		a.flushSessionsLocked()
		return nil
	}
	return errors.New("成员不存在")
}

// ---------------------------------------------------------------- 凭证

// CreateInvite 给某个成员签发一条一次性登录凭证。
func (a *Access) CreateInvite(userID, createdBy string, ttl time.Duration) (Invite, error) {
	if _, ok := a.GetUser(userID); !ok {
		return Invite{}, errors.New("成员不存在")
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now().UTC()
	inv := Invite{
		Token:     randomBase64(24),
		UserID:    userID,
		CreatedBy: createdBy,
		CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(ttl).Format(time.RFC3339),
	}
	a.invites = append(a.invites, inv)
	if err := a.saveUsersLocked(); err != nil {
		a.invites = a.invites[:len(a.invites)-1]
		return Invite{}, err
	}
	return inv, nil
}

// ListInvites 返回凭证，最新的排前面。
func (a *Access) ListInvites() []Invite {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Invite, len(a.invites))
	copy(out, a.invites)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// RevokeInvite 吊销一条尚未使用的凭证。
func (a *Access) RevokeInvite(token string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.invites {
		if a.invites[i].Token != token {
			continue
		}
		if a.invites[i].UsedAt != "" {
			return errors.New("凭证已被使用，无法吊销")
		}
		if a.invites[i].RevokedAt != "" {
			return nil
		}
		prev := a.invites[i]
		a.invites[i].RevokedAt = nowISO()
		if err := a.saveUsersLocked(); err != nil {
			a.invites[i] = prev
			return err
		}
		return nil
	}
	return errors.New("凭证不存在")
}

// RedeemResult 是兑换成功的产出：谁登进来了、会话签了多久。
type RedeemResult struct {
	User    User
	Session Session
}

// startSessionLocked 为一个成员建立会话并追加进台账，返回新会话。
//
// 两条登录路径共用它：凭证兑换与管理员口令登录。
// 共用不是图省事 —— 会话的时限算法、长期有效的表示、最后登录时间的回写，
// 只要有两份实现就一定会各自漂移，到时候「口令登录的会话寿命和凭证的不一样」
// 这种问题查起来非常费劲。
//
// 调用方须持锁。
func (a *Access) startSessionLocked(u User, ip, ua, device string, now time.Time, defaultTTL, defaultIdle time.Duration) Session {
	// 时限为 0 表示长期有效，此时到期时间与空闲上限都留空：
	// 空的语义是「没有到期这回事」，而不是「已经到期了」。
	ttl, idle := durationsFor(u, defaultTTL, defaultIdle)
	sess := Session{
		ID:         randomBase64(32),
		UserID:     u.ID,
		CreatedAt:  now.Format(time.RFC3339),
		LastSeenAt: now.Format(time.RFC3339),
		IP:         ip,
		UserAgent:  ua,
		Device:     device,
	}
	if ttl > 0 {
		sess.ExpiresAt = now.Add(ttl).Format(time.RFC3339)
	}
	if idle > 0 {
		sess.MaxIdle = idle.String()
	}
	a.sessions = append(a.sessions, sess)
	a.sessionsDirty = true
	for j := range a.users {
		if a.users[j].ID == u.ID {
			a.users[j].LastLoginAt = sess.CreatedAt
			break
		}
	}
	return sess
}

// StartSession 不经凭证直接给成员建立会话，管理员口令登录走这条路。
func (a *Access) StartSession(userID, ip, ua, device string, now time.Time, defaultTTL, defaultIdle time.Duration) (User, Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range a.users {
		if u.ID != userID {
			continue
		}
		if u.Status != UserStatusActive {
			return User{}, Session{}, errors.New("该成员已停用")
		}
		sess := a.startSessionLocked(u, ip, ua, device, now, defaultTTL, defaultIdle)
		a.flushSessionsLocked()
		return u, sess, nil
	}
	return User{}, Session{}, errors.New("成员不存在")
}

// AdminUser 返回台账里第一名管理员。
// 口令登录只需要认「管理员」这一种身份，所以取第一个就够：
// 内部控制台不外发，管理员之间不需要再分彼此。
func (a *Access) AdminUser() (User, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, u := range a.users {
		if u.Role == RoleAdmin && u.Status == UserStatusActive {
			return u, true
		}
	}
	return User{}, false
}

// Redeem 用凭证换会话。凭证必须是未使用、未吊销、未过期，且归属成员处于启用状态。
// 四个条件缺一不可，任何一个不满足都当作「不存在」返回，避免被用来枚举凭证。
func (a *Access) Redeem(token, ip, ua, device string, now time.Time, defaultTTL, defaultIdle time.Duration) (RedeemResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.invites {
		if a.invites[i].Token != token {
			continue
		}
		inv := a.invites[i]
		switch {
		case inv.RevokedAt != "":
			return RedeemResult{}, errors.New("登录凭证已失效")
		case inv.UsedAt != "":
			return RedeemResult{}, errors.New("登录凭证已失效")
		case now.After(parseISO(inv.ExpiresAt)):
			return RedeemResult{}, errors.New("登录凭证已失效")
		}
		var u User
		found := false
		for _, x := range a.users {
			if x.ID == inv.UserID {
				u, found = x, true
				break
			}
		}
		if !found || u.Status != UserStatusActive {
			return RedeemResult{}, errors.New("登录凭证已失效")
		}

		prevLogin := u.LastLoginAt
		sess := a.startSessionLocked(u, ip, ua, device, now, defaultTTL, defaultIdle)
		a.invites[i].UsedAt = now.Format(time.RFC3339)
		a.invites[i].UsedIP = ip

		// 兑换结果必须立刻持久化：凭证一旦被标记用过，
		// 哪怕进程下一秒重启，它也不能第二次兑换成功。
		if err := a.saveUsersLocked(); err != nil {
			a.invites[i] = inv
			a.sessions = a.sessions[:len(a.sessions)-1]
			for j := range a.users {
				if a.users[j].ID == u.ID {
					a.users[j].LastLoginAt = prevLogin
					break
				}
			}
			return RedeemResult{}, err
		}
		return RedeemResult{User: u, Session: sess}, nil
	}
	return RedeemResult{}, errors.New("登录凭证无效")
}

// ---------------------------------------------------------------- 会话

// SessionView 是会话在管理界面上的样子。
// 用户名与角色实时取：成员被降权或停用后，台账必须当场反映，
// 不能沿用会话建立那一刻的快照。
type SessionView struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id"`
	UserName   string `json:"user_name"`
	Role       string `json:"role"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	Device     string `json:"device"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
	MaxIdle    string `json:"max_idle"`
	Online     bool   `json:"online"`
	Expired    bool   `json:"expired"`
	IdleSec    int64  `json:"idle_seconds"`
	RemainSec  int64  `json:"remain_seconds"`
}

// SessionState 描述会话当前是否还能用，以及不能用的原因。
type SessionState int

const (
	SessionValid SessionState = iota
	SessionMissing
	SessionRevoked
	SessionExpired
	SessionIdleOut
	// SessionOrphan 指成员已被删除或停用：会话本身没过期，但人已经无权访问。
	SessionOrphan
)

// String 把失效原因说成人能读懂的话，前端直接展示。
func (s SessionState) String() string {
	switch s {
	case SessionValid:
		return ""
	case SessionMissing:
		return "会话不存在"
	case SessionRevoked:
		return "会话已被吊销"
	case SessionExpired:
		return "会话已到有效期"
	case SessionIdleOut:
		return "空闲超时"
	case SessionOrphan:
		return "所属成员已停用"
	}
	return "会话不可用"
}

// Resolve 判断 Session ID 是否还能用，顺带刷新最后活跃时间。
// 它把角色与状态返回给调用方去做权限分级。
func (a *Access) Resolve(sessionID string, now time.Time) (SessionView, SessionState) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.sessions {
		if a.sessions[i].ID != sessionID {
			continue
		}
		s := a.sessions[i]
		if s.RevokedAt != "" {
			return SessionView{}, SessionRevoked
		}
		// ExpiresAt 为空意味着长期有效，这里必须连比较都不做：
		// 空串解析出来是零值时间，直接比 After 会把长期会话判成已过期。
		if exp := parseISO(s.ExpiresAt); !exp.IsZero() && now.After(exp) {
			return SessionView{}, SessionExpired
		}
		idle := parseDur(s.MaxIdle, 0)
		if idle > 0 && now.Sub(parseISO(s.LastSeenAt)) > idle {
			return SessionView{}, SessionIdleOut
		}
		u, found := a.userLocked(s.UserID)
		if !found || u.Status != UserStatusActive {
			return SessionView{}, SessionOrphan
		}
		// 心跳：命中即刷新。内存里立即生效，落盘交给后台节流。
		a.sessions[i].LastSeenAt = now.Format(time.RFC3339)
		a.sessionsDirty = true
		return a.viewLocked(a.sessions[i], now), SessionValid
	}
	return SessionView{}, SessionMissing
}

// ListSessions 返回全部会话视图，最近活跃的排前面。
func (a *Access) ListSessions() []SessionView {
	return a.listSessionsAt(time.Now())
}

// listSessionsAt 接受一个「当下」，只为让在线判定能被单测覆盖。
// 在线是个随时间逃走的结论（超过 OnlineWindow 没心跳就转离线），
// 不把时间做成参数的话，这条规则就测不到。
func (a *Access) listSessionsAt(now time.Time) []SessionView {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]SessionView, 0, len(a.sessions))
	for _, s := range a.sessions {
		out = append(out, a.viewLocked(s, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt > out[j].LastSeenAt })
	return out
}

// DeleteSession 把一条会话从台账里彻底抹掉。
//
// 与 RevokeSession 的区别要说清楚：吊销是留痕的「作废」，台账上还能看见
// 「谁什么时候被踢过」；删除是抹掉记录本身。管理员要的是后者居多 ——
// 台账的作用是「现在谁还登着」，一个三个月前就被踢掉的会话占在这里只是噪音。
//
// 删除自己的会话等于把自己注销： cookie 还在浏览器里，但服务端已经认不出它了。
func (a *Access) DeleteSession(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.sessions {
		if a.sessions[i].ID != id {
			continue
		}
		a.sessions = append(a.sessions[:i], a.sessions[i+1:]...)
		a.sessionsDirty = true
		a.flushSessionsLocked()
		return nil
	}
	return errors.New("会话不存在")
}

// RevokeSession 吊销一条会话。
func (a *Access) RevokeSession(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.sessions {
		if a.sessions[i].ID != id {
			continue
		}
		if a.sessions[i].RevokedAt != "" {
			return nil
		}
		a.sessions[i].RevokedAt = nowISO()
		a.sessionsDirty = true
		a.flushSessionsLocked()
		return nil
	}
	return errors.New("会话不存在")
}

// RevokeUserSessions 吊销某成员名下全部会话，返回被吊销的条数。
func (a *Access) RevokeUserSessions(userID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.revokeUserLocked(userID, nowISO())
	if n > 0 {
		a.flushSessionsLocked()
	}
	return n
}

// ReissueForUser 把某成员名下存活会话的有效期重算到「当前 + 新 TTL」。
// 改完 Token 时限希望存量会话跟着变，就调它。
// ReissueForUser 把某个成员新生效的时限重新贴到他名下现存的会话上。
//
// 「改完时限要不要立刻生效」这个问题，在这个方案里必须回答「要」：
// 会话之所以要移出 cookie 落到服务端台账，就是为了它能被改写。
// 管理员把某人的寿命从 12 小时压到 1 小时，是要现在就压下去，
// 而不是等他下次登录时才生效 —— 那中间这一段时间恰好是最需要收回的那一端。
func (a *Access) ReissueForUser(userID string, now time.Time, newTTL, newIdle time.Duration) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for i := range a.sessions {
		s := a.sessions[i]
		if s.UserID != userID || s.RevokedAt != "" {
			continue
		}
		// 新的时限是长期时，把到期时间清掉而不是算出一个过去的时刻
		a.sessions[i].ExpiresAt = ""
		if newTTL > 0 {
			a.sessions[i].ExpiresAt = now.Add(newTTL).Format(time.RFC3339)
		}
		a.sessions[i].MaxIdle = ""
		if newIdle > 0 {
			a.sessions[i].MaxIdle = newIdle.String()
		}
		n++
	}
	if n > 0 {
		a.sessionsDirty = true
		a.flushSessionsLocked()
	}
	return n
}

// Sweep 清掉过期与被吊销的会话，避免台账无限增长。
// 保留一小段：管理员还要在数据里看到「刚才谁被踢了」。
func (a *Access) Sweep(now time.Time, keep time.Duration) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	kept := a.sessions[:0]
	n := 0
	for _, s := range a.sessions {
		exp := parseISO(s.ExpiresAt)
		// 长期有效的会话永远不会自己过期，只有管理员动手才会离开台账
		dead := s.RevokedAt != "" || (!exp.IsZero() && now.After(exp))
		if dead && now.Sub(parseISO(lastNonZero(s.RevokedAt, s.ExpiresAt))) > keep {
			n++
			continue
		}
		kept = append(kept, s)
	}
	a.sessions = kept
	if n > 0 {
		a.sessionsDirty = true
		a.flushSessionsLocked()
	}
	return n
}

// Flush 把脏的会话表写盘。返回是否真的写了。
func (a *Access) Flush() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.sessionsDirty {
		return nil
	}
	return a.flushSessionsLocked()
}

// Loop 周期性地落盘并清理，ctx 取消时做最后一次落盘。
func (a *Access) Loop(ctx context.Context, every, keep time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.Sweep(time.Now(), keep)
			if err := a.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "会话台账落盘失败: %v\n", err)
			}
			return
		case <-t.C:
			a.Sweep(time.Now(), keep)
			if err := a.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "会话台账落盘失败: %v\n", err)
			}
		}
	}
}

// ---------------------------------------------------------------- 内部

func (a *Access) userLocked(id string) (User, bool) {
	for _, u := range a.users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

// viewLocked 组装视图。成员已被删除时退到「已注销」，而不是丢掉这条会话：
// 管理员正需要看到这种模式。
func (a *Access) viewLocked(s Session, now time.Time) SessionView {
	v := SessionView{
		ID:         s.ID,
		UserID:     s.UserID,
		IP:         s.IP,
		UserAgent:  s.UserAgent,
		Device:     s.Device,
		CreatedAt:  s.CreatedAt,
		LastSeenAt: s.LastSeenAt,
		ExpiresAt:  s.ExpiresAt,
		MaxIdle:    s.MaxIdle,
		UserName:   "已注销成员",
	}
	if u, ok := a.userLocked(s.UserID); ok {
		v.UserName = u.Name
		v.Role = u.Role
	}
	idle := now.Sub(parseISO(s.LastSeenAt))
	v.IdleSec = int64(idle.Seconds())
	exp := parseISO(s.ExpiresAt)
	// 长期有效的会话没有到期时刻，在线与否就只看心跳
	v.Online = s.RevokedAt == "" && idle < OnlineWindow && (exp.IsZero() || now.Before(exp))
	v.Expired = s.RevokedAt != "" || (!exp.IsZero() && now.After(exp))
	// RemainSec 为 -1 表示长期有效。用 -1 而不是 0：
	// 0 会被读成「还剩零秒」，而「没有到期这回事」是另一件事。
	switch {
	case exp.IsZero():
		v.RemainSec = -1
	default:
		v.RemainSec = int64(exp.Sub(now).Seconds())
		if v.RemainSec < 0 {
			v.RemainSec = 0
		}
	}
	if s.RevokedAt != "" {
		v.RemainSec = 0
	}
	return v
}

// revokeUserLocked 一次性吊销名下全部会话与未使用的凭证。
func (a *Access) revokeUserLocked(userID, at string) int {
	n := 0
	for i := range a.sessions {
		if a.sessions[i].UserID == userID && a.sessions[i].RevokedAt == "" {
			a.sessions[i].RevokedAt = at
			n++
		}
	}
	for i := range a.invites {
		if a.invites[i].UserID == userID && a.invites[i].UsedAt == "" && a.invites[i].RevokedAt == "" {
			a.invites[i].RevokedAt = at
		}
	}
	if n > 0 {
		a.sessionsDirty = true
	}
	return n
}

func (a *Access) saveUsersLocked() error {
	raw, err := json.MarshalIndent(usersFileDoc{Users: a.users, Invites: a.invites}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	p := filepath.Join(a.dir, usersFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (a *Access) flushSessionsLocked() error {
	raw, err := json.MarshalIndent(a.sessions, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	p := filepath.Join(a.dir, sessionsFile)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	a.sessionsDirty = false
	return nil
}

// durationsFor 解析某成员的时限；留空则跟随全局默认值。
// durationsFor 算出这次签发要用的一对时限。
//
// 返回 0 表示长期有效，这是一个正当取值而不是「没配好」：
// 管理员必须能显式地说「这个人不设寿命」，所以这里不再把 0 回落成默认值。
// 空串（没单独设置）才回落到全局默认 —— 全局默认自己也可以是 0。
func durationsFor(u User, defaultTTL, defaultIdle time.Duration) (time.Duration, time.Duration) {
	ttl := parseDur(u.TokenTTL, defaultTTL)
	idle := parseDur(u.MaxIdle, defaultIdle)
	// 空闲上限不该超过总寿命，否则「总寿命到了」和「空闲到了」会互相打脸。
	// 总寿命为长期时这条规则自动失效：那时只有空闲上限在起作用。
	if idle > 0 && ttl > 0 && idle > ttl {
		idle = ttl
	}
	return ttl, idle
}

// checkDurations 校验传入的 duration 串，返回中文错误串（空串表示没问题）。
//
// "0" 是一个正当取值：明确要求长期有效。少了它，管理员就没法针对某个人说
// 「这个人不设寿命」—— 他只能把值留空回到全局默认，而默认未必是长期。
func checkDurations(ttl, idle string) string {
	for _, s := range []string{ttl, idle} {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return "时限格式不对，应写成 0、30m、8h、168h 这样的时长"
		}
		if d == 0 {
			continue
		}
		if d < time.Minute {
			return "时限至少 1 分钟"
		}
		if d > 365*24*time.Hour {
			return "时限最长一年"
		}
	}
	return ""
}

func parseDur(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

func parseISO(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }

func lastNonZero(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// newID 生成带前缀的短 ID，便于管理员在终端里对账。
func newID(prefix string) string {
	return prefix + "_" + randomHex(6)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand 不可用: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func randomBase64(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand 不可用: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
