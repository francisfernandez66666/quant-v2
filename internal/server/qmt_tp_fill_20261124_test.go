// internal/server/qmt_tp_fill_20261124_test.go — §TP-LADDER 卖出成交归因锁（2026-11-24）。
//
// 为什么这条链值钱：载体卖出原来**恒为空 signal_id**（补链只认买入），于是云端无法证明
// "这只票的另一半今天已经卖过"。载体重启/投递后 `_STATE` 清空、烘入的 tp_plan 又是按
// 当日 fills 重放的——空归因就会把 Stage 1 的票重放成 Stage 0，在止盈点上**再卖一次一半**。
//
// 本文件只锁三件纯逻辑（不打库）：
//  1. 短码表必须同时登记 TP 与 TT，否则 `carrierRemarkStrategy` 认不出载体卖出的战法名；
//  2. 备注反解只认「短码 + 全数字」的连字符尾巴：版本段/日期段/fac_ 段都不能被当成短码，
//     带尾巴的怪串（`TP600519x`）必须认不出（未知不冒充）；
//  3. 归因键的日键取自**成交时刻**（跨日时段唯一的阶段证据），日期形态不对就不补。
//
// English: pure-logic locks for attributing carrier sell fills to the TP/TT ladder legs.
package server

import "testing"

// TestCarrierSignalCodesHasBothTPLegs TT 必须与 TP 同表登记（§179 静态锁也逐键核对
// local_executor_params.json 的 signal_codes；这里锁的是 Go 侧那一半）。
func TestCarrierSignalCodesHasBothTPLegs(t *testing.T) {
	if got := carrierSignalCodes["TP"]; got != "take_profit" {
		t.Fatalf("TP must map to take_profit, got %q", got)
	}
	if got := carrierSignalCodes["TT"]; got != "take_profit_clear" {
		t.Fatalf("TT must map to take_profit_clear, got %q", got)
	}
	if carrierSignalCodes["TT"] == carrierSignalCodes["TP"] {
		t.Fatal("the two ladder legs must stay distinguishable in the remark vocabulary")
	}
}

// TestCarrierRemarkShortCode 备注反解：只认载体自己写的那一段。
func TestCarrierRemarkShortCode(t *testing.T) {
	cases := []struct {
		remark string
		want   string
	}{
		{"v168-20261124-TP600519", "TP600519"},
		{"v168-20261124-TT600519", "TT600519"},
		{"v168-20261124-BK000797", "BK000797"},
		{"v168-20261124-fac_160-FC000034", "FC000034"},
		{"v168-20261124-LD1600362", "LD1600362"}, // 最长前缀：LD1 压过 LD
		// 负锁：日期段/版本段/规则号段都不是短码；尾巴不是纯数字一律认不出。
		{"v168-20261124-600519", ""},
		{"v168-20261124", ""},
		{"v168-20261124-TP600519x", ""},
		{"", ""},
		{"manual-app-buy", ""},
	}
	for _, tc := range cases {
		if got := carrierRemarkShortCode(tc.remark); got != tc.want {
			t.Fatalf("carrierRemarkShortCode(%q) = %q, want %q", tc.remark, got, tc.want)
		}
	}
}

// TestCarrierTPLegOfShort 只有两条止盈腿有阶梯身份。
func TestCarrierTPLegOfShort(t *testing.T) {
	cases := map[string]string{
		"TP600519": "tp",
		"TT600519": "tt",
		// 其他卖出短码不写假身份（它们是清仓/减仓/破位，不是"卖了一半"）。
		"SL600519": "",
		"BK600519": "",
		"RD600519": "",
		"BD600519": "",
		"FC600519": "",
		"":         "",
	}
	for short, want := range cases {
		if got := carrierTPLegOfShort(short); got != want {
			t.Fatalf("carrierTPLegOfShort(%q) = %q, want %q", short, got, want)
		}
	}
}

// TestCarrierSellFillSignalID 日键必须来自成交时刻，且坏日期不许落键。
func TestCarrierSellFillSignalID(t *testing.T) {
	// 成交是昨天 14:20（载体回报往往在收盘后），键必须写昨天。
	if got, want := carrierSellFillSignalID("600519", "tp", "2026-11-23 14:20:00"),
		"sell:600519:tp:2026-11-23"; got != want {
		t.Fatalf("sell fill key = %q, want %q (the day must come from the trade, not the clock)", got, want)
	}
	if got, want := carrierSellFillSignalID("000797", "tt", "2026-11-24 10:10:31"),
		"sell:000797:tt:2026-11-24"; got != want {
		t.Fatalf("sell fill key = %q, want %q", got, want)
	}
	// 认不出腿/码，或时间没归一成日期，一律不补（空归因不等于假归因）。
	for _, tc := range []struct{ code, leg, at string }{
		{"600519", "", "2026-11-24 10:10:31"},
		{"", "tp", "2026-11-24 10:10:31"},
		{"600519", "tp", "10:10:31"},
		{"600519", "tp", ""},
		{"600519", "tp", "2026/11/24 10:10:31"},
	} {
		if got := carrierSellFillSignalID(tc.code, tc.leg, tc.at); got != "" {
			t.Fatalf("carrierSellFillSignalID(%q,%q,%q) = %q, want empty",
				tc.code, tc.leg, tc.at, got)
		}
	}
}
