// §UAT-2026-09-08 P0 回归：实盘账本库(live.db) 与 D1 评分历史库(trading.db) 分离装配。
// 修复前 registry.go 把 opts.D1Store 传入 SetQMT（第二参=realStore），引擎近实时建议循环
// pushRealAdvice 从研究库读 real_positions 恒为 0——实盘止损/止盈/自动卖出/M8 链路静默失效，
// e2e 因双库同源（RealStore=D1Store=同一 DB）未能暴露。本测试钉死分库不变量：
//
//	① e.realStore 与 e.d1Store 必须分离；
//	② 实盘账本(realStore)持仓必须能被引擎的实时建议读取入口(RealPositionsForUser)看到；
//	③ D1 评分落库走 d1Store（研究库），绝不混入实盘账本库。
//
// English: §UAT-2026-09-08 P0 regression — the live book (live.db) and the D1 score store (trading.db)
// must be wired to separate engine stores. Before the fix, registry.go passed opts.D1Store as the
// realStore (2nd arg of SetQMT), so pushRealAdvice read real_positions from the research DB and always
// saw 0 — silently killing live SL/TP/auto-sell/M8. The e2e rig shared one DB so this rotted silently.
package engine

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

func TestSplitRealAndD1StoreWiring(t *testing.T) {
	realDB, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open real store: %v", err)
	}
	defer realDB.Close()
	d1DB, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open d1 store: %v", err)
	}
	defer d1DB.Close()

	// 实盘账本种一笔持仓（研究库为空）——模拟 2026-08-31 拆库后的真实形态
	if _, err := realDB.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600580.SH", Name: "卧龙电驱", Qty: 3800, CostPrice: 29.74, Amount: 109440},
	}); err != nil {
		t.Fatalf("seed real position: %v", err)
	}

	// 修复后装配（与 registry.go 同款）：realStore=实盘账本库、d1Store=研究库
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := trading.NewController(trading.NoopExecutor{}, realDB, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, realDB)
	e.SetD1Store(d1DB)

	// ① 两库分离（P0 核心不变量）
	if e.realStore == nil || e.realStore == e.d1Store {
		t.Fatalf("realStore 与 d1Store 未分离: real=%p d1=%p", e.realStore, e.d1Store)
	}
	// ② pushRealAdvice 的持仓读取入口必须看到实盘账本持仓（修复前此处读到研究库→0）
	pos, err := e.realStore.RealPositionsForUser(e.primaryMember())
	if err != nil || len(pos) != 1 || pos[0].TsCode != "600580.SH" {
		t.Fatalf("实盘账本读取持仓失败(err=%v): %+v", err, pos)
	}
	// ③ D1 评分落库走研究库，绝不写进实盘账本库
	day := "2026-09-08"
	if err := e.d1Store.UpsertD1Scores(day, []store.D1ScoreRow{{Code: "600580.SH", Score: 0.3}}); err != nil {
		t.Fatalf("upsert d1: %v", err)
	}
	got, err := d1DB.D1ScoresByDate(day)
	if err != nil || got["600580.SH"] != 0.3 {
		t.Fatalf("D1 应落入研究库(d1Store): err=%v rows=%v", err, got)
	}
	realGot, err := realDB.D1ScoresByDate(day)
	if err != nil || len(realGot) != 0 {
		t.Fatalf("D1 不应写入实盘账本库(live.db): err=%v rows=%v", err, realGot)
	}
}
