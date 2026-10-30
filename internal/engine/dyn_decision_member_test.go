// dyn_decision_member_test.go — §W-03（2026-10-29 夜批）决策快照逐成员落点。
//
// 引擎按配置指纹在 coreUsers 多账号间共享。快照若只写 e.userID 一个人，共享引擎下其他成员
// 打开「今日动态仓位」恒空——这正是现网三日 count:0 的另一半成因（另一半是 W-02 的 paper 通道）。
package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

func decisionFixture(t *testing.T) (*store.DB, combat_agent.Signal, *dynBuyReadout) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	sig := combat_agent.Signal{
		ID: "SIG-DEC", Code: "600000.SH", Name: "浦发", StrategyID: "s1", Strategy: "龙头", Action: "buy",
	}
	d := &dynBuyReadout{
		At: time.Now(), MarketScale: 0.6, StockScale: 1.0, PerTradePct: 0.2,
		Total: 200000, Cash: 100000, Target: 120000, CurStrat: 100000, Room: 20000,
		Qty: 1000, Notional: 10000,
	}
	return db, sig, d
}

// C1：两个成员共享同一 Engine ⇒ 两个 uid 各 1 行，且三字段一致。
func TestDynDecisionWrittenPerMember(t *testing.T) {
	db, sig, d := decisionFixture(t)
	e := &Engine{}
	e.realStore = db
	e.SetMembers([]string{"u_a", "u_b"})
	e.persistDynBuyDecision(sig, d)

	day := data.TradingDayDate(d.At)
	for _, uid := range []string{"u_a", "u_b"} {
		rows, err := db.SignalDecisionsForDay(uid, day)
		if err != nil {
			t.Fatalf("SignalDecisionsForDay(%s): %v", uid, err)
		}
		if len(rows) != 1 {
			t.Fatalf("成员 %s 应有 1 行决策快照，实得 %d（共享引擎下只有主账号有行）", uid, len(rows))
		}
		r := rows[0]
		if r.Channel != "live" || r.Code != "600000" || r.Stage != "evaluated" || r.Qty != 1000 {
			t.Fatalf("成员 %s 的快照字段不一致：%+v", uid, r)
		}
	}
}

// C2：单成员（members 为空、userID 有值）⇒ 仍 1 行，防回归到 0 行。
func TestDynDecisionSingleMemberUnchanged(t *testing.T) {
	db, sig, d := decisionFixture(t)
	e := &Engine{}
	e.realStore = db
	e.userID = "u_solo" // 老装配：只有 userID，没有 members
	e.persistDynBuyDecision(sig, d)

	rows, err := db.SignalDecisionsForDay("u_solo", data.TradingDayDate(d.At))
	if err != nil {
		t.Fatalf("SignalDecisionsForDay: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("单成员引擎应仍落 1 行，实得 %d", len(rows))
	}
}
