package data

import "testing"

func TestComputeOrderFlow(t *testing.T) {
	cur := OrderLevels{BidVols: []float64{100, 80, 60, 40, 20}, AskVols: []float64{30, 30, 30, 30, 30}}
	of := ComputeOrderFlow(cur, nil)
	// 委比 = (300-150)/450 = 0.333
	if of.Imbalance < 0.33 || of.Imbalance > 0.34 {
		t.Errorf("imbalance want ~0.333 got %.4f", of.Imbalance)
	}
	if of.ActiveBuyRatio != 0.5 {
		t.Errorf("no prev -> neutral 0.5, got %.2f", of.ActiveBuyRatio)
	}
	// 大单方向：买一100 vs 卖一30 → 强正值
	if of.BigNetInflow <= 0 {
		t.Errorf("bid1>>ask1 should be positive inflow, got %.3f", of.BigNetInflow)
	}
}

func TestComputeOrderFlow_WithPrev(t *testing.T) {
	prev := OrderLevels{BidVols: []float64{90, 70, 50, 30, 10}, AskVols: []float64{40, 40, 40, 40, 40}}
	cur := OrderLevels{BidVols: []float64{140, 80, 60, 40, 20}, AskVols: []float64{30, 30, 30, 30, 30}}
	of := ComputeOrderFlow(cur, &prev)
	// Δ买=90，Δ卖=-40 → activeBuy = 90/(90-40)= 0.9
	if of.ActiveBuyRatio < 0.8 {
		t.Errorf("strong buy-add should raise active-buy, got %.2f", of.ActiveBuyRatio)
	}
}

// TestTrigger 覆盖默认阈值下的三种盘口形态：持续买压触发买入、
// 卖压叠加封单撤减触发卖出预警、温和波动两边都不触发（防噪声下单）。
func TestTrigger(t *testing.T) {
	buy := Trigger(OrderFlow{Imbalance: 0.6, ActiveBuyRatio: 0.8, BigNetInflow: 0.5}, TriggerDefaults)
	if !buy.Buy {
		t.Errorf("strong buy pressure should trigger buy: %+v", buy)
	}
	sell := Trigger(OrderFlow{Imbalance: -0.6, ActiveBuyRatio: 0.2, BigNetInflow: -0.5}, TriggerDefaults)
	if !sell.Sell {
		t.Errorf("strong sell pressure should trigger sell-warning: %+v", sell)
	}
	none := Trigger(OrderFlow{Imbalance: 0.05, ActiveBuyRatio: 0.5, BigNetInflow: 0.05}, TriggerDefaults)
	if none.Buy || none.Sell {
		t.Errorf("mild book must not trigger: %+v", none)
	}
}

func TestTrigger_ZeroCfgFallsBack(t *testing.T) {
	res := Trigger(OrderFlow{Imbalance: 0.9, ActiveBuyRatio: 0.9, BigNetInflow: 0.9}, TriggerRule{})
	if !res.Buy {
		t.Errorf("zero cfg should fall back to defaults and still fire: %+v", res)
	}
}

// TestSmooth 校验滑窗均值：委比/量能为逐帧算术平均，空切片必须回零值，
// 不能让差分噪声直接透传到触发判定里。
func TestSmooth(t *testing.T) {
	frames := []OrderFlow{
		{Imbalance: 0.4, BigNetInflow: 0.2, ActiveBuyRatio: 0.6, TotalVol: 100},
		{Imbalance: 0.6, BigNetInflow: 0.4, ActiveBuyRatio: 0.8, TotalVol: 200},
		{Imbalance: 1.0, BigNetInflow: 0.6, ActiveBuyRatio: 1.0, TotalVol: 300},
	}
	s := Smooth(frames)
	if s.Imbalance < 0.66 || s.Imbalance > 0.67 {
		t.Errorf("smoothed imbalance want ~2/3 got %.3f", s.Imbalance)
	}
	if s.TotalVol != 200 {
		t.Errorf("smoothed total want 200 got %.1f", s.TotalVol)
	}
	if Smooth(nil).TotalVol != 0 {
		t.Errorf("empty smooth must be zero")
	}
}

// TestFromBook 校验五档抽取的降级边界：只取买/卖各前 5 档、档位数量按实到长度截断，
// 空盘口（nil）返回零值骨架而不是 panic，供上游继续差分。
func TestFromBook(t *testing.T) {
	ob := &OrderBook{
		Bids: []OrderLevel{{Price: 10, Volume: 5}, {Price: 9.9, Volume: 4}},
		Asks: []OrderLevel{{Price: 10.1, Volume: 3}},
	}
	lv := FromBook(ob)
	if len(lv.BidVols) != 2 || len(lv.AskVols) != 1 {
		t.Errorf("levels mismatch: bids=%d asks=%d", len(lv.BidVols), len(lv.AskVols))
	}
	if lv.BidVols[0] != 5 {
		t.Errorf("bid1 vol want 5 got %v", lv.BidVols[0])
	}
	if len(FromBook(nil).BidVols) != 0 || len(FromBook(nil).AskVols) != 0 {
		t.Errorf("nil book should be empty")
	}
}
