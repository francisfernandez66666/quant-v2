// 实时触发引擎单元测试：覆盖滑动窗口差分计算、触发判定与冷却抑制行为。
package trigger

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// TestAdvanceWindow 验证窗口推进逻辑：首个 tick 仅初始化，6 秒后应计算出正确的
// 秒均涨幅（约 0.333%/s）与秒均成交额（约 33.3 万）。
func TestAdvanceWindow(t *testing.T) {
	e := New(nil, nil, DefaultConfig())
	now := time.Now()

	// 首个 tick：仅初始化
	_, _, _, st := e.advance("600001", &data.StockInfo{Code: "600001", Price: 10, Amount: 1e7}, now)
	if st == nil {
		t.Fatal("首个 tick 应初始化状态")
	}

	// 6 秒后：+2%（秒均 0.333%/s），成交额 +200 万（秒额 33.3 万）
	secRise, secAmt, _, _ := e.advance("600001", &data.StockInfo{
		Code: "600001", Price: 10.2, Amount: 1.2e7, Turnover: 1,
	}, now.Add(6*time.Second))

	if secRise < 0.333 || secRise > 0.334 {
		t.Errorf("secRise = %v, want ~0.333", secRise)
	}
	if secAmt < 333333.33-1 || secAmt > 333333.33+1 {
		t.Errorf("secAmt = %v, want ~333333.33", secAmt)
	}
}

// TestCheckTriggers 验证 check 全流程：两帧快照（首帧初始化 + 第二帧放量急拉）
// 应把该股票加入监控状态集。
func TestCheckTriggers(t *testing.T) {
	e := New(nil, nil, DefaultConfig())
	now := time.Now()
	// 构造首帧
	e.check(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{
		"600001": {Code: "600001", Price: 10, Amount: 1e7},
	}, Time: now})
	// 第二帧放量急拉
	e.check(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{
		"600001": {Code: "600001", Name: "测试", Price: 10.2, Amount: 1.2e7},
	}, Time: now.Add(6 * time.Second)})
	if got := e.State(); got != 1 {
		t.Errorf("State = %d, want 1", got)
	}
}

// TestCooldownSuppresses 验证冷却机制：股票触发后处于冷却期内，再次急拉应被跳过（advance 返回 nil）。
func TestCooldownSuppresses(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Cooldown = 10 * time.Minute
	e := New(nil, nil, cfg)
	now := time.Now()

	// 触发一次
	e.check(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{
		"600001": {Code: "600001", Price: 10, Amount: 1e7},
	}, Time: now})
	e.check(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{
		"600001": {Code: "600001", Name: "测试", Price: 10.2, Amount: 1.2e7},
	}, Time: now.Add(6 * time.Second)})

	// 冷却期内再次急拉：advance 应返回 nil（跳过）
	_, _, _, st := e.advance("600001", &data.StockInfo{
		Code: "600001", Price: 10.4, Amount: 1.4e7,
	}, now.Add(12*time.Second))
	if st != nil {
		t.Error("冷却期内不应触发")
	}
}
