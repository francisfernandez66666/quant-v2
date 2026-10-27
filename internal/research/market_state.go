// market_state.go — 市场状态机（§SIGNAL_EDGE_ENHANCEMENT_PLAN P2.3）。
// 由涨停家数/连板高度/炸板率/上涨家数比/指数斜率多信号投票判定 牛/震荡/熊 三态，
// 并给出仓位档位上限（MaxPosPct）。带迟滞（分歧票时沿用旧态）与最小停留期
// （MinStayDays 内不切换），避免频繁跳变。
// 纯逻辑自包含（不依赖数据源），引擎侧 Enhance.MarketState 门控接入。
// English: market-state machine (P2.3). Multi-signal vote (limit-up count / board height / break
// rate / up-ratio / index slope) classifies the tape as bull/range/bear, mapping to max position
// caps. Hysteresis (keep prior on a split vote) and a minimum stay (no switch within MinStayDays)
// dampen flicker. Self-contained pure logic; gated by the engine's Enhance.MarketState.

package research

import (
	"math"
	"time"
)

// MarketState 市场状态。English: market regime.
type MarketState string

// 三态常量。
const (
	StateBull  MarketState = "bull"  // 牛市：进攻，高仓位档
	StateRange MarketState = "range" // 震荡：中性档
	StateBear  MarketState = "bear"  // 熊市：防守，低/零仓位档
)

// StateSnapshot 状态判定的输入快照（由引擎从行情/情绪数据装配）。
type StateSnapshot struct {
	LimitUpCount   int     // 当日涨停家数
	LadderHeight   int     // 最高连板高度
	BreakRate      float64 // 炸板率（%）
	UpRatio        float64 // 上涨家数占比（0~1）
	IndexMA20Slope float64 // 指数 MA20 斜率（>0 上行，<0 下行）
	IndexMA60Slope float64 // 指数 MA60 斜率
	// IndexChangePct 当日指数涨跌幅（%，NaN=弃权）。§BUG-STATE（2026-10-27）：状态机此前**没有**
	// 任何一个"当日价格"输入——只有日线 MA 斜率与情绪结构，于是"大盘今天跌 0.5%、下午急杀"
	// 这件事在研判里完全不可见。这一票就是补它，且必须是指数（不是同码个股）。
	IndexChangePct float64
}

// StateConfig 状态机阈值（可配）；零值字段回退默认。
type StateConfig struct {
	BullLimit   int     // 涨停家数 ≥ 此值 → 牛信号
	BearLimit   int     // 涨停家数 ≤ 此值 → 熊信号
	BullLadder  int     // 连板高度 ≥ 此值 → 牛信号
	BearLadder  int     // 连板高度 ≤ 此值 → 熊信号
	BullBreak   float64 // 炸板率 ≤ 此值 → 牛信号
	BearBreak   float64 // 炸板率 ≥ 此值 → 熊信号
	BullUpRatio float64 // 上涨占比 ≥ 此值 → 牛信号
	BearUpRatio float64 // 上涨占比 ≤ 此值 → 熊信号
	// BullIndexPct / BearIndexPct 当日指数涨跌幅 ≥/≤ 此值 → 牛/熊信号（默认 +1.0 / −1.0，中间弃权）。
	BullIndexPct float64
	BearIndexPct float64
	// MinVotes 有效投票数下限（默认 4/6）。低于它时结论**降级为 range**——
	// 这是 §BUG-STATE 的核心安全阀：2026-10-27 当天广度与指数日线斜率都没取到，
	// 只剩"连板高度 + 炸板率"两票就给出了 bull 60% 的仓位档，界面上还看不出来。
	MinVotes int
	// PlungePct 日内急杀线（默认 −0.8%）：当日指数跌幅达此值时，**最多按 range 定档**。
	// 与 T 的 market_plunge 共用同一个指数读数（同源，避免两套尺子）。
	PlungePct float64
	// MaxPosPct 各态仓位档位上限。默认 bull 0.60 / range 0.35 / bear 0.15。
	MaxPosPct map[MarketState]float64
	// MinStayDays 最短停留交易日：切换后须经过此天数才允许再次切换（默认 3）。
	MinStayDays int
}

// defaultStateConfig 返回默认阈值与档位。
func defaultStateConfig() StateConfig {
	return StateConfig{
		BullLimit: 80, BearLimit: 25,
		BullLadder: 5, BearLadder: 2,
		BullBreak: 30, BearBreak: 50,
		BullUpRatio: 0.60, BearUpRatio: 0.30,
		BullIndexPct: 1.0, BearIndexPct: -1.0,
		MinVotes: 4, PlungePct: -0.8,
		MaxPosPct:   map[MarketState]float64{StateBull: 0.60, StateRange: 0.35, StateBear: 0.15},
		MinStayDays: 3,
	}
}

// norm 用默认值补齐零值字段，返回生效配置。
func (s StateConfig) norm() StateConfig {
	d := defaultStateConfig()
	if s.BullLimit == 0 {
		s.BullLimit = d.BullLimit
	}
	if s.BearLimit == 0 {
		s.BearLimit = d.BearLimit
	}
	if s.BullLadder == 0 {
		s.BullLadder = d.BullLadder
	}
	if s.BearLadder == 0 {
		s.BearLadder = d.BearLadder
	}
	if s.BullBreak == 0 {
		s.BullBreak = d.BullBreak
	}
	if s.BearBreak == 0 {
		s.BearBreak = d.BearBreak
	}
	if s.BullUpRatio == 0 {
		s.BullUpRatio = d.BullUpRatio
	}
	if s.BearUpRatio == 0 {
		s.BearUpRatio = d.BearUpRatio
	}
	// 指数票与安全阀：0 视为未配置（这两组默认值都不是 0，所以零值判定安全）。
	if s.BullIndexPct == 0 {
		s.BullIndexPct = d.BullIndexPct
	}
	if s.BearIndexPct == 0 {
		s.BearIndexPct = d.BearIndexPct
	}
	if s.MinVotes <= 0 {
		s.MinVotes = d.MinVotes
	}
	if s.PlungePct == 0 {
		s.PlungePct = d.PlungePct
	}
	if len(s.MaxPosPct) == 0 {
		s.MaxPosPct = d.MaxPosPct
	} else {
		for k, v := range d.MaxPosPct {
			if _, ok := s.MaxPosPct[k]; !ok {
				s.MaxPosPct[k] = v
			}
		}
	}
	if s.MinStayDays <= 0 {
		s.MinStayDays = d.MinStayDays
	}
	return s
}

// voteOne 单信号投票：返回任一三态，nil 表示该信号弃权。
func voteBullBear(bullCond, bearCond bool) *MarketState {
	if bullCond {
		s := StateBull
		return &s
	}
	if bearCond {
		s := StateBear
		return &s
	}
	return nil
}

// Classify 多信号投票判定市场状态：加权计数（牛/熊/弃权），无绝对多数时返回 range。
// 纯函数（无记忆）；迟滞/停留由 StateTracker 施加。
// English: multi-signal vote; weighted counts with abstention; no absolute majority → range.
func Classify(sn StateSnapshot, cfg StateConfig) MarketState {
	st, _, _ := ClassifyWithQuality(sn, cfg)
	return st
}

// ClassifyWithQuality 同 Classify，并返回 (有效投票数, 总票数)，供"研判质量"读数与安全阀使用。
// English: same as Classify plus (present votes, total votes) for the quality readout and the gate.
func ClassifyWithQuality(sn StateSnapshot, cfg StateConfig) (MarketState, int, int) {
	cfg = cfg.norm()
	var bull, bear int
	signals := 0
	if v := voteBullBear(sn.LimitUpCount >= cfg.BullLimit, sn.LimitUpCount <= cfg.BearLimit); v != nil {
		signals++
		if *v == StateBull {
			bull++
		} else {
			bear++
		}
	}
	if v := voteBullBear(sn.LadderHeight >= cfg.BullLadder, sn.LadderHeight <= cfg.BearLadder); v != nil {
		signals++
		if *v == StateBull {
			bull++
		} else {
			bear++
		}
	}
	if v := voteBullBear(sn.BreakRate <= cfg.BullBreak, sn.BreakRate >= cfg.BearBreak); v != nil {
		signals++
		if *v == StateBull {
			bull++
		} else {
			bear++
		}
	}
	if v := voteBullBear(sn.UpRatio >= cfg.BullUpRatio, sn.UpRatio <= cfg.BearUpRatio); v != nil {
		signals++
		if *v == StateBull {
			bull++
		} else {
			bear++
		}
	}
	if v := voteBullBear(sn.IndexMA20Slope > 0 && sn.IndexMA60Slope > 0, sn.IndexMA20Slope < 0 && sn.IndexMA60Slope < 0); v != nil {
		signals++
		if *v == StateBull {
			bull++
		} else {
			bear++
		}
	}
	// 第六票：当日指数涨跌幅（NaN 弃权）。用指数而不是同码个股——`000001` 在个股链里是平安银行。
	if v := voteBullBear(sn.IndexChangePct >= cfg.BullIndexPct, sn.IndexChangePct <= cfg.BearIndexPct); v != nil {
		signals++
		if *v == StateBull {
			bull++
		} else {
			bear++
		}
	}
	const totalVotes = 6
	if signals == 0 {
		return StateRange, 0, totalVotes
	}
	if bull > bear && bull > 0 {
		return StateBull, signals, totalVotes
	}
	if bear > bull && bear > 0 {
		return StateBear, signals, totalVotes
	}
	return StateRange, signals, totalVotes
}

// StateTracker 带滞回与最小停留期的状态跟踪器。
type StateTracker struct {
	cfg         StateConfig
	current     MarketState
	changedAt   time.Time
	lastPresent int // 最近一帧有效投票数（研判质量读数）
	lastTotal   int // 最近一帧总票数
}

// NewStateTracker 新建跟踪器（初始态 range）。English: new tracker starting at range.
func NewStateTracker(cfg StateConfig) *StateTracker {
	cfg = cfg.norm()
	return &StateTracker{cfg: cfg, current: StateRange}
}

// Quality 最近一帧的投票质量：(有效票, 总票)。English: (present votes, total votes) of the last frame.
func (t *StateTracker) Quality() (int, int) {
	return t.lastPresent, t.lastTotal
}

// Current 当前状态。English: current state.
func (t *StateTracker) Current() MarketState {
	return t.current
}

// MaxPosPct 当前状态仓位档位上限（0~1）。English: max position fraction for the current state.
func (t *StateTracker) MaxPosPct() float64 {
	return t.cfg.MaxPosPct[t.current]
}

// Observe 喂入一帧快照，返回当前（可能已切换的）状态。
// 切换规则：新状态须与当前不同，且自上次切换已过 MinStayDays 交易日；
// 同态或未到停留期保持原态（迟滞）。
// English: feeds one snapshot frame and returns the (possibly switched) state. A switch only
// happens when the classified state differs and MinStayDays have passed since the last change.
func (t *StateTracker) Observe(sn StateSnapshot, now time.Time) MarketState {
	want, present, total := ClassifyWithQuality(sn, t.cfg)
	t.lastPresent, t.lastTotal = present, total
	// 安全阀 ①：输入不全（无效票 < MinVotes）⇒ 降级为 range。
	// 宁可少给仓位，也不用两票情绪指标冒充"牛市"——2026-10-27 就是这么给出 60% 档的。
	if present < t.cfg.MinVotes && want != StateRange {
		want = StateRange
	}
	// 安全阀 ②：日内急杀（当日指数跌幅达线）⇒ 最多 range，不许按牛定档。
	if want == StateBull && !math.IsNaN(sn.IndexChangePct) && sn.IndexChangePct <= t.cfg.PlungePct {
		want = StateRange
	}
	if want == t.current {
		return t.current
	}
	if !t.changedAt.IsZero() && now.Sub(t.changedAt).Hours() < float64(t.cfg.MinStayDays)*24 {
		return t.current // 停留期未满，沿用旧态
	}
	t.current = want
	t.changedAt = now
	return t.current
}
