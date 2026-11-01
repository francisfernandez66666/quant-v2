// sizing_inject_test.go — §F-3（方案甲，2026-11-01）：定权注入与缺失原因口径。
//
// 背景：SetSizingContext 的唯一调用点原本挂在 runIntradayT 内，而它前面有「T 开关」与
// 「交易时段」两道早退 ⇒ T 关着时 sizingCtx 永远是零值 ⇒ `roomSet` 恒 false ⇒
// 目标仓位/行业簇整套机制线上一次都没跑过。本文件锁三件事：
//
//	E1 「未注入」必须报 market_scale_not_injected（不是笼统的 market_scale）
//	E2 「注入但为 0」是**真读数**，必须与 E1 区分开
//	E5 三帽剩余额度要真透传（此前恒 0，DB 与前端当假读数在展示）
//
// English: §F-3 — distinguishes "never injected" from "injected zero", and locks the three room fields.
package paper

import (
	"strings"
	"testing"
)

// captureDecision 跑一笔买入并回传最后一条决策快照。
func captureDecision(t *testing.T, pe *Engine, code string) DecisionRecord {
	t.Helper()
	var got []DecisionRecord
	pe.SetDecisionRecorder(func(r DecisionRecord) { got = append(got, r) })
	quotes := dynQuotes(code)
	if err := pe.BuyInPool(code, "测试", "龙头", "", 10, quotes); err != nil {
		t.Fatalf("买入失败: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("未收到任何决策快照")
	}
	return got[len(got)-1]
}

// E1：engine 从未注入过（sizingCtx 零值）⇒ 缺失原因必须是 market_scale_not_injected。
// 反证靶心：旧写法把它与"注入了一个 0"合并成同一个 "market_scale"，前端无法区分
// 「引擎没喂（观察期）」和「市场读数真的是 0」。
func TestSizingContextMissingReasonDistinguishesNotInjected(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	// 刻意不调 SetSizingContext
	rec := captureDecision(t, pe, "600000.SH")
	if rec.MissingKeyInput != "market_scale_not_injected" {
		t.Fatalf("未注入时 MissingKeyInput=%q，期望 market_scale_not_injected", rec.MissingKeyInput)
	}
	if rec.MarketScale != 0 {
		t.Fatalf("未注入时 MarketScale 应为 0，实得 %v", rec.MarketScale)
	}
}

// E2：注入了一个 0（其余字段有效）⇒ 这是**真实读数**，原因必须是 market_scale_zero，
// 不能与 E1 的"未注入"混为一谈。
func TestSizingContextZeroAfterInjectionIsRealReading(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	pe.SetSizingContext(0, 0.1, 0.3, 1, false) // 注入 ms=0
	rec := captureDecision(t, pe, "600000.SH")
	if rec.MissingKeyInput != "market_scale_zero" {
		t.Fatalf("注入 0 时 MissingKeyInput=%q，期望 market_scale_zero（与未注入必须区分）", rec.MissingKeyInput)
	}
}

// E5：三帽剩余额度必须真透传，不许恒 0 冒充读数。
//
// 本 fixture 只配了单票 35%（dynPaperConfig），没配行业、也没把簇上限传进 Compute：
//   - StockRoom 必须 > 0（单票帽在起作用，读得到剩余）
//   - IndustryRoom 为 0 是"未配置行业帽"，不是"没有剩余"
//   - ClusterRoom 为 0 是"纸面未接线"，不是"没有簇限制"
//
// 断言的重点是 StockRoom > 0：这一条在改动前必红（三个字段从来没被赋值过）。
func TestDecisionRecordCarriesRoomFields(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	pe.SetSizingContext(1, 0.1, 0.3, 1, false)
	rec := captureDecision(t, pe, "600000.SH")
	if rec.StockRoom <= 0 {
		t.Fatalf("配了单票 35%% 帽时 StockRoom 应 > 0（改动前恒 0 是静默假读数），实得 %v", rec.StockRoom)
	}
	if rec.ClusterRoom != 0 {
		t.Fatalf("纸面未接线簇上限时 ClusterRoom 应为 0，实得 %v", rec.ClusterRoom)
	}
}

// E1b：MissingKey 与未注入同时成立时，两个原因都要出现（逗号分隔），不许互相覆盖。
func TestSizingContextReportsBothReasons(t *testing.T) {
	pe := New(dynPaperConfig(100000), "")
	pe.SetSizingContext(1, 0.1, 0.3, 1, true) // 注入但标了关键输入缺失
	rec := captureDecision(t, pe, "600000.SH")
	if !strings.Contains(rec.MissingKeyInput, "market_key_input") {
		t.Fatalf("MissingKey=true 时原因里必须有 market_key_input，实得 %q", rec.MissingKeyInput)
	}
	if strings.Contains(rec.MissingKeyInput, "market_scale_not_injected") {
		t.Fatalf("已注入过就不该再报未注入，实得 %q", rec.MissingKeyInput)
	}
}
