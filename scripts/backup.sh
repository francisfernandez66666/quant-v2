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
DB="${DATA_DIR}/trading.db"
if [ -f "$DB" ]; then
    if command -v sqlite3 >/dev/null 2>&1; then
        sqlite3 "$DB" ".backup '${DEST}/trading.db'"
    else
        cp "$DB" "${DEST}/trading.db"   # 无 sqlite3 时退化为文件拷贝（风险：写瞬间不一致）
    fi
else
    echo "警告: 未找到 ${DB}，跳过库备份"
fi

# 2) 关键 JSON（auth.json 权限 0600，拷贝后保持）
for f in auth.json config.json; do
    [ -f "${DATA_DIR}/${f}" ] && install -m 600 "${DATA_DIR}/${f}" "${DEST}/${f}"
done

# 3) 应用战法规则（审批产物，丢失需重跑寻优+审批）
for f in applied_rules.json applied_factors.json applied_patterns.json; do
    [ -f "${DATA_DIR}/${f}" ] && cp "${DATA_DIR}/${f}" "${DEST}/"
done

# 4) 清理过期备份
find "$BACKUP_DIR" -maxdepth 1 -type d -name '20*' -mtime "+$KEEP_DAYS" -exec rm -rf {} \; 2>/dev/null || true

# 5) 异地副本（可选）
if [ -n "$OFFSITE_DIR" ] && [ -d "$OFFSITE_DIR" ]; then
    rsync -a --delete "${BACKUP_DIR}/" "${OFFSITE_DIR}/"
    echo "异地副本已同步 → $OFFSITE_DIR"
fi

SIZE=$(du -sh "$DEST" | cut -f1)
echo "[$(date '+%F %T')] 备份完成: $DEST ($SIZE)"
