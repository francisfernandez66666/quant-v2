// quote_feed_hub_test.go — §SUBSCRIPTION-A（2026-11-01）行情装配层的裁决锁。
//
// 这批要证的东西都很具体，而且每一条都对应一个真实缺陷形状：
//  1. **不许伪造新鲜度**：feed 命中一小撮票时，全局基线不能归零（旧实现命中 1 只就刷，
//     那只票的新鲜替全池作了保，而它喂的是发单前的行情硬闸）；
//  2. **覆盖率阈值含边界**：80% 达标、差一只不达标（防实现成 `>`）；
//  3. **观察模式不改写任何价格**（这是显式关 primary 的回滚位；出厂档是主源模式，两档都必须可证）；
//  4. **多源价格不一致以券商为准**，只有券商自身读数不成立才降级（owner 裁决 5 的字面测试）；
//  5. **合法单位档不误判**：比值≈100（手）时该配系数，不该判通道坏；
//  6. **热点池按源分档 + 不对称迟滞 + 被钉住的代码收池不踢**（owner 裁决 12）。
package data

import (
	"context"
	"fmt"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// hubBase 造一个"外部链已建立基线"的 Fetcher：n 只票全部有有效价（当日可交易分母的近似）。
// lastOK 被刻意压到 90 秒前，这样"本轮是否刷新了基线"是一个可直接读的差值而不是猜测。
func hubBase(t *testing.T, n int) (*Fetcher, []string) {
	t.Helper()
	codes := make([]string, 0, n)
	snap := &MarketSnapshot{Stocks: make(map[string]*StockInfo, n), Source: "新浪", Time: time.Now().Add(-90 * time.Second)}
	for i := 0; i < n; i++ {
		c := fmt.Sprintf("%06d", 600000+i)
		codes = append(codes, c)
		snap.Stocks[c] = &StockInfo{Code: c, Name: "票" + c, Price: 10, Sector: "板块", NetInflow: 5, HasFlow: true}
	}
	f := NewFetcher(codes, &MarketAPI{}, nil)
	f.IngestSnapshot(snap)
	f.lastOK.Store(time.Now().Add(-90 * time.Second).Unix())
	return f, codes
}

// hubTick 造一个单位自洽（比值≈1，即 volume 单位＝股）的新鲜 tick。
func hubTick(px, vol float64) QMTTick {
	return QMTTick{LastPrice: px, Open: px, High: px, Low: px, PrevClose: px,
		Volume: vol, Amount: px * vol, TickTime: time.Now().UnixMilli()}
}

// newPrimaryFeed 构造主源档 feed。
func newPrimaryFeed(f *Fetcher, ticks map[string]QMTTick) (*QMTFeed, *fakeFeedSource) {
	src := &fakeFeedSource{ticks: ticks}
	feed := NewQMTFeed(f, src, time.Second, 30*time.Second, 1)
	feed.SetPrimary(true)
	return feed, src
}

// TestStaleGateNotForgedByPartialFeed 覆盖率不达标 ⇒ 全局陈旧度继续增长，
// 但被券商覆盖的那几只票自己算新鲜、没被覆盖的仍在变陈。
func TestStaleGateNotForgedByPartialFeed(t *testing.T) {
	f, codes := hubBase(t, 200)
	hits := map[string]QMTTick{}
	for _, c := range codes[:10] { // 只覆盖 10/200 ＝ 5%
		hits[c] = hubTick(11, 200000)
	}
	feed, _ := newPrimaryFeed(f, hits)
	before := f.Staleness()
	if n := feed.applyTicks(hits, 200); n != 10 {
		t.Fatalf("应命中 10 只, got %d", n)
	}
	if f.Staleness() < before {
		t.Fatalf("覆盖率 5%% 却把全局基线归零了（陈旧度 %v < %v）：一只票不许替全池作保", f.Staleness(), before)
	}
	if ms := f.StalenessMs(codes[0]); ms < 0 || ms > 3000 {
		t.Fatalf("券商刚给过 tick 的票，每票陈旧度应新鲜, got %dms", ms)
	}
	if ms := f.StalenessMs(codes[199]); ms < 80000 {
		t.Fatalf("没被覆盖的票陈旧度应继续增长, got %dms", ms)
	}
	if st := f.FeedStatsNow(); st.RefreshedGlobal {
		t.Fatal("stats.RefreshedGlobal 必须如实记 false（观测面与判据不能两张皮）")
	}
}

// TestStaleGateThresholdBoundary 阈值边界含等号：80% 达标、差一只不达标。
func TestStaleGateThresholdBoundary(t *testing.T) {
	// 达标：160/200
	f, codes := hubBase(t, 200)
	hits := map[string]QMTTick{}
	for _, c := range codes[:160] {
		hits[c] = hubTick(11, 200000)
	}
	feed, _ := newPrimaryFeed(f, hits)
	feed.applyTicks(hits, 200)
	if st := f.FeedStatsNow(); !st.RefreshedGlobal {
		t.Fatalf("160/200＝80%% 应判达标（阈值含等号）, stats=%+v", st)
	}
	// 不达标：159/200（差一只）
	f2, codes2 := hubBase(t, 200)
	f2.lastOK.Store(time.Now().Add(-90 * time.Second).Unix())
	hits2 := map[string]QMTTick{}
	for _, c := range codes2[:159] {
		hits2[c] = hubTick(11, 200000)
	}
	feed2, _ := newPrimaryFeed(f2, hits2)
	feed2.applyTicks(hits2, 200)
	if st := f2.FeedStatsNow(); st.RefreshedGlobal {
		t.Fatalf("159/200＝79.5%% 不该刷新基线：阈值被实现成了 >= 之下也放行, coverage=%.2f", st.CoveragePct)
	}
}

// TestObserveModeNeverChangesPrices 回滚档（显式 primary=false）：一个价格都不改写、不动基线。
// 这是"发版≠生效"的读面证据：只把 feed 连上看几天数，快照与今天完全同值。
func TestObserveModeNeverChangesPrices(t *testing.T) {
	f, codes := hubBase(t, 200)
	beforePrice := f.Snapshot().Stocks[codes[0]].Price
	beforeSource := f.Snapshot().Source
	beforeOK := f.lastOK.Load()
	src := &fakeFeedSource{ticks: map[string]QMTTick{codes[0]: hubTick(99, 200000)}}
	feed := NewQMTFeed(f, src, time.Second, 30*time.Second, 1) // 不调 SetPrimary ⇒ 观察模式
	hits := feed.applyTicks(src.ticks, 200)
	if hits != 1 {
		t.Fatalf("观察模式仍应统计命中数, got %d", hits)
	}
	if got := f.Snapshot().Stocks[codes[0]].Price; got != beforePrice {
		t.Fatalf("观察模式改写了价格 %v → %v：显式关 primary 必须与旧形状逐字节一致", beforePrice, got)
	}
	if f.Snapshot().Source != beforeSource {
		t.Fatalf("观察模式改了 Source：%v → %v", beforeSource, f.Snapshot().Source)
	}
	if f.lastOK.Load() != beforeOK {
		t.Fatal("观察模式刷新了全局基线")
	}
	if len(f.applyFeedOverlay(f.Snapshot(), time.Now())) != 0 {
		t.Fatal("非主源档不许在采集链里合并 overlay")
	}
}

// TestBrokerWinsOnPriceConflict 两源价格对不上 ⇒ 用券商值，且**不降级**（owner 裁决 5 的字面测试）。
func TestBrokerWinsOnPriceConflict(t *testing.T) {
	f, codes := hubBase(t, 200)
	hits := map[string]QMTTick{}
	for _, c := range codes {
		hits[c] = hubTick(14, 200000) // 外部基线是 10，券商 14：差 40%，但单位自洽
	}
	feed, _ := newPrimaryFeed(f, hits)
	feed.applyTicks(hits, 200)
	merged := f.Snapshot().Stocks[codes[0]]
	if merged.Price != 14 {
		t.Fatalf("多源不一致必须以券商为准, got %v", merged.Price)
	}
	if st := f.FeedStatsNow(); st.Degrade != "" {
		t.Fatalf("两源价差不构成降级理由, Degrade=%q", st.Degrade)
	}
	// tick 不携带的字段必须原样保留（资金流是东财/同花顺的账，券商没有）
	if merged.Name == "" || merged.Sector != "板块" || !merged.HasFlow || merged.NetInflow != 5 {
		t.Fatalf("tick 没有的字段被清了: %+v", merged)
	}
}

// TestDegradeJ3OnlyWhenUnitImplausible 券商读数自洽性判据：
// 比值≈1（股）与≈100（手）都是**合法单位档**，不许判通道坏；只有两档都不像才判 J3。
func TestDegradeJ3OnlyWhenUnitImplausible(t *testing.T) {
	cases := []struct {
		name        string
		amountDiv   float64 // 相对自洽值的缩放
		wantDegrade bool
	}{
		{"单位是股（比值≈1）", 1, false},
		{"单位是手（比值≈100）", 100, false},
		{"口径不成立（比值≈7.4）", 7.4, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, codes := hubBase(t, 200)
			hits := map[string]QMTTick{}
			for _, code := range codes {
				tk := hubTick(10, 200000)
				// amount/(price*volume) == amountDiv
				tk.Amount = 10 * 200000 * c.amountDiv
				hits[code] = tk
			}
			feed, _ := newPrimaryFeed(f, hits)
			n := feed.applyTicks(hits, 200)
			st := f.FeedStatsNow()
			if got := st.Degrade != ""; got != c.wantDegrade {
				t.Fatalf("%s：Degrade=%q，期望不可用=%v", c.name, st.Degrade, c.wantDegrade)
			}
			if c.wantDegrade && n != 0 {
				t.Fatalf("判降级时本轮不该产 overlay, got %d", n)
			}
		})
	}
}

// TestDegradeJ2WhenAllTicksStale 全批 tick 超龄 ⇒ J2（通道在读旧数据），不刷基线、不改价。
func TestDegradeJ2WhenAllTicksStale(t *testing.T) {
	f, codes := hubBase(t, 200)
	hits := map[string]QMTTick{}
	for _, c := range codes {
		tk := hubTick(12, 200000)
		tk.TickTime = time.Now().Add(-time.Hour).UnixMilli()
		hits[c] = tk
	}
	feed, _ := newPrimaryFeed(f, hits)
	before := f.Snapshot().Stocks[codes[0]].Price
	if n := feed.applyTicks(hits, 200); n != 0 {
		t.Fatalf("超龄 tick 全部丢弃后不应有命中, got %d", n)
	}
	if f.FeedStatsNow().Degrade != "J2" {
		t.Fatalf("应判 J2, got %q", f.FeedStatsNow().Degrade)
	}
	if f.Snapshot().Stocks[codes[0]].Price != before {
		t.Fatal("J2 轮改写快照（降级方向错）")
	}
}

// TestCoverageDenominatorFallsBackToPool 分母不可信（本轮谁都给不出有效价）时退回池总数并如实标注。
func TestCoverageDenominatorFallsBackToPool(t *testing.T) {
	f, codes := hubBase(t, 10)
	// 把基线里所有票的价格清成 0 ⇒ 分母取不到，只能退回池总数
	snap := f.Snapshot()
	for _, si := range snap.Stocks {
		si.Price = 0
	}
	f.IngestSnapshot(snap)
	f.lastOK.Store(time.Now().Add(-90 * time.Second).Unix())
	hits := map[string]QMTTick{codes[0]: hubTick(11, 200000)}
	feed, _ := newPrimaryFeed(f, hits)
	feed.applyTicks(hits, 10)
	st := f.FeedStatsNow()
	if st.DenominatorSource != "tradable" && st.DenominatorSource != "pool" {
		t.Fatalf("分母来源必须如实标注, got %q", st.DenominatorSource)
	}
	if st.Pool != 10 || st.Denominator <= 0 {
		t.Fatalf("读数不可信: %+v", st)
	}
}

// TestHotPoolTierHysteresis 升档慢（连续稳定 5 分钟）、降档即刻。
// 为什么不对称：源在两个档位之间抖会让热点池每轮换血、缺价代码进进出出。
func TestHotPoolTierHysteresis(t *testing.T) {
	f, _ := hubBase(t, 2)
	p := f.feedPolicyNow()
	p.Primary = true
	p.HotPoolCap = 400
	f.SetFeedPolicy(p)

	one := make([]string, 100)
	for i := range one {
		one[i] = fmt.Sprintf("%06d", 700000+i)
	}
	// 第一次报健康：还在稳定窗口内 ⇒ 不升档，热点池仍是降级档 60
	f.setFeedHealth(true)
	if f.feedLive() {
		t.Fatal("首次健康就升档＝迟滞没生效（源抖一下池子就换血）")
	}
	f.UpdateHotStocks(one)
	if _, hot := f.watchCounts(); hot != 60 {
		t.Fatalf("未达稳定窗口时热点池应仍为 60, got %d", hot)
	}
	// 把稳定起点推到 6 分钟前 ⇒ 再报一次健康即升档
	h := f.hub()
	h.mu.Lock()
	h.healthySince = time.Now().Add(-6 * time.Minute)
	h.mu.Unlock()
	f.setFeedHealth(true)
	if !f.feedLive() {
		t.Fatal("连续稳定超过窗口后应升档")
	}
	f.UpdateHotStocks(one)
	_, hot := f.watchCounts()
	if hot != 100 {
		t.Fatalf("主源档应放开到配置上限（100 只不截断）, got %d", hot)
	}
	// 降档即刻：一次不健康就要收回 60
	f.setFeedHealth(false)
	f.UpdateHotStocks(one)
	_, hot = f.watchCounts()
	if hot != 60 {
		t.Fatalf("降级档必须当轮收回 60（后备链 80 只一片，池越大请求越多）, got %d", hot)
	}
}

// TestPinnedCodesSurviveShrink 被引用（钉住）的代码不许因为收池被截掉。
// 做 T 的一只票因为池缩而看不见价，比多占几个池位危险得多。
func TestPinnedCodesSurviveShrink(t *testing.T) {
	f, _ := hubBase(t, 2)
	// 钉住的码出现在这轮热点名单的**尾部**（超出 cap 的那一段）——
	// 这正是收池唯一会伤到被引用代码的形状：按热度排序被截断。
	one := make([]string, 100)
	for i := range one {
		one[i] = fmt.Sprintf("%06d", 700000+i)
	}
	one[98] = "889900"
	one[99] = "889901"
	f.pinCode("889900")
	f.pinCode("889901")
	f.setFeedHealth(false) // 降级档：cap=60
	f.UpdateHotStocks(one)
	_, hot := f.watchCounts()
	if hot != 62 {
		t.Fatalf("两个钉住的码必须被保住（60 + 2）, got %d", hot)
	}
	all := f.WatchCodes()
	set := map[string]bool{}
	for _, c := range all {
		set[c] = true
	}
	if !set["889900"] || !set["889901"] {
		t.Fatal("收池把被引用的票踢掉了")
	}
}

// TestFeedFailureDropsTierAndNeverRefreshes J1（连续失败）⇒ 收回主源档且绝不刷新基线。
func TestFeedFailureDropsTierAndNeverRefreshes(t *testing.T) {
	f, _ := hubBase(t, 2)
	p := f.feedPolicyNow()
	p.Primary = true
	p.HotPoolCap = 400
	f.SetFeedPolicy(p)
	h := f.hub()
	h.mu.Lock()
	h.healthySince = time.Now().Add(-6 * time.Minute)
	h.live = true
	h.mu.Unlock()

	before := f.lastOK.Load()
	f.NoteFeedFailure(3, false)
	if f.feedLive() {
		t.Fatal("通道失败后必须立刻收回主源档")
	}
	if f.lastOK.Load() != before {
		t.Fatal("失败轮刷新了基线：什么都拿不到不许冒充「全池新鲜」")
	}
	if st := f.FeedStatsNow(); st.Degrade != "J1" || st.ConsecutiveFail != 3 {
		t.Fatalf("J1 读数没记上: %+v", st)
	}
}

// authSource 是一个只会返回"鉴权被拒"的假源（包装 data.ErrQuoteUnauthorized，
// 与 trading.QMTClient.Quotes 在 401/403 时的包装同形）。
type authSource struct{ calls int }

func (a *authSource) Quotes(_ context.Context, _ []string) (map[string]QMTTick, error) {
	a.calls++
	return nil, fmtErrWrapped()
}

// fmtErrWrapped 造一条 %w 包装链上的哨兵错误（等价于 trading 侧的 fmt.Errorf("%w: HTTP %d", ...)）。
func fmtErrWrapped() error { return fmt.Errorf("%w: HTTP 401", ErrQuoteUnauthorized) }

// TestAuthFailureIsDistinguishable 裁决 14 的核心：撤钥匙后的状态必须是 auth_failed，
// 而不是和"网关挂了"混成同一个 degraded:J1——两者处置动作完全不同（补凭证 vs 查进程）。
func TestAuthFailureIsDistinguishable(t *testing.T) {
	f, _ := hubBase(t, 10)
	p := f.feedPolicyNow()
	p.Primary = true
	f.SetFeedPolicy(p)
	src := &authSource{}
	feed := NewQMTFeed(f, src, time.Second, 30*time.Second, 1)
	feed.pollOnce()
	feed.pollOnce()

	if got := f.QuoteFeedState(); got != "auth_failed" {
		t.Fatalf("撤钥匙后状态应是 auth_failed（与网关挂了分形），got %q", got)
	}
	st := f.FeedStatsNow()
	if st.ConsecutiveFail != 2 {
		t.Fatalf("连续失败轮次应 2, got %d", st.ConsecutiveFail)
	}
	if !st.AuthFailed {
		t.Fatal("AuthFailed 读数没置位：观测面会把它显示成普通通道故障")
	}
	if f.feedLive() {
		t.Fatal("鉴权失败后必须当轮收回主源档（缩池是安全方向）")
	}
}

// TestAuthRestoredClearsState 钥匙恢复后下一轮即回到 ok，不需要重启进程
// （否则"换钥匙=重启才生效"会变成又一条静默失效的路）。
func TestAuthRestoredClearsState(t *testing.T) {
	f, codes := hubBase(t, 10)
	p := f.feedPolicyNow()
	p.Primary = true
	p.HotPoolCap = 400
	f.SetFeedPolicy(p)
	badSrc := &authSource{}
	feed := NewQMTFeed(f, badSrc, time.Second, 30*time.Second, 1)
	feed.pollOnce()
	if f.QuoteFeedState() != "auth_failed" {
		t.Fatalf("前置：应先进入 auth_failed, got %q", f.QuoteFeedState())
	}
	hits := map[string]QMTTick{}
	for _, c := range codes {
		hits[c] = hubTick(11, 200000)
	}
	feed.src = &fakeFeedSource{ticks: hits} // 换回好源（等价于重新配上钥匙）
	feed.pollOnce()
	if got := f.QuoteFeedState(); got != "ok" {
		t.Fatalf("恢复后应回 ok, got %q", got)
	}
	if f.FeedStatsNow().AuthFailed {
		t.Fatal("成功轮之后 AuthFailed 位必须清掉（否则会一直显示成钥匙问题）")
	}
}

// TestObserveModeReportsWouldOverrideAndPromote 追加裁决 15 的 L2 半边（feed 侧）：
// 观察模式不改写价格，但必须把"本可以改写"与"已连续达标、可以开主源"记成读数。
// 没有它，"能不能开 primary"就只能靠人翻日志数轮次——那还是感觉式上线。
func TestObserveModeReportsWouldOverrideAndPromote(t *testing.T) {
	f, codes := hubBase(t, 200)
	hits := map[string]QMTTick{}
	for _, c := range codes { // 全覆盖，观察模式下覆盖率必然达标
		hits[c] = hubTick(12, 200000)
	}
	src := &fakeFeedSource{ticks: hits}
	feed := NewQMTFeed(f, src, time.Second, 30*time.Second, 1)
	// 不调 SetPrimary ⇒ 缺省观察档
	feed.applyTicks(hits, 200)
	st := f.FeedStatsNow()
	if !st.WouldOverride {
		t.Fatalf("观察模式下覆盖率达标就该报 would_override（本可以改写）: %+v", st)
	}
	if f.Snapshot().Stocks[codes[0]].Price != 10 {
		t.Fatal("would_override 是读数，不是改写许可：观察档价格必须一动不动")
	}
	// 连续 3 轮达标 ⇒ ready_to_promote；第 1、2 轮不许提前报
	if st.ReadyToPromote {
		t.Fatal("第 1 轮就 ready_to_promote＝迟滞没生效（单轮尖峰不算达标）")
	}
	feed.applyTicks(hits, 200)
	feed.applyTicks(hits, 200)
	st = f.FeedStatsNow()
	if !st.ReadyToPromote || st.CoverageRounds < 3 {
		t.Fatalf("连续 3 轮达标后应 ready_to_promote, got %+v", st)
	}
}

// TestQuoteFeedStateNilSafeWhenNeverPolled 从没跑过时不能报 ok（"没数据"不许冒充"正常"，
// 与 §M2 的 -1=未知 同一方向）。
// 判据口径于 2026-11-01 改严：以前靠消费端看 Pool/Hits 全 0 来折"未启用"，而出厂开之后
// "启用了却一轮都没跑"恰恰是 Pool 也为 0 的那一态，状态位若还是 ok，前端就只会显示"未启用"，
// 把本批要抓的静默无效藏回原处。现在状态位自己说 not_polled。
func TestQuoteFeedStateNilSafeWhenNeverPolled(t *testing.T) {
	f, _ := hubBase(t, 10)
	st := f.FeedStatsNow()
	if st.Pool != 0 || st.Hits != 0 || st.ConsecutiveFail != 0 || st.Rounds != 0 {
		t.Fatalf("从没轮询过却已有读数: %+v", st)
	}
	if got := f.QuoteFeedState(); got != "not_polled" {
		t.Fatalf("从没轮询应如实说 not_polled（不许冒充 ok），got %q", got)
	}
}

// TestPerCodeSourceOffKeepsFieldsEmpty 显式关闭（出厂是开）：Src/AsOfMs 一律留空。
// 这两个字段是 omitempty ⇒ 关闭时快照落盘文件与 SSE 载荷的字节形状与今天一致；
// 开着才逐票标。摘掉开关判断（无条件打标）会让这条当场红。
func TestPerCodeSourceOffKeepsFieldsEmpty(t *testing.T) {
	f, codes := hubBase(t, 200)
	p := f.feedPolicyNow()
	p.Primary = true // 主源开、逐票标记关：只有 overlay 那一路开标记才够，必须两开关独立
	f.SetFeedPolicy(p)
	hits := map[string]QMTTick{}
	for _, c := range codes[:100] {
		hits[c] = hubTick(12, 200000)
	}
	feed, _ := newPrimaryFeed(f, hits)
	feed.applyTicks(hits, 200)
	f.applyFeedOverlay(f.Snapshot(), time.Now())
	for _, c := range codes {
		si := f.Snapshot().Stocks[c]
		if si == nil {
			continue
		}
		if si.Src != "" || si.AsOfMs != 0 {
			t.Fatalf("关闭逐票标记却写了来源: code=%s src=%q as_of=%d", c, si.Src, si.AsOfMs)
		}
	}
}

// TestPerCodeSourceMarksWhoGaveThisPrice 打开后：券商给的票标 QMT-L1，其余标本轮后备源；
// 且后备源那部分**不许**被整份标称源（QMT-L1）冒充——这正是混源时说谎的地方。
func TestPerCodeSourceMarksWhoGaveThisPrice(t *testing.T) {
	f, codes := hubBase(t, 200)
	p := f.feedPolicyNow()
	p.Primary = true
	p.PerCodeSource = true
	f.SetFeedPolicy(p)
	hits := map[string]QMTTick{}
	for _, c := range codes[:120] {
		hits[c] = hubTick(12, 200000)
	}
	feed, _ := newPrimaryFeed(f, hits)
	feed.applyTicks(hits, 200)
	// 复刻 fetch() 的次序：先记外部源名 → 合并 overlay → 逐票标记 → 最后才改整份 Source。
	// 次序错了（先改 Source 再标记）就会把后备票也标成券商，混源恒 0——那正是本用例要抓的。
	ext := f.Snapshot().Source
	if ext == QuoteSourceQMTL1 {
		ext = "新浪"
	}
	overlaid := f.applyFeedOverlay(f.Snapshot(), time.Now())
	f.stampSnapshotSources(f.Snapshot(), overlaid, ext)
	if len(overlaid) == 0 {
		t.Fatal("overlay 没合并出任何票，标记无从谈起")
	}
	mixed := 0
	for _, c := range codes {
		si := f.Snapshot().Stocks[c]
		if si == nil {
			continue
		}
		if _, ok := overlaid[c]; ok {
			if si.Src != QuoteSourceQMTL1 || si.AsOfMs <= 0 {
				t.Fatalf("券商给价的票标记缺失: code=%s src=%q as_of=%d", c, si.Src, si.AsOfMs)
			}
		}
		if si.Src != "" && si.Src != QuoteSourceQMTL1 {
			mixed++
		}
	}
	if mixed == 0 {
		t.Fatal("混源计数为 0：整份 Source 说券商、每票 Source 却说没人兜底，两者必有一个在撒谎")
	}
}

// TestStampSnapshotSourcesNeverOverwritesBrokerTime 后备源标记只补"没被 overlay 过"的票：
// 拿整份时间戳去覆盖券商给的时刻，等于把 3 秒前的 tick 说成 5 秒前的采集时刻。
func TestStampSnapshotSourcesNeverOverwritesBrokerTime(t *testing.T) {
	f, codes := hubBase(t, 4)
	p := f.feedPolicyNow()
	p.PerCodeSource = true
	f.SetFeedPolicy(p)
	snap := f.Snapshot()
	snap.Stocks[codes[0]].Src = QuoteSourceQMTL1
	snap.Stocks[codes[0]].AsOfMs = 1234567 // 券商时刻（哨兵值，便于断言没被改）
	overlaid := map[string]int64{codes[0]: 1234567}
	f.stampSnapshotSources(snap, overlaid, "新浪")
	if got := snap.Stocks[codes[0]]; got.AsOfMs != 1234567 || got.Src != QuoteSourceQMTL1 {
		t.Fatalf("被 overlay 的票的时刻被后备源覆盖了: %+v", got)
	}
	if got := snap.Stocks[codes[3]]; got.Src == "" || got.AsOfMs == 0 {
		t.Fatalf("后备源票没被标记: %+v", got)
	}
}

// TestHubQuoteRequiresSwitchAndFreshness 快照优先读法的双闸：开关关 ⇒ 一律不命中（今天的行为）；
// 开关开但这一只票的时刻过期 ⇒ 也不命中（宁可不答，也不拿旧价冒充此刻）。
func TestHubQuoteRequiresSwitchAndFreshness(t *testing.T) {
	f, codes := hubBase(t, 3)
	if _, ok := f.HubQuote(codes[0], HubQuoteTTL); ok {
		t.Fatal("开关关着却命中了快照优先（裁决 13 要求缺省＝今天每问一次打一次外部源）")
	}
	p := f.feedPolicyNow()
	p.Consult = true
	f.SetFeedPolicy(p)
	f.lastOK.Store(time.Now().Unix())
	si, ok := f.HubQuote(codes[0], HubQuoteTTL)
	if !ok || si == nil || si.Price <= 0 {
		t.Fatalf("开关开且新鲜时应命中: %+v ok=%v", si, ok)
	}
	// 属性断言而不是指针比对：调用方（自选补价、咨询装配）拿到后会就地改字段，
	// 若交出的是快照里的共享对象，采集器自己的账就被读路径写脏了。
	si.Price = 999
	if got := f.Snapshot().Stocks[codes[0]].Price; got == 999 {
		t.Fatal("HubQuote 交出的对象与快照共享：读路径改字段写坏了采集器的账")
	}
	si = func() *StockInfo { x, _ := f.HubQuote(codes[0], HubQuoteTTL); return x }()
	if si == nil || si.Price != 10 {
		t.Fatalf("快照账本被污染或取回异常: %+v", si)
	}
	f.lastOK.Store(time.Now().Add(-time.Hour).Unix())
	if _, ok := f.HubQuote(codes[0], HubQuoteTTL); ok {
		t.Fatal("陈旧快照仍被判为可用：旧价冒充此刻价，正是咨询语义最不能容的形态")
	}
}

// TestApplyHubPriceNeverTouchesFlowFields 合并只动价格类字段（底线 8）。
// 摘掉这一条，"为了换个更新鲜的价"就把东财的净流入清零——咨询里的主力口径当场失真。
func TestApplyHubPriceNeverTouchesFlowFields(t *testing.T) {
	dst := &StockInfo{Code: "600000", Price: 10, Name: "浦发银行", Sector: "银行",
		NetInflow: 1234.5, HasFlow: true, FloatShares: 9e8, Turnover: 1.2}
	before := *dst
	hub := &StockInfo{Code: "600000", Price: 10.5, Open: 10.1, High: 10.7, Low: 10.0,
		PrevClose: 10.2, Volume: 200000, Amount: 2.1e6, Src: QuoteSourceQMTL1, AsOfMs: 123}
	if !ApplyHubPrice(dst, hub) {
		t.Fatal("合并应成功")
	}
	if dst.Price != 10.5 || dst.Src != QuoteSourceQMTL1 || dst.AsOfMs != 123 {
		t.Fatalf("价格类字段没并进来: %+v", dst)
	}
	if dst.NetInflow != before.NetInflow || dst.HasFlow != before.HasFlow || dst.Name != before.Name ||
		dst.Sector != before.Sector || dst.FloatShares != before.FloatShares || dst.Turnover != before.Turnover {
		t.Fatalf("非价格字段被覆盖/清零: %+v", dst)
	}
	if ApplyHubPrice(dst, &StockInfo{Price: 0}) {
		t.Fatal("hub 无有效价也合并：那会把好值换成 0")
	}
}

// TestFeedAlertCodeFiveStates 通畅自证量规的五个取值必须各自可达且互不混同。
// 这批把券商源改成出厂开，最坏结局是"开关开着、通道死了、日志只剩一条节流行"，
// 所以这里逐档钉：未启用=0 / 一轮没跑=4 / 被鉴权拒=2 / 自身读数不可用=3 / 健康=1。
// 尤其 2 与 3 不许并成一个值：处置动作不同（补钥匙 vs 查网关进程）。
func TestFeedAlertCodeFiveStates(t *testing.T) {
	f, codes := hubBase(t, 10)
	// 档 0：策略没启用（出厂开的语义只在装配后成立，未装配时不得告警）
	if got := f.FeedAlertCode(); got != FeedAlertNotApplicable {
		t.Fatalf("未启用应写 0（不触发），got %d", got)
	}
	f.SetFeedPolicy(FeedPolicy{Enabled: true, Primary: true, CoverageMinPct: 80, MaxAge: 30 * time.Second})
	// 档 4：启用了但一轮都没跑
	if got := f.FeedAlertCode(); got != FeedAlertNeverPolled {
		t.Fatalf("启用未轮询应写 4，got %d", got)
	}
	// 档 1：跑过一轮且读数可用
	feed, src := newPrimaryFeed(f, map[string]QMTTick{})
	f.SetFeedPolicy(FeedPolicy{Enabled: true, Primary: true, CoverageMinPct: -1, MaxAge: 30 * time.Second})
	src.ticks = map[string]QMTTick{codes[0]: hubTick(11, 1000)}
	feed.pollOnce()
	if st := f.FeedStatsNow(); st.Rounds <= 0 {
		t.Fatalf("轮询后必须累计轮次, got %d", st.Rounds)
	}
	if got := f.FeedAlertCode(); got != FeedAlertAlive {
		t.Fatalf("健康轮应写 1，got %d", got)
	}
	// 档 2：鉴权失败（与通道挂必须分形）
	f.NoteFeedFailure(1, true)
	if got := f.FeedAlertCode(); got != FeedAlertAuthFailed {
		t.Fatalf("401/403 应写 2，got %d", got)
	}
	// 档 3：自身读数不可用（J1/J2/J3 都归这一档）
	f.NoteFeedFailure(2, false)
	if got := f.FeedAlertCode(); got != FeedAlertDegraded {
		t.Fatalf("通道连续失败应写 3，got %d", got)
	}
	// 恢复后回到 1（规则用 ge 2，回落才能成对销案）
	feed.pollOnce()
	if got := f.FeedAlertCode(); got != FeedAlertAlive {
		t.Fatalf("恢复后应回到 1，got %d", got)
	}
}

// TestFeedAlertCodeStarvedEmptyReplies J0 档：连续空回包必须算"不可用"，但**只改读数不改数据面**。
//
// 现网 2026-11-02 的真实形态就是这一形：xtdata 在本机扫不到任何行情服务端点，网关 `/quotes`
// 照常回 200 但 `ticks:{}` ⇒ Rounds 一路涨、Hits 恒 0、Degrade 恒空 ⇒ 旧判据编码成 Alive=1，
// 必推规则 broker_feed_unavailable 一次都没响（alert_router_state.json 里它连键都没有，
// 同期 order_fail_rate 10:57 已经 fire 过）。
//
// 三条一起钉死，缺一不可：
//  1. 连续 20 轮（约 60s）空回包 ⇒ 3；**单轮/19 轮不许报**（网关重启、池轮换、收盘前最后一轮
//     都会出现正常空回包，一轮就报是把运维动作刷成告警）；
//  2. J0 只进告警编码与 state 读数，**不写 st.Degrade** ——那个字段会带动主源健康线与池分档迟滞，
//     而"券商不通就自动退回现有源"是 owner 定死的链路规则，不许被一檔告警顺手改掉；
//  3. 池为空时不算（没东西可问 ≠ 通道坏），一旦回包恢复即刻归零（规则用 ge 2，回落才能成对销案）。
func TestFeedAlertCodeStarvedEmptyReplies(t *testing.T) {
	f, _ := hubBase(t, 10)
	// 覆盖率闸用出厂的 80，不用 -1：`-1` 是"显式关闭判断＝命中即刷全局基线"的旧行为排障档，
	// 拿它来验"零条回包不许替全池作保"会自证失败（第一版我就踩在这里，红的是测试不是实现）。
	f.SetFeedPolicy(FeedPolicy{Enabled: true, Primary: true, CoverageMinPct: 80, MaxAge: 30 * time.Second})
	feed, src := newPrimaryFeed(f, map[string]QMTTick{})
	src.ticks = map[string]QMTTick{} // 网关活着、HTTP 成功、回包零条

	lastOKBefore := f.lastOK.Load()
	for i := 1; i < feedEmptyAlertRounds; i++ {
		feed.pollOnce()
		if got := f.FeedAlertCode(); got != FeedAlertAlive {
			t.Fatalf("第 %d 轮空回包不该就报（正常轮换/重启窗口也会有），got %d", i, got)
		}
	}
	feed.pollOnce() // 第 20 轮
	st := f.FeedStatsNow()
	if st.EmptyRounds < feedEmptyAlertRounds {
		t.Fatalf("连续空回包没累计起来, got %d", st.EmptyRounds)
	}
	if st.Degrade != "" {
		t.Fatalf("J0 不许写进 st.Degrade（那会带动主源健康线与池档＝改了退回链）, got %q", st.Degrade)
	}
	if got := f.FeedAlertCode(); got != FeedAlertDegraded {
		t.Fatalf("连续空回包应写 3（处置＝查网关与 xtdata），got %d", got)
	}
	if s := f.QuoteFeedState(); s != "degraded:J0" {
		t.Fatalf("观测面 state 应显式说出 degraded:J0（不能让面板继续报 ok），got %q", s)
	}
	// 数据面纪律：空回包期间全局新鲜度基线**不许被推进**（覆盖率闸要求 Hits>0，这里是行为复核）
	if f.lastOK.Load() != lastOKBefore {
		t.Fatalf("零条回包却刷新了全局基线＝拿没收到的东西替全池作保")
	}
	// 恢复：回包来一条即归零并回到 1，规则才能成对销案
	src.ticks = map[string]QMTTick{"600000": hubTick(11, 1000)}
	feed.pollOnce()
	if st := f.FeedStatsNow(); st.EmptyRounds != 0 {
		t.Fatalf("恢复后空回包计数没归零, got %d", st.EmptyRounds)
	}
	if got := f.FeedAlertCode(); got != FeedAlertAlive {
		t.Fatalf("恢复后应回到 1，got %d", got)
	}
	if s := f.QuoteFeedState(); s != "ok" {
		t.Fatalf("恢复后 state 应回到 ok，got %q", s)
	}
	// 池为空这一形不算故障（"没东西可问"与"通道坏"必须分形，同 not_polled 与 ok 的分形取向）
	feed.applyTicks(map[string]QMTTick{}, 0)
	if got := f.FeedAlertCode(); got == FeedAlertDegraded {
		t.Fatalf("池为 0 时的空回包被当成了通道故障")
	}
}

// TestFeedPolicyFromRuntimeFactoryOn 唯一构建器必须把四枚指针位的**生效值**灌进策略，
// 而不是原始指针（缺键时原始 *bool 是 nil，直接赋值等于"出厂关"——与裁决相反）。
// 同时锁 Enabled 与 Primary 同源：告警腿判"本该在跑"，靠的就是它跟着 qmt_feed_enabled。
func TestFeedPolicyFromRuntimeFactoryOn(t *testing.T) {
	var rt config.RuntimeConfig
	p := FeedPolicyFromRuntime(rt)
	if !p.Enabled || !p.Primary || !p.PerCodeSource || !p.Consult {
		t.Fatalf("零值配置（缺键）四档必须全为生效开: %+v", p)
	}
	if p.CoverageMinPct != feedCoverageDefault || p.VolumeToShares != 1 || p.HotPoolCap == 0 {
		t.Fatalf("Effective* 兜底没走: %+v", p)
	}
	if !p.VolumeAutoAlign {
		t.Fatal("量纲自动对齐不得随配置缺省而关（出厂开是这条改动的前置条件）")
	}
	off := false
	// 四枚键彼此独立：只关 qmt_feed_enabled 时 primary 仍是"开"（轮询没起来而已，
	// 不代表操作员改判过主源档）。构建器不许把一键关折成整组关。
	rt2 := config.RuntimeConfig{QMTFeedEnabled: &off}
	if p2 := FeedPolicyFromRuntime(rt2); p2.Enabled || !p2.Primary {
		t.Fatalf("显式 false 只关对应那枚键: %+v", p2)
	}
	rt3 := config.RuntimeConfig{QMTFeedEnabled: &off, QMTFeedPrimary: &off,
		QuotePerCodeSource: &off, QuoteHubConsult: &off}
	if p3 := FeedPolicyFromRuntime(rt3); p3.Enabled || p3.Primary || p3.PerCodeSource || p3.Consult {
		t.Fatalf("四键显式 false 必须全落成关（＝今天的字节形状）: %+v", p3)
	}
}
