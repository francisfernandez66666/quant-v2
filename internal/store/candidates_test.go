// B5 候选库往返测试：建表 → 保存 → 列表 → 查询 → 状态流转。
package store

import (
	"encoding/json"
	"testing"
)

// TestCandidates 验证 research_candidates 全流程（B5 自动研究闭环）。
func TestCandidates(t *testing.T) {
	db := testDB(t)

	w, _ := json.Marshal(map[string]float64{"EP_ttm": 0.4, "BP": 0.3, "Mom20": 0.3})
	f, _ := json.Marshal([]string{"EP_ttm", "BP", "Mom20"})

	id, err := db.SaveCandidate(&Candidate{
		Kind: "weights", Status: "proposed", Factors: string(f),
		Weights: string(w), Metric: 0.35, ICMean: 0.05, IR: 0.35,
		AvgExcess: 0.012, Horizon: 5, Reason: "通过护栏",
	})
	if err != nil {
		t.Fatalf("SaveCandidate: %v", err)
	}
	if id <= 0 {
		t.Fatalf("SaveCandidate id=%d 期望 >0", id)
	}

	// 列表全部
	all, err := db.ListCandidates("")
	if err != nil || len(all) != 1 {
		t.Fatalf("ListCandidates 全部: n=%d err=%v", len(all), err)
	}
	if all[0].Weights != string(w) || all[0].IR != 0.35 {
		t.Fatalf("ListCandidates 字段不一致: %+v", all[0])
	}

	// 按状态过滤
	if got, _ := db.ListCandidates("proposed"); len(got) != 1 {
		t.Fatalf("ListCandidates proposed: n=%d", len(got))
	}
	if got, _ := db.ListCandidates("applied"); len(got) != 0 {
		t.Fatalf("ListCandidates applied 应为空: n=%d", len(got))
	}

	// 单条查询
	c, err := db.CandidateByID(id)
	if err != nil || c.ID != id || c.Kind != "weights" {
		t.Fatalf("CandidateByID: %+v err=%v", c, err)
	}

	// 状态流转 proposed → applied
	if err := db.UpdateCandidateStatus(id, "applied"); err != nil {
		t.Fatalf("UpdateCandidateStatus: %v", err)
	}
	c, _ = db.CandidateByID(id)
	if c.Status != "applied" {
		t.Fatalf("状态应为 applied，实际 %s", c.Status)
	}
}