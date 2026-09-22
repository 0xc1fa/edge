#!/bin/bash
# gpu-check — GPU 设备自检（只读查询）
set -u

PASS=0
FAIL=0
ok()  { echo "  [ OK ] $*"; PASS=$((PASS + 1)); }
bad() { echo "  [FAIL] $*"; FAIL=$((FAIL + 1)); }
sec() { echo; echo "── $* ────────────────────────────────────"; }
strip_uuid() { sed 's/ (UUID: [^)]*)//'; }

HAS_SMI=0
command -v nvidia-smi >/dev/null 2>&1 && HAS_SMI=1

echo "============================================================"
echo " GPU 自检   $(date '+%F %T')   $(hostname)"
echo "============================================================"

# ---------------------------------------------------------------- 1
sec "1/5 设备节点"
if compgen -G "/dev/nvidia*" >/dev/null; then
	ls -l /dev/nvidia* | sed 's/^/  /'
	ok "设备节点存在"
else
	bad "未发现 /dev/nvidia*，GPU 未就绪"
fi

# ---------------------------------------------------------------- 2
sec "2/5 设备列表"
if [ "$HAS_SMI" -eq 1 ]; then
	if LIST=$(nvidia-smi -L 2>&1); then
		printf '%s\n' "$LIST" | strip_uuid | sed 's/^/  /'
		N=$(printf '%s\n' "$LIST" | grep -c '^GPU ')
		if [ "$N" -gt 0 ]; then
			ok "已识别 $N 张 GPU"
		else
			bad "未识别到 GPU"
		fi
	else
		printf '%s\n' "$LIST" | sed 's/^/  /'
		bad "nvidia-smi 执行失败"
	fi
else
	bad "缺少 nvidia-smi"
fi

# ---------------------------------------------------------------- 3
sec "3/5 驱动版本"
if [ "$HAS_SMI" -eq 1 ]; then
	nvidia-smi --query-gpu=index,name,driver_version --format=csv 2>&1 | sed 's/^/  /'
	ok "驱动可用"
else
	bad "无法获取驱动版本"
fi

# ---------------------------------------------------------------- 4
sec "4/5 设备状态"
if [ "$HAS_SMI" -eq 1 ]; then
	nvidia-smi --query-gpu=index,name,memory.total,memory.used,memory.free,utilization.gpu,temperature.gpu,power.draw --format=csv 2>&1 | sed 's/^/  /'
else
	echo "  跳过"
fi

# ---------------------------------------------------------------- 5
sec "5/5 CUDA 运行时"
PYF=$(mktemp /tmp/.gpuchk.XXXXXX.py 2>/dev/null || echo /tmp/.gpuchk.py)
cat >"$PYF" <<'PYEOF'
import ctypes, sys
try:
    c = ctypes.CDLL("libcuda.so.1")
    rc = c.cuInit(0)
    n = ctypes.c_int()
    rc2 = c.cuDeviceGetCount(ctypes.byref(n))
    print("cuInit            rc = %d" % rc)
    print("cuDeviceGetCount  rc = %d  | device count = %d" % (rc2, n.value))
    sys.exit(0 if (rc == 0 and rc2 == 0 and n.value > 0) else 1)
except Exception as e:
    print("FAILED: %s" % e)
    sys.exit(1)
PYEOF

OUT=$(python3 "$PYF" 2>&1)
RC=$?
printf '%s\n' "$OUT" | sed 's/^/  /'
rm -f "$PYF"
if [ "$RC" -eq 0 ]; then
	ok "CUDA 运行时可用"
else
	bad "CUDA 运行时不可用"
fi

# ---------------------------------------------------------------- 汇总
echo
echo "============================================================"
if [ "$FAIL" -eq 0 ]; then
	echo " 结论: PASS   ($PASS 项通过 / $FAIL 项失败)"
	echo "============================================================"
	exit 0
else
	echo " 结论: FAIL   ($PASS 项通过 / $FAIL 项失败)"
	echo " 请联系管理员处理"
	echo "============================================================"
	exit 1
fi
