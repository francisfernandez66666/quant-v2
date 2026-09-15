// settlement_drill_test.go — §AUDIT-PM 2026-09-15 sync_fills 实战演练（真实 HTTP 契约层）。
// 区别于 mockSettle 桩：本测试用真 trading.QMTClient ↔ httptest 起的服务端网关四端点
// （/health /state /settlement /cancel，字段形态对齐 qmt-mock §P2-14 与实网关 §P0-1a 修复后
// 契约：trades 带 serial、traded_at 可为纯时间 "09:31:00"），把 P0-1 事故链完整重演一遍：
//
//	① 成交回报丢失（网关只有交割单没有回推）→ 本地无仓无流水；
//	② settle(sync_fills) → 按券商交割单补记 → 持仓/流水恰好恢复一次（双倍入账回归）；
//	③ 再跑一次 settle → 0 差异 0 补记（幂等，outbox 重放同形态不再二次累加）；
//	④ phantom：本地多一笔券商没有的成交 → 只标记 ExtraInLocal，绝不删本地行。
//
// English: end-to-end sync_fills drill over the REAL gateway HTTP contract (QMTClient against an
// httptest server shaped like qmt-mock / patched gateway). Replays the P0-1 incident: lost fill
// report → backfill restores exactly once → second run is a no-op → phantom local fill flagged
// only, never deleted.
package trading

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

// drillGateway 构造交割单形态与实网关/qmt-mock 一致的 httptest 网关（serial 齐、traded_at 纯时间）。
func drillGateway(t *testing.T, trades []map[string]any, cash map[string]float64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "broker_connected": true})
	})
	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "connected": true, "positions": []any{}, "orders": []any{}})
	})
	mux.HandleFunc("/settlement", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "date": r.URL.Query().Get("date"), "account": "DRILL0001",
			"trades": trades, "cash": cash, "connected": true,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSettleSyncFillsDrillRealGateway(t *testing.T) {
	db := testDB(t)
	// 券商侧当日两笔成交（600519 买 100@1280、平安 卖 200@12）；本地一笔都没收到回报（①回报丢失）
	trades := []map[string]any{
		{"order_id": "GW-D1", "ts_code": "600519.SH", "side": "买入", "price": 1280.0, "qty": 100,
			"amount": 128000.0, "fee": 3.2, "stamp_tax": 0, "serial": "SER-D1", "traded_at": "09:31:00"},
		{"order_id": "GW-D2", "ts_code": "000001.SZ", "side": "卖出", "price": 12.0, "qty": 200,
			"amount": 2400.0, "fee": 1.5, "stamp_tax": 1.2, "serial": "SER-D2", "traded_at": "14:00:00"},
	}
	srv := drillGateway(t, trades, map[string]float64{"cash": 870000})
	cfg := configDefault()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cfg.Token = "drill-secret"
	cli := NewQMTClient(srv.URL, "drill-secret", 5*time.Second, 0)
	ctrl := NewController(cli, db, "u_drill", cfg, nil)

	// ② sync_fills 补记：缺两笔 → 补记后流水 2 条；卖出无对应持仓（本地空仓卖单）只入流水不动持仓
	diff, err := ctrl.SettleDay("2026-09-08", SettleModeSyncFills)
	if err != nil {
		t.Fatalf("settle#1: %v", err)
	}
	if len(diff.MissingInLocal) != 2 {
		t.Fatalf("首跑应缺 2 笔, got %d: %v", len(diff.MissingInLocal), diff.MissingInLocal)
	}
	pos, err := db.RealPositionByCodeForUser("u_drill", "600519.SH")
	if err != nil || pos.Qty != 100 {
		t.Fatalf("补记后持仓应恢复 100, got %+v err=%v", pos, err)
	}

	// ③ 幂等重放：再 settle 两次（模拟 outbox 重推/运维手滑），持仓与流水不得二次累加
	for i := 0; i < 2; i++ {
		d2, err := ctrl.SettleDay("2026-09-08", SettleModeSyncFills)
		if err != nil {
			t.Fatalf("settle#%d: %v", i+2, err)
		}
		if len(d2.MissingInLocal) != 0 || len(d2.Mismatch) != 0 {
			t.Fatalf("重放应零补记零不符, got miss=%d mismatch=%d", len(d2.MissingInLocal), len(d2.Mismatch))
		}
	}
	if pos, _ := db.RealPositionByCodeForUser("u_drill", "600519.SH"); pos.Qty != 100 {
		t.Fatalf("重放后持仓仍须恰为 100（双倍入账回归）, got %d", pos.Qty)
	}
	fills, err := db.ListFillsByDay("u_drill", "2026-09-08")
	if err != nil {
		t.Fatalf("fills: %v", err)
	}
	if len(fills) != 2 {
		t.Fatalf("补记流水应恰 2 条, got %d", len(fills))
	}

	// ③b 第二层防线：绕过对账配对，直接重放同键补记（模拟配对逻辑再次失灵的最坏情况），
	// ApplySettlementFill→ApplyRealFill 的 (order_id,traded_at,price,qty) 判重必须兜住，不再累加。
	if err := db.ApplySettlementFill(store.RealFill{OrderID: "GW-D1", Code: "600519.SH", Side: "买入",
		Price: 1280, Qty: 100, Amount: 128000, TradedAt: "2026-09-08 09:31:00",
		SignalID: "settle:2026-09-08", UserID: "u_drill", Fee: 3.2, Serial: "SER-D1"}); err != nil {
		t.Fatalf("重放补记应幂等成功: %v", err)
	}
	if pos, _ := db.RealPositionByCodeForUser("u_drill", "600519.SH"); pos.Qty != 100 {
		t.Fatalf("重放补记不得二次累加持仓, got %d", pos.Qty)
	}
	if f2, _ := db.ListFillsByDay("u_drill", "2026-09-08"); len(f2) != 2 {
		t.Fatalf("重放补记不得重复入流水, got %d", len(f2))
	}
}

func TestSettleSyncFillsDrillPhantomOnlyFlagged(t *testing.T) {
	db := testDB(t)
	// 券商交割单为空；本地有一笔"幽灵"成交（券商从未受理）
	srv := drillGateway(t, []map[string]any{}, map[string]float64{"cash": 100000})
	cfg := configDefault()
	cfg.Enabled = true
	cfg.GatewayURL = srv.URL
	cli := NewQMTClient(srv.URL, "drill-secret", 5*time.Second, 0)
	ctrl := NewController(cli, db, "u_drill", cfg, nil)
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GHOST-1", Code: "600999.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: "2026-09-08 09:40:00",
		SignalID: "SIG-GHOST", UserID: "u_drill"}); err != nil {
		t.Fatalf("seed fill: %v", err)
	}
	diff, err := ctrl.SettleDay("2026-09-08", SettleModeSyncFills)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if len(diff.ExtraInLocal) != 1 {
		t.Fatalf("幽灵成交应标 ExtraInLocal=1, got %d: %v", len(diff.ExtraInLocal), diff.ExtraInLocal)
	}
	// 只标不删：本地流水与持仓必须原样保留（删除动作只允许人工 sync 后复核，自动纠偏红线）
	pos, err := db.RealPositionByCodeForUser("u_drill", "600999.SH")
	if err != nil || pos.Qty != 100 {
		t.Fatalf("phantom 行不得被自动删除, got %+v err=%v", pos, err)
	}
}
