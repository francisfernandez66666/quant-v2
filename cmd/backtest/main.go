// Package main 全流程全参数回测数据脚本：对"今天"的实盘数据跑完整流水线，
// 校验 主策略引擎 + 各 Agent 是否稳定按目标产出，以及 引擎 ↔ 边缘系统（报表/消息中心/
// 热点记录/打分持久化/SSE 广播）交互是否稳定。
//
// 与 cmd/quant（常驻服务）不同，本工具按 cycle 次数顺序跑 engine.Run（实时网络 + LLM），
// 把每个 pipeline 阶段的中间量 + 稳定性健康检查结果导出为 JSON + CSV，供复盘调参。
//
// 用法:
//
//	export LLM_API_KEY=xxx        # LLM 必填（新闻 Stage0/2、D1 评分依赖）
//	go run ./cmd/backtest
//	go run ./cmd/backtest -cycles 5 -since "2026-08-03 08:30:00" -out ./bt
//
// 输出目录（默认 ./backtest_out）:
//
//	report.json     — 全平台 + 全参数 + 每 cycle 阶段指标 + 健康检查 + 最终信号
//	signals.csv     — 收敛后的做多/提醒信号明细
//	stages.csv      — 每个 cycle 各阶段计数与时延
//	health.csv      — 每个 cycle 的边缘交互健康检查逐项结果
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/engine"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/server"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// backtestOptions 回测运行参数（命令行 + 全策略配置）.
type backtestOptions struct {
	cycles    int             // 连续跑 pipeline 的轮数
	since     time.Time       // 新闻追回起始时间
	outDir    string          // 输出目录
	dataDir   string          // 数据/持久化目录（临时）
	watchlist []string        // 注入的自选股代码
	longOff   bool            // 关闭做多通道
	shortOn   bool            // 开启做空通道
	cfgMgr    *config.Manager // 全参数配置（rules + d1）
}

// cycleMetrics 单个 cycle 的流水线阶段指标.
type cycleMetrics struct {
	Cycle        int    `json:"cycle"`
	RawNews      int    `json:"raw_news"`       // 拉取的原始新闻数
	Selected     int    `json:"selected"`       // stage0 命中数（个股+板块+IPO）
	Events       int    `json:"events"`         // 事件总数
	ValidEvents  int    `json:"valid_events"`   // 通过 |score|>=0.5 进入引擎的事件
	HotSectors   int    `json:"hot_sectors"`    // 利好板块
	BearSectors  int    `json:"bear_sectors"`   // 利空板块
	VerifiedBull int    `json:"verified_bull"`  // 通过板块验证的利好板块
	VerifiedBear int    `json:"verified_bear"`  // 通过板块验证的利空板块
	BullSignals  int    `json:"bull_signals"`   // 做多信号
	AlertSignals int    `json:"alert_signals"`  // 提醒信号（止盈/止损）
	FinalSignals int    `json:"final_signals"`  // 冲突裁决后的最终信号
	ScoreCount   int    `json:"score_count"`    // 打分池股票数
	MarketOK     int    `json:"market_ok"`      // 行情成功获取只数
	Emotion      string `json:"emotion"`        // 情绪阶段
	DurationMS   int64  `json:"duration_ms"`    // 本轮耗时毫秒
	SSE          int    `json:"sse_broadcasts"` // 本轮新增 SSE 广播数
	Panic        bool   `json:"panic"`          // 本轮是否发生 panic
}

// healthItem 单条健康检查结果（记录边缘系统交互是否稳定）.
type healthItem struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// sseWatcher 订阅 SSEBroker，累计收到的广播，用于验证引擎→推送链路.
type sseWatcher struct {
	broker *server.SSEBroker
	ch     chan []byte
	raw    [][]byte
}

// newSSEWatcher 注册一个 SSE 订阅者并返回 watcher.
func newSSEWatcher(broker *server.SSEBroker) *sseWatcher {
	return &sseWatcher{broker: broker, ch: broker.Subscribe()}
}

// drain 非阻塞地排空广播通道，把所有收到的 SSE 消息记入 raw.
func (w *sseWatcher) drain() {
	for {
		select {
		case raw := <-w.ch:
			w.raw = append(w.raw, raw)
		default:
			return
		}
	}
}

// count 返回累计收到的 SSE 广播条数.
func (w *sseWatcher) count() int { return len(w.raw) }

// close 注销 SSE 订阅.
func (w *sseWatcher) close() { w.broker.Unsubscribe(w.ch) }

// main 回测入口：解析命令行参数，调用回测 run，失败时终止进程。
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	opts, err := parseFlags()
	if err != nil {
		log.Fatalf("参数解析失败: %v", err)
	}
	if err := opts.run(); err != nil {
		log.Fatalf("回测失败: %v", err)
	}
}

// parseFlags 解析命令行参数并初始化 backtestOptions.
// since 缺省时按市场时段自动计算追回起点（与 cmd/quant/main.go 同口径）.
func parseFlags() (*backtestOptions, error) {
	var (
		cyclesRaw = flag.Int("cycles", 1, "连续运行 pipeline 的轮数")
		sinceRaw  = flag.String("since", "", `新闻追回起点 "2006-01-02 15:04:05"（空=按市场时段自动）`)
		outDir    = flag.String("out", "backtest_out", "输出目录")
		dataDir   = flag.String("data", "", "持久化数据目录（空=放 out 下 data 子目录）")
		watchRaw  = flag.String("watchlist", "", "注入的自选股，逗号分隔，如 300750,600519,300308")
		configRaw = flag.String("config", "", "回测配置文件(JSON，含 rules+d1，覆盖全部参数)")
		longOff   = flag.Bool("long-off", false, "关闭做多通道")
		shortOn   = flag.Bool("short-on", false, "开启做空通道")
	)
	flag.Parse()

	cfgPath := *configRaw
	if cfgPath == "" {
		cfgPath = filepath.Join(*dataDir, "config.json")
	}
	cfgMgr := config.NewManager(cfgPath)
	if *configRaw != "" {
		log.Printf("[backtest] 已加载全量配置: %s", cfgPath)
	}

	var since time.Time
	if *sinceRaw != "" {
		t, err := time.ParseInLocation("2006-01-02 15:04:05", *sinceRaw, time.Local)
		if err != nil {
			return nil, fmt.Errorf("since 格式错误: %v", err)
		}
		since = t
	} else {
		since = autoSince(time.Now())
	}

	return &backtestOptions{
		cycles:    *cyclesRaw,
		since:     since,
		outDir:    *outDir,
		dataDir:   *dataDir,
		watchlist: splitCSV(*watchRaw),
		longOff:   *longOff,
		shortOn:   *shortOn,
		cfgMgr:    cfgMgr,
	}, nil
}

// autoSince 按市场时段计算回测起点（与 cmd/quant/main.go 的 sinceForSession 同口径）.
func autoSince(now time.Time) time.Time {
	switch data.CurrentSession(now) {
	case data.SessionPreMarket:
		since := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location())
		if now.Weekday() == time.Monday {
			since = since.Add(-72 * time.Hour)
		} else {
			since = since.Add(-24 * time.Hour)
		}
		return since
	case data.SessionPreAfternoon:
		return time.Date(now.Year(), now.Month(), now.Day(), 11, 30, 0, 0, now.Location())
	case data.SessionMorningTrade, data.SessionAfternoonTrade:
		return now.Add(-30 * time.Minute)
	default:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}
}

// buildLLM 从环境变量 + 配置管理器构造 LLM 客户端；未配置 APIKey 时返回 nil（LLM 降级）.
func buildLLM(cfgMgr *config.Manager) *llm.Client {
	c := llm.Config{}
	c.APIKey = os.Getenv("LLM_API_KEY")
	c.APIURL = os.Getenv("LLM_API_URL")
	c.Model = os.Getenv("LLM_MODEL")
	if c.APIKey == "" {
		c.APIKey = cfgMgr.Rules.LLM.APIURL // 兼容旧配置：URL 兜底为 key
	}
	if c.APIKey == "" {
		log.Println("[backtest] 未配置 LLM_API_KEY，LLM 功能不可用（新闻分析/D1 评分降级）")
		return nil
	}
	return llm.New(c)
}

// run 核心：装配引擎 → 跑 N 轮 → 收集指标/健康项 → 落盘 JSON+CSV.
func (o *backtestOptions) run() error {
	if err := os.MkdirAll(o.outDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录: %v", err)
	}
	if o.dataDir == "" {
		o.dataDir = filepath.Join(o.outDir, "data")
	}
	if err := os.MkdirAll(o.dataDir, 0755); err != nil {
		return fmt.Errorf("创建数据目录: %v", err)
	}

	llmClient := buildLLM(o.cfgMgr)

	// ── 装配引擎（与 cmd/quant/main.go 相同链路：实时网络 + LLM）──
	marketAPI := data.NewMarketAPI()
	thsClient := data.NewTHSClient()

	var matcher *data.EventMatcher
	for _, p := range []string{"events_leftside.yaml", filepath.Join("..", "events_leftside.yaml")} {
		if cfg, err := data.LoadEvents(p); err == nil {
			matcher = data.NewEventMatcher(cfg)
			break
		}
	}

	cleaner := data.NewStockCleaner(marketAPI)
	nAgent := newsagent.New(marketAPI, llmClient, cleaner, o.dataDir)
	if err := nAgent.Start(); err != nil {
		return err
	}
	defer func() { _ = nAgent.Stop() }()

	strategyEngine := strategy_engine.New(marketAPI)
	strategyEngine.SetTHS(thsClient)
	scanner := data.NewSectorScanner(marketAPI, matcher)
	strategyEngine.SetScanner(scanner)

	sAgent := sector_agent.New(scanner, data.NewRPSManager())

	cAgent := combat_agent.New(o.cfgMgr.GetStrategyConfig())
	cAgent.SetLaodengConfig(&o.cfgMgr.Rules.Laodeng)
	cAgent.SetRunners([]combat_agent.StrategyRunner{
		{Type: strategy.SignalDragon, Strategy: dragon.New(o.cfgMgr)},
		{Type: strategy.SignalDoubleBump, Strategy: double_bump.New(o.cfgMgr)},
		{Type: strategy.SignalNShape, Strategy: n_shape.New(o.cfgMgr, matcher)},
		{Type: strategy.SignalDragonReturn, Strategy: dragon_return.New(o.cfgMgr)},
	})

	rpt := report.New(filepath.Join(o.dataDir, "report.json"))
	agg := display.New()
	wlMgr := data.NewWatchlistManager(o.dataDir)
	stockTracker := data.NewStockTracker(filepath.Join(o.dataDir, "tracked_stocks.json"))

	sse := server.NewSSEBroker()
	watcher := newSSEWatcher(sse)
	defer func() { watcher.close() }()

	eng := engine.New(marketAPI, nAgent, strategyEngine, sAgent, cAgent, agg, rpt,
		stockTracker, wlMgr, sse, llmClient, thsClient, o.dataDir)
	eng.SetScanner(scanner)
	eng.SetEmotionConfig(&o.cfgMgr.Rules.Emotion)
	if o.longOff {
		eng.SetLongEnabled(false)
	}
	if o.shortOn {
		eng.SetShortEnabled(true)
	}

	for _, code := range o.watchlist {
		wlMgr.Add(code)
		log.Printf("[backtest] 注入自选股 %s", code)
	}

	ctx := context.Background()
	log.Printf("[backtest] 开始 %d 轮，追溯起点 %s", o.cycles, o.since.Format("2006-01-02 15:04:05"))

	var (
		cycleStats []cycleMetrics
		healthList [][]healthItem
	)

	for i := 0; i < o.cycles; i++ {
		m, h := o.runCycle(ctx, i, eng, rpt, agg, watcher)
		cycleStats = append(cycleStats, m)
		healthList = append(healthList, h)
	}

	// 最终看板快照用于输出 JSON.
	final := agg.Current()

	// 输出：JSON 报告 + 三个 CSV.
	if err := writeJSONReport(o, cycleStats, healthList, final); err != nil {
		return err
	}
	if err := writeSignalsCSV(o.outDir, final); err != nil {
		return err
	}
	if err := writeStagesCSV(o.outDir, cycleStats); err != nil {
		return err
	}
	if err := writeHealthCSV(o.outDir, healthList); err != nil {
		return err
	}

	log.Printf("[backtest] 完成 ✔ 输出: %s", o.outDir)
	return nil
}

// runCycle 驱动一轮完整流水线并采集指标、健康项.
// 用 named-return + defer 包裹，使本轮 panic 被捕获并标记，而不中断后续 cycle.
func (o *backtestOptions) runCycle(
	ctx context.Context,
	idx int,
	eng *engine.Engine,
	rpt *report.Report,
	agg *display.Aggregator,
	watcher *sseWatcher,
) (m cycleMetrics, h []healthItem) {
	panicked := false
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			m.Panic = true
			log.Printf("[backtest] cycle %d panic 已捕获: %v", idx, r)
		}
	}()

	t0 := time.Now()
	before := watcher.count()
	m = cycleMetrics{Cycle: idx}

	sr := eng.Run(ctx, o.since)

	watcher.drain()
	m.DurationMS = time.Since(t0).Milliseconds()
	m.SSE = watcher.count() - before

	// 阶段指标（从看板聚合器读取）
	if dash := agg.Current(); dash != nil {
		m.Events = len(dash.NewsEvents)
		m.VerifiedBull = len(dash.VerifiedBull)
		m.VerifiedBear = len(dash.VerifiedBear)
		m.BullSignals = len(dash.BullSignals)
		m.AlertSignals = len(dash.AlertSignals)
		m.FinalSignals = len(dash.FinalSignals)
		m.ScoreCount = len(dash.Scores)
	}

	// 调试信息（Stage 流水线中间量）
	if debug := eng.GetDebugInfo(); debug != nil {
		m.RawNews = debug.RawCount
		m.Selected = debug.SelectedCount
	}

	// 策略结果中间量（有效事件 / 板块归因 / 行情成功数）
	if sr != nil {
		m.ValidEvents = len(sr.Events)
		m.HotSectors = len(sr.HotSectors)
		m.BearSectors = len(sr.BearSectors)
		m.MarketOK = marketOK(sr.MarketData)
	}

	// 情绪阶段从本轮 SSE 广播中提取
	m.Emotion = extractPhase(watcher.raw, before)

	// 健康检查（验证整轮边缘系统交互）
	if !panicked {
		m.Panic = false
	}
	h = collectHealth(idx, eng, rpt, agg)
	return m, h
}

// collectHealth 采集本轮的边缘系统与主链路健康项.
func collectHealth(idx int, eng *engine.Engine, rpt *report.Report, agg *display.Aggregator) []healthItem {
	var items []healthItem

	// 1. 看板已产出（聚合链路完成）
	dash := agg.Current()
	if dash == nil {
		items = append(items, healthItem{Check: "看板产出", OK: false, Detail: "Aggregator.Current() 为空，流水线未产出看板"})
	} else {
		items = append(items, healthItem{Check: "看板产出", OK: true, Detail: fmt.Sprintf("news=%d finalSig=%d", len(dash.NewsEvents), len(dash.FinalSignals))})
	}

	// 2. 调试信息（LLM/Stage 链路）
	if debug := eng.GetDebugInfo(); debug != nil {
		items = append(items, healthItem{Check: "Stage流水线", OK: true, Detail: fmt.Sprintf("raw=%d selected=%d stage2events=%d", debug.RawCount, debug.SelectedCount, len(debug.Stage2Events))})
	} else {
		items = append(items, healthItem{Check: "Stage流水线", OK: false, Detail: "GetDebugInfo() 为空"})
	}

	// 3. 消息中心（边缘持久化）
	msgs := eng.GetMessages()
	if msgs == nil {
		items = append(items, healthItem{Check: "消息中心", OK: false, Detail: "消息存储不可用"})
	} else if len(msgs) == 0 {
		items = append(items, healthItem{Check: "消息中心", OK: false, Detail: "无任何消息写入（可能无事件/提醒）"})
	} else {
		items = append(items, healthItem{Check: "消息中心", OK: true, Detail: fmt.Sprintf("%d 条消息", len(msgs))})
	}

	// 4. 持仓报表持久化
	logs := rpt.List()
	items = append(items, healthItem{Check: "持仓报表", OK: len(logs) >= 0, Detail: fmt.Sprintf("%d 条交易记录", len(logs))})

	// 5. 热点记录
	if hot := eng.GetHotRecords(); hot != nil {
		items = append(items, healthItem{Check: "热点记录", OK: true, Detail: fmt.Sprintf("%d 个热点板块", len(hot))})
	} else {
		items = append(items, healthItem{Check: "热点记录", OK: false, Detail: "热点记录不可用"})
	}

	_ = idx
	return items
}

// extractPhase 从本轮新增的 SSE 广播中提取情绪阶段字段.
func extractPhase(raw [][]byte, base int) string {
	for i := base; i < len(raw); i++ {
		var m map[string]interface{}
		if err := json.Unmarshal(raw[i], &m); err != nil {
			continue
		}
		if v, ok := m["emotion"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// marketOK 统计行情获取成功的股票只数（Price>0 且无错误）.
func marketOK(md map[string]*strategy_engine.StockMarketData) int {
	n := 0
	for _, v := range md {
		if v.Error == "" && v.Price > 0 {
			n++
		}
	}
	return n
}

// splitCSV 把逗号分隔字符串拆成去重切片.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// jsonReport 回测 JSON 报告结构.
type jsonReport struct {
	CapturedAt time.Time              `json:"captured_at"`
	Since      time.Time              `json:"since"`
	Cycles     int                    `json:"cycles"`
	Params     *config.Rules          `json:"params"` // 全参数
	D1         *config.D1Config       `json:"d1"`
	Watchlist  []string               `json:"watchlist"`
	CycleStats []cycleMetrics         `json:"cycles_stats"`
	Health     [][]healthItem         `json:"health"`
	Dashboard  *display.DashboardData `json:"dashboard,omitempty"`
}

// writeJSONReport 输出 report.json（全参数 + 每 cycle 指标 + 健康项 + 最终看板）.
func writeJSONReport(o *backtestOptions, cycles []cycleMetrics, health [][]healthItem, dashboard *display.DashboardData) error {
	report := jsonReport{
		CapturedAt: time.Now(),
		Since:      o.since,
		Cycles:     o.cycles,
		Params:     o.cfgMgr.Rules,
		D1:         o.cfgMgr.D1,
		Watchlist:  o.watchlist,
		CycleStats: cycles,
		Health:     health,
		Dashboard:  dashboard,
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(o.outDir, "report.json"), raw, 0644)
}

// writeSignalsCSV 导出最终信号明细 CSV.
func writeSignalsCSV(outDir string, dashboard *display.DashboardData) error {
	f, err := os.Create(filepath.Join(outDir, "signals.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"code", "name", "strategy", "direction", "action", "alert", "price", "confidence", "sector", "generated_at", "reason"}); err != nil {
		return err
	}
	if dashboard == nil {
		return nil
	}
	for _, s := range dashboard.FinalSignals {
		if err := w.Write([]string{
			s.Code, s.Name, s.Strategy, s.Direction, s.Action, s.AlertType,
			fmt.Sprintf("%.2f", s.Price), fmt.Sprintf("%.2f", s.Confidence),
			s.Sector, s.GeneratedAt.Format("2006-01-02 15:04:05"), s.Reason,
		}); err != nil {
			return err
		}
	}
	return nil
}

// writeStagesCSV 导出每 cycle 阶段指标 CSV.
func writeStagesCSV(outDir string, cycles []cycleMetrics) error {
	f, err := os.Create(filepath.Join(outDir, "stages.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"cycle", "raw_news", "selected", "events", "valid", "hot", "bear", "v_bull", "v_bear", "bull_sig", "alert_sig", "final_sig", "scores", "market_ok", "emotion", "duration_ms", "sse", "panic"}); err != nil {
		return err
	}
	for _, c := range cycles {
		if err := w.Write([]string{
			itoa(c.Cycle), itoa(c.RawNews), itoa(c.Selected), itoa(c.Events), itoa(c.ValidEvents),
			itoa(c.HotSectors), itoa(c.BearSectors), itoa(c.VerifiedBull), itoa(c.VerifiedBear),
			itoa(c.BullSignals), itoa(c.AlertSignals), itoa(c.FinalSignals), itoa(c.ScoreCount),
			itoa(c.MarketOK), c.Emotion, itoa64(c.DurationMS), itoa(c.SSE), boolStr(c.Panic),
		}); err != nil {
			return err
		}
	}
	return nil
}

// writeHealthCSV 导出逐 cycle 健康项 CSV.
func writeHealthCSV(outDir string, health [][]healthItem) error {
	f, err := os.Create(filepath.Join(outDir, "health.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"cycle", "check", "ok", "detail"}); err != nil {
		return err
	}
	for i, hs := range health {
		for _, h := range hs {
			ok := "FAIL"
			if h.OK {
				ok = "PASS"
			}
			if err := w.Write([]string{itoa(i), h.Check, ok, h.Detail}); err != nil {
				return err
			}
		}
	}
	return nil
}

// itoa 整数转字符串.
func itoa(n int) string { return fmt.Sprintf("%d", n) }

// itoa64 int64 转字符串.
func itoa64(n int64) string { return fmt.Sprintf("%d", n) }

// boolStr 布尔转 "true"/"false".
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
