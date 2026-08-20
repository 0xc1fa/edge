#!/bin/sh
set -e

# 关键修复：dockerd 官方脚本内部用 docker-init(tini) 回收 containerd 僵尸进程。
# 本脚本将其放入后台后 tini 不再是 PID1，必须注册 subreaper，否则
# containerd 启动会 15s 超时（docker-library/docker#318）。
export TINI_SUBREAPER=1

# 配置 nvidia-container-toolkit runtime（生成 /etc/docker/daemon.json）
# 失败不阻断：GPU 不可用时环境仍可用（设备直通模式）
if command -v nvidia-ctk >/dev/null 2>&1; then
    echo "[entrypoint] 配置 nvidia runtime..."
    nvidia-ctk runtime configure --runtime=docker --config=/etc/docker/daemon.json \
        || echo "[entrypoint] 警告: nvidia runtime 配置失败，GPU 功能不可用"
else
    echo "[entrypoint] 警告: 未找到 nvidia-ctk，跳过 nvidia runtime 配置"
fi

dockerd-entrypoint.sh &
DOCKERD_PID=$!

echo "[entrypoint] 等待 dockerd 启动..."
for i in $(seq 1 60); do
    if docker info >/dev/null 2>&1; then
        echo "[entrypoint] dockerd 已就绪"
        break
    fi
    sleep 1
done

if ! docker info >/dev/null 2>&1; then
    echo "[entrypoint] 错误: dockerd 启动超时"
    exit 1
fi

# dind 内无外网，agent 镜像由 Dockerfile 构建时 COPY 进镜像（/agent.tar）
if [ -f /agent.tar ]; then
    echo "[entrypoint] 导入 portainer-agent 镜像..."
    docker load -i /agent.tar
fi

if ! docker image inspect portainer/agent:lts >/dev/null 2>&1; then
    echo "[entrypoint] 错误: portainer/agent:lts 镜像缺失，请检查挂载的 tar"
    exit 1
fi

docker rm -f portainer-agent 2>/dev/null || true

echo "[entrypoint] 启动 portainer-agent..."
docker run -d \
    --name portainer-agent \
    --restart=always \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v /var/lib/docker/volumes:/var/lib/docker/volumes \
    -p 9001:9001 \
    portainer/agent:lts

echo "[entrypoint] portainer-agent 已启动，监听 9001 端口"

wait $DOCKERD_PID
