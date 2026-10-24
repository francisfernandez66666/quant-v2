// B5 研究候选审批端点：GET 候选列表 / POST 审批(应用) / POST 驳回。
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"quant-trading-v2/internal/store"
	"strconv"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/research"
	"time"
)

// handleResearchFactors 处理 GET /api/research/factors：返回全部已注册因子的元数据
// （ID / 中文名 / 大类 / 一句话解释），供前端自动研究页把因子规则渲染成可读文案。
// 由后端 `factor` 库直接提供，前端无需硬编码维护两份映射。
// （handleResearchFactors serves GET /api/research/factors — metadata for every registered factor
// (ID / Chinese name / category / one-line description), so the auto-research page can render factor
// rules as readable text. Sourced directly from the factor registry; the frontend keeps no duplicate map.）
func (s *Server) handleResearchFactors(w http.ResponseWriter, r *http.Request) {
	defs := factor.All()
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			"id":   d.ID,
			"name": d.Name,
			"cat":  d.Cat.CategoryName(),
			"desc": d.Desc,
		})
	}
	writeJSON(w, 200, map[string]any{"factors": out})
}

// handleResearchProgress 处理 GET /api/research/progress：返回研究库数据加载与研究任务进度。
// 统计：股票总数、行情覆盖天数/行数、财务覆盖、候选数、研究池可用股票数。
// 前端自动研究页据此渲染进度条（数据准备度）与候选产出状态。
// （handleResearchProgress serves GET /api/research/progress — the research DB data-loading and
// research-task progress: stock counts, daily coverage, financial coverage, candidate counts and
// how many stocks are research-ready. The auto-research page renders progress bars from this.）
// §2026-10-24 研究进度快照：接口含多次全表统计（daily 数百万行），冷缓存可达数十秒。
// 处理原则：每 Server 独立缓存（不用包级全局，避免测试/多实例串台）+ 落盘快照 +
// stale-while-revalidate + 单飞重算。进度只用于展示，不参与交易决策，允许短时间旧值。
const researchProgressTTL = 5 * time.Minute

// researchProgressSnapshotFile 快照文件名（研究目录下；无 researchDir 时不落盘）。
const researchProgressSnapshotFile = "research_progress_snapshot.json"

// researchProgressSnapshot 落盘结构。
type researchProgressSnapshot struct {
	AsOf time.Time      `json:"as_of"`
	Body map[string]any `json:"body"`
}

// researchProgressPath 快照路径；未配置研究目录返回空串。
func (s *Server) researchProgressPath() string {
	if s.researchDir == "" {
		return ""
	}
	return filepath.Join(s.researchDir, researchProgressSnapshotFile)
}

// loadResearchProgressSnapshot 读取落盘快照；缺失/损坏返回 false。
func (s *Server) loadResearchProgressSnapshot() (map[string]any, time.Time, bool) {
	p := s.researchProgressPath()
	if p == "" {
		return nil, time.Time{}, false
	}
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) == 0 {
		return nil, time.Time{}, false
	}
	var snap researchProgressSnapshot
	if json.Unmarshal(raw, &snap) != nil || snap.Body == nil {
		return nil, time.Time{}, false
	}
	at := snap.AsOf
	if at.IsZero() {
		if st, serr := os.Stat(p); serr == nil {
			at = st.ModTime()
		}
	}
	return snap.Body, at, true
}

// saveResearchProgressSnapshot 原子写快照（临时文件 + rename），失败只记日志。
func (s *Server) saveResearchProgressSnapshot(body map[string]any, at time.Time) {
	p := s.researchProgressPath()
	if p == "" || body == nil {
		return
	}
	raw, err := json.MarshalIndent(researchProgressSnapshot{AsOf: at, Body: body}, "", " ")
	if err != nil {
		log.Printf("[research] progress 快照序列化失败: %v", err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		log.Printf("[research] progress 快照写入失败: %v", err)
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		log.Printf("[research] progress 快照替换失败: %v", err)
		_ = os.Remove(tmp)
	}
}

// progressResponse 复制一份响应并附上快照时间/是否旧值（增量字段，前端可忽略）。
func progressResponse(body map[string]any, at time.Time, stale bool) map[string]any {
	out := make(map[string]any, len(body)+2)
	for k, v := range body {
		out[k] = v
	}
	if !at.IsZero() {
		out["as_of"] = at.Format(time.RFC3339)
	}
	out["stale"] = stale
	return out
}

// startResearchProgressCompute 单飞启动一次后台重算；已有重算在跑时直接返回。
func (s *Server) startResearchProgressCompute() {
	if s.researchDB == nil {
		return
	}
	s.researchProgressMu.Lock()
	if s.researchProgressComputing {
		s.researchProgressMu.Unlock()
		return
	}
	if s.researchProgressBody != nil && time.Since(s.researchProgressAt) < researchProgressTTL {
		s.researchProgressMu.Unlock()
		return
	}
	done := make(chan struct{})
	s.researchProgressComputing = true
	s.researchProgressDone = done
	s.researchProgressMu.Unlock()

	go func() {
		body, err := s.computeResearchProgress()
		s.finishResearchProgressCompute(body, err, done)
	}()
}

// finishResearchProgressCompute 收口一次重算：成功才更新缓存并落盘；无论成败都释放单飞闸。
func (s *Server) finishResearchProgressCompute(body map[string]any, err error, done chan struct{}) {
	now := time.Now()
	s.researchProgressMu.Lock()
	s.researchProgressComputing = false
	if err == nil && body != nil {
		s.researchProgressBody = body
		s.researchProgressAt = now
	}
	if s.researchProgressDone == done {
		close(done)
		s.researchProgressDone = nil
	}
	s.researchProgressMu.Unlock()
	if err != nil {
		log.Printf("[research] progress 计算失败: %v", err)
		return
	}
	if body != nil {
		s.saveResearchProgressSnapshot(body, now)
	}
}

// WarmResearchProgress 启动后延迟预热一次进度快照（不阻塞启动）。
// 目的：避免部署后第一位打开自动研究页的用户吃到 40s 冷算；已有新鲜缓存则跳过。
func (s *Server) WarmResearchProgress(delay time.Duration) {
	if s.researchDB == nil {
		return
	}
	start := func() {
		s.researchProgressMu.Lock()
		fresh := s.researchProgressBody != nil && time.Since(s.researchProgressAt) < researchProgressTTL
		s.researchProgressMu.Unlock()
		if !fresh {
			s.startResearchProgressCompute()
		}
	}
	if delay <= 0 {
		start()
		return
	}
	time.AfterFunc(delay, start)
}

// handleResearchProgress 处理 GET /api/research/progress：返回研究库数据加载与研究任务进度。
// 统计：股票总数、行情覆盖天数/行数、财务覆盖、候选数、研究池可用股票数。
// 前端自动研究页据此渲染进度条（数据准备度）与候选产出状态。
func (s *Server) handleResearchProgress(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}

	// 1) 内存缓存：新鲜直接回；旧值先回并触发后台重算（stale-while-revalidate）。
	s.researchProgressMu.Lock()
	if body := s.researchProgressBody; body != nil {
		at := s.researchProgressAt
		if time.Since(at) < researchProgressTTL {
			s.researchProgressMu.Unlock()
			writeJSON(w, 200, progressResponse(body, at, false))
			return
		}
		s.researchProgressMu.Unlock()
		s.startResearchProgressCompute()
		writeJSON(w, 200, progressResponse(body, at, true))
		return
	}
	s.researchProgressMu.Unlock()

	// 2) 没有内存值：尝试落盘快照；有旧快照先回，再后台重算。
	if body, at, ok := s.loadResearchProgressSnapshot(); ok {
		s.researchProgressMu.Lock()
		if s.researchProgressBody == nil {
			s.researchProgressBody = body
			s.researchProgressAt = at
		}
		body = s.researchProgressBody
		at = s.researchProgressAt
		s.researchProgressMu.Unlock()
		s.startResearchProgressCompute()
		writeJSON(w, 200, progressResponse(body, at, true))
		return
	}

	// 3) 既无缓存也无快照：若已有重算在跑，等它结束；否则同步算一次（首个请求仍可能慢，
	//    但结果会落盘，后续请求走快照/缓存；部署侧用 WarmResearchProgress 预热）。
	s.researchProgressMu.Lock()
	if s.researchProgressComputing {
		done := s.researchProgressDone
		s.researchProgressMu.Unlock()
		if done != nil {
			<-done
		}
		s.researchProgressMu.Lock()
		body := s.researchProgressBody
		at := s.researchProgressAt
		s.researchProgressMu.Unlock()
		if body != nil {
			writeJSON(w, 200, progressResponse(body, at, false))
			return
		}
		writeError(w, 500, "研究进度计算失败，请稍后重试")
		return
	}
	done := make(chan struct{})
	s.researchProgressComputing = true
	s.researchProgressDone = done
	s.researchProgressMu.Unlock()

	body, err := s.computeResearchProgress()
	s.finishResearchProgressCompute(body, err, done)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.researchProgressMu.Lock()
	at := s.researchProgressAt
	s.researchProgressMu.Unlock()
	writeJSON(w, 200, progressResponse(body, at, false))
}

// computeResearchProgress 真正的统计逻辑（纯计算，不写 ResponseWriter）。
// 所有错误向上返回，由 handler/异步重算决定如何呈现。
func (s *Server) computeResearchProgress() (map[string]any, error) {
	db := s.researchDB
	stocks, err := db.StockCodes()
	if err != nil {
		return nil, err
	}
	nStocks := len(stocks)

	// 行情覆盖：近一年有日线的股票数（研究可用的有效池）+ daily 总行数
	dailyRows, err := db.Count("daily", "")
	if err != nil {
		return nil, err
	}
	// 研究池：近一年（约 244 个交易日）有行情的股票数
	ready := 0
	if nStocks > 0 {
		ready, err = db.ReadyStockCount()
		if err != nil {
			return nil, err
		}
	}
	// 财务覆盖（近一年有财报的股票数，作为因子可用性代理）
	fin, err := db.Count("fina_indicator", "")
	if err != nil {
		return nil, err
	}
	// 候选产出
	cands, err := db.ListCandidates("")
	if err != nil {
		return nil, err
	}
	applied, proposed := 0, 0
	for _, c := range cands {
		switch c.Status {
		case "applied":
			applied++
		case "proposed":
			proposed++
		}
	}

	// 覆盖比例：研究池就绪 = 近一年有行情的股票 / 总股票
	readyPct := 0.0
	if nStocks > 0 {
		readyPct = float64(ready) / float64(nStocks)
	}
	log.Printf("[research] progress: stocks=%d ready=%d daily_rows=%d fin=%d cands=%d", nStocks, ready, dailyRows, fin, len(cands))
	// §数据源标识：前端徽标展示「同花顺（新）/ baostock」
	ds := "baostock"
	if s.cfg != nil {
		if ps := s.cfg.Get().Data.PrimarySource; ps == "hithink" { // §0925EVE-D1：字段裸读改加锁访问器
			ds = "同花顺（新）"
		}
	}
	// 汇总研究进度统计并返回：各数据源覆盖量、就绪比、候选/已应用数。
	return map[string]any{
		"stocks":       nStocks,
		"ready_stocks": ready,
		"ready_pct":    readyPct,
		"daily_rows":   dailyRows,
		"fin_rows":     fin,
		"candidates":   len(cands),
		"applied":      applied,
		"proposed":     proposed,
		"db_attached":  true,
		"data_source":  ds,
		"scheduler":    s.schedulerState(),
	}, nil
}

// handleSchedulerStatus 处理 GET /api/scheduler/status：读取 researchd 每 30s 写入的
// scheduler_status.json 可见性快照，让前端直接回答"为何卡排队"（禁用/交易时段/内存闸门/槽位占用/队列空）。
// 文件不存在时回退为"未知"，避免前端误判。English: returns researchd's visibility snapshot so the
// UI can explain why tasks are queued without server logs.
func (s *Server) handleSchedulerStatus(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.researchDir, "scheduler_status.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, 200, map[string]any{
			"ok":     false,
			"reason": "尚未获取到调度状态：researchd 未运行或未到首次写入（请确认 quant-research 服务已启动，且与 quant 共用同一 QUANT_DATA_DIR）",
		})
		return
	}
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "reason": "调度状态解析失败"})
		return
	}
	st["ok"] = true
	writeJSON(w, 200, st)
}

// handleResearchEventFactor 处理 GET /api/research/event-factor：返回 §ENH-4 事件因子检验
// 报告（cmd/research event-layers 落盘的 event_factor_report.json，FactorReport 数组）。
// 未生成时返回 {exists:false}，前端事件因子卡显示引导语，不报 500。
// English: serves the event-factor validation report produced by the research CLI.
func (s *Server) handleResearchEventFactor(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile(filepath.Join(s.researchDir, "event_factor_report.json"))
	if err != nil || len(raw) == 0 {
		writeJSON(w, 200, map[string]any{"exists": false})
		return
	}
	// 体积守卫：逐日 IC 行可能很大，超 4MB 视为异常文件，按未生成处理并透传标记。
	if len(raw) > 4*1024*1024 {
		writeJSON(w, 200, map[string]any{"exists": false, "too_large": true})
		return
	}
	// 重序列化前校验 JSON 合法性，防被外部工具写坏导致前端 JSON.parse 崩。
	var check any
	if json.Unmarshal(raw, &check) != nil {
		writeJSON(w, 200, map[string]any{"exists": false, "corrupt": true})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(raw)
}

// handleResearchTaskLog 处理 GET /api/research/task/{id}/log：读取 researchd 为每条研究任务
// 落盘的 task_logs/task_<id>.log（子进程 stdout/stderr），让前端直接查看回测/研究日志，
// 无需 SSH 翻服务器。文件不存在时返回 exists=false。English: serves the per-task log file so the
// UI can show backtest/research output without server access.
func (s *Server) handleResearchTaskLog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, 400, "missing id")
		return
	}
	logPath := filepath.Join(s.researchDir, "task_logs", "task_"+id+".log")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		writeJSON(w, 200, map[string]any{"exists": false, "log": ""})
		return
	}
	// 限制返回体积：超过 200KB 只取末尾，避免一次性拖垮前端/接口。
	const maxBytes = 200 * 1024
	if len(raw) > maxBytes {
		raw = raw[len(raw)-maxBytes:]
	}
	writeJSON(w, 200, map[string]any{"exists": true, "log": string(raw)})
}

// schedulerState 读取 research_state.json 的最近步骤结果（阶段2.4 状态上报）：
// 返回 {day, step_index, done, last_step, last_status, last_error, last_at}；文件缺失返回 null。
// 前端自动研究页据此展示「夜间作业最近一步：discover_factors / done|error|timeout」，
// 杜绝"dataload 卡 21h、step_index 停在 0"这类静默故障无感知。
// English: reads the latest step outcome from research_state.json ({day, step_index, done, last_step,
// last_status, last_error, last_at}); null when absent. The auto-research page shows it so silent
// stalls (a dataload once hung 21h with step_index stuck at 0) become visible.
func (s *Server) schedulerState() map[string]any {
	if s.researchDir == "" {
		return nil // 未配置研究目录
	}
	b, err := os.ReadFile(filepath.Join(s.researchDir, "research_state.json"))
	if err != nil {
		return nil // 无状态文件
	}
	var st map[string]any
	if json.Unmarshal(b, &st) != nil {
		return nil // 状态文件损坏按缺失处理
	}
	return st
}

// handleResearchCandidates 处理 GET /api/research/candidates：列出候选（默认全部）。
// （handleResearchCandidates lists research candidates, optionally filtered by status.）
func (s *Server) handleResearchCandidates(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	status := r.URL.Query().Get("status")
	cands, err := s.researchDB.ListCandidates(status)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// 通用回测证据（§8.6-B）：factor 候选看 B4 回填的 avg_excess；pattern 候选的
	// 回放结果在任务行 result_text——统一按"两类型最新任务是否 done"判定已测，
	// 并把回放汇总文本附带给前端展示（否则形态候选永远显示"未测"）。
	type btEvidence struct {
		Done bool   `json:"backtest_done"`
		Text string `json:"backtest_result_text,omitempty"`
	}
	evid := map[int64]btEvidence{}
	for _, c := range cands {
		var best *store.ResearchTask
		for _, typ := range []string{store.TaskBacktestCandidate, store.TaskBacktestStrategy} {
			t, err := s.researchDB.LatestTaskByRef(typ, c.ID)
			if err == nil && t != nil && (best == nil || t.ID > best.ID) {
				best = t
			}
		}
		if best != nil && best.Status == store.TaskDone {
			evid[c.ID] = btEvidence{Done: true, Text: best.ResultText}
		}
	}
	out := make([]map[string]any, 0, len(cands))
	for _, c := range cands {
		m := map[string]any{
			"id": c.ID, "kind": c.Kind, "status": c.Status,
			"factors": c.Factors, "weights": c.Weights, "metric": c.Metric,
			"ic_mean": c.ICMean, "ir": c.IR, "avg_excess": c.AvgExcess,
			"horizon": c.Horizon, "reason": c.Reason, "created_at": c.CreatedAt,
		}
		if ev, ok := evid[c.ID]; ok {
			m["backtest_done"] = true
			if ev.Text != "" {
				m["backtest_result_text"] = ev.Text
			}
		}
		out = append(out, m)
	}
	writeJSON(w, 200, map[string]any{"candidates": out})
}

// handleResearchApprove 处理 POST /api/research/candidates/{id}/approve：审批通过并应用权重。
// （handleResearchApprove approves a candidate; for weight candidates the set is written
// to applied_rules.json so live strategies pick it up.）
func (s *Server) handleResearchApprove(w http.ResponseWriter, r *http.Request) {
	s.approveCandidate(w, r, "approve")
}

// handleResearchReject 处理 POST /api/research/candidates/{id}/reject：驳回候选。
func (s *Server) handleResearchReject(w http.ResponseWriter, r *http.Request) {
	s.approveCandidate(w, r, "reject")
}

// handleResearchGrayscale 处理 POST /api/research/candidates/{id}/grayscale：
// §Phase2 自动灰度分级——候选进入灰度观察（写 grayscale_rules.json，仅 paper 盘消费，不上实盘）。
func (s *Server) handleResearchGrayscale(w http.ResponseWriter, r *http.Request) {
	s.approveCandidate(w, r, "grayscale")
}

// approveCandidate 审批/驳回候选的公共实现：更新候选状态；审批时按候选类型
// （weights/factor/pattern）写应用配置并热重载引擎，驳回仅改状态。
func (s *Server) approveCandidate(w http.ResponseWriter, r *http.Request, action string) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, 400, "无效 id")
		return
	}
	c, err := s.researchDB.CandidateByID(id)
	if err != nil {
		writeError(w, 404, "候选不存在")
		return
	}
	// §0929LIB-WATCH：战法库已落盘但没能注入运行引擎时的失败原因（在末尾响应里如实回报，
	// 审批"通过"不等于实盘"换上了"；版本戳轮询会在库重新可读后自动重试）。
	var reloadErr error
	switch action {
	case "approve":
		// §0925EVE-W3-G（FIX_PLAN ⑪ C2）副作用顺序修正：旧实现先写 approved 再执行 Apply*，
		// Apply 中途失败（权重 JSON 非法、战法库落盘失败等）候选会永久停在
		// 「已批准但线上没有」的幽灵态且无 reconcile。现改为 Apply 先行、状态后写：
		//   ① 任一 Apply* 失败 → 状态一律不动（保持原态，通常是 proposed），500 带回失败原因，
		//      运维修好数据后可直接重新点审批；
		//   ② 全部 Apply* 成功后才写 approved；属于可应用类型（weights/factor/pattern）的
		//      再推进到 applied 终态并热重载；不可应用类型 approved 即终态（与旧行为一致）。
		// 读码确认的其余副作用顺序问题（如实登记，不掩盖）：
		//   ③ Apply 成功后、状态写入前失败 → 出现**反向**不一致「线上已生效但状态未推进」。
		//      该方向是安全侧：规则确实已应用，且重新审批可收敛——ApplyWeights 为整文件覆写、
		//      ApplyFactorRule/ApplyPatternRule 按 fac_<id>/pat_<id> 键幂等 upsert，重点不产生
		//      重复条目；旧方向（状态推进了但线上没有）才是会造成误判的危险侧，本批修掉。
		//   ④ 热重载现在有错误返回（§0929LIB-WATCH）：库已落盘但注入失败时，状态仍是 applied、
		//      文件也已生效于下一次引擎启动；响应带 reload_error 如实回告，同时版本戳轮询会在
		//      库重新可读后自动注入。旧口径"失败仅弱一致、无人可见"不再成立。
		applied := false // applied=true 表示本候选类型已完成 Apply*，状态应推进到 applied
		reload := false  // reload=true 表示战法库有变更，需热重载引擎 8a/8b
		if c.Kind == "weights" {
			if err := research.ApplyWeights(s.researchDir, c); err != nil {
				// §0925EVE-W3-G：Apply 失败不改状态，原因带回响应（沿用该 handler 既有 500+error 体例）
				writeError(w, 500, "应用权重失败(状态未变更，可修复后重新审批): "+err.Error())
				return
			}
			applied = true
			log.Printf("[research] 候选 #%d 应用权重成功（状态随后推进）", id)
		}
		// E6：因子战法候选审批 → 追加到战法库 applied_factors.json 并热重载引擎（多战法同时实盘）。
		// English: E6 — approving a factor candidate appends it to the library applied_factors.json and
		// hot-reloads the engine (multiple strategies run concurrently).
		if c.Kind == "factor" {
			if err := research.ApplyFactorRule(s.researchDir, c); err != nil {
				writeError(w, 500, "应用因子规则失败(状态未变更，可修复后重新审批): "+err.Error())
				return
			}
			applied = true
			reload = true
		}
		// F3：形态战法候选审批 → 追加到战法库 applied_patterns.json 并热重载引擎（多形态同时实盘）。
		// English: F3 — approving a pattern candidate appends it to the library and hot-reloads the engine.
		if c.Kind == "pattern" {
			if err := research.ApplyPatternRule(s.researchDir, c); err != nil {
				writeError(w, 500, "应用形态规则失败(状态未变更，可修复后重新审批): "+err.Error())
				return
			}
			applied = true
			reload = true
		}
		// §0925EVE-W3-G：全部 Apply* 已成功后才落状态——approved 先写（不可应用类型的终态），
		// 可应用类型再推进 applied；两步之间失败即 ③ 描述的安全侧反向不一致，重新审批可收敛。
		if err := s.researchDB.UpdateCandidateStatus(id, "approved"); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if applied {
			if err := s.researchDB.UpdateCandidateStatus(id, "applied"); err != nil {
				writeError(w, 500, err.Error())
				return
			}
			if reload {
				reloadErr = s.reloadLibraries() // 立即注入 8a/8b，无需重启（失败原因见响应 reload_error）
			}
			log.Printf("[research] 候选 #%d 审批并应用完成 kind=%s", id, c.Kind)
		}
	case "reject":
		if err := s.researchDB.UpdateCandidateStatus(id, "rejected"); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		log.Printf("[research] 候选 #%d 已驳回", id)
	case "grayscale":
		// §Phase2 自动灰度分级：仅 factor/pattern 支持；写入灰度库（paper 观察），不改实盘。
		// English: Phase-2 grayscale — factor/pattern only; enters the grayscale library (paper observation),
		// the live 8a/8b injection is untouched.
		if c.Kind != "factor" && c.Kind != "pattern" {
			writeError(w, 400, "灰度仅支持 factor/pattern 候选（kind="+c.Kind+"）")
			return
		}
		if err := research.ApplyGrayscale(s.researchDir, c); err != nil {
			writeError(w, 500, "写入灰度库失败: "+err.Error())
			return
		}
		if err := s.researchDB.UpdateCandidateStatus(id, research.StatusGrayscale); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		// §Phase3 paper A/B 对照组：为灰度候选开立独立规则池并标记 B 组，
		// 与回测最优（A 组）并行实测。池 key 与库规则同规则（fac_<id>/pat_<id>）。
		// English: Phase-3 paper A/B — carve a dedicated rule pool for the grayscale candidate and tag
		// it group B, running head-to-head against the backtest champion (group A). Pool key follows
		// the library-rule convention (fac_<id>/pat_<id>).
		if pe := s.paperEngineFor(requestUserID(r)); pe != nil {
			poolKey := "fac_" + strconv.FormatInt(id, 10)
			if c.Kind == "pattern" {
				poolKey = "pat_" + strconv.FormatInt(id, 10)
			}
			pe.EnsurePool(poolKey)
			pe.SetPoolABGroup(poolKey, "B")
		}
		log.Printf("[research] 候选 #%d 进入灰度观察（paper 盘，B 组对照）", id)
	}
	// §0929LIB-WATCH：字段形状稳定——reload_error 恒在（成功为空串），前端可无条件判
	// "这次审批有没有真的把战法换到运行中的引擎上"。
	writeJSON(w, 200, map[string]string{"status": "ok", "reload_error": errText(reloadErr)})
}

// handleStrategySnapshots §WS-H C2：GET /api/research/strategies/snapshots —— 列出历史参数快照
// （applied_*.json / grayscale_rules.json 的写前自动快照，最新在前），前端回滚选择用。
// English: WS-H C2 — lists historical strategy-parameter snapshots (newest first) for rollback selection.
func (s *Server) handleStrategySnapshots(w http.ResponseWriter, r *http.Request) {
	if s.researchDir == "" {
		writeError(w, 503, "研究目录未接入")
		return
	}
	list, err := research.ListSnapshots(s.researchDir)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"snapshots": list})
}

// handleStrategyRollback §WS-H C2：POST /api/research/strategies/rollback {snapshot_ts}
// —— 从历史快照原子恢复四类策略规则文件，记 opslog 审计。
// 仅管理员可操作。English: WS-H C2 — restores the four strategy-rule files from a historical
// snapshot (atomic), records an opslog audit. Admin only.
func (s *Server) handleStrategyRollback(w http.ResponseWriter, r *http.Request) {
	if s.researchDir == "" {
		writeError(w, 503, "研究目录未接入")
		return
	}
	var req struct {
		SnapshotTS string `json:"snapshot_ts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SnapshotTS == "" {
		writeError(w, 400, "需要 snapshot_ts")
		return
	}
	if err := research.RestoreSnapshot(s.researchDir, req.SnapshotTS); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	log.Printf("[research] 参数回滚到快照 %s（操作者=%s）", req.SnapshotTS, userIDFor(r))
	writeJSON(w, 200, map[string]string{"status": "ok", "snapshot_ts": req.SnapshotTS})
}

// handlePaperResearchReports 处理 GET /api/research/paper-reports?limit=30：夜间信号质量研究
// 报告历史（§D-1 GAP_VERIFY_20260917_PM：paper_research_reports 有写无读补齐读端）。
// 报告正文含全账号 daily/归因聚合（生成侧即跨账号），故端点 admin-only，不做行级过滤——
// 子账号越权在数据内容层就无法隔离（与 §WS-E 敏感管线隔离同一口径）。
// English: serves the nightly paper-research report history. The report payload aggregates
// cross-account daily/attribution data by construction, so the endpoint is admin-only — row
// filtering cannot isolate content anyway (same policy as the §WS-E sensitive pipelines).
func (s *Server) handlePaperResearchReports(w http.ResponseWriter, r *http.Request) {
	if s.researchDB == nil {
		writeError(w, 503, "研究库未接入")
		return
	}
	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	reports, err := s.researchDB.ListPaperResearchReports("", limit)
	if err != nil {
		writeError(w, 500, "读取研究报告失败: "+err.Error())
		return
	}
	// 正文以字符串入库，这里逐行解回对象，前端拿到的就是结构化报告（解失败的行保留原文并标注）。
	out := make([]map[string]any, 0, len(reports))
	for _, rp := range reports {
		item := map[string]any{
			"date": rp.Date, "user_id": rp.UserID, "created_at": rp.CreatedAt,
		}
		var body any
		if err := json.Unmarshal([]byte(rp.Summary), &body); err != nil {
			item["summary_text"] = rp.Summary
		} else {
			item["summary"] = body
		}
		out = append(out, item)
	}
	writeJSON(w, 200, map[string]any{"reports": out, "count": len(out)})
}
