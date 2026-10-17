package api

import (
	"testing"
	"time"

	"neo/internal/store"
)

// TestCanControl 钉住归属判定的三条边界。
//
// 重点是第三条：没登记属主的实例谁都不能动。这不是洁癖 ——
// 宿主上现在就跑着一台手工建的 tenant01，台账里没有它。
// 对这种情况放行任何登录成员，等于让陌生人能动一台不属于任何人的机器。
func TestCanControl(t *testing.T) {
	cases := []struct {
		name    string
		isAdmin bool
		user    string
		owner   string
		want    bool
	}{
		{"管理员不受限", true, "任何人", "另一个人", true},
		{"管理员对无主实例也不受限", true, "管理员", "", true},
		{"申请人本人可以", false, "张三", "张三", true},
		{"别人不行", false, "李四", "张三", false},
		{"实例没登记属主，谁都不行", false, "张三", "", false},
		{"会话里没有名字，不行", false, "", "张三", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canControl(c.isAdmin, c.user, c.owner); got != c.want {
				t.Errorf("canControl(%v, %q, %q) = %v，想 %v", c.isAdmin, c.user, c.owner, got, c.want)
			}
		})
	}
}

// TestInstanceOwnerFromApplications 属主只能从开通台账推。
//
// 刻意**不**用 GPU 预约台账当来源：那些记录是会被清理的
// （删除接口就是为清测试数据加的），清理完之后归属就凭空消失了 ——
// 一个「昨天还能用、今天按钮全灰」的功能比没有这个功能更糟。
func TestInstanceOwnerFromApplications(t *testing.T) {
	apps, err := store.NewAppStore(t.TempDir())
	if err != nil {
		t.Fatalf("建台账失败: %v", err)
	}
	if _, err := apps.Create(store.Application{
		Hostname:  "dev01",
		Applicant: "张三",
		Flavor:    "s2.m4.d10",
		Image:     "ubuntu-22.04-base",
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	// 一条被驳回的申请同样算数：机器已经开出来了，归属不会因为
	// 申请单被驳回而改变。真正该看的是「这台机器登记在谁名下」。
	if _, err := apps.Create(store.Application{
		Hostname:  "dev02",
		Applicant: "李四",
		Status:    store.StatusRejected,
	}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}

	s := &Server{cfg: Config{Apps: apps}}
	if got := s.instanceOwner("dev01"); got != "张三" {
		t.Errorf("dev01 的属主 = %q，想 张三", got)
	}
	if got := s.instanceOwner("dev02"); got != "李四" {
		t.Errorf("dev02 的属主 = %q，想 李四", got)
	}
	if got := s.instanceOwner("tenant01"); got != "" {
		t.Errorf("没登记的实例属主应为空，实际 %q", got)
	}

	// 归属台账优先于申请台账：那是管理员显式分配的结果，
	// 必须盖过申请时填的那个名字。换人之后还按老名字放行，
	// 前一个人就仍然能动这台机器。
	owners, err := store.NewOwnerStore(t.TempDir())
	if err != nil {
		t.Fatalf("建归属台账失败: %v", err)
	}
	if _, err := owners.Set("dev01", "王五", "管理员", time.Now()); err != nil {
		t.Fatalf("分配失败: %v", err)
	}
	s2 := &Server{cfg: Config{Apps: apps, Owners: owners}}
	if got := s2.instanceOwner("dev01"); got != "王五" {
		t.Errorf("分配之后 dev01 的属主 = %q，想 王五", got)
	}
	// 没分配过的仍然走申请台账
	if got := s2.instanceOwner("dev02"); got != "李四" {
		t.Errorf("dev02 的属主 = %q，想 李四", got)
	}
	// 台账没启用时不能崩：退回申请台账那条路
	s3 := &Server{cfg: Config{Apps: apps}}
	if got := s3.instanceOwner("dev01"); got != "张三" {
		t.Errorf("无归属台账时 dev01 的属主 = %q，想 张三", got)
	}
}
