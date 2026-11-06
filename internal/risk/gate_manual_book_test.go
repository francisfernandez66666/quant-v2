// gate_manual_book_test.go — §MANUAL-BOOK-UNIFY（2026-11-06，owner 裁决 1）风控闸吃手动账的行为锁。
//
// 这批把上午那条"手动读数绝不进任何必须新鲜硬闸"的边界**改判**成"柜台连不上就以手工为准"。
// 改判不等于放开——所以四条锁里有两条是"必须仍然不成立"的：
//
//	① 柜台新鲜时，手动账哪怕写着 99 万也不许参与（裁决 1 前半句"以柜台为准"，方向是放钱，最容易被改坏）；
//	② 陈旧柜台 + 手动 asof 停在昨天 ⇒ 两本账都不作数，闸保持改前的保守分支；
//	③ 陈旧柜台 + 手动当日账 ⇒ 可用资金帽与集中度分母真的换成手动数，且拒单文案点名"手动实盘口径"；
//	④ T+1 可卖量：持仓基线换手动量，并额外扣掉当日**改账**买入（桥断时人工买的票不进 orders，
//	   这一项是唯一有据可查的当日增量；漏了它会把昨天全量说成今天可卖）。
package risk

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

func riskToday() string { return cntime.DayOf(cntime.Now()) }

func riskYesterday() string {
	return cntime.DayOf(cntime.Now().AddDate(0, 0, -1))
}

// seedStaleCounter 桥断形态：柜台资金停在昨天、柜台持仓快照也停在昨天。
func seedStaleCounter(t *testing.T, db *store.DB, uid, code string, qty int, cash float64) {
	t.Helper()
	if err := db.UpsertRealAccount(store.RealAccount{UserID: uid, AvailableCash: cash,
		UpdatedAt: riskYesterday() + " 14:55:00"}); err != nil {
		t.Fatalf("seed stale cash: %v", err)
	}
	if _, err := db.ReconcilePositionsForUser(uid, []store.RealPosition{
		{TsCode: code, Name: "测试", Qty: qty, CostPrice: 10, UpdatedAt: riskYesterday() + " 14:55:00"},
	}); err != nil {
		t.Fatalf("seed stale positions: %v", err)
	}
}

// TestGateManualBookCounterWinsWhenFresh 锁①：柜台新鲜时手动账一个字都不参与。
// 破坏方向（把 gateBook 里的 CashReportFresh 判据摘掉）会立刻红在这一条上。
func TestGateManualBookCounterWinsWhenFresh(t *testing.T) {
	db := gateDB(t)
	uid := "u_mb_fresh"
	g := NewGate(db, uid, nil)
	cfg := qmtCfg()
	// 柜台：资金刚刚回报、只有 1000 元。
	if err := db.UpsertRealAccount(store.RealAccount{UserID: uid, AvailableCash: 1000,
		UpdatedAt: cntime.Now().Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("seed fresh cash: %v", err)
	}
	// 手动账：写着 99 万，asof 也是今天——按改判后的规则它仍然必须被柜台压住。
	if err := db.PutManualAccount(uid, 990000, true, 990000, riskToday()+" 09:35:00"); err != nil {
		t.Fatalf("seed manual cash: %v", err)
	}
	o := liveOrder(SideBuy)
	o.Amount = 5000
	v := g.CheckLiveOrder(cfg, o)
	if v.Pass {
		t.Fatalf("柜台新鲜时 5000 买单必须按柜台 1000 元拒，手动 99 万不许参与, got %+v", v)
	}
	if strings.Contains(v.Reason, "手动实盘") {
		t.Fatalf("拒单文案不该出现手动口径（本轮用的是柜台）：%q", v.Reason)
	}
}

// TestGateManualCashCap 锁③：陈旧柜台 + 手动当日账 ⇒ 可用资金帽真的按手动数判，且文案点名来源。
func TestGateManualCashCap(t *testing.T) {
	db := gateDB(t)
	uid := "u_mb_cash"
	g := NewGate(db, uid, nil)
	cfg := qmtCfg()
	// 关掉日预算闸，让这一条测的是**可用资金**那一闸而不是预算闸（预算闸默认 10 万会先拦下 30 万）。
	cfg.DailyBudgetAmount = 0
	seedStaleCounter(t, db, uid, "600000.SH", 4000, 5000)
	if err := db.PutManualAccount(uid, 100000, true, 0, riskToday()+" 09:35:00"); err != nil {
		t.Fatalf("seed manual cash: %v", err)
	}
	// 改前形态：柜台 5000 元且陈旧 ⇒ 保守折算 2500，6000 的买单必被拒。
	// 改判后：手动当日账 100000 ⇒ 6000 放行。这一条就是裁决 1 的正面效果。
	o := liveOrder(SideBuy)
	o.Amount = 6000
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("手动当日账 10 万应放行 6000 买单, got %+v", v)
	}
	// 超出手动账上限时，拒单文案必须点名"手动实盘口径"与 asof——
	// 让人一眼看出这笔是被**自己敲的那个余额**拦下的，不是被券商余额拦下的。
	o2 := liveOrder(SideBuy)
	o2.Amount = 300000
	v2 := g.CheckLiveOrder(cfg, o2)
	if v2.Pass {
		t.Fatalf("30 万买单必须被手动账 10 万拦下, got %+v", v2)
	}
	if !strings.Contains(v2.Reason, "手动实盘口径") {
		t.Fatalf("拒单文案必须点名手动实盘口径, got %q", v2.Reason)
	}
}

// TestGateManualStaleAsofKeepsLegacyBranch 锁②：手动 asof 停在昨天 ⇒ 闸回到改前的保守分支。
func TestGateManualStaleAsofKeepsLegacyBranch(t *testing.T) {
	db := gateDB(t)
	uid := "u_mb_stale"
	g := NewGate(db, uid, nil)
	cfg := qmtCfg()
	seedStaleCounter(t, db, uid, "600000.SH", 4000, 5000)
	if err := db.PutManualAccount(uid, 100000, true, 0, riskYesterday()+" 09:35:00"); err != nil {
		t.Fatalf("seed stale manual cash: %v", err)
	}
	o := liveOrder(SideBuy)
	o.Amount = 6000
	v := g.CheckLiveOrder(cfg, o)
	if v.Pass {
		t.Fatalf("asof 停在昨天的手动账不许解开可用资金帽, got %+v", v)
	}
	if strings.Contains(v.Reason, "手动实盘") {
		t.Fatalf("本轮不该用手动账，文案却点名了手动口径：%q", v.Reason)
	}
}

// TestGateT1SellableManualBaseline 锁④：T+1 可卖量在柜台快照陈旧时吃手动持仓，并扣当日改账买入。
//
// 场景：柜台账停在昨天 4000 股（桥断冻结）；owner 今天改账记了 1000 股，又用「加仓」改账买了 300 股
// ⇒ 手动账现在 1300 股，其中 300 股是今天买的（T+1 不可卖）⇒ 可卖 1000。
// 卖 1000 放行、卖 1100 拒，且拒单文案点名"手动实盘"。
func TestGateT1SellableManualBaseline(t *testing.T) {
	db := gateDB(t)
	uid := "u_mb_t1"
	g := NewGate(db, uid, nil)
	cfg := qmtCfg()
	seedStaleCounter(t, db, uid, "600000.SH", 4000, 5000)
	today := riskToday()
	if _, _, err := db.PutManualPositions(uid, []store.ManualPosition{
		{TsCode: "600000.SH", Name: "测试", Qty: 1000, CostPrice: 10},
	}, today+" 09:35:00"); err != nil {
		t.Fatalf("seed manual positions: %v", err)
	}
	if err := db.PutManualAccount(uid, 100000, true, 0, today+" 09:35:00"); err != nil {
		t.Fatalf("seed manual cash: %v", err)
	}
	if _, err := db.AdjustManualBook(uid, store.ManualAdjustReq{
		Op: store.ManualOpAdd, TsCode: "600000.SH", Qty: 300, Price: 10, AsOf: today + " 10:00:00",
	}, today); err != nil {
		t.Fatalf("改账加仓 300: %v", err)
	}
	if n, err := db.ManualTodayBoughtQty(uid, "600000.SH", today); err != nil || n != 300 {
		t.Fatalf("当日改账买入应读到 300，got %d err=%v", n, err)
	}

	ok := liveOrder(SideSell)
	ok.Code, ok.Qty, ok.Amount = "600000.SH", 1000, 10000
	if v := g.CheckLiveOrder(cfg, ok); !v.Pass {
		t.Fatalf("手动基线 1300 − 当日改账买入 300 = 可卖 1000，卖 1000 应放行, got %+v", v)
	}
	bad := liveOrder(SideSell)
	bad.Code, bad.Qty, bad.Amount = "600000.SH", 1100, 11000
	v := g.CheckLiveOrder(cfg, bad)
	if v.Pass {
		t.Fatalf("卖 1100 应被拦（当日改账买入 300 未解锁）, got %+v", v)
	}
	if !strings.Contains(v.Reason, "手动实盘") {
		t.Fatalf("拒单文案必须点名持仓基线来自手动实盘, got %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "改账买入 300") {
		t.Fatalf("拒单文案必须把当日改账买入单独列出来（它不在 orders 表里）, got %q", v.Reason)
	}
}

// TestGateT1SellableCounterBaselineUnchanged 柜台新鲜时 T+1 判定逐字改前：
// 基线取柜台量、不扣改账买入（那时改账本来就不该发生），文案不出现手动口径。
func TestGateT1SellableCounterBaselineUnchanged(t *testing.T) {
	db := gateDB(t)
	uid := "u_mb_t1_fresh"
	g := NewGate(db, uid, nil)
	cfg := qmtCfg()
	if err := db.UpsertRealAccount(store.RealAccount{UserID: uid, AvailableCash: 5000,
		UpdatedAt: cntime.Now().Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("seed fresh cash: %v", err)
	}
	if _, err := db.ReconcilePositionsForUser(uid, []store.RealPosition{
		{TsCode: "600000.SH", Name: "测试", Qty: 500, CostPrice: 10, UpdatedAt: riskToday() + " 09:30:00"},
	}); err != nil {
		t.Fatalf("seed fresh positions: %v", err)
	}
	// 手动账故意写 10000 股：柜台新鲜时不许参与。
	if _, _, err := db.PutManualPositions(uid, []store.ManualPosition{
		{TsCode: "600000.SH", Qty: 10000, CostPrice: 10},
	}, riskToday()+" 09:35:00"); err != nil {
		t.Fatalf("seed manual positions: %v", err)
	}
	o := liveOrder(SideSell)
	o.Code, o.Qty, o.Amount = "600000.SH", 500, 5000
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("柜台新鲜时按柜台 500 股应放行, got %+v", v)
	}
	o2 := liveOrder(SideSell)
	o2.Code, o2.Qty, o2.Amount = "600000.SH", 501, 5010
	v2 := g.CheckLiveOrder(cfg, o2)
	if v2.Pass {
		t.Fatalf("卖 501 应超柜台持仓被拦, got %+v", v2)
	}
	if strings.Contains(v2.Reason, "手动实盘") {
		t.Fatalf("本轮用的是柜台基线，文案不该出现手动口径：%q", v2.Reason)
	}
}

// TestGateConcentrationManualDenominator 集中度分母在柜台不可信时吃手动合计，并在文案里点名成本价近似。
func TestGateConcentrationManualDenominator(t *testing.T) {
	db := gateDB(t)
	uid := "u_mb_conc"
	g := NewGate(db, uid, nil)
	cfg := qmtCfg()
	cfg.RiskGate.SingleStockValuePct = 5 // 上限 5%，让 10% 的敞口必被拦
	seedStaleCounter(t, db, uid, "600000.SH", 1000, 100000)
	// 手动账：现金 10 万 + 持仓 1000 股 @10 = 11 万；本票敞口 10000/110000 ≈ 9.1% > 5%。
	today := riskToday()
	if err := db.PutManualAccount(uid, 100000, true, 0, today+" 09:35:00"); err != nil {
		t.Fatalf("seed manual cash: %v", err)
	}
	if _, _, err := db.PutManualPositions(uid, []store.ManualPosition{
		{TsCode: "600000.SH", Qty: 1000, CostPrice: 10},
	}, today+" 09:35:00"); err != nil {
		t.Fatalf("seed manual positions: %v", err)
	}
	o := liveOrder(SideBuy)
	o.Amount = 1000
	v := g.CheckLiveOrder(cfg, o)
	if v.Pass {
		t.Fatalf("集中度应按手动分母 11 万算出 9.1%% > 5%% 而拒, got %+v", v)
	}
	if !strings.Contains(v.Reason, "手动实盘") {
		t.Fatalf("集中度拒单必须点名分母是手动实盘（成本价口径）, got %q", v.Reason)
	}
}
