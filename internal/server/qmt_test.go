// qmt_test.go — 实盘交易 HTTP 端点测试（AUTO_TRADING_PLAN M1）。
// English: live-trading HTTP endpoint tests (AUTO_TRADING_PLAN M1).
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/store"
)

// reportTestUser 是回报类测试的统一账号。scopedWrite（§L2 2026-11-13）之后，柜台回报
// 必须带 token 解析出的账号才受理——测试里没有任何 token/密钥：这里是把一个内存里的
// auth.User 直接塞进请求 context（与 carrier_shadow_test.go / leg_status_test.go 既有惯例
// 完全一致），生产路径 userIDFor → userFromContext 一字未改，不存在任何可暴露的凭据。
const reportTestUser = "u_test"

// withReportUser 把测试用户注入请求 context，使 handleQMTReport 的 scopedWrite 鉴权
// 走到「有账号」分支。English: injects an in-memory test user into the request context so the
// scoped-write authorization sees a non-empty account; no token or secret is involved.
func withReportUser(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: reportTestUser}))
}

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

// TestHandleRealPositionsFiltersZeroQty §ZERO-QTY-FILTER（2026-11-12 owner 需求）：
// 券商清仓当日仍导出「数量 0 / 成本 0」的持仓行，uplink 原样上报后会在实盘持仓页
// 挂成僵尸行（截图：000520.SZ 凤凰航运 数量 0，页脚却写「共 0 条数据」）。
// 读侧响应层必须把 qty<=0 的行统一滤掉，counter / manual 两条来源都不例外。
func TestHandleRealPositionsFiltersZeroQty(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600519.SH", Name: "贵州茅台", Qty: 100, CostPrice: 1200},
		{TsCode: "000520.SZ", Name: "凤凰航运", Qty: 0, CostPrice: 0},
	}); err != nil {
		t.Fatalf("seed positions: %v", err)
	}
	rr := httptest.NewRecorder()
	s.handleRealPositions(rr, httptest.NewRequest(http.MethodGet, "/api/positions/real", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Positions []store.RealPosition `json:"positions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode positions: %v", err)
	}
	if len(out.Positions) != 1 || out.Positions[0].TsCode != "600519.SH" {
		t.Fatalf("已清仓的 qty=0 行必须被过滤，实际=%+v", out.Positions)
	}
}

// TestCounterReportStrictScopeKeepsOtherAccounts §OWNER-SCOPE-20261112 事故回归：
// 11-12 08:52 admin 的一帧持仓快照把全库 user_id=” 的遗留全局行 claim/DELETE 掉，
// facai 账号的实盘持仓随之清零。柜台回报走 strict 模式后，只准增删 owner 自己
// scoped 的行，其它账号行与遗留全局行一律不许碰。
func TestCounterReportStrictScopeKeepsOtherAccounts(t *testing.T) {
	_, db, _ := newTestResearchServer(t)
	const owner, other = "u_admin", "u_facai"
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, UserID: owner},
		{TsCode: "000001.SZ", Name: "平安", Qty: 200, CostPrice: 50, UserID: other},
		{TsCode: "000520.SZ", Name: "凤凰航运", Qty: 300, CostPrice: 2, UserID: ""},
	}); err != nil {
		t.Fatalf("seed multi-account positions: %v", err)
	}
	// owner 柜台快照：只剩一只新票，其余（含遗留全局桶里的 000520）都不在本帧里。
	if _, err := db.ReconcilePositionsForUser(owner, []store.RealPosition{
		{TsCode: "600519.SH", Name: "贵州茅台", Qty: 50, CostPrice: 1200},
	}, true); err != nil {
		t.Fatalf("strict reconcile: %v", err)
	}
	all, err := db.RealPositions()
	if err != nil {
		t.Fatalf("read all positions: %v", err)
	}
	byCode := map[string]string{}
	for _, p := range all {
		byCode[p.TsCode] = p.UserID
	}
	if uid, ok := byCode["000001.SZ"]; !ok || uid != other {
		t.Fatalf("别的账号持仓被柜台回报误删/改写: %+v", byCode)
	}
	if uid, ok := byCode["000520.SZ"]; !ok || uid != "" {
		t.Fatalf("遗留全局行被柜台回报误删/抢占: %+v", byCode)
	}
	if _, ok := byCode["600000.SH"]; ok {
		t.Fatalf("owner 自己已不在快照中的旧持仓未被清理: %+v", byCode)
	}
	if uid, ok := byCode["600519.SH"]; !ok || uid != owner {
		t.Fatalf("owner 新快照未落到自己名下: %+v", byCode)
	}
}

// TestHandleQMTReportTrade 成交回报 → ApplyRealFill 落库 → handleRealPositions 可读回。
// English: a trade report persists via ApplyRealFill and is readable back through handleRealPositions.
func TestHandleQMTReportTrade(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// 建仓：买 100 股 @ 10.00
	reqBody := `{"type":"trade","order_id":"O1","code":"600519.SH","side":"买入","price":10,"qty":100,"amount":1000,"traded_at":"2026-08-20T10:00:00+08:00","signal_id":"S1"}`
	rr := httptest.NewRecorder()
	r := withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody)))
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
	r = withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody)))
	s.handleQMTReport(rr, r)
	pos, _ = db.RealPositionByCode("600519.SH")
	if pos.Qty != 200 || pos.CostPrice != 11 || pos.HighestPrice != 12 {
		t.Fatalf("unexpected position after add: %+v", pos)
	}

	// 减仓：卖 50 股 @ 13.00 → 剩 150，成本不变
	reqBody = `{"type":"trade","order_id":"O3","code":"600519.SH","side":"卖出","price":13,"qty":50,"amount":650,"traded_at":"2026-08-20T10:02:00+08:00","signal_id":"S3"}`
	rr = httptest.NewRecorder()
	r = withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody)))
	s.handleQMTReport(rr, r)
	pos, _ = db.RealPositionByCode("600519.SH")
	if pos.Qty != 150 || pos.CostPrice != 11 {
		t.Fatalf("unexpected position after sell: %+v", pos)
	}

	// handleRealPositions 读回（同一账号 scope）
	rr = httptest.NewRecorder()
	r = withReportUser(httptest.NewRequest(http.MethodGet, "/api/positions/real", nil))
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
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("report HTTP %d: %s", rr.Code, rr.Body.String())
	}

	// 无费用字段的旧格式成交 → fee/stamp_tax 落 0（与历史口径字节兼容）
	body2 := `{"type":"trade","order_id":"OF2","code":"600519.SH","side":"买入","price":10,"qty":100,"amount":1000,` +
		`"traded_at":"2026-09-18T10:01:00+08:00","signal_id":"SF2"}`
	s.handleQMTReport(httptest.NewRecorder(), withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body2))))

	// 读回落库成交逐信号核对费用腿：SF1 必须透传 32.5/65，SF2（旧格式）落 0 而非 NULL。
	fills, err := db.ListFillsByDay(reportTestUser, "2026-09-18")
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
	// 预置一笔旧持仓（scopedWrite 后柜台回报按账号 reconcile，种子行必须同账号才会被替换）
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "000001.SZ", Name: "平安银行", Qty: 100, CostPrice: 10, UserID: reportTestUser}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 网关推送仅含 600519 → 000001 应被移除
	body := `{"type":"positions","positions":[{"ts_code":"600519.SH","name":"贵州茅台","qty":200,"cost_price":1500,"amount":300000,"highest_price":1500}]}`
	rr := httptest.NewRecorder()
	r := withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
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
		r := withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
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

// TestQMTReportScopedWriteRefusesAnonymous §L2（2026-11-13）T6.4 锁：无账号归属的柜台回报
// 必须 403 + opslog 留痕，绝不落进 user_id=” 遗留全局桶（11-12 跨账号清仓事故的写面封口）。
// English: §L2 T6.4 lock — an unscoped report is refused 403 with an audit trace, never
// landing in the legacy global bucket.
func TestQMTReportScopedWriteRefusesAnonymous(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	body := `{"type":"positions","positions":[{"ts_code":"600519.SH","name":"贵州茅台","qty":100,"cost_price":1500}]}`
	rr := httptest.NewRecorder()
	// 刻意不带任何用户 context（生产上等价于 token 解析失败）
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("无账号归属的回报应 403, got %d: %s", rr.Code, rr.Body.String())
	}
	// 全桶零写入：legacy 全局行不得被创建
	if all, _ := db.RealPositions(); len(all) != 0 {
		t.Fatalf("403 后不得有任何落库（含全局桶）, got %+v", all)
	}
}

// TestQMTReportRemarkAttribution §TRADE-LEDGER（2026-11-13）T7 锁：载体自闭环的成交
// 信封没有 signal_id，委托备注（v24-FC600519）是唯一归因源——Go 反解短码补
// `buy:<码>:<战法>:<日>` 后落 fills 与台账，台账行战法=factor_composite（不再 manual）。
func TestQMTReportRemarkAttribution(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	body := `{"type":"trade","order_id":"O-RM","code":"600519.SH","side":"买入","price":10,"qty":100,` +
		`"amount":1000,"traded_at":"2026-11-13 09:31:05","trade_id":"T-RM","remark":"v24-FC600519"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("report HTTP %d: %s", rr.Code, rr.Body.String())
	}
	pos, err := db.RealPositionByCode("600519.SH")
	if err != nil || pos.Qty != 100 {
		t.Fatalf("成交应落库: %+v err=%v", pos, err)
	}
	if pos.SignalID != "buy:600519.SH:factor_composite:2026-11-13" {
		t.Fatalf("T7: signal_id 应由 remark 反解补全, got %q", pos.SignalID)
	}
	rows, _, err := db.TradeResultsForUser(reportTestUser, "", "", "", 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("台账应有 1 行, n=%d err=%v", len(rows), err)
	}
	if rows[0].Strategy != "factor_composite" {
		t.Fatalf("T7: 台账战法应=factor_composite, got %q", rows[0].Strategy)
	}
	if rows[0].Price != 10 || rows[0].Qty != 100 || rows[0].Status != "open" {
		t.Fatalf("台账行读数不符: %+v", rows[0])
	}
	// 反解纯函数：版本前缀剥离 + 最长前缀（LD1 压过 LD）+ 未知返回空
	if got := carrierRemarkStrategy("v24-LD100519"); got != "leader_decay_v2" {
		t.Fatalf("最长前缀匹配应选 LD1, got %q", got)
	}
	if got := carrierRemarkStrategy("v24-ZZ999999"); got != "" {
		t.Fatalf("未知短码应返回空串, got %q", got)
	}
}

// TestHandleTradeResultsEndpoint §TRADE-LEDGER 端点锁：台账读数按账号过滤、summary 按战法聚合。
func TestHandleTradeResultsEndpoint(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	if err := db.ApplyRealFill(store.RealFill{OrderID: "O-TR", Code: "600519.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, Fee: 5, TradedAt: "2026-11-13 09:31:00",
		SignalID: "buy:600519.SH:龙抬头:2026-11-13", UserID: reportTestUser}); err != nil {
		t.Fatalf("seed fill: %v", err)
	}
	rr := httptest.NewRecorder()
	r := withReportUser(httptest.NewRequest(http.MethodGet, "/api/qmt/trade-results", nil))
	s.handleTradeResults(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Rows []struct {
			Code     string  `json:"code"`
			Strategy string  `json:"strategy"`
			Status   string  `json:"status"`
			Price    float64 `json:"price"`
		} `json:"rows"`
		Summary []struct {
			Strategy string `json:"strategy"`
		} `json:"summary"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 1 || len(out.Rows) != 1 || out.Rows[0].Strategy != "龙抬头" || out.Rows[0].Status != "open" {
		t.Fatalf("台账读数不符: total=%d rows=%+v", out.Total, out.Rows)
	}
	if len(out.Summary) != 1 || out.Summary[0].Strategy != "龙抬头" {
		t.Fatalf("summary 应按战法聚合: %+v", out.Summary)
	}
	// 越权：别的账号看不到
	rr2 := httptest.NewRecorder()
	r2 := r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: "u_other"}))
	s.handleTradeResults(rr2, r2)
	var out2 struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(rr2.Body.Bytes(), &out2)
	if out2.Total != 0 {
		t.Fatalf("跨账号台账必须不可见, got total=%d", out2.Total)
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

	// 种一单本地"已报"（同账号：scopedWrite 后回报按账号定位推进）
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "pend:S-ADV", SignalID: "S-ADV",
		Code: "600519.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100,
		CreatedAt: "2026-08-20T09:31:00+08:00", UserID: reportTestUser}); err != nil {
		t.Fatalf("seed order: %v", err)
	}

	// 回报 部成 → 本地行应推进
	reqBody := `{"type":"order","order_id":"O-ADV","signal_id":"S-ADV","code":"600519.SH","side":"买入","status":"部成","price":10,"qty":100,"at":"2026-08-20T09:32:00+08:00"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody))))
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
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody))))
	assertOrderStatus("已成")

	// 重放 已报 → 绝不回退
	reqBody = `{"type":"order","order_id":"O-ADV","signal_id":"S-ADV","code":"600519.SH","side":"买入","status":"已报","price":10,"qty":100,"at":"2026-08-20T09:34:00+08:00"}`
	rr = httptest.NewRecorder()
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody))))
	assertOrderStatus("已成")
}

// TestHandleQMTReportBrokerEventAccepted §2026-09-11 生产实录：网关通道切换事件
// {"type":"broker"} 旧实现无对应 case → 400 拒收 → outbox 反复重推刷屏。
// broker 事件仅观察用，应 200 接受且不动账本。
func TestHandleQMTReportBrokerEventAccepted(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	body := `{"type":"broker","broker":"queued","from":"xt","at":"2026-09-11T14:48:00+08:00"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))))
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
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(reqBody))))
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
	s.handleQMTReport(rr2, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(tradeBody))))
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
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "600519.SH", Name: "贵州茅台", Qty: 100, CostPrice: 1280, UserID: reportTestUser}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// ① 有仓 + 空快照 → 拒收，账本不动
	rr := httptest.NewRecorder()
	r := withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(`{"type":"positions","positions":[]}`)))
	s.handleQMTReport(rr, r)
	if rr.Code != http.StatusConflict {
		t.Fatalf("空快照+有仓应 409, got %d: %s", rr.Code, rr.Body.String())
	}
	if all, _ := db.RealPositions(); len(all) != 1 {
		t.Fatalf("守卫拒清后持仓应保留, got %+v", all)
	}
	// ② 非空快照正常对账（覆盖为本账号快照口径）
	rr = httptest.NewRecorder()
	r = withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(`{"type":"positions","positions":[{"ts_code":"000001.SZ","name":"平安银行","qty":200,"cost_price":12}]}`)))
	s.handleQMTReport(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("非空快照应 200, got %d: %s", rr.Code, rr.Body.String())
	}
	// ③ 本地已无仓（清到空后）→ 空快照放行（同账号范围内 flatten）
	if _, err := db.ReconcilePositionsForUser(reportTestUser, nil); err != nil {
		t.Fatalf("flatten: %v", err)
	}
	rr = httptest.NewRecorder()
	r = withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(`{"type":"positions","positions":[]}`)))
	s.handleQMTReport(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("空账本收空快照应放行（合法全平）, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestHandleQMTReportPositionsInvalidTsCode400 §F2（2026-09-22 修复批）HTTP 面：
// positions 上报混入 ts_code=<空串> 或非法格式行 → 整批拒 400，账本一行不动（含批内合法行）。
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
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))))
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
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body2))))
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
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))))
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
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body))))
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
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body2))))
	if rr.Code != http.StatusOK {
		t.Fatalf("推进回报应 200, got %d: %s", rr.Code, rr.Body.String())
	}
	orders, _ = db.RealOrders()
	for _, o := range orders {
		if o.OrderID == "MANUAL-77" && o.Status != "已成" {
			t.Fatalf("状态应推进为 已成, got %s", o.Status)
		}
	}

	// ② 缺 order_id：显式拒收 400，不落库（本腿考察缺主键语义，鉴权腿已在①②覆盖）
	body3 := `{"type":"order","signal_id":"S-NOORD","code":"600000.SH","side":"买入","status":"已成","price":10,"qty":100,"at":"2026-09-22T10:06:00+08:00"}`
	rr = httptest.NewRecorder()
	s.handleQMTReport(rr, withReportUser(httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body3))))
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

// TestHandleSignalTradesCriterion §TRADE-LIST-20261112（owner 需求）：交易信号 tab 判据
// = live 通道 + (placed|evaluated) + qty>0。旧判据要求 stage=placed，但实盘通道的
// persistDynBuyDecision 只写 evaluated/skipped（placed 是 paper 专用 stage），导致
// 实盘恒空。这里逐情形验证过滤与 order_state 区分。
func TestHandleSignalTradesCriterion(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	const day = "20261112"
	const uid = "u_test_signal_trades"
	seed := []store.SignalDecision{
		{UserID: uid, Day: day, Channel: "live", Code: "600519.SH", Strategy: "dragon", Stage: "evaluated", Qty: 100, Notional: 120000},
		{UserID: uid, Day: day, Channel: "live", Code: "000001.SZ", Strategy: "factor_composite", Stage: "placed", Qty: 200, Notional: 10000},
		{UserID: uid, Day: day, Channel: "paper", Code: "600000.SH", Strategy: "dragon", Stage: "placed", Qty: 300, Notional: 287}, // 纸面误混入
		{UserID: uid, Day: day, Channel: "live", Code: "000520.SZ", Strategy: "dragon", Stage: "evaluated", Qty: 0, Notional: 0},   // qty=0 僵尸
		{UserID: uid, Day: day, Channel: "live", Code: "300750.SZ", Strategy: "dragon", Stage: "skipped", Qty: 50, Notional: 0},    // 非 placed/evaluated
		{UserID: uid, Day: day, Channel: "live", Code: "688981.SH", Strategy: "factor_composite", Stage: "evaluated", Qty: 150, Notional: 90000},
	}
	for _, d := range seed {
		if err := db.UpsertSignalDecision(d); err != nil {
			t.Fatalf("seed decision %s: %v", d.Code, err)
		}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/signals/trades?day="+day, nil)
	// 注入登录用户上下文（userIDFor 从 context 取 uid，与 seed 的 user_id 对齐）
	req = req.WithContext(context.WithValue(req.Context(), ctxUserKey{}, &auth.User{ID: uid}))
	s.handleSignalTrades(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Day    string `json:"day"`
		Count  int    `json:"count"`
		Trades []struct {
			Code       string `json:"code"`
			Stage      string `json:"stage"`
			OrderState string `json:"order_state"`
			Qty        int    `json:"qty"`
		} `json:"trades"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode trades: %v", err)
	}
	if out.Day != day {
		t.Fatalf("回显 day=%s 期望 %s", out.Day, day)
	}
	// 只应留下 3 条合法：live+evaluated/qty>0（600519.SH、688981.SH）+ live+placed（000001.SZ）
	if out.Count != 3 {
		t.Fatalf("合法建议数=%d 期望 3（live+placed/evaluated+qty>0），实际=%+v", out.Count, out.Trades)
	}
	byCode := map[string]string{}
	for _, tr := range out.Trades {
		byCode[tr.Code] = tr.OrderState
	}
	for _, want := range []string{"600519.SH", "000001.SZ", "688981.SH"} {
		if _, ok := byCode[want]; !ok {
			t.Fatalf("合法建议 %s 被错误过滤，实际=%v", want, byCode)
		}
	}
	for _, exclude := range []string{"600000.SH", "000520.SZ", "300750.SZ"} {
		if _, ok := byCode[exclude]; ok {
			t.Fatalf("非法行 %s 未被过滤，实际=%v", exclude, byCode)
		}
	}
	// order_state 区分：placed=已发单；evaluated=建议未发单
	if byCode["000001.SZ"] != "已发单" {
		t.Fatalf("placed 行的 order_state 应为「已发单」，实得 %q", byCode["000001.SZ"])
	}
	if byCode["600519.SH"] != "建议未发单" {
		t.Fatalf("evaluated 行的 order_state 应为「建议未发单」，实得 %q", byCode["600519.SH"])
	}
}
