// linkage.go — 板块联动交易（§SIGNAL_EDGE_ENHANCEMENT_PLAN P2.2）。
// 龙头封板（封单强度 × 连板高度）确立板块强度，同板块未涨停的龙二/龙三按
// 板块资金净流入 + 候选换手活跃度生成补涨联动候选。
// 仅生成候选并供打分池并入（不脱离既有纪律）；开关 Enhance.SectorLinkage 关闭时零操作。
// 自包含类型，不依赖 data 包具体源，便于测试。
// English: sector linkage trading (P2.2). A limit-up leader (seal strength × board height) anchors
// sector strength; same-sector non-limit-up candidates (2nd/3rd linchpin) become catch-up candidates
// when sector net inflow is positive and the candidate turnover is healthy. Only generates
// candidates into the scoring pool (never bypasses existing discipline); no-op unless the toggle is on.
// Self-contained types (independent of specific data sources) for clean tests.

package sector_agent

import (
	"sort"
)

// LinkageLeader 龙头定义：封板个股 + 板块归属。
type LinkageLeader struct {
	Code        string  // 股票代码
	Name        string  // 股票名称
	Sector      string  // 所属概念/行业板块
	SealRatio   float64 // 封单额/流通市值（%）
	BoardHeight int     // 连板高度
	ChangePct   float64 // 涨跌幅（%）
}

// LinkageCandidate 联动补涨候选（龙二/龙三）。
type LinkageCandidate struct {
	LeaderCode   string  // 龙头代码（联动锚）
	LeaderName   string  // 龙头名称
	Code         string  // 候选代码
	Name         string  // 候选名称
	Sector       string  // 板块
	Turnover     float64 // 候选实时换手率（%）
	ConfirmScore float64 // 联动置信分（0~1）
	// Reasons 联动成立原因摘要（观察字段）。English: why it linked (observation).
	Reasons []string
}

// LinkageDefaults 联动默认阈值（按 A 股经验校准，可经配置覆盖）。
var LinkageDefaults = struct {
	MinSealRatio      float64 // 龙头最小封单/流通市值 %
	MinBoardHeight    int     // 龙头最小连板高度
	MinSectorFlow     float64 // 板块最小主力净流入（元）
	MinTurnover       float64 // 候选最小换手率 %
	MaxLeaderDistance int     // 龙头封板距现在的最长时长（分钟，0=不限）
}{
	MinSealRatio:   3.0,
	MinBoardHeight: 2,
	MinSectorFlow:  5e7, // 5000 万
	MinTurnover:    3.0,
}

// IsLeader 是否满足龙头条件：封单强度 + 连板高度。
// English: qualifies as a sector leader by seal strength + board height.
func IsLeader(l LinkageLeader) bool {
	return l.SealRatio >= LinkageDefaults.MinSealRatio &&
		l.BoardHeight >= LinkageDefaults.MinBoardHeight
}

// LeaderStrength 龙头强度分（封单比 × 连板加权，0~10）。用于联动候选排序与展示。
func LeaderStrength(l LinkageLeader) float64 {
	if !IsLeader(l) {
		return 0
	}
	s := l.SealRatio * (float64(l.BoardHeight) / float64(LinkageDefaults.MinBoardHeight))
	if s > 10 {
		s = 10
	}
	return s
}

// FindLinkageCandidates 由龙头列表 + 板块成分股 + 板块资金流生成联动候选。
// 条件：
//  1. 龙头满足 IsLeader；
//  2. 候选属于同板块且未封板（不在 leaders 内）；
//  3. 板块主力净流入 sectorFlow>=MinSectorFlow；
//  4. 候选换手率 >= MinTurnover。
//
// 返回按 ConfirmScore 降序。English: builds linkage candidates from leaders + sector
// constituents + sector cash flow. Conditions: leader passes IsLeader; candidate shares the
// sector and is not itself a limit-up leader; sector net inflow >= MinSectorFlow; candidate
// turnover >= MinTurnover. Sorted by ConfirmScore descending.
func FindLinkageCandidates(leaders []LinkageLeader, sectorConstituents map[string][]LinkageCandidate, sectorFlow map[string]float64) []LinkageCandidate {
	if len(leaders) == 0 {
		return nil
	}
	leaderByCode := make(map[string]bool, len(leaders))
	for _, ld := range leaders {
		leaderByCode[ld.Code] = true
	}
	var out []LinkageCandidate
	for _, ld := range leaders {
		if !IsLeader(ld) {
			continue
		}
		if f, ok := sectorFlow[ld.Sector]; !ok || f < LinkageDefaults.MinSectorFlow {
			continue
		}
		for _, cand := range sectorConstituents[ld.Sector] {
			if cand.Code == ld.Code || leaderByCode[cand.Code] {
				continue // 龙头自身或已封板个股跳过
			}
			if cand.Turnover < LinkageDefaults.MinTurnover {
				continue
			}
			score := linkageConfirmScore(ld, cand)
			if score <= 0 {
				continue
			}
			out = append(out, LinkageCandidate{
				LeaderCode:   ld.Code,
				LeaderName:   ld.Name,
				Code:         cand.Code,
				Name:         cand.Name,
				Sector:       ld.Sector,
				ConfirmScore: score,
				Reasons:      append([]string{}, cand.Reasons...),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConfirmScore > out[j].ConfirmScore })
	// 按候选代码去重（同板块多龙头并列时，同一候选只保留最强锚的那条）。
	// English: dedup by candidate code — when several leaders share one sector, a candidate is
	// emitted once under its strongest anchor.
	best := make(map[string]LinkageCandidate, len(out))
	for _, c := range out {
		if prev, ok := best[c.Code]; !ok || c.ConfirmScore > prev.ConfirmScore {
			best[c.Code] = c
		}
	}
	deduped := make([]LinkageCandidate, 0, len(best))
	for _, c := range out {
		if b, ok := best[c.Code]; ok && b.ConfirmScore == c.ConfirmScore && b.LeaderCode == c.LeaderCode {
			deduped = append(deduped, c)
			delete(best, c.Code)
		}
	}
	return deduped
}

// linkageConfirmScore 联动置信分（0~1）：龙头强度与候选换手各半。
func linkageConfirmScore(ld LinkageLeader, cand LinkageCandidate) float64 {
	strength := LeaderStrength(ld) / 10 // 0~1
	turn := cand.Turnover / 15.0        // 换手 15% 视为满分
	if turn > 1 {
		turn = 1
	}
	return strength*0.6 + turn*0.4
}