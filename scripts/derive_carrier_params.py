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
    # daily_buy_cap 不进 MAPPING（§CAP-OVERRIDE-ASCII 2026-11-09）：它的真源是
    # live_rules 快照的账号级 daily_budget_amount（owner 2026-11-09 15:15 裁决把
    # 载体帽对齐现网账号值，提交 fa6939b；语义是日买入累计额度的资金流水闸）。
    # 若留在这里，--write 会把文件里已对齐的现网值重写成 Go 出厂 100000——
    # 等于工具替 owner 反悔。对账由 scripts/sync_live_rules.py --check/--write
    # 硬判（reason=daily_buy_cap_mismatch），本脚本不再与 Go 出厂比这枚键。
    # Go 导出读数刻意保留（cmd/derive_carrier_params collectQMT 未动），只是不进映射。
    ("min_order_amount", ("min_order_amount",), None),
    # §CARRIER-PARITY-1105：载体三行为的出厂值，同名直搬（Go 侧真身见 main.go collectQMT）。
    ("signal_gone_cancel_sec", ("signal_gone_cancel_sec",), None),
    ("vwap_reprice_pct", ("vwap_reprice_pct",), None),
    ("buy_room_guard", ("buy_room_guard",), None),
    ("reprice_max_per_day", ("reprice_max_per_day",), None),
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
# 做空四战法与派生窗口（§DERIVE-CARRIER-1105）——**同名直搬**的那一批
#
# 为什么单独成段而不逐条写进上面那张表：这一批 102 枚键是"Go 键名 == 载体键名"的
# 恒等映射（rules_form.py 的 FORM_PARAM_KEYS 逐条写着真源在哪个 Go 文件的哪一行，
# 值现在也真的从那里出来）。它们**只做改名（这里连改名都没有），绝不做算术**：
# 形态转换的规矩和上面那条一致——工具不许产生任何一个新数字。
#
# 为什么必须有真源清单而不是"文件里有什么就搬什么"：这个表列出的每一枚都必须能在
# Go 的导出读数里找到，找不到就整批判红（go_key_absent_in_output）——
# 以前正是"没人知道哪些键该有主"让 102 枚阈值在参数文件里凭空消失，
# 产物里每条移植规则一被调用就 raise KeyError。
#
# 真源（全部由 go run ./cmd/derive_carrier_params --provenance 点名）：
#   internal/strategy/types.go            rule_side 与四枚 *_kind（已是命名常量，只转调）
#   internal/combat_agent/adapter.go      派生窗口族（ma/vol/high60/low20/gain/last3/容忍/在窗天数）
#   internal/strategies/break_down        bd_*
#   internal/strategies/high_churn        hc_*
#   internal/strategies/leader_decay      ld_*
#   internal/strategies/good_news_fade    gf_*
# ---------------------------------------------------------------------------
BEAR_TACTIC_KEYS = (
    # --- 方向与信号种类（载体的 RULES 注册表按这三枚对表） ---
    "rule_side",
    "break_down_kind", "high_churn_kind", "leader_decay_kind", "good_news_fade_kind",
    # --- buildShortData 派生窗口（含今日 vs 不含今日，两处口径不同） ---
    "ma5_window", "ma10_window", "ma20_window",
    "vol5_window", "vol20_window",
    "hc_high_window",                     # 60 日高：含今日
    "bd_low_window", "hc_gain_window", "hc_last3_window",   # 这三族不含今日，导的是根数
    "ld_seal_tolerance", "ld_touch_tolerance",
    "gf_event_window_days",
    # --- break_down ---
    "bd_min_bars", "bd_depth_max_score", "bd_depth_single_cap", "bd_depth_full_pct",
    "bd_volume_max_score", "bd_volume_full_ratio", "bd_volume_half_ratio",
    "bd_volume_shrink_factor", "bd_intraday_max_score", "bd_intraday_severe_pct",
    "bd_intraday_mid_pct", "bd_intraday_mid_base_score",
    "bd_intraday_severe_span_pct", "bd_intraday_severe_span_score",
    "bd_intraday_flat_span_pct", "bd_intraday_flat_span_score",
    "bd_shadow_add_ratio", "bd_shadow_add_score",
    "bd_shakeout_depth_pct", "bd_shakeout_discount", "bd_score_threshold",
    # --- high_churn ---
    "hc_min_bars", "hc_pos_start", "hc_pos_span", "hc_pos_base_score", "hc_pos_step_score",
    "hc_position_max_score", "hc_gain_start", "hc_gain_span", "hc_gain_base_score",
    "hc_gain_step_score", "hc_div_heavy_ratio", "hc_div_stall_pct", "hc_div_full_score",
    "hc_div_half_score", "hc_shadow_ratio", "hc_shadow_score", "hc_below_ma5_score",
    "hc_consec_down_days", "hc_consec_down_score", "hc_weakness_max_score",
    "hc_score_threshold",
    # --- leader_decay ---
    "ld_min_bars", "ld_min_boards", "ld_board_max_score", "ld_board_full_boards",
    "ld_board_base_score", "ld_board_base_boards", "ld_board_span_boards",
    "ld_board_span_score", "ld_big_loss_ratio", "ld_big_loss_score", "ld_touched_score",
    "ld_huge_volume_ratio", "ld_huge_volume_score", "ld_weakness_max_score",
    "ld_sector_drop_ratio", "ld_sector_drop_score", "ld_phase_score",
    "ld_retreat_phase_keys", "ld_retreat_max_score", "ld_score_threshold",
    # --- good_news_fade ---
    "gf_min_bars", "gf_event_fresh_age", "gf_event_stale_age", "gf_event_max_score",
    "gf_event_min_score", "gf_event_age_span", "gf_event_age_drop",
    "gf_gain_start", "gf_gain_full", "gf_gain_span", "gf_gain_max_score",
    "gf_stall_pct", "gf_stall_score", "gf_heavy_ratio", "gf_heavy_score",
    "gf_below_ma5_score", "gf_shadow_ratio", "gf_shadow_score", "gf_weakness_max_score",
    "gf_surge_window", "gf_surge_step_pct", "gf_propagation_discount", "gf_score_threshold",
)

# 恒等映射：Go 键 == 载体键。单独 append 而不是复制粘贴 102 行，是为了让
# "漏了一枚"这件事在 --check 里以 orphan 的形式点名，而不是靠人对着表数。
MAPPING.extend([(key, (key,), None) for key in BEAR_TACTIC_KEYS])

# ---------------------------------------------------------------------------
# 做多四腿（rules_ladder）与因子/形态腿（rules_factor）的旋钮（§DERIVE-CARRIER-1106）
#
# rules_ladder 那 115 枚里有三种形状，分开处理：
#   1. **同名直搬**（db_* / dragon_* / dr_* 的全部，加 pct_scale）：Go 键名 == 载体键名。
#   2. **换名字**（ld_* 那一族 14 枚）：同一个 Go 常量在两条移植腿上叫不同名字——
#      rules_form 按 `ld_board_max_score` 读，rules_ladder 按 `ld_board_score_full` 读
#      （一个是断板因子分，一个是断板高度满分档，Go 里都是 ldBoardMaxScore 那一枚）。
#      换名发生在**这张映射表**里，不在 py 模块里再写一份数，也不在 Go 侧加第二枚常量。
#      一枚 Go 键供两个载体键时 transform 必须返回同值元组——两处供给同一枚数正是本工具
#      判红的形状，所以这里给的是"同一个值的两个名字"，不是两个来源。
#   3. **不属于战法常量**的两枚：`pct_scale` 是所有战法共用的分值域刻度（Go 侧新命名的
#      strategy.ScoreScale，dragon/double_bump 的评分腿都转调它），
#      `ladder_buy_amount` 则**没有 Go 主人**（Go 的单笔额由仓位引擎按目标仓位算，
#      没有"每档固定金额"这个量）——它按别名接进载体已有的那枚单笔帽，见 ALIAS。
#
# rules_factor 那 14 枚：8 枚有 Go 主人（评分尺度与绝对区间，全部来自
# internal/strategies/factor 的判据常量），6 枚是**移植侧自己的表**（因子库/规则模板/
# 覆盖表），由模块自己声明的 PARAMS_TEMPLATE 供给，见 FACTOR_TABLE_KEYS。
#
# 真源（全部由 go run ./cmd/derive_carrier_params --provenance 点名）：
#   internal/strategies/double_bump|dragon|dragon_return  的 derive_carrier.go（战法常量 + 出厂策略表）
#   internal/strategy/types.go                            ScoreScale / ScoreMinBars / ActionBuy
#   internal/strategies/factor/factor.go                  评分尺度、兜底档、财务绝对区间
#   internal/data/{trade_time,limitup_pct}.go             时段边界、板块前缀与 ST 字样
#   internal/engine/intraday_t_live.go                    行情门档位、被撤字样、底仓保留比、费用缓冲、开盘钟点
#   internal/store/real_positions.go                      可开 T 的锚点来源清单
#   internal/tcalib/tcalib.go                             成本安全倍数
# ---------------------------------------------------------------------------
LADDER_DIRECT_KEYS = (
    "pct_scale", "ld_vol_window",
    # --- double_bump（16） ---
    "db_min_bars", "db_weak_day_pct", "db_min_change_pct", "db_lookback", "db_first_window",
    "db_break_ratio", "db_first_vol_multiple", "db_second_vol_multiple", "db_volume_weight",
    "db_adjust_max_pct", "db_position_weight", "db_ma_short", "db_ma_mid", "db_ma_weight",
    "db_ma_align_ratio", "db_full_chain_min",
    # --- dragon（19） ---
    "dragon_min_bars", "dragon_seal_tolerance_pct", "dragon_f1_weight", "dragon_f1_base_ratio",
    "dragon_f1_vol_ratio", "dragon_f2_weight", "dragon_f2_full_pct", "dragon_f2_half_pct",
    "dragon_f2_half_ratio", "dragon_f3_weight", "dragon_f3_premium_pct", "dragon_f3_own_gain_pct",
    "dragon_f3_half_ratio", "dragon_rs_window", "dragon_f4_weight", "dragon_f4_full_pct",
    "dragon_f4_half_pct", "dragon_f4_half_ratio", "dragon_full_chain_min",
    # --- dragon_return（64，含 adapter 派生窗口那五枚：Go 侧同名导出） ---
    "dr_min_bars", "dr_ma_short", "dr_ma_mid", "dr_ma_long", "dr_rally_window",
    "dr_first_rise_min", "dr_first_rise_max", "dr_sector_rps_min", "dr_score_dragon",
    "dr_pb_best_lo", "dr_pb_best_hi", "dr_pb_good_hi", "dr_pb_shallow_lo", "dr_pb_deep_hi",
    "dr_pb_score_best", "dr_pb_score_good", "dr_pb_score_shallow", "dr_pb_score_deep",
    "dr_pb_score_other",
    "dr_days_good_lo", "dr_days_good_hi", "dr_days_short_lo", "dr_days_long_hi",
    "dr_days_score_good", "dr_days_score_near", "dr_days_score_other",
    "dr_vr_good", "dr_vr_ok", "dr_vr_mid", "dr_vr_bad",
    "dr_vr_score_good", "dr_vr_score_ok", "dr_vr_score_mid", "dr_vr_score_bad", "dr_vr_score_other",
    "dr_shrink_max", "dr_duck_top_full", "dr_duck_top_half", "dr_neck_support_ratio",
    "dr_duck_neck_full", "dr_duck_neck_low", "dr_slope_prev_ratio", "dr_slope_delta_ratio",
    "dr_slope_min", "dr_duck_slope_bonus", "dr_macd_green_near",
    "dr_beak_vr_lo", "dr_beak_vr_hi", "dr_beak_score_three", "dr_beak_score_two", "dr_beak_score_one",
    "dr_nostril_full", "dr_nostril_half",
    "dr_confirm_vr_lo", "dr_confirm_vr_hi", "dr_confirm_vol_full", "dr_confirm_vr_mid_lo",
    "dr_confirm_vol_mid", "dr_k_hold_ratio", "dr_k_score_hold", "dr_k_score_cross",
    "dr_k_near_lo_ratio", "dr_k_score_near", "dr_level_first",
)

# 因子/形态腿里**有 Go 主人**的那 8 枚（同名直搬；真源是战法包自己的判据常量）。
FACTOR_SCORING_KEYS = (
    "min_bars",
    "entry_side",
    "default_buy_threshold",
    "default_weight",
    "default_direction",
    "percentile_min_samples",
    "score_domain",
    "financial_bands",
)

# 做 T 移植腿里**有 Go 主人**的那 11 枚（判据写法，不是配置项；见上面真源清单）。
T_VOCAB_KEYS = (
    "t_session_windows", "t_session_open_hhmm",
    "t_wide_board_prefixes", "t_bse_board_prefixes", "t_st_markers",
    "t_regime_allow_states", "t_regime_block_states", "t_cancelled_status_tokens",
    "t_min_remaining_position_pct", "t_fee_buffer_pct", "t_cost_safety_multiplier",
    "t_anchor_ok_sources",
)

MAPPING.extend([(key, (key,), None)
                for key in LADDER_DIRECT_KEYS + FACTOR_SCORING_KEYS + T_VOCAB_KEYS])

# ld_* 那一族的**换名**：同一枚 Go 常量在两条移植腿上叫不同名字。
# 元组是 (Go 键, ladder 侧的名字)；Go 键自己那一份已经由上面两段供到（rules_form 读的就是它），
# 这里**只加第二个名字**——一处值、两个键名，不是两处来源。
# 为什么换名在这张表里而不是把 rules_ladder.py 改成 Go 的名字：那份 py 是已经过用例钉住的
# 移植产物，它的键名就是它的契约；工具的存在意义正是"契约与真源之间只许有一张改名表"。
LADDER_RENAMES = (
    ("ld_board_max_score", "ld_board_score_full"),   # 断板高度满档分（leader_decay.go:146）
    ("ld_board_base_score", "ld_board_score_base"),  # 起算分（:148）
    ("ld_board_span_score", "ld_board_score_span"),  # 跨度加分（:148）
    ("ld_big_loss_ratio", "ld_big_face_ratio"),      # 大面线（:155）
    ("ld_big_loss_score", "ld_score_big_face"),      # 大面分（:156）
    ("ld_touched_score", "ld_score_touched"),        # 摸板分（:159）
    ("ld_huge_volume_score", "ld_score_huge_volume"),  # 天量分（:162）
    ("ld_weakness_max_score", "ld_weakness_cap"),    # 弱势封顶（:164）
    ("ld_sector_drop_score", "ld_score_sector_drop"),  # 板块退潮分（:175）
    ("ld_phase_score", "ld_score_phase"),            # 相位分（:179）
    ("ld_retreat_max_score", "ld_retreat_cap"),      # 退潮封顶（:182）
    ("ld_retreat_phase_keys", "ld_retreat_tokens"),  # 退潮字样表（同一个列表，两处读）
    ("ld_score_threshold", "ld_min_score"),          # 出单门槛（:60）
)
MAPPING.extend([(go_key, (ladder_key,), None) for go_key, ladder_key in LADDER_RENAMES])



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
    # 2026-11-09 内置策略实机日志（account='' -> counter_order_table_unreadable）钉死的缺口：
    # 沙箱禁读文件、本构建无 get_account，柜台资金账号唯一来路是烘进 PARAMS。
    # 值与盒子 config.xt.json 同源（config.bridge.example.json 里公开同一值）；
    # 本地执行腿不受它影响（其下单账号走 CLI/env，且 DisabledBroker 缺省拒绝）。
    "account": "carrier-shape: 柜台资金账号 id，沙箱唯一来路是烘进 PARAMS（值同盒子 config.xt.json）",
    "signal_codes": "carrier-shape: 委托备注短码表只属于载体（幂等键的构造规则）",
    "t_signal_codes": "carrier-shape: 同上，T 腿各自的短码",
    "symbol_head_len": "carrier-shape: 备注里代码前缀长度",
    "t_remark_code_max_len": "carrier-shape: 柜台备注长度上限的载体侧约束",
    "terminal_status_codes": "gateway-python: 真源 qmt_gateway/store.py TERMINAL_STATUSES",
    "t_terminal_status_codes": "gateway-python: 同上一条，T 腿那份按状态字回退比较时读它；"
                               "值由 GATEWAY_VOCAB_READOUTS 从 store.py 现读现搬，脚本里没有抄过",
    "lot_size": "needs-go-naming: A 股一手=100 在 Go 是内联字面量，无导出读数",
    # t_stale_order_sec 现在是**别名**（指向载体已有的那枚 stale_order_sec，只做改名、不给新数）。
    "t_stale_order_sec": "carrier-shape: 与 stale_order_sec 同源，T 侧别名（见 ALIAS）",
    "t_no_new_order_windows": "carrier-shape: 与 no_new_order_windows 同源，T 侧别名",
    "t_no_cancel_windows": "carrier-shape: 与 no_cancel_windows 同源，T 侧别名",
    # 11-06 这批里 t_session_open_hhmm / t_session_windows / 板块前缀 / ST 字样 / 锚点来源 /
    # 行情门两档 / 被撤字样 / 底仓保留比 / 费用缓冲 / 成本安全倍数 **全部改由 Go 供给**
    # （判据在 data / engine / store / tcalib 各自的常量上），所以这几枚从这里**删除**——
    # 一条键既登记"载体自有"又被派生，就是两处供给同一枚数，--check 判红。
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
    "zero_amount": "math-constant: 规则层禁裸数字，预算算术的零元由这里给出；不是策略阈值，Go 无对应",
    # §STRATEGY-SWITCH-SYNC（owner 11-06 裁决）：这三枚不是 Go 出厂默认，而是量化交易 tab
    # 的现网开关快照，由 scripts/sync_strategy_switches.py 单点派生进 params；derive 不产出值。
    "enabled_strategies": "snapshot-derived: 量化交易 tab 的战法白名单（auth.json#qmt.strategies），由 sync_strategy_switches.py 派生",
    "factor_rules": "snapshot-derived: 白名单内已审批因子规则（applied_factors.json），由 sync_strategy_switches.py 派生；不再是模块模板",
    "unmapped_strategies": "snapshot-derived: tab 已开但载体未移植的战法（如 n_shape），如实打 unmapped 读数，不许当成已启用",
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
    # §NEWS-GATE（2026-11-12 owner 裁决 R5/R10/R12）：三枚 gz 烘焙链运行态键（不派生值）。
    "news_weight_boost": "carrier-shape: 资讯因子加权系数（rules_factor NEWS_WEIGHT_FACTORS 消费），gz 烘焙链写入",
    "force_sell_boost": "snapshot-derived: 负分 D1 且当日持仓 → 卖出加速桶（carrier_hot_reload_gz 每次重算覆盖）",
    "news_gate": "snapshot-derived: 负分个股名单（分值闸双保险的名单面，carrier_hot_reload_gz 写入）",
}

# ---------------------------------------------------------------------------
# 没有 Go 主人的三枚旋钮的**登记**（§DERIVE-CARRIER-1106 逐条定过主人，不是一笔带过）
#
# 为什么单列一段：CARRIER_OWNED 只说"这枚键不算孤儿"，它不产出任何一个值。
# 而构建闸 g9 判的是"模块声明的每一枚都必须在烘出来的 PARAMS 里"——登记了却没落地，
# 表现就是产物里那条规则第一次调用就 raise KeyError，正是 11-04 那个形状。
# 所以"真的没有主人"这件事必须以**值**的形式承认，而且只能用两种值：
#   ① 从别的模块读来的（下面 GATEWAY_VOCAB_READOUTS：柜台状态词表，真源在网关账本模块）；
#   ② 让它**一律弃权**的中性形状（空表 / false / 零预算）——这不是"猜一个数"，
#      而是"没有主人时这条腿不许动钱"，与 rules_t 自己的 fail-closed 取向同源。
# 三枚的结论逐条写在这里，将来谁给它们找到主人，就把这条删掉、改走 MAPPING：
#   t_signal_codes          —— 委托备注短码表。Go 侧根本没有这张表（T 腿的幂等键是
#                              `t0:<码>:<向>:<日>:<序>` 那种长键，不是两字母短码），
#                              短码是**载体的 idempotency 构造规则**，主人是 owner 的装填动作。
#                              落空表＝T 腿在载体里不出单（make_signal 无码即 None），
#                              比替 owner 编五个码诚实。
#   t_remark_code_max_len   —— 柜台备注长度预算。这是**券商柜台的属性**，仓库里
#                              Go/网关/文档都没有记过它（只有用例里随手写的 8，那不是读数）。
#                              落 0＝任何码都超预算＝t_short_code_check 直接拒，
#                              这条腿在 owner 填上柜台真值之前不许发单。
#   t_missing_key_input     —— "本轮关键读数缺失"。它在 Go 里是**每轮的 ctx 读数**
#                              （paper.TParams.MissingKeyInput），不是出厂配置；
#                              落 false＝烘参那一刻没有缺失的证据，运行期由 ctx 覆盖。
# ---------------------------------------------------------------------------
CARRIER_UNOWNED = {
    "t_signal_codes": ({}, "carrier-shape: 幂等短码表由 owner 装填；空表＝T 腿不出单（fail-closed）"),
    "t_remark_code_max_len": (0, "counter-property: 柜台备注预算仓库里没记过；0＝任何码都判超长＝拒发"),
    "t_missing_key_input": (False, "runtime-reading: 每轮读数不是配置；烘参刻按\"没有缺失\"落，运行期由 ctx 覆盖"),
}

# §CAP-OVERRIDE-ASCII（2026-11-09 批）：账号级覆盖登记——这些载体键的真源不是
# Go 出厂，而是 qmt_gateway/live_rules_snapshot.json（运营主账号的现网配置导出，
# 由 sync_live_rules.py 派生进 params.live_rules）。这里只登记「去快照的哪个键取值」，
# **一个数字都不写**——数由快照与参数文件持有。
# 对账执行点：scripts/sync_live_rules.py --check/--write 硬判
# （reason=daily_buy_cap_mismatch，失配即红、--write 失配不落盘），它是快照的
# 唯一读者，避免第二处快照解析。新增第二枚豁免键必须同时：
#   (a) 改 pytest 等值锁 test_go_factory_exclusions_are_pinned 并给出理由；
#   (b) 在 sync_live_rules 的对账里补一枚键的账目行；
#   (c) 在 SIZING_DEFAULTS（若它在那张表里）改注释指明新真源。
SNAPSHOT_OVERRIDES = {
    "daily_buy_cap": ("qmt", "daily_budget_amount"),
}

# §3.0 终裁（2026-11-10 10:45 owner 六卡拍板，覆盖 §1.1 早期任何相反表述）的**故意背离
# Go 出厂值**登记表。这些载体键的有意取值与 Go 默认不同，差异登记在案，不许静默：
#   * single_stock_cap_pct 35  —— Q4：覆盖账号 single_stock_value_pct=25（单票 35%）。
#   * cluster_cap_pct      35  —— Q4：对齐账号 concept_value_pct=35（概念/集群帽）。
#   * sizing_enabled       True —— Q2′：账号 sizing.enabled=false（全套 0）视为"未配置"而非
#                            "要关"，恢复 Go 出厂动态仓位（power_mean + 10%~30% 单笔 + T 仓
#                            净额剔除 + 总资产量纲护栏）。
#   * t_intraday_cap_pct   30  —— Q2：T 机动仓 30%（§3.0 "T 仓...机动仓 30%"）。
#   * daily_buy_cap     150000 —— §CAP-OVERRIDE-ASCII / Q4：账号 daily_budget_amount，日额随
#                            动态仓位 room，此值仅作绝对上限旁证（亦在 SNAPSHOT_OVERRIDES）。
# 用途：① --write 模式跳过这些键，保留参数文件里的 override 值，绝不被 Go 出厂值覆盖；
#       ② 测试端到对账时只核 override 值、不核 Go，避免把已裁决背离误报成漂移。
REGISTERED_OVERRIDES = {
    "single_stock_cap_pct": 35,
    "cluster_cap_pct": 35,
    "sizing_enabled": True,
    "t_intraday_cap_pct": 30,
    "daily_buy_cap": 150000,
}

# 柜台状态词表：真源在网关账本模块（构建器烘沙箱产物时读的也是这两个模块，见
# scripts/build_local_executor.py 的 render_status_vocab —— 同一把尺子，不在这另造一份）。
# rules_t 在 ctx 的委托行里没有 terminal 布尔时退回按状态字比较，所以它要的就是这份清单。
GATEWAY_VOCAB_READOUTS = {
    "t_terminal_status_codes": ("store", "TERMINAL_STATUSES",
                                 "gateway-python: 真源 qmt_gateway/store.py TERMINAL_STATUSES"),
}

# 因子/形态腿里那 6 枚**移植侧自己的表**：Go 侧没有对应读数（因子库注册表带的是 Compute 闭包，
# 没有可导出的窗口/方向元数据；规则模板是 owner 按账号配的样例；proxy 档位是 owner 的显式决定）。
# 值由模块自己声明的 PARAMS_TEMPLATE 供给——与 rules_sizing 的 SIZING_DEFAULTS 同一条路：
# 表是模块的一部分，工具只搬不改，绝不在这张表里写任何一个数。
FACTOR_TABLE_KEYS = (
    # §STRATEGY-SWITCH-SYNC（11-06）：factor_rules 已改由现网 tab 开关快照派生
    # （见 CARRIER_OWNED 的登记理由），不能再由模块模板在 --write 时覆盖回去，
    # 否则「今晚批准的 14 条」会被一条 fac_1 模板悄悄顶掉。
    "factor_defs", "pattern_rules", "factor_thresholds", "scalar_bands",
    "require_trading_day",
)


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
    # T 腿的"在途单多久算陈旧"与执行器自己那枚同源（local_executor 的撤单轮用的就是它）。
    "t_stale_order_sec": "stale_order_sec",
    # rules_ladder 的每档买入额：**Go 没有这个量**（Go 的单笔额由仓位引擎按目标仓位现算，
    # 不存在"每档固定金额"这枚出厂默认）。这里不新造一个数，而是把它接到载体已有的那枚
    # 单笔帽上——两条腿从此共用同一枚帽，改帽只有一处要动。
    # 为什么这样算诚实而不是敷衍：写一个新数（哪怕"看起来合理"的 20000）等于让工具替策略
    # 做决定；接现有键则只声明"载体侧这条腿的最大单笔＝那张已有的帽"。owner 若要给它
    # 单独一枚数，那是**一次显式配置**，届时把这条别名换成 params 里的显式值并登记主人。
    "ladder_buy_amount": "order_amount_cap",
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


def form_rule_keys():
    """rules_form 自己声明要读哪些键（与 carrier_rule_keys 同一姿势，单独成函数）。

    为什么不并进 carrier_rule_keys 的返回值：那个三元组有测试按位置解包，
    把它改成四元组会让"谁在扩模块清单"这件事变成一次跨批冲突。
    键的**清单**必须由模块自己报——脚本里再列一份就成了第三本账。
    """
    sys.path.insert(0, QMT_GATEWAY)
    import rules_form  # noqa: E402
    return set(rules_form.FORM_PARAM_KEYS)


def ported_module_inventory(params):
    """逐模块取"它自己声明要读哪些 params"——**清单由构建闸那把尺子量**。

    为什么现在改成这样（2026-11-06）：以前这里手写三份 import（sizing/t/form），
    而构建闸 g9 判的是 `params["ported_modules"]` 里**每一个**模块自己声明的
    `*_PARAM_KEYS`。两边各数一遍的结局就是这一批的起因——
    rules_ladder 与 rules_factor 的 129 枚旋钮在构建闸上报 `rule_knobs_missing`，
    而 `--check` 那边根本没这两行可看，还照样打 `DERIVE_RESULT=OK`。
    尺子有两把，绿的那把说的不是同一件事。

    现在：模块清单来自参数文件（与 g9 同一份），键清单来自构建闸同一个解析器
    （build_local_executor.ported_param_keys，按 AST 读模块自己的声明）。
    解析不了/模块不在册一律**抛**，不折成"这个模块没有键"。
    """
    sys.path.insert(0, os.path.join(REPO_ROOT, "scripts"))
    import build_local_executor as builder  # noqa: E402  (one ruler for both sides)
    names = list(params.get("ported_modules") or [])
    if not names:
        raise DeriveError("ported_modules_absent_from_params")
    out = {}
    for name in names:
        source = builder.read_pported_module(name)
        if source is None:
            raise DeriveError("ported_module_not_on_disk:%s" % name)
        out[name] = set(builder.ported_param_keys(source, name))
    return out



def knob_readout(modules, all_values, derived_keys, params):
    """逐模块打"它声明的旋钮有多少真的有主"，并把两枚坏数返回给 --check。

    这一行是本批存在的原因：移植规则的**代码**在 11-04 就进了载体，
    而它读的 102 枚阈值既不在参数文件里、也不在任何兜底表里——产物里每条规则
    第一次被调用就 raise KeyError，而构建闸只看"模块到没到"，不看"旋钮有没有主人"。
    现在这件事每模块一行数得出来：declared / 派生 / 登记自有 / 仍在文件里缺。

    返回值 {module: (missing_in_file, no_owner)} 是给 check 用的：**缺数就是红**。
    为什么这条必须进判据而不只是打印——11-05 那批的实测形态就是
    "DERIVE_KNOBS ... missing_in_file=17" 打了十七行、最后一行仍是 DERIVE_RESULT=OK，
    绿的人从来不看上面那几行，而构建闸同一天报 146 枚缺数。两把尺子必须同时红。
    """
    gaps = {}
    for name in sorted(modules):
        keys = modules[name]
        derived = sorted(k for k in keys if k in derived_keys)
        owned = sorted(k for k in keys if k in CARRIER_OWNED and k not in derived)
        absent = sorted(k for k in keys if k not in params)
        # 两个数分开看，它们说的是两件不同的事：
        #   missing_in_file —— 参数文件里还没有这一枚（--write 之后应当归 0）；
        #   no_owner —— 连 Go 读数与登记都没有。那只能列出来，绝不许挑一个数补上
        #               （补数＝工具替策略做决定，正是本工具要消灭的那种"没主人的数"）。
        homeless = sorted(k for k in absent if k not in all_values)
        print("DERIVE_KNOBS module=%s declared=%d derived=%d owned=%d missing_in_file=%d no_owner=%d"
              % (name, len(keys), len(derived), len(owned), len(absent), len(homeless)))
        if absent:
            # 单条锚点：门禁与人都要能一眼看到"缺的是哪些"，而不是只看到一个数。
            print("DERIVE_KNOBS_MISSING module=%s keys=%s" % (name, ",".join(absent)))
        if homeless:
            print("DERIVE_KNOBS_NO_OWNER module=%s keys=%s" % (name, ",".join(homeless)))
        gaps[name] = (len(absent), len(homeless))
    return gaps



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


def resolve_extras(carrier_values, params, sizing_defaults, extras=None):
    """把别名与模块自带声明表并进待写表，返回新的值表。

    优先级是刻意的：Go 派生值 > 别名 > 模块声明表。三者冲突时取更靠前的那一个，
    并把冲突原样打出来——**不静默覆盖**，因为覆盖意味着"参数文件里的数和它的
    真源不一致"，而那正是这把尺子要消灭的东西。

    `extras` 是第三条腿的三个来源（rules_factor 自己声明的表 / 网关账本的状态词表 /
    刻意落中性形状的无主键），姿势与 SIZING_DEFAULTS 完全一致：**表长在别人身上，工具只搬**。
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

    tables = dict((key, (value, "qmt_gateway/rules_sizing.py:SIZING_DEFAULTS"))
                  for key, value in sizing_defaults.items())
    for key, pair in (extras or {}).items():
        if key in tables and _norm(tables[key][0]) != _norm(pair[0]):
            # 两条来源腿供同一枚键且值不同＝工具内部的双账，当场停，不"取先到的那个"。
            raise DeriveError("module_table_double_sourced:%s %s vs %s"
                              % (key, tables[key][1], pair[1]))
        tables.setdefault(key, pair)

    for key, (value, source) in sorted(tables.items()):
        if key in merged:
            continue
        if key in params:
            if _norm(params[key]) == _norm(value):
                # 文件里已经有、且与模块声明表**一致**：这条键仍然算"有主人"，必须记进表里。
                # （以前这里是 continue，于是 --write 落完第二次再跑时，模块声明表供的那几枚
                # 全部从值表里消失、被 coverage 判成孤儿——同一棵树第二次跑比第一次红，
                # 而红的原因是"上一次把数写进去了"。这条现在按等值记账，两种姿势都判绿。）
                merged[key] = (value, source)
                continue
            # 参数文件里已有、且与模块声明表不一致：以已有值为准并出声（不悄悄改钱闸）。
            print("DERIVE_TABLE_CONFLICT key=%s file=%s table=%s kept=file"
                  % (key, json.dumps(params[key], ensure_ascii=True),
                     json.dumps(value, ensure_ascii=True)))
            continue
        merged[key] = (value, source)

    return merged


def gateway_vocab_values():
    """从网关账本模块**现读**状态词表（柜台状态这一族真源不在 Go，在网关那本账）。

    为什么在这里 import 而不是把清单抄进脚本：构建器烘沙箱产物时读的就是这两个模块
    （build_local_executor.render_status_vocab），脚本再抄一份就是第三本账——
    网关加一个状态词，抄的那份不会跟着动，而载体按旧词表判"这单还活着"。
    """
    sys.path.insert(0, QMT_GATEWAY)
    import store as gateway_store  # noqa: E402  (the ledger's own status vocabulary)
    modules = {"store": gateway_store}
    out = {}
    for key, (module_name, attr, source) in GATEWAY_VOCAB_READOUTS.items():
        module = modules.get(module_name)
        if module is None:
            raise DeriveError("gateway_vocab_module_unknown:%s" % module_name)
        raw = getattr(module, attr, None)
        if not raw:
            raise DeriveError("gateway_vocab_empty:%s.%s" % (module_name, attr))
        out[key] = (sorted(str(item) for item in raw), source)
    return out


def factor_table_values():
    """rules_factor 自己声明的那 6 枚**移植侧的表**（因子库 / 规则模板 / 覆盖表）。

    这些表在 Go 没有对应读数（internal/factor 的注册表带的是 Compute 闭包，没有可导出的
    窗口/方向元数据；规则模板是 owner 按账号配的样例；proxy 档位刻意留空＝那条腿弃权）。
    表本身是模块的一部分——与 rules_sizing.SIZING_DEFAULTS 同一条路：工具搬，不改，不猜。
    少一枚就当场判红（不许"表里好像没有就当空表"，那会把一条规则静默变成永不命中）。
    """
    sys.path.insert(0, QMT_GATEWAY)
    import rules_factor  # noqa: E402
    template = getattr(rules_factor, "PARAMS_TEMPLATE", None)
    if not isinstance(template, dict):
        raise DeriveError("rules_factor_params_template_absent")
    source = "qmt_gateway/rules_factor.py:PARAMS_TEMPLATE"
    out = {}
    for key in FACTOR_TABLE_KEYS:
        if key not in template:
            raise DeriveError("factor_table_key_absent_from_template:%s" % key)
        out[key] = (template[key], "%s[%s]" % (source, key))
    return out


def unowned_sentinels():
    """没有主人的三枚键的**中性形状**（值与理由都在 CARRIER_UNOWNED 里逐条登记）。

    这一条是整个脚本唯一"值不由 Go 也不由模块表供给"的地方，所以它只许放**弃权形状**：
    空表 / false / 零预算。任何看起来像阈值的数进到这里，就等于工具替策略做决定。
    """
    return dict((key, (value, reason)) for key, (value, reason) in CARRIER_UNOWNED.items())



def apply_write(doc, params, carrier_values):
    added, changed, unchanged = [], [], []
    for key, (value, _src) in sorted(carrier_values.items()):
        if key in REGISTERED_OVERRIDES:
            # §3.0 / Q4 已裁决的**故意背离** Go 出厂值：保留参数文件里的 override 值，
            # 绝不被 Go 默认覆盖（owner 10:45 终裁，差异登记在案）。--write 只更新其余键。
            unchanged.append(key)
            continue
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
    form_keys = form_rule_keys()
    # 模块清单与构建闸同源：params["ported_modules"] 里每一个模块、每个模块自己声明的键。
    modules = ported_module_inventory(params)
    required = set()
    for keys in modules.values():
        required |= keys
    # 第三条腿：模块自带的表 / 网关账本的状态词表 / 无主键的中性形状。
    extras = {}
    extras.update(factor_table_values())
    extras.update(gateway_vocab_values())
    extras.update(unowned_sentinels())
    carrier_values = build_carrier_values(derived, provenance, params, sizing_defaults)

    if chosen == "emit-json":
        print(json.dumps(derived, ensure_ascii=True, indent=2, sort_keys=True))
        return 0

    if chosen == "emit-derived-keys":
        for key in sorted(carrier_values):
            print(key)
        return 0

    all_values = resolve_extras(carrier_values, params, sizing_defaults, extras)
    cov = coverage(all_values, required, params)
    # 每个移植模块自己报清单，这里逐模块数"旋钮有多少真的落了地"。
    gaps = knob_readout(modules, all_values, set(carrier_values), params)
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
    # 每一枚移植模块的"文件里还缺几枚 / 连主人都没有"都算红（与构建闸 g9 同一口径）。
    short = sorted(name for name, (missing, homeless) in gaps.items() if missing or homeless)
    if short:
        print("DERIVE_KNOBS_SHORT modules=%s" % ",".join(short))
    if chosen == "check":
        bad = bad or bool(short)
        print("DERIVE_RESULT=%s" % ("FAIL" if bad else "OK"))
        return 1 if bad else 0
    print("DERIVE_RESULT=%s" % ("DRIFT" if bad else "OK"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
