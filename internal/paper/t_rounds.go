// t_rounds.go — §T-MODE 做 T（2026-10-24 owner 裁决四，施工波次 W6 正 T / W7 反 T）。
//
// 做 T 是什么：持有底仓时，用"当日买、当日卖"的一对腿把日内波动变成成本摊薄。A 股 T+1 下只有
// 底仓可卖，所以两种方向各有一条腿必须落在当日批次上：
//   - **正 T**（先买后卖）：低吸买入机动仓 → 卖出**等量底仓**。收盘后持仓回到原量，成本下降。
//     卖出的是底仓（可卖），当日买入的那批原样留着次日转可卖——这正是裁决三"先只卖可卖部分"的
//     资金形态，所以 W4 的层账是它的前置。
//   - **反 T**（先卖后买）：高抛卖出一部分底仓 → 回踩后买回等量。卖腿必须**留一手底仓**，
//     否则持仓被卖光、生命周期关掉、再入场冷却记上，买回腿会被自家闸门挡死（方案 2.7）。
//
// 三条硬约束（全部来自 Q13 实读结论，不是偏好）：
//  1. **两腿串行**：一轮回转同一时刻最多只有一条开腿，绝不同时挂反向腿。对敲判定只看"执行中的
//     委托"，串行就落不进那个口径；同时挂两条腿必然踩。
//  2. **不做两腿事务**：柜台没有原子两腿（Q13 第 5 条），所以配对失败一律按"收摊"处理而不是
//     "重试到成功"——Q8 明确不追买，追买是方向性风险。
//  3. **不烧退出槽、不关生命周期**：做 T 的腿走 `sellQtyLocked`（部分卖）与 `addToPositionLocked`
//     （加仓），绝不进 `sellAllLocked` 的 delete+冷却分支，也绝不置位 `trimDone`
//     ——否则一次做 T 会把当日真正的退出裁决吞掉。
//
// 幂等键：`t0:<code>:<方向>:<YYYYMMDD>:<seq>`，与退出/减仓键正交（方案 2.6 第 4 项）。
// 落地位置：本文件在 paper 包而不是 engine 包。方案 §4.4 第 2 项原本写 `internal/engine/intraday_t.go`，
// 但回转状态必须与账本同锁、同落盘、同一次原子写——放到 engine 侧就要复制一套持久化与锁，
// 反而更容易出"账本已变、回转状态没跟上"的半态。故取"独立文件、不进 paper.go"这条本意，
// engine 侧只留一个只读配置解析 + 每轮调用的薄驱动（`internal/engine/intraday_t.go`）。
//
// English: §T-MODE intraday T rounds (W6 buy-first / W7 sell-first). Two legs are strictly
// serialized (the Q13 read: the broker's counter-trade check only sees *live* orders, so never
// place both legs at once), there is no two-leg transaction (Q13 #5), so a failed pairing is
// unwound per Q8 instead of retried. T legs never burn the trim dedup slot and never take the
// full-close branch, so a round can't swallow the day's real exit adjudication.
package paper

import (
	"fmt"
	"log"
	"sort"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// 做 T 的枚举（字符串直接进账本与前端，改字面量等于改契约，故集中在此）。
// English: T-round enums; the literals are persisted and shown in the UI, so they live here only.
const (
	TRModeBuyFirst  = "正T" // 先买后卖（低吸机动仓 → 卖等量底仓）
	TRModeSellFirst = "反T" // 先卖后买（高抛底仓 → 回踩买回）

	tRLegBuy  = "buy"  // 第一腿＝买入
	tRLegSell = "sell" // 第一腿＝卖出

	tRStateOpen    = "open"    // 已开腿，等第二腿配对
	tRStateDone    = "done"    // 两腿都成交，回转完成
	tRStateUnwound = "unwound" // 半态收摊（买回被拦 / 尾盘未配平 / 跨日），显式留痕不重试
)

// tRMinBaseKeep 反 T 卖腿必须留下的底仓股数（1 手 = 100 股）。
// 为什么要留：卖光之后 `sellQtyLocked` 会把持仓留成 Qty=0 的僵尸行，而清仓分支
// （sellAllLocked）会删行 + 记再入场冷却 —— 两条路都会让买回腿被自家闸门挡死。
// English: sell-first must keep at least one lot, otherwise the position becomes a zero-qty zombie
// row or takes the full-close branch (delete + re-entry cooldown) and the buy-back is blocked.
const tRMinBaseKeep = 100

// tRKeepRounds 回转账保留条数上限（只留最近这些条，防止一年下来把账本撑成几 MB）。
// English: how many round records to keep (most recent first), bounding the ledger size.
const tRKeepRounds = 300

// TRound 一次做 T 回转的记账单元（Q11：独立回转账，不往 report.ExecLog 里塞 TRounds[]，
// 因为"一仓一生命周期"是 ExecLog 的既有不变量，日内多次回转会直接打穿它）。
// ID 是对外幂等键，也是"这笔成交属于哪次回转"的唯一线索。
// English: one T round. Q11 keeps this as its own ledger instead of extending report.ExecLog,
// whose one-position-one-lifecycle invariant would be broken by intraday repeats.
type TRound struct {
	ID   string `json:"id"`   // 幂等键 t0:<code>:<方向>:<YYYYMMDD>:<seq>
	Code string `json:"code"` // 标的代码
	Name string `json:"name"` // 标的名称
	Mode string `json:"mode"` // 正T / 反T
	Seq  int    `json:"seq"`  // 当日第几轮（1 起）
	// Leg 第一腿方向（buy/sell）；State=open 时它就是"此刻还欠哪条腿"的反面。
	Leg      string    `json:"leg"`
	LegQty   int       `json:"leg_qty"`   // 第一腿成交股数
	LegPrice float64   `json:"leg_price"` // 第一腿成交价（含滑点，第二腿的价差基准）
	LegAt    time.Time `json:"leg_at"`    // 第一腿成交时刻
	// BaseQty 第一腿成交**前**的底仓量。正 T 的卖出腿上界、反 T 的卖出量上界都从它派生，
	// 用事后读到的持仓量去推会把这期间的其他成交算进来。
	BaseQty int    `json:"base_qty"`
	State   string `json:"state"` // open / done / unwound
	// PairQty/PairPrice/PnL 第二腿成交与本次回转的锁定价差（元，未扣第二腿费用）。
	// PnL 用"第二腿价 − 第一腿价"乘股数，正 T 与反 T 同号：正 T 是低买高卖，反 T 是高卖低买。
	PairQty   int       `json:"pair_qty,omitempty"`
	PairPrice float64   `json:"pair_price,omitempty"`
	PnL       float64   `json:"pnl,omitempty"`
	ClosedAt  time.Time `json:"closed_at,omitempty"`
	Day       string    `json:"day"`              // 开腿日 YYYYMMDD（限次与跨日收摊的判据）
	Reason    string    `json:"reason,omitempty"` // 收摊/成交原因（事后核"为什么没配平"）
}

// TParams 一轮做 T 的执行参数——由 engine 侧从账号级配置解析后注入。
// paper 包不读 config（与 AllocLayers/T0Settle 的"纯核算"分工一致），所以阈值全部走入参，
// 好处是同一套编排逻辑可以在测试里用任意阈值驱动，不必构造一份配置。
// English: per-round T parameters injected by the engine layer. The paper package never reads
// config, so every threshold is an argument and the orchestration is testable with any numbers.
type TParams struct {
	BuyFirst      bool    // 允许正 T
	SellFirst     bool    // 允许反 T
	MaxRounds     int     // 单码单日最大回转次数
	CapPct        float64 // 机动仓上限（百分比，如 30）
	MinSpreadPct  float64 // 最小价差（百分比，如 1.0）
	ForceUnwindHM string  // 尾盘强制配平时刻 HHMM（如 "1450"）
}

// RunIntradayT 做 T 的每轮执行入口（交易时段内由纸面估值轮调用）。
//
// 三道前置全部是"用户接管优先"：账本关、自动卖出关、做 T 参数一个方向都没开 —— 任一成立即整轮跳过。
// 第四道是 Q5：预算口径不是 dynamic 时禁用做 T（legacy 档下买回腿要过每日次数/预算/战法日预算三墙，
// 极易做成半态）。这条只打一次日志，不每轮刷。
// English: per-round entry. Four guards, all "the user's switch wins": book off, auto-sell off, no
// direction enabled, and (Q5) budget pipeline not dynamic. The last one logs once per day.
func (e *Engine) RunIntradayT(quotes map[string]*data.StockInfo, p TParams) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled || !e.cfg.AutoSell {
		return
	}
	if !p.BuyFirst && !p.SellFirst {
		return
	}
	if !e.dynamicBudgetOn() {
		now := time.Now()
		day := cntime.DayOf(now)
		if e.tWarnDay != day {
			e.tWarnDay = day
			log.Printf("[paper] §T-MODE 做T已配置但预算口径=%q（非 dynamic）→ 按 Q5 本日禁用"+
				"（legacy 档下买回腿要过每日次数/预算/战法日预算三墙，极易做成半态）",
				e.cfg.BudgetMode)
		}
		return
	}
	e.runIntradayTLocked(quotes, p, time.Now())
}

// runIntradayTLocked 做 T 的一轮主流程（须持锁调用）。
// 次序是硬要求：**先处理已开腿、再考虑开新腿**。反过来的话同一轮里可能先开一条新腿、
// 又去配平另一条旧腿，两条腿在时间上重叠 —— 正是 Q13 第 4 条要避开的并发形态。
// 尾盘（force=true）只收摊不开新腿：留一条刚开的一腿过夜，比"今天少做一轮"贵得多。
// English: pairing runs before opening (never overlap two legs in one round), and once the forced
// unwind time is reached no new leg is opened at all.
func (e *Engine) runIntradayTLocked(quotes map[string]*data.StockInfo, p TParams, now time.Time) {
	day := cntime.DayCompactOf(now)
	e.rollTRoundsLocked(day, now)
	force := e.tForceReached(now, p.ForceUnwindHM)

	for _, code := range e.tOpenCodesLocked() {
		e.pairTRoundLocked(code, quotes, p, force, now)
	}
	if force {
		return
	}
	e.openTRoundsLocked(quotes, p, day, now)
}

// rollTRoundsLocked 跨日收摊：昨日仍 open 的回转一律标记 unwound 并留痕（须持锁调用）。
//
// 为什么必须显式收：正 T 的买腿过夜意味着机动仓变成了底仓（违背"当日买当日平"的定义），
// 反 T 的卖腿过夜意味着那次高抛事实上退化成了一次减仓。两种都不是崩溃，但**必须在账上说出来**，
// 否则复盘时只看到"多了一笔仓位"或"少了一笔仓位"而找不到原因。
// English: yesterday's still-open rounds are explicitly unwound with a reason. An overnight buy leg
// means the intraday layer silently became base; an overnight sell leg means the spike sale degraded
// into a plain trim. Neither is a crash, but both must be visible in the ledger.
func (e *Engine) rollTRoundsLocked(day string, now time.Time) {
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State != tRStateOpen || r.Day == day {
			continue
		}
		r.State = tRStateUnwound
		r.ClosedAt = now
		if r.Leg == tRLegBuy {
			r.Reason = "跨日未配平：机动仓已成隔夜底仓（正T买腿未卖出等量底仓）"
		} else {
			r.Reason = "跨日未回踩：反T卖腿按普通减仓收摊（不追买）"
		}
		log.Printf("[paper] §T-MODE 回转跨日收摊 %s %s 第%d轮 腿=%s %d股 @%.2f → %s",
			r.Code, r.Mode, r.Seq, r.Leg, r.LegQty, r.LegPrice, r.Reason)
	}
}

// tForceReached 是否已到/过了尾盘强制配平时刻（HHMM 字符串比较即时间比较）。
// English: whether the forced-pairing time has been reached (HHMM lexicographic compare is a time compare).
func (e *Engine) tForceReached(now time.Time, hm string) bool {
	if len(hm) != 4 {
		return false
	}
	return cntime.In(now).Format("1504") >= hm
}

// tOpenCodesLocked 当前处于 open 态的回转所对应的代码（排序后返回，保证每轮处理次序稳定）。
// 直接在 map 上 range 会让"这一轮先处理哪只"随哈希漂移，多码同日时排障无从复现。
// English: codes with an open round, sorted so the per-round processing order is reproducible.
func (e *Engine) tOpenCodesLocked() []string {
	seen := make(map[string]bool, len(e.tRounds))
	out := make([]string, 0, len(e.tRounds))
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State == tRStateOpen && !seen[r.Code] {
			seen[r.Code] = true
			out = append(out, r.Code)
		}
	}
	sort.Strings(out)
	return out
}

// openTRoundFor 取该代码当前处于 open 态的回转（0 或 1 条——两腿串行的结构性保证）。
// English: the code's single open round, if any (0 or 1 by construction of the serialized legs).
func (e *Engine) openTRoundFor(code string) *TRound {
	for i := range e.tRounds {
		if e.tRounds[i].Code == code && e.tRounds[i].State == tRStateOpen {
			return &e.tRounds[i]
		}
	}
	return nil
}

// tRoundsUsedLocked 该代码当日已开过的回转次数（含已收摊的，因为收摊那次的腿也已经真实成交了）。
// English: rounds opened today for this code, including unwound ones (their legs really traded).
func (e *Engine) tRoundsUsedLocked(code, day string) int {
	n := 0
	for i := range e.tRounds {
		if e.tRounds[i].Code == code && e.tRounds[i].Day == day {
			n++
		}
	}
	return n
}

// pairTRoundLocked 处理一条已开腿的第二腿（须持锁调用）。
//
// 正 T（Leg=buy）：卖出等量底仓。可卖量必须覆盖 need —— 因为可卖的只有底仓，机动仓当日不可卖；
// 覆盖不了说明这条持仓的底仓在开腿之后被别的路径卖掉了，此时尾盘只能收摊留痕（不硬卖）。
// 反 T（Leg=sell）：回踩后买回等量。买回被拦（现金不足/池纪律/涨停/无价）一律按 Q8 收摊：
// **不重试、不隔夜追买**——追买是在已经证明"买不回来"之后继续加注，方向是错的。
// English: the pairing leg. Buy-first sells an equal base amount (only base is sellable, the intraday
// layer is T+1 locked); sell-first buys the same amount back and, per Q8, unwinds on any block
// instead of retrying — chasing after a proven failure is the wrong direction.
func (e *Engine) pairTRoundLocked(code string, quotes map[string]*data.StockInfo, p TParams, force bool, now time.Time) {
	r := e.openTRoundFor(code)
	if r == nil {
		return
	}
	pos := e.positions[code]
	if pos == nil || pos.Qty <= 0 {
		e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0, "持仓已不在账，回转作废", now)
		return
	}
	price := tQuotePrice(quotes, code)
	if price <= 0 {
		return // 无有效价：不下结论、不动状态（与自动卖出的行情守卫同口径）
	}

	if r.Leg == tRLegBuy {
		need := r.LegQty
		fav := (price - r.LegPrice) / r.LegPrice * 100
		if !force && fav < p.MinSpreadPct {
			return // 价差没到：继续等（不追高卖，卖腿只负责把已实现的浮盈落袋）
		}
		sellable := e.sellableQtyLocked(pos, now)
		if sellable < need {
			if force {
				e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0,
					fmt.Sprintf("尾盘强制配平失败：可卖%d<需卖%d，机动仓将隔夜", sellable, need), now)
			}
			return
		}
		sold, err := e.sellQtyLocked(pos, price, need, "§T-MODE 正T卖出腿 "+r.ID)
		if err != nil {
			log.Printf("[paper] §T-MODE 正T卖出腿被拒 %s: %v（回转保持 open，下一轮再试）", code, err)
			return
		}
		e.finishTRoundLocked(r, tRStateDone, sold, e.applySellPriceLocked(price),
			(e.applySellPriceLocked(price)-r.LegPrice)*float64(sold),
			fmt.Sprintf("正T配对完成：买%.2f→卖%.2f 价差%.2f%%", r.LegPrice, e.applySellPriceLocked(price), fav), now)
		e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
			StrategyType: pos.StrategyType, Side: "sell", Kind: "正T卖出", Price: e.applySellPriceLocked(price),
			Qty: sold, Status: "filled", Reason: "§T-MODE " + r.ID, CreatedAt: now})
		return
	}

	// 反 T：先卖后买，第二腿是买回。
	if force {
		e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0,
			"尾盘未回踩：反T卖腿按普通减仓收摊（Q8 不追买）", now)
		return
	}
	fav := (r.LegPrice - price) / r.LegPrice * 100
	if fav < p.MinSpreadPct {
		return
	}
	need := r.LegQty
	if err := e.addToPositionLocked(pos, pos.Code, pos.Name, pos.Strategy, price, price, need,
		"§T-MODE 反T买回腿 "+r.ID); err != nil {
		// Q8：买回被拦即收摊，不重试不追买；被拒本身也留一条订单（事后能看出"为什么没买回来"）。
		e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
			StrategyType: pos.StrategyType, Side: "buy", Kind: "反T买回", Price: price, Qty: need,
			Status: "rejected", Reason: "买回被拦，按普通减仓收摊: " + err.Error(), CreatedAt: now})
		e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0,
			"买回被拦，按普通减仓收摊（不追买）: "+err.Error(), now)
		return
	}
	fill := price
	if e.cfg.SlippageBps > 0 {
		fill = price * (1 + e.cfg.SlippageBps/10000.0) // 与 addToPositionLocked 内部同口径
	}
	e.finishTRoundLocked(r, tRStateDone, need, fill, (r.LegPrice-fill)*float64(need),
		fmt.Sprintf("反T配对完成：卖%.2f→买%.2f 价差%.2f%%", r.LegPrice, fill, fav), now)
	e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
		StrategyType: pos.StrategyType, Side: "buy", Kind: "反T买回", Price: fill, Qty: need,
		Status: "filled", Reason: "§T-MODE " + r.ID, CreatedAt: now})
}

// finishTRoundLocked 收尾一条回转：置终态、记第二腿与锁定价差、落盘（须持锁调用）。
// 第二腿的成交本身已在 sellQtyLocked / addToPositionLocked 内落过盘，但那时回转还是 open 态，
// 所以这里必须再落一次——否则重启后一条已完成的回转会被读成"还欠一条腿"，次日直接收摊留痕。
// English: closes a round and persists again — the leg fill already persisted while the round was
// still open, so without this second write a restart would read a completed round as still pending.
func (e *Engine) finishTRoundLocked(r *TRound, state string, pairQty int, pairPrice, pnl float64, reason string, now time.Time) {
	r.State = state
	r.PairQty = pairQty
	r.PairPrice = pairPrice
	r.PnL = pnl
	r.ClosedAt = now
	r.Reason = reason
	log.Printf("[paper] §T-MODE 回转 %s %s(%s) 第%d轮 终态=%s 腿=%s %d股@%.2f 配对%d股@%.2f 价差%.2f 原因=%s",
		r.ID, r.Mode, r.Name, r.Seq, state, r.Leg, r.LegQty, r.LegPrice, pairQty, pairPrice, pnl, reason)
	e.trimTRoundsLocked()
	e.persist()
}

// trimTRoundsLocked 只保留最近 tRKeepRounds 条回转（须持锁调用；只影响账本体积，不影响任何判定）。
// English: keeps the most recent tRKeepRounds records (ledger size only, no decision impact).
func (e *Engine) trimTRoundsLocked() {
	if len(e.tRounds) <= tRKeepRounds {
		return
	}
	e.tRounds = append([]TRound(nil), e.tRounds[len(e.tRounds)-tRKeepRounds:]...)
}

// openTRoundsLocked 第二遍：为符合条件的持仓开新腿（须持锁调用）。
//
// 入场条件全部是"宁可少做"：有底仓、无未平机动仓、当日次数未用尽、无开腿、有实时价、基准价有效。
// 触发阈值统一相对 `AnchorOrCost()`（§ANCHOR-FIRST 的基准：有锚点用锚点，存量回落成本）——
// 做 T 的"低吸/高抛"必须相对**建仓那笔的价**，相对摊薄成本会随着做 T 本身不断漂移，
// 越做越容易触发，最后变成一台无脑换手机器。
// English: opens new legs only under conservative conditions. The trigger is measured against
// AnchorOrCost() on purpose: measuring against the diluted cost would drift every time a round
// completes, so the trigger would get easier and easier and degrade into mindless churn.
func (e *Engine) openTRoundsLocked(quotes map[string]*data.StockInfo, p TParams, day string, now time.Time) {
	codes := make([]string, 0, len(e.positions))
	for code := range e.positions {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, code := range codes {
		pos := e.positions[code]
		if pos == nil || pos.Qty <= 0 {
			continue
		}
		if e.openTRoundFor(code) != nil {
			continue // 已有开腿：先把它配平，绝不叠加第二条腿
		}
		if e.tRoundsUsedLocked(code, day) >= p.MaxRounds {
			continue
		}
		layers := LayersOfPosition(pos, now)
		if layers.BaseQty <= 0 {
			continue // 无底仓（含全仓当日买入）：T+1 下没有任何可卖量，做不了 T
		}
		if layers.IntradayQty > 0 {
			continue // 已有未平机动仓：先把旧的配平，不叠新的（否则机动仓会越滚越大）
		}
		price := tQuotePrice(quotes, code)
		if price <= 0 {
			continue
		}
		ref := pos.AnchorOrCost()
		if ref <= 0 {
			continue
		}
		capQty := T0Cap(pos.Qty, p.CapPct/100.0)

		// 正 T：现价相对基准跌够阈值 ⇒ 低吸买入机动仓。
		if p.BuyFirst {
			if dip := (ref - price) / ref * 100; dip >= p.MinSpreadPct {
				qty := lotFloor(capQty)
				if qty >= 100 {
					if e.openBuyFirstLocked(pos, layers.BaseQty, qty, price, day, now, dip) {
						continue // 同一轮同一代码只开一条腿
					}
				}
			}
		}
		// 反 T：现价相对基准涨够阈值 ⇒ 高抛一部分底仓（必须留一手）。
		if p.SellFirst {
			if rise := (price - ref) / ref * 100; rise >= p.MinSpreadPct {
				maxSell := layers.BaseQty - tRMinBaseKeep
				if capQty < maxSell {
					maxSell = capQty
				}
				if qty := lotFloor(maxSell); qty >= 100 {
					e.openSellFirstLocked(pos, layers.BaseQty, qty, price, day, now, rise)
				}
			}
		}
	}
}

// openBuyFirstLocked 正 T 第一腿：买入机动仓。返回 true 表示腿已成交并登记回转。
//
// 买入走 `addToPositionLocked`（与手动加仓同一条路径）：费用、滑点、池扣款、层账批次、展示态
// 全部一致——做 T 的买入没有任何理由绕开这些口径，否则"做 T 的仓位"会成为一个更便宜的资金来源。
// 池纪律/现金不足会直接返回错误，此时不开回转（不是失败，只是这一轮不做）。
// English: the buy-first opening leg goes through addToPositionLocked so fees, slippage, pool debit,
// lot ledger and display state all match a manual add-on. A blocked buy simply means no round this
// round — not a failure.
func (e *Engine) openBuyFirstLocked(pos *Position, baseQty, qty int, price float64, day string, now time.Time, dip float64) bool {
	if err := e.addToPositionLocked(pos, pos.Code, pos.Name, pos.Strategy, price, price, qty,
		"§T-MODE 正T买入腿"); err != nil {
		log.Printf("[paper] §T-MODE 正T买入腿未成交 %s(%s) %d股: %v", pos.Code, pos.Name, qty, err)
		return false
	}
	fill := price
	if e.cfg.SlippageBps > 0 {
		fill = price * (1 + e.cfg.SlippageBps/10000.0) // 与 addToPositionLocked 内部同口径
	}
	r := e.newTRoundLocked(pos, TRModeBuyFirst, tRLegBuy, qty, fill, baseQty, day, now,
		fmt.Sprintf("低吸触发：现价相对基准跌 %.2f%%", dip))
	e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
		StrategyType: pos.StrategyType, Side: "buy", Kind: "正T买入", Price: fill, Qty: qty,
		Status: "filled", Reason: "§T-MODE " + r.ID, CreatedAt: now})
	return true
}

// openSellFirstLocked 反 T 第一腿：卖出底仓的一部分（必然留 ≥ tRMinBaseKeep 股）。
//
// 刻意走 `sellQtyLocked` 而不是 `sellAllLocked`：后者会删持仓 + 触发清仓镜像 + 记再入场冷却，
// 三件事任何一件发生，买回腿都会被自家闸门挡死（方案 2.7）。也不置位 trimDone ——
// 做 T 不是减仓裁决，占了当日减仓槽就会把真正的退出裁决吞掉。
// English: deliberately uses sellQtyLocked, never sellAllLocked (which would delete the position,
// mirror a close and stamp the re-entry cooldown, blocking the buy-back), and never touches trimDone
// (a T leg is not a trim adjudication and must not consume the day's trim slot).
func (e *Engine) openSellFirstLocked(pos *Position, baseQty, qty int, price float64, day string, now time.Time, rise float64) {
	sold, err := e.sellQtyLocked(pos, price, qty, "§T-MODE 反T卖出腿")
	if err != nil {
		log.Printf("[paper] §T-MODE 反T卖出腿被拒 %s(%s) %d股: %v", pos.Code, pos.Name, qty, err)
		return
	}
	fill := e.applySellPriceLocked(price)
	r := e.newTRoundLocked(pos, TRModeSellFirst, tRLegSell, sold, fill, baseQty, day, now,
		fmt.Sprintf("高抛触发：现价相对基准涨 %.2f%%", rise))
	e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
		StrategyType: pos.StrategyType, Side: "sell", Kind: "反T卖出", Price: fill, Qty: sold,
		Status: "filled", Reason: "§T-MODE " + r.ID, CreatedAt: now})
}

// newTRoundLocked 登记一条 open 态回转并落盘（须持锁调用）。
// seq 按"该代码当日已开次数 + 1"生成，因此幂等键在单日单码内严格递增且不与既有键重复。
// English: registers an open round; seq is (rounds opened today for this code + 1), so the idempotency
// key is strictly increasing within one code-day and never collides.
func (e *Engine) newTRoundLocked(pos *Position, mode, leg string, qty int, price float64, baseQty int,
	day string, now time.Time, reason string) *TRound {
	seq := e.tRoundsUsedLocked(pos.Code, day) + 1
	r := TRound{
		ID:       fmt.Sprintf("t0:%s:%s:%s:%d", pos.Code, mode, day, seq),
		Code:     pos.Code,
		Name:     pos.Name,
		Mode:     mode,
		Seq:      seq,
		Leg:      leg,
		LegQty:   qty,
		LegPrice: price,
		LegAt:    now,
		BaseQty:  baseQty,
		State:    tRStateOpen,
		Day:      day,
		Reason:   reason,
	}
	e.tRounds = append(e.tRounds, r)
	log.Printf("[paper] §T-MODE 开腿 %s %s(%s) %s %d股 @%.2f 底仓%d股 原因=%s",
		r.ID, mode, pos.Name, leg, qty, price, baseQty, reason)
	e.trimTRoundsLocked()
	e.persist()
	return &e.tRounds[len(e.tRounds)-1]
}

// TRounds 全部回转记录的快照（只读；供接口与排障展示）。
// 返回副本而不是切片头：调用方在锁外遍历时，引擎可能正在 append，共享底层数组会读到撕裂的行。
// English: a read-only snapshot (a copy, so an out-of-lock reader can't observe a torn append).
func (e *Engine) TRounds() []TRound {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]TRound, len(e.tRounds))
	copy(out, e.tRounds)
	return out
}

// TRoundPnL 已完成的回转累计锁定价差（元，未扣第二腿费用），供收益归因与前端展示。
// 只统计 done 态：unwound 的那一腿已经并进普通持仓/减仓口径，再计一次就是重复记账。
// English: accumulated locked spread of completed rounds only — unwound legs already land in the
// normal position/trim accounting, so counting them again would double-book.
func (e *Engine) TRoundPnL() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	sum := 0.0
	for i := range e.tRounds {
		if e.tRounds[i].State == tRStateDone {
			sum += e.tRounds[i].PnL
		}
	}
	return sum
}

// tQuotePrice 取实时快照价（缺失/非法返回 0，调用方按"无有效价"跳过）。
// English: live snapshot price, 0 when missing/invalid (callers treat 0 as "no valid price").
func tQuotePrice(quotes map[string]*data.StockInfo, code string) float64 {
	if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
		return q.Price
	}
	return 0
}

// lotFloor 向下取整到整手（1 手 = 100 股）。做 T 的两条腿都必须整手，零股只能清仓卖出。
// English: floor to whole lots; both T legs must be whole lots (odd shares are close-only).
func lotFloor(qty int) int {
	if qty < 100 {
		return 0
	}
	return qty / 100 * 100
}
