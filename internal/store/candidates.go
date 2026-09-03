// 研究候选库（B5）：优化器产出的候选 → 人工审批 → 应用。
package store

import (
	"time"
)

// Candidate 一条研究候选（待审批的战法/因子参数改动）。
// （Candidate is one research candidate awaiting approval.）
type Candidate struct {
	ID        int64   `json:"id"`         // 自增 ID
	CreatedAt string  `json:"created_at"` // 创建时间
	Kind      string  `json:"kind"`       // weights | d1rule | factor | depth（候选类型）
	Status    string  `json:"status"`     // proposed | approved | rejected | applied（待审/通过/拒绝/已应用）
	Factors   string  `json:"factors"`    // 因子 JSON 数组
	Weights   string  `json:"weights"`    // 权重 JSON 对象
	Metric    float64 `json:"metric"`     // 指标
	ICMean    float64 `json:"ic_mean"`    // IC均值
	IR        float64 `json:"ir"`         // ICIR（信息比率）
	AvgExcess float64 `json:"avg_excess"` // 平均超额收益
	Horizon   int     `json:"horizon"`    // 周期
	Reason    string  `json:"reason"`     // 原因
}

// RejectedFactorCombos §F4 取全部已驳回（status='rejected'）的因子战法候选的因子集合。
// 返回每个候选的 Factors JSON 原文，供调用方解析为因子 ID 组合做发现去重。
// English: §F4 returns every rejected kind="factor" candidate's raw Factors JSON, so discovery can
// de-duplicate against combinations that were already rejected.
func (d *DB) RejectedFactorCombos() ([]string, error) {
	rows, err := d.db.Query(
		`SELECT factors FROM research_candidates WHERE kind='factor' AND status='rejected' AND factors<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SaveCandidate 写入一条候选。
// （SaveCandidate inserts a candidate.）
func (d *DB) SaveCandidate(c *Candidate) (int64, error) {
	if c.CreatedAt == "" {
		c.CreatedAt = time.Now().Format("2006-01-02 15:04:05")
	}
	if c.Status == "" {
		c.Status = "proposed"
	}
	res, err := d.db.Exec(`INSERT INTO research_candidates
		(created_at, kind, status, factors, weights, metric, ic_mean, ir, avg_excess, horizon, reason)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		c.CreatedAt, c.Kind, c.Status, c.Factors, c.Weights,
		c.Metric, c.ICMean, c.IR, c.AvgExcess, c.Horizon, c.Reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListCandidates 列出候选（status 为空则全量，按创建时间倒序）。
// （ListCandidates lists candidates, newest first.）
func (d *DB) ListCandidates(status string) ([]Candidate, error) {
	query := `SELECT id, created_at, kind, status, factors, weights,
		COALESCE(metric,0), COALESCE(ic_mean,0), COALESCE(ir,0), COALESCE(avg_excess,0),
		COALESCE(horizon,0), COALESCE(reason,'') FROM research_candidates`
	args := []any{}
	if status != "" {
		query += ` WHERE status=?`
		args = append(args, status)
	}
	query += ` ORDER BY id DESC`
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.CreatedAt, &c.Kind, &c.Status, &c.Factors, &c.Weights,
			&c.Metric, &c.ICMean, &c.IR, &c.AvgExcess, &c.Horizon, &c.Reason); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CandidateByID 按 ID 取候选。
// （CandidateByID fetches one candidate by ID.）
func (d *DB) CandidateByID(id int64) (*Candidate, error) {
	query := `SELECT id, created_at, kind, status, factors, weights,
		COALESCE(metric,0), COALESCE(ic_mean,0), COALESCE(ir,0), COALESCE(avg_excess,0),
		COALESCE(horizon,0), COALESCE(reason,'') FROM research_candidates WHERE id=?`
	row := d.db.QueryRow(query, id)
	var c Candidate
	if err := row.Scan(&c.ID, &c.CreatedAt, &c.Kind, &c.Status, &c.Factors, &c.Weights,
		&c.Metric, &c.ICMean, &c.IR, &c.AvgExcess, &c.Horizon, &c.Reason); err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateCandidateStatus 更新候选状态。
// （UpdateCandidateStatus sets a candidate's status.）
func (d *DB) UpdateCandidateStatus(id int64, status string) error {
	_, err := d.db.Exec(`UPDATE research_candidates SET status=? WHERE id=?`, status, id)
	return err
}

// UpdateCandidateAvgExcess 更新候选的回测超额（B4 回测结果回填）。
// （UpdateCandidateAvgExcess backfills a candidate's backtest excess (B4 result).）
func (d *DB) UpdateCandidateAvgExcess(id int64, avgExcess float64) error {
	_, err := d.db.Exec(`UPDATE research_candidates SET avg_excess=? WHERE id=?`, avgExcess, id)
	return err
}
