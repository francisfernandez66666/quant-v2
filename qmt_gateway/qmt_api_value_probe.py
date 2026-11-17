# qmt_api_value_probe.py -- READ/VALUE probe: do these built-in APIs actually RETURN data?
# Owner request 2026-11-17 16:22 (v1) / 17:2x (v2 shape fix + sector plane):
# settle whether the in-client strategy can get (a) the limit-up price, (b)
# 1-minute bars, and (c) SECTOR membership on THIS broker build.
#
# DIFFERENCE from qmt_api_probe.py (presence-only): this probe CALLS the APIs.
#
# WHY v2 EXISTS (first run, owner pasted back the 16:39 log):
#   * get_instrument_detail -> SUCCESS: 30 keys incl UpStopPrice=13.53. The
#     limit price IS available in-client (rules_ladder.py already self-computes
#     sealed/touched/limit_pct from it). SETTLED -- do not re-probe.
#   * get_full_tick -> dict with the code key; v1 printed keys only, not the
#     field->value samples, so lastPrice was invisible in the log.
#   * get_market_data_ex -> ALL THREE v1 calls were CALL_ERR. Two used field=
#     (the real keyword is fieldList) and one passed {} as fieldList (it must
#     be a LIST). The ArgumentError text even named get_market_data2, the legacy
#     signature this build forwards to. => v1 failed on PROBE SHAPE, NOT proof
#     that the API is empty. v2 tries the real signatures from
#     qmt_bridge_strategy.py:2251-2255.
#   * SECTOR plane -- rules_ladder.py:32-37 asserts get_sector_list /
#     get_sector_type / get_index_weight are ABSENT, but that came from a
#     one-or-two-shape capability probe (qmt_bridge_strategy.py:2233-2239).
#     A wrong shape reads the same as "absent". v2 drives each sector API
#     through a shape ladder and prints the HEAD of any member list, so a
#     real "absent" is told apart from "we guessed the signature wrong".
#     If sector membership IS available: combined with UpStopPrice (limit) and
#     get_full_tick (last), sector_limit_up_drop_pct becomes recomputable
#     in-client (today's per-sector limit count vs a _STATE snapshot from
#     yesterday). If absent, that factor stays baked/absent as before.
#
# HARD SAFETY RULES:
#   1. NO file IO, NO socket/http/requests/urllib, NO database.
#   2. NO trading calls: never passorder / cancel / place any order.
#   3. Only DATA-READ APIs, each in try/except; the whole run fires once then
#      goes silent (_done). A signature LADDER (a few shapes for one API) is
#      intentional -- that is the only way to tell absent from wrong-shape.
#   4. NO live-order state touched. Run in PAPER/shadow anyway (orders nothing).
#   5. Pure ASCII (QMT GBK sandbox); stdlib only. Chinese sector names are
#      \\u-escaped so no non-ASCII byte ever lands in this file.
#
# HOW TO RUN (owner, on the physical Win box, QMT model-research / new strategy):
#   1. Paste WHOLE file as a NEW strategy (NEVER touch the live strategy file).
#   2. Run once in simulation/research. It prints PROBEV2_* lines once, then sleeps.
#   3. Paste ALL PROBEV2_* lines back, then STOP this probe strategy.

import sys

PROBE_BUILD = "2026-11-17-value-v2sector"
_done = False
CODE = "000001.SZ"

INST_FIELDS = ("UpStopPrice", "DownStopPrice", "PreClose", "lastPrice")
# Real Chinese sector names, escaped to keep this source byte-ASCII (QMT GBK).
# 0 = "HSJ A-shares" (u6caa u6df1 A u80a1); 1 = "SH A-shares".
SECTOR_NAMES = (u"\u6caa\u6df1A\u80a1", u"\u4e0a\u8bc1A\u80a1")


def _short(v, n=200):
    return repr(v)[:n]


def _safe_tick(label, fn, codes):
    """Print tick field->value samples (v1 printed only the outer code keys)."""
    try:
        v = fn(codes)
    except Exception as exc:  # noqa: BLE001 - exception IS a probe result
        print("PROBEV2_%s CALL_ERR %s" % (label, _short(exc, 160)))
        return
    print("PROBEV2_%s TYPE=%s" % (label, type(v).__name__))
    if isinstance(v, dict):
        print("PROBEV2_%s KEYS=%s" % (label, ",".join(str(k) for k in v.keys())))
        for k in v:
            row = v[k]
            if isinstance(row, dict):
                bits = []
                for f in ("lastPrice", "lastClose", "high", "open", "volume", "stockStatus"):
                    if f in row:
                        bits.append("%s=%s" % (f, _short(row[f], 40)))
                print("PROBEV2_%s ROW %s N=%d %s" % (label, k, len(row), " ".join(bits)))
            else:
                print("PROBEV2_%s ROW %s TYPE=%s VAL=%s" % (label, k, type(row).__name__, _short(row)))


def _safe_read(label, fn, *args, **kwargs):
    """Call one read API and dump its return shape; exceptions are results.
    Handles dict (keys + one-level descend), list/tuple (head), DataFrame-like
    (columns + close/high tail), and scalar -- so bars AND sector members both
    print something decisive without a second probe run.
    """
    try:
        v = fn(*args, **kwargs)
    except Exception as exc:  # noqa: BLE001
        print("PROBEV2_%s CALL_ERR %s" % (label, _short(exc, 200)))
        return
    print("PROBEV2_%s TYPE=%s" % (label, type(v).__name__))
    if isinstance(v, dict):
        keys = list(v.keys())
        print("PROBEV2_%s N=%d KEYS=%s" % (label, len(keys), ",".join(str(k) for k in keys[:40])))
        for k in keys[:2]:
            val = v[k]
            if isinstance(val, dict):
                print("PROBEV2_%s KEY=%s SUBKEYS=%s" % (
                    label, k, ",".join(str(x) for x in list(val.keys())[:20])))
            elif isinstance(val, (list, tuple)):
                print("PROBEV2_%s KEY=%s N=%d HEAD=%s" % (
                    label, k, len(val), _short(list(val)[:6], 160)))
            else:
                print("PROBEV2_%s KEY=%s VAL=%s" % (label, k, _short(val, 80)))
        for f in ("close", "high"):
            try:
                if hasattr(v, "columns") and f in list(v.columns):
                    print("PROBEV2_%s %s_TAIL=%s" % (label, f, _short(list(v[f])[-4:], 120)))
            except Exception:  # noqa: BLE001
                pass
        return
    if isinstance(v, (list, tuple)):
        print("PROBEV2_%s N=%d HEAD=%s" % (label, len(v), _short(list(v[:12]), 300)))
        return
    print("PROBEV2_%s VAL=%s" % (label, _short(v)))


def init(ContextInfo):
    print("PROBEV2_START build=%s" % PROBE_BUILD)
    try:
        ContextInfo.set_universe([CODE])
        print("PROBEV2_INIT universe=%s" % CODE)
    except Exception as exc:  # noqa: BLE001
        print("PROBEV2_INIT skipped %s" % _short(exc, 80))
    for n in ("get_full_tick", "get_market_data_ex", "get_market_data2",
              "get_instrument_detail", "get_sector_list", "get_stock_list_in_sector",
              "get_sector_type", "get_index_weight"):
        print("PROBEV2_PRESENT %s=%d" % (n, 1 if hasattr(ContextInfo, n) else 0))


def handlebar(ContextInfo):
    global _done
    if _done:
        return
    _done = True

    gft = getattr(ContextInfo, "get_full_tick", None)
    if callable(gft):
        _safe_tick("TICK", gft, [CODE])
    else:
        print("PROBEV2_TICK ABSENT")

    # Limit price was settled in v1; one confirm line only, no re-probe needed.
    gid = getattr(ContextInfo, "get_instrument_detail", None)
    if callable(gid):
        try:
            d = gid(CODE)
            bits = ["%s=%s" % (f, _short(d.get(f), 30))
                    for f in INST_FIELDS if isinstance(d, dict) and f in d]
            print("PROBEV2_INST N=%s %s" % (len(d) if isinstance(d, dict) else -1, " ".join(bits)))
        except Exception as exc:  # noqa: BLE001
            print("PROBEV2_INST CALL_ERR %s" % _short(exc, 120))

    # 1m bars: real signatures. fieldList (a LIST) is mandatory; v1's field=
    # keyword and {} first arg were the reason all three calls failed.
    gmd = getattr(ContextInfo, "get_market_data_ex", None)
    if callable(gmd):
        _safe_read("M1_POS", gmd, [], [CODE], "1m", "", "", 20, "front")
        _safe_read("M1_KW", gmd, fieldList=[], stockList=[CODE], period="1m", count=20)
        _safe_read("M1_KWF", gmd, fieldList=["close", "high", "low", "open", "volume"],
                   stockList=[CODE], period="1m", count=20)
        _safe_read("M1_DAY", gmd, [], [CODE], "1d", "", "", 20, "front")
    else:
        print("PROBEV2_M1 ABSENT")

    # The v1 ArgumentError named get_market_data2; probe it directly too.
    gm2 = getattr(ContextInfo, "get_market_data2", None)
    if callable(gm2):
        _safe_read("M2_POS", gm2, [], [CODE], "1m", "", "", 20, "front")

    # ---- SECTOR plane ----
    # get_sector_list: no-arg is the documented shape; try a couple of kwargs
    # in case this build wants them, so "absent" != "wrong shape".
    gsl = getattr(ContextInfo, "get_sector_list", None)
    if callable(gsl):
        _safe_read("SEC_LIST", gsl)
    else:
        print("PROBEV2_SEC_LIST ABSENT")

    # get_stock_list_in_sector: the factor needs MEMBERS of a sector to count
    # limit-ups. Drive several shapes (bare sector / sector+type / kwargs).
    gsil = getattr(ContextInfo, "get_stock_list_in_sector", None)
    if callable(gsil):
        for sn in SECTOR_NAMES:
            _safe_read("SEC_MEMBERS", gsil, sn)
            _safe_read("SEC_MEMBERS_T", gsil, sn, "stock")
        _safe_read("SEC_MEMBERS_KW", gsil, sector=SECTOR_NAMES[0])
    else:
        print("PROBEV2_SEC_MEMBERS ABSENT")

    gst = getattr(ContextInfo, "get_sector_type", None)
    if callable(gst):
        _safe_read("SEC_TYPE", gst)

    giw = getattr(ContextInfo, "get_index_weight", None)
    if callable(giw):
        _safe_read("IDX_WEIGHT", giw, "000300.SH")

    cur = [n for n in dir(ContextInfo)
           if any(k in n.lower() for k in ("bar", "kline", "current", "period", "sector", "industry"))]
    print("PROBEV2_BARNAMES %s" % (",".join(sorted(cur)[:60]) if cur else "none"))
    print("PROBEV2_DONE paste_all_PROBEV2_lines_back")


def stop(ContextInfo):
    return None
