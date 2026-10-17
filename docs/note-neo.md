# Neo 日新坊

## 🔐 租户验证 261017

**需求**

```text
① 删除「成员」概念，只保留会话
② 管理员口令登录，口令明文存 /root/edge/neo/data
③ 控制台只允许管理员用口令进入
④ 其他租户一律通过管理员签发的会话进入
⑤ 没有获批 → 停留在口令页
```

**方案**

```text
进入控制台的两条路（都落在同一张会话台账上）
─────────────────────────────────────────────
管理员 ──► 口令登录 ──► 会话（默认长期有效）
             口令 = data/admin-password.txt（明文 0600）
             make password / make password RESET=1

租户  ──► 管理员签发的链接 ──► 兑换一次 ──► 会话
             链接 32 字节随机、默认长期有效、一次即废
             没有链接的人停在口令页，看不到任何实例

会话台账：谁在哪台设备上、何时登录、最后活跃，均可读可删
         删一条 = 那条会话当场失效（HTTP 401），不是等它过期
```

---

## 🖥️ 租户 GPU 热加载卸载 261017

**需求**

```text
GPU 目前显示未挂载，要能申请
   · 固定时段
   · 申请后管理员可见、可审批
   · 管理员页面提供 GPU 热挂载能力
```

**流程**

```text
使用者在实例列表点「申请」→ 选日期、填用途 → 待批准
        │  提交时提示「已有 N 人排队」
        ▼
管理员在「GPU」页批准 ──► 点「挂载」──► 卡插到实例上
        │                                    │
        │                                    ├─ 问要不要重启（不自动）
        ▼                                    ▼
   驳回 / 取消                          服务端到 16:00 自动摘卡并写回台账
```

**关键取舍**


| 事项         | 结论                                                                                                         |
| ------------ | ------------------------------------------------------------------------------------------------------------ |
| 批准 vs 挂载 | **分成两步**。批准是同意，挂载是执行；合起来做的话，一次设备层失败（卡被占、实例已删）会让审批结论本身变含糊 |
| 待处理申请   | **不占额度**（批准才占）。否则第一个人被驳回时，第二个人的申请早过期了；提交时会提示「已有 N 人排队」        |
| 重启实例     | **不自动**。这台机器上容器内 `/dev/nvidia*` 通常要重启才出现，挂载成功后页面询问，由管理员按下               |
| 时段         | 常量在 `internal/store/gpu.go`                                                                               |

**架构变化**

```text
 首次允许写宿主（此前对 Incus 只读）
   internal/incus/write.go   三个动作：加设备 / 摘设备 / 重启实例
                             每个都对应预约台账的一次状态迁移，都要管理员权限
                             异步 operation 必须等落地，否则「已接受」被当成「已完成」
                             加/摘设备走「GET → PUT 整份定义 → 等 operation」（见踩坑）

 新台账 internal/store/gpu.go   data/gpu-bookings.json
   pending → approved → active → ended（到时间由服务端自动摘）
   按天互斥：同一天的卡只批给一个人

 新调度 main.go   go handler.RunGPUSweeper(ctx)   30s 巡检，只认 status=active

 首度放开 docker-compose 的 socket 挂载：ro → rw
```

**踩坑：时区静默失效**

容器里 `date` 显示 UTC，而 `TZ=Asia/Shanghai` 一直存在。原因是运行时镜像（alpine）没有 tzdata，`TZ` 这个名字**无从解析**，Go 与 musl 都静默回落到 UTC —— 不报错、日志里也看不出来。后果是时段整体错 8 小时：16:00 自动摘卡发生在北京时间半夜零点。

两道保险：

```yaml
# 配置层：把宿主时区库挂进容器
volumes:
  - /usr/share/zoneinfo:/usr/share/zoneinfo:ro
```

```go
// 代码层：自带嵌入时区库，换镜像/换运行时仍然对
import _ "time/tzdata"   // cmd/neo/main.go
```

启动日志打「时区 / GPU 时段」两行，供人工一眼核对。

**踩坑：Incus 没有「加设备 / 删设备」端点**

第一版按直觉写了两个端点，页面上点「挂载」一直报 `incus error 404: not found`：

```text
POST   /1.0/instances/{n}/devices          → 404
DELETE /1.0/instances/{n}/devices/{dev}    → 404
```

设备不是独立资源，它是实例定义里的一张映射，只能整份提交。官方 CLI 的真实动作（`incus --debug config device add` 一眼可见）是：

```text
GET  /1.0/instances/{n}                取回定义，顺带拿到响应头 ETag
PUT  /1.0/instances/{n}  If-Match: …   提交整份定义（改掉的只是 devices 里那一项）
GET  /1.0/operations/{id}              等异步操作落地
```

两个连带结论：


| 事项                 | 结论                                                                                                                                                                                     |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| PUT 的字段           | 照抄 CLI 那一份（architecture / config / devices / ephemeral / profiles / stateful / description）。 **整份替换，少带一个字段等于把它删掉**，尤其 `volatile.*` 必须原样带回              |
| If-Match             | 必带。读到写之间管理员可能刚在宿主上敲过命令行，不带 ETag 就是让后写的人悄悄覆盖前一个改动                                                                                               |
| `pci` 与 `productid` | **互斥**。 `Cannot use "productid" when "pci" is set` —— 按地址挂载已经精确到卡，厂商与型号是冗余描述，别写                                                                            |
| 摘卡收尾             | 最后一个 GPU 设备摘掉后要清 `nvidia.runtime`，否则容器里一直留着 `/dev/nvidia-uvm`、 `/dev/nvidiactl`，与「这台机器没卡」不符。宿主脚本 `gpu off` 同样这么收尾，两边必须收在同一个状态上 |
| 重启实例             | 状态端点 `PUT /1.0/instances/{n}/state` **只认 PUT**，POST 一律 `501 not implemented`。页面上的表现只有「重启失败」四个字，看不出是方法用错了                                            |

核验口径：`nvidia-smi -L` 在租户里说 `GPU 0: … RTX 4090` 才算挂上，说 `No devices found.` 才算摘干净。

---

**网页终端 / 重置口令 / 开关机，以及租户与管理员分视图**

按角色拆成两个视图，并删掉租户实例页顶部的四张宿主配额卡片。

**视图拆分的边界**：`GpuPane` 变成一层薄选择器，成员看 `gpu/MemberLog`（只读调度日志），管理员看 `gpu/AdminConsole`（日志 + 待办 + 操作列 + 待办优先排序）。数据、接口、状态字典仍然只有一份（`gpu/Shared.tsx`），服务端按角色裁剪。**分的是视图，不是业务**。

**归属是这个功能的地基**。电源、口令、终端都会动到一台具体的机器，所以先回答「这台机器是不是这个人的」：


| 判据                                                    | 结论                   |
| ------------------------------------------------------- | ---------------------- |
| 管理员                                                  | 不受限                 |
| 台账里登记在谁名下（ `AppStore.Hostname → Applicant`） | 谁就能动               |
| 没登记属主的实例                                        | 谁都不能动（除管理员） |

可使用 GPU 预约台账当归属来源

**踩坑：零依赖做 WebSocket 与 Incus exec**

这个项目长期保持 `go.mod` 零依赖（镜像构建完全离线），所以 WebSocket 是手写的（`internal/wsutil`：握手、帧编解码、分片重组、自动回 ping）。三个坑都只有「终端一闪就没」这一种表现：


| 坑                                      | 现象                                                                                                  | 结论                                                                           |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| exec 的 POST 必须带 `Connection: close` | keep-alive 会让 Incus 一直等 websocket 连接，几十秒后回 `Timed out waiting for websockets to connect` | 同样的请求用 curl/python 发都正常，差别就在这一个头                            |
| `fds` 里列出的连接要全建                | 只连 stdin 那条，同样等到超时                                                                         | 至少 stdin + control 两条；control 上的数据要有人读，否则 Incus 写满缓冲区卡住 |
| 关闭终端必须取消 Incus operation        | 只关 WebSocket 会在 Incus 侧留下 RUNNING 的僵尸，攒够之后新终端与改口令全部转圈超时                   | `DELETE /1.0/operations/{id}`，关连接之前先取消                                |

另有两个小坑：`pty` 会话里**等 EOF 等不到**（shell 退出后 Incus 未必发 close 帧），所以一次性命令改成「读到完成标记就收」；pty 下 `nvidia.runtime` 与设备热插的可见性判据写在本章上一节。

**改口令**走容器内 `chpasswd`（不需要知道旧口令 —— 「忘了旧口令」正是这个功能存在的理由），口令用单引号包裹再交给 sh，否则 `$`、`` ` ``、`;` 会被容器里的 shell 当场解释掉，结果是「设置成功但口令不是用户输的那个」。

**操作列只给租户**。网页终端、改口令、开关机是「对自己那台机器」的自助动作，管理员那一侧不出现 —— 他要动手是在宿主上敲 `tenant-ctl.sh`。在管理员面前摆一排按钮，只会让他以为这是推荐的操作路径。管理员保留的是监控与归属信息（每台实例登记在谁名下、未登记的标明「未登记属主」）、GPU 调度页的审批与挂载，以及**归属分配**。

## 🛒 轻量云主机控制台 261010

**需求**

```text
公开访问（任何人可访问）                    需登录（后台）
① 两类产品                              ⑤ 审批
  · 轻量云主机（算力主体）                  · 数据不匿名可读，需鉴权
  · GPU（附加项，不能单独成单）  
  · 观感要像能收费的产品
  · 信息来自真实实测，不编不造
        │
        ▼
  ② 选型 ≠ 下单
     · 先看「有什么」，再决定「要几台 / 搭哪档」
        ▼
  ③ 填申请做减法
        ▼
  ④ 拿回执 · 自查进度
     · 回执脱敏返回
```

---

**架构与技术栈**

```text
 浏览器 / 客户端
   │  HTTP :5902（SPA 静态 + /api 同源）
   ▼
 neo 容器（Docker · 运行期 alpine:3.22）
   neo serve   Go 1.24 · 零三方依赖 · 静态二进制
     ├─ HTTP :5902
     │    ├─ /       → 前端静态 (/usr/share/neo/web)
     │    └─ /api/*   → 路由 (internal/api)
     ├─ 业务模块  internal/
     │    api · auth · catalog · incus
     │    limit · recon · store · identity
     └─ 配置 NEO_*（前缀来自 internal/identity）
           │ 挂载 ./data → /data
           ▼
   /data：secrets.json + bootstrap-password.txt（JSON, 0600）
   │  UNIX socket /var/lib/incus/unix.socket
   ▼
 宿主 Incus：系统容器① 系统容器② …（交付形态＝系统容器，非 KVM）
```

后端（Go，零三方依赖，纯标准库）：

- 语言/版本：Go 1.24，`module neo`；`go.mod` 仅两行、**无任何第三方依赖**，构建完全离线（`CGO_ENABLED=0` 静态二进制）
- 模块划分（`internal/`）：`api`（HTTP 路由与鉴权入口）、`auth`（会话与口令）、`catalog`（产品模型与库存口径）、`incus`（对接 Incus socket）、`limit`（配额/限流）、`recon`（实例对账）、`store`（长久状态）、`identity`（产品名与环境变量前缀的唯一来源）
- 对接宿主：经 UNIX socket `/var/lib/incus/unix.socket` 调 Incus `/1.0` API，交付形态为**系统容器**（非 KVM）；GPU 库存因 `/1.0/resources` 不报，改由 `NEO_GPU_TOTAL` 注入
- 长久状态：`internal/store` 写 JSON 文件——`secrets.json`（PBKDF2 口令哈希 + 会话签名密钥，带 `version` 便于迁移）+ `bootstrap-password.txt`；目录 `0700`、文件 `0600`，落在 `NEO_DATA_DIR`（默认 `./data`，容器 `/data`）
- 配置：全部走 `NEO_*` 环境变量（前缀来自 `internal/identity.EnvPrefix`），监听 `:5902`
- 运行：多阶段 Docker 构建（go-builder / web-builder / alpine:3.22 运行期）；`HEALTHCHECK` 用 `neo probe` 自检数据链路

前端（React + TDesign）：

- 框架：React 18.3.1 + TypeScript 5.6.3 + Vite 5.4.11（构建脚本 `tsc -b && vite build`）
- 组件库：`tdesign-react` ^1.18.4（腾讯官方，与腾讯云控制台同源设计语言；主色以 `--td-brand-color` 覆盖到 `#006EFF`）；布局件（Row/Col、Card、Divider、Steps、Form、Descriptions、Drawer、Statistic）与基础件一并替换，删掉手写组件样式
- 路由分包：控制台（`#/admin`）走 `React.lazy` 路由级懒加载，公开页因此少背约 78KB
- 标识：Logo 为 `/root/edge/neo/logo/` 下两份 SVG，经 Vite `import` 内联进 JS（<4kb 默认内联，dist 下无独立 .svg）
- package 名仍为 `vmconsole-web`（历史遗留，与模块名 `neo` 不一致，待统一）

**UI 框架选型**


| 候选                          | 匹配度     | 判断                                                                                                  |
| ----------------------------- | ---------- | ----------------------------------------------------------------------------------------------------- |
| **TDesign React**（腾讯官方） | ★★★★★ | 与腾讯云控制台 **同源设计语言**，配色/控件尺寸不用重新争论；有 Form/Table/Select/Tag/Message 等重组件 |
| Ant Design                    | ★★★★☆ | 生态最全、表单表格最强；视觉是阿里系，要把主题定制到腾讯云蓝；体积偏大                                |
| Arco Design（字节）           | ★★★☆☆ | 设计语言接近，React 版成熟；非腾讯系，细节仍要调                                                      |
| MUI                           | ★★☆☆☆ | Material 风格，不像云控制台                                                                           |
| shadcn/ui + Tailwind          | ★★☆☆☆ | 只给地基不给风格， **不解决「像不像」**                                                               |
| Element Plus                  | ✗         | Vue 为主，本项目 React 不适用                                                                         |

**推荐 TDesign React**（备选 antd，条件是后续表单/表格复杂度明显上升）。
引入路径按「先基础件、后页面」分两步，避免一次性重写：

```text
① 基础件替换   Button / Input / Select / Table / Tag / Message / Radio
               主题用 TDesign CSS 变量把 --accent 映射到 #006EFF，其余保留默认值
② 页面级收口   产品卡 / 申请区 / 进度查询改用 Layout + Grid（1200px 容器、4px 栅格）
               删掉与之重复的手写样式
```
