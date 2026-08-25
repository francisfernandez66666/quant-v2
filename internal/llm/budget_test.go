// budget_test.go — §GAP5.1 成本治理回归：日预算熔断与跨日归零。
package llm

import (
	"strings"
	"testing"
)

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
