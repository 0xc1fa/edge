# vLLM 推理服务

基于 vLLM 的高性能 LLM 推理部署，双 RTX 4090，替代 Ollama 作为推理后端。

通过 Docker Compose **profiles** 管理多个模型，同一时间只运行一个，按需切换。

## 可用模型


| Profile  | 模型                | 类型            | 大小    | 来源        | 对外名            |
| -------- | ------------------- | --------------- | ------- | ----------- | ----------------- |
| `coder`  | Qwen3-Coder-30B-A3B | MoE (30B/3B)    | 16.8 GB | ModelScope  | `qwen3-coder-30b` |
| `qwen38` | Qwen3.8-27B         | 稠密 27B·多模态 | 19.6 GB | HuggingFace | `qwen38-27b`      |

## 目录结构

```
vllm/
├── docker-compose.yml     # 服务编排（vLLM × 2 profiles + Open WebUI）
├── download-model.sh      # 下载模型（支持 coder / qwen38，存到 ../models）
├── pull-engine.sh         # 拉取 vLLM 推理引擎镜像（自动重试断点续传）
└── README.md              # 本文档

models/                    # 模型权重统一目录（上级目录，只读挂载到容器 /models）
```

## 架构

```
浏览器 ──► Open WebUI (:8080)
              │  OpenAI 协议 (/v1)
              ▼
          vLLM (:18000)              ← profiles 决定加载哪个模型
              │  TP=2 双卡并行
              ▼
      2 × RTX 4090 (48GB)
```

- **vLLM**：OpenAI 兼容推理服务，通过 profiles 按需加载不同模型
- **Open WebUI**：前端，通过 OpenAI 协议连接 vLLM（非 Ollama 协议）
- 两个模型共用 18000 端口，同一时间只有一个在运行，切换后 WebUI 刷新即可

## 与 Ollama 的关系

- **互斥切换**：vLLM 与 Ollama 不同时运行（共享 GPU/端口 8080）
- 两者模型格式不通用：Ollama 用 GGUF，vLLM 用 safetensors(AWQ)

## 相关归档

推理引擎的决策与踩坑记录见 [note-infer.md](../../docs/note-infer.md)（vLLM 模型下载/启动参数、多模态 preprocessor_config.json 等）
