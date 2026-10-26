// t_live_readmodel_test.go — §DYN-T 实盘回转的**账本侧读数面**行为锁（批 7~8 落码后补）。
//
// 这三样东西都是"别人据以判钱的读数"，所以每条锁都带一个反证方向：
//  1. EarliestBuyFillPrice：首买价必须来自 `fills_effective`（人工勘误已生效的视图）并且**忽略被勘误成卖出的行**。
//     反证方向：如果实现改读原始 fills，一笔被勘误的"记成买入的卖出"就会被写成锚点——
//     那是把一个根本没发生过的成交价当作风控基准，且事后从库里查不出来源（§FILL-AMEND 同族事故）。
//  2. AnchorTicket：来源不可证（cost_fallback/none/空）一律不给 T 资格。
//     反证方向：只判 `AnchorPrice>0` 的实现会让 W3 回填过的存量票全体获得开日内腿的许可。
//  3. TLegOrderLedger：委托笔数按**每一笔报出的委托**计（含撤后重报），不去重。
//     反证方向：去重后的数字好看但没有解释力——券商异常交易观察看的就是报单量。
//
// English: locks for the live round-trip read models. Each carries its wrong-direction counter-case:
// reading raw fills instead of the amendment view, granting a T ticket to a cost-derived anchor,
// or de-duplicating the submission counts that the compliance evidence is made of.
package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTLiveReadDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// putFill 直接经 ApplyRealFill 落一笔成交（回报通道是唯一生产写入口，不走裸 SQL 造数据）。
func putFill(t *testing.T, d *DB, f RealFill) {
	t.Helper()
	if err := d.ApplyRealFill(f); err != nil {
		t.Fatalf("apply fill %s: %v", f.OrderID, err)
	}
}

func TestEarliestBuyFillPrice(t *testing.T) {
	d := newTLiveReadDB(t)
	const day = "2026-10-20"
	// 同码三笔：更早的一买（9.80）、晚一点的一买（10.40）、一笔卖出。
	// 时间故意用 traded_at 递进而非插入顺序，否则"按 id 取最早"的实现也能碰巧通过。
	putFill(t, d, RealFill{OrderID: "GW-A", Code: "600000.SH", Side: "买入", Price: 10.40, Qty: 300,
		Amount: 3120, TradedAt: day + "T14:55:00+08:00", UserID: "u_1"})
	putFill(t, d, RealFill{OrderID: "GW-B", Code: "600000.SH", Side: "买入", Price: 9.80, Qty: 1000,
		Amount: 9800, TradedAt: day + "T09:31:00+08:00", UserID: "u_1"})
	putFill(t, d, RealFill{OrderID: "GW-C", Code: "600000.SH", Side: "卖出", Price: 8.00, Qty: 100,
		Amount: 800, TradedAt: day + "T09:30:10+08:00", UserID: "u_1"})

	px, ok := d.EarliestBuyFillPrice("600000")
	if !ok || px != 9.80 {
		t.Fatalf("首买价应取时间最早的买入成交 9.80，got price=%.4f ok=%v", px, ok)
	}
	// 带后缀写法必须等价：纸面账本给纯码、成交表存复合码，两种写法取不到同一个数
	// 就会让回填静默退到成本档（降级不报错，正是最难发现的那类）。
	if px2, ok2 := d.EarliestBuyFillPrice("600000.SH"); !ok2 || px2 != 9.80 {
		t.Fatalf("带后缀写法必须等价，got %.4f ok=%v", px2, ok2)
	}
	if _, ok := d.EarliestBuyFillPrice("600009"); ok {
		t.Fatalf("无历史买入的代码必须回 ok=false（不许伪造一个价）")
	}
	// 前缀邻居不得串味：600000.SH 绝不能被 6000001... 之类的码匹配上（LIKE 写法才会犯）。
	putFill(t, d, RealFill{OrderID: "GW-D", Code: "600001.SH", Side: "买入", Price: 5.00, Qty: 100,
		Amount: 500, TradedAt: day + "T09:30:00+08:00", UserID: "u_1"})
	if px3, _ := d.EarliestBuyFillPrice("600000"); px3 != 9.80 {
		t.Fatalf("跨码串味：600001 的首买价污染了 600000，got %.4f", px3)
	}
}

func TestRealPositionAnchorTicket(t *testing.T) {
	cases := []struct {
		name   string
		price  float64
		source string
		want   bool
	}{
		{"首成交建行可开 T", 10, AnchorSourceFirstBuyOrder, true},
		{"层账首买价可开 T", 10, AnchorSourceFirstLot, true},
		{"成本回落档不可开 T", 10, AnchorSourceCostFallback, false},
		{"明确无源不可开 T", 10, AnchorSourceNone, false},
		{"来源列上线前的存量行不可开 T", 10, "", false},
		{"来源可证但锚点为零仍不可开 T", 0, AnchorSourceFirstBuyOrder, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := RealPosition{TsCode: "600000.SH", Qty: 1000, AnchorPrice: c.price, AnchorSource: c.source}
			if got := p.AnchorTicket(); got != c.want {
				t.Fatalf("AnchorTicket()=%v 期望 %v（price=%.2f source=%q）", got, c.want, c.price, c.source)
			}
		})
	}
}

func TestTLegOrderLedgerCountsEverySubmission(t *testing.T) {
	d := newTLiveReadDB(t)
	today := time.Now().Format("2006-01-02")
	dayCompact := time.Now().Format("20060102")
	// 三条 T 腿委托：同一键撤后重报的两笔 + 一笔战略单（不得计入）。
	// qty/price 随意，本函数只数笔数——但状态必须覆盖到三个分类里。
	must := func(o RealOrder) {
		if _, err := d.UpsertRealOrder(o); err != nil {
			t.Fatalf("upsert order %s: %v", o.SignalID, err)
		}
	}
	key1 := "t0:600000:buy_first:" + dayCompact + ":1"
	must(RealOrder{OrderID: "GW-1", SignalID: key1,
		Code: "600000.SH", Side: "买入", Status: "已撤", Price: 10, Qty: 1000,
		CreatedAt: today + " 09:31:00", UserID: "u_1", IntradayT: true})
	must(RealOrder{OrderID: "GW-2", SignalID: key1 + ":p2",
		Code: "600000.SH", Side: "买入", Status: "部成", Price: 10, Qty: 1000,
		CreatedAt: today + " 09:36:00", UserID: "u_1", IntradayT: true})
	must(RealOrder{OrderID: "GW-3", SignalID: key1 + ":p",
		Code: "600000.SH", Side: "买入", Status: "已成", Price: 10, Qty: 500,
		CreatedAt: today + " 09:40:00", UserID: "u_1", IntradayT: true})
	must(RealOrder{OrderID: "GW-9", SignalID: "buy:600000.SH:龙头:" + today,
		Code: "600000.SH", Side: "买入", Status: "已成", Price: 10, Qty: 500,
		CreatedAt: today + " 09:41:00", UserID: "u_1"}) // 战略单：intraday_t=0，绝不进台账

	placed, canceled, filled, err := d.TLegOrderLedger("u_1", today)
	if err != nil {
		t.Fatalf("台账查询失败: %v", err)
	}
	if placed != 3 || canceled != 1 || filled != 2 {
		t.Fatalf("应按每一笔报出的委托计：期望 placed=3 canceled=1 filled=2，got %d/%d/%d", placed, canceled, filled)
	}
	// 前缀是约定、列是事实：造一条 signal_id 长得像 T 腿但没打标的委托，必须仍然不计入
	// （否则"手工塞一条 t0: 开头的战略单"就能污染报撤比证据）。
	must(RealOrder{OrderID: "GW-10", SignalID: "t0:600002:buy_first:x",
		Code: "600002.SH", Side: "买入", Status: "已报", Price: 10, Qty: 100,
		CreatedAt: today + " 09:42:00", UserID: "u_1"})
	if placed2, _, _, _ := d.TLegOrderLedger("u_1", today); placed2 != placed {
		t.Fatalf("台账必须只认 intraday_t 列而不是 signal_id 前缀，got placed=%d（原 %d）", placed2, placed)
	}
}
