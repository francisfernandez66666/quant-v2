// dragon 破局龙战法：F1~F4 评分链路、信号分档与退出判断。
package dragon

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

func newCfg() *config.Manager {
	m := config.NewManager("")
	cfg := m.Get()
	cfg.Strategy.Dragon.F1SealWeight = 0.30
	cfg.Strategy.Dragon.F2ResonanceWeight = 0.25
	cfg.Strategy.Dragon.F3PremiumWeight = 0.20
	cfg.Strategy.Dragon.F4RsWeight = 0.25
	return m
}

// klines 构造 10 根日K，末 5 日从 base 涨到 end（用于 F4 趋势）。
func klines(base, end float64) []data.KLine {
	ks := make([]data.KLine, 10)
	for i := 0; i < 10; i++ {
		c := base + (end-base)*float64(i)/9
		ks[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-9), Close: c}
	}
	return ks
}

// strongSI 构造强势个股（封板 + 高量价）。
func strongSI() *data.StockInfo {
	return &data.StockInfo{
		Price: 11, ChangePct: 9.9, Volume: 1e7, Amount: 1e8,
	}
}

// TestEvaluateFullSeal 四维全命中 → full_chain 且通过。
func TestEvaluateFullSeal(t *testing.T) {
	d := New(newCfg())
	si := strongSI()
	sectors := []data.SectorInfo{{ChangePct: 10}} // 板块共振强
	ev := d.EvaluateReal("300000", si, klines(10, 12), sectors)
	if ev == nil {
		t.Fatal("EvaluateReal 不应返回 nil")
	}
	if !ev.Pass || ev.Level != "full_chain" {
		t.Errorf("强封板应 full_chain/pass, got %s/%v", ev.Level, ev.Pass)
	}
	for _, k := range []string{"f1_seal", "f2_resonance", "f3_premium", "f4_rs"} {
		if _, ok := ev.Details[k]; !ok {
			t.Errorf("明细缺少 %s", k)
		}
	}
}

// TestEvaluateByInsufficient K线不足/无价时为折扣（不 panic，返回 nil）。
func TestEvaluateInsufficientData(t *testing.T) {
	d := New(newCfg())
	if got := d.EvaluateReal("x", nil, nil, nil); got != nil {
		t.Error("无数据应返回 nil")
	}
	if got := d.EvaluateReal("x", strongSI(), klines(10, 10)[:3], nil); got != nil {
		t.Error("K线不足 5 根应返回 nil")
	}
}

// TestGenerateSignalPrio 评分分档到操作/优先级映射。
func TestGenerateSignalPrio(t *testing.T) {
	d := New(newCfg())
	buy := &strategy.Evaluation{Level: "full_chain", Confidence: 0.9}
	sig, err := d.GenerateSignal("1", buy)
	if err != nil || sig.Action != strategy.ActionBuy || sig.Priority != strategy.P1 {
		t.Errorf("full_chain高置信应 buy/P1, got %+v err=%v", sig, err)
	}

	brief := &strategy.Evaluation{Level: "brief", Confidence: 0.6}
	sig2, _ := d.GenerateSignal("2", brief)
	if sig2.Action != strategy.ActionWatch {
		t.Errorf("brief 应 watch, got %s", sig2.Action)
	}
}

// TestBuyPoints 四买点权重映射。
func TestBuyPoints(t *testing.T) {
	d := New(newCfg())
	bp := d.BuyPoints(d.cfg.Get().Strategy.Dragon)
	if bp["P1_first_to_second"] != 0.55 { // 0.30+0.25
		t.Errorf("P1 应=0.55, got %.2f", bp["P1_first_to_second"])
	}
	if bp["P2_divergence"] != 0.20 || bp["P3_weak_to_strong"] != 0.25 {
		t.Errorf("P2/P3 权重异常: %+v", bp)
	}
}

// TestInvarsChecked 验证龙回头战法的信号类型常量等不变量保持预期值。
func TestInvarsChecked(t *testing.T) {
	if strategy.SignalDragon != "dragon" {
		t.Error("SignalDragon 应=dragon")
	}
}
