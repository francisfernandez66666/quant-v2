// §B2 前置件（与实盘 finaCache、回放财务输入共享的裁决函数）的行为锁：
// LatestVisibleFina 钉"ann_date≤判定日 + 缺失不可知放行（§N-5）+ 全未来按缺失（§B7）"，
// ReportStale 钉"asof 可传判定日而非恒今天（§M-7 在回放侧的语义）"。
// English: behavior locks for the shared §B7/§M-7 predicates that both the live financial cache
// and the §B2 replay financial input consume.
package strategy_engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("20060102", s)
	if err != nil {
		t.Fatalf("日期解析 %s: %v", s, err)
	}
	return d
}

func TestLatestVisibleFinaPicksByAnnDate(t *testing.T) {
	rows := []store.FinaRow{
		{EndDate: "20201231", AnnDate: "20210401", ROE: 3},
		{EndDate: "20210331", AnnDate: "20210430", ROE: 5},
		{EndDate: "20210630", AnnDate: "20210930", ROE: 95},
	}
	// 判定日 20210615：H1 期披露日 0930 未到（skipped=1），只该吃到 Q1 期（ROE=5）——
	// 这条就是"回放/实盘都不许用未来函数"的裁决本体。
	f, sk := LatestVisibleFina(rows, "20210615")
	if f == nil || f.Roe != 5 || f.EndDate != "20210331" || sk != 1 {
		t.Fatalf("20210615 应取 ROE=5 且 skipped=1，实得 %+v sk=%d", f, sk)
	}
	// 判定日 20211001：H1 期已披露 → 取最新期（不能粘死在旧期上）
	f2, sk2 := LatestVisibleFina(rows, "20211001")
	if f2 == nil || f2.Roe != 95 || sk2 != 0 {
		t.Fatalf("20211001 应取 ROE=95 且 skipped=0，实得 %+v sk=%d", f2, sk2)
	}
	// 判定日早于所有披露日：全部不可见，按缺失出门（skipped 把成因留给调用方点名）
	f3, sk3 := LatestVisibleFina(rows, "20200101")
	if f3 != nil || sk3 != 3 {
		t.Fatalf("全未来应返回 nil+skipped=3，实得 %+v sk=%d", f3, sk3)
	}
	// §N-5：披露日缺失/非法按「不可知」照常采用该期，绝不连带丢掉整行
	f4, sk4 := LatestVisibleFina([]store.FinaRow{
		{EndDate: "20210630", AnnDate: "", ROE: 7},
	}, "20210701")
	if f4 == nil || f4.Roe != 7 || sk4 != 0 {
		t.Fatalf("披露日缺失应放行该期，实得 %+v sk=%d", f4, sk4)
	}
	// 空序列＝真缺失（库里没这只票）
	if f5, sk5 := LatestVisibleFina(nil, "20210701"); f5 != nil || sk5 != 0 {
		t.Fatalf("空序列应返回 nil+0，实得 %+v sk=%d", f5, sk5)
	}
}

func TestReportStaleUsesDecidingDayNotToday(t *testing.T) {
	// §B2 关键语义：闸问的是"判定那天"够不够新——20210430 披露、20210615 判定：不过旧；
	// 同一行在 20211231 判定（>240 天）就必须停用。若谓词写死 time.Now()，
	// 回放里 2021 年的判定日会全部被误判成"过旧"，财务腿名存实亡。
	f := &FinancialData{EndDate: "20210331", AnnDate: "20210430"}
	if stale, asof := ReportStale(f, mustDay(t, "20210615")); stale {
		t.Fatalf("46 天的报告期不该判过旧（asof %s）", asof)
	}
	if stale, asof := ReportStale(f, mustDay(t, "20211231")); !stale || asof != "20210430" {
		t.Fatalf("245 天的报告期应过旧并回 asof=20210430，实得 stale=%v asof=%q", stale, asof)
	}
	// 披露日缺失退回 end_date；两者皆缺/非法按「不可知」放行（§N-5）
	if stale, _ := ReportStale(&FinancialData{EndDate: "20180101"}, mustDay(t, "20211231")); !stale {
		t.Fatal("披露日缺失应退回报告期判定过旧")
	}
	if stale, asof := ReportStale(&FinancialData{}, mustDay(t, "20211231")); stale || asof != "" {
		t.Fatalf("日期全缺按不可知放行，实得 stale=%v asof=%q", stale, asof)
	}
	if stale, _ := ReportStale(nil, mustDay(t, "20211231")); stale {
		t.Fatal("nil 不该判过旧")
	}
}
