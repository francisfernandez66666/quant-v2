// tech_filter_test.go — §C4 技术过滤输入计算锁。
//
// 锁：已完成日K上能算出 KDJ J / MA60 / CloseVsMA；K 线不足时保持零值（signalctl 弃权）。
// English: §C4 input locks — KDJ/MA computed from completed bars; insufficient bars stay zero.
package combat_agent

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

func techKLines(n int, end time.Time) []data.KLine {
	out := make([]data.KLine, 0, n)
	for i := n - 1; i >= 0; i-- {
		d := end.AddDate(0, 0, -i)
		out = append(out, data.KLine{
			Date: d, Open: 10, High: 10.5, Low: 9.5, Close: 10, Volume: 1000, Amount: 10000,
		})
	}
	return out
}

func TestApplyTechInputs(t *testing.T) {
	// 最后一根为昨天，避免盘中剔除当日 K 的逻辑干扰。
	end := time.Now().AddDate(0, 0, -1)
	sig := &Signal{}
	applyTechInputs(sig, techKLines(260, end))
	if sig.KDJ_J == 0 {
		t.Fatal("KDJ J 未计算")
	}
	if sig.MA60 == 0 || sig.MA120 == 0 || sig.MA250 == 0 {
		t.Fatalf("长均线未计算: ma60=%v ma120=%v ma250=%v", sig.MA60, sig.MA120, sig.MA250)
	}
	if _, ok := sig.CloseVsMA["ma60"]; !ok {
		t.Fatalf("CloseVsMA 缺 ma60: %v", sig.CloseVsMA)
	}
	// 数据不足 → 保持零值，不编造。
	short := &Signal{}
	applyTechInputs(short, techKLines(10, end))
	if short.MA60 != 0 || short.MA120 != 0 || short.MA250 != 0 {
		t.Fatalf("长均线不足应弃权, got %+v", short)
	}
	if _, ok := short.CloseVsMA["ma60"]; ok {
		t.Fatalf("K 线不足不应有 ma60 读数: %v", short.CloseVsMA)
	}
}
