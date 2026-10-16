// regime.go — §DYN-SIZE 动态仓位合成所需的"市场环境快照"类型（docs/DYN_SIZE_FINAL_PLAN_20261016.md §2.2）。
//
// 定位：signalctl 是纯逻辑组件（无 IO、无账本引用，见 signalctl.go 包注释），因此它无法自己去
// 读指数行情、宏观日历、交易日历或账户账本。这些读数由**流程引擎每轮装配一次**，随 Policy 注入，
// 本文件只声明快照的形状与"缺失=弃权"的语义约定。
//
// 关键不变量（与方案 §5 测试锁一一对应）：
//   - 任何"不可得"的读数必须保持零值/NaN/空串（弃权），**绝不**伪造成 0 或中性值——
//     缺失被当成"普通日/无风险"是这套合成最危险的失败模式；
//   - 快照是每轮一次性装配的值拷贝，signalctl 侧只读不写（并发安全）；
//   - 交易日距离一律按**交易日**（跨周末/长假不能用自然日，否则国庆 8 天会被算成 8 个自然日）。
//
// English: §DYN-SIZE — the market-regime snapshot the pure signalctl component cannot fetch itself
// (index quote, macro calendar, trading calendar, account ledger). The flow engine assembles it once
// per round and injects it through Policy. Missing readings stay zero/NaN/empty = abstain, never a
// fabricated neutral.
package signalctl

import (
	"time"

	"quant-trading-v2/internal/config"
)

// Regime 单轮市场环境快照（引擎装配，signalctl 只读）。
//
// English: a per-round market-regime snapshot assembled by the engine and read-only inside signalctl.
type Regime struct {
	// AsOf 快照装配时刻（北京时间），供留痕与陈旧判定。
	AsOf time.Time `json:"as_of"`

	// EmotionPhase 情绪相位：冰点/启动/发酵/高潮/背离/退潮；空串=状态机未产出（弃权）。
	// 冰点走**硬拦**（不进权重连乘）。
	EmotionPhase string `json:"emotion_phase,omitempty"`
	// MarketState 市场状态机结果：bull/range/bear；空串=状态机关闭（该因子弃权）。
	MarketState string `json:"market_state,omitempty"`
	// MaxPosPct 状态机建议最大仓位比例（0=未启用）。**不参与定权连乘**（已由 MarketState 档位覆盖，
	// 二次相乘属"同一风险双罚"，违反方案 §5 测试锁）；仅作 shadow 读数与既有 auto 谨慎层消费。
	MaxPosPct float64 `json:"max_pos_pct,omitempty"`

	// RiskTier 合成风险档：Red/Yellow/""（空=无档）。Red 走**硬拦**。
	RiskTier string `json:"risk_tier,omitempty"`
	// RiskReasons 风险档触发原因（留痕用）。
	RiskReasons []string `json:"risk_reasons,omitempty"`

	// IndexChangePct 大盘最近一个交易日涨跌幅（%，NaN=弃权）。
	IndexChangePct float64 `json:"index_change_pct,omitempty"`
	// IndexChangeDay 该读数所属交易日（YYYYMMDD，防陈旧复用：与 AsOf 同日才可信）。
	IndexChangeDay string `json:"index_change_day,omitempty"`
	// IndexTrendBroken 大盘趋势是否破位（创 20 日新低 / 收盘跌破 MA20 / 连续≥3日阴跌）。
	// 止损两态判别用：false+单日大跌=单日冲击（放宽），true=趋势下跌（收紧）。
	IndexTrendBroken bool `json:"index_trend_broken,omitempty"`

	// BreadthUpRatio 上涨占比（0~1，NaN=弃权）。
	BreadthUpRatio float64 `json:"breadth_up_ratio,omitempty"`

	// MacroEvents 影响期内 + 前瞻窗内的宏观事件（已解析为交易日距离）。
	MacroEvents []MacroEventView `json:"macro_events,omitempty"`

	// Holiday 节前窗口读数（未加载日历 → CalendarLoaded=false → 因子弃权）。
	Holiday HolidayWindow `json:"holiday"`

	// Account 账户级读数（StoplossGuard / MaxDrawdown / 总仓位上限的数据面）。
	Account AccountState `json:"account"`

	// SizeCfg 动态仓位合成配置（引擎侧已 WithDefaults 补齐；signalctl 直接读平字段）。
	SizeCfg config.SizingConfig `json:"size_cfg"`

	// StopLossOffsetPct §DYN-SIZE P5 当日大盘两态偏移（百分点）：>0 放宽 / <0 收紧 / 0 不适用。
	// 与 BaseStopLossPct 一起驱动**风险预算守恒**上限因子（放宽止损必须同比例缩仓）。
	// English: §DYN-SIZE P5 two-state market stop offset (pt); with BaseStopLossPct it drives the
	// risk-budget-conservation cap factor.
	StopLossOffsetPct float64 `json:"stop_loss_offset_pct,omitempty"`
	// BaseStopLossPct 风险预算守恒的基准止损（配置的纪律止损，默认 6）。<=0 = 不可得 → 该上限弃权。
	// English: the base stop used by the risk-budget cap; <=0 abstains.
	BaseStopLossPct float64 `json:"base_stop_loss_pct,omitempty"`
}

// MacroEventView 单个宏观事件在本轮快照里的视图（已按交易日解析距离）。
// English: one macro event as seen by this round's snapshot, with trading-day distance resolved.
type MacroEventView struct {
	// Level 事件类型：war/contract/fomc/pce/cpi/nfp/other。
	Level string `json:"level,omitempty"`
	// Title 事件标题（留痕用）。
	Title string `json:"title,omitempty"`
	// Impact 影响程度：high/medium/low。
	Impact string `json:"impact,omitempty"`
	// DaysToEvent 距事件日的**交易日**数（0=事件当日；负值=已过事件日仍在影响期）。
	DaysToEvent int `json:"days_to_event"`
	// InWindow 是否处于事件影响期（事件日前后 Duration 天内）。
	InWindow bool `json:"in_window,omitempty"`
}

// HolidayWindow 节前窗口读数（由 data 包探针计算，引擎映射进来）。
//
// English: pre-holiday window reading (computed by the data-package probe, mapped in by the engine).
type HolidayWindow struct {
	// DaysToNextOpen 距下一开市日的自然日跨度（0=今日即交易日且明日仍开市）。
	DaysToNextOpen int `json:"days_to_next_open,omitempty"`
	// ClosedRun 下一段连续休市天数：2=周末，≥3=小长假，≥5=长假。
	ClosedRun int `json:"closed_run,omitempty"`
	// PreHolidayN 节前第 N 个交易日（1=节前最后一日；0=不在节前窗口）。
	PreHolidayN int `json:"pre_holiday_n,omitempty"`
	// CalendarLoaded 交易日历是否已加载。false → 节前因子**弃权**（不 fail-open 当普通日）。
	CalendarLoaded bool `json:"calendar_loaded,omitempty"`
}

// AccountState 账户级读数（熔断/上限的数据面；不可得时保持零值=弃权）。
// English: account-level readings for the circuit breakers and the total-position cap.
type AccountState struct {
	// WindowStopLosses 回看窗内止损次数（StoplossGuard 输入）。
	WindowStopLosses int `json:"window_stop_losses,omitempty"`
	// EquityDrawdown 账户权益峰谷回撤比例（0~1，0=未启用/无回撤）。
	EquityDrawdown float64 `json:"equity_drawdown,omitempty"`
	// TotalPosPct 当前持仓市值 / 总资产（0~1，0=不可得→总仓位上限因子弃权）。
	TotalPosPct float64 `json:"total_pos_pct,omitempty"`
}
