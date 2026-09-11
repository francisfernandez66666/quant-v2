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

import "time"

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
	LimitUpCount  int     // 当日涨停家数
	LadderHeight  int     // 最高连板高度
	BreakRate     float64 // 炸板率（%）
	UpRatio       float64 // 上涨家数占比（0~1）
	IndexMA20Slope float64 // 指数 MA20 斜率（>0 上行，<0 下行）
	IndexMA60Slope float64 // 指数 MA60 斜率
}

// StateConfig 状态机阈值（可配）；零值字段回退默认。
type StateConfig struct {
	BullLimit  int     // 涨停家数 ≥ 此值 → 牛信号
	BearLimit  int     // 涨停家数 ≤ 此值 → 熊信号
	BullLadder int     // 连板高度 ≥ 此值 → 牛信号
	BearLadder int     // 连板高度 ≤ 此值 → 熊信号
	BullBreak  float64 // 炸板率 ≤ 此值 → 牛信号
	BearBreak  float64 // 炸板率 ≥ 此值 → 熊信号
	BullUpRatio float64 // 上涨占比 ≥ 此值 → 牛信号
	BearUpRatio float64 // 上涨占比 ≤ 此值 → 熊信号
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
	if signals == 0 {
		return StateRange
	}
	if bull > bear && bull > 0 {
		return StateBull
	}
	if bear > bull && bear > 0 {
		return StateBear
	}
	return StateRange
}

// StateTracker 带滞回与最小停留期的状态跟踪器。
type StateTracker struct {
	cfg       StateConfig
	current   MarketState
	changedAt time.Time
}

// NewStateTracker 新建跟踪器（初始态 range）。English: new tracker starting at range.
func NewStateTracker(cfg StateConfig) *StateTracker {
	cfg = cfg.norm()
	return &StateTracker{cfg: cfg, current: StateRange}
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
	want := Classify(sn, t.cfg)
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