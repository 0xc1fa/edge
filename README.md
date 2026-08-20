# Edge


## Portainer

```
宿主 Docker（第1层）
 ├─ portainer        管理界面 https://10.8.0.8:9443（admin/test 账号）
 ├─ registry-proxy   10.8.0.8:6000 Docker Hub 代理缓存（dind 公共镜像自助通道）
 ├─ dind-platform    第2层 dockerd，test 唯一的隔离环境
 └─ port-forward     外部端口 → dind 内端口（当前 8091/5433）

test 自助部署：Docker Hub ─▶ 6000 缓存 ─▶ dind ─▶ 应用（adminer/postgres）
```


```bash
# 一键启动/更新整套环境
cd /root/edge/portainer && docker compose up -d --build

# 查看各服务状态
docker compose ps
```
