#!/bin/bash
# 租户网络收口：ACL + 旁路 mihomo 策略路由（幂等，可重复执行）
# 若不持久化，重启后 ACL 失效 => 租户可访问宿主 9443/9090/18080 等服务
set -u
BR=incusbr0
SUB=10.66.0.0/24
LAN=10.8.0.0/24
GW=10.8.0.1
EXT=enp193s0

has() { iptables -C "$@" 2>/dev/null; }
ins() { has "$@" || iptables -I "$1" 1 "${@:2}"; }   # 插入链首
app() { has "$@" || iptables -A "$@"; }              # 追加链尾

# ---- FORWARD(走 DOCKER-USER，不动 Docker 自有链) ----
# 注意：ins 插链首，故按「期望顺序」逆序插入
ins DOCKER-USER -i $BR -j DROP
ins DOCKER-USER -i $BR -o $EXT -j ACCEPT
ins DOCKER-USER -i $BR -d 100.64.0.0/10 -j DROP
ins DOCKER-USER -i $BR -d 172.16.0.0/12 -j DROP
ins DOCKER-USER -i $BR -d $LAN -j DROP
ins DOCKER-USER -i $BR -d $GW -j ACCEPT
ins DOCKER-USER -o $BR -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT

# ---- INPUT：容器只够得着桥上的 DHCP/DNS，碰不到宿主任何服务 ----
ins INPUT -i $BR -p tcp --dport 53 -j ACCEPT
ins INPUT -i $BR -p udp --dport 53 -j ACCEPT
ins INPUT -i $BR -p udp --dport 67 -j ACCEPT
app INPUT -i $BR -j DROP

# ---- 策略路由：租户旁路 mihomo TUN（优先级须高于 5270/9002）----
ip rule | grep -q "^5260:" || ip rule add from $SUB lookup main pref 5260
ip rule | grep -q "^5261:" || ip rule add to   $SUB lookup main pref 5261

echo "tenant-net: applied at $(date +%F' '%T)"
