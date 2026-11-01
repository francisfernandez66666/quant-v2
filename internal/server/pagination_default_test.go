// pagination_default_test.go — §PAGING-DEFAULT（2026-11-01）：列表端点必须真分页。
//
// 缺陷原文：`parsePageParams` 的第二个参数是"缺省页大小"，而 `pageBounds` 对 size<=0 返回
// [0,total)（不分页）。两个调用点传的是 **0**，于是「分页机制存在、缺省行为等于没分页」：
// 现网实测 GET /api/alerts 一次 1,900,221 B / 4352 条 / 2.1 s，而响应头里 X-Page-Size=0 就是铁证。
//
// 本文件的核心是 C5 那条**元断言**：扫全仓 `parsePageParams(r, X, Y)` 调用点，X 必须 ≠ 0。
// 它防的不是这两个点，而是"下一个人再写一个 defaultSize=0 的调用点"——只修调用点不加元断言，
// 同一个病会以同样的姿势复发（这正是它第一次进来的方式）。
//
// English: §PAGING-DEFAULT — default page size must never be 0; the meta-assertion guards future call sites.
package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// fakeAlertCtrl 只实现 /api/alerts 用到的两个读方法。
// 名字留空会走 s.quote() + RefreshMessageName()，本 fake 没实现后者 ⇒ 会 panic，
// 所以 fixture 一律给非空 Name，把那条分支排除在外（它不属于本用例的判据）。
type fakeAlertCtrl struct {
	EngineController
	msgs []data.MessageItem
}

// GetMessagesFor 返回预置消息（handler 侧只做 公共 ∪ 本人 的过滤，本 fake 直接给结果）。
func (f *fakeAlertCtrl) GetMessagesFor(string) []data.MessageItem { return f.msgs }

// mkAlerts 造 n 条告警：Name 非空（绕开 quote 回填分支），GeneratedAt 递减保证倒序稳定。
func mkAlerts(n int) []data.MessageItem {
	base := time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local)
	out := make([]data.MessageItem, n)
	for i := 0; i < n; i++ {
		out[i] = data.MessageItem{
			ID:          "a" + strconv.Itoa(i),
			Code:        "000001",
			Name:        "平安银行",
			Level:       "提示",
			Title:       "t",
			GeneratedAt: base.Add(-time.Duration(i) * time.Minute),
		}
	}
	return out
}

// callAlerts 直接打 handler（不经 mux），返回 recorder 与解析后的数组长度。
func callAlerts(t *testing.T, n int, query string) (*httptest.ResponseRecorder, int) {
	t.Helper()
	s := &Server{ctrl: &fakeAlertCtrl{msgs: mkAlerts(n)}}
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
	return w, len(out)
}

// C1：不带参只回缺省 50 条，且 X-Page-Size=50（证明分页真的生效，不是"碰巧只有 50 条"）。
func TestAlertsDefaultPageSizeIs50(t *testing.T) {
	w, got := callAlerts(t, 200, "")
	if got != 50 {
		t.Fatalf("缺省应回 50 条，实得 %d", got)
	}
	if v := w.Header().Get("X-Page-Size"); v != "50" {
		t.Fatalf("X-Page-Size 应为 50，实得 %q（0 就说明分页没生效）", v)
	}
}

// C2：X-Total-Count 必须是**总数**而不是本页条数——它是"被截断了"的唯一机读信号。
func TestAlertsTotalCountHeaderPresent(t *testing.T) {
	w, got := callAlerts(t, 200, "")
	total, err := strconv.Atoi(w.Header().Get("X-Total-Count"))
	if err != nil {
		t.Fatalf("X-Total-Count 缺失或非数字：%q", w.Header().Get("X-Total-Count"))
	}
	if total != 200 {
		t.Fatalf("X-Total-Count 应为 200，实得 %d", total)
	}
	if total <= got {
		t.Fatalf("总数 %d 应大于本页条数 %d（否则说明是全量返回）", total, got)
	}
}

// C3：?page/page_size 生效，且第 2 页与第 1 页无交集。
func TestAlertsPageSizeParamRespected(t *testing.T) {
	_, n1 := callAlerts(t, 200, "?page=1&page_size=10")
	_, n2 := callAlerts(t, 200, "?page=2&page_size=10")
	if n1 != 10 || n2 != 10 {
		t.Fatalf("每页应 10 条，实得 %d / %d", n1, n2)
	}
	// 两页内容必须不同：fixture 的 ID 递增，第 1 页 a0..a9、第 2 页 a10..a19
	w1, _ := callAlerts(t, 200, "?page=1&page_size=10")
	w2, _ := callAlerts(t, 200, "?page=2&page_size=10")
	var p1, p2 []map[string]interface{}
	_ = json.Unmarshal(w1.Body.Bytes(), &p1)
	_ = json.Unmarshal(w2.Body.Bytes(), &p2)
	if len(p1) > 0 && len(p2) > 0 && p1[0]["id"] == p2[0]["id"] {
		t.Fatalf("第 1 页与第 2 页首条相同（%v），分页没有真正切片", p1[0]["id"])
	}
}

// C4：?page_size 超上限夹到 5000（这个上限是给"消息中心要全量做本地筛选"留的显式出口）。
func TestAlertsPageSizeClampedTo5000(t *testing.T) {
	w, _ := callAlerts(t, 20, "?page_size=99999")
	if v := w.Header().Get("X-Page-Size"); v != "5000" {
		t.Fatalf("X-Page-Size 应夹到 5000，实得 %q", v)
	}
}

// C5：**元断言**——扫全仓所有 `parsePageParams(r, X, Y)` 调用点，X 必须 ≠ 0。
// 这一条是防回潮的关键：只修现有两个调用点，下一个人再写一个 defaultSize=0 就白修了。
func TestPaginationDefaultsNeverZero(t *testing.T) {
	// 形如 parsePageParams(r, 0, 500) / parsePageParams(r, 50, 500)；
	// 捕获前两个参数，允许中间有空格。
	re := regexp.MustCompile(`parsePageParams\(\s*\w+\s*,\s*(-?\d+)\s*,`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("列文件失败: %v", err)
	}
	checked := 0
	for _, f := range files {
		if filepath.Ext(f) != ".go" || len(f) > 8 && f[len(f)-8:] == "_test.go" {
			continue // 只查生产代码；本文件自己出现的字面量不算调用点
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			checked++
			def, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("%s：解析缺省页大小失败 %q", f, m[1])
			}
			if def == 0 {
				t.Fatalf("%s 存在 defaultSize=0 的 parsePageParams 调用点（%s）——"+
					"0 会让 pageBounds 返回全量，等于没分页", f, m[0])
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个 parsePageParams 调用点都没扫到——正则或路径失效，这条元断言会假绿")
	}
	t.Logf("已检查 %d 个 parsePageParams 调用点，缺省页大小均 > 0", checked)
}
