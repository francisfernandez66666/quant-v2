// replay_momentum_test.go — 动量（momentum）回放适配器的定点测试。
//
// 定位先说清楚：动量判据 2026-09-24 **按实盘语义重写**并已接入回放（owner 令：
// "动量判据按实盘语义重写，今天也写进去"）。本文件因此测三件事，缺一不可——
//
//	A. 三条实盘语义有没有真的落地（不是注释里说说）：
//	   ① 兜底互斥——同标的当日兄弟战法出了信号，动量那一笔必须不入场（实盘 agent.go 的
//	      `len(sigs) == 0 &&`）；兄弟不在场/未出信号时才放行。回放与扫参两条预计算路径都要钉。
//	   ② 当日撮合——入场价必须是**触发当日收盘**、入场日期必须是信号日，不能再是次日开盘。
//	   ③ 买入档才计交易——[观察档, 买入档) 只算提醒不算信号；≥ 买入档触发（含同分，判据是 ≥）。
//	B. 判据口径会不会腐：打分本体 scoreDay 与实盘 combat_agent.MomentumScore 逐字相等
//	   （实盘双阈值 观察 60 / 买入 75 + "买入档不低于观察档"的夹子、日线 MACD 近似分钟 MACD
//	   的三条取数路径必须同分、MACD 全零丢弃、日K 不足 30 根不参评、盘中量比折算抵消）。
//	C. 有状态适配器的装配（§RFIX-1 的教训对动量同样成立）：MACD 序列必须**逐股**重算，
//	   跨股复用会污染分数甚至越界；兄弟适配器的序列也不能被动量这一遍写坏。
//
// 登记侧同样钉：动量在 BuiltinStrategies / adapterID / 显示名 / 近似说明这套登记里，
// 且**不再**出现在 UnsurveyedLiveForms() 里（它此前是"实盘能下单、排摸量不到"的盲区）；
// 但 DefaultDisabledBuiltins 这套机制本身要留着——下一个量不到的战法仍须以
// "adapter_disabled_by_default"/"no_replay_adapter" 两种状态之一显形，不许静默消失。
//
// 出场口径也钉：动量实盘没有专属 CheckExit（持仓走通用移动止盈回退），回放必须复用
// genericReplayExit 的 8%/15 日，绝不在适配器里另写一套出场。
//
// English: pinned tests for the momentum replay adapter after its criteria were rewritten to live
// semantics (2026-09-24). Group A pins the three live semantics actually landing — same-stock
// fallback exclusivity (both the replay and the sweep pre-computation), same-day-close entry, and
// trading only at the live BUY threshold. Group B pins byte-equality with combat_agent.MomentumScore
// (dual thresholds, the daily-MACD-for-minute-MACD approximation across all three fetch paths,
// warm-up/volume proration). Group C pins the per-stock MACD re-computation (§RFIX-1) for both the
// adapter and its probed siblings. Registration is pinned too: momentum is no longer an
// unsurveyable live form, while the disabled/missing-status mechanism must stay intact. Exit is
// pinned to the generic trailing-stop/timeout rule.
package btreplay

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// beijing 北京时间固定时区：折算测试要按"北京几点"叙述，避免 UTC 心算出错。
var beijing = time.FixedZone("CST", 8*3600)

// buildMomentumBars 造一段日K：前 n-1 根每天缓涨 pct%、成交量 volBase，最后一根按 lastChg%
// （相对前收）放量 lastVolMult 倍。缓涨保证走势四维（站上 MA5/MA10、MA5>MA10、5 日上行）全中，
// 末根的涨幅/量能决定量价档，日线 MACD 因末根加速而呈多头（DIF>DEA>0、红柱）。
func buildMomentumBars(n int, pct, volBase, lastChg, lastVolMult float64) []data.KLine {
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	ks := make([]data.KLine, 0, n)
	close := 10.0
	for i := 0; i < n; i++ {
		prev := close
		chg := pct
		vol := volBase
		if i == n-1 {
			chg, vol = lastChg, volBase*lastVolMult
		}
		close = prev * (1 + chg/100)
		open := prev * (1 + 0.1/100)
		ks = append(ks, data.KLine{
			Date: start.AddDate(0, 0, i), Open: open, High: maxF(close, open) * 1.001,
			Low: minF(close, open) * 0.995, Close: close, Volume: vol, Amount: close * vol,
		})
	}
	return ks
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// defaultMomentumCfg 出厂默认权重与双阈值（与 config.NewManager("") 的动量段一致）。
func defaultMomentumCfg() config.MomentumConfig {
	return config.MomentumConfig{
		VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30,
		SignalThreshold: 60, BuySignalThreshold: 75,
	}
}

// scoreOf 用**判据本体** scoreDay 算出当日动量分与是否达买入档。
// 为什么不改走 Trigger：Trigger 现在只是薄委托，档位/口径断言要钉在实现体上，
// 这样"委托被改坏（比如把观察档当成触发）"与"口径本身腐化"两类回归能被分开定位。
// 调用方已自造 macdSeries（测近似口径 1 的取数路径）时不覆盖其游标。
func scoreOf(t *testing.T, ad *momentumAdapter, ks []data.KLine) (float64, bool) {
	t.Helper()
	if ad.macdSeries == nil {
		ad.macdSeries = data.CalcMACDSeries(ks)
		ad.curIdx = len(ks) - 1
	}
	meta, fired := ad.scoreDay(ks, ks[len(ks)-2].Close)
	if meta == nil {
		return -1, fired
	}
	s, ok := meta["score"]
	if !ok {
		t.Fatalf("scoreDay 未回传 meta.score：%+v", meta)
	}
	return s, fired
}

// buildSurgeBars 在 n 根缓涨日K里，把 surgeAt 指定的交易日改成"放量上涨 lastChg% / 量能 ×lastVolMult"，
// 其余日维持 buildMomentumBars 的缓涨口径（走势四维满格、无量价爆发）。
// 为什么需要它：backtestStock 的判定循环只走到倒数第二根，而 buildMomentumBars 把爆发日固定在
// **最后一根**——用它测"入场时点/兜底互斥"会永远量不到触发（测试会假绿）。
func buildSurgeBars(n int, surgeAt map[int]bool, pct, volBase, lastChg, lastVolMult float64) []data.KLine {
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	ks := make([]data.KLine, 0, n)
	closeP := 10.0
	for i := 0; i < n; i++ {
		prev := closeP
		chg, vol := pct, volBase
		if surgeAt[i] {
			chg, vol = lastChg, volBase*lastVolMult
		}
		closeP = prev * (1 + chg/100)
		open := prev * (1 + 0.1/100)
		ks = append(ks, data.KLine{
			Date: start.AddDate(0, 0, i), Open: open, High: maxF(closeP, open) * 1.001,
			Low: minF(closeP, open) * 0.995, Close: closeP, Volume: vol, Amount: closeP * vol,
		})
	}
	return ks
}

// peerStub 兄弟战法桩：只在 fireAt 里指定的信号日出手，用于精确复现实盘"同标的当日已被兄弟占用"。
// 同时记录 prepareStock 的调用（钉 §RFIX-1 对兄弟同样成立：动量这一遍必须逐股给兄弟重算序列，
// 不能拿上一只股票的序列判今天）。
type peerStub struct {
	name      string
	fireAt    map[int]bool
	prepared  int
	lastLen   int
	triggered int // 被回查次数（钉"只在动量自己达档那一刻才回查兄弟"）
}

func (p *peerStub) Name() string { return p.name }

func (p *peerStub) Trigger(ks []data.KLine, _, _ float64) (map[string]float64, bool) {
	i := len(ks) - 1
	p.triggered++
	if p.fireAt[i] {
		return map[string]float64{"highest_price": ks[i].Close, "score": 1}, true
	}
	return nil, false
}

func (p *peerStub) Exit(_ *strategy.ExitContext, _ []strategy.KLine) (*strategy.ExitResult, bool) {
	return nil, false
}

func (p *peerStub) prepareStock(ks []data.KLine) { p.prepared++; p.lastLen = len(ks) }
func (p *peerStub) setDay(int)                   {}

// 编译期确认：桩满足 adapter 与 dayScoped 两份契约。
// 它**故意不实现** FallbackTier——未实现即"非兜底档"，正是兄弟战法应有的形态
// （setFallbackPeers 把它留在兄弟清单里，而不是当自己是等名额的那个）。
var (
	_ adapter   = (*peerStub)(nil)
	_ dayScoped = (*peerStub)(nil)
)

// allDaySet 生成"每天都出手"的 fireAt 集合（测"动量不达档时一次都不该回查兄弟"）。
func allDaySet(n int) map[int]bool {
	m := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		m[i] = true
	}
	return m
}

// momentumFireDays 复刻 backtestStock 的判定窗口（第 29 根起、到倒数第二根止），返回动量达买入档
// 的日索引。测试据此把"应不应该有交易"写成精确断言，而不是靠猜 fixture 会触发几次。
func momentumFireDays(t *testing.T, ks []data.KLine) []int {
	t.Helper()
	ad := &momentumAdapter{cfg: defaultMomentumCfg(), curIdx: -1}
	ad.prepareStock(ks)
	var days []int
	for i := 29; i < len(ks)-1; i++ {
		ad.setDay(i)
		if _, fired := ad.Trigger(ks[:i+1], ks[i-1].Close, 0); fired {
			days = append(days, i)
		}
	}
	return days
}

// TestMomentumLiveSemanticsInReplay 锁三条实盘语义在回放主循环里真的落地（A 组主体）：
//
//	② 当日撮合：入场日 = 信号日、入场价 = 信号日收盘（不是次日开盘）；
//	① 兜底互斥：同标的当日兄弟出了信号 ⇒ 动量一笔都不入；兄弟没出手 ⇒ 一笔不少；
//	回查开销：兄弟只在动量自己达档那一刻被问一次（不是每个交易日都跑一遍兄弟）。
func TestMomentumLiveSemanticsInReplay(t *testing.T) {
	surge := map[int]bool{40: true}
	ks := buildSurgeBars(60, surge, 0.3, 1_000_000, 6, 2.2)
	newAd := func() *momentumAdapter { return &momentumAdapter{cfg: defaultMomentumCfg(), curIdx: -1} }

	// 前置对照：fixture 在信号日确实达买入档（否则下面的"有/没有交易"都是空断言）。
	probe := newAd()
	probe.macdSeries = data.CalcMACDSeries(ks)
	probe.curIdx = 40
	if _, fired := probe.Trigger(ks[:41], ks[39].Close, 0); !fired {
		t.Fatal("fixture 失效：爆发日 40 未达动量买入档，本测试将退化为空断言")
	}
	// 判定窗口内动量达档的日子（下面几处断言都以它为准，不靠猜"应该只触发一次"）。
	fireDays := momentumFireDays(t, ks)
	if len(fireDays) == 0 || fireDays[0] != 40 {
		t.Fatalf("达档日=%v，期望首个是爆发日 40（fixture 已变，断言需重算）", fireDays)
	}

	// ① 无兄弟在场（Options.fallbackPeers 为空）→ 动量正常入场，首笔就在达档日。
	trades := (&Options{}).backtestStock("600001", ks, newAd(), nil)
	if len(trades) == 0 {
		t.Fatal("无兄弟占用时动量一笔都没入场，兜底/撮合路径被写死了")
	}
	tr := trades[0]
	// ② 当日撮合：入场日就是信号日 40，入场价就是它的收盘价。
	if want := ks[40].Date.Format("20060102"); tr.Date != want {
		t.Errorf("入场日=%s，期望信号日当日 %s（实盘动量在分数产生的那一刻撮合，不是次日）", tr.Date, want)
	}
	if !nearly(tr.Entry, ks[40].Close) {
		t.Errorf("入场价=%.6f，期望当日收盘 %.6f（次日开盘是 %.6f，说明还在按旧口径入场）", tr.Entry, ks[40].Close, ks[41].Open)
	}
	// §RFIX-1 对动量成立：回放必须逐股预计算日线 MACD 序列，且序列长度等于本股的 K 线根数。
	ad2 := newAd()
	(&Options{}).backtestStock("600002", ks, ad2, nil)
	if len(ad2.macdSeries) != len(ks) {
		t.Errorf("动量的 MACD 序列未按本股预计算：%d 项 vs 本股 %d 根", len(ad2.macdSeries), len(ks))
	}

	// ① 兜底互斥：兄弟在**同一信号日**出手 ⇒ 动量一笔都不能入（实盘轮不到它下单）。
	blocker := &peerStub{name: "兄弟桩", fireAt: map[int]bool{40: true}}
	oBlocked := &Options{fallbackPeers: []adapter{blocker}}
	if got := oBlocked.backtestStock("600003", ks, newAd(), nil); len(got) != 0 {
		t.Errorf("兄弟当日已出手时动量仍入场 %d 笔：%+v（兜底互斥未落地）", len(got), got)
	}
	if blocker.prepared != 1 {
		t.Errorf("兄弟的 prepareStock 调了 %d 次，期望每股 1 次（§RFIX-1 装配点）", blocker.prepared)
	}
	if blocker.lastLen != len(ks) {
		t.Errorf("兄弟拿到的序列长度=%d，期望本股 %d 根", blocker.lastLen, len(ks))
	}
	// 回查只挂在"动量自己达档"的那一刻：次数不可能超过达档日数（逐日都回查会远大于它）。
	if blocker.triggered == 0 || blocker.triggered > len(fireDays) {
		t.Errorf("兄弟被回查 %d 次，应在 1~%d（动量达档日数）之间；大于它说明没达档的日子也在回查",
			blocker.triggered, len(fireDays))
	}
	// 桩必须是"非兜底档"，否则 setFallbackPeers 会把它从兄弟清单里剔掉、这道门就空转了。
	var asAdapter adapter = blocker
	if _, isFB := asAdapter.(fallbackTierAdapter); isFB {
		t.Error("peerStub 不该实现 FallbackTier：兄弟不能被判成兜底档")
	}

	// 兄弟在场但**别的日子**才出手 → 不构成占用，动量照旧在那一天入场（防"任一信号即封杀"）。
	otherDay := &peerStub{name: "兄弟桩", fireAt: map[int]bool{33: true}}
	oOther := &Options{fallbackPeers: []adapter{otherDay}}
	gotOther := oOther.backtestStock("600004", ks, newAd(), nil)
	if len(gotOther) == 0 || gotOther[0].Date != ks[40].Date.Format("20060102") {
		t.Errorf("兄弟在其它交易日出手不应占用动量名额，得到 %+v", gotOther)
	}

	// 反证：动量整天不达档的平静序列上，一次都不该回查兄弟（开销只落在候选笔数上）。
	calm := buildSurgeBars(60, nil, 0.3, 1_000_000, 6, 2.2)
	if len(momentumFireDays(t, calm)) != 0 {
		t.Fatal("平静序列达了动量买入档，'不回查兄弟'的反证将失效（需重造 fixture）")
	}
	idle := &peerStub{name: "兄弟桩", fireAt: allDaySet(60)}
	if got := (&Options{fallbackPeers: []adapter{idle}}).backtestStock("600005", calm, newAd(), nil); len(got) != 0 {
		t.Errorf("平静序列不该有动量交易，得到 %+v", got)
	}
	if idle.triggered != 0 {
		t.Errorf("动量未达档仍回查兄弟 %d 次（全池开销会翻数倍）", idle.triggered)
	}
}

// TestMomentumFallbackExclusivityInSweepPrecompute 锁兜底互斥在**扫参预计算**路径同样落地：
// 两处必须同一道门——只在 backtestStock 挡、sweepTriggersOf 漏掉的话，网格会给一条实盘
// 轮不到下单的路径寻优，选出来的"冠军参数"对应的是一批实盘不存在的单子。
// 同时钉入场时点：动量的 trigger.entryIdx 必须等于 sigIdx（当日收盘），缺省战法等于 sigIdx+1。
func TestMomentumFallbackExclusivityInSweepPrecompute(t *testing.T) {
	surge := map[int]bool{40: true}
	ks := buildSurgeBars(60, surge, 0.3, 1_000_000, 6, 2.2)
	newAd := func() *momentumAdapter { return &momentumAdapter{cfg: defaultMomentumCfg(), curIdx: -1} }

	// 无兄弟：每个达档日一笔触发，且 entryIdx==sigIdx（当日撮合）、入场价是当日收盘。
	fireDays := momentumFireDays(t, ks)
	o := &Options{}
	trigs := o.sweepTriggersOf(newAd(), 0, "600001", ks, nil, nil)
	if len(trigs) != len(fireDays) {
		t.Fatalf("预计算触发 %d 笔 ≠ 达档日 %v（%d 天）——两处判据不同源", len(trigs), fireDays, len(fireDays))
	}
	for _, tg := range trigs {
		if tg.entryIdx != tg.sigIdx {
			t.Fatalf("动量 entryIdx=%d sigIdx=%d，当日撮合应相等", tg.entryIdx, tg.sigIdx)
		}
		if !nearly(tg.entry, ks[tg.sigIdx].Close) {
			t.Errorf("预计算入场价=%.6f，期望当日收盘 %.6f", tg.entry, ks[tg.sigIdx].Close)
		}
	}

	// 有兄弟当日占用：一笔都不剩（与 backtestStock 同一道门）。
	oBlocked := &Options{fallbackPeers: []adapter{&peerStub{name: "兄弟桩", fireAt: surge}}}
	if got := oBlocked.sweepTriggersOf(newAd(), 0, "600001", ks, nil, nil); len(got) != 0 {
		t.Errorf("扫参路径兜底互斥未落地，仍预算出 %d 笔：%+v", len(got), got)
	}

	// 缺省口径（未声明 SameDayEntry 的战法）必须还是"次日开盘"——重写没把别的战法带跑。
	nextDay := (&Options{}).sweepTriggersOf(&peerStub{name: "缺省桩", fireAt: surge}, 0, "600001", ks, nil, nil)
	if len(nextDay) != 1 || nextDay[0].entryIdx != 41 || !nearly(nextDay[0].entry, ks[41].Open) {
		t.Errorf("缺省入场口径被改动（期望信号日 40 → 次日开盘 41），得到 %+v", nextDay)
	}
}

// TestMomentumScoreDayBuyThresholdOnly 锁判据本体的档位：达**买入档**（出厂 75，实盘此档才发 buy
// 进动量池）才算可交易信号；落在 [观察档 60, 买入档 75) 只观察不下单，因此不算触发。
// 这里把买入档两侧各钉一次（含"高一分就不触发"），防止 ≥ 被改成 > 或把 watch 档当交易信号。
func TestMomentumScoreDayBuyThresholdOnly(t *testing.T) {
	strong := buildMomentumBars(40, 0.3, 1_000_000, 6, 2.2) // 量价双顶格 + 日线 MACD 多头 + 走势满格
	mid := buildMomentumBars(40, 0.3, 1_000_000, 0.5, 1.1)  // 同趋势，末根只小涨 0.5%、量 1.1 倍 → 量价档掉两档

	ad := &momentumAdapter{cfg: defaultMomentumCfg(), curIdx: -1}
	sStrong, firedStrong := scoreOf(t, ad, strong)
	sMid, firedMid := scoreOf(t, ad, mid)
	t.Logf("动量分：强=%.0f 中=%.0f（买入档 %.0f / 观察档 %.0f）",
		sStrong, sMid, ad.buyThreshold(), ad.watchThreshold())

	if sStrong < 75 {
		t.Fatalf("强动量样例分数 %.0f 未达买入档 75，fixture 失效（需重造日K）", sStrong)
	}
	if !firedStrong {
		t.Errorf("强动量（%.0f≥75）应判达买入档", sStrong)
	}
	if sMid < 60 || sMid >= 75 {
		t.Fatalf("中等动量样例分数 %.0f 应落在观察档 [60,75)，fixture 失效", sMid)
	}
	if firedMid {
		t.Errorf("观察档分数 %.0f 不应算可交易信号（实盘此档只 watch，不下单）", sMid)
	}
	// 观察档的分数必须仍然回传（"只观察"不等于"什么都不知道"，排摸据此报提醒强度）。
	if meta, _ := (&momentumAdapter{cfg: ad.cfg, curIdx: -1}).scoreDay(mid, mid[len(mid)-2].Close); meta == nil {
		t.Errorf("观察档样例也应回传 meta（含 score），得到 nil")
	}

	// 买入档两侧的精确边界：阈值钉在实测分（含）→ 触发；钉高一分 → 不触发。
	at := &momentumAdapter{cfg: ad.cfg, curIdx: -1}
	at.cfg.BuySignalThreshold = sStrong
	if _, ok := scoreOf(t, at, strong); !ok {
		t.Errorf("买入档=%.0f 时同分样例应触发（判据是 ≥，不是 >）", sStrong)
	}
	above := &momentumAdapter{cfg: ad.cfg, curIdx: -1}
	above.cfg.BuySignalThreshold = sStrong + 1
	if _, ok := scoreOf(t, above, strong); ok {
		t.Errorf("买入档=%.0f 时分数 %.0f 不应触发（差一分就是另一档）", sStrong+1, sStrong)
	}
}

// TestMomentumThresholdFallbacks 锁阈值缺省：配置零值回退实盘同一对默认 60/75，
// 且买入档不得低于观察档（实盘 momentumBuySignalThreshold 的同一把夹子）。
func TestMomentumThresholdFallbacks(t *testing.T) {
	zero := &momentumAdapter{curIdx: -1}
	if zero.watchThreshold() != 60 || zero.buyThreshold() != 75 {
		t.Errorf("零配置应回退 60/75，得到 %v/%v", zero.watchThreshold(), zero.buyThreshold())
	}
	low := &momentumAdapter{curIdx: -1, cfg: config.MomentumConfig{SignalThreshold: 80, BuySignalThreshold: 70}}
	if low.buyThreshold() != 80 {
		t.Errorf("买入档不得低于观察档，应夹到 80，得到 %v", low.buyThreshold())
	}
}

// TestMomentumDailyMACDApproximation 锁近似口径 1：实盘 macdRatio 读 5 分钟 MACD，判据本体喂的是
// **日线** MACD（data.CalcMACD）。三条取数路径（逐股预计算序列 + 游标 / 缺序列兜底重算 /
// 游标越界）必须同分——否则将来放行后扫参预计算与生产回放量出的是两份动量分；同时分数必须逐字等于
// "手工把日线 MACD 塞进 MinuteMACD 槽位后调用实盘打分函数"，钉住"没有另写一套打分"。
func TestMomentumDailyMACDApproximation(t *testing.T) {
	ks := buildMomentumBars(40, 0.3, 1_000_000, 6, 2.2)
	last := ks[len(ks)-1]
	prevClose := ks[len(ks)-2].Close
	chg := chgPct(last.Close, prevClose)

	withSeries := &momentumAdapter{cfg: defaultMomentumCfg(), curIdx: -1}
	sSeries, firedSeries := scoreOf(t, withSeries, ks)

	// 路径二：序列为 nil → 兜底逐日重算，分数与档位结论都必须一致。
	fallback := &momentumAdapter{cfg: withSeries.cfg, curIdx: -1}
	metaF, firedFallback := fallback.scoreDay(ks, prevClose)
	if metaF == nil {
		t.Fatal("MACD 序列缺失时应兜底重算而非丢弃信号")
	}
	if metaF["score"] != sSeries || firedFallback != firedSeries {
		t.Errorf("兜底重算与预计算序列口径不一致：%.0f/%v vs %.0f/%v",
			metaF["score"], firedFallback, sSeries, firedSeries)
	}

	// 路径三：游标越界（序列被错位复用）→ 钳制回退重算，同分且不 panic。
	oob := &momentumAdapter{cfg: withSeries.cfg, macdSeries: []data.MACD{{}}, curIdx: 9}
	metaO, _ := oob.scoreDay(ks, prevClose)
	if metaO == nil || metaO["score"] != sSeries {
		t.Errorf("游标越界应钳制回退重算并保持同分，得到 %+v", metaO)
	}

	daily := data.CalcMACD(ks)
	if daily.DIF <= 0 || daily.DEA <= 0 || daily.DIF <= daily.DEA {
		t.Fatalf("fixture 的日线 MACD 应为多头（DIF>DEA>0），得到 %+v", daily)
	}

	// 与"手工喂日线 MACD + 实盘打分函数"逐字相等（同一函数、同一输入，无第二套口径）。
	ref := combat_agent.MomentumScore(&strategy_engine.StockMarketData{
		Price:     last.Close,
		ChangePct: chg,
		KLines:    ks,
		Quote: &data.StockInfo{Price: last.Close, Open: last.Open, High: last.High, Low: last.Low,
			Close: last.Close, Volume: momentumQuoteVolume(time.Now(), last.Volume), Amount: last.Amount, ChangePct: chg},
		MinuteMACD: daily,
	}, withSeries.cfg)
	if ref != sSeries {
		t.Errorf("判据分数 %.0f 与实盘打分函数参考值 %.0f 不一致（动量分被第二套口径污染）", sSeries, ref)
	}

	// MACD 分量确实进了打分：把 MinuteMACD 换成空头后分数必须掉下来（差 MACD 权重量级）。
	bear := &momentumAdapter{cfg: withSeries.cfg, macdSeries: []data.MACD{{DIF: -1, DEA: 1, Bar: -2}}, curIdx: 0}
	sBear, firedBear := scoreOf(t, bear, ks)
	if sBear >= sSeries {
		t.Errorf("日线 MACD 未被用作输入：多头 %.0f vs 空头 %.0f", sSeries, sBear)
	}
	if firedBear {
		t.Errorf("MACD 转空头的样例不应达买入档，分数 %.0f", sBear)
	}

	// 三项全零＝实盘 momentumDataValid 判"MACD 无数据"，回放同样丢弃（不拿零序列凑分触发）。
	empty := &momentumAdapter{cfg: withSeries.cfg, macdSeries: []data.MACD{{}}, curIdx: 0}
	if meta, fired := empty.scoreDay(ks, prevClose); fired || meta != nil {
		t.Errorf("MACD 无数据（DIF=DEA=Bar=0）不应出信号，得到 %v %+v", fired, meta)
	}

	// 日K不足 30 根不参与（日线 MACD 的 EMA26+DEA9 预热不够，比实盘 ≥5 根更严＝近似口径代价）。
	short := buildMomentumBars(29, 0.3, 1_000_000, 6, 2.2)
	if meta, fired := (&momentumAdapter{cfg: withSeries.cfg, curIdx: -1}).scoreDay(short, short[len(short)-2].Close); fired || meta != nil {
		t.Errorf("日K 29 根未达预热门槛，不应出信号，得到 %v %+v", fired, meta)
	}
}

// TestMomentumVolumeProrationIsRunTimeInvariant 锁近似口径 3：实盘 volumePriceRatio 按
// time.Now() 把盘中累计量折算成全天等值；判据本体喂的是**全日量**，必须先按同一时间窗缩小抵消，
// 否则白天跑一次回放会把全市场量比放大数倍、人人达标（同一份历史数据换个钟点出两套结论）。
func TestMomentumVolumeProrationIsRunTimeInvariant(t *testing.T) {
	full := 240.0 // 全日交易分钟数（上午 120 + 下午 120）
	tests := []struct {
		name      string
		h, m      int
		wantRatio float64 // 折算后累计量 ÷ 全日量
	}{
		{"北京 09:00 盘前按1分钟兜底", 9, 0, 1 / full},
		{"北京 10:30 已流逝 60 分钟", 10, 30, 60 / full},
		{"北京 12:30 午休取上午 120", 12, 30, 120 / full},
		{"北京 14:30 午后 90 分钟", 14, 30, 210 / full},
		{"北京 18:00 盘后不折算", 18, 0, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2024, 6, 5, tc.h, tc.m, 0, 0, beijing)
			got := momentumQuoteVolume(now, 1_000_000)
			if diff := got - 1_000_000*tc.wantRatio; diff > 1 || diff < -1 {
				t.Errorf("折算后累计量=%.1f，期望 %.1f（比例 %.6f）", got, 1_000_000*tc.wantRatio, tc.wantRatio)
			}
			// 抵消性：折算量再经实盘的 240/已流逝分钟 放大必须回到全日量（口径与 15:00 收盘一致）。
			elapsed := replayElapsedTradeMinutes(now)
			if elapsed <= 0 {
				elapsed = 1
			}
			if back := got * (240 / elapsed); back < 999_999 || back > 1_000_001 {
				t.Errorf("抵消失败：%.1f ×(240/%.0f)=%.1f，应为全日量 1e6", got, elapsed, back)
			}
		})
	}
	if v := momentumQuoteVolume(time.Date(2024, 6, 5, 14, 30, 0, 0, beijing), 0); v != 0 {
		t.Errorf("零量应返回 0（不得凭空造出成交量），得到 %v", v)
	}
}

// TestMomentumRegisteredAndSurveyable 锁登记状态与盲区机制：动量已回到"默认可回放"集合
// （不再是排摸盲区），但"缺适配器 / 已实现但停用"两套状态与差集锚点机制必须原样留着——
// 下一个进实盘白名单却量不到的战法仍要显形，不许静默变成"表里少一行"。
func TestMomentumRegisteredAndSurveyable(t *testing.T) {
	// ①枚举与登记。
	if !contains(BuiltinStrategies(), "momentum") {
		t.Errorf("momentum 不在 BuiltinStrategies()=%v：内置战法清单是 all 模式与排摸的唯一出处", BuiltinStrategies())
	}
	if contains(DefaultDisabledBuiltins(), "momentum") {
		t.Errorf("momentum 仍在 DefaultDisabledBuiltins()=%v：判据已按实盘语义重写，还标停用就是把已量通的路径重新藏回盲区",
			DefaultDisabledBuiltins())
	}
	if contains(UnsurveyedLiveForms(), "momentum") {
		t.Errorf("UnsurveyedLiveForms()=%v 仍报 momentum：动量已被排摸，不该留在盲区名单", UnsurveyedLiveForms())
	}
	if got := UnsurveyedLiveFormStatus("momentum"); got != "" {
		t.Errorf("momentum 状态=%q，期望空串（默认就被排摸覆盖）", got)
	}
	if got := UnsurveyedLiveFormStatus("double_bump"); got != "" {
		t.Errorf("double_bump 默认在回放集合里，状态应为空串，得到 %q", got)
	}
	// 机制没跟着塌：未登记战法仍报"缺适配器"，而不是被空清单吞掉。
	if got := UnsurveyedLiveFormStatus("totally_unknown_form"); got != "no_replay_adapter" {
		t.Errorf("未登记的战法状态应为 no_replay_adapter（两种缺失必须能分开），得到 %q", got)
	}
	// ②适配器本体登记项齐全（构造、稳定 ID、显示名、近似说明）。
	ad, err := newAdapter("momentum", false, 0)
	if err != nil {
		t.Fatalf("newAdapter(momentum) 失败: %v", err)
	}
	if got := adapterID(ad); got != "momentum" {
		t.Errorf("adapterID=%s，期望 momentum（稳定 ID 是排摸定位键与扫参落库键）", got)
	}
	if ad.Name() != "动量" {
		t.Errorf("Name=%s，期望与实盘显示名同字面的「动量」", ad.Name())
	}
	if got := BuiltinDisplayName("momentum"); got != "动量" {
		t.Errorf("BuiltinDisplayName(momentum)=%s，期望「动量」（排摸行名称不得依赖回放量出什么）", got)
	}
	if got := BuiltinDisplayName("no_such_strategy"); got != "" {
		t.Errorf("未知战法应回空串由调用方兜底，得到 %q", got)
	}
	// ③近似说明必须点名"实盘语义已重写 + 仍量不到的那几件事"：
	//   只写"approx"会被读成"随便近似了一下"，只写"live semantics"会被读成"完全精确"。
	note := ReplayApproxNote("momentum")
	for _, want := range []string{"criteria rewritten to live semantics", "SIGNAL DAY CLOSE",
		"len(sigs)==0", "5-minute MACD replaced by daily MACD", "momentum-improvement gate"} {
		if !strings.Contains(note, want) {
			t.Errorf("momentum 近似说明缺少关键口径 %q，读者会误判这一行的可比性：%s", want, note)
		}
	}
	if strings.Contains(note, "not replayed by default") {
		t.Errorf("momentum 近似说明仍写着「默认不回放」，与已接入回放的事实矛盾：%s", note)
	}
	if note := ReplayApproxNote("double_bump"); note != "" {
		t.Errorf("双响炮是纯日K完整回放，不应有近似说明，得到 %s", note)
	}
}

// TestSetFallbackPeersKeepsOnlySiblings 锁兄弟清单的装配：兜底档自己不进清单（否则自己挡自己、
// 动量永远入不了场），非兜底档一个不少；没有兜底档时不装配（其它战法一次判断都不多走）。
func TestSetFallbackPeersKeepsOnlySiblings(t *testing.T) {
	mom := &momentumAdapter{cfg: defaultMomentumCfg(), curIdx: -1}
	sib := &peerStub{name: "兄弟桩"}
	var o Options
	o.setFallbackPeers([]adapter{sib, mom})
	if len(o.fallbackPeers) != 1 || o.fallbackPeers[0] != adapter(sib) {
		t.Errorf("兄弟清单=%+v，期望只含非兜底档的那一个（兜底档自己不能占自己的名额）", o.fallbackPeers)
	}
	if hasFallbackTier([]adapter{sib, &doubleBumpAdapter{}}) {
		t.Error("清单里没有兜底档时 hasFallbackTier 应为 false（避免无谓装配）")
	}
	if !hasFallbackTier([]adapter{sib, mom}) {
		t.Error("含动量时 hasFallbackTier 应为 true，否则兜底互斥门根本不会装配")
	}
}

// TestMomentumExitIsGenericTrailing 锁出场口径：动量实盘没有专属 CheckExit（持仓走通用
// 移动止盈回退），回放同口径 8%/15 天——未达回撤线不得提前平仓，达线当日必须平仓；
// 扫参覆盖（trailOverride/holdOverride）必须生效，这样判据将来放行时出场参数是现成的。
func TestMomentumExitIsGenericTrailing(t *testing.T) {
	ad := &momentumAdapter{curIdx: -1}
	ctx := &strategy.ExitContext{
		CostPrice: 10, CurPrice: 10.5, EntryMeta: map[string]float64{"highest_price": 11.0},
		EntryAt: "2024-01-10", Now: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
	}
	if res, ex := ad.Exit(ctx, nil); ex {
		t.Errorf("盈利 5%%、距高点回撤 4.5%% 未达 8%% 移动止盈线，不应平仓，得到 %+v", res)
	}
	ctx.CurPrice = 10.0 // 从高点 11.0 回撤 9.09% → 触发
	res, ex := ad.Exit(ctx, nil)
	if !ex || res == nil {
		t.Fatalf("回撤 9.09%% 应触发移动止盈，得到 %+v/%v", res, ex)
	}
	if res.Reason != "回撤止损(移动止盈)" {
		t.Errorf("平仓理由=%s，期望通用移动止盈", res.Reason)
	}
	tr, hd := 5.0, 3
	adO := &momentumAdapter{curIdx: -1, trailOverride: &tr, holdOverride: &hd}
	ctxO := &strategy.ExitContext{
		CostPrice: 10, CurPrice: 10.2, EntryMeta: map[string]float64{"highest_price": 11.0},
		EntryAt: "2024-01-10", Now: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
	}
	if r, ex := adO.Exit(ctxO, nil); !ex || r.Reason != "回撤止损(移动止盈)" {
		t.Errorf("trailOverride=5 时回撤 7.3%% 应触发移动止盈，得到 %+v/%v", r, ex)
	}
	// 超期：未达回撤线但持仓 ≥ holdOverride 天 → 超期离场
	ctxH := &strategy.ExitContext{
		CostPrice: 10, CurPrice: 10.1, EntryMeta: map[string]float64{"highest_price": 10.2},
		EntryAt: "2024-01-10", Now: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
	}
	if r, ex := adO.Exit(ctxH, nil); !ex || r.Reason != "持仓超期离场" {
		t.Errorf("holdOverride=3 时持仓 5 天应超期离场，得到 %+v/%v", r, ex)
	}
}

// TestMomentumDeclaresLiveCapabilities 锁两份能力声明本身都在位且只对动量成立：
// 判据写得再对，只要哪天丢了声明，就会**静默**退化成"次日开盘 + 谁都不占它名额"的旧近似口径
// （回放照跑、数字照出，只是不再是实盘那批单子）——所以这里按接口存在性直接钉。
func TestMomentumDeclaresLiveCapabilities(t *testing.T) {
	ad, err := newAdapter("momentum", false, 0)
	if err != nil {
		t.Fatalf("newAdapter(momentum): %v", err)
	}
	if fb, ok := ad.(fallbackTierAdapter); !ok || !fb.FallbackTier() {
		t.Error("动量必须声明 FallbackTier()=true：实盘它是兜底档，不声明就不会有跨战法互斥门控")
	}
	if sd, ok := ad.(sameDayEntryAdapter); !ok || !sd.SameDayEntry() {
		t.Error("动量必须声明 SameDayEntry()=true：实盘在分数产生的那一刻撮合，不声明就退回次日开盘")
	}
	if _, ok := ad.(dayScoped); !ok {
		t.Error("动量必须实现 dayScoped：不实现就等于 MACD 序列永不逐股预计算（§RFIX-1 的成因）")
	}
	// 反向：另外四个内置形态战法都不该声明这两项（它们在实盘既非兜底档、也不当日撮合）。
	for _, id := range []string{"double_bump", "dragon", "dragon_return", "n_shape"} {
		x, xerr := newAdapter(id, false, 0)
		if xerr != nil {
			t.Fatalf("newAdapter(%s): %v", id, xerr)
		}
		if fb, ok := x.(fallbackTierAdapter); ok && fb.FallbackTier() {
			t.Errorf("%s 不应声明兜底档：它会把自己从兄弟清单里剔掉，动量的互斥门就空转了", id)
		}
		if sd, ok := x.(sameDayEntryAdapter); ok && sd.SameDayEntry() {
			t.Errorf("%s 不应声明当日撮合：实盘它是次日入场，改了会把全部历史数字挪动一格", id)
		}
	}
}

// contains 小工具：字符串切片成员判定（登记名单断言用，避免每处都写一遍三重否定循环）。
func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// 编译期确认：动量适配器满足 replay 的 adapter 契约。
var _ adapter = (*momentumAdapter)(nil)
