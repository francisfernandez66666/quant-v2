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
	ID           int64       `json:"id"`
	TaskID       int64       `json:"task_id"`
	Rank         int         `json:"rank"`
	Strategy     string      `json:"strategy"`                // 显示名：双响炮/因子战法#1/波动突破战法…
	StrategyKind string      `json:"strategy_kind,omitempty"` // 规则 ID（fac_1/pat_2）或空（内置）
	ParamsJSON   string      `json:"-"`
	Params       SweepParams `json:"params"`
	Objective    string      `json:"objective"`
	WinRate      float64     `json:"win_rate"`
	ProfitFactor float64     `json:"profit_factor"`
	AvgHoldDays  float64     `json:"avg_hold_days"`
	TriggerCount int         `json:"trigger_count"`
	Status       string      `json:"status"` // pending | approved | rejected
	CreatedAt    string      `json:"created_at"`
}

// SweepParams 扫参组合参数（与 SWEEP_JSON 的 params 对象对应）。
type SweepParams struct {
	TrailPct float64 `json:"trail_pct"`
	HoldDays int     `json:"hold_days"`
	MinScore float64 `json:"min_score"`
}

// ParseSweepParams 从 params JSON 解析（容错：空/坏 JSON 返回零值）。
func ParseSweepParams(s string) SweepParams {
	var p SweepParams
	_ = json.Unmarshal([]byte(s), &p)
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
		 avg_hold_days, trigger_count, status, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?, 'pending', ?)`)
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
		if _, err := stmt.Exec(taskID, int(rank), strategy, sk, string(params), objective,
			winRate, pf, avgHold, int(count), now); err != nil {
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

var optColumns = `id, task_id, rank, strategy, strategy_kind, params, objective,
	win_rate, profit_factor, avg_hold_days, trigger_count, status, created_at`

// scanOptRow 读一行排名记录。
func scanOptRow(scan func(...any) error) (*OptimizationResult, error) {
	var r OptimizationResult
	var sk, obj sql.NullString
	if err := scan(&r.ID, &r.TaskID, &r.Rank, &r.Strategy, &sk, &r.ParamsJSON, &obj,
		&r.WinRate, &r.ProfitFactor, &r.AvgHoldDays, &r.TriggerCount, &r.Status, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.StrategyKind = sk.String
	r.Params = ParseSweepParams(r.ParamsJSON)
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
