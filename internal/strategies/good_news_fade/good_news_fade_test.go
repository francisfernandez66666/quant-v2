// good_news_fade 利好兑现砸盘战法单元测试：事件在窗前提/超窗不发/主升未止防误杀/传导打折/兑现分档。
package good_news_fade

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/shortbase"
)

// mkBars 生成 n 根日K（收盘从 start 线性到 end）。
func mkBars(n int, start, end, vol float64) []data.KLine {
	kl := make([]data.KLine, n)
	for i := range kl {
		c := start + (end-start)*float64(i)/float64(n-1)
		kl[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-n), Open: c, High: c, Low: c * 0.99, Close: c, Volume: vol}
	}
	return kl
}

func fadeData() *shortbase.Data {
	n := 30
	kl := mkBars(n, 8, 10, 1e6) // 事件日 idx=25 收盘≈10（前段温和上行）
	// idx=26 冲高 +25%（利好兑现拉升），随后 idx 27~29 滞涨横盘（派发）
	kl[25].Close = 10
	for i := 26; i < n; i++ {
		kl[i].Close = 12.5
		kl[i].High = 12.5
		kl[i].Volume = 2.2e6
	}
	return &shortbase.Data{
		Code: "600519.SH", Name: "测试股", Price: 12.5, PrevClose: 12.5,
		KLines:        kl,
		EventInWindow: true, EventAgeDays: 4, EventScore: 0.75,
		EventDayClose: 10,
		Last3RangePct: 0.0, VolRatio5_20: 1.8, BelowMA5: false, UpperShadowPct: 0.5,
	}
}

func TestPassFullChain(t *testing.T) {
	s := New(nil)
	ev, err := s.Evaluate("600519.SH", fadeData())
	if err != nil || ev == nil {
		t.Fatalf("Evaluate 出错: %v", err)
	}
	if !ev.Pass || ev.TotalScore < 60 {
		t.Fatalf("事件在窗+兑现+转弱应过闸: total=%.1f", ev.TotalScore)
	}
	if ev.Confidence < 0.6 {
		t.Fatalf("重大利好(|score|≥0.75)置信度应加成: %.2f", ev.Confidence)
	}
	sig, _ := s.GenerateSignal("600519.SH", ev)
	if sig == nil || string(sig.Action) != "sell" {
		t.Fatalf("应产出 sell 信号")
	}
}

func TestNoEvent(t *testing.T) {
	s := New(nil)
	d := fadeData()
	d.EventInWindow = false
	ev, _ := s.Evaluate("x", d)
	if ev.Level != "no_event" || ev.Pass {
		t.Fatalf("事件不在窗应直接不成立: %s", ev.Level)
	}
}

func TestGainTooSmall(t *testing.T) {
	s := New(nil)
	d := fadeData()
	d.EventDayClose = 13.1 // 事件至今仅 +0.8%，未兑现
	ev, _ := s.Evaluate("x", d)
	if ev.Details["gain_faded"] != 0 {
		t.Fatalf("兑现不足10%% 涨幅分应为0")
	}
	if ev.Pass {
		t.Fatalf("未兑现不应过闸: total=%.1f", ev.TotalScore)
	}
}

func TestFreshSurgeSuppress(t *testing.T) {
	s := New(nil)
	d := fadeData()
	kl := d.KLines
	n := len(kl)
	kl[n-1].Close = kl[n-2].Close * 1.06 // 末日 +6%，主升未止
	ev, _ := s.Evaluate("x", d)
	if ev.Level != "still_rising" || ev.Pass {
		t.Fatalf("近3日仍有≥5%%大阳应防误杀不发: %s", ev.Level)
	}
}

func TestPropagationDiscount(t *testing.T) {
	s := New(nil)
	ev1, _ := s.Evaluate("x", fadeData())
	d := fadeData()
	d.EventPropagation = true
	ev2, _ := s.Evaluate("x", d)
	if ev2.TotalScore >= ev1.TotalScore*0.85 {
		t.Fatalf("传导类应7折缓判: %.1f vs %.1f", ev2.TotalScore, ev1.TotalScore)
	}
}

func TestNoData(t *testing.T) {
	s := New(nil)
	d := fadeData()
	d.KLines = mkBars(15, 10, 12, 1e6) // <20 根
	ev, _ := s.Evaluate("x", d)
	if ev.Level != "nodata" || ev.Pass {
		t.Fatalf("K线不足应 nodata")
	}
}
