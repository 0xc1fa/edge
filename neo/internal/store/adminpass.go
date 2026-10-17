package store

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AdminPasswordFile 是管理员口令的落盘文件。
//
// 它明文躺在数据目录里，和当年 bootstrap-password.txt 的用法一致：
// 管理员 cat 出来粘进登录框，用不着「找回密码」这条路。
//
// 为什么敢明文：这个口令只属于部署者自己，文件权限 0600，
// 而它保护的东西（实例清单与申请人联系方式）并不比文件本身更敏感 ——
// 能读到这个文件的人，本来就能登上这台机器。
// 反过来，为了「不明文」而引入一套自己实现的口令哈希，
// 带来的复杂度比它挡住的风险更多。
const AdminPasswordFile = "admin-password.txt"

// AdminPasswordPath 给出口令文件的完整路径。
func AdminPasswordPath(dir string) string {
	return filepath.Join(dir, AdminPasswordFile)
}

// LoadAdminPassword 读出现有口令；文件不存在或为空时返回空串。
func LoadAdminPassword(dir string) string {
	b, err := os.ReadFile(AdminPasswordPath(dir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// EnsureAdminPassword 保证管理员有一个口令：
// 文件里有就沿用，没有就现生成一个随机口令写进去。
//
// 返回 generated=true 表示这次是新建的，调用方应当把这件事打进日志 ——
// 口令只在屏幕上出现一次，管理员没看见就再也找不到了（当然他随时可以 cat 文件）。
func EnsureAdminPassword(dir string) (password string, generated bool, err error) {
	if p := LoadAdminPassword(dir); p != "" {
		return p, false, nil
	}
	p, err := NewPassword()
	if err != nil {
		return "", false, err
	}
	if err := os.WriteFile(AdminPasswordPath(dir), []byte(p+"\n"), 0o600); err != nil {
		return "", false, fmt.Errorf("写管理员口令失败: %w", err)
	}
	return p, true, nil
}

// ResetAdminPassword 丢弃现有口令，重新生成一个随机的。
// 口令文件本来就该随时可看，所以「重设」不需要任何额外的确认仪式。
func ResetAdminPassword(dir string) (string, error) {
	p, err := NewPassword()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(AdminPasswordPath(dir), []byte(p+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("写管理员口令失败: %w", err)
	}
	return p, nil
}

// SetAdminPassword 覆盖口令文件，用于管理员主动重设。
func SetAdminPassword(dir, password string) error {
	p := strings.TrimSpace(password)
	if p == "" {
		return errors.New("口令不能为空")
	}
	return os.WriteFile(AdminPasswordPath(dir), []byte(p+"\n"), 0o600)
}

// CheckPassword 常量时间比较，避免拿响应时间去猜口令。
func CheckPassword(got, want string) bool {
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// randomPassword 生成 24 字符的 URL 安全随机串。
// 长度取 24 是约 144 bit 熵，比任何人都猜不出的门槛高得多，
// 又不至于长到没法手打进登录框。
// NewPassword 生成一枚新口令。
func NewPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
