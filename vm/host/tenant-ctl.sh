#!/usr/bin/env bash
# tenant-ctl.sh — 租户实例快速管理（宿主侧）
#
# 注意：本脚本只跑在宿主机，不交付给租户。对外可见内容一律走 gpu-check.sh。
#
# 用法:
#   tenant-ctl.sh list                          租户总览（状态/IP/配额/磁盘）
#   tenant-ctl.sh status  <t>                   资源使用详情 + GPU + 网络
#   tenant-ctl.sh gpu     <t> status            查看已挂 GPU
#   tenant-ctl.sh gpu     <t> on  [卡序号...]   挂 GPU（不带序号=全部）
#   tenant-ctl.sh gpu     <t> off               摘掉全部 GPU
#   tenant-ctl.sh net     <t>                   网络收口自检（ACL/出网/DNS）
#   tenant-ctl.sh apply   [t]                   幂等施加网络收口规则（带实例则顺带自检）
#   tenant-ctl.sh start|stop|restart <t>
#   tenant-ctl.sh shell   <t>                  进租户交互 shell
#   tenant-ctl.sh ssh     <t>                  打印 SSH 连接命令
#   tenant-ctl.sh quota   <t> [--cpu N] [--mem N] [--disk N]
#   tenant-ctl.sh destroy <t> [--yes]          删除实例（默认二次确认）
set -uo pipefail

NET_SUB="10.66.0.0/24"
GW=10.66.0.1

C_OK=$'\033[32m'; C_BAD=$'\033[31m'; C_WARN=$'\033[33m'; C_DIM=$'\033[2m'; C_END=$'\033[0m'
hr()    { printf '%s\n' '------------------------------------------------------------'; }
head2() { echo; printf '%s\n' "── $* ──"; }
die()   { printf '%s\n' "${C_BAD}[ERROR]${C_END} $*" >&2; exit 1; }
ok()    { printf '  %s\n' "${C_OK}$*${C_END}"; }
warn()  { printf '  %s\n' "${C_WARN}$*${C_END}"; }

has_instance()  { incus info "$1" >/dev/null 2>&1; }
need_instance() { has_instance "$1" || die "实例不存在: $1"; }

dev_type()  { incus config device get "$1" "$2" type 2>/dev/null; }
gpu_devs() {
	local t=$1 d
	for d in $(incus config device list "$t" 2>/dev/null); do
		[ "$(dev_type "$t" "$d")" = gpu ] && echo "$d"
	done
}
norm_pci() {	# 00000000:C4:00.0 -> 0000:c4:00.0
	local dom=${1%%:*} rest=${1#*:}
	printf '%04x:%s\n' "$((16#$dom))" "$rest" | tr 'A-F' 'a-f'
}
ssh_listen() {
	incus config show "$1" 2>/dev/null |
		awk '/listen: tcp:/{sub(/.*listen: tcp:/,""); n=split($0,a,":"); print a[n-1]":"a[n]; exit}'
}

# ---------------------------------------------------------------- list
cmd_list() {
	hr
	printf '%-12s %-9s %-18s %-8s %-8s %s\n' NAME STATE IPV4 CPU MEM DISK
	hr
	local t st ip typ cpu mem disk
	while IFS=, read -r t st ip typ; do
		[ -z "${t:-}" ] && continue
		cpu=$(incus config get "$t" limits.cpu 2>/dev/null); [ -z "$cpu" ] && cpu=-
		mem=$(incus config get "$t" limits.memory 2>/dev/null); [ -z "$mem" ] && mem=-
		disk=$(incus config device get "$t" root size 2>/dev/null); [ -z "$disk" ] && disk=-
		printf '%-12s %-9s %-18s %-8s %-8s %s\n' "$t" "$st" "${ip:--}" "$cpu" "$mem" "$disk"
	done < <(incus list --format csv -c ns4t 2>/dev/null)
	hr
	printf '%s\n' "${C_DIM}详情: $0 status <实例>     GPU: $0 gpu <实例> status${C_END}"
}

# ---------------------------------------------------------------- status
cmd_status() {
	local t=$1; need_instance "$t"
	hr; printf ' 实例 %s\n' "$t"; hr

	head2 "运行状态"
	incus info "$t" 2>/dev/null |
		grep -E '^(Status|Type|Architecture|PID):|Processes:|CPU usage \(in seconds\):|Memory \(current\):|Memory \(peak\):' |
		sed 's/^[[:space:]]*//; s/^/  /'

	head2 "配额"
	printf '  %-10s %s\n' CPU "$(incus config get "$t" limits.cpu 2>/dev/null || echo -)"
	printf '  %-10s %s\n' MEM "$(incus config get "$t" limits.memory 2>/dev/null || echo -)"
	printf '  %-10s %s\n' DISK "$(incus config device get "$t" root size 2>/dev/null || echo -)"

	head2 "磁盘占用"
	if [ "$(incus list "$t" --format csv -c s 2>/dev/null)" = RUNNING ]; then
		incus exec "$t" -- df -h / 2>/dev/null | tail -1 | sed 's/^/  /'
	else
		printf '%s\n' "  ${C_DIM}(实例未运行，跳过)${C_END}"
	fi

	head2 "GPU"
	local n=0 d
	for d in $(gpu_devs "$t"); do
		printf '  %-6s pci=%s\n' "$d" "$(incus config device get "$t" "$d" pci 2>/dev/null)"
		n=$((n + 1))
	done
	[ "$n" -eq 0 ] && printf '%s\n' "  ${C_DIM}(未挂载 GPU)${C_END}"
	printf '  %-6s %s\n' runtime "$(incus config get "$t" nvidia.runtime 2>/dev/null || echo -)"
	if [ "$n" -gt 0 ] && [ "$(incus list "$t" --format csv -c s 2>/dev/null)" = RUNNING ]; then
		incus exec "$t" -- nvidia-smi -L 2>&1 | sed 's/^/  /'
	fi

	head2 "入口"
	local lp sship
	sship=$(incus list "$t" --format csv -c 4 2>/dev/null | cut -d' ' -f1)
	printf '  %-10s %s\n' IP "${sship:--}"
	lp=$(ssh_listen "$t"); [ -n "$lp" ] && printf '  %-10s %s\n' SSH "$lp"
}

# ---------------------------------------------------------------- gpu
cmd_gpu() {
	local t=$1; shift
	local action=${1:-status}
	[ $# -gt 0 ] && shift
	need_instance "$t"

	case "$action" in
	status)
		head2 "GPU 设备（$t）"
		local n=0 d
		for d in $(gpu_devs "$t"); do
			printf '  %-6s pci=%s\n' "$d" "$(incus config device get "$t" "$d" pci 2>/dev/null)"
			n=$((n + 1))
		done
		[ "$n" -eq 0 ] && printf '%s\n' "  ${C_DIM}(未挂载任何 GPU)${C_END}"
		printf '  nvidia.runtime = %s\n' "$(incus config get "$t" nvidia.runtime 2>/dev/null || echo -)"
		if [ "$(incus list "$t" --format csv -c s 2>/dev/null)" = RUNNING ]; then
			printf '  租户侧视图:\n'
			incus exec "$t" -- nvidia-smi -L 2>&1 | sed 's/^/    /'
		fi
		;;

	off)
		local d removed=0
		for d in $(gpu_devs "$t"); do
			incus config device remove "$t" "$d" >/dev/null && removed=$((removed + 1))
		done
		ok "已摘除 $removed 个 GPU 设备"
		[ "$removed" -gt 0 ] && incus config unset "$t" nvidia.runtime >/dev/null 2>&1
		;;

	on)
		local -a want=("$@")
		local -a targets=()
		local idx pci
		while IFS=, read -r idx pci; do
			idx=${idx// /}; pci=$(norm_pci "${pci// /}")
			if [ "${#want[@]}" -gt 0 ]; then
				printf '%s\n' "${want[@]}" | grep -qx "$idx" || continue
			fi
			targets+=("$idx:$pci")
		done < <(nvidia-smi --query-gpu=index,pci.bus_id --format=csv,noheader 2>/dev/null)
		[ "${#targets[@]}" -eq 0 ] && die "未匹配到任何 GPU（卡序号: $*）"

		# 1) nvidia.runtime：首次设置需要重启容器才生效
		local rt need_restart=0
		rt=$(incus config get "$t" nvidia.runtime 2>/dev/null)
		if [ "$rt" != "true" ]; then
			incus config set "$t" nvidia.runtime=true >/dev/null
			need_restart=1
		fi

		# 2) 按 PCI 匹配现有设备：不在目标集合里的移除，命中的记入 kept
		local d cur removed=0 kept="" wildcard_ok=0
		[ "${#want[@]}" -eq 0 ] && wildcard_ok=1
		for d in $(gpu_devs "$t"); do
			cur=$(incus config device get "$t" "$d" pci 2>/dev/null | tr 'A-F' 'a-f')
			if [ -z "$cur" ] && [ "$wildcard_ok" -eq 1 ]; then
				kept="${kept}${d}:"$'\n'
				printf '  = %-6s (all GPUs)\n' "$d"
				continue
			fi
			if [ -n "$cur" ] && printf '%s\n' "${targets[@]}" | grep -q ":${cur}$"; then
				kept="${kept}${d}:${cur}"$'\n'
				printf '  = %-6s pci=%s  (already attached)\n' "$d" "$cur"
			else
				incus config device remove "$t" "$d" >/dev/null && removed=$((removed + 1))
				printf '  - %-6s pci=%s  (removed)\n' "$d" "${cur:-none}"
			fi
		done

		# 3) 补齐缺失的卡，命名 gpu<卡序号>；名称被占则加后缀
		local entry want_idx want_pci dev n
		for entry in "${targets[@]}"; do
			want_idx=${entry%%:*}; want_pci=${entry#*:}
			printf '%s' "$kept" | grep -q ":${want_pci}$" && continue
			printf '%s' "$kept" | grep -q ':$' && continue	# 已有通配设备覆盖全部
			dev="gpu${want_idx}"; n=1
			while [ -n "$(incus config device get "$t" "$dev" type 2>/dev/null)" ]; do
				dev="gpu${want_idx}-${n}"; n=$((n + 1))
			done
			incus config device add "$t" "$dev" gpu gputype=physical pci="$want_pci" >/dev/null
			printf '  + %-6s pci=%s\n' "$dev" "$want_pci"
		done

		if [ "$need_restart" -eq 1 ]; then
			warn "首次启用 nvidia.runtime，需重启容器才注入 CUDA 库 -> 正在重启 $t"
			incus restart "$t" >/dev/null
		fi
		ok "GPU 分配完成"
		printf '  租户侧视图:\n'
		incus exec "$t" -- nvidia-smi -L 2>&1 | sed 's/^/    /'
		;;

	*)
		die "用法: $0 gpu <实例> on|off|status [卡序号...]"
		;;
	esac
}

# ---------------------------------------------------------------- net
cmd_net() {
	local t=$1; need_instance "$t"
	hr; printf ' 网络收口自检: %s\n' "$t"; hr

	head2 "宿主规则是否在位"
	printf '  %-28s %s 条\n' "DOCKER-USER / incusbr0" "$(iptables -S DOCKER-USER 2>/dev/null | grep -c incusbr0 || true)"
	printf '  %-28s %s 条\n' "INPUT / incusbr0" "$(iptables -S INPUT 2>/dev/null | grep -c incusbr0 || true)"
	printf '  %-28s %s 条\n' "ip rule 5260/5261" "$(ip rule 2>/dev/null | grep -cE '^526[01]:' || true)"
	printf '  %-28s %s 条\n' "ip6tables / incusbr0" "$(ip6tables -S 2>/dev/null | grep -c incusbr0 || true)"
	printf '  %-28s %s\n' "systemd unit" "$(systemctl is-enabled incus-tenant-net.service 2>/dev/null)/$(systemctl is-active incus-tenant-net.service 2>/dev/null)"

	if [ "$(incus list "$t" --format csv -c s 2>/dev/null)" != RUNNING ]; then
		warn "实例未运行，跳过租户侧探测"
		return
	fi

	head2 "租户侧 -> 宿主服务（期望全部 BLOCKED）"
	incus exec "$t" -- bash -s <<EOS
probe(){ timeout 3 bash -c "cat </dev/null >/dev/tcp/\$1/\$2" >/dev/null 2>&1 && echo REACHABLE || echo BLOCKED; }
for p in 9443 9090 18080 1001 8443 22; do printf '  %-22s %s\n' "$GW:\$p" "\$(probe $GW \$p)"; done
EOS

	head2 "租户出口（期望云厂商直连 IP，非宿主代理 IP）"
	incus exec "$t" -- bash -s <<'EOS'
probe(){ timeout 6 bash -c "cat </dev/null >/dev/tcp/$1/$2" >/dev/null 2>&1 && echo OK || echo FAIL; }
printf '  %-22s %s\n' "www.baidu.com:80" "$(probe www.baidu.com 80)"
printf '  %-22s %s\n' "DNS servers" "$(awk '/^nameserver/{printf "%s ", $2}' /etc/resolv.conf 2>/dev/null)"
printf '  %-22s %s\n' "解析 baidu" "$(getent ahostsv4 www.baidu.com 2>/dev/null | head -1 | cut -d' ' -f1)"
EOS
}

# ---------------------------------------------------------------- apply
cmd_apply() {
	local t=${1:-}
	local repo dep ran
	repo="$(cd "$(dirname "$0")" && pwd)/incus-tenant-net.sh"
	dep=/usr/local/sbin/incus-tenant-net.sh

	if [ -f "$repo" ]; then
		ran=$repo
		if [ -f "$dep" ] && ! diff -q "$repo" "$dep" >/dev/null 2>&1; then
			warn "仓库副本与已部署副本 ($dep) 不一致 —— 本次施加的是仓库副本"
		fi
	elif [ -f "$dep" ]; then
		ran=$dep
	else
		die "找不到 incus-tenant-net.sh（仓库与 $dep 均不存在）"
	fi

	[ "$(id -u)" -eq 0 ] || die "施加 iptables / ip rule 需要 root"
	head2 "施加网络收口规则"
	printf '  source: %s\n' "$ran"
	bash "$ran" | sed 's/^/  /'
	ok "规则已幂等施加（systemd 自启用的是 $dep，二者独立）"

	if [ -n "$t" ]; then
		need_instance "$t"
		cmd_net "$t"
	fi
}

# ---------------------------------------------------------------- 生命周期
cmd_life() {
	local act=$1 t=$2; need_instance "$t"
	case "$act" in
	start)   incus start "$t" >/dev/null && ok "$t 已启动" ;;
	stop)    warn "停止会中断 $t 内的所有工作"
	         incus stop "$t" >/dev/null && ok "$t 已停止" ;;
	restart) incus restart "$t" >/dev/null && ok "$t 已重启" ;;
	esac
}

cmd_shell() { local t=$1; need_instance "$t"; exec incus exec "$t" -- bash; }

cmd_ssh() {
	local t=$1; need_instance "$t"
	local lp ip
	lp=$(ssh_listen "$t")
	if [ -z "$lp" ]; then
		warn "$t 没有 proxy 设备，无法从宿主端口 SSH"
		return
	fi
	local ip=${lp%%:*} port=${lp##*:}
	printf '  ssh root@%s -p %s\n' "$ip" "$port"
}

# ---------------------------------------------------------------- quota
cmd_quota() {
	local t=$1; shift; need_instance "$t"
	local changed=0
	while [ $# -gt 0 ]; do
		case "$1" in
		--cpu)  incus config set "$t" limits.cpu="$2" >/dev/null && printf '  CPU  -> %s\n' "$2"; changed=1; shift 2 ;;
		--mem)  incus config set "$t" limits.memory="$2" >/dev/null && printf '  内存 -> %s\n' "$2"; changed=1; shift 2 ;;
		--disk) incus config device set "$t" root size="$2" >/dev/null && printf '  磁盘 -> %s\n' "$2"; changed=1; shift 2 ;;
		*)      die "未知参数: $1（可用 --cpu/--mem/--disk）" ;;
		esac
	done
	[ "$changed" -eq 0 ] && die "用法: $0 quota <实例> [--cpu N] [--mem N] [--disk N]"
	ok "配额已更新"
}

# ---------------------------------------------------------------- destroy
cmd_destroy() {
	local t=$1; shift
	need_instance "$t"
	local yes=0
	[ "${1:-}" = "--yes" ] && yes=1
	if [ "$yes" -eq 0 ]; then
		local n
		n=$(gpu_devs "$t" | wc -l)
		warn "即将删除实例 $t（GPU 设备 $n 个），数据不可恢复"
		printf '  确认请输入实例名: '
		read -r ans
		[ "$ans" = "$t" ] || die "输入不匹配，已取消"
	fi
	incus delete "$t" --force >/dev/null && ok "$t 已删除"
	printf '%s\n' "${C_DIM}提示: 删除后建议核对 DOCKER-USER / ip rule 中是否残留 $NET_SUB 相关放行${C_END}"
}

# ---------------------------------------------------------------- dispatch
usage() {
	sed -n '2,/^set /p' "$0" | grep '^#' | sed 's/^# \{0,1\}//'
}

case "${1:-help}" in
list)    cmd_list ;;
status)  [ $# -ge 2 ] || die "用法: $0 status <实例>"; cmd_status "$2" ;;
gpu)     [ $# -ge 2 ] || die "用法: $0 gpu <实例> on|off|status [卡序号...]"; cmd_gpu "${@:2}" ;;
net)     [ $# -ge 2 ] || die "用法: $0 net <实例>"; cmd_net "$2" ;;
apply)   cmd_apply "${2:-}" ;;
start|stop|restart) [ $# -ge 2 ] || die "用法: $0 $1 <实例>"; cmd_life "$1" "$2" ;;
shell)   [ $# -ge 2 ] || die "用法: $0 shell <实例>"; cmd_shell "$2" ;;
ssh)     [ $# -ge 2 ] || die "用法: $0 ssh <实例>"; cmd_ssh "$2" ;;
quota)   [ $# -ge 3 ] || die "用法: $0 quota <实例> [--cpu N] [--mem N] [--disk N]"; cmd_quota "$@" ;;
destroy) [ $# -ge 2 ] || die "用法: $0 destroy <实例> [--yes]"; cmd_destroy "$@" ;;
help|-h|--help) usage ;;
*)       usage; die "未知命令: $1" ;;
esac
