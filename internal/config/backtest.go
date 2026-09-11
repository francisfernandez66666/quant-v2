// backtest.go 回测引擎增强配置（docs/BACKTEST_AUTO_ENHANCE_PLAN.md 模块 A0）。
//
// 持久化走研究库 backtest_settings 单行 JSON（不挂 config.json/Rules——研究子进程只消费
// 任务 payload，配置管线断链结论见设计文档 §〇）；入队时注入 payload.backtest，
// runtask 反序列化到 btreplay.Options.Backtest。
//
// 向后兼容契约：nil 或 Enabled=false = 行为与增强前完全一致（固定 5bp、无流动性门控、
// 无 Pareto）；Enabled=true 时缺失字段由 FillDefaults 回填新默认（BaseBps=3.0 等）。
// English: backtest enhancement config — persisted in the research DB's single-row
// backtest_settings table, injected into task payloads, and decoded into btreplay options.
// nil/disabled keeps the legacy behavior byte-for-byte.
package config

// BacktestConfig 回测引擎增强配置总览。
type BacktestConfig struct {
	Enabled        bool    `json:"enabled"`          // 增强总开关（false=旧行为）
	OrderValueYuan float64 `json:"order_value_yuan"` // 单笔名义额（元）；0=兜底 10000（注入端已解析 paper.fixed_amount）
	// PaperModelSlippageBps 模拟盘撮合自身的模型滑点（bp）：自动校准扣减项，防双重计费。
	// 注入端从真实配置解析；缺省 5（paper.DefaultConfig 同源）。
	PaperModelSlippageBps float64           `json:"paper_model_slippage_bps"`
	Slippage              SlippageConfig    `json:"slippage"`
	Liquidity             LiquidityConfig   `json:"liquidity"`
	Pareto                ParetoConfig      `json:"pareto"`
	WalkForward           WalkForwardConfig `json:"walk_forward"` // 类型先行定义，A1 轮消费
}

// SlippageConfig 滑点配置（自动校准优先，分档表兜底）。
type SlippageConfig struct {
	BaseBps         float64      `json:"base_bps"`          // 基准滑点兜底值（enabled 且校准未生效时用；默认 3.0）
	VolumeTiers     []VolumeTier `json:"volume_tiers"`      // 低流动性分档（空=内置默认表）
	SizeTiers       []SizeTier   `json:"size_tiers"`        // 大单冲击分档（空=内置默认表）
	Asymmetric      bool         `json:"asymmetric"`        // 买卖非对称
	BuyExtraBps     float64      `json:"buy_extra_bps"`     // 买入额外滑点兜底（默认 1.0）
	AutoCalibrate   bool         `json:"auto_calibrate"`    // 主路线：paper_trades 实测滑点校准 BaseBps/BuyExtraBps
	CalibMinSample  int          `json:"calib_min_sample"`  // 单方向最少样本（默认 30，不足回退配置值）
	CalibWindowDays int          `json:"calib_window_days"` // 校准回看天数（默认 90）
}

// VolumeTier 低流动性分档：信号日滑窗日均成交额 ≤ MaxVolumeWan（万元）时 +ExtraBps。
type VolumeTier struct {
	MaxVolumeWan float64 `json:"max_volume_wan"`
	ExtraBps     float64 `json:"extra_bps"`
}

// SizeTier 大单冲击分档：名义额占日均成交额 ≥ MinRatio 时 +ExtraBps（元/元 无量纲）。
type SizeTier struct {
	MinRatio float64 `json:"min_ratio"`
	ExtraBps float64 `json:"extra_bps"`
}

// LiquidityConfig 流动性约束配置（Enabled=false 时全部子项跳过）。
type LiquidityConfig struct {
	Enabled                 bool    `json:"enabled"`
	LimitUpOpenableExtraBps float64 `json:"limit_up_openable_extra_bps"` // 涨停打开额外滑点（默认 10）
	LimitDownSealedExtraBps float64 `json:"limit_down_sealed_extra_bps"` // 跌停打开日额外滑点（默认 15）
	LimitDownSealedEnabled  bool    `json:"limit_down_sealed_enabled"`   // 跌停封死不可卖开关
	PartialFillEnabled      bool    `json:"partial_fill_enabled"`        // 部分成交开关
	FillRateMin             float64 `json:"fill_rate_min"`               // 最小成交比例（默认 0.3）
}

// ParetoConfig 多目标寻优配置（Enabled=false 时 SWEEP_JSON 不输出 pareto 段）。
type ParetoConfig struct {
	Enabled         bool    `json:"enabled"`
	MinWinRate      float64 `json:"min_win_rate"`      // 硬门槛（默认 30）
	MinProfitFactor float64 `json:"min_profit_factor"` // 默认 1.0
	MinSharpe       float64 `json:"min_sharpe"`        // 默认 0.5
	MinCalmar       float64 `json:"min_calmar"`        // 默认 0.5
	MaxFrontPoints  int     `json:"max_front_points"`  // 前沿最大点数（默认 50）
}

// WalkForwardConfig 滚动优化配置（A1 轮启用；本轮仅先行定义）。
type WalkForwardConfig struct {
	Enabled       bool `json:"enabled"`
	TrainDays     int  `json:"train_days"`      // 训练窗交易日数（默认 244≈1年）
	ValidateDays  int  `json:"validate_days"`   // 验证窗交易日数（默认 62≈3个月）
	StepDays      int  `json:"step_days"`       // 步进交易日数（默认 62≈3个月）
	CandidateTopN int  `json:"candidate_top_n"` // 两阶段法：全时段网格取前 N 候选（默认 30）
}

// 增强启用后的新默认值（旧行为由 Enabled 门保证，enabled 下缺省一律走这里，A0.3 定稿）。
const (
	BacktestDefaultBaseBps         = 3.0   // 基准滑点兜底（bp）
	BacktestDefaultBuyExtraBps     = 1.0   // 买入非对称加项兜底（bp）
	BacktestDefaultOrderValueYuan  = 10000 // 单笔名义额兜底（元，与 paper.fixed_amount 同源）
	BacktestDefaultPaperSlipBps    = 5.0   // 模拟盘模型滑点缺省（bp，paper.DefaultConfig 同源）
	BacktestDefaultCalibMinSample  = 30    // 校准单方向最少样本
	BacktestDefaultCalibWindowDays = 90    // 校准回看天数
	BacktestDefaultLimitUpExtra    = 10.0  // 涨停打开额外滑点（bp）
	BacktestDefaultLimitDownExtra  = 15.0  // 跌停打开日额外滑点（bp）
	BacktestDefaultFillRateMin     = 0.3   // 最小成交比例
	BacktestDefaultMinWinRate      = 30.0  // Pareto 硬门槛：胜率%
	BacktestDefaultMinProfitFactor = 1.0   // Pareto 硬门槛：盈亏比
	BacktestDefaultMinSharpe       = 0.5   // Pareto 硬门槛：夏普
	BacktestDefaultMinCalmar       = 0.5   // Pareto 硬门槛：卡玛
	BacktestDefaultMaxFrontPoints  = 50    // 前沿最大点数
)

// DefaultVolumeTiers 低流动性分档内置默认表（MaxVolumeWan 升序，A.3 定稿）。
// English: built-in low-liquidity tiers (ascending by daily avg turnover in 10k CNY).
func DefaultVolumeTiers() []VolumeTier {
	return []VolumeTier{
		{MaxVolumeWan: 100, ExtraBps: 15},
		{MaxVolumeWan: 500, ExtraBps: 12},
		{MaxVolumeWan: 2000, ExtraBps: 8},
		{MaxVolumeWan: 5000, ExtraBps: 3},
	}
}

// DefaultSizeTiers 大单冲击分档内置默认表（MinRatio 降序，A.3 定稿）。
// English: built-in market-impact tiers (descending by order-value/turnover ratio).
func DefaultSizeTiers() []SizeTier {
	return []SizeTier{
		{MinRatio: 0.02, ExtraBps: 10},
		{MinRatio: 0.01, ExtraBps: 8},
		{MinRatio: 0.005, ExtraBps: 5},
		{MinRatio: 0.001, ExtraBps: 3},
	}
}

// FillDefaults 在 Enabled=true 时回填缺失字段的内置默认（A0.3）。幂等，可重复调用。
// 注意：布尔零值无法区分"未填"与"显式关闭"，非对称/自动校准等子开关以 JSON 显式配置为准；
// 数值零值视为未填回填默认。
// English: backfills zero-valued fields with built-in defaults once the master switch is on.
func (c *BacktestConfig) FillDefaults() {
	if c == nil || !c.Enabled {
		return
	}
	// 名义额与模拟盘同源：注入端解析 rules.paper.fixed_amount；此处仅兜底
	if c.OrderValueYuan <= 0 {
		c.OrderValueYuan = BacktestDefaultOrderValueYuan
	}
	if c.PaperModelSlippageBps <= 0 {
		c.PaperModelSlippageBps = BacktestDefaultPaperSlipBps
	}
	s := &c.Slippage
	if s.BaseBps <= 0 {
		s.BaseBps = BacktestDefaultBaseBps
	}
	if s.BuyExtraBps <= 0 {
		s.BuyExtraBps = BacktestDefaultBuyExtraBps
	}
	if len(s.VolumeTiers) == 0 {
		s.VolumeTiers = DefaultVolumeTiers()
	}
	if len(s.SizeTiers) == 0 {
		s.SizeTiers = DefaultSizeTiers()
	}
	if s.CalibMinSample <= 0 {
		s.CalibMinSample = BacktestDefaultCalibMinSample
	}
	if s.CalibWindowDays <= 0 {
		s.CalibWindowDays = BacktestDefaultCalibWindowDays
	}
	l := &c.Liquidity
	if l.LimitUpOpenableExtraBps <= 0 {
		l.LimitUpOpenableExtraBps = BacktestDefaultLimitUpExtra
	}
	if l.LimitDownSealedExtraBps <= 0 {
		l.LimitDownSealedExtraBps = BacktestDefaultLimitDownExtra
	}
	if l.FillRateMin <= 0 {
		l.FillRateMin = BacktestDefaultFillRateMin
	}
	p := &c.Pareto
	if p.MinWinRate <= 0 {
		p.MinWinRate = BacktestDefaultMinWinRate
	}
	if p.MinProfitFactor <= 0 {
		p.MinProfitFactor = BacktestDefaultMinProfitFactor
	}
	if p.MinSharpe <= 0 {
		p.MinSharpe = BacktestDefaultMinSharpe
	}
	if p.MinCalmar <= 0 {
		p.MinCalmar = BacktestDefaultMinCalmar
	}
	if p.MaxFrontPoints <= 0 {
		p.MaxFrontPoints = BacktestDefaultMaxFrontPoints
	}
	w := &c.WalkForward
	if w.TrainDays <= 0 {
		w.TrainDays = 244
	}
	if w.ValidateDays <= 0 {
		w.ValidateDays = 62
	}
	if w.StepDays <= 0 {
		w.StepDays = 62
	}
	if w.CandidateTopN <= 0 {
		w.CandidateTopN = 30
	}
}

// Active 报告增强是否生效（nil 安全）。
// English: nil-safe "enhancement active" check used by the engine short-circuit.
func (c *BacktestConfig) Active() bool { return c != nil && c.Enabled }
