// realized_pnl_heartbeat.go — 「有卖出但已实现盈亏恒为 0」业务心跳读数（§0929HB-2，
// 09-29 全量审计批 ⑪-4：外部监控只证"进程应答"，全部告警都是"越界型"，没有一条"应有值缺失型"）。
//
// 这条心跳要抓的是什么（因果链，照 docs/FIX_PLAN_20260929.md ⑪-4）：
//   - 日内亏损熔断闸（risk/gate.go checkDayLoss）与成交页已实现盈亏列都必须读同一个数
//     （store.TodayRealizedPnl，§0927AUDIT-D1 把卖出腿佣金+印花税补进来正是为了这条不变量）；
//   - 如果卖出腿的记账断了——成本取不到（costBasisFor 全失败）、方向错记成买入
//     （§0925EVE D-25 实录那族：方向错记→回款 0→预算占满→已实现 0）、费用字段整列为空——
//     已实现盈亏会**恒等于 0**，而 0 在两个读数点上的表现都是"一切正常"：
//     熔断闸判 pnl>=0 直接放行，成交页显示 0.00 没人觉得刺眼；
//   - 现有五条阈值告警（越界型）在这种状态下全部沉默：没有越界，只有"该有的数没了"。
//
// 判据为什么取"当日有卖出成交 且 当日已实现盈亏为 0"，而不是审计报告原文的"连续 M 日"：
//
//	① 历史日重算不可靠——TodayRealizedPnl 的成本口径读的是**当前持仓**（已清仓才回落今日买入均价），
//	   隔天再算昨天的数会因为仓位已变而 fail-open 成 0，"连续 M 日为 0"会把正常清仓误报成故障；
//	② 当日判定用的正是熔断闸当时吃的那个数，读数点与被保护对象严格同源；
//	③ 真断了就会天天断：这条规则每天盘中都会重新成立一次，路由冷却窗负责去重，
//	   不需要我们自己维护一个跨日计数器（那需要新的持久化面）。
//	"没卖出"的日子读数恒 0 是**合法事实**，不参与判定——否则"今天没交易"会被冒充"账断了"。
//
// English: business heartbeat for the "sold today but realized P&L is exactly zero" signature.
// Absence-type alerting (§0929HB): the breaker and the fills page must see the same nonzero number
// once sell fills exist; a silently broken sell-side ledger yields 0.00 and every existing
// threshold rule stays quiet. Same-day scope only, because TodayRealizedPnl's cost basis reads
// current positions and a historical recompute would fail open to zero.
package store

import "math"

// realizedPnlZeroEpsilon 已实现盈亏"视为 0"的容差（元）。
// 取半分钱：金额列都按分入账，|pnl|<0.005 不可能由真实价差+费用组合产生，只能是"没有数"。
// English: half a cent — fees make an exact zero economically impossible, so a zero here means
// "no number was produced", not "break-even".
const realizedPnlZeroEpsilon = 0.005

// RealizedPnlHeartbeat 当日已实现盈亏心跳读数（供量规/告警/日志三个出口共用同一份事实）。
// English: one day's heartbeat reading, shared by the gauge/alert/log egresses.
type RealizedPnlHeartbeat struct {
	UserID      string  `json:"user_id"`
	Day         string  `json:"day"`          // YYYY-MM-DD（cntime 口径，与 fills.traded_at 前 10 位同源）
	SellFills   int     `json:"sell_fills"`   // 当日卖出成交笔数（fills_effective 生效方向）
	RealizedPnl float64 `json:"realized_pnl"` // 当日已实现盈亏（元，与熔断闸同一个函数）
	Suspicious  bool    `json:"suspicious"`   // 有卖出 且 已实现盈亏为 0 ⇒ 账断了的样子
}

// CountSellFillsByDay 当日卖出成交笔数（生效方向 fills_effective，与 ListFillsByDay 同一张表、
// 同一个 substr(traded_at,1,10) 日界口径）。刻意不读原始 fills 表：§FILL-AMEND 之后
// "当日到底卖了几笔"的唯一真值是生效视图，勘误过的方向必须算进去，否则心跳与熔断闸
// 会各自数出不同的卖出笔数。
// English: sell-fill count for a day from the effective view — the same table and day boundary
// TodayRealizedPnl uses, so the two legs can never count different numbers of sells.
func (d *DB) CountSellFillsByDay(userID, day string) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM fills_effective
		WHERE user_id=? AND side='卖出' AND substr(traded_at,1,10)=?`, userID, day).Scan(&n)
	return n, err
}

// RealizedPnlHeartbeatForUser 算某账号某日的心跳读数。
// 语义三条，逐条对应上面的判据设计：
//  1. 当日无卖出成交 → Suspicious=false（读数 0 是合法事实，不判可疑）；
//  2. 有卖出成交 → 复用 TodayRealizedPnl（**同一次调用**，不自己重写公式），
//     |pnl|<realizedPnlZeroEpsilon 才判可疑；
//  3. 任一查询失败返回 error，由调用方决定"不伪造读数"（写 0 不触发 + 留痕），
//     绝不把"查库失败"报成"账断了"——那是把观测面故障冒充成资金面故障。
//
// English: computes the heartbeat for one user/day; reuses TodayRealizedPnl verbatim so the
// alert can never diverge from the number the breaker acts on.
func (d *DB) RealizedPnlHeartbeatForUser(userID, day string) (RealizedPnlHeartbeat, error) {
	h := RealizedPnlHeartbeat{UserID: userID, Day: day}
	n, err := d.CountSellFillsByDay(userID, day)
	if err != nil {
		return RealizedPnlHeartbeat{}, err
	}
	h.SellFills = n
	if n == 0 {
		return h, nil
	}
	pnl, err := d.TodayRealizedPnl(userID, day)
	if err != nil {
		return RealizedPnlHeartbeat{}, err
	}
	h.RealizedPnl = pnl
	h.Suspicious = math.Abs(pnl) < realizedPnlZeroEpsilon
	return h, nil
}
