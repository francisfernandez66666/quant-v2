# b9_probe.py — §B9 账本测算的只读探查脚本：现网 sqlite 与 T 回合帧的结构、行数、日期跨度。
#
# 为什么单独成文件而不是手敲 SQL：现网手工 SQL/Python 经 PowerShell 管道时**中文字面量会被码页换成
# `?`**（11-02 两次假象的根因），所以判据字段一律 ASCII、表名走白名单常量；且 sqlite 只以
# `mode=ro` URI 打开 —— 这个脚本没有任何写路径，跑完不留任何状态。
#
# English: read-only schema/row-count probe for the B9 ledger measurement; opens sqlite with
# mode=ro only, prints ASCII aggregates, never writes.
# Read-only by construction: sqlite URIs open with mode=ro, JSON files are opened 'rb' only.
import glob
import json
import os
import sqlite3

DATA = r"C:\var\lib\quant-trading-v2"
WANT = ("fills", "orders", "signal_decisions", "trades", "settlements")


def p(*a):
    print(" ".join(str(x) for x in a))


def rows_of(db, table):
    try:
        con = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
        cur = con.cursor()
        cur.execute("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", (table,))
        if not cur.fetchone()[0]:
            con.close()
            return None
        cur.execute("SELECT COUNT(*), MIN(rowid), MAX(rowid) FROM \"%s\"" % table)
        n, lo, hi = cur.fetchone()
        cur.execute("PRAGMA table_info(\"%s\")" % table)
        cols = [r[1] for r in cur.fetchall()]
        con.close()
        return n, cols
    except Exception as e:
        p("  ERR", table, repr(e)[:120])
        return None


def main():
    p("PROBE start")
    p("data_dir_exists", os.path.isdir(DATA))
    dbs = sorted(glob.glob(os.path.join(DATA, "*.db"))) + sorted(glob.glob(os.path.join(DATA, "**", "*.db"), recursive=True))
    seen = set()
    for db in dbs:
        if db in seen:
            continue
        seen.add(db)
        try:
            size = os.path.getsize(db)
        except Exception:
            size = -1
        p("DB", os.path.basename(db), "bytes=" + str(size), "path=" + db)
        for t in WANT:
            got = rows_of(db, t)
            if got is None:
                continue
            n, cols = got
            p("   TBL", t, "rows=" + str(n), "cols=" + ",".join(cols)[:400])
        # date span for the tables we actually need, using whatever date column exists
        for t, keys in (("fills", ("trade_date", "day", "created_at", "ts")),
                        ("orders", ("day", "created_at", "updated_at", "placed_at")),
                        ("signal_decisions", ("day", "date", "created_at"))):
            for k in keys:
                try:
                    con = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
                    cur = con.cursor()
                    cur.execute("SELECT MIN(\"%s\"), MAX(\"%s\"), COUNT(*) FROM \"%s\"" % (k, k, t))
                    lo, hi, n = cur.fetchone()
                    con.close()
                    if n:
                        p("   SPAN", t, k, str(lo), "..", str(hi), "n=" + str(n))
                        break
                except Exception:
                    continue
    p("ACCOUNT_FILES")
    for f in sorted(glob.glob(os.path.join(DATA, "accounts", "*", "t_rounds*.json"))):
        p("  ", os.path.basename(os.path.dirname(f))[-8:], os.path.basename(f), "bytes=" + str(os.path.getsize(f)))
        try:
            with open(f, "rb") as fh:
                obj = json.loads(fh.read().decode("utf-8", "replace"))
        except Exception as e:
            p("     read_err", repr(e)[:100])
            continue
        if isinstance(obj, dict):
            p("     top_keys=" + ",".join(sorted(obj.keys())[:20]))
            for k in ("rounds", "frames", "items", "list"):
                v = obj.get(k)
                if isinstance(v, list) and v:
                    p("     list_key=" + k, "n=" + str(len(v)), "entry_keys=" + ",".join(sorted(v[-1].keys())[:30]))
                    break
        elif isinstance(obj, list) and obj:
            p("     list n=" + str(len(obj)), "entry_keys=" + ",".join(sorted(obj[-1].keys())[:30]))
    p("PROBE_DONE")


main()
