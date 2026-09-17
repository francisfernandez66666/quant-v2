// paper_config.go — §F-4（20260917 缺陷修复批）模拟盘撮合配置读写端点。
// 背景：rules.paper.enabled/自动卖出/单笔资金/做空预算等此前只在进程启动时装配一次
// （cmd/quant/main.go），Registry.SetPaperConfig 热同步函数零调用（死代码），改配置必须
// 重启 quant；且实盘有 /api/config/qmt 开关而模拟盘没有任何开关端点。
// 本文件补齐：GET 读当前配置（含引擎实时生效值回显）+ POST 局部更新（指针字段语义，
// 与 /api/config/qmt 一致）并立即热同步全局引擎 + 注册表模板与所有账号引擎。
// English: §F-4 — paper matching-config read/write endpoints. rules.paper was only read once at
// process start (Registry.SetPaperConfig sat dead with zero callers); these handlers persist
// pointer-field updates and hot-apply them to the global engine, the registry template and every
// per-account engine immediately — same partial-update contract as /api/config/qmt.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/paper"
)

// paperConfigView 模拟盘撮合配置的响应视图（rules 层字段 + 引擎实时值）。
type paperConfigView struct {
	Enabled      bool    `json:"enabled"`       // 总开关（rules 层）
	AutoSell     bool    `json:"auto_sell"`     // 自动卖出（nil=开，回显归一后值）
	FixedAmount  float64 `json:"fixed_amount"`  // 每票固定买入资金
	MaxPositions int     `json:"max_positions"` // 持仓上限
	// 初始资金（新清盘重置基准；不影响既有账本现金）
	InitialCapital float64 `json:"initial_capital"`
	ShortEnabled   bool    `json:"short_enabled"`    // 融券开关（归一后值）
	ShortCapital   float64 `json:"short_capital"`    // 做空池预算（0=整侧关闭）
	EngineEnabled  bool    `json:"engine_enabled"`   // 引擎当前实际生效值（热更新回显）
	EngineAutoSell bool    `json:"engine_auto_sell"` // 引擎当前自动卖出实况
}

// handleGetPaperConfig 处理 GET /api/paper/config：返回本人 rules.paper 快照与引擎实况。
func (s *Server) handleGetPaperConfig(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	if s.cfg == nil {
		writeError(w, http.StatusServiceUnavailable, "配置未接入")
		return
	}
	var v paperConfigView
	// rules 层快照：AutoSell/ShortEnabled 的 nil 按"开"回显（与引擎装配缺省语义一致，
	// 前端拿到的永远是布尔值，不再二次判 nil）。
	if rules := s.cfg.GetRulesFor(uid); rules != nil {
		v = paperConfigView{
			Enabled:        rules.Paper.Enabled,
			AutoSell:       rules.Paper.AutoSell == nil || *rules.Paper.AutoSell,
			ShortEnabled:   rules.Paper.ShortEnabled == nil || *rules.Paper.ShortEnabled,
			FixedAmount:    rules.Paper.FixedAmount,
			MaxPositions:   rules.Paper.MaxPositions,
			InitialCapital: rules.Paper.InitialCapital,
			ShortCapital:   rules.Paper.ShortCapital,
		}
	}
	if pe := s.paperEngineFor(uid); pe != nil {
		v.EngineEnabled, v.EngineAutoSell = pe.Enabled(), pe.Cfg().AutoSell
	}
	writeJSON(w, 200, v)
}

// setPaperConfigReq POST /api/paper/config 请求体：指针字段=本次要改的，nil=保持原值
// （与 setPaperStrategiesReq /api/config/qmt 同契约）。
type setPaperConfigReq struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	AutoSell       *bool    `json:"auto_sell,omitempty"`
	FixedAmount    *float64 `json:"fixed_amount,omitempty"`
	MaxPositions   *int     `json:"max_positions,omitempty"`
	InitialCapital *float64 `json:"initial_capital,omitempty"`
	ShortEnabled   *bool    `json:"short_enabled,omitempty"`
	ShortCapital   *float64 `json:"short_capital,omitempty"`
}

// handleSetPaperConfig 处理 POST /api/paper/config（admin）：校验→局部落库→即时热同步。
// 热同步三层：全局模板引擎 s.paper、注册表 opts.Paper 模板（新懒加载账号继承）、
// 所有已创建账号引擎（Registry.SetPaperConfig 自本批起从死代码转正）。
// 说明：持仓上限/资金池的分池口径仍走 /api/paper/pool/config（本端点只管账户级参数）；
// 战法白名单走 /api/paper/strategies；这里改 initial_capital 不回溯既有账本。
func (s *Server) handleSetPaperConfig(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	if s.cfg == nil {
		writeError(w, http.StatusServiceUnavailable, "配置未接入")
		return
	}
	var req setPaperConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "无效请求体")
		return
	}
	// 落库前的四项数值参数校验：负值一律 400 拒绝（fixed_amount/初始资金为金额、
	// max_positions 为数量、short_capital 为做空池预算，负数均无业务含义）。
	if req.FixedAmount != nil && *req.FixedAmount < 0 {
		writeError(w, http.StatusBadRequest, "fixed_amount 不能为负")
		return
	}
	if req.MaxPositions != nil && *req.MaxPositions < 0 {
		writeError(w, http.StatusBadRequest, "max_positions 不能为负")
		return
	}
	if req.InitialCapital != nil && *req.InitialCapital < 0 {
		writeError(w, http.StatusBadRequest, "initial_capital 不能为负")
		return
	}
	if req.ShortCapital != nil && *req.ShortCapital < 0 {
		writeError(w, http.StatusBadRequest, "short_capital 不能为负")
		return
	}
	// 局部落库：只覆盖本次显式携带的指针字段，未传字段保持原值（与 /api/config/qmt 同契约）。
	// SetPaperConfigFor 原子更新该账号 rules.paper 并持久化到 config.json。
	s.cfg.SetPaperConfigFor(uid, func(p *config.PaperConfig) {
		if req.Enabled != nil { // 总开关：立即决定引擎是否继续接收信号/撮合
			p.Enabled = *req.Enabled
		}
		if req.AutoSell != nil { // 自动卖出：指针直传，保留 nil（未配置=默认开）语义
			p.AutoSell = req.AutoSell
		}
		if req.FixedAmount != nil { // 每票固定买入资金（元）
			p.FixedAmount = *req.FixedAmount
		}
		if req.MaxPositions != nil { // 持仓上限（只约束新开仓，不减既有持仓）
			p.MaxPositions = *req.MaxPositions
		}
		if req.InitialCapital != nil { // 初始资金：仅作为新清盘的基准，不回溯既有账本现金
			p.InitialCapital = *req.InitialCapital
		}
		if req.ShortEnabled != nil { // 融券做空总开关（指针直传保留 nil 语义）
			p.ShortEnabled = req.ShortEnabled
		}
		if req.ShortCapital != nil { // 做空池预算（0=整侧关闭）
			p.ShortCapital = *req.ShortCapital
		}
	})
	// 热同步：以落库后的账号规则重建引擎配置（装配口径与 main.go 完全一致）。
	// ConfigFromRules 会把 AutoSell/ShortEnabled 的 nil 归一为默认值，得到引擎可直接消费的 Config。
	pc := paper.Config{}
	if rules := s.cfg.GetRulesFor(uid); rules != nil {
		pc = paper.ConfigFromRules(rules.Paper)
	}
	// 热同步第①层：全局模板引擎（旧单引擎路径）立即换用新配置，不动持仓/账本数据。
	if s.paperEngine() != nil {
		s.paperEngine().UpdateConfig(pc)
	}
	// 热同步第②层：注册表模板（Registry.SetPaperConfig 自 §F-4 起从死代码转正）——
	// 后续懒加载创建的账号引擎都继承这份配置；registry 内部会同步扩散到所有已创建账号引擎。
	if s.registry != nil {
		s.registry.SetPaperConfig(pc)
	}
	log.Printf("[paper] §F-4 账号 %s 撮合配置已热更新: enabled=%v auto_sell=%v fixed=%.0f max_pos=%d short_cap=%.0f",
		uid, pc.Enabled, pc.AutoSell, pc.FixedAmount, pc.MaxPositions, pc.ShortCapital)
	// 审计留痕（adminMiddleware 正常保证 user 非空；直调测试路径下防御 nil）。
	actor := ""
	if u := userFromContext(r); u != nil {
		actor = u.ID
	}
	opslog.Audit("paper_config", actor, uid,
		fmt.Sprintf("enabled=%v auto_sell=%v fixed_amount=%v max_positions=%v initial_capital=%v short=%v",
			req.Enabled, req.AutoSell, req.FixedAmount, req.MaxPositions, req.InitialCapital, req.ShortCapital))
	// 响应回显：以刚热更的引擎配置为准（pc 已是归一后的值），并叠加引擎实况
	// （engine_enabled/engine_auto_sell）供前端确认热更新是否已生效。
	var v paperConfigView
	v.Enabled, v.AutoSell, v.ShortEnabled = pc.Enabled, pc.AutoSell, pc.ShortEnabled
	v.FixedAmount, v.MaxPositions, v.InitialCapital, v.ShortCapital = pc.FixedAmount, pc.MaxPositions, pc.InitialCapital, pc.ShortCapital
	if pe := s.paperEngineFor(uid); pe != nil {
		v.EngineEnabled, v.EngineAutoSell = pe.Enabled(), pe.Cfg().AutoSell
	}
	writeJSON(w, 200, v)
}
