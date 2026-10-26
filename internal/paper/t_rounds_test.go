// t_rounds_test.go — §T-MODE 做 T 的行为锁（2026-10-26 §DYN-T 日内口径重写后的版本）。
//
// 对应方案 §6.1「paper/T3 发起点」组 1~13 条，逐条都配了"故意破坏→必红"的方向：
//
//	 1 日内基准用分时均线，不用成本/锚点（成本口径在单边行情里会整天满足阈值）
//	 2 近极值带（不接刀 / 不做半山腰卖）
//	 3 振幅门（振幅不够就注定配不上对）
//	 4 正 T 信号驱动 + 候选 B（老信号不许反复低吸）
//	 5 反 T 规则发起 + 否决门（有卖出裁决 / 距止盈止损太近 / 读数不可得）
//	 6 一次性消费 + 战略先判
//	6b 翻转不是防抖主力：三次新信号在 max_rounds=1 下只开一条腿
//	6c 流动性前置（含"读数不可得按不过"）
//	6d 反 T 环境门与当日总量帽（六轮 T6-2：分母取 min(总资产,持仓市值)）
//	 7 时段门（冷静期 / 最后开腿时刻）
//	 8 正 T 环境门 + 大盘急跌
//	 9 V 不可信 ⇒ 不做 T，也不回落成本
//	10 尾盘只有活跃买入信号才回补；亏损超阈值熔断；无信号转战略减仓
//	11 锚点三条锁（成本被 T 腿摊薄也不改判线；无锚点/来源不可信不开腿）
//	12 正 T 尾盘无条件卖出（减敞口不需许可）
//	13 做 T 收益独立：done 轮才有净额，且不并入整仓成本
//
// 时钟缝（pinTRClock）保留：不钉时钟会让"尾盘之后跑测试"必然变红，那种用例等于没有用例。
package paper

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// ── 夹具 ───────────────────────────────────────────────────────────────────────────────

// seedBase 种一条"昨日底仓"并把预算口径切成 dynamic（Q5：legacy 档下做 T 整轮禁用）。
// ⚠ 顺序硬要求：先按显式股数种底仓、再切 dynamic——dynamic 口径下 fillLocked 会忽略显式股数、
// 按池权益×单票上限定仓，先切口径会让夹具种出 3500 股而不是想要的那一档。
func seedBase(t *testing.T, code string, qty int) *Engine {
	t.Helper()
	e := partialTestEngine(t)
	if err := e.BuyExInPool(code, "测试股", "龙头", "dragon", 0, 10, qty, nil); err != nil {
		t.Fatalf("预置 %d 股底仓失败: %v", qty, err)
	}
	t1Ready(e)
	e.mu.Lock()
	e.cfg.BudgetMode = "dynamic"
	e.mu.Unlock()
	pinTRClock(e)
	return e
}

// pinTRClock 把时钟钉在当日 10:00（交易时段内、未到冷静期之外、也没到尾盘线）。
func pinTRClock(e *Engine) { pinTRClockAt(e, 10, 0) }

// pinTRClockAt 钉到指定时分（时段门与尾盘判定都要按分钟驱动）。
func pinTRClockAt(e *Engine, hour, minute int) {
	n := cntime.In(time.Now())
	pinned := time.Date(n.Year(), n.Month(), n.Day(), hour, minute, 0, 0, n.Location())
	e.mu.Lock()
	e.nowFn = func() time.Time { return pinned }
	e.mu.Unlock()
}

// tQuote 造一份"日内已经走出足够振幅"的行情快照：
// PrevClose=10、High=10.6、Low=9.4（振幅 12%）、成交额 8000 万、换手 3%（都高于流动性门槛），
// 分时均线由 Amount/Volume 给出＝v（与行情软件分时图上那条线同一定义）。
func tQuote(code string, price, v float64) map[string]*data.StockInfo {
	// 成交额＝V×成交量：量取 800 万股 ⇒ 约 8000 万元，稳在流动性前置（默认 3000 万）之上，
	// 免得各条锁其实测的是"成交额不足"。
	vol := 8e6
	return map[string]*data.StockInfo{code: {
		Code: code, Name: "测试股", Price: price,
		// 位置默认"既没贴着日内低点、也没离高点太远"，让各条锁各测各的门：
		// High 略高于现价（反 T 的近高点区成立），Low 留出 5% 余量（正 T 的"不接刀"成立）。
		High: price * 1.002, Low: price * 0.95, PrevClose: 10, Close: 10,
		Amount: v * vol, Volume: vol, Turnover: 3.0,
	}}
}

// tParams 默认参数（§8.8 出厂默认）+ 全部必需回调接线。
// 每个回调都是"读数的入口"，缺一个都会被判定链按 fail-closed 挡下，所以夹具必须给全。
func tParams() TParams {
	return TParams{
		Enabled: true, BuyFirst: true, SellFirst: true,
		MaxRounds: 1, CapPct: 50, MinSpreadPct: 2.0, ForceUnwindHM: "1450",
		MarketScale: 1.0, FeeBufferPct: 0.6,
		VWAPDevPct: 0.8, MinAmplitudePct: 2.5, NearExtremePct: 0.3, LimitDistancePct: 1.0,
		OpenCoolingMin: 15, LastOpenHM: "1400", VWAPCheckTolPct: 0.1,
		MinMarketScale: 0.40, SellEnvMinMarketScale: 0.20, MarketPlungePct: -2.0,
		IntradaySellCapTotalPct: 20, StopGuardPct: 1.5, LossBreakerPct: 3.0,
		MinAmountWan: 3000, MinTurnoverPct: 1.5, MinListedDays: 60,
		MinuteBasis:       func(code string) (float64, bool) { return minuteVOf(code) },
		NewBuySignal:      func(code string, since time.Time) (string, bool) { return sigFor(since) },
		ActiveBuySignal:   func(code string, since time.Time) (string, bool) { return sigFor(since) },
		ActiveSellVerdict: func(code string) bool { return sellVerdictOn },
		StopLineDistance:  func(code string, price float64) (float64, bool) { return stopDist, stopDistOK },
		StockMeta:         func(code string) (int, bool, bool) { return 1200, false, true },
		StrategicBusy:     func(code string) bool { return strategicBusy },
		StrategicGate:     func(code string, qty int) (int, string) { return qty, "闸门放行" },
	}
}

// 回调的可测状态（同包内测试直接改，避免到处传参数变体）。
var (
	minuteVNow    float64
	sellVerdictOn bool
	stopDist      = 5.0
	stopDistOK    = true
	strategicBusy bool
)

func minuteVOf(code string) (float64, bool) {
	if minuteVNow <= 0 {
		return 0, false
	}
	return minuteVNow, true
}

// sigFor 默认"总是有一个新信号"（每次调用给不同 ID，便于测一次性消费与配对回补）。
// "没有新信号 / 只有老信号"的用例各自覆盖回调来制造，since 参数仍然传下去，
// 这样 TestTRoundCandidateBBarsStaleSignal 能验证判定链确实把"上次回转结束时刻"交了出来。
var sigSeq int

func sigFor(since time.Time) (string, bool) {
	sigSeq++
	return fmt.Sprintf("SIG-%d", sigSeq), true
}

// ── 锁 1/2/3：日内位置 ──────────────────────────────────────────────────────────────────

// TestTRoundReferenceIsIntradayNotCost 发起点必须是分时均线：现价比**成本**低 5%（旧口径必然触发低吸），
// 但现价就在分时均线上（偏离 0%）⇒ 一律不开腿。把基准改回 AnchorOrCost() 这条当场红。
func TestTRoundReferenceIsIntradayNotCost(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000) // 锚点/成本 10
	// V=9.5、现价 9.52 ⇒ 相对成本跌 4.8%（旧口径会开），相对分时均线只偏 +0.21%（新口径不开）
	e.RunIntradayT(tQuote("300001", 9.52, 9.5), tParams())
	if len(e.tRounds) != 0 {
		t.Fatalf("成本口径复发：现价已在分时均线上方却仍低吸，got %+v", e.tRounds[0])
	}
	// 同一只票，现价跌到分时均线下方 1% ⇒ 开腿（证明上面那条不是"任何价都不开"）
	e.RunIntradayT(tQuote("300001", 9.4, 9.5), tParams())
	if len(e.tRounds) != 1 || e.tRounds[0].Leg != tRLegBuy {
		t.Fatalf("分时均线下方 1%% 应开正 T，got %+v", e.tRounds)
	}
}

// TestTRoundDevAndAmplitudeGates 偏离门（0.5% 不开 / 1.0% 开）与振幅门（振幅 2% < 2.5% 不开）。
func TestTRoundDevAndAmplitudeGates(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	q := tQuote("300001", 9.95, 10) // 只偏 0.5%
	e.RunIntradayT(q, tParams())
	if len(e.tRounds) != 0 {
		t.Fatalf("偏离 0.5%% 未达偏离门 0.8%%，不该开腿，got %+v", e.tRounds[0])
	}
	e2 := seedBase(t, "300001", 1000)
	narrow := tQuote("300001", 9.4, 10)
	narrow["300001"].High, narrow["300001"].Low, narrow["300001"].PrevClose = 9.45, 9.35, 10 // 振幅 1% < 2.5%
	e2.RunIntradayT(narrow, tParams())
	if len(e2.tRounds) != 0 {
		t.Fatalf("振幅不足 2.5%% 时两向都不开（开了也注定配不上对），got %+v", e2.tRounds[0])
	}
}

// TestTRoundNearExtremeBand 正 T 不接正在砸向日内低点的刀（现价贴着 Low）。
func TestTRoundNearExtremeBand(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	q := tQuote("300001", 9.4, 10)
	q["300001"].Low = 9.4 * 1.001 // 现价只高于低点 0.1% < 0.3% 带 ⇒ 视为正在砸向低点
	e.RunIntradayT(q, tParams())
	if len(e.tRounds) != 0 {
		t.Fatalf("已贴日内低点仍低吸＝接刀，got %+v", e.tRounds[0])
	}
	// 反 T 侧：涨过又回落过半（不在近高点区）也不许高抛
	e2 := seedBase(t, "300001", 1000)
	hill := tQuote("300001", 10.1, 10)
	hill["300001"].High = 10.6 // 涨过又回落过半：现价远低于 High×99.7%
	e2.RunIntradayT(hill, tParams())
	if len(e2.tRounds) != 0 {
		t.Fatalf("半山腰高抛（不在近高点区）应被位置门挡下，got %+v", e2.tRounds[0])
	}
}

// ── 锁 4/5/6/6b：信号侧 ──────────────────────────────────────────────────────────────────

// TestTRoundBuyNeedsFreshSignal 正 T 候选 B：没有新信号 ⇒ 日内条件全满足也不开；有 ⇒ 开。
func TestTRoundBuyNeedsFreshSignal(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	params := tParams()
	params.NewBuySignal = func(string, time.Time) (string, bool) { return "", false }
	e.RunIntradayT(tQuote("300001", 9.4, 10), params)
	if len(e.tRounds) != 0 {
		t.Fatalf("无活跃买入信号仍开买腿＝把日内低吸变成规则自动买，got %+v", e.tRounds[0])
	}
	e.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e.tRounds) != 1 || !strings.HasPrefix(e.tRounds[0].UsedSignalID, "SIG-") {
		t.Fatalf("有信号应开腿并记录消费的 signal_id，got %+v", e.tRounds)
	}
}

// TestTRoundCandidateBBarsStaleSignal 候选 B 的"专门否掉候选 A"那一半：
// 信号时间戳早于上一次回转结束时刻 ⇒ 不开（"存在任意活跃信号"的实现会当场红）。
func TestTRoundCandidateBBarsStaleSignal(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	var seenSince time.Time
	params := tParams()
	params.NewBuySignal = func(code string, since time.Time) (string, bool) {
		seenSince = since
		return "", false // 模拟"只有老信号"
	}
	e.RunIntradayT(tQuote("300001", 9.4, 10), params)
	if len(e.tRounds) != 0 {
		t.Fatalf("老信号不得开腿，got %+v", e.tRounds[0])
	}
	if seenSince.IsZero() {
		t.Fatal("判定链必须把『上一次该码回转结束时刻』传给信号回调，否则候选 B 无从成立")
	}
}

// TestTRoundSellVetoGates 反 T 否决门：活跃卖出裁决 / 距离太近 / 读数不可得 ⇒ 都不开；
// 两门都过 ⇒ 开，且**不要求**买入信号（破坏成"要求买入信号"必红）。
func TestTRoundSellVetoGates(t *testing.T) {
	minuteVNow = 0
	defer func() { sellVerdictOn, stopDist, stopDistOK = false, 5.0, true }()

	sellVerdictOn = true
	e := seedBase(t, "300001", 1000)
	e.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	if len(e.tRounds) != 0 {
		t.Fatalf("已有卖出裁决时 T 不该抢，got %+v", e.tRounds[0])
	}
	sellVerdictOn = false
	e2 := seedBase(t, "300001", 1000)
	stopDist = 1.0 // < 1.5%
	e2.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	if len(e2.tRounds) != 0 {
		t.Fatalf("距止盈止损 1%%<门槛 1.5%% 仍高抛＝T 抢在裁决前面，got %+v", e2.tRounds[0])
	}
	e3 := seedBase(t, "300001", 1000)
	stopDist, stopDistOK = 9.9, false // 读数不可得 ⇒ 按否决
	e3.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	if len(e3.tRounds) != 0 {
		t.Fatalf("否决门读数不可得必须 fail-closed，got %+v", e3.tRounds[0])
	}
	stopDist, stopDistOK = 5.0, true // 两门都过
	e4 := seedBase(t, "300001", 1000)
	e4.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	if len(e4.tRounds) != 1 || e4.tRounds[0].Leg != tRLegSell {
		t.Fatalf("两道否决门都过应规则发起反 T（不要求买入信号），got %+v", e4.tRounds)
	}
}

// TestTRoundSignalConsumedOnce 一次性消费：同一 signal_id 不产生第二条买入腿（把次数帽抬到 3 才测得到这一层）。
func TestTRoundSignalConsumedOnce(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	p := tParams()
	p.MaxRounds = 3
	p.NewBuySignal = func(string, time.Time) (string, bool) { return "SIG-ONE", true }
	e.RunIntradayT(tQuote("300001", 9.4, 10), p)
	e.completeFirstRoundsForTest(t) // 把已开的腿做完，腾出"可以开第二条"的位置
	e.RunIntradayT(tQuote("300001", 9.4, 10), p)
	if len(e.tRounds) != 1 {
		t.Fatalf("同一 signal_id 买了两次（第 %d 条）＝一次性消费失效", len(e.tRounds))
	}
}

// TestTRoundFlipsAreNotTheDebounce 翻转不是防抖主力：日内来回穿越产生 3 个新信号，
// max_rounds=1 时只开**一条**腿；抬到 3 才开三条（且每条都对应不同 signal_id）。
func TestTRoundFlipsAreNotTheDebounce(t *testing.T) {
	minuteVNow = 0
	seq := 0
	flipSig := func(string, time.Time) (string, bool) { seq++; return "SIG-FLIP", true } // 同 ID 也算"新信号"
	e := seedBase(t, "300001", 1000)
	p := tParams()
	p.NewBuySignal = flipSig
	for i := 0; i < 3; i++ {
		e.RunIntradayT(tQuote("300001", 9.4, 10), p) // 每次都像"又翻转了一次"
	}
	if got := len(e.tRounds); got != 1 {
		t.Fatalf("次数帽是唯一能硬挡反复开腿的那层，got %d 条", got)
	}
}

// completeFirstRoundsForTest 把当前所有 open 腿按"配对失败收摊"推进到终态（只用于腾出次数位）。
func (e *Engine) completeFirstRoundsForTest(t *testing.T) {
	t.Helper()
	e.mu.Lock()
	for i := range e.tRounds {
		if e.tRounds[i].State == tRStateOpen {
			e.tRounds[i].State = tRStateUnwound
		}
	}
	e.mu.Unlock()
}

// ── 锁 6c/6d：流动性、环境门与总量帽 ─────────────────────────────────────────────────────

// TestTRoundLiquidityPrefilter 流动性前置：成交额/换手/上市天数任一不足 ⇒ 两向都不开；
// 读数不可得（Turnover=0 或 StockMeta 不给 ok）⇒ 按不过处理（fail-closed）。
func TestTRoundLiquidityPrefilter(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*data.StockInfo, *TParams)
	}{
		{"成交额不足", func(q *data.StockInfo, p *TParams) { q.Amount = 1000 * 1e4 }},
		{"换手不足", func(q *data.StockInfo, p *TParams) { q.Turnover = 0.4 }},
		{"换手读数缺失", func(q *data.StockInfo, p *TParams) { q.Turnover = 0 }},
		{"次新股", func(q *data.StockInfo, p *TParams) { p.MinListedDays = 5000 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			minuteVNow = 0
			e := seedBase(t, "300001", 1000)
			q := tQuote("300001", 9.4, 10)
			p := tParams()
			c.mut(q["300001"], &p)
			e.RunIntradayT(q, p)
			if len(e.tRounds) != 0 {
				t.Fatalf("%s：流动性前置必须挡下，got %+v", c.name, e.tRounds[0])
			}
		})
	}
	// ST 股（名称判定）
	e := seedBase(t, "300001", 1000)
	q := tQuote("300001", 9.4, 10)
	q["300001"].Name = "ST 测试"
	e.RunIntradayT(q, tParams())
	if len(e.tRounds) != 0 {
		t.Fatalf("ST 股不得做日内回转，got %+v", e.tRounds[0])
	}
}

// TestTRoundEnvironmentGates MS 0.15 ⇒ 两向全停（六轮 T6-1：极端弱势就是不做 T）；
// MS 0.30 ⇒ 反 T 开、正 T 不开。
func TestTRoundEnvironmentGates(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	p := tParams()
	p.MarketScale = 0.15
	e.RunIntradayT(tQuote("300001", 10.55, 10), p)
	if len(e.tRounds) != 0 {
		t.Fatalf("MS 0.15 < 反 T 门限 0.20 仍开卖腿＝弱市连续减仓通道没关掉，got %+v", e.tRounds[0])
	}
	e2 := seedBase(t, "300001", 1000)
	p2 := tParams()
	p2.MarketScale = 0.30
	e2.RunIntradayT(tQuote("300001", 10.55, 10), p2)
	if len(e2.tRounds) != 1 || e2.tRounds[0].Leg != tRLegSell {
		t.Fatalf("MS 0.30 允许反 T（弱市高抛降风险）但不允许正 T，got %+v", e2.tRounds)
	}
	e3 := seedBase(t, "300001", 1000)
	p3 := tParams()
	p3.MarketScale = 0.5
	p3.IndexChangePct = -3 // 大盘急跌 ≤ −2%
	e3.RunIntradayT(tQuote("300001", 9.4, 10), p3)
	if len(e3.tRounds) != 0 {
		t.Fatalf("大盘急跌仍低吸＝接刀，got %+v", e3.tRounds[0])
	}
}

// TestTRoundSellCapUsesMinDenominator 总量帽分母＝min(总资产, 持仓市值)（六轮 T6-2）：
// 持仓 9 万 / 总资产 10 万 ⇒ 帽应是 9 万×20%＝1.8 万，第二笔卖腿必须被余量挡下。
func TestTRoundSellCapUsesMinDenominator(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 4000) // 4000 股 @10 ⇒ 持仓 4 万 < 总资产 ⇒ 帽应按持仓算（8000 元）
	p := tParams()
	p.MaxRounds = 5
	e.RunIntradayT(tQuote("300001", 10.55, 10), p) // 第一笔高抛被帽压到 ~700 股（8000 元余量）
	if len(e.tRounds) != 1 {
		t.Fatalf("第一笔反 T 应开腿，got %d", len(e.tRounds))
	}
	if v := e.tRounds[0].LegPrice * float64(e.tRounds[0].LegQty); v > 8000*1.35 {
		t.Fatalf("第一笔卖腿市值 %.0f 明显超过按持仓算的帽（若按总资产算会放到 2 万）", v)
	}
	// 帽已被吃满 ⇒ 不再开第二条卖腿
	e.completeFirstRoundsForTest(t)
	e.RunIntradayT(tQuote("300001", 10.55, 10), p)
	if len(e.tRounds) != 1 {
		t.Fatalf("总量帽没起作用（分母若按总资产算会放行），got %d 条", len(e.tRounds))
	}
}

// ── 锁 7/9：时段门与 V 可信度 ────────────────────────────────────────────────────────────

// TestTRoundTimeGates 开盘冷静期内不开、过了 last_open_at 不开、配了非法值由配置层回落（这里是 1400）。
func TestTRoundTimeGates(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	pinTRClockAt(e, 9, 35) // 冷静期 15 分钟内
	e.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e.tRounds) != 0 {
		t.Fatalf("开盘冷静期内 V 与现价几乎重合，不该开腿，got %+v", e.tRounds[0])
	}
	e2 := seedBase(t, "300001", 1000)
	pinTRClockAt(e2, 14, 10) // > last_open_at 14:00
	e2.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e2.tRounds) != 0 {
		t.Fatalf("过了最后开腿时刻仍开新腿＝注定带半态进收盘竞价，got %+v", e2.tRounds[0])
	}
	e3 := seedBase(t, "300001", 1000)
	p := tParams()
	p.LastOpenHM = "1455" // 配置层钳到 1445，14:10 仍应允许开腿
	pinTRClockAt(e3, 14, 10)
	e3.RunIntradayT(tQuote("300001", 9.4, 10), p)
	if len(e3.tRounds) != 1 {
		t.Fatalf("last_open_at 越界应被钳到 1445（14:10 仍可开腿），got %d 条", len(e3.tRounds))
	}
}

// TestTRoundVWAPUntrusted Volume/Amount 缺失 ⇒ 本轮不做 T，且**绝不回落成本口径**。
// 同时测分钟链校验腿：主源与复算差超容差 ⇒ 两个读数至少一个不可信，不动资金；
// 分钟链自己取不到 ⇒ 只降级不拦。
func TestTRoundVWAPUntrusted(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	q := tQuote("300001", 9.4, 10)
	q["300001"].Volume = 0
	e.RunIntradayT(q, tParams())
	if len(e.tRounds) != 0 {
		t.Fatalf("分时均线不可信时回落了成本口径，got %+v", e.tRounds[0])
	}

	minuteVNow = 10.5 // 主源 10 / 分钟链 10.5，偏差 5% > 0.1%
	e2 := seedBase(t, "300001", 1000)
	e2.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e2.tRounds) != 0 {
		t.Fatalf("两个分时均线读数打架还开腿＝用可疑数据做双向操作，got %+v", e2.tRounds[0])
	}
	minuteVNow = 0 // 分钟链不可得：只记降级，不拦
	e3 := seedBase(t, "300001", 1000)
	e3.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e3.tRounds) != 1 {
		t.Fatalf("分钟链缺失不该阻塞日内操作（主源本身就够）")
	}
}

// ── 锁 10/11/12/13：尾盘、锚点与做 T 收益 ────────────────────────────────────────────────

// TestTRoundBuyFirstForcedPairingAtClose 正 T 尾盘**无条件**卖出等量底仓（亏损也卖，减敞口不需许可）。
func TestTRoundBuyFirstForcedPairingAtClose(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e.tRounds) != 1 {
		t.Fatalf("应先开买腿，got %d", len(e.tRounds))
	}
	pinTRClockAt(e, 14, 55)
	e.RunIntradayT(tQuote("300001", 9.3, 10), tParams()) // 比买价还低：亏损也要配平
	r := e.tRounds[0]
	if r.State != tRStateDone || r.PairQty != r.LegQty {
		t.Fatalf("尾盘必须把机动仓卖回原仓，got %+v", r)
	}
	if e.positions["300001"].Qty != 1000 {
		t.Fatalf("配平后持仓应回到 1000，got %d", e.positions["300001"].Qty)
	}
}

// TestTRoundEndOfDayNeedsSignalToBuyBack 反 T 尾盘三态：无信号 ⇒ converted；有信号 ⇒ 买回（done）；
// 亏损超阈值 ⇒ 熔断且转战略减仓（绝不再有"无条件买回"那条旧路径）。
func TestTRoundEndOfDayNeedsSignalToBuyBack(t *testing.T) {
	minuteVNow = 0
	// ① 无信号
	e := seedBase(t, "300001", 1000)
	e.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	pinTRClockAt(e, 14, 55)
	noneSig := tParams()
	noneSig.ActiveBuySignal = func(string, time.Time) (string, bool) { return "", false }
	before := e.positions["300001"].Qty
	e.RunIntradayT(tQuote("300001", 10.6, 10), noneSig)
	if r := e.tRounds[0]; r.State != tRStateConvert || !strings.Contains(r.Reason, "无活跃买入信号") {
		t.Fatalf("尾盘无信号必须不回补并记 converted，got %+v", r)
	}
	if e.positions["300001"].Qty != before {
		t.Fatalf("无回补就不该有成交，持仓 %d→%d", before, e.positions["300001"].Qty)
	}

	// ② 有信号 ⇒ 买回等量
	e2 := seedBase(t, "300001", 1000)
	e2.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	pinTRClockAt(e2, 14, 55)
	e2.RunIntradayT(tQuote("300001", 10.6, 10), tParams())
	if r := e2.tRounds[0]; r.State != tRStateDone {
		t.Fatalf("有活跃买入信号且过闸门应当回补，got %+v", r)
	}
	if e2.positions["300001"].Qty != 1000 {
		t.Fatalf("回补后持仓应回 1000，got %d", e2.positions["300001"].Qty)
	}

	// ③ 亏损超阈值 ⇒ 熔断（即使有信号也不走 T 买回）
	e3 := seedBase(t, "300001", 1000)
	e3.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	pinTRClockAt(e3, 14, 55)
	e3.RunIntradayT(tQuote("300001", 10.95, 10), tParams()) // 比卖价贵 3.8% > 3%
	r3 := e3.tRounds[0]
	if r3.State != tRStateConvert || !strings.Contains(r3.Reason, "熔断") {
		t.Fatalf("亏损超过 loss_breaker 必须封买回通道，got %+v", r3)
	}
}

// TestTRoundStrategicGateBlocksConversion 尾盘回补也必须重走战略闸门：闸门不放行 ⇒ 降级战略减仓，
// 不许"改个状态"就把 T 腿变成绕过风控的加仓。
func TestTRoundStrategicGateBlocksConversion(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	pinTRClockAt(e, 14, 55)
	p := tParams()
	p.StrategicGate = func(string, int) (int, string) { return 0, "目标剩余空间已满" }
	e.RunIntradayT(tQuote("300001", 10.6, 10), p)
	if r := e.tRounds[0]; r.State != tRStateConvert || !strings.Contains(r.Reason, "战略闸门") {
		t.Fatalf("转战略必须重走闸门，got %+v", r)
	}
}

// TestTRoundAnchorInvariants 锚点三条：开腿不改锚点、摊薄成本不影响判线（判线读锚点）、
// 无锚点或来源是 cost_fallback ⇒ 一律不开 T 腿。
func TestTRoundAnchorInvariants(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e.tRounds) != 1 {
		t.Fatal("前置用例失败：应开腿")
	}
	if got := e.positions["300001"].AnchorPrice; got != 10 {
		t.Fatalf("买腿改写了锚点（判线基准被 T 腿拖动），got %v", got)
	}
	e.RunIntradayT(tQuote("300001", 9.8, 10), tParams()) // 配对卖出，成本被摊薄
	cost := e.positions["300001"].CostPrice
	if probe := e.TAnchorBlockReason(e.positions["300001"]); probe != "" {
		t.Fatalf("完整一轮后锚点资格不该丢失：%s", probe)
	}
	if e.positions["300001"].AnchorPrice <= 0 || e.positions["300001"].AnchorPrice == cost && cost != 10 {
		t.Fatalf("锚点应仍是首买价、与摊薄成本分家（cost=%.4f anchor=%.4f）", cost, e.positions["300001"].AnchorPrice)
	}

	// 无锚点
	e2 := seedBase(t, "300001", 1000)
	e2.mu.Lock()
	e2.positions["300001"].AnchorPrice = 0
	e2.positions["300001"].AnchorSource = ""
	e2.mu.Unlock()
	e2.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e2.tRounds) != 0 {
		t.Fatalf("无锚点持仓开了 T 腿＝用摊薄成本冒充风控基准，got %+v", e2.tRounds[0])
	}

	// cost_fallback 档
	e3 := seedBase(t, "300001", 1000)
	e3.mu.Lock()
	e3.positions["300001"].AnchorSource = anchorSourceCostFallback
	e3.mu.Unlock()
	e3.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e3.tRounds) != 0 {
		t.Fatalf("成本回落档不得开 T（可能被历史加仓/T 腿摊薄过），got %+v", e3.tRounds[0])
	}
}

// TestTRoundNetPnlIsIndependentOfCost 做 T 收益（六轮 T6-5）：净额<毛额（费用已扣）、
// 只有 done 轮进聚合、converted 轮不计，且整仓成本不因该字段被改写。
func TestTRoundNetPnlIsIndependentOfCost(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	// 开费用档：夹具默认零费率（其它用例要干净的价差），本条锁恰恰要证明"净额已扣两腿费用"。
	e.mu.Lock()
	e.cfg.CommissionRate, e.cfg.StampTaxRate, e.cfg.MinCommission = 0.00025, 0.0005, 5
	e.mu.Unlock()
	e.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	e.RunIntradayT(tQuote("300001", 9.8, 10), tParams()) // 配对完成
	r := e.tRounds[0]
	if r.State != tRStateDone {
		t.Fatalf("用例前置：应完成一轮，got %+v", r)
	}
	if r.TPnlGross <= 0 || r.TPnlNet <= 0 || !(r.TPnlNet < r.TPnlGross) {
		t.Fatalf("净额应为正且小于毛额（两腿费用已扣），got gross=%.2f net=%.2f", r.TPnlGross, r.TPnlNet)
	}
	net, gross, done, share := e.TRoundNetPnL()
	if done != 1 || net <= 0 || gross <= net || share != 0 {
		t.Fatalf("done 轮才计入聚合：%+v done=%d share=%v", e.tRounds, done, share)
	}
	st := e.TRoundDay("")
	if st.Done != 1 || st.Converted != 0 || st.NetPnl != net {
		t.Fatalf("日汇总与逐轮字段不一致： %+v vs net=%.2f", st, net)
	}
}

// TestTRoundFrozenCashIsReservedForBuyBack 反 T 开腿即冻结回购款，战略可用现金必须扣掉它
// （专款专用；否则"尾盘有信号却买不回"会是现金被花光造成的半态）。
func TestTRoundFrozenCashIsReservedForBuyBack(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.RunIntradayT(tQuote("300001", 10.55, 10), tParams())
	if len(e.tRounds) != 1 {
		t.Fatal("应开反 T 卖腿")
	}
	frozen := e.TFrozenCash()
	if frozen <= 0 {
		t.Fatalf("在途反 T 腿必须冻结回购款，got %v", frozen)
	}
	if want := e.tRounds[0].LegPrice * float64(e.tRounds[0].LegQty); frozen < want {
		t.Fatalf("冻结款 %.2f 低于卖腿市值 %.2f（买回时至少要够这些钱）", frozen, want)
	}
	// 终态一律释放
	pinTRClockAt(e, 14, 55)
	noneSig := tParams()
	noneSig.ActiveBuySignal = func(string, time.Time) (string, bool) { return "", false }
	e.RunIntradayT(tQuote("300001", 10.6, 10), noneSig)
	if got := e.TFrozenCash(); got != 0 {
		t.Fatalf("回转已终态，冻结款必须归零，got %v", got)
	}
}

// TestTRoundLegacyBudgetAndGuardsStillBlock Q5/前置闸保持：预算口径非 dynamic 时整轮禁用；
// 影子期与关键输入缺失时整个 T 关闭（不"按半额度照做"）。
func TestTRoundLegacyBudgetAndGuardsStillBlock(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.mu.Lock()
	e.cfg.BudgetMode = "legacy"
	e.mu.Unlock()
	e.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	if len(e.tRounds) != 0 {
		t.Fatalf("legacy 预算档下做 T 必须整轮禁用，got %+v", e.tRounds[0])
	}
	e2 := seedBase(t, "300001", 1000)
	p := tParams()
	p.MissingKeyInput = true
	e2.RunIntradayT(tQuote("300001", 9.4, 10), p)
	if len(e2.tRounds) != 0 {
		t.Fatalf("关键输入缺失时目标仓位本身不可信，T 必须关闭，got %+v", e2.tRounds[0])
	}
	e3 := seedBase(t, "300001", 1000)
	p3 := tParams()
	p3.Shadow = true
	e3.RunIntradayT(tQuote("300001", 9.4, 10), p3)
	if len(e3.tRounds) != 0 {
		t.Fatalf("影子期不得做日内双向操作，got %+v", e3.tRounds[0])
	}
}

// TestTRoundIdempotencyAndSerializedLegs 两腿串行：同一时刻同码只有一条 open 腿；
// 幂等键 t0: 前缀、单码单日递增。
func TestTRoundIdempotencyAndSerializedLegs(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	p := tParams()
	p.MaxRounds = 3
	for i := 0; i < 4; i++ {
		e.RunIntradayT(tQuote("300001", 9.4, 10), p) // 同一时刻反复驱动
	}
	if n := len(e.tRounds); n != 1 {
		t.Fatalf("叠腿或次数帽失效，got %d 条", n)
	}
	if !strings.HasPrefix(e.tRounds[0].ID, "t0:300001:正T:") {
		t.Fatalf("幂等键形态错，got %q", e.tRounds[0].ID)
	}
}
