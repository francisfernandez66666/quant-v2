// §ADJ(P0-A 20260922) 后复权因子前向填充黄金样本：
// adj_factor 是【事件稀疏点】表（dataload 按分红实施日写入，见 cmd/dataload/baostock.go
// bsLoadStockTables 的 adjRows 构造处），
// HfqBars 必须"取不晚于该交易日的最近一个因子"（前向填充），
// 等值 JOIN 会让非除权日因子落空、后复权退化为不复权。
// 本文件用一只票一年内的两次除权（1.0 → 1.5 → 2.0）把这条语义钉死。
// （Golden sample: sparse event factors must be forward-filled; equality-join would degrade
// back-adjusted prices into raw prices on every non-event day.）
package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHfqBarsAdjForwardFill 黄金样本：合成 40 个交易日、年内两次除权，
// 逐日断言生效因子：除权日**之前**保持旧因子（不得反向填充）、除权日**及其次日之后**
// 取新因子；事件之前的日期按 1.0 兜底。
func TestHfqBarsAdjForwardFill(t *testing.T) {
	db := testDB(t)

	// 造数：40 个连续日历日当交易日用（本用例只关心因子取数，不关心周末）。
	// 全部收盘价固定 10.0 → HfqBars 返回的 close 直接等于 10×因子，断言直观。
	dates := make([]string, 40)
	rows := make([]map[string]any, 0, len(dates))
	base := time.Date(2025, 3, 3, 0, 0, 0, 0, time.UTC)
	for i := range dates {
		dates[i] = base.AddDate(0, 0, i).Format("20060102")
		rows = append(rows, map[string]any{
			"ts_code": "600000.SH", "trade_date": dates[i],
			"open": 10.0, "high": 10.0, "low": 10.0, "close": 10.0, "vol": 100.0, "amount": 1000.0,
		})
	}
	if _, err := db.InsertRows("daily", TableColumns("daily"), rows); err != nil {
		t.Fatalf("InsertRows daily: %v", err)
	}

	// 事件稀疏点：只在两个除权日各写一行因子（1.0 是隐含基线，不写行）。
	const ev1, ev2 = 10, 25 // dates[10] 起因子 1.5；dates[25] 起因子 2.0
	if _, err := db.InsertRows("adj_factor", TableColumns("adj_factor"), []map[string]any{
		{"ts_code": "600000.SH", "trade_date": dates[ev1], "adj_factor": 1.5},
		{"ts_code": "600000.SH", "trade_date": dates[ev2], "adj_factor": 2.0},
	}); err != nil {
		t.Fatalf("InsertRows adj_factor: %v", err)
	}

	bars, err := db.HfqBars("600000.SH", dates[0], dates[len(dates)-1])
	if err != nil {
		t.Fatalf("HfqBars: %v", err)
	}
	if len(bars) != len(dates) {
		t.Fatalf("HfqBars 条数=%d，期望 %d", len(bars), len(dates))
	}

	// 期望因子序列：事件日之前=旧值，事件日当天起=新值（与 LegacyAdjFactorAt 的 ≤date 语义一致）。
	wantAdj := func(i int) float64 {
		switch {
		case i >= ev2:
			return 2.0
		case i >= ev1:
			return 1.5
		default:
			return 1.0
		}
	}
	for i, b := range bars {
		want := 10.0 * wantAdj(i)
		if b.Close != want {
			t.Fatalf("第 %d 个交易日(%s) hfq_close=%.4f，期望 %.4f（因子 %.1f）——"+
				"因子未按【不晚于该日的最近事件日】前向填充", i, b.Date, b.Close, want, wantAdj(i))
		}
	}

	// 关键锚点单独断言，避免上表的循环口径把三种错误都掩盖：
	// ① 除权日次日及其后所有交易日必须为新因子（等值 JOIN 的老缺陷正是这里退化成 1.0）；
	// ② 除权日之前仍为旧因子（不得反向填充，否则历史价格被未来事件污染）；
	// ③ 首个事件之前按基线 1.0 兜底。
	if bars[ev1+1].Close != 15.0 {
		t.Fatalf("除权日次日 adj 必须=1.5（实际 hfq_close=%.2f）：等值 JOIN 会让这里退化为不复权", bars[ev1+1].Close)
	}
	if bars[len(bars)-1].Close != 20.0 {
		t.Fatalf("末交易日 adj 必须延续为 2.0（实际 %.2f）：前向填充不得在事件后回落", bars[len(bars)-1].Close)
	}
	if bars[ev1-1].Close != 10.0 {
		t.Fatalf("除权日前一日必须仍是旧因子 1.0（实际 %.2f）：禁止反向填充", bars[ev1-1].Close)
	}
	if bars[ev2-1].Close != 15.0 {
		t.Fatalf("第二次除权前一日必须仍是 1.5（实际 %.2f）", bars[ev2-1].Close)
	}

	// 语义对齐锁：HfqBars 的逐日因子必须与同包 LegacyAdjFactorAt（既有正确实现）完全一致，
	// 两处实现漂移即红（LegacyAdjFactorAt 无行时返回 ok=false，对应基线 1.0）。
	for i, b := range bars {
		f, ok, err := db.LegacyAdjFactorAt("600000.SH", b.Date)
		if err != nil {
			t.Fatalf("LegacyAdjFactorAt(%s): %v", b.Date, err)
		}
		if !ok {
			f = 1.0
		}
		if b.Close != 10.0*f {
			t.Fatalf("第 %d 日 HfqBars(%.2f) 与 LegacyAdjFactorAt(%.2f) 语义不一致", i, b.Close, f)
		}
	}
}

// TestHfqBarsAdjNoEventStock 全年无除权事件的标的：所有交易日因子都按 1.0 兜底，
// 且行数不丢（前向填充子查询在无行时不得过滤掉行情）。
func TestHfqBarsAdjNoEventStock(t *testing.T) {
	db := testDB(t)
	base := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)
	var rows []map[string]any
	for i := 0; i < 5; i++ {
		rows = append(rows, map[string]any{
			"ts_code": "000900.SZ", "trade_date": base.AddDate(0, 0, i).Format("20060102"),
			"open": 5.0, "high": 5.0, "low": 5.0, "close": 5.0, "vol": 1.0, "amount": 5.0,
		})
	}
	if _, err := db.InsertRows("daily", TableColumns("daily"), rows); err != nil {
		t.Fatalf("insert daily: %v", err)
	}
	bars, err := db.HfqBars("000900.SZ", "20250601", "20250610")
	if err != nil {
		t.Fatalf("HfqBars: %v", err)
	}
	if len(bars) != 5 {
		t.Fatalf("无因子标的应返回全部 5 根，实际 %d", len(bars))
	}
	for _, b := range bars {
		if b.Close != 5.0 {
			t.Fatalf("无事件标的 hfq 应等于原始价，实际 %.2f", b.Close)
		}
	}
}

// TestConfigureSourceSingleEntry 装配收口锁（§P0-A 三轮补强·范围盲区）：
// 路由只能经唯一入口 ConfigureSource / ConfigureSourceFromFile 装配，
// "hithink" 的大小写/空白容错也只在该入口实现一次；CurrentSource 用于各 main 自证生效路径一致。
// 用例结束必须恢复出厂默认，避免包内其他用例串味。
func TestConfigureSourceSingleEntry(t *testing.T) {
	defer ConfigureSource("", false) // 恢复出厂默认（旧表 + 门禁关）

	ConfigureSource("HiThink ", false) // 大小写 + 空白容错：入口内部统一 EqualFold(TrimSpace)
	if ths, ready := CurrentSource(); !ths || ready {
		t.Fatalf("primary_source=\"HiThink \" 应装配为 ths_daily=true/门禁=false，实际 %v/%v", ths, ready)
	}
	ConfigureSource("baostock", true)
	if ths, ready := CurrentSource(); ths || !ready {
		t.Fatalf("primary_source=baostock 应为 ths_daily=false（门禁独立），实际 %v/%v", ths, ready)
	}

	// 从 config.json 读 rules.data：与 internal/config.DataConfig 同键，验证下沉入口本身可用
	// （各 main 拿不到配置对象时走的就是这一条）。
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"rules":{"data":{"primary_source":"hithink","ths_factors_ready":true}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := ConfigureSourceFromFile(path); err != nil {
		t.Fatalf("ConfigureSourceFromFile: %v", err)
	}
	if ths, ready := CurrentSource(); !ths || !ready {
		t.Fatalf("应从 config.json 装配为 ths_daily=true/门禁=true，实际 %v/%v", ths, ready)
	}

	// 配置缺失：显式回落出厂默认并返回 error（绝不沿用上一进程的残留态）。
	if err := ConfigureSourceFromFile(filepath.Join(t.TempDir(), "not-exist.json")); err == nil {
		t.Fatal("配置文件缺失时应返回 error 并回落默认")
	}
	if ths, ready := CurrentSource(); ths || ready {
		t.Fatalf("缺省装配应为旧表 + 门禁关，实际 %v/%v", ths, ready)
	}
}
