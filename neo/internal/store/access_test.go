package store

import (
	"testing"
	"time"
)

// newTestAccess 建一个落在临时目录里的台账。
func newTestAccess(t *testing.T) *Access {
	t.Helper()
	a, err := NewAccess(t.TempDir())
	if err != nil {
		t.Fatalf("建立台账失败: %v", err)
	}
	return a
}

// adminUser 造一名管理员，Redeem 需要它来签发会话。
func adminUser(t *testing.T, a *Access) User {
	t.Helper()
	u, err := a.CreateUser(UserInput{Name: "张三", Role: RoleAdmin})
	if err != nil {
		t.Fatalf("创建成员失败: %v", err)
	}
	return u
}

func login(t *testing.T, a *Access, u User, now time.Time) Session {
	t.Helper()
	inv, err := a.CreateInvite(u.ID, "", 15*time.Minute)
	if err != nil {
		t.Fatalf("签发凭证失败: %v", err)
	}
	res, err := a.Redeem(inv.Token, "10.66.0.9", "Chrome/131", "dev-a1b2", now, 12*time.Hour, 8*time.Hour)
	if err != nil {
		t.Fatalf("兑换失败: %v", err)
	}
	return res.Session
}

func TestInviteIsSingleUse(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	inv, err := a.CreateInvite(u.ID, "", 15*time.Minute)
	if err != nil {
		t.Fatalf("签发凭证失败: %v", err)
	}
	now := time.Now()
	if _, err := a.Redeem(inv.Token, "10.66.0.9", "UA", "", now, 12*time.Hour, 8*time.Hour); err != nil {
		t.Fatalf("首次兑换应当成功: %v", err)
	}
	if _, err := a.Redeem(inv.Token, "10.66.0.9", "UA", "", now.Add(time.Second), 12*time.Hour, 8*time.Hour); err == nil {
		t.Error("凭证必须只能用一次，第二次却通过了")
	}
}

func TestExpiredInviteCannotBeRedeemed(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	inv, err := a.CreateInvite(u.ID, "", time.Second)
	if err != nil {
		t.Fatalf("签发凭证失败: %v", err)
	}
	_, err = a.Redeem(inv.Token, "10.66.0.9", "UA", "", time.Now().Add(2*time.Second), 12*time.Hour, 8*time.Hour)
	if err == nil {
		t.Error("过期凭证不应还能兑换")
	}
}

func TestRevokedAndDisabledBlockRedeem(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)

	// 吊销过的凭证
	inv, _ := a.CreateInvite(u.ID, "", 15*time.Minute)
	if err := a.RevokeInvite(inv.Token); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	if _, err := a.Redeem(inv.Token, "10.66.0.9", "UA", "", time.Now(), 12*time.Hour, 8*time.Hour); err == nil {
		t.Error("已吊销的凭证不应还能兑换")
	}

	// 停用成员之后，他手上没用掉的凭证也要一起作废 ——
	// 否则「停用」只是一个被挂起的体面说法。
	inv2, _ := a.CreateInvite(u.ID, "", 15*time.Minute)
	if _, err := a.SetUserStatus(u.ID, UserStatusDisabled); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, err := a.Redeem(inv2.Token, "10.66.0.9", "UA", "", time.Now(), 12*time.Hour, 8*time.Hour); err == nil {
		t.Error("成员停用后，他未使用的凭证应当一并作废")
	}
}

// TestDisablingUserKillsLiveSessions 是整组里最要紧的一条：
// 管理员在页面上点下「停用」，必须立刻断掉这个人的全部会话，
// 而不是等他手上的 cookie 自己过期。
//
// 实现手段是顺手把会话吊销掉，所以查出来的结论是「已吊销」而不是「人已停用」：
// 后者是 SessionOrphan 的语义，留给台账被手工改动过这种兜底场景。
func TestDisablingUserKillsLiveSessions(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	now := time.Now()
	sess := login(t, a, u, now)

	if _, st := a.Resolve(sess.ID, now.Add(time.Minute)); st != SessionValid {
		t.Fatalf("停用前会话应当有效，却得到 %v", st)
	}
	if _, err := a.SetUserStatus(u.ID, UserStatusDisabled); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if _, st := a.Resolve(sess.ID, now.Add(2*time.Minute)); st != SessionRevoked {
		t.Errorf("成员停用后会话应当立刻失效，实际 %v", st)
	}
}

// TestOrphanSessionIsRejected 覆盖会话与成员脱节这条分支：
// 台账里还留着会话、但成员已经被删除，这种数据当然不该还能用。
func TestOrphanSessionIsRejected(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	_, err := a.CreateUser(UserInput{Name: "李四", Role: RoleMember})
	if err != nil {
		t.Fatalf("创建第二名成员失败: %v", err)
	}
	now := time.Now()
	sess := login(t, a, u, now)

	if err := a.DeleteUser(u.ID); err != nil {
		t.Fatalf("删除成员失败: %v", err)
	}
	if _, st := a.Resolve(sess.ID, now.Add(time.Second)); st != SessionRevoked {
		t.Errorf("删除成员后的会话应当失效，实际 %v", st)
	}
}

func TestSessionIdleTimeout(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	if _, err := a.UpdateUserTTL(u.ID, "1h", "10m"); err != nil {
		t.Fatalf("设置时限失败: %v", err)
	}
	now := time.Now()
	sess := login(t, a, u, now)
	if sess.MaxIdle != (10 * time.Minute).String() {
		t.Fatalf("会话应当带上成员的空闲上限，实际 %s", sess.MaxIdle)
	}
	// 注意 Resolve 会顺带刷新最后活跃时间，所以第二次要相对上一次再往后推，
	// 而不是相对会话建立时刻。
	if _, st := a.Resolve(sess.ID, now.Add(9*time.Minute)); st != SessionValid {
		t.Fatalf("空闲上限内应当有效，实际 %v", st)
	}
	if _, st := a.Resolve(sess.ID, now.Add(9*time.Minute).Add(11*time.Minute)); st != SessionIdleOut {
		t.Errorf("超出空闲上限应当失效，实际 %v", st)
	}
}

func TestSessionAbsoluteExpiry(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	if _, err := a.UpdateUserTTL(u.ID, "10m", ""); err != nil {
		t.Fatalf("设置时限失败: %v", err)
	}
	now := time.Now()
	sess := login(t, a, u, now)
	if _, st := a.Resolve(sess.ID, now.Add(11*time.Minute)); st != SessionExpired {
		t.Errorf("超过总寿命应当失效，实际 %v", st)
	}
}

// TestIdleNeverOutlivesToken 锁住一个容易自相矛盾的配置：
// 空闲上限比总寿命还长，等于「总寿命」这条规则形同虚设。
func TestIdleNeverOutlivesToken(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	if _, err := a.UpdateUserTTL(u.ID, "10m", "2h"); err != nil {
		t.Fatalf("设置时限失败: %v", err)
	}
	now := time.Now()
	sess := login(t, a, u, now)
	if sess.MaxIdle != (10 * time.Minute).String() {
		t.Errorf("空闲上限应被夹到不超过总寿命，实际 %s", sess.MaxIdle)
	}
	if got, want := parseISO(sess.ExpiresAt), parseISO(sess.CreatedAt).Add(10*time.Minute); !got.Equal(want) {
		t.Errorf("到期时间应等于签发 + 10m，实际 %v", got)
	}
}

func TestResolveRefreshesLastSeen(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	now := time.Now()
	sess := login(t, a, u, now)

	v, st := a.Resolve(sess.ID, now.Add(30*time.Second))
	if st != SessionValid {
		t.Fatalf("会话应当有效，实际 %v", st)
	}
	if want := now.Add(30 * time.Second).UTC().Format(time.RFC3339); v.LastSeenAt != want {
		t.Errorf("Resolve 应当顺带刷新最后活跃时间：想 %s，实际 %s", want, v.LastSeenAt)
	}
	if !v.Online {
		t.Error("刚活动过的会话应判为在线")
	}
}

// TestOnlineFollowsHeartbeat 覆盖台账上的「在线」是怎么来的：
// 它是心跳的结果，不是「cookie 还没过期」。
// 少了这一条，会话台账上就会挂满一排永远显示在线的僵尸。
func TestOnlineFollowsHeartbeat(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	now := time.Now()
	_ = login(t, a, u, now)

	if views := a.listSessionsAt(now.Add(30 * time.Second)); len(views) != 1 || !views[0].Online {
		t.Fatalf("刚活动过应判为在线，实际 %+v", views)
	}
	if got := a.listSessionsAt(now.Add(OnlineWindow - time.Second)); !got[0].Online {
		t.Error("窗口之内仍应是在线")
	}
	if got := a.listSessionsAt(now.Add(OnlineWindow + time.Second)); got[0].Online {
		t.Error("超过在线窗口没有心跳，就不该再显示在线")
	}
	if got := a.listSessionsAt(now.Add(OnlineWindow + time.Second)); got[0].Expired {
		t.Error("离线不等于失效：没到期的会话随时能被人重新用起来")
	}
}

// TestUpdatedTTLAppliesToLiveSessions 锁住「改完时限立刻生效」这件事。
//
// 会话之所以要从 cookie 搬进服务端台账，就是为了它能被改写：
// 管理员把某人从 12 小时压到 1 小时，是为了现在就压下去，
// 而不是等他下一次登录 —— 中间这段时间恰好是最需要收回的那一端。
func TestUpdatedTTLAppliesToLiveSessions(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	now := time.Now()
	sess := login(t, a, u, now)

	if _, err := a.UpdateUserTTL(u.ID, "1h", "10m"); err != nil {
		t.Fatalf("设置时限失败: %v", err)
	}
	at := now.Add(time.Minute)
	if n := a.ReissueForUser(u.ID, at, time.Hour, 10*time.Minute); n != 1 {
		t.Fatalf("应当把时限贴到 1 个现有会话上，实际 %d", n)
	}

	v, st := a.Resolve(sess.ID, at.Add(time.Second))
	if st != SessionValid {
		t.Fatalf("重贴时限之后会话应当仍然有效，实际 %v", st)
	}
	if got, want := v.MaxIdle, (10 * time.Minute).String(); got != want {
		t.Errorf("空闲上限应当同步到现有会话：想 %s，实际 %s", want, got)
	}
	// ExpiresAt 以 RFC3339 秒精度存盘，比对要对齐到同一个精度
	if got, want := parseISO(v.ExpiresAt), at.Add(time.Hour).Truncate(time.Second); !got.Equal(want) {
		t.Errorf("到期时间应当按新的总寿命重算：想 %v，实际 %v", want, got)
	}
}

func TestRevokeSession(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	now := time.Now()
	sess := login(t, a, u, now)

	if err := a.RevokeSession(sess.ID); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	if _, st := a.Resolve(sess.ID, now.Add(time.Second)); st != SessionRevoked {
		t.Errorf("吊销之后的会话应失效，实际 %v", st)
	}
}

// TestPerpetualSession 覆盖默认形态：不设寿命就长期有效。
//
// 这条之所以要写死在测试里，是因为「长期」在实现上是个危险的取值 ——
// 空串解析出来的零值时间比任何时刻都早，判过期时稍不留神就会把长期会话
// 一律判成已过期（反过来也很轻易：把直觉上的「没配」当成永久）。
func TestPerpetualSession(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	inv, err := a.CreateInvite(u.ID, "", 15*time.Minute)
	if err != nil {
		t.Fatalf("签发凭证失败: %v", err)
	}
	now := time.Now()
	res, err := a.Redeem(inv.Token, "10.66.0.9", "UA", "", now, 0, 0)
	if err != nil {
		t.Fatalf("兑换失败: %v", err)
	}
	if res.Session.ExpiresAt != "" {
		t.Fatalf("长期会话不应有到期时间，实际 %s", res.Session.ExpiresAt)
	}

	// 一年之后回头看，它应当仍然有效。
	// 先用 listSessionsAt 看一眼台账：Resolve 本身会刷新心跳，
	// 所以「离线」这种结论必须在不带副作用的那次读取上看。
	later := now.Add(365 * 24 * time.Hour)
	if got := a.listSessionsAt(later); len(got) != 1 {
		t.Fatalf("台账上应当还有这一条，实际 %d 条", len(got))
	} else {
		if got[0].Online {
			t.Error("长期不等于在线：一年没有心跳，台账上应当是离线")
		}
		if got[0].Expired {
			t.Error("长期会话不应出现在「已失效」的位置上")
		}
	}
	v, st := a.Resolve(res.Session.ID, later)
	if st != SessionValid {
		t.Fatalf("长期会话不应自行失效，实际 %v", st)
	}
	if v.RemainSec != -1 {
		t.Errorf("长期会话的剩余寿命应当标记成 -1，实际 %d", v.RemainSec)
	}
	if v.Expired {
		t.Error("长期会话不应被标为已失效")
	}
}

// TestIdleStillAppliesWithoutExpiry 锁住一个容易写错的组合：
// 总寿命长期、但空闲上限照旧。长期只关掉了「总寿命」这一条，不是全部时限。
func TestIdleStillAppliesWithoutExpiry(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	if _, err := a.UpdateUserTTL(u.ID, "", "10m"); err != nil {
		t.Fatalf("设置时限失败: %v", err)
	}
	inv, err := a.CreateInvite(u.ID, "", 15*time.Minute)
	if err != nil {
		t.Fatalf("签发凭证失败: %v", err)
	}
	now := time.Now()
	// 默认时限传 0：这个时候的总寿命才是长期，login 那个 helper 给的是 12h
	res, err := a.Redeem(inv.Token, "10.66.0.9", "UA", "", now, 0, 0)
	if err != nil {
		t.Fatalf("兑换失败: %v", err)
	}
	if res.Session.ExpiresAt != "" {
		t.Fatalf("总寿命应当是长期，实际 %s", res.Session.ExpiresAt)
	}
	if _, st := a.Resolve(res.Session.ID, now.Add(11*time.Minute)); st != SessionIdleOut {
		t.Errorf("空闲上限应当仍然生效，实际 %v", st)
	}
}

// TestEnsureMemberReusesByName 覆盖「按名字签发」背后的取人规则。
// 控制台上不再摆成员管理之后，管理员只需要写一个名字 ——
// 同名必须落在同一条记录上，否则一个人每次进来都会变成另一个人，
// 会话台账上就会堆满同一个人的分身。
func TestEnsureMemberReusesByName(t *testing.T) {
	a := newTestAccess(t)
	u1, err := a.EnsureMember("张三", RoleMember)
	if err != nil {
		t.Fatalf("按名字取成员失败: %v", err)
	}
	u2, err := a.EnsureMember("张三", RoleMember)
	if err != nil {
		t.Fatalf("再次按名字取成员失败: %v", err)
	}
	if u1.ID != u2.ID {
		t.Errorf("同名应当复用同一条记录：%s 与 %s", u1.ID, u2.ID)
	}
	// 这个测试台账是空的，写两次名字之后应当只有张三一个人
	if got := len(a.ListUsers()); got != 1 {
		t.Errorf("同名不该重复建人，实际 %d 人", got)
	}
}

func TestDeleteSession(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	now := time.Now()
	sess := login(t, a, u, now)

	if err := a.DeleteSession(sess.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, st := a.Resolve(sess.ID, now.Add(time.Second)); st != SessionMissing {
		t.Errorf("删除之后会话应当彻底不存在，实际 %v", st)
	}
	if got := a.listSessionsAt(now.Add(time.Second)); len(got) != 0 {
		t.Errorf("台账里不该再留着它，实际还有 %d 条", len(got))
	}
	if err := a.DeleteSession(sess.ID); err == nil {
		t.Error("重复删除应当报「不存在」，而不是假装成功")
	}
}

func TestLastUserCannotBeDeleted(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	if err := a.DeleteUser(u.ID); err == nil {
		t.Error("删掉最后一名成员会把所有人都关在门外，应当被拒绝")
	}
}

// TestDefaultDurationsApply 保证「没单独设置时限的人」跟随服务端默认值，
// 而不是得到一个意外宽松的会话。
func TestDefaultDurationsApply(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	now := time.Now()
	sess := login(t, a, u, now)
	if got, want := sess.MaxIdle, (8 * time.Hour).String(); got != want {
		t.Errorf("未设置时应当跟随默认空闲上限 %s，实际 %s", want, got)
	}
	if got, want := parseISO(sess.ExpiresAt), parseISO(sess.CreatedAt).Add(12*time.Hour); !got.Equal(want) {
		t.Errorf("未设置时应当跟随默认总寿命，实际 %v", got)
	}
}

func TestRejectBadDurationStrings(t *testing.T) {
	a := newTestAccess(t)
	u := adminUser(t, a)
	bad := []string{"abc", "10", "30 秒", "-5m"}
	for _, s := range bad {
		if _, err := a.UpdateUserTTL(u.ID, s, ""); err == nil {
			t.Errorf("非法时限 %q 应当被拒绝", s)
		}
	}
}

func TestDuplicateUserNameIsRejected(t *testing.T) {
	a := newTestAccess(t)
	if _, err := a.CreateUser(UserInput{Name: "李四", Role: RoleMember}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := a.CreateUser(UserInput{Name: "李四", Role: RoleMember}); err == nil {
		t.Error("同名成员应当被拒绝")
	}
}
