package data

// derive_carrier.go —— 分板块涨跌停幅度与**时段**的**只读导出**。
//
// 为什么加这一处（2026-11-04）
//   QMT 内置策略与本地执行器要判"距涨/跌停还有多少"，需要主板 10%、创业/科创 20%、
//   北交所 30%、ST 5% 这几档。全系统唯一口径就是本文件的 LimitUpPct（§R6 P1-4 收敛过一次），
//   但它是函数、不是表——外部进程要么调它、要么自己抄一份数。抄的那份没有主人：
//   哪天主板规则变了，Go 侧改了这里，载体侧不会有任何东西响。
//
// 边界
//   这里一个数字都没有：四档全部**调用 LimitUpPct 本身**得到，探针代码只是"问哪一块板"，
//   不是答案。探针选在各自板块的中段（600/300/688/830），避免撞上边界写法。
//   English: read-only export; every band comes from calling LimitUpPct itself, no literal here.

import "time"

// LimitUpPctBands 返回四个板块档位的生效值。
// 键名与载体参数名同形，导出工具因此不需要再翻译一遍。
func LimitUpPctBands() map[string]float64 {
	return map[string]float64{
		"main": LimitUpPct("600000.SH", "测试主板"),
		"wide": LimitUpPct("300000.SZ", "测试创业板"),
		"star": LimitUpPct("688000.SH", "测试科创板"),
		"bse":  LimitUpPct("830000.BJ", "测试北交所"),
		"st":   LimitUpPct("600000.SH", "ST测试"),
	}
}

// LimitUpPctProbeCodes 打出上面用了哪几枚探针。
// 探针是**假设**而不是事实，所以必须可见：谁核对这几个数时能立刻看到"问的是谁"。
func LimitUpPctProbeCodes() map[string]string {
	return map[string]string{
		"main": "600000.SH",
		"wide": "300000.SZ",
		"star": "688000.SH",
		"bse":  "830000.BJ",
		"st":   "600000.SH + name contains ST",
	}
}

// LimitUpBoardTables 导出**板块归属的写法表**本身（前缀与名称标记）。
//
// 为什么四档百分比不够：载体移植同一条判据时是先按前缀/名称**分档**、再取该档的幅度，
// 所以它需要的是"哪些前缀算宽幅板、哪些算北交所、ST 用什么字样认"这三张表。
// 这三张表此前只存在于 limitup_pct.go 的 switch 里；现在 switch 与本函数读同一组
// 包级变量（limitUpWidePrefixes / limitUpBsePrefixes / limitUpMarkerST），
// 抄的那一份从此没有存在过。
// 返回的字符串是**判据的写法**而不是探针答案：它们是产品代码正在比较的那几个字面量。
// English: the board-spelling tables the limiter itself compares against; the carrier ports
// the same decision and must read the same table instead of re-typing the prefixes.
func LimitUpBoardTables() map[string]interface{} {
	return map[string]interface{}{
		"wide": append([]string(nil), limitUpWidePrefixes...),
		"bse":  append([]string(nil), limitUpBsePrefixes...),
		"st":   []string{limitUpMarkerST},
	}
}

// ActiveSessionWindows 导出 `IsActiveSession` 判真的**钟点区间表**（[起,止)，北京时 HH:MM）。
//
// 为什么载体要的是区间而不是一个 bool：做 T 的移植腿在自己的机器上没有 Go 的时钟可用，
// 它拿到的只有"现在几点"这一个读数，必须自己判"这一轮引擎到底会不会驱动判定链"。
// 引擎侧的门就是 intraday_t_live.go:222 的 `data.IsActiveSession(now)`，
// 而 CurrentSession 的四段（盘前/早盘/午休/午盘）全部算"活跃"、只有 [8:30, 收盘) 之外才假——
// 所以导出的就是那**一段连续区间**，起止分别取 sessionPreMarketStart 与 TradeClose
// （后者可被 SetTradeTimeConfig 覆盖，这里读的是**当下生效**的那枚，不是抄的常数）。
// 休市日那一维不在这里：它由 IsTradingDay 单独判，载体侧同名的一维走 ctx["trading_day"]，
// 把两维折进一张表会让"今天是休市"读成"现在不在这个钟点段里"——那是两种不同的弃权原因。
// 格式化只改**写法**（HHMM 整数 → HH:MM 串），不参与任何判定，也不产生新的数。
// English: exports the clock range where IsActiveSession is true, built from the same
// bounds the session switch compares against (half-open, Beijing time).
func ActiveSessionWindows() [][]string {
	return [][]string{{clockText(sessionPreMarketStart), clockText(defaultTradeTime.TradeClose)}}
}

// clockText 把 HHMM 整数写成 "HH:MM"。用 time 的格式化器而不是手写百分号，
// 是为了让这个文件里**一个数字都不出现**——它是渲染，不是判据。
func clockText(hhmm int) string {
	return time.Date(0, 1, 1, hhmm/100, hhmm%100, 0, 0, time.UTC).Format("15:04")
}
