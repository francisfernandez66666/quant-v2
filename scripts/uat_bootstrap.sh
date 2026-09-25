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
#
# §0925EVE-W3-H（2026-09-25 晚实跑锤出，FIX_PLAN_20260925EVE ㉓ F1/F2）——UAT 自举两缺陷：
#   F1 残留数据目录不幂等：上一轮 .uat-data 里 admin 已初始化时，`POST /setup` 回 409，
#      旧版把 curl -f 吐出的 HTML 错误体直接喂进 python json 解析器炸 traceback，真因被掩盖。
#      现口径：开头对"本脚本声明的数据目录"做**有防误删闸的重置**——路径恰为默认
#      $ROOT/.uat-data、或目录内有本脚本落的所有权标记文件（$UAT_MARKER）才自动清空重建；
#      两者皆不满足（如 QUANT_DATA_DIR 误指生产盘）一律拒绝清退、打人话报错退出。
#      错误体也不再裸喂解析器：非 200 先原样打印响应，再退出。
#   F2 就绪检查不验实例身份：旧版只看 `GET /setup==200` 就算就绪，别的进程占着 18080
#      （本机僵尸引擎 / quant-binance 同端口史有珠）即"假就绪"打到别人实例。现口径三段：
#      a) 启动前用 lsof 探 18080/18789/5173 三端口——占用者若不是本脚本上轮 pid 文件记录的
#         pid（杀旧只按 pid 文件，绝不用 pkill -f 捞人；§PICKILL-SCOPE 教训），直接报错退出
#         并打印占位者 pid/命令行，绝不静默复用；
#      b) 引擎就绪判据 =「自家 engine.pid 存活 AND GET /setup 回 JSON initialized:false
#         AND :$BACKEND_PORT 的监听者 pid == 本实例 pid」。/api/status 的 build_commit 指纹
#         更强但该端点挂 authMiddleware（server.go:697），初始化前拿不到 token——指纹比对
#         挪到 setup 成功后与重启后会合执行（verify_engine_fingerprint）；
#      c) 前端/重启就绪同样带 pid/端口归属校验，超时即红不再静默放行。
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
# §0925EVE-W3-H F1：数据目录所有权标记。目录内有此文件 = 本脚本声明的"一次性 UAT 盘"，
# 开头可自动 rm -rf 重建；无此文件且路径非默认 .uat-data = 拒绝清理（防 QUANT_DATA_DIR
# 误指生产/他人数据目录时被无脑删库）。
DEFAULT_DATA_DIR="$ROOT/.uat-data"
UAT_MARKER=".uat-managed-by-uat_bootstrap"

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
  # 兜底：清掉可能残留、占用本脚本端口的实例。
  # §PICKILL-SCOPE（2026-09-23 实测误伤）：macOS 的 `pkill -f` 把**环境变量一并计入匹配串**
  # （同机 `pgrep -fl` 输出里命令行后面拖着整段 env 即证），旧写法 `-f "quant.*QUANT_ADDR=:${BACKEND_PORT}"`
  # 会命中**任意一份其它 checkout** 中端口相同的引擎——本机另一项目 quant-binance 的引擎即被这样停掉过。
  # 现改按"本项目数据目录下的二进制绝对路径"匹配：路径含 DATA_DIR，天然只圈住自己拉起的进程。
  # §VITE-ORPHAN：记录的 vite.pid 是 `npx` 包装进程，kill 它只杀掉外壳，真正占端口的
  # node（web/node_modules/.bin/vite）会存活并让下一次 --strictPort 自举直接失败。
  # 按"本仓库绝对路径 + 本次前端端口"精确匹配补杀，仍然只圈自己拉起的进程（见 §PICKILL-SCOPE）。
  pkill -f -- "$ROOT/web/node_modules/\.bin/vite --port ${FRONT_PORT}( |$)" 2>/dev/null || true
  pkill -f -- "$PIDDIR/quant" 2>/dev/null || true
  pkill -f -- "$PIDDIR/qmt-mock" 2>/dev/null || true
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
# §UAT-PORTS：mock 地址必须随端口覆盖一起下发——e2e 里硬编 18789 时，换端口跑会打到
# 同机另一份 checkout 的 mock 上并拿到 200（假绿）。传了该变量即表示"跑在自举栈上"，
# 用例据此把"连不上 mock"从 skip 升级为判红。
export E2E_MOCK_URL=http://127.0.0.1:${MOCK_PORT}
export QMT_TOKEN=${QMT_TOKEN}
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

# §0925EVE-W3-H F2 辅助：端口归属三件套（lsof/ps 判读 + 归属断言）。
# HAVE_LSOF=0 时端口判据降级放行（只剩 pid 存活 + /setup 身份两级判据），并显式告警
# 残余风险="别的进程占着同端口应答"无法用监听者归属排除——macOS/Ubuntu 均自带 lsof，
# 走到降级分支本身就是异常环境，宁可红着提示也不假装全绿。
HAVE_LSOF=0
command -v lsof >/dev/null && HAVE_LSOF=1
# 列出监听某端口的 pid（每行一个；无 lsof 或无监听者时输出空）
lsof_listeners() { [[ "$HAVE_LSOF" == 1 ]] || return 0; lsof -nP -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null || true; }
# 某 pid 的命令行摘要（占位者报告用）
proc_cmd() { ps -p "$1" -o command= 2>/dev/null | cut -c1-200 || echo "<进程已消失>"; }
# 断言 <pid> 在 <port> 上有监听归属；lsof 缺失时恒真（降级）
port_owned_by() {
  [[ "$HAVE_LSOF" == 1 ]] || return 0
  lsof_listeners "$1" | grep -qx "$2"
}

# ── 1) 干净数据目录（§0925EVE-W3-H F1：带防误删闸的重置）────────────────────
log "数据目录：$DATA_DIR"
# 先抢救上一轮 pid 记录（F2-a 用）：rm -rf 之后 PIDDIR 就没了，必须在清理前读。
# 只认本脚本 pid 文件里的 pid——macOS pkill -f 连 env 一起匹配会误伤同机其它 checkout
# 的教训见 §PICKILL-SCOPE，这里从根上不给它出场机会。
OLD_PIDS=""
if [[ -d "$PIDDIR" ]]; then
  for pf in "$PIDDIR"/*.pid; do
    [[ -f "$pf" ]] || continue
    OLD_PIDS="$OLD_PIDS $(cat "$pf" 2>/dev/null || true)"
  done
fi
if [[ -d "$DATA_DIR" ]]; then
  if [[ "$DATA_DIR" == "$DEFAULT_DATA_DIR" || -f "$DATA_DIR/$UAT_MARKER" ]]; then
    # 默认路径或带本脚本所有权标记：确认是 UAT 一次性盘，直接清空重建（幂等，上一轮残留
    # 不再需要人工处理——旧版"目录还在但 pid 记录已死"就卡在这炸 409）。
    log "检测到上一轮 UAT 残留（${DATA_DIR}），按本脚本声明的一次性目录口径自动清空重建"
    rm -rf "$DATA_DIR"
  else
    # 非默认路径且无标记文件：脚本没有权限替用户删目录。报错退出打人话，绝不 rm、
    # 也绝不带着旧初始化态继续跑（旧版正是"不清理硬上"→ POST /setup 409 → 错误体喂
    # json 解析器炸 traceback 掩盖真因）。
    die "上一轮 UAT 残留：目标数据目录 $DATA_DIR 非本脚本默认盘（${DEFAULT_DATA_DIR}）且无所有权标记 ${UAT_MARKER}，拒绝自动清理。请先人工确认内容后执行 rm -rf ${DATA_DIR}，或不设 QUANT_DATA_DIR 用默认盘重跑。"
  fi
fi
mkdir -p "$DATA_DIR" "$PIDDIR"
# 落所有权标记：此后该目录被本脚本认领，下轮开头可安全自动重置
printf 'uat_bootstrap.sh 声明的一次性 UAT 数据目录（删除无害；勿放生产数据）\n' > "$DATA_DIR/$UAT_MARKER"

# §0925EVE-W3-H F2-a：启动前端口清场。
# 只回收"本脚本上轮 pid 文件记录的 pid"；其余占位者一律报错退出并打印 pid/命令行，
# 绝不静默复用（静默复用=打到别人实例上产假绿/假红，09-25 晚两次 409 的真身）。
for pid in $OLD_PIDS; do
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    log "已回收上轮自举遗留进程 pid=${pid}（依据 $PIDDIR 的 *.pid 记录，非模式匹配）"
  fi
done
if [[ -n "$OLD_PIDS" ]]; then sleep 1; fi   # 给 SIGTERM 一点释放端口的时间
for spec in "$BACKEND_PORT:引擎" "$MOCK_PORT:qmt-mock" "$FRONT_PORT:vite"; do
  port="${spec%%:*}"; name="${spec##*:}"
  pids="$(lsof_listeners "$port")"
  if [[ -n "$pids" ]]; then
    for occ in $pids; do
      die "端口 :${port}（${name} 期望口）仍被非本脚本进程占用 pid=${occ}：$(proc_cmd "$occ")。本脚本不复用/不抢占，请先停掉该进程，或经 UAT_BACKEND_PORT/UAT_MOCK_PORT/UAT_FRONT_PORT 换端口。"
    done
  fi
done
[[ "$HAVE_LSOF" == 1 ]] || warn "缺 lsof：端口占用探测降级为 pid 存活 + /setup 身份两级判据（残余风险=他人进程占同端口应答不可辨）"

log "构建 quant 引擎与 qmt-mock 假柜台..."
# §F6（2026-09-22）：与生产部署同款式注入 git 指纹（deploy_guangzhou.sh 步[1/5] / deploy_seoul.sh 步[1/8]
# 的 LDFLAGS="-X main.buildCommit=..."）。旧版裸 go build → UAT 栈 buildCommit 恒为 unknown，
# 引擎启动自检（cmd/quant/main.go）走"未注入指纹"分支，A7 真栈用例与线上口径脱节。
# §0925EVE-W3-H F2-b：BUILD_COMMIT 同时是就绪后的身份指纹——/api/status.build_commit 必须等于
# 本值（unknown 时跳过比对并告警），防"打到了别的构建的实例"。
BUILD_COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
LDFLAGS="-X main.buildCommit=${BUILD_COMMIT}"
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

# ── 3) 起 quant 引擎（独立数据目录），等"确认是本实例"的就绪 ─────────────────
log "启动 quant 引擎 (:${BACKEND_PORT})..."
QUANT_DATA_DIR="$DATA_DIR" QUANT_ADDR=":${BACKEND_PORT}" \
  "$PIDDIR/quant" > "$DATA_DIR/engine.log" 2>&1 &
ENGINE_PID=$!
echo "$ENGINE_PID" > "$PIDDIR/engine.pid"
# §0925EVE-W3-H F2-b 就绪判据（旧版"GET /setup==200 即就绪"作废）三条同时满足：
#   ① 自家 engine.pid 存活——进程半路崩（如端口绑定失败）当场红，不用等超时；
#   ② GET /setup 回**JSON 且 initialized:false**——本脚本刚 rm -rf 重建过数据目录，
#      健康的本实例此刻必然未初始化；别的已长期运行的实例会回 true，直接锤死"假就绪"
#      （旧版只看状态码 200，僵尸引擎/quant-binance 同端口应答时打到别人家毫无感知）；
#   ③ :$BACKEND_PORT 的监听者 pid == ENGINE_PID——响应确认由本进程发出（lsof 缺失时降级跳过）。
# 注：/api/status 的 build_commit 指纹是更强的第四判据，但该端点挂 authMiddleware
# （internal/server/server.go:697），此刻尚无 token——指纹比对后置到 setup 成功之后（见步 4 尾）。
ready=0
for i in $(seq 1 60); do
  kill -0 "$ENGINE_PID" 2>/dev/null || { cat "$DATA_DIR/engine.log" >&2; die "引擎进程已退出（pid=${ENGINE_PID}），见上方 engine.log"; }
  setup_probe="$(curl --noproxy '*' -s "$BACKEND/setup" 2>/dev/null || true)"
  # JSON 解析失败（空串/HTML/别的进程乱吐）一律按未就绪处理，不打 traceback——09-25 晚
  # 就是 409 错误体被喂进解析器炸出 traceback 掩盖真因。
  if printf '%s' "$setup_probe" | "$PY" -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if d.get("initialized") is False else 1)' 2>/dev/null; then
    if port_owned_by "$BACKEND_PORT" "$ENGINE_PID"; then
      ready=1; break
    fi
    # /setup 说"未初始化"但监听者不是自家 pid：极小竞态窗口（别人进程刚好也回 false），
    # 不判死，继续轮询等自家监听就绪；60 轮仍如此则下方超时红。
  fi
  sleep 1
done
[[ "$ready" == 1 ]] || { cat "$DATA_DIR/engine.log" >&2; die "引擎未就绪：60s 内未同时满足「pid=$ENGINE_PID 存活 + /setup 回 initialized:false + :${BACKEND_PORT} 监听归属本 pid」——若日志显示端口绑定失败，多半是他进程占口（见 F2-a 报错口径），最后一次的 /setup 原始响应：${setup_probe:-<空>}"; }
log "引擎就绪（pid=$ENGINE_PID 身份已核验）。"

# ── 4) seed 账号 + 租户配额 + QMT 配置 ──────────────────────────────────────
log "初始化 admin 账号 / 创建 tester / 上调租户配额 / 装配 QMT..."
# §0925EVE-W3-H F1：POST /setup 不再 `curl -f | python`——旧写法在 409（已初始化）时把
# 错误体直接喂进 json 解析器，traceback 刷屏且掩盖"上一轮残留/假就绪"真因。
# 现口径：状态码与响应体分开捕获；非 200 原样打印响应再打人话退出；200 但解析失败
# 同样打印原始响应（错误体永远先见天日，再谈解析）。
setup_http="$(curl --noproxy '*' -s -w '\n%{http_code}' -X POST "$BACKEND/setup" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$E2E_USER\",\"password\":\"$E2E_PASS\"}")"
setup_code="${setup_http##*$'\n'}"           # 末行 = HTTP 状态码
setup_body="${setup_http%$'\n'*}"            # 去掉末行 = 原始响应体
if [[ "$setup_code" != "200" ]]; then
  if [[ "$setup_code" == "409" ]]; then
    die "POST /setup 回 409（admin 已初始化）。数据目录本轮已按 F1 重置且就绪判据核验过 initialized:false，此刻 409 只可能是：①打到了别的实例（F2 防线破口，请查 lsof -iTCP:${BACKEND_PORT}）；②竞态抢跑。原始响应：${setup_body}"
  fi
  die "POST /setup 失败（HTTP ${setup_code}）。原始响应：${setup_body}"
fi
if ! TOKEN="$(printf '%s' "$setup_body" | "$PY" -c 'import json,sys;print(json.load(sys.stdin)["token"])' 2>/dev/null)"; then
  die "POST /setup 回了 200 但响应解析不出 token（形态非契约？）。原始响应：${setup_body}"
fi
[[ -n "$TOKEN" ]] || die "admin token 获取失败（响应 token 为空）。原始响应：${setup_body}"
echo "$TOKEN" > "$DATA_DIR/admin.token"

# §0925EVE-W3-H F2-b：拿到 token 后补第四身份判据——GET /api/status.build_commit 必须
# 等于本次 go build 注入的 BUILD_COMMIT（该端点挂 authMiddleware，初始化前探不了，故后置）。
# unknown 场景（git 不可用/裸构建）无指纹可比，降级告警。
verify_engine_fingerprint() {
  local stage="$1" st_probe bc
  st_probe="$(curl --noproxy '*' -s "$BACKEND/api/status" -H "Authorization: Bearer $TOKEN" 2>/dev/null || true)"
  bc="$(printf '%s' "$st_probe" | "$PY" -c 'import json,sys;print(json.load(sys.stdin).get("build_commit",""))' 2>/dev/null || true)"
  if [[ "$BUILD_COMMIT" == "unknown" ]]; then
    warn "§0925EVE-W3-H ${stage}：本构建无 git 指纹（BUILD_COMMIT=unknown），build_commit 比对跳过（残余风险=无法辨构建代次，端口归属判据仍生效）"
    return 0
  fi
  if [[ "$bc" != "$BUILD_COMMIT" ]]; then
    die "§0925EVE-W3-H ${stage}：/api/status.build_commit='${bc:-<空/解析失败>}' ≠ 本实例注入值 '${BUILD_COMMIT}'——打到了别的构建的实例（假就绪），原始响应：${st_probe}"
  fi
  log "${stage}：build_commit 指纹比对通过（${BUILD_COMMIT}）。"
}
verify_engine_fingerprint "setup 后"

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
ENGINE_PID=$!
echo "$ENGINE_PID" > "$PIDDIR/engine.pid"
# §0925EVE-W3-H F2-b：重启后就绪判据同步升级——此刻系统已初始化（/setup 恒回
# initialized:true，"未初始化"身份证据作废），改用「pid 存活 + /api/health 200 +
# 端口监听归属新 pid」三判据，随后再走 build_commit 指纹四判据（verify_engine_fingerprint）。
ready=0
for i in $(seq 1 60); do
  kill -0 "$ENGINE_PID" 2>/dev/null || { cat "$DATA_DIR/engine2.log" >&2; die "引擎重启进程已退出（pid=${ENGINE_PID}），见上方 engine2.log"; }
  code="$(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' "$BACKEND/api/health" \
    -H "Authorization: Bearer $TOKEN" 2>/dev/null || true)"
  if [[ "$code" == "200" ]] && port_owned_by "$BACKEND_PORT" "$ENGINE_PID"; then
    ready=1; break
  fi
  sleep 1
done
[[ "$ready" == 1 ]] || { cat "$DATA_DIR/engine2.log" >&2; die "引擎重启未就绪：60s 内未同时满足「pid=$ENGINE_PID 存活 + /api/health 200 + :${BACKEND_PORT} 监听归属本 pid」"; }
verify_engine_fingerprint "重启后"
log "引擎重启就绪（pid=$ENGINE_PID 身份已核验）。"

# ── 8) 前端依赖 + 起 vite dev ──────────────────────────────────────────────
if [[ ! -d "$ROOT/web/node_modules" ]]; then
  log "安装前端依赖（npm ci）..."
  ( cd "$ROOT/web" && npm ci )
fi
log "启动 vite dev (:${FRONT_PORT}, 代理→:${BACKEND_PORT})..."
# §0925EVE 收口补（本机两连锤，macOS bash 3.2）：
#   ① 不能用 `npx vite`——$! 记的是 npx 包装进程，真监听者是其子 node，收尸打空（§VITE-ORPHAN 兜底）；
#   ② 也不能用 `( cd web && bin & echo $! )`——bash 3.2 下这个子壳会把端口监听者留作自己的子进程
#      且自身不随主脚本退出（实录：vite.pid 记成子壳 pid、孤儿子壳攥着 stdout 管道让上层 tail 永不
#      EOF）。改顶层直起绝对路径本地 bin（shebang `#!/usr/bin/env node` 走 exec，$! 即监听者），
#      顶层 `&` 挂一个只做 cd+exec 的子壳——exec 让该子壳直接变身为 node 监听者，
#      $! 即真身、无中间层（vite 无 --root 选项，root 是位置参数，改走 cwd 定位项目根；
#      且保留 cmdline 与 §VITE-ORPHAN 兜底模式的 `vite --port <port>` 前缀序）。
#      真值最终以 lsof 反查为准：就绪后反查端口属主、验 cmdline+cwd 归属才落 vite.pid，
#      拿不到/对不上直接红——身份缺失绝不静默放行（F2-c 口径的另一半）。
( cd "$ROOT/web" && VITE_BACKEND_PORT="$BACKEND_PORT" \
    exec ./node_modules/.bin/vite --port "$FRONT_PORT" --strictPort > "$DATA_DIR/vite.log" 2>&1 ) &
VITE_BG_PID=$!
echo "$VITE_BG_PID" > "$PIDDIR/vite.pid"
# §0925EVE-W3-H F2-c：旧版前端探测循环超时后**静默放行**（无收尾断言），vite 没起来也照跑
# 全套 e2e 红成雾。现超时即红并附 vite.log；--strictPort 保证端口被占时 vite 自败，
# 与步 1 前的端口清场（F2-a）互为犄角。
# §0925EVE 收口补（本机首跑锤出）：探测主机名必须走 localhost 而非 127.0.0.1——
# macOS 上 node 裸起 vite 默认只绑 [::1]（实测 lsof 只见 IPv6），127.0.0.1 探测 60s
# 恒不可达＝就绪判据结构性必红；而 Playwright baseURL 就是 http://localhost:5173，
# 判据与真实消费面对齐（§探针按运行时真实取值链口径）。
front_ready=0
for i in $(seq 1 60); do
  if curl --noproxy '*' -fsS "http://localhost:${FRONT_PORT}" >/dev/null 2>&1; then front_ready=1; break; fi
  sleep 1
done
[[ "$front_ready" == 1 ]] || { cat "$DATA_DIR/vite.log" >&2; die "前端未就绪：60s 内 http://localhost:${FRONT_PORT} 不可达（见上方 vite.log；若为端口占用，对照 F2-a 口径排查占位进程）"; }
# §0925EVE 收口补：就绪不等于身份核验——vite.pid 必须钉在**真正的端口属主**上，
# stop 的按 pid 收尸才不会打空。三查：lsof 反查监听 pid → cmdline 含本次 vite 与端口 →
# cwd 落在本仓库 web/（同机另一 checkout 抢口时 cmdline 形态相同，只有 cwd 能区分）。
# 任一查不到即红：宁可停机，也不让一个 pid 错位的 vite 逃过下次 stop。
VITE_LPID="$(lsof -nP -iTCP:"${FRONT_PORT}" -sTCP:LISTEN -t 2>/dev/null | head -1 || true)"
[[ -n "$VITE_LPID" ]] || die "前端就绪但反查不到 :${FRONT_PORT} 监听 pid（lsof 为空）——vite.pid 拒绝登记一个未知属主"
VITE_LCMD="$(ps -o command= -p "$VITE_LPID" 2>/dev/null || true)"
case "$VITE_LCMD" in
  *node_modules/.bin/vite\ --port\ ${FRONT_PORT}*) ;;
  *) die ":${FRONT_PORT} 监听者不是本次拉起的 vite（cmdline：${VITE_LCMD}）——拒绝把 vite.pid 记到别人头上";;
esac
VITE_LCWD="$(lsof -a -p "$VITE_LPID" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -1 || true)"
case "$VITE_LCWD" in
  "$ROOT/web"|"$ROOT") ;;
  *) die ":${FRONT_PORT} 监听者 cwd=${VITE_LCWD} 不属于本仓库（$ROOT/web）——疑似另一份 checkout 占口，拒绝登记";;
esac
echo "$VITE_LPID" > "$PIDDIR/vite.pid"
log "前端就绪（vite.pid=$VITE_LPID 已核验为端口真实属主：cmdline+cwd 双查）。"

# ── 收尾 ───────────────────────────────────────────────────────────────────
echo
log "UAT 栈已就绪（admin token 存于 $DATA_DIR/admin.token）："
"$0" env
echo
log "跑 e2e：cd web && E2E_BASE_URL=http://localhost:${FRONT_PORT} E2E_API=${BACKEND} \\"
log "        E2E_MOCK_URL=http://127.0.0.1:${MOCK_PORT} QMT_TOKEN=${QMT_TOKEN} \\"
log "        E2E_USER=${E2E_USER} E2E_PASS='${E2E_PASS}' E2E_USER2=${E2E_USER2} E2E_PASS2='${E2E_PASS2}' npx playwright test"
log "停栈：  ./scripts/uat_bootstrap.sh stop"
log "日志：  $DATA_DIR/{mock,engine,engine2,vite}.log"

if [[ "$MODE" == "run" ]]; then
  log "运行 Playwright 全量 spec..."
  ( cd "$ROOT/web" && \
    E2E_BASE_URL="http://localhost:${FRONT_PORT}" E2E_API="$BACKEND" \
    E2E_MOCK_URL="http://127.0.0.1:${MOCK_PORT}" QMT_TOKEN="$QMT_TOKEN" \
    E2E_USER="$E2E_USER" E2E_PASS="$E2E_PASS" \
    E2E_USER2="$E2E_USER2" E2E_PASS2="$E2E_PASS2" \
    E2E_QUOTE_SOURCE="$QUOTE_SOURCE" \
    npx playwright test )
fi
