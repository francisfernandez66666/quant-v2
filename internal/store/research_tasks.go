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
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
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
	// TaskMinuteSync §MINUTE-K（2026-09-24）：分钟 K 日增装载（收盘后只补库里已有的票）。
	// 与 dataload 同走专用二进制，不入 research 分发器。
	TaskMinuteSync = "minute_sync"
	// TaskLifecycle §GAP-P1 20260915：策略生命周期评估（灰度晋升 + 实盘衰退自动降级）。
	TaskLifecycle = "lifecycle"
	// TaskEvoL1 §SIZING-EVO 周度 L1 快筛（候选→护栏→进灰度）；与自动研究共用闸门/轮流
	// （由 scheduler 单槽串行 + nightcap/memgate/drainAllowed 约束，详见 worker.ensureNightlyEnqueue）。
	TaskEvoL1 = "evo_l1"
	// TaskEvoL2 §SIZING-EVO 夜间 L2（灰度晋升/回拒 + 生效配置衰退降级）。
	TaskEvoL2 = "evo_l2"
	// TaskEvoR4 §R4 分布标定：读 evo 影子库真实横截面净流入率样本，做档位占比标定。
	TaskEvoR4 = "evo_r4"
	// TaskEvoR08 §R-08 因子值取证：扫描影子库确认 annotations 已落因子值（flow_ratio_5d/today）。
	TaskEvoR08 = "evo_r08"
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
	// TaskYielded §NIGHTCAP（2026-10-14）"这一夜让位"伪状态：单夜占用到上限、或连续两次
	// 没跑完后的顺延——**不是失败**（没报错、断点缓存有效），因此不进失败连败账、不进
	// 累计回队账，只写 defer_day 并保留 progress。与 TaskFailedRetry 同为不持久化的伪状态。
	// English: pseudo "yielded for the night" state — a planned hand-off, not a failure, so it
	// touches neither failure streak nor the requeue total; only defer_day is written.
	TaskYielded = "yielded"
	// TaskNeedsAttention §M14（2026-09-22）连败熔断挂起终态：连续 ConsecutiveFailLimit 次
	// 没跑成（§NIGHTCAP 2026-10-14 起不再比对失败指纹）后由 RequeueFailedTask 置入——
	// 不再回队、不再被出队，等 owner 人工排查后经 RequeueTask 复活（连败计数同时清零）。
	// English: M14 breaker terminal state — after ConsecutiveFailLimit consecutive non-completions
	// (fingerprint-agnostic since §NIGHTCAP) the task stops being requeued until a manual requeue
	// revives it and clears the streak.
	TaskNeedsAttention = "failed_needs_attention"
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
	ID         int64   `json:"id"`                    // 任务自增 ID
	Type       string  `json:"type"`                  // 类型
	RefID      int64   `json:"ref_id"`                // 关联 ID
	Priority   string  `json:"priority"`              // high | low（高/低优先级）
	Status     string  `json:"status"`                // 状态
	Progress   string  `json:"progress"`              // 进度
	ResultNum  float64 `json:"result_num"`            // 结果数值
	ResultText string  `json:"result_text,omitempty"` // 结果文本
	Error      string  `json:"error,omitempty"`       // 错误信息
	Payload    string  `json:"payload"`               // JSON 运行参数（start/end/h/top_k/min_stocks/kind/maxstocks…）
	ChainDay   string  `json:"chain_day,omitempty"`   // 链日期
	ChainSeq   int     `json:"chain_seq"`             // 链序号
	Control    string  `json:"control,omitempty"`     // 控制字段
	// RetryCount §P1-11 失败重试累计次数（跨重启保留，展示/审计口径）。
	// §M14（2026-09-22）：不再作为终止判据——终止改由「同因连败」fail_streak 熔断承担。
	// English: cumulative failure retries (display only since M14; the stop decision moved to the
	// same-cause streak).
	RetryCount int `json:"retry_count"`
	// FailFP/FailStreak §M14 同因连败熔断：fail_fp=最近一次失败指纹（error 前 200 字），
	// fail_streak=该指纹连败次数；换因从 1 重计，成功/人工 Requeue 清零。
	FailFP     string `json:"fail_fp,omitempty"`
	FailStreak int    `json:"fail_streak"`
	// RequeueSeq §失败重排队尾键：失败重入队时取全局 max+1，出队按它 ASC 沉底
	// （秒级 updated_at 在同秒内无法区分先后，专用单调序列才可靠）。
	RequeueSeq int64 `json:"requeue_seq,omitempty"` // 重入队序号
	// RequeueTotal §QUEUE-REQUEUE（2026-10-12）累计回队次数（失败回队 + 抢占续跑都计入，
	// 与失败原因无关）；达 TotalRequeueLimit 即挂起。人工 Requeue 清零。
	// English: cause-independent lifetime requeue count driving the total breaker.
	RequeueTotal int `json:"requeue_total"`
	// NightDay/NightUsedMin §NIGHTCAP（2026-10-14）单条任务的"本夜已占用分钟数"台账。
	// night_day＝记账所属的北京日历日（串到第二天凌晨时仍记在起跑那一夜），
	// night_used_min＝该夜已累计占用的分钟数。出队时用「上限 − 已用」算出这次还能跑多久，
	// 上限是**单条任务单夜的占用天花板**，不是总夜长——所以低优先级长任务再也吃不光整夜。
	// English: per-task per-night occupancy ledger (day key + minutes used that night), which
	// turns the nightly cap into the effective deadline for the next claim.
	NightDay     string `json:"night_day,omitempty"`
	NightUsedMin int    `json:"night_used_min"`
	// UnfinishedStreak §NIGHTCAP 连续"这一夜没跑完"的次数（超时/被抢占/预算让位都算，
	// 跑完一次即清零）。达 DeferralStreakLimit 次就顺延一夜——队首那一条不再占着唯一槽位，
	// 后面几十条链任务才有机会排水（现网实录：#416 每夜吃光夜窗却永不落终态，积压 54 条）。
	// English: consecutive nights this task failed to finish; at the limit it is deferred one
	// night so the queue head stops monopolising the single worker slot.
	UnfinishedStreak int `json:"unfinished_streak"`
	// DeferDay §NIGHTCAP 顺延目标日（北京紧凑日 YYYYMMDD）。非空且**大于今日**时该任务不出队
	// （本夜已让位），到点那夜自动恢复。人工 Requeue 清空。
	// English: deferral target day — rows whose defer_day is later than today are invisible to
	// the dequeue query until that night arrives.
	DeferDay   string `json:"defer_day,omitempty"`
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
	// 入队默认参数：优先级/状态未显式指定时落 low + queued
	if t.Priority == "" {
		t.Priority = "low"
	}
	if t.Status == "" {
		t.Status = TaskQueued
	}
	// created_at/updated_at 统一取当前时间，保证排序字段可用
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
// 异因失败不设重试上限（§NIGHTCAP 后由连败熔断兜底确定性失败）；其他任务先行消化，
// 空队时按冷却间隔慢速重试。（秒级时间串同秒并列不可靠，故用专用单调列。）
// §NIGHTCAP（2026-10-14）顺延闸：defer_day 非空且**晚于今日**的行整条不进候选。
// 为什么必须新加这把闸：上面那句"沉到同类末尾"对**链任务结构性无效**——出队序是
// chain_day → chain_seq → requeue_seq，链内序位排在沉底号之前，同一环回队后仍回到
// 原来的队首槽（现网 #416 连续六夜都是队首，就是这个原因）。让位改由 defer_day 承担：
// 该环本夜不再被认领，后面的链任务得以排水，到顺延那夜自动恢复。
// 无新任务返回 (nil, nil)。failed_needs_attention（§M14 挂起态）不在出队集合。
// English: peeks the next runnable task — high before low; preempted first; then chain FIFO.
// Rows deferred to a later night (defer_day > today) are excluded outright — the actual
// head-of-line fix, since a chain row's chain_seq keeps it in the same head slot after a requeue.
func (d *DB) DequeueHighestTask() (*ResearchTask, error) {
	// 只读预览队首（不出队）：high 先于 low，同级 preempted 续跑优先，
	// 再按 chain_day → chain_seq → requeue_seq → id FIFO；顺延未到点的行整条排除。
	row := d.db.QueryRow(`SELECT `+researchTaskCols+` FROM research_tasks
		WHERE status IN ('`+TaskQueued+`','`+TaskPreempted+`')
			AND (COALESCE(defer_day,'')='' OR defer_day <= ?)
		ORDER BY CASE priority WHEN 'high' THEN 0 ELSE 1 END,
			CASE status WHEN '`+TaskPreempted+`' THEN 0 ELSE 1 END,
			chain_day ASC, chain_seq ASC, requeue_seq ASC, id ASC LIMIT 1`,
		cntime.DayCompactOf(cntime.Now()))
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
	// §M14 成功/取消终结即打断「连败」链条：清零同因连败计数（fail_fp/fail_streak），
	// 避免陈旧连败与新周期的偶发同因失败拼接误触熔断。
	// §NIGHTCAP（2026-10-14）：成功/取消同时清零"连续没跑完"的顺延账（unfinished_streak）
	// 与顺延标记（defer_day）——跑完了就是顺延故事结束；夜账（night_day/night_used_min）
	// 刻意保留，它们按北京日取键、隔夜自动归零，且是审计读数。
	// §QUEUE-REQUEUE：这里**故意不清 requeue_total**——累计闸要防的是"同一个任务在队列里
	// 无限转圈"，而中间偶有一次成功恰恰是空烧的佐证（跑成了还继续重排 19 次），不是免罪；
	// 清账只留给人工 RequeueTask。夜间读数只数活跃行，已终结行的旧计数不会把判据染红。
	if status == TaskDone || status == TaskCancelled {
		if _, e := d.db.Exec(`UPDATE research_tasks SET fail_fp='', fail_streak=0,
			unfinished_streak=0, defer_day='' WHERE id=?`, id); e != nil {
			return e
		}
	}
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

// RequeueTask 把 preempted/paused 任务放回 queued（**人工**复活入口，owner 排查后调用）。
// §M14 同因连败熔断挂起的 failed_needs_attention 也经本入口人工复活：复活即清零连败计数
// （新一轮失败重新计数，不给挂起前旧账"续命"）。
// §QUEUE-REQUEUE（2026-10-12）：本入口同时清零 requeue_total——人工介入就是"这圈空转到此为止，
// 从本次重新起算"；不清零的话 owner 复活一个已经转过 20 圈的任务，它一跑就再次触闸挂起。
// §NIGHTCAP（2026-10-14）：顺延账一并清零（unfinished_streak/defer_day/night_used_min）——
// owner 排查过就是"让位故事到此为止"，复活后立刻可跑满整个单夜预算，不该带着旧让位账。
// 注意：worker 的抢占续跑**不走本函数**（改走 RequeueAfterPreemption），否则连败账会被重启抹掉。
// English: manual revive entry — clears the streak, the lifetime requeue total and the yield
// ledger (unfinished_streak/defer_day/night budget); the worker's preemption path no longer uses
// this (see RequeueAfterPreemption).
func (d *DB) RequeueTask(id int64) error {
	_, err := d.db.Exec(`UPDATE research_tasks SET status='queued', updated_at=?,
		fail_fp='', fail_streak=0, requeue_total=0,
		unfinished_streak=0, defer_day='', night_day='', night_used_min=0, finished_at=''
		WHERE id=? AND status IN ('preempted','paused','failed_needs_attention')`, nowStr(), id)
	return err
}

// ConsecutiveFailLimit §M14（2026-09-22）连败熔断阈值，§NIGHTCAP（2026-10-14）口径归一：
// **连续失败次数**达该值即挂起 failed_needs_attention——不再要求"同一原因"。
// 为什么把"同因"这一维去掉：现网 #416（三年窗因子发现）每夜先撞 8 小时单步超时、
// 次日 09:15 又被交易时段抢占，两条终态文案天然交替，指纹每轮都在变 ⇒ 连败计数被反复
// 洗回 1，阈值 10 永远追不上（库里实录：fail_streak=1 而 retry_count=6）。
// "换因即宽限"本意是保护瞬态抖动（网络断一下），但抖动族在 8 小时量级的任务上根本不是
// 瞬态——真能跑完就不会换因。所以判据改成看"连续几次没成"，原因只留在 error 列供人归因。
// 6 次的来由：夜间链每晚 8 环左右，同一环连败 6 次已跨过 2~3 个夜窗，再试下去只是空烧。
// English: consecutive-failure breaker — the same-fingerprint requirement is dropped because the
// production task alternated timeout/preemption texts forever, so the streak never reached the cap.
const ConsecutiveFailLimit = 6

// TotalRequeueLimit §QUEUE-REQUEUE（2026-10-12）累计回队上限：同一任务不管因为什么回队
// （同因失败、异因失败、抢占续跑），累计回队次数达该值即挂起 failed_needs_attention。
//
// 为什么同因熔断挡不住现网那一晚：10-11 实录里同一个任务被 8 小时单步超时送回队尾 33 次，
// 而 §M14 的连败计数有两条合法的"漏计"通道——
//   - 换因即从 1 重计：超时文案与「运行失败(exit status…)：最后一行输出」交替出现时，
//     指纹每轮都在变，streak 永远到不了 10（现网 33 次正是这个形态）；
//   - 抢占路径 RequeueTask 把 fail_streak 清零：worker 重启/让位一次，连败账就被抹一次。
//
// 取向＝**再加一条与原因无关的总量闸**，而不是给抢占单独开口子：真缺数据、真跑不完的任务，
// 无论失败文案怎么变，都不该在队列里转三十圈。
// §NIGHTCAP（2026-10-14）阈值 20 → 8：单夜占用上限落地后一条任务一夜最多被让位一次，
// 20 次意味着同一环要占满 20 个夜窗（约四周）才有人看见它——太晚了。8 的口径＝"允许它跨
// 8 个夜窗续跑（三年窗因子发现按 240 分钟/夜约需 3~4 夜，留一倍余量），再多就是真跑不完"。
// 失败回队与抢占续跑都计这条账；单夜预算用尽的**设计内让位不计**（见 YieldTaskToNight）。
// English: cause-independent lifetime requeue cap, lowered 20 → 8 once the nightly budget exists
// (a planned yield is not counted; repeated failures and preemptions are).
const TotalRequeueLimit = 8

// DeferralStreakLimit §NIGHTCAP（2026-10-14）顺延触发的"连续没跑完"次数：同一任务连续
// 两次没跑完（超时 / 被抢占 / 单夜预算用尽都算），第三次起就不再回到队首，而是顺延一夜
// （defer_day 一写，本夜剩余时间由后面的链任务接手）。
// 取 2 的理由：一次没跑完完全可能是夜窗太短或上游抖动，立刻顺延会把正常任务挤出去；
// 连着两次就是现网 #416 的形状——每夜吃光夜窗、永不落终态、后面 54 条一条都排不动。
// English: after this many consecutive non-completing attempts the task is deferred one night
// instead of returning to the head — this is the head-of-line fix.
const DeferralStreakLimit = 2

// MinNightStartMin §NIGHTCAP 起跑最小剩余预算（分钟）：本夜余额连这点都不够时不再认领该任务
// （直接顺延），免得跑三分钟就被预算切断、白烧一次装配成本（分钟级加载行情）。
// English: minimum remaining night budget worth starting for; below it the task yields at once.
const MinNightStartMin = 15

// NightCapDefaultMin §NIGHTCAP 单夜占用上限的内置兜底（分钟）：配置项
// rules.scheduler.per_task_night_max_min 未给出正值时使用。与 config 默认值同源，
// 两处必须一致（门禁 §117 有等值锁）。
// 240→360（§QRESUME 2026-10-17 owner 裁决）：断点续跑补齐后（权重优化阶段 trial 级缓存，
// 见 internal/research windowed.go），"到点让位"从"整阶段作废重来"变成"次夜从断点继续"，
// 长任务占满 6h 的代价从零产出降到有缓存收益；仍留一半夜窗给链上其余任务，不重蹈
// #416"8h 单步独占队首"覆辙。三年全池发现的权重优化满程约 194 试验（~8.7h），6h/夜
// +断点续跑两夜可完成。
// English: built-in fallback per-task nightly occupancy cap (minutes), same value as the config
// default — the gate keeps the two equal. 240→360 (§QRESUME 2026-10-17): with trial-level
// checkpoint resume in place, a 6h occupancy leaves cache instead of wasted work, while still
// leaving half the night for the rest of the chain.
const NightCapDefaultMin = 360

// requeueParkError 挂起终态的 error 列文案：把**触闸的那一条**写在最前面，
// 消费方（worker 告警、夜间读数、人工排查）都按这个前缀分成因，不需要再猜。
// 连败熔断在前、总量闸在后：连败说明"确定性失败已经很清楚"，比"转了很多圈"更可诊断。
// §NIGHTCAP 文案口径随判据一起改：旧文案写「同因连败」而判据已不看原因，运维照着那四个字
// 去找一个并不存在的共同原因（读数撒谎族）。
func requeueParkError(streak, total int, errMsg string) string {
	if streak >= ConsecutiveFailLimit {
		return "连败熔断(" + itoa(ConsecutiveFailLimit) + "连败，不看失败原因)：" + errMsg
	}
	return "累计回队熔断(" + itoa(total) + "次回队，原因每次不同未被连败闸捕获)：" + errMsg
}

// RequeueTripReasonOf 从 error 列文案回读触闸成因（worker 侧告警文案用，导出给调度层）。
// §NIGHTCAP 新前缀「连败熔断」判读为 consecutive_fail；同时**保留**旧前缀「同因连败熔断」的
// 判读（same_cause）——现网库里已有按旧文案挂起的行（发版前落的账），读它们时不能撒谎说
// "成因未知"。其余一律 "requeue"（不把"猜不出"写成某个具体成因）。
// English: maps the parked row's error text back to which breaker tripped; the legacy same-cause
// prefix still reads so pre-deploy parked rows aren't mislabelled.
func RequeueTripReasonOf(errText string) string {
	switch {
	case strings.HasPrefix(errText, "连败熔断"):
		return "consecutive_fail"
	case strings.HasPrefix(errText, "同因连败熔断"):
		return "same_cause" // 旧文案（发版前挂起的行）仍可判读
	case strings.HasPrefix(errText, "累计回队熔断"):
		return "total_requeue"
	default:
		return "requeue"
	}
}

// errorFingerprint §M14 失败原因指纹化：去首尾空白取前 200 字符（按 rune 截，
// 中文报错不得切断半个字）。同指纹≈同因。
func errorFingerprint(msg string) string {
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > 200 {
		return string(r[:200])
	}
	return msg
}

// RequeueFailedTask 失败任务自动重新入队——排队尾（requeue_seq 单调尾键，刷新即沉底）；
// retry_count 累计 +1（展示/审计口径）；progress/finished_at 清空等待下次运行；
// error 列保留最后一次失败原因。
// §M14 连败熔断：达 ConsecutiveFailLimit 次不再回队，置 failed_needs_attention 挂起终态，
// 等 owner 人工 Requeue 复活。
// §NIGHTCAP（2026-10-14）口径归一：连败计数**不再比对错误指纹**——超时与被抢占两条文案天然
// 交替时旧实现每轮都把 streak 洗回 1，现网同一任务因此空烧 6 轮而熔断读数仍是 1（见
// ConsecutiveFailLimit 注释）。fail_fp 列继续写，只作展示与归因用，不再参与判据。
// §QUEUE-REQUEUE（2026-10-12）另加与原因无关的累计闸：每次回队都记 requeue_total，
// 达 TotalRequeueLimit 即同挂 failed_needs_attention（error 前缀写清是哪一条闸触的）。
// English: re-enqueues a failed task at the queue tail. The streak now counts consecutive
// failures whatever text each one carried; the fingerprint column is display-only.
func (d *DB) RequeueFailedTask(id int64, errMsg string) error {
	// 按 rune 截而不是字节截：错误行是中文，errMsg[:500] 会把最后一个字劈成非法 UTF-8
	// 再落库（与 errorFingerprint 的截法保持同一口径）。
	if r := []rune(errMsg); len(r) > 500 {
		errMsg = string(r[:500])
	}
	fp := errorFingerprint(errMsg)
	var cur, streak, total int
	if err := d.db.QueryRow(`SELECT COALESCE(retry_count,0), COALESCE(fail_streak,0),
			COALESCE(requeue_total,0)
		FROM research_tasks WHERE id=?`, id).Scan(&cur, &streak, &total); err != nil {
		return err
	}
	cur++
	streak++ // §NIGHTCAP 连续没成即累加：换原因不再把账洗回 1（判据只看"连着几次没成"）
	total++  // §QUEUE-REQUEUE 累计回队：不看原因、不看是否同因，回队一次记一次
	// §M14 连败 / §QUEUE-REQUEUE 累计回队达阈值：挂起终态（不出队、不回队），
	// error 列留「哪条闸触的 + 最后一次原因」。
	if streak >= ConsecutiveFailLimit || total >= TotalRequeueLimit {
		_, err := d.db.Exec(`UPDATE research_tasks SET status='`+TaskNeedsAttention+`',
			retry_count=?, fail_fp=?, fail_streak=?, requeue_total=?, progress='',
			error=?, finished_at=?, updated_at=? WHERE id=?`,
			cur, fp, streak, total,
			requeueParkError(streak, total, errMsg), nowStr(), nowStr(), id)
		return err
	}
	_, err := d.db.Exec(`UPDATE research_tasks SET status='queued', retry_count=?,
		fail_fp=?, fail_streak=?, requeue_total=?, progress='', error=?,
		finished_at='', updated_at=?,
		requeue_seq=(SELECT COALESCE(MAX(requeue_seq),0)+1 FROM research_tasks)
		WHERE id=?`, cur, fp, streak, total, errMsg, nowStr(), id)
	return err
}

// RequeueAfterPreemption §QUEUE-REQUEUE（2026-10-12）抢占/重启续跑的回队入口。
//
// 与人工复活入口 RequeueTask 分开，是因为这两条链的语义正好相反：
//   - 人工复活：owner 已经排查过，必须把连败账（fail_fp/fail_streak）和累计账（requeue_total）
//     一起清零，否则旧账会让刚修好的任务一跑就挂起；
//   - 抢占续跑：只是"这台机器让位/重启了"，**不代表这次跑成功过**，既不能清零连败账
//     （旧形态 RequeueTask 的清零正是 33 次回队循环把熔断打漏的第二条通道），
//     也不能不设上限（每次重启都白跑 8 小时）。
//
// 累计账达 TotalRequeueLimit 时同样挂起为 failed_needs_attention，并落 error 文案——
// 抢占型任务反复跑不完同样是"需要人看"，不是可以无限续跑的常态。
// English: preemption requeue — does NOT clear the failure streak (that reset was one of the two
// ways the same-cause breaker got bypassed), only sinks the task to the tail and counts it against
// the cause-independent lifetime requeue cap.
func (d *DB) RequeueAfterPreemption(id int64) error {
	var total int
	var lastErr string
	if err := d.db.QueryRow(`SELECT COALESCE(requeue_total,0), COALESCE(error,'')
		FROM research_tasks WHERE id=?`, id).Scan(&total, &lastErr); err != nil {
		return err
	}
	total++
	if total >= TotalRequeueLimit {
		// 抢占型反复跑不完没有"最后一次失败原因"可写（error 列留的是上次记录），
		// 所以文案里显式带上"含抢占续跑"与旧 error，读的人不会以为这条是崩溃挂起。
		_, err := d.db.Exec(`UPDATE research_tasks SET status='`+TaskNeedsAttention+`',
			requeue_total=?, progress='', finished_at=?, updated_at=?, error=? WHERE id=?`,
			total, nowStr(), nowStr(),
			"累计回队熔断("+itoa(TotalRequeueLimit)+"次回队，含抢占/重启续跑，同因闸未捕获)：最近一次记录＝"+lastErr, id)
		return err
	}
	// 占位符与实参必须逐一对应（这里曾是 3 个 ? 配 4 个参数＝整条 UPDATE 直接失败，
	// 表现为"抢占回队什么都没改"）：requeue_total / updated_at / id。
	res, err := d.db.Exec(`UPDATE research_tasks SET status='queued', requeue_total=?,
		finished_at='', updated_at=?,
		requeue_seq=(SELECT COALESCE(MAX(requeue_seq),0)+1 FROM research_tasks)
		WHERE id=? AND status IN ('preempted','paused')`, total, nowStr(), id)
	if err != nil {
		return err
	}
	// 0 行必须现形：状态闸（只认 preempted/paused）没命中＝调用顺序错了或任务已被别人改写，
	// 静默返回会让累计闸一次也没计上、任务永远转圈而日志全绿（本仓锤过的静默失败族）。
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("抢占回队未生效(id=%d)：该行当前状态不是 preempted/paused，累计计数与挂起都没落上", id)
	}
	return nil
}

// AddTaskNightUsed §NIGHTCAP（2026-10-14）把本次运行占用的时间记进"单夜账"。
// 记账键是**起跑那一夜**的北京日（worker 在任务结束时传入，跨零点的运行整段记在起跑夜）：
// night_day 与传入日不同（新的一夜第一次跑）就把账本翻页（used=minutes），否则累加。
// 出队侧用「上限 − 当日已用」算本次还能跑多久——这就是单夜占用上限的账本面。
// minutes<=0 也照写翻页（新夜首跑 0 分钟也要把键换过来，否则昨天的账会被算到今天头上）。
// English: records this run's occupancy into the per-night ledger keyed by the night the run
// started; a different day key flips the page, otherwise minutes accumulate.
func (d *DB) AddTaskNightUsed(id int64, nightDay string, minutes int) error {
	if nightDay == "" {
		return nil // 无键不记账（防御：调用方必须给北京日，读数不编造）
	}
	if minutes < 0 {
		minutes = 0
	}
	_, err := d.db.Exec(`UPDATE research_tasks SET
		night_day=CASE WHEN COALESCE(night_day,'')=? THEN night_day ELSE ? END,
		night_used_min=CASE WHEN COALESCE(night_day,'')=? THEN COALESCE(night_used_min,0)+? ELSE ? END,
		updated_at=? WHERE id=?`, nightDay, nightDay, nightDay, minutes, minutes, nowStr(), id)
	return err
}

// MarkTaskUnfinished §NIGHTCAP（2026-10-14）记一次"这一夜没跑完"：unfinished_streak+1；
// 连续没跑完达 DeferralStreakLimit 次就顺延——defer_day 写成传入的下一夜（worker 用
// 北京日历算，store 不重复造日历），出队查询把它整条排除到那一夜为止。
// 返回 (最新 streak, 是否已顺延, error)。
// 只对还活跃的行（queued/preempted/paused）生效：若该行已被熔断挂起或落了终态，
// 顺延无意义（挂起本来就不出队），静默跳过并如实返回 deferred=false——调用方不打扰告警。
// 该函数**不动** requeue_total/fail_streak：让位不是失败，两把熔断账只认失败与抢占。
// English: records one non-completing night; at DeferralStreakLimit the row is deferred to the
// given next night (defer_day), which removes it from the dequeue query. Yielding never touches
// the failure or lifetime-requeue breakers.
func (d *DB) MarkTaskUnfinished(id int64, nextDay string) (streak int, deferred bool, err error) {
	var status string
	if err = d.db.QueryRow(`SELECT COALESCE(unfinished_streak,0), status
		FROM research_tasks WHERE id=?`, id).Scan(&streak, &status); err != nil {
		return 0, false, err
	}
	if status == TaskNeedsAttention || status == TaskDone || status == TaskError || status == TaskCancelled {
		return streak, false, nil // 挂起/终态行：顺延无意义，跳过（不把告警变成误报）
	}
	streak++
	if streak >= DeferralStreakLimit && nextDay != "" {
		if _, err = d.db.Exec(`UPDATE research_tasks SET unfinished_streak=?, defer_day=?,
			updated_at=? WHERE id=?`, streak, nextDay, nowStr(), id); err != nil {
			return streak, false, err
		}
		return streak, true, nil
	}
	if _, err = d.db.Exec(`UPDATE research_tasks SET unfinished_streak=?, updated_at=?
		WHERE id=?`, streak, nowStr(), id); err != nil {
		return streak, false, err
	}
	return streak, false, nil
}

// YieldTaskToNight §NIGHTCAP（2026-10-14）"设计内让位"落库：单夜占用到上限（或余额不足起跑），
// 不是失败——progress 保留（断点缓存有效）、error 写让位原因、status 回 queued、
// defer_day 写下一夜（本夜剩余时间让给后面的链任务），requeue_seq 不动（保持链内槽位）。
// 与 MarkTaskUnfinished 的差别：那条按"连续没跑完"计数、到阈值才顺延；这条**无条件**顺延
// （预算已花光，继续排队只会原地空转），所以单独一个入口，也便于读数按成因区分。
// requeue_total/fail_streak 同样不碰——让位不是失败，也不算"转圈"。
// 只对 running 行生效（0 行=调用顺序错了，必须现形，不能静默）。
// English: lands a *planned* yield — budget exhausted or not worth starting: keeps progress,
// queues the row with defer_day set to the next night, and never touches the breakers.
func (d *DB) YieldTaskToNight(id int64, nextDay, reason string) error {
	if r := []rune(reason); len(r) > 500 {
		reason = string(r[:500])
	}
	res, err := d.db.Exec(`UPDATE research_tasks SET status='`+TaskQueued+`', defer_day=?,
		updated_at=?, error=? WHERE id=? AND status='`+TaskRunning+`'`,
		nextDay, nowStr(), reason, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("单夜让位未生效(id=%d)：该行当前状态不是 running，顺延与留痕都没落上", id)
	}
	return nil
}

// DeferQueuedTaskToNight §NIGHTCAP（2026-10-14）队首"余额不足起跑"的让位落库：该任务还在
// 队列里（尚未认领成 running），本夜剩余预算连 MinNightStartMin 都不够，直接写 defer_day
// 让位——否则出队查询每 30s 都会把它取回来，队首后面的任务一条也轮不上。
// 与 YieldTaskToNight 的差别只是行所在状态（queued/preempted vs running），语义同一条：
// 设计内让位，不碰熔断账、不碰 requeue_seq（保持链内槽位）。0 行必须现形。
// English: head-of-queue yield when the remaining night budget isn't worth starting for —
// same semantics as YieldTaskToNight, applied to not-yet-claimed rows.
func (d *DB) DeferQueuedTaskToNight(id int64, nextDay, reason string) error {
	if r := []rune(reason); len(r) > 500 {
		reason = string(r[:500])
	}
	res, err := d.db.Exec(`UPDATE research_tasks SET defer_day=?, updated_at=?, error=?
		WHERE id=? AND status IN ('`+TaskQueued+`','`+TaskPreempted+`')`,
		nextDay, nowStr(), reason, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("队首让位未生效(id=%d)：该行当前状态不是 queued/preempted，顺延没落上", id)
	}
	return nil
}

// NightBudgetLeftMin §NIGHTCAP 纯读数：该任务在本夜（传入北京日）还剩多少分钟可跑。
// night_day 不是今日＝本夜还没跑过，返回全额 cap；是今日＝cap − 已用。cap<=0 恒返回 -1
// （=不限，调用方据此跳过预算逻辑）。
// English: minutes remaining for this task tonight; -1 means uncapped.
func NightBudgetLeftMin(usedMin int, usedDay, today string, capMin int) int {
	if capMin <= 0 {
		return -1
	}
	if usedDay != today {
		return capMin
	}
	left := capMin - usedMin
	if left < 0 {
		return 0
	}
	return left
}

// itoa 小工具：避免为单一常量引入 strconv（重试上限文案用）。
func itoa(n int) string {
	// 手写整数转字符串：逐位取余逆序填入字节数组（避免为单点文案引 strconv）
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
// §M15 注：夜链半截自愈已改用 ChainTaskSeqs 按序位补缺，不再用「有任一任务即整链短路」判据。
func (d *DB) ChainHasTasks(chainDay string) (bool, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM research_tasks WHERE chain_day=?`, chainDay).Scan(&n)
	return n > 0, err
}

// ChainTaskSeqs §M15（2026-09-22）某运行日夜间链已占用的 chain_seq 集合（次数计数）。
// 夜链半截自愈以「当日序位是否已有任务」为断点补缺口——必须包含全部状态
// （done/error/cancelled/needs_attention 均算该序位已投），否则已完成步骤会被重复入队。
// English: M15 per-day chain sequence occupancy (all statuses included) — the nightly enqueue
// backfill only fills plan positions that have no task row yet.
func (d *DB) ChainTaskSeqs(chainDay string) (map[int]int, error) {
	rows, err := d.db.Query(`SELECT chain_seq FROM research_tasks WHERE chain_day=?`, chainDay)
	if err != nil {
		return nil, fmt.Errorf("chain task seqs: %w", err)
	}
	defer rows.Close()
	out := map[int]int{}
	for rows.Next() {
		var seq int
		if err := rows.Scan(&seq); err != nil {
			return nil, err
		}
		out[seq]++
	}
	return out, rows.Err()
}

// ChainParkedCount §QUEUE-REQUEUE（2026-10-12）某运行日夜间链里挂起（failed_needs_attention）
// 的步数。链收尾必须先问这个数：ActiveResearchTasks 把挂起态算"已终结"，于是"队列里没有活跃行"
// 会被读成"这一天的链全跑完了"——而那一行其实是被熔断闸请出队列的。done=true 就是这么撒的谎
// （前端 /api/research/progress 与心跳都读这个位；§降级链会兜死分支同族）。
// English: count of parked steps in a nightly chain — "no active rows" must never be reported as
// "chain finished" while any step is parked by a breaker.
func (d *DB) ChainParkedCount(chainDay string) (int, error) {
	if chainDay == "" {
		return 0, nil
	}
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM research_tasks WHERE chain_day=? AND status=?`,
		chainDay, TaskNeedsAttention).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("chain parked count(%s): %w", chainDay, err)
	}
	return n, nil
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
	// 共享列表查询：ListResearchTasks 与 ActiveResearchTasks 仅 where 子句不同，
	// 统一追加 WHERE 后按 ID 倒序（最新在前）
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
		COALESCE(fail_fp,''), COALESCE(fail_streak,0), COALESCE(requeue_total,0),
		COALESCE(night_day,''), COALESCE(night_used_min,0), COALESCE(unfinished_streak,0),
		COALESCE(defer_day,''),
		created_at, COALESCE(started_at,''), COALESCE(finished_at,''), updated_at`

// scanResearchTask 从一行扫描出 ResearchTask（QueryRow 与 Rows 共用）。
// 列序必须与 researchTaskCols 逐字对应（§NIGHTCAP 新增四列插在 requeue_total 之后）。
func scanResearchTask(s rowScanner) (*ResearchTask, error) {
	var t ResearchTask
	if err := s.Scan(&t.ID, &t.Type, &t.RefID, &t.Priority, &t.Status, &t.Progress,
		&t.ResultNum, &t.ResultText, &t.Error, &t.Payload,
		&t.ChainDay, &t.ChainSeq, &t.Control, &t.RequeueSeq, &t.RetryCount,
		&t.FailFP, &t.FailStreak, &t.RequeueTotal,
		&t.NightDay, &t.NightUsedMin, &t.UnfinishedStreak, &t.DeferDay,
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
