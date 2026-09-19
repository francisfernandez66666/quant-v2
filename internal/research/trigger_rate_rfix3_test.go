// §RFIX-3 回归测试：预期触发率估算（runner 分位口径近似）与 lifecycle 零观测反静默。
package research

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/factor"
)

// mkTrendPanel 构造单调面板：dir=+1 时因子随时间上升（时间序列分位≈1 → 高分），
// dir=-1/下降时同样本给低分——用已知分位反推 runner 口径复合分。
func mkTrendPanel(k int, dates []string, rising bool) *Panel {
	idx := make(map[string]int, len(dates))
	for i, d := range dates {
		idx[d] = i
	}
	vals := make([]float64, len(dates))
	for i := range dates {
		if rising {
			vals[i] = float64(i) + float64(k)
		} else {
			vals[i] = float64(len(dates)-i) + float64(k)
		}
	}
	closes := make([]float64, len(dates))
	for i := range closes {
		closes[i] = 100.0
	}
	return &Panel{
		Code:    fmt.Sprintf("%d", k),
		Series:  &factor.StockSeries{Dates: dates, CloseHfq: closes},
		DateIdx: idx,
		Factors: map[string][]float64{"f1": vals},
	}
}

// TestTriggerRateRunnerScale 分位口径校验：全升面板（末日分位≈1）阈值 95 应几乎全触发；
// 全降面板（末日分位≈0）阈值 70 应零触发。
func TestTriggerRateRunnerScale(t *testing.T) {
	dates := makeDates(20)
	var rise, fall []*Panel
	for k := 0; k < 6; k++ {
		rise = append(rise, mkTrendPanel(k, dates, true))
		fall = append(fall, mkTrendPanel(k, dates, false))
	}
	w := map[string]float64{"f1": 1}
	dirs := map[string]int{"f1": 1}

	est := TriggerRateFromPanels(rise, []string{"f1"}, dirs, w, []float64{70, 95}, dates[10], dates[19], 3)
	if est.Days == 0 {
		t.Fatal("升面板应有统计日")
	}
	if est.PerDay[95] < 3 {
		t.Fatalf("全升面板末段分位≈1，阈值95 日均触发应≈全池，实际 %.2f", est.PerDay[95])
	}

	est = TriggerRateFromPanels(fall, []string{"f1"}, dirs, w, []float64{70, 95}, dates[10], dates[19], 3)
	if est.PerDay[70] > 0 {
		t.Fatalf("全降面板分位≈0，阈值70 预期触发应=0，实际 %.2f", est.PerDay[70])
	}
	// dir=-1 翻转贡献（1-pct）：同样本应人人高分
	est = TriggerRateFromPanels(fall, []string{"f1"}, map[string]int{"f1": -1}, w, []float64{70, 95}, dates[10], dates[19], 3)
	if est.PerDay[95] < 3 {
		t.Fatalf("dir=-1 时降分位应转为高贡献，实际 %.2f", est.PerDay[95])
	}
	// 样本不足日不计数：MinStocks 大于池容量 → Days=0
	est = TriggerRateFromPanels(rise, []string{"f1"}, dirs, w, []float64{70}, "", "", 100)
	if est.Days != 0 {
		t.Fatalf("池小于 minStocks 不应有统计日，实际 %d", est.Days)
	}
}

// TestTriggerRateNoLookahead 分位只用截至当日的历史：升面板在首日分位≈1/1=1、
// 末日≈1，但降面板首日分位=1（当日为历史最大）→ 首日阈值95 触发、末段不触发，
// 证明统计不含未来数据。
func TestTriggerRateNoLookahead(t *testing.T) {
	dates := makeDates(20)
	var fall []*Panel
	for k := 0; k < 6; k++ {
		fall = append(fall, mkTrendPanel(k, dates, false))
	}
	w := map[string]float64{"f1": 1}
	dirs := map[string]int{"f1": 1}
	first := TriggerRateFromPanels(fall, []string{"f1"}, dirs, w, []float64{95}, dates[0], dates[0], 3)
	if first.PerDay[95] < 3 {
		t.Fatalf("首日历史分位=1（当日即历史最大）应全员触发，实际 %.2f", first.PerDay[95])
	}
	last := TriggerRateFromPanels(fall, []string{"f1"}, dirs, w, []float64{95}, dates[19], dates[19], 3)
	if last.PerDay[95] > 0 {
		t.Fatalf("末日历史分位最低不应触发，实际 %.2f", last.PerDay[95])
	}
}

// TestFactorTrigEstFlowsIntoResult 端到端（legacy 内核）：候选结果应带 TrigDays>0。
func TestFactorTrigEstFlowsIntoResult(t *testing.T) {
	dates := makeDates(40)
	var panels []*Panel
	for k := 0; k < 5; k++ {
		panels = append(panels, mkStockPanel(dates, k))
	}
	res := DiscoverFactors(panels, DiscoverOpts{
		Factors: []string{"f1", "f2"}, Horizon: 1, MinStocks: 3, MaxFactors: 2,
		SplitPct: 0.6, MinDays: 5, MinIR: 0.05,
	})
	if res.TrigDays == 0 {
		t.Fatal("发现结果应带预期触发统计（TrigDays>0）")
	}
	if math.IsNaN(res.Trig70) || res.Trig70 < 0 || res.Trig95 < 0 {
		t.Fatalf("触发统计异常: 70=%v 95=%v", res.Trig70, res.Trig95)
	}
}

// TestDemoteAppliedRulesZeroObsAlert §RFIX-3 零观测告警位：AppliedAt 超过阈值天数
// 且无成交观测 → keep + ZeroObs=true；负阈值关闭；摘要点名条数。
func TestDemoteAppliedRulesZeroObsAlert(t *testing.T) {
	dir := t.TempDir()
	writePaperJSON(t, dir, nil) // 无任何成交 → 全部规则零观测
	old := time.Now().AddDate(0, 0, -45).Format("2006-01-02 15:04:05")
	fresh := time.Now().AddDate(0, 0, -2).Format("2006-01-02 15:04:05")
	b, _ := json.Marshal([]AppliedFactorEntry{
		{ID: "fac_1", Name: "因子战法#1", Enabled: true, CandID: 1, AppliedAt: old},
		{ID: "fac_2", Name: "因子战法#2", Enabled: true, CandID: 2, AppliedAt: fresh},
	})
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	actions, err := DemoteAppliedRules(dir, "", DemoteOpts{}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DemoteAction{}
	for _, a := range actions {
		got[a.Verdict.RuleID] = a
	}
	if len(actions) != 2 {
		t.Fatalf("零观测规则也应留痕（2 条 actions），实际 %d", len(actions))
	}
	if !got["fac_1"].ZeroObs || got["fac_1"].DaysSinceApply < 45 {
		t.Fatalf("fac_1 上线 45 日零观测应告警: %+v", got["fac_1"])
	}
	if got["fac_2"].ZeroObs {
		t.Fatalf("fac_2 上线 2 日 <30 不应告警: %+v", got["fac_2"])
	}
	sum := LifecycleDemoteSummary(actions)
	if !strings.Contains(sum, "零观测告警 1 条") {
		t.Fatalf("摘要应点名零观测条数，实际: %s", sum)
	}
	// 负阈值关闭告警位（判定分支保留留痕）
	actions, _ = DemoteAppliedRules(dir, "", DemoteOpts{ZeroObsDays: -1}, false)
	for _, a := range actions {
		if a.ZeroObs {
			t.Fatalf("ZeroObsDays<0 应关闭告警: %+v", a)
		}
	}
}
