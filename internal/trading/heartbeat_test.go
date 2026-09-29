// heartbeat_test.go — §0929HB-2（2026-09-29 全量审计批 ⑪-4）「有卖出但已实现盈亏恒为 0」心跳用例。
//
// 这一条心跳针对的是"该有的数没了"型静默失效：日内亏损熔断闸（risk.checkDayLoss）与成交页
// 都读 TodayRealizedPnl，一旦卖出腿记账断了（成本取不到、方向错记成买入、费用整列为空——
// §0925EVE 与 §0927AUDIT-D1 锤实过的两族事故），读数就恒等于 0，而 0 在两个出口上都表现为
// "一切正常"，原有五条越界型告警全部沉默。
//
// 反证成对设计（等值锁，不是单向锁）：
//
//	A 有卖出 + 盈亏为 −509（含费口径，§0927AUDIT-D1 的期望值）→ 不判可疑；
//	B 有卖出 + 成本不可知致盈亏恒 0 → 判可疑；
//	C 当日没有卖出 → 不判可疑（"今天没交易"不得冒充"账断了"）。
//
// 摘掉 Suspicious 判据 ⇒ B 变红；把 SellFills==0 的短路删掉 ⇒ C 变红。
//
// English: §0929HB-2 heartbeat tests — the "sells exist yet realized P&L is exactly zero"
// signature, with a matched triple (loss present / zero-with-sells / no sells) so neither the
// predicate nor the no-sell short circuit can silently degrade.
package trading

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/store"
)

// hbNow 用例时钟：与夹具成交时间同日（心跳按"当日"判定，两侧必须落在同一个日界里）。
var hbNow = time.Now()

// hbSeedDB 开一个临时账本库。
func hbSeedDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hb2.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// hbDayOf 返回与 hbNow 同源的当日字符串（YYYY-MM-DD，与 fills.traded_at 前 10 位同口径）。
func hbDayOf() string { return hbNow.Format("2006-01-02") }

// TestRealizedPnlHeartbeatMatchedTriple A/B/C 三支成对判据（口径见文件头）。
func TestRealizedPnlHeartbeatMatchedTriple(t *testing.T) {
	day := hbDayOf()

	// —— A 含费买入 → 含费+印花税卖出：熔断闸看到的是 −509，非 0 ⇒ 不判可疑 ——
	dbA := hbSeedDB(t)
	const uid, code = "u_hb2", "600001.SH"
	if err := dbA.ApplyRealFill(store.RealFill{OrderID: "A-B1", Code: code, Name: "测试甲", Side: "买入",
		Price: 10, Qty: 1000, Amount: 10000, TradedAt: day + " 09:35:00", TradeID: "A-TB1", UserID: uid, Fee: 5}); err != nil {
		t.Fatalf("seed buy: %v", err)
	}
	if err := dbA.ApplyRealFill(store.RealFill{OrderID: "A-S1", Code: code, Name: "测试甲", Side: "卖出",
		Price: 9, Qty: 500, Amount: 4500, TradedAt: day + " 14:30:00", TradeID: "A-TS1", UserID: uid,
		Fee: 4, StampTax: 2.5}); err != nil {
		t.Fatalf("seed sell: %v", err)
	}
	hA, err := dbA.RealizedPnlHeartbeatForUser(uid, day)
	if err != nil {
		t.Fatalf("heartbeat A: %v", err)
	}
	if hA.SellFills != 1 || hA.Suspicious {
		t.Fatalf("A 支应「有 1 笔卖出且盈亏非 0」，got %+v", hA)
	}
	// 数值腿与 §0927AUDIT-D1 的期望值同源：摘掉卖出腿扣费 ⇒ 这里是 −502.50 而不是 −509.00。
	if d := hA.RealizedPnl - (-509.0); d > 1e-6 && d < -1e-6 {
		t.Fatalf("A 支已实现盈亏口径漂移（期望 -509.00，含佣金+印花税），got %.4f", hA.RealizedPnl)
	}

	// —— B 有卖出但成本不可知（无持仓、当日也无买入）⇒ TodayRealizedPnl fail-open 成 0 ——
	dbB := hbSeedDB(t)
	if err := dbB.ApplyRealFill(store.RealFill{OrderID: "B-S1", Code: "600002.SH", Name: "测试乙", Side: "卖出",
		Price: 9, Qty: 500, Amount: 4500, TradedAt: day + " 14:30:00", TradeID: "B-TS1", UserID: uid,
		Fee: 4, StampTax: 2.5}); err != nil {
		t.Fatalf("seed orphan sell: %v", err)
	}
	hB, err := dbB.RealizedPnlHeartbeatForUser(uid, day)
	if err != nil {
		t.Fatalf("heartbeat B: %v", err)
	}
	if hB.SellFills != 1 || hB.RealizedPnl != 0 || !hB.Suspicious {
		t.Fatalf("B 支应「有卖出且盈亏恒 0 ⇒ 可疑」，got %+v", hB)
	}

	// —— C 当日无卖出：读数 0 是合法事实，不得判可疑 ——
	dbC := hbSeedDB(t)
	if err := dbC.ApplyRealFill(store.RealFill{OrderID: "C-B1", Code: "600003.SH", Name: "测试丙", Side: "买入",
		Price: 10, Qty: 1000, Amount: 10000, TradedAt: day + " 09:35:00", TradeID: "C-TB1", UserID: uid, Fee: 5}); err != nil {
		t.Fatalf("seed buy only: %v", err)
	}
	hC, err := dbC.RealizedPnlHeartbeatForUser(uid, day)
	if err != nil {
		t.Fatalf("heartbeat C: %v", err)
	}
	if hC.SellFills != 0 || hC.Suspicious {
		t.Fatalf("C 支应「无卖出不判可疑」，got %+v", hC)
	}
}

// TestRefreshRealizedPnlHeartbeatFeedsGaugePair 喂数量规的成对分支：B 形态写 1、A 形态写 0。
func TestRefreshRealizedPnlHeartbeatFeedsGaugePair(t *testing.T) {
	// 可疑形态：孤儿卖出（成本不可知 ⇒ 盈亏恒 0）
	dbBad := hbSeedDB(t)
	if err := dbBad.ApplyRealFill(store.RealFill{OrderID: "X-S1", Code: "600004.SH", Name: "测试丁", Side: "卖出",
		Price: 9, Qty: 300, Amount: 2700, TradedAt: hbDayOf() + " 14:00:00", TradeID: "X-TS1",
		UserID: "u_hb2g", Fee: 3, StampTax: 1.35}); err != nil {
		t.Fatalf("seed orphan sell: %v", err)
	}
	cBad := NewController(nil, dbBad, "u_hb2g", config.QMTConfig{Enabled: true}, nil)
	cBad.RefreshRealizedPnlHeartbeat(hbNow)
	if v, ok := metrics.GetGauge("realized_pnl_zero_with_sells"); !ok || v != 1 {
		t.Fatalf("有卖出且盈亏恒 0 时量规必须写 1，got %d ok=%v", v, ok)
	}

	// 健康形态：同库补上成本可知的买入后再看（同一夹具的对照支，证明不是"恒写 1"）
	dbOK := hbSeedDB(t)
	const code = "600005.SH"
	if err := dbOK.ApplyRealFill(store.RealFill{OrderID: "Y-B1", Code: code, Name: "测试戊", Side: "买入",
		Price: 10, Qty: 1000, Amount: 10000, TradedAt: hbDayOf() + " 09:35:00", TradeID: "Y-TB1",
		UserID: "u_hb2g", Fee: 5}); err != nil {
		t.Fatalf("seed buy: %v", err)
	}
	if err := dbOK.ApplyRealFill(store.RealFill{OrderID: "Y-S1", Code: code, Name: "测试戊", Side: "卖出",
		Price: 11, Qty: 500, Amount: 5500, TradedAt: hbDayOf() + " 14:00:00", TradeID: "Y-TS1",
		UserID: "u_hb2g", Fee: 4, StampTax: 2.75}); err != nil {
		t.Fatalf("seed sell: %v", err)
	}
	cOK := NewController(nil, dbOK, "u_hb2g", config.QMTConfig{Enabled: true}, nil)
	cOK.RefreshRealizedPnlHeartbeat(hbNow)
	if v := mustHbGauge(t); v != 0 {
		t.Fatalf("卖出且盈亏非 0 时必须等值 0（不是「变小」），got %d", v)
	}
}

// TestRefreshRealizedPnlHeartbeatNonLiveDoesNotWrite 反掩蔽纪律：非实盘控制器不得改写量规。
func TestRefreshRealizedPnlHeartbeatNonLiveDoesNotWrite(t *testing.T) {
	metrics.SetGauge("realized_pnl_zero_with_sells", 1) // 哨兵：模拟实盘引擎刚报出的可疑读数
	db := hbSeedDB(t)
	c := NewController(nil, db, "u_hb2n", config.QMTConfig{Enabled: false}, nil)
	c.RefreshRealizedPnlHeartbeat(hbNow)
	if v, _ := metrics.GetGauge("realized_pnl_zero_with_sells"); v != 1 {
		t.Fatalf("非实盘控制器不得写 0（会掩掉实盘读数），got %d", v)
	}
	// 未接账本同样不落笔（观测面缺口不得伪造成"账本健康"或"账本断了"）。
	metrics.SetGauge("realized_pnl_zero_with_sells", 1)
	cNoStore := NewController(nil, nil, "u_hb2n", config.QMTConfig{Enabled: true}, nil)
	cNoStore.RefreshRealizedPnlHeartbeat(hbNow)
	if v, _ := metrics.GetGauge("realized_pnl_zero_with_sells"); v != 1 {
		t.Fatalf("未接账本时不得改写量规，got %d", v)
	}
}

// TestRealizedPnlHeartbeatRuleKeyAligned 规则登记面等值锁：规则名/键/等级/For 全对齐。
func TestRealizedPnlHeartbeatRuleKeyAligned(t *testing.T) {
	var hits int
	var r metrics.AlertRule
	for _, one := range metrics.DefaultAlertRules() {
		if one.Name == "realized_pnl_zero_with_sells" {
			r = one
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("规则 realized_pnl_zero_with_sells 命中 %d 条（应为 1）", hits)
	}
	if r.Metric != "realized_pnl_zero_with_sells" {
		t.Fatalf("规则读的键 %q ≠ 写端落的键 %q（§DEADGAUGE）", r.Metric, "realized_pnl_zero_with_sells")
	}
	if r.Level != "p2" || r.Op != "gt" || r.Threshold != 0 || r.For != "300s" {
		t.Fatalf("规则口径漂移：level=%s op=%s threshold=%.0f for=%s（期望 p2/gt/0/300s）",
			r.Level, r.Op, r.Threshold, r.For)
	}
}

// mustHbGauge 读心跳量规（缺失即判红：接线比"读到 0"更要紧）。
func mustHbGauge(t *testing.T) int64 {
	t.Helper()
	v, ok := metrics.GetGauge("realized_pnl_zero_with_sells")
	if !ok {
		t.Fatalf("量规 %s 从未被写过（喂数点没接上）", "realized_pnl_zero_with_sells")
	}
	return v
}
