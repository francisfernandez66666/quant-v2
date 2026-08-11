// Package combat_agent 战法引擎：8a/8b 处理信号个股打分，以及持仓/自选的持续打分。
// Package combat_agent: scoring for 8a/8b signal stocks, plus continuous scoring of holdings/watchlist.
// nwave.go 实现 N 形"一突/二突"日内状态机：跨 5s 打分周期跟踪每只股票的突破节奏。
// nwave.go implements the N-shape "first breakout / second breakout" intraday state machine, tracking
// each stock's breakout rhythm across the 5s scoring cycles.
// 一突（左侧）：价格突破前日高点×1.005 且量比≥1.8 → 立即打标买入（≥P2）。
// First breakout (left): price breaks above yesterday's high x 1.005 with a volume ratio >= 1.8 -> an
// immediate buy candidate (>= P2).
// 二突（右侧）：一突破位后经历回调（跌破峰价×0.997），再次放量重破前高 → 最强确认（P1）。
// Second breakout (right): after the first breakout, a pullback (below the peak x 0.997) and then a
// renewed breakout above the prior high -> the strongest confirmation (P1).
// 状态按交易日隔离，跨日自动重置；状态仅在 N 形候选被评分时推进。
// State is isolated per trading day and reset across days; it only advances when an N-shape candidate is scored.
package combat_agent

import (
	"math"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// waveState 单只股票的日内突破状态。
// waveState holds the intraday breakout state of a single stock.
//   - day: trading day (basis for cross-day reset)
//   - prevHigh: yesterday's high
//   - armed: whether the first breakout has already occurred
//   - highAfterArm: intraday high since the first breakout (the peak)
//   - dipped: whether a pullback happened (below 0.997x the peak)
type waveState struct {
	day          string  // 交易日（跨日重置依据）
	prevHigh     float64 // 前日最高价
	armed        bool    // 是否已发生一突破位
	highAfterArm float64 // 一突以来的盘中最高价（峰价）
	dipped       bool    // 是否已从峰价回调（跌破 0.997 倍峰价）
}

// WaveTracker N 形一突/二突状态机容器，按股票代码维护日内状态。
// WaveTracker is the N-shape first/second-breakout state machine container, keeping per-code intraday state.
type WaveTracker struct {
	mu     sync.Mutex
	states map[string]*waveState
}

// NewWaveTracker 创建状态机容器。
// NewWaveTracker creates a new state-machine container.
func NewWaveTracker() *WaveTracker {
	return &WaveTracker{states: make(map[string]*waveState)}
}

// firstBreakRatio 一突量比阈值（与 n_shape 左侧信号口径一致）。
// firstBreakRatio is the first-breakout volume-ratio threshold (aligned with the n_shape left-side signal).
const firstBreakRatio = 1.8

// firstBreakPct 一突突破幅度（价格须超过前高×1.005）。
// firstBreakPct is the first-breakout threshold (price must exceed yesterday's high x 1.005).
const firstBreakPct = 1.005

// dipFactor 二突判定中的回调阈值：跌破峰价×0.997 视为已回调。
// dipFactor is the pullback threshold for the second-breakout check: below the peak x 0.997 counts as a dip.
const dipFactor = 0.997

// Eval 推进并读取 code 的日内突破状态，返回（一突触发, 二突触发）。
// Eval advances and reads the intraday breakout state for code, returning (first-break trigger, second-break trigger).
// 一突每次满足价格+量比条件都返回 true；二突仅在一突后回调再重破峰价时返回一次
// （触发后峰价上移至现价、清除回调标记，供下一波再次判定）。
// The first break returns true whenever the price+volume condition holds; the second break returns once,
// after a pullback following the first break and a renewed break above the peak (the peak moves up to the
// current price and the dip flag clears afterwards, allowing the next wave to be detected again).
func (t *WaveTracker) Eval(code string, md *strategy_engine.StockMarketData) (left, right bool) {
	left, right = false, false
	if code == "" || md == nil || md.Price <= 0 || len(md.KLines) < 2 {
		return left, right
	}
	prev := md.KLines[len(md.KLines)-2]
	prevHigh, prevLow, prevClose := prev.High, prev.Low, prev.Close
	if prevHigh <= 0 || prevClose <= 0 {
		return left, right
	}
	cur := md.Price
	cumVol := 0.0
	if md.Quote != nil {
		cumVol = md.Quote.Volume / 100 // 股 → 手，与 buildIntradayB 口径一致
	}

	day := data.TradingDayDate(time.Now())

	t.mu.Lock()
	defer t.mu.Unlock()

	st := t.states[code]
	if st == nil || st.day != day {
		st = &waveState{day: day, prevHigh: prevHigh}
		t.states[code] = st
	}
	if st.prevHigh != prevHigh {
		// 前日K线刷新（跨日首轮）：重置日态
		// The prior-day bar refreshed (first round across days): reset the daily state.
		st.prevHigh = prevHigh
		st.armed = false
		st.dipped = false
		st.highAfterArm = 0
	}

	// 一突条件：现价 > 前高×1.005 且 量比≥1.8（与 n_shape scorer 口径一致）
	// First-break condition: price > yesterday-high x 1.005 and volume ratio >= 1.8 (aligned with the n_shape scorer).
	isFirst := cur > prevHigh*firstBreakPct && cumVol > 0 && cumVol/math.Max(prevLow, 1) >= firstBreakRatio
	if isFirst {
		left = true
		if !st.armed {
			st.armed = true
			st.dipped = false
			st.highAfterArm = math.Max(cur, prevHigh)
		}
	}

	// 二突判定：已一突 → 峰价回调（dipped）→ 现价重破峰价
	// Second-break check: armed -> pulled back from the peak (dipped) -> price re-breaks the peak.
	if st.armed && cur > st.highAfterArm && st.dipped {
		right = true
		// 触发后上移峰价、清除回调标记，允许后续再次二突
		// After triggering, raise the peak and clear the dip flag to allow further second breaks.
		st.highAfterArm = cur
		st.dipped = false
	}

	// 峰价更新与回调标记
	// Peak tracking and dip flag.
	if st.armed && cur > st.highAfterArm {
		st.highAfterArm = cur
	}
	if st.armed && cur < st.highAfterArm*dipFactor {
		st.dipped = true
	}

	// 惰性清理跨日残留状态，避免 map 无限增长
	// Lazily clean up stale cross-day states to prevent the map from growing unboundedly.
	if len(t.states) > 0 && day != "" {
		for k, s := range t.states {
			if s.day != day {
				delete(t.states, k)
			}
		}
	}
	return left, right
}
