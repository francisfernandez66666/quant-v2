// consult_fix9_engine_test.go — §FIX-9e/9f(20260919 批五) 引擎侧回归：
// 数据头按交易日历诚实标注新鲜度口径（盘中=实时、非盘中=最近收盘），
// 且判定必须走引擎可注入时钟（否则用例结果随真实墙钟漂移）。
// English: the data header's freshness wording must follow the trading calendar and
// honor the injectable engine clock (deterministic, never wall-clock dependent).
package engine

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/newsagent"
)

// TestConsultHeaderFreshnessByClock §FIX-9f：同一引擎，仅改注入时钟——
// 交易日盘中出"盘中实测"，周六出"非交易时段/最近收盘口径"。
// English: only the injected clock changes; intraday vs weekend wording must flip accordingly.
func TestConsultHeaderFreshnessByClock(t *testing.T) {
	e := &Engine{
		newsAgent:        newsagent.New(nil, nil, nil, ""),
		consultBlockLoad: func(code, name string) string { return "—— 股票 " + code + " 测试块" },
	}
	e.clockFn = func() time.Time { return time.Date(2026, 8, 3, 10, 30, 0, 0, cntime.Loc) }
	ctx := e.buildConsultContext("600580 怎么样")
	if !strings.Contains(ctx, "即最新盘中实测数据") || strings.Contains(ctx, "非交易时段") {
		t.Fatalf("盘中应标'盘中实测'口径: %s", firstLines(ctx, 2))
	}
	// 2026-08-08 为周六：同时刻也必须改口"最近收盘口径"
	e.clockFn = func() time.Time { return time.Date(2026, 8, 8, 10, 30, 0, 0, cntime.Loc) }
	ctx2 := e.buildConsultContext("600580 怎么样")
	if !strings.Contains(ctx2, "当前非交易时段") || !strings.Contains(ctx2, "最近收盘") {
		t.Fatalf("周末应改口'最近收盘口径': %s", firstLines(ctx2, 2))
	}
	// 既有契约不破：抓取时间与"实时抓取"字样仍须在
	if !strings.Contains(ctx2, "实时抓取") || !strings.Contains(ctx2, "抓取时间") {
		t.Fatal("新鲜度改口不得丢失抓取时间戳与实时抓取语义")
	}
}

// firstLines 取上下文前 n 行（失败信息用，避免整段刷屏）。
func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}
