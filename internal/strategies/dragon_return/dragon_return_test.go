// dragon_return 龙回头战法：四因子评分、信号分档与硬性前置（龙性）拦截。
package dragon_return

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// newDR 构造测试用龙回头策略实例。
func newDR() *DragonReturnStrategy {
	return New(config.NewManager(""))
}

// goodStock 构造一个满足龙性前置 + 良好回调的标的。
func goodStock() *StockData {
	return &StockData{
		Code:         "x",
		CurrentPrice: 12.0,
		FirstRisePct: 0.5,  // 首轮 +50%（35%~70% 区间）
		PullbackPct:  0.18, // 回调 18%（15%~20% 最优 → 8分）
		PullbackDays: 6,    // 6 天（5~8 → 5分）
		VolumeRatio:  0.2,  // 缩量 20%（<30% → 8分）
		MA5:          11.0,
		MA10:         11.2,
		MA20:         10.8,
		MACDGreen:    -0.3, // 绿柱收窄
		IsSectorTop2: true, // 板块前 2
		SectorRPS20:  80,   // ≥75
		HighestPrice: 14.0,
		PreviousHigh: 13.0,
		HasRiseFirst: true,
	}
}

// TestEvaluateValid  评分通过且非 none；传入非 *StockData → 空结果。
func TestEvaluateValid(t *testing.T) {
	d := newDR()
	ev, err := d.Evaluate("0001", goodStock())
	if err != nil {
		t.Fatalf("Evaluate err: %v", err)
	}
	if ev == nil || !ev.Pass || ev.Level == "none" {
		t.Fatalf("完整标的应通过评分, got level=%s pass=%v", ev.Level, ev.Pass)
	}
	if ev.TotalScore < 60 {
		t.Errorf("总分应≥60, got %.1f", ev.TotalScore)
	}
	for _, k := range []string{"dragon_score", "pullback_score", "duck_score", "confirm_score", "first_rise", "pullback_pct"} {
		if _, ok := ev.Details[k]; !ok {
			t.Errorf("明细缺少 %s", k)
		}
	}
	// 类型不符 → 空结果不 panic
	bad, _ := d.Evaluate("2", "not-stock-data")
	if bad == nil || bad.Pass {
		t.Error("类型不符应返回 Pass=false 空结果")
	}
}

// TestDragonIdentityGate 龙性硬闸：任一项不满足 → 总分 0 / 不通过与。
func TestDragonIdentityGate(t *testing.T) {
	d := newDR()

	// 非板块前2
	s := goodStock()
	s.IsSectorTop2 = false
	if ev, _ := d.Evaluate("1", s); ev.Pass {
		t.Error("非板块前2 不应通过")
	}
	// 首轮涨幅不足
	s = goodStock()
	s.FirstRisePct = 0.2
	if ev, _ := d.Evaluate("1", s); ev.Pass {
		t.Error("首轮涨幅不足不应通过")
	}
	// 板块 RPS 不足
	s = goodStock()
	s.SectorRPS20 = 40
	if ev, _ := d.Evaluate("1", s); ev.Pass {
		t.Error("板块 RPS 不足不应通过")
	}
}

// TestSignalTing 未通过评分时不产信号；通过时产 buy。
func TestGenerateSignal(t *testing.T) {
	d := newDR()
	ev, _ := d.Evaluate("000001", goodStock())
	sig, err := d.GenerateSignal("000001", ev)
	if err != nil {
		t.Fatalf("GenerateSignal err: %v", err)
	}
	if sig == nil || sig.Action == strategy.ActionHold {
		t.Errorf("通过评分应产信号, got %+v", sig)
	}

	// Pass=false 应返回 nil 信号
	nilSig, _ := d.GenerateSignal("1", &strategy.Evaluation{Pass: false})
	if nilSig != nil {
		t.Error("Pass=false 不应产生信号")
	}
}

// TestDefaultParams 默认参数完整性。
func TestDefaultParams(t *testing.T) {
	p := DefaultParams()
	if p.ScoreThreshold != 60 || p.AccelerateScore != 85 || p.MainPositionScore != 75 {
		t.Errorf("默认阈值异常: %+v", p)
	}
	if p.MaxHoldDays != 8 || p.StopLossPct != 0.05 {
		t.Errorf("默认风控异常: %+v", p)
	}
}
