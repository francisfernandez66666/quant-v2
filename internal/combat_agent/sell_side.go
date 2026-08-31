// Package combat_agent 战法引擎：持续打分与卖点评估。
// sell_side.go 实现逐股卖出评估：对打分池内每只个股（持仓/自选/跟踪池）独立评估卖点风险，
// 命中 利空D1 / 破MA5·MA20 / 放量派发 / 动量衰竭 任一因素即产生"卖点"提醒信号。
// 仅提醒、不自动执行，与战法退出引擎（position_exits.go）互补：退出引擎按持仓成本/策略参数算，
// 本模块按行情与事件面判断"该不该卖"，覆盖尚未持仓或无法用持仓成本度量的股票。
//
// 四项卖点因素（按严重度降序）：
//   1. 利空D1：负面过滤拦截（立案/减持/质押/解禁等）→ 清仓级
//   2. 破MA5·MA20：现价同时跌破5日与20日均线 → 减仓级
//   3. 放量派发：当日实时量明显放大且价格下跌 → 减仓级
//   4. 动量衰竭：动量分低于信号阈值一半且分钟MACD转空 → 提示级
//
// 单只个股只产出一条卖点信号，取最严重因素决定级别与动作
package combat_agent

import (
	"fmt"
	"time"

	"quant-trading-v2/internal/strategy_engine"
)

// sellSideFactor 卖点评估命中的因素（按严重度降序：利空事件 > 趋势破位 > 量能派发 > 动量衰竭）。
// 单只个股只产出一条卖点信号，取最严重因素决定级别与动作，Reason 汇总全部命中因素。
// English: a sell-risk factor that tripped (severity descending: bearish event > trend breakdown >
// volume distribution > momentum exhaustion). One sell-side signal per stock, leveled by the most
// severe factor, with the Reason summarizing all tripped factors.
type sellSideFactor struct {
	level  string // 级别：清仓(利空事件)/减仓(破位·派发)/提示(动量衰竭)
	action string // 操作建议：卖出（一律卖出方向，仅提醒）
	reason string // 该因素命中说明
	score  int    // 严重度（越大越严重，决定最终级别）
}

// assessSellFactor 判定单只个股的四项卖点因素并返回命中集合（未命中则空）。
// 数据全部取自分池快照：事件面用 d1Scores（Blocked=负面过滤拦截），
// 行情面用 md 的日K/实时量价（破位/派发），动量用分钟 MACD 与动量分（衰竭）。
// English: judges the four sell factors for one stock and returns the tripped set (empty when none).
// Event face uses d1Scores (Blocked = negative-filter interception); price face uses md's daily bars
// and live quote (breakdown/distribution); momentum uses the minute MACD and the momentum score.
func assessSellFactor(code string, md *strategy_engine.StockMarketData, d1 D1Score, mScore float64, mThreshold float64) []sellSideFactor {
	var factors []sellSideFactor

	// 1. 利空D1：负面过滤拦截（立案/减持/质押/解禁等），属于事件面强卖点 → 清仓级
	// English: bearish D1 — the negative filter blocked this stock (investigation/share-reduction/
	// pledge/restricted-unlock…), a strong event-based sell point → close-out level.
	if d1.Blocked {
		factors = append(factors, sellSideFactor{
			level:  "清仓",
			action: "卖出",
			reason: fmt.Sprintf("利空事件(负面过滤拦截): %s", d1.Reason),
			score:  4,
		})
	}

	// 2. 破MA5·MA20：现价同时跌破5日与20日均线，趋势转弱 → 减仓级
	// English: breaking MA5·MA20 — the current price sits below both the 5-day and 20-day MAs, trend
	// turns weak → trim level.
	if kl := md.KLines; len(kl) >= 20 {
		ma5 := ma(kl, 5)
		ma20 := ma(kl, 20)
		last := kl[len(kl)-1].Close
		// 现价优先（实时报价更新更快），停牌/缺报价时回退最近收盘价
		// English: prefer the live price (fresher); fall back to the last close when suspended/missing.
		cur := md.Price
		if cur <= 0 {
			cur = last
		}
		if ma5 > 0 && ma20 > 0 && cur < ma5 && cur < ma20 {
			factors = append(factors, sellSideFactor{
				level:  "减仓",
				action: "卖出",
				reason: fmt.Sprintf("现价%.2f 同时跌破MA5(%.2f)/MA20(%.2f),短期趋势转弱", cur, ma5, ma20),
				score:  3,
			})
		}
	}

	// 3. 放量派发：当日实时量明显放大（>1.5倍前20日均量）且价格下跌 → 资金派发特征
	// English: volume distribution — today's live volume expands (>1.5× prior 20-day average) while the
	// price falls, a capital-distribution signature.
	if q := md.Quote; q != nil && q.Price > 0 && q.Volume > 0 {
		kl := md.KLines
		if len(kl) >= 21 {
			avgV := avgVol(kl[:len(kl)-1], 20)
			if avgV > 0 && q.Volume > avgV*1.5 && md.ChangePct < 0 {
				factors = append(factors, sellSideFactor{
					level:  "减仓",
					action: "卖出",
					reason: fmt.Sprintf("放量下跌: 量%.0f=%.1f倍均量, 跌幅%.2f%%, 有资金派发迹象", q.Volume, q.Volume/avgV, md.ChangePct),
					score:  2,
				})
			}
		}
	}

	// 4. 动量衰竭：动量分低于信号阈值一半且分钟MACD转空（DIF在零轴下且死叉）→ 上涨动能衰竭
	// English: momentum exhaustion — the momentum score is below half the signal threshold AND the
	// minute MACD turns bearish (DIF below zero axis with a bearish cross) → upside momentum is fading.
	if mThreshold > 0 && mScore < mThreshold/2 {
		m := md.MinuteMACD
		if m.DIF != 0 && m.DEA != 0 && m.DIF < 0 && m.DIF < m.DEA && m.Bar < 0 {
			factors = append(factors, sellSideFactor{
				level:  "提示",
				action: "卖出",
				reason: fmt.Sprintf("动量衰减: 动量分%.0f<%.0f/2, 分钟MACD零下死叉(DIF=%.3f DEA=%.3f), 上涨动能衰竭", mScore, mThreshold, m.DIF, m.DEA),
				score:  1,
			})
		}
	}

	// 命中因素按严重度降序，最高者作最终级别
	// English: sort the tripped factors by severity descending; the top one sets the final level.
	for i := 1; i < len(factors); i++ {
		for j := i; j > 0 && factors[j-1].score < factors[j].score; j-- {
			factors[j-1], factors[j] = factors[j], factors[j-1]
		}
	}
	return factors
}

// AssessSellSide 对打分池内每只个股执行逐股卖出评估，返回卖点提醒信号（仅提醒，不自动执行）。
// 入参 codes 为个股列表，md 为行情快照，d1Scores 为最近一轮 D1 评分，scores 为 8a/8b 打分结果，
// shortEnabled 为做空开关：关闭（仅做多）时调用方应只传持仓代码，本方法按卖出方向输出；
// 开启（做多+做空）时卖点评估的级别徽标改为方向词"做空"（卖出方向），Reason 保留原清仓/减仓/提示等级。
// 数据或评分缺失的个股跳过，未命中任何因素的个股不产生信号。
// English: runs the per-stock sell-point assessment for every code in the pool and returns the pull
// reminders (reminder-only). Inputs are the code list, market snapshot, the latest D1 scores and the
// 8a/8b score table; when shortEnabled is set the level badge becomes the direction word "做空" (sell
// direction) while the original 清仓/减仓/提示 severity stays in the Reason. Stocks with missing
// data/scores are skipped, and those hitting nothing get none.
func (a *Agent) AssessSellSide(codes []string, md map[string]*strategy_engine.StockMarketData, d1Scores map[string]D1Score, scores map[string]StockScores, shortEnabled bool) []Signal {
	now := time.Now()
	mThreshold := a.momentumSignalThreshold()
	var signals []Signal
	for _, code := range codes {
		sd := md[code]
		if sd == nil {
			continue
		}
		var mScore float64
		if sc, ok := scores[code]; ok {
			mScore = sc.MomentumScore
		}
		factors := assessSellFactor(code, sd, d1Scores[code], mScore, mThreshold)
		if len(factors) == 0 {
			continue
		}
		alpha := factors[0]
		reason := alpha.reason
		if len(factors) > 1 {
			for _, f := range factors[1:] {
				reason += "；" + f.reason
			}
		}
		alertType, direction := alpha.level, "提醒"
		if shortEnabled {
			// 做多+做空：卖点评估级别改为方向词"做空"，原等级信息并入 Reason 便于区分严重度
			// English: in long+short mode the level badge reads 做空 (sell direction); the original
			// severity is folded into the Reason so urgency is not lost.
			alertType, direction = "做空", "做空"
			reason = fmt.Sprintf("卖点等级:%s; %s", alpha.level, reason)
		}
		signals = append(signals, Signal{
			ID:          seqID(),
			Code:        code,
			Name:        sd.Name,
			Strategy:    "卖点评估",
			Direction:   direction,
			Action:      alpha.action,
			AlertType:   alertType,
			Price:       sd.Price,
			Confidence:  float64(len(factors)) * 0.25,
			Reason:      reason,
			GeneratedAt: now,
		})
	}
	return signals
}
