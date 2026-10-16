// dynsize_double_count_test.go — §DYN-SIZE §5「双重计数」门禁锁。
//
// 锁的内容（方案 §5 原文）：
//
//	同一因子不得同时出现在 ①动作降级 与 ②金额缩放；`AutoCautionOn` 与 `BuyScale` 不得相乘。
//
// 背景：风险档 Yellow 在两条链路上各有一处缩放——① §DYN-SIZE 定权把它作为**权重因子**乘进
// `BuyScale`（Yellow→0.60）；② §MARKET_RISK_GATE P4 的 auto-buy 谨慎层内联把金额乘 `YellowScale`。
// 两者都描述"同一个风险档"，连乘＝把同一个风险罚两次（方案 §5 明令禁止）。故执行层必须让位：
// `sizingOn=true` 时内联 Yellow 缩放**不生效**（`break`），`sizingOn=false` 时才生效。
//
// 本用例是那条互斥纪律的**唯一自动化回归**——它是"默认真拨之后金额会不会被罚两次"的直接证据。
// English: the §5 double-counting gate lock. Risk tier Yellow has two potential scaling sites — the
// §DYN-SIZE weight factor inside BuyScale, and the §MARKET_RISK_GATE inline Yellow scaling. They
// describe the same risk, so multiplying both penalises it twice; the execution layer must yield
// when dynamic sizing is on. This is the only automated regression pinning that mutual exclusion.
package engine

import (
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

// dynBoolPtr 本文件私有的 *bool 构造（避免与其它测试文件重名）。
func dynBoolPtr(b bool) *bool { return &b }

// TestDynSizeYellowScalingYieldsToBuyScale §5 双重计数锁：sizingOn 时内联 Yellow 缩放必须让位。
func TestDynSizeYellowScalingYieldsToBuyScale(t *testing.T) {
	// FixedAmount 取 30000：Yellow 默认 0.35 → 10500，仍高于单笔最低成交额（默认 5000），
	// 避免用例被"碎单拒买"闸误拦（那测的就不是本锁了）。
	const fixed = 30000.0
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.AutoRiskCaution = dynBoolPtr(true) // 显式开启 P4 谨慎层
		c.FixedAmount = fixed
	})
	// 风险档置 Yellow（内联缩放分支的触发条件）
	e.mu.Lock()
	e.lastRiskTier = combat_agent.RiskTierYellow
	e.lastMaxPosPct = 0 // 不叠加状态机仓位档，隔离出 YellowScale 本身的效果
	e.mu.Unlock()

	live := map[string]*data.StockInfo{
		"600001": {Code: "600001", Price: 10},
		"600002": {Code: "600002", Price: 10},
	}

	// ① sizingOn=true：风险档已进 BuyScale ⇒ 内联 Yellow 缩放让位，金额保持基准。
	//    buyScale 传 1.0（中性）以隔离出"内联缩放是否被跳过"这一件事。
	sigOn := combat_agent.Signal{ID: "SIG-ON", Code: "600001", Name: "甲", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sigOn, live, 1.0, true)
	if len(*orders) != 1 {
		t.Fatalf("sizingOn 应下 1 单, got %d", len(*orders))
	}
	qtyOn := (*orders)[0]["qty"].(float64)
	if qtyOn != fixed/10 {
		t.Fatalf("sizingOn 时内联 Yellow 缩放必须让位：期望 %v 股（=基准 %v），实得 %v 股",
			fixed/10, fixed, qtyOn)
	}

	// ② sizingOn=false：内联 Yellow 缩放正常生效（回归腿——证明①的"不缩放"不是因为闸坏了）。
	sigOff := combat_agent.Signal{ID: "SIG-OFF", Code: "600002", Name: "乙", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlaceScaled(sigOff, live, 0, false)
	if len(*orders) != 2 {
		t.Fatalf("sizingOn=false 应再下 1 单, got %d", len(*orders))
	}
	qtyOff := (*orders)[1]["qty"].(float64)
	// YellowScale 默认 0.35：30000×0.35 = 10500 元 → 1050 股 → 整手折算 1000 股
	yellowAmount := fixed * 0.35
	wantOff := float64(int(yellowAmount/10) / 100 * 100)
	if qtyOff != wantOff {
		t.Fatalf("sizingOn=false 时内联 Yellow 缩放应生效：期望 %v 股（基准 %v×0.35），实得 %v 股",
			wantOff, fixed, qtyOff)
	}
	// 反证：两条腿的股数必须不同，否则本用例什么都没锁住。
	if qtyOn == qtyOff {
		t.Fatalf("两条腿股数相同（%v），用例失去区分力——互斥纪律未被真正验证", qtyOn)
	}
}

// TestDynSizeRedStillRejectedWhenSizingOn §5 附带断言：Red 的第二道闸不被互斥逻辑误关。
// 方案原文：Red 拒开保留为无害的第二道保险（sizing 开启时 Red 已被硬拦在上游，此处不该反而放行）。
func TestDynSizeRedStillRejectedWhenSizingOn(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) {
		c.AutoRiskCaution = dynBoolPtr(true)
		c.FixedAmount = 30000
	})
	e.mu.Lock()
	e.lastRiskTier = combat_agent.RiskTierRed
	e.mu.Unlock()

	sig := combat_agent.Signal{ID: "SIG-RED", Code: "600003", Name: "丙", Strategy: "龙头", Direction: "做多", Price: 10}
	live := map[string]*data.StockInfo{"600003": {Code: "600003", Price: 10}}
	e.autoPlaceScaled(sig, live, 1.0, true) // 即便 sizingOn=true，Red 仍应被拒
	if len(*orders) != 0 {
		t.Fatalf("Red 档在 sizingOn=true 时仍应拒开新仓, got %d 单", len(*orders))
	}
}
