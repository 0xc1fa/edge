package incus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubIncus 搭一个最小 Incus：只认「读实例 / 整份写回 / 等 operation」这三个端点。
//
// 它的存在是为了把一件踩过坑的事固定下来：设备不能靠
// POST /1.0/instances/{n}/devices 与 DELETE .../devices/{d} 增删 —— 那两个端点
// 在真实的 Incus 上是 404。这里只允许 PUT 整份定义，于是任何退回旧写法的改动都会红。
func stubIncus(t *testing.T, inst Instance) (*Client, *[]instancePut, *string, func()) {
	t.Helper()

	etag := `"abc123"`
	puts := make([]instancePut, 0, 2)
	var ifMatch string

	mux := http.NewServeMux()
	mux.HandleFunc("/1.0/instances/tenant01", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("ETag", etag)
			_ = json.NewEncoder(w).Encode(Response{
				Type:       "sync",
				StatusCode: 200,
				Metadata:   mustJSON(t, inst),
			})
		case http.MethodPut:
			ifMatch = r.Header.Get("If-Match")
			var body instancePut
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("PUT body 解析失败: %v", err)
			}
			puts = append(puts, body)
			// 运行中的实例热插是异步的：回一个 operation 让调用方去等
			_ = json.NewEncoder(w).Encode(Response{
				Type:      "async",
				Operation: "/1.0/operations/op-1",
			})
		default:
			t.Errorf("未预期的请求方法: %s", r.Method)
		}
	})
	// 等 operation：返回的 metadata 里 status_code 才是真正的成败
	mux.HandleFunc("/1.0/operations/op-1/wait", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Response{
			Type:       "sync",
			StatusCode: 200,
			Metadata:   json.RawMessage(`{"status":"Success","status_code":200,"err":""}`),
		})
	})

	srv := httptest.NewServer(mux)
	c := &Client{base: srv.URL, http: srv.Client(), write: srv.Client()}
	return c, &puts, &ifMatch, srv.Close
}

func sampleInstance() Instance {
	return Instance{
		Name:         "tenant01",
		Architecture: "x86_64",
		Profiles:     []string{"default"},
		Config: map[string]string{
			"limits.cpu":        "2",
			"volatile.eth0.hw":  "10:66:6a:ab:57:73",
			"image.description": "Ubuntu jammy",
		},
		Devices: map[string]map[string]string{
			"root":    {"type": "disk", "path": "/", "pool": "default", "size": "20GiB"},
			"ssh2201": {"type": "proxy", "listen": "tcp:10.8.0.8:2201"},
		},
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return raw
}

func TestAddDeviceSubmitsFullDefinition(t *testing.T) {
	c, puts, ifMatch, done := stubIncus(t, sampleInstance())
	defer done()

	err := c.AddDevice(context.Background(), "tenant01", map[string]string{
		"name":    "gpu0",
		"type":    "gpu",
		"gputype": "physical",
		"pci":     "0000:c4:00.0",
	})
	if err != nil {
		t.Fatalf("AddDevice: %v", err)
	}
	if *ifMatch != `"abc123"` {
		t.Errorf("If-Match = %q，想带上读回时的 ETag", *ifMatch)
	}
	if len(*puts) != 1 {
		t.Fatalf("PUT 次数 = %d，想 1", len(*puts))
	}
	got := (*puts)[0]

	// name 是映射的键，不该混进设备属性里
	want := map[string]string{"type": "gpu", "gputype": "physical", "pci": "0000:c4:00.0"}
	if !sameDevice(got.Devices["gpu0"], want) {
		t.Errorf("gpu0 = %v，想 %v", got.Devices["gpu0"], want)
	}
	// 整份提交最容易犯的错是漏带：漏掉一个既有设备等于把它删掉
	for name, spec := range sampleInstance().Devices {
		if _, ok := got.Devices[name]; !ok {
			t.Errorf("既有设备 %s 在提交中丢失", name)
		}
		_ = spec
	}
	if got.Config["volatile.eth0.hw"] != "10:66:6a:ab:57:73" {
		t.Error("volatile.* 必须原样带回去：PUT 是整份替换，漏掉等于删除")
	}
	if got.Architecture != "x86_64" || len(got.Profiles) != 1 {
		t.Errorf("架构/配置文件未带全: %+v", got)
	}
}

func TestAddDeviceRefusesToClobberForeignDevice(t *testing.T) {
	inst := sampleInstance()
	inst.Devices["gpu0"] = map[string]string{"type": "gpu", "pci": "0000:81:00.0"}
	c, puts, _, done := stubIncus(t, inst)
	defer done()

	err := c.AddDevice(context.Background(), "tenant01", map[string]string{
		"name": "gpu0", "type": "gpu", "pci": "0000:c4:00.0",
	})
	if err == nil {
		t.Fatal("覆盖一张不同的卡应当报错")
	}
	if len(*puts) != 0 {
		t.Error("拒绝时不应提交")
	}

	// 内容一致则视为重试，不算冲突
	err = c.AddDevice(context.Background(), "tenant01", map[string]string{
		"name": "gpu0", "type": "gpu", "pci": "0000:81:00.0",
	})
	if err != nil {
		t.Errorf("重复提交同一张卡应当幂等，实际: %v", err)
	}
}

func TestDetachLastGPUClearsRuntime(t *testing.T) {
	inst := sampleInstance()
	inst.Config["nvidia.runtime"] = "true"
	inst.Devices["gpu0"] = map[string]string{"type": "gpu", "pci": "0000:c4:00.0"}

	c, puts, _, done := stubIncus(t, inst)
	defer done()

	if err := c.RemoveGPUDevice(context.Background(), "tenant01", "gpu0"); err != nil {
		t.Fatalf("RemoveGPUDevice: %v", err)
	}
	got := (*puts)[0]
	if _, ok := got.Devices["gpu0"]; ok {
		t.Error("gpu0 应当已被摘除")
	}
	if len(got.Devices) != 2 {
		t.Errorf("剩余设备 = %v，只想保留 root 与 ssh2201", got.Devices)
	}
	// 最后一张卡摘掉后，驱动栈的开关也要收掉：留着它容器里会一直有 /dev/nvidia-uvm
	if _, ok := got.Config["nvidia.runtime"]; ok {
		t.Error("nvidia.runtime 应当在无卡时被清除")
	}
	// 其余 config 不能因为清这一项而被连带清空
	if got.Config["limits.cpu"] != "2" {
		t.Errorf("其余 config 应保留，实际 %v", got.Config)
	}
}

func TestDetachKeepsRuntimeWhileAnotherCardRemains(t *testing.T) {
	inst := sampleInstance()
	inst.Config["nvidia.runtime"] = "true"
	inst.Devices["gpu0"] = map[string]string{"type": "gpu", "pci": "0000:c4:00.0"}
	inst.Devices["gpu1"] = map[string]string{"type": "gpu", "pci": "0000:81:00.0"}

	c, puts, _, done := stubIncus(t, inst)
	defer done()

	if err := c.RemoveGPUDevice(context.Background(), "tenant01", "gpu0"); err != nil {
		t.Fatalf("RemoveGPUDevice: %v", err)
	}
	got := (*puts)[0]
	if _, ok := got.Devices["gpu1"]; !ok {
		t.Error("只摘 gpu0，gpu1 必须还在")
	}
	if got.Config["nvidia.runtime"] != "true" {
		t.Error("还有卡在用，nvidia.runtime 不该被清掉")
	}
}

// 状态变更端点只认 PUT：POST 会得到 501 not implemented，
// 而页面上只看得到「重启失败」四个字，看不出是方法用错了。
func TestRestartUsesPutOnStateEndpoint(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1.0/instances/tenant01/state" {
			seen = r.Method
			_ = json.NewEncoder(w).Encode(Response{
				Type: "sync", StatusCode: 200,
				Metadata: json.RawMessage(`{"status":"Success","status_code":200,"err":""}`),
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{base: srv.URL, http: srv.Client(), write: srv.Client()}
	if err := c.RestartInstance(context.Background(), "tenant01", false); err != nil {
		t.Fatalf("RestartInstance: %v", err)
	}
	if seen != http.MethodPut {
		t.Errorf("状态变更用了 %q，想 PUT", seen)
	}
}

func TestDeviceWriteSurfacesOperationFailure(t *testing.T) {
	// operation 走到 Failure：只等 HTTP 200 会把失败当成成功
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/1.0/instances/tenant01" && r.Method == http.MethodPut:
			_ = json.NewEncoder(w).Encode(Response{Type: "async", Operation: "/1.0/operations/op-1"})
		case r.URL.Path == "/1.0/instances/tenant01":
			_ = json.NewEncoder(w).Encode(Response{
				Type: "sync", StatusCode: 200, Metadata: mustJSON(t, sampleInstance()),
			})
		case r.URL.Path == "/1.0/operations/op-1/wait":
			_ = json.NewEncoder(w).Encode(Response{
				Type: "sync", StatusCode: 200,
				Metadata: json.RawMessage(`{"status":"Failure","status_code":400,"err":"卡正被别的实例占用"}`),
			})
		}
	}))
	defer srv.Close()

	c := &Client{base: srv.URL, http: srv.Client(), write: srv.Client()}
	err := c.AddDevice(context.Background(), "tenant01", map[string]string{"name": "gpu0", "type": "gpu"})
	if err == nil {
		t.Fatal("operation 失败应当被当成失败")
	}
}
