#!/usr/bin/env bash
# amend_fill_guangzhou.sh — §FILL-AMEND-CLI（2026-09-25）：把「历史错账的人工改判」从只能开浏览器点
# 变成仓库里可复核的正规通道（**缺省只预览、动手须显式 --apply --yes**，与 §OPS-ALIGN 同口径）。
#
# 为什么要这条脚本：§FILL-AMEND 的落地形态是五个 admin HTTP 端点（提交/批准/撤销/台账/守恒自检），
# 仓库里既没有 CLI 也没有脚本入口——于是每一笔历史错账都只能"owner 开浏览器点四下"。当流水列表只回
# 最近 100 笔、而争议那笔在三个交易日之前时，页面上**根本翻不到那一行**，通道等于对旧账不可用。
# 本脚本不新增任何写能力：它只把同一套端点按同一套顺序调用一遍，写动作仍然只有 apply 那一个。
#
# 五条硬口径（每条都对应本仓库的一条实录教训）：
#   ① **令牌只从本机文件读，且只经 SSH 的标准输入进远端进程**。绝不拼进任何命令行（本机 ps 与远端
#      进程表都能看见命令行），绝不落盘到服务器上，绝不打进日志——脚本收尾会把整轮输出与令牌值逐字节
#      比对，命中即判失败退出（防"降级却把凭据写进日志"）。
#   ② **缺省只预览**：只读台账与守恒自检，一次 POST 都不发。要动账必须同时给 `--apply` 与 `--yes`
#      （两步显式，误敲一个 flag 不会改到钱账）。
#   ③ **远端脚本体一律纯 ASCII**（含中文的字段用 Base64 承载、方向枚举映射成 BUY/SELL 再回传）：
#      bash→ssh→cmd→powershell 四层转义 + GBK 码页回传会把中文判据吃成乱码（§GBK 系列、§4.1b.1② 实录）。
#   ④ **解析回传先去 \r**：Windows 每行是 CRLF，命令替换只剥 \n，行尾最后一个字段会带着 \r 让等值
#      比较恒假红（09-24 首次真跑锤出来的坑，见 §BRIDGE-PLACE）。
#   ⑤ **失败必须显式**：任何一步取不到数/状态不匹配都以非 0 退出并说明缺哪一步，绝不把"跑完了"当成功。
#
# 用法：
#   预览（零写入，连 POST 都不发）：
#     GZ_IP=81.71.69.17 ./scripts/amend_fill_guangzhou.sh --fill-id 14 --new-side 卖出 \
#       --reason "09-22 10:08 该笔实为柜台手动卖单（持仓与网关对账佐证见 RUNBOOK）"
#   真动手（提交 + 批准，一步到位但只在显式双 flag 下）：
#     ... 同上再加 --apply --yes
#   撤销一条勘误（数字立刻回到柜台原始方向）：
#     ... --revoke <勘误ID> --yes
#   本机/夹具直连（不经 SSH，给自动化测试与本地 UAT 栈用）：
#     AMEND_LOCAL_BASE=http://127.0.0.1:18081 ADMIN_TOKEN_FILE=./tok ./scripts/amend_fill_guangzhou.sh ...
# 可选环境变量：
#   GZ_USER          默认 Administrator（与 deploy_guangzhou.sh / survey_live_rules.sh 一致）
#   ENGINE_PORT      默认 8081（与 verify_deploy_guangzhou.sh 的缺省端口同源口径）
#   ADMIN_TOKEN_FILE 默认 ~/.quant-trading-v2/admin_session_token（须 mode 600、不得在仓库工作树内）
#   CONSERVE_DAY     守恒自检的复核截止日 YYYY-MM-DD（默认留空=服务端按当日）
#   AMEND_EVIDENCE_DIR 取证留档目录（默认 /tmp；自动化测试指向自己的临时目录，跑完即删）
# 退出码：0=该模式该做的都做完了；非 0=令牌/通道/取数/状态校验任一失败（不做"看起来成功"的回退）。
set -uo pipefail

APP_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GZ_IP="${GZ_IP:-}"
GZ_USER="${GZ_USER:-Administrator}"
ENGINE_PORT="${ENGINE_PORT:-8081}"
LOCAL_BASE="${AMEND_LOCAL_BASE:-}"
TOKEN_FILE="${ADMIN_TOKEN_FILE:-$HOME/.quant-trading-v2/admin_session_token}"
CONSERVE_DAY=""
FILL_ID=""
NEW_SIDE=""
REASON=""
MODE="preview"        # preview | apply | revoke
AMEND_ID=""
YES=0

usage() {
  sed -n '2,3p;/^# 用法：/,/^# 退出码/p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --fill-id) FILL_ID="${2:-}"; shift 2 ;;
    --new-side) NEW_SIDE="${2:-}"; shift 2 ;;
    --reason) REASON="${2:-}"; shift 2 ;;
    --revoke) MODE="revoke"; AMEND_ID="${2:-}"; shift 2 ;;
    --apply) MODE="apply"; shift ;;
    --yes) YES=1; shift ;;
    --day) CONSERVE_DAY="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "X 未知参数：$1" >&2; usage ;;
  esac
done

# ── 入参校验（缺哪项就点名哪项，绝不默认一个方向替人做决定）────────────────────────
if [ "$MODE" = "revoke" ]; then
  case "$AMEND_ID" in ''|*[!0-9]*) echo "X --revoke 需要勘误行 ID（正整数，从台账预览里取）" >&2; exit 2 ;; esac
else
  case "$FILL_ID" in ''|*[!0-9]*) echo "X 需要 --fill-id（被勘误的成交行 ID，正整数）" >&2; exit 2 ;; esac
  case "$NEW_SIDE" in
    买入|卖出) ;;
    *) echo "X --new-side 只接受 买入 或 卖出（与后端 ErrFillAmendmentSide 同口径，不给自由文本）" >&2; exit 2 ;;
  esac
  [ "$MODE" = "apply" ] || { [ -n "$REASON" ] || { echo "X 预览也要求 --reason：改判无留痕即无据可查" >&2; exit 2; }; }
  if [ "$MODE" = "apply" ] && [ -z "$REASON" ]; then
    echo "X 提交勘误的理由必填（后端同样拒空，这里提前失败免得白连一次生产）" >&2; exit 2
  fi
fi
if [ "$MODE" != "preview" ] && [ "$YES" != "1" ]; then
  echo "X $MODE 模式必须同时给 --yes（这是唯一会让账目数字变动的动作，两步显式是刻意的）" >&2
  exit 2
fi
if [ -z "$LOCAL_BASE" ]; then
  [ -n "$GZ_IP" ] || { echo "X 现网模式必须显式设置 GZ_IP（本脚本不猜主机地址）" >&2; exit 2; }
fi

# ── 令牌门（读得到才继续；读不到就如实说"只能预览到计划"，不假装跑过）──────────────
TOKEN=""
TOKEN_STATE="missing"
if [ -f "$TOKEN_FILE" ]; then
  REPO_ABS="$(cd "$APP_DIR" && pwd -P)"
  TDIR_ABS="$(cd "$(dirname "$TOKEN_FILE")" 2>/dev/null && pwd -P || true)"
  # 令牌文件绝不允许落在仓库工作树里（*.json 在数据目录外不受 .gitignore 保护，掉进工作树就有被
  # commit 的口子；口令类同）。两侧都先经 pwd -P 归一再做前缀判定，防 `../`/软链绕过。
  case "${TDIR_ABS:-?}/" in
    "${REPO_ABS}/"*|"${REPO_ABS}")
      echo "X 令牌文件在仓库工作树内（${TOKEN_FILE}）——凭据永远不该出现在 commit 候选里，换一个位置。" >&2
      exit 2 ;;
  esac
  # macOS 的 stat 是 -f，Linux 是 -c；两条都试，取到就用来判"组/其他可读"，取不到（罕见文件系统）
  # 不放宽——下面只看末两位，非 00 即拒。
  MODE_BITS="$(stat -f '%Lp' "$TOKEN_FILE" 2>/dev/null || stat -c '%a' "$TOKEN_FILE" 2>/dev/null || echo '')"
  if [ -n "$MODE_BITS" ] && [ "${MODE_BITS: -2}" != "00" ]; then
    echo "X 令牌文件权限 ${MODE_BITS}（组或其他可读）——先 chmod 600 ${TOKEN_FILE}" >&2
    exit 2
  fi
  TOKEN="$(head -c 4096 "$TOKEN_FILE" 2>/dev/null | tr -d '\r\n')"
  if [ -z "$TOKEN" ]; then
    echo "X 令牌文件为空：${TOKEN_FILE}" >&2
    exit 2
  fi
  TOKEN_STATE="loaded"
else
  echo "   （未找到管理员会话令牌 ${TOKEN_FILE}：本轮只能打印计划，发不出任何请求）" >&2
fi

b64() { printf '%s' "$1" | base64 | tr -d '\n'; }

# ── 远端脚本体（纯 ASCII + 令牌从 stdin 读）───────────────────────────────────────
# 只负责"取事实并原样吐回"：方向枚举映射成 BUY/SELL，中文一律 Base64，判定全在本地单点做。

# conserve_path 守恒自检的请求路径：未指定 --day 时**不带查询参数**，让服务端按自己的当日
# （cntime.Now()）取截止日。脚本里绝不写死一个日期兜底——那会把"三天后再跑还是按 09-25 核"
# 这种口径错静默吞掉。
conserve_path() {
  if [ -n "$CONSERVE_DAY" ]; then printf '/api/qmt/fills/conservation?day=%s' "$CONSERVE_DAY"; else printf '/api/qmt/fills/conservation'; fi
}

ps_body() {
  local base="http://127.0.0.1:${ENGINE_PORT}"
  local cp
  cp="$(conserve_path)"
  cat <<PSEOF
\$ErrorActionPreference = 'Stop'
\$tok = [Console]::In.ReadToEnd().Trim()
if (-not \$tok) { Write-Output 'AMEND_ERR=no_token_on_stdin'; exit 1 }
\$H = @{ Authorization = ('Bearer ' + \$tok) }
# wantId: sentinel 0 = do not scan the fill rows (revoke mode has no --fill-id;
# [int64]'' would throw in PowerShell).
\$wantId = [int64]${FILL_ID:-0}
function SideTok(\$s) {
  \$sell = [string][char]0x5356 + [char]0x51FA
  \$buy  = [string][char]0x4E70 + [char]0x5165
  if (\$s -eq \$sell) { return 'SELL' } elseif (\$s -eq \$buy) { return 'BUY' } elseif ([string]::IsNullOrEmpty(\$s)) { return 'EMPTY' }
  return 'OTHER'
}
function GetJson(\$path) { return Invoke-RestMethod -Method Get -Uri ('${base}' + \$path) -Headers \$H }
try {
  \$t = GetJson '/api/qmt/trades'
  \$shown = @(\$t.fills).Count
  foreach (\$f in \$t.fills) {
    if (\$wantId -ne 0 -and [int64]\$f.id -eq \$wantId) {
      Write-Output ('FILL id=' + \$f.id + ' code=' + \$f.code + ' side=' + (SideTok \$f.side) +
        ' orig_side=' + (SideTok \$f.orig_side) + ' qty=' + \$f.qty + ' price=' + \$f.price +
        ' amount=' + \$f.amount + ' traded_at=' + \$f.traded_at + ' trade_id_b64=' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string]\$f.trade_id)) +
        ' amend_key_b64=' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string]\$f.amend_key)))
    }
  }
  Write-Output ('TRADES_SHOWN=' + \$shown)
  \$l = GetJson '/api/qmt/fill-amendments'
  Write-Output ('LEDGER_COUNT=' + @(\$l.amendments).Count)
  foreach (\$a in \$l.amendments) {
    Write-Output ('AMEND id=' + \$a.id + ' fill_id=' + \$a.fill_id + ' code=' + \$a.code + ' qty=' + \$a.qty +
      ' orig=' + (SideTok \$a.orig_side) + ' new=' + (SideTok \$a.new_side) + ' status=' + \$a.status +
      ' operator_b64=' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string]\$a.operator)) +
      ' created=' + \$a.created_at + ' applied=' + \$a.applied_at)
  }
  \$c = GetJson '${cp}'
  \$r = \$c.report
  Write-Output ('CONSERVE day=' + \$r.day + ' ok=' + \$r.ok + ' applied_amend=' + \$r.applied_amendments +
    ' pos_lines=' + @(\$r.position_lines).Count + ' cash_checked=' + \$r.cash.checked +
    ' cash_diff=' + \$r.cash.diff + ' book_cash=' + \$r.cash.book_cash + ' expected_cash=' + \$r.cash.expected_cash)
  foreach (\$p in \$r.position_lines) {
    Write-Output ('POSLINE code=' + \$p.code + ' replayed=' + \$p.replayed_qty + ' book=' + \$p.book_qty + ' diff=' + \$p.diff)
  }
  exit 0
} catch {
  Write-Output ('AMEND_ERR=read_' + \$_.Exception.Message.Substring(0, [Math]::Min(160, \$_.Exception.Message.Length)))
  exit 1
}
PSEOF
}

# 写侧（create/apply/revoke）单独一次调用：与读侧分开，保证预览模式在代码里没有任何 POST 路径。
ps_write_body() {
  local base="http://127.0.0.1:${ENGINE_PORT}"
  local rb64="$(b64 "$REASON")"
  local side_tok="SELL"
  [ "$NEW_SIDE" = "买入" ] && side_tok="BUY"
  cat <<PSEOF
\$ErrorActionPreference = 'Stop'
\$tok = [Console]::In.ReadToEnd().Trim()
if (-not \$tok) { Write-Output 'AMEND_ERR=no_token_on_stdin'; exit 1 }
\$H = @{ Authorization = ('Bearer ' + \$tok) }
\$B = 'application/json; charset=utf-8'
\$sell = [string][char]0x5356 + [char]0x51FA
\$buy  = [string][char]0x4E70 + [char]0x5165
# Side enum travels as code points (ASCII-only remote body); pick by --new-side.
\$newSide = if ('${side_tok}' -eq 'SELL') { \$sell } else { \$buy }
\$reason = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('${rb64}'))
function J(\$o) { return (ConvertTo-Json -InputObject \$o -Compress) }
try {
  if ('${MODE}' -eq 'revoke') {
    \$x = Invoke-RestMethod -Method Post -Uri ('${base}/api/qmt/fill-amendments/${AMEND_ID}/revoke') -Headers \$H -ContentType \$B -Body (J @{})
    Write-Output ('REVOKED id=' + \$x.amendment.id + ' status=' + \$x.amendment.status)
    exit 0
  }
  \$c = Invoke-RestMethod -Method Post -Uri ('${base}/api/qmt/fill-amendments') -Headers \$H -ContentType \$B -Body (J @{ fill_id = [int64]${FILL_ID}; new_side = \$newSide; reason = \$reason })
  \$id = \$c.amendment.id
  Write-Output ('CREATED id=' + \$id + ' status=' + \$c.amendment.status + ' fill_id=' + \$c.amendment.fill_id + ' code=' + \$c.amendment.code + ' qty=' + \$c.amendment.qty)
  \$a = Invoke-RestMethod -Method Post -Uri ('${base}/api/qmt/fill-amendments/' + \$id + '/apply') -Headers \$H -ContentType \$B -Body (J @{})
  Write-Output ('APPLIED id=' + \$a.amendment.id + ' status=' + \$a.amendment.status + ' applied_at=' + \$a.amendment.applied_at)
  exit 0
} catch {
  Write-Output ('AMEND_ERR=write_' + \$_.Exception.Message.Substring(0, [Math]::Min(160, \$_.Exception.Message.Length)))
  exit 1
}
PSEOF
}

# ascii_only <文本> → 0=纯 ASCII（制表/换行允许），非 0=掺了非 ASCII。
# 为什么不用 grep -P：macOS 自带 BSD grep 编译时没带 PCRE，`-P` 直接报 invalid option，
# 于是这道自检在开发机上会"永远走不到拒绝分支"（假绿形态，见 §macOS-unicode 教训族）。
ascii_only() {
  printf '%s' "$1" | python3 -c 'import sys
d = sys.stdin.buffer.read()
bad = [b for b in d if b not in (9, 10) and not (32 <= b < 127)]
sys.exit(0 if not bad else 1)'
}

# 直连腿的两段 python 程序体（读 / 写）以变量承载、用 `python3 -c` 调用。
# 为什么不能用 `python3 - <<'PY'`：heredoc 会**接管 stdin**，于是"令牌走 stdin"这条腿永远读不到
# 令牌（本地实测就是这条报 AMEND_ERR=no_token_on_stdin）。令牌仍不进命令行、不进环境变量。
PY_READ="$(cat <<'PY'
import base64, json, sys, urllib.request
base, want_id, cpath = sys.argv[1], str(sys.argv[2]), sys.argv[3]
tok = sys.stdin.read().strip()
def get(p):
    req = urllib.request.Request(base + p, headers={'Authorization': 'Bearer ' + tok})
    return json.load(urllib.request.urlopen(req))
def side(s):
    return {'卖出': 'SELL', '买入': 'BUY', '': 'EMPTY', None: 'EMPTY'}.get(s, 'OTHER')
def b64(s):
    return base64.b64encode((s or '').encode()).decode()
try:
    if not tok:
        print('AMEND_ERR=no_token_on_stdin'); sys.exit(1)
    t = get('/api/qmt/trades')
    fills = t.get('fills') or []
    for f in fills:
        if want_id != '0' and str(f.get('id')) == want_id:
            print('FILL id=%s code=%s side=%s orig_side=%s qty=%s price=%s amount=%s traded_at=%s trade_id_b64=%s amend_key_b64=%s' % (
                f.get('id'), f.get('code'), side(f.get('side')), side(f.get('orig_side')), f.get('qty'), f.get('price'),
                f.get('amount'), f.get('traded_at'), b64(f.get('trade_id')), b64(f.get('amend_key'))))
    print('TRADES_SHOWN=%d' % len(fills))
    rows = (get('/api/qmt/fill-amendments') or {}).get('amendments') or []
    print('LEDGER_COUNT=%d' % len(rows))
    for a in rows:
        print('AMEND id=%s fill_id=%s code=%s qty=%s orig=%s new=%s status=%s operator_b64=%s created=%s applied=%s' % (
            a.get('id'), a.get('fill_id'), a.get('code'), a.get('qty'), side(a.get('orig_side')), side(a.get('new_side')),
            a.get('status'), b64(a.get('operator')), a.get('created_at'), a.get('applied_at')))
    r = (get(cpath) or {}).get('report') or {}
    cash = r.get('cash') or {}
    lines = r.get('position_lines') or []
    print('CONSERVE day=%s ok=%s applied_amend=%s pos_lines=%d cash_checked=%s cash_diff=%s book_cash=%s expected_cash=%s' % (
        r.get('day'), r.get('ok'), r.get('applied_amendments'), len(lines),
        cash.get('checked'), cash.get('diff'), cash.get('book_cash'), cash.get('expected_cash')))
    for p in lines:
        print('POSLINE code=%s replayed=%s book=%s diff=%s' % (p.get('code'), p.get('replayed_qty'), p.get('book_qty'), p.get('diff')))
except Exception as e:
    print('AMEND_ERR=read_%s' % str(e)[:160]); sys.exit(1)
PY
)"

PY_WRITE="$(cat <<'PY'
import base64, json, sys, urllib.error, urllib.request
base, mode, amend_id, fill_id, new_side, rb64 = sys.argv[1:7]
tok = sys.stdin.read().strip()
def post(p, body):
    data = json.dumps(body, ensure_ascii=False).encode()
    req = urllib.request.Request(base + p, data=data, method='POST',
                                 headers={'Authorization': 'Bearer ' + tok,
                                          'Content-Type': 'application/json; charset=utf-8'})
    return json.load(urllib.request.urlopen(req))
try:
    if not tok:
        print('AMEND_ERR=no_token_on_stdin'); sys.exit(1)
    if mode == 'revoke':
        x = post('/api/qmt/fill-amendments/%s/revoke' % amend_id, {})
        a = x.get('amendment') or {}
        print('REVOKED id=%s status=%s' % (a.get('id'), a.get('status')))
        sys.exit(0)
    reason = base64.b64decode(rb64.encode()).decode() if rb64 else ''
    c = post('/api/qmt/fill-amendments', {'fill_id': int(fill_id), 'new_side': new_side, 'reason': reason})
    ca = c.get('amendment') or {}
    print('CREATED id=%s status=%s fill_id=%s code=%s qty=%s' % (
        ca.get('id'), ca.get('status'), ca.get('fill_id'), ca.get('code'), ca.get('qty')))
    a = post('/api/qmt/fill-amendments/%s/apply' % ca.get('id'), {})
    aa = a.get('amendment') or {}
    print('APPLIED id=%s status=%s applied_at=%s' % (aa.get('id'), aa.get('status'), aa.get('applied_at')))
except urllib.error.HTTPError as e:
    print('AMEND_ERR=write_http_%s_%s' % (e.code, e.read().decode('utf-8', 'replace')[:140])); sys.exit(1)
except Exception as e:
    print('AMEND_ERR=write_%s' % str(e)[:160]); sys.exit(1)
PY
)"

# run_remote <read|write> <ps-text> → 标准输出为**已去 \r** 的回传；退出码透传（远端 exit 1 会带回来）。
# 为什么要 kind 参数：直连腿（测试/本地 UAT 栈）用的是同一份 python 入口，若不区分读写，
# 把 --apply 传进本地模式会"跑完读腿、一个 POST 都没发"却回 0——正是本仓反复锤的假绿形态。
run_remote() {
  local kind="$1" ps="$2"
  if ! ascii_only "$ps"; then
    echo "X 远端脚本体掺了非 ASCII 字符（会被 GBK 码页吃成乱码判据），中止。" >&2
    return 3
  fi
  if [ -n "$LOCAL_BASE" ]; then
    if [ "$kind" = "write" ]; then
      printf '%s' "$TOKEN" | python3 -c "$PY_WRITE" "$LOCAL_BASE" "${MODE}" "${AMEND_ID:-0}" \
        "${FILL_ID:-0}" "${NEW_SIDE}" "$(b64 "$REASON")"
      return $?
    fi
    # 直连只读腿：事实行格式与 PS 腿逐字一致（两套腿输出同构，测试才测得到生产路径的判据）。
    printf '%s' "$TOKEN" | python3 -c "$PY_READ" "$LOCAL_BASE" "${FILL_ID:-0}" "$(conserve_path)"
    return $?
  fi
  local b64ps
  b64ps="$(printf '%s' "$ps" | iconv -f UTF-8 -t UTF-16LE | base64 | tr -d '\n')"
  printf '%s' "$TOKEN" | $SSH "powershell -NoProfile -ExecutionPolicy Bypass -EncodedCommand $b64ps" 2>&1 | tr -d '\r'
}

if [ -z "$LOCAL_BASE" ]; then
  SSH="ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa ${GZ_USER}@${GZ_IP}"
fi

OUTDIR="${AMEND_EVIDENCE_DIR:-/tmp}"
OUTFILE="${OUTDIR}/amend_fill_$(date +%H%M%S).log"

if [ "$TOKEN_STATE" = "missing" ]; then
  echo "== 计划（无令牌，未发出任何请求）=="
  echo "   mode        = ${MODE}"
  echo "   fill_id     = ${FILL_ID:-（revoke 模式不需要）}"
  echo "   new_side    = ${NEW_SIDE:-（revoke 模式不需要）}"
  echo "   reason      = ${#REASON} 字节（内容不外打，避免自由文本进日志）"
  echo "   端点        = $( [ -n "$LOCAL_BASE" ] && printf '%s（本机直连）' "$LOCAL_BASE" || printf '127.0.0.1:%s（经 %s@%s 的 SSH 通道，走服务器回环）' "$ENGINE_PORT" "$GZ_USER" "$GZ_IP" )"
  echo "   动作        = $( [ "$MODE" = "preview" ] && echo '只读：trades + 台账 + 守恒自检' || echo "写：POST $( [ "$MODE" = "apply" ] && echo 'create→apply' || echo "revoke ${AMEND_ID}" )" )"
  if [ "$MODE" = "preview" ]; then
    echo "AMEND_PLAN_ONLY"
    exit 0
  fi
  # 写模式缺令牌绝不能回 0：那等于"你要的改判没做，但退出码说做完了"（本仓反复锤的假绿形态）。
  echo "X ${MODE} 需要管理员会话令牌：请把令牌放入 ${TOKEN_FILE}（chmod 600，不要贴进对话或命令行）后重跑。" >&2
  exit 1
fi

echo "==> [1/3] BatchMode 预探测（私钥不可读就立刻失败，绝不挂起）"
if [ -z "$LOCAL_BASE" ]; then
  if ! $SSH "echo ok" >/dev/null 2>&1; then
    echo "X 无法以 BatchMode 登录 ${GZ_USER}@${GZ_IP}：公钥未授权或私钥不可读。本脚本不使用密码认证，直接失败。" >&2
    exit 1
  fi
fi
echo "ok - 通道可用"

echo "==> [2/3] 读现状（成交行 / 勘误台账 / 账本守恒自检）"
READ_OUT="$(run_remote read "$(ps_body)")"
READ_RC=$?
printf '%s\n' "$READ_OUT" > "$OUTFILE"
case "$READ_OUT" in *AMEND_ERR=*) echo "X 读现状失败：$(printf '%s\n' "$READ_OUT" | grep 'AMEND_ERR=' | tail -1)" >&2; exit 1 ;; esac
[ "$READ_RC" = "0" ] || { echo "X 读现状腿非 0 退出（rc=${READ_RC}），不把「跑完了」当成功。" >&2; exit 1; }
printf '%s\n' "$READ_OUT" | grep -E '^(FILL|TRADES_SHOWN|LEDGER_COUNT|AMEND|CONSERVE|POSLINE)' | sed 's/^/    /'

FILL_LINE="$(printf '%s\n' "$READ_OUT" | grep "^FILL id=${FILL_ID} " | tail -1 || true)"
if [ "$MODE" != "revoke" ]; then
  if [ -z "$FILL_LINE" ]; then
    # /api/qmt/trades 只回**最近 100 笔**（qmt.go 流水窗口），所以"找不到"有两种成因：ID 写错 /
    # 这笔在窗口之外。两者处置完全不同（前者改参数，后者要 owner 先从页面/交割单确认行号），
    # 故这里把回传条数一起打出来，不把它压成一句"ID 错"。
    echo "X 成交簿回传里找不到 id=${FILL_ID}（本次窗口内共 $(printf '%s\n' "$READ_OUT" | grep '^TRADES_SHOWN=' | tail -1 | cut -d= -f2) 笔）。" >&2
    echo "  可能原因：① ID 写错或该笔不属于本账号；② 该笔已不在「最近 100 笔」窗口内——此时必须先只读确认行号，不要凭猜提交。" >&2
    exit 1
  fi
  ORIG="$(printf '%s' "$FILL_LINE" | tr ' ' '\n' | grep '^orig_side=' | cut -d= -f2 || true)"
  WANT="$( [ "$NEW_SIDE" = "卖出" ] && echo SELL || echo BUY )"
  [ "$ORIG" != "$WANT" ] || { echo "X 原始方向已经是 ${WANT}：同向勘误是空操作，后端也会拒，这里先拦住。" >&2; exit 1; }
  # 同笔已有活跃勘误（pending/applied）→ 后端回 409。这里提前拦，免得白连一次生产再吃冲突。
  # 取字段一律"按键名取值"（tr 拆词 + grep 前缀），绝不按下标切片：AMEND 行的 created/applied
  # 里带空格（"2026-09-25 08:00:00"），按位置取会错位，把"已有活跃勘误"读成"没有"。
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    st="$(printf '%s' "$line" | tr ' ' '\n' | grep '^status=' | head -1 | cut -d= -f2 || true)"
    fid="$(printf '%s' "$line" | tr ' ' '\n' | grep '^fill_id=' | head -1 | cut -d= -f2 || true)"
    if [ "$fid" = "$FILL_ID" ] && [ "$st" != "revoked" ]; then
      echo "X 该成交已有活跃勘误（${line}）：先撤销原条目，别叠加第二条改判（视图会把一笔成交扇成两行）。" >&2
      exit 1
    fi
  done <<< "$(printf '%s\n' "$READ_OUT" | grep '^AMEND ' || true)"
fi

if [ "$MODE" = "preview" ]; then
  echo "==> [3/3] 预览模式结束：一个字节都没写（未发出任何 POST）。动手请加 --apply --yes。"
  echo "AMEND_PREVIEW_ONLY evidence=${OUTFILE} conservation_line=$(printf '%s\n' "$READ_OUT" | grep '^CONSERVE' | head -1 | cut -c1-80)"
  exit 0
fi

echo "==> [3/3] 写动作（${MODE}）"
WRITE_OUT="$(run_remote write "$(ps_write_body)")"
WRITE_RC=$?
printf '%s\n' "$WRITE_OUT" >> "$OUTFILE"
printf '%s\n' "$WRITE_OUT" | grep -E '^(CREATED|APPLIED|REVOKED|AMEND_ERR)' | sed 's/^/    /'
case "$WRITE_OUT" in *AMEND_ERR=*) echo "X 写动作失败：$(printf '%s\n' "$WRITE_OUT" | grep 'AMEND_ERR=' | tail -1)（原始输出留档 ${OUTFILE}）" >&2; exit 1 ;; esac
[ "$WRITE_RC" = "0" ] || { echo "X 写动作腿非 0（rc=${WRITE_RC}），详见 ${OUTFILE}" >&2; exit 1; }
if [ "$MODE" = "apply" ]; then
  printf '%s\n' "$WRITE_OUT" | grep -q '^APPLIED .*status=applied' || {
    echo "X 没拿到 status=applied 的批准回执——账可能没动，绝不能按「提交成功了」收尾。" >&2; exit 1; }
fi

echo "==> 复核：重读台账与守恒自检（改判后）"
AFTER="$(run_remote read "$(ps_body)")"
printf '%s\n' "$AFTER" >> "$OUTFILE"
printf '%s\n' "$AFTER" | grep -E '^(TRADES_SHOWN|LEDGER_COUNT|CONSERVE|POSLINE)' | sed 's/^/    /'
if [ "$MODE" = "apply" ]; then
  printf '%s\n' "$AFTER" | grep '^AMEND ' | grep -q "fill_id=${FILL_ID} .*status=applied" || {
    echo "X 改判后端台账里仍看不到 applied 行——判失败，不去猜它生效了。" >&2; exit 1; }
fi

# 凭据不出门：整轮留档与令牌值逐字节比对，命中即判失败（宁可刚写完就报失败）。
# 留档**不删**：动过资金账的取证必须留在磁盘上，供事后复核；里面只有业务字段。
if [ -s "$OUTFILE" ] && LC_ALL=C grep -aF -- "$TOKEN" "$OUTFILE" >/dev/null 2>&1; then
  echo "X 留档里出现了令牌值——立即人工检查并删除 ${OUTFILE}；本轮结论不作数。" >&2
  exit 1
fi
echo "AMEND_DONE mode=${MODE} evidence=${OUTFILE} conservation_after=$(printf '%s\n' "$AFTER" | grep '^CONSERVE' | head -1 | cut -c1-80)"
