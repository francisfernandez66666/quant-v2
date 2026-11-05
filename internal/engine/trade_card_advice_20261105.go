// trade_card_advice_20261105.go — §TRADE-CARD（2026-11-05）交易提示卡的两条投影腿：做 T 的行 + 交易级提醒。
//
// owner 的口径是这张卡完全拿"量化自动交易"的数据来做，五个动作＝撤/留/加/减/走，并且
// **"做 T 的也要提醒，特殊标注这是做 T"**；不到交易级别的东西不提醒。
//
// 于是本文件只干两件事，而且两件都只是**投影**，一条判据都不新建：
//  1. tRoundAdvices：把当日日内回转账本（<dataDir>/accounts/<uid>/t_rounds_live.json，
//     §DYN-T 那本账）的每一轮回转投成建议行，动词按首腿方向落 加/减/留，
//     TradeKind=intraday_t 让前端能标出「做T」；
//  2. syncTradeLevelAlerts：只有**交易级**动作（撤单/加仓/减仓/止盈/止损）才往消息中心与
//     P1 强推走；持有/格局/观察预警（unified-watch）一律不推——那些行照样在卡上看得见。
//     提醒里的"系统做没做"读的是执行点留下的真实留痕（trade_action_ledger.go），
//     不再读开关——旧实现拿 `Enabled && mode=auto && auto_sell` 当结果，风控拒掉的单也被报成
//     【已触发自动卖出】，是"降级报成功"那一族的最后一个样本。
//
// 三态文案（缺一个就开始撒谎）：已受理（Placed=true，≠成交）/ 本轮没发出（带拒因）/
// 本进程没见证过（重启前发生的）——第三种**不许**写成"没下单"，只许写"未见证，请核对当日委托"。
//
// English: two projections for the trade-level advice card — intraday round-trip rows (marked 做T)
// and the trade-level alert filter. Neither decides anything: the verbs come from the T ledger and
// the cancel loop, and the "did the system act?" suffix comes from the executor's own record.
package engine

import (
	"fmt"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/trading"
)

// tRoundAdvices 当日日内回转的每一轮 → 一行建议（TradeKind=intraday_t）。
//
// 为什么必须单列一行，而不是并到那只票原有的加仓/减仓行上：同一笔"买 300 股"既可能是战略加仓、
// 也可能是正 T 的机动仓，两者在屏上长得一模一样，但处置完全不同——T 腿当日必须配平收摊，
// 战略腿不动。不标注就是让人拿战略仓的耐心去做一条当天要平的腿。
//
// 动词映射（只做映射，不做判断；回转的判据全在 intraday_t_live.go 那条编排腿里）：
//   - open ＋ 正 T（先买）    → 加仓：机动仓已买入，等配对卖出
//   - open ＋ 反 T（先卖）    → 减仓：底仓已卖出，等回补或收摊
//   - done / unwound / converted → 留：这一轮已经收口（配平 / 主动平掉 / 转成持仓）
//     ——留 不是交易级动作，所以它**不会**进提醒（见 syncTradeLevelAlerts 的词表）。
//
// OrderID 用回转的幂等键（t0:<code>:<dir>:<日>:<序>）：提醒按它去重，
// 一天同一只票做两次 T 就该有两条提醒，折成一条会把第二次吞掉。
func (e *Engine) tRoundAdvices(userID string, now time.Time) []trading.PositionAdvice {
	rg := e.tLiveRegSnapshot()
	if rg == nil {
		return nil
	}
	st := rg.get(userID)
	if st == nil {
		return nil
	}
	rounds := st.allRounds()
	if len(rounds) == 0 {
		return nil
	}
	out := make([]trading.PositionAdvice, 0, len(rounds))
	for _, r := range rounds {
		if r == nil || strings.TrimSpace(r.Code) == "" {
			continue
		}
		action, level, statusCN := "留", "低", "已收口"
		switch r.Status {
		case "open":
			// 一腿在途：这一轮还没配平，是今天唯一还需要人盯的回转状态。
			level = "高"
			statusCN = "一腿在途"
			if r.Dir == tLDirSell {
				action = "减仓" // 反 T：底仓先卖出去，等回补
			} else {
				action = "加仓" // 正 T：机动仓先买进来，等配对卖
			}
		case "done", "unwound", "converted":
			// 终态：出卡是为了"今天做过哪些 T"看得见，但它不是一条待办，故 留＋不提醒。
		default:
			continue // idle/未知形态不出卡：认不出状态的行没有资格占用一个动词
		}
		var b strings.Builder
		b.WriteString("做T·")
		if r.Dir == tLDirSell {
			b.WriteString("反T（先卖底仓，计划回补）")
		} else {
			b.WriteString("正T（先买机动仓，计划配对卖）")
		}
		fmt.Fprintf(&b, " 状态=%s 计划%d股", statusCN, r.Qty)
		if r.BuyFilled > 0 {
			fmt.Fprintf(&b, " 已买%d股@%.2f", r.BuyFilled, r.BuyPrice)
		}
		if r.SellFilled > 0 {
			fmt.Fprintf(&b, " 已卖%d股@%.2f", r.SellFilled, r.SellPrice)
		}
		switch r.Status {
		case "done":
			// 配平轮才带 T 净收益（§8.9a）：没配平的轮算不出这条，也绝不许用 0 冒充"不赚不亏"。
			fmt.Fprintf(&b, " 已配平%d股 净额%+.2f元", r.QMatched, r.TPnlNet)
		case "unwound":
			fmt.Fprintf(&b, " 收摊原因=%s", orNoneT(r.UnwindReason))
		case "converted":
			fmt.Fprintf(&b, " 未配平已转持仓 原因=%s", orNoneT(r.UnwindReason))
		}
		if r.Unfilled && r.Status != "open" {
			fmt.Fprintf(&b, " 有未成交腿 原因=%s", orNoneT(r.UnfilledReason))
		}
		name := r.Name
		if name == "" {
			name = r.Code
		}
		out = append(out, trading.PositionAdvice{
			Code:        pureTsCode(r.Code),
			TsCode:      withSuffix(pureTsCode(r.Code)),
			Name:        name,
			Action:      action,
			Level:       level,
			Reason:      b.String(),
			Source:      "t_round",
			OrderID:     r.ID,
			TradeKind:   trading.AdviceTradeKindIntradayT,
			GeneratedAt: tRoundStamp(r, now),
		})
	}
	return out
}

// orNoneT 空原因写成「无」而不是留空：卡上"原因="后面一片空白会被读成"系统没给原因"，
// 而真实情况是"这一轮没有收摊原因（正常配平）"。
func orNoneT(s string) string {
	if strings.TrimSpace(s) == "" {
		return "无"
	}
	return s
}

// tRoundStamp 一行的时刻：开腿时刻优先（它是这一轮真正发生的时间），退回关闭时刻，
// 再退回本轮生成时刻。解析失败不猜、直接用 now——卡上的时刻只许是真时刻。
func tRoundStamp(r *TRoundLive, now time.Time) time.Time {
	for _, s := range []string{r.OpenedAt, r.ClosedAt} {
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return now
}

// tLiveRegSnapshot 锁内取注册表引用（不在锁内调用它，避免持 e.mu 跨进账本 IO）。
func (e *Engine) tLiveRegSnapshot() *tLiveRegistry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tLiveReg
}

// pushTradeLevelWithoutPositions 空仓时也要把交易级动作推出去（撤单/做 T 两条投影腿不需要持仓）。
//
// 为什么单独成一条而不是"把 pushRealAdvice 的早退挪到后面"：那条主链前半段还要装配行情/日K/打分，
// 空仓时这些入参一个都用不上，挪早退等于让空仓账号每 5s 多做一次全池装配；
// 而 撤/做T 两行的真源（SweepOrders 快照、日内回转账本）本来就不读持仓。
// 走同一条提醒函数 ⇒ 词表、去重键、留痕文案与有持仓时逐字相同，不存在"空仓版第二套口径"。
func (e *Engine) pushTradeLevelWithoutPositions(sendTo string, ctrl *trading.Controller, sse *server.SSEBroker) {
	if ctrl == nil || sendTo == "" {
		return
	}
	rows, _ := trading.Advise(trading.AdviceInput{CancelActions: ctrl.SweepActions()})
	rows = append(rows, e.tRoundAdvices(sendTo, time.Now())...)
	if len(rows) == 0 {
		return
	}
	e.syncTradeLevelAlerts(sendTo, rows)
	if sse == nil {
		return
	}
	sse.BroadcastTo(sendTo, map[string]interface{}{
		"type":    "real_advice",
		"advices": rows,
		"tripped": ctrl.Tripped(),
		// 空仓路径不做战法过滤（买入建议压根没生成），如实报 0 而不是省略键，
		// 免得前端把"没带这个键"与"滤了 0 条"混成同一个读数。
		"filtered_by_switch": 0,
		"time":               time.Now().Format("15:04:05"),
	})
}

// tradeLevelAlertActions 交易级动作词表——**提醒的唯一入口条件**。
// 卡上会出现的其它动作（持有/格局、观察预警卡、已收口的做T轮）一律不推。
//
// 为什么用白名单而不是黑名单：新增一个动作字面量的人必然没想过"它会不会轰炸用户"，
// 白名单让"忘记判断"的默认结果是**不推**（安静），黑名单让默认结果变成推。
var tradeLevelAlertActions = map[string]bool{
	"撤单": true,
	"加仓": true,
	"减仓": true,
	"止盈": true,
	"止损": true,
}

// tradeActionSuffix 把一行建议翻译成"系统到底做没做这一笔"。返回的三态见文件头。
//
// 为什么 撤单 不查执行留痕而用自己的 CancelKind：撤单动作的留痕本来就在 trading.SweepAction 里
// （requested/window/refused 三态就是它的全部真相），再绕道买单留痕会造出第二个真源。
func (e *Engine) tradeActionSuffix(a trading.PositionAdvice) string {
	switch a.Action {
	case "撤单":
		switch a.CancelKind {
		case trading.SweepActionWindow:
			return "【尚未发出：禁撤窗口内，这单此刻还挂在柜台上】"
		case trading.SweepActionRefused:
			return "【撤单被拒：" + a.Reason + "，下轮再评估】"
		case trading.SweepActionRequested:
			return "【撤单请求已发出，等待交易所确认】"
		default:
			// 认不出形态＝不知道，不许猜成"已撤"。这是钱路径上的措辞纪律。
			return "【撤单形态未标注，请核对当日委托】"
		}
	case "加仓":
		act, ok := e.tradeActionFor(tradeActionBuy, a.Code, "", a.TradeKind == trading.AdviceTradeKindIntradayT)
		return placeSuffix(act, ok, "买入", "卖出")
	case "减仓", "止盈", "止损":
		class := a.Action // 卖出类别与建议动词同源（sellRealPosition 的 class 就是它）
		act, ok := e.tradeActionFor(tradeActionSell, a.Code, class, a.TradeKind == trading.AdviceTradeKindIntradayT)
		return placeSuffix(act, ok, "买入", "卖出")
	default:
		return ""
	}
}

// placeSuffix 下单留痕 → 文案。side 只用于措辞（买/卖），两个方向共用同一套三态。
func placeSuffix(act tradeAction, seen bool, buyWord, sellWord string) string {
	word := sellWord
	if act.Kind == tradeActionBuy {
		word = buyWord
	}
	if !seen {
		// 未见证 ≠ 没做：进程重启前的动作在这里没有留痕，落盘真凭据是 orders 表。
		return "【本进程未见证该动作，请核对当日委托】"
	}
	if !act.Placed {
		// 一笔都没发出，且把是哪道闸/哪个拒因说出来——只说"未下单"等于把答案藏起来。
		return "【本轮未自动下单：" + act.Reason + "，请手动处理】"
	}
	if act.Qty > 0 && act.Price > 0 {
		return fmt.Sprintf("【已自动%s %d股@%.2f，等待成交确认】", word, act.Qty, act.Price)
	}
	return fmt.Sprintf("【已自动%s，等待成交确认】", word)
}

// syncTradeLevelAlerts 交易级建议 → 消息中心 + P1 强提醒（撤/加/减/走四类，其余静默）。
//
// 与旧 syncLiveAdviceAlerts 的三点不同，逐条都是 owner 本轮裁决：
//  1. **加仓也推**（旧实现只推卖出三类）：自动买入是真下真单的动作，属于交易级；
//  2. **撤单也推，且按委托号去重**（旧实现根本没有这条腿）：同一天同一只票撤两笔、
//     或撤了又重挂，按"码＋日"折一条就会把第二次撤单静默吞掉；
//  3. 那句"系统做没做"读执行留痕，不读开关（见 tradeActionSuffix）。
//
// 去重键三条规则，互不共用：撤单用 order_id、做T 用回转幂等键（OrderID=rnd.ID）、
// 其余用 码@动作@交易日。做 T 的战略加减**必须**分键，否则同一天先做 T 再收到战略止损，
// 后一条会因为键相同而永不推送——这是"少提醒一次"的方向，正是本仓反对的折叠。
func (e *Engine) syncTradeLevelAlerts(sendTo string, advices []trading.PositionAdvice) {
	if e.msgStore == nil || len(advices) == 0 {
		return
	}
	now := time.Now()
	day := data.TradingDayDate(now)
	idPrefix := "trade@"
	if sendTo != "" {
		idPrefix = "u" + sendTo + "|trade@"
	}
	items := make([]data.MessageItem, 0, len(advices))
	// 一轮内同键只留第一条（同一只票同一动作可能来自两条不同来源的建议行，
	// 消息中心靠 ID 去重，但同轮重复构造会白白让 P1 推两次）。
	seenID := make(map[string]bool, len(advices))
	for _, a := range advices {
		if !tradeLevelAlertActions[a.Action] {
			continue // 不是交易级 ⇒ 卡上看得见，屏外不轰炸（owner 令「不到交易级别不要提醒」）
		}
		isT := a.TradeKind == trading.AdviceTradeKindIntradayT
		key := fmt.Sprintf("%s%s@%s@%s", idPrefix, a.Code, a.Action, day)
		switch {
		case a.Action == "撤单":
			// 撤单按委托号：这是 owner 明确裁过的一条粒度（同日同码第二次撤单不许被折掉）。
			if strings.TrimSpace(a.OrderID) == "" {
				key = fmt.Sprintf("%scancel@%s@%s", idPrefix, a.Code, day) // 没有委托号的撤单行退到按日一条，并留痕
				opslog.Logf("quant", "§TRADE-CARD 撤单行缺 order_id，去重退到按日一条 code=%s", a.Code)
			} else {
				key = fmt.Sprintf("%scancel@%s", idPrefix, a.OrderID)
			}
		case isT:
			key = fmt.Sprintf("%st@%s@%s", idPrefix, a.OrderID, a.Action)
		}
		if seenID[key] {
			continue
		}
		seenID[key] = true

		name := a.Name
		if name == "" {
			name = a.Code
		}
		var b strings.Builder
		if isT {
			// 做 T 的标注放在最前面，让人第一眼就知道这不是战略加减（owner 令「特殊标注这是做T」）。
			b.WriteString("【做T】")
		}
		b.WriteString("[")
		if a.RefPrice > 0 {
			fmt.Fprintf(&b, "现价:%.2f", a.RefPrice)
			if absT(a.ProfitPct) > 0.001 {
				fmt.Fprintf(&b, " 盈亏:%+.2f%%", a.ProfitPct)
			}
			// §P4 缺陷4 的口径保留：只在真实回撤（负值）时渲染，现价新高时不渲染正值噪声。
			if a.DrawdownPct < -0.001 {
				fmt.Fprintf(&b, " 回撤:%+.2f%%", a.DrawdownPct)
			}
		} else {
			b.WriteString("现价未知")
		}
		b.WriteString("]")
		if a.SuggestQty > 0 {
			fmt.Fprintf(&b, " 建议量:%d股", a.SuggestQty)
			if a.SuggestAmount > 0 {
				fmt.Fprintf(&b, "≈%.0f元", a.SuggestAmount)
			}
		}
		if a.Reason != "" {
			b.WriteString(" ")
			b.WriteString(a.Reason)
		}
		b.WriteString(" ")
		b.WriteString(e.tradeActionSuffix(a))

		direction := "利空"
		switch a.Action {
		case "止盈", "加仓":
			direction = "利好"
		case "撤单":
			direction = "中性"
		}
		verb := "卖出"
		switch a.Action {
		case "加仓":
			verb = "买入"
		case "撤单":
			verb = "撤单"
		case "减仓":
			verb = "卖出"
		}
		items = append(items, data.MessageItem{
			ID:          key,
			Scope:       sendTo,
			Code:        a.Code,
			Name:        name,
			Level:       a.Action,
			Action:      verb,
			Strategy:    a.Strategy,
			Time:        a.GeneratedAt.Format("15:04:05"),
			Title:       fmt.Sprintf("%s%s %s", tMark(isT), a.Action, name),
			Body:        b.String(),
			Direction:   direction,
			GeneratedAt: a.GeneratedAt,
		})
	}
	if len(items) == 0 {
		return
	}
	// 与旧实现同序：先判新推送（P1 桌面/Webhook + SSE），后合并入库（下一轮同键即"已存在"静默）。
	e.pushCriticalAlerts(items)
	e.pushSSEMessages(items)
	e.msgStore.Sync(items)
}

// tMark 标题里的做 T 标记（与正文前缀同一句话术，两处都来自这一个函数）。
func tMark(isT bool) string {
	if isT {
		return "做T·"
	}
	return ""
}

// absT 局部绝对值（避免为一行文案把 math 引进来；本文件只用到这一个算术）。
func absT(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
