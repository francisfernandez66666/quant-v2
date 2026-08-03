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

// Agent 新闻智能体：拉取、分析并持久化新闻事件。
type Agent struct {
	marketAPI  *data.MarketAPI    // 行情/新闻数据接口：分页拉取新闻、抓取正文、获取 IPO 日历
	llmClient  *llm.Client        // LLM 客户端：用于 Stage0/1 分类初筛与 Stage2 深度分析
	cleaner    *data.StockCleaner // 股票清洗器：股票名称/代码归一化为 "名称|代码"
	tracker    *tracker           // 去重记账器：记录已见标题与来源同步时间，避免重复处理
	dataDir    string             // 数据目录：存放 news_events.json / news_tracker.json
	newsDBPath string             // 新闻事件本地持久化文件路径（news_events.json）
	minScore   float64            // 落盘过滤最低分（默认 0.25；前端"显示全部"开关可改为 0）
}

// SetLLMClient 设置 LLM 客户端。
func (a *Agent) SetLLMClient(c *llm.Client) { a.llmClient = c }

// SetMinScore 设置落盘过滤最低分（|score| 低于该值的事件不落盘展示）。
func (a *Agent) SetMinScore(v float64) { a.minScore = v }

// MinScore 返回当前落盘过滤最低分。
func (a *Agent) MinScore() float64 { return a.minScore }

// New 创建新闻智能体实例。
func New(marketAPI *data.MarketAPI, llmClient *llm.Client, cleaner *data.StockCleaner, dataDir string) *Agent {
	return &Agent{
		marketAPI:  marketAPI,
		llmClient:  llmClient,
		cleaner:    cleaner,
		tracker:    newTracker(dataDir),
		dataDir:    dataDir,
		newsDBPath: filepath.Join(dataDir, "news_events.json"),
		minScore:   0.25, // 默认最低落盘分 0.25（前端"显示全部"可降为 0）
	}
}

// Start 启动新闻智能体。
func (a *Agent) Start() error {
	log.Printf("[newsagent] 已启动, tracker=%s", a.tracker.filePath)
	return nil
}

// Stop 停止新闻智能体并保存记账数据。
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
		// 对每个事件关联的个股做名称/代码归一化清洗（→ "名称|代码"）
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
		// 兜底时间：新闻缺失发布时间时使用当前时间
		dt := item.Datetime
		if dt == "" {
			dt = time.Now().Format("2006-01-02 15:04:05")
		}
		// 直接按固定模板构建"利好"事件，不走 LLM，保证 IPO 类事件稳定产出
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
		// 从标题中尝试提取涉及的个股并清洗，命中不到则保持空关联
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

	// 跨交易日则清空旧事件，重新按新交易日归档
	if existing.TradingDay != td {
		existing.TradingDay = td
		existing.Events = nil
	}

	// 先建立"已存在标题"索引，用于后续去重（截断标题对比）
	seen := make(map[string]bool)
	for _, e := range existing.Events {
		key := truncTitle(e.Title)
		seen[key] = true
	}
	for _, e := range events {
		// 取分数绝对值作为过滤依据：低于 minScore 的中性/无价值噪音直接丢弃
		// （默认 0.25，前端"显示全部"开关可降为 0，让弱档/中性事件也出现在 /api/news）
		s := e.Score
		if s < 0 {
			s = -s
		}
		if s < a.minScore {
			continue // 过滤中性/无价值噪音
		}
		// 标题级去重：同标题事件仅保留第一条
		key := truncTitle(e.Title)
		if !seen[key] {
			seen[key] = true
			existing.Events = append(existing.Events, e)
		}
	}

	// 控制单日事件规模，只保留最新的 200 条
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

// AllEvents 返回持久化到本地的全部已打标新闻事件（含中性/一般），供 /api/news?all=true 展示。
func (a *Agent) AllEvents() []NewsEvent {
	db := a.loadNewsDB()
	if db == nil {
		return nil
	}
	return db.Events
}

// buildIPOEvents 从 IPO 日历构建 NewsEvent（新股申购/上市），跳过已存在的事件。
func (a *Agent) buildIPOEvents() []NewsEvent {
	if a.marketAPI == nil {
		return nil
	}
	now := time.Now()
	td := data.TradingDayDate(now)

	// 跨交易日重置本地缓存，避免旧事件长期占用去重索引
	existing := a.loadNewsDB()
	if existing.TradingDay != td {
		existing.Events = nil
	}
	// 建立已有 IPO 事件标题索引（仅统计来源为"IPO日历"的事件）
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
		// 按上市状态区分标题：L=新股上市，其余默认新股申购
		status := "新股申购"
		if ipo.ListStatus == "L" {
			status = "新股上市"
		}
		title := fmt.Sprintf("%s: %s(%s)", status, ipo.Name, ipo.Code)
		if cache[title] {
			continue // 已存在的事件跳过，避免重复注入
		}

		// 取上市日期，缺失时回退到申购日期；两者皆无则跳过
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
			Content:       fmt.Sprintf("expiry=%s", expiry), // 过期标记，供引擎判断事件是否已失效
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
