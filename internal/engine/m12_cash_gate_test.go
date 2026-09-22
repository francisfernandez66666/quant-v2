// §M12-A（2026-09-22 PM 修复批，owner 裁决 A）资金三态 fail-close 行为测试。
// 缺陷原文：AvailableCash 旧两态口径把「账本真 0」「口径不可得（未接库/查询失败/回报超 30 分钟）」
// 混写成同一个返回值——消费端 `if cash > 0` 判空即跳过资金降档门控：
//   - 冻结碎钱（H-4 实录的 0.03）落进 cash>0 分支 → 当日买入全拦；
//   - 冻结值恰为 0 / 回报断供 → 「不设限」全量放行，引擎在无任何资金约束下继续下单。
//
// 修法断言的三态：fresh 真值→按可负担整手降档；fresh 真 0→「买不起一手」拒单；
// stale/未接账本→整腿 fail-close 不自动买（本文件核心断言：0 张委托抵达网关）。
// English: §M12-A behavior tests — three cash states drive three outcomes:
// fresh-value downgrades to affordable lots, fresh-zero rejects via the one-lot floor,
// and unknown/stale fail-closes the whole auto-buy leg (zero gateway orders).
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// m12BuySig 构造可通过 autoPlace 前置守卫的买入信号（每用例独立代码，避免跨用例幂等键互踩）。
// 不带 StrategyID：走 §SIGNAL_CONTROLLER 内建四形态默认准入（与 TestAutoPlacePlacesOrder 同型），
// 本测试断言的是资金三态，不是战法准入。
func m12BuySig(code string) combat_agent.Signal {
	return combat_agent.Signal{
		ID: "SIG-" + code, Code: code, Name: "测试股", Strategy: "龙头",
		Direction: "做多", Price: 10,
	}
}

func m12Live(code string) map[string]*data.StockInfo {
	return map[string]*data.StockInfo{code: {Code: code, Price: 10}}
}

// m12SeedCash 播种网关对账回报（real_account）；updated 距 now 的 age 决定新鲜/过期。
// 时刻口径须与 cntime.Loc 一致（§CI 教训：UTC 机器上 8h 偏移会把新鲜值误判成过期）。
func m12SeedCash(t *testing.T, db *store.DB, cash float64, age time.Duration) {
	t.Helper()
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_1", AvailableCash: cash,
		UpdatedAt: time.Now().In(cntime.Loc).Add(-age).Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

func TestAutoPlaceCashThreeStates(t *testing.T) {
	// ①口径不可得（回报行存在但从未带过合法时间戳=等同无有效对账；harness 已播种的大钱行被覆写）
	// → fail-close：一笔都不许到网关。旧行为此处返回 0 → 「不设限」，10000 预算会整手全额下单——
	// 正是被推翻的放行分支。
	t.Run("unknown_fail_close", func(t *testing.T) {
		e, db, _, orders := newQMTEngine(t, nil)
		if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_1", AvailableCash: 0, UpdatedAt: ""}); err != nil {
			t.Fatalf("seed empty: %v", err)
		}
		e.autoPlace(m12BuySig("600901"), m12Live("600901"))
		if len(*orders) != 0 {
			t.Fatalf("资金口径不可得时自动买入必须 fail-close，实际到单 %d 笔", len(*orders))
		}
	})
	// ②过期回报（40 分钟前的碎钱 0.03，H-4 现场形态）→ 同样 fail-close。
	// 旧行为：过期→0→不设限→全额 1000 股放行；中间态碎钱→全拦。两头都是事故。
	t.Run("stale_fail_close", func(t *testing.T) {
		e, db, _, orders := newQMTEngine(t, nil)
		m12SeedCash(t, db, 0.03, 40*time.Minute)
		e.autoPlace(m12BuySig("600902"), m12Live("600902"))
		if len(*orders) != 0 {
			t.Fatalf("回报过期时自动买入必须 fail-close，实际到单 %d 笔", len(*orders))
		}
	})
	// ③新鲜真 0 → 走「买不起一手」拒单出口（而非旧口径的"不设限"全额放行）。
	t.Run("fresh_zero_rejects", func(t *testing.T) {
		e, db, _, orders := newQMTEngine(t, nil)
		m12SeedCash(t, db, 0, time.Minute)
		e.autoPlace(m12BuySig("600903"), m12Live("600903"))
		if len(*orders) != 0 {
			t.Fatalf("新鲜真 0 资金不得被当成不设限，实际到单 %d 笔", len(*orders))
		}
	})
	// ④新鲜有值 → 既有 §UAT-CASH 降档语义原样保留：5000 现金买 10 元股降到 400 股
	// （5000×0.994=4970 余量后可负担 4 手），预算 10000 全额 1000 股被降为 400。
	t.Run("fresh_value_downgrades", func(t *testing.T) {
		e, db, _, orders := newQMTEngine(t, nil)
		m12SeedCash(t, db, 5000, time.Minute)
		e.autoPlace(m12BuySig("600904"), m12Live("600904"))
		if len(*orders) != 1 {
			t.Fatalf("新鲜资金应降档下单一笔，实际 %d 笔", len(*orders))
		}
		if q := (*orders)[0]["qty"]; q != float64(400) {
			t.Fatalf("降档股数应为 400（5000 现金可负担整手），got %v", q)
		}
	})
	// ⑤回鲜恢复：过期 fail-close 后账本被新回报刷新 → 自动买入腿无需重启即恢复（stale 只是闸，不是熔断）。
	t.Run("recovery_after_fresh", func(t *testing.T) {
		e, db, _, orders := newQMTEngine(t, nil)
		m12SeedCash(t, db, 0.03, 40*time.Minute)
		e.autoPlace(m12BuySig("600905"), m12Live("600905"))
		if len(*orders) != 0 {
			t.Fatalf("播种过期时不应下单，实际 %d 笔", len(*orders))
		}
		m12SeedCash(t, db, 500000, time.Minute) // 对账回报恢复（UPSERT 覆盖同账号行）
		e.autoPlace(m12BuySig("600906"), m12Live("600906"))
		if len(*orders) != 1 {
			t.Fatalf("资金回鲜后应恢复下单，实际 %d 笔", len(*orders))
		}
	})
}
