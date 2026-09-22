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
# 退出码：0 全通过；非 0 任一层失败（供 cron/launchd/人工复核，失败会推 ntfy）。
#
# 依赖：restic（brew）、macOS 钥匙串项 quant-restic-repo-pass、sqlite3 CLI（系统自带）、python3。
set -uo pipefail

REPO="${REPO:-$HOME/backups/quant/restic}"
KEYCHAIN_ITEM="${KEYCHAIN_ITEM:-quant-restic-repo-pass}"
MAX_AGE_DAYS="${MAX_AGE_DAYS:-8}"          # 快照超过 8 天视为"备份链断了"，演练直接判红
RESTORE_DIR="${RESTORE_DIR:-}"             # 非空 = 目录模式：不碰 restic，直接验这个产物
ALLOW_EMPTY_LIVE="${ALLOW_EMPTY_LIVE:-0}"  # 显式 1 才允许实盘账本零行（新环境首跑用；默认必红）
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
fail() { log "失败: $*"; alert "quant 恢复演练失败" "${1:-}" ; exit 1; }

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
    SNAP_JSON="$(restic -r "$REPO" snapshots --json --last 1 2>/dev/null)" \
        || fail "restic snapshots 读取失败（仓库密码不对？仓库损坏？）"
    # short_snapshot_id 是较新 restic 才有的字段，缺失时退回完整 id（restore 两个都认）。
    SNAP_ID="$(printf '%s' "$SNAP_JSON" | python3 -c 'import json,sys
d = json.load(sys.stdin)
print((d[-1].get("short_snapshot_id") or d[-1]["id"]) if d else "")' 2>/dev/null)"
    SNAP_TS="$(printf '%s' "$SNAP_JSON" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d[-1]["time"] if d else "")' 2>/dev/null)"
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
DRILL_OUT="$(BACKUP_DIR="$DBROOT" DRILL_DIR="$WORK/_drill" ARTIFACT="${ARTIFACT:-auto}" bash "$DRILL" 2>&1)"
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

log "✅ 恢复演练通过（完整性 + 内容非空 + 可读写恢复，产物根=${DBROOT}）"
exit 0
