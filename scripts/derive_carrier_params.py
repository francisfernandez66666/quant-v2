#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""scripts/derive_carrier_params.py —— Go 出厂默认 → 载体参数文件的单点导出。

为什么需要它（2026-11-04）
  QMT 内置策略（qmt_internal_strategy.py）和本地执行器（local_executor.py）都不读 Go 的
  配置对象，它们只认 `qmt_gateway/local_executor_params.json` 里烘进去的数。那些数以前是
  **人手从 Go 抄过来**的：抄错一位（0.45 抄成 0.54）不会有任何东西响，而它改的是策略的
  含义——这正是本仓反复锤过的"两处各一份账"。

  本脚本把"抄"从流程里拿掉：值只存在于 Go 的一处（DefaultSizingConfig / 零值 TConfig 的
  访问器 / DefaultQMTConfig / paper.DefaultConfig / research 的内置相位阈值 /
  signalctl.MissingKeyTargetCap），由 `go run ./cmd/derive_carrier_params` 打出来，
  本脚本只做**改名、改形、合入、对账**，不产生任何一个新数字。

四条纪律
  1. 本文件**不出现任何阈值数字**。需要形态转换（逗号串 → [起,止] 数组、
     index_scale_thresholds → bands/scales、方向布尔 → mode 串）时，转换只做重命名/拆分，
     不做算术。任何 `* 100`、`+ 0.5` 都等于在脚本里造第二个真源。
  2. 只写参数文件。不改规则模块、不改 Go、不碰现网。
  3. 未映射的载体键必须显式登记理由（CARRIER_OWNED）。既不派生也不登记的键＝孤儿，
     判红。这条锁的意义是"没有主人的数不许混进来"，而不是"尽量都派生"。
  4. 非 ASCII 不进参数文件（GBK 控制台会把中文字节静默换成 ?，构建器 g1 也会直接拒产物），
     所以写盘一律 ensure_ascii=True，并在写完后逐字节复验。

用法
  python3 scripts/derive_carrier_params.py --report          # 只看对账，不写（缺省）
  python3 scripts/derive_carrier_params.py --check           # 门禁姿势：有漂移/孤儿则退出码 1
  python3 scripts/derive_carrier_params.py --write           # 把派生值合进参数文件
  python3 scripts/derive_carrier_params.py --emit-json       # 只打 Go 派生表（调试用）

输出锚点（纯 ASCII，供门禁与人读）
  DERIVE_SUMMARY derived=... mapped=... added=... changed=... unchanged=... orphan=...
  DERIVE_ADDED key=... value=... source=...
  DERIVE_CHANGED key=... old=... new=... source=...
  DERIVE_ORPHAN key=...                 （既不派生也没登记理由）
  DERIVE_DEFAULTS_DRIFT key=... table=... go=...   （rules_sizing 兜底表与 Go 不一致）
  DERIVE_RESULT=OK|DRIFT|FAIL
"""

import argparse
import json
import os
import subprocess
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PARAMS_PATH = os.path.join(REPO_ROOT, "qmt_gateway", "local_executor_params.json")
QMT_GATEWAY = os.path.join(REPO_ROOT, "qmt_gateway")

# ---------------------------------------------------------------------------
# 映射表：Go 派生键 → 载体键
#
# 一条记录只说三件事：落到哪个键、要不要改形、真源在哪（真源字符串由 Go 侧的
# provenance 覆盖，这里留空时自动取）。同一个 Go 键可以落多个载体键（index 那条），
# 但任何一个载体键只许有一个来源，否则就是两处供给同一枚数。
# ---------------------------------------------------------------------------

# 时段闸：Go 是 "09:20-09:30,14:57-15:00"，载体是 [["09:20","09:30"], ...]。
# 只做拆分，不校验窗口含义——窗口语义的判据在 Go 侧（§CANCEL-STATE 三副本同步）。
def _split_windows(value):
    pairs = []
    for chunk in str(value).split(","):
        chunk = chunk.strip()
        if not chunk:
            continue
        parts = chunk.split("-")
        if len(parts) != 2:
            raise ValueError("window_shape_unexpected:%s" % chunk)
        pairs.append([parts[0].strip(), parts[1].strip()])
    return pairs


# index_scale_thresholds 的键是档位（"-1"/"-2"/"deep"），值是缩放。
# 载体把它拆成两张表：bands（档位边界）与 scales（档位缩放）。
# 档位边界**从 Go 那张表的键名里读**，不是在脚本里写 -1.0/-2.0——键名本身就是真源。
#
# 方向不许搞反：Go 的 "-1" 是"跌 1%"（较浅），"-2" 是"跌 2%"（较深）。
# sorted() 是升序，所以最浅档在**末尾**。第一版按升序取 [0] 当 shallow，
# 得到的是 -2.0/0.70——把"跌 1% 不降权"静默改成了"跌 1% 就降到 0.70"，
# 而两张表看起来都"有 shallow 有 deep"，肉眼检不出来。这里按降序取并加自证。
def _index_bands(thresholds):
    numeric = sorted((float(k) for k in thresholds if _is_number(k)), reverse=True)
    if len(numeric) != 2:
        raise ValueError("index_bands_expect_two_numeric_keys_got=%d" % len(numeric))
    if numeric[0] <= numeric[1]:
        raise ValueError("index_bands_order_broken")
    return {"shallow": numeric[0], "deep": numeric[1]}


def _index_scales(thresholds):
    numeric_keys = sorted((k for k in thresholds if _is_number(k)),
                          key=lambda k: float(k), reverse=True)
    if len(numeric_keys) != 2:
        raise ValueError("index_scales_expect_two_numeric_keys")
    if "deep" not in thresholds:
        raise ValueError("index_scales_missing_deep_key")
    return {"shallow": thresholds[numeric_keys[0]],
            "mid": thresholds[numeric_keys[1]],
            "deep": thresholds["deep"]}


def _is_number(text):
    try:
        float(text)
        return True
    except (TypeError, ValueError):
        return False


# 情绪相位缩放：Go 的 EmotionScales 用**中文名**做键（"退潮"），载体用机读码（retreat）。
# 码↔名的对照表只有一份，就是载体自己的 emotion_phase_labels——这里只做重命名，
# 不新造任何一个数。表里出现 Go 没有的名字（或反过来）一律判红，不静默丢一档：
# 丢一档等于那一相的降权从此不参与合成，而合成结果只会更激进。
def _emotion_scales(go_scales, labels):
    if not isinstance(labels, dict) or not labels:
        raise ValueError("emotion_phase_labels_absent_cannot_rekey")
    name_to_code = {}
    for code, name in labels.items():
        if code == "ice":
            # 冰点在 Go 里是硬拦（不进缩放表），载体表里也刻意没有它——一致才放行。
            continue
        name_to_code[name] = code
    missing = sorted(set(go_scales) - set(name_to_code))
    if missing:
        raise ValueError("emotion_phase_label_missing_for_go_names=%s" % ",".join(missing))
    out = {}
    for name, value in go_scales.items():
        out[name_to_code[name]] = value
    extra = sorted(set(name_to_code) - set(go_scales))
    if extra:
        raise ValueError("emotion_label_without_go_scale=%s" % ",".join(extra))
    return out


# T 的方向：Go 给两个布尔（TBuyFirst/TSellFirst），载体要一个 mode 串。
# 名字表在这里，判定不在这里——两个布尔各自原样搬，串起来的方式与 Go 的
# defaultTMode（空串＝both）一致。
def _t_mode(derived):
    buy = bool(derived.get("t_buy_first"))
    sell = bool(derived.get("t_sell_first"))
    if buy and sell:
        return "both"
    if buy:
        return "buy_first"
    if sell:
        return "sell_first"
    return "none"


# (go_key, carrier_keys, transform) —— transform 收 (value, 全量派生表)。
MAPPING = [
    ("sizing_enabled", ("sizing_enabled",), None),
    ("sizing_shadow", ("sizing_shadow",), None),
    ("budget_mode", ("budget_mode",), None),
    ("sizing_mode", ("sizing_mode",), None),
    ("power_p", ("power_p",), None),
    ("group_floor_market", ("group_floor_market",), None),
    ("group_floor_stock", ("group_floor_stock",), None),
    ("scale_floor", ("scale_floor",), None),
    ("scale_cap", ("scale_cap",), None),
    ("emotion_scales", ("emotion_scales",), lambda v, all_, cur, cur_tables: _emotion_scales(v, cur_tables.get("emotion_phase_labels"))),
    ("state_scales", ("state_scales",), None),
    ("tier_scales", ("tier_scales",), None),
    ("tier_red_default", ("tier_red_default",), None),
    ("breadth_missing_scale", ("breadth_missing_scale",), None),
    ("macro_window_scales", ("macro_window_scales",), None),
    ("macro_lead_days", ("macro_lead_days",), None),
    ("macro_lead_curve", ("macro_lead_curve",), None),
    ("macro_event_day_scale", ("macro_event_day_scale",), None),
    ("holiday_scales", ("holiday_scales",), None),
    ("holiday_block_new", ("holiday_block_new",), None),
    ("llm_bearish_scale", ("llm_bearish_scale",), None),
    ("kelly_lambda", ("kelly_lambda",), None),
    ("kelly_min_prob", ("kelly_min_prob",), None),
    ("vol_target_pct", ("vol_target_pct",), None),
    ("vol_clamp", ("vol_clamp",), None),
    ("max_total_position_pct", ("max_total_position_pct",), None),
    ("per_trade_position_pct", ("per_trade_position_pct",), None),
    ("per_trade_position_max_pct", ("per_trade_position_max_pct",), None),
    ("max_drawdown_pct", ("max_drawdown_pct",), None),
    ("stoploss_guard_count", ("stoploss_guard_count",), None),
    ("sl_offset_risk_budget_keep", ("sl_offset_risk_budget_keep",), None),
    ("cluster_cap_pct", ("cluster_cap_pct",), None),
    ("cluster_rho", ("cluster_rho",), None),
    ("sell_boost_level", ("sell_boost_level",), None),
    ("missing_key_target_cap", ("missing_key_target_cap",), None),
    ("daily_buy_cap", ("daily_buy_cap",), None),
    ("min_order_amount", ("min_order_amount",), None),
    ("qmt_no_cancel_windows", ("no_cancel_windows",), lambda v, all_, cur, cur_tables: _split_windows(v)),
    ("qmt_no_new_order_windows", ("no_new_order_windows",), lambda v, all_, cur, cur_tables: _split_windows(v)),
    ("index_scale_thresholds", ("index_bands", "index_scales"),
     lambda v, all_, cur, cur_tables: (_index_bands(v), _index_scales(v))),
    ("flow_factor_enabled", ("flow_factor_enabled",), None),
    ("flow_5d_scales", ("flow_5d_scales",), None),
    ("flow_bands", ("flow_bands",), None),
    ("flow_today_scales", ("flow_today_scales",), None),
    # -- 情绪相位阈值 -------------------------------------------------------
    ("emo_ice_limitup_max", ("emo_ice_limitup_max",), None),
    ("emo_ice_board_max", ("emo_ice_board_max",), None),
    ("emo_start_limitup_min", ("emo_start_limitup_min",), None),
    ("emo_start_limitup_max", ("emo_start_limitup_max",), None),
    ("emo_start_board_max", ("emo_start_board_max",), None),
    ("emo_ferment_limitup_min", ("emo_ferment_limitup_min",), None),
    ("emo_ferment_limitup_max", ("emo_ferment_limitup_max",), None),
    ("emo_ferment_board_max", ("emo_ferment_board_max",), None),
    ("emo_climax_limitup_min", ("emo_climax_limitup_min",), None),
    ("emo_climax_board_min", ("emo_climax_board_min",), None),
    ("emo_retreat_limitup_max", ("emo_retreat_limitup_max",), None),
    ("emo_retreat_board_max", ("emo_retreat_board_max",), None),
    ("emo_diverge_limitup_drop", ("emo_diverge_limitup_drop",), None),
    ("emo_diverge_board_drop", ("emo_diverge_board_drop",), None),
    # -- 做 T ---------------------------------------------------------------
    ("t_enabled", ("t_enabled",), None),
    ("t_shadow", ("t_shadow",), None),
    ("t_intraday_cap_pct", ("t_intraday_cap_pct",), None),
    ("t_min_spread_pct", ("t_min_spread_pct",), None),
    ("t_max_rounds", ("t_max_rounds",), None),
    ("t_force_unwind_at", ("t_force_unwind_at",), None),
    ("t_last_open_at", ("t_last_open_at",), None),
    ("t_vwap_dev_pct", ("t_vwap_dev_pct",), None),
    ("t_min_amplitude_pct", ("t_min_amplitude_pct",), None),
    ("t_min_market_scale", ("t_min_market_scale",), None),
    ("t_sell_env_min_market_scale", ("t_sell_env_min_market_scale",), None),
    ("t_intraday_sell_cap_total_pct", ("t_intraday_sell_cap_total_pct",), None),
    ("t_market_plunge_pct", ("t_market_plunge_pct",), None),
    ("t_near_extreme_pct", ("t_near_extreme_pct",), None),
    ("t_limit_distance_pct", ("t_limit_distance_pct",), None),
    ("t_open_cooling_min", ("t_open_cooling_min",), None),
    ("t_stop_guard_pct", ("t_stop_guard_pct",), None),
    ("t_round_stop_pct", ("t_round_stop_pct",), None),
    ("t_vol_ratio_max", ("t_vol_ratio_max",), None),
    ("t_regime_gate_enabled", ("t_regime_gate_enabled",), None),
    ("t_min_amount_wan", ("t_min_amount_wan",), None),
    ("t_min_turnover_pct", ("t_min_turnover_pct",), None),
    ("t_min_listed_days", ("t_min_listed_days",), None),
    ("t_vwap_check_tol_pct", ("t_vwap_check_tol_pct",), None),
    ("t_commission_rate", ("t_commission_rate",), None),
    ("t_min_commission", ("t_min_commission",), None),
    ("t_stamp_tax_rate", ("t_stamp_tax_rate",), None),
    ("t_slippage_bps", ("t_slippage_bps",), None),
    # -- 分板块涨跌停与 T 时序（真源在 data / engine 的只读导出） ----------
    ("t_limit_pct_main", ("t_limit_pct_main",), None),
    ("t_limit_pct_wide", ("t_limit_pct_wide",), None),
    ("t_limit_pct_star", ("t_limit_pct_star",), None),
    ("t_limit_pct_bse", ("t_limit_pct_bse",), None),
    ("t_limit_pct_st", ("t_limit_pct_st",), None),
    ("t_ghost_grace_sec", ("t_ghost_grace_sec",), None),
    ("t_premise_grace_sec", ("t_premise_grace_sec",), None),
    ("t_regime_stale_sec", ("t_regime_stale_sec",), None),
    ("t_min_base_keep_qty", ("t_min_base_keep_qty",), None),
    ("t_regime_min_votes", ("t_regime_min_votes",), None),
    # 日内已实现亏损熔断：Go 出厂 0＝关（RiskGateConfig.DayLossLimitPct）。
    # 搬 0 不是"把门拆了"——Go 的语义本来就是出厂不开，载体若想要一条自己的线，
    # 那是一次**显式配置**，不是默认值。这里照实搬，别把载体的猜测写成 Go 的默认。
    ("risk_day_loss_limit_pct", ("t_day_loss_limit_pct",), None),
    # 方向两个布尔 → 一个 mode 串（值不新造，串由 t_buy_first/t_sell_first 组）。
    ("__t_mode__", ("t_mode",), lambda _v, all_, cur, cur_tables: _t_mode(all_)),
]

# ---------------------------------------------------------------------------
# 载体自有键（Go 没有真源，登记理由；不登记＝孤儿，判红）
#
# 三类理由：
#   carrier-shape  只有载体需要的形态（短码表、轮询节拍、手数、时区窗口）
#   gateway-python 真源在网关的 Python 侧（柜台状态词表），构建器已经按单点烘入
#   needs-go-naming Go 侧是**内联字面量**、没有导出读数；要从这里单点化，
#                     得先在 Go 把它命名成常量——那是产品代码改动，等 owner 点头。
# ---------------------------------------------------------------------------
CARRIER_OWNED = {
    "signal_codes": "carrier-shape: 委托备注短码表只属于载体（幂等键的构造规则）",
    "t_signal_codes": "carrier-shape: 同上，T 腿各自的短码",
    "symbol_head_len": "carrier-shape: 备注里代码前缀长度",
    "t_remark_code_max_len": "carrier-shape: 柜台备注长度上限的载体侧约束",
    "terminal_status_codes": "gateway-python: 真源 qmt_gateway/store.py TERMINAL_STATUSES",
    "t_terminal_status_codes": "gateway-python: 同上，构建器 counter_status_vocab 烘入",
    "t_cancelled_status_tokens": "gateway-python: 真源 qmt_gateway/handler.py XT_STATUS_CODES",
    "lot_size": "needs-go-naming: A 股一手=100 在 Go 是内联字面量，无导出读数",
    "t_stale_order_sec": "needs-go-naming: 撤单未决时限在 Go 无同名导出",
    "t_session_windows": "carrier-shape: 交易时段由 data.IsActiveSession 判定，无导出时段表",
    "t_session_open_hhmm": "needs-go-naming: 早盘 09:30 在 Go 是内联分钟数",
    "t_no_new_order_windows": "carrier-shape: 与 no_new_order_windows 同源，T 侧别名",
    "t_no_cancel_windows": "carrier-shape: 与 no_cancel_windows 同源，T 侧别名",
    "t_wide_board_prefixes": "carrier-shape: 板块前缀表只在载体侧使用",
    "t_bse_board_prefixes": "carrier-shape: 同上",
    "t_st_markers": "carrier-shape: ST 名称标记",
    "t_anchor_ok_sources": "carrier-shape: 锚点来源白名单（载体侧记账）",
    # t_min_base_keep_qty 的旧写法；两条都指向同一个 Go 常量（engine.tLMinBaseKeep）。
    # 注意 Go 侧那份字段名带 "反 T" 的语义限定，载体侧两条键共用一个数是否成立由 owner 裁。
    "t_cost_safety_multiplier": "needs-go-naming: 成本安全倍数在 Go 无导出读数",
    "t_missing_key_input": "carrier-shape: 这是本轮读数不是配置，运行时由 ctx 提供",
    "emotion_enabled": "needs-go-naming: 维度参与开关在 rules.runtime/enhance，非出厂常量",
    "market_state_enabled": "needs-go-naming: 同上",
    "emotion_phase_labels": "carrier-shape: 相位名与 Go 的 emotionPhases 由 --check 交叉验证",
    "quality_weight_clamp": "needs-go-naming: 质量因子限幅在 signalctl 内联",
    "kelly_no_edge": "needs-go-naming: 无优势固定缩放 0.6 在 signalctl 内联",
    "kelly_edge_gain": "needs-go-naming: 同上",
    "kelly_clamp": "needs-go-naming: 同上",
    "per_trade_default_lo": "needs-go-naming: signalctl.PerTradePctOf 的兜底 0.10 是内联字面量",
    "per_trade_default_hi": "needs-go-naming: 同上 0.30",
    "per_trade_hard_max": "needs-go-naming: 同上 0.50 硬边界",
    "breadth_bands": "needs-go-naming: 广度分档阈值在 signalctl 内联",
    "breadth_scales": "needs-go-naming: 广度缩放档同上",
    "breadth_missing_default": "carrier-shape: 与 breadth_missing_scale 同值，载体侧兜底别名",
    "holiday_long_closed_run": "needs-go-naming: 假期分类天数在 data/holiday 内联",
    "holiday_small_closed_run": "needs-go-naming: 同上",
    "holiday_weekend_closed_run": "needs-go-naming: 同上",
    "holiday_long_pre_max": "needs-go-naming: 同上",
    "holiday_small_pre_max": "needs-go-naming: 同上",
    "holiday_weekend_pre_max": "needs-go-naming: 同上",
    "red_block_new_buy": "needs-go-naming: 风险档整笔拒绝开关在 enhance，非出厂常量",
    "tier_red_default_alias": "carrier-shape: 未使用键（保留登记以防误报）",
    "t_direction_mode": "carrier-shape: 与 t_mode 同值别名（Go 侧是 TBuyFirst/TSellFirst 两个布尔）",
    "emo_breadth_ice_down_ratio": "needs-go-naming: 广度轴的冰点/退潮修正在 data.applyBreadthCorrection 内联，"
                                  "出厂 0＝该轴不设门（载体显式保留 0，不许从别处推）",
    "emo_breadth_retreat_down_ratio": "needs-go-naming: 同上（退潮轴）",
    "min_cash_reserve": "needs-go-naming: Go 侧是 CashReserveRatio（比例）而非金额，量纲不同不可直搬",
    "order_amount_cap": "needs-go-naming: 载体侧单笔帽；Go 的 RiskGate.MaxOrderAmount 出厂为 0（＝关），"
                        "把 0 烘进来等于拆掉载体的帽，故显式保留载体值",
    "industry_cap_pct": "needs-go-naming: 行业敞口帽在 Go 出厂为 0（＝关），不可当默认搬",
    "single_stock_cap_pct": "needs-go-naming: 同上（SingleStockValuePct 出厂 0）",
    "max_ops_per_min": "carrier-shape: 执行器自身的节流，Go 无对应",
    "ops_window_sec": "carrier-shape: 同上",
    "poll_interval_sec": "carrier-shape: 本地进程轮询节拍",
    "breakdown_ratio": "carrier-shape: 载体形态规则的内部参数",
    "pullback_ratio": "carrier-shape: 同上",
    "ma_window": "carrier-shape: 同上",
    "ma_period": "carrier-shape: 同上",
    "min_price": "carrier-shape: 价格合法性下限（非策略阈值）",
    "min_volume": "carrier-shape: 同上",
    "buy_signals": "carrier-shape: 载体侧参与买法的信号名清单",
    "closed_days": "carrier-shape: 日历兜底表（真源是 Go 产的 trading_calendar.json）",
    "non_trading_weekdays": "carrier-shape: 同上",
    "t_fee_buffer_pct": "needs-go-naming: paper.t_rounds 的费用缓冲未导出",
    "t_min_remaining_position_pct": "needs-go-naming: T 侧剩余底仓下限未导出",
    "t_regime_allow_states": "carrier-shape: 放行档由载体决定",
    "t_regime_block_states": "carrier-shape: 拦截档由载体决定",
}

# ---------------------------------------------------------------------------
# 别名与"模块自带声明表"两类补数
#
# ALIAS：载体里有同一个键的两个名字（T 腿沿用了自己的一套前缀）。
#   别名只做**改名**，绝不重新给值——两个名字永远同源，改一处另一处跟着动。
# SIZING_TABLE：rules_sizing 自己声明的 SIZING_DEFAULTS 里，那些 Go 侧还没有导出读数的键。
#   这张表是模块的一部分（不是脚本里的数），工具把它搬进参数文件，
#   于是 `_sz_p(params, key)` 一定拿得到值，而 --check 又会逐条核对它与 Go 是否冲突。
#   登记理由里带 needs-go-naming 的键，就是在等同主把 Go 侧那个内联常量命名出来。
# ---------------------------------------------------------------------------
ALIAS = {
    "t_direction_mode": "t_mode",
    "t_min_base_keep": "t_min_base_keep_qty",
    "t_no_new_order_windows": "no_new_order_windows",
    "t_no_cancel_windows": "no_cancel_windows",
    "breadth_missing_default": "breadth_missing_scale",
}


def build_universe(paths):
    """把"自选 + 观察信号"的裸码烘成载体要的 symbols 列表。

    为什么走 report_uplink.normalize_code 而不是在这里再写一份首位数字判断：
    代码→市场后缀这条规则在仓库里已经是**一处定义**（SH/SZ/BJ 三段 + 裸码/前缀两种写法），
    复制它就是第四本账——首位判错一位，载体观察的就是别人家的票。
    认不出来的码一律丢掉并计数（不猜市场），丢掉多少必须可见。
    """
    sys.path.insert(0, QMT_GATEWAY)
    import report_uplink  # noqa: E402  (single source of the code -> symbol rule)

    codes, seen, dropped = [], set(), []
    for path in paths:
        with open(path, "r", encoding="utf-8") as fh:
            doc = json.load(fh)
        rows = doc if isinstance(doc, list) else (
            doc.get("codes") or doc.get("symbols") or doc.get("watchlist") or [])
        for raw in rows:
            symbol = report_uplink.normalize_code(str(raw if not isinstance(raw, dict)
                                                       else (raw.get("code") or raw.get("symbol") or "")))
            if not symbol:
                dropped.append(str(raw)[:16])
                continue
            if symbol not in seen:
                seen.add(symbol)
                codes.append(symbol)
    return sorted(codes), dropped


def emit_universe(paths, write):
    codes, dropped = build_universe(paths)
    if not codes:
        print("UNIVERSE_RESULT=FAIL reason=no_usable_codes dropped=%d" % len(dropped))
        return 1
    doc, params = load_params_file()
    print("UNIVERSE requested=%d symbols=%d dropped=%d write=%d"
          % (len(codes) + len(dropped), len(codes), len(dropped), 1 if write else 0))
    print("UNIVERSE_CODES=%s" % ",".join(codes))
    if dropped:
        print("UNIVERSE_DROPPED=%s" % ",".join(dropped[:10]))
    if not write:
        print("UNIVERSE_RESULT=PREVIEW reason=pass_--write_to_land_it"
              "（把真实自选池烘进 params 是**你的**决定：这份文件在仓库里，"
              "写进去就等于把你的观察清单交进 git 历史）")
        return 0
    params["symbols"] = codes
    doc["_comment_universe"] = ("params.symbols is the carrier's observation universe. "
                                "Landed by scripts/derive_carrier_params.py --universe-from --write "
                                "from the operator's watchlist/signal files; the repo default is two "
                                "placeholder codes. The downlink package channel that would refresh "
                                "this daily is not built, so re-run this command after any watchlist "
                                "change you want the box to see.")
    emit_ascii(doc)
    print("UNIVERSE_RESULT=OK symbols=%d" % len(codes))
    return 0


class DeriveError(Exception):
    pass


def run_go_tool(timeout=180):
    """跑 Go 出口拿派生表。任何失败都抛，绝不返回半张表。

    stdout 必须是纯 JSON：internal/data 的包初始化会往 stderr 打日志，
    所以这里只看 stdout，并把 stderr 留作失败时的诊断原文。
    """
    cmd = ["go", "run", "./cmd/derive_carrier_params", "--provenance"]
    try:
        proc = subprocess.run(cmd, cwd=REPO_ROOT, capture_output=True, timeout=timeout)
    except FileNotFoundError:
        raise DeriveError("go_binary_absent")
    except subprocess.TimeoutExpired:
        raise DeriveError("go_run_timeout")
    if proc.returncode != 0:
        raise DeriveError("go_run_exit=%d stderr=%s" % (proc.returncode,
                                                        _tail(proc.stderr.decode("utf-8", "replace"))))
    try:
        data = json.loads(proc.stdout.decode("utf-8"))
    except ValueError as exc:
        raise DeriveError("go_output_not_json:%s stderr=%s" % (exc, _tail(proc.stderr.decode("utf-8", "replace"))))
    params = data.get("params")
    if not isinstance(params, dict) or not params:
        raise DeriveError("go_output_empty_params")
    return params, data.get("provenance") or {}


def _tail(text, limit=200):
    flat = " ".join(text.split())
    return flat[-limit:] if flat else "none"


def build_carrier_values(derived, provenance, current_params, sizing_defaults):
    """按映射表把 Go 派生表落成 {载体键: (值, 真源)}。

    重复落同一个载体键 = 两处供给同一枚数，直接判红（与 Go 侧 put() 同取向）。
    """
    out = {}
    for go_key, targets, transform in MAPPING:
        if go_key == "__t_mode__":
            value = transform(None, derived, current_params, sizing_defaults)
            source = "internal/config/config.go:TConfig.TBuyFirst/TSellFirst"
        else:
            if go_key not in derived:
                raise DeriveError("go_key_absent_in_output:%s" % go_key)
            raw = derived[go_key]
            if transform is None:
                value, source = raw, provenance.get(go_key, "unknown")
            else:
                got = transform(raw, derived, current_params, sizing_defaults)
                source = provenance.get(go_key, "unknown")
                if len(targets) == 1 and isinstance(got, tuple):
                    raise DeriveError("transform_shape_mismatch:%s" % go_key)
                if isinstance(got, tuple):
                    if len(got) != len(targets):
                        raise DeriveError("transform_arity_mismatch:%s" % go_key)
                    for key, val in zip(targets, got):
                        _assign(out, key, val, source)
                    continue
                value = got
        if len(targets) != 1:
            raise DeriveError("multi_target_without_tuple:%s" % go_key)
        _assign(out, targets[0], value, source)
    return out


def _assign(out, key, value, source):
    if key in out:
        raise DeriveError("carrier_key_double_sourced:%s" % key)
    out[key] = (value, source)


def load_params_file():
    with open(PARAMS_PATH, "r", encoding="utf-8") as fh:
        doc = json.load(fh)
    params = doc.get("params")
    if not isinstance(params, dict):
        raise DeriveError("params_file_has_no_params_object")
    return doc, params


def _norm(value):
    """比较用的归一化：JSON 里 1 与 1.0 是同一枚数，list 与 tuple 同理。

    不归一化会把"字节不同但数值相同"报成漂移，那种红一天就没人再看这张表了。
    """
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return round(float(value), 10)
    if isinstance(value, (list, tuple)):
        return [_norm(v) for v in value]
    if isinstance(value, dict):
        return {k: _norm(v) for k, v in sorted(value.items())}
    return value


def carrier_rule_keys():
    """从两个规则模块取它们**声明要读**的键集合（不是脚本自己列的清单）。

    这一步是整个对账的地基：清单由模块自己报，模块加了新键而工具没跟上，
    --check 会当场列出孤儿，而不是运行时才 raise。
    """
    sys.path.insert(0, QMT_GATEWAY)
    import rules_sizing  # noqa: E402
    import rules_t  # noqa: E402
    sizing_keys = set(rules_sizing.SIZING_PARAM_KEYS)
    t_keys = set(rules_t.T_PARAM_KEYS)
    return sizing_keys, t_keys, rules_sizing.SIZING_DEFAULTS


def check_phase_labels(derived, sizing_defaults):
    """相位标签表与 Go 的相位名单交叉验证（这是 emotion_phase_labels 登记理由里承诺的那一步）。

    Go 有六个相位名，载体有六条 code→名。少了、多了、拼错都只报问题、绝不猜一个补上：
    猜出来的标签会让某一相的降权在无人察觉的情况下永远不生效。
    """
    problems = []
    names = derived.get("emotion_phases") or []
    labels = sizing_defaults.get("emotion_phase_labels") or {}
    if not names:
        return ["emotion_phases_absent_from_go_output"]
    if not labels:
        return ["emotion_phase_labels_absent_from_params"]
    label_names = sorted(labels.values())
    if label_names != sorted(names):
        problems.append("phase_names_differ go=%s table=%s"
                        % (json.dumps(names, ensure_ascii=True),
                           json.dumps(label_names, ensure_ascii=True)))
    return problems


def coverage(carrier_values, required_keys, current_params):
    """把规则模块声明的每个键分成四类。

    derived   由 Go 供给
    owned     登记过的载体自有键
    existing  参数文件里已有、但既未派生也未登记 → 必须补登记（不是删）
    orphan    规则要求、但既派生不到也没登记 → 判红
    """
    derived = set(k for k in carrier_values if k in required_keys)
    owned = set(k for k in CARRIER_OWNED if k in required_keys)
    orphan = sorted(required_keys - derived - owned)
    unused_derived = sorted(set(carrier_values) - required_keys)
    return {
        "required": sorted(required_keys),
        "derived": sorted(derived),
        "owned": sorted(owned),
        "orphan": orphan,
        "unused_derived": unused_derived,
        "missing_now": sorted(required_keys - set(current_params) - set(orphan)),
    }


def defaults_table_drift(carrier_values, sizing_defaults):
    """rules_sizing.SIZING_DEFAULTS 是"参数文件缺键时"的兜底表。

    它和 Go 出厂值不一致时，缺键行为会静默偏离出厂档——所以这张表必须逐键核对。
    只比两边都有的键；载体独有（如相位标签）不在这里判。
    """
    drift = []
    for key, table_value in sizing_defaults.items():
        if key not in carrier_values:
            continue
        go_value = carrier_values[key][0]
        if _norm(table_value) != _norm(go_value):
            drift.append((key, table_value, go_value))
    return sorted(drift)


def resolve_extras(carrier_values, params, sizing_defaults):
    """把别名与模块自带声明表并进待写表，返回新的值表。

    优先级是刻意的：Go 派生值 > 别名 > 模块声明表。三者冲突时取更靠前的那一个，
    并把冲突原样打出来——**不静默覆盖**，因为覆盖意味着"参数文件里的数和它的
    真源不一致"，而那正是这把尺子要消灭的东西。
    """
    merged = dict(carrier_values)
    for key, src_key in sorted(ALIAS.items()):
        if key in merged:
            continue
        base = merged.get(src_key)
        if base is not None:
            # base 已经是 (值, 真源) 一对——取**值**那半边。曾经这里把整个元组当值写进
            # merged，参数文件里就会出现 ["both","internal/config/..."] 这种形状；
            # 构建器照抄进产物，规则模块拿到的是列表而不是字符串。
            value, source = base[0], "alias:%s(%s)" % (src_key, base[1])
        elif src_key in params:
            value, source = params[src_key], "alias:%s(params)" % src_key
        else:
            continue
        merged[key] = (value, source)

    for key, value in sorted(sizing_defaults.items()):
        if key in merged:
            continue
        if key in params:
            if _norm(params[key]) == _norm(value):
                continue
            # 参数文件里已有、且与模块声明表不一致：以已有值为准并出声（不悄悄改钱闸）。
            print("DERIVE_TABLE_CONFLICT key=%s file=%s table=%s kept=file"
                  % (key, json.dumps(params[key], ensure_ascii=True),
                     json.dumps(value, ensure_ascii=True)))
            continue
        merged[key] = (value, "qmt_gateway/rules_sizing.py:SIZING_DEFAULTS")

    return merged


def apply_write(doc, params, carrier_values):
    added, changed, unchanged = [], [], []
    for key, (value, _src) in sorted(carrier_values.items()):
        if key not in params:
            params[key] = value
            added.append(key)
        elif _norm(params[key]) != _norm(value):
            changed.append((key, params[key], value))
            params[key] = value
        else:
            unchanged.append(key)
    doc["params"] = params
    doc["_comment_derived"] = (
        "Written by scripts/derive_carrier_params.py --write. Three sources, in priority order: "
        "(1) the Go factory defaults, printed by cmd/derive_carrier_params and listed by "
        "--emit-derived-keys -- never hand-edit those, change the Go default and re-run; "
        "(2) aliases (t_direction_mode, the T-side window copies, breadth_missing_default) which "
        "rename an existing key and must never carry their own number; "
        "(3) qmt_gateway/rules_sizing.py SIZING_DEFAULTS rows for knobs the Go side has not "
        "exported a readout for yet -- each is registered with a reason in the script, and "
        "--check compares every overlapping row against Go. A key in none of the three is "
        "reported as DERIVE_UNHOUSED rather than quietly defaulted."
    )
    return added, changed, unchanged


def emit_ascii(doc):
    """写盘 + 逐字节复验（非 ASCII 一律拒）。"""
    text = json.dumps(doc, ensure_ascii=True, indent=2, sort_keys=False) + "\n"
    try:
        text.encode("ascii")
    except UnicodeEncodeError as exc:
        raise DeriveError("params_would_contain_non_ascii:%s" % exc)
    tmp = PARAMS_PATH + ".tmp-derive"
    with open(tmp, "w", encoding="ascii") as fh:
        fh.write(text)
    os.replace(tmp, PARAMS_PATH)
    with open(PARAMS_PATH, "rb") as fh:
        back = fh.read()
    bad = [i for i, b in enumerate(back) if b > 127]
    if bad:
        raise DeriveError("written_file_has_non_ascii_bytes offset=%d" % bad[0])
    if json.loads(back.decode("ascii"))["params"] != doc["params"]:
        raise DeriveError("readback_params_mismatch")
    return len(back)


def main():
    ap = argparse.ArgumentParser()
    # 缺省是 --report（只读对账）。--universe-from 是另一条腿，它自己决定要不要落盘。
    mode = ap.add_mutually_exclusive_group(required=False)
    mode.add_argument("--report", action="store_true", help="print the reconciliation, write nothing")
    mode.add_argument("--check", action="store_true", help="gate posture: exit 1 on drift or orphans")
    mode.add_argument("--write", action="store_true", help="merge derived values into the params file")
    mode.add_argument("--emit-json", action="store_true", help="print the raw Go-derived table")
    mode.add_argument("--emit-derived-keys", action="store_true",
                      help="print the carrier keys this tool owns (for the build gate)")
    ap.add_argument("--universe-from", action="append", default=[], dest="universe_from",
                    metavar="PATH", help="watchlist/signal JSON to bake into params.symbols "
                                         "(repeatable; preview unless --write-universe is given)")
    ap.add_argument("--write-universe", action="store_true", dest="write_universe",
                    help="actually land params.symbols from --universe-from")
    ns = ap.parse_args()
    if ns.universe_from:
        return emit_universe(ns.universe_from, ns.write_universe)
    chosen = "report"
    for candidate in ("report", "check", "write", "emit_json", "emit_derived_keys"):
        if getattr(ns, candidate):
            chosen = candidate.replace("_", "-")
    ns.mode = chosen

    try:
        derived, provenance = run_go_tool()
    except DeriveError as exc:
        print("DERIVE_RESULT=FAIL reason=%s" % exc)
        return 1

    # 先读规则模块与参数文件：改名/重键用的对照表都在它们身上（唯一的一份）。
    doc, params = load_params_file()
    sizing_keys, t_keys, sizing_defaults = carrier_rule_keys()
    carrier_values = build_carrier_values(derived, provenance, params, sizing_defaults)

    if chosen == "emit-json":
        print(json.dumps(derived, ensure_ascii=True, indent=2, sort_keys=True))
        return 0

    if chosen == "emit-derived-keys":
        for key in sorted(carrier_values):
            print(key)
        return 0

    all_values = resolve_extras(carrier_values, params, sizing_defaults)
    required = sizing_keys | t_keys
    cov = coverage(all_values, required, params)
    drift = defaults_table_drift(carrier_values, sizing_defaults)
    label_problems = check_phase_labels(derived, sizing_defaults)
    for problem in label_problems:
        print("DERIVE_LABELS_DRIFT %s" % problem)

    print("DERIVE_SUMMARY derived_total=%d mapped=%d required=%d by_sizing=%d by_t=%d "
          "owned=%d orphan=%d unused_derived=%d keys_missing_in_file=%d"
          % (len(derived), len(carrier_values), len(required), len(sizing_keys), len(t_keys),
             len(cov["owned"]), len(cov["orphan"]), len(cov["unused_derived"]),
             len(cov["missing_now"])))
    for key in cov["orphan"]:
        print("DERIVE_ORPHAN key=%s" % key)
    for key, table_value, go_value in drift:
        print("DERIVE_DEFAULTS_DRIFT key=%s table=%s go=%s"
              % (key, json.dumps(table_value, ensure_ascii=True),
                 json.dumps(go_value, ensure_ascii=True)))
    if chosen in ("check", "write"):
        print("DERIVE_DEFAULTS_TABLE_CHECKED=%d" % len(drift))

    if chosen == "write":
        added, changed, unchanged = apply_write(doc, params, all_values)
        size = emit_ascii(doc)
        for key in added:
            print("DERIVE_ADDED key=%s source=%s" % (key, all_values[key][1]))
        for key, old, new in changed:
            print("DERIVE_CHANGED key=%s old=%s new=%s source=%s"
                  % (key, json.dumps(old, ensure_ascii=True), json.dumps(new, ensure_ascii=True),
                     all_values[key][1]))
        print("DERIVE_WRITE bytes=%d added=%d changed=%d unchanged=%d path=qmt_gateway/local_executor_params.json"
              % (size, len(added), len(changed), len(unchanged)))
    else:
        missing = []
        diff = []
        for key in sorted(all_values):
            if key not in params:
                missing.append(key)
            elif _norm(params[key]) != _norm(all_values[key][0]):
                diff.append(key)
                print("DERIVE_CHANGED key=%s old=%s new=%s source=%s"
                      % (key, json.dumps(params[key], ensure_ascii=True),
                         json.dumps(all_values[key][0], ensure_ascii=True),
                         all_values[key][1]))
        print("DERIVE_NOT_IN_FILE=%d" % len(missing))
        # 单条锚点而不是逐行：门禁要判的是"有没有不一致"，逐行打印在 check 模式会被截断，
        # 截断了就变成"绿是因为没看到红"——那正是本仓锤过的形状。
        print("DERIVE_CHANGED_TOTAL=%d" % len(diff))
        # 规则模块要求、但既没有 Go 读数也没有别名/声明表可搬的键：只能显式承认没有。
        still = sorted(k for k in required if k not in params and k not in all_values)
        print("DERIVE_UNHOUSED=%d %s" % (len(still), ",".join(still) if still else "none"))

    bad = bool(cov["orphan"]) or bool(drift) or bool(label_problems)
    if chosen == "check":
        print("DERIVE_RESULT=%s" % ("FAIL" if bad else "OK"))
        return 1 if bad else 0
    print("DERIVE_RESULT=%s" % ("DRIFT" if bad else "OK"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
