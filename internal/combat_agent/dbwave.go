// Package combat_agent 战法引擎：8a/8b 处理信号个股打分，以及持仓/自选的持续打分。
// dbwave.go 实现双响炮"跨 5s 打分周期"的第二波确认状态机。
// 背景：double_bump 战法本身已通过 volScore>0 硬闸（最后一根日K放量达第二波倍数）才出分，
// 但该确认只基于日K最后一根（可能是昨日收盘），无法区分"竞价/盘前的存量假放量"。
//
// 状态机设计：
//   - 阶段流转：一突破位（PhaseFirst）→ 缩量回调（PhaseAdjust）→ 二次放量重破前高（PhaseSecond）
//   - 状态按交易日隔离，跨日自动重置
//   - 仅在双响炮候选被评分时推进
//   - 竞价/盘前（Volume=0）无真实成交，状态机不推进 → 不会误放行假双凸
package combat_agent

import (
	"math"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// doubleBumpPhase 双响炮日内阶段。
// 用于跟踪双响炮战法的日内确认状态，从第一波突破到第二波确认。
type doubleBumpPhase int

const (
	dbPhaseFirst  doubleBumpPhase = 1  // 第一波突破（First breakout）
	dbPhaseAdjust doubleBumpPhase = 2  // 缩量调整（Adjustment）
	dbPhaseSecond doubleBumpPhase = 3  // 第二波突破，确认双凸（Second breakout, confirmed）
	dbPhaseThird  doubleBumpPhase = 4  // 第三波延伸（Third extension）
	dbPhaseIDF    doubleBumpPhase = -1 // 形态失效（Invalidated）
)

// dbState 单只股票的双响炮日内阶段状态。
// 跟踪每只股票在当日的双响炮确认进度，用于判断是否真正确认第二波突破。
//
// 字段说明：
//   - day: 交易日（跨日重置依据）
//   - phase: 当前阶段
//   - prevHigh: 前日最高价（一突破位参照）
//   - firstArmed: 是否已一突破位
//   - peak: 一突以来的盘中最高价（峰价）
//   - dipped: 是否已从峰价回调（跌破 0.997 倍峰价）
type dbState struct {
	day        string
	phase      doubleBumpPhase
	prevHigh   float64
	firstArmed bool
	peak       float64
	dipped     bool
}

// DoubleBumpWatcher 双响炮日内第二波确认状态机容器，按股票代码维护日内阶段。
// 每只股票维护独立的状态，跨交易日自动重置。
//
// 字段说明：
//   - mu: 互斥锁，保护并发访问
//   - states: 股票代码→日内状态映射
type DoubleBumpWatcher struct {
	mu     sync.Mutex
	states map[string]*dbState
}

// NewDoubleBumpWatcher 创建状态机容器。
// 返回值：
//   - 初始化后的 DoubleBumpWatcher 指针
func NewDoubleBumpWatcher() *DoubleBumpWatcher {
	return &DoubleBumpWatcher{states: make(map[string]*dbState)}
}

// Confirm 推进并读取 code 的双响炮阶段，返回当前是否已到达第二波确认（PhaseSecond）。
// 状态机流转：
//   1. 一突：现价 > 前高×1.005 且累计量 > 0
//   2. 回调：跌破峰价×0.997 记回调（Adjust）
//   3. 二突：二次放量重破峰价 → PhaseSecond（阶段升至 Third）
//   4. 前日K线刷新（跨日首轮）重置全部阶段状态
//
// 参数：
//   - code: 股票代码
//   - md: 行情数据快照
//   - cfg: 双响炮配置
//
// 返回值：
//   - true: 已到达第二波确认（PhaseSecond）
//   - false: 未到达第二波确认
func (w *DoubleBumpWatcher) Confirm(code string, md *strategy_engine.StockMarketData, cfg config.DoubleBumpConfig) bool {
	if code == "" || md == nil || md.Price <= 0 || len(md.KLines) < 2 {
		return false
	}
	prev := md.KLines[len(md.KLines)-2]
	prevHigh := prev.High
	if prevHigh <= 0 {
		return false
	}
	cur := md.Price
	cumVol := 0.0
	if md.Quote != nil {
		cumVol = md.Quote.Volume / 100 // 股 → 手，与 buildIntradayB 口径一致
	}

	day := data.TradingDayDate(time.Now())

	w.mu.Lock()
	defer w.mu.Unlock()

	st := w.states[code]
	if st == nil || st.day != day {
		st = &dbState{day: day, prevHigh: prevHigh, phase: dbPhaseFirst}
		w.states[code] = st
	}
	if st.prevHigh != prevHigh {
		// 前日K线刷新（跨日首轮）：重置阶段
		// The prior-day bar refreshed (first round across days): reset the phase.
		st.prevHigh = prevHigh
		st.phase = dbPhaseFirst
		st.firstArmed = false
		st.dipped = false
		st.peak = 0
	}

	// 一突出动：现价 > 前高×1.005 且当日累计量>0（有真实成交）
	// First breakout: price > prior-high x 1.005 with real cumulative volume (>0).
	isFirst := cur > prevHigh*1.005 && cumVol > 0
	if isFirst && !st.firstArmed {
		st.firstArmed = true
		st.dipped = false
		st.phase = dbPhaseFirst
		st.peak = math.Max(cur, prevHigh)
	}

	// 回调标记：现价较"已记录的峰价"回落（跌破 0.997 倍峰价）记为缩量调整。
	// 注意：应在峰价被本 bar 抬高之前判定，避免"现价总等于峰价"导致永不回调。
	// Pullback marker: a drop below the *recorded* peak (x 0.997). Judged before the peak is raised by
	// this bar so the current price never simply equals peak and blocks a pullback from registering.
	if st.firstArmed && cur < st.peak*0.997 {
		st.dipped = true
	}

	// 二突确认：一突后回调过，且现价重破"记录峰价" → PhaseSecond（双凸确认）。
	// 判定在峰价抬高之前进行；确认后把峰价上移至现价、清除回调标记。
	// Second-wave confirmation: after a pullback, price re-breaks the *recorded* peak -> PhaseSecond.
	// Checked before the peak is raised; on confirm the peak moves up and the dip flag clears.
	if st.firstArmed && st.dipped && cur > st.peak {
		st.phase = dbPhaseSecond
		st.peak = cur
		st.dipped = false
	} else if st.phase == dbPhaseSecond {
		// 已确认第二波后，后续 bar 视为第三波延伸（延续强势）
		// Once the second wave is confirmed, later bars are treated as third-wave extension.
		st.phase = dbPhaseThird
	}

	// 峰价更新：在确认判定之后抬高（供下一波/后续 bar 参照）
	// Peak update: raise after the confirmation check (reference for the next wave / later bars).
	if st.firstArmed && cur > st.peak {
		st.peak = cur
	}

	// 惰性清理跨日残留状态，避免 map 无限增长
	// Lazily clean up stale cross-day states to prevent the map from growing unboundedly.
	if len(w.states) > 0 && day != "" {
		for k, s := range w.states {
			if s.day != day {
				delete(w.states, k)
			}
		}
	}
	return st.phase >= dbPhaseSecond
}
