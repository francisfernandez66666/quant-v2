// Package paper 独立模拟盘（纸面交易）引擎：把策略信号按实时价撮合成虚拟持仓，产出净值曲线并记录滑点/延迟，与真实持仓完全隔离。
package paper

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// poolSnapshotCost 返回某池的累计买入成本（StrategyPools 快照）。
// English: returns a pool's cumulative buy cost from the StrategyPools snapshot.
func poolSnapshotCost(e *Engine, key string) float64 {
	for _, p := range e.StrategyPools() {
		if p.Key == key {
			return p.Cost
		}
	}
	return 0
}

// TestPoolBudgetDayStartCashFixed §修复 FIX#4：预算分母=日初池现金（跨日重捕获、日内不缩水）。
// 旧实现 dayStartCash 恒 0 → 分母回退当前缩水池现金，越买分母越小越松。
// English: §FIX#4 regression — the daily budget denominator stays fixed at the day-start pool cash
// (recaptured on the first buy of a new day); intraday debits never shrink it.
func TestPoolBudgetDayStartCashFixed(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"n_shape"}) // 100000 / 2 池 → n_shape 日初 50000
	e.SetPoolBuyRule("n_shape", &PoolBuyRule{BudgetPctPerDay: 30, MaxDailyBuys: 100})
	quotes := map[string]*data.StockInfo{"600000.SH": {Price: 10}}
	// 首日首笔：预算 50000×30%=15000；先买 5000（500股@10）
	if err := e.BuyExInPool("600000.SH", "浦发", "N形", "n_shape", 10, 10, 500, quotes); err != nil {
		t.Fatalf("首笔买入失败: %v", err)
	}
	d := e.poolDiscipline["n_shape"]
	if math.Abs(d.dayStartCash-50000) > 1e-9 {
		t.Fatalf("日初现金应锁定 50000（日初快照，非扣款后 45000）, got %.2f", d.dayStartCash)
	}
	if math.Abs(d.spentToday-5000) > 1e-9 {
		t.Fatalf("当日已花费应 5000, got %.2f", d.spentToday)
	}
	// 当日第二笔 12000（1200股@10）：5000+12000=17000>15000 超限 → 拒绝
	if err := e.BuyExInPool("000001.SZ", "平安", "N形", "n_shape", 10, 10, 1200, quotes); err == nil {
		t.Fatal("同日内累计 17000 超预算 15000 应被拒绝")
	}
	// 当日第三笔 9000：5000+9000=14000≤15000 放行（若分母按扣款后缩水现金将 <14000 误拒）
	if err := e.BuyExInPool("000002.SZ", "万科", "N形", "n_shape", 10, 10, 900, quotes); err != nil {
		t.Fatalf("同日内 5000+9000=14000≤15000 应放行（分母不得缩水）, got %v", err)
	}
	// 模拟跨日（强制 day 落回昨天）：下次首笔重新捕获"本日开始时池现金"（此时池现金 36000）
	prevDay := cntime.DayOf(time.Now().Add(-48 * time.Hour))
	e.poolDiscipline["n_shape"] = poolDiscipline{day: prevDay}
	if err := e.BuyExInPool("600000.SH", "浦发", "N形", "n_shape", 10, 10, 200, quotes); err != nil {
		t.Fatalf("跨日首笔应放行: %v", err)
	}
	d = e.poolDiscipline["n_shape"]
	if math.Abs(d.dayStartCash-36000) > 1e-9 {
		t.Fatalf("跨日首笔应重新锁定日初池现金 36000, got %.2f", d.dayStartCash)
	}
	if math.Abs(d.spentToday-2000) > 1e-9 {
		t.Fatalf("跨日计数应重置为本次 2000, got %.2f", d.spentToday)
	}
	// 新日预算 = 36000×30% = 10800；再买 8000（累计 10000）放行、当日累计 11000 拒绝
	if err := e.BuyExInPool("000001.SZ", "平安", "N形", "n_shape", 10, 10, 800, quotes); err != nil {
		t.Fatalf("新日 2000+8000=10000 未超 10800 应放行, got %v", err)
	}
	if err := e.BuyExInPool("000002.SZ", "万科", "N形", "n_shape", 10, 10, 100, quotes); err == nil {
		t.Fatal("新日累计 2000+8000+1000=11000>10800 应被拒绝")
	}
}

// TestPoolCapsDecoupled 验证每池持仓上限与全局解耦：设置池上限后该池持仓数被约束，
// 其余池不受影响；池上限 0 = 不单独设限。
// English: verifies per-pool position caps decoupled from the global cap — a pool's holdings are bound
// by its own cap while other pools are unaffected; 0 = no per-pool limit.
func TestPoolCapsDecoupled(t *testing.T) {
	c := testCfg()
	c.MaxPositions = 5 // 全局上限 5（不设限语义下也不至于影响测试）
	e := New(c, "")
	e.SetStrategyPools([]string{"n_shape", "dragon"})
	e.SetPoolCaps(map[string]int{"n_shape": 2})
	now := time.Now()
	quotes := map[string]*data.StockInfo{"600000.SH": {Price: 10}, "000001.SZ": {Price: 5}, "000002.SZ": {Price: 8}, "601000.SH": {Price: 6}, "601001.SH": {Price: 6}}
	// 3 个 n_shape 信号：池上限 2 → 只买 2 个
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "A", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
		{Code: "000001.SZ", Name: "B", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 5, GeneratedAt: now},
		{Code: "000002.SZ", Name: "C", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 8, GeneratedAt: now},
	}, quotes)
	// dragon 池：无上限，1 个信号买入
	e.OnSignals([]combat_agent.Signal{
		{Code: "601000.SH", Name: "D", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 6, GeneratedAt: now},
	}, quotes)
	ns := e.PoolStats("n_shape")
	dr := e.PoolStats("dragon")
	if ns.OpenPositions != 2 {
		t.Errorf("n_shape 池上限 2 应只买 2 仓, 实际 %d", ns.OpenPositions)
	}
	if dr.OpenPositions != 1 {
		t.Errorf("dragon 池不应受 n_shape 上限影响, 实际 %d", dr.OpenPositions)
	}
	// 池上限 0 = 不单独设限：dragon 再买 1 个不受池级约束（全局 5 未到）
	e.OnSignals([]combat_agent.Signal{
		{Code: "601001.SH", Name: "E", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 6, GeneratedAt: now},
	}, quotes)
	if got := e.PoolStats("dragon").OpenPositions; got != 2 {
		t.Errorf("dragon 池无上限应可继续买, 实际 %d", got)
	}
	// 快照应暴露池上限字段
	for _, p := range e.StrategyPools() {
		if p.Key == "n_shape" && p.MaxPos != 2 {
			t.Errorf("n_shape 池上限应在快照中为 2, 实际 %d", p.MaxPos)
		}
	}
}

// TestPoolAllocsConserve 验证资金分配守恒：Σ池现金=总现金，指定池按目标额、未指定池均分剩余。
// English: verifies cash-allocation conservation — Σpool cash = total cash; given pools take their
// targets and unmentioned pools split the remainder evenly.
func TestPoolAllocsConserve(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"n_shape", "dragon"})
	total := e.cash // 100000
	e.SetPoolAllocs(map[string]float64{"n_shape": 30000})
	sum := 0.0
	for _, p := range e.StrategyPools() {
		sum += p.Cash
	}
	if sum != total {
		t.Errorf("Σ池现金应等于总现金 %.2f, 实际 %.2f", total, sum)
	}
	var ns, dr, other float64
	for _, p := range e.StrategyPools() {
		switch p.Key {
		case "n_shape":
			ns = p.Cash
		case "dragon":
			dr = p.Cash
		case "":
			other = p.Cash
		}
	}
	if ns != 30000 {
		t.Errorf("n_shape 池应 30000, 实际 %.2f", ns)
	}
	// 剩余 70000 均分给 dragon + 其他池
	exp := (total - 30000) / 2
	if dr != exp || other != exp {
		t.Errorf("dragon/其他 应各 %.2f, 实际 %.2f/%.2f", exp, dr, other)
	}
	// 持久化后恢复
	path := t.TempDir() + "/paper.json"
	e2 := New(testCfg(), path)
	e2.SetStrategyPools([]string{"n_shape", "dragon"})
	e2.SetPoolAllocs(map[string]float64{"n_shape": 40000})
	e3 := New(testCfg(), path)
	sum2 := 0.0
	for _, p := range e3.StrategyPools() {
		sum2 += p.Cash
	}
	if sum2 != 100000 {
		t.Errorf("重启后 Σ池现金应仍守恒为 100000, 实际 %.2f", sum2)
	}
}

// TestPoolPerfBackfillLegacy 验证旧数据迁移：池有持仓但无累计成本基准时，load 回填
// 成本到持仓成本合计，避免分母≈0 放大涨跌幅；已有成本的池不受影响。
// English: verifies legacy migration — a pool holding positions without a recorded cost basis gets its
// Cost backfilled to the open-position cost sum on load, so a ≈0 denominator can't blow up the return;
// pools already accruing cost are untouched.
func TestPoolPerfBackfillLegacy(t *testing.T) {
	path := t.TempDir() + "/paper.json"
	c := testCfg()
	e := New(c, path)
	e.SetStrategyPools([]string{"factor"})
	now := time.Now()
	// 因子池买入 2 笔（每笔 10000 成本），再卖 1 笔，使其池现金、持仓、成本都真实存在
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "因子", StrategyType: "factor", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
		{Code: "000001.SZ", Name: "平安", Strategy: "因子", StrategyType: "factor", Direction: "做多", Action: "buy", Price: 5, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}, "000001.SZ": {Price: 5}})
	t1Ready(e) // §R3 T+1
	if err := e.Sell("600000.SH", map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("卖出失败: %v", err)
	}
	// 篡改为旧数据形态：池 cost=0（未记录历史），但仍持有 1 笔 000001.SZ（成本 10000）
	st := persistedState{Cash: e.cash, Pools: e.pools, PoolTypes: e.poolTypes, Positions: e.positions, Trades: e.trades}
	raw, _ := json.Marshal(st)
	_ = os.WriteFile(path, raw, 0644)

	e2 := New(c, path)
	var f *StrategyPoolState
	for i := range e2.StrategyPools() {
		p := e2.StrategyPools()[i]
		if p.Key == "factor" {
			f = &p
		}
	}
	if f == nil {
		t.Fatalf("应存在 factor 池")
	}
	if f.Cost < 10000 {
		t.Errorf("旧数据应回填成本至持仓成本合计（≥10000）, 实际 %.2f", f.Cost)
	}
	// 新代码池（已有 cost）不被覆盖：重新写入带成本的状态再 load
	e3 := New(c, path)
	e3.poolPerf["factor"].Cost = 25000
	raw2, _ := json.Marshal(persistedState{Cash: e3.cash, Pools: e3.pools, PoolTypes: e3.poolTypes, Positions: e3.positions, Trades: e3.trades, PoolPerf: e3.poolPerf})
	_ = os.WriteFile(path, raw2, 0644)
	e4 := New(c, path)
	for i := range e4.StrategyPools() {
		p := e4.StrategyPools()[i]
		if p.Key == "factor" && p.Cost != 25000 {
			t.Errorf("已有成本的池应保留 25000, 实际 %.2f", p.Cost)
		}
	}
}

// testCfg 返回测试用模拟盘配置。
func testCfg() Config {
	c := DefaultConfig()
	c.Enabled = true
	c.SlippageBps = 0    // 测试关闭滑点（有专项测试验证滑点模型）
	c.CommissionRate = 0 // 测试关闭手续费
	c.StampTaxRate = 0   // 测试关闭印花税
	c.MinCommission = 0
	c.InitialCapital = 100000
	c.FixedAmount = 10000
	c.MaxPositions = 10
	return c
}

// TestOnSignalsFillAtLivePrice 验证 OnSignals 按实时快照价撮合：成交价=实时价而非信号价，
// 并记录信号价参照与信号→成交延迟；watch 信号不成交；现金按实际支出扣减。
// English: verifies OnSignals fills at the live snapshot price (not the signal price), records the
// signal-price reference and the signal→fill latency, skips "watch" signals, and debits cash exactly.
func TestOnSignalsFillAtLivePrice(t *testing.T) {
	e := New(testCfg(), "")
	now := time.Now()
	quotes := map[string]*data.StockInfo{
		"600000.SH": {Price: 10.0},
		"000001.SZ": {Price: 5.2},
	}
	sigs := []combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发银行", Strategy: "N形", Direction: "做多", Action: "buy", Price: 9.8, GeneratedAt: now.Add(-30 * time.Second)},
		{Code: "000001.SZ", Name: "平安银行", Strategy: "龙头", Direction: "做多", Action: "buy", Price: 5.0, GeneratedAt: now.Add(-10 * time.Second)},
		{Code: "000002.SZ", Name: "万科A", Strategy: "动量", Direction: "做多", Action: "watch", Price: 8.0},
	}
	e.OnSignals(sigs, quotes)
	pos := e.Positions()
	if len(pos) != 2 {
		t.Fatalf("期望 2 笔持仓, 实际 %d", len(pos))
	}
	for _, p := range pos {
		if p.Code == "600000.SH" {
			if p.Qty != 1000 || p.CostPrice != 10.0 {
				t.Errorf("600000: 期望 1000股@10.0(实时价), 实际 %d股@%.2f", p.Qty, p.CostPrice)
			}
			if p.SignalPrice != 9.8 {
				t.Errorf("信号价参照应为 9.8, 实际 %.2f", p.SignalPrice)
			}
			if p.LatencySec() != 30 {
				t.Errorf("延迟应为 30s, 实际 %d", p.LatencySec())
			}
		}
		if p.Code == "000001.SZ" {
			if p.Qty != 1900 || p.CostPrice != 5.2 {
				t.Errorf("000001: 期望 1900股@5.2(实时价), 实际 %d股@%.2f", p.Qty, p.CostPrice)
			}
		}
	}
	// watch 不成交
	if trades := e.Trades(); len(trades) != 2 {
		t.Fatalf("期望 2 笔成交, 实际 %d", len(trades))
	}
	// 现金 = 100000 - 10000 - 9880
	st := e.Stats()
	if got := st.Cash; got != 100000-10000-9880 {
		t.Errorf("现金应为 %v, 实际 %v", 100000-10000-9880, got)
	}
	if st.AvgLatencySec != 20 {
		t.Errorf("平均延迟应为 20s, 实际 %.1f", st.AvgLatencySec)
	}
}

// TestSkipHeldAndNoQuote 验证同票重复信号去重（已持仓跳过）与无行情信号一律拒绝撮合（不伪造成交）。
// English: verifies de-duplication of repeated signals for an already-held code, and that signals with
// no live quote are rejected (no fabricated fills).
func TestSkipHeldAndNoQuote(t *testing.T) {
	e := New(testCfg(), "")
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	// 同票重复信号：已持仓跳过
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "龙头", Direction: "做多", Action: "buy", Price: 10.5, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10.5}})
	if len(e.Positions()) != 1 {
		t.Fatalf("同票应去重, 实际 %d 仓", len(e.Positions()))
	}
	// §R3-3 P0-E：无行情信号一律拒绝撮合（不伪造成交）——此前会回退信号价虚拟成交。
	e.OnSignals([]combat_agent.Signal{
		{Code: "000001.SZ", Name: "平安", Strategy: "N形", Direction: "做多", Action: "buy", Price: 5, GeneratedAt: now},
	}, map[string]*data.StockInfo{})
	if len(e.Positions()) != 1 {
		t.Fatalf("无行情应拒绝撮合, 实际 %d 仓（期望 1，去重后仍只有浦发）", len(e.Positions()))
	}
}

// TestMarkToMarketAndSnapshot 验证 MarkToMarket 刷新持仓市值与 Snapshot 记录当日净值点，
// 且同日重复快照去重不新增（同一交易日只保留最新一个点）。
// English: verifies MarkToMarket refreshes the mark price and Snapshot records one equity point per
// trading day, with same-day repeats de-duplicated.
func TestMarkToMarketAndSnapshot(t *testing.T) {
	e := New(testCfg(), "")
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	// 价格升到 11，估值刷新
	e.MarkToMarket(map[string]*data.StockInfo{"600000.SH": {Price: 11}})
	e.Snapshot(now)
	st := e.Stats()
	if got := st.MarketValue; got != 11000 {
		t.Errorf("市值应为 11000, 实际 %.2f", got)
	}
	if got := st.TotalValue; got != 11000+(100000-10000) {
		t.Errorf("总资产应为 %v, 实际 %.2f", 11000+(100000-10000), got)
	}
	if eq := e.Equity(); len(eq) != 1 {
		t.Fatalf("净值点应为 1, 实际 %d", len(eq))
	}
	// 同日再次快照：覆盖不新增
	e.Snapshot(now.Add(5 * time.Minute))
	if eq := e.Equity(); len(eq) != 1 {
		t.Fatalf("同日净值点应去重为 1, 实际 %d", len(eq))
	}
}

// t1Ready §R3 测试辅助：把全部持仓 FilledAt 回拨到 25 小时前，模拟"次日卖出"绕过 T+1。
// English: test helper — backdates every position's FilledAt by 25h to simulate next-day selling
// past the T+1 gate.
func t1Ready(e *Engine) {
	e.mu.Lock()
	defer e.mu.Unlock()
	y := time.Now().Add(-25 * time.Hour)
	for _, p := range e.positions {
		p.FilledAt = y
	}
}

// TestSellAndReset 验证卖出结算已实现盈亏、清盘重置（不改资金，清空全部持仓/成交/净值），
// 以及确认资金（重开并保留成交日志、净值从新资金重开、持仓上限生效）。
// English: verifies sell settlement of realized P&L, Reset (clears everything, keeps capital), and
// Reconfigure (reopens with new capital, keeps fill log, applies the new position cap).
func TestSellAndReset(t *testing.T) {
	e := New(testCfg(), "")
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	t1Ready(e) // §R3 T+1：模拟次日卖出
	// 12 元卖出：盈利 2000
	if err := e.Sell("600000.SH", map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("卖出失败: %v", err)
	}
	st := e.Stats()
	if got := st.RealizedPnl; got != 2000 {
		t.Errorf("已实现盈亏应为 2000, 实际 %.2f", got)
	}
	if len(e.Positions()) != 0 {
		t.Fatalf("应已清仓")
	}
	// 清盘重置：不改资金，现金恢复初始，全部清空（持仓/成交/净值）
	e.Reset()
	st = e.Stats()
	if st.Cash != 100000 || len(e.Trades()) != 0 || len(e.Equity()) != 0 {
		t.Errorf("清盘重置后应回到初始状态: cash=%v trades=%d equity=%d", st.Cash, len(e.Trades()), len(e.Equity()))
	}
	// 确认资金：设新初始资金/上限，保留成交日志，净值从新资金重开
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	e.Snapshot(now)
	before := len(e.Trades())
	e.Reconfigure(500000, 5)
	st = e.Stats()
	if st.InitialCapital != 500000 || st.Cash != 500000 {
		t.Errorf("确认资金后初始资金/现金应为 500000, 实际 %v/%v", st.InitialCapital, st.Cash)
	}
	if len(e.Positions()) != 0 || len(e.Equity()) != 0 {
		t.Errorf("确认资金后应清空持仓与净值曲线: pos=%d equity=%d", len(e.Positions()), len(e.Equity()))
	}
	if got := len(e.Trades()); got != before {
		t.Errorf("确认资金应保留成交日志: 之前 %d 笔, 现在 %d 笔", before, got)
	}
	if got := e.Cfg().MaxPositions; got != 5 {
		t.Errorf("确认资金后持仓上限应为 5, 实际 %d", got)
	}
}

// TestMaxPositions 验证自定义持仓上限生效：达上限后后续买入信号被拒（不再建仓）。
// English: verifies the custom position cap — once hit, later buy signals are rejected (no new positions).
func TestMaxPositions(t *testing.T) {
	c := testCfg()
	c.MaxPositions = 2
	e := New(c, "")
	now := time.Now()
	quotes := map[string]*data.StockInfo{
		"600000.SH": {Price: 10},
		"000001.SZ": {Price: 5},
		"000002.SZ": {Price: 8},
	}
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "A", Strategy: "N形", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
		{Code: "000001.SZ", Name: "B", Strategy: "N形", Direction: "做多", Action: "buy", Price: 5, GeneratedAt: now},
		{Code: "000002.SZ", Name: "C", Strategy: "N形", Direction: "做多", Action: "buy", Price: 8, GeneratedAt: now},
	}, quotes)
	if len(e.Positions()) != 2 {
		t.Fatalf("应受 2 仓上限约束, 实际 %d", len(e.Positions()))
	}
}

// TestDepositPreservesPositions 验证注入资金是增量：现金增加、持仓/成交/净值保留、收益基准累计。
func TestDepositPreservesPositions(t *testing.T) {
	e := New(testCfg(), "")
	now := time.Now()
	quotes := map[string]*data.StockInfo{"600000.SH": {Price: 10.0}}
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发银行", Strategy: "N形", Direction: "做多", Action: "buy", Price: 9.8, GeneratedAt: now.Add(-30 * time.Second)},
	}, quotes)
	if got := len(e.Positions()); got != 1 {
		t.Fatalf("注入前应已有 1 笔持仓, 实际 %d", got)
	}
	tradesBefore := len(e.Trades())
	cashBefore := e.Stats().Cash
	equityBefore := len(e.Equity())
	capBefore := e.Cfg().InitialCapital

	e.Deposit(50000)

	st := e.Stats()
	if st.Cash != cashBefore+50000 {
		t.Errorf("现金应增量 +50000（%.2f → %.2f）", cashBefore, st.Cash)
	}
	if len(e.Positions()) != 1 {
		t.Errorf("注入后持仓不应被清空, 实际 %d", len(e.Positions()))
	}
	if len(e.Trades()) != tradesBefore {
		t.Errorf("注入后成交日志不应被清空")
	}
	if len(e.Equity()) != equityBefore {
		t.Errorf("注入后净值曲线不应被清空")
	}
	if st.InitialCapital != capBefore+50000 {
		t.Errorf("累计投入基准应 +50000（%.2f → %.2f）", capBefore, st.InitialCapital)
	}
}

// TestPoolPerfTracksReturn 验证战法资金池持久化表现：买入累计成本、卖出仍记本池（已实现盈亏跨重启保留）、
// 浮动盈亏计入总涨跌幅。
// English: verifies the strategy-pool persisted performance — buy cost accumulates, sells still
// attribute to the pool (realized P&L survives restart), floating P&L feeds the total return.
func TestPoolPerfTracksReturn(t *testing.T) {
	c := testCfg()
	e := New(c, "")
	e.SetStrategyPools([]string{"n_shape", "dragon"})
	now := time.Now()
	quotes := map[string]*data.StockInfo{"600000.SH": {Price: 10.0}}
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 9.8, GeneratedAt: now},
	}, quotes)
	// n_shape 池：成本 10000（FixedAmount）
	pools := e.StrategyPools()
	var ns, dragon *StrategyPoolState
	for i := range pools {
		if pools[i].Key == "n_shape" {
			ns = &pools[i]
		}
		if pools[i].Key == "dragon" {
			dragon = &pools[i]
		}
	}
	if ns == nil || ns.Cost != 10000 {
		t.Fatalf("n_shape 池成本应为 10000, 实际 %+v", ns)
	}
	// 浮盈：12 元估值 → 浮动盈亏 +2000，总涨跌幅 +20%
	e.MarkToMarket(map[string]*data.StockInfo{"600000.SH": {Price: 12}})
	pools = e.StrategyPools()
	for i := range pools {
		if pools[i].Key == "n_shape" {
			ns = &pools[i]
		}
	}
	if ns.Floating != 2000 || ns.Realized != 0 {
		t.Errorf("n_shape 浮盈应为 2000/已实现 0, 实际 浮盈%.2f/已实现%.2f", ns.Floating, ns.Realized)
	}
	if ns.ReturnPct != 20 {
		t.Errorf("n_shape 总涨跌幅应为 20%%, 实际 %.2f%%", ns.ReturnPct)
	}
	t1Ready(e) // §R3 T+1：模拟次日卖出
	// 12 元卖出 → 已实现 +2000，成本保留 10000，总涨跌幅仍 +20%（卖出仍记本池）
	if err := e.Sell("600000.SH", map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("卖出失败: %v", err)
	}
	pools = e.StrategyPools()
	for i := range pools {
		if pools[i].Key == "n_shape" {
			ns = &pools[i]
		}
	}
	if ns.Realized != 2000 || ns.Cost != 10000 {
		t.Errorf("卖出后 n_shape 已实现应 2000/成本保留 10000, 实际 %.2f/%.2f", ns.Realized, ns.Cost)
	}
	if ns.ReturnPct != 20 {
		t.Errorf("卖出后 n_shape 总涨跌幅应 20%%, 实际 %.2f%%", ns.ReturnPct)
	}
	// dragon 池从未交易：成本/已实现/涨跌幅均为 0
	if dragon.Cost != 0 || dragon.Realized != 0 || dragon.ReturnPct != 0 {
		t.Errorf("dragon 池应无交易, 实际 %+v", dragon)
	}
}

// TestPoolPerfPersists 验证 poolPerf 跨重启保留（模拟持久化：新引擎从同一路径加载）。
// English: verifies poolPerf survives a restart (a fresh engine loads the same file).
func TestPoolPerfPersists(t *testing.T) {
	path := t.TempDir() + "/paper.json"
	c := testCfg()
	e := New(c, path)
	e.SetStrategyPools([]string{"n_shape"})
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 9.8, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10.0}})
	t1Ready(e) // §R3 T+1：模拟次日卖出
	if err := e.Sell("600000.SH", map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("卖出失败: %v", err)
	}
	// 重启：新引擎加载同一文件
	e2 := New(c, path)
	var ns *StrategyPoolState
	for i := range e2.StrategyPools() {
		p := e2.StrategyPools()[i]
		if p.Key == "n_shape" {
			ns = &p
		}
	}
	if ns == nil || ns.Cost != 10000 || ns.Realized != 2000 {
		t.Fatalf("重启后 n_shape 成本/已实现应为 10000/2000, 实际 %+v", ns)
	}
	if ns.ReturnPct != 20 {
		t.Errorf("重启后 n_shape 总涨跌幅应 20%%, 实际 %.2f%%", ns.ReturnPct)
	}
}

// TestPoolStatsScoped 验证池级 Stats 只统计该池：总资产=池现金+池内持仓市值，
// 滑点/延迟/已撮合信号仅计该池成交；全账号 Stats 与池级可区分。
// English: verifies pool-scoped Stats only count that pool — total value = pool cash + pool market
// value, slippage/latency/filled-buys count only that pool's fills; global vs pool stats differ.
func TestPoolStatsScoped(t *testing.T) {
	c := testCfg()
	e := New(c, "")
	e.SetStrategyPools([]string{"n_shape"})
	now := time.Now()
	// n_shape 池买入：信号价 9.8，成交 10.0（滑点 +2.04%），延迟 30s
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 9.8, GeneratedAt: now.Add(-30 * time.Second)},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10.0}})
	e.MarkToMarket(map[string]*data.StockInfo{"600000.SH": {Price: 11}})
	global := e.Stats()
	pool := e.PoolStats("n_shape")
	if pool.Cash != e.pools["n_shape"] {
		t.Errorf("池现金应为 %.2f, 实际 %.2f", e.pools["n_shape"], pool.Cash)
	}
	if pool.OpenPositions != 1 {
		t.Errorf("池持仓数应为 1, 实际 %d", pool.OpenPositions)
	}
	if pool.TotalValue != e.pools["n_shape"]+11000 {
		t.Errorf("池总资产应 = 池现金+市值, 实际 %.2f", pool.TotalValue)
	}
	if pool.FilledBuys != 1 {
		t.Errorf("池已撮合信号应为 1, 实际 %d", pool.FilledBuys)
	}
	if pool.AvgLatencySec != 30 {
		t.Errorf("池平均延迟应为 30s, 实际 %.1f", pool.AvgLatencySec)
	}
	// 滑点 = (10.0-9.8)/9.8 = +2.04%
	if got := round2(pool.AvgSlippagePct); got != 2.04 {
		t.Errorf("池平均滑点应为 2.04%%, 实际 %.2f%%", got)
	}
	// 全账号与池现金不同（全账号含其他池），且全账号持仓数一致
	if global.Cash == pool.Cash {
		t.Errorf("全账号现金应不等于池现金")
	}
	// 空池：无数据
	empty := e.PoolStats("dragon")
	if empty.OpenPositions != 0 || empty.FilledBuys != 0 || empty.TotalValue != e.pools["dragon"] {
		t.Errorf("空池统计应为 0 持仓/0 信号/总资产=池现金, 实际 %+v", empty)
	}
}

// TestResetPoolOnly 验证单池清盘：只清指定池持仓与表现，其余池与全局净值不受影响。
// English: verifies pool-level reset — only that pool's positions/perf are cleared; other pools and
// the global equity/realized are untouched.
func TestResetPoolOnly(t *testing.T) {
	c := testCfg()
	e := New(c, "")
	e.SetStrategyPools([]string{"n_shape", "dragon"})
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 9.8, GeneratedAt: now},
		{Code: "000001.SZ", Name: "平安", Strategy: "龙", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 8.0, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10.0}, "000001.SZ": {Price: 8.0}})
	if len(e.Positions()) != 2 {
		t.Fatalf("应持有 2 仓, 实际 %d", len(e.Positions()))
	}
	// 估值：n_shape +20%（11 元），dragon 不变（8 元）
	e.MarkToMarket(map[string]*data.StockInfo{"600000.SH": {Price: 11}, "000001.SZ": {Price: 8}})
	e.Snapshot(now)
	eqBefore := len(e.Equity())
	realizedBefore := e.Stats().RealizedPnl

	e.ResetPool("n_shape")

	if len(e.Positions()) != 1 {
		t.Fatalf("清盘 n_shape 后应剩 1 仓, 实际 %d", len(e.Positions()))
	}
	if len(e.Positions()) == 1 && e.Positions()[0].Code != "000001.SZ" {
		t.Errorf("剩余持仓应为 dragon(000001.SZ), 实际 %+v", e.Positions())
	}
	// 清盘后池现金回补：n_shape 池现金应含平仓市值（11000）
	pool := e.PoolStats("n_shape")
	if pool.Cash != e.pools["n_shape"] {
		t.Errorf("n_shape 池现金应保留, 实际 %.2f", pool.Cash)
	}
	// 其他池不受影响：dragon 持仓仍在
	d := e.PoolStats("dragon")
	if d.OpenPositions != 1 {
		t.Errorf("dragon 池应仍持有 1 仓, 实际 %+v", d)
	}
	// 全局净值曲线与已实现盈亏保留（单池清盘不重置全局）
	if len(e.Equity()) != eqBefore {
		t.Errorf("全局净值应保留, 实际 %d", len(e.Equity()))
	}
	if e.Stats().RealizedPnl != realizedBefore {
		t.Errorf("全局已实现盈亏应保留, 实际 %.2f", e.Stats().RealizedPnl)
	}
	// n_shape 池表现归零
	ns := e.PoolStats("n_shape")
	if ns.FilledBuys != 0 || ns.OpenPositions != 0 {
		t.Errorf("n_shape 清盘后应无持仓/信号, 实际 %+v", ns)
	}
}

// TestAddToPoolPosition 验证手动加仓归原持仓池：从该池扣款并累计成本，卖出收益仍记该池。
// English: verifies a manual add-on credits the position's own pool — debits that pool and accrues its
// cost; sale proceeds still attribute to that pool.
func TestAddToPoolPosition(t *testing.T) {
	c := testCfg()
	e := New(c, "")
	e.SetStrategyPools([]string{"n_shape", "dragon"})
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 9.8, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10.0}})
	nsBefore := e.PoolStats("n_shape")
	otherBefore := e.PoolStats("")
	poolCashBefore := nsBefore.Cash
	costBefore := poolSnapshotCost(e, "n_shape")

	// 手动加仓（1000 股 @12）：应从 n_shape 池扣款（qty 为股数，调用方已换算）
	if err := e.BuyEx("600000.SH", "浦发", "N形", 0, 12, 1000, map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("加仓失败: %v", err)
	}

	ns := e.PoolStats("n_shape")
	if ns.Cash != poolCashBefore-12000 {
		t.Errorf("加仓应从 n_shape 池扣 12000, 现金 %.2f → %.2f", poolCashBefore, ns.Cash)
	}
	// 其他池现金不受影响
	if other := e.PoolStats(""); other.Cash != otherBefore.Cash {
		t.Errorf("加仓不应动其他池现金, %.2f → %.2f", otherBefore.Cash, other.Cash)
	}
	// 池累计成本增加 12000
	if got := poolSnapshotCost(e, "n_shape"); got != costBefore+12000 {
		t.Errorf("n_shape 累计成本应 +12000（%.2f → %.2f）", costBefore, got)
	}
	// 持仓合并：均价 = (10000+12000)/2000 = 11
	pos := e.Positions()
	if len(pos) != 1 || pos[0].Qty != 2000 {
		t.Fatalf("应合并为 2000 股, 实际 %+v", pos)
	}
	if got := pos[0].CostPrice; got != 11 {
		t.Errorf("加权均价应为 11, 实际 %.2f", got)
	}
}

// TestMomentumPoolRouting 验证 §动量入模拟盘：momentum buy 信号归动量池撮合，
// 池现金扣减、池持仓统计正确；未开 momentum 池时安全跳过（不留仓）。
// English: verifies momentum buy signals route to the momentum pool (cash debited, stats correct);
// without the pool the signal is safely skipped.
func TestMomentumPoolRouting(t *testing.T) {
	c := testCfg()
	e := New(c, "")
	e.SetStrategyPools([]string{"n_shape", "momentum"})
	now := time.Now()
	quotes := map[string]*data.StockInfo{"600000.SH": {Price: 10}}
	sig := combat_agent.Signal{
		Code: "600000.SH", Name: "浦发", Strategy: "动量", StrategyType: "momentum",
		Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now,
	}
	e.OnSignals([]combat_agent.Signal{sig}, quotes)
	ns := e.PoolStats("momentum")
	if ns.OpenPositions != 1 {
		t.Fatalf("动量buy应归动量池开仓, 实际 %d", ns.OpenPositions)
	}
	if e.PoolStats("n_shape").OpenPositions != 0 {
		t.Fatal("n_shape 池不应受动量信号影响")
	}

	// 未开 momentum 池 → 跳过
	e2 := New(c, "")
	e2.SetStrategyPools([]string{"n_shape"})
	e2.OnSignals([]combat_agent.Signal{sig}, quotes)
	if len(e2.Positions()) != 0 {
		t.Fatalf("未开动量池时不应建仓, 实际 %d", len(e2.Positions()))
	}
}

// TestTrimThenCloseCostNoDoubleCount §修复 FIX#3：减仓摊销持仓成本——
// 减仓后清仓的已实现盈亏不得把已卖股份成本重复扣除。
// English: §FIX#3 regression — trimming a position amortizes its cost, so a later full close settles
// against the remaining cost instead of the original total (realized P&L would otherwise be understated).
func TestTrimThenCloseCostNoDoubleCount(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"n_shape"})
	quotes := map[string]*data.StockInfo{"600000.SH": {Price: 10}}
	if err := e.BuyExInPool("600000.SH", "浦发", "N形", "n_shape", 10, 10, 100, quotes); err != nil {
		t.Fatalf("买入失败: %v", err)
	}
	p := e.positions["600000.SH"]
	if p == nil || p.Cost != 1000 || p.Qty != 100 {
		t.Fatalf("买入应 100股@10 成本1000, got %+v", p)
	}
	// 次日：减仓 50 股 @12 → 减仓已实现 +100（600-500），剩余成本摊销为 500
	t1Ready(e)
	if err := e.SellEx("600000.SH", 12, 50, map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("减仓失败: %v", err)
	}
	p = e.positions["600000.SH"]
	if p.Qty != 50 {
		t.Fatalf("减仓后应余 50 股, got %d", p.Qty)
	}
	if math.Abs(p.Cost-500) > 1e-9 {
		t.Fatalf("减仓后成本应摊销为 500, got %.2f", p.Cost)
	}
	if math.Abs(e.realized-100) > 1e-9 {
		t.Fatalf("减仓已实现盈亏应 +100, got %.2f", e.realized)
	}
	// 清仓剩余 50 股 @12 → 再 +100；总已实现 200（若成本未摊销，清仓会按 1000 再扣一次 → 0）
	if err := e.SellEx("600000.SH", 12, 100, map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("清仓失败: %v", err)
	}
	if len(e.positions) != 0 {
		t.Fatalf("清仓后应无持仓, got %d", len(e.positions))
	}
	if math.Abs(e.realized-200) > 1e-9 {
		t.Fatalf("减仓+清仓总已实现盈亏应 200, got %.2f", e.realized)
	}
	perf := e.poolPerf["n_shape"]
	if perf == nil || math.Abs(perf.Realized-200) > 1e-9 {
		t.Fatalf("池已实现盈亏应 200, got %+v", perf)
	}
	// 全局统计口径一致
	if st := e.Stats(); math.Abs(st.RealizedPnl-200) > 1e-9 {
		t.Fatalf("全局已实现盈亏应 200, got %.2f", st.RealizedPnl)
	}
}

// TestFeesCharged §R1/R2 费用入账：买入扣佣金(含最低5元)、持仓成本含费、
// 卖出扣佣金+印花税且滑点下浮、Trade.Fee 留痕。
// English: fee accounting end-to-end — commission debited on buys (min ¥5), cost is fee-inclusive,
// sells pay commission+stamp tax with slippage marked down, and Trade.Fee records it all.
func TestFeesCharged(t *testing.T) {
	c := DefaultConfig() // §R11 出厂默认即真实费率
	c.Enabled = true
	c.InitialCapital = 1000000
	e := New(c, "")
	e.SetStrategyPools([]string{"n_shape"})
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	p := e.positions["600000.SH"]
	if p == nil {
		t.Fatal("应建仓")
	}
	// FixedAmount=10000 @10元 → 1000股；滑点5bp后价 10.005，本金 10005；
	// 佣金 max(10005×0.00025,5)=5（最低档生效）→ 含费成本 10010
	if math.Abs(p.Cost-10010) > 0.01 {
		t.Errorf("持仓成本应为本金10005+最低佣金5=10010, 实际 %.2f", p.Cost)
	}
	lastBuy := e.trades[len(e.trades)-1]
	if math.Abs(lastBuy.Fee-5) > 1e-9 {
		t.Errorf("买单 Fee 应为 5, 实际 %.2f", lastBuy.Fee)
	}
	// 次日以 12 元清仓：滑点下浮后 11.994 → 毛得 11994；
	// 费用=佣金 max(11994×0.00025,5)=5 + 印花税 11994×0.0005≈6 → 净约 11983
	t1Ready(e)
	if err := e.Sell("600000.SH", map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("卖出失败: %v", err)
	}
	lastSell := e.trades[len(e.trades)-1]
	if want := 11.0; math.Abs(lastSell.Fee-want) > 0.01 {
		t.Errorf("卖单 Fee 应约为佣金5+印花税6=%.2f, 实际 %.2f", want, lastSell.Fee)
	}
	wantRealized := float64(11994-11) - 10010
	if math.Abs(e.realized-wantRealized) > 0.01 {
		t.Errorf("已实现盈亏应按净额口径 %.2f, 实际 %.2f", wantRealized, e.realized)
	}
}

// TestT1BlocksSameDaySell §R3 T+1：当日买入当日卖被拒绝，次日可卖。
// English: T+1 gate — same-day sell after buy is rejected; next-day sell goes through.
func TestT1BlocksSameDaySell(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"n_shape"})
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	err := e.Sell("600000.SH", map[string]*data.StockInfo{"600000.SH": {Price: 12}})
	if err == nil || !strings.Contains(err.Error(), "T+1") {
		t.Fatalf("当日买入当日卖应被 T+1 拦截, got %v", err)
	}
	if len(e.positions) != 1 {
		t.Fatal("被拦截后持仓应保留")
	}
	t1Ready(e)
	if err := e.Sell("600000.SH", map[string]*data.StockInfo{"600000.SH": {Price: 12}}); err != nil {
		t.Fatalf("次日卖出应放行, got %v", err)
	}
}

// TestMaxPositionsContinueProcessing §R4 回归：达持仓上限后，后续止损/清仓信号仍必须被执行。
// English: R4 regression — after the position cap is hit, later stop-loss/close signals must still run.
func TestMaxPositionsContinueProcessing(t *testing.T) {
	c := testCfg()
	c.MaxPositions = 1
	e := New(c, "")
	e.SetStrategyPools([]string{"n_shape", "dragon"})
	now := time.Now()
	q := map[string]*data.StockInfo{
		"600000.SH": {Price: 10},
		"000001.SZ": {Price: 20},
	}
	// 第一笔建仓占满上限；第二笔买入应被拒并留痕
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "A", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, q)
	e.OnSignals([]combat_agent.Signal{
		{Code: "000001.SZ", Name: "B", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 20, GeneratedAt: now},
	}, q)
	if _, held := e.positions["000001.SZ"]; held {
		t.Fatal("达上限后第二笔不应建仓")
	}
	found := false
	for _, o := range e.Orders() {
		if o.Code == "000001.SZ" && o.Status == "rejected" && strings.Contains(o.Reason, "持仓数达上限") {
			found = true
		}
	}
	if !found {
		t.Fatal("超限买单应有 rejected 审计留痕")
	}
	// 关键回归：随后的止损信号必须仍被执行（旧代码 return 直接吞掉）
	t1Ready(e)
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "A", Direction: "提醒", Action: "止损", AlertType: "止损"},
	}, q)
	if len(e.positions) != 0 {
		t.Fatal("达上限后止损信号应正常执行(旧实现被 return 吞掉)")
	}
}

// TestLimitUpByBoard §R6 分板块封板幅度：主板10%、创业科创20cm、ST 5%。
// English: board-aware sealed-board thresholds — main 10%, ChiNext/STAR 20%, ST 5%.
func TestLimitUpByBoard(t *testing.T) {
	if LimitUpPct("600000.SH", "浦发银行") != 9.9 {
		t.Fatal("主板应为 9.9")
	}
	if LimitUpPct("300750.SZ", "宁德时代") != 19.9 {
		t.Fatal("创业板应为 19.9")
	}
	if LimitUpPct("688160.SH", "") != 19.9 {
		t.Fatal("科创板应为 19.9")
	}
	if LimitUpPct("002084.SH", "*ST海工") != 4.9 {
		t.Fatal("ST 应为 4.9")
	}
	if LimitUpPct("920001.BJ", "北交所") != 29.9 {
		t.Fatal("北交所应为 29.9")
	}
	// 创业板涨 15% 未封板 → 可买；主板涨 10% 封板 → 拒
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"n_shape"})
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "300001.SZ", Name: "创板股", StrategyType: "n_shape", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"300001.SZ": {Price: 11.5, ChangePct: 15}})
	if len(e.positions) != 1 {
		t.Fatal("创业板涨15%(未及19.9)不应误拒")
	}
	e2 := New(testCfg(), "")
	e2.SetStrategyPools([]string{"dragon"})
	e2.OnSignals([]combat_agent.Signal{
		{Code: "600519.SH", Name: "茅台", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 1500, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600519.SH": {Price: 1650, ChangePct: 10.02}})
	if len(e2.positions) != 0 {
		t.Fatal("主板涨10.02%(封板)应拒买")
	}
}

// TestPoolMinScoreEnforced §R7 MinScore 生效：自动信号低于门槛被拒，手动买入不受门槛约束。
// English: MinScore is enforced for auto signals below the threshold; manual buys bypass the gate.
func TestPoolMinScoreEnforced(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"dragon"})
	rule := &PoolBuyRule{MinScore: 60}
	e.SetPoolBuyRule("dragon", rule)
	now := time.Now()
	sig := combat_agent.Signal{
		Code: "600000.SH", Name: "低分", Strategy: "龙头", StrategyType: "dragon",
		Direction: "做多", Action: "buy", Price: 10, Confidence: 0.5, GeneratedAt: now,
	}
	e.OnSignals([]combat_agent.Signal{sig}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	if len(e.positions) != 0 {
		t.Fatal("评分50<门槛60的自动信号不应建仓")
	}
	// 高分信号放行
	sig2 := sig
	sig2.Code = "600001.SH"
	sig2.Confidence = 0.7
	e.OnSignals([]combat_agent.Signal{sig2}, map[string]*data.StockInfo{"600001.SH": {Price: 10}})
	if len(e.positions) != 1 {
		t.Fatal("评分70≥门槛60应建仓")
	}
	// 手动买入不受门槛约束（confidence 语义上=用户自主决策）
	e3 := New(testCfg(), "")
	e3.SetStrategyPools([]string{"dragon"})
	e3.SetPoolBuyRule("dragon", &PoolBuyRule{MinScore: 99})
	if err := e3.BuyInPool("600000.SH", "手动", "龙头", "dragon", 10, map[string]*data.StockInfo{"600000.SH": {Price: 10}}); err != nil {
		t.Fatalf("手动买入不受 MinScore 约束, got %v", err)
	}
}

// TestAutoShrinkMinFeeNoNegativePool P2#20 回归：自动缩量须为最低佣金预留头寸，
// 无论缩量后买不买得起都不得让池现金变为负数（旧实现只按费率预留 pool/(1+rate)，
// MinCommission=5 让 cost+fee 超出 pool，池现金被穿成负）。
// English: P2#20 regression — the auto-shrink must reserve MinCommission headroom so the pool cash never
// goes negative (the old shrink reserved only rate-fee via pool/(1+rate); the 5元 minimum-commission
// floor pushed cost+fee past the pool balance).
func TestAutoShrinkMinFeeNoNegativePool(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.SlippageBps = 0
	cfg.CommissionRate = 0.00025
	cfg.MinCommission = 5
	cfg.StampTaxRate = 0
	cfg.InitialCapital = 2008 // dragon+"" 两池 均分 → dragon 池 1004（旧实现缩量后 1000+5=1005>1004 → -1）
	cfg.FixedAmount = 10000
	cfg.MaxPositions = 10

	e := New(cfg, "")
	e.SetStrategyPools([]string{"dragon"})
	dragonCash0 := e.PoolStats("dragon").Cash
	if dragonCash0 != 1004 {
		t.Fatalf("dragon 池应 1004, got %.2f", dragonCash0)
	}
	sig := combat_agent.Signal{
		Code: "600000.SH", Name: "小池票", Strategy: "龙头", StrategyType: "dragon",
		Direction: "做多", Action: "buy", Price: 10, Confidence: 0.6, GeneratedAt: time.Now(),
	}
	e.OnSignals([]combat_agent.Signal{sig}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	// 修复后收尾守卫拒绝（买不起一手含费），池现金分文未动、不穿负
	after := e.PoolStats("dragon").Cash
	if after < 0 {
		t.Fatalf("池现金穿负: %.2f", after)
	}
	if after != dragonCash0 {
		t.Fatalf("买不起一手含费应保持池现金 %.2f, got %.2f", dragonCash0, after)
	}
	// 对照组：池现金充足（可覆盖一手+最低佣金）时正常买入且池现金不为负
	cfgOK := cfg
	cfgOK.InitialCapital = 3000 // dragon 池 1500 ≥ 1000本金+5佣金
	e2 := New(cfgOK, "")
	e2.SetStrategyPools([]string{"dragon"})
	sig2 := sig
	sig2.Code = "600001.SH"
	e2.OnSignals([]combat_agent.Signal{sig2}, map[string]*data.StockInfo{"600001.SH": {Price: 10}})
	st2 := e2.PoolStats("dragon")
	if st2.Cash < 0 || st2.OpenPositions != 1 {
		t.Fatalf("可负担时应买入 1 笔且池现金非负: cash=%.2f open=%d", st2.Cash, st2.OpenPositions)
	}
}

// TestPoolMinScoreNotBypassedByFullConfidence P2#22 回归：真实信号置信度恰为 1.0 时
// 不得被当成"手动买入"绕过评分门槛（旧实现用 confidence<1 判别手动，1.0 的真信号被误放行）。
// English: P2#22 regression — a genuine signal whose confidence is exactly 1.0 must NOT be treated as a
// manual buy and skip the score gate (the old "confidence<1 ⇒ manual" proxy let a full-confidence
// signal slip past MinScore).
func TestPoolMinScoreNotBypassedByFullConfidence(t *testing.T) {
	e := New(testCfg(), "")
	e.SetStrategyPools([]string{"dragon"})
	// MinScore=110（百分制刻度，message语文案 `%.0f`）：1.0 信号×100=100 < 110 → 必须拒绝
	e.SetPoolBuyRule("dragon", &PoolBuyRule{MinScore: 110})
	sig := combat_agent.Signal{
		Code: "600000.SH", Name: "满分信号", Strategy: "龙头", StrategyType: "dragon",
		Direction: "做多", Action: "buy", Price: 10, Confidence: 1.0, GeneratedAt: time.Now(),
	}
	e.OnSignals([]combat_agent.Signal{sig}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
	if len(e.positions) != 0 {
		t.Fatalf("confidence=1.0 的真实信号仍应受 MinScore=110 拦截, got %d positions", len(e.positions))
	}
}
