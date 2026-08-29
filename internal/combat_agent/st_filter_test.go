// 文件：st_filter_test.go
// 包名：combat_agent
// 所属模块：「对抗式/量化交易决策 agent（买卖信号、风控）」
// 模块职责：本文件属于 对抗式/量化交易决策 agent（买卖信号、风控），负责该模块下的具体实现；
//           下文各函数/类型/方法均附有中文说明（用途、参数、返回值、副作用）。
// 说明：本文件仅补充注释，未改动任何原有代码逻辑。

package combat_agent

import "testing"

// TestIsSTStock 覆盖 ST/*ST/S*ST/SST/退市 前缀与正常个股。
// English: TestIsSTStock covers the ST/*ST/S*ST/SST/delisting prefixes and normal stocks.
func TestIsSTStock(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"ST围海", true},
		{"*ST海投", true},
		{"S*ST天海", true},
		{"SST明科", true},
		{"退市博元", true},
		{"退市整理股", true},
		{"平安银行", false},
		{"STOCK INC", false}, // 名称中间含 ST 不算（仅前缀匹配）
		{"", false},
	}
	for _, c := range cases {
		if got := IsSTStock(c.name); got != c.want {
			t.Errorf("IsSTStock(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
