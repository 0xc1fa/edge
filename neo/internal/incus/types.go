package incus

import (
	"encoding/json"
	"time"
)

// Response 是 Incus REST 的统一信封。
// sync 请求的结果落在 Metadata；出错时 Type 为 error，细节在 Error/ErrorCode。
type Response struct {
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	StatusCode int             `json:"status_code"`
	Operation  string          `json:"operation"`
	ErrorCode  int             `json:"error_code"`
	Error      string          `json:"error"`
	Metadata   json.RawMessage `json:"metadata"`
}

// Instance 是 /1.0/instances?recursion=1 返回的单个实例。
// 注意：Incus 7 的列表响应里 state 为空对象，实时运行数据必须单独取 /state。
type Instance struct {
	Name         string                       `json:"name"`
	Type         string                       `json:"type"`
	Status       string                       `json:"status"`
	StatusCode   int                          `json:"status_code"`
	Architecture string                       `json:"architecture"`
	CreatedAt    time.Time                    `json:"created_at"`
	LastUsedAt   time.Time                    `json:"last_used_at"`
	Location     string                       `json:"location"`
	Profiles     []string                     `json:"profiles"`
	Config       map[string]string            `json:"config"`
	Devices      map[string]map[string]string `json:"devices"`
}

// State 是 /1.0/instances/{name}/state 返回的实时运行状态。
type State struct {
	Status     string `json:"status"`
	StatusCode int    `json:"status_code"`
	Pid        int64  `json:"pid"`
	Processes  int64  `json:"processes"`
	StartedAt  time.Time `json:"started_at"`

	CPU struct {
		// Usage 是累计 CPU 时间，单位是纳秒
		Usage         int64 `json:"usage"`
		AllocatedTime int64 `json:"allocated_time"`
	} `json:"cpu"`

	Memory struct {
		Usage         int64 `json:"usage"`
		UsagePeak     int64 `json:"usage_peak"`
		Total         int64 `json:"total"`
		SwapUsage     int64 `json:"swap_usage"`
		SwapUsagePeak int64 `json:"swap_usage_peak"`
	} `json:"memory"`

	Network map[string]Network `json:"network"`
	Disk    map[string]DiskUsg `json:"disk"`
	OSInfo  map[string]string  `json:"os_info"`
}

// Network 是实例内一块网卡的视图。
type Network struct {
	Addresses []NetAddress `json:"addresses"`
	Counters  NetCounters  `json:"counters"`
	Hwaddr    string       `json:"hwaddr"`
	HostName  string       `json:"host_name"`
	Mtu       int          `json:"mtu"`
	State     string       `json:"state"`
	Type      string       `json:"type"`
}

// NetAddress 是网卡上的一个地址条目。
type NetAddress struct {
	Family  string `json:"family"`
	Address string `json:"address"`
	Netmask string `json:"netmask"`
	Scope   string `json:"scope"`
}

// NetCounters 是网卡收发计数，单位字节/包。
type NetCounters struct {
	BytesReceived int64 `json:"bytes_received"`
	BytesSent     int64 `json:"bytes_sent"`
	PacketsRecv   int64 `json:"packets_received"`
	PacketsSent   int64 `json:"packets_sent"`
	ErrorsRecv    int64 `json:"errors_received"`
	ErrorsSent    int64 `json:"errors_sent"`
}

// DiskUsg 是某个挂载点的磁盘用量，单位字节。
type DiskUsg struct {
	Usage int64 `json:"usage"`
	Total int64 `json:"total"`
}

// Resources 是 /1.0/resources 返回的宿主资源。
type Resources struct {
	CPU struct {
		Total   int    `json:"total"`
		Sockets []struct {
			Name string `json:"name"`
		} `json:"sockets"`
	} `json:"cpu"`
	Memory struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
	} `json:"memory"`
}
