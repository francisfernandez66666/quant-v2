// 涨停池分析：龙头识别评分 + 涨停原因分类。
// Limit-up pool analysis: leader identification scoring + limit-up reason classification.
// 龙头评分借鉴 astock-market-engine dragon_leader_engine：连板25+封板15+封单比10+板块影响力15+首封时间10+换手10+板块排名10+舆情5。
// The leader score borrows from astock-market-engine dragon_leader_engine:
// consecutive limit-ups 25 + seal 15 + seal ratio 10 + industry influence 15 + first-seal time 10 + turnover 10 + industry rank 10 + sentiment 5.
// 输出龙头信号（评分≥70 且板块内前10）与预期差提醒信号，供 ScanLimitUp 扫描使用。
// Produces leader signals (score >= 70 and within the industry top 10) and expectation-gap alerts for ScanLimitUp.
package combat_agent

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
)

// LeaderInfo 识别出的板块龙头。
// LeaderInfo is an identified industry leader stock.
//   - Stock: original limit-up pool entry (with consecutive-seal/seal-ratio/first-seal-time raw data)
//   - Score: leader score 0-100
//   - Rank: ranking within the industry (1-based, by descending score)
//   - Reason: scoring summary (consecutive seals / seal ratio / first-seal time)
type LeaderInfo struct {
	Stock  data.LimitUpStock // 对应涨停池原始条目（含连板/封单/首封时间等原始数据）
	Score  float64           // 龙头评分 0-100
	Rank   int               // 板块内排名（1 起，按评分降序）
	Reason string            // 评分理由（连板/封单/首封时间摘要）
}

// LimitUpAnalysis 涨停池分析结果。
// LimitUpAnalysis is the analysis result of the whole limit-up pool.
//   - Total: total number of limit-up stocks
//   - Leaders: leader stocks sorted by descending score
//   - ByType: statistics of limit-up reasons (policy/earnings/theme/sentiment/technical)
//   - HotIndustry: the industry with the most limit-up stocks
type LimitUpAnalysis struct {
	Total       int            // 涨停总数
	Leaders     []LeaderInfo   // 龙头（按评分降序）
	ByType      map[string]int // 涨停原因分类统计（政策/业绩/题材/舆情/情绪技术）
	HotIndustry string         // 涨停家数最多的行业
}

// lianBanScore 连板分档：5板+满25，3-4板20，2板14，1板8。
// lianBanScore ranks consecutive limit-up tiers: 5+ boards = 25, 3-4 boards = 20, 2 boards = 14, 1 board = 8.
func lianBanScore(lb int) float64 {
	switch {
	case lb >= 5:
		return 25
	case lb >= 3:
		return 20
	case lb >= 2:
		return 14
	default:
		return 8
	}
}

// sealRatioScore 封单占流通市值比：>=5%满10，>=2%得8，>=1%得6，>=0.5%得4，否则2。
// sealRatioScore scores the seal order / float market cap ratio: >=5% = 10, >=2% = 8, >=1% = 6, >=0.5% = 4, otherwise 2.
func sealRatioScore(r float64) float64 {
	switch {
	case r >= 5:
		return 10
	case r >= 2:
		return 8
	case r >= 1:
		return 6
	case r >= 0.5:
		return 4
	default:
		return 2
	}
}

// sealTimeScore 首封时间分：09:30前满10，10:00前8，10:30前6，11:30前4，14:00前2，午后1。
// sealTimeScore scores the first-seal time: before 09:30 = 10, before 10:00 = 8, before 10:30 = 6, before 11:30 = 4,
// before 14:00 = 2, otherwise (afternoon) = 1.
func sealTimeScore(t string) float64 {
	switch {
	case t <= "09:30":
		return 10
	case t <= "10:00":
		return 8
	case t <= "10:30":
		return 6
	case t <= "11:30":
		return 4
	case t <= "14:00":
		return 2
	default:
		return 1
	}
}

// turnoverScore 换手分：3-15%满10，15-25%得7，25-35%得5，>35%得3，<3%得6。
// turnoverScore scores turnover: 3-15% = 10, 15-25% = 7, 25-35% = 5, >35% = 3, <3% = 6.
func turnoverScore(t float64) float64 {
	switch {
	case t >= 3 && t <= 15:
		return 10
	case t > 15 && t <= 25:
		return 7
	case t > 25 && t <= 35:
		return 5
	case t > 35:
		return 3
	default:
		return 6
	}
}

// industryInfluenceScore 板块影响力：该行业涨停家数占全池前 3 名分别得 15/12/9，其余 4。
// industryInfluenceScore scores industry influence: industries with the top-3 limit-up counts across the pool
// get 15/12/9 respectively, all others get 4.
func industryInfluenceScore(industry string, industryCnt map[string]int, total int) float64 {
	// 全池无涨停（total==0）时无板块影响力可言，给基准分 4
	// No limit-ups in the pool (total==0): no industry influence, use the baseline 4.
	if total == 0 {
		return 4
	}
	// 将行业计数转成可排序的临时结构
	// Convert industry counts into a sortable temporary struct.
	type ic struct {
		name  string
		count int
	}
	list := make([]ic, 0, len(industryCnt))
	for n, c := range industryCnt {
		list = append(list, ic{n, c})
	}
	// 按涨停家数降序排列 → 排名即板块热度
	// Sort by limit-up count descending -> ranking reflects industry heat.
	sort.Slice(list, func(i, j int) bool { return list[i].count > list[j].count })
	for i, v := range list {
		if v.name != industry {
			continue
		}
		// 涨停家数全池前 3 的行业分别得 15/12/9 分，其余 4 分
		// The top-3 industries get 15/12/9, all others get 4.
		switch i {
		case 0:
			return 15
		case 1:
			return 12
		case 2:
			return 9
		default:
			return 4
		}
	}
	return 4
}

// industryRankScore 板块内排名（同行业按涨停时间越早越靠前）：第1名10分，第2名7，第3名5，其余2。
// industryRankScore scores the rank within an industry (earlier first-seal time ranks higher):
// rank 1 = 10, rank 2 = 7, rank 3 = 5, others = 2.
func industryRankScore(industry string, industryStocks map[string][]data.LimitUpStock) float64 {
	group, ok := industryStocks[industry]
	if !ok {
		return 2
	}
	// 已按首封时间升序（涨停池接口默认 sort=fbt:asc，但本地再确保一次）
	// Already sorted by first-seal time ascending (pool API defaults to sort=fbt:asc, but enforce it again locally).
	sort.Slice(group, func(i, j int) bool { return group[i].FirstSeal < group[j].FirstSeal })
	for i := range group {
		// 同行业内首封越早名次越高，前 3 名给 10/7/5 分，其余 2 分
		// Within an industry, earlier seals rank higher; the top 3 get 10/7/5, the rest get 2.
		switch i {
		case 0:
			return 10
		case 1:
			return 7
		case 2:
			return 5
		default:
			return 2
		}
	}
	return 2
}

// ScoreLeader 计算单只涨停股的龙头评分（0-100）。
// ScoreLeader computes the leader score (0-100) for a single limit-up stock.
// industryCnt/industryStocks 为全池统计，用于板块影响力与板块排名维度。
// industryCnt/industryStocks are whole-pool statistics used for the industry influence and industry rank dimensions.
// 返回 (评分, 评分理由摘要)；各维度按固定权重累加，总和不超过 100。
// Returns (score, scoring summary); each dimension accumulates with fixed weights, capped at 100.
func ScoreLeader(s data.LimitUpStock, industryCnt map[string]int, industryStocks map[string][]data.LimitUpStock, total int) (float64, string) {
	score := 0.0
	score += lianBanScore(s.LianBan) // 连板 25
	// 封板 15：炸板（BreakCount）一次扣 2 分，保底下限 5
	// Seal 15: each break (BreakCount) deducts 2, with a floor of 5.
	seal := 15 - float64(s.BreakCount)*2
	if seal < 5 {
		seal = 5
	}
	score += seal
	score += sealRatioScore(s.SealRatio)                            // 封单比 10
	score += industryInfluenceScore(s.Industry, industryCnt, total) // 板块影响力 15
	score += sealTimeScore(s.FirstSeal)                             // 首封时间 10
	score += turnoverScore(s.Turnover)                              // 换手 10
	score += industryRankScore(s.Industry, industryStocks)          // 板块排名 10
	score += 3                                                      // 舆情 5（缺省3，后续接入新闻情感）

	// 评分理由摘要：连板数 + 封单占比 + 首封时间
	// Scoring summary: consecutive seal count + seal ratio + first-seal time.
	reasons := []string{
		fmt.Sprintf("连板%d", s.LianBan),
		fmt.Sprintf("封单%.1f%%", s.SealRatio),
		fmt.Sprintf("%s首封", s.FirstSeal),
	}
	return score, strings.Join(reasons, "·")
}

// AnalyzeLimitUp 分析整个涨停池：统计涨停家数、识别龙头、分类涨停原因。
// AnalyzeLimitUp analyzes the whole limit-up pool: counts limit-ups, identifies leaders, classifies limit-up reasons.
// 入参 pool 为当日涨停池，newsTitles 为 code → 关联新闻标题列表（用于涨停原因分类）。
// pool is the day's limit-up pool; newsTitles maps code -> related news titles (used for reason classification).
// 返回按评分降序的龙头列表与分类统计。
// Returns the leader list sorted by descending score plus the classification statistics.
func AnalyzeLimitUp(pool []data.LimitUpStock, newsTitles map[string][]string) LimitUpAnalysis {
	res := LimitUpAnalysis{
		Total:  len(pool),
		ByType: make(map[string]int),
	}
	// 空池直接返回零值结果
	// Empty pool: return the zero-value result directly.
	if len(pool) == 0 {
		return res
	}

	// 先按行业聚合，供板块影响力/板块排名维度统计
	// Aggregate by industry first for the influence/rank dimensions.
	industryCnt := make(map[string]int)
	industryStocks := make(map[string][]data.LimitUpStock)
	for _, s := range pool {
		industryCnt[s.Industry]++
		industryStocks[s.Industry] = append(industryStocks[s.Industry], s)
	}

	// 涨停家数最多的行业 → 今日热点行业
	// The industry with the most limit-ups -> today's hot industry.
	maxCnt := 0
	for name, c := range industryCnt {
		if c > maxCnt {
			maxCnt = c
			res.HotIndustry = name
		}
	}

	leaders := make([]LeaderInfo, 0, len(pool))
	for _, s := range pool {
		// 每只涨停股算龙头分 + 分类涨停原因（写入 LimitType 并统计）
		// For each stock: compute the leader score and classify its limit-up reason (write LimitType and count it).
		score, reason := ScoreLeader(s, industryCnt, industryStocks, len(pool))
		s.LimitType = ClassifyLimitUp(s, newsTitles[s.Code])
		res.ByType[s.LimitType]++
		leaders = append(leaders, LeaderInfo{
			Stock:  s,
			Score:  score,
			Reason: reason,
		})
	}
	// 按龙头评分降序排序并回填排名（1 起）
	// Sort leaders by descending score and fill in 1-based ranks.
	sort.Slice(leaders, func(i, j int) bool { return leaders[i].Score > leaders[j].Score })
	for i := range leaders {
		leaders[i].Rank = i + 1
	}
	res.Leaders = leaders
	return res
}

// limitUpTypeRules 涨停原因关键词规则：按优先级匹配，命中即归为该类。
// limitUpTypeRules maps limit-up reasons to keyword rules, matched by priority; the first hit determines the category.
var limitUpTypeRules = []struct {
	Type     string
	Keywords []string
}{
	{"政策驱动", []string{"政策", "国务院", "发改委", "规划", "通知", "意见", "补贴", "专项", "试点", "改革", "条例", "标准", "监管", "部委", "地方债", "特别国债"}},
	{"业绩驱动", []string{"业绩", "预增", "扭亏", "净利润", "营收", "中报", "年报", "一季报", "三季报", "分红", "回购", "超预期", "增长"}},
	{"题材事件", []string{"中标", "签约", "合同", "并购", "重组", "定增", "股权", "新产品", "发布", "突破", "量产", "订单", "合作", "涨价", "获批", "获批上市"}},
	{"消息舆情", []string{"传闻", "报道", "消息", "互动易", "调研", "纪要", "关注函", "澄清", "热搜", "龙虎榜", "异动", "关注"}},
}

// ClassifyLimitUp 对单只涨停股分类涨停原因。
// ClassifyLimitUp classifies the limit-up reason for a single stock.
// newsTitles 为该股当日关联新闻标题；无命中关键词或新闻时归为"情绪技术"（连板情绪/超跌反弹驱动）。
// newsTitles is the day's related news titles; with no keyword hit or no news, it falls back to
// "情绪技术" (sentiment/technical, driven by consecutive-seal sentiment or oversold rebound).
func ClassifyLimitUp(s data.LimitUpStock, newsTitles []string) string {
	if len(newsTitles) == 0 {
		return "情绪技术"
	}
	text := strings.ToLower(strings.Join(newsTitles, " "))
	for _, r := range limitUpTypeRules {
		for _, k := range r.Keywords {
			if strings.Contains(text, strings.ToLower(k)) {
				return r.Type
			}
		}
	}
	return "情绪技术"
}

// newsTitlesOf 提取个股关联新闻标题列表。
// newsTitlesOf extracts the list of news titles related to a stock code.
func newsTitlesOf(news map[string][]NewsBrief, code string) []string {
	var out []string
	for _, nb := range news[code] {
		out = append(out, nb.Title)
	}
	return out
}

// leaderThreshold 龙头评分触发线：≥70 视为板块龙头。
// leaderThreshold is the trigger line for a leader score: >= 70 means an industry leader.
const leaderThreshold = 70.0

// leaderBuyThreshold 龙头识别买入线：评分 ≥60 且排名前 10 → 直接发 buy 买入信号
// （放宽买入层级：用户要求其他战法适当放开买入控制，龙头识别统一到 60 分即可买）。
// 低于该线仍发 watch 观察。English: the leader-buy threshold — score ≥60 and ranked top-10 fires a
// buy signal directly (relaxed buy gate per request: leader identification buys at 60). Below the line
// it stays a watch signal.
const leaderBuyThreshold = 60.0

// ScanLimitUp 涨停池分析扫描：识别龙头 + 涨停原因分类 + 预期差检测。
// ScanLimitUp scans the limit-up pool: leader identification + reason classification + expectation-gap detection.
// 龙头信号（Strategy=龙头识别）：评分≥70 且排进前 10 的个股；
//
//	评分≥60 → Action=buy（放开买入层级，直接可交易）；<60 → watch（仅观察）。
//
// Leader signals (Strategy=龙头识别): stocks with score >= 70 and ranked in the top 10;
//
//	score >= 60 → Action=buy (relaxed buy gate, tradeable); < 60 → watch (observe only).
//
// 预期差信号（Strategy=预期差，Action=提醒）：利好/利空新闻与实际涨跌背离（score≥0.4）。
// Gap signals (Strategy=预期差, Action=提醒): news direction conflicting with the actual move (score >= 0.4).
// 涨停池为空时仍对 IndividualStocks 做预期差检测。
// Even with an empty limit-up pool, expectation-gap detection still runs on IndividualStocks.
func (a *Agent) ScanLimitUp(input ScanInput) []Signal {
	var signals []Signal
	now := time.Now()

	// 1. 龙头识别 + 涨停分类
	// 1. Leader identification + limit-up classification.
	if len(input.LimitUpPool) > 0 {
		// 先提取每只涨停股的新闻标题（用于涨停原因分类）
		// First extract each stock's news titles (for reason classification).
		news := make(map[string][]string, len(input.LimitUpPool))
		for _, s := range input.LimitUpPool {
			news[s.Code] = newsTitlesOf(input.News, s.Code)
		}
		analysis := AnalyzeLimitUp(input.LimitUpPool, news)
		for _, l := range analysis.Leaders {
			// 仅评分达到阈值且板块内排名前 10 的个股产出龙头信号
			// Only stocks meeting the score threshold and ranked in the industry top 10 produce a leader signal.
			if l.Score < leaderThreshold || l.Rank > 10 {
				continue
			}
			// 评分≥60 放开为 buy（可交易）；低于 60 仅 watch 观察
			// English: score ≥60 relaxes to a tradeable buy; below that stays watch-only.
			action := "watch"
			if l.Score >= leaderBuyThreshold {
				action = "buy"
			}
			signals = append(signals, Signal{
				ID:          seqID(),
				Code:        l.Stock.Code,
				Name:        l.Stock.Name,
				Strategy:    "龙头识别",
				// §C 归属修正：龙头识别就是 dragon 战法的实盘主循环路径，此前缺
				// StrategyType 导致买入全部落"其他池"（002412 实录）——现在归入龙头池，
				// 分仓纪律/寻优门槛下发/池级实测统计随之生效。
				// English: leader-ID is dragon's live main-loop path; missing StrategyType used to dump
				// its buys into the "other" pool (002412 case) — now routed to the dragon pool.
				StrategyType: "dragon",
				Direction:   "做多",
				Action:      action,
				Price:       l.Stock.Price,
				Confidence:  clamp01(l.Score / 100),
				Reason:      fmt.Sprintf("龙头评分%.0f(排名%d): %s | %s | 行业:%s", l.Score, l.Rank, l.Reason, l.Stock.LimitType, l.Stock.Industry),
				Sector:      l.Stock.Industry,
				GeneratedAt: now,
			})
		}
	}

	// 2. 预期差检测：涨停池内个股 + 8a/8b 个股直入
	// 2. Expectation-gap detection: pool stocks + direct 8a/8b individual stocks.
	// 汇总所有待检测代码（涨停池 + 个股直入，去重）
	// Collect all codes to check (limit-up pool + individual stocks, deduplicated).
	gapCodes := map[string]bool{}
	for _, s := range input.LimitUpPool {
		gapCodes[s.Code] = true
	}
	for _, code := range input.IndividualStocks {
		// L1 阻塞的个股跳过预期差检测
		if input.L1Blocked[code] {
			continue
		}
		gapCodes[code] = true
	}

	for code := range gapCodes {
		// 无关联新闻则无法判断预期差
		// No related news means no expectation gap can be judged.
		if len(input.News[code]) == 0 {
			continue
		}
		md := input.MarketData[code]
		// 行情缺失或价格无效 → 跳过
		// Skip when market data is missing or the price is invalid.
		if md == nil || md.ChangePct == 0 && md.Price <= 0 {
			continue
		}
		changePct := md.ChangePct
		turnover := 0.0
		// 涨停池内的股票取其换手率用于滞涨判断，其余为 0（未知）
		// Take turnover from the pool entry for stagnant-rise judgement; others default to 0 (unknown).
		if lu, ok := limitUpByCode(input.LimitUpPool, code); ok {
			turnover = lu.Turnover
		}
		// 对每条关联新闻做预期差检测，命中即生成一条提醒并跳出
		// Run the gap check per news item; on a hit, emit one alert and break.
		for _, nb := range input.News[code] {
			gap := CheckExpectationGap(nb.Title, nb.Positive, changePct, turnover, 0)
			if !gap.Trigger {
				continue
			}
			signals = append(signals, Signal{
				ID:          seqID(),
				Code:        code,
				Name:        md.Name,
				Strategy:    "预期差",
				Direction:   "提醒",
				Action:      "watch",
				AlertType:   "预期差",
				Price:       md.Price,
				Confidence:  clamp01(gap.Score),
				Reason:      fmt.Sprintf("%s(%.2f%%): %s — %s", gap.GapType, changePct, gap.Reason, nb.Title),
				Sector:      "个股",
				GeneratedAt: now,
			})
			break
		}
	}

	if len(signals) > 0 {
		log.Printf("[combat_agent] ScanLimitUp: 池%d 个股%d → %d 增强信号", len(input.LimitUpPool), len(input.IndividualStocks), len(signals))
	}
	return signals
}

// limitUpByCode 按代码查涨停池条目。
// limitUpByCode looks up a limit-up pool entry by stock code.
func limitUpByCode(pool []data.LimitUpStock, code string) (data.LimitUpStock, bool) {
	for _, s := range pool {
		if s.Code == code {
			return s, true
		}
	}
	return data.LimitUpStock{}, false
}

// clamp01 将分数截断到 0-1。
// clamp01 clamps a value to the [0,1] range.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
