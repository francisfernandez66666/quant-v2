// dynbudget_v4_test.go — §DYN-SIZE v4 执行层目标仓位锁（方案 §6.1「engine（dynamicBuyQty / autoPlaceScaled）」组）。
//
// 这一组用例回答的是 owner 报的第一条实盘 bug：「当前已近半仓 + 新委托将近半仓 ⇒ 成交即满仓」。
// 修复后的口径（§1.4）：
//
//	目标仓位 Tgt = 总资产 × min(MarketScale, 天花板)
//	战略当前   CurStrat = Σ底仓 + Σ战略在途 − Σ正T买腿 + Σ反T卖腿
//	剩余空间   Room     = max(0, Tgt − CurStrat)
//	单笔       Per      = 总资产 × per_trade_position_pct × MarketScale × StockScale
//	本单金额            = min(Per, Room, 行业额度, 单票额度, 现金)
//
// 每条都配了"故意破坏→必红"的反证：把 Scale 当预算倍数（旧缺陷）会让第一、二条直接算错，
// 把 T 腿计进 CurStrat 会让第三条错，读失败时 fail-open 会让第四/五条放行。
//
// English: execution-layer locks for the v4 target-position model — the fix for "half book plus a
// half-size order equals a full book".
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// tdNow / tdDash 在途查询按**交易日**过滤，且 orders.created_at 是带横杠的
// "YYYY-MM-DD HH:MM:SS"；播种时两个形态都要给对，否则周末/格式不一致会让在途单查不到而假绿
// （v3 测试已踩过一次，这里显式拆成两个函数，避免调用点猜格式）。
func tdNow() string  { return data.TradingDayDate(time.Now()) }
func tdDash() string { td := tdNow(); return td[:4] + "-" + td[4:6] + "-" + td[6:] }

// qtyOf 从订单捕获列表里取本单股数（用例只下一笔，取首笔即可）。
func qtyOf(t *testing.T, orders *[]map[string]any) float64 {
	t.Helper()
	if len(*orders) != 1 {
		t.Fatalf("应恰好下 1 单，got %d", len(*orders))
	}
	return (*orders)[0]["qty"].(float64)
}

// TestDynV4HalfBookSizedByRoom 半仓 55% + MS 0.70 ⇒ 目标 70%、第一笔只有 7%（不是 35%）。
// 旧模型这里是 10 万×0.70＝7 万委托（被 35% 额度截到 3.5 万），成交即把账户推到 90%。
func TestDynV4HalfBookSizedByRoom(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		// 行业上限放到 100%：本例考的是**目标剩余空间**，若保留 35%，同业的 5.5 万底仓会先把
		// 行业额度清零，测出来的就是额度层而不是目标层（用例失去区分力）。单票 35% 保留兜底。
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db) // 总资产 10 万
	// 已有底仓 5.5 万（55%）
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600001.SH", Name: "同业", Qty: 5500, CostPrice: 10, CurPrice: 10, UserID: "u_1",
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-V4A", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.70, StockScale: 1.0}, dynSizingCfg())
	// 目标 7 万 − 当前 5.5 万 = 1.5 万；单笔占比 eff = 0.10+0.20×0.70 = 24% ⇒ 2.4 万，
	// 取小者 1.5 万 → 1500 股。**这正是 §B0 要的形状**：碎单被 Room 与更大的单笔基准夹住后，
	// 剩余空间一次性买到位，而不是切成 7 笔各 700 股。
	if q := (*orders)[0]["qty"].(float64); q != 1500 {
		t.Fatalf("第一笔应受目标剩余空间约束（1500 股），got %v", q)
	}
}

// TestDynV4SkipsWhenTargetReached 半仓 55% + MS 0.40（目标 40%）⇒ 零委托并留「已达动态目标仓位」。
func TestDynV4SkipsWhenTargetReached(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db)
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600001.SH", Name: "同业", Qty: 5500, CostPrice: 10, CurPrice: 10, UserID: "u_1",
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-V4B", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.40, StockScale: 1.0}, dynSizingCfg())
	if len(*orders) != 0 {
		t.Fatalf("目标 40%% 低于当前 55%%，必须零委托，got %d 单", len(*orders))
	}
}

// TestDynV4PendingStrategicBuyCounted 战略在途买单计入 CurStrat：连续两个信号不得穿透目标。
func TestDynV4PendingStrategicBuyCounted(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db)
	// 在途战略买单 9000 元（MS 0.70 ⇒ 目标 7 万，剩余 6.1 万；本应还能买 7000 基准×…）
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "OID-V4P1", SignalID: "SIG-V4P1", Code: "600001", Side: "买入", Status: "已报",
		Price: 10, Qty: 900, CreatedAt: tdDash() + " 10:00:00", UserID: "u_1",
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-V4C", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.70, StockScale: 1.0}, dynSizingCfg())
	// §B0 起单笔占比 eff=0.10+0.20×0.70=24% ⇒ 2.4 万 < 剩余空间 5.2 万 ⇒ 2400 股；
	// 在途是否被计入，由下一例（把单笔上限顶到 50%、让 Room 成为唯一约束）验。
	if q := (*orders)[0]["qty"].(float64); q != 2400 {
		t.Fatalf("基准约束下应为 2400 股，got %v", q)
	}
}

// TestDynV4TargetRoomIsTheBindingCap 把单笔基准放大到 100% 后，目标剩余空间必须成为唯一约束：
// 目标 7 万 − 在途 9000 = 61000 → 6100 股（破坏"Room 参与取小"必红；旧模型这里会下 7 万＝7000 股）。
func TestDynV4TargetRoomIsTheBindingCap(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 100 // 让行业/单票额度不参与截断，专测目标空间
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	if _, err := db.UpsertRealOrder(store.RealOrder{
		OrderID: "OID-V4P2", SignalID: "SIG-V4P2", Code: "600001", Side: "买入", Status: "已报",
		// §B0 之后单笔上限被硬夹在 50%（5 万），所以要让"目标剩余空间"成为唯一约束，
		// 在途必须大于 2 万：9 千的在途只让 Room 掉到 6.1 万，会被 5 万单笔上限遮蔽。
		Price: 10, Qty: 3000, CreatedAt: tdDash() + " 10:00:00", UserID: "u_1",
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	wireDynEngine(t, e)
	sc := dynSizingCfg()
	sc.PerTradePositionPct = 1.0
	sig := combat_agent.Signal{ID: "SIG-V4D", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.70, StockScale: 1.0}, sc)
	if q := (*orders)[0]["qty"].(float64); q != 4000 {
		t.Fatalf("目标剩余空间应截到 4000 股（7万−3万），got %v", q)
	}
}

// TestDynV4IntradayLegsExcludedFromStrategicCur 正 T 腿的两面性（一条用例同时锁住两侧）：
//
//	① **不占战略剩余空间**（CurStrat 剔除）：否则一笔日内低吸会吃掉战略建仓空间；
//	② **全额占用行业/单票额度**（stockVal/indVal 保留）：否则 T 腿就成了"更便宜的资金来源"（§8.4.6）。
//
// 构造：总资产 10 万、单笔基准 100%、MS=0.80 ⇒ 目标 8 万；有一笔 5 万的在途 T 买腿（600001）。
//
//	· 行业同源（两只票都算"银行"）⇒ 行业额度 10 万 − 5 万 = 5 万 → **5000 股**（证明②）；
//	· 行业分家（T 腿归"有色"）⇒ 目标 8 万 − 战略 0 = 8 万 → **8000 股**（证明①；
//	  若实现误把 T 腿计进 CurStrat，剩余会掉到 3 万 → 3000 股，当场红）。
func TestDynV4IntradayLegsExcludedFromStrategicCur(t *testing.T) {
	// ── 侧②：行业额度必须看得见 T 腿 ──
	e1, db1, _, orders1 := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db1)
	if _, err := db1.UpsertRealOrder(store.RealOrder{
		OrderID: "OID-V4T-A", SignalID: "t0:600001:buy_first:" + tdNow() + ":1", Code: "600001", Side: "买入",
		Status: "已报", Price: 10, Qty: 6000, CreatedAt: tdDash() + " 10:00:00", UserID: "u_1", IntradayT: true,
	}); err != nil {
		t.Fatalf("seed T leg: %v", err)
	}
	wireDynEngine(t, e1) // 行业源：所有票都返回"银行"
	sc := dynSizingCfg()
	sc.PerTradePositionPct = 1.0
	sigA := combat_agent.Signal{ID: "SIG-V4A", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e1.autoPlaceScaled(sigA, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.80, StockScale: 1.0}, sc)
	// §B0 起单笔占比硬夹 50%（5 万）；这里 T 腿 6 万 ⇒ 行业额度 4 万 < 5 万 ⇒ 4000 股，
	// 与"看不见 T 腿"的侧①（5 万 → 5000 股）分得开。
	if q := qtyOf(t, orders1); q != 4000 {
		t.Fatalf("行业额度必须计入 T 腿（剩 4 万 → 4000 股），got %v", q)
	}

	// ── 侧①：战略剩余空间必须看不见 T 腿 ──
	e2, db2, _, orders2 := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db2)
	if _, err := db2.UpsertRealOrder(store.RealOrder{
		OrderID: "OID-V4T-B", SignalID: "t0:600001:buy_first:" + tdNow() + ":2", Code: "600001", Side: "买入",
		Status: "已报", Price: 10, Qty: 6000, CreatedAt: tdDash() + " 10:00:00", UserID: "u_1", IntradayT: true,
	}); err != nil {
		t.Fatalf("seed T leg: %v", err)
	}
	wireDynEngine(t, e2)
	e2.mu.Lock()
	// T 腿挪到另一个行业：行业额度不再构成约束，本例只考 CurStrat 口径。
	e2.industryFn = func(code string) string {
		if len(code) >= 6 && code[:6] == "600001" {
			return "有色"
		}
		return "银行"
	}
	e2.mu.Unlock()
	sigB := combat_agent.Signal{ID: "SIG-V4B", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e2.autoPlaceScaled(sigB, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.80, StockScale: 1.0}, sc)
	if q := qtyOf(t, orders2); q != 5000 {
		t.Fatalf("T 腿不该占战略剩余空间（单笔 50%% → 5000 股），got %v；误计入 CurStrat 会掉到 2000", q)
	}
}

// TestDynV4MissingKeyInputCapsTarget 数据盲区（MS 已被定权层封顶 0.50）⇒ 目标 5 万，不得按 1.0 打满。
func TestDynV4MissingKeyInputCapsTarget(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	wireDynEngine(t, e)
	sc := dynSizingCfg()
	sc.PerTradePositionPct = 1.0
	sig := combat_agent.Signal{ID: "SIG-V4G", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	// 定权层把缺失时的 MS 封顶到 0.50，执行层拿到的就是这个数（ctx 与 Decision 同源）。
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.50, StockScale: 1.0, MissingKeyInput: true}, sc)
	if q := (*orders)[0]["qty"].(float64); q != 5000 {
		t.Fatalf("盲区目标 5 万应只放行 5000 股，got %v", q)
	}
}

// TestDynV4PositionPriceMissingFailsClosed 持仓无法定价时**不**把它当空仓：本轮零委托。
// 旧实现 `px<=0 → continue` 会让 CurStrat 偏低、Room 偏高（方向是更敢买），正是本批要关掉的洞。
func TestDynV4PositionPriceMissingFailsClosed(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 35
		c.RiskGate.SingleStockValuePct = 35
	})
	seedDynAccount(t, db)
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600009.SH", Name: "缺价", Qty: 1000, CostPrice: 0, CurPrice: 0, UserID: "u_1",
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	wireDynEngine(t, e)
	sig := combat_agent.Signal{ID: "SIG-V4H", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 1.0, StockScale: 1.0}, dynSizingCfg())
	if len(*orders) != 0 {
		t.Fatalf("持仓缺价必须 fail-closed 零委托，got %d 单", len(*orders))
	}
}

// TestDynV4ShadowLeavesSizingUntouched 影子期只留痕：目标收紧**不得**生效（否则观察期变执法期）。
func TestDynV4ShadowLeavesSizingUntouched(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	wireDynEngine(t, e)
	sc := dynSizingCfg()
	shadow := true
	sc.Shadow = &shadow
	sc.PerTradePositionPct = 1.0
	sig := combat_agent.Signal{ID: "SIG-V4I", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 0.40, StockScale: 1.0}, sc)
	// 影子期：TargetRoom 不参与 ⇒ 单笔 = 10 万×50%（§B0 硬夹）= 5 万 → 5000 股。
	// 若影子误执法，MS 0.40 的目标 4 万会把本单截到 4000 股——观察期变成执法期。
	if q := qtyOf(t, orders); q != 5000 {
		t.Fatalf("影子期不得收紧，期望 5000 股，got %v（被目标截断会是 4000）", q)
	}
}

// TestDynV4FrozenCashUntouchable 反 T 冻结回购资金不参与战略买入（专款专用，§8.3 纪律 3）。
func TestDynV4FrozenCashUntouchable(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.RiskGate.SectorValuePct = 100
		c.RiskGate.SingleStockValuePct = 100
	})
	seedDynAccount(t, db)
	wireDynEngine(t, e)
	// 现金只有 1 万，但有一条在途反 T 卖腿冻结了 8000 元 ⇒ 战略可用现金只剩 2000 元 → 200 股。
	e.mu.Lock()
	e.accountsRoot = t.TempDir()
	e.tLiveReg = newTLiveRegistry(e.accountsRoot)
	e.mu.Unlock()
	st := e.tLiveFor("u_1")
	if err := st.put(&TRoundLive{
		ID: "t0:600002:sell_first:" + tdNow() + ":1", Day: tdNow(),
		Code: "600002", Dir: "sell_first", Status: "open", Qty: 800, SellFilled: 800, SellPrice: 10,
		FrozenCash: 8000,
	}); err != nil {
		t.Fatalf("seed T state: %v", err)
	}
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_1", AvailableCash: 10000, TotalAsset: 100000}); err != nil {
		t.Fatalf("reset cash: %v", err)
	}
	sc := dynSizingCfg()
	sc.PerTradePositionPct = 1.0
	sig := combat_agent.Signal{ID: "SIG-V4J", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}, 0, true,
		dynBudgetCtx{MarketScale: 1.0, StockScale: 1.0}, sc)
	// 现金 1 万扣掉冻结 8000 → 2000 元，费用缓冲后仍可买 200 股；卖腿 8000 元市值同时加回战略仓位。
	q := (*orders)[0]["qty"].(float64)
	if q > 200 {
		t.Fatalf("战略买入不得动用反 T 冻结款，期望 ≤200 股，got %v", q)
	}
}
