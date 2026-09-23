// market_emotion_test.go — §情绪面板 A/B 端点回归（2026-09-13）。
// 锁死三件事：
//  1. trade_date 库内 YYYYMMDD 紧凑格式 → API 输出必须归一化为 YYYY-MM-DD（前端 slice(5) 依赖）；
//  2. days 参数缺省 30 / 非法回落 / 上限 250（回看页一年跨度）；
//  3. 矩阵端点结构契约：phases 六相位固定序 + min_events=20 样本纪律 + thin 标记；
//  4. §ADJ-BASIS：矩阵断点必须写在当前复权口径位上（缓存键含 adj_basis，跨口径混装即假统计）。
//
// English: emotion panel endpoint tests — compact-vs-ISO date normalization contract,
// days clamp, and matrix response shape (phase order, min_events thinning).
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/store"
)

// newEmotionDB 构造带情绪断点数据的研究库：两个候选 × 冰点相位 3+1 条事件 + 引擎日终标注。
func newEmotionDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// 引擎判定（market_risk_daily，YYYYMMDD 落库——与生产 data.TradingDayDate 同格式）
	if err := db.UpsertMarketRiskDaily(store.MarketRiskDailyRow{
		TradeDate: "20260907", Emotion: "冰点", LimitUpCount: 15, LadderHeight: 2, Reasons: "涨停少",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMarketRiskDaily(store.MarketRiskDailyRow{
		TradeDate: "20260908", Emotion: "高潮", LimitUpCount: 120, LadderHeight: 6,
	}); err != nil {
		t.Fatal(err)
	}
	// 候选 + 逐事件回测断点（result_json 与 backtest.EventResult 形状对齐）
	cid, err := db.SaveCandidate(&store.Candidate{Kind: "factor", Factors: "[]", Weights: "{}", Reason: "测试战法甲"})
	if err != nil {
		t.Fatal(err)
	}
	ev := func(limitUp int, excess, hit float64) string {
		b, _ := json.Marshal(map[string]any{
			"mean_excess":    map[string]float64{"5": excess},
			"hit_rate":       map[string]float64{"5": hit},
			"limit_up_count": limitUp,
		})
		return string(b)
	}
	// 冰点 2 事件（hit_rate 为 0-1 比例口径——链式回测 er.HitRate = wins/n；
	// 存储粒度 candidate×date×industry，两条须不同行业否则 upsert 互相覆盖）
	for i, e := range []string{ev(15, 1.0, 0.5), ev(15, 2.0, 0.7)} {
		if err := db.UpsertBacktestEventResult(cid, "20260907", "测试行业甲"+string(rune('1'+i)), "fp", research.AdjBaselineVersion, e); err != nil {
			t.Fatal(err)
		}
	}
	// 高潮 1 事件（薄桶）
	if err := db.UpsertBacktestEventResult(cid, "20260908", "测试行业", "fp-c", research.AdjBaselineVersion, ev(120, 3.0, 0.9)); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestEmotionHistoryDateNormalize 历史端点：紧凑日期归一化 + days 缺省/钳制 + reasons 缺席语义。
func TestEmotionHistoryDateNormalize(t *testing.T) {
	s := &Server{researchDB: newEmotionDB(t)}
	rr := httptest.NewRecorder()
	s.handleMarketEmotionHistory(rr, httptest.NewRequest(http.MethodGet, "/api/market/emotion/history", nil))
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Days   int              `json:"days"`
		Series []map[string]any `json:"series"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Days != 30 {
		t.Fatalf("days 缺省应为 30, got %d", resp.Days)
	}
	if len(resp.Series) != 2 {
		t.Fatalf("应有 2 条, got %d", len(resp.Series))
	}
	// 核心契约：YYYYMMDD → YYYY-MM-DD（前端 ribbon 的 slice(5) 展示依赖）
	if got := resp.Series[0]["date"]; got != "2026-09-07" {
		t.Fatalf("date 未归一化: %v", got)
	}
	if _, ok := resp.Series[1]["reasons"]; ok {
		t.Fatalf("空 reasons 字段应缺席而非空串")
	}
	// days 非法值回落 30；超上限钳到 250
	rr2 := httptest.NewRecorder()
	s.handleMarketEmotionHistory(rr2, httptest.NewRequest(http.MethodGet, "/api/market/emotion/history?days=abc", nil))
	var r2 struct{ Days int }
	json.Unmarshal(rr2.Body.Bytes(), &r2)
	if r2.Days != 30 {
		t.Fatalf("days=abc 应回落 30, got %d", r2.Days)
	}
	rr3 := httptest.NewRecorder()
	s.handleMarketEmotionHistory(rr3, httptest.NewRequest(http.MethodGet, "/api/market/emotion/history?days=9999", nil))
	var r3 struct{ Days int }
	json.Unmarshal(rr3.Body.Bytes(), &r3)
	if r3.Days != 250 {
		t.Fatalf("days 应钳制到 250, got %d", r3.Days)
	}
}

// TestEmotionStrategyMatrixShape 矩阵端点：六相位固定序 + thin 样本纪律 + 命中比例口径透传。
func TestEmotionStrategyMatrixShape(t *testing.T) {
	s := &Server{researchDB: newEmotionDB(t)}
	rr := httptest.NewRecorder()
	s.handleEmotionStrategyMatrix(rr, httptest.NewRequest(http.MethodGet, "/api/research/emotion-strategy-matrix", nil))
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Phases    []string `json:"phases"`
		MinEvents int      `json:"min_events"`
		AdjBasis  string   `json:"adj_basis"`
		Rows      []struct {
			CandidateID int64               `json:"candidate_id"`
			Name        string              `json:"name"`
			Cells       []store.EmotionCell `json:"cells"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Phases) != 6 || resp.MinEvents != store.EmotionMatrixRowMinEvents {
		t.Fatalf("契约漂移: phases=%v min_events=%d", resp.Phases, resp.MinEvents)
	}
	// §ADJ-BASIS 契约：矩阵必须声明自己算在哪个复权口径上（与 research 的单点定义同源）。
	if resp.AdjBasis != research.AdjBaselineVersion {
		t.Fatalf("adj_basis 契约漂移: %q != %q", resp.AdjBasis, research.AdjBaselineVersion)
	}
	if len(resp.Rows) != 1 || resp.Rows[0].Name != "测试战法甲" {
		t.Fatalf("应 1 行且展示名取 reason 首行: %+v", resp.Rows)
	}
	cells := resp.Rows[0].Cells
	if len(cells) != 2 || cells[0].Phase != "冰点" || cells[1].Phase != "高潮" {
		t.Fatalf("相位桶缺失: %+v", cells)
	}
	// 冰点 2 事件 < 20 → thin；均值 (1+2)/2=1.5，命中比例 (0.5+0.7)/2=0.6（0-1 口径原样透传，前端 ×100 显示）
	if !cells[0].Thin || cells[0].Events != 2 {
		t.Fatalf("thin 纪律失效: %+v", cells[0])
	}
	if got := cells[0].AvgExcess[5]; got != 1.5 {
		t.Fatalf("均值超额错误: %v", got)
	}
	if got := cells[0].HitRate[5]; got < 0.599 || got > 0.601 {
		t.Fatalf("命中率口径漂移（应 0-1 原样）: %v", got)
	}
}
