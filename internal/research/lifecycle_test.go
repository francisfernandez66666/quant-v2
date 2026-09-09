package research

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/store"
)

func newTestStore(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/lifecycle.db")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func todayString() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// TestEvaluateGrayscalePromote 20 日观察期已满 + 样本充足 + 全指标达标 → promote。
func TestEvaluateGrayscalePromote(t *testing.T) {
	gs := &grayscaleFile{
		Factors: []GrayscaleFactorRule{
			{ID: "gfac_1", CandID: 1, EnteredAt: "2020-01-01 00:00:00", ObservationDays: 20},
		},
	}
	// 10 笔稳定正收益（IR 高、胜率高、回撤小）
	returns := []float64{2, 1.5, 2.5, 1, 2, 1.8, 2.2, 1.2, 1.5, 2.4}
	verds := EvaluateGrayscale(gs, map[string][]float64{"fac_1": returns}, PromotionOpts{})
	if len(verds) != 1 {
		t.Fatalf("应产出 1 条判定, got %d", len(verds))
	}
	v := verds[0]
	if v.Verdict != "promote" {
		t.Fatalf("全指标达标应 promote, got %q reason=%s", v.Verdict, v.Reason)
	}
	if v.Trades != 10 {
		t.Errorf("样本数应 10, got %d", v.Trades)
	}
}

// TestEvaluateGrayscaleObservationWindow 观察期未满 → pending。
func TestEvaluateGrayscaleObservationWindow(t *testing.T) {
	gs := &grayscaleFile{
		Factors: []GrayscaleFactorRule{
			// EnteredAt 今天 → 观察期 0 天
			{ID: "gfac_1", CandID: 1, EnteredAt: todayString(), ObservationDays: 20},
		},
	}
	verds := EvaluateGrayscale(gs, map[string][]float64{"fac_1": {2, 2, 2}}, PromotionOpts{})
	if verds[0].Verdict != "pending" {
		t.Fatalf("观察期未满应 pending, got %q", verds[0].Verdict)
	}
}

// TestEvaluateGrayscaleReject IR 不足或回撤过大 → reject。
func TestEvaluateGrayscaleReject(t *testing.T) {
	gs := &grayscaleFile{
		Factors: []GrayscaleFactorRule{
			{ID: "gfac_1", CandID: 1, EnteredAt: "2020-01-01 00:00:00", ObservationDays: 20},
		},
	}
	// 亏损多、胜率低、回撤大的组合
	bad := []float64{5, -6, 5, -7, 5, -8, 5, -9, 5, -10}
	verds := EvaluateGrayscale(gs, map[string][]float64{"fac_1": bad}, PromotionOpts{})
	v := verds[0]
	if v.Verdict != "reject" {
		t.Fatalf("差样本应 reject, got %q reason=%s", v.Verdict, v.Reason)
	}
	if v.ProfitFactor >= 1.2 {
		t.Errorf("盈亏比应<1.2, got %.2f", v.ProfitFactor)
	}
}

// TestEvaluateGrayscaleMissingPool 无观测池数据 → pending。
func TestEvaluateGrayscaleMissingPool(t *testing.T) {
	gs := &grayscaleFile{
		Patterns: []GrayscalePatternRule{
			{ID: "gpat_2", CandID: 2, EnteredAt: "2020-01-01 00:00:00", ObservationDays: 20},
		},
	}
	verds := EvaluateGrayscale(gs, nil, PromotionOpts{})
	if len(verds) != 1 || verds[0].Verdict != "pending" {
		t.Fatalf("无池数据应 pending, got %+v", verds)
	}
}

// TestEvaluateDemoteDisable 连续 3 日低于阈值 → disable。
func TestEvaluateDemoteDisable(t *testing.T) {
	days := []DailyStat{
		{IR: -0.1, WinRate: 30, Trades: 5},
		{IR: -0.2, WinRate: 25, Trades: 4},
		{IR: -0.3, WinRate: 20, Trades: 3},
		{IR: -0.4, WinRate: 15, Trades: 2},
	}
	v := EvaluateDemote(days, DemoteOpts{ConsecDays: 3})
	if v.Verdict != "disable" {
		t.Fatalf("连续3日衰退应 disable, got %q reason=%s", v.Verdict, v.Reason)
	}
	if v.ConsecLow < 3 {
		t.Errorf("连续低位天数应≥3, got %d", v.ConsecLow)
	}
}

// TestEvaluateDemoteKeep 恢复后不再累计 → keep。
func TestEvaluateDemoteKeep(t *testing.T) {
	days := []DailyStat{
		{IR: -0.1, WinRate: 30, Trades: 3},
		{IR: 0.6, WinRate: 60, Trades: 5}, // 恢复
		{IR: -0.1, WinRate: 30, Trades: 3},
		{IR: -0.2, WinRate: 28, Trades: 2},
	}
	v := EvaluateDemote(days, DemoteOpts{ConsecDays: 3})
	if v.Verdict != "keep" {
		t.Fatalf("中途恢复应 keep, got %q", v.Verdict)
	}
}

// TestPromotionCandidates 生成晋升候选并防重复。
func TestPromotionCandidates(t *testing.T) {
	db := newTestStore(t)
	gs := &grayscaleFile{
		Factors: []GrayscaleFactorRule{
			{ID: "gfac_1", CandID: 1, EnteredAt: "2020-01-01 00:00:00", ObservationDays: 20,
				Factors: []string{"EP_ttm", "BP"}, Weights: map[string]float64{"EP_ttm": 0.6, "BP": 0.4},
				Directions: map[string]int{"EP_ttm": 1, "BP": 1}, BuyThreshold: 70},
		},
	}
	verds := []GrayscaleVerdict{
		{RuleID: "gfac_1", CandID: 1, Kind: "factor", Verdict: "promote", Trades: 10, IR: 0.5, WinRate: 60, ProfitFactor: 2, MaxDrawdownPct: 5},
	}
	ids, err := PromotionCandidates(db, gs, verds)
	if err != nil {
		t.Fatalf("生成晋升候选失败: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("应生成 1 条晋升候选, got %d", len(ids))
	}
	// 再次生成 → 防重复跳过
	ids2, err := PromotionCandidates(db, gs, verds)
	if err != nil {
		t.Fatalf("重复生成失败: %v", err)
	}
	if len(ids2) != 0 {
		t.Fatalf("重复生成应被跳过, got %d", len(ids2))
	}
	// 候选行内容校验
	c, err := db.CandidateByID(ids[0])
	if err != nil || c == nil {
		t.Fatalf("读取候选失败: %v", err)
	}
	if c.Guard != "promotion" || !strings.HasPrefix(c.Reason, "[晋升候选]") {
		t.Fatalf("候选标记异常: guard=%s reason=%s", c.Guard, c.Reason)
	}
}

// TestIRAnnualized 正收益年化 IR 为正、负收益为负。
func TestIRAnnualized(t *testing.T) {
	if ir := irAnnualized([]float64{1, 2, 1.5, 2, 1.8}); ir <= 0 {
		t.Errorf("正收益 IR 应>0, got %.3f", ir)
	}
	if ir := irAnnualized([]float64{-1, -2, -1.5}); ir >= 0 {
		t.Errorf("负收益 IR 应<0, got %.3f", ir)
	}
}
