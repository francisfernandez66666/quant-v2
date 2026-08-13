package combat_agent

import "testing"

// TestIsSTStock 覆盖 ST/*ST/S*ST/SST/退市 前缀与正常个股。
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
		{"STOCK INC", false},   // 名称中间含 ST 不算（仅前缀匹配）
		{"", false},
	}
	for _, c := range cases {
		if got := IsSTStock(c.name); got != c.want {
			t.Errorf("IsSTStock(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}