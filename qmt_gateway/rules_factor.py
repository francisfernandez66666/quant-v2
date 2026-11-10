#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.rules_factor -- ported factor / pattern rule library.

Why this file exists
--------------------
Effective 2026-11-05 the broker only allows automated trading whose signals
are produced completely INSIDE a QMT built-in strategy, and that strategy has
no local file access and no network access. The signal maths that used to
live in Go therefore has to exist as plain, side-effect-free Python that a
strategy builder can bake into a template. This module is that port.

Go anchors (file:line -> this file)
-----------------------------------
internal/strategies/factor/factor.go:253  scoreRule          -> score_factor_rule
internal/strategies/factor/factor.go:210  Evaluate (30 bar guard, best-rule
                                          selection)         -> evaluate_factor_rules
internal/strategies/factor/factor.go:270  isFinancialFactor / finaScore
                                          (direct 0..1 score instead of a
                                          time-series percentile)
                                                           -> _direct_score /
                                              spec key "direct"
internal/strategies/factor/factor.go:280  dir / weight handling, missing key
                                          defaults to 1.0  -> "default_direction"
internal/strategies/factor/factor.go:298  used <= 0 -> no factor available
internal/strategies/factor/factor.go:306  score = (total/used + 1) / 2 * 100,
                                          clamped to [0, 100]
                                                           -> "score_domain"
internal/strategies/factor/factor.go:313  threshold (<= 0 falls back to 70)
internal/strategies/factor/factor.go:512  percentile (last value rank, NaN
                                          when fewer than 5 samples)
                                                           -> time_series_percentile
internal/strategies/factor/factor.go:561  finaScore absolute ranges
                                                           -> "financial_bands"
internal/strategies/factor/factor.go:589  clamp01
internal/strategies/pattern/pattern.go:198 evalRule (AND over conds, value in
                                          [min, max), pass only when every
                                          condition met)     -> eval_pattern_rule
internal/strategies/pattern/pattern.go:157 Evaluate (first fully matched
                                          template wins)     -> evaluate_pattern_rules
internal/factor/*.go                      every registered price-volume factor
                                                           -> FACTOR_LIBRARY
internal/indicator/ma.go, returns.go, rsi.go, atr.go
                                          SMA / EMA / returns / rolling std /
                                          RSI / ATR recursions (kept digit for
                                          digit, including the warm-up NaN rule)

What is NOT ported and why
--------------------------
The Go legs that FETCH data (KLine cache, capital flow HTTP, THS pools,
research DB, LLM news) are deliberately absent: in the sandbox there is
nothing to fetch from. A factor whose input cannot be obtained there does
not get a made-up value -- it gets no input, and a rule that needs it
DECLINES (returns None). See DECLINE_BY_DESIGN below for the full list with
the missing readout named for each family.

Contract kept for the later build gate
--------------------------------------
  * pure ASCII (the file is loaded into a GBK Windows terminal);
  * no I/O, no network, no clock, no randomness: only `math` is imported;
  * the rule layer (between the markers) contains NO numeric literal --
    every threshold, window, weight and scale is data read from `params`;
  * every signal is built by make_signal(), the same dict shape the local
    executor consumes.
(English: sandbox-safe port of the factor and pattern strategy libraries.)
"""

import math

NAN = float("nan")

# Structural identities used by the indicator maths. They are not business
# thresholds (no trading decision depends on their spelling), which is why
# they may live here instead of in PARAMS.
ZERO = 0.0
UNIT = 1.0
NEG_UNIT = -1.0
TWO = UNIT + UNIT
HALF = UNIT / TWO
HUNDRED = UNIT * UNIT * UNIT * (UNIT + UNIT)


# ---------------------------------------------------------------------------
# Factor library (data).
#
# One entry per factor ID, mirroring the Go registry
# (internal/factor/momentum.go:26, volatility.go:36, liquidity.go:50,
# morphology.go:159, alpha158.go:173, alpha101.go:92, quality.go:32,
# growth.go:19, value.go:72, size.go:24, emotion.go:34, limitup_micro.go:30).
#
# Fields:
#   kind          which handler computes the series
#   windows       every length the handler needs, as data (never a literal)
#   input         which ctx field feeds it; "bars" = the daily bars the
#                 sandbox really has, anything else names the readout the
#                 sandbox does NOT have
#   direct        True = score comes straight from the value (Go: financial
#                 factors by absolute range, factor.go:270-278), False = the
#                 value series is ranked in time (Go percentile(), factor.go:512)
#   missing_input prose naming what is absent when the sandbox declines
# ---------------------------------------------------------------------------

BARS_INPUT = "bars"

FACTOR_LIBRARY = {
    # --- momentum (Go momentum.go) ---
    "Mom5": {"kind": "momentum", "windows": [5], "input": BARS_INPUT},
    "Mom10": {"kind": "momentum", "windows": [10], "input": BARS_INPUT},
    "Mom20": {"kind": "momentum", "windows": [20], "input": BARS_INPUT},
    "Mom60": {"kind": "momentum", "windows": [60], "input": BARS_INPUT},
    # --- volatility (Go volatility.go, alpha158.go) ---
    "Volatility20": {"kind": "realized_vol", "windows": [20], "input": BARS_INPUT},
    "RealizedVol5": {"kind": "realized_vol", "windows": [5], "input": BARS_INPUT},
    "RealizedVol10": {"kind": "realized_vol", "windows": [10], "input": BARS_INPUT},
    "Amplitude20": {"kind": "amplitude", "windows": [20], "input": BARS_INPUT},
    "AtrRatio14": {"kind": "atr_ratio", "windows": [14], "input": BARS_INPUT},
    "HighLow20": {"kind": "high_low", "windows": [20], "input": BARS_INPUT},
    "VolRatio5": {"kind": "vol_break_ratio", "windows": [5], "input": BARS_INPUT},
    # --- liquidity (Go liquidity.go, alpha158.go) ---
    "STOA": {"kind": "log_amount", "windows": [20], "input": BARS_INPUT},
    "Amihud20": {"kind": "amihud", "windows": [20], "input": BARS_INPUT},
    "VolSurge5": {"kind": "vol_surge", "windows": [20], "input": BARS_INPUT},
    "VolShrink": {"kind": "vol_shrink", "windows": [5, 20], "input": BARS_INPUT},
    "VMA5": {"kind": "vol_ma_ratio", "windows": [5, 20], "input": BARS_INPUT},
    "VMA10": {"kind": "vol_ma_ratio", "windows": [10, 20], "input": BARS_INPUT},
    "VSTD20": {"kind": "vol_cv", "windows": [20], "input": BARS_INPUT},
    "VMAX10": {"kind": "vol_peak", "windows": [10], "input": BARS_INPUT},
    "VMIN10": {"kind": "vol_floor", "windows": [10], "input": BARS_INPUT},
    # --- morphology operators, the pattern templates' building blocks
    #     (Go morphology.go) ---
    "Brk20": {"kind": "breakout", "windows": [20], "input": BARS_INPUT},
    "Brk60": {"kind": "breakout", "windows": [60], "input": BARS_INPUT},
    "Drawdown20": {"kind": "drawdown", "windows": [20], "input": BARS_INPUT},
    "BullAlign": {"kind": "bull_align", "windows": [5, 10, 20], "input": BARS_INPUT},
    # --- technical / alpha families (Go alpha158.go, alpha101.go) ---
    "RSI14": {"kind": "rsi", "windows": [14], "input": BARS_INPUT},
    "BBI": {"kind": "bbi", "windows": [3, 6, 12, 24], "input": BARS_INPUT},
    "EMA10_20": {"kind": "ema_ratio", "windows": [10, 20], "input": BARS_INPUT},
    "Alpha1": {"kind": "alpha1", "windows": [5, 20], "input": BARS_INPUT},
    "Alpha4": {"kind": "alpha4", "windows": [9], "input": BARS_INPUT},
    "Alpha12": {"kind": "alpha12", "windows": [1], "input": BARS_INPUT},
    "Alpha101": {"kind": "alpha101", "windows": [], "input": BARS_INPUT},

    # ------------------------------------------------------------------
    # DECLINE BY DESIGN: everything below has no input inside the sandbox.
    # A rule that contains one of these returns None unless the operator
    # both supplies the ctx field AND bakes an explicit band/scale, so the
    # decline path always stays reachable.
    # ------------------------------------------------------------------
    # per-day turnover rate history (Go liquidity.go:13 reads s.Turnover,
    # which the live Go chain itself fills with NaN -- factor.go:455)
    "STO20": {"kind": "turnover_mean", "windows": [20], "input": "turnover_series",
              "missing_input": "per-day turnover rate series"},
    "TurnoverStd20": {"kind": "turnover_std", "windows": [20], "input": "turnover_series",
                      "missing_input": "per-day turnover rate series"},
    # size: raw close x total shares (Go size.go:10)
    "LnMktCap": {"kind": "ln_mktcap", "windows": [], "input": "total_share_series",
                 "missing_input": "total share count series (share capital panel)"},
    # valuation family (Go value.go): needs the per-day PE/PB/PS/PCF/DV panel
    "EP_ttm": {"kind": "valuation", "windows": [], "input": "valuation_series",
               "field": "pe_ttm", "missing_input": "per-day PE/PB/PS/PCF/dividend panel"},
    "BP": {"kind": "valuation", "windows": [], "input": "valuation_series",
           "field": "pb", "missing_input": "per-day PE/PB/PS/PCF/dividend panel"},
    "SP_ttm": {"kind": "valuation", "windows": [], "input": "valuation_series",
               "field": "ps_ttm", "missing_input": "per-day PE/PB/PS/PCF/dividend panel"},
    "CFP_ttm": {"kind": "valuation", "windows": [], "input": "valuation_series",
                "field": "pcf_ttm", "missing_input": "per-day PE/PB/PS/PCF/dividend panel"},
    "DP": {"kind": "valuation", "windows": [], "input": "valuation_series",
           "field": "dv_ttm", "missing_input": "per-day dividend yield panel"},
    # quality + growth family: point-in-time reports (Go quality.go, growth.go).
    # finaScore in Go normalises the LATEST report value on an absolute range,
    # hence direct = True.
    "ROE": {"kind": "financial", "windows": [], "input": "fin_pit", "field": "roe",
            "direct": True, "missing_input": "point-in-time financial report (PIT)"},
    "GrossMargin": {"kind": "financial", "windows": [], "input": "fin_pit",
                    "field": "gross_margin", "direct": True,
                    "missing_input": "point-in-time financial report (PIT)"},
    "NetMargin": {"kind": "financial", "windows": [], "input": "fin_pit",
                  "field": "net_margin", "direct": True,
                  "missing_input": "point-in-time financial report (PIT)"},
    "DebtToAssets": {"kind": "financial", "windows": [], "input": "fin_pit",
                     "field": "debt_to_assets", "direct": True,
                     "missing_input": "point-in-time financial report (PIT)"},
    "YoyNetProfit": {"kind": "financial", "windows": [], "input": "fin_pit",
                     "field": "yoy_net_profit", "direct": True,
                     "missing_input": "point-in-time financial report (PIT)"},
    "SUE": {"kind": "financial", "windows": [], "input": "fin_pit",
            "field": "single_quarter_ni_yoy", "direct": True,
            "missing_input": "point-in-time financial report (PIT)"},
    # market emotion family (Go emotion.go): only TODAY's market scalar exists
    # in the sandbox, never the history a percentile needs.
    "emo_limit_up": {"kind": "scalar_proxy", "windows": [], "input": "limit_up_count",
                     "direct": True, "missing_input": "market limit-up history"},
    "emo_max_board": {"kind": "scalar_proxy", "windows": [], "input": "ladder_height",
                      "direct": True, "missing_input": "market ladder-height history"},
    "emo_blast_rate": {"kind": "scalar_proxy", "windows": [], "input": "broken_board_ratio",
                       "direct": True, "missing_input": "market blast-rate history"},
    "emo_blast_cnt": {"kind": "scalar_proxy", "windows": [], "input": "broken_board_count",
                     "direct": True, "missing_input": "market blast-count history"},
    "emo_chg5": {"kind": "scalar_proxy", "windows": [], "input": "limit_up_count_history",
                 "direct": True, "missing_input": "market limit-up history"},
    # limit-up microstructure family (Go limitup_micro.go): the THS pools are
    # research-DB tables; the sandbox has no per-stock board / seal / first-lock
    # history at all, so these decline whatever the counter reports today.
    "lu_board": {"kind": "scalar_proxy", "windows": [], "input": "limit_board_count",
                 "direct": True, "missing_input": "per-stock limit-up board history (THS pool)"},
    "lu_board3": {"kind": "scalar_proxy", "windows": [], "input": "limit_board_count",
                  "direct": True, "missing_input": "per-stock limit-up board history (THS pool)"},
    "lu_seal_strength": {"kind": "scalar_proxy", "windows": [], "input": "seal_amount_ratio",
                         "direct": True, "missing_input": "seal amount / free-float cap (THS pool)"},
    "lu_early_seal": {"kind": "scalar_proxy", "windows": [], "input": "first_seal_minute",
                      "direct": True, "missing_input": "first seal minute + break count (THS pool)"},
    "lu_reblast_low": {"kind": "scalar_proxy", "windows": [], "input": "broken_board_ratio_history",
                       "direct": True, "missing_input": "market blast-rate history (THS pool)"},
    # capital flow -- the eastmoney main-force net inflow leg (Go reads it through
    # internal/engine/flow_factor_track.go and the GetRealtimeQuoteWithFlow chain).
    # It is an external HTTP readout, banned in the sandbox. Only a band the
    # operator baked can score it, and even then the value has to arrive in ctx;
    # absent means the rule declines.
    "FlowToday": {"kind": "scalar_proxy", "windows": [], "input": "flow_today_pct",
                  "direct": True, "missing_input": "eastmoney main-force net inflow, today"},
    "Flow5d": {"kind": "scalar_proxy", "windows": [], "input": "flow_5d_pct",
               "direct": True, "missing_input": "eastmoney main-force net inflow, 5 day"},
    # concept heat / news / LLM event / index leg / leadership: same story --
    # an external readout that no longer exists inside the sandbox.
    "ConceptHeat": {"kind": "scalar_proxy", "windows": [], "input": "concept_heat",
                    "direct": True, "missing_input": "concept heat ranking (external feed)"},
    "NewsEvent": {"kind": "scalar_proxy", "windows": [], "input": "news_event",
                  "direct": True, "missing_input": "news agent / LLM event scoring (external)"},
    "IndexChange": {"kind": "scalar_proxy", "windows": [], "input": "index_change_pct",
                    "direct": True, "missing_input": "index weight / benchmark leg (external)"},
    "IsLeader": {"kind": "scalar_proxy", "windows": [], "input": "is_leader",
                 "direct": True, "missing_input": "sector leadership ranking (external)"},
}

# The families that decline by design, kept as data so a test and the report
# can enumerate them without re-deriving it from comments.
DECLINE_BY_DESIGN = dict(
    (fid, spec.get("missing_input", ""))
    for fid, spec in FACTOR_LIBRARY.items()
    if spec.get("input") != BARS_INPUT
)

# Absolute ranges behind Go finaScore (factor.go:561-583). A financial factor
# is scored as clamp01(value / scale), or clamp01((offset - value) / scale)
# when lower is better (debt ratio). The reciprocal-ratio valuation factors
# score clamp01(value) straight.
FINANCIAL_BANDS = {
    "ROE": {"scale": 20.0},
    "YoyNetProfit": {"scale": 30.0},
    "SUE": {"scale": 30.0},
    "GrossMargin": {"scale": 40.0},
    "NetMargin": {"scale": 20.0},
    "DebtToAssets": {"scale": 100.0, "offset": 100.0},
    "EP_ttm": {"scale": 1.0},
    "BP": {"scale": 1.0},
    "SP_ttm": {"scale": 1.0},
    "CFP_ttm": {"scale": 1.0},
    "DP": {"scale": 5.0},
}

# Example rule tables. `factor_rules` mirrors factor.ActiveRule (ID, Name,
# Factors, Weights, Directions, BuyThreshold -- factor.go:64-77) and
# `pattern_rules` mirrors pattern.ActivePattern (ID, Name, Conds -- a Cond is
# {factor, min, max} with max EXCLUSIVE, pattern.go:27-39).
FACTOR_RULES_TEMPLATE = [
    {
        "id": "fac_1",
        "name": "factor_composite",
        "factors": ["Mom20", "VolShrink", "BullAlign"],
        "weights": {"Mom20": 0.4, "VolShrink": 0.3, "BullAlign": 0.3},
        "directions": {"Mom20": 1, "VolShrink": -1, "BullAlign": 1},
        "buy_threshold": 70.0,
    },
]

PATTERN_RULES_TEMPLATE = [
    {
        "id": "pat_1",
        "name": "pattern_template",
        "conds": [
            {"factor": "Drawdown20", "min": 0.0, "max": 0.3},
            {"factor": "VolShrink", "min": 0.0, "max": 1.0},
            {"factor": "BullAlign", "min": 0.5, "max": 2.0},
        ],
    },
]

PARAMS_TEMPLATE = {
    # --- guards ---
    # Go: len(md.KLines) < 30 -> nodata (factor.go:219, pattern.go:166)
    "min_bars": 30,
    # Go percentile(): fewer than 5 samples -> NaN -> factor skipped
    "percentile_min_samples": 5,
    "require_trading_day": True,
    # Both libraries only ever produced ActionBuy (factor.go:369, pattern.go:272).
    "entry_side": "buy",
    # --- scoring / thresholding / weight normalisation ---
    "default_buy_threshold": 70.0,   # factor.go:141 and factor.go:314
    "default_weight": 1.0,           # factor.go:286
    "default_direction": 1.0,        # factor.go:281
    "score_domain": {"floor": 0.0, "shift": 1.0, "divisor": 2.0, "full": 100.0},
    # --- tables ---
    "factor_defs": FACTOR_LIBRARY,
    "factor_rules": FACTOR_RULES_TEMPLATE,
    "pattern_rules": PATTERN_RULES_TEMPLATE,
    "financial_bands": FINANCIAL_BANDS,
    # Per-rule threshold overrides (rule id -> threshold). Empty here because
    # every template rule carries its own buy_threshold; the override exists so
    # the operator can retune one rule without rewriting the rule table.
    "factor_thresholds": {},
    # factor id -> {min, max} for the scalar proxies. EMPTY on purpose: no
    # band means no score, which is the decline path for every readout the
    # sandbox cannot obtain. Baking a band is an explicit operator decision.
    "scalar_bands": {},
    # --- signal assembly (same keys the local executor rule layer uses) ---
    "signal_codes": {"factor_composite": "FC", "pattern_template": "PT"},
    "buy_signals": ["factor_composite", "pattern_template"],
    "lot_size": 100,
    "symbol_head_len": 6,
    "min_cash_reserve": 1000.0,
    "order_amount_cap": 20000.0,
    # 2026-11-10 FIX (PLAN_CARRIER_RISK_REBUILD section 1.3 item 13): these two
    # module-local fallbacks used to disagree with the baked account truth
    # (min_order_amount 1000 vs Go factory 10000, daily_buy_cap 100000 vs the
    # account's 150000) -- the exact ambiguity that let sub-10k shreds slip
    # through the local chain. They must mirror the baked top-level values;
    # the baked keys are the single source, these only keep the module
    # importable standalone.
    "daily_buy_cap": 150000.0,
    "min_order_amount": 10000.0,
}


# ---------------------------------------------------------------------------
# INDICATOR LAYER -- faithful ports of internal/indicator/{ma,returns,rsi,atr}.go
# and internal/factor/ts_ops.go.
#
# Not rule logic: they turn bars into numbers and never decide anything.
# Every window length is passed in from the factor table, warm-up slots are
# NaN exactly like the Go originals, and a NaN anywhere Go would propagate
# one here does too.
# ---------------------------------------------------------------------------

# Deliberately byte-identical to local_executor.make_signal: the builder dedups
# identical top-level definitions when it inlines both carriers (PORTED_DEDUP),
# so the artifact carries one copy and never lets one module silently overwrite
# another. If the two ever drift, the texts differ and the collision gate fires.
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

def _finite(value):
    """True for a usable float (finite only; NaN and inf are missing data)."""
    return isinstance(value, (int, float)) and not isinstance(value, bool) \
        and math.isfinite(float(value))


def _nan_list(size):
    return [NAN for _ in range(size)]


def columns_from_bars(bars):
    """Bar dicts -> per-field lists, NaN where a bar lacks the field.

    Field names follow the sandbox context shape: open / high / low / close /
    volume / amount (plus an optional per-bar turnover, absent in practice).
    """
    bars = bars if isinstance(bars, (list, tuple)) else []
    cols = {}
    for field in ("open", "high", "low", "close", "volume", "amount", "turnover"):
        values = []
        for bar in bars:
            raw = bar.get(field) if isinstance(bar, dict) else None
            values.append(float(raw) if _finite(raw) else NAN)
        cols[field] = values
    return cols


def sma(values, window):
    """Simple moving average; NaN for the first window-1 slots (ma.go SMA).

    One deliberate divergence from the Go original: Go accumulates the window
    and lets a NaN poison every later slot (sum +/- NaN stays NaN forever),
    which on a gap-free bar series is identical to this re-read-per-window
    form. Here a bad bar costs only the windows that actually contain it, so a
    single halted day cannot silently blank the rest of the series.
    """
    out = _nan_list(len(values))
    size = int(window)
    if size <= 0:
        return out
    for index in range(size - 1, len(values)):
        block = values[index - size + 1:index + 1]
        if any(math.isnan(item) for item in block):
            continue
        out[index] = sum(block) / float(size)
    return out


def ema(values, window):
    """Exponential moving average seeded with the simple mean (ma.go EMA).

    k = 2 / (n + 1); NaN through the warm-up on purpose (the Go library does
    not back-fill zeros like the older data/macd helper does).
    """
    out = _nan_list(len(values))
    size = int(window)
    if size <= 0 or len(values) < size:
        return out
    out[size - 1] = sum(values[:size]) / float(size)
    factor = TWO / (float(size) + UNIT)
    for index in range(size, len(values)):
        previous = out[index - 1]
        if math.isnan(previous) or math.isnan(values[index]):
            out[index] = NAN
            continue
        out[index] = values[index] * factor + previous * (UNIT - factor)
    return out


def simple_returns(values):
    """r[i] = close[i]/close[i-1] - 1, first slot NaN (returns.go Returns)."""
    out = _nan_list(len(values))
    for index in range(1, len(values)):
        previous = values[index - 1]
        if values[index] > ZERO and previous > ZERO:
            out[index] = values[index] / previous - UNIT
    return out


def log_returns(values):
    """ln(close[i]/close[i-1]), first slot NaN (returns.go LogReturns)."""
    out = _nan_list(len(values))
    for index in range(1, len(values)):
        previous = values[index - 1]
        if values[index] > ZERO and previous > ZERO:
            out[index] = math.log(values[index] / previous)
    return out


def rolling_std(values, window):
    """Population standard deviation over a trailing window (returns.go)."""
    out = _nan_list(len(values))
    size = int(window)
    if size <= 0:
        return out
    for index in range(size - 1, len(values)):
        block = values[index - size + 1:index + 1]
        if any(math.isnan(item) for item in block):
            continue
        average = sum(block) / float(size)
        variance = sum((item - average) ** 2 for item in block)
        out[index] = math.sqrt(variance / float(size))
    return out


def rolling_max(values, window):
    """Trailing maximum, NaN during warm-up (ts_ops.go rollingMax)."""
    out = _nan_list(len(values))
    size = int(window)
    for index in range(len(values)):
        if index < size - 1:
            continue
        block = [item for item in values[index - size + 1:index + 1]
                 if not math.isnan(item)]
        if block:
            out[index] = max(block)
    return out


def rolling_min(values, window):
    """Trailing minimum, same semantics as rolling_max (ts_ops.go)."""
    out = _nan_list(len(values))
    size = int(window)
    for index in range(len(values)):
        if index < size - 1:
            continue
        block = [item for item in values[index - size + 1:index + 1]
                 if not math.isnan(item)]
        if block:
            out[index] = min(block)
    return out


def ts_rank(values, window):
    """Fractional rank inside the trailing window (ts_ops.go tsRank)."""
    out = _nan_list(len(values))
    size = int(window)
    for index in range(len(values)):
        current = values[index]
        if index < size - 1 or math.isnan(current):
            continue
        less = 0
        equal = 0
        for inner in range(index - size + 1, index + 1):
            item = values[inner]
            if math.isnan(item):
                continue
            if item < current:
                less += 1
            elif item == current:
                equal += 1
        out[index] = (float(less) + HALF * float(equal)) / float(size)
    return out


def ts_argmax_offset(values, window):
    """Normalised offset of the trailing maximum (ts_ops.go tsArgMaxOffset).

    0 = the peak is today, 1 = the peak is the oldest bar in the window.
    """
    out = _nan_list(len(values))
    size = int(window)
    if size <= 1:
        return out
    for index in range(len(values)):
        if index < size - 1 or math.isnan(values[index]):
            continue
        best = NAN
        offset = 0
        for inner in range(index - size + 1, index + 1):
            item = values[inner]
            if math.isnan(item):
                continue
            if math.isnan(best) or item > best:
                best = item
                offset = index - inner
        if not math.isnan(best):
            out[index] = float(offset) / float(size - 1)
    return out


def delta_series(values, window):
    """values[i] - values[i-n]; NaN during warm-up (ts_ops.go deltaSeries)."""
    out = _nan_list(len(values))
    size = int(window)
    for index in range(size, len(values)):
        left, right = values[index], values[index - size]
        if math.isnan(left) or math.isnan(right):
            continue
        out[index] = left - right
    return out


def sign_of(value):
    """sign(): positive -> 1, negative -> -1, else 0 (ts_ops.go signOp)."""
    if math.isnan(value):
        return NAN
    if value > ZERO:
        return UNIT
    if value < ZERO:
        return NEG_UNIT
    return ZERO


def rsi(values, window):
    """Wilder RSI (rsi.go RSI): seeded with the simple mean of the first n
    moves, then avg = (avg*(n-1) + cur) / n; average loss 0 gives the ceiling."""
    out = _nan_list(len(values))
    size = int(window)
    if size <= 0 or len(values) <= size:
        return out
    gains = [ZERO] * len(values)
    losses = [ZERO] * len(values)
    for index in range(1, len(values)):
        move = values[index] - values[index - 1]
        if move > ZERO:
            gains[index] = move
        else:
            losses[index] = -move
    avg_gain = sum(gains[1:size + 1]) / float(size)
    avg_loss = sum(losses[1:size + 1]) / float(size)
    out[size] = _rsi_value(avg_gain, avg_loss)
    for index in range(size + 1, len(values)):
        avg_gain = (avg_gain * float(size - 1) + gains[index]) / float(size)
        avg_loss = (avg_loss * float(size - 1) + losses[index]) / float(size)
        out[index] = _rsi_value(avg_gain, avg_loss)
    return out


def _rsi_value(avg_gain, avg_loss):
    if avg_loss == ZERO:
        return HUNDRED
    return HUNDRED - HUNDRED / (UNIT + avg_gain / avg_loss)


def true_range(highs, lows, closes):
    """max(H-L, |H-Cprev|, |L-Cprev|), first bar H-L (atr.go TrueRange)."""
    out = _nan_list(len(highs))
    if not highs:
        return out
    out[0] = highs[0] - lows[0]
    for index in range(1, len(highs)):
        span = highs[index] - lows[index]
        gap_high = abs(highs[index] - closes[index - 1])
        gap_low = abs(lows[index] - closes[index - 1])
        out[index] = max(span, gap_high, gap_low)
    return out


def atr(highs, lows, closes, window):
    """ATR with Wilder recursion, seeded on the first n true ranges (atr.go)."""
    ranges = true_range(highs, lows, closes)
    out = _nan_list(len(ranges))
    size = int(window)
    if size <= 0 or len(ranges) < size:
        return out
    out[size - 1] = sum(ranges[:size]) / float(size)
    for index in range(size, len(ranges)):
        out[index] = (out[index - 1] * float(size - 1) + ranges[index]) / float(size)
    return out


def clamp01(value):
    """Truncate into [0,1] (factor.go:589 clamp01)."""
    if math.isnan(value):
        return NAN
    if value < ZERO:
        return ZERO
    if value > UNIT:
        return UNIT
    return value


# ---------------------------------------------------------------------------
# FACTOR HANDLERS -- one per "kind" in FACTOR_LIBRARY.
# Each returns a list aligned with the bars, or None when the factor's INPUT
# does not exist in the sandbox (that is the decline-by-design signal).
# ---------------------------------------------------------------------------

def _close(cols):
    return cols["close"]


def _f_momentum(cols, windows):
    close = _close(cols)
    span = int(windows[0])
    out = _nan_list(len(close))
    for index in range(len(close)):
        if index < span:
            continue
        here, before = close[index], close[index - span]
        if here > ZERO and before > ZERO:
            out[index] = here / before - UNIT
    return out


def _f_realized_vol(cols, windows):
    return rolling_std(log_returns(_close(cols)), windows[0])


def _f_amplitude(cols, windows):
    close = _close(cols)
    pointwise = _nan_list(len(close))
    for index, price in enumerate(close):
        if price > ZERO:
            pointwise[index] = (cols["high"][index] - cols["low"][index]) / price
    return sma(pointwise, windows[0])


def _f_atr_ratio(cols, windows):
    close = _close(cols)
    series = atr(cols["high"], cols["low"], close, windows[0])
    out = _nan_list(len(close))
    for index, price in enumerate(close):
        if not math.isnan(series[index]) and price > ZERO:
            out[index] = series[index] / price
    return out


def _f_high_low(cols, windows):
    high = rolling_max(cols["high"], windows[0])
    low = rolling_min(cols["low"], windows[0])
    out = _nan_list(len(high))
    for index in range(len(out)):
        if math.isnan(high[index]) or math.isnan(low[index]) or low[index] <= ZERO:
            continue
        out[index] = high[index] / low[index]
    return out


def _f_vol_break_ratio(cols, windows):
    ret = simple_returns(_close(cols))
    std = rolling_std(ret, windows[0])
    out = _nan_list(len(ret))
    for index in range(1, len(out)):
        if math.isnan(std[index]) or std[index] <= ZERO or math.isnan(ret[index]):
            continue
        out[index] = abs(ret[index]) / std[index]
    return out


def _f_log_amount(cols, windows):
    average = sma(cols["amount"], windows[0])
    out = _nan_list(len(average))
    for index, value in enumerate(average):
        if value > ZERO:
            out[index] = math.log(value)
    return out


def _f_amihud(cols, windows):
    close = _close(cols)
    amount = cols["amount"]
    ret = simple_returns(close)
    pointwise = _nan_list(len(close))
    for index in range(1, len(close)):
        if amount[index] > ZERO and not math.isnan(ret[index]):
            pointwise[index] = abs(ret[index]) / amount[index]
    return sma(pointwise, windows[0])


def _f_vol_surge(cols, windows):
    """today volume / mean of the PREVIOUS `windows[0]` bars (morphology.go)."""
    volume = cols["volume"]
    span = int(windows[0])
    out = _nan_list(len(volume))
    for index in range(len(volume)):
        if index < span:
            continue
        block = volume[index - span:index]
        if any(math.isnan(item) for item in block):
            continue
        total = sum(block)
        if total <= ZERO or volume[index] <= ZERO:
            continue
        out[index] = volume[index] / (total / float(span))
    return out


def _f_vol_shrink(cols, windows):
    """short mean volume / long mean volume, both EXCLUDING today."""
    volume = cols["volume"]
    short, long = int(windows[0]), int(windows[1])
    out = _nan_list(len(volume))
    for index in range(len(volume)):
        if index < long:
            continue
        short_block = volume[index - short:index]
        long_block = volume[index - long:index]
        if any(math.isnan(item) for item in short_block + long_block):
            continue
        short_total = sum(short_block)
        long_total = sum(long_block)
        if short_total <= ZERO or long_total <= ZERO:
            continue
        out[index] = (short_total / float(short)) / (long_total / float(long))
    return out


def _f_breakout(cols, windows):
    """1 when the close makes an n-day high over the previous n bars."""
    close = _close(cols)
    span = int(windows[0])
    out = _nan_list(len(close))
    for index in range(len(close)):
        if index < span:
            continue
        block = close[index - span:index]
        if any(math.isnan(item) for item in block):
            continue
        top = max(block)
        if math.isnan(close[index]):
            continue
        out[index] = UNIT if close[index] > top else ZERO
    return out


def _f_drawdown(cols, windows):
    """1 - close / max close over the window INCLUDING today."""
    close = _close(cols)
    span = int(windows[0])
    out = _nan_list(len(close))
    for index in range(len(close)):
        if index < span:
            continue
        block = close[index - span:index + 1]
        if any(math.isnan(item) for item in block):
            continue
        top = max(block)
        if top <= ZERO or close[index] <= ZERO:
            continue
        out[index] = UNIT - close[index] / top
    return out


def _f_bull_align(cols, windows):
    """MA short > MA mid > MA long and close > MA short -> 1 (morphology.go)."""
    close = _close(cols)
    fast, mid, slow = (sma(close, windows[0]), sma(close, windows[1]),
                       sma(close, windows[2]))
    out = _nan_list(len(close))
    for index in range(len(close)):
        if index < int(windows[2]):
            continue
        if math.isnan(fast[index]) or math.isnan(mid[index]) or math.isnan(slow[index]):
            continue
        hit = (fast[index] > mid[index] and mid[index] > slow[index]
               and close[index] > fast[index])
        out[index] = UNIT if hit else ZERO
    return out


def _f_rsi(cols, windows):
    return rsi(_close(cols), windows[0])


def _f_bbi(cols, windows):
    close = _close(cols)
    legs = [sma(close, span) for span in windows]
    out = _nan_list(len(close))
    for index in range(len(close)):
        if any(math.isnan(leg[index]) for leg in legs):
            continue
        out[index] = sum(leg[index] for leg in legs) / float(len(legs))
    return out


def _f_ema_ratio(cols, windows):
    close = _close(cols)
    fast, slow = ema(close, windows[0]), ema(close, windows[1])
    out = _nan_list(len(close))
    for index in range(len(close)):
        if math.isnan(fast[index]) or math.isnan(slow[index]) or slow[index] <= ZERO:
            continue
        out[index] = fast[index] / slow[index] - UNIT
    return out


def _f_vma_ratio(cols, windows):
    volume = cols["volume"]
    fast, base = sma(volume, windows[0]), sma(volume, windows[1])
    out = _nan_list(len(volume))
    for index in range(len(volume)):
        if math.isnan(fast[index]) or math.isnan(base[index]) or base[index] <= ZERO:
            continue
        out[index] = fast[index] / base[index]
    return out


def _f_vol_cv(cols, windows):
    volume = cols["volume"]
    std, base = rolling_std(volume, windows[0]), sma(volume, windows[0])
    out = _nan_list(len(volume))
    for index in range(len(volume)):
        if math.isnan(std[index]) or math.isnan(base[index]) or base[index] <= ZERO:
            continue
        out[index] = std[index] / base[index]
    return out


def _f_vol_peak(cols, windows):
    volume = cols["volume"]
    top = rolling_max(volume, windows[0])
    out = _nan_list(len(volume))
    for index in range(len(volume)):
        if math.isnan(top[index]) or volume[index] <= ZERO:
            continue
        out[index] = top[index] / volume[index]
    return out


def _f_vol_floor(cols, windows):
    volume = cols["volume"]
    floor = rolling_min(volume, windows[0])
    out = _nan_list(len(volume))
    for index in range(len(volume)):
        if math.isnan(floor[index]) or floor[index] <= ZERO or volume[index] <= ZERO:
            continue
        out[index] = volume[index] / floor[index]
    return out


def _f_alpha1(cols, windows):
    """WQ-Alpha1: 5-day argmax offset of (close or 20d vol, squared)."""
    close = _close(cols)
    ret = simple_returns(close)
    std = rolling_std(ret, windows[1])
    composite = _nan_list(len(close))
    for index in range(len(close)):
        if math.isnan(ret[index]) or math.isnan(std[index]):
            continue
        base = std[index] if ret[index] < ZERO else close[index]
        composite[index] = base * base
    return ts_argmax_offset(composite, windows[0])


def _f_alpha4(cols, windows):
    rank = ts_rank(cols["low"], windows[0])
    return [NEG_UNIT * item if not math.isnan(item) else NAN for item in rank]


def _f_alpha12(cols, windows):
    span = int(windows[0])
    dvol = delta_series(cols["volume"], span)
    dclose = delta_series(_close(cols), span)
    out = _nan_list(len(dvol))
    for index in range(len(out)):
        if math.isnan(dvol[index]) or math.isnan(dclose[index]):
            continue
        out[index] = sign_of(dvol[index]) * (NEG_UNIT * dclose[index])
    return out


def _f_alpha101(cols, windows):
    close = _close(cols)
    high, low, volume = cols["high"], cols["low"], cols["volume"]
    out = _nan_list(len(close))
    for index in range(len(close)):
        if high[index] <= low[index] or volume[index] <= ZERO:
            continue
        if math.isnan(close[index]):
            continue
        position = ((TWO * close[index] - low[index] - high[index])
                    / (high[index] - low[index]))
        out[index] = position * volume[index]
    return out


# Kinds whose INPUT is a per-bar series the sandbox does not carry.
def _f_turnover_mean(cols, windows, series):
    return sma(series, windows[0])


def _f_turnover_std(cols, windows, series):
    return rolling_std(series, windows[0])


def _f_ln_mktcap(cols, windows, series):
    close = cols["close"]
    out = _nan_list(len(close))
    for index in range(len(close)):
        share = series[index] if index < len(series) else NAN
        if math.isnan(share) or close[index] <= ZERO or share <= ZERO:
            continue
        out[index] = math.log(close[index] * share)
    return out


def _f_valuation(cols, windows, series, reciprocal):
    out = _nan_list(len(cols["close"]))
    for index in range(len(out)):
        value = series[index] if index < len(series) else NAN
        if math.isnan(value):
            continue
        if reciprocal:
            if value > ZERO:
                out[index] = UNIT / value
        else:
            out[index] = value
    return out


_BAR_KINDS = {
    "momentum": _f_momentum,
    "realized_vol": _f_realized_vol,
    "amplitude": _f_amplitude,
    "atr_ratio": _f_atr_ratio,
    "high_low": _f_high_low,
    "vol_break_ratio": _f_vol_break_ratio,
    "log_amount": _f_log_amount,
    "amihud": _f_amihud,
    "vol_surge": _f_vol_surge,
    "vol_shrink": _f_vol_shrink,
    "breakout": _f_breakout,
    "drawdown": _f_drawdown,
    "bull_align": _f_bull_align,
    "rsi": _f_rsi,
    "bbi": _f_bbi,
    "ema_ratio": _f_ema_ratio,
    "vol_ma_ratio": _f_vma_ratio,
    "vol_cv": _f_vol_cv,
    "vol_peak": _f_vol_peak,
    "vol_floor": _f_vol_floor,
    "alpha1": _f_alpha1,
    "alpha4": _f_alpha4,
    "alpha12": _f_alpha12,
    "alpha101": _f_alpha101,
}

_SERIES_KINDS = {
    "turnover_mean": _f_turnover_mean,
    "turnover_std": _f_turnover_std,
    "ln_mktcap": _f_ln_mktcap,
}


# ---------------------------------------------------------------------------
# INPUT RESOLUTION: does the sandbox even have this factor's raw material?
# ---------------------------------------------------------------------------

def _as_number(value):
    """ctx scalar -> float, or None when it is not a usable reading.

    Bools are kept as bools on purpose: a boolean readout (news_event,
    is_leader) carries no magnitude, so no band may be invented for it.
    """
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)) and math.isfinite(float(value)):
        return float(value)
    return None


def factor_input(params, ctx, fid, cols=None):
    """Resolve one factor id into something scoreable, or None.

    Returns:
      None                        -- the INPUT is not obtainable here; a rule
                                     holding this factor must decline
                                     (never score it as zero: that would
                                     silently keep the Go weight in play)
      {"values": [...]}           -- a series, ranked in time by the caller
      {"values": [...], "last": v}-- same, with the raw last value (patterns)
      {"score": x}                -- a direct 0..1 score, no percentile
    """
    defs = params.get("factor_defs") or FACTOR_LIBRARY
    spec = defs.get(fid)
    if not spec:
        return None
    kind = spec.get("kind")
    windows = spec.get("windows") or []
    input_key = spec.get("input") or BARS_INPUT

    if input_key == BARS_INPUT:
        if cols is None:
            cols = columns_from_bars(ctx.get("bars"))
        handler = _BAR_KINDS.get(kind)
        if handler is None:
            return None
        values = handler(cols, windows)
        return {"values": values, "last": _last_value(values)}

    raw = ctx.get(input_key)
    if raw is None:
        return None

    if kind == "scalar_proxy":
        return _scalar_score(params, fid, raw, spec)

    if kind == "financial":
        value = _pit_value(raw, spec.get("field"))
        if value is None:
            return None
        return _banded_score(params, fid, value, spec)

    if kind == "valuation":
        series = _field_series(raw, spec.get("field"))
        if series is None:
            return None
        reciprocal = fid in ("EP_ttm", "BP", "SP_ttm", "CFP_ttm")
        values = _f_valuation(cols or columns_from_bars(ctx.get("bars")), windows,
                              series, reciprocal)
        return {"values": values, "last": _last_value(values)}

    series = _plain_series(raw)
    if series is None:
        return None
    handler = _SERIES_KINDS.get(kind)
    if handler is None:
        return None
    values = handler(cols or columns_from_bars(ctx.get("bars")), windows, series)
    return {"values": values, "last": _last_value(values)}


def _plain_series(raw):
    """A ctx list/scalar of numbers -> a NaN-padded float list, or None."""
    if isinstance(raw, (list, tuple)):
        values = []
        for item in raw:
            number = _as_number(item)
            values.append(NAN if number is None else float(number))
        return values or None
    number = _as_number(raw)
    if number is None:
        return None
    return [float(number)]


def _field_series(raw, field):
    """{"pe_ttm": [...]} style panel, or a scalar panel entry, -> a series."""
    if not field:
        return None
    if isinstance(raw, dict):
        return _plain_series(raw.get(field))
    return None


def _pit_value(raw, field):
    """Latest point-in-time report value for one field, or None."""
    if isinstance(raw, dict):
        number = _as_number(raw.get(field))
        if number is not None:
            return float(number)
        return None
    if isinstance(raw, (list, tuple)):
        for item in reversed(list(raw)):
            if isinstance(item, dict) and field in item:
                number = _as_number(item.get(field))
                if number is not None:
                    return float(number)
        return None
    return None


def _banded_score(params, fid, value, spec):
    """Absolute-range score (Go finaScore, factor.go:561-583): clamp01 of
    (offset - v)/scale when lower is better, else v/scale.

    Go's default branch falls back to clamp01(v) for an unlisted id; here an
    unlisted id DECLINES instead. Reason: clamp01 on a value whose unit nobody
    pinned is a silent scale error (a 0.15 ratio vs a 15 percent readout both
    pass through it), and the whole point of the PIT families is that the
    sandbox cannot verify the unit it is being handed.
    """
    bands = params.get("financial_bands") or {}
    band = bands.get(fid)
    if not band:
        return None
    scale = band.get("scale")
    if scale is None or scale == ZERO:
        return None
    offset = band.get("offset")
    if offset is not None:
        return {"score": clamp01((float(offset) - value) / float(scale))}
    return {"score": clamp01(value / float(scale))}


def _scalar_score(params, fid, raw, spec):
    """EXPLICIT PROXY for a readout that has no history inside the sandbox.

    The Go library ranks these factors in time (percentile); here only
    today's scalar exists, so a score is possible ONLY if the operator baked
    an absolute band for this exact factor id (params["scalar_bands"]). No
    band baked -- or no value in ctx -- means the factor is unscoreable and
    the rule declines: the proxy can never quietly manufacture a signal, and
    it is opt-in per factor rather than opt-out.
    """
    bands = params.get("scalar_bands") or {}
    band = bands.get(fid)
    if not band:
        return None
    number = _as_number(raw)
    if number is None:
        return None
    if isinstance(number, bool):
        # A boolean readout (news_event / is_leader) carries no magnitude, so
        # the band is only the opt-in marker; the value ITSELF is the condition.
        return {"score": float(number)}
    low = band.get("min")
    high = band.get("max")
    if low is None or high is None:
        return None
    return {"score": float(float(low) <= number < float(high))}


def _last_value(values):
    """The newest value of a series, NaN when there is none."""
    if not values:
        return NAN
    return values[-1]


def last_of(items):
    """Newest element of a list, or None (index maths kept out of the rules)."""
    items = list(items or [])
    if not items:
        return None
    return items[-1]


def all_but_last(items):
    """Every element except the newest one (the percentile history)."""
    items = list(items or [])
    if not items:
        return []
    return items[:len(items) - 1]


def symbol_head(symbol, length):
    """Leading digits of a symbol, e.g. '600000.SH' -> '600000'."""
    text = str(symbol or "")
    return text.split(".")[0][:length]


# ---------------------------------------------------------------------------
# ==== RULE LAYER BEGIN ====
# Everything between the markers is decision logic and carries no numeric
# literal at all: guards, weights, directions, thresholds, score domains and
# pattern bands are all read from `params`.
# ---------------------------------------------------------------------------

def time_series_percentile(values, min_samples):
    """Rank of the last value among its own history (factor.go:512 percentile).

    Go refuses in exactly three shapes and this mirrors them: fewer than
    `min_samples` values in total, a NaN newest value, or no usable value left
    once the newest one is dropped. Each of those is None (Go: NaN) and the
    caller then DROPS the factor from the composite instead of scoring it zero.
    Equal values do not count as "below", so a flat series lands at 0 just like
    the Go output (and a monotone one at 1).
    """
    values = list(values or [])
    floor = int(min_samples)
    if len(values) < floor:
        return None
    last = last_of(values)
    if last is None or math.isnan(float(last)):
        return None
    history = [item for item in all_but_last(values) if not math.isnan(item)]
    if not history:
        return None
    below = len([item for item in history if item < last])
    return float(below) / float(len(history))


def score_factor_rule(params, rule, ctx, cols=None):
    """One factor rule -> {"score", "pass", "details", "declined", "reason"}.

    Mirrors scoreRule (factor.go:253-328): every factor is ranked in its own
    time series, the contribution is weight x direction x percentile with the
    short-side rewrite (1 - pct) when the direction is negative, the weights
    of the factors that actually scored are the normalisation denominator, and
    the composite is mapped onto the baked score domain before it is compared
    with the rule threshold.

    "declined" is True when a factor's INPUT cannot be obtained in the
    sandbox: the Go chain would have scored it from a research column that
    simply does not exist here, so the whole rule is refused instead of being
    scored on a subset of its factors.
    """
    factors = rule.get("factors") or []
    if not factors:
        return {"declined": True, "reason": "no_factors", "score": ZERO, "pass": False,
                "details": {}}
    weights = rule.get("weights") or {}
    directions = rule.get("directions") or {}
    default_weight = params["default_weight"]
    default_direction = params["default_direction"]
    min_samples = params["percentile_min_samples"]

    # Accumulators start at zero only AFTER every required reading above resolved:
    # a missing weight or direction returns earlier, so "sum of nothing" can never be
    # mistaken for "scored zero" (the two read identically downstream: no signal).
    total = ZERO
    used = ZERO
    details = {}
    for fid in factors:
        entry = factor_input(params, ctx, fid, cols)
        if entry is None:
            return {"declined": True, "reason": "input_absent:%s" % (fid,),
                    "score": ZERO, "pass": False, "details": details}
        if "score" in entry:
            pct = float(entry["score"])
            if math.isnan(pct):
                continue
        else:
            pct = time_series_percentile(entry.get("values"), min_samples)
            if pct is None:
                continue  # Go: NaN percentile -> the factor drops out of the composite
        weight = weights.get(fid, default_weight)
        direction = directions.get(fid, default_direction)
        if direction < ZERO:
            total += float(weight) * (UNIT - pct)
        else:
            total += float(weight) * float(direction) * pct
        used += float(weight)
        details[fid] = pct

    if used <= ZERO:
        return {"declined": False, "reason": "no_usable_factor", "score": ZERO,
                "pass": False, "details": details}

    domain = params["score_domain"]
    score = (total / used + float(domain["shift"])) / float(domain["divisor"]) \
        * float(domain["full"])
    if score < float(domain["floor"]):
        score = float(domain["floor"])
    if score > float(domain["full"]):
        score = float(domain["full"])

    threshold = _rule_threshold(params, rule)
    return {"declined": False, "score": score, "pass": score >= threshold,
            "details": details, "threshold": threshold,
            "reason": "factor_composite:%s" % (rule.get("id"),)}


def _rule_threshold(params, rule):
    """Buy threshold of one rule (factor.go:313).

    Precedence: params["factor_thresholds"][rule id] (the knob the operator
    turns per rule), else the rule's own buy_threshold, else the baked
    default -- and a non-positive threshold falls back to the default exactly
    like the Go code (BuyThreshold <= 0 -> 70).
    """
    overrides = params.get("factor_thresholds") or {}
    threshold = overrides.get(rule.get("id"))
    if threshold is None:
        threshold = rule.get("buy_threshold")
    if threshold is None or float(threshold) <= ZERO:
        return float(params["default_buy_threshold"])
    return float(threshold)


def evaluate_factor_rules(params, ctx, side_hint=""):
    """The fac_1..fac_N library: the best-scoring rule fires, if it passes.

    Guards, in the Go order: an empty library is a disabled strategy
    (factor.go:159), a short bar series is "nodata" (factor.go:219), then each
    rule scores independently and the highest raw score wins whether or not it
    passed (factor.go:228-236); only a passing best rule emits (factor.go:337).
    """
    rules = [item for item in (params.get("factor_rules") or []) if item]
    if not rules:
        return None
    if side_hint and side_hint != params["entry_side"]:
        return None
    if params.get("require_trading_day") and not ctx.get("trading_day"):
        return None
    bars = ctx.get("bars") or []
    if len(bars) < int(params["min_bars"]):
        return None
    cols = columns_from_bars(bars)

    best = None
    best_rule = None
    for rule in rules:
        scored = score_factor_rule(params, rule, ctx, cols)
        if scored.get("declined"):
            continue  # this rule's inputs are not obtainable here
        if best is None or scored["score"] > best["score"]:
            best = scored
            best_rule = rule
    if not best or not best.get("pass"):
        return None
    return _emit(params, ctx, "factor_composite", best_rule, best)


def eval_pattern_rule(params, pattern, ctx, cols=None):
    """One ActivePattern -> {"met", "total", "pass", "details", "declined"}.

    Mirrors evalRule (pattern.go:198-236): every condition is
    min <= value < max on the LAST factor value (a half-open band, and the
    exclusivity is load bearing -- pattern_test.go:42 proves a flat series
    must NOT match VolShrink in [0, 1)), the template passes only when every
    condition is met, and a NaN value counts toward the total but never as
    met. A condition whose factor input does not exist here makes the whole
    template unscoreable -- the Go code would likewise never reach met == total.
    """
    conds = [item for item in (pattern.get("conds") or []) if item]
    if not conds:
        return {"declined": True, "total": len(conds), "pass": False,
                "details": {}, "reason": "no_conds"}
    details = {}
    matched = []
    for cond in conds:
        fid = cond.get("factor")
        entry = factor_input(params, ctx, fid, cols)
        if entry is None:
            return {"declined": True, "total": len(conds), "pass": False,
                    "details": details, "reason": "input_absent:%s" % (fid,)}
        value = entry["score"] if "score" in entry else entry.get("last")
        details[fid] = value
        if value is None or math.isnan(float(value)):
            continue
        low = cond.get("min")
        high = cond.get("max")
        if low is None or high is None:
            continue
        matched.append(float(low) <= float(value) < float(high))
    total = len(conds)
    met = len([item for item in matched if item])
    domain = params["score_domain"]
    score = float(met) / float(total) * float(domain["full"])
    return {"declined": False, "met": met, "total": total, "score": score,
            "pass": met == total, "details": details,
            "reason": "pattern_matched:%s/%s" % (met, total)}


def evaluate_pattern_rules(params, ctx, side_hint=""):
    """The pattern library: the first template whose every condition is met wins.

    Guards mirror pattern.go:166 (30 bars) and pattern.go:157 (a fully matched
    template is adopted immediately -- AND semantics, first hit wins).
    """
    patterns = [item for item in (params.get("pattern_rules") or []) if item]
    if not patterns:
        return None
    if side_hint and side_hint != params["entry_side"]:
        return None
    if params.get("require_trading_day") and not ctx.get("trading_day"):
        return None
    bars = ctx.get("bars") or []
    if len(bars) < int(params["min_bars"]):
        return None
    cols = columns_from_bars(bars)

    for pattern in patterns:
        result = eval_pattern_rule(params, pattern, ctx, cols)
        if result.get("declined") or not result.get("pass"):
            continue
        return _emit(params, ctx, "pattern_template", pattern, result)
    return None


def rule_factor_composite(params, ctx, side_hint=""):
    """Factor-strategy rule (Go strategies/factor): buy on a passing composite.

    None means "no intent", and it is returned for every reason that is NOT a
    negative judgement -- missing readout, short history, an input the sandbox
    cannot obtain, a threshold not reached. Never an exception: a rule that
    cannot prove its own inputs must stay silent.
    """
    return evaluate_factor_rules(params, ctx, side_hint)


def rule_pattern_template(params, ctx, side_hint=""):
    """Pattern-strategy rule (Go strategies/pattern): buy on a full template hit."""
    return evaluate_pattern_rules(params, ctx, side_hint)


def _emit(params, ctx, kind, rule, scored):
    """Build the signal for a firing library rule, or None when no amount fits."""
    price = ctx.get("last_price")
    if price is None:
        last = last_of(ctx.get("bars") or [])
        price = last.get("close") if isinstance(last, dict) else None
    if price is None or not _finite(price) or float(price) <= ZERO:
        return None
    amount = entry_amount(params, ctx)
    if amount is None:
        return None
    signal = make_signal(params, ctx, kind, params["entry_side"], amount, float(price))
    if signal is None:
        return None
    # Attribution kept OUT of the money fields: which rule fired and at what
    # score is how the effectiveness monitoring (Go SignalCount/Win/Loss) and
    # the operator's screen read the trade back. The short code stays exactly
    # as make_signal composed it -- that is the idempotency key.
    signal["rule_id"] = str((rule or {}).get("id") or "")
    signal["score"] = float(scored.get("score") or ZERO)
    signal["reason"] = scored.get("reason") or ""
    return signal


def entry_amount(params, ctx):
    """Largest buy amount every money cap allows at once (executor rule_buy_amount).

    The caps are ANDed: available cash above the reserve, the per-order cap and
    the remaining daily quota must all allow it. None when nothing survives --
    the caller then sends nothing.
    """
    caps = []
    cash = ctx.get("available_cash")
    if cash is not None:
        caps.append(float(cash) - float(params["min_cash_reserve"]))
    caps.append(float(params["order_amount_cap"]))
    spent = ctx.get("daily_buy_total")
    if spent is not None:
        caps.append(float(params["daily_buy_cap"]) - float(spent))
    amount = min(caps)
    if amount < float(params["min_order_amount"]):
        return None
    return amount


# make_signal: NOT redefined here. The shared rule layer (local_executor.py)
# ==== RULE LAYER ====) provides the one implementation; a second copy in this module
# would silently overwrite it once the builder inlines both into one namespace.
# ==== RULE LAYER END ====


# ---------------------------------------------------------------------------
# REGISTRATION TABLE (read by scripts/build_local_executor.py by AST, never by
# exec): kind -> the rule function that answers for it. The builder inlines
# this module into both carriers and registers every kind it finds here; a
# kind with no short code in params["signal_codes"] fails the build, because
# the counter remark is the only idempotency key and an uncoded rule could
# double-send.
#
# NOTE for the builder: `make_signal` and `symbol_head` below are copies of
# the local executor's helpers, kept so this module is importable and testable
# on its own. They are byte-for-byte the same decision (same short-code
# composition, same lot flooring). If the name-collision check ever flags
# them, the resolution is to drop the local copy and call the carrier's --
# NOT to let one silently override the other by inlining order.
# ---------------------------------------------------------------------------

RULES = {
    "factor_composite": rule_factor_composite,
    "pattern_template": rule_pattern_template,
}

# Every `params` key this module reads (ctx keys are listed in CTX_KEYS below).
# The strategy builder bakes these; a missing one raises rather than defaults --
# a silently missing threshold is the "changed the knob, kept the old number" bug.
FACTOR_PARAM_KEYS = [
    "daily_buy_cap",
    "default_buy_threshold",
    "default_direction",
    "default_weight",
    "entry_side",
    "factor_defs",
    "factor_rules",
    "factor_thresholds",
    "financial_bands",
    "lot_size",
    "min_bars",
    "min_cash_reserve",
    "min_order_amount",
    "order_amount_cap",
    "pattern_rules",
    "percentile_min_samples",
    "require_trading_day",
    "scalar_bands",
    "score_domain",
    "signal_codes",
    "symbol_head_len",
]

# The ctx fields this module consumes. Everything absent is a DECLINE, never a
# zero: a zero would keep the Go weight in play on a factor that was never read.
CTX_KEYS = [
    "available_cash",
    "bars",
    "concept_heat",
    "daily_buy_total",
    "fin_pit",
    "flow_5d_pct",
    "flow_today_pct",
    "last_price",
    "news_event",
    "symbol",
    "trading_day",
    "turnover_series",
    "valuation_series",
]
