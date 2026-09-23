// 研究窗口级断点（二期）：research_ckpts 表读写。
// 用途：discover-factors 的窗口分块装配是重 IO/CPU 步骤，被抢占（kill）后整段重算代价高。
// 各阶段按 (resume_key, stage, 窗口) 缓存产物 JSON，续跑时命中即跳过该窗装配。
// resume_key = 任务类型+区间+参数+股票池哈希：任何影响结果的参数变更都会生成新 key，
// 旧缓存自然失效（不删除，体积小可留档；需要时可按 key 前缀清理）。
// English: window-level checkpoints (phase 2) — research_ckpts read/write. Each discovery stage
// caches per-window artifacts so a preempted run skips finished windows on resume. resume_key
// embeds type+range+params+pool hash, so any result-affecting change rolls a fresh key.
// §ADJ-BASIS：口径位进 key 之后改前的旧断点行成为永久死重（GB 级），清理入口见
// PruneStaleCheckpoints（默认 dry-run，只删不含当前口径位的行）。
package store

import (
	"fmt"
	"strings"
	"time"
)

// GetWindowCkpt 读一个窗口的断点 payload；未命中返回 ("", false, nil)。
func (d *DB) GetWindowCkpt(resumeKey, stage, winStart, winEnd string) (string, bool, error) {
	var js string
	err := d.db.QueryRow(`SELECT payload FROM research_ckpts
		WHERE resume_key=? AND stage=? AND win_start=? AND win_end=?`,
		resumeKey, stage, winStart, winEnd).Scan(&js)
	if err == nil {
		return js, true, nil
	}
	if err.Error() == "sql: no rows in result set" {
		return "", false, nil
	}
	return "", false, err
}

// PutWindowCkpt 写/覆盖一个窗口的断点 payload（幂等；created_at 取当前时刻）。
func (d *DB) PutWindowCkpt(resumeKey, stage, winStart, winEnd, payload string) error {
	return d.PutWindowCkptAt(resumeKey, stage, winStart, winEnd, payload,
		time.Now().Format("2006-01-02 15:04:05"))
}

// PutWindowCkptAt 同 PutWindowCkpt，但由调用方显式给 created_at（YYYY-MM-DD HH:MM:SS）。
// 只供运维/取证工具与测试回填时间线用——研究主链一律走 PutWindowCkpt 的 now() 语义，
// 因为 prune 的 stale 判据完全依赖 created_at 的真实写入时刻。
// English: PutWindowCkpt with an explicit created_at; for ops/forensics tooling and tests only,
// since the prune predicate reads real write timestamps.
func (d *DB) PutWindowCkptAt(resumeKey, stage, winStart, winEnd, payload, createdAt string) error {
	if createdAt == "" {
		return fmt.Errorf("put ckpt: createdAt 不能为空")
	}
	_, err := d.db.Exec(`INSERT INTO research_ckpts
		(resume_key, stage, win_start, win_end, payload, created_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(resume_key, stage, win_start, win_end)
		DO UPDATE SET payload=excluded.payload, created_at=excluded.created_at`,
		resumeKey, stage, winStart, winEnd, payload, createdAt)
	if err != nil {
		return fmt.Errorf("put ckpt at: %w", err)
	}
	return nil
}

// DeleteWindowCkpts 清除某 resume_key 的全部断点（显式重算用；一般不调用，靠 key 轮换失效）。
func (d *DB) DeleteWindowCkpts(resumeKey string) error {
	_, err := d.db.Exec(`DELETE FROM research_ckpts WHERE resume_key=?`, resumeKey)
	return err
}

// ---------------------------------------------------------------------------
// §ADJ-BASIS 旧口径断点清理（research CLI 子命令 prune-stale-checkpoints 的后端）
//
// 背景：口径位折进 resume_key 之后（见 internal/research.AdjBaselineVersion），改前写入的
// 断点行永远不会再被命中——它们是纯粹的死重，但 research_ckpts 存的是逐窗口的产物 JSON
// （生产库 GB 级），所以要有一个可审计的清理入口。
//
// 设计约束（owner 定的保守默认）：
//   - 默认只统计不删（dry-run），删除必须显式 apply=true；
//   - 只删"不含当前口径位"的行，含口径位的行一律不动；
//   - 本表没有 status/updated_at 列（schema：resume_key/stage/win_start/win_end/payload/
//     created_at，主键四元组），因此"是否有跑到一半的新口径任务"只能问任务队列
//     research_tasks（见 ckptWritingTasksInProgress）；
//   - created_at 在本表承担 updated_at 语义：PutWindowCkpt 命中冲突时会把 created_at
//     一并刷新（重写窗口产物即最近一次写入时间），所以 cutoff 取"含口径位行的
//     MAX(created_at)"，严格早于它才算 stale。
//
// English: offline pruning for pre-basis checkpoint rows. research_ckpts has no status or
// updated_at column, so created_at carries the write timestamp (PutWindowCkpt refreshes it on
// conflict) and the in-progress guard reads research_tasks. Non-destructive by default: apply=false
// only reports, and only rows without the current basis tag are ever eligible.
// ---------------------------------------------------------------------------

// CkptPruneNamespace 单个断点命名空间（resume_key 前缀）的清理统计。
// 只带前缀，绝不携带 resume_key 全文（键里含策略参数载荷）。
// English: per-namespace (key prefix) tallies; full resume_keys are never carried out.
type CkptPruneNamespace struct {
	Namespace string `json:"namespace"` // df| / dp| / pfac-dedup: / other
	Rows      int    `json:"rows"`      // 该命名空间总行数
	Stale     int    `json:"stale"`     // 判定为旧口径的行数
	Bytes     int64  `json:"bytes"`     // 旧口径行 payload 字节合计（回收量下界估算）
}

// CkptPruneReport 一次 prune 的统计结果（dry-run 与 apply 同一结构，Apply 字段区分）。
type CkptPruneReport struct {
	Marker     string `json:"marker"`      // 判据用的口径位字面量
	Apply      bool   `json:"apply"`       // 是否真删
	CutoffAt   string `json:"cutoff_at"`   // 最后一条新口径断点写入时间（严格早于它才算 stale）
	TotalRows  int    `json:"total_rows"`  // 表内全部行数
	MarkedRows int    `json:"marked_rows"` // 含当前口径位的行数（保留基线）
	StaleRows  int    `json:"stale_rows"`  // 命中旧口径判据的行数
	StaleBytes int64  `json:"stale_bytes"` // 命中行的 payload 字节合计
	// DeletedRows 实际删除行数（dry-run 恒为 0）。
	DeletedRows int `json:"deleted_rows"`
	// TableBefore/TableAfter dbstat 统计的 research_ckpts 实占字节；dbstat 不可用时为 -1。
	TableBefore int64 `json:"table_before_bytes"`
	TableAfter  int64 `json:"table_after_bytes"`
	// Blocked 非空 = 检出正在写断点的在跑任务，本次拒绝删除（只报统计）。
	// 元素形如 "discover_factors#123=running"，只含类型/ID/状态，不含 payload。
	Blocked []string `json:"blocked_by,omitempty"`
	// NoBaseline 库里一条含口径位的行都没有：没有"新口径已经落库"的证据，拒绝清理。
	NoBaseline bool `json:"no_baseline,omitempty"`
	// Namespaces 逐命名空间统计（按 stale 降序）。
	Namespaces []CkptPruneNamespace `json:"namespaces"`
}

// staleCond 旧口径判据 SQL 片段（占位符顺序：口径位、cutoff）。
// English: the shared stale predicate fragment — param order is (marker, cutoff).
const staleCond = `instr(resume_key, ?) = 0 AND created_at < ?`

// PruneStaleCheckpoints 统计（apply=false，默认）或删除（apply=true）research_ckpts 里的
// 旧复权口径断点行。
//
// stale 判据（最终采用形态，两条同时成立）：
//
//	① instr(resume_key, marker)=0 —— 键里没有当前口径位 |adj=<版本>；
//	② created_at < cutoff —— 早于"含口径位行的 MAX(created_at)"，即最后一次新口径断点
//	  写入（严格早于，同秒写入的行不删）；库里没有任何含口径位行时整轮不删（NoBaseline）。
//
// 另外的前置门：任务队列里存在未终结的发现类任务（queued/running/paused/preempted 的
// discover_factors / discover_patterns）即视为有跑到一半的写断点任务，拒绝删除。
//
// marker 必须是以 "|adj=" 开头的完整口径位（由 internal/research.AdjBasisMarker 单点定义、
// 调用方传入，避免 store→research 的循环依赖与字符串复制）。
//
// English: counts (default) or deletes pre-basis checkpoint rows. A row is stale only when its
// resume_key lacks the current basis tag AND its created_at predates the newest basis-tagged write;
// any unfinished discovery task in the queue blocks deletion, and with zero basis-tagged rows
// nothing is pruned at all. The marker is passed in because internal/research imports this package.
func (d *DB) PruneStaleCheckpoints(marker string, apply bool) (*CkptPruneReport, error) {
	if !strings.HasPrefix(marker, "|adj=") {
		return nil, fmt.Errorf("prune ckpts: 口径位格式异常（应形如 \"|adj=<版本>\"）: len=%d", len(marker))
	}
	rep := &CkptPruneReport{Marker: marker, Apply: apply, TableBefore: -1, TableAfter: -1}

	// 表内总量 + 含口径位基线（cutoff = 最后一次新口径断点写入时间）。
	if err := d.db.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN instr(resume_key, ?) > 0 THEN 1 ELSE 0 END), 0),
			COALESCE(MAX(CASE WHEN instr(resume_key, ?) > 0 THEN created_at END), '')
		FROM research_ckpts`, marker, marker).Scan(&rep.TotalRows, &rep.MarkedRows, &rep.CutoffAt); err != nil {
		return nil, fmt.Errorf("prune ckpts scan baseline: %w", err)
	}
	// 一条新口径断点都没有：新基线还没落过库，此刻删旧行等于在没有对照的情况下删证据。
	if rep.MarkedRows == 0 || rep.CutoffAt == "" {
		rep.NoBaseline = true
		return rep, nil
	}

	// 逐命名空间统计（前缀白名单，未知前缀统一归 'other'，不外泄键全文）。
	rows, err := d.db.Query(`SELECT CASE
			WHEN resume_key LIKE 'df|%' THEN 'df|'
			WHEN resume_key LIKE 'dp|%' THEN 'dp|'
			WHEN resume_key LIKE 'pfac-dedup:%' THEN 'pfac-dedup:'
			ELSE 'other' END AS ns,
		COUNT(*),
		SUM(CASE WHEN `+staleCond+` THEN 1 ELSE 0 END),
		SUM(CASE WHEN `+staleCond+` THEN LENGTH(COALESCE(payload,'')) ELSE 0 END)
		FROM research_ckpts GROUP BY ns ORDER BY 3 DESC, 1`,
		marker, rep.CutoffAt, marker, rep.CutoffAt)
	if err != nil {
		return nil, fmt.Errorf("prune ckpts group: %w", err)
	}
	for rows.Next() {
		var ns CkptPruneNamespace
		if err := rows.Scan(&ns.Namespace, &ns.Rows, &ns.Stale, &ns.Bytes); err != nil {
			rows.Close()
			return nil, fmt.Errorf("prune ckpts group scan: %w", err)
		}
		rep.Namespaces = append(rep.Namespaces, ns)
		rep.StaleRows += ns.Stale
		rep.StaleBytes += ns.Bytes
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 在跑门：有未终结的发现类任务时不删（连统计都照旧给出，便于运维先看清规模）。
	blocked, err := d.ckptWritingTasksInProgress()
	if err != nil {
		return nil, err
	}
	rep.Blocked = blocked

	if !apply || rep.StaleRows == 0 || len(rep.Blocked) > 0 {
		return rep, nil // dry-run / 无可删 / 在跑：一律不动数据
	}
	rep.TableBefore = d.tableBytes("research_ckpts")
	res, err := d.db.Exec(`DELETE FROM research_ckpts WHERE `+staleCond, marker, rep.CutoffAt)
	if err != nil {
		return nil, fmt.Errorf("prune ckpts delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("prune ckpts rows affected: %w", err)
	}
	rep.DeletedRows = int(n)
	rep.TableAfter = d.tableBytes("research_ckpts")
	return rep, nil
}

// ckptWritingTasksInProgress 返回未终结且会写 research_ckpts 的任务描述（type#id=status）。
// 断点表本身没有状态列，"跑到一半"只能问队列；queued 也算——它随时会被 worker 领取开写。
// backtest_* 任务不写本表，故不入门。
// English: the checkpoints table has no status column, so the in-flight guard asks the queue for
// unfinished discovery tasks (queued counts too: a worker may claim it mid-prune).
func (d *DB) ckptWritingTasksInProgress() ([]string, error) {
	rows, err := d.db.Query(`SELECT type, id, status FROM research_tasks
		WHERE status IN ('queued','running','paused','preempted')
		  AND type IN (?,?) ORDER BY id DESC LIMIT 50`,
		TaskDiscoverFactors, TaskDiscoverPatterns)
	if err != nil {
		return nil, fmt.Errorf("prune ckpts in-flight guard: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ, status string
		var id int64
		if err := rows.Scan(&typ, &id, &status); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s#%d=%s", typ, id, status))
	}
	return out, rows.Err()
}

// tableBytes 用 dbstat 估算某表实占字节（含其索引页）；dbstat 不可用/表不存在时返回 -1，
// 调用方按"无法度量"处理，不影响主流程。
// English: dbstat-based byte footprint of a table (indexes included); -1 when unmeasurable.
func (d *DB) tableBytes(table string) int64 {
	var n int64
	if err := d.db.QueryRow(`SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name IN
		(SELECT name FROM sqlite_master WHERE tbl_name=?)`, table).Scan(&n); err != nil {
		return -1
	}
	return n
}
