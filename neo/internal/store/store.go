// Package store 持有控制台的长久状态。第一版只有一把钥匙：
// 口令哈希与会话签名密钥。明文口令不落库，只在首次引导时返回一次。
package store

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Version 是 secrets 文件格式版本，用于日后迁移。
const Version = 1

// secretsFile 与 bootstrapFile 都落在数据目录下，权限 0600。
const (
	secretsFile   = "secrets.json"
	bootstrapFile = "bootstrap-password.txt"
)

// Secrets 是持久化的钥匙。
type Secrets struct {
	Version      int    `json:"version"`
	CreatedAt    string `json:"created_at"`
	Salt         string `json:"salt"`
	PasswordHash string `json:"password_hash"`
	SessionKey   string `json:"session_key"`
}

// LoadOrCreate 读取钥匙，不存在则生成。
//
// bootstrapPassword 来自环境变量 NEO_ADMIN_PASSWORD；为空时自动生成随机口令，
// 并通过第二个返回值回给调用方（此时为空字符串表示口令由用户自带，无需提示）。
func LoadOrCreate(dir, bootstrapPassword string) (*Secrets, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("创建数据目录失败: %w", err)
	}
	p := filepath.Join(dir, secretsFile)

	if raw, err := os.ReadFile(p); err == nil {
		var s Secrets
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, "", fmt.Errorf("解析 %s 失败: %w", p, err)
		}
		if s.SessionKey == "" || s.Salt == "" || s.PasswordHash == "" {
			return nil, "", errors.New("钥匙文件不完整，缺少 session_key / salt / password_hash")
		}
		return &s, "", nil
	}

	salt := randHex(16)
	pw := bootstrapPassword
	generated := ""
	if pw == "" {
		pw = GeneratePassword(24)
		generated = pw
	}
	s := &Secrets{
		Version:      Version,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		Salt:         salt,
		PasswordHash: HashPassword(pw, salt),
		SessionKey:   randHex(32),
	}
	if err := s.Save(dir); err != nil {
		return nil, "", err
	}
	return s, generated, nil
}

// Save 落盘，权限 0600。
func (s *Secrets) Save(dir string) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, secretsFile), raw, 0o600)
}

// HashPassword 用 PBKDF2-SHA256 拉伸口令，迭代次数固定。
func HashPassword(password, saltHex string) string {
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		// salt 由本项目生成，理论上总是合法 hex；异常时退回用原始字节当 salt，
		// 保证不因一条脏数据就让登录链路崩溃。
		salt = []byte(saltHex)
	}
	// pbkdf2.Key 的签名是 (散列构造函数, 口令字符串, salt, 迭代次数, 密钥长度)
	dk, err := pbkdf2.Key(sha256.New, password, salt, 210000, 32)
	if err != nil {
		// PBKDF2 只在密钥长度非法时报错，这里恒定 32 字节，属于不可能分支；
		// 真发生了也不能默默放行，直接 panic 让问题在启动阶段暴露。
		panic("pbkdf2 参数非法: " + err.Error())
	}
	return hex.EncodeToString(dk)
}

// GeneratePassword 生成一个便于复制粘贴的随机管理员口令。
// 12 字节随机量用 URL-safe base64 编码，约 16 个字符，兼具强度与易用性。
func GeneratePassword(n int) string {
	if n <= 0 {
		n = 12
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand 不可用: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// WriteBootstrap 把首发生成的口令写一份到数据目录，便于后台查看。
func WriteBootstrap(dir, password string) error {
	return os.WriteFile(filepath.Join(dir, bootstrapFile), []byte(password+"\n"), 0o600)
}

// RemoveBootstrap 首次登录成功后抹掉明文口令文件，避免长期留一个可读口令在磁盘上。
func RemoveBootstrap(dir string) error {
	err := os.Remove(filepath.Join(dir, bootstrapFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand 不可用: " + err.Error())
	}
	return hex.EncodeToString(buf)[:n*2]
}
