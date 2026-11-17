# qmt_headless_value_probe3.py -- round 3: get_local_data with explicit
# data_dir to bypass the data service entirely. READ-ONLY, no writes.

import glob
import os
import sys

CODE = "000001.SZ"
SECTOR_MAIN = u"\u6caa\u6df1A\u80a1"


def short(v, n=200):
    # Truncate repr for one-line probe output (same helper as rounds 1-2).
    return repr(v)[:n]


def main():
    print("PROBEH3_START py=%s" % sys.version.split()[0])
    root = None
    for d in glob.glob("D:\\*QMT*\\bin.x64"):
        if os.path.isdir(os.path.join(d, "lib", "site-packages", "xtquant")):
            root = os.path.dirname(d)
    print("PROBEH3_ROOT %s" % short(root, 120))
    if not root:
        print("PROBEH3_DONE reason=no_root")
        return
    sys.path.insert(0, os.path.join(root, "bin.x64", "lib", "site-packages"))
    from xtquant import xtdata  # noqa: E402

    data_dir = os.path.join(root, "datadir")
    print("PROBEH3_DATADIR exists=%s %s" % (os.path.isdir(data_dir), short(data_dir, 120)))

    # Drive three local-read shapes: fielded 1m, all-field 1m, and daily.
    for label, kwargs in (
        ("LOCAL_M1", dict(field_list=["close", "high", "open", "volume"],
                          stock_list=[CODE], period="1m", count=20,
                          data_dir=data_dir)),
        ("LOCAL_M1_ALL", dict(field_list=[], stock_list=[CODE], period="1m",
                              count=20, data_dir=data_dir)),
        ("LOCAL_DAY", dict(field_list=[], stock_list=[CODE], period="1d",
                           count=10, data_dir=data_dir)),
    ):
        try:
            v = xtdata.get_local_data(**kwargs)
            if isinstance(v, dict):
                for k in list(v.keys())[:2]:
                    df = v[k]
                    cols = list(getattr(df, "columns", []))
                    tail = list(df["close"])[-4:] if "close" in cols else None
                    print("PROBEH3_%s KEY=%s N=%d COLS=%s CLOSE_TAIL=%s" % (
                        label, short(k, 30), len(df), ",".join(cols[:12]),
                        short(tail, 120)))
            else:
                print("PROBEH3_%s TYPE=%s %s" % (label, type(v).__name__, short(v)))
        except Exception as exc:
            print("PROBEH3_%s CALL_ERR %s" % (label, short(exc, 250)))

    try:
        d = xtdata.get_instrument_detail(CODE)
        if isinstance(d, dict):
            bits = ["%s=%s" % (f, short(d.get(f), 30))
                    for f in ("UpStopPrice", "DownStopPrice", "PreClose") if f in d]
            print("PROBEH3_INST N=%d %s" % (len(d), " ".join(bits)))
        else:
            print("PROBEH3_INST TYPE=%s %s" % (type(d).__name__, short(d)))
    except Exception as exc:
        print("PROBEH3_INST CALL_ERR %s" % short(exc, 250))

    try:
        v = xtdata.get_stock_list_in_sector(SECTOR_MAIN)
        print("PROBEH3_SEC_MEMBERS N=%s HEAD=%s" % (
            len(v) if hasattr(v, "__len__") else -1, short(list(v)[:8], 200)))
    except Exception as exc:
        print("PROBEH3_SEC_MEMBERS CALL_ERR %s" % short(exc, 250))

    print("PROBEH3_DONE paste_all_PROBEH3_lines_back")


if __name__ == "__main__":
    main()
