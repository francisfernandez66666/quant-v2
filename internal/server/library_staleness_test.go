// library_staleness_test.go — §0929HB-3（2026-09-29 全量审计批 ⑪-4）「战法库陈旧」心跳用例。
//
// 为什么这条不写成"条数变了就告警"（设计取舍，详见 library_staleness.go 文件头）：
//
//	夜间寻优+审批每晚正常应用都会让条数变化，按"变了"告警＝每天一条固定噪音；
//	真正没人报的是**反向失效**——研究链整段停摆，库停在两周前那一版，引擎照常按旧规则出信号、
//	外部三条监控全绿。变更那一半由 §0929LIB-WATCH 的热加载 + opslog.Audit 留痕负责。
//
// 本文件的四道锁：
//  1. 纯函数判据（取较近 mtime / 两库都缺不判 / 未来 mtime 不倒挂）；
//  2. 真目录 mtime 端到端（陈旧天数按真实文件算，不靠注入假时间自证）；
//  3. 两库都缺 ⇒ 量规写 0（成因分家：库里没规则由 p1 no_enabled 那条负责）；
//  4. 规则登记面等值锁 + 路由归属（陈旧是趋势型，走日汇总不走必推，否则每 30 分钟刷一条）。
//
// English: §0929HB-3 staleness tests — pure predicate, real-directory mtime end-to-end,
// both-absent writes 0 (that cause belongs to the no-enabled p1), and equality locks on the
// registered rule plus its daily-summary routing.
package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/metrics"
)

// hbLibNow 固定"今天"，避免用例结果随运行时刻漂移。
var hbLibNow = time.Date(2026, 9, 29, 16, 0, 0, 0, cntime.Loc)

// TestLibraryStalenessDaysPredicate 纯函数判据四支。
func TestLibraryStalenessDaysPredicate(t *testing.T) {
	old := hbLibNow.AddDate(0, 0, -20)
	recent := hbLibNow.AddDate(0, 0, -3)
	cases := []struct {
		name string
		a    time.Time
		okA  bool
		b    time.Time
		okB  bool
		want int64
	}{
		{"两库都缺不判陈旧", time.Time{}, false, time.Time{}, false, 0},
		{"只有一份存在按那份算", recent, true, time.Time{}, false, 3},
		{"取较近的一侧", old, true, recent, true, 3},
		{"另一侧较近", recent, true, old, true, 3},
		{"20 天前＝陈旧", old, true, time.Time{}, false, 20},
		{"未来 mtime 不倒挂", hbLibNow.AddDate(0, 0, 5), true, time.Time{}, false, 0},
	}
	for _, c := range cases {
		if got := libraryStalenessDays(hbLibNow, c.a, c.okA, c.b, c.okB); got != c.want {
			t.Errorf("%s: libraryStalenessDays=%d 期望 %d", c.name, got, c.want)
		}
	}
}

// hbWriteLib 在临时研究目录里落一份库文件并按 wantAge 设 mtime。
func hbWriteLib(t *testing.T, dir, name string, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("{}"), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	mt := hbLibNow.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
}

// TestRefreshLibraryStalenessGaugeEndToEnd 真目录读数：一新一旧必须取较近的 3 天，
// 两库都缺必须写 0（不得伪造陈旧）。
func TestRefreshLibraryStalenessGaugeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	s := &Server{}
	s.researchDir = dir

	// 两支都以 hbLibNow 为"今天"，mtime 用 Chtimes 真落到文件系统上（不注入假读数）。
	hbWriteLib(t, dir, "applied_factors.json", 20*24*time.Hour)
	s.refreshLibraryStalenessGauge(hbLibNow)
	if v := mustLibGauge(t); v != 20 {
		t.Fatalf("单库 20 天前应为 20，got %d", v)
	}
	hbWriteLib(t, dir, "applied_patterns.json", 3*24*time.Hour)
	s.refreshLibraryStalenessGauge(hbLibNow)
	if v := mustLibGauge(t); v != 3 {
		t.Fatalf("另一侧 3 天前更新 ⇒ 必须取较近的 3，got %d", v)
	}

	// 两库都缺：量规归 0（该成因由 p1 no_enabled 那条说，这里不重复判红）。
	dir2 := t.TempDir()
	s2 := &Server{}
	s2.researchDir = dir2
	s2.refreshLibraryStalenessGauge(hbLibNow)
	if v := mustLibGauge(t); v != 0 {
		t.Fatalf("两库都缺必须写 0（不伪造陈旧），got %d", v)
	}
	// 未接研究目录：不落笔（否则会把上一个引擎的读数抹成 0，属掩蔽形态）。
	metrics.SetGauge("library_stale_days", 7)
	s3 := &Server{}
	s3.refreshLibraryStalenessGauge(hbLibNow)
	if v, _ := metrics.GetGauge("library_stale_days"); v != 7 {
		t.Fatalf("researchDir 为空时不得改写量规，got %d", v)
	}
}

// TestLibraryStaleRuleRegisteredAndRouted 规则登记面 + 路由归属等值锁。
// 路由必须是 RouteDaily：这条破线后会持续成立，走必推就是每 30 分钟一条刷屏
// （§CAL-GATE 的日汇总纪律，同一条理由）。
func TestLibraryStaleRuleRegisteredAndRouted(t *testing.T) {
	var hits int
	var r metrics.AlertRule
	for _, one := range metrics.DefaultAlertRules() {
		if one.Name == "library_stale_days" {
			r = one
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("规则 library_stale_days 命中 %d 条（应为 1）", hits)
	}
	if r.Metric != "library_stale_days" {
		t.Fatalf("规则读的键 %q ≠ 写端落的键 %q（§DEADGAUGE）", r.Metric, "library_stale_days")
	}
	if r.Level != "p2" || r.Op != "gt" || int(r.Threshold) != libraryStaleThresholdDays || r.For != "0s" {
		t.Fatalf("规则口径漂移：level=%s op=%s threshold=%.0f for=%s（期望 p2/gt/%d/0s）",
			r.Level, r.Op, r.Threshold, r.For, libraryStaleThresholdDays)
	}
	routes := metrics.DefaultAlertRouting()
	if got := routes.Routes["library_stale_days"]; got != metrics.RouteDaily {
		t.Fatalf("陈旧型心跳路由应为 %q（日汇总），got %q", metrics.RouteDaily, got)
	}
	// 另两条缺失型心跳必须走必推（今天就要处理），与这条的趋势型严格分开。
	if routes.Routes["signal_zero_in_session"] != metrics.RoutePush {
		t.Fatalf("signal_zero_in_session 必须 RoutePush（收盘后才知道今天没信号＝白过一天）")
	}
	if routes.Routes["realized_pnl_zero_with_sells"] != metrics.RoutePush {
		t.Fatalf("realized_pnl_zero_with_sells 必须 RoutePush（亏损熔断闸当天放水）")
	}
}

// mustLibGauge 读陈旧量规（从未被写过即判红：接线比数值更要紧）。
func mustLibGauge(t *testing.T) int64 {
	t.Helper()
	v, ok := metrics.GetGauge("library_stale_days")
	if !ok {
		t.Fatalf("量规 %s 从未被写过（喂数点没接上）", "library_stale_days")
	}
	return v
}
