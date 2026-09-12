// high_churn 高位滞涨战法单元测试：满分形态/贴线不过闸/缩量滞涨减半/数据不足降级/置信度封顶。
package high_churn

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/shortbase"
)

// mkBars 生成 n 根日K（收盘价从 start 线性到 end），量能全程 vol。
func mkBars(n int, start, end, vol float64) []data.KLine {
	kl := make([]data.KLine, n)
	for i := range kl {
		c := start + (end-start)*float64(i)/float64(n-1)
		kl[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-n), Open: c, High: c, Low: c * 0.99, Close: c, Volume: vol}
	}
	return kl
}

// fullData 构造一只"高位+放量+滞涨+长上影"的满分发票。
func fullData() *shortbase.Data {
	// 前段拉升 + 近20日再涨30%+，近3日滞涨，量能放大
	kl := mkBars(60, 10, 16, 1e6)
	// 末3根：横盘放量长上影
	for i := 57; i < 60; i++ {
		kl[i].Close = 16.0
		kl[i].High = 16.6
		kl[i].Volume = 2.2e6
	}
	return &shortbase.Data{
		Code: "600519.SH", Name: "测试股", Price: 15.9, PrevClose: 16.0,
		High60: 16.0, PosHigh: 0.99, Gain20: 0.35,
		VolRatio5_20: 1.8, Last3RangePct: 0.2,
		UpperShadowPct: 0.6, BelowMA5: true, ConsecDownDays: 2,
		KLines: kl,
	}
}

func TestPassFullChain(t *testing.T) {
	s := New(nil)
	ev, err := s.Evaluate("600519.SH", fullData())
	if err != nil || ev == nil {
		t.Fatalf("Evaluate 出错: %v", err)
	}
	if !ev.Pass || ev.Level != "full_chain" {
		t.Fatalf("满分形态应过闸: total=%.1f pass=%v level=%s", ev.TotalScore, ev.Pass, ev.Level)
	}
	if ev.Confidence < 0.85 {
		t.Fatalf("高置信应≥0.85, got %.2f", ev.Confidence)
	}
	sig, _ := s.GenerateSignal("600519.SH", ev)
	if sig == nil || string(sig.Action) != "sell" {
		t.Fatalf("过闸应产出 sell 信号, got %+v", sig)
	}
}

func TestBelowGate(t *testing.T) {
	s := New(nil)
	d := fullData()
	d.Gain20 = 0.05 // 不在高位、无背离
	d.PosHigh = 0.6
	d.VolRatio5_20 = 0.9
	ev, _ := s.Evaluate("600519.SH", d)
	if ev.Pass {
		t.Fatalf("非高位非放量不应过闸: total=%.1f", ev.TotalScore)
	}
	sig, _ := s.GenerateSignal("600519.SH", ev)
	if sig != nil {
		t.Fatalf("未过闸不应有信号")
	}
}

func TestDivergenceHalf(t *testing.T) {
	s := New(nil)
	d := fullData()
	d.VolRatio5_20 = 1.0 // 仅滞涨无放量 → 背离减半为 20
	ev, _ := s.Evaluate("600519.SH", d)
	if got := ev.Details["divergence"]; got != 20 {
		t.Fatalf("单边背离应 20 分, got %.1f", got)
	}
}

func TestNoData(t *testing.T) {
	s := New(nil)
	d := fullData()
	d.KLines = mkBars(40, 10, 16, 1e6) // <60 根
	ev, _ := s.Evaluate("600519.SH", d)
	if ev.Level != "nodata" || ev.Pass {
		t.Fatalf("K线不足应 nodata: %+v", ev)
	}
	// 非 Data 类型输入也安全降级
	if _, err := s.Evaluate("x", "not-a-data"); err != nil {
		t.Fatalf("错误类型输入不应 panic/err: %v", err)
	}
}

func TestConfidenceCap(t *testing.T) {
	s := New(nil)
	ev, _ := s.Evaluate("600519.SH", fullData())
	if ev.Confidence > 0.95 {
		t.Fatalf("置信度应封顶 0.95, got %.2f", ev.Confidence)
	}
}
