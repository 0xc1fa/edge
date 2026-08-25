#!/usr/bin/env bash

set -euo pipefail

# 基于脚本自身位置定位 compose 和 env，避免监控目录调整后失效。
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yml"
ENV_FILE="${SCRIPT_DIR}/monitoring.env"

if [[ -f "${ENV_FILE}" ]]; then
  # 使用 set -a 让 source 进来的变量自动 export 为环境变量，确保 docker compose 能继承。
  set -a
  source "${ENV_FILE}"
  set +a
fi

# 启动监控栈后直接输出访问地址，便于快速确认页面入口。
# 不强制重建容器：仅配置变更后 compose 自动按需重建。
docker compose -f "${COMPOSE_FILE}" up -d

echo
echo "Prometheus 已启动: http://127.0.0.1:${PROMETHEUS_PORT:-9090}"
echo "Grafana 已启动: http://127.0.0.1:${GRAFANA_PORT:-3000}"
echo "Alertmanager UI: http://127.0.0.1:${ALERTMANAGER_PORT:-9093}"
