# Obs 可观测性系统

本目录负责宿主机、容器和 GPU 的资源监控，当前栈为 `Prometheus + Grafana + Alertmanager + node-exporter + cAdvisor + dcgm-exporter`。

## 文件说明

- `docker-compose.yml`：监控系统主编排文件。
- `.env`：端口与 Grafana 管理员配置（compose 自动读取）。
- `prometheus.yml`：Prometheus 抓取目标与 Alertmanager 配置。
- `alert_rules.yml`：Prometheus 告警规则。
- `alertmanager.yml`：告警路由与通知（webhook）配置。
- `grafana-datasource.yml`：Grafana 自动数据源配置。

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

## 告警范围

当前规则已覆盖：

- 主机 CPU、内存、磁盘使用率
- 容器 CPU、内存使用率
- 采集目标掉线
- GPU 温度与显存占用
