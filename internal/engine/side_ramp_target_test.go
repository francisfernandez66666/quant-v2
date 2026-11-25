// side_ramp_target_test.go — D7 目标乘子在资金链上的落点（2026-11-25）。
//
// 为什么要单独一枚：分时释放阶梯（SideTimeRamp）本身是纯函数，但它的**接线**决定了
// 当天能买多少。这里钉的 §17.3 ⑦⑧ 两条是接线正确性：
//
//	⑦ Tgt = total × min(MS × TM, ceiling) × ramp
//	    —— ramp 在 min() **外面**：侧策略的目标乘子已经被天花板夹过（bull L3 = 1.15
//	    本来就在 ceiling 里），再乘一个 0.7 才是"今天只释放 70% 的额度"。如果写进
//	    min() 里面，ceiling=0.5 时 ramp 会被天花板吞掉 ⇒ 上午照旧满仓，下午才释放，
//	    方向正好反了。
//	⑧ T 腿的 TimeRamp 恒为 1.0
//	    —— T 是日内回转，占用的是当日已冻结现金，不是战略目标空间；给它乘 0.35
//	    会让"做了 T 的人反而不能做 T"，而那条腿的额度由 t_intraday_cap_pct 自己管。
//
// English: wiring locks for the D7 ramp — it multiplies OUTSIDE the min(ms*TM, ceiling)
// clamp, and the intraday-T leg pins its ramp to 1.0.
package engine

import (
	"math"
	"testing"

	"quant-trading-v2/internal/store"
)

// rampTargetBook 空账本（无 T 腿金额），这样 target 只受三乘子影响。
func rampTargetBook() tLiveBook { return tLiveBook{} }

func TestFundsTargetRampMultipliesOutsideTheCeiling(t *testing.T) {
	total, ms, ceiling, tm, ramp := 100000.0, 0.8, 0.5, 1.15, 0.7
	target, _, room, ok := fundsTargetCurRoomWithScales(
		total, ms, ceiling, tm, ramp, nil, nil, nil, rampTargetBook())
	if !ok {
		t.Fatal("scales outlet must always be ok for positive total")
	}
	// min(0.8*1.15, 0.5) = 0.5；再 × 0.7 = 0.35 ⇒ 35000。
	want := total * 0.5 * ramp
	if math.Abs(target-want) > 1e-6 {
		t.Fatalf("target=%v want %v (ramp must be applied outside the ceiling)", target, want)
	}
	if math.Abs(room-target) > 1e-6 {
		t.Fatalf("empty book must leave the whole target as room, got %v", room)
	}
	// 反证方向：把它写进 min() 里会得到 total*0.5（=50000），与上面差 15000。
	if math.Abs(target-total*0.5) < 1 {
		t.Fatal("ramp was swallowed by the ceiling clamp (inside-min bug)")
	}
}

func TestFundsTargetRampDefaultIsOne(t *testing.T) {
	total, ms, ceiling, tm := 100000.0, 0.8, 0.9, 1.0
	base, _, _, _ := fundsTargetCurRoomWithScales(total, ms, ceiling, tm, 1.0, nil, nil, nil, rampTargetBook())
	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		got, _, _, _ := fundsTargetCurRoomWithScales(total, ms, ceiling, tm, bad, nil, nil, nil, rampTargetBook())
		if math.Abs(got-base) > 1e-6 {
			t.Fatalf("ramp=%v must normalize to 1.0, got %v want %v", bad, got, base)
		}
	}
}

func TestFundsTargetRampShrinksStrategicRoom(t *testing.T) {
	// 已持 30000 市值、目标 0.35 ⇒ room=5000；ramp=1 时 room=20000。
	positions := map[string]store.RealPosition{"600000": {Qty: 3000, CostPrice: 10}}
	prices := map[string]float64{"600000": 10}
	full, _, roomFull, _ := fundsTargetCurRoomWithScales(
		100000, 0.8, 0.5, 1.0, 1.0, positions, prices, nil, rampTargetBook())
	ramped, _, roomRamped, _ := fundsTargetCurRoomWithScales(
		100000, 0.8, 0.5, 1.0, 0.7, positions, prices, nil, rampTargetBook())
	if math.Abs(full-50000) > 1e-6 || math.Abs(ramped-35000) > 1e-6 {
		t.Fatalf("targets full=%v ramped=%v want 50000/35000", full, ramped)
	}
	if math.Abs(roomFull-20000) > 1e-6 || math.Abs(roomRamped-5000) > 1e-6 {
		t.Fatalf("rooms full=%v ramped=%v want 20000/5000", roomFull, roomRamped)
	}
}

// ⑧ 两个定权上下文各自把"未注入"折叠成 1.0，且 T 装配点显式钉 1.0。
//
// 前者是存取器的语义（0/NaN/Inf 都等于"今天的行为"——不因为缺读数而缩额度）；
// 后者是接线（intraday_t_live.go 的 strategicAllowedQty 把 TimeRamp 硬钉 1.0，
// 因为 T 的额度由 t_intraday_cap_pct 自己管，再乘一档分时释放等于"做了 T 的人
// 反而不能做 T"）。两条一起断：存取器坏了会让全链静默缩额，接线坏了只坏 T 一条腿。
func TestTimeRampAccessorsFoldMissingToFull(t *testing.T) {
	for _, bad := range []float64{0, -0.5, math.NaN(), math.Inf(-1)} {
		if got := (dynBudgetCtx{TimeRamp: bad}).timeRamp(); got != 1.0 {
			t.Fatalf("dynBudgetCtx.TimeRamp=%v folded to %v want 1.0", bad, got)
		}
		if got := (sizingCtx{TimeRamp: bad}).timeRamp(); got != 1.0 {
			t.Fatalf("sizingCtx.TimeRamp=%v folded to %v want 1.0", bad, got)
		}
	}
	if got := (dynBudgetCtx{TimeRamp: 0.35}).timeRamp(); got != 0.35 {
		t.Fatalf("a real ramp must pass through, got %v", got)
	}
}

// §240 ⑦ 方案原数：target = 100000 × min(0.8×1.0, 1.0) × 0.35 = 28000。
// 上面那条反证用的是「天花板先生效」的另一组数（0.5 顶帽），这组钉的是
// 无顶帽时的纯乘算，两条一起才能证明 ramp 既没被吞进 min、也没有多加一层。
func TestTargetRampArithmetic(t *testing.T) {
	target, _, _, ok := fundsTargetCurRoomWithScales(
		100000.0, 0.8, 1.0, 1.0, 0.35, nil, nil, nil, rampTargetBook())
	if !ok {
		t.Fatal("positive total must be ok")
	}
	if math.Abs(target-28000.0) > 1e-9 {
		t.Fatalf("target=%v want 28000 (plan §240 ⑦)", target)
	}
}

// §240 ⑧ T/reduce 走 fundsTargetCurRoom 包装：TM 与 ramp 都钉死 1.0。
func TestTRampNeutral(t *testing.T) {
	target, _, _, ok := fundsTargetCurRoom(
		100000.0, 0.8, 1.0, nil, nil, nil, rampTargetBook())
	if !ok {
		t.Fatal("positive total must be ok")
	}
	if math.Abs(target-80000.0) > 1e-9 {
		t.Fatalf("T wrapper target=%v want 80000 (no ramp / no TM)", target)
	}
}
