// signal_quality_track.go — §DYN-SIZE P3 信号质量归一化层的**结果回喂**端（docs/DYN_SIZE_FINAL_PLAN_20261016.md §2.1）。
//
// 背景：`internal/research/signal_quality.go` 的 `SignalQualityTable`（按 战法×板块×新闻类型×情绪相位
// 四维分桶统计滚动命中率，MinSample=20 才启用，权重乘子限幅 ±10%，目标命中率 0.35）**已完整实现但
// 生产零调用方**——即 OSS 调研 §6.2 要的 forecast_scalar 归一化层。P3 只做两件"接线"：
//
//	① 读：`annotateQualityWeights` 把 `Weight(key)` 解析到每条买入信号的 `Signal.QualityWeight`
//	   （定权合成器的 `qualityFactor` 消费它；0 = 未解析 → 该维度弃权）；
//	② 喂：`observeSignalQuality` 在本轮登记买入信号的**分桶键**（信号触发时快照，含当时的情绪相位），
//	   前瞻窗到期后用**本轮已拉取的行情快照**判胜负（前向收益 > 0 = hit），回喂 `Update(key, hit)`。
//
// 为什么必须"信号触发时快照分桶键"：情绪相位/板块/新闻类型都是**入场时点**的语境，事后从账本
// 反推不出来（账本只有成本价与代码）。故用一条有界待结算队列把键携带到结算时刻。
//
// 为什么用本轮行情快照而不是逐票调上游（§SIGBATCH 教训）：结算只认 `quotes` 里已有且价格有效的
// 代码，缺价的条目**留到下轮**——绝不为了结算而逐票打行情接口。
//
// 冷启动纪律（方案 §5 测试锁）：样本 < MinSample 时 `Weight` 恒 1.0 → `qualityFactor` 返回 1.0 →
// 连乘中性 → 定权结果与"没有这一维"逐字一致。开关 `Enhance.DynWeight` 关闭时本文件全部路径
// **立即返回**（不建表、不登记、不结算、不写 QualityWeight）。
//
// English: §DYN-SIZE P3 — the feedback half of the signal-quality normalization layer. The table
// already exists but had zero production callers. We (1) resolve its Weight onto each buy signal and
// (2) feed outcomes back by snapshotting the bucket key at signal time (emotion phase / sector /
// news type are entry-time context, unrecoverable from the ledger later) and settling against the
// round's already-fetched quotes. Cold start (samples < MinSample) stays neutral at 1.0; with
// Enhance.DynWeight off every path here returns immediately.
package engine

import (
	"log"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/signalctl"
)

// qualityHorizon 前瞻结算窗（自然日，与 internal/engine/factor_monitor.go 同口径，便于两条效果
// 监测口径互相印证）。5 日 ≈ 一个短波段，足够让"战法是否真有优势"显形，又不会拖太久。
const qualityHorizon = 5 * 24 * time.Hour

// qualityMaxEntries 待结算队列上限（防长跑进程在无结算行情时无界增长）。超限**拒收新样本**
// 而不是淘汰旧样本——旧样本已快到期，丢掉等于白记。
const qualityMaxEntries = 2000

// qualityPending 一条待结算的质量样本。Key 在信号触发时快照（含当时的情绪相位）。
// English: one pending quality sample; the bucket key is snapshotted at signal time.
type qualityPending struct {
	Key        research.QualityBucketKey
	Code       string
	EntryPrice float64
	FiredAt    time.Time
}

// signalQualityTracker 信号质量结果回喂器（线程安全；每引擎一份）。
// English: the signal-quality outcome feeder (thread-safe, one per engine).
type signalQualityTracker struct {
	mu      sync.Mutex
	entries map[string]*qualityPending // key = tactic|code（同轮同战法同码只记一条）
	horizon time.Duration
	max     int
}

// newSignalQualityTracker 建回喂器（horizon<=0 用默认 5 日）。
func newSignalQualityTracker(horizon time.Duration) *signalQualityTracker {
	if horizon <= 0 {
		horizon = qualityHorizon
	}
	return &signalQualityTracker{entries: make(map[string]*qualityPending), horizon: horizon, max: qualityMaxEntries}
}

// observe 登记本轮买入信号：为每条可归桶的做多买入信号记一条待结算样本。
// 已登记（同 战法|代码）不重复记——同一波信号的连续轮次是同一个"这次判断"，重复记会让
// 命中率被单次信号刷成"高频轮次"的加权平均，样本量与真实决策次数脱钩。
// English: registers this round's buys, deduped by tactic|code so a multi-round signal counts once.
func (t *signalQualityTracker) observe(buys []combat_agent.Signal, phase string, now time.Time) int {
	if t == nil || len(buys) == 0 {
		return 0
	}
	added := 0
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range buys {
		s := buys[i]
		if !isQualityBuy(s) || s.Price <= 0 {
			continue
		}
		key, tactic, ok := qualityBucketKey(s, phase)
		if !ok {
			continue
		}
		mapKey := tactic + "|" + s.Code
		if _, dup := t.entries[mapKey]; dup {
			continue
		}
		if len(t.entries) >= t.max {
			break // 队列满：拒收新样本（旧样本已快到期，不淘汰）
		}
		fired := s.GeneratedAt
		if fired.IsZero() {
			// GeneratedAt 缺失（存量/测试装配）用当前时刻兜底——零值会被 matured 判为"早就到期"，
			// 立刻用当前价结算出 ≈0 的假收益，污染该桶命中率。
			fired = now
		}
		t.entries[mapKey] = &qualityPending{Key: key, Code: s.Code, EntryPrice: s.Price, FiredAt: fired}
		added++
	}
	return added
}

// settle 结算已到期样本：只用**本轮已拉取的行情快照**判胜负（前向收益 > 0 = hit）。
// 到期但本轮无有效报价的条目**保留**到下轮（绝不为了结算逐票打上游，§SIGBATCH 教训）。
// 返回本批结算条数。English: settles matured samples against the round's existing quotes only;
// entries without a fresh quote stay pending (never fetch upstream just to settle).
func (t *signalQualityTracker) settle(quotes map[string]*data.StockInfo, record func(key research.QualityBucketKey, hit bool, ret float64)) int {
	if t == nil || len(quotes) == 0 {
		return 0
	}
	t.mu.Lock()
	var due []struct {
		mapKey string
		e      *qualityPending
	}
	for mapKey, e := range t.entries {
		if time.Since(e.FiredAt) < t.horizon {
			continue
		}
		if q := quoteFor(quotes, e.Code); q == nil || q.Price <= 0 {
			continue // 无本轮有效报价：留到下轮（不删）
		}
		due = append(due, struct {
			mapKey string
			e      *qualityPending
		}{mapKey, e})
	}
	for _, d := range due {
		delete(t.entries, d.mapKey) // 先摘再算：结算函数不该持锁
	}
	t.mu.Unlock()

	settled := 0
	for _, d := range due {
		q := quoteFor(quotes, d.e.Code)
		if q == nil || q.Price <= 0 || d.e.EntryPrice <= 0 {
			continue
		}
		ret := (q.Price - d.e.EntryPrice) / d.e.EntryPrice
		if record != nil {
			record(d.e.Key, ret > 0, ret)
		}
		settled++
	}
	return settled
}

// size 待结算样本数（观测/测试用）。
func (t *signalQualityTracker) size() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// quoteFor 在快照里按"纯码"取行情（快照键为纯数字码，信号代码可能带 .SH/.SZ 后缀）。
func quoteFor(quotes map[string]*data.StockInfo, code string) *data.StockInfo {
	if q := quotes[code]; q != nil {
		return q
	}
	return quotes[pureTsCode(code)]
}

// isQualityBuy 是否"做多买入"信号（与 signalctl 的准入对象同口径：只有买入方向才值得评估
// "这个战法在这类语境下有没有优势"）。做空/提醒/watch 不入质量桶。
func isQualityBuy(s combat_agent.Signal) bool {
	if s.Direction == "做空" {
		return false
	}
	return s.Action == "buy" || s.Action == "买入"
}

// qualityBucketKey 组装质量分桶键（纯函数）。四个维度：
//
//	tactic   = signalctl.StrategyKeyOf（与白名单/探针同键空间）
//	sector   = Signal.Sector（板块名）
//	newsType = D1 事件状态粗分类（利空 / 事件 / 无）
//	phase    = 信号触发时的情绪相位
//
// 空维度统一写 "-" 占位：research.MakeQualityKey 只做裸拼接，空串会与"真值恰好为空"混同，
// 且不同维度的空值会在同一键上碰撞（`a||c` 与 `a|b|` 不可区分）。占位在引擎侧做，research 包
// 的既有语义与测试不受影响。
// English: builds the four-dimension bucket key; empty dimensions get a "-" placeholder on the
// engine side (research.MakeQualityKey is a bare join, so empty segments would collide).
func qualityBucketKey(s combat_agent.Signal, phase string) (research.QualityBucketKey, string, bool) {
	tactic := signalctl.StrategyKeyOf(s)
	if tactic == "" {
		return "", "", false // 无法归因来源战法 → 不入桶（宁可少统计，不污染）
	}
	key := research.MakeQualityKey(
		tactic,
		orDash(strings.TrimSpace(s.Sector)),
		orDash(qualityNewsType(s)),
		orDash(strings.TrimSpace(phase)),
	)
	return key, tactic, true
}

// qualityNewsType D1 事件状态的粗分类。真正的新闻类别分类器（internal/newsagent/classifier.go）
// 尚未把类别挂到信号上，这里先用信号已有的 D1 字段做一个**可解释**的粗粒度：
// 被负面过滤拦截 → "利空"；有关联事件 → "事件"；否则无。维度留在键里，将来分类器接上后
// 只需替换本函数即可点亮更细的分桶，不必改键结构。
// English: coarse news-type from the D1 fields already on the signal (blocked → 利空, has event →
// 事件); the dimension stays in the key so a real classifier can refine it later without a schema change.
func qualityNewsType(s combat_agent.Signal) string {
	switch {
	case s.D1Blocked:
		return "利空"
	case s.D1Event != "" || s.D1Score > 0:
		return "事件"
	}
	return ""
}

// orDash 空串 → "-" 占位。
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ── 引擎侧接线 ──────────────────────────────────────────────────────────────────

// qualityTrackerRef §DYN-SIZE P3：惰性建回喂器（每引擎一份）。DynWeight 关闭时不建（返回 nil）。
func (e *Engine) qualityTrackerRef() *signalQualityTracker {
	if e.signalQualityTable() == nil {
		return nil // Enhance.DynWeight 关闭 → 不建队列、不登记、不结算
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.qualityTrack == nil {
		e.qualityTrack = newSignalQualityTracker(qualityHorizon)
	}
	return e.qualityTrack
}

// annotateQualityWeights §DYN-SIZE P3 读侧：把分桶质量权重解析到每条买入信号的
// Signal.QualityWeight（定权合成器的 qualityFactor 消费）。
//
// 关闭时（Enhance.DynWeight=false）**立即返回**：QualityWeight 保持 0 → qualityFactor 弃权 →
// 定权结果与"没有这一维"逐字一致。开启但样本未达 MinSample 时 Weight 恒 1.0 → 中性乘子，
// 同样零行为变更（冷启动锁）。
// English: resolves the bucket weight onto each buy signal. Disabled → immediate return (the
// dimension abstains); enabled but below MinSample → neutral 1.0. Both are zero behavior change.
func (e *Engine) annotateQualityWeights(sigs []combat_agent.Signal) {
	tb := e.signalQualityTable()
	if tb == nil || len(sigs) == 0 {
		return
	}
	phase := e.emotionPhaseNow()
	for i := range sigs {
		if !isQualityBuy(sigs[i]) {
			continue
		}
		key, _, ok := qualityBucketKey(sigs[i], phase)
		if !ok {
			continue
		}
		sigs[i].QualityWeight = tb.Weight(key)
	}
}

// observeSignalQuality §DYN-SIZE P3 喂侧：登记本轮买入信号的分桶键 + 结算到期样本。
// 由 scoreCycle 每轮调用（观察段可为空，结算段照跑）。关闭时立即返回。
// English: registers this round's buys and settles matured samples; a no-op when DynWeight is off.
func (e *Engine) observeSignalQuality(buys []combat_agent.Signal, quotes map[string]*data.StockInfo, phase string) {
	tr := e.qualityTrackerRef()
	if tr == nil {
		return
	}
	if n := tr.observe(buys, phase, time.Now()); n > 0 {
		log.Printf("[dynsize] 信号质量登记 %d 条待结算（分桶样本，前瞻 %s）", n, qualityHorizon)
	}
	settled := tr.settle(quotes, func(key research.QualityBucketKey, hit bool, ret float64) {
		e.SignalQualityRecordKey(key, hit)
		log.Printf("[dynsize] 信号质量结算 桶=%s 前向收益=%+.4f 命中=%v", key, ret, hit)
	})
	if settled > 0 {
		log.Printf("[dynsize] 信号质量本批结算 %d 条（待结算余 %d）", settled, tr.size())
	}
}

// emotionPhaseNow 当前情绪相位（主循环缓存；空=未知 → 分桶键用占位）。
func (e *Engine) emotionPhaseNow() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastEmotionPhase
}
