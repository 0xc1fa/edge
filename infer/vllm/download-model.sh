#!/usr/bin/env bash
# =============================================================================
# download-model.sh — 下载 vLLM 所需的量化模型权重
# =============================================================================
#
# 支持两个模型:
#   coder   → Qwen3-Coder-30B-A3B (AWQ, 来自 ModelScope, ~16.8GB)
#   qwen38  → Qwen3.8-27B 稠密     (AWQ, 来自 HuggingFace, ~19.6GB)
#
# 用法:
#   bash download-model.sh          # 默认下载 coder
#   bash download-model.sh coder    # 下载 Coder 模型 (ModelScope)
#   bash download-model.sh qwen38   # 下载 Qwen3.8 模型 (HuggingFace, 走代理)
#
# 环境变量:
#   DOWNLOAD_TIMEOUT  下载单个文件的超时秒数 (默认 3600)
#   HTTPS_PROXY       HuggingFace 代理地址   (默认 http://127.0.0.1:7890)
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DOWNLOAD_TIMEOUT="${DOWNLOAD_TIMEOUT:-14400}"

# =============================================================================
# 模型配置
# =============================================================================

setup_coder() {
  MODEL_DIR="${SCRIPT_DIR}/data/Qwen3-Coder-30B-A3B-Instruct-AWQ"
  SOURCE="ModelScope"
  MS_MODEL_ID="tclf90/Qwen3-Coder-30B-A3B-Instruct-AWQ"
  MS_REVISION="master"
  FILES=(
    "config.json"
    "configuration.json"
    "generation_config.json"
    "LICENSE"
    "merges.txt"
    "model-00001-of-00006.safetensors"
    "model-00002-of-00006.safetensors"
    "model-00003-of-00006.safetensors"
    "model-00004-of-00006.safetensors"
    "model-00005-of-00006.safetensors"
    "model-00006-of-00006.safetensors"
    "model.safetensors.index.json"
    "qwen3coder_tool_parser.py"
    "README.md"
    "tokenizer.json"
    "tokenizer_config.json"
    "vocab.json"
  )
}

setup_qwen38() {
  MODEL_DIR="${SCRIPT_DIR}/data/Qwen3.8-27B-W4A16-AWQ"
  SOURCE="HuggingFace"
  HF_MODEL_ID="philbert440/Qwen3.8-27B-W4A16-AWQ"
  HF_REVISION="main"
  HTTPS_PROXY="${HTTPS_PROXY:-http://127.0.0.1:7890}"
  FILES=(
    "config.json"
    "generation_config.json"
    "model-mtp.safetensors"
    "model.safetensors"
    "model.safetensors.index.json"
    "preprocessor_config.json" # Qwen3.8 是多模态模型,缺此文件 vLLM 启动崩溃
    "tokenizer.json"
    "tokenizer_config.json"
  )
}

# =============================================================================
# 下载函数
# =============================================================================

# 获取远端文件大小(字节);失败时输出空字符串
get_remote_size() {
  local file="$1"
  local url size
  case "${SOURCE}" in
    ModelScope)
      url="https://modelscope.cn/api/v1/models/${MS_MODEL_ID}/repo?Revision=${MS_REVISION}&FilePath=${file}"
      size=$(curl -sfL --max-time 30 -r 0-0 "${url}" -o /dev/null -D - \
        | awk 'BEGIN{IGNORECASE=1} /^content-range:/{split($2,a,"/"); v=a[2]} END{gsub(/\r/,"",v); print v}')
      ;;
    HuggingFace)
      url="https://huggingface.co/${HF_MODEL_ID}/resolve/${HF_REVISION}/${file}"
      size=$(curl -sfL --max-time 30 -r 0-0 -x "${HTTPS_PROXY}" "${url}" -o /dev/null -D - \
        | awk 'BEGIN{IGNORECASE=1} /^content-range:/{split($2,a,"/"); v=a[2]} END{gsub(/\r/,"",v); print v}')
      ;;
  esac
  echo "${size}"
}

download_from_modelscope() {
  local file="$1"
  local dest="${MODEL_DIR}/${file}"
  local url="https://modelscope.cn/api/v1/models/${MS_MODEL_ID}/repo?Revision=${MS_REVISION}&FilePath=${file}"

  if ! timeout "${DOWNLOAD_TIMEOUT}" curl -f -C - -L --http1.1 --retry 3 --retry-delay 3 --retry-all-errors \
       -o "${dest}" "${url}"; then
    echo "    ✗ ${file} 下载失败"
    return 1
  fi
}

download_from_huggingface() {
  local file="$1"
  local dest="${MODEL_DIR}/${file}"
  local url="https://huggingface.co/${HF_MODEL_ID}/resolve/${HF_REVISION}/${file}"

  # HuggingFace 需走代理;--http1.1 规避代理下 HTTP/2 流被取消(CANCEL)问题
  if ! timeout "${DOWNLOAD_TIMEOUT}" curl -f -C - -L --http1.1 --retry 5 --retry-delay 5 --retry-all-errors \
       -x "${HTTPS_PROXY}" \
       -o "${dest}" "${url}"; then
    echo "    ✗ ${file} 下载失败 (代理: ${HTTPS_PROXY})"
    return 1
  fi
}

# =============================================================================
# 主流程
# =============================================================================

MODEL_NAME="${1:-coder}"

case "${MODEL_NAME}" in
  coder)
    setup_coder
    ;;
  qwen38)
    setup_qwen38
    ;;
  *)
    echo "用法: bash download-model.sh [coder|qwen38]"
    echo "  coder   - Qwen3-Coder-30B-A3B (ModelScope, ~16.8GB)"
    echo "  qwen38  - Qwen3.8-27B 稠密     (HuggingFace, ~19.6GB)"
    exit 1
    ;;
esac

echo "==> 模型: ${MODEL_NAME} (来源: ${SOURCE})"
echo "==> 下载目录: ${MODEL_DIR}"
mkdir -p "${MODEL_DIR}"
echo

# ---- 逐文件下载(断点续传 + 幂等跳过) ----
for file in "${FILES[@]}"; do
  echo "==> 下载: ${file}"

  dest="${MODEL_DIR}/${file}"

  # 已存在且非空则跳过;大文件还需校验与远端大小一致,避免部分下载被误判完整
  if [ -f "${dest}" ] && [ -s "${dest}" ]; then
    if [[ "${file}" == *.safetensors ]]; then
      local_size=$(stat -c%s "${dest}")
      # 去除 \r，避免 HuggingFace Content-Range 头含回车导致整数比较失败
      remote_size=$(get_remote_size "${file}" | tr -d '\r')
      if [ -z "${remote_size}" ]; then
        echo "    已存在 (大小 $(du -h "${dest}" | cut -f1))，跳过 (远端大小无法获取)"
        continue
      fi
      if [ "${local_size}" -ge "${remote_size}" ]; then
        echo "    已存在且完整 (大小 $(du -h "${dest}" | cut -f1))，跳过"
        continue
      fi
      echo "    部分下载 (本地 ${local_size} / 远端 ${remote_size} B)，断点续传"
    else
      echo "    已存在 (大小 $(du -h "${dest}" | cut -f1))，跳过"
      continue
    fi
  fi

  case "${SOURCE}" in
    ModelScope)
      download_from_modelscope "${file}" || {
        echo "    ✗ ${file} 下载失败"
        FAILED=1
        continue
      }
      ;;
    HuggingFace)
      download_from_huggingface "${file}" || {
        echo "    ✗ ${file} 下载失败"
        FAILED=1
        continue
      }
      ;;
  esac

  echo "    完成 (大小 $(du -h "${dest}" | cut -f1))"
done

# ---- 失败检查 ----
if [ "${FAILED:-0}" = "1" ]; then
  echo
  echo "!!! 部分文件下载失败,请检查网络/代理后重试"
  exit 1
fi

# ---- 输出结果 ----
echo
echo "=== 下载完成 ==="
du -sh "${MODEL_DIR}"
echo
echo "=== 文件清单 ==="
ls -lh "${MODEL_DIR}"
