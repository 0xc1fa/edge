package incus

import (
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 实例类型的展示语。刻意区分「容器」与「虚拟机」：宿主当前跑的是系统容器，
// 一律叫虚拟机会让租户误以为是 KVM 级隔离。
var typeLabels = map[string]string{
	"container":       "容器",
	"virtual-machine": "虚拟机",
}

var statusLabels = map[string]string{
	"Running":  "运行中",
	"Stopped":  "已停止",
	"Frozen":   "已冻结",
	"Freezing": "冻结中",
	"Thawed":   "已解冻",
	"Starting": "启动中",
	"Stopping": "停止中",
	"Aborting": "中止中",
}

// Nic 是实例内一块网卡的控制台视图。
type Nic struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Type   string `json:"type"`
	Hwaddr string `json:"hwaddr"`
	IPv4   string `json:"ipv4"`
	IPv6   string `json:"ipv6"`
	RxByte int64  `json:"rx_bytes"`
	TxByte int64  `json:"tx_bytes"`
}

// GPU 是挂载到实例的一块 GPU。
type GPU struct {
	Device  string `json:"device"`
	Type    string `json:"type"`
	PCI     string `json:"pci"`
	Product string `json:"product"`
}

// Ingress 是一条宿主端口转发，目前承载 SSH 入口。
type Ingress struct {
	Device   string `json:"device"`
	Listen   string `json:"listen"`
	Port     int    `json:"port"`
	Connect  string `json:"connect"`
	Purpose  string `json:"purpose"`
}

// View 是控制台交付给前端的实例视图。
type View struct {
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	TypeLabel     string    `json:"type_label"`
	Status        string    `json:"status"`
	StatusLabel   string    `json:"status_label"`
	Running       bool      `json:"running"`
	Architecture  string    `json:"architecture"`
	ImageOS       string    `json:"image_os"`
	ImageRelease  string    `json:"image_release"`
	ImageDesc     string    `json:"image_desc"`
	CreatedAt     string    `json:"created_at"`
	StartedAt     string    `json:"started_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	QuotaCPU      string    `json:"quota_cpu"`
	QuotaMemory   string    `json:"quota_memory"`
	QuotaDisk     string    `json:"quota_disk"`
	DiskUsed      int64     `json:"disk_used"`
	DiskTotal     int64     `json:"disk_total"`
	DiskPercent   int       `json:"disk_percent"`
	PID           int64     `json:"pid"`
	Processes     int64     `json:"processes"`
	CPUSeconds    float64   `json:"cpu_seconds"`
	MemUsage      int64     `json:"mem_usage"`
	MemPeak       int64     `json:"mem_peak"`
	MemTotal      int64     `json:"mem_total"`
	MemPercent    int       `json:"mem_percent"`
	SwapUsage     int64     `json:"swap_usage"`
	Profiles      []string  `json:"profiles"`
	Nics          []Nic     `json:"nics"`
	GPUs          []GPU     `json:"gpus"`
	Ingress       []Ingress `json:"ingress"`
}

// NewView 把实例定义与实时状态合成控制台视图。
// now 由调用方注入，单测可固定时间从而得到确定的运行时长。
func NewView(inst Instance, st *State, now time.Time) View {
	v := View{
		Name:         inst.Name,
		Type:         inst.Type,
		TypeLabel:    humanize(inst.Type, typeLabels),
		Status:       inst.Status,
		StatusLabel:  humanize(inst.Status, statusLabels),
		Running:      inst.Status == "Running",
		Architecture: inst.Architecture,
		ImageOS:      inst.Config["image.os"],
		ImageRelease: inst.Config["image.release"],
		ImageDesc:    inst.Config["image.description"],
		QuotaCPU:     dash(inst.Config["limits.cpu"]),
		QuotaMemory:  dash(inst.Config["limits.memory"]),
		Profiles:     inst.Profiles,
		Nics:         []Nic{},
		GPUs:         []GPU{},
		Ingress:      []Ingress{},
	}
	if v.Profiles == nil {
		v.Profiles = []string{}
	}
	if !inst.CreatedAt.IsZero() {
		v.CreatedAt = inst.CreatedAt.UTC().Format(time.RFC3339)
	}

	// 根盘配额：来自 devices.root.size
	if root, ok := inst.Devices["root"]; ok {
		v.QuotaDisk = dash(root["size"])
	}

	// GPU 设备：判据是实例自身的 devices，不查宿主 nvidia-smi
	for _, name := range sortedKeys(inst.Devices) {
		d := inst.Devices[name]
		if d["type"] != "gpu" {
			continue
		}
		v.GPUs = append(v.GPUs, GPU{
			Device:  name,
			Type:    d["gputype"],
			PCI:     d["pci"],
			Product: d["productid"],
		})
	}

	// 端口转发：目前只有 SSH 入口这一种形态
	for _, name := range sortedKeys(inst.Devices) {
		d := inst.Devices[name]
		if d["type"] != "proxy" {
			continue
		}
		ing := Ingress{Device: name, Listen: dash(d["listen"]), Connect: dash(d["connect"])}
		_, addrPort, ok := strings.Cut(d["listen"], "tcp:")
		if ok {
			host, portStr, err := net.SplitHostPort(addrPort)
			if err == nil {
				p, _ := strconv.Atoi(portStr)
				ing.Listen, ing.Port = host, p
			} else {
				ing.Listen = addrPort
			}
		}
		ing.Purpose = "tcp"
		if strings.HasSuffix(d["connect"], ":22") {
			ing.Purpose = "ssh"
		}
		v.Ingress = append(v.Ingress, ing)
	}

	if st == nil {
		return v
	}

	v.PID = st.Pid
	v.Processes = st.Processes
	v.CPUSeconds = float64(st.CPU.Usage) / 1e9
	v.MemUsage = st.Memory.Usage
	v.MemPeak = st.Memory.UsagePeak
	v.MemTotal = st.Memory.Total
	v.MemPercent = percent(st.Memory.Usage, st.Memory.Total)
	v.SwapUsage = st.Memory.SwapUsage
	if !st.StartedAt.IsZero() {
		v.StartedAt = st.StartedAt.UTC().Format(time.RFC3339)
		if v.Running {
			v.UptimeSeconds = int64(now.Sub(st.StartedAt).Seconds())
		}
	}

	if root, ok := st.Disk["root"]; ok {
		v.DiskUsed, v.DiskTotal = root.Usage, root.Total
		v.DiskPercent = percent(root.Usage, root.Total)
	}

	for _, name := range sortedNet(st.Network) {
		n := st.Network[name]
		nic := Nic{
			Name:   name,
			State:  n.State,
			Type:   n.Type,
			Hwaddr: n.Hwaddr,
			RxByte: n.Counters.BytesReceived,
			TxByte: n.Counters.BytesSent,
		}
		for _, a := range n.Addresses {
			switch {
			case a.Family == "inet" && a.Scope == "global":
				nic.IPv4 = a.Address
			case a.Family == "inet6" && a.Scope == "global":
				nic.IPv6 = a.Address
			}
		}
		v.Nics = append(v.Nics, nic)
	}

	return v
}

// QuotaMemoryBytes 把内存配额字符串转成字节，用于容量汇总。
func (v View) QuotaMemoryBytes() int64 {
	return ParseSize(v.QuotaMemory)
}

// QuotaCPUNum 返回 CPU 配额的核心数，未限制返回 0。
func (v View) QuotaCPUNum() int {
	n, err := strconv.Atoi(v.QuotaCPU)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// QuotaDiskBytes 返回根盘配额字节数。
func (v View) QuotaDiskBytes() int64 {
	return ParseSize(v.QuotaDisk)
}

// ParseSize 解析 Incus 的大小字符串（KiB/MiB/GiB/TiB 及其不带 i 的写法）。
// 解析失败返回 0 —— 容量汇总宁可视作未限制，也不要因脏数据 panic。
func ParseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30}, {"TB", 1 << 40},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num := strings.TrimSuffix(s, u.suffix)
			f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil {
				return 0
			}
			return int64(f * float64(u.mult))
		}
	}
	// 不带单位：Incus 视作字节
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// percent 计算占比，total<=0 时返回 0，避免除零。
func percent(x, total int64) int {
	if total <= 0 {
		return 0
	}
	p := float64(x) / float64(total) * 100
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return int(p + 0.5)
}

func humanize(k string, m map[string]string) string {
	if v, ok := m[k]; ok && v != "" {
		return v
	}
	return k
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// sortedKeys 让 map 遍历顺序稳定：JSON 输出与人眼阅读都需要确定性。
func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedNet(m map[string]Network) []string {
	keys := sortedKeys(m)
	// lo 排到最后：它是回环，业务上不关心
	sort.SliceStable(keys, func(i, j int) bool {
		return keys[i] != "lo" && keys[j] == "lo"
	})
	return keys
}
