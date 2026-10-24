// anchor_shadow.go — §ANCHOR-FIRST（2026-10-24 裁决二「止盈止损点和动态加减仓按首次买入价做锚点」）
// 的 **W2 影子对照层**。
//
// 这一层存在的唯一理由：裁决二把风险线（止盈/止损/移动止盈/深破）的分母从"摊薄成本"换成
// "首次买入价"，而**上线那一刻每条在持仓的风险线都会移动**。高位加仓过的票，锚点低于当前成本
// ⇒ 止损线整体下移（更难触发，但单票最大亏损变大）；低位摊薄的票相反。这种事绝不能"改完直接切、
// 看盘感觉不对再回滚"——方案 Q10 要求 owner 先看过逐票的线位移动清单并签认，才允许 W3 切硬。
//
// 因此本文件只做一件事：**同一笔持仓，用两个基准各算一遍线位，把差异列出来**。
// 它不参与任何处置（sellProbe 一行都没读 AnchorPrice），也不写任何账。
//
// 判线口径与 sellProbe ② 分支同阈值、同顺序（深破 → 止损 → 止盈 → 移动止盈），但刻意**不含**
// 状态机那三条腿：观察窗/结算栅格、做多新鲜度延持、利空双源即时硬清。原因是这三条由跨轮状态
// 决定，无状态复现它们只会造出一份"看起来更完整、实际对不上内核"的表。表上标注的是
// 「本轮价格落在线的哪一侧」，正是切硬后每天第一个会变的量。
//
// English: the W2 shadow-comparison layer for ruling 2. It recomputes every risk line twice —
// once against the diluted cost (today's behaviour) and once against the frozen first-buy anchor —
// and lists the divergence so the owner can sign off line movement before anything switches over.
// It is read-only: sellProbe never reads AnchorPrice, and no book is written here.
package signalctl

import "time"

// AnchorShadowRow 一笔持仓一行对照读数（三本账共用同一形状，Book 区分来源）。
// 字段全部带 json tag：engine 侧按日落 anchor_shadow_<日>.json 供 owner 复核（Q10 签认材料）。
// English: one comparison row per position; JSON-tagged because the engine persists it daily.
type AnchorShadowRow struct {
	Book      string  `json:"book"`       // live / paper / report（账本来源）
	Code      string  `json:"code"`       // 持仓主键代码（各账本原样，不重新归一）
	Name      string  `json:"name"`       // 名称（展示用）
	CurPrice  float64 `json:"cur_price"`  // 本轮有效现价（≤0=本轮无有效价，线位照算、涨跌幅不判）
	HighPrice float64 `json:"high_price"` // 持仓期最高价（移动止盈基准；≤0 内核回退建仓价）

	// ── 两个基准与它们之间的偏移（本次切硬的全部原因） ──
	CostPrice   float64 `json:"cost_price"`   // 摊薄成本（现口径，盈亏与风险线共用）
	AnchorPrice float64 `json:"anchor_price"` // 首次买入价锚（裁决二口径；0=未知）
	AnchorKnown bool    `json:"anchor_known"` // 锚点是否已回填（false 时本行只登记、不判差异）
	ShiftPct    float64 `json:"shift_pct"`    // (锚点-成本)/成本×100：正=锚点高于成本（线位上移）

	// ── 阈值（本账号生效纪律，两侧同一套，差别只在分母） ──
	StopLossPct   float64 `json:"stop_loss_pct"`
	TakeProfitPct float64 `json:"take_profit_pct"`
	PullbackPct   float64 `json:"pullback_pct"`
	DeepMult      float64 `json:"deep_mult"`

	// ── 线位（价位）：切硬后每条线会挪到这里 ──
	StopPriceByCost   float64 `json:"stop_price_by_cost"`
	StopPriceByAnchor float64 `json:"stop_price_by_anchor"`
	DeepPriceByCost   float64 `json:"deep_price_by_cost"`
	DeepPriceByAnchor float64 `json:"deep_price_by_anchor"`
	TakePriceByCost   float64 `json:"take_price_by_cost"`
	TakePriceByAnchor float64 `json:"take_price_by_anchor"`

	// ── 触线判定与资格（无状态口径，不含观察窗/延持/利空硬清） ──
	// 判线用中文名而非枚举数字：这张表是给人签认的（Q10），`"line_by_cost": 1` 要对着源码才知道
	// 是止损线，等于没写。内核侧的枚举不变，这里只是对照表的展示层。
	// English: lines are rendered as their Chinese labels — this table exists for a human sign-off,
	// and a bare enum number would force the reader back into the source. The kernel keeps the enum.
	LineByCost         string  `json:"line_by_cost"`
	LineByAnchor       string  `json:"line_by_anchor"`
	TrailReadyByCost   bool    `json:"trail_ready_by_cost"`
	TrailReadyByAnchor bool    `json:"trail_ready_by_anchor"`
	LineChanged        bool    `json:"line_changed"`  // 两侧判线不同 ⇒ 切硬当刻这条票的行为会变
	PnlByCost          float64 `json:"pnl_by_cost"`   // 按成本口径的涨跌幅
	PnlByAnchor        float64 `json:"pnl_by_anchor"` // 按锚点口径的涨跌幅
}

// anchorLineAt 以给定基准价判"本轮价格落在哪条线上"：与 sellProbe ② 同阈值、同顺序，
// 但不带状态机（观察窗、做多延持、利空硬清都留给内核）。
// English: which line this round's price sits on for one basis — same thresholds and order as
// sellProbe's ② block, without the state machine legs.
func anchorLineAt(basis, cur, high float64, p sellParams) SellLine {
	if basis <= 0 || cur <= 0 {
		return SellLineNone // 停牌/缺价：不下结论（与内核 in.EntryPrice<=0 早退同口径）
	}
	pnl := (cur - basis) / basis * 100
	switch {
	case pnl <= -p.sl*p.dm:
		return SellLineDeepBreach
	case pnl <= -p.sl:
		return SellLineStopLoss
	case pnl >= p.tp:
		// 内核在这一支里还会先看 bullLive（有活跃做多信号则延持不下单），表只记线位。
		if high > 0 && (high-cur)/high*100 >= p.pb {
			return SellLineTrail
		}
		return SellLineTakeProfit
	}
	if high > basis && (high-cur)/high*100 >= p.pb {
		return SellLineTrail // 已有利润、回撤超阈值 → 移动止盈
	}
	return SellLineNone
}

// anchorTrailReady 判断移动止盈的**资格**腿（持仓期最高价是否高于基准）。
// 这正是裁决二的额外收益所在：高位加仓把 CostPrice 抬到最高价之上时，
// `high > cost` 今天恒假、移动止盈资格被抹掉；换成首买价锚点后资格回来了。
// English: the trailing-stop eligibility leg (high > basis). This is exactly where ruling 2 buys
// back eligibility that a diluted cost had silently cancelled.
func anchorTrailReady(basis, high float64) bool {
	return basis > 0 && high > basis
}

// CompareAnchor 对一笔持仓做「成本口径 vs 锚点口径」的双侧对照。
// pol 提供该账号生效纪律阈值（与裁决内核同一份 Policy，绝不在表里另配一套参数）。
// AnchorPrice<=0 ⇒ AnchorKnown=false：只登记"该行没有锚点"，两侧判线保持相同，
// 差异计数不得把未回填行算成"线位没变"（那会让回填前的对照表恒绿＝恒盲）。
// English: compares the two bases for one position using the account's live Policy thresholds.
// An unknown anchor is registered as unknown — never silently folded into "no divergence".
func CompareAnchor(in SellInput, pol Policy, book string) AnchorShadowRow {
	// 阈值取该账号生效纪律的默认兜底快照（与裁决内核同一套 sellParamsOf，绝不在表里另配参数）；
	// 高点的回退基准取成本（与 sellProbe 播种同口径），保证两侧共用同一个"持仓期最高价"读数。
	p := sellParamsOf(pol.Discipline)
	high := in.HighPrice
	if high <= 0 {
		high = in.EntryPrice // 与内核 sellProbe 的播种口径一致（无高点时用成本起算）
	}
	row := AnchorShadowRow{
		// 标识与两个基准：账本来源/代码/摊薄成本/首买锚点/本轮现价/持仓期最高价。
		// 名称不在这里填——SellInput 没有名称字段（内核判线不需要它），由调用方从各自账本的
		// 持仓行带过来（见 engine/sell_shadow.go），纯展示用。
		Book:        book,
		Code:        in.Code,
		CostPrice:   in.EntryPrice,
		AnchorPrice: in.AnchorPrice,
		CurPrice:    in.CurPrice,
		HighPrice:   in.HighPrice,
		// 阈值回显：签认的人必须能在同一行看到"这条线是按哪套阈值算的"，
		// 否则线位移动多少无从复核（阈值来自本账号生效纪律，两侧同一套）。
		StopLossPct:   p.sl,
		TakeProfitPct: p.tp,
		PullbackPct:   p.pb,
		DeepMult:      p.dm,
	}
	if in.AnchorPrice > 0 && in.EntryPrice > 0 {
		row.AnchorKnown = true
		row.ShiftPct = (in.AnchorPrice - in.EntryPrice) / in.EntryPrice * 100
	}
	// 线位：锚点未知时 ByAnchor 一侧留 0（不是"和成本一样"，是"算不出来"）。
	if in.EntryPrice > 0 {
		row.StopPriceByCost = in.EntryPrice * (1 - p.sl/100)
		row.DeepPriceByCost = in.EntryPrice * (1 - p.sl*p.dm/100)
		row.TakePriceByCost = in.EntryPrice * (1 + p.tp/100)
		row.TrailReadyByCost = anchorTrailReady(in.EntryPrice, in.HighPrice)
		row.LineByCost = anchorLineAt(in.EntryPrice, in.CurPrice, high, p).String()
		if in.CurPrice > 0 {
			row.PnlByCost = (in.CurPrice - in.EntryPrice) / in.EntryPrice * 100
		}
	}
	if in.AnchorPrice > 0 {
		row.StopPriceByAnchor = in.AnchorPrice * (1 - p.sl/100)
		row.DeepPriceByAnchor = in.AnchorPrice * (1 - p.sl*p.dm/100)
		row.TakePriceByAnchor = in.AnchorPrice * (1 + p.tp/100)
		row.TrailReadyByAnchor = anchorTrailReady(in.AnchorPrice, in.HighPrice)
		// 高点的回退基准仍取成本（账本里的 historical high 与成本同源播种），避免把
		// "锚点未知时的高点口径"再造一个第三种说法。
		row.LineByAnchor = anchorLineAt(in.AnchorPrice, in.CurPrice, high, p).String()
		if in.CurPrice > 0 {
			row.PnlByAnchor = (in.CurPrice - in.AnchorPrice) / in.AnchorPrice * 100
		}
	}
	row.LineChanged = row.AnchorKnown && row.LineByCost != row.LineByAnchor
	return row
}

// AnchorShadowSummary 对照表总账（日志一行读完就知道这轮有没有得签认）。
// Unknown 单列：回填完成前它必须一直可见，否则"扫过 40 行、差异 0 行"会被误读成
// "切硬不影响任何持仓"。
// English: ledger totals. Unknown stays its own bucket — otherwise "40 rows, 0 divergences"
// would read as "the switch changes nothing" while every anchor is still unfilled.
type AnchorShadowSummary struct {
	RoundAt  time.Time      `json:"round_at"`
	Day      string         `json:"day"` // 北京时区交易日（去重与归档口径）
	Rows     int            `json:"rows"`
	Diverged int            `json:"diverged"` // LineChanged=true 的行数
	Shifted  int            `json:"shifted"`  // 线位价位发生移动的行数（含未触线）
	Unknown  int            `json:"unknown"`  // 锚点未回填的行数
	ByBook   map[string]int `json:"by_book"`
}

// SummarizeAnchorRows 汇总一批对照行（不改变任何行内容）。
// English: totals over a batch of rows, contents untouched.
func SummarizeAnchorRows(rows []AnchorShadowRow, day string, now time.Time) AnchorShadowSummary {
	s := AnchorShadowSummary{RoundAt: now, Day: day, ByBook: map[string]int{}}
	for _, r := range rows {
		s.Rows++
		s.ByBook[r.Book]++
		if !r.AnchorKnown {
			s.Unknown++
			continue
		}
		if r.LineChanged {
			s.Diverged++
		}
		if r.StopPriceByAnchor != r.StopPriceByCost {
			s.Shifted++
		}
	}
	return s
}
