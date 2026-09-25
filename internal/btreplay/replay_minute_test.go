// replay_minute_test.go — §MINUTE-K 动量回放换"真 5 分钟 MACD"的定点测试（2026-09-24）。
//
// 这次改动的全部风险都集中在一条边界上：**什么算"有分钟数据"**。
// 半日 12 根算出来的 DIF/DEA 与实盘那 48 根不是同一个东西——如果根数不足时仍然"用分钟口径"，
// 报告上看不出来，量出来的却是一个从没声明过的第三种近似（比原来"日线顶替"更糟，因为它
// 看起来像升级）。所以这里按四类钉：
//  1. 根数门槛：≥48 根才可用；47 根一律报不可用（退回**已声明**的日线近似）；
//  2. 尺寸同源：多于 48 根时只取**最近** 48 根（与实盘 fetchMinuteKLine count=48 一致），
//     且算出来的 MACD 与 data.CalcMACD(最近48根) 逐字相等（没有第二套口径）；
//  3. 退回路径真的退回：src 为 nil / 该股当日没回填 / 整表为空 → 分数与升级前的日线口径**完全相同**
//     （这才是"改了口径没有偷偷改变老结论"的证明）；命中时分数必须与日线口径不同（证明真的换了）；
//  4. 观测面：近似说明里必须带实测覆盖率（NONE / x/y 判档日），零判档日不得冒出 NaN。
//
// 外加零价根（停牌占位）不进窗口、裸代码经反查表还原成 ts_code 两条取数细节。
//
// English: pinned tests for swapping momentum replay onto real 5-minute MACD — the bar-count floor
// (47 bars is NOT an upgrade), the 48-bar window matching the live fetch, byte-equality with
// data.CalcMACD, the fallback path producing exactly the pre-upgrade daily score, and the coverage
// figure riding along in the approximation note.
package btreplay

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// seedMinuteDay 往临时库写某股某日的 count 根 5 分钟线（收盘价按 sine 起伏，保证 MACD
// 不是全零也不是"恰好相等"的巧合值）。
func seedMinuteDay(t *testing.T, db *store.DB, tsCode, day string, count int) {
	t.Helper()
	bars := make([]store.MinuteBar, 0, count)
	for i := 0; i < count; i++ {
		total := 9*60 + 35 + 5*i
		p := 10 + float64(i%7)*0.11 - float64(i%3)*0.07
		bars = append(bars, store.MinuteBar{TsCode: tsCode, Scale: 5,
			Ts:   fmt.Sprintf("%s %02d:%02d:00", day, total/60, total%60),
			Open: p, High: p * 1.002, Low: p * 0.998, Close: p, Vol: 2000, Amount: p * 2000})
	}
	if _, err := db.UpsertMinuteBars(bars); err != nil {
		t.Fatalf("seed 分钟线: %v", err)
	}
}

func newMinuteReplayDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestMinuteMACDRequiresFullWindow 根数门槛：不足 48 根一律不可用（半日数据不是升级）。
func TestMinuteMACDRequiresFullWindow(t *testing.T) {
	db := newMinuteReplayDB(t)
	seedMinuteDay(t, db, "600000.SH", "2026-09-24", 47) // 差一根
	seedMinuteDay(t, db, "600001.SH", "2026-09-24", 48) // 正好
	s := newStoreMinuteMACD(db, 5, []string{"600000.SH", "600001.SH"})
	if _, ok := s.MinuteMACDAt("600000.SH", "2026-09-24"); ok {
		t.Fatalf("47 根被当成可用＝拿半日序列冒充实盘 48 根口径（比日线近似更坏的静默降级）")
	}
	if m, ok := s.MinuteMACDAt("600001.SH", "2026-09-24"); !ok || m.DIF == 0 && m.DEA == 0 && m.Bar == 0 {
		t.Fatalf("48 根应可用且序列非零，得到 %+v ok=%v", m, ok)
	}
}

// TestMinuteMACDWindowIsLast48 多于 48 根时只取最近 48 根，且与 data.CalcMACD 逐字相等。
func TestMinuteMACDWindowIsLast48(t *testing.T) {
	db := newMinuteReplayDB(t)
	// 两天：前一天的根必须被日切片挡在外面（否则窗口是"最近 48 根跨了两日"）
	seedMinuteDay(t, db, "600000.SH", "2026-09-23", 48)
	seedMinuteDay(t, db, "600000.SH", "2026-09-24", 60)
	s := newStoreMinuteMACD(db, 5, nil)
	got, ok := s.MinuteMACDAt("600000.SH", "2026-09-24")
	if !ok {
		t.Fatal("60 根的交易日应可用")
	}
	bars, err := db.MinuteBarsByDay("600000.SH", 5, "2026-09-24")
	if err != nil {
		t.Fatalf("按日查询: %v", err)
	}
	want := data.CalcMACD(minuteKLForTest(bars[len(bars)-48:]))
	if got != want {
		t.Fatalf("分钟 MACD 与实盘同一函数（CalcMACD 最近 48 根）不等价： %+v vs %+v", got, want)
	}
	// 反证：用**前** 48 根算出的值必须不同（说明窗口确实截尾，不是随手取了头部）
	head := data.CalcMACD(minuteKLForTest(bars[:48]))
	if got == head {
		t.Fatalf("窗口截尾失效：截尾值与取头部值相同（样例数据退化，测不出差异）")
	}
}

func minuteKLForTest(bars []store.MinuteBar) []data.KLine {
	out := make([]data.KLine, 0, len(bars))
	for _, b := range bars {
		tt, _ := time.ParseInLocation("2006-01-02 15:04:05", b.Ts, time.UTC)
		out = append(out, data.KLine{Date: tt, Open: b.Open, High: b.High, Low: b.Low,
			Close: b.Close, Volume: b.Vol, Amount: b.Amount})
	}
	return out
}

// TestMinuteMACDZeroPriceBarsNotCounted 零价根（停牌/占位）不得凑进 48 根窗口：
// 凑进去＝把"有 48 行"当成"有 48 根有效数据"，EMA 会被零价拉出假跳水。
func TestMinuteMACDZeroPriceBarsNotCounted(t *testing.T) {
	db := newMinuteReplayDB(t)
	bars := make([]store.MinuteBar, 0, 48)
	for i := 0; i < 48; i++ {
		px := 10.0 + float64(i%5)*0.1
		if i >= 40 {
			px = 0 // 尾盘 8 根是占位零价
		}
		total := 9*60 + 35 + 5*i
		bars = append(bars, store.MinuteBar{TsCode: "600002.SH", Scale: 5,
			Ts: fmt.Sprintf("2026-09-24 %02d:%02d:00", total/60, total%60), Close: px, Vol: 1})
	}
	if _, err := db.UpsertMinuteBars(bars); err != nil {
		t.Fatalf("写入: %v", err)
	}
	s := newStoreMinuteMACD(db, 5, nil)
	if _, ok := s.MinuteMACDAt("600002.SH", "2026-09-24"); ok {
		t.Fatalf("有效根只有 40 根（<48）必须报不可用，零价根不能凑窗口")
	}
}

// TestMinuteMACDBareCodeResolves 回放内部到处用裸 6 位代码，分钟表主键是 ts_code——
// 反查表必须把 600000 还原成 600000.SH；还原不了时报不可用（不是报错、更不是拿别的票的数据）。
func TestMinuteMACDBareCodeResolves(t *testing.T) {
	db := newMinuteReplayDB(t)
	seedMinuteDay(t, db, "600000.SH", "2026-09-24", 48)
	s := newStoreMinuteMACD(db, 5, []string{"600000.SH"})
	if _, ok := s.MinuteMACDAt("600000", "2026-09-24"); !ok {
		t.Fatalf("裸代码应经反查表命中 600000.SH")
	}
	// 全仓 ts_code 写法也照样能用（装配点传的是哪一种都不得哑）
	if _, ok := s.MinuteMACDAt("600000.SH", "2026-09-24"); !ok {
		t.Fatalf("ts_code 形态直接查询失败")
	}
	if _, ok := s.MinuteMACDAt("600999", "2026-09-24"); ok {
		t.Fatalf("反查表里没有的代码不得凭空可用（跨股污染比没升级更糟）")
	}
}

// TestMinuteMACDCachedAndCounted 同 (股,日) 重复问只查一次库，且覆盖率计数按"问了几次/中了几次"走。
func TestMinuteMACDCachedAndCounted(t *testing.T) {
	db := newMinuteReplayDB(t)
	seedMinuteDay(t, db, "600000.SH", "2026-09-24", 48)
	s := newStoreMinuteMACD(db, 5, nil)
	for i := 0; i < 3; i++ {
		if _, ok := s.MinuteMACDAt("600000.SH", "2026-09-24"); !ok {
			t.Fatal("可用样例反复查询失败")
		}
	}
	s.MinuteMACDAt("600000.SH", "2026-09-22") // 没回填的日子
	looks, hits, pct := s.coverage()
	if looks != 4 || hits != 3 {
		t.Fatalf("观测计数应为 looks=4 hits=3，实得 %d/%d", looks, hits)
	}
	if pct < 74 || pct > 76 {
		t.Fatalf("覆盖率应为 75%%，实得 %.1f", pct)
	}
	// 缓存真的生效：4 次问只查过 2 次库（两个不同的 (股,日) 键各一次）
	if s.queries != 2 {
		t.Fatalf("同键重复问必须走缓存（queries 应=2，实得 %d ⇒ 每股判档日数 × 兄弟回查会把 SQL 打爆）", s.queries)
	}
	// 零判档日：pct 必须是 0 而不是 NaN（除零冒出来会污染出门文本）
	s2 := newStoreMinuteMACD(db, 5, nil)
	if l, h, p := s2.coverage(); l != 0 || h != 0 || p != 0 {
		t.Fatalf("未查询过的来源覆盖率应为 0/0/0，实得 %d/%d/%.3f", l, h, p)
	}
}

// spyMinuteSource 可编程的分钟口径来源（判档层只认接口，便于把"命中/未命中"两条路各钉一遍）。
type spyMinuteSource struct {
	macd data.MACD
	ok   bool
	asks []string
}

func (s *spyMinuteSource) MinuteMACDAt(tsCode, day string) (data.MACD, bool) {
	s.asks = append(s.asks, tsCode+"@"+day)
	return s.macd, s.ok
}

// TestMomentumScoreDayPrefersMinuteMACD 判档层三条路各钉一遍：
// 命中分钟 → 用分钟值；src 为 nil → 与"升级前的日线口径"逐字同分；未命中该股当日 → 同前。
func TestMomentumScoreDayPrefersMinuteMACD(t *testing.T) {
	ks := buildMomentumBars(60, 0.3, 1_000_000, 6, 2.2)
	prevClose := ks[len(ks)-2].Close
	cfg := config.MomentumConfig{}

	dailyOnly := &momentumAdapter{cfg: cfg}
	dailyOnly.prepareStock(ks)
	dailyOnly.setDay(len(ks) - 1)
	metaDaily, _ := dailyOnly.scoreDay(ks, prevClose)
	if metaDaily == nil {
		t.Fatal("日线口径样例应出分数")
	}
	wantDaily := data.CalcMACD(ks)

	t.Run("无分钟来源时与升级前逐字相同", func(t *testing.T) {
		ad := &momentumAdapter{cfg: cfg}
		ad.prepareStock(ks)
		ad.setDay(len(ks) - 1)
		meta, _ := ad.scoreDay(ks, prevClose)
		if meta["score"] != metaDaily["score"] {
			t.Fatalf("没有分钟数据时结论必须不变（老回放数字被悄悄改动的唯一征兆）：%.0f vs %.0f",
				meta["score"], metaDaily["score"])
		}
	})

	t.Run("分钟命中时改用分钟序列", func(t *testing.T) {
		// 分钟 MACD 给空头（与样例的日线多头相反）：分数必须掉下来 ⇒ 证明用的是分钟值
		spy := &spyMinuteSource{macd: data.MACD{DIF: -1, DEA: 1, Bar: -2}, ok: true}
		ad := &momentumAdapter{cfg: cfg}
		ad.prepareStock(ks)
		ad.setDay(len(ks) - 1)
		ad.setMinuteScope(spy, "600000.SH")
		meta, _ := ad.scoreDay(ks, prevClose)
		if meta == nil {
			t.Fatal("空头分钟 MACD 仍应出分数（MACD 分量只是三维之一）")
		}
		if meta["score"] >= metaDaily["score"] {
			t.Fatalf("分钟口径没被用上：日线多头 %.0f vs 分钟空头 %.0f", metaDaily["score"], meta["score"])
		}
		if len(spy.asks) != 1 {
			t.Fatalf("每判档日只应问来源一次，实得 %d 次", len(spy.asks))
		}
		// 日期必须是北京时间的判定日（末根日K 那一日）
		wantDay := cntime.DayOf(ks[len(ks)-1].Date)
		if spy.asks[0] != "600000.SH@"+wantDay {
			t.Fatalf("取值参数应为 (ts_code, 判定日)：%q vs %q", spy.asks[0], "600000.SH@"+wantDay)
		}
	})

	t.Run("分钟未命中时退回日线", func(t *testing.T) {
		spy := &spyMinuteSource{ok: false}
		ad := &momentumAdapter{cfg: cfg}
		ad.prepareStock(ks)
		ad.setDay(len(ks) - 1)
		ad.setMinuteScope(spy, "600000.SH")
		meta, _ := ad.scoreDay(ks, prevClose)
		if meta["score"] != metaDaily["score"] {
			t.Fatalf("未命中必须退回日线口径（同分），实得 %.0f vs %.0f", meta["score"], metaDaily["score"])
		}
	})

	// 参考值：与实盘打分函数直接喂分钟 MACD 的结果相等（判据本体只有一套）
	spy := &spyMinuteSource{macd: wantDaily, ok: true}
	ad := &momentumAdapter{cfg: cfg}
	ad.prepareStock(ks)
	ad.setDay(len(ks) - 1)
	ad.setMinuteScope(spy, "600000.SH")
	meta, _ := ad.scoreDay(ks, prevClose)
	dailyMeta, _ := dailyOnly.scoreDay(ks, prevClose)
	if meta["score"] != dailyMeta["score"] {
		t.Fatalf("喂同一份 MACD（分钟来源返回日线值）必须同分：%.0f vs %.0f", meta["score"], dailyMeta["score"])
	}
}

// TestApproxNoteCarriesCoverage 出门文本：静态近似说明（§87 锚点）必须还在，
// 且必须带上本轮实测覆盖率读数——"换了口径"和"没数据所以还是老数字"要能一眼区分。
func TestApproxNoteCarriesCoverage(t *testing.T) {
	db := newMinuteReplayDB(t)
	seedMinuteDay(t, db, "600000.SH", "2026-09-24", 48)

	// 未装配来源（表为空/旧库）
	if n := (&Options{}).approxNote("momentum"); !strings.Contains(n, "coverage: NONE") {
		t.Fatalf("没有分钟来源时说明必须写明 NONE，实得 %q", n)
	}
	if n := (&Options{}).approxNote("momentum"); !strings.Contains(n, "criteria rewritten to live semantics") {
		t.Fatalf("§87 锚点被摘：实盘语义重写的声明必须一直在说明里")
	}
	// 其它战法的说明不受影响
	if n := (&Options{}).approxNote("double_bump"); n != "" {
		t.Fatalf("非近似战法的说明应为空，实得 %q", n)
	}
	// 装配了来源：读数必须出现
	o := &Options{minuteSrc: newStoreMinuteMACD(db, 5, []string{"600000.SH"})}
	o.minuteSrc.MinuteMACDAt("600000.SH", "2026-09-24")
	o.minuteSrc.MinuteMACDAt("600000.SH", "2026-09-23")
	n := o.approxNote("momentum")
	if !strings.Contains(n, "1/2") {
		t.Fatalf("说明必须带 hits/looks 覆盖率读数，实得 %q", n)
	}
	if !strings.Contains(n, "50.0%") {
		t.Fatalf("说明必须带百分比读数，实得 %q", n)
	}
	if strings.Contains(n, "not replayed by default") {
		t.Fatalf("说明里不得复活与事实矛盾的旧措辞")
	}
}

// TestMinuteScopeOnlyHitsMomentum 装配点的可选注入：非动量适配器必须被跳过（不能因为
// 多了这条注入就在别的战法上生出新的取数路径）。
func TestMinuteScopeOnlyHitsMomentum(t *testing.T) {
	db := newMinuteReplayDB(t)
	seedMinuteDay(t, db, "600000.SH", "2026-09-24", 48)
	o := &Options{minuteSrc: newStoreMinuteMACD(db, 5, nil)}
	na := &nShapeAdapter{}
	o.applyMinuteScope(na, "600000.SH") // 不该 panic、也不该被认成动量
	if _, ok := interface{}(na).(minuteMACDScoped); ok {
		t.Fatalf("n_shape 不应实现 minuteMACDScoped（分钟口径只属于动量判据）")
	}
	mo := &momentumAdapter{}
	o.applyMinuteScope(mo, "600000.SH")
	if mo.minuteSrc == nil || mo.minuteCode != "600000.SH" {
		t.Fatalf("动量适配器没接上分钟来源：src=%v code=%q", mo.minuteSrc, mo.minuteCode)
	}
	// 来源未装配时清场：残留的上一只票的 code 会把跨股数据喂进判档
	o2 := &Options{}
	mo2 := &momentumAdapter{minuteCode: "000001.SZ"}
	o2.applyMinuteScope(mo2, "600000.SH")
	if mo2.minuteSrc != nil || mo2.minuteCode != "" {
		t.Fatalf("无来源时必须清空 scope，实得 src=%v code=%q", mo2.minuteSrc, mo2.minuteCode)
	}
}

// TestSimulateComboMinuteScopePerStock §0925EVE-W3-J（B6）冠军复核分钟口径不互相污染。
//
// 修前机制（注释留档）：applyMinuteScope 只有 replay.go 主循环与 sweep.go 触发预算循环两个
// 注入点，verifyChampion→simulateCombo 的循环不经过任何一个——动量适配器带着触发预算循环里
// map 迭代最后一只票的 minuteCode 做整库复核，所有股票的判档都读"同一只票"的分钟序列，
// storeMinuteMACD 的 (股,日) 缓存与 §MINUTE-K 覆盖率计数也全记在错票名下。
//
// 钉法：两只票（600000.SH / 000001.SZ）各回填 48 根分钟线，先手动把 scope 停在 600000.SH
// （复现"残留最后一只票"的修前入场态），再跑 simulateCombo。修前：分钟来源的缓存键只会出现
// 600000.SH|（000001 的判档全部串到 600000 的数据上）；修后：两只票各自的键都必须出现，
// 即每只票拿的是**自己**的分钟序列。
func TestSimulateComboMinuteScopePerStock(t *testing.T) {
	db := newMinuteReplayDB(t)
	ks := buildMomentumBars(32, 0.3, 1_000_000, 6, 2.2) // backtestStock 判定窗 i=29..len-2
	tsCodes := []string{"600000.SH", "000001.SZ"}
	for _, ts := range tsCodes {
		for i := 29; i < len(ks)-1; i++ {
			seedMinuteDay(t, db, ts, cntime.DayOf(ks[i].Date), 48)
		}
	}
	src := newStoreMinuteMACD(db, 5, tsCodes)
	o := &Options{minuteSrc: src}
	ad := &momentumAdapter{cfg: defaultMomentumCfg()}
	klines := map[string][]data.KLine{"600000": ks, "000001": ks}
	tsOfCode := map[string]string{"600000": "600000.SH", "000001": "000001.SZ"}

	// 修前入场态：scope 残留在"最后一只票"（600000.SH）上，复核循环自己不重注入。
	o.applyMinuteScope(ad, "600000.SH")

	simulateCombo(ad, "momentum", o, klines, tsOfCode, nil, 0, 0, 0, 0)

	// 主锁：000001.SZ 必须以自己的 ts_code 查过库——修前它的每一次判档都带着残留 code
	// 600000.SH，缓存键里根本不会出现 000001.SZ| 前缀（串台＝两只票共享同一份分钟读数）。
	var sawA, sawB, hitA, hitB bool
	for k, e := range src.cache {
		switch {
		case strings.HasPrefix(k, "600000.SH|"):
			sawA = true
			hitA = hitA || e.ok
		case strings.HasPrefix(k, "000001.SZ|"):
			sawB = true
			hitB = hitB || e.ok
		default:
			t.Fatalf("出现了未知股票的分钟缓存键 %q——复核读到了第三只票的序列？", k)
		}
	}
	if !sawA || !sawB {
		t.Fatalf("分钟口径串台未修复：缓存键应同时覆盖两只票（各自查各自的），实得 sawA=%v sawB=%v（修前只有残留票 600000.SH 的键）", sawA, sawB)
	}
	// 反证样例数据有效性：两只票的 48 根窗口都应命中（否则上面的"都出现"可能只是查了个空）。
	if !hitA || !hitB {
		t.Fatalf("seed 的分钟窗口应双双可用（样例失效则本锁测不出东西）：hitA=%v hitB=%v", hitA, hitB)
	}
	// §MINUTE-K 计数口径：queries＝唯一 (股,日) 键数，至少各票各一天（残留态串台时只会集中在一票）。
	_, _, queries, _ := src.coverageStats()
	if queries < 2 {
		t.Fatalf("覆盖率 queries 计数应分布在两只票上（≥2 个唯一键），实得 %d——计数仍被单票垄断", queries)
	}
}
