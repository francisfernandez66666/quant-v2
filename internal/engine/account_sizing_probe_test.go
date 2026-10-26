// account_sizing_probe_test.go — §DYN-SIZE P6 账户级熔断接线回归。
//
// 五条锁：
//
//	① 无实盘账本 + 无显式注入 → 探针为 nil（AccountState 恒零 → 各上限因子弃权，零行为变更）；
//	② 显式注入 `SetAccountSizingProbe` 永远是最高优先级（与 `SetQMT` 的先后顺序无关）；
//	③ 有实盘账本 → 自动回落到内置探针，读齐三个读数；
//	④ 权益日快照日级去重（同一天只写一次，且写成功才置位）；
//	⑤ `rules.position.max_total_position_pct` 这个既有死字段被接通（显式 sizing 优先）。
//
// English: P6 account-breaker wiring regression — no ledger means the probe is nil (every
// account-level factor abstains, zero behaviour change); an explicit injection always wins;
// a live book auto-falls back to the built-in probe; the equity snapshot is deduped per day;
// the previously-dead position field is bridged (an explicit sizing value wins).
package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
)

// newProbeTestDB 建临时实盘账本。
func newProbeTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestResolveAccountProbeAbstainsWithoutLedger 无账本、无注入 → nil（整组弃权）。
func TestResolveAccountProbeAbstainsWithoutLedger(t *testing.T) {
	e := &Engine{}
	if e.resolveAccountProbe() != nil {
		t.Fatal("无实盘账本且无显式注入时应返回 nil（各上限因子弃权）")
	}
}

// TestResolveAccountProbeExplicitOverrideWins 显式注入不被内置实现顶掉（两种调用顺序都锁）。
func TestResolveAccountProbeExplicitOverrideWins(t *testing.T) {
	db := newProbeTestDB(t)
	sentinel := func(string) (int, float64, float64, bool) { return 7, 0.2, 0.5, true }

	// 顺序 A：先注入，再 SetQMT
	eA := &Engine{userID: "u"}
	eA.SetAccountSizingProbe(sentinel)
	eA.SetQMT(nil, db)
	if p := eA.resolveAccountProbe(); p == nil {
		t.Fatal("顺序 A：显式注入应仍可用")
	} else if sl, _, _, _ := p("u"); sl != 7 {
		t.Fatalf("顺序 A：显式注入应胜出, got sl=%d", sl)
	}

	// 顺序 B：先 SetQMT（内置探针就位），再注入
	eB := &Engine{userID: "u"}
	eB.SetQMT(nil, db)
	eB.SetAccountSizingProbe(sentinel)
	if p := eB.resolveAccountProbe(); p == nil {
		t.Fatal("顺序 B：显式注入应仍可用")
	} else if sl, _, _, _ := p("u"); sl != 7 {
		t.Fatalf("顺序 B：显式注入应胜出, got sl=%d", sl)
	}
}

// TestResolveAccountProbeFallsBackToBuiltin 有实盘账本 → 自动回落到内置探针。
func TestResolveAccountProbeFallsBackToBuiltin(t *testing.T) {
	db := newProbeTestDB(t)
	e := &Engine{userID: "u"}
	e.SetQMT(nil, db)
	if e.resolveAccountProbe() == nil {
		t.Fatal("有实盘账本时应回落到内置探针（P6 接通）")
	}
}

// TestAccountSizingProbeReadsLedger 内置探针一次读齐三个读数（止损次数 / 权益回撤 / 总仓位占比）。
func TestAccountSizingProbeReadsLedger(t *testing.T) {
	db := newProbeTestDB(t)
	uid := "u_probe"
	now := time.Now()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	// 总仓位占比：券商回报 市值 8 万 / 总资产 10 万 = 0.80
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: uid, AvailableCash: 20000, TotalAsset: 100000, MarketValue: 80000,
	}); err != nil {
		t.Fatalf("UpsertRealAccount: %v", err)
	}
	// 权益序列：昨日 20 万 → 今日 10 万 ⇒ 回撤 0.50
	if err := db.RecordEquitySnapshot(uid, yesterday, 200000); err != nil {
		t.Fatalf("RecordEquitySnapshot: %v", err)
	}
	if err := db.RecordEquitySnapshot(uid, today, 100000); err != nil {
		t.Fatalf("RecordEquitySnapshot: %v", err)
	}
	// 当日一笔止损清仓（signal_id 冒号分段含「止损」）
	f := store.RealFill{
		OrderID: "S1", Code: "600001.SH", Side: "卖出", Price: 10, Qty: 100, Amount: 1000,
		UserID: uid, TradedAt: today + " 10:00:00", SignalID: "sell:600001:止损:" + today,
	}
	if err := db.ApplyRealFill(f); err != nil {
		t.Fatalf("ApplyRealFill: %v", err)
	}

	e := &Engine{userID: uid, realStore: db, cfgMgr: config.NewManagerWithRules(&config.Rules{})}
	sl, dd, tp, ok := e.accountSizingProbe(uid)
	if !ok {
		t.Fatal("有账本应 ok=true")
	}
	if sl != 1 {
		t.Fatalf("回看窗止损次数应为 1, got=%d", sl)
	}
	if dd < 0.4999 || dd > 0.5001 {
		t.Fatalf("权益回撤应为 0.50, got=%.4f", dd)
	}
	if tp < 0.7999 || tp > 0.8001 {
		t.Fatalf("总仓位占比应为 0.80, got=%.4f", tp)
	}

	// 端到端：探针读数经 Regime 喂进 ComputeSizing，总仓位上限因子确实生效（min 合成）。
	r := signalctl.Regime{SizeCfg: config.DefaultSizingConfig().WithDefaults()}
	r.Account = signalctl.AccountState{WindowStopLosses: sl, EquityDrawdown: dd, TotalPosPct: tp}
	res := signalctl.ComputeSizing(r, buySig())
	// 权重类全未命中 ⇒ 1.0；上限类 min(0.80/0.80=1.0, 0.10/0.50=0.20) = 0.20
	if res.Scale > 0.2001 || res.Scale < 0.1999 {
		t.Fatalf("权益回撤 50%% 应把缩放压到 0.20, got=%.4f reasons=%v", res.Scale, res.Reasons)
	}
}

// TestAccountSizingProbeNilStore 无账本时整组弃权。
func TestAccountSizingProbeNilStore(t *testing.T) {
	e := &Engine{userID: "u"}
	if _, _, _, ok := e.accountSizingProbe("u"); ok {
		t.Fatal("无实盘账本应 ok=false（整组弃权）")
	}
}

// TestRecordDailyEquitySnapshotDedups 权益日快照：无券商回报不落点；落点后同日去重。
func TestRecordDailyEquitySnapshotDedups(t *testing.T) {
	db := newProbeTestDB(t)
	uid := "u_snap"
	now := time.Now()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	// 先铺一个更高的昨日峰值，让"今天是否真写了"可观测
	if err := db.RecordEquitySnapshot(uid, yesterday, 200000); err != nil {
		t.Fatalf("RecordEquitySnapshot: %v", err)
	}
	e := &Engine{userID: uid, realStore: db}

	// 券商尚未回报 → 不落点（也不置去重位）
	e.recordDailyEquitySnapshot()
	if _, ok, _ := db.EquityDrawdown(uid, today, today); ok {
		t.Fatal("券商未回报时不应落当日权益点")
	}

	// 回报后落点：峰值 20 万 / 最新 10 万 ⇒ 回撤 0.50
	if err := db.UpsertRealAccount(store.RealAccount{UserID: uid, TotalAsset: 100000}); err != nil {
		t.Fatalf("UpsertRealAccount: %v", err)
	}
	e.recordDailyEquitySnapshot()
	dd, ok, err := db.EquityDrawdown(uid, yesterday, today)
	if err != nil || !ok {
		t.Fatalf("落点后应有序列, ok=%v err=%v", ok, err)
	}
	if dd < 0.4999 || dd > 0.5001 {
		t.Fatalf("回撤应为 0.50, got=%.4f", dd)
	}

	// 同日再调：去重，不覆盖（若被覆盖，回撤会变成 0.75）
	if err := db.UpsertRealAccount(store.RealAccount{UserID: uid, TotalAsset: 50000}); err != nil {
		t.Fatalf("UpsertRealAccount: %v", err)
	}
	e.recordDailyEquitySnapshot()
	if dd2, _, _ := db.EquityDrawdown(uid, yesterday, today); dd2 < 0.4999 || dd2 > 0.5001 {
		t.Fatalf("同日应去重不覆盖, got=%.4f", dd2)
	}
}

// TestSizingConfigForBridgesLegacyTotalPosPct 既有死字段 rules.position.max_total_position_pct
// 被接通；显式 sizing 值优先；百分数写法（80）归一化为 0.8。
func TestSizingConfigForBridgesLegacyTotalPosPct(t *testing.T) {
	// ① 只有老字段 → 桥接
	cm := config.NewManagerWithRules(&config.Rules{Position: config.PositionConfig{MaxTotalPositionPct: 0.6}})
	e := &Engine{userID: "u", cfgMgr: cm}
	if got := e.sizingConfigFor("u").MaxTotalPositionPct; got != 0.6 {
		t.Fatalf("老字段 0.6 应桥接到定权上限, got=%v", got)
	}

	// ② 百分数写法 80 → 0.8（否则 limit/max(limit,risk) 恒 1.0，接了个寂寞）
	cm2 := config.NewManagerWithRules(&config.Rules{Position: config.PositionConfig{MaxTotalPositionPct: 80}})
	e2 := &Engine{userID: "u", cfgMgr: cm2}
	if got := e2.sizingConfigFor("u").MaxTotalPositionPct; got != 0.8 {
		t.Fatalf("百分数 80 应归一化为 0.8, got=%v", got)
	}

	// ③ 显式 sizing 值优先于老字段
	cm3 := config.NewManagerWithRules(&config.Rules{
		Position: config.PositionConfig{MaxTotalPositionPct: 0.6},
		Sizing:   config.SizingConfig{MaxTotalPositionPct: 0.5},
	})
	e3 := &Engine{userID: "u", cfgMgr: cm3}
	if got := e3.sizingConfigFor("u").MaxTotalPositionPct; got != 0.5 {
		t.Fatalf("显式 sizing 值应优先, got=%v", got)
	}

	// ④ 两者都没写 → 出厂默认 **1.00**（§DYN-SIZE v4：语义已从"逐笔缩放乘子"改为"目标仓位天花板"，
	// owner 裁决大牛市可以满仓，弱市靠因子压低；0.80 作为天花板会把 20% 资金永久锁在账外）。
	cm4 := config.NewManagerWithRules(&config.Rules{})
	e4 := &Engine{userID: "u", cfgMgr: cm4}
	if got := e4.sizingConfigFor("u").MaxTotalPositionPct; got != 1.00 {
		t.Fatalf("都未设置应回落到出厂默认 1.00（目标天花板）, got=%v", got)
	}
}

// buySig 一个"权重类因子全未命中"的最小买入信号（用于反证上限类因子的独立作用）。
func buySig() combat_agent.Signal { return combat_agent.Signal{} }
