// signal_quote.go 信号行情快照冻结：把「生成那一刻」的涨跌幅/昨收抄进 Signal。
//
// 背景（台账 2026-10-15 第二条）：Signal 过去只有触发价 Price，没有涨跌幅字段，信号日志里
// 的历史批次只能看到静态触发价，复盘时分不清当时是涨是跌。补字段的关键不是加列，而是**时间语义**：
// 历史批次绝不能拿今天的实时价回填——那等于给旧信号盖上今天的价格戳（「派生状态不是来源」的镜像）。
// 因此统一在信号生成点调用 freezeQuote 抄一份快照，之后随批次落盘，API 与前端只读这份快照。
//
// 口径：
//   - 只有 price > 0 才认为「当时有行情」；无行情 → ChangePct 留 nil（前端显示「—」），
//     不把缺失折叠成 0.00%（0 与真平盘必须能区分）；
//   - 昨收缺失但涨跌幅可得时按 price/(1+changePct/100) 反推，供复盘核对；
//   - 数值统一保留 2 位小数（涨跌幅）/ 4 位小数（昨收），避免浮点尾巴进入落盘快照。
package combat_agent

import (
	"math"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// freezeQuote 把生成时刻的价格、涨跌幅、昨收冻结进信号。
// price<=0 视为当时无有效行情，不改写任何字段（保留 nil → 前端「—」）。
// English: freezes the generation-time quote into the signal; price<=0 means "no quote" and leaves
// ChangePct nil so the UI can render "—" instead of a fake 0.00%.
func freezeQuote(sig *Signal, price, changePct, prevClose float64) {
	if sig == nil || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) {
		return
	}
	validChange := !math.IsNaN(changePct) && !math.IsInf(changePct, 0)
	if prevClose > 0 && !math.IsInf(prevClose, 0) {
		sig.PrevClose = math.Round(prevClose*10000) / 10000
		// 有昨收就以同一口径重算涨跌幅，避免不同数据源混用（如价格来自快照、涨跌幅来自旧缓存）。
		changePct = (price/sig.PrevClose - 1) * 100
		validChange = true
	} else if validChange && changePct > -100 {
		// 昨收缺失时按价格与涨跌幅反推，供复盘核对；-100% 会让分母归零，放弃反推。
		if derived := price / (1 + changePct/100); derived > 0 && !math.IsInf(derived, 0) {
			sig.PrevClose = math.Round(derived*10000) / 10000
		}
	}
	// 涨跌幅不可得（NaN/Inf 且无昨收可算）→ 保持 nil：缺数不是平盘，历史批次宁可显示「—」。
	if !validChange {
		return
	}
	v := math.Round(changePct*100) / 100
	sig.ChangePct = &v
}

// freezeMarketQuote 从打分池行情（策略扫描的 md）冻结信号行情。
// 涨跌幅/昨收一律以该股**当时行情**为基准（md.Price / md.ChangePct / md.Quote.PrevClose）；
// 信号自己的触发价可能高于或低于现价，不能拿来当行情算涨跌幅——否则复盘读数会失真
// （例如触发价 20、现价 10，会冻出一个不存在的 +100%）。
// English: freezes from the stock's own quote at generation time; the signal's trigger price is
// not a quote basis and must never be used to derive change-%.
func freezeMarketQuote(sig *Signal, md *strategy_engine.StockMarketData) {
	if md == nil {
		return
	}
	var prevClose float64
	if md.Quote != nil {
		prevClose = md.Quote.PrevClose
	}
	freezeQuote(sig, md.Price, md.ChangePct, prevClose)
}

// freezeStockQuote 从实时行情快照（持仓提醒/退出的 quote）冻结信号行情。
// English: freezes quote fields from a live StockInfo snapshot (position alerts / exits).
func freezeStockQuote(sig *Signal, q *data.StockInfo) {
	if q == nil {
		return
	}
	freezeQuote(sig, q.Price, q.ChangePct, q.PrevClose)
}
