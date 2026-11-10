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
STRATEGY_VERSION = "v05-20261109"
PARAMS = {
    'account': '2069008957',
    'asset_guard_factor': 10.0,
    'bd_depth_full_pct': 3,
    'bd_depth_max_score': 30,
    'bd_depth_single_cap': 25,
    'bd_intraday_flat_span_pct': 2,
    'bd_intraday_flat_span_score': 10,
    'bd_intraday_max_score': 30,
    'bd_intraday_mid_base_score': 20,
    'bd_intraday_mid_pct': -2,
    'bd_intraday_severe_pct': -5,
    'bd_intraday_severe_span_pct': 3,
    'bd_intraday_severe_span_score': 10,
    'bd_low_window': 20,
    'bd_min_bars': 25,
    'bd_score_threshold': 60,
    'bd_shadow_add_ratio': 0.5,
    'bd_shadow_add_score': 5,
    'bd_shakeout_depth_pct': 1,
    'bd_shakeout_discount': 0.7,
    'bd_volume_full_ratio': 1.5,
    'bd_volume_half_ratio': 1,
    'bd_volume_max_score': 40,
    'bd_volume_shrink_factor': 0.5,
    'breadth_bands': {'low': 0.3, 'mid': 0.5},
    'breadth_missing_default': 0.9,
    'breadth_missing_scale': 0.9,
    'breadth_scales': {'low': 0.7, 'mid': 0.85, 'high': 1.0},
    'break_down_kind': 'break_down',
    'breakdown_ratio': 0.96,
    'budget_mode': 'dynamic',
    'builtin_rules': ['pullback_buy', 'take_profit', 'stop_loss', 'breakdown_sell'],
    'buy_confirm_min_gap_sec': 60,
    'buy_confirm_rounds': 2,
    'buy_room_guard': True,
    'buy_signals': ['pullback_buy'],
    'close_sweep_from': '14:52',
    'closed_days': [],
    'cluster_cap_pct': 35,
    'cluster_rho': 0.7,
    'concept_map': {},
    'daily_buy_cap': 150000.0,
    'day_loss_limit_pct': 3.5,
    'db_adjust_max_pct': 6,
    'db_break_ratio': 1.05,
    'db_first_vol_multiple': 1.5,
    'db_first_window': 5,
    'db_full_chain_min': 60,
    'db_lookback': 20,
    'db_ma_align_ratio': 0.8,
    'db_ma_mid': 10,
    'db_ma_short': 5,
    'db_ma_weight': 0.3,
    'db_min_bars': 10,
    'db_min_change_pct': 0,
    'db_position_weight': 0.3,
    'db_second_vol_multiple': 1.5,
    'db_volume_weight': 0.4,
    'db_weak_day_pct': -1.5,
    'default_allowed_strategies': ['dragon', 'double_bump', 'n_shape', 'dragon_return', 'factor', 'pattern'],
    'default_buy_threshold': 70,
    'default_direction': 1,
    'default_weight': 1,
    'dr_beak_score_one': 2,
    'dr_beak_score_three': 7,
    'dr_beak_score_two': 4,
    'dr_beak_vr_hi': 1.2,
    'dr_beak_vr_lo': 0.5,
    'dr_confirm_vol_full': 7,
    'dr_confirm_vol_mid': 3,
    'dr_confirm_vr_hi': 1.5,
    'dr_confirm_vr_lo': 1.2,
    'dr_confirm_vr_mid_lo': 0.8,
    'dr_days_good_hi': 8,
    'dr_days_good_lo': 5,
    'dr_days_long_hi': 12,
    'dr_days_score_good': 5,
    'dr_days_score_near': 3,
    'dr_days_score_other': 1,
    'dr_days_short_lo': 3,
    'dr_duck_neck_full': 8,
    'dr_duck_neck_low': 5,
    'dr_duck_slope_bonus': 2,
    'dr_duck_top_full': 5,
    'dr_duck_top_half': 2,
    'dr_first_rise_max': 0.7,
    'dr_first_rise_min': 0.35,
    'dr_k_hold_ratio': 1.03,
    'dr_k_near_lo_ratio': 0.98,
    'dr_k_score_cross': 2,
    'dr_k_score_hold': 5,
    'dr_k_score_near': 2,
    'dr_level_first': 60,
    'dr_ma_long': 20,
    'dr_ma_mid': 10,
    'dr_ma_short': 5,
    'dr_macd_green_near': 1,
    'dr_min_bars': 30,
    'dr_neck_support_ratio': 0.98,
    'dr_nostril_full': 5,
    'dr_nostril_half': 3,
    'dr_pb_best_hi': 0.2,
    'dr_pb_best_lo': 0.15,
    'dr_pb_deep_hi': 0.35,
    'dr_pb_good_hi': 0.25,
    'dr_pb_score_best': 8,
    'dr_pb_score_deep': 4,
    'dr_pb_score_good': 6,
    'dr_pb_score_other': 1,
    'dr_pb_score_shallow': 5,
    'dr_pb_shallow_lo': 0.1,
    'dr_rally_window': 40,
    'dr_score_dragon': 25,
    'dr_sector_rps_min': 75,
    'dr_shrink_max': 0.5,
    'dr_slope_delta_ratio': 0.01,
    'dr_slope_min': 0.5,
    'dr_slope_prev_ratio': 0.99,
    'dr_vr_bad': 0.6,
    'dr_vr_good': 0.3,
    'dr_vr_mid': 0.5,
    'dr_vr_ok': 0.4,
    'dr_vr_score_bad': 3,
    'dr_vr_score_good': 8,
    'dr_vr_score_mid': 5,
    'dr_vr_score_ok': 7,
    'dr_vr_score_other': 1,
    'dragon_f1_base_ratio': 0.9,
    'dragon_f1_vol_ratio': 0.1,
    'dragon_f1_weight': 0.3,
    'dragon_f2_full_pct': 3,
    'dragon_f2_half_pct': 1,
    'dragon_f2_half_ratio': 0.5,
    'dragon_f2_weight': 0.25,
    'dragon_f3_half_ratio': 0.5,
    'dragon_f3_own_gain_pct': 5,
    'dragon_f3_premium_pct': 2,
    'dragon_f3_weight': 0.2,
    'dragon_f4_full_pct': 10,
    'dragon_f4_half_pct': 5,
    'dragon_f4_half_ratio': 0.5,
    'dragon_f4_weight': 0.25,
    'dragon_full_chain_min': 60,
    'dragon_min_bars': 5,
    'dragon_rs_window': 5,
    'dragon_seal_tolerance_pct': 0.5,
    'emergency_stop': True,
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
    'enabled_strategies': ['dragon', 'n_shape', 'fac_1', 'fac_118', 'fac_117', 'fac_136', 'fac_131', 'fac_137', 'fac_123', 'fac_150', 'fac_153', 'fac_154', 'fac_155', 'fac_157', 'fac_156', 'fac_160', 'fac_163', 'fac_162'],
    'entry_side': 'buy',
    'factor_defs': {'Mom5': {'kind': 'momentum', 'windows': [5], 'input': 'bars'}, 'Mom10': {'kind': 'momentum', 'windows': [10], 'input': 'bars'}, 'Mom20': {'kind': 'momentum', 'windows': [20], 'input': 'bars'}, 'Mom60': {'kind': 'momentum', 'windows': [60], 'input': 'bars'}, 'Volatility20': {'kind': 'realized_vol', 'windows': [20], 'input': 'bars'}, 'RealizedVol5': {'kind': 'realized_vol', 'windows': [5], 'input': 'bars'}, 'RealizedVol10': {'kind': 'realized_vol', 'windows': [10], 'input': 'bars'}, 'Amplitude20': {'kind': 'amplitude', 'windows': [20], 'input': 'bars'}, 'AtrRatio14': {'kind': 'atr_ratio', 'windows': [14], 'input': 'bars'}, 'HighLow20': {'kind': 'high_low', 'windows': [20], 'input': 'bars'}, 'VolRatio5': {'kind': 'vol_break_ratio', 'windows': [5], 'input': 'bars'}, 'STOA': {'kind': 'log_amount', 'windows': [20], 'input': 'bars'}, 'Amihud20': {'kind': 'amihud', 'windows': [20], 'input': 'bars'}, 'VolSurge5': {'kind': 'vol_surge', 'windows': [20], 'input': 'bars'}, 'VolShrink': {'kind': 'vol_shrink', 'windows': [5, 20], 'input': 'bars'}, 'VMA5': {'kind': 'vol_ma_ratio', 'windows': [5, 20], 'input': 'bars'}, 'VMA10': {'kind': 'vol_ma_ratio', 'windows': [10, 20], 'input': 'bars'}, 'VSTD20': {'kind': 'vol_cv', 'windows': [20], 'input': 'bars'}, 'VMAX10': {'kind': 'vol_peak', 'windows': [10], 'input': 'bars'}, 'VMIN10': {'kind': 'vol_floor', 'windows': [10], 'input': 'bars'}, 'Brk20': {'kind': 'breakout', 'windows': [20], 'input': 'bars'}, 'Brk60': {'kind': 'breakout', 'windows': [60], 'input': 'bars'}, 'Drawdown20': {'kind': 'drawdown', 'windows': [20], 'input': 'bars'}, 'BullAlign': {'kind': 'bull_align', 'windows': [5, 10, 20], 'input': 'bars'}, 'RSI14': {'kind': 'rsi', 'windows': [14], 'input': 'bars'}, 'BBI': {'kind': 'bbi', 'windows': [3, 6, 12, 24], 'input': 'bars'}, 'EMA10_20': {'kind': 'ema_ratio', 'windows': [10, 20], 'input': 'bars'}, 'Alpha1': {'kind': 'alpha1', 'windows': [5, 20], 'input': 'bars'}, 'Alpha4': {'kind': 'alpha4', 'windows': [9], 'input': 'bars'}, 'Alpha12': {'kind': 'alpha12', 'windows': [1], 'input': 'bars'}, 'Alpha101': {'kind': 'alpha101', 'windows': [], 'input': 'bars'}, 'STO20': {'kind': 'turnover_mean', 'windows': [20], 'input': 'turnover_series', 'missing_input': 'per-day turnover rate series'}, 'TurnoverStd20': {'kind': 'turnover_std', 'windows': [20], 'input': 'turnover_series', 'missing_input': 'per-day turnover rate series'}, 'LnMktCap': {'kind': 'ln_mktcap', 'windows': [], 'input': 'total_share_series', 'missing_input': 'total share count series (share capital panel)'}, 'EP_ttm': {'kind': 'valuation', 'windows': [], 'input': 'valuation_series', 'field': 'pe_ttm', 'missing_input': 'per-day PE/PB/PS/PCF/dividend panel'}, 'BP': {'kind': 'valuation', 'windows': [], 'input': 'valuation_series', 'field': 'pb', 'missing_input': 'per-day PE/PB/PS/PCF/dividend panel'}, 'SP_ttm': {'kind': 'valuation', 'windows': [], 'input': 'valuation_series', 'field': 'ps_ttm', 'missing_input': 'per-day PE/PB/PS/PCF/dividend panel'}, 'CFP_ttm': {'kind': 'valuation', 'windows': [], 'input': 'valuation_series', 'field': 'pcf_ttm', 'missing_input': 'per-day PE/PB/PS/PCF/dividend panel'}, 'DP': {'kind': 'valuation', 'windows': [], 'input': 'valuation_series', 'field': 'dv_ttm', 'missing_input': 'per-day dividend yield panel'}, 'ROE': {'kind': 'financial', 'windows': [], 'input': 'fin_pit', 'field': 'roe', 'direct': True, 'missing_input': 'point-in-time financial report (PIT)'}, 'GrossMargin': {'kind': 'financial', 'windows': [], 'input': 'fin_pit', 'field': 'gross_margin', 'direct': True, 'missing_input': 'point-in-time financial report (PIT)'}, 'NetMargin': {'kind': 'financial', 'windows': [], 'input': 'fin_pit', 'field': 'net_margin', 'direct': True, 'missing_input': 'point-in-time financial report (PIT)'}, 'DebtToAssets': {'kind': 'financial', 'windows': [], 'input': 'fin_pit', 'field': 'debt_to_assets', 'direct': True, 'missing_input': 'point-in-time financial report (PIT)'}, 'YoyNetProfit': {'kind': 'financial', 'windows': [], 'input': 'fin_pit', 'field': 'yoy_net_profit', 'direct': True, 'missing_input': 'point-in-time financial report (PIT)'}, 'SUE': {'kind': 'financial', 'windows': [], 'input': 'fin_pit', 'field': 'single_quarter_ni_yoy', 'direct': True, 'missing_input': 'point-in-time financial report (PIT)'}, 'emo_limit_up': {'kind': 'scalar_proxy', 'windows': [], 'input': 'limit_up_count', 'direct': True, 'missing_input': 'market limit-up history'}, 'emo_max_board': {'kind': 'scalar_proxy', 'windows': [], 'input': 'ladder_height', 'direct': True, 'missing_input': 'market ladder-height history'}, 'emo_blast_rate': {'kind': 'scalar_proxy', 'windows': [], 'input': 'broken_board_ratio', 'direct': True, 'missing_input': 'market blast-rate history'}, 'emo_blast_cnt': {'kind': 'scalar_proxy', 'windows': [], 'input': 'broken_board_count', 'direct': True, 'missing_input': 'market blast-count history'}, 'emo_chg5': {'kind': 'scalar_proxy', 'windows': [], 'input': 'limit_up_count_history', 'direct': True, 'missing_input': 'market limit-up history'}, 'lu_board': {'kind': 'scalar_proxy', 'windows': [], 'input': 'limit_board_count', 'direct': True, 'missing_input': 'per-stock limit-up board history (THS pool)'}, 'lu_board3': {'kind': 'scalar_proxy', 'windows': [], 'input': 'limit_board_count', 'direct': True, 'missing_input': 'per-stock limit-up board history (THS pool)'}, 'lu_seal_strength': {'kind': 'scalar_proxy', 'windows': [], 'input': 'seal_amount_ratio', 'direct': True, 'missing_input': 'seal amount / free-float cap (THS pool)'}, 'lu_early_seal': {'kind': 'scalar_proxy', 'windows': [], 'input': 'first_seal_minute', 'direct': True, 'missing_input': 'first seal minute + break count (THS pool)'}, 'lu_reblast_low': {'kind': 'scalar_proxy', 'windows': [], 'input': 'broken_board_ratio_history', 'direct': True, 'missing_input': 'market blast-rate history (THS pool)'}, 'FlowToday': {'kind': 'scalar_proxy', 'windows': [], 'input': 'flow_today_pct', 'direct': True, 'missing_input': 'eastmoney main-force net inflow, today'}, 'Flow5d': {'kind': 'scalar_proxy', 'windows': [], 'input': 'flow_5d_pct', 'direct': True, 'missing_input': 'eastmoney main-force net inflow, 5 day'}, 'ConceptHeat': {'kind': 'scalar_proxy', 'windows': [], 'input': 'concept_heat', 'direct': True, 'missing_input': 'concept heat ranking (external feed)'}, 'NewsEvent': {'kind': 'scalar_proxy', 'windows': [], 'input': 'news_event', 'direct': True, 'missing_input': 'news agent / LLM event scoring (external)'}, 'IndexChange': {'kind': 'scalar_proxy', 'windows': [], 'input': 'index_change_pct', 'direct': True, 'missing_input': 'index weight / benchmark leg (external)'}, 'IsLeader': {'kind': 'scalar_proxy', 'windows': [], 'input': 'is_leader', 'direct': True, 'missing_input': 'sector leadership ranking (external)'}},
    'factor_id_prefix': 'fac_',
    'factor_rules': [{'factors': ['AtrRatio14', 'Brk60', 'STOA', 'Mom20'], 'weights': {'AtrRatio14': 0.25, 'Brk60': 0.25, 'Mom20': 0.25, 'STOA': 0.25}, 'directions': {'AtrRatio14': -1, 'Brk60': 1, 'Mom20': 1, 'STOA': 1}, 'buy_threshold': 95, 'id': 'fac_1', 'name': 'fac_1'}, {'factors': ['VMIN10', 'VSTD20'], 'weights': {'VMIN10': 0.09116249668631496, 'VSTD20': 0.9088375033136851}, 'directions': {'VMIN10': -1, 'VSTD20': -1}, 'buy_threshold': 70, 'id': 'fac_118', 'name': 'fac_118'}, {'factors': ['VSTD20'], 'weights': {'VSTD20': 1}, 'directions': {'VSTD20': -1}, 'buy_threshold': 70, 'id': 'fac_117', 'name': 'fac_117'}, {'factors': ['VSTD20', 'BBI', 'VolSurge5', 'Mom60', 'TurnoverStd20', 'VMA10', 'VMIN10', 'Mom5'], 'weights': {'BBI': 0.25141725291988254, 'Mom5': 0.08537668192063383, 'Mom60': 0.08537668192063383, 'TurnoverStd20': 0.08537668192063383, 'VMA10': 0.08537668192063383, 'VMIN10': 0.08537668192063383, 'VSTD20': 0.23632265555631443, 'VolSurge5': 0.08537668192063383}, 'directions': {'BBI': -1, 'Mom5': -1, 'Mom60': -1, 'TurnoverStd20': -1, 'VMA10': -1, 'VMIN10': -1, 'VSTD20': -1, 'VolSurge5': -1}, 'buy_threshold': 70, 'id': 'fac_136', 'name': 'fac_136'}, {'factors': ['STOA', 'VSTD20', 'VolSurge5', 'Mom60', 'VMAX10', 'VMIN10'], 'weights': {'Mom60': 0.12983567319796552, 'STOA': 0.11290058538953526, 'VMAX10': 0.15879467335038133, 'VMIN10': 0.03396614111444168, 'VSTD20': 0.2822514634738381, 'VolSurge5': 0.2822514634738381}, 'directions': {'Mom60': -1, 'STOA': -1, 'VMAX10': -1, 'VMIN10': -1, 'VSTD20': -1, 'VolSurge5': -1}, 'buy_threshold': 70, 'id': 'fac_131', 'name': 'fac_131'}, {'factors': ['VSTD20', 'BBI', 'VolSurge5', 'Mom60', 'TurnoverStd20', 'VMA5', 'Brk60', 'VMIN10'], 'weights': {'BBI': 0.389245479614369, 'Brk60': 0.07839915695191352, 'Mom60': 0.07839915695191352, 'TurnoverStd20': 0.07839915695191352, 'VMA5': 0.07839915695191352, 'VMIN10': 0.0025087730224612305, 'VSTD20': 0.2162499626036021, 'VolSurge5': 0.07839915695191352}, 'directions': {'BBI': -1, 'Brk60': -1, 'Mom60': -1, 'TurnoverStd20': -1, 'VMA5': -1, 'VMIN10': -1, 'VSTD20': -1, 'VolSurge5': -1}, 'buy_threshold': 70, 'id': 'fac_137', 'name': 'fac_137'}, {'factors': ['VSTD20', 'STOA', 'VolSurge5', 'DebtToAssets', 'VMIN10', 'VMAX10', 'Mom5', 'TurnoverStd20'], 'weights': {'DebtToAssets': 0.4432234417196832, 'Mom5': 0.06414476477883833, 'STOA': 0.06414476477883833, 'TurnoverStd20': 0.06414476477883833, 'VMAX10': 0.06414476477883833, 'VMIN10': 0.06414476477883833, 'VSTD20': 0.11546057660190902, 'VolSurge5': 0.12059215778421609}, 'directions': {'DebtToAssets': -1, 'Mom5': -1, 'STOA': -1, 'TurnoverStd20': -1, 'VMAX10': -1, 'VMIN10': -1, 'VSTD20': -1, 'VolSurge5': -1}, 'buy_threshold': 70, 'id': 'fac_123', 'name': 'fac_123'}, {'factors': ['VSTD20', 'STOA', 'VolSurge5', 'Mom60', 'TurnoverStd20', 'VMIN10', 'GrossMargin', 'CFP_ttm'], 'weights': {'CFP_ttm': 0.2991346082315521, 'GrossMargin': 0.2095294521008854, 'Mom60': 0.12124008470117403, 'STOA': 0.007775123003495552, 'TurnoverStd20': 0.06479269169579631, 'VMIN10': 0.06479269169579631, 'VSTD20': 0.11662684505243333, 'VolSurge5': 0.11610850351886697}, 'directions': {'CFP_ttm': -1, 'GrossMargin': -1, 'Mom60': -1, 'STOA': -1, 'TurnoverStd20': -1, 'VMIN10': -1, 'VSTD20': -1, 'VolSurge5': -1}, 'buy_threshold': 70, 'id': 'fac_150', 'name': 'fac_150'}, {'factors': ['VMIN10', 'VSTD20', 'VMAX10'], 'weights': {'VMAX10': 0.21664456007890354, 'VMIN10': 0.4421317552630684, 'VSTD20': 0.34122368465802805}, 'directions': {'VMAX10': -1, 'VMIN10': -1, 'VSTD20': -1}, 'buy_threshold': 70, 'id': 'fac_153', 'name': 'fac_153'}, {'factors': ['VSTD20', 'BBI', 'VMIN10', 'Brk60', 'DebtToAssets', 'lu_board3'], 'weights': {'BBI': 0.20965691067779363, 'Brk60': 0.044552093519031125, 'DebtToAssets': 0, 'VMIN10': 0.13103556917362102, 'VSTD20': 0.13103556917362102, 'lu_board3': 0.4837198574559332}, 'directions': {'BBI': -1, 'Brk60': -1, 'DebtToAssets': -1, 'VMIN10': -1, 'VSTD20': -1, 'lu_board3': -1}, 'buy_threshold': 70, 'id': 'fac_154', 'name': 'fac_154'}, {'factors': ['VSTD20', 'BBI', 'VMIN10', 'DebtToAssets'], 'weights': {'BBI': 0.3535353535353535, 'DebtToAssets': 0.25252525252525254, 'VMIN10': 0.1414141414141414, 'VSTD20': 0.25252525252525254}, 'directions': {'BBI': -1, 'DebtToAssets': -1, 'VMIN10': -1, 'VSTD20': -1}, 'buy_threshold': 70, 'id': 'fac_155', 'name': 'fac_155'}, {'factors': ['VSTD20', 'BBI', 'VMIN10'], 'weights': {'BBI': 0.4888276706458524, 'VMIN10': 0.2050811141720232, 'VSTD20': 0.30609121518212423}, 'directions': {'BBI': -1, 'VMIN10': -1, 'VSTD20': -1}, 'buy_threshold': 70, 'id': 'fac_157', 'name': 'fac_157'}, {'factors': ['VSTD20', 'BBI', 'VMIN10', 'lu_board3'], 'weights': {'BBI': 0.31660088350205146, 'VMIN10': 0.09658776136475745, 'VSTD20': 0.17247814529420977, 'lu_board3': 0.41433320983898125}, 'directions': {'BBI': -1, 'VMIN10': -1, 'VSTD20': -1, 'lu_board3': -1}, 'buy_threshold': 70, 'id': 'fac_156', 'name': 'fac_156'}, {'factors': ['VSTD20', 'BBI', 'Brk60', 'lu_board3'], 'weights': {'BBI': 0.1425439217307519, 'Brk60': 0.5729384104946673, 'VSTD20': 0.0855263530384511, 'lu_board3': 0.1989913147361296}, 'directions': {'BBI': -1, 'Brk60': -1, 'VSTD20': -1, 'lu_board3': -1}, 'buy_threshold': 70, 'id': 'fac_160', 'name': 'fac_160'}],
    'factor_thresholds': {},
    'financial_bands': {'BP': {'scale': 1}, 'CFP_ttm': {'scale': 1}, 'DP': {'scale': 5}, 'DebtToAssets': {'offset': 100, 'scale': 100}, 'EP_ttm': {'scale': 1}, 'GrossMargin': {'scale': 40}, 'NetMargin': {'scale': 20}, 'ROE': {'scale': 20}, 'SP_ttm': {'scale': 1}, 'SUE': {'scale': 30}, 'YoyNetProfit': {'scale': 30}},
    'flow_5d_scales': {'mild_in': 1.05, 'mild_out': 0.85, 'neutral': 1, 'strong_in': 1.1, 'strong_out': 0.7},
    'flow_bands': {'mild_in_pct': 1, 'mild_out_pct': -1, 'strong_in_pct': 3, 'strong_out_pct': -3},
    'flow_factor_enabled': True,
    'flow_today_scales': {'mild_in': 1.025, 'mild_out': 0.95, 'neutral': 1, 'strong_in': 1.05, 'strong_out': 0.9},
    'gf_below_ma5_score': 8,
    'gf_event_age_drop': 20,
    'gf_event_age_span': 4,
    'gf_event_fresh_age': 1,
    'gf_event_max_score': 25,
    'gf_event_min_score': 5,
    'gf_event_stale_age': 5,
    'gf_event_window_days': 5,
    'gf_gain_full': 0.25,
    'gf_gain_max_score': 35,
    'gf_gain_span': 0.15,
    'gf_gain_start': 0.1,
    'gf_heavy_ratio': 1.5,
    'gf_heavy_score': 10,
    'gf_min_bars': 20,
    'gf_propagation_discount': 0.7,
    'gf_score_threshold': 60,
    'gf_shadow_ratio': 0.4,
    'gf_shadow_score': 7,
    'gf_stall_pct': 1,
    'gf_stall_score': 15,
    'gf_surge_step_pct': 0.05,
    'gf_surge_window': 3,
    'gf_weakness_max_score': 40,
    'good_news_fade_kind': 'good_news_fade',
    'group_floor_market': 0.45,
    'group_floor_stock': 0.35,
    'hc_below_ma5_score': 10,
    'hc_consec_down_days': 2,
    'hc_consec_down_score': 8,
    'hc_div_full_score': 40,
    'hc_div_half_score': 20,
    'hc_div_heavy_ratio': 1.5,
    'hc_div_stall_pct': 2,
    'hc_gain_base_score': 15,
    'hc_gain_span': 0.2,
    'hc_gain_start': 0.3,
    'hc_gain_step_score': 15,
    'hc_gain_window': 20,
    'hc_high_window': 60,
    'hc_last3_window': 3,
    'hc_min_bars': 60,
    'hc_pos_base_score': 15,
    'hc_pos_span': 0.08,
    'hc_pos_start': 0.92,
    'hc_pos_step_score': 15,
    'hc_position_max_score': 30,
    'hc_score_threshold': 60,
    'hc_shadow_ratio': 0.4,
    'hc_shadow_score': 12,
    'hc_weakness_max_score': 30,
    'high_churn_kind': 'high_churn',
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
    'industry_map': {},
    'kelly_clamp': [0.6, 1.15],
    'kelly_edge_gain': 1.1,
    'kelly_lambda': 0.5,
    'kelly_min_prob': 0.52,
    'kelly_no_edge': 0.6,
    'ladder_buy_amount': 20000.0,
    'ld_big_face_ratio': 0.97,
    'ld_big_loss_ratio': 0.97,
    'ld_big_loss_score': 20,
    'ld_board_base_boards': 2,
    'ld_board_base_score': 12,
    'ld_board_full_boards': 5,
    'ld_board_max_score': 30,
    'ld_board_score_base': 12,
    'ld_board_score_full': 30,
    'ld_board_score_span': 18,
    'ld_board_span_boards': 3,
    'ld_board_span_score': 18,
    'ld_huge_volume_ratio': 2,
    'ld_huge_volume_score': 10,
    'ld_min_bars': 10,
    'ld_min_boards': 2,
    'ld_min_score': 65,
    'ld_phase_score': 10,
    'ld_retreat_cap': 30,
    'ld_retreat_max_score': 30,
    'ld_retreat_phase_keys': ['\u9000\u6f6e'],
    'ld_retreat_tokens': ['\u9000\u6f6e'],
    'ld_score_big_face': 20,
    'ld_score_huge_volume': 10,
    'ld_score_phase': 10,
    'ld_score_sector_drop': 20,
    'ld_score_threshold': 65,
    'ld_score_touched': 15,
    'ld_seal_tolerance': 0.995,
    'ld_sector_drop_ratio': 0.5,
    'ld_sector_drop_score': 20,
    'ld_touch_tolerance': 0.998,
    'ld_touched_score': 15,
    'ld_vol_window': 5,
    'ld_weakness_cap': 40,
    'ld_weakness_max_score': 40,
    'leader_decay_kind': 'leader_decay',
    'live_rules': {'captured_at': '2026-11-06T23:28:48+08:00', 'source': 'C:/var/lib/quant-trading-v2/auth.json#configs[].value + applied_factors.json', 'account_key': 'quant_config_json_v1', 'account_user_id': 'u_1785377969825355000', 'enabled_strategies': ['dragon', 'n_shape', 'fac_1', 'fac_118', 'fac_117', 'fac_136', 'fac_131', 'fac_137', 'fac_123', 'fac_150', 'fac_153', 'fac_154', 'fac_155', 'fac_157', 'fac_156', 'fac_160'], 'applied_factor_ids': ['fac_1', 'fac_118', 'fac_117', 'fac_136', 'fac_135', 'fac_134', 'fac_133', 'fac_130', 'fac_131', 'fac_129', 'fac_138', 'fac_137', 'fac_123', 'fac_145', 'fac_150', 'fac_153', 'fac_154', 'fac_155', 'fac_157', 'fac_156', 'fac_160'], 'qmt': {'enabled': True, 'mode': 'auto', 'gateway_url': 'http://127.0.0.1:8789', 'price_type': 'limit', 'fixed_amount': 40000, 'max_positions': 6, 'initial_capital': 100000, 'strategies': ['dragon', 'n_shape', 'fac_1', 'fac_118', 'fac_117', 'fac_136', 'fac_131', 'fac_137', 'fac_123', 'fac_150', 'fac_153', 'fac_154', 'fac_155', 'fac_157', 'fac_156', 'fac_160'], 'timeout_sec': 10, 'subscription_recognize': False, 'subscription_cancel_guard': False, 'miss_heartbeat_sec': 120, 'daily_max_buys': 0, 'daily_budget_amount': 150000, 'min_buy_price': 0, 'entry_pullback_pct': 0.7, 'entry_pullback_max_gap_pct': 0, 'entry_timeout_sec': 300, 'max_signal_vwap_premium_pct': 5, 'max_signal_vwap_premium_shadow': False, 'min_order_amount': 10000, 'auto_sell_all_sources': True, 'auto_sell': True, 'sell_unified_mode': 'shadow', 'advice': {'add_reopen_drawdown_pct': -5, 'add_signal_active': True, 'hold_min_profit_pct': 0}, 'discipline': {'stop_loss_pct': 0, 'take_profit_pct': 0, 'max_pullback_pct': 0, 'deep_stop_mult': 0, 'buy_confirm_min': 0, 'buy_confirm_high_sec': 0, 'exit_confirm_min': 0, 'trail_confirm_min': 0, 'probe_sec': 0, 'high_conf_threshold': 0, 'sell_signal_max_age_sec': 0, 'max_extends': 0}, 'halted': False, 'signal_gone_cancel_sec': 0, 'cancel_stale_sec': 0, 'close_sweep_at': 0, 'money': {'cash_reserve_ratio': 0, 'min_reserve_amount': 0}, 'settle': {'enabled': False, 'mode': '', 'at': 0}, 'risk_gate': {'day_loss_limit_pct': 3.5, 'sector_value_pct': 35, 'sector_value_shadow': False, 'concept_value_pct': 35, 'concept_value_shadow': False, 'risk_per_trade_pct': 0.5, 'risk_budget_shadow': True, 'single_stock_value_pct': 25, 'stale_quote_ms': 30000, 'limit_up_block_buy': True, 'limit_down_block_sell': True, 'max_order_amount': 0, 'cross_check_pct': 0}, 'auto_risk_caution': True}, 'sizing': {'enabled': False, 'floor': 0, 'cap': 0, 'kelly_lambda': 0, 'kelly_min_prob': 0, 'vol_target_pct': 0, 'vol_clamp_lo': 0, 'vol_clamp_hi': 0, 'breadth_missing_scale': 0, 'macro_lead_days': 0, 'macro_event_day_scale': 0, 'llm_bearish_scale': 0, 'max_total_position_pct': 0, 'per_trade_position_pct': 0, 'per_trade_position_max_pct': 0, 'cluster_rho': 0, 'max_drawdown_pct': 0, 'stoploss_guard_count': 0, 'stoploss_guard_window_days': 0, 'sl_offset_per_pct': 0, 'sl_offset_cap': 0, 'tp_offset_per_pct': 0, 'tp_offset_cap': 0, 'sl_offset_min_index_drop': 0, 'bear_sl_tighten_pt': 0, 'sell_boost_level': '', 't': {}, 'flow_factor': {}, 'evo': {}}, 'strategy': {'dragon': {'f1_seal_weight': 0.3, 'f2_resonance_weight': 0.25, 'f3_premium_weight': 0.2, 'f4_rs_weight': 0.25, 'pullback_max_pct': 0.05, 'breaker_sell_half_pct': 0.08, 'breaker_sell_all_pct': 0.12, 'buy_pullback_sell_half_pct': 0.05, 'buy_pullback_sell_all_pct': 0.08, 'buy_day_close_below': 0.03, 'next_open_if_below': 0.05, 'take_profit_pct': 0}, 'double_bump': {'first_break_volume_multiple': 1.5, 'second_break_volume_multiple': 1.5, 'adjust_vol_ratio_max': 3, 'adjust_days_overflow': 6, 'min_change_pct': 0, 'position_weight': 0.3, 'ma_weight': 0.3, 'volume_weight': 0.4, 'double_bump_take_profit_pct': 0.15}, 'n_shape': {'n_pattern_score_threshold': 60, 'hard_stop_loss': 0.08}, 'dragon_return': {'stop_loss_pct': 0.05, 'take_profit_pct': 0.25, 'max_hold_days': 8, 'target1_multiplier': 1, 'target2_multiplier': 1.25, 'trailing_drawback': 0.08}, 'momentum': {'volume_price_weight': 40, 'macd_weight': 30, 'trend_weight': 30, 'signal_threshold': 0, 'buy_signal_threshold': 0, 'momentum_gate_enabled': False, 'momentum_delta_tol': 0}, 'macro_gate': {'enabled': False, 'levels': None, 'min_confidence': 0, 'block_n_shape': False, 'block_momentum': False}, 'short': {'enabled': None, 'high_churn_min': 0, 'break_down_min': 0, 'leader_decay_min': 0, 'good_news_fade_min': 0, 'margin_rate': 0, 'annual_fee_rate': 0}}, 'paper': {'enabled': True, 'fixed_amount': 0, 'max_positions': 0, 'initial_capital': 0, 'auto_sell': True, 'discipline': {'stop_loss_pct': 0, 'take_profit_pct': 0, 'max_pullback_pct': 0, 'deep_stop_mult': 0, 'buy_confirm_min': 0, 'buy_confirm_high_sec': 0, 'exit_confirm_min': 0, 'trail_confirm_min': 0, 'probe_sec': 0, 'high_conf_threshold': 0, 'sell_signal_max_age_sec': 0, 'max_extends': 0}, 'strategies': ['dragon', 'double_bump', 'n_shape', 'dragon_return', 'momentum', 'fac_1', 'fac_118', 'fac_117', 'fac_131', 'fac_145', 'fac_150'], 'short_capital': 0, 'short_margin_rate': 0, 'short_fee_annual': 0, 'short_fixed_amount': 0, 'short_stop_loss_pct': 0}, 'risk_ctrl': {'compliance': {'compliance_mode': False}, 'm8_enabled': False, 'm8_portfolio_drawdown_pct': 0, 'per_stock_max': 0}, 'runtime': {'trim_after_hours': False, 'trim_interval_min': 0, 'feed_interval_sec': 0, 'qmt_feed_enabled': False, 'qmt_feed_interval_sec': 0, 'qmt_feed_coverage_min_pct': 0, 'qmt_feed_volume_to_shares': 0, 'monitor_pool_hot_max': 0, 'scoring_interval_sec': 0, 'review_max_stocks': 0}, 'position': {'max_total_position_pct': 0, 'auto_track_signals': False, 'atr_enabled': False, 'atr_stop_mult': 0, 'daily_drop_alert_pct': 5}, 'enhance': {'news_decay_enabled': False, 'news_decay_half_life_min': 0, 'auction_signal_enabled': False, 'factor_dedup_enabled': False, 'news_impact_enabled': False, 'sector_linkage_enabled': False, 'market_state_enabled': False, 'orderflow_enabled': False, 'dynweight_enabled': False, 't0_enabled': False, 'tech_filter': {'kdj_j_max': 0, 'ma_filter': ''}}, 'signal_ctl': {}, 'emotion_cycle': {'emo_ice_board_max': 0, 'emo_ice_limitup_max': 0, 'emo_ice_blast_min': 0, 'emo_start_board_max': 0, 'emo_start_limitup_min': 0, 'emo_start_limitup_max': 0, 'emo_start_blast_min': 0, 'emo_start_blast_max': 0, 'emo_ferment_board_max': 0, 'emo_ferment_limitup_min': 0, 'emo_ferment_limitup_max': 0, 'emo_ferment_blast_max': 0, 'emo_climax_board_min': 0, 'emo_climax_limitup_min': 0, 'emo_climax_blast_max': 0, 'emo_diverge_board_drop': 0, 'emo_diverge_limitup_drop': 0, 'emo_diverge_blast_rise': 0, 'emo_retreat_board_max': 0, 'emo_retreat_limitup_max': 0, 'emo_retreat_blast_min': 0, 'emo_breadth_ice_down_ratio': 0, 'emo_breadth_retreat_down_ratio': 0}, 'concept': {'enabled': False, 'shadow': False, 'min_coverage_pct': 0, 'max_staleness_hours': 0}, 'main_sector': {'sector_constituent_top_n': 0}, 'theme': {'black_list': None}},
    'llm_bearish_scale': 0.5,
    'lot_size': 100,
    'ma10_window': 10,
    'ma20_window': 20,
    'ma5_window': 5,
    'ma_period': '1d',
    'ma_window': 5,
    'macro_event_day_scale': 0.3,
    'macro_lead_curve': [0.9, 0.85, 0.8, 0.7, 0.5],
    'macro_lead_days': 5,
    'macro_window_scales': {'high': 0.5, 'low': 1, 'medium': 0.8},
    'market_state_enabled': False,
    'max_drawdown_pct': 0.1,
    'max_ops_per_min': 6,
    'max_positions': 6,
    'max_signal_vwap_premium_pct': 5.0,
    'max_total_position_pct': 1,
    'min_bars': 30,
    'min_cash_reserve': 1000.0,
    'min_order_amount': 10000,
    'min_price': 0.01,
    'min_volume': 1.0,
    'missing_key_target_cap': 0.5,
    'no_cancel_windows': [['09:20', '09:30'], ['14:57', '15:00']],
    'no_new_order_windows': [['09:15', '09:30'], ['14:52', '15:00']],
    'non_trading_weekdays': [5, 6],
    'ops_window_sec': 60,
    'order_amount_cap': 20000.0,
    'pattern_id_prefix': 'pat_',
    'pattern_rules': [{'id': 'pat_1', 'name': 'pattern_template', 'conds': [{'factor': 'Drawdown20', 'min': 0.0, 'max': 0.3}, {'factor': 'VolShrink', 'min': 0.0, 'max': 1.0}, {'factor': 'BullAlign', 'min': 0.5, 'max': 2.0}]}],
    'pct_scale': 100,
    'per_trade_default_hi': 0.3,
    'per_trade_default_lo': 0.1,
    'per_trade_hard_max': 0.5,
    'per_trade_position_max_pct': 0.3,
    'per_trade_position_pct': 0.1,
    'percentile_min_samples': 5,
    'poll_interval_sec': 5,
    'ported_modules': ['rules_form', 'rules_ladder', 'rules_factor', 'rules_t', 'rules_sizing'],
    'power_p': 0.5,
    'projection_overrides': {'single_stock_cap_pct': {'account_value': 25, 'value': 35, 'source': 'owner 2026-11-10 10:45 Q4 (PLAN 3.0)'}, 'sizing_enabled': {'account_value': False, 'value': True, 'source': "owner 2026-11-10 10:45 Q2' (PLAN 3.0)"}},
    'pullback_ratio': 0.98,
    'quality_weight_clamp': [0.9, 1.1],
    'red_block_new_buy': False,
    'reprice_max_per_day': 2,
    'require_trading_day': True,
    'risk_list_dynamic': [],
    'risk_list_static': ['000520.SZ'],
    'rule_arm': {'break_down': 'shadow', 'double_bump': 'shadow', 'dragon': 'live', 'dragon_return': 'shadow', 'factor_composite': 'live', 'good_news_fade': 'shadow', 'high_churn': 'shadow', 'leader_decay': 'shadow', 'leader_decay_v2': 'shadow', 'pattern_template': 'shadow'},
    'rule_side': 'sell',
    'scalar_bands': {},
    'scale_cap': 1.15,
    'scale_floor': 0.15,
    'score_domain': {'divisor': 2, 'floor': 0, 'full': 100, 'shift': 1},
    'sell_boost_level': 'L2',
    'signal_codes': {'break_down': 'BD', 'breakdown_sell': 'BK', 'double_bump': 'DB', 'dragon': 'D', 'dragon_return': 'DR', 'factor_composite': 'FC', 'good_news_fade': 'GN', 'high_churn': 'HC', 'leader_decay': 'LD', 'leader_decay_v2': 'LD1', 'pattern_template': 'PT', 'pullback_buy': 'PB', 'stop_loss': 'SL', 'take_profit': 'TP'},
    'signal_gone_cancel_sec': 0,
    'single_stock_cap_pct': 35.0,
    'sizing_enabled': True,
    'sizing_mode': 'power_mean',
    'sizing_shadow': False,
    'sl_offset_risk_budget_keep': True,
    'stale_order_sec': 900,
    'state_scales': {'bear': 0.4, 'bull': 1, 'range': 0.7},
    'stop_loss_ratio': 0.97,
    'stoploss_guard_count': 4,
    'strategy_kind_to_go': {'dragon': 'dragon', 'double_bump': 'double_bump', 'dragon_return': 'dragon_return'},
    'symbol_head_len': 6,
    'symbols': ['000417.SZ', '000520.SZ', '000530.SZ', '000533.SZ', '000625.SZ', '000652.SZ', '000753.SZ', '000823.SZ', '000831.SZ', '000905.SZ', '000927.SZ', '000980.SZ', '001316.SZ', '001331.SZ', '001379.SZ', '002093.SZ', '002115.SZ', '002124.SZ', '002141.SZ', '002149.SZ', '002214.SZ', '002310.SZ', '002316.SZ', '002347.SZ', '002367.SZ', '002385.SZ', '002415.SZ', '002420.SZ', '002477.SZ', '002488.SZ', '002548.SZ', '002578.SZ', '002594.SZ', '002708.SZ', '002811.SZ', '002889.SZ', '002913.SZ', '003004.SZ', '003006.SZ', '300023.SZ', '300033.SZ', '300104.SZ', '300118.SZ', '300169.SZ', '300292.SZ', '300308.SZ', '300310.SZ', '300367.SZ', '300407.SZ', '300479.SZ', '300489.SZ', '300498.SZ', '301226.SZ', '301361.SZ', '301366.SZ', '301592.SZ', '301630.SZ', '600018.SH', '600089.SH', '600408.SH', '600458.SH', '600609.SH', '600620.SH', '600715.SH', '600717.SH', '600770.SH', '600776.SH', '600805.SH', '600839.SH', '600844.SH', '600881.SH', '600928.SH', '601000.SH', '601009.SH', '601011.SH', '601018.SH', '601022.SH', '601188.SH', '601288.SH', '601328.SH', '601398.SH', '601866.SH', '601939.SH', '601988.SH', '603019.SH', '603121.SH', '603220.SH', '603286.SH', '603323.SH', '603366.SH', '603660.SH', '603706.SH', '603881.SH', '603920.SH', '688130.SH', '920062.SH'],
    't_anchor_ok_sources': ['first_buy_order', 'first_lot'],
    't_bse_board_prefixes': ['4', '8', '92'],
    't_cancelled_status_tokens': ['\u64a4'],
    't_commission_rate': 0.00025,
    't_cost_safety_multiplier': 1.3,
    't_day_loss_limit_pct': 0,
    't_direction_mode': 'both',
    't_enabled': True,
    't_fee_buffer_pct': 0.6,
    't_force_unwind_at': '1450',
    't_ghost_grace_sec': 90,
    't_intraday_cap_pct': 30,
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
    't_min_remaining_position_pct': 50,
    't_min_spread_pct': 2,
    't_min_turnover_pct': 1.5,
    't_missing_key_input': False,
    't_mode': 'both',
    't_near_extreme_pct': 0.3,
    't_no_cancel_windows': [['09:20', '09:30'], ['14:57', '15:00']],
    't_no_new_order_windows': [['09:15', '09:30'], ['14:52', '15:00']],
    't_open_cooling_min': 15,
    't_premise_grace_sec': 120,
    't_regime_allow_states': ['range'],
    't_regime_block_states': ['bull', 'bear'],
    't_regime_gate_enabled': True,
    't_regime_min_votes': 4,
    't_regime_stale_sec': 900,
    't_remark_code_max_len': 16,
    't_round_stop_pct': 2,
    't_sell_env_min_market_scale': 0.2,
    't_session_open_hhmm': '09:30',
    't_session_windows': [['08:30', '15:00']],
    't_shadow': False,
    't_signal_codes': {},
    't_slippage_bps': 5,
    't_st_markers': ['ST'],
    't_stale_order_sec': 900,
    't_stamp_tax_rate': 0.0005,
    't_stop_guard_pct': 1.5,
    't_terminal_status_codes': ['\u5df2\u5e9f', '\u5df2\u6210', '\u5df2\u64a4', '\u5e9f\u5355', '\u90e8\u64a4'],
    't_vol_ratio_max': 1.8,
    't_vwap_check_tol_pct': 0.1,
    't_vwap_dev_pct': 0.8,
    't_wide_board_prefixes': ['30', '68'],
    'take_profit_ratio': 1.05,
    'tier_red_default': 0.3,
    'tier_scales': {'Red': 0.3, 'Yellow': 0.6},
    'unmapped_strategies': ['n_shape'],
    'vol20_window': 20,
    'vol5_window': 5,
    'vol_clamp': [0.5, 1.5],
    'vol_target_pct': 2,
    'volume_ratio_min': 1.5,
    'vwap_reprice_pct': 0,
    'vwap_sanity_hi': 2.0,
    'vwap_sanity_lo': 0.5,
    'zero_amount': 0.0,
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


def execution_mode_from(path):
    """Read the box-level bridge/local interlock, defaulting to bridge.

    CUTOVER-1105: the QMT file bridge is still alive on the owner's machine
    (the announced 11-05 ban did not bite there), so BOTH legs could place
    the same order if the local executor were started by hand. The executor
    must therefore refuse to trade unless an explicit cutover says the bridge
    leg is stopped.

    Every unreadable / malformed / unknown state degrades to "bridge" --
    the money-safe direction. A missing file is NOT permission to trade.
    """
    if not path:
        return "local"  # no interlock configured: today's hand-run behaviour
    try:
        with open(path, "r", encoding="utf-8") as handle:
            doc = json.load(handle)
    except (IOError, OSError, ValueError) as exc:
        LOG.warning("[cutover] unreadable execution mode %s: %s (staying on bridge)", path, exc)
        return "bridge"
    mode = doc.get("mode") if isinstance(doc, dict) else None
    if mode not in ("bridge", "local"):
        LOG.warning("[cutover] unknown execution mode %r (staying on bridge)", mode)
        return "bridge"
    return mode


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


# --- CARRIER-PARITY-1105: pure predicates for the three money-path legs -----
#
# These mirror the Go side one for one (buy_reserve.go sendRoomLimit,
# buy_reprice_20261105.go repriceDeviationPct / repriceWorthCancelling,
# controller.go SweepOrders signal-gone). They live INSIDE the rule layer so
# the sandbox artifact inlines the same arithmetic, and they stay pure: no
# I/O, no clock, no randomness and NO numeric literal -- every threshold and
# even the arithmetic identities come from PARAMS, so a knob change always
# goes through a re-bake.

def rule_leg_seconds(params, key):
    """The enabled value of a money-path leg, or None when it is switched off.

    Both legs ship off from Go (factory 0). Treating "off" as a real threshold
    would make every resting order cancel in the first round.
    """
    value = params[key]
    if not value:
        return None
    return float(value)


def rule_room_guard_on(params):
    """Is the pre-send budget backstop armed? Factory: on."""
    return bool(params["buy_room_guard"])


def rule_room_limit(params, room, room_ok, others, cash, cash_ok):
    """Pure arithmetic of the pre-send backstop -> (limit, has_trustworthy_limit).

    Others' reservations SUBTRACT from the room; the old Go bug added them,
    which let two same-round buys each spend the full remaining budget. When
    the target room is unreadable, fall back to available cash minus others;
    only when BOTH rulers are missing do we report no limit (caller passes).
    """
    zero = params["zero_amount"]
    if others is None or others < zero:
        others = zero
    if room_ok and room is not None:
        return max(room - others, zero), True
    if cash_ok and cash is not None:
        return max(cash - others, zero), True
    return zero, False


def rule_reprice_deviation_pct(params, order_price, vwap):
    """Deviation of a resting limit from VWAP in percent -> (dev, ref_ok)."""
    floor = params["min_price"]
    if order_price is None or vwap is None:
        return None, False
    if order_price < floor or vwap < floor:
        return None, False
    return (order_price - vwap) / vwap * params["pct_scale"], True


def rule_reprice_worth_cancelling(params, dev, threshold):
    """Both directions matter: too high overpays, too low never fills."""
    if dev is None or not threshold:
        return False
    return abs(dev) >= threshold


def rule_is_strategic_buy(params, kind):
    """Only strategic buys are ours to cancel; T legs and manual orders are not."""
    return kind in (params.get("buy_signals") or [])


def rule_strategy_admit(params, go_id):
    """The live-trading tab's strategy switch, mirrored from Go signalctl.admit.

    STRATEGY-SWITCH-SYNC (2026-11-06, owner): the enabled-rule set is the tab's
    whitelist, never a second list invented by the carrier. Two branches, both
    word-for-word the Go semantics:
      * empty whitelist = legacy "all known built-ins": form strategies plus
        fac_/pat_ library rules; momentum and unknown keys must be named;
      * non-empty whitelist = explicit membership only.
    An unrecognisable source key is refused in BOTH branches -- a rule nobody
    can attribute must never become a live order.
    """
    allow = params["enabled_strategies"]
    if not go_id:
        return False
    if not allow:
        if go_id in params["default_allowed_strategies"]:
            return True
        if go_id.startswith(params["factor_id_prefix"]):
            return True
        if go_id.startswith(params["pattern_id_prefix"]):
            return True
        return False
    return go_id in allow
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


def ported_leg_status():
    """Which judgement legs the artifact declares, and which of those it can CALL.

    Two questions, deliberately asked separately: "the module was inlined" was always
    answerable and was never enough -- the 11-04 build carried rules_sizing's code while
    no name could reach its readout, so every shadow row was journalled without a sizing
    number and no screen said so. declared-but-unbound is the shape this line exists to
    make loud.

    In the repository copy both lists are empty (nothing is inlined there), and that is
    reported as "none", never as an error.
    """
    declared = []
    for names in (_PORTED_LEGS_DECLARED or {}).values():
        declared.extend(names or [])
    declared = sorted(set(declared))
    bound = sorted(k for k in declared if callable(_PORTED_LEGS.get(k)))
    return {"declared": declared, "bound": bound,
            "unbound": sorted(set(declared) - set(bound))}


def ported_knob_status(params):
    """Declared knobs of the inlined modules vs what PARAMS actually carries.

    Why this exists next to ported_load_status: "the module arrived" and "the module
    can read its thresholds" are two different facts, and only the first one was ever
    checked. The 11-04 batch is the proof -- rules_form was inlined, registered four
    kinds, passed every build gate, and raised KeyError('bd_min_bars') on its first
    call because 102 of the knobs it declares existed nowhere in the parameter file.
    The builder now refuses such an artifact; this leg is so an operator on a box can
    see the same number without rebuilding anything.

    declared=0 is NOT a pass: it means nothing was inlined (an unbaked source copy),
    which is why the module count is printed alongside it.
    """
    ledger = _PORTED_KNOBS or {}
    missing = []
    declared = 0
    for module in sorted(ledger):
        for key in (ledger.get(module) or []):
            declared += 1
            if key not in params:
                missing.append("%s:%s" % (module, key))
    return {"declared": declared, "modules": sorted(ledger), "missing": sorted(missing)}

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
# JUDGEMENT ACCESSOR (SCARRIER-SHADOW, 2026-11-05)
# ---------------------------------------------------------------------------

# What we ask the sizing leg for, and the name it travels under in the journal.
# Kept as a table so the wire contract and the journal can never drift apart by
# somebody "just adding one more field" in one place only.
#  * market_scale / target  -> typed columns on the brain side (nullable REAL)
#  * hard_block / t_allowed / missing_key_dims -> sizing_note (free ASCII text)
_SHADOW_JUDGEMENT_TYPED = ("market_scale", "target")
_SHADOW_JUDGEMENT_NOTE = ("hard_block", "t_allowed", "missing_key_dims", "emotion_phase")


def _judgement_view(ctx, positions, cash):
    """The account-level view the judgement legs need on top of the per-symbol ctx.

    Why the carriers need this at all: the sizing/T chains were ported from Go code that runs
    inside the engine, where the account book and the market state are one struct. A carrier
    round only has a per-symbol context, so feeding it that context directly makes every
    account-level gate abstain -- which is exactly how "the leg is wired" and "the leg always
    says no" become indistinguishable on screen.

    Deliberately ABSENT here (and the chains decline on them, naming the step):
      * market dims (emotion phase, breadth, index change) -- this box has no market feed;
        they are not invented, they are missing, and the sizing chain's blind-spot cap is the
        designed consequence (same stance as the sandbox's _sizing_view);
      * sector/cluster buckets -- no sector list on the box, so the concentration caps do not
        participate rather than guessing a bucket.
    The basis for a position's value is its cost price, except for the one symbol we hold a
    live tick for. Understating room is allowed; overstating it is the defect the Go side
    pinned. English: cost as the declared basis, live tick only where we actually have one.
    """
    out = dict(ctx or {})
    live = []
    holdings = 0.0
    for symbol, row in (positions or {}).items():
        qty = float((row or {}).get("qty") or 0.0)
        price = (row or {}).get("cost")
        if symbol == out.get("symbol"):
            price = out.get("last_price") or price
        price = float(price or 0.0)
        holdings += qty * price
        live.append({"qty": qty, "price": price})
    out["positions_live"] = live
    out["holdings_value"] = holdings
    avail = cash if cash is not None else out.get("available_cash")
    if avail is not None:
        out["available_cash"] = float(avail)
        out["total_asset"] = holdings + float(avail)
        out["equity"] = out["total_asset"]
    return out


def _ascii_compact(text, limit=48):
    """Keep one judgement field ASCII and short enough to ride inside sizing_note.

    A non-ASCII value is DROPPED, not transcoded: the artifact is pinned pure ASCII by gate g1
    (the client decodes source as GBK), so a non-ASCII string here can only come from a runtime
    value nobody sanitised -- and turning it into '?' (the GBK behaviour this repo has been bitten
    by) would put a plausible-looking corruption into the reconciliation ledger. Dropping it costs
    the explanation only, never the measurement.
    """
    text = str(text or "")[:limit]
    if not text.isascii():
        return ""
    return text.replace(" ", "_")


def t_leg_judgement(params, view):
    """Ask the ported T judgement leg what it would have said about this context.

    One key out (`t_leg`, a compact ASCII string) rather than five: the shadow row carries the
    explanation in a single 200-byte column, and the T answers are explanation, not measurement.
    Absent leg => {} (the row is journalled without it); a raising leg => t_leg_error=..., so
    "the leg is not wired here" and "the leg exists but is broken" stay two different readings.
    """
    fn = _PORTED_LEGS.get("t_readout")
    if not callable(fn):
        return {}
    try:
        raw = fn(params, view) or {}
    except Exception as exc:  # noqa: BLE001 -- a broken judgement leg must not cost the round
        return {"t_leg": "t_leg_error=%s" % _ascii_compact(exc.__class__.__name__, 24)}
    parts = []
    for key in ("t_open_allowed", "t_dir_buy", "t_dir_sell"):
        if key in raw:
            parts.append("%s=%s" % (key[2:], "1" if raw.get(key) else "0"))
    for key in ("t_block_reason", "t_dir_reason"):
        text = _ascii_compact(raw.get(key))
        if text:
            parts.append("%s=%s" % (key[2:], text))
    return {"t_leg": " ".join(parts)} if parts else {}


def sizing_judgement(params, ctx):
    """Ask the ported sizing leg what it would have said about this context.

    Why the lookup goes through _PORTED_LEGS instead of an import or a namespace
    probe: the shipped artifact does not have modules -- scripts/build_local_executor.py
    INLINES the bodies and bakes the declared judgement legs into that one registry.
    An `import rules_sizing` would depend on where the file happens to sit (the exact
    shape this executor already rejected for the rule modules: "a runtime import would
    silently shrink the rule set while every gate stayed green"), and globals()/vars()
    is namespace introspection, which gate g2 forbids in a carrier.

    What it must never do is invent a number: when the leg is not wired (the repo copy, or a
    carrier built without rules_sizing inlined -- AGENTS lists "the judgement legs have no
    call site in either carrier" as a NOT DONE item), it returns {} and the row is journaled
    with NULL judgement columns. 0.0 there would be read as "sizing said zero position",
    turning a gap into a measurement.

    A judgement leg that raises is recorded as such (judgement_error) rather than swallowed:
    "the leg exists but is broken" and "the leg is absent" lead to different repairs.
    """
    fn = _PORTED_LEGS.get("sizing_readout")
    if not callable(fn):
        return {}
    try:
        raw = fn(params, ctx) or {}
    except Exception as exc:  # noqa: BLE001 -- a broken judgement leg must not cost the round
        return {"judgement_error": repr(exc)[:80]}
    out = {}
    for key in _SHADOW_JUDGEMENT_TYPED + _SHADOW_JUDGEMENT_NOTE:
        if key not in raw:
            continue
        value = raw.get(key)
        if value is None:
            continue  # absent stays absent; the journal column is NULL, never 0
        out[key] = value
    return out




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
# Judgement legs: named callables a ported module exposes that are NOT signal
# producers (they answer "how big / allowed at all", they never emit a kind).
# Baked by the builder the same way _PORTED_INLINE is -- an explicit registry on
# purpose: the alternative is the artifact poking at its own namespace
# (globals()/vars()), which gate g2 forbids because a carrier that can introspect
# its own environment can also grow a side channel nobody reviewed. In the
# repository copy this dict is empty, so sizing_judgement answers "{}" -- the
# honest "the leg is not wired here", never a invented number.
_PORTED_LEGS = {}
# Which legs each inlined module declared (module -> names). Baked by the builder from
# each module's own JUDGEMENT_LEGS list; the load gate refuses to publish while any
# declared leg is not bound to a callable in this very file.
_PORTED_LEGS_DECLARED = {}
# Knob ledger: module -> the params keys that module declares it reads.
# Baked by the builder from each module's own *_PARAM_KEYS list; the load gate
# refuses to publish while any declared knob is missing from PARAMS, and
# --selftest prints the same count so a box can see it without rebuilding.
_PORTED_KNOBS = {}
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
            # SHADOW-LEG (SCARRIER-SHADOW, 2026-11-05): the day-keyed journal of every
            # shadow-mode decision, i.e. the NUMERATOR of the 5-trading-day comparison
            # window that docs/IMPL_QMT_INTERNAL_LOOP_20261103.md demands before a ported
            # rule may touch money.
            #
            # Why it has to be a table and not a log line: a log line is not uploadable,
            # not queryable and not dedupable, so nothing on the brain side could ever
            # answer "how close is this carrier's judgement to the engine's" -- the window
            # was unmeasurable and the owner's arm button had no data behind it.
            #
            # Key shape, and why it is not (day, symbol):
            #   (day, short_code) is the SAME key the money path uses for idempotency, so
            #   one intent is one row per day here too. If it were (day, symbol) a second
            #   rule firing on the same name would erase the first one's evidence, and the
            #   per-kind day counts (what the window counts) would come out short.
            #
            # NULL vs 0: amount/judgement are nullable on purpose. "The carrier could not
            # compute the sizing leg" (the judgement legs rules_sizing/rules_t are not
            # wired into the carriers yet -- AGENTS "not done" item) must never be stored
            # as 0.0, because 0 on the comparison table reads as "sizing said zero
            # position", which is a measurement, not a gap.
            conn.execute(
                "CREATE TABLE IF NOT EXISTS shadow_decisions ("
                "day TEXT NOT NULL, short_code TEXT NOT NULL, symbol TEXT NOT NULL,"
                " kind TEXT NOT NULL, side TEXT NOT NULL, qty INTEGER NOT NULL DEFAULT 0,"
                " price REAL NOT NULL DEFAULT 0, amount REAL,"
                " mode TEXT NOT NULL DEFAULT 'shadow', judgement TEXT NOT NULL DEFAULT '',"
                " first_ts REAL NOT NULL, last_ts REAL NOT NULL,"
                " PRIMARY KEY (day, short_code))")
            # CARRIER-PARITY-1105 SIGNAL-GONE: when was each signal last
            # COMPUTED (not sent). The Go side reads this from today's decision
            # table and refuses to cancel anything when the whole chain looks
            # idle. A process restart must not reset the liveness evidence, so
            # it is a table rather than an in-memory dict.
            conn.execute(
                "CREATE TABLE IF NOT EXISTS signal_seen ("
                "day TEXT NOT NULL, short_code TEXT NOT NULL, last_ts REAL NOT NULL,"
                " PRIMARY KEY (day, short_code))")
            # CARRIER-PARITY-1105 BUY-REPRICE: per-code daily cancel budget.
            # The Go side counts these from the orders ledger so a restart
            # cannot hand out a fresh pair of reprices; same reason here.
            conn.execute(
                "CREATE TABLE IF NOT EXISTS reprice_counts ("
                "day TEXT NOT NULL, code TEXT NOT NULL, n INTEGER NOT NULL,"
                " PRIMARY KEY (day, code))")
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

    def record_signal_seen(self, day, short_code, ts):
        """Mark that the decision chain still computes this signal today.

        A signal that was merely COMPUTED counts as alive; whether it was
        armed, sent or skipped is a different question. Mirrors the Go side's
        decision-table liveness, where "no row today" means idle, not gone.
        """
        conn = self._connect()
        try:
            conn.execute(
                "INSERT OR REPLACE INTO signal_seen (day, short_code, last_ts)"
                " VALUES (?,?,?)", (day, short_code, float(ts)))
            conn.commit()
        finally:
            conn.close()

    def signal_last_seen(self, day, short_code):
        """Last computation timestamp for one code, or None when unknown."""
        conn = self._connect()
        try:
            rows = conn.execute(
                "SELECT last_ts FROM signal_seen WHERE day = ? AND short_code = ?",
                (day, short_code)).fetchall()
        finally:
            conn.close()
        return float(rows[0][0]) if rows else None

    def freshest_signal(self, day):
        """Newest computation timestamp today (None when the chain never ran)."""
        conn = self._connect()
        try:
            row = conn.execute(
                "SELECT MAX(last_ts) FROM signal_seen WHERE day = ?", (day,)).fetchone()
        finally:
            conn.close()
        return float(row[0]) if row and row[0] is not None else None

    def reprice_count(self, day, code):
        """How many times this code was cancelled for a VWAP reprice today."""
        conn = self._connect()
        try:
            row = conn.execute(
                "SELECT n FROM reprice_counts WHERE day = ? AND code = ?",
                (day, code)).fetchone()
        finally:
            conn.close()
        return int(row[0]) if row else 0

    def record_reprice(self, day, code):
        """Count one REAL reprice cancel (only called after the broker said ok)."""
        conn = self._connect()
        try:
            row = conn.execute(
                "SELECT n FROM reprice_counts WHERE day = ? AND code = ?",
                (day, code)).fetchone()
            count = int(row[0]) if row else 0
            conn.execute(
                "INSERT OR REPLACE INTO reprice_counts (day, code, n) VALUES (?,?,?)",
                (day, code, count + 1))
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

    # ---------------- SHADOW-LEG (the 5-day comparison numerator) ----------------

    def record_shadow(self, day, signal, judgement=None, mode="shadow"):
        """Journal one shadow-mode decision: computed, counted, never sent to the counter.

        Why the conflict clause keeps first_ts and refreshes the rest:
          * first_ts is when this intent FIRST fired that day -- the row's identity in the
            window, so a later round must not move it (that would make "how many days did
            this rule run" drift with every round);
          * qty/price/amount/judgement/last_ts ARE refreshed: the same short code recomputed
            at 14:30 carries a better judgement than the 09:31 one, and the comparison is
            about the rule's verdict, not about how many times the round woke up.
        A plain INSERT OR REPLACE would have reset first_ts on every round, which silently
        shortens the day count; a plain INSERT OR IGNORE would freeze the FIRST judgement of
        the day, which silently mis-states the comparison. Both were the wrong shape.

        judgement: a dict of what the sizing/T legs would have said (see sizing_judgement
        below). Stored as compact ASCII JSON -- the wire side maps a known subset into typed
        columns, so the journal can gain fields without a schema evolution here.
        """
        blob = ""
        if judgement:
            try:
                blob = json.dumps(judgement, sort_keys=True, ensure_ascii=True, default=str)
            except (TypeError, ValueError):
                # An unserialisable judgement must not cost us the DECISION row: keep the
                # row, drop the explanation, and say so in the blob itself.
                blob = '{"judgement_error":"unserialisable"}'
        conn = self._connect()
        try:
            conn.execute(
                "INSERT INTO shadow_decisions"
                " (day, short_code, symbol, kind, side, qty, price, amount, mode,"
                "  judgement, first_ts, last_ts) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)"
                " ON CONFLICT(day, short_code) DO UPDATE SET"
                "  symbol=excluded.symbol, kind=excluded.kind, side=excluded.side,"
                "  qty=excluded.qty, price=excluded.price, amount=excluded.amount,"
                "  mode=excluded.mode, judgement=excluded.judgement,"
                "  last_ts=excluded.last_ts",
                (day, signal["short_code"], signal["symbol"], signal["kind"], signal["side"],
                 int(signal.get("qty") or 0), float(signal.get("price") or 0.0),
                 None if signal.get("amount") is None else float(signal["amount"]),
                 mode, blob, time.time(), time.time()))
            conn.commit()
        finally:
            conn.close()

    def shadow_rows(self, day=None):
        """Journaled shadow decisions (newest day first; day=None returns every day).

        The uplink reads today's rows through here; --shadow-report reads all of them.
        Returns [] for a day with no rows -- and the CALLER must not read that as "the box
        was silent", which is why shadow_days() exists as the separate "which days reported
        at all" ledger (missing != empty, one row of data, two questions).
        """
        conn = self._connect()
        try:
            if day:
                rows = conn.execute(
                    "SELECT day, short_code, symbol, kind, side, qty, price, amount, mode,"
                    " judgement, first_ts, last_ts FROM shadow_decisions"
                    " WHERE day = ? ORDER BY first_ts, short_code", (day,)).fetchall()
            else:
                rows = conn.execute(
                    "SELECT day, short_code, symbol, kind, side, qty, price, amount, mode,"
                    " judgement, first_ts, last_ts FROM shadow_decisions"
                    " ORDER BY day DESC, first_ts, short_code").fetchall()
        finally:
            conn.close()
        out = []
        for r in rows:
            out.append({"day": r[0], "short_code": r[1], "symbol": r[2], "kind": r[3],
                        "side": r[4], "qty": int(r[5] or 0), "price": float(r[6] or 0.0),
                        "amount": None if r[7] is None else float(r[7]), "mode": r[8],
                        "judgement": r[9], "first_ts": float(r[10] or 0.0),
                        "last_ts": float(r[11] or 0.0)})
        return out

    def shadow_days(self):
        """Days that produced at least one shadow decision, with the row count per day.

        This is the ledger that separates "this rule fired zero times today" from "the box
        never reported today": the first shows up here with the OTHER kinds' rows counted,
        the second does not show up at all. Folding them is what makes a 5-day window
        fill itself with silent days.
        """
        conn = self._connect()
        try:
            rows = conn.execute(
                "SELECT day, COUNT(*) FROM shadow_decisions GROUP BY day ORDER BY day DESC"
            ).fetchall()
        finally:
            conn.close()
        return [(str(r[0]), int(r[1])) for r in rows]


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

    def trades_by_order(self):
        """Today's fills grouped by order id, straight from the counter trade table.

        PARTIAL-FILL-1106 (plan 5.2): the order table's traded_volume is only
        a counter-maintained copy. The trade table is the truth for how much of
        a resting order actually filled, which decides how much may be cancelled
        and how much of the same-key replay is still available. An unreadable
        table yields an EMPTY mapping (caller falls back to the copy and says so),
        never a guessed number and never a raised exception that stops the round.
        """
        if not self.is_connected():
            return {}
        try:
            raw = self._trader.query_stock_trades(self._acc)
        except Exception as exc:  # noqa: BLE001
            # Fall back to the order-table copy when the trade table is unavailable;
            # the caller must label this as a degraded source (never silent).
            LOG.warning("[broker] query_stock_trades unavailable, using order-table copy: %s", exc)
            return {}
        if raw is None:
            return {}
        out = {}
        for item in raw:
            oid = str(getattr(item, "order_id", "") or "")
            if not oid:
                continue
            qty = getattr(item, "traded_volume", 0) or 0
            try:
                out[oid] = out.get(oid, 0.0) + float(qty)
            except (TypeError, ValueError):
                continue
        return out

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
        fills = self.trades_by_order()
        for item in raw:
            remark = getattr(item, "order_remark", "") or getattr(item, "remark", "") or ""
            status, terminal = normalize_order_status(
                getattr(item, "order_status", None), codes, terminals)
            order_id = str(getattr(item, "order_id", "") or "")
            # PARTIAL-FILL-1106: prefer the counter TRADE table. A row with no
            # fill records falls back to the order table's own copy, and the
            # source is named on the row so a degraded readout is never silent.
            from_trades = order_id in fills
            rows.append({
                "order_id": order_id,
                "symbol": str(getattr(item, "stock_code", "") or ""),
                "remark": remark,
                "status": status,
                # CANCEL-LEG three-state flag: True = terminal (leave it),
                # False = live (cancellable), None = UNKNOWN (do not touch).
                "terminal": terminal,
                "qty": getattr(item, "order_volume", 0) or 0,
                "price": getattr(item, "price", 0) or 0,
                # Filled quantity: a partial leg cancels its REMAINDER only.
                "traded": fills.get(order_id, getattr(item, "traded_volume", 0) or 0),
                "traded_source": "trade_table" if from_trades else "order_table_copy",
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
            # CARRIER-PARITY-1105: the Go reprice leg reads a real VWAP from the
            # quote snapshot. Here the only local source is the same tick's
            # cumulative amount/volume; a missing or unusable pair stays None
            # so "no reference price" can never be read as "price is fine".
            "vwap": self._vwap_of(tick),
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

    def _vwap_of(self, tick):
        """Volume-weighted average price of the day, or None when unavailable.

        xtdata reports cumulative volume in lots and cumulative amount in yuan,
        so the per-share average needs the lot size. Units come from PARAMS,
        never from a literal here. A tick with no volume or no amount yields
        None: an absent reference must not become a zero reference.
        """
        volume = tick.get("volume")
        amount = tick.get("amount")
        if not volume or not amount or volume <= 0 or amount <= 0:
            return None
        try:
            return float(amount) / (float(volume) * float(self.params["lot_size"]))
        except (TypeError, ValueError, ZeroDivisionError):
            return None

    def _vwap_for_symbol(self, symbol):
        """Fresh VWAP for one resting order's symbol (None on data trouble)."""
        if not symbol:
            return None
        try:
            raw = self.data.get_full_tick([symbol])
            return self._vwap_of(normalize_tick(symbol, raw.get(symbol) if isinstance(raw, dict) else None))
        except (DataUnavailable, TypeError, ValueError):
            return None

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
            mode = rule_arm_mode(self.params, kind)
            # STRATEGY-SWITCH-SYNC: the live whitelist is the live-trading tab's switch
            # list, derived from the captured snapshot. A library rule carries its
            # own rule_id (fac_*), form rules map by kind. Without a tab-owned id
            # a rule may keep observing in shadow but must never place an order:
            # an unattributed live path is exactly how a switch gets bypassed.
            go_id = signal.get("rule_id") or (
                self.params.get("strategy_kind_to_go") or {}).get(
                    signal.get("kind") or kind, "")
            if go_id:
                if not rule_strategy_admit(self.params, go_id):
                    mode = "switch_off"
            elif mode == "live":
                mode = "switch_off"
            out.append((signal, mode))
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
        # CARRIER-PARITY-1105 BUY-ROOM-GUARD: a context is built ONCE per
        # symbol, BEFORE its signals are evaluated. Two buys from the same
        # context therefore both read the same round-start spend -- the exact
        # window that let a same-round pair each spend the full remaining room
        # in Go. The ledger counts what THIS round has already committed.
        # Resting counter orders are already inside daily_buy_total (the broker
        # table feeds that number); counting them here again would double-charge
        # the budget and silently turn the guard into a buy ban.
        budget = {"reserved": 0.0}
        # LIVENESS: "the chain still computes this signal today" is not the
        # same fact as "an order was sent". Keep it day-keyed in the journal
        # so a restart cannot turn a live signal into a vanished one.
        live_shorts = set()
        now_ts = time.time()

        for symbol in self.params.get("symbols", []):
            ctx = self.context_for(symbol, moment, broker_rows, positions, cash, closed_days)
            # Snapshot of the ledger at the moment this context was built:
            # only reservations made AFTER it are "others" for its signals.
            ctx["round_reserved"] = budget["reserved"]
            for signal, mode in self.decide_with_modes(ctx):
                # A computed signal is alive regardless of arm mode / whether
                # it reaches the counter. Go reads the same liveness from the
                # decision table, and treats "no rows today" as idle.
                self.state.record_signal_seen(day, signal["short_code"], now_ts)
                live_shorts.add(signal["short_code"])
                if mode == "shadow":
                    # Shadow leg: compute and record, never spend. The whole
                    # point of the 5-day shadow window is that the judgement can
                    # be compared against the engine while no money is involved.
                    #
                    # SCARRIER-SHADOW (2026-11-05): "record" now means the journal, not a
                    # log line. A hit that only prints is a hit nobody can count -- the
                    # window was unmeasurable and the arm button had no data behind it.
                    # The sizing leg is attached where it is computable, and simply absent
                    # where it is not (never 0): see sizing_judgement for why.
                    view = _judgement_view(ctx, positions, cash)
                    judgement = sizing_judgement(self.params, view)
                    judgement.update(t_leg_judgement(self.params, view))
                    self.state.record_shadow(day, signal, judgement=judgement)
                    entry = {"short_code": signal["short_code"],
                             "kind": signal.get("kind"), "symbol": symbol,
                             "side": signal.get("side"), "qty": int(signal.get("qty") or 0),
                             "price": float(signal.get("price") or 0.0)}
                    if judgement:
                        entry["judgement"] = judgement
                    summary["shadow"].append(entry)
                    continue
                if mode == "switch_off":
                    # Named reason, not folded into rule_not_armed: tomorrow the
                    # operator needs to know whether to look at rule_arm or at the
                    # live-trading tab switch. Folding them hides a turned-off strategy.
                    summary["skipped"].append({"short_code": signal["short_code"],
                                               "reason": "strategy_switch_off"})
                    continue
                if mode != "live":
                    summary["skipped"].append({"short_code": signal["short_code"],
                                               "reason": "rule_not_armed"})
                    continue
                outcome = self._maybe_send(ctx, signal, done, day, budget)
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
        summary["cancels"] = self.cancel_leg(
            day, broker_rows, moment, closed_days,
            live_shorts=live_shorts, now_ts=now_ts,
            freshest=self.state.freshest_signal(day))
        return summary

    def _maybe_send(self, ctx, signal, done, day, budget=None):
        """Apply every gate, then place. Returns {sent, reason, order_id}.

        budget is the round's shared reservation ledger {"reserved": amount};
        it is optional so callers outside run_round keep the old behaviour,
        but every real round passes it (see CARRIER-PARITY-1105).
        """
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
            # CARRIER-PARITY-1105: the pre-send backstop. The three caps above
            # all read the round-start snapshot; this one subtracts what the
            # round has ALREADY committed, so two buys cannot each spend the
            # full remaining room. Same arithmetic as Go sendRoomLimit.
            if rule_room_guard_on(self.params):
                room = rule_remaining_quota(self.params, ctx)
                # Only the amount committed AFTER this context was built is
                # still invisible to room. Everything older is already in
                # daily_buy_total, so adding it again would charge twice.
                others = (float((budget or {}).get("reserved", 0.0))
                          - float(ctx.get("round_reserved", 0.0)))
                cash_now = ctx.get("available_cash")
                limit, has_limit = rule_room_limit(
                    self.params, room, room is not None,
                    others, cash_now, cash_now is not None)
                if has_limit and signal["amount"] > limit:
                    LOG.info("[executor] room guard refused %s amount=%.0f limit=%.0f",
                             short, signal["amount"], limit)
                    return {"sent": False, "reason": "buy_room_guard"}
        remark = build_remark(short)
        self.state.record_op("place")
        ok, order_id, why = self.broker.place_order(signal, remark)
        if not ok:
            LOG.warning("[executor] place rejected %s: %s", short, why)
            return {"sent": False, "reason": "broker_rejected: %s" % (why,)}
        self.state.record_order(day, signal, remark, order_id)
        # Reserve the amount BEFORE the next signal is evaluated. This is the
        # whole point of the ledger: the broker table will not show this order
        # until the next round, but the next signal in THIS round must see it.
        if budget is not None and signal["side"] == "buy":
            budget["reserved"] = (float(budget.get("reserved", 0.0))
                                  + float(signal.get("amount") or 0.0))
        LOG.info("[executor] sent %s %s qty=%s remark=%s id=%s",
                 signal["side"], signal["symbol"], signal["qty"], remark, order_id)
        return {"sent": True, "reason": "sent", "order_id": order_id, "remark": remark}

    def cancel_leg(self, day, broker_rows, moment, closed_days,
                   live_shorts=None, now_ts=None, freshest=None):
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
        now_ts = time.time() if now_ts is None else float(now_ts)
        # CARRIER-PARITY-1105: the two new cancel reasons join THIS single exit.
        # A second cancel path racing the first is how "cancelled then replaced"
        # incidents happen, so they only add triggers -- never a second broker
        # call, a second frequency debit or a second cancellation trail.
        grace = rule_leg_seconds(self.params, "signal_gone_cancel_sec")
        threshold = rule_leg_seconds(self.params, "vwap_reprice_pct")
        live_shorts = live_shorts or set()
        gone_idle = False
        if grace is not None:
            # "No decision at all today" or "the chain stopped longer ago than
            # the grace" means the decision chain is idle, NOT that every
            # resting signal vanished. Go refuses the whole leg in that case.
            gone_idle = freshest is None or (now_ts - float(freshest)) > grace
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
            # Only strategic BUYS are ours to cancel. Manual orders carry no
            # short code and were already skipped; T legs have their own kind
            # and must keep their own pacing.
            strategic_buy = rule_is_strategic_buy(self.params, self._kind_of_short(short))
            gone = False
            reprice = False
            reprice_code = str(row.get("symbol") or short)
            if strategic_buy and grace is not None and not gone_idle:
                if short not in live_shorts:
                    last = self.state.signal_last_seen(day, short)
                    # missing != zero: a code we have never seen computed is
                    # UNKNOWN, never "gone". Only a seen code past its grace
                    # can be cancelled for this reason.
                    if last is not None and (now_ts - last) > grace:
                        gone = True
            # Partial fills are never repriced: cancelling the remainder would
            # also close the same-key replay for the filled part, turning a
            # price correction into a missing position. No VWAP reference is
            # never "price looks fine".
            if strategic_buy and threshold is not None and not row.get("traded"):
                try:
                    order_price = float(row.get("price") or 0)
                except (TypeError, ValueError):
                    order_price = None
                dev, ref_ok = rule_reprice_deviation_pct(
                    self.params, order_price, self._vwap_for_symbol(row.get("symbol")))
                if (ref_ok and rule_reprice_worth_cancelling(self.params, dev, threshold)
                        and self.state.reprice_count(day, reprice_code)
                        < self.params["reprice_max_per_day"]):
                    reprice = True
            if not (gone or reprice or stale or sweep):
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
                # One reason per cancelled order, priority signal_gone >
                # vwap_reprice > stale > close_sweep. The reason is what the
                # next-day operator reads, so it must name the trigger that
                # actually made us spend a counter cancellation.
                if gone:
                    cancel_why = "signal_gone"
                elif reprice:
                    # Count only REAL cancellations, from the journal, so a
                    # restart cannot hand out a fresh pair of reprices.
                    self.state.record_reprice(day, reprice_code)
                    cancel_why = "vwap_reprice"
                elif stale:
                    cancel_why = "stale"
                else:
                    cancel_why = "close_sweep"
                out["cancelled"].append({"order_id": order_id, "short_code": short,
                                         "why": cancel_why})
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
    # SCARRIER-SHADOW (2026-11-05): read the shadow journal without running a round.
    # Why a CLI and not just the log line: the numerator of the 5-day comparison window is
    # the number the owner presses the arm button against, and until it could be counted
    # per kind per day, "we have been running in shadow for five days" was a feeling.
    parser.add_argument("--shadow-report", action="store_true", dest="shadow_report",
                        help="print shadow-decision counts per kind/day and exit")
    parser.add_argument("--interval", type=float, default=None,
                        help="seconds between rounds (default: from PARAMS)")
    # CUTOVER-1105: the box-level bridge/local interlock. Empty keeps today's
    # behaviour for hand-run rounds; the installed scheduled runner always
    # passes it, so a stray manual start cannot create a second order leg.
    parser.add_argument("--execution-mode-file", default=os.environ.get(
        "LOCAL_EXECUTOR_MODE_FILE", ""), dest="execution_mode_file",
        help="box-level bridge/local cutover state (empty = no interlock)")
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
    # Knob coverage (RULE-KNOBS): a module arriving != its thresholds arriving.
    # This line used to report only modules and kinds, so the 11-04 shape --
    # rules_form listed, all four kinds registered, but 102 of the 105 knobs it
    # declares existing nowhere in the parameter file -- was GREEN on --selftest
    # and only raised KeyError on the first real rule call. The builder refuses
    # such a publish now; this line is so a box can see the same number without
    # rebuilding.
    knobs = ported_knob_status(params)
    legs = ported_leg_status()
    print("EXEC_RULES wanted=%s loaded=%s missing=%s judgement=%s kinds=%d live=%d shadow=%d off=%d"
          " knobs_modules=%d knobs_declared=%d knobs_missing=%d legs_bound=%s legs_unbound=%s"
          % (",".join(status["wanted"]) or "none",
             ",".join(status["loaded"]) or "none",
             ",".join(status["missing"]) or "none",
             ",".join(status.get("judgement") or []) or "none",
             len(status["kinds"]),
             sum(1 for v in armed.values() if v == "live"),
             sum(1 for v in armed.values() if v == "shadow"),
             sum(1 for v in armed.values() if v not in ("live", "shadow")),
             len(knobs["modules"]), knobs["declared"], len(knobs["missing"]),
             ",".join(legs["bound"]) or "none", ",".join(legs["unbound"]) or "none"))
    if legs["unbound"]:
        # Its own line for the same reason missing_module has one: a judgement leg that
        # arrived but cannot be reached makes every shadow row silently lossier.
        print("EXEC_RULES_ALERT judgement_leg_unbound=%s" % ",".join(legs["unbound"]))
    if status["missing"]:
        # A named module that did not arrive is not "a quiet market": it is a
        # missing file. Say it in its own line so a log grep finds it.
        print("EXEC_RULES_ALERT missing_module=%s" % ",".join(status["missing"]))
    if knobs["missing"]:
        # Same posture for a threshold with no owner: its own line, its own names.
        print("EXEC_RULES_ALERT missing_knob=%s" % ",".join(knobs["missing"]))
    return 0


def shadow_journal_probe(path):
    """Is the shadow journal actually there, in which of the three senses of "there"?

    Deliberately a raw read-only sqlite touch, NOT LocalState(path): LocalState creates the
    schema on open, so building one to ask the question would ANSWER it (a "missing journal"
    report would leave a fresh empty file behind and the next report would say "empty").

    Three states, and they must not be folded into one line:
      * missing        -- no file: this box never ran an executor round with a journal here;
      * table_absent   -- a journal from BEFORE SCARRIER-SHADOW: the box is running, but the
                          artifact it holds cannot journal shadow decisions (upgrade the carrier);
      * empty          -- the table exists and holds zero rows: the shadow rules ran but never
                          fired (or nothing ran today at all) -- a real measurement, not a gap.
    """
    if not path or not os.path.exists(path):
        return {"state": "missing", "rows": 0}
    conn = None
    try:
        conn = sqlite3.connect("file:%s?mode=ro" % path, uri=True)
        row = conn.execute(
            "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='shadow_decisions'"
        ).fetchone()
        if not row or int(row[0] or 0) == 0:
            return {"state": "table_absent", "rows": 0}
        n = conn.execute("SELECT COUNT(*) FROM shadow_decisions").fetchone()[0]
        return {"state": "present", "rows": int(n or 0)}
    except sqlite3.Error as exc:
        # Unreadable is its own answer: never report it as "empty" (missing != empty).
        return {"state": "unreadable", "rows": 0, "reason": exc.__class__.__name__}
    finally:
        if conn is not None:
            conn.close()


def print_shadow_report(params, state_db):
    """Print the shadow-decision counts per kind and per day (the 5-day window numerator).

    The numbers printed are the ones the brain side counts: one row per (day, short_code),
    grouped by kind, with the days ledger kept SEPARATE from the kinds ledger -- because
    "this rule fired zero times today" and "the box never reported today" are different
    repairs (one is a quiet strategy, the other is a dead uplink leg).

    No required-days literal lives here: the window length is the plan's acceptance rule and
    its single source is the brain side (internal/server/carrier_shadow.go). We print
    params.shadow_required_days only if the box was baked with one -- absent says "unset".
    """
    probe = shadow_journal_probe(state_db)
    # The FULL path, not a basename: two boxes can both hold "executor_state.db" and the
    # operator comparing two logs needs to see which file each one actually opened.
    # (It also keeps the artifact inside the pinned call allow-list -- os.path.basename
    # is not on it, and widening that list for a print statement is not worth it.)
    print("EXEC_SHADOW_DB path=%s state=%s rows=%d%s"
          % (str(state_db or "unset"), probe["state"], probe["rows"],
             (" reason=%s" % probe["reason"]) if probe.get("reason") else ""))
    if probe["state"] != "present":
        # One dedicated line so a log grep can tell "nothing fired" from "nothing journaled".
        print("EXEC_SHADOW_NO_DATA state=%s next=check_carrier_artifact_or_uplink"
              % probe["state"])
        return 0
    state = LocalState(state_db)
    rows = state.shadow_rows()
    days = state.shadow_days()
    print("EXEC_SHADOW_DAYS %s" % (",".join("%s:%d" % (d, n) for d, n in days) or "none"))
    per_kind = {}
    for r in rows:
        bucket = per_kind.setdefault(r["kind"], {"rows": 0, "days": set(), "codes": set()})
        bucket["rows"] += 1
        bucket["days"].add(r["day"])
        bucket["codes"].add(r["symbol"])
    for kind in sorted(per_kind):
        b = per_kind[kind]
        print("EXEC_SHADOW_KIND kind=%s rows=%d days=%d codes=%d"
              % (kind, b["rows"], len(b["days"]), len(b["codes"])))
        if not b["days"]:
            print("EXEC_SHADOW_KIND_ALERT kind=%s never_fired" % kind)
    for kind, day in sorted((k, d) for k in per_kind for d in per_kind[k]["days"]):
        n = sum(1 for r in rows if r["kind"] == kind and r["day"] == day)
        print("EXEC_SHADOW_CELL kind=%s day=%s rows=%d" % (kind, day, n))
    print("EXEC_SHADOW_SUMMARY kinds=%d days_with_data=%d required=%s"
          % (len(per_kind), max([len(b["days"]) for b in per_kind.values()] or [0]),
             params.get("shadow_required_days", "unset")))
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
    if args.shadow_report:
        # Read-only leg: no broker, no data source, no round, nothing sent. It must be
        # runnable on a box whose QMT client is closed -- that is when somebody asks
        # "how far into the shadow window are we".
        return print_shadow_report(params, args.state_db)
    if not args.account:
        LOG.error("[executor] no broker account configured (--account / QMT_ACCOUNT)")
        return 2
    # CUTOVER-1105: fail closed before touching the broker. --selftest and
    # --shadow-report already returned above, so health checks still work on
    # a box that is deliberately still running the bridge leg.
    mode = execution_mode_from(args.execution_mode_file)
    if mode != "local":
        LOG.warning("[cutover] execution_mode=%s: the QMT bridge is the live order leg; "
                    "this process will NOT place orders (two legs would double-spend). "
                    "Run switch_execution_mode.ps1 -Mode local -Apply -AckBridgeStopped "
                    "to cut over, or pass no --execution-mode-file for a hand-run round.", mode)
        return 4
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
