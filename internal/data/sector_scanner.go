// Package data — 热点板块扫描器。
// 基于多因子评分（D1 事件 + 量价 F 系列 + 主力资金）识别热点板块，
// 支持板块成分股展开和自选股扩展。
package data

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// HotSector 热点板块评分结果。
type HotSector struct {
	Sector SectorInfo // 板块基础信息（代码/名称/涨跌幅等）
	Score  float64    // 综合评分（D1 事件分 + F 系列量价分）
	Reason string     // 上榜原因描述（如"涨停潮 主力流入"）
	D1     float64    // D1 事件评分（预留字段）
}

// ScoredStock 板块内个股评分结果。
type ScoredStock struct {
	Code      string  `json:"code"`       // 股票代码
	Name      string  `json:"name"`       // 股票名称
	Price     float64 `json:"price"`      // 最新价（元）
	ChangePct float64 `json:"change_pct"` // 涨跌幅（%）
	Turnover  float64 `json:"turnover"`   // 换手率（%）
	Volume    float64 `json:"volume"`     // 成交量（股）
	Amount    float64 `json:"amount"`     // 成交额（元）
	Score     float64 `json:"score"`      // 0-100 综合分
}

// SectorScanner 板块扫描器。
// 对全量板块进行多因子评分，筛选热点板块；
// 结合 D1 事件匹配引擎，识别事件驱动型板块机会。
type SectorScanner struct {
	mu           sync.RWMutex
	cachedSector []SectorInfo      // 缓存的全量板块列表
	api          *MarketAPI        // 行情 API（用于拉取成分股）
	hotSectors   []HotSector       // 最新热点板块结果
	expandedList []string          // 展开后的自选股列表
	matcher      *EventMatcher     // D1 事件匹配器
	eventMap     map[string]string // 板块代码 → 事件描述
}

// NewSectorScanner 创建板块扫描器。
// matcher 可为 nil（不解 D1 事件评分）。
func NewSectorScanner(api *MarketAPI, matcher *EventMatcher) *SectorScanner {
	return &SectorScanner{
		api:     api,
		matcher: matcher,
	}
}

// SetEventMap 设置板块事件映射表。
// 映射格式：板块代码 → 事件描述文本（用于 D1 匹配）。
func (ss *SectorScanner) SetEventMap(m map[string]string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.eventMap = m
}

// Update 更新板块数据并重新评分。
// sectors 为全量板块列表，limitupBull/limitupShock 为涨停潮/异动阈值，
// maxCount 为最大返回热点板块数。
func (ss *SectorScanner) Update(sectors []SectorInfo, limitupBull, limitupShock, maxCount int) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.cachedSector = sectors
	ss.hotSectors = ss.scoreSectors(sectors, limitupBull, limitupShock, maxCount)
}

// HotSectors 返回当前热点板块列表（副本）。
func (ss *SectorScanner) HotSectors() []HotSector {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	out := make([]HotSector, len(ss.hotSectors))
	copy(out, ss.hotSectors)
	return out
}

// EventDesc 返回板块对应的 D1 事件描述（来自新闻事件映射），无则返回空串。
func (ss *SectorScanner) EventDesc(code string) string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.eventMap[code]
}

// ExpandedStocks 返回展开后的自选股列表（副本）。
func (ss *SectorScanner) ExpandedStocks() []string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	out := make([]string, len(ss.expandedList))
	copy(out, ss.expandedList)
	return out
}

// sectorBlocked 检查板块是否被负面事件阻断。
func (ss *SectorScanner) sectorBlocked(code string) bool {
	if ss.matcher == nil || ss.eventMap == nil {
		return false
	}
	desc, ok := ss.eventMap[code]
	if !ok || desc == "" {
		return false
	}
	mr := ss.matcher.MatchD1(desc)
	return mr.Blocked
}

// fSeriesScore 计算纯量价 F 系列评分（0-100）。
// 综合：涨停家数权重 40%、涨跌幅权重（正值×2、负值×1）、
// 成交额权重 20%、主力净流入权重 15%，取各部分均值。
func fSeriesScore(s SectorInfo, maxLimitup int, maxAmt, maxInflow float64) float64 {
	score := 0.0
	parts := 0

	if maxLimitup > 0 {
		score += float64(s.LimitupCnt) / float64(maxLimitup) * 40
		parts++
	}
	if s.ChangePct > 0 {
		score += s.ChangePct * 2
		parts++
	} else {
		score += s.ChangePct
	}
	if maxAmt > 0 {
		score += s.Amount / maxAmt * 20
		parts++
	}
	if maxInflow > 0 {
		inflowScore := s.NetInflow / maxInflow * 15
		score += inflowScore
		parts++
	}
	if parts > 0 {
		score /= float64(parts)
	}
	return score
}

// scoreSectors 对全量板块进行 D1+F 双维度评分。
// 评分 = D1事件分(带关联度权重) + F系列量价评分
// 兜底策略：无任何板块通过时取涨跌幅前 maxCount 名。
func (ss *SectorScanner) scoreSectors(sectors []SectorInfo, limitupBull, limitupShock, maxCount int) []HotSector {
	if len(sectors) == 0 {
		return nil
	}

	// 计算各指标最大值用于归一化
	maxLimitup := 1
	maxAmt := 1.0
	maxInflow := 1.0
	for _, s := range sectors {
		if s.LimitupCnt > maxLimitup {
			maxLimitup = s.LimitupCnt
		}
		if s.Amount > maxAmt {
			maxAmt = s.Amount
		}
		in := s.NetInflow
		if in < 0 {
			in = -in
		}
		if in > maxInflow {
			maxInflow = in
		}
	}

	scored := make([]HotSector, 0, len(sectors))

	for _, s := range sectors {
		if ss.sectorBlocked(s.Code) {
			continue
		}
		fScore := fSeriesScore(s, maxLimitup, maxAmt, maxInflow)
		total := fScore

		// 按涨停家数与涨跌幅生成上榜原因（涨停潮/异动/领涨/领跌），再追加主力资金方向
		reason := ""
		if s.LimitupCnt >= limitupBull {
			reason = "涨停潮"
		} else if s.LimitupCnt >= limitupShock {
			reason = "异动"
		} else if s.ChangePct > 3 {
			reason = "领涨"
		} else if s.ChangePct < -3 {
			reason = "领跌"
		}
		if s.NetInflow > 0 {
			if reason != "" {
				reason += " "
			}
			reason += "主力流入"
		} else if s.NetInflow < 0 {
			if reason != "" {
				reason += " "
			}
			reason += "主力流出"
		}
		if reason == "" {
			reason = "常规"
		}

		scored = append(scored, HotSector{
			Sector: s,
			Score:  total,
			Reason: reason,
		})
	}

	// 按总分降序排列
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if maxCount <= 0 {
		maxCount = 5
	}
	if len(scored) > maxCount {
		scored = scored[:maxCount]
	}

	// 兜底：没有任何板块通过时，取涨跌幅前 maxCount 名
	if len(scored) == 0 {
		byPct := make([]HotSector, 0, len(sectors))
		for _, s := range sectors {
			byPct = append(byPct, HotSector{Sector: s, Score: s.ChangePct, Reason: "领涨"})
		}
		sort.Slice(byPct, func(i, j int) bool {
			return byPct[i].Score > byPct[j].Score
		})
		n := maxCount
		if n > len(byPct) {
			n = len(byPct)
		}
		scored = byPct[:n]
	}

	return scored
}

// BuildEventMapFromNews 从新闻中构建 板块名称→事件描述 映射。
// 为看板展示填充板块事件数据。
// base 为已有映射（追加而非覆盖）。
func (ss *SectorScanner) BuildEventMapFromNews(news []NewsItem, base map[string]string) map[string]string {
	out := make(map[string]string, len(base))
	for k, v := range base {
		out[k] = v
	}
	if len(news) == 0 {
		return out
	}

	ss.mu.RLock()
	sectors := ss.cachedSector
	ss.mu.RUnlock()

	for _, n := range news {
		text := n.Title + " " + n.Content
		textLower := strings.ToLower(text)

		for _, s := range sectors {
			secName := strings.ToLower(s.Name)
			if secName == "" {
				continue
			}
			if strings.Contains(textLower, secName) {
				if _, exists := out[s.Code]; !exists {
					out[s.Code] = n.Title
				}
			}
		}
	}
	return out
}

// ScoreSectorStocks 对板块内个股进行多因子量化打分（0-100）。
// 因子：涨跌幅(0-35) + 换手率(0-25) + 成交额(0-20) + 量能(0-20) + 龙头加成(0-15)
// 返回按总分降序排列的 ScoredStock 切片。
func (ss *SectorScanner) ScoreSectorStocks(sectorCode string, maxStocks int) ([]ScoredStock, error) {
	allStocks, err := ss.api.GetSectorStocks(sectorCode, 100)
	if err != nil || len(allStocks) == 0 {
		return nil, fmt.Errorf("no stocks for sector %s", sectorCode)
	}

	scored := make([]ScoredStock, 0, len(allStocks))
	for _, st := range allStocks {
		si := ScoredStock{
			Code: st.Code, Name: st.Name,
			Price: st.Price, ChangePct: st.ChangePct,
			Turnover: st.Turnover, Volume: st.Volume, Amount: st.Amount,
		}
		s := 0.0

		// ① 日内涨跌幅 (0-35)
		switch {
		case st.ChangePct >= 9:
			s += 35
		case st.ChangePct >= 7:
			s += 32
		case st.ChangePct >= 5:
			s += 28
		case st.ChangePct >= 3:
			s += 22
		case st.ChangePct >= 1:
			s += 15
		case st.ChangePct >= 0:
			s += 8
		case st.ChangePct >= -2:
			s += 4
		default:
			s += 1
		}

		// ② 换手率 (0-25)
		switch {
		case st.Turnover >= 10:
			s += 25
		case st.Turnover >= 7:
			s += 22
		case st.Turnover >= 5:
			s += 18
		case st.Turnover >= 3:
			s += 12
		case st.Turnover >= 1:
			s += 6
		default:
			s += 2
		}

		// ③ 成交额活跃度 (0-20)
		switch {
		case st.Amount >= 2e9:
			s += 20
		case st.Amount >= 1e9:
			s += 15
		case st.Amount >= 5e8:
			s += 10
		case st.Amount >= 1e8:
			s += 5
		default:
			s += 2
		}

		// ④ 量能强度 (0-20)：上涨放量高分，下跌放量扣分
		if st.ChangePct > 0 && st.Volume > 0 {
			if st.Amount >= 5e8 {
				s += 20
			} else if st.Amount >= 1e8 {
				s += 12
			} else {
				s += 5
			}
		} else if st.ChangePct <= 0 && st.Volume > 0 {
			s += 3
		}

		si.Score = s
		scored = append(scored, si)
	}

	// ⑤ 龙头加成 (0-15)：涨幅前 20% 且上涨的股票
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].ChangePct > scored[j].ChangePct
	})
	leaderCount := (len(scored) + 4) / 5
	if leaderCount < 1 {
		leaderCount = 1
	}
	for i := 0; i < leaderCount && i < len(scored); i++ {
		if scored[i].ChangePct > 0 {
			scored[i].Score += 15
		}
	}

	// 按总分降序排列
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	// 截断到 100
	for i := range scored {
		if scored[i].Score > 100 {
			scored[i].Score = 100
		}
	}

	if maxStocks > 0 && len(scored) > maxStocks {
		scored = scored[:maxStocks]
	}

	return scored, nil
}

// ExpandWatchlist 根据热点板块展开自选股列表。
// 从每个热点板块取 topN 只成分股，合并到 baseWatchlist 中。
func (ss *SectorScanner) ExpandWatchlist(hotSectors []HotSector, baseWatchlist []string, topN int) ([]string, error) {
	codeSet := make(map[string]bool)
	if baseWatchlist != nil {
		for _, c := range baseWatchlist {
			codeSet[c] = true
		}
	}

	for _, hs := range hotSectors {
		if topN <= 0 {
			topN = 3
		}
		stocks, err := ss.api.GetSectorStocks(hs.Sector.Code, topN)
		if err != nil {
			continue
		}
		for _, st := range stocks {
			if !codeSet[st.Code] {
				codeSet[st.Code] = true
			}
		}
	}

	result := make([]string, 0, len(codeSet))
	for c := range codeSet {
		result = append(result, c)
	}

	sort.Strings(result)

	ss.mu.Lock()
	ss.expandedList = result
	ss.mu.Unlock()

	return result, nil
}

// FindSectorsByNames 接收板块名称列表，从缓存的板块 Map 中匹配并返回对应的 SectorInfo（不区分大小写）。
// 供 LLM 热点板块扩展使用：将 LLM 识别的板块名称映射为代码库中的板块对象，以便后续展开成分股。
func (ss *SectorScanner) FindSectorsByNames(names []string) []SectorInfo {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[strings.ToLower(strings.TrimSpace(n))] = true
	}
	var out []SectorInfo
	for _, s := range ss.cachedSector {
		if nameSet[strings.ToLower(strings.TrimSpace(s.Name))] {
			out = append(out, s)
		}
	}
	return out
}
