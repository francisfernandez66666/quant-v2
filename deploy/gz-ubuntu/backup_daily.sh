#!/bin/bash
# quant-backup-daily — Linux-side daily full-directory backup (§6.3 / §6.6 ②).
# Replaces the retired Windows scheduled task quant-backup-snap. Never bare-copies
# a WAL database: both ledgers go through sqlite3 online .backup + integrity_check,
# and the whole <DataDir> tree is archived instead of a fixed name list.
set -uo pipefail
SRC="${QUANT_DATA_DIR:-/var/lib/quant-trading-v2}"
OUT="${BACKUP_OUT_DIR:-/var/backups/quant}"
KEEP_DAYS="${KEEP_DAYS:-7}"
STAMP="$(date +%Y%m%d-%H%M%S)"
WORK="$(mktemp -d /tmp/quant-backup.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT" "$WORK/snapshot" || exit 1

python3 - "$SRC" "$WORK/snapshot" <<'PY'
import os, sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
for db in ("live.db", "trading.db"):
    s = sqlite3.connect(os.path.join(src, db))
    d = sqlite3.connect(os.path.join(dst, db))
    with d:
        s.backup(d)
    verdict = d.execute("PRAGMA integrity_check;").fetchone()[0]
    d.close(); s.close()
    if verdict != "ok":
        raise SystemExit("integrity_check_failed:" + db)
PY
if [ $? -ne 0 ]; then
  printf 'BACKUP_RESULT=FAIL reason=db_snapshot\n'
  exit 1
fi

ACCOUNTS="$(find "$SRC/accounts" -type f 2>/dev/null | wc -l | tr -d ' ')"
LIVE_BYTES="$(stat -c %s "$WORK/snapshot/live.db" 2>/dev/null || echo 0)"
TRADING_BYTES="$(stat -c %s "$WORK/snapshot/trading.db" 2>/dev/null || echo 0)"
printf '{"ok":true,"ts":"%s","dbs":{"live.db":%s,"trading.db":%s},"integrity":"ok","accounts_files":%s,"repo":"%s"}\n' \
  "$(date -Iseconds)" "$LIVE_BYTES" "$TRADING_BYTES" "$ACCOUNTS" "$OUT" > "$WORK/SNAPSHOT_OK"

ARCHIVE="$OUT/quant-$STAMP.tar.gz"
# Write an explicit "in progress" marker BEFORE the long tar, so an interrupted
# run can never leave an ok=true marker behind. Replaced with the real one only
# after the archive has been written successfully.
printf '{"ok":false,"reason":"backup_in_progress","ts":"%s"}\n' "$(date -Iseconds)" > "$OUT/SNAPSHOT_OK"
if ! tar -czf "$ARCHIVE" \
  -C "$SRC" \
    --exclude='./trading.db' --exclude='./live.db' \
    --exclude='*.db-wal' --exclude='*.db-shm' \
    --exclude='qmt_gateway' --exclude='*bridge_report*.jsonl' \
    --exclude='bridge_cmd.json' --exclude='*.exe' --exclude='*.ps1' \
    --exclude='__pycache__' --exclude='*.pyc' \
    . \
  -C "$WORK" snapshot SNAPSHOT_OK; then
  printf 'BACKUP_RESULT=FAIL reason=tar\n'
  rm -f "$ARCHIVE"
  exit 1
fi
cp "$WORK/SNAPSHOT_OK" "$OUT/SNAPSHOT_OK" 2>/dev/null || true
find "$OUT" -maxdepth 1 -name 'quant-*.tar.gz' -mtime "+$KEEP_DAYS" -delete 2>/dev/null || true
printf 'BACKUP_RESULT=OK file=%s bytes=%s accounts_files=%s keep_days=%s\n' \
  "$ARCHIVE" "$(stat -c %s "$ARCHIVE")" "$ACCOUNTS" "$KEEP_DAYS"
