// §P1-11（2026-09-15）POST /api/holdings/balance 回归测试：
// 窄口径改资金端点——只动 real_account.available_cash，绝不触碰持仓表。
// 背景：此前改资金只能整表 POST /api/holdings（full-replace 语义），且服务端显式丢弃
// AvailableBalance 字段，改资金既存不进、并发下还会把手改持仓整体回写覆盖。
// English: regression tests for the narrow available-cash endpoint (P1-11): persists the
// balance, validates negative input, and never mutates the holdings list.
package server

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/store"
)

// TestHoldingsBalanceEndpoint 三条分支：负数 400、正常保存、GET 回读 + 持仓不受影响。
func TestHoldingsBalanceEndpoint(t *testing.T) {
	s, admin := newAdminTestServer(t)
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	s.SetLiveDB(db)
	// handleFixGetHoldings 读持仓报告（s.rpt.ListFor），补接最小报表库避免空指针
	dir := t.TempDir()
	mgr := auth.NewManager(dir)
	_ = mgr.Init()
	cfgMgr := config.NewManager(dir + "/config.json")
	full := New(mgr, display.New(), cfgMgr, report.New(filepath.Join(dir, "report.json")), data.NewMarketAPI(), data.NewWatchlistManager(dir), data.NewTHSClient())
	s.rpt = full.rpt

	// 1) 负数金额 → 400（可用资金不允许为负）
	req := adminReq(s, admin, "POST", "/api/holdings/balance", `{"available_balance":-1}`)
	if rr := adminDo(s, req); rr.Code != 400 {
		t.Fatalf("负数金额期望 400, got %d body=%s", rr.Code, rr.Body.String())
	}

	// 2) 正常保存 → 200 且回显新余额
	req = adminReq(s, admin, "POST", "/api/holdings/balance", `{"available_balance":12345.67}`)
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("正常保存期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	// 3) GET /api/holdings 回读余额（此前硬编码 0），且持仓列表为空（未受写资金影响）
	req = adminReq(s, admin, "GET", "/api/holdings", "")
	rr = adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("GET holdings 期望 200, got %d", rr.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if bal, ok := body["available_balance"].(float64); !ok || bal != 12345.67 {
		t.Fatalf("available_balance 应回读 12345.67, got %v", body["available_balance"])
	}
	if hs, ok := body["holdings"].([]interface{}); !ok || len(hs) != 0 {
		t.Fatalf("持仓列表应为空（未被资金更新触碰）, got %v", body["holdings"])
	}
}
