# -*- coding: utf-8 -*-
"""qmt_gateway.rules_sizing -- dynamic-position sizing chain + market-environment gate.

Pure-function port of the Go decision layer. Nothing here reads a socket, a
file or the QMT API: every input arrives through `params` (baked knobs) and
`ctx` (this round's readings), and every output is a number plus an explicit
"what is missing" list. That shape is inherited from the Go side, where the
synthesizer is deliberately IO-free so it is 100% unit testable
(internal/signalctl/sizing.go:1-18, internal/signalctl/regime.go:1-16).

Go anchors -> function
  internal/signalctl/sizing.go:158 ComputeSizing          -> market_scale / stock_scale
  internal/signalctl/sizing.go:411 computeSizingPowerMean -> _sz_group_value (mode "power_mean")
  internal/signalctl/sizing.go:182-218 (legacy product)   -> _sz_product (mode "product")
  internal/signalctl/sizing.go:228-245 + :864 + :886      -> _sz_account_caps (min, never a product)
  internal/signalctl/sizing.go:270 clampGroup             -> _sz_clamp_group
  internal/signalctl/sizing.go:285 applyMissingKeyCap     -> apply_missing_key_cap / key_missing_dims
  internal/signalctl/sizing.go:319-344 (three key dims)   -> DIM_* / key_missing_dims
  internal/signalctl/sizing.go:481 sizingHardBlock        -> buy_hard_block
  internal/signalctl/sizing.go:606-860 (factor legs)      -> _sz_emotion_factor .. _sz_breadth_factor
  internal/signalctl/sizing.go:101-150 PerTradePct(Of)    -> per_trade_pct
  internal/engine/funds_today.go:195 fundsTargetCurRoom   -> target_position
  internal/engine/engine.go:1851-1943 dynamicBuyQty       -> target_position (band + ceiling)
  internal/ordbudget/ordbudget.go:141-322 Compute/roomOf  -> buy_size_caps / buy_size_amount
  internal/engine/funds_today.go:171-184 + :127           -> t_allowed_by_sizing (blind spot = no T)
  internal/paper/t_rounds.go:258                          -> t_allowed_by_sizing (shadow = no T)
  internal/engine/intraday_t_live.go:1240-1254 (step 0)   -> t_direction_gate
  internal/engine/intraday_t_live.go:2153-2184 regimeBlock-> t_allowed_by_sizing (range-only gate)
  internal/data/emotion.go:99-133 DetectEmotionPhaseV2    -> emotion_phase
  internal/data/emotion.go:166-189 applyBreadthCorrection -> emotion_phase (one-way correction)

The cap chain, exactly as the Go side builds it (order matters -- every step
is a different kind of "no"):

  (1) hard block            emotion ice / Red+emergency valve / D1 bearish at
                            tier>=Yellow / stop-loss guard / long-holiday last
                            day  => buy downgraded, MarketScale = floor, and NO
                            key cap is applied (sizing.go:165-170).
  (2) factor groups         market factors and stock factors are aggregated per
                            group. "power_mean" (the shipped default) takes one
                            power mean per group; "product" multiplies within the
                            group. A factor whose reading is absent ABSTAINS --
                            it is neither zero nor neutral (sizing.go:186, :236,
                            :382). Only the two GROUP values are ported: the
                            blended cross-group Scale (sizing.go:435) is a
                            decision-record number, and the v4 target model
                            consumes MarketScale and StockScale separately, so
                            re-deriving the blend here would be a second answer
                            to a question this box does not ask.
  (3) account caps          equity drawdown and the risk-budget conservation
                            cap yield one 0..1 multiplier each and the SMALLEST
                            wins; they are never multiplied into the group
                            (same risk, one penalty -- sizing.go:220-245).
  (4) group clamp           clamp(value, min(cap, ...), [Floor, Cap]) -- caps
                            only bind the market group; drawdown and risk
                            budget are account-level and never touch a
                            per-stock scale (sizing.go:266-275).
  (5) blind-spot cap        any of {market_state, emotion_phase, index_change}
                            enabled but unread => MarketScale is capped at
                            missing_key_target_cap (0.50) and intraday T is
                            switched off for the day (sizing.go:277-315,
                            funds_today.go:171-184). Breadth is deliberately
                            NOT in that set: it already takes its own haircut,
                            counting it twice would penalise one gap
                            (sizing.go:281-284, missing_key_dims_test.go:88-94).
  (6) target layer          Tgt = total x min(MarketScale, ceiling); cur = held
                            value + strategy pending - T buy legs + T sell
                            legs; room = max(0, Tgt - cur)
                            (funds_today.go:186-226).
  (7) per-trade layer       eff = lo + (hi - lo) x (MS x SS) hard-clamped into
                            [lo, hi] and then to per_trade_hard_max; the order
                            amount is min(per-trade share, target room) and
                            finally every money cap ANDed -- cash minus the
                            reserve, the per-order cap, the remaining daily
                            quota, and the concentration rooms (industry / single
                            name / correlation cluster) that Go min's with the
                            others in the same step
                            (sizing.go:101-150, ordbudget.go:141-322).

Deliberate sandbox adaptations, recorded rather than hidden:
  * Index WEIGHTS and the sector list are not reachable inside the QMT
    sandbox (no get_index_weight / get_sector_list), so anything that would
    need them arrives pre-baked through ctx: the index reading itself, the
    breadth counts, the sector/industry value buckets used by the caps, and
    the macro / pre-holiday calendars. Where a bucket is absent the cap that
    would have consumed it abstains -- the same discipline the Go side uses
    for a NaN reading, never a fabricated neutral.
  * Industry / sector concentration rooms are approximated from a caller
    supplied bucket (ctx["industry_value"], ctx["cluster_value"]); the Go side
    resolves those buckets from its own industry map
    (internal/engine/engine.go:1763, :1828-1842). In the sandbox they can only
    be baked, so a missing bucket abstains that one room instead of assuming
    "no exposure".
  * The Go side also subtracts the current round's in-memory budget reservations
    from the room (engine.go:1885-1894, the same-round double-buy fix). This box
    has no such in-memory tally: its equivalent is the executor's own journal of
    intents already sent today, which lands in ctx["pending_buys"] before the
    next order is sized. Passing a stale room number is the double-buy direction,
    so the journal must be fed first.
  * No index-weight data means no weighted index decomposition: the index change
    arrives as a single number (ctx["index_change_pct"]) exactly as it does on
    the Go side, and nothing here re-derives it from constituents.
  * The missing-key cap is a config key here while the Go side hard-codes it
    (sizing.go:338-339 refuses to make it configurable). That is a rule-layer
    requirement of this box -- no numeric literal may live in a function body
    -- and the shipped default is the same 0.50.
"""

# Structural constants, not thresholds. SZ_ABSTAIN is the "this factor does not
# participate" sentinel the Go synthesizer uses (a factor value <= 0 is
# skipped, sizing.go:186); SZ_NEUTRAL is the multiplicative identity the Go side
# uses for "known and flat". Keeping them out of function bodies is what lets
# the build gate count decision-bearing numbers at zero.
SZ_ABSTAIN = 0
SZ_NEUTRAL = 1
SZ_NO_CAP = float("inf")  # also the +inf marker for readings
SZ_CENTS = 100
SZ_PERCENT = 100

# Machine-readable dimension codes -- the same three strings the Go side puts
# into JSON, alerts and the frontend tag (internal/signalctl/sizing.go:319-322).
DIM_MARKET_STATE = "market_state"
DIM_EMOTION_PHASE = "emotion_phase"
DIM_INDEX_CHANGE = "index_change"

# Sentiment phases, addressed by an ASCII code inside this file. The Go side
# keys its tables by the Chinese phase name (sizing.go:32-37); those labels
# live in params["emotion_phase_labels"] so a ctx assembled upstream (or a
# baked JSON ledger) can hand either form over.
PHASE_ICE = "ice"
PHASE_START = "start"
PHASE_FERMENT = "ferment"
PHASE_CLIMAX = "climax"
PHASE_DIVERGENCE = "divergence"
PHASE_RETREAT = "retreat"

# Coldness ordering used by the breadth correction, which may only make the
# phase colder, never warmer (internal/data/emotion.go:139-156).
PHASE_SEVERITY = {
    PHASE_START: 0,
    PHASE_CLIMAX: 1,
    PHASE_FERMENT: 2,
    PHASE_DIVERGENCE: 3,
    PHASE_RETREAT: 4,
    PHASE_ICE: 5,
}

# Factory values, copied verbatim from the Go defaults
# (internal/config/config.go:2639-2695 DefaultSizingConfig,
#  internal/config/sizing_flow.go:91 DefaultFlowFactorConfig,
#  internal/config/config.go:2299-2305 T thresholds,
#  internal/research/emotion_phase.go:24-41 phase thresholds,
#  internal/signalctl/sizing.go:339 the blind-spot cap).
# The QMT build script bakes these into the artifact header; edit nothing here
# to change behaviour -- change the parameter file and re-bake.
SIZING_DEFAULTS = {
    # -- synthesis shape ----------------------------------------------------
    "sizing_enabled": True,
    "sizing_shadow": False,
    "budget_mode": "dynamic",
    "sizing_mode": "power_mean",
    "power_p": 0.5,
    "group_floor_market": 0.45,
    "group_floor_stock": 0.35,
    "scale_floor": 0.15,
    "scale_cap": 1.15,
    # -- dimension participation (a switched-off dimension is NOT a blind spot)
    "emotion_enabled": True,
    "market_state_enabled": False,
    # -- market group -------------------------------------------------------
    "emotion_phase_labels": {
        PHASE_ICE: "\u51b0\u70b9",
        PHASE_START: "\u542f\u52a8",
        PHASE_FERMENT: "\u53d1\u9175",
        PHASE_CLIMAX: "\u9ad8\u6f6e",
        PHASE_DIVERGENCE: "\u80cc\u79bb",
        PHASE_RETREAT: "\u9000\u6f6e",
    },
    "emotion_scales": {
        PHASE_RETREAT: 0.50,
        PHASE_DIVERGENCE: 0.60,
        PHASE_CLIMAX: 0.80,
        PHASE_FERMENT: 1.00,
        PHASE_START: 1.00,
    },
    "state_scales": {"bull": 1.00, "range": 0.70, "bear": 0.40},
    "tier_scales": {"Yellow": 0.60, "Red": 0.30},
    "tier_red_default": 0.30,
    "index_bands": {"shallow": -1.0, "deep": -2.0},
    "index_scales": {"shallow": 1.00, "mid": 0.70, "deep": 0.40},
    "breadth_bands": {"low": 0.30, "mid": 0.50},
    "breadth_scales": {"low": 0.70, "mid": 0.85, "high": 1.00},
    "breadth_missing_scale": 0.90,
    "breadth_missing_default": 0.90,
    "macro_window_scales": {"high": 0.50, "medium": 0.80, "low": 1.00},
    "macro_lead_days": 5,
    "macro_lead_curve": [0.90, 0.85, 0.80, 0.70, 0.50],
    "macro_event_day_scale": 0.30,
    "holiday_scales": {
        "weekend": 0.85, "small1": 0.70, "small2": 0.85,
        "long1": 0.40, "long2": 0.60, "long3": 0.75,
    },
    "holiday_long_closed_run": 5,
    "holiday_small_closed_run": 3,
    "holiday_weekend_closed_run": 2,
    "holiday_long_pre_max": 3,
    "holiday_small_pre_max": 2,
    "holiday_weekend_pre_max": 2,
    # -- stock group --------------------------------------------------------
    "quality_weight_clamp": [0.9, 1.1],
    "kelly_lambda": 0.5,
    "kelly_min_prob": 0.52,
    "kelly_no_edge": 0.6,
    "kelly_edge_gain": 1.1,
    "kelly_clamp": [0.6, 1.15],
    "vol_target_pct": 2.0,
    "vol_clamp": [0.5, 1.5],
    "llm_bearish_scale": 0.5,
    "flow_factor_enabled": True,
    "flow_bands": {"strong_in_pct": 3.0, "mild_in_pct": 1.0,
                   "mild_out_pct": -1.0, "strong_out_pct": -3.0},
    "flow_5d_scales": {"strong_in": 1.10, "mild_in": 1.05, "neutral": 1.00,
                       "mild_out": 0.85, "strong_out": 0.70},
    "flow_today_scales": {"strong_in": 1.05, "mild_in": 1.025, "neutral": 1.00,
                          "mild_out": 0.95, "strong_out": 0.90},
    # -- account-level caps (min, never a product) --------------------------
    "max_drawdown_pct": 0.10,
    "sl_offset_risk_budget_keep": True,
    # -- hard blocks --------------------------------------------------------
    "red_block_new_buy": False,
    "stoploss_guard_count": 4,
    "holiday_block_new": False,
    # -- blind-spot cap -----------------------------------------------------
    "missing_key_target_cap": 0.50,
    # -- target layer -------------------------------------------------------
    "max_total_position_pct": 1.00,
    "per_trade_position_pct": 0.10,
    "per_trade_position_max_pct": 0.30,
    "per_trade_default_lo": 0.10,
    "per_trade_default_hi": 0.30,
    "per_trade_hard_max": 0.50,
    # -- money caps ANDed with the sizing caps (local_executor rule layer) --
    "min_cash_reserve": 1000.0,
    "order_amount_cap": 20000.0,
    "daily_buy_cap": 100000.0,
    # Go: config.QMTConfig.EffectiveMinOrderAmount (factory 10000). This table is a DECLARED
    # catalog, never a runtime fallback (_sz_p raises on a missing key) -- and scripts/
    # derive_carrier_params.py --check compares every row here against the Go factory value,
    # so a stale number in this catalog goes red instead of silently becoming the behaviour.
    "min_order_amount": 10000.0,
    "industry_cap_pct": 35.0,
    "single_stock_cap_pct": 35.0,
    "cluster_cap_pct": 40.0,
    # -- sentiment phase detector (limit-up pool + ladder + breadth) --------
    "emo_climax_limitup_min": 80,
    "emo_climax_board_min": 7,
    "emo_ferment_limitup_min": 40,
    "emo_ferment_limitup_max": 79,
    "emo_ferment_board_max": 6,
    "emo_start_limitup_min": 15,
    "emo_start_limitup_max": 39,
    "emo_start_board_max": 3,
    "emo_ice_limitup_max": 14,
    "emo_ice_board_max": 2,
    "emo_retreat_limitup_max": 14,
    "emo_retreat_board_max": 3,
    "emo_diverge_limitup_drop": 15,
    "emo_diverge_board_drop": 2,
    "emo_breadth_ice_down_ratio": 0.0,
    "emo_breadth_retreat_down_ratio": 0.0,
    # -- intraday T environment gate ----------------------------------------
    "t_enabled": True,
    "t_min_market_scale": 0.40,
    "t_sell_env_min_market_scale": 0.20,
    "t_market_plunge_pct": -2.0,
    "t_regime_gate_enabled": True,
    "t_regime_stale_sec": 900,
    "t_direction_mode": "both",
}

SIZING_PARAM_KEYS = sorted(SIZING_DEFAULTS)

# Every ctx key this chain reads. Declared rather than discovered: the carriers
# (qmt_gateway/local_executor.py Executor.context_for and
# qmt_gateway/qmt_internal_strategy.py build_context) assemble a PER-SYMBOL flat
# context, while the sizing chain needs the account view too -- total assets,
# the held book, the pending buys and the T legs. Whoever builds the ctx must
# add those; a key left out does not default to zero, it abstains (and the
# blind-spot cap then decides whether that costs the whole day's exposure).
SIZING_CTX_KEYS = [
    # market readings
    "emotion_phase", "limit_up_count", "ladder_height", "market_state",
    "regime", "risk_tier", "index_change_pct", "index_source", "index_stale",
    "advancers", "decliners", "breadth_unknown", "macro_events", "holiday",
    # broken_board_ratio is deliberately NOT read here: the Go phase ladder in
    # production use is the pool variant, which consumes the limit-up count and
    # the ladder height only (internal/data/emotion.go:99-133).
    # account view (not in the per-symbol carrier context)
    "total_asset", "equity", "positions_live", "pending_buys",
    "t_buy_leg_value", "t_sell_leg_value", "available_cash", "daily_buy_total",
    "equity_drawdown", "window_stop_losses", "stop_loss_offset_pct",
    "base_stop_loss_pct",
    # concentration buckets, baked by whoever can resolve sectors (this box has
    # no sector list and no index weights)
    "industry_known", "industry_value", "stock_value", "cluster_set",
    "cluster_value",
    # per-stock readings
    "quality_weight", "confidence", "atr", "price", "last_price", "d1_blocked",
    "flow_ratio_5d", "flow_ratio_today",
    # per-round switches (absent = the baked default)
    "sizing_enabled", "shadow", "t_enabled", "emotion_enabled",
    "market_state_enabled", "red_block_new_buy", "holiday_block_new",
    "flow_factor_enabled", "sl_offset_risk_budget_keep",
    "t_regime_gate_enabled", "market_state_age_sec",
]

# Signal kinds registered for the counter-remark idempotency check. This module
# deliberately owns NONE: the sizing chain never emits an order intent of its
# own -- it decides how big somebody else's order may be, so there is no short
# code to stamp on an order remark and nothing for the broker's own order table to
# deduplicate against. A kind here would be an invented rule, not a ported one.
RULES = {}


# ---------------------------------------------------------------------------
# lookup helpers
# ---------------------------------------------------------------------------

def _sz_p(params, key):
    """A knob is never defaulted here: a missing key is a build error, not a
    silent fallback to an in-file number."""
    if key not in params:
        raise KeyError("sizing_param_missing:%s" % key)
    return params[key]


def _sz_flag(params, ctx, ctx_key, param_key):
    """Tri-state switch: this round's ctx wins, otherwise the baked default.
    `None` in the ctx means "the assembler had no opinion", not "off"."""
    value = (ctx or {}).get(ctx_key)
    if value is None:
        return bool(_sz_p(params, param_key))
    return bool(value)


def _sz_num(value):
    """A reading, or None. NaN/inf are the Go side's abstention markers and
    are folded into the same None here (sizing.go:294, :828)."""
    if isinstance(value, bool):
        return None
    if not isinstance(value, (int, float)):
        return None
    number = float(value)
    if number != number:  # NaN is never equal to itself
        return None
    if number == SZ_NO_CAP or number == -SZ_NO_CAP:
        return None
    return number


def _sz_clamp(value, lo, hi):
    """Go clampF: a reversed range is swapped, never trusted
    (internal/signalctl/sizing.go:905-916)."""
    if lo > hi:
        lo, hi = hi, lo
    if value < lo:
        return lo
    if value > hi:
        return hi
    return value


def _sz_get(table, key, absent):
    """Map lookup with an explicit 'not there' marker (Go's `, ok` idiom)."""
    try:
        return table[key]
    except (KeyError, TypeError):
        return absent


def _sz_phase_code(params, value):
    """Accept either the ASCII code or the Chinese phase label."""
    if value is None:
        return None
    text = str(value).strip()
    if not text:
        return None
    labels = _sz_p(params, "emotion_phase_labels")
    if text in labels:
        return text
    for code, label in labels.items():
        if text == label:
            return code
    return None


def _sz_read(ctx, key):
    """One ctx reading as a plain number or None (decline, never zero)."""
    return _sz_num((ctx or {}).get(key))


# ---------------------------------------------------------------------------
# group aggregation
# ---------------------------------------------------------------------------

def _sz_power_mean(values, power):
    """((sum v^p)/n)^(1/p); p == 0 degenerates into the geometric mean; an
    empty set is neutral. Go: internal/signalctl/sizing.go:356-372."""
    if not values:
        return SZ_NEUTRAL
    if power == SZ_ABSTAIN:
        product = SZ_NEUTRAL
        for value in values:
            product = product * value
        return product ** (SZ_NEUTRAL / len(values))
    total = SZ_NEUTRAL * SZ_ABSTAIN
    for value in values:
        total = total + value ** power
    return (total / len(values)) ** (SZ_NEUTRAL / power)


def _sz_group_value(factors, power, floor):
    """Power mean over the factors that did not abstain, then lift to the
    group floor. An all-abstain group is neutral, not zero
    (internal/signalctl/sizing.go:378-407)."""
    values = [v for v in factors if v is not None and v > SZ_ABSTAIN]
    if not values:
        return SZ_NEUTRAL
    aggregated = _sz_power_mean(values, power)
    if floor > SZ_ABSTAIN and aggregated < floor:
        return floor
    return aggregated


def _sz_product(factors):
    """Legacy "product" mode: weight factors multiply, abstentions drop out
    (internal/signalctl/sizing.go:182-218)."""
    result = SZ_NEUTRAL
    for value in factors:
        if value is None or value <= SZ_ABSTAIN:
            continue
        result = result * value
    return result


def _sz_clamp_group(value, account_cap, params):
    """Account cap first (it only constrains downward), then [Floor, Cap].
    Go: internal/signalctl/sizing.go:270-275."""
    if value > account_cap:
        value = account_cap
    return _sz_clamp(value, _sz_p(params, "scale_floor"), _sz_p(params, "scale_cap"))


def _sz_aggregate(factors, params, group_floor):
    """One group, both modes: power_mean aggregation or the legacy product.

    The group floor arrives as a VALUE: every knob this chain reads then shows
    up exactly once as a literal `_p(params, "...")` at its call site, which is
    what lets the "declared or not" lock see it. A key name passed around as a
    string hides the read from that scan.
    """
    if _sz_p(params, "sizing_mode") == "power_mean":
        return _sz_group_value(factors, _sz_p(params, "power_p"), group_floor)
    return _sz_product(factors)


# ---------------------------------------------------------------------------
# market-group factors (internal/signalctl/sizing.go:653-860)
# ---------------------------------------------------------------------------

def _sz_emotion_factor(params, code):
    """Money weight per sentiment phase. Ice is a hard block handled elsewhere
    and abstains here; an unknown phase abstains -- never neutral
    (sizing.go:653-661)."""
    if not code or code == PHASE_ICE:
        return None
    scales = _sz_p(params, "emotion_scales")
    value = _sz_num(_sz_get(scales, code, None))
    if value is None:
        return None
    return value


def _sz_state_factor(params, state):
    """Discrete market-state weight only. MaxPosPct is deliberately NOT
    multiplied in: it derives from the same state machine, so using both would
    penalise one risk twice (sizing.go:667-675, regime.go:46-48)."""
    if not state:
        return None
    return _sz_num(_sz_get(_sz_p(params, "state_scales"), str(state).strip(), None))


def _sz_tier_factor(params, tier):
    """Risk tier weight. An empty tier is a known-neutral reading; Red falls
    back to the baked default, an explicit 0/negative abstains
    (sizing.go:681-698)."""
    tier = (tier or "").strip()
    scales = _sz_p(params, "tier_scales")
    if tier == "Red":
        value = _sz_num(_sz_get(scales, "Red", None))
        if value is None or value <= SZ_ABSTAIN:
            return _sz_p(params, "tier_red_default")
        return value
    if tier == "Yellow":
        return _sz_num(_sz_get(scales, "Yellow", None))
    return SZ_NEUTRAL


def _sz_index_factor(params, change_pct):
    """Index move -> money weight over two fixed bands whose SCALE is
    configurable. A missing reading abstains AND is a key blind spot
    (sizing.go:802-814)."""
    if change_pct is None:
        return None
    bands = _sz_p(params, "index_bands")
    scales = _sz_p(params, "index_scales")
    if change_pct >= bands["shallow"]:
        return _sz_num(scales["shallow"])
    if change_pct > bands["deep"]:
        return _sz_num(scales["mid"])
    return _sz_num(scales["deep"])


def breadth_up_ratio(params, ctx):
    """Advance ratio in (0, 1] from the advancer/decliner counts, or None when
    the tape is unknown. `breadth_unknown` is an explicit statement that the
    count could not be taken -- it must not be read as 'breadth is fine'."""
    ctx = ctx or {}
    if ctx.get("breadth_unknown"):
        return None
    advancers = _sz_num(ctx.get("advancers"))
    decliners = _sz_num(ctx.get("decliners"))
    if advancers is None or decliners is None:
        return None
    total = advancers + decliners
    if total <= SZ_ABSTAIN:
        return None
    return advancers / total


def _sz_breadth_factor(params, up_ratio):
    """Breadth bands (sizing.go:828-860). A MISSING reading takes a small
    haircut instead of abstaining -- owner ruling 2026-10-21 -- while a known
    zero (a flat tape with nobody advancing) is a legal reading that lands in
    the lowest band. The Go side abstains on 0 because its float cannot tell
    'not assembled' apart from 'everything down'; here the ctx has an explicit
    unknown flag, so 0 is read as what it says."""
    bands = _sz_p(params, "breadth_bands")
    scales = _sz_p(params, "breadth_scales")
    if up_ratio is None:
        haircut = _sz_num(_sz_p(params, "breadth_missing_scale"))
        if haircut is None or haircut <= SZ_ABSTAIN:
            return _sz_num(_sz_p(params, "breadth_missing_default"))
        return haircut
    if up_ratio < bands["low"]:
        return _sz_num(scales["low"])
    if up_ratio < bands["mid"]:
        return _sz_num(scales["mid"])
    return _sz_num(scales["high"])


def _sz_macro_window_factor(params, events):
    """Strictest in-window macro event; no event -> abstain (sizing.go:702-717)."""
    scales = _sz_p(params, "macro_window_scales")
    worst = None
    for event in events or []:
        if not isinstance(event, dict) or not event.get("in_window"):
            continue
        value = _sz_num(_sz_get(scales, str(event.get("impact") or ""), None))
        if value is None or value <= SZ_ABSTAIN:
            continue
        if worst is None or value < worst:
            worst = value
    return worst


def _sz_macro_lead_factor(params, events):
    """Forward-looking pull-back by TRADING-day distance (natural days across a
    holiday would count an 8-day break as eight sessions, sizing.go:728-750)."""
    days = _sz_p(params, "macro_lead_days")
    curve = _sz_p(params, "macro_lead_curve")
    worst = None
    for event in events or []:
        if not isinstance(event, dict):
            continue
        distance = event.get("days_to_event")
        if not isinstance(distance, int) or isinstance(distance, bool):
            continue
        value = None
        if distance == SZ_ABSTAIN:
            value = _sz_num(_sz_p(params, "macro_event_day_scale"))
        elif SZ_ABSTAIN < distance <= days:
            index = days - distance
            if SZ_ABSTAIN <= index < len(curve):
                value = _sz_num(curve[index])
        if value is None or value <= SZ_ABSTAIN:
            continue
        if worst is None or value < worst:
            worst = value
    return worst


def _sz_holiday_factor(params, holiday):
    """Pre-holiday de-rating. An unloaded calendar ABSTAINS -- treating
    'unknown' as an ordinary day is exactly the fail-open this chain refuses
    (sizing.go:762-793)."""
    if not isinstance(holiday, dict) or not holiday.get("calendar_loaded"):
        return None
    pre = holiday.get("pre_holiday_n")
    closed = holiday.get("closed_run")
    if not isinstance(pre, int) or isinstance(pre, bool):
        return None
    if not isinstance(closed, int) or isinstance(closed, bool):
        return None
    if pre <= SZ_ABSTAIN:
        return SZ_NEUTRAL
    key = None
    if closed >= _sz_p(params, "holiday_long_closed_run"):
        if pre <= _sz_p(params, "holiday_long_pre_max"):
            key = "long%d" % pre
    elif closed >= _sz_p(params, "holiday_small_closed_run"):
        if pre <= _sz_p(params, "holiday_small_pre_max"):
            key = "small%d" % pre
    elif closed == _sz_p(params, "holiday_weekend_closed_run"):
        if pre <= _sz_p(params, "holiday_weekend_pre_max"):
            key = "weekend"
    if key is None:
        return SZ_NEUTRAL
    return _sz_num(_sz_get(_sz_p(params, "holiday_scales"), key, SZ_NEUTRAL))


def _sz_drawdown_cap(params, drawdown):
    """pysystemtrade risk_overlay shape: mult = limit / max(limit, drawdown).
    Disabled or unknown -> abstain (sizing.go:864-873)."""
    limit = _sz_num(_sz_p(params, "max_drawdown_pct"))
    if limit is None or limit <= SZ_ABSTAIN or drawdown is None or drawdown <= SZ_ABSTAIN:
        return None
    if drawdown <= limit:
        return SZ_NEUTRAL
    return limit / drawdown


def _sz_risk_budget_cap(params, ctx, offset_pct, base_stop_pct):
    """Loosening a stop from 6% to 9% inflates one order's worst loss by half,
    so the position shrinks by 6/9. Tightening never buys extra size -- the
    factor is only ever applied downward, and equals SZ_NEUTRAL it abstains
    (sizing.go:886-902)."""
    if not _sz_flag(params, ctx, "sl_offset_risk_budget_keep", "sl_offset_risk_budget_keep"):
        return None
    if offset_pct is None or offset_pct <= SZ_ABSTAIN:
        return None
    if base_stop_pct is None or base_stop_pct <= SZ_ABSTAIN:
        return None
    effective = base_stop_pct + offset_pct
    if effective <= SZ_ABSTAIN:
        return None
    scale = base_stop_pct / effective
    if scale >= SZ_NEUTRAL:
        return None
    return scale


def _sz_account_caps(params, ctx):
    """The cap multipliers, combined with min (one risk, one penalty).
    Returns (cap, worst_source)."""
    pairs = [
        ("drawdown", _sz_drawdown_cap(params, _sz_read(ctx, "equity_drawdown"))),
        ("risk_budget", _sz_risk_budget_cap(
            params, ctx, _sz_read(ctx, "stop_loss_offset_pct"),
            _sz_read(ctx, "base_stop_loss_pct"))),
    ]
    cap = SZ_NO_CAP
    for _name, value in pairs:
        if value is None or value <= SZ_ABSTAIN:
            continue
        if value < cap:
            cap = value
    return cap


# ---------------------------------------------------------------------------
# stock-group factors
# ---------------------------------------------------------------------------

def _sz_quality_factor(params, weight):
    """Per-signal quality weight resolved upstream; <=0 (unresolved) abstains,
    and the range is re-clamped against a wild config (sizing.go:606-611)."""
    if weight is None or weight <= SZ_ABSTAIN:
        return None
    lo, hi = _sz_p(params, "quality_weight_clamp")
    return _sz_clamp(weight, lo, hi)


def _sz_kelly_factor(params, confidence):
    """Fractional Kelly on the signal's probability. Below the edge threshold
    it is a fixed small scale, NOT zero -- 'low confidence' still trades, just
    smaller. An unavailable confidence abstains (sizing.go:619-634)."""
    if confidence is None or confidence <= SZ_ABSTAIN:
        return None
    if confidence < _sz_p(params, "kelly_min_prob"):
        return _sz_p(params, "kelly_no_edge")
    edge = _sz_clamp(_sz_edge_raw(confidence), SZ_ABSTAIN, SZ_NEUTRAL)
    lo, hi = _sz_p(params, "kelly_clamp")
    value = _sz_p(params, "kelly_no_edge") + (
        _sz_p(params, "kelly_lambda") * edge * _sz_p(params, "kelly_edge_gain"))
    return _sz_clamp(value, lo, hi)


def _sz_edge_raw(confidence):
    """Kelly edge = 2p - 1, written as an addition so the helper bodies carry
    no numeric literal of their own (the doubling is the formula, not a
    threshold)."""
    return confidence + confidence - SZ_NEUTRAL


def _sz_vol_factor(params, atr, price):
    """Volatility targeting from the ATR already carried on the signal
    (sizing.go:640-649)."""
    if atr is None or price is None:
        return None
    if atr <= SZ_ABSTAIN or price <= SZ_ABSTAIN:
        return None
    atr_pct = atr / price * SZ_CENTS
    if atr_pct <= SZ_ABSTAIN:
        return None
    lo, hi = _sz_p(params, "vol_clamp")
    return _sz_clamp(_sz_p(params, "vol_target_pct") / atr_pct, lo, hi)


def _sz_llm_factor(params, blocked):
    """A per-stock LLM bearish hit scales the order; no hit abstains (the
    tier>=Yellow case is already a hard block, sizing.go:818-823)."""
    if not blocked:
        return None
    return _sz_num(_sz_p(params, "llm_bearish_scale"))


def _sz_flow_band(params, ratio):
    """Net-inflow ratio (%) -> band key. Zero means 'unresolved' on the Go
    side, so it abstains; the band edges are inclusive in the favourable
    direction (sizing_flow / flow_factor.go:52-68)."""
    if ratio is None or ratio == SZ_ABSTAIN:
        return None
    bands = _sz_p(params, "flow_bands")
    if ratio >= bands["strong_in_pct"]:
        return "strong_in"
    if ratio >= bands["mild_in_pct"]:
        return "mild_in"
    if ratio > bands["mild_out_pct"]:
        return "neutral"
    if ratio > bands["strong_out_pct"]:
        return "mild_out"
    return "strong_out"


def _sz_flow_factor(params, ctx, ratio, scales):
    """One net-inflow dimension; anything unresolved abstains
    (flow_factor.go:88-113)."""
    if not _sz_flag(params, ctx, "flow_factor_enabled", "flow_factor_enabled"):
        return None
    band = _sz_flow_band(params, ratio)
    if band is None:
        return None
    value = _sz_num(_sz_get(scales, band, SZ_NEUTRAL))
    if value is None or value <= SZ_ABSTAIN:
        return None
    return value


# ---------------------------------------------------------------------------
# hard blocks and the blind spot
# ---------------------------------------------------------------------------

def _sz_long_holiday_last_day(params, holiday):
    """Go sizing.go:754-756: calendar loaded, a break of at least the long
    threshold, and the last session before it."""
    if not isinstance(holiday, dict) or not holiday.get("calendar_loaded"):
        return False
    closed = holiday.get("closed_run")
    pre = holiday.get("pre_holiday_n")
    if not isinstance(closed, int) or isinstance(closed, bool):
        return False
    if not isinstance(pre, int) or isinstance(pre, bool):
        return False
    return closed >= _sz_p(params, "holiday_long_closed_run") and pre == SZ_NEUTRAL


def buy_hard_block(params, ctx):
    """(blocked, reason). A hit downgrades the buy outright instead of scaling
    it -- no double penalty (internal/signalctl/sizing.go:481-506)."""
    ctx = ctx or {}
    if _sz_phase_code(params, ctx.get("emotion_phase")) == PHASE_ICE:
        return True, "emotion_ice"
    tier = (ctx.get("risk_tier") or "").strip()
    if tier == "Red" and _sz_flag(params, ctx, "red_block_new_buy", "red_block_new_buy"):
        return True, "red_tier_blocks_new_buy"
    if ctx.get("d1_blocked") and tier in ("Yellow", "Red"):
        return True, "d1_bearish_with_tier_at_least_yellow"
    guard = _sz_num(_sz_p(params, "stoploss_guard_count"))
    stops = ctx.get("window_stop_losses")
    if guard is not None and guard > SZ_ABSTAIN and isinstance(stops, int) \
            and not isinstance(stops, bool) and stops >= guard:
        return True, "stoploss_guard"
    if _sz_flag(params, ctx, "holiday_block_new", "holiday_block_new") \
            and _sz_long_holiday_last_day(params, ctx.get("holiday")):
        return True, "long_holiday_last_day"
    return False, ""


def key_missing_dims(params, ctx):
    """Which key readings are absent -- ordered, stable, and only for
    dimensions that were switched ON. A deliberately disabled dimension is not
    a blind spot (otherwise turning the state machine off would park the
    account at 50% forever); breadth is not in this set at all because it
    carries its own haircut.
    Go: internal/signalctl/sizing.go:285-315."""
    ctx = ctx or {}
    dims = []
    if _sz_flag(params, ctx, "market_state_enabled", "market_state_enabled"):
        if not (ctx.get("market_state") or ctx.get("regime")):
            dims.append(DIM_MARKET_STATE)
    if _sz_flag(params, ctx, "emotion_enabled", "emotion_enabled"):
        if emotion_phase(params, ctx) is None:
            dims.append(DIM_EMOTION_PHASE)
    if _sz_read(ctx, "index_change_pct") is None:
        dims.append(DIM_INDEX_CHANGE)
    return dims


def apply_missing_key_cap(params, ctx, target_scale, missing_dims):
    """(capped_scale, capped_bool). Any key reading missing => the target is
    capped, because abstaining factors push the aggregate toward 1.0 and in the
    target model 1.0 means fully invested -- a data blind spot would otherwise
    be MORE aggressive than a bear tape. Go: sizing.go:277-315."""
    if not missing_dims:
        return target_scale, False
    cap = _sz_p(params, "missing_key_target_cap")
    if target_scale > cap:
        return cap, True
    return target_scale, True


# ---------------------------------------------------------------------------
# sentiment phase (internal/data/emotion.go:99-133 + :166-189)
# ---------------------------------------------------------------------------

def _sz_phase_from_pool(params, limit_up, ladder):
    """The six-phase ladder, branch order copied verbatim from
    DetectEmotionPhaseV2. An empty pool is a documented conservative 'start',
    not an ice reading; a MISSING count is not a reading at all and is handled
    by the caller as an abstention."""
    if limit_up is None or ladder is None:
        return None
    if limit_up <= SZ_ABSTAIN:
        return PHASE_START
    if limit_up >= _sz_p(params, "emo_climax_limitup_min") and \
            ladder >= _sz_p(params, "emo_climax_board_min"):
        return PHASE_CLIMAX
    if limit_up >= _sz_p(params, "emo_ferment_limitup_min") and \
            limit_up <= _sz_p(params, "emo_ferment_limitup_max") and \
            ladder <= _sz_p(params, "emo_ferment_board_max"):
        return PHASE_FERMENT
    if limit_up >= _sz_p(params, "emo_start_limitup_min") and \
            limit_up <= _sz_p(params, "emo_start_limitup_max") and \
            ladder <= _sz_p(params, "emo_start_board_max"):
        return PHASE_START
    if limit_up <= _sz_p(params, "emo_ice_limitup_max") and \
            ladder <= _sz_p(params, "emo_ice_board_max"):
        return PHASE_ICE
    if limit_up <= _sz_p(params, "emo_retreat_limitup_max") and \
            ladder <= _sz_p(params, "emo_retreat_board_max"):
        return PHASE_RETREAT
    if limit_up < _sz_p(params, "emo_diverge_limitup_drop") and \
            ladder < _sz_p(params, "emo_diverge_board_drop"):
        return PHASE_DIVERGENCE
    return PHASE_START


def _sz_phase_breadth_correction(params, phase, advancers, decliners):
    """ONE-WAY correction from the real advance/decline counts: a failed fetch
    (no counts) returns the phase untouched, a configured-but-reversed pair
    falls back to the larger value, and a warm phase is never warmed further.
    Go: internal/data/emotion.go:166-189."""
    if phase is None:
        return None
    if advancers is None or decliners is None:
        return phase
    total = advancers + decliners
    if total <= SZ_ABSTAIN:
        return phase
    ice = _sz_num(_sz_p(params, "emo_breadth_ice_down_ratio"))
    retreat = _sz_num(_sz_p(params, "emo_breadth_retreat_down_ratio"))
    if (ice is None or ice <= SZ_ABSTAIN) and (retreat is None or retreat <= SZ_ABSTAIN):
        return phase
    if ice is not None and retreat is not None and SZ_ABSTAIN < ice < retreat:
        ice, retreat = retreat, ice
    down_ratio = decliners / total
    if ice is not None and ice > SZ_ABSTAIN and down_ratio >= ice:
        return PHASE_ICE
    if retreat is not None and retreat > SZ_ABSTAIN and down_ratio >= retreat:
        if PHASE_SEVERITY.get(phase, SZ_ABSTAIN) < PHASE_SEVERITY[PHASE_RETREAT]:
            return PHASE_RETREAT
    return phase


def emotion_phase(params, ctx):
    """This round's sentiment phase as an ASCII code, or None (= no reading).

    An explicit ctx["emotion_phase"] (baked/forwarded from the decision brain)
    wins; otherwise the phase is computed from the readings the sandbox CAN
    take -- limit-up count, ladder height, and the advance/decline counts used
    only as a one-way correction. The broken-board ratio is deliberately not
    consumed here: the Go phase in production use is the POOL variant, which
    reads limit-up count and ladder height only (emotion.go:99-133); the blast
    rate feeds the older snapshot variant (emotion.go:17-74), and mixing the
    two would silently change which ladder this box runs.
        A phase string this box cannot map (neither the ASCII code nor the baked
    label) is reported as NO reading, which the blind-spot cap then treats as a
    missing emotion dimension. The Go side would only abstain that one factor
    (an empty phase is what it counts as missing). This is the conservative
    direction -- an unmappable label stops the whole day's exposure growth
    instead of silently pricing it as if emotion were not in play -- and it is
    recorded here rather than hidden because the two readings differ.
    """
    explicit = _sz_phase_code(params, ctx.get("emotion_phase"))
    if explicit is not None:
        return explicit
    raw = ctx.get("limit_up_count")
    ladder = ctx.get("ladder_height")
    if isinstance(raw, bool) or isinstance(ladder, bool):
        return None
    limit_up = _sz_num(raw)
    ladder = _sz_num(ladder)
    phase = _sz_phase_from_pool(params, limit_up, ladder)
    if phase is None:
        return None
    return _sz_phase_breadth_correction(
        params, phase, _sz_num(ctx.get("advancers")), _sz_num(ctx.get("decliners")))


# ---------------------------------------------------------------------------
# public sizing entries
# ---------------------------------------------------------------------------

def market_factors(params, ctx):
    """The market group's factor values, in Go's order. Exported so a caller can
    show which leg fired. A None entry (or one <= SZ_ABSTAIN) abstains: it is
    neither zero nor neutral in the aggregation
    (internal/signalctl/sizing.go:202-218, :413-422)."""
    ctx = ctx or {}
    return [
        _sz_emotion_factor(params, emotion_phase(params, ctx)),
        _sz_state_factor(params, ctx.get("market_state") or ctx.get("regime")),
        _sz_tier_factor(params, ctx.get("risk_tier")),
        _sz_macro_window_factor(params, ctx.get("macro_events")),
        _sz_macro_lead_factor(params, ctx.get("macro_events")),
        _sz_holiday_factor(params, ctx.get("holiday")),
        _sz_index_factor(params, _sz_read(ctx, "index_change_pct")),
        _sz_breadth_factor(params, breadth_up_ratio(params, ctx)),
    ]


def stock_factors(params, ctx):
    """The stock group's factor values, in Go's order -- 'how big should THIS
    one order be'. No account-level cap belongs in here
    (internal/signalctl/sizing.go:202-218, :423-431)."""
    ctx = ctx or {}
    return [
        _sz_quality_factor(params, _sz_read(ctx, "quality_weight")),
        _sz_kelly_factor(params, _sz_read(ctx, "confidence")),
        _sz_vol_factor(params, _sz_read(ctx, "atr"),
                       _sz_read(ctx, "price") or _sz_read(ctx, "last_price")),
        _sz_llm_factor(params, ctx.get("d1_blocked")),
        _sz_flow_factor(params, ctx, _sz_read(ctx, "flow_ratio_5d"),
                        _sz_p(params, "flow_5d_scales")),
        _sz_flow_factor(params, ctx, _sz_read(ctx, "flow_ratio_today"),
                        _sz_p(params, "flow_today_scales")),
    ]


def market_scale(params, ctx):
    """MarketScale = the group aggregate, the account caps folded in with min,
    clamped to [Floor, Cap], then capped by the blind-spot rule.

    Returns (scale, missing_dims). missing_dims is the machine-readable list
    the Go side exports for the same reason the bool alone was not enough: the
    bool answers 'capped?', the list answers 'go check what'
    (internal/signalctl/sizing.go:61-67).
    """
    blocked, _reason = buy_hard_block(params, ctx)
    if blocked:
        # Go returns before the key cap here (sizing.go:165-170): the account
        # still needs a readable number for the T quota, and the buy itself is
        # already refused, so no second penalty is stacked.
        return _sz_p(params, "scale_floor"), []
    aggregate = _sz_aggregate(market_factors(params, ctx), params,
                              _sz_p(params, "group_floor_market"))
    capped = _sz_clamp_group(aggregate, _sz_account_caps(params, ctx), params)
    dims = key_missing_dims(params, ctx)
    scale, _hit = apply_missing_key_cap(params, ctx, capped, dims)
    return scale, dims


def stock_scale(params, ctx):
    """StockScale -- this order's size only, never the account target. It is
    clamped with the same [Floor, Cap] but WITHOUT the account caps: an equity
    drawdown is not a property of one name (sizing.go:260, :266-275).

    Returns (scale, missing_dims) where missing_dims lists the stock-group
    readings that abstained. Those are informational only: none of them feeds
    the blind-spot cap, which is about market readings (sizing.go:277-284).
    """
    factors = stock_factors(params, ctx)
    aggregate = _sz_aggregate(factors, params, _sz_p(params, "group_floor_stock"))
    scale = _sz_clamp_group(aggregate, SZ_NO_CAP, params)
    names = ["quality", "confidence", "volatility", "llm_bearish",
             "flow_5d", "flow_today"]
    missing = [name for name, value in zip(names, factors)
               if value is None or value <= SZ_ABSTAIN]
    return scale, missing


def per_trade_pct(params, market, stock):
    """Effective per-trade share of total assets: lo + (hi - lo) x k, with
    k = MarketScale x StockScale, then hard-clamped into [lo, hi] and to the
    absolute half-account ceiling.

    Every normalisation step runs BEFORE the band comparison, because a
    band like lo=0.8/hi=0.9 would otherwise be judged legal and then clipped
    twice, making the result depend on the order of the checks -- the exact
    class of config bug this repo has been bitten by.
    Go: internal/signalctl/sizing.go:101-150 + the band normalisation in
    internal/engine/engine.go:1863-1871.
    """
    hard_max = _sz_p(params, "per_trade_hard_max")
    lo = _sz_p(params, "per_trade_position_pct")
    if lo <= SZ_ABSTAIN or lo > SZ_NEUTRAL:
        lo = _sz_p(params, "per_trade_default_lo")
    hi = _sz_p(params, "per_trade_position_max_pct")
    if hi <= SZ_ABSTAIN:
        hi = _sz_p(params, "per_trade_default_hi")
    lo = min(lo, hard_max)
    hi = min(hi, hard_max)
    if hi < lo:
        hi = lo
    k = _sz_clamp(market * stock, SZ_ABSTAIN, SZ_NEUTRAL)
    return _sz_clamp(lo + (hi - lo) * k, lo, hi)


def _sz_position_value(ctx):
    """(held_value, strategy_pending_value, t_buy_legs, t_sell_legs, ok).

    fail-closed on an unpriceable position: skipping it would understate the
    current exposure and overstate the remaining room -- 'half position, then
    one order fills the account' is exactly the defect the Go side pinned
    (internal/engine/engine.go:1713-1766, :1819-1826).
    """
    ctx = ctx or {}
    held = SZ_NEUTRAL * SZ_ABSTAIN
    for row in ctx.get("positions_live") or []:
        if not isinstance(row, dict):
            return None, None, None, None, False
        qty = _sz_num(row.get("qty"))
        if qty is None:
            qty = _sz_num(row.get("quantity"))
        if qty is None or qty <= SZ_ABSTAIN:
            continue
        price = _sz_num(row.get("price"))
        if price is None:
            price = _sz_num(row.get("cur_price"))
        if price is None:
            price = _sz_num(row.get("cost"))
        if price is None:
            price = _sz_num(row.get("cost_price"))
        if price is None or price <= SZ_ABSTAIN:
            return None, None, None, None, False
        held = held + qty * price
    pending = SZ_NEUTRAL * SZ_ABSTAIN
    for row in ctx.get("pending_buys") or []:
        if not isinstance(row, dict) or row.get("intraday_t"):
            continue
        qty = _sz_num(row.get("remain"))
        if qty is None:
            qty = _sz_num(row.get("qty"))
        price = _sz_num(row.get("price"))
        if qty is None or price is None:
            return None, None, None, None, False
        pending = pending + qty * price
    buy_legs = _sz_num(ctx.get("t_buy_leg_value"))
    sell_legs = _sz_num(ctx.get("t_sell_leg_value"))
    if buy_legs is None or sell_legs is None:
        return None, None, None, None, False
    return held, pending, buy_legs, sell_legs, True


def target_position(params, ctx):
    """The account target layer plus this order's share.

    Tgt = total assets x min(MarketScale, ceiling)
    cur = held value + strategy pending buys - T buy legs + T sell legs
    room = max(0, Tgt - cur)
    amount = min(total x per_trade_pct, room)
    Go: internal/engine/funds_today.go:186-226 + internal/engine/engine.go:1844-1943.

    `action` is a readout, not an order: 'trim' only says the current exposure
    is above the target (Go reports that as a room clamped to zero and leaves
    the exit to the sell engine), 'add' says room still clears the minimum
    order size. Returns None when a required reading is absent -- including the
    fail-closed case of a position that cannot be priced.
    """
    if not _sz_flag(params, ctx, "sizing_enabled", "sizing_enabled"):
        return None
    base = _sz_read(ctx, "total_asset")
    if base is None:
        base = _sz_read(ctx, "equity")
    if base is None or base <= SZ_ABSTAIN:
        return None
    market, dims = market_scale(params, ctx)
    if market <= SZ_ABSTAIN:
        market = SZ_NEUTRAL
    stock, _stock_missing = stock_scale(params, ctx)
    if stock <= SZ_ABSTAIN:
        stock = SZ_NEUTRAL
    ceiling = _sz_p(params, "max_total_position_pct")
    if ceiling <= SZ_ABSTAIN or ceiling > SZ_NEUTRAL:
        ceiling = SZ_NEUTRAL
    held, pending, buy_legs, sell_legs, ok = _sz_position_value(ctx)
    if not ok:
        return None
    target = base * min(market, ceiling)
    current = held + pending - buy_legs + sell_legs
    if current < SZ_ABSTAIN:
        current = SZ_NEUTRAL * SZ_ABSTAIN
    room = target - current
    if room < SZ_ABSTAIN:
        room = SZ_NEUTRAL * SZ_ABSTAIN
    share = per_trade_pct(params, market, stock)
    per_trade = base * share
    amount = per_trade
    capped_by = "dynamic"
    if room < amount:
        amount = room
        capped_by = "target"
    min_amount = _sz_p(params, "min_order_amount")
    if current > target:
        action = "trim"
    elif amount >= min_amount:
        action = "add"
    else:
        action = "hold"
    return {
        "total": base,
        "market_scale": market,
        "stock_scale": stock,
        "ceiling": ceiling,
        "target": target,
        "current": current,
        "room": room,
        "per_trade_pct": share,
        "per_trade_amount": per_trade,
        "amount": amount,
        "capped_by": capped_by,
        "action": action,
        "missing_key_dims": dims,
        "missing_key_input": bool(dims),
    }


def _sz_bucket(ctx, key):
    """A caller-baked exposure bucket (industry / single name / correlation
    cluster). None means "this box was never handed that bucket", which is NOT
    the same as "that bucket is empty" -- so the cap that would have consumed
    it abstains instead of granting full headroom.

    The sandbox has no sector list (get_sector_list / get_sector_type absent)
    and no index weights (get_index_weight absent), so these three Go reads
    (internal/engine/engine.go:1763, :1828-1842, :1838-1841) can only arrive
    pre-computed. That is the pool-bucket approximation this port has to fall
    back to while the sector list and the index weights are unreachable.
    """
    return _sz_num((ctx or {}).get(key))


def _sz_room(cap_pct, equity, used):
    """Go ordbudget.roomOf (internal/ordbudget/ordbudget.go:313-322): the money
    still allowed under a percentage cap, floored at zero and rounded to cents
    so a float tail cannot skip a room that is exactly one lot wide."""
    if cap_pct is None or cap_pct <= SZ_ABSTAIN:
        return None
    if equity is None or equity <= SZ_ABSTAIN:
        return None
    if used is None:
        return None
    room = cap_pct / SZ_PERCENT * equity - used
    if room < SZ_ABSTAIN:
        room = SZ_NEUTRAL * SZ_ABSTAIN
    return round(room * SZ_CENTS) / SZ_CENTS


def buy_size_caps(params, ctx):
    """Every money cap that binds this order, by name (None = that cap has no
    reading and therefore does not participate). Cash, per-order, remaining
    daily quota, per-trade share and target room are ANDed -- min, never a
    product (qmt_gateway/local_executor.py rule_buy_amount,
    internal/ordbudget/ordbudget.go:141-322)."""
    caps = {
        "order_amount_cap": _sz_num(_sz_p(params, "order_amount_cap")),
        "daily_remaining": None,
        "cash": None,
        "per_trade": None,
        "target_room": None,
        "industry": None,
        "single_stock": None,
        "cluster": None,
    }
    # Concentration rooms (35% industry / 35% single name / 40% correlation
    # cluster) are ANDed with the money caps in Go, never multiplied: 40% x 35%
    # = 14% would silently tighten two caps threefold
    # (internal/ordbudget/ordbudget.go:168-219). They are min'd here too, which
    # is the same thing expressed on the amount axis.
    equity = _sz_read(ctx, "total_asset")
    if equity is None:
        equity = _sz_read(ctx, "equity")
    if (ctx or {}).get("industry_known"):
        caps["industry"] = _sz_room(_sz_p(params, "industry_cap_pct"), equity,
                                   _sz_bucket(ctx, "industry_value"))
    caps["single_stock"] = _sz_room(_sz_p(params, "single_stock_cap_pct"), equity,
                                    _sz_bucket(ctx, "stock_value"))
    if (ctx or {}).get("cluster_set"):
        caps["cluster"] = _sz_room(_sz_p(params, "cluster_cap_pct"), equity,
                                   _sz_bucket(ctx, "cluster_value"))
    cash = _sz_read(ctx, "available_cash")
    if cash is not None:
        caps["cash"] = cash - _sz_p(params, "min_cash_reserve")
    spent = _sz_read(ctx, "daily_buy_total")
    if spent is not None:
        caps["daily_remaining"] = _sz_p(params, "daily_buy_cap") - spent
    sized = target_position(params, ctx)
    if sized is not None:
        caps["per_trade"] = sized["per_trade_amount"]
        caps["target_room"] = sized["room"]
    return caps


def buy_size_amount(params, ctx):
    """The largest amount every cap allows at once, or None -- which the
    caller must read as 'send nothing', never as 'no limit'.
    Feeds qmt_gateway/local_executor.py rule_buy_amount."""
    caps = [v for v in buy_size_caps(params, ctx).values() if v is not None]
    if not caps:
        return None
    amount = min(caps)
    if amount < _sz_p(params, "min_order_amount"):
        return None
    return amount


# ---------------------------------------------------------------------------
# intraday T: the day-level gate and the per-direction environment gate
# ---------------------------------------------------------------------------

def t_allowed_by_sizing(params, ctx):
    """May intraday T run at all today? False in a data blind spot.

    Three independent stops, all inherited:
      * the blind spot -- with an untrusted target the whole T layer stops
        rather than trading on a half-believed quota, because round-tripping
        inside a blind spot costs far more than not doing it
        (internal/engine/funds_today.go:171-184, internal/paper/t_rounds.go:258);
      * shadow / disabled / a legacy budget pipeline (T is only defined under
        the dynamic target model -- Q5);
      * the shape gate: only a range tape makes T, a bull or bear tape closes
        it, and an unreadable or stale state ABSTAINS (passes) rather than
        stopping everything -- owner's ruling is that a switched-off gate must
        not be reported as a market condition, while an unrecognised state
        string is fail-closed (internal/engine/intraday_t_live.go:2153-2184).
    """
    if not _sz_flag(params, ctx, "sizing_enabled", "sizing_enabled"):
        return False
    if not _sz_flag(params, ctx, "t_enabled", "t_enabled"):
        return False
    if _sz_flag(params, ctx, "shadow", "sizing_shadow"):
        return False
    if _sz_p(params, "budget_mode") != "dynamic":
        return False
    if key_missing_dims(params, ctx):
        return False
    if _sz_flag(params, ctx, "t_regime_gate_enabled", "t_regime_gate_enabled"):
        state = (ctx or {}).get("market_state") or (ctx or {}).get("regime")
        if state:
            stale = _sz_num((ctx or {}).get("market_state_age_sec"))
            if stale is None or stale <= _sz_p(params, "t_regime_stale_sec"):
                # A fresh, non-empty reading only makes T in a range tape: bull
                # and bear are an explicit "no", and anything else -- a state
                # string this box does not recognise -- is fail-closed too,
                # because "configured but unreadable" is not "not configured"
                # (regimeBlock's default branch,
                # internal/engine/intraday_t_live.go:2181-2182).
                if str(state).strip() != "range":
                    return False
    return True


def t_direction_gate(params, ctx, direction):
    """(allowed, reason) for one T direction -- the step-0 environment gates
    (internal/engine/intraday_t_live.go:1240-1254).

    A buy-first leg ADDS exposure, so it faces the market-scale floor and the
    index plunge stop. A sell-first leg REDUCES exposure, so it faces only the
    extreme-weakness floor (the two are not symmetric by design). A missing
    MarketScale or index reading does not open the gate: the caller must have
    already passed t_allowed_by_sizing, which stops the blind spot upstream.
    """
    market, dims = market_scale(params, ctx)
    if dims:
        return False, "missing_key_input:" + "|".join(dims)
    change = _sz_read(ctx, "index_change_pct")
    mode = _sz_p(params, "t_direction_mode")
    if direction == "buy":
        if mode not in ("both", "buy_first"):
            return False, "direction_not_enabled"
        floor = _sz_num(_sz_p(params, "t_min_market_scale"))
        if floor is not None and floor > SZ_ABSTAIN and market < floor:
            return False, "min_market_scale"
        plunge = _sz_num(_sz_p(params, "t_market_plunge_pct"))
        if plunge is not None and plunge < SZ_ABSTAIN and change is not None \
                and change <= plunge:
            return False, "market_plunge"
        return True, "ok"
    if direction == "sell":
        if mode not in ("both", "sell_first"):
            return False, "direction_not_enabled"
        floor = _sz_num(_sz_p(params, "t_sell_env_min_market_scale"))
        if floor is not None and market < floor:
            return False, "sell_env_min_market_scale"
        return True, "ok"
    return False, "unknown_direction"


def sizing_readout(params, ctx):
    """One flat dict with everything the frame/snapshot needs to explain this
    round -- the Go side publishes the same set on the funds frame
    (internal/engine/funds_today.go:100-130, internal/paper/t_rounds_api_readout.go:39):
    the split scales, the blind-spot bool AND its dimension list, the index
    source/staleness, and the target layer. Absent values stay None; they are
    never written as zero, because a zero on the panel reads as a real
    measurement."""
    market, dims = market_scale(params, ctx)
    stock, stock_missing = stock_scale(params, ctx)
    target = target_position(params, ctx)
    blocked, block_reason = buy_hard_block(params, ctx)
    return {
        "market_scale": market,
        "stock_scale": stock,
        "missing_key_input": bool(dims),
        "missing_key_dims": dims,
        "stock_readings_missing": stock_missing,
        "emotion_phase": emotion_phase(params, ctx),
        "breadth_up_ratio": breadth_up_ratio(params, ctx),
        "index_source": (ctx or {}).get("index_source"),
        "index_stale": bool((ctx or {}).get("index_stale")),
        "t_allowed": t_allowed_by_sizing(params, ctx),
        "target": target,
        "buy_amount": buy_size_amount(params, ctx),
        "hard_block": block_reason if blocked else "",
    }
