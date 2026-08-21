package btreplay

import (
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// TestSummarize 验证胜率/平均盈亏/盈亏比/平均持仓的统计口径。
func TestSummarize(t *testing.T) {
	trades := []trade{
		{Strategy: "DoubleBump", PnlPct: 10, HoldDays: 2},
		{Strategy: "DoubleBump", PnlPct: -5, HoldDays: 1},
		{Strategy: "DoubleBump", PnlPct: 20, HoldDays: 3},
		{Strategy: "DoubleBump", PnlPct: -5, HoldDays: 1},
	}
	s := summarize(trades)
	if s.Count != 4 {
		t.Fatalf("Count=%d, want 4", s.Count)
	}
	if s.Win != 2 || s.Loss != 2 {
		t.Fatalf("Win/Loss=%d/%d, want 2/2", s.Win, s.Loss)
	}
	if s.WinRate != 50 {
		t.Fatalf("WinRate=%f, want 50", s.WinRate)
	}
	if s.AvgWinPct != 15 {
		t.Fatalf("AvgWinPct=%f, want 15", s.AvgWinPct)
	}
	if s.AvgLossPct != -5 {
		t.Fatalf("AvgLossPct=%f, want -5", s.AvgLossPct)
	}
	if s.ProfitFactor != 3 {
		t.Fatalf("ProfitFactor=%f, want 3", s.ProfitFactor)
	}
	if s.AvgHold != 1.75 {
		t.Fatalf("AvgHold=%f, want 1.75", s.AvgHold)
	}
}

// TestSummarizeEmpty 无交易时统计应为零值且不除零。
func TestSummarizeEmpty(t *testing.T) {
	s := summarize(nil)
	if s.Count != 0 || s.WinRate != 0 || s.ProfitFactor != 0 || s.AvgHold != 0 {
		t.Fatalf("empty summary = %+v, want zero", s)
	}
}

// TestChgPct 涨跌幅百分比口径。
func TestChgPct(t *testing.T) {
	if got := chgPct(11, 10); got != 10 {
		t.Fatalf("chgPct(11,10)=%f, want 10", got)
	}
	if got := chgPct(9.5, 10); got != -5 {
		t.Fatalf("chgPct(9.5,10)=%f, want -5", got)
	}
	if got := chgPct(0, 0); got != 0 {
		t.Fatalf("chgPct(0,0)=%f, want 0", got)
	}
}

// TestAvgVolK 取截止第 n 根的最近 lookback 根量均值。
func TestAvgVolK(t *testing.T) {
	klines := make([]data.KLine, 22)
	for i := range klines {
		klines[i].Volume = float64(i + 1)
	}
	// 截止第 21 根（klines[20]），取最近 20 根即 klines[1..20] 均值
	got := avgVolK(klines, 21, 20)
	var sum float64
	for i := 1; i <= 20; i++ {
		sum += float64(i + 1)
	}
	if got != sum/20 {
		t.Fatalf("avgVolK=%f, want %f", got, sum/20)
	}
	// 总根数不足窗口时返回 0
	if got := avgVolK(klines, 10, 20); got != 0 {
		t.Fatalf("avgVolK(10,20)=%f, want 0", got)
	}
}

// TestToDataKLine store.Bar 到 data.KLine 的字段映射（Date 解析 YYYYMMDD，Vol 映射到 Volume）。
func TestToDataKLine(t *testing.T) {
	bars := []store.Bar{{Date: "20230101", Open: 1, High: 2, Low: 0.5, Close: 1.5, Vol: 100, Amount: 150}}
	out := toDataKLine(bars)
	if len(out) != 1 {
		t.Fatalf("len=%d, want 1", len(out))
	}
	if out[0].Date.Format("20060102") != "20230101" {
		t.Fatalf("Date=%s, want 20230101", out[0].Date.Format("20060102"))
	}
	if out[0].Close != 1.5 || out[0].Volume != 100 {
		t.Fatalf("Close/Volume=%f/%f, want 1.5/100", out[0].Close, out[0].Volume)
	}
}
