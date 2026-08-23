# vLLM 推理服务

基于 vLLM 的高性能 LLM 推理部署，双 RTX 4090，替代 Ollama 作为推理后端。当前引擎 **v0.27.1**（Qwen3.8 MTP 推测解码）。

通过 Docker Compose **profiles** 管理多个模型，同一时间只运行一个，按需切换。

> 镜像拉取走国内源（华为云 SWR 同步站），已配置 dockerd 直连绕过代理，见 `docs/note-vllm.md`「260823」。

## 可用模型


| Profile  | 模型                | 类型             | 大小    | 来源        | 对外名            |
| -------- | ------------------- | ---------------- | ------- | ----------- | ----------------- |
| `coder`  | Qwen3-Coder-30B-A3B | MoE (30B/3B)     | 16.8 GB | ModelScope  | `qwen3-coder-30b` |
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

## compose 关键参数

**`--speculative-config`**（qwen38 专属，需 v0.27.1+）

```json
{"method": "mtp", "num_speculative_tokens": 2}
```

Qwen3.8 MTP 推测解码：单次前向验证多 token，零额外模型下载

**`--kv-cache-dtype=fp8`**
KV cache 显存减半 → 容量翻倍（506K tokens / 并发 15.4x），读写带宽减半

**`--max-num-batched-tokens`** `8192`(coder) / `4096`(qwen38)
prefill 批处理上限，长 prompt 首字更快

**`--enable-prefix-caching`**
前缀缓存，多轮对话命中更快

**`--max-num-seqs`** `4`
最大并发序列数

> 注意：qwen38 的 `--tool-call-parser=qwen3_xml`（0.26.0 起旧名 `qwen3` 会启动崩溃），多模态模型还需 `preprocessor_config.json`，详见下方归档。

## 镜像拉取

官方 docker hub 镜像经本机代理链路不稳（大 layer 反复断流），已配置 **dockerd 直连国内源**：

```bash
# 华为云 SWR 同步站（等价于 docker.io 官方镜像）
docker pull swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/vllm/vllm-openai:v0.27.1
docker tag swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/vllm/vllm-openai:v0.27.1 vllm/vllm-openai:v0.27.1
```

依赖的环境配置（永久生效）：`/etc/systemd/system/docker.service.d/http-proxy.conf` 的 `NO_PROXY` 含国内源域名；systemd-resolved 网卡 DNS 为 `223.5.5.5`/`119.29.29.29`。

## 相关归档

推理引擎的决策与踩坑记录见 [note-infer.md](../../docs/note-infer.md)，vLLM 专项（Qwen 解析器/多模态 preprocessor、MTP 升级与镜像拉取排障）见 [note-vllm.md](../../docs/note-vllm.md)
