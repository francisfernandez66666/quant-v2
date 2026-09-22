// fina_cache_test.go — §N-5（2026-09-22 PM 批）财务因子必须带出报告期/披露日。
//
// 缺陷本体：strategy_engine.FinancialData 此前只有七个指标值，**根本没有报告期字段**——
// 运行侧打分拿到的是"库里最后一行"，却无从判断它是上季度还是半年前的存量（研究库断更时
// 照用旧财报计分且无人知晓，研究侧有 PIT 装配、运行侧没有对应闸门）。本测试把
// fina_indicator 的 end_date/ann_date 透传钉死，作为 §M-7 新鲜度闸的前置条件。
// English: §N-5 — FinancialData must carry end_date/ann_date out of fina_indicator; without them the
// live scorer cannot tell how stale the row it just used is (prerequisite for the §M7 freshness gate).
package main

import (
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/store"
)

// openFinaDB 建一个只装财务表的研究库，写入指定报告期行。
func openFinaDB(t *testing.T, rows []map[string]any) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatalf("open research db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.InsertRows("fina_indicator", store.TableColumns("fina_indicator"), rows); err != nil {
		t.Fatalf("insert fina_indicator: %v", err)
	}
	return db
}

func TestFinaCacheCarriesReportPeriod(t *testing.T) {
	db := openFinaDB(t, []map[string]any{
		{"ts_code": "600000.SH", "end_date": "20251231", "ann_date": "20260310", "roe": 9.1},
		{"ts_code": "600000.SH", "end_date": "20260331", "ann_date": "20260428", "roe": 2.4, "yoy_net_profit": 11.0},
	})
	c := newFinaCache(db)
	got := c.Lookup("600000") // 6 位代码归一化路径同锁
	if got == nil {
		t.Fatalf("Lookup 返回 nil，财务因子链未接通")
	}
	if got.EndDate != "20260331" || got.AnnDate != "20260428" {
		t.Fatalf("§N-5 报告期/披露日未透传：end=%q ann=%q（期望 20260331/20260428）", got.EndDate, got.AnnDate)
	}
	if got.Roe != 2.4 {
		t.Fatalf("应取最新报告期一行：roe=%.2f", got.Roe)
	}
}

func TestFinaCacheMissingDatesAreUnknownNotZero(t *testing.T) {
	// ann_date 缺失（旧装载行）：字段留空串=「不可知」，下游新鲜度闸按不可知处理，
	// 绝不能伪造成一个日期把"不可知"伪装成"很新"或"很旧"。
	db := openFinaDB(t, []map[string]any{
		{"ts_code": "000001.SZ", "end_date": "20260331", "roe": 3.3},
	})
	got := newFinaCache(db).Lookup("000001.SZ")
	if got == nil {
		t.Fatalf("Lookup 返回 nil")
	}
	if got.EndDate != "20260331" {
		t.Fatalf("end_date 应透传：got %q", got.EndDate)
	}
	if got.AnnDate != "" {
		t.Fatalf("披露日缺失必须是空串（不可知），got %q", got.AnnDate)
	}
}

func TestFinaCacheTTLKeepsSameEntry(t *testing.T) {
	// TTL 命中路径同样必须带着报告期字段（缓存条目整体复用，不能只在回源分支赋值）。
	db := openFinaDB(t, []map[string]any{
		{"ts_code": "300750.SZ", "end_date": "20260630", "ann_date": "20260720", "roe": 5.5},
	})
	c := newFinaCache(db)
	first := c.Lookup("300750")
	second := c.Lookup("300750.SZ")
	if first == nil || second == nil || first.EndDate != second.EndDate || second.AnnDate != "20260720" {
		t.Fatalf("缓存命中路径丢失报告期：first=%+v second=%+v", first, second)
	}
}
