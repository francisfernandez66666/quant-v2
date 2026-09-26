// w4e_streamlog_rotation_test.go — §0926E2E-17B 按日轮转单测：跨北京日首拍把旧内容改名归档、
// 活动文件清空重写、超保留期归档惰性清理、不可解析名的外来文件永不删除、同日多拍绝不轮转。
// 时钟经 nowFn 注入（与生产 time.Now 同一读取缝），日期全部取真实"昨天/今天"，避免与机器时钟打架。
// English: §0926E2E-17B daily-rotation unit tests — first write of a new Beijing day archives the
// old file, the live file starts fresh, >30d archives are pruned lazily, unparseable names are
// never deleted, and same-day writes never rotate. Clock is injected via nowFn.
package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w4eBeijingDate 返回 n 天前的北京日历日 YYYYMMDD 与落在该北京日内的一个绝对时刻（nowFn 用）。
// 取北京当日 12:00 对应的 UTC 瞬时（北京=UTC+8），任何注入时钟经 cntime 换算都稳定落在此日内。
func w4eBeijingDate(t *testing.T, offsetDays int) (string, time.Time) {
	t.Helper()
	now := time.Now()
	anchor := time.Date(now.Year(), now.Month(), now.Day(), 4, 0, 0, 0, time.UTC).AddDate(0, 0, offsetDays)
	dayStr := beijingDayOf(anchor)
	if len(dayStr) != 8 {
		t.Fatalf("北京日格式化异常: %q", dayStr)
	}
	return dayStr, anchor
}

func w4eSnap(code string) *MarketSnapshot {
	return &MarketSnapshot{
		Stocks: map[string]*StockInfo{code: {Code: code, Name: code, Price: 10.5}},
	}
}

// TestW4ERotateOnNewBeijingDay 跨日首拍：旧内容归档为 quote_stream-<昨日>.jsonl，
// 活动文件只剩今日新帧；LoadStreamLog 对归档文件直接可用。
func TestW4ERotateOnNewBeijingDay(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "quote_stream.jsonl")
	yday, ynoons := w4eBeijingDate(t, -1)
	tday, tnoons := w4eBeijingDate(t, 0)

	sl, err := NewStreamLog(p)
	if err != nil {
		t.Fatalf("NewStreamLog: %v", err)
	}
	// 注入"昨日"时钟并覆写所属日（等价于昨日启动的长驻进程）。
	sl.nowFn = func() time.Time { return ynoons }
	sl.day = yday
	if err := sl.Write(w4eSnap("600001")); err != nil {
		t.Fatalf("write day1: %v", err)
	}
	if err := sl.Write(w4eSnap("600002")); err != nil {
		t.Fatalf("write day1-2: %v", err)
	}
	// 切到"今日"：下一拍必须触发轮转。
	sl.nowFn = func() time.Time { return tnoons }
	if err := sl.Write(w4eSnap("000001")); err != nil {
		t.Fatalf("write day2: %v", err)
	}
	if sl.day != tday {
		t.Fatalf("轮转后活动日应为 %s，得到 %s", tday, sl.day)
	}
	if err := sl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	archive := filepath.Join(dir, "quote_stream-"+yday+".jsonl")
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("归档文件未生成 %s: %v", filepath.Base(archive), err)
	}
	arch, _ := LoadStreamLog(archive)
	if len(arch) != 2 {
		t.Fatalf("归档应含昨日 2 帧，得到 %d", len(arch))
	}
	today, _ := LoadStreamLog(p)
	if len(today) != 1 {
		t.Fatalf("活动文件应只剩今日 1 帧（旧内容不得混入），得到 %d", len(today))
	}
}

// TestW4ENoRotationSameDay 反证：同日多拍（含跨 6 拍 flush 节奏）绝不产生归档文件。
func TestW4ENoRotationSameDay(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "quote_stream.jsonl")
	tday, noon := w4eBeijingDate(t, 0)
	sl, err := NewStreamLog(p)
	if err != nil {
		t.Fatalf("NewStreamLog: %v", err)
	}
	sl.nowFn = func() time.Time { return noon }
	sl.day = tday
	for i := 0; i < 20; i++ {
		if err := sl.Write(w4eSnap("60000" + string(rune('0'+i%10)))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := sl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "quote_stream-*.jsonl"))
	if len(matches) != 0 {
		t.Fatalf("同日写入不得产生归档，得到 %v", matches)
	}
	got, _ := LoadStreamLog(p)
	if len(got) != 20 {
		t.Fatalf("同日 20 帧应全在活动文件，得到 %d", len(got))
	}
}

// TestW4EPruneKeepsUnparseableAndRecent 保留策略：轮转时删除早于 30 天窗口的可解析归档，
// 今日/昨日归档与不可解析名的外来文件一律保留。
func TestW4EPruneKeepsUnparseableAndRecent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "quote_stream.jsonl")
	oldDay := beijingDayOf(time.Now().AddDate(0, 0, -40))
	recentDay := beijingDayOf(time.Now().AddDate(0, 0, -10))
	farDay := beijingDayOf(time.Now().AddDate(0, 0, -60))
	oldP := filepath.Join(dir, "quote_stream-"+oldDay+".jsonl")
	recentP := filepath.Join(dir, "quote_stream-"+recentDay+".jsonl")
	farP := filepath.Join(dir, "quote_stream-"+farDay+".jsonl")
	strangeP := filepath.Join(dir, "quote_stream-不合法日期.jsonl")
	for _, f := range []string{oldP, recentP, farP, strangeP, p} {
		if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}
	yday, ynoons := w4eBeijingDate(t, -1)
	tday, tnoons := w4eBeijingDate(t, 0)
	sl, err := NewStreamLog(p)
	if err != nil {
		t.Fatalf("NewStreamLog: %v", err)
	}
	sl.nowFn = func() time.Time { return ynoons }
	sl.day = yday
	if err := sl.Write(w4eSnap("600001")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sl.nowFn = func() time.Time { return tnoons }
	if err := sl.Write(w4eSnap("000001")); err != nil {
		t.Fatalf("write cross-day: %v", err)
	}
	if err := sl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(oldP); !os.IsNotExist(err) {
		t.Fatalf("40 天前归档 %s 应被清理", filepath.Base(oldP))
	}
	if _, err := os.Stat(farP); !os.IsNotExist(err) {
		t.Fatalf("60 天前归档 %s 应被清理", filepath.Base(farP))
	}
	if _, err := os.Stat(recentP); err != nil {
		t.Fatalf("10 天前归档不应被删: %v", err)
	}
	if _, err := os.Stat(strangeP); err != nil {
		t.Fatalf("日期不可解析的外来文件永不应被删: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "quote_stream-"+yday+".jsonl")); err != nil {
		t.Fatalf("本轮昨日归档应存在: %v", err)
	}
	// 活动文件内容应只含今日帧（轮转清场成功）
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "000001") || strings.Contains(string(data), "600001") {
		t.Fatalf("轮转后活动文件混入旧帧: %q", data)
	}
	if sl.day != tday {
		t.Fatalf("轮转后活动日应为 %s，得到 %s", tday, sl.day)
	}
}

// TestW4EStreamLogKeeps30DaysWindowConstant 口径锁：保留天数常量维持 30（改口径须连同本锁与文档同步）。
func TestW4EStreamLogKeeps30DaysWindowConstant(t *testing.T) {
	if streamLogKeepDays != 30 {
		t.Fatalf("§0926E2E-17B 保留天数应为 30，得到 %d", streamLogKeepDays)
	}
}
