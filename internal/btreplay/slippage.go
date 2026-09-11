// slippage.go 回测增强运行期装配：Options.Backtest → slipCtx（A0/A 引擎侧入口）。
//
// 回退链（A.3 定稿）：每战法一次 paper_trades 实测校准（战法级样本不足回退全局）→
// 配置表值 → 内置默认；nil Backtest / Enabled=false 全程返回 nil = 旧行为。
// English: per-strategy assembly of the dynamic-slippage context with the documented
// fallback chain (paper median → config → built-in default); nil context = legacy behavior.
package btreplay

import (
	"log"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
)

// activeBacktest 返回生效的增强配置（nil=不启用）；首次调用时回填内置默认（A0.3）。
func (o *Options) activeBacktest() *config.BacktestConfig {
	if !o.Backtest.Active() {
		return nil
	}
	o.Backtest.FillDefaults()
	return o.Backtest
}

// buildSlipCtx 为单战法构建动态滑点上下文 + 校准审计简报。
// 校准分组键 = paper.PoolKeyForStrategy(显示名, 规则ID)（strategy_type 池键口径，
// 与盘后导出同源）；战法级双向样本不足 CalibMinSample 时回退全局样本再试一次。
// English: builds the per-strategy slippage context; calibration groups by the paper pool
// key (same mapping as the export) with a global-sample fallback.
func (o *Options) buildSlipCtx(db *store.DB, name, kind string) (*slipCtx, map[string]any) {
	bt := o.activeBacktest()
	if bt == nil {
		return nil, nil
	}
	var calib *store.SlippageCalib
	if bt.Slippage.AutoCalibrate && db != nil {
		window := bt.Slippage.CalibWindowDays
		poolKey := paper.PoolKeyForStrategy(name, kind)
		if c, err := db.PaperSlippageCalib(poolKey, window); err == nil && c != nil {
			calib = c
			// 战法级不足 → 回退全局样本（A.3 分组口径）
			if poolKey != "" && (calib.BuyN < bt.Slippage.CalibMinSample || calib.SellN < bt.Slippage.CalibMinSample) {
				if g, gerr := db.PaperSlippageCalib("", window); gerr == nil && g != nil &&
					g.BuyN >= bt.Slippage.CalibMinSample && g.SellN >= bt.Slippage.CalibMinSample {
					calib = g
				}
			}
		}
	}
	base, extra, audit := calibAudit(bt, calib)
	cfg := bt.Slippage
	cfg.BaseBps = base
	cfg.BuyExtraBps = extra
	sc := &slipCtx{
		baseBps:    base,
		slip:       cfg,
		orderValue: bt.OrderValueYuan,
		liq:        bt.Liquidity,
		liqOn:      bt.Liquidity.Enabled,
	}
	if audit != nil {
		audit["order_value_yuan"] = sc.orderValue
		log.Printf("[btreplay] %s 滑点校准 source=%v base=%.2fbps extra=%.2fbps 名义额%.0f元",
			name, audit["source"], base, extra, sc.orderValue)
	}
	return sc, audit
}
