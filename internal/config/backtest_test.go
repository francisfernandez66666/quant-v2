// backtest_test.go 回测增强配置（A0）：默认回填、越界校验、分档单调性、Active 短路。
// English: config tests — FillDefaults, range validation, tier monotonicity, Active gating.
package config

import "testing"

// enabledCfg 一份合法的全开配置（各用例在其上做局部篡改）。
func enabledCfg() *BacktestConfig {
	return &BacktestConfig{
		Enabled:        true,
		OrderValueYuan: 10000,
		Slippage: SlippageConfig{
			BaseBps: 3, BuyExtraBps: 1, Asymmetric: true, AutoCalibrate: true,
			CalibMinSample: 30, CalibWindowDays: 90,
			VolumeTiers: DefaultVolumeTiers(), SizeTiers: DefaultSizeTiers(),
		},
		Liquidity: LiquidityConfig{
			Enabled: true, LimitUpOpenableExtraBps: 10, LimitDownSealedExtraBps: 15,
			LimitDownSealedEnabled: true, PartialFillEnabled: true, FillRateMin: 0.3,
		},
		Pareto: ParetoConfig{Enabled: true, MinWinRate: 30, MinProfitFactor: 1, MinSharpe: 0.5, MinCalmar: 0.5, MaxFrontPoints: 50},
	}
}

// TestValidateBacktestOK 完整合法配置零错误。
func TestValidateBacktestOK(t *testing.T) {
	if err := ValidateBacktest(enabledCfg()); err != nil {
		t.Fatalf("合法配置报错: %v", err)
	}
}

// TestValidateBacktestRanges 各字段越界逐项拒绝。
func TestValidateBacktestRanges(t *testing.T) {
	cases := []struct {
		name string
		f    func(*BacktestConfig)
	}{
		{"base_bps 上限", func(c *BacktestConfig) { c.Slippage.BaseBps = 51 }},
		{"base_bps 负值", func(c *BacktestConfig) { c.Slippage.BaseBps = -1 }},
		{"buy_extra 越界", func(c *BacktestConfig) { c.Slippage.BuyExtraBps = 60 }},
		{"calib_min_sample 越界", func(c *BacktestConfig) { c.Slippage.CalibMinSample = 99999 }},
		{"calib_window 越界", func(c *BacktestConfig) { c.Slippage.CalibWindowDays = 5000 }},
		{"名义额负值", func(c *BacktestConfig) { c.OrderValueYuan = -1 }},
		{"涨停罚分越界", func(c *BacktestConfig) { c.Liquidity.LimitUpOpenableExtraBps = 99 }},
		{"fill_rate_min 越界", func(c *BacktestConfig) { c.Liquidity.FillRateMin = 1.5 }},
		{"min_win_rate 越界", func(c *BacktestConfig) { c.Pareto.MinWinRate = 130 }},
		{"max_front_points 越界", func(c *BacktestConfig) { c.Pareto.MaxFrontPoints = 100000 }},
	}
	for _, tc := range cases {
		c := enabledCfg()
		tc.f(c)
		if err := ValidateBacktest(c); err == nil {
			t.Fatalf("%s 应被拒绝", tc.name)
		}
	}
}

// TestValidateBacktestTierMonotonic 分档表乱序必须拒绝（引擎线性扫描依赖序）。
func TestValidateBacktestTierMonotonic(t *testing.T) {
	c := enabledCfg()
	c.Slippage.VolumeTiers = []VolumeTier{{MaxVolumeWan: 5000, ExtraBps: 3}, {MaxVolumeWan: 100, ExtraBps: 15}}
	if err := ValidateBacktest(c); err == nil {
		t.Fatal("VolumeTiers 非升序应被拒绝")
	}
	c = enabledCfg()
	c.Slippage.SizeTiers = []SizeTier{{MinRatio: 0.001, ExtraBps: 3}, {MinRatio: 0.02, ExtraBps: 10}}
	if err := ValidateBacktest(c); err == nil {
		t.Fatal("SizeTiers 非降序应被拒绝")
	}
}

// TestFillBacktestDefaults enabled 下缺省字段全部回填新默认（BaseBps 3.0/买差 1.0/
// 分档内置表/名义额 10000/Pareto 门槛 30/1/0.5/0.5/前沿 50 点）。
func TestFillBacktestDefaults(t *testing.T) {
	c := &BacktestConfig{Enabled: true, Slippage: SlippageConfig{Asymmetric: true}}
	c.FillDefaults()
	if c.OrderValueYuan != BacktestDefaultOrderValueYuan {
		t.Fatalf("OrderValueYuan=%f", c.OrderValueYuan)
	}
	if c.PaperModelSlippageBps != BacktestDefaultPaperSlipBps {
		t.Fatalf("PaperModelSlippageBps=%f", c.PaperModelSlippageBps)
	}
	if c.Slippage.BaseBps != 3.0 || c.Slippage.BuyExtraBps != 1.0 {
		t.Fatalf("滑点默认=%f/%f", c.Slippage.BaseBps, c.Slippage.BuyExtraBps)
	}
	if len(c.Slippage.VolumeTiers) != 4 || len(c.Slippage.SizeTiers) != 4 {
		t.Fatal("分档表未回填内置默认")
	}
	if c.Liquidity.LimitUpOpenableExtraBps != 10 || c.Liquidity.FillRateMin != 0.3 {
		t.Fatalf("流动性默认=%f/%f", c.Liquidity.LimitUpOpenableExtraBps, c.Liquidity.FillRateMin)
	}
	if c.Pareto.MinWinRate != 30 || c.Pareto.MinProfitFactor != 1 || c.Pareto.MinSharpe != 0.5 ||
		c.Pareto.MinCalmar != 0.5 || c.Pareto.MaxFrontPoints != 50 {
		t.Fatalf("Pareto 默认 %+v", c.Pareto)
	}
	if c.WalkForward.TrainDays != 244 || c.WalkForward.ValidateDays != 62 || c.WalkForward.StepDays != 62 {
		t.Fatalf("WalkForward 默认 %+v", c.WalkForward)
	}
}

// TestFillDefaultsDisabledShortCircuit enabled=false 不回填（旧行为零扰动）。
func TestFillDefaultsDisabledShortCircuit(t *testing.T) {
	c := &BacktestConfig{}
	c.FillDefaults()
	if c.OrderValueYuan != 0 || len(c.Slippage.VolumeTiers) != 0 {
		t.Fatal("未启用不应回填默认")
	}
	if c.Active() {
		t.Fatal("Active 应为 false")
	}
	var nilCfg *BacktestConfig
	if nilCfg.Active() {
		t.Fatal("nil Active 必须为 false（nil 安全）")
	}
}
