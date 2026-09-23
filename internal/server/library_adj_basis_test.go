// library_adj_basis_test.go §ADJ-BASIS-2（2026-09-23）：GET /api/research/library 必须把
// "复权口径基线是否已失效"原样交给前端（adj_basis + stale_adj_basis 两个字段），否则战法卡片上
// 那条红标就没有数据源——本轮实测 fac_1「波动突破」四个分量里三个的分层能力在正确口径下塌到 ≈0，
// 而从库文件本身完全看不出来，只有载入侧判定 + 端点透出能让人在页面上"看得见这件事"。
// English: the library endpoint must expose the adjustment-basis stamp and the load-side staleness
// verdict so the UI can badge entries that lost their historical basis.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/research"
)

// TestResearchLibraryExposesStaleAdjBasis 旧格式条目在端点上判 stale、新盖章条目不判。
func TestResearchLibraryExposesStaleAdjBasis(t *testing.T) {
	s, _, dir := newTestResearchServer(t)
	legacy := `[{"id":"fac_1","name":"波动突破","enabled":true,"candidate_id":1,` +
		`"factors":["AtrRatio14","Brk60","STOA"],"weights":{"AtrRatio14":0.4,"Brk60":0.3,"STOA":0.3},` +
		`"directions":{"AtrRatio14":1,"Brk60":1,"STOA":1},"buy_threshold":71,"horizon":5},` +
		`{"id":"fac_2","name":"新基线战法","enabled":true,"candidate_id":2,"factors":["Mom20"],` +
		`"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70,` +
		`"adj_basis":"` + research.AdjBaselineVersion + `"}]`
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.handleResearchLibrary(rr, httptest.NewRequest(http.MethodGet, "/api/research/library", nil))
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Library []map[string]any `json:"library"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, e := range resp.Library {
		byID[e["id"].(string)] = e
	}
	if len(byID) != 2 {
		t.Fatalf("应返回 2 条, got %d: %s", len(byID), rr.Body.String())
	}
	old, fresh := byID["fac_1"], byID["fac_2"]
	if old == nil || fresh == nil {
		t.Fatalf("缺少条目: %s", rr.Body.String())
	}
	if v, _ := old["stale_adj_basis"].(bool); !v {
		t.Errorf("旧格式（无 adj_basis）应在端点上判 stale, got %+v", old)
	}
	if v, _ := old["adj_basis"].(string); v != "" {
		t.Errorf("旧条目 adj_basis 应为空（不得回填）, got %q", v)
	}
	if v, _ := fresh["stale_adj_basis"].(bool); v {
		t.Errorf("当前基线条目不该判 stale, got %+v", fresh)
	}
	if v, _ := fresh["adj_basis"].(string); v != research.AdjBaselineVersion {
		t.Errorf("新条目应回显口径戳 %s, got %q", research.AdjBaselineVersion, v)
	}
	// §ADJ-BASIS-2P 形态战法与因子战法**同一条链**：条件因子（Vol20/Mom20…）一样跑在复权价上，
	// 未盖章的旧条目必须同样判 stale 并在端点上透出——上一版这里写的是"形态不参与该判定"，
	// 那正是被 #28 抓到的不对称：真库里 pat_* 永不标失效，disable 处置也漏掉它们。
	pats := `[{"id":"pat_1","name":"形态战法#1","enabled":true,"candidate_id":9,"conds":[{"factor":"Vol20","min":1.5}]},` +
		`{"id":"pat_2","name":"形态战法#2","enabled":true,"candidate_id":10,"conds":[{"factor":"Vol20","min":1.5}],` +
		`"adj_basis":"` + research.AdjBaselineVersion + `"}]`
	if err := os.WriteFile(filepath.Join(dir, "applied_patterns.json"), []byte(pats), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "applied_factors.json")); err != nil {
		t.Fatal(err)
	}
	rr2 := httptest.NewRecorder()
	s.handleResearchLibrary(rr2, httptest.NewRequest(http.MethodGet, "/api/research/library", nil))
	var resp2 struct {
		Library []map[string]any `json:"library"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if len(resp2.Library) != 2 {
		t.Fatalf("应返回 2 条形态战法, got %s", rr2.Body.String())
	}
	pat := map[string]map[string]any{}
	for _, e := range resp2.Library {
		if e["kind"] != "pattern" {
			t.Fatalf("本段只该有形态条目, got %+v", e)
		}
		pat[e["id"].(string)] = e
	}
	if v, _ := pat["pat_1"]["stale_adj_basis"].(bool); !v {
		t.Errorf("未盖章形态条目应在端点上判 stale, got %+v", pat["pat_1"])
	}
	if v, _ := pat["pat_1"]["adj_basis"].(string); v != "" {
		t.Errorf("未盖章形态条目 adj_basis 应为空（不得回填）, got %q", v)
	}
	if v, ok := pat["pat_2"]["stale_adj_basis"]; ok && v != false {
		t.Errorf("已盖章形态条目不该判 stale, got %+v (stale_adj_basis=%v)", pat["pat_2"], v)
	}
	if v, _ := pat["pat_2"]["adj_basis"].(string); v != research.AdjBaselineVersion {
		t.Errorf("已盖章形态条目应回显口径戳 %s, got %q", research.AdjBaselineVersion, v)
	}
}
