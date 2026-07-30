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
	debugInfo  *DebugInfo
	newsDBPath string
}

func (a *Agent) SetLLMClient(c *llm.Client) { a.llmClient = c }
func (a *Agent) GetDebugInfo() *DebugInfo    { return a.debugInfo }

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

// Process 完整处理流程：追回未读新闻 → Stage1 初筛（关键字或 LLM）→ Stage2 LLM 全量分析。
// 返回分析结果，包含事件列表、原始新闻数、筛选数等信息。
func (a *Agent) Process(ctx context.Context, since time.Time) (*AnalysisResult, error) {
	t0 := time.Now()

	rawNews := a.fetchCatchUp()
	if len(rawNews) == 0 {
		log.Printf("[newsagent] 无新新闻 (since=%s)", since.Format("01-02 15:04"))
		a.debugInfo = &DebugInfo{
			ProcessTime: time.Now(),
		}
		return &AnalysisResult{
			CatchUpSince: since,
		}, nil
	}

	log.Printf("[newsagent] 追回 %d 条新闻 (%v)", len(rawNews), time.Since(t0))

	titles := make([]string, len(rawNews))
	for i, n := range rawNews {
		titles[i] = n.Title
	}

	stage1t := time.Now()
	stage1Mode := "keyword"
	if a.llmClient != nil {
		stage1Mode = "llm"
	}
	indices, err := a.classifyMaterial(titles)
	if err != nil {
		log.Printf("[newsagent] Stage1失败: %v, 全部视为有价值", err)
		indices = make([]int, len(titles))
		for i := range titles {
			indices[i] = i
		}
	}
	log.Printf("[newsagent] Stage1初筛完成 (%v)", time.Since(stage1t))

	var materialItems []data.NewsItem
	for _, idx := range indices {
		materialItems = append(materialItems, rawNews[idx])
	}

	if a.llmClient == nil || len(materialItems) == 0 {
		events := make([]NewsEvent, len(materialItems))
		for i, item := range materialItems {
			events[i] = NewsEvent{
				Title:      item.Title,
				Content:    item.Content,
				Datetime:   item.Datetime,
				Source:     item.Source,
				IsMaterial: true,
			}
		}

		a.debugInfo = &DebugInfo{
			Stage1Mode:    stage1Mode,
			RawCount:      len(rawNews),
			SelectedCount: len(materialItems),
			RawTitles:     titles,
			SelectedIdx:   indices,
			Stage2Events:  events,
			ProcessTime:   time.Now(),
		}

		return &AnalysisResult{
			Events:        events,
			RawCount:      len(rawNews),
			MaterialCount: len(materialItems),
			CatchUpSince:  since,
		}, nil
	}

	stage2t := time.Now()
	events := a.analyzeDeep(materialItems)
	log.Printf("[newsagent] Stage2全量分析完成 (%v)", time.Since(stage2t))

	if a.cleaner != nil {
		for i := range events {
			events[i].CleanedStocks = a.cleaner.CleanBatch(events[i].RelatedStocks)
		}
		log.Printf("[newsagent] StockCleaner 清洗 %d 个事件", len(events))
	}

	// 注入 IPO 日历事件（直构 NewsEvent，不走 LLM）
	ipoEvents := a.buildIPOEvents()
	events = append(events, ipoEvents...)

	// 持久化到文件
	a.saveNewsEvents(events)

	_ = a.tracker.save()

	log.Printf("[newsagent] 流程完成: %d条原始 → %d条初筛 → %d条分析 (%v)",
		len(rawNews), len(materialItems), len(events), time.Since(t0))

	a.debugInfo = &DebugInfo{
		Stage1Mode:    stage1Mode,
		RawCount:      len(rawNews),
		SelectedCount: len(materialItems),
		RawTitles:     titles,
		SelectedIdx:   indices,
		Stage2Events:  events,
		ProcessTime:   time.Now(),
	}

	return &AnalysisResult{
		Events:        events,
		RawCount:      len(rawNews),
		MaterialCount: len(events),
		CatchUpSince:  since,
	}, nil
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

// GetAllNewsEvents 返回当前交易日所有非过期的事件列表（排除已过期的 IPO 事件）。
func (a *Agent) GetAllNewsEvents() []NewsEvent {
	db := a.loadNewsDB()
	td := data.TradingDayDate(time.Now())

	if db.TradingDay != td {
		return nil
	}

	var out []NewsEvent
	for _, e := range db.Events {
		if e.Source == "IPO日历" && isIPOExpired(e, td) {
			continue
		}
		out = append(out, e)
	}
	return out
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
			Title:   title,
			Content: fmt.Sprintf("expiry=%s", expiry),
			Datetime: listing,
			Source:  "IPO日历",
			Level:   "个股",
			Direction: "利好",
			Score:   0.5,
			RelatedStocks: []string{fmt.Sprintf("%s(%s)", ipo.Name, ipo.Code)},
			ImpactLevel: "中",
			EventType: "公司",
			Urgency:   "关注",
			Reason:    reason,
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
