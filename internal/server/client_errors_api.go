// client_errors_api.go — §OBSERVABILITY（2026-10-26）：前端错误上报与运行用量看板。
//
// 目标：前端运行期错误（window.onerror / unhandledrejection / React 错误边界）不再只留在
// 用户浏览器控制台；后端按指纹去重后落 opslog，并在 /api/usage 里给管理员一个当日计数。
// 纪律：
//   - 载荷限长（16KB）+ 字段截断；Bearer/token/password/api_key/secret 一律脱敏；
//   - 同一指纹 5 分钟窗口内只落一次 opslog，防刷屏；计数照常累加；
//   - 上报端点要求登录（authMiddleware），不开放匿名写面。
package server

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/notify"
	"quant-trading-v2/internal/opslog"
)

const (
	clientErrMaxBody    = 16 << 10
	clientErrMaxEntries = 200
	clientErrDedupe     = 5 * time.Minute
)

// clientErrPayload 前端上报体（message/stack 必填其一；其余可空）。
type clientErrPayload struct {
	Message string `json:"message"`
	Stack   string `json:"stack"`
	Source  string `json:"source"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Route   string `json:"route"`
	Build   string `json:"build"`
}

// clientErrStat 单指纹聚合（只保留展示需要的字段，不存原始堆栈）。
type clientErrStat struct {
	Count   int64     `json:"count"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
	Message string    `json:"message"`
	Source  string    `json:"source"`
	Route   string    `json:"route"`
}

var (
	clientErrBearerRe = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._\-]+`)
	clientErrSecretRe = regexp.MustCompile(`(?i)((?:token|password|api_key|apikey|secret)=)[^\s&,"]+`)
)

// sanitizeClientErrText 截断到 maxRunes 并脱敏常见凭据形态；保留换行/制表符便于读栈。
func sanitizeClientErrText(s string, maxRunes int) string {
	s = clientErrBearerRe.ReplaceAllString(s, "${1}<redacted>")
	s = clientErrSecretRe.ReplaceAllString(s, "${1}<redacted>")
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' || r >= 0x20 {
			b.WriteRune(r)
		}
	}
	s = strings.TrimSpace(b.String())
	if maxRunes <= 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) > maxRunes {
		return string(rs[:maxRunes]) + "…"
	}
	return s
}

// handleClientError POST /api/client-errors：登录用户上报前端错误。
func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, clientErrMaxBody)
	var p clientErrPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "无效请求体")
		return
	}
	msg := sanitizeClientErrText(p.Message, 500)
	if msg == "" {
		msg = sanitizeClientErrText(p.Stack, 500)
	}
	if msg == "" {
		writeError(w, http.StatusBadRequest, "缺少错误信息")
		return
	}
	stack := sanitizeClientErrText(p.Stack, 4000)
	source := sanitizeClientErrText(p.Source, 500)
	route := sanitizeClientErrText(p.Route, 300)
	build := sanitizeClientErrText(p.Build, 40)

	fpRaw := fmt.Sprintf("%s|%s|%d|%d", msg, source, p.Line, p.Col)
	sum := sha1.Sum([]byte(fpRaw))
	fp := hex.EncodeToString(sum[:8])

	now := time.Now()
	s.clientErrMu.Lock()
	if s.clientErrStats == nil {
		s.clientErrStats = make(map[string]*clientErrStat)
	}
	if _, ok := s.clientErrStats[fp]; !ok && len(s.clientErrStats) >= clientErrMaxEntries {
		// 指纹表封顶：整体换新一批（内存态观测数据，不承担长期账本职责）。
		s.clientErrStats = make(map[string]*clientErrStat)
	}
	st := s.clientErrStats[fp]
	isNew := st == nil || now.Sub(st.LastAt) > clientErrDedupe
	if st == nil {
		st = &clientErrStat{FirstAt: now, Message: msg, Source: source, Route: route}
		s.clientErrStats[fp] = st
	}
	st.Count++
	st.LastAt = now
	s.clientErrTotal++
	s.clientErrLast = now
	count := st.Count
	s.clientErrMu.Unlock()

	if isNew {
		opslog.Logf("client_error", "前端错误 fp=%s 路由=%s 来源=%s:%d:%d 构建=%s 次数=%d | %s",
			fp, route, source, p.Line, p.Col, build, count, msg)
		log.Printf("[client-error] fp=%s route=%s source=%s:%d:%d build=%s count=%d stack=%s",
			fp, route, source, p.Line, p.Col, build, count, stack)
		if s.notifier != nil {
			s.notifier.Push(notify.Message{Level: notify.LevelLow, Title: "前端错误", Content: msg})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "deduped": !isNew, "count": count})
}

// clientErrorSnapshot 供 /api/usage 展示：总数、唯一指纹数、最后时刻、Top 10 指纹。
func (s *Server) clientErrorSnapshot() map[string]any {
	s.clientErrMu.Lock()
	defer s.clientErrMu.Unlock()
	top := make([]clientErrStat, 0, len(s.clientErrStats))
	for _, st := range s.clientErrStats {
		top = append(top, *st)
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Count > top[j].Count })
	if len(top) > 10 {
		top = top[:10]
	}
	last := ""
	if !s.clientErrLast.IsZero() {
		last = s.clientErrLast.Format(time.RFC3339)
	}
	return map[string]any{
		"total":   s.clientErrTotal,
		"unique":  len(s.clientErrStats),
		"last_at": last,
		"top":     top,
	}
}

// newsTierAggregator §NEWS-TIER（生产接线批）：注册表的可选只读接口——
// 各账号最近一轮新闻分级出站的读数（必推/折叠/跳过原因……）。
// 为什么做成"可选接口 + 类型断言"而不是加进 EngineRegistry 主接口：加主接口方法会打穿
// 全部测试桩（它们都逐字实现 EngineRegistry），而这条腿是纯观测，缺席只应表现为
// attached=false，不应让任何既有装配编译失败。真源实现是 engine.Registry.NewsTierStatusMaps。
// English: optional read-only interface for the news-tier dispatch readout; asserted rather
// than added to EngineRegistry so observation-leg absence degrades to attached=false.
type newsTierAggregator interface {
	NewsTierStatusMaps() []map[string]interface{}
}

// newsTierUsageReadout 装配 /api/usage 的 news_tier 段。
// 三态必须互不折叠（本仓反复出现的形状——J0 零回包、空快照、指数盲区全是一族）：
//   - attached=false ⇒ 读数腿没接上（注册表缺失/非真注册表），"看不见分级"不等于"分级没跑"；
//   - attached=true 且 accounts 为空 ⇒ 引擎已建但今天还没跑过任何新闻轮；
//   - accounts 里某条 constructed=false + skip_reason ⇒ 真跑了但分级被跳过（缺推送器/缺身份/缺持仓源）。
//
// English: assembles the news_tier block; the three states never collapse into one another.
func (s *Server) newsTierUsageReadout() map[string]interface{} {
	out := map[string]interface{}{"attached": false, "accounts": []map[string]interface{}{}}
	if s.registry == nil {
		return out
	}
	agg, ok := s.registry.(newsTierAggregator)
	if !ok {
		return out
	}
	maps := agg.NewsTierStatusMaps()
	out["attached"] = true
	if maps != nil {
		out["accounts"] = maps
	}
	return out
}

// handleUsage GET /api/usage（admin）：LLM 当日用量 + 数据源健康 + 前端错误聚合。
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"uptime":        time.Since(s.startTime).Round(time.Second).String(),
		"build_commit":  s.buildCommit,
		"client_errors": s.clientErrorSnapshot(),
		// §NEWS-TIER 新闻分级出站读数（JSON 路径 news_tier.attached / news_tier.accounts[]）。
		// 只读观测：值不参与任何交易判定（§OBS-GUARD 同规），放在 ops_admin 这面而不放
		// /api/qmt/state——那是单账号实盘快照，而分级账是按账号各记各的，运营要一次看全。
		"news_tier": s.newsTierUsageReadout(),
	}
	if s.dc != nil {
		out["data_sources"] = s.dc.HealthCheck()
		out["news_sources"] = s.dc.NewsSourceHealth()
	}
	// §SCHEMA-VERSION（旧审计 F10）：看板回显研究库/实盘库 schema 版本，排障时先看这枚。
	if s.researchDB != nil {
		out["schema_version"] = s.researchDB.SchemaVersion()
	}
	if s.liveDB != nil {
		out["live_schema_version"] = s.liveDB.SchemaVersion()
	}
	if s.llmUsageFn != nil {
		out["llm"] = s.llmUsageFn()
	} else {
		out["llm"] = map[string]any{"available": false}
	}
	writeJSON(w, http.StatusOK, out)
}
