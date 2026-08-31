// B5 研究候选审批端点测试：列表 / 审批应用 / 驳回。
// English: B5 research candidate approval endpoint tests: list / approve-apply / reject.
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// newTestResearchServer 建临时研究库与应用目录，返回已接线的 Server。
// English: newTestResearchServer creates a temporary research DB and app directory, returning a wired Server.
func newTestResearchServer(t *testing.T) (*Server, *store.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "trading.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := &Server{researchDB: db, researchDir: dir}
	return s, db, dir
}

// TestResearchCandidateApproveFlow 列表 → 审批应用 → 状态流转 + applied_rules.json 落盘。
// English: TestResearchCandidateApproveFlow list → approve-apply → status transitions + applied_rules.json written to disk.
func TestResearchCandidateApproveFlow(t *testing.T) {
	s, db, dir := newTestResearchServer(t)

	// 种一条候选
	// English: Seed one candidate.
	w := `{"EP_ttm":0.4,"BP":0.3,"Mom20":0.3}`
	id, err := db.SaveCandidate(&store.Candidate{
		Kind: "weights", Status: "proposed", Factors: `["EP_ttm","BP","Mom20"]`,
		Weights: w, Metric: 0.35, ICMean: 0.05, IR: 0.35, AvgExcess: 0.012,
		Horizon: 5, Reason: "通过护栏",
	})
	if err != nil || id <= 0 {
		t.Fatalf("SaveCandidate: id=%d err=%v", id, err)
	}

	// 列表：应含该候选
	// English: List: should contain this candidate.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/research/candidates", nil)
	s.handleResearchCandidates(rr, req)
	if rr.Code != 200 {
		t.Fatalf("列表状态码=%d", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	if cands, ok := body["candidates"].([]any); !ok || len(cands) != 1 {
		t.Fatalf("候选数=%v 期望 1", body["candidates"])
	}

	// 审批应用
	// English: Approve-apply.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/research/candidates/"+itoa(id)+"/approve", nil)
	req.SetPathValue("id", itoa(id))
	s.handleResearchApprove(rr, req)
	if rr.Code != 200 {
		t.Fatalf("审批状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	c, _ := db.CandidateByID(id)
	if c.Status != "applied" {
		t.Fatalf("审批后状态=%s 期望 applied", c.Status)
	}
	// applied_rules.json 落盘且含权重
	// English: applied_rules.json written to disk and contains the weights.
	raw, err := os.ReadFile(filepath.Join(dir, "applied_rules.json"))
	if err != nil {
		t.Fatalf("applied_rules.json 未生成: %v", err)
	}
	if !strings.Contains(string(raw), `"EP_ttm"`) {
		t.Fatalf("applied_rules.json 缺少权重: %s", raw)
	}

	// 驳回另一条
	// English: Reject another candidate.
	id2, _ := db.SaveCandidate(&store.Candidate{Kind: "weights", Status: "proposed", Factors: "[]", Weights: "{}", Metric: 0, ICMean: 0, IR: 0.05, Horizon: 5})
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/research/candidates/"+itoa(id2)+"/reject", nil)
	req.SetPathValue("id", itoa(id2))
	s.handleResearchReject(rr, req)
	if rr.Code != 200 {
		t.Fatalf("驳回状态码=%d", rr.Code)
	}
	c2, _ := db.CandidateByID(id2)
	if c2.Status != "rejected" {
		t.Fatalf("驳回后状态=%s 期望 rejected", c2.Status)
	}
}

// TestResearchCandidatesNoDB 未接入研究库时应返回 503。
// English: TestResearchCandidatesNoDB should return 503 when no research DB is attached.
func TestResearchCandidatesNoDB(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	s.handleResearchCandidates(rr, httptest.NewRequest(http.MethodGet, "/api/research/candidates", nil))
	if rr.Code != 503 {
		t.Fatalf("未接入研究库状态码=%d 期望 503", rr.Code)
	}
}

// TestResearchProgress 研究进度端点：空库与已加载库均返回完整统计（不 panic）。
// English: TestResearchProgress research progress endpoint: both empty and loaded DBs return full stats (no panic).
func TestResearchProgress(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// 种一只股票 + 一条候选，验证统计反映真实数据
	// English: Seed one stock + one candidate to verify the stats reflect real data.
	if _, err := db.InsertRows("stocks", store.TableColumns("stocks"), []map[string]any{
		{"ts_code": "600580.SH", "name": "卧龙电驱", "area": "浙江", "industry": "电机", "list_date": "20021201"},
	}); err != nil {
		t.Fatalf("insert stock: %v", err)
	}
	// 种一条近一年的日线（dataload 时段内），验证 ready_stocks 计入
	// English: Seed one day's daily bar within the past year (within dataload period) to verify ready_stocks counting.
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), []map[string]any{
		{"ts_code": "600580.SH", "trade_date": "20260817", "open": 10.0, "high": 11.0, "low": 9.5, "close": 10.5, "vol": 1000000, "amount": 10500000},
	}); err != nil {
		t.Fatalf("insert daily: %v", err)
	}
	if _, err := db.SaveCandidate(&store.Candidate{Kind: "weights", Status: "applied", Factors: "[]", Weights: "{}", Metric: 0, ICMean: 0, IR: 0.3, Horizon: 5}); err != nil {
		t.Fatalf("save candidate: %v", err)
	}

	rr := httptest.NewRecorder()
	s.handleResearchProgress(rr, httptest.NewRequest(http.MethodGet, "/api/research/progress", nil))
	if rr.Code != 200 {
		t.Fatalf("进度状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析进度失败: %v", err)
	}
	if body["stocks"] != float64(1) {
		t.Fatalf("stocks=%v 期望 1", body["stocks"])
	}
	if body["candidates"] != float64(1) {
		t.Fatalf("candidates=%v 期望 1", body["candidates"])
	}
	if body["applied"] != float64(1) {
		t.Fatalf("applied=%v 期望 1", body["applied"])
	}
	if body["db_attached"] != true {
		t.Fatalf("db_attached=%v 期望 true", body["db_attached"])
	}

	// 未接入研究库 → 503
	// English: No research DB attached → 503.
	s2 := &Server{}
	rr2 := httptest.NewRecorder()
	s2.handleResearchProgress(rr2, httptest.NewRequest(http.MethodGet, "/api/research/progress", nil))
	if rr2.Code != 503 {
		t.Fatalf("未接入研究库状态码=%d 期望 503", rr2.Code)
	}
	_ = db
}

// itoa 手写整数转字符串（测试辅助）。
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

// TestResearchLibraryEndpoints 战法库：应用因子候选 → 列表 → 启用/禁用 → 删除。
// English: TestResearchLibraryEndpoints strategy library: apply factor candidate → list → enable/disable → delete.
func TestResearchLibraryEndpoints(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	// 种一条 factor 候选并审批应用 → 进战法库
	// English: Seed a factor candidate and approve-apply it → enters the strategy library.
	cid, err := db.SaveCandidate(&store.Candidate{
		Kind: "factor", Status: "proposed", Factors: `["Mom20"]`,
		Weights: `{"weights":{"Mom20":1},"directions":{"Mom20":1},"buy_threshold":70}`,
		Horizon: 5, IR: 0.3, AvgExcess: 0,
	})
	if err != nil || cid <= 0 {
		t.Fatalf("SaveCandidate: %v", err)
	}
	// 审批
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/research/candidates/"+itoa(cid)+"/approve", nil)
	req.SetPathValue("id", itoa(cid))
	s.handleResearchApprove(rr, req)
	if rr.Code != 200 {
		t.Fatalf("审批失败 code=%d body=%s", rr.Code, rr.Body.String())
	}
	// 列表应含 1 条
	// English: List should contain 1 entry.
	rr2 := httptest.NewRecorder()
	s.handleResearchLibrary(rr2, httptest.NewRequest(http.MethodGet, "/api/research/library", nil))
	var listResp struct {
		Library []map[string]any `json:"library"`
	}
	json.Unmarshal(rr2.Body.Bytes(), &listResp)
	if len(listResp.Library) != 1 {
		t.Fatalf("战法库应有 1 条, got %d", len(listResp.Library))
	}
	entry := listResp.Library[0]
	id, _ := entry["id"].(string)
	if id == "" || entry["enabled"] != true {
		t.Fatalf("条目异常: %+v", entry)
	}
	// 禁用
	rr3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/api/research/library/"+id+"/disable", nil)
	req3.SetPathValue("id", id)
	s.handleResearchLibraryToggle("disable")(rr3, req3)
	if rr3.Code != 200 {
		t.Fatalf("禁用失败 code=%d body=%s", rr3.Code, rr3.Body.String())
	}
	// 删除
	rr4 := httptest.NewRecorder()
	req4 := httptest.NewRequest(http.MethodPost, "/api/research/library/"+id+"/delete", nil)
	req4.SetPathValue("id", id)
	s.handleResearchLibraryDelete(rr4, req4)
	if rr4.Code != 200 {
		t.Fatalf("删除失败 code=%d body=%s", rr4.Code, rr4.Body.String())
	}
	rr5 := httptest.NewRecorder()
	s.handleResearchLibrary(rr5, httptest.NewRequest(http.MethodGet, "/api/research/library", nil))
	var empty struct {
		Library []any `json:"library"`
	}
	json.Unmarshal(rr5.Body.Bytes(), &empty)
	if len(empty.Library) != 0 {
		t.Fatalf("删除后战法库应为空, got %d", len(empty.Library))
	}
}

// TestCandidateBacktestRouting §8.6-B 路由：factor 候选 → backtest_candidate(B4)；
// pattern 候选 → backtest_strategy(payload 带 candidate_id)；同 ref 重复点击幂等；
// 列表接口对 pattern 候选附带回测证据（backtest_done/result_text）。
func TestCandidateBacktestRouting(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	fid, _ := db.SaveCandidate(&store.Candidate{Kind: "factor", Status: "proposed",
		Factors: `["Mom20"]`, Weights: `{}`})
	pid, _ := db.SaveCandidate(&store.Candidate{Kind: "pattern", Status: "proposed",
		Factors: `[{"factor":"Brk20","min":0.5,"max":1.5}]`, Weights: `{}`})

	post := func(cid int64) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/research/candidates/"+itoa(cid)+"/backtest", nil)
		req.SetPathValue("id", itoa(cid))
		s.handleCandidateBacktest(rr, req)
		return rr
	}

	if rr := post(fid); rr.Code != 202 {
		t.Fatalf("factor 回测状态码=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := post(pid); rr.Code != 202 {
		t.Fatalf("pattern 回测状态码=%d body=%s", rr.Code, rr.Body.String())
	}

	tf, err := db.LatestTaskByRef(store.TaskBacktestCandidate, fid)
	if err != nil || tf == nil || tf.Type != store.TaskBacktestCandidate || tf.Priority != "high" {
		t.Fatalf("factor 任务错误: %+v err=%v", tf, err)
	}
	tp, err := db.LatestTaskByRef(store.TaskBacktestStrategy, pid)
	if err != nil || tp == nil {
		t.Fatalf("pattern 任务缺失: err=%v", err)
	}
	if tp.Type != store.TaskBacktestStrategy || tp.Priority != "high" {
		t.Fatalf("pattern 类型/优先级错误: %+v", tp)
	}
	var p map[string]any
	if json.Unmarshal([]byte(tp.Payload), &p) != nil || p["candidate_id"].(float64) != float64(pid) {
		t.Fatalf("payload 应含 candidate_id: %s", tp.Payload)
	}

	// 幂等：重复发起不新增行
	before, _ := db.ListResearchTasks()
	post(fid)
	after, _ := db.ListResearchTasks()
	if len(before) != len(after) {
		t.Fatalf("重复发起产生新任务: %d→%d", len(before), len(after))
	}

	// 列表证据：模拟 pattern 任务 done → 候选列表 backtest_done=true + result_text
	db.UpdateTaskRunState(tp.ID, store.TaskDone, "100%", 0, "胜率 55.2%", "")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/research/candidates", nil)
	s.handleResearchCandidates(rr, req)
	var body struct {
		Candidates []struct {
			ID           int64  `json:"id"`
			BacktestDone bool   `json:"backtest_done"`
			BacktestText string `json:"backtest_result_text"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析列表失败: %v", err)
	}
	for _, c := range body.Candidates {
		if c.ID == pid && (!c.BacktestDone || c.BacktestText == "") {
			t.Fatalf("pattern 候选证据缺失: %+v", c)
		}
	}
}
