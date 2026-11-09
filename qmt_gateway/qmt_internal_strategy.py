#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_internal_strategy -- the QMT BUILT-IN strategy that trades on the box.

Carrier decision (owner, 2026-11-04): after the broker's 11-05 ban, the only
automated execution path still inside the broker's own exemption is a strategy
whose signals are produced entirely INSIDE QMT and never talk to the outside
world. So the trading brain of a physical box lives here, in the built-in
strategy, while the ordinary local process (qmt_gateway/local_executor.py) is
demoted to delivery, reading-back and reporting -- it does not place orders.

---------------------------------------------------------------------------
WHAT THIS FILE MAY NOT DO (the broker rule, not a style preference)
---------------------------------------------------------------------------
  * no local file read or write -- including logs, sqlite, JSON config;
  * no network at all: no HTTP/TCP/UDP, not even to localhost;
  * therefore: no imports beyond the standard names the sandbox itself allows,
    and every threshold arrives BAKED into the PARAMS block below.

Consequences that are easy to get wrong and are encoded here on purpose:
  1. IDEMPOTENCY HAS ONE SOURCE: the counter's own same-day ORDER table, read
     with get_trade_detail_data, matched by the short code inside the order
     remark. There is no local journal to fall back on -- if that table cannot
     be read, this round places NOTHING (fail closed: rather skip a trade than
     send the same intent twice).
  2. VERSION SELF-PROOF rides in the remark ("<version>-<short code>"). A
     strategy that cannot reach the network cannot phone home, so "is today's
     build actually running?" is answered by tomorrow's exported order list.
     The local delivery process reads that back and stops auto-updating when
     the version it sees is not the one it installed.
  3. TRADING CALENDAR cannot be read from a file either: the closed-day list is
     baked into PARAMS by the builder (the decision side owns the calendar).
  4. RISK CAPS live here, not outside: the decision side can no longer see the
     intraday state, so per-order amount, daily buy total, remaining quota,
     forbidden windows, the shared place+cancel frequency budget, and the
     suspended / sealed-limit "cannot fill" test all run in this file.

---------------------------------------------------------------------------
HOW THIS FILE IS PRODUCED
---------------------------------------------------------------------------
Do not hand-edit the marker blocks. scripts/build_local_executor.py --target
sandbox assembles this file from three sources and refuses to publish it when
any gate fails:
  * BAKED PARAMS   <- qmt_gateway/local_executor_params.json (single source);
  * SHARED CODE    <- the util + rule + status functions of
                      qmt_gateway/local_executor.py, extracted by AST, so the
                      two carriers can never hold two copies of a threshold;
  * STATUS VOCAB   <- handler.XT_STATUS_CODES and store.TERMINAL_STATUSES,
                      generated into the file at build time (the sandbox cannot
                      import them, but it must not hand-copy them either).
"""

# The only imports this carrier may have: a clock and a digest. Both are pure
# computation -- no file, no socket, no process. They used to be MISSING, which
# made the published artifact raise NameError on the first handlebar while all
# six build gates stayed green: g7 (load smoke) now exists precisely because of
# that shape, and these lines are what it guards.
from datetime import datetime, timedelta, timezone
import hashlib
import time

# ==== BAKED PARAMS BEGIN ====
STRATEGY_VERSION = "v00-00000000"
PARAMS = {}
# ==== BAKED PARAMS END ====

# ==== STATUS VOCAB BEGIN ====
# Generated from the gateway's own tables; the sandbox cannot import them.
XT_STATUS_CODES = {}
TERMINAL_STATUSES = ()
# ==== STATUS VOCAB END ====

# ==== SHARED CODE BEGIN ====
# ==== SHARED CODE END ====


# ==== PORTED RULES BEGIN ====
# The ported rule modules are inlined here by scripts/build_local_executor.py.
# A built-in strategy is one file with no import statement available, so the
# bodies -- not an import -- are the only correct delivery, and the builder
# refuses the build when a module named in params is missing on disk (a strategy
# that silently runs with half its rules is worse than one that fails to load).
_PORTED_RULES = {}
_PORTED_INLINE = {}
# Knob ledger (module -> params keys that module declares it reads), baked by the
# builder from each module's own *_PARAM_KEYS list. The build refuses to publish
# while a declared knob has no value in PARAMS: a rule that cannot read its
# threshold is not "quiet today", it raises on the first call that reaches it.
_PORTED_KNOBS = {}
# ==== PORTED RULES END ====


# ---------------------------------------------------------------------------
# SANDBOX ADAPTER: counter access, the round loop, and the QMT entry points.
# ---------------------------------------------------------------------------

# In-process memory only. It is a CACHE, never the source of truth: everything
# in here is rebuilt from the counter on every round, and a restart simply
# loses the cache (the counter is what proves what was already sent).
_STATE = {
    "acct": "",
    "acct_ok": False,
    "acct_type": "",
    "last_round": 0.0,
    "ops": [],          # [epoch, ...] of place/cancel attempts this session
    "cancel_tried": [],  # order ids already cancelled this session (see below)
    "warned": {},
}


def _log(text):
    """Write one line to the terminal's own output window.

    The sandbox has no file and no socket, so the ONLY place a diagnosis can
    go is the client's log pane. Lines are ASCII: a GBK console turns anything
    else into '?' (this exact failure ate two hours on 2026-11-02).
    """
    print("[local_strategy] %s" % (text,))


def _warn_once(key, text):
    if _STATE["warned"].get(key):
        return
    _STATE["warned"][key] = True
    _log("WARN %s" % (text,))


# The four functions the terminal injects for trading, resolved ONCE through
# bare names. Deliberately no getattr-by-variable and no globals() lookup: the
# build gate (and the person reviewing a file that spends money) must see every
# external name this strategy can call, spelled out. A missing one is recorded
# as None and every caller then declines to act -- "not available" is never
# reported as "no orders to place".
_SANDBOX_FNS = {}


def _resolve_sandbox_fns():
    if _SANDBOX_FNS:
        return _SANDBOX_FNS
    for key in ("passorder", "cancel", "get_trade_detail_data", "get_account"):
        _SANDBOX_FNS[key] = None
    try:
        _SANDBOX_FNS["passorder"] = passorder  # noqa: F821
    except NameError:
        pass
    try:
        _SANDBOX_FNS["cancel"] = cancel  # noqa: F821
    except NameError:
        pass
    try:
        _SANDBOX_FNS["get_trade_detail_data"] = get_trade_detail_data  # noqa: F821
    except NameError:
        pass
    try:
        _SANDBOX_FNS["get_account"] = get_account  # noqa: F821
    except NameError:
        pass
    return _SANDBOX_FNS


def _fn(key):
    """Return one injected sandbox function, or None when this build lacks it."""
    _resolve_sandbox_fns()
    found = _SANDBOX_FNS.get(key)
    return found if callable(found) else None


# The sandbox has no file and no socket, so the funding account id can only
# arrive from the client itself (get_account) or be baked into PARAMS by the
# brain-side build (2026-11-09: this QMT build exposes NO get_account, and the
# retired bridge took its id from a config file the sandbox is forbidden to
# read -- so the baked PARAMS entry is the only reliable path).
#
# A candidate is not guessed, it is PROVEN: the pair (account, type) is accepted
# only when the ACCOUNT table returns a real asset row for it -- the bridge's
# own proof on this box (2026-09-11: rows=1). An empty table proves NOTHING: a
# wrong account id reads empty exactly like a right one, and an empty ORDER
# table of a wrong account is indistinguishable from "nothing sent today",
# which is the double-order hazard this resolution exists to close. So a
# candidate that cannot be proven is REJECTED, not defaulted.
_ACCT_TYPES = ("STOCK", "stock", "8", "10")


def _account_candidates():
    """Ordered candidate funding account ids, best guess first."""
    cands = []

    def _add(value):
        value = str(value or "").strip()
        # "__ACCOUNT__" is the unfilled placeholder: it is a literal that would
        # only ever make the counter calls fail, so it is not a candidate.
        if value and not value.startswith("__") and value not in cands:
            cands.append(value)

    # 1) what the client itself reports for this session (absent on this build)
    getter = _fn("get_account")
    if getter:
        for kind in ("stock", "STOCK"):
            try:
                got = getter(kind)
            except Exception:
                continue
            if isinstance(got, (list, tuple)) and got:
                _add(got[0])
            elif isinstance(got, str):
                _add(got)
    # 2) the account baked into PARAMS by the brain-side build
    _add(PARAMS.get("account", ""))
    return cands


def _probe_account(cand, atype):
    """True when this (account, type) pair reads a REAL asset row."""
    gtdd = _fn("get_trade_detail_data")
    if not gtdd:
        return False
    try:
        rows = gtdd(cand, atype, "ACCOUNT")
    except Exception:
        return False
    return bool(rows)


def _account():
    """Resolve the funding account id AND its account type; cache on SUCCESS only.

    A failed resolution is not cached: the ACCOUNT table can be momentarily
    empty while the session is still syncing (the bridge saw exactly that), and
    caching a failure would fail-close the whole day for a 5-second gap. The
    probe is a cheap counter read, so retrying each round is safe.
    """
    if _STATE["acct_ok"]:
        return _STATE["acct"]
    for cand in _account_candidates():
        for atype in _ACCT_TYPES:
            if not _probe_account(cand, atype):
                continue
            _STATE["acct"] = cand
            _STATE["acct_type"] = atype
            _STATE["acct_ok"] = True
            # Only the tail is logged: the full account id is the box's
            # business and does not belong in a log pane.
            _log("account=resolved tail=%s type=%s" % (cand[-4:], atype))
            return cand
    _warn_once("no_acct_proof", "no candidate account reads a real asset row "
               "(candidates=%d); counter tables stay unreadable" %
               len(_account_candidates()))
    return ""


def _counter_rows(table):
    """Read one counter table -> list of raw rows, or None when unreadable.

    None is not "empty". "Empty" is a real answer (nothing on the counter
    today); "None" means we cannot prove anything, and every caller must treat
    the two differently -- the same distinction that keeps the gateway from
    declaring a missing snapshot a cancelled order.
    """
    gtdd = _fn("get_trade_detail_data")
    if not gtdd:
        _warn_once("no_gtdd", "get_trade_detail_data unavailable")
        return None
    acct = _account()
    if not acct:
        # The diagnosis itself (why no candidate was provable) is what
        # _account() already warned once; repeating it here would only
        # duplicate the line every round.
        return None
    kinds = [_STATE["acct_type"]] if _STATE["acct_type"] else ["stock", "STOCK", "8", "10"]
    for kind in kinds:
        try:
            rows = gtdd(acct, kind, table)
        except Exception:
            continue
        if rows is None:
            continue
        _STATE["acct_type"] = kind
        return list(rows)
    return None


def _pick(*values):
    """Return the first value that is actually there (None when none is).

    Why not getattr(row, name) with a loop variable: the build gate g2 demands a
    LITERAL attribute name at every getattr, because this file places orders and
    a reader must be able to see which counter fields it depends on. So callers
    pass explicit getattr(...) results in priority order and this only picks.
    """
    for value in values:
        if value not in (None, ""):
            return value
    return None


def _live_orders():
    """Today's OWN live orders, normalised -> (rows, ok).

    ok=False means the ORDER table could not be read: the caller must then do
    nothing at all. Rows carrying a foreign remark (no short code we recognise)
    are dropped -- another program's or a hand-placed order is not our asset to
    cancel, and counting it would also charge it against our daily budget.
    """
    raw = _counter_rows("ORDER")
    if raw is None:
        return [], False
    out = []
    codes = XT_STATUS_CODES
    for item in raw:
        remark = str(_pick(getattr(item, "m_strOrderRemark", None), getattr(item, "order_remark", None), getattr(item, "remark", None)) or "")
        short = short_code_from_remark(remark)
        if not short:
            continue
        status, terminal = normalize_order_status(
            _pick(getattr(item, "m_nOrderStatus", None), getattr(item, "order_status", None)), codes, TERMINAL_STATUSES)
        out.append({
            "order_id": str(_pick(getattr(item, "m_strOrderSysID", None), getattr(item, "order_id", None)) or ""),
            "symbol": str(_pick(getattr(item, "m_strInstrumentID", None), getattr(item, "stock_code", None)) or ""),
            "remark": remark,
            "short_code": short,
            "status": status,
            "terminal": terminal,
            "qty": _to_float(_pick(getattr(item, "m_nOrderVolume", None), getattr(item, "order_volume", None)), 0.0),
            "traded": _to_float(_pick(getattr(item, "m_nTradedVolume", None), getattr(item, "traded_volume", None)), 0.0),
            "price": _to_float(_pick(getattr(item, "m_dLimitPrice", None), getattr(item, "price", None)), 0.0),
            "order_time": _pick(getattr(item, "m_nOrderTime", None), getattr(item, "order_time", None)),
        })
    return out, True


def _to_float(value, default):
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def _positions():
    """{symbol: {qty, cost}} from the counter, or None when unreadable."""
    raw = _counter_rows("POSITION")
    if raw is None:
        return None
    out = {}
    for item in raw:
        symbol = str(_pick(getattr(item, "m_strInstrumentID", None), getattr(item, "stock_code", None)) or "")
        if not symbol:
            continue
        out[symbol] = {
            "qty": _to_float(_pick(getattr(item, "m_nVolume", None), getattr(item, "volume", None)), 0.0),
            "cost": _to_float(_pick(getattr(item, "m_dOpenPrice", None), getattr(item, "open_price", None)), 0.0),
        }
    return out


def _cash():
    """Available cash from the counter, or None (None blocks every buy)."""
    raw = _counter_rows("ACCOUNT")
    if raw is None:
        return None
    for item in raw:
        cash = _pick(getattr(item, "m_dAvailable", None), getattr(item, "m_dInstrumentValue", None))
        if cash is None:
            continue
        return _to_float(cash, 0.0)
    return None


def _ops_budget_used():
    """Place/cancel attempts inside the frequency window (session memory)."""
    window = float(PARAMS.get("ops_window_sec", 60))
    cutoff = _now_ts() - window
    _STATE["ops"] = [ts for ts in _STATE["ops"] if ts >= cutoff]
    return len(_STATE["ops"])


def _now_ts():
    """Epoch seconds without importing time at module level.

    The sandbox build we target exposes no clock import that we rely on, so we
    take the epoch from the counter's own tick data when available and fall
    back to a monotone round counter: an unreadable clock makes the stale test
    return None, which the cancel leg treats as "not stale" (unknown is never
    guessed old).
    """
    return time.time()


def _tick_of(ContextInfo, symbol):
    """One snapshot -> the flat dict the rule layer reads, or {} when absent."""
    getter = getattr(ContextInfo, "get_full_tick", None)
    if not callable(getter):
        _warn_once("no_tick", "ContextInfo.get_full_tick unavailable")
        return {}
    try:
        raw = getter([symbol])
    except Exception as exc:
        _warn_once("tick_fail:%s" % symbol, "get_full_tick failed: %s" % repr(exc)[:120])
        return {}
    if not isinstance(raw, dict):
        return {}
    entry = raw.get(symbol)
    if not isinstance(entry, dict):
        return {}
    return normalize_tick(symbol, entry)


def _ref_and_volume(ContextInfo, symbol, tick):
    """Reference price (MA of the baked window) and the volume ratio.

    History comes from ContextInfo.get_market_data_ex when the build exposes
    it. When it does not, or returns nothing, both come back as None: the pullback
    rule then declines instead of inventing a level. No download call happens
    here -- a blocking download inside handlebar is what killed the trading leg
    for a whole day on 2026-10-29.
    """
    getter = getattr(ContextInfo, "get_market_data_ex", None)
    if not callable(getter):
        return None, None
    try:
        bars = getter(["close", "volume"], [symbol], PARAMS.get("ma_period", "1d"),
                      "", "", 3, True, True)
    except Exception as exc:
        _warn_once("hist:%s" % symbol, "get_market_data_ex failed: %s" % repr(exc)[:120])
        return None, None
    series = _series(bars, "close", symbol)
    volumes = _series(bars, "volume", symbol)
    if len(series) < 2:
        return None, None
    history = series[:-1] if len(series) > int(PARAMS.get("ma_window", 1)) else series
    ref = (sum(history[-int(PARAMS.get("ma_window", 1)):]) /
           float(len(history[-int(PARAMS.get("ma_window", 1)):]))) if history else None
    volume = tick.get("volume")
    ratio = None
    past = volumes[:-1] if len(volumes) > 1 else []
    if volume and past:
        average = sum(past) / float(len(past))
        if average:
            ratio = volume / average
    return ref, ratio


def _series(payload, field, symbol):
    """Pull one numeric series out of whatever shape get_market_data_ex returned."""
    if not isinstance(payload, dict):
        return []
    entry = payload.get(symbol, payload)
    if field == "close":
        data = getattr(entry, "close", None)
    elif field == "volume":
        data = getattr(entry, "volume", None)
    else:
        return []
    if data is None and isinstance(entry, dict):
        data = entry.get(field)
    try:
        return [_to_float(item, 0.0) for item in list(data)]
    except TypeError:
        return []


def build_context(ContextInfo, symbol, moment, orders, positions, cash):
    """Assemble the flat decision context for one symbol (counter truth only)."""
    tick = _tick_of(ContextInfo, symbol)
    ref, ratio = _ref_and_volume(ContextInfo, symbol, tick)
    day = day_key(moment)
    held = (positions or {}).get(symbol) or {}
    # Daily spend = what OUR buy intents actually traded on the counter, never an
    # in-memory tally. Which short codes count as "buy" comes from the baked
    # PARAMS (buy_signals x signal_codes), so adding a buy rule is a re-bake,
    # not an edit here.
    codes = PARAMS.get("signal_codes") or {}
    buy_prefixes = set()
    for name in (PARAMS.get("buy_signals") or []):
        code = codes.get(name)
        if code:
            buy_prefixes.add(str(code))
    spent = 0.0
    for row in orders or []:
        short = str(row.get("short_code") or "")
        if not any(short.startswith(prefix) for prefix in buy_prefixes):
            continue
        spent += row.get("traded", 0.0) * row.get("price", 0.0)
    return {
        "symbol": symbol,
        "day": day,
        "weekday": moment.weekday(),
        "closed_days": set(PARAMS.get("closed_days") or []),
        "hhmm": hhmm_now(moment),
        "trading_day": rule_is_trading_day(PARAMS, {
            "weekday": moment.weekday(), "day": day,
            "closed_days": set(PARAMS.get("closed_days") or [])}),
        "last_price": tick.get("last_price"),
        "prev_close": tick.get("prev_close"),
        "volume": tick.get("volume"),
        "ask1": tick.get("ask1"),
        "bid1": tick.get("bid1"),
        "ref_price": ref if ref is not None else tick.get("prev_close"),
        "volume_ratio": ratio,
        "position_qty": held.get("qty"),
        "position_cost": held.get("cost"),
        "available_cash": cash,
        "daily_buy_total": spent,
        "ops_last_minute": _ops_budget_used(),
    }


def _account_total_asset():
    """Total assets from the ACCOUNT table, or None when the counter will not say.

    Only the balance field is trusted; cash comes from _cash(). None is propagated
    on purpose: the sizing chain reads a missing total as "this cap does not
    participate", and the blind-spot cap then tightens the target -- the opposite
    of inventing an equity number and sizing a real order from it.
    """
    raw = _counter_rows("ACCOUNT")
    if raw is None:
        return None
    for item in raw:
        total = _pick(getattr(item, "m_dBalance", None), getattr(item, "total_asset", None),
                      getattr(item, "m_dAssureAsset", None))
        if total is not None:
            return _to_float(total, 0.0)
    return None


def _sizing_view(ctx, positions):
    """The account-level view the sizing chain needs on top of the per-symbol ctx.

    Deliberately absent (and the chain abstains on them, which is the point):
      * sector/cluster buckets -- this box has no sector list and no index weights
        (G3 readout 11-04: get_sector_list / get_index_weight are not exposed), so
        the 35%/35%/40% concentration caps do NOT participate rather than guessing;
      * market dims (emotion phase, breadth, index change) -- not computable from a
        two-code universe; they land the round in the blind spot, which caps the
        target at missing_key_target_cap and stops T. That is the designed
        consequence of "no external data inside the sandbox", not a bug to hide.
    """
    out = dict(ctx or {})
    out["total_asset"] = _account_total_asset()
    live = []
    for symbol, row in (positions or {}).items():
        entry = {"qty": (row or {}).get("qty")}
        price = (row or {}).get("cost")
        if symbol == out.get("symbol"):
            # Only the symbol we actually have a tick for gets a market price;
            # the rest keep cost as the declared basis, and the chain declines the
            # target cap if even that is unreadable (understating room is allowed,
            # overstating it is what the Go side pinned as a defect).
            price = out.get("last_price") or price
        entry["price"] = price
        live.append(entry)
    out["positions_live"] = live
    return out


def sizing_cap(ctx, positions):
    """Cap one buy order with the dynamic-sizing chain. Returns (amount, reason, note).

    amount None means "send nothing", never "no limit" -- that is buy_size_amount's
    own contract and the reason this leg can be trusted to fail closed.
    sizing_enabled=false is the legacy escape hatch (the Go side has the same
    master switch; this box never invents its own default).
    """
    if not bool(PARAMS.get("sizing_enabled", False)):
        return None, "sizing_disabled", ""
    sized = _sizing_view(ctx, positions)
    amount = buy_size_amount(PARAMS, sized)
    if amount is None:
        return None, "sizing_amount_unavailable", ""
    caps = buy_size_caps(PARAMS, sized)
    binding = sorted([k for k, v in caps.items() if v is not None],
                     key=lambda k: caps[k])[:1]
    note = ""
    try:
        note = "%s=%.0f" % (binding[0], float(caps[binding[0]])) if binding else ""
    except Exception:  # noqa: BLE001 - the note is diagnostics, never a decision
        note = ""
    return amount, "", note


def _place(ContextInfo, signal):
    """One passorder attempt ladder. Returns (ok, reason).

    The arity ladder is copied from the working bridge, including the two fixes
    it cost: quickOrder must be 2 (a 7-positional call left it at 0 = K-line
    mode, the client skipped every submission and NOTHING reached the counter),
    and TypeError means "this build's signature differs, try the next shape"
    while any other exception means "the request may have partially landed,
    stop and report" -- retrying that would double-send.
    """
    po = _fn("passorder")
    if not po:
        return False, "passorder unavailable"
    symbol = signal["symbol"]
    head = symbol.split(".")[0]
    suffix = (symbol.split(".")[1].upper() if "." in symbol else "")
    expect = _expect_suffix(symbol)
    if not expect or (suffix and suffix != expect):
        return False, "exchange suffix mismatch: %s" % symbol
    code = "%s.%s" % (head, expect)
    op_buy = int(PARAMS.get("op_type_buy", 23))
    op_sell = int(PARAMS.get("op_type_sell", 24))
    op = op_buy if signal["side"] == "buy" else op_sell
    pr_limit = int(PARAMS.get("price_type_limit", 11))
    acct = _account()
    if not acct:
        # passorder with an empty account id is not a recoverable error: the
        # counter would reject it (or worse, route it nowhere). An unproven
        # account must never reach the order API.
        return False, "account unproven: refusing to passorder"
    remark = build_remark(signal["short_code"])
    args = (op, int(PARAMS.get("order_type", 1101)), acct, code, pr_limit,
            float(signal["price"]), int(signal["qty"]))
    attempts = [
        args + ("local_strategy", 2, remark, ContextInfo),
        args + ("local_strategy", 2, remark),
        args + ("local_strategy", 1, remark, ContextInfo),
        args + ("local_strategy", 1, remark),
        args,
    ]
    last = None
    for shape in attempts:
        try:
            po(*shape)
        except TypeError as exc:
            last = exc
            continue
        except Exception as exc:
            return False, "passorder error: %s" % repr(exc)[:120]
        _STATE["ops"].append(_now_ts())
        _log("placed side=%s %s qty=%s remark=%s" % (
            signal["side"], ascii(code), signal["qty"], ascii(remark)))
        return True, "ok"
    return False, "passorder arity fail: %s" % repr(last)[:120]


def _expect_suffix(symbol):
    """SH for Shanghai codes, SZ for Shenzhen, '' for anything we must not send.

    Same rule as the bridge: a wrong suffix means the signal was assembled
    wrong upstream, so we refuse rather than let the counter reject it later.
    """
    head = str(symbol or "").split(".")[0]
    if not head:
        return ""
    first = head[0]
    if first in ("6", "9", "5"):
        return "SH"
    if first in ("0", "2", "3", "1"):
        return "SZ"
    return ""


def _cancel(order_id):
    """Cancel ladder, again copied from the working bridge.

    Documented shape is cancel(order_id:str, account_id, account_type,
    ContextInfo); the numeric two-arg shape is attempted only when the id is
    genuinely numeric -- building int(oid) up front once raised ValueError
    before any documented form ran, and a live order stayed live (10-29).
    """
    ca = _fn("cancel")
    if not ca:
        return False, "cancel unavailable"
    oid = str(order_id or "").strip()
    if not oid:
        return False, "cancel needs an order id"
    acct = _account()
    if not acct:
        return False, "account unproven: refusing to cancel"
    attempts = [("4s", (oid, acct, "STOCK")), ("4l", (oid, acct, "stock")),
                ("3", (oid, acct, "STOCK")), ("2s", (oid, acct))]
    errs = []
    for tag, shape in attempts:
        try:
            ca(*shape)
            _STATE["ops"].append(_now_ts())
            _log("cancelled via %s id=%s" % (tag, ascii(oid)))
            return True, "ok"
        except Exception as exc:
            errs.append("%s:%s" % (tag, repr(exc)[:60]))
    try:
        ca(int(oid), acct)
        _STATE["ops"].append(_now_ts())
        return True, "ok"
    except Exception as exc:
        errs.append("2i:%s" % repr(exc)[:60])
    return False, "cancel failed: %s" % (" | ".join(errs[-2:]),)


def run_round(ContextInfo, moment):
    """One round: rebuild from the counter, decide, then act. Returns a summary.

    Never raises. A round that cannot see the counter places nothing -- the
    same fail-closed posture as the local executor, and for the same reason:
    "we cannot prove we have not already sent this" is not a licence to send.
    """
    summary = {"placed": [], "skipped": [], "cancelled": [], "reason": ""}
    orders, ok = _live_orders()
    if not ok:
        summary["reason"] = "counter_order_table_unreadable"
        _warn_once("order_table", summary["reason"])
        return summary
    sent = set(row["short_code"] for row in orders)
    positions = _positions()
    cash = _cash()
    if positions is None:
        # Fail-closed on the SELL side only: without the position table the exit
        # rules have no qty, so they decline (they never size from memory). Buys
        # stay possible because their caps are cash + the counter order table.
        summary["reason"] = "counter_position_table_unreadable"
        _warn_once("position_table", "POSITION table unreadable: exits decline this round")
    for symbol in PARAMS.get("symbols") or []:
        ctx = build_context(ContextInfo, symbol, moment, orders, positions, cash)
        signals = []
        for rule in (rule_take_profit, rule_stop_loss, rule_breakdown_sell):
            hit = rule(PARAMS, ctx)
            if hit:
                signals = [(hit, "live")]
                break
        if not signals:
            hit = rule_pullback_buy(PARAMS, ctx)
            if hit:
                signals = [(hit, "live")]
        if not signals:
            # Ported rules only get a turn when nothing live fired: the builtin
            # four ARE today's behaviour, and a ported rule must not pre-empt
            # them on the day it is armed.
            for kind in sorted(_PORTED_RULES):
                fn, _src = _PORTED_RULES[kind]
                try:
                    hit = fn(PARAMS, ctx)
                except Exception as exc:  # noqa: BLE001
                    # A rule that raises is not a rule that found nothing: name it.
                    _warn_once("rule_raise:%s" % kind, "rule %s raised %s" % (kind, repr(exc)[:100]))
                    continue
                if hit:
                    signals.append((hit, rule_arm_mode(PARAMS, kind)))
        for signal, mode in signals:
            if mode != "live":
                # shadow/off: counted in the round summary, never sent. The
                # >=5-day shadow window against the engine is the price of
                # porting a re-calibrated threshold; spending during it is not.
                summary["skipped"].append({"short": signal["short_code"],
                                           "reason": "rule_" + mode})
                continue
            short = signal["short_code"]
            if short in sent:
                summary["skipped"].append({"short": short, "reason": "already_sent"})
                continue
            allowed, why = rule_no_new_order_window(PARAMS, ctx)
            if not allowed:
                summary["skipped"].append({"short": short, "reason": why})
                continue
            if not rule_ops_frequency_ok(PARAMS, ctx):
                summary["skipped"].append({"short": short, "reason": "ops_frequency_exceeded"})
                continue
            fillable, why = rule_fillable(PARAMS, ctx, signal["side"])
            if not fillable:
                summary["skipped"].append({"short": short, "reason": why})
                continue
            if signal["side"] == "buy":
                blocked = (not rule_cash_ok(PARAMS, ctx, signal["amount"]) or
                           not rule_amount_cap_ok(PARAMS, signal["amount"]) or
                           not rule_daily_buy_cap_ok(PARAMS, ctx, signal["amount"]))
                if blocked:
                    summary["skipped"].append({"short": short, "reason": "money_cap"})
                    continue
                # DYN-SIZE-IN-SANDBOX (owner 11-04: the dynamic position model must run
                # inside QMT today). The chain is ANDed with the caps above, never a
                # replacement for them: the sizing cap can only make this order smaller.
                sized_amount, why, note = sizing_cap(ctx, positions)
                if sized_amount is None:
                    summary["skipped"].append({"short": short, "reason": why})
                    continue
                if sized_amount < signal.get("amount", 0.0):
                    price = signal.get("price") or ctx.get("last_price")
                    lot = float(PARAMS.get("lot_size") or 0.0)
                    if not price or lot <= 0.0:
                        # Cannot re-cut the share count without a price and a lot size:
                        # sending the ORIGINAL amount would silently trade around the cap.
                        summary["skipped"].append({"short": short, "reason": "sizing_no_price"})
                        continue
                    qty = int(sized_amount / price / lot) * int(lot)
                    if qty <= 0:
                        summary["skipped"].append({"short": short, "reason": "sizing_below_lot"})
                        continue
                    signal = dict(signal)
                    signal["qty"] = qty
                    signal["amount"] = round(qty * price, 2)
                    summary.setdefault("sized", []).append(
                        {"short": short, "qty": qty, "amount": signal["amount"], "cap": note})
            ok, why = _place(ContextInfo, signal)
            if ok:
                sent.add(short)
                summary["placed"].append(short)
            else:
                summary["skipped"].append({"short": short, "reason": why})
    cancelled = cancel_leg(orders, moment)
    summary["cancelled"] = cancelled
    return summary


def cancel_leg(orders, moment):
    """Stale-cancel + close sweep, with the same seven fail-closed gates as
    the local executor's leg (see local_executor.Executor.cancel_leg).

    Only difference forced by the sandbox: "already cancelled" cannot be
    journaled to disk, so it is remembered for the session. After a restart the
    counter is what proves it -- a cancelled row leaves the live set, so the
    rule does not depend on the cache.
    """
    hhmm = hhmm_now(moment)
    ctx = {"trading_day": rule_is_trading_day(PARAMS, {
        "weekday": moment.weekday(), "day": day_key(moment),
        "closed_days": set(PARAMS.get("closed_days") or [])}),
        "hhmm": hhmm, "ops_last_minute": _ops_budget_used()}
    allowed, reason = rule_no_cancel_window(PARAMS, ctx)
    if not allowed:
        return []
    sweep_from = PARAMS.get("close_sweep_from")
    stale_sec = float(PARAMS.get("stale_order_sec"))
    done = []
    now = _now_ts()
    for row in orders:
        oid = row.get("order_id") or ""
        if not oid or oid in _STATE["cancel_tried"]:
            continue
        if row.get("terminal") is not False:
            continue  # terminal, or unknown: do not touch
        # Age baseline: a missing order_time is UNKNOWN age, never "old enough".
        age = None
        try:
            if row.get("order_time") is not None:
                age = now - float(row.get("order_time"))
        except (TypeError, ValueError):
            age = None
        stale = age is not None and age >= stale_sec
        sweep = bool(sweep_from) and hhmm >= sweep_from
        if not (stale or sweep):
            continue
        if _ops_budget_used() >= int(PARAMS.get("max_ops_per_min")):
            _warn_once("cancel_budget", "cancel leg out of frequency budget")
            break
        ok, why = _cancel(oid)
        if ok:
            _STATE["cancel_tried"].append(oid)
            done.append(oid)
        else:
            _log("cancel failed id=%s: %s" % (ascii(oid), why))
    return done


def init(ContextInfo):
    """QMT entry: subscribe the baked universe and prove the trade surface once.

    No file is read and no config is fetched -- everything is baked. This line
    in the client log is also the delivery receipt: the local updater compares
    the version printed here against the version it installed.
    """
    _log("init version=%s params_digest=%s universe=%d" % (
        STRATEGY_VERSION, text_sha256(repr(sorted(PARAMS.items())))[:12],
        len(PARAMS.get("symbols") or [])))
    counter_api = _fn("get_trade_detail_data")
    _log("counter_api=%s passorder_api=%s cancel_api=%s" % (
        bool(counter_api), bool(_fn("passorder")), bool(_fn("cancel"))))
    if not counter_api or not _fn("passorder"):
        _log("WARN trade surface unavailable: this build will place nothing")


def handlebar(ContextInfo):
    """QMT per-bar entry: run at most one round per poll_interval_sec.

    The throttle exists because handlebar fires on every bar of every symbol in
    the universe; without it one clock second would spend the frequency budget
    on no-op rounds. A round that raises is caught here and logged: the loop
    must survive, and an exception must never be reported as a trading result.
    """
    try:
        moment = now_cn()
    except Exception:
        return
    interval = float(PARAMS.get("poll_interval_sec", 5))
    if _now_ts() - _STATE["last_round"] < interval:
        return
    _STATE["last_round"] = _now_ts()
    try:
        summary = run_round(ContextInfo, moment)
    except Exception as exc:
        _log("round error swallowed: %s" % repr(exc)[:160])
        return
    if summary.get("placed") or summary.get("cancelled") or summary.get("reason"):
        _log("round placed=%s cancelled=%s reason=%s" % (
            summary.get("placed"), summary.get("cancelled"), summary.get("reason")))


if __name__ == "__main__":
    # The sandbox never runs this branch; the local test harness imports the
    # module and calls run_round with a fake ContextInfo.
    print("local strategy is loaded by the QMT terminal, not run standalone")
