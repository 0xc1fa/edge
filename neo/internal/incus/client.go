package incus

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Client 通过 unix socket 访问 Incus REST API。
// 不引入 Incus 官方 Go client：本项目只消费几个端点，自持可以避免
// 一整套间接依赖，也让 golden fixture 成为唯一的行为约定。
//
// 绝大多数调用是读。写操作只有三个：给实例加设备、摘设备、重启实例 ——
// 它们全部来自管理员在页面上对一条 GPU 预约的一次点击，
// 并且每一次都会在那条预约上留下时间戳（什么时候挂的、什么时候摘的）。
// 换句话说，写操作不是「随手改宿主的口子」，而是预约台账的一次状态迁移。
type Client struct {
	base string
	// 读操作超时短：对账每 10 秒一轮，卡住的读必须尽快失败
	http *http.Client
	// 写操作超时长：设备热插与重启是异步操作，要等它真正落地
	write *http.Client
}

// NewClient 建立到指定 socket 的客户端。
func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		base:  "http://incus",
		http:  &http.Client{Timeout: 10 * time.Second, Transport: transport},
		write: &http.Client{Timeout: 90 * time.Second, Transport: transport},
	}
}

func (c *Client) get(ctx context.Context, path string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if r.Type == "error" {
		return nil, fmt.Errorf("incus error %d: %s", r.ErrorCode, r.Error)
	}
	return r.Metadata, nil
}

// Ping 探测 socket 是否可达、协议是否可解析。
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.get(ctx, "/1.0")
	return err
}

// ListInstances 列出全部实例（含 config/devices，不含实时 state）。
func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	raw, err := c.get(ctx, "/1.0/instances?recursion=1")
	if err != nil {
		return nil, err
	}
	var list []Instance
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode instances: %w", err)
	}
	return list, nil
}

// GetState 取单个实例的实时运行状态。停机实例仍会返回，但字段多为零值。
func (c *Client) GetState(ctx context.Context, name string) (*State, error) {
	raw, err := c.get(ctx, "/1.0/instances/"+name+"/state")
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode state of %s: %w", name, err)
	}
	return &s, nil
}

// GetResources 取宿主总体资源，用于容量卡片。
func (c *Client) GetResources(ctx context.Context) (*Resources, error) {
	raw, err := c.get(ctx, "/1.0/resources")
	if err != nil {
		return nil, err
	}
	var r Resources
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode resources: %w", err)
	}
	return &r, nil
}
