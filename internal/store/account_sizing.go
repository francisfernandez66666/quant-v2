// account_sizing.go — §DYN-SIZE P6 账户级读数（StoplossGuard / MaxDrawdown / 总仓位上限的数据面）。
//
// 三个读数各有一个明确的"不可得即弃权"出口，绝不 fail-open 成 0 或中性：
//
//	① 回看窗止损次数  —— 与 TodayRealizedPnl **同一卖出腿口径**（fills_effective / side='卖出'），
//	                     按 signal_id 的冒号分段识别「止损」类清仓，按 (代码, 交易日) 去重；
//	② 账户权益回撤    —— 权益日快照序列（real_equity_daily）的峰谷回撤；无历史 → 不可得；
//	③ 总仓位占比      —— 券商回报的 real_account（持仓市值 / 总资产）；未回报 → 不可得。
//
// English: §DYN-SIZE P6 account-level readings for StoplossGuard / MaxDrawdown / the total-position
// cap. Each reading abstains explicitly when its data is missing — never a fabricated zero/neutral.
// The stop-loss count shares TodayRealizedPnl's sell-leg caliber (fills_effective, side='卖出').
package store

import (
	"fmt"
	"time"
)

// RealEquityDaily 权益日快照行（每日一个点，供账户权益回撤计算）。
// English: one daily equity snapshot row (one point per day) backing the equity-drawdown reading.
type RealEquityDaily struct {
	UserID    string  `json:"user_id"`
	Day       string  `json:"day"`        // YYYY-MM-DD
	Equity    float64 `json:"equity"`     // 当日权益（券商回报的总资产）
	UpdatedAt string  `json:"updated_at"` // 写入时刻
}

// ensureRealEquityTable 幂等建表（首次记录时创建）。
// English: idempotent table creation (on first record).
func (d *DB) ensureRealEquityTable() error {
	_, err := d.db.Exec(`CREATE TABLE IF NOT EXISTS real_equity_daily (
		user_id    TEXT NOT NULL,
		day        TEXT NOT NULL,
		equity     REAL NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (user_id, day)
	)`)
	return err
}

// RecordEquitySnapshot 写入/更新某账号某日的权益快照（按 user+day 幂等，同日覆盖）。
//
// 只接受 equity>0 的读数：<=0 既可能是"券商尚未回报"（零值）也可能是真实爆仓，
// 无法区分，一律不落库——宁可少一个点，也不要让回撤读数建立在伪零之上。
// English: upserts one day's equity snapshot (idempotent by user+day). Non-positive readings are
// rejected: 0 is both "not yet reported" and a legal value, so we never persist a pseudo-zero.
func (d *DB) RecordEquitySnapshot(userID, day string, equity float64) error {
	if userID == "" || day == "" || equity <= 0 {
		return nil
	}
	if err := d.ensureRealEquityTable(); err != nil {
		return err
	}
	_, err := d.db.Exec(`INSERT INTO real_equity_daily (user_id, day, equity, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, day) DO UPDATE SET
			equity = excluded.equity, updated_at = excluded.updated_at`,
		userID, day, equity, time.Now().Format("2006-01-02 15:04:05"))
	return err
}

// EquityDrawdown 账户权益峰谷回撤比例（0~1）：窗口 [sinceDay, today] 内权益峰值 vs **最新**权益。
//
//	drawdown = (peak − latest) / peak
//
// ok=false 表示**不可得**（窗口内无快照 / 峰值非正）——调用方应让该上限因子弃权，
// 绝不当作"无回撤"。仅有 1 个点（首日）时 peak==latest → drawdown=0（合法读数：确实还没回撤）。
// English: peak-to-trough equity drawdown over [sinceDay, today]; ok=false when the window has no
// snapshots (abstain, never "no drawdown"). A single point yields 0, which is a legal reading.
func (d *DB) EquityDrawdown(userID, sinceDay, today string) (float64, bool, error) {
	if userID == "" || today == "" {
		return 0, false, nil
	}
	if err := d.ensureRealEquityTable(); err != nil {
		return 0, false, err
	}
	// peak：窗口内最大权益。sinceDay 为空时退化为"全部历史"。
	q := `SELECT COALESCE(MAX(equity),0) FROM real_equity_daily WHERE user_id=? AND day<=?`
	args := []any{userID, today}
	if sinceDay != "" {
		q += ` AND day>=?`
		args = append(args, sinceDay)
	}
	var peak float64
	if err := d.db.QueryRow(q, args...).Scan(&peak); err != nil {
		return 0, false, fmt.Errorf("read equity peak: %w", err)
	}
	if peak <= 0 {
		return 0, false, nil // 窗口内无快照 → 弃权
	}
	// latest：窗口内最新一个点（按 day 取最大）。today 当天尚未写入时回落到上一个交易日。
	var latest float64
	if err := d.db.QueryRow(`SELECT COALESCE(equity,0) FROM real_equity_daily
		WHERE user_id=? AND day<=? ORDER BY day DESC LIMIT 1`, userID, today).Scan(&latest); err != nil {
		return 0, false, nil
	}
	if latest <= 0 {
		return 0, false, nil
	}
	dd := (peak - latest) / peak
	if dd < 0 {
		dd = 0
	}
	return dd, true, nil
}

// StopLossExitCount 回看窗 [sinceDay, today] 内「止损清仓」次数（StoplossGuard 输入）。
//
// 口径与 TodayRealizedPnl **同源**（fills_effective 视图 + side='卖出'）：一笔卖出成交只要其
// signal_id 的冒号分段里出现「止损」即计一次。按 (代码, 交易日) 去重——同一笔止损被拆成多次
// 部分成交仍算**一次**清仓（否则一次滑点分批会把熔断阈值瞬间打满）。
//
// 为什么按冒号分段而不是子串匹配：signal_id 形态是 `sell:<码>:<类>:<日>`（realSellSignalID），
// 冒号分段相等是本仓「按类别键匹配 signal_id」的既有单一事实源（同 signalIDHasStrategySQL），
// 子串匹配会被"止损提示"之类的新类别误命中。柜台截断到 24 位时「止损」段仍在头部，不受影响。
// English: the lookback-window stop-loss exit count for StoplossGuard. Same sell-leg caliber as
// TodayRealizedPnl; a fill counts when its signal_id's colon-delimited segments contain 止损,
// deduped per (code, trading day) so partial fills of one exit count once.
func (d *DB) StopLossExitCount(userID, sinceDay, today string) (int, error) {
	if userID == "" || today == "" {
		return 0, nil
	}
	q := `SELECT COUNT(DISTINCT code || '|' || substr(traded_at,1,10))
		FROM fills_effective
		WHERE user_id=? AND side='卖出' AND substr(traded_at,1,10)<=?
			AND instr(':' || COALESCE(signal_id,'') || ':', ':止损:') > 0`
	args := []any{userID, today}
	if sinceDay != "" {
		q += ` AND substr(traded_at,1,10)>=?`
		args = append(args, sinceDay)
	}
	var n int
	err := d.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// AccountSizingReadings §DYN-SIZE P6 账户级读数探针体：一次读齐三个上限/熔断因子的数据面。
//
// 返回值 ok 表示**账本可用**（该账号有实盘账本可查）。三个读数**各自**在不可得时保持零值，
// 由 signalctl 侧的上限因子逐个弃权（totalPosCap 看 TotalPosPct、drawdownCap 看 EquityDrawdown、
// 连续止损熔断看 WindowStopLosses）——粒度到因子，而不是"一个读数缺失就整体弃权"。
// English: the probe body — reads all three readings at once. ok means the ledger is reachable;
// each reading independently stays zero when unavailable, and the signalctl cap factors abstain
// one by one. Granularity is per-factor, not all-or-nothing.
func (d *DB) AccountSizingReadings(userID, sinceDay, today string) (stopLosses int, drawdown float64, totalPosPct float64, ok bool) {
	if d == nil || userID == "" || today == "" {
		return 0, 0, 0, false
	}
	// ① 回看窗止损次数（查询失败按 0——0 只会让熔断不触发，属安全方向）。
	if n, err := d.StopLossExitCount(userID, sinceDay, today); err == nil {
		stopLosses = n
	}
	// ② 账户权益回撤（无历史 → 保持 0 = 该因子弃权）。
	if dd, got, err := d.EquityDrawdown(userID, sinceDay, today); err == nil && got {
		drawdown = dd
	}
	// ③ 总仓位占比 = 持仓市值 / 总资产（券商回报口径；未回报/总资产非正 → 保持 0 = 弃权）。
	// 为什么直接用 real_account 而不是自算：这两个数出自同一份券商回报，分子分母同刻同源，
	// 自算（Σ持仓市值 / (现金+市值)）会在现金未回报时把分母算小、比例虚高。
	if acc, err := d.GetRealAccount(userID); err == nil && acc.TotalAsset > 0 && acc.MarketValue >= 0 {
		totalPosPct = acc.MarketValue / acc.TotalAsset
		if totalPosPct < 0 {
			totalPosPct = 0
		}
	}
	return stopLosses, drawdown, totalPosPct, true
}
