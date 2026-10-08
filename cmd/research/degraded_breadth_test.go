// degraded_breadth_test.go — §P2-F（2026-10-06 修复批 波 5）广度读数**不被降级行拖偏**的行为锁。
//
// 缺陷落点：dailyUpRatio 的分母原来是 COUNT(*)，降级腿写的行（旧口径 pct_chg 被折成 0＝平盘）
// 一并计入 ⇒ 断源日直接压低上涨家数占比，把市场情绪读数往弱势方向带；而"全市场都没涨"与
// "今天这列没读数"在旧形态下长得一模一样。
// 取向（四条各一枚断言，摘掉任何一条都会红）：
//
//	B1 混合日：分母只数有真实涨跌读数的行（2/3，而不是把降级两行算成平盘的 2/5）；
//	B2 全降级日：返回 nil 弃权（宁可不写这一天的广度，也不写一个"0% 上涨"的假读数）；
//	B3 双保险：来源列没落上（NULL 世代）但涨跌是 NULL 的行，仍被 pct_chg IS NOT NULL 挡在分母外；
//	B4 老世代不误伤：来源 NULL 且有真实涨跌读数的行照常计入（否则历史回放会凭空丢掉整段样本）。
//
// English: breadth reading must not be dragged by degraded rows — denominator counts only rows
// with a real pct_chg reading, a fully-degraded day abstains (nil), NULL-source legacy rows with
// real readings stay in the sample.
package main

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/store"
)

const breadthDate = "20260824"

// newBreadthDB 建临时库并按夹具写入 daily 行（source/pct_chg 由调用方逐行给定）。
func newBreadthDB(t *testing.T, rows []map[string]any) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), rows); err != nil {
		t.Fatalf("写日线失败: %v", err)
	}
	return db
}

func bar(code string, pct any, source any) map[string]any {
	return map[string]any{"ts_code": code, "trade_date": breadthDate, "close": 10.0,
		"pct_chg": pct, "source": source}
}

// B1 + B3 + B4 同批夹具：两涨一跌（真实读数）+ 两条降级行 + 一条"来源没落但涨跌也没落"的行。
func TestUpRatioDenominatorSkipsDegradedRows(t *testing.T) {
	db := newBreadthDB(t, []map[string]any{
		bar("600000.SH", 1.2, store.DailySourceBaostock),
		bar("600001.SH", 0.8, store.DailySourceTushare),
		bar("600002.SH", -2.0, store.DailySourceBaostock),
		bar("600003.SH", nil, "sina_degraded"),
		bar("600004.SH", nil, "eastmoney_degraded"),
		bar("600005.SH", nil, nil), // B3：来源列没落上，但涨跌同样没读数 ⇒ 双保险仍排除
	})
	got := dailyUpRatio(db, breadthDate)
	if got == nil {
		t.Fatalf("返回 nil：有 3 行真实读数的日子不该弃权")
	}
	// 期望 2/3：三条真实行里两涨。若降级行以伪造 0 混进分母，读数是 2/5＝0.4（偏弱假象）。
	want := 2.0 / 3.0
	if *got < want-1e-9 || *got > want+1e-9 {
		t.Fatalf("上涨占比=%v，期望 %v（分母没只数有涨跌读数的行）", *got, want)
	}
}

// B2：整日都是降级行 ⇒ 弃权（nil），而不是 0。
func TestUpRatioAbstainsOnAllDegradedDay(t *testing.T) {
	db := newBreadthDB(t, []map[string]any{
		bar("600010.SH", nil, "sina_degraded"),
		bar("600011.SH", nil, "eastmoney_degraded"),
	})
	if got := dailyUpRatio(db, breadthDate); got != nil {
		t.Fatalf("全降级日返回 %v，期望 nil 弃权——0 会被读成「全市场无一家上涨」这种假读数", *got)
	}
}

// B4 反向：把 NULL 来源判成降级（把 DailySourceNotDegraded 里的 IS NULL 挪走）会让历史样本凭空消失。
func TestUpRatioKeepsLegacyRowsWithoutSource(t *testing.T) {
	db := newBreadthDB(t, []map[string]any{
		bar("600020.SH", 3.0, nil), // 来源列落地前的世代，涨跌读数真实
		bar("600021.SH", -1.0, nil),
	})
	got := dailyUpRatio(db, breadthDate)
	if got == nil {
		t.Fatalf("老世代行被判成降级 ⇒ 分母为 0 而弃权（历史回放会整段丢样本）")
	}
	if *got != 0.5 {
		t.Fatalf("上涨占比=%v，期望 0.5（一涨一跌）", *got)
	}
}

// 对照：判据与写侧来源值脱节时的形态（把 LIKE 模式改歪，降级行会回到分母）。
// 这一枚是等值锁的对照组：它不判代码对错，只确认上面那把尺子真的在拦东西。
// 夹具刻意用旧线格式的产物（降级行 pct_chg 被折成 0＝伪造平盘，但仍带来源标记）：
// 只有这种行才需要来源判据来排除——新口径下它是 NULL，靠 pct_chg IS NOT NULL 就挡住了。
func TestUpRatioDegradesIfPredicateMissesSources(t *testing.T) {
	db := newBreadthDB(t, []map[string]any{
		bar("600030.SH", 1.0, store.DailySourceBaostock),
		bar("600031.SH", 0.0, "sina_degraded"), // 旧形态：断源日被写成平盘 0
	})
	got := dailyUpRatio(db, breadthDate)
	if got == nil || *got != 1.0 {
		t.Fatalf("判据读数=%v，期望恰好 1（唯一真实读数在涨）", got)
	}
	// 去掉来源判据的裸查询复算同一批数据：分母 2、分子 1 ⇒ 0.5。
	// 两个读数差 0.5 就是这条判据的价值；差值消失＝判据在这条 SQL 上根本没生效（假锁）。
	rows, err := db.QueryRows(`SELECT CAST(SUM(CASE WHEN pct_chg > 0 THEN 1 ELSE 0 END) AS REAL) / COUNT(*) AS r
		FROM daily WHERE trade_date = ?`, breadthDate)
	if err != nil || len(rows) == 0 {
		t.Fatalf("对照查询失败: %v", err)
	}
	naked, _ := rows[0]["r"].(float64)
	if naked >= *got-1e-9 {
		t.Fatalf("对照读数 %v 没有比判据读数 %v 更弱 ⇒ 来源判据在这条 SQL 上没生效（假锁），夹具也没在测这件事",
			naked, *got)
	}
}
