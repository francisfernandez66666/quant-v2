// moneyflow_history_test.go — §FLOW-HIST 日线资金流历史解析单测（2026-10-25）。
//
// 夹具为线上实抓报文（2026-10-25，600519 贵州茅台，push2his daykline klt=101&lmt=6）：
// 6 个交易日（10-16~10-23，周末无行），行宽与当日分时解析（f51..f56）逐列同构。
// 对应方案：docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.2/§2.3（5 日净流入因子）。
// English: daily capital-flow history parsing tests; fixture is a live 2026-10-25 capture.
package data

import (
	"math"
	"strings"
	"testing"
)

// emFlowHistoryLive600519 线上实抓报文（2026-10-25 抓取，600519）。只保留 data.klines。
// Live capture 2026-10-25 for 600519 (six trading days 10-16..10-23, weekend has no rows).
const emFlowHistoryLive600519 = `{"rc":0,"data":{"code":"600519","market":1,"name":"贵州茅台","klines":[` +
	`"2026-10-16,468661168.0,-454626.0,-468206560.0,425044128.0,43617040.0",` +
	`"2026-10-19,314089632.0,-361186.0,-313728448.0,71020464.0,243069168.0",` +
	`"2026-10-20,60852848.0,-615351.0,-60237488.0,93491088.0,-32638240.0",` +
	`"2026-10-21,92632544.0,-477569.0,-92154992.0,261017072.0,-168384528.0",` +
	`"2026-10-22,355966928.0,-631916.0,-355335024.0,652632384.0,-296665456.0",` +
	`"2026-10-23,283257760.0,-614451.0,-282643296.0,781667680.0,-498409920.0"]}}`

// TestParseMoneyFlowHistoryLive 真实日线报文必须逐行解析且自洽（大单+超大单=主力）。
// English: the live daily payload must parse row by row with the large+super-large==main sanity.
func TestParseMoneyFlowHistoryLive(t *testing.T) {
	days, err := parseMoneyFlowHistory([]byte(emFlowHistoryLive600519), "600519")
	if err != nil {
		t.Fatalf("parseMoneyFlowHistory: %v", err)
	}
	if len(days) != 6 {
		t.Fatalf("应解析出 6 个交易日, got %d", len(days))
	}
	if days[0].Date != "2026-10-16" || days[5].Date != "2026-10-23" {
		t.Fatalf("首尾日期应=2026-10-16/2026-10-23, got %s/%s", days[0].Date, days[5].Date)
	}
	// 首行钉死值（线上实抓）：主力 468661168 = 大单 425044128 + 超大单 43617040。
	if days[0].MainNet != 468661168 {
		t.Fatalf("首行主力净额应=468661168, got %.0f", days[0].MainNet)
	}
	for i, d := range days {
		if math.Abs(d.LargeNet+d.SuperLargeNet-d.MainNet) > 1 {
			t.Errorf("第 %d 行自洽失败: 大单%.0f+超大%.0f != 主力%.0f", i, d.LargeNet, d.SuperLargeNet, d.MainNet)
		}
	}
	// 累计（近 6 日主力净额合计，钉死值）。
	if got := SumMainNet(days); math.Abs(got-1575460880) > 1 {
		t.Fatalf("6 日主力净额累计应=1575460880, got %.0f", got)
	}
}

// TestParseMoneyFlowHistoryAbstain 缺数纪律：任何一行畸形 → 整体错误（缺一行即整体弃权）。
// English: abstention discipline — any malformed row fails the whole call.
func TestParseMoneyFlowHistoryAbstain(t *testing.T) {
	cases := map[string]string{
		"空 klines":     `{"data":{"klines":[]}}`,
		"字段短缺":        `{"data":{"klines":["2026-10-16,1,2,3"]}}`,
		"非数字":         `{"data":{"klines":["2026-10-16,abc,2,3,4,5"]}}`,
		"null 占位":     `{"data":{"klines":["2026-10-16,null,2,3,4,5"]}}`,
		"空日期":         `{"data":{"klines":[",1,2,3,4,5"]}}`,
		"坏 JSON":       `not-json`,
	}
	for name, body := range cases {
		if _, err := parseMoneyFlowHistory([]byte(body), "600519"); err == nil {
			t.Errorf("%s 应返回错误(整体弃权), got nil", name)
		}
	}
	// 首行好、末行坏 → 仍整体弃权（不返回"前几行凑合用"的半缺拼凑）。
	halfBad := `{"data":{"klines":["2026-10-16,1,2,3,4,5","2026-10-19,x,2,3,4,5"]}}`
	if _, err := parseMoneyFlowHistory([]byte(halfBad), "600519"); err == nil {
		t.Error("末行非数字应整体弃权, got nil")
	}
}

// TestParseMoneyFlowHistoryPartialRows 行数不足请求天数（新股票/断档）是合法形态：按已有行返回。
// English: fewer rows than requested is legal (new listing/gap) — return what exists.
func TestParseMoneyFlowHistoryPartialRows(t *testing.T) {
	days, err := parseMoneyFlowHistory([]byte(`{"data":{"klines":["2026-10-23,100,1,2,3,4"]}}`), "600519")
	if err != nil {
		t.Fatalf("单行应合法返回: %v", err)
	}
	if len(days) != 1 || days[0].MainNet != 100 {
		t.Fatalf("应返回 1 行主力=100, got %d 行", len(days))
	}
}

// TestEmFlowHistoryURLParams §FLOW-HIST：日线请求必须带 klt=101&lmt=N 与 fields2（6 列净额），
// 主机为 push2his daykline（与当日分时 klt=1&lmt=0 的同族不同周期）。
// English: the history request must carry klt=101&lmt=N and the 6-net fields2, on push2his.
func TestEmFlowHistoryURLParams(t *testing.T) {
	m := NewMarketAPI()
	DisableAll = true
	defer func() { DisableAll = false }()
	rec := &urlRecorderTransport{body: `{"data":{"klines":["2026-10-23,1,2,3,4,5"]}}`}
	m.SetTransport(rec)
	if _, err := m.getEastMoneyMoneyFlowHistory("600519", 5); err != nil {
		t.Fatalf("getEastMoneyMoneyFlowHistory: %v", err)
	}
	got := rec.lastURL()
	for _, want := range []string{
		"push2his.eastmoney.com/api/qt/stock/fflow/daykline/get",
		"secid=1.600519",
		"klt=101", "lmt=5",
		"fields2=f51,f52,f53,f54,f55,f56",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("历史请求 URL 缺少 %q: %s", want, got)
		}
	}
	if strings.Contains(got, "klt=1&") {
		t.Errorf("不得误用当日分时周期 klt=1: %s", got)
	}
}
