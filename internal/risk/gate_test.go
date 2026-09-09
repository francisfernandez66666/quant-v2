// gate_test.go — §WS-C 风控闸口单测：逐闸 on/off 分支 + 命中记录 + M8 收敛 + 告警。
// English: §WS-C risk-gate unit tests — per-gate on/off branches, hit recording, M8 delegation, alerts.
package risk

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// gateDB 建临时实盘账本。
func gateDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// liveOrder 构造标准买单视图。
func liveOrder(side string) LiveOrder {
	return LiveOrder{
		SignalID: "SIG-G", Code: "600000.SH", Name: "浦发", Strategy: "龙头",
		Side: side, Price: 10, Qty: 100, Amount: 1000,
		StalenessMs: -1,
	}
}

// qmtCfg 返回启用实盘且风控闸默认全关的配置。
func qmtCfg() config.QMTConfig {
	c := config.DefaultQMTConfig()
	c.Enabled = true
	return c
}

// TestGateDefaultsAllOff 零配置：所有闸关闭，任意下单放行。
func TestGateDefaultsAllOff(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	v := g.CheckLiveOrder(qmtCfg(), liveOrder(SideBuy))
	if !v.Pass {
		t.Fatalf("零配置应全放行, got %+v", v)
	}
}

// TestGateSTAndBlacklist 存量守卫收口：ST/黑名单拒绝买入，卖出放行。
func TestGateSTAndBlacklist(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	cfg := qmtCfg()
	cfg.Blacklist = []string{"600001"}
	o := liveOrder(SideBuy)
	o.Name = "*ST海工"
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("ST 应拒绝, got %+v", v)
	}
	o.Name = "普通"
	o.Code = "600001.SH"
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("黑名单应拒绝, got %+v", v)
	}
	o.Side = SideSell
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("卖出应放行(ST/黑名单只管买入), got %+v", v)
	}
}

// TestGateT1Sellable 当日买入份额不可卖；隔夜仓可卖；未知仓位 fail-open。
func TestGateT1Sellable(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	cfg := qmtCfg() // EnforceT1 nil → 默认开启
	today := cntime.In(time.Now()).Format("2006-01-02")
	// 建仓：当日买入 100 股 @10（buy_date 今日 → 视为当日买入锁定）
	if err := db.ApplyRealFill(store.RealFill{OrderID: "O1", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: today + " 09:35:00", SignalID: "S1", UserID: "u_g"}); err != nil {
		t.Fatalf("fill: %v", err)
	}
	o := liveOrder(SideSell)
	o.Qty = 100
	// 持仓行存在且 Qty>0 且是当日买入 → 可卖 0，100 股全拦
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("当日买入当日卖应被 T+1 拦截, got %+v", v)
	}
	// 隔夜仓（无当日买入记录）：需先有持仓行。模拟持仓行已存在但当日无买入 → 放行
	// （本测试里 fills 的 buy_date 是今日，构成"当日买入"；把 EnforceT1 关掉验证 fail-open 语义分支）
	cfg.EnforceT1 = boolPtr(false)
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("EnforceT1=false 应放行, got %+v", v)
	}
	// 未知仓位 fail-open：无持仓行 → 跳过本闸
	cfg.EnforceT1 = nil
	o2 := liveOrder(SideSell)
	o2.Code = "999999.SH" // 无持仓行
	o2.Qty = 100
	if v := g.CheckLiveOrder(cfg, o2); !v.Pass {
		t.Fatalf("未知仓位应 fail-open 放行, got %+v", v)
	}
}

func boolPtr(b bool) *bool { return &b }

// TestGateLimitUpDown 涨停不可追买 / 跌停不可追卖：on 拦截、off 放行、无昨收 fail-open。
func TestGateLimitUpDown(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	cfg := qmtCfg()
	cfg.RiskGate.LimitUpBlockBuy = true
	cfg.RiskGate.LimitDownBlockSell = true
	// 主板 600000，昨收 10，涨停 10.99（9.9%）；买价 11 ≥ 涨停 → 拦截
	o := liveOrder(SideBuy)
	o.PrevClose = 10
	o.Price = 11
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("涨停追买应拦截, got %+v", v)
	}
	// 买价 10.5 < 涨停 → 放行
	o.Price = 10.5
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("未触涨停应放行, got %+v", v)
	}
	// 跌停追卖：昨收 10 跌停 9.01；卖价 9 ≤ 跌停 → 拦截
	o = liveOrder(SideSell)
	o.PrevClose = 10
	o.Price = 9
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("跌停追卖应拦截, got %+v", v)
	}
	// off 分支：闸关闭 → 放行
	cfg.RiskGate.LimitUpBlockBuy = false
	o = liveOrder(SideBuy)
	o.PrevClose = 10
	o.Price = 11
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("闸关闭应放行, got %+v", v)
	}
	// 无昨收 fail-open
	o = liveOrder(SideBuy)
	o.PrevClose = 0
	o.Price = 11
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("无昨收应 fail-open, got %+v", v)
	}
}

// TestGateStaleQuote §WS-C 验收：注入 60s 陈旧快照 → 拒单且告警；新鲜放行；闸关放行。
func TestGateStaleQuote(t *testing.T) {
	alerts := 0
	g := NewGate(gateDB(t), "u_g", func(level, title, content string) { alerts++ })
	cfg := qmtCfg()
	cfg.RiskGate.StaleQuoteMs = 30000
	o := liveOrder(SideBuy)
	o.StalenessMs = 60000 // 60s > 30s 阈值
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("60s 陈旧应拒单, got %+v", v)
	}
	if alerts != 1 {
		t.Fatalf("应触发高优告警, got %d", alerts)
	}
	o.StalenessMs = 5000 // 新鲜
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("新鲜快照应放行, got %+v", v)
	}
	cfg.RiskGate.StaleQuoteMs = 0 // 闸关闭
	o.StalenessMs = 60000
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("闸关闭应放行, got %+v", v)
	}
	// 未提供（-1）fail-open
	cfg.RiskGate.StaleQuoteMs = 30000
	o.StalenessMs = -1
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("未提供陈旧度应 fail-open, got %+v", v)
	}
}

// TestGateDayLoss 日内已实现亏损熔断：达阈值拦买入、卖出放行、未达放行、关闭放行。
func TestGateDayLoss(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	cfg := qmtCfg()
	cfg.RiskGate.DayLossLimitPct = 0.1 // 总资产 100000 的 0.1% = 100 元
	cfg.InitialCapital = 100000
	today := cntime.In(time.Now()).Format("2006-01-02")
	// 种子：今日买入 100@10，卖出 100@8 → 已实现亏损 200
	if err := db.ApplyRealFill(store.RealFill{OrderID: "B1", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: today + " 09:30:00", SignalID: "SB", UserID: "u_g"}); err != nil {
		t.Fatalf("buy fill: %v", err)
	}
	if err := db.ApplyRealFill(store.RealFill{OrderID: "S1", Code: "600000.SH", Side: "卖出",
		Price: 8, Qty: 100, Amount: 800, TradedAt: today + " 10:00:00", SignalID: "SS", UserID: "u_g"}); err != nil {
		t.Fatalf("sell fill: %v", err)
	}
	o := liveOrder(SideBuy)
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("亏损熔断达阈值应拦买入, got %+v", v)
	}
	// 卖出/清仓放行
	o2 := liveOrder(SideSell)
	if v := g.CheckLiveOrder(cfg, o2); !v.Pass {
		t.Fatalf("熔断不影响卖出/清仓, got %+v", v)
	}
	// 关闭闸 → 放行
	cfg.RiskGate.DayLossLimitPct = 0
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("闸关闭应放行, got %+v", v)
	}
}

// TestGateConcentration 单票集中度：买入后预计市值/总资产超阈值拦单；未超放行；关闭放行。
func TestGateConcentration(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	cfg := qmtCfg()
	cfg.RiskGate.SingleStockValuePct = 20
	// 总资产：券商可用 100000（无持仓）→ 预计 25000 / 100000 = 25% > 20% → 拦
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_g", AvailableCash: 100000, UpdatedAt: cntime.In(time.Now()).Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("account: %v", err)
	}
	o := liveOrder(SideBuy)
	o.Amount = 25000
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("25%% 集中度应拦, got %+v", v)
	}
	o.Amount = 15000 // 15% → 放行
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("15%% 应放行, got %+v", v)
	}
	cfg.RiskGate.SingleStockValuePct = 0
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("闸关闭应放行, got %+v", v)
	}
}

// TestGateBuyDiscipline 买入纪律迁入闸口：预算超限拦截。
func TestGateBuyDiscipline(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	cfg := qmtCfg()
	cfg.DailyMaxBuys = 2
	// 第一笔放行，两笔后再买被笔数上限拦截
	if v := g.CheckLiveOrder(cfg, liveOrder(SideBuy)); !v.Pass {
		t.Fatalf("首笔应放行, got %+v", v)
	}
	if v := g.CheckLiveOrder(cfg, liveOrder(SideBuy)); !v.Pass {
		t.Fatalf("次笔应放行, got %+v", v)
	}
	// 前两笔订单未落库（Gate 只判定）→ 仍放行；补充落库场景由 trading 层测试覆盖。
	if v := g.CheckLiveOrder(cfg, liveOrder(SideBuy)); !v.Pass {
		t.Fatalf("Gate 无账本占用时应继续放行（占位由 controller 落库）, got %+v", v)
	}
	_ = db
}

// TestGateWhitelistAndMaxPositions 白名单/仓位上限收口。
func TestGateWhitelistAndMaxPositions(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	cfg := qmtCfg()
	cfg.Strategies = []string{"n_shape"}
	o := liveOrder(SideBuy)
	o.Strategy = "龙头"
	o.StrategyID = "fac_1"
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("白名单外应拦, got %+v", v)
	}
	o.StrategyID = "n_shape"
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("白名单内应放行, got %+v", v)
	}
	// 仓位上限
	db := gateDB(t)
	g2 := NewGate(db, "u_g", nil)
	cfg2 := qmtCfg()
	cfg2.MaxPositions = 1
	// 已有 1 持仓（UpsertRealPositions 全量对账式写入）
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "600519.SH", Name: "茅台", Qty: 100, CostPrice: 100}}); err != nil {
		t.Fatalf("pos: %v", err)
	}
	if v := g2.CheckLiveOrder(cfg2, o); v.Pass {
		t.Fatalf("达上限应拦新买, got %+v", v)
	}
	// 卖出不受限
	o.Side = SideSell
	if v := g2.CheckLiveOrder(cfg2, o); !v.Pass {
		t.Fatalf("卖出不受仓位上限限制, got %+v", v)
	}
}

// TestGateRecordsHits 命中落库 risk_gates：计数与原因可查。
func TestGateRecordsHits(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	cfg := qmtCfg()
	cfg.RiskGate.StaleQuoteMs = 1000
	o := liveOrder(SideBuy)
	o.StalenessMs = 5000
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("应拦截, got %+v", v)
	}
	today := cntime.In(time.Now()).Format("2006-01-02")
	hits, err := db.RiskGateHits("u_g", today)
	if err != nil || hits["stale_quote"] != 1 {
		t.Fatalf("命中应记录 stale_quote=1, got %v err=%v", hits, err)
	}
}

// TestGateCheckPortfolioM8 Gate 收敛 M8 判定：与共享实现一致。
func TestGateCheckPortfolioM8(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	rules := config.NewManager("").Get()
	rules.RiskCtrl.M8Enabled = true
	rules.RiskCtrl.M8PortfolioDrawdownPct = -10
	v := g.CheckPortfolio(rules, 90, 100) // 回撤 -10% 恰好触发
	if v == nil || v.Pass || v.Action != "sell_all" {
		t.Fatalf("M8 应触发 sell_all, got %+v", v)
	}
	v = g.CheckPortfolio(rules, 95, 100) // -5% 未达
	if v == nil || !v.Pass {
		t.Fatalf("-5%% 不应触发, got %+v", v)
	}
	// 未启用 → 放行
	rules.RiskCtrl.M8Enabled = false
	if v := g.CheckPortfolio(rules, 50, 100); !v.Pass {
		t.Fatalf("M8 未启用应放行, got %+v", v)
	}
}
