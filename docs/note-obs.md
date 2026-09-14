# obs 监控

## 🚨 Grafana 数据 404 260912

> 现象：`/d/Oxed_c6Wz/dcgm-exporter-dashboard?...&var-gpu=$__all` 打不开，且所有看板的面板都挂红角标。

```text
[现象：一个看板打不开 + 所有看板红角标]
  │
  ├─▶ 看板被删 / 迁移时丢了
  │     → ✖ 查 API /api/dashboards/uid/Oxed_c6Wz：在库，
  │       title "DCGM Exporter Dashboard"、folder General
  ├─▶ URL 上模板变量 var-instance / var-gpu=$__all 不对
  │     → ✖ 变量只决定取哪部分数据，解释不了"全部"看板都红
  └─▶ 面板查询本身 404
        → ✔ 所有看板共用一个数据源，只有它能让"全部"同时失败
```

**排除两头：看板在库，采集端也健康**

- 看板在库 ≠ 在 provisioning 目录：它**不在** `obs/dashboards/`（目录里只有 `overview.json`），是 UI 导入存进 `grafana.db` 的——目录里没有不等于丢了，要用 API 确认。
- Prometheus 侧健康：5 个 target 全 `up`，GPU 在采（2 卡 77℃）→ 断的是取数地址，不是采集端。

**决定性证据**（Grafana 容器内实测）：


| 请求                                                | 结果    |
| --------------------------------------------------- | ------- |
| `http://prometheus:9090/api/v1/query?query=up`      | **404** |
| `http://prometheus:9090/prom/api/v1/query?query=up` | 200     |

**根因**：Prometheus 启了 `--web.route-prefix=/prom`，API 全挂到前缀下；数据源地址少了这一段，每次查询都打在不存在的根路径上。

```text
面板 → datasource proxy → http://prometheus:9090 + /api/v1/query
                                 ↑ 少 /prom → 404
实际路由：--web.route-prefix=/prom → /prom/api/v1/query
```

**修复**：`obs/grafana-datasource.yml` 的 `url` 补成 `http://prometheus:9090/prom`，并显式固定 uid `PBFA97CFB590B2093`——该看板 11 个面板、24 处数据源引用全指向这个 uid；provisioning 不写 `uid` 时 Grafana 自生成，数据源一旦换 uid 这 24 处引用集体失效，而 UI 导入的看板改 provisioning 文件也救不回来。

**认知**

- `--web.route-prefix` 改的是 **API 挂载点**（`/api/v1/*`、`/-/reload` 全移到前缀下），`--web.external-url` 只改页面里生成的链接——两者不等价。加了前缀，所有消费方（数据源、脚本、`curl`）都要跟着改。
- 排障顺序：先在 Grafana 容器里 `curl` 数据源探针，再回头怀疑看板——红角标只是表象。

---


> 现象：取数修好后，日志仍持续刷 `ws://<公网IP>:2030/grafana/api/live/ws` 404。两个入口都能开 Grafana——公网 2030 直连宿主 3000、2029 走 Caddy 网关——从 2029 进的页面却在连 2030。

```text
[诉求：只留一个对外入口]
  │
  ├─▶ 双入口保留，root_url 改相对路径 / 去掉端口
  │     → ✖ root_url 必须是完整 URL；去掉端口后页面生成的
  │       绝对链接仍会落到另一个入口，分叉并没解决
  ├─▶ 统一到 2029 网关，root_url 指向 2029
  │     → ✔ 页面所有绝对链接（含 Live WS）统一到网关
  └─▶ 2030 也接进网关，网关内再转一层
        → ✖ 云侧端口映射直通宿主 3000，网关插不进这条链路；徒增一跳
```

**为什么双入口必然出事**

```text
前端拼 Live 地址（12.1.1 源码）
  liveUrl = `${appUrl.replace(/^http/,'ws')}/api/live/ws`
                      ↑ appUrl 取自 root_url，改造前 = http://<公网IP>:2030/grafana/
```

- `serve_from_sub_path=true` 只保证 `/grafana/...` 路由命中（相对 301 让同 host 各端口都能把页面打开）；
- 页面里生成的**绝对**地址（Live WS、邮件/告警链接、API 回跳）一律由 `root_url` 决定。root_url 指 2030，从 2029 进的页面就必然跨端口去连 2030 的 WS → 404。

**实施与验证**

- `obs/.env` 的 `GF_SERVER_ROOT_URL` 改为 `http://<公网IP>:2029/grafana/`；改完必须 `docker compose up -d grafana` **重建**容器——`docker restart` 不重读 `.env`。
- 2029 页面的 `appUrl` = `...2029/grafana/`；从 2030 进去 `appUrl` 同样指 2029 → 老书签自动收敛（软统一），不必等云侧删映射。
- Live 端点 `...2029/grafana/api/live/ws` 握手返回 401（未带会话的正常响应），不再是 404。

**卡点**：`appUrl` 是页面加载时烘焙进 JS 的，旧标签页不硬刷新会继续按旧地址重试——重建后仍观测到 21 次这类 404；历史握手成功 0 次，Live 推送本就一直没生效，面板取数走 HTTP 不受影响。

---

## 🐛 监控目标 vllm 不可达 260901

vLLM 服务全程健康，告警来自「采集链路」——vLLM 端口安全收紧后 prometheus 抓不到，改用容器网络直连修复，全程未重启 vLLM。

**背景拓扑**（告警时的状态）：

```text
┌─ infer/vllm 项目 ───────────────────────────────┐
│ vLLM 容器 (vllm_default 网络, IP 172.21.0.2)     │
│   容器端口 8000 → 宿主端口 127.0.0.1:18000       │
└──────────────────┬───────────────────────────────┘
                   │ prometheus 在容器里，原经宿主 IP 抓取
┌──────────────────▼───────────────────────────────┐
│ obs 项目                                          │
│ prometheus 容器 (obs_default 网络)                 │
│   抓取目标写死：172.18.0.1:18000（宿主 docker0）   │
└──────────────────────────────────────────────────┘
```

**时间线**：


| 阶段         | 事件                                                                                 |
| ------------ | ------------------------------------------------------------------------------------ |
| 84c28fa 之前 | vLLM 端口`"18000:8000"`（绑宿主全部网卡），prometheus 经 `172.18.0.1:18000` 正常抓取 |
| 提交 84c28fa | 安全加固改`"127.0.0.1:18000:8000"`（只绑回环）→ 容器内访问宿主该端口被拒            |
| 84c28fa 之后 | prometheus 抓`172.18.0.1:18000` → refused → `up=0` 持续 5 分钟 → 钉钉告警         |

**排查：告警 ≠ 服务挂了**：

```text
宿主 curl 127.0.0.1:18000/metrics      → 200 ✔ 服务健康
nvidia-smi                             → 两卡各占 ~23GB，日志持续 200 OK
prometheus → 172.18.0.1:18000/metrics  → refused ✖ 断的是采集链路
```

**根因**：vLLM 端口收紧为只绑回环后，该端口只对宿主本机开放；而 prometheus 抓取目标写死的正是「宿主 IP + 该端口」，在容器里必然连不上。

**止血（零中断）**：`docker network connect vllm_default prometheus` 把 prometheus 挂进 vLLM 网络，target 改为容器内地址 `172.21.0.2:8000`，`POST /-/reload` 热加载 → `up=1`。未重启 vLLM。

**端口映射三段式语法**：`宿主IP:宿主端口:容器端口`，省略 IP 即 `0.0.0.0`。


| 写法                      | 监听范围                                   |
| ------------------------- | ------------------------------------------ |
| `"18000:8000"`            | 宿主全部网卡（局域网、Tailscale 均可访问） |
| `"127.0.0.1:18000:8000"`  | 仅宿主本机回环                             |
| `"172.21.0.1:18001:8000"` | 仅该网桥的网关地址                         |

**为什么不能简单改回 `18000:8000`**：vLLM 自身不校验 API key，鉴权全靠 new-api 网关。绑全部网卡 = 局域网/Tailscale 内任意设备可免鉴权直调模型；且 Docker 自写 iptables、**默认绕过 ufw**，想拦需手写 `DOCKER-USER` 链，重启易失效。

**关键认知：Docker 内嵌 DNS 的作用域是「网络」而不是「主机」**

- 容器名解析：单网络容器正常；多网络容器**只能解析「请求源 IP 所属网络」的名字**。
- 内嵌 DNS 固定 `127.0.0.11`，按请求从哪个网络进来决定搜哪个网络。prometheus 挂着 `obs_default` + `vllm_default` 两个网络，必然有一个方向查不到（官方 bridge 文档 `alpine4` 例证）。
- 实测：prometheus 里 `nslookup vllm-qwen36` → No answer；单网络 busybox 查同一名字 → 正常。
- 结论：**跨项目监控必须写 IP，用不了服务名自动发现**。固定 IP 是逆 Compose 官方建议（always reference services by name）的兜底，只在 DNS 此路不通时才用。

**方案对比（跨项目怎么抓 vLLM /metrics）**：


| #  | 方案                    | 做法                                         | 结论                                                                       |
| -- | ----------------------- | -------------------------------------------- | -------------------------------------------------------------------------- |
| ① | 端口绑网桥网关          | `"172.21.0.1:18001:8000"`，target 用网关地址 | ✖ 暂缓：需重建网络，vLLM 中断 1-2 分钟（留作升级路径）                    |
| ② | prometheus 改 host 网络 | target 全写`127.0.0.1`                       | ✖ 否决：exporter 需逐个加端口映射，且失去网络隔离                         |
| ③ | docker_sd 自动发现      | 挂 docker.sock +`docker_sd_configs`          | ✖ 否决：需改运行用户，挂 docker.sock ≈ root 权限                         |
| ④ | host.docker.internal    | `extra_hosts: ...:host-gateway`              | ✖ 否决：Linux 上解析到 docker0 而非 vllm 网桥，还需改端口绑定，无额外收益 |
| ⑤ | 固定容器 IP             | `172.21.0.2:8000` 容器内直连                 | ✔ 采纳（现状）                                                            |

两个重点方案的补充说明：

- **① 端口绑网桥网关**：每个 bridge 网络在宿主有一块网卡，网关地址即宿主在该网络的地址（`vllm_default` → `172.21.0.1`）。端口发到该地址后，只有「接入该网络的容器 + 宿主」可达，外部进不来；地址由 subnet 推导，固定 subnet 即固定网关，容器重建不漂移——比固定容器 IP 更鲁棒。暂缓原因：改 `ipam` 需重建网络 → vLLM `down`/`up` 加载模型约 1-2 分钟。
- **⑤ 固定容器 IP（采纳）**：容器网络内直连，零 DNS 依赖、无额外端口暴露。IP 写在 `x-vllm-common` 锚点，所有 profile 共用同一 IP，切模型时 target 无需改动。

**固化改动**（否则重启即复现）：

```text
infer/vllm/docker-compose.yml  服务加 ipv4_address: 172.21.0.2
                              顶层 networks.default 固定 subnet 172.21.0.0/16
                              （端口保持 127.0.0.1:18000:8000 不变）
obs/docker-compose.yml         prometheus 显式挂 default + vllm_default(external)
obs/prometheus.yml             target: 172.18.0.1:18000 → 172.21.0.2:8000
```

- **网络归属必须写进 compose**：只靠 `docker network connect` 手连，prometheus 一重建就丢、故障重现。
- **启动顺序**：`vllm_default` 是 external 网络，若 vLLM 全部 profile `down` 导致网络被删，obs 栈重建报「network not found」——先起 vLLM 再起 obs。
- **教训**：改「服务对外端口绑定」时，要连带检查所有经宿主端口消费它的内部依赖（监控、网关、前端）。本次只顾收紧安全面，漏了 Prometheus 这条消费方。

---

## 📈  vLLM 指标接入综合面板 260827

**GPU 告警规则优化（消除告警疲劳）**

```text
原规则                                 → 处置
GPU显存占用>90%（warning）               ✖ 删除：vLLM(--gpu-memory-utilization=0.80)常驻下 90%+ 是常态，曾连续 fire 24h
GPU利用率过低（<10% for 1h）            ✖ 删除：vLLM 空闲时利用率本就不高
GPU温度>85℃                            → 88℃(warning, for 10m) / 92℃(critical, for 5m)
GPU 可用显存<1GB（critical）            ✖ 删除：0.90 利用率 + 挖矿 ~1.4GB/卡 下 free 常态 ~290MiB，必然误报（260828）
```

- 教训：阈值定在"常态线"上才会告警疲劳——设计规则先问"什么状态才真正异常"，而不是"什么值看起来高"。vLLM 场景下显存占用高、温度高都是稳态，不是故障。当前 3 条 GPU 规则 0 firing。

**vLLM 指标接入**

```text
prometheus.yml 新增 job: vllm
  scrape_interval 15s, metrics_path /metrics
  targets: ["172.18.0.1:18000"]   ← docker 网关 IP，零依赖
  metric_relabel_configs: "^vllm:(.+)$" → "vllm_$1"（冒号指标名转下划线）
```

- **选型：抓取地址用 docker 网关 `172.18.0.1` 而非 `host.docker.internal`**——后者需给 prometheus 容器加 `extra_hosts` 并重建，前者零依赖（vLLM 端口已映射到宿主机），直接可用。
- vLLM 自带指标名含冒号（`vllm:engine_sleep_state` 等），Prometheus 常规指标名不允许冒号，用 `metric_relabel_configs` 重写为 `vllm_*`。
- 5 条 vLLM 告警：收到请求通知（`vllm_num_requests_running + vllm_num_requests_waiting > 0`，info 0m——低用量下当使用记录，260828 新增）、引擎休眠（`weights_offloaded`/`discard_all`，critical 2m）、请求积压（`vllm_num_requests_waiting>1`，2m——max-num-seqs=4 下排队>1 即容量饱和，原 >20 太钝，260828 收紧）、请求错误（`increase(vllm_request_success_total{finished_reason="error"}[5m])>0`，1m）、KV cache 紧张（`vllm_kv_cache_usage_perc>95`，10m）。

**Grafana 综合面板（uid `obs-vllm-gpu`）**

- provisioning 热更新：`datasources/prometheus.yml` + `dashboards.yml`，面板 JSON 放 `obs/dashboards/`，改动 **30s 内自动加载**，无需重启。
- **面板目录策略**：provider `foldersFromFilesStructure: false`、不建 Obs 文件夹，面板直接进 General——用户侧好找好维护。
- 布局迭代：顶部瞬时卡片区 → 样式按指标类型差异化（stat 背景色块 / 面积图 / 柱状图 / 扇形图）；扇形图独立成行、给足宽度；下方趋势图按 3+2 排布压缩高度（曾压缩 4 行卡片 → 3 行，总高 66 → 46）。
- **gauge 扇形图美观三要素**（对照 Grafana 自带 Node Exporter Full / DCGM 面板）：
  1. `showThresholdLabels: false`——窄卡里刻度标签数字挤在弧线上是视觉元凶，保留 `showThresholdMarkers`
  2. `sizing: "auto"`——Grafana v12 已弃用 `text.titleSize/valueSize` 旧字段
  3. 宽度给足：扇形图独立成行，不塞 1/4 行宽窄卡
- 展示取舍：删除请求错误率、GPU 编解码利用率、运行进程数三个低价值面板，兼顾信息密度与可读性。
- 网络卡：速率 `rate()` → 累计总量 `sum(node_network_{receive,transmit}_bytes_total{device!~"lo|veth.*|br-.*|docker.*|tun.*"})`，单位 `decbytes` 自动换算 TB，`graphMode: area`；GPU 温度卡同样改扇形图，四个扇形图（负载/显存/温度/vLLM 运行）一行排列。
- 主机运行卡挪入卡片区第 2 行后，原位置由 vLLM 排队卡补位——保持每行 4 张的紧凑布局。

---

## 🔔 通过钉钉接收告警通知 260826

```text
prometheus(规则) → alertmanager(路由 group_by: alertname,job)
   → webhook-dingtalk 桥接(host 网络, 端口 8060)
   → 钉钉群机器人(markdown, 加签)
```

- 路由按 `alertname + job` 分组，`group_wait 30s` / `repeat_interval 4h`——同一告警 4 小时内不重复轰炸
- 消息文案由 `template/default.tmpl` 控制：title（`obs-alert.title`：监控告警/告警恢复）+ text（`obs-alert.message`），有 title 即按 markdown 发送

**展示格式定位**：不在告警规则里，而在钉钉桥接服务的消息模板 `obs/template/default.tmpl`：

```go
// 最终态（主题与详细分行）
**{{ .Labels.alertname }}**
{{ .Annotations.summary }}
```

**踩坑：模板目录只读挂载，改完必须重启才生效**。`docker-compose.yml` 中 `./template:/etc/.../template:ro`——文件改动实时同步进容器，但 prometheus-webhook-dingtalk 在**启动时**加载模板，不重启不会重新解析：

```bash
docker restart webhook-dingtalk
```

**验证认知**：钉钉 markdown 单换行即可正常换行显示，无需 `<br/>` 或空行。

---

## 🐳 监控服务迁移 260825

> 监控栈从原 monitoring 项目迁入 edge 仓库统一管理——编排、规则、模板随仓库走，敏感凭据不入库；数据卷复用保证历史指标不丢。

参考现在目录的简短命名风格，按推荐度排序：
obs	observability 缩写，最贴合现代术语、最短	推荐，未来可能扩展日志/链路
telemetry 强调「采集」—exporter 主动上报指标
metrics	直白表达「指标」	如果确定只做指标不做日志
prom-stack	体现技术栈	想突出 Prometheus/Grafana 时
prom-grafana	同上但更啰嗦	不推荐
observe 语义没问题，但有点被动感，且易与 observer/observation 混淆
