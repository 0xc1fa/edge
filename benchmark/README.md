# Benchmark 评测

对 vLLM 推理服务做**简单可对比**的性能评测（固定用例 + 流式计时）。

## 用法

```bash
# 服务须已就绪（:18000）
bash simple-test.sh qwen38-27b 128        # <模型> <max_tokens>
bash simple-test.sh qwen3-coder-30b 128   # 换模型对比（先切 profile）
```

- 固定测试用例：`test-cases.txt`（3 条短题，可自行增删）
- 指标：**TTFT**（首 token 延迟，ms）、**输出吞吐**（tok/s）
- 结果：终端汇总 + `results/<时间戳>/case-*.jsonl` 明细

## 换条件对比

改条件（模型 / max_tokens / 引擎 / GPU 负载）后重跑同一脚本，对比汇总即可。

完整结论见 `docs/page-benchmark.md`。
