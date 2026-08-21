// 回测任务中心持久化（B5 研究闭环）：backtest_jobs 任务状态 + backtest_event_results 断点缓存。
// 覆盖两类回测——kind='candidate'（前端单候选全量回测）与 kind='nightly'（夜间调度器全量回测），
// 均落库，quant/research 进程重启后任务可查、可恢复、可续跑。
// English: backtest task-center persistence for the B5 research loop — backtest_jobs task state plus
// backtest_event_results checkpoint cache. Covers both per-candidate ('candidate') and nightly
// ('nightly') backtests; rows are persisted so jobs stay queryable/recoverable/resumable across restarts.
package store

import (
	"database/sql"
	"time"
)
// BacktestJob 一条回测任务（持久化到 backtest_jobs）。
// English: one backtest job (persisted in backtest_jobs).
type BacktestJob struct {
	ID          int64   `json:"id"`           // 任务自增 ID
	Kind        string  `json:"kind"`         // candidate=单候选 / nightly=夜间全量 / library=战法库规则
	CandidateID int64   `json:"candidate_id"` // kind=candidate/library 时对应候选或规则 ID（nightly=0）
	Status      string  `json:"status"`       // running / paused / done / error / interrupted
	Progress    string  `json:"progress"`     // "45%"
	AvgExcess   float64 `json:"avg_excess"`   // 回测超额（h 日前瞻，done 后回填）
	Error       string  `json:"error"`        // 失败原因（error 时）
	// ResultText 战法库回测的汇总报告文本（阶段3.4：胜率/盈亏比等，done 后回填，前端直接展示）。
	// English: the library-backtest summary report text (win rate / profit factor…, backfilled on done).
	ResultText string `json:"result_text,omitempty"`
	StartedAt  string `json:"started_at"`  // 开始时间 YYYY-MM-DD HH:MM:SS
	FinishedAt string `json:"finished_at"` // 结束时间（done/error/interrupted 时）
	UpdatedAt  string `json:"updated_at"`  // 最近更新时间
}

// UpsertBacktestJob 写入/更新一条回测任务（同一 kind+candidate_id 覆盖，重跑不产生重复行）。
// English: inserts or updates a backtest job (same kind+candidate_id overwrites, so reruns never duplicate).
func (d *DB) UpsertBacktestJob(j *BacktestJob) error {
	if j.StartedAt == "" {
		j.StartedAt = time.Now().Format("2006-01-02 15:04:05")
	}
	j.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")
	if j.Status == "done" || j.Status == "error" || j.Status == "interrupted" {
		j.FinishedAt = j.UpdatedAt
	}
	_, err := d.db.Exec(`INSERT INTO backtest_jobs
		(kind, candidate_id, status, progress, avg_excess, error, result_text, started_at, finished_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(kind, candidate_id) DO UPDATE SET
			status=excluded.status, progress=excluded.progress, avg_excess=excluded.avg_excess,
			error=excluded.error, result_text=excluded.result_text,
			started_at=excluded.started_at, finished_at=excluded.finished_at,
			updated_at=excluded.updated_at`,
		j.Kind, j.CandidateID, j.Status, j.Progress, j.AvgExcess, j.Error, j.ResultText,
		j.StartedAt, j.FinishedAt, j.UpdatedAt)
	return err
}

// GetBacktestJob 按 kind+candidate_id 取一条任务；不存在返回 (nil, nil)。
// English: fetches a job by kind+candidate_id; returns (nil, nil) when absent.
func (d *DB) GetBacktestJob(kind string, candidateID int64) (*BacktestJob, error) {
	row := d.db.QueryRow(`SELECT id, kind, candidate_id, status,
		COALESCE(progress,''), COALESCE(avg_excess,0), COALESCE(error,''), COALESCE(result_text,''),
		COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE(updated_at,'')
		FROM backtest_jobs WHERE kind=? AND candidate_id=?`, kind, candidateID)
	j, err := scanBacktestJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// RunningBacktestJobs 返回所有 status='running' 的任务（前端刷新后恢复轮询、启动时标 interrupted 用）。
// English: returns every status='running' job (used to resume frontend polling after a refresh and to
// mark leftover runs interrupted on startup).
func (d *DB) RunningBacktestJobs() ([]BacktestJob, error) {
	return d.listBacktestJobs(`WHERE status='running'`)
}

// ListBacktestJobs 返回全部回测任务（最新在前），供回测 tab 的进度查看列表。
// English: returns all backtest jobs, newest first, for the backtest tab's progress list.
func (d *DB) ListBacktestJobs() ([]BacktestJob, error) {
	return d.listBacktestJobs("")
}

func (d *DB) listBacktestJobs(where string) ([]BacktestJob, error) {
	rows, err := d.db.Query(`SELECT id, kind, candidate_id, status,
		COALESCE(progress,''), COALESCE(avg_excess,0), COALESCE(error,''), COALESCE(result_text,''),
		COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE(updated_at,'')
		FROM backtest_jobs ` + where + ` ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BacktestJob
	for rows.Next() {
		j, err := scanBacktestJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// scanBacktestJob 从一行扫描出 BacktestJob（QueryRow 与 Rows 共用）。
// English: scans one row into BacktestJob (shared by QueryRow and Rows paths).
func scanBacktestJob(s rowScanner) (*BacktestJob, error) {
	var j BacktestJob
	if err := s.Scan(&j.ID, &j.Kind, &j.CandidateID, &j.Status,
		&j.Progress, &j.AvgExcess, &j.Error, &j.ResultText,
		&j.StartedAt, &j.FinishedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// rowScanner 抽象 sql.Row / sql.Rows 的 Scan（避免重复实现）。
// English: abstracts Scan over sql.Row / sql.Rows to avoid duplication.
type rowScanner interface {
	Scan(dest ...any) error
}

// MarkRunningInterrupted 把全部残留 running 任务标记为 interrupted（quant 启动恢复：上次崩溃遗留）。
// 返回被标记的任务数。
// English: marks every leftover running job as interrupted (startup recovery for crashed runs).
// Returns how many jobs were flagged.
func (d *DB) MarkRunningInterrupted() (int, error) {
	res, err := d.db.Exec(`UPDATE backtest_jobs SET status='interrupted',
		error=COALESCE(NULLIF(error,''),'服务重启，任务中断（可重新发起续跑）'),
		finished_at=COALESCE(finished_at, datetime('now','localtime')),
		updated_at=datetime('now','localtime')
		WHERE status='running'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetBacktestEventResult 读取某候选某事件的断点缓存（result_json）；无缓存返回 ("", false, nil)。
// English: reads a candidate's event-result checkpoint; returns ("", false, nil) when uncached.
func (d *DB) GetBacktestEventResult(candidateID int64, date, industry string) (string, bool, error) {
	var js string
	err := d.db.QueryRow(`SELECT result_json FROM backtest_event_results
		WHERE candidate_id=? AND event_date=? AND industry=?`, candidateID, date, industry).Scan(&js)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return js, true, nil
}

// UpsertBacktestEventResult 写入某候选某事件的断点缓存（完整 EventResult JSON，重跑覆盖）。
// English: upserts a candidate's per-event checkpoint (full EventResult JSON; reruns overwrite).
func (d *DB) UpsertBacktestEventResult(candidateID int64, date, industry, resultJSON string) error {
	_, err := d.db.Exec(`INSERT INTO backtest_event_results (candidate_id, event_date, industry, result_json)
		VALUES (?,?,?,?)
		ON CONFLICT(candidate_id, event_date, industry) DO UPDATE SET result_json=excluded.result_json`,
		candidateID, date, industry, resultJSON)
	return err
}

// CountBacktestEventResults 统计某候选已缓存的断点事件数（诊断/进度参照）。
// English: counts a candidate's cached checkpoint events (diagnostics / progress reference).
func (d *DB) CountBacktestEventResults(candidateID int64) (int, error) {
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM backtest_event_results WHERE candidate_id=?`, candidateID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
