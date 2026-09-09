#!/usr/bin/env bash
# backup.sh — 每日备份（§GAP7.2）：
# SQLite 研究库（trading.db，在线 .backup 一致性快照）+ auth.json + config.json
# 备份到 $BACKUP_DIR，保留最近 $KEEP_DAYS 天，超期自动清理。
#
# 安装（服务器 root crontab，每日收盘后）：
#   30 15 * * 1-5 /var/lib/quant-trading-v2/scripts/backup.sh >> /var/log/quant-backup.log 2>&1
#
# 环境变量：
#   DATA_DIR    数据目录（默认 /var/lib/quant-trading-v2）
#   BACKUP_DIR  备份输出目录（默认 ${DATA_DIR}/backups）
#   KEEP_DAYS   保留天数（默认 7）
#   OFFSITE_DIR 异地副本目录（可选；如挂载的 OSS/NFS 路径，存在则同步一份）

set -eu

DATA_DIR="${DATA_DIR:-/var/lib/quant-trading-v2}"
BACKUP_DIR="${BACKUP_DIR:-${DATA_DIR}/backups}"
KEEP_DAYS="${KEEP_DAYS:-7}"
OFFSITE_DIR="${OFFSITE_DIR:-}"

STAMP=$(date '+%Y%m%d_%H%M%S')
DEST="${BACKUP_DIR}/${STAMP}"
mkdir -p "$DEST"

echo "[$(date '+%F %T')] 备份开始 → $DEST"

# 1) SQLite 在线一致性备份（不锁库不停服）
# §WS-A A1：实盘账本 live.db（持仓/委托/成交/资产）与夜间研究库 trading.db 一并纳入——
# 此前仅备份 trading.db，live.db 坏盘=实盘账本全丢（§R7 审计 A1，P0）。
for DB in trading.db live.db; do
    DB_PATH="${DATA_DIR}/${DB}"
    if [ -f "$DB_PATH" ]; then
        if command -v sqlite3 >/dev/null 2>&1; then
            sqlite3 "$DB_PATH" ".backup '${DEST}/${DB}'"
        else
            # 无 sqlite3 时：仅当存在一致快照路径才拷贝（含 -wal/-shm），否则警告跳过
            echo "警告: 未找到 sqlite3，${DB} 退化为文件拷贝（可能含未 checkpoint 的 WAL 数据）"
            cp "$DB_PATH" "${DEST}/${DB}"
            [ -f "${DB_PATH}-wal" ] && cp "${DB_PATH}-wal" "${DEST}/${DB}-wal"
            [ -f "${DB_PATH}-shm" ] && cp "${DB_PATH}-shm" "${DEST}/${DB}-shm"
        fi
    else
        echo "警告: 未找到 ${DB_PATH}，跳过库备份"
    fi
done

# 2) 关键 JSON（auth.json 权限 0600，拷贝后保持）
for f in auth.json config.json; do
    [ -f "${DATA_DIR}/${f}" ] && install -m 600 "${DATA_DIR}/${f}" "${DEST}/${f}"
done

# 2b) §WS-A A1 账号数据目录（每账号 qmt_m8.json / paper.json / rules / 自选等）
if [ -d "${DATA_DIR}/accounts" ]; then
    cp -r "${DATA_DIR}/accounts" "${DEST}/accounts"
fi

# 3) 应用战法规则（审批产物，丢失需重跑寻优+审批）
for f in applied_rules.json applied_factors.json applied_patterns.json grayscale_rules.json; do
    [ -f "${DATA_DIR}/${f}" ] && cp "${DATA_DIR}/${f}" "${DEST}/"
done

# 3b) §WS-A 配置历史（WS-K 配置治理的版本快照，回滚依赖）
if [ -d "${DATA_DIR}/config_history" ]; then
    cp -r "${DATA_DIR}/config_history" "${DEST}/config_history"
fi

# 4) 清理过期备份
find "$BACKUP_DIR" -maxdepth 1 -type d -name '20*' -mtime "+$KEEP_DAYS" -exec rm -rf {} \; 2>/dev/null || true

# 5) 异地副本（可选）
if [ -n "$OFFSITE_DIR" ] && [ -d "$OFFSITE_DIR" ]; then
    rsync -a --delete "${BACKUP_DIR}/" "${OFFSITE_DIR}/"
    echo "异地副本已同步 → $OFFSITE_DIR"
fi

# 5b) §WS-J 首尔热备同步（可选）：每晚盘后把最新备份 tar 推到首尔，并校验完整性。
# 需配置 SEOUL_SYNC=1 + SEOUL_HOST/SEOUL_USER/SEOUL_PATH（经中继 ssh，主节点不可达不影响本机备份）。
# English: §WS-J Seoul hot-standby sync (optional) — pushes the newest backup tar to Seoul nightly
# via ssh relay and verifies integrity. A Seoul outage never fails this local backup.
if [ "${SEOUL_SYNC:-0}" = "1" ]; then
    SEOUL_USER="${SEOUL_USER:-root}"
    SEOUL_HOST="${SEOUL_HOST:?SEOUL_SYNC=1 时必须设置 SEOUL_HOST（首尔 IP）}"
    SEOUL_PATH="${SEOUL_PATH:-/var/lib/quant-trading-v2/backups}"
    SSH_OPTS="-o ConnectTimeout=30 -o StrictHostKeyChecking=accept-new"
    if ssh $SSH_OPTS "${SEOUL_USER}@${SEOUL_HOST}" "mkdir -p ${SEOUL_PATH}" 2>/dev/null; then
        # 只同步最近 KEEP_DAYS 内的备份目录（含 db 快照 + auth + accounts + config_history）
        if rsync -a --delete -e "ssh $SSH_OPTS" "${BACKUP_DIR}/" "${SEOUL_USER}@${SEOUL_HOST}:${SEOUL_PATH}/"; then
            # 完整性校验：远端 sqlite integrity_check（可用 sqlite3 时）
            if ssh $SSH_OPTS "${SEOUL_USER}@${SEOUL_HOST}" \
                "ls -t ${SEOUL_PATH}/20*/trading.db 2>/dev/null | head -1 | xargs -r sqlite3 2>/dev/null 'PRAGMA integrity_check;' | grep -q ok"; then
                echo "[$(date '+%F %T')] 首尔同步完成且 integrity_check ok → ${SEOUL_HOST}:${SEOUL_PATH}"
            else
                echo "[$(date '+%F %T')] 首尔同步完成（integrity_check 未验证或远端无 sqlite3）"
            fi
        else
            echo "[$(date '+%F %T')] ⚠ 首尔同步失败——不影响本机备份，请人工核查" >&2
        fi
    else
        echo "[$(date '+%F %T')] ⚠ 首尔不可达，跳过同步（仅告警）" >&2
    fi
fi


SIZE=$(du -sh "$DEST" | cut -f1)
echo "[$(date '+%F %T')] 备份完成: $DEST ($SIZE)"

# 6) §WS-A A1 恢复演练（可选，RUN_DRILL=1 时执行）：验证最近备份可真实恢复。
# 默认关闭——日常备份不开演练可省 io；crontab 中每周一次带 RUN_DRILL=1 全量演练。
# English: §WS-A restore drill (optional) — verifies the newest backup actually restores.
# Off by default; schedule a weekly RUN_DRILL=1 run in crontab.
if [ "${RUN_DRILL:-0}" = "1" ]; then
    if bash "$(dirname "$0")/restore_drill.sh"; then
        echo "[$(date '+%F %T')] 恢复演练通过"
    else
        echo "[$(date '+%F %T')] ⚠ 恢复演练失败——备份可能不可用，请立即人工核查" >&2
        exit 1
    fi
fi
