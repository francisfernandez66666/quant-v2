// tech_filter.go — §C4 买入技术过滤判定（纯函数）。
//
// 数据缺失一律弃权：KDJ J=0、CloseVsMA 缺键、均线不足时都不拦。只在“明确越过阈值”时命中。
// 卖出不走本函数（拦退出=强迫扛单）。
//
// English: §C4 buy-side technical filter. Missing inputs abstain; only an explicit threshold
// breach hits. Sells are never filtered here.
package signalctl

import (
	"fmt"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// techFilterHit 返回是否命中与原因；配置未启用或读数缺失时返回 false。
// English: reports a hit and the reason; disabled config or missing readings return false.
func techFilterHit(sig combat_agent.Signal, cfg config.TechFilterConfig) (bool, string) {
	if !cfg.Enabled() {
		return false, ""
	}
	if cfg.KDJJMax > 0 && sig.KDJ_J > cfg.KDJJMax {
		return true, fmt.Sprintf("KDJ J=%.1f > %.1f，不新开仓", sig.KDJ_J, cfg.KDJJMax)
	}
	below := func(key string) bool {
		r, ok := sig.CloseVsMA[key]
		return ok && r < 0
	}
	switch cfg.MAFilterOf() {
	case "ma20":
		if below("ma20") {
			return true, "收盘低于 MA20，不新开仓"
		}
	case "ma60":
		if below("ma60") {
			return true, "收盘低于 MA60，不新开仓"
		}
	case "ma120_250":
		if below("ma120") {
			return true, "收盘低于 MA120，不新开仓"
		}
		if below("ma250") {
			return true, "收盘低于 MA250，不新开仓"
		}
	}
	return false, ""
}
