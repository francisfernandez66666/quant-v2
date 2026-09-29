#!/bin/bash
# verify_restore.sh — Mac 侧「月度恢复演练」（HARDENING_PLAN_20260915.md §一 验收标准里
# 从 09-15 就欠着的那条：`首次完整 copy 落地后 → restic restore latest + sqlite3 integrity_check`）。
#
# 职责：把 Mac 本地 restic 仓库里的**最近快照真的恢复出来**，然后按三层标准验一遍：
#   ① 完整性：仓库层 restic check + 每个库 PRAGMA integrity_check / foreign_key_check；
#   ② 内容非空：库里有表、表里有行（实盘账本四表）、关键 JSON 可解析且不是 0 字节、
#      accounts/ 里每个账号都有 paper.json；
#   ③ 能恢复：把 live.db 复制进隔离目录后**以读写方式打开、跑一条引擎形态的查询、
#      在一个事务里写一次再回滚** —— 证明它是可用的库，不是一个"stat 存在、integrity 也过、
#      但引擎挂不上去"的僵尸文件。
# 只 stat 文件存在不构成本脚本：本仓当批的主题就是「降级不得报成功」，
# "备份文件在" 与 "备份能救回来" 之间隔着的正是这三层。
#
# 为什么结构断言不在这里重写一遍：产物结构（Mac backup.sh 的时间戳目录 vs 广州
# backup_snapshot.ps1 的快照根 + state/ + SNAPSHOT_OK）的判定与非空断言，权威实现在
# scripts/restore_drill.sh（§P0-B 起它已支持两套布局）。本脚本恢复完直接把那个目录交给它，
# 免得同一套判据在两处分叉——本批反复出现的「一份逻辑两份实现」正是我们要收口的缺陷形态。
#
# 安全：本脚本会接触到 auth.json（口令散列）与 restic 仓库密码。**任何情况下都不打印文件内容**，
#   只打印文件名/字节数/是否可解析；密码只从钥匙串导出到环境变量并 trap unset（同 restic_pull_backup.sh 口径）。
# 隔离：只在 mktemp 出来的临时目录里读写，绝不碰生产数据目录；结束即删。
#
# 用法：
#   deploy/mac/verify_restore.sh                        # 验 restic 最近快照（月度演练）
#   MAX_AGE_DAYS=10 deploy/mac/verify_restore.sh        # 放宽新鲜度阈值
#   RESTORE_DIR=/path/to/backup_20*  deploy/mac/verify_restore.sh   # 跳过 restic，直接验一个已存在的产物目录
#   DRILL_RECORD=/path/drill_record.jsonl ...   # 留档路径（默认 ~/backups/quant/drill_record.jsonl，
#                                               #   置空则不落档；定时腿 com.quant.drill 靠它的ts判"演练死没死"）
#   SNAP_MAX_AGE_HOURS=...                      # 产物**内部** SNAPSHOT_OK.ts 的新鲜度上限。
#                                               #   默认按模式分档（§0929DRILL-B）：目录模式 30、
#                                               #   restic 模式 54（快照里的标记天然比快照老一代 +
#                                               #   拉取时点落后一代，两条都在文件头注释里有现网实录）。
#                                               #   只准调大前一种的口径，别拿它当"演练太吵"的消音阀。
# 退出码：0 全通过；非 0 任一层失败（供 cron/launchd/人工复核，失败会推 ntfy）。
#
# 依赖：restic（brew）、macOS 钥匙串项 quant-restic-repo-pass、sqlite3 CLI（系统自带）、python3。
set -uo pipefail

REPO="${REPO:-$HOME/backups/quant/restic}"
KEYCHAIN_ITEM="${KEYCHAIN_ITEM:-quant-restic-repo-pass}"
MAX_AGE_DAYS="${MAX_AGE_DAYS:-8}"          # 快照超过 8 天视为"备份链断了"，演练直接判红
RESTORE_DIR="${RESTORE_DIR:-}"             # 非空 = 目录模式：不碰 restic，直接验这个产物
ALLOW_EMPTY_LIVE="${ALLOW_EMPTY_LIVE:-0}"  # 显式 1 才允许实盘账本零行（新环境首跑用；默认必红）
# §0929DRILL（2026-09-29）演练留档：默认落到稳定副本同目录的 drill_record.jsonl。
#   为什么留档必须由**外层**也写一行，而不是只靠 restore_drill.sh 内部那条：本脚本的失败面
#   大半发生在交给 restore_drill 之前（钥匙串读不到 / restic check 失败 / restore 出来没有 live.db /
#   快照超龄），那些时刻 drill 压根没被调用 ⇒ 记录本上"最后一次演练"永远是上一次的成功行 ⇒
#   定时腿的新鲜度判据看到的是一片绿，而真实状态是"演练连续失败"。这正是本仓反复锤的
#   「降级/中断不得报成功，且不得连失败痕迹都不留」形态。
DRILL_RECORD="${DRILL_RECORD:-$HOME/backups/quant/drill_record.jsonl}"
# §0929DRILL-B（2026-09-29 首次定时实跑锤实的第二处缺陷）：产物内标记的新鲜度阈值**要分模式**。
#   restore_drill.sh 读的是产物**内部**的 SNAPSHOT_OK.ts，默认 30h。这个阈值在两种模式下含义不同：
#     · 目录模式（RESTORE_DIR=刚拉下来的产物）：30h 是对的——产物是现取现验。
#     · restic 模式（验仓库里最近一份快照）：标记天然比快照还老一代，30h 会**天天判红**。
#       两条叠加的老化，都有现网实录：
#         ① 生成代滞后：夜间快照脚本原先在 restic backup **之后**才写标记，
#            所以每份快照里的标记都是上一夜的（Mac 仓最新快照 e29aff27 生成于 09-28T05:18:45，
#            内部标记 ts=09-27T05:26:35）。已在本批把 backup_snapshot.ps1 的顺序调正，
#            但**仓里已有的历史快照改不回来**，要等修复后的脚本跑出新一代快照、并被拉取腿 copy 回来。
#         ② 拉取时点滞后：广州 04:00 起跑，实际收工时间在 05:20（09-28）~08:19（09-29）之间浮动，
#            而 Mac 拉取腿定在 07:00 —— 07:00 那次经常赶在收工之前，于是异地这一侧整整晚一天。
#            （09-29 07:00 的留痕就是"快照 OK：ts=2026-09-28T05:20:16 age=25h"，随后 08:19 才产出当日份。）
#       因此 restic 模式取 54h＝"一个拉取世代(≤24h) + 一个标记世代(≤24h) + 余量"。
#       这不是把判据调松到永远绿：快照**本身**的时间仍由 MAX_AGE_DAYS=8 硬管，
#       而 ② 那条race由本批新增的第二次拉取时点（10:30）收口；等连续两代快照都由调正后的
#       脚本产出，可以把这里显式降回 SNAP_MAX_AGE_HOURS=30 复紧。
SNAP_MAX_AGE_HOURS="${SNAP_MAX_AGE_HOURS:-$( [ -n "$RESTORE_DIR" ] && echo 30 || echo 54 )}"

NTFY_URL="${NTFY_URL:-https://ntfy.sh}"
NTFY_TOPIC="${NTFY_TOPIC:-5fc177ea37320815462af122b5218849}"
ALERT="${ALERT:-1}"

APP_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
DRILL="$APP_DIR/scripts/restore_drill.sh"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/quant-verify-restore.XXXXXX")" || { echo "mktemp 失败"; exit 1; }
WORK="$(cd "$WORK" && pwd)"
DBROOT=""

cleanup() { [ -n "$WORK" ] && rm -rf "$WORK"; [ -n "${RESTIC_PASSWORD:-}" ] && unset RESTIC_PASSWORD; }
trap cleanup EXIT

log() { echo "[verify-restore] $*"; }
alert() {
    local title="$1" body="$2"
    [ "$ALERT" = "1" ] || return 0
    curl -fs -H "Title: $title" -H "Priority: high" -H "Tags: warning" \
         -d "$body" "$NTFY_URL/$NTFY_TOPIC" >/dev/null 2>&1 || log "ntfy 发送失败（不改变本脚本退出码）"
}
# 留档行：纯 bash 拼 JSON（演练机器上不一定有 python3，留痕腿不能反过来把判据腿拖下水，
# 与 restore_drill.sh write_record 同口径）。文案先剥双引号/反斜杠——一行 JSON 带引号的中文
# 会把整行撑裂，而「记录写坏」绝不能把演练结果改成看不出红绿。落档不可写只警告、不改退出码。
record_drill() {
    local res="$1" stage="$2"
    [ -n "$DRILL_RECORD" ] || return 0
    mkdir -p "$(dirname "$DRILL_RECORD")" 2>/dev/null || true
    printf '{"ts":"%s","result":"%s","layout":"%s","artifact":"%s","stage":"%s","source":"verify_restore"}\n' \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$res" "${LAYOUT:-restic-restore}" "${DBROOT:-none}" \
        "$(printf '%s' "$stage" | tr -d '"\\')" >> "$DRILL_RECORD" 2>/dev/null \
        || log "警告: 演练留档不可写（${DRILL_RECORD}），判据结果不受影响"
}

fail() { log "失败: $*"; record_drill "fail" "$*"; alert "quant 恢复演练失败" "${1:-}" ; exit 1; }

command -v sqlite3 >/dev/null 2>&1 || fail "缺 sqlite3 CLI（完整性/内容断言全靠它，不降级通过）"
[ -f "$DRILL" ] || fail "缺结构断言脚本 ${DRILL}（本脚本把它当唯一实现来复用，缺了就不该自行另写一套）"

# ── 1) 取得一份"最近的"恢复源 ─────────────────────────────────────────────────
if [ -n "$RESTORE_DIR" ]; then
    [ -d "$RESTORE_DIR" ] || fail "RESTORE_DIR 不是目录: $RESTORE_DIR"
    DBROOT="$RESTORE_DIR"
    log "目录模式：直接验 ${DBROOT}（跳过 restic 层）"
else
    command -v restic >/dev/null 2>&1 || fail "restic 未安装"
    [ -d "$REPO" ] || fail "本地仓库不存在: ${REPO}（Mac 拉取链没跑过？）"
    RESTIC_PASSWORD="$(security find-generic-password -a "$USER" -s "$KEYCHAIN_ITEM" -w)" \
        || fail "钥匙串取不到仓库密码 $KEYCHAIN_ITEM"
    export RESTIC_PASSWORD
    # §0929DRILL-A（2026-09-29 首次定时实跑锤实的缺陷）：这里**不能用 --last 1**。
    # 本机 restic 0.19.1 把 --last 判为废弃并把它后面的 "1" 当成快照 ID 前缀去过滤：
    #   $ restic snapshots --json --last 1
    #   Flag --last has been deprecated, use --latest 1
    #   Ignoring "1": no matching ID found for prefix "1"
    #   []                      ← 退出码 0、输出空数组
    # 于是本脚本永远走到「仓库里没有任何快照」这条判红，而仓库里实际有 8 份快照。
    # 这条从 §WS-A 写下来就没真跑过（有脚本无调度同族），今天挂上调度第一次拨就露出来。
    # 改法取"不加版本相关开关"的那条：整份列表拉回来自己按 time 排序取最新——
    # --latest 只在较新版本有、--last 在新版本被语义改写，两个都在外部命令上赌版本，
    # 而排序本来就该由本脚本负责（下面还要用它算年龄，多一步零成本）。
    SNAP_JSON="$(restic -r "$REPO" snapshots --json 2>/dev/null)" \
        || fail "restic snapshots 读取失败（仓库密码不对？仓库损坏？）"
    # short_snapshot_id 是较新 restic 才有的字段，缺失时退回完整 id（restore 两个都认）。
    SNAP_ID="$(printf '%s' "$SNAP_JSON" | python3 -c 'import json,sys
d = json.load(sys.stdin)
d = sorted(d, key=lambda s: s.get("time",""))
print((d[-1].get("short_snapshot_id") or d[-1]["id"]) if d else "")' 2>/dev/null)"
    SNAP_TS="$(printf '%s' "$SNAP_JSON" | python3 -c 'import json,sys
d = json.load(sys.stdin)
d = sorted(d, key=lambda s: s.get("time",""))
print(d[-1]["time"] if d else "")' 2>/dev/null)"
    [ -n "$SNAP_ID" ] || fail "仓库里没有任何快照（$REPO 是空的——没有备份可验，别把演练跑成绿灯）"
    # 新鲜度：文件在≠备份在。三个月前的快照能完美恢复出一个完美的旧账本，演练必须对此判红。
    AGE_DAYS="$(python3 - "$SNAP_TS" <<'PY' 2>/dev/null || echo 9999
import sys, datetime
t = datetime.datetime.strptime(sys.argv[1][:19], "%Y-%m-%dT%H:%M:%S")
print((datetime.datetime.now() - t).total_seconds() / 86400)
PY
)"
    if awk "BEGIN{exit !($AGE_DAYS > $MAX_AGE_DAYS)}"; then
        fail "最新快照已 ${AGE_DAYS%.*} 天（> ${MAX_AGE_DAYS} 天，ts=${SNAP_TS}）——先修备份链再谈恢复"
    fi
    log "最新快照 id=$SNAP_ID age=${AGE_DAYS%.*}d（<= ${MAX_AGE_DAYS}d）"

    # ① 仓库层完整性（和 sqlite 层是两个层面：restic check 验的是 pack/index 本身）
    log "restic check（仓库结构自检）..."
    restic -r "$REPO" check >/dev/null 2>&1 || fail "restic check 失败（仓库自身不可信，后面的断言都无意义）"

    # 真恢复（不是 ls）：整份快照落到隔离目录。不做 --exclude 裁剪——快照里除两个库之外
    # 只有几百 KB 的 state/deploy 参考文件，省不下时间，却会引入"排除模式写错、演练少验一块"的新风险。
    log "restic restore $SNAP_ID -> ${WORK}（整份恢复，可能要几分钟）..."
    restic -r "$REPO" restore "$SNAP_ID" --target "$WORK" >/dev/null 2>&1 || fail "restic restore 失败"
    [ -n "$(ls -A "$WORK" 2>/dev/null)" ] || fail "restore 完成但目标目录为空（假成功形态，必须红）"

    # 恢复出来的树带着源机器的绝对路径（Windows 是 C:/var/lib/quant-snapshot，Mac 是 /var/lib/...），
    # 因此不硬编码层级，按 live.db 定位产物根——找不到 live.db 本身就是 §P0-B 回归。
    LIVE_FOUND="$(find "$WORK" -name live.db -type f 2>/dev/null | head -1)"
    [ -n "$LIVE_FOUND" ] || fail "恢复结果里没有 live.db（实盘账本又不在备份对象里了——§P0-B 回归）"
    DBROOT="$(dirname "$LIVE_FOUND")"
    log "产物根定位: $DBROOT"
fi

# ── 2) 结构 + 内容断言：交给权威实现 scripts/restore_drill.sh ──────────────────
# 两套产物布局（Mac / 广州）、库完整性、四表可读、关键 JSON、accounts 非空、
# 以及"复制出去再查一遍完整性"都在它里面。它非零退出即本脚本非零退出。
log "调用 restore_drill.sh 做结构/内容断言（同一标准，不重写第二份）..."
DRILL_OUT="$(BACKUP_DIR="$DBROOT" DRILL_DIR="$WORK/_drill" ARTIFACT="${ARTIFACT:-auto}" SNAP_MAX_AGE_HOURS="$SNAP_MAX_AGE_HOURS" DRILL_RECORD="$DRILL_RECORD" bash "$DRILL" 2>&1)"
DRILL_RC=$?
printf '%s\n' "$DRILL_OUT"
[ "$DRILL_RC" -eq 0 ] || fail "restore_drill.sh 断言未通过（rc=${DRILL_RC}，见上方输出）"

# ── 3) 本脚本独有的深一项：真的能读写 + 外键自洽 + 配置层级正确 ────────────────
LIVE="$DBROOT/live.db"

# 3a) 外键检查：撕裂/半恢复的库常见形态是 integrity_check 仍 ok、但子表指向不存在的父行。
FK="$(sqlite3 "$LIVE" "PRAGMA foreign_key_check;" 2>/dev/null)"
if [ -n "$FK" ]; then
    # 只报条数与涉及表名，不打印行内容（账本数据本身不外泄到日志/推送）。
    fail "live.db foreign_key_check 有 $(printf '%s\n' "$FK" | wc -l | tr -d ' ') 条违规（表: $(printf '%s\n' "$FK" | awk '{print $3}' | sort -u | tr '\n' ',')）"
fi
log "live.db foreign_key_check 干净"

# 3b) 内容非空（restore_drill 只报行数不判红，这里补上判定，口径见 ALLOW_EMPTY_LIVE）
TOTAL=0
for T in real_positions orders fills real_account; do
    N="$(sqlite3 "$LIVE" "SELECT COUNT(*) FROM $T;" 2>/dev/null || echo ERR)"
    [ "$N" = "ERR" ] && fail "live.db 缺表 ${T}（实盘账本表结构不全——研究库能过、账本救不回来）"
    TOTAL=$((TOTAL + N))
    log "live.db.$T rows=$N"
done
if [ "$TOTAL" -eq 0 ] && [ "$ALLOW_EMPTY_LIVE" != "1" ]; then
    fail "live.db 四表全零行：要么这台机器确实没有实盘账（那用 ALLOW_EMPTY_LIVE=1 显式确认），要么备份对象接错了库。不许静默绿"
fi

# 3c) 「能恢复」= 以读写方式打开并真的写一次（事务内 UPDATE 后 ROLLBACK，不留痕）。
#     只读 integrity_check 过不代表引擎挂得上去：文件属主/只读属性/journal 形态异常都会
#     在第一次写时才炸，而那已经是事故现场。这里在隔离副本上做，绝不碰源产物。
RW_COPY="$WORK/_rw_live.db"
cp "$LIVE" "$RW_COPY" || fail "无法复制 live.db 到隔离目录（权限/磁盘异常）"
RW_OUT="$(sqlite3 "$RW_COPY" "
BEGIN;
UPDATE real_account SET total_asset = total_asset;
ROLLBACK;
SELECT 'WRITABLE';
PRAGMA integrity_check;
" 2>&1)"
printf '%s\n' "$RW_OUT" | grep -q '^WRITABLE$' || fail "隔离副本里的 live.db 不可读写（恢复产物只能看不能用）: $(printf '%s' "$RW_OUT" | head -3 | tr '\n' ' ')"
printf '%s\n' "$RW_OUT" | tail -1 | grep -q '^ok$' || fail "写入往返后 integrity_check 不再是 ok（写一次就坏 = 快照本身不可信）"
# 引擎形态查询：实盘持仓按 (ts_code,user_id) 主键读，列名口径见 internal/store/store.go:365-377
sqlite3 "$LIVE" "SELECT ts_code, qty, cost_price FROM real_positions ORDER BY ts_code LIMIT 1;" >/dev/null 2>&1 \
    || fail "live.db 按引擎口径（ts_code/qty/cost_price）读 real_positions 失败——库能开不等于账本能查"
log "live.db 读写往返 + 引擎口径查询通过（真的能恢复，不只是文件在）"

# 3d) 配置层级：恢复回来的 config.json 必须是 Go wrapper 认的 {rules,d1} 形态
#     （§M7a 根级 qmt 死键教训：层级错了配置照样"存在且可解析"，但引擎静默全忽略）。
CFG=""
for cand in "$DBROOT/config.json" "$DBROOT/state/config.json"; do
    [ -f "$cand" ] && CFG="$cand" && break
done
if [ -n "$CFG" ]; then
    python3 - "$CFG" <<'PY' || fail "config.json 根键不是 Go wrapper 的 rules/d1 形态（§M7a 层级漂移）"
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
bad = set(d) - {"rules", "d1"}
print("[verify-restore] config.json 根键=" + ",".join(sorted(d)) + ("（含死键:" + ",".join(sorted(bad)) + "）" if bad else ""))
sys.exit(1 if bad else 0)
PY
else
    log "警告: 产物里没有 config.json（Mac 布局在根、广州布局在 state/；两处都没有就是漏备）"
fi

# 深腿（restic check / foreign_key / 读写往返 / 引擎口径查询 / config 层级）只有本脚本知道结果，
# restore_drill 那条记录看不到它们——所以成功也要由外层补一行，否则"浅腿绿、深腿从没跑过"
# 在留档里与全绿一模一样。
record_drill "ok" "deep-legs-pass"
log "✅ 恢复演练通过（完整性 + 内容非空 + 可读写恢复，产物根=${DBROOT}，留档=${DRILL_RECORD}）"
exit 0
