// sell_unified_exec_test.go — §SELLPOINT-UNIFY P2 切闸的行为锁（投影映射 + T+1 降级 + 来源闸）。
//
// 钉死三件事：
//
//	① projectSellView 映射表：处置 close→止损/止盈（按判定线）、trim→减仓，Source=unified；
//	   观察窗→持有/低、利空未触线→持有/中，全部 unified-watch（永不进执行）；
//	② T+1 当日锁定持仓的处置卡降级为预警（§PROD-T1 同口径，卖不动的指令即误导）；
//	③ autoExecuteRealSells 来源闸：mode=on 时只有 Source=unified 的处置可下单——
//	   旧「止损任意来源直放」后门关闭；shadow/缺省时旧口径原样保留（零行为变化）。
//
// English: P2 locks — the verdict→card projection table, the T+1 downgrade, and the
// unified-only execution gate (legacy any-source stop-loss path preserved under shadow).
package engine

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// disposalVerdict 构造一条带处置的裁决记录（测试辅助）。
func disposalVerdict(action string, line signalctl.SellLine) sellRoundVerdict {
	return sellRoundVerdict{
		TsCode: "600000.SH",
		Price:  9.0,
		Verdict: signalctl.SellVerdict{
			Disposal: &signalctl.SellDisposal{Code: "600000.SH", Action: action, Line: line, Reason: "窗结算无做多信号"},
		},
	}
}

// TestProjectSellViewMapping ①投影映射表逐例钉死（含 nil=无卡）。
func TestProjectSellViewMapping(t *testing.T) {
	cases := []struct {
		name                  string
		in                    sellRoundVerdict
		action, level, source string
		skip                  bool
	}{
		{"止损处置", disposalVerdict(signalctl.SellActionClose, signalctl.SellLineStopLoss), "止损", "高", trading.UnifiedSellSourceAction, false},
		{"深破处置", disposalVerdict(signalctl.SellActionClose, signalctl.SellLineDeepBreach), "止损", "高", trading.UnifiedSellSourceAction, false},
		{"止盈处置", disposalVerdict(signalctl.SellActionClose, signalctl.SellLineTakeProfit), "止盈", "高", trading.UnifiedSellSourceAction, false},
		{"移动止盈处置", disposalVerdict(signalctl.SellActionClose, signalctl.SellLineTrail), "止盈", "高", trading.UnifiedSellSourceAction, false},
		{"减半处置", disposalVerdict(signalctl.SellActionTrim, signalctl.SellLineStopLoss), "减仓", "高", trading.UnifiedSellSourceAction, false},
	}
	for _, c := range cases {
		v := projectSellView(&c.in)
		if v == nil {
			t.Fatalf("%s：应出卡却为 nil", c.name)
		}
		if v.Action != c.action || v.Level != c.level || v.Source != c.source {
			t.Fatalf("%s：期望 %s/%s/%s，得 %s/%s/%s", c.name, c.action, c.level, c.source, v.Action, v.Level, v.Source)
		}
	}

	// 观察窗中（hold，无处置）→ 持有/低 unified-watch
	hold := sellRoundVerdict{TsCode: "600000.SH", Price: 9.0, Verdict: signalctl.SellVerdict{Line: signalctl.SellLineStopLoss, HoldReason: "观察窗等待做多信号"}}
	if v := projectSellView(&hold); v == nil || v.Action != "持有" || v.Level != "低" || v.Source != trading.UnifiedSellSourceWatch {
		t.Fatalf("观察窗应出「持有/低 unified-watch」卡，得 %+v", v)
	}
	// 未触线但命中利空 → 持有/中预警（owner 语义①：无处置资格只预警）
	bear := sellRoundVerdict{TsCode: "600000.SH", Price: 9.9, BearHit: true, Verified: signalctl.BearVerifiedSingle}
	if v := projectSellView(&bear); v == nil || v.Action != "持有" || v.Level != "中" || v.Source != trading.UnifiedSellSourceWatch {
		t.Fatalf("未触线利空应出「持有/中 unified-watch」预警，得 %+v", v)
	}
	// 常规轮次（未触线、无论证利空）→ 无卡
	if v := projectSellView(&sellRoundVerdict{TsCode: "600000.SH", Price: 9.9}); v != nil {
		t.Fatalf("常规轮次不应出卡，得 %+v", v)
	}
}

// TestUnifiedSellViewsT1Downgrade ②：当日买入锁定持仓的处置卡降为预警，未锁的保持可执行。
func TestUnifiedSellViewsT1Downgrade(t *testing.T) {
	e := &Engine{}
	positions := []store.RealPosition{
		{TsCode: "600000.SH", Name: "锁仓", Qty: 100, CostPrice: 10},
		{TsCode: "600519.SH", Name: "可卖", Qty: 200, CostPrice: 1500},
	}
	verdicts := []sellRoundVerdict{
		disposalVerdict(signalctl.SellActionClose, signalctl.SellLineStopLoss),
		{TsCode: "600519.SH", Price: 1400, Verdict: signalctl.SellVerdict{
			Disposal: &signalctl.SellDisposal{Code: "600519.SH", Action: signalctl.SellActionClose, Line: signalctl.SellLineStopLoss, Reason: "深破"},
		}},
	}
	sellable := map[string]int{"600000.SH": 0, "600519.SH": 200}
	views := e.unifiedSellViews(positions, verdicts, sellable)
	if len(views) != 2 {
		t.Fatalf("应投影 2 条，得 %d", len(views))
	}
	byTs := map[string]trading.UnifiedSellView{}
	for _, v := range views {
		byTs[v.TsCode] = v
	}
	if locked := byTs["600000.SH"]; locked.Source != trading.UnifiedSellSourceWatch || locked.Action != "持有" {
		t.Fatalf("T+1 全锁处置必须降级为预警卡，得 %+v", locked)
	}
	if ok := byTs["600519.SH"]; ok.Source != trading.UnifiedSellSourceAction || ok.Action != "止损" {
		t.Fatalf("未锁持仓处置卡必须保持可执行，得 %+v", ok)
	}
}

// TestAutoSellUnifiedOnOnlyExecutesAdjudicated ③正/负锁：on 模式下 legacy 来源止损被来源闸拦下，
// Source=unified 处置正常全平；减仓同理只认 unified。
func TestAutoSellUnifiedOnOnlyExecutesAdjudicated(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.AutoSell = true
		c.SellUnifiedMode = "on"
	})
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000},
	}); err != nil {
		t.Fatal(err)
	}
	// 后门形态：旧五路拼装的「止损/高」（Source=""）——切闸后不得再直达执行
	e.autoExecuteRealSells(e.UserID(), e.QMTController(), db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高", RefPrice: 9, Reason: "破位（旧路误报）"},
	})
	if len(*orders) != 0 {
		t.Fatalf("mode=on 时非裁决来源止损不得下单（任意来源直放后门已关闭），得 %d 单", len(*orders))
	}
	// 观察卡形态：unified-watch 永不执行
	e.autoExecuteRealSells(e.UserID(), e.QMTController(), db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高", RefPrice: 9, Source: trading.UnifiedSellSourceWatch},
	})
	if len(*orders) != 0 {
		t.Fatalf("unified-watch 观察卡不得执行，得 %d 单", len(*orders))
	}
	// 裁决处置形态：Source=unified 正常全平
	e.autoExecuteRealSells(e.UserID(), e.QMTController(), db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高", RefPrice: 9, Reason: "[统一裁决] 窗结算", Source: trading.UnifiedSellSourceAction},
	})
	if len(*orders) != 1 {
		t.Fatalf("Source=unified 处置应下 1 单，得 %d", len(*orders))
	}
	if q := (*orders)[0]["qty"].(float64); q != 500 {
		t.Fatalf("全平应卖 500 股，得 %v", q)
	}
}

// TestAutoSellShadowModeLegacyPathUnchanged ③负向对照：缺省（shadow）模式下旧「止损任意来源」
// 口径原样保留——本次重构的资金安全承诺是切闸前零行为变化。
func TestAutoSellShadowModeLegacyPathUnchanged(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = true })
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000},
	}); err != nil {
		t.Fatal(err)
	}
	e.autoExecuteRealSells(e.UserID(), e.QMTController(), db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高", RefPrice: 9, Reason: "破位"},
	})
	if len(*orders) != 1 {
		t.Fatalf("shadow 模式旧链必须照常执行止损，得 %d 单", len(*orders))
	}
}
