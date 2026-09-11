// backtest_settings.go 回测增强配置的持久化载体（A0 管线定稿，替代 config.json 方案）。
//
// 单行 JSON 表（id 恒为 1）：store 只存原文字符串，类型与校验归 config 包
// （BacktestConfig/ValidateBacktest），避免 store→config 反向依赖。
// 无记录 = 增强不启用 = 引擎旧行为。
// English: single-row JSON settings for the backtest enhancement; the store keeps the raw
// payload while typing/validation live in the config package. No row = disabled = legacy engine.
package store

// GetBacktestSettings 读回测增强配置原文；无记录返回 ("", false, nil)。
func (d *DB) GetBacktestSettings() (string, bool, error) {
	var cfg string
	err := d.db.QueryRow(`SELECT config_json FROM backtest_settings WHERE id = 1`).Scan(&cfg)
	if err != nil {
		return "", false, nil // not found → false（无记录=旧行为）
	}
	return cfg, true, nil
}

// SetBacktestSettings 单行 UPSERT 保存回测增强配置（幂等，重复保存覆盖同一行）。
func (d *DB) SetBacktestSettings(cfgJSON string) error {
	_, err := d.db.Exec(`INSERT INTO backtest_settings (id, config_json, updated_at)
		VALUES (1, ?, datetime('now','localtime'))
		ON CONFLICT(id) DO UPDATE SET
			config_json = excluded.config_json, updated_at = excluded.updated_at`, cfgJSON)
	return err
}
