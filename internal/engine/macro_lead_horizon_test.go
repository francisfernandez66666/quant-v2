// macro_lead_horizon_test.go — §DYN-SIZE P4 引擎侧前瞻窗折算的**性质测试**。
//
// 为什么不注入日历做精确断言：`data` 的日历是进程级全局，`SetClosedDays` 只有写入口没有恢复口——
// 引擎测试若改了它，同包其它依赖"日历未加载（fail-open）"的用例会被污染。精确的交易日折算已由
// `internal/data` 的 `TradingDaySpan` / `GetActiveMacroEventsWithin` 两条用例钉住；这里只锁**性质**：
//   - 不得比改造前的定长 7 天更窄（无回归）；
//   - 不得跑飞（上限 30 天）；
//   - 同一时刻重复调用必须同值（纯函数，无隐藏状态）。
//
// English: property test for the engine-side lead-window conversion. The calendar is a process-global
// with no restore hook, so the exact conversion is locked in the data package instead; here we only
// pin the properties: never narrower than the old 7 days, never runaway, and deterministic.
package engine

import (
	"testing"
	"time"
)

// TestMacroLeadHorizonProperties 前瞻窗的单调边界与确定性。
func TestMacroLeadHorizonProperties(t *testing.T) {
	nows := []time.Time{
		time.Date(2026, 9, 30, 10, 0, 0, 0, time.Local),  // 长假前最后一日
		time.Date(2026, 10, 14, 10, 0, 0, 0, time.Local), // 普通周三
		time.Date(2026, 10, 17, 10, 0, 0, 0, time.Local), // 周六
		time.Date(2027, 2, 3, 10, 0, 0, 0, time.Local),   // 春节前（跨年）
	}
	for _, now := range nows {
		h := macroLeadHorizon(now)
		if h < macroLeadHorizonMinDays*24*time.Hour {
			t.Fatalf("%s：前瞻窗 %s 不得窄于改造前的 %d 天（会漏事件）",
				now.Format("2006-01-02"), h, macroLeadHorizonMinDays)
		}
		if h > 30*24*time.Hour {
			t.Fatalf("%s：前瞻窗 %s 跑飞（上限 30 天）", now.Format("2006-01-02"), h)
		}
		if again := macroLeadHorizon(now); again != h {
			t.Fatalf("%s：同刻重复调用应同值，得 %s vs %s", now.Format("2006-01-02"), h, again)
		}
	}
}

// TestMacroLeadHorizonConstants 兜底常量的语义约束：日历不可用时必须比"日历可用时的下限"更宽
// （否则日历一挂，前瞻窗反而收窄，长假前漏事件更严重）。
func TestMacroLeadHorizonConstants(t *testing.T) {
	if macroLeadHorizonFallbackDays <= macroLeadHorizonMinDays {
		t.Fatalf("兜底 %d 天必须宽于下限 %d 天", macroLeadHorizonFallbackDays, macroLeadHorizonMinDays)
	}
	// 下限必须 >= 改造前的定长 7 天（定长 7 天是本次要修的口径，不能成为回归）。
	if macroLeadHorizonMinDays < 7 {
		t.Fatalf("下限 %d 天窄于改造前的 7 天口径", macroLeadHorizonMinDays)
	}
}
