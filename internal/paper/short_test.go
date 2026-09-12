package paper

// §SHORT-3 模拟盘融券做空侧测试：默认关闭、开仓保证金/整手、T+1 拦截、买回盈亏结算、
// 利息按日计提（幂等）、涨幅止损、两本账资金隔离、OnSignals 路由（watch 开空/sell 平多）、
// 同日去重、权益连续性与持久化往返。
// English: short-book tests — default-off, margin/lot sizing at open, T+1 cover block,
// realized P&L settlement, idempotent daily interest accrual, rally stop-loss, cash isolation
// between the two books, OnSignals routing (watch → open, sell → close long), same-day dedup,
// equity continuity and persistence round-trip.

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

func shortCfg() Config {
	return Config{
		Enabled: true, FixedAmount: 10000, InitialCapital: 100000, AutoSell: true,
		ShortEnabled: true, ShortCapital: 100000, ShortMarginRate: 0.5,
		ShortFeeAnnual: 0, ShortStopLossPct: 0, // 费用/止损单测显式开启
	}
}

func TestShortBookDisabledByDefault(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	if e.ShortBookEnabled() {
		t.Fatal("ShortCapital=0 时做空侧应关闭")
	}
	if _, err := e.ShortOpenManual("600000", "浦发", "放量破位", "break_down", 10, map[string]*data.StockInfo{"600000": {Price: 10}}); !errors.Is(err, errShortDisabled) {
		t.Fatalf("未开设做空池应拒绝开仓, got %v", err)
	}
}

func TestShortOpenMarginAndCashIsolation(t *testing.T) {
	e := New(shortCfg(), "")
	longCashBefore, poolsBefore := e.cash, e.pools[""]
	qty, err := e.ShortOpenManual("600000", "浦发", "放量破位", "break_down", 10, map[string]*data.StockInfo{"600000": {Price: 10}})
	if err != nil || qty != 1000 {
		t.Fatalf("开仓应成交 1000 股: qty=%d err=%v", qty, err)
	}
	s := e.shorts["600000"]
	if s == nil || s.Qty != 1000 || s.OpenPrice != 10 {
		t.Fatalf("空头持仓不符: %+v", s)
	}
	if s.MarginUsed != 5000 || e.shortCash != 95000 {
		t.Fatalf("保证金占用/池现金不符: margin=%v cash=%v", s.MarginUsed, e.shortCash)
	}
	if e.cash != longCashBefore || e.pools[""] != poolsBefore {
		t.Fatal("做空开仓不得触碰做多侧资金")
	}
	// 同票同日去重 + 已持空拒重开
	if _, err := e.ShortOpenManual("600000", "浦发", "放量破位", "break_down", 10, map[string]*data.StockInfo{"600000": {Price: 10}}); !errors.Is(err, errShortHeldDup) {
		t.Fatalf("已持空头应拒绝重复开仓, got %v", err)
	}
	// 行情缺失不伪造成交
	if _, err := e.ShortOpenManual("600519", "茅台", "高位滞涨", "high_churn", 1500, map[string]*data.StockInfo{}); !errors.Is(err, errNoQuote) {
		t.Fatalf("无行情不得开仓, got %v", err)
	}
}

func TestShortCoverT1AndPnl(t *testing.T) {
	e := New(shortCfg(), "")
	q := map[string]*data.StockInfo{"600000": {Price: 10}}
	if _, err := e.ShortOpenManual("600000", "浦发", "放量破位", "break_down", 10, q); err != nil {
		t.Fatal(err)
	}
	// 当日买回被 T+1 拦截
	if _, err := e.ShortCoverManual("600000", map[string]*data.StockInfo{"600000": {Price: 8}}); !errors.Is(err, errShortTPlusOne) {
		t.Fatalf("当日买回应被 T+1 拦截, got %v", err)
	}
	e.shorts["600000"].FilledAt = e.shorts["600000"].FilledAt.AddDate(0, 0, -1) // 模拟次日
	pnl, err := e.ShortCoverManual("600000", map[string]*data.StockInfo{"600000": {Price: 8}})
	if err != nil || pnl != 2000 {
		t.Fatalf("跌 2 元×1000 股应实现盈利 2000, got %v err=%v", pnl, err)
	}
	if e.shortCash != 102000 || e.shortRealized != 2000 || len(e.shorts) != 0 {
		t.Fatalf("平仓结算不符: cash=%v realized=%v shorts=%d", e.shortCash, e.shortRealized, len(e.shorts))
	}
	// 上涨亏钱：开 10 买回 11 → -1000
	if _, err := e.ShortOpenManual("600001", "测试B", "放量破位", "break_down", 10, map[string]*data.StockInfo{"600001": {Price: 10}}); err != nil {
		t.Fatal(err)
	}
	e.shorts["600001"].FilledAt = e.shorts["600001"].FilledAt.AddDate(0, 0, -1)
	pnl, err = e.ShortCoverManual("600001", map[string]*data.StockInfo{"600001": {Price: 11}})
	if err != nil || pnl != -1000 || e.shortCash != 101000 {
		t.Fatalf("上涨买回应亏 1000: pnl=%v cash=%v err=%v", pnl, e.shortCash, err)
	}
}

func TestShortInterestAccrualOncePerDay(t *testing.T) {
	cfg := shortCfg()
	cfg.ShortFeeAnnual = 0.083
	e := New(cfg, "")
	if _, err := e.ShortOpenManual("600000", "浦发", "放量破位", "break_down", 10, map[string]*data.StockInfo{"600000": {Price: 10}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.shortAccrueFeesLocked(now)
	f1 := e.shorts["600000"].FeeAccrued
	expect := 10 * 1000 * 0.083 / 365
	if f1 <= 0 || (f1-expect) > 1e-9 {
		t.Fatalf("日息应按 名义×年化/365 计提: got %v want %v", f1, expect)
	}
	e.shortAccrueFeesLocked(now) // 同日重复计提被去重
	if e.shorts["600000"].FeeAccrued != f1 {
		t.Fatal("同日利息应去重")
	}
	if e.shortCash != 95000 {
		t.Fatalf("利息应计负债不动现金（平仓时扣付）: cash=%v", e.shortCash)
	}
}

func TestShortStopLossOnRally(t *testing.T) {
	cfg := shortCfg()
	cfg.ShortStopLossPct = 8
	e := New(cfg, "")
	if _, err := e.ShortOpenManual("600000", "浦发", "放量破位", "break_down", 10, map[string]*data.StockInfo{"600000": {Price: 10}}); err != nil {
		t.Fatal(err)
	}
	// 当日：涨破阈值但 T+1 锁住 → 不平（次轮重试）
	e.shortMarkToMarketLocked(map[string]*data.StockInfo{"600000": {Price: 10.9}})
	if _, ok := e.shorts["600000"]; !ok {
		t.Fatal("当日空头不应被止损平掉（T+1）")
	}
	e.shorts["600000"].FilledAt = e.shorts["600000"].FilledAt.AddDate(0, 0, -1)
	e.shortMarkToMarketLocked(map[string]*data.StockInfo{"600000": {Price: 10.9}})
	if _, ok := e.shorts["600000"]; ok {
		t.Fatal("次日涨破 8% 应强制买回")
	}
	if d := e.shortRealized + 900; d > 1e-6 || d < -1e-6 {
		t.Fatalf("止损结算应亏 900(10→10.9): %v", e.shortRealized)
	}
	// 未破阈值不动作
	if _, err := e.ShortOpenManual("600001", "测试B", "放量破位", "break_down", 10, map[string]*data.StockInfo{"600001": {Price: 10}}); err != nil {
		t.Fatal(err)
	}
	e.shorts["600001"].FilledAt = e.shorts["600001"].FilledAt.AddDate(0, 0, -1)
	e.shortMarkToMarketLocked(map[string]*data.StockInfo{"600001": {Price: 10.7}})
	if _, ok := e.shorts["600001"]; !ok {
		t.Fatal("涨幅未达阈值不应触发止损")
	}
}

func TestShortOnSignalsRoutingAndEquity(t *testing.T) {
	e := New(shortCfg(), "")
	e.SetStrategyPools([]string{"dragon"})
	buy := combat_agent.Signal{Code: "300001", Name: "多", Strategy: "龙头", StrategyType: "dragon", Action: "buy", Price: 10}
	e.OnSignals([]combat_agent.Signal{buy}, map[string]*data.StockInfo{"300001": {Price: 10}})
	e.positions["300001"].FilledAt = e.positions["300001"].FilledAt.AddDate(0, 0, -1) // 平多受既有 T+1 守卫约束
	watch := combat_agent.Signal{Code: "300002", Name: "空A", Strategy: "放量破位", StrategyType: "break_down", Direction: "做空", Action: "watch", Price: 10}
	sellHeld := combat_agent.Signal{Code: "300001", Name: "多", Strategy: "龙头断板", StrategyType: "leader_decay", Direction: "做空", Action: "sell", Price: 10}
	equityBefore := e.cash + e.marketValueLocked() + e.shortEquityLocked()
	e.OnSignals([]combat_agent.Signal{watch, sellHeld}, map[string]*data.StockInfo{"300001": {Price: 10}, "300002": {Price: 10}})
	// watch 非持仓 → 开空；sell 持仓 → 平多且不双开空
	if _, ok := e.shorts["300002"]; !ok {
		t.Fatal("watch 做空战法信号应融券开仓")
	}
	if _, ok := e.positions["300001"]; ok {
		t.Fatal("持仓股做空 sell 信号应自动平多")
	}
	if _, ok := e.shorts["300001"]; ok {
		t.Fatal("持仓股不应多空双开")
	}
	// 做多侧资金与做空池完全隔离
	if e.cash != 90000+10000 || e.shortCash != 95000 {
		t.Fatalf("资金隔离被破坏: cash=%v shortCash=%v", e.cash, e.shortCash)
	}
	// 权益连续：开空+平多只损失两笔费用（此处费用 0），买卖滑点 0 → 完全相等
	equityAfter := e.cash + e.marketValueLocked() + e.shortEquityLocked()
	if equityBefore != equityAfter {
		t.Fatalf("权益应连续(零费率零滑点): before=%v after=%v", equityBefore, equityAfter)
	}
	// 做多信号回补空头
	bull2 := combat_agent.Signal{Code: "300002", Name: "空A", Strategy: "动量", StrategyType: "", Action: "buy", Price: 9.5}
	e.shorts["300002"].FilledAt = e.shorts["300002"].FilledAt.AddDate(0, 0, -1)
	e.OnSignals([]combat_agent.Signal{bull2}, map[string]*data.StockInfo{"300002": {Price: 9.5}})
	if _, ok := e.shorts["300002"]; ok {
		t.Fatal("做多信号应回补该票空头")
	}
	if e.shortRealized != 500 {
		t.Fatalf("10→9.5×1000 应实现 500, got %v", e.shortRealized)
	}
}

func TestShortBookPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper.json")
	e := New(shortCfg(), path)
	if _, err := e.ShortOpenManual("600000", "浦发", "放量破位", "break_down", 10, map[string]*data.StockInfo{"600000": {Price: 10}}); err != nil {
		t.Fatal(err)
	}
	e2 := New(shortCfg(), path)
	if len(e2.shorts) != 1 || e2.shorts["600000"].Qty != 1000 {
		t.Fatalf("空头持仓应随 paper.json 恢复: %+v", e2.shorts)
	}
	if e2.shortCash != 95000 {
		t.Fatalf("做空池现金应按磁盘余额恢复（防预算重播放大）: %v", e2.shortCash)
	}
	// 旧文件（无做空字段）零值兼容
	if e3 := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, path); len(e3.shorts) != 1 {
		t.Fatal("旧引擎配置也应能恢复空头（数据与配置无关）")
	}
}

func TestShortLimitDownGuard(t *testing.T) {
	e := New(shortCfg(), "")
	q := map[string]*data.StockInfo{"600000": {Price: 9, ChangePct: -10}}
	if _, err := e.ShortOpenManual("600000", "浦发", "放量破位", "break_down", 9, q); !errors.Is(err, errShortLimitDown) {
		t.Fatalf("跌停封板无法融券卖出, got %v", err)
	}
	if len(e.shorts) != 0 {
		t.Fatal("拒单不应建仓")
	}
}
