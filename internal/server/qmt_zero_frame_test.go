// qmt_zero_frame_test.go — §ZERO-FRAME（2026-11-20）全 0 资产帧回归锁。
//
// 现网实录（2026-11-20）：`账户资产上报 … 可用=0.00 冻结=0.00 总值=0.00 市值=0.00` 每约 30 分钟
// 出现一次（10:15:37 / 10:45:37 / 11:15:36 / 11:34:46），每一次都被当成新鲜读数覆盖 real_account，
// 页面上的「可用资金」于是反复跳成 ¥0.00，约 6 分钟后才恢复 48565.54。
//
// 成因在载体侧：`periodic_reconcile` 每 60s 把 `broker.query_asset()` 原样上报且把 synced 写死 True，
// 而 QMT 会话重载/未同步时 `query_stock_asset` 返回全 0 的 XtAsset。Go 侧过去只判
// synced/data_ts（同日即过），无法区分"账户真没钱"与"查询没同步"。
//
// 本文件锁三态：
//
//	① 已有一笔正数资金（或仍有持仓）时，全 0 帧 ⇒ 200 + skipped=zero_frame，账上余额一字不改；
//	② 本来就是空账 ⇒ 全 0 帧照常落库（那是真 0，不许把"新账号首帧"也吞掉）；
//	③ 非全 0 帧（哪怕只有一项非 0）⇒ 走既有落库路径，行为一字不变。
//
// English: locks for the §ZERO-FRAME gate — an all-zero asset frame that contradicts the known book
// is skipped (no write, updated_at untouched); a genuinely empty book still accepts a real zero.
package server

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestAccountZeroFrameSkippedWhenContradictsBook ①：正数余额 + 全 0 帧 ⇒ 弃权，余额保留。
func TestAccountZeroFrameSkippedWhenContradictsBook(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID:        reportTestUser,
		AvailableCash: 48565.54,
		TotalAsset:    96723.54,
		MarketValue:   48158,
		UpdatedAt:     "2026-11-20 11:54:16",
	}); err != nil {
		t.Fatalf("种初始资金: %v", err)
	}

	body := `{"type":"account","user_id":"` + reportTestUser + `",` +
		`"asset":{"cash":0,"frozen_cash":0,"total_asset":0,"market_value":0},` +
		`"synced":true,"data_ts":"2026-11-20T11:15:36+08:00"}`
	code, resp := postReport(t, s, body)
	if code != 200 {
		t.Fatalf("全 0 帧应 200（让网关 outbox 正常结算），got %d body=%s", code, resp)
	}
	if !strings.Contains(resp, "zero_frame") {
		t.Fatalf("响应应标记 skipped=zero_frame，got %s", resp)
	}

	acc, err := db.GetRealAccountOwn(reportTestUser)
	if err != nil {
		t.Fatalf("读回账户: %v", err)
	}
	if acc.AvailableCash != 48565.54 || acc.TotalAsset != 96723.54 {
		t.Fatalf("正数余额被全 0 帧覆盖：可用=%.2f 总值=%.2f", acc.AvailableCash, acc.TotalAsset)
	}
	if acc.UpdatedAt != "2026-11-20 11:54:16" {
		t.Fatalf("弃权帧不得刷新 updated_at（否则陈旧值被冒充满新），got %q", acc.UpdatedAt)
	}
}

// TestAccountZeroFrameSkippedWhenHoldsPositions ①b：余额为 0 但账上仍有持仓 ⇒ 全 0 帧同样弃权。
// 判据不能只看余额：清仓前的"可用 0 + 市值 0"在数学上不可能（持仓市值不会是 0）。
func TestAccountZeroFrameSkippedWhenHoldsPositions(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	if _, err := db.UpsertRealPositions([]store.RealPosition{{
		TsCode: "600519.SH", Name: "贵州茅台", Qty: 100, CostPrice: 1500, Amount: 150000,
		UserID: reportTestUser, BuyDate: "2026-11-19", UpdatedAt: "2026-11-20 09:30:00",
	}}); err != nil {
		t.Fatalf("种持仓: %v", err)
	}

	body := `{"type":"account","user_id":"` + reportTestUser + `",` +
		`"asset":{"cash":0,"frozen_cash":0,"total_asset":0,"market_value":0},` +
		`"synced":true,"data_ts":"2026-11-20T11:34:46+08:00"}`
	if code, resp := postReport(t, s, body); code != 200 || !strings.Contains(resp, "zero_frame") {
		t.Fatalf("有持仓时全 0 帧应弃权，got %d body=%s", code, resp)
	}
	acc, err := db.GetRealAccountOwn(reportTestUser)
	if err == nil && acc.AvailableCash != 0 {
		// 没有初始资金行时不应被写出行；若被写出也必须是 0（真 0），不能是别的值。
		t.Fatalf("弃权帧不应写出非 0 资金：%+v", acc)
	}
}

// TestAccountZeroFrameAcceptedOnEmptyBook ②：空账 + 全 0 帧 ⇒ 照常落库（那是真 0）。
func TestAccountZeroFrameAcceptedOnEmptyBook(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	body := `{"type":"account","user_id":"` + reportTestUser + `",` +
		`"asset":{"cash":0,"frozen_cash":0,"total_asset":0,"market_value":0},` +
		`"synced":true,"data_ts":"2026-11-20T09:30:00+08:00"}`
	code, resp := postReport(t, s, body)
	if code != 200 {
		t.Fatalf("空账全 0 帧应 200，got %d body=%s", code, resp)
	}
	if strings.Contains(resp, "zero_frame") {
		t.Fatalf("空账的真 0 不该被当成占位读数吞掉: %s", resp)
	}
	acc, err := db.GetRealAccountOwn(reportTestUser)
	if err != nil {
		t.Fatalf("空账真 0 应落库: %v", err)
	}
	if acc.AvailableCash != 0 || acc.TotalAsset != 0 {
		t.Fatalf("落库值应为 0，got %+v", acc)
	}
}

// TestAccountPartialFrameWinsOverGate ③：只要有一项非 0 就走既有路径（负值也算真读数）。
func TestAccountPartialFrameWinsOverGate(t *testing.T) {
	s, db, _ := newTestResearchServer(t)
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: reportTestUser, AvailableCash: 48565.54, TotalAsset: 96723.54,
		MarketValue: 48158, UpdatedAt: "2026-11-20 11:54:16",
	}); err != nil {
		t.Fatalf("种初始资金: %v", err)
	}
	// 可用为 0 但市值非 0：这是真实形态（满仓），绝不能判成占位帧。
	// 刻意不带 data_ts：本用例判的是"全 0 闸"，不是新鲜度闸（未来帧会被上层按设计拒收）。
	body := `{"type":"account","user_id":"` + reportTestUser + `",` +
		`"asset":{"cash":0,"frozen_cash":0,"total_asset":48158,"market_value":48158}}`
	if code, resp := postReport(t, s, body); code != 200 || strings.Contains(resp, "zero_frame") {
		t.Fatalf("非全 0 帧应走既有路径，got %d body=%s", code, resp)
	}
	acc, err := db.GetRealAccountOwn(reportTestUser)
	if err != nil {
		t.Fatalf("读回账户: %v", err)
	}
	if acc.AvailableCash != 0 || acc.TotalAsset != 48158 {
		t.Fatalf("满仓帧应落库，got 可用=%.2f 总值=%.2f", acc.AvailableCash, acc.TotalAsset)
	}
	if acc.UpdatedAt == "2026-11-20 11:54:16" {
		t.Fatalf("非全 0 帧应刷新 updated_at，got %q", acc.UpdatedAt)
	}
}
