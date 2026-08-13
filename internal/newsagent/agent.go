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

// Agent 新闻智能体：拉取、分析并持久化新闻事件。（Agent is the news agent: fetching, analyzing and persisting news events.）
type Agent struct {
	marketAPI  *data.MarketAPI    // 行情/新闻数据接口：分页拉取新闻、抓取正文、获取 IPO 日历
	llmClient  *llm.Client        // LLM 客户端：用于 Stage0/1 分类初筛与 Stage2 深度分析
	cleaner    *data.StockCleaner // 股票清洗器：股票名称/代码归一化为 "名称|代码"
	tracker    *tracker           // 去重记账器：记录已见标题与来源同步时间，避免重复处理
	dataDir    string             // 数据目录：存放 news_events.json / news_tracker.json
	newsDBPath string             // 新闻事件本地持久化文件路径（news_events.json）
	frozenPath string             // 固化事件持久化文件路径（frozen_events.json）
	minScore   float64            // 落盘过滤最低分（默认 0.25；前端"显示全部"开关可改为 0）
}

// SetLLMClient 设置 LLM 客户端。（Sets the LLM client.）
func (a *Agent) SetLLMClient(c *llm.Client) { a.llmClient = c }

// SetMinScore 设置落盘过滤最低分（|score| 低于该值的事件不落盘展示）。
// （SetMinScore sets the minimum |score| for persistence; lower-scoring events are not stored.）
func (a *Agent) SetMinScore(v float64) { a.minScore = v }

// MinScore 返回当前落盘过滤最低分。（MinScore returns the current minimum |score| for persistence.）
func (a *Agent) MinScore() float64 { return a.minScore }

// New 创建新闻智能体实例。（New creates a news agent instance.）
func New(marketAPI *data.MarketAPI, llmClient *llm.Client, cleaner *data.StockCleaner, dataDir string) *Agent {
	return &Agent{
		marketAPI:  marketAPI,
		llmClient:  llmClient,
		cleaner:    cleaner,
		tracker:    newTracker(dataDir),
		dataDir:    dataDir,
		newsDBPath: filepath.Join(dataDir, "news_events.json"),
		frozenPath: filepath.Join(dataDir, "frozen_events.json"),
		minScore:   0.25, // 默认最低落盘分 0.25（前端"显示全部"可降为 0）
	}
}

// Start 启动新闻智能体。（Start starts the news agent.）
func (a *Agent) Start() error {
	log.Printf("[newsagent] 已启动, tracker=%s", a.tracker.filePath)
	return nil
}

// Stop 停止新闻智能体并保存记账数据。（Stop stops the news agent and saves the tracker data.）
func (a *Agent) Stop() error {
	return a.tracker.save()
}

// Fetch 拉取未读新闻（含去重记账）。记账属 fetch 自身职能，不对外暴露。
// （Fetch pulls unread news including dedup bookkeeping; the bookkeeping stays internal to fetch.）
func (a *Agent) Fetch(ctx context.Context, since time.Time) []data.NewsItem {
	rawNews := a.fetchCatchUp(false)
	if len(rawNews) > 0 {
		log.Printf("[newsagent] 追回 %d 条新闻 (since=%s)", len(rawNews), since.Format("01-02 15:04"))
	}
	return rawNews
}

// UnattributedItems 返回当前待归因（已抓取但 Stage0/Stage2 尚未成功）的新闻队列，
// 按发布时间最新在前排序。供引擎在盘前/盘中每轮与新增新闻一并重试归因。
// （UnattributedItems returns the current queue of fetched-but-not-yet-attributed news, newest-first,
// for the engine to re-attempt alongside newly fetched news each premarket/intraday round.）
func (a *Agent) UnattributedItems() []data.NewsItem {
	a.tracker.SortPendingNewestFirst()
	return a.tracker.Pending()
}

// MarkAttributed 把成功归因的新闻标记为已见（从未归因队列移除并写入 seen 记账）。
// 归因成功定义：Stage0 分类成功且（对个股/板块）Stage2 深度分析产出事件。
// 被标记后该新闻不再进入重试队列。
// （MarkAttributed marks successfully-attributed news as seen (dropped from the queue, recorded in the
// seen ledger) so they are not retried. Success = Stage0 classified it and Stage2 emitted events.）
func (a *Agent) MarkAttributed(items []data.NewsItem) {
	if len(items) == 0 {
		return
	}
	a.tracker.RemovePending(items)
	log.Printf("[newsagent] 归因成功 %d 条新闻已标记seen, 剩余待归因 %d", len(items), len(a.tracker.Pending()))
}

// MarkAttributedTitles 按标题标记归因成功（用于已入队新闻经一轮归因后，按标题反查移除）。
// 供引擎把本轮成功归因的新闻从未归因队列摘除，避免下轮重复分析。
// （MarkAttributedTitles marks news as attributed by matching titles, dropping them from the queue so
// the next round does not re-analyze already-processed items.）
func (a *Agent) MarkAttributedTitles(titles map[string]bool) {
	if len(titles) == 0 {
		return
	}
	pending := a.tracker.Pending()
	var matched []data.NewsItem
	for _, it := range pending {
		if titles[it.Title] {
			matched = append(matched, it)
		}
	}
	if len(matched) > 0 {
		a.tracker.RemovePending(matched)
	}
}

// Stage1 过滤：判断板块/宏观新闻是否有投资价值，返回有价值的标题索引。
// （Stage1 filtering: judges whether sector/macro news has investment value and returns valuable title indices.）
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
// 返回第二个值 failedItems：LLM 分析失败（被兜底占位）未归因的新闻，调用方应留队重试。
// （Stage2 deep analysis: LLM analyzes all news into structured events with direction/score/attribution;
// neutral events are still emitted and the engine discards them by threshold. The second return failedItems
// lists news whose LLM analysis failed (padded by fallback), which callers should keep for retry.）
func (a *Agent) Stage2(items []data.NewsItem) ([]NewsEvent, []data.NewsItem) {
	events, failed := a.analyzeDeep(items)
	if a.cleaner != nil {
		// 对每个事件关联的个股做名称/代码归一化清洗（→ "名称|代码"）
		for i := range events {
			events[i].CleanedStocks = a.cleaner.CleanBatch(events[i].RelatedStocks)
		}
	}
	return events, failed
}

// CleanStocks 清洗股票列表（名称或代码 → "名称|代码"），供引擎对增强归因做清理。
// （CleanStocks normalizes a stock list from name/code to "名称|代码" for engine attribution cleanup.）
func (a *Agent) CleanStocks(items []string) []string {
	if a.cleaner == nil {
		return items
	}
	return a.cleaner.CleanBatch(items)
}

// FindStocksInText 在文本中查找出现的股票名称（供咨询/归因等按自然语言识别个股）。
// （FindStocksInText finds stock names appearing in the text for natural-language stock recognition.）
func (a *Agent) FindStocksInText(text string) []string {
	if a.cleaner == nil || text == "" {
		return nil
	}
	return a.cleaner.FindStocksInText(text)
}

// SaveEvents 持久化事件到 newsDB 文件并保存 tracker，供 /api/news 展示。
// （SaveEvents persists events to the newsDB file and saves the tracker for /api/news display.）
func (a *Agent) SaveEvents(events []NewsEvent) {
	a.saveNewsEvents(events)
	_ = a.tracker.save()
}

// BuildIPOEvents 从 IPO 日历构建事件（直构 NewsEvent，不走 LLM）。
// （BuildIPOEvents builds events directly from the IPO calendar into NewsEvent without the LLM.）
func (a *Agent) BuildIPOEvents() []NewsEvent {
	return a.buildIPOEvents()
}

// BuildIPOFeedEvents 从 IPO 新闻流直构事件（新股/申购/上市，Score+0.5 利好，不走 LLM）。
// （BuildIPOFeedEvents builds events directly from the IPO news feed (new stock/subscription/listing) with a
// +0.5 bullish score, skipping the LLM.）
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
// （newsDB is the local persistence shape for news events, bucketed per trading day.）
type newsDB struct {
	TradingDay string      `json:"trading_day"` // 交易日 YYYYMMDD
	Events     []NewsEvent `json:"events"`      // 事件列表
}

// saveNewsEvents 将事件持久化到 newsDB 文件，按交易日归并去重，最多保留 200 条。
// （saveNewsEvents persists events to the newsDB file, merging by trading day, deduping and keeping at most 200.）
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

// loadNewsDB 从文件加载持久化的新闻事件数据库。（loadNewsDB loads the persisted news-event database from file.）
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
// （AllEvents returns all locally persisted tagged news events, including neutral/general, for /api/news?all=true.）
func (a *Agent) AllEvents() []NewsEvent {
	db := a.loadNewsDB()
	if db == nil {
		return nil
	}
	return db.Events
}

// FrozenEvents 返回当前全部未过期的固化事件（供引擎合并进有效事件池）。
// （FrozenEvents returns all currently non-expired frozen events for the engine's effective event pool.）
func (a *Agent) FrozenEvents() []NewsEvent {
	db := a.loadFrozenDB()
	if db == nil {
		return nil
	}
	td := data.TradingDayDate(time.Now())
	out := make([]NewsEvent, 0, len(db.Events))
	for i := range db.Events {
		if !isFrozenExpired(db.Events[i], td) {
			out = append(out, db.Events[i].NewsEvent)
		}
	}
	return out
}

// SaveFrozen 将本轮产出的带价值事件写入固化层：同板块+同方向（Key）覆盖、分数取最新；
// 同时做跨日到期清理（过期事件移除）。写盘前先备份原文件（损坏恢复兜底）。
// （SaveFrozen writes this round's valuable events to the frozen layer: same sector+direction overwrites with
// the latest score, plus cross-day expiry cleanup; it backups the file before writing for corruption recovery.）
func (a *Agent) SaveFrozen(fresh []NewsEvent) {
	td := data.TradingDayDate(time.Now())
	db := a.loadFrozenDB()
	if db == nil {
		db = &frozenDB{}
	}
	// 保留未过期的事件
	kept := make([]FrozenEvent, 0, len(db.Events)+len(fresh))
	byKey := make(map[string]int)
	for i := range db.Events {
		if isFrozenExpired(db.Events[i], td) {
			continue
		}
		byKey[db.Events[i].Key] = len(kept)
		kept = append(kept, db.Events[i])
	}
	// 本轮新带价值事件：同板块+同方向 → 覆盖（分数/时间/个股取最新），否则追加
	for _, e := range fresh {
		if !shouldFreeze(e) {
			continue
		}
		key := frozenKey(e)
		fe := FrozenEvent{NewsEvent: e, Day: td, Key: key}
		if idx, ok := byKey[key]; ok {
			kept[idx] = fe // 覆盖旧事件（Score 永远取最新值）
		} else {
			byKey[key] = len(kept)
			kept = append(kept, fe)
		}
	}
	// 控制规模：仅保留最新 100 条
	if len(kept) > 100 {
		kept = kept[len(kept)-100:]
	}
	outDB := &frozenDB{TradingDay: td, Events: kept}
	if err := a.writeFrozenDB(outDB); err != nil {
		log.Printf("[frozen] 固化文件写入失败: %v", err)
	}
}

// writeFrozenDB 序列化并写入固化文件。写入前先备份当前文件为 .bak，便于损坏时恢复。
// （writeFrozenDB serializes and writes the frozen file, backing it up to .bak first for recovery.）
func (a *Agent) writeFrozenDB(db *frozenDB) error {
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	if _, err := os.Stat(a.frozenPath); err == nil {
		if raw, rerr := os.ReadFile(a.frozenPath); rerr == nil {
			_ = os.WriteFile(a.frozenPath+".bak", raw, 0644)
		}
	}
	return os.WriteFile(a.frozenPath, data, 0644)
}

// frozenKey 计算固化覆盖键：sector|direction。无板块时以标题代替板块，方向缺失时按 Score 符号推断。
// （frozenKey computes the overwrite key sector|direction, using the title as sector and inferring the
// direction from the Score sign when missing.）
func frozenKey(e NewsEvent) string {
	sector := ""
	if len(e.Sectors) > 0 && strings.TrimSpace(e.Sectors[0]) != "" {
		sector = strings.TrimSpace(e.Sectors[0])
	} else {
		sector = e.Title
	}
	dir := e.Direction
	if e.Direction == "" {
		if e.Score >= 0 {
			dir = "利好"
		} else {
			dir = "利空"
		}
	}
	return sector + "|" + dir
}

// shouldFreeze 判断事件是否需要固化：|Score|≥0.25 且方向为利好/利空。
// （shouldFreeze reports whether an event should be frozen: |Score|≥0.25 and direction bullish/bearish.）
func shouldFreeze(e NewsEvent) bool {
	s := e.Score
	if s < 0 {
		s = -s
	}
	return s >= 0.25 && (e.Direction == "利好" || e.Direction == "利空")
}

// isFrozenExpired 判断固化事件是否已到期。
// 事件在其产生日(day)及顺延一个自然日(day+1)内有效；当前交易日 td 已在 day+1 之后即过期移除。
// （isFrozenExpired reports whether a frozen event has expired: it stays valid through its day plus one
// calendar day, and is removed once the current trading day td is past day+1.）
func isFrozenExpired(fe FrozenEvent, td string) bool {
	if fe.Day == "" {
		return false // 无日期（旧数据）保守保留
	}
	d, err1 := time.Parse("20060102", fe.Day)
	t, err2 := time.Parse("20060102", td)
	if err1 != nil || err2 != nil {
		return false // 解析失败保守保留
	}
	horizon := d.AddDate(0, 0, 1) // day+1 自然日
	return t.After(horizon)
}

// loadFrozenDB 从文件加载固化事件库（含损坏恢复）。
// 整体解析失败时先逐条尝试抢救，仍失败则把损坏文件备份为 .bak 后返回空库，绝不因坏文件阻断固化层。
// （loadFrozenDB loads the frozen-event DB with corruption recovery: salvages objects one by one on whole-parse
// failure, and backs up a hopelessly broken file as .bak before returning an empty DB.）
func (a *Agent) loadFrozenDB() *frozenDB {
	data, err := os.ReadFile(a.frozenPath)
	if err != nil {
		return &frozenDB{}
	}
	var db frozenDB
	if err := json.Unmarshal(data, &db); err == nil {
		return &db
	}
	// 整体解析失败 → 逐条对象抢救（按行尝试单独解析出可用的 FrozenEvent）
	log.Printf("[frozen] 固化文件整体解析失败, 尝试逐条抢救")
	var salvaged []FrozenEvent
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSuffix(line, ",") // 截断的 JSON 中非末行事件对象末尾常带逗号，先去掉再尝试解析
		if line == "" {
			continue
		}
		var fe FrozenEvent
		if json.Unmarshal([]byte(line), &fe) == nil && fe.Title != "" {
			salvaged = append(salvaged, fe)
		}
	}
	if len(salvaged) > 0 {
		log.Printf("[frozen] 逐条抢救成功 %d 条", len(salvaged))
		return &frozenDB{Events: salvaged}
	}
	// 完全无法解析：备份损坏文件，返回空库
	_ = os.WriteFile(a.frozenPath+".bak", data, 0644)
	log.Printf("[frozen] 固化文件损坏且抢救失败, 已备份为 .bak")
	return &frozenDB{}
}

// buildIPOEvents 从 IPO 日历构建 NewsEvent（新股申购/上市），跳过已存在的事件。
// （buildIPOEvents builds NewsEvents from the IPO calendar (subscription/listing), skipping existing ones.）
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
// （isIPOExpired reports whether an IPO event has expired: current trading day > expiry.）
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
