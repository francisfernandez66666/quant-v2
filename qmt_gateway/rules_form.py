#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.rules_form -- the four bear (short-side) tactics, ported from Go.

Ported sources, one section each, no invention of thresholds:
  internal/strategies/break_down/break_down.go      (bd_* keys)
  internal/strategies/high_churn/high_churn.go      (hc_* keys)
  internal/strategies/leader_decay/leader_decay.go  (ld_* keys)
  internal/strategies/good_news_fade/good_news_fade.go (gf_* keys)
The derived indicators each Go tactic read from shortbase.Data are rebuilt here
from the flat context the carriers already assemble, following
internal/combat_agent/adapter.go buildShortData() so the two sides agree.

Why this file exists (broker rule effective 2026-11-05)
-------------------------------------------------------
After 11-05 the only automated execution path is a QMT built-in strategy whose
signals are produced entirely inside the terminal. So every signal rule must
exist as pure Python with no file, no socket and no process access.

Hard contract (a build gate enforces it later; violating it fails the batch)
----------------------------------------------------------------------------
  1. PURE ASCII. The artifact is loaded into a GBK Windows terminal: a non-ASCII
     byte either dies or silently turns a Chinese comparison literal into '?',
     which is how the "phase contains retreat" test used to break. That is also
     why the sentiment keyword list is a PARAMS value (params["ld_retreat_phase
     _keys"]) and never a literal in this file.
  2. No import beyond the standard library, and none at all is used here: this
     module calls no open / socket / requests / urllib / sqlite / subprocess /
     logging / os / sys. It is computation only.
  3. ZERO NUMERIC LITERALS INSIDE RULE BODIES. Every threshold is read as
     params["<key>"]. A threshold spelled out in a rule is a threshold that
     keeps its old value after someone turns the knob -- the exact failure this
     repo has been bitten by. The only numbers outside the rule bodies are the
     STRUCTURAL CONSTANTS block below (an index position, a percent unit, a
     date prefix length); none of them is a business threshold and none of
     them can make a trading decision.
  4. Every rule is `def rule_<name>(params, ctx, side_hint="")` and returns
     either None (no action) or the signal dict built by make_signal(), the
     shared builder of the local executor, so the short code and the idempotency
     contract are identical on both sides. _f_emit_signal() routes to that shared
     helper whenever it is in scope (it always is in the published artifact);
     this module deliberately does not itself define the name `make_signal`,
     because the builder refuses two ported modules owning one top-level name.
  5. MISSING DATA NEVER BECOMES A SIGNAL. A reading the sandbox cannot get
     makes the rule decline (return None), it is never folded into a value that
     happens to be convenient. Two directions are distinguished on purpose:
       * a reading whose ABSENCE would let a signal through (is_leader,
         ladder_height, afternoon_reseal, news_event.propagation, the news
         event window) -> decline;
       * a reading the Go adapter itself documents as "empty means zero"
         (SectorLimitUpDropPct, EmotionPhase) -> contributes zero points,
         which can only LOWER the score, never manufacture one.
  6. The Go "watch" band (50 to threshold) is deliberately not emitted: in the
     sandbox a watch has no action attached and an emitted sell is a real order.
     Only the Go pass gate (total >= threshold) produces a signal.
  7. RULES at the bottom of this file is the registry the builder reads by AST.
     Its keys are the signal kinds and must match both params["<rule>_kind"] and
     the params["signal_codes"] table -- a kind without a short code cannot be
     proven idempotent, so the build fails rather than publishes it.

What is NOT carried over: Go's Evaluation.Level ("full_chain"/"watch"),
Confidence (total/100 plus a documented bonus) and Priority (P2, P1 for
leader_decay). The signal dict has no field for them and the sandbox has no
watch band to place them in, so the pass gate alone survives; the numbers that
made them up are still baked as params so a re-calibration on the Go side shows
up here as a parameter diff, not as a code diff.
"""

# ---------------------------------------------------------------------------
# STRUCTURAL CONSTANTS -- NOT THRESHOLDS.
#
# Index positions, unit conversion and a date-prefix length. They encode no
# business decision; changing one of them cannot flip a signal, it can only
# break the plumbing. Business thresholds all live in params.
# ---------------------------------------------------------------------------
_F_FIRST = 0        # first element of a split
_F_ZERO = 0.0       # "is this number usable at all" sentinel
_F_LAST = -1        # the newest bar sits at the tail of the list
_F_ONE = 1          # neighbour step (the bar before the newest)
_F_PCT = 100.0      # ratio to percent unit conversion
_F_DATE_LEN = 10    # 'YYYY-MM-DD' prefix of a bar timestamp


def _f_num(value):
    """Float or None. Anything that is not a number reads as 'no data'."""
    try:
        if value is None or isinstance(value, bool):
            return None
        return float(value)
    except (TypeError, ValueError):
        return None


def _f_positive(value):
    """Number that is usable as a denominator or a level (None otherwise)."""
    number = _f_num(value)
    if number is None or number <= _F_ZERO:
        return None
    return number


def _f_bars(ctx):
    """Daily bars from the context: [{time,open,high,low,close,volume,amount},...]."""
    bars = ctx.get("bars")
    return bars if isinstance(bars, (list, tuple)) else []


def _f_column(bars, field):
    """One numeric column of the bar list, in chronological order."""
    out = []
    for bar in bars or []:
        if not isinstance(bar, dict):
            continue
        value = _f_num(bar.get(field))
        if value is not None:
            out.append(value)
    return out


def _f_field_of(bar, field):
    """One numeric field of one bar row (None when absent or unusable)."""
    if not isinstance(bar, dict):
        return None
    return _f_num(bar.get(field))


def _f_tail(seq, window):
    """Last <window> items, clamped to what is there (Go avgVol does the same)."""
    size = int(window)
    if size <= _F_ZERO or not seq:
        return []
    take = size if size < len(seq) else len(seq)
    return seq[len(seq) - take:]


def _f_tail_excl_last(seq, window):
    """The <window> bars immediately BEFORE the newest one.

    buildShortData measures the 20-day low, the 20-day gain and the 3-day range
    against a window that ends at the bar before today -- "today closes below
    it" is the breakdown, so today must not be part of the level it breaks.
    Go indices are [n-1-window, n-1); a short history clamps the start to 0
    exactly as the Go `if hiStart < 0 { hiStart = 0 }` guards do.
    """
    size = int(window)
    stop = len(seq) - _F_ONE
    if size <= _F_ZERO or stop <= _F_ZERO:
        return []
    start = stop - size
    if start < _F_ZERO:
        start = _F_FIRST
    return seq[start:stop]


def _f_mean(seq):
    """Arithmetic mean, None for an empty series."""
    return sum(seq) / float(len(seq)) if seq else None


def _f_max_of(seq):
    """Maximum, None for an empty series."""
    return max(seq) if seq else None


def _f_min_of(seq):
    """Minimum, None for an empty series."""
    return min(seq) if seq else None


def _f_last_of(seq):
    """Newest item, None for an empty series."""
    return seq[_F_LAST] if seq else None


def _f_ratio(numerator, denominator):
    """Numerator over denominator, None when either is missing or 0."""
    top = _f_num(numerator)
    bottom = _f_positive(denominator)
    if top is None or bottom is None:
        return None
    return top / bottom


def _f_pct_change(now, before):
    """Percent change between two levels (Go stores ChangePct as a percent)."""
    delta = _f_ratio(now, before)
    if delta is None:
        return None
    return (delta - _F_ONE) * _F_PCT


def _f_moving_average(closes, window):
    """Simple mean of the last <window> closes (Go ma() tail, warm-up clamped)."""
    return _f_mean(_f_tail(closes, window))


def _f_avg_volume(volumes, window):
    """Mean volume over the last <window> bars INCLUDING the newest one.

    Go avgVol(kl, n) takes the tail n bars, which contains the synthetic live
    bar, and TodayVolVs5d divides that same bar's volume by it. Ported as-is.
    """
    return _f_mean(_f_tail(volumes, window))


def _f_gain_over(closes, window):
    """Rise from the OLDEST close of the window (excl. today) to today's close.

    Go: Gain20 = (last.Close - kl[n-21].Close)/kl[n-21].Close and Last3RangePct
    = the same shape against kl[n-4].Close -- the base is the FIRST bar of the
    window, not the one before today. Getting this wrong silently shifts every
    gain/stall reading by a day.
    """
    span = _f_tail_excl_last(closes, window)
    base = span[_F_FIRST] if span else None
    return _f_pct_change(_f_last_of(closes), base)


def _f_upper_shadow(high, low, close):
    """Upper-shadow ratio of the newest bar, 0..1 (Go: (high-close)/(high-low))."""
    high = _f_num(high)
    low = _f_num(low)
    close = _f_num(close)
    if high is None or low is None or close is None:
        return None
    width = high - low
    if width <= _F_ZERO:
        return _F_ZERO
    return (high - close) / width


def _f_consec_down_days(closes):
    """How many newest bars closed below their predecessor (Go counts from tail)."""
    count = _F_ZERO
    index = len(closes) - _F_ONE
    while index > _F_ZERO:
        if closes[index] is None or closes[index - _F_ONE] is None:
            break
        if closes[index] < closes[index - _F_ONE]:
            count += _F_ONE
            index -= _F_ONE
        else:
            break
    return count


def _f_fresh_surge(closes, window, step_pct):
    """Any of the last <window> bars still gained >= <step_pct> (Go freshSurge).

    Go guards with len(kl) < 4 -> False; here a short history returns None so
    the caller can decline instead of asserting "no surge" from no data.
    """
    if len(closes) <= int(window):
        return None
    span = int(window) + _F_ONE
    tail = closes[len(closes) - span:]
    for offset in range(_F_ONE, len(tail)):
        prev = tail[offset - _F_ONE]
        delta = _f_pct_change(tail[offset], prev)
        if delta is not None and (delta / _F_PCT) >= _f_num(step_pct):
            return True
    return False


def _f_day_of(value):
    """Date prefix ('YYYY-MM-DD') of a bar timestamp, as a comparable string."""
    text = str(value or "")
    if len(text) < _F_DATE_LEN:
        return text
    return text[:_F_DATE_LEN]


def _f_event_bar_index(bars, event_day):
    """Index of the first bar whose date is >= the event date (Go: idx loop)."""
    target = _f_day_of(event_day)
    if not target:
        return None
    for index, bar in enumerate(bars or []):
        if not isinstance(bar, dict):
            continue
        if _f_day_of(bar.get("time")) >= target:
            return index
    return None


def _f_resolve_side(params, side_hint):
    """Side to emit. These four are sell-only rules; a foreign hint is refused."""
    expected = params["rule_side"]
    if side_hint and side_hint != expected:
        return None
    return expected


def _f_sell_qty(params, ctx):
    """Quantity to offer: the held position, and nothing else.

    Go's GenerateSignal always says ActionSell and ScanShort turns a non-held
    name into 'watch'. In the sandbox a watch has no action, so a name we do
    not hold (or whose holding we cannot see) emits no signal at all -- the
    quantity a sell needs must be provable, never assumed.
    """
    held = _f_num(ctx.get("position_qty"))
    if held is None or held <= _F_ZERO:
        return None
    return held


def _f_signal_price(params, ctx):
    """Price to offer at: the live last price, falling back to the newest close."""
    price = _f_positive(ctx.get("last_price"))
    if price is not None:
        return price
    return _f_positive(_f_last_of(_f_column(_f_bars(ctx), "close")))


def _f_derived(params, ctx):
    """Shared indicator bundle rebuilt from the context (None = not computable).

    Mirrors internal/combat_agent/adapter.go buildShortData(): the newest bar is
    today (the engine attaches the live bar to the daily tail), so "close"
    readings come from it while ChangePct/price come from the tick when the
    carrier supplied one.
    """
    bars = _f_bars(ctx)
    closes = _f_column(bars, "close")
    volumes = _f_column(bars, "volume")
    last_bar = bars[_F_LAST] if bars else {}
    last_close = _f_last_of(closes)
    prev_close = _f_positive(ctx.get("prev_close"))
    if prev_close is None and len(closes) > _F_ONE:
        # Go: sd.PrevClose = kl[n-2].Close (adapter.go:309)
        prev_close = _f_positive(closes[_F_LAST - _F_ONE])
    price = _f_positive(ctx.get("last_price"))
    if price is None:
        price = last_close
    last_high = _f_field_of(last_bar, "high")
    last_low = _f_field_of(last_bar, "low")
    return {
        "bar_count": len(closes),
        "last_close": last_close,
        "last_high": last_high,
        "last_low": last_low,
        "last_volume": _f_last_of(volumes),
        "price": price,
        "prev_close": prev_close,
        "change_pct": _f_pct_change(price, prev_close),
        "ma5": _f_moving_average(closes, params["ma5_window"]),
        "ma10": _f_moving_average(closes, params["ma10_window"]),
        "ma20": _f_moving_average(closes, params["ma20_window"]),
        "vol5": _f_avg_volume(volumes, params["vol5_window"]),
        "vol20": _f_avg_volume(volumes, params["vol20_window"]),
        "high60": _f_max_of(_f_tail(closes, params["hc_high_window"])),
        "low20_excl_today": _f_min_of(_f_tail_excl_last(closes, params["bd_low_window"])),
        "gain20": _f_gain_over(closes, params["hc_gain_window"]),
        "last3_change": _f_gain_over(closes, params["hc_last3_window"]),
        "upper_shadow": _f_upper_shadow(last_high, last_low, last_close),
        "consec_down_days": _f_consec_down_days(closes),
        "closes": closes,
    }


# ==== RULE LAYER BEGIN ====
def rule_break_down(params, ctx, side_hint=""):
    """Volume breakdown (Go internal/strategies/break_down/break_down.go).

    Three factors, weighted 0..100: breakdown depth (30), volume confirmation
    (40), intraday weakness (30); a shallow break while MA5 still crosses above
    MA10 is discounted as a possible shakeout. Pass gate: total >= threshold.
    """
    if not ctx.get("trading_day"):
        return None
    side = _f_resolve_side(params, side_hint)
    if side is None:
        return None
    qty = _f_sell_qty(params, ctx)
    if qty is None:
        return None
    price = _f_signal_price(params, ctx)
    if price is None:
        return None

    d = _f_derived(params, ctx)
    if d["bar_count"] < int(params["bd_min_bars"]):
        return None
    last_close = d["last_close"]
    if last_close is None:
        return None

    # Break flags and depth are the existence precondition of this tactic
    # (Go: no break at all -> total 0, level no_break).
    ma20 = d["ma20"]
    low20 = d["low20_excl_today"]
    break_ma20 = ma20 is not None and ma20 > _F_ZERO and last_close < ma20
    break_low20 = low20 is not None and low20 > _F_ZERO and last_close < low20
    if not break_ma20 and not break_low20:
        return None

    depth = _F_ZERO
    if break_ma20:
        depth = (ma20 - last_close) / ma20 * _F_PCT
    if break_low20:
        low_depth = (low20 - last_close) / low20 * _F_PCT
        if low_depth > depth:
            depth = low_depth

    # Factor one: depth. Double break is full marks; a single break scores
    # linearly on the depth and is capped strictly below the double-break mark.
    if break_ma20 and break_low20:
        depth_score = _f_num(params["bd_depth_max_score"])
    else:
        if depth <= _F_ZERO:
            depth_score = _F_ZERO
        else:
            depth_score = depth / _f_num(params["bd_depth_full_pct"]) * _f_num(params["bd_depth_max_score"])
            single_cap = _f_num(params["bd_depth_single_cap"])
            if depth_score > single_cap:
                depth_score = single_cap

    # Factor two: volume confirmation. Full marks at the heavy ratio, linear
    # above the half-mark ratio, and HALF the credit below it -- a break on
    # thin volume is a shakeout suspect, not a trend death.
    vol_ratio = _f_ratio(d["last_volume"], d["vol5"])
    if vol_ratio is None:
        return None
    volume_max = _f_num(params["bd_volume_max_score"])
    full_ratio = _f_num(params["bd_volume_full_ratio"])
    if vol_ratio >= full_ratio:
        volume_score = volume_max
    else:
        volume_score = vol_ratio / full_ratio * volume_max
        if vol_ratio < _f_num(params["bd_volume_half_ratio"]):
            volume_score = volume_score * _f_num(params["bd_volume_shrink_factor"])

    # Factor three: intraday weakness, in percent units like the Go field.
    change = d["change_pct"]
    if change is None:
        return None
    intraday_max = _f_num(params["bd_intraday_max_score"])
    if change <= _f_num(params["bd_intraday_severe_pct"]):
        intraday_score = intraday_max
    elif change <= _f_num(params["bd_intraday_mid_pct"]):
        intraday_score = (_f_num(params["bd_intraday_mid_base_score"]) +
                          (-(change - _f_num(params["bd_intraday_mid_pct"]))) /
                          _f_num(params["bd_intraday_severe_span_pct"]) *
                          _f_num(params["bd_intraday_severe_span_score"]))
    elif change <= _F_ZERO:
        intraday_score = -change / _f_num(params["bd_intraday_flat_span_pct"]) * _f_num(params["bd_intraday_flat_span_score"])
    else:
        intraday_score = _F_ZERO
    if d["upper_shadow"] is not None and d["upper_shadow"] >= _f_num(params["bd_shadow_add_ratio"]):
        intraday_score += _f_num(params["bd_shadow_add_score"])
    if intraday_score > intraday_max:
        intraday_score = intraday_max

    total = depth_score + volume_score + intraday_score

    # Shakeout protection: the bullish moving-average stack is still alive
    # (MA5 > MA10) and the break is shallow -> the whole score takes the
    # discount factor.
    if (d["ma5"] is not None and d["ma10"] is not None and d["ma5"] > d["ma10"] and
            depth < _f_num(params["bd_shakeout_depth_pct"])):
        total = total * _f_num(params["bd_shakeout_discount"])

    if total < _f_num(params["bd_score_threshold"]):
        return None
    return _f_emit_signal(params, ctx, params["break_down_kind"], side, None, price, qty=qty)


def rule_high_churn(params, ctx, side_hint=""):
    """High-level churn (Go internal/strategies/high_churn/high_churn.go).

    Heavy volume with a stalled price near stage highs = distribution.
    Position height (30) + volume-price divergence (40) + candle weakness (30).
    """
    if not ctx.get("trading_day"):
        return None
    side = _f_resolve_side(params, side_hint)
    if side is None:
        return None
    qty = _f_sell_qty(params, ctx)
    if qty is None:
        return None
    price = _f_signal_price(params, ctx)
    if price is None:
        return None

    d = _f_derived(params, ctx)
    if d["bar_count"] < int(params["hc_min_bars"]):
        return None

    # Position height: max of "price vs 60-day high" and "20-day gain".
    pos_high = _f_ratio(price, d["high60"])
    if pos_high is None:
        return None
    position_score = _F_ZERO
    pos_start = _f_num(params["hc_pos_start"])
    if pos_high >= pos_start:
        position_score = (_f_num(params["hc_pos_base_score"]) +
                          (pos_high - pos_start) / _f_num(params["hc_pos_span"]) *
                          _f_num(params["hc_pos_step_score"]))
    gain20 = d["gain20"]
    if gain20 is not None:
        gain_ratio = gain20 / _F_PCT
        gain_start = _f_num(params["hc_gain_start"])
        if gain_ratio >= gain_start:
            gain_score = (_f_num(params["hc_gain_base_score"]) +
                          (gain_ratio - gain_start) / _f_num(params["hc_gain_span"]) *
                          _f_num(params["hc_gain_step_score"]))
            position_cap = _f_num(params["hc_position_max_score"])
            if gain_score > position_cap:
                gain_score = position_cap
            if gain_score > position_score:
                position_score = gain_score
    position_cap = _f_num(params["hc_position_max_score"])
    if position_score > position_cap:
        position_score = position_cap

    # Volume-price divergence: heavy 5d/20d volume AND a stalled 3-day range.
    vol5 = d["vol5"]
    vol20 = d["vol20"]
    if vol5 is None or vol20 is None or vol20 <= _F_ZERO:
        return None
    last3 = d["last3_change"]
    if last3 is None:
        return None
    heavy = (vol5 / vol20) >= _f_num(params["hc_div_heavy_ratio"])
    stalled = last3 < _f_num(params["hc_div_stall_pct"])
    if heavy and stalled:
        divergence_score = _f_num(params["hc_div_full_score"])
    elif heavy or stalled:
        divergence_score = _f_num(params["hc_div_half_score"])
    else:
        divergence_score = _F_ZERO

    # Candle weakness: long shadow, closed under MA5, consecutive down bars.
    weakness_score = _F_ZERO
    if d["upper_shadow"] is not None and d["upper_shadow"] >= _f_num(params["hc_shadow_ratio"]):
        weakness_score += _f_num(params["hc_shadow_score"])
    ma5 = d["ma5"]
    if ma5 is not None and ma5 > _F_ZERO and d["last_close"] is not None and d["last_close"] < ma5:
        weakness_score += _f_num(params["hc_below_ma5_score"])
    if d["consec_down_days"] >= int(params["hc_consec_down_days"]):
        weakness_score += _f_num(params["hc_consec_down_score"])
    weakness_cap = _f_num(params["hc_weakness_max_score"])
    if weakness_score > weakness_cap:
        weakness_score = weakness_cap

    total = position_score + divergence_score + weakness_score
    if total < _f_num(params["hc_score_threshold"]):
        return None
    return _f_emit_signal(params, ctx, params["high_churn_kind"], side, None, price, qty=qty)


def rule_leader_decay(params, ctx, side_hint=""):
    """Leader board-break (Go internal/strategies/leader_decay/leader_decay.go).

    A consecutive limit-up leader that fails to re-seal, with weak price action
    while sentiment cools, marks the end of the run. Board height (30) +
    break-day weakness (40) + sentiment retreat (30). Strictest pass gate of
    the four because a break can be reversed the same day.

    Sandbox data contract, decline-side first (all of these can only ADD a
    signal, so an unknown refuses the trade instead of assuming a value):
      * ctx["is_leader"]      -- the leader verdict (index weight / sector rank)
                                is not computable in the sandbox: None -> decline;
      * ctx["ladder_height"]  -- consecutive boards before today (Go derives it
                                from a board-aware limit pct the sandbox cannot
                                resolve without the security name): None -> decline;
      * ctx["afternoon_reseal"] -- minute-bar re-seal after the protected hour;
                                the Go code suppresses the signal on it, so
                                "unknown" must not silently become "no reseal";
      * ctx["up_stop"]        -- no board price means touched/sealed cannot be
                                judged at all.
    Zero-point side, same as the Go adapter documents ("empty = 0"):
      * ctx["sector_limit_up_drop_pct"] and ctx["emotion_phase"] add nothing
        when absent. Absent can only LOWER the score, never raise one.
    """
    if not ctx.get("trading_day"):
        return None
    side = _f_resolve_side(params, side_hint)
    if side is None:
        return None
    qty = _f_sell_qty(params, ctx)
    if qty is None:
        return None
    price = _f_signal_price(params, ctx)
    if price is None:
        return None

    # Leadership comes from the baked sector/RPS reading. The terminal has no
    # get_index_weight, so None here means "unknown" -- and an unknown leader must
    # decline, never be folded into False (that would turn a data gap into a sell).
    leader = ctx.get("is_leader")
    if leader is None:
        return None
    if not leader:
        return None
    boards = ctx.get("ladder_height")
    if boards is None:
        return None
    boards = int(boards)
    if boards < int(params["ld_min_boards"]):
        return None
    reseal = ctx.get("afternoon_reseal")
    if reseal is None:
        return None
    if reseal:
        return None

    d = _f_derived(params, ctx)
    if d["bar_count"] < int(params["ld_min_bars"]):
        return None
    last_close = d["last_close"]
    if last_close is None:
        return None
    board_price = _f_positive(ctx.get("up_stop"))
    if board_price is None:
        return None

    # Go early-outs: still sealed today -> the run is not over, no signal.
    sealed = board_price * _f_num(params["ld_seal_tolerance"])
    if last_close >= sealed:
        return None
    touched = d["last_high"] is not None and d["last_high"] >= board_price * _f_num(params["ld_touch_tolerance"])

    # Factor one: how tall the ladder was. From the board floor it climbs to
    # full marks at the board ceiling.
    board_max = _f_num(params["ld_board_max_score"])
    if boards >= int(params["ld_board_full_boards"]):
        board_score = board_max
    else:
        board_score = (_f_num(params["ld_board_base_score"]) +
                       (float(boards) - _f_num(params["ld_board_base_boards"])) /
                       _f_num(params["ld_board_span_boards"]) *
                       _f_num(params["ld_board_span_score"]))

    # Factor two: the break day itself -- a big loss, a touched-then-failed
    # board, or a monster-volume split of opinion. Capped on top.
    weakness_score = _F_ZERO
    if d["prev_close"] is not None and price <= d["prev_close"] * _f_num(params["ld_big_loss_ratio"]):
        weakness_score += _f_num(params["ld_big_loss_score"])
    if touched:
        weakness_score += _f_num(params["ld_touched_score"])
    vol_ratio = _f_ratio(d["last_volume"], d["vol5"])
    if vol_ratio is None:
        return None
    if vol_ratio >= _f_num(params["ld_huge_volume_ratio"]):
        weakness_score += _f_num(params["ld_huge_volume_score"])
    weakness_cap = _f_num(params["ld_weakness_max_score"])
    if weakness_score > weakness_cap:
        weakness_score = weakness_cap

    # Factor three: sentiment retreat -- the sector's limit-up count collapsing,
    # and the market phase still in retreat. Keyword list comes from params (a
    # Chinese literal in this file would be mangled by the GBK terminal).
    retreat_score = _F_ZERO
    sector_drop = _f_num(ctx.get("sector_limit_up_drop_pct"))
    if sector_drop is not None and sector_drop >= _f_num(params["ld_sector_drop_ratio"]):
        retreat_score += _f_num(params["ld_sector_drop_score"])
    phase = ctx.get("emotion_phase")
    if phase:
        text = str(phase)
        for keyword in (params["ld_retreat_phase_keys"] or []):
            if keyword and keyword in text:
                retreat_score += _f_num(params["ld_phase_score"])
                break
    retreat_cap = _f_num(params["ld_retreat_max_score"])
    if retreat_score > retreat_cap:
        retreat_score = retreat_cap

    total = board_score + weakness_score + retreat_score
    if total < _f_num(params["ld_score_threshold"]):
        return None
    return _f_emit_signal(params, ctx, params["leader_decay_kind"], side, None, price, qty=qty)


def rule_good_news_fade(params, ctx, side_hint=""):
    """Good-news fully priced (Go internal/strategies/good_news_fade/good_news_fade.go).

    Buy the rumour, sell the news: an event drove >= a floor gain, the price has
    stalled near the highs on heavy volume. Event freshness (25) + premium
    realised (35) + weakness near highs (40).

    The event is the one input this sandbox cannot derive: it must arrive as
    ctx["news_event"] = {"day": 'YYYY-MM-DD', "score": float, "positive": bool,
    "propagation": bool}. Any of those missing -> decline, never a guess. The
    propagation flag in particular carries a DISCOUNT, so assuming it False
    would manufacture a stronger signal than the evidence supports.
    """
    if not ctx.get("trading_day"):
        return None
    side = _f_resolve_side(params, side_hint)
    if side is None:
        return None
    qty = _f_sell_qty(params, ctx)
    if qty is None:
        return None
    price = _f_signal_price(params, ctx)
    if price is None:
        return None

    d = _f_derived(params, ctx)
    if d["bar_count"] < int(params["gf_min_bars"]):
        return None

    # A good-news fade needs the event itself (kind/time/propagation). Without it we
    # cannot tell "no news" from "news we could not read", so the rule abstains.
    event = ctx.get("news_event")
    if not isinstance(event, dict):
        return None
    if event.get("positive") is None or event.get("propagation") is None:
        return None
    if not event.get("positive"):
        return None
    event_score = _f_num(event.get("score"))
    if event_score is None:
        return None
    index = _f_event_bar_index(_f_bars(ctx), event.get("day"))
    if index is None:
        return None
    age = d["bar_count"] - _F_ONE - index
    if age > int(params["gf_event_window_days"]):
        return None
    rows = _f_bars(ctx)
    event_bar = rows[index] if index < len(rows) else None
    event_day_close = _f_positive(_f_field_of(event_bar, "close"))
    if event_day_close is None:
        return None

    # Mis-kill protection: one of the last bars still gained >= the surge step
    # means the main leg is not over -- Go emits nothing. Unknown history ->
    # decline as well.
    surge = _f_fresh_surge(d["closes"], params["gf_surge_window"], params["gf_surge_step_pct"])
    if surge is None or surge:
        return None

    # Factor one: how fresh the event is (full marks at the fresh age, floor at
    # the stale age, linear between).
    fresh_age = int(params["gf_event_fresh_age"])
    stale_age = int(params["gf_event_stale_age"])
    if age <= fresh_age:
        event_factor = _f_num(params["gf_event_max_score"])
    elif age >= stale_age:
        event_factor = _f_num(params["gf_event_min_score"])
    else:
        event_factor = (_f_num(params["gf_event_max_score"]) -
                        float(age - fresh_age) / _f_num(params["gf_event_age_span"]) *
                        _f_num(params["gf_event_age_drop"]))

    # Factor two: how much of the event premium is already in the price.
    gain = _f_pct_change(price, event_day_close)
    if gain is None:
        return None
    gain_ratio = gain / _F_PCT
    gain_start = _f_num(params["gf_gain_start"])
    gain_max = _f_num(params["gf_gain_max_score"])
    if gain_ratio < gain_start:
        gain_score = _F_ZERO
    elif gain_ratio >= _f_num(params["gf_gain_full"]):
        gain_score = gain_max
    else:
        gain_score = (gain_ratio - gain_start) / _f_num(params["gf_gain_span"]) * gain_max

    # Factor three: stalling and softening near the highs.
    last3 = d["last3_change"]
    if last3 is None:
        return None
    weakness_score = _F_ZERO
    if last3 < _f_num(params["gf_stall_pct"]):
        weakness_score += _f_num(params["gf_stall_score"])
        vol5 = d["vol5"]
        vol20 = d["vol20"]
        if vol5 is not None and vol20 is not None and vol20 > _F_ZERO:
            if (vol5 / vol20) >= _f_num(params["gf_heavy_ratio"]):
                weakness_score += _f_num(params["gf_heavy_score"])
    ma5 = d["ma5"]
    if ma5 is not None and ma5 > _F_ZERO and d["last_close"] is not None and d["last_close"] < ma5:
        weakness_score += _f_num(params["gf_below_ma5_score"])
    if d["upper_shadow"] is not None and d["upper_shadow"] >= _f_num(params["gf_shadow_ratio"]):
        weakness_score += _f_num(params["gf_shadow_score"])
    weakness_cap = _f_num(params["gf_weakness_max_score"])
    if weakness_score > weakness_cap:
        weakness_score = weakness_cap

    total = event_factor + gain_score + weakness_score
    # A propagated supply-chain event keeps working longer: the whole score is
    # discounted, exactly as Go does.
    if event.get("propagation"):
        total = total * _f_num(params["gf_propagation_discount"])

    if total < _f_num(params["gf_score_threshold"]):
        return None
    return _f_emit_signal(params, ctx, params["good_news_fade_kind"], side, None, price, qty=qty)
# ==== RULE LAYER END ====


def _f_emit_signal(params, ctx, kind, side, amount, price, qty=None):
    """Build the signal dict with the carrier's ONE shared helper.

    In the published artifact the shared rule layer of local_executor.py -- which
    owns make_signal and the short-code contract -- is inlined above the ported
    modules, so the bare name is already in scope and this routes to it: one
    implementation of the idempotency key on the box that spends money. In the
    repository copy this module deliberately does not define the name
    make_signal itself (the builder refuses two ported modules defining one
    top-level name), so the fallback below stands in for it, and
    tests/test_rules_form.py locks that fallback to the shared helper's exact
    output -- if the two ever drift, that test goes red.
    """
    try:
        builder = make_signal
    except NameError:
        builder = _f_make_signal
    return builder(params, ctx, kind, side, amount, price, qty=qty)


def _f_make_signal(params, ctx, kind, side, amount, price, qty=None):
    """Assemble a signal dict: kind, side, symbol, quantity, price, short code.

    This is the shared builder of qmt_gateway/local_executor.py, copied here
    byte-for-behaviour so the rules can be baked into a carrier that cannot
    import anything. It reads every threshold from params (lot size, the short
    code table, the symbol head length) and holds no numeric literal, so the
    two copies can never disagree about a money rule; the builder keeps one.

    The short code is <kind code><symbol head> and is the idempotency key of
    the whole system: it travels in the broker remark and is rebuilt from the
    broker's own order table after a restart.
    """
    symbol = ctx.get("symbol")
    if qty is None:
        if price is None or not price:
            return None
        lot = params["lot_size"]
        qty = (int(amount / price) // lot) * lot
        if not qty:
            return None
    code = (params["signal_codes"] or {}).get(kind)
    if not code:
        return None
    text = str(symbol or "")
    head = text.split(".")[_F_FIRST][:int(params["symbol_head_len"])]
    short = "%s%s" % (code, head)
    return {
        "kind": kind,
        "side": side,
        "symbol": symbol,
        "qty": int(qty),
        "price": float(price),
        "amount": None if amount is None else float(amount),
        "short_code": short,
    }


# ---------------------------------------------------------------------------
# FORM_PARAM_KEYS -- every params key these rules read.
#
# The build gate takes this list as the contract: each key must be present in
# the baked PARAMS table, and any new key added to a rule must be added here
# and to the parameter file in the same change.
#
# GO SOURCE MAP (file:line -> params key). Go thresholds quoted in the trailing
# comment are the literals found in the source; nothing here was invented.
# ---------------------------------------------------------------------------
FORM_PARAM_KEYS = [
    # --- carrier side / shared ---
    # local_executor.make_signal: lot rounding + short code
    "lot_size",                                # local_executor.py:76 (baked)
    "symbol_head_len",                         # local_executor.py:94 (baked)
    "signal_codes",                            # local_executor.py:91 (baked)
    # side these rules emit; the four Go tactics are ActionSell
    "rule_side",                               # break_down.go:213 / high_churn.go:206 /
                                               # leader_decay.go:204 / good_news_fade.go:238
    "break_down_kind",                         # break_down.go:42 (Name "break_down")
    "high_churn_kind",                         # high_churn.go:41 (Name "high_churn")
    "leader_decay_kind",                       # leader_decay.go:45 (Name "leader_decay")
    "good_news_fade_kind",                     # good_news_fade.go:42 (Name "good_news_fade")
    # indicator windows, from buildShortData (adapter.go:288-380)
    "ma5_window",                              # adapter.go:311 ma(kl, 5)
    "ma10_window",                             # adapter.go:312 ma(kl, 10)
    "ma20_window",                             # adapter.go:313 ma(kl, 20)
    "vol5_window",                             # adapter.go:346 avgVol(kl, 5)
    "vol20_window",                            # adapter.go:347 avgVol(kl, 20)
    "hc_high_window",                          # adapter.go:316 hiStart = n - 60
    "bd_low_window",                           # adapter.go:325 lowStart = n - 21 (20 bars, ex today)
    "hc_gain_window",                          # adapter.go:338 gStart = n - 21
    "hc_last3_window",                         # adapter.go:357 kl[n-4] (3 bars back)

    # --- break_down (volume breakdown) ---
    "bd_min_bars",                             # break_down.go:72 len(sd.KLines) < 25
    "bd_depth_max_score",                      # break_down.go:148,154 full depth marks = 30
    "bd_depth_single_cap",                     # break_down.go:156 single-break cap = 25
    "bd_depth_full_pct",                       # break_down.go:154 depth / 3.0 * 30 -> 3.0 pct
    "bd_volume_max_score",                     # break_down.go:166,169 volume marks = 40
    "bd_volume_full_ratio",                    # break_down.go:165 today vol vs 5d >= 1.5
    "bd_volume_half_ratio",                    # break_down.go:168 r >= 1.0 branch edge
    "bd_volume_shrink_factor",                 # break_down.go:172 shrink break halved * 0.5
    "bd_intraday_max_score",                   # break_down.go:181 intraday marks = 30
    "bd_intraday_severe_pct",                  # break_down.go:180 change <= -5
    "bd_intraday_mid_pct",                     # break_down.go:182 change <= -2
    "bd_intraday_mid_base_score",              # break_down.go:183 base 20
    "bd_intraday_severe_span_pct",             # break_down.go:183 /3 span (-2 .. -5)
    "bd_intraday_severe_span_score",           # break_down.go:183 *10 over that span
    "bd_intraday_flat_span_pct",               # break_down.go:185 /2 (0 .. -2)
    "bd_intraday_flat_span_score",             # break_down.go:185 *10 over that span
    "bd_shadow_add_ratio",                     # break_down.go:187 UpperShadowPct >= 0.5
    "bd_shadow_add_score",                     # break_down.go:188 +5
    "bd_shakeout_depth_pct",                   # break_down.go:89 BreakDepthPct < 1.0
    "bd_shakeout_discount",                    # break_down.go:90 total *= 0.7
    "bd_score_threshold",                      # break_down.go:57 default 60 (rules.strategy.short.break_down_min)

    # --- high_churn (high-level churn) ---
    "hc_min_bars",                             # high_churn.go:71 len(sd.KLines) < 60
    "hc_pos_start",                            # high_churn.go:133 PosHigh >= 0.92
    "hc_pos_span",                             # high_churn.go:134 /0.08
    "hc_pos_base_score",                       # high_churn.go:134 base 15
    "hc_pos_step_score",                       # high_churn.go:134 *15 over the span
    "hc_position_max_score",                   # high_churn.go:140,147 cap 30
    "hc_gain_start",                           # high_churn.go:137 Gain20 >= 0.30
    "hc_gain_span",                            # high_churn.go:138 /0.20 (30% -> 50%)
    "hc_gain_base_score",                      # high_churn.go:138 base 15
    "hc_gain_step_score",                      # high_churn.go:138 *15 over the span
    "hc_div_heavy_ratio",                      # high_churn.go:156 VolRatio5_20 >= 1.5
    "hc_div_stall_pct",                        # high_churn.go:157 Last3RangePct < 2.0
    "hc_div_full_score",                       # high_churn.go:160 both sides -> 40
    "hc_div_half_score",                       # high_churn.go:162 one side -> 20
    "hc_shadow_ratio",                         # high_churn.go:171 UpperShadowPct >= 0.4
    "hc_shadow_score",                         # high_churn.go:172 +12
    "hc_below_ma5_score",                      # high_churn.go:175 BelowMA5 -> +10
    "hc_consec_down_days",                     # high_churn.go:177 ConsecDownDays >= 2
    "hc_consec_down_score",                    # high_churn.go:178 +8
    "hc_weakness_max_score",                   # high_churn.go:181 cap 30
    "hc_score_threshold",                      # high_churn.go:56 default 60 (rules.strategy.short.high_churn_min)

    # --- leader_decay (leader board-break) ---
    "ld_min_bars",                             # leader_decay.go:75 len(sd.KLines) < 10
    "ld_min_boards",                           # leader_decay.go:80 ConsecBoards < 2
    "ld_board_max_score",                      # leader_decay.go:146 board >= 5 -> 30
    "ld_board_full_boards",                    # leader_decay.go:145 b >= 5
    "ld_board_base_score",                     # leader_decay.go:148 base 12
    "ld_board_base_boards",                    # leader_decay.go:148 (b - 2) floor
    "ld_board_span_boards",                    # leader_decay.go:148 /3 boards
    "ld_board_span_score",                     # leader_decay.go:148 *18 points
    "ld_seal_tolerance",                       # adapter.go:388 boardPx*0.995 (SealedToday)
    "ld_touch_tolerance",                      # adapter.go:387 boardPx*0.998 (TouchedBoardToday)
    "ld_big_loss_ratio",                       # leader_decay.go:155 Price <= PrevClose * 0.97
    "ld_big_loss_score",                       # leader_decay.go:156 +20
    "ld_touched_score",                        # leader_decay.go:159 board touched then failed +15
    "ld_huge_volume_ratio",                    # leader_decay.go:161 TodayVolVs5d >= 2.0
    "ld_huge_volume_score",                    # leader_decay.go:162 +10
    "ld_weakness_max_score",                   # leader_decay.go:165 cap 40
    "ld_sector_drop_ratio",                    # leader_decay.go:174 SectorLimitUpDropPct >= 0.5
    "ld_sector_drop_score",                    # leader_decay.go:175 +20
    "ld_phase_score",                          # leader_decay.go:180 phase in retreat +10
    "ld_retreat_phase_keys",                   # leader_decay.go:178 strings.Contains(phase, "retreat-term")
    "ld_retreat_max_score",                    # leader_decay.go:183 cap 30
    "ld_score_threshold",                      # leader_decay.go:60 default 65 (rules.strategy.short.leader_decay_min)

    # --- good_news_fade (good news fully priced) ---
    "gf_min_bars",                             # good_news_fade.go:72 len(sd.KLines) < 20
    "gf_event_window_days",                    # good_news_fade.go:77 / adapter.go:439 age <= 5
    "gf_event_fresh_age",                      # good_news_fade.go:157 age <= 1 -> full marks
    "gf_event_stale_age",                      # good_news_fade.go:159 age >= 5 -> floor marks
    "gf_event_max_score",                      # good_news_fade.go:158,163 25
    "gf_event_min_score",                      # good_news_fade.go:161 5
    "gf_event_age_span",                       # good_news_fade.go:163 (age-1)/4
    "gf_event_age_drop",                       # good_news_fade.go:163 *20 points dropped
    "gf_gain_start",                           # good_news_fade.go:173 g < 0.10 -> 0
    "gf_gain_full",                            # good_news_fade.go:176 g >= 0.25 -> full
    "gf_gain_span",                            # good_news_fade.go:179 /0.15 linear
    "gf_gain_max_score",                       # good_news_fade.go:177,179 35
    "gf_stall_pct",                            # good_news_fade.go:186 Last3RangePct < 1.0
    "gf_stall_score",                          # good_news_fade.go:187 +15
    "gf_heavy_ratio",                          # good_news_fade.go:188 VolRatio5_20 >= 1.5
    "gf_heavy_score",                          # good_news_fade.go:189 +10
    "gf_below_ma5_score",                      # good_news_fade.go:193 BelowMA5 -> +8
    "gf_shadow_ratio",                         # good_news_fade.go:195 UpperShadowPct >= 0.4
    "gf_shadow_score",                         # good_news_fade.go:196 +7
    "gf_weakness_max_score",                   # good_news_fade.go:199 cap 40
    "gf_surge_window",                         # good_news_fade.go:212 for i := n-3; i < n
    "gf_surge_step_pct",                       # good_news_fade.go:214 close/prev-1 >= 0.05
    "gf_propagation_discount",                 # good_news_fade.go:95 total *= 0.7
    "gf_score_threshold",                      # good_news_fade.go:57 default 60 (rules.strategy.short.good_news_fade_min)
]


# ---------------------------------------------------------------------------
# RULES -- the registry the builder reads (never by exec, always by AST).
#
# Keys are the signal kinds; scripts/build_local_executor.py refuses the build
# when a registered kind owns no short code in params.signal_codes, because the
# counter remark carrying that short code is the ONLY idempotency key the
# sandbox has: without one, a restart re-sends the same intent, and two rules
# sharing one code lets the first rule's orders silence the second forever.
# The kinds therefore must match the params["<rule>_kind"] values the rules
# themselves emit -- tests/test_rules_form.py locks that equality in both
# directions so "the knob says four rules, the box registers three" cannot
# happen quietly.
# ---------------------------------------------------------------------------
RULES = {
    "break_down": rule_break_down,
    "high_churn": rule_high_churn,
    "leader_decay": rule_leader_decay,
    "good_news_fade": rule_good_news_fade,
}
