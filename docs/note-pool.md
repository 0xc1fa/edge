# AI 账号池

## WorkBuddy 账号池与每日签到

> 原问题：「在本机跑 workbuddy 有什么虚拟化方案」
> 修正后的表述：WorkBuddy 在本机（Ubuntu Server、无桌面）有哪几种运行形态？哪些形态**必须**虚拟化、哪些**不需要**？若确实要虚拟化本机有什么可选；以及由此浮出的真实需求——把多人的 AI 账号统一成资源池并每日领积分——该怎么落地。

**Q1 要在无桌面的服务器上跑 WorkBuddy，虚拟化是必需的吗？**

WorkBuddy 不是单一形态，先分清再谈虚拟化：


| 形态             | 平台/方式                                                                      | 是否需虚拟化             |
| ---------------- | ------------------------------------------------------------------------------ | ------------------------ |
| 桌面客户端 (GUI) | Win/macOS 官方；Linux 仅 deepin/UOS/银河麒麟                                   | 无桌面 → **需要**       |
| WorkBuddy CLI    | Linux 原生，headless、 `--bg`、daemon（HTTP API + Web UI + systemd user unit） | **不需要**，装二进制即可 |
| 任务沙箱         | 官方 **云端**沙箱（Ubuntu 22.04、root、预装 Python 3.11 等）                   | 本机 **不需要**          |

若确实要虚拟化（GUI 客户端 / 私有沙箱 / 多人共用），本机可选：


| 方案                           | 隔离强度 | 开销  | 本机落地成本             | 适用                         |
| ------------------------------ | -------- | ----- | ------------------------ | ---------------------------- |
| 0. 裸跑 CLI                    | 无       | 0     | 最低                     | 自用、只跑开发任务           |
| 1. Docker 容器                 | 中       | 极低  | 低（已有 Docker）        | 单任务/单项目                |
| 2. Incus 系统容器 (LXC)        | 中高     | 极低  | 低（已有 Incus）         | **推荐首选**                 |
| 3. Incus VM (qemu/KVM)         | 高       | 中    | 低（KVM 就绪）           | 私有沙箱、nested docker、GUI |
| 4. Windows VM                  | 高       | 高    | 中（镜像+授权+远程桌面） | 必须用官方 Win 客户端        |
| 5. gVisor / Kata / Firecracker | 很高     | 低~中 | 中（要装运行时）         | 多人共用、不可信任务         |
| 6. 官方云端沙箱                | 交给云   | 0     | 0                        | 不想自运维执行环境           |

**权衡**：原问题预设"要虚拟化"，但**只有 GUI 客户端 / 私有沙箱 / 多人共用这三种情况才需要**；自用跑开发任务走 CLI + daemon 即可，沙箱在云端，零虚拟化零隔离风险。
**结论**：原问题的答案是"大多数场景不需要虚拟化"——虚拟化不是跑 WorkBuddy 的前置条件。

---

**Q2 若确实要跑 Windows VM（官方 GUI 客户端），本机可行吗？Win10 还是 Win11？**

**方案**：Incus VM（本机 `driver: lxc | qemu`、`/dev/kvm` 存在、`kvm_amd` 已加载）。
**踩坑（纠正过的误判）**：一度认为缺 `qemu/OVMF/swtpm`——实际只查了 `PATH` 与 `/usr/share/OVMF`。Zabbly Incus 把整套虚拟化栈放在私有前缀 `/opt/incus`：`qemu-system-x86_64`(11.0.3)、`swtpm`(0.10.1)、`virtiofsd`、`OVMF_CODE.fd` 均实测可运行 → **大概率一个包都不用装**。


| Win11 硬性要求     | 本机实测                                              |
| ------------------ | ----------------------------------------------------- |
| TPM 2.0            | ✅ `swtpm` 0.10.1 可用                                |
| UEFI + Secure Boot | ❌ 只有 `OVMF_CODE.fd`， **无 `.ms`/`.secboot` 变体** |

**权衡**：

- **Win10**：零安装零重启（可走 SeaBIOS）；但已 EOL 无安全补丁，且官方 ISO 下载入口在收缩（需先确认有 ISO）。
- **Win11**：需补装 `ovmf`（实测 24 个**新增**包、0 升级 0 删除，不碰 docker/incus/nvidia/libc/内核，无 needrestart → **不停机**）；需 TPM 设备 + `security.secureboot`；可能要开窗口重启一次 incusd 才能让它找到固件（重启会重建 nftables，必须复核 tenant 出网三条）。
- 唯一真实影响是**磁盘 I/O**：dir 池 + raw 文件，Windows 安装/更新持续随机写，与 `tenant01` 共享同一 196G loop 镜像，会抬高同池延迟。
- 网络暴露：VM 默认挂 `incusbr0` 可达宿主管理口，等于内网跳板 → RDP 只绑 tailscale 地址，绝不 `0.0.0.0`。
  **结论**：能跑，Win10 是本机近乎零成本路径；但代价是授权 + 80G 盘 + 常驻内存 + 一个要打补丁的系统——**只为"有个能登录的客户端"并不划算**。

---

**Q3 真实需求浮出：不是"跑 WorkBuddy"，而是"多人的 AI 账号统一成资源池"**

**问题**：多个人的 AI 账号（含 WorkBuddy）如何统一成一个资源池、对外一个地址？
**方案**：不二选一，而是**上下叠**——new-api 做管控门面，magpie 做订阅账号池。

```
设备(Claude Code/Codex/Gemini CLI/WorkBuddy/脚本)
   │  唯一地址 + 令牌
   ▼
hub :18080         ← 管控层：用户/令牌/额度/审计，对外唯一入口
   │  渠道 → OpenAI 兼容
   ▼
pool :3425         ← 账号池层：订阅登录态汇聚 + 协议互转 + 路由组
   │
   ▼
Claude/Codex/Copilot/Gemini/Grok/WorkBuddy/各家 key
```

**权衡（决定性差异=账号形态）**：


| 维度        | new-api (hub)                       | magpie (pool)                       |
| ----------- | ----------------------------------- | ----------------------------------- |
| 能管账号    | **key 型**（API key/中转/本地模型） | **key 型 + 订阅型**（OAuth 登录态） |
| 多用户/租户 | ✅ 用户/令牌/额度/审计              | ⚠️ 仅网关密钥，无注册体系         |
| 协议        | OpenAI 兼容为主                     | OpenAI + Anthropic + Gemini 互转    |
| 路由        | ✅ 渠道分组/重试                    | ✅ 路由组 5 策略 + 配额窗口调度     |

- 账号**全为 key 型** → 仅 new-api 就够，不用引入 magpie。
- **含任一订阅登录态**（Claude/Codex/Copilot/Gemini/Grok/WorkBuddy）→ **必须 magpie**，new-api 接不进来。
- 风险：订阅共享条款/封号风控（多人账号同出口 IP）；magpie 单点 → hub 须配 fallback 渠道。
  **结论**：对外仍只有 `18080`；magpie 锁 `127.0.0.1`，由 hub 当渠道调用（hub 的 `NO_PROXY` 已含 `127.0.0.1`）。

---

**Q4 最初的动机"每日领积分"能自动化吗？**

**方案**：magpie **内置** WorkBuddy 签到——用量页 `Check in now` / CLI `magpie accounts checkin` / TUI 按 `c`；设置可开 daily check-in。
实测（本机已验证）：

```
docker exec magpie magpie accounts checkin
  ✓ 0x2077 checked in today +100 · a 10-day streak
  · Trae CN 0x2077 not eligible for the check-in
```

**权衡 / 否决**：

- **自定义脚本、模拟点击机器人、定时 cron 刷积分 → 否决**：属规避平台规则，有封号/清零风控；这不是"能不能写"的问题，是"不该写"。magpie 自带签到是产品能力，不在此列。
- **Trae 不支持**：`not eligible`，其积分只能去 Trae 客户端领，magpie 接不了（与早期观察一致）。
- 风控现实：一人多号本身可能触碰服务条款。
  **结论**：用内置能力（含设置里的每日自动开关），不写任何绕过脚本。
