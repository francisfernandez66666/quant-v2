# qmt_order_time_probe.py -- READ-ONLY probe: which counter field carries the
# order submission time, and in what format?
#
# Owner request 2026-11-17 (PLAN_BOX_STALE_ORDER_AUTOCANCEL section 7-A): the stale-
# cancel fix (a381cb1) reads m_strInsertDate + m_strInsertTime -- the bridge-era
# proven shape (qmt_bridge_strategy.py:679/716) -- but that assumption was never
# confirmed ON THIS BOX by an actual read of a live order object. This probe
# settles it: it prints every candidate time field of the first few ORDER-table
# rows, verbatim, so the fix can be confirmed or corrected before the next bake.
#
# HARD SAFETY RULES:
#   1. NO file IO, NO socket/http, NO database.
#   2. NO trading calls: never passorder / cancel. READ ONLY.
#   3. get_trade_detail_data("ORDER") is the same read the live strategy makes
#      every round (_counter_rows) -- one call, wrapped, then silent forever.
#   4. Pure ASCII (QMT GBK sandbox); stdlib only.
#
# HOW TO RUN (owner, on the physical Win box, QMT model-research / new strategy):
#   1. Paste WHOLE file as a NEW strategy (NEVER touch the live strategy file).
#   2. Run once (simulation is fine -- it orders nothing). It prints ORD_* lines
#      once, then sleeps.
#   3. Paste ALL ORD_* lines back, then STOP this probe strategy.

PROBE_BUILD = "2026-11-17-order-time-v2-3arg"
_done = False

# Candidate time fields, verbatim from the stale-cancel fix and its alternatives.
TIME_FIELDS = ("m_strInsertDate", "m_strInsertTime", "m_nOrderTime",
               "m_strOrderTime", "order_time", "m_strInsertTimeLocal")

# v2 (2026-11-17 21:28 owner paste-back): get_trade_detail_data("ORDER") raised
# ArgumentError -- THIS build wants the 3-arg shape acct/accttype/TABLE, exactly
# what the bridge production code documents. v2 resolves the account the same
# way the bridge does (get_account over type ladder), then drives a bounded
# (acct, accttype, TABLE-casing) ladder. Still one-shot, still read-only.
# (v2 note above). Identity fields so the owner can see WHICH order each row is.
ID_FIELDS = ("m_strOrderSysID", "m_strInstrumentID", "m_strOrderRemark",
             "m_nOrderStatus", "order_id", "stock_code", "order_status")


def _desc(value):
    """ASCII-safe one-line description of an attribute value."""
    if value is None:
        return "None"
    try:
        text = repr(value)
    except Exception:  # noqa: BLE001
        return "<unrepr>"
    return text[:60]


def init(ContextInfo):
    print("ORD_START build=%s" % PROBE_BUILD)


def handlebar(ContextInfo):
    global _done
    if _done:
        return
    _done = True
    getter = getattr(ContextInfo, "get_trade_detail_data", None)
    if not callable(getter):
        getter = globals().get("get_trade_detail_data")
    if not callable(getter):
        print("ORD_ABSENT get_trade_detail_data not on ContextInfo nor globals")
        return
    # Resolve the account the same way the bridge production code does.
    acct = None
    ga = getattr(ContextInfo, "get_account", None)
    if not callable(ga):
        ga = globals().get("get_account")
    if callable(ga):
        # Account-type ladder mirrors the bridge production fallback chain.
        for t in ("stock", "STOCK", "future"):
            try:
                a = ga(t)
            except Exception:  # noqa: BLE001
                continue
            if isinstance(a, (list, tuple)) and a:
                acct = str(a[0])
                break
            if isinstance(a, str) and a:
                acct = a
                break
    print("ORD_ACCT resolved=%s" % _desc(acct))
    if not acct:
        print("ORD_NOACCT get_account ladder empty -- cannot form 3-arg call")
        print("ORD_DONE paste_all_ORD_lines_back")
        return
    # Bounded shape ladder: this build's C++ signature is
    # get_trade_detail_data(acct, accounttype, TABLE) (owner 21:28 paste-back).
    # First shape that returns wins; every rejected shape is logged verbatim so
    # the paste-back alone documents this build's accepted signature.
    raw = None
    last_err = None
    for at in ("stock", "STOCK"):
        # Table-name casing ladder: docs show both ORDER and order spellings.
        for tab in ("ORDER", "order"):
            try:
                raw = getter(acct, at, tab)
                print("ORD_SHAPE_OK accttype=%s table=%s" % (at, tab))
                break
            except Exception as exc:  # noqa: BLE001 - shape errors ARE results
                # remember only the last rejection; earlier shapes stay in the log
                last_err = exc
                print("ORD_SHAPE_ERR accttype=%s table=%s %s" % (
                    at, tab, repr(exc)[:120]))
        if raw is not None:
            break
    if raw is None:
        print("ORD_CALL_ERR all_shapes_failed last=%s" % repr(last_err)[:140])
        return
    if raw is None:
        print("ORD_EMPTY NoneType (no rows or wrong arg shape)")
        return
    try:
        rows = list(raw)
    except TypeError:
        rows = [raw]
    print("ORD_ROWS n=%d" % len(rows))
    shown = 0
    # Dump at most three order rows: identity line + every candidate time field.
    for item in rows:
        if item is None:
            continue
        if shown >= 3:
            break
        shown += 1
        ids = " ".join("%s=%s" % (f, _desc(getattr(item, f, "<absent>")))
                       for f in ID_FIELDS if getattr(item, f, None) is not None)
        print("ORD_ROW %d %s" % (shown, ids[:200]))
        for f in TIME_FIELDS:
            print("ORD_TIME %s = %s" % (f, _desc(getattr(item, f, "<absent>"))))
    if shown == 0:
        print("ORD_NO_ROWS (counter ORDER table empty -- run once while an order exists)")
    print("ORD_DONE paste_all_ORD_lines_back")


def stop(ContextInfo):
    return None
