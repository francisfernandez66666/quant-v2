package server

// §TRADE-LIST（2026-11-11 owner 需求）：交易卡 = **全套量化规则跑完后的下单信号**。
//
// 全套规则的真源与复用点（owner 13:26「确认清楚之前量化交易的全套规则，把结果复用给
// 量化交易的交易卡」）：
//   脑侧：资讯/新闻归因 → 战法/因子打分（FinalSignals）→ signalctl 准入 → 动态仓位
//         sizing（MS/SS/单笔占比）→ 三道额度帽（行业/单票/簇）→ 关键读数缺失拦截
//         ⇒ **signal_decisions.stage=placed** 才是"整条脑侧链路放行"的最终下单信号；
//   盒侧：QMT 载体 12 闸是下单前最后一道（确认窗/VWAP/集中度/1万闸/熔断…），其
//         拒单原因只在 QMT 日志面板（round_skips），不进本接口。
// 因此本接口只吐 stage=placed 的决策行，并复用 /api/signals 的卡片装配
// （attachSignalCards：八字段 + 缺字段点名一处判定）与 5s 快照现价；交易列表 tab
// 与弹窗提醒都只吃这里——原始信号不再直接推卡（owner："而不是乱推送"）。
// English: only stage=placed decisions (the full brain-side chain passed), with the
// same card assembly and 5s snapshot prices as /api/signals; the trade-list tab and
// the modal pushes consume this endpoint exclusively.

import (
	"net/http"
	"strings"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

func (s *Server) handleSignalTrades(w http.ResponseWriter, r *http.Request) {
	db := s.realDB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "real book not available")
		return
	}
	uid := userIDFor(r)
	day := r.URL.Query().Get("day")
	if len(day) != 8 {
		day = cntime.DayCompactOf(cntime.Now())
	}
	items, err := db.SignalDecisionsForDay(uid, day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list signal decisions: "+err.Error())
		return
	}
	placed := make([]store.SignalDecision, 0, len(items))
	codes := make([]string, 0)
	seen := map[string]bool{}
	for _, it := range items {
		// 2026-11-11 owner 实锤：纸面通道的决策（纸面账资金只有几千块）混进交易列表，
		// 推出 ¥287 的碎单——与实盘动态仓位（10 万成本）完全不挂钩。交易列表/弹窗只认
		// **live（实盘）通道 + stage=placed**；纸面决策留在「动态仓位」tab 里看。
		// English: live-channel placed decisions only; paper stays in the decisions tab.
		if !strings.EqualFold(strings.TrimSpace(it.Stage), "placed") ||
			!strings.EqualFold(strings.TrimSpace(it.Channel), "live") {
			continue
		}
		placed = append(placed, it)
		if !seen[it.Code] {
			seen[it.Code] = true
			codes = append(codes, it.Code)
		}
	}
	// 卡片八字段复用 /api/signals 的同一处装配（attachSignalCards 按登录账号装配，
	// "缺什么"仍是后端一处判定）；现价复用 5s 快照，缺报价不覆盖触发价。
	dash := s.dashFor(uid)
	cardByCode := map[string]interface{}{}
	if dash != nil {
		out := toFixSignals(dash.FinalSignals)
		s.attachSignalCards(uid, out)
		for _, fs := range out {
			if _, ok := cardByCode[fs.Code]; !ok {
				cardByCode[fs.Code] = fs.Card
			}
		}
	}
	quotes, _ := s.displayQuotes(codes)
	trades := make([]map[string]interface{}, 0, len(placed))
	for _, it := range placed {
		// 决策行本身不带价：现价一律取 5s 快照（快照缺=none，绝不拿触发价冒充现价）
		price := 0.0
		priceKind := "none"
		if info, ok := quotes[it.Code]; ok && info != nil && info.Price > 0 {
			price = info.Price
			priceKind = "live"
		}
		row := map[string]interface{}{
			"at": it.At, "channel": it.Channel, "code": it.Code,
			"strategy_key": it.Strategy, "signal_id": it.SignalID,
			"qty": it.Qty, "notional": it.Notional,
			"market_scale": it.MarketScale, "stock_scale": it.StockScale,
			"per_trade_pct": it.PerTradePct, "target": it.Target,
			"cur_strat": it.CurStrat, "room": it.Room,
			"price": price, "price_kind": priceKind,
			"missing_key_input": it.MissingKeyInput, "cash_stale": it.CashStale,
			"reason": it.Reason, "pending_note": it.PendingNote,
			"capped_by": it.CappedBy,
		}
		if card, ok := cardByCode[it.Code]; ok && card != nil {
			row["card"] = card
		}
		trades = append(trades, row)
	}
	writeJSON(w, 200, map[string]interface{}{"day": day, "count": len(trades), "trades": trades})
}
