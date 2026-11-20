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

# The only imports this carrier may have: a clock, a digest and math. All are
# pure computation -- no file, no socket, no process. They used to be MISSING,
# which made the published artifact raise NameError on the first handlebar
# while all six build gates stayed green: g7 (load smoke) now exists precisely
# because of that shape, and these lines are what it guards.
# RULE-MATH-20261112: `math` was missing AGAIN in a different shape -- the
# ported rule bodies inlined by the builder (rules_factor.py lineage) call
# math.log/math.sqrt, but the inline delivers bodies, not imports, so every
# factor_composite / pattern_template call raised NameError and the whole
# ported-rule layer went silent on the box (live 2026-11-12). One import here
# covers every inlined body; the sandbox cannot import modules itself.
from datetime import datetime, timedelta, timezone
import hashlib
import math
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
    # 2026-11-10 rebuild (PLAN_CARRIER_RISK_REBUILD section 1.3): the counter
    # ORDER table alone is a SECONDS-late idempotency source, which is how the
    # same intent was re-sent in the open window. Every successful place now
    # registers HERE immediately, and buys are deduped per (day, symbol) too:
    # one buy intent per symbol per day, regardless of short code.
    "sent": set(),       # {(day, symbol, short_code), ...} placed this session
    "buy_intents": set(),  # {(day, symbol), ...} buy intents sent this session
    "buy_amounts": {},   # {(day, symbol): amount} session buy total per symbol
    "buy_seen": {},      # {symbol: [epoch, ...]} Q1 confirm-window rounds
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


def _digits(value):
    """Keep only ASCII digits of a string (no regex, sandbox-allowed)."""
    return "".join(ch for ch in str(value) if ch.isdigit())


def _order_submit_epoch(item):
    """Submit time of one order as epoch SECONDS, or None (unknown => never stale).

    STALE-CANCEL-FIX (owner report 2026-11-17). The counter exposes the submit
    stamp as m_strInsertDate + m_strInsertTime STRINGS (the bridge reads exactly
    this pair -- see qmt_bridge_strategy.embed_orders). The old sandbox code read
    `m_nOrderTime`/`order_time` and float()'d it; this counter never fills that,
    so order_time stayed None -> age stayed None -> the stale test never fired ->
    unfilled buy/sell orders were only ever cleared by the 14:50 close sweep,
    pinning capital all afternoon (the log shows cancelled=[] on EVERY round).

    The sandbox cannot construct a datetime from parts (g2 allows only
    datetime.now), so we build the epoch with pure arithmetic against this
    round's clock: today's Beijing-midnight epoch = now - seconds_of_day(now),
    then add the order's own seconds-of-day. Orders are intraday by the session
    gate, so they share today's date with now -- the result is a correct epoch
    that both cancel_leg and rules_t (now_ts - order_time) consume unchanged. If
    the build hands an epoch-like m_nOrderTime (>1e9) we use it directly.
    Unparseable -> None: unknown age is never guessed "old enough" (fail-safe --
    we may under-cancel but never wrongly cancel a live order).
    """
    n = _pick(getattr(item, "m_nOrderTime", None), getattr(item, "order_time", None))
    try:
        nf = float(n) if n is not None else 0.0
    except (TypeError, ValueError):
        nf = 0.0
    if nf > 1000000000.0:  # already epoch seconds on this counter build
        return nf
    t = _digits(_pick(getattr(item, "m_strInsertTime", None), "") or "")
    if len(t) < 4:
        return None  # no usable submit clock -> unknown age -> never stale
    try:
        hh = int(t[0:2])
        mi = int(t[2:4])
        ss = int(t[4:6]) if len(t) >= 6 else 0
    except (ValueError, TypeError):
        return None
    order_sod = hh * 3600 + mi * 60 + ss  # seconds of day (Beijing wall clock)
    now = _now_ts()
    now_sod = (now + 28800.0) % 86400.0
    midnight = now - now_sod
    return midnight + float(order_sod)


def _int_or(value, default=None):
    """int(value) or default on TypeError/ValueError/None (never raises)."""
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def _order_side(item, op_buy=23, op_sell=24):
    """One ORDER row's side: "buy" / "sell" / "" when unproven.

    Same enum space the retired bridge proved on this box
    (qmt_bridge_strategy.py:300-390): m_nDirection 48/0=buy 49/1=sell,
    m_nOffsetFlag 48=buy 50=sell, m_nOrderType 23/1101=buy 24/1102=sell.
    Reading the side is what lets pending BUY orders reserve budget without
    charging SELL orders against cash (a sell frees cash, it never spends it).
    An unproven side stays "" and the caller treats it as a buy -- over-
    reserving money is the fail-closed direction.
    """
    votes = set()
    # Build gate g2 forbids getattr(item, <loop variable>): every attribute name
    # must be a literal so a reader can see exactly what this file depends on.
    direction = _int_or(getattr(item, "m_nDirection", None))
    if direction in (0, 48):
        votes.add("buy")
    elif direction in (1, 49, 50):
        votes.add("sell")
    offset = _int_or(getattr(item, "m_nOffsetFlag", None))
    if offset == 48:
        votes.add("buy")
    elif offset == 50:
        votes.add("sell")
    offset_type = _int_or(getattr(item, "offset_type", None))
    if offset_type == 48:
        votes.add("buy")
    elif offset_type == 50:
        votes.add("sell")
    if len(votes) == 1:
        return votes.pop()
    otype = _int_or(getattr(item, "m_nOrderType", None))
    if otype is None:
        otype = _int_or(getattr(item, "order_type", None))
    if otype in (23, 1101) or (otype is not None and otype == op_buy):
        return "buy"
    if otype in (24, 1102) or (otype is not None and otype == op_sell):
        return "sell"
    return ""


def _live_orders():
    """Today's OWN live orders, normalised -> (rows, ok).

    ok=False means the ORDER table could not be read: the caller must then do
    nothing at all. Rows carrying a foreign remark (no short code we recognise)
    are dropped -- another program's or a hand-placed order is not our asset to
    cancel, and counting it would also charge it against our daily budget.

    2026-11-19 P0: the remark field order was WRONG. The proven shape on this
    counter is m_strRemark (qmt_bridge_strategy.py:714, the bridge that traded
    this account for months); this file asked m_strOrderRemark first and never
    looked at m_strRemark at all. Every ORDER row therefore read remark="",
    short_code_from_remark returned "", and the whole table was dropped as
    "foreign" -- the live log printed `views pos=N ord=0` on all 279 rounds of
    2026-11-19 (101 rounds on 11-18) while five buy orders sat working on the
    counter. Two things died with it: pending buys never reserved any budget
    (target room stayed wide open) and already_sent/stale-cancel had no counter
    evidence at all. `order_raw`/`order_kept` are kept for the next diagnosis:
    raw>0 with kept=0 is a field-name drift, raw=0 is a genuinely empty table.
    """
    raw = _counter_rows("ORDER")
    if raw is None:
        return [], False
    _STATE["order_raw"] = len(raw)
    out = []
    codes = XT_STATUS_CODES
    op_buy = _int_or(PARAMS.get("op_type_buy"), 23)
    op_sell = _int_or(PARAMS.get("op_type_sell"), 24)
    for item in raw:
        remark = str(_pick(getattr(item, "m_strRemark", None),
                           getattr(item, "m_strOrderRemark", None),
                           getattr(item, "order_remark", None),
                           getattr(item, "remark", None)) or "")
        short = short_code_from_remark(remark)
        if not short:
            continue
        status, terminal = normalize_order_status(
            _pick(getattr(item, "m_nOrderStatus", None), getattr(item, "order_status", None)), codes, TERMINAL_STATUSES)
        # 2026-11-19 P0 (live proof, second half): the field families here must
        # match the ONE build that has actually traded this account for months --
        # qmt_bridge_strategy.py:707-715. It reads
        #     qty    m_nOrderVolume / order_volume / volume
        #     filled m_nVolumeTraded / traded_volume / filled_volume
        #     price  m_dOrderPrice / m_dPrice / price
        # This file asked m_nTradedVolume (not a field on this counter) and
        # m_dLimitPrice. Both came back 0, which is not a harmless readout: a
        # FILLED order then looks exactly like "terminal with zero fill"
        # (cancel_like), so (a) it is dropped from counter_codes and the same
        # short code is re-bought as soon as session memory resets, and (b) its
        # money never counts against the day budget. Observed live: 10:01
        # FC000034/FC000690 filled 900/1700, then the 10:45 restart re-sent the
        # same two names at 10:46 (100/300) because the filled rows looked
        # cancelled. Fail direction is deliberately "unfilled": an unreadable
        # traded count reserves money it may not need, never spends it twice.
        traded = _to_float(_pick(getattr(item, "m_nVolumeTraded", None),
                                 getattr(item, "traded_volume", None),
                                 getattr(item, "filled_volume", None)), 0.0)
        out.append({
            "order_id": str(_pick(getattr(item, "m_strOrderSysID", None), getattr(item, "order_id", None)) or ""),
            "symbol": str(_pick(getattr(item, "m_strInstrumentID", None), getattr(item, "stock_code", None)) or ""),
            "remark": remark,
            "short_code": short,
            "side": _order_side(item, op_buy, op_sell),
            "status": status,
            "terminal": terminal,
            "qty": _to_float(_pick(getattr(item, "m_nOrderVolume", None),
                                   getattr(item, "order_volume", None),
                                   getattr(item, "volume", None)), 0.0),
            "traded": traded,
            "price": _to_float(_pick(getattr(item, "m_dOrderPrice", None),
                                     getattr(item, "m_dPrice", None),
                                     getattr(item, "m_dLimitPrice", None),
                                     getattr(item, "price", None)), 0.0),
            "order_time": _order_submit_epoch(item),
            "cancel_like": bool(terminal and traded <= 0.0),
        })
    _STATE["order_kept"] = len(out)
    if raw and not out:
        # raw rows but not one carries our wire remark: a field-name drift, not
        # an empty table. Print the candidate values once (literal attributes
        # only -- build gate g2 forbids dir()/getattr-with-a-variable) so the
        # next build is a grep instead of another guess.
        first = raw[0]
        _warn_once("order_remark_drift",
                   "ORDER table has %d rows but no remark parsed: m_strRemark=%s "
                   "m_strOrderRemark=%s order_remark=%s remark=%s status=%s" % (
                       len(raw),
                       repr(_pick(getattr(first, "m_strRemark", None))),
                       repr(_pick(getattr(first, "m_strOrderRemark", None))),
                       repr(_pick(getattr(first, "order_remark", None))),
                       repr(_pick(getattr(first, "remark", None))),
                       repr(_pick(getattr(first, "m_nOrderStatus", None),
                                  getattr(first, "order_status", None)))))
    return out, True


def _to_float(value, default):
    try:
        return float(value)
    except (TypeError, ValueError):
        return default


def _t_equity_basis(total, macro, stale_sec):
    """T cap-base equity: the counter reading, else a FRESH baked fallback.

    T-EQUITY-FALLBACK 2026-11-17: this broker's ACCOUNT table does not fill
    m_dBalance intraday, so `total` (from _account_total_asset) is None and
    t_cap_base has no denominator -> T dies all day (no quota denominator).
    Fall back to the outer-brain-baked total_asset in t_macro. Rules:

      * counter reading ALWAYS wins (never override a real number with a bake);
      * the baked value must be positive and carry a non-stale asof_age_sec,
        else return None (fail closed -- never size a T leg off old equity);
      * freshness window is the shared t_regime_stale_sec; a missing age or a
        non-positive window refuses the fallback.

    Pure and independently testable; this is used ONLY for the T ctx, so the
    strategic buy sizing / cash-reserve paths are byte-for-byte untouched.
    """
    if total is not None:
        t = _to_float(total, None)
        return t if t is not None and t > 0.0 else None
    _macro = macro if isinstance(macro, dict) else {}
    baked = _to_float(_macro.get("total_asset"), None)
    if baked is None or baked <= 0.0:
        _warn_once("t_equity_no_denominator",
                   "no counter equity and no baked total_asset: T stays closed")
        return None
    window = _to_float(stale_sec, 0.0)
    if window is None or window <= 0.0:
        return None  # no defined freshness window -> do not trust a baked number
    age = _to_float(_macro.get("asof_age_sec"), None)
    if age is None:
        _warn_once("t_equity_baked_age", "baked total_asset has no age: fail closed")
        return None
    if age > window:
        _warn_once("t_equity_baked_stale", "baked total_asset is stale: fail closed")
        return None
    return baked


def _positions():
    """{symbol: {qty, cost, can_use, market_value}} or None when unreadable.

    2026-11-10 rebuild: can_use (m_nCanUseVolume, the T+1 sellable qty) and
    market_value (m_dMarketValue) used to be left unread, which blinded both
    the sell chain (no T+1 check) and the concentration buckets (no mark-to-
    market value). Missing optional fields stay None -- callers fail closed
    on None, they never fold it into zero.
    """
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
            "can_use": _to_float(_pick(getattr(item, "m_nCanUseVolume", None),
                                       getattr(item, "can_use_volume", None)), 0.0),
            "market_value": _to_float(_pick(getattr(item, "m_dMarketValue", None),
                                            getattr(item, "market_value", None)), 0.0),
        }
    return out


def _deals():
    """Today's fills from the DEAL table, or None when unreadable.

    The day-loss fuse (account truth day_loss_limit_pct=3.5) needs REALIZED
    pnl, and realized pnl only exists in fills -- the ORDER table never carries
    it. Field families are probed one by one exactly like the bridge did
    (qmt_bridge_strategy.py embed_trades_rows); direction is resolved by the
    op codes baked in PARAMS, never by one ambiguous enum field.
    """
    raw = _counter_rows("DEAL")
    if raw is None:
        return None
    op_buy = int(PARAMS.get("op_type_buy", 23))
    op_sell = int(PARAMS.get("op_type_sell", 24))
    out = []
    for item in raw:
        symbol = str(_pick(getattr(item, "m_strInstrumentID", None),
                           getattr(item, "stock_code", None),
                           getattr(item, "m_strCode", None)) or "")
        price = _to_float(_pick(getattr(item, "m_dPrice", None),
                                getattr(item, "traded_price", None)), 0.0)
        qty = _to_float(_pick(getattr(item, "m_nVolume", None),
                              getattr(item, "traded_volume", None)), 0.0)
        amount = _to_float(_pick(getattr(item, "m_dTradeAmount", None),
                                 getattr(item, "amount", None)), 0.0)
        if amount <= 0.0:
            amount = price * qty
        otype = _to_float(_pick(getattr(item, "m_nOrderType", None),
                                getattr(item, "order_type", None)), 0.0)
        if int(otype) == op_sell:
            side = "sell"
        elif int(otype) == op_buy:
            side = "buy"
        else:
            side = ""
        out.append({"symbol": symbol, "side": side, "price": price,
                    "qty": qty, "amount": amount})
    return out


def _vwap_of(tick):
    """Session VWAP approximated from the tick's cumulative amount/volume.

    Owner ruling (Q6): this approximation is accepted; volume<=0 or a reading
    that blatantly disagrees with the last price is VWAP MISSING, and a missing
    VWAP skips the buy (the same fail-closed posture as the Go side's
    autoPlaceScaled). Returns (vwap_or_None, reason).
    """
    volume = tick.get("volume")
    amount = tick.get("amount")
    last = tick.get("last_price")
    if not volume or volume <= 0.0 or not amount or amount <= 0.0:
        return None, "vwap_missing"
    vwap = amount / volume
    if not last or last <= 0.0:
        return None, "vwap_missing"
    if vwap < last * float(PARAMS.get("vwap_sanity_lo", 0.5)) or \
            vwap > last * float(PARAMS.get("vwap_sanity_hi", 2.0)):
        # 2026-11-11 owner: consecutive vwap_suspect in round_skips. QMT builds
        # disagree on amount/volume units (volume in lots vs shares, amount in
        # yuan vs wan). Data-driven unit auto-correction: divide by each baked
        # candidate and accept ONLY the value that lands inside the same sanity
        # band (the existing gate arbitrates; nothing is invented). If none
        # lands, keep suspect (fail-closed). Candidates come from PARAMS.
        for mult in PARAMS.get("vwap_unit_candidates") or []:
            try:
                corrected = vwap / float(mult)
            except (TypeError, ZeroDivisionError):
                continue
            if last * float(PARAMS.get("vwap_sanity_lo", 0.5)) <= corrected <= \
                    last * float(PARAMS.get("vwap_sanity_hi", 2.0)):
                return corrected, ""
        return None, "vwap_suspect"
    return vwap, ""


def _risk_reason(symbol):
    """Reject reason when the symbol carries a baked risk tag, else "".

    Q3 final: the static floor (000520 + whatever else the owner bakes) never
    expires, the daily LLM list is baked fresh every morning and is same-day
    valid. A hit is a HARD exclude -- no down-weighting, reason=llm_bearish
    for dynamic rows so the log pane says why.
    """
    head = symbol.split(".")[0]
    for entry in (PARAMS.get("risk_list_static") or []):
        if str(entry).split(".")[0] == head:
            return "static_risk_list"
    for entry in (PARAMS.get("risk_list_dynamic") or []):
        if str(entry).split(".")[0] == head:
            return "llm_bearish"
    return ""


def _buy_confirmed(symbol, now_ts):
    """Q1 confirm window: a buy needs N observed rounds spaced >= gap seconds.

    Session memory only -- after a restart the count starts over (better to
    miss an entry than to front-run it). Returns (ok, reason).
    """
    rounds = int(PARAMS.get("buy_confirm_rounds", 2))
    gap = float(PARAMS.get("buy_confirm_min_gap_sec", 60))
    seen = _STATE["buy_seen"].setdefault(symbol, [])
    # CONFIRM-FIX-20261112: the old code appended EVERY round's timestamp, so
    # with the ~6s round cadence the last two observations were always ~6s
    # apart and gap=60 could never pass -- 67 live intents sat in
    # confirm_gap_pending forever (14:17 box log). Fixed: only record an
    # observation once the gap since the LAST RECORDED one has elapsed; each
    # recorded observation then advances the count.
    if seen and now_ts - seen[-1] < gap:
        return False, "confirm_gap_pending"
    seen.append(now_ts)
    del seen[:-rounds]
    if len(seen) < rounds:
        return False, "confirm_rounds_%d_of_%d" % (len(seen), rounds)
    if seen[-1] - seen[-2] < gap:
        return False, "confirm_gap_pending"
    return True, ""


def _go_admitted(kind, rule_id=""):
    """Runtime whitelist check, wired from run_round (GO-ADMIT-WIRING 2026-11-18).

    The 2026-11-10 build shipped this helper with ZERO call sites while its
    docstring already claimed the defect closed -- the audit on 11-18 proved it
    with an AST LOAD-count of []. This is now the REAL single entry: run_round's
    ported-rule loop calls it for every live BUY, and the semantics match
    local_executor.decide_with_modes word for word:
      * rule_id first (factor library signals carry their own fac_* id),
      * kind fallback via the baked strategy_kind_to_go table,
      * factor/pattern prefixed kinds pass through verbatim,
      * an unmappable/unattributable rule is REFUSED -- a rule nobody can
        attribute must never become a live order.
    SELL signals never reach this check (the caller exempts them): the Go
    whitelist gates only the BUY dispatch (scoring_loop.go dispatchLive takes
    buys; exits run the sell-judge/fuse chain), so gating sells here would
    blind every live exit leg.
    """
    go_id = str(rule_id or "")
    if not go_id:
        go_id = (PARAMS.get("strategy_kind_to_go") or {}).get(kind)
    if not go_id:
        if str(kind).startswith(str(PARAMS.get("factor_id_prefix", "fac_"))):
            go_id = kind
        elif str(kind).startswith(str(PARAMS.get("pattern_id_prefix", "pat_"))):
            go_id = kind
    if not go_id:
        return False
    try:
        return bool(rule_strategy_admit(PARAMS, go_id))
    except Exception:  # noqa: BLE001 - an unreadable whitelist refuses, never admits
        return False


def _intent_of(signal, held_qty):
    """Explicit intent tag (owner 11:05 ruling -- never inferred from size).

    position_qty>0 => add, ==0 => new; sell => reduce; a t-prefixed short code
    => t_leg. The min-order-amount gate reads ONLY this tag.
    """
    short = str(signal.get("short_code") or "")
    if short.startswith("t0:") or short.startswith("t:"):
        return "t_leg"
    if signal.get("side") == "sell":
        return "reduce"
    if (held_qty or 0.0) > 0.0:
        return "add"
    return "new"


def _day_loss_pct(deals, positions):
    """Realized day pnl as a fraction of nothing yet -- returns (pnl, ok).

    Sell fills booked against the position's open price; a sell whose cost is
    unreadable makes the whole reading UNPROVABLE (ok=False) and the caller
    stops buying for the day rather than guessing the account is fine.
    """
    if deals is None:
        return None, False
    pnl = 0.0
    for row in deals:
        if row.get("side") != "sell":
            continue
        held = (positions or {}).get(row.get("symbol") or "") or {}
        cost = held.get("cost")
        if not cost or cost <= 0.0:
            return None, False
        pnl += (row.get("price", 0.0) - cost) * row.get("qty", 0.0)
    return pnl, True


def _asset_guard_ok(total, cash, positions):
    """Dimension guard on the total-asset reading (the 11-06 pollution).

    From 11-06 the exported target jumped from ~70k to 4-7M because a polluted
    total inflated room to infinity. The counter itself provides the sanity
    check: total must cover both the cash and the held value, and must stay
    within asset_guard_factor of them. An implausible total refuses every buy.
    """
    if not total or total <= 0.0:
        return False
    mv = 0.0
    for _sym, row in (positions or {}).items():
        value = (row or {}).get("market_value") or 0.0
        if value <= 0.0:
            value = ((row or {}).get("qty") or 0.0) * ((row or {}).get("cost") or 0.0)
        mv += value
    floor = max(cash or 0.0, mv)
    if total < floor:
        return False
    factor = float(PARAMS.get("asset_guard_factor", 10.0))
    return total <= floor * factor + (cash or 0.0) + mv


def _bare(code):
    """Bare 6-digit head of a code ("002093.SZ" -> "002093")."""
    return str(code or "").split(".")[0]


def _bucket_view(symbol, positions, tick, session_buy_amount, pending_buy_amount=0.0):
    """Fill the concentration buckets the sizing chain used to abstain on.

    stock_value is ALWAYS computed (THIS symbol's held value + this symbol's
    pending/session buys); industry/cluster are known only when the baked
    ownership maps carry the symbol -- an unknown bucket refuses the BUY at the
    gate (fail closed), so here the unknown simply reports known=False.

    2026-11-19 P0 (owner ruling "the single-name cap is about THAT name only"):
    stock_value used to sum EVERY position in the book. The 09:59:55 probe line
    proved it arithmetically -- equity 99156 x 35% = 34705, minus the whole book
    (one 002093 row at 33859) left `single_stock=846`, so every NEW name was
    refused with sizing_amount_unavailable (813 of 938 skips that round) until
    the 002093 position happened to be sold at 10:01:20. Go has always been per
    code (internal/engine/engine.go:2058 StockValue: stockVal[pureTsCode(code)],
    internal/paper/paper.go:1765-1773) and docs/FIX_PLAN_VWAP_DYNSIZE_20261026.md
    :401 says the single-name cap is a concentration backstop, NOT a total-
    position control -- the total-position brake is target_room.

    The industry/cluster legs stay whole-book on purpose: those caps are about
    the GROUP, not one name.
    """
    price = tick.get("last_price")
    target = _bare(symbol)
    own_buy = float(session_buy_amount or 0.0)
    if float(pending_buy_amount or 0.0) > own_buy:
        # Same-round sends live only in session memory; counter-visible pending
        # buys survive a restart. max() keeps both from double-counting.
        own_buy = float(pending_buy_amount or 0.0)
    stock_value = own_buy
    for sym, row in (positions or {}).items():
        qty = (row or {}).get("qty") or 0.0
        if qty <= 0.0:
            continue
        if _bare(sym) != target:
            continue
        mark = price or ((row or {}).get("market_value") or 0.0) / qty
        if not mark or mark <= 0.0:
            mark = (row or {}).get("cost") or 0.0
        stock_value += qty * mark
    industry = (PARAMS.get("industry_map") or {}).get(symbol)
    concept = (PARAMS.get("concept_map") or {}).get(symbol)
    industry_value = 0.0
    industry_known = isinstance(industry, str) and bool(industry)
    concept_value = 0.0
    cluster_set = isinstance(concept, str) and bool(concept)
    if industry_known or cluster_set:
        for sym, row in (positions or {}).items():
            qty = (row or {}).get("qty") or 0.0
            if qty <= 0.0:
                continue
            mark = ((row or {}).get("market_value") or 0.0) / qty
            if not mark or mark <= 0.0:
                mark = (row or {}).get("cost") or 0.0
            value = qty * mark
            if industry_known and (PARAMS.get("industry_map") or {}).get(sym) == industry:
                industry_value += value
            if cluster_set and (PARAMS.get("concept_map") or {}).get(sym) == concept:
                concept_value += value
        if industry_known:
            industry_value += own_buy
        if cluster_set:
            concept_value += own_buy
    return {
        "stock_value": stock_value,
        "industry_known": industry_known,
        "industry_value": industry_value,
        "cluster_set": cluster_set,
        "cluster_value": concept_value,
    }


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


def _trade_session_ok(moment=None):
    """Whitelist gate: trade only inside a trading day's two cash sessions.

    2026-11-13 owner ruling: no order and no cancel outside 09:30-11:30 /
    13:00-14:52 (lunch break included). The legacy no_*_windows lists are
    BLACKLISTS and let 00:00 straight through -- that is how the overnight
    orders landed. This gate is fail-closed: missing or empty session_windows
    refuses everything. run_round passes its own moment (one clock for the
    whole round); the _place/_cancel backstop passes none and therefore reads
    the real clock, so it can never be bypassed with a forged ctx.
    """
    if moment is None:
        # Inside a round, _place/_cancel inherit the round's clock so a test
        # or a replay that pins the moment stays deterministic. Outside any
        # round the real clock is used -- never a caller-supplied ctx.
        moment = _STATE.get("round_moment")
    if moment is None:
        try:
            moment = now_cn()
        except Exception:  # noqa: BLE001 - a broken clock must refuse, not trade
            return False, "clock_unavailable"
    hhmm = hhmm_now(moment)
    day = day_key(moment)
    trading = rule_is_trading_day(PARAMS, {
        "weekday": moment.weekday(), "day": day,
        "closed_days": set(PARAMS.get("closed_days") or [])})
    if not trading:
        return False, "not_trading_day"
    windows = PARAMS.get("session_windows")
    if not isinstance(windows, (list, tuple)) or not windows:
        return False, "session_windows_missing"
    if not rule_window_hit(PARAMS, windows, hhmm):
        return False, "outside_trading_session"
    return True, "ok"


def _pending_buy_rows(orders):
    """Open BUY orders that still reserve budget, in rules_sizing row shape.

    2026-11-19 owner ruling "pending orders MUST occupy the budget". Two
    defects kept this list empty all day:
      * the ORDER table read as 0 rows (see _live_orders -- wrong remark field);
      * even with rows, the filter only accepted the baked `buy_signals`
        prefixes, which is `pullback_buy` -> "PB". Every factor_composite buy
        ("FC...") that actually traded today was excluded by construction.
    The side is now read from the counter row (_order_side) instead of guessed
    from a prefix. An unproven side is treated as a BUY: over-reserving only
    refuses buys, while under-reserving is the overshoot this exists to stop.
    Terminal rows and fully traded rows reserve nothing.
    """
    known = set(str(v) for v in (PARAMS.get("signal_codes") or {}).values() if v)
    out = []
    for row in orders or []:
        if (row or {}).get("terminal") is not False:
            continue
        side = str((row or {}).get("side") or "")
        if side == "sell":
            continue
        if not side:
            # No side field on this build: fall back to "one of OUR signal
            # short codes" so another program's dashed remark is still skipped.
            short = str((row or {}).get("short_code") or "")
            if not any(short.startswith(prefix) for prefix in known):
                continue
        qty = float((row or {}).get("qty") or 0.0) - float((row or {}).get("traded") or 0.0)
        price = float((row or {}).get("price") or 0.0)
        if qty <= 0.0 or price <= 0.0:
            continue
        out.append({"symbol": _bare((row or {}).get("symbol")),
                    "qty": qty, "remain": qty, "price": price})
    return out


def _reconcile_session_intents(orders, day):
    """Release same-day buy journals the counter has proved dead.

    MANUAL-CANCEL-FIX (owner report 2026-11-19). Cancelling an unfilled order --
    from this strategy's stale leg OR by hand in the client -- leaves three
    session journals pointing at it: `sent` (order dedup), `buy_intents` (one
    intent per symbol per day) and `buy_amounts` (the money charged to today's
    budget). Only the auto-cancel path used to drop `sent`, so after a MANUAL
    cancel the name stayed blocked for the whole day AND its money stayed
    counted: the reported "cancel does nothing, no new order appears".

    The proof standard matches `counter_codes` (owner ruling 2026-11-17): the
    intent is dead only when the counter shows at least one row for that symbol
    AND every row of it today is cancelled-without-fill. A symbol with NO row
    yet stays journaled -- the ORDER table is seconds late, and dropping the
    journal on "not visible yet" would double-buy the same intent.

    RELEASE-BUDGET (same day, second half): one cancelled row releases the
    journal ONCE. Without that counter the re-placed order would be released
    again next round by the very rows that justified the first release (the new
    ticket needs seconds to show up), and the same name would be bought twice.
    The proof is the order id: a release consumes the cancelled ids it saw, and
    an id-less row cannot prove novelty, so it releases nothing (fail-closed).
    """
    if _STATE.get("released_day") != day:
        _STATE["released_day"] = day
        _STATE["released_orders"] = set()
    if "released_orders" not in _STATE:
        _STATE["released_orders"] = set()
    seen = {}
    for row in orders or []:
        sym = _bare((row or {}).get("symbol"))
        if not sym:
            continue
        seen.setdefault(sym, []).append(row or {})
    # One entry per SYMBOL (not per journal key): `sent` is keyed by short code
    # and `buy_intents` by symbol, so iterating both lists would release the
    # same name twice and report it under two different labels.
    keys = {}
    for entry in list(_STATE["sent"]):
        if entry[0] == day:
            keys.setdefault(entry[1], set()).add(entry[2])
    for entry in list(_STATE["buy_intents"]):
        if entry[0] == day:
            keys.setdefault(entry[1], set())
    released = []
    for sym in sorted(keys):
        rows = seen.get(_bare(sym))
        if not rows or not all(bool(r.get("cancel_like")) for r in rows):
            continue
        # A SELL row that was cancelled proves nothing about the buy intent.
        # An unproven side is treated the same way: releasing is the direction
        # that allows a second buy, so an unknown side must not release.
        if not all(str(r.get("side") or "") == "buy" for r in rows):
            continue
        fresh = [str(r.get("order_id") or "") for r in rows]
        fresh = [oid for oid in fresh if oid and oid not in _STATE["released_orders"]]
        if not fresh:
            continue
        _STATE["released_orders"].update(fresh)
        shorts = keys.get(sym) or set()
        for short in shorts:
            _STATE["sent"].discard((day, sym, short))
        _STATE["buy_intents"].discard((day, sym))
        _STATE["buy_amounts"].pop((day, sym), None)
        labels = sorted(short for short in shorts if short)
        released.append(labels[0] if labels else _bare(sym))
    if released:
        _log("journal_release cancelled_unfilled=%s" % ",".join(sorted(set(released))[:8]))
    return released


def _pending_amount_for(symbol, pending_rows):
    """Money still working on the counter for THIS symbol (unfilled part)."""
    target = _bare(symbol)
    total = 0.0
    for row in pending_rows or []:
        if str((row or {}).get("symbol") or "") != target:
            continue
        total += float((row or {}).get("remain") or 0.0) * float((row or {}).get("price") or 0.0)
    return total


def _turnover_pct_of(tick, pre):
    """Today's turnover percent: cumulative amount / (prev_close * float_shares) * 100.

    2026-11-19 owner: bake float shares so the T liquidity gate can actually run.
    Until today the carrier never had `turnover_pct` at all (grep: zero writers),
    so `t_min_turnover_pct=1.5` refused every name every round with
    `turnover_unavailable` -- a gate that can never pass is a gate that silently
    closed the whole T layer.

    Inputs are deliberately narrow and every miss returns None (the gate then
    keeps treating the name as "not passed", which is the honest direction):
      * float_shares comes BAKED from the cloud (scripts/pregen_carrier_factors_gz.py,
        Tencent quote fields); the sandbox cannot fetch it;
      * prev_close comes from the tick, falling back to the baked precalc value;
      * amount is the tick's cumulative turnover in yuan (same field the VWAP uses).
    Units: amount(yuan) / (yuan-per-share * shares) = a pure ratio; x100 = percent.
    """
    try:
        shares = float((pre or {}).get("float_shares") or 0.0)
    except (TypeError, ValueError):
        return None
    if shares <= 0.0:
        return None
    tick = tick or {}
    amount = tick.get("amount")
    if amount is None:
        amount = (pre or {}).get("amount")
    prev = tick.get("prev_close")
    if prev is None:
        prev = (pre or {}).get("prev_close")
    try:
        amount = float(amount)
        prev = float(prev)
    except (TypeError, ValueError):
        return None
    if amount <= 0.0 or prev <= 0.0:
        return None
    cap = prev * shares
    if cap <= 0.0:
        return None
    return amount / cap * 100.0


def _float_mcap_yi_of(tick, pre):
    """Free-float market cap in CNY 100M (CNY 100M), or None when unreadable.

    2026-11-20 (T-TURNOVER-TIER): the Turnover gate on the cloud side is now a
    ladder keyed by float market cap (>=500100M -> 0.40%, 100-500100M -> 0.60%,
    <100100M -> 1.50%). The carrier must compute the SAME key with the SAME
    inputs it already uses for turnover_pct -- `prev_close * float_shares` --
    otherwise the two sides would disagree on which tier a name sits in.
      * float_shares is baked from the cloud (the sandbox cannot fetch it);
      * prev_close comes from the tick, falling back to the baked precalc row;
      * every miss is None, and rules_t then falls back to the strictest flat
        floor rather than silently widening the gate.
    """
    try:
        shares = float((pre or {}).get("float_shares") or 0.0)
    except (TypeError, ValueError):
        return None
    if shares <= 0.0:
        return None
    tick = tick or {}
    prev = tick.get("prev_close")
    if prev is None:
        prev = (pre or {}).get("prev_close")
    try:
        prev = float(prev)
    except (TypeError, ValueError):
        return None
    if prev <= 0.0:
        return None
    cap = prev * shares
    if cap <= 0.0:
        return None
    return cap / 1e8


def _pullback_limit_price(vwap, last_price):
    """VWAP minus the baked pullback pct, never above the last price.

    2026-11-13 owner ruling: "VWAP price minus 0.7%". The min() cap matters:
    VWAP is a session average and can sit ABOVE the last price; without the
    cap a "pullback" limit would rest above the market and lift the ask.
    Returns None when either reading is unusable (caller keeps the old price).
    """
    pct = float(PARAMS.get("vwap_pullback_pct", 0.0))
    if not vwap or vwap <= 0.0 or not last_price or last_price <= 0.0:
        return None
    target = vwap * (1.0 - pct / 100.0)
    if target <= 0.0:
        return None
    return round(min(target, last_price), 2)


def _floor_topup_qty(floor, price, lot, ceiling):
    """Smallest whole-lot share count that reaches `floor`, capped by `ceiling`.

    2026-11-19 owner ruling: "10000 is the FLOOR of opening a position -- if it
    comes out under 10000, top it up to 10000." Two truncations used to make the
    floor unreachable: the dynamic chain's per-trade band (10% of a 99022 book =
    9902) and the whole-lot rounding (10000 / 6.81 -> 1400 shares = 9534). This
    helper answers both with one number, and returns None when the ceiling cannot
    pay for it -- the caller then abandons the order whole (never overdraw).

    Lot grid: the counter only accepts multiples of `lot` (100 on this account),
    so "exactly 10000" usually does not exist; the returned qty is the smallest
    multiple of lot whose value is >= floor.
    """
    floor = _to_float(floor, 0.0)
    price = _to_float(price, 0.0)
    lot = _to_float(lot, 0.0)
    ceiling = _to_float(ceiling, 0.0)
    if floor <= 0.0 or price <= 0.0 or lot <= 0.0:
        return None
    qty = int(floor / price / lot) * int(lot)
    if qty <= 0 or qty * price < floor - 1e-9:
        qty += int(lot)
    if qty * price > ceiling + 1e-6:
        return None
    return qty


def _qmt_code(symbol):
    """Bare 6-digit code -> 'CODE.SH/.SZ/.BJ' for every ContextInfo call.

    LIVE ROOT CAUSE 2026-11-18 (v79 probe receipt on the box): the baked pool is
    1000 codes and only 52 of them carry a market dot; the batched read returned
    exactly 48 -- the dotted subset. Measured per symbol:
        get_full_tick(['000417'])            -> {}      (0 ticks)
        get_full_tick(['000417.SZ'])         -> 1 tick
        subscribe_quote('000417', 'tick')    -> 0       (0 is not a real id)
        subscribe_quote('000417.SZ','tick')  -> 186
    So 948 of 1000 names were structurally unreachable, the two held positions
    included: `no_price=770` refused every buy at rule_fillable and
    `sell_blind held_without_price=2 total_held=2` left all exit legs blind --
    the whole of "today traded nothing".

    ONLY the string handed to the client is normalised. Every key we join on
    (pos_view, PARAMS symbols, precalc, dedup sets) stays BARE, so this cannot
    ripple into the booking logic. `_place` already builds its own suffixed code
    via _expect_suffix, so the order path is unchanged.
    """
    text = str(symbol or "")
    if not text or "." in text:
        return text
    suffix = _expect_suffix(text)
    if suffix:
        return "%s.%s" % (text, suffix)
    # _expect_suffix declines BJ-style heads; a read still needs a code, so map
    # the two remaining exchange families explicitly rather than silently ask
    # for a bare code that this client never resolves.
    head = text[:1]
    if head in ("4", "8"):
        return text + ".BJ"
    return text


def _tick_of(ContextInfo, symbol):
    """One snapshot -> the flat dict the rule layer reads, or {} when absent.

    Round-scoped batch is consulted first (A-ROUTE 2026-11-18); a miss or a
    stale snapshot falls through to the original per-symbol read below, keeping
    the price chain fail-closed if the batch never answers on a given build.
    """
    snap = _STATE.get("tick_snapshot")
    if (isinstance(snap, dict) and symbol in snap
            and _STATE.get("tick_snapshot_round") == _STATE.get("round_moment")):
        return snap[symbol]
    getter = getattr(ContextInfo, "get_full_tick", None)
    if not callable(getter):
        _warn_once("no_tick", "ContextInfo.get_full_tick unavailable")
        return {}
    code = _qmt_code(symbol)
    try:
        raw = getter([code])
    except Exception as exc:
        _warn_once("tick_fail:%s" % symbol, "get_full_tick failed: %s" % repr(exc)[:120])
        return {}
    if not isinstance(raw, dict):
        return {}
    # The client echoes the key it was given; accept both shapes so a build that
    # normalises codes back to bare keys does not silently read as "no price".
    entry = raw.get(code)
    if not isinstance(entry, dict):
        entry = raw.get(symbol)
    if not isinstance(entry, dict):
        return {}
    # Keep the BARE symbol on the normalised tick: ctx identity stays bare.
    return normalize_tick(symbol, entry)


def _batch_ticks(ContextInfo, symbols, positions):
    """A-ROUTE companion 2026-11-18: ONE batched get_full_tick for the whole
    scan universe per round, rekeyed to BARE symbols into a round-scoped snapshot.

    Why: the per-symbol path above costs ~1000 serial reads (~45s/round on this
    client) and is the reason decisions lag the market. The 11-18 probe proved
    the client DOES answer batched reads -- but ONLY for SUFFIXED codes (bare
    universe asked=1000 got=48). So feed every code through _qmt_code and rekey
    the answered keys back to bare before caching.

    FAIL-CLOSED by construction: a missing API, an exception, or an empty/odd
    payload leaves the snapshot empty, and _tick_of then falls back to its
    original per-symbol read for EVERY name -- never worse than before this
    function existed. The snapshot is keyed by the round moment so a stale
    batch from a prior round can never be served. Only names that actually
    answered are cached; misses fall through to the per-symbol path. No order,
    no budget, no state beyond the round snapshot is touched.
    """
    # Clear LAST round's snapshot up front: a batch that fails/returns empty
    # must never leave a stale cache behind, even if the round key were reused.
    _STATE.pop("tick_snapshot", None)
    _STATE.pop("tick_snapshot_round", None)
    getter = getattr(ContextInfo, "get_full_tick", None)
    if not callable(getter):
        return
    bare = []
    seen = set()
    for s in list(symbols or []) + list((positions or {}).keys()):
        key = str(s)
        if key and key not in seen:
            seen.add(key)
            bare.append(key)
    if not bare:
        return
    codes = [_qmt_code(s) for s in bare]
    try:
        raw = getter(codes)
    except Exception:  # noqa: BLE001 - batch is an optimisation, never a gate
        return
    if not isinstance(raw, dict) or not raw:
        return
    snap = {}
    for s, code in zip(bare, codes):
        entry = raw.get(code)
        if not isinstance(entry, dict):
            entry = raw.get(s)
        if isinstance(entry, dict):
            snap[s] = normalize_tick(s, entry)
    _STATE["tick_snapshot"] = snap
    _STATE["tick_snapshot_round"] = _STATE.get("round_moment")
    if snap:
        _log("batch_ticks got=%d asked=%d" % (len(snap), len(bare)))


def _subscribe_fns(ContextInfo):
    """P0-A (2026-11-17): resolve the subscribe callables BY LITERAL NAME.

    The 11-16 "fix" resolved the subscribe names through `_fn(...)`, but `_fn` only
    knows the four whitelisted trade fns (passorder/cancel/get_trade_detail_data/
    get_account), so the lookup for the subscribe names was ALWAYS None and the init
    subscribed -- every sell leg and the T legs read last_price=None all day and
    returned silently. This build exposes get_full_tick but the SUBSCRIBE calls
    were never made, so the tick came back empty for unsubscribed symbols.

    Resolution goes through getattr(ContextInfo, "<literal name>", None) -- the
    same shape as _tick_of / _download_day_bars. g2 forbids getattr with a
    VARIABLE name; here both names are literals, so it is allowed.

    Returns (whole_fn, per_symbol_fn, api_state) where api_state is
    present | absent | error so the init receipt can name WHY nothing subscribed.
    """
    whole = getattr(ContextInfo, "subscribe_whole_quote", None)
    per_symbol = getattr(ContextInfo, "subscribe_quote", None)
    if not callable(whole) and not callable(per_symbol):
        return (whole if callable(whole) else None,
                per_symbol if callable(per_symbol) else None, "absent")
    return (whole if callable(whole) else None,
            per_symbol if callable(per_symbol) else None, "present")


def _subscribe_market(ContextInfo):
    """SUBSCRIBE-FIX P0-A (2026-11-17): whole-market subscribe via getattr.

    get_full_tick returns EMPTY for symbols that were never subscribed; the 11-16
    version routed subscribe through _fn (whitelist of 4 trade fns) so it was
    always None and NOTHING subscribed -- logs showed `tried=[] per_symbol_ok=0`
    while the sell legs sat blind all day. This is the real fix: call the
    literal subscribe fns, with a signature ladder because the whole-market call
    shape differs across builds (list of markets vs one market), and record the
    api state so a silent no-op can never pass as "ran but failed" again.
    """
    whole, _per, api = _subscribe_fns(ContextInfo)
    tried = []
    if api == "absent":
        _log("subscribe_market tried=[] api=absent per_symbol=0/0")
        return "absent"
    if callable(whole):
        # W1: pass both markets at once (the common 2.x shape).
        try:
            sub_id = whole(["SH", "SZ"])
            tried.append("W1=%s" % _short_id(sub_id))
        except Exception as exc:  # noqa: BLE001
            tried.append("W1=err:%s" % repr(exc)[:60])
            # W2: one market per call (fallback build).
            for market in ("SH", "SZ"):
                try:
                    sub_id = whole(market)
                    tried.append("%s=%s" % (market, _short_id(sub_id)))
                except Exception as exc2:  # noqa: BLE001
                    tried.append("%s=err:%s" % (market, repr(exc2)[:40]))
    # api= present means the fn existed and we at least attempted a call;
    # error means every attempted shape raised (subscribe genuinely unavailable).
    api_state = "error" if any("err:" in t for t in tried) and not _any_ok(tried) else api
    _log("subscribe_market tried=[%s] api=%s" % (",".join(tried), api_state))
    return api_state


def _any_ok(tried):
    """True when at least one ladder entry returned a usable subscription id."""
    for t in tried:
        if "err:" not in t and not t.endswith("=bad") and not t.endswith("=none"):
            if "=" in t and t.rsplit("=", 1)[-1] not in ("bad", "none", "-"):
                return True
    return False


def _subscribe_held(ContextInfo, positions):
    """P0-A per-symbol fallback: subscribe only the HELD symbols, once per day.

    init cannot afford 1000 per-symbol subscribe calls (it would block the first
    bar). Whole-market covers the pool; this closes the gap for builds where
    whole-market is unavailable but per-symbol works, by subscribing the handful
    of positions we must price for the sell legs. Idempotent per day via _STATE.
    """
    _whole, per, api = _subscribe_fns(ContextInfo)
    if not callable(per):
        return
    if not positions:
        return
    day = day_key(now_cn())
    done = _STATE.setdefault("subbed_today", {})
    if done.get("day") != day:
        done.clear()
        done["day"] = day
    n = 0
    for symbol, row in positions.items():
        if not row or float(row.get("qty", 0.0) or 0.0) <= 0.0:
            continue
        if symbol in done:
            continue
        # 2026-11-18: subscribe with the SUFFIXED code and the TICK period --
        # the probe measured ('000417','tick') -> id 0 (silent no-op) and
        # ('000417.SZ','tick') -> 186. Bare+1m was the shape that kept every
        # exit leg blind; the ladder keeps the old shape as a fallback for a
        # build that rejects "tick".
        code = _qmt_code(symbol)
        sub_id = None
        for shape in ((code, "tick", "follow"), (code, "tick"), (code, "1m", "follow")):
            try:
                sub_id = per(*shape)
            except Exception:  # noqa: BLE001
                continue
            break
        if sub_id is None:
            continue
        done[symbol] = _short_id(sub_id)
        n += 1
    if n:
        _log("subscribe_held new=%d held_total=%d" % (n, len(done) - 1))


def _subscribe_1m_held(ContextInfo, positions):
    """A-ROUTE 2026-11-18 production wiring: subscribe the MINUTE period for
    held names so the client bar engine aggregates 1m bars the minute legs read.

    Why a SEPARATE function and not a shape in _subscribe_held: that loop tries
    ("tick") first and BREAKS on success -- tick always wins here, so its "1m"
    fallback was unreachable and the 1m store stayed empty. The 11-18 probe
    (_probe_subscribe_1m) proved the suffixed "1m" subscribe DOES make the client
    aggregate bars (mde_1m=5 > 0 after sub1m=243). This promotes that proven
    shape to production, alongside tick (both periods are valid at once on this
    build -- the probe subscribed tick and 1m on the same code without conflict).

    Only HELD names are subscribed: the two minute-leg consumers (afternoon
    reseal protection, the T leg) both operate on positions, never on the pool.
    Idempotent per Beijing day via a dedicated ledger keyed by BARE symbol (the
    join keys stay bare -- see rule 204). Read-only: a subscribe call places no order
    and touches no budget; failure is swallowed so a broken subscribe API does
    not take down the round (the minute legs stay fail-closed on missing bars).
    """
    _whole, per, _api = _subscribe_fns(ContextInfo)
    if not callable(per) or not positions:
        return
    day = day_key(now_cn())
    done = _STATE.setdefault("subbed_1m_today", {})
    if done.get("day") != day:
        done.clear()
        done["day"] = day
    n = 0
    for symbol, row in positions.items():
        if not row or float(row.get("qty", 0.0) or 0.0) <= 0.0:
            continue
        if symbol in done:
            continue
        code = _qmt_code(symbol)
        sub_id = None
        for shape in ((code, "1m", "follow"), (code, "1m")):
            try:
                sub_id = per(*shape)
            except Exception:  # noqa: BLE001 - signature ladder
                continue
            break
        if sub_id is None:
            continue
        done[symbol] = _short_id(sub_id)
        n += 1
    if n:
        _log("subscribe_1m_held new=%d held_total=%d" % (n, len(done) - 1))


def _short_id(sub_id):
    """Normalize a subscribe return into a short readable token.

    -1 is a FAILURE sentinel on this client and reads like a real subscription id
    when printed as "-1", so it is mapped to `bad`; None/empty maps to `none`.
    """
    try:
        value = int(sub_id)
    except (TypeError, ValueError):
        return "none"
    if value < 0:
        return "bad"
    return str(value)


# ---- PRICE-PROBE 2026-11-18 (owner: "why did nothing trade today") ----------
# Live evidence that forced this probe: `round_skips total=859 no_price=770` AND
# `sell_blind held_without_price=2 total_held=2` -- the carrier could not price a
# single symbol, not even the two positions it is supposed to exit. The buy lane
# therefore died at rule_fillable and all three sell legs stood blind, which is
# the whole of "today placed nothing".
#
# Before the price path is touched we need a receipt naming WHICH read shape
# still answers on this client. Five shapes, all read-only:
#   1) one BATCHED get_full_tick over the whole pool (the proposed fix)
#   2) the per-symbol get_full_tick (today's only read)
#   3) subscribe_quote(symbol, "tick") followed by 2) -- is the subscription
#      period ("1m" today) the reason nothing arrives?
#   4) get_market_data_ex(period="tick") -- tick bars instead of snapshots
#   5) get_market_data_ex(period="1d")  -- is the history leg dead too?
#
# A round with the probe on trades exactly what it traded before: the only
# output is a log line. It never touches _STATE["sent"], the budget, or any
# trade helper -- and the probe body must never grow a place/cancel call.
#
# Every 5th round only: rounds already sit ~45s apart because ~1000 serial
# per-symbol reads cost ~45ms each; probing every round would add log volume
# and no information.
_PROBE_ROUND_EVERY = 5


def _probe_hits(raw, symbol):
    """How many tick entries a snapshot read returned (0 for any odd shape)."""
    if not isinstance(raw, dict):
        return 0
    if symbol:
        entry = raw.get(symbol)
        return 1 if isinstance(entry, dict) and bool(entry) else 0
    hits = 0
    for key in raw.keys():
        if isinstance(raw.get(key), dict):
            hits += 1
    return hits


def _probe_mde_rows(getter, symbol, period, fields, count):
    """Rows one get_market_data_ex read returned; -1 means the call raised."""
    try:
        payload = getter(fields, [symbol], period=period, count=count)
    except Exception:  # noqa: BLE001 -- a signature this build rejects IS the answer
        return -1
    return len(_bar_dicts_of(payload, symbol))


def _probe_priced_one(ContextInfo, getter, symbol):
    """Shape 2: the per-symbol snapshot read the round loop uses today."""
    if not callable(getter):
        return 0
    try:
        return _probe_hits(getter([symbol]), symbol)
    except Exception:  # noqa: BLE001
        return 0


def _probe_subscribe_tick(ContextInfo, symbol):
    """Shape 3: subscribe for TICKS (today's held-subscribe asks for "1m").

    Returns the short subscription id, or a reason tag. Idempotent per day per
    symbol so a probe round never turns into a subscription storm.
    """
    _whole, per, _api = _subscribe_fns(ContextInfo)
    if not callable(per):
        return "api_absent"
    day = day_key(now_cn())
    done = _STATE.setdefault("probe_subbed", {})
    if done.get("day") != day:
        done.clear()
        done["day"] = day
    if symbol in done:
        return str(done[symbol])
    try:
        sub_id = per(symbol, "tick")
    except Exception:  # noqa: BLE001
        try:
            sub_id = per(symbol, "tick", "follow")
        except Exception:  # noqa: BLE001
            done[symbol] = "err"
            return "err"
    done[symbol] = _short_id(sub_id)
    return str(done[symbol])


def _probe_subscribe_1m(ContextInfo, symbol):
    """Shape 6: subscribe the MINUTE period and let the CLIENT aggregate.

    Live question 2026-11-18 12:2x: the minute legs (afternoon reseal, T intraday)
    read `get_market_data_ex(..., "1m", ...)`, and the local minute store is
    empty (the SZ/60 minute shard holds one file). A downloader is absent from
    this build, but 1m bars do not need one -- the client bar engine aggregates
    the ticks it already pushes. The suffixed "1m" subscribe has never been
    exercised (the 11-18 rebuild made "tick" primary and "1m" an unreachable
    fallback; the pre-11-18 bare-code "1m" call returned id 0, a silent no-op).
    Returns the short id, or a reason tag. Idempotent per day per symbol.
    """
    _whole, per, _api = _subscribe_fns(ContextInfo)
    if not callable(per):
        return "api_absent"
    day = day_key(now_cn())
    done = _STATE.setdefault("probe_1m", {})
    if done.get("day") != day:
        done.clear()
        done["day"] = day
    if symbol in done:
        return str(done[symbol])
    try:
        sub_id = per(symbol, "1m", "follow")
    except Exception:  # noqa: BLE001
        try:
            sub_id = per(symbol, "1m")
        except Exception:  # noqa: BLE001
            done[symbol] = "err"
            return "err"
    done[symbol] = _short_id(sub_id)
    return str(done[symbol])


def _probe_price_channels(ContextInfo, positions):
    """Read-only receipt: which price channel still answers on this client.

    Called once every _PROBE_ROUND_EVERY rounds from run_round. Symbols probed:
    the held rows first (those are the ones the sell legs must see), then the
    first pool names, capped at four so the probe can never become the reason a
    round is slow.
    """
    seq = int(_STATE.get("probe_seq") or 0) + 1
    _STATE["probe_seq"] = seq
    if (seq - 1) % _PROBE_ROUND_EVERY != 0:
        return
    asked = []
    for symbol in (positions or {}):
        row = (positions or {}).get(symbol) or {}
        try:
            qty = float(row.get("qty", 0.0) or 0.0)
        except (TypeError, ValueError):
            qty = 0.0
        if qty > 0.0:
            asked.append(str(symbol))
    for symbol in (PARAMS.get("symbols") or []):
        if len(asked) >= 4:
            break
        if str(symbol) not in asked:
            asked.append(str(symbol))
    asked = asked[:4]
    if not asked:
        _log("tick_probe skipped=no_symbols")
        return
    getter = getattr(ContextInfo, "get_full_tick", None)
    mde = getattr(ContextInfo, "get_market_data_ex", None)
    universe = [str(s) for s in (PARAMS.get("symbols") or [])]
    if callable(getter):
        # Shape 1: ONE batched call over the whole baked pool. The SAMPLE of the
        # keys that came back is the format receipt: if the hits are exactly the
        # dotted codes, the pool's bare codes are the bug.
        try:
            raw_batch = getter(universe)
            batch_hits = _probe_hits(raw_batch, None)
            sample = ""
            if isinstance(raw_batch, dict):
                keys = [str(k) for k in raw_batch.keys()]
                keys.sort()
                sample = "|".join(keys[:4])
            _log("tick_probe batch asked=%d got=%d sample=%s" % (
                len(universe), batch_hits, sample))
        except Exception as exc:  # noqa: BLE001
            _log("tick_probe batch raised=%s" % repr(exc)[:80])
    else:
        _log("tick_probe batch api=absent")
    # Shapes 2..5, one line per symbol: read before subscribing, subscribe for
    # TICKS, read again (does the subscription change the answer?), then the two
    # bar reads. "-1" means the call shape was rejected, "0" means accepted but
    # empty -- those are different diagnoses and must not be folded together.
    priced = 0
    for symbol in asked:
        sfx = _qmt_code(symbol)
        one = _probe_priced_one(ContextInfo, getter, symbol)
        one_sfx = one if sfx == symbol else _probe_priced_one(ContextInfo, getter, sfx)
        sub = _probe_subscribe_tick(ContextInfo, symbol)
        sub_tick = _probe_priced_one(ContextInfo, getter, symbol)
        if sfx == symbol:
            sfx_sub = sub
            sfx_tick = sub_tick
        else:
            sfx_sub = _probe_subscribe_tick(ContextInfo, sfx)
            sfx_tick = _probe_priced_one(ContextInfo, getter, sfx)
        if not callable(mde):
            mde_tick = -1
            mde_day = -1
        else:
            mde_tick = _probe_mde_rows(mde, symbol, "tick", ["close"], 1)
            mde_day = _probe_mde_rows(mde, symbol, "1d", ["close", "volume"], 2)
        # Shapes 6..7 (2026-11-18 12:2x, MINUTE LEG): can the client aggregate the
        # 1m bars itself, with no downloader at all? The suffixed "1m" subscribe
        # has never actually run -- the 11-18 rebuild switched _subscribe_held to
        # "tick" and left "1m" as an unreachable fallback, while the old bare+1m
        # call returned id 0 (a silent no-op). Read BEFORE subscribing (so round N
        # shows the pre-subscription state), subscribe, read again: if a later
        # round reports mde_1m > 0 the afternoon-reseal and T minute legs can be
        # fed from inside QMT. mde_1d_sfx is the same question for day bars --
        # the production read now uses the suffixed code, this proves whether the
        # local store actually answers it.
        if callable(mde):
            mde_day_sfx = _probe_mde_rows(mde, sfx, "1d", ["close", "volume"], 2)
            mde_1m = _probe_mde_rows(mde, sfx, "1m", ["close"], 5)
        else:
            mde_day_sfx = -1
            mde_1m = -1
        sub_1m = _probe_subscribe_1m(ContextInfo, sfx)
        if callable(mde):
            mde_1m_after = _probe_mde_rows(mde, sfx, "1m", ["close"], 5)
        else:
            mde_1m_after = -1
        if one or one_sfx or sub_tick or sfx_tick or mde_tick > 0 or mde_day > 0:
            priced += 1
        # TWO lines on purpose: the client truncates one printed line at ~150
        # chars (13:00 receipt: `... mde_tick=0 mde_1d` and then cut), so the
        # bar readings were the first thing to fall off the end.
        _log("tick_probe sym=%s one=%d one_sfx=%d sub=%s sfx=%s sub_tick=%d sfx_tick=%d" % (
            symbol, one, one_sfx, sub, sfx_sub, sub_tick, sfx_tick))
        _log("tick_probe M sym=%s mde_tick=%d mde_1d=%d d1d_sfx=%d mde_1m=%d sub1m=%s mde_1m_after=%d" % (
            symbol, mde_tick, mde_day, mde_day_sfx, mde_1m, sub_1m, mde_1m_after))
    _log("tick_probe summary asked=%d priced=%d universe=%d" % (
        len(asked), priced, len(universe)))


def _download_day_bars(ContextInfo):
    """2026-11-11 owner direction: pull the day bars FROM QMT itself.

    The 11-03 probe saw an empty get_market_data_ex because the client had no
    local daily history -- the official fix is download_history_data2 (the
    client's own downloader; no files, no out-of-process network). Kicked once
    in init for the whole pool; after the callback fires, _hist_bars starts
    returning real bars and every bars-consuming tactic goes live. precalc
    stays as the fallback for symbols the download never covers.
    """
    dl = getattr(ContextInfo, "download_history_data2", None)
    if not callable(dl):
        # DL-FALLBACK-20261112: this broker build does not expose the batch
        # downloader as a callable ContextInfo attr (live log "unavailable").
        # Fall back to the legacy single-symbol download_history_data, pulled
        # from the round loop in small batches so init never blocks.
        dl1 = getattr(ContextInfo, "download_history_data", None)
        if callable(dl1):
            _STATE["dl1_pending"] = list(PARAMS.get("symbols") or [])
            _log("day_bars_download legacy mode queued=%d (batched per round)" % len(_STATE["dl1_pending"]))
        else:
            _log("WARN no downloader API in this build: precalc is the only history source")
        return
    symbols = list(PARAMS.get("symbols") or [])
    if not symbols:
        return
    def _cb(data):
        try:
            done = 0
            if isinstance(data, dict):
                fin = data.get("finished") or data.get("finish") or []
                done = len(fin) if isinstance(fin, (list, tuple)) else 1
            _log("day_bars_download callback finished=%s" % str(done))
        except Exception:
            _log("day_bars_download callback fired")
    try:
        dl([_qmt_code(s) for s in symbols], "1d", "", "", _cb)
        _log("day_bars_download kicked symbols=%d period=1d" % len(symbols))
    except Exception as exc:
        _log("WARN download_history_data2 failed: %s" % repr(exc)[:120])


def _pump_legacy_downloads(ContextInfo):
    """Drain the legacy downloader queue, N symbols per round.

    The legacy download_history_data(stock, period, start, end) is synchronous
    per symbol; draining the whole pool inside init would freeze the first
    handlebar for minutes. Batching keeps the round cadence while the local
    day-bar store fills up, after which get_market_data_ex starts returning
    real series and the bars-consuming tactics go live on their own.
    """
    pending = _STATE.get("dl1_pending") or []
    if not pending:
        return
    dl1 = getattr(ContextInfo, "download_history_data", None)
    if not callable(dl1):
        _STATE["dl1_pending"] = []
        return
    batch, _STATE["dl1_pending"] = pending[:20], pending[20:]
    ok = 0
    for sym in batch:
        try:
            dl1(_qmt_code(sym), "1d", "", "")
            ok += 1
        except Exception as exc:  # noqa: BLE001
            _warn_once("dl1_fail", "legacy day-bar download failed: %s" % repr(exc)[:100])
    _log("day_bars_download legacy batch done=%d remain=%d" % (ok, len(_STATE["dl1_pending"])))


def _precalc_bars(symbol, tick):
    """PRECALC-SERIES (2026-11-16 owner): decode the pregen bars_seq into bar
    dicts and append one synthetic in-progress bar from the live tick.

    All 61 factor_defs consume input="bars" (daily series), and this build has
    no downloader API -- bars_coverage was 52/961, so the factor engine
    abstained on 96% of the pool and never even reached the max_positions
    refusal. The pregen sequence (hithink daily bars, forward-adjusted, ending
    on the previous session) is the only data leg that can feed them.

    Two hard rules here:
      * WHOLE-SEQUENCE REPLACEMENT ONLY: local bars (get_market_data_ex) and
        the pregen series use different adjustment bases; splicing them at a
        seam would fabricate a one-day jump that poisons momentum/breakout.
        The caller decides which source wins; this function never merges.
      * NEVER INVENT: a symbol without bars_seq keeps the honest abstain.

    The synthetic today-bar carries real close/volume/amount from the tick;
    open/high/low stay None because get_full_tick has no such fields, so
    HighLow/Atr family factors decline on today's bar only while the
    close/volume/amount family (Mom/Brk/VMA/Alpha/RSI/VolSurge ...) stays
    live. Rows are ints scaled by 1000 on the pregen side (see bars_seq).
    """
    seq = ((PARAMS.get("precalc") or {}).get(symbol) or {}).get("bars_seq")
    if not isinstance(seq, dict):
        return []
    scale = seq.get("scale") or 1000
    rows = seq.get("rows") or []
    bars = []
    for row in rows:
        if not isinstance(row, (list, tuple)) or len(row) < 4:
            continue
        try:
            bars.append({"open": None,
                         "high": row[0] / float(scale),
                         "low": row[1] / float(scale),
                         "close": row[2] / float(scale),
                         "volume": float(row[3]),
                         "amount": float(row[4]) if len(row) > 4 else None})
        except (TypeError, ValueError, IndexError):
            continue
    if not bars:
        return []
    if tick and tick.get("last_price"):
        bars.append({"open": None, "high": None, "low": None,
                     "close": tick.get("last_price"),
                     "volume": tick.get("volume"),
                     "amount": tick.get("amount")})
    return bars


def _bar_dicts_of(payload, symbol):
    """get_market_data_ex payload -> bar dicts for the rules library.

    Field lists are extracted with the same attribute-or-dict reader as
    _series, then zipped position-wise into {open,high,low,close,volume,amount}
    dicts (the shape columns_from_bars / _f_day_data expect). Short/ragged
    fields are padded with None -- the rules treat missing fields as NaN.
    """
    entry = None
    if isinstance(payload, dict):
        entry = payload.get(symbol, payload)
    if entry is None:
        return []
    # g2 demands a LITERAL attribute name on every getattr -- six explicit reads.
    def _col_open():
        v = getattr(entry, "open", None)
        if v is None and isinstance(entry, dict):
            v = entry.get("open")
        return v
    def _col_high():
        v = getattr(entry, "high", None)
        if v is None and isinstance(entry, dict):
            v = entry.get("high")
        return v
    def _col_low():
        v = getattr(entry, "low", None)
        if v is None and isinstance(entry, dict):
            v = entry.get("low")
        return v
    def _col_close():
        v = getattr(entry, "close", None)
        if v is None and isinstance(entry, dict):
            v = entry.get("close")
        return v
    def _col_volume():
        v = getattr(entry, "volume", None)
        if v is None and isinstance(entry, dict):
            v = entry.get("volume")
        return v
    def _col_amount():
        v = getattr(entry, "amount", None)
        if v is None and isinstance(entry, dict):
            v = entry.get("amount")
        return v
    def _as_list(value):
        if value is None:
            return []
        try:
            return list(value)
        except TypeError:
            return []
    cols = {
        "open": _as_list(_col_open()),
        "high": _as_list(_col_high()),
        "low": _as_list(_col_low()),
        "close": _as_list(_col_close()),
        "volume": _as_list(_col_volume()),
        "amount": _as_list(_col_amount()),
    }
    n = max((len(v) for v in cols.values()), default=0)
    bars = []
    for i in range(n):
        bar = {}
        for f, col in cols.items():
            raw = col[i] if i < len(col) else None
            try:
                bar[f] = float(raw) if raw is not None else None
            except (TypeError, ValueError):
                bar[f] = None
        bars.append(bar)
    return bars


# ---- AFTERNOON-RESEAL 2026-11-17 (owner: sell-side protective leg, in-client) ----
# leader_decay / leader_decay_v2 are LIVE sell tactics that were structurally dead:
# ctx["afternoon_reseal"] was only ever read from baked precalc, which never
# carried it, so the rule declined forever (None) and never sold. The 17:46
# read/value probe on this exact box proved the client CAN return 1-minute bars
# (get_market_data_ex positional shape) and the exact limit price (UpStopPrice
# from get_instrument_detail). So compute the protective reading in-client.
# SAFETY (owner ruling 2026-11-17): this is a PROTECTIVE gate -- True/None both
# skip the sell. On ANY missing reading (no minute bars, no timestamps, no
# limit price) return None => no sell (fail-closed, never assume "not resealed"
# from absent data). Mirrors Go shortbase.go:71 / adapter.go:447-454, but uses
# the EXACT UpStopPrice instead of Go's name-based limit-pct inference.
def _minute_bars_payload(ContextInfo, symbol, count):
    """Fetch today's 1-minute bars with the shape the 17:46 probe proved.

    The probe: get_market_data_ex([], [code], "1m", "", "", 20, "front")
    returned {code: DataFrame(close,high,low,open,amount,stime,...)}. Keyword
    shapes (field=/fieldList=) all raised TypeError on this build, so use the
    positional shape only. Absent/exception -> None (caller stays fail-closed).
    """
    getter = getattr(ContextInfo, "get_market_data_ex", None)
    if not callable(getter):
        return None
    try:
        payload = getter([], [_qmt_code(symbol)], "1m", "", "", int(count), "front")
    except Exception:  # noqa: BLE001 - no minute data is a valid outcome
        return None
    return _rekey_payload(payload, symbol) if isinstance(payload, dict) else None


def _instrument_up_stop(ContextInfo, symbol):
    """Exact limit-up price from the instrument detail (probe: UpStopPrice).

    Literal getattr only (g2). None when the API is absent, raises, or carries
    no usable price -> caller stays fail-closed.
    """
    getter = getattr(ContextInfo, "get_instrument_detail", None)
    if not callable(getter):
        return None
    try:
        detail = getter(_qmt_code(symbol))
    except Exception:  # noqa: BLE001
        return None
    if not isinstance(detail, dict):
        return None
    value = detail.get("UpStopPrice")
    try:
        price = float(value)
    except (TypeError, ValueError):
        return None
    return price if price > 0 else None


def _bar_times(entry, n):
    """Timestamps of a market-data entry as strings, len n, or None.

    The QMT DataFrame exposes its time axis as `stime` (probe column listing)
    and/or as the DataFrame index. Both are read by LITERAL attribute/key, so
    the sandbox g2 rule (no variable-name getattr) is satisfied. A payload
    without any readable time axis yields None -> the caller cannot prove a
    14:30 reseal and stays fail-closed.
    """
    times = None
    try:
        column = getattr(entry, "stime", None)
        if column is None and isinstance(entry, dict):
            column = entry.get("stime")
        if column is not None:
            times = list(column)
    except (TypeError, ValueError):  # noqa: BLE001
        times = None
    if not times:
        try:
            index = getattr(entry, "index", None)
            if index is not None:
                times = list(index)
        except (TypeError, ValueError):  # noqa: BLE001
            times = None
    if not times or len(times) < n:
        return None
    return times


def _is_after_1430(stamp):
    """True iff a bar timestamp is 14:30 or later (Beijing wall clock).

    Accepts "202611171430" (YYYYMMDDHHMM), "20261117143000" (+SS),
    "2026-11-17 14:30:00" and "14:30". Pure string work: no date parser, no
    clock, no locale. A stamp carrying letters or too few digits is rejected
    (False) so junk can never manufacture a protective True.
    """
    text = str(stamp)
    # Only digits and common datetime separators are plausible; anything else
    # (letters, stray words) is unparseable and must not be read as a time.
    for ch in text:
        if not (ch.isdigit() or ch in "-: ."):
            return False
    digits = "".join(ch for ch in text if ch.isdigit())
    if len(digits) >= 12:
        hhmm = digits[8:12]        # YYYYMMDDHHMM(SS): positions 8-11 are HHMM
    elif len(digits) == 6:
        hhmm = digits[0:4]         # HHMMSS
    elif len(digits) == 4:
        hhmm = digits              # HHMM
    else:
        return False
    try:
        hour, minute = int(hhmm[:2]), int(hhmm[2:])
    except ValueError:
        return False
    if hour < 0 or hour > 23 or minute < 0 or minute > 59:
        return False
    return hour > 14 or (hour == 14 and minute >= 30)


def _afternoon_reseal_from(payload, symbol, up_stop, tol):
    """PURE: minute bars + exact limit price -> reseal bool, or None.

    None = cannot prove (no bars / no timestamps / no limit price) => the
    caller skips the sell. True = a bar at/after 14:30 closed at/above the
    limit (within tol) => protective skip. False = bars exist, none resealed.
    tol is the sealed-board tolerance fraction read from params.
    """
    if not up_stop or up_stop <= 0:
        return None
    entry = payload.get(symbol, payload) if isinstance(payload, dict) else None
    if entry is None:
        return None
    close_entry = getattr(entry, "close", None)
    if close_entry is None and isinstance(entry, dict):
        close_entry = entry.get("close")
    if close_entry is None:
        return None
    try:
        closes = [float(v) if v is not None else None for v in list(close_entry)]
    except (TypeError, ValueError):
        return None
    n = len(closes)
    if n == 0:
        return None
    times = _bar_times(entry, n)
    if times is None:                      # cannot prove timing -> fail closed
        return None
    threshold = up_stop * (1.0 - float(tol))
    for index in range(n):
        close = closes[index]
        if close is None:
            continue
        if _is_after_1430(times[index]) and close >= threshold:
            return True
    return False


def _afternoon_reseal(ContextInfo, symbol, tol, count):
    """Adapter: pull minute bars + UpStopPrice, then the pure judgment.

    Only called for held symbols by build_context, so the extra reads stay
    bounded (a handful of positions, not the whole 1000-name pool).
    """
    up_stop = _instrument_up_stop(ContextInfo, symbol)
    if up_stop is None:
        return None
    payload = _minute_bars_payload(ContextInfo, symbol, count)
    return _afternoon_reseal_from(payload, symbol, up_stop, tol)


def _mean_tail(values, n):
    vals = [v for v in values if v]
    if len(vals) < n:
        return None
    return sum(vals[-n:]) / float(n)


def _hist_bars(ContextInfo, symbol):
    """History bars via every signature this client family has shown, or None.

    Signatures differ per QMT build AND the 2026-11-03 real-machine probe proved
    this build has no usable history at all (`get_market_data_ex` returned an
    empty dict, `get_market_data` NoneType; only `get_full_tick` works). So the
    bridge-proven KEYWORD shape goes first (it was the one probe that returned
    a dict rather than an ArgumentError), the old positional shape second, and
    the caller must survive both failing.
    """
    getter = getattr(ContextInfo, "get_market_data_ex", None)
    if not callable(getter):
        return None
    fields = ["close", "volume", "high", "low", "open", "amount"]
    period = PARAMS.get("ma_period", "1d")
    count = int(PARAMS.get("bars_count", 80))
    code = _qmt_code(symbol)
    try:
        return _rekey_payload(getter(fields, [code], period=period, count=count), symbol)
    except Exception:
        pass
    try:
        return _rekey_payload(getter(fields, [code], period, "", "", count, True, True), symbol)
    except Exception:
        return None


def _ref_and_volume(ContextInfo, symbol, tick):
    """Reference price (MA of the baked window) and the volume ratio.

    History comes from ContextInfo.get_market_data_ex when the build exposes
    it. When it does not -- the 2026-11-09 live log showed this build rejecting
    every signature -- the reference falls back to the tick's own prev_close
    and the volume ratio to None: the pullback rule then judges against
    yesterday's close instead of inventing a level, and the ratio-only gates
    abstain. No download call happens here -- a blocking download inside
    handlebar is what killed the trading leg for a whole day on 2026-10-29.
    """
    bars = _hist_bars(ContextInfo, symbol)
    series = _series(bars, "close", symbol)
    volumes = _series(bars, "volume", symbol)
    if len(series) >= 2:
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
    # History unavailable on this build. PRECALC (2026-11-11 owner decision): when
    # the pre-open baked factor table (params.precalc, gz-side pregen from public
    # daily bars) covers this symbol, use it instead of abstaining -- ma_ref /
    # vol_ma5 are yesterday-anchored counter-side values, so the pullback rule and
    # the volume-ratio gates become computable with tick data alone. A symbol NOT
    # covered by precalc keeps the old honest fallback (ref=prev_close, ratio
    # abstains): we never invent a number. Warn once per PROCESS either way.
    precalc = (PARAMS.get("precalc") or {}).get(symbol) or {}
    prev_close = tick.get("prev_close")
    if precalc:
        ref = precalc.get("ma_ref") or prev_close
        volume = tick.get("volume")
        base = precalc.get("vol_ma5")
        ratio = (volume / base) if (volume and base) else None
        _warn_once("hist_precalc",
                   "no history series from this build: using params.precalc factors")
        return ref, ratio
    _warn_once("hist_unavailable",
               "no history series from this build: no precalc for this symbol, ref=prev_close fallback, volume ratio abstains")
    return prev_close, None


def _rekey_payload(payload, symbol):
    """Re-key a one-symbol market-data payload back to the BARE symbol.

    Reads now ask the client with 'CODE.SZ' (the only shape it resolves, see
    _qmt_code), but every downstream reader looks the payload up by the bare
    code we join on. Without this re-key the series reader falls back to the
    outer dict, finds no columns, and reports "no history" -- the same blindness
    relocated. Returns the payload untouched when the key is already bare.
    """
    if not isinstance(payload, dict):
        return payload
    code = _qmt_code(symbol)
    if code == symbol or code not in payload:
        return payload
    moved = {}
    for key in payload.keys():
        if key == code:
            moved[symbol] = payload.get(key)
        else:
            moved[key] = payload.get(key)
    return moved


def _series(payload, field, symbol):
    """Pull one numeric series out of whatever shape get_market_data_ex returned."""
    if not isinstance(payload, dict):
        return []
    entry = payload.get(symbol, payload)
    if field == "close":
        data = getattr(entry, "close", None)
    elif field == "volume":
        data = getattr(entry, "volume", None)
    elif field == "amount":
        # FLOW-VOL-20261120: the relative-volume leg is denominated in traded
        # AMOUNT (yuan), not share count -- the 11-19 box probe proved the
        # index `volume` column is in LOTS while `amount` is in yuan, so the
        # same-source ratio must read `amount` to stay unit-free.
        data = getattr(entry, "amount", None)
    else:
        return []
    if data is None and isinstance(entry, dict):
        data = entry.get(field)
    try:
        return [_to_float(item, 0.0) for item in list(data)]
    except TypeError:
        return []


# ---------------------------------------------------------------------------
# FLOW-QUADRANT / FLOW-BLEND (2026-11-20, owner ruling)
# ---------------------------------------------------------------------------
# Two readings feed (a) the single hard veto this carrier owns below the
# strategy gates and (b) the post-composition amount multiplier:
#
#   * relative volume (surge / shrink / neutral) -- computed IN-CLIENT from the
#     index daily bars. The 11-20 box probe proved `get_market_data_ex` serves
#     close/volume/amount for 1d/1m/5m, that the index `volume` column is in
#     LOTS while `amount` is in YUAN, and that the same source yields both. The
#     ratio is therefore same-source and must NEVER be rescaled by 100 (x100 is
#     only for cross-source reconciliation against cloud "shares" columns).
#   * money flow (market delta + per-sector delta) -- BAKED by the decision
#     host every 30 minutes, because this QMT build returns all eight money-flow
#     columns as NaN (same probe). A missing / cross-day / older-than-TTL
#     snapshot ABSTAINS: it is never folded into a fabricated zero.
#
# Thresholds mirror internal/signalctl/flow_regime.go (rYesSurge=1.2,
# rMa5Surge=1.0, rYesShrink=1.0, rMa5Shrink=0.9, posRelHigh=1.3) and
# internal/signalctl/flow_blend.go (three equal weights, renormalise on missing
# items, clamp(1+0.20*b, 0.80, 1.20)). Every threshold is read from PARAMS with
# NO fallback literal: derive_carrier_params.py writes them from the Go constants
# (internal/signalctl/derive_carrier_flow.go), and an absent knob abstains instead
# of silently re-using a hand-copied number. A second copy is how the box keeps
# trading the old threshold the day someone re-tunes the real one.
_FLOW_INDEX_CODES = ("000001.SH", "000300.SH")
_FLOW_BARS_COUNT = 8           # 6 needed (today + T-1..T-5); two spare rows
_FLOW_MINUTE_SCALE = 240.0     # A-share day = 120 AM + 120 PM trading minutes


def _bar_day(stamp):
    """YYYYMMDD out of a bar timestamp, or None (digit-only discipline).

    Accepts "202611171430", "20261117143000", "2026-11-17 14:30:00" and any
    datetime whose str() keeps the date in the leading digits. A stamp that
    cannot prove its DATE is rejected: treating an unproven last bar as history
    would splice an in-progress day into the baseline (a fabricated ratio), and
    treating it as today would double-count. Abstaining is the only safe branch.
    """
    text = str(stamp)
    for ch in text:
        if not (ch.isdigit() or ch in "-: ."):
            return None
    digits = "".join(ch for ch in text if ch.isdigit())
    if len(digits) < 8:
        return None
    return digits[0:8]


def _index_amount_payload(ContextInfo, code, count):
    """One index's daily bars (close/volume/amount), re-keyed to the bare code.

    Both client signatures are tried, in the same order _hist_bars uses (the
    keyword shape is the one the 2026-11-03 probe saw return a dict instead of
    an ArgumentError). The single returned entry is re-keyed to the BARE code, so
    a client that answers under `000001.SH` and one that answers under `000001`
    are read identically -- otherwise one build would report "no index bars"
    while the data is right there.
    """
    getter = getattr(ContextInfo, "get_market_data_ex", None)
    if not callable(getter):
        return None
    fields = ["close", "volume", "amount"]
    bare = _bare(code)
    try:
        payload = getter(fields, [code], period="1d", count=count)
    except Exception:  # noqa: BLE001 - shape mismatch, try the legacy one
        payload = None
    if not isinstance(payload, dict):
        try:
            payload = getter(fields, [code], "1d", "", "", count, True, True)
        except Exception:  # noqa: BLE001 - this build has no usable history
            return None
    if not isinstance(payload, dict):
        return None
    # Re-key ONLY this one entry, by the exact code we asked for. Deliberately
    # NOT _rekey_payload: that helper derives the suffixed code from the bare
    # one (_qmt_code), and for an INDEX `_qmt_code("000001")` guesses .SZ while
    # the answer came back as 000001.SH -- the re-key would silently miss and
    # report "no index bars" every round.
    if bare != code and code in payload:
        return {bare: payload.get(code)}
    return payload


def _trading_minutes_elapsed(moment):
    """Elapsed A-share trading minutes (240 = 120 AM + 120 PM).

    Byte-for-byte port of internal/combat_agent/intraday_ratio.go
    (tradingMinutesElapsed): pre-open 0, lunch 120, after close 240. The volume
    leg prorates today's cumulative amount by 240/elapsed, so elapsed<=0 must
    abstain (prorating an auction print would inflate the ratio 240x).
    """
    minutes = moment.hour * 60 + moment.minute
    am_start, am_end = 9 * 60 + 30, 11 * 60 + 30
    pm_start, pm_end = 13 * 60, 15 * 60
    if minutes < am_start:
        return 0.0
    if minutes <= am_end:
        return float(minutes - am_start)
    if minutes <= pm_start:
        return 120.0
    if minutes <= pm_end:
        return 120.0 + float(minutes - pm_start)
    return _FLOW_MINUTE_SCALE


def _flow_param(key):
    """One baked flow knob as a float; an absent/garbage key yields None (abstain).

    Deliberately NO default value. Every one of these numbers has exactly one
    home -- the Go constant, re-exported by internal/signalctl/derive_carrier_flow.go
    and written into the parameter file by derive_carrier_params.py. Writing a
    fallback here would be a second copy that keeps trading on the old number the
    day someone re-tunes the real one (the "two books, both look right" defect).
    An absent knob therefore abstains: volume leg -> neutral, quadrant -> Neutral,
    blend -> 1.0. None of those change behaviour, and none of them invent a number.
    """
    value = PARAMS.get(key)
    if value is None:
        return None
    try:
        number = float(value)
    except (TypeError, ValueError):
        return None
    if not math.isfinite(number):
        return None
    return number


def _vol_state_of(r_yest, r_ma5):
    """surge / shrink / neutral from the two relative-volume gates.

    Any unreadable or non-positive ratio -> neutral (never "unknown = surge"),
    exactly like signalctl.VolStateOf. A missing baked knob also yields neutral:
    without a threshold there is no defensible state, and "neutral" is the only
    answer that neither blocks nor amplifies anything.
    """
    if r_yest is None or r_ma5 is None or r_yest <= 0 or r_ma5 <= 0:
        return "neutral"
    r_yes_surge = _flow_param("flow_r_yest_surge")
    r_ma5_surge = _flow_param("flow_r_ma5_surge")
    r_yes_shrink = _flow_param("flow_r_yest_shrink")
    r_ma5_shrink = _flow_param("flow_r_ma5_shrink")
    if None in (r_yes_surge, r_ma5_surge, r_yes_shrink, r_ma5_shrink):
        return "neutral"
    if r_yest > r_yes_surge and r_ma5 > r_ma5_surge:
        return "surge"
    if r_yest < r_yes_shrink and r_ma5 < r_ma5_shrink:
        return "shrink"
    return "neutral"


def _finite_pos(value):
    """A strictly positive finite float, or None (abstain).

    NaN must not slip through a `<= 0` test: NaN compares False against every
    bound, so a NaN amount would sail past the history check, poison both
    ratios into NaN, and then `_vol_state_of` would report "neutral" -- i.e. a
    missing reading would masquerade as a REAL flat-volume zero and be counted
    into the blend at full weight. That is the "abstain = neutral" defect this
    carrier exists to avoid, so the check is explicit.
    """
    number = _to_float(value, None)
    if number is None:
        return None
    if not math.isfinite(number) or number <= 0.0:
        return None
    return number


def _vol_ratio_state(ContextInfo, moment):
    """Round-scoped relative-volume reading, or an honest abstain.

    r_yest = prorated today / T-1 and r_ma5 = prorated today / mean(T-1..T-5)
    (VolRatiosIntraday). Today comes from the last daily bar when that bar is
    provably TODAY; otherwise the index tick's cumulative `amount` supplies it
    and every bar is history. Fail-closed: pre-open, fewer than 5 history bars,
    any non-positive amount, or an unprovable bar date -> reason is recorded and
    every delta abstains. The cache is keyed by day+HHMM, so a 1000-name round
    reads the index series once a minute, not once per symbol.
    """
    day = day_key(moment)
    cache_key = "%s|%s" % (day, hhmm_now(moment))
    cached = _STATE.get("vol_ratio_state")
    if isinstance(cached, dict) and cached.get("key") == cache_key:
        return cached
    state = {"key": cache_key, "r_yest": None, "r_ma5": None,
             "vol_state": "neutral", "delta": None, "source": "", "reason": ""}
    elapsed = _trading_minutes_elapsed(moment)
    if elapsed <= 0.0:
        state["reason"] = "vol_premarket"
    else:
        for code in _FLOW_INDEX_CODES:
            payload = _index_amount_payload(ContextInfo, code, _FLOW_BARS_COUNT)
            if not isinstance(payload, dict):
                state["reason"] = "vol_no_bars:%s" % code
                continue
            bare = _bare(code)
            amounts = _series(payload, "amount", bare)
            if len(amounts) < 6:
                state["reason"] = "vol_short_series:%s" % code
                continue
            entry = payload.get(bare, payload)
            times = _bar_times(entry, len(amounts))
            if times is None:
                state["reason"] = "vol_no_timestamps:%s" % code
                continue
            last_day = _bar_day(times[-1])
            if last_day is None:
                state["reason"] = "vol_unparsed_timestamp:%s" % code
                continue
            if last_day == day:
                today_raw = amounts[-1]
                hist_raw = list(reversed(amounts[:-1]))
                source = "%s:bar" % code
            else:
                tick = _tick_of(ContextInfo, code)
                today_raw = tick.get("amount")
                hist_raw = list(reversed(amounts))
                source = "%s:tick" % code
            today_amount = _finite_pos(today_raw)
            if today_amount is None:
                state["reason"] = "vol_no_today_amount:%s" % code
                continue
            if len(hist_raw) < 5:
                state["reason"] = "vol_short_history:%s" % code
                continue
            hist = [_finite_pos(v) for v in hist_raw[:5]]
            if any(v is None for v in hist):
                state["reason"] = "vol_bad_history:%s" % code
                continue
            prorated = today_amount * (_FLOW_MINUTE_SCALE / elapsed)
            r_yest = prorated / hist[0]
            r_ma5 = prorated / (sum(hist) / 5.0)
            state.update({"r_yest": r_yest, "r_ma5": r_ma5,
                          "source": source, "reason": ""})
            state["vol_state"] = _vol_state_of(r_yest, r_ma5)
            if state["vol_state"] == "surge":
                state["delta"] = 1.0
            elif state["vol_state"] == "shrink":
                state["delta"] = -1.0
            else:
                state["delta"] = 0.0
            break
    _STATE["vol_ratio_state"] = state
    return state


def _clamp_delta(value):
    """Any JSON value -> a delta in [-1,1], or None (abstain)."""
    if value is None:
        return None
    try:
        delta = float(value)
    except (TypeError, ValueError):
        return None
    if math.isnan(delta):
        return None
    if delta < -1.0:
        return -1.0
    if delta > 1.0:
        return 1.0
    return delta


def _flow_snapshot_state(moment):
    """Decode the baked money-flow snapshot, or abstain on any staleness.

    Freshness is TWO gates, both required: the snapshot's Beijing day must equal
    today, and its size must be younger than flow_snapshot_ttl_sec (default 45
    min, the publisher re-bakes every 30). A missing key, a cross-day key or an
    expired key leaves every delta None -> the blend renormalises and the hard
    veto cannot fire. Cached by day+HHMM like the volume leg.
    """
    cache_key = "%s|%s" % (day_key(moment), hhmm_now(moment))
    cached = _STATE.get("flow_snapshot_state")
    if isinstance(cached, dict) and cached.get("key") == cache_key:
        return cached
    state = {"key": cache_key, "market_delta": None, "sector_delta": {},
             "market_state": None, "max_pos_pct": None, "source": "", "reason": ""}
    snap = PARAMS.get("flow_snapshot")
    if not isinstance(snap, dict):
        state["reason"] = "flow_snapshot_missing"
    elif str(snap.get("day") or "") != day_key(moment):
        state["reason"] = "flow_snapshot_stale_day"
    else:
        baked_at = _to_float(snap.get("baked_at"), 0.0)
        ttl = _to_float(PARAMS.get("flow_snapshot_ttl_sec"), 0.0) or 0.0
        age = None
        if baked_at > 0:
            age = _now_ts() - baked_at
            if age < 0:
                # The baker lives on the decision host and the reader on the box,
                # so a small NEGATIVE age only means the two clocks disagree by a
                # few seconds. Treating that as "expired" would abstain all day on
                # a skew the day check already guards against; clamp to 0 instead.
                age = 0.0
        if age is None:
            state["reason"] = "flow_snapshot_no_timestamp"
        elif ttl > 0 and age > ttl:
            state["reason"] = "flow_snapshot_expired"
        else:
            state["market_delta"] = _clamp_delta(snap.get("market_delta"))
            raw_sectors = snap.get("sector_delta")
            sectors = {}
            if isinstance(raw_sectors, dict):
                for name, value in raw_sectors.items():
                    delta = _clamp_delta(value)
                    if delta is not None:
                        sectors[str(name)] = delta
            state["sector_delta"] = sectors
            state["market_state"] = snap.get("market_state")
            state["max_pos_pct"] = _to_float(snap.get("max_pos_pct"), None)
            state["source"] = str(snap.get("source") or "")
    _STATE["flow_snapshot_state"] = state
    return state


def _sector_delta_for(symbol, snap):
    """Per-stock sector delta through the baked `sector_map` (absent map -> None).

    WHY A DEDICATED MAP, NOT `industry_map` (2026-11-20 live finding): the baked
    `industry_map` holds the CSRC industry taxonomy (values like
    "C39 computer/communication/electronics manufacturing", 79 distinct values,
    from baostock / stocks.industry), while `sector_delta`'s keys are EastMoney
    BOARD names straight out of `clist` f14. The two taxonomies have an EMPTY
    intersection, so joining through `industry_map` would look wired and match
    nothing -- a silent 0% leg, which is this repo's most expensive shape.
    (`industry_map` keeps its real job: the concentration buckets.)

    So the join key is a separate, purpose-built `sector_map` (code -> EM board
    name), produced daily on the decision host by
    `scripts/pregen_sector_map_gz.py` (industry boards first, then concepts)
    and baked into params by the hot-reload chain. When today's map is absent
    or stale this leg ABSTAINS (None) and the blend renormalises onto market +
    volume; the hourly readout prints the hit/miss tally so "0%" is visible
    instead of inferred.
    """
    table = snap.get("sector_delta") or {}
    if not table:
        return None
    maps = PARAMS.get("sector_map") or {}
    if not maps:
        return None
    industry = maps.get(symbol)
    if not industry:
        industry = maps.get(_qmt_code(symbol))
    if not industry:
        industry = maps.get(_bare(symbol))
    if not industry:
        return None
    return _clamp_delta(table.get(str(industry)))


def _bump_sector_tally(hit):
    """Count one per-stock sector lookup into the HOURLY tally (diagnostics only).

    Deliberately hour-scoped and accumulated across rounds: the readout line is
    printed at the top of a round, before any sizing (and therefore before any
    lookup), so a per-round counter would ALWAYS print 0/0 -- a fake reading that
    looks like "the leg is dead" every single hour. The tally is reset by the
    readout itself, so one line = "this hour, N of M names matched".
    """
    moment = _STATE.get("round_moment")
    key = "" if moment is None else "%s_%d" % (day_key(moment), moment.hour)
    tally = _STATE.get("flow_tally")
    if not isinstance(tally, dict) or tally.get("hour") != key:
        tally = {"hour": key, "hit": 0, "miss": 0}
        _STATE["flow_tally"] = tally
    field = "hit" if hit else "miss"
    tally[field] = int(tally.get(field) or 0) + 1


def _book_market_value(positions):
    """Held market value from the counter rows (0 when nothing is readable)."""
    total = 0.0
    for _symbol, row in (positions or {}).items():
        if not isinstance(row, dict):
            continue
        qty = _to_float(row.get("qty"), 0.0)
        if qty <= 0.0:
            continue
        value = _to_float(row.get("market_value"), 0.0)
        if value <= 0.0:
            value = qty * _to_float(row.get("cost"), 0.0)
        if value > 0.0:
            total += value
    return total


def _quadrant_state(vol, snap, held, total):
    """Four-quadrant decision (signalctl.DecideQuadrant) + the ONE hard gate.

    owner ruling 2026-11-19/20: EVERY quadrant except "forbidden bottom-fishing"
    lands in the amount blend, never in a hard gate. So `block` is TRUE only for
    bear + surge + high posRel (posRel>=flow_posrel_high on the relative target
    tier, never an absolute 70%). range / unknown state / unknown target ->
    Neutral. The bull cases are still COMPUTED and reported (reduce / no-add)
    so the readout shows why the blend moved, but they do not veto.
    """
    state = {"decision": "neutral", "reason": "neutral", "pos_rel": 0.0,
             "vol_state": vol.get("vol_state") or "neutral", "block": False}
    market_state = snap.get("market_state")
    target = snap.get("max_pos_pct")
    total_pct = None
    if total is not None and total > 0 and held is not None:
        total_pct = held / total
    if total_pct is not None and target is not None and target > 0:
        state["pos_rel"] = total_pct / target
    else:
        state["pos_rel"] = 0.0
    if market_state not in ("bull", "bear"):
        state["reason"] = "regime_not_quadrant"
        return state
    high = _flow_param("flow_posrel_high")
    if high is None:
        # No baked threshold = no defensible "high tier" answer. Neutral, and the
        # reason names the missing knob instead of silently using 1.3.
        state["reason"] = "flow_knob_missing"
        return state
    if state["pos_rel"] < high:
        state["reason"] = "pos_not_high"
        return state
    vol_state = state["vol_state"]
    if market_state == "bear" and vol_state == "surge":
        state["decision"] = "block_buy"
        state["reason"] = "bear_surge_high_nobottom"
        state["block"] = True
    elif market_state == "bull" and vol_state == "surge":
        state["decision"] = "reduce"
        state["reason"] = "bull_surge_high_reduce"
    elif market_state == "bull" and vol_state == "shrink":
        state["decision"] = "block_buy"
        state["reason"] = "bull_shrink_high_noadd"
    return state


def _flow_round_prepare(ContextInfo, positions, total, moment):
    """Compute the round-scoped flow readings ONCE (volume, snapshot, quadrant).

    Called from run_round before any signal is judged; sizing_cap and the buy
    gate read the cached result so a 1000-name round pays for one index read and
    one snapshot decode.

    NEVER RAISES, on purpose. This leg talks to the client (get_market_data_ex /
    get_full_tick), and a readout leg that can abort the round is exactly how
    2026-11-19 lost a whole day: a NameError inside a cash *probe* aborted every
    round at the first buy candidate and took the sell legs with it. A broken
    flow reading must therefore degrade to "abstain", never to "no trading".
    """
    vol = {"vol_state": "neutral", "delta": None, "r_yest": None, "r_ma5": None,
           "source": "", "reason": "flow_not_prepared"}
    if ContextInfo is not None and moment is not None:
        try:
            vol = _vol_ratio_state(ContextInfo, moment)
        except Exception as exc:  # noqa: BLE001 - a readout must never stop the round
            _warn_once("flow_vol", "vol ratio raised: %s" % repr(exc)[:120])
            vol = {"vol_state": "neutral", "delta": None, "r_yest": None, "r_ma5": None,
                   "source": "", "reason": "vol_read_failed"}
    snap = {"sector_delta": {}}
    quadrant = {"decision": "neutral", "reason": "flow_not_prepared",
                "pos_rel": 0.0, "vol_state": "neutral", "block": False}
    try:
        snap = _flow_snapshot_state(moment) if moment is not None else {"sector_delta": {}}
        held = _book_market_value(positions)
        quadrant = _quadrant_state(vol, snap, held, total)
    except Exception as exc:  # noqa: BLE001 - same reason as above
        _warn_once("flow_snap", "flow snapshot/quadrant raised: %s" % repr(exc)[:120])
        held = 0.0
    _STATE["round_flow"] = {
        "moment_key": ("%s|%s" % (day_key(moment), hhmm_now(moment))) if moment is not None else "",
        "vol": vol, "snap": snap, "quadrant": quadrant,
        "held": held, "total": total,
    }
    return _STATE["round_flow"]


def _flow_blend_for(symbol, moment):
    """Post-composition amount multiplier in [0.80,1.20]; 1.0 = zero change.

    b = mean(available deltas among market / sector / volume), blend =
    clamp(1 + 0.20*b, 0.80, 1.20). Missing items are DROPPED (renormalise), a
    real zero is KEPT -- 0 means "flat volume" and skipping it would silently
    give the remaining legs a bigger weight. All three missing -> 1.0, and
    flow_blend_pct=0 is the operator's escape hatch (blend becomes exactly 1.0)
    -- there is deliberately no second on/off switch that could disagree with it.
    """
    round_flow = _STATE.get("round_flow") or {}
    key = ""
    if moment is not None:
        key = "%s|%s" % (day_key(moment), hhmm_now(moment))
    if round_flow.get("moment_key") != key:
        round_flow = {}
    snap = round_flow.get("snap")
    if not isinstance(snap, dict):
        snap = _flow_snapshot_state(moment) if moment is not None else {"sector_delta": {}}
    vol = round_flow.get("vol")
    if not isinstance(vol, dict):
        vol = {}
    parts = []
    market = snap.get("market_delta")
    if market is not None:
        parts.append(float(market))
    sector = _sector_delta_for(symbol, snap)
    if sector is not None:
        parts.append(float(sector))
        _bump_sector_tally(True)
    elif snap.get("sector_delta"):
        # Only a real miss (table present, this name unmatched) is counted; an
        # empty table means "no sector flow today", which is a different fact.
        _bump_sector_tally(False)
    surge = vol.get("delta")
    if surge is not None:
        parts.append(float(surge))
    if not parts:
        return 1.0
    total = sum(parts) / float(len(parts))
    pct = _flow_param("flow_blend_pct")
    lo = _flow_param("flow_blend_lo")
    hi = _flow_param("flow_blend_hi")
    if pct is None or lo is None or hi is None:
        # No baked amplitude := no multiplier. 1.0 is the "zero behaviour change"
        # answer, and it is NOT a fabricated reading: the blend simply does not run.
        return 1.0
    blend = 1.0 + pct * total
    if blend < lo:
        blend = lo
    if blend > hi:
        blend = hi
    return blend


def _flow_readout():
    """One ASCII line naming both legs and the quadrant verdict (hourly).

    Bounded on purpose: a per-round line would train the operator to ignore it.
    An abstaining leg prints its reason, so "no flow today" is distinguishable
    from "flow says neutral".
    """
    round_flow = _STATE.get("round_flow") or {}
    vol = round_flow.get("vol") or {}
    snap = round_flow.get("snap") or {}
    quadrant = round_flow.get("quadrant") or {}
    # sector_hit/miss makes the per-stock sector join VISIBLE: a wired-but-never-
    # matching leg prints 0/N every hour instead of quietly abstaining forever.
    # The tally is the PREVIOUS hour's (this line is emitted before any lookup in
    # this round); it is reset here so the next line reports one hour only.
    tally = _STATE.get("flow_tally") or {}
    hit = int(tally.get("hit") or 0)
    miss = int(tally.get("miss") or 0)
    _STATE["flow_tally"] = {"hour": "", "hit": 0, "miss": 0}
    _log("flow vol_state=%s r_yest=%s r_ma5=%s src=%s reason=%s market_delta=%s sectors=%d "
         "sector_hit=%d/%d state=%s pos_rel=%.3f decision=%s qwhy=%s" % (
             vol.get("vol_state") or "neutral",
             "na" if vol.get("r_yest") is None else "%.4f" % float(vol.get("r_yest")),
             "na" if vol.get("r_ma5") is None else "%.4f" % float(vol.get("r_ma5")),
             vol.get("source") or "-", vol.get("reason") or "-",
             "na" if snap.get("market_delta") is None else "%.4f" % float(snap.get("market_delta")),
             len(snap.get("sector_delta") or {}),
             hit,
             hit + miss,
             snap.get("market_state") or "-",
             float(quadrant.get("pos_rel") or 0.0),
             quadrant.get("decision") or "neutral",
             quadrant.get("reason") or "-"))


def _spent_buy_amount(orders, buy_prefixes, day):
    """Today's committed buy money = counter truth + session journal, never both.

    BUDGET-RELEASE (owner reports 2026-11-19, two halves):

      * "pending orders occupy the budget" -- the unfilled remainder of a WORKING
        buy is committed money, so a second signal cannot spend it again;
      * "after a manual cancel the budget must come back" -- a terminal row
        (filled / cancelled / rejected) contributes only what it actually
        traded, so the cancel releases its remainder on the very next round.

    The counter ORDER table is seconds late, so the session journal covers the
    symbols it has not shown yet. Counting both would charge one intent twice;
    counting neither would under-charge the round that just sent it. Before
    2026-11-19 this function looked up the CURRENT symbol only, which is how a
    cancelled order's amount stayed charged to that name for the whole day.

    Side comes from the counter row (`_order_side`); the baked prefix list is
    only a fallback for builds that do not expose the side field. An unproven
    side is treated as a BUY: over-reserving only refuses buys, while
    under-reserving is the overshoot this exists to stop.
    """
    spent = 0.0
    seen_symbols = set()
    for row in orders or []:
        sym = _bare(row.get("symbol"))
        if sym:
            seen_symbols.add(sym)
        side = str(row.get("side") or "")
        if side and side != "buy":
            continue
        short = str(row.get("short_code") or "")
        if not side and not any(short.startswith(prefix) for prefix in buy_prefixes):
            continue
        price = float(row.get("price") or 0.0)
        spent += float(row.get("traded") or 0.0) * price
        if row.get("terminal") is False:
            remain = float(row.get("qty") or 0.0) - float(row.get("traded") or 0.0)
            if remain > 0.0:
                spent += remain * price
    for entry, amount in list((_STATE.get("buy_amounts") or {}).items()):
        if entry[0] != day or _bare(entry[1]) in seen_symbols:
            continue
        spent += float(amount or 0.0)
    return spent


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
    spent = _spent_buy_amount(orders, buy_prefixes, day)
    # 2026-11-11 owner: bars come FROM QMT (download_history_data2 + get_market_data_ex)
    # so the ported form/ladder/factor tactics finally have their day-series inputs.
    # Derived features mirror the Go adapters (ma/vol windows over the last bars,
    # today's tick volume vs the 5-day average). precalc passthrough covers ONLY
    # what QMT cannot provide (valuation/turnover/fin/flow/news=D1/concept heat).
    # PRECALC-SERIES (2026-11-16 owner): when the client's own history is short
    # of the factor windows, the pregen sequence replaces it WHOLE (never
    # spliced -- different adjustment bases must not meet at a seam) and one
    # synthetic today-bar from the live tick is appended. A symbol with NEITHER
    # source keeps the honest abstain.
    bars = _bar_dicts_of(_hist_bars(ContextInfo, symbol), symbol)
    if len(bars) < 20:
        bars = _precalc_bars(symbol, tick)
    closes = [b["close"] for b in bars if b.get("close")]
    vols = [b["volume"] for b in bars if b.get("volume")]
    ma5 = _mean_tail(closes, 5)
    ma20 = _mean_tail(closes, 20)
    vol5 = _mean_tail(vols, 5)
    tick_volume = tick.get("volume")
    today_vol_vs_5d = (tick_volume / vol5) if (tick_volume and vol5) else None
    _STATE.setdefault("bars_cov", {})[symbol] = 1 if bars else 0
    pre = (PARAMS.get("precalc") or {}).get(symbol) or {}
    # AFTERNOON-RESEAL (2026-11-17): compute the protective reading in-client
    # for HELD names only (leader_decay fires on positions). Failed reads stay
    # None -> the sell is skipped (owner ruling: never infer "not resealed"
    # from missing minute data). Non-held names keep the baked passthrough.
    reseal = pre.get("afternoon_reseal")
    if held.get("qty") and reseal is None:
        reseal = _afternoon_reseal(ContextInfo, symbol,
                                   PARAMS.get("ld_seal_tolerance", 0.995),
                                   PARAMS.get("bars_count", 80))
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
        "amount": tick.get("amount"),
        "ask1": tick.get("ask1"),
        "bid1": tick.get("bid1"),
        "ref_price": ref if ref is not None else tick.get("prev_close"),
        "volume_ratio": ratio,
        "position_qty": held.get("qty"),
        "position_cost": held.get("cost"),
        "can_use_qty": held.get("can_use"),
        "available_cash": cash,
        "daily_buy_total": spent,
        "ops_last_minute": _ops_budget_used(),
        "bars": bars,
        "ma5": ma5,
        "ma20": ma20,
        "today_vol_vs_5d": today_vol_vs_5d,
        "valuation_series": pre.get("valuation_series"),
        "turnover_series": pre.get("turnover_series"),
        "total_share_series": pre.get("total_share_series"),
        "fin_pit": pre.get("fin_pit"),
        "flow_today_pct": pre.get("flow_today_pct"),
        "flow_5d_pct": pre.get("flow_5d_pct"),
        # SIZING-FLOW-ALIAS 2026-11-20: rules_sizing.SIZING_CTX_KEYS declares
        # flow_ratio_today / flow_ratio_5d (the two names both Go sizing factors
        # read), while this builder only ever wrote flow_today_pct / flow_5d_pct
        # -- so the whole net-inflow factor leg was structurally abstaining on the
        # box. The baked values are already PERCENT net-inflow rates over
        # circulating market cap (pregen: MainNet / circ_mv * 100), exactly the
        # unit the baked flow_*_scales bands expect, so this is an alias, not a
        # conversion. Absent stays None = honest abstain (flow_factor.go:88-113).
        "flow_ratio_today": pre.get("flow_today_pct"),
        "flow_ratio_5d": pre.get("flow_5d_pct"),
        # C1 (2026-11-17): good_news_fade reads ctx["news_event"] as a DICT
        # {score, positive, propagation, day}; the R9 bearish gate at ~:1958 reads
        # the PRECALC news_event float directly (pre.get("news_event")), so the two
        # consumers must not collide. We keep precalc.news_event a signed float and
        # build the dict here from it + the baked news_day/news_propagation. Absent
        # extras -> the dict still has score, and the rule declines on missing
        # propagation/day (honest), never on a fabricated value.
        "news_event": _news_event_obj(pre),
        "concept_heat": pre.get("concept_heat"),
        # LIMIT-BOARD (2026-11-16 owner): lu_board3/lu_board scalar proxies read
        # ctx["limit_board_count"] by input_key -- baked by pregen from the EM
        # limit-up pool. Absent = declined (honest), same as the engine side.
        "limit_board_count": pre.get("limit_board_count"),
        # C1-LEADER 2026-11-17: leader_decay/leader_decay_v2 and the emotion/limit-up
        # scalar proxies consumed ctx keys that build_context NEVER wrote, so those
        # LIVE rules were structurally dead on the box. Each name mirrors the factor's
        # input_key; pregen bakes it from fresh daily tables (limit-up count, ladder,
        # broken board, sector top stocks, index change, seal microstructure). Absent
        # = None = honest decline -- never coerce to 0/False, that reopens the
        # "abstain=allow" defect that filled the account at 98.9%.
        "is_leader": pre.get("is_leader"),
        "ladder_height": pre.get("ladder_height"),
        # CONSEC-BOARDS 2026-11-17: leader_decay_v2's OTHER protective reading --
        # per-stock consecutive limit-ups before today, baked from daily+stk_limit
        # (pool ladder_height is NOT a substitute). Absent = honest decline.
        "consec_boards": pre.get("consec_boards"),
        "afternoon_reseal": reseal,
        "up_stop": pre.get("up_stop"),
        "index_change_pct": pre.get("index_change_pct"),
        "limit_up_count": pre.get("limit_up_count"),
        "limit_up_count_history": pre.get("limit_up_count_history"),
        "broken_board_ratio": pre.get("broken_board_ratio"),
        "broken_board_count": pre.get("broken_board_count"),
        "broken_board_ratio_history": pre.get("broken_board_ratio_history"),
        "seal_amount_ratio": pre.get("seal_amount_ratio"),
        "first_seal_minute": pre.get("first_seal_minute"),
        "sector_limit_up_drop_pct": pre.get("sector_limit_up_drop_pct"),
        "emotion_phase": pre.get("emotion_phase"),
        # as-of stamp of the authoritative emotion snapshot (the ths pool session it
        # was aggregated from); carried so a reader can see the emotion scalar is a
        # prior-session baseline, not a live intraday count.
        "emotion_day": pre.get("emotion_day"),
    }


def _news_event_obj(pre):
    """Build the good_news_fade dict shape from a flat precalc entry.

    score comes from the signed float precalc.news_event (the engine's authoritative
    D1); positive derives from its sign; propagation/day come from baked extras.
    Returns None when there is no readable score so good_news_fade declines rather
    than treating "no news" as "news with score 0".
    """
    score = pre.get("news_event")
    if score is None or not isinstance(score, (int, float)):
        return None
    day = pre.get("news_day")
    propagation = pre.get("news_propagation")
    if day is None and propagation is None:
        # score present but event metadata baked nowhere: good_news_fade needs
        # propagation+day, so abstain rather than fabricate (the rule itself also
        # declines on None, this just keeps one source of truth for the shape).
        return None
    return {
        "score": float(score),
        "positive": float(score) > 0.0,
        "propagation": propagation,
        "day": str(day)[:8] if day else None,
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


def _sizing_view(ctx, positions, pending_buys=None):
    """The account-level view the sizing chain needs on top of the per-symbol ctx.

    2026-11-10 rebuild: the buckets this view used to leave empty
    (stock_value / pending_buys / industry_known / cluster_set / the T legs)
    are exactly why every concentration cap abstained while the account went
    to 98.9% one name. They are computed per round now (see _bucket_view);
    market dims (emotion phase, breadth, index change) remain ABSENT on
    purpose -- not computable from a tick-only sandbox, they land the round in
    the blind spot, which caps the target at missing_key_target_cap. That is
    the designed consequence of "no external data inside the sandbox".
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
    # T legs: this carrier does not place T legs itself (t orchestration is a
    # separate batch), so both are honestly zero -- never absent, which the
    # target layer would read as an unprovable account.
    out.setdefault("t_buy_leg_value", 0.0)
    out.setdefault("t_sell_leg_value", 0.0)
    bucket = out.get("bucket_view") or {}
    for key in ("stock_value", "industry_known", "industry_value",
                "cluster_set", "cluster_value"):
        if key in bucket:
            out[key] = bucket[key]
    # 2026-11-13: feed the existing dynamic-sizing chain with REAL open buy
    # orders. rules_sizing.py:94-99 states this journal must be filled before
    # the next order is sized, otherwise the same-round room is stale and the
    # account gets filled in one round. It was an empty list unconditionally.
    out["pending_buys"] = list(pending_buys or [])
    return out


def _probe_cash_due():
    """CASH-PROBE gate: true once every _PROBE_ROUND_EVERY rounds, for the first
    new candidate only. Bounded so the line can never slow a round or flood the
    log; pure read of a session counter, no trading state touched."""
    seq = int(_STATE.get("cash_probe_seq") or 0) + 1
    _STATE["cash_probe_seq"] = seq
    return (seq - 1) % _PROBE_ROUND_EVERY == 0 and not _STATE.get("cash_probe_done")


def _probe_cash_line(short, amount, why, note, ctx, params):
    """CASH-PROBE readout (read-only): name the exact sizing verdict and the
    money caps it was built from, so 'sizing_amount_unavailable' stops being an
    opaque reason. Marks done for this round via the caller's flag."""
    _STATE["cash_probe_done"] = True
    cap = ""
    basis = ""
    try:
        sized_view = _sizing_view(ctx, _STATE.get("cash_probe_positions") or {}, None)
        names = buy_size_caps(params, sized_view)
        cap = ",".join("%s=%s" % (k, "na" if v is None else "%.0f" % float(v))
                       for k, v in sorted(names.items()))
        # 2026-11-19 owner asked: how is today's "48.5% position cap" computed? The caps line alone cannot
        # answer that -- target = base * min(MarketScale, ceiling) and room = target -
        # (held + pending) has FOUR inputs and none of them was printed. Print them so
        # the derivation is arithmetic on the log line, not an inference.
        # Pure readout: target_position/_sz_position_value are the SAME functions the
        # decision used, and any failure here degrades to na (never blocks a round).
        target = target_position(params, sized_view) or {}
        held, pending, _bl, _sl, okv = _sz_position_value(sized_view)
        basis = "base=%s ms=%s tgt=%s held=%s pend=%s room=%s" % (
            "na" if target.get("total") is None else "%.0f" % float(target.get("total")),
            "na" if target.get("market_scale") is None else "%.4f" % float(target.get("market_scale")),
            "na" if target.get("target") is None else "%.0f" % float(target.get("target")),
            "na" if (not okv or held is None) else "%.0f" % float(held),
            "na" if (not okv or pending is None) else "%.0f" % float(pending),
            "na" if target.get("room") is None else "%.0f" % float(target.get("room")))
    except Exception as exc:  # noqa: BLE001 - readout only, never a decision
        cap = cap or "err:%s" % repr(exc)[:60]
        basis = basis or "basis_err:%s" % repr(exc)[:60]
    _log("cash_probe sym=%s amt=%s why=%s note=%s cash=%s eq=%s caps=%s %s" % (
        short, "na" if amount is None else "%.0f" % amount, why or "", note or "",
        "na" if (ctx or {}).get("available_cash") is None else "%.0f" % float(ctx.get("available_cash") or 0.0),
        "na" if (ctx or {}).get("total_asset") is None else "%.0f" % float(ctx.get("total_asset") or 0.0),
        cap, basis))


def sizing_cap(ctx, positions, intent="new", pending_buys=None):
    """Cap one buy order with the dynamic-sizing chain. Returns (amount, reason, note).

    amount None means "send nothing", never "no limit" -- that is buy_size_amount's
    own contract and the reason this leg can be trusted to fail closed.
    sizing_enabled=false is the legacy escape hatch (the Go side has the same
    master switch; this box never invents its own default).

    2026-11-10 Q7 ruling: add / t_leg intents are EXEMPT from the min-order-
    amount floor (new-position shreds are what the floor exists to stop). For
    those intents the amount is min(buy_size_caps) WITHOUT the floor refusal;
    every other cap in the chain still binds.

    2026-11-19 owner ruling ("10000 is the FLOOR of opening a position -- if it
    comes out under 10000, top it up to 10000"): a NEW position that the dynamic
    chain shreds below the floor is raised to the floor instead of refused. Only
    the per-trade knob is bypassed by that top-up; every hard cap still binds
    (cash, per-order cap, daily remaining, target room, industry / single-name /
    cluster), so a 4000 cash line still refuses -- it must not overdraw.
    The note "floor_topup:<allow>" carries the ceiling the top-up was checked
    against, so the share-count rounding in run_round can add the one lot it
    needs without crossing it.
    """
    if not bool(PARAMS.get("sizing_enabled", False)):
        return None, "sizing_disabled", ""
    sized = _sizing_view(ctx, positions, pending_buys)
    caps = buy_size_caps(PARAMS, sized)
    # FLOW-BLEND 2026-11-20: the four-quadrant reading lands on the AMOUNT, not
    # on a gate (owner ruling). Multiplying per_trade here is the carrier-side
    # equivalent of Go's ComputeSizingWithBlend wrapping the same chain: the
    # ported rules_sizing.buy_size_caps stays byte-identical to its Go source
    # (parity is the whole point of that file), and only this one cap is scaled.
    # Everything else (cash / per-order / daily remaining / target room /
    # industry / single name / cluster) still binds, so the blend can only make
    # an order SMALLER or larger within +-20%, never bypass a hard cap.
    # A negative blend can push the order under min_order_amount: that is NOT a
    # bug, the floor top-up below then raises it back to the 10000 opening floor
    # while still respecting every hard cap (a 4000-cash account still refuses).
    if caps.get("per_trade") is not None:
        blend = _flow_blend_for(ctx.get("symbol"), _STATE.get("round_moment"))
        if blend != 1.0:
            base = sized.get("total_asset")
            if base is None:
                base = sized.get("equity")
            base = _to_float(base, 0.0)
            scaled = float(caps["per_trade"]) * blend
            # Go PerTradePctOfBlend clamps the share to [0, 0.5]; mirroring it on
            # the AMOUNT keeps the two carriers on the same hard ceiling.
            if base > 0.0 and scaled > base * 0.5:
                scaled = base * 0.5
            if scaled < 0.0:
                scaled = 0.0
            caps["per_trade"] = scaled
            _STATE["flow_blend_note"] = "per_trade*%.4f" % blend
    values = [v for v in caps.values() if v is not None]
    if not values:
        return None, "sizing_amount_unavailable", ""
    amount = min(values)
    if intent in ("add", "t_leg"):
        return amount, "", ""
    floor = float(PARAMS.get("min_order_amount", 0.0) or 0.0)
    if floor > 0.0 and amount < floor:
        hard = [v for k, v in caps.items() if k != "per_trade" and v is not None]
        allow = min(hard) if hard else amount
        if allow < floor:
            # 2026-11-19 DIAG (owner asked "why is v114 still placing nothing"):
            # the old label here was "sizing_amount_unavailable", which ALSO covers
            # "every cap unreadable" -- an operator reading the skip histogram
            # concluded "the amount could not be computed" while in fact every cap
            # was readable and the binding one was target_room (live box 13:2x:
            # target_room=138..330 vs floor=10000, 662/670 buys refused per round,
            # because holdings sat at the 50% blind cap and the room left was ~200).
            # Name the real gate; keep fail-closed semantics byte-for-byte identical.
            return None, "hard_cap_below_floor", "min_hard=%.0f<floor=%.0f" % (allow, floor)
        return floor, "", "floor_topup:%.2f" % allow
    binding = sorted([k for k, v in caps.items() if v is not None],
                     key=lambda k: caps[k])[:1]
    note = ""
    try:
        note = "%s=%.0f" % (binding[0], float(caps[binding[0]])) if binding else ""
    except Exception:  # noqa: BLE001 - the note is diagnostics, never a decision
        note = ""
    return amount, "", note


def _rule_tag(signal):
    """The attribution segment for the order remark: `fac_<digits>` or "".

    2026-11-19 owner: the box could only say "factor_composite", not WHICH
    factor (fac_160). The rule id was already on the signal (`rules_factor`
    stamps it), it just never left the process. Only `fac_<digits>` is allowed
    through: the remark is the idempotency key's carrier, so an arbitrary
    string (Chinese, spaces, a vendor id) must never be spliced into it.
    """
    rule_id = str((signal or {}).get("rule_id") or "").strip()
    if not rule_id.startswith("fac_"):
        return ""
    digits = rule_id[4:]
    if not digits or not digits.isdigit():
        return ""
    return rule_id


def _place(ContextInfo, signal):
    """One passorder attempt ladder. Returns (ok, reason).

    The arity ladder is copied from the working bridge, including the two fixes
    it cost: quickOrder must be 2 (a 7-positional call left it at 0 = K-line
    mode, the client skipped every submission and NOTHING reached the counter),
    and TypeError means "this build's signature differs, try the next shape"
    while any other exception means "the request may have partially landed,
    stop and report" -- retrying that would double-send.

    2026-11-13 hard stop: this is the single convergence point of EVERY order,
    so the trading-session gate lives here too (not only in run_round) and
    cannot be bypassed by a new caller.
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
    # Session gate runs AFTER account proof so the long-standing "unproven
    # account" refusal keeps its reason, but BEFORE the only passorder call.
    allowed, why = _trade_session_ok()
    if not allowed:
        return False, why
    remark = build_remark(signal["short_code"], _rule_tag(signal))
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

    2026-11-13 hard stop: cancelling outside the two cash sessions is refused
    just like placing, per owner ruling (both close at 14:52).
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
    # Same session gate, same position: after account proof, before any cancel.
    allowed, why = _trade_session_ok()
    if not allowed:
        return False, why
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


def _holdings_value(positions, price_of):
    """Mark-to-market of the held rows, in the order the fields can be trusted.

    LIVE READING 2026-11-18 13:1x: with prices finally working, the T leg moved
    past `step0 no_realtime_price` and stopped at `step0
    equity_or_holdings_value_unavailable: no quota denominator`. The counter
    equity side is already covered (`_t_equity_basis`), so the denominator that
    collapsed was holdings_value: this broker's POSITION rows carry neither
    `m_dMarketValue` nor `m_dOpenPrice`, and both default to 0.0 in `_positions`
    -- so the sum stayed exactly 0.0 and every T gate closed on arithmetic, not
    on judgement.

    Order of trust (never invents a number, only uses a reading that exists):
      1. the counter's market value (what the position is worth right now);
      2. qty x cost (cost basis, the 2026-11-17 floor);
      3. qty x last price from the price channel (the same tick the sell legs use).
    A row that yields none of the three contributes 0.0, and an all-zero sum
    still fails closed at `t_cap_base` -- a missing price is never a zero.
    """
    total = 0.0
    for symbol, row in (positions or {}).items():
        item = row or {}
        qty = _to_float(item.get("qty"), 0.0) or 0.0
        if qty <= 0.0:
            continue
        mv = _to_float(item.get("market_value"), 0.0) or 0.0
        if mv > 0.0:
            total += mv
            continue
        cost = _to_float(item.get("cost"), 0.0) or 0.0
        if cost > 0.0:
            total += cost * qty
            continue
        last = _to_float(price_of(symbol), 0.0) or 0.0
        if last > 0.0:
            total += last * qty
    return total


def _run_t(ContextInfo, moment, orders, positions, deals, cash, total, day, summary):
    """LIVE-T-20261116: drive the intraday round-trip (zuo T) layer from the counter.

    The T state machine (t_round_state / TRoundBook / t_gate_open ...) is inlined
    into this artifact from rules_t.py by the builder; the template source does
    not name them, so we resolve them defensively and no-op if a build lacks them.

    Data the T gates need arrives BAKED (this carrier has zero external I/O):
      * params.t_anchor_book  -- first-buy anchors, baked by the Go outer brain
                                 from live.db (signed backfill). The counter's
                                 POSITION row has NO anchor columns, so this is
                                 the only provable source for the step0 anchor gate.
      * params.t_macro        -- market_scale / index_change_pct / market_state,
                                 baked from the engine's t_rounds_live.json.
                                 total_asset here is a freshness-gated fallback
                                 for the T cap base when the counter ACCOUNT
                                 table gives no equity (T-EQUITY-FALLBACK).
      * params.t_signal_codes -- T leg remark short codes (owner-loaded).

    Every gate is fail-closed: a missing reading makes the T layer do nothing,
    never invent a value and place an order. Actions go through the SAME _place /
    _cancel convergence points as the strategic legs (one session gate, one
    idempotency source: the counter ORDER table via short code in remark).
    """
    # --- double gate: t_enabled AND owner-loaded short codes must both be on ---
    if not (PARAMS.get("t_enabled") and PARAMS.get("t_signal_codes")):
        return
    # The T state machine symbols are inlined by the builder; the build gate
    # refuses to publish when a module named in params is missing, so they are
    # guaranteed present here. The caller wraps this whole function in try/except.

    anchor_book = PARAMS.get("t_anchor_book") or {}
    macro = PARAMS.get("t_macro") or {}
    # macro freshness: the outer brain bakes asof_age_sec (it can parse ISO;
    # this sandbox cannot call datetime.fromisoformat -- g2 forbids it). A stale
    # number, or a missing age when a staleness window exists, disables T.
    stale_sec = float(PARAMS.get("t_regime_stale_sec", 900) or 0)
    if stale_sec > 0:
        age = macro.get("asof_age_sec")
        if age is None:
            _warn_once("t_macro_age", "baked T macro has no age: fail closed")
            macro = {}
        elif float(age) > stale_sec:
            _warn_once("t_macro_stale", "baked T macro is stale")
            macro = {}  # fail closed: no T on stale market reading

    # positions with a provable baked anchor; counter qty/cost/can_use are truth,
    # anchor_price/anchor_source come ONLY from the outer brain's baked book.
    pos_view = {}
    for symbol, row in (positions or {}).items():
        head = str(symbol or "").split(".")[0]
        bake = anchor_book.get(head) or anchor_book.get(symbol) or {}
        if not bake:
            continue
        entry = dict(row or {})
        # T1-A 2026-11-17: the counter position row keys the sellable field as
        # `can_use` (hardcoded by _positions), but the ported rules_t._sellable_qty
        # only probes can_use_qty/available_qty/sellable_qty/can_use_volume -- so
        # `can_use` is never found, sellable stays None, and the open/balance/close
        # legs all fail closed (zuo T structurally dead on the box). build_context
        # already renames can_use->can_use_qty on the buy side (:1167); the T side
        # missed it. Fix at the assembly point (one place, ported rules untouched);
        # pos_view is exactly the table rules_t._position_of reads first.
        if entry.get("can_use_qty") is None and entry.get("can_use") is not None:
            entry["can_use_qty"] = entry.get("can_use")
        entry["anchor_price"] = bake.get("anchor_price")
        entry["anchor_source"] = bake.get("anchor_source")
        entry["symbol"] = symbol
        pos_view[symbol] = entry
    if not pos_view:
        return  # no provable anchor on any holding -> nothing may open a T leg

    now_ts = _now_ts()
    hhmm = hhmm_now(moment)
    # T1-B 2026-11-17: holdings_value uses the counter's `market_value` (taken from
    # m_dMarketValue in _positions). The old `price or cost` found neither key on the
    # pos_view row -> silently fell back to COST, so the T position cap was computed
    # on cost basis. Prefer real market value; only fall back to qty*cost when the
    # counter gave no market_value for that row.
    # 2026-11-18 13:1x: on THIS broker neither market_value nor cost is present (both
    # default 0.0), so the sum was 0 and the T leg died on its quota denominator.
    # _holdings_value adds the last-price leg as the final fallback, per symbol.
    holdings_value = _holdings_value(
        positions, lambda sym: (_tick_of(ContextInfo, sym) or {}).get("last_price"))

    book = TRoundBook(PARAMS)
    try:
        rebuilt, unknown = book.rebuild_from(orders, deals, day)
    except Exception as exc:  # noqa: BLE001 -- rebuild must not take the round down
        _warn_once("t_rebuild", "T rebuild failed: " + repr(exc)[:100])
        return

    t_actions = {"open": 0, "balance": 0, "cancel": 0, "close": 0, "hold": 0}
    t_skips = {}

    # T-EQUITY-FALLBACK 2026-11-17: counter equity first, else a fresh baked
    # total_asset (see _t_equity_basis). Only feeds the T ctx -- the strategic
    # buy sizing / cash-reserve paths are untouched.
    t_total = _t_equity_basis(total, macro, stale_sec)
    # 2026-11-18: the denominator pair is what decides whether ANY T leg can be
    # sized (`t_cap_base` = min of the two). Printing them once per round turns
    # "step0 equity_or_holdings_value_unavailable" from a dead end into a number.
    _log("t_denom equity=%s holdings=%.0f" % (
        "none" if t_total is None else "%.0f" % t_total, holdings_value))

    def base_ctx(symbol):
        row = pos_view.get(symbol) or {}
        tick = _tick_of(ContextInfo, symbol)
        px = (tick or {}).get("last_price")
        # 2026-11-19 P0 (owner asked why the T leg never opened all day):
        # the T ctx never carried the tick's cumulative amount/volume, so
        # rules_t._t_vwap_of(ctx) had neither `vwap` nor `amount`/`volume` and
        # fell back to `bars` -- which this build does not have (no downloader
        # API) -- hence `step0 vwap_untrustworthy` for EVERY held name, every
        # round, all day (live: 3-4 held names, 100% of rounds).
        # Fix: hand the T layer the same sanity-checked VWAP the strategic leg
        # uses (_vwap_of applies vwap_sanity_* + vwap_unit_candidates), plus the
        # raw fields so a future miss is diagnosable from the log instead of
        # being inferred.
        pre_row = (PARAMS.get("precalc") or {}).get(str(symbol).split(".")[0]) or {}
        turnover_pct = _turnover_pct_of(tick, pre_row)
        if turnover_pct is None:
            _warn_once("t_turnover:%s" % symbol,
                       "t_turnover_unavailable sym=%s float_shares=%s amount=%s prev=%s" % (
                           symbol, (pre_row or {}).get("float_shares"),
                           (tick or {}).get("amount"), (tick or {}).get("prev_close")))
        vwap_value, vwap_why = (None, "no_tick") if not tick else _vwap_of(tick)
        if vwap_value is None:
            _warn_once("t_vwap:%s" % symbol,
                       "t_vwap_untrusted sym=%s why=%s volume=%s amount=%s last=%s" % (
                           symbol, vwap_why or "-", (tick or {}).get("volume"),
                           (tick or {}).get("amount"), px))
        # The T layer's per-symbol context: only readings actually resolved above
        # are copied in (None stays None -> rules_t abstains).
        c = {
            "symbol": symbol, "day": day, "hhmm": hhmm, "now_ts": now_ts,
            "trading_day": True,  # run_round already passed _trade_session_ok
            # Raw price/VWAP/volume fields are carried verbatim so a future miss
            # is diagnosable from the log instead of being inferred from a skip.
            "last_price": px, "position_qty": (row or {}).get("qty"),
            "vwap": vwap_value,
            "turnover_pct": turnover_pct,
            # T-TURNOVER-TIER: same key the cloud ladder uses (prev_close * float_shares).
            "float_mcap_yi": _float_mcap_yi_of(tick, pre_row),
            "volume": (tick or {}).get("volume"),
            "amount": (tick or {}).get("amount"),
            "available_cash": cash, "total_asset": t_total,
            "holdings_value": holdings_value,
            "positions_live": pos_view,
            "market_scale": macro.get("market_scale"),
            "index_change_pct": macro.get("index_change_pct"),
            "market_state": macro.get("market_state"),
            "market_state_age_sec": macro.get("market_state_age_sec"),
            "initial_capital": float(PARAMS.get("initial_capital", 0) or 0),
            "t_has_open_leg": bool(book.open_for(symbol, day)),
        }
        return c

    # (a) in-flight rounds first: reconcile / pair / close (never stack a new leg)
    for rnd in list(book.rounds.values()):
        if str(rnd.get("status")) != T_STATE_OPEN:
            continue
        symbol = rnd.get("symbol") or ""
        if symbol not in pos_view:
            continue  # no provable anchor baked -> hands off
        ctx = base_ctx(symbol)
        ctx["positions_live"] = book.context_for(ctx, symbol)
        try:
            act = t_round_state(PARAMS, ctx, rnd)
        except Exception as exc:  # noqa: BLE001
            _warn_once("t_round_state", "round decision failed: " + repr(exc)[:100])
            continue
        _apply_t_action(ContextInfo, act, rnd, symbol, day, summary, t_actions, t_skips)

    # (b) then new legs on holdings with no round in flight
    for symbol, row in pos_view.items():
        if book.open_for(symbol, day):
            continue
        ctx = base_ctx(symbol)
        try:
            act = t_round_state(PARAMS, ctx, None)
        except Exception as exc:  # noqa: BLE001
            _warn_once("t_round_state_open", "open decision failed: " + repr(exc)[:100])
            continue
        if act.get("action") == "hold":
            t_actions["hold"] += 1
            why = str(act.get("reason") or "hold")
            t_skips[why] = t_skips.get(why, 0) + 1
            continue
        _apply_t_action(ContextInfo, act, None, symbol, day, summary, t_actions, t_skips)

    # one bounded log line per round, ASCII only (GBK console safety)
    _log("t_round actions=%s skips=%s rebuilt=%d unknown=%d" % (
        ",".join("%s=%d" % (k, v) for k, v in sorted(t_actions.items())),
        ",".join("%s=%d" % (k, v) for k, v in sorted(t_skips.items())[:6]),
        len(rebuilt), len(unknown)))


def _apply_t_action(ContextInfo, act, rnd, symbol, day, summary, t_actions, t_skips):
    """Translate one t_round_state decision into the shared place/cancel exit."""
    action = act.get("action")
    if action in ("hold", None):
        t_actions["hold"] += 1
        why = str(act.get("reason") or "hold")
        t_skips[why] = t_skips.get(why, 0) + 1
        return
    if action == "cancel":
        detail = act.get("detail") or {}
        oid = detail.get("order_id") or (detail.get("orders") or [{}])[0].get("order_id")
        if oid:
            ok, why = _cancel(oid)
            if ok:
                t_actions["cancel"] += 1
                summary["cancelled"].append(str(oid))
            else:
                t_skips[why] = t_skips.get(why, 0) + 1
        return
    if action in ("open", "balance", "close"):
        side = act.get("side")
        qty = int(act.get("qty") or 0)
        short = act.get("short_code") or ""
        if not side or qty <= 0 or not short:
            t_skips["incomplete_action"] = t_skips.get("incomplete_action", 0) + 1
            return
        if (day, symbol, short) in _STATE["sent"]:
            t_skips["already_sent"] = t_skips.get("already_sent", 0) + 1
            return
        tick = _tick_of(ContextInfo, symbol)
        px = (tick or {}).get("last_price")
        if px is None or px <= 0:
            t_skips["no_price"] = t_skips.get("no_price", 0) + 1
            return
        sig = {"symbol": symbol, "side": side, "qty": qty,
               "price": float(px), "amount": float(px) * qty, "short_code": short}
        ok, why = _place(ContextInfo, sig)
        if ok:
            t_actions[{"open": "open", "balance": "balance", "close": "close"}[action]] += 1
            _STATE["sent"].add((day, symbol, short))
            summary["placed"].append(short)
        else:
            t_skips[why] = t_skips.get(why, 0) + 1
        return
    t_skips["unknown_action"] = t_skips.get("unknown_action", 0) + 1


def run_round(ContextInfo, moment):
    """One round: rebuild from the counter, decide, then act. Returns a summary.

    2026-11-10 rebuild -- the gate order below IS the plan's section 2 order
    (engine.autoPlace -> signalctl.Admit -> ordbudget -> risk.Gate 12 gates ->
    execution quality -> place). Every missing reading REFUSES THE BUY
    (fail-closed); "abstain = allow" is the defect that filled the account
    with one name at 98.9% and it is reversed here line by line.

    Never raises. A round that cannot see the counter places nothing.
    """
    summary = {"placed": [], "skipped": [], "cancelled": [], "reason": ""}
    _STATE["cash_probe_done"] = False  # CASH-PROBE: allow one new candidate this round
    _STATE["round_moment"] = moment
    # 2026-11-13 P0: refuse the WHOLE round outside the two cash sessions.
    # The downloader leg is intentionally kept alive so bars coverage does
    # not collapse overnight; everything else (counter reads included) stops.
    allowed, why = _trade_session_ok(moment)
    if not allowed:
        _pump_legacy_downloads(ContextInfo)
        summary["reason"] = why
        summary["skipped"].append({"short": "-", "reason": why})
        return summary
    orders, ok = _live_orders()
    if not ok:
        summary["reason"] = "counter_order_table_unreadable"
        _warn_once("order_table", summary["reason"])
        return summary
    # owner ruling (2026-11-17) "re-place allowed after a cancel": drop only the
    # rows that were cancelled WITHOUT filling (cancel_like) from the same-day
    # block set. A code with an OPEN (non-terminal/unknown) row or any FILLED row
    # stays blocked exactly as before -- re-place is opened only for a cancelled,
    # unfilled order, never to re-buy a position that actually traded today.
    counter_codes = set(row["short_code"] for row in orders if not row.get("cancel_like"))
    # MANUAL-CANCEL-FIX (2026-11-19): a cancelled-unfilled order must also stop
    # blocking its symbol and stop being charged to today's budget, no matter
    # who cancelled it. Runs BEFORE the budget view is built.
    _reconcile_session_intents(orders, day_key(moment))
    positions = _positions()
    # P0-A (2026-11-17): whole-market subscribe ran in init; for builds where only
    # per-symbol works, subscribe the held names here (once/day) so the sell legs
    # and T legs actually get last_price. A no-op when whole-market already covers it.
    if positions:
        _subscribe_held(ContextInfo, positions)
        # A-ROUTE 2026-11-18: subscribe 1m for held names so the client bar
        # engine aggregates the 1m bars the minute legs (_minute_bars_payload)
        # read. Separate from the tick subscribe above (which breaks on tick
        # success and never reaches its 1m fallback). Idempotent per day.
        _subscribe_1m_held(ContextInfo, positions)
    # PRICE-PROBE 2026-11-18: read-only receipt for "why did nothing trade".
    # Runs after the position read (held rows first) and before any sizing, so
    # the line lands even on a round that goes on to refuse every signal.
    _probe_price_channels(ContextInfo, positions)
    cash_raw = _cash()
    total = _account_total_asset()
    # FLOW-QUADRANT 2026-11-20 (owner ruling): compute this round's three flow
    # readings ONCE -- relative volume from the index daily bars (in-client),
    # the 30-minute baked money-flow snapshot, and the four-quadrant verdict.
    # Everything downstream (the one hard veto below, the +-20% amount blend in
    # sizing_cap) reads this cache, so a 1000-name round pays one index read and
    # one snapshot decode, not one per symbol. A missing reading abstains; it
    # never becomes a fabricated zero.
    _flow_round_prepare(ContextInfo, positions, total, moment)
    _flow_log_key = "%s_%d" % (day_key(moment), moment.hour)
    if _STATE.get("flow_log_key") != _flow_log_key:
        _STATE["flow_log_key"] = _flow_log_key
        _flow_readout()
    # COUNTER-VIEW 2026-11-18 13:2x: the round stopped printing subscribe/probe
    # lines and the T leg stopped running, which can only mean `positions` read as
    # empty (or unreadable). Without a readout those two states are indistinguishable
    # from the log -- and "no positions" versus "table refused" lead to opposite
    # conclusions. One short line per round names both tables and both money rows.
    _log("views pos=%s ord=%d raw=%s kept=%s cash=%s eq=%s" % (
        "na" if positions is None else len(positions), len(orders),
        _STATE.get("order_raw", "na"), _STATE.get("order_kept", "na"),
        "na" if cash_raw is None else "%.0f" % cash_raw,
        "na" if total is None else "%.0f" % total))
    # The count alone cannot answer "did the 13:0x orders actually fill?" -- a
    # position row with qty>0 for a code we just sold says the sell never reached
    # the counter, and that is a different world from "it filled and the row is
    # gone". Name the rows (bounded to six: this account will never hold more).
    if positions:
        _log("held %s" % ",".join(
            "%sx%d" % (sym, int(_to_float(row.get("qty"), 0.0) or 0.0))
            for sym, row in list(positions.items())[:6]))
    # Pending BUY orders occupy the TARGET budget (via _sizing_view pending_buys)
    # and the concentration buckets -- NOT the cash line.
    # 2026-11-19 correction: this used to subtract pending_amount from cash_raw
    # as well. m_dAvailable is the counter's AVAILABLE cash and the counter has
    # already frozen every working buy (proved on 2026-11-19: eq 99004 with five
    # working buys left cash 21165, i.e. the frozen 77.8k was gone). Subtracting
    # the pending amount a second time would double-count it -- with the ORDER
    # table finally readable that would have refused every buy all afternoon.
    # Plus the owner's remaining-cash floor: keep total_asset x
    # min_cash_after_reserve_pct untouched by auto buys (no daily_buy_cap).
    pending_rows = _pending_buy_rows(orders)
    reserve_floor = 0.0
    if total and total > 0.0:
        reserve_floor = total * float(PARAMS.get("min_cash_after_reserve_pct", 0.0))
    cash = None
    if cash_raw is not None:
        cash = cash_raw - reserve_floor
        if cash < 0.0:
            cash = 0.0
    deals = _deals()
    if positions is None:
        # Fail-closed on BOTH sides now: without the position table the exits
        # have no qty AND the buys have no concentration proof (single-stock /
        # max-positions buckets are unprovable), so neither may fire.
        summary["reason"] = "counter_position_table_unreadable"
        _warn_once("position_table", "POSITION table unreadable: buys and exits decline this round")
    if deals is None:
        # Day-loss fuse input: without fills the realized loss is UNPROVABLE,
        # and an unprovable fuse means no buys this round (never "assume fine").
        _warn_once("deal_table", "DEAL table unreadable: buys decline this round")
    day = day_key(moment)
    day_loss, loss_ok = _day_loss_pct(deals, positions)
    day_loss_limit = None
    if loss_ok and total and total > 0.0:
        day_loss_limit = total * float(PARAMS.get("day_loss_limit_pct", 0.0)) / 100.0
    emergency = bool(PARAMS.get("emergency_stop", False))
    if emergency:
        _warn_once("emergency_stop", "emergency_stop=true: every buy refused, sells unaffected")
    held_symbols = 0
    if positions is not None:
        held_symbols = len([1 for row in positions.values() if (row or {}).get("qty", 0.0) > 0.0])
    # P0-B sell_blind (2026-11-17): a held position with NO last_price makes the
    # stop_loss/take_profit/breakdown legs return None silently -- "no trigger"
    # and "cannot see price" looked identical, so a whole day of blindness was
    # invisible. Name it, hourly-deduped so it never becomes round_skips noise
    # (4 rows / 5s would train the operator to ignore real alerts). This is the
    # leg's own receipt that subscription either worked or did not.
    if positions:
        blind = 0
        for symbol, row in positions.items():
            if not row or float(row.get("qty", 0.0) or 0.0) <= 0.0:
                continue
            tick = _tick_of(ContextInfo, symbol)
            if not tick.get("last_price"):
                blind += 1
        blind_key = "blind_%s" % day
        if blind > 0 and _STATE.get("blind_log_key") != blind_key:
            _STATE["blind_log_key"] = blind_key
            _log("sell_blind held_without_price=%d total_held=%d" % (blind, held_symbols))
    now_ts = _now_ts()
    # DL-FALLBACK: keep draining the legacy downloader queue (batch per round).
    _pump_legacy_downloads(ContextInfo)
    # 2026-11-10 owner ruling: bake the brain-side BULLISH pool (d1 / n-shape
    # / factor scores) as an OPPORTUNITY pool. Merging it into the scan list
    # only widens what the rules may LOOK at -- every entry still passes the
    # full 12-gate chain, so a hot name buys nothing by itself.
    # English: merge the baked hot_pool (score-sorted, capped) into the scan
    # symbols, dedup, bounded by hot_pool_max_codes.
    symbols = list(PARAMS.get("symbols") or [])
    hot = (PARAMS.get("hot_pool") or {}).get("bullish") or {}
    if hot:
        cap = int(PARAMS.get("hot_pool_max_codes", 20))
        extra = [c for c in hot if c not in symbols][:max(0, cap)]
        symbols = symbols + extra
    # A-ROUTE 2026-11-18: one batched price read for the whole scan universe,
    # cached round-scoped so every _tick_of below hits memory (~1s vs ~45s).
    # Fail-closed: if the client ignores the suffixed batch, the snapshot stays
    # empty and each _tick_of falls back to its per-symbol read.
    _batch_ticks(ContextInfo, symbols, positions)
    # DATA-HEALTH: hourly one-line coverage log so a silent download failure is
    # visible in the log pane (2026-11-11 review finding -- never fail silently).
    # READING GUIDE (owner 2026-11-18): one line per hour AND the counters live
    # only in _STATE -- a client/strategy restart clears them, so the FIRST line
    # after a restart showing 0/1000 is an ARTEFACT (bars not re-counted yet),
    # NOT proof that downloads failed. Judge coverage from the steady-state
    # lines after the first hour, never from that first post-restart frame.
    cov_key = "%s_%d" % (day, moment.hour)
    if _STATE.get("cov_log_key") != cov_key:
        _STATE["cov_log_key"] = cov_key
        cov = _STATE.get("bars_cov") or {}
        covered = sum(1 for sym in symbols if cov.get(sym))
        _log("data_health bars_coverage=%d/%d precalc=%d" % (
            covered, len(symbols), len(PARAMS.get("precalc") or {})))
    # BUDGET-GATE: hourly one-line, beside data_health -- shows that factor
    # composites DID pass on a full-position day (owner request 13:40 2026-11-16).
    bg = _STATE.get("budget_gate") or {}
    if bg.get("n") and bg.get("logged_key") != cov_key:
        _STATE["budget_gate"]["logged_key"] = cov_key
        try:
            reserve = float(total or 0.0) * float(
                PARAMS.get("min_cash_after_reserve_pct") or 0.0)
        except (TypeError, ValueError):
            reserve = 0.0
        _log("budget_gate rules_passed_but_no_budget=%d best_score=%.1f code=%s cash=%.0f reserve_floor=%.0f" % (
            int(bg.get("n", 0)), float(bg.get("best") or 0.0),
            str(bg.get("code") or "-"), float(cash_raw or 0.0), reserve))
    for symbol in symbols:
        ctx = build_context(ContextInfo, symbol, moment, orders, positions, cash)
        signals = []
        # STOP-LADDER 2026-11-20 (owner): -3% trims half, -6% clears the whole
        # position. Order is the risk order: take-profit, clear-out (stop_loss),
        # breakdown, then the trim step. The trim is the ONLY step a fresh bull
        # signal may override ("signal strength outranks the trim, never the
        # clear"), and the override is a round-level HOLD: this symbol is
        # neither trimmed nor added to this round -- which also keeps the
        # pre-existing rule that a name down past the first ladder step never
        # gets bought into on the same round.
        bull = rule_pullback_buy(PARAMS, ctx)
        for rule in (rule_take_profit, rule_stop_loss, rule_breakdown_sell):
            hit = rule(PARAMS, ctx)
            if hit:
                signals = [(hit, "live")]
                break
        if not signals:
            hit = rule_reduce_position(PARAMS, ctx)
            if hit:
                if bull:
                    summary["skipped"].append({"short": hit["short_code"],
                                               "reason": "rule_bull_override"})
                    bull = None
                else:
                    signals = [(hit, "live")]
        if not signals and bull:
            signals = [(bull, "live")]
        if not signals:
            # Ported rules only get a turn when nothing live fired: the builtin
            # set IS today's behaviour, and a ported rule must not pre-empt it
            # on the day it is armed.
            for kind in sorted(_PORTED_RULES):
                fn, _src = _PORTED_RULES[kind]
                try:
                    hit = fn(PARAMS, ctx)
                except Exception as exc:  # noqa: BLE001
                    # A rule that raises is not a rule that found nothing: name it.
                    _warn_once("rule_raise:%s" % kind, "rule %s raised %s" % (kind, repr(exc)[:100]))
                    continue
                if hit:
                    # GO-ADMIT-WIRING 2026-11-18 (owner approved plan A): the
                    # runtime whitelist check via _go_admitted (see its
                    # docstring). Only LIVE BUYS are gated: a BUY that cannot
                    # prove its tab-owned id is demoted to "switch_off" --
                    # computed, counted, observable as skipped reason
                    # rule_switch_off, never placed. SELL signals are exempt:
                    # the Go whitelist only gates the BUY dispatch
                    # (scoring_loop.go dispatchLive takes buys only; exits run
                    # the sell-judge/fuse chain), so gating sells here would
                    # blind every live exit leg.
                    _mode = rule_arm_mode(PARAMS, kind)
                    if _mode == "live" and str(hit.get("side") or "") == "buy":
                        if not _go_admitted(kind, rule_id=hit.get("rule_id")):
                            _mode = "switch_off"
                    signals.append((hit, _mode))
        # BUDGET-GATE (2026-11-16 owner): the rules layer marks ctx when a factor
        # composite PASSED but entry_amount was None (full position + reserve
        # floor) -- aggregate per round here; the hourly log beside data_health
        # prints it so a full-position day still shows the engine working.
        nb = ctx.pop("_passed_but_no_budget", 0)
        if nb:
            agg = _STATE.setdefault("budget_gate", {})
            agg["n"] = int(agg.get("n", 0)) + int(nb)
            best = float(ctx.get("_passed_but_no_budget_best") or 0.0)
            if best > float(agg.get("best") or 0.0):
                agg["best"] = best
                agg["code"] = ctx.get("_passed_but_no_budget_code") or ""
        for signal, mode in signals:
            if mode != "live":
                # shadow/off: counted in the round summary, never sent. The
                # >=5-day shadow window against the engine is the price of
                # porting a re-calibrated threshold; spending during it is not.
                summary["skipped"].append({"short": signal["short_code"],
                                           "reason": "rule_" + mode})
                continue
            short = signal["short_code"]
            # NEWS-GATE (owner ruling R10) sell acceleration: force_sell_boost
            # (negative-D1 holdings) SELLS are exempt from the same-day
            # already_sent dedup so a close-out level bearish case can keep
            # working its exit legs (staged sells). Oversell is bounded by the
            # T+1 can_use hard cap, not by dedup. Buys are NOT exempt.
            _boosted_sell = (signal["side"] == "sell"
                             and short in (PARAMS.get("force_sell_boost") or {}))
            if short in counter_codes or (not _boosted_sell and (day, symbol, short) in _STATE["sent"]):
                summary["skipped"].append({"short": short, "reason": "already_sent"})
                continue
            if _boosted_sell:
                print("[local_strategy] force_sell_boost sell %s (negative-D1 holding)" % short)
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
            intent = _intent_of(signal, ctx.get("position_qty"))
            if signal["side"] == "buy":
                if (day, symbol) in _STATE["buy_intents"] and intent != "t_leg":
                    # One buy intent per symbol per day (plan 1.3 item 1): a
                    # second live signal on the same name must not stack.
                    summary["skipped"].append({"short": short, "reason": "same_symbol_day"})
                    continue
                # FLOW-QUADRANT 2026-11-20: the ONLY hard veto this carrier takes
                # from the four-quadrant reading is "do not fish the bottom"
                # (bear market + volume surge + account already at/above its
                # relative target tier). Every other quadrant is expressed in the
                # amount blend, never as a gate. A range/unknown regime never
                # blocks, and the T leg is intentionally exempt (it has its own
                # t_allowed_by_sizing chain and must keep running in a range tape).
                _quadrant = (_STATE.get("round_flow") or {}).get("quadrant") or {}
                if intent in ("new", "add") and _quadrant.get("block"):
                    summary["skipped"].append({"short": short, "reason": "flow_quadrant_block"})
                    continue
            if signal["side"] == "sell":
                # Sell chain (tomorrow's 000520 disposal runs through here): the
                # T+1 sellable qty is a HARD bound. An unreadable can_use refuses
                # the exit (fail-closed); a qty above can_use is clipped down to
                # whole lots rather than sent into a reject.
                can_use = ctx.get("can_use_qty")
                if can_use is None:
                    summary["skipped"].append({"short": short, "reason": "can_use_unknown"})
                    continue
                lot = float(PARAMS.get("lot_size") or 0.0)
                if signal.get("qty", 0.0) > can_use and lot > 0.0:
                    clipped = int(can_use / lot) * int(lot)
                    if clipped <= 0:
                        summary["skipped"].append({"short": short, "reason": "can_use_zero"})
                        continue
                    signal = dict(signal)
                    signal["qty"] = clipped
                    signal["amount"] = round(clipped * (signal.get("price") or 0.0), 2)
                ok, why = _place(ContextInfo, signal)
                if ok:
                    _STATE["sent"].add((day, symbol, short))
                    summary["placed"].append(short)
                else:
                    summary["skipped"].append({"short": short, "reason": why})
                continue
            # ---- BUY side: stage 1 filters, then the 12-gate chain ----
            if emergency:
                summary["skipped"].append({"short": short, "reason": "emergency_stop"})
                continue
            if positions is None:
                summary["skipped"].append({"short": short, "reason": "position_table_unreadable"})
                continue
            if deals is None or not loss_ok or day_loss is None:
                summary["skipped"].append({"short": short, "reason": "day_loss_unprovable"})
                continue
            if day_loss_limit is not None and (-day_loss) >= day_loss_limit:
                summary["skipped"].append({"short": short, "reason": "day_loss_limit"})
                continue
            if not _asset_guard_ok(total, cash, positions):
                summary["skipped"].append({"short": short, "reason": "asset_guard_failed"})
                continue
            risk = _risk_reason(symbol)
            if risk:
                summary["skipped"].append({"short": short, "reason": risk})
                continue
            # NEWS-GATE 2026-11-12 (owner ruling R9, same semantics as the brain
            # news gate): precalc.news_event carries the SIGNED D1 score -- a
            # negative value (explicit bearish) or the news_negative marker
            # refuses the buy (reason=d1_negative). Belt-and-suspenders with the
            # name-based risk_list_dynamic gate: this one is score-based (pregen
            # re-bakes precalc every round) and catches a stock the list missed.
            # Missing precalc = abstain, never a veto (missing data is never
            # invented). The sell chain exits earlier (line ~1335 continue).
            _pc = (PARAMS.get("precalc") or {}).get(symbol) or {}
            _nev = _pc.get("news_event")
            try:
                _nev = float(_nev) if _nev is not None else None
            except (TypeError, ValueError):
                _nev = None
            if (_nev is not None and _nev < 0.0) or _pc.get("news_negative"):
                summary["skipped"].append({"short": short, "reason": "d1_negative"})
                continue
            vwap, vwhy = _vwap_of(ctx)
            if vwap is None:
                # Q6: VWAP missing (or insane vs last) SKIPS the buy -- the same
                # fail-closed contract as the Go side's autoPlaceScaled.
                summary["skipped"].append({"short": short, "reason": vwhy})
                continue
            price = signal.get("price") or ctx.get("last_price")
            # 2026-11-13 owner ruling: buy limit = VWAP price minus 0.7%.
            # min(..., last_price) is mandatory: VWAP is a session average and
            # may sit ABOVE the last price; without the cap the "pullback"
            # order would rest above the market and lift the ask immediately.
            new_price = None
            if str(PARAMS.get("entry_price_mode") or "") == "vwap_pullback":
                new_price = _pullback_limit_price(vwap, price)
            if new_price and new_price < price:
                lot = float(PARAMS.get("lot_size") or 0.0)
                amount = float(signal.get("amount", 0.0))
                if lot > 0.0 and amount > 0.0:
                    qty = int(amount / new_price / lot) * int(lot)
                    if qty > 0:
                        signal = dict(signal)
                        signal["price"] = new_price
                        signal["qty"] = qty
                        signal["amount"] = round(qty * new_price, 2)
                        price = new_price
                        summary.setdefault("priced", []).append(
                            {"short": short, "price": new_price, "qty": qty})
            if price and vwap > 0.0:
                premium_pct = (price - vwap) / vwap * 100.0
                if premium_pct > float(PARAMS.get("max_signal_vwap_premium_pct", 0.0)):
                    summary["skipped"].append({"short": short,
                                               "reason": "vwap_premium_%.1f" % premium_pct})
                    continue
            if intent in ("new", "add"):
                confirmed, cwhy = _buy_confirmed(symbol, now_ts)
                if not confirmed:
                    summary["skipped"].append({"short": short, "reason": cwhy})
                    continue
            if intent == "new" and held_symbols >= int(PARAMS.get("max_positions", 0)):
                summary["skipped"].append({"short": short, "reason": "max_positions"})
                continue
            session_amount = float(_STATE["buy_amounts"].get((day, symbol), 0.0) or 0.0)
            # Pending buys of THIS symbol count toward its single-name room even
            # after a client/strategy restart (session memory is gone, the counter
            # is not) -- owner 2026-11-19 "pending orders occupy the budget".
            buckets = _bucket_view(symbol, positions, {"last_price": ctx.get("last_price")},
                                   session_amount, _pending_amount_for(symbol, pending_rows))
            if not buckets["industry_known"] or not buckets["cluster_set"]:
                # Baked ownership maps are a MUST-BAKE item (plan section 1.2):
                # a symbol with no sector/concept bucket refuses the buy instead
                # of letting the 35% caps abstain.
                summary["skipped"].append({"short": short, "reason": "sector_bucket_unknown"})
                continue
            ctx["bucket_view"] = buckets
            blocked = (not rule_cash_ok(PARAMS, ctx, signal["amount"]) or
                       not rule_amount_cap_ok(PARAMS, signal["amount"]))
            if blocked:
                summary["skipped"].append({"short": short, "reason": "money_cap"})
                continue
            # NOTE (Q4, owner 10:45): the flat daily_buy_cap no longer gates the
            # buy -- the day budget IS the dynamic-position room below; the flat
            # cap stays baked as corroborating evidence only.
            # DYN-SIZE-IN-SANDBOX: the sizing chain is ANDed with the caps above,
            # never a replacement for them: it can only make this order smaller.
            sized_amount, why, note = sizing_cap(ctx, positions, intent, pending_rows)
            # CASH-PROBE 2026-11-18 (read-only, bounded): why every NEW buy is
            # sizing_amount_unavailable while cash/equity readouts look healthy.
            # Prints ONE new candidate per _PROBE_ROUND_EVERY rounds with the
            # exact sizing_cap verdict and the caps it was built from. No state
            # is changed and no decision reads this line -- it only names the
            # cap that refuses the order. Remove once the root cause is closed.
            if intent == "new" and _probe_cash_due():
                _STATE["cash_probe_positions"] = positions
                # 2026-11-19 P0 (live proof): this line used to pass the bare name
                # `params` -- the parameter name of _probe_cash_line, never a module
                # global. Every probe-eligible round therefore raised NameError and
                # handlebar swallowed it as `round error swallowed`, aborting the WHOLE
                # round at the first new-buy candidate: 73 aborts on 2026-11-19, zero
                # orders all day, and the sell legs of the remaining symbols were cut
                # off too. The baked strategy has exactly one parameter source: PARAMS.
                _probe_cash_line(short, sized_amount, why, note, ctx, PARAMS)
            if sized_amount is None:
                summary["skipped"].append({"short": short, "reason": why})
                continue
            if sized_amount < signal.get("amount", 0.0):
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
                # 2026-11-19 owner floor ruling: when the cap was raised to the
                # min-order floor, truncating to whole lots must not drop the
                # order back under it (10000 / 6.81 -> 1400 shares = 9534).
                # One extra lot, only if the ceiling the top-up was checked
                # against still allows it.
                if note.startswith("floor_topup"):
                    ceiling = _to_float(note.split(":", 1)[-1], 0.0)
                    topup = _floor_topup_qty(PARAMS.get("min_order_amount"), price, lot, ceiling)
                    if topup is not None and topup > qty:
                        qty = topup
                signal = dict(signal)
                signal["qty"] = qty
                signal["amount"] = round(qty * price, 2)
                summary.setdefault("sized", []).append(
                    {"short": short, "qty": qty, "amount": signal["amount"], "cap": note})
            # Final min-order-amount gate (owner 11:05): NEW-position auto buys
            # only -- add / reduce / t_leg are exempt, but every gate above and
            # below still binds them. Judged on the FINAL share count, after the
            # cash downgrade and the lot rounding, right before _place.
            # 2026-11-19 owner ruling ("10000 is the FLOOR of opening a position;
            # if it comes out under 10000, top it up to 10000"): this used to be a
            # refusal, which is how a 9902 per-trade band (10% of 99022) plus lot
            # truncation turned into "no new positions ever". The top-up raises the
            # share count to the smallest whole-lot count that reaches the floor,
            # and is bounded by the ceiling sizing_cap already cleared (sized_amount)
            # -- so cash, per-order cap, daily remaining, target room and every
            # concentration bucket still bind. Without a price/lot, or when the
            # ceiling cannot pay the floor, the order is still abandoned whole.
            if intent == "new":
                floor = float(PARAMS.get("min_order_amount", 0.0) or 0.0)
                final_amount = float(signal.get("amount", 0.0))
                if floor > 0.0 and final_amount < floor:
                    lot = float(PARAMS.get("lot_size") or 0.0)
                    ceiling = float(sized_amount or 0.0)
                    if note.startswith("floor_topup"):
                        ceiling = max(ceiling, _to_float(note.split(":", 1)[-1], 0.0))
                    qty = _floor_topup_qty(floor, price, lot, ceiling)
                    if qty is None:
                        summary["skipped"].append({"short": short, "reason": "min_order_amount"})
                        continue
                    signal = dict(signal)
                    signal["qty"] = qty
                    signal["amount"] = round(qty * price, 2)
                    summary.setdefault("sized", []).append(
                        {"short": short, "qty": qty, "amount": signal["amount"],
                         "cap": "floor_topup:%.2f" % ceiling})
            ok, why = _place(ContextInfo, signal)
            if ok:
                _STATE["sent"].add((day, symbol, short))
                if intent != "t_leg":
                    # One buy intent per symbol per day (plan 1.3 item 1): the
                    # session registry, not the seconds-late ORDER table.
                    _STATE["buy_intents"].add((day, symbol))
                    _STATE["buy_amounts"][(day, symbol)] = session_amount + float(signal.get("amount", 0.0))
                # 2026-11-13: reserve this send IMMEDIATELY for the next symbol
                # in the same round (the counter ORDER table is seconds late).
                placed_amount = float(signal.get("amount", 0.0))
                pending_rows.append({"symbol": _bare(symbol),
                                     "qty": float(signal.get("qty") or 0.0),
                                     "remain": float(signal.get("qty") or 0.0),
                                     "price": float(signal.get("price") or 0.0)})
                if cash is not None:
                    cash -= placed_amount
                    if cash < 0.0:
                        cash = 0.0
                summary["placed"].append(short)
            else:
                summary["skipped"].append({"short": short, "reason": why})
    # LIVE-T-20261116: drive the intraday round-trip layer AFTER the strategic
    # buy/sell chain (signal execution outranks T) and BEFORE the stale-cancel
    # sweep. The T layer is double-gated (t_enabled + owner-loaded short codes)
    # and consumes only baked anchor/macro readings; every missing input makes
    # it no-op, never place an order (fail closed). It reuses _place/_cancel,
    # so session gate + counter idempotency are the same as every other leg.
    if PARAMS.get("t_enabled") and PARAMS.get("t_signal_codes"):
        try:
            _run_t(ContextInfo, moment, orders, positions, deals, cash, total, day, summary)
        except Exception as exc:  # noqa: BLE001 -- T must never break the main round
            _warn_once("t_layer", "T layer raised: " + repr(exc)[:120])
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
    # 2026-11-13: cancelling obeys the SAME session whitelist as placing.
    allowed, reason = _trade_session_ok(moment)
    if not allowed:
        return []
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
    day = day_key(moment)
    # CANCEL-WATCH (owner report 2026-11-19 "orders never cancel themselves"):
    # one line per hour naming how many live rows exist, the oldest age and the
    # threshold actually baked. The absence of such a line is why a whole
    # afternoon of un-cancelled orders used to look like "the leg never ran":
    # silence and "nothing was stale" were indistinguishable.
    watch_key = "%s_%d" % (day, moment.hour)
    if _STATE.get("cancel_watch_key") != watch_key:
        _STATE["cancel_watch_key"] = watch_key
        ages = []
        open_rows = 0
        for row in orders:
            if row.get("terminal") is not False:
                continue
            open_rows += 1
            try:
                if row.get("order_time") is not None:
                    ages.append(max(0.0, now - float(row.get("order_time"))))
            except (TypeError, ValueError):
                pass
        _log("cancel_watch open=%d oldest_age=%.0f threshold=%.0f sweep_from=%s" % (
            open_rows, max(ages) if ages else -1.0, stale_sec, str(sweep_from or "-")))
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
            # owner ruling (2026-11-17) "re-place allowed after a cancel": drop
            # this order's same-day "sent" journal entry so the next round's
            # signal for the same code may re-place. The cancel leg only touches
            # non-terminal rows, so a row that actually traded is never cancelled
            # and its journal stays -- this frees only a placed-then-cancelled,
            # unfilled intent.
            _STATE["sent"].discard((day, row.get("symbol"), row.get("short_code")))
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
    # SUBSCRIBE-FIX (2026-11-16, P0): the sell legs (stop_loss/take_profit/
    # breakdown_sell) read ctx.last_price from get_full_tick, and get_full_tick
    # returns an EMPTY dict for symbols that were never subscribed on this
    # client -- live evidence: round_skips no_price=2 EVERY round, and held
    # symbol 002115 sat at -5.46% (stop line -3%) with the sell leg blind until
    # the operator sold by hand at 14:09:46. The init comment always SAID
    # "subscribe the baked universe" but no subscribe call existed. Subscribe
    # whole-market (one call, covers the whole pool regardless of re-bakes)
    # with a per-symbol fallback; log the outcome so a silent empty return
    # cannot hide again.
    # P0-B2 (2026-11-17): init delivery receipt. The subscribe_market line above
    # names the api state, but a single grep of the init block must tell us
    # whether THIS shipped build can subscribe at all -- put it beside the
    # counter_api line so a remote read of the first log lines is conclusive.
    sub_api = _subscribe_market(ContextInfo)
    _per_callable = callable(_subscribe_fns(ContextInfo)[1])
    _log("subscribe_api=%s per_symbol=%s" % (
        sub_api, "present" if _per_callable else "absent"))
    _download_day_bars(ContextInfo)


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
    # 2026-11-11 owner: signals exist but no buys were placed, and refusals were
    # invisible: they only went into summary. Print a bounded ASCII histogram so
    # the QMT client log names the gate that refused (vwap / confirm / caps /
    # min-amount / whitelist). English: observable gate refusals, ASCII only.
    skips = summary.get("skipped") or []
    if skips:
        counts = {}
        for row in skips:
            why = str((row or {}).get("reason") or "unknown")
            counts[why] = counts.get(why, 0) + 1
        parts = sorted(counts.items())[:12]
        _log("round_skips total=%d %s" % (
            len(skips), " ".join("%s=%d" % (k, v) for k, v in parts)))


if __name__ == "__main__":
    # The sandbox never runs this branch; the local test harness imports the
    # module and calls run_round with a fake ContextInfo.
    print("local strategy is loaded by the QMT terminal, not run standalone")
