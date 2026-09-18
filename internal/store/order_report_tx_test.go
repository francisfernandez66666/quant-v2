// §A4（20260918 全栈审计批）ApplyOrderReportTx 单事务委托回报落库用例：
// 覆盖旧两步写（AdvanceRealOrderStatus → 未命中再 UpsertRealOrder 补插）合并后的全部三态：
// 推进（advanced）/ 补插（inserted）/ 幂等 no-op（ignored），以及乱序重放序列下的最终态。
// English: §A4 tests for the folded single-transaction order-report write.
package store

import "testing"

// TestApplyOrderReportTxAdvance 本地已有占位行：高秩回报推进、等秩/低秩一律 ignored 且不留痕。
func TestApplyOrderReportTxAdvance(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "pend:A4-1", SignalID: "A4-1", Code: "600000.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-18T09:30:00+08:00", UserID: "u1"}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("upsert placeholder: %v", err)
	}
	// 高秩"部成"→ advanced，状态落库
	rep := o
	rep.Status = "部成"
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportAdvanced {
		t.Fatalf("部成应推进: act=%s err=%v", act, err)
	}
	// 等秩重放 → ignored（幂等）
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportIgnored {
		t.Fatalf("等秩重放应 no-op: act=%s err=%v", act, err)
	}
	// 低秩晚到 → ignored，绝不回退
	rep.Status = "已报"
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportIgnored {
		t.Fatalf("低秩晚到应 no-op: act=%s err=%v", act, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if len(os) != 1 || os[0].Status != "部成" {
		t.Fatalf("三态回报序列后本地应停留部成, got %+v", os)
	}
}

// TestApplyOrderReportTxInsertWhenAbsent 本地无单：补插完整委托行（含归属账号），重复回报幂等。
func TestApplyOrderReportTxInsertWhenAbsent(t *testing.T) {
	db := testDB(t)
	rep := RealOrder{OrderID: "GW-A4-2", SignalID: "A4-2", Code: "600519.SH", Side: "卖出",
		Status: "已成", Price: 1500, Qty: 200, CreatedAt: "2026-09-18T14:00:00+08:00", UserID: "u1"}
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportInserted {
		t.Fatalf("无单回报应补插: act=%s err=%v", act, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if len(os) != 1 || os[0].OrderID != "GW-A4-2" || os[0].Status != "已成" || os[0].Price != 1500 || os[0].Qty != 200 {
		t.Fatalf("补插行字段应完整, got %+v", os)
	}
	// 同回报重放：行已在终态（秩最高），秩不升 → ignored，不产生第二行
	if act, err := db.ApplyOrderReportTx(rep); err != nil || act != OrderReportIgnored {
		t.Fatalf("终态重放应 no-op: act=%s err=%v", act, err)
	}
	if os, _ = db.RealOrdersForUser("u1"); len(os) != 1 {
		t.Fatalf("重放不得产生重复行, got %+v", os)
	}
}

// TestApplyOrderReportTxUserScope 作用域口径与 AdvanceRealOrderStatus 一致：
// 同 signal_id 不同账号各插各的，互不越界（orders 唯一键为 (user_id, signal_id)）。
func TestApplyOrderReportTxUserScope(t *testing.T) {
	db := testDB(t)
	base := RealOrder{SignalID: "A4-3", Code: "600000.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-18T09:30:00+08:00"}
	base.OrderID, base.UserID = "GW-U1", "u1"
	if act, err := db.ApplyOrderReportTx(base); err != nil || act != OrderReportInserted {
		t.Fatalf("u1 补插: act=%s err=%v", act, err)
	}
	base.OrderID, base.UserID, base.Status = "GW-U2", "u2", "部成"
	if act, err := db.ApplyOrderReportTx(base); err != nil || act != OrderReportInserted {
		t.Fatalf("u2 应独立补插: act=%s err=%v", act, err)
	}
	os, _ := db.RealOrdersForUser("u1")
	if len(os) != 1 || os[0].Status != "已报" {
		t.Fatalf("u1 行不得被 u2 回报覆盖, got %+v", os)
	}
}

// TestApplyOrderReportTxOut-of-orderReplay 乱序重放整序列（已成→已报→部成）终态为最高秩，
// 等价于旧两步实现在无崩溃情况下的合并结果，且任意一步都无半写。
func TestApplyOrderReportTxOutOfOrderReplay(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "pend:A4-4", SignalID: "A4-4", Code: "600000.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-18T09:30:00+08:00", UserID: "u1"}
	if _, err := db.UpsertRealOrder(o); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for _, s := range []string{"已成", "已报", "部成", "已撤", "已成"} {
		r := o
		r.Status = s
		if _, err := db.ApplyOrderReportTx(r); err != nil {
			t.Fatalf("replay %s: %v", s, err)
		}
	}
	os, _ := db.RealOrdersForUser("u1")
	if len(os) != 1 || os[0].Status != "已成" {
		t.Fatalf("乱序重放终态应为已成, got %+v", os)
	}
}

// TestApplyOrderReportTxLegacyGlobalScope userID 为空只作用于遗留全局行（与旧函数口径一致）。
func TestApplyOrderReportTxLegacyGlobalScope(t *testing.T) {
	db := testDB(t)
	o := RealOrder{OrderID: "pend:A4-5", SignalID: "A4-5", Code: "600000.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-09-18T09:30:00+08:00", UserID: ""}
	if act, err := db.ApplyOrderReportTx(o); err != nil || act != OrderReportInserted {
		t.Fatalf("全局行补插: act=%s err=%v", act, err)
	}
	o.Status = "部成"
	if act, err := db.ApplyOrderReportTx(o); err != nil || act != OrderReportAdvanced {
		t.Fatalf("全局行推进: act=%s err=%v", act, err)
	}
	// 非空账号不得命中全局行：应走独立补插（真实回报单号全局唯一，此处须换新 order_id）
	o.UserID = "u9"
	o.OrderID = "GW-A4-5-U9"
	if act, err := db.ApplyOrderReportTx(o); err != nil || act != OrderReportInserted {
		t.Fatalf("u9 应独立补插而非推进全局行: act=%s err=%v", act, err)
	}
}
