// qmt_buy_floor_test.go — 买入侧两把新闸守护测试（owner 2026-10-12 裁决，反例锁）。
//
// 背景（2026-10-12 实盘事故）：09:54 五十余个买入信号瞬间打满日预算，后续信号被现金降档成
// 100~200 股的几块钱"杂毛碎单"堆仓；且全链路此前没有任何价格/成交额下限。
// 裁决两条：①现价低于最低买入价（出厂默认 5 元）不买；②单笔成交额=股价×**最终**买入股数
// 低于最低成交额（出厂默认 5000 元）不买——判据必须在现金降档**之后**（降档后的那单才是
// 真正报给柜台的）。两闸数值可配置并热生效（syncAccountConfig 每轮 QueueConfigUpdate）。
// 三态口径锁：0=出厂默认（存量账号快照缺键反序列化得 0，必须兜默认而非旁路）；
// >0=显式配置；<0=显式关闭本闸。摘锁反证：删掉 autoPlace 对应闸分支 → 对应腿转红。
//
// English: guard tests for the two owner-ruled buy gates (2026-10-12): a minimum price floor
// (default 5) and a per-order minimum notional floor (default 5000) checked on the FINAL lot
// count after the cash downgrade. Zero config falls back to the default (legacy snapshots lack
// the key); negative disables. Deleting the gate branch turns the matching legs red.
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// buyFloorSig 构造做多信号（autoPlace 最小输入）。
func buyFloorSig(code, name string, price float64) combat_agent.Signal {
	return combat_agent.Signal{ID: "SIG-FLOOR", Code: code, Name: name, Strategy: "龙头", Direction: "做多", Price: price}
}

// TestBuyFloorMinPriceDefaultRejectsJunk 低价闸默认开：现价 3.5 元（<5）直接拒买，不下任何单。
func TestBuyFloorMinPriceDefaultRejectsJunk(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	e.autoPlace(buyFloorSig("300001", "低价股", 3.5), map[string]*data.StockInfo{})
	if len(*orders) != 0 {
		t.Fatalf("低价杂毛股应被默认闸拦下，实得 %d 单: %+v", len(*orders), *orders)
	}
}

// TestBuyFloorMinPriceNegativeDisables 低价闸显式关闭（min_buy_price=-1）：同价恢复下单（对照腿）。
func TestBuyFloorMinPriceNegativeDisables(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(cfg *config.QMTConfig) { cfg.MinBuyPrice = -1 })
	e.autoPlace(buyFloorSig("300002", "低价股", 3.5), map[string]*data.StockInfo{})
	if len(*orders) != 1 {
		t.Fatalf("显式关闭低价闸后应放行 1 单，实得 %d", len(*orders))
	}
}

// TestBuyFloorMinPriceConfiguredThreshold 显式配置阈值（min_buy_price=8）：8 元整恰好放行、
// 7.9 元被拦——判据是"低于阈值"而非"小于等于"。
func TestBuyFloorMinPriceConfiguredThreshold(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(cfg *config.QMTConfig) { cfg.MinBuyPrice = 8 })
	e.autoPlace(buyFloorSig("600010", "压线股", 7.9), map[string]*data.StockInfo{})
	if len(*orders) != 0 {
		t.Fatalf("7.9 元低于阈值 8 应被拦，实得 %d 单", len(*orders))
	}
	e2, _, _, orders2 := newQMTEngine(t, func(cfg *config.QMTConfig) { cfg.MinBuyPrice = 8 })
	e2.autoPlace(buyFloorSig("600011", "压线股", 8.0), map[string]*data.StockInfo{})
	if len(*orders2) != 1 {
		t.Fatalf("8.0 元等于阈值应放行，实得 %d 单", len(*orders2))
	}
}

// TestBuyFloorMinOrderAmountDefaultRejectsCrumb 成交额闸默认开（§B0 起默认 1 万）：预算 6000 元、
// 现价 40 元 → 整手折算 100 股 = 4000 元 < 10000 → 拒买（旧口径会照报这笔碎单）。
// 注意：harness 统一把闸关掉了（`MinOrderAmount=-1`），本例显式写 0＝回到出厂默认。
func TestBuyFloorMinOrderAmountDefaultRejectsCrumb(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(cfg *config.QMTConfig) { cfg.FixedAmount = 6000; cfg.MinOrderAmount = 0 })
	e.autoPlace(buyFloorSig("600012", "碎单股", 40), map[string]*data.StockInfo{})
	if len(*orders) != 0 {
		t.Fatalf("成交额 4000<5000 应被默认闸拦下，实得 %d 单: %+v", len(*orders), *orders)
	}
}

// TestBuyFloorMinOrderAmountAfterCashDowngrade 关键口径腿：**现金降档之后**核算成交额——
// 预算 10000/现价 40 本应折 200 股（8000 元达标），但可用资金只有 6000 → 降档成 100 股
// （4000 元）→ 必须在降档后被闸拦下，而不是拿降档前的名义金额放行。
func TestBuyFloorMinOrderAmountAfterCashDowngrade(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(cfg *config.QMTConfig) { cfg.MinOrderAmount = 0 })
	// 覆写账户现金为 6000（harness 默认播种 1000 万；时刻口径与 cntime.Loc 一致保新鲜）。
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_1", AvailableCash: 6000,
		UpdatedAt: time.Now().In(cntime.Loc).Format("2006-01-02 15:04:05"),
	}); err != nil {
		t.Fatalf("seed cash: %v", err)
	}
	e.autoPlace(buyFloorSig("600013", "降档股", 40), map[string]*data.StockInfo{})
	if len(*orders) != 0 {
		t.Fatalf("降档后 100股×40=4000 元应被成交额闸拦下，实得 %d 单: %+v", len(*orders), *orders)
	}
}

// TestBuyFloorNormalOrderStillPasses 对照腿：价 10 元、预算 10000 → 1000 股 = 10000 元，
// 两闸全过 → 正常下单 1 笔（保证新闸没有误伤正常买单）。
func TestBuyFloorNormalOrderStillPasses(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	e.autoPlace(buyFloorSig("600014", "正常股", 10), map[string]*data.StockInfo{})
	if len(*orders) != 1 {
		t.Fatalf("正常买单不应被新闸误伤，实得 %d 单", len(*orders))
	}
	if o := (*orders)[0]; o["qty"].(float64) != 1000 {
		t.Fatalf("整手折算应为 1000 股，得 %v", o["qty"])
	}
}
