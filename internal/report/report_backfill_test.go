// report_backfill_test.go — §ANCHOR-FIRST W3 存量锚点回填（持仓账/ExecLog 侧）的行为锁。
//
// 与 paper/store 两本账同一口径：锚点取**含费入场价**（EntryPrice），已有锚点绝不覆盖，
// 无入场价的记录跳过。持仓账是退出引擎（rpt）读的那本账，回填漏了就等于退出侧继续按成本判线。
// English: the holding-book W3 backfill — anchor from the fee-inclusive entry price, never overwriting an
// existing anchor, skipping priceless rows. This is the book the exit engines read.
package report

import (
	"testing"
	"time"
)

// TestBackfillAnchorsLegacyReport 回填存量、跳过无价、不覆盖已有锚点、幂等。
// English: backfills legacy rows, skips priceless ones, never overwrites, and is idempotent.
func TestBackfillAnchorsLegacyReport(t *testing.T) {
	entryAt := time.Date(2026, 10, 20, 9, 31, 0, 0, time.Local)
	r := NewFromLogs([]ExecLog{
		{SignalID: "s1", Code: "600519", EntryPrice: 15.05, EntryAt: entryAt},                 // 存量：无锚点
		{SignalID: "s2", Code: "600000", EntryPrice: 0, EntryAt: entryAt},                     // 无入场价：跳过
		{SignalID: "s3", Code: "000001", EntryPrice: 20, EntryAt: entryAt, AnchorPrice: 10.5}, // 已锚定（≠入场价）
	})

	if n := r.BackfillAnchors(); n != 1 {
		t.Fatalf("应回填 1 条（只有 s1 满足无锚点+有入场价）, got %d", n)
	}
	logs := r.List()
	byID := make(map[string]ExecLog, len(logs))
	for _, l := range logs {
		byID[l.SignalID] = l
	}
	if got := byID["s1"].AnchorPrice; got != 15.05 {
		t.Fatalf("存量记录锚点应回填为入场价 15.05, got %v", got)
	}
	if !byID["s1"].AnchorAt.Equal(entryAt) {
		t.Fatalf("AnchorAt 应沿用开仓时刻, got %v", byID["s1"].AnchorAt)
	}
	if got := byID["s2"].AnchorPrice; got != 0 {
		t.Fatalf("无入场价的记录不得被写入锚点, got %v", got)
	}
	if got := byID["s3"].AnchorPrice; got != 10.5 {
		t.Fatalf("已有锚点绝不能被回填覆盖（会抹掉真首买价）, got %v", got)
	}
	if n := r.BackfillAnchors(); n != 0 {
		t.Fatalf("第二次回填应为 0（幂等）, got %d", n)
	}
}
