// breaker_detail_test.go — §DATA-RELIABILITY（2026-10-26）：
// 东财接口域熔断明细必须真实反映连败/熔断态，成功调用要清零连败计数。
package data

import (
	"fmt"
	"testing"
)

func TestBreakerDetail(t *testing.T) {
	m := &MarketAPI{emBreakers: make(map[string]*emBreaker)}
	for i := 0; i < emBreakerThreshold; i++ {
		m.emRecord("push2.test", fmt.Errorf("boom-%d", i))
	}
	item := m.BreakerDetail()["push2.test"]
	if item == nil {
		t.Fatal("熔断明细缺少 push2.test")
	}
	if open, _ := item["open"].(bool); !open {
		t.Fatalf("连续 %d 次失败后应处于熔断态: %+v", emBreakerThreshold, item)
	}
	if streak, _ := item["fail_streak"].(int); streak != emBreakerThreshold {
		t.Fatalf("连败计数应为 %d，got %v", emBreakerThreshold, item["fail_streak"])
	}
	m.emRecord("push2.test", nil)
	item = m.BreakerDetail()["push2.test"]
	if streak, _ := item["fail_streak"].(int); streak != 0 {
		t.Fatalf("成功调用应清零连败，got %v", item["fail_streak"])
	}
}
