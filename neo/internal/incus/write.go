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
	return c.doWith(ctx, method, path, body, "")
}

// doWith 与 do 相同，额外带上 If-Match（ETag）。
//
// 整份提交实例定义时必须带它：从读回来到写回去之间，
// 管理员可能刚在宿主上用命令行改过这台实例。
// 带了 ETag，Incus 会拒绝这次覆盖（412），而不是让后写的人悄悄赢。
func (c *Client) doWith(ctx context.Context, method, path string, body any, etag string) (json.RawMessage, error) {
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
	if etag != "" {
		req.Header.Set("If-Match", etag)
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
	inst, _, err := c.fetchForUpdate(ctx, name)
	return inst, err
}

// fetchForUpdate 取回实例定义，连它的 ETag 一起。
//
// 单独留这一个方法而不是顺手用 get()，是因为改设备必须先读回整份定义：
// 提交的 body 少一个字段就等于把那个字段清掉，而 ETag 是这次读改写唯一的并发保护。
func (c *Client) fetchForUpdate(ctx context.Context, name string) (*Instance, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/1.0/instances/"+name, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var r Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, "", fmt.Errorf("decode /1.0/instances/%s: %w", name, err)
	}
	if r.Type == "error" {
		return nil, "", fmt.Errorf("incus error %d: %s", r.ErrorCode, r.Error)
	}
	var inst Instance
	if err := json.Unmarshal(r.Metadata, &inst); err != nil {
		return nil, "", fmt.Errorf("decode instance %s: %w", name, err)
	}
	return &inst, resp.Header.Get("ETag"), nil
}

// instancePut 是 PUT /1.0/instances/{name} 的请求体。
//
// 字段集合照抄官方 CLI 提交的那一份（`incus --debug config device add` 可见）：
// 架构、config、devices、ephemeral、profiles、stateful、description。
// 不照抄的代价是丢字段 —— 而 PUT 的语义是整份替换，漏掉 volatile.* 这类键
// 会被当成「把主机上的这些内部记录删掉」。
type instancePut struct {
	Architecture string                       `json:"architecture"`
	Config       map[string]string            `json:"config"`
	Devices      map[string]map[string]string `json:"devices"`
	Ephemeral    bool                         `json:"ephemeral"`
	Profiles     []string                     `json:"profiles"`
	Stateful     bool                         `json:"stateful"`
	Description  string                       `json:"description"`
}

// updateInstance 读-改-写整份实例定义。
//
// 为什么非得整份提交：Incus 的 REST 里**没有**「加一个设备」「删一个设备」这种端点 ——
// POST /1.0/instances/{n}/devices 与 DELETE /1.0/instances/{n}/devices/{d} 实测都返回 404。
// 设备是实例定义的一部分，只能改完再整份 PUT 回去；
// 官方 CLI 的 config device add/remove 走的正是这条路
// （GET → PUT 带 If-Match → 等 operation 落地）。
//
// 代价是一次改动要提交整台实例的定义，所以提交前必须先读回最新的那一份，
// 并带上 ETag —— 否则从读到写之间别人在宿主上的改动会被这一次提交悄悄覆盖。
func (c *Client) updateInstance(ctx context.Context, instance string, mutate func(*Instance) error) error {
	inst, etag, err := c.fetchForUpdate(ctx, instance)
	if err != nil {
		return err
	}
	if inst.Devices == nil {
		inst.Devices = map[string]map[string]string{}
	}
	if err := mutate(inst); err != nil {
		return err
	}
	_, err = c.doWith(ctx, http.MethodPut, "/1.0/instances/"+instance, instancePut{
		Architecture: inst.Architecture,
		Config:       inst.Config,
		Devices:      inst.Devices,
		Ephemeral:    inst.Ephemeral,
		Profiles:     inst.Profiles,
		Stateful:     inst.Stateful,
		Description:  inst.Description,
	}, etag)
	return err
}

// updateDevices 只改设备表，其余原样带回去。
func (c *Client) updateDevices(ctx context.Context, instance string, mutate func(map[string]map[string]string) error) error {
	return c.updateInstance(ctx, instance, func(inst *Instance) error {
		return mutate(inst.Devices)
	})
}

// AddDevice 给实例加一个设备。dev 里至少要有 name 与 type。
//
// 对运行中的实例这是热插：设备会立刻出现在实例里，
// 但容器内的 /dev/nvidia* 往往需要重启才注入 —— 那件事不在这里自动做，
// 由调用方把「要不要重启」交给管理员决定。
//
// 同名设备已存在时：内容一致就当这次已经做完了（重试的场景会走到这里），
// 内容不同则拒绝覆盖 —— 那多半是有人在宿主上手工挂的卡，
// 覆盖掉它比报错更糟。
func (c *Client) AddDevice(ctx context.Context, instance string, dev map[string]string) error {
	if dev["name"] == "" {
		return fmt.Errorf("设备缺少 name")
	}
	if dev["type"] == "" {
		return fmt.Errorf("设备缺少 type")
	}
	name := dev["name"]
	// name 不是设备的属性，它是 devices 这张映射的键；
	// 混进属性里会让 Incus 把它当成一条未知配置项收下来
	spec := make(map[string]string, len(dev))
	for k, v := range dev {
		if k == "name" {
			continue
		}
		spec[k] = v
	}
	return c.updateDevices(ctx, instance, func(devices map[string]map[string]string) error {
		if old, ok := devices[name]; ok && !sameDevice(old, spec) {
			return fmt.Errorf("实例上已存在同名设备 %s（%v），未做覆盖", name, old)
		}
		devices[name] = spec
		return nil
	})
}

// RemoveGPUDevice 摘掉一个 GPU 设备，并把痕迹收干净。
//
// 设备已经不在了不算错：这里要达成的是「卡不在实例上」这个结果，
// 而管理员可能已经在宿主上用命令行摘过了（或它从来没挂上去）。
// 报一个错只会让这条预约永远停在「使用中」，到期巡检每个周期失败一次。
//
// 摘完之后如果这台实例上一个 GPU 设备都不剩，就把 nvidia.runtime 一起清掉：
// 留着它，容器里会一直挂着 /dev/nvidia-uvm、/dev/nvidiactl 这些驱动节点，
// 与「这台机器现在没有卡」不符。宿主脚本 tenant-ctl.sh 的 gpu off 正是这么收尾的 ——
// 命令行与页面必须收在同一个状态上，否则两边交替操作过一次之后，
// 实例的最终配置取决于「最后一次是谁按的」，那就无从判断了。
func (c *Client) RemoveGPUDevice(ctx context.Context, instance, devName string) error {
	if devName == "" {
		return fmt.Errorf("设备名为空")
	}
	return c.updateInstance(ctx, instance, func(inst *Instance) error {
		delete(inst.Devices, devName)
		if !hasGPUDevice(inst.Devices) {
			delete(inst.Config, nvidiaRuntimeKey)
		}
		return nil
	})
}

// nvidiaRuntimeKey 是 Incus 把显卡驱动栈注入容器的开关。
const nvidiaRuntimeKey = "nvidia.runtime"

func hasGPUDevice(devices map[string]map[string]string) bool {
	for _, d := range devices {
		if d["type"] == "gpu" {
			return true
		}
	}
	return false
}

// sameDevice 判断两份设备定义是否一致，用于识别「重复提交」而不是「要覆盖别人的东西」。
func sameDevice(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
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
//
// 用 PUT 而不是 POST：状态变更端点只认 PUT，POST 会得到一个
// 501 not implemented —— 而且这个错在页面上只是「重启失败」四个字，
// 看不出是方法用错了。官方 CLI 的 `incus --debug restart` 同样是 PUT。
func (c *Client) RestartInstance(ctx context.Context, instance string, force bool) error {
	_, err := c.do(ctx, http.MethodPut, "/1.0/instances/"+instance+"/state", map[string]any{
		"action": "restart",
		"force":  force,
	})
	return err
}

// StartInstance 开机。
//
// 对已经在跑的实例执行 start 不会报错（Incus 认为它已经满足了），
// 所以前端不必先查状态再决定按钮亮不亮 —— 但页面仍然只按状态显示，
// 因为对一个停机实例亮着「开机」按钮会让人以为机器还活着。
func (c *Client) StartInstance(ctx context.Context, instance string) error {
	_, err := c.do(ctx, http.MethodPut, "/1.0/instances/"+instance+"/state", map[string]any{
		"action": "start",
	})
	return err
}

// StopInstance 关机。force 走强制停止（相当于拔电源），
// 不给进程退出的机会 —— 只在正常关机卡住时用，所以它必须是
// 页面上一个单独的、要二次确认的动作。
func (c *Client) StopInstance(ctx context.Context, instance string, force bool) error {
	_, err := c.do(ctx, http.MethodPut, "/1.0/instances/"+instance+"/state", map[string]any{
		"action": "stop",
		"force":  force,
	})
	return err
}
