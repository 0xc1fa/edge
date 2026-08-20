#!/bin/sh
# 给普通用户授权 dind-platform 环境访问（Standard User，幂等）
# 用法: sh grant-access.sh [用户名]   （默认 test）
# 说明: 只调 Portainer API，不改 compose / 不碰容器
set -e

PORTAINER_URL="${PORTAINER_URL:-https://127.0.0.1:9443}"
PASSWORD_FILE="${PASSWORD_FILE:-/root/edge/portainer/portainer_admin_password.txt}"
TARGET_USER="${1:-test}"
ENDPOINT_NAME="${ENDPOINT_NAME:-dind-platform}"
ROLE_ID="${ROLE_ID:-1}"   # 1=Environment Admin, 2=Standard User（可用 ROLE_ID=2 覆盖）

ADMIN_PASSWORD=$(tr -d '\n\r' < "$PASSWORD_FILE" 2>/dev/null || echo "")
if [ -z "$ADMIN_PASSWORD" ]; then
    echo "[grant] 错误: 无法读取 admin 密码 ($PASSWORD_FILE)"
    exit 1
fi

echo "[grant] 登录 Portainer ($PORTAINER_URL) ..."
JWT=""
LOGIN_RESP=""
for i in $(seq 1 10); do
    LOGIN_RESP=$(curl -sk -X POST "${PORTAINER_URL}/api/auth" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}")
    JWT=$(echo "$LOGIN_RESP" | grep -o '"jwt":"[^"]*"' | head -1 | sed 's/"jwt":"//;s/"//')
    [ -n "$JWT" ] && break
    echo "[grant] 登录未成功（第 ${i} 次），2s 后重试..."
    sleep 2
done
if [ -z "$JWT" ]; then
    echo "[grant] 登录失败: ${LOGIN_RESP}"
    exit 1
fi
echo "[grant] 登录成功"

echo "[grant] 查询用户 '$TARGET_USER' ..."
USERS_RESP=$(curl -sk -X GET "${PORTAINER_URL}/api/users" \
    -H "Authorization: Bearer ${JWT}")
USER_ID=$(echo "$USERS_RESP" | grep -o "\"Id\":[0-9]*,\"Username\":\"${TARGET_USER}\"" | head -1 | sed 's/.*"Id"://;s/,.*//')
if [ -z "$USER_ID" ]; then
    echo "[grant] 错误: 用户 '$TARGET_USER' 不存在！现有用户:"
    echo "$USERS_RESP" | grep -o '"Username":"[^"]*"' | sed 's/"Username":"//;s/"//'
    exit 1
fi
echo "[grant]   用户存在: '$TARGET_USER' Id=${USER_ID}"

USER_OBJ=$(echo "$USERS_RESP" | grep -o "{\"Id\":${USER_ID},[^}]*" | head -1)
USER_ROLE=$(echo "$USER_OBJ" | grep -o '"Role":[0-9]*' | head -1 | sed 's/"Role"://')
echo "[grant]   用户角色 Role=${USER_ROLE:-未知}（1=管理员, 2=标准用户）"

echo "[grant] 查询环境 '$ENDPOINT_NAME' ..."
EP_RESP=$(curl -sk -X GET "${PORTAINER_URL}/api/endpoints" \
    -H "Authorization: Bearer ${JWT}")
EP_ID=$(echo "$EP_RESP" | grep -o "\"Id\":[0-9]*,\"Name\":\"${ENDPOINT_NAME}\"" | head -1 | sed 's/.*"Id"://;s/,.*//')
if [ -z "$EP_ID" ]; then
    echo "[grant] 错误: 环境 '$ENDPOINT_NAME' 不存在！现有环境:"
    echo "$EP_RESP" | grep -o '"Name":"[^"]*"' | sed 's/"Name":"//;s/"//'
    exit 1
fi
echo "[grant]   环境存在: '$ENDPOINT_NAME' Id=${EP_ID}"

echo "[grant] 授权 '$TARGET_USER'(Id=${USER_ID}) → '$ENDPOINT_NAME'(Id=${EP_ID}) 角色=Standard User ..."
GRANT_RESP=$(curl -sk -X PUT \
    -H "Authorization: Bearer ${JWT}" \
    -H "Content-Type: application/json" \
    -d "{\"UserAccessPolicies\":{\"${USER_ID}\":{\"RoleId\":${ROLE_ID}}}}" \
    "${PORTAINER_URL}/api/endpoints/${EP_ID}")
echo "[grant]   授权返回: ${GRANT_RESP:-（空 = 成功）}"

echo "[grant] 验证环境配置 ..."
EP_VERIFY=$(curl -sk -X GET "${PORTAINER_URL}/api/endpoints/${EP_ID}" \
    -H "Authorization: Bearer ${JWT}")
echo "[grant]   环境 Name: $(echo "$EP_VERIFY" | grep -o '"Name":"[^"]*"' | head -1 | sed 's/"Name":"//;s/"//')"
echo "[grant]   环境 URL:  $(echo "$EP_VERIFY" | grep -o '"URL":"[^"]*"' | head -1 | sed 's/"URL":"//;s/"//')"
echo "[grant]   UserAccessPolicies: $(echo "$EP_VERIFY" | grep -o '"UserAccessPolicies":{[^}]*}' | head -1 | sed 's/"UserAccessPolicies"://')"
echo "[grant] 完成。请让 '$TARGET_USER' 退出重新登录，Home 页应能看到 '$ENDPOINT_NAME'。"
