// floatcap_test.go — §FLOW-FLOATCAP 流通市值解析与兜底单测（2026-10-25）。
//
// 夹具取值均为线上实测：
//   - 600580 见 thsReal600580（2026-09-20 抓取，卧龙电驱）；
//   - 600519 为 2026-10-25 复探 realhead 报文，与腾讯 qt.gtimg.cn 交叉验证钉死：
//     流通股本 402=1250081601（与腾讯同值），流通市值=402×现价=16,688.59 亿元（逐元一致）。
//
// 对应方案：docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.2/§2.3（净流入/流通市值归一化）。
// English: float-market-cap parsing and fallback tests. All fixtures are live captures;
// the 600519 values are cross-verified cent-exact against Tencent quotes.
package data

import (
	"math"
	"testing"
)

// thsReal600519FloatCap 600519 realhead 实测报文（2026-10-25 抓取，贵州茅台）。
// 只保留本测试必需字段（5/6/10/402/407/name）+ 代码校验所需的 5；其余字段不影响股本解析。
// Live 2026-10-25 capture for 600519; only the fields this test needs are kept.
const thsReal600519FloatCap = `quotebridge_v2_realhead_hs_600519_last({"items":{` +
	`"402":"1250081601.00","407":"1250081601.00","10":"1335.00","6":"1318.49",` +
	`"5":"600519","name":"\u8d35\u5dde\u8305\u53f0"}})`

// TestParseTHSQuoteFloatSharesCap 股本字段必须解析进 StockInfo，且流通市值=流通股本×现价。
// English: share-count fields must land on StockInfo and float cap must equal shares×price.
func TestParseTHSQuoteFloatSharesCap(t *testing.T) {
	si, err := parseTHSQuote([]byte(thsReal600519FloatCap), "600519")
	if err != nil {
		t.Fatalf("parseTHSQuote: %v", err)
	}
	if si.FloatShares != 1250081601 {
		t.Fatalf("流通股本应=1250081601（与腾讯交叉验证同值）, got %.0f", si.FloatShares)
	}
	if si.TotalShares != 1250081601 {
		t.Fatalf("总股本应=1250081601（全流通同值）, got %.0f", si.TotalShares)
	}
	// 钉死值：1250081601 × 1335 = 1,668,858,937,335 元 = 16,688.59 亿元（腾讯直给值逐元一致）。
	wantCap := 1250081601.0 * 1335.0
	if math.Abs(si.FloatMarketCap-wantCap) > 1 {
		t.Fatalf("流通市值应=%.0f（16,688.59 亿，腾讯钉死）, got %.0f", wantCap, si.FloatMarketCap)
	}
	// 存量夹具（600580）同样必须带上股本——线上报文一直有这些字段，只是旧解析器没接。
	si2, err := parseTHSQuote([]byte(thsReal600580), "600580")
	if err != nil {
		t.Fatalf("parseTHSQuote(600580): %v", err)
	}
	if si2.FloatShares != 1562117511 {
		t.Fatalf("600580 流通股本应=1562117511（线上实测）, got %.0f", si2.FloatShares)
	}
	if math.Abs(si2.FloatMarketCap-si2.FloatShares*si2.Price) > 1 {
		t.Fatalf("600580 流通市值应=股本×现价, got %.0f (shares=%.0f price=%.2f)",
			si2.FloatMarketCap, si2.FloatShares, si2.Price)
	}
}

// TestFloatMarketCapOf 消费方取市值三态：直取优先 → 换手率反推兜底 → 不可得弃权（绝不造假值）。
// English: three states for consumers — direct value wins, turnover-derived fallback, else
// abstain (never fabricate).
func TestFloatMarketCapOf(t *testing.T) {
	// ① 直取优先：反推值不同也应返回直取值。
	direct := &StockInfo{Price: 10, Volume: 1000, Turnover: 1, FloatShares: 500, FloatMarketCap: 5000}
	if got, ok := FloatMarketCapOf(direct); !ok || got != 5000 {
		t.Fatalf("直取值应优先且=5000, got %.0f ok=%v", got, ok)
	}
	// ② 换手率反推：无直取值时 shares = Volume/(Turnover/100) = 1000/0.01 = 100000，cap=1,000,000。
	derived := &StockInfo{Price: 10, Volume: 1000, Turnover: 1}
	got, ok := FloatMarketCapOf(derived)
	if !ok || math.Abs(got-1000000) > 1 {
		t.Fatalf("换手率反推应=1,000,000, got %.0f ok=%v", got, ok)
	}
	// ③ 两条路都缺输入 → ok=false（缺数纪律：不折 0、不折现价）。
	for name, q := range map[string]*StockInfo{
		"nil":            nil,
		"全零":             {},
		"有价无量":           {Price: 10},
		"有量无换手":          {Price: 10, Volume: 1000},
		"换手为零(歧义弃权)":    {Price: 10, Volume: 1000, Turnover: 0},
	} {
		if got, ok := FloatMarketCapOf(q); ok {
			t.Fatalf("%s 应弃权(ok=false), got %.0f", name, got)
		}
	}
	// ④ 真实报文闭环：600580 Fixture 解析后直取必须命中（反推路径不掺和）。
	si, err := parseTHSQuote([]byte(thsReal600580), "600580")
	if err != nil {
		t.Fatalf("parseTHSQuote: %v", err)
	}
	got, ok = FloatMarketCapOf(si)
	if !ok || math.Abs(got-si.FloatMarketCap) > 1 {
		t.Fatalf("真实报文应走直取且=%0.f, got %.0f ok=%v", si.FloatMarketCap, got, ok)
	}
}
