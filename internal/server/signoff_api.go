// signoff_api.go — §DYN-SIZE v4 / §ANCHOR-FIRST 的**签字面**：dry-run 报告与签字后落盘（2026-10-26）。
//
// 为什么这两件事要单独成文件、而且要"先报告后落盘"：
//   - v4 改的是 `sizing.mode` 与 `max_total_position_pct`（§1.7 owner 裁决本批切 power_mean +
//     天花板抬到 1.00）。这两键被旧 DefaultSizingConfig **物化进过账号键**，只改代码默认值
//     对存量载荷无效；而自动改写资金参数必须先让人看到前后值。
//   - 锚点回填的第三档（cost_fallback）把含费摊薄成本写成卖出裁决的基准，是风控线的位移。
//     账本加载时只自动补"层账可证的首买价"，成本档必须走这里签字后的一次显式调用。
//
// 所以两件事都是同一个形状：GET 出报告（只读，任何时刻可查、可反复查），POST 才写。
// POST 还要求 body 里带 `authorize` 字段并等于后端常量——不是"防误触的二次确认框"，
// 而是**把签字这件事留在服务端日志与订单留痕之外的一层**：谁在什么时候确认了哪一版报告，
// 事后能从 access log 与 config 快照里对上号（§0926E2E-W1B 的"写操作必须能自证"同族）。
//
// 报告本身不缓存、不排队：每次 GET 都按当前载荷重算。把 dry-run 报告存成文件再读，
// 就会出现"报告是昨天的、落盘吃的是今天的配置"这种对不上账的签字。
//
// English: the sign-off surface. GET reports are always recomputed from the live payloads
// (never cached — a stored report plus a live write is how signatures stop matching reality),
// and POST requires the literal authorize token before anything is written.
package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// v4SignoffToken 是签字落盘要求的 authorize 值。写成常量而不是"任意非空"：
// 面板按钮要显式带上这个词，等于把"我看过报告了"这句人类语言留在请求里，
// 而不是一句可以在 UI 上不小心点到的"确认"。
const v4SignoffToken = "SIZING-V4-SIGNED"

// SignoffV4Token 是上面那枚 token 的导出版，给启动链（cmd/quant 的环境变量判据）与部署脚本用。
// 只允许一处定义：env 路径与接口路径若各写一个字面量，改了其中一个就会出现
// "环境变量签了但接口说没签字"（或反之）——资金参数的授权判定不许有第二套口径。
// English: the exported alias of the same token, so the startup env gate and the HTTP gate can never
// drift into two different definitions of "signed".
const SignoffV4Token = v4SignoffToken

// handleSizingV4Plan 处理 GET /api/config/sizing-v4-plan（admin）：v4 迁移的 dry-run 报告。
// apply 恒为 false——这个接口**永不写盘**，落盘走 POST /api/config/sizing-v4-apply。
// English: GET /api/config/sizing-v4-plan (admin) — the read-only migration report.
func (s *Server) handleSizingV4Plan(w http.ResponseWriter, r *http.Request) {
	rep := s.cfg.MigrateSizingToV4(s.sizingAccountIDs(), false)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"report":        rep,
		"authorize_key": v4SignoffToken, // 前端按钮要把这个值原样带进 POST，报告里先说清楚
		"note": "dry-run 只读不写；签字后请调用 POST /api/config/sizing-v4-apply" +
			"（body 带 authorize）。迁移只动逐字等于旧物化默认的两键（mode=product、" +
			"max_total_position_pct=0.80），键缺失与 owner 手调值一律不碰。",
	})
}

// handleSizingV4Apply 处理 POST /api/config/sizing-v4-apply（admin）：按报告落盘。
//
// 只认 body 里 authorize == 常量这一种签字形态；不匹配一律 400 并回报告，不静默忽略。
// 落盘后立刻把新报告回给前端（第二次跑必须判 unchanged），这样"写了但没生效"当场就能看出来。
// English: POST /api/config/sizing-v4-apply (admin) — writes only with the exact authorize token,
// then re-runs the read-only pass so the response itself proves idempotency.
func (s *Server) handleSizingV4Apply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Authorize string `json:"authorize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Authorize) != v4SignoffToken {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":         "未签字：需要 authorize=\"" + v4SignoffToken + "\"（请先看 GET /api/config/sizing-v4-plan 的前后值报告）",
			"authorize_key": v4SignoffToken,
		})
		return
	}
	ids := s.sizingAccountIDs()
	applied := s.cfg.MigrateSizingToV4(ids, true)
	after := s.cfg.MigrateSizingToV4(ids, false) // 复跑一遍只读：签字后必须没有遗留可迁项
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"applied":    applied,
		"recheck":    after,
		"residual":   after.Changed, // >0 说明有载荷写失败（Failures 里有逐条原因），不能报"迁移完成"
		"note":       "residual>0 表示仍有可迁载荷未落盘，请看 applied.rows 里的 error 字段",
		"hot_synced": s.syncPaperSizingAfterMigration(), // 迁完让模拟盘立刻吃新口径，不等下一次配置保存
	})
}

// handleAnchorBackfillPlan 处理 GET /api/paper/anchor-backfill-plan（admin）：
// 存量锚点覆盖率 + 四级回填计划的**前后触发价**，逐票列出。只读，不动账本。
// English: GET /api/paper/anchor-backfill-plan (admin) — coverage plus the per-position
// before/after trigger prices of the four-tier backfill plan; read-only.
func (s *Server) handleAnchorBackfillPlan(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(s.operatorID())
	if pe == nil {
		writeError(w, http.StatusServiceUnavailable, "模拟盘引擎未接入")
		return
	}
	lookup := s.firstBuyPriceLookup()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"book":               "paper",
		"coverage":           pe.AnchorCoverage(lookup),
		"plan":               pe.AnchorBackfillPlan(lookup, true),
		"authorize_key":      v4SignoffToken,
		"load_time_behavior": "账本加载时只自动补层账首买价（first_lot）；成本档（cost_fallback）与买入历史档必须走本接口签字",
		"note": "回填后 source=cost_fallback / none 的持仓不得开做 T 腿（AnchorTicket 已按此收口）；" +
			"签字落盘请调用 POST /api/paper/anchor-backfill（body 带 authorize）。",
	})
}

// handleAnchorBackfillApply 处理 POST /api/paper/anchor-backfill（admin）：签字后的显式回填。
//
// 回填本体（paper.BackfillAnchors）是幂等的：已有锚点的行永不覆盖，所以重复调用只会在
// "确实还缺锚点"的行上继续补。响应里把回填数与最新覆盖率一起给出——
// 只报"成功"而不报"补了几条、还差几条"就是本项目反复点名过的降级报成功。
// English: POST /api/paper/anchor-backfill (admin) — the signed backfill; the response carries
// counts and the post-backfill coverage instead of a bare "ok".
func (s *Server) handleAnchorBackfillApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Authorize string `json:"authorize"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求体解析失败: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Authorize) != v4SignoffToken {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":         "未签字：需要 authorize=\"" + v4SignoffToken + "\"（请先看 GET /api/paper/anchor-backfill-plan 的前后触发价）",
			"authorize_key": v4SignoffToken,
		})
		return
	}
	pe := s.paperEngineFor(s.operatorID())
	if pe == nil {
		writeError(w, http.StatusServiceUnavailable, "模拟盘引擎未接入")
		return
	}
	lookup := s.firstBuyPriceLookup()
	filled, skipped := pe.BackfillAnchors(lookup)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"filled":     filled,
		"skipped":    skipped,
		"coverage":   pe.AnchorCoverage(lookup),
		"remaining":  pe.AnchorBackfillPlan(lookup, true), // 回填后仍缺锚点的行（正常应为空数组）
		"t_eligible": pe.TAnchorEligibleCount(),
		"note":       "cost_fallback/none 来源的持仓不计入 t_eligible（可判线但不开 T 腿）",
	})
}

// sizingAccountIDs 返回本次迁移要扫描的账号集合：全部已注册账号（含运营账号）。
// 从配置管理器取而不是从引擎注册表取：迁移发生在配置层，账号是否"已登录过"不影响载荷是否存在。
// English: every known account id — the migration is a config-layer concern, so it does not
// depend on whether an engine has been lazily created for the account.
func (s *Server) sizingAccountIDs() []string {
	if s.auth == nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	for _, u := range s.auth.ListUsers() {
		if u.ID == "" || seen[u.ID] {
			continue
		}
		seen[u.ID] = true
		out = append(out, u.ID)
	}
	return out
}

// firstBuyPriceLookup 给锚点回填提供第四档之前的"买入历史最早成交价"。
// 账本引擎自己不读实盘库（paper 是纯核算），所以这一档必须由 server 侧注入回调。
// 取不到实盘库（未启用 QMT/无账本）时返回 nil 回调，回填自然退到层账/成本两档。
// English: the paper book never reads the live DB, so the first-buy tier is injected as a callback;
// nil when there is no live ledger, which degrades the ladder to lot/cost exactly as before.
func (s *Server) firstBuyPriceLookup() func(code string) (float64, bool) {
	db := s.realDB()
	if db == nil {
		return nil
	}
	return func(code string) (float64, bool) {
		return db.EarliestBuyFillPrice(code)
	}
}

// syncPaperSizingAfterMigration 在迁移落盘后把新口径推进模拟盘引擎（等价于面板改完一次配置）。
// 模拟盘是操作员级共享账本，所以只有操作员那一档 sizing 会影响撮合引擎；返回它是否真被同步。
// 为什么必须带着这一步：迁移写了盘而引擎还在吃旧配置，就是"改完要重启才生效"那一类老坑，
// 而这次改的是资金参数——旧口径多跑一轮就是一次真实的下单差异。
// English: after the migration lands, push the new caliber into the operator's shared paper book
// so the money parameters take effect now rather than on the next restart.
func (s *Server) syncPaperSizingAfterMigration() bool {
	uid := s.operatorID()
	if uid == "" {
		return false
	}
	s.syncPaperSizing(uid)
	sc, _ := s.cfg.GetSizingConfigFor(uid)
	return sc.ModeOf() == "power_mean"
}
