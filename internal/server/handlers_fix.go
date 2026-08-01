// ── fix 兼容端点 ──
// 本文件提供与前端兼容的 HTTP API 处理函数，
// 将内部数据模型转换为前端期望的格式。

package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
)

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
			D1:           s.Confidence * 100,
			D2:           s.Confidence * 100,
			D3:           s.Confidence * 100,
			D4:           s.Confidence * 100,
			D1Desc:       s.Reason,
			D2Desc:       s.Sector,
			SignalActive: true,
		}
		out = append(out, fs)
	}
	return out
}

// handleFixSignals 处理 GET /api/signals 请求，返回最新策略信号列表。
func (s *Server) handleFixSignals(w http.ResponseWriter, r *http.Request) {
	dash := s.agg.Current()
	if dash == nil {
		writeJSON(w, 200, []fixSignal{})
		return
	}
	writeJSON(w, 200, toFixSignals(dash.FinalSignals))
}

// handleFixStatus 处理 GET /api/status 请求，返回系统运行状态。
// 包含：运行时长、当前交易时段（早盘/午盘/非交易）、信号数量、扫描统计信息。
func (s *Server) handleFixStatus(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.startTime).Round(time.Second).String()
	dash := s.agg.Current()
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

// handleFixAlerts 处理 GET /api/alerts 请求，返回系统告警列表。
// 数据来源：消息中心持久化存储（引擎每轮同步 止盈/止损/策略信号/持仓提示）。
// 未接入引擎时回退到实时看板 + 持仓日志。结果按时间倒序排列。
func (s *Server) handleFixAlerts(w http.ResponseWriter, r *http.Request) {
	if s.ctrl != nil {
		msgs := s.ctrl.GetMessages()
		if msgs == nil {
			msgs = []data.MessageItem{}
		}
		out := make([]map[string]interface{}, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, map[string]interface{}{
				"id":        m.ID,
				"code":      m.Code,
				"name":      m.Name,
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

	dash := s.agg.Current()
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
	for _, l := range s.rpt.List() {
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

// handleClearAlerts 处理 DELETE /api/alerts 请求：清空消息中心全部消息。
func (s *Server) handleClearAlerts(w http.ResponseWriter, r *http.Request) {
	if s.ctrl == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	s.ctrl.ClearMessages()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleDeleteAlert 处理 DELETE /api/alerts/{id} 请求：手工删除单条消息。
func (s *Server) handleDeleteAlert(w http.ResponseWriter, r *http.Request) {
	if s.ctrl == nil {
		writeJSON(w, 200, map[string]string{"status": "no_engine"})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, 400, map[string]string{"error": "missing id"})
		return
	}
	s.ctrl.DeleteMessage(id)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleSectorHotRecords 处理 GET /api/sector/hot/records 请求，返回当日热点板块轮次记录。
func (s *Server) handleSectorHotRecords(w http.ResponseWriter, r *http.Request) {
	if s.ctrl == nil {
		writeJSON(w, 200, []data.HotRecord{})
		return
	}
	recs := s.ctrl.GetHotRecords()
	if recs == nil {
		recs = []data.HotRecord{}
	}
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	writeJSON(w, 200, recs)
}

// fixHolding 前端持仓格式的结构体。
// 包含持仓数量、成本价、现价、盈亏比例、止盈止损价等字段。
type fixHolding struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Quantity      float64 `json:"quantity"`
	CostPrice     float64 `json:"cost_price"`
	CurPrice      float64 `json:"cur_price"`
	ChangePct     float64 `json:"change_pct"`
	PnlPct        float64 `json:"pnl_pct"`
	TakeProfitPct float64 `json:"take_profit_pct"`
	StopLossPct   float64 `json:"stop_loss_pct"`
	SignalActive  bool    `json:"signal_active"`
	NSscore       float64 `json:"n_score"`
	DragonScore   float64 `json:"dragon_score"`
	DbScore       float64 `json:"db_score"`
	DrScore       float64 `json:"dr_score"`
	MScore        float64 `json:"m_score"`
	TakeProfit    float64 `json:"take_profit"`
	StopLoss      float64 `json:"stop_loss"`
}

// handleFixGetHoldings 处理 GET /api/holdings 请求，返回当前持仓列表。
// 从执行日志中筛选状态为"持仓中"的记录，实时拉取最新股价计算盈亏。
// 同时关联信号数据，标注持仓是否有活跃信号。
func (s *Server) handleFixGetHoldings(w http.ResponseWriter, r *http.Request) {
	logs := s.rpt.List()
	holdings := make([]fixHolding, 0)
	for _, l := range logs {
		if l.Status != "持仓中" {
			continue
		}
		cur := l.EntryPrice
		chg := 0.0
		pnl := 0.0
		name := l.Name
		// 实时拉取股价；失败时回退到开仓价（盈亏视为 0）
		if info, err := s.market.GetRealtimeQuote(l.Code); err == nil {
			cur = info.Price
			chg = info.ChangePct
			name = info.Name
		}
		// 盈亏比例 = (现价 - 成本价) / 成本价 * 100
		if cur > 0 && l.EntryPrice > 0 {
			pnl = (cur - l.EntryPrice) / l.EntryPrice * 100
		}
		h := fixHolding{
			Code:          l.Code,
			Name:          name,
			Quantity:      1,
			CostPrice:     l.EntryPrice,
			CurPrice:      cur,
			ChangePct:     chg,
			PnlPct:        pnl,
			TakeProfitPct: l.TakeProfitPct,
			StopLossPct:   l.StopLossPct,
			TakeProfit:    l.EntryPrice * (1 + l.TakeProfitPct/100),
			StopLoss:      l.EntryPrice * (1 - l.StopLossPct/100),
		}
		dash := s.agg.Current()
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
		holdings = append(holdings, h)
	}
	writeJSON(w, 200, map[string]interface{}{
		"holdings":          holdings,
		"available_balance": 0,
	})
}

// fixSetHoldingsReq 手动设置持仓的请求结构体。
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
	_ = req.AvailableBalance
	for _, h := range req.Holdings {
		existing := s.rpt.FindBySignalID(h.Code + "_fix")
		if existing == nil {
			s.rpt.LogSignal(h.Code+"_fix", h.Code, h.Name, "做多", "手动", h.CostPrice, h.TakeProfitPct, h.StopLossPct)
		} else {
			s.rpt.Update(h.Code+"_fix", func(l *report.ExecLog) {
				l.EntryPrice = h.CostPrice
				l.TakeProfitPct = h.TakeProfitPct
				l.StopLossPct = h.StopLossPct
			})
		}
	}
	// 删除不在本次提交中的手动持仓
	for _, l := range s.rpt.List() {
		if strings.HasSuffix(l.SignalID, "_fix") {
			found := false
			for _, h := range req.Holdings {
				if h.Code+"_fix" == l.SignalID {
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

// handleFixSectorHot 处理 GET /api/sector/hot 请求，返回热门板块列表。
// 数据出口：同花顺首屏 top-20 板块表（一级行业+概念），含同花顺涨跌幅/主力净流入。
// 仅展示能匹配到同花顺 top-20 的板块，匹配不到的丢弃。
func (s *Server) handleFixSectorHot(w http.ResponseWriter, r *http.Request) {
	dash := s.agg.Current()
	if dash == nil {
		writeJSON(w, 200, []map[string]interface{}{})
		return
	}
	// 同花顺板块行情表（首屏 top-20，按涨跌幅排序），按名称精确匹配
	sectorMap := map[string]data.SectorInfo{}
	if s.ths != nil {
		if list, err := s.ths.GetTopBoards(); err == nil {
			for _, si := range list {
				sectorMap[si.Name] = si
			}
		} else {
			log.Printf("[server] 同花顺 top板块获取失败: %v", err)
		}
	}
	out := make([]map[string]interface{}, 0)
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
			"score":         sec.Score,
			"change_pct":    si.ChangePct,
			"d1":            0,
			"reason":        sec.Reason,
			"reason_detail": sec.Reason,
			"direction":     sec.Direction,
			"limitup_cnt":   si.LimitupCnt,
			"net_inflow":    si.NetInflow,
			"news_titles":   newsTitles,
		})
	}
	writeJSON(w, 200, out)
}

// handleFixSnapshot 处理 GET /api/snapshot 请求，返回指定个股或全部自选股的实时快照数据。
// 支持 ?codes=600519,000001 参数指定代码列表，不传则返回自选股列表中的所有个股。
func (s *Server) handleFixSnapshot(w http.ResponseWriter, r *http.Request) {
	codes := r.URL.Query().Get("codes")
	var stockList []string
	if codes != "" {
		stockList = strings.Split(codes, ",")
	} else {
		stockList = s.watchlist.List()
	}
	out := make([]map[string]interface{}, 0)
	for _, code := range stockList {
		info, err := s.market.GetRealtimeQuote(code)
		if err != nil {
			continue
		}
		chg := info.ChangePct
		out = append(out, map[string]interface{}{
			"code":       info.Code,
			"name":       info.Name,
			"price":      info.Price,
			"change_pct": chg,
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
	dash := s.agg.Current()
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
		info, err := s.market.GetRealtimeQuote(sig.Code)
		price := sig.Price
		chg := 0.0
		if err == nil {
			price = info.Price
			chg = info.ChangePct
		}
		out = append(out, map[string]interface{}{
			"code":          sig.Code,
			"name":          sig.Name,
			"price":         price,
			"change_pct":    chg,
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
	dash := s.agg.Current()
	codes := s.watchlist.List()
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
		info, _ := s.market.GetRealtimeQuote(code)
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
			"price":         price,
			"change_pct":    chg,
			"n_score":       nScore,
			"n_pass":        nScore >= 60,
			"dragon_score":  dragonScore,
			"dragon_pass":   dragonScore >= 70,
			"db_score":      dbScore,
			"db_pass":       dbScore >= 70,
			"dr_score":      drScore,
			"dr_pass":       drScore >= 60,
			"m_score":       mScore,
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
	info, err := s.market.GetRealtimeQuote(code)
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

// handleFixNews 处理 GET /api/news 请求，返回新闻事件列表（含宏观日历事件）。
// 数据来源：引擎持久化的新闻事件（聚合器展示缓存）+ 自动生成的宏观日历事件（影响级别高/中/低）。
// 宏观日历仅显示近 14 天内的事件，标注剩余天数。
func (s *Server) handleFixNews(w http.ResponseWriter, r *http.Request) {
	var events []newsagent.NewsEvent
	if cur := s.agg.Current(); cur != nil {
		events = cur.NewsEvents
	}
	out := make([]map[string]interface{}, 0)
	for _, e := range events {
		item := map[string]interface{}{
			"id":           e.Title,
			"title":        e.Title,
			"content":      e.Content,
			"datetime":     e.Datetime,
			"source":       e.Source,
			"direction":    e.Direction,
			"sentiment":    e.Direction,
			"impact_level": e.ImpactLevel,
			"sectors":      e.Sectors,
			"stocks":       e.CleanedStocks,
			"score":        e.Score,
		}
		out = append(out, item)
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
	writeJSON(w, 200, out)
}

// handleFixGetWatchlist 处理 GET /api/watchlist 请求，返回自选股列表及其实时行情。
func (s *Server) handleFixGetWatchlist(w http.ResponseWriter, r *http.Request) {
	list := s.watchlist.List()
	out := make([]map[string]interface{}, 0)
	for _, code := range list {
		info, err := s.market.GetRealtimeQuote(code)
		name := code
		price := 0.0
		chg := 0.0
		if err == nil {
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
	if !s.watchlist.Add(code) {
		writeJSON(w, 200, map[string]interface{}{"status": "ok", "duplicate": true})
		return
	}
	info, _ := s.market.GetRealtimeQuote(code)
	name := code
	price := 0.0
	chg := 0.0
	if info != nil {
		name = info.Name
		price = info.Price
		chg = info.ChangePct
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
	s.watchlist.Remove(req.Code)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleFixAction 处理 POST /api/action 请求，接收用户手动操作指令（买入/卖出等）。
// 目前仅记录日志，暂未实现实际交易逻辑。
func (s *Server) handleFixAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code   string `json:"code"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	log.Printf("[action] %s %s", req.Action, req.Code)
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
	_ = user

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.sse.Subscribe()
	defer s.sse.Unsubscribe(ch)

	ctx := r.Context()
	// 发送心跳保活
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
