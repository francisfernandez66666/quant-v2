// §RFIX-3/4 候选 reason 组装回归：预期触发率尾巴（含「预期触发=0」告警 token）与
// 样本内 IR 口径一致性说明（纯函数级，不触库）。
package main

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/research"
)

func TestFactorTrigNote(t *testing.T) {
	if s := factorTrigNote(research.DiscoverResult{TrigDays: 0}); s != "" {
		t.Fatalf("未估算（TrigDays=0）应空串，实际 %q", s)
	}
	zero := factorTrigNote(research.DiscoverResult{TrigDays: 24, Trig70: 0, Trig95: 0})
	if !strings.Contains(zero, "预期触发=0") || !strings.Contains(zero, "24日") {
		t.Fatalf("零触发应含告警 token 与天数，实际 %q", zero)
	}
	pos := factorTrigNote(research.DiscoverResult{TrigDays: 24, Trig70: 12.5, Trig95: 3.2})
	if strings.Contains(pos, "预期触发=0") || !strings.Contains(pos, "12.5") || !strings.Contains(pos, "3.2") {
		t.Fatalf("正常触发应展示两档只数且不误带告警 token，实际 %q", pos)
	}
	// 近似格式用「预期触发≈」前缀，保证 apply 守卫的 "预期触发=0" 子串识别不被 0.x 误命中
	if strings.Contains(pos, "预期触发=0.") {
		t.Fatal("格式退化：0.x 值不得以等号形式输出")
	}
}
