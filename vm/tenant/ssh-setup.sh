#!/bin/bash
set -e
echo "tenant ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/tenant
chmod 440 /etc/sudoers.d/tenant
sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config
sed -i 's/^#*PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config
grep -q '^PasswordAuthentication yes' /etc/ssh/sshd_config || echo 'PasswordAuthentication yes' >> /etc/ssh/sshd_config
systemctl restart ssh
sleep 1
echo "--- 生效配置 ---"
sshd -T 2>/dev/null | grep -E '^(passwordauthentication|permitrootlogin)'
echo "--- 账号 ---"
id tenant
