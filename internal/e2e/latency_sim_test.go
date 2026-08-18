// 实速模拟（real-speed rehearsal）端到端测试：
// 把"数据回传、LLM调用、LLM结果回传、引擎计算、归因、出信号"全部按真实延迟基线跑一遍，
// 逐环节计量并给出"实盘一轮"的外推消耗；把超出 5s 近实时预算的环节列入 TODO 清单。
//
// 默认 ScaleFactor≈0.02（秒级跑完，保留阶段关系），报告外推 1×；
// 设 E2E_REALSPEED=1 走真实 1× 卡时间彩排（更接近实盘的真实墙钟）。
//
// 运行：go test ./internal/e2e/ -run TestRealSpeedRehearsal -v
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
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

// rehearsalRig 实速彩排专用装配产物。
type rehearsalRig struct {
	eng     *engine.Engine
	calls   *llmCalls
	metrics *simMetrics
	wl      *data.WatchlistManager
}

// newRehearsalRig 复刻 newTestEngine 的装配，但注入 latencyTransport + 分阶段时延 mock LLM，
// 共享同一份 simMetrics，使多轮彩排的分项时延计数可累计。
// withNews=false 时注入空新闻源，用于"近实时打分轮"彩排（无新闻，测稳态打分/D1复用）。
func newRehearsalRig(t *testing.T, fix *Fixture, profile *LatencyProfile, metrics *simMetrics, withNews bool) *rehearsalRig {
	t.Helper()

	if withNews {
		applyScenarioOverrides(fix)
	} else {
		fix.News = map[string][]data.NewsItem{"ths": {}, "sina": {}, "cls": {}}
	}

	rt := &latencyTransport{fix: &fixtureTransport{fix: fix}, profile: profile, metrics: metrics}

	marketAPI := data.NewMarketAPI()
	marketAPI.SetTransport(rt)

	thsClient := data.NewTHSClient()
	thsClient.SetTransport(rt)

	var matcher *data.EventMatcher
	if cfg, err := data.LoadEvents(filepath.Join("..", "..", "events_leftside.yaml")); err == nil {
		matcher = data.NewEventMatcher(cfg)
	}

	srv, calls := newLatencyLLM(profile, metrics)
	llmClient := llm.New(llm.Config{APIKey: "e2e-mock", APIURL: srv.URL, Model: "mock"})

	tmp := t.TempDir()

	authMgr := auth.NewManager(tmp)
	if err := authMgr.Init(); err != nil {
		t.Fatalf("auth init: %v", err)
	}
	if _, err := authMgr.Register("tester", "tester123"); err != nil {
		t.Fatalf("auth register: %v", err)
	}

	cleaner := data.NewStockCleaner(marketAPI)
	nAgent := newsagent.New(marketAPI, llmClient, cleaner, tmp)

	strategyEngine := strategy_engine.New(marketAPI)
	scanner := data.NewSectorScanner(marketAPI, matcher)
	strategyEngine.SetScanner(scanner)

	cfgMgr := config.NewManager(filepath.Join(tmp, "config.json"))
	cfgMgr.Rules.Strategy.Dragon.F1SealWeight = 0.30
	cfgMgr.Rules.Strategy.Dragon.F2ResonanceWeight = 0.25
	cfgMgr.Rules.Strategy.Dragon.F3PremiumWeight = 0.20
	cfgMgr.Rules.Strategy.Dragon.F4RsWeight = 0.25
	cfgMgr.Rules.Emotion.EmoClimaxLimitupMin = 90
	cfgMgr.Rules.Emotion.EmoClimaxBoardMin = 5

	sAgent := sector_agent.New(scanner, data.NewRPSManager())
	cAgent := combat_agent.New(cfgMgr.GetStrategyConfig())
	cAgent.SetLaodengConfig(&cfgMgr.Rules.Laodeng)
	cAgent.SetRunners([]combat_agent.StrategyRunner{
		{Type: strategy.SignalDragon, Strategy: dragon.New(cfgMgr)},
		{Type: strategy.SignalDoubleBump, Strategy: double_bump.New(cfgMgr)},
		{Type: strategy.SignalNShape, Strategy: n_shape.New(cfgMgr, matcher)},
		{Type: strategy.SignalDragonReturn, Strategy: dragon_return.New(cfgMgr)},
	})

	rpt := report.New(filepath.Join(tmp, "report.json"))
	agg := display.New()
	wlMgr := data.NewWatchlistManager(tmp)
	stockTracker := data.NewStockTracker(filepath.Join(tmp, "tracked_stocks.json"))

	sse := server.NewSSEBroker()
	eng := engine.New(marketAPI, nAgent, strategyEngine, sAgent, cAgent, agg, rpt,
		stockTracker, wlMgr, sse, llmClient, thsClient, tmp)
	eng.SetScanner(scanner)
	eng.SetEmotionConfig(&cfgMgr.Rules.Emotion)

	t.Cleanup(func() { srv.Close() })
	return &rehearsalRig{eng: eng, calls: calls, metrics: metrics, wl: wlMgr}
}

// newsSince 返回 fixture 抓取日 08:30 作为追回起点（数据驱动）。
func newsSince(t *testing.T, fix *Fixture) time.Time {
	capT, err := time.ParseInLocation("2006-01-02 15:04:05", fix.CapturedAt, time.Local)
	if err != nil {
		t.Fatalf("解析 fixture 抓取时间: %v", err)
	}
	return time.Date(capT.Year(), capT.Month(), capT.Day(), 8, 30, 0, 0, time.Local)
}

// TestRealSpeedRehearsal 全链路实速彩排：逐环节计时并输出报告 + TODO 超标清单。
func TestRealSpeedRehearsal(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix, err := LoadFixture(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("加载 fixture: %v", err)
	}

	// 默认快跑外推；E2E_REALSPEED=1 走真实 1× 卡时间彩排
	profile := realisticProfile()
	if os.Getenv("E2E_REALSPEED") == "1" {
		profile.ScaleFactor = 1.0
	}

	// 阶段1：新闻触发主循环彩排（每轮新装配 → 每轮都有新闻 → 全关键路径 3 次）
	metricsMain := &simMetrics{}
	var mainTimings []*engine.RunTiming
	for i := 0; i < 3; i++ {
		rig := newRehearsalRig(t, fix, profile, metricsMain, true)
		rig.eng.SetShortEnabled(true)
		rig.eng.SetLongEnabled(true)
		rig.wl.Add("", "300750")
		rig.wl.Add("", "300308")
		rig.eng.Run(context.Background(), newsSince(t, fix))
		mainTimings = append(mainTimings, rig.eng.LastRunTiming())
	}

	// 阶段2：近实时打分轮（无新闻 + D1 复用 → 打分-only 路径，测 5s 稳态）
	metricsScore := &simMetrics{}
	consumeRig := newRehearsalRig(t, fix, profile, metricsScore, false)
	consumeRig.eng.SetShortEnabled(true)
	consumeRig.eng.SetLongEnabled(true)
	consumeRig.wl.Add("", "300750")
	consumeRig.wl.Add("", "300308")
	since := newsSince(t, fix)
	consumeRig.eng.Run(context.Background(), since) // 消费新闻
	var scoreTimings []*engine.RunTiming
	for i := 0; i < 3; i++ {
		consumeRig.eng.Run(context.Background(), since.Add(2*time.Second))
		scoreTimings = append(scoreTimings, consumeRig.eng.LastRunTiming())
	}

	// 阶段3：纯计算标定（Scale=0 → 注入 0，测得各分段纯 CPU 时间）
	prof0 := realisticProfile()
	prof0.ScaleFactor = 0
	rig0 := newRehearsalRig(t, fix, prof0, &simMetrics{}, true)
	rig0.eng.SetShortEnabled(true)
	rig0.eng.SetLongEnabled(true)
	rig0.wl.Add("", "300750")
	rig0.wl.Add("", "300308")
	rig0.eng.Run(context.Background(), newsSince(t, fix))
	compute := rig0.eng.LastRunTiming()

	// 报告 + TODO 清单
	todos := renderRehearsalReport(t, profile, metricsMain, metricsScore, mainTimings, scoreTimings, compute)

	// ---- 断言：彩排驱动全链路且无行为回归 ----
	if metricsMain.stage0N == 0 || metricsMain.stage2N == 0 || metricsMain.d1N == 0 {
		t.Errorf("新闻触发轮应产生 Stage0/2/D1 LLM 调用(实测 %d/%d/%d)",
			metricsMain.stage0N, metricsMain.stage2N, metricsMain.d1N)
	}
	if metricsScore.stage0N != 0 || metricsScore.stage2N != 0 {
		t.Errorf("近实时打分轮不应重复调 Stage0/Stage2 新闻分析 LLM(实测 %d/%d), 打分轮应无新闻分析",
			metricsScore.stage0N, metricsScore.stage2N)
	}
	if metricsScore.d1N < 3 {
		t.Errorf("近实时打分轮应对打分池执行 D1 评分(≥3次, 实测%d), 否则打分池断链", metricsScore.d1N)
	}
	// 打分轮稳态应显著快于新闻触发轮（无 LLM 等待）
	avgMain := avgDur(mainTimings, func(tm *engine.RunTiming) time.Duration { return tm.Total })
	avgScore := avgDur(scoreTimings, func(tm *engine.RunTiming) time.Duration { return tm.Total })
	if avgMain <= avgScore {
		t.Errorf("打分轮稳态(%v)应快于新闻触发轮(%v)", avgScore, avgMain)
	}
	if compute == nil {
		t.Fatal("纯计算标定轮未产出分段耗时")
	}
	_ = todos
}

// avgDur 计算一组时序分段均耗时。
func avgDur(ts []*engine.RunTiming, f func(*engine.RunTiming) time.Duration) time.Duration {
	if len(ts) == 0 {
		return 0
	}
	var sum time.Duration
	for _, tm := range ts {
		sum += f(tm)
	}
	return sum / time.Duration(len(ts))
}

// maxDur 计算一组时序分段最大耗时。
func maxDur(ts []*engine.RunTiming, f func(*engine.RunTiming) time.Duration) time.Duration {
	var mx time.Duration
	for _, tm := range ts {
		if v := f(tm); v > mx {
			mx = v
		}
	}
	return mx
}

// renderRehearsalReport 打印分项计量表 + 1× 外推 + 5s 预算对照，返回超预算 TODO 清单。
func renderRehearsalReport(t *testing.T, p *LatencyProfile, mMain, mScore *simMetrics,
	mainTimings, scoreTimings []*engine.RunTiming, compute *engine.RunTiming) []string {
	t.Helper()

	var out []string
	push := func(s string) { out = append(out, s) }
	p1 := oneXBase(p)

	push("")
	push("========== 实速模拟报告（real-speed rehearsal） ==========")
	push("注入缩放 Scale=" + strconv.FormatFloat(p.ScaleFactor, 'g', 3, 64) + "，报告外推口径 1×（实盘一轮）")

	// —— 数据回传 ——
	push("")
	push("[数据回传速度] 名称 | 彩排请求数 | 单次1× | 累计1×")
	emit := func(rows [][4]string) {
		for _, r := range rows {
			push("  " + pad(r[0], 22) + pad(r[1], 8) + pad(r[2], 12) + r[3])
		}
	}
	emit(mMain.tabData(p))

	// —— LLM ——
	push("")
	push("[LLM 调用+回传速度] 名称 | 调用数 | 单次1× | 累计1×")
	emit(mMain.tabLLM(p))

	// —— 引擎分段实测 ——
	push("")
	push("[引擎分段 新闻触发轮] 平均 | 最大 | 纯CPU标定")
	stageRows := []struct {
		name string
		f    func(*engine.RunTiming) time.Duration
	}{
		{"新闻拉取", func(tm *engine.RunTiming) time.Duration { return tm.News.Fetch }},
		{"Stage0 质检", func(tm *engine.RunTiming) time.Duration { return tm.News.Stage0 }},
		{"Stage2+事件构建", func(tm *engine.RunTiming) time.Duration { return tm.News.Stage2 }},
		{"策略评估Evaluate(行情)", func(tm *engine.RunTiming) time.Duration { return tm.Evaluate }},
		{"D1 批量评分", func(tm *engine.RunTiming) time.Duration { return tm.D1 }},
		{"PE预取/涨停池", func(tm *engine.RunTiming) time.Duration { return tm.PE + tm.Pool }},
		{"板块验证Verify", func(tm *engine.RunTiming) time.Duration { return tm.Verify }},
		{"板块→个股归因", func(tm *engine.RunTiming) time.Duration { return tm.MergeSector }},
		{"出信号(做多/做空/个股)", func(tm *engine.RunTiming) time.Duration { return tm.Signals }},
		{"持仓提醒+看板+广播", func(tm *engine.RunTiming) time.Duration { return tm.Alerts + tm.Agg + tm.SSE }},
	}
	for _, s := range stageRows {
		avg := avgDur(mainTimings, s.f)
		max := maxDur(mainTimings, s.f)
		cpu := s.f(compute)
		push("  " + pad(s.name, 22) + pad(fmtDur(avg), 12) + pad(fmtDur(max), 12) + fmtDur(cpu))
	}
	avgMainTotal := avgDur(mainTimings, func(tm *engine.RunTiming) time.Duration { return tm.Total })
	push("  整轮总耗时" + pad("", 22) + pad(fmtDur(avgMainTotal), 12) + pad(fmtDur(maxDur(mainTimings, func(tm *engine.RunTiming) time.Duration { return tm.Total })), 12) + fmtDur(compute.Total))

	// —— 打分轮（5s 稳态） ——
	push("")
	avgScore := avgDur(scoreTimings, func(tm *engine.RunTiming) time.Duration { return tm.Total })
	maxScore := maxDur(scoreTimings, func(tm *engine.RunTiming) time.Duration { return tm.Total })
	push("[近实时打分轮(无新新闻/D1复用)] 平均 " + fmtDur(avgScore) + " | 最大 " + fmtDur(maxScore))

	// —— 1× 外推关键路径 ——
	push("")
	push("[1× 外推：新闻→信号 关键路径单轮]")
	llm1x := func(tokens int, n int) time.Duration { return p1.llmDuration(tokens) * time.Duration(n) }
	data1x := func(base time.Duration, n int) time.Duration { return p1.speed(base) * time.Duration(n) }
	critical := time.Duration(0)
	type seg struct {
		name string
		d    time.Duration
	}
	var segs []seg
	add := func(name string, d time.Duration) {
		segs = append(segs, seg{name, d})
		critical += d
	}
	add("新闻源拉取(3源并行估算)", data1x(p.News, 4))
	add("Stage0 LLM 调用", llm1x(p.Stage0Tokens, 1))
	add("Stage2 LLM 深度分析", llm1x(p.Stage2Tokens, 2))
	add("行情/板块数据回传", data1x(p.Quote, 18))
	add("D1 批量评分 LLM", llm1x(p.D1Tokens, 1))
	add("引擎纯计算(标定)", compute.Total)
	_ = segs
	push("  关键路径合计 ≈ " + fmtDur(critical))

	// —— 预算对照 + TODO ——
	push("")
	push("[预算对照 5s 近实时节奏]")
	const budget5s = 5 * time.Second
	var todos []string
	if critical > budget5s {
		todos = append(todos, "新闻触发主循环关键路径 "+fmtDur(critical)+" 超 5s 预算 → 建议：Stage0/Stage2/D1 三次串行 LLM 改并行/批量合并或降低频次")
		push("  ✗ 主循环关键路径 " + fmtDur(critical) + " 超 5s 预算")
	} else {
		push("  ✓ 主循环关键路径 " + fmtDur(critical) + " 在 5s 预算内")
	}
	llmSerial := llm1x(p.Stage0Tokens, 1) + llm1x(p.Stage2Tokens, 2) + llm1x(p.D1Tokens, 1)
	if llmSerial > budget5s {
		todos = append(todos, "单轮 LLM 串行总时长 "+fmtDur(llmSerial)+" 超 5s → 建议：批量合并或 LLM 超时/降级策略")
		push("  ✗ 单轮 LLM 串行 "+fmtDur(llmSerial)+" 超 5s 预算")
	} else {
		push("  ✓ 单轮 LLM 串行 " + fmtDur(llmSerial) + " 在 5s 预算内")
	}
	score1x := avgScore / time.Duration(maxI(int(p.ScaleFactor*1000), 1)) * 1000
	if score1x > budget5s {
		todos = append(todos, "近实时打分轮 1× "+fmtDur(score1x)+" 超 5s → 建议：行情拉取批量/缓存")
		push("  ✗ 打分轮 1× "+fmtDur(score1x)+" 超 5s 预算")
	} else {
		push("  ✓ 打分轮 1× " + fmtDur(score1x) + " 在 5s 预算内")
	}
	for _, st := range []struct {
		name string
		f    func(*engine.RunTiming) time.Duration
	}{
		{"出信号", func(tm *engine.RunTiming) time.Duration { return tm.Signals }},
		{"策略评估", func(tm *engine.RunTiming) time.Duration { return tm.Evaluate }},
		{"板块验证", func(tm *engine.RunTiming) time.Duration { return tm.Verify }},
	} {
		if cpu := st.f(compute); cpu > time.Second {
			todos = append(todos, "纯CPU热点 "+st.name+" "+fmtDur(cpu)+" > 1s")
		}
	}

	if len(todos) > 0 {
		push("")
		push("[TODO 超预算清单]")
		sort.Strings(todos)
		for _, td := range todos {
			push("  - " + td)
		}
	} else {
		push("[TODO 超预算清单] 无")
	}

	t.Log(strings.Join(out, "\n"))
	return todos
}

// fmtDur 格式化耗时（自动选 ms/s 单位）。
func fmtDur(d time.Duration) string {
	if d >= time.Second {
		return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + "s"
	}
	if d >= time.Millisecond {
		return strconv.Itoa(int(d.Milliseconds())) + "ms"
	}
	return strconv.Itoa(int(d.Microseconds())) + "µs"
}

// pad 左对齐补齐宽度。
func pad(s string, w int) string {
	if len(s) >= w {
		return s + " "
	}
	return s + strings.Repeat(" ", w-len(s))
}
