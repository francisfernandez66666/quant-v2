# qmt_headless_value_probe.py -- HEADLESS read-only value probe (agent-run variant).
# Purpose: settle (a) 1m bars availability, (b) sector plane availability on THIS
# broker build WITHOUT needing the owner to paste a strategy into QMT GUI.
# Complements qmt_api_value_probe.py (v2, GUI variant, already on box).
#
# HOW IT WORKS: imports xtquant.xtdata from the RUNNING XtItClient install and
# talks to the client's local data service. Same broker build, same local data,
# so a returned value here answers the same question the GUI probe asks.
#
# HARD SAFETY RULES:
#   1. READ-ONLY data APIs only. NO trading, NO passorder, NO xttrader session.
#   2. NO file writes (except nothing), NO network beyond localhost data svc.
#   3. Pure ASCII source (QMT GBK box). Chinese sector names are \\u-escaped.
#   4. Prints PROBEH_* lines once, exits.
#
# Run: python C:\\qmt\\qmt_headless_value_probe.py

import glob
import os
import sys

CODE = "000001.SZ"
SECTOR_MAIN = u"\u6caa\u6df1A\u80a1"      # HS A-shares
SECTOR_SH = u"\u4e0a\u8bc1A\u80a1"        # SH A-shares


def find_site_packages():
    # Locate the running QMT install without hardcoding the Chinese dir name.
    for d in glob.glob("D:\\*QMT*\\bin.x64") + glob.glob("C:\\*QMT*\\bin.x64"):
        sp = os.path.join(d, "lib", "site-packages")
        if os.path.isdir(os.path.join(sp, "xtquant")):
            return sp
    return None


def short(v, n=160):
    return repr(v)[:n]


def main():
    print("PROBEH_START headless=1 py=%s" % sys.version.split()[0])
    sp = find_site_packages()
    print("PROBEH_SITEPKG %s" % sp)
    if not sp:
        print("PROBEH_DONE reason=no_xtquant_found")
        return
    sys.path.insert(0, sp)
    try:
        from xtquant import xtdata  # noqa: E402
    except Exception as exc:
        print("PROBEH_IMPORT_ERR %s" % short(exc, 300))
        print("PROBEH_DONE reason=import_failed")
        return
    print("PROBEH_IMPORT_OK %s" % short(xtdata.__file__, 160))

    # (a) instrument detail incl limit price (GUI probe settled this; confirm
    #     headless parity once)
    try:
        d = xtdata.get_instrument_detail(CODE)
        if isinstance(d, dict):
            bits = ["%s=%s" % (f, short(d.get(f), 30))
                    for f in ("UpStopPrice", "DownStopPrice", "PreClose", "lastPrice")
                    if f in d]
            print("PROBEH_INST N=%d %s" % (len(d), " ".join(bits)))
        else:
            print("PROBEH_INST TYPE=%s %s" % (type(d).__name__, short(d)))
    except Exception as exc:
        print("PROBEH_INST CALL_ERR %s" % short(exc, 200))

    # (b) 1-minute bars -- the real question from v2 GUI probe
    for label, kwargs in (
        ("M1_EX", dict(fieldList=[], stock_list=[CODE], period="1m", count=20)),
        ("M1_EXF", dict(fieldList=["close", "high", "low", "open", "volume"],
                        stock_list=[CODE], period="1m", count=20)),
        ("M1_DAY", dict(fieldList=[], stock_list=[CODE], period="1d", count=10)),
    ):
        try:
            v = xtdata.get_market_data_ex(**kwargs)
            if isinstance(v, dict):
                for k in list(v.keys())[:2]:
                    df = v[k]
                    cols = list(getattr(df, "columns", []))
                    tail = list(df["close"])[-4:] if "close" in cols else None
                    print("PROBEH_%s KEY=%s N=%d COLS=%s CLOSE_TAIL=%s" % (
                        label, short(k, 30), len(df), ",".join(cols[:12]),
                        short(tail, 120)))
            else:
                print("PROBEH_%s TYPE=%s %s" % (label, type(v).__name__, short(v)))
        except Exception as exc:
            print("PROBEH_%s CALL_ERR %s" % (label, short(exc, 200)))

    # (c) tick
    try:
        v = xtdata.get_full_tick([CODE])
        row = v.get(CODE) if isinstance(v, dict) else None
        if isinstance(row, dict):
            bits = ["%s=%s" % (f, short(row.get(f), 40))
                    for f in ("lastPrice", "lastClose", "high", "open", "volume")
                    if f in row]
            print("PROBEH_TICK %s" % " ".join(bits))
        else:
            print("PROBEH_TICK TYPE=%s %s" % (type(v).__name__, short(v)))
    except Exception as exc:
        print("PROBEH_TICK CALL_ERR %s" % short(exc, 200))

    # (d) sector plane -- is membership available headless?
    try:
        v = xtdata.get_sector_list()
        print("PROBEH_SEC_LIST TYPE=%s N=%s HEAD=%s" % (
            type(v).__name__, len(v) if hasattr(v, "__len__") else -1,
            short(list(v)[:8], 200)))
    except Exception as exc:
        print("PROBEH_SEC_LIST CALL_ERR %s" % short(exc, 200))
    for sn in (SECTOR_MAIN, SECTOR_SH):
        try:
            v = xtdata.get_stock_list_in_sector(sn)
            print("PROBEH_SEC_MEMBERS sector=%s N=%s HEAD=%s" % (
                short(sn, 20), len(v) if hasattr(v, "__len__") else -1,
                short(list(v)[:8], 200)))
        except Exception as exc:
            print("PROBEH_SEC_MEMBERS sector=%s CALL_ERR %s" % (
                short(sn, 20), short(exc, 200)))

    print("PROBEH_DONE paste_all_PROBEH_lines_back")


if __name__ == "__main__":
    main()
