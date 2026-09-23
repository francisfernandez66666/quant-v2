// 文件：library_open_positions_test.go
// 包名：server
// 所属模块：「HTTP 接口层：战法库（因子/形态）」
// 模块职责：§EXIT-RETAIN（2026-09-23）战法库载荷的持仓可观测性——GET /api/research/library
// 每条必须带 open_positions（该战法名下仍开放的持仓笔数，按持仓 Strategy 匹配规则 ID 或显示名）。
// 前端「停用」确认文案要说"你还有 N 笔仓在它名下"，而这个数只有后端知道（实盘账本 ∪ 模拟盘账本）；
// 出场参数已经跟随持仓，所以这个字段是操作员判断"停用会不会动到存量仓"的唯一入口。
// 本用例走注册表未接入的回退分支（只读实盘账本），覆盖持仓历史的两种记录形态：
//   - 按**显示名**存（历史形态，pos.Strategy="波动突破"）；
//   - 按**规则 ID** 存（新形态，pos.Strategy="fac_2"）；
//     并锁住"未持有的条目 = 0（字段存在而非缺失）"与"非库内战法（龙头）不串数"。
//
// English: the library payload must expose open_positions per entry, matching positions stored under
// either the rule's display name (legacy shape) or its id, and zero (not absent) for rules with no
// holdings.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/store"
)

// TestResearchLibraryExposesOpenPositions GET /api/research/library 的 open_positions 计数口径。
func TestResearchLibraryExposesOpenPositions(t *testing.T) {
	s, _, dir := newTestResearchServer(t)
	// 实盘账本隔离库（生产同址）：注册表未接入时 openPositionCounts 的回退数据源
	live, err := store.Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open live.db: %v", err)
	}
	t.Cleanup(func() { live.Close() })
	s.SetLiveDB(live)

	// 两条因子战法 + 一条形态战法（fac_1 显示名"波动突破"、fac_2 显示名"新基线战法"）
	library := `[{"id":"fac_1","name":"波动突破","enabled":true,"candidate_id":1,` +
		`"factors":["Mom20"],"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70,"horizon":5,` +
		`"exit_trail_pct":6,"exit_max_hold_days":9},` +
		`{"id":"fac_2","name":"新基线战法","enabled":true,"candidate_id":2,` +
		`"factors":["Mom20"],"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70,"horizon":5},` +
		`{"id":"pat_1","name":"形态战法#1","enabled":true,"candidate_id":3,` +
		`"conds":[{"factor":"Vol20","min":1.5}]}]`
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), []byte(library), 0o644); err != nil {
		t.Fatal(err)
	}

	// 持仓：两笔按显示名挂在 fac_1 下（历史形态）、一笔按规则 ID 挂在 fac_2 下、
	// 一笔是四个内置手写战法的（龙头，不经战法库）——它不该出现在任何条目的计数里。
	// UpsertRealPositions 是 store 登记的测试专用入口，此处正按该用途使用。
	if _, err := live.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发银行", Qty: 100, CostPrice: 10, Strategy: "波动突破", UserID: "u1"},
		{TsCode: "600001.SH", Name: "邯郸钢铁", Qty: 100, CostPrice: 10, Strategy: "波动突破", UserID: "u2"},
		{TsCode: "000001.SZ", Name: "平安银行", Qty: 100, CostPrice: 10, Strategy: "fac_2", UserID: "u1"},
		{TsCode: "000002.SZ", Name: "万科A", Qty: 100, CostPrice: 10, Strategy: "龙头", UserID: "u1"},
	}); err != nil {
		t.Fatalf("seed real positions: %v", err)
	}

	// 请求一次库端点即可同时验证两侧：持仓数与出场覆盖留痕都在同一份响应里。
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
	if len(byID) != 3 {
		t.Fatalf("应返回 3 条, got %d: %s", len(byID), rr.Body.String())
	}
	want := map[string]int{"fac_1": 2, "fac_2": 1, "pat_1": 0}
	for id, w := range want {
		item, ok := byID[id]
		if !ok {
			t.Fatalf("缺少条目 %s", id)
		}
		v, exists := item["open_positions"]
		if !exists {
			t.Fatalf("%s 未透出 open_positions 字段: %+v", id, item)
		}
		n, isNum := v.(float64)
		if !isNum || int(n) != w {
			t.Fatalf("%s open_positions: got %v want %d (item=%+v)", id, v, w, item)
		}
	}
	// 停用态条目的计数同样要出（自动降级后持仓还在，页面必须看得见）
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"),
		[]byte(`[{"id":"fac_1","name":"波动突破","enabled":false,"candidate_id":1,`+
			`"factors":["Mom20"],"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70,`+
			`"exit_trail_pct":6,"exit_max_hold_days":9}]`), 0o644); err != nil {
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
	if len(resp2.Library) != 1 {
		t.Fatalf("应只剩 1 条, got %d", len(resp2.Library))
	}
	if got, _ := resp2.Library[0]["open_positions"].(float64); got != 2 {
		t.Fatalf("已停用条目的 open_positions 应为 2, got %v", resp2.Library[0])
	}
}

// TestOpenPositionsForNoDoubleCount ID 与显示名归一化同值时不重复计数（一笔只算一笔），
// 两种记录形态并存时相加。
func TestOpenPositionsForNoDoubleCount(t *testing.T) {
	held := combat_agent.HeldStrategyKeys{}
	for i := 0; i < 3; i++ {
		held.Add("fac_1")
	}
	if got := openPositionsFor(held, "fac_1", "fac_1"); got != 3 {
		t.Fatalf("同值双键应只计一次, got %d", got)
	}
	if got := openPositionsFor(held, "fac_1", "因子战法#1"); got != 3 {
		t.Fatalf("显示名未持有时应仍为 ID 的 3, got %d", got)
	}
	held.Add("因子战法#1")
	if got := openPositionsFor(held, "fac_1", "因子战法#1"); got != 4 {
		t.Fatalf("两种记录形态应相加, got %d", got)
	}
	if got := openPositionsFor(nil, "fac_1", "因子战法#1"); got != 0 {
		t.Fatalf("nil 计数表应为 0, got %d", got)
	}
}
