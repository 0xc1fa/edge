package incus

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loadFixtures 读取真实抓取的 Incus 响应。
// 这些文件来自宿主机的一次真实请求，是适配层唯一的行为约定：
// Incus 升级或回复结构变化时，本测试先于用户发现。
func loadFixtures(t *testing.T) (Instance, State) {
	t.Helper()

	var listResp Response
	raw, err := os.ReadFile(filepath.Join("testdata", "instances.json"))
	if err != nil {
		t.Fatalf("读取实例 fixture 失败: %v", err)
	}
	if err := json.Unmarshal(raw, &listResp); err != nil {
		t.Fatalf("解析实例 fixture 失败: %v", err)
	}
	var insts []Instance
	if err := json.Unmarshal(listResp.Metadata, &insts); err != nil {
		t.Fatalf("解出实例列表失败: %v", err)
	}
	if len(insts) == 0 {
		t.Fatal("fixture 中没有实例")
	}

	var stResp Response
	rawState, err := os.ReadFile(filepath.Join("testdata", "tenant01-state.json"))
	if err != nil {
		t.Fatalf("读取状态 fixture 失败: %v", err)
	}
	if err := json.Unmarshal(rawState, &stResp); err != nil {
		t.Fatalf("解析状态 fixture 失败: %v", err)
	}
	var st State
	if err := json.Unmarshal(stResp.Metadata, &st); err != nil {
		t.Fatalf("解出实例状态失败: %v", err)
	}
	return insts[0], st
}

func TestViewFromGoldenFixture(t *testing.T) {
	inst, st := loadFixtures(t)
	v := NewView(inst, &st, time.Now())

	if v.Name != "tenant01" {
		t.Errorf("name = %q, 想 tenant01", v.Name)
	}
	// 宿主当前跑的是系统容器，这里必须叫「容器」而不是「虚拟机」
	if v.Type != "container" || v.TypeLabel != "容器" {
		t.Errorf("type = %q/%q, 想 container/容器", v.Type, v.TypeLabel)
	}
	if v.Status != "Running" || v.StatusLabel != "运行中" || !v.Running {
		t.Errorf("status = %q/%q running=%v, 想 Running/运行中/true", v.Status, v.StatusLabel, v.Running)
	}
	if v.Architecture != "x86_64" {
		t.Errorf("architecture = %q, 想 x86_64", v.Architecture)
	}
	if v.ImageOS != "Ubuntu" || v.ImageRelease != "jammy" {
		t.Errorf("image = %s %s, 想 Ubuntu jammy", v.ImageOS, v.ImageRelease)
	}

	// 配额
	if v.QuotaCPU != "2" || v.QuotaMemory != "4GiB" || v.QuotaDisk != "20GiB" {
		t.Errorf("quota = cpu=%s mem=%s disk=%s, 想 2/4GiB/20GiB",
			v.QuotaCPU, v.QuotaMemory, v.QuotaDisk)
	}

	// 磁盘：state.disk.root 直接给出用量，比 exec df 更快也更准
	if v.DiskUsed != 11302543360 || v.DiskTotal != 21474836480 {
		t.Errorf("disk = %d/%d, 想 11302543360/21474836480", v.DiskUsed, v.DiskTotal)
	}
	if v.DiskPercent != 53 {
		t.Errorf("disk_percent = %d, 想 53", v.DiskPercent)
	}

	// 内存
	if v.MemTotal != 4294967296 || v.MemUsage != 1287929856 {
		t.Errorf("mem = %d/%d, 想 1287929856/4294967296", v.MemUsage, v.MemTotal)
	}
	if v.PID != 297642 || v.Processes != 20 {
		t.Errorf("pid/proc = %d/%d, 想 297642/20", v.PID, v.Processes)
	}

	// CPU 时间：纳秒转秒
	if math.Abs(v.CPUSeconds-15475.590941) > 0.001 {
		t.Errorf("cpu_seconds = %f, 想 15475.590941", v.CPUSeconds)
	}

	// 网络入口
	if len(v.Nics) == 0 {
		t.Fatal("没有解析出网卡")
	}
	if v.Nics[0].IPv4 != "10.66.0.243" {
		t.Errorf("ipv4 = %q, 想 10.66.0.243", v.Nics[0].IPv4)
	}

	// SSH 入口：proxy device 的 listen/connect
	if len(v.Ingress) != 1 {
		t.Fatalf("ingress 数量 = %d, 想 1", len(v.Ingress))
	}
	ing := v.Ingress[0]
	if ing.Port != 2201 || ing.Listen != "10.8.0.8" || ing.Purpose != "ssh" {
		t.Errorf("ingress = %+v, 想 listen=10.8.0.8 port=2201 purpose=ssh", ing)
	}

	// 该实例未挂载 GPU
	if len(v.GPUs) != 0 {
		t.Errorf("gpus = %+v, 想空", v.GPUs)
	}
}

func TestUptimeIsDerivedFromStartedAt(t *testing.T) {
	inst, st := loadFixtures(t)
	now := st.StartedAt.Add(90 * time.Minute)
	v := NewView(inst, &st, now)
	if v.UptimeSeconds != 5400 {
		t.Errorf("uptime_seconds = %d, 想 5400", v.UptimeSeconds)
	}
	if v.StartedAt != st.StartedAt.UTC().Format(time.RFC3339) {
		t.Errorf("started_at = %q, 想 %q", v.StartedAt, st.StartedAt.UTC().Format(time.RFC3339))
	}
}

func TestStoppedInstanceWithoutState(t *testing.T) {
	inst, _ := loadFixtures(t)
	inst.Status = "Stopped"
	v := NewView(inst, nil, time.Now())

	if v.Running {
		t.Error("停机实例的 running 应为 false")
	}
	if v.UptimeSeconds != 0 {
		t.Errorf("停机实例 uptime = %d, 想 0", v.UptimeSeconds)
	}
	if v.StatusLabel != "已停止" {
		t.Errorf("status_label = %q, 想 已停止", v.StatusLabel)
	}
	// 配额与端口转发来自实例定义，停机也应当保留；
	// 网卡/进程等量只有运行着才存在，停机必须为空数组而不是 null。
	for name, arr := range map[string]int{
		"nics": len(v.Nics),
		"gpus": len(v.GPUs),
	} {
		if arr != 0 {
			t.Errorf("%s 应为空数组，长度 %d", name, arr)
		}
	}
	if len(v.Ingress) != 1 {
		t.Errorf("停机实例的端口转发也应保留，实际 %d 条", len(v.Ingress))
	}
	if raw, err := json.Marshal(v); err != nil {
		t.Fatalf("序列化失败: %v", err)
	} else if strings.Contains(string(raw), `"nics":null`) {
		t.Error("nics 序列化成了 null")
	}
}

func TestQuotaHelpers(t *testing.T) {
	inst, st := loadFixtures(t)
	v := NewView(inst, &st, time.Now())

	if got := v.QuotaMemoryBytes(); got != 4294967296 {
		t.Errorf("QuotaMemoryBytes() = %d, 想 4294967296", got)
	}
	if got := v.QuotaDiskBytes(); got != 21474836480 {
		t.Errorf("QuotaDiskBytes() = %d, 想 21474836480", got)
	}
	if got := v.QuotaCPUNum(); got != 2 {
		t.Errorf("QuotaCPUNum() = %d, 想 2", got)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"4GiB", 4294967296},
		{"20GiB", 21474836480},
		{"512MiB", 536870912},
		{"1TB", 1099511627776},
		{"1024", 1024},
		{"", 0},
		{"-", 0},
		{"abc", 0},
	}
	for _, c := range cases {
		if got := ParseSize(c.in); got != c.want {
			t.Errorf("ParseSize(%q) = %d, 想 %d", c.in, got, c.want)
		}
	}
}

func TestPercentNeverDividesByZero(t *testing.T) {
	if got := percent(100, 0); got != 0 {
		t.Errorf("percent(100, 0) = %d, 想 0", got)
	}
	if got := percent(200, 100); got != 100 {
		t.Errorf("percent(200, 100) = %d, 想 100（超卖也要封顶）", got)
	}
}
