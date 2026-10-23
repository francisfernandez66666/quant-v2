// qmt_freshness_test.go — v3 §8.7 桥快照新鲜度判定锁。
//
// English: locks for the bridge snapshot freshness decision (synced / data_ts age).
package server

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

func TestQMTReportFreshness(t *testing.T) {
	now := time.Now()
	old := now.Add(-20 * time.Minute).Format(time.RFC3339)
	recent := now.Add(-time.Minute).Format(time.RFC3339)
	unsynced := false
	synced := true

	cases := []struct {
		name  string
		ev    qmtReportEvent
		fresh bool
	}{
		{"旧桥无字段按旧口径放行", qmtReportEvent{}, true},
		{"显式未同步", qmtReportEvent{Synced: &unsynced}, false},
		{"显式同步", qmtReportEvent{Synced: &synced}, true},
		{"数据停滞 20 分钟", qmtReportEvent{DataTS: old}, false},
		{"数据 1 分钟前", qmtReportEvent{DataTS: recent}, true},
		{"同步但数据停滞", qmtReportEvent{Synced: &synced, DataTS: old}, false},
		{"data_ts 不可解析按旧口径", qmtReportEvent{DataTS: "not-a-time"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fresh, ts := qmtReportFreshness(c.ev, cntime.In(now))
			if fresh != c.fresh {
				t.Fatalf("fresh=%v want %v", fresh, c.fresh)
			}
			if ts.IsZero() {
				t.Fatalf("时间戳不得为零值")
			}
		})
	}
}
