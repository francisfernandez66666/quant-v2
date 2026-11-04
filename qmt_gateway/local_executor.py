#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway.local_executor -- the local Python executor for a physical box.

Why this module exists (broker rule effective 2026-11-05):
  * QMT built-in Python strategies lose all local file I/O and every piece
    of external network access;
  * QMT trading terminals may not run on public cloud or VMs.
  => QMT can only live on a physical Windows box, and because the
     computation can no longer live inside QMT, both the computation and
     the execution must live in a SEPARATE ordinary Python process (own
     venv, not the QMT built-in strategy sandbox), where file I/O and
     networking are allowed.

This file IS that ordinary process. It is split into layers so a unit test
can import any of them directly:

  DATA LAYER      xtquant.xtdata market reads (get_full_tick /
                  get_market_data_ex). Import protected on purpose: this
                  module must import cleanly on a machine without xtquant
                  (CI box, macOS dev laptop) and must never crash the
                  process when the QMT client is down.
  RULE LAYER      pure decision functions. Every threshold comes from the
                  baked PARAMS table and the rule layer contains NO numeric
                  literal at all, so a parameter change can never be
                  half-applied ("changed the knob, forgot to re-bake").
  STATE LAYER     a local sqlite journal of "what did we already send
                  today", used together with the broker's own same-day
                  order table as the idempotency double insurance.
  EXECUTION LAYER order placement / cancellation through xtquant.xttrader
                  plus the risk caps that the caller can no longer enforce
                  from the outside.

Hard rules encoded here (each one is a money rule, not a style rule):
  * Every order remark is "<version>-<signal short code>". The short code
    is the idempotency key: it is rebuilt from the broker's own same-day
    order table, so a restart can never re-send the same intent.
  * If the broker order table cannot be read, this round places NOTHING
    (fail closed -- rather not buy than buy twice).
  * The risk caps live here, not in the caller: per-order amount cap,
    daily cumulative buy cap, remaining quota, forbidden-cancel window,
    forbidden-new-order window, an order+cancel frequency hard gate, and
    the suspended / one-price-limit "not fillable" judgement.
  * Time windows are judged on the trading-day axis FIRST: on a closed day
    the answer is "not a trading day", never a fake "call auction
    rejected" story.
  * The broker order table, the local journal and the risk caps are all
    fail-closed: "cannot prove it is safe" means "do not trade".

The PARAMS block below is BAKED from qmt_gateway/local_executor_params.json
by scripts/build_local_executor.py. Edit the JSON, re-bake, re-test.
(English: the local executor outside QMT -- market data, P0 price rules,
idempotent order placement with fail-closed risk caps.)
"""

import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
import logging
import os
import sqlite3
import sys
import time

LOG = logging.getLogger("qmt_gateway.local_executor")

# ==== BAKED PARAMS BEGIN ====
STRATEGY_VERSION = "v02-20261104"
PARAMS = {
    'breadth_bands': {'low': 0.3, 'mid': 0.5},
    'breadth_missing_default': 0.9,
    'breadth_missing_scale': 0.9,
    'breadth_scales': {'low': 0.7, 'mid': 0.85, 'high': 1.0},
    'breakdown_ratio': 0.96,
    'budget_mode': 'dynamic',
    'builtin_rules': ['pullback_buy', 'take_profit', 'stop_loss', 'breakdown_sell'],
    'buy_signals': ['pullback_buy'],
    'close_sweep_from': '14:52',
    'closed_days': [],
    'cluster_cap_pct': 40,
    'cluster_rho': 0.7,
    'daily_buy_cap': 100000.0,
    'emo_breadth_ice_down_ratio': 0.0,
    'emo_breadth_retreat_down_ratio': 0.0,
    'emo_climax_board_min': 7,
    'emo_climax_limitup_min': 80,
    'emo_diverge_board_drop': 2,
    'emo_diverge_limitup_drop': 15,
    'emo_ferment_board_max': 6,
    'emo_ferment_limitup_max': 79,
    'emo_ferment_limitup_min': 40,
    'emo_ice_board_max': 2,
    'emo_ice_limitup_max': 14,
    'emo_retreat_board_max': 3,
    'emo_retreat_limitup_max': 14,
    'emo_start_board_max': 3,
    'emo_start_limitup_max': 39,
    'emo_start_limitup_min': 15,
    'emotion_enabled': True,
    'emotion_phase_labels': {'ice': '\u51b0\u70b9', 'start': '\u542f\u52a8', 'ferment': '\u53d1\u9175', 'climax': '\u9ad8\u6f6e', 'divergence': '\u80cc\u79bb', 'retreat': '\u9000\u6f6e'},
    'emotion_scales': {'ferment': 1, 'start': 1, 'divergence': 0.6, 'retreat': 0.5, 'climax': 0.8},
    'flow_5d_scales': {'mild_in': 1.05, 'mild_out': 0.85, 'neutral': 1, 'strong_in': 1.1, 'strong_out': 0.7},
    'flow_bands': {'mild_in_pct': 1, 'mild_out_pct': -1, 'strong_in_pct': 3, 'strong_out_pct': -3},
    'flow_factor_enabled': True,
    'flow_today_scales': {'mild_in': 1.025, 'mild_out': 0.95, 'neutral': 1, 'strong_in': 1.05, 'strong_out': 0.9},
    'group_floor_market': 0.45,
    'group_floor_stock': 0.35,
    'holiday_block_new': False,
    'holiday_long_closed_run': 5,
    'holiday_long_pre_max': 3,
    'holiday_scales': {'long1': 0.4, 'long2': 0.6, 'long3': 0.75, 'small1': 0.7, 'small2': 0.85, 'weekend': 0.85},
    'holiday_small_closed_run': 3,
    'holiday_small_pre_max': 2,
    'holiday_weekend_closed_run': 2,
    'holiday_weekend_pre_max': 2,
    'index_bands': {'shallow': -1.0, 'deep': -2.0},
    'index_scales': {'shallow': 1, 'mid': 0.7, 'deep': 0.4},
    'industry_cap_pct': 35.0,
    'kelly_clamp': [0.6, 1.15],
    'kelly_edge_gain': 1.1,
    'kelly_lambda': 0.5,
    'kelly_min_prob': 0.52,
    'kelly_no_edge': 0.6,
    'llm_bearish_scale': 0.5,
    'lot_size': 100,
    'ma_period': '1d',
    'ma_window': 5,
    'macro_event_day_scale': 0.3,
    'macro_lead_curve': [0.9, 0.85, 0.8, 0.7, 0.5],
    'macro_lead_days': 5,
    'macro_window_scales': {'high': 0.5, 'low': 1, 'medium': 0.8},
    'market_state_enabled': False,
    'max_drawdown_pct': 0.1,
    'max_ops_per_min': 6,
    'max_total_position_pct': 1,
    'min_cash_reserve': 1000.0,
    'min_order_amount': 10000,
    'min_price': 0.01,
    'min_volume': 1.0,
    'missing_key_target_cap': 0.5,
    'no_cancel_windows': [['09:20', '09:30'], ['14:57', '15:00']],
    'no_new_order_windows': [['09:20', '09:30'], ['14:52', '15:00']],
    'non_trading_weekdays': [5, 6],
    'ops_window_sec': 60,
    'order_amount_cap': 20000.0,
    'per_trade_default_hi': 0.3,
    'per_trade_default_lo': 0.1,
    'per_trade_hard_max': 0.5,
    'per_trade_position_max_pct': 0.3,
    'per_trade_position_pct': 0.1,
    'poll_interval_sec': 5,
    'ported_modules': ['rules_form', 'rules_ladder', 'rules_factor', 'rules_t', 'rules_sizing'],
    'power_p': 0.5,
    'pullback_ratio': 0.98,
    'quality_weight_clamp': [0.9, 1.1],
    'red_block_new_buy': False,
    'rule_arm': {'break_down': 'shadow', 'double_bump': 'shadow', 'dragon': 'shadow', 'dragon_return': 'shadow', 'factor_composite': 'shadow', 'good_news_fade': 'shadow', 'high_churn': 'shadow', 'leader_decay': 'shadow', 'leader_decay_v2': 'shadow', 'pattern_template': 'shadow'},
    'scale_cap': 1.15,
    'scale_floor': 0.15,
    'sell_boost_level': 'L2',
    'signal_codes': {'break_down': 'BD', 'breakdown_sell': 'BK', 'double_bump': 'DB', 'dragon': 'D', 'dragon_return': 'DR', 'factor_composite': 'FC', 'good_news_fade': 'GN', 'high_churn': 'HC', 'leader_decay': 'LD', 'leader_decay_v2': 'LD1', 'pattern_template': 'PT', 'pullback_buy': 'PB', 'stop_loss': 'SL', 'take_profit': 'TP'},
    'single_stock_cap_pct': 35.0,
    'sizing_enabled': True,
    'sizing_mode': 'power_mean',
    'sizing_shadow': False,
    'sl_offset_risk_budget_keep': True,
    'stale_order_sec': 900,
    'state_scales': {'bear': 0.4, 'bull': 1, 'range': 0.7},
    'stop_loss_ratio': 0.97,
    'stoploss_guard_count': 4,
    'symbol_head_len': 6,
    'symbols': ['600000.SH', '000001.SZ'],
    't_commission_rate': 0.00025,
    't_day_loss_limit_pct': 0,
    't_direction_mode': 'both',
    't_enabled': True,
    't_force_unwind_at': '1450',
    't_ghost_grace_sec': 90,
    't_intraday_cap_pct': 50,
    't_intraday_sell_cap_total_pct': 20,
    't_last_open_at': '1400',
    't_limit_distance_pct': 1,
    't_limit_pct_bse': 29.9,
    't_limit_pct_main': 9.9,
    't_limit_pct_st': 4.9,
    't_limit_pct_star': 19.9,
    't_limit_pct_wide': 19.9,
    't_market_plunge_pct': -2,
    't_max_rounds': 1,
    't_min_amount_wan': 3000,
    't_min_amplitude_pct': 2.5,
    't_min_base_keep': 100,
    't_min_base_keep_qty': 100,
    't_min_commission': 5,
    't_min_listed_days': 60,
    't_min_market_scale': 0.4,
    't_min_spread_pct': 2,
    't_min_turnover_pct': 1.5,
    't_mode': 'both',
    't_near_extreme_pct': 0.3,
    't_no_cancel_windows': [['09:20', '09:30'], ['14:57', '15:00']],
    't_no_new_order_windows': [['09:20', '09:30'], ['14:52', '15:00']],
    't_open_cooling_min': 15,
    't_premise_grace_sec': 120,
    't_regime_gate_enabled': True,
    't_regime_min_votes': 4,
    't_regime_stale_sec': 900,
    't_round_stop_pct': 2,
    't_sell_env_min_market_scale': 0.2,
    't_shadow': False,
    't_slippage_bps': 5,
    't_stamp_tax_rate': 0.0005,
    't_stop_guard_pct': 1.5,
    't_vol_ratio_max': 1.8,
    't_vwap_check_tol_pct': 0.1,
    't_vwap_dev_pct': 0.8,
    'take_profit_ratio': 1.05,
    'tier_red_default': 0.3,
    'tier_scales': {'Red': 0.3, 'Yellow': 0.6},
    'vol_clamp': [0.5, 1.5],
    'vol_target_pct': 2,
    'volume_ratio_min': 1.5,
}
# ==== BAKED PARAMS END ====


# ---------------------------------------------------------------------------
# UTIL layer: formatting, calendar and file helpers.
#
# These live OUTSIDE the rule layer on purpose. They carry structural
# constants (a time-zone offset, a "HH:MM" spelling) that are not business
# thresholds -- business thresholds belong in PARAMS so that changing one
# forces a re-bake. The build gate counts numeric literals inside the rule
# layer only; a threshold smuggled in here would be a silent bypass, so
# nothing in here may make a trading decision.
# ---------------------------------------------------------------------------

def now_cn():
    """Beijing wall clock (naive). The box is expected to run on CN time,
    but we do not rely on the OS clock's zone setting."""
    cn_zone = timezone(timedelta(hours=8))
    return datetime.now(cn_zone).replace(tzinfo=None)


def day_key(moment):
    """Trading day key, e.g. '20261103'."""
    return moment.strftime("%Y%m%d")


def hhmm_now(moment):
    """Wall clock as a zero padded 'HH:MM' string.

    A string (not a minute count) on purpose: the forbidden windows are
    spelled 'HH:MM' too, so the comparison stays a pure string compare and
    the rule layer never needs an arithmetic constant.
    """
    return moment.strftime("%H:%M")


def symbol_head(symbol, length):
    """Leading digits of a symbol, e.g. '600000.SH' -> '600000'."""
    text = str(symbol or "")
    return text.split(".")[0][:length]


def file_sha256(path):
    """SHA256 of a file, hex digest ('' when unreadable)."""
    digest = hashlib.sha256()
    try:
        with open(path, "rb") as handle:
            for chunk in iter(lambda: handle.read(65536), b""):
                digest.update(chunk)
    except (IOError, OSError):
        return ""
    return digest.hexdigest()


def text_sha256(text):
    """SHA256 of a text blob, hex digest."""
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def load_closed_days(path):
    """Read a Go-produced trading calendar file.

    Same shape the gateway consumes: {"saved_at": "...", "closed_days":
    ["YYYYMMDD", ...]}. An unreadable or malformed file yields an empty
    set and the caller falls back to the weekday rule baked in PARAMS. A
    missing calendar must never silently turn into "every day is a trading
    day", so the degradation is logged.
    """
    if not path:
        return set()
    try:
        with open(path, "r", encoding="utf-8") as handle:
            doc = json.load(handle)
    except (IOError, OSError, ValueError) as exc:
        LOG.warning("[calendar] unreadable calendar %s: %s", path, exc)
        return set()
    days = doc.get("closed_days")
    if not isinstance(days, list):
        LOG.warning("[calendar] malformed calendar %s: no closed_days list", path)
        return set()
    return set(str(item) for item in days)


def build_remark(short_code):
    """Compose the broker remark for an order: <version>-<signal short code>.

    The version prefix makes the deployed artifact traceable; the short
    code makes it idempotent. The remark is the only thing that survives a
    restart, so it must carry both.
    """
    return "%s-%s" % (STRATEGY_VERSION, short_code)


def short_code_from_remark(remark):
    """Reverse build_remark(): the short code carried by a remark ('' when absent)."""
    text = str(remark or "")
    if "-" not in text:
        return ""
    return text.rsplit("-", 1)[-1]


# ---------------------------------------------------------------------------
# DATA LAYER: local market data through xtquant.xtdata.
# ---------------------------------------------------------------------------

class DataUnavailable(Exception):
    """Local market data cannot be read (xtquant missing, client down...)."""


class XtDataSource(object):
    """Read-only wrapper over xtquant.xtdata.

    The import is guarded so that importing this module never fails on a
    machine without xtquant; the failure is reported as a readable error
    instead of an ImportError traceback at start-up.
    """

    def __init__(self):
        self._xtdata = None
        self._import_error = ""

    def _load(self):
        if self._xtdata is not None:
            return self._xtdata
        if self._import_error:
            return None
        try:
            from xtquant import xtdata  # noqa: PLC0415 (Windows-only dependency)
        except Exception as exc:  # noqa: BLE001 -- any import failure degrades
            self._import_error = "xtquant.xtdata unavailable: %s" % (exc,)
            LOG.warning("[data] %s", self._import_error)
            return None
        self._xtdata = xtdata
        return self._xtdata

    def available(self):
        """True when the local QMT market data channel can be used."""
        return self._load() is not None

    def status(self):
        """'up' or 'down' -- the readable degradation readout."""
        return "up" if self.available() else "down"

    def get_full_tick(self, symbols):
        """One full tick per symbol, as returned by xtdata.get_full_tick."""
        xt = self._load()
        if xt is None:
            raise DataUnavailable(self._import_error)
        try:
            return xt.get_full_tick(list(symbols))
        except Exception as exc:  # noqa: BLE001 -- broker SDK raises many types
            raise DataUnavailable("get_full_tick failed: %s" % (exc,))

    def get_market_data_ex(self, field_list, stock_list, period):
        """Historical bars through xtdata.get_market_data_ex."""
        xt = self._load()
        if xt is None:
            raise DataUnavailable(self._import_error)
        try:
            return xt.get_market_data_ex(list(field_list), list(stock_list),
                                         period=period, count=-1)
        except Exception as exc:  # noqa: BLE001
            raise DataUnavailable("get_market_data_ex failed: %s" % (exc,))

    def moving_average(self, symbol, window, period):
        """Simple moving average of the close price, or None when unavailable."""
        bars = self.get_market_data_ex(["close"], [symbol], period)
        series = self._extract_series(bars, "close", symbol)
        if not series:
            return None
        tail = series[-int(window):]
        if not tail:
            return None
        return sum(tail) / float(len(tail))

    @staticmethod
    def _extract_series(bars, field, symbol):
        """Pull one field series for one symbol out of a market_data_ex reply.

        The reply shape is {field: {symbol: [values, ...]}}; different xtquant
        builds nest it slightly differently, so every known shape is tried and
        a miss yields an empty series (the caller then treats the indicator
        as unavailable instead of guessing a value).
        """
        if not isinstance(bars, dict):
            return []
        block = bars.get(field)
        if not isinstance(block, dict):
            return []
        series = block.get(symbol)
        if series is None:
            for key, value in block.items():
                if str(key).split(".")[0] == str(symbol).split(".")[0]:
                    series = value
                    break
        if not isinstance(series, (list, tuple)):
            return []
        return [item for item in series if isinstance(item, (int, float))]


def normalize_tick(symbol, raw):
    """Normalise one raw tick into the flat dict the rule layer consumes."""
    raw = raw if isinstance(raw, dict) else {}

    def _num(key):
        value = raw.get(key)
        return float(value) if isinstance(value, (int, float)) else None

    def _first(key):
        value = raw.get(key)
        if isinstance(value, (list, tuple)) and value:
            value = value[0]
        return float(value) if isinstance(value, (int, float)) else None

    return {
        "symbol": symbol,
        "last_price": _num("lastPrice"),
        "prev_close": _num("lastClose"),
        "open": _num("open"),
        "high": _num("high"),
        "low": _num("low"),
        "volume": _num("volume"),
        "amount": _num("amount"),
        "ask1": _first("askPrice"),
        "bid1": _first("bidPrice"),
    }


# The rule layer is the decision core. Rules:
#   * NO numeric literal may appear anywhere in these function bodies (the
#     build gate counts them and fails the build). Every threshold comes
#     from PARAMS, so a knob change always goes through a re-bake. Even
#     "1" and "0" are banned: a comparison against a bare literal is how a
#     threshold sneaks in.
#   * no I/O, no clock, no randomness: the functions take what they need
#     as arguments and return a decision, so they can be unit tested
#     without a broker, a clock or a disk.
#   * missing PARAMS keys raise instead of defaulting: a silently missing
#     threshold must fail loudly, not fall back to a baked-in number.
#
# ==== RULE LAYER BEGIN ====
def rule_is_trading_day(params, ctx):
    """Is the moment a trading day?

    The weekday rule and the closed-day list both come from PARAMS; the
    closed-day list may be extended at runtime from the Go calendar file.
    """
    weekday = ctx.get("weekday")
    if weekday is None:
        return False
    if weekday in params["non_trading_weekdays"]:
        return False
    if ctx.get("day") in (ctx.get("closed_days") or []):
        return False
    return True


def rule_fillable(params, ctx, side):
    """Can this order possibly be filled right now? -> (fillable, reason).

    Rejects the two classical non-fillable shapes: a suspended name (no
    price / no volume) and a sealed limit board (no counterparty on the
    side we want to trade -- a limit-up board has no ask to lift, a
    limit-down board has no bid to hit).
    """
    last = ctx.get("last_price")
    if last is None or last < params["min_price"]:
        return False, "no_price"
    volume = ctx.get("volume")
    if volume is not None and volume < params["min_volume"]:
        return False, "suspended"
    if side == "buy":
        ask1 = ctx.get("ask1")
        if ask1 is not None and ask1 < params["min_price"]:
            return False, "limit_up_sealed"
    else:
        bid1 = ctx.get("bid1")
        if bid1 is not None and bid1 < params["min_price"]:
            return False, "limit_down_sealed"
    return True, "fillable"


def rule_volume_ratio_ok(params, ctx):
    """Volume ratio gate: today's volume against its own recent average."""
    ratio = ctx.get("volume_ratio")
    if ratio is None:
        return False
    return ratio >= params["volume_ratio_min"]


def rule_cash_ok(params, ctx, amount):
    """Available cash gate: the order must fit in cash above the reserve."""
    cash = ctx.get("available_cash")
    if cash is None:
        return False
    usable = cash - params["min_cash_reserve"]
    return amount <= usable


def rule_amount_cap_ok(params, amount):
    """Per-order amount cap."""
    return amount <= params["order_amount_cap"]


def rule_daily_buy_cap_ok(params, ctx, amount):
    """Daily cumulative buy cap, evaluated against what is already spent."""
    spent = ctx.get("daily_buy_total")
    if spent is None:
        return False
    return (spent + amount) <= params["daily_buy_cap"]


def rule_remaining_quota(params, ctx):
    """Remaining daily buy quota (None when the daily total is unknown)."""
    spent = ctx.get("daily_buy_total")
    if spent is None:
        return None
    return params["daily_buy_cap"] - spent


def rule_buy_amount(params, ctx):
    """Largest order amount allowed by every money cap at once.

    Returns None when no amount survives -- the caller then sends nothing.
    The caps are ANDed, not ORed: cash, per-order cap and remaining daily
    quota must all allow the order.
    """
    caps = []
    cash = ctx.get("available_cash")
    if cash is not None:
        caps.append(cash - params["min_cash_reserve"])
    caps.append(params["order_amount_cap"])
    remaining = rule_remaining_quota(params, ctx)
    if remaining is not None:
        caps.append(remaining)
    amount = min(caps)
    if amount < params["min_order_amount"]:
        return None
    return amount


def rule_window_hit(params, windows, hhmm):
    """Does the wall clock fall into one of the given 'HH:MM' windows?

    Half-open [start, end) -- the SAME convention as the three existing
    copies (Go internal/trading/session_guard.go "min >= start && min < end",
    qmt_gateway/broker.py no_cancel_window_now, bridge _no_cancel_window_at).
    This is not cosmetic: with a closed end, "09:20-09:30" also blocks 09:30,
    and 09:30 is exactly when the continuous session opens -- the fourth copy
    would then refuse a legal order for one minute every day while the other
    three allowed it. Windows that wrap past midnight keep the same shape.
    """
    for window in windows or []:
        try:
            start, end = window
        except (TypeError, ValueError):
            continue
        if start <= end:
            if start <= hhmm < end:
                return True
        elif hhmm >= start or hhmm < end:
            return True
    return False


def rule_no_cancel_window(params, ctx):
    """May we cancel right now? -> (allowed, reason).

    The trading-day check comes FIRST. On a closed day the answer is
    'not_trading_day' -- never a story about the call auction, because on
    a closed day there is no session to have refused anything.
    """
    if not ctx.get("trading_day"):
        return False, "not_trading_day"
    if rule_window_hit(params, params["no_cancel_windows"], ctx.get("hhmm")):
        return False, "no_cancel_window"
    return True, "ok"


def rule_no_new_order_window(params, ctx):
    """May we place a new order right now? -> (allowed, reason)."""
    if not ctx.get("trading_day"):
        return False, "not_trading_day"
    if rule_window_hit(params, params["no_new_order_windows"], ctx.get("hhmm")):
        return False, "no_new_order_window"
    return True, "ok"


def rule_ops_frequency_ok(params, ctx):
    """Order+cancel frequency hard gate (operations per minute)."""
    recent = ctx.get("ops_last_minute")
    if recent is None:
        return False
    return recent < params["max_ops_per_min"]


def rule_pullback_buy(params, ctx):
    """P0 pullback buy signal.

    The price must have pulled back to (or just under) its reference level
    and the volume ratio must confirm the pullback. Returns a signal dict
    or None.
    """
    if not ctx.get("trading_day"):
        return None
    last = ctx.get("last_price")
    ref = ctx.get("ref_price")
    if last is None or ref is None:
        return None
    if last > ref * params["pullback_ratio"]:
        return None
    if not rule_volume_ratio_ok(params, ctx):
        return None
    amount = rule_buy_amount(params, ctx)
    if amount is None:
        return None
    return make_signal(params, ctx, "pullback_buy", "buy", amount, last)


def rule_take_profit(params, ctx):
    """P0 take-profit signal for a held position."""
    if not ctx.get("trading_day"):
        return None
    last = ctx.get("last_price")
    cost = ctx.get("position_cost")
    qty = ctx.get("position_qty")
    if last is None or cost is None or not qty:
        return None
    if last < cost * params["take_profit_ratio"]:
        return None
    return make_signal(params, ctx, "take_profit", "sell", None, last, qty=qty)


def rule_stop_loss(params, ctx):
    """P0 stop-loss signal for a held position."""
    if not ctx.get("trading_day"):
        return None
    last = ctx.get("last_price")
    cost = ctx.get("position_cost")
    qty = ctx.get("position_qty")
    if last is None or cost is None or not qty:
        return None
    if last > cost * params["stop_loss_ratio"]:
        return None
    return make_signal(params, ctx, "stop_loss", "sell", None, last, qty=qty)


def rule_breakdown_sell(params, ctx):
    """P0 breakdown sell signal: the price lost its reference level."""
    if not ctx.get("trading_day"):
        return None
    last = ctx.get("last_price")
    ref = ctx.get("ref_price")
    qty = ctx.get("position_qty")
    if last is None or ref is None or not qty:
        return None
    if last > ref * params["breakdown_ratio"]:
        return None
    return make_signal(params, ctx, "breakdown_sell", "sell", None, last, qty=qty)


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
# ==== RULE LAYER END ====



def _ported_static_table():
    """kind -> (fn, module name) read from what the builder inlined.

    A module named in params but contributing nothing is reported by
    load_ported_modules as a failure, not as "no signals today".
    """
    out = {}
    for name, table in _PORTED_INLINE.items():
        for kind, fn in (table or {}).items():
            out[kind] = (fn, name)
    return out


def register_ported_rule(kind, fn, source):
    """Add one ported rule to the registry.

    A duplicate kind is a build failure, not a last-write-wins: two rules with
    the same short code would make the counter-remark idempotency check prove
    the wrong intent (the first rule's orders would silence the second's).
    """
    if kind in _PORTED_RULES and _PORTED_RULES[kind][1] != source:
        raise ValueError("ported_rule_kind_conflict:%s(%s,%s)"
                         % (kind, _PORTED_RULES[kind][1], source))
    _PORTED_RULES[kind] = (fn, source)


def load_ported_modules(params):
    """Import the ported rule modules and register what they expose.

    Fail-loud-but-continue: a module that will not import is recorded in
    params-visible state so the round reports it. A strategy module that is
    silently missing is the "it looks like it runs but nothing was ever loaded"
    shape this repo keeps getting bitten by (see the 2026-11-02 dead-gauge notes).
    """
    # Static list on purpose: the build gate must be able to see which symbols
    # the artifact can reach, so there is no dynamic import here (and the sandbox
    # artifact has no import at all -- the builder inlines the bodies instead).
    table = {}
    try:
        table.update(_ported_static_table())
    except Exception as exc:  # noqa: BLE001
        return [], ["table_unavailable:%s" % repr(exc)[:80]]
    wanted = params.get("ported_modules") or sorted(set(src for _k, (_f, src) in table.items()))
    loaded, failed = [], []
    for name in wanted:
        kinds = [k for k, (_f, src) in table.items() if src == name]
        if not kinds:
            # The module was named but registers nothing: it did not come along
            # (package missing a file, or the sandbox inline failed). Report it
            # red -- folding "not loaded" into "no signal today" is the exact
            # failure shape this repo keeps getting bitten by.
            failed.append("%s:no_rules_registered" % name)
            continue
        for kind in kinds:
            try:
                register_ported_rule(kind, table[kind][0], name)
            except ValueError as exc:
                failed.append("%s:%s" % (name, repr(exc)[:80]))
                continue
        loaded.append(name)
    return loaded, failed


def rule_arm_mode(params, kind):
    """shadow | live | off for one rule (default off for anything ported).

    Why this exists instead of "port it and it trades": docs/IMPL_QMT_INTERNAL_LOOP
    .md requires every P1 rule to run in shadow against the Go engine for at
    least five trading days
    before it may touch money, because a threshold that was re-calibrated wrong
    changes the strategy's MEANING without changing any number anyone can see.
    Shadow means the rule computes, is counted, and never places.
    """
    modes = params.get("rule_arm") or {}
    if kind in modes:
        return str(modes.get(kind) or "off")
    if kind in (params.get("builtin_rules") or []):
        return "live"
    return "off"

def ported_load_status(params):
    """Which ported rule modules actually arrived, and which did not.

    This exists because "the box is running 4 rules instead of the 20 that were
    tested" is invisible from the outside: the round simply produces fewer
    signals, which looks exactly like a quiet market. Anything that cannot be
    loaded has to be said out loud (log, --selftest, round summary) -- the same
    stance the news and quote legs take with "not fetched" vs "nothing there".
    """
    table = _ported_static_table()
    wanted = list(params.get("ported_modules") or [])
    # "carried in the artifact" and "produces a signal kind" are two different
    # facts and must be counted separately:
    #   name present in _PORTED_INLINE -> the builder really shipped this module
    #     (its RULES snapshot is baked in, even when that snapshot is empty --
    #     rules_t and rules_sizing are judgement legs: they decide whether/how big,
    #     they emit no signal kind of their own);
    #   name absent -> the file was missing or the inline failed. That is a shortage.
    # Counting "modules that have kinds" as loaded used to report both judgement
    # legs as missing, which mixed "by design" into the same readout as "a file is
    # gone". Split apart, EXEC_RULES_ALERT fires only for a real shortage.
    present = sorted(_PORTED_INLINE)
    kinds = sorted(table)
    kinds_by_module = {}
    for _kind, (_fn, src) in table.items():
        kinds_by_module[src] = kinds_by_module.get(src, 0) + 1
    missing = [name for name in wanted if name not in present]
    judgement = [name for name in wanted
                 if name in present and kinds_by_module.get(name, 0) == 0]
    armed = {kind: rule_arm_mode(params, kind) for kind in kinds}
    return {"wanted": wanted, "loaded": present, "missing": missing,
            "judgement": judgement, "kinds": kinds,
            "kinds_by_module": kinds_by_module, "armed": armed}




# ---------------------------------------------------------------------------
# ==== PORTED RULES BEGIN ====
# The ported rule modules (rules_form / rules_ladder / rules_factor / rules_t /
# rules_sizing) are INLINED here by scripts/build_local_executor.py, in both
# carriers. One mechanism on purpose:
#   * the QMT built-in strategy is a single file and cannot import anything;
#   * the published local-executor artifact is also a single file, and a runtime
#     "import rules_t" would then depend on where the file happens to sit --
#     a missing module would silently shrink the rule set while every gate stayed
#     green. That is the "the knob says 5 strategies, the box runs 4" shape.
# In the repository copy this block is empty (the modules are not written yet);
# the builder fails the build when params names a module that is not on disk.
_PORTED_RULES = {}
_PORTED_INLINE = {}
# ==== PORTED RULES END ====

# STATE LAYER: the local sqlite journal ("what did we already send today").
# ---------------------------------------------------------------------------

class LocalState(object):
    """Local journal of today's orders and of every broker operation.

    This is half of the idempotency double insurance: the other half is
    the broker's own same-day order table. Neither alone is trusted --
    the journal can be lost (disk, reinstall) and the broker table can be
    unreadable (client down), and in both cases we must not re-send.
    """

    def __init__(self, path):
        self.path = path
        self._ensure_schema()

    def _connect(self):
        directory = os.path.dirname(self.path)
        if directory and not os.path.isdir(directory):
            os.makedirs(directory)
        conn = sqlite3.connect(self.path)
        # Journal mode: TRUNCATE keeps the rollback journal but zeroes it
        # instead of deleting the file on every commit.
        #
        # Why this matters here: the journal lives on the operator's box,
        # and a rollback journal that has to be DELETED on each commit
        # fails outright when the file cannot be unlinked (locked by a
        # scanner, a read-only mount, a sandboxed process). A journal
        # that cannot be written means the idempotency half of the
        # insurance is silently gone -- and a silently missing idempotency
        # journal is how the same order gets sent twice. TRUNCATE keeps
        # full transactional rollback and only needs write access to a
        # file that already exists.
        #
        # The mode is a pragma, not a literal threshold: it is not a
        # trading parameter and does not belong in PARAMS.
        try:
            conn.execute("PRAGMA journal_mode=TRUNCATE")
        except sqlite3.Error:
            # A journal mode the build refuses is not a reason to lose
            # the journal: fall back to the default and carry on.
            pass
        return conn

    def _ensure_schema(self):
        """Create the journal tables if they are not there yet.

        Both tables are created on every open: CREATE TABLE IF NOT EXISTS
        is cheap, and a journal whose schema does not match the code
        would be worse than a fresh one.
        """
        conn = self._connect()
        try:
            conn.execute(
                "CREATE TABLE IF NOT EXISTS sent_orders ("
                "day TEXT NOT NULL, short_code TEXT NOT NULL, symbol TEXT NOT NULL,"
                " kind TEXT NOT NULL, side TEXT NOT NULL, qty INTEGER NOT NULL,"
                " price REAL NOT NULL, remark TEXT NOT NULL, order_id TEXT NOT NULL,"
                " status TEXT NOT NULL, ts REAL NOT NULL,"
                " PRIMARY KEY (day, short_code))")
            conn.execute(
                "CREATE TABLE IF NOT EXISTS ops_log ("
                "ts REAL NOT NULL, kind TEXT NOT NULL)")
            # CANCEL-LEG: cancel trail ("this id was already cancelled today"). The DDL
            # lives HERE only -- two places holding DDL means two schema evolutions.
            conn.execute(
                "CREATE TABLE IF NOT EXISTS cancelled_orders ("
                "day TEXT NOT NULL, order_id TEXT NOT NULL, ts REAL NOT NULL,"
                " PRIMARY KEY (day, order_id))")
            conn.commit()
        finally:
            conn.close()

    def record_order(self, day, signal, remark, order_id, status="sent"):
        """Remember that we sent this short code today."""
        conn = self._connect()
        try:
            conn.execute(
                "INSERT OR REPLACE INTO sent_orders"
                " (day, short_code, symbol, kind, side, qty, price, remark,"
                "  order_id, status, ts) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                (day, signal["short_code"], signal["symbol"], signal["kind"],
                 signal["side"], int(signal["qty"]), float(signal["price"]),
                 remark, str(order_id), status, time.time()))
            conn.commit()
        finally:
            conn.close()

    def short_codes(self, day):
        """Short codes already sent today (from the local journal)."""
        conn = self._connect()
        try:
            rows = conn.execute(
                "SELECT short_code FROM sent_orders WHERE day = ?", (day,)).fetchall()
        finally:
            conn.close()
        return set(row[0] for row in rows)

    def orders(self, day):
        """Rows sent today (short code, order id, status, side, qty, price)."""
        conn = self._connect()
        try:
            rows = conn.execute(
                "SELECT short_code, order_id, status, side, symbol, qty, price"
                " FROM sent_orders WHERE day = ? ORDER BY ts", (day,)).fetchall()
        finally:
            conn.close()
        return [{"short_code": r[0], "order_id": r[1], "status": r[2],
                 "side": r[3], "symbol": r[4], "qty": r[5], "price": r[6]}
                for r in rows]

    def record_op(self, kind):
        """Append one broker operation (place or cancel) to the ops log."""
        conn = self._connect()
        try:
            conn.execute("INSERT INTO ops_log (ts, kind) VALUES (?,?)",
                         (time.time(), kind))
            conn.commit()
        finally:
            conn.close()

    def mark_cancelled(self, day, order_id):
        """Remember that this order id has already been cancelled once today.

        Why its own table instead of a status column on sent_orders: a cancel
        belongs to an ORDER ID while sent_orders is keyed by (day, short_code)
        -- one intent can carry several ids. Without it, every round after a
        restart re-cancels the same id: exactly the "frequent instant cancel"
        pattern the exchange watches (5.3 gates new work, this gates repeats).
        """
        conn = self._connect()
        try:
            conn.execute("INSERT OR REPLACE INTO cancelled_orders (day, order_id, ts)"
                         " VALUES (?,?,?)", (day, str(order_id), time.time()))
            conn.commit()
        finally:
            conn.close()

    def cancelled_ids(self, day):
        """Order ids already cancelled today (read-only)."""
        conn = self._connect()
        try:
            rows = conn.execute(
                "SELECT order_id FROM cancelled_orders WHERE day = ?", (day,)).fetchall()
        finally:
            conn.close()
        return set(str(r[0]) for r in rows)

    def ops_since(self, since_ts):
        """Number of broker operations recorded at or after the given timestamp."""
        conn = self._connect()
        try:
            row = conn.execute(
                "SELECT COUNT(*) FROM ops_log WHERE ts >= ?", (float(since_ts),)).fetchone()
        finally:
            conn.close()
        return int(row[0]) if row else 0


# ---------------------------------------------------------------------------
# EXECUTION LAYER: order placement through xtquant.xttrader.
# ---------------------------------------------------------------------------

class BrokerUnavailable(Exception):
    """The broker channel cannot be used right now."""


class DisabledBroker(object):
    """The broker face used when the xt trading channel is NOT permitted -- which is the default.

    Why the default refuses to trade (2026-11-04, before anyone runs this on a real account):
      * this file's execution layer places orders through `xtquant.xttrader` -- that is the
        INDEPENDENT TRADING channel (miniqmt). The broker closed it in September 2026 and
        answered us in writing: "independent trading belongs to miniqmt, closed since September";
      * 2026-10-29's postmortem is the physical reason to keep it off rather than "try once":
        every `connect()` while the client is in queued state takes a writer slot in the
        client's shared memory, slots filled up, and the QMT client crashed every 3-6 minutes
        across 713 consecutive failures. A loop that "just checks whether it can connect"
        is therefore not a read-only probe -- it is an attack on our own trading terminal;
      * after 2026-11-05 the only legal automated order path we are allowed to build is the
        QMT BUILT-IN strategy (this repo's qmt_internal_strategy.py, placing through
        passorder inside the sandbox). The local process computes and delivers readings;
        it does not place.

    So every face of this object refuses, and it refuses with a reason that can be grepped.
    Refusing is also what keeps `--once` safe to run on a box with a funded account: the
    round fail-closes at "the broker order table is unreadable" and never reaches an order.
    """

    reason = "xt_channel_disabled"

    def connect(self):
        return False, self.reason

    def is_connected(self):
        return False

    def status(self):
        return "disabled"

    def orders_today(self):
        raise BrokerUnavailable(self.reason)

    def positions_today(self):
        raise BrokerUnavailable(self.reason)

    def asset(self):
        raise BrokerUnavailable(self.reason)

    def place_order(self, signal, remark):
        # Returns the refusal instead of raising: callers treat (ok=False, "", reason)
        # as "nothing was sent", which is exactly what happened.
        return False, "", self.reason

    def cancel_order(self, order_id):
        return False, self.reason


def build_broker(params, account):
    """Choose the broker face from the parameter table -- the closed channel is OFF by default.

    `xt_channel_enable` is a pointer-style opt-in: the key being absent means the safe
    behaviour (no trading channel at all). That is deliberate: a fresh box pulled out of the
    friend package must not place orders just because nobody edited a file.
    """
    if params.get("xt_channel_enable"):
        return XtBroker(account)
    return DisabledBroker()


def counter_status_vocab():
    """Counter status vocabulary + terminal set: IMPORTED, never copied here.

    Sources of truth:
      - handler.XT_STATUS_CODES -- counter m_nOrderStatus (48..57) to the ledger
        literals, the same code space the bridge uses;
      - store.TERMINAL_STATUSES -- the terminal set the close gate consumes.

    Why copying is forbidden here: a hand-written SUBSET of the status table
    fails silently in both directions -- a missing code read as non-terminal
    means re-cancelling forever, one read as terminal means an open leg is
    booked as settled and its quota released early. When the vocabulary cannot
    be imported we return ({}, ()) and the caller must treat that as UNKNOWN
    (do nothing), never as "nothing is terminal" or "everything is".

    English: the counter status vocabulary and terminal-status set, imported from
    their single sources; unavailable means "unknown", never a guessed subset.
    """
    try:
        from handler import XT_STATUS_CODES  # noqa: PLC0415
        from store import TERMINAL_STATUSES  # noqa: PLC0415
    except Exception as exc:  # noqa: BLE001
        LOG.warning("[status] counter status vocabulary unavailable: %s", exc)
        return {}, ()
    return XT_STATUS_CODES, TERMINAL_STATUSES


def normalize_order_status(raw, codes, terminals):
    """One raw order status -> (ledger literal | unknown(...), terminal flag).

    The flag is THREE-VALUED: True / False / None = unknown. None is the key
    output of this helper -- unavailable vocabulary, an unmapped code or a
    non-numeric value must travel outward unchanged so the caller declines to
    act. Folding unknown into False re-cancels forever; into True books a live
    order as settled and releases its quota early.
    """
    if not codes:
        return "unknown(vocabulary_unavailable)", None
    text = str(raw).strip() if raw is not None else ""
    if not text:
        return "unknown(missing)", None
    try:
        literal = codes.get(int(text))
    except (TypeError, ValueError):
        # not numeric: this gateway build may already hand us the literal
        literal = text
    if literal is None:
        return "unknown(%s)" % text, None
    return literal, (literal in terminals) if terminals else None


class XtBroker(object):
    """Order placement through the local xtquant trading channel.

    The broker is the only place that knows what really happened, so this
    wrapper is deliberately thin: it maps our signal dict onto the SDK and
    maps the SDK's answers back. Every failure is reported as text.
    """

    def __init__(self, account, path="", session_id=1):
        self.account = account
        self.path = path
        self.session_id = session_id
        self._trader = None
        self._acc = None
        self._error = ""

    def _load(self):
        try:
            from xtquant.xttrader import XtQuantTrader  # noqa: PLC0415
            from xtquant.xttype import StockAccount  # noqa: PLC0415
        except Exception as exc:  # noqa: BLE001
            self._error = "xtquant.xttrader unavailable: %s" % (exc,)
            raise BrokerUnavailable(self._error)
        return XtQuantTrader, StockAccount

    def connect(self):
        """Open the trading session; returns (ok, reason)."""
        try:
            trader_cls, account_cls = self._load()
        except BrokerUnavailable as exc:
            return False, str(exc)
        try:
            trader = trader_cls(self.path, self.session_id)
            if trader.start() is not None:
                raise RuntimeError("XtQuantTrader.start() failed")
            if trader.connect() != 0:
                raise RuntimeError("XtQuantTrader.connect() failed")
            account = account_cls(self.account)
            asset = trader.query_stock_asset(account)
            if asset is None:
                raise RuntimeError("query_stock_asset() returned None")
        except Exception as exc:  # noqa: BLE001
            self._error = "broker connect failed: %s" % (exc,)
            LOG.warning("[broker] %s", self._error)
            return False, self._error
        self._trader = trader
        self._acc = account
        return True, "ok"

    def is_connected(self):
        return self._trader is not None

    def status(self):
        """'up' or 'down' -- the readable degradation readout."""
        return "up" if self.is_connected() else "down"

    def place_order(self, signal, remark):
        """Place one order. Returns (ok, order_id, reason).

        The remark goes into the SDK's order_remark field: it is the field
        the broker hands back on every same-day order query, and therefore
        the field we rebuild our idempotency state from.
        """
        if not self.is_connected():
            return False, "", "broker_not_connected"
        try:
            from xtquant import xtconstant  # noqa: PLC0415
        except Exception as exc:  # noqa: BLE001
            return False, "", "xtconstant unavailable: %s" % (exc,)
        try:
            order_type = int(getattr(xtconstant, "STOCK_BUY", 23)) \
                if signal["side"] == "buy" \
                else int(getattr(xtconstant, "STOCK_SELL", 24))
            price_type = int(getattr(xtconstant, "FIX_PRICE", 11))
        except Exception as exc:  # noqa: BLE001
            return False, "", "order constants unavailable: %s" % (exc,)
        try:
            seq = self._trader.order_stock(
                self._acc, signal["symbol"], order_type, int(signal["qty"]),
                price_type, float(signal["price"]), "local_executor", remark)
        except Exception as exc:  # noqa: BLE001
            return False, "", "order_stock failed: %s" % (exc,)
        if not isinstance(seq, int) or seq <= 0:
            return False, "", "order rejected by broker (seq=%s)" % (seq,)
        return True, str(seq), "ok"

    def cancel_order(self, order_id):
        """Cancel one order by exchange order id. Returns (ok, reason)."""
        if not self.is_connected():
            return False, "broker_not_connected"
        try:
            numeric = int(order_id)
        except (TypeError, ValueError):
            return False, "invalid order id: %s" % (order_id,)
        try:
            self._trader.cancel_order_stock(self._acc, numeric)
        except Exception as exc:  # noqa: BLE001
            return False, "cancel failed: %s" % (exc,)
        return True, "ok"

    def orders_today(self):
        """The broker's own same-day order table.

        Returns a list of normalised rows. Raises BrokerUnavailable when the
        table cannot be read -- the caller MUST then place nothing this
        round (fail closed: an unreadable table means we cannot prove that
        we have not already sent the same intent).
        """
        if not self.is_connected():
            raise BrokerUnavailable("broker_not_connected")
        try:
            raw = self._trader.query_stock_orders(self._acc)
        except Exception as exc:  # noqa: BLE001
            # Every failure mode here is the SAME failure mode for the
            # caller: the order table is unreadable, so the caller must
            # place nothing this round. They are not distinguished on
            # purpose -- the caller has one response to "unknown".
            raise BrokerUnavailable("query_stock_orders failed: %s" % (exc,))
        if raw is None:
            raise BrokerUnavailable("query_stock_orders returned None")
        rows = []
        codes, terminals = counter_status_vocab()
        for item in raw:
            remark = getattr(item, "order_remark", "") or getattr(item, "remark", "") or ""
            status, terminal = normalize_order_status(
                getattr(item, "order_status", None), codes, terminals)
            rows.append({
                "order_id": str(getattr(item, "order_id", "") or ""),
                "symbol": str(getattr(item, "stock_code", "") or ""),
                "remark": remark,
                "status": status,
                # CANCEL-LEG three-state flag: True = terminal (leave it),
                # False = live (cancellable), None = UNKNOWN (do not touch).
                "terminal": terminal,
                "qty": getattr(item, "order_volume", 0) or 0,
                "price": getattr(item, "price", 0) or 0,
                # Filled quantity comes from the counter row (traded_volume),
                # never an in-memory counter: a partial leg cancels its REMAINDER.
                "traded": getattr(item, "traded_volume", 0) or 0,
                # Placement time (epoch seconds): the stale-cancel baseline.
                "order_time": getattr(item, "order_time", None),
            })
        return rows

    def available_cash(self):
        """Cash available for buying (None when the broker cannot say)."""
        if not self.is_connected():
            return None
        try:
            asset = self._trader.query_stock_asset(self._acc)
        except Exception as exc:  # noqa: BLE001
            LOG.warning("[broker] query_stock_asset failed: %s", exc)
            return None
        if asset is None:
            return None
        if isinstance(asset, dict):
            cash = asset.get("cash", 0)
            frozen = asset.get("frozen_cash", 0)
        else:
            cash = getattr(asset, "cash", 0)
            frozen = getattr(asset, "frozen_cash", 0)
        try:
            return float(cash) - float(frozen)
        except (TypeError, ValueError):
            return None

    def positions(self):
        """Current positions as {symbol: {qty, cost}} ({} when unknown)."""
        if not self.is_connected():
            return {}
        try:
            raw = self._trader.query_stock_positions(self._acc)
        except Exception as exc:  # noqa: BLE001
            LOG.warning("[broker] query_stock_positions failed: %s", exc)
            return {}
        out = {}
        for item in raw or []:
            symbol = str(getattr(item, "stock_code", "") or "")
            if not symbol:
                continue
            try:
                qty = float(getattr(item, "volume", 0) or 0)
                cost = float(getattr(item, "open_price", 0) or 0)
            except (TypeError, ValueError):
                continue
            out[symbol] = {"qty": qty, "cost": cost}
        return out


# ---------------------------------------------------------------------------
# EXECUTION LAYER: the round loop.
# ---------------------------------------------------------------------------

class Executor(object):
    """One trading round: read the market, decide, then send (or not send).

    The order of the checks is the point of this class:
      1. broker order table readable? NO -> send nothing (fail closed);
      2. idempotency: short code already sent today? -> skip;
      3. trading day / forbidden windows / frequency gate / money caps;
      4. only then place the order and write it into the local journal.
    """

    def __init__(self, params, data, broker, state, clock=None):
        self.params = params
        # Rule failures are counted, never folded into "no signal today".
        self.errors = []
        self.data = data
        self.broker = broker
        self.state = state
        self.clock = clock or now_cn
        # Register whatever the artifact inlined (or whatever the box can import)
        # once, at construction, so a missing module is reported by the FIRST
        # round instead of being discovered by a human comparing signal counts.
        self.ported_loaded, self.ported_failures = load_ported_modules(params)

    def context_for(self, symbol, moment, broker_rows, positions, cash, closed_days):
        """Assemble the flat context the rule layer consumes."""
        tick = {}
        try:
            raw = self.data.get_full_tick([symbol])
            tick = normalize_tick(symbol, raw.get(symbol) if isinstance(raw, dict) else None)
        except DataUnavailable as exc:
            LOG.warning("[data] tick unavailable for %s: %s", symbol, exc)
        ref = tick.get("prev_close")
        volume_ratio = None
        try:
            avg = self.data.moving_average(symbol, self.params["ma_window"],
                                           self.params["ma_period"])
            if avg:
                volume_ratio = self._volume_ratio(symbol, tick)
                if ref is None:
                    ref = avg
        except DataUnavailable:
            avg = None
        position = positions.get(symbol) or {}
        day = day_key(moment)
        return {
            "symbol": symbol,
            "day": day,
            "weekday": moment.weekday(),
            "closed_days": closed_days,
            "hhmm": hhmm_now(moment),
            "trading_day": rule_is_trading_day(self.params, {
                "weekday": moment.weekday(),
                "day": day,
                "closed_days": closed_days,
            }),
            "last_price": tick.get("last_price"),
            "prev_close": tick.get("prev_close"),
            "volume": tick.get("volume"),
            "ask1": tick.get("ask1"),
            "bid1": tick.get("bid1"),
            "ref_price": ref,
            "volume_ratio": volume_ratio,
            "position_qty": position.get("qty"),
            "position_cost": position.get("cost"),
            "available_cash": cash,
            "daily_buy_total": self._daily_buy_total(day, broker_rows),
            "ops_last_minute": self.state.ops_since(
                time.time() - float(self.params["ops_window_sec"])),
        }

    def _volume_ratio(self, symbol, tick):
        """Today's volume against the recent average volume of the same name."""
        volume = tick.get("volume")
        if not volume:
            return None
        try:
            bars = self.data.get_market_data_ex(["volume"], [symbol],
                                                self.params["ma_period"])
        except DataUnavailable:
            return None
        series = XtDataSource._extract_series(bars, "volume", symbol)
        if len(series) < 2:
            return None
        history = series[:-1]
        if not history:
            return None
        average = sum(history) / float(len(history))
        if not average:
            return None
        return volume / average

    def _kind_of_short(self, short):
        """Map a short code back to its signal kind ('' when unknown)."""
        for kind, code in (self.params.get("signal_codes") or {}).items():
            if short.startswith(code):
                return kind
        return ""

    def _daily_buy_total(self, day, broker_rows):
        """How much was already bought today (broker table vs local journal).

        The larger of the two is used: the broker table is the truth, the
        journal is the fallback, and under-counting the spend would break
        the daily cap. Only orders carrying one of OUR short codes count --
        another program's orders are not our budget.
        """
        buy_signals = self.params.get("buy_signals") or []
        broker_total = 0.0
        for row in broker_rows or []:
            short = short_code_from_remark(row.get("remark"))
            if not short:
                continue
            if self._kind_of_short(short) not in buy_signals:
                continue
            try:
                qty = float(row.get("qty") or 0)
                price = float(row.get("price") or 0)
            except (TypeError, ValueError):
                continue
            broker_total += qty * price
        local_total = 0.0
        for row in self.state.orders(day):
            if row.get("side") == "buy":
                try:
                    local_total += float(row.get("qty") or 0) * float(row.get("price") or 0)
                except (TypeError, ValueError):
                    continue
        return max(broker_total, local_total)

    def decide_with_modes(self, ctx):
        """All signals for one symbol, exits first, each tagged with its arm mode.

        An exit always wins over an entry on the same symbol: selling a name that
        must be sold is more urgent than buying anything.

        The built-in four run in the order they were written. Rules ported from
        rules_form / rules_ladder / rules_factor / rules_sizing / rules_t are
        evaluated after them and carry their OWN mode (shadow / live / off), so a
        freshly ported strategy can be computed and counted for days without ever
        reaching the counter. Returns [(signal, mode), ...] and not a bare list,
        because "the rule fired" and "the rule may place an order" are two
        different facts -- conflating them is how a shadow rule starts spending.
        """
        out = []
        for rule in (rule_take_profit, rule_stop_loss, rule_breakdown_sell):
            signal = rule(self.params, ctx)
            if signal:
                out.append((signal, "live"))
                return out
        signal = rule_pullback_buy(self.params, ctx)
        if signal:
            out.append((signal, "live"))
            return out
        for kind in sorted(_PORTED_RULES):
            fn, _source = _PORTED_RULES[kind]
            try:
                signal = fn(self.params, ctx)
            except Exception as exc:  # noqa: BLE001
                # A rule that raises is a rule that is NOT working: count it and
                # name it, never swallow it into "no signal today".
                self.errors.append("%s:%s" % (kind, repr(exc)[:80]))
                continue
            if not signal:
                continue
            out.append((signal, rule_arm_mode(self.params, kind)))
        return out

    def decide(self, ctx):
        """Just the signals, no arm mode (the shape the existing locks assert).

        Kept as the public read of "what did the rule layer decide", so the
        round's money gate can live in decide_with_modes without weakening the
        determinism/purity tests that only care about the judgement itself.
        """
        return [signal for signal, _mode in self.decide_with_modes(ctx)]

    def run_round(self):
        """Run one round. Returns a summary dict (never raises on broker
        trouble -- a broken broker must not take the process down)."""
        moment = self.clock()
        day = day_key(moment)
        summary = {"day": day, "placed": [], "skipped": [], "shadow": [], "reason": ""}

        try:
            broker_rows = self.broker.orders_today()
        except BrokerUnavailable as exc:
            # Fail closed: without the broker's order table we cannot prove
            # that an intent was not already sent. Sending nothing costs us
            # a trade; sending twice costs us money.
            LOG.warning("[executor] broker order table unreadable (%s): no orders this round", exc)
            summary["reason"] = "broker_orders_unreadable"
            return summary

        done = self.state.short_codes(day)
        for row in broker_rows:
            short = short_code_from_remark(row.get("remark"))
            if short:
                done.add(short)
        # Closed days: the baked fallback list from PARAMS, UNION the ones
        # the Go-produced calendar file adds. The file overrides nothing --
        # it only ever adds days, so a missing file degrades to the baked
        # list rather than to "every day is a trading day".
        closed_days = set(self.params.get("closed_days") or [])
        closed_days |= load_closed_days(self.params.get("calendar_file"))
        positions = self.broker.positions()
        cash = self.broker.available_cash()

        for symbol in self.params.get("symbols", []):
            ctx = self.context_for(symbol, moment, broker_rows, positions, cash, closed_days)
            for signal, mode in self.decide_with_modes(ctx):
                if mode == "shadow":
                    # Shadow leg: compute and record, never spend. The whole
                    # point of the 5-day shadow window is that the judgement can
                    # be compared against the engine while no money is involved.
                    summary["shadow"].append({"short_code": signal["short_code"],
                                              "kind": signal.get("kind"), "symbol": symbol})
                    continue
                if mode != "live":
                    summary["skipped"].append({"short_code": signal["short_code"],
                                               "reason": "rule_not_armed"})
                    continue
                outcome = self._maybe_send(ctx, signal, done, day)
                if outcome.get("sent"):
                    summary["placed"].append(outcome)
                    done.add(signal["short_code"])
                else:
                    summary["skipped"].append({"short_code": signal["short_code"],
                                               "reason": outcome.get("reason")})
        # CANCEL-LEG: the cancel leg closes the same round, after placements.
        # The order is deliberate: build/refresh positions first, then clear
        # stale and pre-close live orders. Reversed, an order placed seconds
        # earlier is seen by the sweep in the same instant and the frequency
        # gate would spend its budget on cancels before any new intent.
        summary["cancels"] = self.cancel_leg(day, broker_rows, moment, closed_days)
        return summary

    def _maybe_send(self, ctx, signal, done, day):
        """Apply every gate, then place. Returns {sent, reason, order_id}."""
        short = signal["short_code"]
        if short in done:
            return {"sent": False, "reason": "already_sent"}
        allowed, reason = rule_no_new_order_window(self.params, ctx)
        if not allowed:
            return {"sent": False, "reason": reason}
        if not rule_ops_frequency_ok(self.params, ctx):
            return {"sent": False, "reason": "ops_frequency_exceeded"}
        fillable, fill_reason = rule_fillable(self.params, ctx, signal["side"])
        if not fillable:
            return {"sent": False, "reason": fill_reason}
        if signal["side"] == "buy":
            if not rule_cash_ok(self.params, ctx, signal["amount"]):
                return {"sent": False, "reason": "insufficient_cash"}
            if not rule_amount_cap_ok(self.params, signal["amount"]):
                return {"sent": False, "reason": "order_amount_cap"}
            if not rule_daily_buy_cap_ok(self.params, ctx, signal["amount"]):
                return {"sent": False, "reason": "daily_buy_cap"}
        remark = build_remark(short)
        self.state.record_op("place")
        ok, order_id, why = self.broker.place_order(signal, remark)
        if not ok:
            LOG.warning("[executor] place rejected %s: %s", short, why)
            return {"sent": False, "reason": "broker_rejected: %s" % (why,)}
        self.state.record_order(day, signal, remark, order_id)
        LOG.info("[executor] sent %s %s qty=%s remark=%s id=%s",
                 signal["side"], signal["symbol"], signal["qty"], remark, order_id)
        return {"sent": True, "reason": "sent", "order_id": order_id, "remark": remark}

    def cancel_leg(self, day, broker_rows, moment, closed_days):
        """P0 execution leg number two: stale-order cancel + close sweep (11-04).

        Why it must exist here: while the bridge lived, CANCEL-STATE and
        CLOSE-RECON did this from the decision side. After 11-05 nothing
        reaches the counter from there -- this process is the only hand left.

        Order of checks, each one fail-closed (rather leave an order alone than
        cancel it twice):
          1. trading day FIRST: on a closed day the reason is "not a trading
             day", never a false "call auction rejected" story (138 posture);
          2. forbidden-cancel window, half-open like the other three copies;
          3. only our own orders (remark carrying our short code) -- an order
             someone placed by hand in the app is not ours to cancel;
          4. terminal rows and UNKNOWN rows are both left alone;
          5. an id already cancelled today is never cancelled again (the
             cancelled_orders trail survives a restart);
          6. places and cancels share one per-minute budget (max_ops_per_min):
             "frequent instant cancellation" is one of the four monitored
             abnormal-trading patterns, so this is self-restraint, not exemption;
          7. cancel when live beyond stale_order_sec, or sweep everything live
             once the clock passes close_sweep_from. A row without order_time
             is never treated as stale -- unknown age is not "old enough".

        Returns {"attempted", "cancelled", "skipped_reason"} for the readout.
        """
        hhmm = hhmm_now(moment)
        trading_day = rule_is_trading_day(self.params, {
            "weekday": moment.weekday(), "day": day, "closed_days": closed_days})
        base = {"trading_day": trading_day, "hhmm": hhmm,
                "ops_last_minute": self.state.ops_since(
                    time.time() - float(self.params["ops_window_sec"]))}
        allowed, reason = rule_no_cancel_window(self.params, base)
        if not allowed:
            return {"attempted": 0, "cancelled": [], "skipped_reason": reason}
        sweep_from = self.params.get("close_sweep_from")
        stale_sec = float(self.params.get("stale_order_sec"))
        already = self.state.cancelled_ids(day)
        out = {"attempted": 0, "cancelled": [], "skipped_reason": ""}
        now_ts = time.time()
        for row in broker_rows or []:
            short = short_code_from_remark(row.get("remark"))
            if not short:
                continue  # not ours: another program or a hand-placed order
            order_id = str(row.get("order_id") or "")
            if not order_id or order_id in already:
                continue
            terminal = row.get("terminal")
            if terminal is not False:
                continue  # True = terminal, None = unknown: do not touch
            # Age baseline: a missing order_time is UNKNOWN age, never "old enough".
            age = None
            try:
                if row.get("order_time") is not None:
                    age = now_ts - float(row.get("order_time"))
            except (TypeError, ValueError):
                age = None
            stale = age is not None and age >= stale_sec
            sweep = bool(sweep_from) and hhmm >= sweep_from
            if not (stale or sweep):
                continue
            if not rule_ops_frequency_ok(self.params, base):
                out["skipped_reason"] = "ops_frequency_exceeded"
                break
            out["attempted"] += 1
            ok, why = self.broker.cancel_order(order_id)
            self.state.record_op("cancel")
            if ok:
                self.state.mark_cancelled(day, order_id)
                already.add(order_id)
                out["cancelled"].append({"order_id": order_id, "short_code": short,
                                         "why": "stale" if stale else "close_sweep"})
            else:
                LOG.warning("[executor] cancel failed id=%s: %s", order_id, why)
                out["skipped_reason"] = "cancel_failed"
        return out


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def build_parser():
    """Command line surface of the executor."""
    parser = argparse.ArgumentParser(
        prog="local_executor",
        description="Local P0 executor: compute signals and trade them locally.")
    parser.add_argument("--account", default=os.environ.get("QMT_ACCOUNT", ""),
                        help="broker account id (default: QMT_ACCOUNT)")
    parser.add_argument("--state-db", default=os.environ.get(
        "LOCAL_EXECUTOR_DB", "executor_state.db"),
        help="path of the local sqlite journal")
    parser.add_argument("--calendar", default=os.environ.get(
        "TRADING_CALENDAR_FILE", ""),
                        help="trading calendar file (closed_days)")
    parser.add_argument("--log-file", default=os.environ.get(
        "LOCAL_EXECUTOR_LOG", ""),
                        help="optional log file (default: stderr only)")
    parser.add_argument("--once", action="store_true",
                        help="run a single round and exit")
    parser.add_argument("--selftest", action="store_true",
                        help="print a health/version readout and exit")
    parser.add_argument("--interval", type=float, default=None,
                        help="seconds between rounds (default: from PARAMS)")
    return parser


def configure_logging(log_file):
    """Send the log to stderr, and to a file when one is configured."""
    handlers = [logging.StreamHandler(sys.stderr)]
    if log_file:
        directory = os.path.dirname(log_file)
        if directory and not os.path.isdir(directory):
            os.makedirs(directory)
        handlers.append(logging.FileHandler(log_file, encoding="utf-8"))
    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s %(message)s",
                        handlers=handlers)


def print_selftest(params):
    """Health and version fingerprint of this artifact.

    The fingerprint is the SHA256 of the running file: the installer and
    the updater compare it to prove that the deployed artifact is the one
    that was built.
    """
    fingerprint = file_sha256(os.path.abspath(__file__))
    params_digest = text_sha256(json.dumps(params, sort_keys=True))
    print("EXEC_SELFTEST version=%s fingerprint=%s params_sha256=%s"
          % (STRATEGY_VERSION, fingerprint, params_digest))
    print("EXEC_PARAMS symbols=%s daily_buy_cap=%s order_amount_cap=%s"
          % (",".join(params.get("symbols", [])), params.get("daily_buy_cap"),
             params.get("order_amount_cap")))
    # ported_load_status's own docstring promises this leg is said out loud in
    # --selftest and in the round summary. Until now it was said in neither: a box
    # running 4 rules instead of the 14 that were tested looked exactly like a
    # healthy box that simply had nothing to say. Counts are printed even when
    # everything loaded (loaded=5 missing=none), because "absent" must not be
    # readable as "not checked".
    status = ported_load_status(params)
    armed = status.get("armed") or {}
    print("EXEC_RULES wanted=%s loaded=%s missing=%s judgement=%s kinds=%d live=%d shadow=%d off=%d"
          % (",".join(status["wanted"]) or "none",
             ",".join(status["loaded"]) or "none",
             ",".join(status["missing"]) or "none",
             ",".join(status.get("judgement") or []) or "none",
             len(status["kinds"]),
             sum(1 for v in armed.values() if v == "live"),
             sum(1 for v in armed.values() if v == "shadow"),
             sum(1 for v in armed.values() if v not in ("live", "shadow"))))
    if status["missing"]:
        # A named module that did not arrive is not "a quiet market": it is a
        # missing file. Say it in its own line so a log grep finds it.
        print("EXEC_RULES_ALERT missing_module=%s" % ",".join(status["missing"]))
    return 0


def main(argv=None):
    """Entry point of the local executor process."""
    args = build_parser().parse_args(argv)
    configure_logging(args.log_file)
    params = dict(PARAMS)
    if args.calendar:
        params["calendar_file"] = args.calendar
    if args.selftest:
        return print_selftest(params)
    if not args.account:
        LOG.error("[executor] no broker account configured (--account / QMT_ACCOUNT)")
        return 2
    data = XtDataSource()
    broker = build_broker(params, args.account)
    if isinstance(broker, DisabledBroker):
        # Say it once at start-up, in the same words the round will use later: without this
        # line a quiet operator reads "placed=0" as "the strategy found nothing today".
        LOG.warning("[executor] xt trading channel is DISABLED (params.xt_channel_enable is "
                    "not set): this process computes and delivers readings only -- the legal "
                    "order path after 2026-11-05 is the QMT built-in strategy (passorder inside "
                    "the sandbox), not xtquant.xttrader")
    ok, reason = broker.connect()
    if not ok:
        LOG.error("[executor] broker not available: %s", reason)
        return 3
    state = LocalState(args.state_db)
    executor = Executor(params, data, broker, state)
    interval = args.interval
    if interval is None:
        interval = float(params["poll_interval_sec"])
    if args.once:
        summary = executor.run_round()
        # shadow is its own field, not folded into skipped: a shadow hit is a
        # *computation that agreed to run* and is the numerator of the 5-day
        # comparison, while skipped is "armed off / gated". Folding them makes the
        # shadow window unmeasurable, which is the whole reason the switch exists.
        status = ported_load_status(params)
        print("EXEC_ROUND day=%s placed=%d skipped=%d shadow=%d rules_missing=%s reason=%s"
              % (summary["day"], len(summary["placed"]), len(summary["skipped"]),
                 len(summary.get("shadow") or []),
                 ",".join(status["missing"]) or "none",
                 summary["reason"]))
        return 0
    while True:
        try:
            summary = executor.run_round()
            if summary["placed"] or summary["reason"]:
                LOG.info("[executor] round day=%s placed=%d skipped=%d reason=%s",
                         summary["day"], len(summary["placed"]),
                         len(summary["skipped"]), summary["reason"])
        except Exception:  # noqa: BLE001 -- one bad round must not kill the loop
            LOG.exception("[executor] round failed")
        time.sleep(interval)


if __name__ == "__main__":
    sys.exit(main())
