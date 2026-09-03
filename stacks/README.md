# Portainer + DinD 隔离环境

宿主 Docker 的管理平面 + 隔离测试环境，单 compose 一键启动。

## 架构

```
宿主 Docker（第1层）
 ├─ portainer       管理界面 :9443（不挂宿主 socket）
 ├─ registry-proxy  :6000 Docker Hub 代理缓存（dind 拉公共镜像唯一通道）
 ├─ dind-platform   第2层 dockerd v28.5.2 + GPU 卡0 穿透（Portainer 唯一环境）
 ├─ port-forward    外部端口 → dind 内端口（socat 转发，不动 dind）
 └─ auto-register   自动把 dind 注册进 Portainer（幂等）

镜像供给：Docker Hub ──▶ registry-proxy(6000) ──▶ dind 自助拉取
私有镜像：10.8.0.8:5000（infra-registry，可直接 push）
```

## 镜像供给（dind 无外网）


| 场景       | 写法                                                    |
| ---------- | ------------------------------------------------------- |
| 官方镜像   | 短名`nginx:alpine`（自动走 mirror）                     |
| 带前缀镜像 | `10.8.0.8:6000/library/bitnami/xx`（mirror 不覆盖前缀） |
| 私有镜像   | `10.8.0.8:5000/xxx`                                     |

- 两层缓存：dind 本地镜像库（`/var/lib/docker`）+ proxy 卷（`registry-proxy-data`）——删容器不清镜像、"缓存删了还快"是命中了另一层
- 验证真回源：清 `registry-proxy-data` 卷后重新拉起 registry-proxy

## 端口转发（port-forward）

- 规则格式 `PF_RULES=监听端口:目标主机:目标端口`，目标主机用 compose 网络名（如 `dind-platform`），不依赖容器 IP
- 改规则后 `docker compose up -d port-forward`——**recreate 才生效**，`restart` 用旧配置不生效
- 自愈双保险：entrypoint 每条规则 while 循环（进程级）+ `restart: always`（容器级）
- 只改 `PF_RULES` 不碰 dind；`up -d --build` 涉及镜像重建时会连带 recreate dind-platform

## GPU 与资源

- **GPU**：整卡透传卡0（4090 不可切分），需卡1 改 `/dev/nvidia1`
- **toolkit**：dind 内 nvidia-toolkit = 宿主只读挂载方案（离线、不装包）；宿主升级驱动/toolkit 只需重启 dind，无需重建镜像
- **资源**：实际配额 16 核 / 32G 由 cgroup 强制（`cpu.max`/`memory.max`）；Portainer 界面显示 32 核 / 134.9 GB 是宿主全量（dockerd 读 `/proc`），非配置错误

## 账号与授权

- admin 密码 `Root@edge202608`（当前生效，260903 统一，`portainer_admin_password.txt` 已同步），首次启动经 `--admin-password-file` 自动初始化，绕过 UI 超时锁定
- 改密接口强制 12 位 + 大小写+数字+符号（硬编码，无法关闭）
- test 用户（Environment Admin）已授权 dind-platform 环境：可穿 GPU、管 stack；平台层不可触（Settings/用户/其他环境）

## 相关归档

决策与踩坑记录见 [note-docker.md](../docs/note-docker.md)

启动、访问、常用操作见根目录 [README](../../README.md#容器服务)
