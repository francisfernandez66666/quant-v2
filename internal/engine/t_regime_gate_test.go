package engine

import (
	"math"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
)

// blockCountOf 当日某一步的拦截计数（测试只读；生产侧对应 blockStats）。
func (s *tLiveStore) blockCountOf(step string) int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.blockCount[step+"|"]
}

// ── §W-09 行情门断言：形态门（入口）+ 量能门（逐票）─────────────────────────────
//
// 这批用例钉的是**默认生效后的三件事**：
//  1. 牛/熊真的会关（不是只写在文档里）；
//  2. 读数缺失是**弃权**而不是 fail-closed（写反了会让所有账号的 T 立刻全停，
//     而且现象是"T 不做了"这种最难归因的形态）；
//  3. T 的读数**不漏进定权**（漏了就是全账户仓位打七折，且无人察觉）。

// regimeFix 造一轮带行情门参数的 T 轮次。tweak 拿到**夹具时刻**而不是 f 本身
// （f 在自己的初始化表达式里还不存在——这类闭包顺序错会直接编译不过）。
func regimeFix(t *testing.T, hh, mm int, tweak func(*paper.TParams, time.Time)) (*tLiveFix, *tLiveRound) {
	t.Helper()
	f := newTLiveFix(t, hh, mm, nil)
	p := f.params(func(p *paper.TParams) { tweak(p, f.now) })
	return f, f.round(p, f.quotes())
}

// H1 牛/熊关门，盘整放行。
func TestRegimeGateBlocksBullBear(t *testing.T) {
	for _, st := range []string{"bull", "bear"} {
		_, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
			p.MarketState = st
			p.MarketStateAt = now.Add(-1 * time.Minute)
		})
		if why := r.regimeBlock(); why == "" || !strings.Contains(why, "行情门") {
			t.Fatalf("%s 应关闭做 T（canOpen=false），实得原因 %q", st, why)
		}
	}
	// range ⇒ 放行
	_, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
		p.MarketState = "range"
		p.MarketStateAt = now.Add(-1 * time.Minute)
	})
	if why := r.regimeBlock(); why != "" {
		t.Fatalf("range 是放行档，实得 %q", why)
	}
}

// H2 空串 ⇒ 弃权放行（不是拦）。写成 fail-closed 会让默认配置下所有账号的 T 立刻全停。
func TestRegimeGateAbstainsWhenMachineOff(t *testing.T) {
	f, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {})
	if why := r.regimeBlock(); why != "" {
		t.Fatalf("T 行情门关/读数从未产生 ⇒ 应弃权放行，实得 %q", why)
	}
	// 弃权必须**出声**：否则 owner 会以为"牛熊关闭 T"在跑。
	// 判据用 step 计数而不是日志文本——本包的日志输出另有重定向，测试里抓不到。
	if n := f.ledger().blockCountOf("step0"); n <= 0 {
		t.Fatal("弃权必须留痕（step0 计数应 >0），否则 owner 无从判断牛熊关闭到底生效没有")
	}
}

// H3 未知状态字符串 ⇒ fail-closed（那不是"没配"，是"配了但值不认识"）。
func TestRegimeGateUnknownStateFailsClosed(t *testing.T) {
	_, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
		p.MarketState = "sideways"
		p.MarketStateAt = now.Add(-1 * time.Minute)
	})
	why := r.regimeBlock()
	if why == "" || !strings.Contains(why, "未知市场状态") {
		t.Fatalf("未知状态必须按不做 T 处理（fail-closed），实得 %q", why)
	}
}

// H13/H14 新鲜度门：过期弃权；零值时间戳也弃权（不许当成"刚刚更新"）。
func TestRegimeGateStaleAbstains(t *testing.T) {
	f, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
		p.MarketState = "bear"
		p.MarketStateAt = now.Add(-30 * time.Minute)
	})
	if why := r.regimeBlock(); why != "" {
		t.Fatalf("读数过期 30min ⇒ 弃权（不许拿旧结论关掉今天的 T），实得 %q", why)
	}
	if n := f.ledger().blockCountOf("step0"); n <= 0 {
		t.Fatal("过期弃权要留痕（排障时要一眼看出是过期还是真熊）")
	}
}

func TestRegimeGateZeroTimestampAbstains(t *testing.T) {
	_, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) { p.MarketState = "bull" })
	if why := r.regimeBlock(); why != "" {
		t.Fatalf("时间戳为零值（老引擎/没写过）⇒ 弃权，不许当成刚刚更新，实得 %q", why)
	}
}

// H19/H21 票数只出声、不做门：2/6 与 0/6 都**放行**，但都要留痕。
func TestRegimeGateLowVotesStillOpens(t *testing.T) {
	f, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
		p.MarketState = "range"
		p.MarketStateAt = now.Add(-1 * time.Minute)
		p.MarketStateVotes, p.MarketStateVotesTotal = 2, 6
	})
	if why := r.regimeBlock(); why != "" {
		t.Fatalf("票数不足时状态机已降级为 range（放行档）⇒ T 侧不该再拦一次，实得 %q", why)
	}
	if n := f.ledger().blockCountOf("step0"); n <= 0 {
		t.Fatal("低票放行必须留痕（否则复盘分不清真震荡与票太少）")
	}
}

func TestRegimeGateZeroVotesStillOpensButLogs(t *testing.T) {
	f, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
		p.MarketState = "range"
		p.MarketStateAt = now.Add(-1 * time.Minute)
		p.MarketStateVotes, p.MarketStateVotesTotal = 0, 6
	})
	if why := r.regimeBlock(); why != "" {
		t.Fatalf("零票也要放行（只是更不可信），实得 %q", why)
	}
	// 零票单列一条：写成 `votes > 0 && ...` 会把"数据源全挂"静默成"真震荡"。
	if n := f.ledger().blockCountOf("step0"); n <= 0 {
		t.Fatal("零票必须单独出声（比 2/6 更不可信也更安静）")
	}
}

// H4/H5/H6 量能门：放量拦、读数不可得按不过、阈值 0 时整条不生效且不打上游。
func TestVolumeGateBlocksExpanding(t *testing.T) {
	_, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
		p.VolRatioMax = 1.8
		p.VolRatio = func(string) (float64, bool) { return 2.40, true }
	})
	why := r.volumeBlock("600000")
	if why == "" || !strings.Contains(why, "放量") {
		t.Fatalf("折算量比 2.40 ≥ 1.8 ⇒ 应拦，实得 %q", why)
	}
}

func TestVolumeGateFailsClosedOnMissing(t *testing.T) {
	_, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
		p.VolRatioMax = 1.8
		p.VolRatio = func(string) (float64, bool) { return 0, false }
	})
	if why := r.volumeBlock("600000"); !strings.Contains(why, "读数不可得") {
		t.Fatalf("读数不可得 ⇒ 按不过（判据缺了就不能判通过），实得 %q", why)
	}
	// 无源（nil）同样按不过
	_, r2 := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) { p.VolRatioMax = 1.8 })
	if why := r2.volumeBlock("600000"); !strings.Contains(why, "无源") {
		t.Fatalf("未接 VolRatio ⇒ 按不过，实得 %q", why)
	}
}

func TestVolumeGateOffWhenZero(t *testing.T) {
	calls := 0
	_, r := regimeFix(t, 10, 30, func(p *paper.TParams, now time.Time) {
		p.VolRatioMax = 0 // 0 合法＝不设该条
		p.VolRatio = func(string) (float64, bool) { calls++; return 2.40, true }
	})
	if why := r.volumeBlock("600000"); why != "" {
		t.Fatalf("阈值 0 ⇒ 整条不生效，实得 %q", why)
	}
	// 关键：不能"调了但没拦"——那说明门其实还在跑，只是判据写错。
	if calls != 0 {
		t.Fatalf("阈值 0 时不该去取量比（实调 %d 次）⇒ 门没真的关掉", calls)
	}
}

// H16 行情门默认开，且 *bool 的"没配=true / 配了 false=false"真的成立。
func TestTRegimeGateDefaultOn(t *testing.T) {
	var zero config.TConfig
	if !zero.TRegimeGateEnabledOf() {
		t.Fatal("零值配置 ⇒ T 行情门应默认生效（owner 裁决：默认 true）")
	}
	off := false
	c := config.TConfig{RegimeGateEnabled: &off}
	if c.TRegimeGateEnabledOf() {
		t.Fatal("显式配 false ⇒ 必须关得掉（bool 做不到这点，所以类型是 *bool）")
	}
}

// H17 全局 enhance 开关关着、T 门开着 ⇒ tracker 照跑，T 字段有值，定权字段仍空。
// 这条是 §W-09「0）」的行为侧证明：两个开关各管各的，不是"随手把 enhance 打开"。
func TestTRegimeGateIgnoresGlobalEnhanceSwitch(t *testing.T) {
	cm := config.NewManagerWithRules(&config.Rules{})
	e := &Engine{}
	e.SetCfgMgr(cm)
	e.MarketStateObserve(50, 3, 2, 60, 0.01, 0.01)
	e.mu.RLock()
	tState, live := e.lastTMarketState, e.lastMarketState
	e.mu.RUnlock()
	if tState == "" {
		t.Fatal("T 门默认开 ⇒ 即使 enhance.market_state_enabled=false，T 专属读数也必须产生")
	}
	if live != "" {
		t.Fatalf("enhance 开关关着 ⇒ 定权字段必须保持空串（否则全账户仓位会被 state_scales 打折），实得 %q", live)
	}
}

// H20 炸板率 60 秒缓存：默认开启后 riskBreakRate 每 5s 一轮会打上游，必须有 TTL。
// 观测方式：把缓存预置成"30 秒前"，coordinator 留 nil——若判据没命中缓存，就会走到
// `dc == nil` 返回 NaN；连续 5 次都是缓存值即证明没有打上游。过期后必须回到取数分支。
func TestBreakRateCached60s(t *testing.T) {
	now := time.Now()
	e := &Engine{
		riskHithink: true,
		brkRateDay:  data.TradingDayDate(now),
		brkRateAt:   now.Add(-30 * time.Second),
		brkRateVal:  1.23,
	}
	for i := 0; i < 5; i++ {
		if got := e.riskBreakRate(100); got != 1.23 {
			t.Fatalf("60s 内应命中缓存（第 %d 次实得 %v，NaN 说明又去打上游了）", i+1, got)
		}
	}
	e.mu.Lock()
	e.brkRateAt = now.Add(-61 * time.Second)
	e.mu.Unlock()
	if got := e.riskBreakRate(100); !math.IsNaN(got) {
		t.Fatalf("超过 60s 必须重新取数（无 coordinator 时取数失败=NaN），实得 %v", got)
	}
}

// H17/H18 T 读数是自己的字段，定权字段一个都不许被污染。
func TestTRegimeGateDoesNotLeakToSizing(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	f.e.mu.Lock()
	f.e.lastTMarketState = "bear"
	f.e.lastTMarketStateAt = f.now
	f.e.lastMarketState = "" // 定权开关关着
	f.e.mu.Unlock()
	st, at, _, _ := f.e.tRegimeNow()
	if st != "bear" || at.IsZero() {
		t.Fatalf("T 侧应读到自己的读数，实得 %q/%v", st, at)
	}
	// 最重要的一条负锁：定权字段必须仍是空串。
	// 一旦非空，sizing 的 stateFactor 就会乘 state_scales ⇒ 全账户仓位变七折且无人察觉。
	// （不直接调 assembleRegime：那需要完整的定权上下文，本夹具没有；这里锁的是"两字段分家"
	//  这个不变量本身，定权链的端到端比对由 H18 的专项用例在完整装配下做。）
	f.e.mu.RLock()
	live := f.e.lastMarketState
	f.e.mu.RUnlock()
	if live != "" {
		t.Fatalf("T 的行情读数漏进定权字段了（lastMarketState=%q）⇒ 全账户仓位会被 state_scales 打折", live)
	}
}
