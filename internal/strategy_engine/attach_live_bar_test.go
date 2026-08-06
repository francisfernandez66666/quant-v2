package strategy_engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// TestAttachLiveBarAppendsToday 最后一根日K是昨日时，应追加当日实时bar，
// 且收盘价 = 实时价、高点不折价。
func TestAttachLiveBarAppendsToday(t *testing.T) {
	e := &Engine{}
	yesterday := time.Now().AddDate(0, 0, -1)
	klines := []data.KLine{
		{Date: yesterday, Open: 10, High: 10.5, Low: 9.8, Close: 10.2, Volume: 1000},
	}
	md := &StockMarketData{
		Price:     11.0,
		ChangePct: 7.8,
		KLines:    klines,
		Quote:     &data.StockInfo{Open: 10.4, High: 11.2, Low: 10.3, Price: 11.0, Volume: 5000},
	}
	e.attachLiveBar(md)
	if len(md.KLines) != 2 {
		t.Fatalf("expected 2 bars, got %d", len(md.KLines))
	}
	last := md.KLines[1]
	if last.Close != 11.0 {
		t.Errorf("close = %v, want 11.0", last.Close)
	}
	if last.High != 11.2 {
		t.Errorf("high = %v, want 11.2", last.High)
	}
	if last.Volume != 5000 {
		t.Errorf("volume = %v, want 5000", last.Volume)
	}
}

// TestAttachLiveBarFixesToday 最后一根已是今日时，仅用实时价修正其收盘，不新增bar。
func TestAttachLiveBarFixesToday(t *testing.T) {
	e := &Engine{}
	today := time.Now()
	klines := []data.KLine{
		{Date: time.Now().AddDate(0, 0, -2), Open: 10, High: 10.5, Low: 9.8, Close: 10.2, Volume: 1000},
		{Date: today, Open: 10.4, High: 10.6, Low: 10.1, Close: 10.3, Volume: 3000},
	}
	md := &StockMarketData{
		Price:     10.9,
		ChangePct: 1.5,
		KLines:    klines,
		Quote:     &data.StockInfo{Open: 10.4, High: 11.0, Low: 10.1, Price: 10.9, Volume: 4000},
	}
	e.attachLiveBar(md)
	if len(md.KLines) != 2 {
		t.Fatalf("expected 2 bars (no append), got %d", len(md.KLines))
	}
	last := md.KLines[1]
	if last.Close != 10.9 {
		t.Errorf("close = %v, want 10.9", last.Close)
	}
	if last.High != 11.0 {
		t.Errorf("high = %v, want 11.0 (real-time high must win)", last.High)
	}
}

// TestAttachLiveBarNoQuote 无实时价格时不应改动K线。
func TestAttachLiveBarNoQuote(t *testing.T) {
	e := &Engine{}
	klines := []data.KLine{
		{Date: time.Now().AddDate(0, 0, -1), Open: 10, High: 10.5, Low: 9.8, Close: 10.2, Volume: 1000},
	}
	md := &StockMarketData{Price: 0, KLines: klines}
	e.attachLiveBar(md)
	if len(md.KLines) != 1 {
		t.Fatalf("expected unchanged 1 bar, got %d", len(md.KLines))
	}
}
