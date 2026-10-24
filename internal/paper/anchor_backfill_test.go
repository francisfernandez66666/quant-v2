// anchor_backfill_test.go — §ANCHOR-FIRST W3 存量锚点回填（模拟盘账本侧）的行为锁。
//
// 锁三件事：
//  1. 无锚点、有成本价 ⇒ 回填成本价，且 AnchorAt 跟着 FilledAt 盖章（锚点是"建仓那刻的价"）；
//  2. 无成本价 ⇒ **不写**（写 0 等于没写，读侧照样回落成本），计入跳过数；
//  3. 幂等：第二次调用回填数为 0，已有锚点（哪怕与成本不等）绝不被覆盖。
//
// 第 3 条的反证方向很重要：如果回填写成"无条件对齐成本"，那 W2 之后新建仓的真首买价锚点
// 会在每次重启时被摊薄成本覆盖——裁决二当场失效，且日志上只会显示"回填了 N 条"，看起来像正常工作。
// English: locks the W3 legacy backfill — cost becomes the anchor (with AnchorAt stamped from FilledAt),
// rows without a cost are skipped, and the operation is idempotent: an existing anchor is never
// overwritten, even when it differs from cost (that would silently undo ruling #2 on every restart).
package paper

import (
	"testing"
	"time"
)

// TestBackfillAnchorsLegacyPaper 存量持仓回填成本价、跳过无成本行、二次调用幂等。
// English: backfills legacy positions with cost, skips priceless rows, and is idempotent.
func TestBackfillAnchorsLegacyPaper(t *testing.T) {
	e := New(Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000}, "")
	filledAt := time.Date(2026, 10, 20, 9, 31, 0, 0, time.Local)
	e.mu.Lock()
	e.positions["600000"] = &Position{Code: "600000", Name: "浦发", Qty: 1000,
		CostPrice: 12.5, Cost: 12500, FilledAt: filledAt} // 无锚点＝W2 之前落盘的存量行
	e.positions["600001"] = &Position{Code: "600001", Name: "无价股", Qty: 100} // 无成本价，无从回填
	e.positions["600002"] = &Position{Code: "600002", Name: "已锚定", Qty: 100,
		CostPrice: 20, AnchorPrice: 15.05, FilledAt: filledAt} // 已有锚点（且≠成本）
	e.mu.Unlock()

	filled, skipped := e.BackfillAnchors()
	if filled != 1 || skipped != 1 {
		t.Fatalf("应回填 1 条、跳过 1 条, got filled=%d skipped=%d", filled, skipped)
	}
	e.mu.Lock()
	legacy := e.positions["600000"]
	priceless := e.positions["600001"]
	anchored := e.positions["600002"]
	e.mu.Unlock()

	if legacy.AnchorPrice != 12.5 {
		t.Fatalf("存量行锚点应回填为成本 12.5, got %v", legacy.AnchorPrice)
	}
	if !legacy.AnchorAt.Equal(filledAt) {
		t.Fatalf("AnchorAt 应沿用建仓时刻, got %v want %v", legacy.AnchorAt, filledAt)
	}
	if priceless.AnchorPrice != 0 {
		t.Fatalf("无成本价的行不得被写入锚点（写 0 等于没写）, got %v", priceless.AnchorPrice)
	}
	if anchored.AnchorPrice != 15.05 {
		t.Fatalf("已有锚点绝不能被回填覆盖（会抹掉真首买价）, got %v", anchored.AnchorPrice)
	}

	// 幂等：第二次调用不再回填任何东西。
	if filled2, _ := e.BackfillAnchors(); filled2 != 0 {
		t.Fatalf("第二次回填应为 0（幂等）, got %d", filled2)
	}
}
