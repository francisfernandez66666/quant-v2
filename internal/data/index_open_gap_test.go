// index_open_gap_test.go — §SIDE-POLICY 09:30 指数缺口与资金流分母单位锁。
package data

import (
	"strconv"
	"testing"
)

func gapBody(todayOpen float64) []byte {
	return []byte(`{"data":{"klines":["2026-11-19,3490,3510,3480,3500,1000,100000","2026-11-20,` +
		formatGapFloat(todayOpen) + `,3600,3480,3570,2000,200000"]}}`)
}

func formatGapFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func TestIndexOpenGapToday(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com": gapBody(3570),
	}, true, true)
	gap, err := m.IndexOpenGapToday("2026-11-20")
	if err != nil {
		t.Fatal(err)
	}
	if gap < 1.99 || gap > 2.01 {
		t.Fatalf("09:30 gap want +2.00%%, got %.4f", gap)
	}
}

func TestIndexOpenGapRejectsStaleLastBar(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com": emKLineBody([]string{"2026-11-18", "2026-11-19"}, 1e5),
	}, true, true)
	if _, err := m.IndexOpenGapToday("2026-11-20"); err == nil {
		t.Fatal("stale last bar must be rejected")
	}
}

func TestIndexOpenGapRejectsMissingOpen(t *testing.T) {
	m, _ := newFakeMarket(t, map[string][]byte{
		"push2his.eastmoney.com": gapBody(0),
	}, true, true)
	if _, err := m.IndexOpenGapToday("2026-11-20"); err == nil {
		t.Fatal("missing open must be rejected")
	}
}

func TestIndexAmountIsCNY(t *testing.T) {
	if !IndexAmountIsCNY("em:000001.SH") || !IndexAmountIsCNY("em_mirror:000300.SH") {
		t.Fatal("EastMoney amount must be CNY")
	}
	if IndexAmountIsCNY("sina:000001.SH") {
		t.Fatal("Sina volume must never be used as CNY denominator")
	}
}
