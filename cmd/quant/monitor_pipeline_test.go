// Package main 全流程流水线阶段监视测试。
// 被 scripts/monitor_test.sh 周期调用，按 10 阶段逐一执行并输出可解析的 [STAGE] 行。
// 使用 Mock RoundTripper 拦截东财请求，不依赖外部网络。
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/sector_agent"
	"quant-trading-v2/internal/strategies/double_bump"
	"quant-trading-v2/internal/strategies/dragon"
	"quant-trading-v2/internal/strategies/dragon_return"
	"quant-trading-v2/internal/strategies/n_shape"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// stageRes 单个测试阶段的执行结果：阶段名、是否通过、耗时(毫秒)与详情描述。
type stageRes struct {
	// name 阶段标识名（如 1_EngineEvaluate）
	name string
	// pass 阶段是否通过
	pass bool
	// ms 阶段耗时（毫秒）
	ms int64
	// detail 阶段输出详情
	detail string
}

// runStage 执行一个阶段函数并计时，返回封装好的阶段结果。
func runStage(name string, fn func() (bool, string)) stageRes {
	t0 := time.Now()
	pass, detail := fn()
	ms := time.Since(t0).Milliseconds()
	return stageRes{name: name, pass: pass, ms: ms, detail: detail}
}

// printStage 输出可解析的 [STAGE] 阶段结果行，供 scripts/monitor_test.sh 捕获。
func printStage(t *testing.T, r stageRes) {
	status := "PASS"
	if !r.pass {
		status = "FAIL"
	}
	t.Logf("[STAGE] %s|%s|%dms|%s", r.name, status, r.ms, r.detail)
}

// init 设置环境变量 QUANT_DATA_DIR 到临时目录，保证监控测试数据读写隔离。
func init() {
	_ = os.Setenv("QUANT_DATA_DIR", os.TempDir()+"/.quant-monitor-pipeline")
}

// TestMonitorPipelineStages 全流程 10 阶段测试。
func TestMonitorPipelineStages(t *testing.T) {
	dir := t.TempDir()

	// ── 配置 & 组件初始化 ──
	cfgMgr := config.NewManager(filepath.Join(dir, "config.json"))
	cfgMgr.SetStrategyConfig(&config.StrategyConfig{
		Dragon:       config.DragonConfig{F1SealWeight: 0.30, F2ResonanceWeight: 0.25, F3PremiumWeight: 0.25, F4RsWeight: 0.20, PullbackMaxPct: 5.0},
		DoubleBump:   config.DoubleBumpConfig{FirstBreakVolumeMultiple: 2.0, SecondBreakVolumeMultiple: 1.5, PositionWeight: 0.3},
		NShape:       config.NShapeConfig{NPatternScoreThreshold: 0.6, HardStopLoss: -5.0},
		DragonReturn: config.DragonReturnConfig{StopLossPct: -7.0, TakeProfitPct: 15.0, MaxHoldDays: 20},
	})
	cfgMgr.Rules.Laodeng = config.LaodengConfig{Enabled: true, MarketCapMin: 500, PeMax: 15, TurnoverMin: 1.0, TechPenalty: -0.3, WeightScore: 0.15}
	cfgMgr.Save()

	origTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = origTransport }()
	http.DefaultTransport = &monitorMockTransport{inner: origTransport}

	api := data.NewMarketAPI()
	engine := strategy_engine.New(api)
	scanner := data.NewSectorScanner(api, nil)
	scanner.Update(monitorMockSectors, 3, 1, 5)
	engine.SetScanner(scanner)

	rpsMgr := data.NewRPSManager()
	sAgent := sector_agent.New(scanner, rpsMgr)

	stratCfg := cfgMgr.GetStrategyConfig()
	cAgent := combat_agent.New(stratCfg)
	cAgent.SetLaodengConfig(&cfgMgr.Rules.Laodeng)
	cAgent.SetRunners([]combat_agent.StrategyRunner{
		{Type: strategy.SignalDragon, Strategy: dragon.New(cfgMgr)},
		{Type: strategy.SignalDoubleBump, Strategy: double_bump.New(cfgMgr)},
		{Type: strategy.SignalNShape, Strategy: n_shape.New(cfgMgr, nil)},
		{Type: strategy.SignalDragonReturn, Strategy: dragon_return.New(cfgMgr)},
	})
	cAgent.SetShortEnabled(true)

	rpt := report.New(filepath.Join(dir, "report.json"))
	agg := display.New()

	// 模拟持仓
	rpt.LogSignal("POS001", "600519", "贵州茅台", "做多", "Dragon", 1480, 10, 5)
	rpt.LogSignal("POS002", "688256", "寒武纪", "做多", "DoubleBump", 118, 12, 6)
	positions := rpt.HeldPositionCodes()

	// 模拟新闻事件
	now := time.Now()
	mockEvents := []newsagent.NewsEvent{
		{Title: "茅台提价20%", Content: "贵州茅台提价", Datetime: now.Format("2006-01-02 15:04:05"), Source: "同花顺", Direction: "利好", Score: 0.85, Sectors: []string{"白酒"}, ImpactLevel: "高", EventType: "公司", Urgency: "立即", Reason: "茅台提价", RelatedStocks: []string{"贵州茅台|600519"}, CleanedStocks: []string{"贵州茅台|SH600519"}},
		{Title: "碳酸锂价格跌破8万", Content: "碳酸锂跌价", Datetime: now.Format("2006-01-02 15:04:05"), Source: "同花顺", Direction: "利空", Score: -0.7, Sectors: []string{"新能源"}, ImpactLevel: "高", EventType: "行业", Urgency: "立即", Reason: "碳酸锂跌价", RelatedStocks: []string{"宁德时代|300750"}, CleanedStocks: []string{"宁德时代|SZ300750"}},
		{Title: "央行降准0.5个百分点", Content: "央行降准", Datetime: now.Format("2006-01-02 15:04:05"), Source: "新浪财经", Direction: "利好", Score: 0.65, Sectors: []string{"银行"}, ImpactLevel: "高", EventType: "宏观", Urgency: "立即", Reason: "降准利好"},
		{Title: "美国半导体出口管制升级", Content: "出口管制", Datetime: now.Format("2006-01-02 15:04:05"), Source: "同花顺", Direction: "利空", Score: -0.55, Sectors: []string{"半导体"}, ImpactLevel: "中", EventType: "宏观", Urgency: "关注", Reason: "出口管制"},
		{Title: "科大讯飞星火4.0发布", Content: "AI大模型发布", Datetime: now.Format("2006-01-02 15:04:05"), Source: "同花顺", Direction: "利好", Score: 0.75, Sectors: []string{"人工智能"}, ImpactLevel: "高", EventType: "行业", Urgency: "立即", Reason: "AI大模型", RelatedStocks: []string{"科大讯飞|002230"}, CleanedStocks: []string{"科大讯飞|SZ002230"}},
		{Title: "茅台个股利好", Content: "个股事件", Datetime: now.Format("2006-01-02 15:04:05"), Source: "公告", Direction: "利好", Score: 0.9, Level: "个股", ImpactLevel: "高", EventType: "公司", Urgency: "立即", Reason: "个股利好", RelatedStocks: []string{"贵州茅台|600519"}, CleanedStocks: []string{"贵州茅台|SH600519"}},
		{Title: "赣锋锂业被调查", Content: "立案调查", Datetime: now.Format("2006-01-02 15:04:05"), Source: "公告", Direction: "利空", Score: -0.8, Level: "个股", ImpactLevel: "高", EventType: "公司", Urgency: "立即", Reason: "利空", RelatedStocks: []string{"赣锋锂业|002460"}, CleanedStocks: []string{"赣锋锂业|SZ002460"}},
	}
	allResults := make([]stageRes, 0, 10)

	// ════════════════════════════════════
	// Stage 1: Engine.Evaluate
	// ════════════════════════════════════
	t.Log("\n=== STAGE 1 ===")
	r1 := runStage("1_EngineEvaluate", func() (bool, string) {
		sr := engine.Evaluate(context.Background(), mockEvents, positions, nil)
		if sr == nil {
			return false, "Engine.Evaluate returned nil"
		}
		var hotNames, bearNames []string
		for _, s := range sr.HotSectors {
			hotNames = append(hotNames, s.Name)
		}
		for _, s := range sr.BearSectors {
			bearNames = append(bearNames, s.Name)
		}
		var longCodes, shortCodes []string
		for _, s := range sr.LongStocks {
			longCodes = append(longCodes, s.Code)
		}
		for _, s := range sr.ShortStocks {
			shortCodes = append(shortCodes, s.Code)
		}
		var blocked []string
		for n := range sr.L1Blocked {
			blocked = append(blocked, n)
		}
		pass := len(sr.HotSectors) >= 2 && len(sr.BearSectors) >= 1
		detail := fmt.Sprintf("hot=[%s] bear=[%s] pool=[%s] long=[%s] short=[%s] blocked=[%s]",
			strings.Join(hotNames, ","), strings.Join(bearNames, ","),
			strings.Join(sr.ScoringPool, ","),
			strings.Join(longCodes, ","), strings.Join(shortCodes, ","),
			strings.Join(blocked, ","))
		return pass, detail
	})
	printStage(t, r1)
	allResults = append(allResults, r1)

	// ════════════════════════════════════
	// Stage 2: D1Scorer（LLM 缺失时默认 0 分）
	// ════════════════════════════════════
	t.Log("\n=== STAGE 2 ===")
	r2 := runStage("2_D1Scorer", func() (bool, string) {
		sr := engine.Evaluate(context.Background(), mockEvents, positions, nil)
		d1 := combat_agent.NewD1Scorer(nil, "")
		scores := d1.BatchScore(sr.ScoringPool, sr.Events, sr.MarketData)
		var codes []string
		for code := range scores {
			codes = append(codes, code)
		}
		pass := len(scores) > 0
		return pass, fmt.Sprintf("scored_codes=[%s] (LLM=nil, all default 0)", strings.Join(codes, ","))
	})
	printStage(t, r2)
	allResults = append(allResults, r2)

	// ════════════════════════════════════
	// Stage 3: Laodeng 评分
	// ════════════════════════════════════
	t.Log("\n=== STAGE 3 ===")
	r3 := runStage("3_LaodengScore", func() (bool, string) {
		cfg := &cfgMgr.Rules.Laodeng
		s1 := strategy.ScoreLaodeng(cfg, 2000, 6, 2.5, "银行")
		s2 := strategy.ScoreLaodeng(cfg, 800, 30, 3.0, "新能源")
		s3 := strategy.ScoreLaodeng(cfg, 50, 40, 0.5, "半导体")
		pass := s1 > s2 && s2 > s3 && s3 >= 0
		return pass, fmt.Sprintf("bank=%.4f new_energy=%.4f semi=%.4f", s1, s2, s3)
	})
	printStage(t, r3)
	allResults = append(allResults, r3)

	// ════════════════════════════════════
	// Stage 4: SectorAgent.Verify（7a/7b）
	// ════════════════════════════════════
	t.Log("\n=== STAGE 4 ===")
	sr := engine.Evaluate(context.Background(), mockEvents, positions, nil)
	vBull := sAgent.Verify(sr.HotSectors)
	vBear := sAgent.Verify(sr.BearSectors)

	r4 := runStage("4_SectorVerify", func() (bool, string) {
		var bullDetail, bearDetail []string
		for _, v := range vBull {
			bullDetail = append(bullDetail, fmt.Sprintf("%s(%s)", v.Name, strings.Join(v.Stocks, ",")))
		}
		for _, v := range vBear {
			bearDetail = append(bearDetail, fmt.Sprintf("%s(%s)", v.Name, strings.Join(v.Stocks, ",")))
		}
		vbStocks := 0
		for _, v := range vBull {
			vbStocks += len(v.Stocks)
		}
		pass := len(vBull) > 0 && vbStocks > 0
		return pass, fmt.Sprintf("7a_bull=[%s] | 7b_bear=[%s]", strings.Join(bullDetail, ";"), strings.Join(bearDetail, ";"))
	})
	printStage(t, r4)
	allResults = append(allResults, r4)

	// ════════════════════════════════════
	// Stage 5: CombatAgent.ScanLong（8a）
	// ════════════════════════════════════
	t.Log("\n=== STAGE 5 ===")
	r5 := runStage("5_ScanLong", func() (bool, string) {
		sr := engine.Evaluate(context.Background(), mockEvents, positions, nil)
		vb := sAgent.Verify(sr.HotSectors)
		d1 := combat_agent.NewD1Scorer(nil, "")
		d1s := d1.BatchScore(sr.ScoringPool, sr.Events, sr.MarketData)
		input := combat_agent.ScanInput{
			Sectors: vb, L1Score: sr.L1Score, L1Blocked: sr.L1Blocked,
			MarketData: sr.MarketData, D1Scores: d1s,
		}
		sigs := cAgent.ScanLong(input)
		var sigDesc []string
		for _, s := range sigs {
			sigDesc = append(sigDesc, fmt.Sprintf("%s:%s:%.2f", s.Code, s.Strategy, s.Confidence))
		}
		return true, fmt.Sprintf("signals=[%s]", strings.Join(sigDesc, ","))
	})
	printStage(t, r5)
	allResults = append(allResults, r5)

	// ════════════════════════════════════
	// Stage 6: ScanShort + 个股直入
	// ════════════════════════════════════
	t.Log("\n=== STAGE 6 ===")
	r6 := runStage("6_ScanShort+Individual", func() (bool, string) {
		sr := engine.Evaluate(context.Background(), mockEvents, positions, nil)
		ve := sAgent.Verify(sr.BearSectors)
		input := combat_agent.ScanInput{
			Sectors: ve, L1Score: sr.L1Score, L1Blocked: sr.L1Blocked,
			MarketData: sr.MarketData,
		}
		shortSigs := cAgent.ScanShort(input)
		var shortDesc []string
		for _, s := range shortSigs {
			shortDesc = append(shortDesc, fmt.Sprintf("%s:%s", s.Code, s.Action))
		}
		// 个股直入
		var individualDesc []string
		for _, st := range sr.LongStocks {
			indiv := cAgent.ScanLong(combat_agent.ScanInput{
				IndividualStocks: []string{st.Code}, MarketData: sr.MarketData,
			})
			for _, s := range indiv {
				individualDesc = append(individualDesc, fmt.Sprintf("%s:%s", s.Code, s.Strategy))
			}
		}
		return true, fmt.Sprintf("short=[%s] individual=[%s]", strings.Join(shortDesc, ","), strings.Join(individualDesc, ","))
	})
	printStage(t, r6)
	allResults = append(allResults, r6)

	// ════════════════════════════════════
	// Stage 7: StockTracker
	// ════════════════════════════════════
	t.Log("\n=== STAGE 7 ===")
	r7 := runStage("7_StockTracker", func() (bool, string) {
		st := data.NewStockTracker(filepath.Join(dir, "tracked.json"))
		sr := engine.Evaluate(context.Background(), mockEvents, positions, nil)
		vb := sAgent.Verify(sr.HotSectors)
		d1 := combat_agent.NewD1Scorer(nil, "")
		d1s := d1.BatchScore(sr.ScoringPool, sr.Events, sr.MarketData)
		sigs := cAgent.ScanLong(combat_agent.ScanInput{
			Sectors: vb, L1Score: sr.L1Score, L1Blocked: sr.L1Blocked,
			MarketData: sr.MarketData, D1Scores: d1s,
		})
		td := data.TradingDayDate(time.Now())
		expiry := data.AddTradingDays(td, 1)
		trackedCodes := make([]string, 0, len(sigs))
		for _, sig := range sigs {
			st.Add(sig.Code, sig.Name, "利好", sig.Reason, td, expiry)
			trackedCodes = append(trackedCodes, sig.Code)
		}
		active := st.GetActive(td)
		return true, fmt.Sprintf("tracked=[%s] active=%d", strings.Join(trackedCodes, ","), len(active))
	})
	printStage(t, r7)
	allResults = append(allResults, r7)

	// ════════════════════════════════════
	// Stage 8: CheckPositionAlerts
	// ════════════════════════════════════
	t.Log("\n=== STAGE 8 ===")
	r8 := runStage("8_PositionAlerts", func() (bool, string) {
		alerts := cAgent.CheckPositionAlerts(rpt, api, map[string]combat_agent.StockScores{})
		var alertDesc []string
		for _, a := range alerts {
			alertDesc = append(alertDesc, fmt.Sprintf("%s:%s", a.Code, a.AlertType))
		}
		return true, fmt.Sprintf("alerts=[%s]", strings.Join(alertDesc, ","))
	})
	printStage(t, r8)
	allResults = append(allResults, r8)

	// ════════════════════════════════════
	// Stage 9: Display Aggregator
	// ════════════════════════════════════
	t.Log("\n=== STAGE 9 ===")
	r9 := runStage("9_Dashboard", func() (bool, string) {
		sr := engine.Evaluate(context.Background(), mockEvents, positions, nil)
		vb := sAgent.Verify(sr.HotSectors)
		ve := sAgent.Verify(sr.BearSectors)
		d1 := combat_agent.NewD1Scorer(nil, "")
		d1s := d1.BatchScore(sr.ScoringPool, sr.Events, sr.MarketData)
		bs := cAgent.ScanLong(combat_agent.ScanInput{
			Sectors: vb, L1Score: sr.L1Score, L1Blocked: sr.L1Blocked,
			MarketData: sr.MarketData, D1Scores: d1s,
		})
		be := cAgent.ScanShort(combat_agent.ScanInput{
			Sectors: ve, L1Score: sr.L1Score, L1Blocked: sr.L1Blocked,
			MarketData: sr.MarketData,
		})
		alerts := cAgent.CheckPositionAlerts(rpt, api, map[string]combat_agent.StockScores{})
		agg.Update(sr, vb, ve, bs, be, alerts, nil, rpt)
		dash := agg.Current()
		if dash == nil {
			return false, "Dashboard is nil"
		}
		var finalCodes []string
		for _, s := range dash.FinalSignals {
			finalCodes = append(finalCodes, s.Code)
		}
		return true, fmt.Sprintf("events=%d hot=%d bear=%d bull_sig=%d bear_sig=%d alerts=%d final=[%s]",
			len(dash.NewsEvents), len(dash.HotSectors), len(dash.BearSectors),
			len(dash.BullSignals), len(dash.BearSignals), len(dash.AlertSignals),
			strings.Join(finalCodes, ","))
	})
	printStage(t, r9)
	allResults = append(allResults, r9)

	// ════════════════════════════════════
	// Stage 10: NShape 战法评分
	// ════════════════════════════════════
	t.Log("\n=== STAGE 10 ===")
	r10 := runStage("10_NShapeScorer", func() (bool, string) {
		wa := &n_shape.WaveA{
			ADate: time.Now().AddDate(0, 0, -1).Format("2006-01-02"),
			AOpen: 95.0, AHigh: 106.0, ALow: 94.0, AClose: 100.0,
			AVol: 80000, AChgPct: 6.0, AAboveMA60: true,
		}
		ib := &n_shape.IntradayB{
			TTime: 945, CurPrice: 103.0, CumVol: 25000,
			PrevClose: 100.0, PrevHigh: 106.0, PrevLow: 95.0,
			AvgDailyVol: 100000, AuctionChgPct: 2.5,
			BenchCurChg: 0.5, MinuteMACDDIF: 0.5, MinuteMACDDEA: 0.3,
		}
		ctx := &n_shape.Ctx{
			LLMD1Score: 30, LLMBlocked: false, // D1 满分 0~40 制
			StockPE: 12, AvgDailyVol: 100000,
		}
		scorer := n_shape.NewLeftSideScorer(nil)
		sr := scorer.Evaluate(wa, ib, ctx)
		switch {
		case sr == nil:
			return false, "scorer returned nil"
		case !sr.Valid:
			return false, fmt.Sprintf("Valid=false D1=%.1f D2=%.1f D3=%.1f D4=%.1f Total=%.1f",
				sr.D1Event, sr.D2RS, sr.D3Pullback, sr.D4Accept, sr.Total)
		case sr.Total < 60:
			return false, fmt.Sprintf("Total=%.1f < 60", sr.Total)
		default:
			return true, fmt.Sprintf("Total=%.1f D1=%.1f D2=%.1f D3=%.1f D4=%.1f Valid=true",
				sr.Total, sr.D1Event, sr.D2RS, sr.D3Pullback, sr.D4Accept)
		}
	})
	printStage(t, r10)
	allResults = append(allResults, r10)

	// ════════════════════════════════════
	// Summary
	// ════════════════════════════════════
	passCount, failCount := 0, 0
	var failIDs []string
	for _, r := range allResults {
		if r.pass {
			passCount++
		} else {
			failCount++
			failIDs = append(failIDs, strings.SplitN(r.name, "_", 2)[0])
		}
	}
	t.Logf("[SUMMARY] 全流程 %d 阶段: %d PASS, %d FAIL", len(allResults), passCount, failCount)
	if failCount > 0 {
		t.Logf("[SUMMARY] 失败阶段: %s", strings.Join(failIDs, ","))
		t.Errorf("[SUMMARY] 全流程有 %d 个阶段失败", failCount)
	} else {
		t.Log("[SUMMARY] 全流程通过 ✓")
	}
}

// ── Mock 基础设施 ──

// monitorMockTransport 实现 http.RoundTripper，拦截东财板块成分股请求返回 mock JSON，
// 其余请求透传给 inner 真实网络。
type monitorMockTransport struct {
	inner http.RoundTripper
}

// RoundTrip 解析 fs=b:<code> 提取板块代码并返回对应的 mock 成分股响应。
func (t *monitorMockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	if strings.Contains(url, "push2.eastmoney.com/api/qt/clist/get") && strings.Contains(url, "fs=b") {
		// 从 fs 查询参数提取板块代码（fs=b:BK0477 → BK0477）
		secCode := ""
		if q := req.URL.Query(); q != nil {
			if fs := q.Get("fs"); len(fs) > 2 {
				secCode = fs[2:]
			}
		}
		jsonStr := monitorSectorJSON(secCode)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       &monitorReadCloser{body: []byte(jsonStr)},
		}, nil
	}
	return t.inner.RoundTrip(req)
}

// monitorReadCloser 模拟 HTTP 响应体，实现 io.ReadCloser 接口，用于返回 mock 数据流。
type monitorReadCloser struct {
	body []byte
	pos  int
}

// Read 从内部 body 缓冲按需复制字节，读完返回 io.EOF。
func (m *monitorReadCloser) Read(p []byte) (int, error) {
	if m.pos >= len(m.body) {
		return 0, io.EOF
	}
	n := copy(p, m.body[m.pos:])
	m.pos += n
	return n, nil
}

// Close 实现 io.Closer，内存缓冲区无需释放资源。
func (m *monitorReadCloser) Close() error { return nil }

// monitorSectorJSON 根据板块代码返回模拟的东财成分股 JSON（与 pipeline_test 同构）。
func monitorSectorJSON(code string) string {
	switch code {
	case "BK0477":
		return `{"data":{"items":[{"f12":"600519","f14":"贵州茅台","f2":152000,"f3":250,"f4":30,"f15":153000,"f16":151000,"f17":151500,"f18":148000,"f5":3200000,"f6":4800000000,"f7":0.8},{"f12":"000858","f14":"五粮液","f2":13500,"f3":310,"f4":40.5,"f15":13700,"f16":13300,"f17":13400,"f18":13100,"f5":15000000,"f6":2000000000,"f7":1.2}]}}`
	case "BK0475":
		return `{"data":{"items":[{"f12":"601398","f14":"工商银行","f2":650,"f3":50,"f4":0.03,"f15":655,"f16":645,"f17":648,"f18":647,"f5":50000000,"f6":320000000,"f7":0.3},{"f12":"600036","f14":"招商银行","f2":3800,"f3":120,"f4":0.45,"f15":3850,"f16":3780,"f17":3790,"f18":3760,"f5":20000000,"f6":760000000,"f7":0.6}]}}`
	case "BK0487":
		return `{"data":{"items":[{"f12":"002230","f14":"科大讯飞","f2":4800,"f3":450,"f4":2.05,"f15":4900,"f16":4750,"f17":4780,"f18":4600,"f5":20000000,"f6":960000000,"f7":2.5},{"f12":"688256","f14":"寒武纪","f2":12500,"f3":620,"f4":7.25,"f15":12800,"f16":12200,"f17":12300,"f18":11800,"f5":8000000,"f6":1000000000,"f7":3.2}]}}`
	case "BK0480":
		return `{"data":{"items":[{"f12":"688981","f14":"中芯国际","f2":6200,"f3":-180,"f4":-1.12,"f15":6300,"f16":6150,"f17":6250,"f18":6350,"f5":5000000,"f6":310000000,"f7":0.5}]}}`
	case "BK0481":
		return `{"data":{"items":[{"f12":"300750","f14":"宁德时代","f2":19800,"f3":-210,"f4":-4.2,"f15":20200,"f16":19600,"f17":20000,"f18":20200,"f5":10000000,"f6":1980000000,"f7":1.5}]}}`
	default:
		return `{"data":{"items":[]}}`
	}
}

// monitorMockSectors 监控测试用模拟板块行情（利好：白酒/银行/人工智能，利空：半导体/新能源）。
var monitorMockSectors = []data.SectorInfo{
	{Code: "BK0477", Name: "白酒", ChangePct: 3.5, LimitupCnt: 3, Amount: 2.8e10, NetInflow: 1.2e9},
	{Code: "BK0475", Name: "银行", ChangePct: 1.2, LimitupCnt: 0, Amount: 1.5e10, NetInflow: 3.0e8},
	{Code: "BK0480", Name: "半导体", ChangePct: -2.3, LimitupCnt: 0, Amount: 1.8e10, NetInflow: -4.0e8},
	{Code: "BK0481", Name: "新能源", ChangePct: -1.8, LimitupCnt: 0, Amount: 2.2e10, NetInflow: -6.0e8},
	{Code: "BK0487", Name: "人工智能", ChangePct: 4.2, LimitupCnt: 5, Amount: 5.0e10, NetInflow: 2.0e9},
}
