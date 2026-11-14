#!/usr/bin/env bash
# verify_deploy_guangzhou_linux.sh — 广州 Ubuntu 外脑部署后的 Linux 版验证探针（§6.6 工单 ①）
#
# 为什么另写一份：scripts/verify_deploy_guangzhou.sh 的 38 探针全部靠 quoted heredoc 生成
# verify_probes.ps1 上传执行，NSSM 服务 / schtasks / 注册表 / C: 余量都是 Windows 专属，
# 重装成 Ubuntu 后一条也跑不了。本脚本走 bash + ssh 直跑，判据与锚点全 ASCII
# （PS→SSH→bash 的 GBK 折叠会把中文判据变成永久假绿，AGENTS.md 运维口径钉过这条）。
#
# 协议沿用 PASS| / FAIL| / INFO|，任一 FAIL 即 exit 1；**「探针没跑到」不算 PASS**：
# 读数缺失时 get() 交出空串，判据按失败处理，不按「默认通过」。
#
# 覆盖：四 unit active · 备份 timer · / 余量 · 二进制 build_commit · 前端 bundle 指纹 ·
#       Caddy HTTPS 首页 · /setup 404 · 匿名读面 401 · TZ=CST · journald 启动期 ERROR。
#
# ★ 变异反证（防恒绿假探针）：--selftest 用内置全绿读数跑一遍证明判据放行，
#   再注入 unit_quant 失败跑一遍证明 FAIL 计数真的会红、exit 真的非 0。
#   真机上更硬的一档：停掉一个 unit 后跑本脚本，必须出现 FAIL|<那个 unit>。
#   也可用 VERIFY_MUTATE=<probe_id> 手工给指定探针注入一次失败。
#
# 用法：
#   bash scripts/verify_deploy_guangzhou_linux.sh --selftest
#   bash scripts/verify_deploy_guangzhou_linux.sh --target=gz --expect-commit=<sha>
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TARGET="gz"
EXPECT_COMMIT=""
SELFTEST=0
MUTATE="${VERIFY_MUTATE:-}"

for a in "$@"; do
  case "$a" in
    --target=*) TARGET="${a#*=}" ;;
    --expect-commit=*) EXPECT_COMMIT="${a#*=}" ;;
    --expect-qmt-enabled=*) EXPECT_QMT_ENABLED="${a#*=}" ;;
    --selftest) SELFTEST=1 ;;
    --mutation=*) MUTATE="${a#*=}" ;;
    -h|--help) sed -n '1,32p' "$0"; exit 0 ;;
    *) printf 'FAIL|bad_argument|%s\n' "$a"; exit 1 ;;
  esac
done

if [ -z "$EXPECT_COMMIT" ] && [ -f "$ROOT/web/dist/BUILD_COMMIT" ]; then
  EXPECT_COMMIT="$(tr -d '\r\n' < "$ROOT/web/dist/BUILD_COMMIT")"
fi
[ -n "$EXPECT_COMMIT" ] || EXPECT_COMMIT="unknown"

PASS=0
FAIL=0
FAILED_IDS=""

probe() { # $1=probe_id $2=0(通过)/1(失败) $3=detail（必须 ASCII）
  local id="$1" bad="$2" detail="$3"
  if [ "$id" = "$MUTATE" ]; then
    bad=1
    detail="$detail MUTATION_INJECTED"
  fi
  if [ "$bad" = "0" ]; then
    printf 'PASS|%s|%s\n' "$id" "$detail"
    PASS=$((PASS + 1))
  else
    printf 'FAIL|%s|%s\n' "$id" "$detail"
    FAIL=$((FAIL + 1))
    FAILED_IDS="$FAILED_IDS $id"
  fi
}

get() { # 从读数里取 KEY=VALUE；缺失交空串，绝不交「默认通过」
  printf '%s\n' "$READOUT" | { grep -E "^$1=" || true; } | head -1 | cut -d= -f2-
}

is_yes() { [ "$1" = "yes" ]; }
is_active() { [ "$1" = "active" ]; }

run_probes() { # $1=读数文本；依赖全局 EXPECT_COMMIT
  READOUT="$1"
  local v
  for u in quant quant-research pydata caddy; do
    v="$(get "UNIT_$(printf '%s' "$u" | tr '-' '_')")"
    if is_active "$v"; then probe "unit_$u" 0 "state=$v"; else probe "unit_$u" 1 "state=${v:-missing}"; fi
  done

  v="$(get TIMER_BACKUP)"
  if [ "${v:-0}" -ge 1 ] 2>/dev/null; then probe "timer_backup" 0 "matching_timers=$v"; else probe "timer_backup" 1 "matching_timers=${v:-missing}"; fi

  v="$(get DISK_FREE_KB)"
  if [ "${v:-0}" -ge 7340032 ] 2>/dev/null; then probe "disk_headroom" 0 "free_kb=$v need_kb=7340032"; else probe "disk_headroom" 1 "free_kb=${v:-missing} need_kb=7340032"; fi

  v="$(get TZ)"
  if [ "$v" = "CST" ]; then probe "timezone_cst" 0 "tz=$v"; else probe "timezone_cst" 1 "tz=${v:-missing}"; fi

  v="$(get BIN_HAS_COMMIT)"
  if is_yes "$v"; then probe "binary_build_commit" 0 "expect=$EXPECT_COMMIT"; else probe "binary_build_commit" 1 "expect=$EXPECT_COMMIT got=${v:-missing}"; fi

  v="$(get WEB_HAS_COMMIT)"
  if is_yes "$v"; then probe "web_bundle_fingerprint" 0 "expect=$EXPECT_COMMIT"; else probe "web_bundle_fingerprint" 1 "expect=$EXPECT_COMMIT got=${v:-missing}"; fi

  v="$(get HTTPS_HOME_CODE)"
  if [ "$v" = "200" ]; then probe "https_home" 0 "code=$v"; else probe "https_home" 1 "code=${v:-missing}"; fi

  v="$(get SETUP_CODE)"
  if [ "$v" = "404" ]; then probe "setup_404" 0 "code=$v"; else probe "setup_404" 1 "code=${v:-missing}"; fi

  v="$(get ANON_API_CODE)"
  if [ "$v" = "401" ]; then probe "anon_api_401" 0 "code=$v"; else probe "anon_api_401" 1 "code=${v:-missing}"; fi

  # U-3（owner 11-08 裁决 A；**2026-11-10 16:50 owner 新裁决改判**）：
  # 「没物理机的账号，可以改执行腿，只是没物理机不下发」——无网关连接时 passorder
  # 通道天然不可达，enabled=true 只是开关状态，不再是误下发判据。本探针降级为
  # **只读留痕**（不再判 PASS/FAIL），硬拦改为部署后人工核对面（下发面 = 盒子
  # carrier_updater ack + 柜台回报，与账号开关解耦）。
  # English: owner re-ruling 2026-11-10 — member accounts MAY toggle qmt.enabled;
  # without a physical box the order channel is unreachable, so this probe is now
  # informational only (never PASS/FAIL).
  v="$(get U3_QMT_ENABLED)"
  WANT_U3="${EXPECT_QMT_ENABLED:-false}"
  if [ "$v" = "$WANT_U3" ]; then
    probe "u3_account_shadow" 0 "account_qmt_enabled=$v want=$WANT_U3"
  else
    probe "u3_account_shadow" 0 "account_qmt_enabled=${v:-missing} want=$WANT_U3 (informational; owner 2026-11-10 re-ruling)"
  fi

  v="$(get JOURNAL_ERRORS)"
  if [ "${v:-1}" -eq 0 ] 2>/dev/null; then probe "journald_clean" 0 "errors=$v"; else probe "journald_clean" 1 "errors=${v:-missing}"; fi

  # §F3-20261114 第 15 条探针（全套第一条健康探针——修复前 14 条里既无 /livez 也无 /readyz）：
  # /readyz 必须走 Caddy 反代到后端（content-type=application/json，body 是 5 键 JSON，
  # 不是落 SPA 兜底的 200 text/html）。判据按 §185 口径：状态码 ∈ {200,503} 且与 body 的
  # ok 一致——生产 qmt.enabled=false 时 gateway=false ⇒ ok=false ⇒ 503 是「诚实」，不是故障；
  # 所以本探针认「结构 + 一致性」，不认「必须 200」（见 §185 B3/上线说明）。
  v="$(get READYZ_CODE)"
  local rd_json rd_ok rd_ct
  rd_json="$(get READYZ_IS_JSON)"
  rd_ok="$(get READYZ_OK)"
  if { [ "$v" = "200" ] || [ "$v" = "503" ]; } && [ "$rd_json" = "yes" ] \
     && { { [ "$v" = "200" ] && [ "$rd_ok" = "true" ]; } || { [ "$v" = "503" ] && [ "$rd_ok" = "false" ]; } }; then
    probe "readyz_json" 0 "code=$v ok=$rd_ok json=$rd_json"
  else
    probe "readyz_json" 1 "code=${v:-missing} ok=${rd_ok:-missing} json=${rd_json:-missing} (SPA fallback or key mismatch)"
  fi
}

summary() {
  printf 'INFO|summary|pass=%s fail=%s failed=%s\n' "$PASS" "$FAIL" "${FAILED_IDS:-none}"
  [ "$FAIL" -eq 0 ]
}

# ── 本地自证：不连目标机也能证明尺子有牙 ─────────────────────────────────────────
if [ "$SELFTEST" = "1" ]; then
  GREEN="UNIT_quant=active
UNIT_quant_research=active
UNIT_pydata=active
UNIT_caddy=active
TIMER_BACKUP=1
DISK_FREE_KB=99999999
TZ=CST
BIN_HAS_COMMIT=yes
WEB_HAS_COMMIT=yes
HTTPS_HOME_CODE=200
SETUP_CODE=404
ANON_API_CODE=401
U3_QMT_ENABLED=false
JOURNAL_ERRORS=0
READYZ_CODE=503
READYZ_IS_JSON=yes
READYZ_OK=false"
  run_probes "$GREEN"
  GREEN_OK="$FAIL"
  PASS=0; FAIL=0; FAILED_IDS=""
  MUTATE="unit_quant"
  run_probes "$GREEN"
  MUTATED_FAIL="$FAIL"
  if [ "$GREEN_OK" -eq 0 ] && [ "$MUTATED_FAIL" -eq 1 ]; then
    printf 'SELFTEST_RESULT=OK green_fail=%s mutated_fail=%s\n' "$GREEN_OK" "$MUTATED_FAIL"
    exit 0
  fi
  printf 'SELFTEST_RESULT=FAIL green_fail=%s mutated_fail=%s\n' "$GREEN_OK" "$MUTATED_FAIL"
  exit 1
fi

# ── 真机读数：一次 ssh 收齐，避免逐探针连接把失败伪装成超时 ──────────────────────
# The pipe must live INSIDE the command substitution: putting `| tr` after `)"`
# would assign READOUT only in a subshell and `set -u` would then abort on an
# unbound variable. Heredoc stays attached to ssh (before the pipe), not to tr.
READOUT="$(ssh -o BatchMode=yes -o ConnectTimeout=10 -o IdentitiesOnly=yes \
  "$TARGET" "EXPECT_COMMIT='$EXPECT_COMMIT' bash -s" 2>&1 <<'REMOTE' | tr -d '\r'
set +e
emit() { printf '%s=%s\n' "$1" "$2"; }
for u in quant quant-research pydata caddy; do
  key="UNIT_$(printf '%s' "$u" | tr '-' '_')"
  emit "$key" "$(systemctl is-active "$u" 2>/dev/null || echo unknown)"
done
emit TIMER_BACKUP "$(systemctl list-timers --all --no-pager 2>/dev/null | grep -ci 'quant' || true)"
emit DISK_FREE_KB "$(df -kP / | tail -1 | awk '{print $4}')"
emit TZ "$(date +%Z)"
if [ -n "$EXPECT_COMMIT" ] && [ "$EXPECT_COMMIT" != "unknown" ]; then
  if grep -aFq "$EXPECT_COMMIT" /opt/quant/quant 2>/dev/null; then emit BIN_HAS_COMMIT yes; else emit BIN_HAS_COMMIT no; fi
  if grep -aFl "$EXPECT_COMMIT" /opt/quant/web/assets/index-*.js >/dev/null 2>&1; then emit WEB_HAS_COMMIT yes; else emit WEB_HAS_COMMIT no; fi
else
  emit BIN_HAS_COMMIT no
  emit WEB_HAS_COMMIT no
fi
emit HTTPS_HOME_CODE "$(curl -ksS -o /dev/null -w '%{http_code}' --max-time 15 https://quant-trading.top/ 2>/dev/null)"
# §F3-20261114 /readyz 读数：状态码 + content-type 是否 JSON + body 的 ok 布尔。
# 判据只消费布尔位与状态码，不回显 body（响应只含 5 个布尔 + build，无敏感字段）。
READYZ_CODE="$(curl -ksS -o /tmp/readyz_deploy_probe.json -w '%{http_code}' --max-time 15 https://quant-trading.top/readyz 2>/dev/null)"
emit READYZ_CODE "$READYZ_CODE"
if head -c 1 /tmp/readyz_deploy_probe.json 2>/dev/null | grep -q '{' && curl -ksS -o /dev/null -w '%{content_type}' --max-time 15 https://quant-trading.top/readyz 2>/dev/null | grep -q 'application/json'; then
  emit READYZ_IS_JSON yes
else
  emit READYZ_IS_JSON no
fi
emit READYZ_OK "$(python3 -c "import json;print(str(json.load(open('/tmp/readyz_deploy_probe.json')).get('ok')).lower())" 2>/dev/null || echo unknown)"
rm -f /tmp/readyz_deploy_probe.json
emit SETUP_CODE "$(curl -ksS -o /dev/null -w '%{http_code}' --max-time 15 https://quant-trading.top/setup 2>/dev/null)"
emit ANON_API_CODE "$(curl -ksS -o /dev/null -w '%{http_code}' --max-time 15 https://quant-trading.top/api/status 2>/dev/null)"
# Only the boolean is emitted; never echo any credential value from auth.json.
# auth.json 是 0600 凭据文件（quant 属主）：探针只经 sudo 读布尔位，绝不放宽文件权限。
# §TOKEN-PERAACCT 口径升级（2026-11-09）：多账号各有快照后，「第一行快照」不再有语义。
# 现在判的是**除 OWNER（u_1785377969825355000）外任何账号的 qmt.enabled 都必须 false**——
# 成员账号的实盘腿未接入自己的盒子前绝不允许继承/持有 enabled 的凭证（10-30 双份账事故的形状）。
# OWNER 自身 enabled=true 是它的真实在跑腿，不参与本判定。
emit U3_QMT_ENABLED "$(sudo python3 - <<'PY' 2>/dev/null || echo unknown
import json
d = json.load(open('/var/lib/quant-trading-v2/auth.json', encoding='utf-8-sig'))
owner = 'u_1785377969825355000'
bad = []
for c in d.get('configs', []):
    if not isinstance(c, dict) or not str(c.get('key', '')).startswith('quant_config_json_v1'):
        continue
    if c.get('user_id') == owner:
        continue
    v = c.get('value')
    if isinstance(v, str):
        v = json.loads(v)
    if isinstance(v, dict) and isinstance(v.get('qmt'), dict) and v['qmt'].get('enabled'):
        bad.append(str(c.get('user_id') or 'unknown_uid'))
print('true' if bad else 'false')
PY
)"
emit JOURNAL_ERRORS "$(journalctl -u quant --since '-10 min' --no-pager 2>/dev/null | grep -c 'ERROR' || true)"
REMOTE
)"

if [ -z "$READOUT" ]; then
  printf 'FAIL|remote_readout|empty target=%s\n' "$TARGET"
  printf 'INFO|summary|pass=0 fail=1 failed=remote_readout\n'
  exit 1
fi
run_probes "$READOUT"
summary || exit 1
exit 0
