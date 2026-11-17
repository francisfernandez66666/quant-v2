// wiring_d_20261117_test.go — §WIRING-D（2026-11-17 owner 裁决）：外脑/半自动模式下，
// 云端对**上报来的实盘持仓**照常算一份止盈止损并通知，但绝不替盒子把单发出去。
//
// 背景：owner 令「及时上报持仓/资金 + 复用云端功能同步也计算一份止盈止损结果通知」。
// 2026-11-10 Q8 早已裁决"qmt.enabled=false 只停自动执行、建议链照常输出"，但 pushRealAdvice
// 函数顶部仍残留一句 `!ctrl.Enabled() → 整链早退`，与该裁决自相矛盾——于是运营账号处于外脑
// （现网 qmt.enabled=false，执行在盒子自闭环）时，云端连一份止盈止损建议都不算、通知也不发。
// 本轮删掉那句早退。删它的安全性全靠执行侧三层既有闸（Q8 注释里点名），本用例把最关键的
// 第一层钉成行为锁：enabled=false 时 autoExecuteRealSellsRound 即使拿到止损/止盈建议，
// 也一笔都不下——"建议照跑"不等于"云端也去抢盒子的下单权"（外脑不直接接实盘，会重复下单）。
//
// English: §WIRING-D — under the external-brain (qmt.enabled=false) posture the cloud must
// still compute a copy of the 止盈止损 advice on the uplinked live book, but must NOT place
// any order itself. This locks execution-safety net ①: disabled ctrl → autoExecuteRealSellsRound
// records zero orders even given 止损/止盈 advices, so "advice still runs" never becomes
// "the brain also grabs the box's order right" (which would double-spend).
package engine

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// 半自动/外脑（enabled=false）下拿到止损+止盈建议 → 一笔都不发（执行闸①在位）。
// 故意把 AutoSell 也设成 true：证明拦住的是"enabled=false"这一道，不是顺带的 auto_sell 开关——
// 否则用例在没真正测到第一层闸的情况下也能恒绿。
func TestDisabledBrainAdviceRunsButPlacesNoOrders(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.Enabled = false // 外脑：执行腿归盒子自闭环
		c.AutoSell = true
	})
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
		{TsCode: "600001.SH", Name: "华夏", Qty: 300, CostPrice: 12, Amount: 3600, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := e.QMTController()
	if ctrl.Enabled() {
		t.Fatal("夹具应装配 enabled=false 的控制器（否则本锁测不到第一层闸）")
	}
	// 止损 + 止盈 两枚建议一起喂：enabled=false 时执行体在来源闸前就 return。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高", RefPrice: 8.8, Reason: "统一纪律止损"},
		{Code: "600001", TsCode: "600001.SH", Action: "止盈", Level: "高", RefPrice: 14, Reason: "战法止盈"},
	})
	if len(*orders) != 0 {
		t.Fatalf("外脑(enabled=false)绝不可替盒子下单, got %d 笔: %+v", len(*orders), *orders)
	}
}

// 反证方向：把 enabled 打开（同款 auto_sell=true），同一批建议必须真下单——
// 证明上一条的"0 笔"是被 enabled 这道闸拦的，不是夹具根本没接到执行腿（恒绿用例）。
func TestEnabledBrainAdvicePlacesOrders(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.Enabled = true
		c.AutoSell = true
		c.Mode = "auto"
	})
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := e.QMTController()
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高", RefPrice: 8.8, Reason: "统一纪律止损"},
	})
	if len(*orders) == 0 {
		t.Fatal("enabled=true+auto+auto_sell 时止损建议应真下单（否则上一条 0 笔测不到东西）")
	}
}
