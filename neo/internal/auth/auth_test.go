package auth

import (
	"testing"
	"time"

	"neo/internal/limit"
	"neo/internal/store"
)

// 口令登录是管理员进控制台的唯一入口，而「唯一」这个词要靠测试守住：
// 口令错了不给会话、没配口令时整条路关掉 ——
// 后者尤其要紧，否则一次配置疏漏就会变成「空口令也能登进来」。
func TestLoginWithPassword(t *testing.T) {
	dir := t.TempDir()
	acc, err := store.NewAccess(dir)
	if err != nil {
		t.Fatalf("建台账失败: %v", err)
	}
	if _, err := acc.CreateUser(store.UserInput{Name: "管理员", Role: store.RoleAdmin}); err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}
	s := New(acc, limit.New(10, time.Minute), 0, 0)
	s.SetAdminPassword("correct-horse")

	u, sess, err := s.Login("correct-horse", "10.66.0.9", "UA", "laptop")
	if err != nil {
		t.Fatalf("口令正确却登录失败: %v", err)
	}
	if u.Role != store.RoleAdmin {
		t.Errorf("口令登录进来的人应当是管理员，实际 %s", u.Role)
	}
	// 默认长期有效，与凭证兑换那条路保持一致
	if sess.ExpiresAt != "" {
		t.Errorf("口令登录的会话默认应当是长期，实际 %s", sess.ExpiresAt)
	}
	if _, st := acc.Resolve(sess.ID, time.Now()); st != store.SessionValid {
		t.Errorf("刚签发的会话应当可用，实际 %v", st)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	dir := t.TempDir()
	acc, err := store.NewAccess(dir)
	if err != nil {
		t.Fatalf("建台账失败: %v", err)
	}
	if _, err := acc.CreateUser(store.UserInput{Name: "管理员", Role: store.RoleAdmin}); err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}
	s := New(acc, limit.New(10, time.Minute), 0, 0)
	s.SetAdminPassword("correct-horse")

	if _, _, err := s.Login("wrong", "10.66.0.9", "UA", "laptop"); err == nil {
		t.Fatal("口令不对也登进来了")
	}
	if n := len(acc.ListSessions()); n != 0 {
		t.Errorf("登录失败不该留下会话，实际 %d 条", n)
	}
}

func TestLoginDisabledWithoutPassword(t *testing.T) {
	dir := t.TempDir()
	acc, err := store.NewAccess(dir)
	if err != nil {
		t.Fatalf("建台账失败: %v", err)
	}
	if _, err := acc.CreateUser(store.UserInput{Name: "管理员", Role: store.RoleAdmin}); err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}
	// 不调用 SetAdminPassword：模拟口令文件缺失这种配置疏漏
	s := New(acc, limit.New(10, time.Minute), 0, 0)

	if _, _, err := s.Login("", "10.66.0.9", "UA", "laptop"); err == nil {
		t.Fatal("没有配口令时口令登录必须整条关掉")
	}
}

// TestSharedSessionRulesAcrossBothPaths 锁住一件事：
// 口令登录与凭证兑换必须落在同一套会话规则上。
// 两条路各自实现一次时限算法，迟早会出现「口令进来的活 12 小时、链接进来的长期有效」
// 这种说不清的差异。
func TestSharedSessionRulesAcrossBothPaths(t *testing.T) {
	dir := t.TempDir()
	acc, err := store.NewAccess(dir)
	if err != nil {
		t.Fatalf("建台账失败: %v", err)
	}
	admin, err := acc.CreateUser(store.UserInput{Name: "管理员", Role: store.RoleAdmin})
	if err != nil {
		t.Fatalf("建管理员失败: %v", err)
	}
	s := New(acc, limit.New(10, time.Minute), 2*time.Hour, 30*time.Minute)
	s.SetAdminPassword("pw")

	_, byPass, err := s.Login("pw", "10.66.0.9", "UA", "laptop")
	if err != nil {
		t.Fatalf("口令登录失败: %v", err)
	}
	inv, err := acc.CreateInvite(admin.ID, "", 15*time.Minute)
	if err != nil {
		t.Fatalf("签发凭证失败: %v", err)
	}
	res, err := acc.Redeem(inv.Token, "10.66.0.9", "UA", "phone", time.Now(), 2*time.Hour, 30*time.Minute)
	if err != nil {
		t.Fatalf("凭证兑换失败: %v", err)
	}

	if byPass.MaxIdle != res.Session.MaxIdle {
		t.Errorf("两条路签出的空闲上限应当一致：口令 %q / 凭证 %q", byPass.MaxIdle, res.Session.MaxIdle)
	}
	if byPass.ExpiresAt == "" || res.Session.ExpiresAt == "" {
		t.Fatalf("全局时限为 2h 时，两条路都应当带到期时间")
	}
}
