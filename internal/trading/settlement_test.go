// settlement_test.go — §WS-B 交割单三方对账的 trading 层测试：
// 用 mock SettlementFetcher 验证三方比对（券商 vs 本地 fills）差异识别、纠偏与告警判定。
// English: §WS-B trading-layer settlement tests — three-way comparison against a mock fetcher,
// diff detection, sync-fills backfill and alert gating.
package trading

import (
	"fmt"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// mockSettle 可控交割单源（实现 SettlementFetcher）。
// English: controllable settlement source implementing SettlementFetcher.
type mockSettle struct {
	resp *SettlementResponse
	err  error
}

func (m *mockSettle) FetchSettlement(date string) (*SettlementResponse, error) {
	return m.resp, m.err
}

// settleExecutor mock 交割单源 + Executor 双接口（使 SettleDay 的 execRef 断言命中）。
// English: mock settlement source that is also an Executor, so SettleDay's execRef() type assertion
// resolves to it.
type settleExecutor struct {
	guardStub
	src *mockSettle
}

func (s *settleExecutor) FetchSettlement(date string) (*SettlementResponse, error) {
	return s.src.FetchSettlement(date)
}

func configDefault() config.QMTConfig {
	return config.DefaultQMTConfig()
}

// TestSettleThreeWay §WS-B：券商有本地无 → MissingInLocal；本地有券商无 → ExtraInLocal；
// 量价一致 → 不报差异；费用差统计正确。
func TestSettleThreeWay(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	// 本地先有一条成交（券商也有的）
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-1", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: "2026-09-08 09:35:00",
		SignalID: "SIG1", UserID: "u_st", Fee: 2.5, Serial: "SER-1"}); err != nil {
		t.Fatalf("local fill: %v", err)
	}
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-08", Connected: true,
		Trades: []SettlementTrade{
			{OrderID: "GW-1", TsCode: "600000.SH", Side: "买入", Price: 10, Qty: 100, Fee: 2.5, Serial: "SER-1", TradedAt: "09:35:00"},
			{OrderID: "GW-2", TsCode: "000001.SZ", Side: "卖出", Price: 12, Qty: 200, Fee: 3, Serial: "SER-2", TradedAt: "10:00:00"},
		},
	}}}
	ctrl := NewController(src, db, "u_st", cfg, nil)
	diff, err := ctrl.SettleDay("2026-09-08", SettleModeReportOnly)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if diff == nil {
		t.Fatalf("diff 不应为 nil")
	}
	if len(diff.MissingInLocal) != 1 {
		t.Fatalf("券商有本地无应 1 条（000001 卖出）, got %d: %v", len(diff.MissingInLocal), diff.MissingInLocal)
	}
	if len(diff.Mismatch) != 0 {
		t.Fatalf("量价一致不应有 Mismatch: %v", diff.Mismatch)
	}
	if len(diff.ExtraInLocal) != 0 {
		t.Fatalf("本地成交都在券商侧不应有 Extra: %v", diff.ExtraInLocal)
	}
	// 券商费用 2.5+3=5.5 vs 本地 2.5 → FeeDiff=3
	if diff.FeeDiff != 3.0 {
		t.Fatalf("费用差应 3.0, got %.2f", diff.FeeDiff)
	}
	// 差异已落库
	diffs, _ := db.ListSettlementDiffs(5)
	if len(diffs) != 1 {
		t.Fatalf("对账差异应落库 1 条, got %d", len(diffs))
	}
}

// TestSettleSyncFills §WS-B：sync_fills 模式把券商有本地无的成交补记，二次对账零差异。
func TestSettleSyncFills(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-08", Connected: true,
		Trades: []SettlementTrade{
			{OrderID: "GW-9", TsCode: "600519.SH", Side: "买入", Price: 100, Qty: 100, Fee: 5, Serial: "SER-9", TradedAt: "09:31:00"},
		},
	}}}
	ctrl := NewController(src, db, "u_st", cfg, nil)
	diff, err := ctrl.SettleDay("2026-09-08", SettleModeSyncFills)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if len(diff.MissingInLocal) != 1 {
		t.Fatalf("初始应 1 条缺失: %v", diff.MissingInLocal)
	}
	// 二次对账：补记后应为 0 差异
	diff2, err := ctrl.SettleDay("2026-09-08", SettleModeSyncFills)
	if err != nil {
		t.Fatalf("settle2: %v", err)
	}
	if len(diff2.MissingInLocal) != 0 || len(diff2.ExtraInLocal) != 0 {
		t.Fatalf("补记后二次对账应 0 差异, got %+v", diff2)
	}
	// 持仓已建
	p, err := db.RealPositionByCodeForUser("u_st", "600519.SH")
	if err != nil || p.Qty != 100 {
		t.Fatalf("补记后持仓应 100, got %+v err=%v", p, err)
	}
}

// TestSettleUnsupported §WS-B：执行器不支持交割单（Noop/桩）→ SettleDay 静默跳过（nil,nil）。
func TestSettleUnsupported(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	ctrl := NewController(guardServer(), db, "u_st", cfg, nil) // guardStub 未实现 SettlementFetcher
	diff, err := ctrl.SettleDay("2026-09-08", SettleModeReportOnly)
	if err != nil || diff != nil {
		t.Fatalf("不支持交割单应 (nil,nil), got diff=%+v err=%v", diff, err)
	}
}

// TestSettleDisconnected §WS-B：网关未连接时交割单不可信 → 跳过。
func TestSettleDisconnected(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{Date: "2026-09-08", Connected: false}}}
	ctrl := NewController(src, db, "u_st", cfg, nil)
	diff, err := ctrl.SettleDay("2026-09-08", SettleModeReportOnly)
	if err != nil || diff != nil {
		t.Fatalf("未连接应 (nil,nil), got diff=%+v err=%v", diff, err)
	}
}

// TestSettleNormalizeSide §WS-B：方向归一化（BUY/b/买 → 买入；未知 → 忽略）。
func TestSettleNormalizeSide(t *testing.T) {
	if normalizeSide("BUY") != "买入" || normalizeSide("卖") != "卖出" {
		t.Fatalf("归一化异常")
	}
	if normalizeSide("unknown") != "" {
		t.Fatalf("未知方向应忽略")
	}
}

// TestSettleFactKeyNoCollapse §P0-1b 回归（2026-09-15）：同代码同方向同量价的多笔成交
// 不得因关联键塌缩而互相覆盖——旧实现 CorrKey 塌缩为 "f:@@买入"，broker map 只剩最后一笔，
// 对账必出假差异。现在按物理事实键多重集合逐笔配对。
func TestSettleFactKeyNoCollapse(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	// 本地两笔同码同向同量价成交（不同委托单）
	for i, oid := range []string{"GW-A1", "GW-A2"} {
		if err := db.ApplyRealFill(store.RealFill{OrderID: oid, Code: "600000.SH", Side: "买入",
			Price: 10, Qty: 100, Amount: 1000, TradedAt: fmt.Sprintf("2026-09-08 09:3%d:00", i+1),
			SignalID: "SIG" + oid, UserID: "u_st"}); err != nil {
			t.Fatalf("local fill %d: %v", i, err)
		}
	}
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-08", Connected: true,
		Trades: []SettlementTrade{
			{TsCode: "600000.SH", Side: "买入", Price: 10, Qty: 100}, // 两笔同键，均无 serial/order_id（最恶劣形态）
			{TsCode: "600000.SH", Side: "买入", Price: 10, Qty: 100},
			{TsCode: "000001.SZ", Side: "卖出", Price: 12, Qty: 200}, // 本地没有 → missing 1
		},
	}}}
	ctrl := NewController(src, db, "u_st", cfg, nil)
	diff, err := ctrl.SettleDay("2026-09-08", SettleModeReportOnly)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if len(diff.MissingInLocal) != 1 {
		t.Fatalf("两笔同键配对后应只剩 1 条缺失(000001), got %d: %v", len(diff.MissingInLocal), diff.MissingInLocal)
	}
	if len(diff.ExtraInLocal) != 0 || len(diff.Mismatch) != 0 {
		t.Fatalf("本地两笔都应配对成功: extra=%v mismatch=%v", diff.ExtraInLocal, diff.Mismatch)
	}
}

// TestSettleSyncFillsNoDoubleCount §P0-1c 回归（2026-09-15）：补记必须携带券商真实
// 委托号+成交时间，使 (order_id,traded_at,price,qty) 判重键与真实回报路径重叠——
// 补记后同一笔成交再从回报通道到达（网关 outbox 重放）时不得二次累加持仓。
func TestSettleSyncFillsNoDoubleCount(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-08", Connected: true,
		Trades: []SettlementTrade{
			{OrderID: "GW-9", TsCode: "600519.SH", Side: "买入", Price: 100, Qty: 100, Fee: 5, Serial: "S-9", TradedAt: "09:31:00"},
		},
	}}}
	ctrl := NewController(src, db, "u_st", cfg, nil)
	if _, err := ctrl.SettleDay("2026-09-08", SettleModeSyncFills); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// 模拟网关 outbox 对同一笔成交的重放（同 order_id + 同时间到达回报通道）
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-9", Code: "600519.SH", Side: "买入",
		Price: 100, Qty: 100, Amount: 10000, TradedAt: "2026-09-08 09:31:00",
		SignalID: "buy:test", UserID: "u_st"}); err != nil {
		t.Fatalf("replayed report fill should be deduped by (order_id,traded_at,price,qty), got err: %v", err)
	}
	p, err := db.RealPositionByCodeForUser("u_st", "600519.SH")
	if err != nil || p.Qty != 100 {
		t.Fatalf("重放回报后持仓必须仍为 100（不双倍累加）, got %+v err=%v", p, err)
	}
}
