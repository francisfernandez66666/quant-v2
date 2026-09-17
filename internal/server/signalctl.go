// signalctl.go — §SIGNAL_CONTROLLER（20260917）信号控制器对外端点：
//   - GET  /api/signalctl/verdicts   最近裁定留痕（实盘/模拟盘两通道 pass/hold/block+原因，审计"为何没成交"）
//   - GET  /api/paper/strategies     模拟盘战法白名单当前值 + 已知战法全集（与 /api/config/qmt 同构）
//   - POST /api/paper/strategies     保存模拟盘战法白名单/黑名单（热同步资金池模板）
//
// English: HTTP surface for the unified signal controller — a verdict audit endpoint plus the
// paper-side strategy whitelist endpoints (mirroring the live whitelist contract).
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/signalctl"
)

// handleSignalVerdicts 处理 GET /api/signalctl/verdicts?limit=50：聚合注册表内全部引擎的
// 信号控制器裁定留痕（最新在前）。供前端"信号裁定"面板与排障 CLI 使用。
// English: merges each engine's controller verdict tail, newest first, for the audit panel.
func (s *Server) handleSignalVerdicts(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	var all []signalctl.Decision
	if s.registry != nil {
		for _, c := range s.registry.AllControllers() {
			all = append(all, c.SignalVerdicts(limit)...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].At.After(all[j].At) })
	if len(all) > limit {
		all = all[:limit]
	}
	if all == nil {
		all = []signalctl.Decision{}
	}
	writeJSON(w, 200, map[string]any{"verdicts": all, "count": len(all)})
}

// paperStrategiesView 模拟盘战法开关面板响应（前端以 known_strategies 渲染开关组，
// strategies 为已列名集合；空=默认全集语义，动量不在默认全集内）。
// English: the paper strategy-panel payload (same shape as the live whitelist view).
type paperStrategiesView struct {
	Strategies      []string            `json:"strategies"`
	Blacklist       []string            `json:"blacklist"`
	ShadowBlacklist bool                `json:"shadow_blacklist"`
	Known           []knownStrategyInfo `json:"known_strategies"`
}

// handleGetPaperStrategies 处理 GET /api/paper/strategies。
func (s *Server) handleGetPaperStrategies(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	rules := s.cfg.GetRulesFor(uid)
	view := paperStrategiesView{Known: s.knownStrategyList(), ShadowBlacklist: true}
	if rules != nil {
		view.Strategies = rules.Paper.Strategies
		view.Blacklist = rules.Paper.Blacklist
		view.ShadowBlacklist = rules.SignalCtl.BlacklistShadow()
	}
	if view.Strategies == nil {
		view.Strategies = []string{}
	}
	if view.Blacklist == nil {
		view.Blacklist = []string{}
	}
	writeJSON(w, 200, view)
}

// setPaperStrategiesReq 局部更新：指针字段=本次要改的，nil=保持原值（与 /api/config/qmt 同契约）。
type setPaperStrategiesReq struct {
	Strategies *[]string `json:"strategies"`
	Blacklist  *[]string `json:"blacklist"`
}

// handleSetPaperStrategies 处理 POST /api/paper/strategies：校验战法 ID 合法后落账号规则快照，
// 并热同步资金池模板（动量显式列名才开立动量池，§SIGNAL_CONTROLLER P3）。
// 空 strategies = 恢复默认全集语义（内置四形态+库规则；动量需显式开启）。
// English: validates against the known-strategy set, persists, and hot-syncs the pool template.
func (s *Server) handleSetPaperStrategies(w http.ResponseWriter, r *http.Request) {
	uid := userIDFor(r)
	if s.cfg == nil {
		writeError(w, 503, "配置未接入")
		return
	}
	var req setPaperStrategiesReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "无效请求体")
		return
	}
	rules := s.cfg.GetRulesFor(uid)
	cur, curBL := []string{}, []string{}
	if rules != nil {
		cur = append(cur, rules.Paper.Strategies...)
		curBL = append(curBL, rules.Paper.Blacklist...)
	}
	known := s.knownStrategyIDSet()
	if req.Strategies != nil {
		seen := map[string]bool{}
		out := make([]string, 0, len(*req.Strategies))
		for _, v := range *req.Strategies {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			if !known[v] {
				writeError(w, 400, "未知战法: "+v)
				return
			}
			seen[v] = true
			out = append(out, v)
		}
		cur = out
	}
	if req.Blacklist != nil {
		seen := map[string]bool{}
		out := make([]string, 0, len(*req.Blacklist))
		for _, v := range *req.Blacklist {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
		curBL = out
	}
	s.cfg.SetPaperStrategyFor(uid, cur, curBL)
	// 资金池模板热同步：动量显式列名才开池（分仓守恒在 SetStrategyPools 内处理）。
	if s.registry != nil {
		s.registry.SetPaperPools(ActivePaperPoolTypes(s.researchDir, s.paperStrategiesForOperator()))
	}
	log.Printf("[signalctl] 账号 %s 模拟盘战法白名单已保存: strategies=%v blacklist=%v", uid, cur, curBL)
	opslog.Logf("quant", "模拟盘战法开关保存 账号=%s 允许=%v", uid, cur)
	writeJSON(w, 200, map[string]any{"status": "ok", "strategies": cur, "blacklist": curBL})
}
