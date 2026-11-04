package main

// cmd/derive_carrier_params —— 把 Go 侧的**出厂默认值**按载体参数名打出来。
//
// 为什么需要它（2026-11-04）
//   QMT 内置策略与本地执行器是两个独立进程，它们不认识 Go 的配置对象，只能靠
//   `qmt_gateway/local_executor_params.json` 里烘进去的数。以前那些数是**人手抄**的，
//   抄错一位（0.45 抄成 0.54）没有任何东西会响——而它改的是策略的含义，不是某个数字。
//   这条工具把"抄"从流程里拿掉：值只存在于 Go 的一处（结构体默认 + 访问器），
//   脚本按名字取，取不到就整批红。
//
// 边界（不许越过的两条）
//   1. 本文件**一个数字都不许出现**。任何看起来像阈值的字面量都意味着第二本账。
//      需要 Go 内部的常量时，调用那一侧已有的导出访问器；没有访问器的键一律走
//      `--report` 的"未映射"清单，等 owner 决定要不要在 Go 侧把它命名出来。
//   2. 只读。这里不写盘、不动配置、不接数据源；根对象一律是**出厂默认**
//      （DefaultSizingConfig / 空 TConfig / DefaultQMTConfig / paper.DefaultConfig），
//      即"一个从没被账号改过的号会得到什么"，不是现网值。
//
// 用法
//   go run ./cmd/derive_carrier_params                 # 全部键，JSON
//   go run ./cmd/derive_carrier_params --scale_floor   # 只打某几键
//   go run ./cmd/derive_carrier_params --provenance    # 键 → 真源文件（审计用）
//
// 输出契约：`{"params": {...}, "provenance": {key: "internal/config/config.go:DefaultSizingConfig"}}`。
// 任何一键取值 panic/失败 ⇒ 不打半成品，直接退出码 1 并把键名打到 stderr。
// English: prints the factory defaults of the Go decision layer under the carrier parameter names.
// This file must contain no numeric literal at all; values come only from Go accessors.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/engine"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/signalctl"
)

// collector 攒住"载体键 → 值 + 真源"。重复键直接判红：同一枚数由两处供给正是本仓反复锤过的双账形态。
// English: collects key -> value plus its source; a duplicate key is fatal (two suppliers, one number).
type collector struct {
	params map[string]interface{}
	prov   map[string]string
}

func newCollector() *collector {
	return &collector{params: map[string]interface{}{}, prov: map[string]string{}}
}

func (c *collector) put(key, source string, value interface{}) {
	if prev, dup := c.prov[key]; dup {
		fmt.Fprintf(os.Stderr, "DERIVE_DUPLICATE key=%s first=%s second=%s\n", key, prev, source)
		os.Exit(1)
	}
	c.params[key] = value
	c.prov[key] = source
}

func main() {
	only := map[string]bool{}
	wantProv := false
	for _, arg := range os.Args[1:] {
		if arg == "--provenance" {
			wantProv = true
			continue
		}
		only[arg] = true
	}

	c := newCollector()
	collectSizing(c)
	collectTConfig(c)
	collectQMT(c)
	collectEmotion(c)
	collectFlow(c)
	collectPaperCost(c)
	collectSignalctl(c)
	collectBoards(c)
	collectEngineTimings(c)

	keys := make([]string, 0, len(c.params))
	for k := range c.params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 输出先攒 params，再按 --provenance 决定是否带真源表：
	// 编码只在最后做一次，中途任何取值失败都已 exit，调用方因此永远拿不到半张表。
	out := map[string]interface{}{"params": map[string]interface{}{}}
	pj := out["params"].(map[string]interface{})
	for _, k := range keys {
		if len(only) > 0 && !only[k] {
			continue
		}
		pj[k] = c.params[k]
	}
	if wantProv {
		pv := map[string]string{}
		for k := range pj {
			pv[k] = c.prov[k]
		}
		out["provenance"] = pv
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "DERIVE_ENCODE_FAIL err=%v\n", err)
		os.Exit(1)
	}
}

// collectSizing 取动态仓位合成的出厂形状。
// 根对象是 `DefaultSizingConfig().WithDefaults()`——先出厂、再补默认映射，
// 与引擎装配 Regime 时走的是同一条链（config.WithDefaults 注释），所以这里不需要重写任何兜底。
func collectSizing(c *collector) {
	const src = "internal/config/config.go:DefaultSizingConfig"
	sc := config.DefaultSizingConfig().WithDefaults()
	c.put("sizing_enabled", src, sc.EnabledOn())
	c.put("sizing_shadow", src, sc.ShadowOn())
	c.put("budget_mode", src, sc.BudgetModeOf())
	c.put("sizing_mode", src, sc.ModeOf())
	c.put("power_p", src, sc.PowerPOf())
	c.put("group_floor_market", src, sc.GroupFloor("market"))
	c.put("group_floor_stock", src, sc.GroupFloor("stock"))
	c.put("scale_floor", src, sc.Floor)
	c.put("scale_cap", src, sc.Cap)
	c.put("emotion_scales", src, sc.EmotionScales)
	c.put("state_scales", src, sc.StateScales)
	c.put("tier_scales", src, sc.TierScales)
	c.put("tier_red_default", "internal/config/config.go:SizingConfig.TierScales[Red]", sc.TierScales["Red"])
	c.put("breadth_missing_scale", src, sc.BreadthMissingScale)
	c.put("macro_window_scales", src, sc.MacroWindowScales)
	c.put("macro_lead_days", src, sc.MacroLeadDays)
	c.put("macro_lead_curve", src, sc.MacroLeadCurve)
	c.put("macro_event_day_scale", src, sc.MacroEventDayScale)
	c.put("holiday_scales", src, sc.HolidayScales)
	c.put("holiday_block_new", src, sc.HolidayBlockNewOn())
	c.put("llm_bearish_scale", src, sc.LLMBearishScale)
	c.put("kelly_lambda", src, sc.KellyLambda)
	c.put("kelly_min_prob", src, sc.KellyMinProb)
	c.put("vol_target_pct", src, sc.VolTargetPct)
	c.put("vol_clamp", "internal/config/config.go:SizingConfig.VolClampLo/Hi",
		[]float64{sc.VolClampLo, sc.VolClampHi})
	c.put("max_total_position_pct", src, sc.MaxTotalPositionPct)
	c.put("per_trade_position_pct", src, sc.PerTradePositionPct)
	c.put("per_trade_position_max_pct", src, sc.PerTradePositionMaxPct)
	c.put("max_drawdown_pct", src, sc.MaxDrawdownPct)
	c.put("stoploss_guard_count", src, sc.StoplossGuardCount)
	c.put("stoploss_guard_window_days", src, sc.StoplossGuardWindowDays)
	c.put("sl_offset_risk_budget_keep", "internal/config/config.go:SizingConfig.RiskBudgetKeepOn", sc.RiskBudgetKeepOn())
	c.put("cluster_cap_pct", "internal/config/config.go:SizingConfig.ClusterCapPctOf", sc.ClusterCapPctOf())
	c.put("cluster_rho", "internal/config/config.go:SizingConfig.ClusterRhoOf", sc.ClusterRhoOf())
	c.put("sell_boost_level", "internal/config/config.go:SizingConfig.SellBoostLevelOf", sc.SellBoostLevelOf())
	c.put("sl_offset_per_pct", src, sc.SLOffsetPerPct)
	c.put("sl_offset_cap", src, sc.SLOffsetCap)
	c.put("tp_offset_per_pct", src, sc.TPOffsetPerPct)
	c.put("tp_offset_cap", src, sc.TPOffsetCap)
	c.put("sl_offset_min_index_drop", src, sc.SLOffsetMinIndexDrop)
	c.put("bear_sl_tighten_pt", src, sc.BearSLTightenPt)
	// index_scale_thresholds 的键是"档位"（-1 / -2 / deep），值是"该档缩放"。
	// 载体要的是同一批数，只是换了两个名字（bands 与 scales）——换算只做**改名**，不做算术。
	c.put("index_scale_thresholds", src, sc.IndexScaleThresholds)
}

// collectTConfig 取"从没配过 T 的账号"的生效值。
// 根对象刻意是**零值** TConfig：TConfig 整块没有被 DefaultSizingConfig 物化（config.go 注释），
// 出厂值全部由各访问器给出，所以零值 + 访问器 == 出厂生效值，而物化副本反而会把
// "键缺失"和"显式配 0"两态抹平——那正是本项目最忌的静默改档。
func collectTConfig(c *collector) {
	const src = "internal/config/config.go:TConfig accessors (zero value = factory)"
	tc := &config.TConfig{}
	c.put("t_enabled", src, tc.TEnabled())
	c.put("t_shadow", src, tc.TShadowNoTOf())
	c.put("t_buy_first", src, tc.TBuyFirst())
	c.put("t_sell_first", src, tc.TSellFirst())
	c.put("t_intraday_cap_pct", src, tc.TIntradayCapPct())
	c.put("t_min_spread_pct", src, tc.TMinSpreadPct())
	c.put("t_max_rounds", src, tc.TMaxRounds())
	c.put("t_force_unwind_at", src, tc.TForceUnwindHM())
	c.put("t_last_open_at", src, tc.TLastOpenHM())
	c.put("t_vwap_dev_pct", src, tc.VWAPDevPctOf())
	c.put("t_min_amplitude_pct", src, tc.TMinAmplitudePctOf())
	c.put("t_min_market_scale", src, tc.TMinMarketScaleOf())
	c.put("t_sell_env_min_market_scale", src, tc.TSellEnvMinMarketScaleOf())
	c.put("t_intraday_sell_cap_total_pct", src, tc.TIntradaySellCapTotalPctOf())
	c.put("t_market_plunge_pct", src, tc.TMarketPlungePctOf())
	c.put("t_near_extreme_pct", src, tc.TNearExtremePctOf())
	c.put("t_limit_distance_pct", src, tc.TLimitDistancePctOf())
	c.put("t_open_cooling_min", src, tc.TOpenCoolingMinOf())
	c.put("t_stop_guard_pct", src, tc.TStopGuardPctOf())
	c.put("t_round_stop_pct", src, tc.TRoundStopPctOf())
	c.put("t_loss_breaker_pct", src, tc.TLossBreakerPctOf())
	c.put("t_vol_ratio_max", src, tc.TVolRatioMaxOf())
	c.put("t_regime_gate_enabled", src, tc.TRegimeGateEnabledOf())
	c.put("t_min_amount_wan", src, tc.TMinAmountWanOf())
	c.put("t_min_turnover_pct", src, tc.TMinTurnoverPctOf())
	c.put("t_min_listed_days", src, tc.TMinListedDaysOf())
	c.put("t_vwap_check_tol_pct", src, tc.TVWAPCheckTolPctOf())
}

// collectQMT 取执行侧金额与时段闸的出厂值。
// 时段闸在 Go 里是逗号分隔字符串，载体里是 [起,止] 数组——这里只出 Go 的原文，
// 改名/拆形由脚本做（形态转换不是数值来源）。
func collectQMT(c *collector) {
	const src = "internal/config/config.go:DefaultQMTConfig"
	q := config.DefaultQMTConfig()
	c.put("qmt_no_cancel_windows", src, q.NoCancelWindows)
	c.put("qmt_no_new_order_windows", src, q.NoNewOrderWindows)
	c.put("daily_buy_cap", "internal/config/config.go:QMTConfig.DailyBudgetAmount", q.DailyBudgetAmount)
	c.put("min_order_amount", "internal/config/config.go:QMTConfig.EffectiveMinOrderAmount", q.EffectiveMinOrderAmount())
	c.put("qmt_min_buy_price", src, q.MinBuyPrice)
	c.put("qmt_daily_max_buys", src, q.DailyMaxBuys)
	c.put("qmt_max_positions", src, q.MaxPositions)
	c.put("risk_day_loss_limit_pct", "internal/config/config.go:RiskGateConfig.DayLossLimitPct", q.RiskGate.DayLossLimitPct)
	c.put("risk_concept_value_pct", "internal/config/config.go:DefaultQMTConfig RiskGate.ConceptValuePct", q.RiskGate.ConceptValuePct)
	c.put("risk_max_order_amount", "internal/config/config.go:RiskGateConfig.MaxOrderAmount", q.RiskGate.MaxOrderAmount)
	c.put("entry_pullback_pct", src, q.EntryPullbackPct)
	c.put("entry_pullback_max_gap_pct", src, q.EntryPullbackMaxGapPct)
	c.put("entry_timeout_sec", src, q.EntryTimeoutSec)
}

// collectEmotion 取情绪相位判定阈值的出厂值。
// 真源是 research 侧那份内置默认（data 包不自带默认，cfg 由调用方给），
// 所以这里加一个只读导出（internal/research/derive_carrier.go），而不是再抄一份表。
func collectEmotion(c *collector) {
	const src = "internal/research/emotion_phase.go:emotionPhaseDefaultCfg"
	e := research.EmotionPhaseDefaultThresholds()
	c.put("emo_ice_limitup_max", src, e.EmoIceLimitupMax)
	c.put("emo_ice_board_max", src, e.EmoIceBoardMax)
	c.put("emo_start_limitup_min", src, e.EmoStartLimitupMin)
	c.put("emo_start_limitup_max", src, e.EmoStartLimitupMax)
	c.put("emo_start_board_max", src, e.EmoStartBoardMax)
	c.put("emo_ferment_limitup_min", src, e.EmoFermentLimitupMin)
	c.put("emo_ferment_limitup_max", src, e.EmoFermentLimitupMax)
	c.put("emo_ferment_board_max", src, e.EmoFermentBoardMax)
	c.put("emo_climax_limitup_min", src, e.EmoClimaxLimitupMin)
	c.put("emo_climax_board_min", src, e.EmoClimaxBoardMin)
	c.put("emo_retreat_limitup_max", src, e.EmoRetreatLimitupMax)
	c.put("emo_retreat_board_max", src, e.EmoRetreatBoardMax)
	c.put("emo_diverge_limitup_drop", src, e.EmoDivergeLimitupDrop)
	c.put("emo_diverge_board_drop", src, e.EmoDivergeBoardDrop)
	c.put("emotion_phases", "internal/research/emotion_phase.go:emotionPhases", research.EmotionPhaseNames())
}

// collectFlow 取资金流因子的出厂档位（5 日与当日两条腿的键名与载体侧一致，原样搬）。
// 三个开关都走 SizingConfig 上的访问器——出厂档里 Enabled 是 nil（＝开），
// 直接打 `*f.Enabled` 会 panic，打 nil 会让载体收到 null 而不是 true。
func collectFlow(c *collector) {
	const src = "internal/config/sizing_flow.go:DefaultFlowFactorConfig"
	f := config.DefaultSizingConfig().FlowFactor
	c.put("flow_factor_enabled", "internal/config/config.go:SizingConfig.FlowFactorOn", config.DefaultSizingConfig().FlowFactorOn())
	c.put("flow_sell_enabled", "internal/config/config.go:SizingConfig.FlowSellOn", config.DefaultSizingConfig().FlowSellOn())
	c.put("flow_ratio_basis", "internal/config/config.go:SizingConfig.FlowRatioBasisOf", config.DefaultSizingConfig().FlowRatioBasisOf())
	c.put("flow_5d_scales", src, f.Buy5DScales)
	c.put("flow_bands", src, f.Buy5DBands)
	c.put("flow_today_scales", src, f.BuyTodayScales)
	c.put("flow_today_bands", src, f.BuyTodayBands)
	c.put("flow_sell_scales", src, f.SellScales)
	c.put("flow_sell_bands", src, f.SellBands)
}

// collectPaperCost 取费用口径。
// 实盘 T 与纸面账本同源是既有裁决（§8.9a：不新造费率），所以出厂值就取纸面那一份；
// 执行器里的 t_commission_rate / t_min_commission / t_stamp_tax_rate / t_slippage_bps
// 全部从这一处来。
func collectPaperCost(c *collector) {
	const src = "internal/paper/paper.go:DefaultConfig"
	p := paper.DefaultConfig()
	c.put("t_commission_rate", src, p.CommissionRate)
	c.put("t_min_commission", src, p.MinCommission)
	c.put("t_stamp_tax_rate", src, p.StampTaxRate)
	c.put("t_slippage_bps", src, p.SlippageBps)
}

// collectSignalctl 取仓位链里"常量级"的封顶值。
// MissingKeyTargetCap 早就有导出读数（§DYN-BLIND 的出声腿用它，避免第二处写 0.50），
// 这里同样只调导出函数。
func collectSignalctl(c *collector) {
	c.put("missing_key_target_cap", "internal/signalctl/sizing.go:MissingKeyTargetCap", signalctl.MissingKeyTargetCap())
}

// collectBoards 取分板块涨跌停幅度。
// 唯一口径是 data.LimitUpPct（§R6 P1-4 收敛过），载体要的是一档一档的数，
// 所以这里按板块探针代码调用同一个函数取值——探针写在 data/derive_carrier.go 里，
// 连同"问的是谁"一起导出，避免读数的人以为拿到的是全系统通用值。
func collectBoards(c *collector) {
	const src = "internal/data/limitup_pct.go:LimitUpPct (via probe codes)"
	bands := data.LimitUpPctBands()
	c.put("t_limit_pct_main", src, bands["main"])
	c.put("t_limit_pct_wide", src, bands["wide"])
	c.put("t_limit_pct_star", src, bands["star"])
	c.put("t_limit_pct_bse", src, bands["bse"])
	c.put("t_limit_pct_st", src, bands["st"])
	c.put("t_limit_pct_probe_codes", "internal/data/derive_carrier.go:LimitUpPctProbeCodes",
		data.LimitUpPctProbeCodes())
}

// collectEngineTimings 取做 T 的时序常量。
// Go 侧是 time.Duration，载体 params 一律按**秒**收——换算用 Duration.Seconds()，
// 不在这里手写 60（那等于把单位口径的第二份定义放进工具）。
func collectEngineTimings(c *collector) {
	const src = "internal/engine/intraday_t_live.go (exported readouts)"
	c.put("t_ghost_grace_sec", src, engine.TGhostGraceSeconds())
	c.put("t_premise_grace_sec", src, engine.TPremiseGraceSeconds())
	c.put("t_regime_stale_sec", src, engine.TRegimeStaleSeconds())
	c.put("t_min_base_keep_qty", src, engine.TMinBaseKeepQty())
	c.put("t_regime_min_votes", src, engine.TRegimeMinVotes())
}
