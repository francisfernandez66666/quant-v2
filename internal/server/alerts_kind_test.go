// alerts_kind_test.go — §B6（2026-11-05 通知增强）消息中心读面的 kind 分流。
//
// 判据两条：
//  1. 「新闻」tab 与信号流走**同一个** GET /api/alerts，只按 ?kind=news 分流（不开新路由 ⇒
//     契约台账与 §115 五组派生计数一个字都不动，这是施工图 §5A.4 写死的影响面约束）；
//  2. 缺省视图必须**排除**新闻行——利多非持仓"不混进信号流"的那半句就在读面上落地，
//     只在前端不渲染等于把两种账混在一本里。
package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// kindFixture 造 n 条普通消息 + m 条新闻行（Name 非空以绕开行情回填分支）。
// English: kindFixture builds n classic rows plus m news rows, both with non-empty names.
func kindFixture(t *testing.T, n, m int) []data.MessageItem {
	t.Helper()
	base := time.Date(2026, 11, 5, 9, 30, 0, 0, time.Local)
	out := make([]data.MessageItem, 0, n+m)
	for i := 0; i < n; i++ {
		out = append(out, data.MessageItem{
			ID: "sig" + strconv.Itoa(i), Code: "000001", Name: "平安银行",
			Level: "交易信号", Title: "信号", GeneratedAt: base.Add(-time.Duration(i) * time.Minute),
		})
	}
	for i := 0; i < m; i++ {
		out = append(out, data.MessageItem{
			ID: "news|" + strconv.Itoa(i), Code: "300750", Name: "宁德时代",
			Level: "新闻", Title: "行业利好", Direction: "利好",
			Kind:        data.KindNews,
			GeneratedAt: base.Add(-time.Duration(i) * time.Minute),
		})
	}
	return out
}

// callAlertsKind 直接打 handler，返回 recorder 与解析后的行。
// English: callAlertsKind hits the handler directly and returns the recorder plus parsed rows.
func callAlertsKind(t *testing.T, msgs []data.MessageItem, query string) (*httptest.ResponseRecorder, []map[string]interface{}) {
	t.Helper()
	s := &Server{ctrl: &fakeAlertCtrl{msgs: msgs}}
	r := httptest.NewRequest("GET", "/api/alerts"+query, nil)
	w := httptest.NewRecorder()
	s.handleFixAlerts(w, r)
	if w.Code != 200 {
		t.Fatalf("期望 200，实得 %d body=%s", w.Code, w.Body.String())
	}
	var out []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON 数组: %v", err)
	}
	return w, out
}

// TestAlertsKindNewsSplitsNewsRows 带 ?kind=news 只回新闻行；不带参只回非新闻行。
func TestAlertsKindNewsSplitsNewsRows(t *testing.T) {
	msgs := kindFixture(t, 3, 2)

	w, news := callAlertsKind(t, msgs, "?kind=news&page_size=100")
	if len(news) != 2 {
		t.Fatalf("?kind=news 应回 2 条新闻行，实得 %d", len(news))
	}
	for _, row := range news {
		if row["kind"] != data.KindNews {
			t.Errorf("新闻行没带 kind 字段：%v", row)
		}
	}
	if total := w.Header().Get("X-Total-Count"); total != "2" {
		t.Errorf("X-Total-Count 应只数新闻行，实得 %q", total)
	}

	_, classic := callAlertsKind(t, msgs, "&page_size=100")
	if len(classic) != 3 {
		t.Fatalf("缺省视图应回 3 条非新闻行，实得 %d", len(classic))
	}
	for _, row := range classic {
		if row["kind"] == data.KindNews {
			t.Errorf("新闻行混进了信号流：%v", row)
		}
	}
}

// TestAlertsKindNewsPaginationReallySlices 新闻 tab 的分页必须真切片（第 2 页与第 1 页不重叠）——
// 与自选卡/J-03 同一族：分页参数在但没切，等于没有分页。
// English: paging on the news lane must really slice; a no-op pager is the same defect as §J-03's.
func TestAlertsKindNewsPaginationReallySlices(t *testing.T) {
	msgs := kindFixture(t, 1, 5)
	_, p1 := callAlertsKind(t, msgs, "?kind=news&page=1&page_size=2")
	_, p2 := callAlertsKind(t, msgs, "?kind=news&page=2&page_size=2")
	_, p3 := callAlertsKind(t, msgs, "?kind=news&page=3&page_size=2")
	if len(p1) != 2 || len(p2) != 2 || len(p3) != 1 {
		t.Fatalf("新闻行分页切片不对：p1=%d p2=%d p3=%d（应 2/2/1）", len(p1), len(p2), len(p3))
	}
	if p1[0]["id"] == p2[0]["id"] {
		t.Errorf("第 1 页与第 2 页首条相同（%v），分页没真切片", p1[0]["id"])
	}
	// 三页并起来必须恰好是那 5 条，且没有把非新闻行漏进来（分页不许改变种类判定）
	seen := map[string]bool{}
	for _, row := range append(append(append([]map[string]interface{}{}, p1...), p2...), p3...) {
		if row["kind"] != data.KindNews {
			t.Errorf("新闻分页里混进了非新闻行：%v", row)
		}
		id, _ := row["id"].(string)
		if seen[id] {
			t.Errorf("同一条新闻在分页里出现了两次：%s", id)
		}
		seen[id] = true
	}
	if len(seen) != 5 {
		t.Errorf("三页并起来应覆盖 5 条新闻，实得 %d", len(seen))
	}
}

// TestNewsReadOpensNoNewRoute 静态锁：新闻读数只许挂在既有的 GET /api/alerts 上。
// 多一条路由就是动契约台账（§115 五组派生计数 + 台账行 evidence），本批明确不许碰。
// English: the news read rides the existing route only — a new route would move the contract ledger.
func TestNewsReadOpensNoNewRoute(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("读 server.go 失败: %v", err)
	}
	src := string(b)
	// 先证明判据有对象：路由表里确实挂着三条 /api/alerts（读/清空/单删），一条都不许多
	// English: first prove the ruler has something to measure — exactly three alerts routes exist.
	seen := alertsRouteRe.FindAllString(src, -1)
	if len(seen) != 3 {
		t.Fatalf("/api/alerts 路由应为 3 条（读/清/单删），实得 %d 条：%v", len(seen), seen)
	}
	for _, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "HandleFunc(") {
			continue
		}
		if newsTabRouteRe.MatchString(line) {
			t.Fatalf("出现了为「新闻」tab 新开的路由（§5A.4 要求零新路由）：%s", strings.TrimSpace(line))
		}
	}
}

// alertsRouteRe 抓 /api/alerts 的路由注册行。
// English: alertsRouteRe captures the alerts route registrations.
var alertsRouteRe = regexp.MustCompile(`HandleFunc\("(?:GET|POST|PUT|DELETE) /api/alerts[^"]*`)

// newsTabRouteRe 抓任何"看起来是给新闻 tab 单独开的读路由"（/api/news-tab、/api/alerts/news 之类）。
// 既有的 /api/news 一族（事件展示/健康/重分析）与 /api/alerts/{id}（单条删除）都不在射程内——
// 前者不是本批新增，后者是消息中心现成的删除路由。
// English: newsTabRouteRe catches a freshly opened news-tab read route only; the pre-existing
// /api/news family and the classic DELETE /api/alerts/{id} stay out of its range.
var newsTabRouteRe = regexp.MustCompile(`HandleFunc\("(?:GET|POST|PUT|DELETE) /api/(?:news[-_]?tab|alerts/news)`)

// TestAlertsKindStillUsesSharedHandler 元断言：kind 分流不许另起一个 handler 函数
// （两本账各写一遍排序/分页/名称回填，迟早漂移出"新闻 tab 与信号流行为不一致"）。
// English: the kind split stays inside the shared handler — duplicating paging/sorting is how drift starts.
func TestAlertsKindStillUsesSharedHandler(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("列文件失败: %v", err)
	}
	hits := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if handleNewsAlertsRe.Match(b) {
			hits++
		}
	}
	if hits != 0 {
		t.Fatalf("出现了独立的新闻消息 handler（hits=%d）——§B6 要求在 handleFixAlerts 内按 kind 分流", hits)
	}
}

// handleNewsAlertsRe 找"新起一个新闻告警 handler"的函数签名形状。
// English: handleNewsAlertsRe matches a separately-registered news-alerts handler signature.
var handleNewsAlertsRe = regexp.MustCompile(`func \(s \*Server\) handle(?:NewsAlerts|NewsTabMessages|AlertsNews)\(`)
