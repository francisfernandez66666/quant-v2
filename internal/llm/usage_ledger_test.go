// LLM 用量账本回归（§FIX-20261028-F17，2026-10-29）。
//
// 缺陷本体：计数在 Client 自己的 atomic 上 —— ①进程重启清零（现网 uptime 14m49s 而 calls=0）；
// ②每次保存 LLM 配置都会 llmRecreate 重建 Client，计数又清零一次（比重启频繁）。
//
// 本文件锁四件事：
//  1. 重建 Client（同一账本）后计数不丢；
//  2. 重启（同一文件新建账本）后计数不丢；
//  3. 跨日文件不回填（新交易日从 0 开始，不累计）；
//  4. 日键走北京时间（TZ=UTC 下也要落在北京日）。
//
// English: the usage ledger must survive client recreation and process restart, reset across
// days, and key on the Beijing trading day regardless of the host TZ.
package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// ledgerTestClient 造一个只用来计数的 Client（不真打网络）。
func ledgerTestClient(l *UsageLedger) *Client {
	return New(Config{APIKey: "k-test", DailyCallBudget: 0, UsageLedger: l})
}

// TestUsageSurvivesRecreate 同一账本重建 Client ⇒ 计数延续（热换配置不丢账）。
func TestUsageSurvivesRecreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm_usage.json")
	l := NewUsageLedger(path)
	for i := 0; i < 3; i++ {
		c := ledgerTestClient(l)
		if err := c.preFlight(); err != nil {
			t.Fatalf("preFlight: %v", err)
		}
	}
	// 第 4 个 Client（模拟再一次热换）读到的不该是 0
	c := ledgerTestClient(l)
	if got := c.UsageStats()["calls"]; got != 3 {
		t.Fatalf("热换 Client 后 calls 应为 3，实到 %d", got)
	}
}

// TestUsageSurvivesRestart 同一文件新建账本 ⇒ 计数延续（进程重启不丢账）。
func TestUsageSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm_usage.json")
	l := NewUsageLedger(path)
	for i := 0; i < 3; i++ {
		if err := ledgerTestClient(l).preFlight(); err != nil {
			t.Fatalf("preFlight: %v", err)
		}
	}
	// 模拟重启：全新账本读同一文件
	l2 := NewUsageLedger(path)
	c := ledgerTestClient(l2)
	if got := c.UsageStats()["calls"]; got != 3 {
		t.Fatalf("重启后 calls 应为 3，实到 %d", got)
	}
}

// TestUsageResetsOnDayChange 文件里是昨日 ⇒ 今日从 0 开始（不累计）。
func TestUsageResetsOnDayChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llm_usage.json")
	// 造一份"昨天"的文件
	raw, _ := json.Marshal(usageFileShape{Day: "19990101", Calls: 99, Tokens: 12345, ConsultCalls: 7})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	l := NewUsageLedger(path)
	day, calls, tokens, consult := l.Snapshot()
	if day != cntime.DayCompactOf(time.Now()) {
		t.Fatalf("日键应为北京今日 %s，实到 %s", cntime.DayCompactOf(time.Now()), day)
	}
	if calls != 0 || tokens != 0 || consult != 0 {
		t.Fatalf("跨日必须从 0 起算，实到 calls=%d tokens=%d consult=%d", calls, tokens, consult)
	}
	// 第一次写入后文件必须被改写成今日（否则下次启动还会读到旧日）
	l.AddCalls(1)
	var f usageFileShape
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse back: %v", err)
	}
	if f.Day != day || f.Calls != 1 {
		t.Fatalf("落盘应为今日+1 次，实到 %+v", f)
	}
}

// TestUsageDayKeyIsBeijing 日键必须是北京日（与 llmToday 同源），不随宿主 TZ 漂移。
func TestUsageDayKeyIsBeijing(t *testing.T) {
	// llmToday 现在走 cntime.DayCompactOf；两者必须逐字一致（防止有人改回本地时区）
	want := cntime.DayCompactOf(time.Now())
	got := llmToday()
	if want != "" && int64Str(got) != want {
		t.Fatalf("llmToday()=%d 与北京日 %s 不同源", got, want)
	}
}

// int64Str 把 yyyymmdd 的 int64 还原成同宽字符串（不足 8 位不补零：20010101 这类是 8 位）。
func int64Str(n int64) string {
	if n == 0 {
		return "0"
	}
	buf := [8]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
