// sweep_pool_configs.go 各战法独立寻优参数池的持久化配置（§OPTIMIZE_POOL_INTEGRATION_PLAN D1）。
//
// 每个战法一行：止盈线/止损线/持仓天数/门槛分数 四个维度的 from/to/step 步进搜索空间。
// 未配置的战法走代码内置默认池（btreplay.strategyPools）；PUT 时服务端计算组合数并执行
// 护栏校验（单战法组合总数上限），前端实时预估展示。
//
// English: per-strategy sweep search-space configs (four stepping dimensions). Strategies without
// a row fall back to code defaults; the API validates combo-count guardrails on write.
package store

import (
	"fmt"
	"time"
)

// SweepPoolConfig 单战法的四维步进搜索空间。
type SweepPoolConfig struct {
	Strategy string  `json:"strategy"` // 战法显示名（与 adapter.Name() / 排名行 strategy 一致）
	TpFrom   float64 `json:"tp_from"`  // 止盈线起点%
	TpTo     float64 `json:"tp_to"`    // 止盈线终点%（含）
	TpStep   float64 `json:"tp_step"`  // 止盈线步长
	SlFrom   float64 `json:"sl_from"`  // 止损起始
	SlTo     float64 `json:"sl_to"`    // 止损结束
	SlStep   float64 `json:"sl_step"`  // 止损步长
	// 持仓天数回归搜索维度（§D）：组合的 max_hold 即该维取值，兜底语义由各维取值承担
	HoldFrom int `json:"hold_from"` // 持仓起始
	HoldTo   int `json:"hold_to"`   // 持仓结束
	HoldStep int `json:"hold_step"` // 持仓步长
	// 门槛分数维：0~100；无连续分的战法引擎自动跳过该维（只跑 0 档）
	ScoreFrom float64 `json:"score_from"` // 评分起始
	ScoreTo   float64 `json:"score_to"`   // 评分结束
	ScoreStep float64 `json:"score_step"` // 评分步长

	UpdatedAt string `json:"updated_at"` // 更新时间
}

// ComboCount 计算该配置的组合总数（四维档数乘积；任一维非法按 1 档计）。
func (c *SweepPoolConfig) ComboCount() int {
	steps := func(from, to, step float64) int {
		if step <= 0 || to < from {
			return 1
		}
		return int((to-from)/step+0.5) + 1
	}
	hSteps := func(from, to, step int) int {
		if step <= 0 || to < from {
			return 1
		}
		return (to-from)/step + 1
	}
	return steps(c.TpFrom, c.TpTo, c.TpStep) *
		steps(c.SlFrom, c.SlTo, c.SlStep) *
		hSteps(c.HoldFrom, c.HoldTo, c.HoldStep) *
		steps(c.ScoreFrom, c.ScoreTo, c.ScoreStep)
}

// sweepPoolMaxCombos 单战法组合总数硬护栏（超出拒绝保存，提示放宽步长）。
// 分批锦标赛保证任意规模都能算（批 ≤5000 全量模拟后批冠军 PK），
// 此护栏仅防误填出天量任务（如步长 0.1 把网格放大百倍）。
const sweepPoolMaxCombos = 100000

// Validate 护栏校验：步长正、范围有序、组合数不超上限。返回人话错误信息。
func (c *SweepPoolConfig) Validate() error {
	pos := func(from, to, step float64, name string) error {
		if step <= 0 {
			return fmt.Errorf("%s 步长必须大于 0", name)
		}
		if to < from {
			return fmt.Errorf("%s 终点不能小于起点", name)
		}
		return nil
	}
	if err := pos(c.TpFrom, c.TpTo, c.TpStep, "止盈线"); err != nil {
		return err
	}
	if err := pos(c.SlFrom, c.SlTo, c.SlStep, "止损线"); err != nil {
		return err
	}
	if err := pos(c.ScoreFrom, c.ScoreTo, c.ScoreStep, "门槛分数"); err != nil {
		return err
	}
	if c.HoldStep <= 0 || c.HoldTo < c.HoldFrom {
		return errSprintf("持仓天数 步长/范围非法")
	}
	if n := c.ComboCount(); n > sweepPoolMaxCombos {
		return errSprintf("组合数 %d 超上限 %d——请放宽步长缩小搜索空间", n, sweepPoolMaxCombos)
	}
	return nil
}

// errSprintf 轻量错误构造（保持本文件错误信息风格统一）。
func errSprintf(s string, args ...any) error { return fmt.Errorf(s, args...) }

// GetSweepPoolConfig 读单战法配置；不存在返回 nil（调用方回退默认池）。
func (d *DB) GetSweepPoolConfig(strategy string) (*SweepPoolConfig, error) {
	row := d.db.QueryRow(`SELECT strategy, tp_from, tp_to, tp_step,
		sl_from, sl_to, sl_step, hold_from, hold_to, hold_step,
		score_from, score_to, score_step, updated_at
		FROM sweep_pool_configs WHERE strategy = ?`, strategy)
	var c SweepPoolConfig
	err := row.Scan(&c.Strategy, &c.TpFrom, &c.TpTo, &c.TpStep,
		&c.SlFrom, &c.SlTo, &c.SlStep, &c.HoldFrom, &c.HoldTo, &c.HoldStep,
		&c.ScoreFrom, &c.ScoreTo, &c.ScoreStep, &c.UpdatedAt)
	if err != nil {
		return nil, nil // not found → nil,nil（调用方回退默认池）
	}
	return &c, nil
}

// ListSweepPoolConfigs 全部已配置战法。
func (d *DB) ListSweepPoolConfigs() ([]*SweepPoolConfig, error) {
	rows, err := d.db.Query(`SELECT strategy, tp_from, tp_to, tp_step,
		sl_from, sl_to, sl_step, hold_from, hold_to, hold_step,
		score_from, score_to, score_step, updated_at
		FROM sweep_pool_configs ORDER BY strategy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SweepPoolConfig{}
	for rows.Next() {
		var c SweepPoolConfig
		if err := rows.Scan(&c.Strategy, &c.TpFrom, &c.TpTo, &c.TpStep,
			&c.SlFrom, &c.SlTo, &c.SlStep, &c.HoldFrom, &c.HoldTo, &c.HoldStep,
			&c.ScoreFrom, &c.ScoreTo, &c.ScoreStep, &c.UpdatedAt); err == nil {
			out = append(out, &c)
		}
	}
	return out, rows.Err()
}

// UpsertSweepPoolConfig 幂等写入（strategy 主键覆盖）。
func (d *DB) UpsertSweepPoolConfig(c *SweepPoolConfig) error {
	now := time.Now().Format("2006-01-02 15:04:05")
	_, err := d.db.Exec(`INSERT INTO sweep_pool_configs
		(strategy, tp_from, tp_to, tp_step, sl_from, sl_to, sl_step,
		 hold_from, hold_to, hold_step, score_from, score_to, score_step, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(strategy) DO UPDATE SET
			tp_from=excluded.tp_from, tp_to=excluded.tp_to, tp_step=excluded.tp_step,
			sl_from=excluded.sl_from, sl_to=excluded.sl_to, sl_step=excluded.sl_step,
			hold_from=excluded.hold_from, hold_to=excluded.hold_to, hold_step=excluded.hold_step,
			score_from=excluded.score_from, score_to=excluded.score_to, score_step=excluded.score_step,
			updated_at=excluded.updated_at`,
		c.Strategy, c.TpFrom, c.TpTo, c.TpStep, c.SlFrom, c.SlTo, c.SlStep,
		c.HoldFrom, c.HoldTo, c.HoldStep, c.ScoreFrom, c.ScoreTo, c.ScoreStep, now)
	return err
}

// UpdateOptimizationGrid 把某战法冠军行的止盈×止损热力网格 JSON 回写（§D3 前端渲染源）。
// 该行由 (task_id, strategy) 定位——每战法每任务恰好一条冠军行。
func (d *DB) UpdateOptimizationGrid(taskID int64, strategy, gridJSON string) error {
	_, err := d.db.Exec(`UPDATE optimization_results SET grid_json = ?
		WHERE task_id = ? AND strategy = ?`, gridJSON, taskID, strategy)
	return err
}
