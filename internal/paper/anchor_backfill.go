// anchor_backfill.go — §ANCHOR-FIRST W3 存量锚点回填（模拟盘账本侧）。
//
// 背景：W2 只给"新建仓"写锚点，此前的存量持仓 AnchorPrice=0。影子期这没关系（对照表把它点名成
// "未回填"），但 W3 切硬之后 `AnchorOrCost()` 会把 0 回落成成本——如果一直不回填，这批持仓的
// 风险线就永远走成本口径，而新建仓走锚点口径，同一本账里并存两套分母，正是裁决二要收掉的东西。
//
// 回填口径（Q9 的边界）：**把含费摊薄成本当作锚点**。
//   - 为什么不去翻成交历史凑"真正的首买价"：`fills` 表按保留期裁剪（tradeRetention），
//     硬凑会造出一个"看起来像首买价、其实是某天均价"的数——这比诚实回落更危险，因为它会被
//     当成事实参与判线且再也查不出来。
//   - 用成本回填的语义很干净：**切硬对这批存量持仓零位移**（风险线与 W2 完全一致）。
//     真正的首买价锚点只对 W2 之后建的仓成立，这是能如实交代的最强承诺。
//   - CostPrice<=0 的行跳过：没有任何价格信息，回填只会写进一个 0，读侧照样回落，属于假动作。
//
// 幂等：只处理 AnchorPrice<=0 的行，第二次调用不会再写任何东西。
// English: W3 legacy anchor backfill for the paper book. The anchor is backfilled with the fee-inclusive
// diluted cost, not with a reconstructed "first buy price": the fills table is retention-trimmed, so any
// reconstruction would invent a number that merely looks like the real first buy price and would then be
// treated as fact. Backfilling with cost means the hard cutover moves no existing risk line at all.
package paper

import "log"

// BackfillAnchors 为无锚点的存量持仓补写锚点，返回回填条数与跳过条数（跳过＝无成本价、无从回填）。
// 有变更才落盘；落盘沿用引擎既有的原子写（temp+rename+fsync）。
// English: backfills anchors for legacy positions and returns (filled, skipped); persists only on change.
func (e *Engine) BackfillAnchors() (filled, skipped int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	filled, skipped = e.backfillAnchorsLocked()
	if filled > 0 {
		e.persist()
	}
	return filled, skipped
}

// backfillAnchorsLocked 回填本体（须持锁调用）。
// AnchorAt 取 FilledAt：锚点是"建仓那一刻的价"，用建仓时刻盖章才自洽；FilledAt 为零值时留空，
// 前端与对照表都以 AnchorPrice==0 判"未回填"，不会因为时刻缺失而误判。
// English: the backfill itself (caller holds the lock). AnchorAt mirrors FilledAt; a zero FilledAt stays
// empty — both the UI and the shadow table key off AnchorPrice==0, never the timestamp.
func (e *Engine) backfillAnchorsLocked() (filled, skipped int) {
	for _, p := range e.positions {
		if p == nil || p.AnchorPrice > 0 {
			continue
		}
		if p.CostPrice <= 0 {
			skipped++
			continue
		}
		p.AnchorPrice = p.CostPrice
		if p.AnchorAt.IsZero() {
			p.AnchorAt = p.FilledAt
		}
		filled++
	}
	if filled > 0 || skipped > 0 {
		log.Printf("[paper] §ANCHOR-FIRST W3 存量锚点回填：回填 %d 条（按含费摊薄成本，切硬零位移），跳过 %d 条（无成本价）",
			filled, skipped)
	}
	return filled, skipped
}
