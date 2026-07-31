// Package engine 顶层编排引擎：持有全部子代理，驱动新闻流水线（Stage0/1/2）→ 归因 → 板块验证 → 战法扫描 → 信号聚合。
// 各子模块只输出结果，不做跨模块调用；唯一编排者是本引擎。
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/strategy_engine"
)

// Engine 顶层编排引擎，持有全部子代理引用与利好/利空开关。
type Engine struct {
	mu sync.RWMutex

	marketAPI    *data.MarketAPI
	newsAgent    *newsagent.Agent
	strategy     *strategy_engine.Engine
	sectorAgent  *sector_agent.Agent
	combatAgent  *combat_agent.Agent
	agg          *display.Aggregator
	rpt          *report.Report
	stockTracker *data.StockTracker
	wlMgr        *data.WatchlistManager
	sse          *server.SSEBroker
	llmClient    *llm.Client
	ths          *data.THSClient
	scanner      *data.SectorScanner

	longEnabled  bool // 利好开关（做多分支）
	shortEnabled bool // 利空开关（做空分支）

	debugInfo    *newsagent.DebugInfo
	stageRecords []newsagent.DebugInfo // 当日全量轮次记录（固化到磁盘）
	stageRecPath string

	msgStore   *data.MessageStore // 消息中心持久化存储
	hotRecords []data.HotRecord   // 当日热点板块轮次记录（固化到磁盘）
	hotRecPath string

	sectorEventTimes map[string]time.Time // 板块事件时间戳（重复事件衰减状态）
	emotionCfg       *config.EmotionConfig // 情绪周期阈值（SSE 广播情绪阶段）
}

// SetEmotionConfig 设置情绪周期阈值（线程安全）。
func (e *Engine) SetEmotionConfig(cfg *config.EmotionConfig) {
	e.mu.Lock()
	e.emotionCfg = cfg
	e.mu.Unlock()
}

// stageRecordFile Stage 记录磁盘持久化结构（按交易日分桶）。
type stageRecordFile struct {
	TradingDay string                `json:"trading_day"`
	Records    []newsagent.DebugInfo `json:"records"`
}

// hotRecordFile 热点板块记录磁盘持久化结构（按交易日分桶）。
type hotRecordFile struct {
	TradingDay string           `json:"trading_day"`
	Records    []data.HotRecord `json:"records"`
}

// New 创建顶层编排引擎。
func New(
	marketAPI *data.MarketAPI,
	newsAgent *newsagent.Agent,
	strategy *strategy_engine.Engine,
	sectorAgent *sector_agent.Agent,
	combatAgent *combat_agent.Agent,
	agg *display.Aggregator,
	rpt *report.Report,
	stockTracker *data.StockTracker,
	wlMgr *data.WatchlistManager,
	sse *server.SSEBroker,
	llmClient *llm.Client,
	ths *data.THSClient,
	dataDir string,
) *Engine {
	stageRecPath := ""
	msgPath := ""
	if dataDir != "" {
		stageRecPath = filepath.Join(dataDir, "stage_records.json")
		msgPath = filepath.Join(dataDir, "messages.json")
	}
	hotRecPath := ""
	if dataDir != "" {
		hotRecPath = filepath.Join(dataDir, "hot_records.json")
	}
	e := &Engine{
		marketAPI:    marketAPI,
		newsAgent:    newsAgent,
		strategy:     strategy,
		sectorAgent:  sectorAgent,
		combatAgent:  combatAgent,
		agg:          agg,
		rpt:          rpt,
		stockTracker: stockTracker,
		wlMgr:        wlMgr,
		sse:          sse,
		llmClient:    llmClient,
		ths:          ths,
		longEnabled:  true,
		shortEnabled: false,
		stageRecords: loadStageRecords(stageRecPath),
		stageRecPath: stageRecPath,
		msgStore:     data.NewMessageStore(msgPath),
		hotRecords:   loadHotRecords(hotRecPath),
		hotRecPath:   hotRecPath,
		sectorEventTimes: make(map[string]time.Time),
	}
	e.syncMessages(nil)
	return e
}

// loadStageRecords 从磁盘加载当日 Stage 记录；跨交易日自动重置。
func loadStageRecords(path string) []newsagent.DebugInfo {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f stageRecordFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[engine] stage_records 解析失败: %v", err)
		return nil
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return nil
	}
	return f.Records
}

// persistStageRecords 将当日 Stage 记录写入磁盘。
func (e *Engine) persistStageRecords() {
	if e.stageRecPath == "" {
		return
	}
	e.mu.RLock()
	f := stageRecordFile{TradingDay: data.TradingDayDate(time.Now()), Records: e.stageRecords}
	e.mu.RUnlock()
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("[engine] stage_records 序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(e.stageRecPath, raw, 0644); err != nil {
		log.Printf("[engine] stage_records 写入失败: %v", err)
	}
}

// GetStageRecords 返回当日全量 Stage 轮次记录（供复盘 / 策略侧实时调取）。
func (e *Engine) GetStageRecords() []newsagent.DebugInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]newsagent.DebugInfo, len(e.stageRecords))
	copy(out, e.stageRecords)
	return out
}

// ── 热点板块记录 ──

// loadHotRecords 从磁盘加载当日热点板块记录；跨交易日自动重置。
func loadHotRecords(path string) []data.HotRecord {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f hotRecordFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[engine] hot_records 解析失败: %v", err)
		return nil
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return nil
	}
	return f.Records
}

// persistHotRecords 将当日热点板块记录写入磁盘。
func (e *Engine) persistHotRecords() {
	if e.hotRecPath == "" {
		return
	}
	e.mu.RLock()
	f := hotRecordFile{TradingDay: data.TradingDayDate(time.Now()), Records: e.hotRecords}
	e.mu.RUnlock()
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("[engine] hot_records 序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(e.hotRecPath, raw, 0644); err != nil {
		log.Printf("[engine] hot_records 写入失败: %v", err)
	}
}

// GetHotRecords 返回当日全量热点板块轮次记录（供前端展示）。
func (e *Engine) GetHotRecords() []data.HotRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]data.HotRecord, len(e.hotRecords))
	copy(out, e.hotRecords)
	return out
}

// captureHotRecord 将本轮热点板块（匹配同花顺 top-20 行情表后）固化为记录。
// 无板块归因或匹配不到真实板块时跳过。
func (e *Engine) captureHotRecord(sr *strategy_engine.StrategyResult) {
	if sr == nil || len(sr.HotSectors) == 0 {
		return
	}
	e.mu.RLock()
	ths := e.ths
	e.mu.RUnlock()
	if ths == nil {
		return
	}
	boards, err := ths.GetTopBoards()
	if err != nil {
		log.Printf("[engine] 热点记录: 同花顺板块行情获取失败: %v", err)
		return
	}
	sectorMap := make(map[string]data.SectorInfo, len(boards))
	for _, b := range boards {
		sectorMap[b.Name] = b
	}
	rec := data.HotRecord{ProcessTime: time.Now()}
	for _, sec := range sr.HotSectors {
		si, ok := sectorMap[sec.Name]
		if !ok {
			continue
		}
		rec.Sectors = append(rec.Sectors, data.HotSectorRecord{
			Name:       sec.Name,
			Code:       si.Code,
			Score:      sec.Score,
			ChangePct:  si.ChangePct,
			D1:         0,
			Direction:  sec.Direction,
			LimitupCnt: si.LimitupCnt,
			NetInflow:  si.NetInflow,
			Reason:     sec.Reason,
			NewsTitles: sec.NewsTitles,
		})
	}
	if len(rec.Sectors) == 0 {
		return
	}
	e.mu.Lock()
	e.hotRecords = append(e.hotRecords, rec)
	if len(e.hotRecords) > 50 {
		e.hotRecords = e.hotRecords[len(e.hotRecords)-50:]
	}
	e.mu.Unlock()
	e.persistHotRecords()
}

// ── 消息中心 ──

// GetMessages 返回消息中心全部消息（按生成时间倒序）。
func (e *Engine) GetMessages() []data.MessageItem {
	if e.msgStore == nil {
		return nil
	}
	return e.msgStore.List()
}

// ClearMessages 清空消息中心全部消息。
func (e *Engine) ClearMessages() {
	if e.msgStore != nil {
		e.msgStore.ClearAll()
	}
}

// DeleteMessage 手工删除单条消息。
func (e *Engine) DeleteMessage(id string) {
	if e.msgStore != nil {
		e.msgStore.Delete(id)
	}
}

// syncMessages 将本轮止盈止损告警与持仓提示合并进消息存储（按稳定键去重）。
func (e *Engine) syncMessages(alertSignals []combat_agent.Signal) {
	if e.msgStore == nil {
		return
	}
	items := make([]data.MessageItem, 0, len(alertSignals)+2)
	for _, sig := range alertSignals {
		level := sig.AlertType
		if level == "" {
			level = "策略信号"
		}
		items = append(items, data.MessageItem{
			ID:          sig.Code + "@" + level,
			Code:        sig.Code,
			Name:        sig.Name,
			Level:       level,
			Action:      sig.Action,
			Strategy:    sig.Strategy,
			Time:        sig.GeneratedAt.Format("15:04:05"),
			Title:       fmt.Sprintf("%s %s", level, sig.Code),
			Body:        sig.Reason,
			Direction:   sig.Direction,
			GeneratedAt: sig.GeneratedAt,
		})
	}
	for _, l := range e.rpt.List() {
		if l.Status != "持仓中" && l.ExitAt == nil {
			continue
		}
		pct := ""
		if l.ProfitPct != nil {
			pct = fmt.Sprintf("%.1f%%", *l.ProfitPct)
		}
		items = append(items, data.MessageItem{
			ID:          "hold@" + l.SignalID,
			Code:        l.Code,
			Name:        l.Name,
			Level:       "持仓提示",
			Action:      l.Status,
			Strategy:    l.Strategy,
			Time:        l.EntryAt.Format("15:04:05"),
			Title:       fmt.Sprintf("%s %s", l.Status, l.Code),
			Body:        fmt.Sprintf("策略:%s 入场:%.2f %s", l.Strategy, l.EntryPrice, pct),
			Direction:   l.Direction,
			GeneratedAt: l.EntryAt,
		})
	}
	e.msgStore.Sync(items)
}

// SetScanner 设置板块扫描器（线程安全，透传给策略引擎）。
func (e *Engine) SetScanner(scanner *data.SectorScanner) {
	e.mu.Lock()
	e.scanner = scanner
	e.strategy.SetScanner(scanner)
	e.mu.Unlock()
}

// SetSectorSource 设置同花顺出口（板块名单/行情表）。
func (e *Engine) SetSectorSource(ths *data.THSClient) {
	e.mu.Lock()
	e.ths = ths
	e.mu.Unlock()
}

// SetLLMClient 热重建 LLM 客户端（前端改配置时调用）。
func (e *Engine) SetLLMClient(c *llm.Client) {
	e.mu.Lock()
	e.llmClient = c
	e.newsAgent.SetLLMClient(c)
	e.mu.Unlock()
}

// ── 利好/利空开关 ──

func (e *Engine) SetLongEnabled(v bool) {
	e.mu.Lock()
	e.longEnabled = v
	e.mu.Unlock()
}

func (e *Engine) LongEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.longEnabled
}

func (e *Engine) SetShortEnabled(v bool) {
	e.mu.Lock()
	e.shortEnabled = v
	e.mu.Unlock()
}

func (e *Engine) ShortEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.shortEnabled
}

// GetDebugInfo 返回最近一轮流水线的调试数据。
func (e *Engine) GetDebugInfo() *newsagent.DebugInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.debugInfo
}

// Run 驱动一轮完整流水线：拉取 → Stage0 → Stage1/2 → 阈值过滤 → 归因 → 板块验证 → 战法扫描 → 信号聚合 → 广播。
// since 为新闻追回起始时间，由调用方（主循环）根据市场时段计算。
func (e *Engine) Run(ctx context.Context, since time.Time) *strategy_engine.StrategyResult {
	t0 := time.Now()

	// 0. 刷新同花顺板块名单到 scanner（FindSectorsByNames/归因校验依赖真实板块名单）
	e.refreshSectors()

	// 1. 拉取原始新闻（含去重记账，属 newsagent 职能）
	rawNews := e.newsAgent.Fetch(ctx, since)
	if len(rawNews) == 0 {
		log.Printf("[engine] 无新新闻 (since=%s)", since.Format("01-02 15:04"))
		return &strategy_engine.StrategyResult{}
	}

	// 2. Stage0 归因分类：个股 / 板块 / 一般（合并垃圾过滤+价值初筛+标题党复核）
	st0 := e.newsAgent.Stage0(rawNews)

	// 2b. 标题党修复：LLM 校正标题应用到原文（供 Stage2 分析、事件与展示使用）
	for i, t := range st0.CorrectedTitle {
		if i >= 0 && i < len(rawNews) && t != "" {
			rawNews[i].Title = t
		}
	}

	// 3. 收集全量事件
	var events []newsagent.NewsEvent

	// 3a. 个股新闻：跳过 Stage1，直接 Stage2 深度分析
	if len(st0.StockIdx) > 0 {
		stockItems := pickItems(rawNews, st0.StockIdx)
		events = append(events, e.newsAgent.Stage2(stockItems)...)
	}

	// 3b. 板块新闻：material 价值初筛（合并进 Stage0 单次调用）→ Stage2 深度分析
	if len(st0.SectorIdx) > 0 {
		sectorItems := pickItems(rawNews, st0.SectorIdx)
		var kept []int
		for j := range sectorItems {
			if st0.Material[st0.SectorIdx[j]] {
				kept = append(kept, j)
			}
		}
		if len(kept) > 0 {
			events = append(events, e.newsAgent.Stage2(pickItems(sectorItems, kept))...)
		}
	}

	// 3c. IPO 新闻：直构事件（不走 LLM）
	if len(st0.IpoIdx) > 0 {
		ipoItems := pickItems(rawNews, st0.IpoIdx)
		events = append(events, e.newsAgent.BuildIPOFeedEvents(ipoItems)...)
	}

	// 3d. 一般新闻：不入引擎，仅由 SaveEvents 保存展示

	// 4. 注入 IPO 日历事件
	events = append(events, e.newsAgent.BuildIPOEvents()...)

	// 5. 持久化全量事件供 /api/news 展示（含中性/一般新闻）
	e.newsAgent.SaveEvents(events)

	// 6. 阈值过滤：仅 |score| ≥ 0.50 进引擎（弱/中性丢弃）
	valid := filterThreshold(events, 0.50)
	if len(valid) == 0 {
		log.Printf("[engine] 无有效事件(|score|>=0.5), 跳过本轮归因")
		e.captureDebug(rawNews, st0, events)
		return &strategy_engine.StrategyResult{}
	}

	// 6a. 事件聚簇：同板块/同方向的重复新闻合并为单条（去重避免刷屏）
	valid = clusterEvents(valid)

	// 6b. 事件衰减：同板块同方向事件在窗口内重复出现时按 0.5^(h/4) 降权
	e.applyEventDecay(valid)

	// 6c. 衰减后再次阈值过滤（重复事件降权后可掉出 0.5 线）
	valid = filterThreshold(valid, 0.50)
	if len(valid) == 0 {
		log.Printf("[engine] 无有效事件(衰减后), 跳过本轮归因")
		e.captureDebug(rawNews, st0, events)
		return &strategy_engine.StrategyResult{}
	}

	// 6b. 板块验真回填：剔除 LLM 幻觉板块名（命中真实板块名单才保留）
	e.verifySectorAttribution(valid)

	// 6c. 板块→个股事件级传播：板块 top 成分股注入 RelatedStocks 进个股监测池
	e.propagateSectorToStocks(valid)

	// 7. 策略评估：归因 + 分流 + 评分池 + 行情数据
	positions := e.rpt.HeldPositionCodes()
	watchlist := e.wlMgr.List()
	sr := e.strategy.Evaluate(ctx, valid, positions, watchlist)

	// 7b. 固化本轮热点板块记录（同花顺 top-20 匹配后，供前端展示历史）
	e.captureHotRecord(sr)

	// 8. D1 评分（所有打分池个股）
	d1Scorer := combat_agent.NewD1Scorer(e.llmClient, "")
	d1Scores := d1Scorer.BatchScore(sr.ScoringPool, valid, sr.MarketData)

	td := data.TradingDayDate(time.Now())

	// 8b. 当日涨停池 + 事件新闻简报（龙头识别 / 涨停分类 / 预期差检测）
	pool, poolErr := e.marketAPI.GetLimitUpPool("")
	if poolErr != nil {
		log.Printf("[engine] 涨停池拉取失败: %v", poolErr)
	}
	newsBriefs := newsBriefsByCode(valid)

	// 9. 板块验证（开关控制），结果同时用于战法扫描与看板展示
	var verifiedBull, verifiedBear []sector_agent.VerifiedSector
	if e.LongEnabled() {
		verifiedBull = e.sectorAgent.Verify(sr.HotSectors)
	}
	if e.ShortEnabled() {
		verifiedBear = e.sectorAgent.Verify(sr.BearSectors)
	}

	// 10. 利好开关开 → 做多分支
	var bullSignals []combat_agent.Signal
	if e.LongEnabled() {
		bullInput := combat_agent.ScanInput{
			Sectors:     verifiedBull,
			L1Score:     sr.L1Score,
			L1Blocked:   sr.L1Blocked,
			MarketData:  sr.MarketData,
			D1Scores:    d1Scores,
			LimitUpPool: pool,
			News:        newsBriefs,
		}
		bullSignals = e.combatAgent.ScanLong(bullInput)
	}

	// 10b. 涨停池增强：龙头识别 + 涨停分类 + 预期差（并入做多信号流）
	var gapCodes []string
	for code := range newsBriefs {
		gapCodes = append(gapCodes, code)
	}
	limitSignals := e.combatAgent.ScanLimitUp(combat_agent.ScanInput{
		LimitUpPool:      pool,
		IndividualStocks: gapCodes,
		MarketData:       sr.MarketData,
		L1Blocked:        sr.L1Blocked,
		News:             newsBriefs,
	})
	bullSignals = append(bullSignals, limitSignals...)

	// 11. 利空开关开 → 做空分支
	var bearSignals []combat_agent.Signal
	if e.ShortEnabled() {
		bearInput := combat_agent.ScanInput{
			Sectors:     verifiedBear,
			L1Score:     sr.L1Score,
			L1Blocked:   sr.L1Blocked,
			MarketData:  sr.MarketData,
			D1Scores:    d1Scores,
			LimitUpPool: pool,
			News:        newsBriefs,
		}
		bearSignals = e.combatAgent.ScanShort(bearInput)
	}

	// 12. 个股直入（跳过板块验证）：分做多/做空两组
	var newLong, newShort []string
	for _, st := range sr.LongStocks {
		newLong = append(newLong, st.Code)
	}
	for _, st := range sr.ShortStocks {
		newShort = append(newShort, st.Code)
	}

	trackedLong := e.stockTracker.GetActiveByDirection(td, "利好")
	trackedShort := e.stockTracker.GetActiveByDirection(td, "利空")

	// 8a/8b 个股监测池 = 新闻个股 + 已跟踪个股 + 持仓 + 自选（去重）
	longCodes := mergeCodes(trackedCodes(trackedLong), newLong, positions, watchlist)
	shortCodes := mergeCodes(trackedCodes(trackedShort), newShort, positions, watchlist)

	var individualSignals []combat_agent.Signal
	if len(longCodes) > 0 && e.LongEnabled() {
		in := combat_agent.ScanInput{
			IndividualStocks: longCodes,
			L1Score:          sr.L1Score,
			L1Blocked:        sr.L1Blocked,
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
		}
		individualSignals = append(individualSignals, e.combatAgent.ScanLong(in)...)
	}
	if len(shortCodes) > 0 && e.ShortEnabled() {
		in := combat_agent.ScanInput{
			IndividualStocks: shortCodes,
			L1Score:          sr.L1Score,
			L1Blocked:        sr.L1Blocked,
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
		}
		individualSignals = append(individualSignals, e.combatAgent.ScanShort(in)...)
	}

	expiry := data.AddTradingDays(td, 1)
	for _, sig := range individualSignals {
		dir := "利好"
		if sig.Direction == "做空" {
			dir = "利空"
		}
		e.stockTracker.Add(sig.Code, sig.Name, dir, sig.Reason, td, expiry)
	}

	allSigCodes := make([]string, len(individualSignals))
	for i, sig := range individualSignals {
		allSigCodes[i] = sig.Code
	}
	e.stockTracker.OnCycleDone(td, allSigCodes)

	bullSignals = append(bullSignals, individualSignals...)

	// 13. 持仓止盈/止损提醒
	alertSignals := e.combatAgent.CheckPositionAlerts(e.rpt, e.marketAPI)

	// 14. 聚合器更新看板
	e.agg.Update(sr, verifiedBull, verifiedBear, bullSignals, bearSignals, alertSignals, e.rpt)

	// 14b. 告警/持仓提示合并进消息中心（持久化）
	e.syncMessages(alertSignals)

	// 15. 调试数据
	e.captureDebug(rawNews, st0, events)

	// 16. SSE 广播通知前端（附信号摘要）
	if e.sse != nil && e.sse.Len() > 0 {
		payload := map[string]string{
			"type":   "scan",
			"status": "done",
			"bull":   fmt.Sprintf("%d", len(bullSignals)),
			"bear":   fmt.Sprintf("%d", len(bearSignals)),
			"alert":  fmt.Sprintf("%d", len(alertSignals)),
			"time":   time.Now().Format("15:04:05"),
		}
		if pool != nil {
			payload["zt_pool"] = fmt.Sprintf("%d", len(pool))
		}
		if e.emotionCfg != nil {
			payload["emotion"] = data.DetectEmotionPhaseV2(pool, 0, 0, e.emotionCfg)
		}
		e.sse.Broadcast(payload)
	}

	log.Printf("[engine] 流水线完成: %d条原始 → %d事件 → %d有效 (%v)",
		len(rawNews), len(events), len(valid), time.Since(t0))

	return sr
}

// refreshSectors 每轮拉取同花顺全量板块名单并喂给 scanner，保证 FindSectorsByNames 可命中真实板块。
func (e *Engine) refreshSectors() {
	e.mu.RLock()
	scanner, ths := e.scanner, e.ths
	e.mu.RUnlock()
	if scanner == nil || ths == nil {
		return
	}
	boards, err := ths.GetBoardList()
	if err != nil {
		log.Printf("[engine] 同花顺板块名单刷新失败: %v", err)
		return
	}
	scanner.Update(boards, 0, 0, 0)
	e.feedRPS(boards)
	log.Printf("[engine] 板块名单刷新: %d 个 (一级行业+概念)", len(boards))
}

// feedRPS 按当日涨跌幅百分位构造 RPS20/RPS60 近似值并喂给板块验证代理。
// 真实多周期 RPS 需历史K线（后续 Phase F 可升级），当日涨幅近似足以支撑 RPSRank 排序。
func (e *Engine) feedRPS(boards []data.SectorInfo) {
	e.mu.RLock()
	sa := e.sectorAgent
	e.mu.RUnlock()
	if sa == nil || len(boards) == 0 {
		return
	}
	type br struct {
		code, name string
		pct        float64
	}
	rows := make([]br, 0, len(boards))
	for _, b := range boards {
		if b.Name == "" {
			continue
		}
		rows = append(rows, br{code: b.Code, name: b.Name, pct: b.ChangePct})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].pct > rows[j].pct })
	rps := make([]data.SectorRPS, 0, len(rows))
	for i, r := range rows {
		rank := 0.0
		if len(rows) > 1 {
			rank = 100 * (1 - float64(i)/float64(len(rows)-1))
		}
		rps = append(rps, data.SectorRPS{
			Code:   r.code,
			Name:   r.name,
			RPS20:  rank,
			RPS60:  rank,
		})
	}
	sa.FeedRPS(rps)
}

// verifySectorAttribution 板块验真回填：对 level=板块 且 |score|≥0.5 的事件，
// 用真实板块名单（FindSectorsByNames 精确命中）校验 Sectors，剔除 LLM 幻觉板块。
func (e *Engine) verifySectorAttribution(events []newsagent.NewsEvent) {
	e.mu.RLock()
	scanner := e.scanner
	e.mu.RUnlock()
	if scanner == nil {
		return
	}
	removed := 0
	for i := range events {
		ev := &events[i]
		if ev.Level != "板块" || absScore(ev.Score) < 0.5 || len(ev.Sectors) == 0 {
			continue
		}
		kept := make([]string, 0, len(ev.Sectors))
		for _, s := range ev.Sectors {
			if len(scanner.FindSectorsByNames([]string{s})) > 0 {
				kept = append(kept, s)
			}
		}
		if len(kept) != len(ev.Sectors) {
			removed += len(ev.Sectors) - len(kept)
			ev.Sectors = kept
		}
	}
	if removed > 0 {
		log.Printf("[engine] 板块验真回填: 剔除 %d 个非真实板块名", removed)
	}
}

// propagateSectorToStocks 板块→个股事件级传播：对命中真实板块的板块级事件，
// 取板块前10成分股注入 RelatedStocks（"名称(代码)"），并同步清洗 CleanedStocks，
// 使板块权重沿 事件→个股监测池(8a/8b) 传递。同一板块每轮只取一次成分股。
func (e *Engine) propagateSectorToStocks(events []newsagent.NewsEvent) {
	e.mu.RLock()
	scanner := e.scanner
	e.mu.RUnlock()
	if scanner == nil {
		return
	}
	injected := 0
	fetched := make(map[string]bool)
	for i := range events {
		ev := &events[i]
		if ev.Level != "板块" || absScore(ev.Score) < 0.5 {
			continue
		}
		for _, name := range ev.Sectors {
			si := e.sectorByName(name)
			if si == nil {
				continue
			}
			if fetched[si.Code] {
				continue
			}
			fetched[si.Code] = true
			stocks, err := e.marketAPI.GetSectorStocks(si.Code, 10)
			if err != nil {
				log.Printf("[engine] 板块成分股获取失败 %s: %v", name, err)
				continue
			}
			for _, st := range stocks {
				label := fmt.Sprintf("%s(%s)", st.Name, st.Code)
				if strContains(ev.RelatedStocks, label) || strContains(ev.RelatedStocks, st.Name) {
					continue
				}
				ev.RelatedStocks = append(ev.RelatedStocks, label)
				injected++
			}
		}
		if injected > 0 || len(ev.RelatedStocks) > 0 {
			ev.CleanedStocks = e.newsAgent.CleanStocks(ev.RelatedStocks)
		}
	}
	if injected > 0 {
		log.Printf("[engine] 板块→个股传播: 注入 %d 只成分股", injected)
	}
}

// sectorByName 精确匹配板块名称返回 SectorInfo，未命中返回 nil。
func (e *Engine) sectorByName(name string) *data.SectorInfo {
	if e.scanner == nil {
		return nil
	}
	for _, s := range e.scanner.FindSectorsByNames([]string{name}) {
		if s.Name == name {
			return &s
		}
	}
	return nil
}

// absScore 返回带符号分数的绝对值。
func absScore(s float64) float64 {
	if s < 0 {
		return -s
	}
	return s
}

// strContains 判断字符串切片中是否包含指定元素。
func strContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// captureDebug 收集本轮流水线调试数据，并固化到当日 Stage 记录。
func (e *Engine) captureDebug(rawNews []data.NewsItem, st0 newsagent.Stage0Result, events []newsagent.NewsEvent) {
	titles := make([]string, len(rawNews))
	for i, n := range rawNews {
		titles[i] = n.Title
	}
	idx := append([]int{}, st0.StockIdx...)
	idx = append(idx, st0.SectorIdx...)
	idx = append(idx, st0.IpoIdx...)

	e.mu.Lock()
	e.debugInfo = &newsagent.DebugInfo{
		Stage1Mode:    "combined",
		RawCount:      len(rawNews),
		SelectedCount: len(idx),
		RawTitles:     titles,
		SelectedIdx:   idx,
		Stage2Events:  events,
		ProcessTime:   time.Now(),
	}
	e.stageRecords = append(e.stageRecords, *e.debugInfo)
	if len(e.stageRecords) > 20 {
		e.stageRecords = e.stageRecords[len(e.stageRecords)-20:]
	}
	e.mu.Unlock()

	e.persistStageRecords()
}

// pickItems 按索引从新闻列表中选取条目。
func pickItems(items []data.NewsItem, indices []int) []data.NewsItem {
	var out []data.NewsItem
	for _, i := range indices {
		if i >= 0 && i < len(items) {
			out = append(out, items[i])
		}
	}
	return out
}

// filterThreshold 过滤事件：仅保留 |score| ≥ 阈值的（弱/中性丢弃）。
func filterThreshold(events []newsagent.NewsEvent, threshold float64) []newsagent.NewsEvent {
	var out []newsagent.NewsEvent
	for _, ev := range events {
		s := ev.Score
		if s < 0 {
			s = -s
		}
		if s >= threshold {
			out = append(out, ev)
		}
	}
	return out
}

// trackedCodes 提取跟踪个股代码列表。
func trackedCodes(tracked []*data.TrackedStock) []string {
	out := make([]string, 0, len(tracked))
	for _, s := range tracked {
		out = append(out, s.Code)
	}
	return out
}

// mergeCodes 按顺序合并多组个股代码并去重（保留首次出现顺序）。
func mergeCodes(groups ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, g := range groups {
		for _, c := range g {
			c = strings.TrimSpace(c)
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// newsBriefsByCode 将有效新闻事件转为 code → 新闻简报映射（供预期差检测）。
// 方向由事件 Score 符号推导（score≥0 视为利好）。
func newsBriefsByCode(events []newsagent.NewsEvent) map[string][]combat_agent.NewsBrief {
	m := make(map[string][]combat_agent.NewsBrief)
	for _, ev := range events {
		if ev.Title == "" {
			continue
		}
		positive := ev.Score >= 0
		for _, code := range ev.RelatedStocks {
			code = strings.TrimSpace(code)
			if code == "" {
				continue
			}
			m[code] = append(m[code], combat_agent.NewsBrief{
				Title:    ev.Title,
				Positive: positive,
				Time:     ev.Datetime,
			})
		}
	}
	return m
}

// clusterEvents 事件聚簇：同方向且共享任一板块的事件合并为单条。
// 簇内标题用" | "连接（最多保留 3 条），个股/相关股票去重合并，Score 取 |score| 最大者。
// 防止同一主题的多条快讯在信号流中刷屏。
func clusterEvents(events []newsagent.NewsEvent) []newsagent.NewsEvent {
	if len(events) < 2 {
		return events
	}
	// 为每个板块名维护簇索引
	clusterOf := make(map[string]int)
	clusters := make([][]int, 0, len(events))
	assign := func(ev newsagent.NewsEvent) int {
		if ev.Level == "个股" {
			return -1
		}
		for _, sec := range ev.Sectors {
			if idx, ok := clusterOf[sec]; ok {
				return idx
			}
		}
		return -1
	}
	for i, ev := range events {
		idx := assign(ev)
		if idx < 0 {
			idx = len(clusters)
			clusters = append(clusters, nil)
		}
		clusters[idx] = append(clusters[idx], i)
		for _, sec := range ev.Sectors {
			clusterOf[sec] = idx
		}
	}

	out := make([]newsagent.NewsEvent, 0, len(clusters))
	for _, idxs := range clusters {
		if len(idxs) == 0 {
			continue
		}
		merged := events[idxs[0]]
		if len(idxs) == 1 {
			out = append(out, merged)
			continue
		}
		// 合并标题（最多3条）
		titles := []string{merged.Title}
		for _, i := range idxs[1:] {
			ev := events[i]
			if len(titles) < 3 && ev.Title != "" && !containsStr(titles, ev.Title) {
				titles = append(titles, ev.Title)
			}
			// 保留 |score| 最大的事件属性
			if absScore(ev.Score) > absScore(merged.Score) {
				merged.Score = ev.Score
				merged.Direction = ev.Direction
				merged.Reason = ev.Reason
				merged.EventType = ev.EventType
				merged.Level = ev.Level
			}
			merged.RelatedStocks = mergeStr(merged.RelatedStocks, ev.RelatedStocks)
			merged.CleanedStocks = mergeStr(merged.CleanedStocks, ev.CleanedStocks)
		}
		merged.Title = strings.Join(titles, " | ")
		out = append(out, merged)
	}
	if len(out) != len(events) {
		log.Printf("[engine] 事件聚簇: %d → %d 条", len(events), len(out))
	}
	return out
}

// applyEventDecay 板块事件衰减：同板块同方向事件在 H 小时内重复出现时，
// Score 乘以 0.5^(H/4)（1h→0.84, 2h→0.71, 4h→0.50, 8h→0.25），弱化重复消息。
func (e *Engine) applyEventDecay(events []newsagent.NewsEvent) {
	now := time.Now()
	for i := range events {
		ev := &events[i]
		if ev.Level == "个股" || len(ev.Sectors) == 0 {
			continue
		}
		key := strings.Join(ev.Sectors, "+") + "|" + ev.Direction
		if last, ok := e.sectorEventTimes[key]; ok {
			hours := now.Sub(last).Hours()
			if hours > 0 && hours < 24 {
				ev.Score *= math.Pow(0.5, hours/4)
				log.Printf("[engine] 事件衰减 %s(%s): 距上次%.1fh, score→%.2f", key, ev.Title, hours, ev.Score)
			}
		}
		e.sectorEventTimes[key] = now
	}
}

// containsStr 判断字符串切片是否包含目标。
func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// mergeStr 合并两个字符串切片（去重，保留顺序）。
func mergeStr(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(a, b...) {
		if s == "" || containsStr(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}
