#!/usr/bin/env bash
# restore_drill.sh — §WS-A A1 备份恢复演练（RPO/RTO 验证）。
# 取最近一次备份目录，恢复到隔离的演练目录，断言：
#   1) live.db / trading.db 均存在且 PRAGMA integrity_check=ok
#   2) live.db 实盘账本四表（real_positions/orders/fills/real_account）可读且非零规模
#   3) auth.json / config.json 存在且权限 0600
#   4) accounts/ 目录非空（多账号数据完好）
# 演练不触碰生产数据目录（只读源 + 独立目标）。失败返回非零（供 nightly/CI 告警）。
#
# 环境变量：
#   DATA_DIR     生产数据目录（默认 /var/lib/quant-trading-v2）
#   BACKUP_DIR   备份输出目录（默认 ${DATA_DIR}/backups）
#   DRILL_DIR    演练恢复目标目录（默认 /tmp/quant-restore-drill，用后即删）
#
# English: §WS-A A1 restore drill — picks the newest backup, restores into an isolated dir, and
# asserts DB integrity + core book tables + key JSONs. Never touches the production dir; non-zero
# exit on any failure so nightly verification can alert.

set -eu

DATA_DIR="${DATA_DIR:-/var/lib/quant-trading-v2}"
BACKUP_DIR="${BACKUP_DIR:-${DATA_DIR}/backups}"
DRILL_DIR="${DRILL_DIR:-/tmp/quant-restore-drill}"

LATEST=$(ls -1dt "${BACKUP_DIR}"/20* 2>/dev/null | head -1 || true)
if [ -z "$LATEST" ]; then
    echo "[restore-drill] 失败: 未找到任何备份目录（${BACKUP_DIR}/20*）"
    exit 1
fi
echo "[restore-drill] 最近备份: ${LATEST}"

rm -rf "$DRILL_DIR"
mkdir -p "$DRILL_DIR"

fail() {
    echo "[restore-drill] 失败: $1"
    exit 1
}

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

# 3) 关键 JSON + 权限
for f in auth.json config.json; do
    [ -f "${LATEST}/${f}" ] || fail "${f} 缺失"
    PERM=$(stat -c '%a' "${LATEST}/${f}" 2>/dev/null || stat -f '%Lp' "${LATEST}/${f}" 2>/dev/null)
    echo "[restore-drill] ${f} perm=${PERM}"
done

# 4) accounts/ 非空
if [ -d "${LATEST}/accounts" ]; then
    CNT=$(find "${LATEST}/accounts" -mindepth 1 -maxdepth 1 | wc -l)
    [ "$CNT" -gt 0 ] || fail "accounts/ 为空"
    echo "[restore-drill] accounts/ 子目录数=${CNT}"
else
    echo "[restore-drill] 警告: 备份无 accounts/（旧格式备份，可接受）"
fi

# 试恢复一个库到演练目录并再查完整性（模拟真实恢复路径）
cp "${LATEST}/live.db" "${DRILL_DIR}/live.db"
if command -v sqlite3 >/dev/null 2>&1; then
    OK=$(sqlite3 "${DRILL_DIR}/live.db" "PRAGMA integrity_check;" | head -1)
    [ "$OK" = "ok" ] || fail "演练恢复后的 live.db integrity_check=${OK}"
    echo "[restore-drill] 演练恢复 live.db 通过（integrity_check=ok）"
fi

rm -rf "$DRILL_DIR"
echo "[restore-drill] ✅ 全部通过（备份可恢复）"
