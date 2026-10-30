// registry_decision_test.go — §W-02（2026-10-29 夜批）纸面决策快照的落库接线。
//
// 单测层面（internal/paper）只证明"算完会播"；这条证明"播出来真的进了 signal_decisions，
// 且 Channel=paper、UserID 是当前账号"。缺了它，回调注入写歪（写错库/写错账号/吞掉 error）
// 在页面上只表现为"纸面那半张卡还是空的"。
package engine

import (
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
)

func TestPaperDecisionWritesPerUser(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	// 用与账号无关的模板引擎：GetPaper 会按账号 rules.paper 重建，这里没有 CfgMgr，
	// 走模板就是"该账号的配置"。模板必须是 dynamic，否则 Paper 的新仓走固定金额，不产出动态预算。
	tmpl := paper.New(paper.Config{
		Enabled: true, InitialCapital: 100000, BudgetMode: "dynamic",
		SizingEnabled: true, SingleStockCapPct: 35,
	}, "")
	dataDir := t.TempDir()
	// paper 引擎会把账本落到 <dataDir>/accounts/<uid>/paper.json；目录不存在时落盘只告警，
	// 决策快照不受影响，但噪声日志会掩盖"到底哪一步失败"。这里先把账号目录建好。
	if err := os.MkdirAll(filepath.Join(dataDir, "accounts", "u_paper_dec"), 0o755); err != nil {
		t.Fatalf("mkdir accounts: %v", err)
	}
	reg := NewRegistry(EngineOptions{Paper: tmpl, RealStore: db, DataDir: dataDir})
	pe := reg.GetPaper("u_paper_dec")
	if pe == nil {
		t.Fatal("GetPaper 返回 nil")
	}
	pe.SetSizingContext(1, 0.1, 0.3, 1, false)
	quotes := map[string]*data.StockInfo{
		"600000.SH": {Code: "600000.SH", Name: "浦发", Price: 10, ChangePct: 1},
	}
	if err := pe.BuyInPool("600000.SH", "浦发", "龙头", "", 10, quotes); err != nil {
		t.Fatalf("纸面买入: %v", err)
	}

	day := cntime.DayCompactOf(cntime.Now())
	rows, err := db.SignalDecisionsForDay("u_paper_dec", day)
	if err != nil {
		t.Fatalf("SignalDecisionsForDay: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("纸面买入应落 1 行决策快照，实得 %d（回调没接通或日键不对）", len(rows))
	}
	if rows[0].Channel != "paper" {
		t.Fatalf("纸面快照 Channel 必须是 paper（与实盘 live 区分），实得 %q", rows[0].Channel)
	}
	if rows[0].Qty <= 0 || rows[0].Stage != "placed" {
		t.Fatalf("纸面快照应落 placed 且 Qty>0，实得 %+v", rows[0])
	}
}
