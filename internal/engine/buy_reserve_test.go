// buy_reserve_test.go — §P0-5（2026-10-30）预算预占的账本语义锁。
//
// 只测内存算术与释放/TTL：发送前复核依赖真实行情/账本，放集成面验证；
// 这里保证"预占能被看见、能被释放、过期会自清"三条不变量。
package engine

import (
	"testing"
	"time"
)

func TestBuyReserveAccounting(t *testing.T) {
	e := &Engine{userID: "u1"}
	e.reserveBuyBudget("u1", "sigA", 1000)
	e.reserveBuyBudget("u1", "sigB", 500)
	if got := e.reservedBuyAmount("u1"); got != 1500 {
		t.Fatalf("预占合计应为 1500, got %v", got)
	}
	// 发送前复核要算"别人占了多少"：排除自己
	if got := e.reservedAmountExcept("u1", "sigA"); got != 500 {
		t.Fatalf("排除自己后应为 500, got %v", got)
	}
	e.releaseBuyBudget("u1", "sigA")
	if got := e.reservedBuyAmount("u1"); got != 500 {
		t.Fatalf("释放后应为 500, got %v", got)
	}
	// 账号隔离：别的账号看不见这笔预占
	if got := e.reservedBuyAmount("u2"); got != 0 {
		t.Fatalf("跨账号不得串用预占, got %v", got)
	}
}

func TestBuyReserveTTLExpiry(t *testing.T) {
	e := &Engine{userID: "u1"}
	e.reserveBuyBudget("u1", "sigA", 1000)
	// 手工把时刻拨到 TTL 之外：模拟 worker 异常退出后留下的幽灵预占
	e.mu.Lock()
	ent := e.buyReserve["u1|sigA"]
	ent.At = time.Now().Add(-buyReserveTTL - time.Second)
	e.buyReserve["u1|sigA"] = ent
	e.mu.Unlock()
	if got := e.reservedBuyAmount("u1"); got != 0 {
		t.Fatalf("过期预占必须自清, got %v", got)
	}
}
