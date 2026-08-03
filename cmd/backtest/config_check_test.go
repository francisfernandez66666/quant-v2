// Package main backtest 工具：本测试文件验证回测参数配置文件能被 config.Manager 全量解析。
package main

import (
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/config"
)

// TestParamsConfigLoad 验证 backtest_params.json 能被 config.Manager 全量解析。
func TestParamsConfigLoad(t *testing.T) {
	path := filepath.Join("..", "..", "backtest_params.json")
	if _, err := os.Stat(path); err != nil {
		t.Skip("backtest_params.json 不存在")
	}
	m := config.NewManager(path)
	if m.Rules.Emotion.EmoClimaxBoardMin == 0 {
		t.Error("emotion_cycle 参数未加载")
	}
	if m.Rules.Strategy.Dragon.F1SealWeight == 0 {
		t.Error("strategy.dragon 参数未加载")
	}
	if m.Rules.Strategy.Momentum.MACDWeight == 0 {
		t.Error("strategy.momentum 参数未加载")
	}
	if m.Rules.Laodeng.MarketCapMin == 0 {
		t.Error("laodeng 参数未加载")
	}
	if len(m.D1.Rules) == 0 {
		t.Error("d1.rules 未加载")
	}
}
