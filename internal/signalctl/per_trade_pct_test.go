// per_trade_pct_test.go — §B0 单笔占比区间锁（owner 2026-10-27：单笔按因子在 10%~30% 之间动）。
//
// 这一组是整个"碎单"修复的口径源头：实盘与纸面都只调这里的函数，所以边界必须钉死在函数上，
// 而不是钉在某个调用点的日志里。三条：
//  1. 单调：k=MS×SS 越大，eff 越大（不许出现"因子好反而买得少"）；
//  2. 有界：k 取 0 / 极大 / 负数时，eff 仍落在 [lo,hi]（资金参数越界必须由函数兜住）；
//  3. 区间配置错（hi<lo、>0.5、≤0）时收敛到安全值，而不是算出负数或恒等上限。
//
// English: range locks for the per-trade share — monotone in MS×SS, always inside [lo,hi], and
// self-healing when the configured bounds are reversed or out of range.
package signalctl

import (
	"math"
	"testing"
)

func TestPerTradePctRange(t *testing.T) {
	cases := []struct {
		name           string
		ms, ss, lo, hi float64
		want           float64
	}{
		{"因子全灭取下限", 0, 0, 0.10, 0.30, 0.10},
		{"全因子中性取上限", 1, 1, 0.10, 0.30, 0.30},
		{"实测下沿0.67线性", 0.67, 1.0, 0.10, 0.30, 0.10 + 0.20*0.67},
		{"实测上沿0.82线性", 0.82, 1.0, 0.10, 0.30, 0.10 + 0.20*0.82},
		{"双双低于1相乘", 0.7, 0.5, 0.10, 0.30, 0.10 + 0.20*0.35},
		{"k越界>1硬夹到上限", 3, 2, 0.10, 0.30, 0.30},
		{"k为负硬夹到下限", -1, 0.5, 0.10, 0.30, 0.10},
		{"hi<lo时抬到lo", 0.5, 1, 0.25, 0.10, 0.25},
		{"lo≤0回落默认0.10", 1, 1, 0, 0.30, 0.30},
		{"hi≤0回落默认0.30", 1, 1, 0.10, 0, 0.30},
		{"hi>0.5钳到0.5", 1, 1, 0.10, 0.90, 0.50},
		{"lo>0.5钳到0.5", 1, 1, 0.80, 0.90, 0.50},
	}
	for _, c := range cases {
		got := PerTradePct(c.ms, c.ss, c.lo, c.hi)
		if math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("%s: PerTradePct(%v,%v,%v,%v)=%v 期望 %v", c.name, c.ms, c.ss, c.lo, c.hi, got, c.want)
		}
	}
}

// TestPerTradePctMonotone 单调性：因子越好→单笔越大。反证：把映射写成减号必红。
func TestPerTradePctMonotone(t *testing.T) {
	prev := -1.0
	for _, k := range []float64{0, 0.2, 0.4, 0.6, 0.8, 1.0} {
		got := PerTradePct(k, 1.0, 0.10, 0.30)
		if got < prev {
			t.Fatalf("单调性破了：k=%v 得 %v < 上一个 %v", k, got, prev)
		}
		prev = got
	}
}

// TestPerTradePctNeverExceedsHalf 单笔永不超过半仓（含输入全越界的极端组合）。
func TestPerTradePctNeverExceedsHalf(t *testing.T) {
	for _, lo := range []float64{-1, 0, 0.1, 0.4, 0.9} {
		for _, hi := range []float64{-1, 0, 0.1, 0.3, 0.7, 1.5} {
			for _, k := range []float64{-0.5, 0, 0.5, 1, 5} {
				if got := PerTradePct(k, 1, lo, hi); got < 0 || got > 0.5+1e-9 {
					t.Fatalf("越界：lo=%v hi=%v k=%v ⇒ %v", lo, hi, k, got)
				}
			}
		}
	}
}
