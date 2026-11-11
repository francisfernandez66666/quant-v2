package store

// 2026-11-11 owner 实锤两格：① 清掉最后一只票后手动账必须仍然算「已维护」（否则读面
// 回落六天前的柜台行，屏上原持仓股数不动）；② 「直接改资金」要能同时维护总资产，
// 且不填时绝不按现金+成本瞎推一个市值合计。
// English: an explicitly maintained empty book stays authoritative; total_asset is
// written only when the user supplies it.

import "testing"

func TestManualEmptyBookAfterCloseIsUsable(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_empty_book"
	today := manualToday()
	asof := manualAsofOn(today)
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{
		{TsCode: "600000.SH", Name: "浦发银行", Qty: 500, CostPrice: 10},
	}, asof); err != nil {
		t.Fatalf("写初始持仓: %v", err)
	}
	if err := db.PutManualAccount(uid, 50000, true, 0, asof); err != nil {
		t.Fatalf("写初始资金: %v", err)
	}
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{
		Op: ManualOpClose, TsCode: "600000.SH", Qty: 500, Price: 10, AsOf: asof, User: uid,
	}, today); err != nil {
		t.Fatalf("清仓改账: %v", err)
	}
	v, err := db.ManualBookViewFor(uid, today)
	if err != nil {
		t.Fatalf("读手动账: %v", err)
	}
	if len(v.Positions) != 0 {
		t.Fatalf("清仓后不应再有持仓行, got %d", len(v.Positions))
	}
	if !v.PositionsUsable || v.PositionsAsOf != asof {
		t.Fatalf("显式维护的空仓必须可用且带时刻, got usable=%v asof=%q", v.PositionsUsable, v.PositionsAsOf)
	}
}

func TestManualSetCashCanMaintainTotalAsset(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_total_asset"
	today := manualToday()
	asof := manualAsofOn(today)
	if err := db.PutManualAccount(uid, 50000, true, 0, asof); err != nil {
		t.Fatalf("写初始资金: %v", err)
	}
	cash, total := 60000.0, 93500.0
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{
		Op: ManualOpSetCash, Cash: &cash, TotalAsset: &total, AsOf: asof, User: uid,
	}, today); err != nil {
		t.Fatalf("改资金: %v", err)
	}
	v, err := db.ManualBookViewFor(uid, today)
	if err != nil {
		t.Fatalf("读手动账: %v", err)
	}
	if v.Cash.Account.AvailableCash != cash || v.Cash.Account.TotalAsset != total {
		t.Fatalf("可用/总值必须按人填的值落库, got cash=%.2f total=%.2f",
			v.Cash.Account.AvailableCash, v.Cash.Account.TotalAsset)
	}
	// 只改可用资金、不带总资产的那一笔，不许把已填的总资产抹成 0。
	cash2 := 61000.0
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{
		Op: ManualOpSetCash, Cash: &cash2, AsOf: asof, User: uid,
	}, today); err != nil {
		t.Fatalf("只改可用资金: %v", err)
	}
	v2, err := db.ManualBookViewFor(uid, today)
	if err != nil {
		t.Fatalf("复读: %v", err)
	}
	if v2.Cash.Account.TotalAsset != total {
		t.Fatalf("不带 total_asset 的改账应保留旧总值, got %.2f", v2.Cash.Account.TotalAsset)
	}
}
