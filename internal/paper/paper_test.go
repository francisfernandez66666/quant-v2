package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

func testCfg() Config {
	c := DefaultConfig()
	c.Enabled = true
	c.InitialCapital = 100000
	c.FixedAmount = 10000
	c.MaxPositions = 10
	return c
}

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
	// 无行情信号：回退信号价成交
	e.OnSignals([]combat_agent.Signal{
		{Code: "000001.SZ", Name: "平安", Strategy: "N形", Direction: "做多", Action: "buy", Price: 5, GeneratedAt: now},
	}, map[string]*data.StockInfo{})
	if len(e.Positions()) != 2 {
		t.Fatalf("无行情应回退信号价成交, 实际 %d 仓", len(e.Positions()))
	}
}

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

func TestSellAndReset(t *testing.T) {
	e := New(testCfg(), "")
	now := time.Now()
	e.OnSignals([]combat_agent.Signal{
		{Code: "600000.SH", Name: "浦发", Strategy: "N形", Direction: "做多", Action: "buy", Price: 10, GeneratedAt: now},
	}, map[string]*data.StockInfo{"600000.SH": {Price: 10}})
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
