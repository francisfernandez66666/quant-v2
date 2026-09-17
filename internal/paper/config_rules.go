// config_rules.go — §F-4（20260917 缺陷修复批）rules.paper（配置层）→ paper.Config（引擎层）
// 的唯一装配函数。此前该映射内联在 cmd/quant/main.go（仅进程启动时执行一次），
// HTTP 端点无处复用导致模拟盘配置只能重启生效（Registry.SetPaperConfig 成死代码）。
// 现由 main.go 与 POST /api/paper/config 共同调用，零值归一口径与原 main.go 完全一致。
// English: single builder turning rules.paper into the engine-side paper.Config (was inlined in
// main.go at process start only). Now shared by main.go and the §F-4 hot-update endpoint; the
// zero-value normalization matches the original main.go exactly.
package paper

import "quant-trading-v2/internal/config"

// ConfigFromRules 把账户级 rules.paper 装配为撮合引擎配置：
//   - AutoSell/ShortEnabled nil=开（历史默认语义）；
//   - §SHORT-3 零值归一：保证金率/年化费率/止损涨幅未配置时取 DefaultConfig 真实券商口径
//     （0 会退化为「无保证金约束/免费/无止损」，均不可接受）。
//
// English: builds the engine Config from account rules; nil auto-sell/short flags mean ON, and
// zero margin/fee/stop values normalize to broker-real defaults (same contract as the old main.go).
func ConfigFromRules(rp config.PaperConfig) Config {
	d := DefaultConfig()
	shortMarginRate, shortFeeAnnual, shortStopPct := rp.ShortMarginRate, rp.ShortFeeAnnual, rp.ShortStopLossPct
	if shortMarginRate <= 0 {
		shortMarginRate = d.ShortMarginRate
	}
	if shortFeeAnnual <= 0 {
		shortFeeAnnual = d.ShortFeeAnnual
	}
	if shortStopPct <= 0 {
		shortStopPct = d.ShortStopLossPct
	}
	return Config{
		Enabled:          rp.Enabled,
		FixedAmount:      rp.FixedAmount,
		MaxPositions:     rp.MaxPositions,
		InitialCapital:   rp.InitialCapital,
		AutoSell:         rp.AutoSell == nil || *rp.AutoSell,
		ShortEnabled:     rp.ShortEnabled == nil || *rp.ShortEnabled,
		ShortCapital:     rp.ShortCapital,
		ShortMarginRate:  shortMarginRate,
		ShortFeeAnnual:   shortFeeAnnual,
		ShortFixedAmount: rp.ShortFixedAmount,
		ShortStopLossPct: shortStopPct,
	}
}
