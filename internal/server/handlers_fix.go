// ── fix 兼容端点 ──
// 本文件提供与前端兼容的 HTTP API 处理函数，
// 将内部数据模型转换为前端期望的格式。

package server

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/trading"
)

// r2 四舍五入到 2 位小数（价格/百分比）。
func r2(v float64) float64 { return math.Round(v*100) / 100 }

// r0 四舍五入到整数（分数）。
func r0(v float64) float64 { return math.Round(v) }

// fixSignal 适配前端信号格式的结构体。
// 将内部 combat_agent.Signal 转换为前端期望的字段名和格式。
type fixSignal struct {
	Code         string  `json:"code"`          // 股票代码
	Name         string  `json:"name"`          // 股票名称
	Strategy     string  `json:"strategy"`      // 触发策略
	TotalScore   float64 `json:"total_score"`   // 总分（0~100）
	RemindLevel  string  `json:"remind_level"`  // 提醒级别：strong/observe/mute
	Level        string  `json:"level"`         // 固定"交易"
	Action       string  `json:"action"`        // 交易动作（buy 等）
	Price        float64 `json:"price"`         // 信号触发价格
	ChangePct    float64 `json:"change_pct"`    // 实时涨跌幅（%）
	CanOpen      bool    `json:"can_open"`      // 是否可开仓（置信度≥0.7 且为买入）
	D1           float64 `json:"d1"`            // 维度1 评分
	D2           float64 `json:"d2"`            // 维度2 评分
	D3           float64 `json:"d3"`            // 维度3 评分
	D4           float64 `json:"d4"`            // 维度4 评分
	D1Desc       string  `json:"d1_desc"`       // 维度1 说明（触发理由）
	D2Desc       string  `json:"d2_desc"`       // 维度2 说明（所属板块）
	D3Desc       string  `json:"d3_desc"`       // 维度3 说明
	D4Desc       string  `json:"d4_desc"`       // 维度4 说明
	SignalActive bool    `json:"signal_active"` // 信号是否活跃

	// 真实 D1 事件信息：区别于上面的 D1Desc（策略理由），单独展示新闻事件的 D1 分析
	// English: real D1 event info — distinct from D1Desc (strategy reason), shown separately as the
	// news-event D1 analysis (score 0~40, negative-filter flag, LLM reason, linked event title).
	D1Score   float64 `json:"d1_score"`   // D1 事件评分（0~40）
	D1Blocked bool    `json:"d1_blocked"` // D1 负面过滤拦截标记
	D1Reason  string  `json:"d1_reason"`  // D1 事件分析理由（LLM）
	D1Event   string  `json:"d1_event"`   // D1 关联事件名称

	// DepthFactors 盘口因子（买卖压力/封单量，免费五档 / Level-2 十档），供前端与战法展示使用
	// English: order-book factors (bid/ask pressure & seal volumes; 5 levels free / 10 with Level-2)
	DepthFactors *data.OrderBookFactors `json:"depth_factors,omitempty"`
}

// scoreToRemindLevel 将总分转换为前端提醒级别。
// >= 0.7 → "strong"（强信号），>= 0.4 → "observe"（观察），否则 → "mute"（静默）。
func scoreToRemindLevel(score float64) string {
	if score >= 0.7 {
		return "strong"
	}
	if score >= 0.4 {
		return "observe"
	}
	return "mute"
}

// toFixSignals 将内部 Signal 列表转换为前端 fixSignal 格式。
// signals: 内部策略信号列表。
// 返回前端兼容的信号列表，包含评分、级别、可开仓标志等字段。
func toFixSignals(signals []combat_agent.Signal) []fixSignal {
	out := make([]fixSignal, 0, len(signals))
	for _, s := range signals {
		fs := fixSignal{
			Code:         s.Code,
			Name:         s.Name,
			Strategy:     s.Strategy,
			TotalScore:   s.Confidence * 100,
			RemindLevel:  scoreToRemindLevel(s.Confidence),
			Level:        "交易",
			Action:       s.Action,
			Price:        s.Price,
			CanOpen:      s.Confidence >= 0.7 && s.Action == "buy",
			D1:           metaD(s, "d1"),
			D2:           metaD(s, "d2"),
			D3:           metaD(s, "d3"),
			D4:           metaD(s, "d4"),
			D1Desc:       s.Reason,
			D2Desc:       s.Sector,
			SignalActive: true,
			D1Score:      s.D1Score,
			D1Blocked:    s.D1Blocked,
			D1Reason:     s.D1Reason,
			D1Event:      s.D1Event,
			DepthFactors: s.DepthFactors,
		}
		out = append(out, fs)
	}
	return out
}

// metaD 从信号 Meta 读取某维度的真实评分（d1/d2/d3/d4），缺失或非正数返回 0。
// 修复历史问题：旧实现把 Confidence×100 复用到全部维度，导致前端 D2/D3/D4 虚高为总分。
// English: reads a real dimension score (d1/d2/d3/d4) from signal Meta; 0 when absent. Fixes the
// legacy bug of reusing Confidence×100 for every dimension, which inflated D2/D3/D4 to the total.
func metaD(s combat_agent.Signal, key string) float64 {
	if s.Meta == nil {
		return 0
	}
	return s.Meta[key]
}

// filterStaleSignals 信号展示的实时复核（仅影响"当前信号"展示，不改写任何存储/日志）：
// 做多信号当日转绿(ChangePct<=0)、做空信号当日转红(ChangePct>=0) 视为已失效剔除；
// ST/*ST/S*ST/退市整理 个股信号一律剔除（风险警示）。
// 行情缺失时保留（fail-open，避免网络波动误撤）。返回筛选后的信号与剔除条数。
// （filterStaleSignals is the display-only live re-validation for the current-signals tab: a long
// signal whose stock turned red (ChangePct<=0), or a short signal whose stock turned green
// (ChangePct>=0), is stale and removed. ST/*ST/delisting stocks are always dropped (risk warning).
// Missing quotes keep the signal (fail-open).）
func filterStaleSignals(sigs []combat_agent.Signal, quotes map[string]*data.StockInfo) ([]combat_agent.Signal, int) {
	live := make([]combat_agent.Signal, 0, len(sigs))
	pruned := 0
	for _, sig := range sigs {
		if combat_agent.IsSTStock(sig.Name) {
			pruned++
			continue
		}
		if info, ok := quotes[sig.Code]; ok && info != nil && info.Price > 0 {
			if (sig.Direction == "做多" && info.ChangePct <= 0) ||
				(sig.Direction == "做空" && info.ChangePct >= 0) {
				pruned++
				continue
			}
		}
		live = append(live, sig)
	}
	return live, pruned
}

// handleFixSignals 处理 GET /api/signals 请求，返回最新策略信号列表（附实时股价/涨跌幅）。
func (s *Server) handleFixSignals(w http.ResponseWriter, r *http.Request) {
	dash := s.dashFor(requestUserID(r))
	if dash == nil {
		writeJSON(w, 200, []fixSignal{})
		return
	}
	// 逐票从 5s 快照取实时行情（只读，不回落真打上游，避免轮询打爆数据源）
	quotes := make(map[string]*data.StockInfo, len(dash.FinalSignals))
	var mu sync.Mutex
	var wg sync.WaitGroup
	seen := make(map[string]bool, len(dash.FinalSignals))
	for _, sig := range dash.FinalSignals {
		if sig.Code == "" || seen[sig.Code] {
			continue
		}
		seen[sig.Code] = true
		wg.Add(1)
		go func(code string) {
			defer wg.Done()
			if info := s.quoteDisplay(code); info != nil {
				mu.Lock()
				quotes[code] = info
				mu.Unlock()
			}
		}(sig.Code)
	}
	wg.Wait()
	live, pruned := filterStaleSignals(dash.FinalSignals, quotes)
	if pruned > 0 {
		log.Printf("[server] /api/signals 撤下 %d 条失效信号(仅展示层,不影响日志/存储)", pruned)
	}
	out := toFixSignals(live)
	// 逐票补充实时现价与涨跌幅（忽略失败，保留信号触发价兜底）
	for i := range out {
		if info, ok := quotes[out[i].Code]; ok && info != nil {
			out[i].Price = info.Price
			out[i].ChangePct = info.ChangePct
		}
	}
	writeJSON(w, 200, out)
}

// fixKLine 前端 K 线单条数据格式。
// （fixKLine is one frontend K-line bar.）
type fixKLine struct {
	Date   string  `json:"date"`   // 交易日（2006-01-02）
	Open   float64 `json:"open"`   // 开盘价（元，2 位小数）
	High   float64 `json:"high"`   // 最高价（元，2 位小数）
	Low    float64 `json:"low"`    // 最低价（元，2 位小数）
	Close  float64 `json:"close"`  // 收盘价（元，2 位小数）
	Volume float64 `json:"volume"` // 成交量（股，取整）
	Amount float64 `json:"amount"` // 成交额（元，取整）
}

// fixMinutePoint 分时数据点。MACD 三值由后端按分钟K线收盘价整条计算。
// fixMinutePoint is one intraday (分时) point; MACD values are computed on the whole minute series.
type fixMinutePoint struct {
	Time   string  `json:"time"`   // 时间（2006-01-02 15:04）
	Open   float64 `json:"open"`   // 开盘价
	High   float64 `json:"high"`   // 最高价
	Low    float64 `json:"low"`    // 最低价
	Close  float64 `json:"close"`  // 收盘价
	Volume float64 `json:"volume"` // 成交量（股）
	Amount float64 `json:"amount"` // 成交额（元）
	DIF    float64 `json:"dif"`    // MACD DIF
	DEA    float64 `json:"dea"`    // MACD DEA
	BAR    float64 `json:"bar"`    // MACD 柱（2*(DIF-DEA)）
}

// handleFixMinute 处理 GET /api/minute 请求，返回个股分钟级分时 + 成交量 + MACD。
// 参数：code 必填；scale 分钟数（默认 1）；count 点数（默认 241，即一整交易日分钟数）。
// 返回 { code, name, prev_close, points: [...] }。
func (s *Server) handleFixMinute(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		writeJSON(w, 400, map[string]interface{}{"error": "缺少 code 参数"})
		return
	}
	scale := 1
	if raw := r.URL.Query().Get("scale"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			scale = n
		}
	}
	count := 241
	if raw := r.URL.Query().Get("count"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			count = n
		}
	}
	if count > 3000 {
		count = 3000
	}

	var klines []data.KLine
	var err error
	if s.dc != nil {
		klines, err = s.dc.GetMinuteKLine(code, scale, count)
	} else if s.market != nil {
		klines, err = s.market.GetTencentMinuteKLine(code, scale, count)
	}
	if err != nil || len(klines) == 0 {
		log.Printf("[server] /api/minute %s 获取失败: %v", code, err)
		writeJSON(w, 200, map[string]interface{}{"code": code, "name": "", "prev_close": 0, "points": []fixMinutePoint{}})
		return
	}

	macd := data.CalcMACDSeries(klines)
	points := make([]fixMinutePoint, 0, len(klines))
	for i, k := range klines {
		m := macd[i]
		points = append(points, fixMinutePoint{
			Time:   k.Date.Format("2006-01-02 15:04"),
			Open:   r2(k.Open),
			High:   r2(k.High),
			Low:    r2(k.Low),
			Close:  r2(k.Close),
			Volume: r0(k.Volume),
			Amount: r0(k.Amount),
			DIF:    math.Round(m.DIF*100) / 100,
			DEA:    math.Round(m.DEA*100) / 100,
			BAR:    math.Round(m.Bar*100) / 100,
		})
	}

	// 昨收价：优先取分时数据首根之前最近一根日线收盘，缺省用首根开盘价
	prevClose := 0.0
	if len(klines) > 0 && klines[0].Open > 0 {
		prevClose = klines[0].Open
	}
	if s.dc != nil {
		if daily, derr := s.dc.GetKLine(code, "101", 2); derr == nil && len(daily) > 0 {
			prevClose = daily[len(daily)-1].Close
		}
	} else if s.market != nil {
		if daily, derr := s.market.GetSinaKLine(code, 2); derr == nil && len(daily) > 0 {
			prevClose = daily[len(daily)-1].Close
		}
	}

	writeJSON(w, 200, map[string]interface{}{
		"code":       code,
		"name":       s.stockName(code),
		"prev_close": r2(prevClose),
		"points":     points,
	})
}

// stockName 返回个股名称（持仓记录已知则用之，否则返回空串）。
func (s *Server) stockName(code string) string {
	if s.rpt != nil {
		for _, h := range s.rpt.List() {
			if h.Code == code && h.Name != "" {
				return h.Name
			}
		}
	}
	return ""
}

// handleFixKLine 处理 GET /api/kline 请求，返回个股 K 线数据。
// 参数：code 必填（股票代码）；period 周期（默认 "101" 日线）；count 数量（默认 90，上限 500）。
// 数据源：DataCoordinator（新浪日线 → 东财）。
func (s *Server) handleFixKLine(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		writeJSON(w, 400, map[string]interface{}{"error": "缺少 code 参数"})
		return
	}
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "101"
	}
	count := 90
	if raw := r.URL.Query().Get("count"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			count = n
		}
	}
	if count > 500 {
		count = 500
	}

	var klines []data.KLine
	var err error
	if s.dc != nil {
		klines, err = s.dc.GetKLine(code, period, count)
	} else if s.market != nil {
		klines, err = s.market.GetKLine(code, period, count)
	}
	if err != nil || len(klines) == 0 {
		log.Printf("[server] /api/kline %s 获取失败: %v", code, err)
		writeJSON(w, 200, []fixKLine{})
		return
	}

	out := make([]fixKLine, 0, len(klines))
	for _, k := range klines {
		out = append(out, fixKLine{
			Date:   k.Date.Format("2006-01-02"),
			Open:   r2(k.Open),
			High:   r2(k.High),
			Low:    r2(k.Low),
			Close:  r2(k.Close),
			Volume: r0(k.Volume),
			Amount: r0(k.Amount),
		})
	}
	writeJSON(w, 200, out)
}

// handleFixStatus 处理 GET /api/status 请求，返回系统运行状态。
// 包含：运行时长、当前交易时段（早盘/午盘/非交易）、信号数量、扫描统计信息。
func (s *Server) handleFixStatus(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.startTime).Round(time.Second).String()
	userID := requestUserID(r)
	dash := s.dashFor(userID)
	rawCount := 0
	matCount := 0
	hotCount := 0
	finalCount := 0
	session := 99
	if dash != nil {
		rawCount = len(dash.NewsEvents)
		matCount = rawCount
		hotCount = len(dash.HotSectors)
		finalCount = len(dash.FinalSignals)
	}
	now := time.Now()
	// 交易时段判定：1=早盘(9:00-11:30)，3=午盘(13:00-15:00)，0=盘前，2=午间休市/盘后
	switch {
	case now.Hour() >= 9 && now.Hour() < 11 || (now.Hour() == 11 && now.Minute() < 30):
		session = 1
	case now.Hour() >= 13 && now.Hour() < 15:
		session = 3
	case now.Hour() < 9:
		session = 0
	default:
		session = 2
	}
	writeJSON(w, 200, map[string]interface{}{
		"uptime":        uptime,
		"session":       session,
		"in_trade_time": session == 1 || session == 3,
		"signal_count":  finalCount,
		"scan_stats": map[string]interface{}{
			"total_stocks":     rawCount,
			"hot_sector_count": hotCount,
			"raw_signals":      rawCount,
			"material_events":  matCount,
			"final_signals":    finalCount,
		},
	})
}

// handleFixEngineHealth 处理 GET /api/engine_health 请求，返回流程引擎各子系统健康状况。
// （handleFixEngineHealth handles GET /api/engine_health, returning the health status of each engine subsystem.）
func (s *Server) handleFixEngineHealth(w http.ResponseWriter, r *http.Request) {
	ctrl := s.ctrlFor(requestUserID(r))
	// 模拟盘子系统：流程引擎的信号/估值分发目标，账户级引擎存在且启用即健康。
	// English: paper subsystem — the pipeline's signal/mark dispatch target; healthy when the
	// account-level engine exists and is enabled.
	pe := s.paperEngineFor(requestUserID(r))
	status := map[string]bool{
		"news_agent":      ctrl != nil && ctrl.GetAllNewsEvents() != nil,
		"strategy_engine": ctrl != nil && ctrl.GetStageRecords() != nil,
		"sector_agent":    ctrl != nil && ctrl.GetHotRecords() != nil,
		"combat_agent":    ctrl != nil && ctrl.GetSignalLogs() != nil,
		"llm":             ctrl != nil && s.runtimeLLM != "",
		"ths":             s.ths != nil,
		"fetcher":         s.fetcher != nil,
		"aggregator":      ctrl != nil,
		"paper":           pe != nil && pe.Enabled(),
	}
	writeJSON(w, 200, status)
}

// handleFixAlerts 处理 GET /api/alerts 请求，返回系统告警列表。
// 数据来源：消息中心持久化存储（引擎每轮同步 止盈/止损/策略信号/持仓提示）。
// 未接入引擎时回退到实时看板 + 持仓日志。结果按时间倒序排列。
func (s *Server) handleFixAlerts(w http.ResponseWriter, r *http.Request) {
	ctrl := s.ctrlFor(requestUserID(r))
	if ctrl != nil {
		msgs := ctrl.GetMessages()
		if msgs == nil {
			msgs = []data.MessageItem{}
		}
		out := make([]map[string]interface{}, 0, len(msgs))
		for _, m := range msgs {
			name := m.Name
			// 消息中心名称为空或等于代码时，用行情权威名回填（一次性迁移，持久化到存储）
			if name == "" || name == m.Code {
				if info, err := s.quote(m.Code); err == nil && info.Name != "" && info.Name != m.Code {
					name = info.Name
					ctrl.RefreshMessageName(m.Code, name)
				}
			}
			out = append(out, map[string]interface{}{
				"id":        m.ID,
				"code":      m.Code,
				"name":      name,
				"type":      m.Level,
				"level":     m.Level,
				"action":    m.Action,
				"strategy":  m.Strategy,
				"time":      m.Time,
				"title":     m.Title,
				"body":      m.Body,
				"direction": m.Direction,
			})
		}
		writeJSON(w, 200, out)
		return
	}

	dash := s.dashFor(requestUserID(r))
	if dash == nil {
		writeJSON(w, 200, []map[string]interface{}{})
		return
	}
	out := make([]map[string]interface{}, 0)
	// 兜底路径：先用看板告警信号，再补充持仓日志中的在持/已平仓记录
	for _, a := range dash.AlertSignals {
		lvl := a.AlertType
		if lvl == "" {
			lvl = "策略信号"
		}
		item := map[string]interface{}{
			"id":        a.ID,
			"code":      a.Code,
			"name":      a.Name,
			"type":      lvl,
			"level":     lvl,
			"action":    a.Action,
			"strategy":  a.Strategy,
			"time":      a.GeneratedAt.Format("15:04:05"),
			"title":     fmt.Sprintf("%s %s", lvl, a.Code),
			"body":      a.Reason,
			"direction": a.Direction,
		}
		out = append(out, item)
	}
	for _, l := range s.rpt.ListFor(requestUserID(r)) {
		// 仅展示持仓中或已平仓的记录，平仓记录用当前盈亏补全提示文本
		if l.Status == "持仓中" || l.ExitAt != nil {
			alertType := "持仓提示"
			pct := ""
			if l.ProfitPct != nil {
				pct = fmt.Sprintf("%.1f%%", *l.ProfitPct)
			}
			item := map[string]interface{}{
				"id":        l.SignalID,
				"code":      l.Code,
				"name":      l.Name,
				"type":      alertType,
				"level":     alertType,
				"action":    l.Status,
				"strategy":  l.Strategy,
				"time":      l.EntryAt.Format("15:04:05"),
				"title":     fmt.Sprintf("%s %s", l.Status, l.Code),
				"body":      fmt.Sprintf("策略:%s 入场:%.2f %s", l.Strategy, l.EntryPrice, pct),
				"direction": l.Direction,
			}
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["time"].(string) > out[j]["time"].(string)
	})
	writeJSON(w, 200, out)
}

// handleClearAlerts 处理 DELETE /api/alerts 请求：清空消息中心全部消息（按账号）。
func (s *Server) handleClearAlerts(w http.ResponseWriter, r *http.Request) {
	ctrl := s.ctrlFor(requestUserID(r))
	if ctrl == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	ctrl.ClearMessages()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDeleteAlert 处理 DELETE /api/alerts/{id} 请求：手工删除单条消息（按账号）。
func (s *Server) handleDeleteAlert(w http.ResponseWriter, r *http.Request) {
	ctrl := s.ctrlFor(requestUserID(r))
	if ctrl == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing id"})
		return
	}
	ctrl.DeleteMessage(id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleSectorHotRecords 处理 GET /api/sector/hot/records 请求，返回当日热点板块轮次记录（按账号）。
func (s *Server) handleSectorHotRecords(w http.ResponseWriter, r *http.Request) {
	ctrl := s.ctrlFor(requestUserID(r))
	if ctrl == nil {
		writeJSON(w, 200, []data.HotRecord{})
		return
	}
	recs := ctrl.GetHotRecords()
	if recs == nil {
		recs = []data.HotRecord{}
	}
	// 就地倒序，最新轮次的热点记录排在最前
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	writeJSON(w, 200, recs)
}

// fixHolding 前端持仓格式的结构体。
// 包含持仓数量、成本价、现价、盈亏比例、止盈止损价等字段。
type fixHolding struct {
	Code          string       `json:"code"`            // 股票代码
	Name          string       `json:"name"`            // 股票名称
	Quantity      float64      `json:"quantity"`        // 持仓数量
	CostPrice     float64      `json:"cost_price"`      // 持仓成本价
	CurPrice      float64      `json:"cur_price"`       // 最新现价
	ChangePct     float64      `json:"change_pct"`      // 当日涨跌幅（%）
	PnlPct        float64      `json:"pnl_pct"`         // 持仓盈亏比例（%）
	TakeProfitPct float64      `json:"take_profit_pct"` // 止盈百分比设置
	StopLossPct   float64      `json:"stop_loss_pct"`   // 止损百分比设置
	SignalActive  bool         `json:"signal_active"`   // 是否有活跃信号
	NSscore       float64      `json:"n_score"`         // N形策略评分
	DragonScore   float64      `json:"dragon_score"`    // 破局龙策略评分
	DbScore       float64      `json:"db_score"`        // 双凸策略评分
	DrScore       float64      `json:"dr_score"`        // 龙回头策略评分
	MScore        float64      `json:"m_score"`         // 动量策略评分
	TakeProfit    float64      `json:"take_profit"`     // 止盈目标价
	StopLoss      float64      `json:"stop_loss"`       // 止损价位
	HighestPrice  float64      `json:"highest_price"`   // 移动止盈基准（阶段最高价，开仓=入场价）
	RealizedPnl   float64      `json:"realized_pnl"`    // 该标的累计已实现盈亏（元）
	Lots          []report.Lot `json:"lots,omitempty"`  // 加仓批次明细
}

// handleFixGetHoldings 处理 GET /api/holdings 请求，返回当前持仓列表。
// 从执行日志中筛选状态为"持仓中"的记录，实时拉取最新股价计算盈亏。
// 同时关联信号数据，标注持仓是否有活跃信号。
func (s *Server) handleFixGetHoldings(w http.ResponseWriter, r *http.Request) {
	userID := requestUserID(r)
	logs := s.rpt.ListFor(userID)
	holdings := make([]fixHolding, 0)
	for _, l := range logs {
		if l.Status != "持仓中" {
			continue
		}
		holdings = append(holdings, s.buildHolding(l, userID))
	}
	writeJSON(w, 200, map[string]interface{}{
		"holdings":           holdings,
		"available_balance":  0,
		"total_realized_pnl": s.rpt.TotalRealizedPnl(userID),
	})
}

// buildHolding 将一条持仓执行日志组装为前端 fixHolding 格式：
// 实时拉取股价计算盈亏与当日涨跌，关联聚合器的评分/活跃信号，附上加仓批次明细。
func (s *Server) buildHolding(l report.ExecLog, userID string) fixHolding {
	cur := l.EntryPrice
	chg := 0.0
	pnl := 0.0
	name := l.Name
	// 实时拉取股价；失败时回退到开仓价（盈亏视为 0）
	if info, err := s.quote(l.Code); err == nil {
		cur = info.Price
		chg = info.ChangePct
		name = info.Name
		// 顺带回填仓库里的旧名/空名，提升消息与展示一致性
		if name != "" && name != l.Name {
			s.rpt.Update(l.SignalID, func(x *report.ExecLog) { x.Name = name })
		}
	}
	// 盈亏比例 = (现价 - 成本价) / 成本价 * 100
	if cur > 0 && l.EntryPrice > 0 {
		pnl = (cur - l.EntryPrice) / l.EntryPrice * 100
	}
	qty := l.Quantity
	if qty <= 0 {
		qty = 1
	}
	h := fixHolding{
		Code:          l.Code,
		Name:          name,
		Quantity:      qty,
		CostPrice:     r2(l.EntryPrice),
		CurPrice:      r2(cur),
		ChangePct:     r2(chg),
		PnlPct:        r2(pnl),
		TakeProfitPct: r2(l.TakeProfitPct),
		StopLossPct:   r2(l.StopLossPct),
		TakeProfit:    r2(l.EntryPrice * (1 + l.TakeProfitPct/100)),
		StopLoss:      r2(l.EntryPrice * (1 - l.StopLossPct/100)),
		HighestPrice:  r2(l.HighestPrice),
		RealizedPnl:   r2(l.RealizedPnl),
		Lots:          holdingLots(l),
	}
	dash := s.dashFor(userID)
	if dash != nil {
		// 优先取 8a/8b 持续打分分数；无打分记录时回退到最终信号置信度
		if sc, ok := dash.Scores[l.Code]; ok {
			h.SignalActive = sc.SignalActive
			h.NSscore = sc.NScore
			h.DragonScore = sc.DragonScore
			h.MScore = sc.MomentumScore
			h.DbScore = sc.DoubleBumpScore
			h.DrScore = sc.DragonReturnScore
		} else {
			for _, fs := range dash.FinalSignals {
				if fs.Code == l.Code {
					h.SignalActive = true
					h.NSscore = fs.Confidence * 100
					break
				}
			}
		}
	}
	return h
}

// holdingLots 返回持仓的加仓批次明细；无批次记录的旧数据用一条合成批次兜底
// （以现有开仓价/数量为准），保证前端明细始终有数据可展示。
func holdingLots(l report.ExecLog) []report.Lot {
	if len(l.Lots) > 0 {
		return l.Lots
	}
	qty := l.Quantity
	if qty <= 0 {
		qty = 1
	}
	return []report.Lot{{Price: l.EntryPrice, Quantity: qty, At: l.EntryAt}}
}

// fixSetHoldingsReq 手动设置持仓的请求结构体：待同步的持仓列表 + 可用资金。
type fixSetHoldingsReq struct {
	Holdings         []fixHolding `json:"holdings"`
	AvailableBalance float64      `json:"available_balance"`
}

// handleFixSetHoldings 处理 POST /api/holdings 请求，手动设置/同步持仓信息。
// 逻辑：遍历请求中的持仓列表 → 创建或更新执行日志 → 删除已不在列表中的手动持仓。
func (s *Server) handleFixSetHoldings(w http.ResponseWriter, r *http.Request) {
	var req fixSetHoldingsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	uid := requestUserID(r)
	// 手动持仓 ID 按账号隔离：code_userID_fix（空账号兼容旧格式 code_fix）
	fixSuffix := "_fix"
	if uid != "" {
		fixSuffix = "_" + uid + "_fix"
	}
	_ = req.AvailableBalance
	for _, h := range req.Holdings {
		// 定位持仓：优先手动 _fix；无 _fix 时回退到同代码的现有持仓（兼容信号创建的持仓，避免重复建档）
		id := h.Code + fixSuffix
		if s.rpt.FindBySignalID(id) == nil {
			if heldID := s.heldSignalIDByCode(h.Code, uid); heldID != "" {
				id = heldID
			}
		}
		existing := s.rpt.FindBySignalID(id)
		if existing == nil {
			s.rpt.LogSignal(id, h.Code, h.Name, "做多", "手动", h.CostPrice, h.TakeProfitPct, h.StopLossPct)
			s.rpt.AddLot(id, h.CostPrice, h.Quantity)
			s.rpt.Update(id, func(l *report.ExecLog) { l.UserID = uid })
		} else {
			now := time.Now()
			s.rpt.Update(id, func(l *report.ExecLog) {
				l.UserID = uid
				// 重新买入：若该记录此前已平仓/删除，先重置为持仓中并清空平仓信息，
				// 否则会被 handleFixGetHoldings 的“持仓中”过滤掉，导致刷新后持仓消失。
				if l.Status != "持仓中" {
					l.Status = "持仓中"
					l.ExitAt = nil
					l.ExitPrice = nil
					l.ProfitPct = nil
				}
				l.TakeProfitPct = h.TakeProfitPct
				l.StopLossPct = h.StopLossPct
				if h.Name != "" {
					l.Name = h.Name
				}
				// 仅当成本/数量被显式改动时才重建批次明细（编辑/覆盖）；
				// 否则保留 加仓 接口维护的真实批次，避免整表同步时误清零明细。
				costChanged := math.Abs(h.CostPrice-l.EntryPrice) > 0.005 ||
					math.Abs(h.Quantity-l.Quantity) > 0.5
				if len(l.Lots) == 0 || costChanged {
					l.EntryPrice = h.CostPrice
					l.Quantity = h.Quantity
					l.Lots = []report.Lot{{Price: h.CostPrice, Quantity: h.Quantity, At: now}}
				}
			})
		}
	}
	// 删除不在本次提交中的本账号手动持仓
	for _, l := range s.rpt.ListFor(uid) {
		if strings.HasSuffix(l.SignalID, fixSuffix) {
			found := false
			for _, h := range req.Holdings {
				if h.Code+fixSuffix == l.SignalID {
					found = true
					break
				}
			}
			if !found {
				s.rpt.Delete(l.SignalID)
			}
		}
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// addHoldingLotReq 加仓请求体：加仓价格与数量。
type addHoldingLotReq struct {
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
}

// heldSignalIDByCode 返回指定账号某代码当前最末一笔"持仓中"记录的信号 ID；无持仓返回空串。
func (s *Server) heldSignalIDByCode(code, userID string) string {
	for _, l := range s.rpt.HeldPositionsFor(userID) {
		if l.Code == code {
			return l.SignalID
		}
	}
	return ""
}

// handleFixAddHoldingLot 处理 POST /api/holdings/{code}/add 请求：对持仓增量买入加仓。
// 按代码定位持仓（兼容手动 _fix 与信号创建的持仓，避免产生重复记录），
// 追加一笔批次并重算加权平均成本；该股无持仓时直接创建手动持仓作为首笔。
func (s *Server) handleFixAddHoldingLot(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	var req addHoldingLotReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if code == "" || req.Price <= 0 || req.Quantity <= 0 {
		writeError(w, 400, "code and positive price/quantity required")
		return
	}
	uid := requestUserID(r)
	id := s.heldSignalIDByCode(code, uid)
	if id == "" {
		name := code
		if info, err := s.quote(code); err == nil && info.Name != "" {
			name = info.Name
		}
		// 新开仓使用唯一 ID（code+t时间戳），避免与已平仓的旧 _fix 记录复用同一 ID 导致批次错乱
		id = code + "_fix_" + strconv.FormatInt(time.Now().UnixNano(), 10)
		s.rpt.LogSignal(id, code, name, "做多", "手动", req.Price, 8, 5)
		s.rpt.Update(id, func(l *report.ExecLog) { l.UserID = uid })
		log.Printf("[server] 手动开仓 %s %s 价%.3f (id=%s uid=%s)", code, name, req.Price, id, uid)
	}
	s.rpt.AddLot(id, req.Price, req.Quantity)
	log.Printf("[server] 加仓 %s 价%.3f 量%.0f", code, req.Price, req.Quantity)
	for _, l := range s.rpt.HeldPositionsFor(uid) {
		if l.Code == code {
			writeJSON(w, 200, map[string]interface{}{"holding": s.buildHolding(l, uid)})
			return
		}
	}
	writeJSON(w, 200, map[string]interface{}{"holding": nil})
}

// handleFixSetCost 处理 POST /api/holdings/{code}/cost 请求：直接更新持仓成本价。
func (s *Server) handleFixSetCost(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	var req addHoldingLotReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	uid := requestUserID(r)
	id := s.heldSignalIDByCode(code, uid)
	if id == "" {
		writeError(w, 404, "no position held for code")
		return
	}
	if req.Price <= 0 {
		writeError(w, 400, "positive price required")
		return
	}
	s.rpt.SetCostBasis(id, req.Price)
	log.Printf("[server] 更新成本 %s 成本%.3f", code, req.Price)
	for _, l := range s.rpt.HeldPositionsFor(uid) {
		if l.Code == code {
			writeJSON(w, 200, map[string]interface{}{"holding": s.buildHolding(l, uid)})
			return
		}
	}
	writeJSON(w, 200, map[string]interface{}{"holding": nil})
}

// closeHoldingReq 清仓请求体：清仓价。
type closeHoldingReq struct {
	Price float64 `json:"price"`
}

// sellHoldingReq 减仓请求体：卖出价与卖出数量。
type sellHoldingReq struct {
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
}

// handleFixSellHolding 处理 POST /api/holdings/{code}/sell 请求：对该持仓减仓卖出部分数量。
// 按代码定位持仓，调用 SellLot 以 FIFO 扣减批次并重算加权平均成本；
// 卖出数量不足或超过当前持仓数量时返回 400。全部卖完时自动平仓（记录盈亏）。
// 返回减仓后更新过的持仓（供前端原地替换）。
func (s *Server) handleFixSellHolding(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	var req sellHoldingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if code == "" || req.Price <= 0 || req.Quantity <= 0 {
		writeError(w, 400, "code and positive price/quantity required")
		return
	}
	uid := requestUserID(r)
	var target report.ExecLog
	var targetID string
	for _, l := range s.rpt.HeldPositionsFor(uid) {
		if l.Code == code {
			target = l
			targetID = l.SignalID
			break
		}
	}
	if targetID == "" {
		writeError(w, 404, "no position held for code")
		return
	}
	qty := target.Quantity
	if qty <= 0 {
		qty = 1
	}
	if req.Quantity > qty {
		writeError(w, 400, "sell quantity exceeds held quantity")
		return
	}
	// 全部卖完时走清仓路径（记录完整盈亏与平仓状态）
	if req.Quantity >= qty {
		s.rpt.LogExit(targetID, req.Price, "手动减仓(全清)")
		log.Printf("[server] 减仓(全清) %s 价%.3f 量%.0f", code, req.Price, req.Quantity)
		writeJSON(w, 200, map[string]interface{}{"holding": nil, "closed": true, "code": code})
		return
	}
	s.rpt.SellLot(targetID, req.Price, req.Quantity)
	log.Printf("[server] 减仓 %s 价%.3f 量%.0f (剩余持仓)", code, req.Price, req.Quantity)
	for _, l := range s.rpt.HeldPositionsFor(uid) {
		if l.Code == code {
			writeJSON(w, 200, map[string]interface{}{"holding": s.buildHolding(l, uid)})
			return
		}
	}
	writeJSON(w, 200, map[string]interface{}{"holding": nil})
}

// handleFixCloseHolding 处理 POST /api/holdings/{code}/close 请求：按指定价格清仓该股持仓。
// 定位持仓（兼容手动 _fix 与信号持仓），调用 LogExit 记录真实盈亏并标记已平仓；
// 返回盈亏金额（(清仓价-成本)×数量）与盈亏比例，供前端展示。
func (s *Server) handleFixCloseHolding(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	var req closeHoldingReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if code == "" || req.Price <= 0 {
		writeError(w, 400, "code and positive close price required")
		return
	}
	var target report.ExecLog
	for _, l := range s.rpt.HeldPositionsFor(requestUserID(r)) {
		if l.Code == code {
			target = l
			break
		}
	}
	if target.SignalID == "" {
		writeError(w, 404, "no position held for code")
		return
	}
	qty := target.Quantity
	if qty <= 0 {
		qty = 1
	}
	amount := (req.Price - target.EntryPrice) * qty
	pct := 0.0
	if target.EntryPrice > 0 {
		pct = (req.Price - target.EntryPrice) / target.EntryPrice * 100
	}
	s.rpt.LogExit(target.SignalID, req.Price, "手动清仓")
	log.Printf("[server] 清仓 %s 价%.3f 量%.0f 成本%.3f 盈亏¥%.2f(%.2f%%)", code, req.Price, qty, target.EntryPrice, amount, pct)
	writeJSON(w, 200, map[string]interface{}{
		"status":        "ok",
		"code":          code,
		"name":          target.Name,
		"quantity":      qty,
		"cost_price":    r2(target.EntryPrice),
		"close_price":   r2(req.Price),
		"profit_pct":    r2(pct),
		"profit_amount": r2(amount),
	})
}

// thsTopFallbackBoards 返回同花顺首屏 top 板块列表（带 60s 缓存），
// 兜底板块每分钟轮动一次（前端 3s 轮询 /api/sector/hot 时不再逐次请求同花顺）。
func (s *Server) thsTopFallbackBoards() []data.SectorInfo {
	s.thsMu.Lock()
	defer s.thsMu.Unlock()
	if s.ths == nil {
		return nil
	}
	// 缓存命中（60s 内）：直接复用，避免高频刷新同花顺页面
	if len(s.thsBoards) > 0 && time.Since(s.thsBoardsAt) < time.Minute {
		out := make([]data.SectorInfo, len(s.thsBoards))
		copy(out, s.thsBoards)
		return out
	}
	list, err := s.ths.GetTopBoards()
	if err != nil {
		log.Printf("[server] 同花顺 top板块获取失败: %v", err)
		return nil
	}
	s.thsBoards = list
	s.thsBoardsAt = time.Now()
	out := make([]data.SectorInfo, len(list))
	copy(out, list)
	return out
}

// handleFixSectorHot 处理 GET /api/sector/hot 请求，返回热门板块列表。
// 数据出口：同花顺首屏 top-20 板块表（一级行业+概念），含同花顺涨跌幅/主力净流入。
// 优先展示 LLM 归因出的热点板块（仅保留能匹配到同花顺 top-20 的板块）；
// 当 LLM 未筛选出任何板块时，用同花顺板块行情表（行业+概念）兜底，取涨幅前十，
// 每分钟刷新一次实现板块轮动。
func (s *Server) handleFixSectorHot(w http.ResponseWriter, r *http.Request) {
	dash := s.dashFor(requestUserID(r))
	// 同花顺板块行情表（首屏 top-20，按涨跌幅排序），按名称精确匹配
	sectorMap := map[string]data.SectorInfo{}
	thsBoards := s.thsTopFallbackBoards()
	for _, si := range thsBoards {
		sectorMap[si.Name] = si
	}
	out := make([]map[string]interface{}, 0)
	if dash != nil {
		for _, sec := range dash.HotSectors {
			si, ok := sectorMap[sec.Name]
			if !ok {
				continue
			}
			newsTitles := sec.NewsTitles
			if newsTitles == nil {
				newsTitles = []string{}
			}
			out = append(out, map[string]interface{}{
				"name":          sec.Name,
				"code":          si.Code,
				"score":         r0(sec.Score),
				"change_pct":    r2(si.ChangePct),
				"d1":            0,
				"reason":        sec.Reason,
				"reason_detail": sec.Reason,
				"direction":     sec.Direction,
				"limitup_cnt":   si.LimitupCnt,
				"net_inflow":    r2(si.NetInflow),
				"news_titles":   newsTitles,
			})
		}
	}
	// LLM 未筛选出热点板块（或匹配不到同花顺 top-20）：拿同花顺板块+概念兜底
	if len(out) == 0 {
		// 按涨跌幅从高到低取前十，实现轮动
		top := append([]data.SectorInfo(nil), thsBoards...)
		sort.SliceStable(top, func(i, j int) bool { return top[i].ChangePct > top[j].ChangePct })
		if len(top) > 10 {
			top = top[:10]
		}
		for _, si := range top {
			out = append(out, map[string]interface{}{
				"name":          si.Name,
				"code":          si.Code,
				"score":         0,
				"change_pct":    r2(si.ChangePct),
				"d1":            0,
				"reason":        "",
				"reason_detail": "同花顺板块兜底（LLM 本轮未归因出热点板块）",
				"direction":     "中性",
				"limitup_cnt":   si.LimitupCnt,
				"net_inflow":    r2(si.NetInflow),
				"news_titles":   []string{},
			})
		}
	}
	writeJSON(w, 200, out)
}

// quote 统一行情入口：优先读 fetcher 5s 快照（新浪批量，一次全池），
// 缺失时走 DataCoordinator 新浪→同花顺→东财 三级降级链。
// 所有展示价格的 handler 一律调用本函数，保证跨页同一时刻价格一致。
func (s *Server) quote(code string) (*data.StockInfo, error) {
	if s.fetcher != nil {
		if snap := s.fetcher.Snapshot(); snap != nil {
			if si, ok := snap.Stocks[code]; ok && si != nil && si.Price > 0 {
				return si, nil
			}
		}
	}
	if s.dc != nil {
		return s.dc.GetQuote(code)
	}
	return s.market.GetRealtimeQuote(code)
}

// quoteSnapshot 只读行情入口：仅从 fetcher 5s 快照取价，缺失不回落真打上游。
// 供高频展示接口（/api/signals、/api/snapshot、/api/snapshot/hot 等）使用，
// 避免前端轮询每次逐票打行情接口造成数据源洪峰（同一份后端结果跨设备一致）。
// （English: read-only quote from the fetcher 5s snapshot; does NOT fall back to live upstream
// calls. Used by high-frequency display endpoints so frontend polling never thunders the data
// sources, keeping results consistent across devices.）
func (s *Server) quoteSnapshot(code string) *data.StockInfo {
	if s.fetcher == nil {
		return nil
	}
	snap := s.fetcher.Snapshot()
	if snap == nil {
		return nil
	}
	si, ok := snap.Stocks[code]
	if !ok || si == nil || si.Price <= 0 {
		return nil
	}
	return si
}

// quoteDisplay 展示行情入口：优先读 fetcher 5s 快照（批量、跨页一致），
// 快照缺失（信号/热门个股刚出现尚未入池）时回落到 TTL 缓存的实时行情（s.quote），
// 保证 /api/signals、/api/snapshot/hot 等展示接口的现价/涨跌幅始终真实，而非 0.00%/陈旧价。
// 回落走 dc.GetQuote 的 5s TTL 缓存，同一股票在窗口内只打一次上游，不会造成洪峰。
// （English: display quote entry: prefers the fetcher 5s snapshot, and falls back to the
// TTL-cached live quote via s.quote when the stock is missing (a signal/hot stock that just
// appeared and hasn't joined the pool yet), so price/change are always real instead of 0.00%.）
func (s *Server) quoteDisplay(code string) *data.StockInfo {
	if si := s.quoteSnapshot(code); si != nil {
		return si
	}
	si, err := s.quote(code)
	if err != nil {
		return nil
	}
	return si
}

// handleFixSnapshot 处理 GET /api/snapshot 请求，返回指定个股或全部自选股的实时快照数据。
// 支持 ?codes=600519,000001 参数指定代码列表，不传则返回自选股列表中的所有个股。
func (s *Server) handleFixSnapshot(w http.ResponseWriter, r *http.Request) {
	codes := r.URL.Query().Get("codes")
	var stockList []string
	if codes != "" {
		stockList = strings.Split(codes, ",")
	} else {
		stockList = s.watchlist.List(requestUserID(r))
	}
	out := make([]map[string]interface{}, 0)
	for _, code := range stockList {
		info := s.quoteDisplay(code)
		if info == nil {
			continue
		}
		chg := info.ChangePct
		out = append(out, map[string]interface{}{
			"code":       info.Code,
			"name":       info.Name,
			"price":      r2(info.Price),
			"change_pct": r2(chg),
			"sector":     info.Sector,
		})
	}
	if len(out) == 0 {
		writeJSON(w, 200, []map[string]interface{}{})
		return
	}
	writeJSON(w, 200, out)
}

// handleFixHotSnapshot 处理 GET /api/snapshot/hot 请求，返回当前有信号的个股实时快照。
// 从 FinalSignals 中提取个股信息并拉取实时行情，去重后返回。
func (s *Server) handleFixHotSnapshot(w http.ResponseWriter, r *http.Request) {
	dash := s.dashFor(requestUserID(r))
	if dash == nil {
		writeJSON(w, 200, []map[string]interface{}{})
		return
	}
	out := make([]map[string]interface{}, 0)
	seen := map[string]bool{}
	for _, sig := range dash.FinalSignals {
		if seen[sig.Code] {
			continue
		}
		seen[sig.Code] = true
		info := s.quoteDisplay(sig.Code)
		price := sig.Price
		chg := 0.0
		if info != nil {
			price = info.Price
			chg = info.ChangePct
		}
		out = append(out, map[string]interface{}{
			"code":          sig.Code,
			"name":          sig.Name,
			"price":         r2(price),
			"change_pct":    r2(chg),
			"sector":        sig.Sector,
			"sector_reason": sig.Reason,
			"reason":        sig.Reason,
		})
	}
	writeJSON(w, 200, out)
}

// handleFixEvaluations 处理 GET /api/evaluations 请求，返回自选股的多维度评分评估数据。
// 包含 N-score、Dragon-score、DB-score、DR-score、M-score 五种评分及对应的通过阈值判断。
// 数据来源为 8a/8b 持续打分（dash.Scores），无打分记录时按 0 处理。
func (s *Server) handleFixEvaluations(w http.ResponseWriter, r *http.Request) {
	dash := s.dashFor(requestUserID(r))
	codes := s.watchlist.List(requestUserID(r))
	seen := map[string]bool{}
	out := make([]map[string]interface{}, 0)
	var scores map[string]combat_agent.StockScores
	if dash != nil {
		scores = dash.Scores
	}
	for _, code := range codes {
		if seen[code] {
			continue
		}
		seen[code] = true
		nScore := 0.0
		dragonScore := 0.0
		dbScore := 0.0
		drScore := 0.0
		mScore := 0.0
		sigActive := false
		if sc, ok := scores[code]; ok {
			nScore = sc.NScore
			dragonScore = sc.DragonScore
			dbScore = sc.DoubleBumpScore
			drScore = sc.DragonReturnScore
			mScore = sc.MomentumScore
			sigActive = sc.SignalActive
		}
		info := s.quoteDisplay(code)
		name := code
		price := 0.0
		chg := 0.0
		if info != nil {
			name = info.Name
			price = info.Price
			chg = info.ChangePct
		}
		out = append(out, map[string]interface{}{
			"code":          code,
			"name":          name,
			"price":         r2(price),
			"change_pct":    r2(chg),
			"n_score":       r0(nScore),
			"n_pass":        nScore >= 60,
			"dragon_score":  r0(dragonScore),
			"dragon_pass":   dragonScore >= 70,
			"db_score":      r0(dbScore),
			"db_pass":       dbScore >= 70,
			"dr_score":      r0(drScore),
			"dr_pass":       drScore >= 60,
			"m_score":       r0(mScore),
			"m_pass":        mScore >= 50,
			"signal_active": sigActive,
		})
	}
	if len(out) == 0 {
		writeJSON(w, 200, []map[string]interface{}{})
		return
	}
	writeJSON(w, 200, out)
}

// handleFixIPOCalendar 处理 GET /api/ipo/calendar 请求，返回新股发行/上市日历数据。
// 数据来源：东方财富 IPO 日历接口（按天缓存，每天首次请求才远程拉取）。
func (s *Server) handleFixIPOCalendar(w http.ResponseWriter, r *http.Request) {
	list, err := s.ipoCalendar(time.Now())
	if err != nil {
		log.Printf("[ipo] 获取失败: %v", err)
		writeJSON(w, 200, []map[string]interface{}{})
		return
	}
	out := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		out = append(out, map[string]interface{}{
			"code":         item.Code,
			"name":         item.Name,
			"listing_date": item.ListingDate,
			"ipo_date":     item.IPODate,
			"issue_price":  item.IssuePrice,
			"list_status":  item.ListStatus,
		})
	}
	writeJSON(w, 200, out)
}

// handleFixStockLookup 处理 GET /api/stock/lookup 请求，根据股票代码查询实时行情。
// 参数：?code=600519，返回代码、名称和最新价格。
func (s *Server) handleFixStockLookup(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, 400, "code required")
		return
	}
	info, err := s.quote(code)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"code": code, "name": "", "price": 0})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"code":  info.Code,
		"name":  info.Name,
		"price": info.Price,
	})
}

// handleFixDepth 处理 GET /api/depth/{code} 请求，返回个股盘口快照与派生因子。
// 免费数据源返回五档（Bids/Asks 按十档预分配，6~10 档为零值）；
// 战法可读 factors 字段（买卖压力、委比、封单量、价差、报价覆盖范围）。
func (s *Server) handleFixDepth(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if code == "" {
		writeError(w, 400, "code required")
		return
	}
	ob, err := s.market.GetOrderBook(code)
	if err != nil {
		writeError(w, 502, "depth unavailable: "+err.Error())
		return
	}
	levels := data.DepthLevels
	writeJSON(w, 200, map[string]interface{}{
		"code":       ob.Code,
		"name":       ob.Name,
		"price":      ob.Price,
		"prev_close": ob.PrevClose,
		"time":       ob.Time,
		"source":     ob.Source,
		"bids":       ob.Bids,
		"asks":       ob.Asks,
		"levels":     levels,
		"factors":    ob.Factors(5),
	})
}

// 兼容三种来源格式：已格式化的日期字符串（原样或截断）、epoch 秒（数字或纯数字字符串）、
// 以及纯日期 "YYYY-MM-DD"。防止任何源的 epoch 秒时间直接透传给前端展示成乱码。
// normalizeNewsTime coerces a news timestamp into "YYYY-MM-DD HH:MM". It accepts formatted
// date strings (passed through/truncated), epoch seconds (numeric or numeric-string), and
// bare dates, so raw epoch seconds can never leak to the frontend as garbage.
func normalizeNewsTime(datetime interface{}) string {
	switch v := datetime.(type) {
	case nil:
		return ""
	case float64:
		return newsTimeFromEpoch(int64(v))
	case int64:
		return newsTimeFromEpoch(v)
	case int:
		return newsTimeFromEpoch(int64(v))
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return ""
		}
		if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
			return newsTimeFromEpoch(sec)
		}
		// 已格式化：优先取 "MM-DD HH:MM"（长度足够时截掉秒）
		if len(s) >= 16 {
			return s[:16]
		}
		return s
	default:
		return fmt.Sprint(v)
	}
}

// newsTimeFromEpoch 将 epoch 秒转为 "YYYY-MM-DD HH:MM"。
// 非法/越界值返回空串，避免 0001-01-01 之类的脏数据展示。
func newsTimeFromEpoch(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).Format("2006-01-02 15:04")
}

// handleFixNews 处理 GET /api/news 请求，返回热点资讯（混合数据源，独立于 LLM Stage）。
// 数据来源（按序混合去重）：
//  1. 原始新闻流：同花顺快讯（主源）+ 新浪财经（兜底），"有啥刷啥"不依赖 LLM；
//  2. 已打标事件：引擎持久化的新闻事件（聚合器展示缓存，跨轮次累计）；
//  3. 宏观日历事件：自动生成，影响级别高/中/低，仅显示近 14 天内。
//
// 原始新闻（未打标）以 source 区分展示，已打标事件带 direction/sectors/stocks 等标签。
// newsTTL 资讯接口 TTL 缓存时长：30 秒。
// 资讯页 3s 轮询时命中缓存，避免每次请求直接打同花顺/新浪新闻源造成数据源洪峰。
// （English: news endpoint TTL cache duration, 30s. The hotspot page polls every 3s; the cache
// absorbs the bursts so the news sources are not hit on every request.）
const newsTTL = 30 * time.Second

func (s *Server) handleFixNews(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	cacheKey := "all"
	if !all {
		cacheKey = ""
	}

	// TTL 缓存：30s 内命中直接返回上次 JSON 响应（原始新闻流 + 事件合并结果均被缓存）
	// English: within the 30s TTL serve the cached JSON; otherwise recompute below.
	s.newsMu.Lock()
	if s.newsCache != nil && time.Since(s.newsCacheAt) < newsTTL {
		if body, ok := s.newsCache[cacheKey]; ok {
			s.newsMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
			return
		}
	}
	s.newsMu.Unlock()

	// 1. 原始新闻流：同花顺快讯(主) → 新浪(兜底)，标题截断去重合并
	rawNews := make([]data.NewsItem, 0, 60)
	rawSeen := make(map[string]bool)
	addRaw := func(items []data.NewsItem, err error) {
		if err != nil {
			return
		}
		for _, n := range items {
			if n.Title == "" {
				continue
			}
			key := truncateTitle(n.Title, 60)
			if rawSeen[key] {
				continue
			}
			rawSeen[key] = true
			rawNews = append(rawNews, n)
		}
	}
	addRaw(s.market.GetTonghuashunNews(40))
	addRaw(s.market.GetSinaNews(40))

	// 2. 已打标事件：all=true 读取持久化全量已打标新闻（含中性/一般，跨轮次累计）
	var events []newsagent.NewsEvent
	if c := s.ctrlFor(requestUserID(r)); all && c != nil {
		events = c.GetAllNewsEvents()
	}
	// 再补充看板内存中的本轮事件（去重标题），保证实时事件不遗漏
	seen := make(map[string]bool)
	for _, e := range events {
		seen[e.Title] = true
	}
	if cur := s.dashFor(requestUserID(r)); cur != nil {
		for _, e := range cur.NewsEvents {
			if !seen[e.Title] {
				events = append(events, e)
				seen[e.Title] = true
			}
		}
	}

	// 3. 合并输出：先已打标事件（带标签），再补原始新闻（仅标题/来源/时间）
	// 已打标事件的标题优先于原始流同名标题（原始流中已被 LLM 打标的去重掉）
	tagged := make(map[string]bool)
	out := make([]map[string]interface{}, 0, len(events)+len(rawNews))
	for _, e := range events {
		item := map[string]interface{}{
			"id":           e.Title,
			"title":        e.Title,
			"content":      e.Content,
			"datetime":     normalizeNewsTime(e.Datetime),
			"source":       e.Source,
			"direction":    e.Direction,
			"sentiment":    e.Direction,
			"impact_level": e.ImpactLevel,
			"sectors":      e.Sectors,
			"stocks":       e.CleanedStocks,
			"score":        e.Score,
			"tagged":       true,
		}
		out = append(out, item)
		tagged[truncateTitle(e.Title, 60)] = true
	}
	for _, n := range rawNews {
		if tagged[truncateTitle(n.Title, 60)] {
			continue // 已被 LLM 打标，不重复展示原始版
		}
		out = append(out, map[string]interface{}{
			"id":           n.Title,
			"title":        n.Title,
			"content":      n.Content,
			"datetime":     normalizeNewsTime(n.Datetime),
			"source":       n.Source,
			"direction":    "",
			"sentiment":    "",
			"impact_level": "",
			"sectors":      []string{},
			"stocks":       []string{},
			"score":        0,
			"tagged":       false,
		})
	}

	// 追加宏观日历事件（按天缓存）
	now := time.Now()
	macroEvents := s.macroEvents(now)
	for _, me := range macroEvents {
		// 仅展示近 14 天内的事件（已开始超过 1 天的直接丢弃）
		daysLeft := int(me.Date.Sub(now).Hours() / 24)
		if daysLeft < -1 || daysLeft > 14 {
			continue
		}
		// 影响级别转中文标签：high→高 / medium→中 / 其余→低
		label := "低"
		if me.Impact == "high" {
			label = "高"
		} else if me.Impact == "medium" {
			label = "中"
		}
		// 剩余天数文案：已开始→进行中 / 今天→今日 / 未来→N天后
		leftStr := ""
		switch {
		case daysLeft < 0:
			leftStr = "进行中"
		case daysLeft == 0:
			leftStr = "今日"
		default:
			leftStr = fmt.Sprintf("%d天后", daysLeft)
		}
		out = append(out, map[string]interface{}{
			"title":        me.Title,
			"datetime":     me.Date.Format("2006-01-02"),
			"source":       "宏观日历",
			"direction":    "",
			"impact_level": label,
			"content":      leftStr,
			"sectors":      []string{},
			"stocks":       []string{},
		})
	}
	// 按事件时间倒序（最新在前）：datetime 已统一为 "YYYY-MM-DD HH:MM"（宏观日历为 "YYYY-MM-DD"），
	// 字符串字典序即时间序；空时间排最后。
	// （Sort news by event time descending: datetime is normalized to "YYYY-MM-DD HH:MM" (macro
	// calendar uses "YYYY-MM-DD"), so string order equals time order; empty timestamps go last.）
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := out[i]["datetime"].(string), out[j]["datetime"].(string)
		if di == "" {
			return false
		}
		if dj == "" {
			return true
		}
		return di > dj
	})
	// 写入 TTL 缓存后返回（仅缓存最近一次 all 与默认视图）
	// English: store into the TTL cache then respond.
	if body, err := json.Marshal(out); err == nil {
		s.newsMu.Lock()
		if s.newsCache == nil {
			s.newsCache = make(map[string][]byte)
		}
		s.newsCache[cacheKey] = body
		s.newsCacheAt = time.Now()
		s.newsMu.Unlock()
		writeJSON(w, 200, out)
		return
	}
	writeJSON(w, 200, out)
}

// truncateTitle 将标题按 rune 截断到 maxLen（保留中文字符完整性），用于标题去重归一化 key。
func truncateTitle(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}

// handleFixGetWatchlist 处理 GET /api/watchlist 请求，返回自选股列表及其实时行情。
func (s *Server) handleFixGetWatchlist(w http.ResponseWriter, r *http.Request) {
	list := s.watchlist.List(requestUserID(r))
	out := make([]map[string]interface{}, 0)
	for _, code := range list {
		info := s.quoteDisplay(code)
		name := code
		price := 0.0
		chg := 0.0
		if info != nil {
			name = info.Name
			price = info.Price
			chg = info.ChangePct
		}
		out = append(out, map[string]interface{}{
			"code":       code,
			"name":       name,
			"price":      price,
			"change_pct": chg,
		})
	}
	writeJSON(w, 200, map[string]interface{}{"stocks": out})
}

// watchlistReq 自选股操作的请求结构体。
type watchlistReq struct {
	Code string `json:"code"`
}

// handleFixAddWatchlist 处理 POST /api/watchlist 请求，添加个股到自选股。
// 返回新增股票的行情数据（名称/现价/涨跌幅），前端可直接追加行，无需整表重载。
func (s *Server) handleFixAddWatchlist(w http.ResponseWriter, r *http.Request) {
	var req watchlistReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeError(w, 400, "code required")
		return
	}
	if !s.watchlist.Add(requestUserID(r), code) {
		writeJSON(w, 200, map[string]interface{}{"status": "ok", "duplicate": true})
		return
	}
	// 把新自选股纳入 fetcher 5s 监控池，使其后续进入快照（下一次轮询即补齐行情），
	// 避免本请求真打行情接口（数据源被限流占满时阻塞 → 前端添加失败）。
	// (Add the symbol to the fetcher's 5s monitor pool so its quote arrives on the next poll;
	// this avoids a live upstream call in the add request, which would block while the
	// rate limiter is saturated and make the frontend "add" fail on timeout.)
	if s.fetcher != nil {
		s.fetcher.EnsureStock(code)
	}
	info := s.quoteDisplay(code)
	name := code
	price := 0.0
	chg := 0.0
	if info != nil {
		name = info.Name
		price = info.Price
		chg = info.ChangePct
	}
	// 加自选后同步消息中心该股的名称（旧名/空名刷新为权威名）
	if name != "" {
		if c := s.ctrlFor(requestUserID(r)); c != nil {
			c.RefreshMessageName(code, name)
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"status": "ok",
		"stock":  map[string]interface{}{"code": code, "name": name, "price": price, "change_pct": chg},
	})
}

// handleFixRemoveWatchlist 处理 DELETE /api/watchlist 请求，从自选股中移除个股。
func (s *Server) handleFixRemoveWatchlist(w http.ResponseWriter, r *http.Request) {
	var req watchlistReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	s.watchlist.Remove(requestUserID(r), req.Code)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleFixAction 处理 POST /api/action 请求，接收用户手动操作指令（买入/卖出等）。
// AUTO_TRADING_PLAN M1：qmt.enabled 且 manual 模式时，该端点为前端确认后的实盘下单入口
// （signal_id 幂等 + 熔断前置校验）；未启用实盘时保持兼容的空操作 stub（仅日志）。
// English: POST /api/action — manual operation command. Under AUTO_TRADING_PLAN M1, when qmt.enabled and
// mode=manual this is the frontend-confirmed live order entry (signal_id idempotent + breaker pre-check);
// otherwise it stays a no-op stub (log only) for compatibility.
func (s *Server) handleFixAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string  `json:"code"`
		Action   string  `json:"action"`
		SignalID string  `json:"signal_id"` // 信号 ID（幂等键）
		Price    float64 `json:"price"`     // 参考价
		Qty      int     `json:"qty"`       // 股数
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	user := userFromContext(r)
	ctrl := s.qmtCtrlFor(user.ID)
	if ctrl != nil && ctrl.Enabled() && ctrl.Mode() == "manual" {
		if req.Code == "" {
			writeError(w, 400, "code required")
			return
		}
		side := trading.SideBuy
		if req.Action == "卖出" || req.Action == "sell" {
			side = trading.SideSell
		}
		price := req.Price
		if price <= 0 {
			if q, err := s.quote(req.Code); err == nil && q != nil && q.Price > 0 {
				price = q.Price
			}
		}
		if price <= 0 {
			writeError(w, 400, "price unavailable")
			return
		}
		qty := req.Qty
		if qty <= 0 {
			qty = 100
		}
		signalID := req.SignalID
		if signalID == "" {
			signalID = "manual@" + req.Code + "@" + time.Now().Format("20060102150405")
		}
		res, err := ctrl.PlaceOrder(trading.OrderRequest{
			SignalID: signalID, Code: normalizeTsCode(req.Code), Name: s.stockName(req.Code),
			Side: side, PriceType: ctrl.Config().PriceType, Price: price, Qty: qty,
			Amount: float64(qty) * price, CreatedAt: time.Now().Format(time.RFC3339),
		})
		if err != nil {
			writeError(w, 400, "order rejected: "+err.Error())
			return
		}
		log.Printf("[action] %s %s(%s) qty=%d price=%.2f → %+v", side, req.Code, s.stockName(req.Code), qty, price, res)
		writeJSON(w, 200, res)
		return
	}
	log.Printf("[action] %s %s (qmt disabled, stub)", req.Action, req.Code)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleFixNotifyTest 处理 POST /api/notify-test 请求，通知测试接口。
// 用于前端测试通知功能的连通性。
func (s *Server) handleFixNotifyTest(w http.ResponseWriter, r *http.Request) {
	log.Printf("[notify] 通知测试")
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleFixSSE 处理 GET /api/events 请求，建立 Server-Sent Events (SSE) 连接。
// 用于向前端推送实时事件更新。需要 token 认证。
// 连接建立后：15 秒发送一次心跳保活，有数据时立即推送。
// 账号隔离：按 token 解析 userID，仅订阅该账号定向事件；断线续传：读取 Last-Event-ID 补发漏掉的事件。
func (s *Server) handleFixSSE(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		writeError(w, 401, "missing token")
		return
	}
	user := s.auth.ValidateToken(tokenStr)
	if user == nil {
		writeError(w, 401, "invalid token")
		return
	}
	userID := user.ID

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// 读取 Last-Event-ID 头实现断线续传：>0 时按账号补发历史中该序号之后的事件
	var lastID uint64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		fmt.Sscanf(v, "%d", &lastID)
	}
	ch := s.sse.SubscribeFor(userID, lastID)
	defer s.sse.UnsubscribeFor(userID, ch)

	ctx := r.Context()
	// 发送心跳保活
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			// 先写事件序号（供客户端断线续传），再写数据与空行
			if ev.ID > 0 {
				fmt.Fprintf(w, "id: %d\n", ev.ID)
			}
			fmt.Fprintf(w, "data: %s\n\n", ev.Data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
