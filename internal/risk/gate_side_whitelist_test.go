// gate_side_whitelist_test.go — §N-4（2026-09-22 修复批，M-1 升级项）风控闸方向白名单 fail-close 单测。
//
// 缺陷原文：CheckLiveOrder 里多数闸按 `o.Side == SideBuy` / `o.Side != SideSell` 精确匹配区分方向，
// 传入 "buy"/"SELL"/" 买入"（带空格）等非法串时，同一道闸的"非买"与"非卖"两个分支同时不成立 →
// T+1 卖出限制、涨停拒买、跌停拒卖**三道方向闸一起静默跳过**（还捎带 ST/黑名单/买入纪律/集中度）。
// 修法是在唯一权威入口 CheckLiveOrder 开头把未知方向直接拒掉（fail-close）。
//
// 本测试因此不只看"被拒"这一件事，而是把**旧缺陷的具体形态**钉死：
// 每道方向闸在"非法方向 + 本该命中的场景"下必须先被 side_unknown 拦住，
// 而不是各自弃权放行。
//
// English: §N-4 regression — an unknown order side must be rejected fail-close at the single
// authoritative gate entry, proven against the concrete old failure shape (each directional gate
// abstaining on a scenario where it would have blocked a canonical side).
package risk

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// illegalSides 非法方向样本：英文大小写/首尾空格/简写/空串/任意噪声串。
// 空串也在列——闸口本身不做"缺省买入"（那是 HTTP 入口的兼容契约），到达闸口的订单必须已带明确方向。
var illegalSides = []string{"buy", "BUY", "SELL", "sell", " 买入", "买入 ", "买", "卖", "", "??? ", "long"}

// TestGateUnknownSideFailClosed §N-4 主断言：非法方向在入口即拒（Gate=side_unknown），
// 且即便全部闸关闭也一样拒（证明它先于闸清单，不依赖任何配置开关）。
func TestGateUnknownSideFailClosed(t *testing.T) {
	g := NewGate(gateDB(t), "u_side", nil)
	cfg := qmtCfg() // 默认零配置：其余闸全关（旧实现下非法方向会被"全部放行"）；§A5 常开的跌停卖闸对夹具单 fail-open（无昨收），不影响本用例
	for _, side := range illegalSides {
		v := g.CheckLiveOrder(cfg, liveOrder(side))
		if v.Pass {
			t.Fatalf("非法方向 %q 必须 fail-close 拒单（旧形态：所有方向闸静默跳过 → 放行）", side)
		}
		if !v.Blocked || v.Action != "block" {
			t.Fatalf("非法方向 %q 应为彻底阻断: %+v", side, v)
		}
		if v.Gate != "side_unknown" {
			t.Fatalf("非法方向 %q 命中闸标识应为 side_unknown, got %q", side, v.Gate)
		}
		if !strings.Contains(v.Reason, "非法下单方向") {
			t.Fatalf("拒单原因需自解释（供 risk_gates 留痕直读）, got %q", v.Reason)
		}
	}
	// 合法两值必须照常放行（白名单不能顺手把正常单也拦了）
	for _, side := range []string{SideBuy, SideSell} {
		if v := g.CheckLiveOrder(cfg, liveOrder(side)); !v.Pass {
			t.Fatalf("合法方向 %q 不应被白名单误拦: %+v", side, v)
		}
	}
}

// TestGateUnknownSideDoesNotSkipDirectionalGates §N-4 反例锁：把三道方向性闸各自"本该命中"的
// 场景配好，断言非法方向拿不到任何一道闸的跳过红利——且留下的命中留痕是 side_unknown 而不是空。
func TestGateUnknownSideDoesNotSkipDirectionalGates(t *testing.T) {
	db := gateDB(t)
	g := NewGate(db, "u_side", nil)
	cfg := qmtCfg()
	cfg.RiskGate.LimitUpBlockBuy = true    // 涨停拒买闸开
	cfg.RiskGate.LimitDownBlockSell = boolPtr(true) // 跌停拒卖闸开
	today := cntime.In(g.now()).Format("2006-01-02")

	// T+1 场景：当日买入 100 股（未结算不可卖），卖 100 股对合法方向必拦
	if err := db.ApplyRealFill(store.RealFill{OrderID: "OS-N4", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: today + " 09:35:00",
		SignalID: "SIG-N4-B", UserID: "u_side"}); err != nil {
		t.Fatalf("seed fill: %v", err)
	}

	// ① 卖出腿：合法"卖出"被 T+1 拦；非法"SELL"也必须被拒（且不得因为"非卖出"而跳过 T+1 后放行）
	sell := liveOrder(SideSell)
	sell.Qty = 100
	if v := g.CheckLiveOrder(cfg, sell); v.Pass || !strings.Contains(v.Reason, "T+1") {
		t.Fatalf("合法卖单应被 T+1 闸拦下, got %+v", v)
	}
	badSell := liveOrder("SELL")
	badSell.Qty = 100
	if v := g.CheckLiveOrder(cfg, badSell); v.Pass {
		t.Fatalf("非法方向的卖单不得享受 T+1 跳过红利, got %+v", v)
	} else if v.Gate != "side_unknown" {
		t.Fatalf("非法方向应命中 side_unknown 前置闸（而不是走到某道方向闸的 else 分支）, got %q", v.Gate)
	}

	// ② 买入腿：参考价 ≥ 涨停价，合法"买入"被拒买闸拦；非法"buy"同样必须被拒
	buy := liveOrder(SideBuy)
	buy.PrevClose = 10
	buy.Price = 11 // 主板涨停 10×1.10=11.00，等于涨停价即命中
	if v := g.CheckLiveOrder(cfg, buy); v.Pass || !strings.Contains(v.Reason, "涨停") {
		t.Fatalf("合法追买涨停应被拒, got %+v", v)
	}
	badBuy := liveOrder("buy")
	badBuy.PrevClose = 10
	badBuy.Price = 11
	if v := g.CheckLiveOrder(cfg, badBuy); v.Pass {
		t.Fatalf("非法方向的追买不得享受涨停拒买闸跳过红利, got %+v", v)
	}

	// ③ 卖出腿：参考价 ≤ 跌停价（换个代码避开 T+1 场景的干扰）
	badDown := liveOrder("Sell ")
	badDown.Code = "000001.SZ"
	badDown.PrevClose = 10
	badDown.Price = 9 // 主板跌停 10×0.90=9.00
	if v := g.CheckLiveOrder(cfg, badDown); v.Pass {
		t.Fatalf("非法方向的追卖不得享受跌停拒卖闸跳过红利, got %+v", v)
	}

	// ④ 留痕可观测：side_unknown 必须进 risk_gates（否则线上只表现为"下单被拒"无从归因）
	hits, err := db.RiskGateHits("u_side", today)
	if err != nil {
		t.Fatalf("risk gate hits: %v", err)
	}
	if hits["side_unknown"] < 3 {
		t.Fatalf("非法方向拒单应逐笔留痕（上面共 3 笔），got %v", hits)
	}
}

// TestGateUnknownSideAlerts §N-4：未知方向属于资金安全级异常，命中需走高优告警回调
// （与"新机构级闸"同一姿势），否则线上只会静默表现为下单被拒。
func TestGateUnknownSideAlerts(t *testing.T) {
	var level, title string
	g := NewGate(gateDB(t), "u_side", func(l, ti, _ string) { level, title = l, ti })
	if v := g.CheckLiveOrder(qmtCfg(), liveOrder("BUY")); v.Pass {
		t.Fatalf("非法方向应拒单: %+v", v)
	}
	if level != "high" || !strings.Contains(title, "风控闸") {
		t.Fatalf("非法方向应触发高优告警, got level=%q title=%q", level, title)
	}
}
