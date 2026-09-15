# backup_snap.py — 广州机 SQLite 一致性快照 + 完整性校验（HARDENING 件1，拉取模式的服务端半边）
# 由 backup_snapshot.ps1 调用。用 sqlite3 标准库 backup API：并发写入下产出事务一致的单文件，
# 不需要停引擎、不需要 wal_checkpoint 独占（.backup 自带锁语义但极短）。
# 保持 ASCII 源码，Windows PS5.1/cron 环境无编码歧义。
import json
import sqlite3
import sys
from pathlib import Path

SRC = Path(r"C:\var\lib\quant-trading-v2\trading.db")
DST = Path(r"C:\var\lib\quant-snapshot\trading.db")


def main() -> int:
    if not SRC.exists():
        print(f"missing source db: {SRC}", file=sys.stderr)
        return 2
    DST.parent.mkdir(parents=True, exist_ok=True)
    if DST.exists():
        DST.unlink()
    src = sqlite3.connect(str(SRC))
    dst = sqlite3.connect(str(DST))
    try:
        with dst:
            src.backup(dst)
        ok = dst.execute("PRAGMA integrity_check;").fetchone()[0]
        if ok != "ok":
            print(f"integrity_check failed: {ok}", file=sys.stderr)
            return 3
        print(f"snapshot ok size={DST.stat().st_size}")
        return 0
    finally:
        dst.close()
        src.close()


if __name__ == "__main__":
    sys.exit(main())
