// qmt_ledger_frozen_test.go — §LEDGER-FROZEN-BANNER（2026-11-09 实锤后的诚实化）行为锁。
//
// counterFrameReadout 是持仓页「柜台数据冻结」横幅的唯一数据源：
//   - 两维时刻串有两种历史形态（live.db 旧行 ISO "2026-11-05T15:00:06+08:00"、
//     新行 "2026-11-09 14:15:14"），必须都能吃（当天实库里两种并存）；
//   - 取两维里**最新**的非空读数；全空 ⇒ 帧不存在（"" , false），绝不伪造时刻；
//   - 最新读数不属于当前交易日 ⇒ frozen=true（11-09 实锤：整本冻结在 11-05 的账
//     被一行当日触碰顶出假新鲜，横幅就是为这个形态挂的）。
package server

import (
	"testing"

	"quant-trading-v2/internal/store"
)

func TestCounterFrameReadout(t *testing.T) {
	day := "2026-11-09"
	cases := []struct {
		note       string
		acc        store.RealAccount
		positions  []store.RealPosition
		wantAt     string
		wantFrozen bool
	}{
		{"两维全空 ⇒ 帧不存在", store.RealAccount{}, nil, "", false},
		{"冻结账：资金帧停在过去", store.RealAccount{UpdatedAt: "2026-11-05 15:00:06"}, nil,
			"2026-11-05 15:00:06", true},
		{"单行当日触碰掩盖冻结账 ⇒ 仍判冻结（取更老的读数没有意义，取最新也要看属不属今日）",
			store.RealAccount{UpdatedAt: "2026-11-05 15:00:06"},
			[]store.RealPosition{{UpdatedAt: "2026-11-05T15:00:06+08:00"}, {UpdatedAt: "2026-11-09 14:15:14"}},
			"2026-11-09 14:15:14", true},
		{"两种时刻形态并存取最新", store.RealAccount{UpdatedAt: "2026-11-09 10:00:00"},
			[]store.RealPosition{{UpdatedAt: "2026-11-09T09:30:00+08:00"}},
			"2026-11-09 10:00:00", false},
		{"当日新鲜帧 ⇒ 不冻结", store.RealAccount{UpdatedAt: "2026-11-09 15:20:00"},
			[]store.RealPosition{{UpdatedAt: "2026-11-09 15:20:01"}}, "2026-11-09 15:20:01", false},
	}
	for _, tc := range cases {
		gotAt, gotFrozen := counterFrameReadout(tc.acc, tc.positions, day)
		if gotAt != tc.wantAt || gotFrozen != tc.wantFrozen {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", tc.note, gotAt, gotFrozen, tc.wantAt, tc.wantFrozen)
		}
	}
}
