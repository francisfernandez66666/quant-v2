// Package paper 独立模拟盘引擎：买入确认扳机（探针+扳机）单元测试。
// English: buy-confirmation gate (probe+trigger) unit tests for the paper engine.
package paper

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

// TestRecordBuyRejectLockedDedup 买入拒绝订单留痕去重：同一原因按码只留痕一次，
// 新原因/不同码再记录；成功成交后清除该码记录（后续同码新拒绝重新留痕）。
// English: rejected-buy order dedup — the same (code, reason) is audited once; a fresh reason or a
// different code re-records; a successful fill clears the entry so later rejects re-audit.
func TestRecordBuyRejectLockedDedup(t *testing.T) {
	e := New(testCfg(), "")
	now := time.Now()
	base := Order{Code: "600000.SH", Name: "浦发", Side: "buy", Kind: "自动撮合", SignalPrice: 10, Status: "rejected", CreatedAt: now}

	// 1. 同一原因重复 → 只留痕一次。
	e.recordBuyRejectLocked(base, "持仓数达上限(10)")
	e.recordBuyRejectLocked(base, "持仓数达上限(10)")
	if got := len(e.Orders()); got != 1 {
		t.Fatalf("同因拒绝应只留痕 1 条, got %d", got)
	}
	// 2. 新原因 → 再记录。
	e.recordBuyRejectLocked(base, "涨停封板无法买入(9.9%)")
	if got := len(e.Orders()); got != 2 {
		t.Fatalf("新原因应再留痕, got %d", got)
	}
	if got := e.Orders()[1].Reason; got != "涨停封板无法买入(9.9%)" {
		t.Fatalf("应记录新原因, got %q", got)
	}
	// 3. 不同码 + 旧原因 → 按码隔离再记录。
	base2 := base
	base2.Code = "000001.SZ"
	e.recordBuyRejectLocked(base2, "持仓数达上限(10)")
	if got := len(e.Orders()); got != 3 {
		t.Fatalf("不同码应再留痕, got %d", got)
	}
	// 4. 成交后清除：同码新拒绝重新留痕。
	if e.lastBuyReject != nil {
		delete(e.lastBuyReject, "600000.SH")
	}
	e.recordBuyRejectLocked(base, "现金不足")
	if got := len(e.Orders()); got != 4 {
		t.Fatalf("清除后同码新拒绝应重新留痕, got %d", got)
	}
}

// TestOnSignalsBuyConfirmGate 买入确认扳机：
//   - discipline 未注入（nil）= 旧行为，立即撮合；
//   - 低置信（<0.85）首探针不成交，记录首现时刻 + 待确认订单留痕，满 5min 持续存在才撮合；
//   - 信号中断（连续出现被打破）→ 确认记录清除，重来需重新累计；
//   - 高置信（≥0.85 对应后台阈值 85%）也不即买，需至少 30s 观察。
//
// English: buy-confirmation gate — nil discipline keeps legacy instant fill; low-confidence (<85) needs
// 5min continuous presence, high-confidence (≥85) needs 30s observation; interruption resets the timer.
func TestOnSignalsBuyConfirmGate(t *testing.T) {
	quotes := map[string]*data.StockInfo{"600000.SH": {Price: 10}}
	sig := combat_agent.Signal{Code: "600000.SH", Name: "浦发", Direction: "做多", Action: "buy",
		Price: 10, GeneratedAt: time.Now()}

	// 1. discipline 未注入（nil）：立即撮合（兼容旧行为）
	e := New(testCfg(), "")
	e.OnSignals([]combat_agent.Signal{sig}, quotes)
	if got := len(e.Positions()); got != 1 {
		t.Fatalf("discipline 未注入应立即成交, 持仓 %d", got)
	}

	// 2. 注入纪律：低置信首探针不成交（记录首现 + 待确认订单留痕）
	dc := config.DefaultDisciplineConfig()
	e2 := New(testCfg(), "")
	e2.SetDiscipline(&dc)
	e2.OnSignals([]combat_agent.Signal{sig}, quotes)
	if got := len(e2.Positions()); got != 0 {
		t.Fatalf("低置信首探针不应成交, 持仓 %d", got)
	}
	first, ok := e2.buyConfirm["600000.SH"]
	if !ok || first.IsZero() {
		t.Fatal("应记录买入信号首现探针时刻")
	}
	if got := len(e2.Orders()); got < 1 {
		t.Fatalf("应有待确认订单留痕, 实际 %d", got)
	}

	// 3. 信号中断：本轮无该信号 → 确认表清理，重来需重新累计
	e2.OnSignals(nil, quotes)
	if _, ok := e2.buyConfirm["600000.SH"]; ok {
		t.Fatal("信号中断后确认记录应被清除")
	}
	e2.OnSignals([]combat_agent.Signal{sig}, quotes)
	if first2, _ := e2.buyConfirm["600000.SH"]; first2.Equal(first) {
		t.Fatal("中断重来应重新记录首现时刻（不得跨轮累计）")
	}

	// 4. 低置信确认窗满（回拨首现时间）→ 撮合
	e2.buyConfirm["600000.SH"] = time.Now().Add(-time.Duration(dc.BuyConfirmMin+1) * time.Minute)
	e2.OnSignals([]combat_agent.Signal{sig}, quotes)
	if got := len(e2.Positions()); got != 1 {
		t.Fatalf("低置信持续满窗应撮合, 持仓 %d", got)
	}
	if _, ok := e2.buyConfirm["600000.SH"]; ok {
		t.Fatal("撮合后应清除确认记录")
	}

	// 5. 高置信：≥0.85（配置阈值 85%，Confidence 存 0~1）需 30s 观察；刚出现不成交，回拨满观察窗成交
	dc2 := config.DefaultDisciplineConfig()
	e3 := New(testCfg(), "")
	e3.SetDiscipline(&dc2)
	hi := sig
	hi.Code, hi.Name, hi.Confidence = "600001.SH", "平安", 0.9
	hiQ := map[string]*data.StockInfo{"600001.SH": {Price: 10}}
	e3.OnSignals([]combat_agent.Signal{hi}, hiQ)
	if got := len(e3.Positions()); got != 0 {
		t.Fatalf("高置信首探针也不应即买（防插针）, 持仓 %d", got)
	}
	e3.buyConfirm["600001.SH"] = time.Now().Add(-time.Duration(dc2.BuyConfirmHighSec+1) * time.Second)
	e3.OnSignals([]combat_agent.Signal{hi}, hiQ)
	if got := len(e3.Positions()); got != 1 {
		t.Fatalf("高置信观察满窗应撮合, 持仓 %d", got)
	}
}
