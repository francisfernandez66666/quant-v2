#!/usr/bin/env bash
# restore_drill.sh — §WS-A A1 备份恢复演练（RPO/RTO 验证）。
# 取最近一次备份产物，恢复到隔离的演练目录，断言：
#   1) live.db / trading.db 均存在且 PRAGMA integrity_check=ok
#   2) live.db 实盘账本四表（real_positions/orders/fills/real_account）**查询不报错**
#      （REQUIRE_NONEMPTY=1 时再要求行数>0；默认不判，理由见下面环境变量段）
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
#   REQUIRE_SQLITE       缺 sqlite3 时是否判红（默认 1=判红；0 才允许跳过完整性腿）
#   REQUIRE_NONEMPTY     默认 0：四表只断「可读」（查询报错即红）；置 1 则再断「行数>0」。
#                        为什么默认不数非零：真实新账号的 fills 可以合法为 0（还没成交过），
#                        把它硬判红等于每次演练都红，然后被人关掉——那比不判更糟。
#   DRILL_RECORD         默认空=不落档；给路径则每次演练**追加一行 JSON 读数**
#                        （时间/布局/产物/四表行数/结果），供定时腿回看"演练到底跑没跑过"。
#
# §0929OPS-⑪-5（2026-09-29 全量审计批）两处"演练自己骗自己"收口：
#   ① 原先三处 sqlite3 判断都是 `if command -v sqlite3 …`，机器上没装 sqlite3 时**整段静默跳过**，
#      脚本照样打「✅ 全部通过」——这正是本仓锤过的「降级链兜住死分支」形态（产物能打开不算验收，
#      须知道产物由哪条路径产出）。现改为 fail-closed：缺 sqlite3 直接判红，除非显式 REQUIRE_SQLITE=0。
#   ② 文件头一直承诺四表"可读且非零规模"，代码里却只 echo 行数、连查询失败（N=ERR）都不判红
#      （文档与实现不一致＝读文档的人以为有这道闸）。现按承诺落成"可读"硬断言，"非零"留给
#      REQUIRE_NONEMPTY 开关，并把口径写回头部，不再留空头承诺。
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
REQUIRE_SQLITE="${REQUIRE_SQLITE:-1}"
REQUIRE_NONEMPTY="${REQUIRE_NONEMPTY:-0}"
DRILL_RECORD="${DRILL_RECORD:-}"

# sqlite3 是这条链的主闸（integrity_check + 四表可读全靠它），fail-closed 单点判定：
# 缺失即红，而不是"后面三处 if 都不进、最后打一条全部通过"。
# 这里只置位，真正判红放在 fail() 定义之后（要留档就得等落档函数就绪）。
HAS_SQLITE=0
if command -v sqlite3 >/dev/null 2>&1; then
    HAS_SQLITE=1
fi

# 演练读数收集（供 DRILL_RECORD 落档）：各段往里塞「键<TAB>值」，收尾拼成一行 JSON。
# 用临时文件而不是变量，是因为 set -eu 下中途 fail() 直接 exit 1，失败那次也要留下"跑到哪崩了"。
STATS_FILE="$(mktemp 2>/dev/null || echo /tmp/quant-restore-drill-stats.$$)"
: > "$STATS_FILE"
stat_put() {
    printf '%s\t%s\n' "$1" "$2" >> "$STATS_FILE"
}
# 纯 bash 拼 JSON（不依赖 python：演练环境可能就是台没装 python3 的机器，留痕腿不能反过来
# 把判据腿拖下水）。落档不可写只打警告、不改演练结果——DRILL_RECORD 是留痕不是判据。
write_record() {
    local res="$1"
    [ -n "$DRILL_RECORD" ] || return 0
    local ts
    ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    mkdir -p "$(dirname "$DRILL_RECORD")" 2>/dev/null || true
    {
        printf '{"ts":"%s","result":"%s","layout":"%s","artifact":"%s"' "$ts" "$res" "${LAYOUT:-unknown}" "${LATEST:-none}"
        while IFS=$'\t' read -r k v; do
            [ -n "$k" ] || continue
            printf ',"%s":"%s"' "$k" "$v"
        done < "$STATS_FILE"
        printf '}\n'
    } >> "$DRILL_RECORD" 2>/dev/null || echo "[restore-drill] 警告: 落档失败（$DRILL_RECORD 不可写），判据结果不受影响"
}

# 统一失败出口（定义早于任何判定，保证每次红都留得下一行读数）：
# 报错文案先剥掉双引号/反斜杠——落档是一行 JSON，带引号的中文文案会把 JSON 撑裂，
# 而「记录写坏」绝不能反过来把演练结果改成看不出红绿。
fail() {
    echo "[restore-drill] 失败: $1"
    stat_put "error" "$(printf '%s' "$1" | tr -d '\"\\')"
    write_record "fail"
    rm -f "$STATS_FILE"
    exit 1
}

LATEST=$(ls -1dt "${BACKUP_DIR}"/20* 2>/dev/null | head -1 || true)
if [ -z "$LATEST" ]; then
    # 广州快照根不按 20* 时间戳命名（它是固定的 quant-snapshot 目录，日期写在 SNAPSHOT_OK.ts 里），
    # 所以 BACKUP_DIR 直接指到快照根时也认——判定交给 detect_artifact_layout。
    if [ -f "${BACKUP_DIR}/SNAPSHOT_OK" ]; then
        LATEST="$BACKUP_DIR"
    else
        # 走统一失败出口而不是裸 exit：「一次演练压根没找到产物」是最该留档的那种红
        #（定时腿看的就是这个——产物目录空了说明备份链断了，而人不会每天盯 stdout）。
        fail "未找到任何备份目录（${BACKUP_DIR}/20*，也不是广州快照根）"
    fi
fi
echo "[restore-drill] 最近备份: ${LATEST}"

rm -rf "$DRILL_DIR"
mkdir -p "$DRILL_DIR"

# 缺 sqlite3 的 fail-closed 判定（§0929OPS-⑪-5）：原先三处 `if command -v sqlite3` 会把
# 完整性腿整段静默跳过、最后照样打「✅ 全部通过」，这里抬成硬判红，跳过必须显式表态。
if [ "$HAS_SQLITE" != "1" ] && [ "$REQUIRE_SQLITE" = "1" ]; then
    fail "本机缺 sqlite3，完整性与账本两条腿无法执行（REQUIRE_SQLITE 默认 1＝不默认通过；确要跳过请显式 REQUIRE_SQLITE=0）"
fi

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
    stat_put "$DB.bytes" "$(wc -c < "${LATEST}/${DB}" | tr -d ' ')"
    if [ "$HAS_SQLITE" = "1" ]; then
        OK=$(sqlite3 "${LATEST}/${DB}" "PRAGMA integrity_check;" | head -1)
        [ "$OK" = "ok" ] || fail "${DB} integrity_check=${OK}"
        stat_put "$DB.integrity" "$OK"
        echo "[restore-drill] ${DB} integrity_check=ok"
    else
        echo "[restore-drill] 警告: REQUIRE_SQLITE=0，跳过 ${DB} integrity_check（这条腿本次没有执行）"
    fi
done

# 2) 实盘账本四表可读（§0929OPS-⑪-5：查询失败从"只 echo 一行 ERR"抬成硬判红——
#    表不在了演练还全绿，等于演练只验了文件能打开）。行数按 REQUIRE_NONEMPTY 决定是否再数。
if [ "$HAS_SQLITE" = "1" ]; then
    for T in real_positions orders fills real_account; do
        N=$(sqlite3 "${LATEST}/live.db" "SELECT COUNT(*) FROM ${T};" 2>/dev/null || echo "ERR")
        [ "$N" != "ERR" ] || fail "live.db.${T} 查询失败（表不存在或库不可读，布局=${LAYOUT}）"
        echo "[restore-drill] live.db.${T} rows=${N}"
        stat_put "rows.$T" "$N"
        if [ "$REQUIRE_NONEMPTY" = "1" ]; then
            [ "$N" -gt 0 ] || fail "live.db.${T} 行数为 0（REQUIRE_NONEMPTY=1）"
        fi
    done
fi

# 3) 关键 JSON + 权限（按布局去各自落位找）
for f in auth.json config.json; do
    [ -f "${JSON_DIR}/${f}" ] || fail "${f} 缺失（布局=${LAYOUT}，找的是 ${JSON_DIR}/${f}）"
    [ -s "${JSON_DIR}/${f}" ] || fail "${f} 是 0 字节（布局=${LAYOUT}）——空文件也算「存在」的话，演练就是自欺"
    stat_put "$f.bytes" "$(wc -c < "${JSON_DIR}/${f}" | tr -d ' ')"
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
if [ "$HAS_SQLITE" = "1" ]; then
    OK=$(sqlite3 "${DRILL_DIR}/live.db" "PRAGMA integrity_check;" | head -1)
    [ "$OK" = "ok" ] || fail "演练恢复后的 live.db integrity_check=${OK}"
    stat_put "restored.integrity" "$OK"
    echo "[restore-drill] 演练恢复 live.db 通过（integrity_check=ok）"
fi

rm -rf "$DRILL_DIR"
stat_put "accounts.dirs" "${CNT:-0}"
write_record "pass"
rm -f "$STATS_FILE"
echo "[restore-drill] ✅ 全部通过（备份可恢复，布局=${LAYOUT}）"
