# dind-platform 定制镜像：官方 docker:dind + 预置 portainer-agent 镜像
#
# 构建前需先在宿主上准备 agent 镜像包（宿主已配置镜像加速，dind 内无外网拉不到）：
#   docker save portainer/agent:lts -o agent.tar
#
# nvidia-container-toolkit 说明：
#   - Alpine 官方仓库无该包、NVIDIA 官方仓库不支持 alpine，apk 装不了
#   - 采用「宿主挂载」方案：宿主已装 toolkit 1.19.1 + 驱动库，
#     由 docker-compose 将二进制、glibc、驱动库、config 挂载进 dind（见 compose 注释）
#   - 镜像本身保持纯净，宿主升级驱动/toolkit 无需重建镜像
FROM docker:28-dind

COPY agent.tar /agent.tar
# 预置 daemon.json：insecure-registries 信任内网 registry(5000/6000) + nvidia runtime
# nvidia-ctk runtime configure 会 merge 保留 insecure-registries 字段
COPY daemon.json /etc/docker/daemon.json
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
