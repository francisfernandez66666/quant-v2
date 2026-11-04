#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""rules_t 的行为锁（2026-11-04：做 T 内化到物理机上的 py 载体，覆盖原裁决 3）。

为什么这些锁必须存在（不是"测一下功能"，而是"少了会亏钱且不会有任何别的测试变红"）：

  (1) 判定顺序本身就是判据：Go 那条链（intraday_t_live.go:313-338 + openPlan :1207-1299）
      的顺序是逐条对照搬过来的。顺序换了，同一天同一个读数会被归因到不同的 step，
      灰度期唯一的排障地图（被拦分布）就废了；
  (2) 盲区当天绝不开腿：关键读数缺失时目标仓位本身不可信，在盲区里做日内双向操作是资损方向
      （signalctl/sizing.go:277-343 + paper/t_rounds.go:258-263）；
  (3) 方向门不对称：正 T 新增敞口要过环境门，反 T 减敞口要过弱势门 + 当日总量帽；
  (4) 配平数量只认柜台读数：内存累加一旦与委托/成交行分家，就会"以为卖了其实没卖"；
  (5) 重启后从柜台重建：重建不出来的回合判为未知并保持不动手（fail-closed），
      绝不因为"账本没了"就把在途腿当成不存在而再开一条；
  (6) 尾盘两态：正 T 无条件配平（减敞口不需要许可），反 T 买回必须有活跃买入信号且过战略闸门，
      只批部分就整轮转战略减仓（半条腿既不是 done 也不是干净的 converted）；
  (7) 成本线：阈值全部来自 params，成本常量对照 docs/PLAN_T_COST_CALIB_20261027.md:128-130
      与 docs/IMPL_T_COST_CALIB_20261027.md:197；不达标就不动（宁可少做一轮）；
  (8) 短码与现有 build_remark / short_code_from_remark 完全兼容：幂等反查靠的就是它，
      前缀相撞会把止盈单读成 T 单——要么重复下单要么把单子吞掉；
  (9) 形状锁：纯 ASCII、函数体里零数字字面量、零 import、零特权调用，
      以及"代码里读到的每个 params 键都在 T_PARAM_KEYS 在册"（漏一条＝装配侧永远不知道自己少配了）。

所有用例都不需要柜台、时钟或网络：判定是纯函数，读数以 dict 传入。

English: behaviour locks for the ported T state machine -- gate order, blind-zone stop,
asymmetric direction gates, counter-only balance quantities, counter-rebuildable round book
with fail-closed unknown rounds, end-of-day two-state pairing, the cost line, remark-compatible
short codes, and the pure/ASCII/no-literals shape locks.
"""

import ast
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
sys.path.insert(0, GW_DIR)

import rules_t as rt  # noqa: E402

MODULE_PATH = os.path.join(GW_DIR, "rules_t.py")

# 出厂阈值一律取自 Go 的访问器与区间（internal/config/config.go:2098-2565）；
# 成本常量取自 internal/engine/intraday_t_live.go:73-76（与 paper.go:360-382 同源）。
BASE_PARAMS = {
    # 开关 / 方向 / 盲区
    "t_enabled": True,
    "t_mode": "both",
    "t_shadow": False,
    "t_missing_key_input": False,
    "t_max_rounds": 1,
    "t_intraday_cap_pct": 50.0,
    "t_min_spread_pct": 2.0,
    "t_force_unwind_at": "14:50",
    "t_last_open_at": "14:00",
    "t_session_open_hhmm": "09:30",
    "t_open_cooling_min": 15.0,
    # 时间窗（载体统一的 'HH:MM' 写法）
    "t_session_windows": [["09:30", "11:30"], ["13:00", "15:00"]],
    "t_no_new_order_windows": [["09:20", "09:30"], ["14:52", "15:00"]],
    "t_no_cancel_windows": [["09:20", "09:30"], ["14:57", "15:00"]],
    # 环境门
    "t_min_market_scale": 0.40,
    "t_sell_env_min_market_scale": 0.20,
    "t_market_plunge_pct": -2.0,
    "t_intraday_sell_cap_total_pct": 20.0,
    "t_day_loss_limit_pct": 3.0,
    # 行情门 / 量能门（§W-09）
    "t_regime_gate_enabled": True,
    "t_regime_stale_sec": 900.0,
    "t_regime_block_states": ["bull", "bear"],
    "t_regime_allow_states": ["range"],
    "t_vol_ratio_max": 1.8,
    # 日内口径
    "t_vwap_dev_pct": 0.8,
    "t_min_amplitude_pct": 2.5,
    "t_near_extreme_pct": 0.3,
    "t_limit_distance_pct": 1.0,
    "t_vwap_check_tol_pct": 0.1,
    "t_limit_pct_main": 9.9,
    "t_limit_pct_wide": 19.9,
    "t_limit_pct_st": 4.9,
    "t_limit_pct_bse": 29.9,
    "t_wide_board_prefixes": ["30", "68"],
    "t_bse_board_prefixes": ["4", "8", "92"],
    # 否决门 / 锚点
    "t_stop_guard_pct": 1.5,
    "t_round_stop_pct": 2.0,
    "t_anchor_ok_sources": ["first_buy"],
    # 流动性前置
    "t_min_amount_wan": 3000.0,
    "t_min_turnover_pct": 1.5,
    "t_min_listed_days": 60.0,
    "t_st_markers": ["ST"],
    # 额度 / 整手 / 现金
    "lot_size": 100,
    "t_min_base_keep_qty": 100,
    "t_min_remaining_position_pct": 50.0,
    "t_fee_buffer_pct": 0.6,
    # 成本线
    "t_commission_rate": 0.00025,
    "t_min_commission": 5.0,
    "t_stamp_tax_rate": 0.0005,
    "t_slippage_bps": 5.0,
    "t_cost_safety_multiplier": 1.3,
    # 对账与时限
    "t_ghost_grace_sec": 90.0,
    "t_premise_grace_sec": 120.0,
    "t_stale_order_sec": 900.0,
    # 柜台词汇与留痕
    "t_terminal_status_codes": ["53", "54", "56", "57"],
    "t_cancelled_status_tokens": ["cancel", "revoke"],
    "symbol_head_len": 6,
    "signal_codes": {"breakdown_sell": "BK", "pullback_buy": "PB",
                     "stop_loss": "SL", "take_profit": "TP"},
    "t_signal_codes": {"t_buy_first": "TB", "t_buy_first_pair": "TS",
                       "t_sell_first": "XB", "t_sell_pair": "XS",
                       "t_close_out": "TC"},
    "t_remark_code_max_len": 8,
}

NOW_TS = 1_000_000_000.0
DAY = "20261104"
REMARK_VERSION = "v01-20261104"


def params(**overrides):
    out = dict(BASE_PARAMS)
    out.update(overrides)
    return out


def position(**overrides):
    row = {"qty": 10000, "can_use_qty": 10000, "anchor_price": 10.0,
           "anchor_source": "first_buy"}
    row.update(overrides)
    return row


def ctx(**overrides):
    """一份"什么都齐、可以开正 T"的默认上下文（现价 9.85 / 分时均线 10.00 ⇒ 偏离 -1.5%）。"""
    base = {
        "symbol": "600000.SH",
        "day": DAY,
        "hhmm": "10:30",
        "trading_day": True,
        "last_price": 9.85,
        "prev_close": 10.0,
        "high": 10.4,
        "low": 9.8,
        "volume": 4_000_000.0,
        "amount": 40_000_000.0,
        "name": "SP BANK",
        "turnover_pct": 2.0,
        "listed_days": 900,
        "volume_ratio": 1.2,
        "market_scale": 0.6,
        "index_change_pct": 0.5,
        "market_state": "range",
        "market_state_age_sec": 60.0,
        "sizing_missing_dims": [],
        "total_asset": 200_000.0,
        "holdings_value": 100_000.0,
        "available_cash": 50_000.0,
        "strategic_room": 10_000.0,
        "t_frozen_cash": 0.0,
        "t_sell_notional": 0.0,
        "t_rounds_used": 0,
        "t_has_open_leg": False,
        "fresh_buy_signal_id": "sig-1",
        "consumed_signal_ids": [],
        "expected_spread_pct": 2.0,
        "today_realized_pnl": 0.0,
        "discipline_stop_loss_pct": 8.0,
        "discipline_take_profit_pct": 8.0,
        "now_ts": NOW_TS,
        "positions_live": {"600000.SH": position()},
        "orders_today": [],
        "fills_today": [],
    }
    base.update(overrides)
    return base


def order_row(kind, symbol="600000.SH", traded=0.0, price=10.0, terminal=False,
              order_time=None, day=DAY, qty=1000):
    code = BASE_PARAMS["t_signal_codes"][kind]
    head = symbol.split(".")[0]
    return {"remark": "%s-%s%s" % (REMARK_VERSION, code, head), "symbol": symbol,
            "code": symbol, "status": "SUBMITTED", "terminal": terminal, "qty": qty,
            "traded": traded, "price": price,
            "order_time": NOW_TS - 10.0 if order_time is None else order_time, "day": day}


def fill_row(kind, qty=1000, price=10.0, symbol="600000.SH", day=DAY):
    code = BASE_PARAMS["t_signal_codes"][kind]
    head = symbol.split(".")[0]
    return {"remark": "%s-%s%s" % (REMARK_VERSION, code, head), "symbol": symbol,
            "code": symbol, "qty": qty, "price": price, "day": day}


def round_record(direction=rt.T_DIR_BUY_FIRST, first_filled=1000, first_price=10.0,
                 pair_filled=0, status=rt.T_STATE_OPEN, opened_at=None, day=DAY,
                 symbol="600000.SH"):
    return {"id": rt.t_round_id(symbol, direction, day, 1), "symbol": symbol, "day": day,
            "direction": direction, "status": status, "qty": first_filled,
            "first_filled": first_filled, "first_price": first_price,
            "pair_filled": pair_filled, "pair_price": None,
            "opened_at": NOW_TS - 200.0 if opened_at is None else opened_at}


class GateOrderTests(unittest.TestCase):
    """判定顺序与逐条门（顺序错了＝归因地图错了）。"""

    def test_default_context_allows_a_forward_leg(self):
        allowed, reason = rt.t_gate_open(params(), ctx())
        self.assertTrue(allowed, reason)
        self.assertIn(rt.T_DIR_BUY_FIRST, reason)

    def test_stop_switch_is_first_and_wins_over_everything_else(self):
        # t.enabled 是止血位：它必须排在交易时段/盲区/闸门之前（intraday_t_live.go:314）。
        allowed, reason = rt.t_gate_open(
            params(t_enabled=False),
            ctx(trading_day=False, sizing_missing_dims=["index_change"], hhmm="12:00"))
        self.assertFalse(allowed)
        self.assertIn("t_disabled", reason)

    def test_blind_zone_missing_dims_stops_the_whole_day(self):
        # 盲区（关键读数缺失）当天不开腿：缺哪一维必须写进 reason，否则值班无从去查。
        allowed, reason = rt.t_gate_open(params(), ctx(sizing_missing_dims=["index_change"]))
        self.assertFalse(allowed)
        self.assertIn("blind_zone_missing_dims(index_change)", reason)
        action = rt.t_round_state(params(), ctx(sizing_missing_dims=["market_state"]), None)
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertIn("blind_zone_missing_dims", action["reason"])

    def test_shadow_precedes_the_blind_zone(self):
        allowed, reason = rt.t_gate_open(
            params(t_shadow=True), ctx(sizing_missing_dims=["index_change"]))
        self.assertFalse(allowed)
        self.assertIn("shadow_period", reason)

    def test_session_and_windows_are_judged_before_the_market_gates(self):
        allowed, reason = rt.t_gate_open(params(), ctx(hhmm="12:00"))
        self.assertFalse(allowed)
        self.assertIn("outside_continuous_session", reason)
        # 时段闸先于禁挂闸（Go 的 IsActiveSession 在轮次装配之前）；14:55 既在连续竞价时段内、
        # 又落进"禁挂 14:52-15:00"，才轮到窗口这条。
        allowed, reason = rt.t_gate_open(params(), ctx(hhmm="14:55"))
        self.assertFalse(allowed)
        self.assertIn("no_new_order_window", reason)

    def sell_ctx(self, **overrides):
        """高抛侧的默认上下文：现价就在日内高点附近（否则先撞 step4 位置门，测不到 step5）。"""
        base = {"last_price": 10.15, "high": 10.16, "low": 9.9, "prev_close": 10.1}
        base.update(overrides)
        return ctx(**base)

    def test_forward_env_gate_and_reverse_env_gate_are_different_numbers(self):
        # 正 T 新增敞口 ⇒ 严（0.40）；反 T 减敞口 ⇒ 宽（0.20）。
        allowed, reason = rt.t_gate_open(params(), ctx(market_scale=0.30))
        self.assertFalse(allowed)
        self.assertIn("buy_first_env_gate", reason)
        allowed, reason = rt.t_gate_open(params(), self.sell_ctx(market_scale=0.30))
        self.assertTrue(allowed, reason)

    def test_reverse_leg_refused_in_extreme_weakness(self):
        # 反 T 环境门关闭时不开反 T。
        allowed, reason = rt.t_gate_open(params(), self.sell_ctx(market_scale=0.10))
        self.assertFalse(allowed)
        self.assertIn("sell_first_env_gate", reason)

    def test_index_plunge_stops_forward_only(self):
        allowed, reason = rt.t_gate_open(params(), ctx(index_change_pct=-3.0))
        self.assertFalse(allowed)
        self.assertIn("index_plunge", reason)
        allowed, reason = rt.t_gate_open(params(), self.sell_ctx(index_change_pct=-3.0))
        self.assertTrue(allowed, reason)

    def test_daily_sell_cap_uses_counter_fills(self):
        # 当日反 T 总量帽：分母 min(总资产, 持仓市值)=100000 × 20% = 20000，已卖 20000 ⇒ 不再开卖腿。
        sold = fill_row(rt.KIND_SELL_FIRST, qty=2000, price=10.0)
        allowed, reason = rt.t_gate_open(
            params(), self.sell_ctx(t_sell_notional=None, fills_today=[sold]))
        self.assertFalse(allowed)
        self.assertIn("sell_cap_exhausted", reason)

    def test_liquidity_and_volume_readings_decline_when_missing(self):
        # "读不到"一律按不过：流动性三项与量能一项各自点名（intraday_t_live.go:1328-1365 / 2194）。
        for field in ("amount", "turnover_pct", "listed_days"):
            blocked, reason = rt.t_liquidity_gate(params(), ctx(**{field: None}))
            self.assertTrue(blocked, field)
            self.assertIn("unavailable", reason)
        blocked, reason = rt.t_volume_gate(params(), ctx(volume_ratio=None))
        self.assertTrue(blocked)
        self.assertIn("volume_ratio_unavailable", reason)
        blocked, _why = rt.t_volume_gate(params(t_vol_ratio_max=0.0), ctx(volume_ratio=None))
        self.assertFalse(blocked)  # 0 是合法配置＝这条门不设

    def test_st_name_is_never_a_t_candidate(self):
        allowed, reason = rt.t_gate_open(params(), ctx(name="ST STOCK"))
        self.assertFalse(allowed)
        self.assertIn("ST_or_delisting_name", reason)

    def test_open_leg_refused_while_a_leg_is_in_flight(self):
        allowed, reason = rt.t_gate_open(params(), ctx(t_has_open_leg=True))
        self.assertFalse(allowed)
        self.assertIn("step1", reason)
        self.assertIn("already in flight", reason)

    def test_anchor_must_be_provable(self):
        allowed, reason = rt.t_gate_open(
            params(), ctx(positions_live={"600000.SH": position(anchor_source="cost_fallback")}))
        self.assertFalse(allowed)
        self.assertIn("anchor_not_provable", reason)

    def test_missing_price_or_vwap_declines_without_falling_back_to_cost(self):
        allowed, reason = rt.t_gate_open(params(), ctx(last_price=None))
        self.assertFalse(allowed)
        self.assertIn("no_realtime_price", reason)
        allowed, reason = rt.t_gate_open(params(), ctx(amount=None, volume=None))
        self.assertFalse(allowed)
        self.assertIn("vwap_untrustworthy", reason)

    def test_intraday_position_gates(self):
        allowed, reason = rt.t_gate_open(params(), ctx(high=10.1, low=9.95))
        self.assertFalse(allowed)
        self.assertIn("amplitude", reason)
        allowed, reason = rt.t_gate_open(params(), ctx(low=9.85))
        self.assertFalse(allowed)
        self.assertIn("buy_first_position_gate", reason)
        allowed, reason = rt.t_gate_open(params(), ctx(last_price=10.98))
        self.assertFalse(allowed)
        self.assertIn("near_limit", reason)

    def test_minute_chain_disagreement_stops_but_absence_only_degrades(self):
        allowed, reason = rt.t_gate_open(params(), ctx(vwap_minute_chain=10.5))
        self.assertFalse(allowed)
        self.assertIn("vwap_cross_check_mismatch", reason)
        allowed, _reason = rt.t_gate_open(params(), ctx(vwap_minute_chain=None))
        self.assertTrue(allowed)

    def test_time_gates(self):
        allowed, reason = rt.t_gate_open(params(), ctx(hhmm="14:10"))
        self.assertFalse(allowed)
        self.assertIn("last_open_at", reason)
        allowed, reason = rt.t_gate_open(params(), ctx(hhmm="09:35"))
        self.assertFalse(allowed)
        self.assertIn("cooling", reason)
        allowed, reason = rt.t_gate_open(params(t_max_rounds=1), ctx(t_rounds_used=1))
        self.assertFalse(allowed)
        self.assertIn("step2 round_count_used", reason)

    def test_regime_gate_blocks_trend_and_abstains_on_stale(self):
        allowed, reason = rt.t_gate_open(params(), ctx(market_state="bear"))
        self.assertFalse(allowed)
        self.assertIn("regime_gate", reason)
        allowed, reason = rt.t_gate_open(params(), ctx(market_state="sideways"))
        self.assertFalse(allowed)
        self.assertIn("fail closed", reason)
        allowed, reason = rt.t_gate_open(params(), ctx(market_state="", market_state_age_sec=None))
        self.assertTrue(allowed, reason)  # 空读数＝弃权，不是拦
        allowed, reason = rt.t_gate_open(params(), ctx(market_state="bull",
                                                      market_state_age_sec=5000.0))
        self.assertTrue(allowed, reason)  # 过期＝弃权：不许拿旧结论做动作

    def test_cross_account_opposite_leg_blocks(self):
        allowed, reason = rt.t_gate_open(params(), ctx(opposite_leg_blocked=True))
        self.assertFalse(allowed)
        self.assertIn("step1.5", reason)

    def test_strategic_verdict_outranks_t(self):
        allowed, reason = rt.t_gate_open(params(), ctx(strategic_busy=True))
        self.assertFalse(allowed)
        self.assertIn("step6", reason)

    def test_reverse_leg_vetoed_by_an_active_sell_verdict(self):
        allowed, reason = rt.t_gate_open(
            params(), self.sell_ctx(active_sell_verdict=True))
        self.assertFalse(allowed)
        self.assertIn("sell_veto", reason)

    def test_reverse_leg_vetoed_when_stop_line_distance_is_unreadable(self):
        allowed, reason = rt.t_gate_open(
            params(), self.sell_ctx(discipline_stop_loss_pct=None,
                                    discipline_take_profit_pct=None))
        self.assertFalse(allowed)
        self.assertIn("distance to the stop lines is unreadable", reason)

    def test_reverse_leg_vetoed_close_to_the_risk_lines(self):
        # 锚点 10.0、止盈 5% ⇒ 线在 10.5；现价 10.47 ⇒ 距离 0.29% < stop_guard 1.5% ⇒ 否决反 T。
        allowed, reason = rt.t_gate_open(
            params(), ctx(last_price=10.47, high=10.48, low=10.1,
                          discipline_stop_loss_pct=3.0, discipline_take_profit_pct=5.0))
        self.assertFalse(allowed)
        self.assertIn("from the stop lines", reason)

    def test_forward_leg_needs_a_fresh_unconsumed_buy_signal(self):
        allowed, reason = rt.t_gate_open(params(), ctx(fresh_buy_signal_id=""))
        self.assertFalse(allowed)
        self.assertIn("no_fresh_buy_signal", reason)
        allowed, reason = rt.t_gate_open(
            params(), ctx(fresh_buy_signal_id="sig-1", consumed_signal_ids=["sig-1"]))
        self.assertFalse(allowed)
        self.assertIn("signal_already_consumed", reason)

    def test_forward_leg_refused_when_the_base_cannot_cover_pairing(self):
        # T+1：当日买入不可卖，底仓可卖量必须覆盖本腿，否则开出来就是注配不上的半态腿。
        allowed, reason = rt.t_gate_open(
            params(), ctx(positions_live={"600000.SH": position(can_use_qty=300)}))
        self.assertFalse(allowed)
        self.assertIn("step7", reason)
        self.assertIn("could not pair it", reason)

    def test_reverse_leg_keeps_half_the_base_and_one_lot(self):
        # 总量帽关掉后只剩"单票帽 50%"与"卖后留 50%"：10000 股 ⇒ 最多 5000。
        qty, _reason = rt.t_open_qty(params(t_intraday_sell_cap_total_pct=0.0),
                                     self.sell_ctx(), rt.T_DIR_SELL_FIRST,
                                     rt.t_cap_base(params(), ctx(last_price=10.15)))
        self.assertEqual(qty, 5000)
        # 当日总量帽（min(总资产,持仓市值) × 20% = 20000 元）才是更紧的那条。
        qty, _reason = rt.t_open_qty(params(), self.sell_ctx(), rt.T_DIR_SELL_FIRST,
                                     rt.t_cap_base(params(), self.sell_ctx()))
        self.assertEqual(qty, 1900)
        qty, reason = rt.t_open_qty(
            params(),
            self.sell_ctx(positions_live={"600000.SH": position(qty=100, can_use_qty=100)}),
            rt.T_DIR_SELL_FIRST,
            rt.t_cap_base(params(), self.sell_ctx()))
        self.assertEqual(qty, 0)
        self.assertIn("below_one_lot", reason)

    def test_equity_or_sellable_reading_absent_declines(self):
        allowed, reason = rt.t_gate_open(params(), ctx(total_asset=None))
        self.assertFalse(allowed)
        self.assertIn("equity_or_holdings_value_unavailable", reason)
        allowed, reason = rt.t_gate_open(
            params(), ctx(positions_live={"600000.SH": position(available_qty=None,
                                                               can_use_qty=None)}))
        self.assertFalse(allowed)
        self.assertIn("sellable_qty_unavailable", reason)


class CostLineTests(unittest.TestCase):
    """单笔 T 的成本线（docs/PLAN_T_COST_CALIB_20261027.md:128-130、IMPL:197）。"""

    def test_cost_formula_matches_the_broker_rates_in_the_repo(self):
        # 4000 股 @ 9.85：佣金 9.85/9.85、印花 19.7、滑点 39.4 ⇒ 合计 78.8 元。
        ok, _why, detail = rt.t_cost_gate(params(), ctx(t_plan_qty=4000))
        self.assertTrue(ok)
        self.assertAlmostEqual(detail["fee_buy"], 9.85, places=2)
        self.assertAlmostEqual(detail["fee_sell"], 9.85, places=2)
        self.assertAlmostEqual(detail["stamp_tax"], 19.7, places=2)
        self.assertAlmostEqual(detail["slippage_yuan"], 39.4, places=2)
        self.assertAlmostEqual(detail["cost_yuan"], 78.8, places=2)

    def test_min_commission_floor_bites_on_small_legs(self):
        _ok, _why, detail = rt.t_cost_gate(params(), ctx(t_plan_qty=100))
        self.assertAlmostEqual(detail["fee_buy"], 5.0, places=2)
        self.assertAlmostEqual(detail["fee_sell"], 5.0, places=2)

    def test_small_leg_cannot_clear_its_own_cost(self):
        # 100 股 @ 9.85 的成本约 12.7 元 ⇒ 需价差约 1.3%；只给 0.2% 就不过线。
        ok, reason, _detail = rt.t_cost_gate(
            params(), ctx(t_plan_qty=100, expected_spread_pct=0.2))
        self.assertFalse(ok)
        self.assertIn("cost_line_not_met", reason)

    def test_cost_line_missing_reading_declines(self):
        ok, reason, _detail = rt.t_cost_gate(params(), ctx(t_plan_qty=100,
                                                           expected_spread_pct=None))
        self.assertFalse(ok)
        self.assertIn("cost_gate_missing_expected_spread", reason)

    def test_gate_refuses_the_leg_when_the_cost_line_is_not_met(self):
        # 成本线不达标 ⇒ 整条开腿链不动手（step9）。
        allowed, reason = rt.t_gate_open(params(), ctx(expected_spread_pct=0.01))
        self.assertFalse(allowed)
        self.assertIn("step9 cost_line_not_met", reason)
        action = rt.t_round_state(params(), ctx(expected_spread_pct=0.01), None)
        self.assertEqual(action["action"], rt.ACTION_HOLD)

    def test_safety_multiplier_is_a_parameter(self):
        _ok, _why, loose = rt.t_cost_gate(params(t_cost_safety_multiplier=1.0),
                                          ctx(t_plan_qty=100))
        _ok, _why, tight = rt.t_cost_gate(params(t_cost_safety_multiplier=2.0),
                                          ctx(t_plan_qty=100))
        self.assertGreater(tight["required_spread_pct"], loose["required_spread_pct"])


class RoundStateTests(unittest.TestCase):
    """在途回合的下一步：数量只认柜台，尾盘两态，绝不叠单。"""

    def test_balance_quantity_comes_from_the_counter_not_memory(self):
        # 账本里的 qty 故意写错（5000）：结果必须跟着成交行走（1000），改成交行结果就变。
        fills = [fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)]
        record = round_record(first_filled=5000, first_price=10.0)
        c = ctx(fills_today=fills, orders_today=[order_row(rt.KIND_BUY_FIRST, traded=1000)],
                last_price=10.25)
        action = rt.t_round_state(params(), c, record)
        self.assertEqual(action["action"], rt.ACTION_BALANCE)
        self.assertEqual(action["qty"], 1000)
        self.assertEqual(action["side"], "sell")
        self.assertEqual(action["short_code"], "TS600000")
        # 成交改小 ⇒ 配平数量跟着变小（证明不是内存累加出来的数）。
        fewer = [fill_row(rt.KIND_BUY_FIRST, qty=700, price=10.0)]
        action2 = rt.t_round_state(
            params(), ctx(fills_today=fewer, orders_today=[
                order_row(rt.KIND_BUY_FIRST, traded=700)], last_price=10.25), record)
        self.assertEqual(action2["qty"], 700)

    def test_orders_traded_used_when_no_fill_report_arrived_yet(self):
        orders = [order_row(rt.KIND_BUY_FIRST, traded=800, price=10.0, terminal=True, qty=800)]
        action = rt.t_round_state(params(), ctx(fills_today=[], orders_today=orders,
                                                last_price=10.25), round_record())
        self.assertEqual(action["action"], rt.ACTION_BALANCE)
        self.assertEqual(action["qty"], 800)

    def test_fills_and_orders_are_never_added_together(self):
        # 两条都写 1000：若相加会配平 2000 股（多卖一倍底仓）。
        rows = [order_row(rt.KIND_BUY_FIRST, traded=1000, terminal=True)]
        fills = [fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)]
        action = rt.t_round_state(params(), ctx(fills_today=fills, orders_today=rows,
                                                last_price=10.25), round_record())
        self.assertEqual(action["qty"], 1000)

    def test_partial_pair_only_the_gap_is_balanced(self):
        fills = [fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0),
                 fill_row(rt.KIND_BUY_FIRST_PAIR, qty=400, price=10.3)]
        action = rt.t_round_state(params(), ctx(fills_today=fills, last_price=10.3),
                                  round_record())
        self.assertEqual(action["action"], rt.ACTION_BALANCE)
        self.assertEqual(action["qty"], 600)

    def test_matched_legs_hold_and_seal_as_done(self):
        fills = [fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0),
                 fill_row(rt.KIND_BUY_FIRST_PAIR, qty=1000, price=10.3)]
        action = rt.t_round_state(params(), ctx(fills_today=fills, last_price=10.3),
                                  round_record())
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertEqual(action["terminal"], rt.T_STATE_DONE)

    def test_spread_not_met_holds(self):
        action = rt.t_round_state(
            params(), ctx(fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)],
                          last_price=10.05), round_record())
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertIn("spread_not_met", action["reason"])

    def test_pair_leg_in_flight_is_never_stacked(self):
        rows = [order_row(rt.KIND_BUY_FIRST, traded=1000, terminal=True),
                order_row(rt.KIND_BUY_FIRST_PAIR, traded=0, terminal=False)]
        action = rt.t_round_state(
            params(), ctx(fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)],
                          orders_today=rows, last_price=10.3), round_record())
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertIn("never stack", action["reason"])

    def test_stale_pair_order_is_cancelled(self):
        rows = [order_row(rt.KIND_BUY_FIRST, traded=1000, terminal=True),
                order_row(rt.KIND_BUY_FIRST_PAIR, traded=0, terminal=False,
                          order_time=NOW_TS - 2000.0)]
        action = rt.t_round_state(
            params(), ctx(fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)],
                          orders_today=rows, last_price=10.3), round_record())
        self.assertEqual(action["action"], rt.ACTION_CANCEL)

    def test_cancel_respects_the_no_cancel_window(self):
        rows = [order_row(rt.KIND_BUY_FIRST, traded=1000, terminal=True),
                order_row(rt.KIND_BUY_FIRST_PAIR, traded=0, terminal=False,
                          order_time=NOW_TS - 2000.0)]
        action = rt.t_round_state(
            params(), ctx(hhmm="14:58",
                          fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)],
                          orders_today=rows, last_price=10.3), round_record())
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertIn("no_cancel_window", action["reason"])

    def test_accel_stop_pairs_early_without_the_spread(self):
        # 价差已亏到 round_stop_pct ⇒ 跳过价差门立刻配平（intraday_t_live.go:2278-2282）。
        action = rt.t_round_state(
            params(), ctx(fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)],
                          last_price=9.75, now_ts=NOW_TS + 1000.0),
            round_record(opened_at=NOW_TS))
        self.assertEqual(action["action"], rt.ACTION_BALANCE)
        self.assertIn("accel=stop", action["reason"])

    def test_leg_age_grace_protects_the_stop_trigger(self):
        action = rt.t_round_state(
            params(), ctx(fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)],
                          last_price=9.75), round_record(opened_at=NOW_TS))
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertIn("spread_not_met", action["reason"])

    def test_forward_leg_sellable_short_falls_to_close_out_at_force(self):
        c = ctx(hhmm="14:55",
                fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)],
                last_price=10.3,
                positions_live={"600000.SH": position(can_use_qty=200)})
        action = rt.t_round_state(params(), c, round_record())
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertEqual(action["terminal"], rt.T_STATE_UNWOUND)
        self.assertIn("forced_pair_failed", action["reason"])

    def test_end_of_day_forces_the_forward_pair_even_at_a_loss(self):
        # 减敞口不需要许可：正 T 到点无条件卖出等量底仓（owner 三轮裁决唯一的时钟义务）。
        c = ctx(hhmm="14:55",
                fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)],
                last_price=9.9)
        action = rt.t_round_state(params(), c, round_record())
        self.assertEqual(action["action"], rt.ACTION_BALANCE)
        self.assertIn("end of day", action["reason"])

    def test_end_of_day_reverse_without_signal_converts(self):
        c = ctx(hhmm="14:55",
                fills_today=[fill_row(rt.KIND_SELL_FIRST, qty=1000, price=10.0)],
                last_price=9.9, active_buy_signal_id="")
        action = rt.t_round_state(params(), c, round_record(direction=rt.T_DIR_SELL_FIRST))
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertEqual(action["terminal"], rt.T_STATE_CONVERTED)
        self.assertIn("no_live_buy_signal", action["reason"])

    def test_end_of_day_reverse_with_signal_and_gate_balance_buys_back(self):
        c = ctx(hhmm="14:55",
                fills_today=[fill_row(rt.KIND_SELL_FIRST, qty=1000, price=10.0)],
                last_price=9.9, active_buy_signal_id="sig-2", strategic_allowed_qty=1000)
        action = rt.t_round_state(params(), c, round_record(direction=rt.T_DIR_SELL_FIRST))
        self.assertEqual(action["action"], rt.ACTION_BALANCE)
        self.assertEqual(action["side"], "buy")
        self.assertEqual(action["short_code"], "XS600000")

    def test_partial_strategic_gate_converts_the_whole_round(self):
        c = ctx(hhmm="14:55",
                fills_today=[fill_row(rt.KIND_SELL_FIRST, qty=1000, price=10.0)],
                last_price=9.9, active_buy_signal_id="sig-2", strategic_allowed_qty=500)
        action = rt.t_round_state(params(), c, round_record(direction=rt.T_DIR_SELL_FIRST))
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertEqual(action["terminal"], rt.T_STATE_CONVERTED)
        self.assertIn("strategic_gate_partial 500/1000", action["reason"])

    def test_buy_back_breaker_uses_the_single_spread_measure(self):
        # 整笔价差率 <= -round_stop_pct ⇒ 封买回通道（不是单腿价差）。
        c = ctx(hhmm="14:55",
                fills_today=[fill_row(rt.KIND_SELL_FIRST, qty=1000, price=10.0)],
                last_price=10.35, active_buy_signal_id="sig-2", strategic_allowed_qty=1000)
        action = rt.t_round_state(params(), c, round_record(direction=rt.T_DIR_SELL_FIRST))
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertIn("buy_back_breaker", action["reason"])

    def test_breaker_day_seals_the_reverse_leg_as_unwound(self):
        c = ctx(hhmm="14:55", today_realized_pnl=-8000.0,
                fills_today=[fill_row(rt.KIND_SELL_FIRST, qty=1000, price=10.0)],
                last_price=9.9, active_buy_signal_id="sig-2", strategic_allowed_qty=1000)
        action = rt.t_round_state(params(), c, round_record(direction=rt.T_DIR_SELL_FIRST))
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertEqual(action["terminal"], rt.T_STATE_UNWOUND)
        self.assertIn("breaker_day", action["reason"])

    def test_cash_short_closes_out_and_never_chases(self):
        # 价差已达配对线（10.0 -> 9.79 = 2.1%），但专款外的现金不够 ⇒ 收摊不追买。
        c = ctx(fills_today=[fill_row(rt.KIND_SELL_FIRST, qty=1000, price=10.0)],
                last_price=9.79, available_cash=100.0)
        action = rt.t_round_state(params(), c, round_record(direction=rt.T_DIR_SELL_FIRST))
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertIn("buy_back_blocked_cash", action["reason"])

    def test_no_price_holds_except_the_reverse_buy_back_at_force(self):
        action = rt.t_round_state(
            params(), ctx(hhmm="14:55", last_price=None,
                          fills_today=[fill_row(rt.KIND_SELL_FIRST, qty=1000, price=10.0)]),
            round_record(direction=rt.T_DIR_SELL_FIRST))
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertIn("no_price", action["reason"])
        action = rt.t_round_state(
            params(), ctx(last_price=None,
                          fills_today=[fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0)]),
            round_record())
        self.assertEqual(action["action"], rt.ACTION_HOLD)

    def test_ghost_open_converges(self):
        action = rt.t_round_state(params(), ctx(fills_today=[], orders_today=[]),
                                  round_record(first_filled=1000, opened_at=NOW_TS - 500.0))
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertIn("ghost_open", action["reason"])

    def test_fresh_leg_without_counter_rows_still_waits(self):
        action = rt.t_round_state(params(), ctx(fills_today=[], orders_today=[]),
                                  round_record(first_filled=0, opened_at=NOW_TS - 5.0))
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertIn("first_leg_not_filled_yet", action["reason"])

    def test_cross_day_rolls_are_explicit(self):
        old = round_record(day="20261103", first_filled=1000)
        action = rt.t_round_state(params(), ctx(fills_today=[
            fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0, day="20261103")]), old)
        self.assertEqual(action["action"], rt.ACTION_CLOSE)
        self.assertEqual(action["terminal"], rt.T_STATE_UNWOUND)
        self.assertIn("cross_day_unpaired", action["reason"])
        action = rt.t_round_state(params(), ctx(fills_today=[
            fill_row(rt.KIND_SELL_FIRST, qty=1000, price=10.0, day="20261103")]),
            round_record(direction=rt.T_DIR_SELL_FIRST, day="20261103"))
        self.assertEqual(action["terminal"], rt.T_STATE_CONVERTED)
        self.assertIn("cross_day_not_bought_back", action["reason"])

    def test_missing_balance_gap_below_one_lot_holds(self):
        fills = [fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0),
                 fill_row(rt.KIND_BUY_FIRST_PAIR, qty=950, price=10.3)]
        action = rt.t_round_state(params(), ctx(fills_today=fills, last_price=10.3),
                                  round_record())
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertIn("below_one_lot", action["reason"])


class RoundBookTests(unittest.TestCase):
    """回合账：重启后从柜台重建；重建不出来就一动不动。"""

    def test_rebuild_from_orders_recreates_the_open_round(self):
        book = rt.TRoundBook(params())
        rows = [order_row(rt.KIND_BUY_FIRST, traded=1000, price=10.0, terminal=True)]
        rebuilt, unknown = book.rebuild_from(rows, [], day=DAY)
        self.assertEqual(len(rebuilt), 1)
        self.assertEqual(rebuilt[0]["status"], rt.T_STATE_OPEN)
        self.assertEqual(rebuilt[0]["direction"], rt.T_DIR_BUY_FIRST)
        self.assertEqual(rebuilt[0]["first_filled"], 1000)
        self.assertEqual(unknown, [])
        self.assertEqual(book.open_for("600000.SH", DAY) is not None, True)

    def test_rebuilt_round_does_not_open_a_second_leg(self):
        book = rt.TRoundBook(params())
        book.rebuild_from([order_row(rt.KIND_BUY_FIRST, traded=1000, terminal=True)], [],
                          day=DAY)
        merged = book.context_for(ctx(), "600000.SH")
        self.assertTrue(merged["t_has_open_leg"])
        self.assertEqual(merged["t_rounds_used"], 1)
        allowed, reason = rt.t_gate_open(params(), merged)
        self.assertFalse(allowed)
        self.assertIn("step1", reason)
        action = rt.t_round_state(params(), merged, None)
        self.assertEqual(action["action"], rt.ACTION_HOLD)

    def test_rebuild_from_fills_marks_a_completed_round_as_done(self):
        book = rt.TRoundBook(params())
        fills = [fill_row(rt.KIND_BUY_FIRST, qty=1000, price=10.0),
                 fill_row(rt.KIND_BUY_FIRST_PAIR, qty=1000, price=10.3)]
        rebuilt, _unknown = book.rebuild_from([], fills, day=DAY)
        self.assertEqual(rebuilt[0]["status"], rt.T_STATE_DONE)
        self.assertEqual(rebuilt[0]["sell_price"], 10.3)

    def test_ambiguous_rounds_become_unknown_and_are_not_actable(self):
        # 同一天同码上既有正 T 又有反 T 的腿 ⇒ remark 里没有序号，无从归属。
        book = rt.TRoundBook(params())
        rows = [order_row(rt.KIND_BUY_FIRST, traded=1000, terminal=True),
                order_row(rt.KIND_SELL_FIRST, qty=500, traded=500, terminal=True)]
        rebuilt, unknown = book.rebuild_from(rows, [], day=DAY)
        self.assertEqual(rebuilt, [])
        self.assertEqual(len(unknown), 1)
        self.assertIn("both_directions_same_day", unknown[0]["why"])
        unknown_round = {"id": unknown[0]["id"], "status": rt.T_STATE_UNKNOWN}
        action = rt.t_round_state(params(), ctx(), unknown_round)
        self.assertEqual(action["action"], rt.ACTION_HOLD)
        self.assertIn("round_unknown_after_rebuild", action["reason"])

    def test_pair_without_first_leg_is_unknown(self):
        book = rt.TRoundBook(params())
        _rebuilt, unknown = book.rebuild_from(
            [order_row(rt.KIND_SELL_PAIR, traded=300, terminal=True)], [], day=DAY)
        self.assertEqual(len(unknown), 1)
        self.assertIn("pair_leg_without_first_leg", unknown[0]["why"])

    def test_remark_without_a_head_is_unknown(self):
        book = rt.TRoundBook(params())
        _rebuilt, unknown = book.rebuild_from(
            [{"remark": "%s-TB" % REMARK_VERSION, "terminal": True, "traded": 100,
              "price": 10.0}], [], day=DAY)
        self.assertEqual(unknown[0]["why"], "no_symbol_head_in_remark")

    def test_book_refuses_a_second_open_round_on_the_same_name(self):
        book = rt.TRoundBook(params())
        ok, _why = book.new_round("600000.SH", rt.T_DIR_BUY_FIRST, DAY, 1000, 10.0, NOW_TS)
        self.assertTrue(ok)
        ok, why = book.new_round("600000.SH", rt.T_DIR_SELL_FIRST, DAY, 500, 10.2, NOW_TS)
        self.assertFalse(ok)
        self.assertIn("already_open_on_symbol", why)

    def test_terminal_release_earmarked_cash(self):
        book = rt.TRoundBook(params())
        book.new_round("600000.SH", rt.T_DIR_SELL_FIRST, DAY, 1000, 10.0, NOW_TS)
        rnd = book.open_for("600000.SH", DAY)
        self.assertGreater(rnd["frozen_cash"], 0.0)
        self.assertAlmostEqual(rnd["frozen_cash"], 10_060.0, places=2)
        book.seal(rnd["id"], rt.T_STATE_CONVERTED, "no signal")
        self.assertEqual(book.frozen_cash(), 0.0)

    def test_round_ids_follow_the_go_key_shape(self):
        self.assertEqual(rt.t_round_id("600000.SH", rt.T_DIR_BUY_FIRST, DAY, 2),
                         "t0:600000.SH:buy_first:20261104:2")
        self.assertEqual(rt.t_pair_round_id("t0:X:buy_first:D:1"), "t0:X:buy_first:D:1:p")
        self.assertEqual(rt.t_pair_round_id("t0:X:buy_first:D:1", 3), "t0:X:buy_first:D:1:p3")


class ShortCodeTests(unittest.TestCase):
    """T 短码与现有 build_remark / short_code_from_remark 的兼容性。"""

    def test_configured_codes_pass_the_round_trip(self):
        ok, why = rt.t_short_code_check(params())
        self.assertTrue(ok, why)
        self.assertEqual(rt.t_short_codes(params())[rt.KIND_BUY_FIRST], "TB")

    def test_reverse_lookup_round_trips_through_a_remark(self):
        # 复刻 local_executor.build_remark 的形状，再用本模块的反查取回短码。
        remark = "%s-TB600000" % REMARK_VERSION
        row = {"remark": remark}
        self.assertEqual(rt._short_code_of(row), "TB600000")
        self.assertEqual(rt._kind_of_short(rt._short_code_of(row),
                                           rt.t_short_codes(params())), rt.KIND_BUY_FIRST)

    def test_prefix_collision_with_existing_codes_is_refused(self):
        bad = dict(BASE_PARAMS["t_signal_codes"])
        bad[rt.KIND_BUY_FIRST] = "T"  # "TP"(take_profit) 会被读成 T 单：幂等反查直接错位
        ok, why = rt.t_short_code_check(params(t_signal_codes=bad))
        self.assertFalse(ok)
        self.assertIn("prefix_collision", why)

    def test_dash_in_code_is_refused(self):
        bad = dict(BASE_PARAMS["t_signal_codes"])
        bad[rt.KIND_CLOSE_OUT] = "T-C"
        ok, why = rt.t_short_code_check(params(t_signal_codes=bad))
        self.assertFalse(ok)
        self.assertIn("code_not_upper_alpha", why)

    def test_overlong_code_is_refused(self):
        bad = dict(BASE_PARAMS["t_signal_codes"])
        bad[rt.KIND_BUY_FIRST] = "TABLESS"
        ok, why = rt.t_short_code_check(params(t_signal_codes=bad))
        self.assertFalse(ok)
        self.assertIn("code_too_long_for_remark", why)

    def test_leg_short_codes_are_stable(self):
        self.assertEqual(rt._t_short(params(), rt.KIND_SELL_FIRST, "600000.SH"), "XB600000")
        self.assertEqual(rt._t_short(params(), rt.KIND_SELL_PAIR, "000001.SZ"), "XS000001")


class MoneyMeasureTests(unittest.TestCase):
    """价差 / 滑点 / 费用：全系统只有一把尺子（owner 2026-10-29：做 T 的唯一口径是价差）。"""

    def test_round_spread_is_the_same_formula_for_both_directions(self):
        # 正 T：买 1000@10.0，未配平部分按现价折算。
        self.assertAlmostEqual(rt.round_spread_pct(rt.T_DIR_BUY_FIRST, 1000, 10.0, 0, None, 10.2),
                               2.0, places=6)
        self.assertAlmostEqual(rt.round_spread_pct(rt.T_DIR_SELL_FIRST, 1000, 10.0, 0, None, 9.8),
                               2.0, places=6)

    def test_spread_needs_the_first_fill(self):
        self.assertIsNone(rt.round_spread_pct(rt.T_DIR_BUY_FIRST, 0, 10.0, 0, None, 10.2))

    def test_slippage_sign_convention(self):
        self.assertAlmostEqual(rt.t_slip_bps(rt.T_DIR_BUY_FIRST, 10.0, 10.01), 10.0, places=2)
        self.assertAlmostEqual(rt.t_slip_bps(rt.T_DIR_SELL_FIRST, 10.0, 9.99), 10.0, places=2)

    def test_pnl_only_counts_matched_shares_and_splits_fees(self):
        frame = rt.t_round_pnl(params(), rt.T_DIR_BUY_FIRST, 1000, 10.0, 800, 10.2)
        self.assertEqual(frame["q_matched"], 800)
        self.assertAlmostEqual(frame["t_pnl_gross"], 160.0, places=2)
        self.assertEqual(frame["fee_source"], "estimate")
        # 印花税只对卖腿：800 股 @10.2 = 8160 元 × 0.05% = 4.08
        self.assertAlmostEqual(frame["stamp_tax"], 4.08, places=2)
        self.assertAlmostEqual(frame["t_pnl_net"],
                               frame["t_pnl_gross"] - frame["fee_buy"] - frame["fee_sell"]
                               - frame["stamp_tax"], places=2)

    def test_broker_fees_win_and_mixed_is_labelled_as_mixed(self):
        frame = rt.t_round_pnl(params(), rt.T_DIR_BUY_FIRST, 1000, 10.0, 1000, 10.2,
                               broker_fee_buy=7.0)
        self.assertEqual(frame["fee_source"], "mixed")
        self.assertAlmostEqual(frame["fee_buy"], 7.0, places=2)

    def test_participation_denominator_is_turnover_so_far(self):
        value = rt.t_participation_pct(params(), 1000, 10.0, 40_000_000.0)
        self.assertAlmostEqual(value, 0.025, places=4)  # 四位小数：分位会把 B4 观察列整片抹成 0
        self.assertIsNone(rt.t_participation_pct(params(), 1000, 10.0, None))

    def test_day_totals_counts_are_not_decisions(self):
        rows = [order_row(rt.KIND_BUY_FIRST, traded=1000, terminal=True),
                order_row(rt.KIND_BUY_FIRST_PAIR, traded=0, terminal=True)]
        rows[1]["status"] = "CANCELLED"
        rounds = [{"id": "a", "day": DAY, "status": rt.T_STATE_DONE, "t_pnl_net": 12.0,
                   "t_pnl_gross": 20.0}]
        totals = rt.t_day_totals(params(), ctx(orders_today=rows), rounds)
        self.assertEqual(totals["done"], 1)
        self.assertEqual(totals["orders_placed"], 2)
        self.assertEqual(totals["orders_canceled"], 1)


class ShapeLockTests(unittest.TestCase):
    """形状锁：这些是"载体能不能被 QMT 装载"的前提，不是风格偏好。"""

    def _source(self):
        with open(MODULE_PATH, "rb") as handle:
            return handle.read()

    def test_module_is_pure_ascii(self):
        raw = self._source()
        bad = [index for index, byte in enumerate(raw) if byte > 127]
        self.assertEqual(bad, [], "non_ascii_bytes=%d first=%s" % (len(bad), bad[:3]))

    def test_no_numeric_literals_inside_function_bodies(self):
        tree = ast.parse(self._source().decode("ascii"))
        hits = []
        for node in ast.walk(tree):
            if not isinstance(node, ast.FunctionDef):
                continue
            for inner in ast.walk(node):
                if isinstance(inner, ast.Constant) and not isinstance(inner.value, bool) \
                        and isinstance(inner.value, (int, float)):
                    hits.append("%s:%r" % (node.name, inner.value))
        self.assertEqual(hits, [], "bare_numbers=%s" % hits[:6])

    def test_no_imports_and_no_privileged_calls(self):
        tree = ast.parse(self._source().decode("ascii"))
        imports = [node for node in ast.walk(tree)
                   if isinstance(node, (ast.Import, ast.ImportFrom))]
        self.assertEqual(imports, [])
        banned = {"open", "socket", "requests", "urllib", "sqlite3", "subprocess", "system",
                  "popen", "remove", "rename"}
        for node in ast.walk(tree):
            if isinstance(node, ast.Call):
                name = node.func.id if isinstance(node.func, ast.Name) else (
                    node.func.attr if isinstance(node.func, ast.Attribute) else "")
                self.assertNotIn(name, banned, "line=%d call=%s" % (node.lineno, name))

    def test_every_param_key_read_is_declared(self):
        # 读到的键必须全部在册：漏登记＝装配侧不知道自己少配了哪条阈值。
        keys = _params_keys_read(ast.parse(self._source().decode("ascii")))
        self.assertEqual(sorted(keys - set(rt.T_PARAM_KEYS)), [])
        self.assertTrue(keys, "the module must read at least one threshold")

    def test_missing_params_key_raises_instead_of_defaulting(self):
        bare = dict(BASE_PARAMS)
        bare.pop("t_vwap_dev_pct")
        with self.assertRaises(KeyError):
            rt.t_gate_open(bare, ctx())


def _params_keys_read(tree):
    """Every params key the module reads through _param/_flag (AST, not text grep)."""
    keys = set()
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        func = node.func
        name = func.id if isinstance(func, ast.Name) else ""
        if name not in ("_param", "_flag"):
            continue
        if len(node.args) >= 2 and isinstance(node.args[1], ast.Constant):
            if isinstance(node.args[1].value, str):
                keys.add(node.args[1].value)
    return keys


if __name__ == "__main__":
    unittest.main()
