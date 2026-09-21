#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# §FIX-5 uat_bootstrap.sh — 全栈 UAT 一键自举（2026-09-20）
#
# 背景：这套"mock 柜台 + quant 引擎 + vite 前端 + seed"的启动流程此前**只存在于**
#   .github/workflows/nightly-e2e.yml 的内联步骤里——本机想跑 UAT 只能照抄 YAML、
#   极易漏步（漏 seed 情绪/持仓 → 用例 skip 或假红），且一处改动两处不同步。
#   本脚本把该流程**提取为可复跑的本地脚本**，CI 与本地共用同一套口径。
#
# 用法：
#   ./scripts/uat_bootstrap.sh up      # 起三件套 + seed，留在前台提示后续命令
#   ./scripts/uat_bootstrap.sh run     # up 之后直接跑 Playwright 全量 spec
#   ./scripts/uat_bootstrap.sh stop    # 停掉本次自举拉起的全部进程
#   ./scripts/uat_bootstrap.sh env     # 只打印 E2E_* 环境变量（供手工跑）
#
# 端口/账号/数据目录均可经环境变量覆盖（见下方默认值）。
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-up}"

# ── 可覆盖配置 ──────────────────────────────────────────────────────────────
DATA_DIR="${QUANT_DATA_DIR:-$ROOT/.uat-data}"     # 引擎数据目录（与 CI 同默认值）
BACKEND_PORT="${UAT_BACKEND_PORT:-18080}"         # quant 引擎端口
FRONT_PORT="${UAT_FRONT_PORT:-5173}"              # vite dev 端口（与 playwright baseURL 一致）
MOCK_PORT="${UAT_MOCK_PORT:-18789}"               # qmt-mock 假柜台端口
E2E_USER="${E2E_USER:-admin}"
E2E_PASS="${E2E_PASS:-Nightly!2026}"
E2E_USER2="${E2E_USER2:-tester}"
E2E_PASS2="${E2E_PASS2:-Tester!2026}"
QMT_TOKEN="${QMT_TOKEN:-uat-secret}"
PY="${PYTHON:-python3}"
BACKEND="http://127.0.0.1:${BACKEND_PORT}"
PIDDIR="$DATA_DIR/pids"

log()  { printf '\033[1;36m[uat-boot]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[uat-boot:warn]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[uat-boot:ERR]\033[0m %s\n' "$*" >&2; exit 1; }
# 本机 shell 有出站代理：访问本地服务必须 --noproxy '*'，否则拿到代理的 502 而非服务响应。
api()  { curl --noproxy '*' -fsS "$@"; }

# ── stop：结束后台进程 ──────────────────────────────────────────────────────
if [[ "$MODE" == "stop" ]]; then
  [[ -d "$PIDDIR" ]] || { log "无自举记录（$PIDDIR 不存在），无需停止"; exit 0; }
  for name in engine mock vite; do
    pf="$PIDDIR/$name.pid"
    if [[ -f "$pf" ]]; then
      pid="$(cat "$pf" 2>/dev/null || true)"
      if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        log "已停止 $name (pid=$pid)"
      fi
      rm -f "$pf"
    fi
  done
  # 兜底：清掉可能残留、占用本脚本端口的实例
  pkill -f "quant.*QUANT_ADDR=:${BACKEND_PORT}" 2>/dev/null || true
  exit 0
fi

# ── env：只打印环境变量 ─────────────────────────────────────────────────────
if [[ "$MODE" == "env" ]]; then
  # §3.1-1：E2E_QUOTE_SOURCE = 本次自举注入 mock 的行情源名（up 阶段落盘的文件）；
  # 消费方（web/e2e/uat_full.spec.mjs）据此判定「盘内形态是否已注入」，注入在场时
  # /api/status.quote_source 必须非空且命中 golden 枚举——空=旧盘外形态，只跑宽松断言。
  QS_FILE="$DATA_DIR/quote_source.txt"
  QS="$([[ -f "$QS_FILE" ]] && cat "$QS_FILE" || true)"
  cat <<EOF
export E2E_BASE_URL=http://localhost:${FRONT_PORT}
export E2E_API=${BACKEND}
export E2E_USER=${E2E_USER}
export E2E_PASS='${E2E_PASS}'
export E2E_USER2=${E2E_USER2}
export E2E_PASS2='${E2E_PASS2}'
export E2E_QUOTE_SOURCE='${QS}'
EOF
  exit 0
fi

[[ "$MODE" == "up" || "$MODE" == "run" ]] || die "未知模式：${MODE}（可用 up|run|stop|env）"

# ── 前置检查 ────────────────────────────────────────────────────────────────
command -v go >/dev/null   || die "缺 go 工具链"
command -v node >/dev/null || die "缺 node（vite 前端）"
command -v "$PY" >/dev/null || die "缺 ${PY}（seed 脚本依赖）"

# ── 1) 干净数据目录 + 构建二进制 ───────────────────────────────────────────
log "数据目录：$DATA_DIR"
rm -rf "$DATA_DIR"
mkdir -p "$DATA_DIR" "$PIDDIR"

log "构建 quant 引擎与 qmt-mock 假柜台..."
# §F6（2026-09-22）：与生产部署同款式注入 git 指纹（deploy_guangzhou.sh 步[1/5] / deploy_seoul.sh 步[1/8]
# 的 LDFLAGS="-X main.buildCommit=..."）。旧版裸 go build → UAT 栈 buildCommit 恒为 unknown，
# 引擎启动自检（cmd/quant/main.go）走"未注入指纹"告警分支，A7 真栈用例与线上口径脱节。
LDFLAGS="-X main.buildCommit=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
( cd "$ROOT" && go build -ldflags "${LDFLAGS}" -o "$PIDDIR/quant" ./cmd/quant )
( cd "$ROOT" && go build -o "$PIDDIR/qmt-mock" ./cmd/qmt-mock )

# ── 1b) §3.1-1/FIX_PLAN_20260922（M1/F4）盘内形态可测化：注入 quote_source + 行情时间戳 ──
# 缺陷原文：引擎 /api/status.quote_source 只在「盘内 + 行情链跑通」时才有值，盘外恒为空串，
#   E2E 的白名单断言（uat_full.spec.mjs 旧 :994-997）永远走 if(quote_source) 的假分支 → 词表漂移
#   （Go 侧恒吐小写英文 hithink/sina/ths/eastmoney，白名单只收中文）在 nightly 里从不被触发。
# 本步做法（零改 internal/**）：
#   ① 单一事实源：注入值只从 qmt_gateway/contract/quote_sources.json 解析（golden 缺失时显式告警
#      并退回旧行为=不注入，绝不在脚本里硬编一份中文/英文词表——那正是 M1 的病根）；
#   ② mock 注入面：qmt-mock 新增 -quote-source/-quote-tick-age-sec/-quote-tick-time-ms（见 cmd/qmt-mock），
#      /quotes 顶层回显注入的源名，E2E 据此把「mock 侧声明的源」与 golden 对齐做硬断言；
#   ③ 引擎侧「有源快照」：走引擎既有的 §GAP3.2 同日快照恢复通道（fetcher.LoadPersistedSnapshot 读
#      snapshot_latest.json，盘外 5s 采集循环本就暂停 → 快照原样保留），把注入源写成当日快照。
#      注：不伪造 tick 级数据、不改判定链，只让"快照带来源"这一盘内形态在盘外可复现。
GOLDEN_SOURCES="$ROOT/qmt_gateway/contract/quote_sources.json"
QUOTE_SOURCE="${UAT_QUOTE_SOURCE:-}"
if [[ -z "$QUOTE_SOURCE" ]]; then
  QUOTE_SOURCE="$("$PY" - "$GOLDEN_SOURCES" <<'PY' || true
# 解析 quote_source golden：输出本次自举要注入的源名（golden 缺失/无枚举时输出空串=不注入）。
# 选取口径：优先 "QMT-L1"（=mock 柜台驱动 L1 feed 的真实盘内形态），否则取枚举末位。
# 注：`$PY - <脚本路径>` 是本仓库既有约定（见下方 seed 段）。写成 `$PY <json> <<PY` 会让
# python 把 json 文件当程序执行（这份 golden 恰好是合法 Python 列表字面量 → 静默无输出、注入失效），
# 属真机踩点，务必保留 "-"。
import json, os, sys

path = sys.argv[1]
if not os.path.exists(path):
    sys.stderr.write("[uat-boot] golden 缺失: %s（quote_source 注入停用，E2E 侧将显式失败）\n" % path)
    print("")
    raise SystemExit(0)

raw = json.load(open(path))


def collect(obj):
    """按候选键取枚举串列表；候选键全不命中时退回"所有顶层字符串数组并集"。"""
    if isinstance(obj, list):
        return [x for x in obj if isinstance(x, str)]
    if not isinstance(obj, dict):
        return []
    for k in ("quote_sources", "sources", "values", "enum", "names"):
        v = obj.get(k)
        if isinstance(v, list):
            got = [x for x in v if isinstance(x, str)]
            if got:
                return got
    out = []
    for v in obj.values():
        if isinstance(v, list):
            out += [x for x in v if isinstance(x, str)]
    return out


vals = collect(raw)
if not vals:
    sys.stderr.write("[uat-boot] golden 无可用枚举: %s\n" % path)
    print("")
    raise SystemExit(0)
print("QMT-L1" if "QMT-L1" in vals else vals[-1])
PY
)"
fi
if [[ -n "$QUOTE_SOURCE" ]]; then
  log "§3.1-1 行情注入源=${QUOTE_SOURCE}（golden: ${GOLDEN_SOURCES#$ROOT/}）"
  echo "$QUOTE_SOURCE" > "$DATA_DIR/quote_source.txt"
else
  warn "§3.1-1 未取到 quote_source golden（$GOLDEN_SOURCES 缺失或无枚举）——本轮不注入，"
  warn "  /api/status.quote_source 将维持盘外空串，消费 golden 的 E2E 用例会显式失败提示。"
fi

# 预置「盘内快照」形态：snapshot_latest.json 字段形态与 Go 侧 data.MarketSnapshot 一致
# （无 json tag → Stocks/Sector/Time/Source 原样大写；StockInfo 用其 json tag）。
# 伪价格取 mock /quotes 的同一公式（digits%90+10 元），保证盘内外读到的价一致。
if [[ -n "$QUOTE_SOURCE" ]]; then
  QUOTE_SOURCE="$QUOTE_SOURCE" QUANT_DATA_DIR="$DATA_DIR" "$PY" - <<'PY'
import json, os
from datetime import datetime, timedelta, timezone
# 与 mock /quotes / 自举持仓 seed 同源的三只标的（600000.SH / 000001.SZ 亦即步骤 6 的模拟盘持仓）
codes = [("600000.SH", "浦发银行"), ("000001.SZ", "平安银行")]
def base_price(code):
    digits = int("".join(ch for ch in code if ch.isdigit()) or "0")
    return float((digits % 90 + 10))
stocks = {}
for code, name in codes:
    p = base_price(code)
    stocks[code] = {
        "code": code, "name": name, "price": p, "open": p - 0.5, "high": p + 0.3,
        "low": p - 0.3, "close": p, "prev_close": p, "volume": 1000000, "amount": p * 1000000,
        "change_pct": 0.0, "turnover": 0.0, "net_inflow": 0.0, "has_flow": False, "sector": "UAT",
    }
snap = {
    "Stocks": stocks,
    "Sector": [],
    # §LOW 族时区口径：显式 +08:00 偏移（不自签本机时区），与引擎 TradingDayDate 的北京日一致
    "Time": datetime.now(timezone(timedelta(hours=8))).isoformat(timespec="seconds"),
    "Source": os.environ["QUOTE_SOURCE"],
}
path = os.path.join(os.environ["QUANT_DATA_DIR"], "snapshot_latest.json")
json.dump(snap, open(path, "w"), ensure_ascii=False)
print("seeded snapshot_latest.json source=%s codes=%d" % (snap["Source"], len(stocks)))
PY
fi

# ── 2) 起 qmt-mock 假柜台（预置茅台持仓行情 + 1.5s 回报延迟）──────────────────
log "启动 qmt-mock (:${MOCK_PORT})..."
# §3.1-1：注入面参数按可用性拼接（golden 缺失时 MOCK_QUOTE_ARGS 为空数组 → 命令与旧版逐字一致）
MOCK_QUOTE_ARGS=()
if [[ -n "$QUOTE_SOURCE" ]]; then
  MOCK_QUOTE_ARGS=(-quote-source "$QUOTE_SOURCE")
fi
"$PIDDIR/qmt-mock" -listen ":${MOCK_PORT}" -token "$QMT_TOKEN" \
  -server "$BACKEND" -delay 1.5s \
  -seed "600519.SH,贵州茅台,200,1500.00" \
  "${MOCK_QUOTE_ARGS[@]+"${MOCK_QUOTE_ARGS[@]}"}" \
  > "$DATA_DIR/mock.log" 2>&1 &
echo $! > "$PIDDIR/mock.pid"

# ── 3) 起 quant 引擎（独立数据目录），等 /setup 可达 ────────────────────────
log "启动 quant 引擎 (:${BACKEND_PORT})..."
QUANT_DATA_DIR="$DATA_DIR" QUANT_ADDR=":${BACKEND_PORT}" \
  "$PIDDIR/quant" > "$DATA_DIR/engine.log" 2>&1 &
echo $! > "$PIDDIR/engine.pid"
for i in $(seq 1 60); do
  code="$(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' "$BACKEND/setup" 2>/dev/null || true)"
  [[ "$code" == "200" ]] && break
  sleep 1
done
[[ "$code" == "200" ]] || { cat "$DATA_DIR/engine.log" >&2; die "引擎未就绪（/setup 非 200）"; }
log "引擎就绪。"

# ── 4) seed 账号 + 租户配额 + QMT 配置 ──────────────────────────────────────
log "初始化 admin 账号 / 创建 tester / 上调租户配额 / 装配 QMT..."
TOKEN="$(api -X POST "$BACKEND/setup" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$E2E_USER\",\"password\":\"$E2E_PASS\"}" \
  | "$PY" -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[[ -n "$TOKEN" ]] || die "admin token 获取失败"
echo "$TOKEN" > "$DATA_DIR/admin.token"

api -X POST "$BACKEND/api/admin/users" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$E2E_USER2\",\"password\":\"$E2E_PASS2\",\"role\":\"user\"}" \
  > /dev/null

# §MT 租户级频控默认 600/min 与全套 Playwright 突发互踩（第 600 请求准时 429、失败集随顺序漂移）。
# 仅 UAT 栈上调配额；生产默认 600 不动。（与 nightly-e2e 同口径）
api -X PUT "$BACKEND/api/tenants/t_default" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"quota":{"api_rate_per_min":100000}}' \
  > /dev/null

api -X POST "$BACKEND/api/config/qmt" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d "{
    \"enabled\": true, \"mode\": \"manual\",
    \"gateway_url\": \"http://127.0.0.1:${MOCK_PORT}\", \"token\": \"$QMT_TOKEN\",
    \"price_type\": \"limit\", \"max_positions\": 5, \"fixed_amount\": 20000,
    \"initial_capital\": 500000, \"daily_max_buys\": 3,
    \"daily_budget_amount\": 100000, \"max_order_amount\": 50000,
    \"miss_heartbeat_sec\": 120,
    \"sell_unified_mode\": \"shadow\"
  }" > /dev/null

# ── 5) seed 情绪日线 + 夜研报告（否则情绪回看/报告弹窗用例无数据可断言）──────
log "seed market_risk_daily + paper_research_reports..."
QUANT_DATA_DIR="$DATA_DIR" "$PY" - <<'PY'
import os, sqlite3, json
from datetime import date, timedelta
db = sqlite3.connect(os.path.join(os.environ["QUANT_DATA_DIR"], "trading.db"))
d0 = date.today().strftime("%Y%m%d")
d1 = (date.today() - timedelta(days=1)).strftime("%Y%m%d")
# 昨日"偏冷/yellow"、今日"中性/green"，供情绪回看/风险档用例断言
for d, emo, tier in ((d1, "偏冷", "yellow"), (d0, "中性", "green")):
    db.execute(
        "INSERT OR REPLACE INTO market_risk_daily(trade_date,emotion,market_state,risk_tier,reasons,up_ratio,break_rate,max_pos_pct,limit_up_count,ladder_height) "
        "VALUES (?,?,?,?,?,?,?,?,?,?)",
        (d, emo, "震荡", tier, "uat seed", 0.55, 0.12, 0.6, 30, 4))
db.commit()
# auth 已迁 auth.json（trading.db 无 users 表）——admin id 从 auth.json 取
auth = json.load(open(os.path.join(os.environ["QUANT_DATA_DIR"], "auth.json")))
admin_id = next(u["id"] for u in auth["users"] if u.get("role") == "admin")
rep = {"generated_at": d0, "trades": [{"strategy_type": "dragon", "side": "buy", "count": 2,
       "total_amount": 40000.0, "avg_price": 10.5, "avg_slippage": 0.12, "avg_latency": 2.0}],
       "daily": [], "attribution": [], "emotion": {"days": 2, "last_phase": "中性", "phase_days": {}}}
db.execute("INSERT OR REPLACE INTO paper_research_reports(date,user_id,summary_json,created_at) VALUES (?,?,?,datetime('now'))",
           ("2026-09-17", admin_id, json.dumps(rep)))
db.commit()
print("seeded risk_daily + reports")
PY

# ── 6) seed 模拟盘持仓（否则手动卖出分支永远 skip —— 历史 P0 藏身处）──────────
log "seed 模拟盘持仓（两笔手动买入）..."
api -X POST "$BACKEND/api/paper/config" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"enabled": true, "auto_sell": false}' > /dev/null
for p in '600000.SH|浦发银行|10|500' '000001.SZ|平安银行|12|200'; do
  IFS='|' read -r code name price qty <<< "$p"
  api -X POST "$BACKEND/api/paper/buy" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "{\"code\":\"$code\",\"name\":\"$name\",\"strategy\":\"手动\",\"price\":$price,\"qty\":$qty}" > /dev/null
done

# ── 7) 重启引擎让 QMT 配置装配生效 + 持仓 filled_at 隔日改写（绕 T+1）────────
log "重启引擎装配 QMT 配置 + 隔日改写持仓..."
kill "$(cat "$PIDDIR/engine.pid")" 2>/dev/null || true
sleep 2
QUANT_DATA_DIR="$DATA_DIR" "$PY" - <<'PY'
import glob, json, os
from datetime import datetime, timedelta, timezone
files = glob.glob(os.path.join(os.environ["QUANT_DATA_DIR"], "accounts", "*", "paper.json"))
old = (datetime.now(timezone(timedelta(hours=8))) - timedelta(days=2)).isoformat()
n = 0
for path in files:
    st = json.load(open(path))
    positions = st.get("positions") or {}
    for pos in positions.values():
        pos["filled_at"] = old
        n += 1
    if positions:
        json.dump(st, open(path, "w"))
print("backdated positions:", n, "files:", len(files))
PY
QUANT_DATA_DIR="$DATA_DIR" QUANT_ADDR=":${BACKEND_PORT}" \
  "$PIDDIR/quant" > "$DATA_DIR/engine2.log" 2>&1 &
echo $! > "$PIDDIR/engine.pid"
for i in $(seq 1 60); do
  code="$(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' "$BACKEND/api/health" \
    -H "Authorization: Bearer $TOKEN" 2>/dev/null || true)"
  [[ "$code" == "200" ]] && break
  sleep 1
done
[[ "$code" == "200" ]] || { cat "$DATA_DIR/engine2.log" >&2; die "引擎重启后 /api/health 未 200"; }
log "引擎重启就绪。"

# ── 8) 前端依赖 + 起 vite dev ──────────────────────────────────────────────
if [[ ! -d "$ROOT/web/node_modules" ]]; then
  log "安装前端依赖（npm ci）..."
  ( cd "$ROOT/web" && npm ci )
fi
log "启动 vite dev (:${FRONT_PORT}, 代理→:${BACKEND_PORT})..."
( cd "$ROOT/web" && VITE_BACKEND_PORT="$BACKEND_PORT" \
    npx vite --port "$FRONT_PORT" --strictPort > "$DATA_DIR/vite.log" 2>&1 & echo $! > "$PIDDIR/vite.pid" )
for i in $(seq 1 60); do
  curl --noproxy '*' -fsS "http://127.0.0.1:${FRONT_PORT}" >/dev/null 2>&1 && break
  sleep 1
done
log "前端就绪。"

# ── 收尾 ───────────────────────────────────────────────────────────────────
echo
log "UAT 栈已就绪（admin token 存于 $DATA_DIR/admin.token）："
"$0" env
echo
log "跑 e2e：cd web && E2E_BASE_URL=http://localhost:${FRONT_PORT} E2E_API=${BACKEND} \\"
log "        E2E_USER=${E2E_USER} E2E_PASS='${E2E_PASS}' E2E_USER2=${E2E_USER2} E2E_PASS2='${E2E_PASS2}' npx playwright test"
log "停栈：  ./scripts/uat_bootstrap.sh stop"
log "日志：  $DATA_DIR/{mock,engine,engine2,vite}.log"

if [[ "$MODE" == "run" ]]; then
  log "运行 Playwright 全量 spec..."
  ( cd "$ROOT/web" && \
    E2E_BASE_URL="http://localhost:${FRONT_PORT}" E2E_API="$BACKEND" \
    E2E_USER="$E2E_USER" E2E_PASS="$E2E_PASS" \
    E2E_USER2="$E2E_USER2" E2E_PASS2="$E2E_PASS2" \
    E2E_QUOTE_SOURCE="$QUOTE_SOURCE" \
    npx playwright test )
fi
