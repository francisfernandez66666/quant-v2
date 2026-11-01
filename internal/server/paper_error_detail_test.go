// paper_error_detail_test.go — §F-4（2026-11-01）：模拟盘写面拒绝必须带决策上下文。
//
// 缺陷原文：动态仓位下被拒只回 `{"error":"动态仓位: 额度不足1手"}`。而同一套裁定在
// /api/signals/decisions 里带着 capped_by / room / cash / per_trade_pct / notional 全套——
// 用户看到"系统坏了"，运维拿不到可查的数。
//
// 本文件锁两件事：
//
//	① 响应体形状 = {error, detail{...}}，且 **error 键仍在**（既有前端按它分流）
//	② 非预算类错误不得被改形状（否则会顺带改掉所有其它 400 的契约）
//
// English: §F-4 — the write-path rejection carries the same decision context the read path already
// exposes, without changing the shape of unrelated 400s.
package server

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/paper"
)

// F1：预算拒绝 ⇒ 400 + error + detail 四个关键字段齐全。
func TestPaperBudgetErrorCarriesDetail(t *testing.T) {
	be := &paper.BudgetError{
		Reason:      "额度不足1手",
		CappedBy:    "target",
		Budget:      6000,
		OneLotCost:  150000,
		Price:       1500,
		Cash:        20000,
		Room:        0,
		PerTradePct: 0.30,
	}
	w := httptest.NewRecorder()
	writePaperOpError(w, be)

	if w.Code != 400 {
		t.Fatalf("预算拒绝应回 400，实得 %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v body=%s", err, w.Body.String())
	}
	// ① error 键必须在（前端按它分流；改成纯对象会当场炸消费点）
	if _, ok := body["error"]; !ok {
		t.Fatalf("必须保留 error 键，实得 %v", body)
	}
	detail, ok := body["detail"].(map[string]interface{})
	if !ok {
		t.Fatalf("必须带 detail 对象，实得 %v", body["detail"])
	}
	// ② 四个"让用户知道该改哪个参数"的字段
	for _, k := range []string{"capped_by", "budget", "one_lot_cost", "cash", "per_trade_pct", "room"} {
		if _, ok := detail[k]; !ok {
			t.Fatalf("detail 缺字段 %s，实得 %v", k, detail)
		}
	}
	if detail["capped_by"] != "target" {
		t.Fatalf("capped_by 应为 target，实得 %v", detail["capped_by"])
	}
}

// F2：错误文案要说清"差多少"，不能只有一句"额度不足1手"。
func TestPaperBudgetErrorExplainsOneLotFloor(t *testing.T) {
	be := &paper.BudgetError{Reason: "额度不足1手", Budget: 6000, OneLotCost: 150000}
	msg := be.Error()
	if !containsAll(msg, "额度不足1手", "6000.00", "150000.00") {
		t.Fatalf("文案应含原因与两个金额，实得 %q", msg)
	}
}

// F3：非预算类错误保持原形状（只有 error，没有 detail）——避免顺手改掉所有 400 的契约。
func TestPaperNonBudgetErrorShapeUnchanged(t *testing.T) {
	w := httptest.NewRecorder()
	writePaperOpError(w, errors.New("模拟盘未启用"))

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if _, ok := body["detail"]; ok {
		t.Fatalf("非预算类错误不应带 detail，实得 %v", body)
	}
	if body["error"] != "模拟盘未启用" {
		t.Fatalf("error 文案应原样透传，实得 %v", body["error"])
	}
}
