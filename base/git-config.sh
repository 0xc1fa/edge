#!/usr/bin/env bash
# =============================================================================
# git-config.sh - edge 仓库 Git 配置向导
# =============================================================================
# 分 4 步: 初始化仓库 -> 提交身份 -> 关联远程 -> 项目级凭据
# 只做初始化, 提交/推送由你在 VS Code 中完成。
#
# 用法:
#   bash git-config.sh                                  # 交互式(推荐)
#   GIT_NAME=xx GIT_EMAIL=yy bash git-config.sh --yes   # 非交互
#   GIT_REMOTE_URL=... GIT_NAME=... GIT_EMAIL=... bash git-config.sh --yes
#   非交互可额外提供:
#     GIT_CREDENTIAL_FILE=...  # 项目级凭据文件路径, 提供则自动配置
#     GIT_GITHUB_LOGIN=...     # 远程 URL 要带的 GitHub 用户名
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# 注意: 不 cd, 脚本作用于"当前所在目录"的仓库, 便于不同项目复用

DEFAULT_REMOTE="https://github.com/0xc1fa/edge.git"
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
  local login="" name="" uid="" f resp
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    login=$(gh api user --jq '.login' 2>/dev/null || true)
    name=$(gh api user --jq '.name' 2>/dev/null || true)
    uid=$(gh api user --jq '.id' 2>/dev/null || true)
  fi
  # 依次检查 项目级 / 全局 凭据文件中的 github 条目 (支持 UID 或 用户名)
  if [ -z "$login" ]; then
    for f in "$(git rev-parse --show-toplevel 2>/dev/null)/.git-credentials" "$HOME/.git-credentials"; do
      [ -f "$f" ] || continue
      uid=$(grep 'github.com' "$f" 2>/dev/null | head -1 | sed -E 's|https://([^:]+):[^@]*@github.com.*|\1|')
      [ -n "$uid" ] || continue
      resp=$(curl -s --max-time 6 "https://api.github.com/user/${uid}" 2>/dev/null || true)
      if ! printf '%s' "$resp" | grep -q '"login"'; then
        resp=$(curl -s --max-time 6 "https://api.github.com/users/${uid}" 2>/dev/null || true)
      fi
      login=$(printf '%s' "$resp" | sed -n 's/.*"login"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
      name=$(printf '%s' "$resp"  | sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
      [ -n "$login" ] && break
    done
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

# ---- 项目级凭据: 不同项目推不同账号 ----
setup_credential() {  # 项目级 credential.helper + 独立凭据文件
  local repo_root cred_file url url_clean owner repo user add_user
  repo_root=$(git rev-parse --show-toplevel)
  cred_file="${GIT_CREDENTIAL_FILE:-$repo_root/.git-credentials}"
  echo "  [4/4] 项目级凭据 (不同项目可推不同账号)"
  echo "        凭据文件: $cred_file"

  if [ "$ASSUME_YES" = true ]; then
    [ -n "${GIT_CREDENTIAL_FILE:-}" ] || { echo "  跳过 (非交互未提供 GIT_CREDENTIAL_FILE)"; return 0; }
  elif ! confirm "  为本项目配置独立凭据文件?"; then
    echo "  跳过, 沿用全局凭据"; return 0
  fi

  git config --local credential.helper "store --file=$cred_file"
  [ -f "$cred_file" ] || { touch "$cred_file"; chmod 600 "$cred_file"; }
  echo "  已配置: credential.helper = store --file=$cred_file"

  # 远程 URL 带用户名, 便于 store 精确匹配
  url=$(git remote get-url origin 2>/dev/null || true)
  if [[ "$url" == https://github.com/* && "$url" != https://*@github.com/* ]]; then
    url_clean="${url%.git}"
    owner=$(printf '%s' "$url_clean" | sed -E 's|https://github.com/([^/]+)/([^/]+)$|\1|')
    repo=$(printf '%s' "$url_clean" | sed -E 's|https://github.com/([^/]+)/([^/]+)$|\2|')
    user="${GIT_GITHUB_LOGIN:-$owner}"
    add_user=false
    if [ "$ASSUME_YES" = true ] && [ -n "${GIT_GITHUB_LOGIN:-}" ]; then
      add_user=true
    elif [ "$ASSUME_YES" = false ]; then
      confirm "  远程 URL 加上用户名 ${user}@ (精确匹配凭据)?" && add_user=true
    fi
    if [ "$add_user" = true ]; then
      git remote set-url origin "https://${user}@github.com/${owner}/${repo}.git"
      echo "  远程已更新: https://${user}@github.com/${owner}/${repo}.git"
    fi
  fi
  echo "  提示: 凭据文件行格式为 https://<用户名>:<token>@github.com"
}

# ===========================================================================
echo
echo "  edge Git 配置向导"
echo "  共 4 步: 初始化仓库 / 提交身份 / 关联远程 / 项目级凭据"
echo

# [1/3] 初始化仓库
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "[1/4] 仓库已就绪"
else
  git init -q -b main
  echo "[1/4] 仓库已初始化 (main)"
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
  echo "[2/4] 提交身份"
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
  REMOTE_URL=$(ask "  [3/4] 远程地址(回车用默认)" "$DEFAULT_REMOTE")
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

# [4/4] 项目级凭据
echo
setup_credential

# [检查] 首次推送: 远端是否已存在内容
echo
if REMOTE_HEAD=$(git ls-remote origin HEAD 2>/dev/null); then
  if [ -n "$REMOTE_HEAD" ]; then
    echo "  [!] 远端仓库已有提交"
    echo "      若为初始化时勾选了 README/.gitignore, 首次推送会因"
    echo "      历史不相关被拒 (unrelated histories), 处理见 docs/note-git.md"
  else
    echo "  [ok] 远端为空仓库, 可直接首次推送"
  fi
else
  echo "  [i] 未能连接远端(离线?), 推送时如被拒见 docs/note-git.md"
fi

# 敏感文件保护
f=".gitignore"; [ -f "$f" ] || touch "$f"
for rule in "stacks/portainer_admin_password.txt" "stacks/agent.tar" ".git-credentials"; do
  grep -qxF "$rule" "$f" 2>/dev/null || echo "$rule" >> "$f"
done

echo
echo "=============================================="
echo "  配置完成"
echo "    仓库: $(git rev-parse --show-toplevel)"
echo "    身份: $(git config user.name) <$(git config user.email)>"
echo "    远程: $(git remote get-url origin)"
CRED_CFG=$(git config --local --get credential.helper || echo "未配置(沿用全局)")
echo "    凭据: ${CRED_CFG}"
echo
echo "  接下来在 VS Code 中提交推送:"
echo "    1. 打开目录: code /root/edge"
echo "    2. 源代码管理 -> 写说明 -> 提交"
echo "    3. 同步更改 -> 推送到 GitHub"
echo "=============================================="
