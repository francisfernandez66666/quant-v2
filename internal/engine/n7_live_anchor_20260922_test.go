// n7_live_anchor_20260922_test.go — §N-7（2026-09-22 傍晚批复验）live 移动止盈锚点跨重启的行为锁。
//
// 缺陷形态：裁决内核（signalctl.sellProbe）的锚点是「播种 + 每轮自抬」，live 侧的播种值取自
// real_positions.highest_price，而该列此前只有券商快照 open_price / 成交回报成交价两个来源，
// **期间最高价从未写入** → 进程一重启锚点退回建仓价，涨过 tp 再回落的仓位移动止盈永不触发。
// paper 侧 §M10 已有 json 持久化，live 一直漏修（owner 裁决 12=回写账本，不新增 json）。
//
// 本用例走完整链路：内核自抬 → 回写账本 → 新 Engine（模拟重启，内核状态清零）→ 重新读账本
// 播种 → 同一现价下仍命中移动止盈线；并用「账本锚点仍停在建仓价」的对照实例复现修复前形态，
// 证明断言真的有鉴别力（而不是任何价格下都会命中移动线）。
//
// English: §N-7 behavior lock — the live trailing-stop anchor raised by the kernel is written back
// to real_positions.highest_price, so a restarted engine (fresh kernel state) still seeds 20 and
// hits the trailing take-profit line; a control book that kept only the entry price proves the
// assertion discriminates.
package engine

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// n7Env 在给定实盘账本上装配一个全新引擎实例（=进程重启：signalctl 卖出状态纯内存，随实例归零）。
func n7Env(t *testing.T, db *store.DB) *Engine {
	t.Helper()
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.SellUnifiedMode = "shadow" // 只裁决留痕，本用例不依赖任何下单
	ctrl := trading.NewController(trading.NoopExecutor{}, db, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, db)
	return e
}

// n7SeedBook 铺一本「1000 股 @10、账本最高价停在建仓价 10」的实盘账（券商口径的历史形态：
// highest_price 从来没有期间最高价写入过）。
func n7SeedBook(t *testing.T, db *store.DB, tsCode, name string) {
	t.Helper()
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: tsCode, Name: name, Qty: 1000, CostPrice: 10, Amount: 10000, HighestPrice: 10, UserID: "u_1"},
	}); err != nil {
		t.Fatalf("seed book %s: %v", tsCode, err)
	}
}

// n7Round 跑一轮 live 统一裁决（现价由调用方给），返回该代码的裁决视图。
// 持仓直接从账本重读——与线上 pushRealAdvice 每轮 RealPositionsForUser 的取数口径一致，
// 这样"重启后播种值"就是真实链路里的播种值。
func n7Round(t *testing.T, e *Engine, db *store.DB, price float64) sellRoundVerdict {
	t.Helper()
	positions, err := db.RealPositionsForUser("u_1")
	if err != nil || len(positions) != 1 {
		t.Fatalf("读实盘持仓失败: n=%d err=%v", len(positions), err)
	}
	quotes := map[string]*data.StockInfo{"600000": {Code: "600000", Price: price}}
	verdicts := e.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, nil, e.sellUnifiedModeEngine())
	if len(verdicts) != 1 {
		t.Fatalf("应产出 1 条有效裁决, got %d", len(verdicts))
	}
	return verdicts[0]
}

// TestN7LiveAnchorSurvivesRestart §N-7 主用例：
//  1. 涨到 20 的那一轮，内核把锚点自抬到 20，并且**回写 real_positions.highest_price**；
//  2. 模拟重启（新引擎实例，内核状态清零）后，裁决输入仍是 20（账本播种）；
//  3. 现价回落到 11（盈亏 +10% 未及止盈线 +15%），相对锚点 20 回撤 45% ≥ 6% → 命中移动止盈线；
//  4. 对照实例（账本锚点仍停在建仓价 10=修复前形态）同价裁决 → 不命中任何线，
//     即"涨过 15% 再回落永不触发移动止盈"的原始缺陷形态。
//
// English: the raised anchor is persisted to the ledger, a restarted engine re-seeds it and still
// hits the trailing line at price 11 (pullback 45% from high 20); the control book that kept only
// the entry price hits no line at all — exactly the pre-fix failure.
func TestN7LiveAnchorSurvivesRestart(t *testing.T) {
	realDB, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	t.Cleanup(func() { realDB.Close() })
	n7SeedBook(t, realDB, "600000.SH", "锚点测试")

	// ① 首个高点轮：现价 20（+100%）——内核自抬锚点，§N-7 要求同轮回写账本
	e1 := n7Env(t, realDB)
	v1 := n7Round(t, e1, realDB, 20)
	if v1.Verdict.Line == signalctl.SellLineTrail {
		t.Fatalf("现价即高点轮不应命中移动止盈（回撤尚未达阈值）, got %s", v1.Verdict.Line)
	}
	if got := e1.SignalCtl().SellHighAnchor(signalctl.ChannelLive, "u_1", "600000.SH"); got != 20 {
		t.Fatalf("内核内存锚点应为 20, got %v", got)
	}
	p, err := realDB.RealPositionByCodeForUser("u_1", "600000.SH")
	if err != nil || p.HighestPrice != 20 {
		t.Fatalf("§N-7 锚点必须回写 real_positions.highest_price=20: %+v err=%v", p, err)
	}

	// ② 模拟重启：新实例（内核纯内存状态归零）+ 重新读账本
	e2 := n7Env(t, realDB)
	if got := e2.SignalCtl().SellHighAnchor(signalctl.ChannelLive, "u_1", "600000.SH"); got != 0 {
		t.Fatalf("新实例内核状态应为空（确认这真的是一次重启）, got %v", got)
	}
	positions2, err := realDB.RealPositionsForUser("u_1")
	if err != nil || positions2[0].HighestPrice != 20 {
		t.Fatalf("重启后账本播种值应仍是 20: %+v err=%v", positions2, err)
	}
	// ③ 现价回落到 11：盈亏 +10% 不触及盈线（+15%），但对锚点 20 回撤 45% → 移动止盈
	v2 := n7Round(t, e2, realDB, 11)
	if v2.Verdict.Line != signalctl.SellLineTrail {
		t.Fatalf("§N-7 重启后移动止盈锚点丢失（裁决输入应仍为 20 并命中移动线），got line=%q hold=%q",
			v2.Verdict.Line, v2.Verdict.HoldReason)
	}

	// ④ 对照：账本锚点停在建仓价（修复前的唯一形态）时，同一现价不命中任何线
	ctlDB, err := store.Open(filepath.Join(t.TempDir(), "live_control.db"))
	if err != nil {
		t.Fatalf("open control store: %v", err)
	}
	t.Cleanup(func() { ctlDB.Close() })
	n7SeedBook(t, ctlDB, "600000.SH", "锚点测试对照")
	if p, _ := ctlDB.RealPositionByCodeForUser("u_1", "600000.SH"); p.HighestPrice != 10 {
		t.Fatalf("对照实例应保持在建仓价 10, got %v", p.HighestPrice)
	}
	if v := n7Round(t, n7Env(t, ctlDB), ctlDB, 11); v.Verdict.Line == signalctl.SellLineTrail {
		t.Fatalf("对照实例不该命中移动止盈（否则本用例失去鉴别力）")
	}
}

// TestN7ReconcileCannotLowerRaisedAnchor §N-6/§N-7 同一条 upsert 的协调断言（引擎侧链路版）：
// 锚点回写后再跑一轮券商对账（快照 highest_price=开仓价 10、cost_price=不含费 10），
// 账本锚点必须仍是 20——两条写路径都单调不降，谁后跑都不丢高点。
// English: after the anchor is written back, a broker reconcile whose snapshot carries only the
// entry price must not pull it down (both writers are monotonic on that column).
func TestN7ReconcileCannotLowerRaisedAnchor(t *testing.T) {
	realDB, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	t.Cleanup(func() { realDB.Close() })
	n7SeedBook(t, realDB, "600000.SH", "锚点测试")
	if raised, err := realDB.RaiseRealPositionHigh("u_1", "600000.SH", 20); err != nil || !raised {
		t.Fatalf("回写锚点失败: raised=%v err=%v", raised, err)
	}
	// 券商快照：开仓价 10（既当成本也当"最高价"），数量不变
	if _, err := realDB.ReconcilePositionsForUser("u_1", []store.RealPosition{
		{TsCode: "600000.SH", Name: "锚点测试", Qty: 1000, CostPrice: 10, Amount: 10000, HighestPrice: 10},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	p, err := realDB.RealPositionByCodeForUser("u_1", "600000.SH")
	if err != nil || p.HighestPrice != 20 {
		t.Fatalf("§N-7 对账不得拉回已回写的锚点, got %+v err=%v", p, err)
	}
	// 裁决输入仍取自该锚点（重启语义下的播种值）
	positions, _ := realDB.RealPositionsForUser("u_1")
	if positions[0].HighestPrice != 20 {
		t.Fatalf("对账后探针播种值应为 20, got %v", positions[0].HighestPrice)
	}
}
