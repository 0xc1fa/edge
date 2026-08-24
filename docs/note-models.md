# 模型

## 🚀 Qwen  选型/下载/部署/调优 260824

> 背景：双 4090 + vLLM 0.27.1，新增 Qwen3.6 系列（MoE 35B 总参/3B 激活、原生多模态、Gated DeltaNet 混合注意力、128K 上下文）。用户目标链：脚本体检找问题 → 版本调研选型 → 直连下载 → compose 部署 → 显存调优 → 思考模式修复 → 上下文提升。

**① 版本选型：硬件条件驱动**

```text
[硬件条件 = 决策起点]
  2 × RTX 4090，24GB/卡
  ├─▶ 后台算力任务占用 ~1.4GB/卡
  └─▶ 每卡实际可用 ≈ 22.6GB（还要留 KV cache + CUDA graph）
  引擎 vLLM 0.27.1，TP2 → 权重平分两卡
```

```text
[版本筛选：按硬件预算逐一淘汰]
  │
  ├─▶ Qwen3.6-Plus ────── ✖ 闭源 API，本地不可用
  ├─▶ Qwen3.6-27B（稠密）─ ✖ 全参激活推理慢（qwen38 同档实测仅 ~28 tok/s）
  ├─▶ 35B-A3B-FP8 37.5GB ── ✖ TP2 每卡 18.75GB，余 ~3.9GB 不够 KV/graph
  ├─▶ 35B-A3B-NVFP4 23.4GB ── ✖ 显存够，但 4090(Ada) 无原生指令，仅 weight-only
  └─▶ [采纳] 35B-A3B-AWQ 25.5GB（W4A16）
        ├─▶ TP2 每卡 ~12.7GB，余量充足 ✅
        ├─▶ 4090 Marlin 内核最成熟 ✅
        └─▶ 含多模态 preprocessor ✅
```

**关键认知**：

- 选型第一问不是"哪个量化更先进"，而是"这张卡装得下、跑得动吗"——显存容量是硬约束，直接淘汰没资格的版本
- FP8 纸面最优（4090 有原生 FP8 指令），但 37.5GB TP2 后每卡 18.75GB，只剩 ~3.9GB 给 KV/graph，几乎不可用 → 被硬件淘汰，与精度无关
- NVFP4 显存够，但它是 Blackwell 格式，4090（Ada, sm_89）无原生指令只能反量化跑 → 位宽收益白给
- AWQ W4A16 是唯一"显存装得下 + 4090 Marlin 内核成熟 + 生态完善"三者全满足者；下载量 34:1 只反映热度，不改硬件约束
- MTP 默认关闭：260823 实测计算受限环境下为负优化（并发 -27%~-40%）

**② 下载：脚本 bug 修复 + 直连 ModelScope**

```text
[download-model.sh 体检修复]
  │
  ├─▶ bug：模型路径带 data/ 前缀 ──▶ 与 compose 挂载不一致
  │       └─▶ 删除 data/（MODEL_DIR 及三处模型路径）
  ├─▶ 代理：默认走 clash TUN ──▶ 强制直连
  │       └─▶ 脚本顶部 unset 全部代理变量 + curl --noproxy '*'
  ├─▶ 日志目录 ──▶ LOG_DIR="${SCRIPT_DIR}/../logs"
  └─▶ 完整性校验与远程大小探测
        ├─▶ ModelScope repo 文件列表 API（单文件 HEAD 不可靠）
        └─▶ FILES 清单 21 个文件（含 chat_template.jinja /
             preprocessor_config / video_preprocessor_config / 9 个 safetensors）
```

**关键认知**：

- 下载脚本与 compose 挂载路径必须一致（脚本曾带 `data/` 前缀导致路径错位）
- 直连需"脚本层 unset + 命令层 --noproxy"双重保障；ModelScope 走国内源天然快
- 多模态模型缺 `preprocessor_config.json` 会启动崩溃（qwen38 已有教训，Qwen3.6 同理补全）

**③ 部署与显存核算（gpu-memory-utilization=0.90，等效 0.876）**


| 项目                              | 每卡占用         |
| --------------------------------- | ---------------- |
| 后台算力任务                      | ~1.4GB           |
| 权重（AWQ 25.5GB / TP2）          | ~12.7GB          |
| KV cache（FP8，1,152,837 tokens） | ~8.5GB           |
| CUDA graph                        | ~1.3GB           |
| 合计 / 余量                       | ~21.9GB / ~0.7GB |

**关键认知**：

- 架构决定 KV 省显存：40 层仅 10 层 full-attention（`full_attention_interval=4`）、KV heads=2、head_dim=256、KV 走 FP8 → 每 token KV 仅 ~15.5KB
- 因此 32768 → 65536 上下文无显存压力（KV 容量支持），余量合理偏保守

**④ 思考模式：文本式思考的折叠难题**

```text
[前端"循环显示"排查]
  │
  ▼
现象：问问题后前端反复显示同一段思考文本
  │
  ▼
根因：tclf90 AWQ 输出 "Here's a thinking process..." 文本式思考
  │     无 <think> 标签 → 思考混入 content → 前端全文展示像循环
  │
  ├─▶ 尝试 --reasoning-parser=qwen3 ✖ 整个输出被当思考 → content 为空
  └─▶ [采纳] --default-chat-template-kwargs={"enable_thinking": false}
        └─▶ 默认直接回答；按请求传 chat_template_kwargs 可临时开启
```

**关键认知**：

- 思考折叠的前提 = 模型输出带 `<think>` 标签 + vLLM reasoning parser；tclf90 AWQ 版是"文本式思考"（无标签），严格折叠不可实现
- 260824 用户决定改为默认开启思考（`enable_thinking: true`），思考与答案同混 content，前端不折叠；需要直接回答时按请求传参关闭

**⑤ max-model-len 32768 → 65536 + 开启思考模式**

- API 验证 `max_model_len=65536` ✅；思考模式默认开启 ✅（测试请求输出以 `Here's a thinking process:` 开头）
- 显存：每卡 23050 / 24564 MiB，余 ~1GB，无 OOM
- 服务就绪 ~12 分钟（权重加载 475s + torch.compile 92s + warmup 90s）

**待解卡点**：

- 启动慢：`torch_compile_cache` 未命中，每次重启重新编译（候选：`--enforce-eager` 或排查缓存目录，需用户确认）
