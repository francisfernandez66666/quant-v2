// amount_scale_probe_test.go — §0929SCALE-⑩ 落库后量纲抽检的行为锁。
// 五种结论各一条（ok / thousand-yuan / over-scaled / mixed / no-data），
// 并按"摘掉判据必红"的口径写：喂千元数据必须判红、喂元数据必须判绿（等值锁，非单向锁）。
package store

import (
	"path/filepath"
	"testing"
)

// newScaleDB 建一个带 daily 表的临时库，rows 逐行写入（ts_code/trade_date/close/vol/amount）。
// 不走 dataload，直接写 SQL：本测试要证的只是"读数判定"，掺进装载路径会把两个变量耦在一起。
func newScaleDB(t *testing.T, date string, rows [][3]float64, codes ...string) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for i, r := range rows {
		code := codes[i]
		// 列：close / vol(手) / amount。均价 = amount/(vol*100)。
		if _, err := db.db.Exec(`INSERT INTO daily(ts_code,trade_date,close,vol,amount) VALUES(?,?,?,?,?)`,
			code, date, r[0], r[1], r[2]); err != nil {
			t.Fatalf("insert %s: %v", code, err)
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
