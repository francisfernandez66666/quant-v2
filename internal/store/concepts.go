// concepts.go — §C1-V2 概念库落库/覆盖率读数。
//
// 设计约束：概念数据每日全量替换单个概念（先删后插，避免残留旧成分）；同步状态单独一张表，
// 覆盖率按“有成员且 status=ok 的概念数 / 概念总数”计算。读不到就是 0/空，不伪造。
//
// English: §C1-V2 concept-library persistence. Each concept is fully replaced per sync (delete then
// insert) to avoid stale members; per-concept state lives in its own table and coverage is
// “concepts with members and status=ok / total concepts”. Missing reads stay zero/empty.
package store

import "fmt"

// ConceptSyncState 单个概念的最近一次同步结果。
// English: the latest sync result for one concept.
type ConceptSyncState struct {
	ConceptCode string `json:"concept_code"`
	ConceptName string `json:"concept_name"`
	LastSuccess string `json:"last_success"`
	MemberCount int    `json:"member_count"`
	Status      string `json:"status"`
	Error       string `json:"error"`
}

// ReplaceConceptMembers 单概念全量替换：同一事务内删旧插新。
// empty tsCodes 也执行删除（该概念当前无有效成分），避免把旧成员当新数据。
// English: fully replaces one concept's members in a transaction; an empty list still deletes old rows.
func (d *DB) ReplaceConceptMembers(conceptCode, conceptName, source string, tsCodes []string, updatedAt string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM concept_members WHERE concept_code=?`, conceptCode); err != nil {
		return err
	}
	seen := map[string]bool{}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO concept_members
		(concept_code, concept_name, ts_code, source, updated_at) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, code := range tsCodes {
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		if _, err := stmt.Exec(conceptCode, conceptName, code, source, updatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertConceptSyncState 写入/更新单个概念的同步状态。
// English: upserts one concept's sync state.
func (d *DB) UpsertConceptSyncState(conceptCode, conceptName, lastSuccess string, memberCount int, status, errMsg string) error {
	if conceptCode == "" {
		return fmt.Errorf("concept_code 为空")
	}
	_, err := d.db.Exec(`INSERT INTO concept_sync_state
		(concept_code, concept_name, last_success, member_count, status, error)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(concept_code) DO UPDATE SET
			concept_name=excluded.concept_name,
			last_success=excluded.last_success,
			member_count=excluded.member_count,
			status=excluded.status,
			error=excluded.error`,
		conceptCode, conceptName, lastSuccess, memberCount, status, errMsg)
	return err
}

// ListConceptSyncState 列出全部概念的同步状态（按代码排序）。
// English: lists every concept's sync state ordered by code.
func (d *DB) ListConceptSyncState() ([]ConceptSyncState, error) {
	rows, err := d.db.Query(`SELECT concept_code, concept_name, COALESCE(last_success,''),
		COALESCE(member_count,0), COALESCE(status,''), COALESCE(error,'')
		FROM concept_sync_state ORDER BY concept_code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConceptSyncState{}
	for rows.Next() {
		var s ConceptSyncState
		if err := rows.Scan(&s.ConceptCode, &s.ConceptName, &s.LastSuccess, &s.MemberCount, &s.Status, &s.Error); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ConceptCoverage 返回 (已同步概念数, 概念总数, 覆盖率%, 最近成功时间)。
// 已同步口径：status='ok' 且 member_count>0；总数为同步状态表行数（后台每轮写全量概念列表）。
// English: returns (synced, total, coverage%, latest success). "Synced" = status ok with members;
// total = state-table rows (the sync job writes the full concept list each run).
func (d *DB) ConceptCoverage() (synced, total int, coveragePct float64, lastSuccess string, err error) {
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM concept_sync_state`).Scan(&total); err != nil {
		return 0, 0, 0, "", err
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM concept_sync_state WHERE status='ok' AND member_count>0`).Scan(&synced); err != nil {
		return 0, 0, 0, "", err
	}
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(last_success),'') FROM concept_sync_state`).Scan(&lastSuccess); err != nil {
		return 0, 0, 0, "", err
	}
	if total > 0 {
		coveragePct = float64(synced) / float64(total) * 100
	}
	return synced, total, coveragePct, lastSuccess, nil
}

// ConceptMembersForTsCode 返回某只股票所属的概念代码列表（反向查询）。
// English: concept codes for one stock (reverse lookup).
func (d *DB) ConceptMembersForTsCode(tsCode string) ([]string, error) {
	rows, err := d.db.Query(`SELECT concept_code FROM concept_members WHERE ts_code=? ORDER BY concept_code`, tsCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
