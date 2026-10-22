// auto_sell_all_sources_1022_test.go — §B7 全卖出自动化锁。
//
// 开关 false（默认）：战法自带止盈/减仓只提醒（既有语义）。
// 开关 true：战法自带止盈自动全平、战法自带减仓自动半平整手；同日重复触发不重复卖。
//
// English: §B7 locks — switch off keeps strategy-native TP/trim reminder-only; switch on makes
// them auto-execute, with same-day duplicate triggers not selling twice.
package engine

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

func seedSellPos(t *testing.T, db *store.DB) {
	t.Helper()
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAutoSellAllSourcesOffKeepsLegacy(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = true })
	seedSellPos(t, db)
	e.autoExecuteRealSells(e.UserID(), e.QMTController(), db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止盈", Level: "高", RefPrice: 12, Reason: "战法止盈"},
	})
	if len(*orders) != 0 {
		t.Fatalf("默认关闭时战法自带止盈不得自动卖, got %d", len(*orders))
	}
}

func TestAutoSellAllSourcesOnExecutesTPAndTrim(t *testing.T) {
	all := true
	eng, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.AutoSell = true
		c.AutoSellAllSources = &all
	})
	seedSellPos(t, db)
	ctrl := eng.QMTController()

	// 战法自带止盈 → 自动全平 500。
	eng.autoExecuteRealSells(eng.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止盈", Level: "高", RefPrice: 12, Reason: "战法止盈"},
	})
	if len(*orders) != 1 || (*orders)[0]["qty"].(float64) != 500 {
		t.Fatalf("战法自带止盈应自动全平 500, got %+v", *orders)
	}
	// 同日重复触发 → 在途/已卖量已占满，不再下单。
	eng.autoExecuteRealSells(eng.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止盈", Level: "高", RefPrice: 12, Reason: "战法止盈重放"},
	})
	if len(*orders) != 1 {
		t.Fatalf("同日止盈重放不得重复卖, got %d", len(*orders))
	}

	// 另一只票验证战法自带减仓 → 半平整手 200。
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600001.SH", Name: "华夏", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	eng.autoExecuteRealSells(eng.UserID(), ctrl, db, []trading.PositionAdvice{
		{Code: "600001", TsCode: "600001.SH", Action: "减仓", Level: "中", RefPrice: 9, Reason: "战法减仓"},
	})
	if len(*orders) != 2 || (*orders)[1]["qty"].(float64) != 200 {
		t.Fatalf("战法自带减仓应半平整手 200, got %+v", *orders)
	}
}
