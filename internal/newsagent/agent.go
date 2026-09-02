// agent.go — 新闻智能体核心编排：启动/拉取/Stage0-2 归因、固化事件与新闻持久化。
package newsagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// Agent 新闻智能体：拉取、分析并持久化新闻事件。（Agent is the news agent: fetching, analyzing and persisting news events.）
// English: Agent is the news agent: fetching, analyzing and persisting news events.
type Agent struct {
	marketAPI *data.MarketAPI // 行情/新闻数据接口：分页拉取新闻、抓取正文、获取 IPO 日历
	// English: market/news data API: paged news fetch, body grabbing, IPO calendar
	llmClient *llm.Client // LLM 客户端：用于 Stage0/1 分类初筛与 Stage2 深度分析
	// English: LLM client: Stage0/1 classification screening and Stage2 deep analysis
	cleaner *data.StockCleaner // 股票清洗器：股票名称/代码归一化为 "名称|代码"
	// English: stock cleaner: normalizes stock names/codes to "name|code"
	tracker *tracker // 去重记账器：记录已见标题与来源同步时间，避免重复处理
	// English: dedup ledger: records seen titles and per-source sync times to avoid duplicates
	dataDir string // 数据目录：存放 news_events.json / news_tracker.json
	// English: data directory: news_events.json / news_tracker.json
	newsDBPath string // 新闻事件本地持久化文件路径（news_events.json）
	// English: news-event local persistence file path (news_events.json)
	frozenPath string // 固化事件持久化文件路径（frozen_events.json）
	// English: frozen-event persistence file path (frozen_events.json)
	minScore float64 // 落盘过滤最低分（默认 0.25；前端"显示全部"开关可改为 0）
	// English: minimum |score| to persist (default 0.25; the frontend "show all" toggle can set 0)
	bootCache map[string]bool // IPO启动分析缓存：交易日:代码 → 已分析
	// English: IPO-boot analysis cache: trading-day:code -> analyzed
	bootCacheDay string // bootCache 对应的交易日 YYYYMMDD
	// English: the trading day YYYYMMDD that bootCache corresponds to

	// hotMu §R3-8 P1-K 可变字段锁：llmClient/minScore 由 HTTP 热更线程写（SetLLMClient/
	// SetMinScore）、异步 Run goroutine 读；bootCache 由手动 ReanalyzeNews 与异步 Run 并发读写。
	// 此前三者完全无锁 = data race。慢路径（LLM 调用）一律先快照指针再放锁外。
	hotMu sync.RWMutex
}

// SetLLMClient 设置 LLM 客户端。（Sets the LLM client.）
// English: sets the LLM client.
func (a *Agent) SetLLMClient(c *llm.Client) {
	a.hotMu.Lock()
	a.llmClient = c
	a.hotMu.Unlock()
}

// llmClientSnapshot 锁内取当前 LLM 客户端指针（调用方在锁外使用）。
// English: returns the current LLM client under RLock; use outside the lock.
func (a *Agent) llmClientSnapshot() *llm.Client {
	a.hotMu.RLock()
	defer a.hotMu.RUnlock()
	return a.llmClient
}

// currentMinScore 锁内读当前落盘过滤最低分。
func (a *Agent) currentMinScore() float64 {
	a.hotMu.RLock()
	defer a.hotMu.RUnlock()
	return a.minScore
}

// SetMinScore 设置落盘过滤最低分（|score| 低于该值的事件不落盘展示）。
// （SetMinScore sets the minimum |score| for persistence; lower-scoring events are not stored.）
// English: SetMinScore sets the minimum |score| for persistence; lower-scoring events are not stored.
func (a *Agent) SetMinScore(v float64) {
	a.hotMu.Lock()
	a.minScore = v
	a.hotMu.Unlock()
}

// MinScore 返回当前落盘过滤最低分。（MinScore returns the current minimum |score| for persistence.）
// English: MinScore returns the current minimum |score| for persistence.
func (a *Agent) MinScore() float64 { return a.currentMinScore() }

// New 创建新闻智能体实例。（New creates a news agent instance.）
// English: New creates a news agent instance.
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
		// English: default minimum persist score 0.25 (the frontend "show all" toggle can lower it to 0)
	}
}

// Start 启动新闻智能体。（Start starts the news agent.）
// English: Start starts the news agent.
func (a *Agent) Start() error {
	log.Printf("[newsagent] 已启动, tracker=%s", a.tracker.filePath)
	return nil
}

// Stop 停止新闻智能体并保存记账数据。（Stop stops the news agent and saves the tracker data.）
// English: Stop stops the news agent and saves the tracker data.
func (a *Agent) Stop() error {
	return a.tracker.save()
}

// Fetch 拉取未读新闻（含去重记账）。记账属 fetch 自身职能，不对外暴露。
// （Fetch pulls unread news including dedup bookkeeping; the bookkeeping stays internal to fetch.）
// English: Fetch pulls unread news including dedup bookkeeping; the bookkeeping stays internal to fetch.
func (a *Agent) Fetch(ctx context.Context, since time.Time) []data.NewsItem {
	rawNews := a.fetchCatchUp(false)
	if len(rawNews) > 0 {
		log.Printf("[newsagent] 追回 %d 条新闻 (since=%s)", len(rawNews), since.Format("01-02 15:04"))
	}
	return rawNews
}

// HasNewNews 轻量探测"是否有新新闻到达"：只拉各源第 1 页并检查是否存在
// 既未读(seen)也未排队(pending)的标题，不做分页追回、不做正文抓取，远轻于 Fetch。
// 供盘中调度器高频探测，实现"新闻到达即触发扫描"而非等固定 5min 心跳。
// （HasNewNews cheaply probes whether new news has arrived: it fetches only the first page of each
// source and checks for titles that are neither seen nor queued. It skips paging and body enrichment,
// so it is far lighter than Fetch. The intraday scheduler uses it to trigger a scan the moment news
// arrives instead of waiting on the fixed 5-minute heartbeat.）
// English: HasNewNews cheaply probes whether new news has arrived: it fetches only the first page of each source and checks for titles that are neither seen nor queued (pending), skipping paging and body enrichment, so it is far lighter than Fetch. The intraday scheduler uses it to trigger a scan the moment news arrives instead of waiting on the fixed 5-minute heartbeat.
func (a *Agent) HasNewNews() bool {
	// 同花顺第 1 页探测
	// English: THS first-page probe
	if items, err := a.marketAPI.GetTonghuashunNewsPage(1, 20); err == nil {
		for _, it := range items {
			if a.tracker.IsSeen(it.Title) || a.tracker.IsPending(it.Title) {
				continue
			}
			return true
		}
	}
	// 财联社电报第 1 页探测（正文自带，来源最及时）
	// English: Cailian Press telegram first-page probe (bodies included; the timeliest source)
	if items, err := a.marketAPI.GetCLSNews(20); err == nil {
		for _, it := range items {
			if a.tracker.IsSeen(it.Title) || a.tracker.IsPending(it.Title) {
				continue
			}
			return true
		}
	}
	// 新浪第 1 页探测（补充视角）
	// English: Sina first-page probe (supplementary view)
	if items, err := a.marketAPI.GetSinaNews(20); err == nil {
		for _, it := range items {
			if a.tracker.IsSeen(it.Title) || a.tracker.IsPending(it.Title) {
				continue
			}
			return true
		}
	}
	return false
}

// UnattributedItems 返回当前待归因（已抓取但 Stage0/Stage2 尚未成功）的新闻队列，
// 按发布时间最新在前排序。供引擎在盘前/盘中每轮与新增新闻一并重试归因。
// （UnattributedItems returns the current queue of fetched-but-not-yet-attributed news, newest-first,
// for the engine to re-attempt alongside newly fetched news each premarket/intraday round.）
// English: UnattributedItems returns the current queue of fetched-but-not-yet-attributed news, sorted newest-first, for the engine to re-attempt attribution alongside newly fetched news each premarket/intraday round.
func (a *Agent) UnattributedItems() []data.NewsItem {
	a.tracker.SortPendingNewestFirst()
	return a.tracker.Pending()
}

// MarkAttributed 把成功归因的新闻标记为已见（从未归因队列移除并写入 seen 记账）。
// 归因成功定义：Stage0 分类成功且（对个股/板块）Stage2 深度分析产出事件。
// 被标记后该新闻不再进入重试队列。
// （MarkAttributed marks successfully-attributed news as seen (dropped from the queue, recorded in the
// seen ledger) so they are not retried. Success = Stage0 classified it and Stage2 emitted events.）
// English: MarkAttributed marks successfully-attributed news as seen (dropped from the queue and recorded in the seen ledger) so they are not retried. Success = Stage0 classified it and (for stock/sector items) Stage2 deep analysis emitted events.
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
// English: MarkAttributedTitles marks news as attributed by matching titles, dropping them from the unattributed queue so the next round does not re-analyze already-processed items.
func (a *Agent) MarkAttributedTitles(titles map[string]bool) {
	if len(titles) == 0 {
		return
	}
	pending := a.tracker.Pending()
	var matched []data.NewsItem
	for _, it := range pending {
		if titles[it.Title] {
			matched = append(matched, it) // 命中已归因标题 → 待处理队列中移除
		}
	}
	if len(matched) > 0 {
		a.tracker.RemovePending(matched)
	}
}

// Stage2 深度分析：LLM 对新闻全量分析，输出带方向/分数/归因的结构化事件。
// 中性事件照常输出，由引擎按阈值过滤丢弃。
// 返回第二个值 failedItems：LLM 分析失败（nil 占位，不做关键词兜底）未归因的新闻，
// 调用方应把它们留在未归因队列供下一轮重试。
// （Stage2 deep analysis: LLM analyzes all news into structured events with direction/score/attribution;
// neutral events are still emitted and the engine discards them by threshold. The second return failedItems
// lists news whose LLM analysis failed (nil placeholder, no keyword fallback), which callers should keep
// in the unattributed queue for the next round.）
// English: Stage2 deep analysis: the LLM analyzes all news into structured events with direction/score/attribution; neutral events are still emitted and the engine discards them by threshold. The second return failedItems lists news whose LLM analysis failed (nil placeholder, no keyword fallback), which callers should keep in the unattributed queue for the next round.
func (a *Agent) Stage2(items []data.NewsItem) ([]NewsEvent, []data.NewsItem) {
	events, failed := a.analyzeDeep(items)
	if a.cleaner != nil {
		// 对每个事件关联的个股做名称/代码归一化清洗（→ "名称|代码"）
		// English: normalize each event's related stocks by name/code (-> "name|code")
		for i := range events {
			events[i].CleanedStocks = a.cleaner.CleanBatch(events[i].RelatedStocks)
		}
	}
	return events, failed
}

// CleanStocks 清洗股票列表（名称或代码 → "名称|代码"），供引擎对增强归因做清理。
// （CleanStocks normalizes a stock list from name/code to "名称|代码" for engine attribution cleanup.）
// English: CleanStocks normalizes a stock list from name/code to "name|code", for the engine's attribution cleanup.
func (a *Agent) CleanStocks(items []string) []string {
	if a.cleaner == nil {
		return items
	}
	return a.cleaner.CleanBatch(items)
}

// FindStocksInText 在文本中查找出现的股票名称（供咨询/归因等按自然语言识别个股）。
// （FindStocksInText finds stock names appearing in the text for natural-language stock recognition.）
// English: FindStocksInText finds stock names appearing in the text, for natural-language stock recognition in consultations/attribution.
func (a *Agent) FindStocksInText(text string) []string {
	if a.cleaner == nil || text == "" {
		return nil
	}
	return a.cleaner.FindStocksInText(text)
}

// SaveEvents 持久化事件到 newsDB 文件并保存 tracker，供 /api/news 展示。
// （SaveEvents persists events to the newsDB file and saves the tracker for /api/news display.）
// English: SaveEvents persists events to the newsDB file and saves the tracker, for /api/news display.
func (a *Agent) SaveEvents(events []NewsEvent) {
	a.saveNewsEvents(events)
	_ = a.tracker.save()
}

// BuildIPOEvents 从 IPO 日历构建事件（直构 NewsEvent，不走 LLM）。
// （BuildIPOEvents builds events directly from the IPO calendar into NewsEvent without the LLM.）
// English: BuildIPOEvents builds events directly from the IPO calendar into NewsEvent, without the LLM.
func (a *Agent) BuildIPOEvents() []NewsEvent {
	return a.buildIPOEvents()
}

// BuildIPOFeedEvents 从 IPO 新闻流直构事件（新股/申购/上市，Score+0.5 利好，不走 LLM）。
// （BuildIPOFeedEvents builds events directly from the IPO news feed (new stock/subscription/listing) with a
// +0.5 bullish score, skipping the LLM.）
// English: BuildIPOFeedEvents builds events directly from the IPO news feed (new stock/subscription/listing) with a +0.5 bullish score, skipping the LLM.
func (a *Agent) BuildIPOFeedEvents(items []data.NewsItem) []NewsEvent {
	var out []NewsEvent
	for _, item := range items {
		// 兜底时间：新闻缺失发布时间时使用当前时间
		// English: fallback time: use the current time when the news lacks a publish time
		dt := item.Datetime
		if dt == "" {
			dt = time.Now().Format("2006-01-02 15:04:05")
		}
		// 直接按固定模板构建"利好"事件，不走 LLM，保证 IPO 类事件稳定产出
		// English: build "bullish" events directly from a fixed template, skipping the LLM for stable IPO event output
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
		// English: try to extract the involved stocks from the title and clean them; keep an empty association when nothing hits
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
// English: newsDB is the local persistence shape for news events, bucketed per trading day.
type newsDB struct {
	// 交易日 YYYYMMDD
	TradingDay string `json:"trading_day"`
	// English: trading day YYYYMMDD
	// 事件列表
	Events []NewsEvent `json:"events"`
	// English: event list
}

// saveNewsEvents 将事件持久化到 newsDB 文件，按交易日归并去重，最多保留 200 条。
// （saveNewsEvents persists events to the newsDB file, merging by trading day, deduping and keeping at most 200.）
// English: saveNewsEvents persists events to the newsDB file, merging by trading day, deduping and keeping at most 200 items.
func (a *Agent) saveNewsEvents(events []NewsEvent) {
	td := data.TradingDayDate(time.Now())
	existing := a.loadNewsDB()

	// 跨交易日则清空旧事件，重新按新交易日归档
	// English: on a new trading day, clear old events and re-archive under the new trading day
	if existing.TradingDay != td {
		existing.TradingDay = td
		existing.Events = nil
	}

	// 先建立"已存在标题"索引，用于后续去重（截断标题对比）
	// English: first index existing titles for dedup (comparing truncated titles)
	seen := make(map[string]bool)
	for _, e := range existing.Events {
		key := truncTitle(e.Title)
		seen[key] = true
	}
	for _, e := range events {
		// 取分数绝对值作为过滤依据：低于 minScore 的中性/无价值噪音直接丢弃
		// （默认 0.25，前端"显示全部"开关可降为 0，让弱档/中性事件也出现在 /api/news）
		// English: use the absolute score as the filter: neutral/valueless noise below minScore is dropped (default 0.25; the frontend "show all" toggle can lower it to 0 so weak/neutral events also appear in /api/news)
		s := e.Score
		if s < 0 {
			s = -s
		}
		if s < a.currentMinScore() {
			continue // 过滤中性/无价值噪音（§R3-8 P1-K 锁内读）
			// English: title-level dedup: keep only the first event with the same title
		}
		// 标题级去重：同标题事件仅保留第一条
		// English: cap daily event size, keeping only the newest 200
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
// English: loadNewsDB loads the persisted news-event database from file.
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
// English: AllEvents returns all locally persisted tagged news events, including neutral/general, for /api/news?all=true.
func (a *Agent) AllEvents() []NewsEvent {
	db := a.loadNewsDB()
	if db == nil {
		return nil
	}
	return db.Events
}

// FrozenEvents 返回当前全部未过期的固化事件（供引擎合并进有效事件池）。
// （FrozenEvents returns all currently non-expired frozen events for the engine's effective event pool.）
// English: FrozenEvents returns all currently non-expired frozen events for the engine's effective event pool.
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
// English: SaveFrozen writes this round's valuable events to the frozen layer: same sector+direction (Key) overwrites with the latest score, plus cross-day expiry cleanup (expired events removed). It backs up the original file before writing, as a corruption-recovery fallback.
func (a *Agent) SaveFrozen(fresh []NewsEvent) {
	td := data.TradingDayDate(time.Now())
	db := a.loadFrozenDB()
	if db == nil {
		db = &frozenDB{}
	}
	// 保留未过期的事件
	// English: keep non-expired events
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
	// English: this round's new valuable events: same sector+direction -> overwrite (score/time/stocks take the latest), otherwise append
	for _, e := range fresh {
		if !shouldFreeze(e) {
			continue
		}
		key := frozenKey(e)
		fe := FrozenEvent{NewsEvent: e, Day: td, Key: key}
		if idx, ok := byKey[key]; ok {
			kept[idx] = fe // 覆盖旧事件（Score 永远取最新值）
			// English: cap size: keep only the newest 100
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
// English: writeFrozenDB serializes and writes the frozen file, backing it up to .bak first for recovery.
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
// English: frozenKey computes the overwrite key sector|direction, using the title as the sector when there is none, and inferring the direction from the Score sign when missing.
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
// English: shouldFreeze reports whether an event should be frozen: |Score|>=0.25 and direction bullish/bearish.
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
// English: isFrozenExpired reports whether a frozen event has expired: it stays valid through its producing day (day) plus one calendar day (day+1); it is removed once the current trading day td is past day+1.
func isFrozenExpired(fe FrozenEvent, td string) bool {
	if fe.Day == "" {
		return false // 无日期（旧数据）保守保留
		// English: no date (legacy data) -> conservatively keep
	}
	d, err1 := time.Parse("20060102", fe.Day)
	t, err2 := time.Parse("20060102", td)
	if err1 != nil || err2 != nil {
		return false // 解析失败保守保留
	}
	// English: parse failure -> conservatively keep
	horizon := d.AddDate(0, 0, 1) // day+1 自然日
	return t.After(horizon)
}

// loadFrozenDB 从文件加载固化事件库（含损坏恢复）。
// 整体解析失败时先逐条尝试抢救，仍失败则把损坏文件备份为 .bak 后返回空库，绝不因坏文件阻断固化层。
// （loadFrozenDB loads the frozen-event DB with corruption recovery: salvages objects one by one on whole-parse
// failure, and backs up a hopelessly broken file as .bak before returning an empty DB.）
// English: loadFrozenDB loads the frozen-event DB with corruption recovery: on whole-parse failure it tries to salvage objects one by one, and if that also fails it backs up the broken file as .bak and returns an empty DB, never letting a bad file block the frozen layer.
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
	// English: whole-parse failure -> salvage per object (try parsing each line alone into a usable FrozenEvent)
	log.Printf("[frozen] 固化文件整体解析失败, 尝试逐条抢救")
	var salvaged []FrozenEvent
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSuffix(line, ",") // 截断的 JSON 中非末行事件对象末尾常带逗号，先去掉再尝试解析
		// English: non-last event objects in truncated JSON usually end with a comma; strip it before parsing
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
	// English: completely unparseable: back up the broken file and return an empty DB
	_ = os.WriteFile(a.frozenPath+".bak", data, 0644)
	log.Printf("[frozen] 固化文件损坏且抢救失败, 已备份为 .bak")
	return &frozenDB{}
}

// buildIPOEvents 从 IPO 日历构建 NewsEvent（新股申购/上市），跳过已存在的事件。
// （buildIPOEvents builds NewsEvents from the IPO calendar (subscription/listing), skipping existing ones.）
// English: buildIPOEvents builds NewsEvents from the IPO calendar (subscription/listing), skipping existing ones.
func (a *Agent) buildIPOEvents() []NewsEvent {
	if a.marketAPI == nil {
		return nil
	}
	now := time.Now()
	td := data.TradingDayDate(now)

	// 跨交易日重置本地缓存，避免旧事件长期占用去重索引
	// English: reset the local cache on a new trading day so old events do not occupy the dedup index forever
	existing := a.loadNewsDB()
	if existing.TradingDay != td {
		existing.Events = nil
	}
	// 建立已有 IPO 事件标题索引（仅统计来源为"IPO日历"的事件）
	// English: index existing IPO-event titles (counting only events sourced from "IPO日历")
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
		// English: distinguish the title by listing status: L = new listing, otherwise default to new subscription
		status := "新股申购"
		if ipo.ListStatus == "L" {
			status = "新股上市"
		}
		title := fmt.Sprintf("%s: %s(%s)", status, ipo.Name, ipo.Code)
		if cache[title] {
			continue // 已存在的事件跳过，避免重复注入
			// English: skip existing events to avoid duplicate injection
		}

		// 取上市日期，缺失时回退到申购日期；两者皆无则跳过
		// English: take the listing date, falling back to the subscription date when missing; skip if neither exists
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
			Content: fmt.Sprintf("expiry=%s", expiry), // 过期标记，供引擎判断事件是否已失效
			// English: expiry marker for the engine to judge whether the event has lapsed
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

// BuildIPOBootEvents 对"即将上市"的新股做板块级 LLM 深度分析（IPO 启动事件）。
// 背景：宇树科技等未上市公司不在股票库、东财行业接口也查不到板块，单纯"新股上市"
// 个股级事件被 attribution() 以 Level==个股 直接跳过，永远无法归因出机器人板块，
// 也无法把卧龙电驱/三花智控等上下游价值传导到打分池与 D1 评分上下文。
// 因此本函数把即将上市（未来 ≤ bootDays 个交易日，默认 5）的新股标题交给 LLM
// 按产业链热点分析（AnalyzeHotTopic → HotTopic），产出 Level=板块 的事件，
// 其中 Sectors=所属概念板块、Upstream/DownstreamStocks=上下游影响个股、
// Upstream/DownstreamSectors=上下游板块。随后 buildChainEvents 展开为带方向事件，
// 经 attribution() 归因出热点板块，板块成分股/上下游个股并入打分池供 D1 评分。
// 缓存：按交易日+新股代码记录已分析项，同日不重复调用 LLM（防每轮全跑）。
// （BuildIPOBootEvents runs LLM chain analysis for soon-to-list IPOs. An unlisted issuer like
// Unitree is absent from the stock DB and its industry cannot be fetched via EastMoney, so the plain
// per-stock IPO event (Level=个股) is skipped by attribution() and never produces a robot sector nor
// propagates upstream/downstream beneficiaries (e.g. Wolong/Sanhua) into the scoring pool / D1 context.
// This method feeds each soon-to-list IPO title (within bootDays trading days, default 5) to the LLM
// hotspot analyzer (AnalyzeHotTopic → HotTopic) and emits Level=板块 events carrying Sectors plus
// upstream/downstream stocks/sectors; buildChainEvents expands them into directional events that
// attribution() turns into hot sectors whose constituents/beneficiaries join the D1 scoring pool.
// Cache: analyzed IPO codes are recorded per trading day so the LLM is not re-invoked every round.）
// English: BuildIPOBootEvents runs sector-level LLM deep analysis for soon-to-list IPOs (IPO boot events). Background: unlisted issuers like Unitree are absent from the stock DB and their industry cannot be found via the EastMoney interface, so the plain per-stock "new listing" event is skipped by attribution() (Level==stock) and never produces a robotics sector, nor does it propagate upstream/downstream beneficiaries (e.g. Wolong Electric Drive / Sanhua) into the scoring pool and D1 scoring context. So this function feeds each soon-to-list IPO title (within bootDays trading days, default 5) to the LLM for supply-chain hotspot analysis (AnalyzeHotTopic -> HotTopic), producing Level=sector events where Sectors = the concept sectors, Upstream/DownstreamStocks = upstream/downstream affected stocks, and Upstream/DownstreamSectors = upstream/downstream sectors. buildChainEvents then expands them into directional events; attribution() derives hot sectors, and the sectors' constituents / upstream-downstream stocks join the scoring pool for D1 scoring. Cache: analyzed items are recorded per trading-day+code so the LLM is not re-invoked every round.
func (a *Agent) BuildIPOBootEvents() []NewsEvent {
	// §R3-8 P1-K 锁内快照：llmClient 由热更线程并发替换，先取指针再放锁外慢路径。
	client := a.llmClientSnapshot()
	if a.marketAPI == nil || client == nil {
		return nil
	}
	now := time.Now()
	td := data.TradingDayDate(now)

	// 跨交易日重置缓存（bootAnalyzed: "交易日:代码" → 已分析）
	// English: reset the cache on a new trading day (bootAnalyzed: "trading-day:code" -> analyzed)
	a.hotMu.Lock()
	if a.bootCacheDay != td {
		a.bootCache = make(map[string]bool)
		a.bootCacheDay = td
	}
	a.hotMu.Unlock()

	list, err := a.marketAPI.GetEastMoneyIPOCalendar()
	if err != nil {
		log.Printf("[newsagent] 获取IPO日历失败(boot): %v", err)
		return nil
	}

	// bootDays 启动期事件探测的交易日前瞻天数。
	const bootDays = 5
	bootDeadline := data.AddTradingDays(td, bootDays)
	var titles []string
	// bootTarget 启动期事件目标：代码 + 名称 + 事件标题。
	type bootTarget struct {
		code, name, title string
	}
	var targets []bootTarget
	for _, ipo := range list {
		if ipo.ListStatus != "U" && ipo.ListStatus != "" {
			continue // 已上市的不再视为启动事件
			// English: already-listed IPOs are no longer boot events
		}
		listing := ipo.ListingDate
		if listing == "" {
			listing = ipo.IPODate
		}
		if listing == "" || listing > bootDeadline {
			continue // 超过 bootDays 内上市的跳过
			// English: skip listings beyond bootDays
		}
		key := td + ":" + ipo.Code
		a.hotMu.RLock()
		done := a.bootCache[key]
		a.hotMu.RUnlock()
		if done {
			continue
		}
		title := fmt.Sprintf("%s(%s) 将于 %s 上市，请分析该新股上市对A股产业链的价值传导影响", ipo.Name, ipo.Code, listing)
		targets = append(targets, bootTarget{code: ipo.Code, name: ipo.Name, title: title})
		titles = append(titles, title)
	}
	if len(titles) == 0 {
		return nil
	}

	var out []NewsEvent
	for i, t := range titles {
		ht, err := client.AnalyzeHotTopic(t)
		if err != nil || ht == nil {
			// §修复：此前 bootCache 在调 LLM 之前就写 true——LLM 失败 continue 后该 IPO 当天
			// 永不重试（缓存次日才重置），与全系统"失败留队重试"原则相悖。现改为成功才记缓存，
			// 失败的下一轮重新分析。English: the old code marked bootCache before the LLM call,
			// so a failed analysis was never retried that day; mark only on success now.
			log.Printf("[newsagent] IPO启动LLM分析失败(%s): %v", targets[i].code, err)
			continue
		}
		a.hotMu.Lock()
		a.bootCache[td+":"+targets[i].code] = true
		a.hotMu.Unlock()
		postProcess(ht)
		// 强制板块级：IPO 启动是产业链事件，非个股
		// English: force sector level: an IPO boot is a supply-chain event, not a stock-level one
		ht.Level = "板块"
		item := data.NewsItem{
			Title:    t,
			Datetime: now.Format("2006-01-02 15:04:05"),
			Source:   "IPO日历",
			Content:  fmt.Sprintf("新股 %s(%s) 上市，产业链价值传导启动", targets[i].name, targets[i].code),
		}
		out = append(out, buildChainEvents(ht, item)...)
	}
	if len(out) > 0 {
		log.Printf("[newsagent] IPO启动板块事件注入 %d 个", len(out))
	}
	return out
}

// isIPOExpired 判断 IPO 事件是否已过期（当前交易日 > 到期日）。
// （isIPOExpired reports whether an IPO event has expired: current trading day > expiry.）
// English: isIPOExpired reports whether an IPO event has expired: current trading day > expiry.
func isIPOExpired(e NewsEvent, td string) bool {
	if !strings.HasPrefix(e.Content, "expiry=") {
		return false
	}
	expiry := strings.TrimPrefix(e.Content, "expiry=")
	return td > expiry
}

// truncTitle 截断标题到 60 个字符，用于去重对比。
// English: truncTitle truncates a title to 60 characters for dedup comparison.
func truncTitle(t string) string {
	if len(t) > 60 {
		return t[:60]
	}
	return t
}
