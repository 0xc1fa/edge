# obs 监控

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
