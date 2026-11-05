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

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/engine"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategies/break_down"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/factor"
	"quant-trading-v2/internal/strategies/good_news_fade"
	"quant-trading-v2/internal/strategies/high_churn"
	"quant-trading-v2/internal/strategies/leader_decay"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/tcalib"
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
	collectBearTactics(c)
	collectScoreScale(c)
	collectFactorScoring(c)
	collectCarrierVocabulary(c)

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
	// §CARRIER-PARITY-1105：载体要与现网同款地撤单/改价/排队占预算，四枚值只从 Go 取。
	// 前两枚出厂为关（0），照实搬 0 不是"把门拆了"——Go 的语义本来就是由 owner 按下。
	c.put("signal_gone_cancel_sec", src, q.SignalGoneCancelSec)
	c.put("vwap_reprice_pct", src, q.VWAPRepricePct)
	c.put("buy_room_guard", "internal/engine/buy_reserve.go:buyRoomGuardOn", engine.BuyRoomGuardDefaultForCarrier())
	c.put("reprice_max_per_day", "internal/engine/buy_reprice_20261105.go:repriceMaxPerDay",
		engine.RepriceMaxPerDayForCarrier())
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

// collectBearTactics 取做空战法的评分阈值与派生窗口（§DERIVE-CARRIER-1105 / 1106 扩到七包）。
//
// 为什么必须补这一腿：qmt_gateway/rules_form.py 是从这四个 Go 战法移植的，它按
// FORM_PARAM_KEYS 声明自己读哪些 params，而那 102 枚键在参数文件里**一个都不存在**——
// 产物里每条移植规则第一次被调用就 raise KeyError('bd_min_bars')。规则代码搬完了，
// 值没搬。现在值由各战法包**转调自己的常量**导出（各包 derive_carrier.go），
// 本函数只是把这几张表并进派生结果，一个数都不在这里落地。
//
// 11-06 这批又并进来三个包（double_bump / dragon / dragon_return）：它们是
// qmt_gateway/rules_ladder.py 那四条做多腿的真源，声明的 115 枚旋钮此前同样没有主人。
// 加进**同一个批次列表**而不是另开一个函数：批次列表的形状（source + table，空表即判红）
// 就是这批唯一要防的东西——"包在、旋钮空"必须当场停，而不是等装载闸去猜。
//
// rule_side 与四枚 kind 走的是"已有名字"那条路：strategy.ActionSell / Signal* 本来就是
// 命名常量（战法出信号用的就是它们），所以这里只做 string 转换，不写 "sell" 这个字面量。
// English: every tactic threshold comes from the tactic package's own constants;
// the side and the kinds come from the already-named strategy identifiers.
func collectBearTactics(c *collector) {
	for _, batch := range []struct {
		src   string
		table map[string]interface{}
	}{
		{"internal/combat_agent/adapter.go:shortXxx window constants", combat_agent.DeriveCarrierWindows()},
		{"internal/strategies/break_down/break_down.go:bdXxx constants", break_down.DeriveCarrierThresholds()},
		{"internal/strategies/high_churn/high_churn.go:hcXxx constants", high_churn.DeriveCarrierThresholds()},
		{"internal/strategies/leader_decay/leader_decay.go:ldXxx constants", leader_decay.DeriveCarrierThresholds()},
		{"internal/strategies/good_news_fade/good_news_fade.go:gfXxx constants", good_news_fade.DeriveCarrierThresholds()},
		{"internal/strategies/double_bump/double_bump.go:dbXxx constants + factory strategy cfg", double_bump.DeriveCarrierThresholds()},
		{"internal/strategies/dragon/dragon.go:dgXxx constants + factory strategy cfg", dragon.DeriveCarrierThresholds()},
		{"internal/strategies/dragon_return/dragon_return.go:drXxx constants", dragon_return.DeriveCarrierThresholds()},
	} {
		if len(batch.table) == 0 {
			// 空表＝那一包里一个读数都没导出。这比"键缺失"更安静，必须当场判红。
			fmt.Fprintf(os.Stderr, "DERIVE_EMPTY_TABLE source=%s\n", batch.src)
			os.Exit(1)
		}
		for _, key := range sortedInterfaceKeys(batch.table) {
			c.put(key, batch.src, batch.table[key])
		}
	}
	// 四个战法的信号种类与方向：Go 出信号用的就是这两组命名常量（战法包内不重写）。
	const sideSrc = "internal/strategy/types.go:ActionSell"
	c.put("rule_side", sideSrc, string(strategy.ActionSell))
	const kindSrc = "internal/strategy/types.go:Signal* type constants"
	c.put("break_down_kind", kindSrc, string(strategy.SignalBreakDown))
	c.put("high_churn_kind", kindSrc, string(strategy.SignalHighChurn))
	c.put("leader_decay_kind", kindSrc, string(strategy.SignalLeaderDecay))
	c.put("good_news_fade_kind", kindSrc, string(strategy.SignalGoodNewsFade))
}

// collectScoreScale 取两条做多腿共用的**分值域刻度**与两个战法共用的**最少根数**。
//
// 为什么单独成一个采集函数：载体侧 rules_ladder 的 `pct_scale` 与 rules_factor 的 `min_bars`
// 都是**一枚数供两条腿**（dragon 与 double_bump 用同一个尺度归一各自的加权分；factor 与
// pattern 用同一条"不足几根就不算"的线）。Go 侧此前是各包各写字面量，所以这两枚一旦
// 由某个包自己导出，就会变成"两个战法两个主人、载体只有一个键"——collector.put 会判红，
// 判得对。现在它们是 internal/strategy/types.go 的两枚公共常量，两处战法都转调它，
// 这里也从那一处取。
// English: the shared score scale and the shared minimum-bar guard, both taken from the
// single strategy-package constants the tactics themselves now call.
func collectScoreScale(c *collector) {
	const src = "internal/strategy/types.go:ScoreScale"
	c.put("pct_scale", src, strategy.ScoreScale)
	const barsSrc = "internal/strategy/types.go:ScoreMinBars (factor.go + pattern.go both call it)"
	c.put("min_bars", barsSrc, strategy.ScoreMinBars)
}

// collectFactorScoring 取因子战法那条移植腿（rules_factor.py）声明的评分旋钮。
// 每一枚都是 internal/strategies/factor 自己判据用的常量（factor.go 顶部那组 +
// finaScore 读的那张表），本函数只并表，不落地任何一个数。
// entry_side 走的是"已有名字"那条路（战法出信号用的就是 strategy.ActionBuy）。
// English: the factor rule library's own scale/fallback constants and its absolute bands.
func collectFactorScoring(c *collector) {
	const src = "internal/strategies/factor/factor.go:factorDefaultXxx / scoreDomainXxx constants"
	scoring := factor.DeriveCarrierScoring()
	if len(scoring) == 0 {
		fmt.Fprintf(os.Stderr, "DERIVE_EMPTY_TABLE source=%s (scoring)\n", src)
		os.Exit(1)
	}
	for _, key := range sortedInterfaceKeys(scoring) {
		c.put(key, src, scoring[key])
	}
	c.put("score_domain", src, factor.DeriveCarrierScoreDomain())
	c.put("financial_bands", "internal/strategies/factor/factor.go:finaBands table",
		factor.DeriveCarrierFinancialBands())
	c.put("entry_side", "internal/strategy/types.go:ActionBuy (via factor entry readout)",
		factor.DeriveCarrierEntrySide())
}

// collectCarrierVocabulary 取做 T 移植腿（rules_t.py）声明的那批**判据写法**：
// 时段边界、板块前缀与 ST 字样、行情门认的档位串、可开 T 的锚点来源、被撤的字样、
// 卖腿必须留下的底仓比例、费用缓冲、成本安全倍数、开盘钟点。
//
// 为什么这些也要从 Go 出：它们在 Go 都不是"配置项"而是**判据本身**——
// switch 的 case、Contains 的字样、quota 里的那次除法。移植腿在自己的机器上调不到
// Go 的函数，只能收一张烘进 params 的表；表一旦由载体自己写，改 Go 的那一处不会有任何东西响。
// 单位/形态换算一律用被测对象自己的方法（Duration.Seconds / time.Format），这里不写数字。
// English: the gate vocabulary and ratios the live T chain itself compares against, exported
// so the ported carrier never becomes the owner of a decision the engine makes.
func collectCarrierVocabulary(c *collector) {
	const dataSrc = "internal/data/trade_time.go + limitup_pct.go (gate tables)"
	c.put("t_session_windows", dataSrc, data.ActiveSessionWindows())
	boards := data.LimitUpBoardTables()
	if len(boards) == 0 {
		fmt.Fprintf(os.Stderr, "DERIVE_EMPTY_TABLE source=%s (board tables)\n", dataSrc)
		os.Exit(1)
	}
	c.put("t_wide_board_prefixes", "internal/data/limitup_pct.go:limitUpWidePrefixes", boards["wide"])
	c.put("t_bse_board_prefixes", "internal/data/limitup_pct.go:limitUpBsePrefixes", boards["bse"])
	c.put("t_st_markers", "internal/data/limitup_pct.go:limitUpMarkerST", boards["st"])

	const engineSrc = "internal/engine/intraday_t_live.go (regime gate / keep / token / clock)"
	c.put("t_regime_allow_states", engineSrc, engine.TRegimeAllowStates())
	c.put("t_regime_block_states", engineSrc, engine.TRegimeBlockStates())
	c.put("t_cancelled_status_tokens", engineSrc, []string{engine.TCancelStatusToken()})
	c.put("t_min_remaining_position_pct", engineSrc, engine.TSellKeepPositionPct())
	c.put("t_fee_buffer_pct", engineSrc, engine.TFeeBufferPct())
	c.put("t_session_open_hhmm", engineSrc, engine.TDayStartClock())

	c.put("t_anchor_ok_sources", "internal/store/real_positions.go:anchorTicketSources",
		store.TAnchorOkSources())
	// 成本安全倍数：min_spread_pct 的安全边际，真源是标定包那枚出厂默认。
	// 载体用它乘成本线，Go 用它乘成本线（tcalib.Params.MinSpreadPct），同一枚数两处用。
	c.put("t_cost_safety_multiplier", "internal/tcalib/tcalib.go:DefaultSafetyMul", tcalib.DefaultSafetyMul)
}

// sortedInterfaceKeys 把 map 的键排序后返回：导出顺序必须稳定，
// 否则同一棵树两次跑出来的 provenance 顺序不同，diff 就成了噪音。
func sortedInterfaceKeys(table map[string]interface{}) []string {
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
