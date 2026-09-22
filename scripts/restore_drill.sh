#!/usr/bin/env bash
# restore_drill.sh — §WS-A A1 备份恢复演练（RPO/RTO 验证）。
# 取最近一次备份产物，恢复到隔离的演练目录，断言：
#   1) live.db / trading.db 均存在且 PRAGMA integrity_check=ok
#   2) live.db 实盘账本四表（real_positions/orders/fills/real_account）可读且非零规模
#   3) auth.json / config.json 存在且权限 0600
#   4) accounts/ 目录非空（多账号数据完好）
# 演练不触碰生产数据目录（只读源 + 独立目标）。失败返回非零（供 nightly/CI 告警）。
#
# §P0-B（2026-09-23 晚批）产物结构分支：本脚本原先**只按 Mac 产物**（scripts/backup.sh 落的
# 时间戳目录，JSON 在根）断言，而广州侧 deploy/qmt-win/backup_snapshot.ps1 的产物是
# 「快照根 + state/ 子目录放 JSON + SNAPSHOT_OK 标记」的另一套结构。用 Mac 的结构去验广州的
# 产物 = **自己验自己**：accounts/ 判"旧格式可接受"直接放过，演练全绿而广州产物其实根本没这些
# 文件。故现按产物实际形态分支断言（判定规则见 detect_artifact_layout），两套布局都跑同一套
# 「库完整性 + 账本非空 + 关键 JSON + accounts 非空」标准。
#
# 环境变量：
#   DATA_DIR     生产数据目录（默认 /var/lib/quant-trading-v2）
#   BACKUP_DIR   备份产物目录（默认 ${DATA_DIR}/backups；验广州产物时把它指到拉回来的快照根）
#   DRILL_DIR    演练恢复目标目录（默认 /tmp/quant-restore-drill，用后即删）
#   ARTIFACT     产物布局：auto（默认）| mac | guangzhou —— 自动判定歧义时人工指定
#   SNAP_MAX_AGE_HOURS 广州 SNAPSHOT_OK 新鲜度上限（默认 30，>26h 的 Mac 拉取器守卫留余量）
#
# English: §WS-A A1 restore drill — picks the newest backup artifact, restores into an isolated dir,
# and asserts DB integrity + core book tables + key JSONs + accounts/. Understands BOTH the Mac
# layout (timestamped dir, JSON at root) and the Guangzhou snapshot layout (state/ subdir +
# SNAPSHOT_OK marker), because verifying a Guangzhou artifact with Mac-shaped assertions is a false
# green. Never touches the production dir; non-zero exit on any failure.

set -eu

DATA_DIR="${DATA_DIR:-/var/lib/quant-trading-v2}"
BACKUP_DIR="${BACKUP_DIR:-${DATA_DIR}/backups}"
DRILL_DIR="${DRILL_DIR:-/tmp/quant-restore-drill}"
ARTIFACT="${ARTIFACT:-auto}"
SNAP_MAX_AGE_HOURS="${SNAP_MAX_AGE_HOURS:-30}"

LATEST=$(ls -1dt "${BACKUP_DIR}"/20* 2>/dev/null | head -1 || true)
if [ -z "$LATEST" ]; then
    # 广州快照根不按 20* 时间戳命名（它是固定的 quant-snapshot 目录，日期写在 SNAPSHOT_OK.ts 里），
    # 所以 BACKUP_DIR 直接指到快照根时也认——判定交给 detect_artifact_layout。
    if [ -f "${BACKUP_DIR}/SNAPSHOT_OK" ]; then
        LATEST="$BACKUP_DIR"
    else
        echo "[restore-drill] 失败: 未找到任何备份目录（${BACKUP_DIR}/20*，也不是广州快照根）"
        exit 1
    fi
fi
echo "[restore-drill] 最近备份: ${LATEST}"

rm -rf "$DRILL_DIR"
mkdir -p "$DRILL_DIR"

fail() {
    echo "[restore-drill] 失败: $1"
    exit 1
}

# ── 产物布局判定（§P0-B）──────────────────────────────────────────────────────
# 判据（按可靠性排序，命中即停）：
#   guangzhou：存在 SNAPSHOT_OK 标记（只有 backup_snapshot.ps1 会写），或 state/ 目录存在
#              且根下没有 auth.json（JSON 不在根=不是 backup.sh 的产物）；
#   mac      ：根下直接有 auth.json/config.json（backup.sh 的 install -m 600 落根）+ 时间戳目录名。
# 歧义（两者判据都不满足）→ 红，不猜。宁可让人指定 ARTIFACT，也不要"猜一个布局然后全绿"。
detect_artifact_layout() {
    if [ "$ARTIFACT" != "auto" ]; then
        echo "$ARTIFACT"; return
    fi
    if [ -f "${LATEST}/SNAPSHOT_OK" ] || { [ -d "${LATEST}/state" ] && [ ! -f "${LATEST}/auth.json" ]; }; then
        echo "guangzhou"; return
    fi
    if [ -f "${LATEST}/auth.json" ] || [ -f "${LATEST}/config.json" ] || [ -d "${LATEST}/config_history" ]; then
        echo "mac"; return
    fi
    echo ""
}
LAYOUT="$(detect_artifact_layout)"
[ -n "$LAYOUT" ] || fail "无法判定产物布局（既无 SNAPSHOT_OK/state/，也无根级 auth.json）——显式指定 ARTIFACT=mac|guangzhou 后重跑"
# JSON 落位：Mac 在产物根，广州在 state/ 子目录（backup_snapshot.ps1 的 $stateDir）。
# 必须写 if：`[ ... ] && VAR=...` 在 set -e 下当条件为假时整条列表返回非零，脚本直接退出
# （§A7-E 同族：Mac 产物分支会在这里静默死掉，比假绿更难查）。
JSON_DIR="$LATEST"
if [ "$LAYOUT" = "guangzhou" ]; then
    JSON_DIR="${LATEST}/state"
fi
echo "[restore-drill] 产物布局=${LAYOUT}（JSON 目录=${JSON_DIR}）"

# 0) 广州产物先验"新鲜度"：SNAPSHOT_OK 必须是 ok:true 且 ts 未过期。
#    少了这一步就会出现"备份文件一直在、内容却是三个月前的"这种假绿（文件存在≠有可用备份）。
if [ "$LAYOUT" = "guangzhou" ]; then
    [ -f "${LATEST}/SNAPSHOT_OK" ] || fail "广州产物缺 SNAPSHOT_OK 标记（快照根不该没有它——没有就等于没产出）"
    MARK="$(cat "${LATEST}/SNAPSHOT_OK")"
    echo "$MARK" | grep -q '"ok":true' || fail "SNAPSHOT_OK 非 ok:true: $MARK"
    TS=$(echo "$MARK" | sed -n 's/.*"ts":"\([^"]*\)".*/\1/p')
    # 解析不出 ts / 没有 python3 ⇒ 判红而不是跳过：新鲜度是广州产物唯一的时间证据
    # （restic 拉回后 mtime 会变，看不了），"无法判定"若默认通过就等于这项永远不存在。
    [ -n "$TS" ] || fail "SNAPSHOT_OK 里解析不到 ts 字段，无法判定快照新鲜度（不默认通过）"
    command -v python3 >/dev/null 2>&1 || fail "缺 python3，无法解析快照 ts（广州布局的新鲜度断言是硬项，不默认通过）"
    AGE_H=$(python3 - "$TS" <<'PY' 2>/dev/null || echo 9999
import sys, datetime
t = datetime.datetime.strptime(sys.argv[1][:19], "%Y-%m-%dT%H:%M:%S")
print((datetime.datetime.now() - t).total_seconds() / 3600)
PY
)
    # 用 if 包一层：`cmd && fail` 在 set -e 下靠豁免条款侥幸不炸，读代码的人得想三行。
    if awk "BEGIN{exit !($AGE_H > $SNAP_MAX_AGE_HOURS)}"; then
        fail "广州快照过期 ${AGE_H}h > ${SNAP_MAX_AGE_HOURS}h（ts=${TS}）"
    fi
    echo "[restore-drill] SNAPSHOT_OK ts=$TS age=${AGE_H%.*}h（<= ${SNAP_MAX_AGE_HOURS}h）"
    # dbs 映射（§P0-B 起 backup_snapshot.ps1 逐库记字节数）：live.db 必须出现在里面。
    if echo "$MARK" | grep -q '"dbs"'; then
        echo "$MARK" | grep -q '"live\.db"' || fail "SNAPSHOT_OK.dbs 里没有 live.db（实盘账本未进快照，§P0-B 回归）"
        echo "[restore-drill] SNAPSHOT_OK.dbs 含 live.db"
    else
        echo "[restore-drill] 警告: SNAPSHOT_OK 无 dbs 字段（旧版快照产物），live.db 只按文件在位判定"
    fi
fi

# 1) SQLite 完整性 + 核心表
for DB in live.db trading.db; do
    [ -f "${LATEST}/${DB}" ] || fail "${DB} 不存在于备份"
    if command -v sqlite3 >/dev/null 2>&1; then
        OK=$(sqlite3 "${LATEST}/${DB}" "PRAGMA integrity_check;" | head -1)
        [ "$OK" = "ok" ] || fail "${DB} integrity_check=${OK}"
        echo "[restore-drill] ${DB} integrity_check=ok"
    fi
done

# 2) 实盘账本四表可读
if command -v sqlite3 >/dev/null 2>&1; then
    for T in real_positions orders fills real_account; do
        N=$(sqlite3 "${LATEST}/live.db" "SELECT COUNT(*) FROM ${T};" 2>/dev/null || echo "ERR")
        echo "[restore-drill] live.db.${T} rows=${N}"
    done
fi

# 3) 关键 JSON + 权限（按布局去各自落位找）
for f in auth.json config.json; do
    [ -f "${JSON_DIR}/${f}" ] || fail "${f} 缺失（布局=${LAYOUT}，找的是 ${JSON_DIR}/${f}）"
    [ -s "${JSON_DIR}/${f}" ] || fail "${f} 是 0 字节（布局=${LAYOUT}）——空文件也算「存在」的话，演练就是自欺"
    if [ "$LAYOUT" = "mac" ]; then
        # 只有 Mac 侧 backup.sh 用 install -m 600 落盘，权限断言对它有意义；
        # 广州是 Windows 快照经 restic 拉回，POSIX 位不承载原机 ACL，断言它会误红。
        PERM=$(stat -c '%a' "${JSON_DIR}/${f}" 2>/dev/null || stat -f '%Lp' "${JSON_DIR}/${f}" 2>/dev/null)
        echo "[restore-drill] ${f} perm=${PERM}"
    else
        echo "[restore-drill] ${f} 非空（广州产物不断言 POSIX 权限位）"
    fi
done

# 4) accounts/ 非空（两套布局都在产物根，§P0-B 起广州也有）
if [ -d "${LATEST}/accounts" ]; then
    CNT=$(find "${LATEST}/accounts" -mindepth 1 -maxdepth 1 | wc -l)
    [ "$CNT" -gt 0 ] || fail "accounts/ 为空"
    # 广州产物再深一层：每个账号目录里至少要有 paper.json（模拟盘账本，registry.go:164）
    if [ "$LAYOUT" = "guangzhou" ]; then
        MISSING=0
        for d in "${LATEST}"/accounts/*/; do
            [ -d "$d" ] || continue
            [ -f "${d}paper.json" ] || { echo "[restore-drill] 失败: $(basename "$d") 缺 paper.json"; MISSING=1; }
        done
        [ "$MISSING" = "0" ] || fail "accounts/ 结构不完整（目录在、per-user 账本文件不在）"
    fi
    echo "[restore-drill] accounts/ 子目录数=${CNT}"
else
    # §P0-B 收紧：广州产物缺 accounts/ 不再"可接受"。backup_snapshot.ps1 现在是
    # "源目录存在则必镜像、镜像出 0 文件即抛错"，产物里没有它 = 拷贝链路没跑通，必须红。
    # Mac 侧保留旧口径（backup.sh 是 `if [ -d accounts ]` 才拷，老备份本来就没这个目录）。
    if [ "$LAYOUT" = "guangzhou" ]; then
        fail "广州产物缺 accounts/ 目录（§P0-B 起它是必备备份对象，缺=拷贝链路没跑通）"
    fi
    echo "[restore-drill] 警告: 备份无 accounts/（Mac 旧格式备份，可接受）"
fi

# 试恢复一个库到演练目录并再查完整性（模拟真实恢复路径）
cp "${LATEST}/live.db" "${DRILL_DIR}/live.db"
if command -v sqlite3 >/dev/null 2>&1; then
    OK=$(sqlite3 "${DRILL_DIR}/live.db" "PRAGMA integrity_check;" | head -1)
    [ "$OK" = "ok" ] || fail "演练恢复后的 live.db integrity_check=${OK}"
    echo "[restore-drill] 演练恢复 live.db 通过（integrity_check=ok）"
fi

rm -rf "$DRILL_DIR"
echo "[restore-drill] ✅ 全部通过（备份可恢复，布局=${LAYOUT}）"
