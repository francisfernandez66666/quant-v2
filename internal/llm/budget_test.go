// budget_test.go — §GAP5.1 成本治理回归：日预算熔断与跨日归零。
package llm

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestBudgetCircuitBreaker 验证日调用/token 预算熔断与预算 0 不设限。
func TestBudgetCircuitBreaker(t *testing.T) {
	c := New(Config{APIKey: "k", DailyCallBudget: 2})
	// 模拟当日已用 2 次
	c.usageDay.Store(llmToday())
	c.usageCalls.Store(2)
	if err := c.preFlight(); err == nil || !strings.Contains(err.Error(), "调用预算") {
		t.Fatalf("超日调用预算应熔断, got %v", err)
	}
	// 预算 0 = 不设限
	c.callBudget.Store(0)
	if err := c.preFlight(); err != nil {
		t.Fatalf("预算 0 应不设限, got %v", err)
	}
	// token 预算
	c.tokenBudget.Store(100)
	c.usageTokens.Store(100)
	if err := c.preFlight(); err == nil || !strings.Contains(err.Error(), "token 预算") {
		t.Fatalf("超 token 预算应熔断, got %v", err)
	}
}

// TestUsageDayRoll 验证跨日用量计数归零、日戳更新为今日。
func TestUsageDayRoll(t *testing.T) {
	c := New(Config{APIKey: "k"})
	c.usageDay.Store(llmToday() - 1)
	c.usageCalls.Store(99)
	c.usageTokens.Store(999)
	c.rollUsageDay()
	if c.usageCalls.Load() != 0 || c.usageTokens.Load() != 0 {
		t.Fatalf("跨日应归零, calls=%d tokens=%d", c.usageCalls.Load(), c.usageTokens.Load())
	}
	if c.usageDay.Load() != llmToday() {
		t.Fatal("日戳应更新为今日")
	}
}

// TestRecordUsageEstimate 验证 token 累计与字符粗估口径。
func TestRecordUsageEstimate(t *testing.T) {
	c := New(Config{APIKey: "k"})
	c.recordUsage(0, 0)
	if c.usageTokens.Load() != 0 {
		t.Fatal("零用量不应累计")
	}
	c.recordUsage(30, 30)
	if got := c.usageTokens.Load(); got != 60 {
		t.Fatalf("直传 token 应累计 60, got %d", got)
	}
	// 粗估口径：30 字符 ≈ 10 token
	if got := estimateTokens(strings.Repeat("a", 30)); got != 10 {
		t.Fatalf("estimateTokens(30 字符) 应为 10, got %d", got)
	}
}

// TestPickKeySkipsCooling §S6 回归：冷却中的 key 被跳过；全部冷却时降级仍返回可用 key。
func TestPickKeySkipsCooling(t *testing.T) {
	c := New(Config{APIKeys: []string{"K0", "K1", "K2"}})
	c.keyCoolUntil = make([]atomic.Int64, 3)
	c.keyCoolUntil[1].Store(time.Now().Add(time.Minute).Unix()) // K1 冷却中
	c.keyCoolUntil[2].Store(time.Now().Add(time.Minute).Unix()) // K2 冷却中
	got := map[string]bool{}
	for i := 0; i < 10; i++ {
		got[c.pickKey()] = true
	}
	if got["K1"] || got["K2"] {
		t.Fatalf("冷却中的 key 不应被选中: %v", got)
	}
	if !got["K0"] {
		t.Fatal("健康 key K0 应被选中")
	}
	// 全部冷却 → 降级仍返回某 key
	c.keyCoolUntil[0].Store(time.Now().Add(time.Minute).Unix())
	if c.pickKey() == "" {
		t.Fatal("全部冷却时应降级返回轮询 key")
	}
}

// TestMarkKeyStatus §S6 回归：401 长冷却、429 按 Retry-After。
func TestMarkKeyStatus(t *testing.T) {
	c := New(Config{APIKeys: []string{"A", "B"}})
	c.keyCoolUntil = make([]atomic.Int64, 2)
	now := time.Now().Unix()
	c.markKeyStatus("B", 429, 30*time.Second)
	if c.keyCoolUntil[1].Load() <= now {
		t.Fatal("429 应给 B 记冷却")
	}
	c.markKeyStatus("B", 401, 0)
	if c.keyCoolUntil[1].Load() <= now+int64(20*time.Second/time.Second) {
		t.Fatalf("401 应记长冷却(%s), until=%d", keyCoolAuthFail, c.keyCoolUntil[1].Load())
	}
	// 200 不标记
	before := c.keyCoolUntil[0].Load()
	c.markKeyStatus("A", 200, 0)
	if c.keyCoolUntil[0].Load() != before {
		t.Fatal("成功响应不应触发冷却")
	}
}

// TestParseRetryAfter Retry-After 头解析（秒数与 HTTP 日期两种形态）。
func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("30"); d != 30*time.Second {
		t.Fatalf("秒数形态解析错误: %v", d)
	}
	future := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(future)
	if d <= 20*time.Second || d > 60*time.Second {
		t.Fatalf("HTTP 日期形态解析异常: %v", d)
	}
	if parseRetryAfter("") != 0 || parseRetryAfter("junk") != 0 {
		t.Fatal("缺失/非法应返回 0")
	}
}
