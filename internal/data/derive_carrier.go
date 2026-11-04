package data

// derive_carrier.go —— 分板块涨跌停幅度的**只读导出**。
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
