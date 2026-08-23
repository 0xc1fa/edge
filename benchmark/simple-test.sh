#!/usr/bin/env bash
# =============================================================================
# 简单固定用例评测（对比测试用）
#
# 用法:
#   bash simple-test.sh <模型> [max_tokens]
#     <模型>:     qwen3-coder-30b | qwen38-27b（默认 qwen38-27b）
#     max_tokens: 输出上限，默认 256
#
# 换条件对比：改模型 / max_tokens / 引擎后重跑同一脚本即可
# 例:
#   bash simple-test.sh qwen38-27b 256
#   bash simple-test.sh qwen3-coder-30b 256   # 先切换 profile
#
# 指标: TTFT（首个内容 token 延迟）、总耗时、输出 token 数、tok/s
# 结果: 终端汇总 + ../logs/benchmark-<时间戳>/case-*.jsonl 明细
# =============================================================================
set -euo pipefail

MODEL="${1:-qwen38-27b}"
MAX_TOKENS="${2:-256}"
BASE_URL="http://127.0.0.1:18000/v1"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CASES_FILE="$SCRIPT_DIR/test-cases.txt"

# ① 服务就绪检查
if ! curl -sf -m 5 "$BASE_URL/models" >/dev/null; then
  echo "✖ 服务未就绪，请先启动对应模型"
  exit 1
fi

# ② 结果目录（精确到秒，避免同分钟多次运行互相覆盖）
# 统一写到 ../logs/ 下，前缀 benchmark-，已被 .gitignore 忽略
TS=$(date +%y%m%d-%H%M%S)
RESULT_DIR="$SCRIPT_DIR/../logs/benchmark-$TS"
mkdir -p "$RESULT_DIR"

echo "模型: $MODEL | max_tokens: $MAX_TOKENS | $(date '+%F %T')"
printf '%-4s %-12s %-12s %-10s %-8s %s\n' "#" "TTFT(ms)" "总耗时(s)" "输出tok" "tok/s" "用例"

total_ttft=0; total_toks=0; total_time=0; n=0

while IFS='|' read -r case_id prompt; do
  [ -z "$case_id" ] && continue
  n=$((n+1))

  out_file="$RESULT_DIR/case-$case_id.jsonl"
  body=$(jq -n --arg m "$MODEL" --arg p "$prompt" --argjson mt "$MAX_TOKENS" \
    '{model:$m, messages:[{role:"user",content:$p}], max_tokens:$mt, stream:true,
      stream_options:{include_usage:true}}')

  start=$(date +%s%N)
  ttft_ns=""
  # 进程替换：while 在当前 shell 执行，避免子 shell 变量丢失
  while IFS= read -r line; do
    if [ -z "$ttft_ns" ] && echo "$line" | grep -q '"delta":{"content"'; then
      ttft_ns=$(date +%s%N)   # 首个内容 token 到达时刻 = TTFT
    fi
    echo "$line" >> "$out_file"
  done < <(curl -sN -H "Content-Type: application/json" -d "$body" \
    "$BASE_URL/chat/completions")
  end=$(date +%s%N)

  ttft_ms=$(awk -v s="$start" -v t="${ttft_ns:-$end}" 'BEGIN{printf "%.0f", (t-s)/1000000}')
  total_s=$(awk -v s="$start" -v e="$end" 'BEGIN{printf "%.2f", (e-s)/1000000000}')
  completion_tokens=$(grep -o '"completion_tokens":[0-9]*' "$out_file" | tail -1 | cut -d: -f2)
  completion_tokens=${completion_tokens:-0}
  tps=$(awk -v a="$total_s" -v t="$completion_tokens" 'BEGIN{printf "%.1f", (a>0)?t/a:0}')

  printf '%-4s %-12s %-12s %-10s %-8s %s\n' "$case_id" "$ttft_ms" "$total_s" "$completion_tokens" "$tps" "case-$case_id"
  total_ttft=$((total_ttft + ${ttft_ns:-0} - start))
  total_toks=$((total_toks + completion_tokens))
  total_time=$(awk -v a="$total_time" -v b="$total_s" 'BEGIN{print a+b}')
done < "$CASES_FILE"

echo "---- 汇总 ----"
awk -v t="$total_ttft" -v n="$n" 'BEGIN{ printf "平均 TTFT: %.0f ms\n", t/1000000/n }'
awk -v tt="$total_toks" -v tm="$total_time" 'BEGIN{ if(tm>0) printf "总输出 %d tok / 总耗时 %.1f s = %.1f tok/s\n", tt, tm, tt/tm }'
echo "明细目录: $RESULT_DIR"
