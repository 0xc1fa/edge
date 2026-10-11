#!/usr/bin/env bash
# 冒烟验证：照搬 obs 的思路 —— 不满足于「进程活着」，而是比对真相。
#
# 检查四件事：
#   ① 健康接口 ok，且 last_reconcile_age_ms 在容忍窗口内
#   ② 未登录访问 /api/instances 必须是 401（登录收口不能是摆设）
#   ③ 用 bootstrap 口令登录 → 会话可用 → 实例列表与 incus list 的真实值逐字相同
#   ④ 静态首页可访问
set -u

ADDR="${ADDR:-http://127.0.0.1:5902}"
DATA_DIR="${DATA_DIR:-$(dirname "$0")/../data}"
FAIL=0

# 口令只在脚本开头读一次：首次登录成功后后端会销毁 bootstrap 文件，
# 放到后面再读就取不到了。也支持外部注入 SMOKE_PASSWORD=xxx。
PW="${SMOKE_PASSWORD:-}"
[ -f "$DATA_DIR/bootstrap-password.txt" ] && PW=$(tr -d '\r\n' < "$DATA_DIR/bootstrap-password.txt")

pass() { printf '  \033[32m✔\033[0m %s\n' "$1"; }
bad()  { printf '  \033[31m✘\033[0m %s\n' "$1"; FAIL=$((FAIL + 1)); }
sec()  { printf '\n\033[1m%s\033[0m\n' "$1"; }

sec "1/4 健康接口"
health=$(curl -sS --max-time 8 "$ADDR/api/health" 2>/dev/null) || { bad "无法访问 $ADDR"; exit 1; }
ok=$(printf '%s' "$health" | python3 -c 'import sys,json;print(json.load(sys.stdin)["ok"])')
age=$(printf '%s' "$health" | python3 -c 'import sys,json;print(json.load(sys.stdin)["last_reconcile_age_ms"])')
count=$(printf '%s' "$health" | python3 -c 'import sys,json;print(json.load(sys.stdin)["instance_count"])')
if [ "$ok" = "True" ]; then pass "数据链路健康（实例 $count 台，最近同步 ${age}ms 前）"; else bad "ok=$ok 原始响应: $health"; fi
if [ "$age" -lt 60000 ]; then pass "最近同步在 60s 内"; else bad "同步延迟 ${age}ms，已超过容忍阈值"; fi

sec "2/4 未登录收口"
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 "$ADDR/api/instances")
if [ "$code" = "401" ]; then pass "未登录访问实例列表返回 401"; else bad "未登录返回 $code，期望 401"; fi

sec "3/4 登录并比对真相"
if [ -z "$PW" ]; then
  printf '  \033[33m●\033[0m 跳过：未拿到口令（bootstrap 文件已销毁且未设 SMOKE_PASSWORD）\n'
  printf '     重设口令：docker compose down && rm -f %s/secrets.json && docker compose up -d\n' "$DATA_DIR"
else
  jar=$(mktemp)
  login=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -c "$jar" \
    -H 'Content-Type: application/json' -d "{\"password\":\"$PW\"}" "$ADDR/api/auth/login")
  if [ "$login" = "200" ]; then pass "登录成功，会话已签发"; else bad "登录失败 HTTP $login"; fi

  body=$(curl -sS --max-time 8 -b "$jar" "$ADDR/api/instances")
  web_ip=$(printf '%s' "$body" | python3 -c '
import sys, json
items = json.load(sys.stdin)["instances"]
for it in items:
    if it["name"] == "tenant01":
        print(it["nics"][0]["ipv4"] if it["nics"] else "")
')
  true_ip=$(incus list tenant01 --format=csv -c 4 2>/dev/null | cut -d' ' -f1)
  web_cpu=$(printf '%s' "$body" | python3 -c '
import sys, json
items = json.load(sys.stdin)["instances"]
print([it["quota_cpu"] for it in items if it["name"] == "tenant01"][0])
')
  true_cpu=$(incus config get tenant01 limits.cpu 2>/dev/null)

  [ -n "$web_ip" ] && [ "$web_ip" = "$true_ip" ] \
    && pass "tenant01 内网 IP 与 incus list 一致（$web_ip）" \
    || bad "IP 不一致：页面 $web_ip / incus $true_ip"
  [ "$web_cpu" = "$true_cpu" ] \
    && pass "tenant01 CPU 配额与 incus config 一致（$web_cpu）" \
    || bad "CPU 配额不一致：页面 $web_cpu / incus $true_cpu"

  rm -f "$jar"
fi

sec "4/4 静态页面"
home=$(curl -sS --max-time 8 -o /dev/null -w '%{http_code}' "$ADDR/")
if [ "$home" = "200" ]; then pass "首页可访问"; else bad "首页 HTTP $home"; fi

# ---------------------------------------------------------------- 5/N 公开申请与访问控制
sec "5/6 公开站点：匿名可用"
code=$(curl -sS -o /tmp/neo-catalog.json -w '%{http_code}' --max-time 8 "$ADDR/api/catalog")
[ "$code" = "200" ] && pass "匿名可读售卖目录" || bad "售卖目录不可读（HTTP $code）"

FLAVOR=$(python3 -c '
import sys, json
c = json.load(open("/tmp/neo-catalog.json"))
ks = [f["name"] for f in c["flavors"] if f["available"]]
print(ks[0] if ks else "")
')
IMG=$(python3 -c '
import sys, json
c = json.load(open("/tmp/neo-catalog.json"))
ks = [i["id"] for i in c["images"] if i["available"]]
print(ks[0] if ks else "")
')
if [ -n "$FLAVOR" ] && [ -n "$IMG" ]; then
  pass "取到可售项：$FLAVOR + $IMG"
else
  bad "可取目录里没有可售规格或镜像"
fi

KEY='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGgvyMGVbHlqJ8W0UKSwNwfWfAqB9tRzR1pQ2nZ3k7yF smoke@neo'
HOST="smoke$(date +%m%d%H%M%S)"

# 主机名与公钥都是选填：这里刻意两个都不填，验证系统自动生成这条路走得通
submit_apply() {
  local host="$1" key="$2" out="$3"
  local extras=""
  [ -n "$host" ] && extras="$extras,\"hostname\":\"$host\""
  [ -n "$key" ] && extras="$extras,\"ssh_public_key\":\"$key\""
  local body
  body=$(printf '{"flavor":"%s","image":"%s","gpu_count":0%s,"applicant":"冒烟测试","contact":"smoke@example.com","purpose":"自动化冒烟验证申请链路","duration_days":30}' \
    "$FLAVOR" "$IMG" "$extras")
  curl -sS -o "$out" -w '%{http_code}' --max-time 8 -X POST \
    -H 'Content-Type: application/json' -d "$body" "$ADDR/api/applications"
}

code=$(submit_apply "" "" /tmp/neo-apply.json)
if [ "$code" = "201" ]; then
  read RID AUTOHOST AUTOUSER AUTOIMG < <(python3 -c '
import json
a = json.load(open("/tmp/neo-apply.json"))["application"]
print(a["id"], a["hostname"], a.get("login_user",""), a.get("image",""))
')
  pass "匿名提交申请成功，回执 $RID"
else
  bad "匿名提交申请失败 HTTP $code：$(cat /tmp/neo-apply.json)"
fi

if [ -n "$AUTOHOST" ] && printf '%s' "$AUTOHOST" | grep -Eq '^vm-[a-z2-9]{5}$'; then
  pass "主机名由系统自动生成，且不与现有实例冲突（$AUTOHOST）"
else
  bad "自动生成的主机名不符合预期：$AUTOHOST"
fi

[ "$AUTOUSER" = "root" ] && pass "默认登录账户为 root" || bad "登录账户异常：$AUTOUSER"
[ -n "$AUTOIMG" ] && pass "未选镜像时落到默认镜像（$AUTOIMG）" || bad "未选镜像时镜像为空"

if [ -n "$RID" ]; then
  # 回执查询：必须可用，且必须脱敏。
  # 联系方式恒脱敏；公钥选填——填了才需要出现脱敏标记，空表示「用随机口令」。
  # 另外整份响应不得泄露完整联系方式，避免回执号泄露即信息泄露。
  code=$(curl -sS -o /tmp/neo-track.json -w '%{http_code}' --max-time 8 "$ADDR/api/applications/$RID")
  masked=$(python3 -c '
import json
a = json.load(open("/tmp/neo-track.json"))["application"]
key = a["ssh_public_key"]
# 公钥要么为空（随机口令），要么带脱敏标记；绝不能返回完整公钥
key_ok = (key == "") or ("脱敏" in key)
print(("***" in a["contact"]) and key_ok and ("smoke@example.com" not in a["contact"]))
')
  if [ "$code" = "200" ] && [ "$masked" = "True" ]; then
    pass "凭回执可查进度，且联系方式与公钥已脱敏"
  else
    bad "回执查询异常：HTTP $code / 脱敏=$masked"
  fi
fi

# 非法输入必须被服务端挡住，而不是写进台账
code=$(submit_apply "" "not-a-real-key" /dev/null)
[ "$code" = "400" ] && pass "非法 SSH 公钥被拒绝" || bad "非法公钥未被拦下（HTTP $code）"

code=$(submit_apply "$AUTOHOST" "" /dev/null)
[ "$code" = "400" ] && pass "与上一条自动生成名重名被拒" || bad "重名主机名未被拦下（HTTP $code）"

# 不存在的规格/镜像不能混进来
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}y\",\"flavor\":\"nonexistent\",\"image\":\"$IMG\",\"ssh_public_key\":\"$KEY\",\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"测试非法规格\",\"duration_days\":1}" \
  "$ADDR/api/applications")
[ "$code" = "400" ] && pass "不存在的规格被拒绝" || bad "不存在的规格返回了 $code"

# 产品规则：GPU 是标准实例的附加项，不得单独申购，也不得超量
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}g\",\"flavor\":\"\",\"image\":\"$IMG\",\"gpu_count\":1,\"ssh_public_key\":\"$KEY\",\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"只想要一张卡\",\"duration_days\":1}" \
  "$ADDR/api/applications")
[ "$code" = "400" ] && pass "GPU 单独申购被拒（必须先有标准实例）" || bad "GPU 竟可单独申购（$code）"

code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}gx\",\"flavor\":\"$FLAVOR\",\"image\":\"$IMG\",\"gpu_count\":9,\"ssh_public_key\":\"$KEY\",\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"超量配卡\",\"duration_days\":1}" \
  "$ADDR/api/applications")
[ "$code" = "400" ] && pass "超出显卡上限被拒" || bad "超量配卡未被拦下（$code）"

GPUOPTS=$(python3 -c 'import json;print(len(json.load(open("/tmp/neo-catalog.json"))["gpu_models"]))')
[ "$GPUOPTS" -gt 0 ] && pass "目录按型号返回显卡（$GPUOPTS 款）" || bad "目录缺少显卡型号"

# 库存口径：0 库存是「待上架」（还没上货架），与「已被全部占用」（上过但卖完）是两回事
PROOK=$(python3 -c '
import json
ms = json.load(open("/tmp/neo-catalog.json"))["gpu_models"]
p = [m for m in ms if m["id"] == "rtx-pro-6000"]
print(p and (not p[0]["available"]) and p[0]["reason"] == "待上架" and p[0]["available_cards"] == 0)
')
[ "$PROOK" = "True" ] && pass "零库存型号标为待上架（不是售罄）" || bad "零库存型号的状态不对：$(python3 -c 'import json;print([m for m in json.load(open("/tmp/neo-catalog.json"))["gpu_models"] if m["id"]=="rtx-pro-6000"])')"

# 没装的卡不能下单：避免页面（或脚本）绕过前端把没货的东西提交进来
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}pro\",\"flavor\":\"$FLAVOR\",\"gpu_model\":\"rtx-pro-6000\",\"gpu_count\":1,\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"试探没装的卡\",\"duration_days\":1}" \
  "$ADDR/api/applications")
[ "$code" = "409" ] && pass "申购待上架的显卡被拒" || bad "未安装的显卡竟可下单（HTTP $code）"

# 有数量的请求必须指名型号：只给张数不给了型号应被拦下
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}nm\",\"flavor\":\"$FLAVOR\",\"gpu_count\":1,\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"只给了数量\",\"duration_days\":1}" \
  "$ADDR/api/applications")
[ "$code" = "400" ] && pass "未指明型号时 gpu_count 被拒" || bad "缺型号的请求未被拦下（HTTP $code）"

sec "6/6 访问控制边界"
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 "$ADDR/api/applications")
[ "$code" = "401" ] && pass "未登录不可遍历申请列表" || bad "申请列表未收口（$code）"

code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d '{"action":"approve","reason":"未登录试探"}' "$ADDR/api/applications/$RID/decision")
[ "$code" = "401" ] && pass "未登录不可审批" || bad "审批接口未收口（$code）"

code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 "$ADDR/api/instances")
[ "$code" = "401" ] && pass "未登录不可读实例列表" || bad "实例列表未收口（$code）"

# 登录后 Admin 侧应能看到未脱敏的联系方式，并把它结单，避免污染管理员待办
if [ -z "$PW" ] || [ -z "$RID" ]; then
  printf '  \033[33m●\033[0m 跳过管理员侧校验（无口令或回执）\n'
else
  jar2=$(mktemp)
  curl -sS -o /dev/null --max-time 8 -c "$jar2" -H 'Content-Type: application/json' \
    -d "{\"password\":\"$PW\"}" "$ADDR/api/auth/login"
  raw=$(curl -sS --max-time 8 -b "$jar2" "$ADDR/api/applications" | python3 -c '
import sys, json
a = json.load(sys.stdin)["applications"]
mine = [x for x in a if x["id"] == "'"$RID"'"]
print("yes" if (mine and mine[0]["contact"] == "smoke@example.com") else "no")
')
  [ "$raw" = "yes" ] && pass "管理员可看到完整联系方式" || bad "管理员看到的联系方式异常（$raw）"

  code=$(curl -sS -o /tmp/neo-decision.json -w '%{http_code}' --max-time 8 -b "$jar2" \
    -X POST -H 'Content-Type: application/json' \
    -d '{"action":"reject","reason":"冒烟测试自动关闭"}' "$ADDR/api/applications/$RID/decision")
  if [ "$code" = "200" ]; then
    pass "管理员处理成功，台账已结单（待办不再堆积）"
  else
    bad "管理员处理失败 HTTP $code：$(cat /tmp/neo-decision.json)"
  fi
  rm -f "$jar2"
fi

echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m结论: PASS\033[0m  冒烟全部通过\n'
  exit 0
fi
printf '\033[31m结论: FAIL\033[0m  %s 项未通过\n' "$FAIL"
exit 1
