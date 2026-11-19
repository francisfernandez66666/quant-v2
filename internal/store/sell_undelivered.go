// 卖出未送达计数（§SELL-UNDELIVERED，2026-11-19 补齐 owner 早前裁定的第 25 条告警量规）。
//
// 为什么单独立一个只读计数而不是复用 order_fail_rate：那一条在"外脑通道断"时会被放进
// 静默域（scoring_loop 把 order_fail_rate/uplink_stale 折进日汇总），而**通道断的那天恰好
// 就是"卖不出去"最该叫醒人的那天**。卖出意图已经产生、却没送达券商（止损/止盈没执行）
// 是资金路径上的孤立事件，必须有自己的规则与自己的量规，不能和"下单链路整体降级"共用一条。
//
// 判据口径（与 PLAN_SELL_BLIND §2.1/B3 一字对齐）：
//   - 只数当日（按 orders.created_at 的日期前缀，北京自然日，与其它 day-keyed 读面同源）；
//   - 只数 side='卖出'（买入失败是另一件事：少买一笔不造成裸敞口）；
//   - 只数 status='发送失败'（"已报/已成/已撤/废单"都不是未送达；"撤单中"是在途，不是失败）；
//   - 只数本账号（user_id 严格相等：遗留 user_id=” 行属运营账号的旧账，不让成员引擎去数）。
//
// English: read-only count of today's SELL intents that never reached the broker
// (side='卖出' and status='发送失败'), scoped to one account and one natural day.
package store

import "fmt"

// CountSellUndelivered 统计某账号某自然日"卖出意图未送达券商"的委托笔数。
//
// day 接受两种写法（调用方既有用 TradingDayDate 的 YYYYMMDD，也有用查询参数的 YYYY-MM-DD），
// 统一在函数内归一成 orders.created_at 的日期前缀再比较——比较用等值而不是 replace()，
// 这样 orders(created_at) 上的既有索引仍然可用，日级扫描不会退化成全表函数求值。
//
// 读不到（库未接、SQL 失败）时返回 error，由调用方决定写 0 还是保持上一读数：
// 本函数**不伪造 0**——"查不到"与"确实是 0 笔"在告警语义上是两件事。
func (d *DB) CountSellUndelivered(day, userID string) (int, error) {
	prefix, err := normalizeOrderDayPrefix(day)
	if err != nil {
		return 0, err
	}
	var n int
	err = d.db.QueryRow(`SELECT COUNT(*) FROM orders
		WHERE user_id=? AND side='卖出' AND status='发送失败'
		  AND substr(created_at,1,10)=?`, userID, prefix).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store count sell undelivered day=%s uid=%s: %w", prefix, userID, err)
	}
	return n, nil
}

// normalizeOrderDayPrefix 把 YYYYMMDD / YYYY-MM-DD 归一成 created_at 前缀 YYYY-MM-DD。
//
// 不做"猜格式"的宽容解析：长度不为 8 或 10 一律报错。告警量规宁可当天不响（由调用方
// 记一条 WARN），也不许因为日期格式猜错而把昨天/前天的失败单算进今天——那会训练用户
// 对这条必推规则的信任，而它是通道断链时唯一能叫醒人的一条。
func normalizeOrderDayPrefix(day string) (string, error) {
	allDigits := func(s string) bool {
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return false
			}
		}
		return true
	}
	switch len(day) {
	case 10:
		if day[4] != '-' || day[7] != '-' || !allDigits(day[0:4]) || !allDigits(day[5:7]) || !allDigits(day[8:10]) {
			return "", fmt.Errorf("store count sell undelivered: bad day %q", day)
		}
		return day, nil
	case 8:
		// 注意：8 位不等于"一定是紧凑格式"——"2026-1-9" 也是 8 个字符。
		// 只验长度会让它拼出 "2026--1-9" 这类永假前缀，运行期表现为"这条规则永远不响"。
		if !allDigits(day) {
			return "", fmt.Errorf("store count sell undelivered: bad day %q", day)
		}
		return day[0:4] + "-" + day[4:6] + "-" + day[6:8], nil
	default:
		return "", fmt.Errorf("store count sell undelivered: bad day %q", day)
	}
}
