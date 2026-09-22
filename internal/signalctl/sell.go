// sell.go — 卖出统一裁决通道（§SELLPOINT-UNIFY 2026-09-21，docs/REFACTOR_UNIFIED_SELL_20260921.md）。
//
// 架构定位：与买入确认窗完全对称的卖出侧唯一裁决器。五路探测器（战法退出/卖点评估/情绪退潮/
// 利空归因/价格探针）只产证据，全部处置结论（止盈/止损/减仓/延持/观察）只能从本通道出——
// 三态裁定 pass=处置（全平 close/半平 trim）、hold=观察窗内延持或等待、无结论=不动作，
// 与买入共用同一留痕环（record/JSONL），"为什么没卖/为什么卖了"同端点可查。
//
// 裁决语义（owner 已拍板，§六/§七实施基准）：
//   - 硬线判定：止损 −StopLossPct / 止盈 +TakeProfitPct / 移动止盈=最高价−MaxPullbackPct /
//     深破兜底=止损线×DeepStopMult（默认 −12）；
//   - 触线≠即卖：首触锁定判定线与固定结算点（对齐 09:30 栅格，边界⑤不随价格波动滚动），
//     窗结算时窗内有新鲜做多信号 → 延持一个窗长（信号在就拿着），无信号 → 处置；
//   - 延持只认做多信号 hasBull（语义②，修正旧实现止损窗 hasBear→延持的方向性错误）；
//     做多信号必须新鲜（边界⑥：产出时刻超 SellSignalMaxAgeSec 按无信号）；
//   - 利空即时硬清（语义①）：触止损线 + 利空且验证等级=双源（§八护栏4）→ 当轮直接全平、不等窗；
//     未触线或验证不足双源 → 无处置资格（预警由展示层投影，不在此出）；
//   - 深破无条件全清（边界④）：深破窗结算无视任何信号直接全平，不给延持——这是"延持止损"
//     这个无界下行的定价上限（FIX#12 时代 −13% 扛单数日的事故路径）；
//   - 结算处置后处置单每轮重放（幂等由执行层日级键兜底），已平仓代码由 PruneSellStates 清理。
//
// 修复的旧实现缺陷（internal/trading/discipline.go，本通道为唯一口径后旧路退役）：
//
//	a) 窗结算"有利空→延持"方向写反（discipline.go:200-206 vs 自身 :90/:156/:199 注释）；
//	b) 结算时命中"有信号延持"后 Settled=true/Confirmed=false 被顶部早退永久吞掉——
//	   一次延持即永久失明，之后信号消失、跌到深破都不再裁决。本实现延持只顺延结算点，
//	   状态机持续运转。
package signalctl

import (
	"fmt"
	"time"

	"quant-trading-v2/internal/config"
)

// SellLine 单持仓当前锁定的判定线类型。
// （SellLine: the discipline line a position has locked onto.）
type SellLine int

const (
	// SellLineNone 未命中任何判定线。
	SellLineNone SellLine = iota
	// SellLineStopLoss 止损线（浮亏 ≥ StopLossPct）。
	SellLineStopLoss
	// SellLineTakeProfit 止盈线（盈利 ≥ TakeProfitPct）。
	SellLineTakeProfit
	// SellLineTrail 移动止盈线（有利润后自最高价回撤 ≥ MaxPullbackPct）。
	SellLineTrail
	// SellLineDeepBreach 深破兜底（浮亏 ≥ StopLossPct×DeepStopMult）。
	SellLineDeepBreach
)

// String 判定线中文名（留痕/理由文案用）。
func (l SellLine) String() string {
	switch l {
	case SellLineStopLoss:
		return "止损线"
	case SellLineTakeProfit:
		return "止盈线"
	case SellLineTrail:
		return "移动止盈"
	case SellLineDeepBreach:
		return "深破兜底"
	default:
		return "未触线"
	}
}

// 利空验证等级（§八护栏4：Blocked 验证等级绑定处置权限）。
// 未触线的利空一律无处置资格；触线即时硬清仅认 BearVerifiedDual。
const (
	// BearVerifiedDual 双源（同花顺+东财板块成分/事件数据）验证命中——具备即时硬清资格。
	BearVerifiedDual = "dual"
	// BearVerifiedSingle 仅单数据源命中——只预警。
	BearVerifiedSingle = "single"
	// BearUnverified LLM 提案/新题材首日未验证——只预警。
	BearUnverified = "unverified"
)

// SignalFresh 带新鲜度的方向信号（边界⑥：延持信号必须新鲜）。
// Active=信号是否产出，At=信号产出时刻（打分轮次时间戳；零值按不过期处理仅限测试/未注入）。
type SignalFresh struct {
	Active bool
	At     time.Time
}

// BearConfirm 利空确认输入（语义①+§八护栏）。Hit=本轮是否命中利空归因/D1 负面拦截。
type BearConfirm struct {
	Hit      bool
	Verified string // BearVerifiedDual / BearVerifiedSingle / BearUnverified
}

// SellInput 单持仓一轮探针的裁决输入。价格与信号由引擎每轮装配（行情快照+打分轮次时间戳）。
// Evidence 为探测器证据摘要（战法退出/卖点评估/情绪退潮命中说明），只进留痕理由、不参与处置判定。
type SellInput struct {
	Code       string
	EntryPrice float64 // 持仓成本
	HighPrice  float64 // 持仓期最高价（持久化侧传入，≤0 回退成本价）
	CurPrice   float64 // 现价（无效=停牌/缺失，本轮不下结论）
	Bull       SignalFresh
	Bear       BearConfirm
	Evidence   []string
}

// SellDisposal 处置单（仅 pass 裁定产生）：Action=close 全平 / trim 半平。
type SellDisposal struct {
	Code   string
	Action string
	Reason string
	Line   SellLine
	PnlPct float64
	At     time.Time
	Shadow bool // §P1 影子模式产出（只留痕不对接执行）
}

// 处置动作常量。
const (
	SellActionClose = "close"
	SellActionTrim  = "trim"
)

// StageSell 卖出裁决留痕层名（Decision.Stage）。
const StageSell = "sell_discipline"

// sellState 单持仓跨轮状态（原 trading.PositionState 的继承者，新增延长持有计数）。
type sellState struct {
	Line        SellLine
	FirstTouch  time.Time
	SettleStart time.Time // 当前结算点（固定栅格；延持只顺延一格，不滚动重置——边界⑤）
	WindowMin   int
	Settled     bool
	Confirmed   bool
	// ConfirmedAction 确认时固化的处置动作（close/trim）。重放必须与首结论逐字节一致——
	// 旧实现按当前价重算 trim/close，价格更深会把半平翻成全平，执行层幂等语义被破坏。
	ConfirmedAction string
	HighPrice       float64
	Extends         int // 已延持次数（留痕用，不设上限：信号持续=按设计拿着，深破线兜底）
}

// sellKey 卖出状态键：(通道, 账号, 代码)——与买入探针同构，live/paper 天然隔离。
type sellKey struct {
	ch      Channel
	account string
	code    string
}

// sellParams 裁决参数（默认值兜底后的一次性快照）。
type sellParams struct {
	sl, tp, pb, dm float64
	exitMin        int
	trailMin       int
	bullMaxAge     time.Duration
}

// sellParamsOf 从纪律配置解析卖出裁决参数（缺省与 DefaultDisciplineConfig 同口径）。
func sellParamsOf(cfg config.DisciplineConfig) sellParams {
	p := sellParams{sl: 6, tp: 15, pb: 6, dm: 2, exitMin: 15, trailMin: 45, bullMaxAge: 5 * time.Minute}
	if cfg.StopLossPct > 0 {
		p.sl = cfg.StopLossPct
	}
	if cfg.TakeProfitPct > 0 {
		p.tp = cfg.TakeProfitPct
	}
	if cfg.MaxPullbackPct > 0 {
		p.pb = cfg.MaxPullbackPct
	}
	if cfg.DeepStopMult > 0 {
		p.dm = cfg.DeepStopMult
	}
	if cfg.ExitConfirmMin > 0 {
		p.exitMin = cfg.ExitConfirmMin
	}
	if cfg.TrailConfirmMin > 0 {
		p.trailMin = cfg.TrailConfirmMin
	}
	if cfg.SellSignalMaxAgeSec > 0 {
		p.bullMaxAge = time.Duration(cfg.SellSignalMaxAgeSec) * time.Second
	}
	return p
}

// bullLive 做多信号是否"新鲜且活跃"（边界⑥）。At 零值=调用方未注入时间戳（测试/旧装配），
// 按不过期放行，与缺省宽松语义一致；生产接线必须注入打分轮次时刻。
func (in SellInput) bullLive(now time.Time, p sellParams) bool {
	if !in.Bull.Active {
		return false
	}
	if in.Bull.At.IsZero() {
		return true
	}
	return now.Sub(in.Bull.At) <= p.bullMaxAge
}

// bearHard 利空是否具备"触线即时硬清"资格（语义①+§八护栏4）：命中且双源验证。
func (in SellInput) bearHard() bool {
	return in.Bear.Hit && in.Bear.Verified == BearVerifiedDual
}

// SellVerdict 单持仓一轮裁决的完整产出（§SELLPOINT-UNIFY P2 展示投影用）：
// Disposal=pass 处置单（nil=本轮无处置）、HoldReason=观察/延持说明、Line/Settled/Extends=状态快照。
type SellVerdict struct {
	Disposal   *SellDisposal
	HoldReason string
	Line       SellLine
	Settled    bool
	Extends    int
	Valid      bool // false=价格无效等不可裁决轮次（状态未动，展示层不得出卡）
}

// JudgeSellView 与 JudgeSell 同一次裁决，但回传完整三态视图（P2 切闸后 engine 投影/执行都吃它）。
// （JudgeSellView performs the same adjudication as JudgeSell and returns the full verdict view.）
func (c *Controller) JudgeSellView(ch Channel, account string, in SellInput, pol Policy, now time.Time) SellVerdict {
	p := sellParamsOf(pol.Discipline)
	key := sellKey{ch, account, in.Code}

	c.mu.Lock()
	if c.sellStates == nil {
		c.sellStates = map[sellKey]*sellState{}
	}
	prev, existed := c.sellStates[key]
	// §H2（2026-09-22 修复批）：sellProbe 对传入状态**原地修改**并返回同一指针，
	// 旧实现直接拿 prev 做跃迁比较 → `st.Line != prev.Line`/Settled/SettleStart 三判据
	// 恒假，除首见外的所有跃迁（进窗锁定、延持顺延、深破升级）都不入裁定环，
	// 留痕面只剩处置、观察面全盲。比较前先值拷贝一份旧快照。
	// English: §H2 — sellProbe mutates the given state in place and returns the same pointer;
	// snapshot prev by value before the call, otherwise the transition-detection compares a
	// struct with itself and every hold-stage record silently vanishes.
	var prevSnap sellState
	if prev != nil {
		prevSnap = *prev
	}
	st, disposal, holdReason := sellProbe(prev, in, now, p)
	if st == nil {
		// 价格无效等不可裁决轮次：状态原样保留（含首见时的占位不落）。
		c.mu.Unlock()
		return SellVerdict{}
	}
	c.sellStates[key] = st
	// 留痕只在状态跃迁时打点（5s 探针节奏下"窗内等待"重复态不入环防刷屏）；处置必留痕。
	record := Decision{Channel: ch, Account: account, Code: in.Code, Strategy: "卖出纪律", SKey: "sell_discipline", At: now, Stage: StageSell}
	switch {
	case disposal != nil:
		record.Verdict = VerdictPass
		record.Reason = disposal.Reason
	case !existed || st.Line != prevSnap.Line || !st.SettleStart.Equal(prevSnap.SettleStart) || st.Settled != prevSnap.Settled:
		if holdReason != "" {
			record.Verdict = VerdictHold
			record.Reason = holdReason
		}
	}
	c.mu.Unlock()
	if record.Verdict != "" {
		c.record(record)
	}
	return SellVerdict{Disposal: disposal, HoldReason: holdReason, Line: st.Line, Settled: st.Settled, Extends: st.Extends, Valid: true}
}

// JudgeSell 卖出裁决唯一入口（单持仓一轮探针）。返回处置单或 nil；三态裁定自动留痕。
// 状态机跨轮保存在 Controller（按 通道+账号+代码 隔离），平仓后由 PruneSellStates 清理。
// English: the single sell adjudication entry — probe one position once; returns a disposal (pass)
// or nil (hold/no-line). State survives rounds inside the Controller, keyed per channel+account+code.
func (c *Controller) JudgeSell(ch Channel, account string, in SellInput, pol Policy, now time.Time) *SellDisposal {
	return c.JudgeSellView(ch, account, in, pol, now).Disposal
}

// SellHighAnchor 回读某持仓裁决状态机当前的**移动止盈锚点**（持仓期最高价，含内核每轮自抬的部分）。
// 无状态/无锚点回 0（调用方按"无锚点"处理，不得据此把账本高点写成 0）。
// 为什么需要这个只读口（§N-7 2026-09-22 傍晚批复验）：内核 sellState.HighPrice 是"播种 + 每轮自抬"
// 的运行期真值，live 账本列 real_positions.highest_price 此前只被建仓价/成交价写过，期间最高价
// 从未入账 → 重启即回落到建仓价，涨过 15% 再回落的仓位移动止盈永不触发。回写账本需要把这份
// 运行期真值取出来，而 sellState 是包内私有类型，只能由本包提供受控只读访问器（不让 engine
// 直接改状态，避免绕过裁决的单调语义）。
// English: §N-7 — read-only accessor for the kernel's live trailing-stop anchor (the high it has
// raised during this process), so the engine can persist it back to the ledger.
func (c *Controller) SellHighAnchor(ch Channel, account, code string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.sellStates[sellKey{ch, account, code}]; st != nil {
		return st.HighPrice
	}
	return 0
}

// PruneSellStates 清理该通道/账号下本轮已不在持仓的裁决状态（重新入场从零开始）。
// English: drop sell-judge states for codes no longer held (re-entry starts fresh).
func (c *Controller) PruneSellStates(ch Channel, account string, held map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.sellStates {
		if k.ch == ch && k.account == account && !held[k.code] {
			delete(c.sellStates, k)
		}
	}
}

// ResetSell 清空全部卖出裁决状态（引擎重启/账号切换用；nil 安全）。
func (c *Controller) ResetSell() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sellStates = nil
}

// sellProbe 纯函数裁决内核：返回更新后的状态 + 处置单（pass）或延持/观察理由（hold）。
// st 为 nil 时按输入价格新建状态。绝不在本函数内取锁/读全局——100% 可单测。
func sellProbe(st *sellState, in SellInput, now time.Time, p sellParams) (*sellState, *SellDisposal, string) {
	if in.EntryPrice <= 0 || in.CurPrice <= 0 {
		return st, nil, "" // 无有效成本/现价不下结论（停牌/行情缺失，状态保留）
	}
	if st == nil {
		high := in.HighPrice
		if high <= 0 {
			high = in.EntryPrice
		}
		st = &sellState{HighPrice: high}
	}
	if in.CurPrice > st.HighPrice {
		st.HighPrice = in.CurPrice
	}
	pnl := (in.CurPrice - in.EntryPrice) / in.EntryPrice * 100
	bullLive := in.bullLive(now, p)
	evi := evidenceSuffix(in.Evidence)

	// ① 已确认处置：每轮重放同一处置单（执行层日级幂等兜底），理由含最新证据。
	if st.Settled && st.Confirmed {
		return st, confirmedReplay(st, in.Code, pnl, now, p, evi), ""
	}

	// ② 本轮命中判定线（含延持期间的利空即时硬清与深破升级——状态机在延持期持续运转，
	//    修复旧实现"一次延持永久失明"缺陷 b）。
	line := SellLineNone
	switch {
	case pnl <= -p.sl*p.dm:
		line = SellLineDeepBreach
	case pnl <= -p.sl:
		if in.bearHard() {
			// 语义①：触止损线 + 双源验证利空 → 当轮直接硬清，不等窗（首触/窗内/延持期均即时）。
			st.Line = firstOf(st.Line, SellLineStopLoss)
			st.Settled, st.Confirmed = true, true
			st.ConfirmedAction = SellActionClose
			return st, &SellDisposal{Code: in.Code, Action: SellActionClose, Line: st.Line, PnlPct: pnl, At: now,
				Reason: "触止损线且利空双源验证，即时硬清" + evi}, ""
		}
		line = SellLineStopLoss
	case pnl >= p.tp:
		if bullLive {
			return st, nil, "止盈线有活跃做多信号，延持" // 止盈延持不开窗（与旧口径一致）
		}
		line = SellLineTakeProfit
		if st.HighPrice > 0 && (st.HighPrice-in.CurPrice)/st.HighPrice*100 >= p.pb {
			line = SellLineTrail
		}
	}
	if line == SellLineNone && st.Line == SellLineNone && st.HighPrice > in.EntryPrice &&
		(st.HighPrice-in.CurPrice)/st.HighPrice*100 >= p.pb {
		line = SellLineTrail // 已有利润、回撤超阈值 → 移动止盈（洗盘过滤窗更长）
	}

	// ③ 首触锁定：判定线 + 固定结算点（对齐 09:30 栅格一次锁死，边界⑤）。
	if st.Line == SellLineNone {
		if line == SellLineNone {
			return st, nil, ""
		}
		st.Line = line
		st.FirstTouch = now
		st.WindowMin = p.exitMin
		if line == SellLineTrail {
			st.WindowMin = p.trailMin
		}
		st.SettleStart = alignSettleSell(now, st.WindowMin)
		return st, nil, fmt.Sprintf("命中%s(盈亏%.2f%%)，进入%d分钟观察窗", st.Line, pnl, st.WindowMin) + evi
	}

	// 线升级（任意线型→深破）：§C2（2026-09-22 修复批）旧的升级判定只认 StopLoss→DeepBreach，
	// 线已锁定为 TakeProfit/Trail 时砸穿 −12% 不进深破分支、落到下方 bullLive 延持——
	// 边界④（深破无条件全清）在两类盈利线下被架空（FIX#12 事故形态）。现为本轮判定命中
	// 深破即无条件覆写线型；Settled/Confirmed 一并清零是对存量持久化状态的防御（正常路径
	// 到不了已确认态，① 已提前重放返回）。窗时序不动，结算口径按新线（⑤ 无条件全清）。
	// English: §C2 — a fresh deep-breach verdict upgrades st.Line from ANY locked line
	// (StopLoss/TakeProfit/Trail) to DeepBreach unconditionally; boundary-④ full clear must
	// not be bypassed just because a profit line was locked first.
	if line == SellLineDeepBreach {
		st.Line = SellLineDeepBreach
		st.Settled, st.Confirmed = false, false
	}

	// ④ 未到结算点：窗内等待。
	if now.Before(st.SettleStart) {
		return st, nil, fmt.Sprintf("观察窗内等待信号（结算点 %s）", st.SettleStart.Format("15:04:05"))
	}

	// ⑤ 窗结算。深破无条件全清（边界④，不看任何信号）。
	if st.Line == SellLineDeepBreach {
		st.Settled, st.Confirmed = true, true
		st.ConfirmedAction = SellActionClose
		return st, &SellDisposal{Code: in.Code, Action: SellActionClose, Line: st.Line, PnlPct: pnl, At: now,
			Reason: "深破窗结算，无条件全平离场" + evi}, ""
	}
	// 有新鲜做多信号 → 延持一个窗长（结算点沿固定栅格顺延一格；反复延持=按设计拿着）。
	if bullLive {
		st.Extends++
		st.SettleStart = st.SettleStart.Add(time.Duration(st.WindowMin) * time.Minute)
		return st, nil, fmt.Sprintf("窗结算仍有做多信号，延持第%d次（新结算点 %s）", st.Extends, st.SettleStart.Format("15:04:05")) + evi
	}
	// 无信号 → 处置：止盈/移动止盈全平；止损首触未深破半平（与旧口径一致）。
	st.Settled, st.Confirmed = true, true
	d := &SellDisposal{Code: in.Code, Line: st.Line, PnlPct: pnl, At: now, Action: SellActionClose}
	switch st.Line {
	case SellLineTakeProfit, SellLineTrail:
		d.Reason = "止盈窗结算无信号，止盈离场" + evi
	case SellLineStopLoss:
		d.Action = SellActionTrim
		d.Reason = "止损窗结算无信号，首触止损未深破减半仓" + evi
	default:
		d.Reason = "止损窗结算无信号，止损离场" + evi
	}
	st.ConfirmedAction = d.Action // 固化动作，后续重放逐字一致
	return st, d, ""
}

// confirmedReplay 重放已确认处置：动作取确认时固化的 ConfirmedAction（价格再深也不改写
// trim→close），理由仅按当前盈亏快照刷新文案。
func confirmedReplay(st *sellState, code string, pnl float64, now time.Time, p sellParams, evi string) *SellDisposal {
	action := st.ConfirmedAction
	if action == "" { // 兜底：存量状态无固化动作时按线型回退
		if st.Line == SellLineStopLoss && pnl > -2*p.sl {
			action = SellActionTrim
		} else {
			action = SellActionClose
		}
	}
	d := &SellDisposal{Code: code, Line: st.Line, PnlPct: pnl, At: now, Action: action}
	switch st.Line {
	case SellLineTakeProfit, SellLineTrail:
		d.Reason = "止盈离场（窗结算无信号）" + evi
	case SellLineDeepBreach:
		d.Reason = "深破止损离场" + evi
	default:
		if action == SellActionTrim {
			d.Reason = "首触止损未深破，减半仓" + evi
		} else {
			d.Reason = "止损离场" + evi
		}
	}
	return d
}

// alignSettleSell 触发时刻对齐到 09:30 锚定的固定窗栅格，取下一格为结算点（锁死不滚动）。
// （与旧 trading.alignSettle 同算法；迁移完成后旧实现随五路拼装一并退役。）
func alignSettleSell(t time.Time, winMin int) time.Time {
	if winMin <= 0 {
		winMin = 15
	}
	base := time.Date(t.Year(), t.Month(), t.Day(), 9, 30, 0, 0, t.Location())
	off := int(t.Sub(base).Minutes())
	if off < 0 {
		off = 0
	}
	next := (off/winMin + 1) * winMin
	return base.Add(time.Duration(next) * time.Minute)
}

// firstOf 已有线取旧线（锁定语义），无线取新触发。
func firstOf(cur, detected SellLine) SellLine {
	if cur != SellLineNone {
		return cur
	}
	return detected
}

// evidenceSuffix 证据摘要拼进理由（只影响展示/留痕，不参与处置判定）。
func evidenceSuffix(evi []string) string {
	s := ""
	for _, e := range evi {
		if e != "" {
			s += "；" + e
		}
	}
	return s
}
