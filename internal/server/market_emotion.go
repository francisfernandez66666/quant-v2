// market_emotion.go — §Dashboard 情绪面板 A（2026-09-13）
//
// GET /api/market/emotion/history?days=30 — 返回最近 N 个交易日的市场风险档快照
// （emotion/market_state/risk_tier/limit_up_count/ladder_height/up_ratio/break_rate/
// max_pos_pct/reasons），数据源 market_risk_daily（引擎每交易日一轮快照落库）。
//
// 前端 Dashboard 情绪卡：
//   - 左：SSE score 事件推的当前 emotion + 判定时间戳
//   - 中：本接口的 30 日色带（每日一段，按 EMOTION_STYLE 上色）
//   - 右：涨停家数 / 最高连板 / 建议仓位
//
// English: Dashboard sentiment card backend — daily emotion/market-state series from
// market_risk_daily, complemented by the live SSE "score" channel for the current phase.
package server

import (
	"net/http"
	"strconv"
	"time"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// handleMarketEmotionHistory 处理 GET /api/market/emotion/history?days=30。
// days 缺省 30，硬顶 250（约一年交易日——情绪回看页 C 档的默认跨度；防全表拖库仍设上限）。
// 自然日窗口按 5/7 反推 +15 覆盖长假（春节/国庆一周以上停市），前端按尾部截取。
func (s *Server) handleMarketEmotionHistory(w http.ResponseWriter, r *http.Request) {
	// 研究库缺失（旧部署未接 trading.db）→ 503 明示降级，前端按空态处理
	if s.researchDB == nil {
		writeError(w, http.StatusServiceUnavailable, "研究库未接入")
		return
	}
	// 参数解析：days 缺省 30，硬顶 250（回看页一年跨度）
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 250 {
				n = 250
			}
			days = n
		}
	}
	// 自然日跨度按 5/7 反推 +15 覆盖长假（春节/国庆一周以上停市）。
	// 注意键格式：market_risk_daily.trade_date 由引擎按 data.TradingDayDate 落库为 YYYYMMDD，
	// 窗口下界必须同格式字符串比较（跨格式比较会把所有行都放行，过滤失效）。
	// English: trade_date is stored compact (YYYYMMDD) by the engine; the range key must match
	// its format or the string comparison silently admits every row.
	naturalSpan := days*7/5 + 15
	from := time.Now().AddDate(0, 0, -naturalSpan).Format("20060102")
	list, err := s.researchDB.ListMarketRiskDaily(from, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 行转 map：nullable 字段（up_ratio/break_rate/max_pos_pct）取回 nil→0 供前端直接展示；
	// reasons 为空则整字段缺席，避免前端把 "" 判成"已说明原因"；
	// date 统一输出 YYYY-MM-DD 供前端 slice(5) 展示 MM-DD。
	out := make([]map[string]any, 0, len(list))
	for _, row := range list {
		item := map[string]any{
			"date":           isoDate(row.TradeDate),
			"emotion":        row.Emotion,
			"market_state":   row.MarketState,
			"risk_tier":      row.RiskTier,
			"limit_up_count": row.LimitUpCount,
			"ladder_height":  row.LadderHeight,
			"up_ratio":       derefOr(row.UpRatio, 0),
			"break_rate":     derefOr(row.BreakRate, 0),
			"max_pos_pct":    derefOr(row.MaxPosPct, 0),
		}
		if row.Reasons != "" {
			item["reasons"] = row.Reasons
		}
		out = append(out, item)
	}
	// 尾部截取最后 days 条（自然日窗口比交易日多取，此处收紧到用户请求的样本量）
	if len(out) > days {
		out = out[len(out)-days:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "series": out})
}

// derefOr 指针值取回或返回默认（nil 语义=当日该项取数失败，前端按 0 显示）。
func derefOr(v *float64, def float64) float64 {
	if v == nil {
		return def
	}
	return *v
}

// isoDate 把库内日期统一成 YYYY-MM-DD（输入兼容 YYYYMMDD 与已是 ISO 的两种形态）。
func isoDate(s string) string {
	if len(s) == 8 {
		return s[:4] + "-" + s[4:6] + "-" + s[6:]
	}
	return s
}

// isoCompact 反向：库内日期统一成 YYYYMMDD（矩阵相位键用，与 backtest event_date 同格式）。
func isoCompact(s string) string {
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		return s[:4] + s[5:7] + s[8:10]
	}
	return s
}

// handleEmotionStrategyMatrix 处理 GET /api/research/emotion-strategy-matrix（§情绪面板 B 档）。
// 聚合"候选战法 × 情绪相位"回测矩阵：数据源 backtest_event_results（B4 链路断点缓存），
// 相位标注 market_risk_daily（引擎日终判定）优先，缺失日回退 EmotionStatsRange+PhaseFromEmotionStat
// （涨停家数×最高连板，同阈值口径）现算。
// English: emotion×strategy matrix endpoint — buckets cached per-event backtest results by daily
// sentiment phase; engine labels (market_risk_daily) preferred, historical gaps recomputed with
// the same thresholds via EmotionStatsRange.
func (s *Server) handleEmotionStrategyMatrix(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, http.StatusServiceUnavailable, "研究库未接入")
		return
	}
	// 相位标注两级来源（键统一 YYYYMMDD，engine/market_risk_daily 与 backtest event_date 同格式）：
	// ① EmotionStatsRange+PhaseFromEmotionStat 先算全历史（同阈值口径的现算器）；
	// ② market_risk_daily 引擎日终判定后写（权威覆盖现算值——引擎有实时广度纠偏，更准）。
	// 注：ListMarketRiskDaily 的 from 键必须与落库格式（YYYYMMDD）一致，否则字符串比较失真。
	phaseByDate := map[string]string{}
	if stats, err := s.researchDB.EmotionStatsRange("20200101", time.Now().Format("20060102")); err == nil {
		for _, st := range stats {
			phaseByDate[st.Date] = research.PhaseFromEmotionStat(st, nil) // YYYYMMDD
		}
	}
	if days, err := s.researchDB.ListMarketRiskDaily("20200101", ""); err == nil {
		for _, d := range days {
			if d.Emotion == "" {
				continue
			}
			phaseByDate[isoCompact(d.TradeDate)] = d.Emotion // 权威标注
		}
	}
	rows, err := s.researchDB.ListEmotionStrategyMatrix(phaseByDate, nil, research.AdjBaselineVersion)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"phases":     []string{"冰点", "启动", "发酵", "高潮", "退潮", "背离"},
		"min_events": store.EmotionMatrixRowMinEvents,
		"adj_basis":  research.AdjBaselineVersion, // §ADJ-BASIS 本矩阵算在哪个复权口径上（前端可标注）
		"rows":       rows,
	})
}
