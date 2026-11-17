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

PROBE_BUILD = "2026-11-17-order-time"
_done = False

# Candidate time fields, verbatim from the stale-cancel fix and its alternatives.
TIME_FIELDS = ("m_strInsertDate", "m_strInsertTime", "m_nOrderTime",
               "m_strOrderTime", "order_time", "m_strInsertTimeLocal")

# Identity fields so the owner can see WHICH order each row is (no account data).
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
    try:
        raw = getter("ORDER")
    except Exception as exc:  # noqa: BLE001 - a raised error IS a probe result
        # a raised read error IS the probe answer (wrong arg shape on this build)
        print("ORD_CALL_ERR %s" % repr(exc)[:120])
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
