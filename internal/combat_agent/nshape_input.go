// Package combat_agent 战法引擎：8a/8b 处理信号个股打分，以及持仓/自选的持续打分。
// nshape_input.go 从 8a/8b 打分池行情数据构造 N 形战法评分输入（WaveA/IntradayB/Ctx）。
// 三个构造函数在 adapter.go 的 evalFor 中被 N 形策略分支调用。
//
// 构造逻辑：
//   - buildWaveA: 从日K线构造 N 形昨日波形（A波）
//   - buildIntradayB: 从实时量价快照 + 分钟MACD 构造日内快照（B段）
//   - buildCtx: 构造 N 形评分上下文（情绪阶段 + 均量 + D1事件）
package combat_agent

import (
	"time"

	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy_engine"
)

// buildWaveA 从日K线构造 N 形昨日波形（A波）。
// 昨开/高/低/收/量取自日K倒数第二根，涨幅对前日收盘计算，MA60 判断用不含今日的K线。
// sector 用于注入板块龙头属性；K线不足 2 根时返回零值 WaveA（评分降级为 0）。
//
// 构造逻辑：
//   1. 昨日波形 = 倒数第二根日K
//   2. 昨日涨幅相对前日收盘计算
//   3. 前日收阴线 → 标记弱势
//   4. A波是否站上 MA60（剔除今日K线，避免未来函数）
//   5. 板块 RPS 前 2 → 视为板块龙头
//
// 参数：
//   - md: 行情数据快照
//   - sector: 板块上下文（可为 nil）
//
// 返回值：
//   - N 形昨日波形数据指针
func buildWaveA(md *strategy_engine.StockMarketData, sector *sector_agent.VerifiedSector) *n_shape.WaveA {
	wa := &n_shape.WaveA{}
	kl := md.KLines
	n := len(kl)
	if n < 2 {
		return wa
	}
	// 昨日波形 = 倒数第二根日K
	// Yesterday's waveform = the second-to-last daily bar.
	y := kl[n-2]
	wa.ADate = y.Date.Format("2006-01-02")
	wa.AOpen, wa.AHigh, wa.ALow, wa.AClose = y.Open, y.High, y.Low, y.Close
	wa.AVol = y.Volume
	if n >= 3 {
		// 昨日涨幅相对前日收盘计算
		// Yesterday's gain is computed against the prior day's close.
		d2 := kl[n-3]
		if d2.Close > 0 {
			wa.AChgPct = (y.Close - d2.Close) / d2.Close * 100
		}
		// 前日收阴线 → 标记弱势（影响 N 形强弱判断）
		// Prior day closed bearish -> mark as weak (affects the N-shape strength judgement).
		wa.PrevSessionWeak = d2.Close < d2.Open
	}
	// A波是否站上 MA60（剔除今日K线，避免未来函数）
	// Whether Wave A is above MA60 (excluding today's bar to avoid look-ahead bias).
	prev := kl[:n-1]
	wa.AAboveMA60 = y.Close > ma(prev, 60)
	// 板块 RPS 前 2 → 视为板块龙头，N 形给加分
	// Industry RPS top 2 -> treated as an industry leader, gaining a bonus in the N-shape score.
	if sector != nil {
		wa.IsSectorLeader = sector.RPSRank > 0 && sector.RPSRank <= 2
	}
	return wa
}

// buildIntradayB 从实时量价快照 + 分钟MACD 构造日内快照（B段）。
// 竞价数据仅盘前可得：开盘竞价涨跌幅用 Quote.Open 对前日收盘计算（非零时填充），
// 竞价量/趋势在非竞价时段无法回溯，置零降级（D2 相对强度偏低但 N 形仍出总分）。
//
// 构造逻辑：
//   1. 当前时间转为 HHMM 整数格式
//   2. 实时成交量：股 → 手 换算
//   3. 昨日收盘/最高/最低用于日内相对强度与位置计算
//   4. 基准指数当前涨跌幅
//   5. 开盘竞价涨跌幅
//   6. 近20日日均成交量
//   7. 分钟级 MACD 三值
//
// 参数：
//   - md: 行情数据快照
//
// 返回值：
//   - N 形日内快照数据指针
func buildIntradayB(md *strategy_engine.StockMarketData) *n_shape.IntradayB {
	ib := &n_shape.IntradayB{}
	kl := md.KLines
	// 当前时间转为 HHMM 整数格式（如 14:35 → 1435），供日内时段判断
	// Convert the current time to an HHMM integer (e.g. 14:35 → 1435) for the intraday-period judgement.
	now := time.Now()
	ib.TTime = now.Hour()*100 + now.Minute()
	ib.CurPrice = md.Price
	// 实时成交量：股 → 手 换算
	// Real-time volume: shares → lots.
	if q := md.Quote; q != nil {
		ib.CumVol = q.Volume / 100 // 股 → 手
	}
	if len(kl) >= 2 {
		// 昨日收盘/最高/最低用于日内相对强度与位置计算
		// Yesterday's close/high/low used for intraday relative strength and position.
		prev := kl[len(kl)-2]
		ib.PrevClose = prev.Close
		ib.PrevHigh = prev.High
		ib.PrevLow = prev.Low
	}
	// 基准指数当前涨跌幅（N 形 D2 相对强度对比），无数据时为 0
	// The benchmark index's current change % (for the N-shape D2 relative-strength comparison); 0 when absent.
	ib.BenchCurChg = md.BenchChg
	// 开盘竞价涨跌幅：Quote.Open 相对前日收盘（%）
	// Auction change %: Quote.Open vs the prior close (%).
	if q := md.Quote; q != nil && q.Open > 0 && ib.PrevClose > 0 {
		ib.AuctionChgPct = (q.Open - ib.PrevClose) / ib.PrevClose * 100
	}
	// 近20日日均成交量：剔除今日未收盘K线，避免盘中把未完成的今日量计入均值（压低/抬升量比）。
	// English: 20-day average daily volume, excluding today's unfinished bar to avoid distorting the ratio.
	ib.AvgDailyVol = avgVol(kl[:len(kl)-1], 20)
	// 分钟级 MACD 三值直接透传，供 B 段多头/红柱判断
	// The three minute-level MACD values are passed through for the Segment-B bullish/red-bar judgement.
	ib.MinuteMACDDIF = md.MinuteMACD.DIF
	ib.MinuteMACDDEA = md.MinuteMACD.DEA
	ib.MinuteMACDBar = md.MinuteMACD.Bar
	return ib
}

// buildCtx 构造 N 形评分上下文：情绪阶段 + 20日均量 + D1 事件评分。
// emotionPhase 供情绪硬闸（如冰点禁开仓）使用；均量为波动率/强度参考。
// d1 为 D1Scorer 批量评分结果（LLM 0~40 分 + 负面阻断标记），nil 表示本轮无 D1 数据；
// eventDesc 为个股关联新闻标题（供 calcD1 的 YAML 负面阻断 + LLM 评分）；
// pe 为个股市盈率（供 D3 超跌评分，<=0 时走斐波那契兜底）。
//
// 参数：
//   - md: 行情数据快照
//   - emotionPhase: 情绪阶段
//   - d1: D1 评分结果（可为 nil）
//   - eventDesc: 个股关联新闻标题
//   - pe: 个股市盈率
//
// 返回值：
//   - N 形评分上下文指针
func buildCtx(md *strategy_engine.StockMarketData, emotionPhase string, d1 *D1Score, eventDesc string, pe float64) *n_shape.Ctx {
	ctx := &n_shape.Ctx{EmotionPhase: emotionPhase, EventDesc: eventDesc, StockPE: pe}
	if d1 != nil {
		// LLM 评分 0~40 直接作为 D1（calcD1 不再乘 MaxD1）；负面阻断标记透传
		// The LLM 0~40 score is used directly as D1 (calcD1 no longer multiplies by MaxD1); the negative-block flag passes through.
		ctx.LLMD1Score = d1.Score
		ctx.LLMBlocked = d1.Blocked
	}
	if md != nil && len(md.KLines) >= 2 {
		// 与 buildIntradayB 同口径：剔除今日未收盘K线。
		// English: same口径 as buildIntradayB — exclude today's unfinished bar.
		ctx.AvgDailyVol = avgVol(md.KLines[:len(md.KLines)-1], 20)
	}
	return ctx
}
