// trade_actions_20261105.go — §TRADE-CARD（2026-11-05）撤单腿的"这一轮到底做了什么"动作快照。
//
// 背景是 owner 的一条产品口径：交易提示卡完全拿量化交易的数据来做，五个动作＝撤/留/加/减/走，
// "量化自动交易准备怎么做，你就怎么提醒"。于是这张卡的本体只能是一条判决：
// **屏上每一句"要撤/已撤/撤不掉"，都只可能来自真正做撤单的那条腿**（Controller.SweepOrders），
// 绝不在展示层再判一次"这单超没超时、信号消没消失"。同一个数两处各算一遍是本仓最贵的老毛病
// （253 枚规则旋钮没有主人，就是它的钱路径版本）。
//
// 这不是第二本账：委托行的 cancel_source / cancel_reason / cancel_requested_at 三列
// （§CANCEL-STATE 2026-10-28）才是落盘凭据，收盘对账与次日复盘一律以那三列为准。
// 本快照是**进程内即时窗**，重启即空；重启后卡上表现为"今天没有撤单动作"，而当日委托可能仍是
// 撤单中——所以它只配回答"这一轮系统刚做了什么"，不配当对账依据，也不进任何资金判定。
//
// 三个形态与一个"没有的形态"（缺一个都会让读数撒谎）：
//   - requested：撤单请求已发出（≠ 已撤，交易所仍可能拒绝，终态由回报/快照裁决）；
//   - window  ：判定要撤，但落在禁撤窗口 ⇒ 一个请求都没发（这单此刻还挂在柜台上）；
//   - refused ：请求发了但被拒（已成交/委托号未回报），下轮再评估；
//   - **没有"应该撤"这一档**：只有真的进入过撤单判定分支的委托才进来，
//     否则卡上会出现"提示要撤、系统根本没看它"的第三种状态，那就回到两本账了。
//
// English: an in-process, day-keyed snapshot of the cancel actions SweepOrders really took.
// The advice card reads only this for the 撤 verb and never re-decides staleness; the durable
// record stays in orders.cancel_* columns. Bounded, resets on day change, empty after a restart.
package trading

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/store"
)

// 撤单动作的三种真实形态（见文件头）。字面量会被交易提示卡与提醒文案直接消费，
// 也进门禁锚点，因此只许在这里定义一次。
const (
	SweepActionRequested = "requested" // 撤单请求已发出（等交易所确认）
	SweepActionWindow    = "window"    // 判定要撤，但禁撤窗口内未发任何请求
	SweepActionRefused   = "refused"   // 撤单请求被拒（已成交/委托号未回报等）
)

// 撤单理由（与 orders.cancel_source 同一批字面量，两处必须同源）：
//   - sweep       例行超时撤单（cancel_stale_sec）
//   - signal_gone 信号消失即撤（§SIGNAL-GONE，宽限秒数缺省关）
//   - close_sweep 收盘清单（close_sweep_at，每日一次）
//
// 为什么把"收盘清单"从 sweep 里拆出来：这两个触发条件在次日的含义完全不同——
// 一个是"这单挂太久"，一个是"每天收盘都会发生"。折成同一个字面量，运维就分不出
// 到底是超时阈值设错了，还是本来就到点了。
const (
	SweepSourceSweep      = "sweep"
	SweepSourceSignalGone = "signal_gone"
	SweepSourceClose      = "close_sweep"
	// SweepSourceVWAPReprice §BUY-REPRICE（2026-11-05，owner 令「根据 VWAP 动态更新买入价格」）：
	// 挂单价跟不上市场参考价，撤掉它是为了**按新价重挂**，不是不想要这笔仓了。
	// 所以它必须与"到期撤""消失撤"在账上分得开：改价被限次挡下与"根本没必要撤"在卡上
	// 若是同一句"没撤单"，就把真正的原因藏掉了。
	SweepSourceVWAPReprice = "vwap_reprice"
)

// sweepActionLimit 当日快照的条数帽：撤单一天最多几十笔，200 足够容纳收盘清场那种成片撤单，
// 又能挡住任何"循环里每轮都记一条"的实现把内存吃穿。超限时按动作时刻丢最旧的。
const sweepActionLimit = 200

// SweepAction 一笔委托的一次撤单动作（卡上"撤"那一行的全部事实来源）。
type SweepAction struct {
	OrderID  string    `json:"order_id"`  // 委托号（提醒的去重键，见 owner 裁决「按委托号一条」）
	SignalID string    `json:"signal_id"` // 幂等键（buy:/sell:/t0: 原样，卡上据此标"做T"）
	TsCode   string    `json:"ts_code"`   // 带后缀代码（与持仓卡同键）
	Name     string    `json:"name,omitempty"`
	Side     string    `json:"side"`   // 买入/卖出
	Qty      int       `json:"qty"`    // 委托量（不是成交量）
	Kind     string    `json:"kind"`   // SweepActionRequested / Window / Refused
	Source   string    `json:"source"` // SweepSourceSweep / SignalGone / Close
	Reason   string    `json:"reason"` // 给成人看的理由，与委托行 cancel_reason 同句
	At       time.Time `json:"at"`     // 动作时刻（本轮 SweepOrders 的 now）
	Day      string    `json:"day"`    // YYYYMMDD（北京时间交易日），跨日自动失效
}

// recordSweepAction 记下一次撤单动作（同 order_id 覆盖为最新形态）。
//
// 为什么同键覆盖而不是追加：一笔委托在卡上只该有一行"最新事实"——
// 先 window 后 requested 是正常推进（窗口一过就发出去），追加会让同一委托号出现两行，
// 而前端建议列是按 ts_code 归一的映射，两行会互相遮蔽成"随机显示其中一条"。
// 时刻（撤单请求发出的那一秒）以最早那次为准不成立—— requested 才是事实，所以照覆盖语义。
func (c *Controller) recordSweepAction(a SweepAction) {
	if a.OrderID == "" {
		return // 没有委托号的动作在卡上无法定位到任何一行，也不配进快照
	}
	now := a.At
	if now.IsZero() {
		now = time.Now()
		a.At = now
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// 日切：先判日再写，跨日不复活（与 orders/t_rounds 的 day-keyed 口径一致）。
	if c.lastSweepDay != a.Day {
		c.lastSweepDay = a.Day
		c.sweepActionsByID = nil
	}
	if c.sweepActionsByID == nil {
		c.sweepActionsByID = make(map[string]SweepAction)
	}
	c.sweepActionsByID[a.OrderID] = a
	if len(c.sweepActionsByID) <= sweepActionLimit {
		return
	}
	// 超限：按时刻升序找到最旧的那些删掉（只删到帽内，不整本清空——整本清空会让
	// 刚记下的这一笔也一起没了，读数反而变成"什么都没发生"）。
	acts := make([]SweepAction, 0, len(c.sweepActionsByID))
	for _, v := range c.sweepActionsByID {
		acts = append(acts, v)
	}
	sort.Slice(acts, func(i, j int) bool { return acts[i].At.Before(acts[j].At) })
	for _, v := range acts[:len(acts)-sweepActionLimit] {
		delete(c.sweepActionsByID, v.OrderID)
	}
}

// SweepActions 返回当日撤单动作快照（按动作时刻倒序，最新在前）。
// 空切片是合法空态（今天确实没撤过单，或进程刚重启），调用方必须把它读成"没有读数"，
// 而不是"没有该撤的单"——前者要去看 orders，后者要去看打分链。
func (c *Controller) SweepActions() []SweepAction {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.sweepActionsByID) == 0 {
		return nil
	}
	out := make([]SweepAction, 0, len(c.sweepActionsByID))
	for _, v := range c.sweepActionsByID {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// AdviceSourceCancel 撤单行的来源标记。为什么必须给一个**新**值而不是留空：
// 实盘自动卖出腿（engine.autoExecuteRealSellsRound）是按 Source 做准入的，
// 撤单行如果被它认成"一条普通的卖出建议"，就会有人在一轮展示里被再卖一次。
// 这个值不在任何执行词表里 ⇒ 结构上不可能被执行，只可能被显示。
const AdviceSourceCancel = "sweep_action"

// cancelAdvices 撤单动作 → 卡上的 撤 行：一条委托一行，**一个判断都不做**。
//
// 三条刻意的形态：
//   - 一行=一笔委托（不是"一码一行"）：owner 裁的是撤单提醒按委托号一条，卡与提醒同一个粒度，
//     否则屏上折成一条、柜台却是两笔；
//   - Qty 留空、委托量写在理由里：PositionAdvice.Qty 的字段语义是"当前持仓股数"，
//     把委托量塞进去会让持仓列显示一个根本没持有的数量——那是把两个事实折成一个字段；
//   - 禁撤窗口/被拒的紧迫度高于"已发出"：那两种形态下**这单还挂在柜台上**，
//     而已发出的只需要等交易所确认。Level 因此按"还挂着没有"分高/中。
//
// English: project the cancel loop's actions onto 撤单 rows — one row per ticket, no re-judgement.
// Order qty lives in the reason text (PositionAdvice.Qty means held qty), and a ticket still resting
// at the broker (window/refused) ranks higher than one already sent.
func cancelAdvices(actions []SweepAction) []PositionAdvice {
	if len(actions) == 0 {
		return nil
	}
	out := make([]PositionAdvice, 0, len(actions))
	for _, a := range actions {
		if strings.TrimSpace(a.OrderID) == "" {
			continue // 没有委托号的动作定位不到任何一行，宁可少一行也不造一条指不到东西的提示
		}
		// 紧迫度按"这单还挂在柜台上没有"分档：窗口内未发/被拒＝高（还需要人盯），
		// 已发出＝中（等交易所确认，终态由回报裁决）。
		level := "中"
		switch a.Kind {
		case SweepActionWindow, SweepActionRefused:
			level = "高"
		}
		// 名称缺失时退到代码：卡上这一行必须有可辨识的主语，留空会让人以为是一行系统噪声。
		name := a.Name
		if name == "" {
			name = a.TsCode
		}
		// 时刻缺失才补 now（补的是"没读数"，不是"改读数"）：卡上显示的时刻必须是动作发生的那一刻。
		at := a.At
		if at.IsZero() {
			at = time.Now()
		}
		out = append(out, PositionAdvice{
			Code:         pureCode(a.TsCode),
			TsCode:       a.TsCode,
			Name:         name,
			Action:       adviceActionCancel,
			Level:        level,
			Reason:       cancelAdviceReason(a),
			Source:       AdviceSourceCancel,
			OrderID:      a.OrderID,
			CancelKind:   a.Kind,
			CancelSource: a.Source,
			GeneratedAt:  at,
		})
	}
	return out
}

// CancelOutcome 发起撤单后的四种真实结果。**没有"已撤"这一档**：撤单请求被受理不等于交易所已撤
// （§CANCEL-STATE 的整个来由），终态由回报/快照裁决，这里只报"我们这一手做成了什么"。
type CancelOutcome int

const (
	CancelRequested        CancelOutcome = iota + 1 // 请求已发出，本地行已置撤单中
	CancelWindowBlocked                             // 落在禁撤窗口：一个请求都没发，这单还挂在柜台上
	CancelRefused                                   // 撤单调用被拒（已成交/委托号未回报等）
	CancelStateNotAdvanced                          // 请求发出但本地状态没推进成撤单中（不能声称撤了）
)

// RequestCancel 是"对一笔在途委托发起撤单"的**唯一出口**（2026-11-05）。
//
// 为什么必须是唯一出口：窗口闸、撤单调用、orders.cancel_* 留痕、状态推进、动作快照这五件事
// 少做一件，卡上/提醒里就有一份说谎。撤单的触发条件不止一个（超时、信号消失、收盘清单、
// VWAP 改价），如果每个触发点各写一遍撤单，第一个漏写留痕的那条就是"撤了但没人知道"。
// 所以这里**只收来源和话术**，判定归各条腿；动作快照由本函数统一记，形态与来源不会分叉。
//
// day 由调用方给（本轮的 YYYYMMDD）：不在这里读墙钟，注入时钟的用例才能固定日键。
func (c *Controller) RequestCancel(o store.RealOrder, source, reason string, now time.Time, day string) CancelOutcome {
	act := SweepAction{OrderID: o.OrderID, SignalID: o.SignalID, TsCode: o.Code,
		Side: o.Side, Qty: o.Qty, Source: source, Reason: reason, At: now, Day: day}
	if blocked, _ := c.windowsRef().CancelBlocked(now); blocked {
		// 卡上必须写"要撤但没撤"：这单此刻还挂在柜台上，与"已经撤了"是两种处置。
		act.Kind = SweepActionWindow
		act.Reason = "判定要撤（" + reason + "），但落在禁撤窗口内未发任何请求"
		c.recordSweepAction(act)
		return CancelWindowBlocked
	}
	if err := c.execRef().Cancel(o.OrderID); err != nil {
		// 典型失败：交易所委托号尚未回报（网关暂不可撤）/ 已成交（撤单被拒）——
		// 不强试，交由回报线程推进真实状态，下轮再评估。
		act.Kind = SweepActionRefused
		act.Reason = "撤单请求被拒（" + err.Error() + "），下轮再评估"
		c.recordSweepAction(act)
		return CancelRefused
	}
	// §FIX-20261028-F14：留痕先写（终态行也要留），再推进状态。
	if _, err := c.store.SetCancelTrace(c.userID, o.OrderID, source, reason); err != nil {
		log.Printf("[trading] cancel trace %s: %v", o.OrderID, err)
	}
	if ok, err := c.store.MarkRealOrderCancelRequested(c.userID, o.OrderID, source, reason); err != nil || !ok {
		// 状态没推进成功＝这一笔其实还挂在柜台上，卡上不许记成 requested（那会让"撤了"
		// 成为假读数）；按被拒记下，下轮重评。
		act.Kind = SweepActionRefused
		act.Reason = "撤单请求已发出但本地状态未推进为撤单中，下轮再评估"
		c.recordSweepAction(act)
		return CancelStateNotAdvanced
	}
	act.Kind = SweepActionRequested
	c.recordSweepAction(act)
	return CancelRequested
}

// cancelAdviceReason 撤单行的正文：动作形态 + 理由 + 委托量。
// 委托量在这里以文字出现（不是 Qty 字段），理由见上面第 2 条形态说明。
func cancelAdviceReason(a SweepAction) string {
	what := "撤单请求已发出"
	switch a.Kind {
	case SweepActionWindow:
		what = "判定要撤，但禁撤窗口内未发请求（这单仍挂在柜台上）"
	case SweepActionRefused:
		what = "撤单请求被拒（这单仍挂在柜台上）"
	}
	side := a.Side
	if side == "" {
		side = "委托"
	}
	reason := a.Reason
	if reason == "" {
		reason = "未给出理由"
	}
	return fmt.Sprintf("%s：%s｜来源=%s %s%d股（委托 %s）",
		what, reason, a.Source, side, a.Qty, a.OrderID)
}
