#!/bin/sh
set -e

PORTAINER_URL="${PORTAINER_URL:-https://portainer:9443}"
PORTAINER_PASSWORD_FILE="${PORTAINER_PASSWORD_FILE:-/run/secrets/portainer_admin_password}"
AGENT_ENDPOINT="${AGENT_ENDPOINT:-dind-platform:9001}"
AGENT_NAME="${AGENT_NAME:-dind-platform}"

echo "[auto-register] 等待 Portainer API 就绪..."
API_READY=0
for i in $(seq 1 60); do
    if curl -sk "${PORTAINER_URL}/api/status" >/dev/null 2>&1; then
        API_READY=1
        echo "[auto-register] Portainer API 已就绪"
        break
    fi
    sleep 2
done
if [ "$API_READY" != "1" ]; then
    echo "[auto-register] 错误: Portainer API 未就绪"
    exit 1
fi

# 去除文件末尾换行，避免拼进 JSON 导致无效
ADMIN_PASSWORD=$(tr -d '\n\r' < "${PORTAINER_PASSWORD_FILE}" 2>/dev/null || echo "")
if [ -z "$ADMIN_PASSWORD" ]; then
    echo "[auto-register] 错误: 无法读取管理员密码"
    exit 1
fi

echo "[auto-register] 登录 Portainer..."
JWT=""
LOGIN_RESP=""
for i in $(seq 1 10); do
    LOGIN_RESP=$(curl -sk -X POST "${PORTAINER_URL}/api/auth" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}")
    JWT=$(echo "$LOGIN_RESP" | grep -o '"jwt":"[^"]*"' | head -1 | sed 's/"jwt":"//;s/"//')
    [ -n "$JWT" ] && break
    echo "[auto-register] 登录未成功（第 ${i} 次），2s 后重试..."
    sleep 2
done
if [ -z "$JWT" ]; then
    echo "[auto-register] 登录失败: ${LOGIN_RESP}"
    exit 1
fi
echo "[auto-register] 登录成功"

ENDPOINTS_RESP=$(curl -sk -X GET "${PORTAINER_URL}/api/endpoints" \
    -H "Authorization: Bearer ${JWT}")

# 已存在同名端点则跳过（幂等；Portainer JSON 中 Id 在 Name 之前，不能用 Name 后跟 Id 的正则）
if echo "$ENDPOINTS_RESP" | grep -q "\"Name\":\"${AGENT_NAME}\""; then
    echo "[auto-register] 端点 '${AGENT_NAME}' 已存在，跳过注册"
    exit 0
fi

echo "[auto-register] 注册 Agent 端点 '${AGENT_NAME}' (tcp://${AGENT_ENDPOINT})..."
# 关键约束（Portainer 2.39 API）：
#   1. 必须是 multipart/form-data，不能用 JSON（否则 Name 无法提取）
#   2. EndpointCreationType=2 为 Agent 环境
#   3. URL 用 tcp:// 前缀
#   4. agent 默认监听 HTTPS(9001)，必须 TLS=true + SkipVerify，否则握手失败
CREATE_RESP=$(curl -sk -X POST "${PORTAINER_URL}/api/endpoints" \
    -H "Authorization: Bearer ${JWT}" \
    -F "Name=${AGENT_NAME}" \
    -F "EndpointCreationType=2" \
    -F "URL=tcp://${AGENT_ENDPOINT}" \
    -F "TLS=true" \
    -F "TLSSkipVerify=true" \
    -F "TLSSkipClientVerify=true" \
    -F "GroupID=1")

ENDPOINT_ID=$(echo "$CREATE_RESP" | grep -o '"Id":[0-9]*' | head -1 | sed 's/"Id"://')
if [ -n "$ENDPOINT_ID" ]; then
    echo "[auto-register] 注册成功! 端点 ID: ${ENDPOINT_ID}"
else
    echo "[auto-register] 注册失败: ${CREATE_RESP}"
    exit 1
fi
