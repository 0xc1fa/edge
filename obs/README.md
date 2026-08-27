# Obs 可观测性系统

本目录负责宿主机、容器和 GPU 的资源监控，当前栈为 `Prometheus + Grafana + Alertmanager + node-exporter + cAdvisor + dcgm-exporter`。

## 文件说明

- `docker-compose.yml`：监控系统主编排文件。
- `.env`：端口与 Grafana 管理员配置（compose 自动读取）。
- `prometheus.yml`：Prometheus 抓取目标与 Alertmanager 配置。
- `alert_rules.yml`：Prometheus 告警规则（GPU + vLLM 组）。
- `alertmanager.yml`：告警路由与通知（webhook）配置。
- `grafana-datasource.yml`：Grafana 自动数据源配置（uid `PBFA97CFB590B2093`，指向 `prometheus:9090`）。
- `dashboards.yml` + `dashboards/`：Grafana 面板 provisioning，改动 **30s 内自动热加载**，无需重启。
- `template/`：钉钉告警消息模板（`default.tmpl`），只读挂载，**改后需 `docker restart webhook-dingtalk`** 才生效。

当前 `dcgm-exporter` 默认使用 Docker Hub 上的 `nvidia/dcgm-exporter` 公共镜像，以避免 `nvcr.io` 在未登录或受限网络场景下返回 `403 Forbidden`。

> 容器直接以服务名命名（`prometheus`、`grafana`、`alertmanager` 等），不设前缀。
> exporter（node-exporter/cadvisor/dcgm-exporter）端口不映射宿主机，仅由 Prometheus 经 Docker 网络内访问。

## 启动方式

```bash
cd /root/edge/obs && docker compose up -d
```

> compose 自动读取同目录 `.env`（端口与 Grafana 管理员配置），无需 `--env-file` 或 source 脚本。

## 访问地址

- Prometheus：`http://127.0.0.1:9090`
- Grafana：`http://127.0.0.1:3000`
- Alertmanager：`http://127.0.0.1:9093`（仅本机可访问）

实际端口以 `.env` 中配置为准。

Grafana 默认账号密码也直接来自 `.env`。

## 配置方式

直接维护 `.env`（不入库，见根 `.gitignore`）。

> 注意：本目录复用了原 `monitoring` 项目的数据卷（`monitoring_prometheus_data`、`monitoring_grafana_data`），迁移后历史指标与 Grafana 配置原样保留。

当前默认采集目标：

- `prometheus:9090`
- `node-exporter:9100`
- `cadvisor:8080`
- `dcgm-exporter:9400`
- `vllm`：`172.18.0.1:18000`（docker 网关 IP，vLLM 端口已映射宿主机；指标名经 `metric_relabel_configs` 将 `vllm:` 冒号前缀改写为 `vllm_`）

## 告警范围

**基础（alert 组）**：主机 CPU / 内存 / 磁盘使用率、采集目标掉线、容器资源。

**GPU（obs-gpu-alerts 组）**：

- GPU 温度 > 88℃（warning，10m）/< 92℃（critical，5m）——90% 常态占用下温度高不是故障，阈值定在降频线
- GPU 可用显存 < 1GB（critical，5m）——显存占用率高是常态，只有"耗尽"才告警

**vLLM（obs-vllm-alerts 组）**：

- 引擎休眠（`weights_offloaded` / `discard_all`，critical，2m）
- 请求积压：`vllm_num_requests_waiting > 20`（5m）
- 请求错误：`increase(vllm_request_success_total{finished_reason="error"}[5m]) > 0`（1m）
- KV cache 紧张：`vllm_kv_cache_usage_perc > 95`（10m）

## Grafana 面板

综合监控面板（含瞬时卡片 + vLLM/GPU 趋势图）已接入 provisioning，uid `obs-overview`，文件 `dashboards/overview.json`，面板归 General 。修改 JSON 后 30s 内自动生效。

面板命名规范：`<主题>.json` + `uid: obs-<主题>`（如 `overview.json` = 综合总览；后续 vLLM 应用专题用 `vllm.json` / uid `obs-vllm`）。
