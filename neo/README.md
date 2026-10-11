# console · 轻量云主机控制台

`host/`（宿主侧脚本）与 `tenant/`（租户侧脚本）之外的第三块：**面向人的操作面**。
第一版只做一件事——把宿主机上正在运行的实例，以云厂商控制台的样子呈现出来。

## 形态与边界

当前实例是 **Incus 系统容器**，不是 KVM 虚拟机（宿主未装 qemu/OVMF）。
页面刻意把形态显示为「容器」而非「虚拟机」，避免租户误判隔离级别。

第一版**完全只读**：unix socket 以 `:ro` 挂载，代码里没有任何写操作接口。

## 两个访问面

同一份服务，**两条权限水位**，前端用 hash 路由区分：

| 入口 | 面向 | 鉴权 | 能看到 |
| --- | --- | --- | --- |
| `#/` 产品主页 | 任何拿到地址的人 | 无 | 售卖目录、提交申请、凭回执查自己的进度 |
| `#/admin` 管理员控制台 | 后台验证过的人 | 会话 cookie | 实例清单、全部申请、申请人的联系方式与公钥 |

**匿名可用 ≠ 匿名可读。** `/api/instances` 与 `GET /api/applications` 一律 401，
只有 `POST /api/applications`（提交）与 `GET /api/applications/{id}`（凭回执查自己）开放。
这条边界由 `internal/api.isPublic()` 按**方法**（不只是路径）判定，
并有一条专门的单测守着：`TestIsPublic`。

## 结构

```text
cmd/neo/       入口：serve | probe | version
internal/incus/      唯一的外部依赖点（REST over unix socket）+ golden fixture
internal/recon/      10s 一轮对账，产出快照与近 30 分钟采样
internal/api/        REST + 静态托管，含访问控制水位判定
internal/auth/       单管理员：口令校验通过即平台管理员
internal/limit/      滑动窗口限流（登录与匿名提交共用）
internal/catalog/    售卖目录：标准实例 + GPU 附加项 + 镜像 + 可售判断
internal/store/      口令哈希、会话密钥、开通申请台账
web/                 React 18 + Vite + TypeScript（严格模式）+ TDesign React
```

**健康口径沿用 obs：进程活着 ≠ 健康。**
`/api/health` 带 `last_reconcile_age_ms`，超过「3 个采集周期 + 10s」即判 503，
`probe` 子命令据此给 Docker healthcheck 返回退出码。

## 信息一致性

页面数据全部来自 Incus REST，**不落离线副本**。
- 配额取 `config.limits.*` 与 `devices.root.size`
- 磁盘用量取 `state.disk.root`（比 `incus exec df` 更快更准，停机实例不查）
- 网络取 `state.network.*.addresses`，SSH 入口取 `type=proxy` 的 device
- GPU 归属判据是**实例自身的 devices**，不是宿主 `nvidia-smi` 的行数

## 当前生效值

| 项 | 值 |
| --- | --- |
| 端口 | `5902`（5901 属 miner-obs/solar） |
| compose 工程名 | `edge-vm-console`（显式指定，避免误删同名栈） |
| 容器名 / 镜像 | `neo` / `neo:0.1.0` |
| 网络 | `network_mode: host`（bridge 的 DNS 会被 TUN 劫成 fake-ip） |
| Incus socket | `/var/lib/incus/unix.socket`，只读挂载 |
| 数据目录 | `./data`（宿主可读；含 `secrets.json` 与 `applications.json`） |
| 采集间隔 | 10s；健康容忍窗口 40s |
| 会话 | cookie `vmc_session`，12h，HttpOnly + SameSite=Lax |
| GPU 总量 | `NEO_GPU_TOTAL=2`（resources 拿不到卡信息，做成配置而非自动探测） |
| 匿名成功单 | 同一来源 1 小时 5 单（保护管理员待办） |
| 匿名失败尝试 | 同一来源 1 小时 30 次（防止脚本枚举主机名） |

**这两条额度是分开计的**：表单填错几次不该把本小时的申请额度耗光；
但失败也不能完全不限，否则 `/api/applications` 就成了「哪些主机名已存在」的探测器。

## 常用命令

```bash
make vet      # 静态检查
make test     # 单测：以真实 Incus 响应为 golden fixture
make web      # 构建前端
make image    # 构建镜像
make up/down  # 启停服务
make smoke    # 冒烟：健康 / 401 收口 / 登录 / 与 incus list 逐字段比对
```

## 登录

`#/admin` 是唯一需要登录的入口。首次启动自动生成管理员口令并写入
`data/bootstrap-password.txt`，同时打印到日志（`make token` 可再次查看）。

该口令文件由管理员自行留存或删除，登录后不再自动销毁。
需要重设时：`docker compose down && rm -f data/secrets.json && docker compose up -d`。

想固定口令：在 `docker-compose.yml` 里放开 `NEO_ADMIN_PASSWORD`。

## 申请流转（当前的边界）

```text
匿名选配 → POST /api/applications → 台账 pending
                                        │
                管理员在 #/admin 批准 ────┤──► 改状态 + 生成建议开通命令
                                        │
                          ✗ 不自动创建实例
```

**批准后不会自动开通。** Incus socket 目前是只读挂载，且创建实例涉及端口池与整卡互斥，
这两个分配器还没落地，自动执行的收益小于风险。所以批准后页面上给出可直接复制的命令，
由管理员在宿主确认执行。这是刻意保留的人工闸门，不是没做完的功能。

申请人凭回执号自助查进度时，联系方式与公钥是**脱敏**返回的
（`al***@ex.com`、`ssh-ed25519 AAAA…(已脱敏)`），避免回执号泄露即信息泄露。

## 产品规则

只有两类，且不是并列关系：

四个上架项，两类产品：

| 类别 | 上架项 | 能否单独申购 |
| --- | --- | --- |
| 云主机 | 云主机（2 核 4G 10G）、高级云主机（4 核 8G 20G） | 必选，它就是算力主体 |
| GPU | RTX 4090（24GB）、RTX PRO 6000（96GB） | **不能**，必须挂在云主机上 |

**产品页只回答「这是什么」**：GPU 卡上只有型号，没有数量；
「配几张」是下单动作，在申请表单里用分段控件选。这两件事混在一张卡上，
页面就得同时承担选型和下单，两边都做不好。

**页面上写的是库存，不是「本机有几张卡」**——这是商品语言，不是机器语言。
RTX 4090 库存 2 张（实测 `nvidia-smi -L`）；RTX PRO 6000 库存 0，对外一律标
**「待上架」**。它与「已被全部占用」是两种状态：前者是还没上货架，后者是上过但卖完了，
对申请人是完全不同的信息，`TestZeroStockNeverSellable` 守着这条措辞。
等真有货了，改 `catalog.Default()` 里的 `Total` 即可。

已挂的卡通过设备的 Product ID（`2684`）认回型号；认不出来的占用全部记到
`DefaultTarget` 名下——**宁可少卖一张，也不能超卖**。

「GPU 依附于标准实例」是产品规则，因此它在**服务端** enforcing（`RequiresFlavor` +
`MaxGPUCards` 两处校验），前端置灰只是给人看的提示。要增加档位就往
`catalog.Default()` 的 `Flavors` 里追加一项，其余链路无需改动。

产品页上是**两张卡各自一个「立即申请」**：点「GPU 卡」会自动连带一台云主机，
不需要用户先理解这层依赖关系。

**申请表单刻意做得很短**，首次申请只需填「申请人 / 联系方式 / 用途 / 时长」：

| 字段 | 处理方式 | 理由 |
| --- | --- | --- |
| 主机名 | 服务端生成 `vm-xxxxx`（随机，非自增序号） | 让人纠结命名规范是纯成本；自增还会暴露租户数量 |
| 镜像 | 取 `Default()` 标记的镜像 | 选镜像是技术判断，不该丢给第一次来的人 |
| SSH 公钥 | **不用**（选填） | 以 root + 开通时下发的随机口令交付 |
| 登录账户 | 恒为 root | 第一版不做多用户体系 |

这些默认值写在服务端而非前端：表单里没有的字段，直接发 HTTP 也一样拿得到同样的默认值。

## 前端：组件交给 TDesign

按钮 / 输入 / 下拉 / 分段 / 表格 / 标签 / 弹层提示全部用 **TDesign React**（腾讯官方，
与腾讯云控制台同源设计语言），手写 CSS 只保留三件事：页面布局、产品卡这类强定制块、少量对齐微调。

**布局也用框架**，不只是按钮输入框：栅格 `Row/Col`、卡片 `Card`、分隔 `Divider`、步骤条 `Steps`、
表单 `Form/FormItem`、描述列表 `Descriptions`、抽屉 `Drawer`、统计 `Statistic`、概览提示 `Alert`。
手写 CSS 只剩页面级容器与产品卡的少量定制（798 行，换框架前 1303 行）。

```text
src/td.ts            按需出口：基础件 + 布局件，只注册真正用到的组件
src/main.tsx         TDesign 样式排在自定义样式之前，主色由 --td-brand-color 覆盖
src/App.tsx          三个面：#/ 产品页、#/apply 申请页、#/admin 控制台（懒加载）
src/styles.css       只剩页面容器与产品卡；组件级样式已删除，避免两套圆角/高度并存
```

## 页面结构：选型与下单分开

```text
#/        产品页   两类产品，每类两款 —— 轻量云主机（2核4G / 4核8G）、GPU（4090 / PRO 6000）
                   只回答「有什么」；点「立即申请」带着选型跳走
   │
   ▼  #/apply?flavor=…&gpu=…&cards=1
#/apply   申请页   步骤条 + 表单（4 项）+ 右侧配置清单；提交后整页换成回执
   │
   ▼
#/admin   控制台   登录 → 实例（表格 + 抽屉）/ 申请审批（表格）
```

选型和下单本来就是两件事：产品页不塞表单，表单才能占满整屏、也才配得上一个明确的提交动作。

实测代价（gzip）：公开页 `156KB JS + 19KB CSS`，后台再加 `78KB JS + 9KB CSS`；
换框架之前是 `54KB + 4KB`。**按组件子路径引入几乎没有收益**——Table 一个组件就把大半库拖进来了，
真正有效的只有路由级分包。

几个踩到的点，改的时候别再踩：

- `TabPanel` 不是顶层导出，要用 `Tabs.TabPanel`
- `ConfigProvider` 没有 `theme` 属性，主色改 `--td-brand-color` 系列变量
- `Input` 的属性是 `autocomplete` / `autofocus`（小写），不是 HTML 的驼峰写法
- 类型（`PrimaryTableCol` 等）仍从 `tdesign-react` 引入：`import type` 编译期擦除，不进产物

可售档位是实时算的，**待处理申请按「预占」计入**：两个人同时勾选最后一张卡时，
第二个人会在页面上看到缺货，而不是等管理员来解释「卡没了」。

## 为什么构建方式是这样

宿主的 registry-mirrors（`dockerproxy.net`、`docker.1ms.run`）均已失效，
且 mihomo TUN 的 DNS fake-ip 让 dockerd 拉取外部镜像返回 404 / TLS 握手超时。
因此镜像三个阶段只用本地已有的 `ubuntu:24.04` 与 `alpine:3.22`，
Go 与 Node 以官方二进制包放在 `build/` 下随构建上下文提供，
**任何一台拔掉外网的环境都能重现这个构建**。

`build/go.tar.gz` 与 `build/node.tar` 不入库（见 `.gitignore`）。
