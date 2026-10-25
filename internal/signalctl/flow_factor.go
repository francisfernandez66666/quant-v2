// flow_factor.go — §FLOW-FACTOR 净流入 dynamic 因子（纯函数，无 IO）。
//
// 口径（docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.3~§2.5，owner 2026-10-25 五条裁决）：
//
//	FlowRatio = 主力净流入（超大单+大单）/ 流通市值 × 100（%）
//	  · Buy5D    近 5 个交易日累计（主因子）      → 买入乘子，进个股组/连乘
//	  · BuyToday 当日实时值（辅因子，幅度减半）   → 买入乘子，进个股组/连乘
//	  · Sell     净流出档位 → 卖出确认窗时长×权重（不缩量；权重=买入偏离折半）
//
// 三层正相关模型（§2.7.0）：本因子只是"动态仓位"层的又一个维度，与战法、信号质量等并列，
// 不改变"战法 ~ 因子加权 ~ 动态仓位 ~ 最终执行"的正相关结构，绝不与之相乘归因。
//
// 弃权纪律（与 sizing.go 逐字一致）：比值 0（引擎未解析/功能未开）→ 该维**不进合成**，
// 既不当作 0 也不当作中性；负值（净流出）是合法读数，买入侧走惩罚档、卖出侧触发加权。
// English: net-inflow dynamic factor. Zero ratio = unresolved → abstain; negative ratios are
// valid readings (buy penalty bands / sell weights). Never treated as neutral.
package signalctl

import (
	"fmt"
	"math"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// flowCfgOf 兜底解析因子配置：调用方漏过 WithDefaults（裸 cfg 直传）时按出厂档位行事。
// 两表全空=从未配置 → 全默认；有任一张表 → 走 WithDefaults 合并（用户键覆盖默认键）。
// English: resolves the factor config, falling back to factory defaults when nothing was configured.
func flowCfgOf(cfg config.SizingConfig) config.FlowFactorConfig {
	if len(cfg.FlowFactor.Buy5DScales) == 0 && len(cfg.FlowFactor.SellScales) == 0 &&
		len(cfg.FlowFactor.BuyTodayScales) == 0 {
		return config.DefaultFlowFactorConfig()
	}
	return cfg.FlowFactor.WithDefaults()
}

// bandOr 读分档阈值，缺失或非有限时用默认值（带宽口径与出厂默认逐字一致）。
// English: reads a band threshold, falling back to the factory value.
func bandOr(m map[string]float64, key string, def float64) float64 {
	if v, ok := m[key]; ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
		return v
	}
	return def
}

// flowBandOf 净流入率 → 档位键。ratio==0=未解析 → 返回 ""（调用方弃权）。
// 带宽（出厂值，可配）：≥strong_in(3) 强入；≥mild_in(1) 轻入；>mild_out(−1) 中性；
// >strong_out(−3) 轻出；≤strong_out 强出。边界归严不归松：恰好压在阈值上按**更有利**档计
// （ratio==mild_in_pct 记轻入而非中性）——阈值是"达到即算"的授权线，不是排除线。
// English: maps the ratio to a band; zero (unresolved) → ""; thresholds are inclusive lower bounds.
func flowBandOf(ratio float64, bands map[string]float64) string {
	if ratio == 0 || math.IsNaN(ratio) {
		return ""
	}
	switch {
	case ratio >= bandOr(bands, "strong_in_pct", 3):
		return config.FlowBandStrongIn
	case ratio >= bandOr(bands, "mild_in_pct", 1):
		return config.FlowBandMildIn
	case ratio > bandOr(bands, "mild_out_pct", -1):
		return config.FlowBandNeutral
	case ratio > bandOr(bands, "strong_out_pct", -3):
		return config.FlowBandMildOut
	default:
		return config.FlowBandStrongOut
	}
}

// flowScaleOr 档位取值：**显式 0 = 该档弃权**（进化器 C 组合层关因子的手段：把某因子
// 所有档位写 0 → 每个 band 都弃权 → 该维整体退出合成）；缺键或负值才用默认（中性 1.0）。
// 与包内 scaleOr 同口径——"关因子"必须在配置层可表达，不能被打兜底默认悄悄顶回来。
// English: band scale lookup; an explicit 0 abstains that band (the evo loop's off switch).
func flowScaleOr(m map[string]float64, key string, def float64) float64 {
	if v, ok := m[key]; ok {
		if v == 0 {
			return 0
		}
		if v > 0 {
			return v
		}
	}
	return def
}

// flowBuyFactor 档位 → 买入乘子。档位值 0（显式弃权）→ 0 弃权；缺键/负值按中性 1.0。
// English: band → buy multiplier; explicit 0 abstains, missing/negative is neutral.
func flowBuyFactor(band string, scales map[string]float64) float64 {
	if band == "" {
		return 0 // 未解析 → 弃权
	}
	return flowScaleOr(scales, band, 1.0)
}

// flow5DFactor 5 日净流入率 → 买入乘子（主因子）。总开关关 / 比值未解析 → 0（弃权）。
// English: 5-day net-inflow ratio → buy multiplier (the primary factor).
func flow5DFactor(sig combat_agent.Signal, cfg config.SizingConfig) float64 {
	if !cfg.FlowFactorOn() {
		return 0
	}
	fc := flowCfgOf(cfg)
	return flowBuyFactor(flowBandOf(sig.FlowRatio5D, fc.Buy5DBands), fc.Buy5DScales)
}

// flowTodayFactor 当日净流入率 → 买入乘子（辅因子，实时值，档位幅度减半）。
// English: same-day net-inflow ratio → buy multiplier (the secondary, halved-band factor).
func flowTodayFactor(sig combat_agent.Signal, cfg config.SizingConfig) float64 {
	if !cfg.FlowFactorOn() {
		return 0
	}
	fc := flowCfgOf(cfg)
	return flowBuyFactor(flowBandOf(sig.FlowRatioToday, fc.BuyTodayBands), fc.BuyTodayScales)
}

// flowSellWeight 净流出 → 卖出确认窗时长权重（1.0=不加权；0=未注入/关闭）。
//
// 裁决口径（§2.5，owner 原话"流出要管卖出，但加权要显著小于流入的买入加权，可以先做买入加权的
// 一半数值"）：强出买 0.70 ↔ 卖 0.85（惩罚的中和不足一半）、轻出买 0.85 ↔ 卖 0.95；
// 当日档再轻一档（强出 0.95 / 轻出 0.975）。5 日档与当日档**取最严**（任一命中即加权，
// 与 ComputeSellBoost 的 any-trigger 哲学一致），不连乘——两个读数描述的是同一件事
// （这只票在失血），连乘等于把一次失血罚两次。
//
// 与 L1/L2/L3 分层叠加是刻意的两维：tier 改**行为集**（取消延持/单源即 dual），
// 权重改**时长**（窗长×权重），互不覆盖。
// English: net-outflow → sell confirm-window weight. The strictest of the 5-day and same-day
// bands wins (never multiplied — one hemorrhage, one penalty). Coexists with the L1/L2/L3 tiers:
// tier changes the behavior set, the weight changes the duration.
func flowSellWeight(in SellInput, cfg config.SizingConfig) float64 {
	if !cfg.FlowSellOn() {
		return 0 // 卖出侧显式关 → 未注入
	}
	fc := flowCfgOf(cfg)
	w := 1.0
	// 5 日档：只有流出两档加权（流入/中性不缩短任何窗）。档位显式 0（进化器关该档）
	// → 该档不参与取最严（v>0 门），而不是把权重拉成 0——0 权重会被调用方当成"未注入"。
	// English: an explicit 0 disables that band's weight (v>0 gate); it must not zero the
	// whole weight, because 0 means "not injected" to the caller.
	if b := flowBandOf(in.FlowRatio5D, fc.SellBands); b == config.FlowBandStrongOut {
		if v := flowScaleOr(fc.SellScales, config.FlowBandStrongOut, 0.85); v > 0 && v < w {
			w = v
		}
	} else if b == config.FlowBandMildOut {
		if v := flowScaleOr(fc.SellScales, config.FlowBandMildOut, 0.95); v > 0 && v < w {
			w = v
		}
	}
	// 当日档（键名带 today_ 前缀，与买入档分开配——当日是实时读数，波动大，加权更轻）。
	if b := flowBandOf(in.FlowRatioToday, fc.SellBands); b == config.FlowBandStrongOut {
		if v := flowScaleOr(fc.SellScales, "today_strong_out", 0.95); v > 0 && v < w {
			w = v
		}
	} else if b == config.FlowBandMildOut {
		if v := flowScaleOr(fc.SellScales, "today_mild_out", 0.975); v > 0 && v < w {
			w = v
		}
	}
	return w
}

// flowWindowMin 窗长×权重（下限 1 分钟——再短就等于取消观察窗，那属于 L2+ 档位的职权）。
// English: window minutes × weight, floored at 1.
func flowWindowMin(min int, w float64) int {
	if w <= 0 || w >= 1 || min <= 0 {
		return min
	}
	v := int(float64(min) * w)
	if v < 1 {
		v = 1
	}
	return v
}

// FlowFactorReason 因子命中的人类可读理由（留痕/前端 tooltip 用）。
// English: readable reason for one fired flow factor.
func FlowFactorReason(name string, ratio float64, band string, v float64) string {
	return fmt.Sprintf("%s(%.2f%%,%s)×%.2f", name, ratio, band, v)
}
