package data

import "strings"

// LimitUpPct §R6 P1-4 分板块涨停近似阈值（百分比）——全系统唯一权威实现。
// 此前口径散落且不一致：paper.LimitUpPct 已分板块，但龙头战法 F1 封板判定硬编码 `>9.5%`
// （主板假设）——北交所 30% 板下 10% 远未封板却被误判"触及涨停"，导致 BJ 龙头误发信号。
// 现统一收敛到 data：模拟盘封板拒买（paper）、实盘 autoPlace 封板拒买（engine）、回测开盘即封板
// 判定（btreplay costOpenAtLimitUp）、龙头 F1 封板判定（dragon）均消费同一口径。
//
// 返回：ST/*ST→4.9、双创（30/68 开头）→19.9、北交所（4/8/92 开头）→29.9、主板→9.9。
// （English: authoritative board-aware limit-up threshold (%). Unified here so the paper/auto-place/
// backtest seal guards and the dragon F1 seal check share one semantic — fixing the old hardcoded 9.5
// that misread a 30% BJ board.）
// 板块归属的**写法表**集中在此（2026-11-05，§DERIVE-CARRIER-1105 补）。
//
// 为什么要把它们从 switch 里拿出来：载体（QMT 内置策略 / 本地执行器）移植同一条判准时，
// 需要的是"哪几个前缀算宽幅板、哪几个算北交所、ST 用什么字样认"这三张**表**，
// 而不是四档百分比——它自己也要按前缀分档。以前这些写法只活在 switch 的字面量里，
// 载体侧要么抄一份（抄对了也是两本账：改一处另一处不跟着动），要么自己猜。
// 现在判据与导出读的是同一组常量，改一次两边一起动。
// English: the board-spelling tables (ST marker, wide-board and BSE prefixes) are named here
// so the carrier reads the very same table this switch decides on.
const (
	limitUpMarkerST = "ST" // ST/*ST 的认法：名称里含这两个字母（Go 与载体同源）
)

// limitUpWidePrefixes 双创（创业板 30 / 科创板 68）前缀＝20% 档。
var limitUpWidePrefixes = []string{"30", "68"}

// limitUpBsePrefixes 北交所前缀（4 / 8 / 92 开头）＝30% 档。
var limitUpBsePrefixes = []string{"4", "8", "92"}

func hasAnyPrefix(head string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(head, p) {
			return true
		}
	}
	return false
}

func LimitUpPct(code, name string) float64 {
	if strings.Contains(strings.ToUpper(name), limitUpMarkerST) {
		return 4.9
	}
	head := strings.Split(code, ".")[0]
	switch {
	case hasAnyPrefix(head, limitUpWidePrefixes):
		return 19.9
	case hasAnyPrefix(head, limitUpBsePrefixes):
		return 29.9
	}
	return 9.9
}
