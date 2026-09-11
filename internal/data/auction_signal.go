// auction_signal.go — 竞价信号（§SIGNAL_EDGE_ENHANCEMENT_PLAN P1.2）。
// 用 hithink 竞价快照（auction 接口）的四维特征合成"竞价强度分"：
//   量比(VolumeRatio) 高开幅度(Pct) 未匹配占比(turnover) 换手
// 作为开盘强弱最早的官方信号；仅做打分池预排名/开盘确认增强，
// 不单独触发买入（竞价虚拟价必须由开盘实盘量价确认，见 combat_agent Volume=0 护栏）。
// English: auction signal (P1.2). Synthesizes an "auction strength score" from four dimensions
// of the hithink auction snapshot — volume ratio, open gap pct, unmatched ratio, turnover.
// Used only for pool pre-ranking / open confirmation, never a standalone buy trigger (the
// auction virtual price must be confirmed by live open volume; see combat_agent's Volume=0 guard).

package data

import "math"

// AuctionStrength 竞价强度分四维分解，供看板/日志展示合成过程。
// English: four-dimensional breakdown of the auction strength score (for board/log display).
type AuctionStrength struct {
	VolumeRatio    float64 `json:"volume_ratio"`    // 竞价量比（抢筹意愿）
	OpenPct        float64 `json:"open_pct"`        // 高开幅度 %
	UnmatchedRatio float64 `json:"unmatched_ratio"` // 未匹配量/竞价成交量 占比
	TurnoverPct    float64 `json:"turnover_pct"`    // 竞价换手 %
	Strength       float64 `json:"strength"`        // 合成强度分 [0,10]
	Phase          string  `json:"phase,omitempty"` // live / final
}

// cap 限幅辅助。
func capn(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// AuctionStrengthScore 竞价强度分合成：四维归一加权输出 [0,10]。
// 各项物理含义与归一区间（按 A 股竞价经验值校准）：
//
//	量比           已按倍率给出（0~20），加权 0.35；
//	高开幅度       -10%~+10% 线性映射到 [0,1]（高开=正项，低开=负项），加权 0.30；
//	未匹配占比     未匹配/成交量，0~3 归一（未匹配大=抛压大或抢筹未完，双向项），加权 0.20；
//	竞价换手率     0~2% 归一，加权 0.15。
//
// 输出用 10 分制，便于与既有打分体系对齐。
// English: synthesizes an auction strength score [0,10] from four normalized dimensions:
// volume ratio (width 0.35), open gap percent mapped -10%~+10% -> [0,1] (0.30), unmatched
// ratio to volume 0~3 (0.20, two-sided), auction turnover 0~2% (0.15).
func AuctionStrengthScore(it HithinkAuctionItem) float64 {
	vol := capn(it.AuctionVolumeRatio, 0, 20) / 20.0
	// 高开为正项：+10% → 1.0，0% → 0.5，-10% → 0.0
	open := capn((it.AuctionPct+10)/20, 0, 1)
	var unm float64
	if it.AuctionVolume > 0 {
		unm = it.AuctionUnmatched / it.AuctionVolume
	}
	umr := capn(unm, 0, 3) / 3.0
	turn := capn(it.AuctionTurnoverPct, 0, 2) / 2.0
	s := 0.35*vol + 0.30*open + 0.20*umr + 0.15*turn
	return math.Round(s*10) / 10
}

// AuctionStrengthBreakdown 便捷结构体：返回四维分解 + 合成分。
// English: convenient breakdown of strength dimensions with the composite score.
func AuctionStrengthBreakdown(it HithinkAuctionItem) AuctionStrength {
	return AuctionStrength{
		VolumeRatio:    it.AuctionVolumeRatio,
		OpenPct:        it.AuctionPct,
		UnmatchedRatio: safediv(it.AuctionUnmatched, it.AuctionVolume),
		TurnoverPct:    it.AuctionTurnoverPct,
		Strength:       AuctionStrengthScore(it),
	}
}

// safediv a/b，b<=0 返回 0。
func safediv(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b
}
