// event_factor.go — §ENH-4(2026-09-19) 事件因子面板：把 newsagent 历史事件归档（§ENH-3 的
// news_event_history.jsonl）中的 LLM 冲击分 score 装配为截面因子，复用本包现成的
// ICByDate/LayerReturns/Monotonic 原语检验"事件分有没有截面预测力"。
// 与 internal/backtest/chain.go 互补：chain 回答"事件信号追不追得上"，这里回答"score 本身
// 是否可用作打分权重"——后者反过来为打分池权重提供标定依据。
// 口径（写死在注释，改动需同步文档）：
//   - 两档因子：news_score@stock（事件点名个股）、news_score@sector（板块级事件经
//     stocks.industry 与事件 Sectors 名称互相包含匹配传导至成分股；宏观级事件不入面板）。
//   - 同日同股多事件：取 |score| 最大的一条（带符号），不做求和——求和会让刷屏利好虚增强度。
//   - 无事件日=NaN，ICByDate/LayerReturns 天然跳过 NaN，无需改原语。
//
// English: event factor panel — maps archived news scores onto the daily cross-section
// (two IDs: stock-level and sector-level), NaN for event-free days, max-|score| aggregation.
package research

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"quant-trading-v2/internal/data"
)

// 事件因子 ID 常量（cmd/research event-layers 与前端事件因子卡共用）。
const (
	EventFactorStock  = "news_score@stock"  // 个股级事件分
	EventFactorSector = "news_score@sector" // 板块级事件分（经行业映射传导）
)

// eventHistoryRow 对应 newsagent 归档行 {"trading_day","event"}
// （本地最小结构，不 import newsagent 以免把 LLM 依赖拖进研究链路）。
type eventHistoryRow struct {
	TradingDay string `json:"trading_day"` // 事件归属交易日 YYYYMMDD
	Event      struct {
		Level         string   `json:"level"`          // 影响级别：个股/板块/宏观
		Score         float64  `json:"score"`          // 带符号冲击分（利空为负）
		Sectors       []string `json:"sectors"`        // 相关板块名（同花顺口径）
		CleanedStocks []string `json:"cleaned_stocks"` // 关联个股 "名称|代码"
	} `json:"event"`
}

// LoadEventHistory 读取归档 jsonl，坏行跳过不致命（研究口径容忍上游脏数据）。
// LoadEventHistory reads the append-only event history, skipping malformed lines.
func LoadEventHistory(path string) ([]eventHistoryRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("event history: %w", err)
	}
	defer f.Close()
	var out []eventHistoryRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024) // 单行事件含正文可能较长，放大缓冲上限
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r eventHistoryRow
		if json.Unmarshal([]byte(line), &r) != nil || len(r.TradingDay) != 8 {
			continue // 坏 JSON/缺归属日期的行直接丢弃
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// BuildEventScores 把事件行装配为 day(YYYYMMDD)→ts_code→score 两档因子值表。
// industryByCode 来自 store.StockIndustries（ts_code→行业名）；
// 板块匹配为"名称互相包含"近似（同花顺板块名与 tushare 行业名字典不同源，
// 匹配不上即不赋分——宁缺毋滥，绝不错配污染因子）。
// English: builds day×ts_code score maps for both tiers; sector events propagate through
// mutual-substring industry matching (unmatched sectors contribute nothing).
func BuildEventScores(rows []eventHistoryRow, industryByCode map[string]string) (stock, sector map[string]map[string]float64) {
	stock = map[string]map[string]float64{}
	sector = map[string]map[string]float64{}
	// put 实现"同日同票取 |score| 最大"聚合口径（带符号比较绝对值）。
	put := func(m map[string]map[string]float64, day, code string, score float64) {
		if m[day] == nil {
			m[day] = map[string]float64{}
		}
		if old, ok := m[day][code]; !ok || math.Abs(score) > math.Abs(old) {
			m[day][code] = score
		}
	}
	for _, r := range rows {
		if r.Event.Score == 0 {
			continue // 中性零分无截面区分度，不占格
		}
		switch r.Event.Level {
		case "个股":
			// 个股事件：点名清单逐个清洗为 ts_code（"名称|600519" → "600519.SH"）。
			for _, cs := range r.Event.CleanedStocks {
				if code := bareToTsCode(cs); code != "" {
					put(stock, r.TradingDay, code, r.Event.Score)
				}
			}
		case "板块":
			// 板块事件：反查行业归属，把同一分广播到该行业全部成分股。
			if len(r.Event.Sectors) == 0 {
				continue
			}
			for code, ind := range industryByCode {
				if ind == "" || !matchAnySector(ind, r.Event.Sectors) {
					continue
				}
				put(sector, r.TradingDay, code, r.Event.Score)
			}
		}
	}
	return stock, sector
}

// bareToTsCode "名称|600519"/"600519" → "600519.SH"（后缀走 ExchangeSuffix 唯一权威口径）；
// 清洗不出 6 位数字则返回空串（脏条目跳过）。
func bareToTsCode(s string) string {
	if i := strings.LastIndex(s, "|"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	if len(s) != 6 {
		return ""
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return "" // 仅名称无代码的条目不入库
		}
	}
	return data.ExchangeSuffix(s)
}

// matchAnySector 行业名与任一事件事件板块名"互相包含"即算命中（如 半导体 ⊂ 半导体设备）。
func matchAnySector(industry string, sectors []string) bool {
	for _, sec := range sectors {
		sec = strings.TrimSpace(sec)
		if sec == "" {
			continue
		}
		if strings.Contains(industry, sec) || strings.Contains(sec, industry) {
			return true
		}
	}
	return false
}

// ApplyEventScores 把 day→ts_code→score 值表写成面板上的因子列（对齐 Panel.DateIdx，
// 无事件日保持 NaN）。factorID 为 EventFactorStock/EventFactorSector。
// English: writes the day×code score table onto each panel as a factor column aligned to
// Dates; event-free days stay NaN.
func ApplyEventScores(panels []*Panel, scores map[string]map[string]float64, factorID string) {
	// 先转置为 ts_code→day→score：面板按自身代码取行，避免每股全表扫描。
	byCode := make(map[string]map[string]float64, len(panels))
	for day, perCode := range scores {
		for code, v := range perCode {
			if byCode[code] == nil {
				byCode[code] = map[string]float64{}
			}
			byCode[code][day] = v
		}
	}
	for _, p := range panels {
		col := make([]float64, len(p.Series.Dates))
		for i := range col {
			col[i] = math.NaN() // 默认无事件=NaN，横截面原语自动跳过
		}
		for d, v := range byCode[p.Code] {
			if i, has := p.DateIdx[d]; has {
				col[i] = v
			}
		}
		p.Factors[factorID] = col
	}
}
