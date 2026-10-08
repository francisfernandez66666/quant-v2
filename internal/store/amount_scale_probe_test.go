// amount_scale_probe_test.go — §0929SCALE-⑩ 落库后量纲抽检的行为锁。
// 五种结论各一条（ok / thousand-yuan / over-scaled / mixed / no-data），
// 并按"摘掉判据必红"的口径写：喂千元数据必须判红、喂元数据必须判绿（等值锁，非单向锁）。
//
// §W7-D（2026-10-09 波 7）追加 TestProbeAmountScaleThsDaily：ths_daily 进抽检集合后，
// 与 daily 的读数必须逐字段相等（同一把尺子）、倍率参数必须是承重件（改 1 就判红）、
// 空表回 no-data 而不是报错（这条同时钉住 daily 侧同族的一处自伤）。
package store

import (
	"path/filepath"
	"testing"
)

// newScaleDB 建一个带 daily 表的临时库，rows 逐行写入（ts_code/trade_date/close/vol/amount）。
// 不走 dataload，直接写 SQL：本测试要证的只是"读数判定"，掺进装载路径会把两个变量耦在一起。
func newScaleDB(t *testing.T, date string, rows [][3]float64, codes ...string) *DB {
	t.Helper()
	return newScaleDBOn(t, "daily", date, rows, codes...)
}

// newScaleDBOn 与 newScaleDB 同一构造，只是目标表可换（§W7-D 需要 ths_daily 的同款样本）。
// 两张表的列名在这套夹具里一致（ts_code/trade_date/close/vol/amount），
// 因此夹具共用一份——夹具分家的话，"同一把尺子"就退化成"两套各测各的"。
func newScaleDBOn(t *testing.T, table, date string, rows [][3]float64, codes ...string) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for i, r := range rows {
		code := codes[i]
		// 列：close / vol(手) / amount。均价 = amount/(vol*倍率)。
		if _, err := db.db.Exec(`INSERT INTO `+table+`(ts_code,trade_date,close,vol,amount) VALUES(?,?,?,?,?)`,
			code, date, r[0], r[1], r[2]); err != nil {
			t.Fatalf("insert %s %s: %v", table, code, err)
		}
	}
	return db
}

// scaleRows 生成 n 行同均价的样本（代码从 base 起编号，避免同一 (ts_code,trade_date) 撞唯一键）。
// 均价由 amount/(vol*100) 反推，vol 固定 2e5 手：factor=1 得元口径（均价 10 元）、
// factor=0.001 得千元口径（均价 0.01 元）、factor=1000 得双重换算（均价 1e4 元）。
func scaleRows(base, n int, factor float64) ([][3]float64, []string) {
	const vol = 200000.0
	const cnyAmount = vol * 100 * 10 // 均价 10 元 ⇒ 2e8 元
	rows := make([][3]float64, 0, n)
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, [3]float64{10.0, vol, cnyAmount * factor})
		codes = append(codes, codeAt(base+i))
	}
	return rows, codes
}

// codeAt 生成确定且互不相同的 ts_code（升序取样下保证每行都在样本窗口内）。
func codeAt(i int) string {
	return "60" + pad4(1+i) + ".SH"
}

// pad4 左补零到四位，配合 codeAt 保证字典序稳定。
func pad4(n int) string {
	d := [4]byte{'0', '0', '0', '0'}
	for i := 3; i >= 0 && n > 0; i-- {
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[:])
}

// TestProbeDailyAmountScale 四态判定：元口径绿、千元红、双重换算红、空表不误报。
func TestProbeDailyAmountScale(t *testing.T) {
	const date = "20260918"

	t.Run("元口径样本判绿", func(t *testing.T) {
		rows, codes := scaleRows(0, 20, 1)
		db := newScaleDB(t, date, rows, codes...)
		p, err := db.ProbeDailyAmountScale(date, 0)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if p.Verdict != AmountScaleOK || p.Red() {
			t.Fatalf("verdict=%s red=%v，期望 ok/绿（median=%.4f low=%d）", p.Verdict, p.Red(), p.MedianRatio, p.Low)
		}
		if p.Rows != 20 || p.Normal != 20 {
			t.Errorf("取样计数 rows=%d normal=%d，期望 20/20", p.Rows, p.Normal)
		}
	})

	t.Run("千元口径样本必须判红", func(t *testing.T) {
		// 反证支：这就是 tushare 未经写侧归一直接落库的形态，摘掉判定它就成了"数据正常"。
		rows, codes := scaleRows(0, 20, 0.001)
		db := newScaleDB(t, date, rows, codes...)
		p, err := db.ProbeDailyAmountScale(date, 0)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if p.Verdict != AmountScaleThousand || !p.Red() {
			t.Fatalf("verdict=%s red=%v，期望 thousand-yuan/红（median=%.6f）", p.Verdict, p.Red(), p.MedianRatio)
		}
		if p.Reason == "" {
			t.Error("判红未带理由文案，日志将无法定位")
		}
	})

	t.Run("重复换算判红", func(t *testing.T) {
		// 均价 1e4 元：只有把元再乘一次 1000 才会这样（双重归一形态）。
		rows, codes := scaleRows(0, 20, 1000)
		db := newScaleDB(t, date, rows, codes...)
		p, err := db.ProbeDailyAmountScale(date, 0)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if p.Verdict != AmountScaleOver || !p.Red() {
			t.Fatalf("verdict=%s red=%v，期望 over-scaled/红（median=%.1f）", p.Verdict, p.Red(), p.MedianRatio)
		}
	})

	t.Run("空表不误报只报无样本", func(t *testing.T) {
		db := newScaleDB(t, date, nil)
		p, err := db.ProbeDailyAmountScale(date, 0)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if p.Verdict != AmountScaleNoData || p.Red() {
			t.Errorf("verdict=%s red=%v，期望 no-data/不判红（新鲜度问题由别的腿报）", p.Verdict, p.Red())
		}
	})

	t.Run("空表且不指定日期仍是 no-data 不是报错", func(t *testing.T) {
		// §W7-D 实测锤出的自伤形态：取最近交易日用 MAX(trade_date)，空表回 NULL，
		// 旧实现 Scan 进 string ⇒ "converting NULL to string is unsupported"，
		// 于是 amount-check 独立腿退 2（读取失败）、第 30 探针把"表还没装"判成红。
		// 这一条不红（即回 no-data）才是那处修复的证据；两张表同一条腿各测一次。
		db := newScaleDB(t, date, nil)
		p, err := db.ProbeDailyAmountScale("", 0)
		if err != nil {
			t.Fatalf("空库取最近交易日不该报错：%v", err)
		}
		if p.Verdict != AmountScaleNoData || p.Red() {
			t.Errorf("verdict=%s red=%v，期望 no-data/不判红", p.Verdict, p.Red())
		}
	})

	t.Run("取样上限生效", func(t *testing.T) {
		rows, codes := scaleRows(0, 50, 1)
		db := newScaleDB(t, date, rows, codes...)
		p, err := db.ProbeDailyAmountScale(date, 10)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if p.Rows != 10 {
			t.Errorf("rows=%d，期望按 maxRows=10 截断（取样确定性直接影响门禁可复跑）", p.Rows)
		}
	})
}

// TestProbeDailyAmountScaleMixed 混源判定：多数行正常、≥5% 呈千元形态 ⇒ mixed（标量系数救不回来）。
// 这条是"整池股票被静默剔光"最阴的形态：中位数看着正常，逐票阈值却有一半在错口径上比。
func TestProbeDailyAmountScaleMixed(t *testing.T) {
	const date = "20260918"
	rows, codes := scaleRows(0, 19, 1)
	low, lowCodes := scaleRows(19, 1, 0.001)
	rows = append(rows, low...)
	codes = append(codes, lowCodes...)
	db := newScaleDB(t, date, rows, codes...)
	// 千元行按 ts_code 升序要落在取样窗口内：这里 20 行全取，位置不影响判定。
	p, err := db.ProbeDailyAmountScale(date, 0)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if p.Verdict != AmountScaleMixed || !p.Red() {
		t.Fatalf("verdict=%s red=%v，期望 mixed/红（low=%d rows=%d median=%.4f）", p.Verdict, p.Red(), p.Low, p.Rows, p.MedianRatio)
	}
	// 反向自证：混源比例低于阈值（20 行里 0 行千元）必须判绿，否则 mixed 锁是"结构性必红"。
	rows2, codes2 := scaleRows(0, 20, 1)
	db2 := newScaleDB(t, date, rows2, codes2...)
	p2, err := db2.ProbeDailyAmountScale(date, 0)
	if err != nil {
		t.Fatalf("probe2: %v", err)
	}
	if p2.Verdict != AmountScaleOK {
		t.Errorf("对照组 verdict=%s，期望 ok（证明 mixed 阈值真的在数占比，而非恒红）", p2.Verdict)
	}
}

// TestProbeAmountScaleThsDaily §W7-D：ths_daily 进抽检集合后的四件事——
// ① 与 daily **同一把尺子**（同一样本喂两张表，读数必须逐字段相等）；
// ② 千元形态照样判红（这条就是 THS dump turnover 口径若为千元/换手率时的现形形态）；
// ③ 表为空只报 no-data（现网 ths_daily 可能整表未装载，不许冒充"量纲错了"）；
// ④ 白名单外的表名一律报错（它要拼进 SQL，是标识符白名单而不是清洗问题）。
// English: §W7-D — ths_daily joins the shared caliber probe; same ruler, same verdicts.
func TestProbeAmountScaleThsDaily(t *testing.T) {
	const date = "20260918"

	t.Run("同一样本两张表读数逐字段相等", func(t *testing.T) {
		// 这是"同口径"的等值断言，不是"ths_daily 也有一条测试"的在场断言：
		// 如果实现里给新表另写一套带宽或另一种取样规则，这里会先分叉。
		rows, codes := scaleRows(0, 20, 1)
		daily := newScaleDBOn(t, "daily", date, rows, codes...)
		ths := newScaleDBOn(t, "ths_daily", date, rows, codes...)
		pd, err := daily.ProbeAmountScale("daily", date, 0)
		if err != nil {
			t.Fatalf("daily probe: %v", err)
		}
		pt, err := ths.ProbeAmountScale("ths_daily", date, 0)
		if err != nil {
			t.Fatalf("ths_daily probe: %v", err)
		}
		if pt.Verdict != pd.Verdict || pt.Rows != pd.Rows || pt.Normal != pd.Normal ||
			pt.Low != pd.Low || pt.High != pd.High || pt.MedianRatio != pd.MedianRatio {
			t.Fatalf("同一样本读数分叉 daily=%+v ths=%+v（两张表必须共用一把尺子）", pd, pt)
		}
		if pt.Table != "ths_daily" {
			t.Errorf("读数里的表名=%q，期望 ths_daily（JSON 输出给现网腿读，名字错＝探针拨错表）", pt.Table)
		}
		if pd.Table != "daily" {
			t.Errorf("daily 读数里的表名=%q，期望 daily", pd.Table)
		}
	})

	t.Run("vol 倍率是承重件不是装饰", func(t *testing.T) {
		// 反证支（本枚独有）：把 ths_daily 的倍率从 100 改成 1（＝把"手"当成"股"），
		// 同一样本的均价会被抬 100 倍并越过 500 元上带 ⇒ 判红。
		// 摘掉倍率参数（写死 100 或写死 1）时，上面那条"逐字段相等"照样绿，
		// 只有这一条能证明 AmountProbedTables 的值真的进了 SQL。
		rows, codes := scaleRows(0, 20, 1)
		ths := newScaleDBOn(t, "ths_daily", date, rows, codes...)
		const want = "ths_daily"
		old, ok := AmountProbedTables[want]
		if !ok || old != 100.0 {
			t.Fatalf("AmountProbedTables[%s]=%v（期望 100＝vol 落库口径是手）", want, old)
		}
		AmountProbedTables[want] = 1.0
		defer func() { AmountProbedTables[want] = old }()
		p, err := ths.ProbeAmountScale(want, date, 0)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if p.Verdict != AmountScaleOver || !p.Red() {
			t.Fatalf("倍率改 1 后 verdict=%s red=%v，期望 over-scaled/红（median=%.1f）——"+
				"这一条不红说明倍率参数根本没被用上", p.Verdict, p.Red(), p.MedianRatio)
		}
	})

	t.Run("千元形态判红", func(t *testing.T) {
		rows, codes := scaleRows(0, 20, 0.001)
		ths := newScaleDBOn(t, "ths_daily", date, rows, codes...)
		p, err := ths.ProbeAmountScale("ths_daily", date, 0)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if p.Verdict != AmountScaleThousand || !p.Red() {
			t.Fatalf("verdict=%s red=%v，期望 thousand-yuan/红", p.Verdict, p.Red())
		}
		if p.Reason == "" {
			t.Error("判红未带理由文案")
		}
	})

	t.Run("空表只报无样本", func(t *testing.T) {
		ths := newScaleDBOn(t, "ths_daily", date, nil)
		p, err := ths.ProbeAmountScale("ths_daily", "", 0)
		if err != nil {
			t.Fatalf("probe: %v", err)
		}
		if p.Verdict != AmountScaleNoData || p.Red() {
			t.Fatalf("verdict=%s red=%v，期望 no-data/不判红", p.Verdict, p.Red())
		}
	})

	t.Run("白名单外表名报错且不落到 SQL", func(t *testing.T) {
		ths := newScaleDBOn(t, "ths_daily", date, nil)
		for _, bad := range []string{"", "DAILY", "sqlite_master", "daily; DROP TABLE daily"} {
			p, err := ths.ProbeAmountScale(bad, date, 0)
			if err == nil {
				t.Fatalf("表名 %q 竟然放行（表名会拼进 SQL，只能是 AmountProbedTables 的键）", bad)
			}
			if p.Verdict != AmountScaleNoData {
				t.Errorf("表名 %q 拒绝时的 verdict=%q，期望 no-data（拒读不许冒充读数）", bad, p.Verdict)
			}
		}
		// 反向自证：白名单内的表名必须放行，否则上面的循环是"恒红"的结构性能量。
		if _, err := ths.ProbeAmountScale("daily", date, 0); err != nil {
			t.Errorf("白名单内表名被拒：%v", err)
		}
	})

	t.Run("换算系数按表走", func(t *testing.T) {
		// AmountCaliberFactorFor("ths_daily") 与 daily 同一取向：整日千元才给 1000。
		// 但这条腿在 §W7-D 里只用于**读数**，装载侧不许拿它去乘（见 data.AmountScaledTables 注释）。
		rows, codes := scaleRows(0, 20, 0.001)
		ths := newScaleDBOn(t, "ths_daily", date, rows, codes...)
		f, p, err := ths.AmountCaliberFactorFor("ths_daily", date)
		if err != nil {
			t.Fatalf("factor: %v", err)
		}
		if f != TushareThousandToCNY || p.Verdict != AmountScaleThousand {
			t.Fatalf("factor=%v verdict=%s，期望 %v/thousand-yuan", f, p.Verdict, TushareThousandToCNY)
		}
		// 等值锁的另一半：daily 的同名包装必须与 For("daily") 完全同读数，
		// 否则"保留旧名字"就成了第二把尺子。
		d := newScaleDBOn(t, "daily", date, rows, codes...)
		wf, wp, err := d.AmountCaliberFactor(date)
		if err != nil {
			t.Fatalf("legacy factor: %v", err)
		}
		if wf != f || wp.Verdict != p.Verdict || wp.Table != "daily" {
			t.Fatalf("AmountCaliberFactor 与 For(\"daily\") 分叉 %v/%s vs %v/%s", wf, wp.Verdict, f, p.Verdict)
		}
	})
}

// TestAmountCaliberFactor 换算系数只认"整日千元"这一种可标量修复的形态。
func TestAmountCaliberFactor(t *testing.T) {
	const date = "20260918"

	t.Run("元口径系数为 1", func(t *testing.T) {
		rows, codes := scaleRows(0, 20, 1)
		db := newScaleDB(t, date, rows, codes...)
		f, p, err := db.AmountCaliberFactor(date)
		if err != nil {
			t.Fatalf("factor: %v", err)
		}
		if f != 1 || p.Verdict != AmountScaleOK {
			t.Fatalf("factor=%v verdict=%s，期望 1/ok", f, p.Verdict)
		}
	})

	t.Run("千元口径系数为 1000", func(t *testing.T) {
		rows, codes := scaleRows(0, 20, 0.001)
		db := newScaleDB(t, date, rows, codes...)
		f, p, err := db.AmountCaliberFactor(date)
		if err != nil {
			t.Fatalf("factor: %v", err)
		}
		if f != TushareThousandToCNY || p.Verdict != AmountScaleThousand {
			t.Fatalf("factor=%v verdict=%s，期望 %v/thousand-yuan", f, p.Verdict, TushareThousandToCNY)
		}
	})

	t.Run("混源不猜系数", func(t *testing.T) {
		rows, codes := scaleRows(0, 19, 1)
		low, lowCodes := scaleRows(19, 1, 0.001)
		rows = append(rows, low...)
		codes = append(codes, lowCodes...)
		db := newScaleDB(t, date, rows, codes...)
		f, p, err := db.AmountCaliberFactor(date)
		if err != nil {
			t.Fatalf("factor: %v", err)
		}
		// 混源返回 1（宁可按现状判并报警），但结论必须留痕，调用侧据此打 WARN。
		if f != 1 || p.Verdict != AmountScaleMixed {
			t.Fatalf("factor=%v verdict=%s，期望 1/mixed（混源禁止用标量悄悄修复）", f, p.Verdict)
		}
	})
}
