// signal_decisions.go — §DECISION-TODAY（2026-10-28）信号仓「动态仓位计算过程」快照的读写面。
//
// 这张表回答一个问题：**今天这条信号，为什么是这些量（或为什么没买）**。
// 以前这些读数（MarketScale/StockScale、目标/当前/剩余、行业/单票/簇额度、约束项、拒因）
// 只存在于 dynamicBuyQty 的内存与 log.Printf 里，下完单就散失；事后只能靠人翻当天日志。
//
// 口径纪律：
//   - day 用北京时间交易日（YYYYMMDD），一天一信号一行（last-write）：每日重新定权，不累计；
//   - 被拦/未下单同样落一行（stage=skipped），否则"没买"永远没有解释；
//   - 本表只做观测，不参与任何判定；读不到就是读不到，绝不给 0 冒充。
//
// English: persistence for the per-day, per-signal dynamic-sizing snapshot (including rejected
// signals). Read-only for consumers; never feeds back into any decision.
package store

import (
	"fmt"
	"strings"
)

// SignalDecision 一条「当日动态仓位」快照（与 signal_decisions 表一一对应）。
type SignalDecision struct {
	UserID          string  `json:"user_id"`
	Day             string  `json:"day"`       // YYYYMMDD（北京时间交易日）
	Channel         string  `json:"channel"`   // live / paper
	SignalID        string  `json:"signal_id"` // 信号 ID（缺省用 code+strategy 兜底）
	Code            string  `json:"code"`
	Strategy        string  `json:"strategy_key"`
	At              string  `json:"at"`    // 计算时刻（RFC3339）
	Stage           string  `json:"stage"` // evaluated / placed / rejected / skipped
	MarketScale     float64 `json:"market_scale"`
	StockScale      float64 `json:"stock_scale"`
	PerTradePct     float64 `json:"per_trade_pct"`
	Total           float64 `json:"total"`
	Cash            float64 `json:"cash"`
	Target          float64 `json:"target"`
	CurStrat        float64 `json:"cur_strat"`
	Room            float64 `json:"room"`
	IndustryRoom    float64 `json:"industry_room"`
	StockRoom       float64 `json:"stock_room"`
	ClusterRoom     float64 `json:"cluster_room"`
	CappedBy        string  `json:"capped_by"`
	Qty             int     `json:"qty"`
	Notional        float64 `json:"notional"`
	MissingKeyInput bool    `json:"missing_key_input"`
	CashStale       bool    `json:"cash_stale"`
	Reason          string  `json:"reason"`
}

// UpsertSignalDecision 写入/覆盖一条当日快照（同 (user,day,channel,code,strategy,signal) 幂等）。
func (d *DB) UpsertSignalDecision(x SignalDecision) error {
	if strings.TrimSpace(x.UserID) == "" || strings.TrimSpace(x.Day) == "" || strings.TrimSpace(x.Code) == "" {
		return fmt.Errorf("signal decision: user/day/code 不能为空")
	}
	pk := func() string {
		sid := strings.TrimSpace(x.SignalID)
		if sid == "" {
			sid = x.Code + "|" + x.Strategy // 无信号 ID 时用代码+战法兜底，仍保证一天一行
		}
		return sid
	}
	// 同日同信号的 last-write：主键里含 day，跨日天然隔离；被拦（stage=skipped）与放行写同一行，
	// 后者覆盖前者——用户看到的是"今天最终结论"，不是每次扫描的时间序列。
	_, err := d.db.Exec(`INSERT INTO signal_decisions
		(user_id, day, channel, signal_id, code, strategy_key, at, stage,
		 market_scale, stock_scale, per_trade_pct, total, cash, target, cur_strat, room,
		 industry_room, stock_room, cluster_room, capped_by, qty, notional,
		 missing_key_input, cash_stale, reason)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id, day, channel, code, strategy_key, signal_id) DO UPDATE SET
		 at=excluded.at, stage=excluded.stage,
		 market_scale=excluded.market_scale, stock_scale=excluded.stock_scale,
		 per_trade_pct=excluded.per_trade_pct, total=excluded.total, cash=excluded.cash,
		 target=excluded.target, cur_strat=excluded.cur_strat, room=excluded.room,
		 industry_room=excluded.industry_room, stock_room=excluded.stock_room,
		 cluster_room=excluded.cluster_room, capped_by=excluded.capped_by,
		 qty=excluded.qty, notional=excluded.notional,
		 missing_key_input=excluded.missing_key_input, cash_stale=excluded.cash_stale,
		 reason=excluded.reason`,
		x.UserID, x.Day, orDefaultStr(x.Channel, "live"), pk(), x.Code, x.Strategy,
		x.At, x.Stage, x.MarketScale, x.StockScale, x.PerTradePct, x.Total, x.Cash,
		x.Target, x.CurStrat, x.Room, x.IndustryRoom, x.StockRoom, x.ClusterRoom,
		x.CappedBy, x.Qty, x.Notional, b2i(x.MissingKeyInput), b2i(x.CashStale), x.Reason)
	return err
}

// SignalDecisionsForDay 返回某账号某交易日的全部快照（at 倒序；day 为 YYYYMMDD）。
func (d *DB) SignalDecisionsForDay(userID, day string) ([]SignalDecision, error) {
	rows, err := d.db.Query(`SELECT user_id, day, channel, signal_id, code, strategy_key, at, stage,
		market_scale, stock_scale, per_trade_pct, total, cash, target, cur_strat, room,
		industry_room, stock_room, cluster_room, capped_by, qty, notional,
		missing_key_input, cash_stale, reason
		FROM signal_decisions WHERE user_id=? AND day=? ORDER BY at DESC, code ASC`, userID, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SignalDecision, 0, 32)
	for rows.Next() {
		var x SignalDecision
		var mk, cs int
		if err := rows.Scan(&x.UserID, &x.Day, &x.Channel, &x.SignalID, &x.Code, &x.Strategy,
			&x.At, &x.Stage, &x.MarketScale, &x.StockScale, &x.PerTradePct, &x.Total, &x.Cash,
			&x.Target, &x.CurStrat, &x.Room, &x.IndustryRoom, &x.StockRoom, &x.ClusterRoom,
			&x.CappedBy, &x.Qty, &x.Notional, &mk, &cs, &x.Reason); err != nil {
			return nil, err
		}
		x.MissingKeyInput = mk != 0
		x.CashStale = cs != 0
		out = append(out, x)
	}
	return out, rows.Err()
}

// PruneSignalDecisions 删除早于 beforeDay（YYYYMMDD）的快照，返回删除行数。
// 只做保留期收缩，不参与判定；调用方按日一次即可。
func (d *DB) PruneSignalDecisions(beforeDay string) (int64, error) {
	if strings.TrimSpace(beforeDay) == "" {
		return 0, nil
	}
	res, err := d.db.Exec(`DELETE FROM signal_decisions WHERE day < ?`, beforeDay)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// orDefaultStr 空串回退（小工具，避免在 SQL 参数处写分支）。
func orDefaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
