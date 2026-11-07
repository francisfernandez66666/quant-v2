#!/bin/bash
# quant-backup-daily — Linux-side daily full-directory backup (§6.3 / §6.6 ②).
# v2 (2026-11-08): tolerate "file changed as we read it" (services keep writing
# logs/wal while the archive is built), but PROVE the archive actually contains
# both ledger snapshots and the marker before declaring success.
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
# Explicit in-progress marker: an interrupted run must never leave ok=true behind.
printf '{"ok":false,"reason":"backup_in_progress","ts":"%s"}\n' "$(date -Iseconds)" > "$OUT/SNAPSHOT_OK"

ARCHIVE="$OUT/quant-$STAMP.tar.gz"
# './trading.db' (NOT bare 'trading.db'): GNU tar matches bare names at ANY path,
# which silently dropped snapshot/trading.db (9.58 GB) in the first run.
tar --warning=no-file-changed -czf "$ARCHIVE" \
  -C "$SRC" \
    --exclude='./trading.db' --exclude='./live.db' \
    --exclude='*.db-wal' --exclude='*.db-shm' \
    --exclude='qmt_gateway' --exclude='*bridge_report*.jsonl' \
    --exclude='bridge_cmd.json' --exclude='*.exe' --exclude='*.ps1' \
    --exclude='__pycache__' --exclude='*.pyc' \
    . \
  -C "$WORK" snapshot SNAPSHOT_OK
trc=$?
# tar rc=1 means "some files changed during read" (expected with live services);
# rc>=2 is a real failure. Either way the member check below is the authority.
if [ "$trc" -ge 2 ]; then
  printf 'BACKUP_RESULT=FAIL reason=tar rc=%s\n' "$trc"
  rm -f "$ARCHIVE"
  exit 1
fi
MEMBERS="$WORK/members.txt"
if ! tar -tzf "$ARCHIVE" > "$MEMBERS" 2>/dev/null; then
  printf 'BACKUP_RESULT=FAIL reason=archive_unreadable\n'
  rm -f "$ARCHIVE"
  exit 1
fi
missing=""
for m in snapshot/trading.db snapshot/live.db SNAPSHOT_OK; do
  grep -qxF "$m" "$MEMBERS" || missing="$missing $m"
done
if [ -n "$missing" ]; then
  printf 'BACKUP_RESULT=FAIL reason=archive_missing:%s\n' "$missing"
  rm -f "$ARCHIVE"
  exit 1
fi

cp "$WORK/SNAPSHOT_OK" "$OUT/SNAPSHOT_OK" 2>/dev/null || true
find "$OUT" -maxdepth 1 -name 'quant-*.tar.gz' -mtime "+$KEEP_DAYS" -delete 2>/dev/null || true
printf 'BACKUP_RESULT=OK file=%s bytes=%s accounts_files=%s tar_rc=%s live_service_writes=allowed keep_days=%s\n' \
  "$ARCHIVE" "$(stat -c %s "$ARCHIVE")" "$ACCOUNTS" "$trc" "$KEEP_DAYS"
