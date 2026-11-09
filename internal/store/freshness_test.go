// freshness_test.go — §LEDGER-FROZEN（2026-11-09 实锤后的判据收紧锁）。
//
// 锁 CounterPositionsReporting 的"全部非空行属今日"语义：旧实现"至少一行属今日"被
// 一笔后台任务把整本冻结账（停在 11-05）里的单行摸成当日时间戳打穿——两行 11-05 旧仓
// 被当作真柜台展示（owner 已清仓，屏上却挂着旧仓与旧余额）。帧式写入下健康回报本就
// 把全表一起戳新，"全部属今日"不冲突，却能抓住任何单行漂移。
package store

import "testing"

func TestCounterPositionsReportingAllRowsMustBeToday(t *testing.T) {
	day := "2026-11-09"
	cases := []struct {
		note string
		rows []RealPosition
		want bool
	}{
		{"全部属今日 ⇒ 在报数", []RealPosition{{UpdatedAt: "2026-11-09 10:00:00"}, {UpdatedAt: "2026-11-09 10:00:05"}}, true},
		{"混入一行昨日 ⇒ 不在报数（单行漂移必须被抓）", []RealPosition{
			{UpdatedAt: "2026-11-05T15:00:06+08:00"}, {UpdatedAt: "2026-11-09 14:15:14"}}, false},
		{"全部停在过去 ⇒ 不在报数", []RealPosition{{UpdatedAt: "2026-11-05 15:00:06"}}, false},
		{"零行 ⇒ 不在报数（空仓真值由资金帧承载）", nil, false},
		{"空串时间戳行被忽略，其余全今日 ⇒ 在报数", []RealPosition{{UpdatedAt: ""}, {UpdatedAt: "2026-11-09 10:00:00"}}, true},
		{"全空串 ⇒ 不在报数", []RealPosition{{UpdatedAt: ""}, {UpdatedAt: ""}}, false},
	}
	for _, tc := range cases {
		if got := CounterPositionsReporting(tc.rows, day); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.note, got, tc.want)
		}
	}
}
