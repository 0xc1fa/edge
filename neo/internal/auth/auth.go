// Package auth 提供无口令登录：签发一次性凭证、把凭证兑换成会话、判定会话是否还有效。
//
// 和旧版本的差别是根本性的：原先「谁握着口令谁就是管理员」，
// 会话是自签名、无服务端状态的一串字符 —— 改不了寿命、踢不掉、也数不出来。
// 现在口令消失（连它的容器 secrets.json 一起退休），身份改成：
//
//	管理员签发凭证 → 使用者兑换 → 服务端落一条会话 → cookie 里只留会话 ID
//
// 于是「管理员能改 Token 时限」「能实时看见谁在哪台机器上」「能单独踢掉某台」
// 这三件事变成对会话表的普通操作，而不是要重签密钥才能让所有人下线。
//
// 为什么凭证是一次性的：它的强度来自「用完即废、短寿、可吊销」，
// 而不是来自「使用者记得住」。这也是不需要口令的根本原因 ——
// 使用者不必持有任何长期秘密，管理员也不必再处理「帮我重置一下」。
package auth

import (
	"errors"
	"time"

	"neo/internal/limit"
	"neo/internal/store"
)

// CookieName 沿用旧值，避免存量部署升级后浏览器里留下同名不同义的两枚 cookie。
const CookieName = "vmc_session"

// 默认值。成员未单独指定时限时跟随它们，管理员再用环境变量覆盖。
//
// 会话的总寿命默认是长期有效（0），不是某个小时数。理由写在 store.Session 的注释里：
// 内部控制台的访问者就那么几个人，到期把人踢出去的麻烦比收益多，
// 而台账是可读可删的 —— 想收紧就改个人的时限，或者把那条会话删掉。
const (
	// DefaultTokenTTL 是会话的总寿命，0 表示长期有效。
	DefaultTokenTTL = 0
	// DefaultMaxIdle 是多久没有动静就自动失效，同样为 0 表示不限。
	// 「在线 / 离线」的显示仍然正常工作：那是按心跳窗口判的，与失效无关。
	DefaultMaxIdle = 0
	// DefaultInviteTTL 是一次性凭证的存活时间。
	// 够管理员复制链接、走 IM 发给对方并在一会儿之内点开即可，不需要更长。
	DefaultInviteTTL = 15 * time.Minute
	// MaxInviteTTL 挡住「签一年期的万能链接」这种用法：
	// 凭证的价值就在于短寿，超过一天的寿命等于把它退回成了口令。
	MaxInviteTTL = 24 * time.Hour
)

// ErrTooMany 是兑换限流被触发时的返回值，上层据此回 429。
var ErrTooMany = errors.New("尝试过于频繁，请稍后再试")

// Service 串起两条登录链路：
//
//   - 管理员：口令登录（Login），口令明文落在数据目录里；
//   - 所有人：管理员签发的一次性凭证（Redeem）。
//
// 两条路最终落在同一张会话台账上 —— 因此会话的寿命、吊销、删除对两者一视同仁，
// 管理员在会话页上删掉谁，谁就用不了，跟他当初是拿口令还是拿链接进来的无关。
type Service struct {
	acc  *store.Access
	lim  *limit.Limiter
	ttl  time.Duration
	idle time.Duration
	// adminPass 在进程启动时从数据目录读入，之后常驻内存
	adminPass string
	// 口令与凭证各用一个限流器：口令被试探时，不该顺带把凭证兑换也拖进惩罚里
	passLim *limit.Limiter
}

// New 建立服务。ttl / idle 为全局默认，**零值与负值都表示长期有效**，
// 不再回落到某个写死的常量 —— 否则部署者就再也没法把默认设成长期了。
func New(acc *store.Access, lim *limit.Limiter, ttl, idle time.Duration) *Service {
	if ttl < 0 {
		ttl = 0
	}
	if idle < 0 {
		idle = 0
	}
	// 总寿命有上限时，空闲上限不得超过它；
	// 总寿命是长期（0）时，空闲上限独立生效，不必跟着被压成 0。
	if idle > 0 && ttl > 0 && idle > ttl {
		idle = ttl
	}
	return &Service{acc: acc, lim: lim, ttl: ttl, idle: idle, passLim: limit.New(10, time.Minute)}
}

// SetAdminPassword 装入管理员口令。
func (s *Service) SetAdminPassword(p string) { s.adminPass = p }

// HasAdminPassword 是否配了口令。
// 口令为空时口令登录必须整条关掉，而不是「空口令也能登进来」。
func (s *Service) HasAdminPassword() bool { return s.adminPass != "" }

// Login 是管理员的口令登录。
//
// 限流放在校验之前：口令是部署者自己设的，通常不会太长，
// 不先挡住频次的话，这里就成了一个可以无节制试的入口。
// 失败原因只给一句「口令不对」—— 不存在「口令对但账号不存在」这种提示，
// 那等于把「这台机器有没有管理员」也告诉了对方。
func (s *Service) Login(password, ip, ua, device string) (store.User, store.Session, error) {
	if s.adminPass == "" {
		return store.User{}, store.Session{}, errors.New("尚未设置管理员口令")
	}
	if ok, retry := s.passLim.Allow(ip); !ok {
		return store.User{}, store.Session{}, &Throttled{Retry: retry}
	}
	if !store.CheckPassword(password, s.adminPass) {
		s.passLim.Record(ip)
		return store.User{}, store.Session{}, errors.New("口令不对")
	}
	u, ok := s.acc.AdminUser()
	if !ok {
		return store.User{}, store.Session{}, errors.New("还没有管理员")
	}
	// 口令登录签发的会话，时限规则与凭证兑换完全一致（共用 startSessionLocked）
	_, sess, err := s.acc.StartSession(u.ID, ip, ua, device, time.Now(), s.ttl, s.idle)
	if err != nil {
		return store.User{}, store.Session{}, err
	}
	s.passLim.Reset(ip)
	return u, sess, nil
}

// Defaults 是当前的全局时限，用于回填到响应里：
// 前端要能把「这个成员没单独设置」如实显示成跟随全局。
func (s *Service) Defaults() (tokenTTL, maxIdle time.Duration) {
	return s.ttl, s.idle
}

// IssueInvite 给成员签发一条登录凭证。
// ttl 超出 MaxInviteTTL 会被压下来：一次性凭证的价值就是短寿。
func (s *Service) IssueInvite(userID, createdBy string, ttl time.Duration) (store.Invite, error) {
	if ttl <= 0 {
		ttl = DefaultInviteTTL
	}
	if ttl > MaxInviteTTL {
		ttl = MaxInviteTTL
	}
	return s.acc.CreateInvite(userID, createdBy, ttl)
}

// Redeem 用凭证换一个会话。
//
// 失败原因一律归一成同一句话：凭证无效、已用过、已被吊销、已过期、
// 归属成员被停用 —— 返回的信息完全相同。这不是偷懒，
// 是为了让这里不能当作「凭证是否存在」的探测口。
func (s *Service) Redeem(token, ip, ua, device string) (store.User, store.Session, error) {
	if ok, retry := s.lim.Allow(ip); !ok {
		return store.User{}, store.Session{}, &Throttled{Retry: retry}
	}
	res, err := s.acc.Redeem(token, ip, ua, device, time.Now(), s.ttl, s.idle)
	if err != nil {
		s.lim.Record(ip)
		return store.User{}, store.Session{}, err
	}
	s.lim.Reset(ip)
	return res.User, res.Session, nil
}

// Throttled 携带建议的重试等待时间，便于回 Retry-After。
type Throttled struct {
	Retry time.Duration
}

func (e *Throttled) Error() string { return ErrTooMany.Error() }

// Resolve 判定会话 ID 是否还能用，命中则顺带刷新最后活跃时间。
func (s *Service) Resolve(value string) (store.SessionView, store.SessionState) {
	if value == "" {
		return store.SessionView{}, store.SessionMissing
	}
	return s.acc.Resolve(value, time.Now())
}

// Logout 让一枚 cookie 值对应的会话立刻失效。
func (s *Service) Logout(value string) {
	if value == "" {
		return
	}
	_ = s.acc.RevokeSession(value)
}

// RevokeSession / RevokeUserSessions 供管理员在会话台账上操作。
func (s *Service) RevokeSession(id string) error        { return s.acc.RevokeSession(id) }
func (s *Service) RevokeUserSessions(userID string) int { return s.acc.RevokeUserSessions(userID) }

// DeleteSession 把会话从台账里抹掉，而不是留一条被吊销的记录。
// 它不挑目标状态：已失效的、所属成员已删除的、三个月前的，一律能删 ——
// 台账要回答的是「现在谁登着」，不该被历史记录占满而让人误判。
func (s *Service) DeleteSession(id string) error { return s.acc.DeleteSession(id) }

// ApplyTTL 把某个成员名下存活会话的到期时间按新时限重算。
// 返回受影响的会话数：改时限要能当场作用于已经在线的人，
// 否则管理员改完还得等他自己重新登一次才生效。
func (s *Service) ApplyTTL(user store.User) int {
	ttl, idle := s.Effective(user)
	return s.acc.ReissueForUser(user.ID, time.Now(), ttl, idle)
}

// Effective 算出某个成员实际生效的时限（含回落到全局默认的部分）。
func (s *Service) Effective(u store.User) (tokenTTL, maxIdle time.Duration) {
	tokenTTL = s.ttl
	if d, err := time.ParseDuration(u.TokenTTL); err == nil && d > 0 {
		tokenTTL = d
	}
	maxIdle = s.idle
	if d, err := time.ParseDuration(u.MaxIdle); err == nil && d > 0 {
		maxIdle = d
	}
	if maxIdle > 0 && tokenTTL > 0 && maxIdle > tokenTTL {
		maxIdle = tokenTTL
	}
	return tokenTTL, maxIdle
}
