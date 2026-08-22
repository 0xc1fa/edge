#!/usr/bin/env bash
# =============================================================================
# pull-engine.sh — 拉取 vLLM 推理引擎镜像（自动重试断点续传）
# =============================================================================
#
# 拉取目标: vllm/vllm-openai:latest
#   这是 vLLM 推理引擎的 Docker 镜像（含 CUDA/Python 环境），与模型权重无关。
#   模型权重通过 download-model.sh 单独下载，挂载进容器使用。
#
# 背景: 免费 Docker 镜像加速源不稳定，大 layer(如 9dc141b872c1, 5.2GB)
#       经常中途 EOF 中断。Docker 支持 layer 级断点续传——
#       已下载完成的 layer 会被保留(Already exists)，失败重试只补未完成的。
#
# 本脚本: 反复执行 docker pull 直到成功，配合 layer 续传，最终拉取完整镜像。
#
# 用法:
#   cd /root/deAI/infra/vllm && bash pull-engine.sh
#   # 可指定最大重试次数
#   MAX_ATTEMPTS=20 bash pull-engine.sh
# =============================================================================

set -uo pipefail

IMAGE="${1:-vllm/vllm-openai:latest}"
MAX_ATTEMPTS="${MAX_ATTEMPTS:-30}"
WAIT_BETWEEN="${WAIT_BETWEEN:-10}"   # 失败后等待秒数
LOG="/tmp/pull-vllm.log"

echo "==> 目标镜像: ${IMAGE}"
echo "==> 最大尝试: ${MAX_ATTEMPTS} 次，失败间隔 ${WAIT_BETWEEN}s"
echo "==> 日志: ${LOG}"
echo

for (( i=1; i<=MAX_ATTEMPTS; i++ )); do
  echo "===== 第 ${i}/${MAX_ATTEMPTS} 次尝试: $(date '+%H:%M:%S') ====="

  if docker pull "${IMAGE}"; then
    echo
    echo "🎉 镜像拉取成功！"
    docker images "${IMAGE}"
    exit 0
  fi

  code=$?
  echo "    第 ${i} 次失败 (exit $code)。已完成的 layer 已缓存，下次继续。"

  # 判断是否已经拉取完整（可能 pull 报错但镜像已可用）
  if docker image inspect "${IMAGE}" >/dev/null 2>&1; then
    echo "🎉 镜像已存在！"
    docker images "${IMAGE}"
    exit 0
  fi

  if [ "${i}" -lt "${MAX_ATTEMPTS}" ]; then
    echo "    等待 ${WAIT_BETWEEN}s 后重试..."
    sleep "${WAIT_BETWEEN}"
  fi
done

echo
echo "❌ 超过最大尝试次数 ${MAX_ATTEMPTS}，仍未成功。"
echo "   建议: 提高 MAX_ATTEMPTS，或检查网络/加速源。"
exit 1
