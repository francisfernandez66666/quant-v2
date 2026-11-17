# qmt_value_probe_c.py -- COMPACT value probe (v3 coverage, ~half the bytes of
# qmt_api_value_probe.py so a whole-file paste cannot truncate). Settles on this
# broker build, inside the GUI strategy engine: (a) 1m bars, (b) sector plane,
# (c) limit price confirm, (d) tick fields. Covers BOTH camelCase (fieldList)
# and snake_case (field_list) spellings -- headless round 2 proved the local
# xtquant lib uses snake_case, ContextInfo may forward unchanged.
# HARD RULES: read-only data APIs only, no passorder/cancel, no file IO,
# no network, pure ASCII, prints PROBEV2_* once then sleeps. STOP after pasting
# the lines back. PROBE_BUILD below tells the paste-back apart from older runs.

CODE = "000001.SZ"
PROBE_BUILD = "2026-11-17-value-v3c-compact"
_done = False
SN = (u"\u6caa\u6df1A\u80a1", u"\u4e0a\u8bc1A\u80a1")


def s(v, n=180):
    return repr(v)[:n]


def call(label, fn, *a, **kw):
    """One read API call; any exception IS a probe result, keep it."""
    try:
        v = fn(*a, **kw)
    except Exception as exc:  # noqa: BLE001
        print("PROBEV2_%s CALL_ERR %s" % (label, s(exc, 180)))
        return
    print("PROBEV2_%s TYPE=%s" % (label, type(v).__name__))
    if isinstance(v, dict):
        ks = list(v.keys())
        print("PROBEV2_%s N=%d KEYS=%s" % (label, len(ks), ",".join(map(str, ks[:30]))))
        # Sample at most two rows/keys: enough to see field values, keeps the
        # log paste-back short.
        for k in ks[:2]:
            r = v[k]
            if isinstance(r, dict):
                bits = ["%s=%s" % (f, s(r[f], 40))
                        for f in ("lastPrice", "lastClose", "high", "open", "volume")
                        if f in r]
                if bits:
                    print("PROBEV2_%s ROW %s %s" % (label, k, " ".join(bits)))
                else:
                    print("PROBEV2_%s ROW %s SUBKEYS=%s" % (
                        label, k, ",".join(map(str, list(r.keys())[:16]))))
            elif hasattr(r, "columns"):
                cols = list(r.columns)
                tail = list(r["close"])[-4:] if "close" in cols else None
                print("PROBEV2_%s DF %s N=%d COLS=%s CLOSE_TAIL=%s" % (
                    label, k, len(r), ",".join(map(str, cols[:12])), s(tail, 120)))
            else:
                print("PROBEV2_%s ROW %s VAL=%s" % (label, k, s(r)))
    elif isinstance(v, (list, tuple)):
        print("PROBEV2_%s N=%d HEAD=%s" % (label, len(v), s(list(v[:10]), 240)))
    else:
        print("PROBEV2_%s VAL=%s" % (label, s(v)))


def init(ContextInfo):
    print("PROBEV2_START build=%s" % PROBE_BUILD)
    # Presence dump of every API this probe may drive, so an ABSENT line
    # downstream is distinguishable from a paste truncation.
    for n in ("get_full_tick", "get_market_data_ex", "get_market_data2",
              "get_instrument_detail", "get_sector_list",
              "get_stock_list_in_sector", "get_sector_type", "get_index_weight"):
        print("PROBEV2_PRESENT %s=%d" % (n, 1 if hasattr(ContextInfo, n) else 0))


def handlebar(ContextInfo):
    global _done
    if _done:
        return
    _done = True
    # (a) tick with field->value samples
    gft = getattr(ContextInfo, "get_full_tick", None)
    if callable(gft):
        call("TICK", gft, [CODE])
    else:
        print("PROBEV2_TICK ABSENT")
    # (b) limit price one-line confirm (settled in v1, re-confirm on v3 run)
    gid = getattr(ContextInfo, "get_instrument_detail", None)
    if callable(gid):
        call("INST", gid, CODE)
    # (c) 1m bars: positional + both keyword spellings
    gmd = getattr(ContextInfo, "get_market_data_ex", None)
    if callable(gmd):
        call("M1_POS", gmd, [], [CODE], "1m", "", "", 20, "front")
        call("M1_KW", gmd, fieldList=[], stockList=[CODE], period="1m", count=20)
        call("M1_KWF", gmd, fieldList=["close", "high", "low", "open", "volume"],
             stockList=[CODE], period="1m", count=20)
        call("M1_KW_SN", gmd, field_list=[], stock_list=[CODE], period="1m", count=20)
        call("M1_KWF_SN", gmd,
             field_list=["close", "high", "low", "open", "volume"],
             stock_list=[CODE], period="1m", count=20)
        call("M1_DAY", gmd, [], [CODE], "1d", "", "", 10, "front")
    gm2 = getattr(ContextInfo, "get_market_data2", None)
    if callable(gm2):
        call("M2_POS", gm2, [], [CODE], "1m", "", "", 20, "front")
    # (d) sector plane: absent vs wrong-shape must be told apart
    gsl = getattr(ContextInfo, "get_sector_list", None)
    if callable(gsl):
        call("SEC_LIST", gsl)
    else:
        print("PROBEV2_SEC_LIST ABSENT")
    gsil = getattr(ContextInfo, "get_stock_list_in_sector", None)
    if callable(gsil):
        # Real Chinese sector names, \\u-escaped to keep the source byte-ASCII.
        for sn in SN:
            call("SEC_MEMBERS", gsil, sn)
            call("SEC_MEMBERS_T", gsil, sn, "stock")
        call("SEC_MEMBERS_KW", gsil, sector=SN[0])
    else:
        print("PROBEV2_SEC_MEMBERS ABSENT")
    gst = getattr(ContextInfo, "get_sector_type", None)
    if callable(gst):
        call("SEC_TYPE", gst)
    giw = getattr(ContextInfo, "get_index_weight", None)
    if callable(giw):
        call("IDX_WEIGHT", giw, "000300.SH")
    print("PROBEV2_BARNAMES %s" % ",".join(sorted(
        n for n in dir(ContextInfo)
        if any(k in n.lower() for k in ("bar", "kline", "current", "period", "sector", "industry")))[:40] or ["none"]))
    print("PROBEV2_DONE paste_all_PROBEV2_lines_back")


def stop(ContextInfo):
    return None
