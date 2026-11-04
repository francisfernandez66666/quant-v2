# -*- coding: utf-8 -*-
"""动态仓位定权链 + 市场环境门（Python 侧）行为用例。

这批用例盯的是"从 Go 搬过来之后不能变形"的那几条，不是覆盖率：

  1. 弃权纪律——任何关键读数缺失，该维度进 missing_dims，既不当 0 也不当中性
     （internal/signalctl/sizing.go:277-315、regime.go:1-16）；
  2. 盲区封顶——缺读数即目标封顶 0.50，且当天整体不做 T
     （sizing.go:339 + internal/engine/funds_today.go:171-184）；
  3. 上限类取 min 不连乘——权益回撤/风险预算与组值之间是 min 关系
     （sizing.go:220-245）；
  4. 组归属不许串——个股维度不得抬账户级目标，账户级帽不得进个股缩放
     （sizing.go:258-275）；
  5. 双重限制取严——单笔占比与目标剩余空间谁小用谁
     （internal/engine/engine.go:1844-1943、internal/ordbudget/ordbudget.go:141-322）；
  6. 广度缺失走小幅降权，不计进关键缺失（同一件事不罚两次，
     sizing.go:828-860 + missing_key_dims_test.go:88-94）；
  7. 情绪相位/市场状态的"owner 主动关掉"≠ 盲区（regime.go:37-45）。

oracle_* 那几枚函数是把 Go 的合成口径独立重写一遍（不是 import 被测实现），
用来交叉核对 MS/SS 的数值——搬公式时最容易出错的是分组与取 min 的顺序。
"""

import math

import pytest

from qmt_gateway import rules_sizing as rs


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

def params(**overrides):
    knobs = dict(rs.SIZING_DEFAULTS)
    knobs.update(overrides)
    return knobs


def base_ctx(**overrides):
    """A ctx with no blind spot: emotion produced, index read, breadth counted."""
    # 夹具读数一次摆齐：缺哪个维度就让对应用例显式删掉，靠"缺失⇒封顶"的路径去走红，
    # 而不是靠"零值刚好不触发"——那是本仓反复出事的假绿形状。
    ctx = {
        "emotion_phase": "start",
        "index_change_pct": 0.5,
        "advancers": 1000,
        "decliners": 500,
        "total_asset": 100000.0,
        "available_cash": 50000.0,
        "daily_buy_total": 0.0,
        "positions_live": [],
        "pending_buys": [],
        "t_buy_leg_value": 0.0,
        "t_sell_leg_value": 0.0,
    }
    ctx.update(overrides)
    return ctx


def oracle_power_mean(values, power):
    """Go internal/signalctl/sizing.go:356-372, re-transcribed independently."""
    if not values:
        return 1.0
    if power == 0:
        product = 1.0
        for value in values:
            product *= value
        return product ** (1.0 / len(values))
    return (sum(value ** power for value in values) / len(values)) ** (1.0 / power)


def oracle_group(values, floor, power):
    """Go sizingGroup / 组幂平均 + 组地板（sizing.go:378-407）。"""
    kept = [v for v in values if v > 0]
    if not kept:
        return 1.0
    aggregated = oracle_power_mean(kept, power)
    return floor if floor > 0 and aggregated < floor else aggregated


def oracle_clamp(value, lo, hi):
    return min(max(value, lo), hi)


def oracle_scale(knobs, values, account_cap):
    """Go clampGroup（sizing.go:266-275）：先套账户帽，再夹到 [Floor, Cap]。"""
    group = oracle_group(values, knobs["group_floor_market"], knobs["power_p"])
    if group > account_cap:
        group = account_cap
    return oracle_clamp(group, knobs["scale_floor"], knobs["scale_cap"])


# ---------------------------------------------------------------------------
# 1. abstention discipline -- a missing reading is neither 0 nor neutral
# ---------------------------------------------------------------------------

def test_missing_index_is_a_key_dim_and_caps_the_target():
    # 逐个旋钮单独拨动：一次只改一个参数，才能证明每条判据真的吃的是那一个键
    # （一起改就退化成"改了什么都会红"的无信息断言）。
    knobs = params()
    scale, dims = rs.market_scale(knobs, base_ctx(index_change_pct=None))
    assert dims == [rs.DIM_INDEX_CHANGE]
    assert scale == pytest.approx(0.50)


def test_missing_index_nan_is_the_same_as_absent():
        # 逐个旋钮单独拨动：一次只改一个参数，才能证明每条判据真的吃的是那一个键。
    knobs = params()
    scale, dims = rs.market_scale(knobs, base_ctx(index_change_pct=float("nan")))
    assert dims == [rs.DIM_INDEX_CHANGE]
    assert scale < 1.0


def test_stale_but_real_index_is_not_a_blind_spot():
    """§INDEX-BLIND 的 LKG 行为不动：陈旧但真实的点位照常定权（owner 裁决二）。"""
    knobs = params()
    scale, dims = rs.market_scale(
        knobs, base_ctx(index_change_pct=-0.4, index_stale=True,
                        index_source="eastmoney"))
    assert dims == []
    assert scale == pytest.approx(1.0)


def test_disabled_dimension_is_not_a_blind_spot():
    knobs = params(market_state_enabled=True)
    assert rs.key_missing_dims(knobs, base_ctx()) == [rs.DIM_MARKET_STATE]
    knobs = params(market_state_enabled=False)
    assert rs.key_missing_dims(knobs, base_ctx()) == []


def test_disabled_emotion_dimension_is_not_a_blind_spot():
    knobs = params(emotion_enabled=False)
    ctx = base_ctx(emotion_phase=None, limit_up_count=None, ladder_height=None)
    assert rs.key_missing_dims(knobs, ctx) == []


def test_missing_emotion_produced_by_enabled_dimension_is_blind():
    knobs = params(emotion_enabled=True)
    ctx = base_ctx(limit_up_count=None, ladder_height=None)
    ctx.pop("emotion_phase")
    assert rs.key_missing_dims(knobs, ctx) == [rs.DIM_EMOTION_PHASE]


def test_dim_order_is_stable_for_the_same_blind_spot():
    """前端与告警直接渲染这一串，乱序会让同一天打两种文案。"""
    knobs = params(market_state_enabled=True, emotion_enabled=True)
    ctx = base_ctx(index_change_pct=None, emotion_phase=None,
                   limit_up_count=None, ladder_height=None)
    assert rs.key_missing_dims(knobs, ctx) == [
        rs.DIM_MARKET_STATE, rs.DIM_EMOTION_PHASE, rs.DIM_INDEX_CHANGE]


def test_missing_dims_are_a_fresh_list():
    knobs = params()
    _scale, dims = rs.market_scale(knobs, base_ctx(index_change_pct=None))
    dims.append("tampered")
    _again, second = rs.market_scale(knobs, base_ctx(index_change_pct=None))
    assert second == [rs.DIM_INDEX_CHANGE]


# ---------------------------------------------------------------------------
# 2. breadth -- unknown takes the small haircut, never "breadth is fine"
# ---------------------------------------------------------------------------

def test_unknown_breadth_applies_the_haircut_not_neutral():
    knobs = params()
    assert rs.breadth_up_ratio(knobs, base_ctx(breadth_unknown=True,
                                               advancers=None,
                                               decliners=None)) is None
    weak = rs.market_scale(knobs, base_ctx(breadth_unknown=True,
                                           advancers=None, decliners=None))[0]
    healthy = rs.market_scale(knobs, base_ctx())[0]
    assert weak < healthy
    assert weak == pytest.approx(oracle_scale(
        knobs, [1.0, 1.0, 1.0, knobs["breadth_missing_scale"]], rs.SZ_NO_CAP))


def test_zero_advance_count_is_read_as_a_real_tape():
    """advancers=0 是合法读数（全跌），走最低档；它不等于"取数缺失"。"""
    knobs = params()
    ratio = rs.breadth_up_ratio(knobs, base_ctx(advancers=0, decliners=1000))
    assert ratio == pytest.approx(0.0)
    _scale, dims = rs.market_scale(knobs, base_ctx(advancers=0, decliners=1000))
    assert dims == []
    assert rs._sz_breadth_factor(knobs, ratio) == pytest.approx(
        knobs["breadth_scales"]["low"])


def test_breadth_missing_does_not_enter_key_dims():
    """广度缺失已按 breadth_missing_scale 降过权，再计一次是同一件事罚两次。"""
    knobs = params()
    _scale, dims = rs.market_scale(
        knobs, base_ctx(breadth_unknown=True, advancers=None, decliners=None))
    assert dims == []


def test_breadth_bands_match_the_go_bands():
    knobs = params()
    assert rs._sz_breadth_factor(knobs, 0.29) == pytest.approx(0.70)
    assert rs._sz_breadth_factor(knobs, 0.30) == pytest.approx(0.85)
    assert rs._sz_breadth_factor(knobs, 0.50) == pytest.approx(1.00)


# ---------------------------------------------------------------------------
# 3. the market group must agree with Go, factor by factor
# ---------------------------------------------------------------------------

def test_market_scale_matches_go_in_the_baseline_ctx():
    knobs = params()
    ctx = base_ctx()
    scale, dims = rs.market_scale(knobs, ctx)
    expected = oracle_scale(
        knobs, [1.0, 1.0, 1.0, 1.0, 1.0], rs.SZ_NO_CAP)  # emotion/tier/index/breadth
    assert dims == []
    assert scale == pytest.approx(expected)
    assert scale == pytest.approx(1.0)


def test_market_scale_matches_go_across_a_weak_tape():
    knobs = params(market_state_enabled=True)
    ctx = base_ctx(emotion_phase="retreat", market_state="bear",
                   risk_tier="Red", index_change_pct=-2.5,
                   advancers=100, decliners=900)
    scale, dims = rs.market_scale(knobs, ctx)
    assert dims == []
    values = [0.50, 0.40, 0.30, 0.40, 0.70]
    assert scale == pytest.approx(oracle_scale(knobs, values, rs.SZ_NO_CAP))
    assert scale < 0.5


def test_market_scale_matches_go_when_only_one_factor_is_known():
    """只带少数市场读数时，组值由在场的那几条决定（其余弃权，不补齐成中性）。"""
    knobs = params(emotion_enabled=False)
    ctx = base_ctx(breadth_unknown=True, advancers=None, decliners=None,
                   index_change_pct=0.5, emotion_phase=None,
                   limit_up_count=None, ladder_height=None)
    scale, _dims = rs.market_scale(knobs, ctx)
    values = [1.0, 1.0, 0.90]
    assert scale == pytest.approx(oracle_scale(knobs, values, rs.SZ_NO_CAP))


def test_retreat_phase_pulls_market_scale_the_same_way_as_go():
    """情绪相位在位时 MS 与 Go 同向：退潮(0.50) 比启动/发酵(1.00) 更低。"""
    knobs = params()
    warm = rs.market_scale(knobs, base_ctx(emotion_phase="start"))[0]
    cold = rs.market_scale(knobs, base_ctx(emotion_phase="retreat"))[0]
    assert cold < warm
    assert cold == pytest.approx(oracle_scale(knobs, [0.50, 1.0, 1.0, 1.0],
                                              rs.SZ_NO_CAP))


def test_product_mode_is_the_legacy_multiplier_chain():
    knobs = params(sizing_mode="product")
    ctx = base_ctx(emotion_phase="retreat", index_change_pct=-1.5,
                   advancers=1000, decliners=500)
    scale, dims = rs.market_scale(knobs, ctx)
    assert dims == []
    # 0.50 (emotion) x 1.00 (tier) x 0.70 (index mid band) x 1.00 (breadth)
    assert scale == pytest.approx(0.35)


def test_index_bands_map_to_the_go_scales():
    knobs = params()
    assert rs._sz_index_factor(knobs, 0.0) == pytest.approx(1.00)
    assert rs._sz_index_factor(knobs, -1.5) == pytest.approx(0.70)
    assert rs._sz_index_factor(knobs, -2.0) == pytest.approx(0.40)
    assert rs._sz_index_factor(knobs, None) is None


def test_market_state_scale_only_uses_the_discrete_state():
    """MaxPosPct 由同一状态机派生，再乘一次就是同一风险双罚（sizing.go:663-675）。"""
    knobs = params()
    ctx = base_ctx(market_state="range", max_pos_pct=0.35)
    scale_with_max = rs.market_scale(knobs, ctx)[0]
    scale_without = rs.market_scale(knobs, base_ctx(market_state="range"))[0]
    assert scale_with_max == pytest.approx(scale_without)
    assert scale_with_max < rs.market_scale(knobs, base_ctx())[0]


# ---------------------------------------------------------------------------
# 4. hard blocks -- downgrade instead of a second penalty
# ---------------------------------------------------------------------------

def test_emotion_ice_hard_blocks_and_returns_the_floor_without_a_key_cap():
    knobs = params()
    ctx = base_ctx(emotion_phase="ice", index_change_pct=None)
    blocked, reason = rs.buy_hard_block(knobs, ctx)
    assert blocked and reason == "emotion_ice"
    scale, dims = rs.market_scale(knobs, ctx)
    assert scale == pytest.approx(knobs["scale_floor"])
    assert dims == []


def test_red_tier_only_blocks_behind_the_emergency_valve():
    knobs = params()
    assert rs.buy_hard_block(knobs, base_ctx(risk_tier="Red")) == (False, "")
    blocked, reason = rs.buy_hard_block(
        knobs, base_ctx(risk_tier="Red", red_block_new_buy=True))
    assert blocked and reason == "red_tier_blocks_new_buy"


def test_d1_bearish_blocks_only_at_yellow_or_red():
    knobs = params()
    assert rs.buy_hard_block(knobs, base_ctx(d1_blocked=True)) == (False, "")
    blocked, reason = rs.buy_hard_block(
        knobs, base_ctx(d1_blocked=True, risk_tier="Yellow"))
    assert blocked and reason == "d1_bearish_with_tier_at_least_yellow"


def test_stoploss_guard_uses_the_baked_count():
    knobs = params(stoploss_guard_count=4)
    assert rs.buy_hard_block(knobs, base_ctx(window_stop_losses=3)) == (False, "")
    blocked, reason = rs.buy_hard_block(knobs, base_ctx(window_stop_losses=4))
    assert blocked and reason == "stoploss_guard"


def test_long_holiday_last_day_blocks_only_when_the_valve_is_on():
    knobs = params()
    ctx = base_ctx(holiday={"calendar_loaded": True, "closed_run": 5,
                            "pre_holiday_n": 1})
    assert rs.buy_hard_block(knobs, ctx) == (False, "")
    blocked, reason = rs.buy_hard_block(params(holiday_block_new=True), ctx)
    assert blocked and reason == "long_holiday_last_day"


def test_unloaded_calendar_abstains_instead_of_calling_it_a_normal_day():
    """日历没加载时节前因子弃权——把"未知"当普通日是 fail-open。"""
    knobs = params()
    assert rs._sz_holiday_factor(knobs, {"calendar_loaded": False, "closed_run": 5,
                                      "pre_holiday_n": 1}) is None
    assert rs._sz_holiday_factor(knobs, None) is None
    assert rs._sz_holiday_factor(knobs, {"calendar_loaded": True, "closed_run": 5,
                                      "pre_holiday_n": 1}) == pytest.approx(0.40)
    assert rs._sz_holiday_factor(knobs, {"calendar_loaded": True, "closed_run": 2,
                                      "pre_holiday_n": 4}) == pytest.approx(1.0)


# ---------------------------------------------------------------------------
# 5. account caps are a min, never a product
# ---------------------------------------------------------------------------

def test_drawdown_cap_only_binds_above_the_limit():
    knobs = params()
    assert rs._sz_drawdown_cap(knobs, 0.05) == pytest.approx(1.0)
    assert rs._sz_drawdown_cap(knobs, 0.20) == pytest.approx(0.5)
    assert rs._sz_drawdown_cap(knobs, None) is None
    assert rs._sz_drawdown_cap(params(max_drawdown_pct=0.0), 0.20) is None


def test_risk_budget_cap_shrinks_when_the_stop_is_loosened():
    """止损 6%→9% 时同一笔最大亏损被放大 50%，仓位必须按 6/9 收回。"""
    knobs = params()
    assert rs._sz_risk_budget_cap(knobs, base_ctx(), 3.0, 6.0) == pytest.approx(0.666667,
                                                                            abs=1e-6)
    assert rs._sz_risk_budget_cap(knobs, base_ctx(), 0.0, 6.0) is None
    assert rs._sz_risk_budget_cap(knobs, base_ctx(), -1.0, 6.0) is None
    assert rs._sz_risk_budget_cap(params(sl_offset_risk_budget_keep=False),
                               base_ctx(), 3.0, 6.0) is None


def test_account_caps_are_taken_as_min_and_only_bind_the_market_group():
    knobs = params()
    ctx = base_ctx(equity_drawdown=0.20, stop_loss_offset_pct=3.0,
                   base_stop_loss_pct=6.0)
    capped = rs._sz_account_caps(knobs, ctx)
    assert capped == pytest.approx(min(0.5, 6.0 / 9.0))
    scale, _dims = rs.market_scale(knobs, ctx)
    assert scale <= capped + 1e-9
    # 个股缩放里不许出现账户级帽
    stock, _missing = rs.stock_scale(knobs, ctx)
    assert stock == pytest.approx(1.0)


def test_group_floor_lifts_an_all_abstain_group_to_neutral_not_zero():
    knobs = params(emotion_enabled=False)
    ctx = base_ctx(advancers=None, decliners=None, breadth_unknown=False,
                   index_change_pct=0.0)
    scale, dims = rs.market_scale(knobs, ctx)
    assert dims == []
    assert scale > knobs["scale_floor"]


# ---------------------------------------------------------------------------
# 6. the stock group -- this order's size only
# ---------------------------------------------------------------------------

def test_stock_scale_uses_the_per_stock_readings():
    knobs = params()
    ctx = base_ctx(quality_weight=1.0, confidence=0.60, atr=2.0, price=100.0,
                   flow_ratio_5d=4.0, flow_ratio_today=0.0)
    stock, missing = rs.stock_scale(knobs, ctx)
    values = [1.0, 0.6 + 0.5 * (2 * 0.60 - 1) * 1.1, 2.0 / 2.0, 1.10]
    expected = oracle_clamp(
        oracle_group(values, knobs["group_floor_stock"], knobs["power_p"]),
        knobs["scale_floor"], knobs["scale_cap"])
    assert stock == pytest.approx(expected)
    assert "llm_bearish" in missing and "flow_today" in missing


def test_unresolved_stock_readings_abstain_and_are_reported():
    knobs = params()
    stock, missing = rs.stock_scale(knobs, base_ctx())
    assert stock == pytest.approx(1.0)
    assert set(missing) >= {"quality", "confidence", "volatility"}


def test_quality_weight_is_re_clamped_against_a_wild_config():
    knobs = params()
    assert rs._sz_quality_factor(knobs, 5.0) == pytest.approx(1.1)
    assert rs._sz_quality_factor(knobs, 0.5) == pytest.approx(0.9)
    assert rs._sz_quality_factor(knobs, 0.0) is None


def test_kelly_no_edge_and_no_confidence_are_different_things():
    """低置信是"没优势"（固定小档），拿不到置信度才是弃权。"""
    knobs = params()
    assert rs._sz_kelly_factor(knobs, 0.40) == pytest.approx(0.6)
    assert rs._sz_kelly_factor(knobs, 1.0) == pytest.approx(1.15)
    assert rs._sz_kelly_factor(knobs, None) is None
    assert rs._sz_kelly_factor(knobs, 0.0) is None


def test_flow_band_zero_is_unresolved_not_neutral():
    knobs = params()
    assert rs._sz_flow_factor(knobs, base_ctx(), 0.0, "flow_5d_scales") is None
    assert rs._sz_flow_factor(knobs, base_ctx(), 4.0,
                              knobs["flow_5d_scales"]) == pytest.approx(1.10)
    assert rs._sz_flow_factor(knobs, base_ctx(), -4.0,
                              knobs["flow_5d_scales"]) == pytest.approx(0.70)


# ---------------------------------------------------------------------------
# 7. per-trade share and the target layer
# ---------------------------------------------------------------------------

def test_per_trade_share_maps_the_factor_onto_the_band():
    knobs = params()
    assert rs.per_trade_pct(knobs, 1.0, 1.0) == pytest.approx(0.30)
    assert rs.per_trade_pct(knobs, 0.0, 1.0) == pytest.approx(0.10)
    assert rs.per_trade_pct(knobs, 0.5, 1.0) == pytest.approx(0.20)


def test_per_trade_share_hard_clamps_both_ends():
    knobs = params(per_trade_position_pct=0.80, per_trade_position_max_pct=0.90)
    assert rs.per_trade_pct(knobs, 1.0, 1.0) == pytest.approx(knobs["per_trade_hard_max"])


def test_per_trade_share_survives_a_reversed_band():
    """区间写反时抬上限而不是压下限——配置错了也不该把单笔缩成 0。"""
    knobs = params(per_trade_position_pct=0.30, per_trade_position_max_pct=0.10)
    assert rs.per_trade_pct(knobs, 1.0, 1.0) == pytest.approx(0.30)


def test_per_trade_share_falls_back_when_the_lo_is_a_typo():
    """把 10% 写成 10 是手滑：按默认回落而不是放大成单笔满仓。"""
    knobs = params(per_trade_position_pct=10.0)
    assert rs.per_trade_pct(knobs, 0.5, 1.0) == pytest.approx(0.20)
    knobs = params(per_trade_position_pct=0.0, per_trade_position_max_pct=0.0)
    assert rs.per_trade_pct(knobs, 1.0, 1.0) == pytest.approx(
        knobs["per_trade_default_hi"])


def test_target_is_base_times_market_scale_and_blind_spot_gives_half():
    knobs = params()
    sized = rs.target_position(knobs, base_ctx(index_change_pct=None))
    assert sized["missing_key_input"] is True
    assert sized["missing_key_dims"] == [rs.DIM_INDEX_CHANGE]
    assert sized["market_scale"] == pytest.approx(0.50)
    assert sized["target"] == pytest.approx(50000.0)

    sized = rs.target_position(knobs, base_ctx())
    assert sized["target"] == pytest.approx(100000.0)


def test_ceiling_caps_the_target_independently_of_market_scale():
    knobs = params(max_total_position_pct=0.80)
    sized = rs.target_position(knobs, base_ctx())
    assert sized["market_scale"] == pytest.approx(1.0)
    assert sized["target"] == pytest.approx(80000.0)


def test_pending_buys_and_t_legs_enter_the_current_exposure():
    """T 腿不占战略空间（买腿减、卖腿加回），战略在途买单占。"""
    knobs = params()
    sized = rs.target_position(knobs, base_ctx(
        index_change_pct=-1.5,
        positions_live=[{"qty": 1000, "price": 10.0}],
        pending_buys=[{"price": 10.0, "remain": 500},
                      {"price": 10.0, "remain": 200, "intraday_t": True}],
        t_buy_leg_value=1000.0, t_sell_leg_value=500.0))
    assert sized["current"] == pytest.approx(10000.0 + 5000.0 - 1000.0 + 500.0)
    assert sized["room"] == pytest.approx(max(0.0, sized["target"] - sized["current"]))


def test_double_limit_takes_the_stricter_of_share_and_room():
    knobs = params()
    # 剩余空间比单笔占比更严 -> target
    sized = rs.target_position(knobs, base_ctx(
        positions_live=[{"qty": 9000, "price": 10.0}],
        pending_buys=[], t_buy_leg_value=0.0, t_sell_leg_value=0.0))
    assert sized["target"] == pytest.approx(100000.0)
    assert sized["current"] == pytest.approx(90000.0)
    assert sized["per_trade_amount"] == pytest.approx(
        sized["total"] * sized["per_trade_pct"])
    assert sized["room"] < sized["per_trade_amount"]
    assert sized["capped_by"] == "target"
    assert sized["amount"] == pytest.approx(sized["room"])

    # 单笔占比更严 -> dynamic
    sized = rs.target_position(knobs, base_ctx())
    assert sized["capped_by"] == "dynamic"
    assert sized["amount"] == pytest.approx(sized["per_trade_amount"])


def test_over_target_reads_as_trim_not_as_extra_room():
    knobs = params()
    sized = rs.target_position(knobs, base_ctx(
        positions_live=[{"qty": 20000, "price": 10.0}],
        pending_buys=[], t_buy_leg_value=0.0, t_sell_leg_value=0.0))
    assert sized["current"] > sized["target"]
    assert sized["room"] == pytest.approx(0.0)
    assert sized["action"] == "trim"


def test_unpriceable_position_fails_closed():
    """持仓读不出价就不开仓——把"读不到"当"没有仓位"会让剩余空间被高估。"""
    knobs = params()
    assert rs.target_position(knobs, base_ctx(
        positions_live=[{"qty": 100}], pending_buys=[],
        t_buy_leg_value=0.0, t_sell_leg_value=0.0)) is None
    assert rs.target_position(knobs, base_ctx(
        positions_live=[{"qty": 100, "price": 10.0, "cost": None}],
        pending_buys=[], t_buy_leg_value=0.0, t_sell_leg_value=0.0))["current"] == \
        pytest.approx(1000.0)


def test_unknown_equity_declines_instead_of_fabricating_a_base():
    knobs = params()
    assert rs.target_position(knobs, base_ctx(total_asset=None, equity=None)) is None
    assert rs.target_position(knobs, base_ctx(total_asset=None, equity=50000.0))[
        "total"] == pytest.approx(50000.0)


def test_sizing_disabled_returns_no_target():
    knobs = params(sizing_enabled=False)
    assert rs.target_position(knobs, base_ctx()) is None


# ---------------------------------------------------------------------------
# 8. the money caps are ANDed (rule_buy_amount's cap set)
# ---------------------------------------------------------------------------

def test_buy_size_amount_is_the_strictest_cap():
    knobs = params()
    ctx = base_ctx()
    caps = rs.buy_size_caps(knobs, ctx)
    assert caps["cash"] == pytest.approx(50000.0 - knobs["min_cash_reserve"])
    assert caps["order_amount_cap"] == pytest.approx(knobs["order_amount_cap"])
    assert caps["daily_remaining"] == pytest.approx(knobs["daily_buy_cap"])
    assert rs.buy_size_amount(knobs, ctx) == pytest.approx(
        min(v for v in caps.values() if v is not None))


def test_cash_and_daily_quota_shrink_the_amount():
    knobs = params()
    # 现金只剩 1500、预留 1000 -> 只剩 500，低于最小单额即拒（返回 None）
    assert rs.buy_size_amount(knobs, base_ctx(available_cash=1500.0)) is None
    # 当日额度已用满 -> 同样拒
    assert rs.buy_size_amount(
        knobs, base_ctx(daily_buy_total=knobs["daily_buy_cap"])) is None
    # 额度还剩一点时，那个余额就是上限
    assert rs.buy_size_amount(knobs, base_ctx(daily_buy_total=85000.0)) == pytest.approx(
        15000.0)


def test_missing_cash_reading_does_not_become_an_unlimited_order():
    knobs = params()
    caps = rs.buy_size_caps(knobs, base_ctx(available_cash=None))
    assert caps["cash"] is None
    assert rs.buy_size_amount(knobs, base_ctx(available_cash=None)) == pytest.approx(
        min(v for v in caps.values() if v is not None))


def test_buy_amount_declines_when_only_the_floor_survives():
    knobs = params(order_amount_cap=500.0)
    assert rs.buy_size_amount(knobs, base_ctx()) is None


# ---------------------------------------------------------------------------
# 9. the intraday T environment gate
# ---------------------------------------------------------------------------

def test_t_allowed_is_false_in_a_blind_spot():
    knobs = params()
    assert rs.t_allowed_by_sizing(knobs, base_ctx(index_change_pct=None)) is False
    assert rs.t_allowed_by_sizing(knobs, base_ctx()) is True


def test_t_allowed_follows_shadow_and_budget_pipeline():
    knobs = params(sizing_shadow=True)
    assert rs.t_allowed_by_sizing(knobs, base_ctx()) is False
    assert rs.t_allowed_by_sizing(params(budget_mode="legacy"), base_ctx()) is False
    assert rs.t_allowed_by_sizing(params(t_enabled=False), base_ctx()) is False


def test_t_shape_gate_closes_on_bull_and_bear_but_abstains_on_no_reading():
    knobs = params()
    for state in ("bull", "bear"):
        assert rs.t_allowed_by_sizing(
            knobs, base_ctx(market_state=state, market_state_age_sec=60)) is False
    assert rs.t_allowed_by_sizing(
        knobs, base_ctx(market_state="range", market_state_age_sec=60)) is True
    # 读数过期 -> 弃权放行（"T 行情门关了"不该被读成"行情不允许"）
    assert rs.t_allowed_by_sizing(
        knobs, base_ctx(market_state="bear", market_state_age_sec=99999.0)) is True
    # 未知状态串 -> fail-closed（"配了但值不认识"不是"没配"）
    assert rs.t_allowed_by_sizing(
        knobs, base_ctx(market_state="sideways", market_state_age_sec=60)) is False
    assert rs.t_allowed_by_sizing(
        params(t_regime_gate_enabled=False),
        base_ctx(market_state="bear", market_state_age_sec=60)) is True


def test_buy_direction_gate_uses_the_market_scale_floor():
    knobs = params()
    allowed, reason = rs.t_direction_gate(knobs, base_ctx(), "buy")
    assert allowed and reason == "ok"
    # 冰点走硬拦：MarketScale 落回地板（0.15 < t_min_market_scale 0.40），
    # 大盘 0.5% 不触发急跌停，所以这里必须报环境门而不是急跌停。
    weak = base_ctx(emotion_phase="ice", market_state="bear", risk_tier="Red",
                    index_change_pct=0.5)
    allowed, reason = rs.t_direction_gate(knobs, weak, "buy")
    assert not allowed and reason == "min_market_scale"


def test_buy_direction_gate_stops_on_a_plunge_but_sell_does_not():
    knobs = params()
    _ok, _why = rs.t_direction_gate(knobs, base_ctx(index_change_pct=-2.5), "buy")
    # -2.5% 时 MS 已低，先用环境门；这里单独验证急跌停本身的方向不对称
    plunge = base_ctx(index_change_pct=-2.1, emotion_phase="start",
                      advancers=4000, decliners=1000)
    allowed, reason = rs.t_direction_gate(knobs, plunge, "buy")
    assert not allowed and reason == "market_plunge"
    allowed, _why = rs.t_direction_gate(knobs, plunge, "sell")
    assert allowed


def test_sell_direction_gate_still_refuses_extreme_weakness():
    knobs = params()
    ctx = base_ctx(emotion_phase="ice")
    allowed, reason = rs.t_direction_gate(knobs, ctx, "sell")
    assert not allowed
    assert reason == "min_market_scale" or reason == "sell_env_min_market_scale"


def test_direction_gate_refuses_a_blind_spot_before_any_sizing():
    knobs = params()
    allowed, reason = rs.t_direction_gate(knobs, base_ctx(index_change_pct=None), "buy")
    assert not allowed and reason.startswith("missing_key_input")
    assert rs.t_direction_gate(knobs, base_ctx(), "short")[1] == "unknown_direction"


# ---------------------------------------------------------------------------
# 10. the sentiment phase detector (limit-up pool / ladder / breadth)
# ---------------------------------------------------------------------------

def test_phase_ladder_matches_the_go_branch_order():
    knobs = params()
    assert rs.emotion_phase(knobs, {"limit_up_count": 100, "ladder_height": 8}) == "climax"
    assert rs.emotion_phase(knobs, {"limit_up_count": 50, "ladder_height": 5}) == "ferment"
    assert rs.emotion_phase(knobs, {"limit_up_count": 20, "ladder_height": 3}) == "start"
    assert rs.emotion_phase(knobs, {"limit_up_count": 10, "ladder_height": 2}) == "ice"
    assert rs.emotion_phase(knobs, {"limit_up_count": 14, "ladder_height": 3}) == "retreat"
    # 池里没有一条涨停 = 数据缺失兜底，按 Go 归"启动"而不是判冰点
    assert rs.emotion_phase(knobs, {"limit_up_count": 0, "ladder_height": 0}) == "start"


def test_phase_abstains_when_the_pool_could_not_be_counted():
    knobs = params()
    assert rs.emotion_phase(knobs, {}) is None
    assert rs.emotion_phase(knobs, {"limit_up_count": None, "ladder_height": 4}) is None


def test_phase_accepts_the_chinese_label_from_upstream():
    """上游（广州侧/Go 快照）给的是中文名，本模块只存 ASCII 字节。"""
    knobs = params()
    assert rs.emotion_phase(knobs, {"emotion_phase": "\u51b0\u70b9"}) == "ice"
    assert rs.emotion_phase(knobs, {"emotion_phase": "not-a-phase"}) is None
    assert rs._sz_phase_code(knobs, "\u9000\u6f6e") == "retreat"


def test_breadth_correction_is_one_way_only():
    knobs = params(emo_breadth_ice_down_ratio=0.8, emo_breadth_retreat_down_ratio=0.6)
    ctx = {"limit_up_count": 100, "ladder_height": 8, "advancers": 100,
           "decliners": 900}
    assert rs.emotion_phase(knobs, ctx) == "ice"
    # 涨多跌少时绝不把冷相位修暖
    warm = {"limit_up_count": 20, "ladder_height": 3, "advancers": 900,
            "decliners": 100}
    assert rs.emotion_phase(knobs, warm) == "start"


def test_breadth_correction_abstains_without_counts():
    knobs = params(emo_breadth_ice_down_ratio=0.8, emo_breadth_retreat_down_ratio=0.6)
    ctx = {"limit_up_count": 100, "ladder_height": 8, "advancers": None,
           "decliners": None}
    assert rs.emotion_phase(knobs, ctx) == "climax"
    # 阈值没配（<=0）时不纠偏，与 Go 同口径
    assert rs.emotion_phase(params(), dict(ctx, advancers=100, decliners=900)) == "climax"


def test_reversed_breadth_thresholds_fall_back_to_the_larger_one():
    knobs = params(emo_breadth_ice_down_ratio=0.4, emo_breadth_retreat_down_ratio=0.7)
    ctx = {"limit_up_count": 20, "ladder_height": 3, "advancers": 350,
           "decliners": 650}
    assert rs.emotion_phase(knobs, ctx) == "retreat"


# ---------------------------------------------------------------------------
# 11. the readout and the contract shape
# ---------------------------------------------------------------------------

def test_readout_carries_bool_and_dims_together():
    knobs = params()
    read = rs.sizing_readout(knobs, base_ctx(index_change_pct=None,
                                             market_state="bear"))
    assert read["missing_key_input"] is True
    assert read["missing_key_dims"] == [rs.DIM_INDEX_CHANGE]
    assert read["t_allowed"] is False
    assert read["emotion_phase"] == "start"
    assert read["index_source"] is None
    assert math.isfinite(read["market_scale"])


def test_readout_never_writes_a_missing_reading_as_zero():
    knobs = params()
    read = rs.sizing_readout(knobs, {"emotion_phase": None,
                                     "index_change_pct": None})
    assert read["target"] is None
    assert read["buy_amount"] == pytest.approx(knobs["order_amount_cap"])
    assert read["breadth_up_ratio"] is None
    assert read["missing_key_dims"] == [rs.DIM_EMOTION_PHASE, rs.DIM_INDEX_CHANGE]


def test_param_keys_cover_the_baked_defaults():
    assert rs.SIZING_PARAM_KEYS == sorted(rs.SIZING_DEFAULTS)
    for key in ("missing_key_target_cap", "per_trade_position_pct",
                "max_total_position_pct", "t_min_market_scale",
                "breadth_missing_scale"):
        assert key in rs.SIZING_PARAM_KEYS


def test_a_missing_knob_raises_instead_of_defaulting():
    knobs = params()
    knobs.pop("scale_floor")
    with pytest.raises(KeyError):
        rs.market_scale(knobs, base_ctx())


def test_rule_layer_shape_purity():
    """这条把构建闸的两条硬要求在本机就钉住：产物纯 ASCII、函数体零数字字面量。"""
    import ast
    import pathlib

    source = pathlib.Path(rs.__file__).read_bytes()
    assert [byte for byte in source if byte > 127] == []
    tree = ast.parse(source.decode("ascii"))
    offenders = []
    for node in ast.walk(tree):
        if not isinstance(node, ast.FunctionDef):
            continue
        for inner in ast.walk(node):
            if isinstance(inner, ast.Constant) and not isinstance(inner.value, bool) \
                    and isinstance(inner.value, (int, float)):
                offenders.append("%s:%r" % (node.name, inner.value))
    assert offenders == []


def test_no_io_or_network_reachable_from_the_rule_module():
    """沙箱里的规则层不许长出文件/网络/反射的手——这正是 11-05 之后被关掉的能力。

    判据走 AST（只认代码，不认散文）：注释里提到 socket 是解释口径，
    真出现 import socket / open(...) 才是能力。
    """
    import ast
    import pathlib

    tree = ast.parse(pathlib.Path(rs.__file__).read_text(encoding="ascii"))
    modules = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            modules.update(alias.name.split(".")[0] for alias in node.names)
        elif isinstance(node, ast.ImportFrom):
            modules.add((node.module or "").split(".")[0])
        elif isinstance(node, ast.Call) and isinstance(node.func, ast.Name):
            assert node.func.id not in ("open", "eval", "exec", "__import__",
                                        "compile", "input", "getattr"), node.func.id
    # 本模块刻意一个 import 都没有：承载文件（local_executor / 内置策略）自己管
    # import 面，被内联的规则模块不许在外面再拉一个模块进来。
    assert modules == set()


def test_deterministic_pure_function():
    knobs = params()
    ctx = base_ctx(emotion_phase="climax", index_change_pct=-1.2)
    first = rs.sizing_readout(knobs, ctx)
    second = rs.sizing_readout(knobs, dict(ctx))
    assert first["market_scale"] == second["market_scale"]
    assert first["target"] == second["target"]


# ---------------------------------------------------------------------------
# 12. the seam with the executor's own money caps
# ---------------------------------------------------------------------------

def test_sizing_amount_is_never_looser_than_the_executor_rule():
    """buy_size_amount 只能是 local_executor.rule_buy_amount 的**子集**（取严）。

    这条是资金方向上的锁：定权那侧新加的任何帽（单笔占比、目标剩余空间）
    只许把金额压小，不许把执行器原有的现金/单笔/当日额度三道帽放宽——
    放宽的那一侧是"买得比闸门允许的多"。
    """
    from qmt_gateway import local_executor as le

    # 逐个旋钮单独拨动：一次只改一个参数，才能证明每条判据真的吃的是那一个键
    knobs = params()
    for ctx in (base_ctx(), base_ctx(available_cash=3000.0),
                base_ctx(daily_buy_total=95000.0),
                base_ctx(index_change_pct=None),
                base_ctx(positions_live=[{"qty": 5000, "price": 10.0}],
                         pending_buys=[], t_buy_leg_value=0.0,
                         t_sell_leg_value=0.0)):
        executor_le = dict(le.PARAMS)
        for key in ("min_cash_reserve", "order_amount_cap", "daily_buy_cap",
                    "min_order_amount"):
            executor_le[key] = knobs[key]
        old = le.rule_buy_amount(executor_le, ctx)
        new = rs.buy_size_amount(knobs, ctx)
        if old is None:
            assert new is None
        elif new is None:
            assert True  # 定权侧更严是允许的（拒单方向）
        else:
            assert new <= old + 1e-9


def test_executor_params_and_sizing_defaults_share_the_money_knobs():
    """同名钱帽的出厂值必须同源，否则"改了执行器的帽、定权还按旧的算"。"""
    from qmt_gateway import local_executor as le

    for key in ("min_cash_reserve", "order_amount_cap", "daily_buy_cap",
                "min_order_amount"):
        assert rs.SIZING_DEFAULTS[key] == le.PARAMS[key], key


def test_sizing_caps_are_reported_by_name():
    knobs = params()
    caps = rs.buy_size_caps(knobs, base_ctx())
    assert set(caps) == {"order_amount_cap", "daily_remaining", "cash",
                         "per_trade", "target_room", "industry",
                         "single_stock", "cluster"}


def test_every_knob_read_is_declared():
    """代码里读到的每个 params 键都必须在 SIZING_PARAM_KEYS 在册。

    漏一条的表现是装配侧永远不知道自己少配了什么（本模块对缺键是 raise，
    所以漏册会在第一次真跳时才炸）。
    """
    import pathlib
    import re

    text = pathlib.Path(rs.__file__).read_text(encoding="ascii")
    read = set(re.findall(r'_p\(params,\s*"([a-z0-9_]+)"\)', text))
    read |= set(re.findall(r'_flag\(params,\s*\w+,\s*"[a-z0-9_]+",\s*"([a-z0-9_]+)"\)', text))
    read |= set(re.findall(r'params\["([a-z0-9_]+)"\]', text))
    undeclared = sorted(read - set(rs.SIZING_PARAM_KEYS))
    assert undeclared == []
    # 反向也要闭合：在册但没人读的键＝装配侧的假旋钮
    unread = sorted(set(rs.SIZING_PARAM_KEYS) - read)
    assert unread == []


def test_every_ctx_key_read_is_declared():
    import pathlib
    import re

    text = pathlib.Path(rs.__file__).read_text(encoding="ascii")
    read = set(re.findall(r'_read\(ctx,\s*"([a-z0-9_]+)"\)', text))
    read |= set(re.findall(r'\(ctx or \{\}\)\.get\("([a-z0-9_]+)"\)', text))
    read |= set(re.findall(r'ctx\.get\("([a-z0-9_]+)"\)', text))
    # per-round switches arrive through the tri-state helper (ctx first, baked
    # default after), which is the only place a ctx key is not a literal get()
    read |= set(re.findall(r'_flag\(params, ctx, "([a-z0-9_]+)"', text))
    # concentration buckets arrive through the baked-bucket helper
    read |= set(re.findall(r'_bucket\(ctx,\s*"([a-z0-9_]+)"\)', text))
    assert sorted(read - set(rs.SIZING_CTX_KEYS)) == []
    assert sorted(set(rs.SIZING_CTX_KEYS) - read) == []


def test_sizing_module_registers_no_signal_kind():
    """定权链不发信号：它只决定别人那笔能多大，所以没有柜台短码可登记。"""
    assert rs.RULES == {}


def test_concentration_rooms_are_anded_when_baked():
    """行业/单票/簇三顶帽在 Go 里是与额度取 min（不连乘）；沙箱只能靠烘进来的桶。

    桶缺席（None）时这条帽**不参与**而不是给满额——本框拿不到板块清单与指数权重，
    所以桶由上层算好后随 ctx 带来。
    """
    knobs = params()
    # 已投 24000 而非 34000：行业帽 35% 打 10 万分位得到 11000 的空间，仍然过
    # min_order_amount（Go 出厂 10000）；取 34000 时空间只剩 1000，会被金额闸判成
    # "这一笔不值得下"（None），测的就是另一件事了。
    wide = rs.buy_size_amount(knobs, base_ctx())
    tight = rs.buy_size_amount(knobs, base_ctx(
        industry_known=True, industry_value=24000.0, stock_value=24000.0))
    assert tight < wide
    assert tight == pytest.approx(
        knobs["industry_cap_pct"] / 100 * 100000.0 - 24000.0)

    # 行业未知 -> 那条帽不参与，单票那条仍然参与
    caps = rs.buy_size_caps(knobs, base_ctx(industry_value=99000.0,
                                           stock_value=34000.0))
    assert caps["industry"] is None
    assert caps["single_stock"] == pytest.approx(1000.0)

    # 簇值恰好为 0 是有效读数（这簇还没有仓位），只在 cluster_set 为真时参与
    caps = rs.buy_size_caps(knobs, base_ctx(cluster_set=True, cluster_value=0.0))
    assert caps["cluster"] == pytest.approx(40000.0)
    assert rs.buy_size_caps(knobs, base_ctx())["cluster"] is None


def test_cluster_cap_can_be_switched_off_by_an_explicit_zero():
    """显式写 0 = 关掉这条帽（缺键才是出厂 40）。"""
    knobs = params(cluster_cap_pct=0.0)
    caps = rs.buy_size_caps(knobs, base_ctx(cluster_set=True, cluster_value=0.0))
    assert caps["cluster"] is None
