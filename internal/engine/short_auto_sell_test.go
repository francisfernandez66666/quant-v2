package engine

// §SHORT-2 做空战法自动卖出接线测试：标记当日有效/隔日失效、标记→清仓建议转换
// （跳过已有止损/无行情）、Source=short_tactic 走实盘自动卖出全仓。
// English: wiring tests for bear-tactic auto-sell — mark freshness by trading day, mark→advice
// conversion (skip existing 止损 / missing quotes), and short_tactic advices executing a full
// live sell through the existing auto-sell path.

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

func TestShortSellMarkDayScope(t *testing.T) {
	e := &Engine{}
	fixed := time.Date(2026, 9, 11, 10, 0, 0, 0, time.Local)
	e.SetClock(func() time.Time { return fixed })
	e.markShortSell("600000.SH", "放量破位")
	if m, ok := e.shortSellMarkOf("600000"); !ok || m.Tactic != "放量破位" {
		t.Fatalf("当日标记应有效, got %+v ok=%v", m, ok)
	}
	// 时钟拨到下周一（真实下一交易日；TradingDayDate 周末会回滚到周五，不能用 +24h）→ 隔日失效
	e.SetClock(func() time.Time { return fixed.Add(72 * time.Hour) })
	if _, ok := e.shortSellMarkOf("600000"); ok {
		t.Fatal("跨交易日标记应失效")
	}
}

func TestShortTacticCloseAdvices(t *testing.T) {
	fixed := time.Date(2026, 9, 11, 10, 0, 0, 0, time.Local)
	mk := func() *Engine {
		e := &Engine{}
		e.SetClock(func() time.Time { return fixed })
		return e
	}
	positions := []store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10},
		{TsCode: "600519.SH", Name: "茅台", Qty: 100, CostPrice: 1500},
		{TsCode: "000001.SZ", Name: "平安", Qty: 300, CostPrice: 12},
		{TsCode: "000002.SZ", Name: "万科", Qty: 0, CostPrice: 8},
	}
	quotes := map[string]*data.StockInfo{
		"600000": {Code: "600000", Price: 9.5},
		"600519": {Code: "600519", Price: 1400},
		// 000001 无行情 → 跳过
	}
	existing := []trading.PositionAdvice{{Code: "600519", Action: "止损", RefPrice: 1400}}
	e := mk()
	e.markShortSell("600000.SH", "放量破位")
	e.markShortSell("600519.SH", "高位滞涨")
	e.markShortSell("000001.SZ", "龙头断板")
	e.markShortSell("000002.SZ", "利好兑现砸盘")
	out := e.shortTacticCloseAdvices(positions, existing, quotes)
	if len(out) != 1 {
		t.Fatalf("只应生成 1 条清仓建议（其余命中已有止损/无行情/零仓）, got %d: %+v", len(out), out)
	}
	a := out[0]
	if a.Code != "600000" || a.Action != "止损" || a.Source != "short_tactic" || a.Strategy != "放量破位" {
		t.Fatalf("建议字段不符: %+v", a)
	}
	// 成本 10 → 现价 9.5：盈亏比应为 -5（断言落在 -5 邻域，证明按实时行情计算）
	if a.RefPrice != 9.5 || a.ProfitPct < -5.1 || a.ProfitPct > -4.9 {
		t.Fatalf("参考价/盈亏比 应来自实时行情: %+v", a)
	}
}

func TestAutoExecuteRealSellsShortTactic(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = true })
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000},
	}); err != nil {
		t.Fatal(err)
	}
	advices := []trading.PositionAdvice{{
		Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高",
		RefPrice: 9.0, Source: "short_tactic", Strategy: "放量破位",
	}}
	e.autoExecuteRealSells(e.UserID(), e.QMTController(), db, advices)
	if len(*orders) != 1 {
		t.Fatalf("short_tactic 止损建议应自动全仓卖出, got %d", len(*orders))
	}
	o := (*orders)[0]
	if o["side"] != "卖出" || o["qty"].(float64) != 500 {
		t.Fatalf("应全仓卖出 500 股: %+v", o)
	}
	// 二次触发（如战法每轮重复出信号）→ 幂等键防重，不二次下单
	e.autoExecuteRealSells(e.UserID(), e.QMTController(), db, advices)
	if len(*orders) != 1 {
		t.Fatalf("同日重复标记应幂等防重, got %d", len(*orders))
	}
}

// TestPaperSellSignalsIncludeShortTactic 验证 SellAction 归一口径（与 combat_agent 侧一致）：
// 做空战法 sell=close 使 13e 纸面/report 通道自动平多。
func TestPaperSellSignalsIncludeShortTactic(t *testing.T) {
	sell := combat_agent.Signal{Code: "600000", Direction: "做空", StrategyType: "break_down", Action: "sell"}
	if combat_agent.SellAction(sell) != "close" {
		t.Fatalf("做空战法 sell 应归一 close")
	}
	watch := combat_agent.Signal{Code: "600000", Direction: "做空", StrategyType: "break_down", Action: "watch"}
	if combat_agent.SellAction(watch) != "" {
		t.Fatalf("watch 不应触发动作")
	}
	legacy := combat_agent.Signal{Code: "600000", Direction: "做空", StrategyType: "龙头", Action: "sell"}
	if combat_agent.SellAction(legacy) != "" {
		t.Fatalf("非做空战法的做空方向信号应维持开仓语义（返回空）")
	}
}
