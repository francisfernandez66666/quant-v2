// 研究候选库（B5）：优化器产出的候选 → 人工审批 → 应用。
package store

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Candidate 一条研究候选（待审批的战法/因子参数改动）。
// （Candidate is one research candidate awaiting approval.）
type Candidate struct {
	ID        int64   `json:"id"`         // 自增 ID
	CreatedAt string  `json:"created_at"` // 创建时间
	Kind      string  `json:"kind"`       // weights | d1rule | factor | depth（候选类型）
	Status    string  `json:"status"`     // proposed | approved | rejected | applied | grayscale（待审/通过/拒绝/已应用/灰度）
	Factors   string  `json:"factors"`    // 因子 JSON 数组
	Weights   string  `json:"weights"`    // 权重 JSON 对象
	Metric    float64 `json:"metric"`     // 指标
	ICMean    float64 `json:"ic_mean"`    // IC均值
	IR        float64 `json:"ir"`         // ICIR（信息比率）
	AvgExcess float64 `json:"avg_excess"` // 平均超额收益
	Horizon   int     `json:"horizon"`    // 周期
	Reason    string  `json:"reason"`     // 原因
	// Guard §C2 护栏档位：strong/standard/weak/reject（前端分级色标；审批面随档位放行）。
	Guard string `json:"guard"`
	// Params §C4 参数快照：{start,end,h,variant,top_n,min_ir,min_days,…} JSON，精确复现审批时的战法。
	Params string `json:"params"`
}

// 候选常见状态。
const (
	CandProposed  = "proposed"
	CandApproved  = "approved"
	CandRejected  = "rejected"
	CandApplied   = "applied"
	CandGrayscale = "grayscale"
)

// candidateCols 候选行的通用列（Guard/Params 追加在 reason 之后，与表结构一一对应）。
const candidateCols = `id, created_at, kind, status, factors, weights,
		COALESCE(metric,0), COALESCE(ic_mean,0), COALESCE(ir,0), COALESCE(avg_excess,0),
		COALESCE(horizon,0), COALESCE(reason,''), COALESCE(guard,''), COALESCE(params,'')`

// scanCandidate 把候选行扫描进 Candidate（字段顺序与 candidateCols 严格一致）。
func scanCandidate(sc interface{ Scan(...any) error }) (*Candidate, error) {
	var c Candidate
	if err := sc.Scan(&c.ID, &c.CreatedAt, &c.Kind, &c.Status, &c.Factors, &c.Weights,
		&c.Metric, &c.ICMean, &c.IR, &c.AvgExcess, &c.Horizon, &c.Reason, &c.Guard, &c.Params); err != nil {
		return nil, err
	}
	return &c, nil
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
		c.Status = CandProposed
	}
	if c.Guard == "" {
		c.Guard = "standard"
	}
	res, err := d.db.Exec(`INSERT INTO research_candidates
		(created_at, kind, status, factors, weights, metric, ic_mean, ir, avg_excess, horizon, reason, guard, params)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.CreatedAt, c.Kind, c.Status, c.Factors, c.Weights,
		c.Metric, c.ICMean, c.IR, c.AvgExcess, c.Horizon, c.Reason, c.Guard, c.Params)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListCandidates 列出候选（status 为空则全量，按创建时间倒序）。
// （ListCandidates lists candidates, newest first.）
func (d *DB) ListCandidates(status string) ([]Candidate, error) {
	query := `SELECT ` + candidateCols + ` FROM research_candidates`
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
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// CandidateByID 按 ID 取候选。
// （CandidateByID fetches one candidate by ID.）
func (d *DB) CandidateByID(id int64) (*Candidate, error) {
	row := d.db.QueryRow(`SELECT `+candidateCols+` FROM research_candidates WHERE id=?`, id)
	c, err := scanCandidate(row)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

// CandidateExistsPromotion §WS-H 维7：判断某灰度候选是否已有 [晋升候选] 记录且仍在生命周期内
// （proposed/approved/applied/grayscale）——按 Params.from_candidate 匹配，避免重复生成晋升候选。
// English: WS-H 维7 — reports whether a grayscale candidate already has an in-lifecycle promotion
// record (matched via Params.from_candidate), preventing duplicate promotion candidates.
func (d *DB) CandidateExistsPromotion(fromCandidate int64) (bool, error) {
	rows, err := d.db.Query(`SELECT `+candidateCols+` FROM research_candidates
		WHERE status IN (?,?,?,?)`, CandProposed, CandApproved, CandApplied, CandGrayscale)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return false, err
		}
		if promotionFromCandidate(c.Params) == fromCandidate {
			return true, nil
		}
	}
	return false, rows.Err()
}

// promotionFromCandidate 解析 Params JSON 中的 from_candidate（0=无）。
func promotionFromCandidate(params string) int64 {
	var p struct {
		FromCandidate int64 `json:"from_candidate"`
	}
	if json.Unmarshal([]byte(params), &p) != nil {
		return 0
	}
	return p.FromCandidate
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

// AppendCandidateReason 在候选 reason 后追加后缀（以换行分隔，幂等：已含则不重复追加）。
// （AppendCandidateReason appends a suffix line to a candidate's reason, skipping when already present.）
func (d *DB) AppendCandidateReason(id int64, suffix string) error {
	c, err := d.CandidateByID(id)
	if err != nil || c == nil || suffix == "" {
		return err
	}
	if strings.Contains(c.Reason, suffix) {
		return nil
	}
	reason := c.Reason
	if reason != "" {
		reason += "\n"
	}
	reason += suffix
	_, err = d.db.Exec(`UPDATE research_candidates SET reason=? WHERE id=?`, reason, id)
	return err
}

// comboKey 规范化因子组合键：排序后以控制符连接（set equality，顺序无关）。
// English: canonical combo key — sorted factor IDs joined by a control sep (set equality).
func comboKey(combo []string) string {
	s := append([]string(nil), combo...)
	sort.Strings(s)
	return strings.Join(s, "\x1f")
}

// parseCombo 解析候选 Factors JSON 为因子 ID 切片（解析失败返回 nil）。
func parseCombo(raw string) []string {
	var out []string
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// ComboExistsLike §S2 精确重复：是否存在候选其规范化因子集合与 combo 相同且状态在 statuses 内。
// （ComboExistsLike reports whether any candidate's canonical factor set equals combo within statuses.）
func (d *DB) ComboExistsLike(combo []string, statuses ...string) (bool, error) {
	cands, err := d.listStatuses("", statuses)
	if err != nil {
		return false, err
	}
	key := comboKey(combo)
	for i := range cands {
		if comboKey(parseCombo(cands[i].Factors)) == key {
			return true, nil
		}
	}
	return false, nil
}

// ComboNearDup §S2 近似重复：是否存在候选与 combo 的 Jaccard（交/并）≥ threshold 且状态在 statuses 内。
// English: Jaccard(I∪)≥threshold against any candidate's factor set within statuses.
func (d *DB) ComboNearDup(combo []string, threshold float64, statuses ...string) (bool, error) {
	if threshold <= 0 || len(combo) == 0 {
		return false, nil
	}
	cands, err := d.listStatuses("", statuses)
	if err != nil {
		return false, err
	}
	my := map[string]bool{}
	for _, f := range combo {
		my[f] = true
	}
	for i := range cands {
		other := parseCombo(cands[i].Factors)
		if len(other) == 0 {
			continue
		}
		their := map[string]bool{}
		intersect := 0
		for _, f := range other {
			their[f] = true
			if my[f] {
				intersect++
			}
		}
		union := len(my) + len(their) - intersect
		if union == 0 {
			continue
		}
		if float64(intersect)/float64(union) >= threshold {
			return true, nil
		}
	}
	return false, nil
}

// LatestCandidate 按 kind+status 取最新一条候选（id 最大），无则返回 (nil,nil)。
// （LatestCandidate returns the newest candidate matching kind and any of statuses.）
func (d *DB) LatestCandidate(kind string, statuses ...string) (*Candidate, error) {
	query := `SELECT ` + candidateCols + ` FROM research_candidates WHERE kind=?`
	args := []any{kind}
	if len(statuses) > 0 {
		query += ` AND status IN (` + placeholders(len(statuses)) + `)`
		for _, s := range statuses {
			args = append(args, s)
		}
	}
	query += ` ORDER BY id DESC LIMIT 1`
	row := d.db.QueryRow(query, args...)
	c, err := scanCandidate(row)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	return c, nil
}

// ProposedFactorCandidatesSince 取自 since（YYYYMMDD 或 YYYY-MM-DD，含当日）以来创建的 proposed 因子候选（id 升序，回填顺序稳定）。
// English: proposed kind="factor" candidates created on/after since, oldest first for deterministic backfilling.
func (d *DB) ProposedFactorCandidatesSince(since string) ([]Candidate, error) {
	if len(since) == 8 {
		since = since[0:4] + "-" + since[4:6] + "-" + since[6:8]
	}
	query := `SELECT ` + candidateCols + ` FROM research_candidates
		WHERE kind='factor' AND status='proposed' AND substr(created_at,1,10) >= ? ORDER BY id ASC`
	rows, err := d.db.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// listStatuses 取全部候选并就地按 status 过滤（候选库体量小，走全量再过滤）。
func (d *DB) listStatuses(kind string, statuses []string) ([]Candidate, error) {
	query := `SELECT ` + candidateCols + ` FROM research_candidates`
	args := []any{}
	conds := []string{}
	if kind != "" {
		conds = append(conds, `kind=?`)
		args = append(args, kind)
	}
	if len(statuses) > 0 {
		conds = append(conds, `status IN (`+placeholders(len(statuses))+`)`)
		for _, s := range statuses {
			args = append(args, s)
		}
	}
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY id DESC`
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// placeholders 生成逗号分隔的 ? 占位符（个数 n）。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
