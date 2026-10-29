// tlive_daykey_test.go — §W-07（2026-10-29 夜批）：日键必须走 data.TradingDayDate（北京时），
// 不许用服务器本地钟。广州 +08 看不出差别，KST/UTC 部署下日切会错位，账本会提前一天换、
// 当天的在途腿被当成"昨日记录"（跨日不复活 ⇒ 直接丢）。
package engine

import (
	"testing"
	"time"
)

// G1：北京 23:30 仍是当日。这条本身区分不了新旧实现，作用是钉住"归北京时"这个前提。
func TestDayKeyUsesBeijingClock(t *testing.T) {
	rg := newTLiveRegistry(t.TempDir())
	now := time.Date(2026, 10, 29, 15, 30, 0, 0, time.UTC) // = 北京 10-29 23:30
	if got := rg.dayOfFn(now); got != "20261029" {
		t.Fatalf("北京 23:30 的日键应为 20261029，got %s", got)
	}
}

// G2：**这条才是判别性断言**。北京 10-30 00:30 必须给 20261030；
// 旧实现（服务器本地钟、UTC 日期）会给 20261029 —— 账本提前一天换。
func TestDayKeyCrossesAtBeijingMidnight(t *testing.T) {
	rg := newTLiveRegistry(t.TempDir())
	now := time.Date(2026, 10, 29, 16, 30, 0, 0, time.UTC) // = 北京 10-30 00:30
	if got := rg.dayOfFn(now); got != "20261030" {
		t.Fatalf("北京 00:30 的日键应为 20261030（旧实现会给 20261029），got %s", got)
	}
}

// G3：已接受的语义变化——周六看到的是**周五**的账本，不是"当日空账"。
// 这与资金帧、信号决策、cntime 日键一致，是修正不是退化；别当成 bug 改回去。
func TestDayKeyOnSaturdayIsFriday(t *testing.T) {
	rg := newTLiveRegistry(t.TempDir())
	cst := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, cst) // 周六
	if got := rg.dayOfFn(now); got != "20261030" {
		t.Fatalf("周六的日键应为上一交易日 20261030，got %s", got)
	}
}
