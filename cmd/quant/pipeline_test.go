// Package main 全流程 Mock 测试文件。本文件通过 mock HTTP RoundTripper 拦截东方财富板块成分股请求，
// 构造完整的新闻→策略引擎→板块验证→战法评分→持仓跟踪→展示的端到端测试。
package main

import (
	"context"
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

// mockSectorStocksJSON 根据板块代码返回模拟的东方财富成分股 JSON 响应。
// 覆盖白酒（BK0477）、银行（BK0475）、人工智能（BK0487）、半导体（BK0480）、新能源（BK0481）等板块，
// 字段映射规则：f2=最新价(分)、f3=涨跌幅(基点/100)、f12=股票代码、f14=股票名称。
func mockSectorStocksJSON(sectorCode string) string {
	// 注意：parseSectorStocks 使用 f12/f14/f2/f3/f4/f15/f16/f17/f18/f5/f6/f7
	// f2/f15/f16/f17/f18 单位为分(÷100得元), f3 为基点(÷100得%)
	switch sectorCode {
	case "BK0477":
		return `{"data":{"items":[
			{"f12":"600519","f14":"贵州茅台","f2":152000,"f3":250,"f4":30,"f15":153000,"f16":151000,"f17":151500,"f18":148000,"f5":3200000,"f6":4800000000,"f7":0.8},
			{"f12":"000858","f14":"五粮液","f2":13500,"f3":310,"f4":40.5,"f15":13700,"f16":13300,"f17":13400,"f18":13100,"f5":15000000,"f6":2000000000,"f7":1.2}
		]}}`
	case "BK0475":
		return `{"data":{"items":[
			{"f12":"601398","f14":"工商银行","f2":650,"f3":50,"f4":0.03,"f15":655,"f16":645,"f17":648,"f18":647,"f5":50000000,"f6":320000000,"f7":0.3},
			{"f12":"600036","f14":"招商银行","f2":3800,"f3":120,"f4":0.45,"f15":3850,"f16":3780,"f17":3790,"f18":3760,"f5":20000000,"f6":760000000,"f7":0.6}
		]}}`
	case "BK0487":
		return `{"data":{"items":[
			{"f12":"002230","f14":"科大讯飞","f2":4800,"f3":450,"f4":2.05,"f15":4900,"f16":4750,"f17":4780,"f18":4600,"f5":20000000,"f6":960000000,"f7":2.5},
			{"f12":"688256","f14":"寒武纪","f2":12500,"f3":620,"f4":7.25,"f15":12800,"f16":12200,"f17":12300,"f18":11800,"f5":8000000,"f6":1000000000,"f7":3.2}
		]}}`
	case "BK0480":
		return `{"data":{"items":[
			{"f12":"688981","f14":"中芯国际","f2":6200,"f3":-180,"f4":-1.12,"f15":6300,"f16":6150,"f17":6250,"f18":6350,"f5":5000000,"f6":310000000,"f7":0.5}
		]}}`
	case "BK0481":
		return `{"data":{"items":[
			{"f12":"300750","f14":"宁德时代","f2":19800,"f3":-210,"f4":-4.2,"f15":20200,"f16":19600,"f17":20000,"f18":20200,"f5":10000000,"f6":1980000000,"f7":1.5}
		]}}`
	default:
		return `{"data":{"items":[]}}`
	}
}

// mockReadCloser 模拟 HTTP 响应体，实现 io.ReadCloser 接口，用于替换真实网络请求的返回流。
type mockReadCloser struct {
	body []byte
	pos  int
}

// Read 实现 io.Reader：从内部 body 缓冲按需复制字节，读完返回 io.EOF。
func (m *mockReadCloser) Read(p []byte) (int, error) {
	if m.pos >= len(m.body) {
		return 0, io.EOF
	}
	n := copy(p, m.body[m.pos:])
	m.pos += n
	return n, nil
}

// Close 实现 io.Closer：内存缓冲区无需释放资源，直接返回 nil。
func (m *mockReadCloser) Close() error { return nil }

// mockSectorInfos 提供 10 个模拟板块行情数据，涵盖利好（白酒、银行、人工智能）和利空（半导体、新能源、锂电池）方向，
// 用于验证板块扫描器的 Update 方法和后续的板块分流与验证逻辑。
var mockSectorInfos = []data.SectorInfo{
	{Code: "BK0477", Name: "白酒", ChangePct: 3.5, LimitupCnt: 3, Amount: 2.8e10, NetInflow: 1.2e9},
	{Code: "BK0475", Name: "银行", ChangePct: 1.2, LimitupCnt: 0, Amount: 1.5e10, NetInflow: 3.0e8},
	{Code: "BK0476", Name: "证券", ChangePct: 2.1, LimitupCnt: 1, Amount: 2.0e10, NetInflow: 5.0e8},
	{Code: "BK0480", Name: "半导体", ChangePct: -2.3, LimitupCnt: 0, Amount: 1.8e10, NetInflow: -4.0e8},
	{Code: "BK0481", Name: "新能源", ChangePct: -1.8, LimitupCnt: 0, Amount: 2.2e10, NetInflow: -6.0e8},
	{Code: "BK0482", Name: "锂电池", ChangePct: -2.5, LimitupCnt: 0, Amount: 1.2e10, NetInflow: -3.0e8},
	{Code: "BK0485", Name: "房地产", ChangePct: 0.8, LimitupCnt: 0, Amount: 0.8e10, NetInflow: 1.0e8},
	{Code: "BK0483", Name: "芯片", ChangePct: -3.1, LimitupCnt: 0, Amount: 1.0e10, NetInflow: -2.0e8},
	{Code: "BK0486", Name: "消费电子", ChangePct: -1.5, LimitupCnt: 0, Amount: 0.9e10, NetInflow: -1.0e8},
	{Code: "BK0487", Name: "人工智能", ChangePct: 4.2, LimitupCnt: 5, Amount: 5.0e10, NetInflow: 2.0e9},
}

// newMockedMarketAPI 创建 MarketAPI 实例，并将其底层 http.DefaultTransport 替换为 mockTransport2，
// 使得后续所有通过标准库发起的 HTTP 请求中匹配东方财富成分股 URL 的都会被拦截返回 mock 数据。
func newMockedMarketAPI() *data.MarketAPI {
	api := data.NewMarketAPI()
	defaultTransport := http.DefaultTransport
	mock := &mockTransport2{inner: defaultTransport}
	http.DefaultTransport = mock
	return api
}

// mockTransport2 实现 http.RoundTripper 接口，拦截东方财富板块成分股 API 请求并返回 mock JSON，
// 其余请求透传给 inner（真实的 http.DefaultTransport）以避免影响系统其他网络功能。
type mockTransport2 struct {
	inner http.RoundTripper
}

// RoundTrip 拦截东方财富板块成分股请求并返回 mock JSON：
// 从 fs 查询参数提取板块代码（fs=b:<code>），其余请求透传给 inner 真实网络。
func (t *mockTransport2) RoundTrip(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	if strings.Contains(url, "push2.eastmoney.com/api/qt/clist/get") && strings.Contains(url, "fs=b") {
		// 解析板块代码：fs=b:BK0477 → secCode=BK0477
		secCode := ""
		if q := req.URL.Query(); q != nil {
			fs := q.Get("fs")
			if len(fs) > 2 {
				secCode = fs[2:]
			}
		}
		jsonStr := mockSectorStocksJSON(secCode)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       &mockReadCloser{body: []byte(jsonStr)},
		}
		return resp, nil
	}
	return t.inner.RoundTrip(req)
}

// TestFullPipelineMock 是全流程 Mock 集成测试，覆盖场景包括：
//   - 6 条新闻事件（4 板块级 + 2 个股级）输入
//   - 策略引擎 Evaluate：板块分流（利好/利空/个股直入）、打分池构建（持仓+自选+新闻股）、行情填充
//   - SectorAgent.Verify：验证板块成分股（7a/7b）
//   - D1Scorer：无 LLM 时默认返回 0 分
//   - CombatAgent.ScanLong / ScanShort：板块级与个股级战法信号生成（8a/8b）
//   - StockTracker：信号跟踪记录
//   - Display Aggregator：最终 Dashboard 聚合
func TestFullPipelineMock(t *testing.T) {
	dir := t.TempDir()

	// 1. 配置
	cfgMgr := config.NewManager(filepath.Join(dir, "config.json"))
	cfgMgr.SetStrategyConfig(&config.StrategyConfig{
		Dragon:       config.DragonConfig{F1SealWeight: 0.30, F2ResonanceWeight: 0.25, F3PremiumWeight: 0.25, F4RsWeight: 0.20, PullbackMaxPct: 5.0},
		DoubleBump:   config.DoubleBumpConfig{FirstBreakVolumeMultiple: 2.0, SecondBreakVolumeMultiple: 1.5, PositionWeight: 0.3},
		NShape:       config.NShapeConfig{NPatternScoreThreshold: 0.6, HardStopLoss: -5.0},
		DragonReturn: config.DragonReturnConfig{StopLossPct: -7.0, TakeProfitPct: 15.0, MaxHoldDays: 20},
	})
	cfgMgr.Rules.Laodeng = config.LaodengConfig{Enabled: true, MarketCapMin: 500, PeMax: 15, TurnoverMin: 1.0, TechPenalty: -0.3, WeightScore: 0.15}
	cfgMgr.Save()

	// 2. 组件（使用 mock RoundTripper 拦截东财板块成分股请求）
	origTransport := http.DefaultTransport
	api := newMockedMarketAPI()
	defer func() { http.DefaultTransport = origTransport }()

	engine := strategy_engine.New(api)
	scanner := data.NewSectorScanner(api, nil)
	scanner.Update(mockSectorInfos, 3, 1, 5)
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
	wlMgr := data.NewWatchlistManager(dir)

	// 3. 模拟持仓 + 自选
	rpt.LogSignal("POS001", "600519", "贵州茅台", "做多", "Dragon", 1480, 10, 5)
	rpt.LogSignal("POS002", "688256", "寒武纪", "做多", "DoubleBump", 118, 12, 6)
	positions := rpt.HeldPositionCodes()
	wlMgr.Add("", "002230")
	watchlist := wlMgr.List("")

	t.Logf("持仓: %v", positions)
	t.Logf("自选: %v", watchlist)

	// 4. 模拟新闻事件
	now := time.Now()
	mockEvents := []newsagent.NewsEvent{
		{
			Title: "茅台提价20% 高端白酒景气度提升", Content: "贵州茅台公告上调出厂价20%。",
			Datetime: now.Format("2006-01-02 15:04:05"), Source: "同花顺",
			Direction: "利好", Score: 0.85,
			Sectors: []string{"白酒"}, UpstreamSectors: []string{"粮食种植", "包装印刷"},
			DownstreamSectors: []string{"白酒经销", "餐饮"},
			RelatedStocks:     []string{"贵州茅台", "600519", "五粮液|000858"},
			CleanedStocks:     []string{"贵州茅台|SH600519", "五粮液|SZ000858"},
			ImpactLevel:       "高", EventType: "公司", Urgency: "立即", Reason: "茅台提价带动行业利润预期",
		},
		{
			Title: "碳酸锂价格跌破8万 锂电板块承压", Content: "碳酸锂现货持续走低。",
			Datetime: now.Format("2006-01-02 15:04:05"), Source: "同花顺",
			Direction: "利空", Score: -0.7,
			Sectors: []string{"新能源", "锂电池"}, UpstreamSectors: []string{"锂矿"},
			DownstreamSectors: []string{"新能源汽车"},
			RelatedStocks:     []string{"宁德时代", "300750", "赣锋锂业|002460"},
			ImpactLevel:       "高", EventType: "行业", Urgency: "立即", Reason: "上游价格下行压缩利润",
		},
		{
			Title: "央行降准0.5个百分点", Content: "中国人民银行宣布全面降准。",
			Datetime: now.Format("2006-01-02 15:04:05"), Source: "新浪财经",
			Direction: "利好", Score: 0.65,
			Sectors: []string{"银行", "房地产", "证券"}, UpstreamSectors: []string{"金融科技"},
			RelatedStocks: []string{"工商银行", "601398", "招商银行|600036"},
			ImpactLevel:   "高", EventType: "宏观", Urgency: "立即", Reason: "降准释放流动性利好金融",
		},
		{
			Title: "美国对华半导体出口管制升级", Content: "美国商务部将更多中国半导体企业列入实体清单。",
			Datetime: now.Format("2006-01-02 15:04:05"), Source: "同花顺",
			Direction: "利空", Score: -0.55,
			Sectors: []string{"半导体", "芯片"}, UpstreamSectors: []string{"半导体设备", "电子化学品"},
			DownstreamSectors: []string{"消费电子"},
			RelatedStocks:     []string{"中芯国际|688981", "北方华创|002371"},
			ImpactLevel:       "中", EventType: "宏观", Urgency: "关注", Reason: "出口管制升级打压半导体",
		},
		{
			Title: "科大讯飞星火大模型4.0发布", Content: "科大讯飞发布星火大模型4.0。",
			Datetime: now.Format("2006-01-02 15:04:05"), Source: "同花顺",
			Direction: "利好", Score: 0.75,
			Sectors:       []string{"人工智能"},
			RelatedStocks: []string{"科大讯飞", "002230", "寒武纪|688256"},
			CleanedStocks: []string{"科大讯飞|SZ002230", "寒武纪|SH688256"},
			ImpactLevel:   "高", EventType: "行业", Urgency: "立即", Reason: "AI大模型发布",
		},
		{
			Title: "茅台提价——个股利好", Content: "贵州茅台提价20%。",
			Datetime: now.Format("2006-01-02 15:04:05"), Source: "公司公告",
			Direction: "利好", Score: 0.9,
			Level: "个股", RelatedStocks: []string{"贵州茅台|600519"},
			CleanedStocks: []string{"贵州茅台|SH600519"}, ImpactLevel: "高", EventType: "公司", Urgency: "立即", Reason: "个股直接利好",
		},
		{
			Title: "赣锋锂业收到证监会调查通知书", Content: "赣锋锂业公告收到证监会立案调查通知书。",
			Datetime: now.Format("2006-01-02 15:04:05"), Source: "公司公告",
			Direction: "利空", Score: -0.8,
			Level: "个股", RelatedStocks: []string{"赣锋锂业|002460"},
			CleanedStocks: []string{"赣锋锂业|SZ002460"}, ImpactLevel: "高", EventType: "公司", Urgency: "立即", Reason: "立案调查利空",
		},
	}

	t.Logf("\n=== 事件输入: %d 条 (板块%d + 个股%d) ===", len(mockEvents), 5, 2)

	// ─── Step 1: StrategyEngine.Evaluate ───
	t.Logf("\n=== [1/6] StrategyEngine.Evaluate ===")
	sr := engine.Evaluate(context.Background(), mockEvents, positions, watchlist)
	if sr == nil {
		t.Fatal("Evaluate 返回 nil")
	}

	hotNames := make(map[string]bool)
	for _, s := range sr.HotSectors {
		hotNames[s.Name] = true
		t.Logf("  利好板块: %s score=%.2f change=%.2f%% limitup=%d inflow=%.1e", s.Name, s.Score, s.ChangePct, s.LimitupCnt, s.NetInflow)
	}
	if !hotNames["白酒"] {
		t.Error("利好板块应包含 白酒")
	}
	if !hotNames["银行"] {
		t.Error("利好板块应包含 银行")
	}
	if !hotNames["人工智能"] {
		t.Error("利好板块应包含 人工智能")
	}

	bearNames := make(map[string]bool)
	for _, s := range sr.BearSectors {
		bearNames[s.Name] = true
		t.Logf("  利空板块: %s score=%.2f change=%.2f%%", s.Name, s.Score, s.ChangePct)
	}
	if !bearNames["新能源"] {
		t.Error("利空板块应包含 新能源")
	}
	if !bearNames["半导体"] {
		t.Error("利空板块应包含 半导体")
	}

	// 验证板块行情填充
	for _, s := range sr.HotSectors {
		if s.Name == "白酒" && s.ChangePct != 3.5 {
			t.Errorf("白酒 ChangePct 应为 3.5，实际 %.2f", s.ChangePct)
		}
	}

	// 验证个股分流
	if len(sr.LongStocks) != 1 {
		t.Errorf("个股利好应为 1只(茅台)，实际 %d", len(sr.LongStocks))
	} else {
		t.Logf("  个股利好: %s(%s)", sr.LongStocks[0].Name, sr.LongStocks[0].Code)
		if sr.LongStocks[0].Code != "600519" {
			t.Errorf("归一化后代码应为 600519，实际 %s", sr.LongStocks[0].Code)
		}
	}
	if len(sr.ShortStocks) != 1 {
		t.Errorf("个股利空应为 1只(赣锋)，实际 %d", len(sr.ShortStocks))
	} else {
		t.Logf("  个股利空: %s(%s)", sr.ShortStocks[0].Name, sr.ShortStocks[0].Code)
		if sr.ShortStocks[0].Code != "002460" {
			t.Errorf("归一化后代码应为 002460，实际 %s", sr.ShortStocks[0].Code)
		}
	}

	// 验证打分池：应包含 600519, 688256, 002230 (归一化后不应再有 SH/SZ 前缀)
	poolCodes := make(map[string]bool)
	for _, c := range sr.ScoringPool {
		poolCodes[c] = true
		t.Logf("  打分池: %s", c)
		if strings.HasPrefix(c, "SH") || strings.HasPrefix(c, "SZ") || strings.HasPrefix(c, "BJ") {
			t.Errorf("打分池代码不应有交易所前缀: %s", c)
		}
	}
	if !poolCodes["600519"] {
		t.Errorf("打分池应包含 600519(茅台)，实际有: %v", sr.ScoringPool)
	}
	if !poolCodes["688256"] {
		t.Errorf("打分池应包含 688256(持仓寒武纪)")
	}
	if !poolCodes["002230"] {
		t.Errorf("打分池应包含 002230(自选科大讯飞)")
	}
	if len(sr.ScoringPool) != 3 {
		t.Logf("注意: 打分池有 %d 只（期望 3），可能有重复", len(sr.ScoringPool))
	}

	// ─── Step 2: SectorAgent.Verify (7a/7b) ───
	t.Logf("\n=== [2/6] SectorAgent.Verify (7a/7b) ===")
	verifiedBull := sAgent.Verify(sr.HotSectors)
	t.Logf("  7a 验证利好板块: %d", len(verifiedBull))
	verifiedSectorCodes := make(map[string][]string)
	for _, v := range verifiedBull {
		t.Logf("    %s stocks=%d rps_rank=%d", v.Name, len(v.Stocks), v.RPSRank)
		verifiedSectorCodes[v.Name] = v.Stocks
		if v.Name == "白酒" {
			if len(v.Stocks) == 0 {
				t.Error("白酒板块应有成分股(mock RoundTripper 未生效)")
			} else {
				t.Logf("      白酒成分股: %v", v.Stocks)
			}
		}
	}
	verifiedBear := sAgent.Verify(sr.BearSectors)
	t.Logf("  7b 验证利空板块: %d", len(verifiedBear))
	for _, v := range verifiedBear {
		t.Logf("    %s stocks=%d", v.Name, len(v.Stocks))
		if v.Name == "新能源" && len(v.Stocks) > 0 {
			t.Logf("      新能源成分股: %v", v.Stocks)
		}
	}

	// ─── Step 3: D1Scorer ───
	t.Logf("\n=== [3/6] D1Scorer (无LLM→默认0分) ===")
	d1Scorer := combat_agent.NewD1Scorer(nil, "")
	d1Scores := d1Scorer.BatchScore(sr.ScoringPool, sr.Events, sr.MarketData)
	t.Logf("  D1评分: %d只", len(d1Scores))
	for code, ds := range d1Scores {
		if ds.Score != 0 {
			t.Logf("    %s score=%.2f (LLM未配置但得分非0)", code, ds.Score)
		} else {
			t.Logf("    %s score=0 (默认)", code)
		}
	}

	// ─── Step 4: CombatAgent (8a/8b) ───
	t.Logf("\n=== [4/6] CombatAgent (8a/8b) ===")

	bullInput := combat_agent.ScanInput{
		Sectors:    verifiedBull,
		L1Score:    sr.L1Score,
		L1Blocked:  sr.L1Blocked,
		MarketData: sr.MarketData,
		D1Scores:   d1Scores,
	}
	bullSignals := cAgent.ScanLong(bullInput)
	t.Logf("  8a 做多(板块→战法): %d 信号", len(bullSignals))
	for _, sig := range bullSignals {
		t.Logf("    [做多] %s(%s) strategy=%s conf=%.2f action=%s", sig.Code, sig.Name, sig.Strategy, sig.Confidence, sig.Action)
	}

	// 个股利好直入 (8a)
	for _, st := range sr.LongStocks {
		indivInput := combat_agent.ScanInput{
			IndividualStocks: []string{st.Code},
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
		}
		signals := cAgent.ScanLong(indivInput)
		t.Logf("  8a 做多(个股%s→战法): %d 信号", st.Code, len(signals))
		bullSignals = append(bullSignals, signals...)
	}

	// 8b 做空
	var bearSignals []combat_agent.Signal
	if cAgent.ShortEnabled() && len(verifiedBear) > 0 {
		bearInput := combat_agent.ScanInput{
			Sectors:    verifiedBear,
			L1Score:    sr.L1Score,
			L1Blocked:  sr.L1Blocked,
			MarketData: sr.MarketData,
			D1Scores:   d1Scores,
		}
		signals := cAgent.ScanShort(bearInput)
		t.Logf("  8b 做空(板块→战法): %d 信号", len(signals))
		bearSignals = append(bearSignals, signals...)
	}
	for _, st := range sr.ShortStocks {
		indivInput := combat_agent.ScanInput{
			IndividualStocks: []string{st.Code},
			MarketData:       sr.MarketData,
			D1Scores:         d1Scores,
		}
		signals := cAgent.ScanShort(indivInput)
		t.Logf("  8b 做空(个股%s→战法): %d 信号", st.Code, len(signals))
		bearSignals = append(bearSignals, signals...)
	}

	// ─── Step 5: StockTracker ───
	t.Logf("\n=== [5/6] StockTracker ===")
	td := data.TradingDayDate(time.Now())
	stockTracker := data.NewStockTracker(filepath.Join(dir, "tracked_stocks.json"))
	for _, sig := range append(bullSignals, bearSignals...) {
		dirLabel := "利好"
		if sig.Direction == "做空" {
			dirLabel = "利空"
		}
		expiry := data.AddTradingDays(td, 1)
		stockTracker.Add(sig.Code, sig.Name, dirLabel, sig.Reason, td, expiry)
		t.Logf("    跟踪: %s(%s) dir=%s", sig.Code, sig.Name, dirLabel)
	}
	tracked := stockTracker.GetActive(td)
	t.Logf("    活跃跟踪: %d 只", len(tracked))
	for _, ts := range tracked {
		t.Logf("      [%s] %s(%s) entry=%s", ts.Direction, ts.Code, ts.Name, ts.EntryTD)
	}

	// ─── Step 6: Display ───
	t.Logf("\n=== [6/6] Display Aggregator ===")
	alertSignals := cAgent.CheckPositionAlerts(rpt, api, map[string]combat_agent.StockScores{})
	agg.Update(sr, verifiedBull, verifiedBear, bullSignals, bearSignals, alertSignals, nil, rpt)
	dash := agg.Current()
	if dash == nil {
		t.Fatal("Dashboard 为 nil")
	}
	t.Logf("    Dashboard: events=%d hot=%d bear=%d long_sig=%d short_sig=%d alerts=%d final=%d",
		len(dash.NewsEvents), len(dash.HotSectors), len(dash.BearSectors),
		len(dash.BullSignals), len(dash.BearSignals), len(dash.AlertSignals), len(dash.FinalSignals))

	// ─── 汇总 ───
	t.Log("\n" + strings.Repeat("=", 60))
	t.Log("全流程测试结果:")
	t.Logf("  板块分流: 利好%d 利空%d", len(sr.HotSectors), len(sr.BearSectors))
	t.Logf("  个股分流: 利好%d 利空%d (归一化验证: %s)", len(sr.LongStocks), len(sr.ShortStocks), sr.LongStocks[0].Code)
	t.Logf("  打分池: %d只 (已去交易所前缀)", len(sr.ScoringPool))
	t.Logf("  7a验证: %d板块 (含成分股)", len(verifiedBull))
	t.Logf("  7b验证: %d板块 (含成分股)", len(verifiedBear))
	t.Logf("  8a做多信号: %d", len(bullSignals))
	t.Logf("  8b做空信号: %d", len(bearSignals))
	t.Logf("  最终信号: %d", len(dash.FinalSignals))
	t.Log(strings.Repeat("=", 60))
}

// TestNShapeScorer 直接构造 NShape（N型战法）的 A 浪、日内 B 段和上下文数据，
// 验证评分器 Evaluate 的得分计算是否符合预期（D1 事件分、D2 强度分、D3 回调分、D4 承接分、总分 >= 60 触发有效信号）。
func TestNShapeScorer(t *testing.T) {
	wa := &n_shape.WaveA{
		ADate:      time.Now().AddDate(0, 0, -1).Format("2006-01-02"),
		AOpen:      95.0,
		AHigh:      106.0,
		ALow:       94.0,
		AClose:     100.0,
		AVol:       80000,
		AChgPct:    6.0, // >= 5% → 通过 morphologyGate
		AAboveMA60: true,
	}
	ib := &n_shape.IntradayB{
		TTime:         945, // 9:45
		CurPrice:      103.0,
		CumVol:        25000,
		PrevClose:     100.0,
		PrevHigh:      106.0,
		PrevLow:       95.0,
		AvgDailyVol:   100000,
		AuctionChgPct: 2.5, // 1.5%~5.0% → D2a=15
		BenchCurChg:   0.5,
		MinuteMACDDIF: 0.5,
		MinuteMACDDEA: 0.3, // DIF > DEA && DIF > 0 → D4a=5
	}
	ctx := &n_shape.Ctx{
		LLMD1Score:  20, // D1 满分 0~40 制，直接采用 LLM 分
		LLMBlocked:  false,
		StockPE:     12, // < 15 → D3=20
		AvgDailyVol: 100000,
	}

	scorer := n_shape.NewLeftSideScorer(nil)
	sr := scorer.Evaluate(wa, ib, ctx)
	if sr == nil {
		t.Fatal("评分结果 nil")
	}

	t.Logf("D1=%.1f D2=%.1f D3=%.1f D4=%.1f Total=%.1f Valid=%v LeftSignal=%v",
		sr.D1Event, sr.D2RS, sr.D3Pullback, sr.D4Accept, sr.Total, sr.Valid, sr.LeftSignal)
	t.Logf("D2明细: %s", sr.D2Desc)
	t.Logf("D3明细: %s", sr.D3Desc)
	t.Logf("D4明细: %s", sr.D4Desc)

	if !sr.Valid {
		t.Error("期望评分 Valid=true，但得到 false — NShape 战法未触发")
		t.Logf("  检查: D1=%.1f(需>0), D2=%.1f(需>=15), Total=%.1f(需>=60)",
			sr.D1Event, sr.D2RS, sr.Total)
	}
	if sr.Total < 60 {
		t.Errorf("总分 %.1f < 60", sr.Total)
	}
	if sr.D2RS < 15 {
		t.Errorf("D2=%.1f < 15", sr.D2RS)
	}
	if sr.D1Event <= 0 {
		t.Errorf("D1=%.1f <= 0", sr.D1Event)
	}
	if sr.Valid {
		t.Logf("\n  ✅ NShape 战法触发成功! Total=%.1f >= 60", sr.Total)
	}
}

// init 包初始化函数：设置环境变量 QUANT_DATA_DIR 到临时目录，
// 确保测试期间所有依赖该环境变量的数据读写操作（如 StockTracker）使用隔离的测试路径。
func init() {
	_ = os.Setenv("QUANT_DATA_DIR", os.TempDir()+"/.quant-test-pipeline")
}
