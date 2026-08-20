#!/usr/bin/env bash
# =============================================================================
# git-config.sh - edge 仓库 Git 配置向导
# =============================================================================
# 分 3 步: 初始化仓库 -> 提交身份 -> 关联远程
# 只做初始化, 提交/推送由你在 VS Code 中完成。
#
# 用法:
#   bash git-config.sh                                  # 交互式(推荐)
#   GIT_NAME=xx GIT_EMAIL=yy bash git-config.sh --yes   # 非交互
#   GIT_REMOTE_URL=... GIT_NAME=... GIT_EMAIL=... bash git-config.sh --yes
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

DEFAULT_REMOTE="https://github.com/a6b0x/edge.git"
ASSUME_YES=false

for arg in "$@"; do
  case "$arg" in
    -y|--yes) ASSUME_YES=true ;;
    -h|--help)
      sed -n '1,14p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' | grep -v '^$' | grep -v '^!/'
      exit 0 ;;
    *) echo "未知参数: $arg"; exit 1 ;;
  esac
done

# ---- 工具函数 ----
ask() {  # 提问, 回车用默认值
  local reply
  if [ -n "${2:-}" ]; then
    read -r -p "$1 [$2]: " reply
    reply="${reply:-$2}"
  else
    read -r -p "$1: " reply
  fi
  echo "$reply"
}

confirm() {
  local reply
  read -r -p "$1 (y/N): " reply
  case "${reply,,}" in y|yes) return 0 ;; *) return 1 ;; esac
}

detect_gh() {  # 检测本机 GitHub 账号, 输出 LOGIN|NAME|ID
  local login="" name="" uid=""
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    login=$(gh api user --jq '.login' 2>/dev/null || true)
    name=$(gh api user --jq '.name' 2>/dev/null || true)
    uid=$(gh api user --jq '.id' 2>/dev/null || true)
  fi
  if [ -z "$login" ] && [ -f "$HOME/.git-credentials" ]; then
    uid=$(grep 'github.com' "$HOME/.git-credentials" 2>/dev/null \
          | head -1 | sed -E 's|https://([^:]+):[^@]*@github.com.*|\1|')
    if [ -n "$uid" ] && [[ "$uid" =~ ^[0-9]+$ ]]; then
      local resp
      resp=$(curl -s --max-time 6 "https://api.github.com/user/${uid}" 2>/dev/null || true)
      login=$(printf '%s' "$resp" | sed -n 's/.*"login"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
      name=$(printf '%s' "$resp"  | sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
    fi
  fi
  [ -n "$login" ] && echo "${login}|${name:-$login}|${uid}"
}

# ---- 身份配置: GitHub 账号 ----
setup_gh() {  # 成功返回 0; 未检测到登录返回 1
  local detected
  detected=$(detect_gh || true)
  if [ -z "$detected" ]; then
    echo "  [!] 未检测到本机 GitHub 登录"
    echo "      请先执行 gh auth login, 或改用手动输入"
    return 1
  fi
  GH_LOGIN=$(echo "$detected" | cut -d'|' -f1)
  GH_NAME=$(echo "$detected"  | cut -d'|' -f2)
  GH_UID=$(echo "$detected"   | cut -d'|' -f3)
  echo "  账号: $GH_NAME (@$GH_LOGIN)"
  NAME=$(ask "  姓名(回车用显示名)" "$GH_NAME")
  if [ -n "$GH_UID" ]; then
    GH_PRIV_EMAIL="${GH_UID}+${GH_LOGIN}@users.noreply.github.com"
    if confirm "  用隐私邮箱 $GH_PRIV_EMAIL?"; then
      EMAIL="$GH_PRIV_EMAIL"
    else
      EMAIL=$(ask "  邮箱")
    fi
  else
    EMAIL=$(ask "  邮箱")
  fi
  return 0
}

# ---- 身份配置: 手动输入 ----
setup_manual() {
  NAME=$(ask "  姓名")
  [ -z "$NAME" ] && echo "姓名不能为空" && exit 1
  EMAIL=$(ask "  邮箱")
  [ -z "$EMAIL" ] && echo "邮箱不能为空" && exit 1
}

# ===========================================================================
echo
echo "  edge Git 配置向导"
echo "  共 3 步: 初始化仓库 / 提交身份 / 关联远程"
echo

# [1/3] 初始化仓库
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "[1/3] 仓库已就绪"
else
  git init -q -b main
  echo "[1/3] 仓库已初始化 (main)"
fi

# [2/3] 提交身份
echo
CUR_NAME=$(git config user.name || true)
CUR_EMAIL=$(git config user.email || true)
NAME=""; EMAIL=""

if [ "$ASSUME_YES" = true ]; then
  NAME="${GIT_NAME:-${CUR_NAME:-}}"
  EMAIL="${GIT_EMAIL:-${CUR_EMAIL:-}}"
  [ -z "$NAME" ] || [ -z "$EMAIL" ] && { echo "缺少身份: 请提供 GIT_NAME / GIT_EMAIL"; exit 1; }
else
  echo "[2/3] 提交身份"
  if [ -n "$CUR_NAME" ]; then
    echo "  当前: $CUR_NAME <$CUR_EMAIL>"
    if ! confirm "  修改身份?"; then
      NAME="$CUR_NAME"; EMAIL="$CUR_EMAIL"
    fi
  fi
  if [ -z "$NAME" ]; then
    echo "  身份来源:"
    echo "    1) GitHub 账号"
    echo "    2) 手动输入"
    if [ "$(ask '  选择' '1')" = "2" ]; then
      setup_manual
    elif setup_gh; then
      :
    else
      setup_manual
    fi
    echo "  确认: $NAME <$EMAIL>"
    confirm "  写入?" || { echo "已取消"; exit 0; }
  fi
fi

git config user.name "$NAME"
git config user.email "$EMAIL"
echo "  已保存: $NAME <$EMAIL>"

# [3/3] 关联远程
echo
REMOTE_URL="${GIT_REMOTE_URL:-}"
if [ -z "$REMOTE_URL" ] && [ "$ASSUME_YES" = false ]; then
  if [ -n "${GH_LOGIN:-}" ]; then
    DEFAULT_REMOTE="https://github.com/${GH_LOGIN}/edge.git"
  fi
  REMOTE_URL=$(ask "  [3/3] 远程地址(回车用默认)" "$DEFAULT_REMOTE")
fi
REMOTE_URL="${REMOTE_URL:-$DEFAULT_REMOTE}"

if git remote | grep -qx origin; then
  CUR_URL=$(git remote get-url origin)
  if [ "$CUR_URL" != "$REMOTE_URL" ]; then
    git remote set-url origin "$REMOTE_URL"
    echo "  远程已更新: $REMOTE_URL"
  else
    echo "  远程已就绪: $CUR_URL"
  fi
else
  git remote add origin "$REMOTE_URL"
  echo "  远程已关联: $REMOTE_URL"
fi

# 敏感文件保护
f=".gitignore"; [ -f "$f" ] || touch "$f"
for rule in "portainer/portainer_admin_password.txt" "portainer/agent.tar"; do
  grep -qxF "$rule" "$f" 2>/dev/null || echo "$rule" >> "$f"
done

echo
echo "=============================================="
echo "  配置完成"
echo "    仓库: $(git rev-parse --show-toplevel)"
echo "    身份: $(git config user.name) <$(git config user.email)>"
echo "    远程: $(git remote get-url origin)"
echo
echo "  接下来在 VS Code 中提交推送:"
echo "    1. 打开目录: code /root/edge"
echo "    2. 源代码管理 -> 写说明 -> 提交"
echo "    3. 同步更改 -> 推送到 GitHub"
echo "=============================================="
