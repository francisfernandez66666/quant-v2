// trade_action_ledger.go — §TRADE-CARD（2026-11-05）自动交易链"这一轮真的做了什么"的进程内留痕。
//
// 起因是 owner 的一条产品口径：交易提示卡完全拿量化交易的数据来做（撤/留/加/减/走，做 T 要特殊
// 标注），"量化自动交易准备怎么做，你就怎么提醒"；不到交易级别的不许提醒。
//
// 为什么非要在执行点留痕，而不是让提醒层自己去查/自己判：
//   - 旧实现往消息中心推的那句【已触发自动卖出】判的是**开关**
//     （`ctrl.Enabled() && Mode()=="auto" && AutoSell`，见 scoring_loop.go pushRealAdvice），
//     不是这一笔的真实结果。风控闸（涨跌停不可追、集中度、额度、VWAP 缺失 fail-closed）把单拒掉时，
//     屏上照样写"已触发"——这正是本仓一路在锤的"降级报成功"；
//   - 反向 reconstruction 也不行：买入幂等键是 `buy:<代码>:<战法键>:<日>`，而建议行上的
//     「战法」是持仓归属的战法、卖出键里还带减仓轮次后缀（`sell:<码>:<类>:<日>:r<量>`），
//     在提醒层拼键＝猜键；猜错一次就把"其实下过单"报成"没下单"，那是往钱的方向撒谎。
//     所以只在**委托真正发出的那一处**（placeOrderNow / sellRealPosition / T 腿下单）记一条。
//
// 三态，缺一即假读数（missing ≠ 否）：
//   - Placed=true  ：委托已被受理（≠ 成交，更 ≠ 已入库的终态）；
//   - Placed=false ：本轮**一笔都没发出**，Reason 给出是哪道闸/哪个拒因；
//   - 查无此记录   ：本进程没见证过这个动作（多半是重启之前发生的）⇒ 提醒不许写"没下单"，
//     只写"本进程未见证，请核对当日委托"。落盘真凭据永远是 orders 表，快照只是即时窗。
//
// 当日键控、条数封顶、重启即空——它是读数不是账本：任何资金判定都不许读它。
// English: an in-process, day-keyed, bounded record of what the auto-trading chain actually did
// (placed / refused-with-reason), written at the exact place an order is sent. The advice card and
// trade-level alerts read only this; absence means "not witnessed by this process", never "not done".
package engine

import (
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
)

// 动作种类的字面量。提醒文案与卡上的标签都消费这两个值，门禁锚点也钉在这里。
const (
	tradeActionBuy  = "buy"
	tradeActionSell = "sell"
)

// tradeActionLimit 当日留痕条数帽。一笔动作一行，正常一天几十笔；封顶是为了挡住
// "每次轮询都重记一条"的实现把内存吃穿。超限时按时刻丢最旧。
const tradeActionLimit = 300

// tradeAction 一次下单动作的真实结果（撤单动作不在这里，见 trading.SweepAction——
// 那条腿有它自己的留痕三列，且它的"动作"就是撤单本身）。
type tradeAction struct {
	Kind      string    `json:"kind"`  // tradeActionBuy / tradeActionSell
	Code      string    `json:"code"`  // 纯代码（不带 .SH/.SZ 后缀，与 orders.signal_id 同尺）
	Class     string    `json:"class"` // 卖单类别（止损/止盈/减仓/m8）；买单为空
	OrderID   string    `json:"order_id"`
	Placed    bool      `json:"placed"` // true=已受理（≠成交）；false=一笔都没发出
	Qty       int       `json:"qty"`    // 实际报出量（风控减量后），0=未报出
	Price     float64   `json:"price"`
	Reason    string    `json:"reason"` // 受理回执或拒因（发给人的话术在这里）
	At        time.Time `json:"at"`     // 动作时刻
	Day       string    `json:"day"`    // YYYYMMDD（data.TradingDayDate），跨日自动失效
	IntradayT bool      `json:"t_leg"`  // 这一腿是日内回转（做 T），不是战略加减（owner 令特殊标注）
}

// key 留痕的去重键。做 T 与战略加减**分键**：同一个代码同一天既做 T 又战略加仓是合法形态，
// 折成一条就会把战略加仓标成"做T"，或反过来把 T 腿当成建仓被撤的理由。
func (a tradeAction) key() string {
	t := "s" // strategic
	if a.IntradayT {
		t = "t" // 做 T 的一腿
	}
	return a.Kind + "|" + a.Code + "|" + a.Class + "|" + t
}

// recordTradeAction 记一次下单动作（同键覆盖为最新形态，日切清空）。
//
// 覆盖而不是追加：卡上一码一行，最新事实才有用（先被集中度拒、后放行 ⇒ 应当显示已受理）。
// 代价写在注释里，不假装没有：同一天同一键被拒过、之后成功，"被拒过"这一次只在日志里可查。
func (e *Engine) recordTradeAction(a tradeAction) {
	a.Code = strings.TrimSpace(pureTsCode(a.Code))
	if a.Code == "" || (a.Kind != tradeActionBuy && a.Kind != tradeActionSell) {
		return // 认不出种类/代码的留痕在卡上无法定位，不配进快照（也不许瞎猜一个键）
	}
	if a.At.IsZero() {
		a.At = time.Now()
	}
	if a.Day == "" {
		a.Day = data.TradingDayDate(a.At)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tradeActDay != a.Day {
		e.tradeActDay = a.Day
		e.tradeActions = nil // 跨日不复活（与 orders / t_rounds 的 day-keyed 口径一致）
	}
	if e.tradeActions == nil {
		e.tradeActions = make(map[string]tradeAction)
	}
	e.tradeActions[a.key()] = a
	if len(e.tradeActions) <= tradeActionLimit {
		return
	}
	// 超限自保：先把 map 摊平成切片，按**动作时刻**升序排，再从头部（最旧）删到帽内。
	// 排序不能用 map 迭代顺序——Go 的 map 顺序是随机的，"最旧"必须由时刻判定，
	// 否则被删掉的可能是刚记下的这一笔，读数反过来变成"什么都没发生"。
	acts := make([]tradeAction, 0, len(e.tradeActions))
	for _, v := range e.tradeActions {
		acts = append(acts, v)
	}
	sort.Slice(acts, func(i, j int) bool { return acts[i].At.Before(acts[j].At) })
	// 只删超出的那一段（len−帽），其余全部留下；整本清空是另一种错法。
	for _, v := range acts[:len(acts)-tradeActionLimit] {
		delete(e.tradeActions, v.key())
	}
}

// tradeActionFor 查某一类动作的最新留痕。ok=false ＝**本进程没见证过**，
// 调用方必须把它读成"不知道"，绝不许折成"今天没做过"。
func (e *Engine) tradeActionFor(kind, code, class string, intradayT bool) (tradeAction, bool) {
	code = strings.TrimSpace(pureTsCode(code))
	if code == "" {
		return tradeAction{}, false
	}
	k := tradeAction{Kind: kind, Code: code, Class: class, IntradayT: intradayT}.key()
	e.mu.RLock()
	defer e.mu.RUnlock()
	a, ok := e.tradeActions[k]
	return a, ok
}

// tradeActionsSnapshot 当日全部留痕（时刻倒序）。给用例与"这一轮自动交易做了什么"的日志用，
// 不参与任何判定。
func (e *Engine) tradeActionsSnapshot() []tradeAction {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.tradeActions) == 0 {
		return nil
	}
	out := make([]tradeAction, 0, len(e.tradeActions))
	for _, v := range e.tradeActions {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}
