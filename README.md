# Edge

## 容器服务

```
[启动容器环境]
  │
  ├─▶ ✖ agent.tar 未保存（仅首次）──────▶ ① 保存 agent 镜像
  └─▶ ✔ 已保存 ──▶ ② 启动：docker compose up -d --build
                      │
                      ▼
                   [检查] 5 组件全 up · dind 注册成功？
                      ├─▶ ✖ 异常 ──▶ docker compose ps / logs 排查
                      └─▶ ✔ 正常 ──▶ ③ 使用：管理 / 拉镜像 / 端口转发（见表）
                                      │
                                      ▼
                                 ④ 停止：docker compose down
```


| 服务          | 地址                                                                     |
| ------------- | ------------------------------------------------------------------------ |
| Portainer     | `https://<宿主IP>:9443`（admin / 密码见 `portainer_admin_password.txt`） |
| Registry 代理 | `<宿主IP>:6000`                                                          |
| 端口转发      | 8001(adminer) / 5433(postgres) / 8002-8004(预留) / 3306 / 5432           |

```bash
cd /root/edge/stacks

# ① 准备（仅首次）：保存 agent 镜像，dind 内 agent 离线可用
docker save portainer/agent:lts -o agent.tar

# ② 启动 / 更新
docker compose up -d --build

# ③ 检查：5 组件状态 + dind 注册结果
docker compose ps
docker compose logs -f auto-register

# ④ 停止
docker compose down

# 可选：加外部转发（PF_RULES 追加"监听端口:dind-platform:端口"并同步 ports）
docker compose up -d port-forward
```

## 推理服务

```
[启动推理服务]
  │
  ├─▶ ✖ 镜像未拉 / 模型未下（pull_policy: never）──▶ ① 准备
  ├─▶ ✖ Ollama 占用 GPU/8080 ───────────────────────▶ ② 停 Ollama
  └─▶ ✔ 前置满足
       │
       ▼
    [选模型]（共用 18000 端口，互斥运行）
       ├─▶ coder  ──▶ ③ docker compose --profile coder up -d（代码/工具调用）
       └─▶ qwen38 ──▶ ③ docker compose --profile qwen38 up -d（通用 27B）
       │
       ▼
    [检查] curl :18000/v1/models 出现模型名（MoE 首载约 3-4 分钟）
       │
       ▼
    [使用] WebUI :8080 对话
       │
       ▼
    [要换模型？]
       ├─▶ 是 ──▶ ④ 停当前（--profile X down）→ 启另一个 → 刷新 WebUI
       └─▶ 否 ──▶ 继续使用
       │
       ▼
    [停止] ⑤ 全部 profile down（可回滚 Ollama）
```


| Profile  | 模型                      | 对外名            | 说明                         |
| -------- | ------------------------- | ----------------- | ---------------------------- |
| `coder`  | Qwen3-Coder-30B-A3B (MoE) | `qwen3-coder-30b` | 代码/工具调用，ModelScope 源 |
| `qwen38` | Qwen3.8-27B (稠密)        | `qwen38-27b`      | HuggingFace 源               |

```bash
cd /root/edge/infer/vllm

# ① 准备（pull_policy: never，须先拉镜像再下模型）
bash pull-engine.sh                       # 引擎镜像 v0.27.1（国内源直连，见 docs/note-vllm.md 260823）
bash download-model.sh coder              # ~16.8GB，ModelScope 国内源
bash download-model.sh qwen38             # ~19.6GB，HuggingFace 走代理

# ② 互斥：停 Ollama 释放 GPU/8080
cd /root/deAI/infra/ollama && docker compose down
cd /root/edge/infer/vllm

# ③ 启动（选一个 profile，见表）
docker compose --profile coder up -d      # 或 --profile qwen38 up -d

# ④ 切换（共用 18000 须互斥；裸 down 只关 WebUI）
docker compose --profile coder down       # 先停当前
docker compose --profile qwen38 up -d     # 切到 Qwen3.8；切回用 coder
# 切换后 WebUI 刷新即见新模型名

# ⑤ 停止 / 回滚
docker compose --profile coder --profile qwen38 down
cd /root/deAI/infra/ollama && docker compose up -d   # 回滚 Ollama
```
