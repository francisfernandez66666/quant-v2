package newsagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

type Agent struct {
	marketAPI  *data.MarketAPI
	llmClient  *llm.Client
	cleaner    *data.StockCleaner
	tracker    *tracker
	dataDir    string
	newsDBPath string
}

func (a *Agent) SetLLMClient(c *llm.Client) { a.llmClient = c }

func New(marketAPI *data.MarketAPI, llmClient *llm.Client, cleaner *data.StockCleaner, dataDir string) *Agent {
	return &Agent{
		marketAPI:  marketAPI,
		llmClient:  llmClient,
		cleaner:    cleaner,
		tracker:    newTracker(dataDir),
		dataDir:    dataDir,
		newsDBPath: filepath.Join(dataDir, "news_events.json"),
	}
}

func (a *Agent) Start() error {
	log.Printf("[newsagent] 已启动, tracker=%s", a.tracker.filePath)
	return nil
}

func (a *Agent) Stop() error {
	return a.tracker.save()
}

// Fetch 拉取未读新闻（含去重记账）。记账属 fetch 自身职能，不对外暴露。
func (a *Agent) Fetch(ctx context.Context, since time.Time) []data.NewsItem {
	rawNews := a.fetchCatchUp()
	if len(rawNews) > 0 {
		log.Printf("[newsagent] 追回 %d 条新闻 (since=%s)", len(rawNews), since.Format("01-02 15:04"))
	}
	return rawNews
}

// Stage1 过滤：判断板块/宏观新闻是否有投资价值，返回有价值的标题索引。
func (a *Agent) Stage1(titles []string) []int {
	indices, err := a.classifyMaterial(titles)
	if err != nil {
		log.Printf("[newsagent] Stage1失败: %v", err)
		return nil
	}
	return indices
}

// Stage2 深度分析：LLM 对新闻全量分析，输出带方向/分数/归因的结构化事件。
// 中性事件照常输出，由引擎按阈值过滤丢弃。
func (a *Agent) Stage2(items []data.NewsItem) []NewsEvent {
	events := a.analyzeDeep(items)
	if a.cleaner != nil {
		for i := range events {
			events[i].CleanedStocks = a.cleaner.CleanBatch(events[i].RelatedStocks)
		}
	}
	return events
}

// CleanStocks 清洗股票列表（名称或代码 → "名称|代码"），供引擎对增强归因做清理。
func (a *Agent) CleanStocks(items []string) []string {
	if a.cleaner == nil {
		return items
	}
	return a.cleaner.CleanBatch(items)
}

// SaveEvents 持久化事件到 newsDB 文件并保存 tracker，供 /api/news 展示。
func (a *Agent) SaveEvents(events []NewsEvent) {
	a.saveNewsEvents(events)
	_ = a.tracker.save()
}

// BuildIPOEvents 从 IPO 日历构建事件（直构 NewsEvent，不走 LLM）。
func (a *Agent) BuildIPOEvents() []NewsEvent {
	return a.buildIPOEvents()
}

// BuildIPOFeedEvents 从 IPO 新闻流直构事件（新股/申购/上市，Score+0.5 利好，不走 LLM）。
func (a *Agent) BuildIPOFeedEvents(items []data.NewsItem) []NewsEvent {
	var out []NewsEvent
	for _, item := range items {
		dt := item.Datetime
		if dt == "" {
			dt = time.Now().Format("2006-01-02 15:04:05")
		}
		event := NewsEvent{
			Title:         item.Title,
			Content:       item.Content,
			Datetime:      dt,
			Source:        item.Source,
			Level:         "个股",
			Direction:     "利好",
			Score:         0.5,
			ImpactLevel:   "中",
			EventType:     "公司",
			Urgency:       "关注",
			Reason:        "IPO相关新闻（新股申购/上市），按利好直构",
			RelatedStocks: nil,
		}
		if a.cleaner != nil {
			if hits := a.cleaner.FindStocksInText(item.Title); len(hits) > 0 {
				event.RelatedStocks = hits
				event.CleanedStocks = a.cleaner.CleanBatch(hits)
			}
		}
		out = append(out, event)
	}
	if len(out) > 0 {
		log.Printf("[newsagent] IPO新闻流注入 %d 个事件", len(out))
	}
	return out
}

// newsDB 新闻事件本地持久化结构，按交易日分批存储。
type newsDB struct {
	TradingDay string      `json:"trading_day"` // 交易日 YYYYMMDD
	Events     []NewsEvent `json:"events"`      // 事件列表
}

// saveNewsEvents 将事件持久化到 newsDB 文件，按交易日归并去重，最多保留 200 条。
func (a *Agent) saveNewsEvents(events []NewsEvent) {
	td := data.TradingDayDate(time.Now())
	existing := a.loadNewsDB()

	if existing.TradingDay != td {
		existing.TradingDay = td
		existing.Events = nil
	}

	seen := make(map[string]bool)
	for _, e := range existing.Events {
		key := truncTitle(e.Title)
		seen[key] = true
	}
	for _, e := range events {
		s := e.Score
		if s < 0 {
			s = -s
		}
		if s < 0.25 {
			continue // 过滤中性/无价值噪音
		}
		key := truncTitle(e.Title)
		if !seen[key] {
			seen[key] = true
			existing.Events = append(existing.Events, e)
		}
	}

	if len(existing.Events) > 200 {
		existing.Events = existing.Events[len(existing.Events)-200:]
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		log.Printf("[newsagent] newsDB 序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(a.newsDBPath, data, 0644); err != nil {
		log.Printf("[newsagent] newsDB 写入失败: %v", err)
	}
}

// loadNewsDB 从文件加载持久化的新闻事件数据库。
func (a *Agent) loadNewsDB() *newsDB {
	data, err := os.ReadFile(a.newsDBPath)
	if err != nil {
		return &newsDB{}
	}
	var db newsDB
	if err := json.Unmarshal(data, &db); err != nil {
		log.Printf("[newsagent] newsDB 解析失败: %v", err)
		return &newsDB{}
	}
	return &db
}

// buildIPOEvents 从 IPO 日历构建 NewsEvent（新股申购/上市），跳过已存在的事件。
func (a *Agent) buildIPOEvents() []NewsEvent {
	if a.marketAPI == nil {
		return nil
	}
	now := time.Now()
	td := data.TradingDayDate(now)

	existing := a.loadNewsDB()
	if existing.TradingDay != td {
		existing.Events = nil
	}
	cache := make(map[string]bool)
	for _, e := range existing.Events {
		if e.Source == "IPO日历" {
			cache[strings.TrimSpace(e.Title)] = true
		}
	}

	list, err := a.marketAPI.GetEastMoneyIPOCalendar()
	if err != nil {
		log.Printf("[newsagent] 获取IPO日历失败: %v", err)
		return nil
	}

	var out []NewsEvent
	for _, ipo := range list {
		status := "新股申购"
		if ipo.ListStatus == "L" {
			status = "新股上市"
		}
		title := fmt.Sprintf("%s: %s(%s)", status, ipo.Name, ipo.Code)
		if cache[title] {
			continue
		}

		listing := ipo.ListingDate
		if listing == "" {
			listing = ipo.IPODate
		}
		if listing == "" {
			continue
		}

		reason := fmt.Sprintf("%s，发行价¥%.2f", status, ipo.IssuePrice)
		expiry := data.AddTradingDays(listing, 1)

		event := NewsEvent{
			Title:         title,
			Content:       fmt.Sprintf("expiry=%s", expiry),
			Datetime:      listing,
			Source:        "IPO日历",
			Level:         "个股",
			Direction:     "利好",
			Score:         0.5,
			RelatedStocks: []string{fmt.Sprintf("%s(%s)", ipo.Name, ipo.Code)},
			ImpactLevel:   "中",
			EventType:     "公司",
			Urgency:       "关注",
			Reason:        reason,
		}
		if a.cleaner != nil {
			event.CleanedStocks = a.cleaner.CleanBatch(event.RelatedStocks)
		}
		out = append(out, event)
	}
	if len(out) > 0 {
		log.Printf("[newsagent] IPO注入 %d 个事件", len(out))
	}
	return out
}

// isIPOExpired 判断 IPO 事件是否已过期（当前交易日 > 到期日）。
func isIPOExpired(e NewsEvent, td string) bool {
	if !strings.HasPrefix(e.Content, "expiry=") {
		return false
	}
	expiry := strings.TrimPrefix(e.Content, "expiry=")
	return td > expiry
}

// truncTitle 截断标题到 60 个字符，用于去重对比。
func truncTitle(t string) string {
	if len(t) > 60 {
		return t[:60]
	}
	return t
}
