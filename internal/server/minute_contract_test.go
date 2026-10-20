package server

import (
	"errors"
	"strings"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// §N-12a：对外 reason_code 只允许固定枚举；原始错误串绝不进 message。
func TestMinuteFailureResponseSanitized(t *testing.T) {
	raw := errors.New("eastmoney circuit-breaker open (push2.eastmoney.com/api/qt/stock/kline/get)")
	code, msg := minuteFailureResponse(raw, false)
	if code != "minute_sources_unavailable" {
		t.Fatalf("源错误应归 minute_sources_unavailable, got %s", code)
	}
	for _, bad := range []string{"push2", "circuit-breaker", "eastmoney", "unexpected end"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("对客 message 不得含内部串 %q: %s", bad, msg)
		}
	}
	code, _ = minuteFailureResponse(data.ErrNoMinuteData, false)
	if code != "no_symbol_data" {
		t.Fatalf("no_data 应归 no_symbol_data, got %s", code)
	}
}

// §N-12d：没有行情时信号价格必须是 trigger/none，不能默认当 live。
func TestToFixSignalsPriceKind(t *testing.T) {
	out := toFixSignals([]combat_agent.Signal{
		{Code: "300023", Name: "测试", Price: 9.78},
		{Code: "600000", Name: "无价", Price: 0},
	})
	if out[0].PriceKind != "trigger" {
		t.Fatalf("信号触发价初始应标 trigger, got %s", out[0].PriceKind)
	}
	if out[1].PriceKind != "none" {
		t.Fatalf("零价信号应标 none, got %s", out[1].PriceKind)
	}
}
