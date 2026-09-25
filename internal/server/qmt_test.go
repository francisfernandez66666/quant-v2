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

// TestHandleQMTReportTradeFeeLeg §P2-FEE 20260918：成交回报费用腿透传入本地 fills。
// 此前 handleQMTReport 的 trade 分支不落 fee/stamp_tax → 三方对账费用差腿恒 0。
// 现回报带 fee/stamp_tax 则 ApplyRealFill 落库、ListFillsByDay 读回；缺省字段按 0（旧口径兼容）。
// English: a trade report carrying fee/stamp_tax persists them into local fills; absent fields stay 0 (back-compat).
func TestHandleQMTReportTradeFeeLeg(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// 带费用腿的卖出成交（fee=32.5 佣金 + stamp_tax=65 印花税）
	body := `{"type":"trade","order_id":"OF1","code":"600519.SH","side":"买入","price":10,"qty":100,"amount":1000,` +
		`"traded_at":"2026-09-18T10:00:00+08:00","signal_id":"SF1","trade_id":"TF1","fee":32.5,"stamp_tax":65}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("report HTTP %d: %s", rr.Code, rr.Body.String())
	}

	// 无费用字段的旧格式成交 → fee/stamp_tax 落 0（与历史口径字节兼容）
	body2 := `{"type":"trade","order_id":"OF2","code":"600519.SH","side":"买入","price":10,"qty":100,"amount":1000,` +
		`"traded_at":"2026-09-18T10:01:00+08:00","signal_id":"SF2"}`
	s.handleQMTReport(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body2)))

	// 读回落库成交逐信号核对费用腿：SF1 必须透传 32.5/65，SF2（旧格式）落 0 而非 NULL。
	fills, err := db.ListFillsByDay("", "2026-09-18")
	if err != nil {
		t.Fatalf("list fills: %v", err)
	}
	bySig := map[string]store.RealFill{}
	for _, f := range fills {
		bySig[f.SignalID] = f
	}
	if f := bySig["SF1"]; f.Fee != 32.5 || f.StampTax != 65 {
		t.Fatalf("费用腿未透传：SF1 fee=%.2f stamp=%.2f（应 32.5/65）", f.Fee, f.StampTax)
	}
	if f := bySig["SF2"]; f.Fee != 0 || f.StampTax != 0 {
		t.Fatalf("旧格式成交费用应缺省为 0：SF2 fee=%.2f stamp=%.2f", f.Fee, f.StampTax)
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
	s.cfg.Get().QMT.Token = "gw-secret-123" // 全局 QMT 网关 token（GetRulesFor 对无覆盖账号回落全局）；§0925EVE-D1 字段转私有后经 Get() 取活体
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

// TestHandleQMTReportBrokerEventAccepted §2026-09-11 生产实录：网关通道切换事件
// {"type":"broker"} 旧实现无对应 case → 400 拒收 → outbox 反复重推刷屏。
// broker 事件仅观察用，应 200 接受且不动账本。
func TestHandleQMTReportBrokerEventAccepted(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	body := `{"type":"broker","broker":"queued","from":"xt","at":"2026-09-11T14:48:00+08:00"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("broker 切换事件应 200 接受, got %d: %s", rr.Code, rr.Body.String())
	}
	if pos, _ := db.RealPositionByCode("600000.SH"); pos.TsCode != "" {
		t.Fatal("broker 事件不应动账本")
	}
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

	// 走 /api/qmt/trades 取汇总：成交簿里没有买入记录时，这笔退出属于「无成本基准」，
	// 既不能凭空定价成盈亏，也不该计胜负，但仍要出现在流水里。
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

	// 同样打 /api/qmt/trades，但这里持仓还在账上（剩 60 股），
	// 卖出的 40 股超出成交簿买量的部分要借用账本成本 1500 定价出 -4000 亏损。
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

// TestQMTTradesFeeInclusiveReplay §F1+§F12（2026-09-22 修复批）反例锁：/api/qmt/trades
// 重放必须含费——买入佣金摊入加权成本、卖出 pnl 扣 fee+stamp_tax（与 paper 含费口径一致）；
// 流水回显必须带非零 fee/stamp_tax；金额统计取落库 Amount 而非重算 Price×Qty（§F12 单口径）。
// 构造：买 100@10 fee5（成本 10.05）→ 卖 100@11 fee6.5 印6.5
// → realized=(11−10.05)×100−13=82（旧不含费实现会虚报 100）。
// English: §F1/§F12 regression — trades replay is fee-inclusive (buy commission amortized into
// weighted cost, sell pnl net of fee+stamp), ledger echo carries non-zero fee legs, and amount
// stats use the stored Amount instead of a recomputed Price×Qty.
func TestQMTTradesFeeInclusiveReplay(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	if err := db.ApplyRealFill(store.RealFill{OrderID: "F-F1-B", Code: "600519.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, Fee: 5, TradedAt: "2026-09-22 09:31:00"}); err != nil {
		t.Fatalf("seed buy: %v", err)
	}
	if err := db.ApplyRealFill(store.RealFill{OrderID: "F-F1-S", Code: "600519.SH", Side: "卖出",
		Price: 11, Qty: 100, Amount: 1100, Fee: 6.5, StampTax: 6.5, TradedAt: "2026-09-22 14:00:00"}); err != nil {
		t.Fatalf("seed sell: %v", err)
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
		} `json:"summary"`
		Fills []struct {
			OrderID  string  `json:"order_id"`
			Amount   float64 `json:"amount"`
			Fee      float64 `json:"fee"`
			StampTax float64 `json:"stamp_tax"`
		} `json:"fills"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Summary.RealizedPnl != 82 {
		t.Fatalf("含费重放已实现应=82（(11−10.05)×100−13），旧不含费口径会是 100, got %v", out.Summary.RealizedPnl)
	}
	if out.Summary.Wins != 1 {
		t.Fatalf("含费后仍为盈利应计 1 胜, got %d", out.Summary.Wins)
	}
	if len(out.Fills) != 2 {
		t.Fatalf("流水应 2 笔, got %d", len(out.Fills))
	}
	for _, f := range out.Fills {
		switch f.OrderID {
		case "F-F1-B":
			if f.Fee != 5 || f.StampTax != 0 {
				t.Fatalf("买入流水费用腿回显错误: %+v", f)
			}
		case "F-F1-S":
			if f.Fee != 6.5 || f.StampTax != 6.5 {
				t.Fatalf("卖出流水费用腿回显错误: %+v", f)
			}
		}
	}
}

// TestHandleQMTReportPositionsClearGuard §AUDIT-PM 2026-09-15 空快照纵深守卫：
// 本地有仓 + 空快照 → 409 拒清、持仓保留；非空快照正常对账；本地无仓时空快照放行（合法全平）。
// English: empty-snapshot defense-in-depth — local rows + empty push ⇒ 409 and rows survive;
// non-empty push reconciles normally; empty push on an empty book passes (legit flat).
func TestHandleQMTReportPositionsClearGuard(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "600519.SH", Name: "贵州茅台", Qty: 100, CostPrice: 1280}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// ① 有仓 + 空快照 → 拒收，账本不动
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(`{"type":"positions","positions":[]}`))
	s.handleQMTReport(rr, r)
	if rr.Code != http.StatusConflict {
		t.Fatalf("空快照+有仓应 409, got %d: %s", rr.Code, rr.Body.String())
	}
	if all, _ := db.RealPositions(); len(all) != 1 {
		t.Fatalf("守卫拒清后持仓应保留, got %+v", all)
	}
	// ② 非空快照正常对账（覆盖为本账号快照口径）
	rr = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(`{"type":"positions","positions":[{"ts_code":"000001.SZ","name":"平安银行","qty":200,"cost_price":12}]}`))
	s.handleQMTReport(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("非空快照应 200, got %d: %s", rr.Code, rr.Body.String())
	}
	// ③ 本地已无仓（清到空后）→ 空快照放行
	if _, err := db.ReconcilePositionsForUser("", nil); err != nil {
		t.Fatalf("flatten: %v", err)
	}
	rr = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(`{"type":"positions","positions":[]}`))
	s.handleQMTReport(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("空账本收空快照应放行（合法全平）, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleQMTReportPositionsInvalidTsCode400 §F2（2026-09-22 修复批）HTTP 面：
// positions 上报混入 ts_code=” 或非法格式行 → 整批拒 400，账本一行不动（含批内合法行）。
// 合法快照不受影响；「本地有仓+空快照 409」守卫保留（见 TestHandleQMTReportPositionsClearGuard）。
// English: §F2 — a snapshot containing any invalid ts_code is rejected wholesale with 400
// and nothing lands; valid snapshots keep flowing.
func TestHandleQMTReportPositionsInvalidTsCode400(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "000001.SZ", Name: "平安", Qty: 100, CostPrice: 12}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := `{"type":"positions","positions":[{"ts_code":"600519.SH","name":"贵州茅台","qty":100,"cost_price":1500},{"ts_code":"","name":"垃圾行","qty":100}]}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("含空 ts_code 的快照应整批 400, got %d: %s", rr.Code, rr.Body.String())
	}
	all, _ := db.RealPositions()
	if len(all) != 1 || all[0].TsCode != "000001.SZ" {
		t.Fatalf("拒收后账本应原样不动, got %+v", all)
	}
	// 非法后缀格式同样拒
	body2 := `{"type":"positions","positions":[{"ts_code":"600519","name":"缺后缀","qty":100}]}`
	rr = httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body2)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("缺交易所后缀应 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleQMTReportOrderStoreError500 §M4（2026-09-22 修复批）：委托状态腿落库失败
// 不得吞错回 200 ok——旧实现让网关 outbox 误判投递成功、状态回报永久丢失。
// 现回 500 触发 outbox 重推（成交腿同口径）。
// English: §M4 — order-status persistence failure must answer 500 (retryable by the gateway
// outbox) instead of the old swallowed-error 200 ok.
func TestHandleQMTReportOrderStoreError500(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	body := `{"type":"order","order_id":"O-M4","signal_id":"S-M4","code":"600519.SH","side":"买入","status":"部成","price":1500,"qty":100,"at":"2026-09-22T10:00:00+08:00"}`
	// 关闭底层库模拟 store 落库报错（Begin 即失败）
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	if rr.Code >= 200 && rr.Code < 300 {
		t.Fatalf("落库失败必须非 2xx（让 outbox 重推）, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("落库失败应 500, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleQMTReportOrderMissingKeys §F5（2026-09-22 修复批）：缺键委托回报不再静默丢弃——
// ① 缺 signal_id（手工单形态）：以 ext:<order_id> 占位键落一条可查的最小状态行，同单号后续
//
//	状态回报正常单调推进；② 缺 order_id（主键不可落行）：显式拒收 400 并 log+opslog 留痕
//	（网关 outbox 对 4xx 走死信留痕，不无限重推）。
//
// English: §F5 — reports lacking signal_id land a queryable minimal row keyed by ext:<order_id>;
// reports lacking order_id (the primary key) are explicitly rejected with a durable trace.
func TestHandleQMTReportOrderMissingKeys(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// ① 缺 signal_id：已报 → 落最小行
	body := `{"type":"order","order_id":"MANUAL-77","code":"600000.SH","side":"买入","status":"已报","price":10,"qty":100,"at":"2026-09-22T10:00:00+08:00"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("缺 signal_id 的 order 回报应落最小行并 200, got %d: %s", rr.Code, rr.Body.String())
	}
	orders, _ := db.RealOrders()
	var row *store.RealOrder
	for i := range orders {
		if orders[i].OrderID == "MANUAL-77" {
			row = &orders[i]
		}
	}
	if row == nil {
		t.Fatal("手工单委托行应可查（此前静默丢弃形态）")
	}
	if row.SignalID != "ext:MANUAL-77" || row.Status != "已报" {
		t.Fatalf("最小状态行键值不符: %+v", row)
	}
	// 同单号后续 已成 → 单调推进（占位键幂等）
	body2 := `{"type":"order","order_id":"MANUAL-77","code":"600000.SH","side":"买入","status":"已成","price":10,"qty":100,"at":"2026-09-22T10:05:00+08:00"}`
	rr = httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body2)))
	if rr.Code != http.StatusOK {
		t.Fatalf("推进回报应 200, got %d: %s", rr.Code, rr.Body.String())
	}
	orders, _ = db.RealOrders()
	for _, o := range orders {
		if o.OrderID == "MANUAL-77" && o.Status != "已成" {
			t.Fatalf("状态应推进为 已成, got %s", o.Status)
		}
	}

	// ② 缺 order_id：显式拒收 400，不落库
	body3 := `{"type":"order","signal_id":"S-NOORD","code":"600000.SH","side":"买入","status":"已成","price":10,"qty":100,"at":"2026-09-22T10:06:00+08:00"}`
	rr = httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body3)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("缺 order_id 应显式拒收 400, got %d: %s", rr.Code, rr.Body.String())
	}
	orders, _ = db.RealOrders()
	for _, o := range orders {
		if o.SignalID == "S-NOORD" {
			t.Fatal("拒收回报不得落库")
		}
	}
}

// TestJSONErrorEnvelopeFor404405 §F3（2026-09-22 修复批）：404/405 必须走统一 JSON 错误信封
// （{"error":…}，Content-Type=application/json），不再是 Go ServeMux 默认 text/plain。
// English: §F3 — unmatched routes (404) and wrong methods (405) answer with the standard
// {"error":…} JSON envelope instead of ServeMux's plain-text default.
func TestJSONErrorEnvelopeFor404405(t *testing.T) {
	s, _ := newAdminTestServer(t)

	do := func(method, path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		// 走完整 chain(muxWithJSONErrors)：与生产 Serve 同一接线点
		s.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
		return rr
	}

	// 404：不存在的路径
	rr := do(http.MethodGet, "/api/definitely-not-a-route")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("未注册路径应 404, got %d", rr.Code)
	}
	assertJSONError(t, rr, "404")

	// 405：/api/health 只注册了 GET，POST 应 405
	rr = do(http.MethodPost, "/api/health")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("方法不匹配应 405, got %d", rr.Code)
	}
	assertJSONError(t, rr, "405")
}

// assertJSONError 校验响应为 Content-Type=application/json 且体含 "error" 键的统一信封。
func assertJSONError(t *testing.T, rr *httptest.ResponseRecorder, what string) {
	t.Helper()
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s 响应 Content-Type 应为 application/json, got %q body=%s", what, ct, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s 响应体应为 JSON 信封, 解析失败: %v body=%s", what, err, rr.Body.String())
	}
	if body["error"] == "" {
		t.Fatalf("%s 响应体应含非空 error 字段: %s", what, rr.Body.String())
	}
}
