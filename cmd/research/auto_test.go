// §2026-09-05 多轮发现 + 护栏分级 + 参数快照 + 去重的命令层自动化测试。
// 覆盖：resolveFactorPool 风格子池、护栏分级、组合判等/近似判等、
// 候选龄期计算、--since 回填候选选择。
// English: automated tests for the multi-round discovery command layer —
// style-pool resolution, guard tiering, combo equality, candidate age, since-backfill selection.
package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

// openTestDB 临时研究库（含 research_candidates 建表与 guard/params 迁移）。
// English: opens a temporary research DB (schema + guard/params migration applied).
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestResolveFactorPool 风格子池解析：显式列表优先；""/all=全池；组合名展开且去重。
func TestResolveFactorPool(t *testing.T) {
	if got := resolveFactorPool("EP_ttm,Mom20", ""); len(got) != 2 || got[0] != "EP_ttm" || got[1] != "Mom20" {
		t.Errorf("显式列表应原样返回: %v", got)
	}
	if got := resolveFactorPool("", ""); got != nil {
		t.Errorf("空池应返回 nil（全池兜底），实际 %v", got)
	}
	if got := resolveFactorPool("", "all"); got != nil {
		t.Errorf("all 池应返回 nil（全池兜底），实际 %v", got)
	}
	// mom_liq = 动量 + 流动性 大类并集（去重）
	momliq := resolveFactorPool("", "mom_liq")
	if len(momliq) == 0 {
		t.Fatal("mom_liq 池不应为空")
	}
	seen := map[string]bool{}
	for _, id := range momliq {
		d, ok := factor.Get(id)
		if !ok {
			t.Errorf("池内含未注册因子 %s", id)
		}
		if d.Cat != factor.CatMomentum && d.Cat != factor.CatLiquidity {
			t.Errorf("%s 不属于动量/流动性大类", id)
		}
		if seen[id] {
			t.Errorf("池内因子重复: %s", id)
		}
		seen[id] = true
	}
	// 单一大类名（value）应只含估值类
	val := resolveFactorPool("", "value")
	for _, id := range val {
		if d, ok := factor.Get(id); ok && d.Cat != factor.CatValue {
			t.Errorf("%s 不属于估值大类", id)
		}
	}
}

// TestFactorGuardTier C2 护栏分级边界：strong ≥ guardStrong；standard ≥ minIR；weak ≥ guardWeak；否则 reject。
func TestFactorGuardTier(t *testing.T) {
	cases := []struct {
		outIR, strong, standard, weak float64
		want                          string
	}{
		{0.5, 0.45, 0.3, 0.2, "strong"},
		{0.45, 0.45, 0.3, 0.2, "strong"},
		{0.35, 0.45, 0.3, 0.2, "standard"},
		{0.3, 0.45, 0.3, 0.2, "standard"},
		{0.25, 0.45, 0.3, 0.2, "weak"},
		{0.2, 0.45, 0.3, 0.2, "weak"},
		{0.1, 0.45, 0.3, 0.2, "reject"},
	}
	for _, tc := range cases {
		if got := factorGuardTier(tc.outIR, tc.strong, tc.standard, tc.weak); got != tc.want {
			t.Errorf("factorGuardTier(%.2f)=%s, want %s", tc.outIR, got, tc.want)
		}
	}
}

// TestComboEquality 组合判等：乱序 JSON 数组也应判等；不同组合/坏 JSON 判不等。
func TestComboEquality(t *testing.T) {
	if !comboEqual([]string{"Mom20", "Brk60"}, `["Brk60","Mom20"]`) {
		t.Error("乱序组合应判等")
	}
	if comboEqual([]string{"Mom20", "Brk60"}, `["Brk60","Mom21"]`) {
		t.Error("不同组合不应判等")
	}
	if comboEqual([]string{"Mom20"}, `not-json`) {
		t.Error("坏 JSON 不应判等")
	}
	if comboKey([]string{"b", "a"}) != comboKey([]string{"a", "b"}) {
		t.Error("comboKey 应排序不敏感")
	}
}

// TestCandidateAgeDays 候选龄期解析：完整时间戳/纯日期/YYYYMMDD/坏值回退 0。
func TestCandidateAgeDays(t *testing.T) {
	if candidateAgeDays("") != 0 {
		t.Error("空串应回退 0")
	}
	if candidateAgeDays("garbage") != 0 {
		t.Error("坏格式应回退 0")
	}
	if d := candidateAgeDays(time.Now().Format("2006-01-02 15:04:05")); d < 0 || d >= 1 {
		t.Errorf("刚创建的候选龄期应 <1 天，实际 %.2f", d)
	}
	if candidateAgeDays("20200101") <= 0 {
		t.Error("2020 年候选龄期应 > 0 天")
	}
}

// TestFactorCandidatesForBackfill 回填候选选择：
// --since 只取当日以来全部 proposed factor 候选（升序）；--id 单条优先。
func TestFactorCandidatesForBackfill(t *testing.T) {
	db := openTestDB(t)
	oldF, _ := json.Marshal([]string{"EP_ttm"})
	w, _ := json.Marshal(map[string]float64{"EP_ttm": 1})
	oldID, err := db.SaveCandidate(&store.Candidate{
		Kind: "factor", Status: store.CandProposed, Guard: "standard",
		Factors: string(oldF), Weights: string(w), IR: 0.3, Horizon: 5,
	})
	if err != nil {
		t.Fatalf("保存旧候选: %v", err)
	}
	// 新候选把 created_at 明确改成"当天"（SaveCandidate 默认取 now）
	newF, _ := json.Marshal([]string{"Mom20"})
	newID, err := db.SaveCandidate(&store.Candidate{
		Kind: "factor", Status: store.CandProposed, Guard: "strong",
		Factors: string(newF), Weights: string(w), IR: 0.5, Horizon: 5,
		CreatedAt: "2026-09-05 16:00:00",
	})
	if err != nil {
		t.Fatalf("保存新候选: %v", err)
	}

	// --since 今日 → 两条（old 的 CreatedAt 是 SaveCandidate 写入的真实 now，>= 今日字面前缀）
	sinceCands, err := factorCandidatesForBackfill(db, 0, "20260905")
	if err != nil || len(sinceCands) != 2 {
		t.Fatalf("since 应回填 2 条, got %d err=%v", len(sinceCands), err)
	}
	if sinceCands[0].ID != oldID || sinceCands[1].ID != newID {
		t.Errorf("since 结果应按 id 升序（old<new）: %+v", ids(sinceCands))
	}
	// --id 单条优先于 since
	single, err := factorCandidatesForBackfill(db, newID, "20260905")
	if err != nil || len(single) != 1 || single[0].ID != newID {
		t.Errorf("--id 应返回单条新候选, got %+v err=%v", single, err)
	}
	// 缺省 → 最近一条 proposed factor
	latest, err := factorCandidatesForBackfill(db, 0, "")
	if err != nil || len(latest) != 1 || latest[0].ID != newID {
		t.Errorf("缺省应取最近一条, got %+v err=%v", latest, err)
	}
}

func ids(cs []store.Candidate) []int64 {
	out := make([]int64, len(cs))
	for i := range cs {
		out[i] = cs[i].ID
	}
	return out
}
