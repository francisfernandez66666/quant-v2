// backtest_settings_test.go 回测增强配置管线测试（A0）：单行读写幂等、
// paper_trades 实测滑点统计（分方向中位数、非法价格过滤、战法过滤、回看窗口）。
// English: settings round-trip (single-row upsert) + slippage calibration stats tests.
package store

import (
	"fmt"
	"testing"
	"time"
)

// TestBacktestSettingsRoundTrip 无记录 → false；Set 后可读回；重复 Set 幂等覆盖（仍一行）。
func TestBacktestSettingsRoundTrip(t *testing.T) {
	db := testDB(t)
	raw, ok, err := db.GetBacktestSettings()
	if err != nil || ok || raw != "" {
		t.Fatalf("初始应为无记录: ok=%v raw=%q err=%v", ok, raw, err)
	}
	if err := db.SetBacktestSettings(`{"enabled":true,"slippage":{"base_bps":3}}`); err != nil {
		t.Fatal(err)
	}
	raw, ok, err = db.GetBacktestSettings()
	if err != nil || !ok || raw != `{"enabled":true,"slippage":{"base_bps":3}}` {
		t.Fatalf("读回不一致: ok=%v raw=%q err=%v", ok, raw, err)
	}
	// 覆盖保存（UPSERT 单行）
	if err := db.SetBacktestSettings(`{"enabled":false}`); err != nil {
		t.Fatal(err)
	}
	raw, _, _ = db.GetBacktestSettings()
	if raw != `{"enabled":false}` {
		t.Fatalf("覆盖后 raw=%q", raw)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM backtest_settings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应恒为单行, got %d", n)
	}
}

// seedFills 写入若干模拟盘成交（相对"今天"回看偏移生成 filled_at）。
// 时刻按"策略名长度 + 序号"错开——UNIQUE(user_id,code,side,filled_at) 下
// 不同批次不得撞键（撞了会被 INSERT OR IGNORE 静默吞行）。
func seedFills(t *testing.T, db *DB, side, strategyType string, bps []float64, daysAgo int) {
	t.Helper()
	day := time.Now().AddDate(0, 0, -daysAgo).Format("2006-01-02")
	recs := make([]PaperTradeRecord, 0, len(bps))
	for i, b := range bps {
		sig := 10.0
		var price float64
		switch side {
		case "buy":
			price = sig * (1 + b/10000) // 买贵为正
		default:
			price = sig * (1 - b/10000) // 卖便宜为正
		}
		recs = append(recs, PaperTradeRecord{
			UserID: "u_1", Code: fmt.Sprintf("60%04d.SH", i), StrategyType: strategyType,
			Side: side, Price: price, SignalPrice: sig, Qty: 100, Amount: price * 100,
			FilledAt: fmt.Sprintf("%s 10:%02d:00", day, (i+len(strategyType))%60),
		})
	}
	if err := db.SavePaperTrades(recs); err != nil {
		t.Fatal(err)
	}
}

// TestPaperSlippageCalib 中位数口径、非法 signal_price 行过滤、战法分组、全局回退可查。
func TestPaperSlippageCalib(t *testing.T) {
	db := testDB(t)
	// 买入 5 笔实测滑点（bp）：1/2/3/4/5 → 中位数 3；卖出：2/3/4/5/6 → 中位数 4
	seedFills(t, db, "buy", "dragon", []float64{1, 2, 3, 4, 5}, 1)
	seedFills(t, db, "sell", "dragon", []float64{2, 3, 4, 5, 6}, 1)
	// 另一战法 + 非法行（signal_price=0 应被过滤，不进样本）
	seedFills(t, db, "buy", "n_shape", []float64{20}, 1)
	if err := db.SavePaperTrades([]PaperTradeRecord{{
		UserID: "u_1", Code: "600999.SH", StrategyType: "dragon", Side: "buy",
		Price: 10, SignalPrice: 0, Qty: 100, Amount: 1000,
		FilledAt: time.Now().Format("2006-01-02") + " 11:00:00",
	}}); err != nil {
		t.Fatal(err)
	}

	c, err := db.PaperSlippageCalib("dragon", 30)
	if err != nil {
		t.Fatal(err)
	}
	if c.BuyN != 5 || c.SellN != 5 {
		t.Fatalf("样本数 = %d/%d, want 5/5（非法行应被过滤）", c.BuyN, c.SellN)
	}
	if diff := c.BuyMedBps - 3; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("买入中位数=%f, want 3", c.BuyMedBps)
	}
	if diff := c.SellMedBps - 4; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("卖出中位数=%f, want 4", c.SellMedBps)
	}
	// 战法分组：n_shape 只有 1 笔
	g, err := db.PaperSlippageCalib("n_shape", 30)
	if err != nil || g.BuyN != 1 {
		t.Fatalf("n_shape 分组样本=%d err=%v, want 1", g.BuyN, err)
	}
	// 回看窗口：365 天前无样本
	old := time.Now().AddDate(0, 0, -400).Format("2006-01-02") + " 10:00:00"
	if err := db.SavePaperTrades([]PaperTradeRecord{{
		UserID: "u_1", Code: "600001.SH", StrategyType: "dragon", Side: "buy",
		Price: 10.005, SignalPrice: 10, Qty: 100, Amount: 1000, FilledAt: old,
	}}); err != nil {
		t.Fatal(err)
	}
	if c2, _ := db.PaperSlippageCalib("dragon", 7); c2.BuyN != 5 {
		t.Fatalf("窗口回看失效: BuyN=%d, want 5", c2.BuyN)
	}
	// 全局口径（strategyType=""）含所有战法
	all, err := db.PaperSlippageCalib("", 30)
	if err != nil || all.BuyN != 6 {
		t.Fatalf("全局样本=%d err=%v, want 6", all.BuyN, err)
	}
}
