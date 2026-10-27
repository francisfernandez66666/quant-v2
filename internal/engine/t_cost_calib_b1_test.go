// t_cost_calib_b1_test.go — §T-COST B1（2026-10-27）实盘账本侧的行为锁。
//
// 三条断言：
//  1. 两本账字段同集：B1 的十个字段在 paper.TRound 与 engine.TRoundLive 上 json tag 逐字相等
//     （防止"纸面能算、实盘算不了"的半截实现——那类分叉在复盘时最难发现）；
//  2. 实盘滑点符号与纸面同义：买腿向上成交为正、卖腿向下成交为正（正数＝对我不利）；
//  3. 日汇总 cost 段分项自洽：|gross − 三个分项 − net| < 0.01（用一条构造的 done 轮验证）。
//
// 反证思路（注释即判据）：把 refreshSlip 的方向取反 ⇒ 判据 2 红；把 markUnfilled 删掉 ⇒
// 终态零成交腿在接口上会伪装成"正常收摊"（Unfilled=false），本文件第 4 条用例会红。
package engine

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
)

// b1CostFields B1 新增的十个字段（Go 字段名；json tag 由 reflect 读出比对）。
var b1CostFields = []string{
	"DecisionPrice", "SlipBps", "FeeBuy", "FeeSell", "StampTax",
	"FeeSource", "LatencyMs", "Unfilled", "UnfilledReason", "ParticipationPct",
}

// jsonTagOf 取结构体某导出字段的 json tag（只取逗号前的名字）。
func jsonTagOf(v interface{}, field string) (string, bool) {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	f, ok := t.FieldByName(field)
	if !ok {
		return "", false
	}
	tag := f.Tag.Get("json")
	name := strings.Split(tag, ",")[0]
	return name, name != "" && name != "-"
}

// TestTRoundCostFieldsParity 两本账的 B1 字段 json tag 逐字相等。
func TestTRoundCostFieldsParity(t *testing.T) {
	var p paper.TRound
	var l TRoundLive
	for _, f := range b1CostFields {
		pt, okP := jsonTagOf(p, f)
		lt, okL := jsonTagOf(l, f)
		if !okP || !okL {
			t.Fatalf("字段 %s 缺账：paper=%v live=%v（两本账必须同名同义）", f, okP, okL)
		}
		if pt != lt {
			t.Fatalf("字段 %s 的 json tag 不一致：paper=%q live=%q（改一边必须同批改另一边）", f, pt, lt)
		}
	}
}

// TestTLiveSlipSignByLeg 实盘首腿滑点符号：与 paper.tSlipBpsOf 同义（正数＝不利）。
func TestTLiveSlipSignByLeg(t *testing.T) {
	r := &tLiveRound{}
	buy := &TRoundLive{Dir: tLDirBuy, DecisionPrice: 10, BuyFilled: 100, BuyPrice: 10.01}
	r.refreshSlip(buy)
	if math.Abs(buy.SlipBps-10) > 1e-6 {
		t.Fatalf("买腿成交高于决策价 0.1%% ⇒ 应≈+10bp，got %.4f", buy.SlipBps)
	}
	sell := &TRoundLive{Dir: tLDirSell, DecisionPrice: 10, SellFilled: 100, SellPrice: 9.99}
	r.refreshSlip(sell)
	if math.Abs(sell.SlipBps-10) > 1e-6 {
		t.Fatalf("卖腿成交低于决策价 0.1%% ⇒ 应≈+10bp（不利），got %.4f", sell.SlipBps)
	}
	// 零成交不写滑点：拿"没成交"算滑点等于凭空造读数。
	none := &TRoundLive{Dir: tLDirBuy, DecisionPrice: 10}
	r.refreshSlip(none)
	if none.SlipBps != 0 {
		t.Fatalf("零成交腿不得有滑点读数，got %.4f", none.SlipBps)
	}
}

// TestTLiveDailyCostSelfConsistent 日汇总 cost 段：分项自洽 + 估算占比 + 滑点只算首腿。
func TestTLiveDailyCostSelfConsistent(t *testing.T) {
	done := &TRoundLive{
		Dir: tLDirBuy, Status: "done", DecisionPrice: 10,
		BuyFilled: 500, BuyPrice: 10.005, SellFilled: 500, SellPrice: 10.2,
		TPnlGross: 97.5, FeeBuy: 1.25, FeeSell: 1.28, StampTax: 2.55,
		FeeSource: tLFeeSourceEstimate, ParticipationPct: 0.031,
	}
	done.TPnlNet = tLRound2(done.TPnlGross - done.FeeBuy - done.FeeSell - done.StampTax)
	cost := tLiveCostOf([]*TRoundLive{done})
	diff := math.Abs(cost.Gross - cost.FeeBuy - cost.FeeSell - cost.StampTax - cost.Net)
	if diff >= 0.01 {
		t.Fatalf("日汇总分项自洽失败：%+v diff=%.6f", cost, diff)
	}
	if cost.Fills != 2 || cost.FeeEstimateShare != 1 || cost.ParticipationPctMax != 0.031 {
		t.Fatalf("成交腿数/估算占比/参与率口径不对：%+v", cost)
	}
	// 滑点成本只算首腿：|10.005−10|×500 ≈ 2.5 元（配对腿的成交价不进这个数）。
	if math.Abs(cost.SlipCost-2.5) > 1e-6 {
		t.Fatalf("滑点成本应只按首腿算：want 2.5 got %.4f", cost.SlipCost)
	}
}

// TestTLiveUnfilledReason 零成交但下过单的腿必须给出原因码（撤/废/超时三档）。
func TestTLiveUnfilledReason(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"已撤", "canceled"},
		{"部成待撤", "canceled"},
		{"废单", "rejected"},
		{"已报", "timeout"},
	}
	for _, c := range cases {
		f := &tLiveLegFact{orders: []store.RealOrder{{Status: c.status}}}
		if got := tLUnfilledReasonOf(f); got != c.want {
			t.Fatalf("委托终态 %q 的原因码应为 %q，got %q", c.status, c.want, got)
		}
	}
}
