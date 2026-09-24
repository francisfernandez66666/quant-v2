// minute_sync_test.go — §MINUTE-K 分钟装载器（dataload minute-sync）的定点测试（2026-09-24）。
//
// 真实上游（新浪 5 分钟）只有"最近 5025 根"这一扇窗口、且会随机封 IP，拿它跑出来的红绿
// 不可复现，等于没有测试。所以这里用桩 fetcher 把三件**出门口径**钉死（本仓审计主题：
// 降级不许报成功）：
//  1. 全部取数失败 ⇒ 0 行落库 ⇒ **必须报错**（"跑完了但没有数据"绝不能算成功）；
//  2. 部分失败但失败率超过 --max-fail-pct ⇒ 整轮判失败（那种形态是封 IP/接口改版，不是偶发）；
//  3. 清单为空 ⇒ 直接失败，不去猜"那要不要顺手灌全市场"。
//
// 外加三条口径：主键幂等（同一轮重跑不产生双行）、零价根不落库、上游时间戳按北京时间归一
// （某源给 UTC 时区时，同一天的根会被劈成两半，回放按 ts 前缀切日就少一半根）。
//
// English: pinned tests for the minute loader with a stubbed fetcher — zero rows must fail,
// excessive failure rate must fail, empty universe must fail, upsert stays idempotent,
// zero-price bars are dropped, and timestamps normalize to Beijing wall clock.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// stubFetcher 可编程取数桩：codes 里的票各返回 count 根，missing 里的票返回错误。
type stubFetcher struct {
	count   int
	missing map[string]bool
	calls   []string
}

// GetUnadjustedMinuteKLine 假上游（对应 DataCoordinator 的严格不复权分钟链）：按 missing 表决定
// "这只票取不到"，否则吐 count 根连续 5 分钟线
// （价格递增，便于断言落库顺序；时间从当日 15:00 倒推，覆盖 UTC→北京归一那条用例）。
func (s *stubFetcher) GetUnadjustedMinuteKLine(code string, scale, count int) ([]data.KLine, error) {
	s.calls = append(s.calls, fmt.Sprintf("%s/%d/%d", code, scale, count))
	if s.missing[code] {
		return nil, errors.New("stub: upstream blocked")
	}
	start := time.Date(2026, 9, 24, 15, 0, 0, 0, cstZone)
	out := make([]data.KLine, 0, count)
	for i := 0; i < count; i++ {
		p := 10.0 + float64(i)*0.01
		out = append(out, data.KLine{
			Date: start.Add(-time.Duration(count-1-i) * 5 * time.Minute),
			Open: p, High: p * 1.001, Low: p * 0.999, Close: p, Volume: 1000, Amount: p * 1000,
		})
	}
	return out, nil
}

func newSyncDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func writeCodesFile(t *testing.T, codes ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "codes.txt")
	body := "# 测试清单\n" + strings.Join(codes, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写清单: %v", err)
	}
	return p
}

func stats5(t *testing.T, db *store.DB) store.MinuteStats {
	t.Helper()
	st, err := db.MinuteTableStats(5)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	return st
}

func TestMinuteSyncHappyPathAndIdempotent(t *testing.T) {
	db := newSyncDB(t)
	o := minuteSyncOpts{Scale: 5, Count: 48, CodesFile: writeCodesFile(t, "600000.SH", "000001.SZ"), MaxFailPct: 10}
	dc := &stubFetcher{count: 48}
	if _, err := runMinuteSync(db, dc, o, time.Now()); err != nil {
		t.Fatalf("首轮装载: %v", err)
	}
	st := stats5(t, db)
	if st.Rows != 96 || st.Codes != 2 {
		t.Fatalf("两票各 48 根应为 96 行/2 只，实得 %+v", st)
	}
	if st.AvgBars < 47.9 {
		t.Fatalf("平均每(票,日)应≈48 根，实得 %.2f（回填窗口被上游截断就在这露出来）", st.AvgBars)
	}
	// 同一轮重跑（日增与回填共用 upsert 路径）不得产生双行
	if _, err := runMinuteSync(db, dc, o, time.Now()); err != nil {
		t.Fatalf("重跑: %v", err)
	}
	if st2 := stats5(t, db); st2.Rows != 96 {
		t.Fatalf("重跑后必须仍是 96 行，实得 %d", st2.Rows)
	}
	// 请求参数按 scale/count 原样传给上游（缺省尺寸被改动=口径漂移）
	if dc.calls[0] != "600000.SH/5/48" {
		t.Fatalf("上游调用参数应带 scale=5 count=48，实得 %q", dc.calls[0])
	}
}

func TestMinuteSyncZeroRowsFails(t *testing.T) {
	db := newSyncDB(t)
	o := minuteSyncOpts{Scale: 5, Count: 48, CodesFile: writeCodesFile(t, "600000.SH"), MaxFailPct: 100}
	dc := &stubFetcher{count: 0, missing: map[string]bool{"600000.SH": true}}
	if _, err := runMinuteSync(db, dc, o, time.Now()); err == nil {
		t.Fatalf("全部取数失败、0 行落库时必须报错（降级报成功是本仓反复出事的形态）")
	}
	if st := stats5(t, db); st.Rows != 0 {
		t.Fatalf("失败轮不应有残留行，实得 %d", st.Rows)
	}
}

func TestMinuteSyncFailureRateGate(t *testing.T) {
	db := newSyncDB(t)
	// 3 只里失败 1 只 = 33% > 缺省 10% ⇒ 判失败，但已写入的行不回滚（主键幂等，重跑即补齐）
	o := minuteSyncOpts{Scale: 5, Count: 48, MaxFailPct: 10,
		CodesFile: writeCodesFile(t, "600000.SH", "000001.SZ", "000002.SZ")}
	dc := &stubFetcher{count: 48, missing: map[string]bool{"000002.SZ": true}}
	if _, err := runMinuteSync(db, dc, o, time.Now()); err == nil {
		t.Fatalf("失败率 33%% 超过上限 10%% 时必须判整轮失败")
	}
	if st := stats5(t, db); st.Rows != 96 {
		t.Fatalf("判失败不得把已成功的那部分抹掉（幂等重跑要能续上），实得 %d 行", st.Rows)
	}
	// 同样失败率，把上限抬到 50% 就算通过（阈值确实是可配的闸门而不是硬编码的运气）
	db2 := newSyncDB(t)
	o.MaxFailPct = 50
	if _, err := runMinuteSync(db2, dc, o, time.Now()); err != nil {
		t.Fatalf("上限 50%% 时同一轮应通过：%v", err)
	}
}

func TestMinuteSyncEmptyUniverseFails(t *testing.T) {
	db := newSyncDB(t)
	// 回填模式 + 空池（全新库没跑池同步）：必须报错，绝不静默改成全市场
	o := minuteSyncOpts{Scale: 5, Count: 48, Since: "20260624", Limit: 500, MaxFailPct: 10}
	if _, err := runMinuteSync(db, &stubFetcher{count: 48}, o, time.Now()); err == nil {
		t.Fatalf("空清单必须失败退出（悄悄灌全市场=2GB 写入藏在例行任务里）")
	}
	// 日增模式在空库上同样失败（清单自维护，没有存量就没有清单）
	o.Incremental = true
	if _, err := runMinuteSync(db, &stubFetcher{count: 48}, o, time.Now()); err == nil {
		t.Fatalf("日增模式在空库上必须失败")
	}
}

// TestMinuteSyncIncrementalUsesStoredCodes 日增模式的清单必须来自库里已有代码（自维护），
// 而不是重新按池算一遍——否则回填当天之外的新面孔会被漏掉，且池表为空时整轮直接失败。
func TestMinuteSyncIncrementalUsesStoredCodes(t *testing.T) {
	db := newSyncDB(t)
	seed := func(codes []string) {
		for _, c := range codes {
			bars := make([]store.MinuteBar, 0, 3)
			for i := 0; i < 3; i++ {
				bars = append(bars, store.MinuteBar{TsCode: c, Scale: 5,
					Ts: fmt.Sprintf("2026-09-23 %02d:%02d:00", 9+((35+5*i)/60), (35+5*i)%60), Close: 10})
			}
			if _, err := db.UpsertMinuteBars(bars); err != nil {
				t.Fatalf("seed %s: %v", c, err)
			}
		}
	}
	seed([]string{"600000.SH", "000001.SZ"})
	o := minuteSyncOpts{Scale: 5, Count: 48, Incremental: true, MaxFailPct: 10}
	dc := &stubFetcher{count: 48}
	if _, err := runMinuteSync(db, dc, o, time.Now()); err != nil {
		t.Fatalf("日增: %v", err)
	}
	joined := strings.Join(dc.calls, ",")
	if !strings.Contains(joined, "600000.SH/5/48") || !strings.Contains(joined, "000001.SZ/5/48") {
		t.Fatalf("日增清单必须是库里已有的票，实得 %q", joined)
	}
	if strings.Contains(joined, "000002.SZ") {
		t.Fatalf("日增不得把没回填过的票拉进来（清单自维护）：%q", joined)
	}
}

func TestMinuteSyncZeroPriceBarsDropped(t *testing.T) {
	kls := []data.KLine{
		{Date: time.Date(2026, 9, 24, 9, 35, 0, 0, cstZone), Close: 10},
		{Date: time.Date(2026, 9, 24, 9, 40, 0, 0, cstZone), Close: 0}, // 停牌/占位根
		{Date: time.Date(2026, 9, 24, 9, 45, 0, 0, cstZone), Close: -1},
		{Date: time.Date(2026, 9, 24, 9, 50, 0, 0, cstZone), Close: 11},
	}
	bars := toMinuteBars("600000.SH", 5, kls)
	if len(bars) != 2 {
		t.Fatalf("零价/负价根必须丢弃（喂进 EMA 会出假跳水），实得 %d 根", len(bars))
	}
	if bars[0].Ts != "2026-09-24 09:35:00" || bars[1].Ts != "2026-09-24 09:50:00" {
		t.Fatalf("ts 格式或顺序不对：%s / %s", bars[0].Ts, bars[1].Ts)
	}
}

// TestMinuteSyncUTCSourceNormalizedToBeijing 钉"某源返回 UTC 时区时间"这一手：
// 不折算就会把 09:35 那根写成 "2026-09-24 01:35"，同一天的 48 根被劈成两段，
// 回放按 ts 前缀切日只拿到一半，MACD 口径静默改变。
func TestMinuteSyncUTCSourceNormalizedToBeijing(t *testing.T) {
	utc := time.Date(2026, 9, 24, 1, 35, 0, 0, time.UTC) // = 北京 09:35
	bars := toMinuteBars("600000.SH", 5, []data.KLine{{Date: utc, Close: 10}})
	if len(bars) != 1 {
		t.Fatalf("应写入 1 根，实得 %d", len(bars))
	}
	if bars[0].Ts != "2026-09-24 09:35:00" {
		t.Fatalf("UTC 时间必须折算成北京时间墙钟，实得 %q", bars[0].Ts)
	}
	// 北京时间**开盘前 8 小时内**的时刻，用 UTC 表达会落到前一天：折算后必须回到当日
	prevEvening := time.Date(2026, 9, 24, 1, 30, 0, 0, time.UTC) // = 北京 2026-09-24 09:30
	b2 := toMinuteBars("600000.SH", 5, []data.KLine{{Date: prevEvening, Close: 10}})
	if b2[0].Ts != "2026-09-24 09:30:00" {
		t.Fatalf("09:30 那根不得被写进 09-23 的切片，实得 %q", b2[0].Ts)
	}
}

func TestParseMinuteFlagsDefaultsAndOverrides(t *testing.T) {
	o, err := parseMinuteFlags(nil)
	if err != nil {
		t.Fatalf("缺省解析: %v", err)
	}
	if o.Scale != 5 || o.Count != 5025 || o.Limit != 500 || o.MaxFailPct != 10 {
		t.Fatalf("缺省值应锁定本次裁决的口径（5 分钟/上游封顶窗口/500 只/10%% 失败率），实得 %+v", o)
	}
	if o.Incremental || o.CodesFile != "" || o.Since != "" {
		t.Fatalf("缺省必须是回填模式且不指定清单：%+v", o)
	}
	o2, err := parseMinuteFlags([]string{"--incremental", "--count", "60", "--limit", "50", "--codes", "/tmp/x.txt", "--max-fail-pct", "0"})
	if err != nil {
		t.Fatalf("覆盖解析: %v", err)
	}
	if !o2.Incremental || o2.Count != 60 || o2.Limit != 50 || o2.CodesFile != "/tmp/x.txt" || o2.MaxFailPct != 0 {
		t.Fatalf("覆盖未生效：%+v", o2)
	}
	if _, err := parseMinuteFlags([]string{"--nope"}); err == nil {
		t.Fatalf("未知参数必须报错（拼错开关不能静默按缺省跑）")
	}
}
