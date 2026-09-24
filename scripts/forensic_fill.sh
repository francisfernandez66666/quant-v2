#!/usr/bin/env bash
# forensic_fill.sh — §FILL-AMEND 第 1 步（2026-09-23 夜间批）：成交流水方向的**只读取证**五方对照。
#
# 职责（入参：交易日 yyyy-MM-dd + 代码，可选 user 键名）：把下面五方原始记录摆在一起打印，
# 并在末尾给一段自动结论提示（只提示、绝不改动）：
#   ① live.db `fills`          —— 本地成交流水（side/price/qty/amount/traded_at/signal_id/order_id/trade_id/serial/user）
#   ② live.db `orders`         —— 同锚点委托行：下单时写下的方向 + 当前状态（status 只读展示，不改）
#   ③ live.db `real_positions` —— 该票当前持仓行：这笔若是卖出，数量该减却减了没有
#   ④ live.db `real_account`   —— 账户现金快照方向性核对（available/frozen/updated_at；user 一律掩码，不出明文）
#   ⑤ 网关 `dispatch` 表        —— 我们下单时落下的方向（方案 §0 事实10 认定的唯一权威源）
#      + 网关 `fills`（柜台/桥回报口径）
#      + 网关日志本码相关的 `side detect failed` / `trade side mismatch`：只出命中条数 + 时间戳 + 方向值
#
# 为什么只读（这笔账的全部难点都在这里）：
#   现网那笔 `09-22 10:08:32 603468.SH 买入 22.55 900 ￥20295.00 manual` owner 断言实际是卖出，
#   但它到底是「网关查不到派发项、于是采信了桥/柜台的枚举推断」还是「派发行根本没落盘」，
#   **光读代码定不下来**，两种成因的修法也不同（前者补 §SIDE-AUTH-2 的 fail-close，后者要另批修派发落盘）。
#   更要紧的是：fills 是实盘唯一客观流水，改判一行方向会连带五处派生量——
#     持仓数量、加权成本、T+1 可卖量（当日买入量由 fills side=买入 聚合而来）、
#     当日买入笔数与预算三本账（已成交 + 在途冻结 − 卖出回款；回款按 side=卖出 求和）、
#     熔断与纪律/胜负/各战法卖出额统计（由 fills 重放）。
#   没有券商原始凭证就动账 = 拿猜测污染账本。所以本脚本**只摆证据**，
#   任何改判都必须走 §FILL-AMEND 第 2 步的人工逐笔勘误通道（fill_amendments 表 + 守恒自检 + 可撤销）。
#
# 只读是怎么落实的（不是口号，是四道机制）：
#   1) 每条 SQL 都是单条 SELECT 语句，运行前逐文件机器复核（首词非 SELECT/dot 命令即非 0 退出）；
#   2) 现网库文件一律**先拷到独立临时目录（远端副本 + 本地副本各一次），再只对副本查询**——
#      不给现网库加任何锁，也不会读到半态；本地查询用 mode=ro URI 打开副本，双保险；
#   3) 不做 VACUUM、不做任何 PRAGMA 写、不建索引、不落任何文件到仓库工作树；
#   4) 输出只允许字段名/计数/聚合值/时间戳/方向枚举/掩码前缀：user 键名以 sha256 前 12 位呈现，
#      日志侧只回显「时间戳 + dispatch=…/inferred=…」切片，原文不整段回显（token 类字面值进不了输出）。
#
# 用法：
#   本地离线（本机 sqlite 库，可无网络跑通全部逻辑）：
#     ./scripts/forensic_fill.sh --local <live.db> 2026-09-22 603468.SH [user键名]
#     可选环境变量：GW_DB=<网关库> GW_LOG_DIR=<网关日志目录>
#   现网（广州单机全合一：引擎与 qmt_gateway 同机，见 docs/MIGRATION_GUANGZHOU_ALLINONE.md §1；
#         首尔链路已撤，因此 live.db 与网关 data.db 都在 GZ_IP 这一台机器上）：
#     GZ_IP=<广州公网IP> ./scripts/forensic_fill.sh 2026-09-22 603468.SH
#     可选环境变量（全部与现成脚本同源，不新增任何凭据）：
#       GZ_USER       默认 Administrator（与 deploy_guangzhou.sh / survey_live_rules.sh 一致）
#       DATA_DIR      默认 C:/var/lib/quant-trading-v2（live.db 所在，§OPT-3 与 trading.db 拆分）
#       GW_DIR        默认 C:/qmt/quant-trading-v2/qmt_gateway（网关目录）
#       GW_DB         默认 <GW_DIR>/data.db（config.xt.template.json 的 "db": "data.db"）
#       GW_LOG_GLOB   默认 <GW_DIR>/gateway-*.log（网关自管 UTF-8 轮转日志，gateway.py 日志段）
#       SQLITE_GZ     远端 sqlite3 可执行文件，默认 sqlite3；**找不到就自动改走同机 Python 只读腿**
#                     （见 write_py_runner：两条腿的只读强度相同，Python 腿不是"降级"）
#   演练（不连网、不取数，只打印将要执行的 SQL 与远端命令清单）：
#     ./scripts/forensic_fill.sh --dry-run 2026-09-22 603468.SH
#
# 退出码：0 = 取证流程完整跑完（结论可以是任何一种，包括"无法判定"）；
#         非 0 = 参数/工具/通道/取数任一环节失败。§M-8/§N-6 姿势：缺工具或取不到数
#         一律显式失败，绝不"降级却报成功"。
set -u

PROG="forensic_fill.sh"

# ── 参数解析 ──────────────────────────────────────────────────────────────
LOCAL_MODE=0
DRY_RUN=0
LOCAL_LIVE_DB=""
POS=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --local)
      LOCAL_MODE=1
      if [ -z "${2:-}" ]; then echo "X 用法：--local 后面要跟 live.db 路径" >&2; exit 2; fi
      LOCAL_LIVE_DB="$2"; shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '1,30p' "$0"; exit 0 ;;
    --*) echo "X 未知参数：$1" >&2; exit 2 ;;
    *) POS+=("$1"); shift ;;
  esac
done
if [ "${#POS[@]}" -lt 2 ] || [ "${#POS[@]}" -gt 3 ]; then
  echo "X 用法：$PROG [--local <live.db>] [--dry-run] <yyyy-MM-dd> <代码> [user键名|-]" >&2
  exit 2
fi
DAY="${POS[0]}"
CODE="$(printf '%s' "${POS[1]}" | tr 'a-z' 'A-Z')"
USER_KEY="${POS[2]:-}"
[ "$USER_KEY" = "-" ] && USER_KEY=""

if ! printf '%s' "$DAY" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'; then
  echo "X 日期格式必须是 yyyy-MM-dd，收到：${DAY}" >&2; exit 2
fi
if ! printf '%s' "$CODE" | grep -Eq '^[0-9A-Za-z.]+$'; then
  echo "X 代码格式异常（只允许字母数字与点）：${CODE}" >&2; exit 2
fi
if printf '%s' "$USER_KEY" | grep -Eq "[^A-Za-z0-9_.:@-]"; then
  echo "X user 键名含非法字符（拒当 SQL 字面量拼进去）" >&2; exit 2
fi
CODE_BARE="${CODE%%.*}"

GZ_IP="${GZ_IP:-}"
GZ_USER="${GZ_USER:-Administrator}"
DATA_DIR="${DATA_DIR:-C:/var/lib/quant-trading-v2}"
GW_DIR="${GW_DIR:-C:/qmt/quant-trading-v2/qmt_gateway}"
GW_DB="${GW_DB:-${GW_DIR}/data.db}"
GW_LOG_GLOB="${GW_LOG_GLOB:-${GW_DIR}/gateway-*.log}"
SQLITE_GZ="${SQLITE_GZ:-sqlite3}"
LIVE_DB_REMOTE="${LIVE_DB_REMOTE:-${DATA_DIR}/live.db}"
GW_LOG_LOCAL="${GW_LOG_DIR:-}"

# SSH/SCP 装配照抄 scripts/survey_live_rules.sh（BatchMode 预探测 + IdentitiesOnly + 指定私钥）：
# 本仓库既有实录教训——agent 挂多把 key 时 ssh 会逐把尝试直至跌落密码认证，表现为
# "无任何输出的挂死"。故先 BatchMode 探活，不通立刻失败，绝不进入可能挂起的路径。
SSH_OPTS="-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"
SSH_BIN="ssh $SSH_OPTS ${GZ_USER}@${GZ_IP}"
SCP_BIN="scp -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes -i $HOME/.ssh/id_rsa"
SCP_TARGET="${GZ_USER}@${GZ_IP}"

# ── 工具闸：缺 sqlite3 / sha256sum / awk 直接非 0 退出（不静默跳过）─────────
need_cmd() {
  command -v "$1" >/dev/null 2>&1 && return 0
  echo "X 缺少可执行文件 $1：$PROG 无法在只读模式下取数，按失败退出（不降级报成功）。" >&2
  return 1
}
need_cmd sqlite3 || exit 3
need_cmd sha256sum || exit 3
need_cmd awk || exit 3

# ── 临时目录：只查这里的只读副本；退出时无条件删除 ───────────────────────────
TMP="$(mktemp -d "${TMPDIR:-/tmp}/forensic_fill.XXXXXX")" || { echo "X mktemp 失败" >&2; exit 3; }
chmod 700 "$TMP"
mkdir -p "$TMP/sql"
REMOTE_STAGE=""
cleanup() {
  rm -rf "$TMP" 2>/dev/null
  # 远端只删本脚本自己建的暂存目录（里面只有本次拷出的只读副本），不碰现网原库/原文件。
  # 前缀自证：递归删除前先确认路径就是本脚本的 forensic_fill_<PID> 目录——变量被改写或为空时
  # 宁可留个垃圾目录让人工删，也绝不在现网机器上发一条作用域不明的 Remove-Item -Recurse。
  if [ -n "$REMOTE_STAGE" ] && [ "${DRY_RUN:-0}" = "0" ]; then
    case "$REMOTE_STAGE" in
      */forensic_fill_[0-9]*)
        $SSH_BIN "powershell -NoProfile -Command \"Remove-Item -Recurse -Force '${REMOTE_STAGE}'; exit 0\"" >/dev/null 2>&1 \
          || echo "[warn] 远端暂存目录未清（${REMOTE_STAGE}，内为本次只读副本），请手工删除。" >&2 ;;
      *) echo "[warn] 远端暂存路径不符合 forensic_fill_<PID> 形态，已拒绝删除：${REMOTE_STAGE}" >&2 ;;
    esac
  fi
}
trap cleanup EXIT

# ── 用户过滤谓词（各表列名不同，故分开给）───────────────────────────────────
# 注意：dispatch（⑤a）故意不按 user 过滤 —— 要找的是"这笔成交到底有没有派发项"，
# 若把归属不同/为空的那一行筛掉了，就会把"查得到但被过滤"误报成"没有派发行"。
if [ -n "$USER_KEY" ]; then
  P_FILLS="AND (f.user_id='${USER_KEY}' OR COALESCE(f.user_id,'')='')"
  P_ORDERS="AND (o.user_id='${USER_KEY}' OR COALESCE(o.user_id,'')='')"
  P_POS="AND (p.user_id='${USER_KEY}' OR COALESCE(p.user_id,'')='')"
  P_ACC="AND (a.user_id='${USER_KEY}' OR COALESCE(a.user_id,'')='')"
else
  P_FILLS=""; P_ORDERS=""; P_POS=""; P_ACC=""
fi

# ── SQL 装配：每条 = 一个 .sql 文件（dot 命令 + 单条 SELECT，全部 ASCII）──────
# 表存在性不靠猜：老库可能缺 serial/trade_id 等迁移列，统一用 COALESCE + 现有列集。
write_sql() { # $1=名字 $2=SELECT
  {
    printf '.headers on\n.separator "|"\n.mode list\n'
    printf '%s;\n' "$2"
  } > "$TMP/sql/$1.sql"
}

build_sql_live() {
  # ① fills：该日该票全部成交行
  write_sql 01_fills "SELECT f.id AS fill_id, f.order_id, f.code, f.side, f.price, f.qty, f.amount, f.traded_at, COALESCE(f.signal_id,'') AS signal_id, COALESCE(f.user_id,'') AS ukey, COALESCE(f.fee,0) AS fee, COALESCE(f.stamp_tax,0) AS stamp_tax, COALESCE(f.serial,'') AS serial, COALESCE(f.trade_id,'') AS trade_id FROM fills f WHERE substr(f.traded_at,1,10)='${DAY}' AND (f.code='${CODE}' OR f.code='${CODE_BARE}') ${P_FILLS}"
  # ② orders：该日该票委托行（按 order_id / signal_id 与 fills 双向关联，含 pend: 占位行）
  write_sql 02_orders "SELECT o.order_id, COALESCE(o.signal_id,'') AS signal_id, o.code, o.side, o.status, o.price, o.qty, o.created_at, COALESCE(o.user_id,'') AS ukey FROM orders o WHERE substr(o.created_at,1,10)='${DAY}' AND (o.code='${CODE}' OR o.code='${CODE_BARE}' OR o.order_id IN (SELECT f.order_id FROM fills f WHERE substr(f.traded_at,1,10)='${DAY}' AND (f.code='${CODE}' OR f.code='${CODE_BARE}')) OR (COALESCE(o.signal_id,'') <> '' AND o.signal_id IN (SELECT COALESCE(f.signal_id,'') FROM fills f WHERE substr(f.traded_at,1,10)='${DAY}' AND (f.code='${CODE}' OR f.code='${CODE_BARE}')))) ${P_ORDERS}"
  # ③ real_positions：该票当前行（qty=0/行不存在=已被按卖出记账）
  write_sql 03_positions "SELECT p.ts_code, COALESCE(p.name,'') AS name, p.qty, p.cost_price, p.amount, p.highest_price, COALESCE(p.strategy,'') AS strategy, COALESCE(p.signal_id,'') AS signal_id, p.updated_at, COALESCE(p.user_id,'') AS ukey FROM real_positions p WHERE (p.ts_code='${CODE}' OR p.ts_code='${CODE_BARE}') ${P_POS}"
  # ④ real_account：账户现金快照（打印时 ukey 掩码）
  write_sql 04_account "SELECT COALESCE(a.user_id,'') AS ukey, a.available_cash, a.frozen_cash, a.total_asset, a.market_value, a.updated_at FROM real_account a ${P_ACC}"
  # ④b 当日该票 fills 按方向聚合：回款/买入额两侧合计 + 价量乘积口径差额（费用腿线索）
  write_sql 05_fills_day "SELECT f.side, COUNT(*) AS n, SUM(f.qty) AS sum_qty, ROUND(SUM(COALESCE(f.amount,0)),2) AS sum_amount, ROUND(SUM(f.price*f.qty),2) AS sum_notional, ROUND(SUM(COALESCE(f.fee,0)),2) AS sum_fee, ROUND(SUM(COALESCE(f.stamp_tax,0)),2) AS sum_stamp FROM fills f WHERE substr(f.traded_at,1,10)='${DAY}' AND (f.code='${CODE}' OR f.code='${CODE_BARE}') ${P_FILLS} GROUP BY f.side"
  # ④c 当日全账户方向合计（Bug D 的现场：side=卖出 回款恒 0）
  write_sql 06_day_total "SELECT f.side, COUNT(*) AS n, SUM(f.qty) AS sum_qty, ROUND(SUM(COALESCE(f.amount,0)),2) AS sum_amount, ROUND(SUM(COALESCE(f.fee,0)),2) AS sum_fee FROM fills f WHERE substr(f.traded_at,1,10)='${DAY}' ${P_FILLS} GROUP BY f.side"
}

build_sql_gw_fills() {
  # ⑤b 网关 fills：柜台/桥回报落库口径（方向在哪一层定下来）。只引用网关库自身的表。
  write_sql 21_gw_fills "SELECT g.id AS gfill_id, COALESCE(g.order_id,'') AS order_id, COALESCE(g.code,'') AS code, g.side, g.price, g.qty, COALESCE(g.amount,0) AS amount, COALESCE(g.traded_at,'') AS traded_at, COALESCE(g.signal_id,'') AS signal_id, COALESCE(g.trade_id,'') AS trade_id FROM fills g WHERE substr(g.traded_at,1,10)='${DAY}' AND (g.code='${CODE}' OR g.code='${CODE_BARE}') ORDER BY g.id DESC LIMIT 60"
}

# ⑤a dispatch（第二轮）：锚点集合由第一轮实际查到的 signal_id / order_id / seq 填入。
# 为什么分两轮：网关库自己也有 orders/fills 表（§QMT-DUAL），若在第一轮就用子查询关联，
# 命中条件会莫名其妙落到网关侧的同名表上，等于用另一本账去筛权威账本 —— 语义不干净。
# 故这里只引用 dispatch 一张表，锚点全部是本地实查出来的字面量（已过字符白名单）。
build_sql_dispatch() {
  local list="" a
  for a in "$@"; do
    [ -n "$a" ] || continue
    case "$a" in *[!A-Za-z0-9_.:|-]*) continue ;; esac   # 白名单外字符一律不进 SQL 字面量
    list="${list:+${list}, }'${a}'"
  done
  local anchor_arms=""
  if [ -n "$list" ]; then
    anchor_arms="COALESCE(d.signal_id,'') IN (${list}) OR COALESCE(d.order_id,'') IN (${list}) OR COALESCE(d.seq,'') IN (${list}) OR "
  fi
  write_sql 20_dispatch "SELECT d.id AS disp_id, COALESCE(d.seq,'') AS seq, COALESCE(d.kind,'') AS kind, COALESCE(d.signal_id,'') AS signal_id, COALESCE(d.code,'') AS code, d.side, d.price, d.qty, COALESCE(d.order_id,'') AS order_id, d.status, COALESCE(d.created_at,'') AS created_at, COALESCE(d.user_id,'') AS ukey FROM dispatch d WHERE (${anchor_arms}((d.code='${CODE}' OR d.code='${CODE_BARE}') AND substr(COALESCE(d.created_at,''),1,10)='${DAY}')) ORDER BY d.id DESC LIMIT 60"
}

# 从第一轮结果里抽锚点（signal_id / order_id / seq 三类，含 pend: 占位委托号）
extract_anchors() {
  {
    body_of "$TMP/out_01_fills.txt"     | LC_ALL=C awk -F'|' '{print $2; print $9}'
    body_of "$TMP/out_02_orders.txt"    | LC_ALL=C awk -F'|' '{print $1; print $2}'
    body_of "$TMP/out_21_gw_fills.txt"  | LC_ALL=C awk -F'|' '{print $2; print $9}'
  } 2>/dev/null | LC_ALL=C grep -Ev "^[[:space:]]*$|^EMPTY$" | LC_ALL=C tr -d '\r' | sort -u
}

# ── 第二轮：⑤a dispatch 权威方向的取数编排（两种模式共用同一判据）──────────────
# 为什么必须分两轮：dispatch 在网关库里，而"这笔成交该对哪条派发行"用的锚点（signal_id /
# order_id / seq）只有等第一轮把 live.orders / live.fills / 网关 fills 查回来才知道。
# 先验地按"日期 + 代码"扫 dispatch 会把同日多笔混成一片（判据退化成猜），所以锚点必须实查。
# 网关库不可达时**显式留空产物**而不是跳过不产出：下游 show_table 与结论段都按"这一方证据
# 缺席"处理，最终判语落到 INCONCLUSIVE —— 这是 §M-8/§N-6 的姿势：拿不到证据就承认拿不到，
# 绝不拿剩下三方凑成一个判词。
# （English: round 2 queries the gateway dispatch table with anchors harvested from round-1
# rows; an unreachable gateway DB is journaled as an empty leg so the verdict degrades to
# INCONCLUSIVE instead of guessing.）
run_round2_dispatch() {
  if [ "$GW_AVAILABLE" != "1" ]; then
    : > "$TMP/out_20_dispatch.txt"; : > "$TMP/err_20_dispatch.txt"
    echo "[warn] 网关库副本不可用 → ⑤ dispatch（权威方向）这一方证据缺席，结论按 INCONCLUSIVE 给出。" >&2
    return 0
  fi
  local anchors
  anchors="$(extract_anchors | LC_ALL=C tr '\n' ' ')"
  build_sql_dispatch $anchors          # 逐词传实参；非法字符在 build_sql_dispatch 侧已被白名单剔除
  if [ "$LOCAL_MODE" = "1" ]; then
    run_sql_local "$GW_COPY" "$TMP/sql/20_dispatch.sql" "$TMP/out_20_dispatch.txt"
    return 0
  fi
  # 远端：同一暂存目录、同一份只读副本，只补跑 20_ 这一条；logscan=0（日志证据第一轮已取，
  # 第二轮再扫一遍会把两份计数混在一起）。
  echo "==> 第二轮（现网）：按第一轮实查锚点查网关 dispatch" >&2
  remote_pull_round 2 0 '20_'
}


# SQL 词法复核：每个 .sql 去掉 dot 命令后必须恰好一条语句且以 select 开头。
audit_sql() {
  local f bad=0 n
  for f in "$TMP"/sql/*.sql; do
    [ -f "$f" ] || continue
    n="$(LC_ALL=C grep -Ev '^\.' "$f" | LC_ALL=C grep -Ec '[^[:space:]]' || true)"
    if [ "$n" != "1" ]; then echo "X SQL 复核失败：$(basename "$f") 含 ${n} 条语句（只允许 1 条）" >&2; bad=1; continue; fi
    if ! LC_ALL=C grep -Ev '^\.' "$f" | LC_ALL=C grep -Eqi '^[[:space:]]*select[[:space:]]'; then
      echo "X SQL 复核失败：$(basename "$f") 首词不是 select" >&2; bad=1
    fi
  done
  # 写库关键字黑名单（词表在此拼出来，避免本文件自身被判定命中）
  local W
  W="$(printf '%s' 'uPdAtE diLeTe iNSert vAcUuM aLtEr rEpLaCe dRoP tRuNcAtE')"
  if LC_ALL=C grep -Eoi "\b($(printf '%s' "$W" | tr ' ' '|'))\b" "$TMP"/sql/*.sql 2>/dev/null | LC_ALL=C grep -v '^$' >/dev/null; then
    echo "X SQL 复核失败：命中写库关键字" >&2; bad=1
  fi
  return $bad
}

# ── 执行一条 SQL（对副本，mode=ro）──────────────────────────────────────────
# 产物名与远端分支完全同源：out_<base>.txt / err_<base>.txt（避免本地 err 落在 .txt.err 上、
# 而判据按 err_<base>.txt 读，于是"查询报错"被当成"没有报错"——那是假绿的一条现成路子）。
run_sql_local() { # $1=库 $2=sql 文件 $3=out 文件（out_xxx.txt）
  local ef
  ef="$(LC_ALL=C basename "$3" .txt | LC_ALL=C sed 's/^out_/err_/')"
  sqlite3 "file:$1?mode=ro" ".read $2" > "$3" 2> "$TMP/$ef"
}

# ── 打印辅助 ───────────────────────────────────────────────────────────────
section() { echo; echo "── $1 ──────────────────────────────────────────────"; }
body_of() { [ -f "$1" ] && LC_ALL=C sed -n '2,$p' "$1" | LC_ALL=C grep -v '^[[:space:]]*$' || true; }
head_of() { [ -f "$1" ] && LC_ALL=C head -1 "$1" || true; }
# out_xxx.txt → err_xxx.txt（本地与远端两分支同名约定）
err_of() { LC_ALL=C basename "$1" .txt | LC_ALL=C sed 's/^out_/err_/'; }
# 某一方查询是否报错（报错=证据不完整，结论必须降级为无法判定，不能当"没查到"）
has_err() { [ -s "$TMP/$(err_of "$1")" ]; }
# 该方方向集合（BUY/SELL 词集；查询失败或无行时为空串）
sides_of() { # $1=out 文件 $2=方向列序号
  [ -f "$1" ] || return 0
  has_err "$1" && return 0
  body_of "$1" | LC_ALL=C awk -F'|' -v c="$2" '{print $c}' | side_norm | sort -u | LC_ALL=C tr '\n' ' '
}

build_usermap() {
  : > "$TMP/usermap"
  : > "$TMP/ukeys"
  local f
  for f in "$TMP"/out_*.txt; do
    [ -f "$f" ] || continue
    LC_ALL=C awk -F'|' 'NR==1{for(i=1;i<=NF;i++) if($i=="ukey") c=i; next} c{if($c!="") print $c}' "$f" >> "$TMP/ukeys"
  done
  sort -u "$TMP/ukeys" 2>/dev/null | while IFS= read -r u; do
    [ -n "$u" ] || continue
    printf '%s|%s\n' "$u" "$(printf '%s' "$u" | sha256sum | cut -c1-12)" >> "$TMP/usermap"
  done
}

show_table() { # $1=输出文件 $2=表名 $3=标题
  local f="$1" tname="$2" title="$3" rows
  section "$title"
  if [ ! -f "$f" ]; then echo "  未取得该方数据（取数环节失败或该库不可达）→ 本方证据缺席，按 UNKNOWN 参与结论"; return; fi
  if has_err "$f"; then
    echo "  表 ${tname} 查询报错（多为老库缺列）：$(LC_ALL=C head -1 "$TMP/$(err_of "$f")")"
    echo "  → 本方证据不完整，结论按 UNKNOWN 处理"
    return
  fi
  rows="$(body_of "$f")"
  if [ -z "$rows" ]; then
    echo "  表 ${tname} 在，但本次条件命中 0 行"
    return
  fi
  printf '%s\n' "$(head_of "$f")"
  printf '%s\n' "$rows" | LC_ALL=C awk -F'|' '
    NR==FNR { if (NF>=2) m[$1]="u:" substr($2,1,12); next }
    {
      out=""
      for (i=1;i<=NF;i++) { v=$i; if (v in m) v=m[v]; out = out (i>1?"|":"") v }
      print out
    }' "$TMP/usermap" /dev/stdin \
    | LC_ALL=C sed -e 's/买入/[BUY]/g' -e 's/卖出/[SELL]/g'
}

side_norm() { # stdin: 方向列 → BUY/SELL/EMPTY
  LC_ALL=C awk '{ if ($0 ~ /买入/) print "BUY"; else if ($0 ~ /卖出/) print "SELL"; else print "EMPTY" }'
}
# 方向词 ASCII 化：日志切片里的中文方向必须先换成 BUY/SELL 再过滤可打印字符——
# 下游的 `tr -cd '[:print:]\n'` 跑在 LC_ALL=C 下，CJK 字节整段不在 [:print:] 集合里，
# 顺序颠倒就会把 "dispatch=卖出" 洗成 "dispatch="（空值看着像"日志里没方向"，是假线索）。
side_tok() { LC_ALL=C sed -e 's/买入/BUY/g' -e 's/卖出/SELL/g'; }
set_has() { printf '%s' "$2" | LC_ALL=C grep -qw "$1"; }
# 日志侧计数是否"确有记录"：UNKNOWN（没提供日志目录 / 远端未回传）绝不能算成有记录——
# `${VAR:-0} != 0` 这种写法会把 UNKNOWN 判成"存在冲突记录"，凭空给一条不存在的线索。
cnt_gt0() { case "${1:-}" in ''|UNKNOWN|*[!0-9]*) return 1 ;; *) [ "$1" != "0" ] ;; esac; }
# 方向集合 → 单一取值（"BUY"/"SELL"）；空集→空串；多值→MIXED（多值一律不给整票判语）
word_of() {
  case "$(printf '%s' "$1" | tr -s ' ' '\n' | LC_ALL=C grep -Ev '^$' | sort -u | wc -l | tr -d ' ')" in
    0) printf '%s' "" ;;
    1) printf '%s' "$1" | tr -s ' ' '\n' | LC_ALL=C grep -Ev '^$' | sort -u | head -1 ;;
    *) printf '%s' "MIXED" ;;
  esac
}

# ── dry-run：只打印 SQL 与远端动作清单 ───────────────────────────────────────
LIVE_SHOW="${LOCAL_LIVE_DB:-${LIVE_DB_REMOTE}（远端）}"
# §FILL-AMEND 取证分两轮：第一轮只能查"锚点已知"的表（live 库 + 网关 fills）；
# ⑤a dispatch 的锚点（signal_id/order_id/seq）必须先由第一轮实查得到，故第二轮再建 SQL。
# 这里的 build 只装配第一轮；dispatch 由 run_round2_dispatch 现场装配。
build_sql_live
build_sql_gw_fills
if [ "$DRY_RUN" = "1" ]; then
  # dry-run 也要打印 ⑤a：用占位锚点出示例 SQL，让审阅者看得见第二轮到底查了什么。
  build_sql_dispatch "ROUND1_ANCHOR_PLACEHOLDER"
  audit_sql || { echo "X SQL 自检未通过，dry-run 也拒绝打印" >&2; exit 6; }
  echo "== DRY-RUN（不连网、不取数、不改任何东西）：日期=${DAY} 代码=${CODE} user=${USER_KEY:-（不限归属）} =="
  echo "--local=${LOCAL_MODE}  live 库=${LIVE_SHOW}  网关库=${GW_DB}"
  echo
  for f in "$TMP"/sql/*.sql; do
    echo "--- $(basename "$f") ---"
    LC_ALL=C grep -Ev '^\.' "$f"
  done
  echo
  echo "--- 远端动作清单（真跑时；全部只读）---"
  echo "  1) BatchMode 探活（ssh/scp 装配同 survey_live_rules.sh，不新增凭据、不用密码认证）"
  echo "  2) 远端 %TEMP%/forensic_fill_<PID> 建暂存目录，把 ${LIVE_DB_REMOTE}${GW_DB:+ 与 ${GW_DB}} 拷成只读副本"
  echo "  3) 第一轮：scp 上传 params.txt + forensic_pull.ps1 + sqlrunner.py + sql/*.sql → 远端取数腿逐条执行"
  echo "     （主腿 ${SQLITE_GZ} -readonly .read；该可执行文件不在位时自动改走同机 python/py -3 + sqlrunner.py，"
  echo "      后者强制 file:...?mode=ro 且只放行单条 select，两腿产物文件名与分隔符完全一致，远端以 EXEC 行自报用了哪条）"
  echo "     → 按显式文件名回传 out_*/err_*（不用通配符）；同时 Select-String 扫 ${GW_LOG_GLOB}，"
  echo "     只回传 本码命中行的 时间戳 + BUY/SELL 方向值 与两类 pattern 的当日条数"
  echo "  4) 第二轮：从第一轮 fills/orders/网关fills 结果里实查锚点（signal_id/order_id），装配 20_dispatch.sql 后"
  echo "     补传到**同一个暂存目录**（副本复用、不重拷，免得两轮读的是两张快照）再跑一次，取回权威方向"
  echo "  5) 本地与远端暂存目录全部删除（trap EXIT；远端只删本脚本自己建的这个前缀目录）"
  echo "DRY-RUN 未改动任何东西"
  exit 0
fi

# ── 取数（两轮，全部只读）────────────────────────────────────────────────────
# 第一轮：live.db 六查（fills/orders/real_positions/real_account/当日聚合）+ 网关 fills。
# 第二轮：拿第一轮实查出的锚点（signal_id/order_id/seq）查网关 dispatch（见 build_sql_dispatch 注释）。
echo "== §FILL-AMEND 成交流水方向取证（只读）：日期=${DAY} 代码=${CODE} user=${USER_KEY:-（不限归属）} =="
echo "   live 库=${LIVE_SHOW}"
echo "   网关库=${GW_DB}"
echo "   网关日志=${GW_LOG_GLOB}"
LIVE_COPY="$TMP/live.db"
GW_COPY="$TMP/gw.db"
# GW_AVAILABLE：本地模式=是否拷到网关库只读副本；远端模式=远端是否报 STAGED gw.db。
# 两种模式同一个变量，判据只写一处（避免"本地能查/远端不能查"两套语义漂移）。
GW_AVAILABLE=0
REMOTE_STAGE=""
DETECT_N="UNKNOWN"; MISMATCH_N="UNKNOWN"
# EXEC_LEG：本轮实际用了哪条只读取数腿（sqlite3 主腿 / Python 兜底腿 / 本地 CLI）。
# 之所以要显式记下来打印：两条腿的只读强度相同，但**用了哪条**决定了证据可否复现，
# 汇报里含糊成"取到了数"是不够的（owner 追问"哪台机器用什么取的"必须答得出）。
EXEC_LEG="UNKNOWN"

audit_sql || { echo "X 第一轮 SQL 只读自检未通过，拒绝取数。" >&2; exit 6; }

# 参数一律走文件（路径/日期/代码/日志通配 + 本轮开关），彻底躲开命令行引号嵌套。
write_params() { # $1=logscan(1|0) $2=本轮只跑的 SQL 前缀（空=全部）
  {
    printf 'stage=%s\n'    "$REMOTE_STAGE"
    printf 'livedb=%s\n'   "$LIVE_DB_REMOTE"
    printf 'gwdb=%s\n'     "$GW_DB"
    printf 'sqlite=%s\n'   "$SQLITE_GZ"
    printf 'logglob=%s\n'  "$GW_LOG_GLOB"
    printf 'day=%s\n'      "$DAY"
    printf 'code=%s\n'     "$CODE_BARE"
    printf 'logscan=%s\n'  "$1"
    printf 'only=%s\n'     "$2"
  } > "$TMP/params.txt"
}

# 本地：只对临时目录里的副本开 mode=ro 查询，绝不查原库
run_local_sql() { # $1=库文件 $2..=sql 文件名模式（相对 sql 目录）
  local db="$1" pat f base
  shift
  for pat in "$@"; do
    for f in "$TMP"/sql/$pat; do
      [ -f "$f" ] || continue
      base="$(basename "$f" .sql)"
      run_sql_local "$db" "$f" "$TMP/out_${base}.txt"
    done
  done
}
# 网关侧根本没查（没拿到网关库）→ 造空结果 + 空 err，下游按"证据缺席"处理，而不是"查到 0 行"
touch_gw_missing() { # $1..=sql 文件名模式
  local pat f base
  for pat in "$@"; do
    for f in "$TMP"/sql/$pat; do
      [ -f "$f" ] || continue
      base="$(basename "$f" .sql)"
      : > "$TMP/out_${base}.txt"; : > "$TMP/err_${base}.txt"
    done
  done
}
# Windows 侧文本噪声归一：CRLF 行尾 + UTF-8 BOM（重定向到文件时按文本写会带上）
normalize_local() {
  local f
  for f in "$TMP"/out_*.txt "$TMP"/err_*.txt "$TMP"/mismatch_hits.txt; do
    [ -f "$f" ] || continue
    LC_ALL=C tr -d '\r' < "$f" | LC_ALL=C sed '1s/^\xEF\xBB\xBF//' > "$f.norm" && mv "$f.norm" "$f"
  done
}
# 远端一轮：上传（params + pull 脚本 + 本轮 SQL）→ 执行 pull 脚本 → 按显式文件名回传。
# 通道与仓库既有脚本完全一致（scp/ssh + 同一把私钥，见 survey_live_rules.sh:44-45），
# 不新增任何凭据、不碰密码认证。
# 回传为什么逐个列文件名而不用通配符：Windows 侧 scp 的通配展开取决于远端默认 shell
# （这台机器是 cmd.exe，不是 sh），拿不到通配结果**看起来就像"查到 0 行"**——那是假线索。
# 产物名在本脚本里是可枚举的（每条 SQL 定长对应 out_/err_ 两个文件），列出来更可靠。
PULL_LOG=""
remote_pull_round() { # $1=轮次标 $2=logscan(1|0) $3=本轮 SQL 前缀（空=全部）
  local tag="$1" logscan="$2" only="$3"
  write_params "$logscan" "$only"
  local files=("$TMP/params.txt" "$TMP/forensic_pull.ps1") f base
  # sqlrunner.py 只在兜底腿用得上，但**每轮都一起上传**：让"远端有没有 python"这件事
  # 由 pull 脚本自报（EXEC 行）决定，而不是靠本地猜——少一个文件就会把"该走兜底腿"
  # 变成"远端报 SQLITE_MISSING"，看上去像工具没装齐，实际是我们没把腿递过去。
  [ -f "$TMP/sqlrunner.py" ] && files+=("$TMP/sqlrunner.py")
  local want=()
  for f in "$TMP"/sql/${only}*.sql; do
    [ -f "$f" ] || continue
    files+=("$f")
    base="$(basename "$f" .sql)"
    want+=("out_${base}.txt" "err_${base}.txt")
  done
  if [ "${#want[@]}" = "0" ]; then
    echo "X 本轮没有可跑的 SQL（only='${only}' 没匹配到 sql/ 下任何文件）——按失败退出。" >&2
    return 2
  fi
  [ "$logscan" = "1" ] && want+=("mismatch_hits.txt")
  if ! $SCP_BIN "${files[@]}" "${SCP_TARGET}:${REMOTE_STAGE}/" >/dev/null 2>&1; then
    echo "X scp 上传（params / pull 脚本 / 本轮 SQL）失败——按失败退出，不猜远端状态。" >&2
    return 1
  fi
  PULL_LOG="$TMP/pull_${tag}.log"
  $SSH_BIN "powershell -NoProfile -ExecutionPolicy Bypass -File ${REMOTE_STAGE}/forensic_pull.ps1" 2>&1 \
    | LC_ALL=C tr -d '\r' | LC_ALL=C tee "$PULL_LOG" \
    | LC_ALL=C grep -E '^(SQLITE_VER |EXEC |STAGED |REUSED |MISSING |STAGE_FAIL |RAN |SKIP |DETECT_N=|MISMATCH_N=|LOGSCAN_SKIPPED|PULL_DONE)' \
    | LC_ALL=C sed 's/^/    /' >&2
  if [ ! -s "$PULL_LOG" ]; then
    echo "X 远端 pull 脚本无任何输出（通道或 powershell 起动失败）——判失败，不出结论。" >&2
    return 1
  fi
  if LC_ALL=C grep -q 'SQLITE_MISSING' "$PULL_LOG"; then
    echo "X 远端两条只读取数腿都不可用（sqlite3='${SQLITE_GZ}'，且 python/py -3 都拿不到 Python 3）——显式失败，绝不静默跳过取数。" >&2
    echo "  可选解法（任一）：① 用 SQLITE_GZ=C:/完整路径/sqlite3.exe 重跑；② 确认现网 Python 在 PATH（qmt_gateway/pydata 用的那个解释器）。" >&2
    return 3
  fi
  if ! LC_ALL=C grep -q 'PULL_DONE' "$PULL_LOG"; then
    echo "X 远端取数未正常收尾（无 PULL_DONE 锚点行）——判失败，不拿半截结果出结论。" >&2
    return 5
  fi
  if LC_ALL=C grep -qE '^(MISSING live.db|STAGE_FAIL live.db)' "$PULL_LOG"; then
    echo "X 现网 live.db 未能拷出（${LIVE_DB_REMOTE}）——没有只读副本就没有取证，按失败退出。" >&2
    return 1
  fi
  local got=0 miss_req=0 miss_opt=0 w
  local miss_list=""
  for w in "${want[@]}"; do
    if $SCP_BIN "${SCP_TARGET}:${REMOTE_STAGE}/${w}" "$TMP/" >/dev/null 2>&1; then got=$((got+1)); continue; fi
    miss_list="${miss_list} ${w}"
    case "$w" in
      out_2*|err_2*) miss_opt=$((miss_opt+1)) ;;   # 网关侧缺项：降级为"证据缺席"
      *)             miss_req=$((miss_req+1)) ;;   # live 侧缺项：证据链断了，不能出结论
    esac
  done
  if [ "$miss_req" != "0" ]; then
    echo "X live 侧结果文件未取回 ${miss_req} 个（本轮共需 ${#want[@]} 个）——缺项就是断链，绝不当成「查到 0 行」。" >&2
    # 失败必须自带原因：本轮实测吃过一次"退 5 但只说缺项"的亏——远端取数腿已换 Python 后，
    # out 缺项既可能是"腿没建文件"，也可能是"腿在词法闸处 die 了"（那时 err 一定有内容）。
    # 只回显缺哪几个文件 + 已取回 err 的首行（查询报错文本，不含凭据），截断到 200 字节。
    echo "  缺项:${miss_list}" >&2
    local ef2
    for ef2 in "$TMP"/err_*.txt; do
      [ -s "$ef2" ] || continue
      echo "  $(LC_ALL=C basename "$ef2")：$(LC_ALL=C head -c 200 "$ef2" | LC_ALL=C tr '\n' ' ')" >&2
    done
    echo "  取数腿自报：$(LC_ALL=C sed -n 's/^EXEC //p' "$PULL_LOG" | LC_ALL=C tail -1)" >&2
    return 5
  fi
  if [ "$miss_opt" != "0" ]; then
    GW_AVAILABLE=0
    touch_gw_missing '2*_*.sql'
    echo "[warn] 网关库侧产物未取回（缺 ${miss_opt} 个文件）→ ⑤/⑤b 记为证据缺席，结论按 INCONCLUSIVE 给出。" >&2
  fi
  normalize_local
  exec_leg_from_log
  return 0
}

# ── Python 只读取数兜底腿（内嵌生成，随本次取证一起销毁）────────────────────────
# 为什么要有这条腿：2026-09-23 21:45 现网首跑 `FORENSIC_EXIT=3`，根因不是权限也不是通道，
# 而是这台广州机器的 PATH 里根本没有 sqlite3.exe（owner 手上才有路径），于是"改判要用的
# 五方证据"卡在取数工具上。而同一台机器本来就在跑 Python（qmt_gateway 与 pydata 都是 Python
# 进程），标准库自带 sqlite3 —— 用**同机现成能力**当只读客户端，不装东西、不新增凭据。
# 只读强度与主腿持平（三道闸，见 sqlrunner.py 内的注释），且产物文件名/分隔符/NULL 口径与
# sqlite3 CLI 完全对齐，下游 awk/判语不需要为第二条腿分叉。
write_py_runner() {
  cat > "$TMP/sqlrunner.py" <<'PYEOF'
# -*- coding: utf-8 -*-
# sqlrunner.py — 由 forensic_fill.sh 现场生成的只读取数兜底腿（不是独立工具，勿单独复用）。
# 只读三闸：
#   1) 连接串强制 file:...?mode=ro + uri=True —— SQLite 层面拒绝任何写；
#   2) 词法复核：去掉以 '.' 开头的 dot 行后必须恰好一条语句、必须以 select 开头、分号只允许
#      一个，写库关键字黑名单再兜一层（防"一条 SELECT 后面偷偷跟一句别的"）；
#   3) 只用 cursor.execute + fetchall，不 executescript、不 commit；异常一律非 0 退出并把
#      原文写进 stderr 文件——上游按 has_err 把该方证据降级为 UNKNOWN，绝不当成"查到 0 行"。
# 输出口径与 sqlite3 CLI 的 `.headers on / .separator "|" / .mode list` 逐字对齐：
#   有行时首行列名、其后每行以 | 连接、NULL 打成空串；0 行时输出零字节（CLI 同口径，见下）。
import sys
import re as _re

# 写库关键字黑名单：整词比对（re 分词），命中即拒。列名 updated_at 是一个词，不会被误伤；
# 而偷偷跟在 SELECT 后面的 delete/drop 会被单独切成词命中。
FORBIDDEN = ('insert', 'update', 'delete', 'drop', 'alter', 'replace', 'vacuum', 'attach', 'pragma')


def die(msg, errf):
    try:
        with open(errf, 'a', encoding='utf-8') as fh:
            fh.write(msg + '\n')
    except Exception:
        sys.stderr.write(msg + '\n')
    sys.exit(1)


def main(argv):
    if len(argv) != 5:
        sys.stderr.write('用法: sqlrunner.py <db> <sql文件> <out文件> <err文件>\n')
        return 1
    db, sqlf, outf, errf = argv[1:5]
    # 产物集合必须与主腿逐字相同：CLI 腿是 `-RedirectStandardOutput out -RedirectStandardError err`
    # / shell `> out 2> err`，**成功与否都会生成两个文件**（成功时 err 是 0 字节）。Python 腿若只在
    # 出错时建 err，则"查询成功"反而缺 err 文件 —— 上游 want 的 out+err 成对检查会把成功判成断链
    # （2026-09-24 08:05 现网首跑实测：7 条查询全 RAN、out 全部取回，只因 6+1 个 err 不存在而退 5）。
    # 所以进 main 就先把两个产物建出来，后续一律"写"而不是"造"。
    try:
        open(outf, 'w', encoding='utf-8').close()
        open(errf, 'w', encoding='utf-8').close()
    except Exception as e:
        sys.stderr.write('创建结果/错误文件失败: %r\n' % (e,))
        return 1
    try:
        raw = open(sqlf, encoding='utf-8-sig').read()
    except Exception as e:
        die('读 SQL 文件失败: %r' % (e,), errf)
    body = [ln for ln in raw.splitlines() if not ln.strip().startswith('.')]
    stmt = ' '.join(body).strip()
    if not stmt:
        die('SQL 文件去掉 dot 命令后为空', errf)
    if stmt.count(';') != 1 or not stmt.endswith(';'):
        die('只允许恰好一条语句（分号计数=%d）' % stmt.count(';'), errf)
    stmt = stmt.rstrip(';').strip()
    if stmt.split(None, 1)[0].lower() != 'select':
        die('首词不是 select，拒绝执行', errf)
    low = stmt.lower()
    # 按"整词"比对而不是子串：本仓库查询里 select 的列名自带 updated_at，子串匹配会把
    # "update" 当成写关键字拒掉，于是兜底腿把持仓/账户两方证据降级成 UNKNOWN，而主腿正常出数
    # ——两腿判语不同源比缺一条腿更糟。词边界与上游 audit_sql 的 \b 口径保持一致。
    for tok in _re.findall(r'[a-z0-9_]+', low):
        if tok in FORBIDDEN:
            die('语句含写库关键字 %s，拒绝执行' % tok, errf)
    import sqlite3
    # 两个产物已在 main 入口建好（空文件）。这里再截断一次 out 只为防御：万一上面的
    # open 语义被改动，也不会把旧内容留在本轮结果里。查询报错时 out 保持 0 字节 + err 非空，
    # 上游 has_err 把这一方降级为 UNKNOWN 继续跑（dispatch 表在旧网关库里可能根本不存在，
    # 那是 §FILL-AMEND 判语 NO_DISPATCH_ROW 的正常输入），而不是整轮断链。
    try:
        open(outf, 'w', encoding='utf-8').close()
    except Exception as e:
        die('创建结果文件失败: %r' % (e,), errf)
    try:
        con = sqlite3.connect('file:%s?mode=ro' % db, uri=True, timeout=10)
    except Exception as e:
        die('以只读方式打开副本失败: %r' % (e,), errf)
    try:
        cur = con.cursor()
        cur.execute(stmt)
        rows = cur.fetchall()
        cols = [d[0] for d in (cur.description or [])]
    except Exception as e:
        die('查询失败: %r' % (e,), errf)
    finally:
        try:
            con.close()
        except Exception:
            pass
    def cell(v):
        return '' if v is None else str(v)
    # 0 行时 CLI 连表头都不打（实测 sqlite3 3.54 `.headers on` + 空结果集 = 零字节输出）。
    # 若这条腿坚持写表头，下游 `[ -s out_xxx ]`（该方是否取到数）在两腿间给出不同答案，
    # 而判语是按"缺项=断链 / 有项=取到数"分支的——口径对齐比好看重要。
    with open(outf, 'w', encoding='utf-8', newline='') as fh:
        if rows:
            fh.write('|'.join(cols) + '\n')
            for r in rows:
                fh.write('|'.join(cell(v) for v in r) + '\n')
    return 0


if __name__ == '__main__':
    sys.exit(main(sys.argv))
PYEOF
}

exec_leg_from_log() { # 从最近一轮 pull 日志里取 EXEC 行（取不到就保留原值，绝不猜成 sqlite）
  local v
  v="$(LC_ALL=C sed -n 's/^EXEC //p' "$PULL_LOG" 2>/dev/null | LC_ALL=C tail -1)"
  [ -n "$v" ] && EXEC_LEG="$v"
}

# ── 远端 pull 脚本（内嵌生成）────────────────────────────────────────────────
# 生成的 .ps1 必须带 UTF-8 BOM 落盘：广州侧是 Windows PowerShell 5.1，无 BOM 的 UTF-8
# .ps1 会被按本地 ANSI 码页解码（§BOM-REPO 同族坑），中文注释会错位甚至吞掉引号。
# 这与 deploy_guangzhou.sh 上传前对 qmt-win/*.ps1 做 ps1_bom 是同一条口径。
# 比对中文方向值时不依赖码页：用 [char] 码位拼装（买入=U+4E70 U+5165 / 卖出=U+5356 U+51FA）
# 后回写 ASCII 的 BUY/SELL，日志切片文件本身也只出 ASCII。
write_pull_ps1() {
  cat > "$TMP/forensic_pull.ps1" <<'PSEOF'
param()
$ErrorActionPreference = 'Continue'
$p = @{}
foreach ($ln in (Get-Content -LiteralPath (Join-Path $PSScriptRoot 'params.txt') -Encoding UTF8)) {
  $i = $ln.IndexOf('='); if ($i -gt 0) { $p[$ln.Substring(0,$i)] = $ln.Substring($i+1) }
}
$stage = $p['stage']
$only  = $p['only']
New-Item -ItemType Directory -Force -Path $stage | Out-Null
function SideTok($v) {
  $buy  = [string]([char]0x4E70 + [char]0x5165)
  $sell = [string]([char]0x5356 + [char]0x51FA)
  if ($v -eq $buy)  { return 'BUY' }
  if ($v -eq $sell) { return 'SELL' }
  return 'OTHER'
}
$script:stageMsgs = @()
function Stage($name, $src) {
  # 两轮共用同一份只读副本：第二轮绝不重新拷贝。重新拷贝等于换一张快照，
  # 两轮的数会对不上，那是自己给自己造"证据冲突"。
  # 状态行**不能**在这里 Write-Output：调用形如 `$haveLive = Stage ...`，函数里所有管道输出
  # 都会被吸进那个变量（变成 "STAGED live.db" + $true 的数组），日志里就一行都不剩——
  # 上游两条判据（grep 'STAGED gw.db' 定 GW_AVAILABLE、grep '^MISSING live.db' 定拷库失败）
  # 于是恒为假：网关证据被无脑降级成"证据缺席"，而现网真拷不出库时那个守卫又完全失明。
  # 2026-09-24 08:05 现网首跑实测（日志只有 RAN/EXEC，没有任何 STAGED 行）锤实。
  # 改成攒进 $script:stageMsgs，由调用点在两次 Stage 之后统一打进行首锚点（可被 grep 白名单命中）。
  $dst = Join-Path $stage $name
  if (Test-Path -LiteralPath $dst) { $script:stageMsgs += ('REUSED ' + $name); return $true }
  if (-not (Test-Path -LiteralPath $src)) { $script:stageMsgs += ('MISSING ' + $name); return $false }
  try {
    Copy-Item -LiteralPath $src -Destination $dst -Force -ErrorAction Stop
    Set-ItemProperty -LiteralPath $dst -Name IsReadOnly -Value $true -ErrorAction SilentlyContinue
    $script:stageMsgs += ('STAGED ' + $name)
    return $true
  } catch { $script:stageMsgs += ('STAGE_FAIL ' + $name); return $false }
}
$ver = ''
try { $ver = (& $p['sqlite'] -version 2>&1 | Select-Object -First 1) } catch { $ver = '' }
Write-Output ('SQLITE_VER ' + $ver)
# 取数两腿，顺序固定且**必须自报走了哪条**（EXEC 行）：sqlite3 CLI 主腿 → 同机 Python 兜底腿。
# 两腿都不在位才打 SQLITE_MISSING 并收尾——上游据此非 0 退出，绝不拿半套证据出结论。
$execMode = ''; $pyExe = ''; $pyPre = @()
if ($ver) { $execMode = 'sqlite' } else {
  foreach ($cand in @('python', 'py')) {
    $pre = @(); if ($cand -eq 'py') { $pre = @('-3') }
    $o = ''
    try { $o = (& $cand ($pre + @('--version')) 2>&1 | Select-Object -First 1) } catch { $o = '' }
    if ("$o" -match 'Python 3') { $execMode = 'python'; $pyExe = $cand; $pyPre = $pre; break }
  }
}
if (-not $execMode) { Write-Output 'SQLITE_MISSING'; exit 0 }
if ($execMode -eq 'sqlite') { Write-Output ('EXEC sqlite|' + $ver) } else { Write-Output ('EXEC python|' + $pyExe) }
$haveLive = Stage 'live.db' $p['livedb']
$haveGw   = Stage 'gw.db'   $p['gwdb']
# 状态行统一在调用点后打（原因见 Stage 函数头的管道输出被赋值吞掉那条）。锚点白名单已含
# STAGED/REUSED/MISSING/STAGE_FAIL，所以 bash 侧能看到、grep 判据不再恒假。
foreach ($m0 in $script:stageMsgs) { Write-Output $m0 }
# 只读通道双保险：副本已 chmod/IsReadOnly，查询侧再带 -readonly（sqlite3 CLI 只读模式）；
# Python 腿由 sqlrunner.py 自己强制 mode=ro + 单条 SELECT 词法闸（强度相同，不是降级）。
# 表名 2* 前缀归网关库，其余归 live 库——两边各有同名 fills（§QMT-DUAL），绝不能混查。
foreach ($q in (Get-ChildItem -LiteralPath $stage -Filter '*.sql' | Sort-Object Name)) {
  if ($only -and -not $q.BaseName.StartsWith($only)) { continue }
  $whichDb = 'live.db'; if ($q.BaseName -like '2*') { $whichDb = 'gw.db' }
  if (($whichDb -eq 'live.db') -and (-not $haveLive)) { Write-Output ('SKIP ' + $q.BaseName); continue }
  if (($whichDb -eq 'gw.db')   -and (-not $haveGw))   { Write-Output ('SKIP ' + $q.BaseName); continue }
  $outf = Join-Path $stage ('out_' + $q.BaseName + '.txt')
  $errf = Join-Path $stage ('err_' + $q.BaseName + '.txt')
  $dbPath = Join-Path $stage $whichDb
  if ($execMode -eq 'sqlite') {
    Start-Process -FilePath $p['sqlite'] `
      -ArgumentList @('-readonly', $dbPath, ('.read ' + $q.FullName)) `
      -NoNewWindow -Wait -RedirectStandardOutput $outf -RedirectStandardError $errf
  } else {
    # out/err 由 runner 自己落盘（名字与主腿完全一致）：python 起不来时两个文件都不会出现，
    # 上游按"结果文件缺项=断链"判失败，而不是"缺文件=查到 0 行"。
    Start-Process -FilePath $pyExe `
      -ArgumentList ($pyPre + @((Join-Path $stage 'sqlrunner.py'), $dbPath, $q.FullName, $outf, $errf)) `
      -NoNewWindow -Wait
  }
  Write-Output ('RAN ' + $q.BaseName)
}
Write-Output ('STAGE_PATH ' + $stage)
if ($p['logscan'] -eq '1') {
  # 日志侧只回传「条数」+「时间戳 + 两个方向值」切片：日志原文可能夹带 token 等字面值，
  # 不整段回传、不落仓库工作树。方向值经 SideTok 变 ASCII 后才进 ascii 编码文件。
  $detection = @()
  try { $detection = @(Select-String -Path $p['logglob'] -Pattern 'side detect failed' -SimpleMatch | Where-Object { $_.Line -like ($p['day'] + '*') }) } catch { $detection = @() }
  $mismatch = @()
  try { $mismatch = @(Select-String -Path $p['logglob'] -Pattern 'trade side mismatch' -SimpleMatch | Where-Object { $_.Line -match ([regex]::Escape($p['code'])) }) } catch { $mismatch = @() }
  Write-Output ('DETECT_N=' + $detection.Count)
  Write-Output ('MISMATCH_N=' + $mismatch.Count)
  $mmFile = Join-Path $stage 'mismatch_hits.txt'
  $mismatch | Select-Object -Last 20 | ForEach-Object {
    $m = [regex]::Match([string]$_.Line, '^(\S+ \S+).*dispatch=(\S+).*inferred=(\S+)')
    if ($m.Success) {
      $m.Groups[1].Value + ' dispatch=' + (SideTok $m.Groups[2].Value) + ' inferred=' + (SideTok $m.Groups[3].Value)
    } else { 'UNPARSED' }
  } | Out-File -FilePath $mmFile -Encoding ascii
  Write-Output 'LOGSCAN_DONE'
} else {
  Write-Output 'LOGSCAN_SKIPPED'
}
Write-Output 'PULL_DONE'
exit 0
PSEOF
  # BOM 前置：PowerShell 5.1 靠 BOM 判定 UTF-8，缺它中文注释按 ANSI 码页解（同 §BOM-REPO）。
  { LC_ALL=C printf '\xEF\xBB\xBF'; cat "$TMP/forensic_pull.ps1"; } > "$TMP/forensic_pull.ps1.bom" \
    && mv "$TMP/forensic_pull.ps1.bom" "$TMP/forensic_pull.ps1"
}

if [ "$LOCAL_MODE" = "1" ]; then
  [ -f "$LOCAL_LIVE_DB" ] || { echo "X 本地 live 库不存在：${LOCAL_LIVE_DB}" >&2; exit 4; }
  cp "$LOCAL_LIVE_DB" "$LIVE_COPY" || { echo "X 拷贝本地库到临时目录失败（连只读副本都建不出来）" >&2; exit 4; }
  chmod u+r "$LIVE_COPY" 2>/dev/null || true
  if [ -n "${GW_DB:-}" ] && [ -f "$GW_DB" ]; then
    cp "$GW_DB" "$GW_COPY" && GW_AVAILABLE=1
  fi
  echo "==> 第一轮（本地副本，mode=ro）：live.db 六查 + 网关 fills" >&2
  EXEC_LEG="local|sqlite3 CLI mode=ro"
  run_local_sql "$LIVE_COPY" '0*.sql'
  if [ "$GW_AVAILABLE" = "1" ]; then run_local_sql "$GW_COPY" '21_*.sql'; else touch_gw_missing '2*_*.sql'; fi
else
  # ══ 远端分支 ══（转义链压到最短：远端**逻辑**全在 write_pull_ps1 生成的单个 .ps1 +
  #    params.txt 里，bash 侧只做编排与计数——§UAT 20260915 / §A7-C 的实录教训都指向
  #    "bash→SSH→PowerShell 多层引号嵌套迟早咬人"。这里只留两句短的内联 powershell：
  #    建暂存目录、删暂存目录，形状与 deploy_guangzhou.sh:148 一致。）
  [ -n "$GZ_IP" ] || { echo "X 现网模式必须显式设置 GZ_IP（本脚本不猜主机地址）" >&2; exit 2; }
  echo "==> 预探测：BatchMode 登录（私钥不可读就立刻失败，绝不挂起）" >&2
  if ! $SSH_BIN "echo ok" >/dev/null 2>&1; then
    echo "X 无法以 BatchMode 登录 ${GZ_USER}@${GZ_IP}：公钥未授权或私钥不可读。本脚本不使用密码认证，直接失败。" >&2
    exit 1
  fi
  echo "    ok - 通道可用" >&2
  # 暂存目录带 PID：同机多人/多次跑互不清对方的副本；删除只在 trap 里按这个全路径做。
  REMOTE_STAGE="C:/Users/${GZ_USER}/AppData/Local/Temp/forensic_fill_$$"
  echo "==> 远端建暂存目录（本脚本自己的子目录，内装本次只读副本；不碰现网原库原文件）" >&2
  $SSH_BIN "powershell -NoProfile -Command \"New-Item -ItemType Directory -Force -Path ${REMOTE_STAGE} | Out-Null; exit 0\"" >/dev/null 2>&1 \
    || { echo "X 远端暂存目录创建失败（${REMOTE_STAGE}）" >&2; exit 1; }
  write_pull_ps1
  write_py_runner
  echo "==> 第一轮（现网只读副本 + sqlite3 -readonly，缺 sqlite3.exe 时自动走同机 Python 只读腿）：live.db 六查 + 网关 fills + 日志侧计数" >&2
  remote_pull_round 1 1 '' || exit $?
  # 网关库这一方到底可用不可用，只认 pull 脚本自报的暂存结果（STAGED gw.db）：
  # 拿"有没有 out_20_dispatch.txt"当判据必错——第一轮根本还没查 dispatch。
  if LC_ALL=C grep -q 'STAGED gw.db' "$PULL_LOG"; then GW_AVAILABLE=1; fi
  DETECT_N="$(LC_ALL=C sed -n 's/^DETECT_N=//p' "$PULL_LOG" | LC_ALL=C tail -1)"
  MISMATCH_N="$(LC_ALL=C sed -n 's/^MISMATCH_N=//p' "$PULL_LOG" | LC_ALL=C tail -1)"
  DETECT_N="${DETECT_N:-UNKNOWN}"; MISMATCH_N="${MISMATCH_N:-UNKNOWN}"
fi

# 第二轮（两种模式共用）：⑤a 网关 dispatch 权威方向。
run_round2_dispatch || exit $?
# 两轮跑完再复核一次：第二轮新装配的 20_dispatch.sql 也要过同一道只读词法闸。
audit_sql || { echo "X SQL 只读自检未通过，拒绝出结论。" >&2; exit 6; }
build_usermap

# ── 五方对照打印 ────────────────────────────────────────────────────────────
show_table "$TMP/out_01_fills.txt"      fills          "① live.db fills —— 该日该票成交流水（本地账本口径）"
show_table "$TMP/out_02_orders.txt"     orders         "② live.db orders —— 同锚点委托行（下单时写下的方向 + 状态）"
show_table "$TMP/out_03_positions.txt"   real_positions "③ live.db real_positions —— 该票当前持仓（卖出该减没减）"
show_table "$TMP/out_04_account.txt"     real_account   "④ live.db real_account —— 账户现金快照（ukey 为 sha256 前 12 位掩码）"
show_table "$TMP/out_05_fills_day.txt"   fills_day      "④b 当日该票 fills 按方向聚合 —— 买入额 vs 卖出回款（预算三本账的输入）"
show_table "$TMP/out_06_day_total.txt"   fills_day_total "④c 当日全账户 fills 按方向聚合"
show_table "$TMP/out_20_dispatch.txt"    dispatch       "⑤ 网关 dispatch 表 —— 派发项方向（本端下单时写下的权威源）"
show_table "$TMP/out_21_gw_fills.txt"    gw_fills       "⑤b 网关 fills 表 —— 柜台/桥回报落库口径（方向在哪一层定下来）"

# ── ⑤c 网关日志：只出条数 + 时间戳 + 方向值 ─────────────────────────────────
section "⑤c 网关日志方向异常（side detect failed / trade side mismatch）"
# 说明：远端模式的计数与切片已在取数阶段由 forensic_pull.ps1 算好（DETECT_N / MISMATCH_N /
# mismatch_hits.txt），这里只做展示；本地模式现场扫 GW_LOG_DIR 下的 *.log。
# 两种模式输出一律只有：条数 + 时间戳 + 方向值。日志原文不回传、不落盘、不打印（防夹带 token 等字面值）。
MISMATCH_SAMPLES="$TMP/mismatch_samples"
: > "$MISMATCH_SAMPLES"
if [ "$LOCAL_MODE" = "1" ]; then
  DETECT_N="UNKNOWN"; MISMATCH_N="UNKNOWN"
  if [ -n "$GW_LOG_LOCAL" ] && [ -d "$GW_LOG_LOCAL" ]; then
    DETECT_N=$(LC_ALL=C grep -h "side detect failed" "$GW_LOG_LOCAL"/*.log 2>/dev/null | LC_ALL=C grep -c "^${DAY}" || true)
    MISMATCH_N=$(LC_ALL=C grep -h "trade side mismatch" "$GW_LOG_LOCAL"/*.log 2>/dev/null | LC_ALL=C grep "$CODE_BARE" | wc -l | tr -d ' ')
    LC_ALL=C grep -h "trade side mismatch" "$GW_LOG_LOCAL"/*.log 2>/dev/null | LC_ALL=C grep "$CODE_BARE" \
      | LC_ALL=C sed -n 's/^\([0-9-]* [0-9:,]*\).*dispatch=\([^ ]*\).*inferred=\([^ ]*\).*/\1 dispatch=\2 inferred=\3/p' \
      | side_tok | LC_ALL=C tr -cd '[:print:]\n' | LC_ALL=C sed -n '1,10p' > "$MISMATCH_SAMPLES"
  else
    echo "  未提供 GW_LOG_DIR（本地模式）→ 日志侧证据 UNKNOWN"
  fi
else
  # 远端切片再切一刀：只留「时间戳 + dispatch= + inferred=」，其余字面（含可能夹带的任何值）一律丢弃
  if [ -f "$TMP/mismatch_hits.txt" ]; then
    LC_ALL=C sed -n 's/^\([0-9-]* [0-9:,]*\).*dispatch=\([^ ]*\).*inferred=\([^ ]*\).*/\1 dispatch=\2 inferred=\3/p' \
      "$TMP/mismatch_hits.txt" | LC_ALL=C tr -cd '[:print:]\n' | LC_ALL=C sed -n '1,20p' > "$MISMATCH_SAMPLES"
    UNPARSED_N=$(LC_ALL=C grep -c '^UNPARSED$' "$TMP/mismatch_hits.txt" || true)
    [ "${UNPARSED_N:-0}" != "0" ] && echo "  注：远端有 ${UNPARSED_N} 条 mismatch 行格式不认识（切不出时间戳/方向值），已丢弃不回显，需人工看该日志。"
  else
    echo "  注：远端未回传 mismatch_hits.txt（无命中或写入失败）→ 明细缺失，仅有计数。"
  fi
fi
echo "  side detect failed（本日内，方向认不出来）：条数=${DETECT_N}"
echo "  trade side mismatch（本码相关，推断与派发项冲突）：条数=${MISMATCH_N}"
if [ -s "$MISMATCH_SAMPLES" ]; then
  echo "  mismatch 明细（只留时间戳 + 方向值）："
  LC_ALL=C sed 's/^/    /' "$MISMATCH_SAMPLES"
fi

# ── 自动结论提示（只提示，不改动；判据保守，宁可说无法判定）─────────────────
section "自动结论提示（任何改判都要人工按 §FILL-AMEND 勘误通道逐笔签字 + 守恒自检）"

F_SIDES="$(sides_of "$TMP/out_01_fills.txt" 4)"
O_SIDES="$(sides_of "$TMP/out_02_orders.txt" 4)"
# dispatch 的 side 空串（历史/运维行）不参与判定，只算"这一方到底给了什么方向"
D_SIDES="$(sides_of "$TMP/out_20_dispatch.txt" 6 | LC_ALL=C tr -s ' ' '\n' | LC_ALL=C grep -Ev '^EMPTY$|^$' | sort -u | LC_ALL=C tr '\n' ' ')"
G_SIDES="$(sides_of "$TMP/out_21_gw_fills.txt" 4)"
F_ROWS="$(body_of "$TMP/out_01_fills.txt" 2>/dev/null | wc -l | tr -d ' ')"
D_ROWS="$(body_of "$TMP/out_20_dispatch.txt" 2>/dev/null | wc -l | tr -d ' ')"
F_ERR="no"; O_ERR="no"; D_ERR="no"
has_err "$TMP/out_01_fills.txt" && F_ERR="yes"
has_err "$TMP/out_02_orders.txt" && O_ERR="yes"
has_err "$TMP/out_20_dispatch.txt" && D_ERR="yes"
GW_TABLE="no"
if [ "$GW_AVAILABLE" = "1" ] && [ -f "$TMP/out_20_dispatch.txt" ] && [ "$D_ERR" = "no" ]; then GW_TABLE="yes"; fi

echo "  fills=[${F_SIDES:-UNKNOWN}]  orders=[${O_SIDES:-UNKNOWN}]  dispatch=[${D_SIDES:-UNKNOWN}]  网关fills=[${G_SIDES:-UNKNOWN}]  fills行数=${F_ROWS:-0} dispatch行数=${D_ROWS:-0}"

if [ "${F_ROWS:-0}" = "0" ]; then
  echo "  >> INCONCLUSIVE：该日该代码在 fills 里没有行（或该表查询失败）——先把日期/代码口径对齐再取一次证，不在这里下结论。"
elif [ "$F_ERR" = "yes" ]; then
  echo "  >> INCONCLUSIVE：fills 这一方查询报错（列缺失/库版本不匹配），证据不完整时不下任何判语。"
elif [ "$GW_TABLE" = "no" ]; then
  echo "  >> INCONCLUSIVE：网关 dispatch 这一方证据缺席（网关库未拷到 / 无 dispatch 表 / 查询报错）。"
  echo "     没有权威派发方向就没有判错记的锚——只能补证据，不能在这里下判断。"
  if [ -n "$O_SIDES" ] && [ "$O_SIDES" != "$F_SIDES" ]; then
    echo "     可提示的一点：orders.side=[${O_SIDES}] 与 fills.side=[${F_SIDES}] 不同向，委托簿可作旁证，但它不是权威源，仍按无法判定处理。"
  fi
elif [ -n "$D_SIDES" ] && { { set_has BUY "$F_SIDES" && set_has SELL "$D_SIDES"; } || { set_has SELL "$F_SIDES" && set_has BUY "$D_SIDES"; }; }; then
  echo "  >> MISLABEL_SUSPECT：fills 方向与网关 dispatch 方向相反 —— 派发项是我们自己下单时写下的物理事实，"
  echo "     按方案 §0 事实10 它是唯一权威；成交行与它背离，即成交侧把方向换掉了。"
  echo "     下一步（人工）：核对该 signal_id 的 orders.side 与券商交割凭证，确认后再走 §FILL-AMEND 勘误通道（不得自动改）。"
elif { set_has BUY "$F_SIDES" || set_has SELL "$F_SIDES"; } && { { set_has BUY "$F_SIDES" && set_has SELL "$G_SIDES"; } || { set_has SELL "$F_SIDES" && set_has BUY "$G_SIDES"; }; }; then
  echo "  >> MISLABEL_SUSPECT：fills 方向与网关 fills 方向相反（网关落库口径与本地账本不一致）——"
  echo "     需人工比对两侧 trade_id/serial，确认是同一笔的两套口径、还是两笔不同回报。"
elif [ "${D_ROWS:-0}" = "0" ]; then
  echo "  >> NO_DISPATCH_ROW：按 signal_id / order_id / seq / 委托号 / 代码等六路都没查到派发项行。"
  echo "     这一条恰恰分不开两种成因（所以才留给人工看凭证）："
  echo "       A) 派发行根本没落盘（独立故障，要另批修派发落盘）；"
  echo "       B) 派发行在，但成交回报带的锚点与派发行对不上（§SIDE-AUTH-2 的三级回落全落空，"
  echo "          于是网关静默采信桥/柜台的枚举推断）。"
  echo "     区分靠上面两条日志计数：本日内 side detect failed=${DETECT_N} 条（方向认不出来过）、"
  echo "     本码 trade side mismatch=${MISMATCH_N} 条（推断与派发项冲突过）。"
  if [ -n "$O_SIDES" ] && [ "$O_SIDES" != "$F_SIDES" ]; then
    echo "     另注：orders.side=[${O_SIDES}] 与 fills.side=[${F_SIDES}] 也相反（委托簿留的是下单方向）——错记落在成交侧的可能更大。"
  fi
elif [ "$(word_of "$O_SIDES")" = "MIXED" ] || [ "$(word_of "$F_SIDES")" = "MIXED" ]; then
  echo "  >> UNDETERMINED：同一方内部方向就不唯一（多笔部成/多归属混在一起），需人工逐笔拆开看，不给整票判语。"
else
  F_W="$(word_of "$F_SIDES")"
  if [ -n "$F_W" ] && [ "$O_SIDES" = "$F_SIDES" ] && { [ -z "$D_SIDES" ] || set_has "$F_W" "$D_SIDES"; } && { [ -z "$G_SIDES" ] || set_has "$F_W" "$G_SIDES"; }; then
    echo "  >> CONSISTENT：fills / orders / dispatch（及可见时的网关 fills）方向一致，本次未发现错记证据。"
    if cnt_gt0 "${DETECT_N:-}" || cnt_gt0 "${MISMATCH_N:-}"; then
      echo "     但日志侧存在方向识别失败/冲突记录，说明这条链路确实猜过方向——本笔一致不等于链路安全。"
    elif [ "${DETECT_N:-}" = "UNKNOWN" ] || [ "${MISMATCH_N:-}" = "UNKNOWN" ]; then
      echo "     注：日志侧计数未取得（本次未给 GW_LOG_DIR，或远端未回传），本笔一致只说明账面对得上，链路是否猜过方向无从判断。"
    fi
  else
    echo "  >> UNDETERMINED：四方证据组合不在保守判据覆盖范围内（含某方 EMPTY 或多值），宁可说无法判定。"
    if [ -n "$O_SIDES" ] && [ -n "$F_SIDES" ] && [ "$O_SIDES" != "$F_SIDES" ]; then
      echo "     已知分歧：orders.side=[${O_SIDES}] 与 fills.side=[${F_SIDES}] 不同向，而 dispatch 未提供反证——"
      echo "     这只说明委托方向与成交方向不一致，可能是废单/改单等正常路径，不自动判成错账。"
    fi
  fi
fi

# 持仓/现金侧证（只摆数字，不硬判）
if [ -f "$TMP/out_03_positions.txt" ] && [ -n "$(body_of "$TMP/out_03_positions.txt")" ]; then
  echo "  持仓侧证：$(body_of "$TMP/out_03_positions.txt" | LC_ALL=C awk -F'|' '{print "qty=" $3 " cost_price=" $4 " updated_at=" $9}' | LC_ALL=C tr '\n' ';')"
else
  echo "  持仓侧证：该票当前无持仓行（若 fills 记的是买入而这里没有行，说明这笔没被按买入加进持仓，与账本自洽不符，值得人工看）"
fi
echo "  现金侧证：见上面 ④ / ④b / ④c 三段数字（卖出回款是否恒 0、当日买入额是否被占满），本脚本不据数字反推方向。"

section "只读自检"
echo "  取数腿：${EXEC_LEG}（两腿只读强度相同；用了哪条决定证据可否复现，故必须如实打印）"
echo "  ok - 全部 SQL 为单条 select（运行前后各复核一次）；副本以 mode=ro 打开；未执行 VACUUM/PRAGMA 写；本地与远端临时文件由 trap 删除"
echo "FORENSIC_FILL_DONE"
exit 0
