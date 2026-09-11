// qmt_test.go — 实盘交易 HTTP 端点测试（AUTO_TRADING_PLAN M1）。
// English: live-trading HTTP endpoint tests (AUTO_TRADING_PLAN M1).
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestQMTEndpointsUnavailableWithoutDB §白板修复回归：researchDB 未接入时，
// 实盘族端点必须返回 503（前端 request() 据此走失败分支），绝不允许 200+{"error":…}——
// 那会把错误体当成功数据渲染，Quant 页 trades.summary 为 undefined 直接 TypeError 白屏。
func TestQMTEndpointsUnavailableWithoutDB(t *testing.T) {
	s := &Server{} // researchDB 未接线（模拟主程序 store.Open 失败的降级形态）

	cases := []struct {
		name   string
		method string
		path   string
		h      func(w http.ResponseWriter, r *http.Request)
	}{
		{"trades", http.MethodGet, "/api/qmt/trades", s.handleQMTTrades},
		{"real positions", http.MethodGet, "/api/positions/real", s.handleRealPositions},
		{"real advice", http.MethodGet, "/api/positions/advice", s.handleRealAdvice},
	}
	for _, tc := range cases {
		rr := httptest.NewRecorder()
		tc.h(rr, httptest.NewRequest(tc.method, tc.path, nil))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: researchDB 缺失应返回 503, got %d body=%s", tc.name, rr.Code, rr.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["error"] == "" {
			t.Fatalf("%s: 响应应为 {\"error\":…} 形态: %s", tc.name, rr.Body.String())
		}
	}
}

// TestHandleQMTReportTrade 成交回报 → ApplyRealFill 落库 → handleRealPositions 可读回。
// English: a trade report persists via ApplyRealFill and is readable back through handleRealPositions.
func TestHandleQMTReportTrade(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// 建仓：买 100 股 @ 10.00
	reqBody := `{"type":"trade","order_id":"O1","code":"600519.SH","side":"买入","price":10,"qty":100,"amount":1000,"traded_at":"2026-08-20T10:00:00+08:00","signal_id":"S1"}`
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody))
	s.handleQMTReport(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("report HTTP %d: %s", rr.Code, rr.Body.String())
	}

	pos, err := db.RealPositionByCode("600519.SH")
	if err != nil {
		t.Fatalf("read position: %v", err)
	}
	if pos.TsCode == "" || pos.Qty != 100 || pos.CostPrice != 10 || pos.HighestPrice != 10 {
		t.Fatalf("unexpected position after buy: %+v", pos)
	}

	// 加仓：再买 100 股 @ 12.00 → 加权成本 11，最高价 12
	reqBody = `{"type":"trade","order_id":"O2","code":"600519.SH","side":"买入","price":12,"qty":100,"amount":1200,"traded_at":"2026-08-20T10:01:00+08:00","signal_id":"S2"}`
	rr = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody))
	s.handleQMTReport(rr, r)
	pos, _ = db.RealPositionByCode("600519.SH")
	if pos.Qty != 200 || pos.CostPrice != 11 || pos.HighestPrice != 12 {
		t.Fatalf("unexpected position after add: %+v", pos)
	}

	// 减仓：卖 50 股 @ 13.00 → 剩 150，成本不变
	reqBody = `{"type":"trade","order_id":"O3","code":"600519.SH","side":"卖出","price":13,"qty":50,"amount":650,"traded_at":"2026-08-20T10:02:00+08:00","signal_id":"S3"}`
	rr = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody))
	s.handleQMTReport(rr, r)
	pos, _ = db.RealPositionByCode("600519.SH")
	if pos.Qty != 150 || pos.CostPrice != 11 {
		t.Fatalf("unexpected position after sell: %+v", pos)
	}

	// handleRealPositions 读回
	rr = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/positions/real", nil)
	s.handleRealPositions(rr, r)
	var out struct {
		Positions []store.RealPosition `json:"positions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode positions: %v", err)
	}
	if len(out.Positions) != 1 || out.Positions[0].TsCode != "600519.SH" || out.Positions[0].Qty != 150 {
		t.Fatalf("unexpected /api/positions/real: %+v", out)
	}
}

// TestHandleQMTReportPositions 全量对账：upsert + 移除不在集合内的持仓。
// English: full reconciliation upserts and drops positions absent from the push.
func TestHandleQMTReportPositions(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	// 预置一笔旧持仓
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "000001.SZ", Name: "平安银行", Qty: 100, CostPrice: 10}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 网关推送仅含 600519 → 000001 应被移除
	body := `{"type":"positions","positions":[{"ts_code":"600519.SH","name":"贵州茅台","qty":200,"cost_price":1500,"amount":300000,"highest_price":1500}]}`
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))
	s.handleQMTReport(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("report HTTP %d: %s", rr.Code, rr.Body.String())
	}
	all, err := db.RealPositions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || all[0].TsCode != "600519.SH" {
		t.Fatalf("reconcile result: %+v", all)
	}
}

// TestHandleQMTReportOrder 委托回报落库（幂等 upsert）。
// English: order reports persist via idempotent upsert.
func TestHandleQMTReportOrder(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	body := `{"type":"order","order_id":"ORD1","signal_id":"S1","code":"600519.SH","side":"买入","status":"已报","price":1500,"qty":100,"at":"2026-08-20T10:00:00+08:00"}`
	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))
		s.handleQMTReport(rr, r)
	}
	orders, err := db.RealOrders()
	if err != nil {
		t.Fatalf("list orders: %v", err)
	}
	if len(orders) != 1 || orders[0].OrderID != "ORD1" {
		t.Fatalf("orders: %+v", orders)
	}
}

// TestNormalizeTsCode 代码补后缀。
// English: exchange-suffix completion for bare codes.
func TestNormalizeTsCode(t *testing.T) {
	cases := map[string]string{
		"600000":    "600000.SH",
		"000001":    "000001.SZ",
		"300750":    "300750.SZ",
		"830799":    "830799.BJ",
		"688001":    "688001.SH",
		"600000.SH": "600000.SH",
		"000001.SZ": "000001.SZ",
	}
	for in, want := range cases {
		if got := normalizeTsCode(in); got != want {
			t.Fatalf("normalizeTsCode(%q)=%q, want %q", in, got, want)
		}
	}
}

// TestQMTReportAuthzGatewayTokenOnly §GAP2-W1 回归（P0 收权）：POST /api/qmt/report 只认网关 token。
// 旧实现优先接受任意合法用户 token——叠加 /auth/temp 匿名领号，公网任何人都能伪造成交回报或
// 用空数组 positions 清空 real_positions 全表（资损级数据面）。现断言：
// ①合法用户 token → 401；②空 token → 401；③配置的网关 token → 通过中间件进入业务层
// （本测试环境无 real book，业务层返回 500 "real book not available"，恰好证明已穿过鉴权）。
func TestQMTReportAuthzGatewayTokenOnly(t *testing.T) {
	s, admin := newAdminTestServer(t)
	s.cfg.Rules.QMT.Token = "gw-secret-123" // 全局 QMT 网关 token（GetRulesFor 对无覆盖账号回落全局）
	handler := s.qmtReportMiddleware(s.handleQMTReport)

	mk := func(token string) int {
		body := `{"type":"trade","order_id":"OA","code":"600519.SH","side":"买入","price":10,"qty":100,` +
			`"amount":1000,"traded_at":"2026-08-26T10:00:00+08:00","signal_id":"SA"}`
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		handler(rr, r)
		return rr.Code
	}

	// ① 合法用户 token 也必须被拒（旧实现此处放行 = 漏洞本体）
	if code := mk(admin.Token); code != http.StatusUnauthorized {
		t.Fatalf("用户 token 访问网关回报端点应 401, got %d", code)
	}
	// ② 空 token → 401
	if code := mk(""); code != http.StatusUnauthorized {
		t.Fatalf("缺失 token 应 401, got %d", code)
	}
	// ③ 网关 token 放行：到达业务层（测试库未注入 real book → 500 属预期的"已过鉴权"信号）
	if code := mk("gw-secret-123"); code == http.StatusUnauthorized || code == http.StatusForbidden {
		t.Fatalf("网关 token 应通过鉴权, got %d", code)
	}
}

// TestHandleQMTReportOrderAdvancesStatus §R4-4 接线回归：网关委托状态回报
// （部成/已成）必须推进本地订单行——旧实现 UpsertRealOrder（INSERT OR IGNORE）
// 把状态回报静默吞掉，本地永远停留"已报"。
func TestHandleQMTReportOrderAdvancesStatus(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// 种一单本地"已报"
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "pend:S-ADV", SignalID: "S-ADV",
		Code: "600519.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100,
		CreatedAt: "2026-08-20T09:31:00+08:00", UserID: ""}); err != nil {
		t.Fatalf("seed order: %v", err)
	}

	// 回报 部成 → 本地行应推进
	reqBody := `{"type":"order","order_id":"O-ADV","signal_id":"S-ADV","code":"600519.SH","side":"买入","status":"部成","price":10,"qty":100,"at":"2026-08-20T09:32:00+08:00"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("report HTTP %d: %s", rr.Code, rr.Body.String())
	}
	assertOrderStatus := func(want string) {
		t.Helper()
		orders, _ := db.RealOrders()
		for _, o := range orders {
			if o.SignalID == "S-ADV" {
				if o.Status != want {
					t.Fatalf("订单状态应=%s, got %s", want, o.Status)
				}
				return
			}
		}
		t.Fatal("订单行丢失")
	}
	assertOrderStatus("部成")

	// 回报 已成 → 推进
	reqBody = `{"type":"order","order_id":"O-ADV","signal_id":"S-ADV","code":"600519.SH","side":"买入","status":"已成","price":10,"qty":100,"at":"2026-08-20T09:33:00+08:00"}`
	rr = httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody)))
	assertOrderStatus("已成")

	// 重放 已报 → 绝不回退
	reqBody = `{"type":"order","order_id":"O-ADV","signal_id":"S-ADV","code":"600519.SH","side":"买入","status":"已报","price":10,"qty":100,"at":"2026-08-20T09:34:00+08:00"}`
	rr = httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody)))
	assertOrderStatus("已成")
}

// TestHandleQMTReportOrderUnknownSideNotRejected §2026-09-11 生产实录：
// 桥侧干跑探测单（TEST-DRY…）side="???" 会被旧实现 400 拒收并在网关 outbox
// 反复重推刷屏。order 事件仅展示/推进状态不动账本，未知方向应原样落库 200；
// trade 事件保持强校验（§安全 T3）。
func TestHandleQMTReportOrderUnknownSideNotRejected(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	reqBody := `{"type":"order","order_id":"O-DRY","signal_id":"TEST-DRY-20260911-1","code":"600000.SH","side":"???","status":"已报","price":9.3,"qty":100,"at":"2026-09-11T11:19:00+08:00"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("未知方向 order 回报不应拒收: HTTP %d: %s", rr.Code, rr.Body.String())
	}
	orders, _ := db.RealOrders()
	found := false
	for _, o := range orders {
		if o.SignalID == "TEST-DRY-20260911-1" {
			if o.Side != "???" {
				t.Fatalf("未知方向应原样保留, got %q", o.Side)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("干跑委托行应落库（仅展示）")
	}

	// trade 事件：未知方向仍必须 400 拒收（§安全 T3 防误走卖分支清仓）
	tradeBody := `{"type":"trade","order_id":"O-DRY-T","code":"600000.SH","side":"???","price":9.3,"qty":100,"amount":930,"traded_at":"2026-09-11T11:19:05+08:00","signal_id":"TEST-DRY-20260911-2"}`
	rr2 := httptest.NewRecorder()
	s.handleQMTReport(rr2, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(tradeBody)))
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("未知方向 trade 必须拒收, got HTTP %d", rr2.Code)
	}
}

// TestQMTTradesUnknownBasisSellNotCountedAsWin §2026-09-08 验证②：对账来源持仓（成交簿无买入
// 记录）卖出时，成本基准不可得——旧实现 sellQty 被钳到 0 → pnl=0 → 一律 wins++，把亏损退出
// 伪造成"胜"并吞掉已实现盈亏。视为缺陷：无基准退出不得计入胜/负，也不得伪造 realized。
// English: sells of reconcile-seeded positions (no buy fill in the ledger) must not be reported
// as a 0-PnL win — the exit has no cost basis in the fill replay and must be excluded from
// win/loss and realized PnL rather than fabricated as a win.
func TestQMTTradesUnknownBasisSellNotCountedAsWin(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// 对账来源持仓：成本 1500（UpsertRealPositions 仅对账写入，成交簿无买入）
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600519.SH", Name: "贵州茅台", Qty: 100, CostPrice: 1500, Amount: 150000},
	}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	// 全仓卖出 @1318（真实亏损 -(1500-1318)*100=-18200），成交簿无对应买入
	if err := db.ApplyRealFill(store.RealFill{
		OrderID: "F-SELL-RECON", Code: "600519.SH", Side: "卖出",
		Price: 1318, Qty: 100, Amount: 131800, TradedAt: "2026-09-07 14:00:00",
	}); err != nil {
		t.Fatalf("seed fill: %v", err)
	}

	rr := httptest.NewRecorder()
	s.handleQMTTrades(rr, httptest.NewRequest(http.MethodGet, "/api/qmt/trades", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("trades HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Summary struct {
			RealizedPnl float64 `json:"realized_pnl"`
			Wins        int     `json:"wins"`
			Losses      int     `json:"losses"`
			TradeCount  int     `json:"trade_count"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if out.Summary.Wins != 0 || out.Summary.Losses != 0 {
		t.Fatalf("无成本基准退出应不计胜/负, got wins=%d losses=%d", out.Summary.Wins, out.Summary.Losses)
	}
	if out.Summary.RealizedPnl != 0 {
		t.Fatalf("无成本基准退出无法定价, realized 应为 0, got %v", out.Summary.RealizedPnl)
	}
	if out.Summary.TradeCount != 1 {
		t.Fatalf("流水应计入该笔卖出, trade_count=%d", out.Summary.TradeCount)
	}
}

// TestQMTTradesPartialSellUsesPositionCost §2026-09-08 验证②：对账来源持仓**部分卖出**且持仓仍
// 存在时，超出成交簿买量的部分应借用当前账本成本定价（旧实现 sellQty 钳 0 → pnl=0 白记"胜"）。
// 例：持仓 100@1500（对账来源），卖 40 @1400 → 应计已实现盈亏 (1400-1500)*40 = -4000 且计 1 亏。
// English: partial sells of reconcile-seeded positions (position still on book) must price the share
// portion beyond the ledger via the live-book cost basis instead of clamping PnL to a fabricated 0.
func TestQMTTradesPartialSellUsesPositionCost(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// 对账来源持仓：成本 1500、数量 100（成交簿无买入记录）
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600519.SH", Name: "贵州茅台", Qty: 100, CostPrice: 1500, Amount: 150000},
	}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	// 部分卖出 40 股 @1400（亏损 (1400-1500)*40=-4000），持仓剩 60@1500
	if err := db.ApplyRealFill(store.RealFill{
		OrderID: "F-SELL-PART", Code: "600519.SH", Side: "卖出",
		Price: 1400, Qty: 40, Amount: 56000, TradedAt: "2026-09-07 14:10:00",
	}); err != nil {
		t.Fatalf("seed fill: %v", err)
	}

	rr := httptest.NewRecorder()
	s.handleQMTTrades(rr, httptest.NewRequest(http.MethodGet, "/api/qmt/trades", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("trades HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Summary struct {
			RealizedPnl float64 `json:"realized_pnl"`
			Wins        int     `json:"wins"`
			Losses      int     `json:"losses"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if out.Summary.Losses != 1 || out.Summary.Wins != 0 {
		t.Fatalf("亏损部分卖出应计 1 亏 0 胜, got wins=%d losses=%d", out.Summary.Wins, out.Summary.Losses)
	}
	if out.Summary.RealizedPnl != -4000 {
		t.Fatalf("部分卖出已实现盈亏应=-4000(借用账本成本), got %v", out.Summary.RealizedPnl)
	}
}
