// 研究窗口级断点（二期）：research_ckpts 表读写。
// 用途：discover-factors 的窗口分块装配是重 IO/CPU 步骤，被抢占（kill）后整段重算代价高。
// 各阶段按 (resume_key, stage, 窗口) 缓存产物 JSON，续跑时命中即跳过该窗装配。
// resume_key = 任务类型+区间+参数+股票池哈希：任何影响结果的参数变更都会生成新 key，
// 旧缓存自然失效（不删除，体积小可留档；需要时可按 key 前缀清理）。
// English: window-level checkpoints (phase 2) — research_ckpts read/write. Each discovery stage
// caches per-window artifacts so a preempted run skips finished windows on resume. resume_key
// embeds type+range+params+pool hash, so any result-affecting change rolls a fresh key.
package store

import (
	"fmt"
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

// PutWindowCkpt 写/覆盖一个窗口的断点 payload（幂等）。
func (d *DB) PutWindowCkpt(resumeKey, stage, winStart, winEnd, payload string) error {
	_, err := d.db.Exec(`INSERT INTO research_ckpts
		(resume_key, stage, win_start, win_end, payload, created_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(resume_key, stage, win_start, win_end)
		DO UPDATE SET payload=excluded.payload, created_at=excluded.created_at`,
		resumeKey, stage, winStart, winEnd, payload, time.Now().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("put ckpt: %w", err)
	}
	return nil
}

// DeleteWindowCkpts 清除某 resume_key 的全部断点（显式重算用；一般不调用，靠 key 轮换失效）。
func (d *DB) DeleteWindowCkpts(resumeKey string) error {
	_, err := d.db.Exec(`DELETE FROM research_ckpts WHERE resume_key=?`, resumeKey)
	return err
}
