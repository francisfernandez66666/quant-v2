// D1 评分历史（历史D1方案B·攒数据期）：d1_scores 表写入。
// 数据源：盘中引擎每轮 LLM 批量打标结果（engine D1 步骤），按 (日期,股票) 幂等落库；
// 用途：N 形战法回放按触发日 JOIN 当日真实 D1 分，替代固定规则分近似。
// English: D1 score history — idempotent persistence of intraday LLM scoring results;
// consumed later by N-shape replay joining the real trigger-day score.
package store

import (
	"fmt"
	"time"
)

// D1ScoreRow 一条 D1 评分落库行（与 combat_agent.D1Score 字段解耦，避免反向依赖）。
type D1ScoreRow struct {
	Code    string  // 代码
	Score   float64 // 评分
	Blocked bool    // 是否阻断
	Reason  string  // 原因
}

// UpsertD1Scores 批量幂等写入某日期的 D1 评分（同 date+code 覆盖，保留最新一轮）。
func (d *DB) UpsertD1Scores(date string, rows []D1ScoreRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("d1 begin: %w", err)
	}
	now := time.Now().Format("2006-01-02 15:04:05")
	stmt, err := tx.Prepare(`INSERT INTO d1_scores (date, code, score, blocked, reason, created_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(date, code) DO UPDATE SET
		  score=excluded.score, blocked=excluded.blocked,
		  reason=excluded.reason, created_at=excluded.created_at`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("d1 prepare: %w", err)
	}
	defer stmt.Close()
	for _, r := range rows {
		bl := 0
		if r.Blocked {
			bl = 1
		}
		if _, err := stmt.Exec(date, r.Code, r.Score, bl, r.Reason, now); err != nil {
			tx.Rollback()
			return fmt.Errorf("d1 exec %s: %w", r.Code, err)
		}
	}
	return tx.Commit()
}

// D1ScoresByDate 读某日期全部评分（回放侧按触发日 JOIN 的查询入口，预留）。
func (d *DB) D1ScoresByDate(date string) (map[string]float64, error) {
	rows, err := d.db.Query(`SELECT code, score FROM d1_scores WHERE date=?`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var code string
		var v float64
		if err := rows.Scan(&code, &v); err != nil {
			return nil, err
		}
		out[code] = v
	}
	return out, rows.Err()
}
