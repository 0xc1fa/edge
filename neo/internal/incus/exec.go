package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"neo/internal/wsutil"
)

// ExecRequest 描述一次容器内执行。
//
// Interactive 为 true 时分配 pty：这是「网页终端」唯一可用的形态 ——
// 没有 pty 的话 bash 不回显、vi 进不去、top 花屏，租户看到的
// 只是一个能打字但没有响应的黑框。
type ExecRequest struct {
	Command     []string
	Interactive bool
	Env         map[string]string
	Width       int
	Height      int
}

// ExecSession 是一条到容器内的双向流：读是 stdout/stderr，写是 stdin。
//
// 它与 http 那一套完全无关（连接已经升级成 WebSocket），
// 所以这里不返回 error 的语义都跟 net.Conn 一致：EOF 就是对端关了。
type ExecSession struct {
	ws   *wsutil.Conn
	// aux 是 control 之类的辅助连接。它们不承载终端数据，
	// 但必须一直开着：Incus 在 exec 开始时等的就是「声明的连接都到齐」，
	// 少连一条它就直接判超时（Timed out waiting for websockets to connect）。
	aux  []*wsutil.Conn
	opID string
	// cancelFunc 取消 Incus 侧的 operation。Close 必须调它 ——
	// 只关连接会留下僵尸 operation，攒够了新终端就打不开。
	cancelFunc func() error
}

// postExec 发起 exec 请求，并原样拿回响应体。
//
// 不走通用的 do()，是因为这里必须带 `Connection: close`：
// wait-for-websocket 为 true 时，Incus 会等 websocket 连接出现才肯善罢甘休，
// 而 keep-alive 的 HTTP 连接让它以为「客户端还在，继续等」——
// 于是几十秒后回一句 Timed out waiting for websockets to connect。
// 明确告诉它「这条 HTTP 连接马上就断」，它才会把 operation 交回来。
// （同样的请求用 curl/python 发都正常，差别就在这一个头。）
func (c *Client) postExec(ctx context.Context, instance string, body map[string]any) (json.RawMessage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode exec: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/1.0/instances/"+instance+"/exec", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "close")

	resp, err := c.write.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("解析 exec 响应: %w", err)
	}
	if r.Type == "error" {
		return nil, fmt.Errorf("incus error %d: %s", r.ErrorCode, r.Error)
	}
	return r.Metadata, nil
}

// CancelOperation 取消一个 Incus operation。
//
// 终端关闭时必须调它。只关 WebSocket 是不够的：Incus 那一侧的
// operation 不会跟着断，它会一直停在 RUNNING，而 Incus 对同一实例的
// 并发 exec 是有限额的 —— 攒够之后，新的终端与改口令全都打不开，
// 表现为「连上去转圈十几秒然后黑屏」。那种故障离现场已经隔了
// 几十次关闭，极难回头查。
//
// 取消的同时，容器里的那个 shell 也会收到 SIGHUP：这与用户在终端里
// 敲 exit 的效果一致，正是我们要的（不留没人管的孤儿 shell）。
func (c *Client) CancelOperation(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := c.do(ctx, http.MethodDelete, "/1.0/operations/"+id, nil)
	return err
}

// Exec 在实例里执行命令，返回一条双向流。
//
// 两步走，两步都不能省：
//
//	POST /1.0/instances/{n}/exec   →  拿 operation id 与 stdin 的 secret
//	GET  /1.0/operations/{id}/websocket?secret=…  →  升级成 WebSocket
//
// 实测确认过的几件事（都踩过）：
//   - POST 返回的是 **202** 而不是 200，所以不能按状态码判成败，
//     只能看响应体里的 type 是不是 error；
//   - secret 在 metadata.metadata.fds["0"]，嵌了两层；
//   - 一条 WebSocket 承载全部流（stdin/stdout/stderr/pty 回显），
//     不需要为每个 fd 各建一条连接 —— 帧里没有 fd 前缀，payload 就是裸数据；
//   - 服务端每隔几秒发一个 keepalive ping，wsutil 会自动回 pong。
func (c *Client) Exec(ctx context.Context, instance string, req ExecRequest) (*ExecSession, error) {
	if len(req.Command) == 0 {
		return nil, errors.New("命令为空")
	}
	body := map[string]any{
		"command":            req.Command,
		"wait-for-websocket": true,
		"interactive":        req.Interactive,
	}
	if len(req.Env) > 0 {
		body["environment"] = req.Env
	}
	// 不带宽高 Incus 会给 pty 一个默认尺寸，xterm 的 fit 插件就对不上了，
	// 表现为光标位置和实际渲染差一截
	if req.Interactive {
		if req.Width <= 0 {
			req.Width = 80
		}
		if req.Height <= 0 {
			req.Height = 24
		}
		body["width"] = req.Width
		body["height"] = req.Height
	}

	raw, err := c.postExec(ctx, instance, body)
	if err != nil {
		return nil, err
	}
	log.Printf("exec %s 响应: %.300s", instance, string(raw))
	var meta struct {
		ID       string `json:"id"`
		Metadata struct {
			FDs map[string]string `json:"fds"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("解析 exec 响应: %w", err)
	}
	if meta.ID == "" {
		return nil, errors.New("exec 响应里没有 operation id")
	}
	secret := meta.Metadata.FDs["0"]
	if secret == "" {
		return nil, errors.New("exec 响应里没有 stdin 通道的 secret")
	}

	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", c.socketPath)
	}
	ws, err := wsutil.DialConn(ctx, dial, "/1.0/operations/"+meta.ID+"/websocket?secret="+secret)
	if err != nil {
		return nil, fmt.Errorf("连接容器终端: %w", err)
	}

	// 把其余声明的连接也建起来，其中 control 是必须的：
	// Incus 等的是「fds 里每一条都有人听」，只连 stdin 那条
	// 会在几十秒后得到一句 Timed out waiting for websockets to connect，
	// 而那时终端里还什么都看不到 —— 用户只会看到一个一闪而过的黑框。
	session := &ExecSession{ws: ws, opID: meta.ID}
	// 取消函数在这里就装上：万一后面建辅助连接失败，Close 也能把
	// 已经起来的那个 operation 收掉，不会漏在 Incus 那边。
	session.cancelFunc = func() error { return c.CancelOperation(context.Background(), meta.ID) }
	for fd, s := range meta.Metadata.FDs {
		if fd == "0" {
			continue
		}
		aux, err := wsutil.DialConn(ctx, dial, "/1.0/operations/"+meta.ID+"/websocket?secret="+s)
		if err != nil {
			session.Close()
			return nil, fmt.Errorf("连接 %s 通道: %w", fd, err)
		}
		session.aux = append(session.aux, aux)
		// control 连接上偶尔会有消息过来（比如 resize 的回执）。
		// 不读的话 Incus 那一侧会写满缓冲区然后卡住，
		// 所以起一个 goroutine 专职丢弃。
		go func(a *wsutil.Conn) {
			for {
				if _, _, err := a.Read(); err != nil {
					return
				}
			}
		}(aux)
	}
	return session, nil
}

// OperationID 是对应的 Incus operation，取消终端时要用。
func (e *ExecSession) OperationID() string { return e.opID }

// Read 读一段容器输出。返回 io.EOF 表示命令结束或连接关闭。
func (e *ExecSession) Read(p []byte) (int, error) {
	_, payload, err := e.ws.Read()
	if err != nil {
		return 0, err
	}
	n := copy(p, payload)
	if n < len(payload) {
		// 上层缓冲区小于一帧：余下的部分会被丢掉。
		// 终端的读缓冲区应该远大于任何一帧输出，这里如实报出来，
		// 而不是悄悄吞掉 —— 吞掉的表现为屏幕上少几行。
		return n, errors.New("读缓冲区小于一帧输出，数据被截断")
	}
	return n, nil
}

// Write 把输入送进容器。
func (e *ExecSession) Write(p []byte) (int, error) {
	if err := e.ws.Write(wsutil.OpBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize 通知容器 pty 尺寸变了。
//
// 不做的后果是行编辑器（bash 的 readline、vim）按旧宽度折行，
// 拖动浏览器窗口之后屏幕上的光标会越来越偏。
// 尺寸通过 control 通道送，格式是 Incus 约定的 JSON。
func (e *ExecSession) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	payload := fmt.Sprintf(`{"command":["window-resize"],"args":{"width":%d,"height":%d}}`, cols, rows)
	_ = e.ws.Write(wsutil.OpBinary, []byte(payload))
}

// Close 关闭连接，并取消 Incus 侧的 operation。
//
// 两件事都必须做：只关连接会留下 RUNNING 的僵尸 operation（攒够了
// 新终端就打不开），只取消 operation 而不断连接则会有一段时间
// 两头都不知该往哪送数据。顺序上先取消后断连 ——
// 反过来的话，取消请求会和还没读完的输出抢同一条通道。
//
// 幂等：handler 里 defer 一次、正常路径一次，取消已取消的 operation
// 会报错，那个错误在这里被故意吞掉。
func (e *ExecSession) Close() error {
	_ = e.cancel()
	err := e.ws.Close()
	for _, a := range e.aux {
		if cerr := a.Close(); cerr != nil {
			err = cerr
		}
	}
	return err
}

// cancel 取消 operation。socket 路径在 Client 上，所以它由 session 持有。
func (e *ExecSession) cancel() error {
	if e.cancelFunc == nil {
		return nil
	}
	return e.cancelFunc()
}

// RunOnce 在容器里跑一条命令并等它结束，返回合并后的输出。
//
// 用于「重置密码」这类一次性动作：不需要人看过程，只要一个结论。
//
// 为什么不依赖 EOF：pty 会话里 shell 退出后，Incus 未必会发 close 帧 ——
// 实测等 EOF 会一直挂到超时，而页面上的表现是「重置口令转圈十几秒后失败」。
// 所以这里发一个完成标记，读到它就认为命令结束了；标记没出现
// （命令卡住或压根没跑起来）则由 ctx 超时兜底，并如实报出来。
func (c *Client) RunOnce(ctx context.Context, instance string, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	const doneMarker = "__NEO_DONE__"
	session, err := c.Exec(ctx, instance, ExecRequest{
		Command:     []string{"/bin/sh", "-c", command + "; echo " + doneMarker},
		Interactive: true,
	})
	if err != nil {
		return "", err
	}
	defer session.Close()

	var out []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := session.Read(buf)
		out = append(out, buf[:n]...)
		if bytes.Contains(out, []byte(doneMarker)) {
			return string(out), nil
		}
		if err != nil {
			if isEOF(err) {
				return string(out), nil // 对端关了：命令也跑完了，标记可能被截断
			}
			return string(out), err
		}
		if len(out) > 1<<20 {
			return string(out), errors.New("命令输出超过 1MiB，已截断")
		}
	}
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}
