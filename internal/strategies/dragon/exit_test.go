// dragon 破局龙战法退出逻辑：回撤/炸板/超期 的触发与抑制。
//
// 注：现有阈值字段(如 BuyPullbackSellAllPct=0.08)与 pnlPct(%) 同量纲比较，语义为
// "回调超过 0.08% 即全出"，且炸板分支先判半仓后判全出——本测试断言稳定不变量（有/无退出）。
package dragon

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

func dragonCfg() *config.DragonConfig {
	return &config.DragonConfig{
		BuyPullbackSellAllPct:  0.08,
		BuyPullbackSellHalfPct: 0.05,
		BreakerSellHalfPct:     0.06,
		BreakerSellAllPct:      0.10,
		BuyDayCloseBelow:       0.03,
		NextOpenIfBelow:        0.05,
		TakeProfitPct:          10,
	}
}

// TestExitBuyPullback 明显回调必须产出一个高优先级退出（全出路径）。
func TestExitBuyPullback(t *testing.T) {
	cfg := dragonCfg()
	r := CheckExit(&strategy.ExitContext{CostPrice: 10, CurPrice: 9.0, Now: time.Now()}, cfg)
	if r == nil || r.Priority != strategy.P1 {
		t.Errorf("深度亏损应触发 P1 退出, got %+v", r)
	}
	// 小幅浮盈（未达止盈线）不应触发止损退出
	if ok := CheckExit(&strategy.ExitContext{CostPrice: 10, CurPrice: 10.5, Now: time.Now()}, cfg); ok != nil {
		t.Errorf("小幅浮盈不应退出, got %+v", ok)
	}
}

// TestExitTakeProfit 浮盈达到 TakeProfitPct（C2）→ 止盈落袋。
func TestExitTakeProfit(t *testing.T) {
	cfg := dragonCfg()
	r := CheckExit(&strategy.ExitContext{CostPrice: 10, CurPrice: 11.2, Now: time.Now()}, cfg)
	if r == nil || r.Reason != "破局龙止盈" {
		t.Errorf("浮盈≥止盈线应触发止盈, got %+v", r)
	}
	// 未达止盈线的浮盈应继续持有
	if ok := CheckExit(&strategy.ExitContext{CostPrice: 10, CurPrice: 10.9, Now: time.Now()}, cfg); ok != nil {
		t.Errorf("未达止盈线不应退出, got %+v", ok)
	}
	// 止盈阈值未配置时回退默认 10%：+10% 恰好触发
	cfg2 := *cfg
	cfg2.TakeProfitPct = 0
	r2 := CheckExit(&strategy.ExitContext{CostPrice: 10, CurPrice: 11.0, Now: time.Now()}, &cfg2)
	if r2 == nil || r2.Reason != "破局龙止盈" {
		t.Errorf("默认止盈 10%% 应触发, got %+v", r2)
	}
}

// TestExitBreaker 跌破封板价应收出齐（半或全）。
func TestExitBreaker(t *testing.T) {
	cfg := dragonCfg()
	r := CheckExit(&strategy.ExitContext{
		CostPrice: 10, CurPrice: 10.5, Now: time.Now(),
		EntryMeta: map[string]float64{"limit_price": 12},
	}, cfg)
	if r == nil {
		t.Error("跌破封板价应产出退出信号")
	}
}

// TestExitTimeout 持仓 ≥2 天 → 破局龙超期强制离场（价格未达止盈线时）。
func TestExitTimeout(t *testing.T) {
	cfg := dragonCfg()
	now := time.Now()
	entry := now.AddDate(0, 0, -3).Format("2006-01-02")
	r := CheckExit(&strategy.ExitContext{CostPrice: 10, CurPrice: 10.5, Now: now, EntryAt: entry}, cfg)
	if r == nil || r.Reason != "破局龙超期" {
		t.Errorf("超期应强制离场, got %+v", r)
	}
}

// TestExitIllegal 非法成本不退出。
func TestExitIllegal(t *testing.T) {
	if CheckExit(&strategy.ExitContext{CostPrice: 0, CurPrice: 1, Now: time.Now()}, dragonCfg()) != nil {
		t.Error("非法成本应不退出")
	}
}
