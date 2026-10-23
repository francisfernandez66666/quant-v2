// ordbudget_test.go — 共享买入预算纯函数边界锁（v3 §9.1）。
//
// 覆盖：正常预算、行业/单票减额、1 手地板、地板不得突破额度、行业未知兜底、
// 现金压缩与费用缓冲、参数不可得、整手向下取整。
//
// English: boundary locks for the shared buy-budget pure function — normal sizing, industry/stock
// rooms, one-lot floor (which may never break a cap), unknown-industry fallback, cash compression,
// fee buffer, missing inputs, and whole-lot rounding.
package ordbudget

import (
	"math"
	"testing"
)

// baseInput 正常场景基线：总资产 10 万、缩放 0.82、股价 10 元。
func baseInput() Inputs {
	return Inputs{
		Base:              100000,
		CapEquity:         100000,
		Scale:             0.82,
		Price:             10,
		Cash:              1e9,
		Lot:               DefaultLot,
		IndustryCapPct:    35,
		SingleStockCapPct: 35,
		IndustryKnown:     true,
	}
}

func TestComputeNormalBudgets(t *testing.T) {
	cases := []struct {
		name       string
		mut        func(*Inputs)
		wantQty    int
		wantCapped string
		wantSkip   string
	}{
		{"动态预算被行业 35% 截断", func(in *Inputs) {}, 3500, "industry", ""},
		{"行业已持 34% 减到 1%", func(in *Inputs) { in.IndustryValue = 34000 }, 100, "industry", ""},
		{"行业剩余恰好 1 手", func(in *Inputs) { in.IndustryValue = 34000 }, 100, "industry", ""},
		{"行业剩余不足 1 手跳过", func(in *Inputs) { in.IndustryValue = 34500 }, 0, "industry", "额度不足1手"},
		{"单票额度更小时按单票减", func(in *Inputs) { in.StockValue = 34000 }, 100, "stock", ""},
		{"行业未知时单票兜底", func(in *Inputs) {
			in.IndustryKnown = false
			in.StockValue = 34000
		}, 100, "stock", ""},
		{"行业未知且单票未满按动态预算", func(in *Inputs) { in.IndustryKnown = false }, 3500, "stock", ""},
		{"行业额度为负按 0 跳过", func(in *Inputs) { in.IndustryValue = 40000 }, 0, "industry", "额度不足1手"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := baseInput()
			c.mut(&in)
			res := Compute(in)
			if res.SkipReason != c.wantSkip {
				t.Fatalf("SkipReason=%q want %q (res=%+v)", res.SkipReason, c.wantSkip, res)
			}
			if res.Qty != c.wantQty {
				t.Fatalf("Qty=%d want %d (res=%+v)", res.Qty, c.wantQty, res)
			}
			if c.wantSkip == "" {
				if res.CappedBy != c.wantCapped {
					t.Fatalf("CappedBy=%q want %q", res.CappedBy, c.wantCapped)
				}
				if res.Notional != float64(res.Qty)*in.Price {
					t.Fatalf("Notional 口径错误: %v", res.Notional)
				}
				if res.Budget != res.Notional {
					t.Fatalf("Budget 应等于 Notional，got %v/%v", res.Budget, res.Notional)
				}
			}
		})
	}
}

// TestComputeOneLotFloor §4：raw 低于 1 手时抬到 1 手，但抬升不得突破行业/单票额度。
func TestComputeOneLotFloor(t *testing.T) {
	in := baseInput()
	in.Base, in.Scale = 1000, 0.1 // raw=100 < 1 手 1000
	in.IndustryCapPct, in.SingleStockCapPct = 0, 0
	res := Compute(in)
	if res.SkipReason != "" || res.Qty != 100 || res.CappedBy != "one_lot_floor" {
		t.Fatalf("应抬到 1 手并标记 one_lot_floor，got %+v", res)
	}
	if res.Raw != 1000 {
		t.Fatalf("Raw 应为抬升后的 1000，got %v", res.Raw)
	}

	// 行业额度只剩 500：地板不得把它抬成 1 手。
	in2 := baseInput()
	in2.Base, in2.Scale = 1000, 0.1
	in2.IndustryValue = 34500 // 35% 额度 35000 - 34500 = 500
	res2 := Compute(in2)
	if res2.Qty != 0 || res2.SkipReason != "额度不足1手" {
		t.Fatalf("1 手地板不得突破行业额度，got %+v", res2)
	}

	// 单票额度同理。
	in3 := baseInput()
	in3.Base, in3.Scale = 1000, 0.1
	in3.StockValue = 34500
	res3 := Compute(in3)
	if res3.Qty != 0 || res3.SkipReason != "额度不足1手" {
		t.Fatalf("1 手地板不得突破单票额度，got %+v", res3)
	}
}

// TestComputeCashAndFeeBuffer §4：现金按费用缓冲向下压缩到整手，压到 0 则跳过。
func TestComputeCashAndFeeBuffer(t *testing.T) {
	in := baseInput()
	in.Base, in.Scale = 100000, 1
	in.Cash, in.FeeBufferPct = 5000, 0.6
	res := Compute(in)
	if res.Qty != 400 || res.CappedBy != "cash" || res.Notional != 4000 {
		t.Fatalf("现金+0.6%%缓冲应压到 400 股，got %+v", res)
	}

	// 5000 元、无缓冲 → 500 股；有缓冲 → 400 股。
	in2 := baseInput()
	in2.Base, in2.Scale = 100000, 1
	in2.Cash = 5000
	res2 := Compute(in2)
	if res2.Qty != 500 {
		t.Fatalf("无缓冲应买 500 股，got %d", res2.Qty)
	}

	// 1000 元、10 元票：无缓冲恰好 1 手；有 0.6% 缓冲则不足 1 手。
	in3 := baseInput()
	in3.Base, in3.Scale = 100000, 1
	in3.Cash = 1000
	if got := Compute(in3).Qty; got != 100 {
		t.Fatalf("无缓冲恰好 1 手，got %d", got)
	}
	in3.FeeBufferPct = 0.6
	if res := Compute(in3); res.Qty != 0 || res.SkipReason != "现金不足" {
		t.Fatalf("费用缓冲后不足 1 手应跳过，got %+v", res)
	}
}

// TestComputeWholeLotRounding 所有向下取整都按整手。
func TestComputeWholeLotRounding(t *testing.T) {
	in := baseInput()
	in.Base, in.Scale, in.Price = 100000, 1, 30
	in.IndustryCapPct, in.SingleStockCapPct = 0, 0
	res := Compute(in)
	// 100000/30 = 3333.3 股 → 3300
	if res.Qty != 3300 {
		t.Fatalf("应向下取整到 3300 股，got %d", res.Qty)
	}
	// Lot<=0 按 100 处理。
	in.Lot = 0
	if got := Compute(in).Qty; got != 3300 {
		t.Fatalf("Lot=0 应按 100 股整手，got %d", got)
	}
}

// TestComputeMissingInputsSkips 任何不可得输入都不得伪造成可交易。
func TestComputeMissingInputsSkips(t *testing.T) {
	muts := []struct {
		name string
		mut  func(*Inputs)
	}{
		{"Base=0", func(in *Inputs) { in.Base = 0 }},
		{"Scale=0", func(in *Inputs) { in.Scale = 0 }},
		{"Price=0", func(in *Inputs) { in.Price = 0 }},
		{"CapEquity=0", func(in *Inputs) { in.CapEquity = 0 }},
		{"Cash=0", func(in *Inputs) { in.Cash = 0 }},
		{"Base<0", func(in *Inputs) { in.Base = -1 }},
	}
	for _, c := range muts {
		t.Run(c.name, func(t *testing.T) {
			in := baseInput()
			c.mut(&in)
			res := Compute(in)
			if res.Qty != 0 || res.SkipReason == "" {
				t.Fatalf("不可得输入必须跳过，got %+v", res)
			}
		})
	}
}

// TestComputeDisabledCaps 上限<=0 视为不启用，不得把订单压成 0。
func TestComputeDisabledCaps(t *testing.T) {
	in := baseInput()
	in.IndustryCapPct, in.SingleStockCapPct = 0, 0
	in.Base, in.Scale = 100000, 0.5
	res := Compute(in)
	if res.Qty != 5000 || res.CappedBy != "dynamic" {
		t.Fatalf("上限关闭时按动态预算，got %+v", res)
	}
	if res.IndustryRoom != 0 || res.StockRoom != 0 {
		t.Fatalf("未启用额度的 Room 应保持 0，got %+v", res)
	}
}

// TestRoomOf 额度计算边界。
func TestRoomOf(t *testing.T) {
	if got := roomOf(35, 100000, 34000); math.Abs(got-1000) > 1e-9 {
		t.Fatalf("roomOf=%.4f want 1000", got)
	}
	if got := roomOf(35, 100000, 40000); got != 0 {
		t.Fatalf("超限应钳到 0，got %v", got)
	}
}
