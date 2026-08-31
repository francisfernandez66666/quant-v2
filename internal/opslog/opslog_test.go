// opslog_test.go — §DAILY_OPSLOG 每日运行日志回归：写入/滚动/DayOnce 去重/OncePer 节流/保留期清理。
package opslog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedClock 注入固定时钟（局部替换 now，测试完恢复）。
func fixedClock(t *testing.T, at time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
}

// TestLogfWriteAndAppend 验证：同日多次写入追加同文件，行格式含时间戳/标签/消息。
func TestLogfWriteAndAppend(t *testing.T) {
	dir := t.TempDir()
	fixedClock(t, time.Date(2026, 8, 31, 15, 0, 0, 0, time.Local))
	Init(dir, 0)

	Logf("quant", "成交 %s %s qty=%d", "买入", "000600.SZ", 100)
	Logf("research", "任务 #%d 完成", 7)

	data, err := os.ReadFile(filepath.Join(dir, "opslog-20260831.log"))
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("应有两行, got %d: %q", len(lines), data)
	}
	if !strings.Contains(lines[0], "[quant] 成交 买入 000600.SZ qty=100") ||
		!strings.HasPrefix(lines[0], "2026-08-31 15:00:00") {
		t.Fatalf("行格式不符: %q", lines[0])
	}
	if !strings.Contains(lines[1], "[research] 任务 #7 完成") {
		t.Fatalf("第二行不符: %q", lines[1])
	}
}

// TestNotInitializedSilent 未 Init 时静默无 panic、无文件。
func TestNotInitializedSilent(t *testing.T) {
	savedDir, savedWarn := dir, warned
	t.Cleanup(func() { dir, warned = savedDir, savedWarn })
	dir = ""
	Logf("quant", "不应写出") // 不应 panic
}

// TestDayOnce 每本地日至多一次：同日重复调用只执行一次，跨日后重新执行。
func TestDayOnce(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 31, 9, 30, 0, 0, time.Local)
	fixedClock(t, at)
	Init(dir, 0)

	n := 0
	fn := func() { n++ }
	DayOnce("snapshot", fn)
	DayOnce("snapshot", fn)
	DayOnce("snapshot", fn)
	if n != 1 {
		t.Fatalf("同日应只执行一次, got %d", n)
	}
	fixedClock(t, at.Add(26*time.Hour)) // 次日
	DayOnce("snapshot", fn)
	if n != 2 {
		t.Fatalf("跨日应再执行一次, got %d", n)
	}
}

// TestOncePerThrottle 同窗口内节流，窗口过后放行。
func TestOncePerThrottle(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 31, 10, 0, 0, 0, time.Local)
	fixedClock(t, at)
	Init(dir, 0)

	n := 0
	fn := func() { n++ }
	OncePer("memgate", time.Hour, fn)
	OncePer("memgate", time.Hour, fn)
	OncePer("memgate", time.Hour, fn)
	if n != 1 {
		t.Fatalf("1h 窗口内应只执行一次, got %d", n)
	}
	fixedClock(t, at.Add(61*time.Minute))
	OncePer("memgate", time.Hour, fn)
	if n != 2 {
		t.Fatalf("窗口过后应再执行, got %d", n)
	}
}

// TestRetentionSweep 保留期清理：跨日写入时删除超期文件，保留期内不动。
func TestRetentionSweep(t *testing.T) {
	dir := t.TempDir()
	fixedClock(t, time.Date(2026, 8, 31, 9, 0, 0, 0, time.Local))
	Init(dir, 3) // 保留 3 天

	// 伪造 4 份历史文件：保留期内(留) / 恰好保留天数边界(留) / 90 天前(删) / 乱名(不删)
	stale := time.Date(2026, 8, 27, 0, 0, 0, 0, time.Local).Format(dateLayout) // 4 天前 → 删
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local).Format(dateLayout)    // 90 天前 → 删
	for _, name := range []string{"opslog-20260830.log", "opslog-20260828.log", "opslog-" + stale + ".log", "opslog-" + old + ".log", "other.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	Logf("quant", "触发跨日清理") // 同日首写 → lastDay 从空变 20260831 → sweep

	for _, keep := range []string{"opslog-20260830.log", "opslog-20260828.log", "other.txt"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("保留期内文件不应被删: %s (%v)", keep, err)
		}
	}
	for _, gone := range []string{"opslog-" + stale + ".log", "opslog-" + old + ".log"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("超期文件应被删除: %s", gone)
		}
	}
}

// TestDayRollNewFile 跨日写入滚动到新文件，旧文件保留。
func TestDayRollNewFile(t *testing.T) {
	dir := t.TempDir()
	fixedClock(t, time.Date(2026, 8, 31, 23, 59, 0, 0, time.Local))
	Init(dir, 90)
	Logf("quant", "收盘前")
	fixedClock(t, time.Date(2026, 9, 1, 9, 15, 0, 0, time.Local))
	Logf("quant", "开盘")

	if _, err := os.Stat(filepath.Join(dir, "opslog-20260831.log")); err != nil {
		t.Fatalf("旧日文件应保留: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "opslog-20260901.log"))
	if err != nil || !strings.Contains(string(data), "开盘") {
		t.Fatalf("新日文件应含新行: %v %q", err, data)
	}
}
