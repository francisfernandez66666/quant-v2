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
