// backtest_payload_test.go — §回测自动增强 A0：runtask 端 payload.backtest 解析回退链
// （缺失/停用/坏结构 = nil = 引擎旧行为）。
// English: payload.backtest decoding fallbacks on the run-task dispatcher side.
package main

import (
	"encoding/json"
	"testing"
)

// decodeP JSON 文本 → payload map（模拟任务行解析）。
func decodeP(t *testing.T, s string) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPayloadBacktestFallbacks 四路回退 + 正常解析。
func TestPayloadBacktestFallbacks(t *testing.T) {
	// 缺字段
	if payloadBacktest(decodeP(t, `{"kind":"optimize"}`)) != nil {
		t.Fatal("缺 backtest 字段应为 nil")
	}
	// enabled=false
	if payloadBacktest(decodeP(t, `{"backtest":{"enabled":false}}`)) != nil {
		t.Fatal("停用应为 nil")
	}
	// 类型错（不是对象）
	if payloadBacktest(decodeP(t, `{"backtest":"yes"}`)) != nil {
		t.Fatal("类型错应为 nil")
	}
	// enabled=true 全字段
	cfg := payloadBacktest(decodeP(t, `{"backtest":{"enabled":true,"order_value_yuan":8888,
		"slippage":{"base_bps":4,"auto_calibrate":true,"calib_min_sample":50}}}`))
	if cfg == nil || !cfg.Enabled {
		t.Fatal("enabled 配置应解析成功")
	}
	if cfg.OrderValueYuan != 8888 || cfg.Slippage.BaseBps != 4 || !cfg.Slippage.AutoCalibrate ||
		cfg.Slippage.CalibMinSample != 50 {
		t.Fatalf("字段解析异常: %+v", cfg)
	}
	// 旧 worker 新二进制反向兼容：未知字段忽略即可（json 宽容）
	if cfg := payloadBacktest(decodeP(t, `{"backtest":{"enabled":true,"future_field":1}}`)); cfg == nil {
		t.Fatal("未知字段不应导致解析失败")
	}
}
