# 网络 代理 下载

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

## 🐛 git push 报 HTTP2 framing layer 错误排障 260823

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
