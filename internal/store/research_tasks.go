// 研究任务队列（子系统统一改造一期）：research_tasks 表 CRUD + backtest_jobs 一次性迁移。
// 设计要点见 docs/RESEARCH_TASK_QUEUE_PLAN.md：
//   - quant(API) 与 researchd 夜间链只入队；唯一消费者 researchd worker；
//   - 优先级 high（手动）先于 low（夜间）；同级按 chain_day/chain_seq/id FIFO；
//   - preempted（系统抢占/重启遗留，自动回队首续跑）与 cancelled（用户取消，终态）分离；
//   - control 列是 API→worker 的控制通道（pause/resume/cancel），worker 消费后清空。
//
// English: research task queue (unified-subsystem phase 1) — research_tasks CRUD plus the one-shot
// backtest_jobs migration. quant(API) and the researchd nightly chain only enqueue; the sole consumer
// is the researchd worker. 'high' (manual) outranks 'low' (nightly); peers order FIFO by
// chain_day/chain_seq/id. 'preempted' (system kill, auto-requeued at class head) is distinct from
// 'cancelled' (user intent, terminal). The control column is the API→worker channel.
package store

import (
	"database/sql"
	"fmt"
	"time"
)

// 研究任务类型（type 列取值）。
const (
	TaskDiscoverFactors   = "discover_factors"
	TaskDiscoverPatterns  = "discover_patterns"
	TaskSectorRebuild     = "sector_rebuild"
	TaskPaperResearch     = "paper_research"
	TaskBacktestCandidate = "backtest_candidate"
	TaskBacktestStrategy  = "backtest_strategy" // 战法库规则回放（二期并入 research 二进制）
	TaskBacktestNightly   = "backtest_nightly"  // 夜间全量回测（ref_id=0 取最近候选）
	TaskList              = "list"
	TaskDataload          = "dataload"
)

// 任务状态（status 列取值）。
const (
	TaskQueued    = "queued"
	TaskRunning   = "running"
	TaskPaused    = "paused"
	TaskDone      = "done"
	TaskError     = "error"
	TaskCancelled = "cancelled" // 用户取消：终态，不自动重跑
	TaskPreempted = "preempted" // 系统抢占/重启遗留：自动回队首续跑
	// TaskFailedRetry §失败重排队：worker 内部伪状态——落库前立即经 RequeueFailedTask
	// 转为 queued（队尾），不持久化该字面值。error 列保留失败原因。
	TaskFailedRetry = "failed_retry"
)

// 控制标志（control 列取值；worker 消费后清空）。
const (
	ControlPause  = "pause"
	ControlResume = "resume"
	ControlCancel = "cancel"
)

// ResearchTask 队列中的一条研究任务。
// English: one research task in the queue.
type ResearchTask struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`                  // 类型
	RefID      int64   `json:"ref_id"`                // 关联ID
	Priority   string  `json:"priority"`              // high | low
	Status     string  `json:"status"`                // 状态
	Progress   string  `json:"progress"`              // 进度
	ResultNum  float64 `json:"result_num"`            // 结果数值
	ResultText string  `json:"result_text,omitempty"` // 结果文本
	Error      string  `json:"error,omitempty"`       // 错误信息
	Payload    string  `json:"payload"`               // JSON 运行参数（start/end/h/top_k/min_stocks/kind/maxstocks…）
	ChainDay   string  `json:"chain_day,omitempty"`   // 链日期
	ChainSeq   int     `json:"chain_seq"`             // 链序号
	Control    string  `json:"control,omitempty"`     // 控制字段
	// RetryCount §P1-11 失败重试累计次数：达到上限（retryCap）后任务转终态 error，不再自动重入队，
	// 避免拉取型任务（如 dataload）无限重试打满队列/带宽。
	// English: P1-11 cumulative failure retries; once it reaches the cap the task becomes terminal error.
	RetryCount int `json:"retry_count"`
	// RequeueSeq §失败重排队尾键：失败重入队时取全局 max+1，出队按它 ASC 沉底
	// （秒级 updated_at 在同秒内无法区分先后，专用单调序列才可靠）。
	RequeueSeq int64  `json:"requeue_seq,omitempty"` // 重入队序号
	CreatedAt  string `json:"created_at"`            // 创建时间
	StartedAt  string `json:"started_at,omitempty"`  // 开始时间
	FinishedAt string `json:"finished_at,omitempty"` // 完成时间
	UpdatedAt  string `json:"updated_at"`            // 更新时间
}

// nowStr 当前本地时间串（统一格式，与既有表一致）。
func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }

// EnqueueResearchTask 入队一条任务并返回自增 ID。
// English: enqueues a task and returns its auto-increment ID.
func (d *DB) EnqueueResearchTask(t *ResearchTask) (int64, error) {
	if t.Priority == "" {
		t.Priority = "low"
	}
	if t.Status == "" {
		t.Status = TaskQueued
	}
	now := nowStr()
	res, err := d.db.Exec(`INSERT INTO research_tasks
		(type, ref_id, priority, status, progress, result_num, result_text, error,
		 payload, chain_day, chain_seq, control, retry_count, created_at, started_at, finished_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Type, t.RefID, t.Priority, t.Status, t.Progress, t.ResultNum, t.ResultText, t.Error,
		t.Payload, t.ChainDay, t.ChainSeq, t.Control, t.RetryCount, now, t.StartedAt, t.FinishedAt, now)
	if err != nil {
		return 0, fmt.Errorf("enqueue research task: %w", err)
	}
	return res.LastInsertId()
}

// DequeueHighestTask 取下一个应执行的任务（不出队，仅查询）：high 先于 low；
// 同级内 preempted（自动续跑）排最前，其余按 chain_day → chain_seq → requeue_seq → id FIFO。
// §失败重排队：requeue_seq 单调递增作为尾键——失败重新入队取全局 max+1 沉到同类末尾，
// 不设重试上限；其他任务先行消化，空队时按冷却间隔慢速重试。（秒级时间串同秒并列不可靠，
// 故用专用单调列。）无新任务返回 (nil, nil)。
// English: peeks the next runnable task — high before low; preempted first; then FIFO with the
// monotonic requeue_seq as the tail key so failed-and-requeued tasks sink to the back (no retry cap).
func (d *DB) DequeueHighestTask() (*ResearchTask, error) {
	row := d.db.QueryRow(`SELECT ` + researchTaskCols + ` FROM research_tasks
		WHERE status IN ('` + TaskQueued + `','` + TaskPreempted + `')
		ORDER BY CASE priority WHEN 'high' THEN 0 ELSE 1 END,
			CASE status WHEN '` + TaskPreempted + `' THEN 0 ELSE 1 END,
			chain_day ASC, chain_seq ASC, requeue_seq ASC, id ASC LIMIT 1`)
	t, err := scanResearchTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ClaimResearchTask 把任务置为 running（worker 出队认领；CAS 语义防双消费）。
// 返回 false 表示任务已被并发改态（理论上单 worker 不会发生，防御用）。
// English: flips a task to running as the worker claims it (CAS guard against double-consume).
func (d *DB) ClaimResearchTask(id int64) (bool, error) {
	res, err := d.db.Exec(`UPDATE research_tasks SET status='running', started_at=?,
		updated_at=? WHERE id=? AND status IN ('queued','preempted')`,
		nowStr(), nowStr(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateTaskClaimed 认领启动标记：status=running、progress 预写 1%（§8.6-A 装配期不再空窗），
// 刻意不触碰 error——续跑场景下保留上次中断原因，直到本次运行产出新终态。
// English: claim marker — flips to running with a 1% baseline; deliberately leaves error untouched
// so a resumed task keeps its previous interruption reason until a new terminal state lands.
func (d *DB) UpdateTaskClaimed(id int64) error {
	_, err := d.db.Exec(`UPDATE research_tasks SET status='running', progress='1%', updated_at=?
		WHERE id=?`, nowStr(), id)
	return err
}

// GetResearchTask 按 ID 取一条。
func (d *DB) GetResearchTask(id int64) (*ResearchTask, error) {
	row := d.db.QueryRow(`SELECT `+researchTaskCols+` FROM research_tasks WHERE id=?`, id)
	t, err := scanResearchTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// LatestTaskByRef 取某类型某引用的最新任务行（前端按候选ID/规则序号轮询用）。
// English: newest task row for (type, ref) — what the frontend polls by candidate/rule number.
func (d *DB) LatestTaskByRef(taskType string, refID int64) (*ResearchTask, error) {
	row := d.db.QueryRow(`SELECT `+researchTaskCols+` FROM research_tasks
		WHERE type=? AND ref_id=? ORDER BY id DESC LIMIT 1`, taskType, refID)
	t, err := scanResearchTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// HasActiveTaskByRef 某 ref 是否已有 queued/running/paused 任务（入队幂等去重用）。
func (d *DB) HasActiveTaskByRef(taskType string, refID int64) (bool, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM research_tasks
		WHERE type=? AND ref_id=? AND status IN ('queued','running','paused')`,
		taskType, refID).Scan(&n)
	return n > 0, err
}

// UpdateTaskRunState 回写任务运行态（progress/result/error；终态补 finished_at）。
// English: writes back run state (progress/result/error; finished_at filled on terminal states).
func (d *DB) UpdateTaskRunState(id int64, status, progress string, resultNum float64, resultText, errMsg string) error {
	now := nowStr()
	fin := ""
	switch status {
	case TaskDone, TaskError, TaskCancelled:
		fin = now
	}
	_, err := d.db.Exec(`UPDATE research_tasks SET status=?, progress=?, result_num=?,
		result_text=?, error=?, finished_at=CASE WHEN ?<>'' THEN ? ELSE finished_at END,
		updated_at=? WHERE id=?`,
		status, progress, resultNum, resultText, errMsg, fin, fin, now, id)
	return err
}

// SetTaskControl 写控制标志（API 调用；仅对未终结任务生效）。
func (d *DB) SetTaskControl(id int64, control string) error {
	_, err := d.db.Exec(`UPDATE research_tasks SET control=?, updated_at=?
		WHERE id=? AND status IN ('running','paused','queued')`, control, nowStr(), id)
	return err
}

// ConsumeTaskControl 取走并清空控制标志（worker 每 ~2s 轮询；返回 "" 表示无指令）。
// English: reads and clears the control flag (polled by the worker every ~2s; "" means none).
func (d *DB) ConsumeTaskControl(id int64) (string, error) {
	var c string
	err := d.db.QueryRow(`SELECT COALESCE(control,'') FROM research_tasks WHERE id=?`, id).Scan(&c)
	if err != nil || c == "" {
		return "", err
	}
	_, err = d.db.Exec(`UPDATE research_tasks SET control='' WHERE id=? AND control=?`, id, c)
	if err != nil {
		return "", err
	}
	return c, nil
}

// RequeueTask 把 preempted/paused 任务放回 queued（worker 抢占后续跑入口）。
// English: puts a preempted/paused task back to queued (resume entry after preemption).
func (d *DB) RequeueTask(id int64) error {
	_, err := d.db.Exec(`UPDATE research_tasks SET status='queued', updated_at=?
		WHERE id=? AND status IN ('preempted','paused')`, nowStr(), id)
	return err
}

// researchTaskRetryCap §P1-11 失败重试上限：达到后任务转终态 error，不再自动重入队，
// 避免拉取型任务（如 dataload）无限重试打满队列/带宽。
// English: P1-11 max retries — beyond it the task becomes terminal error instead of re-enqueuing.
const researchTaskRetryCap = 5

// RequeueFailedTask §P1-11 失败任务自动重新入队——排队尾（出队排序以 updated_at 为
// 尾键，刷新即沉底）；重试计数 +1，达到上限（researchTaskRetryCap）后任务转终态 error，
// 不再自动重入队（error 列保留最后一次失败原因供前端展示）；progress/finished_at 清空等待下次运行。
// English: re-enqueues a failed task at the queue tail (updated_at is the tail key in the dequeue
// ordering; refreshed on requeue). The retry counter increments; once it reaches the cap the task
// becomes terminal error instead of re-enqueuing. No infinite retry for fetch-type tasks.
func (d *DB) RequeueFailedTask(id int64, errMsg string) error {
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	var cur int
	if err := d.db.QueryRow(`SELECT COALESCE(retry_count,0) FROM research_tasks WHERE id=?`, id).Scan(&cur); err != nil {
		return err
	}
	cur++
	// §P1-11 达到重试上限：转终态 error，停止自动重入队（拉取型任务防无限重试）。
	if cur >= researchTaskRetryCap {
		_, err := d.db.Exec(`UPDATE research_tasks SET status='error', retry_count=?, progress='',
			error=?, finished_at=?, updated_at=? WHERE id=?`,
			cur, "重试达到上限("+itoa(researchTaskRetryCap)+")："+errMsg, nowStr(), nowStr(), id)
		return err
	}
	_, err := d.db.Exec(`UPDATE research_tasks SET status='queued', retry_count=?, progress='', error=?,
		finished_at='', updated_at=?,
		requeue_seq=(SELECT COALESCE(MAX(requeue_seq),0)+1 FROM research_tasks)
		WHERE id=?`, cur, errMsg, nowStr(), id)
	return err
}

// itoa 小工具：避免为单一常量引入 strconv（重试上限文案用）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// CancelChainTasks 把某夜间链的剩余 queued 任务全部置 cancelled（AbortOnError 用）。
// English: cancels every queued sibling of a nightly chain (AbortOnError semantics).
func (d *DB) CancelChainTasks(chainDay string) (int64, error) {
	res, err := d.db.Exec(`UPDATE research_tasks SET status='cancelled',
		error='同链前置步骤失败，作业中止', finished_at=?, updated_at=?
		WHERE chain_day=? AND status='queued'`, nowStr(), nowStr(), chainDay)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ChainHasTasks 某运行日是否已有链任务（夜间入队幂等判断）。
func (d *DB) ChainHasTasks(chainDay string) (bool, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM research_tasks WHERE chain_day=?`, chainDay).Scan(&n)
	return n > 0, err
}

// ListResearchTasks 全部任务最新在前（前端「回测」tab 列表）。
func (d *DB) ListResearchTasks() ([]ResearchTask, error) {
	return d.listResearchTasks("")
}

// ActiveResearchTasks 所有 queued/running/paused/preempted 任务。
func (d *DB) ActiveResearchTasks() ([]ResearchTask, error) {
	return d.listResearchTasks(`WHERE status IN ('queued','running','paused','preempted')`)
}

// listResearchTasks 列表查询共用体：ListResearchTasks（全部）与 ActiveResearchTasks（未终结）
// 仅 where 子句不同，扫描逻辑共享。
func (d *DB) listResearchTasks(where string) ([]ResearchTask, error) {
	rows, err := d.db.Query(`SELECT ` + researchTaskCols + ` FROM research_tasks ` + where +
		` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResearchTask
	for rows.Next() {
		t, err := scanResearchTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ResetStaleRunningTasks 服务启动恢复：把上次进程崩溃遗留的 running/paused 置为 preempted
// （下次盘后自动回队续跑）。返回处理行数。取代旧 MarkRunningInterrupted 的职责。
// English: startup recovery — leftover running/paused rows from a crashed process become preempted
// (auto-requeued after hours). Returns affected rows; replaces the old MarkRunningInterrupted role.
func (d *DB) ResetStaleRunningTasks() (int64, error) {
	res, err := d.db.Exec(`UPDATE research_tasks SET status='preempted', updated_at=?
		WHERE status IN ('running','paused')`, nowStr())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// researchTaskCols 列清单（scan 共用，顺序与结构体字段一致）。
const researchTaskCols = `id, type, ref_id, priority, status, progress,
		COALESCE(result_num,0), COALESCE(result_text,''), COALESCE(error,''), payload,
		COALESCE(chain_day,''), COALESCE(chain_seq,0), COALESCE(control,''),
		COALESCE(requeue_seq,0), COALESCE(retry_count,0),
		created_at, COALESCE(started_at,''), COALESCE(finished_at,''), updated_at`

// scanResearchTask 从一行扫描出 ResearchTask（QueryRow 与 Rows 共用）。
func scanResearchTask(s rowScanner) (*ResearchTask, error) {
	var t ResearchTask
	if err := s.Scan(&t.ID, &t.Type, &t.RefID, &t.Priority, &t.Status, &t.Progress,
		&t.ResultNum, &t.ResultText, &t.Error, &t.Payload,
		&t.ChainDay, &t.ChainSeq, &t.Control, &t.RequeueSeq, &t.RetryCount,
		&t.CreatedAt, &t.StartedAt, &t.FinishedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// migrateBacktestJobsToTasks 一次性迁移 backtest_jobs → research_tasks（§9 映射规则）：
//   - kind candidate→backtest_candidate / library→backtest_strategy / nightly→backtest_nightly；
//   - 终态行原样平移（interrupted→cancelled）；非终态 candidate/nightly → preempted（下次盘后
//     自动断点续跑）；library 缺规则类型无法重跑 → cancelled 并注明原因；
//   - priority 一律 low；payload 按最小可跑参数重建。
//
// 仅当 research_tasks 为空且旧表有数据时执行（幂等：队列一旦有真实写入绝不回填）。
// English: one-shot legacy migration with the §9 mapping; terminal rows copy through
// (interrupted→cancelled), non-terminal candidate/nightly become preempted (auto-resume), library
// rows lacking rule-kind become cancelled. Runs only when the queue is empty and legacy data exists.
func (d *DB) migrateBacktestJobsToTasks() error {
	var nTasks, nJobs int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM research_tasks`).Scan(&nTasks); err != nil {
		return err
	}
	if nTasks > 0 {
		return nil // 队列已有真实数据：绝不回填（queue already live: never backfill）
	}
	hasJob, err := hasTable(d.db, "backtest_jobs")
	if err != nil || !hasJob {
		return err
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM backtest_jobs`).Scan(&nJobs); err != nil || nJobs == 0 {
		return err
	}
	rows, err := d.db.Query(`SELECT kind, candidate_id, status, COALESCE(progress,''),
		COALESCE(avg_excess,0), COALESCE(error,''), COALESCE(result_text,''),
		started_at, COALESCE(finished_at,'') FROM backtest_jobs ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	now := nowStr()
	for rows.Next() {
		var kind, status, progress, errMsg, resultText, startedAt, finishedAt string
		var candID int64
		var avgExcess float64
		if err := rows.Scan(&kind, &candID, &status, &progress, &avgExcess, &errMsg,
			&resultText, &startedAt, &finishedAt); err != nil {
			return err
		}
		taskType, payload := mapLegacyKind(kind)
		newStatus := status
		switch status {
		case "running", "paused":
			if taskType == TaskBacktestStrategy {
				// 旧行缺 fac_/pat_ 规则类型，无法安全重跑（legacy row lacks rule kind: not rerunnable）
				newStatus, errMsg = TaskCancelled, "迁移自旧任务表：缺少战法库规则类型，请重新发起回测"
			} else {
				newStatus = TaskPreempted
				if errMsg == "" {
					errMsg = "迁移自旧任务表：服务切换时中断，将自动断点续跑"
				}
			}
		case "interrupted":
			newStatus = TaskCancelled
		}
		if _, err := d.db.Exec(`INSERT INTO research_tasks
			(type, ref_id, priority, status, progress, result_num, result_text, error,
			 payload, created_at, started_at, finished_at, updated_at)
			VALUES (?,?, 'low', ?,?,?,?,?, ?,?,?,?,?)`,
			taskType, candID, newStatus, progress, avgExcess, resultText, errMsg,
			payload, startedAt, startedAt, finishedAt, now); err != nil {
			return err
		}
	}
	return rows.Err()
}

// mapLegacyKind 旧 backtest_jobs.kind → 新任务 type + 最小 payload。
func mapLegacyKind(kind string) (string, string) {
	switch kind {
	case "candidate":
		return TaskBacktestCandidate, `{"h":5}`
	case "library":
		return TaskBacktestStrategy, `{}` // 规则类型缺失，重跑需重新发起（rule kind unknown）
	default: // "nightly" 与未知兜底
		return TaskBacktestNightly, `{"h":5}`
	}
}

// hasTable 判断表是否存在（迁移守卫用）。
func hasTable(db interface{ QueryRow(string, ...any) *sql.Row }, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0, err
}
