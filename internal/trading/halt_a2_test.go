// halt_a2_test.go §0925EVE-A2（2026-09-25 缺陷批）：kill-switch HaltAll 撤单失败回传明细。
// 旧实现失败只 log+continue、返回裸 int 成功数——"停止交易"按下后有几笔没撤掉，
// 操作者完全看不到（本仓主题「降级报成功」残留）。本文件锁死新契约：
//  1. 失败笔必须出现在 HaltAllResult.Failed（order_id + 原因），成功计数不被污染；
//  2. 全成功时 Failed 是空数组而非 nil——JSON 侧必须是 []，前端可无条件遍历；
//  3. 量规 halt_cancel_fail_count 每轮覆写（失败数进、清零出），支撑 p1 告警与成对销案。
//
// English: §0925EVE-A2 regression — HaltAll must surface per-order cancel failures (not just a
// success count), keep Failed a JSON-empty array on full success, and maintain the gauge.
package trading

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/store"
)

// haltPickFailExec 只让指定单号撤失败的假执行器：其余方法走 NoopExecutor 兜底。
// 为什么按单号点名失败：HaltAll 是批量循环，必须能构造"部分成功部分失败"的现场，
// 全体失败/全体成功都验证不了失败明细与成功计数互不污染这一点。
// English: fake executor failing Cancel for a chosen order id only.
type haltPickFailExec struct {
	NoopExecutor
	failOrderID string
	failErr     error
}

// Cancel 对点名单号返回注入错误，模拟网关 409（已成交/已撤/无法撤）等真实拒撤场景。
func (x haltPickFailExec) Cancel(orderID string) error {
	if x.failOrderID != "" && orderID == x.failOrderID {
		return x.failErr
	}
	return nil
}

// seedHaltOrders 往账本塞一批指定用户/状态的委托，返回控制器构造所需 db。
func seedHaltOrders(t *testing.T, db *store.DB, uid string, rows []store.RealOrder) {
	t.Helper()
	for _, o := range rows {
		o.UserID = uid
		if _, err := db.UpsertRealOrder(o); err != nil {
			t.Fatalf("seed %s: %v", o.OrderID, err)
		}
	}
}

// TestHaltAllReportsCancelFailures §0925EVE-A2 主用例：三笔在途（含一部成）+占位行+已成单，
// 其中一笔撤失败——成功计数只认撤成的两笔，失败明细精确到单号与原因，量规同步抬升。
func TestHaltAllReportsCancelFailures(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	now := time.Now().Format(time.RFC3339)
	seedHaltOrders(t, db, "u_a2", []store.RealOrder{
		{OrderID: "GW-A2-1", SignalID: "SIG-A2-1", Code: "600000.SH", Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: now},
		{OrderID: "GW-A2-2", SignalID: "SIG-A2-2", Code: "600000.SH", Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: now},
		{OrderID: "GW-A2-3", SignalID: "SIG-A2-3", Code: "600000.SH", Side: SideSell, Status: "部成", Price: 10, Qty: 100, CreatedAt: now},
		{OrderID: "pend:SIG-A2-P", SignalID: "SIG-A2-P", Code: "600000.SH", Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: now},
		{OrderID: "GW-A2-4", SignalID: "SIG-A2-4", Code: "600000.SH", Side: SideBuy, Status: "已成", Price: 10, Qty: 100, CreatedAt: now},
	})
	// GW-A2-2 点名失败：模拟网关拒撤（409 族）
	exec := haltPickFailExec{failOrderID: "GW-A2-2", failErr: errors.New("gateway 409: 当前状态不可撤")}
	ctrl := NewController(exec, db, "u_a2", cfg, nil)

	res := ctrl.HaltAll()

	// 成功计数：GW-A2-1（已报）与 GW-A2-3（部成）撤成；占位行与已成单不进批量，不得虚增
	if res.Cancelled != 2 {
		t.Fatalf("期望撤成 2 笔, got %d", res.Cancelled)
	}
	// 失败明细：恰好一条，单号与原因都要在——这是操作者能看到"有几笔没撤掉"的唯一凭证
	if len(res.Failed) != 1 {
		t.Fatalf("期望失败明细 1 条, got %+v", res.Failed)
	}
	if res.Failed[0].OrderID != "GW-A2-2" || !strings.Contains(res.Failed[0].Reason, "409") {
		t.Fatalf("失败明细应指向 GW-A2-2 且含网关原因, got %+v", res.Failed[0])
	}
	// 本地账本核验：撤成的推进为已撤；撤失败的保持原状（留给 SweepOrders/人工，不得假撤）
	statuses := map[string]string{}
	orders, _ := db.RealOrdersForUser("u_a2")
	for _, o := range orders {
		statuses[o.OrderID] = o.Status
	}
	if statuses["GW-A2-1"] != "已撤" || statuses["GW-A2-3"] != "已撤" {
		t.Fatalf("撤成单应落 已撤, got %+v", statuses)
	}
	if statuses["GW-A2-2"] != "已报" {
		t.Fatalf("撤失败单本地不得被推进, got %s", statuses["GW-A2-2"])
	}
	if statuses["pend:SIG-A2-P"] != "已报" || statuses["GW-A2-4"] != "已成" {
		t.Fatalf("占位行/已成单不应被 kill-switch 批量触碰, got %+v", statuses)
	}
	// 量规读数：失败 1 笔即 halt_cancel_fail_count=1（halt_cancel_failed p1 规则的触发面）
	if v, ok := metrics.GetGauge("halt_cancel_fail_count"); !ok || v != 1 {
		t.Fatalf("期望量规 halt_cancel_fail_count=1, got %d(ok=%v)", v, ok)
	}
}

// TestHaltAllFullSuccessFailedIsEmptyArray 反证用例：全部撤成时 Failed 必须是
// 空数组而非 nil——落到 HTTP JSON 必须是 "failed":[]。若这里是 null，前端
// `r.failed.length` 会直接 TypeError，"全成功"反而把页面点崩。
// English: counter-case — on full success Failed serializes as [] (never null).
func TestHaltAllFullSuccessFailedIsEmptyArray(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	now := time.Now().Format(time.RFC3339)
	seedHaltOrders(t, db, "u_a2ok", []store.RealOrder{
		{OrderID: "GW-OK-1", SignalID: "SIG-OK-1", Code: "600000.SH", Side: SideBuy, Status: "已报", Price: 10, Qty: 100, CreatedAt: now},
		{OrderID: "GW-OK-2", SignalID: "SIG-OK-2", Code: "600000.SH", Side: SideBuy, Status: "部成", Price: 10, Qty: 100, CreatedAt: now},
	})
	ctrl := NewController(haltPickFailExec{}, db, "u_a2ok", cfg, nil) // 无点名失败=全部撤成

	res := ctrl.HaltAll()
	if res.Cancelled != 2 {
		t.Fatalf("应撤成 2 笔, got %d", res.Cancelled)
	}
	if res.Failed == nil {
		t.Fatal("全成功时 Failed 也必须是空切片（JSON 序列化为 []），不得为 nil")
	}
	if len(res.Failed) != 0 {
		t.Fatalf("全成功时 Failed 应为空, got %+v", res.Failed)
	}
	// 直接验 JSON 契约：这是 HTTP 响应字段的最终形态，比 Go 值断言更贴近前端消费面
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"failed":[]`) {
		t.Fatalf(`响应必须含 "failed":[]，got %s`, b)
	}
	// 量规归零：上一用例抬到 1 后，本轮全成功必须写 0，否则告警 firing 态永不销案（resolved 发不出）
	if v, _ := metrics.GetGauge("halt_cancel_fail_count"); v != 0 {
		t.Fatalf("全成功后量规应归零, got %d", v)
	}
}
