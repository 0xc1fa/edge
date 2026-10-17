// Package wsutil 是一条 WebSocket 链路的帧编解码，零第三方依赖。
//
// 之所以自己写而不是引一个库：这个项目长期保持「go.mod 里一个依赖都没有」——
// 镜像构建完全离线，多一个依赖就多一条会断的链。而 WebSocket 在这里的用法
// 又窄得很：只有终端转发一种场景，需要的能力就是握手、收发二进制帧、回 ping。
//
// 两端都要用：
//
//	服务端（对浏览器）—— Upgrade 把一条 HTTP 连接升级成 WebSocket；
//	客户端（对 Incus）—— DialConn 握手后同样得到 *Conn，发出去的帧必须掩码。
//
// 帧格式按 RFC 6455：客户端发的一定带掩码，服务端发的一定不带。
// 读的时候两种都容错（掩码就解，没掩码就当明文），因为这里的两端
// 一端是浏览器、一端是 Incus，谁都不该把对方写死。
package wsutil

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 帧操作码。只列用得到的：终端的全部数据都走二进制帧，
// 文本帧只为兼容而认（有些客户端探测时会发一帧空的）。
const (
	OpContinuation byte = 0x0
	OpText         byte = 0x1
	OpBinary       byte = 0x2
	OpClose        byte = 0x8
	OpPing         byte = 0x9
	OpPong         byte = 0xA
)

// acceptGUID 是 RFC 6455 写死的魔法串：Sec-WebSocket-Accept 由它算出，
// 两端各算一遍，对不上就说明中间有人改了握手请求。
const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// AcceptKey 由浏览器给的 Sec-WebSocket-Key 算出服务端要回的 Accept 值。
func AcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Conn 是一条已升级的连接。读侧自动重组分片、自动应答 ping；
// 写侧加锁 —— 两个 goroutine 同时写会把帧写烂，而那在终端里
// 表现为「输入几个字符之后画面再也不动」，极难排查。
type Conn struct {
	c        net.Conn
	r        *bufio.Reader
	isClient bool // 客户端发出的帧必须掩码，服务端发出的不能掩码

	mu sync.Mutex
}

// Upgrade 把一条 HTTP 连接升级成 WebSocket（服务端一侧）。
//
// 用 Hijack 而不是 httputil 那一套：升级之后这条连接上跑的是裸 WebSocket，
// net/http 的响应包装器到此为止，再往下它帮不上忙。
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("不是 WebSocket 升级请求")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("缺少 Sec-WebSocket-Key")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("这条连接不支持 hijack")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	// 子协议不回显：本项目不实现任何子协议。客户端坚持要的话，
	// 握手失败比连上一个「以为在 binary 模式」的终端更好。
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + AcceptKey(key) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	// brw 里可能已经缓冲了浏览器发来的第一帧数据，不能丢
	_ = brw.Flush()
	return &Conn{c: conn, r: bufio.NewReader(io.MultiReader(brw.Reader, conn))}, nil
}

// DialConn 以客户端身份完成 WebSocket 握手。
//
// 不借道 http.Client：net/http 不认识 101，拿到升级响应就撒手了，
// 而我们要的恰恰是升级之后那条裸连接。所以握手请求自己写、
// 响应自己读，读到 "101" 才算成。
//
// path 形如 /1.0/operations/<id>/websocket?secret=<s>。
func DialConn(ctx context.Context, dial func(context.Context) (net.Conn, error), path string) (*Conn, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(raw)

	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	// 握手阶段就要能取消：卡在 connect 上的握手必须跟着 ctx 一起断
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: incus\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Protocol: binary\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("写握手请求: %w", err)
	}

	br := bufio.NewReader(conn)
	status := ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("读握手响应: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if status == "" {
			status = line
			if !strings.Contains(line, "101") {
				conn.Close()
				return nil, fmt.Errorf("升级被拒: %s", status)
			}
			continue
		}
		if line == "" {
			break // 空行：头结束
		}
	}
	_ = conn.SetDeadline(time.Time{}) // 之后由调用方按需设置
	return &Conn{c: conn, r: br, isClient: true}, nil
}

// Read 读一个完整的消息。
//
// 分片在这里被吃掉：调用方看到的永远是一个完整消息，
// 不用关心它是一条还是十条过来的 —— 终端的输入输出都是流，
// 让上层去拼帧只会多一处能写错的地方。
//
// 收到 ping 自动回 pong。Incus 每隔几秒就发一个 keepalive ping，
// 不回它会认为连接死了，终端就会莫名断开；而终端两端都不需要
// 看到这些帧，所以在这里静默处理掉。
func (c *Conn) Read() (byte, []byte, error) {
	var (
		opcode    byte
		buf       []byte
		fragStart = true
	)
	for {
		op, fin, masked, length, err := c.readFrameHeader()
		if err != nil {
			return 0, nil, err
		}
		payload, err := c.readPayload(length, masked)
		if err != nil {
			return 0, nil, err
		}

		// 控制帧不参与分片：它们穿插在数据帧之间（Incus 的 keepalive ping
		// 随时可能插进来），把也算进分片序列会让一次 ping 被当成
		// 「分片中途出现新操作码」而把连接断掉。
		switch op {
		case OpPing:
			// 静默应答，继续等真正的数据帧。
			// pong 放到 goroutine 里发：读循环不该被一次写堵住 ——
			// 对端完全可能只读不写（终端里最常见的形态），
			// 同步发 pong 会让整个读循环停在一次 Write 上，画面从此不动。
			go func(p []byte) { _ = c.Write(OpPong, p) }(append([]byte{}, payload...))
			continue
		case OpPong:
			continue
		case OpClose:
			code, reason := parseClose(payload)
			_ = c.writeClose(code, reason)
			return 0, nil, io.EOF
		}

		if fragStart {
			opcode = op
			fragStart = false
		} else if op != OpContinuation {
			// 分片中途出现新的数据操作码：协议错误，直接断
			c.writeClose(1002, "unexpected opcode during fragmentation")
			return 0, nil, errors.New("分片中途出现新操作码")
		}

		buf = append(buf, payload...)
		if fin != 0 {
			return opcode, buf, nil
		}
	}
}

// Write 发一个完整的消息（FIN=1）。客户端路径自动加掩码。
func (c *Conn) Write(opcode byte, payload []byte) error {
	return c.writeFrame(opcode, true, payload)
}

// writeFrame 发一帧，fin 单独给出来：分片时中间几帧必须是 FIN=0，
// 否则对端会以为消息已经结束 —— 那种错在终端上表现为
// 「一大段输出只剩开头一截」，而协议本身并没有报错。
func (c *Conn) writeFrame(opcode byte, fin bool, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeFrameLocked(opcode, fin, payload)
}

func (c *Conn) writeFrameLocked(opcode byte, fin bool, payload []byte) error {
	var mask []byte
	first := opcode
	if fin {
		first |= 0x80
	}
	head := []byte{first}
	n := len(payload)
	switch {
	case n < 126:
		head = append(head, byte(n))
	case n < 1<<16:
		head = append(head, 126, byte(n>>8), byte(n))
	default:
		head = append(head, 127)
		for shift := 7; shift >= 0; shift-- {
			head = append(head, byte(n>>(8*shift)))
		}
	}
	if c.isClient {
		mask = make([]byte, 4)
		if _, err := rand.Read(mask); err != nil {
			return err
		}
		head[1] |= 0x80
		head = append(head, mask...)
	}
	if _, err := c.c.Write(head); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if c.isClient {
		out := make([]byte, n)
		for i, b := range payload {
			out[i] = b ^ mask[i%4]
		}
		_, err := c.c.Write(out)
		return err
	}
	_, err := c.c.Write(payload)
	return err
}

func (c *Conn) writeClose(code int, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	payload := []byte{byte(code >> 8), byte(code)}
	payload = append(payload, reason...)
	return c.writeFrameLocked(OpClose, true, payload)
}

// readFrameHeader 返回 (opcode, fin, masked, length)。
// 注意 length 用 int 回传：封成 byte 会在 65535 以上被截断，
// 而粘贴一大段文本进终端完全可能越过这个数。
func (c *Conn) readFrameHeader() (opcode byte, fin byte, masked byte, length int, err error) {
	var h [2]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return 0, 0, 0, 0, err
	}
	opcode, fin, masked = h[0]&0x0F, h[0]&0x80, h[1]&0x80
	length = int(h[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return 0, 0, 0, 0, err
		}
		length = int(ext[0])<<8 | int(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return 0, 0, 0, 0, err
		}
		// 64 位长度里高 32 位必须是 0；不是的话这条帧不可能属于终端场景
		for _, b := range ext[:4] {
			if b != 0 {
				return 0, 0, 0, 0, errors.New("帧长度超过 4GiB，拒绝")
			}
		}
		// 必须从 0 开始累积：length 此刻还装着 case 值 127，
		// 不清零的话 65536 会被算成 21 亿，一条正常的粘贴就被当成超大帧拒掉
		length = 0
		for _, b := range ext[4:] {
			length = length<<8 | int(b)
		}
	}
	return opcode, fin, masked, length, nil
}

// readPayload 读 payload 并按需解掩码。
//
// 顺序不能反：RFC 6455 的帧是「头 → 掩码键（4 字节，仅客户端帧）→ 数据」。
// 写成先读数据再读掩码键，短帧会因为错位而内容全乱，
// 而那种错位在终端上的表现是「输入几个字符就花屏」—— 极难往回查。
func (c *Conn) readPayload(length int, masked byte) ([]byte, error) {
	if length > 1<<20 {
		return nil, errors.New("单帧超过 1MiB，拒绝：终端场景不该出现")
	}
	var key []byte
	if masked != 0 {
		key = make([]byte, 4)
		if _, err := io.ReadFull(c.r, key); err != nil {
			return nil, err
		}
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(c.r, buf); err != nil {
		return nil, err
	}
	for i := range buf {
		if key != nil {
			buf[i] ^= key[i%4]
		}
	}
	return buf, nil
}

// parseClose 从 close 帧的 payload 里取出状态码与原因。
func parseClose(payload []byte) (int, string) {
	if len(payload) < 2 {
		return 1000, ""
	}
	code := int(payload[0])<<8 | int(payload[1])
	return code, string(payload[2:])
}

// SetDeadline 同时管读写。终端的空闲是没有意义的：
// 一头不说话不等于链路断了，所以默认不设超时，
// 需要时（比如等首帧）由调用方显式设置。
func (c *Conn) SetDeadline(t time.Time) error {
	return c.c.SetDeadline(t)
}

// Close 关掉底层连接。
func (c *Conn) Close() error {
	return c.c.Close()
}

// RemoteAddr 对端地址，写日志用。
func (c *Conn) RemoteAddr() string {
	return c.c.RemoteAddr().String()
}
