package api

import (
	"strings"
	"testing"

	"neo/internal/catalog"
	"neo/internal/store"
)

// sampleKey 是形似 ssh-keygen 产物的公钥（非真实凭证）。
const sampleKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGgvyMGVbHlqJ8W0UKSwNwfWfAqB9tRzR1pQ2nZ3k7yF alice@macbook"

func goodRequest() applyRequest {
	return applyRequest{
		Hostname:     "dev01",
		Flavor:       "s2.m4.d10",
		Image:        "ubuntu-22.04-base",
		GPUCount:     0,
		SSHPubKey:    sampleKey,
		Applicant:    "张三",
		Contact:      "13800138000",
		Purpose:      "跑一个内部小服务",
		DurationDays: 30,
	}
}

func TestValidateApplyAcceptsGoodRequest(t *testing.T) {
	if msg := validateApply(goodRequest(), map[string]bool{}); msg != "" {
		t.Errorf("合法申请被拒: %s", msg)
	}
}

// TestValidateApplyAcceptsMinimalRequest 锁住「表单做减法」：
// 主机名与 SSH 公钥都不填也必须能提交，由服务端生成/兜底。
func TestValidateApplyAcceptsMinimalRequest(t *testing.T) {
	req := goodRequest()
	req.Hostname = ""
	req.SSHPubKey = ""
	if msg := validateApply(req, map[string]bool{}); msg != "" {
		t.Errorf("最简申请应通过，被拒: %s", msg)
	}
}

// TestValidateApplyStillChecksProvidedHostname —— 选填不等于放弃校验：
// 填了非法主机名依然要拦下。
func TestValidateApplyStillChecksProvidedHostname(t *testing.T) {
	req := goodRequest()
	req.Hostname = "Bad_Name"
	if msg := validateApply(req, map[string]bool{}); msg == "" {
		t.Error("非法主机名（含下划线）应被拒绝")
	}
	if msg := validateApply(goodRequest(), map[string]bool{"dev01": true}); msg == "" {
		t.Error("与现有实例重名应被拒绝")
	}
}

func TestAutoHostnameIsValidAndAvoidsCollisions(t *testing.T) {
	taken := map[string]bool{}
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		name, err := autoHostname(taken)
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if !hostnameRe.MatchString(name) {
			t.Errorf("生成的主机名不符合命名规则: %s", name)
		}
		if seen[name] {
			t.Errorf("重复生成了同一主机名: %s", name)
		}
		seen[name] = true
		taken[name] = true
	}
}

func TestValidateApplyRejectsBadInput(t *testing.T) {
	cases := []struct {
		name  string
		mod   func(r *applyRequest)
		taken map[string]bool
	}{
		{"主机名大写", func(r *applyRequest) { r.Hostname = "Dev01" }, nil},
		{"主机名以数字开头", func(r *applyRequest) { r.Hostname = "1dev" }, nil},
		{"主机名含下划线", func(r *applyRequest) { r.Hostname = "dev_01" }, nil},
		{"主机名与现有实例冲突", func(r *applyRequest) {}, map[string]bool{"dev01": true}},
		{"公钥只有类型名", func(r *applyRequest) { r.SSHPubKey = "ssh-ed25519" }, nil},
		{"公钥类型不支持", func(r *applyRequest) { r.SSHPubKey = "gibberish " + sampleKey[12:] }, nil},
		{"粘贴的是私钥", func(r *applyRequest) {
			r.SSHPubKey = "-----BEGIN OPENSSH PRIVATE KEY----- b3BlbnNzaAAAAAAA " +
				"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		}, nil},
		{"申请人太短", func(r *applyRequest) { r.Applicant = "张" }, nil},
		{"申请人留空", func(r *applyRequest) { r.Applicant = "   " }, nil},
		{"联系方式不是手机也不是邮箱", func(r *applyRequest) { r.Contact = "微信 abc" }, nil},
		{"用途太短", func(r *applyRequest) { r.Purpose = "用" }, nil},
		{"时长超一年", func(r *applyRequest) { r.DurationDays = 400 }, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := goodRequest()
			c.mod(&req)
			taken := c.taken
			if taken == nil {
				taken = map[string]bool{}
			}
			if msg := validateApply(req, taken); msg == "" {
				t.Errorf("本应被拒绝，却通过了")
			}
		})
	}
}

func TestValidateApplyAcceptsEmailContact(t *testing.T) {
	req := goodRequest()
	req.Contact = "alice@example.com"
	if msg := validateApply(req, map[string]bool{}); msg != "" {
		t.Errorf("邮箱联系方式应可通过: %s", msg)
	}
}

func TestValidSSHKeyAcceptsCommonTypes(t *testing.T) {
	ok := []string{
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDexampleexampleAAAAAAAAAAAAAAAAAAAAAAAAAAAA==",
		sampleKey,
		// 真实长度量级的 ecdsa 公钥，用来守住 80 字符下限不误杀
		"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBHk3Xp1Rw8fVJXbN2rq5LkQ7mTzY8VcWJdKwZ1sN4vGhTQ9pRxMfLcEuajd5oP2wQABCDEF",
	}
	for _, k := range ok {
		if !validSSHKey(k) {
			t.Errorf("应接受: %.40s...", k)
		}
	}
}

func TestMaskContactKeepsUsefulHintOnly(t *testing.T) {
	cases := map[string]string{
		"13800138000":  "138****8000",
		"alice@ex.com": "al***@ex.com",
	}
	for in, want := range cases {
		if got := maskContact(in); got != want {
			t.Errorf("maskContact(%q) = %q, 想 %q", in, got, want)
		}
	}
}

// TestProvisionCommandMentionsGpu 保证带卡的申请不会被当成普通申请下发 ——
// 命令里必须显式提醒挂载步骤。
func TestProvisionCommandMentionsGpu(t *testing.T) {
	c := catalog.Default()
	gpu := store.Application{
		Hostname: "gpu01", Image: "ubuntu-22.04-base", Flavor: "s2.m4.d10",
		GPUModel: "rtx-4090", GPUCount: 2,
	}
	cmd := provisionCommand(gpu, c)
	if !strings.Contains(cmd, "挂载 2 张 RTX 4090") {
		t.Errorf("带卡申请的命令应写明型号与张数: %s", cmd)
	}
	if !strings.Contains(cmd, "gpu01") {
		t.Errorf("命令应带上主机名: %s", cmd)
	}

	plain := store.Application{
		Hostname: "dev01", Image: "ubuntu-22.04-base", Flavor: "s2.m4.d10",
	}
	if cmd := provisionCommand(plain, c); strings.Contains(cmd, "GPU") {
		t.Errorf("不带卡的命令不应出现 GPU 提醒: %s", cmd)
	}
}

// TestIsPublic 是整个访问控制模型的收口测试。
// 一旦有人误把 GET /api/applications 放开，这条会立刻报警。
func TestIsPublic(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/health", true},
		{"GET", "/api/session", true},
		{"POST", "/api/auth/redeem", true},
		{"GET", "/api/auth/redeem", false},
		{"GET", "/api/catalog", true},
		{"POST", "/api/applications", true}, // 匿名提交
		{"GET", "/api/applications", false}, // 管理员全量列表
		{"GET", "/api/applications/AP-20261010-7XK4", true},
		{"POST", "/api/applications/AP-20261010-7XK4/decision", false},
		{"GET", "/api/instances", false},
		{"GET", "/api/capacity", false},
	}
	for _, c := range cases {
		if got := isPublic(c.method, c.path); got != c.want {
			t.Errorf("isPublic(%s %s) = %v, 想 %v", c.method, c.path, got, c.want)
		}
	}
}

// TestRequiredLevel 守住「登录即可」与「必须管理员」的分界。
//
// 无口令登录引入成员角色之后，鉴权不再是一刀切：看实例是成员就能做的事，
// 管人、发证、踢会话不是。这里每多一条新接口，都应该有一条对应的断言，
// 否则某天把 GET /api/sessions 划给普通成员，控制台上的会话台账就向所有人敞开了。
func TestRequiredLevel(t *testing.T) {
	cases := []struct {
		method, path string
		want         level
	}{
		{"GET", "/api/health", levelAny},
		{"GET", "/api/session", levelAny},
		{"POST", "/api/auth/redeem", levelAny},
		{"POST", "/api/auth/login", levelAny},
		{"GET", "/api/catalog", levelAny},
		{"POST", "/api/applications", levelAny},
		{"GET", "/api/applications/AP-20261010-7XK4", levelAny},

		{"GET", "/api/instances", levelUser},
		{"GET", "/api/instances/dev01", levelUser},
		{"GET", "/api/series/dev01", levelUser},
		{"GET", "/api/capacity", levelUser},

		{"GET", "/api/applications", levelAdmin},
		{"POST", "/api/applications/AP-20261010-7XK4/decision", levelAdmin},
		{"GET", "/api/users", levelAdmin},
		{"POST", "/api/users", levelAdmin},
		{"PATCH", "/api/users/u_1a2b3c", levelAdmin},
		{"DELETE", "/api/users/u_1a2b3c", levelAdmin},
		{"POST", "/api/users/u_1a2b3c/invite", levelAdmin},
		{"POST", "/api/users/u_1a2b3c/revoke-sessions", levelAdmin},
		{"GET", "/api/invites", levelAdmin},

		// GPU 预约：排期谁都能看、谁都能申请；动宿主的动作只有管理员
		{"GET", "/api/gpu-bookings", levelUser},
		{"POST", "/api/gpu-bookings", levelUser},
		{"POST", "/api/gpu-bookings/GU-20261017-7XK4/decision", levelAdmin},
		{"POST", "/api/gpu-bookings/GU-20261017-7XK4/attach", levelAdmin},
		{"POST", "/api/gpu-bookings/GU-20261017-7XK4/detach", levelAdmin},
		{"POST", "/api/gpu-bookings/GU-20261017-7XK4/cancel", levelUser},
		{"POST", "/api/instances/tenant01/restart", levelAdmin},
		{"POST", "/api/invites/abc123/revoke", levelAdmin},
		{"GET", "/api/sessions", levelAdmin},
		{"POST", "/api/sessions/abc123/revoke", levelAdmin},
	}
	for _, c := range cases {
		if got := requiredLevel(c.method, c.path); got != c.want {
			t.Errorf("requiredLevel(%s %s) = %v, 想 %v", c.method, c.path, got, c.want)
		}
	}
}
