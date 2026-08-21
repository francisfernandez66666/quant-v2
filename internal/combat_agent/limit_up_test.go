package combat_agent

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

// TestClassifyLimitUp 验证涨停原因分类：按关键词命中政策/业绩/题材/舆情，
// 无新闻或新闻无命中关键词时归为"情绪技术"。
func TestClassifyLimitUp(t *testing.T) {
	cases := []struct {
		name string
		news []string
		want string
	}{
		{"政策", []string{"国务院印发关于设备更新改造的通知"}, "政策驱动"},
		{"业绩", []string{"公司中报预增180%"}, "业绩驱动"},
		{"题材", []string{"公司中标大额订单"}, "题材事件"},
		{"舆情", []string{"互动易回复引发市场关注"}, "消息舆情"},
		{"无新闻", nil, "情绪技术"},
		{"无关新闻", []string{"今日天气晴"}, "情绪技术"},
	}
	for _, c := range cases {
		got := ClassifyLimitUp(data.LimitUpStock{}, c.news)
		if got != c.want {
			t.Errorf("%s: ClassifyLimitUp = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestCheckExpectationGap 验证预期差检测：
// 利好不涨触发、涨停兑现不触发、利空不跌触发、空新闻不触发。
func TestCheckExpectationGap(t *testing.T) {
	g := CheckExpectationGap("公司中标大单", true, -2.0, 5, 0)
	if !g.Trigger || g.GapType != GapBullishNoRise {
		t.Errorf("利好不涨: got %+v", g)
	}
	g = CheckExpectationGap("公司中标大单", true, 10.0, 5, 0)
	if g.Trigger {
		t.Errorf("涨停兑现不应触发: got %+v", g)
	}
	g = CheckExpectationGap("减持公告", false, 3.0, 5, 0)
	if !g.Trigger || g.GapType != GapBearishNoDrop {
		t.Errorf("利空不跌: got %+v", g)
	}
	g = CheckExpectationGap("", true, -5, 5, 0)
	if g.Trigger {
		t.Errorf("空新闻不应触发: got %+v", g)
	}
}

// TestScoreLeaderWeights 验证龙头评分：强票（高连板/早封板/高封单/低换手）
// 评分应在 (0,100] 区间，且明显高于弱票（首板/午后封板/低封单/高换手）。
func TestScoreLeaderWeights(t *testing.T) {
	industryCnt := map[string]int{"燃气": 1}
	industryStocks := map[string][]data.LimitUpStock{
		"燃气": {{Code: "600001", Name: "A", FirstSeal: "09:25"}},
	}
	total := 1
	s := data.LimitUpStock{
		Code: "600001", Name: "A", Price: 10,
		LianBan: 5, FirstSeal: "09:25", SealRatio: 6, Turnover: 8,
		BreakCount: 0, Industry: "燃气",
	}
	score, _ := ScoreLeader(s, industryCnt, industryStocks, total)
	if score <= 0 || score > 100 {
		t.Errorf("ScoreLeader = %v, want (0,100]", score)
	}
	// 弱票：首板、午后封板、低封单、高换手 → 明显低于强票
	weak := data.LimitUpStock{
		Code: "600002", Name: "B", Price: 20,
		LianBan: 1, FirstSeal: "14:30", SealRatio: 0.2, Turnover: 40,
		BreakCount: 3, Industry: "燃气",
	}
	weakScore, _ := ScoreLeader(weak, industryCnt, industryStocks, total)
	if weakScore >= score {
		t.Errorf("弱票(%v)应低于强票(%v)", weakScore, score)
	}
}

// TestAnalyzeLimitUpEmpty 验证空涨停池的兜底行为：总数 0、龙头列表为空。
func TestAnalyzeLimitUpEmpty(t *testing.T) {
	res := AnalyzeLimitUp(nil, nil)
	if res.Total != 0 || res.Leaders != nil {
		t.Errorf("空池: got %+v", res)
	}
}

// TestScanLimitUpLeaderBuy 验证龙头识别信号放宽买入：评分≥60 且排名前 10 → Action=buy（可交易）。
// English: verifies the leader-ID buy relaxation — score ≥60 and top-10 rank yields Action=buy (tradeable).
func TestScanLimitUpLeaderBuy(t *testing.T) {
	a := New(&config.StrategyConfig{})
	pool := []data.LimitUpStock{
		{Code: "600001", Name: "强票", Price: 10, LianBan: 5, FirstSeal: "09:25", SealRatio: 6, Turnover: 8, Industry: "燃气"},
		{Code: "600002", Name: "弱票", Price: 20, LianBan: 1, FirstSeal: "14:30", SealRatio: 0.2, Turnover: 40, BreakCount: 3, Industry: "燃气"},
	}
	input := ScanInput{LimitUpPool: pool}
	sigs := a.ScanLimitUp(input)
	foundStrong := false
	for _, s := range sigs {
		if s.Code == "600001" && s.Strategy == "龙头识别" {
			foundStrong = true
			if s.Action != "buy" {
				t.Errorf("强票龙头评分应放行为 buy, 实际 %q (score=%s)", s.Action, s.Reason)
			}
		}
	}
	if !foundStrong {
		t.Errorf("应产出 600001 龙头识别信号, 实际 %+v", sigs)
	}
}
