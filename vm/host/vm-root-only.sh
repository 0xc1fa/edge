#!/usr/bin/env bash
# 把租户容器收敛成「只有 root 可登录」的默认姿态：
#   - 停用镜像自带账号 ubuntu 与历史账号 tenant（锁口令 + 改 nologin，家目录保留，可恢复）
#   - 删掉它们的 sudo 规则（ubuntu 的 90-incus 与 tenant 的独立文件）
#   - 保证 root 口令登录可用（PermitRootLogin yes）
#
# 用法: vm-root-only.sh <容器名> [root新口令]
#   ./vm-root-only.sh tenant01             # 只收敛账号，不动 root 口令
#   ./vm-root-only.sh tenant02 '<新口令>'   # 收敛 + 设置 root 口令
#
# 注意: 口令走 argv，本机 ps 可见；不传则只做账号收敛。
set -euo pipefail

CT="${1:?用法: $0 <容器名> [root新口令]}"
PW="${2:-}"

incus exec "$CT" -- bash -c '
set -e
# 需要停用的账号：镜像自带 + 迁移前的历史账号
for u in ubuntu tenant; do
  if getent passwd "$u" >/dev/null 2>&1; then
    passwd -l "$u" >/dev/null 2>&1 || true
    usermod -s /usr/sbin/nologin "$u"
    rm -f "/etc/sudoers.d/$u"
  fi
done
# 镜像自带的 sudo 规则文件（形如 90-incus，内容多为 ubuntu ALL=(ALL) NOPASSWD:ALL）
rm -f /etc/sudoers.d/90-incus

# root 直登：口令认证 + PermitRootLogin yes
if ! grep -q "^PermitRootLogin yes" /etc/ssh/sshd_config 2>/dev/null; then
  sed -i "s/^#\?PermitRootLogin.*/PermitRootLogin yes/" /etc/ssh/sshd_config 2>/dev/null || true
  grep -q "^PermitRootLogin yes" /etc/ssh/sshd_config || echo "PermitRootLogin yes" >> /etc/ssh/sshd_config
fi
sshd -t && systemctl restart ssh

echo "== [$HOSTNAME] 剩余 sudoers.d =="
ls /etc/sudoers.d/ 2>/dev/null || true
echo "== 可登录账号（P=有口令 L=锁定）=="
for u in root ubuntu tenant; do
  getent passwd "$u" >/dev/null 2>&1 && passwd -S "$u" | awk "{print \$1\"  \"\$2\"  shell=\"\$0}" | cut -d" " -f1-3
done
'

if [ -n "$PW" ]; then
  printf 'root:%s\n' "$PW" | incus exec "$CT" -- chpasswd
  echo "[$CT] root 口令已更新"
fi

echo "[$CT] 完成：仅 root 可登录（tenant / ubuntu 已停用，家目录保留）"
