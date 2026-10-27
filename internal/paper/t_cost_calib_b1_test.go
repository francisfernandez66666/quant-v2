// t_cost_calib_b1_test.go — §T-COST B1（2026-10-27）纸面成本分解的行为锁。
//
// 断言范围（对应施工图 §2.4 判据 1/2/4）：
//  1. 分项自洽：|gross − fee_buy − fee_sell − stamp_tax − t_pnl_net| < 0.01；
//  2. 滑点符号：买腿"成交高于决策价"为正、卖腿"成交低于决策价"为正（正数＝对我不利）；
//  3. 分母口径：participation_pct 用**开腿时已累计**的当日成交额，不是全天量；
//  4. 日汇总的 cost 段与逐轮字段同源（同一批数据、同一口径）。
//
// 反证思路（写在注释里，不另造一条假红用例）：把 applyOpenLegCostLocked 里的 slip 计算删掉，
// 判据 2 立刻读到 0（卖腿也会被当成"零滑点"）；把 tRoundFeeSplitLocked 的印花税并进佣金，
// 判据 1 仍绿但"成本里最大项"的归因就错了——所以本用例同时断言三个分项都 > 0（配置费率时），
// 只验恒等式而不验分项归零的测试抓不到这种"分项搬错家"。
package paper

import (
	"math"
	"testing"
)

// TestTRoundCostBreakdownSelfConsistent 正 T 完成一轮后，分项自洽且滑点/参与率口径正确。
func TestTRoundCostBreakdownSelfConsistent(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.mu.Lock()
	e.cfg.CommissionRate, e.cfg.StampTaxRate, e.cfg.MinCommission = 0.00025, 0.0005, 5
	e.cfg.SlippageBps = 5 // 买腿成交价 = 决策价 × 1.0005 ⇒ 滑点应≈+5bp（不利）
	e.mu.Unlock()

	qOpen := tQuote("300001", 9.4, 10) // 决策价 9.4，开正 T 买腿
	e.RunIntradayT(qOpen, tParams())
	if len(e.tRounds) != 1 {
		t.Fatalf("用例前置：应开一条正 T 腿，got %+v", e.tRounds)
	}
	open := e.tRounds[0]
	if math.Abs(open.DecisionPrice-9.4) > 1e-9 {
		t.Fatalf("decision_price 必须是开腿那一刻的报价 9.4，got %.6f", open.DecisionPrice)
	}
	if open.SlipBps <= 0 {
		t.Fatalf("买腿成交价高于决策价应记正滑点（不利），got %.4f", open.SlipBps)
	}
	if open.FeeBuy <= 0 || open.FeeSource != tRFeeSourceEstimate {
		t.Fatalf("开腿即应落首腿佣金与 estimate 来源，got fee_buy=%.4f source=%q", open.FeeBuy, open.FeeSource)
	}
	if open.ParticipationPct <= 0 {
		t.Fatalf("参与率必须有读数（分母＝开腿时累计成交额），got %.6f", open.ParticipationPct)
	}
	// 分母口径锁：用**开腿时**的 q.Amount 复算，必须与落库值一致（换成全天量会读到另一个数）。
	vol, v := 8e6, 10.0
	wantPart := round4(open.LegPrice * float64(open.LegQty) / (v * vol) * 100)
	if math.Abs(open.ParticipationPct-wantPart) > 1e-9 {
		t.Fatalf("participation_pct 分母应取当时累计成交额：want %.6f got %.6f", wantPart, open.ParticipationPct)
	}

	e.RunIntradayT(tQuote("300001", 9.8, 10), tParams()) // 配对卖出腿
	r := e.tRounds[0]
	if r.State != tRStateDone {
		t.Fatalf("用例前置：应完成一轮，got %+v", r)
	}
	if r.FeeBuy <= 0 || r.FeeSell <= 0 || r.StampTax <= 0 {
		t.Fatalf("三个费用分项都应 > 0（费率已开）：buy=%.4f sell=%.4f stamp=%.4f",
			r.FeeBuy, r.FeeSell, r.StampTax)
	}
	diff := math.Abs(r.TPnlGross - r.FeeBuy - r.FeeSell - r.StampTax - r.TPnlNet)
	if diff >= 0.01 {
		t.Fatalf("分项自洽失败：gross=%.4f fee_buy=%.4f fee_sell=%.4f stamp=%.4f net=%.4f diff=%.6f",
			r.TPnlGross, r.FeeBuy, r.FeeSell, r.StampTax, r.TPnlNet, diff)
	}

	st := e.TRoundDay("")
	if math.Abs(st.Cost.Gross-r.TPnlGross) > 1e-9 || math.Abs(st.Cost.Net-r.TPnlNet) > 1e-9 {
		t.Fatalf("日汇总毛/净应与逐轮同源：cost=%+v round gross=%.4f net=%.4f", st.Cost, r.TPnlGross, r.TPnlNet)
	}
	if st.Cost.Fills != 2 {
		t.Fatalf("done 轮应记 2 条成交腿，got %d", st.Cost.Fills)
	}
	if st.Cost.SlipCost <= 0 || st.Cost.ParticipationPctMax <= 0 {
		t.Fatalf("日汇总滑点成本/参与率应有读数：%+v", st.Cost)
	}
	if math.Abs(st.Cost.FeeEstimateShare-1) > 1e-9 {
		t.Fatalf("纸面费用全部估算，估算占比应为 1，got %.4f", st.Cost.FeeEstimateShare)
	}
}

// TestTRoundSlipSignByLeg 滑点符号的两向锁：买腿向上成交=正、卖腿向下成交=正（都是不利方向）。
func TestTRoundSlipSignByLeg(t *testing.T) {
	if got := tSlipBpsOf(tRLegBuy, 10, 10.01); math.Abs(got-10) > 1e-6 {
		t.Fatalf("买腿成交高于决策价 0.1%% 应≈+10bp，got %.4f", got)
	}
	if got := tSlipBpsOf(tRLegBuy, 10, 9.99); got >= 0 {
		t.Fatalf("买腿成交低于决策价是有利方向，应记负滑点，got %.4f", got)
	}
	if got := tSlipBpsOf(tRLegSell, 10, 9.99); math.Abs(got-10) > 1e-6 {
		t.Fatalf("卖腿成交低于决策价 0.1%% 应≈+10bp（不利），got %.4f", got)
	}
	if got := tSlipBpsOf(tRLegSell, 10, 10.01); got >= 0 {
		t.Fatalf("卖腿成交高于决策价是有利方向，应记负滑点，got %.4f", got)
	}
}

// TestTRoundCostReverseSellFirst 反 T 侧：首腿是卖出，成交价被滑点压低 ⇒ slip_bps 同样为正；
// 印花税只出现在卖出腿（首腿），买回腿只有佣金。
func TestTRoundCostReverseSellFirst(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.mu.Lock()
	e.cfg.CommissionRate, e.cfg.StampTaxRate, e.cfg.MinCommission = 0.00025, 0.0005, 5
	e.cfg.SlippageBps = 5 // 卖腿成交价 = 决策价 × 0.9995 ⇒ 应≈+5bp（不利）
	e.mu.Unlock()
	e.RunIntradayT(tQuote("300001", 10.55, 10), tParams()) // 反 T 卖腿
	if len(e.tRounds) != 1 || e.tRounds[0].Leg != tRLegSell {
		t.Fatalf("用例前置：应开反 T 卖腿，got %+v", e.tRounds)
	}
	r := e.tRounds[0]
	if r.DecisionPrice <= 0 || r.SlipBps <= 0 {
		t.Fatalf("反 T 首腿应落决策价与正滑点：decision=%.4f slip=%.4f", r.DecisionPrice, r.SlipBps)
	}
	if r.StampTax <= 0 || r.FeeSell <= 0 {
		t.Fatalf("卖出首腿必须有印花税与佣金：stamp=%.4f fee_sell=%.4f", r.StampTax, r.FeeSell)
	}
}
