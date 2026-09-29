#!/usr/bin/env bash
# tenant-ctl.sh — 租户实例快速管理（宿主侧）
#
# 注意：本脚本只跑在宿主机，不交付给租户。对外可见内容一律走 gpu-check.sh。
#
# 用法:
#   tenant-ctl.sh list                          租户总览（状态/IP/配额/GPU/磁盘）
#   tenant-ctl.sh status  <t>                   资源使用详情 + GPU + 网络
#   tenant-ctl.sh gpu                           宿主 GPU 全景（显存/利用率/温度/功耗/风扇/归属）
#   tenant-ctl.sh gpu     free|assigned         只看未分配 / 已分配的卡
#   tenant-ctl.sh gpu     <t>                   查看该租户 GPU 挂载明细（含租户侧视图）
#   tenant-ctl.sh gpu     <t> status            同上（显式写法）
#   tenant-ctl.sh gpu     <t> watch [秒]        实时刷新该租户 GPU 指标（默认 2s）
#   tenant-ctl.sh gpu     watch  [秒]           实时刷新宿主 GPU 全景（默认 2s）
#   tenant-ctl.sh gpu     <t> on  [卡序号...] [--now|--no-restart]
#                                               按需挂卡（不带序号=全部），自动判断是否需要重启
#   tenant-ctl.sh gpu     <t> off [卡序号...] [--now|--no-restart]
#                                               按需摘卡（不带序号=全部）
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

# ---- GPU 辅助 --------------------------------------------------------------
HOST_GPU_AVAIL=0
command -v nvidia-smi >/dev/null 2>&1 && HOST_GPU_AVAIL=1

all_instances() { incus list --format csv -c n 2>/dev/null; }
inst_running()  { [ "$(incus list "$1" --format csv -c s 2>/dev/null)" = RUNNING ]; }

host_gpus() {	# index,name,pci,mem_total,mem_used,util,temp,power,fan
	nvidia-smi \
		--query-gpu=index,name,pci.bus_id,memory.total,memory.used,utilization.gpu,temperature.gpu,power.draw,fan.speed \
		--format=csv,noheader,nounits 2>/dev/null
}

gpu_pci_index_map() {	# "归一化pci 卡序号" 行；宿主 PCI 归一化后与设备 pci 可比
	[ "$HOST_GPU_AVAIL" -eq 1 ] || return 0
	nvidia-smi --query-gpu=index,pci.bus_id --format=csv,noheader 2>/dev/null |
		while IFS=, read -r idx pci; do
			printf '%s %s\n' "$(norm_pci "${pci// /}")" "${idx// /}"
		done
}

gpu_holders() {	# $1=归一化 pci -> 引用该卡的 "实例(设备名)" 列表
	local want=$1 t d p out=""
	for t in $(all_instances); do
		for d in $(gpu_devs "$t"); do
			p=$(incus config device get "$t" "$d" pci 2>/dev/null | tr 'A-F' 'a-f')
			if [ -z "$p" ]; then
				out="${out}${out:+, }${t}(${d}=all)"
			elif [ "$p" = "$want" ]; then
				out="${out}${out:+, }${t}(${d})"
			fi
		done
	done
	[ -n "$out" ] && echo "$out" || echo "未分配"
}

inst_gpu_brief() {	# 该实例占用的宿主卡序号（逗号分隔）/ all / -
	local t=$1 d p idx map out="" has_all=0
	map=$(gpu_pci_index_map)
	for d in $(gpu_devs "$t"); do
		p=$(incus config device get "$t" "$d" pci 2>/dev/null | tr 'A-F' 'a-f')
		if [ -z "$p" ]; then has_all=1; continue; fi
		idx=$(printf '%s\n' "$map" | awk -v k="$p" '$1==k{print $2; exit}')
		[ -n "$idx" ] && out="${out}${out:+,}${idx}"
	done
	if [ "$has_all" -eq 1 ]; then echo all
	elif [ -n "$out" ]; then echo "$out"
	else echo -
	fi
}

attached_gpu_count() {	# 期望租户侧可见的卡数（通配=宿主全部）
	local t=$1 d p n=0
	for d in $(gpu_devs "$t"); do
		p=$(incus config device get "$t" "$d" pci 2>/dev/null)
		if [ -z "$p" ]; then host_gpus | wc -l; return; fi
		n=$((n + 1))
	done
	echo "$n"
}

inst_visible_gpu_count() {	# 租户侧 nvidia-smi -L 实际可见卡数（未运行返回 -1）
	inst_running "$1" || { echo -1; return; }
	incus exec "$1" -- nvidia-smi -L 2>/dev/null | grep -c '^GPU '
}

# ---------------------------------------------------------------- list
cmd_list() {
	hr
	printf '%-12s %-9s %-18s %-6s %-8s %-8s %s\n' NAME STATE IPV4 CPU MEM GPU DISK
	hr
	local t st ip typ cpu mem gpu disk
	while IFS=, read -r t st ip typ; do
		[ -z "${t:-}" ] && continue
		cpu=$(incus config get "$t" limits.cpu 2>/dev/null); [ -z "$cpu" ] && cpu=-
		mem=$(incus config get "$t" limits.memory 2>/dev/null); [ -z "$mem" ] && mem=-
		gpu=$(inst_gpu_brief "$t")
		disk=$(incus config device get "$t" root size 2>/dev/null); [ -z "$disk" ] && disk=-
		printf '%-12s %-9s %-18s %-6s %-8s %-8s %s\n' "$t" "$st" "${ip:--}" "$cpu" "$mem" "$gpu" "$disk"
	done < <(incus list --format csv -c ns4t 2>/dev/null)
	hr
	printf '%s\n' "${C_DIM}GPU 列 = 占用的宿主卡序号（all=全部卡）${C_END}"
	printf '%s\n' "${C_DIM}详情: $0 status <实例>     GPU: $0 gpu [实例] [on|off|status] [卡序号...]${C_END}"
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
		printf '  %-8s pci=%s\n' "$d" "$(incus config device get "$t" "$d" pci 2>/dev/null)"
		n=$((n + 1))
	done
	[ "$n" -eq 0 ] && printf '%s\n' "  ${C_DIM}(未挂载 GPU)${C_END}"
	printf '  %-8s %s\n' runtime "$(incus config get "$t" nvidia.runtime 2>/dev/null || echo -)"
	printf '  %-8s %s\n' 卡序号 "$(inst_gpu_brief "$t")"
	if [ "$n" -gt 0 ] && inst_running "$t"; then
		incus exec "$t" -- nvidia-smi \
			--query-gpu=index,name,memory.used,memory.total,utilization.gpu,temperature.gpu,power.draw \
			--format=csv 2>&1 | sed 's/^/  /'
	fi

	head2 "入口"
	local lp sship
	sship=$(incus list "$t" --format csv -c 4 2>/dev/null | cut -d' ' -f1)
	printf '  %-10s %s\n' IP "${sship:--}"
	lp=$(ssh_listen "$t"); [ -n "$lp" ] && printf '  %-10s %s\n' SSH "$lp"
}

# ---------------------------------------------------------------- gpu
gpu_host_matrix() {	# 宿主 GPU 全景（物理卡，含未分配）；$1=all|free|assigned
	local filter=${1:-all}
	hr
	case "$filter" in
	free)     printf ' 宿主 GPU 全景（仅未分配）\n' ;;
	assigned) printf ' 宿主 GPU 全景（仅已分配）\n' ;;
	*)        printf ' 宿主 GPU 全景（物理卡总数，含未分配）\n' ;;
	esac
	hr
	if [ "$HOST_GPU_AVAIL" -ne 1 ]; then
		warn "宿主未找到 nvidia-smi，无法枚举 GPU"
		return
	fi
	printf '  %-4s %-23s %-13s %-14s %-6s %-6s %-9s %-8s %s\n' \
		IDX MODEL PCI MEM UTIL TEMP POWER FAN HOLDER
	hr
	local idx name pci tot used util temp power fan np holder
	local total=0 assigned=0
	while IFS=, read -r idx name pci tot used util temp power fan; do
		[ -z "${idx:-}" ] && continue
		name=$(printf '%s' "$name" | sed 's/^ *//; s/ *$//')
		pci=${pci// /}; tot=${tot// /}; used=${used// /}; util=${util// /}
		temp=${temp// /}; power=${power// /}; fan=${fan// /}
		np=$(norm_pci "$pci")
		holder=$(gpu_holders "$np")
		total=$((total + 1))
		[ "$holder" != "未分配" ] && assigned=$((assigned + 1))
		case "$filter" in
		free)     [ "$holder" = "未分配" ] || continue ;;
		assigned) [ "$holder" != "未分配" ] || continue ;;
		esac
		printf '  %-4s %-23s %-13s %-14s %-6s %-6s %-9s %-8s %s\n' \
			"${idx// /}" "$name" "$np" "${used}M/${tot}M" "${util}%" \
			"${temp}C" "${power}W" "${fan}%" "$holder"
	done < <(host_gpus)
	hr
	printf '  宿主物理卡 %s 张：已分配 %s，未分配 %s\n' "$total" "$assigned" "$((total - assigned))"
	printf '%s\n' "${C_DIM}说明: 本表列的是宿主全部物理卡（不代表分配），HOLDER 才是归属${C_END}"
	printf '%s\n' "${C_DIM}只看某租户: $0 gpu <实例>   仅未分配: $0 gpu free   仅已分配: $0 gpu assigned${C_END}"
	printf '%s\n' "${C_DIM}挂载/摘除: $0 gpu <实例> on|off [卡序号...]   实时刷新: $0 gpu watch [秒]${C_END}"
}

gpu_show() {	# 单实例 GPU 明细
	local t=$1 n=0 d p
	head2 "实例 $t 的 GPU 挂载"
	for d in $(gpu_devs "$t"); do
		p=$(incus config device get "$t" "$d" pci 2>/dev/null)
		printf '  %-8s pci=%s\n' "$d" "${p:-（通配=宿主全部卡）}"
		n=$((n + 1))
	done
	[ "$n" -eq 0 ] && printf '%s\n' "  ${C_DIM}(未挂载任何 GPU)${C_END}"
	printf '  %-8s %s\n' runtime "$(incus config get "$t" nvidia.runtime 2>/dev/null || echo -)"
	printf '  %-8s %s\n' 卡序号 "$(inst_gpu_brief "$t")"

	if ! inst_running "$t"; then
		warn "实例未运行，跳过租户侧视图"
		return
	fi
	head2 "租户侧视图（nvidia-smi）"
	if [ "$n" -gt 0 ]; then
		incus exec "$t" -- nvidia-smi \
			--query-gpu=index,name,memory.used,memory.total,utilization.gpu,temperature.gpu,power.draw \
			--format=csv 2>&1 | sed 's/^/  /'
	else
		incus exec "$t" -- nvidia-smi -L 2>&1 | sed 's/^/  /'
	fi
}

# 实时监控：gpu watch [实例] [秒]
gpu_watch() {
	local t=$1 interval=${2:-2}
	command -v clear >/dev/null 2>&1 || true
	while :; do
		clear 2>/dev/null || printf '\033[2J\033[H'
		if [ -z "$t" ]; then
			gpu_host_matrix
		elif has_instance "$t"; then
			hr; printf ' 实例 %s GPU 实时视图  (%s)\n' "$t" "$(date '+%F %T')"; hr
			if inst_running "$t"; then
				incus exec "$t" -- nvidia-smi \
					--query-gpu=index,name,memory.used,memory.total,utilization.gpu,temperature.gpu,power.draw \
					--format=csv 2>&1 | sed 's/^/  /'
			else
				warn "实例未运行"
			fi
		else
			die "实例不存在: $t"
		fi
		printf '%s\n' "${C_DIM}每 ${interval}s 刷新，Ctrl-C 退出${C_END}"
		sleep "$interval"
	done
}

gpu_apply() {	# 变更后决定是否需要重启；$1=实例 $2=auto|force|never
	local t=$1 mode=$2 exp vis
	if ! inst_running "$t"; then
		printf '%s\n' "  ${C_DIM}(实例未运行，配置将在下次启动时生效)${C_END}"
		return
	fi
	exp=$(attached_gpu_count "$t")
	vis=$(inst_visible_gpu_count "$t")
	if [ "$mode" != force ] && [ "$vis" = "$exp" ]; then
		ok "热更新已生效（租户侧可见 $vis 张卡，无需重启）"
		return
	fi
	case "$mode" in
	never) warn "配置已写入，但需重启实例才生效（当前可见 $vis / 期望 $exp）"; return ;;
	force) warn "按要求强制重启 $t 以应用 GPU 变更" ;;
	*)     warn "GPU 设备变更需重启才注入 /dev/nvidia*（当前可见 $vis / 期望 $exp）-> 重启 $t" ;;
	esac
	incus restart "$t" >/dev/null
	vis=$(inst_visible_gpu_count "$t")
	ok "已重启，租户侧可见 $vis 张卡"
}

gpu_on() {
	local t=$1 mode=$2; shift 2
	local -a want=("$@")
	[ "$HOST_GPU_AVAIL" -eq 1 ] || die "宿主未找到 nvidia-smi，无法枚举 GPU"

	local -a targets=()
	local idx pci
	while IFS=, read -r idx pci; do
		idx=${idx// /}; pci=$(norm_pci "${pci// /}")
		if [ "${#want[@]}" -gt 0 ]; then
			printf '%s\n' "${want[@]}" | grep -qx "$idx" || continue
		fi
		targets+=("$idx:$pci")
	done < <(nvidia-smi --query-gpu=index,pci.bus_id --format=csv,noheader 2>/dev/null)
	[ "${#targets[@]}" -eq 0 ] && die "未匹配到任何 GPU（卡序号: ${want[*]:-全部}）"

	# 1) nvidia.runtime：首次设置需重启才注入 CUDA 库（由 gpu_apply 统一判断）
	local rt
	rt=$(incus config get "$t" nvidia.runtime 2>/dev/null)
	[ "$rt" != "true" ] && incus config set "$t" nvidia.runtime=true >/dev/null

	# 2) 按 PCI 对齐现有设备：不在目标集合里的移除，命中的记入 kept
	local d cur removed=0 kept="" wildcard_ok=0
	[ "${#want[@]}" -eq 0 ] && wildcard_ok=1
	for d in $(gpu_devs "$t"); do
		cur=$(incus config device get "$t" "$d" pci 2>/dev/null | tr 'A-F' 'a-f')
		if [ -z "$cur" ] && [ "$wildcard_ok" -eq 1 ]; then
			kept="${kept}${d}:"$'\n'
			printf '  = %-8s (all GPUs)\n' "$d"
			continue
		fi
		if [ -n "$cur" ] && printf '%s\n' "${targets[@]}" | grep -q ":${cur}$"; then
			kept="${kept}${d}:${cur}"$'\n'
			printf '  = %-8s pci=%s  (already attached)\n' "$d" "$cur"
		else
			incus config device remove "$t" "$d" >/dev/null && removed=$((removed + 1))
			printf '  - %-8s pci=%s  (removed)\n' "$d" "${cur:-none}"
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
		printf '  + %-8s pci=%s\n' "$dev" "$want_pci"
	done

	gpu_apply "$t" "$mode"
	ok "GPU 挂载完成（卡序号: $(inst_gpu_brief "$t")）"
	inst_running "$t" && { printf '  租户侧视图:\n'; incus exec "$t" -- nvidia-smi -L 2>&1 | sed 's/^/    /'; }
}

gpu_off() {
	local t=$1 mode=$2; shift 2
	local -a want=("$@")
	local d p idx map removed=0
	map=$(gpu_pci_index_map)
	for d in $(gpu_devs "$t"); do
		p=$(incus config device get "$t" "$d" pci 2>/dev/null | tr 'A-F' 'a-f')
		if [ "${#want[@]}" -gt 0 ]; then
			idx=$(printf '%s\n' "$map" | awk -v k="$p" '$1==k{print $2; exit}')
			[ -n "$idx" ] || continue
			printf '%s\n' "${want[@]}" | grep -qx "$idx" || continue
		fi
		incus config device remove "$t" "$d" >/dev/null && {
			removed=$((removed + 1))
			printf '  - %-8s pci=%s\n' "$d" "${p:-all}"
		}
	done
	[ "$removed" -eq 0 ] && { warn "没有匹配到可摘除的 GPU（卡序号: ${want[*]:-全部}）"; return; }
	[ -z "$(gpu_devs "$t")" ] && incus config unset "$t" nvidia.runtime >/dev/null 2>&1
	gpu_apply "$t" "$mode"
	ok "已摘除 $removed 个 GPU（剩余卡序号: $(inst_gpu_brief "$t")）"
}

cmd_gpu() {
	local t=${1:-}
	case "$t" in
	""|list|all)      gpu_host_matrix all; return ;;
	free|assigned)    gpu_host_matrix "$t"; return ;;
	watch)            gpu_watch "" "${2:-2}"; return ;;
	esac
	shift
	local action=${1:-status}
	[ $# -gt 0 ] && shift
	need_instance "$t"

	# 解析 --now / --no-restart，其余为卡序号
	local mode=auto
	local -a args=()
	while [ $# -gt 0 ]; do
		case "$1" in
		--now)        mode=force ;;
		--no-restart) mode=never ;;
		*)            args+=("$1") ;;
		esac
		shift
	done

	case "$action" in
	status) gpu_show "$t" ;;
	on)     gpu_on  "$t" "$mode" "${args[@]+"${args[@]}"}" ;;
	off)    gpu_off "$t" "$mode" "${args[@]+"${args[@]}"}" ;;
	watch)  gpu_watch "$t" "${args[0]:-2}" ;;
	*)      die "用法: $0 gpu [实例] [on|off|status|watch] [卡序号...] [--now|--no-restart]" ;;
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
gpu)     cmd_gpu "${@:2}" ;;
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
