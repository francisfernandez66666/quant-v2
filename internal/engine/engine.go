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
	"regexp"
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
// 它是唯一被允许跨模块调用的对象：把新闻流水线、板块验证、战法扫描与信号聚合串联成一条完整链路。
type Engine struct {
	mu sync.RWMutex // 保护全部可变字段的读写锁（多 goroutine：主循环 + 近实时打分循环 + SSE/HTTP 调用）

	marketAPI    *data.MarketAPI         // 行情 API（实时价/K线/资金流/涨停池）
	newsAgent    *newsagent.Agent        // 新闻代理（拉取 + Stage0/1/2 归因分析）
	strategy     *strategy_engine.Engine // 策略引擎（事件归因 → 评分池 → 行情数据）
	sectorAgent  *sector_agent.Agent     // 板块验证代理（战法扫描前做板块真伪验证）
	combatAgent  *combat_agent.Agent     // 战法代理（8a/8b 打分 + 多战法信号扫描）
	agg          *display.Aggregator     // 看板聚合器（SSE 数据源）
	rpt          *report.Report          // 持仓/交易报表（止盈止损提醒依赖）
	stockTracker *data.StockTracker      // 个股跟踪池（8a/8b 入池与失效管理）
	wlMgr        *data.WatchlistManager  // 用户自选股管理
	sse          *server.SSEBroker       // SSE 广播器（推送打分/信号到前端）
	llmClient    *llm.Client             // LLM 客户端（D1 评分 / 标题党校正）
	ths          *data.THSClient         // 同花顺客户端（板块名单/行情表/实时报价降级）
	scanner      *data.SectorScanner     // 板块扫描器（板块名单索引，板块验真与归因校验依赖）

	longEnabled  bool // 利好开关（做多分支）
	shortEnabled bool // 利空开关（做空分支）

	debugInfo    *newsagent.DebugInfo  // 最近一轮流水线的调试数据（/api/debug 展示）
	stageRecords []newsagent.DebugInfo // 当日全量轮次记录（固化到磁盘）
	stageRecPath string                // Stage 记录持久化文件路径

	signalRecords []combat_agent.SignalLog // 当日全量信号批次记录（固化到磁盘）
	signalRecPath string                   // 信号批次记录持久化文件路径

	msgStore       *data.MessageStore        // 消息中心持久化存储
	consultStore   *data.ConsultStore        // 股票咨询对话持久化存储（跨交易日清空）
	confrontStore  *data.ConfrontationStore  // 政策反制事件持久化存储（跨交易日清空）
	hotRecords []data.HotRecord   // 当日热点板块轮次记录（固化到磁盘）
	hotRecPath string             // 热点板块记录持久化文件路径

	sectorEventTimes map[string]time.Time  // 板块事件时间戳（重复事件衰减状态）
	emotionCfg       *config.EmotionConfig // 情绪周期阈值（SSE 广播情绪阶段）

	fetcher          *data.Fetcher                   // 5s 实时行情采集器（近实时打分快照来源）
	scoreStore       *scoreStore                     // 8a/8b 打分持久化（scores.json）
	prevPass         map[string]map[string]bool      // 近实时信号状态翻转去重（code → strategy → 上次是否Pass）
	lastD1Scores     map[string]combat_agent.D1Score // 主循环最近一轮 D1 评分（近实时循环复用，不每 5s 调 LLM）
	lastEmotionPhase string                          // 主循环最近一轮情绪阶段（近实时循环复用）
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

// signalRecordFile 信号批次记录磁盘持久化结构（按交易日分桶）。
type signalRecordFile struct {
	TradingDay string                   `json:"trading_day"`
	Records    []combat_agent.SignalLog `json:"records"`
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
	// 根据 dataDir 计算各持久化文件路径（dataDir 为空时不落盘，纯内存模式）
	stageRecPath := ""
	msgPath := ""
	scoreRecPath := ""
	if dataDir != "" {
		stageRecPath = filepath.Join(dataDir, "stage_records.json")
		msgPath = filepath.Join(dataDir, "messages.json")
		scoreRecPath = filepath.Join(dataDir, "scores.json")
	}
	consultPath := ""
	if dataDir != "" {
		consultPath = filepath.Join(dataDir, "consult_history.json")
	}
	confrontPath := ""
	if dataDir != "" {
		confrontPath = filepath.Join(dataDir, "confrontation.json")
	}
	hotRecPath := ""
	signalRecPath := ""
	if dataDir != "" {
		hotRecPath = filepath.Join(dataDir, "hot_records.json")
		signalRecPath = filepath.Join(dataDir, "signal_records.json")
	}
	e := &Engine{
		marketAPI:        marketAPI,
		newsAgent:        newsAgent,
		strategy:         strategy,
		sectorAgent:      sectorAgent,
		combatAgent:      combatAgent,
		agg:              agg,
		rpt:              rpt,
		stockTracker:     stockTracker,
		wlMgr:            wlMgr,
		sse:              sse,
		llmClient:        llmClient,
		ths:              ths,
		longEnabled:      true,
		shortEnabled:     false,
		stageRecords:     loadStageRecords(stageRecPath),
		stageRecPath:     stageRecPath,
		signalRecords:    loadSignalRecords(signalRecPath),
		signalRecPath:    signalRecPath,
		msgStore:         data.NewMessageStore(msgPath),
		consultStore:     data.NewConsultStore(consultPath),
		confrontStore:    data.NewConfrontationStore(confrontPath),
		hotRecords:       loadHotRecords(hotRecPath),
		hotRecPath:       hotRecPath,
		sectorEventTimes: make(map[string]time.Time),
		scoreStore:       newScoreStore(scoreRecPath),
		prevPass:         make(map[string]map[string]bool),
		lastD1Scores:     make(map[string]combat_agent.D1Score),
	}
	e.syncMessages(nil, nil) // 首次同步：把历史持仓/止盈止损提示并入消息中心
	// 启动时回填上次持久化的 8a/8b 打分（重启后前端立即可见）
	if loaded := e.scoreStore.Load(); len(loaded) > 0 {
		e.agg.UpdateFast(loaded, nil, e.rpt)
	}
	return e
}

// SetFetcher 设置 5s 实时行情采集器（近实时打分循环的快照来源）。
func (e *Engine) SetFetcher(f *data.Fetcher) {
	e.mu.Lock()
	e.fetcher = f
	e.mu.Unlock()
}

// updateHotPool 将验证通过的板块成分股并入 5s 实时监控池。
// 热点股随板块轮换替换（上限 60 由 Fetcher 内部裁剪），缺失板块验证结果时保留原热点。
func (e *Engine) updateHotPool(bull, bear []sector_agent.VerifiedSector) {
	e.mu.RLock()
	f := e.fetcher
	e.mu.RUnlock()
	if f == nil {
		return
	}
	set := make(map[string]bool)
	for _, sec := range bull {
		for _, code := range sec.Stocks {
			set[code] = true
		}
	}
	for _, sec := range bear {
		for _, code := range sec.Stocks {
			set[code] = true
		}
	}
	if len(set) == 0 {
		return // 本轮无验证通过的板块，保持原热点不变
	}
	stocks := make([]string, 0, len(set))
	for code := range set {
		stocks = append(stocks, code)
	}
	f.UpdateHotStocks(stocks)
	log.Printf("[engine] 热点池更新: %d 只板块成分股入 5s 实时池", len(stocks))
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

// loadSignalRecords 从磁盘加载当日信号批次记录；跨交易日自动重置。
func loadSignalRecords(path string) []combat_agent.SignalLog {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f signalRecordFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("[engine] signal_records 解析失败: %v", err)
		return nil
	}
	if f.TradingDay != data.TradingDayDate(time.Now()) {
		return nil
	}
	return f.Records
}

// persistSignalRecords 将当日信号批次记录写入磁盘。
func (e *Engine) persistSignalRecords() {
	if e.signalRecPath == "" {
		return
	}
	e.mu.RLock()
	f := signalRecordFile{TradingDay: data.TradingDayDate(time.Now()), Records: e.signalRecords}
	e.mu.RUnlock()
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		log.Printf("[engine] signal_records 序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(e.signalRecPath, raw, 0644); err != nil {
		log.Printf("[engine] signal_records 写入失败: %v", err)
	}
}

// GetSignalLogs 返回当日全量信号批次记录（供前端"信号日志"弹窗按批次展示）。
func (e *Engine) GetSignalLogs() []combat_agent.SignalLog {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]combat_agent.SignalLog, len(e.signalRecords))
	copy(out, e.signalRecords)
	return out
}

// captureSignalRecords 收集本轮全部信号为一条批次快照，固化到当日信号记录。
func (e *Engine) captureSignalRecords(rawCount int, signals []combat_agent.Signal) {
	e.mu.Lock()
	rec := combat_agent.SignalLog{
		ProcessTime: time.Now(),
		RawCount:    rawCount,
		Signals:     make([]combat_agent.Signal, len(signals)),
	}
	copy(rec.Signals, signals)
	e.signalRecords = append(e.signalRecords, rec)
	if len(e.signalRecords) > 20 {
		e.signalRecords = e.signalRecords[len(e.signalRecords)-20:]
	}
	e.mu.Unlock()
	e.persistSignalRecords()
}

// GetAllNewsEvents 返回持久化到本地的全部已打标新闻事件，供 /api/news?all=true 展示。
func (e *Engine) GetAllNewsEvents() []newsagent.NewsEvent {
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	if na == nil {
		return nil
	}
	return na.AllEvents()
}

// SetNewsShowAll 设置"资讯显示全部"开关：开启时落盘过滤分降到 0，
// 弱档/中性事件也出现在 /api/news；关闭时恢复默认 0.25。
func (e *Engine) SetNewsShowAll(v bool) {
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	if na == nil {
		return
	}
	if v {
		na.SetMinScore(0)
	} else {
		na.SetMinScore(0.25)
	}
	log.Printf("[engine] 资讯显示全部开关: %v (落盘最低分=%v)", v, na.MinScore())
}

// NewsShowAll 返回"资讯显示全部"开关当前状态。
func (e *Engine) NewsShowAll() bool {
	e.mu.RLock()
	na := e.newsAgent
	e.mu.RUnlock()
	if na == nil {
		return false
	}
	return na.MinScore() == 0
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

// ── 股票咨询（多轮对话）──

// ConsultLLM 以多轮对话方式调用 LLM 生成咨询回复（股票咨询页使用）。
// 将用户提问与历史对话一并发给模型；LLM 未配置时返回错误提示前端引导配置。
// 回复生成后同步追加到当日对话历史（跨交易日自动清空）。
func (e *Engine) ConsultLLM(userID, userMsg string, proMode bool) (string, error) {
	e.mu.RLock()
	client := e.llmClient
	e.mu.RUnlock()
	if client == nil {
		return "", fmt.Errorf("未配置 LLM_API_KEY，请先在股票咨询页配置 API Key")
	}

	// 组装多轮对话历史（此前全部 user/assistant 消息 + 本轮提问）
	messages := make([]llm.Message, 0, 8)
	if e.consultStore != nil {
		for _, m := range e.consultStore.List() {
			messages = append(messages, llm.Message{Role: m.Role, Content: m.Content})
		}
	}

	// 专业模式：解析用户提到的股票，注入真实实时行情上下文，禁止 LLM 编造数字。
	if proMode {
		if ctx := e.buildConsultContext(userMsg); ctx != "" {
			messages = append(messages, llm.Message{Role: "system", Content: ctx})
		} else {
			messages = append(messages, llm.Message{Role: "system", Content: consultNoStockPrompt})
		}
	}

	messages = append(messages, llm.Message{Role: "user", Content: userMsg})

	reply, err := client.ChatMessages(messages)
	if err != nil {
		return "", fmt.Errorf("咨询调用失败: %v", err)
	}

	// 对话历史落盘：用户提问 + 模型回复
	if e.consultStore != nil {
		e.consultStore.Append("user", userMsg)
		e.consultStore.Append("assistant", reply)
	}
	return reply, nil
}

// consultCodeRe 从文本中提取 6 位股票代码。
var consultCodeRe = regexp.MustCompile(`\b\d{6}\b`)

// consultNoStockPrompt 专业模式下未能从消息中解析出股票时注入的提示词。
const consultNoStockPrompt = `当前消息中未识别到明确的股票名称或 6 位代码。请向用户说明：专业模式需要您指明具体股票（如：卧龙电驱 600580），我才能拉取其实时行情（现价/涨跌幅/主力净流入/大单明细/均线/MACD/策略信号）做分析。在未指定股票前，不要编造任何个股的具体行情数字。`

// buildConsultContext 从用户消息解析提到的股票，拉取真实实时行情组装为上下文文本。
// 返回空串表示未解析出任何股票（调用方应提示用户指明股票）。
// 数据来源：东财 push2 实时价（含主力净流入 F162）+ 东财资金流明细 + 新浪日K/分钟K + 引擎战法信号。
func (e *Engine) buildConsultContext(userMsg string) string {
	codes := make(map[string]string) // code → name

	// 1. 名称 → 代码：解析文本中出现的股票名称，再清洗为代码
	var names []string
	if e.newsAgent != nil {
		names = e.newsAgent.FindStocksInText(userMsg)
		for _, c := range e.newsAgent.CleanStocks(names) {
			parts := strings.SplitN(c, "|", 2)
			if len(parts) != 2 || parts[0] == "" {
				continue
			}
			codes[parts[1]] = parts[0]
		}
	}
	// 2. 文本中的纯 6 位代码
	for _, m := range consultCodeRe.FindAllString(userMsg, -1) {
		if _, ok := codes[m]; !ok {
			codes[m] = ""
		}
	}

	if len(codes) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("以下是用户可能关心的股票今日实时行情实测数据（数据获取时间 " +
		time.Now().Format("2006-01-02 15:04:05") + "）：\n")
	sb.WriteString("【要求】仅可引用下列提供的数据；未提供的信息（如大盘资金、期指贴水、撤单、盘口等）如实说明" +
		"无法获取，严禁编造净流入/成交量/涨跌/触发等任何具体数字；净流入口径=主力(超大单+大单)，东方财富。\n")

	for code, name := range codes {
		sb.WriteString(e.buildStockBlock(code, name))
	}
	return sb.String()
}

// buildStockBlock 组装单只股票的实时行情数据块。
func (e *Engine) buildStockBlock(code, name string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n—— 股票 %s", code))
	if name != "" {
		b.WriteString(" " + name)
	}
	b.WriteString(" ——\n")

	// 实时报价（东财 push2，含主力净流入）
	if e.marketAPI == nil {
		b.WriteString("实时行情数据源未初始化。\n")
		return b.String()
	}
	si, err := e.marketAPI.GetRealtimeQuote(code)
	if err == nil && si != nil && si.Price > 0 {
		if si.Name != "" {
			name = si.Name
		}
		b.WriteString(fmt.Sprintf("现价 %.2f元 涨跌幅%.2f%% 今开%.2f 最高%.2f 最低%.2f 昨收%.2f\n",
			si.Price, si.ChangePct, si.Open, si.High, si.Low, si.Close))
		b.WriteString(fmt.Sprintf("成交量 %.0f股 成交额%.0f元 换手率 %.2f%%\n",
			si.Volume, si.Amount, si.Turnover))
		b.WriteString(fmt.Sprintf("主力净流入 %.2f万元\n", si.NetInflow/1e4))
	} else {
		b.WriteString("实时行情获取失败。\n")
	}

	// 资金流明细（超大/大/中/小单，均以万元计）
	if cf, err := e.marketAPI.GetStockMoneyFlow(code); err == nil && cf != nil {
		b.WriteString(fmt.Sprintf("资金明细: 超大单净流入%.0f万 大单净流入%.0f万 中单净流入%.0f万 小单净流入%.0f万\n",
			(cf.SuperLargeIn-cf.SuperLargeOut)/1e4, (cf.LargeIn-cf.LargeOut)/1e4,
			(cf.MediumIn-cf.MediumOut)/1e4, (cf.SmallIn-cf.SmallOut)/1e4))
	}

	// 日K：当日振幅、MA5/MA10、近5日量能
	if kl, err := e.marketAPI.GetSinaKLine(code, 30); err == nil && len(kl) > 0 {
		last := kl[len(kl)-1]
		amp := 0.0
		if last.Close > 0 {
			amp = (last.High - last.Low) / last.Close * 100
		}
		b.WriteString(fmt.Sprintf("日K(最新一根 %s): 振幅%.2f%% 收%.2f 高%.2f 低%.2f\n",
			last.Date.Format("2006-01-02"), amp, last.Close, last.High, last.Low))
		if len(kl) >= 10 {
			b.WriteString(fmt.Sprintf("MA5=%.2f MA10=%.2f %s\n",
				consultMA(kl[len(kl)-5:]), consultMA(kl[len(kl)-10:]), consultMATrend(kl)))
		}
		// 近5日量能
		avg5 := consultMAVolume(kl)
		b.WriteString(fmt.Sprintf("近5日平均成交量 %.0f股，最新一根量 %.0f股\n", avg5, last.Volume))
	}

	// 分钟K（5分钟）MACD 状态
	if minKL, err := e.marketAPI.GetSinaMinuteKLine(code, 5, 48); err == nil && len(minKL) >= 2 {
		macd := data.CalcMACD(minKL)
		status := "空头"
		if macd.Bar > 0 {
			status = "多头(红柱)"
		} else if macd.Bar == 0 {
			status = "零轴"
		}
		b.WriteString(fmt.Sprintf("5分钟MACD: DIF=%.4f DEA=%.4f BAR=%.4f(%s)\n",
			macd.DIF, macd.DEA, macd.Bar, status))
	}

	// 引擎战法信号（该股是否已触发某战法）
	if e.agg != nil {
		if dash := e.agg.Current(); dash != nil {
			for _, sig := range dash.FinalSignals {
				if sig.Code == code {
					b.WriteString(fmt.Sprintf("策略信号: [%s] %s %s %s 触发价%.2f 理由:%s\n",
						sig.Strategy, sig.Direction, sig.Action, sig.Name, sig.Price, sig.Reason))
				}
			}
		}
	}

	return b.String()
}

// consultMA 计算一段收盘价的简单平均。
func consultMA(kl []data.KLine) float64 {
	if len(kl) == 0 {
		return 0
	}
	var sum float64
	for _, k := range kl {
		sum += k.Close
	}
	return sum / float64(len(kl))
}

// consultMAVolume 计算最近5根日K的平均成交量（不足则取全部）。
func consultMAVolume(kl []data.KLine) float64 {
	if len(kl) == 0 {
		return 0
	}
	n := 5
	if len(kl) < n {
		n = len(kl)
	}
	var sum float64
	for _, k := range kl[len(kl)-n:] {
		sum += k.Volume
	}
	return sum / float64(n)
}

// consultMATrend 判断 MA5 相对 MA10 的多头/空头排列。
func consultMATrend(kl []data.KLine) string {
	if len(kl) < 10 {
		return ""
	}
	ma5 := consultMA(kl[len(kl)-5:])
	ma10 := consultMA(kl[len(kl)-10:])
	if ma5 > ma10 {
		return "均线多头排列"
	}
	return "均线空头排列"
}

// GetConsultHistory 返回当日咨询对话历史。
func (e *Engine) GetConsultHistory() []data.ConsultMessage {
	if e.consultStore == nil {
		return nil
	}
	return e.consultStore.List()
}

// ClearConsultHistory 清空当日咨询对话历史。
func (e *Engine) ClearConsultHistory() {
	if e.consultStore != nil {
		e.consultStore.Clear()
	}
}

// buildPolicyRetaliationSignals 将政策反制事件转为可展示信号：
//  1. 事件去重后持久化到 confrontationStore（仅当日首次出现）；
//  2. 生成消息中心"政策反制"提示（利空方向，提醒关注受影响板块）；
//  3. 返回合成后的 NewsEvent（Source="政策反制"，供事件流/资讯展示）。
func (e *Engine) buildPolicyRetaliationSignals(events []data.ConfrontationEvent) []newsagent.NewsEvent {
	if len(events) == 0 {
		return nil
	}
	var out []newsagent.NewsEvent
	for _, ev := range events {
		if e.confrontStore != nil {
			if e.confrontStore.HasTitle(ev.Title) {
				continue // 当日已处理过，跳过避免重复提醒
			}
			e.confrontStore.Append(ev)
		}
		// 方向转带符号分数：利空 -0.75 / 利好 +0.75（高强度涉外政策事件）
		score := 0.75
		if ev.Direction == "利空" {
			score = -0.75
		}
		newsEv := newsagent.NewsEvent{
			Title:       ev.Title,
			Content:     ev.Content,
			Datetime:    ev.Datetime,
			Source:      "政策反制",
			Level:       "宏观",
			Direction:   ev.Direction,
			Score:       score,
			Sectors:     ev.Sectors,
			ImpactLevel: ev.Impact,
			EventType:   "政策",
			Urgency:     "紧急",
			Reason:      "涉外政策反制事件，直接影响相关板块",
		}
		out = append(out, newsEv)

		// 消息中心提示：提醒关注受影响板块（利空/利好方向由事件决定）
		if e.msgStore != nil {
			e.msgStore.Sync([]data.MessageItem{{
				ID:          "confront@" + ev.Title,
				Code:        "",
				Name:        "",
				Level:       "政策反制",
				Action:      "提示",
				Strategy:    "政策反制",
				Time:        nowTimeString(),
				Title:       "政策反制事件",
				Body:        ev.Title + "（影响板块：" + strings.Join(ev.Sectors, "、") + "）",
				Direction:   ev.Direction,
				GeneratedAt: time.Now(),
			}})
		}
	}
	return out
}

// nowTimeString 返回当前时间的 HH:MM:SS 字符串，用于消息中心提示的时间戳。
func nowTimeString() string {
	return time.Now().Format("15:04:05")
}

// syncMessages 将本轮止盈止损告警与持仓提示合并进消息存储（按稳定键去重）。
// bearSectors/bearStocks 为本轮利空板块与利空个股，用于扫出"命中利空板块的持仓"并提醒卖出。
func (e *Engine) syncMessages(alertSignals []combat_agent.Signal, sr *strategy_engine.StrategyResult) {
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

	// 利空板块持仓提醒：扫描当前持仓,凡命中本轮利空板块领跌股/利空个股的,提醒"卖出"。
	// 仅在存在利空依据时触发；不出做空战法信号、不自动平仓。
	if sr != nil {
		bearCodes := bearHitCodes(sr)
		held := e.rpt.HeldPositions()
		now := time.Now()
		for _, pos := range held {
			if bearCodes[pos.Code] {
				reason := fmt.Sprintf("持仓 %s(%s) 命中利空板块,建议考虑减仓/卖出", pos.Name, pos.Code)
				items = append(items, data.MessageItem{
					ID:          "bearhold@" + pos.Code,
					Code:        pos.Code,
					Name:        pos.Name,
					Level:       "利空提示",
					Action:      "卖出",
					Strategy:    pos.Strategy,
					Time:        now.Format("15:04:05"),
					Title:       fmt.Sprintf("利空提示 %s", pos.Code),
					Body:        reason,
					Direction:   "利空",
					GeneratedAt: now,
				})
			}
		}
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

// SetLongEnabled 设置做多开关状态（线程安全，前端控制面板调用）。
func (e *Engine) SetLongEnabled(v bool) {
	e.mu.Lock()
	e.longEnabled = v
	e.mu.Unlock()
}

// LongEnabled 返回做多开关是否开启。
func (e *Engine) LongEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.longEnabled
}

// SetShortEnabled 设置做空开关状态。
func (e *Engine) SetShortEnabled(v bool) {
	e.mu.Lock()
	e.shortEnabled = v
	e.mu.Unlock()
}

// ShortEnabled 返回做空开关是否开启。
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
	var st0 newsagent.Stage0Result
	var events []newsagent.NewsEvent
	var valid []newsagent.NewsEvent

	if len(rawNews) == 0 {
		log.Printf("[engine] 无新新闻 (since=%s), 本轮仅执行打分", since.Format("01-02 15:04"))
	} else {
		// 2. Stage0 归因分类：个股 / 板块 / 一般（合并垃圾过滤+价值初筛+标题党复核）
		st0 = e.newsAgent.Stage0(rawNews)
		log.Printf("[engine][news漏斗] 原始=%d 个股=%d 板块=%d IPO=%d 一般=%d (板块material保留=%d)",
			len(rawNews), len(st0.StockIdx), len(st0.SectorIdx), len(st0.IpoIdx), len(st0.GeneralIdx), len(st0.Material))

		// 2b. 标题党修复：LLM 校正标题应用到原文（供 Stage2 分析、事件与展示使用）
		for i, t := range st0.CorrectedTitle {
			if i >= 0 && i < len(rawNews) && t != "" {
				rawNews[i].Title = t
			}
		}

		// 3. 收集全量事件
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

		// 4b. 政策反制事件：从涉外政策新闻关键词识别（直构，不走 LLM），并入事件流
		retEvents := e.buildPolicyRetaliationSignals(e.newsAgent.DeriveRetaliation(rawNews))
		events = append(events, retEvents...)

		// 5. 持久化全量事件供 /api/news 展示（含中性/一般新闻）
		e.newsAgent.SaveEvents(events)
		log.Printf("[engine][news漏斗] 事件共=%d (>=0.25落盘), 个股+板块+IPO来源", len(events))

		// 6. 阈值过滤：仅 |score| ≥ 0.50 进引擎（弱/中性丢弃）
		valid = filterThreshold(events, 0.50)
		log.Printf("[engine][news漏斗] 阈值过滤0.5 -> 有效=%d", len(valid))
		if len(valid) > 0 {
			// 6a. 事件聚簇：同板块/同方向的重复新闻合并为单条（去重避免刷屏）
			valid = clusterEvents(valid)

			// 6b. 事件衰减：同板块同方向事件在窗口内重复出现时按 0.5^(h/4) 降权
			e.applyEventDecay(valid)

			// 6c. 衰减后再次阈值过滤（重复事件降权后可掉出 0.5 线）
			valid = filterThreshold(valid, 0.50)
			log.Printf("[engine][news漏斗] 聚簇+衰减后再滤 -> 有效=%d", len(valid))
			if len(valid) > 0 {
				// 6b. 板块验真回填：剔除 LLM 幻觉板块名（命中真实板块名单才保留）
				e.verifySectorAttribution(valid)

				// 6c. 板块→个股事件级传播：板块 top 成分股注入 RelatedStocks 进个股监测池
				e.propagateSectorToStocks(valid)
			}
		}
		if len(valid) == 0 {
			log.Printf("[engine] 无有效事件(|score|>=0.5), 本轮仅执行打分")
		}
	}

	// 7. 策略评估：归因 + 分流 + 评分池 + 行情数据（无事件时仅覆盖 持仓+自选 打分池）
	positions := e.rpt.HeldPositionCodes()
	watchlist := e.wlMgr.List()
	sr := e.strategy.Evaluate(ctx, valid, positions, watchlist)

	// 7b. 固化本轮热点板块记录（同花顺 top-20 匹配后，供前端展示历史）
	e.captureHotRecord(sr)

	// 8. D1 评分（所有打分池个股；LLM 失败/漏项回退上一轮评分，避免断链归零）
	d1Scorer := combat_agent.NewD1Scorer(e.llmClient, "")
	e.mu.RLock()
	prevD1 := e.lastD1Scores
	e.mu.RUnlock()
	d1Scores := d1Scorer.BatchScore(sr.ScoringPool, valid, sr.MarketData, prevD1)
	e.mu.Lock()
	e.lastD1Scores = d1Scores
	e.mu.Unlock()

	// 8a. 打分池 PE 预取（N 形 D3 超跌评分；东财 clist f9，TTL 缓存降低限流压力）
	peScores := make(map[string]float64, len(sr.ScoringPool))
	for _, code := range sr.ScoringPool {
		peScores[code] = e.marketAPI.GetStockPE(code)
	}

	td := data.TradingDayDate(time.Now())

	// 8b. 当日涨停池 + 事件新闻简报（龙头识别 / 涨停分类 / 预期差检测）
	pool, poolErr := e.marketAPI.GetLimitUpPool("")
	if poolErr != nil {
		log.Printf("[engine] 涨停池拉取失败: %v", poolErr)
	}
	newsBriefs := newsBriefsByCode(valid)

	// 8c. 情绪阶段（供 N 形评分硬闸）+ 8a/8b 持续打分输出容器
	emotionPhase := ""
	if e.emotionCfg != nil {
		emotionPhase = data.DetectEmotionPhaseV2(pool, 0, 0, e.emotionCfg)
	}
	e.mu.Lock()
	e.lastEmotionPhase = emotionPhase
	e.mu.Unlock()
	stockScores := make(map[string]combat_agent.StockScores)

	// 9. 板块验证（开关控制），结果同时用于战法扫描与看板展示
	var verifiedBull, verifiedBear []sector_agent.VerifiedSector
	if e.LongEnabled() {
		verifiedBull = e.sectorAgent.Verify(sr.HotSectors)
	}
	if e.ShortEnabled() {
		verifiedBear = e.sectorAgent.Verify(sr.BearSectors)
	}

	// 9b. 验证通过的板块成分股并入 5s 实时监控池（fetcher 新浪批量拉取，热点股随板块轮换）
	e.updateHotPool(verifiedBull, verifiedBear)

	// 10. 利好开关开 → 做多分支
	var bullSignals []combat_agent.Signal
	if e.LongEnabled() {
		bullInput := combat_agent.ScanInput{
			Sectors:      verifiedBull,
			L1Score:      sr.L1Score,
			L1Blocked:    sr.L1Blocked,
			MarketData:   sr.MarketData,
			D1Scores:     d1Scores,
			PE:           peScores,
			LimitUpPool:  pool,
			News:         newsBriefs,
			Scores:       stockScores,
			EmotionPhase: emotionPhase,
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
		Scores:           stockScores,
		EmotionPhase:     emotionPhase,
		PE:               peScores,
	})
	bullSignals = append(bullSignals, limitSignals...)

	// 11. 利空开关开 → 做空分支
	var bearSignals []combat_agent.Signal
	if e.ShortEnabled() {
		bearInput := combat_agent.ScanInput{
			Sectors:      verifiedBear,
			L1Score:      sr.L1Score,
			L1Blocked:    sr.L1Blocked,
			MarketData:   sr.MarketData,
			D1Scores:     d1Scores,
			PE:           peScores,
			LimitUpPool:  pool,
			News:         newsBriefs,
			Scores:       stockScores,
			EmotionPhase: emotionPhase,
		}
		bearSignals = e.combatAgent.ScanShort(bearInput)
	}

	// 12. 个股直入（跳过板块验证）：分做多/做空两组
	// 先收集本轮的个股事件候选（来自新闻事件的 stage2 个股），再与已跟踪个股/持仓/自选合并
	var newLong, newShort []string
	for _, st := range sr.LongStocks {
		newLong = append(newLong, st.Code)
	}
	for _, st := range sr.ShortStocks {
		newShort = append(newShort, st.Code)
	}

	// 取当日仍在跟踪期内的个股池（按方向区分做多/做空）
	trackedLong := e.stockTracker.GetActiveByDirection(td, "利好")
	trackedShort := e.stockTracker.GetActiveByDirection(td, "利空")

	// 8a/8b 个股监测池 = 新闻个股 + 已跟踪个股 + 持仓 + 自选（去重）
	longCodes := mergeCodes(trackedCodes(trackedLong), newLong, positions, watchlist)
	shortCodes := mergeCodes(trackedCodes(trackedShort), newShort, positions, watchlist)

	// 分别对做多/做空监测池执行战法扫描（D1 评分复用，避免重复调 LLM）
	var individualSignals []combat_agent.Signal
	if len(longCodes) > 0 && e.LongEnabled() {
		in := combat_agent.ScanInput{
			IndividualStocks: longCodes,
			L1Score:          sr.L1Score,
			L1Blocked:        sr.L1Blocked,
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
			PE:               peScores,
			Scores:           stockScores,
			EmotionPhase:     emotionPhase,
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
			PE:               peScores,
			Scores:           stockScores,
			EmotionPhase:     emotionPhase,
		}
		individualSignals = append(individualSignals, e.combatAgent.ScanShort(in)...)
	}

	// 将本轮的个股信号写入跟踪池：有效期至下一交易日（到期后自动移出监测池）
	expiry := data.AddTradingDays(td, 1)
	for _, sig := range individualSignals {
		// 按信号方向映射为跟踪池的 利好/利空 标记
		dir := "利好"
		if sig.Direction == "做空" {
			dir = "利空"
		}
		e.stockTracker.Add(sig.Code, sig.Name, dir, sig.Reason, td, expiry)
	}

	// 收拢本轮全部有信号的个股代码，通知跟踪池做当日轮次收尾（失效未命中的个股）
	allSigCodes := make([]string, len(individualSignals))
	for i, sig := range individualSignals {
		allSigCodes[i] = sig.Code
	}
	e.stockTracker.OnCycleDone(td, allSigCodes)

	// 个股信号并入做多信号流统一展示/广播
	bullSignals = append(bullSignals, individualSignals...)

	// 13. 持仓止盈/止损提醒
	alertSignals := e.combatAgent.CheckPositionAlerts(e.rpt, e.marketAPI)

	// 14. 聚合器更新看板
	e.agg.Update(sr, verifiedBull, verifiedBear, bullSignals, bearSignals, alertSignals, stockScores, e.rpt)

	// 14b. 信号产生日志：逐条输出本轮生成的做多/做空/提醒信号（带日期时间戳，便于排障）
	for _, sig := range bullSignals {
		log.Printf("[engine] 产生信号 %s %s(%s) 方向=%s 操作=%s 置信=%.2f | %s",
			sig.Strategy, sig.Code, sig.Name, sig.Direction, sig.Action, sig.Confidence, sig.Reason)
	}
	for _, sig := range bearSignals {
		log.Printf("[engine] 产生信号 %s %s(%s) 方向=%s 操作=%s 置信=%.2f | %s",
			sig.Strategy, sig.Code, sig.Name, sig.Direction, sig.Action, sig.Confidence, sig.Reason)
	}
	for _, sig := range alertSignals {
		log.Printf("[engine] 产生信号 %s %s(%s) 方向=%s 操作=%s 置信=%.2f | %s",
			sig.Strategy, sig.Code, sig.Name, sig.Direction, sig.Action, sig.Confidence, sig.Reason)
	}

	// 8a/8b 打分持久化（与近实时循环同口径，当日最新分）
	e.scoreStore.Save(td, stockScores)

	// 14b. 告警/持仓提示合并进消息中心（持久化）
	e.syncMessages(alertSignals, sr)

	// 15. 调试数据
	e.captureDebug(rawNews, st0, events)

	// 15b. 信号批次快照：收拢本轮全部信号（做多/做空/提醒）供"信号日志"弹窗按批次复盘
	allSignals := make([]combat_agent.Signal, 0, len(bullSignals)+len(bearSignals)+len(alertSignals))
	allSignals = append(allSignals, bullSignals...)
	allSignals = append(allSignals, bearSignals...)
	allSignals = append(allSignals, alertSignals...)
	e.captureSignalRecords(len(rawNews), allSignals)

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
			// 按当日涨幅排名线性映射 RPS 近似值（第一名≈100，最后一名≈0）
			rank = 100 * (1 - float64(i)/float64(len(rows)-1))
		}
		rps = append(rps, data.SectorRPS{
			Code:  r.code,
			Name:  r.name,
			RPS20: rank,
			RPS60: rank,
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
				continue // 同一板块每轮只取一次成分股，避免重复注入
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
					continue // 已注入过同名/同标签则跳过
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
			return -1 // 个股级事件不参与聚簇，各自独立
		}
		for _, sec := range ev.Sectors {
			if idx, ok := clusterOf[sec]; ok {
				return idx // 命中已有板块簇则归入该簇
			}
		}
		return -1 // 无共享板块，新建独立簇
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

// bearHitCodes 收拢本轮全部利空标的（利空板块领跌股 + 利空个股），返回 code→true 映射。
// 供持仓利空提醒使用：凡命中该集合的持仓提示卖出。
func bearHitCodes(sr *strategy_engine.StrategyResult) map[string]bool {
	out := make(map[string]bool)
	for _, bs := range sr.BearSectors {
		for _, code := range bs.LeadStocks {
			out[code] = true
		}
	}
	for _, code := range sr.BearStocks {
		out[code] = true
	}
	return out
}
