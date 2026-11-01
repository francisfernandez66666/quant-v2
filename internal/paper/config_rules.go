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
// 可选 sizing 入参：存在时把账号的动态预算口径/开关/影子同步进引擎配置（v3）。
// English: optional sizing input carries the account's v3 budget mode / switch / shadow into
// the engine config.
//
// rg §F-4（2026-11-01）：账号的 qmt.risk_gate。目前只取 MaxOrderAmount（单笔金额绝对帽），
// 但**入参是整份 RiskGate**而不是单个 float：纸面与实盘同口径的闸门以后还会加（跌停/时效等），
// 每次新增一个 float 参数会让 4 个装配点逐个改签名、逐个漏掉——漏掉的那个点就是两本账分歧。
// 传整份结构，新增闸门只改本函数体。
//
// 为什么必须从这里取、而不是在 paper.New 之后补一句赋值：装配点有四处（main / registry 懒加载 /
// paper/config 热更 / sizing 热更），补赋值的写法必然漏一处，而漏的那处表现为"重启后帽消失"
// 或"改完配置帽不生效"——§PAPER-CFG-DESYNC 就是这么来的。单一构建器是唯一防法。
// English: §F-4 — the whole RiskGate is passed (not one float) so future paper/live parity gates
// only change this body, and every assembly point gets them by construction.
func ConfigFromRules(rp config.PaperConfig, rg config.RiskGateConfig, sizing ...config.SizingConfig) Config {
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
	c := Config{
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
		// §F-4 单笔金额绝对帽：与实盘 risk.checkMaxOrderAmount 同一阈值。负值按未启用处理
		// （config.validate 已经拒负，这里再兜一次：装配层的口径必须自洽，不能指望上游）。
		MaxOrderAmount: max0(rg.MaxOrderAmount),
	}
	if len(sizing) > 0 {
		sc := sizing[0].WithDefaults()
		c.BudgetMode = sc.BudgetModeOf()
		c.SizingEnabled = sc.EnabledOn()
		c.SizingShadow = sc.ShadowOn()
	}
	return c
}

// max0 负值归零（0 = 该闸未启用）。只为把"负阈值"和"未配置"收敛成同一种语义：
// 两者都表示不拦，绝不能因为 -1 被当成"极小的帽"而把所有单子拒光。
// English: clamps a negative threshold to zero (0 = gate off), so a typo'd -1 can never act as
// a tiny cap that rejects every order.
func max0(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}
