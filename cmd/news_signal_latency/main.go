// news_signal_latency 测量"新闻资讯→战法信号"的端到端耗时。
//
// 复刻 cmd/quant/main.go 的装配方式，使用真实行情数据源 + 真实 LLM，沿用 production 的
// D1Scorer.BatchScore（新闻→D1）与 combat_agent.ScorePool（逐战法评分+信号）。
// 对一组今日实盘标的，按生产 5s 节奏跑有限轮次，记录每战法首个信号的产出轮次与耗时。
//
// 运行：go run ./cmd/news_signal_latency -cycles 12 -sleep 5
// 可选：LLM_API_KEY / LLM_API_URL / LLM_MODEL 环境变量启用真实 LLM D1；否则 D1 用利好种子分。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// probeStock 探针标的（代码 + 名称）。
type probeStock struct{ Code, Name string }

// 今日关注的实盘标的（有色/电力/算力，覆盖四大战法候选）。
var probeStocks = []probeStock{
	{"600206", "有研新材"}, {"002428", "云南锗业"}, {"300489", "光智科技"},
	{"600362", "江西铜业"}, {"300308", "中际旭创"}, {"600396", "华电辽能"},
	{"601991", "大唐发电"}, {"000539", "粤电力A"}, {"600744", "华银电力"},
	{"001896", "豫能控股"},
}

// defaultDataDir 返回默认数据目录：QUANT_DATA_DIR 环境变量优先，否则 ~/.quant-trading-v2。
func defaultDataDir() string {
	if d := os.Getenv("QUANT_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".quant-trading-v2")
}

// buildNews 构造今日纪实（板块+个股）利好事件，作为"新闻资讯"输入。
func buildNews() []newsagent.NewsEvent {
	c2n := func(c, n string) string { return n + "|" + c }
	powerStocks := []string{
		c2n("600396", "华电辽能"), c2n("601991", "大唐发电"), c2n("000539", "粤电力A"),
		c2n("600744", "华银电力"), c2n("001896", "豫能控股"),
	}
	metalStocks := []string{
		c2n("600206", "有研新材"), c2n("002428", "云南锗业"),
		c2n("300489", "光智科技"), c2n("600362", "江西铜业"),
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	return []newsagent.NewsEvent{
		{
			Title: "迎峰度夏电力保供加码 电价上浮预期升温 多只电力股涨停", Content: "夏季用电高峰来临，多省上调电价，电力板块集体走强。",
			Datetime: ts, Source: "财联社", IsMaterial: true, Level: "板块", Direction: "利好", Score: 8,
			Sectors: []string{"电力"}, RelatedStocks: []string{"华电辽能", "大唐发电", "粤电力A", "华银电力", "豫能控股"}, CleanedStocks: powerStocks,
		},
		{
			Title: "锗价稀土行情爆发 有色金属板块强势上攻", Content: "锗、稀土等小金属价格走高，库存低位，有色板块涨停潮。",
			Datetime: ts, Source: "财联社", IsMaterial: true, Level: "板块", Direction: "利好", Score: 8,
			Sectors: []string{"有色金属"}, RelatedStocks: []string{"有研新材", "云南锗业", "光智科技", "江西铜业"}, CleanedStocks: metalStocks,
		},
		{
			Title: "中际旭创 800G光模块出货大增 业绩超预期", Content: "公司公告800G出货量同比大增，算力需求景气。",
			Datetime: ts, Source: "效现", IsMaterial: true, Level: "个股", Direction: "利好", Score: 7,
			RelatedStocks: []string{"中际旭创"}, CleanedStocks: []string{"中际旭创|300308"},
		},
	}
}

// loadRawEvents 读取左侧事件 YAML（若存在），供 D1Scorer 作为事件匹配上下文；
// 文件缺失时返回空串（不阻塞后续链路）。
func loadRawEvents() string {
	b, err := os.ReadFile("events_leftside.yaml")
	if err != nil {
		return ""
	}
	return string(b)
}

// main 测量"新闻→信号"端到端耗时：
// 阶段1 真实网络拉行情 → 阶段2 新闻→D1（真实 LLM 或种子分）→ 阶段3 按生产 5s 节奏
// 逐轮跑逐战法评分，记录每个战法首个信号的产出耗时并汇总打印。
func main() {
	cycles := flag.Int("cycles", 12, "模拟生产近实时扫描轮数（每轮间隔 -sleep 秒）")
	sleepSec := flag.Int("sleep", 5, "每轮等待(秒，对齐生产5s节奏)")
	dataDir := flag.String("dataDir", defaultDataDir(), "数据目录(存放 config.json)")
	flag.Parse()

	// 配置：优先真实生产 config.json，缺失用默认
	cfgMgr := config.NewManager(filepath.Join(*dataDir, "config.json"))

	// —— 真实行情数据客户端（不注入 mock transport，走真实网络降级链） ——
	marketAPI := data.NewMarketAPI()
	thsClient := data.NewTHSClient()
	strategyEngine := strategy_engine.New(marketAPI)
	strategyEngine.SetTHS(thsClient)

	var matcher *data.EventMatcher
	if cfg, err := data.LoadEvents("events_leftside.yaml"); err == nil {
		matcher = data.NewEventMatcher(cfg)
	}

	codes := make([]string, 0, len(probeStocks))
	for _, p := range probeStocks {
		codes = append(codes, p.Code)
	}

	// —— 阶段1：真实行情数据获取（新浪→同花顺→腾讯→东财 降级链） ——
	fmt.Printf("=== 阶段1 行情数据获取(真实网络, %d只) ===\n", len(codes))
	tData := time.Now()
	md := strategyEngine.BuildScoringData(context.Background(), codes, nil)
	dataMs := time.Since(tData)
	valid := 0
	for _, c := range codes {
		d := md[c]
		if d != nil && (d.Price > 0 || len(d.KLines) > 0 || len(d.MinuteKLine) > 0) {
			valid++
		}
	}
	fmt.Printf("  行情拉取 %d只: 总耗时 %v (%d有效)\n", len(codes), dataMs, valid)

	// —— 阶段2：新闻→D1 ——
	fmt.Println("=== 阶段2 新闻→D1 ===")
	// 配置真实 LLM D1 评分器：API 参数缺省回退配置文件，随后批量评分。
	d1s := map[string]combat_agent.D1Score{}
	var d1Ms time.Duration
	if apiKey := os.Getenv("LLM_API_KEY"); apiKey != "" {
		llmCfg := llm.Config{
			APIKey:    apiKey,
			APIURL:    os.Getenv("LLM_API_URL"),
			Model:     os.Getenv("LLM_MODEL"),
			Streaming: cfgMgr.Rules.LLM.StreamingEnabled(),
		}
		if llmCfg.APIURL == "" {
			llmCfg.APIURL = cfgMgr.Rules.LLM.APIURL
		}
		if llmCfg.Model == "" {
			llmCfg.Model = cfgMgr.Rules.LLM.Model
		}
		llmCfg.Timeout = time.Duration(cfgMgr.Rules.LLM.TimeoutSec) * time.Second
		scorer := combat_agent.NewD1Scorer(llm.New(llmCfg), loadRawEvents())
		fmt.Printf("  真实LLM D1批量评分(模型=%s): ", llmCfg.Model)
		tD1 := time.Now()
		d1s = scorer.BatchScore(codes, buildNews(), md)
		d1Ms = time.Since(tD1)
		fmt.Printf("完成, 耗时 %v\n", d1Ms)
	} else {
		fmt.Println("  未配置 LLM_API_KEY → D1 利好种子分(仅测行情+评分链路耗时)")
		for _, c := range codes {
			d1s[c] = combat_agent.D1Score{Code: c, Score: 0.8, Blocked: false, Reason: "模拟:利好消息"}
		}
	}

	// —— 阶段3：逐战法评分+信号（生产5s节奏） ——
	// 新闻就绪锚点：行情+D1 完成后（等同于新闻注入完成）。
	cAgent := combat_agent.New(cfgMgr.GetStrategyConfig())
	cAgent.SetLaodengConfig(&cfgMgr.Rules.Laodeng)
	cAgent.SetRunners(combat_agent.NewRunners(cfgMgr, matcher))

	fmt.Printf("=== 阶段3 逐战法评分+信号(生产5s节奏, %d轮) ===\n", *cycles)
	// 按生产 5s 节奏循环评分：记录各战法首次信号时间与信号计数。
	tAnchor := time.Now()
	firstSig := map[string]time.Time{}
	sigCount := map[string]int{}
	scanTotal := time.Duration(0)
	for i := 1; i <= *cycles; i++ {
		tScan := time.Now()
		_, sigs := cAgent.ScorePool(codes, md, d1s, "")
		scanTotal += time.Since(tScan)
		for _, s := range sigs {
			_LAT := string(s.Strategy)
			sigCount[_LAT]++
			if _, ok := firstSig[_LAT]; !ok {
				firstSig[_LAT] = time.Now()
			}
		}
		if i < *cycles {
			time.Sleep(time.Duration(*sleepSec) * time.Second)
		}
	}

	// —— 汇总 ——
	fmt.Println("\n=== 耗时汇总 (自新闻就绪时点起) ===")
	fmt.Printf("  行情获取(真实网络): %v\n", dataMs)
	fmt.Printf("  新闻→D1:            %v\n", d1Ms)
	avgScan := scanTotal / time.Duration(*cycles)
	fmt.Printf("  平均每轮扫描(10只×4战法): %v\n", avgScan)
	fmt.Printf("  各战法首信号耗时(自新闻就绪起算):\n")
	for _, st := range []strategy.SignalType{strategy.SignalDragon, strategy.SignalDoubleBump, strategy.SignalNShape, strategy.SignalDragonReturn} {
		k := string(st)
		if ts, ok := firstSig[k]; ok {
			elapsed := ts.Sub(tAnchor)
			fmt.Printf("    %-14s: %v (共%d轮内产出%d个信号)\n", k, elapsed, *cycles, sigCount[k])
		} else {
			fmt.Printf("    %-14s: 未出信号 (共%d轮)\n", k, *cycles)
		}
	}
	fmt.Printf("\n注: 龙头=N/A(依赖涨停池, 盘后拉取可能为空); N形/龙回头首信号通常需多轮预热(波态/回调计数).\n")
}
