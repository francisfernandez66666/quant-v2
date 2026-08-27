// 实速模拟（real-speed rehearsal）基础设施：
// 在现役 mock 网络与 mock LLM 之上叠加可调的真实时延注入 + 分项计量，
// 让 engine.Run 全链路以接近实盘的速度"彩排"一遍，逐环节给出真实消耗时间。
//
// 时延基线与生产观测对齐：
//   - 行情/板块接口 RTT    约 150~500ms（新浪/东财 push2 外网往返）
//   - 新闻源 RTT           约 120~300ms（财联社/同花顺快讯）
//   - LLM 首 token         约 1.5~3s（SiliconFlow GLM-Z1-9B 排队+首段）
//   - LLM 结果回传:token    按 ~45 tok/s 流式速率折算
//
// 测试默认把注入时延长压缩到 Scale≈0.02（秒级跑完、仍保留阶段关系），报告按 1× 外推
// "实盘一轮真实消耗"；显式传 Scale=1.0（或设 E2E_REALSPEED=1）走 1× 卡时间彩排。
package e2e

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LatencyProfile 采集到并用于注入的真实网络/LLM 延迟基线。
type LatencyProfile struct {
	// 数据回传速度：实时行情/资金流/涨停池等（指数、实时价、K线、资金流、板块行情、成分股、新股日历、龙虎榜）。
	// 行情回传时延
	Quote time.Duration
	// 数据回传速度：新闻来源（财联社/同花顺/新浪滚动/文章正文）。
	// 新闻回传时延
	News time.Duration
	// 数据回传速度：板块名单/成分股（同花顺板块页、全市场列表）。
	// 板块回传时延
	Board time.Duration

	// LLM 调用速度：请求到达服务端 + 排队 + 首 token（不含正文传输）。
	// LLM 首 token 时延
	LLMFirstToken time.Duration
	// LLM 结果回传速度：流式 token 速率（每 token 耗时，~45 tok/s → ~22ms/token）。
	// LLM 每 token 时延
	LLMPerToken time.Duration

	// 各类 LLM 正文体积（token），用于按 LLMPerToken 折算回传时长。
	// Stage0 token 数
	Stage0Tokens int
	// Stage2 token 数
	Stage2Tokens int
	// D1 token 数
	D1Tokens     int

	// Jitter 抖动比例(0~1)：实际延迟 ∈ base×(1, 1+Jitter)，模拟网络抖动。
	// 抖动系数
	Jitter float64
	// ScaleFactor 时间缩放系数：默认 0.02 秒级跑完；1.0=真实彩排卡时间。
	// 整体缩放系数
	ScaleFactor float64

	rng *rand.Rand
	mu  sync.Mutex
}

// realisticProfile 返回一组对齐实盘观测的延迟基线。
func realisticProfile() *LatencyProfile {
	return &LatencyProfile{
		Quote:         200 * time.Millisecond,
		News:          150 * time.Millisecond,
		Board:         250 * time.Millisecond,
		LLMFirstToken: 2200 * time.Millisecond,
		LLMPerToken:   time.Second / 45,
		Stage0Tokens:  30,
		Stage2Tokens:  90,
		D1Tokens:      60,
		Jitter:        0.10,
		ScaleFactor:   0.02,
	}
}

// speed 缩放+抖动后的单次注入耗时；profile 或注入无效时返回 0（不阻塞）。
func (p *LatencyProfile) speed(base time.Duration) time.Duration {
	if p == nil || base <= 0 || p.ScaleFactor <= 0 {
		return 0
	}
	p.mu.Lock()
	if p.rng == nil {
		p.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	f := 1 + p.rng.Float64()*p.Jitter
	p.mu.Unlock()
	return time.Duration(float64(base) * f * p.ScaleFactor)
}

// llmDuration 计算一次 LLM 调用的完整墙钟：首 token + 正文回传(按 token 速率)。
func (p *LatencyProfile) llmDuration(tokens int) time.Duration {
	first := p.speed(p.LLMFirstToken)
	body := p.speed(p.LLMPerToken) * time.Duration(maxI(tokens, 0))
	return first + body
}

// maxI 返回两整数较大者（LLM token 数下限保护）。
func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// simMetrics 计量注入时延的真实消耗，按数据类别与 LLM 阶段分别累计。
// 全部字段为取墙钟测量值；当 ScaleFactor<1 时，报告按 1/ScaleFactor 外推 1×。
type simMetrics struct {
	mu sync.Mutex

	quoteMs time.Duration
	quoteN  int
	newsMs  time.Duration
	newsN   int
	boardMs time.Duration
	boardN  int

	stage0Ms time.Duration
	stage0N  int
	stage2Ms time.Duration
	stage2N  int
	d1Ms     time.Duration
	d1N      int
	otherMs  time.Duration
	otherN   int
}

func (m *simMetrics) addQuote(d time.Duration) {
	m.mu.Lock()
	m.quoteMs += d
	m.quoteN++
	m.mu.Unlock()
}

// addNews 记一笔新闻源注入时延（线程安全）。
func (m *simMetrics) addNews(d time.Duration) { m.mu.Lock(); m.newsMs += d; m.newsN++; m.mu.Unlock() }

// addBoard 记一笔板块行情注入时延。
func (m *simMetrics) addBoard(d time.Duration) {
	m.mu.Lock()
	m.boardMs += d
	m.boardN++
	m.mu.Unlock()
}

// addStage0 记一笔 Stage0 合并分类注入时延。
func (m *simMetrics) addStage0(d time.Duration) {
	m.mu.Lock()
	m.stage0Ms += d
	m.stage0N++
	m.mu.Unlock()
}

// addStage2 记一笔 Stage2 深度分析注入时延。
func (m *simMetrics) addStage2(d time.Duration) {
	m.mu.Lock()
	m.stage2Ms += d
	m.stage2N++
	m.mu.Unlock()
}

// addD1 记一笔 D1 评分注入时延。
func (m *simMetrics) addD1(d time.Duration) { m.mu.Lock(); m.d1Ms += d; m.d1N++; m.mu.Unlock() }

// addOther 记一笔其余环节（触发/聚合等）注入时延。
func (m *simMetrics) addOther(d time.Duration) {
	m.mu.Lock()
	m.otherMs += d
	m.otherN++
	m.mu.Unlock()
}

// tabData 汇总数据回传分项：(名称, 调用次数, 单次1×耗时, 累计1×耗时)。
func (m *simMetrics) tabData(p *LatencyProfile) [][4]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	one := oneXBase(p)
	type row struct {
		name string
		n    int
		lat  time.Duration
	}
	rows := []row{
		{"行情/指数/涨停池", m.quoteN, p.Quote},
		{"新闻源(财联社/快讯)", m.newsN, p.News},
		{"板块名单/成分股", m.boardN, p.Board},
		{"其他(mock兜底/正文)", m.otherN, 10 * time.Millisecond},
	}
	out := make([][4]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, [4]string{r.name, fmtCount(r.n), fmtDurMS(one.speed(r.lat)), fmtDurMS(time.Duration(r.n) * one.speed(r.lat))})
	}
	return out
}

// tabLLM 汇总 LLM 阶段分项：(名称, 调用次数, 单次1×耗时, 累计1×耗时)。
func (m *simMetrics) tabLLM(p *LatencyProfile) [][4]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	one := oneXBase(p)
	type row struct {
		name   string
		n      int
		tokens int
	}
	rows := []row{
		{"Stage0 质检/价值判断", m.stage0N, p.Stage0Tokens},
		{"Stage2 深度分析", m.stage2N, p.Stage2Tokens},
		{"D1 批量评分", m.d1N, p.D1Tokens},
		{"咨询(其他)", m.otherN, 20},
	}
	out := make([][4]string, 0, len(rows))
	for _, r := range rows {
		single := one.llmDuration(r.tokens)
		out = append(out, [4]string{r.name, fmtCount(r.n), fmtDurMS(single), fmtDurMS(single * time.Duration(r.n))})
	}
	return out
}

// oneXBase 返回 1× 均抖口径（去掉 Scale/Jitter 波动项用于口径统一外推）。
func oneXBase(p *LatencyProfile) *LatencyProfile {
	return &LatencyProfile{
		Quote:         p.Quote,
		News:          p.News,
		Board:         p.Board,
		LLMFirstToken: p.LLMFirstToken,
		LLMPerToken:   p.LLMPerToken,
		Stage0Tokens:  p.Stage0Tokens,
		Stage2Tokens:  p.Stage2Tokens,
		D1Tokens:      p.D1Tokens,
		Jitter:        p.Jitter / 2,
		ScaleFactor:   1,
	}
}

// fmtDurMS 格式化为毫秒表项。
func fmtDurMS(d time.Duration) string {
	return fmtMs(d.Milliseconds())
}

// fmtMs 毫秒数格式化为可读文本：≥1s 显示为秒，否则显示为毫秒。
func fmtMs(ms int64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.2fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}

// fmtCount 调用次数格式化为十进制字符串。
func fmtCount(n int) string {
	return strconv.Itoa(n)
}

// latencyTransport 包装 fixtureTransport：按路由类别注入网络回传时延，计名校验后转发真响应。
type latencyTransport struct {
	fix     *fixtureTransport
	profile *LatencyProfile
	metrics *simMetrics
}

// RoundTrip 按路由类别（新闻/板块/行情）注入网络回传时延并计量分项耗时。
func (l *latencyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()

	// 路由类别 → 注入基线：新闻源 / 板块名单 / 其余行情。
	// 顺序重要：q.10jqka(板块页) 与 news.10jqka(快讯) 需分开判定。
	var cat string
	var lat time.Duration
	switch {
	case host == "q.10jqka.com.cn" || strings.Contains(host, "vip.stock.finance.sina.com.cn"):
		cat, lat = "board", l.profile.Board
	case strings.Contains(host, "cls.cn") ||
		host == "news.10jqka.com.cn" ||
		strings.Contains(host, "feed.mix.sina") ||
		host == "finance.sina.com.cn":
		cat, lat = "news", l.profile.News
	default:
		cat, lat = "quote", l.profile.Quote
	}

	start := time.Now()
	time.Sleep(l.profile.speed(lat))
	resp, err := l.fix.RoundTrip(req)
	elapsed := time.Since(start)

	switch cat {
	case "news":
		l.metrics.addNews(elapsed)
	case "board":
		l.metrics.addBoard(elapsed)
	default:
		l.metrics.addQuote(elapsed)
	}
	return resp, err
}

// newLatencyLLM returns an httptest server with per-stage injected latency for the rehearsal.
// 复用 mock.go 的确定性响应逻辑。
func newLatencyLLM(profile *LatencyProfile, metrics *simMetrics) (*httptest.Server, *llmCalls) {
	calls := &llmCalls{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var system, user string
		for _, m := range req.Messages {
			if m.Role == "system" {
				system += m.Content + "\n"
			}
			if m.Role == "user" && user == "" {
				user = m.Content
			}
		}

		var content string
		var stage int
		switch {
		case strings.Contains(system, "股票投资顾问"):
			calls.record("consult", system)
			content = "已收到咨询。"
			metrics.addOther(profile.llmDuration(20))
		case strings.Contains(system, "质检与价值判断"):
			calls.record("stage0", user)
			content = mockStage0JSON(user)
			stage = profile.Stage0Tokens
			metrics.addStage0(profile.llmDuration(stage))
		case strings.Contains(system, "D1事件评分"):
			calls.record("d1", user)
			if calls.IsFailD1() {
				http.Error(w, "mock D1 failure", 500)
				return
			}
			content = mockD1JSON(user)
			stage = profile.D1Tokens
			metrics.addD1(profile.llmDuration(stage))
		case strings.Contains(system, "热点分析专家"):
			calls.record("stage2", user)
			content = mockStage2JSON(user)
			stage = profile.Stage2Tokens
			metrics.addStage2(profile.llmDuration(stage))
		default:
			content = "[]"
			metrics.addOther(0)
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"role": "assistant", "content": content}},
			},
		})
	}))
	return srv, calls
}
