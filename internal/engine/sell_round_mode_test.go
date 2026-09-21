// sell_round_mode_test.go — §M9（2026-09-22 修复批）sell_unified_mode「轮首快照」的反例锁。
//
// 缺陷原型（docs/AUDIT_FULL_UAT_20260921C.md M9）：一轮 5s/主循环内，scoring_loop、
// runSellUnifiedJudge、autoExecuteRealSells 各自独立现读配置——用户在轮中将
// sell_unified_mode 从 shadow 翻到 on 时，前半段消费方按 shadow 行事（统一裁决只留痕不执行、
// 旧链出口尚未关闭……实际是后半段读到 on：探测器直卖被证据闸/来源闸关闭），两个口径在同
// 一轮里各跑一半 → 该轮既没有旧链卖出、也没有统一处置落地 =「保护空窗轮」（止损被跳过一轮）。
//
// 修复口径：轮首读一次（scoreCycle / 主循环），本轮所有消费方（runSellUnifiedJudge /
// autoExecuteRealSellsRound / paperSignals 证据闸 / judgePaperLedgers→runPaperUnifiedJudge /
// 13e report 旧链出口闸）一律透传同一快照，消费方内部禁止现读。
//
// 本锁钉死两件事：
//
//	① 轮中 shadow→on 翻转：以轮首快照 shadow 走完本轮——统一裁决只留痕不动账，
//	   探测器直卖旧链出口照常放行（本轮必有旧链处置机会，不出现双双不卖的空窗）；
//	② 轮中 on→shadow 翻转：以轮首快照 on 走完本轮——探测器卖出直达撮合被证据闸关闭，
//	   统一裁决出口照常受理（本轮必有统一处置，且不双写）。
//	③ 参数权威：裁决/执行函数的 sellMode 参数优先于控制器当前配置（off 快照即静止，
//	   shadow 快照即留痕，哪怕配置已翻成别的值）。
//
// English: locks the round-start config snapshot for sell_unified_mode — a mid-round flip
// can no longer split one round across two regimes (the "protection-empty round" that skipped
// stops); every same-round consumer uses the injected snapshot, which is authoritative over
// the controller's current value.
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// flipSellMode 轮中翻转控制器生效配置（模拟用户在轮边界改 sell_unified_mode，
// UpdateConfig 立即生效——正是旧实现"轮中各消费方现读到不同值"的触发器）。
func flipSellMode(t *testing.T, e *Engine, mode string) {
	t.Helper()
	ctrl := e.qmtCtrlRef()
	if ctrl == nil {
		t.Fatal("需要实盘控制器以承载配置翻转")
	}
	cfg := ctrl.Config()
	cfg.SellUnifiedMode = mode
	ctrl.UpdateConfig(cfg)
	if got := e.sellUnifiedModeEngine(); got != mode {
		t.Fatalf("前置失败：配置翻转未生效（got %q want %q）", got, mode)
	}
}

// detectorSellSig 探测器"清仓"级卖出信号（SellAction 归一为 close，13e 旧链/证据闸的输入形态）。
func detectorSellSig(code string) combat_agent.Signal {
	return combat_agent.Signal{Code: code, Name: "翻转测试", Direction: "提醒", Action: "卖出", AlertType: "清仓", GeneratedAt: time.Now()}
}

// TestSellRoundModeShadowToOnFlip ①轮中 shadow→on：本轮按快照 shadow 完整走旧链——
// 统一裁决只留痕不动账，探测器直卖信号照常抵达撮合出口（旧链未被"新口径"提前关闸）。
// 旧实现反例：裁决层（先跑）读 shadow 不执行，13e/paperSignals（后跑）读到 on 把直卖闸关死
// → 本轮两头都不卖，止损提醒被吞一轮。
func TestSellRoundModeShadowToOnFlip(t *testing.T) {
	e := shadowTestEnv(t, "shadow")
	e.bearTier = map[string]bearTierEntry{"300001": {verified: signalctl.BearVerifiedDual, at: time.Now()}}
	pe := sellTestPaper(t)

	roundSellMode := e.sellUnifiedModeEngine() // 轮首快照 = "shadow"
	if roundSellMode != "shadow" {
		t.Fatalf("轮首快照应为 shadow，得 %q", roundSellMode)
	}
	// —— 轮中翻转：用户把模式切到 on ——
	flipSellMode(t, e, "on")

	// 消费方①：纸面统一裁决（本轮先跑，喂轮首快照）→ shadow 语义：有处置留痕但绝不动账。
	before := len(pe.Orders())
	vs := e.runPaperUnifiedJudge("u_1", pe, hardClearFeed(), e.paperSignalPolicy("u_1"), roundSellMode)
	if len(vs) != 1 || vs[0].Verdict.Disposal == nil {
		t.Fatalf("shadow 快照下硬清证据应产出留痕处置（不执行），得 %+v", vs)
	}
	if len(pe.Orders()) != before || !pe.Holds("300001") {
		t.Fatalf("shadow 快照下统一裁决不得动账（orders %d→%d）", before, len(pe.Orders()))
	}

	// 消费方②：13e 探测器直卖出口（本轮后跑，同用轮首快照）→ 旧链必须照常放行。
	var dispatched []combat_agent.Signal
	e.SetPaperDispatch(func(emit, exit []combat_agent.Signal, quotes map[string]*data.StockInfo) {
		dispatched = append(dispatched, emit...)
		dispatched = append(dispatched, exit...)
	}, nil)
	e.paperSignals([]combat_agent.Signal{detectorSellSig("300001")}, nil,
		map[string]*data.StockInfo{"300001": {Price: 9.3}}, roundSellMode)
	if len(dispatched) != 1 || dispatched[0].Code != "300001" {
		t.Fatalf("shadow 快照轮内探测器直卖必须照常送达撮合（保护空窗反例：被轮中 on 闸关死），得 %d 条 %+v", len(dispatched), dispatched)
	}
	// 本轮结论：旧链出口有处置机会（①留痕不执行 + ②旧链放行）→「旧链 或 投影」至少一路发生。
}

// TestSellRoundModeOnToShadowFlip ②轮中 on→shadow：本轮按快照 on 完整走统一口径——
// 探测器直卖被证据闸降级（不再直达撮合），统一裁决出口照常受理处置。
// 旧实现反例：裁决层读到 on 执行后，13e 出口又读到 shadow 再卖一遍（双写）；方向虽与①不同，
// 根因同为"一轮多读"。
func TestSellRoundModeOnToShadowFlip(t *testing.T) {
	e := shadowTestEnv(t, "on")
	e.bearTier = map[string]bearTierEntry{"300001": {verified: signalctl.BearVerifiedDual, at: time.Now()}}
	pe := sellTestPaper(t)

	roundSellMode := e.sellUnifiedModeEngine() // 轮首快照 = "on"
	flipSellMode(t, e, "shadow")               // 轮中翻回 shadow

	// 消费方①：统一裁决按快照 on 执行——当日买入受 T+1 拦截落 rejected 卖单（唯一出口受理证据）。
	before := len(pe.Orders())
	vs := e.runPaperUnifiedJudge("u_1", pe, hardClearFeed(), e.paperSignalPolicy("u_1"), roundSellMode)
	if len(vs) != 1 || vs[0].Verdict.Disposal == nil {
		t.Fatalf("on 快照下应产出处置，得 %+v", vs)
	}
	after := len(pe.Orders())
	if after != before+1 {
		t.Fatalf("on 快照下处置应且仅应经唯一出口受理一次（卖单 %d→%d）", before, after)
	}

	// 消费方②：探测器直卖出口同用轮首快照 on → 卖出信号降级为证据，不得再直达撮合（杜绝双写）。
	var dispatched []combat_agent.Signal
	e.SetPaperDispatch(func(emit, exit []combat_agent.Signal, quotes map[string]*data.StockInfo) {
		dispatched = append(dispatched, emit...)
		dispatched = append(dispatched, exit...)
	}, nil)
	e.paperSignals([]combat_agent.Signal{detectorSellSig("300001")}, nil,
		map[string]*data.StockInfo{"300001": {Price: 9.3}}, roundSellMode)
	for _, s := range dispatched {
		if s.Code == "300001" && combat_agent.SellAction(s) != "" {
			t.Fatalf("on 快照轮内探测器直卖必须被证据闸关闭（双写反例），得 %+v", dispatched)
		}
	}
	if n := countSellVerdicts(e, "300001", signalctl.VerdictPass); n == 0 {
		t.Fatal("统一裁决处置留痕缺失：本轮两条出口都没走 = 保护空窗")
	}
}

// TestSellRoundModeParamAuthoritative ③参数权威：live 裁决/执行消费方只认注入快照——
// 配置读到 on、快照给 off → 完全静默；配置读到 off、快照给 shadow → 照常留痕。
// （反证函数内部不再现读配置，轮中翻转影响不到已定快照的本轮。）
func TestSellRoundModeParamAuthoritative(t *testing.T) {
	positions := []store.RealPosition{{TsCode: "600580.SH", Name: "快照测试", Qty: 100, CostPrice: 10}}
	quotes := map[string]*data.StockInfo{"600580": {Price: 9.3}}

	// 配置 on、快照 off → 不裁决不留痕
	e := shadowTestEnv(t, "on")
	if got := e.sellUnifiedModeEngine(); got != "on" {
		t.Fatalf("前置失败：配置应为 on，得 %q", got)
	}
	if vs := e.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, nil, "off"); vs != nil {
		t.Fatalf("off 快照必须返回 nil（参数权威，配置 on 不得复活裁决），得 %+v", vs)
	}
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictHold) + countSellVerdicts(e, "600580.SH", signalctl.VerdictPass); n != 0 {
		t.Fatalf("off 快照轮不得留痕，得 %d", n)
	}

	// 配置 off、快照 shadow → 照常影子留痕
	e2 := shadowTestEnv(t, "off")
	if got := e2.sellUnifiedModeEngine(); got != "off" {
		t.Fatalf("前置失败：配置应为 off，得 %q", got)
	}
	if vs := e2.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, nil, "shadow"); len(vs) != 1 {
		t.Fatalf("shadow 快照应产出 1 条裁决（参数权威，配置 off 不得熄火本轮），得 %+v", vs)
	}
	if n := countSellVerdicts(e2, "600580.SH", signalctl.VerdictHold); n != 1 {
		t.Fatalf("shadow 快照轮应留 1 条 hold 痕迹，得 %d", n)
	}
}
