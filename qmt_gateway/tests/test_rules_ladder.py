#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""rules_ladder 单元测试：四条战法腿（破局龙 / 龙回头 / 双凸 / 龙头断板）的行为锁。

覆盖每个规则的四种形态：
  * 命中 —— 用 Go 侧同款夹具读数复现评分（dragon_return 直接用
    dragon_return_test.go goodStock 的原始值）；
  * 不命中 —— 阈值不达标时必须返回 None；
  * 缺读数弃权 —— up_stop / bars / consec_boards / afternoon_reseal /
    is_leader / sector_rps20 缺失一律返回 None，绝不猜；
  * 改 params 会改变判定 —— 证明判据真的来自 params（烘参单点），不是写死。

另含两条全局锁：
  * rules_ladder.py 必须纯 ASCII（GBK 终端会把非 ASCII 静默变 '?'）；
  * 源码里读到的每一个 params 键都必须在 LADDER_PARAM_KEYS 台账里。
"""

import os
import re
import sys

_GATEWAY_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, _GATEWAY_DIR)

import pytest  # noqa: E402

import rules_ladder  # noqa: E402

MODULE_PATH = os.path.join(_GATEWAY_DIR, "rules_ladder.py")


def base_params():
    """烘参基线：数值逐条取自 Go 的 config.go:5332-5353 默认档与
    dragon_return.DefaultParams()（95-113），测试只改这些键来证明判据来自 params。"""
    return {
        "pct_scale": 100.0,
        "lot_size": 100,
        "symbol_head_len": 6,
        "signal_codes": {
            "dragon_breakout": "DB",
            "dragon_return_buy": "DR",
            "double_bump_buy": "BB",
            "leader_decay_sell": "LD",
        },
        "ladder_buy_amount": 20000.0,
        # dragon（internal/strategies/dragon/dragon.go）
        "dragon_min_bars": 5,
        "dragon_seal_tolerance_pct": 0.5,
        "dragon_f1_weight": 0.30,
        "dragon_f1_base_ratio": 0.9,
        "dragon_f1_vol_ratio": 0.1,
        "dragon_f2_weight": 0.25,
        "dragon_f2_full_pct": 3.0,
        "dragon_f2_half_pct": 1.0,
        "dragon_f2_half_ratio": 0.5,
        "dragon_f3_weight": 0.20,
        "dragon_f3_premium_pct": 2.0,
        "dragon_f3_own_gain_pct": 5.0,
        "dragon_f3_half_ratio": 0.5,
        "dragon_rs_window": 5,
        "dragon_f4_weight": 0.25,
        "dragon_f4_full_pct": 10.0,
        "dragon_f4_half_pct": 5.0,
        "dragon_f4_half_ratio": 0.5,
        "dragon_full_chain_min": 60.0,
        # dragon_return（dragon_return.go + combat_agent/adapter.go:138-202）
        "dr_min_bars": 30,
        "dr_ma_short": 5,
        "dr_ma_mid": 10,
        "dr_ma_long": 20,
        "dr_rally_window": 40,
        "dr_first_rise_min": 0.35,
        "dr_first_rise_max": 0.70,
        "dr_sector_rps_min": 75.0,
        "dr_score_dragon": 25.0,
        "dr_pb_best_lo": 0.15,
        "dr_pb_best_hi": 0.20,
        "dr_pb_good_hi": 0.25,
        "dr_pb_shallow_lo": 0.10,
        "dr_pb_deep_hi": 0.35,
        "dr_pb_score_best": 8.0,
        "dr_pb_score_good": 6.0,
        "dr_pb_score_shallow": 5.0,
        "dr_pb_score_deep": 4.0,
        "dr_pb_score_other": 1.0,
        "dr_days_good_lo": 5,
        "dr_days_good_hi": 8,
        "dr_days_short_lo": 3,
        "dr_days_long_hi": 12,
        "dr_days_score_good": 5.0,
        "dr_days_score_near": 3.0,
        "dr_days_score_other": 1.0,
        "dr_vr_good": 0.3,
        "dr_vr_ok": 0.4,
        "dr_vr_mid": 0.5,
        "dr_vr_bad": 0.6,
        "dr_vr_score_good": 8.0,
        "dr_vr_score_ok": 7.0,
        "dr_vr_score_mid": 5.0,
        "dr_vr_score_bad": 3.0,
        "dr_vr_score_other": 1.0,
        "dr_shrink_max": 0.5,
        "dr_duck_top_full": 5.0,
        "dr_duck_top_half": 2.0,
        "dr_neck_support_ratio": 0.98,
        "dr_duck_neck_full": 8.0,
        "dr_duck_neck_low": 5.0,
        "dr_slope_prev_ratio": 0.99,
        "dr_slope_delta_ratio": 0.01,
        "dr_slope_min": 0.5,
        "dr_duck_slope_bonus": 2.0,
        "dr_macd_green_near": 1.0,
        "dr_beak_vr_lo": 0.5,
        "dr_beak_vr_hi": 1.2,
        "dr_beak_score_three": 7.0,
        "dr_beak_score_two": 4.0,
        "dr_beak_score_one": 2.0,
        "dr_nostril_full": 5.0,
        "dr_nostril_half": 3.0,
        "dr_confirm_vr_lo": 1.2,
        "dr_confirm_vr_hi": 1.5,
        "dr_confirm_vol_full": 7.0,
        "dr_confirm_vr_mid_lo": 0.8,
        "dr_confirm_vol_mid": 3.0,
        "dr_k_hold_ratio": 1.03,
        "dr_k_score_hold": 5.0,
        "dr_k_score_cross": 2.0,
        "dr_k_near_lo_ratio": 0.98,
        "dr_k_score_near": 2.0,
        "dr_level_first": 60.0,
        # double_bump（double_bump.go + config.go:5346-5353）
        "db_min_bars": 10,
        "db_weak_day_pct": -1.5,
        "db_min_change_pct": 0.0,
        "db_lookback": 20,
        "db_first_window": 5,
        "db_break_ratio": 1.05,
        "db_first_vol_multiple": 1.5,
        "db_second_vol_multiple": 1.5,
        "db_volume_weight": 0.4,
        "db_adjust_max_pct": 6.0,
        "db_position_weight": 0.3,
        "db_ma_short": 5,
        "db_ma_mid": 10,
        "db_ma_weight": 0.3,
        "db_ma_align_ratio": 0.8,
        "db_full_chain_min": 60.0,
        # leader_decay（leader_decay.go + shortbase.go）
        "ld_min_bars": 10,
        "ld_vol_window": 5,
        "ld_min_boards": 2,
        "ld_board_full_boards": 5,
        "ld_board_score_full": 30.0,
        "ld_board_score_base": 12.0,
        "ld_board_score_span": 18.0,
        "ld_big_face_ratio": 0.97,
        "ld_score_big_face": 20.0,
        "ld_score_touched": 15.0,
        "ld_huge_volume_ratio": 2.0,
        "ld_score_huge_volume": 10.0,
        "ld_weakness_cap": 40.0,
        "ld_sector_drop_ratio": 0.5,
        "ld_score_sector_drop": 20.0,
        "ld_score_phase": 10.0,
        "ld_retreat_cap": 30.0,
        "ld_retreat_tokens": ["退潮"],
        "ld_min_score": 65.0,
    }


def dragon_bars():
    """破局龙夹具：末根贴板放量，5 日趋势 +34%（dragon.go:192 口径）。"""
    closes = [8.0, 8.2, 8.5, 8.8, 9.0, 11.0]
    return [{"close": c, "high": c, "low": c, "volume": 100000.0, "amount": 1000000.0}
            for c in closes]


def dragon_ctx(**over):
    ctx = {
        "symbol": "600123.SH",
        "last_price": 11.0,
        "prev_close": 10.0,
        "up_stop": 11.0,
        "sector_best_change_pct": 4.0,
        "bars": dragon_bars(),
    }
    ctx.update(over)
    return ctx


# ---------------------------------------------------------------------------
# rule_dragon
# ---------------------------------------------------------------------------

def test_dragon_hit_full_chain():
    # F1=27.3(27+0.3) F2=25 F3=20 F4=25 => 97.3 >= 60 -> full_chain 买入
    sig = rules_ladder.rule_dragon(base_params(), dragon_ctx())
    assert sig is not None
    assert sig["kind"] == "dragon_breakout"
    assert sig["side"] == "buy"
    assert sig["symbol"] == "600123.SH"
    assert sig["qty"] == 1800            # 20000/11 向下取整到百股
    assert sig["short_code"] == "DB600123"


def test_dragon_no_hit_weak_tape():
    ctx = dragon_ctx(last_price=10.1, sector_best_change_pct=0.0)
    assert rules_ladder.rule_dragon(base_params(), ctx) is None


def test_dragon_abstain_without_up_stop():
    """涨跌停价是板上唯一封板参照（Go 用 LimitUpPct 码表，沙箱必须自算）；
    缺 up_stop 只能弃权，不许拿涨幅猜封板。"""
    assert rules_ladder.rule_dragon(base_params(), dragon_ctx(up_stop=None)) is None


def test_dragon_abstain_without_bars():
    assert rules_ladder.rule_dragon(base_params(), dragon_ctx(bars=None)) is None
    assert rules_ladder.rule_dragon(base_params(), dragon_ctx(bars=[])) is None


def test_dragon_params_drive_the_verdict():
    """把 full_chain 门槛抬到 99.5：97.3 分不再成交 —— 判据确实来自 params。"""
    p = base_params()
    p["dragon_full_chain_min"] = 99.5
    assert rules_ladder.rule_dragon(p, dragon_ctx()) is None
    # 封板容差放宽到 9.6%（等于不再要求贴板）后，弱票也能拿到 F1 满分档
    q = base_params()
    assert rules_ladder.rule_dragon(q, dragon_ctx(last_price=10.1)) is None
    q["dragon_seal_tolerance_pct"] = 9.6
    assert rules_ladder.rule_dragon(q, dragon_ctx(last_price=10.1)) is not None


# ---------------------------------------------------------------------------
# rule_dragon_return
# ---------------------------------------------------------------------------

def dr_readout_ctx(**over):
    """逐值复刻 Go 的 goodStock()（dragon_return_test.go:17-35）的聚合读数。"""
    ctx = {
        "symbol": "000456.SZ",
        "last_price": 12.0,
        "is_leader": True,
        "sector_rps20": 80.0,
        "first_rise_pct": 0.5,
        "pullback_pct": 0.18,
        "pullback_days": 6,
        "shrink_ratio": 0.2,
        "ma5": 11.0,
        "ma10": 11.2,
        "ma20": 10.8,
        "macd_green": -0.3,
    }
    ctx.update(over)
    return ctx


def test_dragon_return_hit_go_fixture():
    # Go: 25 + 21(8+5+8) + 12(5+2+5) + 7(5+2) = 65 >= 60 -> first 档买入
    sig = rules_ladder.rule_dragon_return(base_params(), dr_readout_ctx())
    assert sig is not None
    assert sig["kind"] == "dragon_return_buy"
    assert sig["side"] == "buy"
    assert sig["qty"] == 1600            # 20000/12 取整到百股
    assert sig["short_code"] == "DR000456"


def test_dragon_return_abstain_when_is_leader_missing():
    """龙头不虚构：is_leader 为 None 时必须弃权 —— 板块清单/指数权重在
    券商内置面上 ABSENT，池内涨幅不许冒充龙头。"""
    ctx = dr_readout_ctx()
    del ctx["is_leader"]
    assert rules_ladder.rule_dragon_return(base_params(), ctx) is None


def test_dragon_return_abstain_when_rps_missing():
    ctx = dr_readout_ctx()
    del ctx["sector_rps20"]
    assert rules_ladder.rule_dragon_return(base_params(), ctx) is None


def test_dragon_return_no_hit_identity_gate():
    """Go dragonIdentity 硬闸（dragon_return.go:292-303）：任一项不达标 -> 整体否决。"""
    assert rules_ladder.rule_dragon_return(base_params(), dr_readout_ctx(is_leader=False)) is None
    assert rules_ladder.rule_dragon_return(base_params(), dr_readout_ctx(first_rise_pct=0.2)) is None
    assert rules_ladder.rule_dragon_return(base_params(), dr_readout_ctx(sector_rps20=40.0)) is None


def test_dragon_return_params_drive_the_verdict():
    p = base_params()
    p["dr_level_first"] = 70.0           # 65 分被抬高的首档门槛挡下
    assert rules_ladder.rule_dragon_return(p, dr_readout_ctx()) is None
    p2 = base_params()
    p2["dr_sector_rps_min"] = 40.0       # RPS 门槛放松后 rps=40 也过龙性闸
    assert rules_ladder.rule_dragon_return(p2, dr_readout_ctx(sector_rps20=40.0)) is not None


def dr_derived_bars():
    """31 根：8 起步线性拉到 12（idx24 见顶），随后 6 根回落；量能末段缩到 1/4。
    复刻 combat_agent/adapter.go:151-192 的派生路径（首轮 +50%、回调 18%、
    缩量 0.25、回调 6 天）。"""
    rise = [8.0 + i * (4.0 / 24.0) for i in range(25)]
    fall_closes = [11.5, 10.6, 10.2, 9.9, 9.8, 9.7]
    closes = rise + fall_closes
    volumes = [1000.0] * 26 + [200.0] * 5
    return [{"close": c, "high": c, "low": c, "volume": v, "amount": c * v}
            for c, v in zip(closes, volumes)]


def test_dragon_return_bars_derivation_path():
    """bars-only：默认 60 档不成交（派生总分 53），把首档降到 50 立刻成交 ——
    证明派生读数真的进了评分，而评分真的由 params 决定。"""
    ctx = dr_readout_ctx()
    for k in ("first_rise_pct", "pullback_pct", "pullback_days",
              "shrink_ratio", "ma5", "ma10", "ma20", "macd_green"):
        del ctx[k]
    ctx["bars"] = dr_derived_bars()
    ctx["last_price"] = 9.84
    p = base_params()
    assert rules_ladder.rule_dragon_return(p, ctx) is None
    p["dr_level_first"] = 50.0
    assert rules_ladder.rule_dragon_return(p, ctx) is not None


def test_dragon_return_abstain_without_any_history():
    ctx = dr_readout_ctx()
    for k in ("first_rise_pct", "pullback_pct", "pullback_days",
              "shrink_ratio", "ma5", "ma10", "ma20"):
        del ctx[k]
    ctx["bars"] = [{"close": 10.0}] * 12   # < dr_min_bars(30)：安全降级（adapter.go:147）
    assert rules_ladder.rule_dragon_return(base_params(), ctx) is None


# ---------------------------------------------------------------------------
# rule_double_bump
# ---------------------------------------------------------------------------

def db_bars(second_wave=True):
    """25 根夹具（double_bump.go:140-299 口径）：idx21 首波放量突破，
    idx24 二波放量；基准窗口 idx4..23 均量 115 / 均价 10.075。"""
    closes = [10.0] * 21 + [11.0, 10.2, 10.3, 10.9]
    volumes = [100.0] * 21 + [400.0, 100.0, 100.0, 300.0 if second_wave else 100.0]
    bars = []
    for i, (c, v) in enumerate(zip(closes, volumes)):
        spread = 0.2 if i == len(closes) - 1 and second_wave else 0.05
        bars.append({"close": c, "high": c + spread / 2, "low": c - spread / 2,
                     "volume": v, "amount": c * v})
    return bars


def db_ctx(**over):
    ctx = {
        "symbol": "600789.SH",
        "last_price": 10.9,
        "prev_close": 10.4,
        "bars": db_bars(),
    }
    ctx.update(over)
    return ctx


def test_double_bump_hit_two_waves():
    # vol 40 + adjust 30 + ma 30 = 100 >= 60 -> full_chain 买入
    sig = rules_ladder.rule_double_bump(base_params(), db_ctx())
    assert sig is not None
    assert sig["kind"] == "double_bump_buy"
    assert sig["side"] == "buy"
    assert sig["qty"] == 1800            # 20000/10.9 取整到百股
    assert sig["short_code"] == "BB600789"


def test_double_bump_no_hit_without_second_wave():
    """Go 双凸硬闸（double_bump.go:219-227）：没有第二波放量就不是双凸。"""
    ctx = db_ctx(bars=db_bars(second_wave=False))
    assert rules_ladder.rule_double_bump(base_params(), ctx) is None


def test_double_bump_no_hit_weak_day():
    """当日跌幅 <= -1.5% -> Go 直接判 watch（double_bump.go:148-155）不出单。"""
    ctx = db_ctx(last_price=10.2, prev_close=10.4)   # -1.9%
    assert rules_ladder.rule_double_bump(base_params(), ctx) is None


def test_double_bump_underwater_blocks_second_wave():
    """方向闸（double_bump.go:162）：水下放量不算第二波（出货），量能分强制 0。"""
    ctx = db_ctx(last_price=10.39, prev_close=10.4)  # -0.1%: up_session 不成立
    assert rules_ladder.rule_double_bump(base_params(), ctx) is None


def test_double_bump_abstain_without_bars():
    assert rules_ladder.rule_double_bump(base_params(), db_ctx(bars=None)) is None
    assert rules_ladder.rule_double_bump(base_params(), db_ctx(bars=[{"close": 10.0}] * 5)) is None
    assert rules_ladder.rule_double_bump(base_params(), db_ctx(prev_close=None)) is None


def test_double_bump_params_drive_the_verdict():
    p = base_params()
    p["db_first_vol_multiple"] = 100.0    # 首波倍数抬高 -> 形态前提消失
    assert rules_ladder.rule_double_bump(p, db_ctx()) is None
    q = base_params()
    q["db_full_chain_min"] = 101.0        # 总分封顶 100，门槛 101 谁都过不了
    assert rules_ladder.rule_double_bump(q, db_ctx()) is None


# ---------------------------------------------------------------------------
# rule_leader_decay_v2（shortbase.Data 消费腿）
# ---------------------------------------------------------------------------

def ld_bars():
    volumes = [100.0] * 10 + [500.0]
    return [{"close": 11.0, "high": 11.6, "low": 10.5, "volume": v, "amount": 11.0 * v}
            for v in volumes]


def ld_ctx(**over):
    ctx = {
        "symbol": "001234.SZ",
        "last_price": 10.6,
        "prev_close": 11.0,
        "up_stop": 12.1,
        "consec_boards": 3,
        "sealed_today": False,
        "afternoon_reseal": False,
        "touched_board_today": True,
        "sector_limit_up_drop_pct": 0.6,
        "emotion_phase": "退潮",
        "position_qty": 700,
        "bars": ld_bars(),
    }
    ctx.update(over)
    return ctx


def test_leader_decay_hit_sell():
    # board 18 + weakness min(20+15+10,40)=40 + retreat 30 = 88 >= 65 -> 卖出
    sig = rules_ladder.rule_leader_decay_v2(base_params(), ld_ctx())
    assert sig is not None
    assert sig["kind"] == "leader_decay_sell"
    assert sig["side"] == "sell"
    assert sig["qty"] == 700
    assert sig["short_code"] == "LD001234"


def test_leader_decay_still_sealed_blocks():
    """Go still_sealed 腿（leader_decay.go:85-88）：今日仍封板 -> 行情未断。"""
    assert rules_ladder.rule_leader_decay_v2(base_params(), ld_ctx(sealed_today=True)) is None


def test_leader_decay_reseal_protection():
    """Go 反包保护（leader_decay.go:90-93）：14:30 后回封 -> 当日不发，留待次日。"""
    assert rules_ladder.rule_leader_decay_v2(base_params(), ld_ctx(afternoon_reseal=True)) is None


def test_leader_decay_abstain_without_boards():
    """连板数是每票历史事实，池内 ladder_height 不能替代（缺即弃权）。"""
    ctx = ld_ctx()
    del ctx["consec_boards"]
    assert rules_ladder.rule_leader_decay_v2(base_params(), ctx) is None


def test_leader_decay_abstain_without_reseal_reading():
    ctx = ld_ctx()
    del ctx["afternoon_reseal"]
    assert rules_ladder.rule_leader_decay_v2(base_params(), ctx) is None


def test_leader_decay_no_hit_below_gate():
    """2 板 + 无弱势 + 无退潮 = 12 分：连板高度不足与低分都出不了单。"""
    ctx = ld_ctx(consec_boards=1)
    assert rules_ladder.rule_leader_decay_v2(base_params(), ctx) is None
    weak = ld_ctx(touched_board_today=False, sector_limit_up_drop_pct=None,
                  emotion_phase="", prev_close=10.9)
    assert rules_ladder.rule_leader_decay_v2(base_params(), weak) is None


def test_leader_decay_requires_position():
    ctx = ld_ctx()
    del ctx["position_qty"]
    assert rules_ladder.rule_leader_decay_v2(base_params(), ctx) is None


def test_leader_decay_params_drive_the_verdict():
    p = base_params()
    p["ld_min_score"] = 89.0              # 88 分被抬高的门槛挡下
    assert rules_ladder.rule_leader_decay_v2(p, ld_ctx()) is None
    q = base_params()
    q["ld_min_boards"] = 4                # 3 板不再满足连板前提
    assert rules_ladder.rule_leader_decay_v2(q, ld_ctx()) is None
    r = base_params()
    r["ld_big_face_ratio"] = 1.0          # "大面"口径放宽：弱势分 +20，仍 >=65
    assert rules_ladder.rule_leader_decay_v2(r, ld_ctx()) is not None


def test_leader_decay_seal_self_compute_from_up_stop():
    """sealed_today 未烘参时用 UpStopPrice 自算（11-04 实读：可自算一字/封板）。"""
    ctx = ld_ctx()
    del ctx["sealed_today"]
    assert rules_ladder.rule_leader_decay_v2(base_params(), ctx) is not None
    sealed = ld_ctx(last_price=12.1)      # 现价已达涨停价
    del sealed["sealed_today"]
    assert rules_ladder.rule_leader_decay_v2(base_params(), sealed) is None


# ---------------------------------------------------------------------------
# 全局形状锁
# ---------------------------------------------------------------------------

def test_rules_ladder_source_is_pure_ascii():
    with open(MODULE_PATH, "rb") as handle:
        raw = handle.read()
    bad = [i for i, byte in enumerate(raw) if byte >= 128]
    assert not bad, "non-ASCII bytes at %s (GBK terminal would print '?')" % bad[:5]


def test_ladder_param_keys_cover_every_reference():
    with open(MODULE_PATH, "r", encoding="ascii") as handle:
        source = handle.read()
    referenced = set(re.findall(r'params\[["\']([A-Za-z0-9_]+)["\']\]', source))
    declared = set(rules_ladder.LADDER_PARAM_KEYS)
    assert referenced <= declared, "params key read but not台账: %s" % sorted(referenced - declared)
    assert declared <= referenced, "台账键从未被读: %s" % sorted(declared - referenced)


def test_rule_signatures_carry_side_hint():
    import inspect
    for name in ("rule_dragon", "rule_dragon_return", "rule_double_bump", "rule_leader_decay_v2"):
        fn = getattr(rules_ladder, name)
        spec = inspect.signature(fn)
        params = list(spec.parameters)
        assert params[:3] == ["params", "ctx", "side_hint"]
        assert spec.parameters["side_hint"].default == ""


if __name__ == "__main__":
    sys.exit(pytest.main([__file__, "-q"]))
