// sector_exposure_test.go — §C1 纸面行业敞口同口径锁。
//
// 纸面买入/加仓必须与实盘闸一致：同行业超上限拒单；不同行业互不影响；影子期放行。
//
// English: §C1 paper-book parity — same-sector cap rejects, different sectors are independent,
// shadow mode passes.
package paper

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/data"
)

func TestPaperConceptExposure(t *testing.T) {
	quotes := map[string]*data.StockInfo{
		"600000.SH": {Code: "600000.SH", Price: 10, ChangePct: 1},
		"600001.SH": {Code: "600001.SH", Price: 10, ChangePct: 1},
		"600519.SH": {Code: "600519.SH", Price: 10, ChangePct: 1},
	}
	concepts := func(code string) []string {
		switch code {
		case "600000.SH", "600001.SH":
			return []string{"AI"}
		case "600519.SH":
			return []string{"白酒"}
		}
		return nil
	}
	coverage := func() (float64, bool) { return 100, true }
	newConceptBook := func(shadow bool) *Engine {
		pe := New(Config{Enabled: true, InitialCapital: 100000, FixedAmount: 20000, MaxPositions: 10}, "")
		pe.SetConceptExposure(30, shadow, 95, coverage, concepts)
		return pe
	}
	pe := newConceptBook(false)
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, quotes); err != nil {
		t.Fatalf("首笔: %v", err)
	}
	if err := pe.Buy("600001.SH", "华夏", "龙头", 10, quotes); err == nil || !strings.Contains(err.Error(), "概念敞口") {
		t.Fatalf("同概念超限应拒, got %v", err)
	}
	if err := pe.Buy("600519.SH", "茅台", "龙头", 10, quotes); err != nil {
		t.Fatalf("不同概念应放行: %v", err)
	}
	// 覆盖率不足 → 弃权。
	low := newConceptBook(false)
	low.SetConceptExposure(30, false, 95, func() (float64, bool) { return 80, true }, concepts)
	if err := low.Buy("600000.SH", "浦发", "龙头", 10, quotes); err != nil {
		t.Fatalf("首笔: %v", err)
	}
	if err := low.Buy("600001.SH", "华夏", "龙头", 10, quotes); err != nil {
		t.Fatalf("覆盖率不足应弃权: %v", err)
	}
}

func TestPaperSectorExposure(t *testing.T) {
	quotes := map[string]*data.StockInfo{
		"600000.SH": {Code: "600000.SH", Price: 10, ChangePct: 1},
		"600001.SH": {Code: "600001.SH", Price: 10, ChangePct: 1},
		"600519.SH": {Code: "600519.SH", Price: 10, ChangePct: 1},
	}
	industry := func(code string) string {
		switch code {
		case "600000.SH", "600001.SH":
			return "银行"
		case "600519.SH":
			return "白酒"
		}
		return ""
	}

	newBook := func(shadow bool) *Engine {
		pe := New(Config{Enabled: true, InitialCapital: 100000, FixedAmount: 20000, MaxPositions: 10}, "")
		pe.SetSectorExposure(30, shadow, industry)
		return pe
	}

	pe := newBook(false)
	if err := pe.Buy("600000.SH", "浦发", "龙头", 10, quotes); err != nil {
		t.Fatalf("首笔买入应成功: %v", err)
	}
	// 第二笔同行业：20k + 20k = 40% > 30% → 拒。
	if err := pe.Buy("600001.SH", "华夏", "龙头", 10, quotes); err == nil || !strings.Contains(err.Error(), "行业敞口") {
		t.Fatalf("同行业超限应拒单, got %v", err)
	}
	// 不同行业 → 放行。
	if err := pe.Buy("600519.SH", "茅台", "龙头", 10, quotes); err != nil {
		t.Fatalf("不同行业应放行: %v", err)
	}

	// 影子期：同行业第二笔放行。
	shadowBook := newBook(true)
	if err := shadowBook.Buy("600000.SH", "浦发", "龙头", 10, quotes); err != nil {
		t.Fatalf("影子首笔: %v", err)
	}
	if err := shadowBook.Buy("600001.SH", "华夏", "龙头", 10, quotes); err != nil {
		t.Fatalf("影子期不得拒单: %v", err)
	}
}
