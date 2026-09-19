// §RFIX-1 回归测试：nShapeAdapter 的 MACD 序列必须逐股票重算，且 Trigger 对越界索引
// 钳位回退（旧实现按首只股票算一次全池复用：跨股污染 + 更长序列 panic）。
package btreplay

import (
	"fmt"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategies/n_shape"
)

// mkKLines 生成 n 根等间隔日K（收盘价缓步上行，量额非零避免均价带除零）。
func mkKLines(n int) []data.KLine {
	out := make([]data.KLine, 0, n)
	base := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		c := 10 + float64(i)*0.05
		out = append(out, data.KLine{
			Date: base.AddDate(0, 0, i), Open: c * 0.99, High: c * 1.02, Low: c * 0.98,
			Close: c, Volume: float64(1000 + i), Amount: c * float64(1000+i),
		})
	}
	return out
}

// newNSHApe 构造带真实策略实例的 N形适配器（内存默认配置，不触发不写盘）。
func newNSHApe(d1 float64) *nShapeAdapter {
	cfgMgr := config.NewManager("")
	sc := cfgMgr.Get().Strategy
	return &nShapeAdapter{st: n_shape.New(cfgMgr, nil), cfg: &sc.NShape, d1Score: d1}
}

// TestBacktestStockRefreshesMacdPerStock 核心回归：先后回放两根不同长度的股票，
// macdSeries 长度必须跟随"当前股票"——旧 `== nil` 守卫会永远停在第一只的长度。
func TestBacktestStockRefreshesMacdPerStock(t *testing.T) {
	na := newNSHApe(0) // d1Score=0：Trigger 恒不触发，只验证序列装配
	o := &Options{}
	a := mkKLines(60)
	o.backtestStock("000001", a, na, nil)
	if len(na.macdSeries) != 60 {
		t.Fatalf("首只股票后 macdSeries 长度应为 60，实际 %d", len(na.macdSeries))
	}
	b := mkKLines(80) // 比第一只更长——旧实现下 curIdx 到 60+ 即越界 panic
	o.backtestStock("000002", b, na, nil)
	if len(na.macdSeries) != 80 {
		t.Fatalf("次只股票后 macdSeries 应重算为 80（跨股污染回归），实际 %d", len(na.macdSeries))
	}
}

// TestNShapeTriggerClampsOutOfRange 钳位兜底：curIdx 越出 macdSeries 时 Trigger 不得崩溃，
// 应回退到 CalcMACD(klines) 逐日重算路径（结果合理性由既有回放测试族保证）。
func TestNShapeTriggerClampsOutOfRange(t *testing.T) {
	na := newNSHApe(70) // d1Score>0 让 Trigger 走完 MACD 取值分支
	kl := mkKLines(70)
	na.macdSeries = []data.MACD{{DIF: 1, DEA: 1, Bar: 0}} // 故意只放 1 个元素
	na.curIdx = 65                                        // 越界索引
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("Trigger 越界索引不应 panic，实际: %v", p)
		}
	}()
	_, _ = na.Trigger(kl, kl[len(kl)-2].Close, 0)
}

// TestNShapeRunTwoStocksNoPanic 端到端最小面：两只不等长股票连续回放（模拟生产
// #264 场景的缩小版），全程无 panic 即通过。
func TestNShapeRunTwoStocksNoPanic(t *testing.T) {
	na := newNSHApe(70)
	o := &Options{}
	kodes := []int{760, 761} // 生产实录：首只 760 根、次只更长
	for i, n := range kodes {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("第 %d 只（%d 根）回放 panic: %v", i+1, n, p)
				}
			}()
			o.backtestStock(fmt.Sprintf("60000%d", i), mkKLines(n), na, nil)
		}()
	}
}
