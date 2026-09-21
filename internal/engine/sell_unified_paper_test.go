// sell_unified_paper_test.go — §SELLPOINT-UNIFY P3 模拟盘并轨的 engine 侧行为锁。
//
// 钉死四件事：
//
//	① unifiedSellGateSigs 证据闸：切闸后探测器「做多向卖出」只作证据不再直达撮合，
//	   买入与做空方向信号（融券账本不在并轨范围）原样保留；
//	② runPaperUnifiedJudge 模式闸：shadow 只裁决留痕、绝不动账（资金行为零变化承诺）；
//	   on 才经 ApplyUnifiedSell 唯一出口下处置（T+1 当日以 rejected 留痕证明已受理）；
//	③ judgePaperLedgers 分发：off 全静默；注入的按账号回调优先于全局回退账本；
//	④ report 手动账本处置（applyReportVerdicts）：close→LogExit 全平、trim→SellLot 半仓
//	   且每码每日一次、不足两手不减、全局纸面引擎持有的 code 跳过（双账簿不重复卖）。
//
// English: P3 engine-side locks — the detector-sell evidence gate, the shadow/on execution split
// on the paper channel, judge dispatch (off silent, per-account hook preferred), and the
// report-book exits reusing the FIX#15 guards.
package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/signalctl"
)

// sellTestPaper 构造已成交一笔 300001（1000 股 @10）的模拟盘（当日持仓 → T+1 锁定，
// 处置只落 rejected 留痕，正好用作「on 已受理」的确定性证据）。
func sellTestPaper(t *testing.T) *paper.Engine {
	t.Helper()
	pe := paper.New(paper.Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000, AutoSell: true}, "")
	pe.SetStrategyPools([]string{"dragon"})
	sig := combat_agent.Signal{Code: "300001", Name: "测试股", Strategy: "龙头", StrategyType: "dragon",
		Action: "buy", Price: 10, GeneratedAt: time.Now()}
	pe.OnSignals([]combat_agent.Signal{sig}, map[string]*data.StockInfo{"300001": {Price: 10}})
	if !pe.Holds("300001") {
		t.Fatal("预置模拟盘持仓失败")
	}
	return pe
}

// hardClearFeed 构造「触止损线 + 双源验证利空」的当轮即时硬清证据（§D1 护栏4 同 live 测试口径）。
func hardClearFeed() sellJudgeFeed {
	return sellJudgeFeed{
		BearReasons: map[string]string{"300001": "贵金属板块利空"},
		SnapQuotes:  map[string]*data.StockInfo{"300001": {Price: 9.3}}, // −7% 触 6% 止损线
	}
}

// TestUnifiedSellGateSigs ①证据闸映射表。
func TestUnifiedSellGateSigs(t *testing.T) {
	sigs := []combat_agent.Signal{
		{Code: "300001", Direction: "做多", Action: "buy"},
		{Code: "300002", Direction: "提醒", Action: "卖出", AlertType: "清仓"},
		{Code: "300003", Direction: "提醒", Action: "卖出", AlertType: "减仓"},
		{Code: "300004", Direction: "做空", Action: "sell"},
		{Code: "300005", Direction: "提醒", Action: "关注", AlertType: "跌幅提醒"},
	}
	out := unifiedSellGateSigs(sigs)
	kept := map[string]bool{}
	for _, s := range out {
		kept[s.Code] = true
	}
	if !kept["300001"] {
		t.Fatal("买入信号必须保留")
	}
	if !kept["300004"] {
		t.Fatal("做空方向信号（融券账本）必须原样透传")
	}
	if !kept["300005"] {
		t.Fatal("提醒级（SellAction 不命中）必须保留")
	}
	if kept["300002"] || kept["300003"] {
		t.Fatalf("做多向清仓/减仓探测器信号必须被降级为证据（不下发）：%v", kept)
	}
}

// TestRunPaperUnifiedJudgeShadowNoExecute ②shadow 负锁：触线+双源利空当轮有处置，
// 但只留痕不动账——模拟盘持仓原样、不产生任何卖单。
func TestRunPaperUnifiedJudgeShadowNoExecute(t *testing.T) {
	e := shadowTestEnv(t, "shadow")
	e.bearTier = map[string]bearTierEntry{"300001": {verified: signalctl.BearVerifiedDual, at: time.Now()}}
	pe := sellTestPaper(t)
	before := len(pe.Orders())
	e.runPaperUnifiedJudge("u_1", pe, hardClearFeed(), e.paperSignalPolicy("u_1"))
	if n := countSellVerdicts(e, "300001", signalctl.VerdictPass); n != 1 {
		t.Fatalf("shadow 下触线+双源利空应留 1 条处置留痕（证据对照），得 %d", n)
	}
	if !pe.Holds("300001") {
		t.Fatal("shadow 模式绝不允许动账")
	}
	if len(pe.Orders()) != before {
		t.Fatalf("shadow 模式不得产生卖单: %d → %d", before, len(pe.Orders()))
	}
}

// TestRunPaperUnifiedJudgeOnExecutes ②on 正锁：处置经 ApplyUnifiedSell 唯一出口受理——
// 当日 T+1 拦截落 rejected 卖单（GAP1.9 口径），这就是「已执行到唯一出口」的确定性证据。
func TestRunPaperUnifiedJudgeOnExecutes(t *testing.T) {
	e := shadowTestEnv(t, "on")
	e.bearTier = map[string]bearTierEntry{"300001": {verified: signalctl.BearVerifiedDual, at: time.Now()}}
	pe := sellTestPaper(t)
	e.runPaperUnifiedJudge("u_1", pe, hardClearFeed(), e.paperSignalPolicy("u_1"))
	orders := pe.Orders()
	if len(orders) == 0 {
		t.Fatal("on 模式处置应到达 ApplyUnifiedSell（产生卖单留痕）")
	}
	var sell *paper.Order
	for i := range orders {
		if orders[i].Side == "sell" {
			sell = &orders[i]
			break
		}
	}
	if sell == nil {
		t.Fatalf("应存在卖出方向留痕，得 %+v", orders)
	}
	if sell.Status != "rejected" {
		t.Fatalf("当日买入受 T+1 拦截应留 rejected（不得假成交），得 %s", sell.Status)
	}
	if !pe.Holds("300001") {
		t.Fatal("T+1 拦截后持仓应保留")
	}
}

// TestJudgePaperLedgersDispatch ③分发闸：off 全静默（回调不被调用）；
// 注入了按账号回调时优先走回调（多账号 registry 路径）。
func TestJudgePaperLedgersDispatch(t *testing.T) {
	called := 0
	e := shadowTestEnv(t, "shadow")
	e.SetPaperSellJudge(func(feed sellJudgeFeed) { called++ })
	e.judgePaperLedgers(hardClearFeed())
	if called != 1 {
		t.Fatalf("shadow/on 均应调用按账号裁决回调，得 %d", called)
	}
	off := shadowTestEnv(t, "off")
	off.SetPaperSellJudge(func(feed sellJudgeFeed) { called++ })
	off.judgePaperLedgers(hardClearFeed())
	if called != 1 {
		t.Fatalf("off 模式必须完全静默（回调不得被调用），得 %d", called)
	}
}

// TestApplyReportVerdictsExits ④report 账本处置：close→LogExit 全平、trim→SellLot 半仓 +
// 当日去重 + 不足两手不减、全局纸面引擎持有的 code 跳过。
func TestApplyReportVerdictsExits(t *testing.T) {
	rpt := report.New(filepath.Join(t.TempDir(), "report.json"))
	rpt.LogSignalWithMetaQtyUser("sig-close", "600000", "全平股", "做多", "龙头", 10, 15, 6, 1000, nil, "u_1")
	rpt.LogSignalWithMetaQtyUser("sig-trim", "600001", "减仓股", "做多", "龙头", 10, 15, 6, 1000, nil, "u_1")
	rpt.LogSignalWithMetaQtyUser("sig-small", "600002", "碎股", "做多", "龙头", 10, 15, 6, 100, nil, "u_1")
	rpt.LogSignalWithMetaQtyUser("sig-paper-held", "600003", "纸面镜像股", "做多", "龙头", 10, 15, 6, 1000, nil, "u_1")

	e := shadowTestEnv(t, "on")
	e.rpt = rpt
	pe := paper.New(paper.Config{Enabled: true, FixedAmount: 10000, InitialCapital: 100000, AutoSell: true}, "")
	pe.SetStrategyPools([]string{"dragon"})
	pe.OnSignals([]combat_agent.Signal{{Code: "600003", Name: "纸面镜像股", Strategy: "龙头", StrategyType: "dragon", Action: "buy", Price: 10}},
		map[string]*data.StockInfo{"600003": {Price: 10}})

	byCode := map[string]report.ExecLog{
		"600000": {SignalID: "sig-close", Code: "600000", Quantity: 1000},
		"600001": {SignalID: "sig-trim", Code: "600001", Quantity: 1000},
		"600002": {SignalID: "sig-small", Code: "600002", Quantity: 100},
		"600003": {SignalID: "sig-paper-held", Code: "600003", Quantity: 1000},
	}
	verdictFor := func(code, action string) sellRoundVerdict {
		v := sellRoundVerdict{TsCode: code, Price: 9} // TsCode=探针代码（纯数字），与 byCode 键对齐
		v.Verdict.Disposal = &signalctl.SellDisposal{Code: code, Action: action, Reason: "窗结算"}
		return v
	}
	verdicts := []sellRoundVerdict{
		verdictFor("600000", signalctl.SellActionClose),
		verdictFor("600001", signalctl.SellActionTrim),
		verdictFor("600002", signalctl.SellActionTrim),
		verdictFor("600003", signalctl.SellActionClose),
	}
	e.applyReportVerdicts("u_1", byCode, verdicts, pe)

	find := func(id string) *report.ExecLog { return rpt.FindBySignalID(id) }
	if p := find("sig-close"); p == nil || p.Status == "持仓中" {
		t.Fatalf("close 处置应已全平: %+v", p)
	}
	if p := find("sig-trim"); p == nil || p.Status != "持仓中" || p.Quantity != 500 {
		t.Fatalf("trim 处置应半仓（1000→500）且仍在持仓: %+v", p)
	}
	if p := find("sig-small"); p == nil || p.Status != "持仓中" || p.Quantity != 100 {
		t.Fatalf("不足两手的 trim 应跳过: %+v", p)
	}
	if p := find("sig-paper-held"); p == nil || p.Status != "持仓中" {
		t.Fatalf("纸面引擎持有的 code 应跳过（双账簿不重复卖）: %+v", p)
	}
	// 同日重放：trimDone 去重，减仓股不再减
	e.applyReportVerdicts("u_1", byCode, []sellRoundVerdict{verdictFor("600001", signalctl.SellActionTrim)}, pe)
	if p := find("sig-trim"); p == nil || p.Quantity != 500 {
		t.Fatalf("同日二次 trim 应被去重: %+v", p)
	}
}

// TestJudgeReportLedgerShadowRecordOnly ④负锁延伸：judgeReportLedger 在 shadow 下只留痕，
// report 账本一条都不平（旧链 13e 仍是唯一执行出口）。
func TestJudgeReportLedgerShadowRecordOnly(t *testing.T) {
	rpt := report.New(filepath.Join(t.TempDir(), "report.json"))
	rpt.LogSignalWithMetaQtyUser("sig-1", "300001", "利空股", "做多", "龙头", 10, 15, 6, 1000, nil, "u_1")
	e := shadowTestEnv(t, "shadow")
	e.rpt = rpt
	e.bearTier = map[string]bearTierEntry{"300001": {verified: signalctl.BearVerifiedDual, at: time.Now()}}
	e.judgeReportLedger(hardClearFeed(), "shadow")
	if n := countSellVerdicts(e, "300001", signalctl.VerdictPass); n != 1 {
		t.Fatalf("shadow 下 report 探针应留处置痕迹，得 %d", n)
	}
	if p := rpt.FindBySignalID("sig-1"); p == nil || p.Status != "持仓中" {
		t.Fatalf("shadow 模式 report 账本不得被平: %+v", p)
	}
}
