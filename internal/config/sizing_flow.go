// sizing_flow.go — §FLOW-FACTOR 净流入 dynamic 因子配置 + §SIZING-EVO 自动回测研究配置
// （2026-10-25，docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.6/§2.7）。
//
// 设计口径（与 SizingConfig 既有字段逐字对齐）：
//   - 指针三态：nil=出厂默认、显式 true/false=账号选择——"没配过"与"显式关"必须可区分；
//   - 映射合并：WithDefaults 时用户键覆盖默认键（mergeScales），不要求整表重写；
//   - 缺数纪律由消费方（signalctl 因子）执行：本层只存配置，不伪造数值。
//
// English: net-inflow dynamic-factor config + auto-backtest-research (sizing_evo) config.
// Pointer tri-state and map-merge semantics match the rest of SizingConfig.
package config

import "strings"

// FlowFactorConfig 净流入因子配置（账号级，挂在 SizingConfig.FlowFactor）。
//
// 指标口径（§2.3）：FlowRatio = 主力净流入 / 流通市值（%）。
//   - Buy5D*     ：近 5 个交易日净流入累计 / 流通市值（主因子）；
//   - BuyToday*  ：当日主力净流入 / 流通市值（辅因子，实时值，幅度减半）；
//   - Sell*      ：净流出卖出加权（权重=买入偏离折半，§2.5）。
//
// English: net-inflow factor config — ratios are main-force net inflow over float market cap.
type FlowFactorConfig struct {
	// Enabled 总开关（nil=开）。关=两个买入因子弃权（与"没有这一维"逐字一致）。
	// English: master switch (nil = on); off abstains both buy factors.
	Enabled *bool `json:"enabled,omitempty"`
	// SellEnabled 卖出侧开关（nil=开）。可单独关卖出侧而不动买入侧。
	// English: sell-side switch (nil = on); can be turned off independently of the buy side.
	SellEnabled *bool `json:"sell_enabled,omitempty"`
	// RatioBasis 归一化分母：float_mcap（默认，Q1 裁决）/ amount（成交额，备选）。
	// English: normalization denominator — float market cap (default) or turnover amount.
	RatioBasis string `json:"ratio_basis,omitempty"`
	// Buy5DScales 5 日净流入率 → 买入乘子（键：strong_in/mild_in/neutral/mild_out/strong_out）。
	// English: 5-day net-inflow ratio band → buy multiplier.
	Buy5DScales map[string]float64 `json:"buy_5d_scales,omitempty"`
	// Buy5DBands 5 日分档阈值（%，键：strong_in_pct/mild_in_pct/mild_out_pct/strong_out_pct）。
	// English: 5-day band thresholds in percent.
	Buy5DBands map[string]float64 `json:"buy_5d_bands,omitempty"`
	// BuyTodayScales 当日净流入率 → 买入乘子（键同上）。
	// English: same-day net-inflow ratio band → buy multiplier.
	BuyTodayScales map[string]float64 `json:"buy_today_scales,omitempty"`
	// BuyTodayBands 当日分档阈值（%）。
	// English: same-day band thresholds in percent.
	BuyTodayBands map[string]float64 `json:"buy_today_bands,omitempty"`
	// SellScales 净流出 → 卖出权重（键：strong_out/mild_out/today_strong_out/today_mild_out；
	// 权重作用于卖出确认窗时长×权重，§2.5）。
	// English: net-outflow band → sell weight (applied to the sell confirm-window duration).
	SellScales map[string]float64 `json:"sell_scales,omitempty"`
	// SellBands 卖出触发阈值（%，键：strong_out_pct/mild_out_pct）。
	// English: sell trigger thresholds in percent.
	SellBands map[string]float64 `json:"sell_bands,omitempty"`
}

// 档位键名常量（映射表与带映射共用，防拼写漂移）。
// English: band key constants shared by the maps and the band mapper.
const (
	FlowBandStrongIn  = "strong_in"
	FlowBandMildIn    = "mild_in"
	FlowBandNeutral   = "neutral"
	FlowBandMildOut   = "mild_out"
	FlowBandStrongOut = "strong_out"
)

// DefaultFlowFactorConfig 出厂默认（§2.4/§2.5 默认档位；R4：标定后由 evo 承接修正）。
// English: factory defaults for the net-inflow factor (subject to calibration per R4).
func DefaultFlowFactorConfig() FlowFactorConfig {
	return FlowFactorConfig{
		RatioBasis: "float_mcap",
		Buy5DScales: map[string]float64{
			FlowBandStrongIn: 1.10, FlowBandMildIn: 1.05, FlowBandNeutral: 1.00,
			FlowBandMildOut: 0.85, FlowBandStrongOut: 0.70,
		},
		Buy5DBands: map[string]float64{
			"strong_in_pct": 3, "mild_in_pct": 1, "mild_out_pct": -1, "strong_out_pct": -3,
		},
		BuyTodayScales: map[string]float64{
			FlowBandStrongIn: 1.05, FlowBandMildIn: 1.025, FlowBandNeutral: 1.00,
			FlowBandMildOut: 0.95, FlowBandStrongOut: 0.90,
		},
		BuyTodayBands: map[string]float64{
			"strong_in_pct": 3, "mild_in_pct": 1, "mild_out_pct": -1, "strong_out_pct": -3,
		},
		SellScales: map[string]float64{
			FlowBandStrongOut: 0.85, FlowBandMildOut: 0.95,
			"today_strong_out": 0.95, "today_mild_out": 0.975,
		},
		SellBands: map[string]float64{
			"strong_out_pct": -3, "mild_out_pct": -1,
		},
	}
}

// WithDefaults 合并出厂默认：用户键覆盖默认键；RatioBasis 空=float_mcap。
// English: merges user keys over the factory defaults; empty RatioBasis → float_mcap.
func (f FlowFactorConfig) WithDefaults() FlowFactorConfig {
	d := DefaultFlowFactorConfig()
	out := f
	if strings.TrimSpace(out.RatioBasis) == "" {
		out.RatioBasis = d.RatioBasis
	}
	out.Buy5DScales = mergeScales(d.Buy5DScales, out.Buy5DScales)
	out.Buy5DBands = mergeScales(d.Buy5DBands, out.Buy5DBands)
	out.BuyTodayScales = mergeScales(d.BuyTodayScales, out.BuyTodayScales)
	out.BuyTodayBands = mergeScales(d.BuyTodayBands, out.BuyTodayBands)
	out.SellScales = mergeScales(d.SellScales, out.SellScales)
	out.SellBands = mergeScales(d.SellBands, out.SellBands)
	return out
}

// FlowFactorOn 买入侧总开关（nil=开）。
// English: buy-side master switch (nil = on).
func (s SizingConfig) FlowFactorOn() bool {
	return s.FlowFactor.Enabled == nil || *s.FlowFactor.Enabled
}

// FlowSellOn 卖出侧开关（nil=开）。
// English: sell-side switch (nil = on).
func (s SizingConfig) FlowSellOn() bool {
	return s.FlowFactor.SellEnabled == nil || *s.FlowFactor.SellEnabled
}

// FlowRatioBasisOf 归一化分母口径（空/未识别按 float_mcap）。
// English: normalization basis; empty or unrecognized → float_mcap.
func (s SizingConfig) FlowRatioBasisOf() string {
	if strings.EqualFold(strings.TrimSpace(s.FlowFactor.RatioBasis), "amount") {
		return "amount"
	}
	return "float_mcap"
}

// EvoConfig §SIZING-EVO 自动回测研究配置（账号级，挂在 SizingConfig.Evo）。
// 回路独立性铁律见方案 §2.7.0：sizing_evo 零 import internal/research。
// English: auto-backtest-research config for the sizing synthesizer (independent loop).
type EvoConfig struct {
	// Enabled 进化引擎开关（nil=**关**）。与因子默认开相反：evo 是新增常驻行为，
	// 出厂默认不动，必须 owner 显式开（§2.11：首期还要求 auto_promote=false）。
	// English: evolution engine switch (nil = OFF). Unlike the factors, this new always-on
	// behavior defaults off and must be enabled explicitly.
	Enabled *bool `json:"enabled,omitempty"`
	// AutoPromote 达标候选自动晋升（nil=false：首期只产候选+留痕，owner 审阅后开）。
	// English: auto-promote passing candidates (nil = false: candidates + audit trail only).
	AutoPromote *bool `json:"auto_promote,omitempty"`
	// Schedule 运行节奏：weekly（默认，周度 L1 + 夜间 L2/降级）。
	// English: run cadence — weekly (L1 weekly + nightly L2/demote).
	Schedule string `json:"schedule,omitempty"`
	// MinSamples L1 寻优最小样本数（默认 200；不足不动配置=冷启动纪律）。
	// English: minimum L1 sample count (default 200; below it nothing changes).
	MinSamples int `json:"min_samples,omitempty"`
	// ObservationDays 灰度观测期（交易日，默认 20，与 research lifecycle 同口径）。
	// English: grayscale observation window in trading days (default 20).
	ObservationDays int `json:"observation_days,omitempty"`
	// SearchBounds 每参数搜索围栏覆盖（键=参数路径如 "flow_factor.buy_5d_scales.strong_in"，
	// 值=[lo,hi] 二元组）。**缺键=用 sizing_evo 内置默认围栏**（§2.7.1 硬围栏），配置只能
	// 在默认围栏内收紧、不能放宽——防"进化把系统改坏"的最后一道闸在代码里，不在配置里。
	// English: per-parameter search-bound overrides (missing keys use the built-in rails).
	SearchBounds map[string][]float64 `json:"search_bounds,omitempty"`
}

// DefaultEvoConfig 出厂默认：关、不自动晋升、周度、200 样本、20 观测日。
// English: factory defaults — off, no auto-promote, weekly, 200 samples, 20 observation days.
func DefaultEvoConfig() EvoConfig {
	return EvoConfig{
		Schedule:        "weekly",
		MinSamples:      200,
		ObservationDays: 20,
	}
}

// WithDefaults 补默认（指针键不碰：nil 态由访问器解析）。
// English: fills defaults; pointer keys are left to their accessors.
func (e EvoConfig) WithDefaults() EvoConfig {
	d := DefaultEvoConfig()
	out := e
	if strings.TrimSpace(out.Schedule) == "" {
		out.Schedule = d.Schedule
	}
	if out.MinSamples <= 0 {
		out.MinSamples = d.MinSamples
	}
	if out.ObservationDays <= 0 {
		out.ObservationDays = d.ObservationDays
	}
	return out
}

// EvoOn 进化引擎是否开启（nil=关——显式开才跑）。
// English: whether the evolution engine runs (nil = off; explicit opt-in).
func (s SizingConfig) EvoOn() bool {
	return s.Evo.Enabled != nil && *s.Evo.Enabled
}

// EvoAutoPromoteOn 是否自动晋升（nil=false）。
// English: whether passing candidates auto-promote (nil = false).
func (s SizingConfig) EvoAutoPromoteOn() bool {
	return s.Evo.AutoPromote != nil && *s.Evo.AutoPromote
}
