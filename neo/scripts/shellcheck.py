#!/usr/bin/env python3
"""冒烟用的网页终端检查：连上、执行一条命令、看到结果。

用法: shellcheck.py <服务地址 host:port> <cookie 文件> <实例名>

单独抽出来而不是塞在冒烟脚本里，是因为这段逻辑踩过两个坑，
而踩坑时的表现都只是「终端一闪就没」—— 那种故障必须能单独复现：

  1. exec 的 POST 必须带 Connection: close。keep-alive 会让 Incus
     一直等 websocket 连接，几十秒后回一句 Timed out waiting for
     websockets to connect，终端随即被关掉。
  2. fds 里列出的连接要全部建起来（至少 stdin 与 control 两条），
     只连 stdin 那条同样等到超时。

退出码 0 表示终端真的能用；非 0 时 stdout 上会留下原因。
"""
import base64
import json
import os
import socket
import struct
import sys
import time

def read_cookie(path):
    """从 curl 的 Netscape 格式 cookie jar 里取出会话 cookie。

    两个坑，都踩过：

    1. 不能直接读整个文件当 Cookie 头。那个文件是
       「域 标志 路径 安全 过期 名字 值」的多行文本，
       整份塞进请求头只会得到一个畸形请求，服务端静默拒绝。
    2. 不能用「# 开头就是注释」来过滤。HttpOnly 的 cookie
       数据行同样以 # 开头（#HttpOnly_127.0.0.1\\tFALSE\\t/\\t...），
       按注释跳过就一个 cookie 都取不到。
       可靠的判据是「tab 分成 7 列」—— 注释行没有 tab。
    """
    for line in open(path, errors="replace"):
        parts = line.rstrip("\n").split("\t")
        if len(parts) < 7:
            continue
        if parts[5].strip() == "vmc_session":
            return parts[5].strip() + "=" + parts[6].strip()
    return ""


ADDR_HOST, ADDR_PORT = sys.argv[1].rsplit(":", 1)
COOKIE_FILE = sys.argv[2]
INSTANCE = sys.argv[3]
COOKIE = read_cookie(COOKIE_FILE)
if not COOKIE:
    raise SystemExit("NO_COOKIE " + COOKIE_FILE)


def ws_connect(path, cookie):
    key = base64.b64encode(os.urandom(16)).decode()
    s = socket.create_connection((ADDR_HOST, int(ADDR_PORT)), timeout=10)
    s.sendall(("\r\n".join([
        f"GET {path} HTTP/1.1", f"Host: {ADDR_HOST}",
        "Upgrade: websocket", "Connection: Upgrade", f"Sec-WebSocket-Key: {key}",
        "Sec-WebSocket-Version: 13", "Sec-WebSocket-Protocol: binary",
        f"Cookie: {cookie}", "", ""])).encode())
    hdr = b""
    while b"\r\n\r\n" not in hdr:
        hdr += s.recv(1)
    status = hdr.split(b"\r\n")[0].decode()
    if "101" not in status:
        raise SystemExit("HANDSHAKE_FAIL " + status + " " + hdr.decode(errors="replace")[:200])
    return s


def send(s, payload):
    mask = os.urandom(4)
    assert len(payload) < 126
    s.sendall(bytes([0x82, 0x80 | len(payload)]) + mask
              + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))


def recv(s):
    def rd(n):
        buf = b""
        while len(buf) < n:
            chunk = s.recv(n - len(buf))
            if not chunk:
                raise EOFError
            buf += chunk
        return buf

    b0, b1 = rd(2)
    ln = b1 & 0x7F
    if ln == 126:
        ln = struct.unpack(">H", rd(2))[0]
    elif ln == 127:
        ln = struct.unpack(">Q", rd(8))[0]
    return b0 & 0x0F, rd(ln)


s = ws_connect(f"/api/instances/{INSTANCE}/shell", COOKIE)
send(s, b"echo SHELL_$((6*7)); id -un\n")
out = b""
deadline = time.time() + 12
while time.time() < deadline:
    try:
        s.settimeout(2)
        op, payload = recv(s)
        if op == 9:            # keepalive ping：不回 pong 服务端会掐链路
            mask = os.urandom(4)
            s.sendall(bytes([0x8A, 0x80 | len(payload)]) + mask
                      + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))
            continue
        out += payload
        if b"SHELL_42" in out:
            break
    except (socket.timeout, TimeoutError):
        continue
    except EOFError:
        break
send(s, b"exit\n")
time.sleep(0.3)
s.close()

if b"SHELL_42" in out and b"root" in out:
    print("SHELL_OK")
    sys.exit(0)
print("SHELL_FAIL " + out.decode(errors="replace")[-300:])
sys.exit(1)
