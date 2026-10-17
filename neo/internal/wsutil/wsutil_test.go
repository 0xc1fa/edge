package wsutil

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAcceptKeyMatchesRFC 用 RFC 6455 手册里的例子钉住握手算法。
// 算法错一点点，浏览器会直接拒绝升级，而页面上的表现只是「连不上」。
func TestAcceptKeyMatchesRFC(t *testing.T) {
	key := "dGhlIHNhbXBsZSBub25jZQ==" // RFC 6455 §1.3 的经典样例
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got := AcceptKey(key); got != want {
		t.Errorf("AcceptKey(%q) = %q，想 %q", key, got, want)
	}
}

// pair 造一对互联的 Conn：一端按服务端规则（不掩码），一端按客户端规则（掩码）。
// 用 net.Pipe 而不是真 socket：帧是同步的，测试不用处理时序。
func pair() (*Conn, *Conn) {
	a, b := net.Pipe()
	server := &Conn{c: a, r: bufio.NewReader(a)}
	client := &Conn{c: b, r: bufio.NewReader(b), isClient: true}
	return server, client
}

// readRaw 从连接上读 n 个未经帧解析的字节，用来检查帧头。
func readRaw(t *testing.T, c net.Conn, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	total := 0
	for total < n {
		got, err := c.Read(buf[total:])
		if err != nil {
			t.Fatalf("读原始字节: %v", err)
		}
		total += got
	}
	return buf
}

// TestClientFramesAreMasked 客户端发出去的帧必须带掩码。
// 不带的话 Incus（以及任何遵守 RFC 的服务端）会直接断开，
// 而且断开时不带任何原因 —— 页面上只表现为「终端一片黑」。
func TestClientFramesAreMasked(t *testing.T) {
	server, client := pair()
	go func() { _ = client.Write(OpBinary, []byte("hello")) }()

	raw := readRaw(t, server.c, 2)
	if raw[0]&0x0F != OpBinary {
		t.Errorf("opcode = %d，想 binary", raw[0]&0x0F)
	}
	if raw[0]&0x80 == 0 {
		t.Error("FIN 位没设：这是一条不完整的帧")
	}
	if raw[1]&0x80 == 0 {
		t.Errorf("客户端帧没带掩码位: %x", raw)
	}
}

// TestServerFramesAreNotMasked 服务端发出去的帧不能带掩码：
// 浏览器见到带掩码的服务端帧会直接断开连接。
func TestServerFramesAreNotMasked(t *testing.T) {
	server, client := pair()
	go func() { _ = server.Write(OpBinary, []byte("hi")) }()

	raw := readRaw(t, client.c, 2)
	if raw[1]&0x80 != 0 {
		t.Errorf("服务端帧带了掩码位: %x", raw)
	}
}

// TestFramesRoundTrip 覆盖各种长度的消息：0、小、恰好越过 126、
// 以及必须走 64 位扩展长度的大帧。长度分档写错只在大帧上暴露，
// 而往终端里粘贴一大段文本正是那条路径。
//
// 串行发：net.Pipe 一次 Write 对应一次 Read，两个 goroutine 同时写
// 会把两帧的字节交错起来，那时看到的花屏是测试自己造成的。
func TestFramesRoundTrip(t *testing.T) {
	server, client := pair()

	cases := []struct {
		name    string
		payload []byte
	}{
		{"空帧", []byte{}},
		{"一个字符", []byte{'a'}},
		{"125 字节", bytes.Repeat([]byte{'x'}, 125)},
		{"126 字节", bytes.Repeat([]byte{'y'}, 126)},
		{"65535 字节", bytes.Repeat([]byte{'z'}, 65535)},
		{"65536 字节", bytes.Repeat([]byte{'w'}, 65536)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() { _ = client.Write(OpBinary, c.payload); close(done) }()
			_ = server.SetDeadline(time.Now().Add(5 * time.Second))
			op, got, err := server.Read()
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if op != OpBinary {
				t.Errorf("opcode = %d，想 binary", op)
			}
			if !bytes.Equal(got, c.payload) {
				t.Errorf("内容不符：拿到 %d 字节，想 %d", len(got), len(c.payload))
			}
			<-done
		})
	}
}

// TestPingIsAnsweredSilently Incus 每几秒发一个 keepalive ping。
// 不回它会认为链路死了，终端表现为莫名断开；而终端使用者不该看到这些帧 ——
// 所以 Read 必须静默回掉 pong，然后继续等真正的数据。
//
// pong 是异步发的：读循环不能被一次写堵住，否则对端只读不写时画面就再也不动。
func TestPingIsAnsweredSilently(t *testing.T) {
	server, client := pair()

	done := make(chan struct{})
	go func() {
		_ = server.Write(OpPing, []byte("keepalive"))
		_ = server.Write(OpPing, []byte("keepalive"))
		_ = server.Write(OpBinary, []byte("after-pings"))
		close(done)
	}()

	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	op, payload, err := client.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if op != OpBinary || string(payload) != "after-pings" {
		t.Fatalf("ping 之后应直接拿到数据帧，拿到 op=%d %q", op, payload)
	}
	// 两个 pong 必须真的被发出去：服务端那三个 Write 能陆续完成，
	// 说明客户端的应答没有被读循环卡住
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("服务端的写没跑完：pong 应答堵住了读循环")
	}
}

// TestFragmentationIsReassembled 分片到达的消息必须拼成一条交给上层。
// 不拼的话，一次大输出会被切成好几帧送上去，终端上表现为花屏或丢字。
func TestFragmentationIsReassembled(t *testing.T) {
	server, client := pair()
	done := make(chan struct{})
	go func() {
		// 中间两帧必须 FIN=0，否则对端会以为消息已经结束
		_ = server.writeFrameLocked(OpBinary, false, []byte("first-"))
		_ = server.writeFrameLocked(OpContinuation, false, []byte("second"))
		_ = server.writeFrameLocked(OpContinuation, true, []byte("-third"))
		close(done)
	}()

	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_, payload, err := client.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(payload) != "first-second-third" {
		t.Errorf("分片重组结果 = %q，想 first-second-third", payload)
	}
	<-done
}

// TestCloseFrameSurfacesAsEOF 对端关闭时，Read 必须回 io.EOF
// 而不是一个协议错误 —— 终端的正常退出走的就是这条路。
func TestCloseFrameSurfacesAsEOF(t *testing.T) {
	server, client := pair()
	go func() { _ = server.Write(OpClose, []byte{0x03, 0xE8}) }()

	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_, _, err := client.Read()
	if err == nil {
		t.Fatal("close 帧应当让 Read 返回错误")
	}
	if !strings.Contains(err.Error(), "EOF") {
		t.Errorf("close 帧应表现为 EOF，实际 %v", err)
	}
}

// TestOversizedFrameIsRefused 单帧上限是防线：坏掉的或恶意的对端
// 不该能把服务端内存吃光。终端场景里一帧超过 1MiB 一定是异常。
func TestOversizedFrameIsRefused(t *testing.T) {
	server, client := pair()
	// 客户端手写一条声称 2MiB 的帧头（FIN+binary，64 位扩展长度）
	go func() {
		head := []byte{0x82, 127, 0, 0, 0, 0, 0, 0x20, 0, 0}
		mask := []byte{1, 2, 3, 4}
		buf := append(head, mask...)
		for i, b := range bytes.Repeat([]byte{'a'}, 4) {
			buf = append(buf, b^mask[i%4])
		}
		_, _ = client.c.Write(buf)
	}()

	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	_, _, err := server.Read()
	if err == nil {
		t.Fatal("超大帧竟然被接受了")
	}
	if !strings.Contains(err.Error(), "1MiB") {
		t.Errorf("拒绝理由应当指向帧大小，实际: %v", err)
	}
}

// TestUpgradeAndDial 端到端：真 HTTP 服务上升级、真客户端握手。
// 这一步覆盖 Upgrade 里的 hijack 与 AcceptKey —— 前面几个用例
// 都是包内直连，绕过了 HTTP 那一层。
func TestUpgradeAndDial(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, payload, err := conn.Read()
		if err != nil {
			return
		}
		got <- string(payload)
		_ = conn.Write(OpBinary, payload)
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	client, err := DialConn(context.Background(), func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, "/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.Write(OpBinary, []byte("ping-through-http")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case msg := <-got:
		if msg != "ping-through-http" {
			t.Errorf("服务端收到 %q", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("服务端没收到消息")
	}
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, payload, err := client.Read(); err != nil || string(payload) != "ping-through-http" {
		t.Errorf("回显不符: %q, err=%v", payload, err)
	}
}

// TestDialRejectsNonUpgrade 对端不回 101 必须握手失败，
// 而不是留一个「连上了但什么都不动」的连接 —— 那种故障在页面上最难查。
func TestDialRejectsNonUpgrade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not a websocket"))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, err := DialConn(context.Background(), func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, "/")
	if err == nil {
		t.Fatal("非 101 响应竟然握手成功")
	}
}

// TestUpgradeRejectsPlainRequest 非升级请求必须被拒。
func TestUpgradeRejectsPlainRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Upgrade(w, r); err == nil {
			t.Error("普通 HTTP 请求竟然升级成功")
		}
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
}
