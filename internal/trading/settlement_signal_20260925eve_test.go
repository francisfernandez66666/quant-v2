// settlement_signal_20260925eve_test.go — §0925EVE-W3-E（C5）交割补记归因两分支钉桩。
//
// 分支①：网关 /settlement 行带真实 signal_id 时，sync_fills 补记成交必须原样沿用
// （战法/信号级盈亏归因从这一刻才真正接通）。
//
// 分支②：行上没有 signal_id（旧网关/柜台回灌）时，补记打「settle-unattributed:」
// 专属前缀，可整体识别为「无归因行」，不许再写旧虚构键 "settle:"+day 冒充普通信号。
//
// English: §0925EVE-W3-E — pins both backfill attribution branches: real signal_id
// passthrough and the distinguishable unattributed marker.
package trading

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestSettleSyncFillsKeepsRealSignalID 分支①：两笔缺失成交，一笔带真实 signal_id、
// 一笔没有——补记后两条 fills 的归因键必须分别为真实键与无归因标记键。
func TestSettleSyncFillsKeepsRealSignalID(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-08", Connected: true,
		Trades: []SettlementTrade{
			// 真实链路成交：网关交割行带下单时的 signal_id（store.py settlement_trades 的 SELECT 列）
			{OrderID: "GW-A", TsCode: "600519.SH", Side: "买入", Price: 100, Qty: 100, Fee: 5,
				Serial: "SER-A", TradedAt: "09:31:00", SignalID: "buy:600519.SH:dragon:20260908"},
			// 无归因行形态（旧版本网关行/手工柜台流水回灌）：signal_id 缺省
			{OrderID: "GW-B", TsCode: "000001.SZ", Side: "卖出", Price: 12, Qty: 200, Fee: 3,
				Serial: "SER-B", TradedAt: "14:00:00"},
		},
	}}}
	ctrl := NewController(src, db, "u_st", cfg, nil)
	if _, err := ctrl.SettleDay("2026-09-08", SettleModeSyncFills); err != nil {
		t.Fatalf("settle: %v", err)
	}
	fills, err := db.ListFillsByDay("u_st", "2026-09-08")
	if err != nil {
		t.Fatalf("fills: %v", err)
	}
	byOrder := map[string]store.RealFill{}
	for _, f := range fills {
		byOrder[f.OrderID] = f
	}
	if len(fills) != 2 {
		t.Fatalf("应补记 2 条, got %d", len(fills))
	}
	// ① 真实归因原样沿用
	if got := byOrder["GW-A"].SignalID; got != "buy:600519.SH:dragon:20260908" {
		t.Fatalf("带真实 signal_id 的交割行补记后必须沿用原键, got %q", got)
	}
	// ② 无归因行单独标记：前缀可识别，且不落回旧虚构键
	sigB := byOrder["GW-B"].SignalID
	if !strings.HasPrefix(sigB, settleUnattributedPrefix) {
		t.Fatalf("无 signal_id 行必须打无归因前缀 %q, got %q", settleUnattributedPrefix, sigB)
	}
	if sigB == "settle:2026-09-08" {
		t.Fatal("旧虚构键 \"settle:\"+day 复活：与真实信号不可分辨即冒充")
	}
	// 前缀键与真实 signal_id 空间不碰撞（真实键从不含该标记）
	if strings.HasPrefix(byOrder["GW-A"].SignalID, settleUnattributedPrefix) {
		t.Fatal("真实归因键不得命中无归因前缀")
	}
}

// TestSettlementTradeDecodesSignalID 结构层反证：网关 HTTP 响应里的 signal_id 必须被
// SettlementTrade 解码接住（旧结构无该 tag，字段在反序列化即丢，后续一切沿用都无从谈起）。
// 走真实 FetchSettlement 解码路径，不做手搓 map 断言（防契约锁测合成样本的假绿形态）。
func TestSettlementTradeDecodesSignalID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"date":"2026-09-08","account":"A1","connected":true,
		  "trades":[{"order_id":"GW-A","ts_code":"600519.SH","side":"买入","price":100,"qty":100,
		             "amount":10000,"fee":5,"stamp_tax":0,"serial":"SER-A","traded_at":"09:31:00",
		             "signal_id":"buy:600519.SH:dragon:20260908"}],
		  "cash":{}}`))
	}))
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tk", 2e9, 0)
	resp, err := c.FetchSettlement("2026-09-08")
	if err != nil {
		t.Fatalf("FetchSettlement: %v", err)
	}
	if len(resp.Trades) != 1 || resp.Trades[0].SignalID != "buy:600519.SH:dragon:20260908" {
		t.Fatalf("SettlementTrade 未解码 signal_id（tag 缺失回归）: %+v", resp.Trades)
	}
}
