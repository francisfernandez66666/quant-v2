// optimization_results.go 参数扫参结果存储（§P2-c STRATEGY_OPTIMIZE_PLAN）。
//
// 每次 optimize 任务完成后，worker 解析子进程输出的 SWEEP_JSON 行，
// 把 TOP-N 排名写入本表（status=pending）。用户在「优化结果」页审批：
// approve → 规则级参数覆盖写回 applied_*.json 并热重载生效；reject → 淘汰。
//
// English: per-task sweep rankings. Worker persists the TOP-N rows after parsing the
// SWEEP_JSON line; approvals turn a row into rule-level parameter overrides.
package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// OptimizationResult 一次扫参任务的单条排名行。
type OptimizationResult struct {
	ID           int64       `json:"id"`                      // 自增主键
	TaskID       int64       `json:"task_id"`                 // 所属扫参任务 ID
	Rank         int         `json:"rank"`                    // 排名
	Strategy     string      `json:"strategy"`                // 显示名：双响炮/因子战法#1/波动突破战法…
	StrategyKind string      `json:"strategy_kind,omitempty"` // 规则 ID（fac_1/pat_2）或空（内置）
	ParamsJSON   string      `json:"-"`                       // 参数JSON
	Params       SweepParams `json:"params"`                  // 参数
	Objective    string      `json:"objective"`               // 目标函数
	WinRate      float64     `json:"win_rate"`                // 胜率
	ProfitFactor float64     `json:"profit_factor"`           // 盈亏比
	Expectancy   float64     `json:"expectancy"`              // 期望收益率
	Win          int         `json:"win"`                     // 盈利次数
	Loss         int         `json:"loss"`                    // 亏损次数
	AvgWinPct    float64     `json:"avg_win_pct"`             // 平均盈利百分比
	AvgLossPct   float64     `json:"avg_loss_pct"`            // 平均亏损百分比
	AvgHoldDays  float64     `json:"avg_hold_days"`           // 平均持仓天数
	TriggerCount int         `json:"trigger_count"`           // 触发次数
	Status       string      `json:"status"`                  // pending | approved | rejected（待审/通过/拒绝）
	CreatedAt    string      `json:"created_at"`              // 创建时间
	GridJSON     string      `json:"grid_json,omitempty"`     // §D3 止盈×止损热力网格（冠军行携带）

	// §GAP4.5 风险调整指标（SWEEP_JSON 带出落库，前端寻优行展示）
	Sharpe          float64 `json:"sharpe"`            // 夏普比率
	MaxDrawdownPct  float64 `json:"max_drawdown_pct"`  // 最大回撤百分比
	AnnualReturnPct float64 `json:"annual_return_pct"` // 年化收益率百分比
	Calmar          float64 `json:"calmar"`            // Calmar比率
	// PoolStats 该战法对应模拟盘资金池的实测绩效（§B 列表接口运行时附加，不落库；
	// nil=无对应池或引擎不可用）。前端与回测指标并排对比。
	PoolStats *PoolLiveStats `json:"pool_stats,omitempty"` // 池级实时统计
}

// PoolLiveStats 寻优行关联的模拟盘池实测摘要（§B：回测最优 vs 模拟盘验证）。
type PoolLiveStats struct {
	WinRatePct float64 `json:"win_rate_pct"` // 已平仓胜率%
	Expectancy float64 `json:"expectancy"`   // 每笔期望收益%
	FilledBuys int     `json:"filled_buys"`  // 已撮合买入笔数（样本量参考）
}

// SweepParams 扫参组合参数（与 SWEEP_JSON 的 params 对象对应）。
type SweepParams struct {
	TakeProfitPct float64 `json:"take_profit_pct"` // 止盈百分比
	StopLossPct   float64 `json:"stop_loss_pct"`   // Stop亏损次数Pct
	HoldDays      int     `json:"hold_days"`       // 持仓天数
	MinScore      float64 `json:"min_score"`       // Min评分
}

// ParseSweepParams 从 params JSON 解析（容错：空/坏 JSON 返回零值）。
func ParseSweepParams(s string) SweepParams {
	var p SweepParams
	_ = json.Unmarshal([]byte(s), &p)
	// 向后兼容：旧格式 {trail_pct} → 新格式 {take_profit_pct}
	if p.TakeProfitPct == 0 {
		var old struct {
			TrailPct float64 `json:"trail_pct"`
		}
		if json.Unmarshal([]byte(s), &old) == nil && old.TrailPct > 0 {
			p.TakeProfitPct = old.TrailPct
		}
	}
	return p
}

// SaveOptimizationResults 幂等写入一次任务的排名（先清该 task 旧行再插，重跑不重复）。
func (d *DB) SaveOptimizationResults(taskID int64, objective string, results []map[string]any) error {
	if len(results) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM optimization_results WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO optimization_results
		(task_id, rank, strategy, strategy_kind, params, objective, win_rate, profit_factor,
		 win, loss, avg_win_pct, avg_loss_pct, expectancy, stop_loss, avg_hold_days, trigger_count,
		 sharpe, max_drawdown_pct, annual_return_pct, calmar, status, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'pending',?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Format("2006-01-02 15:04:05")
	for _, r := range results {
		rank, _ := r["rank"].(float64)
		strategy, _ := r["strategy"].(string)
		sk, _ := r["strategy_kind"].(string)
		params, _ := json.Marshal(r["params"])
		winRate, _ := r["win_rate"].(float64)
		pf, _ := r["profit_factor"].(float64)
		avgHold, _ := r["avg_hold_days"].(float64)
		count, _ := r["trigger_count"].(float64)
		win, _ := r["win"].(float64)
		loss, _ := r["loss"].(float64)
		avgWin, _ := r["avg_win_pct"].(float64)
		avgLoss, _ := r["avg_loss_pct"].(float64)
		expectancy, _ := r["expectancy"].(float64)
		stopLoss, _ := r["stop_loss_pct"].(float64)
		sharpe, _ := r["sharpe"].(float64)
		maxDD, _ := r["max_drawdown_pct"].(float64)
		annualRet, _ := r["annual_return_pct"].(float64)
		calmar, _ := r["calmar"].(float64)
		if _, err := stmt.Exec(taskID, int(rank), strategy, sk, string(params), objective,
			winRate, pf, int(win), int(loss), avgWin, avgLoss, expectancy, stopLoss, avgHold, int(count),
			sharpe, maxDD, annualRet, calmar, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListOptimizations 按任务倒序返回全部扫参任务及其排名行（前端「优化结果」页）。
func (d *DB) ListOptimizations(limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 20
	}
	taskIDs := []int64{}
	rows, err := d.db.Query(`SELECT DISTINCT task_id FROM optimization_results ORDER BY task_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			taskIDs = append(taskIDs, id)
		}
	}
	rows.Close()
	out := make([]map[string]any, 0, len(taskIDs))
	for _, tid := range taskIDs {
		items, err := d.OptimizationResultsByTask(tid)
		if err != nil {
			return nil, err
		}
		objective := ""
		if len(items) > 0 {
			objective = items[0].Objective
		}
		out = append(out, map[string]any{
			"task_id":   tid,
			"objective": objective,
			"results":   items,
		})
	}
	return out, nil
}

// optColumns 排名表查询列清单（与 scanOptRow 的 Scan 顺序一一对应，
// 新增列时两处必须同步修改）。
var optColumns = `id, task_id, rank, strategy, strategy_kind, params, objective,
	win_rate, profit_factor, win, loss, avg_win_pct, avg_loss_pct, expectancy,
	stop_loss, avg_hold_days, trigger_count,
	sharpe, max_drawdown_pct, annual_return_pct, calmar,
	status, created_at, grid_json`

// scanOptRow 读一行排名记录。
func scanOptRow(scan func(...any) error) (*OptimizationResult, error) {
	var r OptimizationResult
	var sk, obj sql.NullString
	var sl sql.NullFloat64
	var grid sql.NullString
	if err := scan(&r.ID, &r.TaskID, &r.Rank, &r.Strategy, &sk, &r.ParamsJSON, &obj,
		&r.WinRate, &r.ProfitFactor, &r.Win, &r.Loss, &r.AvgWinPct, &r.AvgLossPct,
		&r.Expectancy, &sl, &r.AvgHoldDays, &r.TriggerCount,
		&r.Sharpe, &r.MaxDrawdownPct, &r.AnnualReturnPct, &r.Calmar,
		&r.Status, &r.CreatedAt, &grid); err != nil {
		return nil, err
	}
	r.GridJSON = grid.String
	r.StrategyKind = sk.String
	r.Params = ParseSweepParams(r.ParamsJSON)
	if sl.Valid && r.Params.StopLossPct == 0 {
		r.Params.StopLossPct = sl.Float64
	}
	r.Objective = obj.String
	return &r, nil
}

// OptimizationResultsByTask 某次任务的全部排名行（rank 升序）。
func (d *DB) OptimizationResultsByTask(taskID int64) ([]*OptimizationResult, error) {
	rows, err := d.db.Query(`SELECT `+optColumns+` FROM optimization_results
		WHERE task_id = ? ORDER BY rank ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OptimizationResult
	for rows.Next() {
		r, err := scanOptRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetOptimization 单条排名记录。
func (d *DB) GetOptimization(id int64) (*OptimizationResult, error) {
	row := d.db.QueryRow(`SELECT `+optColumns+` FROM optimization_results WHERE id = ?`, id)
	return scanOptRow(row.Scan)
}

// UpdateOptimizationStatus 状态流转：pending → approved/rejected（幂等，可重复设置）。
func (d *DB) UpdateOptimizationStatus(id int64, status string) error {
	_, err := d.db.Exec(`UPDATE optimization_results SET status = ? WHERE id = ?`, status, id)
	return err
}
