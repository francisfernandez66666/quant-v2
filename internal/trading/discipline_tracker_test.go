// DisciplineTracker（实盘统一止盈止损纪律接入层）单元测试：探针+扳机状态机映射为 PositionAdvice。
// English: DisciplineTracker (live unified-discipline adapter) unit tests — probe+trigger state machine
// mapped onto PositionAdvice.
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

func trackerInput(positions []store.RealPosition, quotes map[string]*data.StockInfo, scores map[string]combat_agent.StockScores, bears map[string]string) AdviceInput {
	return AdviceInput{
		Positions:   positions,
		Quotes:      quotes,
		Scores:      scores,
		BearReasons: bears,
	}
}

// TestTrackerStopLossWindowAndPrune 止损线 -6% → 首探针进入观察窗（持有/低，阻止加仓）；
// 窗结算仍无利空信号 → 减仓（半平，Source=discipline）；已平仓代码状态清理（重入从零开始）。
func TestTrackerStopLossWindowAndPrune(t *testing.T) {
	cfg := config.DefaultDisciplineConfig()
	tr := NewDisciplineTracker()
	pos := store.RealPosition{TsCode: "600000.SH", Name: "浦发", Qty: 1000, CostPrice: 100, HighestPrice: 100, Strategy: "龙头"}

	quotes := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 94}} // -6%
	in := trackerInput([]store.RealPosition{pos}, quotes, nil, nil)
	// 首探针：触及止损线 → 观察窗，产出 持有/低（阻止加仓/格局）。
	advs := tr.ProbeAll(in, cfg)
	if len(advs) != 1 || advs[0].Action != "持有" || advs[0].Level != "低" || advs[0].Source != "discipline" {
		t.Fatalf("首触止损应产出 持有/低/discipline, got %+v", advs)
	}

	// 模拟时间推进到结算点 10:45（对齐 15min 边界）：仍无利空信号 → 减半仓止损离场。
	// 通过直接改状态机的 FirstTouch/SettleStart 模拟（窗口逻辑本身由 discipline_test 覆盖）。
	st := tr.states["600000"]
	if st == nil || st.Line != LineStopLoss {
		t.Fatalf("应已进入止损观察窗, got %+v", st)
	}
	st.SettleStart = at(2026, 10, 45) // 结算点置为已到
	advs = tr.ProbeAll(in, cfg)
	if len(advs) != 1 || advs[0].Action != "减仓" || advs[0].Level != "高" {
		t.Fatalf("止损窗结算无信号应减仓半平, got %+v", advs)
	}
	if advs[0].RefPrice != 94 {
		t.Fatalf("减仓建议 RefPrice 应为现价 94, got %.2f", advs[0].RefPrice)
	}

	// 持仓清空 → 状态清理（重入从零开始）。
	tr.ProbeAll(trackerInput(nil, quotes, nil, nil), cfg)
	if len(tr.states) != 0 {
		t.Fatalf("持仓清空后状态应清理, got %d", len(tr.states))
	}
}

// TestTrackerTakeProfitNoAutoExec 止盈线 +15%：无做多信号 → 观察窗 → 结算无信号 → 止盈全平。
func TestTrackerTakeProfit(t *testing.T) {
	cfg := config.DefaultDisciplineConfig()
	tr := NewDisciplineTracker()
	pos := store.RealPosition{TsCode: "000001.SZ", Name: "平安", Qty: 500, CostPrice: 100, HighestPrice: 130, Strategy: "N形"}
	quotes := map[string]*data.StockInfo{"000001": {Code: "000001", Price: 116}} // +16%
	in := trackerInput([]store.RealPosition{pos}, quotes, nil, nil)
	advs := tr.ProbeAll(in, cfg)
	if len(advs) != 1 || advs[0].Action != "持有" {
		t.Fatalf("触止盈线首探针应进入观察窗(持有/低), got %+v", advs)
	}
	st := tr.states["000001"]
	st.SettleStart = at(2026, 10, 46)
	advs = tr.ProbeAll(in, cfg)
	if len(advs) != 1 || advs[0].Action != "止盈" || advs[0].Level != "高" {
		t.Fatalf("止盈窗结算无信号应止盈全平, got %+v", advs)
	}
}

// TestTrackerMissingQuoteSkipped 行情缺失（现价≤0）→ 跳过该持仓（不伪造现价/不下结论）。
func TestTrackerMissingQuoteSkipped(t *testing.T) {
	cfg := config.DefaultDisciplineConfig()
	tr := NewDisciplineTracker()
	pos := store.RealPosition{TsCode: "600000.SH", Name: "浦发", Qty: 1000, CostPrice: 100, HighestPrice: 100}
	in := trackerInput([]store.RealPosition{pos}, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 0}}, nil, nil)
	if advs := tr.ProbeAll(in, cfg); len(advs) != 0 {
		t.Fatalf("行情缺失应跳过不下结论, got %+v", advs)
	}
}

// TestTrackerTimeWarpRealProbe 用真实探针时间推进验证跨轮次窗口（首探针确认 → 结算点后离场）。
func TestTrackerTimeWarpRealProbe(t *testing.T) {
	cfg := config.DefaultDisciplineConfig()
	tr := NewDisciplineTracker()
	pos := store.RealPosition{TsCode: "600000.SH", Name: "浦发", Qty: 1000, CostPrice: 100, HighestPrice: 100}
	quotes := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 94}}
	in := trackerInput([]store.RealPosition{pos}, quotes, nil, nil)
	// 首探针：实时 now → 观察窗（ActionConfirm）。
	advs := tr.ProbeAll(in, cfg)
	if len(advs) != 1 || advs[0].Action != "持有" {
		t.Fatalf("首探针应进入观察窗, got %+v", advs)
	}
	// 覆盖首触时间为过去 16 分钟（跨过 15min 窗），再用实时 now 探针 → 应结算离场。
	st := tr.states["600000"]
	st.FirstTouch = time.Now().Add(-16 * time.Minute)
	st.SettleStart = time.Now().Add(-time.Minute)
	advs = tr.ProbeAll(in, cfg)
	if len(advs) != 1 || advs[0].Action != "减仓" {
		t.Fatalf("跨窗结算应减仓离场, got %+v", advs)
	}
}
