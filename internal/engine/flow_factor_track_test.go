// flow_factor_track_test.go — §FLOW-FACTOR 引擎侧挂载层守护测试
// （2026-10-25，docs/MODPLAN_UAT_FLOWFACTOR_20261025.md §2.3 + R3/R8/R9）。
//
// 钉死五类语义：
//  1. 当日比值只用本轮快照（HasFlow/流通市值两道闸；0=合法读数与缺失可区分）；
//  2. R3 日期断言（末行≠本交易日 → 整体弃权）；
//  3. (code, 交易日) 缓存：TTL 内不重复取数、跨日/超时必重取、失败负缓存；
//  4. 挂载只认买入信号；两侧开关全关 → 零操作零取数；
//  5. 缺数纪律：任何一环不可得 → 比值保持 0（弃权），绝不编造。
package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
)

// flowQuote 一票带资金流与流通市值的快照（600519 形制的缩小版）。
// English: a quote carrying flow + float cap.
func flowQuote(price, netInflow, floatShares float64, hasFlow bool) *data.StockInfo {
	return &data.StockInfo{
		Price: price, NetInflow: netInflow, HasFlow: hasFlow,
		FloatShares: floatShares, FloatMarketCap: floatShares * price,
	}
}

// flowDays 构造 n 行历史（末行日期=today，日期从 today 往前推自然日近似——测试只验归一化比较）。
// English: builds n history rows whose last row carries today's date.
func flowDays(n int, today string, mainNet float64) []data.FlowDay {
	out := make([]data.FlowDay, 0, n)
	base, err := time.Parse("20060102", today)
	if err != nil {
		base = time.Now()
	}
	for i := n - 1; i >= 0; i-- {
		d := base.AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, data.FlowDay{Date: d, MainNet: mainNet})
	}
	return out
}

// ── 1. flowTodayRatio：当日比值的两道闸 ─────────────────────────────────────────

// TestFlowTodayRatioGates 当日比值：无报价 / 源未返回（HasFlow=false）/ 无流通市值 → 弃权；
// 合法 0 读数（HasFlow=true, NetInflow=0）返回 0.0 而非弃权原因（压线由档位映射处理）。
// English: the same-day ratio's two gates; a legal zero reading is not a missing one.
func TestFlowTodayRatioGates(t *testing.T) {
	cases := []struct {
		name    string
		q       *data.StockInfo
		want    float64
		wantWhy string
	}{
		{"无报价", nil, 0, "no_quote"},
		{"无价格", &data.StockInfo{NetInflow: 1e6, HasFlow: true, FloatShares: 1e8}, 0, "no_quote"},
		{"源未返回资金流", flowQuote(10, 1e6, 1e8, false), 0, "no_flow_today"},
		{"无流通市值", &data.StockInfo{Price: 10, NetInflow: 1e6, HasFlow: true}, 0, "no_floatcap"},
		{"合法0读数", flowQuote(10, 0, 1e8, true), 0, ""},
		{"直取流通市值", flowQuote(10, 1e6, 1e8, true), 0.1, ""}, // 1e6/1e9×100
		{"换手率反推流通市值", &data.StockInfo{
			Price: 20, NetInflow: 2e6, HasFlow: true, Volume: 1e6, Turnover: 1, // 1e6/(1%)=1e8 股
		}, 0.1, ""},
	}
	for _, c := range cases {
		got, why := flowTodayRatio(c.q)
		if got != c.want || why != c.wantWhy {
			t.Errorf("%s: flowTodayRatio = (%v,%q), want (%v,%q)", c.name, got, why, c.want, c.wantWhy)
		}
	}
}

// ── 2. R3 日期断言 + 归一化 ─────────────────────────────────────────────────────

// TestFlowHistFresh 末行必须等于本交易日（容忍 "-" 分隔符）；空序列/零 today 一律不新鲜。
// English: the newest row must equal today's trading date (dash-tolerant).
func TestFlowHistFresh(t *testing.T) {
	today := "20261025"
	cases := []struct {
		name string
		days []data.FlowDay
		want bool
	}{
		{"空序列", nil, false},
		{"末行当天(带横线)", []data.FlowDay{{Date: "2026-10-24"}, {Date: "2026-10-25"}}, true},
		{"末行当天(裸串)", []data.FlowDay{{Date: "2026-10-25"}}, true},
		{"末行昨天", []data.FlowDay{{Date: "2026-10-23"}, {Date: "2026-10-24"}}, false},
		{"末行下一天(未来)", []data.FlowDay{{Date: "2026-10-26"}}, false},
		{"末行空日期", []data.FlowDay{{Date: "  "}}, false},
	}
	for _, c := range cases {
		if got := flowHistFresh(c.days, today); got != c.want {
			t.Errorf("%s: flowHistFresh = %v, want %v", c.name, got, c.want)
		}
	}
	if flowHistFresh([]data.FlowDay{{Date: "2026-10-25"}}, "") {
		t.Error("today 空串不得判新鲜")
	}
	if got := normFlowDate(" 2026-10-16 "); got != "20261016" {
		t.Errorf("normFlowDate = %q, want 20261016", got)
	}
}

// ── 3. (code, 交易日) 缓存：TTL / 跨日 / 负缓存 / 三道闸 ─────────────────────────

// countingFetch 统计取数次数的假 fetch。
// English: a fetch stub that counts calls.
func countingFetch(days []data.FlowDay, err error, n *int) func() ([]data.FlowDay, error) {
	return func() ([]data.FlowDay, error) {
		*n++
		return days, err
	}
}

// TestFlowTrackerCacheTTL TTL 内不重复取数；超时/跨日必重取。
// English: within the TTL the fetch runs once; expiry or a new day re-fetches.
func TestFlowTrackerCacheTTL(t *testing.T) {
	tr := newFlowFactorTracker()
	day := "20261025"
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	n := 0
	fetch := countingFetch(flowDays(5, day, 1e6), nil, &n)

	if _, ok, why := tr.fiveDaySum("600519", day, fetch, now); !ok || why != "" {
		t.Fatalf("首次应命中，得 ok=%v why=%q", ok, why)
	}
	if n != 1 {
		t.Fatalf("首次应取数 1 次，得 %d", n)
	}
	// 5 分钟后：TTL 内 → 缓存命中，不取数。
	if _, ok, _ := tr.fiveDaySum("600519", day, fetch, now.Add(5*time.Minute)); !ok || n != 1 {
		t.Fatalf("TTL 内应命中缓存（取数 %d 次）", n)
	}
	// 11 分钟后：超 TTL → 重取。
	if _, ok, _ := tr.fiveDaySum("600519", day, fetch, now.Add(11*time.Minute)); !ok || n != 2 {
		t.Fatalf("超 TTL 应重取（共 %d 次）", n)
	}
	// 跨交易日：必重取（盘中序列追加了新行）——用当日新夹具证明"重取且过新鲜度闸"。
	n2 := 0
	fetch2 := countingFetch(flowDays(5, "20261026", 1e6), nil, &n2)
	if _, ok, _ := tr.fiveDaySum("600519", "20261026", fetch2, now.Add(11*time.Minute)); !ok || n2 != 1 {
		t.Fatalf("跨日应重取并过新鲜度闸（新夹具取数 %d 次）", n2)
	}
	// 另一只票：独立键（此前原码共取数 2 次，加本次 = 3）。
	if _, ok, _ := tr.fiveDaySum("600001", day, fetch, now); !ok || n != 3 {
		t.Fatalf("异码应独立取数（共 %d 次）", n)
	}
}

// TestFlowTrackerNegativeCache 失败负缓存 60s：上游抖动不每轮硬打；超时后再试。
// English: failures are negatively cached for 60s, then retried.
func TestFlowTrackerNegativeCache(t *testing.T) {
	tr := newFlowFactorTracker()
	day := "20261025"
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	n := 0
	fetch := countingFetch(nil, fmt.Errorf("boom"), &n)

	if _, ok, why := tr.fiveDaySum("600519", day, fetch, now); ok || why != "hist_fail" {
		t.Fatalf("失败应弃权 hist_fail，得 ok=%v why=%q", ok, why)
	}
	tr.fiveDaySum("600519", day, fetch, now.Add(30*time.Second)) // 负缓存内
	if n != 1 {
		t.Fatalf("负缓存 60s 内不应重试，取数 %d 次", n)
	}
	tr.fiveDaySum("600519", day, fetch, now.Add(90*time.Second)) // 负缓存过
	if n != 2 {
		t.Fatalf("负缓存超时应重试，取数 %d 次", n)
	}
}

// TestFlowTrackerSameDayLKG §DATA-RELIABILITY（2026-10-26）：东财日线历史单源熔断时，
// 同一交易日内回退最近一次成功读数（30 分钟窗口），不让 5 日净流入因子整段失明；
// 跨日/超窗必须回到弃权，R3 日期断言不被 LKG 绕过。
func TestFlowTrackerSameDayLKG(t *testing.T) {
	tr := newFlowFactorTracker()
	day := "20261025"
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	nGood := 0
	good := countingFetch(flowDays(5, day, 1e6), nil, &nGood)
	if sum, ok, why := tr.fiveDaySum("600519", day, good, now); !ok || why != "" || sum != 5e6 {
		t.Fatalf("首次成功应得 5e6，got sum=%v ok=%v why=%q", sum, ok, why)
	}

	// 11 分钟后上游失败：应回退同日 LKG，而不是 hist_fail。
	nBad := 0
	bad := countingFetch(nil, fmt.Errorf("eastmoney down"), &nBad)
	sum, ok, why := tr.fiveDaySum("600519", day, bad, now.Add(11*time.Minute))
	if !ok || why != "" || sum != 5e6 {
		t.Fatalf("失败应回退同日 LKG=5e6，got sum=%v ok=%v why=%q", sum, ok, why)
	}
	// 负缓存 60s 内不再打上游，仍返回 LKG。
	if sum, ok, why := tr.fiveDaySum("600519", day, bad, now.Add(11*time.Minute+30*time.Second)); !ok || why != "" || sum != 5e6 || nBad != 1 {
		t.Fatalf("负缓存内应复用 LKG 且不重取，got sum=%v ok=%v why=%q fetches=%d", sum, ok, why, nBad)
	}
	// 超过 30 分钟 LKG 窗口：必须弃权，不能拿过期读数继续用。
	if _, ok, why := tr.fiveDaySum("600519", day, bad, now.Add(31*time.Minute)); ok || why != "hist_fail" {
		t.Fatalf("LKG 超窗应弃权 hist_fail，got ok=%v why=%q", ok, why)
	}
	// 跨交易日：不得复用前一日 LKG（R3 日期断言优先）。
	if _, ok, why := tr.fiveDaySum("600519", "20261026", bad, now.Add(32*time.Minute)); ok || why != "hist_fail" {
		t.Fatalf("跨日不得复用 LKG，got ok=%v why=%q", ok, why)
	}
}

// TestFlowTrackerGates 三道闸：取数失败 / 末行陈旧（R3）/ 行数不足 → 各自弃权且原因可归因。
// English: the three gates — fetch failure, stale last row (R3), too few rows.
func TestFlowTrackerGates(t *testing.T) {
	tr := newFlowFactorTracker()
	day := "20261025"
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)

	// R3 陈旧：末行是昨天。
	stale := []data.FlowDay{{Date: "2026-10-24", MainNet: 1e6}, {Date: "2026-10-24", MainNet: 1e6}}
	if _, ok, why := tr.fiveDaySum("600001", day, countingFetch(stale, nil, new(int)), now); ok || why != "hist_stale" {
		t.Fatalf("陈旧序列应弃权 hist_stale，得 ok=%v why=%q", ok, why)
	}
	// 行数不足：只有当日一行（累计恒等于当日 → 双倍权重，弃权）。
	short := []data.FlowDay{{Date: "2026-10-25", MainNet: 1e6}}
	if _, ok, why := tr.fiveDaySum("600002", day, countingFetch(short, nil, new(int)), now); ok || why != "hist_short" {
		t.Fatalf("单行应弃权 hist_short，得 ok=%v why=%q", ok, why)
	}
	// 正常：当日 + 前 1 日即可（新股票至少两行）。
	two := []data.FlowDay{{Date: "2026-10-24", MainNet: 2e6}, {Date: "2026-10-25", MainNet: 3e6}}
	sum, ok, why := tr.fiveDaySum("600003", day, countingFetch(two, nil, new(int)), now)
	if !ok || why != "" || sum != 5e6 {
		t.Fatalf("两行应累计 5e6，得 sum=%v ok=%v why=%q", sum, ok, why)
	}
	// nil fetch（无协调器）→ 弃权而非恐慌。
	if _, ok, why := tr.fiveDaySum("600004", day, nil, now); ok || why != "no_source" {
		t.Fatalf("nil fetch 应弃权 no_source，得 ok=%v why=%q", ok, why)
	}
}

// TestFlowTrackerCacheCap 缓存条数上限：超限不缓存（不取数豁免），但取值仍正确。
// English: beyond the cap entries are not cached but values stay correct.
func TestFlowTrackerCacheCap(t *testing.T) {
	tr := newFlowFactorTracker()
	day := "20261025"
	now := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	n := 0
	fetch := countingFetch(flowDays(5, day, 1e6), nil, &n)
	for i := 0; i < flowHistMaxEntries+5; i++ {
		tr.fiveDaySum(fmt.Sprintf("60%04d", i), day, fetch, now)
	}
	if n != flowHistMaxEntries+5 {
		t.Fatalf("每码都应取数一次，得 %d", n)
	}
	if len(tr.hist) != flowHistMaxEntries {
		t.Fatalf("缓存应封顶 %d，得 %d", flowHistMaxEntries, len(tr.hist))
	}
}

// ── 4. annotateFlowFactors：挂载只认买入 + 两侧全关零操作 ─────────────────────────

// flowBuySig 一条做多买入信号。
func flowBuySig(code string) combat_agent.Signal {
	return combat_agent.Signal{ID: "F-" + code, Code: code, Name: code, Direction: "做多", Action: "buy", Price: 10}
}

// TestAnnotateFlowFactorsBuyOnly 只挂买入：当日比值从本轮快照解析，5 日因无协调器弃权（0）。
// English: only buys are annotated; the same-day ratio comes from the round's quote.
func TestAnnotateFlowFactorsBuyOnly(t *testing.T) {
	e := &Engine{} // 无 cfgMgr → 出厂默认（因子开）；无 coordinator → 5 日弃权
	quotes := map[string]*data.StockInfo{
		"600001": flowQuote(10, 1e6, 1e8, true), // 当日 0.1%
		"600002": flowQuote(10, 5e6, 1e8, true), // 当日 0.5%
	}
	sigs := []combat_agent.Signal{
		flowBuySig("600001"),
		flowBuySig("600002"),
		{Code: "600003", Direction: "做空", Action: "sell"}, // 非买入：不挂不取数
	}
	e.annotateFlowFactors(sigs, quotes)
	if sigs[0].FlowRatioToday != 0.1 || sigs[1].FlowRatioToday != 0.5 {
		t.Fatalf("当日比值应挂到买入信号：%v/%v", sigs[0].FlowRatioToday, sigs[1].FlowRatioToday)
	}
	if sigs[0].FlowRatio5D != 0 || sigs[1].FlowRatio5D != 0 {
		t.Fatalf("无协调器时 5 日维应弃权（0），得 %v/%v", sigs[0].FlowRatio5D, sigs[1].FlowRatio5D)
	}
	if sigs[2].FlowRatioToday != 0 || sigs[2].FlowRatio5D != 0 {
		t.Fatal("非买入信号不得挂载")
	}
	// 无本轮报价 → 两维全弃权（缺数不当 0 的反面：保持 0=弃权，不编造）。
	sigs2 := []combat_agent.Signal{flowBuySig("600999")}
	e.annotateFlowFactors(sigs2, quotes)
	if sigs2[0].FlowRatioToday != 0 || sigs2[0].FlowRatio5D != 0 {
		t.Fatal("无报价信号必须两维弃权")
	}
}

// TestAnnotateFlowFactorsGateOff 两侧开关全关 → 零操作（一个比值都不挂）。
// English: both switches off → a complete no-op.
func TestAnnotateFlowFactorsGateOff(t *testing.T) {
	e := &Engine{}
	cm := config.NewManager("")
	off := false
	cm.Get().Sizing.FlowFactor.Enabled = &off
	cm.Get().Sizing.FlowFactor.SellEnabled = &off
	e.cfgMgr = cm
	quotes := map[string]*data.StockInfo{"600001": flowQuote(10, 1e6, 1e8, true)}
	sigs := []combat_agent.Signal{flowBuySig("600001")}
	e.annotateFlowFactors(sigs, quotes)
	if sigs[0].FlowRatioToday != 0 {
		t.Fatalf("两侧全关不得挂载，得 %v", sigs[0].FlowRatioToday)
	}
}

// TestFlowRatiosForSellPath 卖出裁决路径的薄封装：当日比值同源解析，5 日无协调器弃权。
// English: the sell-path wrapper resolves the same-day ratio identically.
func TestFlowRatiosForSellPath(t *testing.T) {
	e := &Engine{}
	q := flowQuote(20, 4e6, 1e8, true) // 4e6/2e9×100 = 0.2%
	today, fiveD := e.flowRatiosFor("600001", q, time.Now())
	if today != 0.2 {
		t.Fatalf("卖出路径当日比值 = %v, want 0.2", today)
	}
	if fiveD != 0 {
		t.Fatalf("无协调器 5 日应弃权，得 %v", fiveD)
	}
	// 卖出侧开关关（账号级）不改变挂载层行为——消费侧（flowSellWeight）自己把关。
	// 这里只验证 nil 行情（停牌轮）两维全弃权。
	t2, f2 := e.flowRatiosFor("600002", nil, time.Now())
	if t2 != 0 || f2 != 0 {
		t.Fatalf("nil 行情必须两维弃权，得 %v/%v", t2, f2)
	}
}

// ── 5. R9 弃权率告警（opslog 阈值）─────────────────────────────────────────────

// flowAlertRecorder 把 R9 告警收进切片（替代 opslog.Logf 的文件落盘），返回读取闭包。
// English: captures R9 alerts in memory instead of writing the opslog journal.
func flowAlertRecorder(e *Engine) func() []string {
	var lines []string
	e.flowAlertf = func(tag string, format string, args ...any) {
		lines = append(lines, tag+"|"+fmt.Sprintf(format, args...))
	}
	return func() []string { return lines }
}

// countDim 数某个维度（"当日"/"5日"）的告警条数——两个维度各自独立判定，混着数会看不出
// 是哪一维越线。
// English: counts alerts per dimension.
func countDim(lines []string, dim string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "维度="+dim) {
			n++
		}
	}
	return n
}

// TestFlowAbstainAlertThreshold 压线即报 / 未压线不报 / 同键节流 / 负数关闭 四态。
//
// 口径：4 买 1 弃权 = 25% < 50% 不报；2 买 1 弃权 = 50% 压线必报（阈值是"达到即报"，
// 不是"超过才报"——运维写 50 的意思就是"一半拿不到数就叫我"）；同一原因分布 30 分钟内
// 只留一条（弃权是持续态，不节流会把日刊写成墙）；阈值配负数=只留每轮诊断行。
// English: threshold crossing (>=, not >), throttle per reason histogram, negative disables.
func TestFlowAbstainAlertThreshold(t *testing.T) {
	// ① 未压线：4 条买入里 1 条无报价 = 25%
	e := &Engine{}
	lines := flowAlertRecorder(e)
	quotes := map[string]*data.StockInfo{
		"600001": flowQuote(10, 1e6, 1e8, true),
		"600002": flowQuote(10, 1e6, 1e8, true),
		"600003": flowQuote(10, 1e6, 1e8, true),
	}
	sigs := []combat_agent.Signal{flowBuySig("600001"), flowBuySig("600002"), flowBuySig("600003"), flowBuySig("600009")}
	e.annotateFlowFactors(sigs, quotes)
	if got := countDim(lines(), "当日"); got != 0 {
		t.Fatalf("25%% 弃权不应告警，得 %d 条：%v", got, lines())
	}

	// ② 压线：2 条买入里 1 条无报价 = 50%（阈值达到即报）
	e2 := &Engine{}
	lines2 := flowAlertRecorder(e2)
	sigs2 := []combat_agent.Signal{flowBuySig("600001"), flowBuySig("600009")}
	e2.annotateFlowFactors(sigs2, quotes)
	if got := countDim(lines2(), "当日"); got != 1 {
		t.Fatalf("50%% 弃权必须告警 1 条，得 %d 条：%v", got, lines2())
	}
	hit := lines2()[0]
	for _, want := range []string{"FLOWFACTOR-ALERT", "弃权率=50.0%", "阈值=50.0%", "no_quote=1"} {
		if !strings.Contains(hit, want) {
			t.Errorf("告警文案缺 %q：%s", want, hit)
		}
	}

	// ③ 同键节流：同一原因分布再来一轮，不新增告警
	e2.annotateFlowFactors([]combat_agent.Signal{flowBuySig("600001"), flowBuySig("600009")}, quotes)
	if got := countDim(lines2(), "当日"); got != 1 {
		t.Fatalf("同键应节流（仍 1 条），得 %d 条", got)
	}

	// ④ 负数=关闭：同样输入一条都不写
	e3 := &Engine{}
	lines3 := flowAlertRecorder(e3)
	cm := config.NewManager("")
	cm.Get().Sizing.FlowFactor.AbstainAlertPct = -1
	e3.cfgMgr = cm
	e3.annotateFlowFactors([]combat_agent.Signal{flowBuySig("600001"), flowBuySig("600009")}, quotes)
	if got := len(lines3()); got != 0 {
		t.Fatalf("阈值负数应关闭告警，得 %d 条：%v", got, lines3())
	}
}

// TestFlowAbstainAlertCustomThreshold 自定义阈值 100%：只有全军覆没才报（"一个都拿不到"）。
// English: a custom 100% threshold only fires when every signal abstains.
func TestFlowAbstainAlertCustomThreshold(t *testing.T) {
	e := &Engine{}
	lines := flowAlertRecorder(e)
	cm := config.NewManager("")
	cm.Get().Sizing.FlowFactor.AbstainAlertPct = 100
	e.cfgMgr = cm
	quotes := map[string]*data.StockInfo{"600001": flowQuote(10, 1e6, 1e8, true)}
	// 一半弃权（50%）< 100% → 不报
	e.annotateFlowFactors([]combat_agent.Signal{flowBuySig("600001"), flowBuySig("600009")}, quotes)
	if got := countDim(lines(), "当日"); got != 0 {
		t.Fatalf("50%% < 100%% 不应告警，得 %d 条", got)
	}
	// 全弃权（100%）→ 报
	e.annotateFlowFactors([]combat_agent.Signal{flowBuySig("600009")}, quotes)
	if got := countDim(lines(), "当日"); got != 1 {
		t.Fatalf("100%% 弃权必须告警，得 %d 条：%v", got, lines())
	}
}
