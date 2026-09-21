// gate_test.go — §WS-C 风控闸口单测：逐闸 on/off 分支 + 命中记录 + M8 收敛 + 告警。
// English: §WS-C risk-gate unit tests — per-gate on/off branches, hit recording, M8 delegation, alerts.
package risk

import (
	"path/filepath"
	"strings"
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

// §UAT-D4（2026-09-16）：T+1 可卖量必须扣减当日在途卖单——并发双卖不得各自按全量放行。
// 实录：两笔同秒卖单（合计超过 T+1 可卖量）先后通过旧守卫全部成交（校验输入相同、成交回报未回写）。
// 本用例锁死：先到者占额度，后到者在网关侧被拦；在途单结算后额度自然回补/继续受持仓约束。
// English: §UAT-D4 — T+1 sellable must deduct today's still-open sell tickets so concurrent sells
// cannot each pass on the full settled quantity.
func TestGateT1SellableCountsOpenSells(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	cfg := qmtCfg() // EnforceT1 nil → 默认开
	today := cntime.In(time.Now()).Format("2006-01-02")
	// 隔夜持仓 300 股（无当日买入成交）
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "600000.SH", Name: "浦发", Qty: 300, CostPrice: 10, UserID: "u_g"}}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	// 第一笔卖 200 已受理在途（已报，成交回报未回写）
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "OS1", SignalID: "SS1", Code: "600000.SH",
		Side: "卖出", Status: "已报", Price: 10, Qty: 200, CreatedAt: today + "T10:00:00+08:00", UserID: "u_g"}); err != nil {
		t.Fatalf("seed open sell: %v", err)
	}
	// 第二笔卖 150：可卖 = 300 − 0 − 200(在途) = 100 < 150 → 拦
	o := liveOrder(SideSell)
	o.Qty = 150
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("并发第二笔超在途剩余额度应被拦, got %+v", v)
	}
	// 剩余额度内的 100 → 放行
	o2 := liveOrder(SideSell)
	o2.Qty = 100
	if v := g.CheckLiveOrder(cfg, o2); !v.Pass {
		t.Fatalf("剩余额度 100 应放行, got %+v", v)
	}
	// 在途单结算（成交回报回写持仓+终态）后再试 150：可卖 = 100 − 0 − 0 = 100 → 仍拦
	if err := db.ApplyRealFill(store.RealFill{OrderID: "OS1", Code: "600000.SH", Side: "卖出",
		Price: 10, Qty: 200, Amount: 2000, TradedAt: today + "T10:00:05", SignalID: "SS1", UserID: "u_g"}); err != nil {
		t.Fatalf("apply fill: %v", err)
	}
	if _, err := db.AdvanceRealOrderStatus("u_g", "SS1", "已成"); err != nil {
		t.Fatalf("advance status: %v", err)
	}
	o3 := liveOrder(SideSell)
	o3.Qty = 150
	if v := g.CheckLiveOrder(cfg, o3); v.Pass {
		t.Fatalf("结算后持仓仅剩 100，卖 150 应被拦")
	}
}

// boolPtr 取 bool 地址（夹具可选字段）。
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

// TestGateBuyDiscipline 买入纪律迁入闸口：笔数按「已成交」计 + 预算超限拦截。
//
// §P0 2026-09-18 口径修正回归：笔数闸原先数 orders 表里「今日已报」的委托数——报单即占额度，
// 一笔被券商废掉或挂在委托簿上没成交的报单同样吃掉一天的买入额度（事故形态：daily_max_buys=5，
// 当日 5 笔报单实际 0 成交，闸口仍报「今日已报 5 笔」）。现在只有真实成交（fills）才占额度。
func TestGateBuyDiscipline(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	cfg := qmtCfg()
	cfg.DailyMaxBuys = 2
	// Gate 只判定（占位由 controller 落库）：无成交时连续放行——旧口径在第 3 笔就拦了。
	for i := 0; i < 3; i++ {
		if v := g.CheckLiveOrder(cfg, liveOrder(SideBuy)); !v.Pass {
			t.Fatalf("第 %d 笔（今日 0 成交）应放行, got %+v", i+1, v)
		}
	}
	// 落 2 笔真实成交 → 当日额度用尽。
	for i, code := range []string{"600001.SH", "600002.SH"} {
		if err := db.ApplyRealFill(store.RealFill{
			OrderID: "GW-" + code, Code: code, Side: SideBuy, Price: 10, Qty: 100,
			Amount: 1000, UserID: "u_g",
			TradedAt: cntime.Now().Format("2006-01-02 15:04:05"),
		}); err != nil {
			t.Fatalf("落成交 #%d: %v", i+1, err)
		}
	}
	v := g.CheckLiveOrder(cfg, liveOrder(SideBuy))
	if v.Pass || v.Gate != "buy_discipline" {
		t.Fatalf("已成交 2 笔应被 buy_discipline 拦截, got %+v", v)
	}
	if !strings.Contains(v.Reason, "今日已成交 2 笔") {
		t.Fatalf("拦截原因须说明「已成交」口径（避免再次混淆报单/成交）: %s", v.Reason)
	}
	// 卖出不受买入纪律限制
	if v := g.CheckLiveOrder(cfg, liveOrder(SideSell)); !v.Pass {
		t.Fatalf("卖出不受买入纪律限制, got %+v", v)
	}
}

// TestGateBuyDisciplineFailClosedOnReadError §C1（2026-09-22 修复批）反例锁：
// 冻结账读取 DB 错误绝不吞成 0 放行——三本账（已成交/在途冻结/卖出回款）任何一本读失败，
// checkBuyDiscipline 必须返回拒绝原因（fail-closed，宁可少买不放水超买）。
// 旧形态 LocalBuyFrozen 单值返回把错误压成 frozen=0，DB 故障期间预算闸整体失效。
// English: §C1 regression — any freeze-ledger read error must fail closed (reject the buy),
// never be swallowed into frozen=0 which silently disables the budget gates.
func TestGateBuyDisciplineFailClosedOnReadError(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	// 健康库 + 零配置：三本账读取成功，买入纪律不设防（放行）。
	if r := g.checkBuyDiscipline(qmtCfg(), liveOrder(SideBuy)); r != "" {
		t.Fatalf("健康库零配置应放行, got %q", r)
	}
	// 关库模拟存储故障：必须拒绝而非按 0 冻结放行。
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if r := g.checkBuyDiscipline(qmtCfg(), liveOrder(SideBuy)); r == "" {
		t.Fatal("三本账读取失败时买入纪律必须 fail-closed 拒绝（§C1 回归）")
	}
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

// TestGateMaxOrderAmount §AUDIT-PM 2026-09-15 单笔金额绝对帽：超限双向拒单+告警、
// Amount 缺省回退 qty×参考价、帽内放行、闸关放行。
// English: per-order amount cap — oversize rejected (both sides) with alert, Amount==0 falls back
// to qty×ref price, in-cap passes, gate off passes.
func TestGateMaxOrderAmount(t *testing.T) {
	alerts := 0
	g := NewGate(gateDB(t), "u_g", func(level, title, content string) { alerts++ })
	cfg := qmtCfg()
	cfg.RiskGate.MaxOrderAmount = 200000
	// 帽内放行
	if v := g.CheckLiveOrder(cfg, liveOrder(SideBuy)); !v.Pass {
		t.Fatalf("1000 元帽内应放行, got %+v", v)
	}
	// 超限买入拒绝
	o := liveOrder(SideBuy)
	o.Qty = 30000
	o.Amount = 300000
	v := g.CheckLiveOrder(cfg, o)
	if v.Pass || v.Gate != "max_order_amount" {
		t.Fatalf("30 万应拒单, got %+v", v)
	}
	// 超限卖出同样拒绝（绝对帽不分方向）
	so := liveOrder(SideSell)
	so.Qty = 30000
	so.Amount = 300000
	if v := g.CheckLiveOrder(cfg, so); v.Pass {
		t.Fatalf("卖出超限同样应拒单, got %+v", v)
	}
	// Amount 缺省 → 回退 qty×参考价
	noAmt := liveOrder(SideBuy)
	noAmt.Qty = 30000
	noAmt.Amount = 0 // 10×30000=300000 > 200000
	if v := g.CheckLiveOrder(cfg, noAmt); v.Pass {
		t.Fatalf("Amount 缺省应按 qty×价 回退判定, got %+v", v)
	}
	// 告警计数：本用例命中 3 次
	if alerts != 3 {
		t.Fatalf("超限应各触发高优告警, got %d", alerts)
	}
	// 闸关（0）放行（顺带关掉默认预算/资金闸，避免撞 buy_discipline 误读）
	cfg.RiskGate.MaxOrderAmount = 0
	cfg.DailyBudgetAmount = 0
	cfg.DailyMaxBuys = 0
	cfg.InitialCapital = 1000000
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("闸关闭应放行, got %+v", v)
	}
	// AnyEnabled 感知新闸
	if !(config.RiskGateConfig{MaxOrderAmount: 1}).AnyEnabled() {
		t.Fatal("AnyEnabled 应感知 max_order_amount")
	}
}
