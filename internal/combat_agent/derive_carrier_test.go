package combat_agent

// derive_carrier_test.go —— 做空派生窗口的导出读数与 buildShortData 实际用的那批常量同源，
// 并且**"含今日 / 不含今日"这一侧由行为证明**：载体把偏移写成根数时差的就是今日那一根，
// 只比数字比不出来（20 与 21 都是"合法窗口"，判出的却是另一件事）。

import (
	"math"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// stubMarketData 用给定日K造一份最小行情快照（价格即最后一根收盘）。
func stubMarketData(kl []data.KLine, price float64) *strategy_engine.StockMarketData {
	return &strategy_engine.StockMarketData{Code: "600000.SH", Name: "测试主板", Price: price, KLines: kl}
}

func caFloat(t *testing.T, table map[string]interface{}, key string) float64 {
	t.Helper()
	raw, ok := table[key]
	if !ok {
		t.Fatalf("导出表缺键 %s", key)
	}
	switch v := raw.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		t.Fatalf("导出键 %s 类型不是数：%T", key, raw)
	}
	return 0
}

// TestDeriveWindowTable 窗口族逐键锁死：导出的秒数必须与适配器生产在算的那条时间窗同源
// （期望值取自常量、判据取自导出表，漂任何一边即红；这里不产生任何新数字）。
func TestDeriveWindowTable(t *testing.T) {
	table := DeriveCarrierWindows()
	want := map[string]float64{
		"ma5_window":           5,
		"ma10_window":          10,
		"ma20_window":          20,
		"vol5_window":          5,
		"vol20_window":         20,
		"hc_high_window":       60,
		"bd_low_window":        20,
		"hc_gain_window":       20,
		"hc_last3_window":      3,
		"ld_seal_tolerance":    0.995,
		"ld_touch_tolerance":   0.998,
		"gf_event_window_days": 5,
		// 龙回头族 + 断板天量比窗口（§DERIVE-CARRIER-1105：rules_ladder 声明了这些键，
		// 值只许从适配器那批常量出，所以它们必须出现在导出表里；dr_ma_* 与上面 ma*_window
		// 是**同一枚常量的两个名字**（短/中/长=5/10/20），这里两处各写各的字面量正是为了：
		// 一旦有人在导出侧把它们接成不同的常量，这两行就会不同值，本用例当场判红。
		"dr_min_bars":     30,
		"dr_ma_short":     5,
		"dr_ma_mid":       10,
		"dr_ma_long":      20,
		"dr_rally_window": 40,
		"ld_vol_window":   5,
	}
	for key, expected := range want {
		if got := caFloat(t, table, key); math.Abs(got-expected) > 1e-9 {
			t.Errorf("%s 导出=%v 期望=%v", key, got, expected)
		}
	}
	for key := range table {
		if _, ok := want[key]; !ok {
			t.Errorf("导出表有多余键 %s（载体不读它，等于无主的数进产物）", key)
		}
	}
	if len(table) != len(want) {
		t.Errorf("导出键数=%d 期望=%d", len(table), len(want))
	}
	// 容忍的方向：封板比触板更严（同一根 bar 摸到板不等于封住），
	// 两枚数一旦抄反，"断板"与"仍封板"会整批判反。
	if caFloat(t, table, "ld_seal_tolerance") >= caFloat(t, table, "ld_touch_tolerance") {
		t.Error("封板容忍必须严于（数值低于）触板容忍")
	}
}

// shortBars 造 n 根日K：收盘与成交都是 baseline，唯独最后一根（今日）可覆盖。
func shortBars(n int, closeToday, highToday, volumeToday float64) []data.KLine {
	base := 50.0
	kl := make([]data.KLine, n)
	for i := range kl {
		kl[i] = data.KLine{Date: time.Now().AddDate(0, 0, i-n),
			Open: base, High: base, Low: base, Close: base, Volume: 10}
	}
	kl[n-1] = data.KLine{Date: kl[n-1].Date, Open: base, High: highToday,
		Low: base, Close: closeToday, Volume: volumeToday}
	return kl
}

// TestWindowsMatchExportedRootCounts 均线族与量能族：导出的窗口就是实际取的那几根。
func TestWindowsMatchExportedRootCounts(t *testing.T) {
	table := DeriveCarrierWindows()
	n := 30
	kl := shortBars(n, 999, 999, 110)
	md := stubMarketData(kl, 999)
	sd := buildShortData("600000.SH", md, nil, &ScanInput{}, "", false, 0)

	ma5 := caFloat(t, table, "ma5_window")
	if got, want := sd.MA5, ((ma5-1)*50+999)/ma5; math.Abs(got-want) > 1e-9 {
		t.Errorf("MA5 窗口=%v 应为 %v，实算 %v", ma5, want, got)
	}
	ma20 := caFloat(t, table, "ma20_window")
	if got, want := sd.MA20, ((ma20-1)*50+999)/ma20; math.Abs(got-want) > 1e-9 {
		t.Errorf("MA20 窗口=%v 应为 %v，实算 %v", ma20, want, got)
	}
	vol5 := caFloat(t, table, "vol5_window")
	if got, want := sd.Vol5, ((vol5-1)*10+110)/vol5; math.Abs(got-want) > 1e-9 {
		t.Errorf("5 日均量窗口=%v 应为 %v，实算 %v", vol5, want, got)
	}
	vol20 := caFloat(t, table, "vol20_window")
	if got, want := sd.Vol20, ((vol20-1)*10+110)/vol20; math.Abs(got-want) > 1e-9 {
		t.Errorf("20 日均量窗口=%v 应为 %v，实算 %v", vol20, want, got)
	}
}

// TestExclTodayFamiliesStartOneBarFurtherOut 不含今日的三族：起点比根数再多让一根。
// 这一条是整组里最容易抄反的地方，所以判据用导出的根数**算出基准价**再对比。
func TestExclTodayFamiliesStartOneBarFurtherOut(t *testing.T) {
	table := DeriveCarrierWindows()
	n := 30
	kl := shortBars(n, 999, 999, 110)
	md := stubMarketData(kl, 999)
	sd := buildShortData("600000.SH", md, nil, &ScanInput{}, "", false, 0)

	// 今日收盘 999 是全序列最高：20 日最低必须还是 50（今日没进窗口）。
	if got := sd.Low20; math.Abs(got-50) > 1e-9 {
		t.Errorf("bd_low_window=%v（不含今日）时 20 日最低应为 50，实算 %v",
			caFloat(t, table, "bd_low_window"), got)
	}
	// 60 日高**含**今日（导出的那枚就是含今日的根数）。
	if got := sd.High60; math.Abs(got-999) > 1e-9 {
		t.Errorf("hc_high_window=%v（含今日）时 60 日高应为 999，实算 %v",
			caFloat(t, table, "hc_high_window"), got)
	}
	// 基准根：close[n-(window+1)]，窗口的每一根都不许错位。
	gainWindow := caFloat(t, table, "hc_gain_window")
	gainBase := 50.0
	if got := sd.Gain20; math.Abs(got-(999-gainBase)/gainBase) > 1e-9 {
		t.Errorf("hc_gain_window=%v 的基准应是昨日起溯 %v 根那根的收盘，实算涨幅 %v",
			gainWindow, gainWindow+1, got)
	}
	stallWindow := caFloat(t, table, "hc_last3_window")
	stallBase := 50.0
	if got, want := sd.Last3RangePct, (999-stallBase)/stallBase*100; math.Abs(got-want) > 1e-9 {
		t.Errorf("hc_last3_window=%v 的近 3 日区间涨幅应为 %v，实算 %v（昨日起溯 %v 根）",
			stallWindow, want, got, stallWindow+1)
	}

	// 反向验证"多让一根"真的让了：把倒数第 (window+1) 根改成异常值，涨幅必须跟着变。
	kl2 := shortBars(n, 999, 999, 110)
	kl2[n-int(gainWindow)-1] = data.KLine{Date: kl2[n-int(gainWindow)-1].Date,
		Open: 40, High: 40, Low: 40, Close: 40, Volume: 10}
	sd2 := buildShortData("600000.SH", stubMarketData(kl2, 999), nil, &ScanInput{}, "", false, 0)
	if got, want := sd2.Gain20, (999-40.0)/40.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("改动基准根后涨幅应变成 %v，实算 %v（说明窗口取错了根）", want, got)
	}
}

// TestSealAndTouchTolerancesDriveTheVerdicts 封板与触板两枚容忍按导出的数生效（含等号）。
func TestSealAndTouchTolerancesDriveTheVerdicts(t *testing.T) {
	table := DeriveCarrierWindows()
	seal := caFloat(t, table, "ld_seal_tolerance")
	touch := caFloat(t, table, "ld_touch_tolerance")
	n := 30
	limit := data.LimitUpPct("600000.SH", "测试主板")
	boardPx := 50 * (1 + limit/100)

	// 收盘正好在封板容忍上（Go 判 >=）→ 封住；浅一丝 → 没封住但摸过板。
	for _, c := range []struct {
		name        string
		close, high float64
		sealed      bool
		touched     bool
	}{
		{"收盘=板价×封板容忍", boardPx * seal, boardPx * touch, true, true},
		{"收盘差一丝", boardPx*seal - 1e-6, boardPx * touch, false, true},
		{"最高价=板价×触板容忍", boardPx*seal - 1e-6, boardPx * touch, false, true},
		{"最高价差一丝", boardPx*seal - 1e-6, boardPx*touch - 1e-6, false, false},
	} {
		kl := shortBars(n, c.close, c.high, 10)
		sd := buildShortData("600000.SH", stubMarketData(kl, c.close), nil, &ScanInput{}, "", false, 0)
		if sd.SealedToday != c.sealed {
			t.Errorf("%s：SealedToday 应为 %v，实为 %v（板价 %.4f 收盘 %.4f）",
				c.name, c.sealed, sd.SealedToday, boardPx, c.close)
		}
		if sd.TouchedBoardToday != c.touched {
			t.Errorf("%s：TouchedBoardToday 应为 %v，实为 %v（最高 %.4f）",
				c.name, c.touched, sd.TouchedBoardToday, c.high)
		}
	}
}

// TestEventWindowDayDrivesTheVerdict 在窗天数按导出的那一枚生效（含等号）。
func TestEventWindowDayDrivesTheVerdict(t *testing.T) {
	table := DeriveCarrierWindows()
	window := int(caFloat(t, table, "gf_event_window_days"))
	n := 30
	for _, age := range []int{0, window - 1, window, window + 1} {
		kl := shortBars(n, 50, 50, 10)
		idx := n - 1 - age
		in := &ScanInput{News: map[string][]NewsBrief{"600000.SH": {{
			Title: "利好", Positive: true, Score: 0.5, Level: "个股",
			Time: kl[idx].Date.Format("2006-01-02 15:04:05"),
		}}}}
		sd := buildShortData("600000.SH", stubMarketData(kl, 50), nil, in, "", false, 0)
		want := age <= window
		if sd.EventInWindow != want {
			t.Errorf("事件年龄 %d 日（在窗 %d）应 %v，实为 %v", age, window, want, sd.EventInWindow)
		}
		if want && sd.EventAgeDays != age {
			t.Errorf("事件年龄应记 %d，实为 %d", age, sd.EventAgeDays)
		}
	}
}
