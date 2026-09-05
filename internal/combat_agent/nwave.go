// Package combat_agent 战法引擎：8a/8b 处理信号个股打分，以及持仓/自选的持续打分。
// nwave.go 实现 N 形"一突/二突"日内状态机：跨 5s 打分周期跟踪每只股票的突破节奏。
//
// 一突（左侧）：价格突破前日高点×1.005 且量比≥1.8 → 立即打标买入（≥P2）。
// 二突（右侧）：一突破位后经历回调（跌破峰价×0.997），再次放量重破前高 → 最强确认（P1）。
//
// 状态机设计：
//   - 状态按交易日隔离，跨日自动重置
//   - 状态仅在 N 形候选被评分时推进
//   - 一突每次满足条件都返回 true
//   - 二突仅在一突后回调再重破峰价时返回一次
package combat_agent

import (
	"math"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// waveState 单只股票的日内突破状态。
// 跟踪每只股票在当日的 N 形突破进度，用于判断一突/二突信号。
//
// 字段说明：
//   - day: 交易日（跨日重置依据）
//   - prevHigh: 前日最高价
//   - armed: 是否已发生一突破位
//   - highAfterArm: 一突以来的盘中最高价（峰价）
//   - dipped: 是否已从峰价回调（跌破 0.997 倍峰价）
type waveState struct {
	day          string  // 交易日（跨日重置依据）
	prevHigh     float64 // 前日最高价
	armed        bool    // 是否已发生一突破位
	highAfterArm float64 // 一突以来的盘中最高价（峰价）
	dipped       bool    // 是否已从峰价回调（跌破 0.997 倍峰价）
}

// WaveTracker N 形一突/二突状态机容器，按股票代码维护日内状态。
// 每只股票维护独立的状态，跨交易日自动重置。
//
// 字段说明：
//   - mu: 互斥锁，保护并发访问
//   - states: 股票代码→日内状态映射
type WaveTracker struct {
	mu     sync.Mutex
	states map[string]*waveState
}

// NewWaveTracker 创建状态机容器。
// 返回值：
//   - 初始化后的 WaveTracker 指针
func NewWaveTracker() *WaveTracker {
	return &WaveTracker{states: make(map[string]*waveState)}
}

// firstBreakRatio 一突量比阈值（与 n_shape 左侧信号口径一致）。
const firstBreakRatio = 1.8

// firstBreakPct 一突突破幅度（价格须超过前高×1.005）。
const firstBreakPct = 1.005

// dipFactor 二突判定中的回调阈值：跌破峰价×0.997 视为已回调。
const dipFactor = 0.997

// Eval 推进并读取 code 的日内突破状态，返回（一突触发, 二突触发）。
// 一突每次满足价格+量比条件都返回 true；二突仅在一突后回调再重破峰价时返回一次
// （触发后峰价上移至现价、清除回调标记，供下一波再次判定）。
//
// 一突条件：
//   - 现价 > 前高×1.005
//   - 量比≥1.8（当日累计成交量 / 近20日日均成交量）
//
// 二突条件：
//   - 已一突（armed）
//   - 已回调（dipped）
//   - 现价重破峰价
//
// 参数：
//   - code: 股票代码
//   - md: 行情数据快照
//
// 返回值：
//   - left: 一突触发
//   - right: 二突触发
func (t *WaveTracker) Eval(code string, md *strategy_engine.StockMarketData) (left, right bool) {
	left, right = false, false
	if code == "" || md == nil || md.Price <= 0 || len(md.KLines) < 2 {
		return left, right
	}
	prev := md.KLines[len(md.KLines)-2]
	prevHigh, prevClose := prev.High, prev.Close
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

	// 一突条件：现价 > 前高×1.005 且 量比≥1.8（与 n_shape scorer 口径一致）。
	// 量比 = 当日累计成交量(股) / 近20日日均成交量(股)，剔除今日未收盘K线避免未来函数。
	// English: first-break condition: price > yesterday-high x 1.005 and volume ratio >= 1.8. Volume ratio
	// is today's cumulative volume (shares) divided by the 20-day average daily volume (shares), excluding
	// today's unfinished bar to avoid look-ahead bias.
	avgDailyVol := avgVol(md.KLines[:len(md.KLines)-1], 20)
	volRatio := 0.0
	if avgDailyVol > 0 && md.Quote != nil {
		// §修复 P2#26：盘中时间窗归一——旧公式 cumVol*100/avgDailyVol 把「实时累计量」直接
		// 对「全日均量」，上午累计量天然偏小→量比被稀释，同一放量在开盘半小时只显示 1/5
		// 强度，早盘一突经常因"量比达标前量不足"被误杀。现在按已流逝交易分钟折算全天等值
		// 量再与均量比较，量比口径任意时刻与收盘一致。
		// English: P2#26 — time-normalize the intraday cumulative volume to a full-day equivalent before
		// comparing with the daily average; otherwise a morning breakout's volume ratio is diluted ~5x
		// and the 一突 signal is wrongly killed early in the session.
		volRatio = intradayVolumeRatio(time.Now(), md.Quote.Volume, avgDailyVol)
	}
	isFirst := cur > prevHigh*firstBreakPct && cumVol > 0 && volRatio >= firstBreakRatio
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
