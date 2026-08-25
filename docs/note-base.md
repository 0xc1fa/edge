# 网络 代理 下载

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
- **待解卡点**：Mac 经小火箭（Shadowrocket）加入 tailnet 后数据面不通，tcpdump 0 包确认包未进隧道，疑为 Shadowrocket 分流接管 tailnet 网段；备选方案：装官方 Tailscale App（brew install --cask tailscale）

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
