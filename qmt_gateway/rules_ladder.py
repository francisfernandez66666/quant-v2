#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.rules_ladder -- limit-up / ladder pattern rules ported to pure Python.

Why this file exists (broker rule effective 2026-11-05):
  QMT built-in strategies may not read local files, reach any external
  system, or fetch history by download. The only automated execution shape
  left is a strategy whose signals are produced entirely inside QMT. These
  scoring rules are the ported decision core of four Go tactics:

    dragon          <- internal/strategies/dragon/dragon.go       (F1-F4 seal scoring)
    dragon_return   <- internal/strategies/dragon_return/dragon_return.go
                      plus the input derivation of combat_agent/adapter.go:138-202
    double_bump     <- internal/strategies/double_bump/double_bump.go
    leader_decay    <- internal/strategies/leader_decay/leader_decay.go
                      consuming the shared input struct of
                      internal/strategies/shortbase/shortbase.go (Data)

Hard contract honored here (a build gate checks it later):
  * pure ASCII source (loaded into a GBK Windows terminal; anything else
    silently becomes '?' -- the 2026-11-02 lesson);
  * no third-party names, no data-pull, no download call: history arrives
    pre-batched in ctx["bars"] (list of bar dicts; the fields consumed
    here are time, high, low, close, volume, amount) assembled once by
    the carrier at start-up;
  * every threshold is a params[...] read -- NO business numeric literal in
    any rule body (0/1 as index or counter arithmetic only);
  * cross-sectional pool readings (limit_up_count, ladder_height,
    broken_board_ratio, advancers, decliners, leader_codes) are consumed as
    given aggregates; a judgment whose reading is absent declines with a
    comment naming the missing reading;
  * LEADERSHIP CANNOT BE COMPUTED IN THE SANDBOX: get_sector_list /
    get_sector_type / get_index_weight are ABSENT on the built-in surface,
    so the Go sector-RPS rank / sector RPS20 feed (adapter.go:196-200) has
    no equivalent. Rules read ctx["is_leader"] and ctx["sector_rps20"] --
    baked by the decision side or hand-labeled -- and decline (None) when
    the reading is missing. A pool gain is NEVER substituted for leadership.

Signal output shape is the carrier make_signal() dict (same contract as
qmt_gateway/local_executor.py:560-587), so the ladder rules plug into both
carriers without a translation layer. Sizing on the Go side belongs to the
position engine (not part of these four tactic packages); after 11-05 there
is no engine at the box, so the buy amount is a single baked knob
params["ladder_buy_amount"] -- a deliberate decline-by-bake, not an
invented formula.
(English: pure-ASCII, params-driven port of the dragon / dragon-return /
double-bump / leader-decay scoring cores for the post-1105 sandbox.)
"""


# ---------------------------------------------------------------------------
# helpers (pure computation only; no reading, no pull, no clock)
# ---------------------------------------------------------------------------

# Deliberately byte-identical to local_executor.make_signal: the builder dedups
# identical top-level definitions when it inlines both carriers (PORTED_DEDUP),
# so the artifact carries one copy and never lets one module silently overwrite
# another. If the two ever drift, the texts differ and the collision gate fires.
# Same text as local_executor.symbol_head on purpose: the builder dedups byte-identical
# top-level definitions when it inlines both carriers (PORTED_DEDUP), so the artifact keeps
# exactly one and this module still works when imported on its own.
def symbol_head(symbol, length):
    """Leading digits of a symbol, e.g. '600000.SH' -> '600000'."""
    text = str(symbol or "")
    return text.split(".")[0][:length]

def make_signal(params, ctx, kind, side, amount, price, qty=None):
    """Assemble a signal dict: kind, side, symbol, quantity, price, short code.

    The short code is <kind code><symbol head> and is the idempotency key
    of the whole system: it travels in the broker remark and it is rebuilt
    from the broker's own order table after a restart.
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
    short = "%s%s" % (code, symbol_head(symbol, params["symbol_head_len"]))
    return {
        "kind": kind,
        "side": side,
        "symbol": symbol,
        "qty": int(qty),
        "price": float(price),
        "amount": None if amount is None else float(amount),
        "short_code": short,
    }

def _num(value):
    """Coerce a reading to float, or None when it is not a number.

    Bools are refused on purpose: a True slipped in as a numeric reading
    would silently satisfy a >= comparison.
    """
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return float(value)
    if isinstance(value, float):
        return value
    return None


def symbol_head(symbol, length):
    """Leading digits of a symbol, e.g. '600000.SH' -> '600000'."""
    text = str(symbol or "")
    return text.split(".")[0][:length]


def _bar_field(bars, index, field):
    """One numeric field of one bar (None when absent or not numeric)."""
    try:
        bar = bars[index]
    except (IndexError, TypeError):
        return None
    if not isinstance(bar, dict):
        return None
    return _num(bar.get(field))


def _ma_field(bars, window, field):
    """Mean of one field over the last `window` bars.

    Mirrors the Go movingAvg semantics (double_bump.go:321-330): when fewer
    bars than the window are available, fall back to the last bar value
    instead of fabricating a partial average.
    """
    count = len(bars)
    if count < window:
        return _bar_field(bars, count - 1, field)
    total = 0.0
    for i in range(count - window, count):
        value = _bar_field(bars, i, field)
        if value is None:
            return None
        total += value
    return total / float(window)


def _avg_field_window(bars, window, field):
    """Mean of one field over the last `window` bars, 0.0 when unavailable
    (mirrors adapter.go:103-116 avgVol clamping semantics)."""
    count = len(bars)
    if count < 1:
        return 0.0
    if window > count:
        window = count
    if window <= 0:
        return 0.0
    total = 0.0
    for i in range(count - window, count):
        value = _bar_field(bars, i, field)
        if value is None:
            return 0.0
        total += value
    return total / float(window)


# make_signal: NOT redefined here. The shared rule layer (local_executor.py)
# ==== RULE LAYER ====) provides the one implementation; a second copy in this module
# would silently overwrite it once the builder inlines both into one namespace.


# ---------------------------------------------------------------------------
# rule 1: dragon (sector-leader seal quality, F1-F4)
# Go source: internal/strategies/dragon/dragon.go EvaluateReal:140-239,
#            GenerateSignal:288-326 (only full_chain turns into an order).
# ---------------------------------------------------------------------------

def rule_dragon(params, ctx, side_hint=""):
    """Dragon buy when the four-factor seal score reaches full_chain.

    F1 seal quality / F2 sector resonance / F3 premium / F4 five-day RS.
    Declines (None) when a required reading is missing:
      * last_price / prev_close / up_stop: the board-limit reference. In
        Go this was data.LimitUpPct(code, name) (dragon.go:152); inside the
        sandbox the exact UpStopPrice from the instrument detail is
        available, so limit_pct is self-computed (a fidelity gain, not an
        approximation).
      * bars: daily sequence including the synthesized live bar.
    sector_best_change_pct: ABSENT reading (get_sector_list missing). When
    not supplied the rule follows Go's own empty-sector leg (dragon.go:164
    leaves bestSector at 0 -> F2 scores 0, F3 falls to its own-gain half
    leg) -- it is NOT replaced by a pool-gain proxy.
    side_hint is accepted for contract uniformity; the side is fixed buy.
    """
    scale = params["pct_scale"]                      # from dragon.go:192 (x100 percent scale)
    min_bars = params["dragon_min_bars"]             # from dragon.go:142 (len(kLines) < 5 -> nil)
    seal_tol = params["dragon_seal_tolerance_pct"]   # from dragon.go:154 (limitPct - 0.5)
    f1_weight = params["dragon_f1_weight"]           # from dragon.go:156 (F1SealWeight, config.go:5332 = 0.30)
    f1_base_ratio = params["dragon_f1_base_ratio"]   # from dragon.go:156 (* 0.9)
    f1_vol_ratio = params["dragon_f1_vol_ratio"]     # from dragon.go:158 (* 0.1 of the volume/amount term)
    f2_weight = params["dragon_f2_weight"]           # from dragon.go:172 (F2ResonanceWeight, config.go:5333 = 0.25)
    f2_full_pct = params["dragon_f2_full_pct"]       # from dragon.go:171 (bestSector > 3)
    f2_half_pct = params["dragon_f2_half_pct"]       # from dragon.go:174 (bestSector > 1)
    f2_half_ratio = params["dragon_f2_half_ratio"]   # from dragon.go:174 (* 0.5)
    f3_weight = params["dragon_f3_weight"]           # from dragon.go:180 (F3PremiumWeight, config.go:5334 = 0.20)
    f3_premium_pct = params["dragon_f3_premium_pct"] # from dragon.go:180 (bestSector + 2)
    f3_own_gain_pct = params["dragon_f3_own_gain_pct"]  # from dragon.go:182 (ChangePct > 5)
    f3_half_ratio = params["dragon_f3_half_ratio"]   # from dragon.go:183 (* 0.5)
    rs_window = params["dragon_rs_window"]           # from dragon.go:192 (kLines[-5] trend window)
    f4_weight = params["dragon_f4_weight"]           # from dragon.go:195 (F4RsWeight, config.go:5335 = 0.25)
    f4_full_pct = params["dragon_f4_full_pct"]       # from dragon.go:194 (trend5 > 10)
    f4_half_pct = params["dragon_f4_half_pct"]       # from dragon.go:197 (trend5 > 5)
    f4_half_ratio = params["dragon_f4_half_ratio"]   # from dragon.go:197 (* 0.5)
    full_chain_min = params["dragon_full_chain_min"] # from dragon.go:207 (total >= 60 -> full_chain)
    buy_amount = params["ladder_buy_amount"]         # sizing lives in the Go position engine, not in this package: baked single amount stands in (decline-by-bake)

    price = _num(ctx.get("last_price"))              # Go guard dragon.go:142 (si.Price <= 0)
    prev_close = _num(ctx.get("prev_close"))         # missing -> no change pct and no limit pct: decline
    up_stop = _num(ctx.get("up_stop"))               # missing -> seal reference unknown: decline
    bars = ctx.get("bars")
    if price is None or prev_close is None or prev_close <= 0 or up_stop is None:
        return None
    if not isinstance(bars, list) or len(bars) < min_bars:
        return None                                  # from dragon.go:142 (fewer than 5 bars -> nil)

    change_pct = (price - prev_close) / prev_close * scale

    # F1 seal quality: dragon.go:152-160. Go used the board table
    # LimitUpPct; the sandbox computes the exact board limit from UpStopPrice.
    limit_pct = (up_stop - prev_close) / prev_close * scale
    f1 = 0.0
    if change_pct > limit_pct - seal_tol:            # from dragon.go:154
        f1 = f1_weight * scale * f1_base_ratio       # from dragon.go:156
        # Go reads si.Volume / si.Amount (dragon.go:158); the Go adapter
        # fills both from the last daily bar (adapter.go:54-60), so the
        # last bar is the faithful sandbox source, not an approximation.
        bar_volume = _bar_field(bars, len(bars) - 1, "volume")
        bar_amount = _bar_field(bars, len(bars) - 1, "amount")
        if bar_volume is not None and bar_amount is not None and bar_amount > 0:
            f1 += (bar_volume / bar_amount) * f1_vol_ratio * f1_weight * scale  # from dragon.go:158

    # F2 sector resonance: dragon.go:162-175. sector list ABSENT in the
    # sandbox; missing reading = Go's empty-sectors leg (bestSector stays 0).
    best_sector = _num(ctx.get("sector_best_change_pct"))
    f2 = 0.0
    if best_sector is not None:
        if best_sector > f2_full_pct:                # from dragon.go:171
            f2 = f2_weight * scale
        elif best_sector > f2_half_pct:              # from dragon.go:174
            f2 = f2_weight * scale * f2_half_ratio

    # F3 premium: dragon.go:177-184.
    f3 = 0.0
    if best_sector is not None and best_sector > 0 and change_pct > best_sector + f3_premium_pct:
        f3 = f3_weight * scale                       # from dragon.go:180
    elif change_pct > f3_own_gain_pct:               # from dragon.go:182
        f3 = f3_weight * scale * f3_half_ratio

    # F4 five-day RS: dragon.go:186-199 (kLines[-1] vs kLines[-5]).
    f4 = 0.0
    if len(bars) >= rs_window:                         # from dragon.go:191 (len(kLines) >= 5 to score the trend leg)
        trend_base = _bar_field(bars, len(bars) - rs_window, "close")
        trend_last = _bar_field(bars, len(bars) - 1, "close")
        if trend_base is not None and trend_last is not None and trend_base > 0:
            trend5 = (trend_last - trend_base) / trend_base * scale   # from dragon.go:192
            if trend5 > f4_full_pct:                     # from dragon.go:194
                f4 = f4_weight * scale
            elif trend5 > f4_half_pct:                   # from dragon.go:197
                f4 = f4_weight * scale * f4_half_ratio

    total = f1 + f2 + f3 + f4
    if total > scale:                                # from dragon.go:202 (cap at 100)
        total = scale
    if total < full_chain_min:                       # from dragon.go:207; brief/watch legs are ActionWatch in Go (GenerateSignal:293-306) -> no order
        return None
    return make_signal(params, ctx, "dragon_breakout", "buy", buy_amount, price)


# ---------------------------------------------------------------------------
# rule 2: dragon_return (second-leg of a former leader)
# Go source: internal/strategies/dragon_return/dragon_return.go score:255-281,
#            dragonIdentity:292-303, pullbackHealth:334-378, duckHead:405-459,
#            confirmSignal:476-497, GenerateSignal:509-546;
#            input derivation: internal/combat_agent/adapter.go:138-202.
# ---------------------------------------------------------------------------

def rule_dragon_return(params, ctx, side_hint=""):
    """Dragon-return buy when the four-factor total clears the first tier.

    Leadership feed is the deliberate decline-by-design: Go derives
    IsSectorTop2 / SectorRPS20 from the sector RPS rank
    (adapter.go:196-200); the sandbox has NO sector list and NO index
    weight, so the rule reads ctx["is_leader"] and ctx["sector_rps20"].
    Either reading absent -> decline with None (never a pool-gain proxy).
    Derived shape inputs (first_rise_pct / pullback_pct / pullback_days /
    shrink_ratio / ma5 / ma10 / ma20) may arrive pre-aggregated in ctx;
    otherwise they are computed from ctx["bars"] with adapter.go:151-192.
    """
    min_bars = params["dr_min_bars"]                 # from adapter.go:147 (fewer than 30 bars -> zero struct)
    ma_short = params["dr_ma_short"]                 # from adapter.go:153 (MA5)
    ma_mid = params["dr_ma_mid"]                     # from adapter.go:154 (MA10)
    ma_long = params["dr_ma_long"]                   # from adapter.go:155 (MA20)
    rally_window = params["dr_rally_window"]         # from adapter.go:159 (last 40 bars)
    rise_min = params["dr_first_rise_min"]           # from dragon_return.go:296 (0.35)
    rise_max = params["dr_first_rise_max"]           # from dragon_return.go:296 (0.70)
    rps_min = params["dr_sector_rps_min"]            # from dragon_return.go:299 (SectorRPS20 < 75)
    score_dragon = params["dr_score_dragon"]         # from dragon_return.go:302 (25)
    pb_best_lo = params["dr_pb_best_lo"]             # from dragon_return.go:337 (0.15)
    pb_best_hi = params["dr_pb_best_hi"]             # from dragon_return.go:337 (0.20)
    pb_good_hi = params["dr_pb_good_hi"]             # from dragon_return.go:339 (0.25)
    pb_shallow_lo = params["dr_pb_shallow_lo"]       # from dragon_return.go:341 (0.10)
    pb_deep_hi = params["dr_pb_deep_hi"]             # from dragon_return.go:343 (0.35)
    pb_score_best = params["dr_pb_score_best"]       # from dragon_return.go:337 (8)
    pb_score_good = params["dr_pb_score_good"]       # from dragon_return.go:339 (6)
    pb_score_shallow = params["dr_pb_score_shallow"] # from dragon_return.go:341 (5)
    pb_score_deep = params["dr_pb_score_deep"]       # from dragon_return.go:343 (4)
    pb_score_other = params["dr_pb_score_other"]     # from dragon_return.go:345 (1)
    days_good_lo = params["dr_days_good_lo"]         # from dragon_return.go:352 (5)
    days_good_hi = params["dr_days_good_hi"]         # from dragon_return.go:352 (8)
    days_short_lo = params["dr_days_short_lo"]       # from dragon_return.go:354 (3)
    days_long_hi = params["dr_days_long_hi"]         # from dragon_return.go:356 (12)
    days_score_good = params["dr_days_score_good"]   # from dragon_return.go:353 (5)
    days_score_near = params["dr_days_score_near"]   # from dragon_return.go:355,357 (3)
    days_score_other = params["dr_days_score_other"] # from dragon_return.go:359 (1)
    vr_good = params["dr_vr_good"]                   # from dragon_return.go:365 (<0.3)
    vr_ok = params["dr_vr_ok"]                       # from dragon_return.go:367 (<0.4)
    vr_mid = params["dr_vr_mid"]                     # from dragon_return.go:368 (<0.5)
    vr_bad = params["dr_vr_bad"]                     # from dragon_return.go:369 (<0.6)
    vr_score_good = params["dr_vr_score_good"]       # from dragon_return.go:366 (8)
    vr_score_ok = params["dr_vr_score_ok"]           # from dragon_return.go:367 (7)
    vr_score_mid = params["dr_vr_score_mid"]         # from dragon_return.go:368 (5)
    vr_score_bad = params["dr_vr_score_bad"]         # from dragon_return.go:369 (3)
    vr_score_other = params["dr_vr_score_other"]     # from dragon_return.go:371 (1)
    shrink_max = params["dr_shrink_max"]             # from dragon_return.go:409 (VolumeRatio < 0.5)
    duck_top_full = params["dr_duck_top_full"]       # from dragon_return.go:410 (5)
    duck_top_half = params["dr_duck_top_half"]       # from dragon_return.go:412 (2)
    neck_support = params["dr_neck_support_ratio"]   # from dragon_return.go:416 (MA20 * 0.98)
    neck_full = params["dr_duck_neck_full"]          # from dragon_return.go:419 (8)
    neck_low = params["dr_duck_neck_low"]            # from dragon_return.go:421 (5)
    slope_prev = params["dr_slope_prev_ratio"]       # from dragon_return.go:417 (* 0.99 term)
    slope_delta = params["dr_slope_delta_ratio"]     # from dragon_return.go:417 (* 0.01 term)
    slope_min = params["dr_slope_min"]               # from dragon_return.go:424 (slope > 0.5)
    slope_bonus = params["dr_duck_slope_bonus"]      # from dragon_return.go:425 (2)
    macd_near = params["dr_macd_green_near"]         # from dragon_return.go:434 (green bar > -1)
    beak_vr_lo = params["dr_beak_vr_lo"]             # from dragon_return.go:439 (volume ratio > 0.5)
    beak_vr_hi = params["dr_beak_vr_hi"]             # from dragon_return.go:439 (volume ratio < 1.2)
    beak_three = params["dr_beak_score_three"]       # from dragon_return.go:444 (7)
    beak_two = params["dr_beak_score_two"]           # from dragon_return.go:446 (4)
    beak_one = params["dr_beak_score_one"]           # from dragon_return.go:448 (2)
    nostril_full = params["dr_nostril_full"]         # from dragon_return.go:453 (5)
    nostril_half = params["dr_nostril_half"]         # from dragon_return.go:455 (3)
    confirm_vr_lo = params["dr_confirm_vr_lo"]       # from dragon_return.go:480 (1.2)
    confirm_vr_hi = params["dr_confirm_vr_hi"]       # from dragon_return.go:480 (1.5)
    confirm_vol_full = params["dr_confirm_vol_full"] # from dragon_return.go:481 (7)
    confirm_vr_mid_lo = params["dr_confirm_vr_mid_lo"]  # from dragon_return.go:482 (> 0.8)
    confirm_vol_mid = params["dr_confirm_vol_mid"]   # from dragon_return.go:483 (3)
    k_hold_ratio = params["dr_k_hold_ratio"]         # from dragon_return.go:487 (MA5 * 1.03)
    k_score_hold = params["dr_k_score_hold"]         # from dragon_return.go:488 (5)
    k_score_cross = params["dr_k_score_cross"]       # from dragon_return.go:491 (2)
    k_near_lo = params["dr_k_near_lo_ratio"]         # from dragon_return.go:492 (MA5 * 0.98)
    k_score_near = params["dr_k_score_near"]         # from dragon_return.go:493 (2)
    tier_first = params["dr_level_first"]            # from dragon_return.go:206 (>= 60 first)
    buy_amount = params["ladder_buy_amount"]         # sizing lives in the Go position engine: baked amount (decline-by-bake)

    is_leader = ctx.get("is_leader")                 # Go: StockData.IsSectorTop2 fed by sector RPS rank
    if is_leader is None:                            # (adapter.go:196-197); sector list ABSENT in
        return None                                  # the sandbox -> decline, never fabricate a leader
    sector_rps20 = _num(ctx.get("sector_rps20"))     # Go: SectorRPS20 (adapter.go:198); ABSENT reading -> decline
    if sector_rps20 is None:
        return None

    price = _num(ctx.get("last_price"))              # Go: md.Price (adapter.go:142)
    bars = ctx.get("bars")
    if price is None and isinstance(bars, list) and bars:
        price = _bar_field(bars, len(bars) - 1, "close")
    if price is None:
        return None

    # Derived shape inputs: pre-aggregated ctx readings win, otherwise the
    # adapter.go:151-192 derivation from bars runs (single source: ctx["bars"]
    # is batched once by the carrier; download_history_data is ABSENT).
    first_rise = _num(ctx.get("first_rise_pct"))
    pullback = _num(ctx.get("pullback_pct"))
    pullback_days = _num(ctx.get("pullback_days"))
    shrink = _num(ctx.get("shrink_ratio"))
    ma5 = _num(ctx.get("ma5"))
    ma10 = _num(ctx.get("ma10"))
    ma20 = _num(ctx.get("ma20"))
    if (first_rise is None or pullback is None or pullback_days is None
            or shrink is None or ma5 is None or ma10 is None or ma20 is None):
        if not isinstance(bars, list) or len(bars) < min_bars:
            return None                              # from adapter.go:147 (no history -> zero struct -> no signal)
        n = len(bars)
        if ma5 is None:
            ma5 = _ma_field(bars, ma_short, "close")             # from adapter.go:153
        if ma10 is None:
            ma10 = _ma_field(bars, ma_mid, "close")              # from adapter.go:154
        if ma20 is None:
            ma20 = _ma_field(bars, ma_long, "close")             # from adapter.go:155
        start = n - rally_window                                 # from adapter.go:159-162
        if start < 0:
            start = 0
        hi = start                                               # from adapter.go:165-170 (peak close)
        peak = _bar_field(bars, start, "close")
        if peak is None:
            return None
        for i in range(start, n):
            value = _bar_field(bars, i, "close")
            if value is None:
                return None
            if value > peak:
                peak = value
                hi = i
        window_start_close = _bar_field(bars, start, "close")
        if peak is None or window_start_close is None or window_start_close <= 0:
            return None
        if first_rise is None:
            first_rise = (peak - window_start_close) / window_start_close  # from adapter.go:175
        if pullback is None:
            pullback = (peak - price) / peak if peak > 0 else None          # from adapter.go:178
        if pullback_days is None:
            pullback_days = float(n - 1 - hi)                               # from adapter.go:182
        if shrink is None:                                    # from adapter.go:189-192 (5-day avg volume / 20-day avg volume)
            avg_long = _avg_field_window(bars, ma_long, "volume")
            shrink = (_avg_field_window(bars, ma_short, "volume") / avg_long) if avg_long > 0 else None
    if (first_rise is None or pullback is None or pullback_days is None
            or shrink is None or ma5 is None or ma10 is None or ma20 is None):
        return None

    # dragonIdentity hard precondition: dragon_return.go:292-303. Any unmet
    # leg zeroes the whole score (Go returns early, score:260-261) -> no buy.
    if not is_leader:
        return None
    if first_rise < rise_min or first_rise > rise_max:         # from dragon_return.go:296
        return None
    if sector_rps20 < rps_min:                                 # from dragon_return.go:299
        return None
    total = score_dragon

    # pullbackHealth: dragon_return.go:334-378 (depth + days + shrink).
    if pullback >= pb_best_lo and pullback < pb_best_hi:       # from dragon_return.go:337
        total += pb_score_best
    elif pullback >= pb_best_hi and pullback < pb_good_hi:     # from dragon_return.go:339 (0.20..0.25 band)
        total += pb_score_good
    elif pullback >= pb_shallow_lo and pullback < pb_best_lo:  # from dragon_return.go:341
        total += pb_score_shallow
    elif pullback >= pb_good_hi and pullback < pb_deep_hi:     # from dragon_return.go:343 (0.25..0.35 band)
        total += pb_score_deep
    else:
        total += pb_score_other                                # from dragon_return.go:345

    if days_good_lo <= pullback_days <= days_good_hi:          # from dragon_return.go:352
        total += days_score_good
    elif days_short_lo <= pullback_days < days_good_lo:        # from dragon_return.go:354
        total += days_score_near
    elif days_good_hi < pullback_days <= days_long_hi:         # from dragon_return.go:356
        total += days_score_near
    else:
        total += days_score_other                              # from dragon_return.go:359

    if shrink < vr_good:                                       # from dragon_return.go:365
        total += vr_score_good
    elif shrink < vr_ok:                                       # from dragon_return.go:367
        total += vr_score_ok
    elif shrink < vr_mid:                                      # from dragon_return.go:368
        total += vr_score_mid
    elif shrink < vr_bad:                                      # from dragon_return.go:369
        total += vr_score_bad
    else:
        total += vr_score_other                                # from dragon_return.go:371

    # duckHead: dragon_return.go:405-459.
    if ma5 < ma10 and shrink < shrink_max:                    # from dragon_return.go:409
        total += duck_top_full
    elif ma5 < ma10:                                          # from dragon_return.go:412
        total += duck_top_half

    if ma20 > 0 and ma5 < ma20 and price >= ma20 * neck_support:   # from dragon_return.go:416
        if ma10 > ma5:
            total += neck_full                                 # from dragon_return.go:419
        else:
            total += neck_low                                  # from dragon_return.go:421
        # Go computes (MA10 - MA10*0.99)/(MA10*0.01), which is identically 1
        # for any MA10 > 0 (dragon_return.go:417 -- a degenerate "slope" that
        # always clears the 0.5 gate). Ported as written, bug included.
        slope_denom = ma10 * slope_delta
        if slope_denom > 0:
            slope = (ma10 - ma10 * slope_prev) / slope_denom   # from dragon_return.go:417
            if slope > slope_min:                              # from dragon_return.go:424
                total += slope_bonus                           # from dragon_return.go:425

    macd_green = _num(ctx.get("macd_green"))               # Go leaves MACDGreen at the zero value in the
    if macd_green is None:                                 # live adapter (adapter.go fills no MACD leg) ->
        macd_green = 0.0                                   # default 0 is the faithful port, not a decline
    legs = [ma5 > ma10,                                    # from dragon_return.go:430-441 (three beak sub-conditions;
            (macd_green < 0 and macd_green > 0 - macd_near) or macd_green > 0,   # :434-438
            shrink > beak_vr_lo and shrink < beak_vr_hi]                          # :439
    beak = sum(1 for leg in legs if leg)
    all_legs = sum(1 for leg in legs if leg is not None)   # counter arithmetic only (0/1 allowed)
    if beak == all_legs:                                   # from dragon_return.go:443 (case 3)
        total += beak_three
    elif beak + 1 == all_legs:                             # from dragon_return.go:445 (case 2)
        total += beak_two
    elif beak == 1:                                        # from dragon_return.go:447 (case 1)
        total += beak_one

    if ma5 < ma10 and ma5 > ma20:                              # from dragon_return.go:452
        total += nostril_full
    elif ma5 <= ma20 and ma5 > ma20 * neck_support:            # from dragon_return.go:454
        total += nostril_half

    # confirmSignal: dragon_return.go:476-497.
    if confirm_vr_lo <= shrink <= confirm_vr_hi:               # from dragon_return.go:480
        total += confirm_vol_full
    elif shrink > confirm_vr_mid_lo and shrink < confirm_vr_lo:  # from dragon_return.go:482
        total += confirm_vol_mid

    if price > ma5 and price > ma5 * k_hold_ratio:             # from dragon_return.go:487
        total += k_score_hold
        if price > ma5 and price > ma10:                       # from dragon_return.go:489
            total += k_score_cross
    elif price >= ma5 * k_near_lo and price <= ma5:            # from dragon_return.go:492
        total += k_score_near

    if total < tier_first:                                     # from dragon_return.go:206-208,509-531
        return None                                            # (first/main/accelerate all ActionBuy; below first -> nil)
    return make_signal(params, ctx, "dragon_return_buy", "buy", buy_amount, price)


# ---------------------------------------------------------------------------
# rule 3: double_bump (two volume waves)
# Go source: internal/strategies/double_bump/double_bump.go EvaluateReal:140-299,
#            GenerateSignal:352-390 (only full_chain turns into an order).
# ---------------------------------------------------------------------------

def rule_double_bump(params, ctx, side_hint=""):
    """Double-bump buy: first wave, second wave, gentle adjust, bullish MAs.

    No sector reading required -- the Go core is single-stock, so nothing
    here declines by the sector/index absence. Declines when bars or the
    price reference are missing, when no first wave exists (Go returns nil,
    double_bump.go:194-196) and at the second-wave hard gate (Go:225-227).
    """
    scale = params["pct_scale"]                        # from double_bump.go:257 (cap total at 100)
    min_bars = params["db_min_bars"]                   # from double_bump.go:142 (len(kLines) < 10 -> nil)
    weak_day_pct = params["db_weak_day_pct"]           # from double_bump.go:148 (ChangePct <= -1.5 -> watch)
    min_change_pct = params["db_min_change_pct"]       # from double_bump.go:162 (direction gate, config.go:5350 = 0)
    lookback = params["db_lookback"]                   # from double_bump.go:169 (lookback capped at 20)
    first_window = params["db_first_window"]           # from double_bump.go:181 (first wave within last 5 bars)
    break_ratio = params["db_break_ratio"]             # from double_bump.go:185 (close > avgClose * 1.05)
    first_multiple = params["db_first_vol_multiple"]   # from double_bump.go:185 (config.go:5346 = 1.5)
    second_multiple = params["db_second_vol_multiple"] # from double_bump.go:211 (config.go:5347 = 1.5)
    volume_weight = params["db_volume_weight"]         # from double_bump.go:212 (config.go:5353 = 0.4)
    adjust_max_pct = params["db_adjust_max_pct"]       # from double_bump.go:236 (AdjustVolRatioMax * 2, config.go:5348 = 3 -> 6.0)
    position_weight = params["db_position_weight"]     # from double_bump.go:237 (config.go:5351 = 0.3)
    ma_short = params["db_ma_short"]                   # from double_bump.go:243 (MA5)
    ma_mid = params["db_ma_mid"]                       # from double_bump.go:244 (MA10)
    ma_weight = params["db_ma_weight"]                 # from double_bump.go:246 (config.go:5352 = 0.3)
    align_ratio = params["db_ma_align_ratio"]          # from double_bump.go:246 (* 0.8 for bare alignment)
    full_chain_min = params["db_full_chain_min"]       # from double_bump.go:260 (total >= 60 -> full_chain)
    buy_amount = params["ladder_buy_amount"]           # sizing lives in the Go position engine: baked amount (decline-by-bake)

    price = _num(ctx.get("last_price"))                # Go guard double_bump.go:142 (si.Price <= 0)
    prev_close = _num(ctx.get("prev_close"))           # missing -> change pct unknown: decline
    bars = ctx.get("bars")
    if price is None or prev_close is None or prev_close <= 0:
        return None
    if not isinstance(bars, list) or len(bars) < min_bars:
        return None                                    # from double_bump.go:142

    change_pct = (price - prev_close) / prev_close * scale
    if change_pct <= weak_day_pct:                     # from double_bump.go:148-155 (watch leg -> no order)
        return None
    up_session = change_pct > min_change_pct           # from double_bump.go:162 (second wave must be an up leg)

    n = len(bars)
    window = n - 1                                     # from double_bump.go:169 (drop the last bar from the baseline)
    if lookback < window:
        window = lookback
    avg_vol = 0.0
    avg_close = 0.0
    for i in range(n - window - 1, n - 1):             # from double_bump.go:170-176
        value_v = _bar_field(bars, i, "volume")
        value_c = _bar_field(bars, i, "close")
        if value_v is None or value_c is None:
            return None
        avg_vol += value_v
        avg_close += value_c
    avg_vol /= float(window)
    avg_close /= float(window)
    if avg_close <= 0:                                 # Go would divide; fail closed instead
        return None

    first_start = n - first_window                     # from double_bump.go:181-183 (Go skips i < 0)
    if first_start < 0:
        first_start = 0
    first_idx = -1                                     # from double_bump.go:177-191 (first wave within last 5 bars)
    for i in range(first_start, n):
        value_c = _bar_field(bars, i, "close")
        value_v = _bar_field(bars, i, "volume")
        if value_c is None or value_v is None:
            continue
        if value_c > avg_close * break_ratio and value_v > avg_vol * first_multiple:
            first_idx = i
            break
    if first_idx < 0:                                  # from double_bump.go:193-196 (no first wave -> nil)
        return None

    vol_score = 0.0                                    # from double_bump.go:204-217 (second wave after the first)
    second_break = False
    if up_session:
        for i in range(first_idx + 1, n):
            value_v = _bar_field(bars, i, "volume")
            if value_v is None:
                continue
            if value_v > avg_vol * second_multiple:
                vol_score = volume_weight * scale
                second_break = True
                break
    if not second_break:                               # from double_bump.go:219-227 (hard gate: no second wave -> nil)
        return None

    adjust_score = 0.0                                 # from double_bump.go:229-238 (amplitude vs the baseline mean)
    high = _bar_field(bars, n - 1, "high")
    low = _bar_field(bars, n - 1, "low")
    if high is not None and low is not None:
        depth = (high - low) / avg_close * scale
        if up_session and depth < adjust_max_pct:
            adjust_score = position_weight * scale

    ma_score = 0.0                                     # from double_bump.go:240-250
    ma5 = _ma_field(bars, ma_short, "close")
    ma10 = _ma_field(bars, ma_mid, "close")
    last_close = _bar_field(bars, n - 1, "close")
    if ma5 is not None and ma10 is not None and last_close is not None:
        if ma5 > ma10:
            ma_score = ma_weight * scale * align_ratio
            if last_close > ma5:
                ma_score = ma_weight * scale

    total = vol_score + adjust_score + ma_score
    if total > scale:                                  # from double_bump.go:257
        total = scale
    if total < full_chain_min:                         # from double_bump.go:260; brief is ActionWatch (GenerateSignal:359-370) -> no order
        return None
    return make_signal(params, ctx, "double_bump_buy", "buy", buy_amount, price)


# ---------------------------------------------------------------------------
# rule 4: leader_decay (shortbase shared-input tactic, sell side)
# Go source: internal/strategies/leader_decay/leader_decay.go Evaluate:73-139,
#            boardScore:143-149, weaknessScore:153-168, retreatScore:172-185,
#            GenerateSignal:197-210; shared input struct:
#            internal/strategies/shortbase/shortbase.go Data:37-84.
# ---------------------------------------------------------------------------

def rule_leader_decay_v2(params, ctx, side_hint=""):
    """Leader board-break sell for a HELD name (shortbase.Data consumer).

    The Go tactic scores the shared shortbase.Data; the sandbox carrier
    supplies those derived facts through ctx readings named after the
    struct fields. Declines with None -- each naming the missing reading --
    when the per-stock board facts are absent:
      * consec_boards      (shortbase.go:69 ConsecBoards; needs a per-stock
        day-by-day limit history -- ladder_height is the POOL max, not a
        substitute, so it is never used here)
      * afternoon_reseal   (shortbase.go:71 AfternoonReseal from minute
        bars; without it the reseal protection cannot be proven, and a
        protective leg may never be assumed false)
    sealed_today / touched_board_today may arrive baked or are self-computed
    from UpStopPrice (available on the built-in surface). sector_drop_pct
    defaults to zero when absent, matching the Go adapter's documented
    "empty = 0" leg (shortbase.go:32). Emotion phase matching uses the
    baked params["ld_retreat_tokens"] list (Chinese phrases live in the
    baked JSON only -- this source stays pure ASCII).
    Sell needs a position: no qty -> no signal (cash market, no shorting).
    """
    min_bars = params["ld_min_bars"]                   # from leader_decay.go:75 (len(KLines) < 10 -> nodata)
    vol_window = params["ld_vol_window"]               # from shortbase.go:23 (TodayVolVs5d = today vs 5-day average)
    min_boards = params["ld_min_boards"]               # from leader_decay.go:80 (ConsecBoards < 2 -> not applicable)
    board_full_boards = params["ld_board_full_boards"] # from leader_decay.go:145 (b >= 5)
    board_score_full = params["ld_board_score_full"]   # from leader_decay.go:146 (30)
    board_score_base = params["ld_board_score_base"]   # from leader_decay.go:148 (12)
    board_score_span = params["ld_board_score_span"]   # from leader_decay.go:148 (18)
    big_face_ratio = params["ld_big_face_ratio"]       # from leader_decay.go:155 (Price <= PrevClose * 0.97)
    score_big_face = params["ld_score_big_face"]       # from leader_decay.go:156 (20)
    score_touched = params["ld_score_touched"]         # from leader_decay.go:159 (15)
    huge_volume_ratio = params["ld_huge_volume_ratio"] # from leader_decay.go:161 (TodayVolVs5d >= 2.0)
    score_huge_volume = params["ld_score_huge_volume"] # from leader_decay.go:162 (10)
    weakness_cap = params["ld_weakness_cap"]           # from leader_decay.go:164-166 (cap 40)
    sector_drop_ratio = params["ld_sector_drop_ratio"] # from leader_decay.go:174 (drop >= 0.5)
    score_sector_drop = params["ld_score_sector_drop"] # from leader_decay.go:175 (20)
    score_phase = params["ld_score_phase"]             # from leader_decay.go:179 (10)
    retreat_cap = params["ld_retreat_cap"]             # from leader_decay.go:182-184 (cap 30)
    min_score = params["ld_min_score"]                 # from leader_decay.go:60 (default pass gate 65)
    # leader_decay.go:108 puts 50..gate in a "watch" tier -- Go emits no order
    # there either (GenerateSignal:199 returns nil unless pass), so the watch
    # floor needs no knob here: anything under min_score is already "no order".

    boards = _num(ctx.get("consec_boards"))            # MISSING READING -> decline (see docstring)
    if boards is None:
        return None
    reseal = ctx.get("afternoon_reseal")               # MISSING READING -> decline (see docstring)
    if reseal is None:
        return None

    bars = ctx.get("bars")
    if not isinstance(bars, list) or len(bars) < min_bars:
        return None                                    # from leader_decay.go:75 (nodata leg)
    price = _num(ctx.get("last_price"))                # Go: sd.Price
    prev_close = _num(ctx.get("prev_close"))           # Go: sd.PrevClose
    if price is None:
        return None

    # still-sealed leg, leader_decay.go:85-88: baked reading wins, else the
    # exact UpStopPrice (available on the built-in surface) decides it.
    sealed = ctx.get("sealed_today")
    if sealed is None:
        up_stop = _num(ctx.get("up_stop"))
        sealed = bool(up_stop is not None and price >= up_stop)
    if sealed:
        return None
    if reseal:                                         # from leader_decay.go:90-93 (reseal protection: skip today)
        return None

    touched = ctx.get("touched_board_today")           # from leader_decay.go:158; self-computed fallback:
    if touched is None:                                # intraday high vs UpStopPrice
        up_stop = _num(ctx.get("up_stop"))
        last_high = _bar_field(bars, len(bars) - 1, "high")
        touched = up_stop is not None and last_high is not None and last_high >= up_stop

    today_volume = _bar_field(bars, len(bars) - 1, "volume")
    history_avg = _avg_field_window(bars[:len(bars) - 1], vol_window, "volume")
    vol_vs_5d = ctx.get("today_vol_vs_5d")             # from shortbase.go:23 TodayVolVs5d
    vol_vs_5d = _num(vol_vs_5d)
    if vol_vs_5d is None and today_volume is not None and history_avg > 0:
        vol_vs_5d = today_volume / history_avg

    # boardScore: leader_decay.go:143-149 (height below the gate: no tactic).
    if boards < min_boards:                            # from leader_decay.go:80
        return None
    if boards >= board_full_boards:                    # from leader_decay.go:145-146
        board = board_score_full
    else:                                              # from leader_decay.go:148: 12 + (b-2)/(5-2)*18
        board = board_score_base + ((boards - min_boards) / (board_full_boards - min_boards)) * board_score_span

    # weaknessScore: leader_decay.go:153-168 (big face + touched-board + huge volume, capped).
    weak = 0.0
    if prev_close is not None and prev_close > 0 and price <= prev_close * big_face_ratio:
        weak += score_big_face                         # from leader_decay.go:155-156
    if touched:
        weak += score_touched                          # from leader_decay.go:158-159
    if vol_vs_5d is not None and vol_vs_5d >= huge_volume_ratio:
        weak += score_huge_volume                      # from leader_decay.go:161-162
    if weak > weakness_cap:                            # from leader_decay.go:164-166
        weak = weakness_cap

    # retreatScore: leader_decay.go:172-185. sector drop absent = the Go
    # adapter's documented "empty = 0" leg (shortbase.go:32), NOT a proxy.
    retreat = 0.0
    sector_drop = _num(ctx.get("sector_limit_up_drop_pct"))
    if sector_drop is not None and sector_drop >= sector_drop_ratio:
        retreat += score_sector_drop                   # from leader_decay.go:174-175
    phase = ctx.get("emotion_phase")                   # from shortbase.go:31 (may be empty in Go)
    if isinstance(phase, str):
        for token in params["ld_retreat_tokens"] or []:    # from leader_decay.go:179 (contains "retreat" phase)
            if token and token in phase:
                retreat += score_phase                 # from leader_decay.go:179
                break
    if retreat > retreat_cap:                          # from leader_decay.go:182-184
        retreat = retreat_cap

    total = board + weak + retreat
    if total < min_score:                              # from leader_decay.go:104; watch leg (:108) is not an order
        return None

    qty = _num(ctx.get("position_qty"))                # Go emits sell for held names (GenerateSignal:202-209);
    if not qty:                                        # a non-held watch signal has no executable shape here
        return None
    return make_signal(params, ctx, "leader_decay_sell", "sell", None, price, qty=qty)


# ---------------------------------------------------------------------------
# Every params key the four rules read. Values are baked by the decision side
# (default tiers mirror config.go:5332-5353 and dragon_return DefaultParams
# 95-113; the retreat phase tokens carry Chinese text and live in the baked
# JSON only). Missing keys raise -- a silently missing threshold must fail
# loudly, exactly like the carrier rule layer.
# ---------------------------------------------------------------------------
LADDER_PARAM_KEYS = [
    "pct_scale",                            # from dragon.go:192 / double_bump.go:257 (percent scale)
    "lot_size",                             # from local_executor.py:572 (make_signal lot rounding)
    "symbol_head_len",                      # from local_executor.py:578 (short code build)
    "signal_codes",                         # from local_executor.py:575 (kind -> short code)
    "ladder_buy_amount",                    # sizing lives in the Go position engine: baked single buy amount
    "dragon_min_bars",                      # from dragon.go:142
    "dragon_seal_tolerance_pct",            # from dragon.go:154
    "dragon_f1_weight",                     # from dragon.go:156 (config.go:5332)
    "dragon_f1_base_ratio",                 # from dragon.go:156
    "dragon_f1_vol_ratio",                  # from dragon.go:158
    "dragon_f2_weight",                     # from dragon.go:172 (config.go:5333)
    "dragon_f2_full_pct",                   # from dragon.go:171
    "dragon_f2_half_pct",                   # from dragon.go:174
    "dragon_f2_half_ratio",                 # from dragon.go:174
    "dragon_f3_weight",                     # from dragon.go:180 (config.go:5334)
    "dragon_f3_premium_pct",                # from dragon.go:180
    "dragon_f3_own_gain_pct",               # from dragon.go:182
    "dragon_f3_half_ratio",                 # from dragon.go:183
    "dragon_rs_window",                     # from dragon.go:192
    "dragon_f4_weight",                     # from dragon.go:195 (config.go:5335)
    "dragon_f4_full_pct",                   # from dragon.go:194
    "dragon_f4_half_pct",                   # from dragon.go:197
    "dragon_f4_half_ratio",                 # from dragon.go:197
    "dragon_full_chain_min",                # from dragon.go:207
    "dr_min_bars",                          # from adapter.go:147
    "dr_ma_short",                          # from adapter.go:153
    "dr_ma_mid",                            # from adapter.go:154
    "dr_ma_long",                           # from adapter.go:155
    "dr_rally_window",                      # from adapter.go:159
    "dr_first_rise_min",                    # from dragon_return.go:296
    "dr_first_rise_max",                    # from dragon_return.go:296
    "dr_sector_rps_min",                    # from dragon_return.go:299
    "dr_score_dragon",                      # from dragon_return.go:302
    "dr_pb_best_lo",                        # from dragon_return.go:337
    "dr_pb_best_hi",                        # from dragon_return.go:337
    "dr_pb_good_hi",                        # from dragon_return.go:339
    "dr_pb_shallow_lo",                     # from dragon_return.go:341
    "dr_pb_deep_hi",                        # from dragon_return.go:343
    "dr_pb_score_best",                     # from dragon_return.go:337
    "dr_pb_score_good",                     # from dragon_return.go:339
    "dr_pb_score_shallow",                  # from dragon_return.go:341
    "dr_pb_score_deep",                     # from dragon_return.go:343
    "dr_pb_score_other",                    # from dragon_return.go:345
    "dr_days_good_lo",                      # from dragon_return.go:352
    "dr_days_good_hi",                      # from dragon_return.go:352
    "dr_days_short_lo",                     # from dragon_return.go:354
    "dr_days_long_hi",                      # from dragon_return.go:356
    "dr_days_score_good",                   # from dragon_return.go:353
    "dr_days_score_near",                   # from dragon_return.go:355
    "dr_days_score_other",                  # from dragon_return.go:359
    "dr_vr_good",                           # from dragon_return.go:365
    "dr_vr_ok",                             # from dragon_return.go:367
    "dr_vr_mid",                            # from dragon_return.go:368
    "dr_vr_bad",                            # from dragon_return.go:369
    "dr_vr_score_good",                     # from dragon_return.go:366
    "dr_vr_score_ok",                       # from dragon_return.go:367
    "dr_vr_score_mid",                      # from dragon_return.go:368
    "dr_vr_score_bad",                      # from dragon_return.go:369
    "dr_vr_score_other",                    # from dragon_return.go:371
    "dr_shrink_max",                        # from dragon_return.go:409
    "dr_duck_top_full",                     # from dragon_return.go:410
    "dr_duck_top_half",                     # from dragon_return.go:412
    "dr_neck_support_ratio",                # from dragon_return.go:416
    "dr_duck_neck_full",                    # from dragon_return.go:419
    "dr_duck_neck_low",                     # from dragon_return.go:421
    "dr_slope_prev_ratio",                  # from dragon_return.go:417
    "dr_slope_delta_ratio",                 # from dragon_return.go:417
    "dr_slope_min",                         # from dragon_return.go:424
    "dr_duck_slope_bonus",                  # from dragon_return.go:425
    "dr_macd_green_near",                   # from dragon_return.go:434
    "dr_beak_vr_lo",                        # from dragon_return.go:439
    "dr_beak_vr_hi",                        # from dragon_return.go:439
    "dr_beak_score_three",                  # from dragon_return.go:444
    "dr_beak_score_two",                    # from dragon_return.go:446
    "dr_beak_score_one",                    # from dragon_return.go:448
    "dr_nostril_full",                      # from dragon_return.go:453
    "dr_nostril_half",                      # from dragon_return.go:455
    "dr_confirm_vr_lo",                     # from dragon_return.go:480
    "dr_confirm_vr_hi",                     # from dragon_return.go:480
    "dr_confirm_vol_full",                  # from dragon_return.go:481
    "dr_confirm_vr_mid_lo",                 # from dragon_return.go:482
    "dr_confirm_vol_mid",                   # from dragon_return.go:483
    "dr_k_hold_ratio",                      # from dragon_return.go:487
    "dr_k_score_hold",                      # from dragon_return.go:488
    "dr_k_score_cross",                     # from dragon_return.go:491
    "dr_k_near_lo_ratio",                   # from dragon_return.go:492
    "dr_k_score_near",                      # from dragon_return.go:493
    "dr_level_first",                       # from dragon_return.go:206
    "db_min_bars",                          # from double_bump.go:142
    "db_weak_day_pct",                      # from double_bump.go:148
    "db_min_change_pct",                    # from double_bump.go:162 (config.go:5350)
    "db_lookback",                          # from double_bump.go:169
    "db_first_window",                      # from double_bump.go:181
    "db_break_ratio",                       # from double_bump.go:185
    "db_first_vol_multiple",                # from double_bump.go:185 (config.go:5346)
    "db_second_vol_multiple",               # from double_bump.go:211 (config.go:5347)
    "db_volume_weight",                     # from double_bump.go:212 (config.go:5353)
    "db_adjust_max_pct",                    # from double_bump.go:236 (config.go:5348 x 2)
    "db_position_weight",                   # from double_bump.go:237 (config.go:5351)
    "db_ma_short",                          # from double_bump.go:243
    "db_ma_mid",                            # from double_bump.go:244
    "db_ma_weight",                         # from double_bump.go:246 (config.go:5352)
    "db_ma_align_ratio",                    # from double_bump.go:246
    "db_full_chain_min",                    # from double_bump.go:260
    "ld_min_bars",                          # from leader_decay.go:75
    "ld_vol_window",                        # from shortbase.go:23 (TodayVolVs5d window)
    "ld_min_boards",                        # from leader_decay.go:80
    "ld_board_full_boards",                 # from leader_decay.go:145
    "ld_board_score_full",                  # from leader_decay.go:146
    "ld_board_score_base",                  # from leader_decay.go:148
    "ld_board_score_span",                  # from leader_decay.go:148
    "ld_big_face_ratio",                    # from leader_decay.go:155
    "ld_score_big_face",                    # from leader_decay.go:156
    "ld_score_touched",                     # from leader_decay.go:159
    "ld_huge_volume_ratio",                 # from leader_decay.go:161
    "ld_score_huge_volume",                 # from leader_decay.go:162
    "ld_weakness_cap",                      # from leader_decay.go:164
    "ld_sector_drop_ratio",                 # from leader_decay.go:174
    "ld_score_sector_drop",                 # from leader_decay.go:175
    "ld_score_phase",                       # from leader_decay.go:179
    "ld_retreat_cap",                       # from leader_decay.go:182
    "ld_retreat_tokens",                    # from leader_decay.go:179 (baked phase tokens)
    "ld_min_score",                         # from leader_decay.go:60
]

# Registration table read by scripts/build_local_executor.py (it inlines this
# module into both carriers and refuses a kind without a short code).
#
# leader_decay is ported TWICE on this box: rules_form.py carries the version
# taken from internal/strategies/leader_decay/leader_decay.go, this one was
# derived together with shortbase's data feed. Both are registered -- the second
# under its own kind and short code -- and both run in shadow. That is deliberate:
# deleting one would throw away an independent reading of the same Go code
# without ever comparing them, and the shadow window exists exactly to answer
# "do these two agree, and with the engine".
RULES = {
    "dragon": rule_dragon,
    "dragon_return": rule_dragon_return,
    "double_bump": rule_double_bump,
    "leader_decay_v2": rule_leader_decay_v2,
}
