// 回测任务中心持久化（B5 研究闭环）：backtest_jobs 任务状态 + backtest_event_results 断点缓存。
// 覆盖两类回测——kind='candidate'（前端单候选全量回测）与 kind='nightly'（夜间调度器全量回测），
// 均落库，quant/research 进程重启后任务可查、可恢复、可续跑。
// §ADJ-BASIS（2026-09-23）：断点缓存的键含**复权口径位** adj_basis，取值由调用方传入
// （internal/research.AdjBaselineVersion——本包被 research 依赖，无法自己 import 回来），
// 空串是"改前旧证据行"的哨兵值，读写侧一律排除。
// English: backtest task-center persistence for the B5 research loop — backtest_jobs task state plus
// backtest_event_results checkpoint cache. Covers both per-candidate ('candidate') and nightly
// ('nightly') backtests; rows are persisted so jobs stay queryable/recoverable/resumable across restarts.
// The checkpoint key carries the adjustment-price basis (adj_basis), passed in by the caller because
// internal/research imports this package; the empty string is the sentinel for pre-basis rows.
package store

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

// BacktestJob 一条回测任务（持久化到 backtest_jobs）。
// English: one backtest job (persisted in backtest_jobs).
type BacktestJob struct {
	ID          int64   `json:"id"`           // 任务自增 ID
	Kind        string  `json:"kind"`         // candidate=单候选 / nightly=夜间全量 / library=战法库规则
	CandidateID int64   `json:"candidate_id"` // kind=candidate/library 时对应候选或规则 ID（nightly=0）
	Status      string  `json:"status"`       // running / paused / done / error / interrupted（运行中/暂停/完成/错误/中断）
	Progress    string  `json:"progress"`     // "45%"（进度百分比）
	AvgExcess   float64 `json:"avg_excess"`   // 回测超额（h 日前瞻，done 后回填）
	Error       string  `json:"error"`        // 失败原因（error 时）
	// ResultText 战法库回测的汇总报告文本（阶段3.4：胜率/盈亏比等，done 后回填，前端直接展示）。
	// English: the library-backtest summary report text (win rate / profit factor…, backfilled on done).
	ResultText string `json:"result_text,omitempty"` // 结果文本
	// StrategyKind 回放子类型（factor/pattern/内置战法名），供前端失败重跑重建规则 ID。
	StrategyKind string `json:"strategy_kind,omitempty"` // 战法Kind
	// ParamsJSON 入队运行参数原文（start/end/top_k/min_stocks/maxstocks 等），
	// 回测行展示"具体参数"用。English: raw enqueue params for the job's parameter display.
	ParamsJSON string `json:"params_json,omitempty"` // 参数JSON
	StartedAt  string `json:"started_at"`            // 开始时间 YYYY-MM-DD HH:MM:SS
	FinishedAt string `json:"finished_at"`           // 结束时间（done/error/interrupted 时）
	UpdatedAt  string `json:"updated_at"`            // 最近更新时间
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

// listBacktestJobs 按可选 WHERE 条件查询回测任务（按更新时间倒序、再按 ID 倒序）。
// 供 ListBacktestJobs 与按条件筛选的调用方复用。
// English: queries backtest jobs with an optional WHERE clause, newest-first by updated_at then id.
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
// §RFIX-4 复活：本函数此前是死代码（research_tasks 启动接管只覆盖任务队列表，
// backtest_jobs 僵尸行无人回收，生产 id=3 running 挂死近一月）；现由 researchd 与
// quant 双启动路径调用（同一 trading.db，幂等）。finished_at 用 NULLIF 容空串——
// 本表以 ”（非 NULL）落库空值，旧 COALESCE 永远不回填。
// English: marks every leftover running job as interrupted (startup recovery for crashed runs).
// Returns how many jobs were flagged.
func (d *DB) MarkRunningInterrupted() (int, error) {
	res, err := d.db.Exec(`UPDATE backtest_jobs SET status='interrupted',
		error=COALESCE(NULLIF(error,''),'服务重启，任务中断（可重新发起续跑）'),
		finished_at=COALESCE(NULLIF(finished_at,''), datetime('now','localtime')),
		updated_at=datetime('now','localtime')
		WHERE status='running'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetBacktestEventResult 读取某候选某事件的断点缓存（result_json）；无缓存返回 ("", false, nil)。
// ruleFP §GAP 二.3#5 规则参数指纹：键组成部分——改参后旧缓存自动失效（不再命中）。
// adjBasis §ADJ-BASIS（2026-09-23）复权口径位：键组成部分，取值只能是调用方传入的
// internal/research.AdjBaselineVersion（本包 import 不到 research，字符串单点定义在那边）。
// 两条保守回退（都是「宁重算，不拿旧口径行冒充新结果」）：
//   - adjBasis == ""：口径位未装配 → 恒判未命中（空串是改前旧证据行的哨兵值，绝不当当前口径）；
//   - 口径位主键迁移被守卫中止（EventBasisDegraded）：表里根本没有可用的 adj_basis → 同样恒判未命中。
//
// English: reads a candidate's per-event checkpoint; the rule fingerprint and the adjustment basis
// are both part of the key, so a parameter change or a price-basis change invalidates stale cache.
// An empty basis or a degraded (un-rebuilt) table always misses instead of surfacing pre-fix rows.
func (d *DB) GetBacktestEventResult(candidateID int64, date, industry, ruleFP, adjBasis string) (string, bool, error) {
	if adjBasis == "" || d.EventBasisDegraded() {
		d.warnBasisMissOnce(adjBasis)
		return "", false, nil
	}
	var js string
	err := d.db.QueryRow(`SELECT result_json FROM backtest_event_results
		WHERE candidate_id=? AND event_date=? AND industry=? AND adj_basis=? AND COALESCE(rule_fp,'')=?`,
		candidateID, date, industry, adjBasis, ruleFP).Scan(&js)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return js, true, nil
}

// UpsertBacktestEventResult 写入某候选某事件的断点缓存（完整 EventResult JSON，重跑覆盖）。
// 携带规则指纹：同键不同指纹直接覆盖 result_json 与指纹（旧行不残留混用）。
// adjBasis 复权口径位（四列主键的最后一列，见 GetBacktestEventResult 的说明）：
// 空串一律拒绝——它正是「改前旧证据行」的哨兵，写进去就等于把新结果伪装成旧口径行、
// 并与历史证据混在同一个键上。口径位迁移中止（降级）时同样拒绝写入：此时表结构承载不了
// 口径信息，写下去就是继续往旧键里混数据。两者都让调用方**重算**，缓存只是不再命中。
//
// English: upserts a per-event checkpoint keyed by (candidate, date, industry, basis) with its rule
// fingerprint. An empty basis, or a table whose basis-PK rebuild was aborted, is refused outright so
// callers fall back to recomputation rather than mixing new results into pre-basis evidence rows.
func (d *DB) UpsertBacktestEventResult(candidateID int64, date, industry, ruleFP, adjBasis, resultJSON string) error {
	if adjBasis == "" {
		return fmt.Errorf("store: backtest_event_results 拒绝写入——复权口径位未装配（adj_basis 空串是改前旧证据行的哨兵值，"+
			"须由调用方传入 research.AdjBaselineVersion）cand=%d %s/%s", candidateID, date, industry)
	}
	if d.EventBasisDegraded() {
		d.warnBasisWriteOnce(adjBasis)
		return fmt.Errorf("store: backtest_event_results 复权口径位主键未生效（%s），拒绝写缓存：宁可整轮重算，不把新结果混进旧键",
			d.eventBasisReason)
	}
	_, err := d.db.Exec(`INSERT INTO backtest_event_results (candidate_id, event_date, industry, adj_basis, rule_fp, result_json)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(candidate_id, event_date, industry, adj_basis) DO UPDATE SET
			rule_fp=excluded.rule_fp, result_json=excluded.result_json`,
		candidateID, date, industry, adjBasis, ruleFP, resultJSON)
	return err
}

// CountBacktestEventResults 统计某候选已缓存的断点事件数（诊断/进度参照）。
// 只数当前口径的行：空串 adj_basis（改前旧证据行）与别的口径行都不算进"这个候选已经跑了多少事件"。
// 口径位未装配或降级时返回 0（无一条行可被认定为当前结果），调用方据此判"没有缓存可复用"。
// English: counts a candidate's cached events **on the current basis only**; legacy empty-basis rows and other
// bases never count, and an unset basis or degraded table yields 0.
func (d *DB) CountBacktestEventResults(candidateID int64, adjBasis string) (int, error) {
	if adjBasis == "" || d.EventBasisDegraded() {
		return 0, nil
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM backtest_event_results WHERE candidate_id=? AND adj_basis=?`,
		candidateID, adjBasis).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// warnBasisMissOnce / warnBasisWriteOnce 保守回退路径的一次性告警（逐事件打会刷屏）。
func (d *DB) warnBasisMissOnce(adjBasis string) {
	d.eventBasisWarnRead.Do(func() {
		log.Printf("[store] WARN §ADJ-BASIS backtest_event_results 读侧按保守模式恒判未命中（adj_basis=%q degraded=%v）："+
			"本轮全部事件重算，绝不复用改前旧行", adjBasis, d.eventBasisAborted)
	})
}

func (d *DB) warnBasisWriteOnce(adjBasis string) {
	d.eventBasisWarnWrite.Do(func() {
		log.Printf("[store] WARN §ADJ-BASIS backtest_event_results 写侧拒绝缓存（adj_basis=%q degraded=%v）：%s",
			adjBasis, d.eventBasisAborted, d.eventBasisReason)
	})
}
