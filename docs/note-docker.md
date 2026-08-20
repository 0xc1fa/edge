# 容器化 多租户 资源隔离

## 📦 自助运行 compose 数据库管理服务 260820

```
test 要用新镜像建应用，dind 内 docker pull 直接 TCP 悬挂超时
    │
    │  ← 用户问"dind 为什么没有外网 如果其他成员使用 test 用户建立自己的应用是什么流程"
    ▼
分层排查：宿主 curl registry 通 / dind ping IP 通 / DNS 解析返回 198.18.0.170
    │
    │  ← 198.18.0.170 是 fake-ip（透明代理特征），容器流量不在代理规则内
    ▼
根因：宿主透明代理只对宿主本机进程（OUTPUT 链）生效，
      dind 容器流量走 FORWARD 链被悬挂 → dind 永远无法直连外网
    │
    │  ← 用户提出"解决 Test 用户自助进行镜像管理"
    ▼
方案A：宿主 pull→save→load 离线导入（每个新镜像都要管理员介入）✗ 不自助
方案B：双 registry：5000 私有仓库不动 + 新增 6000 proxy-cache（Docker Hub 代理）✓
    │
    │  ← 用户问"devops 已有5000 端口的 registry-proxy 是否有必要再单独开一个"
    ▼
纠正前提：5000 是纯私有仓库（solar-server，无 proxy 配置），不是 proxy；
registry:2 私有存储与 proxy 模式互斥（proxy 模式官方禁 push）→ 必须两个实例
    │
    ▼
6000 proxy-cache 落地：host 网络（容器网桥出网受限）+ dind insecure-registries 信任
    │
    │  ← 用户依次质疑"缓存删了为何还快"（两层缓存）、"是否该放 compose"（统一管理）
    ▼
registry-mirrors 短名拉取 + proxy 收编 compose + 样例工程 adminer+postgres 验证通过
```

**dind 无外网根因（三层证据）：**


| 层           | 现象                                | 结论                     |
| ------------ | ----------------------------------- | ------------------------ |
| 宿主进程     | curl registry-1.docker.io 401（通） | 代理对 OUTPUT 生效       |
| dind ping IP | 223.5.5.5 通                        | 纯 IP 路由正常           |
| dind DNS     | 返回 198.18.0.170                   | fake-ip，透明代理特征    |
| dind pull    | TCP 握手悬挂                        | FORWARD 链不在代理规则内 |

**镜像供给三条路对比（决策依据）：**


| 方案               | 管理员介入       | test 自助 | 私有镜像  |
| ------------------ | ---------------- | --------- | --------- |
| save/load 离线导入 | 每个新镜像都要   | ✗        | 兼容      |
| 5000 私有仓库      | push 需管理员/CI | 拉取自助  | ✓        |
| 6000 proxy-cache   | 首次回源自动     | 完全自助  | 不能 push |

**registry:2 双实例必须分离（5000 vs 6000）：**

- 5000 `infra-registry`：私有存储模式，可 push（存 solar-server）
- 6000 `registry-proxy`：proxy 模式（`proxy.remoteurl=https://registry-1.docker.io`），**官方 pull-through cache 不接受上传** → 合并会断私有镜像流 + 缓存与私有数据混库互毁

**两层缓存（用户踩坑点："缓存删了为什么还快"）：**

```
缓存层②                       缓存层①
Docker Hub ──▶ 6000 proxy ──▶ dind 本地镜像库 ──▶ 容器
               registry-proxy-data 卷    /var/lib/docker
               没删（用户误以为删了）      用户 rmi 删的是这层
```

- 证据：`docker logs registry-proxy` 部署瞬间 19 条 `GET /v2/library/adminer/blobs/...` 200，响应 `4-8ms` = 命中层②缓存秒回；`written=562110` 字节确实传输 → "重新下载了，只是走内网所以快"
- 想验证真回源：清 `registry-proxy-data` 卷 → 重新拉起 registry-proxy（约 400MB 重新回源下载）

**关键工程决策：**

- **registry-proxy 必须 host 网络**：容器网桥出网受限（回源 Docker Hub 失败），host 模式流量走宿主代理；config 中 `http.addr` 相应改 `:6000`
- **dind daemon.json 演进**：初版仅 nvidia runtime → 加 `insecure-registries:[5000,6000]` → 加 `registry-mirrors:["http://10.8.0.8:6000"]`（短名自助）；nvidia-ctk runtime configure 会 **merge 保留** insecure-registries 字段（Dockerfile COPY daemon.json 预置）
- **短名 vs 完整地址**：官方镜像写 `nginx:alpine`（走 mirror）；带前缀镜像如 `bitnami/xx` mirror 不覆盖，写 `10.8.0.8:6000/library/xx`（insecure-registries 已信任）
- **port-forward 加新服务**：PF_RULES 加规则 + 重启本容器即可，不用重启 dind；`depends_on` 用 `condition: service_healthy`

**踩坑记录（按提问顺序）：**

1. 页面部署"没重新拉取" → 镜像已在 dind 本地，直接复用秒起；**删容器不删镜像**，Portainer 用 `Unused` 标记无引用镜像
2. 页面 Containers 显示空但删镜像报"image is being used by running container" → **UI 缓存未刷新**，agent 实际能看到容器（`docker exec dind-platform docker ps` 为准）
3. `docker rm -f` 后镜像还在 → 正常，rm 只删容器，镜像需单独 rmi
4. 一个镜像显示两个名字（`10.8.0.8:6000/library/adminer:4` + `adminer:4`）→ 同一镜像两个 tag（完整地址拉过 + 短名 mirror 拉过），删除时一起消失
5. 无 tag 镜像 `f0ba77f796e5`（62.4MB）→ `demo-nginx` 容器的 dangling 镜像（nginx:alpine tag 被删但容器在用，Docker 保留层）→ 删容器后 `docker image prune -f` 回收 62.36MB
6. compose 中 `port-forward` 曾有两个 `depends_on` 键 → 后者覆盖前者，已合并为 `condition: service_healthy`
7. `portainer-auto-register` 停止（Exited 0）→ **设计行为**：一次性注册任务成功退出，失败才 `on-failure:3` 重试，非残存物
8. `docker exec dind-platform sh -c 'cat > file' <<EOF` 写入空文件 → **docker exec 需加 `-i`** 才转发 stdin
9. 页面直接删 stack 容器即消失 → 但镜像不随 stack 删除，需手动 rmi

**样例工程（test 自助验证用例，双入口可验证）：**

- `/root/edge/portainer/examples/demo-app/docker-compose.yml`：adminer:4（管理页面 8091）+ postgres:16-alpine（数据库 5433）
- 验证：管理页面 `http://10.8.0.8:8091`（app/app123/appdb）；PG 客户端直连 `10.8.0.8:5433`
- ⚠️ 样例工程当前不在运行：`/opt/demo-app` 写在 dind 可写层，dind 重建后丢失，需重新写入再 `docker compose up -d`

**本次收尾动作：**

- demo-nginx 遗留容器已删 + 8088 转发规则移除（PF_RULES 只剩 8091/5433）
- dind 内仅剩 `portainer/agent:lts`，dangling 全部清理

---

## 🎭 环境授权与宿主机风险权衡 260820

```
test 看不到资源，团队用不上环境
    │
    │  ← 既定的共享 admin 方案更省事，不如直接交账号
    ▼
把 admin 直接交给团队
    │
    │  ← 但动手前问了一句：对宿主机有什么风险
    ▼
风险：admin 能改密锁门、动全局设置；容器网络可达宿主
无鉴权服务（ollama 11434 / webui 8080）→ 资产可能被摸走
    │
    ▼
放弃共享 admin，改为给 test 用户授权环境资源
    │
    │  ← 授权时卡住：Standard User 穿不了 GPU
    ▼
需要一个"环境内能跑 GPU、平台层碰不到"的角色
```

**授权阶梯（给团队开环境的通用决策框架）：**


| 角色                          | 权限范围                                                                    | 何时给                |
| ----------------------------- | --------------------------------------------------------------------------- | --------------------- |
| 平台 Admin                    | 全平台全权：改密码锁死你、动全局设置、管所有环境                            | **永不外借**          |
| Environment Admin（RoleId=1） | 环境内全权：可穿 GPU、管 stack；平台层不可触（Settings/用户/改密/其他环境） | 给要跑 GPU 推理的团队 |
| Standard User（RoleId=2）     | 环境内受限：穿不了设备、挂不了任意目录（SecuritySettings 默认禁）           | 给只跑一般服务的团队  |

**宿主机资产暴露（顾虑的根因，网络级风险"现在就能利用"）：**

- 无鉴权服务：ollama 11434 / vllm 18000（dummy key）/ webui 8080（WEBUI_AUTH=False）/ solar 5901（默认 JWT）
- 团队容器在 `portainer-net`，默认可达宿主 10.8.0.8 任意端口 → 模型/数据可被摸走
- rancher 8008/8443 是 privileged 容器 → 潜在的逃逸跳板

**共享 admin 的代价（决策依据）：**


| 风险面 | 具体                                                                   | 严重度       |
| ------ | ---------------------------------------------------------------------- | ------------ |
| 账号级 | admin 可改密码锁死你、改设置、管用户                                   | 最现实       |
| 网络级 | dind 内容器可达宿主无鉴权服务，见上                                    | 现在就能利用 |
| 逃逸级 | dind 是 privileged + 透传 /dev/nvidia*，恶意+内核/N卡驱动漏洞=逃逸路径 | 理论威胁     |
| 资源级 | GPU 无硬配额，整卡穿透靠自觉                                           | 可用性风险   |

**本次落地（注脚）：**

- test(Id=2) → dind-platform(Id=1)：`UserAccessPolicies: {"2":{"RoleId":1}}`，环境配置完好
- 授权 API 踩坑：❌ 旧 `PUT /api/endpoints/{id}/user/accesses/{userId}` 在 2.39 已移除（404）；✅ 正确 `PUT /api/endpoints/{id}`，body `{"UserAccessPolicies":{"<用户Id>":{"RoleId":n}}}`；查询 `GET /api/users` / `GET /api/endpoints`
- SecuritySettings（Standard User 默认限制，环境级）：bind mount / privileged / device mapping / host namespace 均 false；stack 管理 true
- ⚠️ 环境 GPUs 显示 **GPU0+GPU1 两张卡都透传进了 dind**（非之前以为的只有卡0）；若只给团队卡0 需在 dind 层去掉 GPU1

---

## 🌀 UI 右上角加载图标周期性出现 260820

> 打开 `https://10.8.0.8:9443/#!/home`，右上角加载图标隔一段时间就转一下——是页面在定期取数据？是的，这是 Portainer 前端的**定期轮询（polling）机制**，正常现象，不是故障。

**原因链路（本部署）：**

```
浏览器 → Portainer Server(9443) → Agent(9001) → dind-platform 内 dockerd
```

每次轮询都是一条完整的 `Server → Agent → dockerd` 链路请求，加载图标由前端全局 HTTP 拦截器驱动：**发起 API 请求就显示、请求完成就隐藏**，所以呈周期性闪现。

**轮询内容（三类）：**


| 类型                | 说明                                                       |
| ------------------- | ---------------------------------------------------------- |
| 环境状态刷新        | Home 页定期重新拉取 endpoint 列表及状态（容器数/运行态等） |
| 环境健康/心跳       | Server 定期探测各 endpoint 的 agent 是否在线               |
| 事件流（WebSocket） | 订阅 Docker 事件做实时刷新，断线重连也会触发加载           |

**关键认知：**

- 轮询间隔在前端是**固定常量**（约 30s ~ 1min 量级，随版本/视图不同），F12 → Network 可看到对 `/api/endpoints/*`、`/api/agent/*` 的周期请求
- **没有官方"关闭加载动画"的开关**，要"不显示"只能从浏览器端入手
- 处理方案（本次结论：**先不用，记录备用**）：
  1. **浏览器端隐藏**（唯一能"不显示"的办法）：Tampermonkey/Stylus 注入 CSS 隐藏加载元素，页面数据照常刷新；注意 SPA 会重建节点，需 MutationObserver 兜底
  2. **降低后端轮询频率**（减少出现次数）：`PUT /api/settings` 调大 `agentPollInterval`（Server 轮询 Agent 间隔）与 `snapshotInterval`（环境快照生成间隔），单位秒，需先 `POST /api/auth` 拿 token
- 注意：前端自身的周期刷新（如 Home 页定期重拉 endpoint 状态）**没有可配置项**，只能靠 CSS 隐藏；也不建议完全取消轮询，环境/容器状态就靠这些请求保持最新

---

## 🔑 admin 密码接口硬编码复杂度校验 260820

> 想用 API 把 admin 密码改成 `test123456789`（13 位）却被拒（400 `Invalid new password`）？因为 Portainer 的**改密码接口**（`PUT /api/users/{id}/passwd`）有硬编码的密码复杂度校验（至少 12 位 + 需含大小写+数字+符号），而**创建用户接口没有**——同一密码建用户成功、改密码却失败（实测验证）。


| 操作                                       | 密码             | 结果                         |
| ------------------------------------------ | ---------------- | ---------------------------- |
| `POST /api/users`（创建用户）              | `test123456789`  | ✅ 200                       |
| `PUT /api/users/1/passwd`（改 admin 密码） | `test123456789`  | ❌ 400`Invalid new password` |
| `PUT /api/users/1/passwd`（改 admin 密码） | `Admin@2026pass` | ✅                           |

**关键认知：**

- 复杂度校验在代码里写死，`/api/settings` 的 `InternalAuthSettings` 只有 `RequiredPasswordLength: 12`，**无法关闭**
- `--admin-password-file` 只在**首次初始化**生效；已初始化后改文件/重启**不会改变数据库里的密码**，必须走 UI 或 API
- 正确改密姿势：新密码必须满足 12 位 + 含大小写+数字+符号（如 `Test123456789!`），`PUT /api/users/1/passwd` body 为 `{"Password": "...", "CurrentPassword": "..."}`
- 本次最终决定：**保持原密码 `Admin@2026pass` 不变**（密码文件与数据库一致），未改密；测试用户（pf-probe/pf-probe2）已清理

---

## 🌐 外部访问 dind 中的服务 260819

> dind 里的 demo-nginx 映射了 8080，但宿主 `10.8.0.8:8080` 访问到的是 Open WebUI 而不是 nginx；并且 socat 必须在本机（宿主）执行——为什么？怎么让外部真正访问到 dind 内的服务？因为 dind 内容器端口映射只在 dind 网络命名空间生效，宿主 8080 又被 Open WebUI 占用，最终用 socat 在宿主转发 `8088 → 172.24.0.3:8080` 打通外部访问。

**为什么 socat 必须在宿主跑——看这条链路就明白了：**

```
外部设备 / 本机
    │
    ▼ 访问 10.8.0.8:8088（宿主 IP，本机的网卡）
本机 socat  ← 在这里监听 + 转发
    │
    ▼ TCP 转发到 172.24.0.3:8080
dind 内 nginx
```

转发点必须**监听外部可访问的地址**（宿主网卡），只能放在宿主。若放到 dind 内部跑，它监听的是 dind 网络命名空间里的端口，外部（10.8.0.8）访问不到——因为没有宿主层转发，宿主端口与 dind 内端口不联通。

**端口占用排查（实测）：**


| 地址                               | 返回            | 结论                        |
| ---------------------------------- | --------------- | --------------------------- |
| `10.8.0.8:8080`（宿主 IP）         | Open WebUI 页面 | 8080 被宿主 Open WebUI 占用 |
| `172.24.0.3:8080`（dind 内 nginx） | nginx HTTP 200  | dind 内服务正常             |

**socat 转发（宿主执行）：**

```bash
nohup socat TCP-LISTEN:8088,fork,reuseaddr TCP:172.24.0.3:8080 &
# 验证：外部访问宿主 8088 → 落到 dind 内 nginx
curl -s -o /dev/null -w "%{http_code}" http://10.8.0.8:8088/   # → 200
```

**持久化方案 A/B——核心区别：谁来完成"宿主机 → dind 内"这段路？**

两个方案都要把流量从**宿主机**送到 **dind 里面**，区别是这段路由谁来跑。

方案 B：给 dind-platform 容器加 ports（Docker 内核跑腿）：

```
访问 10.8.0.8:8088
        │
        │  ← 这段由 Docker 内核的 NAT 直接完成（ports 一行配置）
        ▼
dind-platform:8080（dind 内的 docker-proxy 在收）
        │
        ▼
nginx
```

- 只是给**现有的 dind-platform 容器**加一行 `ports: "8088:8080"`
- 不新增任何东西，Docker 自动转发
- 但条件是：**dind 里得有服务占着 8080 端口**（docker-proxy 监听它），所以 dind 内 nginx 必须发布成 `-p 8080:80`

方案 A：多雇一个 socat 容器专门跑腿：

```
访问 10.8.0.8:8088
        │
        │  ← Docker NAT（ports），只负责送到 socat 容器
        ▼
port-forward 容器（socat 进程）← 多出来的一个容器
        │
        │  ← socat 自己决定转发到哪（172.24.0.3:8080）
        ▼
dind 内 nginx
```

- 新增一个独立容器，里面跑 socat
- 转发目标是 socat 自己说了算，**不要求 dind 内必须有某个端口**

**一句话总结（跑腿的 / 要新增什么 / 转发目标 / 加新服务）：**


|            | 方案 B                     | 方案 A                       |
| ---------- | -------------------------- | ---------------------------- |
| 跑腿的     | Docker 内核（免费自带）    | 独立 socat 容器              |
| 要新增什么 | 零（dind 加一行 ports）    | 一个新容器                   |
| 转发目标   | 必须是 dind 的某个发布端口 | 任意 IP:端口                 |
| 加新服务   | 改 compose 重启 dind       | 改 socat 配置重启 socat 容器 |

**用一个实际场景感受区别**：假设团队在 dind 里同时部署了三个服务：nginx(80)、MySQL(3306)、Redis(6379)。

- 方案 B：dind 内容器发布端口要"挤"在 dind 容器上，每个服务占用 dind 容器的一个端口号，以后每加一个服务都要改 compose、重启 dind：

```yaml
ports:
  - "8088:8080"    # 宿主8088 → dind的8080 → 只能给 nginx 用
  - "8090:3306"    # 宿主8090 → dind的3306 → MySQL
  - "8091:6379"    # 宿主8091 → dind的6379 → Redis
```

- 方案 A：socat 转发目标自己定，还可以配多条，不用管 dind 里端口号怎么发布，只改 socat 容器的配置，**不用动 dind**：

```
宿主8088 → nginx 容器IP:80
宿主8090 → mysql 容器IP:3306
宿主8091 → redis 容器IP:6379
```

**结论**：对"团队在 dind 里自己起服务"的场景，**方案 A 更省事**：团队把服务 IP 报给你，你在 socat 里加一条就完事，不用重启 dind、不影响里面正在跑的东西。

**落地进度（260820）：方案 A 已容器化，临时 socat 退役**

原 `nohup socat ... &` 临时进程已关闭，方案 A 落地为 `port-forward` 服务（`/root/edge/portainer/docker-compose.yml`），新增 `Dockerfile.port-forward`（alpine:3.22 + socat）与 `entrypoint-port-forward.sh`：

```yaml
# docker-compose.yml 新增服务（独立容器，不碰 dind-platform）
port-forward:
  build:
    context: .
    dockerfile: Dockerfile.port-forward   # alpine:3.22 + socat
  restart: always
  environment:
    # 规则: 监听端口:目标主机:目标端口（逗号分隔多条）
    - PF_RULES=8088:dind-platform:8080    # 目标主机用 compose 网络名，不依赖容器 IP
  ports:
    - "8088:8088"
  networks:
    - portainer-net
  depends_on:
    dind-platform:
      condition: service_healthy
```

- 转发链路：外部访问 → Docker NAT → port-forward 容器 → socat → `dind-platform:8080`（dind 内 docker-proxy 在收）→ nginx，实测 200
- 加新服务：改 `PF_RULES` 加一条规则（如 `8090:dind-platform:3306`），然后 `docker compose up -d port-forward` —— **recreate 才生效**；`restart` 只是用旧配置重启，环境变量改了不生效
- 自愈双保险：entrypoint 每条规则 `while :; do socat ...; sleep 2; done`（进程级）+ compose `restart: always`（容器级）
- 注意：`up -d --build` 涉及镜像重建时会**连带 recreate dind-platform**（镜像有依赖）；只改 `PF_RULES` 不会动 dind

---

## 📥 dind 内镜像导入 260819

> dind-platform 里没有外网（无法 pull 镜像），怎么在里面跑一个测试应用验证整套链路？

链路是宿主导出 tar → `docker cp` 进 dind → 容器内 `docker load` → 再 `docker run`，用 nginx:alpine 验证通过。

**完整链路（demo-nginx）：**

```bash
# 1. 宿主导出 nginx 镜像
docker save nginx:alpine -o nginx.tar
# 2. 复制进 dind-platform
docker cp nginx.tar dind-platform:/tmp/
# 3. dind 内导入并启动
docker exec dind-platform sh -c 'docker load -i /tmp/nginx.tar && \
  docker run -d --name demo-nginx --restart unless-stopped -p 8080:80 nginx:alpine'
# 4. 验证：dind 内访问
docker exec dind-platform curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8080/
```

**关键认知**：dind 内容器 `-p 8080:80` 的端口映射**只在 dind 自己的网络命名空间生效**，宿主 `127.0.0.1:8080` 并不会自动出现——外部访问需要额外转发（见上一条）。

---

## 🔄 宿主机 dind 两套资源隔离 260819

> 多用户资源隔离怎么做才对？模板配额、环境级隔离、共享 admin 三种思路，最终拍板了哪种？最终就是共享 admin：`admin / Admin@2026pass` 管理唯一环境 `dind-platform`（16 核 / 32G / GPU 卡0），宿主对 admin 完全不可见。

**演进过程（维度：隔离粒度 / 管理复杂度 / 强制性）：**


| 阶段     | 方案                                                        | 问题                                   |
| -------- | ----------------------------------------------------------- | -------------------------------------- |
| 初始思路 | compose 模板写死配额（cpus/mem/device_ids）+ 存储宿主层兜底 | 模板可被修改、无强制力                 |
| 阶段一   | 模板 → 环境级隔离（每团队独立 dind + 用户授权）            | 管理复杂；用户"一个团队就够了"         |
| 阶段二   | 环境级隔离 →**共享 admin**（最终）                         | 协调靠自觉、共享密码有安全面，但最简单 |

**最终拍板：**

```
admin（共享账号，团队自己协调）
└─ 唯一环境 dind-platform: 16核 / 32G / GPU卡0（docker 层锁死，UI 改不了）
   ├─ 团队A 部署自己的容器
   ├─ 团队B 部署自己的容器
   └─ 资源总量不超过 16核/32G/卡0 → 宿主永不超卖
```

- 账号：`admin / Admin@2026pass`，访问 `https://<IP>:9443`
- 环境：唯一 `dind-platform`，配额在 dind 容器层（`cpus: 16` / `mem_limit: 32g` / `device_ids`）——**已生效并经界面/cgroup 验证**
- 隔离：Portainer 不挂宿主 socket → **宿主（miner/vllm/gitea/rancher）对 admin 不可见**，admin 能控制的"整体"就是 dind 这个环境
- 模板方案、用户授权方案都不需要了，交付就一句话：账号 + 地址
- 两点注意：① 资源协调靠团队自觉；② 共享 admin 密码应定期更换
- 扩展性：宿主 32 核/125G/2 卡，两个 16 核/32G 分片正好用完；要第三四个团队就降配额（如 8核/16G，GPU 无卡共享或不给）

---

## ⚙️ dind 打通 GPU 260819

> dind-platform 用 Alpine 镜像，官方/NVIDIA 仓库都没有 nvidia-container-toolkit 包，如何让第2层 Docker 也能 `--gpus all`？最终把宿主的 nvidia-container-toolkit **全部只读挂载进 dind**（离线、不装包）+ 修正版 `config.toml`，`docker run --gpus all nvidia-smi` 验证通过（2× RTX 4090，驱动 590.48.01）。

**为什么走挂载方案（路线排除过程）：**


| 尝试路线                        | 结果                                      |
| ------------------------------- | ----------------------------------------- |
| apk 装 nvidia-container-toolkit | Alpine 3.22 community**没有这个包**       |
| NVIDIA 官方仓库（Alpine）       | **官方仓库不支持 Alpine**（只有 deb/rpm） |
| **宿主挂载（最终）** ✅         | 离线、只读、不装包，全部从宿主同步        |

**挂载清单（全部只读、离线）：**


| 挂载项                                          | 作用                                                  |
| ----------------------------------------------- | ----------------------------------------------------- |
| 4 个二进制（nvidia-ctk / runtime / hook / cli） | toolkit 本体                                          |
| glibc 库（/lib/x86_64-linux-gnu + /lib64）      | 二进制是动态链接，Alpine musl 缺 glibc                |
| 驱动库（libcuda/libnvidia-*）                   | 供 hook 挂进目标容器                                  |
| `/etc/ld.so.cache`                              | CLI 解析驱动库路径（Alpine musl ldconfig 不支持`-p`） |
| `/sbin/ldconfig.real`                           | 容器内生成`.so.1` 软链（static-pie，直接可跑）        |
| `nvidia/config.toml`（修正版）                  | 禁`load-kmods` 等探测                                 |

**三个坑（必须同时解决）：**

1. **musl ldconfig 不支持 `-p`** → CLI 库探测失败 → 改挂宿主 `/etc/ld.so.cache`。
2. **缺 `.so.1` 软链**：驱动目录只有 `.590.48.01` 真实文件，没有 `.so.1` → 挂载 glibc 版 `ldconfig.real` 并在 config.toml 里指定 `ldconfig = "@/sbin/ldconfig.real"` 生成软链。
3. **config.toml 需禁 `load-kmods`**：容器内无内核模块加载能力，开启会报错。

**Portainer 界面看不到 GPU 的原因（链路）：**

设备直通 `/dev/nvidia*`（dind 内 CUDA 应用可直接用）≠ **dockerd 认识 GPU**。Portainer 的 GPU 信息来自 Docker API 上报：dockerd 没装 nvidia-container-toolkit / 没配 nvidia runtime → API 无 GPU 信息 → 界面 GPU 列显示 `-`。要界面显示并支持 `gpus:` 语法部署，需 dind 内 toolkit + `daemon.json` 配 nvidia runtime；UI 还需手动登记"Show GPU in the UI"。

**后续维护**：宿主升级驱动/toolkit 时**无需重建镜像**，重启 dind 即可生效。

**权衡**：坚持"不挂宿主 /host"（保住宿主不可见隔离），代价是 toolkit 升级靠宿主侧同步。

---

## 📊 界面数字 vs cgroup 真实限制 260819

> dind 明明限制了 16 核/32G，为什么 Portainer 界面却显示 32 核/134.9 GB？这个数字是假的吗？不假但只是宿主全量：那是 dockerd 在容器内读 `/proc` 看到的 32 核 / 134.9 GB，**不反映 cgroup 配额**，真实限制看 `cpu.max=1600000`（16 核）、`memory.max=34359738368`（32 GiB）。

**实测数据：**


| 指标 | Portainer 界面       | 真实 cgroup 限制                     |
| ---- | -------------------- | ------------------------------------ |
| CPU  | 32 核（宿主全量）    | `cpu.max = 1600000/100000` → 16 核  |
| 内存 | 134.9 GB（宿主全量） | `memory.max = 34359738368` → 32 GiB |

**原因链路**：Portainer 通过 Agent 拿到的资源数，是 **dockerd 在容器内"看到"的硬件**（`/proc/cpuinfo`、`/proc/meminfo`），这些文件在普通容器里**不伪装、不反映 cgroup 配额**——所以 dockerd 报告的是宿主全量，而**实际可用额度**由 cgroup 强制。这是所有 cgroup 限容容器的通病，不是配置错误。

**实际效果**：团队在 UI 里看着有 32核/134.9GB（虚高），但真跑超 16核/32G 时会被 cgroup 节流/OOM 强杀——**限制是硬的，不会冲破**，也不会波及宿主和其他环境。

**可选优化（两种处理）：**


| 处理       | 做法                                                     | 评价                   |
| ---------- | -------------------------------------------------------- | ---------------------- |
| 不改       | 环境说明里注明"实际配额 16核/32G/GPU卡0"即可             | 推荐，团队用超自然被卡 |
| lxcfs 伪装 | 给 dind 挂 lxcfs 把`/proc` 虚拟成配额值，UI 就显示真实数 | 改动不小，收益不大     |

---

## 🏗️ DinD 强隔离架构 260819

> 如何让 Portainer 只管理"平台隔离环境"、完全屏蔽宿主上已有容器（miner/vllm/gitea/rancher）和硬件资源？做法：**Portainer Server 不挂宿主 docker.sock**（纯控制面），通过 Agent 连接一个 **dind-platform**（第2层 dockerd v28.5.2）作为唯一隔离环境，宿主旧容器对 Portainer 完全不可见。

**架构图：**

```
宿主 Docker（第1层）
├─ portainer      （Server，不挂宿主 socket，纯控制面，9443 UI）
└─ dind-platform  （privileged，内部 dockerd v28.5.2，GPU 整卡穿透）
   ├─ 平台容器（团队A 的容器）
   └─ portainer-agent（9001，挂 /var/run/docker.sock）
宿主旧容器 vllm/gitea/rancher → Portainer 完全不可见 ✅
```

**强隔离 vs 授权隔离（选型决策依据）：**


| 维度               | 选项3（授权隔离，挂宿主 socket） | 选项2（DinD 强隔离，最终）                    |
| ------------------ | -------------------------------- | --------------------------------------------- |
| 容器物理位置       | 都在宿主 Docker                  | 各自独立 dind                                 |
| 团队互不可见       | 否（授权级）                     | **是（物理级）**                              |
| 数据卷隔离         | 靠命名                           | **各自独立卷**                                |
| 资源配额           | compose limits（软）             | 宿主 cgroup 限 dind（**硬**）                 |
| 单团队可跑独立镜像 | 共享宿主镜像                     | 独立镜像缓存                                  |
| 资源开销           | 低                               | **高（每个 dind 一套 dockerd + 镜像层翻倍）** |
| GPU                | 整卡透传简单                     | 穿透两层，麻烦                                |
| 复杂度             | 低                               | **高**                                        |

最初建议：只有"团队之间必须是**不可信/物理隔离**"（外部客户、安全合规"绝对看不到彼此"）才值得上 DinD；内部团队 + 可信 + 单机 + 不想重，授权隔离 + 宿主 cgroup 兜底足够。**用户最终坚持 DinD 强隔离**（要求屏蔽本机硬件和已有资源），落地为上述架构。

**关键配置：**

- `dind-platform`：privileged、内部 dockerd v28.5.2、GPU 设备穿透（`/dev/nvidia0`、`nvidia1`、`nvidiactl`、`nvidia-uvm` 全部在 dind 内可见，实际按需整卡）。
- 用户明确：**不用分两个团队，一个 dind 即可**（从双团队配置回退）。
- Portainer Server 不挂宿主 socket → 宿主不可见 → 天然满足"屏蔽本机硬件和已有资源"。

**agent 注册（手动方式）：**

```bash
# 在 dind-platform 内拉取并启动 portainer-agent（暴露 9001，供 Server 连接）
docker exec dind-platform sh -c 'docker run -d --name portainer-agent -p 9001:9001 \
  -v /var/run/docker.sock:/var/run/docker.sock portainer/agent:latest'
```

然后在 Portainer UI：Environments → Add environment → **Docker Standalone → Agent**，地址填 `dind-platform:9001`。

**agent 自动注册（脚本化方式）：** `auto-register.sh` 通过 API 幂等注册环境：`EndpointCreationType=2`（Agent）、`URL=tcp://dind-platform:9001`、`TLS=true + SkipVerify`，请求头用 `X-Api-Key`，密码从 `/run/secrets/portainer_admin_password` 读取。

---

## 🐳 Portainer 部署 260819

> Portainer 首次启动要走 UI 初始化 admin，但中途遇到"初始化超时锁定"和"密码策略误报"，最终怎么自动完成初始化？用 `--admin-password-file=/run/secrets/portainer_admin_password` 让 Portainer **自动初始化 admin**，彻底绕过 UI 初始化页面、避开超时锁定；密码 `Admin@2026pass`（13 位）。

**部署 compose（`/root/edge/portainer/docker-compose.yml`）：**

```yaml
services:
  portainer:
    image: portainer/portainer-ce:lts      # 2.39.6 最新 LTS
    command: ["--admin-password-file=/run/secrets/portainer_admin_password"]
    ports:
      - "9443:9443"                        # UI(HTTPS)
      - "8000:8000"                        # Edge(可选)
    volumes:
      - portainer_data:/data
    secrets:
      - portainer_admin_password
secrets:
  portainer_admin_password:
    file: ./portainer_admin_password.txt   # 内容：Admin@2026pass
```

**踩坑记录：**

1. **初始化超时锁定**：首次初始化必须在 ~240s 内完成，超时后 `/api/setup` 被锁定，只能重建容器/清卷。
2. **密码策略 12 位误报**：UI 提示文案不精确，实际要求密码**至少 12 位**（含大小写+数字+符号双保险）；`Admin@2026pass` 13 位复合密码一次通过。
3. **setup token**：Portainer 首次启动会生成一次性 setup token（`X-Setup-Token` 请求头），用于 `/api/setup` 初始化 admin——被超时锁定后此路不通，改走密码文件方案。

**验证**：`curl -X POST https://127.0.0.1:9443/api/auth -d '{"Username":"admin","Password":"Admin@2026pass"}'` 返回 200 登录成功。

**清理**：原 Rancher 目录 `/root/deAI/infra/rancher` 已清理。

---

## 📐 理清以 compose 为核心的需求 260819

> 分析 rancher 为什么依赖 K3S、镜像启动过程是怎样的？Rancher 这个版本有点复杂——换方案还是用老的版本？需求重构：以 docker compose 为管理对象、对多个用户做资源隔离即可

候选方案对比


| 维度           | ① Rancher v2.14                                 | ② Rancher 1.x       | ③ Portainer CE             | ④ 纯 compose + 脚本/RBAC |
| -------------- | ------------------------------------------------ | -------------------- | --------------------------- | ------------------------- |
| **管理对象**   | K8s（内嵌 k3s）                                  | Cattle/Docker        | **Docker compose** ✅       | **Docker compose** ✅     |
| 多用户操作隔离 | 用户+集群/项目 RBAC                              | 环境/账号            | 用户+团队+环境权限          | 无面板，靠文件权限        |
| CPU/内存配额   | 项目配额（成熟）                                 | 无                   | compose limits+模板（够用） | compose limits            |
| 存储隔离       | 项目配额                                         | 无                   | 需宿主 XFS quota 兜底       | 需宿主 XFS quota          |
| GPU 整卡分配   | 支持（透传）                                     | 早期支持             | **支持** `device_ids` ✅    | 支持                      |
| GPU 切分共享   | 需 HAMi（额外）                                  | ❌                   | ❌（不需要）                | ❌                        |
| 管理组件开销   | **重**（内嵌集群+fleet+webhook+turtles+helm-op） | 中（server+agent）   | **轻**（1 个管理容器）✅    | 零                        |
| 部署复杂度     | 高（特权、内嵌集群、镜像源坑）                   | 中                   | **低** ✅                   | 中（无 UI）               |
| 自助 UI        | 有                                               | 有                   | 有                          | ❌                        |
| 维护状态       | 官方推荐 Helm on K3s（弃 Docker 单容器）         | **EOL 6年** ❌       | **活跃** ✅                 | —                        |
| 匹配度         | **错配**（多集群管理面）                         | 严重不匹配（非 K8s） | **最贴合** ✅               | 贴合但无自助              |

** 为什么 Rancher 2.x 那么重（compose 配置 ↔ 启动链路对应关系）：**

Rancher 是 K8s 原生管理平台，自身必须跑在集群上；单容器模式没有外部集群，官方把 K3s（二进制 + airgap 系统镜像 + containerd 配置）整体内嵌进 `rancher/rancher` 镜像，启动时由 `entrypoint.sh` → `rancher` 主进程先在容器内拉起一个本地 K3s 集群，再把 Rancher 自身部署上去对外提供服务。**"一个容器 = 一套完整 Rancher"**，所以它需要 privileged、必须挂载 k3s 的 `registries.yaml`、`/var/lib/rancher` 里同时存在 K3s 数据。

** 为什么 Rancher 1.x 老版本也不行：**


| 项目       | Rancher 1.x（2016-2020）                                   | Rancher 2.x（2018 至今）                        |
| ---------- | ---------------------------------------------------------- | ----------------------------------------------- |
| 编排引擎   | **Cattle**（docker-compose 风格编排容器）                  | **Kubernetes**（k8s 优先）                      |
| 部署       | 一条命令`docker run rancher/server`，UI 在 8080            | Docker 单容器（内嵌 k3s）或 Helm on K3s         |
| 概念       | 环境(environments)/主机(hosts)/堆栈(stacks)/服务(services) | 集群(clusters)/项目(projects)/命名空间/工作负载 |
| 为什么简单 | **没有 K8s 那一层**，直接编排 Docker 容器                  | 自带整套 K8s + CRD + 控制器                     |

Portainer vs K8S


| 你的诉求                        | K8s/K3s 表现                                                                                                                                                         |
| ------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 以**docker compose** 为管理对象 | ❌ K8s 根本不用 compose，用 Helm/Deployment——要么改写法，要么套 K3s 反向兼容，都别扭                                                                               |
| **不想重**                      | ❌ 必然引入 kubelet + apiserver + etcd + containerd + CNI + 一堆系统组件，哪怕 K3s 轻量也是整层栈；现有 compose 项目（miner/vllm/solar/gitea）要么迁进去要么双轨并存 |
| GPU 整卡即可                    | ❌ 要装 HAMi/device-plugin 才勉强等同 Docker 的`device_ids`，绕远路                                                                                                  |
| 多用户隔离                      | ✅ K8s 确实强，但 compose 场景用 Portainer 的团队/环境权限就够了                                                                                                     |

Portainer 架构理解

```
┌────────────────────────────────────────────┐
│ 宿主机 Docker                                 │
│  ┌─────────────────────────────┐           │
│  │ Portainer Server 容器       │ ← 唯一的常驻管理容器
│  │   ├─ Web UI (HTTP API)      │     挂载 /var/run/docker.sock
│  │   └─ 内置 BoltDB 数据库     │     + /data 卷(存用户/团队/配置)
│  └─────────────────────────────┘           │
│  ┌─────────────────────┐  ┌────────────┐  │
│  │ 团队A compose 容器   │  │ 团队B ...  │  │  ← 实际工作负载，普通容器
│  └─────────────────────┘  └────────────┘  │
└────────────────────────────────────────────┘
```

- **local agent 模式（单机最简单）**：Portainer Server 挂载宿主 `docker.sock` 直接控制 Docker。本方案**不用**（宿主不可见需求）。
- **agent 模式（本方案）**：每台被管机器跑一个轻量 `portainer-agent` 容器，Server 通过它远程控制——本项目 Portainer Server 连的是 **dind 内的 agent**，宿主 socket 完全不挂。
- 存储：配置存本地 BoltDB（`/data`），不依赖外部数据库。
- 对比 Rancher：Rancher 自带整层 K8s（apiserver/etcd/containerd/fleet/webhook...几十个组件），Portainer **只有 1 个管理容器 + 直接控制 docker.sock**，轻两个量级。

---
