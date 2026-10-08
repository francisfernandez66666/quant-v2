// daily_source_test.go — §P2-F（2026-10-06 修复批 波 5）日线来源列的**读侧**判据锁。
//
// 写侧把降级行的痕迹全押在 daily.source 这一列上，读侧判据一旦与写侧脱节，
// 失效形态是"筛不到降级数据"（恒绿）而不是报错——所以这里三组断言各自独立可红：
//
//	S1 片段形状：别名限定、LIKE 常量只出现一次（判据单源，禁止调用点各写一遍字面量）；
//	S2 真表分区：同一张 daily 上"非降级"与"仅降级"两支必须互斥且并集为全集，
//	   并且第三条兜底腿（未来 *_degraded 命名的新来源）自动落进降级支（后缀匹配取向）；
//	S3 统计点行为：板块聚合的成员数/平均涨幅/领涨股排除降级行，
//	   涨停家数**不**排除（close 与 up_limit 降级行同样给得出，筛掉反而少计）——
//	   两条方向不同的取向都锁住，防止有人"顺手把判据加满"或"顺手一处没加"。
//
// English: read-side locks for the daily.source predicate — fragment shape, real-table partition
// (NULL = pre-column era, new *_degraded legs land automatically), and the sector aggregation
// behavior including the deliberate asymmetry for limit-up counts.
package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// S1：判据片段自身的形状锁。
func TestDailySourceFragmentShape(t *testing.T) {
	keep := DailySourceNotDegraded("dv")
	drop := DailySourceDegradedOnly("dv")
	wantRefs := map[string]int{keep: 2, drop: 1} // 排除支引两次列（IS NULL + NOT LIKE），仅降级支引一次
	for frag, want := range wantRefs {
		if n := strings.Count(frag, DailySourceLikeDegraded); n != 1 {
			t.Fatalf("片段 %q 里 LIKE 模式出现 %d 次，期望恰好 1（多处＝有人在本函数里又拼了一遍字面量）", frag, n)
		}
		if n := strings.Count(frag, "dv.source"); n != want {
			t.Fatalf("片段 %q 里别名限定列出现 %d 次，期望 %d（别名丢了＝JOIN 里歧义或绑错表）", frag, n, want)
		}
		// 列名只准以别名限定形态出现：裸 source 的次数必须等于 dv.source 的次数。
		if strings.Count(frag, "source") != strings.Count(frag, "dv.source") {
			t.Fatalf("片段 %q 含未被别名限定的裸 source 列", frag)
		}
	}
	// 两支互斥的写法锁：NOT LIKE 只准出现在"排除"支，"仅降级"支不得含 NOT。
	if !strings.Contains(keep, "NOT LIKE") {
		t.Fatalf("排除支 %q 不含 NOT LIKE ⇒ 它没在排除任何东西（恒真＝判据空转）", keep)
	}
	if strings.Contains(drop, "NOT LIKE") {
		t.Fatalf("仅降级支 %q 含 NOT LIKE ⇒ 与排除支同义，统计点会把降级行留在样本里", drop)
	}
	// NULL 世代只落在排除支（"世代未知"不等于"数据坏了"），这一条是取向锁：
	// 若有人把 IS NULL 挪到仅降级支，历史数据会被整段当成降级数据筛掉。
	if !strings.Contains(keep, "IS NULL") {
		t.Fatalf("排除支 %q 不含 IS NULL ⇒ 来源列落地前的老行会被判成降级（凭空丢历史）", keep)
	}
	if strings.Contains(drop, "IS NULL") {
		t.Fatalf("仅降级支 %q 含 IS NULL ⇒ 老世代行被当降级行，读侧会凭空丢掉整段历史", drop)
	}
	// 无别名形态（单表查询）不得留下点号。
	if strings.Contains(DailySourceNotDegraded(""), ".") {
		t.Fatalf("空别名仍带点号：单表查询里会被判成列名歧义")
	}
}

// S2：真表上的分区等值锁（含"第三条腿自动进射程"）。
func TestDailySourcePartitionOnRealTable(t *testing.T) {
	db := newTestDB(t)
	const date = "20260918"
	srcs := []string{DailySourceBaostock, DailySourceTushare, "sina_degraded", "eastmoney_degraded",
		"tencent_degraded"} // 假想的第三条兜底腿：沿用 *_degraded 命名就自动进降级支
	rows := make([]map[string]any, 0, len(srcs))
	for i, s := range srcs {
		rows = append(rows, map[string]any{
			"ts_code": "60000" + string(rune('0'+i)) + ".SH", "trade_date": date,
			"close": 10.0, "pct_chg": 1.0, "source": s,
		})
	}
	// 再插一行来源列为 NULL（本列落地前的世代）。
	rows = append(rows, map[string]any{
		"ts_code": "600099.SH", "trade_date": date, "close": 10.0, "pct_chg": 1.0,
	})
	if _, err := db.InsertRows("daily", TableColumns("daily"), rows); err != nil {
		t.Fatalf("insert: %v", err)
	}
	total := countDaily(t, db, "")
	deg := countDaily(t, db, DailySourceDegradedOnly(""))
	keep := countDaily(t, db, DailySourceNotDegraded(""))
	if total != int64(len(srcs))+1 {
		t.Fatalf("总数=%d，期望 %d（夹具本身没落全，后面的分区断言都不可信）", total, len(srcs)+1)
	}
	if deg != 3 {
		t.Fatalf("降级支=%d，期望 3（sina/eastmoney/tencent 三个 *_degraded；后缀匹配失效＝新兜底腿静默放行）", deg)
	}
	if keep != int64(len(srcs))+1-3 {
		t.Fatalf("排除支=%d，期望 %d（baostock/tushare/NULL 三行）", keep, int64(len(srcs))+1-3)
	}
	if keep+deg != total {
		t.Fatalf("两支不互补：%d+%d≠%d ⇒ 有行同时落两支或谁都不落（判据写坏了）", keep, deg, total)
	}
	// 反向锁：把 LIKE 模式写成枚举（%sina_degraded%）会让 tencent 腿漏网——
	// 这里用真实查询确认"新命名腿"确实被排除支挡在外面。
	if n := countDaily(t, db, "source LIKE '%sina_degraded%'"); n != 1 {
		t.Fatalf("单来源枚举计数=%d，期望 1（对照组：证明上面 3 这个数字不是蒙的）", n)
	}
}

func countDaily(t *testing.T, db *DB, where string) int64 {
	t.Helper()
	q := "SELECT COUNT(*) FROM daily"
	if where != "" {
		q += " WHERE " + where
	}
	var n int64
	if err := db.db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// S3：板块聚合的行为锁（两支统计方向不同，都要对上）。
func TestSectorAggExcludesDegradedRows(t *testing.T) {
	db := newTestDB(t)
	const date = "20260918"
	const ind = "半导体"
	// 三只票：两只主源真实涨跌 +5 / -3，一只降级行（旧口径下会被写成 0＝平盘）。
	stocks := []map[string]any{
		{"ts_code": "600101.SH", "name": "甲", "industry": ind},
		{"ts_code": "600102.SH", "name": "乙", "industry": ind},
		{"ts_code": "600103.SH", "name": "丙", "industry": ind},
	}
	if _, err := db.InsertRows("stocks", TableColumns("stocks"), stocks); err != nil {
		t.Fatalf("stocks insert: %v", err)
	}
	daily := []map[string]any{
		{"ts_code": "600101.SH", "trade_date": date, "close": 10.5, "pct_chg": 5.0, "source": DailySourceBaostock},
		{"ts_code": "600102.SH", "trade_date": date, "close": 9.7, "pct_chg": -3.0, "source": DailySourceTushare},
		// 降级行：涨跌读数为 NULL（新口径），价格与停板价仍然可用。
		{"ts_code": "600103.SH", "trade_date": date, "close": 11.0, "pct_chg": nil, "source": "sina_degraded"},
	}
	if _, err := db.InsertRows("daily", TableColumns("daily"), daily); err != nil {
		t.Fatalf("daily insert: %v", err)
	}
	// 涨停表：三只票都封板（close >= up_limit*0.995 由夹具的 up_limit 值决定）。
	limits := []map[string]any{
		{"ts_code": "600101.SH", "trade_date": date, "up_limit": 10.5, "down_limit": 9.0},
		{"ts_code": "600102.SH", "trade_date": date, "up_limit": 9.7, "down_limit": 8.0},
		{"ts_code": "600103.SH", "trade_date": date, "up_limit": 11.0, "down_limit": 9.9},
	}
	if _, err := db.InsertRows("stk_limit", TableColumns("stk_limit"), limits); err != nil {
		t.Fatalf("stk_limit insert: %v", err)
	}

	days, err := db.aggregateSectorDayFull(date)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(days) != 1 {
		t.Fatalf("板块数=%d，期望 1：%+v", len(days), days)
	}
	sd := days[0]
	// 成员数：只数有涨跌读数的两行（降级行被排除）⇒ 2 而不是 3。
	if sd.MemberCount != 2 {
		t.Fatalf("成员数=%d，期望 2（把降级行留在样本里＝均值口径与成员口径不同一样本集）", sd.MemberCount)
	}
	// 平均涨幅：(5 + (-3)) / 2 = 1；若降级行以 0 混入则是 2/3＝把断源日读成偏弱。
	if sd.ChangePct != 1.0 {
		t.Fatalf("平均涨幅=%v，期望恰好 1（降级行的伪造 0 会把它压到 0.6667）", sd.ChangePct)
	}
	// 领涨股：降级行不得进榜（它没有涨跌读数，COALESCE 兜 0 后仍可能排进前列）。
	if strings.Join(sd.TopStocks, ",") != "600101.SH,600102.SH" {
		t.Fatalf("领涨股=%v，期望 [600101.SH 600102.SH]（降级行进榜＝拿没有读数的票当领涨）", sd.TopStocks)
	}
	// 反向取向锁：涨停家数**必须**包含降级行（close/up_limit 两个读数它都给得出，
	// 把它筛掉会少计涨停家数——这是刻意不对称，不是漏加判据）。
	if sd.LimitupCnt != 3 {
		t.Fatalf("涨停家数=%d，期望 3（若这里变成 2，说明给涨停那条也加了来源判据＝少计共振强度）", sd.LimitupCnt)
	}
}
