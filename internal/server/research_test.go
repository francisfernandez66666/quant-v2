// B5 研究候选审批端点测试：列表 / 审批应用 / 驳回。
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
func TestResearchCandidateApproveFlow(t *testing.T) {
	s, db, dir := newTestResearchServer(t)

	// 种一条候选
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
	raw, err := os.ReadFile(filepath.Join(dir, "applied_rules.json"))
	if err != nil {
		t.Fatalf("applied_rules.json 未生成: %v", err)
	}
	if !strings.Contains(string(raw), `"EP_ttm"`) {
		t.Fatalf("applied_rules.json 缺少权重: %s", raw)
	}

	// 驳回另一条
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
func TestResearchCandidatesNoDB(t *testing.T) {
	s := &Server{}
	rr := httptest.NewRecorder()
	s.handleResearchCandidates(rr, httptest.NewRequest(http.MethodGet, "/api/research/candidates", nil))
	if rr.Code != 503 {
		t.Fatalf("未接入研究库状态码=%d 期望 503", rr.Code)
	}
}

// TestResearchProgress 研究进度端点：空库与已加载库均返回完整统计（不 panic）。
func TestResearchProgress(t *testing.T) {
	s, db, _ := newTestResearchServer(t)

	// 种一只股票 + 一条候选，验证统计反映真实数据
	if _, err := db.InsertRows("stocks", store.TableColumns("stocks"), []map[string]any{
		{"ts_code": "600580.SH", "name": "卧龙电驱", "area": "浙江", "industry": "电机", "list_date": "20021201"},
	}); err != nil {
		t.Fatalf("insert stock: %v", err)
	}
	// 种一条近一年的日线（dataload 时段内），验证 ready_stocks 计入
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
	s2 := &Server{}
	rr2 := httptest.NewRecorder()
	s2.handleResearchProgress(rr2, httptest.NewRequest(http.MethodGet, "/api/research/progress", nil))
	if rr2.Code != 503 {
		t.Fatalf("未接入研究库状态码=%d 期望 503", rr2.Code)
	}
	_ = db
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}