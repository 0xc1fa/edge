package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 这一层是本项目对宿主的唯一写入面。
//
// 只有三个动作，且都对应 GPU 预约台账的一次状态迁移：
// 挂载（approved → active）、摘卡（active → ended）、重启（让挂载在租户侧生效）。
// 刻意不提供「任意改配置」的口子：那种接口一旦存在，
// 迟早会被用来做一些台账上不留痕的事情。

// do 发一个带 body 的请求，并在需要时等待异步操作结束。
//
// 返回 async 是 Incus 对「运行中实例的写操作」的常态：
// 设备热插、重启都不可能在响应里同步完成。
// 不等待会把「已接受」误当成「已完成」—— 页面立刻刷新，
// 看到的却还是旧状态，管理员就会以为操作失败了而再点一次。
func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var payload *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", path, err)
		}
		payload = bytes.NewReader(b)
	}

	var req *http.Request
	var err error
	if payload != nil {
		req, err = http.NewRequestWithContext(ctx, method, c.base+path, payload)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, c.base+path, nil)
	}
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.write.Do(req)
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
	if r.Type == "async" && r.Operation != "" {
		return r.Metadata, c.waitOperation(ctx, r.Operation)
	}
	return r.Metadata, nil
}

// waitOperation 等一个异步操作走到终态。
//
// 光看 HTTP 200 是不够的：async 响应只是说「开始做了」。
// 真正的成败在 operation 的 err 字段里 —— 比如卡正被别的实例占着，
// 或者设备名撞车，这些都要到这里才暴露。
func (c *Client) waitOperation(ctx context.Context, op string) error {
	path := op + "/wait?timeout=60"
	if !strings.HasPrefix(op, "/") {
		path = "/1.0/operations/" + op + "/wait?timeout=60"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.write.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if r.Type == "error" {
		return fmt.Errorf("incus error %d: %s", r.ErrorCode, r.Error)
	}
	var o struct {
		Status     string `json:"status"`
		StatusCode int    `json:"status_code"`
		Err        string `json:"err"`
	}
	if err := json.Unmarshal(r.Metadata, &o); err != nil {
		// 部分版本在成功时不回 metadata；此时没有错误可言
		return nil
	}
	if o.Err != "" {
		return fmt.Errorf("incus 操作失败: %s", o.Err)
	}
	if strings.EqualFold(o.Status, "Failure") || o.StatusCode >= 400 {
		return fmt.Errorf("incus 操作失败: %s", o.Status)
	}
	return nil
}

// GetInstance 取单个实例的完整定义，含 devices 与 config。
func (c *Client) GetInstance(ctx context.Context, name string) (*Instance, error) {
	raw, err := c.get(ctx, "/1.0/instances/"+name)
	if err != nil {
		return nil, err
	}
	var inst Instance
	if err := json.Unmarshal(raw, &inst); err != nil {
		return nil, fmt.Errorf("decode instance %s: %w", name, err)
	}
	return &inst, nil
}

// AddDevice 给实例加一个设备。dev 里至少要有 name 与 type。
//
// 对运行中的实例这是热插：设备会立刻出现在实例里，
// 但容器内的 /dev/nvidia* 往往需要重启才注入 —— 那件事不在这里自动做，
// 由调用方把「要不要重启」交给管理员决定。
func (c *Client) AddDevice(ctx context.Context, instance string, dev map[string]string) error {
	if dev["name"] == "" {
		return fmt.Errorf("设备缺少 name")
	}
	if dev["type"] == "" {
		return fmt.Errorf("设备缺少 type")
	}
	_, err := c.do(ctx, http.MethodPost, "/1.0/instances/"+instance+"/devices", dev)
	return err
}

// RemoveDevice 摘掉实例上的一个设备。设备不存在时 Incus 会报错，
// 这个错误要如实往上抛 —— 「以为挂着其实没挂」是需要被看见的偏差。
func (c *Client) RemoveDevice(ctx context.Context, instance, devName string) error {
	if devName == "" {
		return fmt.Errorf("设备名为空")
	}
	_, err := c.do(ctx, http.MethodDelete, "/1.0/instances/"+instance+"/devices/"+devName, nil)
	return err
}

// PatchConfig 合并一组 config 键。
//
// 用 PATCH 而不是 PUT 是有意的：PUT 要求提交完整实例定义，
// 少给一个字段（比如 devices）就会被当成「删掉它」。
// 代价是 PATCH 删不掉键 —— 对本项目无所谓，nvidia.runtime 留着无害。
func (c *Client) PatchConfig(ctx context.Context, instance string, kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	_, err := c.do(ctx, http.MethodPatch, "/1.0/instances/"+instance, map[string]any{"config": kv})
	return err
}

// RestartInstance 重启实例。
// force 为 false 时走正常关机流程，给里面的进程留出退出的时间。
func (c *Client) RestartInstance(ctx context.Context, instance string, force bool) error {
	_, err := c.do(ctx, http.MethodPost, "/1.0/instances/"+instance+"/state", map[string]any{
		"action": "restart",
		"force":  force,
	})
	return err
}
