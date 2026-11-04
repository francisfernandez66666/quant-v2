#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""qmt_gateway/tests/test_rules_form.py -- 四个做空战法 Python 形态的行为锁。

这份文件锁什么（为什么不能只靠 Go 那边的单测）：
  券商 2026-11-03 公告、11-05 起生效之后，量仔与 QMT 之间的文件/网络通道全部关闭，
  自动执行只剩"信号完全在 QMT 内部产生"这一条合法形态。于是 internal/strategies/
  下的四个做空战法（放量破位 break_down、高位滞涨 high_churn、龙头断板 leader_decay、
  利好兑现砸盘 good_news_fade）必须有一份**纯 Python** 等价物，落在
  qmt_gateway/rules_form.py，由打包脚本烘进机内策略。Go 侧用例改不动那份 Python，
  所以行为正确性必须在这里另立一套锁。

锁的层次（每一条都对应一个"改了会亏钱"的形状）：
  A. 判据真的来自 params：同一份行情，把阈值挪一下，判定必须翻转——
     证明阈值不是写死在函数体里的（这条正是本仓反复踩的"旋钮改了、产物还是旧数")；
  B. 命中出信号 / 不命中返回 None：四战法各自的满分形态与不足形态；
  C. 缺关键读数一律**不出信号**（只读不到就返回 None，不许折成假答案）：
     日K 根数不足、现价缺失、持仓未知、龙头判定 is_leader=None、连板高度
     ladder_height=None、分钟级回封 afternoon_reseal=None（这条是"反包保护"，
     把未知当成"没回封"就会多发一笔卖单）、涨停价 up_stop 缺失、
     利好事件 news_event 缺失或 propagation 未知（折扣方向，当成 False 会**放大**信号）；
  D. 形态契约本身：文件必须纯 ASCII（GBK 终端会把中文换成 '?'，把源码撕坏）、
     规则层函数体内零数字字面量（和构建门禁 G5 同一把尺子）、
     没有任何 I/O/网络/进程调用、FORM_PARAM_KEYS 与实际读取的键两向闭合。

C 组里刻意区分了两种"缺数据"：
  * 缺了会**让信号更容易发出去**的（is_leader / ladder_height / afternoon_reseal /
    news_event / propagation / up_stop）=> 拒绝出信号；
  * Go 适配层自己就写明"空即 0"的（板块涨停降幅 sector_limit_up_drop_pct、
    情绪相位 emotion_phase）=> 记 0 分。0 分只会把总分压低，只会少发不会多发，
    与 Go 逐字一致，所以这里不拒绝（拒绝反而偏离 Go 语义）。
"""

import ast
import io
import os
import re
import sys

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
GW_DIR = os.path.dirname(HERE)
if GW_DIR not in sys.path:
    sys.path.insert(0, GW_DIR)

import rules_form  # noqa: E402

RULES_MODULE = os.path.join(GW_DIR, "rules_form.py")


# ---------------------------------------------------------------------------
# 参数表：每个键的缺省值都取自 Go 源码里的那个字面量（见 rules_form.py 末尾映射）
# ---------------------------------------------------------------------------

def base_params(**overrides):
    """完整的 params 表（等价于烘进产物的 PARAMS），可用 kwargs 局部覆盖。"""
    params = {
        # --- 通道/形态共用 ---
        "lot_size": 100,
        "symbol_head_len": 6,
        "signal_codes": {"break_down": "BD", "high_churn": "HC",
                         "leader_decay": "LD", "good_news_fade": "GF"},
        "rule_side": "sell",
        "break_down_kind": "break_down",
        "high_churn_kind": "high_churn",
        "leader_decay_kind": "leader_decay",
        "good_news_fade_kind": "good_news_fade",
        # --- buildShortData 派生窗口 ---
        "ma5_window": 5,
        "ma10_window": 10,
        "ma20_window": 20,
        "vol5_window": 5,
        "vol20_window": 20,
        "hc_high_window": 60,
        "bd_low_window": 20,
        "hc_gain_window": 20,
        "hc_last3_window": 3,
        # --- break_down ---
        "bd_min_bars": 25,
        "bd_depth_max_score": 30.0,
        "bd_depth_single_cap": 25.0,
        "bd_depth_full_pct": 3.0,
        "bd_volume_max_score": 40.0,
        "bd_volume_full_ratio": 1.5,
        "bd_volume_half_ratio": 1.0,
        "bd_volume_shrink_factor": 0.5,
        "bd_intraday_max_score": 30.0,
        "bd_intraday_severe_pct": -5.0,
        "bd_intraday_mid_pct": -2.0,
        "bd_intraday_mid_base_score": 20.0,
        "bd_intraday_severe_span_pct": 3.0,
        "bd_intraday_severe_span_score": 10.0,
        "bd_intraday_flat_span_pct": 2.0,
        "bd_intraday_flat_span_score": 10.0,
        "bd_shadow_add_ratio": 0.5,
        "bd_shadow_add_score": 5.0,
        "bd_shakeout_depth_pct": 1.0,
        "bd_shakeout_discount": 0.7,
        "bd_score_threshold": 60.0,
        # --- high_churn ---
        "hc_min_bars": 60,
        "hc_pos_start": 0.92,
        "hc_pos_span": 0.08,
        "hc_pos_base_score": 15.0,
        "hc_pos_step_score": 15.0,
        "hc_position_max_score": 30.0,
        "hc_gain_start": 0.30,
        "hc_gain_span": 0.20,
        "hc_gain_base_score": 15.0,
        "hc_gain_step_score": 15.0,
        "hc_div_heavy_ratio": 1.5,
        "hc_div_stall_pct": 2.0,
        "hc_div_full_score": 40.0,
        "hc_div_half_score": 20.0,
        "hc_shadow_ratio": 0.4,
        "hc_shadow_score": 12.0,
        "hc_below_ma5_score": 10.0,
        "hc_consec_down_days": 2,
        "hc_consec_down_score": 8.0,
        "hc_weakness_max_score": 30.0,
        "hc_score_threshold": 60.0,
        # --- leader_decay ---
        "ld_min_bars": 10,
        "ld_min_boards": 2,
        "ld_board_max_score": 30.0,
        "ld_board_full_boards": 5,
        "ld_board_base_score": 12.0,
        "ld_board_base_boards": 2.0,
        "ld_board_span_boards": 3.0,
        "ld_board_span_score": 18.0,
        "ld_seal_tolerance": 0.995,
        "ld_touch_tolerance": 0.998,
        "ld_big_loss_ratio": 0.97,
        "ld_big_loss_score": 20.0,
        "ld_touched_score": 15.0,
        "ld_huge_volume_ratio": 2.0,
        "ld_huge_volume_score": 10.0,
        "ld_weakness_max_score": 40.0,
        "ld_sector_drop_ratio": 0.5,
        "ld_sector_drop_score": 20.0,
        "ld_phase_score": 10.0,
        "ld_retreat_phase_keys": ["退潮"],
        "ld_retreat_max_score": 30.0,
        "ld_score_threshold": 65.0,
        # --- good_news_fade ---
        # 逐条与 Go 侧 good_news_fade.go 的阈值同源（20 根K线门槛＝:72 的 gate），
        # 这里改一个数就必须同步改那条判据，否则用例判的是另一套策略。
        "gf_min_bars": 20,
        "gf_event_window_days": 5,
        "gf_event_fresh_age": 1,
        "gf_event_stale_age": 5,
        "gf_event_max_score": 25.0,
        "gf_event_min_score": 5.0,
        "gf_event_age_span": 4.0,
        "gf_event_age_drop": 20.0,
        "gf_gain_start": 0.10,
        "gf_gain_full": 0.25,
        "gf_gain_span": 0.15,
        "gf_gain_max_score": 35.0,
        "gf_stall_pct": 1.0,
        "gf_stall_score": 15.0,
        "gf_heavy_ratio": 1.5,
        "gf_heavy_score": 10.0,
        "gf_below_ma5_score": 8.0,
        "gf_shadow_ratio": 0.4,
        "gf_shadow_score": 7.0,
        "gf_weakness_max_score": 40.0,
        "gf_surge_window": 3,
        "gf_surge_step_pct": 0.05,
        "gf_propagation_discount": 0.7,
        "gf_score_threshold": 60.0,
    }
    params.update(overrides)
    return params


def make_bars(closes, volumes=None, highs=None, lows=None, start="2026-11-01"):
    """日K 序列（time 为 ISO 日期，供 good_news_fade 定位事件日）。"""
    count = len(closes)
    volumes = volumes or [1000.0] * count
    highs = highs or [c * 1.01 for c in closes]
    lows = lows or [c * 0.99 for c in closes]
    day = int(start[-2:])
    bars = []
    for index in range(count):
        bars.append({
            "time": "%s-%02d" % (start[:8], day + index),
            "open": closes[index], "high": highs[index],
            "low": lows[index], "close": closes[index],
            "volume": volumes[index], "amount": closes[index] * volumes[index],
        })
    return bars


def base_context(**overrides):
    """公共上下文形状（与 Executor.context_for / build_context 同源 + 本批 extras）。"""
    ctx = {
        "symbol": "600000.SH", "day": "20261105", "weekday": 3, "hhmm": "10:30",
        "trading_day": True, "last_price": 10.0, "prev_close": 10.0,
        "open": 10.0, "high": 10.1, "low": 9.9, "volume": 1000.0, "amount": 10000.0,
        "ask1": 10.01, "bid1": 9.99, "ref_price": 10.0, "volume_ratio": 1.0,
        "turnover_rate": 2.0, "position_qty": 1000.0, "position_cost": 9.0,
        "available_cash": 50000.0, "daily_buy_total": 0.0, "ops_last_minute": 0,
        "bars": [], "up_stop": None, "down_stop": None, "suspended": False,
        "index_change_pct": 0.0, "limit_up_count": 30, "ladder_height": None,
        "broken_board_ratio": None, "advancers": 1000, "decliners": 1500,
        "news_event": None, "is_leader": None,
    }
    ctx.update(overrides)
    return ctx


# ---------------------------------------------------------------------------
# 四战法各自的行情夹具（构造到 Go 语义里的"满分形态"）
# ---------------------------------------------------------------------------

def breakdown_hit_context():
    """双破位 + 放量 + 大跌：depth 30 + volume 40 + intraday 30 = 100。"""
    closes = [10.0] * 24 + [9.0]
    volumes = [1000.0] * 24 + [5000.0]
    bars = make_bars(closes, volumes)
    return base_context(bars=bars, last_price=9.0, prev_close=10.0)


def high_churn_hit_context():
    """贴 60 日高 + 放量滞涨 + 长上影：position 30 + divergence 40 + weakness 12。"""
    closes = [10.0] * 49 + [13.0] * 11
    volumes = [1000.0] * 55 + [3000.0] * 5
    highs = [c * 1.01 for c in closes]
    highs[-1] = 14.5
    lows = [c * 0.99 for c in closes]
    lows[-1] = 13.0
    bars = make_bars(closes, volumes, highs, lows)
    return base_context(bars=bars, last_price=13.0, prev_close=13.0)


def leader_decay_hit_context():
    """5 连板断板：board 30 + weakness 40（触板+大面+天量，封顶 40）+ 情绪退潮 30。"""
    closes = [10.0] * 11 + [9.0]
    volumes = [1000.0] * 11 + [5000.0]
    highs = [c * 1.01 for c in closes]
    highs[-1] = 11.0            # 盘中触板后炸板（Go TouchedBoardToday）
    lows = [c * 0.99 for c in closes]
    bars = make_bars(closes, volumes, highs, lows)
    return base_context(
        bars=bars, last_price=9.0, prev_close=10.0, up_stop=11.0,
        is_leader=True, ladder_height=5, afternoon_reseal=False,
        sector_limit_up_drop_pct=0.6, emotion_phase="退潮")


def good_news_hit_context(propagation=False):
    """4 个交易日前的利好：兑现 25% 之后横住、放量、长上影（派发形状）。"""
    closes = [8.0] * 20 + [10.0] * 4
    volumes = [1000.0] * 20 + [3000.0] * 4
    highs = [c * 1.01 for c in closes]
    highs[-1] = 10.8            # 长上影
    lows = [c * 0.99 for c in closes]
    lows[-1] = 9.9
    bars = make_bars(closes, volumes, highs, lows)
    return base_context(
        bars=bars, last_price=10.0, prev_close=10.0,
        news_event={"day": bars[19]["time"], "score": 0.8,
                    "positive": True, "propagation": propagation})


# ---------------------------------------------------------------------------
# A. 命中出信号
# ---------------------------------------------------------------------------

@pytest.mark.parametrize("maker,kind,code", [
    (breakdown_hit_context, "break_down", "BD600000"),
    (high_churn_hit_context, "high_churn", "HC600000"),
    (leader_decay_hit_context, "leader_decay", "LD600000"),
    (good_news_hit_context, "good_news_fade", "GF600000"),
])
def test_hit_emits_sell_signal(maker, kind, code):
    """满分形态必须出信号，且是 sell + 持仓数量 + 可复算的幂等短码。"""
    signal = getattr(rules_form, "rule_" + kind)(base_params(), maker())
    assert signal is not None, kind
    assert signal["kind"] == kind
    assert signal["side"] == "sell"
    assert signal["qty"] == 1000
    assert signal["short_code"] == code
    assert signal["amount"] is None


def test_break_down_reason_scores_follow_the_three_factors():
    """破位/量能/日内三因子同时成立才够门槛（这里验 100 分档位确实过）。"""
    params = base_params()
    signal = rules_form.rule_break_down(params, breakdown_hit_context())
    assert signal["price"] == 9.0
    # 把量能门槛提到 5 倍以上 -> 量能分从满分掉到线性段，总分仍高，
    # 但再把日内档收紧，就应当落回不发出（见 B 组），这里先确认命中稳定。
    assert rules_form.rule_break_down(params, breakdown_hit_context()) == signal


# ---------------------------------------------------------------------------
# B. 不命中返回 None
# ---------------------------------------------------------------------------

def test_break_down_no_break_declines():
    """没有任何破位（收在 20 日线与 20 日低之上）=> 本战法不成立。"""
    closes = [10.0] * 24 + [10.2]
    bars = make_bars(closes, [1000.0] * 24 + [5000.0])
    ctx = base_context(bars=bars, last_price=10.2, prev_close=10.0)
    assert rules_form.rule_break_down(base_params(), ctx) is None


def test_high_churn_quiet_decline():
    """既不贴高也不放量：三因子凑不满门槛 => 不发。"""
    closes = [10.0] * 59 + [9.0]
    bars = make_bars(closes, [1000.0] * 60)
    ctx = base_context(bars=bars, last_price=9.0, prev_close=10.0)
    assert rules_form.rule_high_churn(base_params(), ctx) is None


def test_leader_decay_still_sealed_declines():
    """今日仍封板 = 行情未断，Go 在 Evaluate 顶部就返回（level still_sealed）。"""
    ctx = leader_decay_hit_context()
    ctx["last_price"] = 11.0
    ctx["bars"][-1]["close"] = 11.0
    assert rules_form.rule_leader_decay(base_params(), ctx) is None


def test_good_news_fade_fresh_surge_declines():
    """近 3 日内还有 >=5% 的单日（主升未止）=> 防误杀，不发。"""
    ctx = good_news_hit_context()
    ctx["bars"][-1]["close"] = 11.0
    ctx["bars"][-1]["high"] = 11.2
    ctx["bars"][-1]["low"] = 10.9
    ctx["last_price"] = 11.0
    assert rules_form.rule_good_news_fade(base_params(), ctx) is None


def test_good_news_fade_propagation_discount_can_flip_the_verdict():
    """传导类利好打 7 折（good_news_fade.go:95）——折扣确实吃进总分。"""
    params = base_params(gf_score_threshold=60.0)
    plain = rules_form.rule_good_news_fade(params, good_news_hit_context(False))
    propagated = rules_form.rule_good_news_fade(params, good_news_hit_context(True))
    assert plain is not None
    assert propagated is None


# ---------------------------------------------------------------------------
# C. 阈值来自 params：改旋钮必须改变判定（证明没有把数写死进函数体）
# ---------------------------------------------------------------------------

def test_break_down_threshold_moves_the_verdict():
    ctx = breakdown_hit_context()
    assert rules_form.rule_break_down(base_params(), ctx) is not None
    tight = base_params(bd_score_threshold=101.0)
    assert rules_form.rule_break_down(tight, ctx) is None
    loose = base_params(bd_score_threshold=1.0)
    assert rules_form.rule_break_down(loose, ctx) is not None


def test_break_down_volume_ratio_knob_moves_the_verdict():
    """把"满分量比"从 1.5 提到 100：量能分落到线性段，总分掉下门槛。"""
    ctx = breakdown_hit_context()
    assert rules_form.rule_break_down(base_params(bd_score_threshold=95.0), ctx) is not None
    strict = base_params(bd_volume_full_ratio=100.0, bd_score_threshold=95.0)
    assert rules_form.rule_break_down(strict, ctx) is None


def test_high_churn_threshold_moves_the_verdict():
    ctx = high_churn_hit_context()
    assert rules_form.rule_high_churn(base_params(), ctx) is not None
    assert rules_form.rule_high_churn(base_params(hc_score_threshold=90.0), ctx) is None


def test_high_churn_divergence_knob_moves_the_verdict():
    """放量口径从 1.5 提到 3.0：背离从满分变单边减半，总分跌破门槛。"""
    ctx = high_churn_hit_context()
    assert rules_form.rule_high_churn(base_params(hc_score_threshold=70.0), ctx) is not None
    strict = base_params(hc_div_heavy_ratio=3.0, hc_score_threshold=70.0)
    assert rules_form.rule_high_churn(strict, ctx) is None


def test_leader_decay_threshold_moves_the_verdict():
    """板块/情绪读数缺席时只记 0 分（与 Go 的"空即 0"同源），总分只剩 70。"""
    ctx = leader_decay_hit_context()
    ctx["sector_limit_up_drop_pct"] = None
    ctx["emotion_phase"] = None
    assert rules_form.rule_leader_decay(base_params(), ctx) is not None
    assert rules_form.rule_leader_decay(base_params(ld_score_threshold=71.0), ctx) is None


def test_leader_decay_boards_knob_moves_the_verdict():
    """连板高度从 5 板改判到 4 板（board 分档线性），门槛提高后判定翻转。"""
    ctx = leader_decay_hit_context()
    ctx["sector_limit_up_drop_pct"] = None
    ctx["emotion_phase"] = None
    assert rules_form.rule_leader_decay(base_params(), ctx) is not None
    assert rules_form.rule_leader_decay(base_params(ld_min_boards=6), ctx) is None


def test_good_news_fade_threshold_moves_the_verdict():
    ctx = good_news_hit_context()
    assert rules_form.rule_good_news_fade(base_params(), ctx) is not None
    assert rules_form.rule_good_news_fade(base_params(gf_score_threshold=90.0), ctx) is None


def test_good_news_fade_event_window_knob_moves_the_verdict():
    """事件窗从 4 日收到 3 日：事件不在窗 => 战法前提不成立。"""
    ctx = good_news_hit_context()
    assert rules_form.rule_good_news_fade(base_params(gf_event_window_days=3), ctx) is None


def test_break_down_single_cap_knob_moves_the_verdict():
    """单破位的深度帽（break_down.go:156）确实读自 params：帽子一收，总分掉下门槛。"""
    # 只破 20 日线、未破 20 日低（两日前有一根 8 元的坑就是那道低）——单破位形态。
    closes = [10.0] * 20 + [8.0] + [10.0] * 3 + [9.0]
    volumes = [1000.0] * 24 + [5000.0]
    bars = make_bars(closes, volumes)
    ctx = base_context(bars=bars, last_price=9.0, prev_close=10.0)
    params = base_params(bd_score_threshold=85.0)
    assert rules_form.rule_break_down(params, ctx) is not None
    tightened = base_params(bd_score_threshold=85.0, bd_depth_single_cap=10.0)
    assert rules_form.rule_break_down(tightened, ctx) is None


# ---------------------------------------------------------------------------
# C2. 缺关键读数 => 拒绝出信号（绝不折成假答案）
# ---------------------------------------------------------------------------

def test_non_trading_day_declines_for_every_rule():
    ctx = breakdown_hit_context()
    ctx["trading_day"] = False
    for name in ("rule_break_down", "rule_high_churn", "rule_leader_decay",
                 "rule_good_news_fade"):
        assert getattr(rules_form, name)(base_params(), ctx) is None


def test_missing_bars_declines_for_every_rule():
    """日K 拿不到（bars 缺省/过短）=> 四战法一律不发。"""
    for name in ("rule_break_down", "rule_high_churn", "rule_leader_decay",
                 "rule_good_news_fade"):
        ctx = base_context(bars=[])
        assert getattr(rules_form, name)(base_params(), ctx) is None


def test_break_down_short_history_declines():
    """25 根硬闸（break_down.go:72）：差一根也不评。"""
    closes = [10.0] * 23 + [9.0]
    bars = make_bars(closes, [1000.0] * 23 + [5000.0])
    ctx = base_context(bars=bars, last_price=9.0, prev_close=10.0)
    assert rules_form.rule_break_down(base_params(), ctx) is None
    assert rules_form.rule_break_down(base_params(bd_min_bars=24), ctx) is not None


def test_missing_price_declines():
    ctx = breakdown_hit_context()
    ctx["last_price"] = None
    ctx["bars"] = []
    assert rules_form.rule_break_down(base_params(), ctx) is None


def test_unknown_position_declines():
    """持仓未知/为零 => 沙箱里没有 watch 这个动作，只能不发。"""
    ctx = breakdown_hit_context()
    ctx["position_qty"] = None
    assert rules_form.rule_break_down(base_params(), ctx) is None
    ctx["position_qty"] = 0
    assert rules_form.rule_break_down(base_params(), ctx) is None


def test_leader_decay_declines_when_leader_verdict_unknown():
    ctx = leader_decay_hit_context()
    ctx["is_leader"] = None
    assert rules_form.rule_leader_decay(base_params(), ctx) is None


def test_leader_decay_declines_when_ladder_height_unknown():
    """连板高度要靠日K+板感知涨幅推断，沙箱里没有名称判据 => 未知即拒绝。"""
    ctx = leader_decay_hit_context()
    ctx["ladder_height"] = None
    assert rules_form.rule_leader_decay(base_params(), ctx) is None


def test_leader_decay_declines_when_reseal_state_unknown():
    """14:30 后回封是"抑制信号"的读数：未知当成 False 会多发一笔卖单 => 必须拒绝。"""
    ctx = leader_decay_hit_context()
    ctx["afternoon_reseal"] = None
    assert rules_form.rule_leader_decay(base_params(), ctx) is None
    ctx["afternoon_reseal"] = True
    assert rules_form.rule_leader_decay(base_params(), ctx) is None


def test_leader_decay_declines_without_limit_price():
    ctx = leader_decay_hit_context()
    ctx["up_stop"] = None
    assert rules_form.rule_leader_decay(base_params(), ctx) is None


def test_leader_decay_scores_missing_sentiment_as_zero():
    """Go 适配层写明"空即 0"（shortbase.go:79-80）：缺读数只压分，不制造信号。"""
    ctx = leader_decay_hit_context()
    ctx["sector_limit_up_drop_pct"] = None
    ctx["emotion_phase"] = None
    fired = rules_form.rule_leader_decay(base_params(), ctx)
    assert fired is not None
    # 同样缺读数、但门槛高于可得总分 => 不发（缺数据不会反过来放宽判据）。
    assert rules_form.rule_leader_decay(base_params(ld_score_threshold=99.0), ctx) is None


def test_good_news_fade_declines_without_event():
    """事件是本战法的前提：news_event 缺失/非正向/字段不全 => 一律不发。"""
    ctx = good_news_hit_context()
    ctx["news_event"] = None
    assert rules_form.rule_good_news_fade(base_params(), ctx) is None
    ctx["news_event"] = {"day": "2026-11-21", "score": 0.8,
                        "positive": False, "propagation": False}
    assert rules_form.rule_good_news_fade(base_params(), ctx) is None
    ctx["news_event"] = {"day": "2026-11-21", "score": 0.8, "positive": True}
    assert rules_form.rule_good_news_fade(base_params(), ctx) is None
    ctx["news_event"] = {"day": "1999-01-01", "score": 0.8,
                         "positive": True, "propagation": False}
    assert rules_form.rule_good_news_fade(base_params(), ctx) is None


def test_foreign_side_hint_is_refused():
    """四战法只有 sell 方向；递进来一个 buy 只能拒绝，不能改判方向。"""
    assert rules_form.rule_break_down(base_params(), breakdown_hit_context(), "buy") is None
    assert rules_form.rule_break_down(base_params(), breakdown_hit_context(), "sell") is not None


# ---------------------------------------------------------------------------
# D. 形态契约（构建门禁稍后会独立再判一次，这里先把同源判据钉在用例里）
# ---------------------------------------------------------------------------

def _source_and_tree():
    with io.open(RULES_MODULE, "r", encoding="utf-8") as handle:
        source = handle.read()
    return source, ast.parse(source)


def test_rules_module_is_pure_ascii():
    """GBK 终端会把任何非 ASCII 字节换成 '?'：整文件必须纯 ASCII。"""
    source, _tree = _source_and_tree()
    offenders = [index for index, char in enumerate(source) if ord(char) > 127]
    assert offenders == [], "non-ascii offsets=%s" % (offenders[:5],)


def test_rule_layer_bodies_contain_no_numeric_literal():
    """与 scripts/build_local_executor.py 的 G5 同一把尺子：规则层零数字字面量。"""
    source, tree = _source_and_tree()
    begin = source.index("# ==== RULE LAYER BEGIN ====")
    end = source.index("# ==== RULE LAYER END ====")
    def lineno(offset):
        return source[:offset].count("\n") + 1
    first, last = lineno(begin), lineno(end)
    hits = []
    for node in ast.walk(tree):
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        if not (first <= node.lineno <= last):
            continue
        for inner in ast.walk(node):
            if isinstance(inner, ast.Constant) and not isinstance(inner.value, bool):
                if isinstance(inner.value, (int, float)):
                    hits.append("%s:%r" % (node.name, inner.value))
    assert hits == [], "bare numbers in rule layer=%s" % (hits,)


def test_rules_module_does_no_io_and_imports_nothing_outside_stdlib():
    """沙箱里不许有文件/网络/进程/日志；这里连 import 都没有。"""
    _source, tree = _source_and_tree()
    imports = [node for node in ast.walk(tree)
               if isinstance(node, (ast.Import, ast.ImportFrom))]
    assert imports == [], "rules_form must not import anything"
    banned = {"open", "socket", "requests", "urllib", "sqlite3", "subprocess",
              "logging", "os", "sys", "eval", "exec", "compile", "__import__",
              "globals", "locals"}
    names = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Name):
            names.add(node.id)
        if isinstance(node, ast.Attribute):
            names.add(node.attr)
    assert not (names & banned), "privileged names used=%s" % sorted(names & banned)


def test_form_param_keys_close_the_loop():
    """FORM_PARAM_KEYS 与实际读取的键两向闭合：多一个键或漏一个键都算红。"""
    source, _tree = _source_and_tree()
    used = set(re.findall(r'params\["([a-z0-9_]+)"\]', source))
    declared = set(rules_form.FORM_PARAM_KEYS)
    assert used - declared == set(), "read but not declared=%s" % sorted(used - declared)
    assert declared - used == set(), "declared but never read=%s" % sorted(declared - used)


def test_base_params_cover_form_param_keys():
    """测试夹具本身也必须跟着参数表走，否则新键漏配会在这里先红。"""
    missing = [key for key in rules_form.FORM_PARAM_KEYS if key not in base_params()]
    assert missing == [], "base_params missing=%s" % missing


def test_go_defaults_are_the_baked_values():
    """四个门槛的缺省值取自 Go（60/60/65/60），断板那条最严是 Go 源码写明的一档。"""
    params = base_params()
    assert params["bd_score_threshold"] == 60.0
    assert params["hc_score_threshold"] == 60.0
    assert params["ld_score_threshold"] == 65.0
    assert params["gf_score_threshold"] == 60.0


def test_rules_registry_matches_the_kinds_the_rules_emit():
    """RULES 的键 = 规则实际发出的 kind = params["<rule>_kind"]，三者必须同源。

    构建脚本按 AST 读这张表：注册了一个 params 里没有短码的 kind，就等于交出一枚
    "柜台回报无法证明幂等"的规则（短码是唯一的重发判据），所以这里双向锁死。
    """
    params = base_params()
    kinds = {"break_down": "break_down_kind", "high_churn": "high_churn_kind",
             "leader_decay": "leader_decay_kind", "good_news_fade": "good_news_fade_kind"}
    assert sorted(rules_form.RULES) == sorted(kinds)
    for kind, key in kinds.items():
        assert params[key] == kind
        assert rules_form.RULES[kind].__name__ == "rule_" + kind
        # 每个在册 kind 都得有自己的短码，否则构建侧会直接拒发布
        assert params["signal_codes"].get(kind)
    # 短码不许两两相同：共用一枚短码 = 第一根的委托把第二根永久消音
    codes = [params["signal_codes"][kind] for kind in kinds]
    assert len(set(codes)) == len(codes)


def test_emitted_kind_is_the_registered_kind():
    """出信号的 kind 必须就是在册的那枚（否则影子计数会把它算成陌生规则）。"""
    cases = (("rule_break_down", breakdown_hit_context),
             ("rule_high_churn", high_churn_hit_context),
             ("rule_leader_decay", leader_decay_hit_context),
             ("rule_good_news_fade", good_news_hit_context))
    for name, maker in cases:
        signal = getattr(rules_form, name)(base_params(), maker())
        assert signal["kind"] in rules_form.RULES, name


def test_signal_builder_matches_local_executor_shape():
    """短码/字段形状与 local_executor.make_signal 一致，否则幂等键两侧不同源。"""
    import local_executor as lex
    ctx = breakdown_hit_context()
    ours = rules_form.rule_break_down(base_params(), ctx)
    theirs = lex.make_signal(base_params(), ctx, "break_down", "sell", None, 9.0, qty=1000)
    assert ours == theirs


if __name__ == "__main__":
    sys.exit(pytest.main([__file__, "-q"]))
