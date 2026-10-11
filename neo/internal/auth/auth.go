// Package auth 提供单管理员身份校验与会话签发。
// 第一版的模型刻意简单：通过口令校验的人即平台管理员，没有多用户体系。
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"neo/internal/limit"
	"neo/internal/store"
)

// RoleAdmin 是唯一的角色。后续若引入多用户，这里扩展即可。
const RoleAdmin = "admin"

// CookieName 是会话 cookie 的名字。
const CookieName = "vmc_session"

// Auth 持有口令哈希与会话密钥，并做登录节流。
// 节流实现与匿名申请共用 internal/limit，避免两处各写一份。
type Auth struct {
	sec *store.Secrets
	key []byte
	lim *limit.Limiter
}

// Claims 是会话里携带的内容。
type Claims struct {
	Role  string `json:"role"`
	Exp   int64  `json:"exp"`
	Nonce string `json:"n"`
}

// New 建立认证器。
func New(sec *store.Secrets, limitN int, win time.Duration) (*Auth, error) {
	key, err := hex.DecodeString(sec.SessionKey)
	if err != nil {
		return nil, errors.New("session_key 不是合法 hex")
	}
	if len(key) < 16 {
		return nil, errors.New("session_key 过短")
	}
	return &Auth{sec: sec, key: key, lim: limit.New(limitN, win)}, nil
}

// Limiter 暴露给上层复用同一份节流状态。
func (a *Auth) Limiter() *limit.Limiter { return a.lim }

// CheckPassword 用常数时间比较校验口令。
func (a *Auth) CheckPassword(password string) bool {
	want, _ := hex.DecodeString(a.sec.PasswordHash)
	got := pbkdf2Of(password, a.sec.Salt)
	if len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare(want, got) == 1
}

// Issue 签发一个带过期的会话串，格式 base64url(json).base64url(hmac)。
func (a *Auth) Issue(role string, ttl time.Duration) (string, error) {
	return a.IssueAt(role, time.Now().Add(ttl))
}

// IssueAt 允许注入过期时刻，便于单测断言边界。
func (a *Auth) IssueAt(role string, exp time.Time) (string, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	c := Claims{Role: role, Exp: exp.Unix(), Nonce: hex.EncodeToString(nonce)}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Verify 校验会话串的签名与过期时间。
func (a *Auth) Verify(token string) (*Claims, bool) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, false
	}
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(payload))
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, false
	}
	if subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, false
	}
	if c.Role == "" || time.Now().Unix() > c.Exp {
		return nil, false
	}
	return &c, true
}

// pbkdf2Of 复用 store 的口令拉伸实现，保证写入与校验完全一致。
func pbkdf2Of(password, saltHex string) []byte {
	h := store.HashPassword(password, saltHex)
	out, _ := hex.DecodeString(h)
	return out
}
