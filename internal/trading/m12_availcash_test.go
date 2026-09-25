// §M12-A 资金三态单元锁：Controller.AvailableCash 的 (cash, fresh) 语义与 Snapshot 暴露。
// 断言四态：未接账本库 / 账本无行（查询失败）→ fresh=false；新鲜真值（含真 0）→ fresh=true；
// 回报超 30 分钟 → fresh=false 且原值随带（运维侧 cash_stale 横幅要能看到最近原始值）。
// 「真 0 与不可得不得混写」是本批的核心不变式——H-4 事故两头（全拦/全放）都源于旧两态折叠。
// English: §M12-A unit lock for the three-state cash basis and its snapshot exposure:
// nil-store / missing row / >30min stale ⇒ fresh=false; fresh payload (including a real 0) ⇒
// fresh=true; stale keeps carrying the raw last value for the frontend banner.
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

func TestAvailableCashThreeStates(t *testing.T) {
	cfg := config.DefaultQMTConfig()
	// ①未接账本库：口径不可得。
	ctrlNil := NewController(guardServer(), nil, "u_3s", cfg, nil)
	if cash, fresh := ctrlNil.AvailableCash(); cash != 0 || fresh {
		t.Fatalf("未接账本库应 (0,false)，got (%v,%v)", cash, fresh)
	}
	db := testDB(t)
	ctrl := NewController(guardServer(), db, "u_3s", cfg, nil)
	// ②账本无行（GetRealAccount 返回零值行、UpdatedAt 为空→解析失败）：同为口径不可得（而非"真 0"）。
	if cash, fresh := ctrl.AvailableCash(); cash != 0 || fresh {
		t.Fatalf("账本无行应 (0,false)，got (%v,%v)", cash, fresh)
	}
	seed := func(cash float64, age time.Duration) {
		t.Helper()
		if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_3s", AvailableCash: cash,
			UpdatedAt: time.Now().In(cntime.Loc).Add(-age).Format("2006-01-02 15:04:05")}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// ③新鲜真值 + 新鲜真 0：都属"可得"，0 的裁决交给消费端拒单腿。
	seed(500, time.Minute)
	if cash, fresh := ctrl.AvailableCash(); cash != 500 || !fresh {
		t.Fatalf("新鲜 500 应 (500,true)，got (%v,%v)", cash, fresh)
	}
	seed(0, time.Minute)
	if cash, fresh := ctrl.AvailableCash(); cash != 0 || !fresh {
		t.Fatalf("新鲜真 0 应 (0,true)（真零≠不可得），got (%v,%v)", cash, fresh)
	}
	// ④过期碎钱（H-4 现场形态）：fresh=false，但原值随带供前端横幅展示。
	seed(0.03, 40*time.Minute)
	cash, fresh := ctrl.AvailableCash()
	if cash != 0.03 || fresh {
		t.Fatalf("过期 0.03 应 (0.03,false)，got (%v,%v)", cash, fresh)
	}
	// ⑤Snapshot 暴露：cash_stale=true + 最近原始值（/api/qmt/state 的前端降级横幅数据源）。
	st := ctrl.Snapshot()
	if !st.CashStale || st.Cash != 0.03 {
		t.Fatalf("快照应 CashStale=true/Cash=0.03，got %+v", st)
	}
	seed(5000, time.Minute)
	st = ctrl.Snapshot()
	if st.CashStale || st.Cash != 5000 {
		t.Fatalf("回鲜后快照应 CashStale=false/Cash=5000，got %+v", st)
	}
}

// TestAvailableCashBeijingParseOnUTCHost §0925EVE-W3-F（⑰/D2 时间口径收口反证锁）：
// UpdatedAt 是网关按北京时间写入的墙钟串，新鲜度判据必须用 cntime.Loc 解析。本用例把
// time.Local 临时伪造成 UTC（模拟容器/云主机宿主时区），再喂 40 分钟前的北京墙钟串：
//   - 正确实现（cntime.Loc 解析）：判过期 fresh=false；
//   - 回归实现（time.Local 解析）：北京串被当 UTC 读成「未来 8 小时」，Since 为负恒判
//     新鲜，过期资金放行 fresh=true——正是 §M12-A fail-close 要拦的 H-4 同族形态。
// 即：删掉修复本测试必红（反证成立）。夹具不并发跑（包内无 t.Parallel，改全局
// time.Local 仅此一处，收尾必还原）。
// English: regression lock — with time.Local faked to UTC (container default), a 40-min-old
// Beijing wall-clock string must still be judged stale; the old time.Local parse would read it
// as ~8h in the future and wrongly report fresh=true.
func TestAvailableCashBeijingParseOnUTCHost(t *testing.T) {
	original := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = original })

	cfg := config.DefaultQMTConfig()
	db := testDB(t)
	ctrl := NewController(guardServer(), db, "u_tzpin", cfg, nil)
	seed := func(cash float64, age time.Duration) {
		t.Helper()
		// 北京时间视角的墙钟串（与网关写入侧 cntime 口径同源）
		if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_tzpin", AvailableCash: cash,
			UpdatedAt: time.Now().In(cntime.Loc).Add(-age).Format("2006-01-02 15:04:05")}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// 40 分钟前的北京串：宿主时区=UTC 下仍须判过期
	seed(100, 40*time.Minute)
	if _, fresh := ctrl.AvailableCash(); fresh {
		t.Fatal("UTC 宿主下 40 分钟前的北京回报被判 fresh=true——时间口径回退到 time.Local（⑰ 回归）")
	}
	// 5 分钟前的北京串：正常新鲜
	seed(200, 5*time.Minute)
	if cash, fresh := ctrl.AvailableCash(); !fresh || cash != 200 {
		t.Fatalf("5 分钟内应 (200,true)，got (%v,%v)", cash, fresh)
	}
}
