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
func LimitUpPct(code, name string) float64 {
	if strings.Contains(strings.ToUpper(name), "ST") {
		return 4.9
	}
	head := strings.Split(code, ".")[0]
	switch {
	case strings.HasPrefix(head, "30"), strings.HasPrefix(head, "68"):
		return 19.9
	case strings.HasPrefix(head, "4"), strings.HasPrefix(head, "8"), strings.HasPrefix(head, "92"):
		return 29.9
	}
	return 9.9
}
