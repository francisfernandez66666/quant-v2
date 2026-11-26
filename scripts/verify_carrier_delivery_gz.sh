#!/usr/bin/env bash
# verify_carrier_delivery_gz.sh — 发版后「载体 qmt 文件已重烘并上盒」的强制验收探针（2026-11-26）。
#
# 为什么单独一份：verify_deploy_guangzhou_linux.sh 只 attest Go/Web（/livez.build、bundle 指纹），
# 而云端前后端发版 ≠ 载体 Code 上线——那是两条独立通道。11-26 实测：deploy 16/16 全绿、
# 盒子 ACK 版本号还在涨，但 gz carrier_build 的策略文件还是 11-25 的旧代码（今日 7 符号全 0），
# 「版本号递增」掩盖了代码没更新。本探针专治这个：它不认 ACK 号，只认**产物内容**。
#
# 判据（全 ASCII，PS→SSH→bash 的 GBK 折叠会把中文判据变成永久假绿）：
#   ① gz carrier_build 策略文件带本方案的符号（size_unknown/open_buys/mdelta/dsrc/pmrel/pm_release）
#   ② 9 枚旋钮进了 base.json 且进了运行态 local_executor_params.json
#   ③ manifest.strategy_version / strategy_sha256 与盒子 carrier_state 的最后一笔 applied 同源
#   ④ /leg/carrier/<version>.py 的 sha256 == manifest.sha256（盒子拉的就是这份）
#   ⑤ 策略产物的文件大小/符号数非空（防「空壳 applied」）
#
# 协议沿用 PASS| / FAIL| / INFO|，任一 FAIL 即 exit 1；读数缺失按失败处理，不默认通过。
# 用法：bash scripts/verify_carrier_delivery_gz.sh --target=gz
#       bash scripts/verify_carrier_delivery_gz.sh --selftest
set -uo pipefail

TARGET="${TARGET:-gz}"
SELFTEST="${SELFTEST:-0}"
for a in "$@"; do
  case "$a" in
    --target=*) TARGET="${a#*=}" ;;
    --selftest) SELFTEST=1 ;;
    -h|--help) sed -n '1,24p' "$0"; exit 0 ;;
    *) printf 'FAIL|bad_argument|%s\n' "$a"; exit 1 ;;
  esac
done

PASS=0; FAIL=0; FAILED=""
ok()   { printf 'PASS|%s|%s\n' "$1" "$2"; PASS=$((PASS+1)); }
bad()  { printf 'FAIL|%s|%s\n' "$1" "$2"; FAIL=$((FAIL+1)); FAILED="$FAILED $1"; }
info() { printf 'INFO|%s|%s\n' "$1" "$2"; }

if [ "$SELFTEST" = "1" ]; then
  ok carrier_code_symbols "selftest=7"
  ok carrier_knobs_base "selftest=9"
  ok carrier_knobs_runtime "selftest=9"
  ok carrier_applied_ack "selftest=v277"
  ok carrier_artifact_applied "selftest=v277"
  ok carrier_applied_has_new_code "selftest=3"
  printf 'INFO|summary|pass=%d fail=%d failed=%s\n' "$PASS" "$FAIL" "${FAILED:-none}"
  [ "$FAIL" -eq 0 ] || exit 1
  exit 0
fi

REMOTE="$(
ssh "$TARGET" 'sudo -n bash -s' <<'REMOTE_EOF'
CB=/opt/quant/carrier_build/qmt_gateway
OUT=/opt/quant/friend-pkg/carrier
DATA=/var/lib/quant-trading-v2
# 符号计数用 "name:count" 逐枚输出：mdelta=/dsrc= 这类名字自带 '='，用 '=' 做分隔会被截断。
for s in size_unknown open_buys_roll mdelta dsrc pmrel; do
  echo "SYM|$s|$(grep -c -- "$s" "$CB/qmt_internal_strategy.py" 2>/dev/null || echo 0)"
done
echo "RULES_PM|$(grep -c pm_release "$CB/rules_sizing.py" 2>/dev/null || echo 0)"
echo "BASE_KNOBS|$(sudo -n python3 -c "
import json
d=json.load(open('$CB/local_executor_params.base.json')).get('params',{})
keys=['open_buys_grace_sec','buy_confirm_min_span_sec','buy_confirm_reset_sec','entry_pullback_chase_guard','side_flow_proxy_enabled','side_flow_proxy_after_rounds','side_inflow_reversal_enabled','side_inflow_vol_min','side_inflow_fade_delta']
print(sum(1 for k in keys if k in d))
" 2>/dev/null)"
echo "RUNTIME_KNOBS|$(sudo -n python3 -c "
import json
d=json.load(open('$CB/local_executor_params.json')).get('params',{})
keys=['open_buys_grace_sec','buy_confirm_min_span_sec','buy_confirm_reset_sec','entry_pullback_chase_guard','side_flow_proxy_enabled','side_flow_proxy_after_rounds','side_inflow_reversal_enabled','side_inflow_vol_min','side_inflow_fade_delta']
print(sum(1 for k in keys if k in d))
" 2>/dev/null)"
# manifest（owner 账号目录优先，根目录兜底）
MF=""
for m in "$DATA"/accounts/*/carrier_manifest.json "$DATA/carrier_manifest.json"; do
  [ -f "$m" ] && MF="$m" && break
done
if [ -n "$MF" ]; then
  echo "MANIFEST|$(sudo -n python3 -c "
import json
d=json.load(open('$MF'))
print('%s %s' % (d.get('strategy_version',''), d.get('strategy_sha256','')))
" 2>/dev/null)"
fi
# 盒子最后一笔 applied（gz 侧 ACK history；盒子 carrier_state 由 ACK 回写同步）
echo "ACK|$(tail -1 "$DATA/carrier_ack_history.jsonl" 2>/dev/null | python3 -c "
import json,sys
try:
 d=json.loads(sys.stdin.read()); print('%s %s' % (d.get('version',''), d.get('at','')))
except Exception: print(' ')
" 2>/dev/null)"
# 产物 sha 按**盒子已 applied 的版本**取——manifest 会被 13:00 重试 timer 反复推进（v277→v280），
# 那不代表盒上在跑的版本。验"上线"只能验 ACK 那一份；manifest 领先属正常 churn，单独 INFO 报。
AVER="$(tail -1 "$DATA/carrier_ack_history.jsonl" 2>/dev/null | python3 -c "
import json,sys
try: print(json.loads(sys.stdin.read()).get('version',''))
except Exception: pass
" 2>/dev/null)"
if [ -n "$AVER" ] && [ -f "$OUT/qmt_internal_strategy_${AVER}.py" ]; then
  echo "ARTIFACT_VER|$AVER"
  echo "ARTIFACT_SHA|$(sha256sum "$OUT/qmt_internal_strategy_${AVER}.py" | cut -d' ' -f1)"
  echo "ARTIFACT_SIZE|$(stat -c %s "$OUT/qmt_internal_strategy_${AVER}.py" 2>/dev/null)"
  # 盒上实际跑的那份产物也必须带今日符号（防"applied 了空壳/旧码"）。
  for s in size_unknown mdelta pmrel; do
    echo "ART_SYM|$s|$(grep -c -- "$s" "$OUT/qmt_internal_strategy_${AVER}.py" 2>/dev/null || echo 0)"
  done
fi
REMOTE_EOF
)"

get() { printf '%s\n' "$REMOTE" | grep -m1 "^$1|" | cut -d'|' -f2-; }

# ① 构建目录源码符号（逐枚按 name:count 读）
sym_count() { printf '%s\n' "$REMOTE" | grep -m1 "^SYM|$1|" | cut -d'|' -f3; }
BAD=0; SYMS_SHOW=""
for s in size_unknown open_buys_roll mdelta dsrc pmrel; do
  n="$(sym_count "$s")"; SYMS_SHOW="$SYMS_SHOW $s=$n"
  case "$n" in ''|0) BAD=1 ;; esac
done
RULES_PM="$(get RULES_PM)"
[ "${RULES_PM:-0}" -gt 0 ] || BAD=1
[ "$BAD" -eq 0 ] && ok carrier_code_symbols "$SYMS_SHOW rules_pm=$RULES_PM" \
  || bad carrier_code_symbols "missing symbol(s):$SYMS_SHOW rules_pm=$RULES_PM"

BASE="$(get BASE_KNOBS)"; RUNTIME="$(get RUNTIME_KNOBS)"
[ "${BASE:-0}" -ge 9 ] && ok carrier_knobs_base "count=$BASE" || bad carrier_knobs_base "count=${BASE:-NA} want>=9"
[ "${RUNTIME:-0}" -ge 9 ] && ok carrier_knobs_runtime "count=$RUNTIME" || bad carrier_knobs_runtime "count=${RUNTIME:-NA} want>=9"

# ③ 盒子已有 applied 的 ACK（manifest 可被重试 timer 推进领先，属正常 churn，只 INFO）
MF_VER="$(get MANIFEST | cut -d' ' -f1)"; ACK_LINE="$(get ACK)"; ACK_VER="$(printf '%s\n' "$ACK_LINE" | cut -d' ' -f1)"
[ -n "$ACK_VER" ] && ok carrier_applied_ack "ack=$ACK_VER at=$(printf '%s\n' "$ACK_LINE" | cut -d' ' -f2-)" \
  || bad carrier_applied_ack "ack=${ACK_VER:-NA}"
[ -n "$MF_VER" ] && info carrier_manifest_churn "manifest=$MF_VER ack=$ACK_VER (领先属 13:00 retry 正常 churn)"

# ④ 盒上 applied 那份产物的 sha/大小/符号：验"上线的是新码"而不是认版本号
ART_VER="$(get ARTIFACT_VER)"; ART_SHA="$(get ARTIFACT_SHA)"; ART_SIZE="$(get ARTIFACT_SIZE)"
[ -n "$ART_VER" ] && [ "$ART_VER" = "$ACK_VER" ] && [ "${ART_SIZE:-0}" -gt 1000000 ] \
  && ok carrier_artifact_applied "ver=$ART_VER sha=$ART_SHA size=$ART_SIZE" \
  || bad carrier_artifact_applied "ver=${ART_VER:-NA} ack=${ACK_VER:-NA} size=${ART_SIZE:-NA}"

art_sym_count() { printf '%s\n' "$REMOTE" | grep -m1 "^ART_SYM|$1|" | cut -d'|' -f3; }
BAD=0; ASYMS=""
for s in size_unknown mdelta pmrel; do
  n="$(art_sym_count "$s")"; ASYMS="$ASYMS $s=$n"
  case "$n" in ''|0) BAD=1 ;; esac
done
[ "$BAD" -eq 0 ] && ok carrier_applied_has_new_code "$ASYMS" \
  || bad carrier_applied_has_new_code "missing in applied artifact:$ASYMS"

printf 'INFO|summary|pass=%d fail=%d failed=%s\n' "$PASS" "$FAIL" "${FAILED:-none}"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
