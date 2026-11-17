# qmt_order_time_probe.py -- READ-ONLY probe: which counter field carries the
# order submission time, and in what format?
#
# Owner request 2026-11-17 (PLAN_BOX_STALE_ORDER_AUTOCANCEL section 7-A): the
# stale-cancel fix (a381cb1) reads m_strInsertDate + m_strInsertTime -- the
# bridge-era proven shape (qmt_bridge_strategy.py:679/716) -- but that assumption
# was never confirmed ON THIS BOX by an actual read of a live order object.
#
# VERSION HISTORY (all owner paste-backs, same day):
#   v1 get_trade_detail_data("ORDER") 1-arg -> ArgumentError: this build wants
#      the 3-arg C++ shape (acct, accounttype, TABLE).
#   v2 get_account ladder empty in the model-research environment -> no acct.
#   v3 cfg fallback -> NameError: the QMT strategy sandbox has NO __file__.
#   v4 fixed paths -> PermissionError 'Foribdden FileIO': the sandbox forbids
#      ALL file IO. Sandbox env-info channels are ONLY builtins + baked params.
#   v5 const acct -> chain FULLY OK: shape (acct,"stock","ORDER") returns, but
#      the ORDER table was empty (after close, no in-flight orders).
#   v6 WATCH MODE: query every bar, print ONLY on change, so the owner runs it
#      once before the next session and pastes back whenever orders flow.
#
# HARD SAFETY RULES:
#   1. NO socket/http, NO database, NO file IO at all (sandbox forbids it; the
#      v3/v4 cfg-read attempts are kept only as inert logging on non-sandbox
#      runs). NO trading calls: never passorder / cancel. READ ONLY.
#   2. Account fallback is a constant (002069008957) -- same id already in repo
#      fixtures (tests/test_broker_reads_rebuild_20261110.py), no new disclosure.
#   3. Pure ASCII (QMT GBK sandbox); stdlib only.
#
# HOW TO RUN (owner, QMT model-research / new strategy):
#   1. Paste WHOLE file as a NEW strategy (NEVER touch the live strategy file).
#   2. Run in simulation. It prints ORD_* once at boot, then stays quiet until
#      the ORDER table changes (new order / status change), re-printing fields
#      then. Leave it running through the session; paste ALL ORD_* lines back
#      afterwards, then STOP this probe strategy.

PROBE_BUILD = "2026-11-17-order-time-v6-watch"
CONST_ACCT = "002069008957"

# Candidate time fields, verbatim from the stale-cancel fix and its alternatives.
TIME_FIELDS = ("m_strInsertDate", "m_strInsertTime", "m_nOrderTime",
               "m_strOrderTime", "order_time", "m_strInsertTimeLocal")

# Identity fields so the owner can see WHICH order each row is (no account data).
ID_FIELDS = ("m_strOrderSysID", "m_strInstrumentID", "m_strOrderRemark",
             "m_nOrderStatus", "order_id", "stock_code", "order_status")

# Watch state: resolved once, then reused every bar.
_state = {"acct": None, "shape": None, "getter": None, "sig": None, "dead": False}


def _desc(value):
    """ASCII-safe one-line description of an attribute value."""
    if value is None:
        return "None"
    try:
        text = repr(value)
    except Exception:  # noqa: BLE001
        return "<unrepr>"
    return text[:60]


def _short(v, n=160):
    return repr(v)[:n]


def _dump_rows(rows, tag):
    """Print identity + every candidate time field for up to 5 order rows."""
    shown = 0
    for item in rows:
        if item is None:
            continue
        if shown >= 5:
            break
        shown += 1
        ids = " ".join("%s=%s" % (f, _desc(getattr(item, f, "<absent>")))
                       for f in ID_FIELDS if getattr(item, f, None) is not None)
        print("ORD_%s %d %s" % (tag, shown, ids[:220]))
        # Every candidate time field of this row, verbatim -- THE answer.
        for f in TIME_FIELDS:
            print("ORD_%sTIME %s = %s" % (tag, f, _desc(getattr(item, f, "<absent>"))))
    return shown


def _resolve(ContextInfo):
    """One-time account + shape resolution; sets _state, returns True when ready."""
    getter = getattr(ContextInfo, "get_trade_detail_data", None)
    if not callable(getter):
        getter = globals().get("get_trade_detail_data")
    if not callable(getter):
        print("ORD_ABSENT get_trade_detail_data not on ContextInfo nor globals")
        _state["dead"] = True
        return False
    _state["getter"] = getter
    # Account ladder mirrors the bridge production fallback chain.
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
        # v3/v4 cfg fallback (kept for non-sandbox runs only -- the strategy
        # sandbox forbids file IO and has no __file__; see version history).
        try:
            import json
            import os
            cands = (
                os.path.join("C:\\", "qmt", "quant-trading-v2", "qmt_gateway",
                             "config.bridge.json"),
                os.path.join("C:\\", "qmt", "config.bridge.json"),
            )
            cand = None
            for c in cands:
                if os.path.isfile(c):
                    cand = c
                    break
            with open(cand, "rb") as fh:
                cfg = json.loads(fh.read().decode("utf-8-sig", "replace"))
            acct = str(cfg.get("account", "") or "")
            print("ORD_ACCT_CFG file=%s acct=%s" % (_short(cand), acct))
        except Exception as exc:  # noqa: BLE001 - sandbox forbids FileIO
            print("ORD_ACCT_CFG_ERR %s" % repr(exc)[:140])
        if not acct:
            acct = CONST_ACCT
            print("ORD_ACCT_CONST used")
    # Bounded shape ladder: this build's C++ signature is
    # get_trade_detail_data(acct, accounttype, TABLE) (owner 21:28 paste-back).
    for at in ("stock", "STOCK"):
        # Table-name casing ladder: docs show both ORDER and order spellings.
        for tab in ("ORDER", "order"):
            try:
                getter(acct, at, tab)
                _state["acct"] = acct
                _state["shape"] = (at, tab)
                print("ORD_SHAPE_OK accttype=%s table=%s acct=%s" % (at, tab, acct))
                return True
            except Exception as exc:  # noqa: BLE001 - shape errors ARE results
                # every rejected shape stays in the log verbatim
                print("ORD_SHAPE_ERR accttype=%s table=%s %s" % (
                    at, tab, repr(exc)[:120]))
    print("ORD_CALL_ERR all_shapes_failed")
    _state["dead"] = True
    return False


def init(ContextInfo):
    print("ORD_START build=%s" % PROBE_BUILD)


def handlebar(ContextInfo):
    if _state["dead"]:
        return
    # First bar: resolve account + accepted shape once, then wait for orders.
    if _state["getter"] is None:
        if not _resolve(ContextInfo):
            return
    # Watch loop: one counter read per bar; print ONLY when the ORDER table
    # changes (row count or leading order ids), so a whole session is quiet
    # until real orders flow.
    try:
        raw = _state["getter"](_state["acct"], _state["shape"][0], _state["shape"][1])
        rows = list(raw) if raw is not None else []
    except TypeError:
        rows = [raw] if raw is not None else []
    except Exception as exc:  # noqa: BLE001 - mid-session read error is notable
        print("ORD_WATCH_ERR %s" % repr(exc)[:140])
        return
    # Change signature: count + leading identity heads (order ids when present).
    heads = []
    for item in rows[:5]:
        if item is None:
            continue
        heads.append(_desc(getattr(item, "m_strOrderSysID",
                                   getattr(item, "order_id", "?")))[:40])
    sig = (len(rows), tuple(heads))
    if sig == _state["sig"]:
        return
    _state["sig"] = sig
    print("ORD_WATCH rows=%d heads=%s" % (len(rows), ",".join(heads) or "none"))
    if not rows:
        print("ORD_WATCH_EMPTY (counter ORDER table empty -- prints again on the first order)")
        return
    shown = _dump_rows(rows, "ROW")
    if shown:
        # Full field dump complete for this change; no further prints until the
        # next change. Paste every ORD_* line back once the session is done.
        print("ORD_DONE paste_all_ORD_lines_back_then_stop_probe")


def stop(ContextInfo):
    return None
