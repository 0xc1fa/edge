# Demo App — test 用户自助部署示例

**目的**：演示 test 用户在 Portainer 中如何自助部署一个 `web + db` 两层应用，全程无需管理员介入。

- 数据库管理页面（Adminer） + 数据库（PostgreSQL）
- 镜像走 dind 的 `registry-mirrors`，短名即可（`adminer:4` / `postgres:16-alpine`）
- 包含外部端口转发和命名卷（数据持久化）

## 一键部署（test 用户操作）

![test 账号在 Portainer 中创建 stack](stack-create-screenshot.png)

> 截图说明：test 账号登录后，环境选择 `dind-platform`，进入 **Stacks → Add stack**，Name 填 `demo-app`，Build method 选 **Web editor**，粘贴 `examples/demo-app/docker-compose.yml` 全部内容。

页面操作步骤：

1. 登录 `https://10.8.0.8:9443`（test 账户）
2. 顶部环境确认选择 **dind-platform**（左栏应显示该环境）
3. 左侧 **Stacks** → 右上角 **+ Add stack**
4. Name 填 `demo-app`，Build method 选 **Web editor**
5. 将 `examples/demo-app/docker-compose.yml` 全部内容粘贴到编辑框
6. 滚动到底部，点 **Deploy the stack**
7. 等待 5-10 秒（首次拉取会经内网 proxy-cache 缓存秒回）

## 部署后访问验证

| 入口 | 地址 | 凭据 |
| --- | --- | --- |
| Adminer 管理页面 | `http://10.8.0.8:8091` | 系统 `PostgreSQL` / 服务器 `db` / 用户 `app` / 密码 `app123` / 数据库 `appdb` |
| PostgreSQL 直连 | `10.8.0.8:5433` | 用户 `app` / 密码 `app123` / 库 `appdb` |

> **可选凭据自动填充**：因为 compose 中 adminer 配置了 `ADMINER_DEFAULT_SERVER=db`，浏览器打开后"服务器"字段已预填 `db`，只需要填其他字段即可登录。

## 持久化验证

1. 在 Adminer 中建一张表（如 `CREATE TABLE t (id int, name text);`），插入几行
2. 在 Portainer **Stacks** 中点 `demo-app` → **Delete the stack**（容器和默认网络会被删除）
3. 重新执行本文"一键部署"步骤
4. 再次打开 Adminer，表和数据应仍然存在（命名卷 `db-data` 保留）

## 故障排查

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 拉取镜像时长时间无响应 | dind 首次拉取新镜像，正在回源 Docker Hub | 等待数十秒（受宿主代理带宽影响） |
| 端口 8091/5433 访问不到 | port-forward 容器未启动 | 通知管理员执行 `docker compose up -d port-forward` |
| Adminer 登录后报 "Connection refused" | db 容器未就绪 | 刷新页面重试，或在 Portainer `Containers` 确认 `demo-db` 状态为 `running` |
| 页面 Containers 为空但镜像删除报错 | Portainer UI 缓存未刷新 | 浏览器 F5 刷新，或确认顶部环境在 `dind-platform` |
