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
