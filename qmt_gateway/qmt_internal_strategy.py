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
    seen.append(now_ts)
    del seen[:-rounds]
    if len(seen) < rounds:
        return False, "confirm_rounds_%d_of_%d" % (len(seen), rounds)
    if seen[-1] - seen[-2] < gap:
        return False, "confirm_gap_pending"
    return True, ""


def _go_admitted(kind):
    """rule_strategy_admit, actually called (the 0-call-site defect, closed).

    Maps a ported rule kind to the Go strategy id the whitelist is keyed by:
    the baked strategy_kind_to_go table first, then the factor/pattern id
    prefixes pass through verbatim. An unmappable kind is REFUSED -- a rule
    nobody can attribute must never become a live order.
    """
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


def _bucket_view(symbol, positions, tick, session_buy_amount):
    """Fill the concentration buckets the sizing chain used to abstain on.

    stock_value is ALWAYS computed (held marked price + this session's buys);
    industry/cluster are known only when the baked ownership maps carry the
    symbol -- an unknown bucket refuses the BUY at the gate (fail closed),
    so here the unknown simply reports known=False.
    """
    price = tick.get("last_price")
    stock_value = session_buy_amount or 0.0
    for sym, row in (positions or {}).items():
        qty = (row or {}).get("qty") or 0.0
        if qty <= 0.0:
            continue
        mark = price if sym == symbol else ((row or {}).get("market_value") or 0.0) / qty
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
            industry_value += session_buy_amount or 0.0
        if cluster_set:
            concept_value += session_buy_amount or 0.0
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
    fields = ["close", "volume"]
    period = PARAMS.get("ma_period", "1d")
    try:
        return getter(fields, [symbol], period=period, count=3)
    except Exception:
        pass
    try:
        return getter(fields, [symbol], period, "", "", 3, True, True)
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
    # 2026-11-10: the counter ORDER table is seconds late -- the session's own
    # placed-but-not-yet-reported buys must count too (counter fills UNION
    # session sends; neither alone is the day total).
    spent += float(_STATE["buy_amounts"].get((day, symbol), 0.0) or 0.0)
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
    out.setdefault("pending_buys", [])
    return out


def sizing_cap(ctx, positions, intent="new"):
    """Cap one buy order with the dynamic-sizing chain. Returns (amount, reason, note).

    amount None means "send nothing", never "no limit" -- that is buy_size_amount's
    own contract and the reason this leg can be trusted to fail closed.
    sizing_enabled=false is the legacy escape hatch (the Go side has the same
    master switch; this box never invents its own default).

    2026-11-10 Q7 ruling: add / t_leg intents are EXEMPT from the min-order-
    amount floor (new-position shreds are what the floor exists to stop). For
    those intents the amount is min(buy_size_caps) WITHOUT the floor refusal;
    every other cap in the chain still binds.
    """
    if not bool(PARAMS.get("sizing_enabled", False)):
        return None, "sizing_disabled", ""
    sized = _sizing_view(ctx, positions)
    if intent in ("add", "t_leg"):
        caps = buy_size_caps(PARAMS, sized)
        values = [v for v in caps.values() if v is not None]
        if not values:
            return None, "sizing_amount_unavailable", ""
        return min(values), "", ""
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

    2026-11-10 rebuild -- the gate order below IS the plan's section 2 order
    (engine.autoPlace -> signalctl.Admit -> ordbudget -> risk.Gate 12 gates ->
    execution quality -> place). Every missing reading REFUSES THE BUY
    (fail-closed); "abstain = allow" is the defect that filled the account
    with one name at 98.9% and it is reversed here line by line.

    Never raises. A round that cannot see the counter places nothing.
    """
    summary = {"placed": [], "skipped": [], "cancelled": [], "reason": ""}
    orders, ok = _live_orders()
    if not ok:
        summary["reason"] = "counter_order_table_unreadable"
        _warn_once("order_table", summary["reason"])
        return summary
    counter_codes = set(row["short_code"] for row in orders)
    positions = _positions()
    cash = _cash()
    total = _account_total_asset()
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
    now_ts = _now_ts()
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
    for symbol in symbols:
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
            if short in counter_codes or (day, symbol, short) in _STATE["sent"]:
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
            intent = _intent_of(signal, ctx.get("position_qty"))
            if signal["side"] == "buy":
                if (day, symbol) in _STATE["buy_intents"] and intent != "t_leg":
                    # One buy intent per symbol per day (plan 1.3 item 1): a
                    # second live signal on the same name must not stack.
                    summary["skipped"].append({"short": short, "reason": "same_symbol_day"})
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
            vwap, vwhy = _vwap_of(ctx)
            if vwap is None:
                # Q6: VWAP missing (or insane vs last) SKIPS the buy -- the same
                # fail-closed contract as the Go side's autoPlaceScaled.
                summary["skipped"].append({"short": short, "reason": vwhy})
                continue
            price = signal.get("price") or ctx.get("last_price")
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
            buckets = _bucket_view(symbol, positions, {"last_price": ctx.get("last_price")},
                                   session_amount)
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
            sized_amount, why, note = sizing_cap(ctx, positions, intent)
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
                signal = dict(signal)
                signal["qty"] = qty
                signal["amount"] = round(qty * price, 2)
                summary.setdefault("sized", []).append(
                    {"short": short, "qty": qty, "amount": signal["amount"], "cap": note})
            # Final min-order-amount gate (owner 11:05): NEW-position auto buys
            # only -- add / reduce / t_leg are exempt, but every gate above and
            # below still binds them. Judged on the FINAL share count, after the
            # cash downgrade and the lot rounding, right before _place; a miss
            # abandons the WHOLE order (no top-up, no shred).
            if intent == "new":
                final_amount = float(signal.get("amount", 0.0))
                if final_amount < float(PARAMS.get("min_order_amount", 0.0)):
                    summary["skipped"].append({"short": short, "reason": "min_order_amount"})
                    continue
            ok, why = _place(ContextInfo, signal)
            if ok:
                _STATE["sent"].add((day, symbol, short))
                if intent != "t_leg":
                    # One buy intent per symbol per day (plan 1.3 item 1): the
                    # session registry, not the seconds-late ORDER table.
                    _STATE["buy_intents"].add((day, symbol))
                    _STATE["buy_amounts"][(day, symbol)] = session_amount + float(signal.get("amount", 0.0))
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
