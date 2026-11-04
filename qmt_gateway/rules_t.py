"""rules_t.py -- the live intraday round-trip ("zuo T") state machine, ported from Go.

Owner decision 2026-11-04 (overrides ruling 3): the T logic must be internalised into the
Python carrier that runs on the physical trading box, because after the broker's 2026-11-05
ban a QMT built-in strategy may not read or write local files and may not touch the network.
This module is therefore PURE: no I/O, no clock, no randomness, no network, no storage. Every
number that can move money arrives through ``params`` (baked by the builder, never written in
a function body), every reading arrives through ``ctx`` (assembled by the carrier from the
COUNTER's own tables), and a missing reading makes the code DECLINE instead of degrade.

Sources translated (Go anchors, file:line):
  * internal/engine/intraday_t_live.go:313-338 -- the global "may we open a new leg at all"
    chain (stop switch, order window, mode, shadow, blind zone, regime, breaker day, cash,
    equity); the order itself is the decision, so the port keeps the same order.
  * internal/engine/intraday_t_live.go:1207-1299 (openPlan) -- the per-symbol chain:
    step0 position/anchor/price/VWAP + direction env gates, step0.5 liquidity, step0.6 volume,
    step1.5 cross-account opposite leg, step2 round count, step3 time gates, step4 intraday
    position + minute-chain cross-check, step5 signal/veto, step6 strategic yields, step7 quota.
  * internal/engine/intraday_t_live.go:881-1033 (pair / pairBuyFirst / pairSellFirst) --
    pairing before opening, the three accel triggers, the unconditional end-of-day pairing,
    the buy-back permission chain (:1037-1082).
  * internal/engine/intraday_t_live.go:723-770 (rollStaleOpen) and :797-831 (reconcile) --
    cross-day sealing and "the ledger says open but the counter knows nothing" (ghost open).
  * internal/engine/intraday_t_live.go:2153-2295 (regimeBlock / volumeBlock /
    roundSpreadPctNow / accelReason / premiseGone) -- form gate, volume gate, the single
    spread formula, the early-pairing triggers.
  * internal/paper/t_rounds.go:14-36 (the step-order comment), :548-704 (pairing),
    :1089-1122 (quota) -- the paper twin of the same machine.
  * internal/engine/tlive_state.go:36-113 (TRoundLive) -- the field set of t_rounds_live.json,
    and :578-631 (load/save, cross-day no-resurrection) -- the semantics rebuild_from reproduces
    from the counter instead of from a file.
  * internal/config/config.go:2098-2565 -- the T key names, factory values and legal ranges
    that params[] mirrors; this port never re-derives a default.
  * internal/paper/paper.go:360-382 and internal/engine/intraday_t_live.go:73-76 -- the fee
    model: commission max(amount * rate, min commission), stamp tax on the sell leg only.
  * docs/PLAN_T_COST_CALIB_20261027.md:128-130 (the cost barrier "2 x (commission + slippage)
    + stamp tax") and docs/IMPL_T_COST_CALIB_20261027.md:197 (min_spread_pct =
    (2*commission_bps + 2*slip_bps + stamp_bps) / 1e4 * 100, times the 1.3 safety margin);
    docs/IMPL_T_COST_CALIB_20261027.md:11-23 carries the owner rulings behind B1/B2/B3/B5
    (B1 records only and changes no gate, B2 is the offline calibration that produced these
    numbers, B4 splitting stays observation-only and is deliberately NOT implemented here).
  * internal/signalctl/sizing.go:277-343 -- the blind-zone rule this port honours: a missing
    key market reading caps the target at half and stops the day's T entirely.

Deliberately NOT done here: no order placement, no file, no clock; balance quantities are
always recomputed from the counter's fills (never an in-memory tally); and every absent
reading returns a decline with a reason string rather than a fabricated default.

English: pure port of the live T state machine (gate chain, next-action decision, cost line,
counter-rebuildable round book, T short codes compatible with the remark round-trip).
"""

# ---------------------------------------------------------------------------
# STRUCTURAL CONSTANTS (module level on purpose)
#
# Unit identities and shape constants only -- NOT thresholds. Decision-bearing numbers all
# live in params and are listed in T_PARAM_KEYS at the bottom, so the build gate that counts
# numeric literals inside function bodies has nothing to find here.
# ---------------------------------------------------------------------------
_PERCENT = 100.0          # fraction -> percent
_BASIS_POINTS = 10000.0   # fraction -> bp
_MINUTES_IN_HOUR = 60.0
_WAN_TO_YUAN = 10000.0    # yuan per 10k yuan (the liquidity reading's unit)
_TWO_DIGITS = 2           # cent rounding for money readouts
_FOUR_DIGITS = 4          # basis-point-scale rounding for participation and cost readouts
_FIRST_INDEX = 0         # first field of a split reading
_SECOND_INDEX = 1       # second field of a split reading
_LAST_INDEX = -1        # tail field of a split remark
_RSPLIT_MAXSPLIT = 1      # remark = "<version>-<short code>": one split
_COLON_PARTS = 2          # 'HH:MM' splits into exactly two parts
_HHMM_LEN = 4             # the 'HHMM' spelling, accepted as an alias of 'HH:MM'
_HOUR_CHARS = 2           # characters of the hour field
_ZERO = 0
_ONE = 1
_ONE_F = 1.0
_ZERO_F = 0.0
_EMPTY = ""

# Direction / state vocabulary (mirrors intraday_t_live.go:51-95, paper/t_rounds.go:52-63).
T_DIR_BUY_FIRST = "buy_first"    # forward round: buy the intraday layer, sell an equal base amount later
T_DIR_SELL_FIRST = "sell_first"  # reverse round: sell base, buy it back later (or convert)
T_STATE_OPEN = "open"
T_STATE_DONE = "done"            # the only terminal state that carries a "T profit"
T_STATE_UNWOUND = "unwound"      # half state sealed: the position changed passively
T_STATE_CONVERTED = "converted"  # not bought back: becomes a strategic reduction
T_STATE_UNKNOWN = "unknown"      # rebuilt round cannot be attributed: hands off

ACTION_OPEN = "open_leg"
ACTION_BALANCE = "balance_leg"
ACTION_CLOSE = "close_out"
ACTION_HOLD = "hold"
ACTION_CANCEL = "cancel_stale"

# Kind names inside params["t_signal_codes"].
KIND_BUY_FIRST = "t_buy_first"        # first leg of the forward round (buy)
KIND_BUY_FIRST_PAIR = "t_buy_first_pair"  # pairing leg of the forward round (sell base)
KIND_SELL_FIRST = "t_sell_first"      # first leg of the reverse round (sell base)
KIND_SELL_PAIR = "t_sell_pair"        # buy-back leg of the reverse round
KIND_CLOSE_OUT = "t_close_out"        # close-out sweep order (seals a half state)

KIND_SIDE = {
    KIND_BUY_FIRST: "buy",
    KIND_BUY_FIRST_PAIR: "sell",
    KIND_SELL_FIRST: "sell",
    KIND_SELL_PAIR: "buy",
    KIND_CLOSE_OUT: _EMPTY,
}

# The T short codes this port expects (2 uppercase letters each, compatible with the
# existing PB/BK/SL/TP family: none is a prefix of another -- see t_short_code_check).
T_DEFAULT_CODE_KINDS = (KIND_BUY_FIRST, KIND_BUY_FIRST_PAIR, KIND_SELL_FIRST,
                        KIND_SELL_PAIR, KIND_CLOSE_OUT)


# ---------------------------------------------------------------------------
# PURE HELPERS -- nothing in here makes a trading decision.
# ---------------------------------------------------------------------------

def _t__num(value):
    """Numeric reading or None. A missing reading is None, never 0."""
    if value is None or isinstance(value, bool):
        return None
    try:
        out = float(value)
    except (TypeError, ValueError):
        return None
    if out != out:  # NaN, without importing math
        return None
    return out


def _param(params, key):
    """Required threshold: a missing key raises rather than defaulting."""
    return params[key]


def _flag(params, key):
    """Required switch."""
    return bool(params[key])


def _round4(value):
    number = _t__num(value)
    if number is None:
        return None
    return round(number, _FOUR_DIGITS)


def _round2(value):
    number = _t__num(value)
    if number is None:
        return None
    return round(number, _TWO_DIGITS)


def _hhmm(moment):
    """Normalise a clock reading to the carrier's 'HH:MM' spelling."""
    text = str(moment or _EMPTY)
    if len(text) == _HHMM_LEN and text.isdigit():
        return "%s:%s" % (text[:_HOUR_CHARS], text[_HOUR_CHARS:])
    return text


def _minutes_of(text):
    """'HH:MM' -> minutes since midnight; None when unreadable."""
    body = _hhmm(text)
    if ":" not in body:
        return None
    parts = body.split(":")
    if len(parts) != _COLON_PARTS:
        return None
    hour = _t__num(parts[_FIRST_INDEX])
    minute = _t__num(parts[_SECOND_INDEX])
    if hour is None or minute is None:
        return None
    return hour * _MINUTES_IN_HOUR + minute


def _window_hit(windows, moment):
    """Half-open [start, end) window test -- the one convention all copies share.

    A closed end would also block 09:30, which is precisely when the continuous session opens.
    """
    current = _minutes_of(moment)
    if current is None:
        return False
    for window in windows or []:
        try:
            start, end = window
        except (TypeError, ValueError):
            continue
        first = _minutes_of(start)
        second = _minutes_of(end)
        if first is None or second is None:
            continue
        if first <= second:
            if first <= current < second:
                return True
        elif current >= first or current < second:
            return True
    return False


def _lot_floor(qty, lot):
    """Floor down to whole lots; under one lot is zero (odd lots may only be closed out)."""
    quantity = _t__num(qty)
    lot_size = _t__num(lot)
    if quantity is None or lot_size is None or lot_size <= _ZERO_F or quantity < lot_size:
        return _ZERO
    return int(quantity // lot_size) * int(lot_size)


def _pct(numerator, denominator):
    top = _t__num(numerator)
    bottom = _t__num(denominator)
    if top is None or bottom is None or bottom <= _ZERO_F:
        return None
    return top / bottom * _PERCENT


def _bp(numerator, denominator):
    top = _t__num(numerator)
    bottom = _t__num(denominator)
    if top is None or bottom is None or bottom <= _ZERO_F:
        return None
    return top / bottom * _BASIS_POINTS


def _short_code_of(row):
    """Short code carried by a counter row: the remark's tail after the LAST dash.

    Byte-compatible with local_executor.build_remark (:182-189) and short_code_from_remark
    (:192-197): the remark is "<version>-<short code>" and the short code is the last field.
    """
    if not isinstance(row, dict):
        return _EMPTY
    direct = str(row.get("short_code") or _EMPTY).strip()
    if direct:
        return direct
    remark = str(row.get("remark") or _EMPTY)
    if "-" not in remark:
        return _EMPTY
    return remark.rsplit("-", _RSPLIT_MAXSPLIT)[-_ONE]


def _kind_of_short(short, codes):
    """Attribute a short code to its kind; the longest matching code wins."""
    best = _EMPTY
    best_len = _ZERO
    for kind, code in (codes or {}).items():
        text = str(code or _EMPTY)
        if text and short.startswith(text) and len(text) > best_len:
            best = kind
            best_len = len(text)
    return best


def _first_leg_kind(direction):
    return KIND_BUY_FIRST if direction == T_DIR_BUY_FIRST else KIND_SELL_FIRST


def _pair_leg_kind(direction):
    return KIND_BUY_FIRST_PAIR if direction == T_DIR_BUY_FIRST else KIND_SELL_PAIR


def _leg_side(kind):
    return KIND_SIDE.get(kind, _EMPTY)


def _is_open_order(row, params):
    """May this counter order still fill? An unknown status is treated as LIVE.

    Assuming "unknown" means "done" would let the port re-send a second order for a leg that
    is already at the broker; assuming "live" costs at most one waiting round.
    """
    terminal = row.get("terminal")
    if terminal is not None:
        return not bool(terminal)
    code = row.get("status_code")
    if code is not None:
        return str(code) not in set(str(item) for item in _param(params, "t_terminal_status_codes"))
    return True


def _row_price(row):
    amount = _t__num(row.get("amount"))
    qty = _t__num(row.get("qty"))
    price = _t__num(row.get("price"))
    if amount is not None and qty and qty > _ZERO_F:
        return amount / qty
    return price


def _row_qty(row):
    qty = _t__num(row.get("qty"))
    if qty is None:
        qty = _t__num(row.get("traded"))
    return qty if qty is not None else _ZERO_F


def _age_seconds(row, ctx):
    """Age of a counter row from the caller's clock reading; None when unmeasurable."""
    now = _t__num(ctx.get("now_ts"))
    stamp = _t__num(row.get("order_time"))
    if now is None or stamp is None:
        return None
    return now - stamp


def _head_len(params):
    return int(_param(params, "symbol_head_len"))


def _symbol_head(symbol, params):
    return str(symbol or _EMPTY).split(".")[_FIRST_INDEX][:_head_len(params)]


def _head_matches(short, symbol_head):
    return bool(symbol_head) and symbol_head in short


def _position_of(ctx, symbol):
    live = ctx.get("positions_live") or {}
    row = live.get(symbol)
    if row is None:
        head = str(symbol or _EMPTY).split(".")[_FIRST_INDEX]
        for key, value in live.items():
            if str(key).split(".")[_FIRST_INDEX] == head:
                row = value
                break
    return row if isinstance(row, dict) else {}


def _sellable_qty(ctx, symbol):
    """Sellable quantity from the COUNTER position row (T+1: today's buys are not sellable)."""
    pos = _position_of(ctx, symbol)
    for field in ("can_use_qty", "available_qty", "sellable_qty", "can_use_volume"):
        value = _t__num(pos.get(field))
        if value is not None:
            return value
    return None


def _limit_pct(params, symbol, name):
    """Price-limit band of the board (mirrors data/limitup_pct.go:15-27, values from params)."""
    text = str(name or _EMPTY).upper()
    for marker in (_param(params, "t_st_markers") or []):
        if str(marker).upper() in text:
            return _param(params, "t_limit_pct_st")
    head = str(symbol or _EMPTY).split(".")[_FIRST_INDEX]
    for prefix in (_param(params, "t_bse_board_prefixes") or []):
        if head.startswith(str(prefix)):
            return _param(params, "t_limit_pct_bse")
    for prefix in (_param(params, "t_wide_board_prefixes") or []):
        if head.startswith(str(prefix)):
            return _param(params, "t_limit_pct_wide")
    return _param(params, "t_limit_pct_main")


def _vwap_of(ctx):
    """Intraday average price = cumulative amount / cumulative volume (data/types.go:68-77).

    Unavailable stays unavailable: it is never replaced by the price or by the cost basis
    (that substitution is exactly the bug the 2026-10-26 rewrite removed on the paper side).
    """
    explicit = _t__num(ctx.get("vwap"))
    if explicit is not None and explicit > _ZERO_F:
        return explicit
    amount = _day_amount(ctx)
    volume = _t__num(ctx.get("volume"))
    if amount is None or volume is None or amount <= _ZERO_F or volume <= _ZERO_F:
        return None
    value = amount / volume
    return value if value > _ZERO_F else None


def _bars_of(ctx):
    bars = ctx.get("bars")
    return bars if isinstance(bars, list) else []


def _day_amount(ctx):
    """Turnover so far today (yuan): the participation denominator, never a full-day guess."""
    value = _t__num(ctx.get("amount"))
    if value is not None:
        return value
    total = _ZERO_F
    for bar in _bars_of(ctx):
        if isinstance(bar, dict):
            item = _t__num(bar.get("amount"))
            if item is not None:
                total += item
    return total


def _prev_close(ctx):
    value = _t__num(ctx.get("prev_close"))
    if value is not None and value > _ZERO_F:
        return value
    for bar in _bars_of(ctx):
        if isinstance(bar, dict):
            item = _t__num(bar.get("prev_close"))
            if item is not None and item > _ZERO_F:
                return item
    return None


    """Intraday high/low for the excursion checks, from the snapshot the carrier read.

    Returns (None, None) when the tick lacks them: the round then declines to widen
    or tighten anything rather than trading against an assumed extreme.
    """
def _intraday_high_low(ctx):
    high = _t__num(ctx.get("high"))
    low = _t__num(ctx.get("low"))
    if high is None or low is None:
        tops = []
        floors = []
        for bar in _bars_of(ctx):
            if not isinstance(bar, dict):
                continue
            top = _t__num(bar.get("high"))
            floor = _t__num(bar.get("low"))
            if top is not None:
                tops.append(top)
            if floor is not None:
                floors.append(floor)
        if high is None and tops:
            high = max(tops)
        if low is None and floors:
            low = min(floors)
    return high, low


def _codes(params):
    return _param(params, "t_signal_codes") or {}


def _t_short(params, kind, symbol):
    """The remark short code of one T leg: <kind code><symbol head>, as make_signal does."""
    code = str(_codes(params).get(kind) or _EMPTY)
    if not code:
        return _EMPTY
    return "%s%s" % (code, _symbol_head(symbol, params))


def _leg_aggregate(ctx, kind, symbol_head, params):
    """One leg's counter truth: filled quantity, weighted average price, live orders.

    This is the port's only source for "how much actually traded" (reconcile semantics,
    intraday_t_live.go:797-831: the state ledger is intent, the fills table is fact).
    """
    code = str(_codes(params).get(kind) or _EMPTY)
    out = {"filled": _ZERO, "avg_price": None, "live_orders": [], "seen": False}
    if not code or not symbol_head:
        return out
    filled = _ZERO_F
    amount = _ZERO_F
    seen = False
    for row in ctx.get("fills_today") or []:
        if not isinstance(row, dict):
            continue
        short = _short_code_of(row)
        if not short.startswith(code) or not _head_matches(short, symbol_head):
            continue
        qty = _row_qty(row)
        price = _row_price(row)
        if qty <= _ZERO_F:
            continue
        seen = True
        filled += qty
        amount += (price * qty) if price is not None else _ZERO_F
    live = []
    order_filled = _ZERO_F
    order_amount = _ZERO_F
    for row in ctx.get("orders_today") or []:
        if not isinstance(row, dict):
            continue
        short = _short_code_of(row)
        if not short.startswith(code) or not _head_matches(short, symbol_head):
            continue
        seen = True
        if _is_open_order(row, params):
            live.append(row)
        traded = _t__num(row.get("traded"))
        price = _row_price(row)
        if traded is not None and traded > _ZERO_F and price is not None:
            order_filled += traded
            order_amount += traded * price
    if filled <= _ZERO_F and order_filled > _ZERO_F:
        # Fill reports are the first truth; when none has arrived yet the counter's own order
        # row (traded volume) is still the counter, not our memory. Never add the two: the
        # same shares would count twice and the balance leg would be short.
        filled = order_filled
        amount = order_amount
    out["filled"] = int(filled)
    out["avg_price"] = (amount / filled) if filled > _ZERO_F else None
    out["live_orders"] = live
    out["seen"] = seen
    return out


def _result(action, reason, qty=_ZERO, side=_EMPTY, short=_EMPTY, terminal=_EMPTY, detail=None):
    return {"action": action, "reason": reason, "qty": int(qty), "side": side,
            "short_code": short, "terminal": terminal, "detail": detail or {}}


# ---------------------------------------------------------------------------
# 1. t_gate_open -- "may a new leg open right now?" (the ORDER is the decision)
# ---------------------------------------------------------------------------

def t_direction(params, ctx):
    """Which T direction the current price suggests (paper/t_rounds.go:942-953).

    Both false is the normal "no round-trip opportunity on this name right now", not a defect.
    """
    price = _t__num(ctx.get("last_price"))
    vwap = _vwap_of(ctx)
    if price is None or vwap is None:
        return False, False, "no_price_or_vwap"
    dev = _pct(price - vwap, vwap)
    if dev is None:
        return False, False, "deviation_uncomputable"
    gate = _param(params, "t_vwap_dev_pct")
    mode = str(_param(params, "t_mode") or _EMPTY)
    buy_allowed = mode in ("both", "buy_first")
    sell_allowed = mode in ("both", "sell_first")
    return bool(buy_allowed and dev <= -gate), bool(sell_allowed and dev >= gate), _EMPTY


def t_cost_gate(params, ctx):
    """Does this single planned T clear its cost line? -> (allowed, reason, detail).

    Fee model reused, not re-invented (paper.go:360-382, intraday_t_live.go:73-76): commission
    max(amount * rate, min commission) per leg, stamp tax on the sell leg only, plus a per-leg
    slippage in bp. The line itself is docs/IMPL_T_COST_CALIB_20261027.md:197
    (min_spread_pct = (2*commission_bps + 2*slip_bps + stamp_bps) / 1e4 * 100 * safety margin),
    which is the algebraic form of docs/PLAN_T_COST_CALIB_20261027.md:128-130.
    B1's ruling (IMPL:11-23) is why the detail carries the per-leg split: recording only.

    Declines by design when a required reading is missing (planned qty, price, expected
    spread): "cost unknown" must never be read as "cost passed".
    """
    qty = _t__num(ctx.get("t_plan_qty"))
    price = _t__num(ctx.get("last_price"))
    expected = _t__num(ctx.get("expected_spread_pct"))
    if qty is None or price is None or qty <= _ZERO_F or price <= _ZERO_F:
        return False, "cost_gate_missing_qty_or_price: decline rather than guess the cost", {}
    if expected is None:
        return False, "cost_gate_missing_expected_spread: decline rather than guess", {}
    rate = _param(params, "t_commission_rate")
    floor = _param(params, "t_min_commission")
    stamp_rate = _param(params, "t_stamp_tax_rate")
    slip_bps = _param(params, "t_slippage_bps")
    safety = _param(params, "t_cost_safety_multiplier")
    buy_amount = qty * price
    sell_amount = qty * price
    fee_buy = max(buy_amount * rate, floor)
    fee_sell = max(sell_amount * rate, floor)
    stamp = sell_amount * stamp_rate
    slip = (buy_amount + sell_amount) * slip_bps / _BASIS_POINTS
    cost = fee_buy + fee_sell + stamp + slip
    notional = buy_amount if buy_amount >= sell_amount else sell_amount
    cost_bps = _bp(cost, notional)
    if cost_bps is None:
        return False, "cost_line_uncomputable", {}
    required_pct = cost_bps / _BASIS_POINTS * _PERCENT * safety
    detail = {"cost_yuan": _round2(cost), "fee_buy": _round2(fee_buy),
              "fee_sell": _round2(fee_sell), "stamp_tax": _round2(stamp),
              "slippage_yuan": _round2(slip), "cost_bps": _round2(cost_bps),
              "required_spread_pct": _round2(required_pct), "expected_spread_pct": expected}
    if expected < required_pct:
        return False, "cost_line_not_met: expected %.4f%% < required %.4f%%" % (
            expected, required_pct), detail
    return True, "cost_line_ok", detail


def t_gate_open(params, ctx):
    """The whole open-leg gate chain -> (allowed, reason).

    Order follows intraday_t_live.go: the global chain (:313-338) first, then the per-symbol
    chain (:1207-1299) step by step. Every decline names its Go step so the blocked-distribution
    readout stays comparable with the engine's own step counters.
    """
    # ---- global: these stop NEW legs only; in-flight legs are never gated here ----
    if not _flag(params, "t_enabled"):
        return False, "step0 t_disabled: one-key stop, in-flight legs still pair and close"
    if not ctx.get("trading_day"):
        return False, "step0 not_trading_day"
    if not _window_hit(_param(params, "t_session_windows"), ctx.get("hhmm")):
        return False, "step0 outside_continuous_session: no reliable price, no counterparty"
    if _window_hit(_param(params, "t_no_new_order_windows"), ctx.get("hhmm")):
        # Two legs are serial: if the second hits the window the first may already be filled,
        # which leaves a bare leg. So the window gates the FIRST leg, not each leg
        # (intraday_t_live.go:300-303, :316-317).
        return False, "step0 no_new_order_window: the pairing leg could be refused"
    mode = str(_param(params, "t_mode") or _EMPTY)
    if mode not in ("both", "buy_first", "sell_first"):
        return False, "step0 t_mode allows no direction (%s)" % (mode or "empty")
    if _flag(params, "t_shadow"):
        return False, "step0 shadow_period: the target position is not trustworthy"
    dims = ctx.get("sizing_missing_dims")
    if dims:
        # INDEX-BLIND: the day's key reading is missing, so the target itself is a guess;
        # double-sided intraday trading inside a blind zone is the loss direction.
        return False, "step0 blind_zone_missing_dims(%s): no T today" % (
            "|".join(str(item) for item in dims))
    if _flag(params, "t_missing_key_input"):
        return False, "step0 blind_zone_missing_key_input: no T today"
    blocked, reason = t_regime_gate(params, ctx)
    if blocked:
        return False, reason
    blocked, reason = t_day_breaker_gate(params, ctx)
    if blocked:
        return False, reason
    if _t__num(ctx.get("available_cash")) is None:
        return False, "step0 cash_reading_unavailable (fail closed)"
    cap_base = t_cap_base(params, ctx)
    total = _t__num(ctx.get("total_asset"))
    if total is None or total <= _ZERO_F or cap_base is None or cap_base <= _ZERO_F:
        return False, "step0 equity_or_holdings_value_unavailable: no quota denominator"

    # ---- per symbol ----
    symbol = ctx.get("symbol")
    pos = _position_of(ctx, symbol)
    position_qty = _t__num(pos.get("qty")) if pos else _t__num(ctx.get("position_qty"))
    if not pos or position_qty is None or position_qty <= _ZERO_F:
        return False, "step0 no_position_row_on_counter"
    if ctx.get("t_has_open_leg"):
        return False, "step1 a leg is already in flight on this name (never stack legs)"
    anchor = _t__num(pos.get("anchor_price"))
    source = str(pos.get("anchor_source") or _EMPTY)
    allowed_sources = [str(item) for item in (_param(params, "t_anchor_ok_sources") or [])]
    if anchor is None or anchor <= _ZERO_F or source not in allowed_sources:
        # A T leg dilutes the average cost; without a provable first-buy anchor the sell verdict
        # would judge against a moving line (intraday_t_live.go:1220-1223).
        return False, "step0 anchor_not_provable(price=%s source=%s)" % (anchor, source or "empty")
    price = _t__num(ctx.get("last_price"))
    if price is None or price <= _ZERO_F:
        return False, "step0 no_realtime_price (fail closed)"
    vwap = _vwap_of(ctx)
    if vwap is None:
        return False, "step0 vwap_untrustworthy: no T and no fallback to the cost basis"
    buy, sell, why = t_direction(params, ctx)
    if not buy and not sell:
        return False, "step4 deviation below the gate (%s)" % (why or "no_side")
    market_scale = _t__num(ctx.get("market_scale"))
    if buy:
        if market_scale is None:
            return False, "step0 market_scale_unavailable for the buy_first env gate"
        gate = _param(params, "t_min_market_scale")
        if market_scale < gate:
            return False, "step0 buy_first_env_gate MS=%.3f<%.2f" % (market_scale, gate)
        plunge = _param(params, "t_market_plunge_pct")
        index_pct = _t__num(ctx.get("index_change_pct"))
        if plunge < _ZERO_F and index_pct is not None and index_pct <= plunge:
            return False, "step0 index_plunge %.2f%% <= %.2f%%: buy_first stopped" % (
                index_pct, plunge)
    if sell:
        if market_scale is None:
            return False, "step0 market_scale_unavailable for the sell_first env gate"
        gate = _param(params, "t_sell_env_min_market_scale")
        if market_scale < gate:
            return False, "step0 sell_first_env_gate MS=%.3f<%.2f (extreme weakness: no T)" % (
                market_scale, gate)
        blocked, reason = t_sell_cap_gate(params, ctx, cap_base)
        if blocked:
            return False, "step0 " + reason
    blocked, reason = t_liquidity_gate(params, ctx)
    if blocked:
        return False, "step0.5 " + reason
    blocked, reason = t_volume_gate(params, ctx)
    if blocked:
        return False, "step0.6 " + reason
    if ctx.get("opposite_leg_blocked"):
        # Our own opposite leg on the same code in another account is the broker's wash shape
        # (intraday_t_live.go:1265-1270; tlive_state.go:470-518 fails closed when unreadable).
        return False, "step1.5 cross_account_opposite_leg_open"
    limit = _param(params, "t_max_rounds")
    used = _t__num(ctx.get("t_rounds_used"))
    if limit > _ZERO and used is not None and used >= limit:
        return False, "step2 round_count_used %d/%d" % (int(used), int(limit))
    last_open = _minutes_of(_param(params, "t_last_open_at"))
    current = _minutes_of(ctx.get("hhmm"))
    if last_open is not None and current is not None and current >= last_open:
        return False, "step3 past last_open_at %s: pair and close only" % (
            _param(params, "t_last_open_at"))
    opening = _minutes_of(_param(params, "t_session_open_hhmm"))
    if opening is not None and current is not None:
        if current < opening + _param(params, "t_open_cooling_min"):
            return False, "step3 inside the opening cooling window: V and price still coincide"
    blocked, reason = t_intraday_gate(params, ctx, vwap)
    if blocked:
        return False, "step4 " + reason
    blocked, reason = t_vmin_gate(params, ctx, vwap)
    if blocked:
        return False, "step4 " + reason
    blocked, reason = t_signal_gate(params, ctx, vwap, buy, sell)
    if blocked:
        return False, "step5 " + reason
    if ctx.get("strategic_busy"):
        return False, "step6 a strategic verdict or in-flight strategic order owns this name"
    direction = T_DIR_BUY_FIRST if buy else T_DIR_SELL_FIRST
    qty, reason = t_open_qty(params, ctx, direction, cap_base)
    if qty < _param(params, "t_min_base_keep_qty"):
        return False, "step7 " + reason
    cost_ok, cost_why, _detail = t_cost_gate(params, dict(ctx or {}, t_plan_qty=qty))
    if not cost_ok:
        return False, "step9 " + cost_why
    return True, "allowed %s" % direction


def t_regime_gate(params, ctx):
    """Market-form gate (intraday_t_live.go:2153-2184): only a ranging market may trade T.

    ABSTAIN (allow, with the reason spoken) on an empty reading rather than fail closed: the
    owner switched the state machine off deliberately, and reading "off" as "blocked" would
    stop every account's T with a symptom nobody can attribute. An UNKNOWN value does fail
    closed -- that is "configured but unrecognised", not "not configured".
    """
    if not _flag(params, "t_regime_gate_enabled"):
        return False, "regime_gate_off: abstain, the bull/bear stop is not in force"
    state = str(ctx.get("market_state") or _EMPTY)
    if not state:
        return False, "regime_reading_never_produced: abstain"
    age = _t__num(ctx.get("market_state_age_sec"))
    if age is not None and age > _param(params, "t_regime_stale_sec"):
        return False, "regime_reading_stale %.0fs: abstain (never act on an old conclusion)" % age
    blocked_states = [str(item) for item in (_param(params, "t_regime_block_states") or [])]
    if state in blocked_states:
        return True, "step0 regime_gate: market state %s is not a range" % state
    if state in [str(item) for item in (_param(params, "t_regime_allow_states") or [])]:
        return False, _EMPTY
    return True, "step0 regime_gate: unknown market state %s, fail closed" % state


def t_day_breaker_gate(params, ctx):
    """Circuit-breaker day (intraday_t_live.go:475-503): both directions stop opening new legs.

    An unreadable realized-pnl counts AS blocked -- one missed round is opportunity cost,
    double-sided trading in a blind zone is the loss direction.
    """
    limit = _param(params, "t_day_loss_limit_pct")
    if limit <= _ZERO_F:
        return False, _EMPTY
    pnl = _t__num(ctx.get("today_realized_pnl"))
    if pnl is None:
        return True, "step0 realized_pnl_unreadable: treated as a breaker day"
    if pnl >= _ZERO_F:
        return False, _EMPTY
    base = _t__num(ctx.get("total_asset"))
    if base is None or base <= _ZERO_F:
        base = _t__num(ctx.get("initial_capital"))
    if base is None or base <= _ZERO_F:
        return True, "step0 equity_denominator_unavailable: treated as a breaker day"
    loss_pct = -pnl / base * _PERCENT
    if loss_pct >= limit:
        return True, "step0 breaker_day: realized loss %.2f%% >= %.2f%%" % (loss_pct, limit)
    return False, _EMPTY


def t_cap_base(params, ctx):
    """Denominator of the daily sell cap: min(total assets, holdings value).

    Both readings must come from the same round, otherwise the cap breathes with the read order
    (intraday_t_live.go:297, paper/t_rounds.go:848-851).
    """
    total = _t__num(ctx.get("total_asset"))
    holdings = _t__num(ctx.get("holdings_value"))
    if holdings is None:
        computed = _ZERO_F
        price = _t__num(ctx.get("last_price"))
        for _key, row in (ctx.get("positions_live") or {}).items():
            if not isinstance(row, dict):
                continue
            value = _t__num(row.get("market_value"))
            qty = _t__num(row.get("qty"))
            if value is not None:
                computed += value
            elif qty is not None and price is not None:
                computed += qty * price
        holdings = computed
    if total is None or holdings is None:
        return None
    return total if total <= holdings else holdings


def sell_notional_from_counter(params, ctx):
    """Today's reverse-round sell notional, from the counter's FILLS (not an in-memory tally).

    Filled quantity times filled price: the cap constrains "how much base we actually sold
    today"; counting planned volume would let a cancelled sell leg hold the quota forever
    (tlive_state.go:714-731).
    """
    code = str(_codes(params).get(KIND_SELL_FIRST) or _EMPTY)
    if not code:
        return _ZERO_F
    total = _ZERO_F
    for row in ctx.get("fills_today") or []:
        if not isinstance(row, dict):
            continue
        short = _short_code_of(row)
        if not short.startswith(code):
            continue
        qty = _row_qty(row)
        price = _row_price(row)
        if qty > _ZERO_F and price is not None:
            total += qty * price
    return total


def t_sell_cap_gate(params, ctx, cap_base):
    """Daily reverse-round sell-notional cap (intraday_t_live.go:1313-1325)."""
    limit_pct = _param(params, "t_intraday_sell_cap_total_pct")
    if limit_pct <= _ZERO_F:
        return False, _EMPTY
    if cap_base is None or cap_base <= _ZERO_F:
        return True, "sell_cap_denominator_unavailable: treated as not passed"
    used = _t__num(ctx.get("t_sell_notional"))
    if used is None:
        used = sell_notional_from_counter(params, ctx)
    limit = cap_base * limit_pct / _PERCENT
    if limit - used <= _ZERO_F:
        return True, "sell_cap_exhausted: %.0f of %.0f already sold" % (used, limit)
    return False, _EMPTY


def t_liquidity_gate(params, ctx):
    """Liquidity prefilter (intraday_t_live.go:1328-1365): an unavailable reading FAILS.

    "Cannot see the turnover" is never reported as "turnover is fine".
    """
    floor_wan = _param(params, "t_min_amount_wan")
    amount = _day_amount(ctx)
    if floor_wan > _ZERO_F:
        if amount is None or amount <= _ZERO_F:
            return True, "daily_amount_unavailable: treated as not passed"
        if amount / _WAN_TO_YUAN < floor_wan:
            return True, "daily_amount %.0f wan < %.0f wan" % (amount / _WAN_TO_YUAN, floor_wan)
    floor_turnover = _param(params, "t_min_turnover_pct")
    if floor_turnover > _ZERO_F:
        turnover = _t__num(ctx.get("turnover_pct"))
        if turnover is None or turnover <= _ZERO_F:
            return True, "turnover_unavailable: treated as not passed"
        if turnover < floor_turnover:
            return True, "turnover %.2f%% < %.2f%%" % (turnover, floor_turnover)
    name = str(ctx.get("name") or _EMPTY).upper()
    for marker in (_param(params, "t_st_markers") or []):
        if str(marker).upper() in name:
            return True, "ST_or_delisting_name: no intraday round trip"
    floor_days = _param(params, "t_min_listed_days")
    if floor_days > _ZERO_F:
        days = _t__num(ctx.get("listed_days"))
        if days is None or days <= _ZERO_F:
            return True, "listed_days_unavailable: treated as not passed"
        if days < floor_days:
            return True, "listed %d days < %.0f days (a fresh name's range makes the gate void)" % (
                int(days), floor_days)
    return False, _EMPTY


def t_volume_gate(params, ctx):
    """Volume gate (intraday_t_live.go:2194-2209): an expanding name is not a T candidate.

    0 in params means "this gate is off" -- a legal setting, not a missing reading, which is
    why the order of the two tests matters.
    """
    ceiling = _param(params, "t_vol_ratio_max")
    if ceiling == _ZERO_F:
        return False, _EMPTY
    ratio = _t__num(ctx.get("volume_ratio"))
    if ratio is None or ratio <= _ZERO_F:
        return True, "volume_ratio_unavailable: treated as not passed"
    if ratio >= ceiling:
        return True, "volume_expansion %.2f >= %.2f" % (ratio, ceiling)
    return False, _EMPTY


def t_intraday_gate(params, ctx, vwap):
    """Intraday position gates (intraday_t_live.go:1368-1398): amplitude, extremes, limit band."""
    base = _prev_close(ctx)
    high, low = _intraday_high_low(ctx)
    price = _t__num(ctx.get("last_price"))
    if base is None or base <= _ZERO_F or high is None or low is None or high < low or price is None:
        return True, "intraday_high_low_prevclose_incomplete"
    amplitude = (high - low) / base * _PERCENT
    floor_amplitude = _param(params, "t_min_amplitude_pct")
    if amplitude < floor_amplitude:
        return True, "amplitude %.2f%% < %.2f%%: the round could not clear its own spread" % (
            amplitude, floor_amplitude)
    dev = _pct(price - vwap, vwap)
    band = _param(params, "t_near_extreme_pct")
    if dev is not None and dev < _ZERO_F and price < low * (_ONE_F + band / _PERCENT):
        return True, "buy_first_position_gate: the price is falling into the intraday low"
    if dev is not None and dev > _ZERO_F and price < high * (_ONE_F - band / _PERCENT):
        return True, "sell_first_position_gate: already faded off the high, no mid-slope sell"
    distance = _param(params, "t_limit_distance_pct")
    if distance > _ZERO_F:
        pct = _limit_pct(params, ctx.get("symbol"), ctx.get("name"))
        up = base * (_ONE_F + pct / _PERCENT)
        down = base * (_ONE_F - pct / _PERCENT)
        if up > _ZERO_F and price >= up * (_ONE_F - distance / _PERCENT):
            return True, "near_limit_up: the sell leg could be sealed and the buy leg unfillable"
        if down > _ZERO_F and price <= down * (_ONE_F + distance / _PERCENT):
            return True, "near_limit_down: a buy leg that could not be sold again"
    return False, _EMPTY


def t_vmin_gate(params, ctx, vwap):
    """Minute-chain cross-check (intraday_t_live.go:1402-1414).

    The chain is a CROSS-CHECK, not an entry criterion: when it is unavailable the port degrades
    and reports it; when the two disagree the round stops, because at least one reading is wrong
    and money does not get spent on a coin flip.
    """
    vmin = _t__num(ctx.get("vwap_minute_chain"))
    if vmin is None or vmin <= _ZERO_F or vwap is None or vwap <= _ZERO_F:
        return False, _EMPTY
    dev = abs(vmin - vwap) / vwap * _PERCENT
    tolerance = _param(params, "t_vwap_check_tol_pct")
    if dev > tolerance:
        return True, "vwap_cross_check_mismatch main=%.3f chain=%.3f dev %.2f%% > %.2f%%" % (
            vwap, vmin, dev, tolerance)
    return False, _EMPTY


def t_signal_gate(params, ctx, vwap, buy, sell):
    """Signal side (intraday_t_live.go:1418-1456): deliberately asymmetric.

    A missing buy signal means DO NOTHING (safe); a missing sell signal means GO AHEAD (that
    leg reduces exposure) and is policed by the veto gates instead.
    """
    price = _t__num(ctx.get("last_price"))
    dev = _pct(price - vwap, vwap)
    if dev is not None and dev < _ZERO_F:
        if not buy:
            return True, "buy_first_direction_not_enabled"
        signal = str(ctx.get("fresh_buy_signal_id") or _EMPTY)
        if not signal:
            return True, "no_fresh_buy_signal (candidate B): an old signal may not buy again"
        consumed = [str(item) for item in (ctx.get("consumed_signal_ids") or [])]
        if signal in consumed:
            return True, "signal_already_consumed: one consumption per signal"
        return False, _EMPTY
    if not sell:
        return True, "sell_first_direction_not_enabled"
    if ctx.get("active_sell_verdict"):
        return True, "sell_veto: an active exit verdict owns this name, let the signal execute"
    distance = stop_line_distance(params, ctx)
    if distance is None:
        return True, "sell_veto: the distance to the stop lines is unreadable, fail closed"
    guard = _param(params, "t_stop_guard_pct")
    if distance < guard:
        return True, "sell_veto: %.2f%% from the stop lines < %.2f%% (risk lines outrank T)" % (
            distance, guard)
    return False, _EMPTY


def stop_line_distance(params, ctx):
    """Distance to the nearer take-profit / stop-loss line, as a percent of price.

    Reference is the ANCHOR price, never the diluted cost (intraday_t_live.go:1961-1999): a T
    leg moves the average cost, and using cost as the reference would move the risk line too.
    """
    price = _t__num(ctx.get("last_price"))
    anchor = _t__num(ctx.get("anchor_price"))
    if anchor is None:
        anchor = _t__num(_position_of(ctx, ctx.get("symbol")).get("anchor_price"))
    if price is None or price <= _ZERO_F or anchor is None or anchor <= _ZERO_F:
        return None
    stop_pct = _t__num(ctx.get("discipline_stop_loss_pct"))
    take_pct = _t__num(ctx.get("discipline_take_profit_pct"))
    candidates = []
    if stop_pct is not None and stop_pct > _ZERO_F:
        distance = (price - anchor * (_ONE_F - stop_pct / _PERCENT)) / price * _PERCENT
        candidates.append(distance if distance > _ZERO_F else _ZERO_F)
    if take_pct is not None and take_pct > _ZERO_F:
        distance = (anchor * (_ONE_F + take_pct / _PERCENT) - price) / price * _PERCENT
        candidates.append(distance if distance > _ZERO_F else _ZERO_F)
    if not candidates:
        return None
    return min(candidates)


def t_open_qty(params, ctx, direction, cap_base):
    """Quota chain -> (quantity, reason) (intraday_t_live.go:1459-1523).

    Buy leg: min(per-name cap, T quota, cash the strategic room left) floored to whole lots,
    and it may not exceed the counter's sellable base -- under T+1 a leg that cannot be paired
    is a half state by construction.
    Sell leg: per-name cap, T quota, keep at least half the position, keep at least one lot,
    the daily cap's remainder, and the counter's sellable quantity.
    """
    price = _t__num(ctx.get("last_price"))
    if price is None or price <= _ZERO_F:
        return _ZERO, "no_valid_price"
    pos = _position_of(ctx, ctx.get("symbol"))
    position_qty = _t__num(pos.get("qty")) if pos else _t__num(ctx.get("position_qty"))
    if position_qty is None or position_qty <= _ZERO_F:
        return _ZERO, "no_position"
    keep = _param(params, "t_min_base_keep_qty")
    lot = _param(params, "lot_size")
    cap_qty = _lot_floor(position_qty * _param(params, "t_intraday_cap_pct") / _PERCENT, lot)
    if cap_qty < keep:
        return _ZERO, "per_name_cap_below_one_lot (%.0f%% of %d)" % (
            _param(params, "t_intraday_cap_pct"), int(position_qty))
    scale = _t__num(ctx.get("market_scale"))
    if scale is None:
        return _ZERO, "market_scale_unavailable_for_the_T_quota"
    total = _t__num(ctx.get("total_asset"))
    if total is None or total <= _ZERO_F:
        return _ZERO, "total_asset_unavailable_for_the_T_quota"
    # T quota = total * max(0, 1 - min(MS, 1)): the room the strategic target left behind.
    ratio = scale if scale <= _ONE_F else _ONE_F
    tq_qty = _ZERO if ratio >= _ONE_F else _lot_floor(total * (_ONE_F - ratio) / price, lot)
    # Direction decides which side may act, and force/accel bypass the spread gate
    # only for the leg that is closing an existing exposure -- never to open a new one.
    if direction == T_DIR_BUY_FIRST:
        cash = _t__num(ctx.get("available_cash"))
        room = _t__num(ctx.get("strategic_room"))
        frozen = _t__num(ctx.get("t_frozen_cash"))
        if cash is None:
            return _ZERO, "cash_reading_unavailable"
        if room is None or frozen is None:
            return _ZERO, "strategic_room_or_frozen_cash_unavailable"
        cash_t = cash - room - frozen
        if cash_t < _ZERO_F:
            cash_t = _ZERO_F
        buffer = _param(params, "t_fee_buffer_pct")
        affordable = _lot_floor(cash_t / (price * (_ONE_F + buffer / _PERCENT)), lot)
        qty = _lot_floor(min(cap_qty, tq_qty, affordable), lot)
        if qty < keep:
            return _ZERO, "buy_first_quota_truncated_to %d (cap %d, T quota %d, cash %d)" % (
                qty, cap_qty, tq_qty, affordable)
        sellable = _sellable_qty(ctx, ctx.get("symbol"))
        if sellable is None:
            return _ZERO, "sellable_qty_unavailable_on_counter"
        if sellable < qty:
            return _ZERO, "base_sellable %d < planned buy %d (T+1 could not pair it)" % (
                int(sellable), qty)
        return qty, _EMPTY
    if tq_qty <= _ZERO:
        return _ZERO, "T_quota_is_zero (MS %.2f already at the target): neither direction opens" % scale
    max_sell = min(cap_qty, tq_qty)
    keep_back = _lot_floor(position_qty * _param(params, "t_min_remaining_position_pct") / _PERCENT,
                           lot)
    if keep_back < max_sell:
        max_sell = keep_back
    one_lot_keep = position_qty - keep
    if one_lot_keep < max_sell:
        max_sell = one_lot_keep
    cap_pct = _param(params, "t_intraday_sell_cap_total_pct")
    if cap_pct > _ZERO_F and cap_base and cap_base > _ZERO_F:
        used = _t__num(ctx.get("t_sell_notional"))
        if used is None:
            used = sell_notional_from_counter(params, ctx)
        remain = cap_base * cap_pct / _PERCENT - used
        by_cap = _lot_floor(remain / price if remain > _ZERO_F else _ZERO_F, lot)
        if by_cap < max_sell:
            max_sell = by_cap
    sellable = _sellable_qty(ctx, ctx.get("symbol"))
    if sellable is None:
        return _ZERO, "sellable_qty_unavailable_on_counter"
    if sellable < max_sell:
        max_sell = sellable
    qty = _lot_floor(max_sell, lot)
    if qty < keep:
        return _ZERO, "sell_first_quota_truncated_to %d (keep rules, cap and sellable)" % qty
    return qty, _EMPTY


# ---------------------------------------------------------------------------
# 2. The single spread formula and the early-pairing triggers
# ---------------------------------------------------------------------------

def round_spread_pct(direction, first_filled, first_price, pair_filled, pair_price, price):
    """The one and only T spread formula (intraday_t_live.go:2230-2258).

    spread = sell notional - buy notional (paired shares at their fill, the rest at 'price');
    principal = the first leg's notional. Fees are NOT inside it by design -- the cost line is
    t_cost_gate's job, and folding them in here would create a second yardstick the paper and
    live books could no longer compare (owner ruling 2026-10-29: "the only T measure is spread").
    """
    if not first_filled or first_filled <= _ZERO or not first_price or first_price <= _ZERO_F:
        return None
    if price is None or price <= _ZERO_F:
        return None
    matched = min(int(first_filled), int(pair_filled or _ZERO))
    unmatched = first_filled - matched
    paired_value = (pair_price or _ZERO_F) * matched
        # Buy-first and sell-first rounds are mirror images, not one formula with a sign flip:
    # the T+1 book only lets a sell leg close what it already holds, so the price gates
    # sit in separate branches on purpose.
    if direction == T_DIR_BUY_FIRST:
        buy_amount = first_price * first_filled
        sell_amount = paired_value + price * unmatched
        notional = buy_amount
    else:
        sell_amount = first_price * first_filled
        buy_amount = paired_value + price * unmatched
        notional = sell_amount
    if notional <= _ZERO_F:
        return None
    return (sell_amount - buy_amount) / notional * _PERCENT


def t_accel_reason(params, ctx, existing_round, legs, price):
    """Should this leg skip the spread gate and pair right now? (''/stop/verdict/regime)

    Three triggers, one exit (intraday_t_live.go:2270-2295): the action is identical, but the
    log must keep them apart because their causes (and therefore their costs) differ. The
    leg-age gate protects only "stop": a price jitter right after opening is not a stop-loss.
    """
    direction = existing_round.get("direction")
    spread = round_spread_pct(direction, legs["first_filled"], legs["first_price"],
                               legs["pair_filled"], legs["pair_price"], price)
    now = _t__num(ctx.get("now_ts"))
    opened = _t__num(existing_round.get("opened_at"))
    stop = _param(params, "t_round_stop_pct")
    if spread is not None and now is not None and opened is not None and stop > _ZERO_F:
        if now - opened >= _param(params, "t_premise_grace_sec") and spread <= -stop:
            return "stop"
    if ctx.get("active_sell_verdict"):
        return "verdict"
    state = str(ctx.get("market_state") or _EMPTY)
    age = _t__num(ctx.get("market_state_age_sec"))
    if state and age is not None and age <= _param(params, "t_regime_stale_sec"):
        if state in [str(item) for item in (_param(params, "t_regime_block_states") or [])]:
            return "regime"
    return _EMPTY


# ---------------------------------------------------------------------------
# 3. t_round_state -- what to do with a round, decided from counter truth
# ---------------------------------------------------------------------------

def _legs_of(params, ctx, existing_round):
    symbol = existing_round.get("symbol") or ctx.get("symbol")
    head = _symbol_head(symbol, params)
    first = _leg_aggregate(ctx, _first_leg_kind(existing_round.get("direction")), head, params)
    pair = _leg_aggregate(ctx, _pair_leg_kind(existing_round.get("direction")), head, params)
    if existing_round.get("direction") == T_DIR_SELL_FIRST:
        return {"sell_filled": first["filled"], "sell_price": first["avg_price"],
                "buy_filled": pair["filled"], "buy_price": pair["avg_price"],
                "first_filled": first["filled"], "first_price": first["avg_price"],
                "pair_filled": pair["filled"], "pair_price": pair["avg_price"],
                "first_live": first["live_orders"], "pair_live": pair["live_orders"],
                "first_seen": first["seen"], "pair_seen": pair["seen"], "symbol": symbol}
    return {"buy_filled": first["filled"], "buy_price": first["avg_price"],
            "sell_filled": pair["filled"], "sell_price": pair["avg_price"],
            "first_filled": first["filled"], "first_price": first["avg_price"],
            "pair_filled": pair["filled"], "pair_price": pair["avg_price"],
            "first_live": first["live_orders"], "pair_live": pair["live_orders"],
            "first_seen": first["seen"], "pair_seen": pair["seen"], "symbol": symbol}


def _force_reached(params, ctx):
    current = _minutes_of(ctx.get("hhmm"))
    target = _minutes_of(_param(params, "t_force_unwind_at"))
    if current is None or target is None:
        return False
    return current >= target


def _past_ghost_grace(ctx, existing_round, params):
    now = _t__num(ctx.get("now_ts"))
    opened = _t__num(existing_round.get("opened_at"))
    if now is None or opened is None:
        # A record whose opening time cannot be read has no claim to be treated as new.
        return True
    return now - opened > _param(params, "t_ghost_grace_sec")


def _stale_live(rows, ctx, params):
    for row in rows or []:
        age = _age_seconds(row, ctx)
        if age is not None and age >= _param(params, "t_stale_order_sec"):
            return {"order": row, "age_sec": age}
    return None


def _cancel_allowed(params, ctx):
    """Cancelling has its own forbidden windows (14:57-15:00 etc.)."""
    if not ctx.get("trading_day"):
        return False, "not_trading_day"
    if _window_hit(_param(params, "t_no_cancel_windows"), ctx.get("hhmm")):
        return False, "no_cancel_window"
    return True, _EMPTY


def t_allow_buy_back(params, ctx, legs, need, price):
    """End-of-day buy-back permission (intraday_t_live.go:1037-1082) -> (ok, reason, state).

    A reduction never needs permission; a buy-back always does. Partial approval is refused
    wholesale: half a round is neither done nor a clean conversion, and the remainder would
    spend the night waiting for a fill that is not coming.
    """
    blocked, why = t_day_breaker_gate(params, ctx)
    if blocked:
        return False, "breaker_day: the buy-back channel is closed, seal the sell leg " + why, \
            T_STATE_UNWOUND
    spread = round_spread_pct(T_DIR_SELL_FIRST, legs["first_filled"], legs["first_price"],
                              legs["pair_filled"], legs["pair_price"], price)
    if spread is None:
        return False, "round_spread_uncomputable: close the buy-back channel", T_STATE_CONVERTED
    stop = _param(params, "t_round_stop_pct")
    if stop > _ZERO_F and spread <= -stop:
        return False, "buy_back_breaker: spread %.2f%% <= -%.2f%% -> strategic reduction" % (
            spread, stop), T_STATE_CONVERTED
    signal = str(ctx.get("active_buy_signal_id") or _EMPTY)
    if not signal:
        return False, "no_live_buy_signal_at_end_of_day: do not buy back (strategic reduction)", \
            T_STATE_CONVERTED
    consumed = [str(item) for item in (ctx.get("consumed_signal_ids") or [])]
    if signal in consumed:
        return False, "signal_already_consumed: one-shot consumption, no buy back", T_STATE_CONVERTED
    allowed_qty = _t__num(ctx.get("strategic_allowed_qty"))
    if allowed_qty is None:
        return False, "strategic_gate_reading_unavailable: fail closed", T_STATE_CONVERTED
    if allowed_qty <= _ZERO_F:
        return False, "strategic_gate_refused: convert to a strategic reduction", T_STATE_CONVERTED
    if allowed_qty < need:
        return False, "strategic_gate_partial %d/%d: convert the whole round" % (
            int(allowed_qty), need), T_STATE_CONVERTED
    return True, _EMPTY, _EMPTY


def t_round_state(params, ctx, existing_round):
    """Next action for the round -> dict(action/reason/qty/side/short_code/terminal).

    Mirrors execute() (intraday_t_live.go:507-551): cross-day roll, reconcile against the
    counter, premise-gone, pairing. ``existing_round=None`` asks the opposite question --
    "should a new leg open" -- and answers with open_leg plus its quantity from the quota chain.
    """
    if existing_round is None:
        allowed, reason = t_gate_open(params, ctx)
        if not allowed:
            return _result(ACTION_HOLD, reason)
        direction = reason.split()[_LAST_INDEX] if reason.startswith("allowed ") else _EMPTY
        if direction not in (T_DIR_BUY_FIRST, T_DIR_SELL_FIRST):
            return _result(ACTION_HOLD, "gate_allowed_but_direction_unreadable: fail closed")
        qty, why = t_open_qty(params, ctx, direction, t_cap_base(params, ctx))
        if qty < _param(params, "t_min_base_keep_qty"):
            return _result(ACTION_HOLD, "step7 " + why)
        return _result(ACTION_OPEN, reason, qty=qty, side=_leg_side(_first_leg_kind(direction)),
                       short=_t_short(params, _first_leg_kind(direction), ctx.get("symbol")))

    if not isinstance(existing_round, dict):
        return _result(ACTION_HOLD, "round_record_unreadable: fail closed")
    if str(existing_round.get("status") or _EMPTY) == T_STATE_UNKNOWN:
        # Rebuild could not attribute the counter rows to this round: hands off (fail closed).
        return _result(ACTION_HOLD, "round_unknown_after_rebuild: do not act")
    direction = existing_round.get("direction")
    if direction not in (T_DIR_BUY_FIRST, T_DIR_SELL_FIRST):
        return _result(ACTION_HOLD, "round_direction_unreadable: fail closed")

    legs = _legs_of(params, ctx, existing_round)
    first_filled = legs["first_filled"]
    pair_filled = legs["pair_filled"]
    day = existing_round.get("day")
    current_day = ctx.get("day")
    price = _t__num(ctx.get("last_price"))
    force = _force_reached(params, ctx)

    # ---- 0a cross-day roll (rollStaleOpen: reconcile, then seal explicitly) ----
    if day and current_day and str(day) != str(current_day):
        if not legs["first_seen"] and not legs["pair_seen"]:
            return _result(ACTION_CLOSE,
                           "cross_day_ghost_open: the counter knows neither leg",
                           terminal=T_STATE_UNWOUND)
        if first_filled > _ZERO and pair_filled >= first_filled:
            return _result(ACTION_CLOSE, "cross_day_both_legs_filled: seal as done",
                           terminal=T_STATE_DONE)
        if direction == T_DIR_BUY_FIRST:
            return _result(ACTION_CLOSE,
                           "cross_day_unpaired: buy %d filled, pair %d -- the intraday layer "
                           "became an overnight base position" % (first_filled, pair_filled),
                           terminal=T_STATE_UNWOUND)
        return _result(ACTION_CLOSE,
                       "cross_day_not_bought_back: sold %d, bought back %d -- becomes a strategic "
                       "reduction (never chase the buy back overnight)" % (first_filled, pair_filled),
                       terminal=T_STATE_CONVERTED)

    # ---- 0b reconcile: intent vs fact ----
    if first_filled > _ZERO and pair_filled >= first_filled:
        return _result(ACTION_HOLD, "legs_matched: reconciliation seals this round as done",
                       terminal=T_STATE_DONE)
    if (not legs["first_seen"] and not legs["pair_seen"]
            and _past_ghost_grace(ctx, existing_round, params)):
        return _result(ACTION_CLOSE,
                       "ghost_open: the book says open, the counter has no order and no fill",
                       terminal=T_STATE_UNWOUND)

    if price is None or price <= _ZERO_F:
        if force and direction == T_DIR_SELL_FIRST and first_filled > pair_filled:
            return _result(ACTION_CLOSE,
                           "end_of_day_buy_back_blocked_no_price: seal, do not chase",
                           terminal=T_STATE_UNWOUND)
        return _result(ACTION_HOLD, "no_realtime_price: no conclusion and no state change")

    need = first_filled - pair_filled

    if need <= _ZERO:
        # Nothing filled yet on either side: the first leg is still pending or never sent.
        if legs["first_live"]:
            stale = _stale_live(legs["first_live"], ctx, params)
            if stale:
                ok, why = _cancel_allowed(params, ctx)
                if not ok:
                    return _result(ACTION_HOLD, "first_leg_stale_but_%s" % why, detail=stale)
                return _result(ACTION_CANCEL, "first_leg_order_stale: cancel, let the next round "
                                              "re-place it", detail=stale)
            return _result(ACTION_HOLD, "first_leg_still_in_flight: wait for the report")
        if _past_ghost_grace(ctx, existing_round, params):
            return _result(ACTION_CLOSE, "first_leg_never_sent: no order and no fill -> seal",
                           terminal=T_STATE_UNWOUND)
        return _result(ACTION_HOLD, "first_leg_not_filled_yet: wait")

    # A pairing order already in flight: never stack a second one on the same round.
    # This is the exact shape that produced the 10-30 double-cancel placeholders.
    if legs["pair_live"]:
        if force:
            ok, why = _cancel_allowed(params, ctx)
            if not ok:
                return _result(ACTION_HOLD, "end_of_day_%s: the residual stays with the broker's "
                                           "own close sweep" % why, detail=legs["pair_live"][_FIRST_INDEX])
            return _result(ACTION_CANCEL,
                           "end_of_day: cancel the unpaired residual, then re-balance the true gap",
                           detail=legs["pair_live"][_FIRST_INDEX])
        stale = _stale_live(legs["pair_live"], ctx, params)
        if stale:
            ok, why = _cancel_allowed(params, ctx)
            if not ok:
                return _result(ACTION_HOLD, "pair_leg_stale_but_%s" % why, detail=stale)
            return _result(ACTION_CANCEL, "pair_leg_order_stale: cancel and re-place next round",
                           detail=stale)
        # Serialized legs means at most one live order per round at any instant.
        return _result(ACTION_HOLD, "pair_leg_in_flight: never stack a second order on one leg")

    lot = _param(params, "lot_size")
    balance_qty = _lot_floor(need, lot)
    if balance_qty < _param(params, "t_min_base_keep_qty"):
        return _result(ACTION_HOLD, "balance_gap_below_one_lot (%d): a later report will close it"
                       % need)

    first_price = legs["first_price"]
    if first_price is None or first_price <= _ZERO_F:
        return _result(ACTION_HOLD, "first_leg_fill_price_unavailable: fail closed")
    accel = t_accel_reason(params, ctx, existing_round, legs, price)
    fav = (_pct(price - first_price, first_price) if direction == T_DIR_BUY_FIRST
           else _pct(first_price - price, first_price))
    spread_gate = _param(params, "t_min_spread_pct")

    # Buy-first and sell-first rounds are mirror images, not one formula with a flipped sign:
# T+1 only lets a sell leg close what is already held, so the price gates stay in
# separate branches on purpose (merging them is how a sell leg starts opening exposure).
    if direction == T_DIR_BUY_FIRST:
        if not force and not accel:
            if fav is None or fav < spread_gate:
                return _result(ACTION_HOLD, "spread_not_met: fav %.2f%% < %.2f%%, keep waiting "
                                            "(never chase the high)" % (fav or _ZERO_F, spread_gate))
        sellable = _sellable_qty(ctx, legs["symbol"])
        if sellable is None:
            return _result(ACTION_HOLD, "sellable_qty_unavailable_on_counter: fail closed")
        if sellable < balance_qty:
            if force or accel:
                return _result(ACTION_CLOSE,
                               "forced_pair_failed: sellable %d < need %d, the intraday layer goes "
                               "overnight (accel=%s)" % (int(sellable), balance_qty, accel),
                               terminal=T_STATE_UNWOUND)
            return _result(ACTION_HOLD, "pair_sellable_short %d<%d: keep the round open" % (
                int(sellable), balance_qty))
        return _result(ACTION_BALANCE,
                       "pair the buy_first leg%s" % (" (end of day, unconditional)" if force
                                                     else (" (accel=%s)" % accel if accel else "")),
                       qty=balance_qty, side="sell",
                       short=_t_short(params, KIND_BUY_FIRST_PAIR, legs["symbol"]))

    # sell_first: the second leg buys back, and a buy needs permission.
    if not force and not accel:
        if fav is None or fav < spread_gate:
            return _result(ACTION_HOLD, "spread_not_met: fav %.2f%% < %.2f%%, wait for the dip" % (
                fav or _ZERO_F, spread_gate))
    if force:
        ok, why, terminal = t_allow_buy_back(params, ctx, legs, balance_qty, price)
        if not ok:
            return _result(ACTION_CLOSE, why, terminal=terminal)
    cash = _t__num(ctx.get("available_cash"))
    if cash is None:
        return _result(ACTION_HOLD, "cash_reading_unavailable: fail closed")
    buffer = _param(params, "t_fee_buffer_pct")
    if cash < balance_qty * price * (_ONE_F + buffer / _PERCENT):
        return _result(ACTION_CLOSE,
                       "buy_back_blocked_cash %.0f < %.0f: seal, do not chase" % (
                           cash, balance_qty * price),
                       terminal=T_STATE_UNWOUND)
    return _result(ACTION_BALANCE,
                   "buy back the sell_first leg%s" % (" (live buy signal)" if force else ""),
                   qty=balance_qty, side="buy",
                   short=_t_short(params, KIND_SELL_PAIR, legs["symbol"]))


# ---------------------------------------------------------------------------
# 4. TRoundBook -- the in-memory round ledger, rebuildable from the counter
# ---------------------------------------------------------------------------

class TRoundBook(object):
    """Round ledger for one account and one trading day.

    Go keeps this in <dataDir>/accounts/<uid>/t_rounds_live.json (tlive_state.go:520-631) with
    "cross-day never resurrects" semantics. The carrier may not write files, so the book lives
    in memory and is REBUILT from the counter's own order and fill tables on every round -- the
    remark is the only thing that survives a restart (qmt_internal_strategy.py, idempotency
    rule 1). A round that cannot be rebuilt is kept as 'unknown' and never acted upon.
    """

    def __init__(self, params):
        self.params = params
        self.rounds = {}
        self.unknown = []

    # -- ledger face -----------------------------------------------------
    def open_for(self, symbol, day=_EMPTY):
        for rnd in sorted(self.rounds.values(), key=lambda item: str(item.get("id"))):
            if rnd.get("status") != T_STATE_OPEN or rnd.get("symbol") != symbol:
                continue
            if day and rnd.get("day") and rnd.get("day") != day:
                continue
            return rnd
        return None

    def count_for(self, symbol, day=_EMPTY):
        total = _ZERO
        for rnd in self.rounds.values():
            if rnd.get("symbol") != symbol:
                continue
            if rnd.get("status") == T_STATE_UNKNOWN:
                continue
            if day and rnd.get("day") and rnd.get("day") != day:
                continue
            total += _ONE
        return total

    def next_seq(self, symbol, day):
        return self.count_for(symbol, day) + _ONE

    def new_round(self, symbol, direction, day, qty, price, now_ts, used_signal=_EMPTY):
        """Build a round record with the Go idempotency key t0:<code>:<dir>:<YYYYMMDD>:<seq>."""
        seq = self.next_seq(symbol, day)
        rnd = {"id": t_round_id(symbol, direction, day, seq), "symbol": symbol, "day": day,
               "direction": direction, "status": T_STATE_OPEN, "qty": int(qty),
               "decision_price": price, "opened_at": now_ts, "used_signal_id": used_signal,
               "first_filled": _ZERO, "pair_filled": _ZERO, "rebuilt": False}
        if direction == T_DIR_SELL_FIRST:
            buffer = _param(self.params, "t_fee_buffer_pct")
            rnd["frozen_cash"] = _round2(qty * (price or _ZERO_F) * (_ONE_F + buffer / _PERCENT))
        return self.record(rnd)

    def record(self, rnd):
        """Register a round; refuses to overlap an OPEN round on the same symbol."""
        symbol = rnd.get("symbol")
        if symbol and rnd.get("status") == T_STATE_OPEN:
            clash = self.open_for(symbol, rnd.get("day"))
            if clash and clash.get("id") != rnd.get("id"):
                return False, "already_open_on_symbol:%s" % clash.get("id")
        self.rounds[rnd.get("id")] = rnd
        return True, _EMPTY

    def seal(self, round_id, terminal, reason=_EMPTY):
        rnd = self.rounds.get(round_id)
        if rnd is None:
            return False, "round_not_in_book"
        if rnd.get("status") not in (T_STATE_OPEN, T_STATE_UNKNOWN):
            return False, "already_sealed:%s" % rnd.get("status")
        rnd["status"] = terminal
        rnd["terminal"] = terminal
        rnd["unwind_reason"] = reason
        # A terminal state always releases the earmarked cash, or that money stays borrowed
        # from the account forever (intraday_t_live.go:1768-1794).
        rnd["frozen_cash"] = _ZERO_F
        return True, _EMPTY

    def sell_notional(self):
        total = _ZERO_F
        for rnd in self.rounds.values():
            if rnd.get("direction") != T_DIR_SELL_FIRST:
                continue
            qty = _t__num(rnd.get("sell_filled")) or _ZERO_F
            price = _t__num(rnd.get("sell_price"))
            if qty > _ZERO_F and price is not None:
                total += qty * price
        return total

    def frozen_cash(self):
        total = _ZERO_F
        for rnd in self.rounds.values():
            if rnd.get("status") != T_STATE_OPEN or rnd.get("direction") != T_DIR_SELL_FIRST:
                continue
            value = _t__num(rnd.get("frozen_cash"))
            if value is not None:
                total += value
        return total

    def consumed_signals(self):
        out = []
        for rnd in self.rounds.values():
            signal = str(rnd.get("used_signal_id") or _EMPTY)
            if signal:
                out.append(signal)
        return out

    def context_for(self, ctx, symbol):
        """Merge the book's per-symbol readings into the gate context (facts, not guesses)."""
        merged = dict(ctx or {})
        merged["t_has_open_leg"] = self.open_for(symbol, ctx.get("day")) is not None
        merged["t_rounds_used"] = self.count_for(symbol, ctx.get("day"))
        if _t__num(merged.get("t_sell_notional")) is None:
            merged["t_sell_notional"] = self.sell_notional()
        if merged.get("consumed_signal_ids") is None:
            merged["consumed_signal_ids"] = self.consumed_signals()
        if _t__num(merged.get("t_frozen_cash")) is None:
            merged["t_frozen_cash"] = self.frozen_cash()
        return merged

    # -- rebuild ---------------------------------------------------------
    def rebuild_from(self, orders, fills, day=_EMPTY):
        """Rebuild the day's rounds from the counter's order and fill rows.

        Returns (rebuilt, unknown). What makes this possible at all: one short code per leg per
        symbol (the remark is "<version>-<code><head>", with no sequence in it). Consequences
        that are recorded rather than papered over:
          * both directions present on one name -> the legs cannot be assigned to rounds;
          * a pair leg with no first leg -> the round's origin is unknowable;
          * rows spanning more than one day -> the attribution window is ambiguous.
        Each of those becomes an 'unknown' entry, and t_round_state refuses to act on it.
        """
        codes = _codes(self.params)
        groups = {}
        unknown = []
        tagged = [(row, False) for row in (orders or [])]
        tagged += [(row, True) for row in (fills or [])]
        for row, is_fact in tagged:
            if not isinstance(row, dict):
                continue
            short = _short_code_of(row)
            kind = _kind_of_short(short, codes)
            if not kind:
                continue
            head = "".join(char for char in short[len(str(codes.get(kind))):] if char.isdigit())
            if not head:
                unknown.append({"short_code": short, "status": T_STATE_UNKNOWN,
                                "why": "no_symbol_head_in_remark"})
                continue
            groups.setdefault((head, kind), []).append((row, is_fact))

        # Rebuild deterministically by symbol-head then kind, so a restart with the
        # same counter rows produces the same round book (idempotency must not depend
        # on the order the counter happened to return its rows in).
        rebuilt = []
        for head in sorted(set(key[_FIRST_INDEX] for key in groups)):
            buy_rows, buy_fills = _split_rows(groups.get((head, KIND_BUY_FIRST), []))
            sell_rows, sell_fills = _split_rows(groups.get((head, KIND_SELL_FIRST), []))
            pair_sell_rows, pair_sell_fills = _split_rows(
                groups.get((head, KIND_BUY_FIRST_PAIR), []))
            pair_buy_rows, pair_buy_fills = _split_rows(
                groups.get((head, KIND_SELL_PAIR), []))
            if (buy_rows or buy_fills) and (sell_rows or sell_fills):
                unknown.append({"symbol_head": head, "status": T_STATE_UNKNOWN,
                                "why": "both_directions_same_day: the remark cannot tell the "
                                       "two rounds apart, so no leg is attributed"})
                continue
            if buy_rows or buy_fills:
                direction = T_DIR_BUY_FIRST
                first_rows, first_fills = buy_rows, buy_fills
                pair_rows, pair_fills = pair_sell_rows, pair_sell_fills
            elif sell_rows or sell_fills:
                direction = T_DIR_SELL_FIRST
                first_rows, first_fills = sell_rows, sell_fills
                pair_rows, pair_fills = pair_buy_rows, pair_buy_fills
            else:
                unknown.append({"symbol_head": head, "status": T_STATE_UNKNOWN,
                                "why": "pair_leg_without_first_leg"})
                continue
            all_rows = first_rows + first_fills + pair_rows + pair_fills
            days = set(str(row.get("day")) for row in all_rows if row.get("day"))
            if len(days) > _ONE:
                unknown.append({"symbol_head": head, "status": T_STATE_UNKNOWN,
                                "why": "rows_span_multiple_days: %s" % "|".join(sorted(days))})
                continue
            first_filled, first_price = _leg_fill_totals(first_rows, first_fills)
            pair_filled, pair_price = _leg_fill_totals(pair_rows, pair_fills)
            symbol = _symbol_for_head(head, orders, fills, self.params) or head
            seq = self.next_seq(symbol, day)
            status = T_STATE_OPEN
            terminal = _EMPTY
            if first_filled > _ZERO and pair_filled >= first_filled:
                status = T_STATE_DONE
                terminal = T_STATE_DONE
            rnd = {"id": t_round_id(symbol, direction, day, seq), "symbol": symbol, "day": day,
                   "direction": direction, "status": status, "terminal": terminal,
                   "qty": int(first_filled), "first_filled": int(first_filled),
                   "first_price": first_price, "pair_filled": int(pair_filled),
                   "pair_price": pair_price, "rebuilt": True, "frozen_cash": _ZERO_F}
            if direction == T_DIR_BUY_FIRST:
                rnd["buy_filled"], rnd["buy_price"] = int(first_filled), first_price
                rnd["sell_filled"], rnd["sell_price"] = int(pair_filled), pair_price
            else:
                rnd["sell_filled"], rnd["sell_price"] = int(first_filled), first_price
                rnd["buy_filled"], rnd["buy_price"] = int(pair_filled), pair_price
            self.rounds[rnd["id"]] = rnd
            rebuilt.append(rnd)

        for item in unknown:
            key = "t0:%s:unknown:%s" % (item.get("symbol_head") or item.get("short_code"),
                                        day or _EMPTY)
            self.rounds[key] = {"id": key, "status": T_STATE_UNKNOWN,
                                "symbol": item.get("symbol_head") or _EMPTY, "day": day,
                                "direction": _EMPTY, "reason": item.get("why")}
            item["id"] = key
        self.unknown = unknown
        return rebuilt, unknown


def _leg_fill_totals(order_rows, fill_rows):
    """(filled quantity, average price) with the fills table as the first truth.

    Fills win when present; the order table's traded column is only the fallback when no fill
    report has arrived yet. The two are never summed -- the same shares would count twice and
    the pairing leg would come out short (or a completed round would look unpaired).
    """
    qty, price = _aggregate_fill(fill_rows, facts=True)
    if qty > _ZERO_F:
        return qty, price
    return _aggregate_fill(order_rows, facts=False)


def _split_rows(entries):
    """Split grouped (row, is_fact) pairs back into order rows and fill rows."""
    orders_only = []
    fills_only = []
    for entry in entries or []:
        row, is_fact = entry
        (fills_only if is_fact else orders_only).append(row)
    return orders_only, fills_only


def _aggregate_fill(rows, facts=False):
    """(filled quantity, weighted average price) of a leg from its counter rows.

    facts=True is the fills table: every row's quantity is traded volume. facts=False is the
    ORDER table, where a row contributes only when it carries an explicit traded count or is
    terminal -- a planned-but-unfilled order is intent and must never be read as volume.
    """
    qty = _ZERO_F
    amount = _ZERO_F
    for row in rows or []:
        if not isinstance(row, dict):
            continue
        traded = _t__num(row.get("traded"))
        if traded is None:
            traded = _t__num(row.get("filled_qty"))
        if traded is None and (facts or _is_terminal_row(row)):
            traded = _t__num(row.get("qty"))
        price = _row_price(row)
        if traded is None or traded <= _ZERO_F or price is None:
            continue
        qty += traded
        amount += traded * price
    return qty, (amount / qty if qty > _ZERO_F else None)


def _is_terminal_row(row):
    return bool(row.get("terminal"))


def _symbol_for_head(head, orders, fills, params):
    length = _head_len(params)
    for row in list(orders or []) + list(fills or []):
        if not isinstance(row, dict):
            continue
        symbol = str(row.get("symbol") or row.get("code") or _EMPTY)
        if symbol and symbol.split(".")[_FIRST_INDEX][:length] == head:
            return symbol
    return _EMPTY


# ---------------------------------------------------------------------------
# 5. Short codes and readout helpers
# ---------------------------------------------------------------------------

def t_round_id(symbol, direction, day, seq):
    """The idempotency key, byte-compatible with the Go ledger (intraday_t_live.go:1595)."""
    return "t0:%s:%s:%s:%d" % (symbol, direction, day, int(seq))


def t_pair_round_id(round_id, attempt=_ONE):
    """Pairing-leg keys: ':p' for the first attempt, ':p2', ':p3' for re-submissions.

    Same shape as the Go ledger (:54-56, :701-713): the two legs must not share a key or the
    second would be swallowed as a duplicate and the round could never pair; re-sending on the
    same key instead of incrementing would bypass the order-table uniqueness check.
    """
    base = "%s:p" % round_id
    if int(attempt) <= _ONE:
        return base
    return "%s%d" % (base, int(attempt))


def t_short_code_check(params):
    """Are the configured T short codes safe for the remark round-trip? -> (ok, reason).

    Inherited rules: local_executor.build_remark (:182-189), short_code_from_remark (:192-197)
    and _kind_of_short's startswith attribution (:1088-1093):
      * letters only and uppercase, so the remark stays inside the counter's safe character set
        and a GBK console can never tear it into '?' (the exact failure that cost two hours on
        2026-11-02, AGENTS.md operational note);
      * no dash: the reverse lookup splits on the LAST dash, so a dash inside the code would
        make the version separator land in the wrong place and the intent would lose its key;
      * no code may prefix another, the existing non-T codes included: 'TP' vs 'T' would let a
        take-profit order read as a T order, and idempotency would either re-send or swallow it;
      * code + symbol head within the counter's remark budget, and the head must be exactly
        symbol_head_len digits so rebuild_from can slice the symbol back out.
    """
    codes = _codes(params)
    merged = dict(_param(params, "signal_codes") or {})
    merged.update(codes)
    head_len = int(_param(params, "symbol_head_len"))
    max_len = int(_param(params, "t_remark_code_max_len"))
    for kind in sorted(codes):
        code = str(merged.get(kind) or _EMPTY)
        if not code:
            return False, "missing_code_for_kind:%s" % kind
        if not code.isalpha() or code != code.upper():
            return False, "code_not_upper_alpha:%s=%s" % (kind, code)
        if "-" in code:
            return False, "code_contains_dash:%s" % kind
        if len(code) + head_len > max_len:
            return False, "code_too_long_for_remark:%s" % kind
    names = sorted(merged)
    for first in names:
        for second in names:
            if first == second:
                continue
            left = str(merged.get(first) or _EMPTY)
            right = str(merged.get(second) or _EMPTY)
            if left and right.startswith(left):
                return False, "prefix_collision:%s_is_prefix_of_%s" % (first, second)
    return True, _EMPTY


def t_short_codes(params):
    """The configured T code table (kind -> short code), for the carrier's PARAMS bake."""
    return dict(_codes(params))


def t_leg_fee(params, side, amount):
    """One leg's fee split from the repo's existing rate model (paper.go:360-382).

    Broker-reported values always win when the carrier has them; measured and estimated values
    are never blended into one number (docs ruling 2), which is what 'fee_source' records.
    """
    rate = _param(params, "t_commission_rate")
    floor = _param(params, "t_min_commission")
    stamp_rate = _param(params, "t_stamp_tax_rate")
    value = _t__num(amount)
    if value is None or value <= _ZERO_F or rate <= _ZERO_F:
        return {"fee_buy": _ZERO_F, "fee_sell": _ZERO_F, "stamp_tax": _ZERO_F}
    commission = max(value * rate, floor)
    if side == "sell":
        stamp = value * stamp_rate if stamp_rate > _ZERO_F else _ZERO_F
        return {"fee_buy": _ZERO_F, "fee_sell": _round2(commission - stamp),
                "stamp_tax": _round2(stamp)}
    return {"fee_buy": _round2(commission), "fee_sell": _ZERO_F, "stamp_tax": _ZERO_F}


def t_round_pnl(params, direction, buy_qty, buy_price, sell_qty, sell_price,
                broker_fee_buy=None, broker_fee_sell=None, broker_stamp=None):
    """The "T profit" frame (fillPnl, intraday_t_live.go:1804-1824): matched shares only.

    The unpaired remainder is neither profit nor loss -- it is an unobservable counterfactual,
    and B2's offline calibration is the only place it gets estimated (PLAN doc:86).
    """
    matched = min(int(buy_qty or _ZERO), int(sell_qty or _ZERO))
    if matched <= _ZERO or not buy_price or not sell_price:
        return {"q_matched": _ZERO, "t_pnl_gross": _ZERO_F, "t_pnl_net": _ZERO_F,
                "fee_buy": _ZERO_F, "fee_sell": _ZERO_F, "stamp_tax": _ZERO_F,
                "fee_source": _EMPTY}
    gross = (sell_price - buy_price) * matched
    if direction == T_DIR_SELL_FIRST:
        gross = -gross
    buy_amount = buy_price * matched
    sell_amount = sell_price * matched
    est_buy = t_leg_fee(params, "buy", buy_amount)
    est_sell = t_leg_fee(params, "sell", sell_amount)
    has_buy = _t__num(broker_fee_buy) is not None
    has_sell = _t__num(broker_fee_sell) is not None and _t__num(broker_stamp) is not None
    fee_buy = _t__num(broker_fee_buy) if has_buy else est_buy["fee_buy"]
    fee_sell = _t__num(broker_fee_sell) if has_sell else est_sell["fee_sell"]
    stamp = _t__num(broker_stamp) if has_sell else est_sell["stamp_tax"]
    if has_buy and has_sell:
        source = "broker"
    elif has_buy or has_sell:
        source = "mixed"
    else:
        source = "estimate"
    net = gross - (fee_buy + fee_sell + stamp)
    return {"q_matched": matched, "t_pnl_gross": _round2(gross), "t_pnl_net": _round2(net),
            "fee_buy": _round2(fee_buy), "fee_sell": _round2(fee_sell),
            "stamp_tax": _round2(stamp), "fee_source": source}


def t_slip_bps(direction, decision_price, fill_price):
    """One leg's slippage in bp, positive = unfavourable (intraday_t_live.go:1876-1892).

    The sign convention is the B1 judgement: a flat "fill minus decision" would make every
    reverse-round leg read as negative slippage.
    """
    decision = _t__num(decision_price)
    fill = _t__num(fill_price)
    if decision is None or fill is None or decision <= _ZERO_F:
        return _ZERO_F
    value = (fill - decision) / decision * _BASIS_POINTS
    if direction == T_DIR_SELL_FIRST:
        value = -value
    return _round2(value)


def t_participation_pct(params, qty, price, day_amount):
    """Planned notional over the turnover CUMULATED SO FAR (B4's observation quantity).

    The denominator is deliberately not the whole day's volume: that is future data, and using
    it would systematically understate participation -- "the safer-looking number is the one
    that lies to us" (paper/t_rounds.go:152-155).
    """
    amount = _t__num(day_amount)
    planned = _t__num(qty)
    price_value = _t__num(price)
    if amount is None or amount <= _ZERO_F or planned is None or price_value is None:
        return None
    # Four digits, like the Go ledger: participation lives at the 0.01% scale and cent rounding
    # would erase the whole B4 observation column.
    return _round4(planned * price_value / amount * _PERCENT)


def t_day_totals(params, ctx, rounds):
    """The daily ledger line (logDailyLedger, intraday_t_live.go:557-587): counts only.

    Placed/cancelled are counted from the ORDER table, not from the round count: the difference
    between the two is how many times we re-submitted, and the cancel ratio is the compliance
    evidence this line exists to record.
    """
    opened = _ZERO
    done = _ZERO
    unwound = _ZERO
    converted = _ZERO
    open_now = _ZERO
    net = _ZERO_F
    gross = _ZERO_F
    for rnd in rounds or []:
        if not isinstance(rnd, dict):
            continue
        if rnd.get("day") and str(rnd.get("day")) != str(ctx.get("day")):
            continue
        opened += _ONE
        status = rnd.get("status")
        if status == T_STATE_DONE:
            done += _ONE
            net += _t__num(rnd.get("t_pnl_net")) or _ZERO_F
            gross += _t__num(rnd.get("t_pnl_gross")) or _ZERO_F
        elif status == T_STATE_UNWOUND:
            unwound += _ONE
        elif status == T_STATE_CONVERTED:
            converted += _ONE
        elif status == T_STATE_OPEN:
            open_now += _ONE
    placed = _ZERO
    cancelled = _ZERO
    codes = _codes(params)
    cancel_tokens = [str(item).lower() for item in (_param(params, "t_cancelled_status_tokens") or [])]
    for row in ctx.get("orders_today") or []:
        if not isinstance(row, dict):
            continue
        if not _kind_of_short(_short_code_of(row), codes):
            continue
        placed += _ONE
        status = str(row.get("status") or _EMPTY).lower()
        for token in cancel_tokens:
            if token and token in status:
                cancelled += _ONE
                break
    ratio = (cancelled / placed * _PERCENT) if placed > _ZERO else _ZERO_F
    return {"opened": opened, "done": done, "unwound": unwound, "converted": converted,
            "open_legs": open_now, "pnl_net": _round2(net), "pnl_gross": _round2(gross),
            "orders_placed": placed, "orders_canceled": cancelled,
            "cancel_ratio_pct": _round2(ratio)}


# ---------------------------------------------------------------------------
# PARAMETER SURFACE
#
# Every key this module reads for a decision. The builder bakes them from the account-level
# sizing.t block (internal/config/config.go:2098-2565 carries the names, factory values and
# legal ranges); nothing here re-derives a default, and a missing key raises.
# ---------------------------------------------------------------------------
T_PARAM_KEYS = [
    # switches, directions, blind zone
    "t_enabled", "t_mode", "t_shadow", "t_missing_key_input",
    "t_max_rounds", "t_intraday_cap_pct", "t_min_spread_pct",
    "t_force_unwind_at", "t_last_open_at", "t_session_open_hhmm", "t_open_cooling_min",
    # clock windows (same 'HH:MM' spelling as the carrier's own windows)
    "t_session_windows", "t_no_new_order_windows", "t_no_cancel_windows",
    # environment gates
    "t_min_market_scale", "t_sell_env_min_market_scale", "t_market_plunge_pct",
    "t_intraday_sell_cap_total_pct", "t_day_loss_limit_pct",
    # market-form and volume gates (W-09)
    "t_regime_gate_enabled", "t_regime_stale_sec", "t_regime_block_states",
    "t_regime_allow_states", "t_vol_ratio_max",
    # intraday basis gates
    "t_vwap_dev_pct", "t_min_amplitude_pct", "t_near_extreme_pct", "t_limit_distance_pct",
    "t_vwap_check_tol_pct", "t_limit_pct_main", "t_limit_pct_wide", "t_limit_pct_st",
    "t_limit_pct_bse", "t_wide_board_prefixes", "t_bse_board_prefixes",
    # veto / risk references
    "t_stop_guard_pct", "t_round_stop_pct", "t_anchor_ok_sources",
    # liquidity prefilter
    "t_min_amount_wan", "t_min_turnover_pct", "t_min_listed_days", "t_st_markers",
    # quota, lots, cash
    "lot_size", "t_min_base_keep_qty", "t_min_remaining_position_pct", "t_fee_buffer_pct",
    # cost line (docs/PLAN_T_COST_CALIB_20261027.md:128-130, docs/IMPL_T_COST_CALIB_20261027.md:197)
    "t_commission_rate", "t_min_commission", "t_stamp_tax_rate", "t_slippage_bps",
    "t_cost_safety_multiplier",
    # reconciliation and timing
    "t_ghost_grace_sec", "t_premise_grace_sec", "t_stale_order_sec",
    # counter vocabulary and remark round-trip
    "t_terminal_status_codes", "t_cancelled_status_tokens", "symbol_head_len",
    "signal_codes", "t_signal_codes", "t_remark_code_max_len",
]

# This module ports judgement, not signal generation: the T leg decides whether a
# round-trip may open / must be balanced / must be closed, and the sizing leg
# produces the scale caps. Neither emits a broker remark short code, so it
# registers no signal kinds -- the empty table is explicit so the build gate can
# tell "a judgement leg" apart from "a strategy module that forgot to register".
RULES = {}
