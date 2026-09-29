// quality_caliber_test.go — §0929SCALE-⑩ 第三条腿：流动性质控阈值必须按元口径比，且与量纲判定等值。
//
// 危害形态（AUDIT P2-4 的原话）：MinAvgAmount 默认 3e7 按"元"写死，而它比的是 `AVG(amount)`。
// 库里若混进千元口径行，这一条会**把整池股票全部剔掉且不报任何错**——股票池突然空掉，
// 回测/动量判据都建立在空池上。本文件用四条子用例把这件事钉成一个等值闭环：
//
//	A 元口径 + 阈值 3e7 ⇒ 全过；
//	B 同一批数换成千元口径 + 同一阈值 ⇒ **仍然全过**（自校准生效，摘掉系数必红）；
//	C 千元口径 + 阈值抬到换算后仍不达标 ⇒ 全剔（证明阈值真的在比，B 不是"恒过"造出来的假绿）；
//	D 元口径 + 同样抬阈值 ⇒ 全剔（与 C 成对，两口径在阈值两侧行为一致）。
package store

import (
	"path/filepath"
	"testing"
)

// newQualityDB 建临时库：stocks 三只在市股票 + daily 各自同日一行（amount 由 factor 决定口径）。
// vol 固定 2e5 手、均价 10 元 ⇒ 元口径 amount=2e8（2 亿），千元口径 amount=2e5。
func newQualityDB(t *testing.T, date string, factor float64) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, c := range []string{"600001.SH", "600002.SH", "600003.SH"} {
		if _, err := db.db.Exec(`INSERT INTO stocks(ts_code,name,list_date) VALUES(?,?,?)`, c, "测试股", "20100101"); err != nil {
			t.Fatalf("stocks %s: %v", c, err)
		}
		if _, err := db.db.Exec(`INSERT INTO daily(ts_code,trade_date,close,vol,amount) VALUES(?,?,?,?,?)`,
			c, date, 10.0, 200000.0, 2e8*factor); err != nil {
			t.Fatalf("daily %s: %v", c, err)
		}
	}
	return db
}

// qualityScreen 只开流动性阈值，其余条件关闭（本测试只盯量纲，掺进 ST/亏损会把变量耦在一起）。
func qualityScreen(threshold float64, date string) StockScreen {
	return StockScreen{MinAvgAmount: threshold, WindowDays: 60, End: date}
}

// TestScreenedCodesAmountCaliber 量纲自校准的等值闭环（A/B/C/D 四条，见文件头）。
func TestScreenedCodesAmountCaliber(t *testing.T) {
	const date = "20260918"

	t.Run("A 元口径达阈值全过", func(t *testing.T) {
		db := newQualityDB(t, date, 1)
		codes, err := db.ScreenedCodes(qualityScreen(3e7, date))
		if err != nil {
			t.Fatalf("screen: %v", err)
		}
		if len(codes) != 3 {
			t.Fatalf("通过 %d 只，期望 3（元口径 2e8 ≥ 3e7）：%v", len(codes), codes)
		}
	})

	t.Run("B 千元口径经自校准同样全过", func(t *testing.T) {
		// 反证支：把 quality.go 里的 l.Amt*amountFactor 退回 l.Amt，这条必红（0 只 vs 3 只）。
		db := newQualityDB(t, date, 0.001)
		f, p, err := db.AmountCaliberFactor(date)
		if err != nil {
			t.Fatalf("factor: %v", err)
		}
		if f != TushareThousandToCNY || p.Verdict != AmountScaleThousand {
			t.Fatalf("量纲判定未走千元分支 factor=%v verdict=%s ⇒ B 的等值会在错误的分支上成立", f, p.Verdict)
		}
		codes, err := db.ScreenedCodes(qualityScreen(3e7, date))
		if err != nil {
			t.Fatalf("screen: %v", err)
		}
		if len(codes) != 3 {
			t.Fatalf("通过 %d 只，期望与 A 等值 3 只；千元口径未自校准就会整池剔光：%v", len(codes), codes)
		}
	})

	t.Run("C 千元口径阈值抬到换算后仍不达标必全剔", func(t *testing.T) {
		// 存在理由：B 若靠"阈值根本不生效"就能绿。这里把阈值抬到 2e8 之上（5e8），
		// 换算后的 2e8 依然不达标 ⇒ 必须 0 只，说明比较真的在按元口径进行。
		db := newQualityDB(t, date, 0.001)
		codes, err := db.ScreenedCodes(qualityScreen(5e8, date))
		if err != nil {
			t.Fatalf("screen: %v", err)
		}
		if len(codes) != 0 {
			t.Fatalf("通过 %d 只，期望 0（阈值在换算后仍在比）：%v", len(codes), codes)
		}
	})

	t.Run("D 元口径同阈值同样全剔", func(t *testing.T) {
		db := newQualityDB(t, date, 1)
		codes, err := db.ScreenedCodes(qualityScreen(5e8, date))
		if err != nil {
			t.Fatalf("screen: %v", err)
		}
		if len(codes) != 0 {
			t.Fatalf("通过 %d 只，期望与 C 等值 0 只：%v", len(codes), codes)
		}
	})
}
