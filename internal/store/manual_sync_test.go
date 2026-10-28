package store

// §MANUAL-SYNC（2026-10-28）：委托状态守卫与手工单（ext: 键）落账口径。
// 覆盖：秩守卫的两条真实语义例外（已报⇄发送失败）、终态不可改写、ext 行可插入/可终结。

import "testing"

// TestOrderStatusAdvanceManualSync §MANUAL-SYNC：状态推进矩阵。
// 为什么单独测：这条函数现在是三个写入路径（回报/撤单/回填）共用的唯一裁决点，
// 一旦把「发送失败」当成普通低秩状态，僵尸「已报」就再也清不掉（预算永久被占），
// 反过来把 已报→发送失败 放得太宽又会诱导重复下单。
func TestOrderStatusAdvanceManualSync(t *testing.T) {
	cases := []struct {
		cur, next string
		want      bool
	}{
		{"已报", "部成", true},
		{"部成", "已成", true},
		{"已报", "已撤", true},
		{"已成", "已报", false}, // 终态不被迟到回报回退
		{"已撤", "已报", false},
		{"废单", "已报", false},
		{"已废", "已报", false}, // 网关拒单终态（网关侧字面量）
		{"已报", "已报", false}, // 等秩重放不是推进
		{"已报", "未知(255)", false},
		{"已报", "发送失败", true}, // 柜台证伪：其实没到券商 → 放开同日重放
		{"发送失败", "已报", true}, // 迟到真实回报证明券商确有此单
		{"发送失败", "部成", true},
		{"pending", "已报", true}, // 占位/待核对待瞬态
		{"待核对", "已报", true},
	}
	for _, c := range cases {
		if got := orderStatusAdvance(c.cur, c.next); got != c.want {
			t.Errorf("orderStatusAdvance(%q,%q)=%v want %v", c.cur, c.next, got, c.want)
		}
	}
}

// TestApplyOrderReportZombieToSendFailed §MANUAL-SYNC：僵尸「已报」可被柜台证伪为发送失败，
// 且迟到的真实「已报」又能把它补回来——两个方向都必须活着，否则会出现
// 「预算永远被占」或「重复下单」二选一。
func TestApplyOrderReportZombieToSendFailed(t *testing.T) {
	db := testDB(t)
	base := RealOrder{
		OrderID: "GW-1", SignalID: "buy:600000:fac_1:20261028", Code: "600000.SH",
		Side: "买入", Status: "已报", Price: 10, Qty: 100,
		CreatedAt: "2026-10-28 09:31:00", UserID: "u1",
	}
	if _, err := db.UpsertRealOrder(base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ev := base
	ev.Status = "发送失败"
	if act, err := db.ApplyOrderReportTx(ev); err != nil || act != OrderReportAdvanced {
		t.Fatalf("已报→发送失败应推进: act=%s err=%v", act, err)
	}
	if act, err := db.ApplyOrderReportTx(base); err != nil || act != OrderReportAdvanced {
		t.Fatalf("发送失败→已报应推进（真实回报纠正）: act=%s err=%v", act, err)
	}
}

// TestManualOrderExtRowLifecycle §MANUAL-SYNC：券商 App 手动单以 ext:<委托号> 落行、
// 手动撤单后终结——这是「僵尸单解除额度占用」的落库前提。
func TestManualOrderExtRowLifecycle(t *testing.T) {
	db := testDB(t)
	sid := "ext:12345"
	o := RealOrder{
		OrderID: "12345", SignalID: sid, Code: "600000.SH", Side: "买入",
		Status: "已报", Price: 10, Qty: 100, CreatedAt: "2026-10-28 09:31:00", UserID: "u1",
	}
	if act, err := db.ApplyOrderReportTx(o); err != nil || act != OrderReportInserted {
		t.Fatalf("手动单应补插: act=%s err=%v", act, err)
	}
	o.Status = "已撤"
	if act, err := db.ApplyOrderReportTx(o); err != nil || act != OrderReportAdvanced {
		t.Fatalf("手动撤单应推进为已撤: act=%s err=%v", act, err)
	}
	orders, _ := db.RealOrdersForUser("u1")
	if len(orders) != 1 || orders[0].Status != "已撤" || orders[0].SignalID != sid {
		t.Fatalf("手动单终态不符: %+v", orders)
	}
}
