// 本文件：统一纪律（探针+扳机）的实盘侧单元测试——
//  1. §SIGNAL_CONTROLLER 20260917：实盘买入持续性确认（原 realBuyConfirmPass/pruneRealBuyConfirm）
//     已迁入信号控制器 live 通道，由 dispatchLive 统一编排：首探针受阻、满窗放行、消失重置、
//     放行后清除；
//  2. autoExecuteRealSells 纪律来源门（§统一纪律）：止损任意来源自动执行；
//     止盈/减仓仅 Source=discipline 自动执行；减仓半平每码每日一次（realTrimDone 去重）。
//
// English: live-side discipline tests — buy-confirmation now lives in the signal controller
// (dispatchLive integration below) and the §unified-discipline source gate in autoExecuteRealSells.
package engine

import (
	"fmt"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// TestDispatchLiveBuyConfirmWindow 持续性确认窗（live 通道）：低置信首探针受阻、窗内受阻、
// 满窗放行并清探针；高置信走快车道（30s）。参数取 DefaultQMTConfig().Discipline（5min/30s）。
// English: dispatchLive confirm window — first probe holds, persistence under the window holds,
// at-window passes and clears; high-confidence takes the 30s fast lane.
func TestDispatchLiveBuyConfirmWindow(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
	sig := combat_agent.Signal{ID: "S1", Code: "600000", Name: "浦发", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 10}
	now := time.Now()

	e.dispatchLive([]combat_agent.Signal{sig}, live, true, now) // 首探针 → hold
	if len(*orders) != 0 {
		t.Fatalf("首探针不应下单, got %d", len(*orders))
	}
	if d := e.liveDecisionOf(sig); d == nil || d.Verdict != signalctl.VerdictHold {
		t.Fatalf("首探针应标注 hold, got %+v", d)
	}
	e.dispatchLive([]combat_agent.Signal{sig}, live, true, now.Add(4*time.Minute)) // 窗内
	if len(*orders) != 0 {
		t.Fatalf("未满窗不应下单, got %d", len(*orders))
	}
	e.dispatchLive([]combat_agent.Signal{sig}, live, true, now.Add(6*time.Minute)) // 满窗 → pass
	if len(*orders) != 1 {
		t.Fatalf("满窗应放行 1 单, got %d", len(*orders))
	}
	if d := e.liveDecisionOf(sig); d != nil {
		t.Fatal("放行后注解应清除")
	}

	// 高置信快车道：≥85% 阈值只需 BuyConfirmHighSec（默认30s）。
	hs := combat_agent.Signal{ID: "S2", Code: "600001", Name: "测试", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 10, Confidence: 0.9}
	live1 := map[string]*data.StockInfo{"600001": {Code: "600001", Price: 10}}
	e.dispatchLive([]combat_agent.Signal{hs}, live1, true, now)
	if len(*orders) != 1 {
		t.Fatalf("高置信首探针不应即买（30s 观察）, got %d", len(*orders))
	}
	e.dispatchLive([]combat_agent.Signal{hs}, live1, true, now.Add(31*time.Second))
	if len(*orders) != 2 {
		t.Fatalf("高置信满 30s 应放行, got %d", len(*orders))
	}
}

// TestDispatchLivePruneOnAbsence 信号消失即重置探针：A 码建探针后不再活跃，
// 后续全量喂入（含 B 码）将其清理；A 码重现重新从首探针计窗（防跨轮累计插针）。
// English: probes for vanished signals are pruned by the next full feed, so a re-appearing signal
// restarts its confirmation window (no cross-gap accumulation).
func TestDispatchLivePruneOnAbsence(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	a := combat_agent.Signal{ID: "A", Code: "600000", Name: "A", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 10}
	b := combat_agent.Signal{ID: "B", Code: "600001", Name: "B", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 10}
	live := map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}, "600001": {Code: "600001", Price: 10}}
	now := time.Now()

	e.dispatchLive([]combat_agent.Signal{a}, live, true, now)                    // A 首探针
	e.dispatchLive([]combat_agent.Signal{b}, live, true, now.Add(8*time.Minute)) // B 的活跃集缺席 A → A 探针清理
	// A 重现于 t+14min：若旧探针仍在则早已满窗应放行；清理生效 → 视为首探针受阻。
	e.dispatchLive([]combat_agent.Signal{a}, live, true, now.Add(14*time.Minute))
	if len(*orders) != 0 {
		t.Fatalf("A 探针应已被清理，重现重新计窗不得下单, got %d: %+v", len(*orders), *orders)
	}
	// A 持续存在到满窗 → 正常放行（证明新探针从重现时刻起算）。
	e.dispatchLive([]combat_agent.Signal{a}, live, true, now.Add(20*time.Minute))
	if len(*orders) != 1 || (*orders)[0]["code"] != "600000.SH" {
		t.Fatalf("A 从重现时刻满窗应放行 1 单, got %+v", *orders)
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

// TestRealSoldOrOpenQtyInflight §P0-2（2026-09-15）回归：同轮 M8 清仓先占额度后，
// 止损建议的剩余量必须把「在途卖单」并入扣减——旧实现只扣已成交（fills 尚未回报时为 0），
// M8 与止损同轮各按全量下一笔全额卖单（第二笔靠柜台「证券不足」废单兜底）。
// 同时锁 §P0-3：止盈类全平的已成交量也必须计入（fullCloseClasses 补齐）。
func TestRealSoldOrOpenQtyInflight(t *testing.T) {
	e, db, _, _ := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = true; c.Enabled = true; c.Mode = "auto" })
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	userID := e.UserID()
	// 场景 A：止损单在途（已报未回报）→ 全平类不可再卖量 = 500
	if _, err := db.UpsertRealOrder(store.RealOrder{OrderID: "GW-OPEN", SignalID: realSellSignalID("600000.SH", "止损"),
		Code: "600000.SH", Side: "卖出", Status: "已报", Price: 9, Qty: 500, CreatedAt: time.Now().Format(time.RFC3339), UserID: userID}); err != nil {
		t.Fatal(err)
	}
	if got := e.realSoldOrOpenQtyToday(db, userID, "600000.SH"); got != 500 {
		t.Fatalf("在途止损 500 应全额占额度, got %d", got)
	}
	// 场景 B：终态（废单）与「发送失败」占位行不占额度（各用独立幂等键，signal_id 唯一约束）
	for i, st := range []struct {
		status, class string
		qty           int
	}{{"废单", "m8", 300}, {"发送失败", "止盈", 400}} {
		o := store.RealOrder{
			OrderID: fmt.Sprintf("GW-T%d", i), SignalID: realSellSignalID("600000.SH", st.class),
			Code: "600000.SH", Side: "卖出", Status: st.status, Price: 9, Qty: st.qty,
			CreatedAt: time.Now().Format(time.RFC3339), UserID: userID,
		}
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.realSoldOrOpenQtyToday(db, userID, "600000.SH"); got != 500 {
		t.Fatalf("终态/发送失败不得占额度（仍应=500）, got %d", got)
	}
	// 场景 C：止盈类已成交量计入（§P0-3 fullCloseClasses 补止盈）
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-TP", Code: "600000.SH", Side: "卖出",
		Price: 12, Qty: 100, Amount: 1200, TradedAt: time.Now().Format(time.RFC3339),
		SignalID: realSellSignalID("600000.SH", "止盈") + ":r100", UserID: userID}); err != nil {
		t.Fatal(err)
	}
	if got := e.realSoldOrOpenQtyToday(db, userID, "600000.SH"); got != 600 {
		t.Fatalf("止盈已成交 100 必须并入全平类扣减（500在途+100已成）, got %d", got)
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

	// 2. 减仓 Source=discipline → 半平整手 200（§P0-3：500/2=250 非整手会被柜台废单，取整到 200）。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "减仓", Level: "高", RefPrice: 9, Source: "discipline", Reason: "首触止损未深破"},
	})
	if len(*orders) != 1 {
		t.Fatalf("减仓 discipline 来源应自动卖出 1 单, got %d", len(*orders))
	}
	if o := (*orders)[0]; o["qty"].(float64) != 200 {
		t.Fatalf("减仓应半平整手 200（500/2=250 → 取整手 200，§P0-3）, got %+v", o)
	}

	// 3. 同日重放同一减仓建议 → realTrimDone 去重，不再二次减仓。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "减仓", Level: "高", RefPrice: 9, Source: "discipline", Reason: "首触止损未深破"},
	})
	if len(*orders) != 1 {
		t.Fatalf("减仓同日重放应去重不再下单, got %d", len(*orders))
	}
}
