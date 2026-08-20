#!/bin/sh
set -e

# PF_RULES 格式：监听端口:目标主机:目标端口，多条用逗号分隔
#   示例: "8088:dind-platform:8080,8090:dind-platform:3306"
: "${PF_RULES:?PF_RULES 环境变量未设置，示例: \"8088:dind-platform:8080\"}"

echo "[port-forward] 解析规则: ${PF_RULES}"

start_rule() {
    listen="$1"; host="$2"; port="$3"
    echo "[port-forward] 启动 0.0.0.0:${listen} -> ${host}:${port}"
    while :; do
        socat TCP-LISTEN:${listen},fork,reuseaddr TCP:${host}:${port}
        echo "[port-forward] ${listen} 转发进程退出，2s 后重启"
        sleep 2
    done
}

# 每条规则一个独立进程，崩溃自动拉起；主进程 wait 保活
IFS=','
for rule in $PF_RULES; do
    [ -z "$rule" ] && continue
    l=$(echo "$rule" | cut -d: -f1)
    h=$(echo "$rule" | cut -d: -f2)
    p=$(echo "$rule" | cut -d: -f3)
    start_rule "$l" "$h" "$p" &
done

echo "[port-forward] 所有规则已启动"
wait
