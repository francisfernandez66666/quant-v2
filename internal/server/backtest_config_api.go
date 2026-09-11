// backtest_config_api.go 回测增强配置 API（A0.1b 管线服务端）。
//
// GET /api/research/backtest-config   读取当前配置（无记录返回 {config:null}）
// PUT /api/research/backtest-config   保存（先 config.ValidateBacktest，非法 400）
//
// 持久化于研究库 backtest_settings 单行 JSON；入队点（手动寻优/夜间 optimize/战法回放）
// 经 injectBacktestPayload 注入 payload.backtest，enabled 才注入。
// English: GET/PUT endpoints for the backtest enhancement settings (single-row JSON in the
// research DB); enqueue points inject payload.backtest only when enabled.
package server

import (
	"encoding/json"
	"net/http"

	"quant-trading-v2/internal/config"
)

// handleBacktestConfigGet 处理 GET /api/research/backtest-config。
func (s *Server) handleBacktestConfigGet(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, http.StatusServiceUnavailable, "研究库未接入")
		return
	}
	raw, ok, err := s.researchDB.GetBacktestSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeJSON(w, 200, map[string]any{"config": nil, "enabled": false})
		return
	}
	// 原文透出不做强校验（历史脏数据也要能展示/覆盖保存）
	var cfg map[string]any
	if jerr := json.Unmarshal([]byte(raw), &cfg); jerr != nil {
		writeJSON(w, 200, map[string]any{"config_raw": raw, "enabled": false,
			"parse_error": jerr.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"config": cfg, "enabled": cfg["enabled"] == true})
}

// handleBacktestConfigPut 处理 PUT /api/research/backtest-config：类型化解析+越界校验后落库。
func (s *Server) handleBacktestConfigPut(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, http.StatusServiceUnavailable, "研究库未接入")
		return
	}
	var cfg config.BacktestConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "请求体非法: "+err.Error())
		return
	}
	// 保存入口统一校验（Enabled=false 也校验，防半坏配置后续启用踩坑）
	if err := config.ValidateBacktest(&cfg); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	out, err := json.Marshal(&cfg)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := s.researchDB.SetBacktestSettings(string(out)); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": "saved", "enabled": cfg.Enabled})
}

// injectBacktestPayload 读 backtest_settings 并在任务 payload 里注入 backtest 段：
// 无记录/解析失败/enabled=false 一律不注入（引擎侧 = nil = 旧行为，前向兼容）。
// 注入端顺带把单笔名义额解析为显式数值——btreplay 固定出厂默认配置、拿不到用户
// rules.paper.fixed_amount（管线断链），只能在有真实配置句柄的 server/worker 侧解析。
// English: reads backtest_settings and injects payload.backtest only when enabled, resolving
// order_value_yuan from the live paper config (the engine process cannot read it).
func (s *Server) injectBacktestPayload(p map[string]any) {
	if s.researchDB == nil || p == nil {
		return
	}
	raw, ok, err := s.researchDB.GetBacktestSettings()
	if !ok || err != nil {
		return
	}
	var cfg config.BacktestConfig
	if json.Unmarshal([]byte(raw), &cfg) != nil || !cfg.Enabled {
		return
	}
	// 名义额与模拟盘同源（缺省经 s.cfg 解析 fixed_amount；无配置句柄时留 0 走引擎兜底 10000）
	if cfg.OrderValueYuan <= 0 && s.cfg != nil {
		cfg.OrderValueYuan = s.cfg.Get().Paper.FixedAmount
	}
	p["backtest"] = cfg
}
