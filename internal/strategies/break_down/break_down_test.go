// break_down 放量破位战法单元测试：双破位满分/无破位0分/缩量减半/防洗盘7折/数据不足。
package break_down

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/shortbase"
)

// mkBars 生成 n 根日K（收盘从 start 线性到 end），末根可覆盖 OHLCV。
func mkBars(n int, start, end, vol float64) []data.KLine {
	kl := make([]data.KLine, n)
	for i := range kl {
		c := start + (end-start)*float64(i)/float64(n-1)
		kl[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-n), Open: c, High: c, Low: c * 0.99, Close: c, Volume: vol}
	}
	return kl
}

func brokenData() *shortbase.Data {
	kl := mkBars(40, 20, 15, 1e6)
	last := &kl[len(kl)-1]
	last.Close = 13.6
	last.High = 15.2 // 高开低走长上影
	last.Volume = 2.5e6
	return &shortbase.Data{
		Code: "000001.SZ", Name: "测试股", Price: 13.6, PrevClose: 14.5, ChangePct: -6.2,
		MA5: 14.3, MA10: 14.6, MA20: 14.9,
		Low20:     14.0,
		BreakMA20: true, BreakLow20: true, BreakDepthPct: 3.5,
		Vol5: 1.1e6, TodayVolVs5d: 2.27,
		UpperShadowPct: 0.6,
		KLines:         kl,
	}
}

func TestPassFullChain(t *testing.T) {
	s := New(nil)
	ev, err := s.Evaluate("000001.SZ", brokenData())
	if err != nil || ev == nil {
		t.Fatalf("Evaluate 出错: %v", err)
	}
	if !ev.Pass || ev.Level != "full_chain" {
		t.Fatalf("双破位+放量+大面应过闸: total=%.1f", ev.TotalScore)
	}
	sig, _ := s.GenerateSignal("000001.SZ", ev)
	if sig == nil || string(sig.Action) != "sell" {
		t.Fatalf("应产出 sell 信号")
	}
}

func TestNoBreak(t *testing.T) {
	s := New(nil)
	d := brokenData()
	d.BreakMA20, d.BreakLow20, d.BreakDepthPct = false, false, 0
	ev, _ := s.Evaluate("000001.SZ", d)
	if ev.Level != "no_break" || ev.Pass {
		t.Fatalf("无破位应 no_break: %+v", ev.Level)
	}
}

func TestShakeoutDiscount(t *testing.T) {
	s := New(nil)
	d := brokenData()
	d.MA5, d.MA10 = 15.0, 14.4 // 均线仍多头
	d.BreakDepthPct = 0.5      // 破位极浅
	evFull, _ := s.Evaluate("000001.SZ", brokenData())
	evDisc, _ := s.Evaluate("000001.SZ", d)
	if evDisc.TotalScore >= evFull.TotalScore*0.85 {
		t.Fatalf("浅破位+均线多头应7折缓判: full=%.1f disc=%.1f", evFull.TotalScore, evDisc.TotalScore)
	}
}

func TestLowVolHalved(t *testing.T) {
	s := New(nil)
	d := brokenData()
	d.TodayVolVs5d = 0.6 // 缩量破位
	ev, _ := s.Evaluate("000001.SZ", d)
	if ev.Details["volume"] > 9 {
		t.Fatalf("缩量破位量能分应减半(<9): got %.1f", ev.Details["volume"])
	}
}

func TestNoData(t *testing.T) {
	s := New(nil)
	d := brokenData()
	d.KLines = mkBars(20, 20, 15, 1e6) // <25 根
	ev, _ := s.Evaluate("000001.SZ", d)
	if ev.Level != "nodata" || ev.Pass {
		t.Fatalf("K线不足应 nodata")
	}
}
