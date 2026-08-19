package newsagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// newTestTracker 创建临时目录下的 tracker 实例，测试结束清理。
func newTestTracker(t *testing.T) *tracker {
	t.Helper()
	dir := t.TempDir()
	return newTracker(dir)
}

// nowStr 返回当前时刻的 Datetime 字符串（保证 >= 当前交易日窗口起点，避免被裁剪）。
func nowStr() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// TestTrackerPendingAddRemove pending 队列基础语义：添加→查询→移除并标记 seen。
func TestTrackerPendingAddRemove(t *testing.T) {
	tr := newTestTracker(t)
	items := []data.NewsItem{
		{Title: "某公司业绩预增", Datetime: nowStr()},
		{Title: "某板块政策利好", Datetime: nowStr()},
	}
	tr.AddPending(items)

	if got := len(tr.Pending()); got != 2 {
		t.Fatalf("期望 pending 2 条, 实际 %d", got)
	}
	if !tr.IsPending("某公司业绩预增") {
		t.Fatal("期望标题在 pending 中")
	}

	tr.RemovePending(items[:1])
	if tr.IsPending("某公司业绩预增") {
		t.Fatal("期望已移除的标题不在 pending 中")
	}
	if !tr.IsSeen("某公司业绩预增") {
		t.Fatal("期望移除的标题标记为 seen")
	}
	if got := len(tr.Pending()); got != 1 {
		t.Fatalf("期望 pending 剩 1 条, 实际 %d", got)
	}
}

// TestTrackerPendingDedup 已 seen 或已在队列中的标题不重复入队。
func TestTrackerPendingDedup(t *testing.T) {
	tr := newTestTracker(t)
	it := data.NewsItem{Title: "某公司业绩预增", Datetime: nowStr()}

	tr.AddPending([]data.NewsItem{it})
	tr.AddPending([]data.NewsItem{it}) // 重复入队应被忽略
	if got := len(tr.Pending()); got != 1 {
		t.Fatalf("期望 pending 1 条(去重), 实际 %d", got)
	}

	// 成功归因（RemovePending 移出队列并标记 seen）后，同标题不得重新入队
	tr.RemovePending([]data.NewsItem{it})
	if tr.IsPending("某公司业绩预增") {
		t.Fatal("移除后不应仍在队列中")
	}
	if !tr.IsSeen("某公司业绩预增") {
		t.Fatal("移除应标记为 seen")
	}
	tr.AddPending([]data.NewsItem{it})
	if tr.IsPending("某公司业绩预增") {
		t.Fatal("已 seen 的标题不应重新入队")
	}
	if got := len(tr.Pending()); got != 0 {
		t.Fatalf("期望重新入队被拦截, 实际 %d 条", got)
	}
}

// TestTrackerPendingCap 超上限淘汰最旧（保新弃旧）。
func TestTrackerPendingCap(t *testing.T) {
	tr := newTestTracker(t)
	base := time.Now() // 全部落在当前交易日窗口内
	for i := 0; i < maxPendingItems+20; i++ {
		tr.AddPending([]data.NewsItem{
			{Title: "新闻" + itoa(i), Datetime: base.Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05")},
		})
	}
	if got := len(tr.Pending()); got != maxPendingItems {
		t.Fatalf("期望 pending 恰为 %d 条, 实际 %d", maxPendingItems, got)
	}
	// 保留的是最新加入的
	if !tr.IsPending("新闻" + itoa(maxPendingItems+19)) {
		t.Fatal("期望最新加入的标题保留")
	}
	if tr.IsPending("新闻0") {
		t.Fatal("期望最早的标题被淘汰")
	}
}

// TestTrackerPendingPersist pending 队列持久化到文件并可跨实例恢复。
func TestTrackerPendingPersist(t *testing.T) {
	dir := t.TempDir()
	tr := newTracker(dir)
	tr.AddPending([]data.NewsItem{
		{Title: "某板块利好", Datetime: nowStr()},
	})
	tr.save()

	tr2 := newTracker(dir) // 重新加载
	if got := len(tr2.Pending()); got != 1 {
		t.Fatalf("期望重启后恢复 1 条 pending, 实际 %d", got)
	}
	if !tr2.IsPending("某板块利好") {
		t.Fatal("期望重启后标题仍在 pending")
	}
	os.Remove(filepath.Join(dir, "news_tracker.json"))
}

// TestTradingDayStart 交易日窗口起点：15:00 后归属下一交易日窗口（跳过周末）。
func TestTradingDayStart(t *testing.T) {
	// 周五 16:00：已过当日 15:00，窗口起点为周五 15:00（下一个交易日窗口开头）
	friday := time.Date(2026, 1, 9, 16, 0, 0, 0, time.Local)
	start := tradingDayStart(friday)
	want := time.Date(2026, 1, 9, 15, 0, 0, 0, time.Local)
	if !start.Equal(want) {
		t.Fatalf("期望窗口起点 %v, 实际 %v", want, start)
	}

	// 周一 10:00：未到 15:00，窗口起点回退到上周五 15:00
	monday := time.Date(2026, 1, 12, 10, 0, 0, 0, time.Local)
	start = tradingDayStart(monday)
	want = time.Date(2026, 1, 9, 15, 0, 0, 0, time.Local)
	if !start.Equal(want) {
		t.Fatalf("期望周一窗口起点 %v, 实际 %v", want, start)
	}
}

// TestPrunePendingWindow 跨交易日窗口的旧新闻被裁剪，窗口内新闻保留。
// 直接设置队列并调用底层方法，注入固定"当前时刻"，避免真实时钟的边界抖动。
func TestPrunePendingWindow(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Date(2026, 1, 12, 10, 0, 0, 0, time.Local) // 周一 10:00
	items := []data.NewsItem{
		{Title: "上周四新闻", Datetime: "2026-01-08 10:00:00"},  // 早于窗口起点(周五15:00) → 裁剪
		{Title: "周五盘后新闻", Datetime: "2026-01-09 16:00:00"}, // 周五15:00后 → 保留
		{Title: "周一盘前新闻", Datetime: "2026-01-12 08:30:00"}, // 窗口内 → 保留
	}
	tr.mu.Lock()
	tr.data.PendingItems = append(tr.data.PendingItems, items...)
	tr.prunePendingLocked(now)
	kept := make(map[string]bool, len(tr.data.PendingItems))
	for _, it := range tr.data.PendingItems {
		kept[it.Title] = true
	}
	tr.mu.Unlock()
	if kept["上周四新闻"] {
		t.Fatal("期望上周四新闻被裁剪")
	}
	if !kept["周五盘后新闻"] {
		t.Fatal("期望周五盘后新闻保留")
	}
	if !kept["周一盘前新闻"] {
		t.Fatal("期望周一盘前新闻保留")
	}
	_ = tr.save()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
