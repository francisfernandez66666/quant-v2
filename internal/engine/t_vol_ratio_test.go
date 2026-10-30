// t_vol_ratio_test.go — §W-09 个股量能门读数的口径与缓存锁。
//
// H7/H9 钉"分母怎么算"：今日未完成 K 线必须剔除（否则当天放量越大，分母越大，反而测不出放量），
// 历史不足 20 根不许拿 10 根冒充。
// H8/H15 钉"上游打几次"：成功缓存 30 分钟（盘中 20 日均量不变），失败缓存 60 秒
// （一次抖动不能让该票半天做不了 T，也不能让源挂了以后每秒打上游）。
package engine

import (
	"errors"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// volBars 造 n 根日 K，最后一根落在 end（含），每根 volume 均为 vol。
func volBars(n int, end time.Time, vol float64) []data.KLine {
	out := make([]data.KLine, 0, n)
	for i := n - 1; i >= 0; i-- {
		d := time.Date(end.Year(), end.Month(), end.Day()-i, 0, 0, 0, 0, cntime.Loc)
		out = append(out, data.KLine{Date: d, Close: 10, Volume: vol})
	}
	return out
}

func volEngine(t *testing.T, now time.Time) *Engine {
	t.Helper()
	e := &Engine{tVolCache: newTVolCache()}
	e.SetClock(func() time.Time { return now })
	return e
}

// H7：今日未完成 K 线必须剔除——今日量取极大/极小两种，算出的量比必须逐分相等。
func TestVolRatioExcludesTodayBar(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 30, 0, 0, cntime.Loc) // 周五 10:30，已流逝 60 分钟
	ratio := func(todayVol float64) float64 {
		bars := volBars(24, now.AddDate(0, 0, -1), 1000) // 24 根历史，末根 = 昨天
		bars = append(bars, data.KLine{Date: now, Close: 10, Volume: todayVol})
		e := volEngine(t, now)
		e.tKLineFn = func(code, period string, n int) ([]data.KLine, error) { return bars, nil }
		e.tCumVolFn = func(string) (float64, bool) { return 1000, true }
		v, ok := e.tVolRatioOf("600000.SH")
		if !ok {
			t.Fatalf("量比应可算（今日量 %.0f）", todayVol)
		}
		return v
	}
	hi, lo := ratio(1e12), ratio(1)
	if hi != lo {
		t.Fatalf("今日 K 线必须被剔除：今日量极大/极小时量比应相等，实得 %.6f vs %.6f", hi, lo)
	}
	// 10:30 已流逝 60 分钟 ⇒ 折算系数 4；累计量 1000 / 均量 1000 ⇒ 4.0。
	if hi < 3.999 || hi > 4.001 {
		t.Fatalf("10:30 的折算量比应约 4.0（1000×240/60÷1000），实得 %.6f（分母被今日 K 线污染时不会是这个数）", hi)
	}
}

// H8：同 code 连调 5 次只打一次上游；跨交易日（日键变化）必须重新取。
func TestVolRatioCachesPerDay(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 30, 0, 0, cntime.Loc)
	e := volEngine(t, now)
	calls := 0
	e.tKLineFn = func(code, period string, n int) ([]data.KLine, error) {
		calls++
		return volBars(24, now.AddDate(0, 0, -1), 1000), nil
	}
	e.tCumVolFn = func(string) (float64, bool) { return 1000, true }
	for i := 0; i < 5; i++ {
		if _, ok := e.tVolRatioOf("600000.SH"); !ok {
			t.Fatalf("第 %d 次量比应可算", i+1)
		}
	}
	if calls != 1 {
		t.Fatalf("同一交易日内 5 次调用只许取 1 次日 K（否则 5s 循环逐票打上游），实得 %d", calls)
	}
	// 2026-10-31 是周六，TradingDayDate 会归到周五（同日键）⇒ 用下一个交易日 11-02。
	next := time.Date(2026, 11, 2, 10, 30, 0, 0, cntime.Loc)
	e.SetClock(func() time.Time { return next })
	if _, ok := e.tVolRatioOf("600000.SH"); !ok {
		t.Fatal("跨交易日后量比应可算")
	}
	if calls != 2 {
		t.Fatalf("跨交易日必须重新取日 K，实得调用 %d 次", calls)
	}
}

// H9：历史只有 10 根 ⇒ 不拿它冒充 20 日均量（ok=false）。
func TestVolRatioShortHistory(t *testing.T) {
	now := time.Date(2026, 10, 30, 10, 30, 0, 0, cntime.Loc)
	e := volEngine(t, now)
	e.tKLineFn = func(code, period string, n int) ([]data.KLine, error) {
		return volBars(10, now.AddDate(0, 0, -1), 1000), nil
	}
	e.tCumVolFn = func(string) (float64, bool) { return 1000, true }
	if v, ok := e.tVolRatioOf("600000.SH"); ok {
		t.Fatalf("10 根日 K 不足以算 20 日均量，应返回 ok=false，实得 %.2f/true", v)
	}
}

// H15：失败负缓存 60 秒——源失败一次后 60 秒内连调 5 次只打 1 次上游；第 61 秒重试。
func TestVolRatioNegativeCacheShort(t *testing.T) {
	base := time.Date(2026, 10, 30, 10, 30, 0, 0, cntime.Loc)
	now := base
	e := &Engine{tVolCache: newTVolCache()}
	e.SetClock(func() time.Time { return now })
	calls := 0
	e.tKLineFn = func(code, period string, n int) ([]data.KLine, error) {
		calls++
		return nil, errors.New("upstream down")
	}
	e.tCumVolFn = func(string) (float64, bool) { return 1000, true }
	for i := 0; i < 5; i++ {
		if _, ok := e.tVolRatioOf("600000.SH"); ok {
			t.Fatal("源失败时量比不许可算")
		}
		now = now.Add(10 * time.Second)
	}
	if calls != 1 {
		t.Fatalf("失败负缓存 60 秒内只许打 1 次上游（否则源挂了就是每秒 4 次），实得 %d", calls)
	}
	now = base.Add(61 * time.Second)
	if _, ok := e.tVolRatioOf("600000.SH"); ok {
		t.Fatal("源仍失败时量比不许可算")
	}
	if calls != 2 {
		t.Fatalf("第 61 秒应重试（失败只缓存 60 秒，不是 30 分钟），实得 %d", calls)
	}
}
