// 本文件：统一纪律（探针+扳机）的实盘侧单元测试——
//  1. 实盘买入确认扳机 realBuyConfirmPass / pruneRealBuyConfirm：
//     低置信需持续满窗、高置信快车道、信号中断清理、确认后清除、禁用直通；
//  2. autoExecuteRealSells 纪律来源门（§统一纪律）：止损任意来源自动执行；
//     止盈/减仓仅 Source=discipline 自动执行；减仓半平每码每日一次（realTrimDone 去重）。
//
// English: live-side unified-discipline tests — the real buy-confirmation gate (realBuyConfirmPass /
// pruneRealBuyConfirm) and the discipline-source gate in autoExecuteRealSells (stop-loss from any
// source; TP/trim only when Source=discipline; trim half-qty dedup once per code per day).
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// TestRealBuyConfirmPass 实盘买入确认扳机：未启用（两窗≤0）直通；低置信首探针受阻并记录首现时刻，
// 持续存在满 BuyConfirmMin 才放行且放行后清除；高置信（≥阈值对应 0~1）走 BuyConfirmHighSec 快车道。
// English: the real buy-confirm gate — disabled (both windows ≤0) passes instantly; a low-confidence
// signal is blocked on the first probe (first-appearance time recorded) and passes only after persisting
// BuyConfirmMin, then the entry is cleared; high-confidence (≥ threshold in 0~1) uses the fast lane.
func TestRealBuyConfirmPass(t *testing.T) {
	e, _, _, _ := newQMTEngine(t, nil)

	// 1. 未启用（两窗 ≤0）→ 直通，不记录。
	discDis := config.DefaultDisciplineConfig()
	discDis.BuyConfirmMin, discDis.BuyConfirmHighSec = 0, 0
	if !e.realBuyConfirmPass("600000", 0.5, discDis) {
		t.Fatal("禁用买入确认时应直通放行")
	}

	// 2. 低置信：默认 5min 确认窗，首探针受阻。
	disc := config.DefaultDisciplineConfig()
	if e.realBuyConfirmPass("600000", 0.5, disc) {
		t.Fatal("低置信首探针不应放行")
	}
	e.mu.RLock()
	_, tracked := e.buyConfirmReal["600000"]
	e.mu.RUnlock()
	if !tracked {
		t.Fatal("低置信首探针应记录首现时刻")
	}
	// 同一轮（未满窗）仍受阻。
	if e.realBuyConfirmPass("600000", 0.5, disc) {
		t.Fatal("未满确认窗不应放行")
	}

	// 3. 持续存在满窗：回拨首现时刻 → 放行，且放行后清除。
	e.mu.Lock()
	e.buyConfirmReal["600000"] = time.Now().Add(-time.Duration(disc.BuyConfirmMin+1) * time.Minute)
	e.mu.Unlock()
	if !e.realBuyConfirmPass("600000", 0.5, disc) {
		t.Fatal("低置信满窗应放行")
	}
	e.mu.RLock()
	_, still := e.buyConfirmReal["600000"]
	e.mu.RUnlock()
	if still {
		t.Fatal("放行后应清除确认记录")
	}

	// 4. 高置信（0.9 ≥ 0.85 阈值归一）→ BuyConfirmHighSec 快车道：首探针受阻，满 30s 放行。
	if e.realBuyConfirmPass("600001", 0.9, disc) {
		t.Fatal("高置信首探针也不应即买（防插针）")
	}
	e.mu.Lock()
	e.buyConfirmReal["600001"] = time.Now().Add(-time.Duration(disc.BuyConfirmHighSec+1) * time.Second)
	e.mu.Unlock()
	if !e.realBuyConfirmPass("600001", 0.9, disc) {
		t.Fatal("高置信满 30s 观察应放行")
	}
}

// TestPruneRealBuyConfirm 清理实盘买入确认表：本轮无信号的记录清除（信号须连续存在），
// 超过最大观察窗的僵尸记录清除；活跃记录保留。
// English: prunes the real buy-confirm table — codes without a signal this round are dropped (presence
// must be continuous), over-max-window zombies cleaned, active entries preserved.
func TestPruneRealBuyConfirm(t *testing.T) {
	e, _, _, _ := newQMTEngine(t, nil)
	disc := config.DefaultDisciplineConfig()
	e.mu.Lock()
	e.buyConfirmReal = map[string]time.Time{
		"600000": time.Now().Add(-time.Minute),                                       // 活跃
		"600001": time.Now().Add(-time.Minute),                                       // 本轮无信号 → 清
		"600002": time.Now().Add(-time.Duration(disc.BuyConfirmMin+2) * time.Minute), // 僵尸 → 清
	}
	e.mu.Unlock()

	seen := map[string]struct{}{"600000": {}, "600002": {}}
	e.pruneRealBuyConfirm(seen, disc)

	e.mu.RLock()
	_, ok0 := e.buyConfirmReal["600000"]
	_, ok1 := e.buyConfirmReal["600001"]
	_, ok2 := e.buyConfirmReal["600002"]
	e.mu.RUnlock()
	if !ok0 {
		t.Fatal("活跃记录应保留")
	}
	if ok1 {
		t.Fatal("本轮无信号的记录应清除")
	}
	if ok2 {
		t.Fatal("超最大观察窗的僵尸记录应清除")
	}
}

// TestAutoExecuteRealSellsDisciplineSource §统一纪律来源门：
//   - 止损：任意来源（Source 为空=常规卖出侧）都自动全仓卖出（保护性不变）；
//   - 止盈：Source=discipline（纪律裁决引擎）→ 自动全平；Source 为空（战法自带止盈）→ 仅通知不下单；
//   - 减仓：Source=discipline → 半平（剩余/2）；Source 为空 → 不下单；
//   - 减仓每码每日一次（realTrimDone 去重）：同日重放同一减仓建议不再二次减仓。
//
// English: §unified-discipline source gate — 止损 auto-executes from any source; 止盈/减仓 only when
// Source=discipline (strategy-native TP/SL degrade to notifications); trim halts the remaining once per
// code per day (realTrimDone dedup blocks same-day re-fires).
func TestAutoExecuteRealSellsDisciplineSource(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = true })
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := e.QMTController()

	// 1. 止盈 Source 为空（战法自带）→ 不自动执行。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止盈", Level: "高", RefPrice: 12, Reason: "战法止盈"},
	})
	if len(*orders) != 0 {
		t.Fatalf("止盈无 discipline 来源不应自动卖出, got %d", len(*orders))
	}

	// 2. 止盈 Source=discipline → 全平。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止盈", Level: "高", RefPrice: 12, Source: "discipline", Reason: "止盈窗结算"},
	})
	if len(*orders) != 1 {
		t.Fatalf("止盈 discipline 来源应自动卖出 1 单, got %d", len(*orders))
	}
	if o := (*orders)[0]; o["side"] != "卖出" || o["qty"].(float64) != 500 {
		t.Fatalf("止盈应全平 500, got %+v", o)
	}
}

// TestAutoExecuteRealSellsTrimHalfOnce 减仓纪律：discipline 来源半平一次；同日重放去重不二次减仓。
// English: discipline trim — a Source=discipline 减仓 sells half once and same-day re-fires are deduped.
func TestAutoExecuteRealSellsTrimHalfOnce(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = true })
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := e.QMTController()

	// 1. 减仓 Source 为空（战法自带减仓）→ 不下单。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "减仓", Level: "高", RefPrice: 9, Reason: "战法减仓"},
	})
	if len(*orders) != 0 {
		t.Fatalf("减仓无 discipline 来源不应自动卖出, got %d", len(*orders))
	}

	// 2. 减仓 Source=discipline → 半平 250（剩余 500/2）。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "减仓", Level: "高", RefPrice: 9, Source: "discipline", Reason: "首触止损未深破"},
	})
	if len(*orders) != 1 {
		t.Fatalf("减仓 discipline 来源应自动卖出 1 单, got %d", len(*orders))
	}
	if o := (*orders)[0]; o["qty"].(float64) != 250 {
		t.Fatalf("减仓应半平 250（500/2）, got %+v", o)
	}

	// 3. 同日重放同一减仓建议 → realTrimDone 去重，不再二次减仓。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "减仓", Level: "高", RefPrice: 9, Source: "discipline", Reason: "首触止损未深破"},
	})
	if len(*orders) != 1 {
		t.Fatalf("减仓同日重放应去重不再下单, got %d", len(*orders))
	}
}
