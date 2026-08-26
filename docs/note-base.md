# 网络 代理 下载

## 🐛 stacks 镜像代理服务异常 260826

> 场景：断电重启后，stacks 中 `registry-proxy`（Docker Hub 代理缓存）重启 59 次、约 50 分钟不可用后自愈；其余 8 个服务全部正常。根因不是 Docker，而是「**外网 DNS 唯一来源 mihomo 是登录后才启动的 GUI 应用**」——自启 ≠ 开机启。

```text
[排障链路 —— 定位异常服务]
  │
  ▼
① docker compose ps -a：其余 8 服务全 "Up About an hour"
   registry-proxy 独独 "Up 10 minutes" → 最近重启过
  ▼
② docker inspect：RestartCount=59（restart: always 反复拉起）
  ▼
③ 日志定性：panic: Get "https://registry-1.docker.io/v2/":
   dial tcp: lookup registry-1.docker.io on 127.0.0.53:53: server misbehaving
   → DNS 解析失败 → registry 镜像 proxy 模式启动强制回源探测，失败 panic 不重试
  ▼
④ 现状自愈：GET /v2/ → HTTP 200；dind 内 9 容器全正常
   → 判定为开机时序竞态，非配置/数据损坏
```

```text
[根因拆解 —— 为什么 DNS 会挂 50 分钟]
  │
  ▼
① 物理网卡（enp193s0/194s0）：Current Scopes: none，无任何 DNS 上游
  ▼
② 外网 DNS 唯一来源：mihomo TUN fake-ip DNS（198.18.0.2）
  ▼
③ Tailscale MagicDNS（100.100.100.100）只管 tailnet 内网域名
  ▼
④ mihomo-party 是 GUI 会话级应用：
   · systemctl list-unit-files → 无 mihomo 系统服务
   · /etc/xdg/autostart、~/.config/autostart → 无 mihomo 条目
   · 两次开机均在「用户登录后」以 scope 启动（07-28 09:55 / 08-26 16:57）
  ▼
⑤ 结论：GUI「开机自启」选项 = XDG autostart/login item 类机制，
   只在桌面会话建立（登录）时执行；无登录的纯开机阶段不触发
  ▼
⑥ 因果链：断电 → 16:07 开机（无人登录）→ 容器 16:07:18 拉起
   → 无外网 DNS → registry-proxy panic → 重启风暴 59 次
   → 16:56 用户 xrdp 登录 → 16:57 mihomo 起 → 16:58 proxy 恢复
```

**08-26 时间线**（时区 +0800）：

```text
16:07:07  系统启动，systemd-resolved 就绪（无可用外网上游）
16:07:08  tailscaled 启动（warming-up，MagicDNS 未就绪）
16:07:14  dockerd 启动
16:07:18  restart:always 立即拉起 registry-proxy
16:07:2x  回源探测 → 解析失败 → panic（127.0.0.53 server misbehaving）
16:07~16:57  指数退避重启 ×59（约 50 分钟）
16:56:57  xrdp 登录（MacBook24）
16:57:00  mihomo-party 随会话启动（app-mihomo-party scope）
16:57:01  Mihomo TUN 激活，resolved 上游切为 198.18.0.2
16:58:18  registry-proxy 最后一次重启，回源成功 → 恢复
```

**为什么 9 个服务只有 registry-proxy 挂**：它启动时强制依赖外网（`proxy.remoteurl` 回源探测 Docker Hub）；5000 私有 registry 无 proxy 配置、portainer/gitea/registry-ui/port-forward 仅监听端口、dind 的 dockerd 不依赖外网（内部应用按需拉镜像、非开机动作），故全部不受影响。

**物理网卡配 DNS 能解决吗**：

> 用户质疑：「物理网卡不是配的 DNS 么」——若真配了 DNS，问题就只出在解析层，补个 DNS 即可解决。

```text
[核实 —— 物理网卡 DNS 配置三查]
  │
  ▼
① NetworkManager 连接文件（Wired connection 1.nmconnection，enp193s0）：
   address1=10.8.0.8/24,10.8.0.1 / method=manual
   → 只有静态 IP + 网关，无 dns= 字段（亦无 dns-search / ignore-auto-dns）
  ▼
② netplan（01-network-manager-all.yaml）：仅 4 行转交 NetworkManager，无 DNS
  ▼
③ resolvectl status：enp193s0/enp194s0 均 Current Scopes: none、无 DNS Server
  ▼
④ 印象来源：曾用 resolvectl dns <网卡> 223.5.5.5 等运行时命令临时配过 → 重启即丢
   （本次开机 journalctl 无 set-dns 记录；静态 IP+网关 ≠ DNS，10.8.0.1 是路由下一跳）
```

```text
[分析 —— 配 DNS 为什么不是方案]
  │
  ▼
① 物理网卡有默认路由（main 表 default via 10.8.0.1）
   → 配 DNS 后解析层必通（不再报 127.0.0.53 server misbehaving）
  ▼
② 但 registry-proxy 回源是两步：DNS 解析 + TCP 连接 registry-1.docker.io:443
  ▼
③ 本机访问 Docker Hub 必须走代理（note-base 多起排障佐证：vLLM 5.2GB 镜像、
   git push、AI API 均需代理出网）→ 物理网卡直连连接层大概率仍失败
  ▼
④ 配 DNS 后失败形态改变：lookup server misbehaving → dial tcp timeout，
   重启风暴照旧 → 问题没解决
  ▼
⑤ 验证方法（mihomo 停止时）：
   curl -v --connect-timeout 8 https://registry-1.docker.io/v2/
   能握手 → 配 DNS 可根治；卡 TCP → 仅兜底，根治仍靠 mihomo 开机即起（方案1/2）
```

**关键认知（延伸）**：

- **静态 IP+网关 ≠ DNS**：`address1=10.8.0.8/24,10.8.0.1` 的 10.8.0.1 是路由下一跳，不是 DNS 服务器；网卡没有 DNS 字段就是没有 DNS，手动模式（非 DHCP）更不会自动下发
- **resolvectl dns 是运行时命令**：不落盘、重启即丢；持久化须写 NetworkManager 连接（`nmcli connection modify`）或 netplan
- **解析层 ≠ 连接层**：配 DNS 只解决「域名→IP」；直连被墙时 panic 只是换个错误形态（lookup → dial tcp timeout），重启风暴照旧
- **方案判定标准**：不改变失败结果（重启风暴依旧）的改动不算方案，只算兜底；解析兜底价值有限（apt/国内源可用），外网连接仍依赖 mihomo 开机即起

**服务化可行性**（支撑后续方案）：`/opt/clash-party/resources/sidecar/mihomo` 为独立内核二进制（另有 -alpha/-smart），配置在 `/home/user/.config/mihomo-party/mihomo.yaml`——两个方案都只需「内核 + 配置」跑成服务，GUI 可不再常驻。

```text
[方案权衡 —— 让 mihomo 真正开机即启]
  ├─▶ 方案1：systemd 系统服务（/etc/systemd/system/mihomo.service）
  │     · root 身份 / PID 1 管理 / 开机最早一批 / 完全无关登录
  │     · 管理：sudo systemctl enable --now；日志：journalctl -u
  │     └─▶ [采纳倾向] 本机是无头服务器 + 机器级依赖，root 跑内核最稳
  ├─▶ 方案2：systemd 用户服务 + linger（~/.config/systemd/user/）
  │     · user 身份 / 默认仅登录会话存活期运行
  │     · loginctl enable-linger user 后无登录也开机即起
  │     · 管理：systemctl --user；日志：journalctl --user -u
  │     → 优势「配置跟随用户」在本机无体现，适用性弱于方案1
  └─▶ 共同注意：服务化后 GUI 版不能再自启，否则双内核抢 TUN / 7890 / 9090
```

**关键认知**：

- **registry 镜像 proxy 模式的脆弱点**：启动时强制同步回源探测 `proxy.remoteurl`，DNS 失败直接 panic、不重试、不降级——这是它区别于同栈其他服务的唯一脆弱点
- **restart: always 放大崩溃**：把单次 panic 变成 59 次重启风暴；「自愈」本质是网络恢复后某次重启碰巧成功，不是 Docker 在修复
- **自启 ≠ 开机启**：GUI 应用的"开机自启"（XDG autostart/login item）只在桌面会话建立时触发；无头服务器要让服务开机即起必须用 systemd（系统服务或 user+linger）
- **外网 DNS 单点隐患**：本机物理网卡无 DNS 上游，外网解析完全依赖 mihomo（198.18.0.2 fake-ip）；mihomo 不在 = 所有需要外网的服务全挂；Tailscale MagicDNS 只管 tailnet 内网域名，救不了外网
- **时序竞态判定法**：对比各服务启动时间（compose ps 的 Up 时长差异）+ 崩溃日志的 DNS 报错 + 恢复时间点前后的系统事件（登录 / mihomo scope 启动），三者对齐即锁定根因

---

## 🐛 无法连接 Tailscale 设备 260825

> 场景：Mac（小火箭 Shadowrocket 路由模式接入 tailnet）ping Linux agent（100.69.186.2）全部 Request timeout；管理界面两设备均在线。最终解法：删除小火箭旁路路由 `100.64.0.0/10`；随后补开 MagicDNS 实现机器名访问。

```text
[排障链路 —— 方向性不通定位]
  │
  ▼
① 先分方向：agent → mac23 用 tailscale ping（数据面协议）→ pong via DERP(hkg)
   → 两端可达，Tailscale 数据面正常
  ▼
② agent 本机无拦截：iptables INPUT policy ACCEPT、icmp_echo_ignore_all=0、
   ufw 未启用、tailscale0 UP
  ▼
③ agent 直接 ICMP ping mac23 → 3/3 收到回复（0% 丢包），tcpdump 双向可见
   → ICMP over DERP 双向通 → 问题只在 mac23 → agent 方向
  ▼
④ agent 抓包 tailscale0（mac23 侧同时 ping）：0 包 → 请求根本没进 Tailscale 隧道
  ▼
⑤ mac23 定位：
   route -n get 100.69.186.2 → 命中 100.64.0.0/10 → en1 → 家用路由器 192.168.50.1
   netstat -rn → default/128.0/1/64/2 全劫持到 utun6（全局代理接管特征）
  ▼
⑥ 根因：小火箭是「路由模式」（Route，非 TUN 模式），旁路路由含 100.64.0.0/10
   → 该网段流量绕过 Shadowrocket，从物理网卡裸发
   → 家用路由器无 100.x 私有路由 → 丢弃 → 超时
  ▼
⑦ 修复：只删旁路路由 100.64.0.0/10 → ping 立即恢复
   （无需另加 DIRECT 规则：Shadowrocket 对 CGNAT 保留段有内置识别，
     流量进入处理管道后自动交给内置 Tailscale 通道）
```

**为什么只删一条就够**：路由模式的「旁路路由」= **绕过列表**（哪些网段绕过 Shadowrocket 裸发）。删除后 `100.64.0.0/10` 流量回到 Shadowrocket 处理管道，其内置 Tailscale 集成自动接管 CGNAT 段，无需用户配置 DIRECT。

**DNS（MagicDNS）访问**（同一问题延伸）：

```text
[MagicDNS 与 mihomo DNS 覆写是独立开关]
  想用短名访问 → nslookup agent 100.100.100.100 → SERVFAIL
  ① tailscale dns status → MagicDNS: disabled tailnet-wide（控制面级，非本机）
  ② 开启：admin console（login.tailscale.com/admin/dns）开 MagicDNS
  ③ 各端接受：tailscale set --accept-dns=true（本机 + Mac 端）
  ④ 验证：短名 / <name>.tail31403e.ts.net 解析到 100.x，双向 ping 0% 丢包
  ⑤ 共存：route-exclude 100.100.100.100/32 + 策略路由 9001（dport 53 → main）
     保证 MagicDNS 查询不被 mihomo TUN 劫持；DNS 覆写开关保持关闭（保机场 DoH）
```

**关键认知**：

- **方向性不通排查法**：Linux→Mac 通（tailscale ping 有 pong）、Mac→Linux 不通时，Linux 端 `tcpdump -i tailscale0` 抓入站——**0 包 = Mac 侧数据面问题**（旁路/分流把 `100.64.0.0/10` 挡在隧道外），Linux 侧防火墙/路由已排除
- **路由模式 ≠ TUN 模式**：路由模式的「旁路路由」是绕过列表而非虚拟网卡路由；Tailscale 网段必须留在 Shadowrocket 处理管道内，它才能识别并走内置 Tailscale 通道
- **「DNS 覆写总开关」≠ MagicDNS**：前者管 mihomo 是否整体接管 dns 段（影响机场 DoH / 控制面域名解析），后者管 tailnet 机器名解析（100.100.100.100）——两个独立开关，互不影响；本次全程 DNS 覆写保持关闭
- **MagicDNS 开启后与 mihomo 共存无冲突**：依赖既有 route-exclude `100.100.100.100/32` + 策略路由 9001 DNS 直出；本机验证：systemd-resolved 指向 100.100.100.100，机场节点不受影响

---

## 🐛 Antigravity IDE 无响应 260825

> 场景：Antigravity 对话报 network issue；API 实测 XFLTD 20 节点 delay 全 FAIL、订阅停 07-21（autoUpdate: false）且 API 403，但 Mac 同订阅正常。根因：「DNS 覆写总开关」开启 → GUI 模板整体替换订阅 dns 段 → 机场专属 DoH（proxy-server-nameserver）丢失 → 节点域名解析错位 → vless 握手失败。关闭开关后全部恢复。

**机制**：mihomo 用 `proxy-server-nameserver` 解析**节点服务器域名**（非普通 `nameserver`）；机场节点是私有域名，只有机场专属 DoH 能解析到机场**当前中转 IP**，公共 DoH 解析出的 IP 与实况不符 → 握手必败。判据：节点全挂 ≠ 机场挂——端口有 HTTP 响应但 vless 握手失败、公共 DNS 能解析但 IP 不对，即「解析错位」。

**DNS 覆写总开关 = dns 段整体接管开关**（260824 要开 / 260825 要关，互斥撞车，按当前主要矛盾切换）：

- 开启：GUI 完整合并 DNS，`+.tailscale.com`/`+.ts.net` 进 fake-ip-filter → tailscale 域名真实解析
- 关闭：保留订阅自带 dns 段 → 机场专属 DoH 存活 → 节点不挂

**external-controller 踩坑（顺带）**：

- 手改 `mihomo.yaml` **无效**：GUI 内存设置在合并时覆盖文件改动（`work/config.yaml` 仍旧值），须在 GUI「mihomo」内核设置页改
- 9090 被 docker `infra-prometheus` 占用 → 核心静默回退 unix socket、curl 9090 返回 prometheus 版本号（假象）；改 9999 生效
- API 验证：`curl -H 'Authorization: Bearer <secret>' http://127.0.0.1:9999/version`（不带 secret 返回 Unauthorized 属正常）
- metacubexd 面板 mixed content：HTTPS 页禁访 HTTP 后端；本地 file:// 打开填 API 地址，或服务器部署 external-ui

**节点/订阅诊断顺序**：节点 delay 全 FAIL → 先分「节点不可达」与「域名解析错位」；再查订阅能否拉取（403 = token 失效）；Mac 正常 ≠ Ubuntu 正常（token 时效/更新状态可不同）。

---

## 🐛 Tailscale 设备无法识别 260824

> 场景：本机曾因「小火箭配置后连不上」卸载 Tailscale；重装后与 Mihomo（Clash Meta）TUN 模式共存，从控制面连不上 → DNS 劫持 → 数据面单向不通，最终用「覆盖脚本 + 策略路由」解决。

```text
[排障链路 —— 两个数据平面抢流量]
  │
  ▼
① 根因：Mihomo TUN 透明接管全部流量，兜底 MATCH 走代理
   → tailscale 控制面（login/controlplane.tailscale.com）连不上
  ▼
② 方案：旁路规则（TUN 排除 + DNS 白名单 + 分流规则）
   · 小火箭（Shadowrocket）iOS 版原生支持 Tailscale
   · Linux Mihomo 无 Tailscale 支持 → 必须旁路
  ▼
③ GUI 限制：规则页无添加按钮
   → 用「覆盖 Override」JS 脚本注入规则（mihomo-party 全局覆盖，
     合并后进 work/config.yaml）
  ▼
④ 误区：fake-ip 改真实 IP 无效
   根因是 TUN 接管 + 规则兜底走代理，不是 DNS 模式问题
  ▼
⑤ tailscale up 卡住排查
   · --headless 已被 1.98.9 移除
   · control 域名返回 fake-ip → 需真实解析
   · 「DNS 覆写总开关」决定真实解析能力
   （fake-ip-filter 条目本身不产生真实解析，脚本注入无效）
  ▼
⑥ 数据面问题：本机 → 节点被 TUN 劫持
   · 回包走 9002: from all iif lo lookup 2022 → 进 TUN → ICMP 丢失
   · route-exclude 100.64.0.0/10 实际未生效（表 2022 仍有 100/10 路由）
  ▼
⑦ 最终解法：策略路由优先级高于 TUN 接管（priority < 9000）
   ip rule add to 100.64.0.0/10 lookup main priority 8990
   ip rule add from 172.16.0.0/12 lookup main priority 8999
   → systemd oneshot 持久化（重启不丢）
```

**最终生效配置**：

```yaml
# mihomo tun 排除（GUI 修改，持久）
tun:
  route-exclude-address:
    - 172.16.0.0/12        # Docker 全段
    - 100.64.0.0/10        # Tailscale 内网
    - 100.100.100.100/32   # Tailscale DNS

# 覆盖脚本 override/*.js（全局覆盖，注入规则）
config.rules.unshift(
  "PROCESS-NAME,tailscaled,DIRECT",
  "DOMAIN-SUFFIX,tailscale.com,DIRECT",
  "DOMAIN-SUFFIX,ts.net,DIRECT",
  "IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
  "DST-PORT,41641,DIRECT"
);

# DNS：fake-ip-filter 含 +.tailscale.com / +.ts.net
# 「DNS 覆写总开关」必须保持开启（决定完整合并 DNS 配置）
```

```text
[策略路由优先级全景 —— 谁在抢流量]
  优先级    规则                             谁赢
  ──────    ────                             ───
  9000      to 198.18.0.0/30 lookup 2022     fake-ip 回环
  9001      dport 53 lookup main             DNS 直出
  9002      from all iif lo lookup 2022      lo 出站 → TUN ← 回包劫持点
  8990      to 100.64.0.0/10 lookup main     Tailscale 直出 ✅
  8999      from 172.16.0.0/12 lookup main   Docker 直出 ✅
  ──────
  ip rule 按 priority 从小到大匹配，8990/8999 先于 9002 命中 → 绕过 TUN
```

**把 fake-ip 改成真实 IP（为什么不行）**

> 用户原话："那把fake-ip 修改为真实 Ip 是不是也可以解决"——直觉：DNS 直接返回真实 IP，tailscale 域名就能解析成真实地址，控制面就能连上。

```text
[DNS 两种模式的差异 —— 只改解析结果的「形态」，不改流量走向]
                │
   ┌────────────┴────────────┐
   ▼                         ▼
fake-ip 模式              redir-host（真实 IP）模式
DNS 返回假地址               DNS 返回真实地址
198.18.0.x / 16             e.g. 192.200.0.103
   │                         │
   ▼                         ▼
流量按假地址路由            流量按真实地址路由
   │                         │
   └────────┬────────────────┘
            ▼
    两者的流量最终都进入 TUN 接管 + 分流规则
    ┌───────────────────────────────────────┐
    │ TUN：系统网络层改路由表，接管全部流量   │
    │ 规则：兜底 MATCH → 走代理              │
    │ → tailscale 域名流量照样进代理         │
    └───────────────────────────────────────┘
            │
            ▼
  结论：DNS 模式改不改，流量走向都一样 → 无效
```

**为什么无效的根因拆解**：

- **两个独立层面**：DNS 模式（域名→IP 的映射形态）与流量走向（路由表 + 分流规则）是**两个独立层面**；问题出在后者（TUN 接管 + 规则兜底走代理），前者改了不解决后者
- **fake-ip 只是表象**：控制面连不上的表象是「域名解析到 fake-ip」，但根因是「流量进代理」；就算解析到真实 IP，流量仍被 TUN 劫持进代理，照样连不上
- **正确解法在流量层**：让 tailscale 域名的流量走 DIRECT（旁路规则：`DOMAIN-SUFFIX,tailscale.com,DIRECT` + `IP-CIDR,100.64.0.0/10,DIRECT`），而不是改 DNS 模式——最终实测有效

**关键认知**：

- **TUN 会劫持回包**：本机进程回复 tailnet 节点的包（目标 100.x）会被 `9002: from all iif lo lookup 2022` 劫持进 TUN，ICMP 在 TUN 中丢失——`route-exclude` 不生效时用显式策略路由绕过（priority 小于 9000 即可），Linux→节点 ping 从 100% 丢包恢复到 0%
- **ip rule 是内存态**：重启即丢，必须持久化（systemd oneshot service）；`route-exclude-address` 才是 mihomo 持久配置
- **fake-ip-filter 条目 ≠ 解析能力**：白名单条目只决定「哪些域名不返回 fake-ip」，真实解析依赖 DNS 完整合并（mihomo-party 的「DNS 覆写总开关」控制），覆盖脚本注入 filter 条目无效
- **小火箭是接入端不是管理端**：Shadowrocket 的 Tailscale 集成用 auth key 让 Mac 加入 tailnet，**无节点列表 UI**，看不到其它机器是正常设计；验证是否加入以 Linux 端 `tailscale status` 为准；创建 key 时**勿勾 Ephemeral**（离线即删节点）
- **方向性不通排查法**：Linux→Mac 通（`tailscale ping` 有 pong）、Mac→Linux 不通时，Linux 端 `tcpdump -i tailscale0` 抓入站——**0 包 = Mac 侧数据面问题**（Shadowrocket 分流规则可能把 `100.64.0.0/10` 接管去代理），Linux 侧防火墙/路由已排除
- **✅ 已解决（见上方「无法连接 Tailscale 设备 260825」）**：Mac 经小火箭加入 tailnet 后数据面不通、tcpdump 0 包确认包未进隧道——根因是小火箭路由模式旁路路由 `100.64.0.0/10` 把流量绕过 Shadowrocket 裸发，删除该旁路即恢复；备选方案仍是装官方 Tailscale App（brew install --cask tailscale）

---

## 🐛 Antigravity IDE "working" 260824

> 场景：Mac（小火箭）通过 Antigravity IDE 远程连接 Ubuntu 主机开发，关闭 TUN 后 AI 对话一直显示 "working" 卡死；设置 ~/.bashrc 代理变量无效；重开 TUN 恢复正常。

```text
[排障步骤]

① IDE 显示 "working" 卡死
  ▼
② 排查：Mac 开小火箭 → 无效（AI API 从 Ubuntu 发出，不是 Mac）
  ▼
③ 打开 Ubuntu 上的 Clash TUN → 立即恢复
  根因确认：Ubuntu 直连 AI API 被 GFW 拦截
  ▼
④ 尝试设 ~/.bashrc 环境变量，关 TUN 测试 → 仍然失败
  ▼
⑤ 根因：Antigravity 以 daemon/service 运行，不继承 ~/.bashrc
  ▼
⑥ 当前可用方案：保持 TUN 开启（AI API 为小流量，不触发大流问题）
   长期方案：/etc/environment 或 systemd 注入（见下）
```

**为什么 Mac 开了小火箭，Ubuntu IDE 还是卡？**

```text
[流量实际路径]

  Mac（小火箭）
  ┌──────────────────────┐
  │  IDE 界面（仅显示）  │  ← Mac 只负责显示 UI
  └──────────┬───────────┘
             │ SSH / WebSocket 远程连接
             ↓
  Ubuntu 主机（Antigravity 核心运行在这里）
  ┌──────────────────────────────────────────┐
  │  Antigravity Agent                       │
  │  - 接收问题                              │
  │  - 调用 AI API（api.anthropic.com 等）  ─┼──→ 🌐 互联网
  │  - 执行工具（读文件、跑命令）            │   （被GFW拦截）
  └──────────────────────────────────────────┘

  Mac 的小火箭：只代理 Mac 自己发出的流量，
               对 Ubuntu 的出网请求完全无感知、无效。
  Mac 和 Ubuntu 是完全独立的两条出网路径。

[没有代理时（卡住）]

  你输入问题 → Mac IDE界面 → SSH → Ubuntu Agent
                                        ↓
                              尝试连接 api.anthropic.com
                                        ↓
                                   GFW 拦截
                                        ↓
                              一直等待超时 → 显示 "working"

[开启 Ubuntu Clash TUN 后（正常）]

  你输入问题 → Mac IDE界面 → SSH → Ubuntu Agent
                                        ↓
                              尝试连接 api.anthropic.com
                                        ↓
                              TUN 虚拟网卡透明接管
                                        ↓
                              代理节点（海外）→ API 返回 OK
                                        ↓
                                   AI 正常响应
```

**为什么设了 ~/.bashrc 环境变量还是不行？**

```text
[.bashrc 的生效范围]

  打开终端 → bash 启动 → 读 ~/.bashrc → 变量生效
                                          ↓
                               只在这个 Shell 会话里有效

  Antigravity 启动方式：systemd service / 开机自启 daemon
    → 不经过任何 bash shell
    → 完全不读 ~/.bashrc
    → 没有 https_proxy 变量 → 关了 TUN 就断

[代理的三个层级，及对 daemon 的覆盖能力]

  第一层：TUN（系统网络层）
    改路由表，强制接管所有 TCP/UDP，应用无感知
    覆盖：100%（含国内服务器，可能帮倒忙）
    daemon 是否受影响：是 ✅

  第二层：环境变量（进程层）≈ "软TUN"
    只有「读这个变量」的应用才走代理，仅覆盖 HTTP/HTTPS
    ~/.bashrc    → 只对交互 Shell 有效，daemon 不读 ❌
    /etc/environment → 系统级，PAM 登录时继承，daemon 也能读 ✅

  第三层：应用配置（应用层）
    git config http.proxy → 只影响 git，最精确
    daemon 不受影响 ❌
```

**大文件下载要关 TUN，AI API 要开 TUN，矛盾怎么解？**

```text
代理节点 = 一根水管

  API 请求（KB 级）：0.1 秒过完，水管稳不稳无所谓 → TUN 没问题
  5.2GB 镜像（GB 级）：要流 10-30 分钟，水管抖动即断 → TUN 有风险
                       且国内镜像走代理节点绕一圈海外，毫无意义

结论：两个场景风险不同，不矛盾
  日常 IDE 使用 → 保持 TUN 开启（请求小，快进快出）
  拉大文件时   → 临时关 TUN，走国内镜像直连
```

**不开 TUN 有没有其他方案让 IDE 正常？**

```text
方案一：/etc/environment（优先试）
  系统级变量，PAM 登录时所有进程继承（含 daemon）

  sudo tee -a /etc/environment <<'EOF'
  https_proxy=http://127.0.0.1:7890
  http_proxy=http://127.0.0.1:7890
  no_proxy=localhost,127.0.0.1,172.18.0.0/16
  EOF
  # 需重启或重新登录生效

方案二：找到 Antigravity 启动机制，直接注入
  ps aux | grep -i antigravity
  systemctl --user list-units | grep -i antigravity

  若是 systemd 服务：
  systemctl --user edit antigravity
    写入：
    [Service]
    Environment="https_proxy=http://127.0.0.1:7890"
    Environment="http_proxy=http://127.0.0.1:7890"
  systemctl --user restart antigravity

方案三：redsocks + iptables（精准重定向，无需 TUN）
  把指定进程/端口的 TCP 流量重定向到 SOCKS5
  不动路由表，不劫持 DNS，比 TUN 更轻量
  大文件可用 no_proxy 精确放行

方案四：mihomo tproxy 模式
  不建虚拟网卡，iptables tproxy 实现透明代理
  DNS 不劫持，比 TUN 更稳定

优先级：/etc/environment → 找启动机制注入 → redsocks → tproxy
```

**TUN vs 端口代理 决策框架**

```text
                    目标服务器能直连吗？
                          │
              ┌───────────┴───────────┐
              │ No（被GFW拦）          │ Yes（国内/不被拦）
              │                       │
              ▼                       ▼
        有国内镜像吗？         直连，不走任何代理
              │               （关TUN，不设proxy）
        ┌─────┴─────┐
     Yes│           │No
        │           │
        ▼           ▼
   关TUN，直连      数据量大不大？
   国内镜像源            │
   （最快最稳）    ┌──────┴──────┐
               小数据│         大文件│
                    │             │
                    ▼             ▼
              TUN/端口        显式端口代理
              均可            + HTTP/1.1降级
                              避免TUN
```

一句话原则：能直连就直连；必须代理选显式端口；TUN 只在「多应用全局翻墙」时开；大文件确认不过海外节点。


| 场景                 | 路径                           | 结论                    |
| -------------------- | ------------------------------ | ----------------------- |
| Antigravity AI API   | 被拦 → 无镜像 → 小数据       | 走代理（TUN或显式均可） |
| vLLM 5.2GB 镜像      | 被拦 → 有国内镜像             | 关TUN，直连华为云       |
| git push GitHub      | 被拦 → 无镜像 → 中等数据不稳 | 显式代理 + HTTP/1.1     |
| docker pull 国内镜像 | 不被拦                         | 直连，TUN 会帮倒忙      |
| apt install 国内源   | 不被拦                         | 直连，no_proxy          |

**关键认知**：

- **~/.bashrc 对 daemon 无效**：systemd service、nohup 启动的进程不读 ~/.bashrc；正确做法是 `/etc/environment`（系统级）或 systemd unit `Environment=`
- **TUN 与 AI API 共存无大流风险**：AI API 为 KB 级小数据，不触发 GB 级大流被掐断的问题
- **两个需求的和解**：日常保持 TUN 开启；拉大文件时临时关 TUN 走国内镜像

---

## ⚖️ TUN 透明代理 vs 端口代理 260823

```text
[git 流量出网 —— 两条路径对比]
  │
  ▼
┌─ TUN 透明代理（路由器模式）─────────────────────────┐
│  mihomo 创建虚拟网卡 + 改路由表，透明接管全部流量     │
│  应用无感知、无需配置 → 全局生效                      │
│  代价：DNS 被劫持（dns-hijack any:53 + fake-ip）      │
│        大流量易被掐断（vLLM 5.2GB 镜像拉断的根因）    │
│        内网需 route-exclude 白名单（本机已排          │
│        172.18.0.0/16）                               │
│  适配：日常网页/多应用全局翻墙                        │
└─────────────────────────────────────────────────────┘
  ▼
┌─ 端口代理（指路模式，本机当前）─────────────────────┐
│  本地端口监听（混合 7890 / Socks 7891 / HTTP 7892）   │
│  应用显式配置代理地址才走，其余直连                   │
│  不劫持 DNS、不影响内网、大流可配 no_proxy 精确放行   │
│  代价：每个应用要单独配置                             │
│  适配：git / docker 拉镜像 / 大文件下载               │
└─────────────────────────────────────────────────────┘
  ▼
选用结论：日常浏览开 TUN 省心；
         git/下载类用显式端口代理，要的是可控不是全局
```

**关键认知**：

- **两种模式最终都到节点出网**，成败取决于当时节点/链路质量；但**行为可控性完全不同**——TUN 是系统层全接管，端口代理是按需指路
- **"开 TUN 推送成功"是表象**：开 TUN 时 git 直连 github 的流量被透明接管走节点，链路好就成；失败那次是节点/链路抖动（长连接帧被掐断），与模式无关
- **显式代理后 git 走回环**：`http.proxy=127.0.0.1:7890` 是本地回环流量，TUN 一般不劫持 → 不再双重经过 TUN，且可叠加 `no_proxy`、`http.version HTTP/1.1` 等精确控制
- **本机当前状态（260823 实测）**：TUN 关闭（`mihomo.yaml` 第 24 行 `tun.enable: false`），混合 7890 / Socks 7891 / HTTP 7892 端口运行中
- **大流风险对照**：vLLM 镜像排障中 TUN fake-ip 劫持导致 5.2GB 反复断流（见下条）；git 大仓库长连接同样适用——节点不稳优先降级 HTTP/1.1 或走 SSH

---

## 🐛 git push 报 HTTP2 framing layer  260823

> 问题：git push 报 `fatal: unable to access 'https://github.com/a6b0x/edge.git/': Error in the HTTP2 framing layer`，最初以为是提交失败。实为 commit 成功、push 失败；两种网络错误交替出现 = 通道不稳，最终配置本机代理解决。

```text
[git push 报 HTTP2 framing layer —— 判定为提交失败]
  │
  ▼
① git status 澄清：工作区干净、ahead by 1
   commit 已落盘，报错发生在 push 阶段
  ▼
② curl -I https://github.com → HTTP/2 200
   网络并非不可达
  ▼
③ 开 GIT_TRACE_CURL 重试 push → HTTP/2 200、推送成功
   但立刻再推 → Connection timed out（TCP 建连超时）
  ▼
④ 判定：HTTP2 framing layer 与 Connection timed out 交替
   = 网络通道不稳（丢包/连接被重置），非 git 配置问题
  ▼
⑤ 检测本机代理：mihomo-smart 监听 127.0.0.1:7890/7891
  ▼
⑥ git config --global http.proxy http://127.0.0.1:7890
   → push 成功 ✅
```

**关键认知**：

- **commit 成功、push 失败**：`fatal: unable to access` 报错在 push 阶段；`git status` 显示 ahead by 1 且工作区干净，即证明 commit 已落盘
- **错误信息可定位失败阶段**：`HTTP2 framing layer` = TCP 已连上但帧数据被破坏/截断（丢包、中间设备干扰、MTU）；`Connection timed out` = TCP 握手阶段超时；两者交替出现 = 网络不稳定
- **本机代理是 mihomo-smart**：监听 `127.0.0.1:7890`（混合代理，HTTP/SOCKS 通用）；用 `ss -tlnp | grep 127.0.0.1:7890` 可确认
- **配置命令**：`git config --global http.proxy http://127.0.0.1:7890` 与 `https.proxy` 同值；取消用 `--unset`；配置对 git 全部子命令（clone/fetch/pull/push）生效
- **待解卡点**：mihomo TUN 透明代理仍在运行，曾掐断 5.2GB 大流（见下条 vLLM 排障）；git 大仓库/长连接若再断，备选方案是降级 HTTP/1.1（`git config --global http.version HTTP/1.1`）或改用 SSH 协议

---

## 🐛 vLLM 镜像拉取排障 260823

> 问题：拉取 0.27.1 引擎镜像（5.2GB）反复中断，耗时一天。先后经历误诊、串行重试无效、换国内源仍被劫持、改 NO_PROXY 无效，最终靠关 TUN + 补 DNS 才直连成功。全程不影响其他容器服务。

```text
[后台拉取 0.27.1 镜像 5.2GB —— 升级 MTP 的前置步骤]
  │
  ▼
┌─ ① EOF 反复中断 ────────────────────────────────┐
│   第 1 次 22:38:06 → 第 2 次 22:38:34，28 秒即断 │
│   用户："为什么这么久？不应该是增量么？"         │
│   确认增量有效：22 个 layer Already exists 复用  │
└─────────────────────────────────────────────────┘
  ▼
┌─ ② 误诊 + 串行重试 ─────────────────────────────┐
│   ss 显示 node 进程挂 CLOSE-WAIT → 以为是代理掐流 │
│   改串行重试 → 无效（--max-concurrent-downloads  │
│   是 daemon 配置非 CLI 参数，纯 pull 循环断流照旧）│
└─────────────────────────────────────────────────┘
  ▼
┌─ ③ 纠正误诊，定位真因 ──────────────────────────┐
│   用户："怎么这么长时间" → 深度诊断              │
│   PID 1363059 实为 Trae IDE 扩展进程，与 docker 无关│
│   真因：dockerd HTTP_PROXY=127.0.0.1:7890       │
│   → mihomo TUN 透明代理（fake-ip 198.18.0.162）  │
│   上游节点不稳 → 2-8GB 大 layer 流被掐断         │
│   证据：pull 进程 20 分钟 CPU 仅 2 秒（挂起等流） │
└─────────────────────────────────────────────────┘
  ▼
┌─ ④ 换国内源（用户引导）─────────────────────────┐
│   用户："国内没有这个的镜像么" → 华为云 SWR 同步站 │
│   实测仍被 TUN 劫持：连接全指向 198.18.0.x fake-ip│
└─────────────────────────────────────────────────┘
  ▼
┌─ ⑤ 改 NO_PROXY + 重启 dockerd ─────────────────┐
│   追加国内源域名到白名单 → 发现也没用            │
│   TUN 在系统网络层劫持全部流量，                 │
│   NO_PROXY 只对 dockerd 的 HTTP_PROXY 路径生效   │
└─────────────────────────────────────────────────┘
  ▼
┌─ ⑥ 用户关 TUN ─────────────────────────────────┐
│   流量不再被透明接管                             │
└─────────────────────────────────────────────────┘
  ▼
┌─ ⑦ DNS 挂 ─────────────────────────────────────┐
│   systemd-resolved 无上游（SERVFAIL）           │
│   mihomo 之前劫持了 DNS，关闭后未还原            │
│   修复：resolvectl dns <网卡> 223.5.5.5 119.29.29.29│
└─────────────────────────────────────────────────┘
  ▼
⑧ dockerd 直连国内源 → ESTAB → 几分钟拉完 ✅
```

**关键认知**：

- **断流根因在代理链**：mihomo TUN 透明代理把 docker 流量全部 fake-ip 化，上游节点不稳即掐断大流；重试、调并发都治标不治本——大 layer 每次掐断都从头重下
- **NO_PROXY 单独没用，关 TUN 才是关键**：TUN 系统层劫持全部流量，NO_PROXY 只绕 HTTP_PROXY 路径，改完实测连接仍全指向 fake-ip
- **"走国内源"由用户引导**：用户问"国内没有这个的镜像么"→ 搜索落地华为云 SWR 同步站（`swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/<镜像>:<tag>`，实测直连稳定，几分钟拉完 5.2GB）
- **治本三步（按实际生效顺序）**：① 关 TUN → ② 补 DNS（`resolvectl dns <网卡> 223.5.5.5 119.29.29.29`）→ ③ NO_PROXY 白名单配合 dockerd 直连国内源（改 `/etc/systemd/system/docker.service.d/http-proxy.conf`）
- **dockerd 重启不影响容器**：容器进程由 containerd 独立管理，dockerd 重启后容器自动恢复（后台算力容器实测无感）
- **镜像体积认知**：`docker images` 磁盘占用 28.1GB 是 overlayfs 统计假象，内容 8.91GB（CUDA+PyTorch 全家桶）；模型 35GB 在宿主机 `/root/edge/models` 只读挂载，**不进镜像**——换版本只需重拉引擎，模型不动

---
