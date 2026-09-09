// boardSource_test.go — §WS-D D-1 FailoverBoard 单测：顺序降级、连续失败熔断、冷却探测恢复、
// 全源耗尽告警、空列表处理。
// English: §WS-D D-1 FailoverBoard tests — ordered failover, circuit opening after N failures, cooldown
// probe recovery, all-sources-down alert, empty-list handling.
package data

import (
	"errors"
	"testing"
	"time"
)

// mockBoard 可控板块源（每次调用计次，失败计数注入）。
// English: controllable board source (call counter + injected failure).
type mockBoard struct {
	name   string
	fail   bool
	calls  int
	empty  bool
	sector []SectorInfo
}

func (m *mockBoard) Name() string { return m.name }
func (m *mockBoard) Sectors() ([]SectorInfo, error) {
	m.calls++
	if m.fail {
		return nil, errors.New("mock fail")
	}
	if m.empty {
		return nil, nil
	}
	return m.sector, nil
}

func sector(name string) []SectorInfo {
	return []SectorInfo{{Code: "BK0001", Name: name}}
}

// TestFailoverOrder 主源失败 → 依次降级到健康源。
func TestFailoverOrder(t *testing.T) {
	em := &mockBoard{name: "eastmoney", fail: true, sector: sector("EM")}
	ths := &mockBoard{name: "ths", sector: sector("THS")}
	fb := NewFailoverBoard(em, ths)
	s, err := fb.Sectors()
	if err != nil || len(s) != 1 || s[0].Name != "THS" {
		t.Fatalf("应降级到 ths, got %v err=%v", s, err)
	}
	if em.calls != 1 || ths.calls != 1 {
		t.Fatalf("调用次数: em=%d ths=%d", em.calls, ths.calls)
	}
}

// TestFailoverCircuitBreaks 连续失败达阈值 → 熔断跳过该源（不再调用），后续由健康源接管。
func TestFailoverCircuitBreaks(t *testing.T) {
	em := &mockBoard{name: "eastmoney", fail: true}
	ths := &mockBoard{name: "ths", sector: sector("THS")}
	fb := NewFailoverBoard(em, ths)
	fb.MaxFails = 2
	fb.Cooldown = time.Minute
	switches := 0
	fb.OnSwitch = func(from, to, reason string) { switches++ }

	for i := 0; i < 5; i++ {
		if _, err := fb.Sectors(); err != nil {
			t.Fatalf("第 %d 次应成功(thd), got %v", i, err)
		}
	}
	if em.calls != 2 {
		t.Fatalf("熔断后不应再调用 em（调用 %d 次，应为 2）", em.calls)
	}
	if switches == 0 {
		t.Fatalf("熔断应触发源切换告警")
	}
}

// TestFailoverRecovery 冷却期后放行一次探测：成功即恢复为可用源。
func TestFailoverRecovery(t *testing.T) {
	em := &mockBoard{name: "eastmoney", fail: true, sector: sector("EM")}
	ths := &mockBoard{name: "ths", sector: sector("THS")}
	fb := NewFailoverBoard(em, ths)
	fb.MaxFails = 1
	fb.Cooldown = 1 * time.Millisecond
	fb.ProbeEvery = 1 * time.Millisecond
	// 首次：em 失败即熔断，ths 接管
	if _, err := fb.Sectors(); err != nil {
		t.Fatalf("首次: %v", err)
	}
	// 冷却+探测窗口已过（毫秒级），em 恢复健康 → 下次调用 em 重新成为主源
	time.Sleep(20 * time.Millisecond)
	em.fail = false
	s, err := fb.Sectors()
	if err != nil || s[0].Name != "EM" {
		t.Fatalf("恢复后 em 应接管, got %v err=%v", s, err)
	}
}

// TestFailoverAllDown 全源失败 → 返回错误 + 全源耗尽告警。
func TestFailoverAllDown(t *testing.T) {
	em := &mockBoard{name: "eastmoney", fail: true}
	ths := &mockBoard{name: "ths", fail: true}
	fb := NewFailoverBoard(em, ths)
	alerted := 0
	fb.OnSwitch = func(from, to, reason string) { alerted++ }
	if _, err := fb.Sectors(); err == nil {
		t.Fatalf("全源失败应返回错误")
	}
	if alerted == 0 {
		t.Fatalf("全源耗尽应告警")
	}
}

// TestFailoverEmptyList 空列表视为失败（跳过该源，降级下一个）。
func TestFailoverEmptyList(t *testing.T) {
	empty := &mockBoard{name: "empty", empty: true}
	ths := &mockBoard{name: "ths", sector: sector("THS")}
	fb := NewFailoverBoard(empty, ths)
	s, err := fb.Sectors()
	if err != nil || len(s) != 1 || s[0].Name != "THS" {
		t.Fatalf("空列表应降级, got %v err=%v", s, err)
	}
}

// TestFailoverZeroSources 零源注入 → 错误（不臆造兜底）。
func TestFailoverZeroSources(t *testing.T) {
	fb := NewFailoverBoard()
	if _, err := fb.Sectors(); err == nil {
		t.Fatalf("零源应返回错误")
	}
}
