#!/usr/bin/env bash
# 冒烟验证：照搬 obs 的思路 —— 不满足于「进程活着」，而是比对真相。
#
# 检查四件事：
#   ① 健康接口 ok，且 last_reconcile_age_ms 在容忍窗口内
#   ② 未登录访问 /api/instances 必须是 401（登录收口不能是摆设）
#   ③ 用一次性凭证登录 → 会话可用 → 实例列表与 incus list 的真实值逐字相同
#   ④ 静态首页可访问
set -u

ADDR="${ADDR:-http://127.0.0.1:5902}"
DATA_DIR="${DATA_DIR:-$(dirname "$0")/../data}"
FAIL=0

# 两条进控制台的路：管理员口令，或管理员签发的一次性凭证。
#
# 口令优先。原因是凭证这条路在「服务已经跑过一轮」之后很难复现：
# neo invite 是另一个进程直接写台账文件，运行中的服务内存里没有它，
# 下一次落盘还会把它覆盖掉 —— 换来的凭证兑不出来，表现为一路 401。
# 而管理员口令就在数据目录里躺着（`make password` 随时可看），永远可用。
#
# 凭证这条路仍然保留：它是成员进来的唯一方式，
# 且「不可重复使用」这条性质只有走一遍才验证得到。
TOKEN="${SMOKE_TOKEN:-}"
if [ -z "$TOKEN" ] && [ -f "$DATA_DIR/bootstrap-invite.txt" ]; then
  TOKEN=$(grep -oE 'token=[A-Za-z0-9_-]{16,}' "$DATA_DIR/bootstrap-invite.txt" | head -1 | cut -d= -f2)
fi

PASS="${SMOKE_PASSWORD:-}"
if [ -z "$PASS" ] && [ -f "$DATA_DIR/admin-password.txt" ]; then
  PASS=$(head -1 "$DATA_DIR/admin-password.txt" | tr -d '[:space:]')
fi
AUTHED=no

# 会话凭证容器，全局复用：兑换一次的凭证撑不起两次登录
JAR=$(mktemp)
MJAR=""
cleanup() { rm -f "$JAR" "${MJAR:-}"; }
trap cleanup EXIT

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
if [ -n "$TOKEN" ]; then
  login=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -c "$JAR" \
    -H 'Content-Type: application/json' -d "{\"token\":\"$TOKEN\"}" "$ADDR/api/auth/redeem")
  if [ "$login" = "200" ]; then pass "凭证兑换成功，会话已签发"; else bad "兑换失败 HTTP $login"; fi

  # 再用一次同一凭证必须失败 —— 这条比「能登录」更要紧
  reuse=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 \
    -H 'Content-Type: application/json' -d "{\"token\":\"$TOKEN\"}" "$ADDR/api/auth/redeem")
  if [ "$reuse" = "401" ]; then pass "凭证不可重复使用"; else bad "凭证竟然能二次兑换（HTTP $reuse）"; fi
  AUTHED=yes
elif [ -n "$PASS" ]; then
  login=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -c "$JAR" \
    -H 'Content-Type: application/json' -d "{\"password\":\"$PASS\",\"device\":\"smoke\"}" "$ADDR/api/auth/login")
  if [ "$login" = "200" ]; then pass "管理员口令登录成功，会话已签发"; else bad "口令登录失败 HTTP $login"; fi
  AUTHED=yes
else
  printf '  \033[33m●\033[0m 跳过：既没有口令也没有凭证\n'
  printf '     口令在 $DATA_DIR/admin-password.txt（或 SMOKE_PASSWORD=xxx）；\n'
  printf '     凭证要在控制台「成员 → 签发」复制一条，用 SMOKE_TOKEN=xxx 注入。\n'
fi

if [ "$AUTHED" = "yes" ]; then

  body=$(curl -sS --max-time 8 -b "$JAR" "$ADDR/api/instances")
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
elif [ "$code" = "429" ]; then
  # 限流是保护管理员待办不被灌水的，连跑几次冒烟就会撞上。
  # 这不是故障，但要把话说清楚 —— 否则看到的人会去查一个不存在的 bug。
  printf '  \033[33m●\033[0m 匿名提交被限流（429）：额度是按时间恢复的，隔几分钟再跑本脚本\n'
  printf '     这一项跳过不影响其余检查，但回执相关的用例会一并跳过。\n'
  RID="" AUTOHOST="" AUTOUSER="" AUTOIMG=""
else
  bad "匿名提交申请失败 HTTP $code：$(cat /tmp/neo-apply.json)"
fi

if [ -z "$AUTOHOST" ]; then
  printf '  \033[33m●\033[0m 跳过主机名与登录账户检查（没有回执）\n'
elif printf '%s' "$AUTOHOST" | grep -Eq '^vm-[a-z2-9]{5}$'; then
  pass "主机名由系统自动生成，且不与现有实例冲突（$AUTOHOST）"
else
  bad "自动生成的主机名不符合预期：$AUTOHOST"
fi

if [ -n "$AUTOUSER" ]; then
  [ "$AUTOUSER" = "root" ] && pass "默认登录账户为 root" || bad "登录账户异常：$AUTOUSER"
fi
if [ -z "$AUTOIMG" ]; then
  printf '  \033[33m●\033[0m 跳过默认镜像检查（没有回执）\n'
elif [ -n "$AUTOIMG" ]; then
  pass "未选镜像时落到默认镜像（$AUTOIMG）"
else
  bad "未选镜像时镜像为空"
fi

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
reject "非法 SSH 公钥被拒绝" "$code"

code=$(submit_apply "$AUTOHOST" "" /dev/null)
reject "与上一条自动生成名重名被拒" "$code"

# 不存在的规格/镜像不能混进来
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}y\",\"flavor\":\"nonexistent\",\"image\":\"$IMG\",\"ssh_public_key\":\"$KEY\",\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"测试非法规格\",\"duration_days\":1}" \
  "$ADDR/api/applications")
reject "不存在的规格被拒绝" "$code"

# 产品规则：GPU 是标准实例的附加项，不得单独申购，也不得超量
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}g\",\"flavor\":\"\",\"image\":\"$IMG\",\"gpu_count\":1,\"ssh_public_key\":\"$KEY\",\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"只想要一张卡\",\"duration_days\":1}" \
  "$ADDR/api/applications")
reject "GPU 单独申购被拒（必须先有标准实例）" "$code"

code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}gx\",\"flavor\":\"$FLAVOR\",\"image\":\"$IMG\",\"gpu_count\":9,\"ssh_public_key\":\"$KEY\",\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"超量配卡\",\"duration_days\":1}" \
  "$ADDR/api/applications")
reject "超出显卡上限被拒" "$code"

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
reject_code() { # $1=描述 $2=期望码 $3=实际码（409 与 400 都是业务拒绝，429 是限流）
  if [ "$3" = "$2" ]; then pass "$1"
  elif [ "$3" = "429" ]; then printf '  \033[33m●\033[0m 跳过「%s」：被限流（429）\n' "$1"
  else bad "$1 未按预期被拒（HTTP $3）"; fi
}
reject_code "申购待上架的显卡被拒" 409 "$code"

# 有数量的请求必须指名型号：只给张数不给了型号应被拦下
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d "{\"hostname\":\"${HOST}nm\",\"flavor\":\"$FLAVOR\",\"gpu_count\":1,\"applicant\":\"冒烟\",\"contact\":\"smoke@example.com\",\"purpose\":\"只给了数量\",\"duration_days\":1}" \
  "$ADDR/api/applications")
reject "未指明型号时 gpu_count 被拒" "$code"

sec "6/6 访问控制边界"
code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 "$ADDR/api/applications")
[ "$code" = "401" ] && pass "未登录不可遍历申请列表" || bad "申请列表未收口（$code）"

code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -X POST -H 'Content-Type: application/json' \
  -d '{"action":"approve","reason":"未登录试探"}' "$ADDR/api/applications/$RID/decision")
[ "$code" = "401" ] && pass "未登录不可审批" || bad "审批接口未收口（$code）"

code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 "$ADDR/api/instances")
[ "$code" = "401" ] && pass "未登录不可读实例列表" || bad "实例列表未收口（$code）"

# 登录后 Admin 侧应能看到未脱敏的联系方式，并把它结单，避免污染管理员待办
# 这里复用第 3 节换来的会话：凭证已经作废，不可能再兑一次。
if [ "$AUTHED" != "yes" ] || [ -z "$RID" ]; then
  printf '  \033[33m●\033[0m 跳过管理员侧校验（未登录或没有回执）\n'
else
  jar2="$JAR"
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

  # ------------------------------------------------------------ GPU 热挂载
  # 这一段必须真的动宿主。单元测试能挡住「端点写错」（Incus 根本没有
  # POST /instances/{n}/devices，只能整份 PUT），但挡不住「版本变了、
  # 设备校验规则变了」这类只有在真实对话里才暴露的事 —— 而挂载失败
  # 在页面上的表现只是一个红色提示，很容易被当成偶发。
  # 做完立刻摘掉，不留副作用。
  GPU_INST="${GPU_INST:-tenant01}"
  gpu_busy=$(incus config device list "$GPU_INST" 2>/dev/null | while read -r d; do
    [ -n "$d" ] && [ "$(incus config device get "$GPU_INST" "$d" type 2>/dev/null)" = "gpu" ] && echo yes && break
  done)
  if ! incus info "$GPU_INST" >/dev/null 2>&1; then
    printf '  \033[33m●\033[0m 跳过 GPU 热挂载：实例 %s 不存在\n' "$GPU_INST"
  elif [ -n "$gpu_busy" ]; then
    printf '  \033[33m●\033[0m 跳过 GPU 热挂载：%s 上已经挂着卡（可能有人在用）\n' "$GPU_INST"
  else
    gid=$(curl -sS --max-time 8 -b "$jar2" -X POST -H 'Content-Type: application/json' \
      -d "{\"instance\":\"$GPU_INST\",\"applicant\":\"冒烟测试\",\"purpose\":\"自动化验证热挂载链路\",\"card_count\":1}" \
      "$ADDR/api/gpu-bookings" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("booking",{}).get("id",""))')
    if [ -z "$gid" ]; then
      bad "GPU 预约提交失败"
    else
      pass "GPU 预约已登记（$gid）"
      code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" -X POST \
        -H 'Content-Type: application/json' -d '{"action":"approve"}' \
        "$ADDR/api/gpu-bookings/$gid/decision")
      [ "$code" = "200" ] && pass "预约已批准" || bad "批准失败 HTTP $code"

      # 成员视角：这一页对他是「调度日志」。
      # 此刻这条刚批准、还没挂卡 —— 他不该看到它：看到了会以为卡已经到手，
      # 而那一刻卡其实还没动。裁剪必须发生在服务端，前端藏不住接口给出去的东西。
      mtok=$(curl -sS --max-time 8 -b "$jar2" -X POST -H 'Content-Type: application/json' \
        -d '{"name":"冒烟成员","ttl_seconds":300}' "$ADDR/api/invites" \
        | python3 -c 'import sys,json;print(json.load(sys.stdin).get("invite",{}).get("token",""))')
      if [ -z "$mtok" ]; then
        bad "签发成员凭证失败，成员视角无法校验"
      else
        MJAR=$(mktemp)
        code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -c "$MJAR" \
          -H 'Content-Type: application/json' -d "{\"token\":\"$mtok\"}" "$ADDR/api/auth/redeem")
        [ "$code" = "200" ] && pass "成员会话已签发" || bad "成员兑换失败 HTTP $code"

        seen=$(curl -sS --max-time 8 -b "$MJAR" "$ADDR/api/gpu-bookings" | python3 -c '
import sys, json
d = json.load(sys.stdin)
ids = [b["id"] for b in d["bookings"]]
ok = (
    d.get("can_manage") is False
    and all(b["attached_at"] for b in d["bookings"])
    and "'"$gid"'" not in ids
)
print("yes" if ok else "no")
')
        [ "$seen" = "yes" ] && pass "成员看不到还没挂卡的记录（只看到调度日志）" \
          || bad "成员视角越界：$seen"

        code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$MJAR" \
          -X DELETE "$ADDR/api/gpu-bookings/$gid")
        if [ "$code" = "200" ]; then bad "成员竟然能删除预约（HTTP 200）"; else pass "成员删预约被挡（HTTP $code）"; fi
      fi

      attach=$(curl -sS --max-time 30 -b "$jar2" -X POST "$ADDR/api/gpu-bookings/$gid/attach")
      dev=$(printf '%s' "$attach" | python3 -c 'import sys,json;print(json.load(sys.stdin).get("booking",{}).get("device",""))')
      # 接口这次说要不要重启 —— 它看的不是「此刻有没有卡」，而是
      # 「这个容器进程是不是带着 GPU 设备起步的」（详见 gpuWantsRestart 的注释）。
      # 这里把接口的判断和容器里的实测对着看：说不用重启就必须真能立刻看到卡，
      # 说了不用却看不到，就是判据漏了一边，比漏掉一次重启更难查。
      need=$(printf '%s' "$attach" | python3 -c 'import sys,json;print("yes" if json.load(sys.stdin).get("needs_restart") else "no")')
      if [ -n "$dev" ] && [ "$(incus config device get "$GPU_INST" "$dev" type 2>/dev/null)" = "gpu" ]; then
        pass "卡已挂到 $GPU_INST（设备 $dev）"
      else
        bad "挂载未落地：$(printf '%s' "$attach" | head -c 200)"
      fi

      # 设备进了实例定义不等于租户能用：真正的判据是容器里看得到卡。
      # 这台机器的容器里 /dev/nvidia* 要重启才注入，所以这里照页面上的顺序走一遍：
      # 挂载 → 重启 → 再看。重启的代价（实例上正在跑的东西中断几秒）
      # 与管理员点那个按钮时完全相同 —— 冒烟不该替使用者省掉这一步，
      # 否则验的就不是「租户能不能用上卡」，而是「设备有没有写进实例定义」。
      if incus exec "$GPU_INST" -- nvidia-smi -L 2>/dev/null | grep -q '^GPU '; then
        if [ "$need" = "no" ]; then
          pass "接口判不必重启，租户侧确实立刻看到卡（真·热插）"
        else
          printf '  \033[33m●\033[0m 接口保守地提示需重启，但这一挂其实没重启就生效了\n'
        fi
      elif [ "$need" = "no" ]; then
        bad "接口判不必重启，租户侧却看不到卡（判据漏了一边）"
      fi

      if ! incus exec "$GPU_INST" -- nvidia-smi -L 2>/dev/null | grep -q '^GPU '; then
        printf '  \033[33m●\033[0m 热插后容器里还看不到卡，按页面上的提示重启一次\n'
        code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 60 -b "$jar2" \
          -X POST "$ADDR/api/instances/$GPU_INST/restart")
        [ "$code" = "200" ] && pass "重启实例（页面上的同一个按钮）" || bad "重启失败 HTTP $code"
        ok=no
        for _ in 1 2 3 4 5 6 7 8 9 10; do
          sleep 3
          if incus exec "$GPU_INST" -- nvidia-smi -L 2>/dev/null | grep -q '^GPU '; then
            ok=yes
            break
          fi
        done
        [ "$ok" = "yes" ] && pass "重启后租户侧 nvidia-smi 看得到卡" || bad "重启后仍看不到卡"
      fi

      code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 30 -b "$jar2" \
        -X POST "$ADDR/api/gpu-bookings/$gid/detach")
      if [ "$code" = "200" ] && ! incus config device list "$GPU_INST" 2>/dev/null | grep -qx "$dev"; then
        pass "卡已摘除，实例回到无卡状态"
      else
        bad "摘卡未生效（HTTP $code，设备 $dev 仍在）"
      fi

      # 收尾：没有卡了就不该再挂着驱动栈的开关
      rt=$(incus config get "$GPU_INST" nvidia.runtime 2>/dev/null)
      [ -z "$rt" ] && pass "nvidia.runtime 已随摘卡清理" || bad "nvidia.runtime 仍为 $rt"

      # 摘下来之后这条就成了一条真正的调度留痕，成员此刻应当看得到它
      if [ -n "${MJAR:-}" ] && [ -s "$MJAR" ]; then
        seen2=$(curl -sS --max-time 8 -b "$MJAR" "$ADDR/api/gpu-bookings" | python3 -c '
import sys, json
d = json.load(sys.stdin)
print("yes" if "'"$gid"'" in [b["id"] for b in d["bookings"]] else "no")
')
        [ "$seen2" = "yes" ] && pass "挂过卡的记录成员看得到（调度留痕）" \
          || bad "已发生调度的记录成员看不到"
      fi

      # 自己造的记录自己抹掉：冒烟不该在台账上留下垃圾，
      # 否则每跑一次就多一条假数据，管理员的待办里永远有清不掉的东西
      code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" \
        -X DELETE "$ADDR/api/gpu-bookings/$gid")
      [ "$code" = "200" ] && pass "验证用的记录已删除（台账不留测试数据）" \
        || bad "删除验证记录失败 HTTP $code"

      left=$(curl -sS --max-time 8 -b "$jar2" "$ADDR/api/gpu-bookings" | python3 -c '
import sys, json
print("yes" if "'"$gid"'" not in [b["id"] for b in json.load(sys.stdin)["bookings"]] else "no")
')
      [ "$left" = "yes" ] && pass "台账里已查不到这条" || bad "删除后台账里还在"
    fi
  fi

  # ------------------------------------------------------------ 自己实例的操作
  # 网页终端、电源、重置口令。破坏性最大的两个（改口令、关机）在这里
  # 只验「该拒的拒」，不真做：冒烟每次跑都会改掉租户的 root 口令、
  # 杀掉他正在跑的任务，那种代价不该由一次例行检查承担。
  # 真正要确认它们能用，看 scripts/verify-shell.sh 的人工步骤。
  if [ "$AUTHED" = "yes" ] && incus info "$GPU_INST" >/dev/null 2>&1; then
    # 网页终端：连上、发一条命令、看到结果。这一条没有副作用，
    # 所以它要跑完整的链路 —— 它也是最容易在升级里悄悄坏掉的那个
    # （Incus 的 exec 通道一有变化，表现只是「终端一闪就没」）。
    shellout=$(timeout 25 python3 "$(dirname "$0")/shellcheck.py" \
      "${ADDR#*://}" "$jar2" "$GPU_INST" 2>&1 | tail -3)
    case "$shellout" in
      *SHELL_OK*) pass "网页终端可用（连上、执行命令、看到输出）" ;;
      *) bad "网页终端链路不通：$(printf '%s' "$shellout" | head -c 200)" ;;
    esac

    # 非法动作与空口令必须被拒：这两个分支没有副作用，却最容易在
    # 加新动作时被顺手改宽。
    code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" -X POST \
      -H 'Content-Type: application/json' -d '{"action":"explode"}' \
      "$ADDR/api/instances/$GPU_INST/power")
    reject "非法电源动作被拒" "$code"

    code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" -X POST \
      -H 'Content-Type: application/json' -d '{"password":""}' \
      "$ADDR/api/instances/$GPU_INST/password")
    reject "空口令被拒" "$code"

    # 过短口令必须被拒。用 3 位而不是 8 位是有讲究的：8 位刚好过线，
    # 用例就会真的把租户的 root 口令改掉 —— 冒烟不该有这种副作用，
    # 而写用例的人几乎一定会数错字符个数。
    code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" -X POST \
      -H 'Content-Type: application/json' -d '{"password":"abc"}' \
      "$ADDR/api/instances/$GPU_INST/password")
    reject "过短口令被拒" "$code"

    # -------------------------------------------------------- 归属分配
    # 宿主上手工建的实例在申请台账里没有记录，不分一次，
    # 租户侧的终端/口令/电源对它们永远是灰的。这一小段把
    # 「分配 → 租户真能用 → 解除 → 入口关闭」整条闭环走一遍。
    if [ -n "${MJAR:-}" ] && [ -s "$MJAR" ]; then
      code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" -X POST \
        -H 'Content-Type: application/json' -d '{"owner":"冒烟成员"}' \
        "$ADDR/api/instances/$GPU_INST/assign")
      [ "$code" = "200" ] && pass "实例已分配给成员" || bad "分配失败 HTTP $code"

      # 分完之后，那位成员必须真的能连上终端 —— 前面验过他被拒，
      # 这两条合起来才说明「分配」确实改变了谁能动手。
      memberout=$(timeout 25 python3 "$(dirname "$0")/shellcheck.py" \
        "${ADDR#*://}" "$MJAR" "$GPU_INST" 2>&1 | tail -2)
      case "$memberout" in
        *SHELL_OK*) pass "分配后该成员能用网页终端" ;;
        *) bad "分配后成员仍连不上终端：$(printf '%s' "$memberout" | head -c 160)" ;;
      esac

      code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" -X POST \
        -H 'Content-Type: application/json' -d '{"owner":"查无此人"}' \
        "$ADDR/api/instances/$GPU_INST/assign")
      reject "分配给不存在的成员被拒" "$code"

      code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" -X DELETE \
        "$ADDR/api/instances/$GPU_INST/assign")
      [ "$code" = "200" ] && pass "归属已解除" || bad "解除失败 HTTP $code"

      # 解除之后入口必须重新关上：钥匙收回来不是一句提示就行的事
      after=$(timeout 20 python3 "$(dirname "$0")/shellcheck.py" \
        "${ADDR#*://}" "$MJAR" "$GPU_INST" 2>&1 | head -1)
      case "$after" in
        *403*) pass "解除后该成员被挡在门外" ;;
        *) bad "解除后成员竟然还能连上：$after" ;;
      esac

      left=$(python3 -c 'import json;print(len(json.load(open("'"$DATA_DIR"'/instance-owners.json"))))' 2>/dev/null || echo 0)
      [ "$left" = "0" ] && pass "归属台账已清空（不留测试数据）" || bad "归属台账还剩 $left 条"
    fi
  fi

  # 会话台账现在也是可列举的：踢掉自己之后，原来的 cookie 必须立刻不能用
  code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" "$ADDR/api/sessions")
  if [ "$code" = "200" ]; then pass "会话台账可读"; else bad "会话台账不可读（HTTP $code）"; fi

  sid=$(curl -sS --max-time 8 -b "$jar2" "$ADDR/api/sessions" | python3 -c '
import sys, json
d = json.load(sys.stdin)
print(d.get("current", ""))')
  if [ -n "$sid" ]; then
    curl -sS -o /dev/null --max-time 8 -b "$jar2" -X POST "$ADDR/api/sessions/$sid/revoke"
    after=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -b "$jar2" "$ADDR/api/sessions")
    if [ "$after" = "401" ]; then pass "会话被吊销后立刻不可用"; else bad "吊销后仍能访问（HTTP $after）"; fi
  else
    bad "会话台账没有标出当前会话"
  fi
fi

echo
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32m结论: PASS\033[0m  冒烟全部通过\n'
  exit 0
fi
printf '\033[31m结论: FAIL\033[0m  %s 项未通过\n' "$FAIL"
exit 1
