// tcalib_test.go — §T-COST-CALIB B2 的三条锁（确定性 / 窗口不重叠 / 样本数门槛）。
//
// 为什么 B2 不进 verify_changes.sh 而把锁放在这里：它是**离线只读工具**，
// 不参与任何交易判定；把它的判据塞进门禁脚本只会让"上线验收"变成跑一次标定。
// §8 的表格写的就是"不进 verify_changes，离线工具内置确定性/窗口/样本数三锁"。
//
// English: B2 locks live here (not in verify_changes.sh) because the tool is offline/read-only.
package tcalib

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// fakeBars 造一天 48 根 5 分钟根（09:35–11:30、13:05–15:00），价格按 walk 回调给。
// 造 48 根是刻意的：与实盘 CalcMACD 喂的"当日最近 48 根"同量级，窗口统计才有代表性。
// English: fakeBars builds one day of 48 five-minute bars.
func fakeBars(day string, base float64, walk func(i int) float64) DayBars {
	slots := make([]string, 0, 48)
	// 上午 09:35–11:30，每 5 分钟一根，共 24 根。
	for h, m := 9, 35; h < 11 || (h == 11 && m <= 30); {
		slots = append(slots, fmt.Sprintf("%02d:%02d", h, m))
		m += 5
		if m >= 60 {
			h, m = h+1, m-60
		}
	}
	// 下午 13:05–15:00，共 24 根。
	for h, m := 13, 5; h < 15 || (h == 15 && m == 0); {
		slots = append(slots, fmt.Sprintf("%02d:%02d", h, m))
		m += 5
		if m >= 60 {
			h, m = h+1, m-60
		}
	}
	bars := make([]Bar, 0, len(slots))
	for i, hm := range slots {
		px := base * (1 + walk(i))
		bars = append(bars, Bar{
			Ts: day + " " + hm + ":00", Open: px, High: px * 1.002, Low: px * 0.998,
			Close: px, Vol: 10000, Amount: px * 10000,
		})
	}
	return DayBars{Day: day, Bars: bars}
}

// tradingDays 造 n 个连续交易日（跳过周末；日期字符串升序即可，不必是真日历）。
// English: tradingDays builds n sequential weekday date strings.
func tradingDays(start time.Time, n int) []string {
	out := make([]string, 0, n)
	d := start
	for len(out) < n {
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			out = append(out, d.Format("2006-01-02"))
		}
		d = d.AddDate(0, 0, 1)
	}
	return out
}

// synthCode 造一只票的样本日：价格用确定性锯齿波（先跌后涨），
// 保证每天都有一个可捕获的单波段，且不同票的相位不同（分档才有差异）。
// English: synthCode builds one code's sample days with a deterministic sawtooth.
func synthCode(code string, days []string, base float64, phase float64) CodeDays {
	cd := CodeDays{Code: code}
	for di, day := range days {
		daySeed := float64(di)*0.37 + phase
		cd.Days = append(cd.Days, fakeBars(day, base, func(i int) float64 {
			// 前半段下跌、后半段上涨：单波段价差约 3%，够覆盖任何合理成本门槛。
			x := float64(i) / 47.0
			return 0.03*(x-0.5)*(x-0.5) - 0.015 + 0.001*math.Sin(daySeed+float64(i))
		}))
	}
	return cd
}

// TestPercentileNearestRank 最近秩分位：确定性、无插值、空样本返回 0。
// English: nearest-rank percentile is deterministic and interpolation-free.
func TestPercentileNearestRank(t *testing.T) {
	vals := []float64{5, 1, 4, 2, 3}
	if got := percentile(vals, 0); got != 1 {
		t.Fatalf("P0 应为最小值 1，got %v", got)
	}
	if got := percentile(vals, 100); got != 5 {
		t.Fatalf("P100 应为最大值 5，got %v", got)
	}
	if got := percentile(vals, 50); got != 3 {
		t.Fatalf("P50（最近秩，n=5）应为 3，got %v", got)
	}
	if got := percentile(nil, 70); got != 0 {
		t.Fatalf("空样本应返回 0，got %v", got)
	}
	// NaN 必须被剔除：留着它会把整条分位链算成 NaN 再静默写进报告。
	if got := percentile([]float64{math.NaN(), 2, 4}, 50); got != 2 && got != 4 {
		t.Fatalf("NaN 应被剔除，got %v", got)
	}
}

// TestTwoRoundsNeverReusesSegment 反证的核心：单调上涨的一天只能算 1 轮。
//
// 若实现允许两段重叠（例如两次都从最低点起算），这段数据会算出 r2≈r1，
// 于是"第 2 轮还有净价差"永远成立 —— 这正是必须用不重叠贪心的原因。
// English: counter-proof — a monotone day may only yield one round; overlap would fake round 2.
func TestTwoRoundsNeverReusesSegment(t *testing.T) {
	closes := make([]float64, 0, 48)
	for i := 0; i < 48; i++ {
		closes = append(closes, 10+float64(i)*0.02) // 单调上涨，只有一段
	}
	r1, r2 := twoRounds(closes)
	if r1 <= 0 {
		t.Fatalf("单调上涨应识别出正价差，got r1=%v", r1)
	}
	if r2 != 0 {
		t.Fatalf("单调上涨不该有第 2 轮（重叠实现会得到 r2=%v）", r2)
	}
}

// TestTwoRoundsSplitsDisjoint 两段互不重叠的行情（大涨 → 回落 → 再涨）应识别出两轮。
// 第 1 轮取全窗口最大波段 10→12（20%）；第 2 轮只能在卖出点之后的 [10,10.5,11.5] 里取 15%。
// English: two disjoint swings must be captured as two rounds.
func TestTwoRoundsSplitsDisjoint(t *testing.T) {
	closes := []float64{10, 11, 12, 10, 10.5, 11.5}
	r1, r2 := twoRounds(closes)
	if math.Abs(r1-20.0) > 0.01 { // (12−10)/10 = 20%
		t.Fatalf("第 1 轮应取最大波段 10→12（20%%），got %v", r1)
	}
	if math.Abs(r2-15.0) > 0.01 { // (11.5−10)/10 = 15%
		t.Fatalf("第 2 轮应取卖出点之后的 10→11.5（15%%），got %v", r2)
	}
}

// TestCoverageExcludesShortCodes 步骤 0：天数不足的票要**列出来**，不是静默丢。
// English: step 0 lists ineligible codes instead of dropping them silently.
func TestCoverageExcludesShortCodes(t *testing.T) {
	days := tradingDays(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), 80)
	good := synthCode("600000.SH", days, 10, 0.1)
	short := synthCode("600001.SH", days[:30], 10, 0.2)
	sum := Coverage([]CodeDays{good, short}, 60)
	if sum.CodesTotal != 2 || sum.CodesEligible != 1 {
		t.Fatalf("应 2 票里 1 票合格，got total=%d eligible=%d", sum.CodesTotal, sum.CodesEligible)
	}
	if len(sum.Excluded) != 1 || sum.Excluded[0].Code != "600001.SH" {
		t.Fatalf("不合格票必须逐条列出，got %+v", sum.Excluded)
	}
	if sum.Excluded[0].Reason == "" {
		t.Fatal("不合格必须带原因（差多少天），否则读者无法决定要不要回填")
	}
}

// TestSplitWindowsNonOverlapLock 窗口锁 + 反证。
//
// 反证腿：把验证窗起点改到标定窗终点（偷进一天）⇒ splitWindowsAt 必须报错。
// 这条锁是 B2 全部读数的前提——重叠一天，"样本外"就已经不是样本外了。
// English: the non-overlap lock, plus the counter-proof that overlapping must be rejected.
func TestSplitWindowsNonOverlapLock(t *testing.T) {
	days := tradingDays(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), 90)
	win, err := SplitWindows(days, 60, 11)
	if err != nil {
		t.Fatalf("90 个交易日足够 60+11，不该报错: %v", err)
	}
	if win.CalibN != 60 || win.OOSN != 11 {
		t.Fatalf("窗口长度应为 60/11，got %d/%d", win.CalibN, win.OOSN)
	}
	if win.OOSFrom <= win.CalibTo {
		t.Fatalf("验证窗起点必须严格晚于标定窗终点：%s vs %s", win.OOSFrom, win.CalibTo)
	}
	// 反证：故意重叠一天（oosFrom = calibTo）。
	if _, err := splitWindowsAt(days, 59, 59); err == nil {
		t.Fatal("验证窗起点 == 标定窗终点时必须判红（重叠锁失效）")
	}
	// 样本不足：只给 70 天却要 60+11 ⇒ 必须报 ErrShortHistory。
	if _, err := SplitWindows(days[:70], 60, 11); err == nil {
		t.Fatal("样本日不足时必须报 ErrShortHistory，不许静默缩短窗口")
	}
}

// TestCalibrateDeterministic 确定性锁：同输入两次跑，JSON 与 Markdown 都必须逐字节相等。
// English: determinism lock — two runs over the same input must be byte-identical.
func TestCalibrateDeterministic(t *testing.T) {
	days := tradingDays(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), 85)
	codes := []CodeDays{
		synthCode("600000.SH", days, 10, 0.1),
		synthCode("600001.SH", days, 20, 0.7),
		synthCode("600002.SH", days, 40, 1.3),
		synthCode("600003.SH", days, 8, 2.1),
	}
	p := Params{MinDays: 60, CalibDays: 60, OOSDays: 11, SlipBps: 5}
	r1, err := Calibrate(codes, p)
	if err != nil {
		t.Fatalf("标定失败: %v", err)
	}
	r2, err := Calibrate(codes, p)
	if err != nil {
		t.Fatalf("标定失败: %v", err)
	}
	b1, err := r1.JSON()
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	b2, _ := r2.JSON()
	if string(b1) != string(b2) {
		t.Fatal("两次跑的 JSON 必须逐字节一致（确定性锁）；差异通常来自读时钟或遍历 map")
	}
	if r1.Markdown() != r2.Markdown() {
		t.Fatal("两次跑的 Markdown 必须逐字节一致")
	}
	// 入参顺序也不能影响结果：反过来喂一遍必须得到同样的字节。
	rev := []CodeDays{codes[3], codes[2], codes[1], codes[0]}
	r3, err := Calibrate(rev, p)
	if err != nil {
		t.Fatalf("标定失败: %v", err)
	}
	b3, _ := r3.JSON()
	if string(b1) != string(b3) {
		t.Fatal("票的入参顺序不得影响结果（否则产物不可复现）")
	}
}

// TestCalibrateSampleFloorBlocksReplacement 样本数门槛锁：样本外不足 ⇒ 该档必须标"不替换"。
//
// 反证腿：把 MinSample 降到 1，同一批数据就必须变成可替换 —— 说明拦截确实来自门槛本身，
// 而不是这段代码恰好返回 false。
// English: the sample-count floor, with a counter-proof that lowering it flips the verdict.
func TestCalibrateSampleFloorBlocksReplacement(t *testing.T) {
	days := tradingDays(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), 30)
	codes := []CodeDays{
		synthCode("600000.SH", days, 10, 0.1),
		synthCode("600001.SH", days, 20, 0.7),
	}
	// 标定窗 12 天 × 2 票 = 24 样本、样本外 4 天 × 2 票 = 8 样本，都远低于 30。
	p := Params{MinDays: 10, CalibDays: 12, OOSDays: 4, MinSample: 30, SlipBps: 5}
	rep, err := Calibrate(codes, p)
	if err != nil {
		t.Fatalf("标定失败: %v", err)
	}
	if len(rep.Tiers) == 0 {
		t.Fatal("应有至少一档（全部不足时合并成单档）")
	}
	for _, st := range rep.Tiers {
		if st.Replaced {
			t.Fatalf("样本外 %d < 30 时不许替换，got Replaced=true（T%d）", st.OOSN, st.Tier)
		}
		if st.Note == "" {
			t.Fatalf("不替换必须写明原因（T%d 的 Note 为空）", st.Tier)
		}
	}
	if len(rep.MergeNotes) == 0 {
		t.Fatal("档位被合并时必须写明合并原因")
	}

	// 反证：门槛降到 1，同一批数据必须转为可替换。
	p2 := p
	p2.MinSample = 1
	rep2, err := Calibrate(codes, p2)
	if err != nil {
		t.Fatalf("标定失败: %v", err)
	}
	replaced := 0
	for _, st := range rep2.Tiers {
		if st.Replaced {
			replaced++
		}
	}
	if replaced == 0 {
		t.Fatal("门槛降到 1 后必须至少有一档可替换（否则拦截不来自样本数门槛）")
	}
}

// TestCalibrateShortHistoryStillDelivers 数据不足时工具照常交付：报告带覆盖率、结论写"先回填"。
// English: a short history still yields a coverage-only report with a "backfill first" verdict.
func TestCalibrateShortHistoryStillDelivers(t *testing.T) {
	days := tradingDays(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), 40)
	rep, err := Calibrate([]CodeDays{synthCode("600000.SH", days, 10, 0.1)},
		Params{MinDays: 10, CalibDays: 60, OOSDays: 11})
	if err == nil {
		t.Fatal("40 天样本应报 ErrShortHistory（不许静默缩短窗口）")
	}
	if rep.Coverage.CodesTotal != 1 {
		t.Fatalf("即使窗口不足，覆盖率也必须照常产出，got %+v", rep.Coverage)
	}
	if rep.Verdict == "" {
		t.Fatal("结论不能为空：面板/脚本靠它决定下一步（回填 or 签字）")
	}
}

// TestMinSpreadIsDerivedFromCost 价差门槛是唯一"算出来"的量：必须严格等于成本×安全边际。
// 出厂值 2.5bp 佣金×2 + 5bp 滑点×2 + 5bp 印花 = 20bp = 0.20%，×1.3 = 0.26%。
// English: min_spread_pct must equal the cost × safety multiplier exactly.
func TestMinSpreadIsDerivedFromCost(t *testing.T) {
	p := Params{SlipBps: 5}.norm()
	if got := p.costPct(); math.Abs(got-0.20) > 1e-9 {
		t.Fatalf("回转成本应为 0.20%%，got %.6f", got)
	}
	if got := p.minSpreadPct(); math.Abs(got-0.26) > 1e-9 {
		t.Fatalf("价差门槛应为 0.26%%，got %.6f", got)
	}
	// 滑点未给（0）时门槛必须更低——这正是报告要提示"偏乐观"的原因。
	p0 := Params{}.norm()
	if p0.minSpreadPct() >= p.minSpreadPct() {
		t.Fatalf("滑点 0 的门槛应低于滑点 5bp 的门槛：%.6f vs %.6f", p0.minSpreadPct(), p.minSpreadPct())
	}
}
