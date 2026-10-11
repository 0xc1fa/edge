# Neo 日新坊

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
