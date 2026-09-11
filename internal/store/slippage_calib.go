// slippage_calib.go paper_trades 实测滑点统计（A.3 自动校准主路线的数据端）。
//
// 口径（信号价→实际成交价的总执行成本，bp，正=成本）：
//
//	buy  = (price − signal_price)/signal_price × 1e4（买贵为正）
//	sell = (signal_price − price)/signal_price × 1e4（卖便宜为正）
//
// 过滤 price<=0 或 signal_price<=0 的行；按 filled_at 回看 windowDays；
// 中位数在 Go 侧算（SQLite 无 median）。分组先按 strategy_type（池键，比展示名稳），
// 战法级样本不足时调用方回退全局（strategyType=""）再查询。
// English: per-side median slippage derived from paper fills (signal price vs actual fill
// price, in bps); the strategy-level sample falls back to global by re-querying with "" upstream.
package store

import (
	"sort"
	"strconv"
)

// SlippageCalib paper_trades 分方向实测滑点统计（bp）。
type SlippageCalib struct {
	BuyMedBps  float64 // 买入实测滑点中位数（bp，正=买贵）
	SellMedBps float64 // 卖出实测滑点中位数（bp，正=卖便宜）
	BuyN       int     // 买入有效样本数
	SellN      int     // 卖出有效样本数
}

// PaperSlippageCalib 统计近 windowDays 天模拟盘成交的分方向滑点中位数。
// strategyType 非空时按池类型过滤；windowDays<=0 视为不限窗口。
func (d *DB) PaperSlippageCalib(strategyType string, windowDays int) (*SlippageCalib, error) {
	q := `SELECT side,
		CASE WHEN side = 'buy' THEN (price - signal_price) / signal_price * 10000
		     ELSE (signal_price - price) / signal_price * 10000 END AS bps
		FROM paper_trades
		WHERE price > 0 AND signal_price > 0 AND side IN ('buy','sell')`
	args := []any{}
	if strategyType != "" {
		q += ` AND strategy_type = ?`
		args = append(args, strategyType)
	}
	if windowDays > 0 {
		// filled_at 为 'YYYY-MM-DD HH:MM:SS' 文本，date() 字典序比较即可回看窗口
		q += ` AND filled_at >= date('now','localtime',?)`
		args = append(args, "-"+strconv.Itoa(windowDays)+" day")
	}
	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var buys, sells []float64
	for rows.Next() {
		var side string
		var bps float64
		if err := rows.Scan(&side, &bps); err != nil {
			continue
		}
		if side == "buy" {
			buys = append(buys, bps)
		} else {
			sells = append(sells, bps)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 中位数：偶数样本取两中值均值（Go 侧排序，样本量千级以内代价可忽略）
	med := func(xs []float64) (float64, int) {
		if len(xs) == 0 {
			return 0, 0
		}
		sort.Float64s(xs)
		m := len(xs) / 2
		if len(xs)%2 == 1 {
			return xs[m], len(xs)
		}
		return (xs[m-1] + xs[m]) / 2, len(xs)
	}
	buyMed, buyN := med(buys)
	sellMed, sellN := med(sells)
	return &SlippageCalib{BuyMedBps: buyMed, SellMedBps: sellMed, BuyN: buyN, SellN: sellN}, nil
}
