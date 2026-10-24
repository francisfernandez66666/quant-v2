// t1_partial.go — §T1-PARTIAL（2026-10-24 owner 裁决三，施工波次 W4）。
//
// 裁决三原文：「加仓后遇到退出，先只卖可卖部分（就是做正 T）」。
// 改前的形态：模拟盘的 T+1 是**整仓二值**——一个持仓只有一个 FilledAt，且加仓从不刷新它
// （paper.go 的 addToPositionLocked 旧实现只改 Cost/Qty/CostPrice/Mark）。于是同一持仓里
// "昨日底仓 + 当日新买机动仓" 无法区分，卖出入口只有两种错法：
//   - 底仓昨日成交、今日加仓 ⇒ 整仓按旧日期判"可卖"，当日买入的那部分被**漏放**（少拦，方向危险）；
//   - 全仓今日买入 ⇒ 一笔卖出整单报错，退出裁决被 T+1 完全挡死（该卖的卖不掉）。
//
// 本文件把两者都收掉：持仓改按**批次层账**（PositionLot）记账，可卖量按批汇总；
// 卖出入口从"报错"改成"减量成交"，卖不掉的那部分登记为**残量退出意图**（PendingExit），
// 次一交易日无条件继续卖（Q6 采纳：退出是风险裁决，不是价格博弈，敞口挂着比卖早了更贵）。
//
// English: base layer ledger for ruling #3 — positions keep per-batch fill dates, the sellable
// quantity is summed from batches (closing the same-day add-on leak), and a close/trim that hits the
// T+1 wall now executes the sellable part instead of erroring, recording the unsellable residual as a
// PendingExit that runs unconditionally on the following trading days (valid 3 trading days).
package paper

import (
	"fmt"
	"log"
	"sort"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// PositionLot 一个持仓里"同一交易日买入"的最小记账单元（§T1-PARTIAL 层账）。
// 层账只回答一件事：这批股票是哪一天成交的 ⇒ 今天能不能卖（A 股 T+1）。
// Price 记的是该批**含费**每股成本，仅供批次审计与对照展示；盈亏与卖出结算基准仍是
// Position.CostPrice（含费摊薄），§FIX#3 的减仓摊销口径本批刻意不动——因此一次部分减仓之后
// Σ(lot.Qty×lot.Price) 与 p.Cost 不再严格相等，这是有意取舍（不为层账去改一条已被门禁锁死的
// 成本口径，两套真值只会更糟）。
// English: one same-day buy batch. Price is the fee-inclusive per-share cost of that batch, kept for
// audit only; settlement still uses the diluted Position.CostPrice (§FIX#3 unchanged), so after a partial
// trim the lot-weighted cost may drift from p.Cost by design.
type PositionLot struct {
	Qty      int       `json:"qty"`       // 该批股数
	Price    float64   `json:"price"`     // 该批含费每股成本（审计用，非结算基准）
	FilledAt time.Time `json:"filled_at"` // 该批成交时间（T+1 判日唯一依据）
}

// addBuyLot 买入成交后追加批次（建仓与加仓共用，须持锁调用；调用点负责在此之前/之后维护 p.Qty）。
// 同日多笔合并成一批：既让"当日机动仓"天然是单条记录（做 T 的配平判定要用），也把层账长度钉在
// "持仓经过的交易日数"这一量级，不会随加仓次数无限增长。
// ⚠ 首次给一条无层账的存量持仓追加批次时，先把**已有持仓**补写成一批（日期取 p.FilledAt）——
// 否则层账只覆盖这一笔加仓，可卖量会凭空少掉旧股（该卖的卖不掉）。
// English: appends a buy batch (shared by open and add-on), merging same-day fills into one lot so the
// intraday layer stays a single record and the ledger size is bounded by trading days held. Seeding the
// pre-existing shares as a batch is mandatory when a legacy position (no lots yet) gets its first add-on,
// otherwise sellable would silently drop the older shares.
func (p *Position) addBuyLot(qty int, perShareCost float64, at time.Time) {
	if p == nil || qty <= 0 {
		return
	}
	// 存量持仓首笔加仓：旧股先成批，日期沿用整仓成交时间（与改前判据同源，不新造事实）。
	if len(p.Lots) == 0 && p.Qty > 0 {
		p.Lots = append(p.Lots, PositionLot{Qty: p.Qty, Price: p.CostPrice, FilledAt: p.FilledAt})
	}
	last := len(p.Lots) - 1
	if last >= 0 && cntime.DayOf(p.Lots[last].FilledAt) == cntime.DayOf(at) {
		// 同日合并：数量相加、含费成本按量加权，成交时间保留该批**首笔**（判日同值，不必改写）。
		prev := p.Lots[last]
		total := prev.Qty + qty
		merged := PositionLot{Qty: total, Price: prev.Price, FilledAt: prev.FilledAt}
		if total > 0 && perShareCost > 0 {
			merged.Price = (prev.Price*float64(prev.Qty) + perShareCost*float64(qty)) / float64(total)
		}
		p.Lots[last] = merged
		return
	}
	p.Lots = append(p.Lots, PositionLot{Qty: qty, Price: perShareCost, FilledAt: at})
}

// LayersOfPosition 用层账汇总底仓/机动仓（§T1-PARTIAL 的判据入口，也是 t0.go 那份"已设计未接线"
// 数学的第一条生产接线）。无层账的存量持仓走 AllocLayers 的整仓二值口径，行为与改前逐笔一致。
// WeightedCost 刻意取 Position.CostPrice 而不是批次加权：结算与风险线各自只有一把真值（摊薄成本、
// 首买锚点），层账不再造第三个价格口径。
// English: aggregates base/intraday layers from the lot ledger — the first production consumer of t0.go's
// previously unwired math. Legacy positions without lots fall back to the old whole-position binary rule.
// WeightedCost deliberately reports the diluted CostPrice so the lot ledger never becomes a third cost
// source of truth.
func LayersOfPosition(p *Position, now time.Time) PositionLayers {
	if p == nil {
		return PositionLayers{}
	}
	if len(p.Lots) == 0 {
		return AllocLayers(p.Qty, p.CostPrice, p.FilledAt, now)
	}
	var base, intraday int
	for _, l := range p.Lots {
		if l.Qty <= 0 {
			continue
		}
		// 判日口径与改前完全一致（北京时区 day 判定，零值时间视为可卖的历史数据）。
		if canSellToday(l.FilledAt, now) {
			base += l.Qty
		} else {
			intraday += l.Qty
		}
	}
	out := PositionLayers{
		BaseQty:      base,
		IntradayQty:  intraday,
		Sellable:     base,
		TotalQty:     p.Qty,
		WeightedCost: p.CostPrice,
	}
	// 层账数量与持仓数量不一致只可能来自历史数据被手工编辑；以持仓量封顶，宁可少卖不多卖。
	if out.Sellable > out.TotalQty {
		out.Sellable = out.TotalQty
	}
	return out
}

// sellableQtyLocked 当前可卖股数——两个卖出入口共用的新判据，取代旧的整仓二值 sellCheckT1Locked。
// 返回值 0 表示"今日一股都卖不掉"（等价于改前的 errTPlusOne 场景）；返回正数但小于 p.Qty 时，
// 调用方按可卖量减量成交，残量转 PendingExit。
// English: shares sellable right now (replaces the old binary T+1 guard). Zero reproduces the former
// errTPlusOne case; a positive value below the position size means "sell this much now, park the rest".
func (e *Engine) sellableQtyLocked(p *Position, now time.Time) int {
	return LayersOfPosition(p, now).Sellable
}

// consumeSellableLots 从可卖批次里按 FIFO（最早一天先出）扣减 qty 股，扣空的批次整批移除。
// 只动可卖批次、且只从最早的那天动 ⇒ 层账自己就是"卖了哪几天"的台账，当日机动仓永不被这笔扣减
// 误伤（它此刻还不可卖，也就绝不可能出现在扣减集合里）。
// English: consumes qty shares FIFO from the sellable batches only, dropping exhausted batches. The
// intraday layer can never be touched here because it is not in the sellable set.
func consumeSellableLots(p *Position, qty int, now time.Time) {
	if p == nil || qty <= 0 || len(p.Lots) == 0 {
		return
	}
	out := make([]PositionLot, 0, len(p.Lots))
	left := qty
	for _, l := range p.Lots {
		if left > 0 && l.Qty > 0 && canSellToday(l.FilledAt, now) {
			take := l.Qty
			if take > left {
				take = left
			}
			l.Qty -= take
			left -= take
		}
		if l.Qty > 0 {
			out = append(out, l)
		}
	}
	p.Lots = out
}

// 残量退出意图的动作档（去重键的一维：3.3 要求意图与"每日一次减仓"互不吞并，
// 同码只留一条意图、清仓档不被减仓档降级）。
const (
	pendingExitClose = "close" // 清仓裁决没卖完的残量（次日整笔退出）
	pendingExitTrim  = "trim"  // 减仓裁决没卖完的残量（次日补到该减的量）
)

// pendingExitValidDays 残量意图有效期（交易日）——Q6 采纳的边界：无条件执行，但不无限追卖。
// 过期只做留痕、不再补卖，理由必须落在订单流水里，否则事后看不出"当初为什么没卖完"。
// English: Q6's validity window — residual exits run unconditionally but only for 3 trading days, after
// which they are audited as expired instead of chasing the sale forever.
const pendingExitValidDays = 3

// refreshSellStatusLocked 按层账刷新持仓的展示态（须持锁调用；建仓/加仓/减仓三条路径各刷一次）。
// 判据只有一条：一部分能卖、一部分不能卖 ⇒ 标"部分可卖"，其余（全可卖 / 一股都不可卖）留空。
// "一股都不可卖"不单独设标签，因为它的读数就在 sellable_qty=0 上；把两个维度挤进同一个字符串，
// 前端就无法分别显示"卖得掉多少"与"为什么卖不掉"。
// English: refreshes the display status from the lot ledger — only the mixed case (part sellable, part
// locked) is labelled; the fully-locked case is already visible as sellable_qty=0.
func (p *Position) refreshSellStatusLocked(now time.Time) {
	if p == nil {
		return
	}
	sellable := LayersOfPosition(p, now).Sellable
	if sellable > 0 && sellable < p.Qty {
		p.Status = statusPartSellable
		return
	}
	p.Status = ""
}

// PendingExit 残量退出意图：一次退出裁决因 T+1 只执行了一部分，剩下的量登记在此，
// 次一交易日由 runPendingExitsLocked 无条件卖出。随 persistedState 落盘，跨重启不丢。
// ExpireOn 用 YYYYMMDD 紧凑日（与 FiredDay 同口径，字典序比较才等价于日期比较）。
// English: an unsold residual from an exit adjudication, executed unconditionally from the next trading
// day until it completes or ExpireOn passes (both dates in the same compact-day regime, so lexicographic
// comparison is date comparison).
type PendingExit struct {
	Code     string    `json:"code"`      // 标的代码
	WantQty  int       `json:"want_qty"`  // 尚需卖出的股数
	Kind     string    `json:"kind"`      // close / trim（见上）
	Reason   string    `json:"reason"`    // 触发该退出的裁决文案（留痕与展示用）
	FiredAt  time.Time `json:"fired_at"`  // 意图登记时刻
	FiredDay string    `json:"fired_day"` // 登记当日 YYYYMMDD（当日不自家补卖）
	ExpireOn string    `json:"expire_on"` // 最后有效交易日 YYYYMMDD（晚于它即过期）
}

// recordPendingExitLocked 登记或合并残量退出意图（须持锁调用）。
// 合并规则防止"反复触发的信号把有效期无限顺延"：同码已存在意图时保留最初的触发日与到期日，
// 只刷新残量，并把动作档取更强的一个（close > trim，清仓残量不会被随后一条减仓信号降级）。
// English: records or merges a residual exit. An existing intent keeps its original firing day and expiry
// (later signals must not extend the window), refreshes the residual quantity, and upgrades close over
// trim so a half-size signal can't downgrade a full-exit intent.
func (e *Engine) recordPendingExitLocked(code string, want int, kind, reason string, now time.Time) {
	if code == "" || want <= 0 {
		return
	}
	if e.pendingExits == nil {
		e.pendingExits = make(map[string]*PendingExit)
	}
	day := cntime.DayCompactOf(now)
	if ex := e.pendingExits[code]; ex != nil {
		ex.WantQty = want
		ex.Reason = reason
		if ex.Kind == pendingExitClose || kind == pendingExitClose {
			ex.Kind = pendingExitClose
		}
		if ex.FiredDay == "" {
			ex.FiredDay = day
		}
		return
	}
	e.pendingExits[code] = &PendingExit{
		Code:     code,
		WantQty:  want,
		Kind:     kind,
		Reason:   reason,
		FiredAt:  now,
		FiredDay: day,
		ExpireOn: data.AddTradingDays(day, pendingExitValidDays),
	}
	log.Printf("[paper] §T1-PARTIAL 残量退出意图 %s 待卖%d股 档=%s 到期=%s 原因=%s", code, want, kind, e.pendingExits[code].ExpireOn, reason)
	e.persist()
}

// PendingExits 当前在册的残量退出意图（按代码排序，供只读接口/前端展示与排障）。
// English: the live residual-exit intents, code-sorted, for read-only display and troubleshooting.
func (e *Engine) PendingExits() []PendingExit {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]PendingExit, 0, len(e.pendingExits))
	for _, pe := range e.pendingExits {
		if pe != nil {
			out = append(out, *pe)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// RunPendingExits 残量意图的执行入口：交易时段每轮由纸面估值轮调用（不依赖本轮是否有卖出信号，
// 否则"价格回升后裁决不再给 close ⇒ 残量永远卖不掉"这条 3.3 的死角依然存在）。
// AutoSell 关闭时不擅自执行——用户关掉自动卖出就是明确接管，意图保留在册直到过期留痕。
// English: execution entry, called each in-session round by the mark loop (independent of whether this
// round produced sell signals). Disabled when AutoSell is off: that switch means the user took over.
func (e *Engine) RunPendingExits(quotes map[string]*data.StockInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled {
		return
	}
	e.runPendingExitsLocked(quotes, time.Now())
}

// runPendingExitsLocked 逐条处理残量意图（须持锁调用）。
// 处理次序：持仓已不在账 → 注销；超过有效期 → 拒绝留痕并注销；触发当日 → 跳过（残量正是当日买入，
// 本就不可卖）；无有效价 → 跳过（宁可不卖也不按错价记账，与 autoSellLocked 行情守卫同口径）；
// 其余按 min(意图量, 可卖量, 持仓量) 卖出，成交即注销意图。
// 代码顺序先排序再遍历：直接在 map 上 range 时，卖出路径可能新增/删除键，遍历次序与可见集合
// 都会漂（同轮多码时留下"这一轮卖到哪条"的不确定性）。
// English: processes intents in sorted code order (mutating the map mid-range would make the visited set
// and order non-deterministic), dropping flat/expired intents, skipping same-day and unpriced rounds, and
// selling min(intent, sellable, position) otherwise.
func (e *Engine) runPendingExitsLocked(quotes map[string]*data.StockInfo, now time.Time) {
	if len(e.pendingExits) == 0 {
		return
	}
	if !e.cfg.AutoSell {
		return
	}
	today := cntime.DayCompactOf(now)
	codes := make([]string, 0, len(e.pendingExits))
	for code := range e.pendingExits {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		pe := e.pendingExits[code]
		if pe == nil {
			delete(e.pendingExits, code)
			continue
		}
		p, held := e.positions[code]
		if !held || p == nil || p.Qty <= 0 {
			// 持仓已整笔不在账（手动清仓/清盘重置）⇒ 意图失去对象，静默注销（不重复留痕）。
			delete(e.pendingExits, code)
			continue
		}
		if pe.ExpireOn != "" && today > pe.ExpireOn {
			e.recordOrderLocked(Order{Code: p.Code, Name: p.Name, Strategy: p.Strategy,
				StrategyType: p.StrategyType, Side: "sell", Kind: "残量退出", Qty: pe.WantQty,
				Status: "rejected", Reason: fmt.Sprintf("残量意图超过 %d 个交易日不再补卖", pendingExitValidDays),
				CreatedAt: now})
			log.Printf("[paper] §T1-PARTIAL 残量意图过期 %s 待卖%d股 到期=%s", code, pe.WantQty, pe.ExpireOn)
			delete(e.pendingExits, code)
			continue
		}
		if pe.FiredDay == today {
			continue
		}
		var price float64
		if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
			price = q.Price
		} else {
			continue
		}
		sellable := e.sellableQtyLocked(p, now)
		if sellable <= 0 {
			continue
		}
		want := pe.WantQty
		if want > sellable {
			want = sellable
		}
		if want > p.Qty {
			want = p.Qty
		}
		if want <= 0 {
			delete(e.pendingExits, code)
			continue
		}
		reason := pe.Reason
		if reason == "" {
			reason = "残量退出"
		}
		// 清仓档且这次能一次卖光 ⇒ 走完整清仓（删持仓 + 生命周期镜像 + 再入场冷却），
		// 否则只按股数减仓（持仓留着，还能继续被后续裁决处置）。
		// whole 必须在卖出**之前**取：sellAllLocked 不写零 p.Qty（持仓是从 map 里删的），
		// sellQtyLocked 又会即时减量，卖完再比就会把"清光"误判成"部分成交"。
		whole := p.Qty
		soldAll := pe.Kind == pendingExitClose && want >= whole
		var (
			sold int
			err  error
		)
		if soldAll {
			sold, err = e.sellAllLocked(p, price, reason)
		} else {
			sold, err = e.sellQtyLocked(p, price, want, reason)
		}
		if err != nil {
			log.Printf("[paper] §T1-PARTIAL 残量执行被拒 %s: %v", code, err)
			continue
		}
		status := "partial"
		if soldAll && sold >= whole {
			status = "filled"
		}
		e.recordOrderLocked(Order{Code: p.Code, Name: p.Name, Strategy: p.Strategy,
			StrategyType: p.StrategyType, Side: "sell", Kind: "残量退出", Price: price, Qty: sold,
			Status: status, Reason: reason, CreatedAt: now})
		// 本轮没卖完的剩余量**回到同一把意图上继续追**（合并保留最初触发日与到期日，所以有效期不会被
		// 无限顺延）。若不这样处理，本轮可卖量小于意图量时（例如残量里还夹着另一天买的批次）就会
		// 删掉意图、把退出裁决静默丢弃——正是裁决三要收掉的那类"该卖的没卖、也没人知道"。
		// 清仓档且持仓已被整笔删除 ⇒ 退出完成，不再有残量。
		// English: an unfinished intent is re-armed (merge keeps the original fired day/expiry, so the
		// window can't be extended) instead of being dropped — otherwise a round whose sellable qty fell short
		// would silently discard the exit adjudication. A close that deleted the position is complete.
		rem := pe.WantQty - sold
		if soldAll {
			rem = 0
		}
		if rem > 0 {
			e.recordPendingExitLocked(code, rem, pe.Kind, reason, now)
		} else {
			delete(e.pendingExits, code)
		}
		log.Printf("[paper] §T1-PARTIAL 残量退出成交 %s(%s) %d股 @%.2f 档=%s 余%d股", code, p.Name, sold, price, pe.Kind, rem)
	}
}
