# vLLM 引擎

## 🚀 优化模型编译缓存 260824

> 启动慢——模型编译缓存（`torch_compile_cache`）未命中，每次重启重新编译（候选 `--enforce-eager` 或排查缓存目录）。已解决并验证。

**现象**：切换模型（`down/up`）或重建容器后，vLLM 每次都要完整重跑 torch.compile（~91s），加上权重加载 474s + warmup 90s，启动总时长 ~13min。正常应只首次编译、后续秒级加载缓存。

**根因：缓存落在容器"可写层"，随容器销毁**：

```
docker compose up  →  镜像层(只读) + 新建空可写层 ──▶ 容器开始运行
                        │
                        └─ vLLM 写入 /root/.cache/vllm/torch_compile_cache
                           → 落在这层"可写层"里

docker compose down → 删除容器 = 镜像层保留, 但【可写层整个扔掉】
docker compose up   → 又新建一个空的可写层 → 缓存没了, 重新编译
```

关键理解：**不是"必须映射宿主机"，而是"必须放在容器生命周期之外"**。`docker restart` 不删容器所以缓存能保留，但切换模型用的是 `down/up`（删容器重建），可写层被清空：


| 操作                        | 容器保留？ | 可写层保留？ | torch_compile_cache 还在吗 |
| --------------------------- | ---------- | ------------ | -------------------------- |
| `docker restart`            | ✅         | ✅           | ✅ 秒起                    |
| `docker compose down && up` | ❌ 重建    | ❌ 清空      | ❌ 重编译                  |

**方案**（compose `x-vllm-common` 公共锚点 + 文件末尾 volumes 段，命名卷生命周期独立于容器）：

```yaml
  volumes:
    - /root/edge/models:/models:ro
    - vllm_cache:/root/.cache/vllm # torch.compile 缓存持久化: down/up 后不重编译

volumes:
  vllm_cache: # 命名卷,生命周期独立于容器
```

**验证**（260824 实测，`down/up` 重建容器 + 卷保留）：


| 指标                 | 第一次（空卷）                            | 第二次（缓存命中）                                    | 变化                 |
| -------------------- | ----------------------------------------- | ----------------------------------------------------- | -------------------- |
| compile 日志         | `saved AOT compiled function`（编译保存） | `Directly load AOT compilation from path`（直接加载） | 编译 → 加载         |
| torch.compile 耗时   | **91.18 s**                               | **8.51 s**                                            | **-91%**             |
| 容器启动 → API 就绪 | ~13 min                                   | ~9 min                                                | 省 ~4 min            |
| 模型权重加载         | 474 s                                     | 474 s                                                 | 无变化（权重不缓存） |

第二次启动日志铁证（直接命中命名卷里的缓存）：

```text
13:43:06 Directly load AOT compilation from path /root/.cache/vllm/torch_compile_cache/.../rank_0_0/model
13:43:06 Directly load AOT compilation from path /root/.cache/vllm/torch_compile_cache/.../rank_1_0/model
13:43:06 torch.compile took 8.51 s in total
```

产物 447MB 写入卷 `vllm_vllm_cache`，跨容器重建存活。

**关键认知**：

- 命名卷与宿主机 bind mount 本质相同：存储生命周期独立于容器，都能跨容器重建存活。bind mount 排障直观（宿主机 `ls` 直接看），命名卷更"纯净"（Docker 托管 `/var/lib/docker/volumes/`）——本方案选命名卷
- 缓存 key 由启动参数/模型/引擎版本/GPU 架构决定，**改参数仍会失效重编译** → 调参集中一次重启，别改一个参数重启一次
- `--enforce-eager` 不划算：只省 ~90s 启动，但永久失去运行时算子图优化（tok/s 下降）
- vLLM 镜像"膨胀"与运行时 compile 缓存无关：压缩 ~9GB ≠ 落盘 ~30GB（CUDA 二进制解压 ~3.4x），多版本堆积才是磁盘大头

---

## 🚀 Qwen3.6 思考模式修复 260824

> 背景：双 4090 + vLLM 0.27.1 + Qwen3.6-35B-A3B-AWQ（tclf90），Open WebUI v0.10.2。用户提问后前端反复显示思考文本、最后几段不断循环，只有"结束对话"才能终止。目标：**保留思考模式且正常响应**。最终定性：**模型无缺陷，真因是 vLLM 配置缺失**（缺 `--reasoning-parser=qwen3`）。本节为"先错后纠"完整演进：第一阶段旧方向（v0.26.0 时代）被推翻，第二阶段 260824 找到真根源。

```text
[思考模式折叠 · 完整演进（先错后纠）]
  │
  ▼
【第一阶段：初遇问题（v0.26.0 时代，旧方向，已被推翻）】
  现象：问问题后前端反复显示同一段思考文本
    │
    ├─▶ 分析：tclf90 AWQ 输出 "Here's a thinking process..." 文本式思考
    │    尝试 --reasoning-parser=qwen3 ✖ 整个输出被当思考 → content 为空
    │    归因：模型无 <think> 标签 → "严格折叠不可实现"
    │
    └─▶ [采纳] --default-chat-template-kwargs={"enable_thinking": false}
          └─▶ 默认直接回答；按请求传 chat_template_kwargs 可临时开启
          （隐藏代价：放弃思考模式 = 没解决真实需求）
  │
  ▼
【第二阶段：纠偏（260824）】
  触发：用户明确要"思考模式且正常响应"，绕开方案不满足需求
    │
    ▼ ① 读模型文件 chat_template.jinja
    │    有完整 <think> / reasoning_content / enable_thinking 逻辑
    │    → 模型文件层面支持标准思考格式，旧结论"无标签"存疑
    ▼ ② 读官方 README（模型仓库自带）
    │    官方明确推荐启动参数 --reasoning-parser qwen3
    │    → 直接否定旧注释"无法用 reasoning-parser 拆分"
    ▼ ③ 版本实证（容器内查 vLLM 0.27.1 源码）
    │    vllm/reasoning/qwen3_engine_reasoning_parser.py（注册名 qwen3）
    │    但 compose 从未启用 → 思考全文混入 content
    │    → 根因锁定：vLLM 配置缺失，非模型缺陷
    ▼ ④ DB 铁证（webui.db chat_message）
    │    assistant content="" 但 output 53KB，消息 status 永久卡 in_progress
    │    （未收到 finish_reason/[DONE]）→ 前端对未完成消息反复重放 = "循环"假象
    ▼ ⑤ 修复 ── compose 加 --reasoning-parser=qwen3（与 --tool-call-parser=qwen3_xml 共存）
    ▼ ⑥ 验证 ── 思考拆到 reasoning 字段、content 仅正式回答、流正常 [DONE]
    └─▶ 完成：思考可折叠、不再循环、不再卡 in_progress
```

**排查取证细节（按 ①~⑥ 展开，命令可复用）**：

**① 读 chat_template.jinja——模型文件层面"输出思考"是预期设计**

```bash
grep -n "think\|reasoning" /root/edge/models/Qwen3.6-35B-A3B-AWQ/chat_template.jinja | head
# <think> 起止、reasoning_content 字段、enable_thinking 分支齐全
# → 模型"输出 <think> 思考"是官方设计，第一阶段"无标签不可拆分"的结论存疑
```

**② 读官方 README——启动参数白纸黑字**

```bash
grep -n "reasoning-parser" /root/edge/models/Qwen3.6-35B-A3B-AWQ/README.md
# 官方示例直接给 --reasoning-parser qwen3
# → 与旧笔记"无法用 reasoning-parser 拆分"直接冲突，以官方为准
```

**③ 版本实证——容器内查引擎源码（现象-猜测不可靠）**

```bash
docker exec vllm-qwen36 ls /opt/vllm/vllm/reasoning/ | grep qwen3
# qwen3_engine_reasoning_parser.py ← v0.27.1 已回归注册名 qwen3
# （v0.26.0 该解析器拆分后无旧名，参数校验即崩——旧失败结论随版本作废，见 260822 条目）
docker exec vllm-qwen36 grep -n "qwen3" /opt/vllm/vllm/reasoning/parser_manager.py | head
```

**④ DB 铁证——"循环"是前端对未完成消息的重放**

```bash
docker cp vllm-webui:/app/backend/data/webui.db /tmp/webui.db
sqlite3 /tmp/webui.db "SELECT status, length(content), length(output) FROM chat_message WHERE role='assistant';"
# status=in_progress | content 长度 0 | output ≈53KB
# → 该条消息从未收到完成事件(finish_reason/[DONE])，前端对 in_progress 消息反复重放 = "循环"假象
```

**⑤ 修复——compose 一行参数 + 日志确认生效**

```diff
  - --tool-call-parser=qwen3_xml
+ - --reasoning-parser=qwen3   # 与 tool-call-parser 相互独立，可共存
```

```bash
docker logs vllm-qwen36 2>&1 | grep -i "reasoning"
# 关键两行:
#   ApiServer: ... reasoning_parser='qwen3'   ← 参数被接受生效
#   EngineCore: ... parser_manager 加载 qwen3 解析器 + Application startup complete
```

**⑥ 验证闭环——API 修复前后对照（同一请求）**

```bash
# 非流式: 修复前 content=整段思考全文(无 reasoning 字段); 修复后:
curl -s http://127.0.0.1:18000/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.6-35b","messages":[{"role":"user","content":"1+1=? 简单回答"}],"stream":false}' \
  | jq '{reasoning_len: (.message.reasoning|length), content_len: (.message.content|length), finish_reason: .finish_reason}'
# → reasoning=思考(1686 字符) | content=正式回答(31 字符) | finish_reason=stop

# 流式: 思考与回答分块送达、末尾正常 [DONE]（warmup 3 次后: reasoning 块 3293 字符 / content 块 94 字符）
curl -sN http://127.0.0.1:18000/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.6-35b","messages":[{"role":"user","content":"1+1=? 简单回答"}],"stream":true}' | tail -3
# data: [DONE]
# Open WebUI 侧: middleware.py:2967 兼容 delta.get('reasoning_content') or delta.get('reasoning')，
#   日志无解析错误 → 前后端字段天然匹配，渲染链路无障碍
```

**隐藏坑（后果链）**：

- `--reasoning-parser` 缺失的连锁反应：思考全文混入 content → 流式不产生正常 `finish_reason`/`[DONE]` → Open WebUI 消息卡 `in_progress` → 前端反复重放最后几段 = "循环"假象。**根子在引擎侧，别在前端找**
- **旧笔记会"冻结"排查方向**：260822 条目"qwen3 旧名崩 + 无法拆分"是 v0.26.0 的结论，照抄就会绕开（关思考）。升级版本后同一结论必须重新验证——本次正是靠"读源码"推翻旧结论

**重启成本构成**（改启动参数为什么等 15 分钟）：

```text
权重加载 ~475s + torch.compile 91.29s + Triton kernel warmup（GPU 99%）
→ 合计约 15 分钟，一次性成本
产物缓存于容器 /root/.cache/vllm/torch_compile_cache
→ 容器不重建则后续秒起；改任何启动参数都会使缓存 key 失效触发重编译
→ 教训：调参集中一次重启，别改一个参数重启一次
```

**关键认知**：

- **思考折叠的两前提**：模型输出 `<think>` 标签 + vLLM 启用 `--reasoning-parser`。tclf90 AWQ 版**实际带 `<think>` 标签**，第一阶段"文本式思考不可拆分"的结论只适用于 v0.26.0——该版本解析器已拆分为 `qwen3_coder`/`qwen3_xml`，`qwen3` 旧名参数校验即崩（见 260822 条目）
- **纠偏方法：现象-猜测不可靠，用"模型文件 + 官方文档 + 引擎源码"三重实证**。第一阶段错在：把 v0.26.0 失败经验当永久结论、只看输出现象（无标签）就归因模型缺陷、用"关思考"绕开而非解决需求；升级版本后必须逐版重新验证
- **vLLM 0.27.1 字段名是 `reasoning`**（非 OpenAI 的 `reasoning_content`），Open WebUI v0.10.2 `middleware.py:2967` 明确支持 `delta.get('reasoning')`——前后端字段天然匹配
- **`--reasoning-parser` 与 `--tool-call-parser` 相互独立可共存**：工具解析与思考拆分解耦，启用互不干扰
- **消息卡 in_progress 的机制**：Open WebUI 未收到完成事件（`finish_reason`/`[DONE]`）时消息永远停在生成中状态，前端反复重放；根子在引擎侧思考文本不结束，非前端 bug
- **修复价值排序**：解决真实需求（思考模式正常响应）> 绕开（关思考）；绕开会掩盖根源，下次同问题复发
- **重启成本**：改启动参数后 torch.compile 缓存失效需重编译（约 15 分钟，权重加载 475s + compile 92s + warmup），产物缓存于容器 `/root/.cache/vllm`，容器不重建则后续秒起

---

## 🚀 测试 MTP 优化方案  260823

```text
[升级 v0.27.1 + MTP（用户"重建升级"）]
  │
  ▼
┌─────────────────────────────────────────────┐
│ ① 重建容器 v0.27.1 + MTP                     │
│    [卡点] 启动即崩: Insufficient space in     │
│    /dev/shm: 160 MiB required, 64 MiB free   │
└─────────────────────────────────────────────┘
  │  修复: compose x-vllm-common 加 shm_size: 2g
  ▼
┌─────────────────────────────────────────────┐
│ ② 初测: 21.3 / 27.5 / 24.2 tok/s（波动大）   │
│    未达预期（后台参考 +39%）                  │
│    用户:"MTP 没发挥出来,需要调研并测试优化"    │
└─────────────────────────────────────────────┘
  │
  ▼
┌─────────────────────────────────────────────┐
│ ③ 对照实验（同引擎 0.27.1 仅开关 MTP）        │
│    串行 n=2: +4% | 并发4 n=2: -40% | n=1: -27%│
│    结论: 满载并发下 MTP 负优化                │
└─────────────────────────────────────────────┘
  │  用户:"compose 最好把 v0.26.0 和加 MTP 分开"
  ▼
┌─────────────────────────────────────────────┐
│ ④ compose 拆双 profile（同引擎,仅推测开关）    │
│    qwen38 无MTP(默认) / qwen38-mtp(MTP n=2)   │
└─────────────────────────────────────────────┘
```

**升级排障**：

- **新坑 `/dev/shm` 不足**：0.27.1 的 MTP 多进程 `shm_broadcast` 需共享内存，Docker 默认 64MiB 不够（0.26.0 无此需求，升级专属坑）。实测容器内广播缓冲 4 块共 ~668MiB（25/161/241 MiB ×2），用途：草稿 tokens/layout/metadata 从 EngineCore 广播到 TP worker
- 验证通过项：`qwen3_xml` 解析器无 KeyError ✅；`Qwen3_5MTP` 架构加载成功（AWQ 主模型 + MTP 头兼容）✅；显存无 OOM（模型加载 9.42 GiB/卡，KV cache 375K tokens，并发 11.45x）✅

**① 首轮请求含 JIT/编译开销，基准测试必须剔除或 warmup**

- 现象：升级后首测 21.3 tok/s、case-1 TTFT 2745ms 明显异常，第 2 轮即 27.5 tok/s
- 根因：首个请求触发 torch.compile（Dynamo 转换 ~18s）+ Triton kernel warmup，仅发生一次
- 方法：先发 1 个 warmup 请求，或取第 2 轮起稳态数据（本次 3 轮取 2、3 轮均值）

**② GPU 满载下噪声大，多轮取稳态**

- 现象：同配置 3 次串行 21.3/27.5/24.2 tok/s，波动 ±15%
- 根因：后台计算任务（GPU 99%）动态抢 SM，时延随负载波动
- 方法：每配置 ≥2 轮取均值；结论差距小于噪声（~10%）时不可信

**③ compose 双 profile 共用 18000 端口，不能同时 up**

- 现象：qwen38 / qwen38-mtp 先后 up，后者端口占用启动失败（预期行为）
- 方法：切换先 `docker compose --profile <旧> down` 再 `up -d <新>`；每次切换重载 27B 模型约 5-8 分钟
- 注：`docker compose config --quiet` 不做 profile 组合校验，直接 up 验证即可

**④ 评测方法论沉淀**

- 串行单请求测延迟/单流吞吐（TTFT、tok/s）；并发 N 请求测聚合吞吐（服务真实能力）
- 变量隔离：对照实验须同引擎同参数（0.27.1 无MTP vs 0.27.1+MTP），排除版本/参数差异
- 聚合吞吐才是服务真实能力，单流数字严重低估（对比数据见报告）

**⑤ max-num-batched-tokens 与推测解码槽位**

- vLLM 源码：`max_num_scheduled_tokens = max_num_batched_tokens - (n-1)×max_num_seqs`
- 4096 时推测只剩 4092 < 8192 → 告警 may lead to suboptimal performance
- 提到 12288 消除告警（TTFT 改善数据见报告）

## 🚀 vLLM 加速优化方案 260822

> 背景：双 4090 与后台算力任务共存（GPU 99% 满载），qwen38 生成仅 ~28 tok/s。用户目标：**不影响后台算力服务**的前提下最大化 vLLM 响应速度。

```text
[qwen38 与后台算力任务共存 28 tok/s，用户：怎样更快且不关后台服务]
  │
  ▼
① vLLM 侧降占用：fp8 KV cache + max-num-batched-tokens + prefix caching
  │   KV 容量 506K tokens（并发 15.4x）、长 prompt 首字更快
  │   实测仍 ~28 tok/s —— vLLM 侧榨不出数量级
  ▼
② 瓶颈确认：后台算力任务抢算力（SM/带宽），非显存
  │   后台算力程序无限速参数（-d 只能选卡）→ 算力侧无解
  ▼
③ 用户：研究两条推文（MTP：2×4090 300+ tok/s；DFlash：2.2×）
  │   调研结论 ↓ 三种推测解码方案权衡
  │
  ├─▶ MTP：官方自带 MTP 头，零额外下载，需 vLLM ≥0.27.1
  │        （v0.26.0 只覆盖 Inkling/Gemma4，不支持 Qwen3.8）
  ├─▶ DFlash2：1.92B 草稿模型，2.7~3.4x，但与 MTP 互斥 + 显存 +4GB/卡
  │        后台算力满载下草稿推理需排队，收益打折
  └─▶ INT8-W8A16-MTP：质量最优（KLD 0.000894），但并发 bug ✖
            ≥2 并发请求引擎崩溃（cudaErrorIllegalAddress），Open WebUI 必崩
  ▼
④ 决策：升级 vLLM 0.26.0 → 0.27.1 + 开启 MTP（用户："升级 vLLM 并开启 MTP"）
  │   compose 改动：image: v0.27.1 + --speculative-config={"method":"mtp","num_speculative_tokens":2}
  ▼
⑤ 卡点：镜像拉取（5.2GB）→ 见"镜像拉取排障"节
  ▼
⑥ 镜像就绪，容器未重建 —— 升级验证待执行
```

**三种推测解码方案权衡**：


| 方案           | 加速                                              | 代价                                                | 结论                   |
| -------------- | ------------------------------------------------- | --------------------------------------------------- | ---------------------- |
| **MTP**（选）  | 双 4090 实测 133→185 tok/s（+39%，接受率 65.7%） | 需 v0.27.1+；MTP=3 吞吐 +46% 但接受率 52.6%，取 2   | 零下载，先启用         |
| DFlash2        | 2.7~3.4x（1.92B 草稿）                            | 显存 +4GB/卡、与 MTP 互斥、后台算力满载下排队       | 等 vLLM 稳定支持后再测 |
| INT8-W8A16-MTP | 质量最优（KLD 0.000894，Top-1 一致率 99.36%）     | **并发 bug**：≥2 并发引擎崩溃，Open WebUI 多人必崩 | 不用                   |

**关键认知**：

- 提速先榨 vLLM 侧（量化 KV/带宽），榨不动再确认瓶颈归属（后台算力任务抢算力）
- 推测解码是"算力换延迟"：草稿/验证需要额外算力，后台算力任务满载下收益打折——MTP 优势在零成本（官方自带头）与减少串行 step
- 版本门槛卡死选型：0.26.0 不支持 Qwen3.8 MTP → 升级本身成了最大工程（见"镜像拉取排障"节）

---

## 🐛 vLLM 的 Qwen 解析器与多模态配置 260822

> 问题：vllm 镜像的两个服务都已经启动 为何 Open webui 还是看不到模型

```text
问题：vllm 与 webui 两容器都已启动，Open WebUI 看不到模型
  │
  ▼
① 容器状态：docker ps ────────── 卡点①：Up 7 seconds = restart 崩溃循环假象，服务未就绪
  │
  ▼
② API 探测：curl 18000/v1/models ── 卡点②：EXIT=56 连接重置，服务从未起来
  │
  ▼
③ 日志定位：docker logs vllm-qwen38 ── 卡点③：KeyError: parser qwen3（0.26.0 已拆分，旧名必崩）
  │  修坑 1：改 --tool-call-parser=qwen3_xml → 重建
  ▼
④ 重建后新错误 ──────────────── 卡点④：ValueError 缺 preprocessor_config.json（多模态必需）
  │  修坑 2：代理补下载 + download-model.sh 清单补全
  ▼
⑤ 验证：vLLM 返回 qwen38-27b ✅，webui 仍空 ── 卡点⑤：判断为 webui 模型列表缓存
  │  重启 vllm-webui → 页面可见 qwen38-27b ✅
  ▼
完成
```

**① 看容器状态——先识别"Up 7 seconds"假象**

```bash
docker ps -a --format 'table {{.Names}}\t{{.Status}}'
# vllm-qwen38    Up 7 seconds    ← 刚被 restart 拉起，不是稳定运行
```

`restart: unless-stopped` 会让崩溃容器反复重启，Status 时间戳很短就是循环的破绽。

**② 直接打 API——确认服务是否真的就绪**

```bash
curl -s -m 10 http://127.0.0.1:18000/v1/models; echo "EXIT=$?"
# EXIT=56（连接重置），无任何 JSON —— API 从未就绪
```

Open WebUI 侧同样可见证据：`docker logs vllm-webui` 里 `Connection error: [Errno 104] Connection reset by peer`、`GET /openai/models/0 → 500`，模型列表自然为空。

**③ 修坑 1——日志抓 KeyError，改解析器名重建**

```bash
docker logs vllm-qwen38 --tail 80 2>&1 | tail -80
# KeyError: 'invalid tool call parser: qwen3 (chose from { ..., qwen3_coder, qwen3_xml, ... })'
```

vLLM 0.26.0 把 `qwen3` 解析器拆分为 `qwen3_coder`（Coder 系列）与 `qwen3_xml`（稠密系列），旧名在参数校验阶段（`validate_api_server_args`）即抛 KeyError 崩溃。修复 `infer/vllm/docker-compose.yml`：

```diff
- - --tool-call-parser=qwen3
+ - --tool-call-parser=qwen3_xml
```

```bash
cd /root/edge/infer/vllm && docker compose --profile qwen38 up -d --force-recreate vllm-qwen38
```

**④ 修坑 2——补 preprocessor_config.json 并进清单**

重建后日志不再报解析器错误，但暴露下一个崩溃点：

```bash
docker logs vllm-qwen38 | grep -i "preprocessor\|processor\|ValueError"
# ValueError: ...does not have a processor/preprocessor config...
# OSError: Can't load image processor for '/models/Qwen3.8-27B-W4A16-AWQ'
```

该模型架构是 `Qwen3_5ForConditionalGeneration`（多模态），启动要加载图片处理器配置；HF repo 有此文件但下载脚本没下它。修复两步：补下载（走 `127.0.0.1:7890` 代理）+ 修 `infer/vllm/download-model.sh` 的 `setup_qwen38()` FILES 清单：

```bash
curl -sfL -C - -L --http1.1 -x http://127.0.0.1:7890 \
  -o /root/edge/models/Qwen3.8-27B-W4A16-AWQ/preprocessor_config.json \
  https://huggingface.co/philbert440/Qwen3.8-27B-W4A16-AWQ/resolve/main/preprocessor_config.json
```

```diff
  setup_qwen38() {
    ...
    FILES=(
      "config.json"
      "generation_config.json"
      "model-mtp.safetensors"
      "model.safetensors"
      "model.safetensors.index.json"
+     "preprocessor_config.json" # Qwen3.8 是多模态模型,缺此文件 vLLM 启动崩溃
      "tokenizer.json"
      "tokenizer_config.json"
    )
  }
```

**⑤ 验证——vLLM 已好但 webui 仍空，判断为缓存并重启**

两个坑都修好后，vLLM 侧先确认：

```bash
curl -s http://127.0.0.1:18000/v1/models
# {"object":"list","data":[{"id":"qwen38-27b", ...}]}   ← 27B 双卡加载约 5 分钟
```

但此时 **Open WebUI 页面依然看不到模型**——全部已解决却仍不正常，当时的判断：

- 后端已好 ≠ webui 会重新拉取：Open WebUI 有**模型列表缓存**，之前 vLLM 崩溃期间拉取失败（webui 日志 `GET /openai/models/0 → 500` 发生在 vLLM 就绪前）的结果被记住，后端修好后不会自动刷新
- 解法：重启 webui 强制清缓存重新拉取

```bash
docker restart vllm-webui
# 重启后 /api/models 返回 qwen38-27b，页面刷新可见
```

**关键认知：**

- **容器 "Up" ≠ 服务就绪**：崩溃循环靠 Status 时间戳识别，先 curl API 再排查 WebUI。
- **后端修好 ≠ 前端自动恢复**：Open WebUI 模型列表有缓存，vLLM 崩溃期间的失败/空结果被记住，需重启 webui 强制重新拉取。
- **解析器名随 vLLM 版本演化**：0.26.0 中 `qwen3` → `qwen3_coder` / `qwen3_xml`，参数校验阶段即崩。
- **多模态模型必需 `preprocessor_config.json`**：`Qwen3_5ForConditionalGeneration` 等架构启动即加载，下载清单按 HF repo 文件列表补全，别只按惯用文件想当然。
