// slippage_test.go — buildSlipCtx 集成回归：paper_trades 实测校准 → 战法级/全局回退 →
// 配置兜底（A.3 回退链在引擎侧的装配），以及增强关闭的 nil 短路。
// English: engine-side assembly tests for the calibration fallback chain (strategy pool →
// global → configured default) and the disabled short-circuit.
package btreplay

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// seedMedianFills 写入 n 笔同滑点成交（buy=买贵 bps、sell=卖便宜 bps）。
func seedMedianFills(t *testing.T, db *store.DB, pool string, n int, buyBps, sellBps float64) {
	t.Helper()
	at := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	recs := make([]store.PaperTradeRecord, 0, n*2)
	for i := 0; i < n; i++ {
		recs = append(recs,
			store.PaperTradeRecord{UserID: "u", Code: fmt.Sprintf("60%04d.SH", i), StrategyType: pool,
				Side: "buy", Price: 10 * (1 + buyBps/1e4), SignalPrice: 10, Qty: 100, Amount: 1000,
				FilledAt: at + " 10:00:00"},
			store.PaperTradeRecord{UserID: "u", Code: fmt.Sprintf("60%04d.SH", i), StrategyType: pool,
				Side: "sell", Price: 10 * (1 - sellBps/1e4), SignalPrice: 10, Qty: 100, Amount: 1000,
				FilledAt: at + " 14:00:00"},
		)
	}
	if err := db.SavePaperTrades(recs); err != nil {
		t.Fatal(err)
	}
}

// calibratedBT 一份开自动校准的启用配置（名义额显式给定，绕开默认回填）。
func calibratedBT() *config.BacktestConfig {
	bt := &config.BacktestConfig{Enabled: true, OrderValueYuan: 10000, PaperModelSlippageBps: 5}
	bt.Slippage = config.SlippageConfig{BaseBps: 3, BuyExtraBps: 1, Asymmetric: true,
		AutoCalibrate: true, CalibMinSample: 30, CalibWindowDays: 90}
	return bt
}

// TestBuildSlipCtxDisabled 关闭/nil 配置 → nil 上下文（旧行为短路）。
func TestBuildSlipCtxDisabled(t *testing.T) {
	o := &Options{}
	if sc, audit := o.buildSlipCtx(nil, "龙头", ""); sc != nil || audit != nil {
		t.Fatal("未配置应返回 nil")
	}
	o.Backtest = &config.BacktestConfig{Enabled: false}
	if sc, _ := o.buildSlipCtx(nil, "龙头", ""); sc != nil {
		t.Fatal("停用应返回 nil")
	}
}

// TestBuildSlipCtxStrategyCalib 战法级样本充足：base=clamp(min(14,10)-5,3,15)=5、
// 买差=clamp(14-10,0,5)=4，审计 source=paper_median。
func TestBuildSlipCtxStrategyCalib(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedMedianFills(t, db, "dragon", 35, 14, 10) // 35 笔/向 ≥ 30 门槛
	o := &Options{Backtest: calibratedBT()}
	sc, audit := o.buildSlipCtx(db, "龙头", "")
	if sc == nil {
		t.Fatal("enabled 应有上下文")
	}
	if !nearly(sc.baseBps, 5) {
		t.Fatalf("校准基准=%f, want 5", sc.baseBps)
	}
	if !nearly(sc.slip.BuyExtraBps, 4) {
		t.Fatalf("非对称买差=%f, want 4", sc.slip.BuyExtraBps)
	}
	if audit["source"] != "paper_median" {
		t.Fatalf("审计 source=%v", audit["source"])
	}
}

// TestBuildSlipCtxGlobalFallback 战法级无样本 → 回退全局样本（A.3 分组口径）。
func TestBuildSlipCtxGlobalFallback(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedMedianFills(t, db, "n_shape", 40, 14, 10) // 只喂 n_shape 池
	o := &Options{Backtest: calibratedBT()}
	sc, audit := o.buildSlipCtx(db, "龙头", "") // dragon 池零样本 → 全局回退命中
	if sc == nil || audit["source"] != "paper_median" {
		t.Fatalf("应回退全局样本: %v", audit)
	}
	if !nearly(sc.baseBps, 5) {
		t.Fatalf("全局回退基准=%f, want 5", sc.baseBps)
	}
}

// TestBuildSlipCtxNoDataFallback 完全无成交 → 配置值兜底（BaseBps=3/BuyExtra=1）。
func TestBuildSlipCtxNoDataFallback(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	o := &Options{Backtest: calibratedBT()}
	sc, audit := o.buildSlipCtx(db, "龙头", "")
	if sc == nil || !nearly(sc.baseBps, 3) || !nearly(sc.slip.BuyExtraBps, 1) {
		t.Fatalf("无数据应回退配置值: %+v", sc)
	}
	if audit["source"] == "paper_median" {
		t.Fatal("无数据不得标 paper_median")
	}
}
