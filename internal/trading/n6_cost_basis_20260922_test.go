// n6_cost_basis_20260922_test.go — §N-6（2026-09-22 傍晚批复验）实盘成本基准被对账裸写覆盖的行为锁。
//
// 缺陷形态：持仓对账 upsert 的 SET 列表里 name/strategy/signal_id/highest_price 都有 CASE 保护，
// 唯独 qty / cost_price / amount 是 `= excluded.*` 裸写；而成交回报路径（ApplyRealFill）把
// **含费**成本写进同一列（§F1：(旧含费账+成交额+佣金)/新量）。券商持仓接口给的是**不含费**
// open_price（§BUGFIX_BUDGET_FREEZE_LEDGER 二·费用口径：柜台侧拿不到"含费每股成本"，
// 含费摊薄是本地约定），字段缺失时更是直接归零 → 每 5 分钟一次 MaybeReconcile/Reconcile
// 就把含费成本洗成不含费、甚至清零，且已落库、重启救不回。
// 下游是三条判定线共同的输入：advice.go 的 `if p.CostPrice > 0`（盈亏/止盈/止损）与
// registry.go 的 ATR 移动止盈回退——全链路零日志零告警（本批主题「静默失效」）。
//
// owner 裁决 11：本地含费口径优先——快照只在本地为 0/缺失时兜底回填，绝不覆盖已有非零含费值；
// amount 与 cost_price 同源推导（选定成本 × 快照数量）；守卫丢弃快照值必须留痕（计数）。
//
// English: §N-6 lock — a broker reconcile (fee-less open_price, present or missing) must never
// overwrite the local fee-inclusive cost basis; amount stays consistent with the chosen cost ×
// snapshot qty; the drop is counted; downstream ProfitPct keeps its input.
package trading

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// n6Exec 可编排快照的假网关：Positions 即券商侧回报的持仓（open_price 不含费/可能缺失=0）。
type n6Exec struct {
	snapshot []store.RealPosition
}

func (x *n6Exec) PlaceBuy(OrderRequest) (*OrderResult, error) {
	return &OrderResult{OK: true, OrderID: "GW-N6B"}, nil
}
func (x *n6Exec) PlaceSell(OrderRequest) (*OrderResult, error) {
	return &OrderResult{OK: true, OrderID: "GW-N6S"}, nil
}
func (x *n6Exec) Cancel(string) error { return nil }
func (x *n6Exec) State() (*GatewayState, error) {
	return &GatewayState{Connected: true, Positions: x.snapshot}, nil
}
func (x *n6Exec) Health() (bool, error) { return true, nil }

// n6DB 临时实盘账本（绝不落到仓内 data/）。
func n6DB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestN6ReconcileKeepsFeeInclusiveCost §N-6 主用例：
//   - 建仓含费成本 10.05（100 股 @10 + 佣金 5）；
//   - 快照分别给「不含费正值 10.00」与「字段缺失 0」两种形态各跑一轮对账；
//   - 断言列值仍是含费成本、amount=成本×快照数量、守卫有留痕计数、Advise 的 ProfitPct 仍以含费成本起算。
//
// English: two reconcile rounds (fee-less positive open_price, then missing=0) leave the local
// fee-inclusive basis intact and keep downstream ProfitPct computed from it.
func TestN6ReconcileKeepsFeeInclusiveCost(t *testing.T) {
	db := n6DB(t)
	// 成交回报建仓：100 股 @10 佣金 5 → 每股含费 10.05（ApplyRealFill §F1）
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-N6-1", Code: "600000.SH", Name: "浦发",
		Side: "买入", Price: 10, Qty: 100, Amount: 1000, Fee: 5,
		TradedAt: time.Now().Format("2006-01-02 15:04:05"), SignalID: "buy:600000:N形", UserID: "u_1"}); err != nil {
		t.Fatalf("buy fill: %v", err)
	}
	p0, err := db.RealPositionByCodeForUser("u_1", "600000.SH")
	if err != nil || p0.CostPrice != 10.05 {
		t.Fatalf("建仓含费成本应为 10.05: %+v err=%v", p0, err)
	}

	exec := &n6Exec{snapshot: []store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 200, CostPrice: 10, Amount: 2000}, // 不含费 + 柜台加了 100 股
	}}
	ctrl := NewController(exec, db, "u_1", n6Cfg(), nil)
	if err := ctrl.Reconcile(); err != nil {
		t.Fatalf("reconcile#1: %v", err)
	}
	p, err := db.RealPositionByCodeForUser("u_1", "600000.SH")
	if err != nil {
		t.Fatalf("read after reconcile: %v", err)
	}
	if p.Qty != 200 {
		t.Fatalf("数量必须以柜台为准刷新为 200（本条只保护成本）, got %d", p.Qty)
	}
	if p.CostPrice != 10.05 {
		t.Fatalf("§N-6 裁决11：不含费快照成本 10 不得覆盖本地含费 10.05, got %v", p.CostPrice)
	}
	if math.Abs(p.Amount-2010) > 1e-6 {
		t.Fatalf("§N-6 amount 必须与成本同源（10.05×200=2010，不是快照的 2000）, got %v", p.Amount)
	}
	if db.CostGuardDrops() == 0 {
		t.Fatal("§N-6 守卫丢弃快照值必须留痕计数（静默保护也算静默失效）")
	}
	// 盈亏输入仍在：现价 11 → 对含费成本的盈亏 (11-10.05)/10.05×100 ≈ 9.4527%
	if a := n6FirstAdvice(t, db); a.ProfitPct <= 0 || a.ProfitPct > 9.5 || a.ProfitPct < 9.4 {
		t.Fatalf("§N-6 下游 ProfitPct 应以含费成本起算（期望 ≈9.45%%），got %v", a.ProfitPct)
	}

	// 第二轮：券商字段缺失（open_price 取不到 → 0）。裸写时代会把成本清零并永久落库。
	exec.snapshot = []store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 200, CostPrice: 0, Amount: 0},
	}
	if err := ctrl.Reconcile(); err != nil {
		t.Fatalf("reconcile#2: %v", err)
	}
	// amount 由 SQLite 侧「本地成本 × 快照数量」推导，与 Go 侧浮点乘法的最后一位可能不同，
	// 因此断言必须带容差（用 == 比字面量会造出本用例自己的假红，不是守卫失效）。
	if p, err = db.RealPositionByCodeForUser("u_1", "600000.SH"); err != nil {
		t.Fatalf("read after reconcile#2: %v", err)
	}
	if p.CostPrice != 10.05 || math.Abs(p.Amount-2010) > 1e-6 {
		t.Fatalf("§N-6 快照成本缺失(0)时不得清零本地含费账: cost=%v amount=%v", p.CostPrice, p.Amount)
	}
	if a := n6FirstAdvice(t, db); a.ProfitPct <= 0 {
		t.Fatalf("§N-6 清零形态下盈亏判定线输入丢失（ProfitPct=%v）", a.ProfitPct)
	}
}

// TestN6ReconcileStillBackfillsMissingLocalCost 守卫不得把唯一的纠正通道焊死：
// 本地成本为 0（券商快照先建行的历史持仓）时，快照成本仍应回填。
// English: the guard keeps the back-fill path open when the local basis is missing.
func TestN6ReconcileStillBackfillsMissingLocalCost(t *testing.T) {
	db := n6DB(t)
	if _, err := db.ReconcilePositionsForUser("u_1", []store.RealPosition{
		{TsCode: "301009.SZ", Name: "无本仓", Qty: 300, CostPrice: 0, Amount: 0},
	}); err != nil {
		t.Fatalf("seed costless row: %v", err)
	}
	exec := &n6Exec{snapshot: []store.RealPosition{
		{TsCode: "301009.SZ", Name: "无本仓", Qty: 300, CostPrice: 12.5, Amount: 3750},
	}}
	ctrl := NewController(exec, db, "u_1", n6Cfg(), nil)
	if err := ctrl.Reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	p, err := db.RealPositionByCodeForUser("u_1", "301009.SZ")
	if err != nil || p.CostPrice != 12.5 || p.Amount != 3750 {
		t.Fatalf("§N-6 本地无成本时快照必须回填 12.5/3750: %+v err=%v", p, err)
	}
}

// n6Cfg 对账用的最小实盘配置（Enabled 供 MaybeReconcile 语义，其余走默认）。
func n6Cfg() config.QMTConfig {
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	return cfg
}

// n6FirstAdvice 以账本当前持仓 + 现价 11 跑一次 Advise，返回该持仓的建议（承载 ProfitPct）。
// Agent=nil 走持有/格局路径（与 advice_test.go 同口径），只需成本基准这一个输入。
func n6FirstAdvice(t *testing.T, db *store.DB) PositionAdvice {
	t.Helper()
	positions, err := db.RealPositionsForUser("u_1")
	if err != nil || len(positions) == 0 {
		t.Fatalf("读持仓失败: n=%d err=%v", len(positions), err)
	}
	cfg := n6Cfg()
	cfg.Advice.HoldMinProfitPct = 0
	in := AdviceInput{
		Agent:     nil,
		Positions: positions,
		Quotes:    map[string]*data.StockInfo{"600000": {Code: "600000", Price: 11}},
		Scores:    map[string]combat_agent.StockScores{},
		Cfg:       cfg,
	}
	adv := Advise(in)
	for _, a := range adv {
		if a.TsCode == "600000.SH" {
			return a
		}
	}
	t.Fatalf("Advise 未产出 600000.SH 的建议（成本基准被判 0 时持有卡会消失）: %+v", adv)
	return PositionAdvice{}
}
