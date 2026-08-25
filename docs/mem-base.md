# 网络

- **策略路由服务**（Tailscale 旁路，systemd oneshot 持久化）：

  - `ip rule add to 100.64.0.0/10 lookup main priority 8990`
  - `ip rule add from 172.16.0.0/12 lookup main priority 8999`

# 端口


| 端口             | 服务                          | 说明                       |
| ---------------- | ----------------------------- | -------------------------- |
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
