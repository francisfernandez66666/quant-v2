// gate_test.go — §WS-C 风控闸口单测：逐闸 on/off 分支 + 命中记录 + M8 收敛 + 告警。
// English: §WS-C risk-gate unit tests — per-gate on/off branches, hit recording, M8 delegation, alerts.
package risk

import (
	"errors"
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

// TestGateDefaultsAllOff 零配置：除 §A5 常开的跌停追卖闸外其余闸关闭；买单与"无昨收卖单"放行
// （常开闸对缺数据的单 fail-open，不改变本用例的放行结论，详见 TestGateLimitDownAlwaysOn）。
func TestGateDefaultsAllOff(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	v := g.CheckLiveOrder(qmtCfg(), liveOrder(SideBuy))
	if !v.Pass {
		t.Fatalf("零配置应全放行, got %+v", v)
	}
	// 零配置卖单（夹具 PrevClose=0 未知昨收）：常开闸 fail-open，仍放行
	if v := g.CheckLiveOrder(qmtCfg(), liveOrder(SideSell)); !v.Pass {
		t.Fatalf("零配置无昨收卖单应 fail-open 放行, got %+v", v)
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
	cfg.RiskGate.LimitDownBlockSell = boolPtr(true)
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

// TestGateLimitDownAlwaysOn §A5-常开（owner 裁决 2026-09-26「跌停追卖闸是否常开：是」）：
// 零配置（未写 limit_down_block_sell）时本闸必须直接生效——旧语义"默认关、等谁去开"不再成立。
// 三条断言：① 零配置跌停追卖被拒；② 显式 false 可关（回滚通道保留）；③ 无昨收仍 fail-open
// （数据缺口不误拦，与开关形态无关）。
// English: §A5-always-on — with the key unset the limit-down sell block must bite; explicit false
// still opts out; unknown prevClose still fails open.
func TestGateLimitDownAlwaysOn(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	cfg := qmtCfg() // 零配置：LimitDownBlockSell == nil → 常开
	// ① 主板 600000 昨收 10，跌停 9.00；卖价 9 ≤ 跌停 → 未配置任何开关也应拦截
	o := liveOrder(SideSell)
	o.PrevClose = 10
	o.Price = 9
	v := g.CheckLiveOrder(cfg, o)
	if v.Pass {
		t.Fatalf("§A5 零配置下跌停追卖必须被拒（常开），got %+v", v)
	}
	if !strings.Contains(v.Reason, "跌停") {
		t.Fatalf("拒单原因须落在跌停闸本体（而非别的闸顺带命中）, got %q", v.Reason)
	}
	// ② 显式 false 关闭：同场景应放行（回滚通道）
	cfg.RiskGate.LimitDownBlockSell = boolPtr(false)
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("显式关闭后应放行, got %+v", v)
	}
	// ③ 恢复常开 + 无昨收：fail-open 语义不受开关形态影响
	cfg.RiskGate.LimitDownBlockSell = nil
	o2 := liveOrder(SideSell)
	o2.Price = 9 // PrevClose=0（未知）
	if v := g.CheckLiveOrder(cfg, o2); !v.Pass {
		t.Fatalf("无昨收应 fail-open 放行, got %+v", v)
	}
	// ④ AnyEnabled 短路位：零配置也应报"至少一道闸开"（常开闸的诚实展示）
	if !(config.RiskGateConfig{}).AnyEnabled() {
		t.Fatal("§A5 常开后零配置 AnyEnabled 应为 true（展示生效值）")
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
	// §XCHECK 2026-09-22 C批：AnyEnabled 同样须感知价格复核闸——只开 cross_check_pct
	// 也算"有闸在管"，否则 UI 闸口卡片/健康展示会在唯一启用的正是本闸时谎报全关。
	if !(config.RiskGateConfig{CrossCheckPct: 3}).AnyEnabled() {
		t.Fatal("AnyEnabled 应感知 cross_check_pct")
	}
}

// TestGatePriceCrossCheck §XCHECK 2026-09-22 C批 价格复核闸四态表驱动：
// 关闭跳过 / 复核源出错 fail-open / 影子命中→放行但 risk_gates 留 [shadow] 痕 / 正式命中→拒单。
// English: §XCHECK cross-check gate, table-driven over four states — disabled, fail-open on
// source error, shadow hit (pass + [shadow] record), enforce hit (reject + high alert).
func TestGatePriceCrossCheck(t *testing.T) {
	// 参考价 10 元 vs 复核价 12 元 → 偏差 |10−12|/12×100 = 16.67% > 3%（必命中）；
	// vs 复核价 10.1 元 → 偏差 ~0.99% ≤ 3%（必不命中）。
	cases := []struct {
		name      string
		pct       float64
		shadow    *bool
		cross     float64
		srcErr    bool
		wantPass  bool
		wantTrace bool // risk_gates 应出现 price_cross_check 行（影子/正式均留痕，原因前缀不同）
	}{
		{"关闭跳过", 0, nil, 999, false, true, false},                  // pct=0：源给什么价都不拦
		{"源出错fail-open", 3, boolPtr(false), 0, true, true, false},  // 数据缺口不误拦（即便已切正式）
		{"源无价fail-open", 3, boolPtr(false), 0, false, true, false}, // 复核价 ≤0 同样跳过
		{"偏差阈内放行", 3, boolPtr(false), 10.1, false, true, false},    // 0.99% ≤ 3%
		{"影子命中放行留痕", 3, nil, 12, false, true, true},                // 默认影子：放行 + [shadow] 留痕
		{"正式命中拒单", 3, boolPtr(false), 12, false, false, true},      // shadow=false：拒单
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := gateDB(t)
			alerts := 0
			g := NewGate(db, "u_g", func(level, title, content string) { alerts++ })
			// 注入独立复核源（stub：固定返回用例价或错误）。
			g.SetCrossPriceSource(func(code string) (float64, error) {
				if c.srcErr {
					return 0, errors.New("quote unavailable")
				}
				return c.cross, nil
			})
			cfg := qmtCfg()
			cfg.RiskGate.CrossCheckPct = c.pct
			cfg.RiskGate.CrossCheckShadow = c.shadow
			v := g.CheckLiveOrder(cfg, liveOrder(SideBuy))
			if v.Pass != c.wantPass {
				t.Fatalf("Pass=%v, want %v (verdict %+v)", v.Pass, c.wantPass, v)
			}
			if !c.wantPass {
				if v.Gate != "price_cross_check" {
					t.Fatalf("应命中 price_cross_check 闸, got %q", v.Gate)
				}
				// 拒单文案必须含代码、两价与偏差/阈值，供审计直读。
				for _, frag := range []string{"600000.SH", "10.000", "12.000", "16.67", "3.00"} {
					if !strings.Contains(v.Reason, frag) {
						t.Fatalf("拒单原因缺少 %q: %s", frag, v.Reason)
					}
				}
				if alerts != 1 {
					t.Fatalf("正式命中应触发 1 次高优告警, got %d", alerts)
				}
			} else {
				if alerts != 0 {
					t.Fatalf("放行路径不得告警, got %d", alerts)
				}
			}
			// risk_gates 留痕核验：影子命中=放行但记 [shadow] 行；正式命中=verdict 统一落库。
			today := cntime.In(time.Now()).Format("2006-01-02")
			hits, err := db.RiskGateHits("u_g", today)
			if err != nil {
				t.Fatalf("RiskGateHits: %v", err)
			}
			if !c.wantTrace {
				if hits["price_cross_check"] != 0 {
					t.Fatalf("本态不应留痕, got %v", hits)
				}
				return
			}
			if hits["price_cross_check"] != 1 {
				t.Fatalf("应有 price_cross_check 留痕 1 次, got %v", hits)
			}
			rows, err := db.RiskGateDay(today, 10)
			if err != nil || len(rows) == 0 {
				t.Fatalf("RiskGateDay: %v rows=%d", err, len(rows))
			}
			var reason string
			for _, r := range rows {
				if r.Gate == "price_cross_check" {
					reason = r.LastReason
				}
			}
			if reason == "" {
				t.Fatal("未找到 price_cross_check 留痕行")
			}
			if c.wantPass { // 影子：原因必须带 [shadow] 前缀（与正式拒单留痕可区分）
				if !strings.HasPrefix(reason, "[shadow] ") {
					t.Fatalf("影子留痕应带 [shadow] 前缀, got %q", reason)
				}
			} else if strings.HasPrefix(reason, "[shadow]") {
				t.Fatalf("正式拒单留痕不应带 [shadow] 前缀, got %q", reason)
			}
		})
	}
}

// TestGateT1SellableUsesNetOpenSellQty §N-3（2026-09-22 傍晚批复验）联动断言：T+1 可卖量里的
// 在途项改为「未成交余量」后，本闸的三项扣减必须逐项读通——
//   - p.Qty 已由 ApplyRealFill 即时扣掉该单已成交的部分；
//   - bought 是当日买入（T+1 锁定）；
//   - openSell 只剩该单**未成交**的余量。
//
// 旧整笔口径等于把同一笔成交扣两次（p.Qty 已减 + openSell 仍含）→ 可卖量虚低，把合法的
// 手动/补卖退出一起拦死。本用例同时锁"不得反向变松"：净额之外超量的请求照样要拦。
// English: §N-3 — the T+1 gate's open-sell term is now the unfilled remainder (the settled part is
// already gone from p.Qty), so legitimate exits are no longer locked out; over-quantity requests
// are still rejected.
func TestGateT1SellableUsesNetOpenSellQty(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_g", nil)
	cfg := qmtCfg() // EnforceT1 默认开
	today := cntime.In(time.Now()).Format("2006-01-02")
	// 隔夜持仓 2000 股（无当日买入成交）
	if _, err := db.UpsertRealPositions([]store.RealPosition{{TsCode: "600000.SH", Name: "浦发",
		Qty: 2000, CostPrice: 10, Amount: 20000, UserID: "u_g"}}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	// 当日卖出委托 1000 股，已成交 500（ApplyRealFill 即时把持仓降到 1500）
	sid := "sell:600000:止损:" + today
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "GW-N3", SignalID: sid, Code: "600000.SH",
		Side: "卖出", Status: "部成", Price: 11, Qty: 1000, CreatedAt: today + "T09:35:00+08:00", UserID: "u_g"}); err != nil {
		t.Fatalf("seed open sell: %v", err)
	}
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-N3", Code: "600000.SH", Side: "卖出",
		Price: 11, Qty: 500, Amount: 5500, TradedAt: today + "T09:40:00", SignalID: sid, UserID: "u_g"}); err != nil {
		t.Fatalf("apply partial fill: %v", err)
	}
	// 台账前提：持仓 1500、在途净额 500 → 可卖 1000
	if p, err := db.RealPositionByCodeForUser("u_g", "600000.SH"); err != nil || p.Qty != 1500 {
		t.Fatalf("持仓应已被成交扣到 1500: %+v err=%v", p, err)
	}
	if got := db.SumOpenSellQty("u_g", "600000.SH", today); got != 500 {
		t.Fatalf("在途卖量应为未成交余量 500, got %d", got)
	}
	// 卖 800（≤ 净额口径可卖 1000，> 旧口径可卖 500）→ 必须放行（旧口径在此拦死合法退出）
	o := liveOrder(SideSell)
	o.Qty = 800
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("§N-3 净额口径下卖 800 应放行（旧整笔口径误拦），got %+v", v)
	}
	// 反向锁：超过净额可卖量的请求照样拦（本闸不得因净额口径变松）
	o2 := liveOrder(SideSell)
	o2.Qty = 1001
	if v := g.CheckLiveOrder(cfg, o2); v.Pass {
		t.Fatalf("超净额可卖量(1000)的 1001 股应被拦, got %+v", v)
	}
	// 在途单结清（剩余 500 亦成交 + 终态）：可卖量回到持仓量 1000
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-N3", Code: "600000.SH", Side: "卖出",
		Price: 11, Qty: 500, Amount: 5500, TradedAt: today + "T10:00:00", SignalID: sid, UserID: "u_g"}); err != nil {
		t.Fatalf("apply rest fill: %v", err)
	}
	if _, err := db.AdvanceRealOrderStatus("u_g", sid, "已成"); err != nil {
		t.Fatalf("settle order: %v", err)
	}
	if got := db.SumOpenSellQty("u_g", "600000.SH", today); got != 0 {
		t.Fatalf("已成终态后在途应为 0, got %d", got)
	}
	o3 := liveOrder(SideSell)
	o3.Qty = 1000
	if v := g.CheckLiveOrder(cfg, o3); !v.Pass {
		t.Fatalf("结清后可卖量应=持仓 1000，卖 1000 应放行, got %+v", v)
	}
	o4 := liveOrder(SideSell)
	o4.Qty = 1100
	if v := g.CheckLiveOrder(cfg, o4); v.Pass {
		t.Fatalf("持仓仅 1000，卖 1100 应被拦, got %+v", v)
	}
}

// intPtr 取 int 地址（§A1 夹具：CanUseQty 三态里的"柜台真值"分支）。
func intPtr(v int) *int { return &v }

// TestGateT1SellableCounterPriority §0925EVE-W2-A1（2026-09-26 批）判定改口径回归：
// 柜台 can_use_qty 优先（收紧方向绝不放行更多卖出），本地推算作交叉告警，读不到退回本地。
// 表驱动逐案钉死四态：
//
//	① 柜台更小（qty 300 / can_use 100，无当日买入、无在途）：改前可卖 300 放行 150，
//	  改后拦 150 放 100 —— 资损方向（柜台说可卖更少）一律以柜台为准；
//	② 交叉偏差告警：①场景偏差 200 > 阈值 max(100, 300×2%)=100 → warn 告警不静默；
//	③ 柜台更大（can_use 1000 > 本地 300）：不得放行更多（min 封顶，本地在途扣减腿
//	  在快照窗口内不可替代，§UAT-D4 语义保留），但偏差告警同样要响；
//	④ 柜台读不到（CanUseQty=nil，桥通道旧行/成交回报建行）：现行为原样保留（回归到
//	  TestGateT1Sellable / TestGateT1SellableCountsOpenSells 已覆盖，此处只钉"无告警"）；
//	⑤ 柜台真值 0：任何卖单都拦——0 是可卖量真值，不是"没读到"。
//	⑥ 柜台可读 + 在途卖单：sellable=min(counter, qty−bought−openSell)，在途腿不因柜台
//	  优先而丢失（快照先于本单受理，柜台值对窗口内新单不可见）。
//
// English: §A1 — counter can_use_qty tightens the sellable estimate (never loosens it),
// deviations beyond max(100, 2%) raise warn alerts, absent counter values keep legacy behavior.
func TestGateT1SellableCounterPriority(t *testing.T) {
	db := gateDB(t)
	var alerts []string
	g := NewGate(db, "u_g", func(level, title, content string) {
		alerts = append(alerts, level+"|"+title+"|"+content)
	})
	cfg := qmtCfg() // EnforceT1 默认开
	today := cntime.In(time.Now()).Format("2006-01-02")

	seed := func(code string, qty, canUse int, withCounter bool) {
		p := store.RealPosition{TsCode: code, Name: "浦发", Qty: qty, CostPrice: 10, Amount: float64(qty) * 10, HighestPrice: 10}
		if withCounter {
			c := canUse
			p.CanUseQty = &c
		}
		if _, err := db.ReconcilePositionsForUser("u_g", []store.RealPosition{p}); err != nil {
			t.Fatalf("seed position: %v", err)
		}
	}

	// ①+② 柜台更小：150 拦、100 放，且偏差告警响。
	seed("600000.SH", 300, 100, true)
	o := liveOrder(SideSell)
	o.Qty = 150
	if v := g.CheckLiveOrder(cfg, o); v.Pass {
		t.Fatalf("① 柜台可卖 100 < 请求 150 必须拦（资损方向）, got %+v", v)
	} else if !strings.Contains(v.Reason, "柜台 can_use_qty=100") {
		// 拒单文案必须标注口径来源（柜台优先还是本地推算）——排障时第一眼要能分辨。
		t.Fatalf("① 拒单理由应含柜台口径标注, got %q", v.Reason)
	}
	o2 := liveOrder(SideSell)
	o2.Qty = 100
	if v := g.CheckLiveOrder(cfg, o2); !v.Pass {
		t.Fatalf("① 恰好可卖 100 应放行, got %+v", v)
	}
	if len(alerts) == 0 {
		t.Fatalf("② 偏差 200 > 阈值 100 必须告警（不静默）")
	}
	if !strings.Contains(alerts[0], "warn|T+1 可卖量交叉偏差") {
		t.Fatalf("② 告警应为 warn 级交叉偏差, got %q", alerts[0])
	}

	// ③ 柜台更大：本地推算封顶，但告警照响（两本账互相守望）。
	alerts = nil
	seed("600000.SH", 300, 1000, true)
	o3 := liveOrder(SideSell)
	o3.Qty = 400
	if v := g.CheckLiveOrder(cfg, o3); v.Pass {
		t.Fatalf("③ 本地推算 300 封顶，柜台乐观不得放大卖出权限, got %+v", v)
	}
	o3b := liveOrder(SideSell)
	o3b.Qty = 300
	if v := g.CheckLiveOrder(cfg, o3b); !v.Pass {
		t.Fatalf("③ 卖 300（本地=柜台扣在途前口径内）应放行, got %+v", v)
	}
	if len(alerts) == 0 || !strings.Contains(alerts[0], "交叉偏差") {
		t.Fatalf("③ 柜台>本地 同样要触发交叉告警, got %v", alerts)
	}

	// ④ 柜台读不到（CanUseQty=nil）：退回本地推算，零告警（现行为保留）。
	// 用另一代码 600001.SH 建行——600000 行已带柜台值且 upsert 的 COALESCE 会保留
	// 最近一次已知值（store 测试②已钉该语义），无法在此造出"从没读到"态。
	alerts = nil
	seed("600001.SH", 300, 0, false)
	o4 := liveOrder(SideSell)
	o4.Code = "600001.SH"
	o4.Qty = 300
	if v := g.CheckLiveOrder(cfg, o4); !v.Pass {
		t.Fatalf("④ 无柜台值时应按本地推算放行 300, got %+v", v)
	}
	if len(alerts) != 0 {
		t.Fatalf("④ 无柜台值不得发交叉告警, got %v", alerts)
	}

	// ⑤ 柜台真值 0：与"没读到"两态分家——0 一律拦。
	seed("600000.SH", 300, 0, true)
	o5 := liveOrder(SideSell)
	o5.Qty = 100
	if v := g.CheckLiveOrder(cfg, o5); v.Pass {
		t.Fatalf("⑤ 柜台可卖 0（真值）必须拦, got %+v", v)
	}

	// ⑥ 柜台可读 + 当日在途卖单：在途扣减腿保留（§UAT-D4）。
	seed("600000.SH", 300, 300, true)
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "OS9", SignalID: "SS9", Code: "600000.SH",
		Side: "卖出", Status: "已报", Price: 10, Qty: 200, CreatedAt: today + "T10:00:00+08:00", UserID: "u_g"}); err != nil {
		t.Fatalf("seed open sell: %v", err)
	}
	o6 := liveOrder(SideSell)
	o6.Qty = 150
	if v := g.CheckLiveOrder(cfg, o6); v.Pass {
		t.Fatalf("⑥ 在途 200 占额度后可卖仅 100，150 必须拦, got %+v", v)
	}
}
