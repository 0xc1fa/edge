# 网络

- **策略路由服务**（Tailscale 旁路，systemd oneshot 持久化）：

  - `ip rule add to 100.64.0.0/10 lookup main priority 8990`
  - `ip rule add from 172.16.0.0/12 lookup main priority 8999`

# 端口


| 端口             | 服务                          | 说明                       |
| ---------------- | ----------------------------- | -------------------------- |
| 80               | caddy-gateway（docker）       | 统一入口，路径分发，勿占用 |
| 22               | sshd                          | 远程登录                   |
| 7890/7891/7892   | mihomo 代理                   | 127.0.0.1，HTTP/SOCKS      |
| 9999             | mihomo external-controller    | REST API，secret`U5c1dG4n` |
| 198.18.0.1:37021 | mihomo TUN                    | fake-ip 网段网关           |
| 38324/14122      | mihomo-party GUI              | 127.0.0.1 本地管理         |
| 45351            | tailscaled                    | 100.69.186.2               |
| 19091            | agent-tool-host               | IDE 助手                   |
| 9090             | infra-prometheus（docker）    | 勿占用                     |
| 3000             | infra-grafana（docker）       | 监控面板                   |
| 3100/2222        | infra-gitea（docker）         | HTTP / SSH                 |
| 5000/5100        | infra-registry / UI（docker） | 镜像仓库                   |
| 9400             | infra-dcgm-exporter（docker） | GPU 指标                   |
| 8082             | infra-cadvisor（docker）      | 容器指标                   |
| 9443             | portainer（docker）           | 容器管理                   |
| 3306/5432/5433   | port-forward 容器（docker）   | MySQL/Postgres 转发        |
| 8001-8004        | port-forward 容器（docker）   | 转发                       |
| 18000            | vllm-qwen36（docker）         | 容器内 8000                |
| 5901             | solar-server（docker）        | 容器内 5001                |

# 导航

> 网关同时监听 `:80`（内网直连）与 `:8080`（公网 2029→8080 映射），规则完全一致。


| 路径        | 后端                       | 说明                             |
| ----------- | -------------------------- | -------------------------------- |
| `/`         | 静态首页（www/index.html） | 服务导航，hao123 风格            |
| `/gitea`    | 127.0.0.1:3100             | 剥前缀转发（Gitea 挂根路径）     |
| `/grafana`  | 127.0.0.1:3000             | 保前缀（serve_from_sub_path）    |
| `/prom`     | 127.0.0.1:9090             | 保前缀（route-prefix=/prom）     |
| `/registry` | 127.0.0.1:5100             | 剥前缀转发（nginx 相对路径）     |
| `/solar`    | 127.0.0.1:5901             | 保前缀                           |
| `/webui`    | 127.0.0.1:8081             | 保前缀（Open WebUI 已迁至 8081） |
| `/api/*`    | 127.0.0.1:18080            | new-api API（页面独立端口）      |

# 运维

- 网关容器：`cd /root/edge/base && docker compose up -d`
- 改 Caddyfile 后重载：`docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile`
- 改首页：编辑 `www/index.html`，纯静态即时生效
