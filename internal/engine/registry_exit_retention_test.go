// 文件：registry_exit_retention_test.go
// 包名：engine
// 所属模块：「多账号引擎注册表与装配」
// 模块职责：§EXIT-RETAIN（2026-09-23）装配侧的开放持仓策略键聚合——
//   - 启动装配（newAccountRunners）必须把"仍持有"的停用战法覆盖继续入表（持仓键来自实盘账本 + 各账号模拟盘）；
//   - OpenPositionStrategyCounts 是战法库 open_positions 与出场保留判定的同一份真相源，
//     必须同时覆盖**实盘持仓**（real_positions.strategy，历史形态存显示名 / 新形态存规则 ID）
//     与**模拟盘持仓**（paper.Positions，Strategy 空时退回池键 StrategyType）。
//
// English: assembly-side coverage — startup seeding keeps a disabled-but-still-held rule's exit
// override, and the open-position counts span both the live book (positions stored by display name
// and by rule id) and every account's paper book.
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
)

// TestNewAccountRunnersKeepsDisabledRuleOverrideForHeldPositions 启动装配：停用但仍有持仓的规则，
// 其出场覆盖继续在表（ID 与显示名双键）；无持仓时立即失效。
func TestNewAccountRunnersKeepsDisabledRuleOverrideForHeldPositions(t *testing.T) {
	t.Cleanup(func() { combat_agent.SetRuleExitOverrides(nil, nil, nil) })
	dir := t.TempDir()
	entry := map[string]any{
		"id": "fac_7", "name": "因子战法#7", "enabled": false, // 已停用（人工或自动降级落库）
		"candidate_id": 7, "applied_at": "2026-08-23 00:00:00",
		"factors":       []string{"mom_5"},
		"weights":       map[string]float64{"mom_5": 1},
		"directions":    map[string]int{"mom_5": 1},
		"buy_threshold": 60.0, "horizon": 5,
		"exit_trail_pct": 6.0, "exit_max_hold_days": 9,
	}
	b, _ := json.Marshal([]map[string]any{entry})
	if err := os.WriteFile(filepath.Join(dir, "applied_factors.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	// 无持仓：装配后覆盖不入表（回退全局 8%/15 天）——旧行为在这一支保持不变
	newAccountRunners(config.NewManager(""), nil, "tester", dir, nil)
	if _, _, ok := combat_agent.RuleExitOverrideFor("因子战法#7"); ok {
		t.Fatal("停用且无持仓不应保留覆盖")
	}

	// 有持仓（按显示名记录，历史形态）：装配后双键继续命中同一份覆盖
	held := combat_agent.HeldStrategyKeys{}
	held.Add("因子战法#7")
	newAccountRunners(config.NewManager(""), nil, "tester", dir, held)
	trail, hold, ok := combat_agent.RuleExitOverrideFor("因子战法#7")
	if !ok || trail != 6 || hold != 9 {
		t.Fatalf("停用但仍持有的规则覆盖未保留: ok=%v trail=%v hold=%v", ok, trail, hold)
	}
	if _, _, ok := combat_agent.RuleExitOverrideFor("fac_7"); !ok {
		t.Fatal("ID 键应同样保留（双键同值语义）")
	}
}

// TestOpenPositionStrategyCountsCoversLiveAndPaper 计数聚合：实盘账本（显示名 + 规则 ID 两种历史形态）
// 与账号模拟盘（懒加载 + 磁盘恢复）都要进同一张表；池键 StrategyType 只在 Strategy 为空时兜底。
func TestOpenPositionStrategyCountsCoversLiveAndPaper(t *testing.T) {
	dir := t.TempDir()

	live, err := store.Open(filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatalf("open live.db: %v", err)
	}
	t.Cleanup(func() { live.Close() })
	// UpsertRealPositions 是 store 侧登记的"测试专用入口"（生产走 ReconcilePositionsForUser），
	// 本用例正好按该用途使用它：两笔按显示名、一笔按规则 ID 记录战法的实盘持仓。
	if _, err := live.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发银行", Qty: 100, CostPrice: 10, Strategy: "因子战法#1", UserID: "u1"},
		{TsCode: "600001.SH", Name: "邯郸钢铁", Qty: 100, CostPrice: 10, Strategy: "因子战法#1", UserID: "u1"},
		{TsCode: "000001.SZ", Name: "平安银行", Qty: 100, CostPrice: 10, Strategy: "fac_2", UserID: "u2"},
	}); err != nil {
		t.Fatalf("seed real positions: %v", err)
	}

	// 账号模拟盘：accounts/u1/paper.json 携带两笔持仓（一笔显示名、一笔池键）
	paperJSON := `{"cash":100000,"initial_capital":100000,"positions":{` +
		`"600096":{"code":"600096","name":"云天化","qty":300,"cost_price":31.46,"cost":9438,"mark":31.9,"strategy":"形态战法#3"},` +
		`"002815":{"code":"002815","name":"崇达技术","qty":1100,"cost_price":17.3,"cost":19030,"mark":17.59,"strategy_type":"pat_4"}}}`
	pdir := filepath.Join(dir, "accounts", "u1")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "paper.json"), []byte(paperJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(EngineOptions{
		RealStore: live,
		DataDir:   dir,
		Paper:     paper.New(paper.Config{}, ""), // 全局模板账本（仅提供配置，无持仓）
	})
	// 登记共享引擎服务的账号（coreUsers 驱动模拟盘引擎的锁外预创建，与 allPaperHeldCodes 同口径）
	r.coreUsers[&Engine{}] = []string{"u1"}

	held := r.OpenPositionStrategyCounts()
	for key, want := range map[string]int{
		"因子战法#1": 2,
		"fac_2":  1,
		"形态战法#3": 1,
		"pat_4":  1,
	} {
		if got := held.CountFor(key); got != want {
			t.Fatalf("开放持仓计数 %q: got %d want %d (全表=%v)", key, got, want, map[string]int(held))
		}
	}
	// 未持有的键恒为 0（战法库 open_positions 才不会凭空多出数）
	if n := held.CountFor("fac_9"); n != 0 {
		t.Fatalf("未持有键应为 0, got %d", n)
	}
}
