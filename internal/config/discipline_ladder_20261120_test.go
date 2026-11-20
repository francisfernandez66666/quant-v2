// discipline_ladder_20261120_test.go — §STOP-LADDER 出厂档与线名锁（2026-11-20）。
//
// 这一批把止损拆成两级（−3% 减半仓 / −6% 清仓），出厂的三个数字必须一起被钉住：
//
//	① 减仓线 3、清仓线 6，且**减仓严格轻于清仓**——撞线的形态是"第一级永远不单独命中"，
//	   现网看起来每天都只走清仓，没人会怀疑配置；
//	② 线上键名必须叫 reduce_pct：账号级纪律快照是**稀疏 merge**，键名写错等于永远读不到，
//	   而且读不到会被当成"缺键兜默认"静默通过（形状与 §PARAMS-STATIC-BASE 同族）。
//
// English: pins the factory ladder (trim 3 / clear 6, trim strictly lighter) and the wire key
// name reduce_pct — a wrong key name is silently absorbed by the sparse per-account merge.
package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDefaultDisciplineLadder 出厂阶梯：−3 减仓 / −6 清仓，且减仓严格轻于清仓。
func TestDefaultDisciplineLadder(t *testing.T) {
	d := DefaultDisciplineConfig()
	if d.ReducePct != 3 {
		t.Fatalf("减仓线出厂应为 3（owner 裁决「3 个点减仓」），实际 %v", d.ReducePct)
	}
	if d.StopLossPct != 6 {
		t.Fatalf("清仓线出厂应为 6（owner 裁决「6 个点清仓」），实际 %v", d.StopLossPct)
	}
	if d.ReducePct >= d.StopLossPct {
		t.Fatalf("减仓线必须严格轻于清仓线（%v >= %v）：撞线会让第一级永远不单独命中",
			d.ReducePct, d.StopLossPct)
	}
}

// TestDisciplineReducePctWireKey 键名锁：JSON 里必须是 reduce_pct（不是 reduce / reduce_line）。
// 稀疏 merge 下写错键名的表现是"配置里明明写了 2，运行时仍按 3 走"，且没有任何报错。
func TestDisciplineReducePctWireKey(t *testing.T) {
	b, err := json.Marshal(DefaultDisciplineConfig())
	if err != nil {
		t.Fatalf("纪律配置无法序列化: %v", err)
	}
	if !strings.Contains(string(b), `"reduce_pct":3`) {
		t.Fatalf("JSON 键名/取值不是 reduce_pct:3：%s", string(b))
	}
}
