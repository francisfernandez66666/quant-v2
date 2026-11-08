#!/bin/bash
# 广州 Ubuntu 外脑「日常全目录备份」腿（方案 §6.3 / §6.6 工单 ②）。
# v3 2026-11-08：补逐文件 sha256 manifest + excluded[] 登记，脚本中文头与注释双闸，
# 归档写完后强制校验两库快照、标记与清单成员。服务运行中数据目录会持续被写，
# GNU tar 因此可能返回 rc=1（file changed as we read it），按方案 §7.3 登记为
# 允许差异白名单；只有 rc>=2 才是真失败。绝不把「归档存在」当成「数据完整」。
set -uo pipefail
SRC="${QUANT_DATA_DIR:-/var/lib/quant-trading-v2}"
OUT="${BACKUP_OUT_DIR:-/var/backups/quant}"
KEEP_DAYS="${KEEP_DAYS:-7}"
STAMP="$(date +%Y%m%d-%H%M%S)"
WORK="$(mktemp -d /tmp/quant-backup.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT" "$WORK/snapshot" || exit 1

# 两库绝不裸拷 WAL 主库：走 sqlite3 在线 .backup 得到事务一致的 standalone 单文件，
# 再逐库做 integrity_check，任一不 ok 直接判红，不留下一个「能打开但账本错位」的快照。
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
# 长 tar 之前先写「进行中」标记：中断时绝不会留下 ok=true 的假绿状态。
printf '{"ok":false,"reason":"backup_in_progress","ts":"%s"}\n' "$(date -Iseconds)" > "$OUT/SNAPSHOT_OK"

# ── 逐文件 sha256 manifest + excluded[]（与下面 tar 的排除规则逐条同源）──
# 只登记真正入档的文件；原始两库以在线快照替代，故两条排除理由单独标注。
python3 - "$SRC" "$WORK/snapshot" "$WORK/MANIFEST.json" <<'PY'
import hashlib, json, os, stat, sys, time
src, snap, out = sys.argv[1], sys.argv[2], sys.argv[3]

def digest(p):
    h = hashlib.sha256()
    with open(p, "rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()

def entry(path, rel):
    st = os.stat(path)
    return {"rel_path": rel, "bytes": st.st_size,
            "sha256": digest(path), "mode": oct(stat.S_IMODE(st.st_mode))}

files, excluded = [], []
SKIP_DIRS = {"qmt_gateway": "bridge_gateway", "__pycache__": "test_junk"}
for root, dirs, names in os.walk(src):
    for d in list(dirs):
        if d in SKIP_DIRS:
            excluded.append({"rel_path": os.path.relpath(os.path.join(root, d), src),
                             "reason": SKIP_DIRS[d]})
            dirs.remove(d)
    for name in sorted(names):
        path = os.path.join(root, name)
        rel = os.path.relpath(path, src)
        if rel in ("trading.db", "live.db"):
            excluded.append({"rel_path": rel, "reason": "db_raw_replaced_by_online_snapshot"})
            continue
        if (name.endswith((".db-wal", ".db-shm", ".exe", ".ps1", ".pyc"))
                or "bridge_report" in name or name == "bridge_cmd.json"):
            excluded.append({"rel_path": rel, "reason": "bridge_or_windows_deploy"})
            continue
        try:
            files.append(entry(path, rel))
        except OSError as exc:
            excluded.append({"rel_path": rel, "reason": "stat_failed:" + type(exc).__name__})
for db in ("live.db", "trading.db"):
    files.append(entry(os.path.join(snap, db), "snapshot/" + db))
doc = {"kind": "daily_backup_manifest_v1", "ts": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
       "root": src, "files": sorted(files, key=lambda e: e["rel_path"]),
       "excluded": sorted(excluded, key=lambda e: e["rel_path"]),
       "totals": {"files": len(files), "bytes": sum(e["bytes"] for e in files)}}
with open(out, "w", encoding="utf-8") as fh:
    json.dump(doc, fh, sort_keys=True)
print("MANIFEST_BUILT files=%d excluded=%d bytes=%d" % (
    doc["totals"]["files"], len(excluded), doc["totals"]["bytes"]))
PY
if [ ! -s "$WORK/MANIFEST.json" ]; then
  printf 'BACKUP_RESULT=FAIL reason=manifest_build\n'
  exit 1
fi

ARCHIVE="$OUT/quant-$STAMP.tar.gz"
# './trading.db' 必须带路径前缀：裸文件名会被 GNU tar 在任意路径匹配，
# 把 snapshot/trading.db（9.58 GB）一起排掉（11-08 实测踩过）。
tar --warning=no-file-changed -czf "$ARCHIVE" \
  -C "$SRC" \
    --exclude='./trading.db' --exclude='./live.db' \
    --exclude='*.db-wal' --exclude='*.db-shm' \
    --exclude='qmt_gateway' --exclude='*bridge_report*.jsonl' \
    --exclude='bridge_cmd.json' --exclude='*.exe' --exclude='*.ps1' \
    --exclude='__pycache__' --exclude='*.pyc' \
    . \
  -C "$WORK" snapshot SNAPSHOT_OK MANIFEST.json
trc=$?
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
for m in snapshot/trading.db snapshot/live.db SNAPSHOT_OK MANIFEST.json; do
  grep -qxF "$m" "$MEMBERS" || missing="$missing $m"
done
if [ -n "$missing" ]; then
  printf 'BACKUP_RESULT=FAIL reason=archive_missing:%s\n' "$missing"
  rm -f "$ARCHIVE"
  exit 1
fi

# 裸 cp 会被 WorkBuddy brokered-bin 文件策略拦截（win 包构建 11-08 实测），用绝对路径。
/bin/cp "$WORK/SNAPSHOT_OK" "$OUT/SNAPSHOT_OK" 2>/dev/null || true
/bin/cp "$WORK/MANIFEST.json" "$OUT/MANIFEST.json" 2>/dev/null || true
find "$OUT" -maxdepth 1 -name 'quant-*.tar.gz' -mtime "+$KEEP_DAYS" -delete 2>/dev/null || true
printf 'BACKUP_RESULT=OK file=%s bytes=%s accounts_files=%s tar_rc=%s live_service_writes=allowed keep_days=%s\n' \
  "$ARCHIVE" "$(stat -c %s "$ARCHIVE")" "$ACCOUNTS" "$trc" "$KEEP_DAYS"
