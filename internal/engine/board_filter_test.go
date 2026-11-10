// board_filter_test.go — 2026-11-10 owner 需求（需求6）：交易提醒面（消息中心）板块过滤的行为锁。
// syncMessages 是「交易提醒」surface 的入口，三开关关闭的板块个股不得进消息中心；
// cfgMgr 未装配（最小装配/测试）时保持三板块全开的历史行为。
package engine

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

func TestFilterSignalsByBoard(t *testing.T) {
	in := []combat_agent.Signal{
		{Code: "688001.SH", Strategy: "龙头"},
		{Code: "300750.SZ", Strategy: "N形"},
		{Code: "430047.BJ", Strategy: "动量"},
		{Code: "600000.SH", Strategy: "双响炮"},
		{Code: "000001.SZ", Strategy: "龙头"},
	}
	// 只关科创板：688 拦、其余留
	bf := config.BoardFilterConfig{StarEnabled: false, ChiNextEnabled: true, BseEnabled: true}
	got := filterSignalsByBoard(in, bf)
	if len(got) != 4 {
		t.Fatalf("科创板关闭后应剩 4 条，got %d", len(got))
	}
	for _, s := range got {
		if s.Code == "688001.SH" {
			t.Fatal("科创板关闭后 688 开头不得进入提醒面")
		}
	}
	// 全开：原样保留（含顺序）
	all := config.DefaultBoardFilterConfig()
	got = filterSignalsByBoard(in, all)
	if len(got) != len(in) {
		t.Fatalf("三板块全开应原样保留，got %d want %d", len(got), len(in))
	}
	// 空表：安全返回
	if n := len(filterSignalsByBoard(nil, bf)); n != 0 {
		t.Fatalf("空表应返回空，got %d", n)
	}
}
