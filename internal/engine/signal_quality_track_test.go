// signal_quality_track_test.go — §DYN-SIZE P3 信号质量归一化层接线守护测试。
//
// 三把锁（docs/DYN_SIZE_FINAL_PLAN_20261016.md §5）：
//  1. **冷启动不变行为**：`Enhance.DynWeight` 关闭时 `QualityWeight` 恒 0（该维度弃权）；
//     开启但样本 < MinSample 时 `Weight` 恒 1.0（中性乘子）——两种情形下 `ComputeSizing`
//     与"没有这一维"逐字一致。
//  2. **结算纪律**：只用本轮已有行情结算（无报价的条目留下轮，绝不逐票打上游）；未到期的不结算；
//     到期即摘除（不重复计分）。
//  3. **分桶键**：空维度写 "-" 占位（防不同维度的空值在同一键上碰撞）；无法归因战法的不入桶。
//
// English: guard tests for the §DYN-SIZE P3 normalization wiring — cold-start neutrality, settlement
// discipline (round quotes only, no upstream fan-out), and bucket-key placeholders.
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/signalctl"
)

// newQualityEngine 构建最小引擎 + 内存配置管理器；dynWeight=Enhance.DynWeight 开关。
func newQualityEngine(t *testing.T, dynWeight bool) *Engine {
	t.Helper()
	e := &Engine{}
	cm := config.NewManager("")
	cm.Get().Enhance.DynWeight = dynWeight
	e.cfgMgr = cm
	return e
}

// qualityBuy 一条可归桶的做多买入信号。
func qualityBuy(code, tactic string) combat_agent.Signal {
	return combat_agent.Signal{
		ID: "Q-" + code, Code: code, Name: code, Strategy: tactic, StrategyType: tactic,
		Direction: "做多", Action: "buy", Price: 10, Confidence: 0.8, Sector: "AI",
		GeneratedAt: time.Now().Add(-time.Minute),
	}
}

// seedHits 给某桶喂 n 次命中（MinSample 默认 20，故 n>=23 时权重触上限 1.3）。
func seedHits(t *testing.T, e *Engine, key research.QualityBucketKey, n int, hit bool) {
	t.Helper()
	tb := e.signalQualityTable()
	if tb == nil {
		t.Fatal("DynWeight 开启时质量表应可建")
	}
	for i := 0; i < n; i++ {
		tb.Update(key, hit)
	}
}

// ── 1. 冷启动 / 关闭态：零行为变更 ──────────────────────────────────────────────

// TestQualityDisabledLeavesWeightZero 开关关闭：不解析权重、不建队列、不登记——该维度弃权。
func TestQualityDisabledLeavesWeightZero(t *testing.T) {
	e := newQualityEngine(t, false)
	sigs := []combat_agent.Signal{qualityBuy("600001", "dragon")}
	e.annotateQualityWeights(sigs)
	if sigs[0].QualityWeight != 0 {
		t.Fatalf("关闭态不得写 QualityWeight，得 %v", sigs[0].QualityWeight)
	}
	if tr := e.qualityTrackerRef(); tr != nil {
		t.Fatal("关闭态不应建回喂器")
	}
	e.observeSignalQuality(sigs, map[string]*data.StockInfo{"600001": {Price: 10}}, "发酵")
	if e.qualityTrack != nil {
		t.Fatal("关闭态不应登记任何待结算样本")
	}
}

// TestQualityColdStartNeutral 开启但无样本：Weight 恒 1.0 → 定权合成与"没有这一维"**逐字一致**。
// 断言方式说明：信号本身还有置信度（Kelly）等其它因子会动 scale，故不能直接断言 scale==1.0；
// 精确的锁是「写 1.0 的结果 == 弃权（0）的结果」——这正是"冷启动零行为变更"的定义。
func TestQualityColdStartNeutral(t *testing.T) {
	e := newQualityEngine(t, true)
	sigs := []combat_agent.Signal{qualityBuy("600002", "dragon")}
	e.annotateQualityWeights(sigs)
	if sigs[0].QualityWeight != 1.0 {
		t.Fatalf("冷启动应写中性 1.0，得 %v", sigs[0].QualityWeight)
	}
	r := signalctl.Regime{SizeCfg: config.SizingConfig{Enabled: true}.WithDefaults()}
	withDim := signalctl.ComputeSizing(r, sigs[0])
	withoutDim := sigs[0]
	withoutDim.QualityWeight = 0 // 该维度弃权（等价于功能关闭）
	absent := signalctl.ComputeSizing(r, withoutDim)
	if withDim.Scale != absent.Scale {
		t.Fatalf("冷启动 1.0 必须与弃权同值：写 1.0=%v 弃权=%v", withDim.Scale, absent.Scale)
	}
	if withDim.Downgrade || absent.Downgrade {
		t.Fatalf("冷启动不应降级：%+v / %+v", withDim, absent)
	}
}

// TestQualityWeightDrivesSizing 样本充足且命中率高 → 权重上漂（限幅内）→ 合成放大；
// 命中率差 → 权重下漂 → 合成缩小。这是"归一化层真的进了定权"的正向证据。
func TestQualityWeightDrivesSizing(t *testing.T) {
	e := newQualityEngine(t, true)
	sig := qualityBuy("600003", "dragon")
	key, _, ok := qualityBucketKey(sig, e.emotionPhaseNow())
	if !ok {
		t.Fatal("应可归桶")
	}
	seedHits(t, e, key, 30, true) // 命中率 100% ≫ 目标 0.35 → 权重触上限 1.3
	sigs := []combat_agent.Signal{sig}
	e.annotateQualityWeights(sigs)
	if sigs[0].QualityWeight <= 1.0 {
		t.Fatalf("高命中率桶应得 >1.0 的权重，得 %v", sigs[0].QualityWeight)
	}
	r := signalctl.Regime{SizeCfg: config.SizingConfig{Enabled: true}.WithDefaults()}
	res := signalctl.ComputeSizing(r, sigs[0])
	if res.Scale <= 1.0 {
		t.Fatalf("高命中率应放大合成，得 %+v", res)
	}
	if res.Scale > 1.15 {
		t.Fatalf("合成不得越过 cap=1.15，得 %v", res.Scale)
	}

	// 反向腿：命中率 0% → 权重触下限 0.7 → 合成缩小。
	e2 := newQualityEngine(t, true)
	sig2 := qualityBuy("600004", "dragon")
	key2, _, _ := qualityBucketKey(sig2, e2.emotionPhaseNow())
	seedHits(t, e2, key2, 30, false)
	sigs2 := []combat_agent.Signal{sig2}
	e2.annotateQualityWeights(sigs2)
	if sigs2[0].QualityWeight >= 1.0 {
		t.Fatalf("低命中率桶应得 <1.0 的权重，得 %v", sigs2[0].QualityWeight)
	}
	res2 := signalctl.ComputeSizing(r, sigs2[0])
	if res2.Scale >= 1.0 {
		t.Fatalf("低命中率应缩小合成，得 %+v", res2)
	}
}

// TestQualityChannelParity live/paper 同源：同一信号两次解析（模拟两通道）得同一权重。
func TestQualityChannelParity(t *testing.T) {
	e := newQualityEngine(t, true)
	sig := qualityBuy("600005", "n_shape")
	key, _, _ := qualityBucketKey(sig, e.emotionPhaseNow())
	seedHits(t, e, key, 25, true)

	live := []combat_agent.Signal{sig}
	paper := []combat_agent.Signal{sig}
	e.annotateQualityWeights(live)
	e.annotateQualityWeights(paper)
	if live[0].QualityWeight != paper[0].QualityWeight {
		t.Fatalf("通道口径应一致：live=%v paper=%v", live[0].QualityWeight, paper[0].QualityWeight)
	}
	if live[0].QualityWeight == 1.0 {
		t.Fatal("本用例应得到非中性权重（否则一致是无意义的）")
	}
}

// ── 2. 分桶键 ───────────────────────────────────────────────────────────────────

// TestQualityBucketKeyPlaceholders 空维度写 "-" 占位；无法归因战法不入桶。
func TestQualityBucketKeyPlaceholders(t *testing.T) {
	sig := combat_agent.Signal{Code: "600006", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 10}
	key, tactic, ok := qualityBucketKey(sig, "")
	if !ok || tactic != "dragon" {
		t.Fatalf("应可归桶且 tactic=dragon，得 %q %v", tactic, ok)
	}
	// 四段：dragon|-|-|-（板块空、新闻类型无、相位空）
	want := research.MakeQualityKey("dragon", "-", "-", "-")
	if key != want {
		t.Fatalf("空维度应写占位：得 %q 期望 %q", key, want)
	}
	// 无战法来源 → 不入桶。
	if _, _, ok := qualityBucketKey(combat_agent.Signal{Code: "600007"}, "发酵"); ok {
		t.Fatal("无法归因战法的信号不应入桶")
	}
}

// TestQualityNewsTypeCoarse D1 字段的粗分类：拦截=利空、有事件=事件、否则空。
func TestQualityNewsTypeCoarse(t *testing.T) {
	if got := qualityNewsType(combat_agent.Signal{D1Blocked: true, D1Event: "立案"}); got != "利空" {
		t.Fatalf("拦截应为利空，得 %q", got)
	}
	if got := qualityNewsType(combat_agent.Signal{D1Event: "中标"}); got != "事件" {
		t.Fatalf("有关联事件应为事件，得 %q", got)
	}
	if got := qualityNewsType(combat_agent.Signal{D1Score: 12}); got != "事件" {
		t.Fatalf("有 D1 分数应为事件，得 %q", got)
	}
	if got := qualityNewsType(combat_agent.Signal{}); got != "" {
		t.Fatalf("无事件应为空，得 %q", got)
	}
}

// ── 3. 登记与结算 ───────────────────────────────────────────────────────────────

// TestQualityObserveDedupAndFilters 同 战法|代码 只登记一次；卖出/做空/watch/无价不入队。
func TestQualityObserveDedupAndFilters(t *testing.T) {
	e := newQualityEngine(t, true)
	buys := []combat_agent.Signal{
		qualityBuy("600010", "dragon"),
		qualityBuy("600010", "dragon"), // 重复：同轮多轮次只算一次判断
		qualityBuy("600011", "dragon"),
		{Code: "600012", StrategyType: "dragon", Direction: "做多", Action: "sell", Price: 10},
		{Code: "600013", StrategyType: "dragon", Direction: "做空", Action: "buy", Price: 10},
		{Code: "600014", StrategyType: "dragon", Direction: "做多", Action: "watch", Price: 10},
		{Code: "600015", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 0},
	}
	e.observeSignalQuality(buys, nil, "发酵")
	tr := e.qualityTrackerRef()
	if got := tr.size(); got != 2 {
		t.Fatalf("应只登记 2 条（600010/600011），得 %d", got)
	}
}

// TestQualitySettleMaturedWithQuote 到期 + 本轮有报价 → 结算并回喂（前向收益>0 = hit），条目摘除。
func TestQualitySettleMaturedWithQuote(t *testing.T) {
	e := newQualityEngine(t, true)
	tr := e.qualityTrackerRef()
	key := research.MakeQualityKey("dragon", "AI", "-", "发酵")
	// 直接插入一条 6 天前的样本（超过 5 日前瞻窗）。
	tr.mu.Lock()
	tr.entries["dragon|600020"] = &qualityPending{Key: key, Code: "600020", EntryPrice: 10, FiredAt: time.Now().Add(-6 * 24 * time.Hour)}
	tr.mu.Unlock()

	quotes := map[string]*data.StockInfo{"600020": {Price: 11}} // +10% → hit
	e.observeSignalQuality(nil, quotes, "发酵")
	if got := tr.size(); got != 0 {
		t.Fatalf("到期且已结算的条目应摘除，余 %d", got)
	}
	if w := e.SignalQualityWeight("dragon", "AI", "-", "发酵"); w == 0 {
		t.Fatal("结算后该桶应有权重读数")
	}
}

// TestQualitySettleLeavesUnquotedPending 到期但本轮无报价 → 保留到下轮（绝不逐票打上游）。
func TestQualitySettleLeavesUnquotedPending(t *testing.T) {
	e := newQualityEngine(t, true)
	tr := e.qualityTrackerRef()
	tr.mu.Lock()
	tr.entries["dragon|600021"] = &qualityPending{Key: "k", Code: "600021", EntryPrice: 10, FiredAt: time.Now().Add(-6 * 24 * time.Hour)}
	tr.mu.Unlock()

	// 快照里没有 600021（或价格为 0）→ 不结算。
	e.observeSignalQuality(nil, map[string]*data.StockInfo{"600099": {Price: 9}}, "发酵")
	if got := tr.size(); got != 1 {
		t.Fatalf("无报价应留下轮，得 %d", got)
	}
	e.observeSignalQuality(nil, map[string]*data.StockInfo{"600021": {Price: 0}}, "发酵")
	if got := tr.size(); got != 1 {
		t.Fatalf("零价应留下轮，得 %d", got)
	}
	// 下一轮有价 → 结算。
	e.observeSignalQuality(nil, map[string]*data.StockInfo{"600021": {Price: 12}}, "发酵")
	if got := tr.size(); got != 0 {
		t.Fatalf("有价应结算，余 %d", got)
	}
}

// TestQualitySettleIgnoresImmature 未到前瞻窗的样本即使有报价也不结算（防"当天买当天算"）。
func TestQualitySettleIgnoresImmature(t *testing.T) {
	e := newQualityEngine(t, true)
	tr := e.qualityTrackerRef()
	tr.mu.Lock()
	tr.entries["dragon|600022"] = &qualityPending{Key: "k", Code: "600022", EntryPrice: 10, FiredAt: time.Now()}
	tr.mu.Unlock()
	e.observeSignalQuality(nil, map[string]*data.StockInfo{"600022": {Price: 12}}, "发酵")
	if got := tr.size(); got != 1 {
		t.Fatalf("未到期不应结算，得 %d", got)
	}
}

// TestQualityZeroFiredAtGetsStamped GeneratedAt 零值的信号用当前时刻兜底登记——
// 否则会被 matured 判为"早就到期"，立刻用当前价结算出 ≈0 的假收益，污染该桶命中率。
func TestQualityZeroFiredAtGetsStamped(t *testing.T) {
	e := newQualityEngine(t, true)
	sig := qualityBuy("600023", "dragon")
	sig.GeneratedAt = time.Time{}
	e.observeSignalQuality([]combat_agent.Signal{sig}, nil, "发酵")
	tr := e.qualityTrackerRef()
	if got := tr.size(); got != 1 {
		t.Fatalf("应登记 1 条，得 %d", got)
	}
	tr.mu.Lock()
	fired := tr.entries["dragon|600023"].FiredAt
	tr.mu.Unlock()
	if fired.IsZero() {
		t.Fatal("零值 GeneratedAt 应被当前时刻兜底，不得留零")
	}
	// 立刻带报价结算：不应被结算（未到期）。
	e.observeSignalQuality(nil, map[string]*data.StockInfo{"600023": {Price: 12}}, "发酵")
	if got := tr.size(); got != 1 {
		t.Fatalf("刚登记的不应被立刻结算，得 %d", got)
	}
}

// TestQualityQueueBounded 队列上限：满则拒收新样本（旧样本已快到期，不淘汰）。
func TestQualityQueueBounded(t *testing.T) {
	tr := newSignalQualityTracker(time.Hour)
	tr.max = 2
	mk := func(code string) combat_agent.Signal {
		s := qualityBuy(code, "dragon")
		return s
	}
	n := tr.observe([]combat_agent.Signal{mk("1"), mk("2"), mk("3")}, "发酵", time.Now())
	if n != 2 || tr.size() != 2 {
		t.Fatalf("队列上限 2 应只收 2 条，得 n=%d size=%d", n, tr.size())
	}
}

// TestQualityQuoteForSuffix 快照键为纯码：带后缀的信号代码也能取到报价。
func TestQualityQuoteForSuffix(t *testing.T) {
	quotes := map[string]*data.StockInfo{"600030": {Price: 10}}
	if q := quoteFor(quotes, "600030.SH"); q == nil || q.Price != 10 {
		t.Fatal("带后缀代码应回退纯码取值")
	}
	if q := quoteFor(quotes, "600031.SH"); q != nil {
		t.Fatal("不存在的代码应返回 nil")
	}
}
