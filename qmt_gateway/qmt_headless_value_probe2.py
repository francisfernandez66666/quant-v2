# qmt_headless_value_probe2.py -- HEADLESS probe round 2 (read-only, no service).
# Round 1 findings: xtdata imports OK on the bundled py3.6.8, but data calls
# need the QMT mini/research data service (full GUI client does not expose it),
# and get_market_data_ex REJECTS keyword fieldList on this build.
# Round 2 therefore:
#   (a) dumps the REAL signatures of the data APIs on this build;
#   (b) tries get_local_data / direct local-cache reads that need NO service.
# Still read-only: no trading, no file writes, no external network.

import glob
import inspect
import os
import sys

CODE = "000001.SZ"
SECTOR_MAIN = u"\u6caa\u6df1A\u80a1"


def short(v, n=200):
    return repr(v)[:n]


def main():
    print("PROBEH2_START py=%s" % sys.version.split()[0])
    sp = None
    for d in glob.glob("D:\\*QMT*\\bin.x64"):
        cand = os.path.join(d, "lib", "site-packages")
        if os.path.isdir(os.path.join(cand, "xtquant")):
            sp = cand
    print("PROBEH2_SITEPKG %s" % sp)
    if not sp:
        print("PROBEH2_DONE reason=no_xtquant")
        return
    sys.path.insert(0, sp)
    from xtquant import xtdata  # noqa: E402

    # (a) real signatures on this build
    for name in ("get_market_data_ex", "get_market_data", "get_local_data",
                 "get_instrument_detail", "get_sector_list",
                 "get_stock_list_in_sector", "get_full_tick"):
        fn = getattr(xtdata, name, None)
        if fn is None:
            print("PROBEH2_SIG %s ABSENT" % name)
            continue
        try:
            sig = str(inspect.signature(fn))
        except Exception:
            sig = "no-signature"
        print("PROBEH2_SIG %s %s" % (name, short(sig, 300)))

    # (b) no-service local reads
    try:
        v = xtdata.get_local_data(field_list=[], stock_list=[CODE],
                                  period="1m", count=20)
        if isinstance(v, dict):
            for k in list(v.keys())[:2]:
                df = v[k]
                cols = list(getattr(df, "columns", []))
                tail = list(df["close"])[-4:] if "close" in cols else None
                print("PROBEH2_LOCAL_M1 KEY=%s N=%d COLS=%s CLOSE_TAIL=%s" % (
                    short(k, 30), len(df), ",".join(cols[:12]), short(tail, 120)))
        else:
            print("PROBEH2_LOCAL_M1 TYPE=%s %s" % (type(v).__name__, short(v)))
    except Exception as exc:
        print("PROBEH2_LOCAL_M1 CALL_ERR %s" % short(exc, 250))

    try:
        d = xtdata.get_instrument_detail(CODE, True)  # True = local only variant
        if isinstance(d, dict):
            bits = ["%s=%s" % (f, short(d.get(f), 30))
                    for f in ("UpStopPrice", "DownStopPrice", "PreClose")
                    if f in d]
            print("PROBEH2_INST_LOCAL N=%d %s" % (len(d), " ".join(bits)))
        else:
            print("PROBEH2_INST_LOCAL TYPE=%s %s" % (type(d).__name__, short(d)))
    except Exception as exc:
        print("PROBEH2_INST_LOCAL CALL_ERR %s" % short(exc, 250))

    try:
        v = xtdata.get_instrument_detail(CODE)
        if isinstance(v, dict):
            bits = ["%s=%s" % (f, short(v.get(f), 30))
                    for f in ("UpStopPrice", "DownStopPrice", "PreClose")
                    if f in v]
            print("PROBEH2_INST N=%d %s" % (len(v), " ".join(bits)))
        else:
            print("PROBEH2_INST TYPE=%s %s" % (type(v).__name__, short(v)))
    except Exception as exc:
        print("PROBEH2_INST CALL_ERR %s" % short(exc, 250))

    try:
        v = xtdata.get_stock_list_in_sector(SECTOR_MAIN)
        print("PROBEH2_SEC_MEMBERS N=%s HEAD=%s" % (
            len(v) if hasattr(v, "__len__") else -1, short(list(v)[:8], 200)))
    except Exception as exc:
        print("PROBEH2_SEC_MEMBERS CALL_ERR %s" % short(exc, 250))

    print("PROBEH2_DONE paste_all_PROBEH2_lines_back")


if __name__ == "__main__":
    main()
