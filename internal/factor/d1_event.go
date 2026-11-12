// d1_event.go §NEWS-GATE（2026-11-12，owner 裁决 R7）——D1 事件带符号分作为一等因子注册。
// 素材为个股当日 LLM 事件评分（combat_agent.D1Score，带符号 −40~+40：正=事件驱动强度、
// 0=无实质事件、负=明确利空），由实盘打分链路（combat_agent/adapter.go evalFor）经
// StockMarketData.D1 → seriesFromKLines 装载为**单日常量列**（时间序列上逐日同值，
// 与财务字段同型）。研究侧对应 news_score@stock 截面因子（event_factor.go §ENH-4），两侧口径
// 同源（都出自 D1/新闻事件链）。
//
// 打分语义（实盘 scoreRule 特例分支，见 strategies/factor/factor.go）：
//   - D1>0：pct = clamp01(D1/40)（绝对带，非时序分位——事件分本身就是绝对量级）；
//   - D1≤0 或 NaN：该因子弃权（不参与复合分）——负分的处置在信号层一票否决（engine
//     news_gate），因子层绝不把利空当"低分参与"，避免负值污染凸组合语义。
//
// English: D1Event registers the signed LLM D1 event score (−40~+40) as a first-class factor,
// loaded as a single-day constant column. Positive scores normalize clamp01(D1/40); zero/negative
// abstain — negative handling lives in the engine-level buy veto, never in the composite.
package factor

// d1Array 安全取 D1 列，空序列返回 nil（用例层过滤）。
func d1Array(s *StockSeries) []float64 {
	if s == nil || len(s.Dates) == 0 {
		return nil
	}
	return s.D1
}

// init 注册 D1 事件因子（§NEWS-GATE R7：D1 彻底作为因子，引入信号生成）。
func init() {
	Register(Def{
		ID:   "D1Event",
		Name: "D1事件分",
		Cat:  CatSentiment,
		Desc: "个股当日 LLM 事件评分（带符号 −40~+40：正=利好强度，负=明确利空；负分由信号层直接拦截买入，因子层弃权）",
		Compute: func(s *StockSeries) []float64 {
			return d1Array(s)
		},
	})
}
