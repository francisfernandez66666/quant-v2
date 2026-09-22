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
	"time"

	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategy_engine"
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

// §M-7 新鲜度停用闸：报告期滞后超 finaStaleMaxDays 的旧财报按缺失计入。
// 缺陷本体：§N-5 只把 end_date/ann_date 带了出来，但带出来 ≠ 用得上——研究库断更时
// 打分仍照用半年前的财报计分，闸门缺位。以下测试钉死四态。
// English: §M-7 stale-report refusal gate — carrying the dates (§N-5) is useless unless the
// consumer refuses them; pins the four verdict states.

func TestFinaReportStaleThresholds(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		f        *strategy_engine.FinancialData
		wantOld  bool
		wantAsof string
	}{
		// ann_date 优先：end 20260630 距 now 84 天（新），但 ann 20250506 滞后 504 天 → 过旧
		{"ann优先判旧", &strategy_engine.FinancialData{EndDate: "20260630", AnnDate: "20250506"}, true, "20250506"},
		{"ann缺失退回end", &strategy_engine.FinancialData{EndDate: "20250331"}, true, "20250331"},
		{"两者皆缺=不可知放行", &strategy_engine.FinancialData{}, false, ""},
		{"日期非法=不可知放行", &strategy_engine.FinancialData{EndDate: "2026-06-30", AnnDate: "null"}, false, ""},
		{"临界内放行", &strategy_engine.FinancialData{EndDate: "20260331", AnnDate: "20260428"}, false, "20260428"},
		{"nil放行", nil, false, ""},
	}
	for _, tc := range cases {
		old, asof := finaReportStale(tc.f, now)
		if old != tc.wantOld || asof != tc.wantAsof {
			t.Errorf("%s: got (%v,%q) want (%v,%q)", tc.name, old, asof, tc.wantOld, tc.wantAsof)
		}
	}
	// 阈值本身钉死：恰好 finaStaleMaxDays 当天不算旧（严格大于才触发）。
	// 基准取零点：finaReportStale 里日期按 YYYYMMDD 解析为 UTC 零点，带时分秒的 now 会虚增半天。
	nowDay := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	boundary := nowDay.Add(-finaStaleMaxDays * 24 * time.Hour)
	f := &strategy_engine.FinancialData{EndDate: boundary.Format("20060102")}
	if old, _ := finaReportStale(f, nowDay); old {
		t.Errorf("临界当天不应判旧")
	}
	f2 := &strategy_engine.FinancialData{EndDate: nowDay.Add(-(finaStaleMaxDays + 1) * 24 * time.Hour).Format("20060102")}
	if old, _ := finaReportStale(f2, nowDay); !old {
		t.Errorf("超阈值一天应判旧")
	}
}

func TestFinaCacheRefusesStaleReport(t *testing.T) {
	// 库里只有 2025 年中的旧财报（滞后 >240 天）：Lookup 必须返回 nil（按缺失计入打分），
	// 且该 nil 会写入缓存——不能每 5s 重查重报。
	db := openFinaDB(t, []map[string]any{
		{"ts_code": "600000.SH", "end_date": "20250630", "ann_date": "20250828", "roe": 8.8},
	})
	c := newFinaCache(db)
	if got := c.Lookup("600000.SH"); got != nil {
		t.Fatalf("§M-7 过旧财报必须停用（返回 nil），got %+v", got)
	}
	c.mu.Lock()
	_, cached := c.cache["600000.SH"]
	c.mu.Unlock()
	if !cached {
		t.Fatalf("停用结果应按缺失写缓存（避免 5s 循环重查）")
	}
	// 新股不受牵连：同库另存一只新鲜的票
	if _, err := db.InsertRows("fina_indicator", store.TableColumns("fina_indicator"), []map[string]any{
		{"ts_code": "000001.SZ", "end_date": "20260630", "ann_date": "20260825", "roe": 3.3},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got := c.Lookup("000001"); got == nil || got.Roe != 3.3 {
		t.Fatalf("新鲜财报不应被闸误伤，got %+v", got)
	}
}

func TestFinaCacheUnknownDatesPass(t *testing.T) {
	// 日期皆缺（旧装载行）：不可知 ≠ 过旧，必须放行——否则历史数据装载路径整体失能。
	db := openFinaDB(t, []map[string]any{
		{"ts_code": "002594.SZ", "end_date": "", "roe": 7.7},
	})
	got := newFinaCache(db).Lookup("002594.SZ")
	if got == nil || got.Roe != 7.7 {
		t.Fatalf("日期不可知应按放行处理（§M-7 不误伤），got %+v", got)
	}
}
