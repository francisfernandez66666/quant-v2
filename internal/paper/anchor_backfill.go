// anchor_backfill.go — §DYN-T 存量锚点回填（口径＝**首买价**，方案 §1.6a，四轮裁决 T4-6）。
//
// 为什么这批必须先回填：批 4 把卖出裁决内核的涨跌幅基准从摊薄成本 `CostPrice` 改读
// `AnchorPrice`（`QMTConfig.AnchorHard` 默认已翻成"按锚点判线"），于是**所有持仓**的风险线
// 基准都变成锚点。存量里 `AnchorPrice=0` 的行如果不处理，就有两种坏结局：
//   - `AnchorOrCost()` 回落成本 ⇒ 摊薄成本重新当风控分母，正是本批要修掉的缺陷；
//   - 或者干脆不开 T 腿 ⇒ 这批票永远进不了日内回转，功能对老持仓形同不存在。
//
// 回填口径与降级顺序（每票取第一个拿得到的读数，并把来源写进 AnchorSource）：
//  1. `first_lot`       层账里最早那一批的含费每股成本＝真正的首买价（最可信，与新建仓同源）；
//  2. `first_buy_order` 调用方（engine）从买入历史（实盘 orders/fills）取到的最早一笔成交价；
//  3. `cost_fallback`   只有摊薄成本可读——**只给裁决内核一个能算的基准，不作为开 T 的资格**；
//  4. `none`            什么价都没有：`AnchorPrice` 保持 0，既不判线也不开 T。
//
// 为什么第 3 档不能开 T：摊薄成本可能被历史加仓、也可能被正 T 买腿本身改写过，
// 把它写成"最初那笔"就是让 T 腿继续移动风控线，只是换了个入口（§0 第 10 条）。
//
// 幂等：只处理 `AnchorPrice<=0` 的行；已回填的第二次调用一律不动（含来源字段）。
// dry-run：`AnchorBackfillPlan` 只算不写，供 owner 逐票看"前后触发价差多少"再签字落盘——
// 首买价通常**高于**摊薄成本（加仓摊薄的方向），切锚点后止损线会抬高、离现价更近，
// 这是把隐藏的基准漂移显性化，不是新发明的严格，必须让 owner 先看见。
//
// English: backfill legacy anchors with the **first buy price** (four provenance levels), never with a
// diluted cost as a T-round ticket, and always plan-then-sign before writing.
package paper

import (
	"fmt"
	"log"
	"sort"
	"time"
)

// 锚点来源枚举（字面量进账本、接口与 dry-run 报告，改字面量等于改契约）。
const (
	anchorSourceFirstLot     = "first_lot"       // 层账最早批次的含费买入价（＝首买价，可开 T）
	anchorSourceFirstBuy     = "first_buy_order" // 买入历史里最早一笔成交价（可开 T）
	anchorSourceCostFallback = "cost_fallback"   // 只有摊薄成本（有基准但**不开 T**）
	anchorSourceNone         = "none"            // 无从回填（不判线、不开 T）
)

// AnchorBackfillRow 一行回填计划/结果（dry-run 报告与落盘共用同一结构，避免两套口径漂移）。
type AnchorBackfillRow struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Qty         int     `json:"qty"`
	OldAnchor   float64 `json:"old_anchor"`
	NewAnchor   float64 `json:"new_anchor"`
	CostPrice   float64 `json:"cost_price"`
	Source      string  `json:"anchor_source"`
	Mark        float64 `json:"mark"`
	StopBefore  float64 `json:"stop_before"`   // 回填前的止损触发价（按旧基准算）
	StopAfter   float64 `json:"stop_after"`    // 回填后的止损触发价
	LineDeltaBP float64 `json:"line_delta_bp"` // 触发价位移（基点，正＝线抬高＝更早触发）
	WouldSkip   bool    `json:"would_skip"`    // 拿不到任何价，本次不动
}

// AnchorBackfillCoverage 覆盖率读数（批 4 动手前必须先有它，方案 §1.6a）。
type AnchorBackfillCoverage struct {
	Positions    int     `json:"positions"`     // Qty>0 的持仓行数
	WithAnchor   int     `json:"with_anchor"`   // AnchorPrice>0
	Missing      int     `json:"missing"`       // AnchorPrice=0（待回填）
	Backfillable int     `json:"backfillable"`  // 待回填里能拿到首买价或历史的行数
	CostOnly     int     `json:"cost_only"`     // 只能按成本回落（回填后也不得开 T）
	Unfillable   int     `json:"unfillable"`    // 完全无价可回填
	MissingValue float64 `json:"missing_value"` // 待回填行的市值合计
	TotalValue   float64 `json:"total_value"`   // 全部持仓市值
	CoveragePct  float64 `json:"coverage_pct"`  // 已有锚点覆盖率（按行数）
	TicketPct    float64 `json:"ticket_pct"`    // 回填后可开 T 的市值占比
}

// firstBuyPrice 取该持仓的**首买价**：层账里成交时间最早那一批的含费每股成本。
// 没有层账（本批之前落盘的存量）返回 0，交给调用方的买入历史回调。
// English: earliest lot's fee-inclusive per-share cost; 0 when a legacy position has no lot ledger.
func (p *Position) firstBuyPrice() float64 {
	if len(p.Lots) == 0 {
		return 0
	}
	best := p.Lots[0]
	for _, l := range p.Lots[1:] {
		if l.Price > 0 && (best.Price <= 0 || l.FilledAt.Before(best.FilledAt)) {
			best = l
		}
	}
	if best.Price <= 0 {
		return 0
	}
	return best.Price
}

// AnchorTicket 该持仓有没有"开 T 腿"的资格（§8.4.5 锚点前置）。
// 只有锚点非零**且**来源可证（首买价/买入历史）才放行；cost_fallback 与 none 一律拒绝，
// 哪怕后者已经把 AnchorPrice 填成了一个能算的数。
// English: T eligibility needs a non-zero anchor **and** a provable provenance; a cost-derived anchor
// is only good enough to compute risk lines, never to justify intraday trading.
func (p *Position) AnchorTicket() bool {
	if p == nil || p.AnchorPrice <= 0 {
		return false
	}
	return p.AnchorSource == anchorSourceFirstLot || p.AnchorSource == anchorSourceFirstBuy ||
		// 新建仓路径在建仓时就写好了锚点（fillLocked），当时还没有来源字段——按 first_lot 同性质处理，
		// 层账与锚点同源，可信度等同 first_lot。
		(len(p.AnchorSource) == 0 && len(p.Lots) > 0)
}

// AnchorBackfillPlan 生成回填计划（**只读**，不落盘）：dry-run 报告与签字前核对都用它。
// firstBuyLookup 由 engine 注入（实盘买入历史最早一笔成交价）；paper 单测传 nil 即只走层账/成本两档。
// English: builds the backfill plan without touching the book; the optional lookup is supplied by the
// engine for broker order history, nil keeps it to the lot/cost ladder.
func (e *Engine) AnchorBackfillPlan(firstBuyLookup func(code string) (float64, bool), allowCostFallback bool) []AnchorBackfillRow {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.anchorBackfillPlanLocked(firstBuyLookup, allowCostFallback)
}

// anchorBackfillPlanLocked 回填计划本体（须持锁调用；加载期与覆盖率读数也复用它，避免两套口径）。
// English: the plan body; load-time and coverage reads share it so no caller grows its own variant.
func (e *Engine) anchorBackfillPlanLocked(firstBuyLookup func(code string) (float64, bool), allowCostFallback bool) []AnchorBackfillRow {
	rows := make([]AnchorBackfillRow, 0, len(e.positions))
	for _, p := range e.positions {
		if p == nil || p.Qty <= 0 {
			continue
		}
		if p.AnchorPrice > 0 {
			continue // 已有锚点（含上一次回填）：幂等，不动
		}
		row := AnchorBackfillRow{Code: p.Code, Name: p.Name, Qty: p.Qty,
			OldAnchor: p.AnchorPrice, CostPrice: p.CostPrice, Mark: p.Mark}
		if v := p.firstBuyPrice(); v > 0 {
			row.NewAnchor, row.Source = v, anchorSourceFirstLot
		} else if firstBuyLookup != nil {
			if v, ok := firstBuyLookup(p.Code); ok && v > 0 {
				row.NewAnchor, row.Source = v, anchorSourceFirstBuy
			}
		}
		if row.NewAnchor <= 0 && p.CostPrice > 0 && allowCostFallback {
			// 第三档只给"能算的基准"，不给 T 资格（见文件头与 AnchorTicket）。
			// 而且**只有 owner 签字后的显式回填才允许**：账本加载时的自动回填不带这一档，
			// 否则"加载即把摊薄成本写成锚点"会绕过签字，把本批要防的口径悄悄放回风控线。
			row.NewAnchor, row.Source = p.CostPrice, anchorSourceCostFallback
		}
		if row.NewAnchor <= 0 {
			row.Source, row.WouldSkip = anchorSourceNone, true
		}
		// 位移读数：用「基准价 × (1−止损距离)」表达触发价移动。止损距离取不到时按 6%（纪律止损默认），
		// 这一列只服务于 owner 的签字判断，不参与任何执行判据，因此宁缺勿假：Mark 缺失就留 0。
		if p.CostPrice > 0 && row.NewAnchor > 0 && row.Mark > 0 {
			const stopPct = 0.06
			row.StopBefore = p.CostPrice * (1 - stopPct)
			row.StopAfter = row.NewAnchor * (1 - stopPct)
			row.LineDeltaBP = (row.StopAfter - row.StopBefore) / p.CostPrice * 10000
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Code < rows[j].Code })
	return rows
}

// AnchorCoverage 存量锚点覆盖率读数（批 4 前置，dry-run 报告首页就是这几个数）。
// 一次加锁内同时算计划与覆盖率：分两次加锁会在两次读之间看到不同的账本，报告自相矛盾。
func (e *Engine) AnchorCoverage(firstBuyLookup func(code string) (float64, bool)) AnchorBackfillCoverage {
	e.mu.Lock()
	defer e.mu.Unlock()
	plan := e.anchorBackfillPlanLocked(firstBuyLookup, true)
	var c AnchorBackfillCoverage
	for _, p := range e.positions {
		if p == nil || p.Qty <= 0 {
			continue
		}
		c.Positions++
		v := p.Mark
		if v <= 0 {
			v = p.CostPrice
		}
		c.TotalValue += v * float64(p.Qty)
		if p.AnchorPrice > 0 {
			c.WithAnchor++
			if p.AnchorTicket() {
				c.TicketPct += v * float64(p.Qty)
			}
			continue
		}
		c.Missing++
		c.MissingValue += v * float64(p.Qty)
	}
	for _, r := range plan {
		switch r.Source {
		case anchorSourceFirstLot, anchorSourceFirstBuy:
			c.Backfillable++
		case anchorSourceCostFallback:
			c.CostOnly++
		default:
			c.Unfillable++
		}
	}
	if c.Positions > 0 {
		c.CoveragePct = float64(c.WithAnchor) / float64(c.Positions) * 100
	}
	if c.TotalValue > 0 {
		c.TicketPct = c.TicketPct / c.TotalValue * 100
	}
	return c
}

// BackfillAnchors 执行回填（写内存 + 有变更才落盘）。返回写入条数与跳过条数。
// ⚠ 必须先给 owner 看过 AnchorBackfillPlan 的输出并签字（方案 §1.6a）；
// 本函数不做任何"顺手把成本当锚点"的隐藏动作：写进去的价与来源一一对应，可审计。
// English: performs the backfill (persist on change). The plan must be signed off first; the provenance
// column is what makes a cost-derived anchor auditable instead of a silent re-import of the bug.
func (e *Engine) BackfillAnchors(firstBuyLookup func(code string) (float64, bool)) (filled, skipped int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	plan := e.anchorBackfillPlanLocked(firstBuyLookup, true)
	now := e.clockNow()
	for _, r := range plan {
		p := e.positions[r.Code]
		if p == nil || p.Qty <= 0 || p.AnchorPrice > 0 {
			continue // 计划与执行之间持仓可能已平/已被别的路径补写：以执行时读数为准
		}
		if r.WouldSkip || r.NewAnchor <= 0 {
			skipped++
			continue
		}
		p.AnchorPrice = r.NewAnchor
		p.AnchorSource = r.Source
		if p.AnchorAt.IsZero() {
			p.AnchorAt = firstNonZeroTime(p.FilledAt, now)
		}
		filled++
	}
	if filled > 0 {
		e.persist()
	}
	if filled > 0 || skipped > 0 {
		log.Printf("[paper] §DYN-T 锚点回填：写入 %d 条（首买价/买入历史/成本回落三档），跳过 %d 条（无任何价）",
			filled, skipped)
	}
	return filled, skipped
}

// TAnchorBlockReason 开 T 腿前的锚点资格检查（返回非空＝拒绝原因，供 step0 归因日志）。
// 为什么单独成函数：纸面与实盘、以及 §6.1 锁 11③ 都要同一句判定，写三遍必然漂移。
// English: one shared T-eligibility check so paper, live and the lock file cannot drift apart.
func (e *Engine) TAnchorBlockReason(p *Position) string {
	if p == nil {
		return "无持仓"
	}
	if p.AnchorPrice <= 0 {
		return "锚点缺失(AnchorPrice=0)，未回填不得开T"
	}
	if !p.AnchorTicket() {
		return fmt.Sprintf("锚点来源不可信(anchor_source=%q)，成本回落档不得开T", p.AnchorSource)
	}
	return ""
}

// BackfillAnchorsOnLoad 账本加载时的**保守回填**：只认层账首买价（first_lot），
// 绝不写成本回落档——那一档必须等 owner 看过 dry-run 签字后由 BackfillAnchors 显式执行。
// English: the load-time backfill is restricted to lot-proven anchors; a cost-derived anchor only ever
// lands through the signed path, never as a side effect of opening the book.
func (e *Engine) BackfillAnchorsOnLoad() (filled, skipped int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	plan := e.anchorBackfillPlanLocked(nil, false)
	now := e.clockNow()
	for _, r := range plan {
		p := e.positions[r.Code]
		if p == nil || p.Qty <= 0 || p.AnchorPrice > 0 || r.NewAnchor <= 0 {
			if r.NewAnchor <= 0 {
				skipped++
			}
			continue
		}
		p.AnchorPrice = r.NewAnchor
		p.AnchorSource = r.Source
		if p.AnchorAt.IsZero() {
			p.AnchorAt = firstNonZeroTime(p.FilledAt, now)
		}
		filled++
	}
	if filled > 0 {
		e.persist()
	}
	return filled, skipped
}

// TAnchorEligibleCount 当前有多少持仓具备"开做 T 腿"的锚点资格（AnchorTicket 为真的行数）。
// 签字接口用它给一个总数：回填完成后 owner 要看的不是"补了几条"，而是"能开 T 的仓有多少"——
// cost_fallback/none 两档即便有锚点也不计入，这正是 §8.4.5 那道口径的读数面。
// English: how many positions may open a T leg at all — cost-derived anchors deliberately excluded.
func (e *Engine) TAnchorEligibleCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, p := range e.positions {
		if p == nil || p.Qty <= 0 {
			continue
		}
		if p.AnchorTicket() {
			n++
		}
	}
	return n
}

// firstNonZeroTime 回填盖章用：优先原始建仓时刻，没有就用本轮时刻（绝不留零值造成"未知时刻"）。
func firstNonZeroTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}
