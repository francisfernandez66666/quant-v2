// board_filter_test.go — 2026-11-10 owner 需求（需求6 板块过滤）统一判定函数的行为锁。
// StockBoardOf 是前后端共用口径的后端真源（前端镜像 web/src/utils/boardFilter.js），
// 前缀漂一个字就会出现"后端滤了前端放回"或反向漏网，所以逐格钉死。
package config

import "testing"

func TestStockBoardOf(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"688001.SH", "star"},
		{"688981", "star"},
		{"689009.SH", "star"},
		{"300750.SZ", "chinext"},
		{"301236", "chinext"},
		{"430047.BJ", "bse"},
		{"832566", "bse"},
		{"871981.SH", "bse"},
		{"873223", "bse"},
		{"889966", "bse"},
		{"920001", "bse"},
		{"600000.SH", ""}, // 主板
		{"000001.SZ", ""},
		{"002594.SZ", ""}, // 中小板并入主板
		{"", ""},
		{"SH", ""}, // 无数字前缀
	}
	for _, c := range cases {
		if got := StockBoardOf(c.code); got != c.want {
			t.Errorf("StockBoardOf(%q)=%q, want %q", c.code, got, c.want)
		}
	}
}

func TestBoardFilterAllows(t *testing.T) {
	// 全开（出厂态）：任何代码都放行
	all := DefaultBoardFilterConfig()
	if !all.Allows("688001.SH") || !all.Allows("300750.SZ") || !all.Allows("430047.BJ") || !all.Allows("600000.SH") {
		t.Fatal("出厂态三板块全开，任何代码都应放行")
	}
	// 关科创板：688/689 拦、其余放行
	only := BoardFilterConfig{StarEnabled: false, ChiNextEnabled: true, BseEnabled: true}
	if only.Allows("688001.SH") || only.Allows("689009") {
		t.Fatal("科创板关闭时 688/689 应被拦")
	}
	if !only.Allows("300750.SZ") || !only.Allows("600000.SH") || !only.Allows("430047.BJ") {
		t.Fatal("科创板关闭不应波及其他板块")
	}
	// 关北交所：4/8/92 开头拦
	bseOff := BoardFilterConfig{StarEnabled: true, ChiNextEnabled: true, BseEnabled: false}
	if bseOff.Allows("430047.BJ") || bseOff.Allows("832566") || bseOff.Allows("920001") {
		t.Fatal("北交所关闭时 4/8/92 开头应被拦")
	}
	// 关创业板
	cnOff := BoardFilterConfig{StarEnabled: true, ChiNextEnabled: false, BseEnabled: true}
	if cnOff.Allows("300750.SZ") || cnOff.Allows("301236") {
		t.Fatal("创业板关闭时 300/301 应被拦")
	}
}
