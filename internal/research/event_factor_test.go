// event_factor_test.go — §ENH-4 事件因子装配单测：jsonl 解析容错、两档归属与行业互相包含匹配、
// 同日同票 max|score| 聚合、面板 NaN 对齐。
package research

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/factor"
)

// writeHistory 生成事件归档 jsonl 测试文件（一行 {"trading_day","event"}）。
func writeHistory(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "news_event_history.jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// histRow 构造一条归档行的 JSON 文本。
func histRow(t *testing.T, day, level string, score float64, sectors, stocks []string) string {
	t.Helper()
	inner := map[string]any{"level": level, "score": score, "sectors": sectors, "cleaned_stocks": stocks}
	body, err := json.Marshal(map[string]any{"trading_day": day, "event": inner})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// 校验从历史归档聚合样本并构建事件因子（§ENH-4）。
func TestEventFactorBuildAndAggregate(t *testing.T) {
	path := writeHistory(t, []string{
		// 个股事件两条：同日同票 卧龙电驱，|−0.8| > 0.5 应取 −0.8。
		histRow(t, "20260728", "个股", 0.5, nil, []string{"卧龙电驱|600580"}),
		histRow(t, "20260728", "个股", -0.8, nil, []string{"卧龙电驱|600580"}),
		// 无代码脏条目 + 中性零分：均不占格。
		histRow(t, "20260728", "个股", 0.9, nil, []string{"某某公司"}),
		histRow(t, "20260729", "个股", 0.0, nil, []string{"平安银行|000001"}),
		// 板块事件：行业"半导体设备"应与"半导体"互相包含命中；"银行"不命中；宏观级不入板块档。
		histRow(t, "20260728", "板块", 0.7, []string{"半导体", " ", "黄金"}, nil),
		histRow(t, "20260728", "宏观", 0.9, []string{"半导体"}, nil),
		`坏行不是json`,
		`{"trading_day":"2026","event":{}}`,
	})
	rows, err := LoadEventHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 { // 8 行里剔除 2 条坏行
		t.Fatalf("应解析 6 条，got %d", len(rows))
	}
	industry := map[string]string{
		"600580.SH": "电机", "000001.SZ": "银行", "688000.SH": "半导体设备", "600000.SH": "",
	}
	stock, sector := BuildEventScores(rows, industry)
	if got := stock["20260728"]["600580.SH"]; got != -0.8 {
		t.Errorf("同日同票应取 |score| 最大的 -0.8，got %v", got)
	}
	if _, ok := stock["20260728"][""]; ok {
		t.Error("无代码条目不应入表")
	}
	if _, ok := stock["20260729"]; ok {
		t.Error("零分事件不应占格")
	}
	if got := sector["20260728"]["688000.SH"]; got != 0.7 {
		t.Errorf("行业互相包含应命中半导体设备，got %v", got)
	}
	if _, ok := sector["20260728"]["000001.SZ"]; ok {
		t.Error("银行不应命中半导体/黄金板块")
	}
	if _, ok := sector["20260728"]["600000.SH"]; ok {
		t.Error("空行业不得参与匹配")
	}
	if len(sector["20260728"]) != 1 {
		t.Errorf("宏观级不应入板块档，got %v", sector["20260728"])
	}
}

// 校验事件因子值按日期对齐回填进面板。
func TestEventFactorApplyToPanels(t *testing.T) {
	// 手工构造两只股票面板（日期键与归档 trading_day 同为 YYYYMMDD）。
	mk := func(code string, dates []string) *Panel {
		idx := map[string]int{}
		for i, d := range dates {
			idx[d] = i
		}
		return &Panel{Code: code, Series: &factor.StockSeries{Dates: dates}, DateIdx: idx, Factors: map[string][]float64{}}
	}
	panels := []*Panel{
		mk("600580.SH", []string{"20260727", "20260728", "20260729"}),
		mk("688000.SH", []string{"20260728"}),
	}
	ApplyEventScores(panels, map[string]map[string]float64{"20260728": {"600580.SH": -0.8}}, EventFactorStock)
	ApplyEventScores(panels, map[string]map[string]float64{"20260728": {"688000.SH": 0.7}}, EventFactorSector)

	col := panels[0].Factors[EventFactorStock]
	if len(col) != 3 || col[1] != -0.8 || !math.IsNaN(col[0]) || !math.IsNaN(col[2]) {
		t.Errorf("个股档应只在 20260728 有值，其余 NaN，got %v", col)
	}
	if v := panels[1].Factors[EventFactorSector][0]; v != 0.7 {
		t.Errorf("板块档装配失败，got %v", v)
	}
	// 未涉及的档位也要存在且全 NaN（横截面原语依赖列存在）。
	if v := panels[1].Factors[EventFactorStock][0]; !math.IsNaN(v) {
		t.Error("无事件面板档位应整列 NaN")
	}
}
