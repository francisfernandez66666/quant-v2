// advice_unified_test.go — §REFACTOR_UNIFIED_SELL P2：Advise 投影化的行为锁。
//
// 钉死两件事：
//
//	① SellUnifiedOn=true 时旧五路卖出拼装整体跳过（基线对照：同输入 off 态确有超期卖出卡）；
//	② SellProjection 注入的裁决卡是唯一卖出结论来源，只做持仓上下文补全；
//	   已投影代码不再叠加加仓/格局；持仓外的幽灵投影直接作废。
//
// English: P2 locks on the advice layer — with the unified gate on the legacy five-way assembly is
// skipped, projected verdict cards are the only sell source, and stale/ghost projections drop.
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// TestAdviseUnifiedOnSkipsFiveWayAndProjects 五路跳过 + 投影注入 + 幽灵过滤。
func TestAdviseUnifiedOnSkipsFiveWayAndProjects(t *testing.T) {
	old := time.Now().AddDate(0, -2, 0).Format("2006-01-02")
	positions := []store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 10, BuyDate: old},
	}
	in := AdviceInput{
		Agent:     combat_agent.New(&config.StrategyConfig{}),
		Positions: positions,
		Quotes:    map[string]*data.StockInfo{"600000": {Price: 10.05}},
		Cfg:       config.QMTConfig{},
	}
	// 基线（off）：远古开仓触发五路卖出建议（与 TestAdviseT1LockedSkipsSellSide 同形态）
	if codes := baseOf(in); len(codes) == 0 {
		t.Fatal("基线 off 态应产出卖出级建议（否则本测试无对照意义）")
	}
	// 切闸：五路跳过、无投影 → 卖出级卡片归零（只剩可能的加仓/格局持有级）
	in.SellUnifiedOn = true
	for _, a := range Advise(in) {
		if a.Action == "止盈" || a.Action == "止损" || a.Action == "减仓" {
			t.Fatalf("切闸后旧五路不得再产出卖出卡，got %+v", a)
		}
	}
	// 注入裁决投影：唯一卖出来源 + 上下文补全（数量/市值来自持仓行）
	in.SellProjection = []UnifiedSellView{
		{TsCode: "600000.SH", Action: "止盈", Level: "高", Reason: "[统一裁决] 窗结算无做多信号", RefPrice: 10.5, Source: UnifiedSellSourceAction},
		{TsCode: "000002.SZ", Action: "止损", Level: "高", Reason: "幽灵投影（已不在持仓）", RefPrice: 9, Source: UnifiedSellSourceAction},
	}
	out := Advise(in)
	if len(out) != 1 {
		t.Fatalf("应只剩 1 条投影卡（幽灵投影作废、不再叠加加仓/格局），got %+v", out)
	}
	a := out[0]
	if a.Code != "600000" || a.Action != "止盈" || a.Level != "高" || a.Source != UnifiedSellSourceAction {
		t.Fatalf("投影卡结论字段被篡改，got %+v", a)
	}
	if a.Qty != 100 || a.Amount != 1050 || a.ProfitPct <= 0 {
		t.Fatalf("投影卡未补全持仓上下文（qty/amount/profit），got %+v", a)
	}
}
