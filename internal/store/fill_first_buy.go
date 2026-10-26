// fill_first_buy.go — §ANCHOR-FIRST 回填第四档之前的"买入历史最早成交价"读数（2026-10-26）。
//
// 为什么单独一个查询而不是复用 TodayBoughtQty 那一族：锚点回填要的语义是**这笔持仓第一次买入那刻
// 的价格**，与"今天买了多少股"是两个问题。而且它必须读 `fills_effective`（人工勘误已生效的视图）：
// 一笔被勘误成卖出的成交如果还留在原始 fills 的"买入"里，回填就会把一个根本没发生过的买入价
// 写成风控基准——那是 §FILL-AMEND 已经锤过一次的形态，读数面必须同源。
//
// 代码格式是这里的第二个坑：纸面账本用 6 位纯代码（"600000"），成交表存的是带后缀的复合码
// （"600000.SH"）。查询同时接受两种写法（按纯代码补三个交易所后缀），否则回填会以为"没有历史买入"
// 而静默退到成本档——**降级不报错**正是本批要收掉的那类缺陷。
//
// English: the earliest buy fill price for one code, read through the amendment view and matching
// both the bare 6-digit code and its exchange-suffixed forms, so an anchor backfill never silently
// degrades to the cost tier just because of a code-format mismatch.
package store

import (
	"log"
	"strings"
)

// EarliestBuyFillPrice 返回该代码**最早一笔买入成交价**（无记录或价非法时 ok=false）。
// code 可以是纯 6 位（纸面账本口径）或带 .SH/.SZ/.BJ 后缀（成交表口径）。
// English: the earliest buy fill price for a code, accepting both code spellings.
func (d *DB) EarliestBuyFillPrice(code string) (price float64, ok bool) {
	c := strings.TrimSpace(code)
	if c == "" {
		return 0, false
	}
	// 三种后缀都列出来而不是 LIKE 'code%'：LIKE 会把 60000 匹配到 600000.SH 这类前缀码上，
	// 那等于给另一只票写了锚点。显式枚举是唯一安全的写法。
	var forms []string
	if strings.Contains(c, ".") {
		forms = []string{c}
	} else {
		base := c
		if dot := strings.Index(base, "."); dot > 0 {
			base = base[:dot]
		}
		forms = []string{base + ".SH", base + ".SZ", base + ".BJ"}
	}
	args := make([]any, 0, len(forms)+1)
	placeholders := make([]string, 0, len(forms))
	for _, f := range forms {
		placeholders = append(placeholders, "?")
		args = append(args, f)
	}
	args = append(args, c) // 也接受表里就存成纯码的历史行
	q := `SELECT price FROM fills_effective
		WHERE side='买入' AND price > 0 AND code IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY traded_at ASC, id ASC LIMIT 1`
	if err := d.db.QueryRow(q, args...).Scan(&price); err != nil {
		// 查不到是常态（新标的、历史已按保留期裁剪），不当错误刷屏；真错误（表不存在等）由
		// 调用方按"该档不可得"处理，回填会退到下一档并如实标 source。
		return 0, false
	}
	if price <= 0 {
		return 0, false
	}
	log.Printf("[store] §ANCHOR-FIRST 首买价读数 %s → %.4f（fills_effective 最早一笔买入）", c, price)
	return price, true
}

// TLegOrderLedger 当日日内回转腿的委托台账（§6.3 B 组第 19 条：报撤比是量化证据，不是可选项）。
//
// 为什么按 `intraday_t=1` 而不是按 signal_id 前缀 `t0:` 过滤：这一列就是为这个统计加的
// （批 7/W8-A），前缀匹配会把"哪天有人手工塞了一条 t0: 开头的战略单"算成 T 腿；
// 列是结构化事实，前缀是约定——能用列的时候永远用列。
//
// 三个数的含义要分清楚，否则"报撤比"会被读成别的：
//   - placed   当日为 T 腿报出的委托笔数（含同一腿撤后重报的每一笔——重报就是新委托，占柜台报单量）；
//   - canceled 其中终态为撤单的笔数（已撤/部撤）；
//   - filled   其中已成交（部成/已成）的笔数。
//
// 券商异常交易观察看的就是 placed 与 canceled 的量级，所以这里**不做任何去重**：
// 去重后的数字好看但没有解释力。
//
// English: today's T-leg order ledger (placements, cancellations, fills) — deliberately counted
// per submitted order, because the broker's abnormal-trading watch looks at submission volume.
func (d *DB) TLegOrderLedger(userID, day string) (placed, canceled, filled int, err error) {
	if d == nil || d.db == nil {
		return 0, 0, 0, nil
	}
	rows, err := d.db.Query(`SELECT status, COUNT(*) FROM orders
		WHERE intraday_t = 1 AND substr(created_at,1,10) = ? AND (? = '' OR user_id = ? OR user_id = '')
		GROUP BY status`, day, userID, userID)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if serr := rows.Scan(&st, &n); serr != nil {
			continue
		}
		placed += n
		switch st {
		case "已撤", "部撤", "作废":
			canceled += n
		case "已成":
			filled += n
		case "部成":
			filled += n
		}
	}
	return placed, canceled, filled, nil
}
