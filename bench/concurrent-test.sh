#!/usr/bin/env bash
# =============================================================================
# 并发固定用例评测（对比测试用）— 验证推测解码在并发下的收益
#
# 用法:
#   bash concurrent-test.sh <模型> [max_tokens] [并发数] [用例]
#     <模型>:     qwen3-coder-30b | qwen38-27b（默认 qwen38-27b）
#     max_tokens: 输出上限，默认 256
#     并发数:     默认 4（vLLM max-num-seqs=4）
#     用例:       默认 case-1（test-cases.txt 第 1 行）
#
# 指标:
#   - 聚合吞吐: 总输出 tok / 墙钟总耗时（N 请求同时发出 → 全部完成）
#   - 平均 TTFT / 最慢完成（并发调度公平性）
# =============================================================================
set -euo pipefail

MODEL="${1:-qwen38-27b}"
MAX_TOKENS="${2:-256}"
CONCURRENCY="${3:-4}"
CASE_ID="${4:-1}"
BASE_URL="http://127.0.0.1:18000/v1"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CASES_FILE="$SCRIPT_DIR/test-cases.txt"

if ! curl -sf -m 5 "$BASE_URL/models" >/dev/null; then
  echo "✖ 服务未就绪，请先启动对应模型"
  exit 1
fi

# 结果目录统一写到 ../logs/ 下，前缀 bench-，已被 .gitignore 忽略
TS=$(date +%y%m%d-%H%M%S)
RESULT_DIR="$SCRIPT_DIR/../logs/bench-$TS"
mkdir -p "$RESULT_DIR"

PROMPT=$(awk -F'|' -v id="$CASE_ID" '$1==id{print $2}' "$CASES_FILE")
if [ -z "$PROMPT" ]; then echo "✖ 用例 case-$CASE_ID 不存在"; exit 1; fi

echo "模型: $MODEL | max_tokens: $MAX_TOKENS | 并发: $CONCURRENCY | 用例: case-$CASE_ID | $(date '+%F %T')"

WALL_START=$(date +%s%N)

run_one() {
  local i="$1"
  local out_file="$RESULT_DIR/case-${i}.jsonl"
  local body
  body=$(jq -n --arg m "$MODEL" --arg p "$PROMPT" --argjson mt "$MAX_TOKENS" \
    '{model:$m, messages:[{role:"user",content:$p}], max_tokens:$mt, stream:true,
      stream_options:{include_usage:true}}')
  local start ttft_ns="" end
  start=$(date +%s%N)
  while IFS= read -r line; do
    if [ -z "$ttft_ns" ] && echo "$line" | grep -q '"delta":{"content"'; then
      ttft_ns=$(date +%s%N)
    fi
    echo "$line" >> "$out_file"
  done < <(curl -sN -H "Content-Type: application/json" -d "$body" \
    "$BASE_URL/chat/completions")
  end=$(date +%s%N)
  echo "$i|$ttft_ns|$end" >> "$RESULT_DIR/timing.txt"
}

for i in $(seq 1 "$CONCURRENCY"); do
  run_one "$i" &
done
wait
WALL_END=$(date +%s%N)

# 汇总
total_toks=0
for f in "$RESULT_DIR"/case-*.jsonl; do
  t=$(grep -o '"completion_tokens":[0-9]*' "$f" | tail -1 | cut -d: -f2)
  total_toks=$((total_toks + t))
done

awk -v ws="$WALL_START" -v we="$WALL_END" -v tt="$total_toks" -v c="$CONCURRENCY" '
BEGIN{
  wall_s = (we-ws)/1000000000
  printf "墙钟: %.1f s | 总输出 %d tok | 聚合吞吐: %.1f tok/s\n", wall_s, tt, tt/wall_s
}'
awk -F'|' -v ws="$WALL_START" '
{
  ttft_s=($2-ws)/1000000000; fin_s=($3-ws)/1000000000
  sum_ttft+=ttft_s; n++; if (fin_s>max_fin) max_fin=fin_s
}
END{
  printf "平均 TTFT: %.0f ms | 最慢完成: %.1f s | 请求数: %d\n", sum_ttft/n*1000, max_fin, n
}' "$RESULT_DIR/timing.txt"
echo "明细目录: $RESULT_DIR"
