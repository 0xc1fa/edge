# vLLM 引擎

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
