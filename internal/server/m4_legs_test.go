// m4_legs_test.go — §M4（2026-09-22 PM 批）回报丢腿回归：name / trade_id / created_at 三条腿。
//
// 缺陷本体（三条腿各自独立，共用同一个根因：Go 信封没有对应 json tag，
// encoding/json 静默丢弃，任何一侧的测试都不会红）：
//
//	① trade.name 丢 → ApplyRealFill 建仓拿不到名称（R8 回填路径一直在等它），
//	   再叠加持仓对账 `name=excluded.name` 无 COALESCE 保护 → 券商空名快照把已有名称抹空；
//	② trade.trade_id 丢 → fills 判重只剩复合键 (order_id,traded_at,price,qty)，
//	   同委托同秒同价同量的**两笔真实部成**第二笔被当成重放（旧库直接撞唯一索引回 500，
//	   网关 outbox 无限重推）；
//	③ order.created_at 丢 → 委托行 created_at 记的是回报时刻，不是委托创建时刻。
//
// English: §M4 regression — three report legs the Go envelope never declared, so
// encoding/json dropped them silently.
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestReportTradeNameLegPersists ① 成交名称入建仓 + 空名对账快照不抹空。
func TestReportTradeNameLegPersists(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	body := `{"type":"trade","order_id":"O-M4A","trade_id":"T-M4A","name":"贵州茅台","code":"600519.SH",` +
		`"side":"买入","price":10,"qty":100,"amount":1000,"traded_at":"2026-09-22T10:00:00+08:00","signal_id":"M4A"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("report HTTP %d: %s", rr.Code, rr.Body.String())
	}
	pos, err := db.RealPositionByCode("600519.SH")
	if err != nil {
		t.Fatalf("read position: %v", err)
	}
	if pos.Name != "贵州茅台" {
		t.Fatalf("① 成交回报的 name 未回填建仓行：got %q", pos.Name)
	}

	// 同一持仓随后到达的券商对账快照不带名称 → 旧名必须保留（不得被空串洗掉）。
	if _, err := db.ReconcilePositionsForUser("", []store.RealPosition{
		{TsCode: "600519.SH", Name: "", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 10},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	pos, _ = db.RealPositionByCode("600519.SH")
	if pos.Name != "贵州茅台" {
		t.Fatalf("① 空名对账快照把持仓名称抹空了：got %q", pos.Name)
	}
	// 快照带非空名称时仍以快照为准（券商改名要能生效）。
	if _, err := db.ReconcilePositionsForUser("", []store.RealPosition{
		{TsCode: "600519.SH", Name: "贵州茅台A", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 10},
	}); err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	pos, _ = db.RealPositionByCode("600519.SH")
	if pos.Name != "贵州茅台A" {
		t.Fatalf("① 非空快照名称应覆盖本地值：got %q", pos.Name)
	}
}

// TestReportTradeIDAnchorsPartialFills ② trade_id 判重锚：
// 同号重放只入账一次；同委托同秒同价同量的两笔不同成交编号都要入账。
func TestReportTradeIDAnchorsPartialFills(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	post := func(tradeID string) int {
		body := `{"type":"trade","order_id":"O-M4B","trade_id":"` + tradeID + `","name":"平安银行","code":"000001.SZ",` +
			`"side":"买入","price":10,"qty":100,"amount":1000,"traded_at":"2026-09-22T10:05:00+08:00","signal_id":"M4B"}`
		rr := httptest.NewRecorder()
		s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
		return rr.Code
	}
	if c := post("T-M4B-1"); c != http.StatusOK {
		t.Fatalf("首笔成交应 200，got %d", c)
	}
	if c := post("T-M4B-1"); c != http.StatusOK {
		t.Fatalf("同 trade_id 重放应幂等吞掉并回 200，got %d", c)
	}
	// 旧实现在这里会撞复合唯一索引回 500（第二笔真实部成永远进不了账本）。
	if c := post("T-M4B-2"); c != http.StatusOK {
		t.Fatalf("② 第二笔真实部成（同秒同价同量、不同成交编号）被误判重放：HTTP %d", c)
	}

	pos, err := db.RealPositionByCodeForUser("", "000001.SZ")
	if err != nil {
		t.Fatalf("read position: %v", err)
	}
	if pos.Qty != 200 {
		t.Fatalf("② 持仓量应为两笔部成合计 200（重放不得累加）：got %d", pos.Qty)
	}
	fills, err := db.RealFills()
	if err != nil {
		t.Fatalf("list fills: %v", err)
	}
	seen := map[string]bool{}
	n := 0
	for _, f := range fills {
		if f.SignalID != "M4B" {
			continue
		}
		n++
		seen[f.TradeID] = true
	}
	if n != 2 || !seen["T-M4B-1"] || !seen["T-M4B-2"] {
		t.Fatalf("② fills 应有两条带成交编号的行：n=%d ids=%v", n, seen)
	}
}

// TestReportOrderCreatedAtLeg ③ 委托行的 created_at 取网关的委托创建时间，缺省才退回回报时刻。
func TestReportOrderCreatedAtLeg(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	body := `{"type":"order","order_id":"O-M4C","signal_id":"M4C","code":"600519.SH","side":"买入",` +
		`"status":"已报","price":10,"qty":100,"created_at":"2026-09-22T09:31:02+08:00","at":"2026-09-22T09:31:09+08:00"}`
	rr := httptest.NewRecorder()
	s.handleQMTReport(rr, httptest.NewRequest(http.MethodPost, "/api/qmt/report", bytes.NewBufferString(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("order report HTTP %d: %s", rr.Code, rr.Body.String())
	}
	orders, err := db.RealOrders()
	if err != nil {
		t.Fatalf("read orders: %v", err)
	}
	var got string
	for _, o := range orders {
		if o.SignalID == "M4C" {
			got = o.CreatedAt
		}
	}
	if got != "2026-09-22T09:31:02+08:00" {
		t.Fatalf("③ 委托 created_at 应为网关的委托创建时间，got %q", got)
	}
}

// TestReportEnvelopeDecodesAllEmittedLegs 契约面的行为兜底：网关发出的每条字段都能解码进信封
// （静态契约测只比对 golden 文档，这条真跑一遍 json.Decode，防 tag 拼错/类型不匹配）。
func TestReportEnvelopeDecodesAllEmittedLegs(t *testing.T) {
	raw := `{"type":"trade","order_id":"O1","trade_id":"T1","name":"名称","code":"600519.SH","side":"买入",` +
		`"status":"已成","price":1,"qty":2,"amount":3,"traded_at":"2026-09-22T10:00:00+08:00","signal_id":"S1",` +
		`"reason":"r","fee":4,"stamp_tax":5,"asset":{"cash":6},"at":"2026-09-22T10:00:01+08:00",` +
		`"created_at":"2026-09-22T10:00:00+08:00","user_id":"u1","broker":"queued","from":"xt"}`
	var ev qmtReportEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	checks := map[string]string{
		"trade_id":   ev.TradeID,
		"name":       ev.Name,
		"created_at": ev.CreatedAt,
		"reason":     ev.Reason,
		"broker":     ev.Broker,
		"from":       ev.From,
	}
	for k, v := range checks {
		if v == "" {
			t.Errorf("§M4 字段 %s 解码后为空——tag 漂移或未声明", k)
		}
	}
	if ev.Fee != 4 || ev.StampTax != 5 || ev.Asset["cash"] != 6 {
		t.Errorf("§M4 数值/资产腿解码不符：%+v", ev)
	}
}
