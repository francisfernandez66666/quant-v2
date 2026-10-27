// dynbudget_v3_test.go — 实盘 dynamic 预算执行层锁（v3 §9.2）。
//
// 验证：dynamic 下固定金额/战法金额/每日次数/每日预算/ATR/最低成交额都不再影响股数；
// 行业 35% 与在途委托计入；legacy 路径不受影响。
//
// English: execution-layer locks for the v3 live dynamic budget — legacy amount/count/budget/ATR/
// min-notional knobs no longer change qty; the 35% industry room includes pending buys; the legacy
// path stays intact.
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// dynSizingCfg dynamic 预算 + 已退出影子的 sizing 配置。
func dynSizingCfg() config.SizingConfig {
	sc := config.DefaultSizingConfig()
	sc.Enabled = dynBoolPtr(true)
	shadow := false
	sc.Shadow = &shadow
	sc.BudgetMode = "dynamic"
	sc.Mode = "power_mean"
	return sc
}

// seedDynAccount 播种总资产 10 万 + 充足现金（dynamic 预算基数）。
func seedDynAccount(t *testing.T, db *store.DB) {
	t.Helper()
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_1", AvailableCash: 10_000_000, TotalAsset: 100000,
		UpdatedAt: cntime.In(time.Now()).Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

// wireDynEngine 注入账号/行情/行业源；离线测试不触网。
func wireDynEngine(t *testing.T, e *Engine) {
	t.Helper()
	e.SetMembers([]string{"u_1"}) // 单成员 → userID=u_1
	e.mu.Lock()
	e.marketAPI = &data.MarketAPI{}
	e.industryFn = func(code string) string { return "银行" }
	e.mu.Unlock()
}

// TestLiveDynamicBudgetIgnoresLegacyKnobs §9.2：dynamic 下旧预算/闸门全部不影响 qty。
func TestLiveDynamicBudgetIgnoresLegacyKnobs(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		// 把所有旧键都设成“会把订单改成别的样子”的值，证明它们已被 dynamic 预算让位。
		c.FixedAmount = 999999
		c.StrategyAmounts = map[string]float64{"龙头": 999999}
		c.DailyMaxBuys = 1
		c.DailyBudgetAmount = 1
		// §B0（owner 2026-10-27）：最小成交额闸在 dynamic 下**已恢复生效**（旧实现用
		// !dynamicBudget 把它整段跳过）。本用例要测的是"旧预算键不让位"，所以把它显式关掉
		// （负数=关）；闸本身的行为由 TestB0MinOrderBlocksDynamic 单独钉。
		c.MinOrderAmount = -1
		c.RiskGate.RiskPerTradePct = 50
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db)
	wireDynEngine(t, e)

	sig := combat_agent.Signal{ID: "SIG-DYN-1", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlaceScaled(sig, live, 0.82, true, dynBudgetCtx{}, dynSizingCfg())

	if len(*orders) != 1 {
		t.Fatalf("应下 1 单，got %d", len(*orders))
	}
	// §B0（2026-10-27）起单笔占比是"下限 10% → 上限 30% 按因子线性动"：ctx 未注入时
	// MS=SS=1 ⇒ eff=30% ⇒ 10 万×30%=3 万；行业/单票 35%、目标剩余 100% 都不拦 ⇒ 3000 股。
	// 旧的固定 10%（800 股）是 owner 报"碎单"的口径，已被替换；本用例只证明旧预算键不再影响股数。
	if qty := (*orders)[0]["qty"].(float64); qty != 3000 {
		t.Fatalf("单笔占比 eff=30%% 应为 3000 股，got %v（旧键仍在影响股数或 eff 未生效）", qty)
	}
}

// TestLiveDynamicBudgetIndustryRoom 行业已持 34% → 只减到剩余 1% 额度，不拒买。
func TestLiveDynamicBudgetIndustryRoom(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db)
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600001.SH", Name: "同业", Qty: 1000, CostPrice: 34, CurPrice: 34, UserID: "u_1",
	}}); err != nil {
		t.Fatalf("seed positions: %v", err)
	}
	wireDynEngine(t, e)

	sig := combat_agent.Signal{ID: "SIG-DYN-2", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlaceScaled(sig, live, 0.82, true, dynBudgetCtx{}, dynSizingCfg())

	if len(*orders) != 1 {
		t.Fatalf("额度不足 1 手才跳过；本案还剩 1%% 应下单，got %d", len(*orders))
	}
	if qty := (*orders)[0]["qty"].(float64); qty != 100 {
		t.Fatalf("行业剩余额度 1000 元 → 100 股，got %v", qty)
	}
}

// TestLiveDynamicBudgetCountsPendingBuys 在途买单计入行业市值：连续信号不得穿透 35%。
func TestLiveDynamicBudgetCountsPendingBuys(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db)
	// 在途查询按**交易日**过滤（非交易日回退到上一个交易日），这里必须用同一口径播种：
	// 用日历日会在周末/休市日静默失配（实录：周六跑本用例时委托查不到，额度未被计入而假绿）。
	// English: seed with the trading day, not the calendar day — PendingBuys filters by trading
	// day, so a weekend run would silently miss the order and pass for the wrong reason.
	td := data.TradingDayDate(time.Now()) // YYYYMMDD
	today := td[:4] + "-" + td[4:6] + "-" + td[6:] + " 09:30:00"
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "PEND-1", SignalID: "SIG-PEND-1", Code: "600001.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 3000, CreatedAt: today, UserID: "u_1",
	}); err != nil {
		t.Fatalf("seed pending order: %v", err)
	}
	wireDynEngine(t, e)

	sig := combat_agent.Signal{ID: "SIG-DYN-3", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlaceScaled(sig, live, 0.82, true, dynBudgetCtx{}, dynSizingCfg())

	if len(*orders) != 1 {
		t.Fatalf("在途 3 万后仍剩 5%% 额度，应下单，got %d", len(*orders))
	}
	if qty := (*orders)[0]["qty"].(float64); qty != 500 {
		t.Fatalf("在途 3 万应把 35000 额度压到 500 股，got %v", qty)
	}
}

// TestLiveLegacyBudgetUnchanged legacy 路径仍按固定金额，证明 dynamic 是显式开关。
func TestLiveLegacyBudgetUnchanged(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.FixedAmount = 10000
	})
	sig := combat_agent.Signal{ID: "SIG-LEG-1", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlaceScaled(sig, live, 0, false, dynBudgetCtx{}) // 无 sizing 配置 = legacy

	if len(*orders) != 1 {
		t.Fatalf("legacy 应下 1 单，got %d", len(*orders))
	}
	if qty := (*orders)[0]["qty"].(float64); qty != 1000 {
		t.Fatalf("legacy 固定金额 10000/10 = 1000 股，got %v", qty)
	}
}

// wireDynEngineIndustry 指定行业解析器的变体（空串=行业未知）。
func wireDynEngineIndustry(t *testing.T, e *Engine, industry string) {
	t.Helper()
	e.SetMembers([]string{"u_1"}) // 单成员 → userID=u_1
	e.mu.Lock()
	e.marketAPI = &data.MarketAPI{}
	e.industryFn = func(code string) string { return industry }
	e.mu.Unlock()
}

// TestLiveDynamicBudgetRoomBelowOneLotSkips §4/§5：行业只剩不到 1 手额度时必须跳过，
// 不允许"先凑 1 手"突破 35%（连续第二笔把额度吃光后的第三笔）。
// English: when the remaining industry room is under one lot the buy must skip — the one-lot
// floor may never force a trade through the 35% cap.
func TestLiveDynamicBudgetRoomBelowOneLotSkips(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db)
	// 行业已持 34,900：35% 额度仅剩 100 元 < 1 手（10 元×100=1000）→ 跳过。
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600001.SH", Name: "同业", Qty: 1000, CostPrice: 34.9, CurPrice: 34.9, UserID: "u_1",
	}}); err != nil {
		t.Fatalf("seed positions: %v", err)
	}
	wireDynEngine(t, e)

	sig := combat_agent.Signal{ID: "SIG-DYN-LOT", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlaceScaled(sig, live, 0.82, true, dynBudgetCtx{}, dynSizingCfg())

	if len(*orders) != 0 {
		t.Fatalf("额度不足 1 手必须跳过（1 手地板不得突破 35%%），got %d 单", len(*orders))
	}
}

// TestLiveDynamicBudgetUnknownIndustryFallsBackToStockCap §2/§5：行业未知不套行业额度，单票 35% 兜底。
// English: an unknown industry disables the industry room but the single-stock 35% still applies.
func TestLiveDynamicBudgetUnknownIndustryFallsBackToStockCap(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db)
	wireDynEngineIndustry(t, e, "") // 行业未知

	sig := combat_agent.Signal{ID: "SIG-DYN-UNKNOWN", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlaceScaled(sig, live, 0.82, true, dynBudgetCtx{}, dynSizingCfg())

	if len(*orders) != 1 {
		t.Fatalf("行业未知时单票 35%% 仍应放行，got %d 单", len(*orders))
	}
	// §B0 起单笔占比 = 0.10+0.20×(MS×SS)；ctx 未注入 ⇒ 30% ⇒ 3 万 < 单票 3.5 万额度，
	// 本例要证的是"行业未知不跳过"（额度没被行业分支清零），不是额度截断。
	if qty := (*orders)[0]["qty"].(float64); qty != 3000 {
		t.Fatalf("行业未知应放行（单笔 30%% ⇒ 3000 股），got %v", qty)
	}
}

// TestLiveDynamicBudgetAbstainsWithoutTotalAsset §1.5：总资产不可得时不自动开仓（未知不冒充安全）。
// English: an unavailable total asset must abstain rather than fabricate a budget.
func TestLiveDynamicBudgetAbstainsWithoutTotalAsset(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	// harness 默认注入了 1000 万可用现金，而 dynamicBuyQty 在 total_asset 缺失时会回落
	// 「账本合计=现金+持仓市值」——所以必须显式把账户行清零，才能测到"基数不可得"这一支。
	// English: zero the account row explicitly — with cash seeded, the ledger fallback would
	// supply a base and the abstain branch would never be reached.
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_1", AvailableCash: 0, TotalAsset: 0,
		UpdatedAt: cntime.In(time.Now()).Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("zero account: %v", err)
	}
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-DYN-NOEQ", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlaceScaled(sig, live, 0.82, true, dynBudgetCtx{}, dynSizingCfg())

	if len(*orders) != 0 {
		t.Fatalf("总资产不可得时不得开仓，got %d 单", len(*orders))
	}
}

// TestLiveLegacyStrategyAmountsStillApply §10：legacy 下战法金额仍生效——证明 dynamic 是显式开关，
// 而不是把旧键全局删掉（回滚路径必须完整可用）。
// English: legacy keeps strategy amounts — dynamic is an explicit switch, and the rollback path
// must remain fully functional.
func TestLiveLegacyStrategyAmountsStillApply(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.FixedAmount = 10000
		c.StrategyAmounts = map[string]float64{"龙头": 30000}
	})
	sig := combat_agent.Signal{ID: "SIG-LEG-SA", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	e.autoPlaceScaled(sig, live, 0, false, dynBudgetCtx{}) // legacy

	if len(*orders) != 1 {
		t.Fatalf("legacy 应下 1 单，got %d", len(*orders))
	}
	// 战法金额 30000 覆盖全局 10000 → 3000 股（若被误删会得 1000 股）。
	if qty := (*orders)[0]["qty"].(float64); qty != 3000 {
		t.Fatalf("legacy 战法金额应生效出 3000 股，got %v", qty)
	}
}

// TestLiveDynamicBudgetCapsClampedTo100 §5.6：行业/单票上限 >100 必须钳到 100，不能凭空放大预算。
func TestLiveDynamicBudgetCapsClampedTo100(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 150
		c.RiskGate.SingleStockValuePct = 150
	})
	seedDynAccount(t, db)
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-DYN-CAP", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	// 本例考的是"上限 100% vs 150% 的钳制"，必须让额度重新成为约束项 ⇒ 单笔基准设 100%。
	// （v4 出厂基准 10% 时本例的原始预算只有 8200 元，任何上限都拦不住，锁会假绿。）
	clampCfg := dynSizingCfg()
	clampCfg.PerTradePositionPct = 1.0
	e.autoPlaceScaled(sig, live, 0.82, true, dynBudgetCtx{}, clampCfg)
	if len(*orders) != 1 {
		t.Fatalf("应下 1 单，got %d", len(*orders))
	}
	// §B0 之后单笔占比被硬夹在 50%：本例能证的只是"caps>100 时不跳过、仍按上限内出单"。
	// 钳制本身（150 → 100）由 TestClampCapPct 直接钉——写在下单路径里会被 50% 单笔上限遮蔽。
	if qty := (*orders)[0]["qty"].(float64); qty != 5000 {
		t.Fatalf("单笔占比 50%% 上限下应出 5000 股，got %v", qty)
	}
}
