package combat_agent

// derive_carrier.go —— 做空战法**派生窗口**的只读导出。
//
// 为什么加这一处（2026-11-05）
//   四战法在载体侧（qmt_gateway/rules_form.py）是各自重建指标的：MA5/MA10/MA20、
//   5 日与 20 日均量、60 日高、20 日低（不含今日）、近 20 日涨幅、近 3 日区间、
//   封板与触板的容忍、利好在窗天数。这些窗口在 Go 是 buildShortData 里的内联数字，
//   载体拿不到——以前是人手抄，抄错一位（或把"含今日/不含今日"抄反）没有任何东西会响，
//   而它改的是判据本身：11-04 那批把"抄"从流程里拿掉，值只许存在于 Go 的一处。
//
// 与"回溯根数"有关的口径（载体侧最容易抄反的一处，写死在这里）
//   不含今日的三族（20 日低 / 20 日涨幅 / 3 日区间），Go 起点是 n-(根数+1)：
//   今日占一根，所以根数 20 对应偏移 21。导出的是**根数**（与 rules_form 的
//   _f_tail_excl_last 同口径），不是偏移。偏移那一根是结构，不是阈值。
//   English: the exported value is the bar COUNT excluding today; the adapter adds the
//   structural +1 for the start index. Never export the offset as a threshold.
//
// 边界
//   本文件一个数字都没有：每一项都转调 adapter.go 里 buildShortData 正在用的常量。
//   只导出载体真要读的键；板幅容忍之外的结构性数字（比如 lowEnd 的 -1）不导出。
//   English: read-only export; every value delegates to the constant the adapter uses.

// DeriveCarrierWindows 返回做空派生指标的全部窗口与容忍（键名＝载体 params 键名）。
// 返回新建的 map：调用方（离线导出工具）改了也不该动到进程内的判定。
func DeriveCarrierWindows() map[string]interface{} {
	return map[string]interface{}{
		// 均线族（含今日的最后一根）
		"ma5_window":  shortMA5Period,
		"ma10_window": shortMA10Period,
		"ma20_window": shortMA20Period,
		// 量能族
		"vol5_window":  shortVol5Period,
		"vol20_window": shortVol20Period,
		// 位置族：60 日高含今日，20 日低不含今日
		"hc_high_window": shortHighBars,
		"bd_low_window":  shortLowBars,
		// 涨幅与滞涨族（都不含今日，导根数）
		"hc_gain_window":  shortGainBars,
		"hc_last3_window": shortStallBars,
		// 连板族：封板与触板的容忍（分钟回封用的是同一枚封板容忍，两处同源）
		"ld_seal_tolerance":  shortSealTolerance,
		"ld_touch_tolerance": shortTouchTolerance,
		// 事件族：利好在窗天数（战法侧的年龄衰减档在 good_news_fade 包里）
		"gf_event_window_days": shortEventWindowDay,
		// 龙回头族（dragonReturnDataFromMarketData 那一侧的派生窗口；评分档在
		// internal/strategies/dragon_return 包里，两处各出各的键、绝不互相代填）。
		// 三条均线窗口**与做空族同一批常量**（短长口径都是 5/10/20），因此这里出现两次是
		// **改名而不是复制数值**：载体侧 rules_ladder 按 dr_ma_* 读，rules_form 按 ma*_window 读，
		// 两边读到的是同一个 Go 常量——将来改一处，两个键一起动，不会漂成两条不同的 MA。
		"dr_min_bars":     drMinBars,
		"dr_ma_short":     shortMA5Period,
		"dr_ma_mid":       shortMA10Period,
		"dr_ma_long":      shortMA20Period,
		"dr_rally_window": drRallyBars,
		// 断板天量比（TodayVolVs5d）用的 5 日均量窗口：rules_ladder 与 rules_form 也是同一枚
		// 常量的两个名字（vol5_window / ld_vol_window）。
		"ld_vol_window": shortVol5Period,
	}
}
