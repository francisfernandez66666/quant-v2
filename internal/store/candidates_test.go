// B5 候选库往返测试：建表 → 保存 → 列表 → 查询 → 状态流转。
// English: B5 candidate store round-trip test: create table → save → list → query → status transition.
package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCandidates 验证 research_candidates 全流程（B5 自动研究闭环）。
// English: TestCandidates verifies the full research_candidates flow (B5 automated research loop).
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
	// English: List all.
	all, err := db.ListCandidates("")
	if err != nil || len(all) != 1 {
		t.Fatalf("ListCandidates 全部: n=%d err=%v", len(all), err)
	}
	if all[0].Weights != string(w) || all[0].IR != 0.35 {
		t.Fatalf("ListCandidates 字段不一致: %+v", all[0])
	}

	// 按状态过滤
	// English: Filter by status.
	if got, _ := db.ListCandidates("proposed"); len(got) != 1 {
		t.Fatalf("ListCandidates proposed: n=%d", len(got))
	}
	if got, _ := db.ListCandidates("applied"); len(got) != 0 {
		t.Fatalf("ListCandidates applied 应为空: n=%d", len(got))
	}

	// 单条查询
	// English: Query a single record.
	c, err := db.CandidateByID(id)
	if err != nil || c.ID != id || c.Kind != "weights" {
		t.Fatalf("CandidateByID: %+v err=%v", c, err)
	}

	// 状态流转 proposed → applied
	// English: Status transition proposed → applied.
	if err := db.UpdateCandidateStatus(id, "applied"); err != nil {
		t.Fatalf("UpdateCandidateStatus: %v", err)
	}
	c, _ = db.CandidateByID(id)
	if c.Status != "applied" {
		t.Fatalf("状态应为 applied，实际 %s", c.Status)
	}
}

// TestCandidateGuardParams §C2/C4 候选护栏档位 + 参数快照列往返。
func TestCandidateGuardParams(t *testing.T) {
	db := testDB(t)

	save := func(kind, status, factors, guard, params string) int64 {
		id, err := db.SaveCandidate(&Candidate{
			Kind: kind, Status: status, Factors: factors,
			IR: 0.3, AvgExcess: 0.01, Horizon: 5,
			Reason: "r", Guard: guard, Params: params,
		})
		if err != nil {
			t.Fatalf("SaveCandidate(%s): %v", kind, err)
		}
		return id
	}
	id := save("factor", "proposed", `["EP_ttm","BP","Mom20"]`, "weak", `{"h":5,"top_n":2,"min_ir":0.3}`)
	c, err := db.CandidateByID(id)
	if err != nil || c == nil {
		t.Fatalf("CandidateByID: %v", err)
	}
	if c.Guard != "weak" || c.Params != `{"h":5,"top_n":2,"min_ir":0.3}` {
		t.Fatalf("guard/params 往返不一致: %+v", c)
	}
	// 默认 guard=standard
	id2 := save("factor", "proposed", `["EP_ttm","BP"]`, "", "")
	c2, _ := db.CandidateByID(id2)
	if c2.Guard != "standard" {
		t.Fatalf("缺省 guard 应为 standard, got %s", c2.Guard)
	}
	// AppendCandidateReason 幂等
	if err := db.AppendCandidateReason(id, "事件数不足(5<20)"); err != nil {
		t.Fatalf("AppendCandidateReason: %v", err)
	}
	if err := db.AppendCandidateReason(id, "事件数不足(5<20)"); err != nil {
		t.Fatalf("AppendCandidateReason 二次追加: %v", err)
	}
	c, _ = db.CandidateByID(id)
	if got := strings.Count(c.Reason, "事件数不足(5<20)"); got != 1 {
		t.Fatalf("reason 重复追加: %q", c.Reason)
	}
}

// TestCandidateDedupQueries §S2 精确 + 近似(Jaccard) 重复判定与候选查询辅助。
func TestCandidateDedupQueries(t *testing.T) {
	db := testDB(t)

	boom := `["EP_ttm","BP","Mom20"]`
	other := `["ROE","YoyNetProfit"]`

	for _, f := range []string{boom, other} {
		if _, err := db.SaveCandidate(&Candidate{
			Kind: "factor", Status: "proposed", Factors: f, IR: 0.3, Horizon: 5,
		}); err != nil {
			t.Fatalf("SaveCandidate: %v", err)
		}
	}

	// 精确重复（状态过滤内）
	exact, err := db.ComboExistsLike([]string{"Mom20", "EP_ttm", "BP"}, CandProposed)
	if err != nil || !exact {
		t.Fatalf("ComboExistsLike 应命中(顺序无关): exact=%v err=%v", exact, err)
	}
	// 状态过滤外不命中
	exact, err = db.ComboExistsLike([]string{"EP_ttm", "BP", "Mom20"}, CandApplied)
	if err != nil || exact {
		t.Fatalf("applied 状态下不应命中 proposed 候选: exact=%v err=%v", exact, err)
	}
	// 近似重复 threshold=0.8 -> 2/3≈0.67 不命中；threshold=0.6 命中
	near, _ := db.ComboNearDup([]string{"Mom20", "EP_ttm"}, 0.8, CandProposed)
	if near {
		t.Fatal("Jaccard {Mom20,EP_ttm} vs {EP_ttm,BP,Mom20}=2/3≈0.67<0.8 不应命中")
	}
	near, _ = db.ComboNearDup([]string{"Mom20", "EP_ttm"}, 0.6, CandProposed)
	if !near {
		t.Fatal("Jaccard 2/3≈0.67≥0.6 应命中近似重复")
	}

	// LatestCandidate
	applied, _ := db.SaveCandidate(&Candidate{
		Kind: "factor", Status: "applied", Factors: boom, IR: 0.5, Horizon: 5,
	})
	if applied <= 0 {
		t.Fatal("applied 候选保存失败")
	}
	lc, err := db.LatestCandidate("factor", CandApplied)
	if err != nil || lc == nil || lc.ID != applied {
		t.Fatalf("LatestCandidate(applied): %+v err=%v", lc, err)
	}

	// ProposedFactorCandidatesSince：按 created_at（今天）过滤
	proposed, err := db.ProposedFactorCandidatesSince(time.Now().Format("20060102"))
	if err != nil {
		t.Fatalf("ProposedFactorCandidatesSince: %v", err)
	}
	if len(proposed) < 2 {
		t.Fatalf("应取到 ≥2 条当日 proposed 因子候选, got %d", len(proposed))
	}
	_ = other
}
