// sizing_evo_track.go — §SIZING-EVO 引擎侧影子落盘接线（R1）。
//
// 职责（§2.7.3/§2.7.4）：
//  1. **记录**：每轮 live/paper 通道的定权结果落一条结构化快照（Regime+Signal 画像+
//     Scale+Reasons），按 (交易日, 通道, 配置戳, 代码) 去重——同一天同一只票只留首次
//     定权，不因 5s 轮询重复膨胀；
//  2. **灰度照算**：状态库里有灰度候选时，用候选配置对同一信号再算一次 scale，落
//     shadow 通道（只留痕不下发）——L2 灰度的观测数据来源；
//  3. **结算**：快照满 ForwardDays(5) 个交易日后，用库内日K（后复权序列，比值口径
//     不受复权因子影响）回填前向收益。缺K/停牌 → 保留未结算重试，超次数丢弃（缺数
//     不当 0，绝不伪造收益）。
//
// 门控：evo 未启用（SizingConfig.EvoOn()=false）或影子库目录为空（纯内存模式）→
// 整个文件零操作。所有写路径 best-effort：影子库是旁路观测面，失败绝不阻断下单链路。
//
// English: engine-side wiring for the sizing_evo shadow store — per-round snapshot
// recording (deduped), grayscale shadow recomputation, and forward-return settlement.
package engine

import (
	"sync"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/sizing_evo"
)

const (
	// evoStateTTL 生命周期状态文件读取缓存（晋升/降级落库后最多 30s 内被引擎看到）。
	evoStateTTL = 30 * time.Second
	// evoSettleEvery 结算扫描节流（库内日K一天只变一次，60s 足够）。
	evoSettleEvery = 60 * time.Second
	// evoMaxSettleTries 单条待结算快照的重试上限（超过即丢弃，防无界堆积）。
	evoMaxSettleTries = 20
	// evoPendingCap 待结算队列上限（超出不再收新，防长跑无界）。
	evoPendingCap = 4000
	// evoSettleBars 结算取K根数（5 日 horizon + 余量）。
	evoSettleBars = 40
)

// evoPendingSettle 一条待结算快照（按 (day, code) 归并——Settle 是日+代码粒度）。
// English: one pending settlement (grouped by day+code, matching the store's Settle).
type evoPendingSettle struct {
	day   string
	code  string
	tries int
	at    time.Time
}

// sizingEvoTracker 影子落盘器（每引擎一份；线程安全）。
// English: the per-engine shadow-store recorder (thread-safe).
type sizingEvoTracker struct {
	mu         sync.Mutex
	store      *sizing_evo.SnapshotStore
	state      *sizing_evo.EvoStateStore
	bars       func(code string, count int) ([]data.KLine, bool)
	seen       map[string]bool // 当日已落盘键（day|channel|cfgver|code）
	seenDay    string
	pending    map[string]*evoPendingSettle // key=day|code
	stateAt    time.Time
	stateSnap  *sizing_evo.EvoState
	ps         *sizing_evo.ParamSpace
	lastSettle time.Time
}

// evoTrackerRef 惰性建落盘器（影子库目录为空 → 返回 nil，调用方零操作）。
// English: lazily creates the recorder; a nil store dir disables it entirely.
func (e *Engine) evoTrackerRef() *sizingEvoTracker {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.evoTrack != nil {
		return e.evoTrack
	}
	dir := e.evoStoreDir
	if dir == "" {
		return nil
	}
	e.evoTrack = &sizingEvoTracker{
		store:   sizing_evo.NewSnapshotStore(dir),
		state:   sizing_evo.NewEvoStateStore(dir),
		bars:    NewDayBarsLookup(e.d1Store).Lookup,
		seen:    make(map[string]bool),
		pending: make(map[string]*evoPendingSettle),
		ps:      sizing_evo.DefaultParamSpace(),
	}
	return e.evoTrack
}

// evoStateOf 读生命周期状态（TTL 缓存；读失败按空状态——宁可少观测不误晋升）。
// English: reads the lifecycle state with a TTL cache (failures read as empty).
func (e *Engine) evoStateOf(tr *sizingEvoTracker, now time.Time) *sizing_evo.EvoState {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.stateSnap != nil && now.Sub(tr.stateAt) < evoStateTTL {
		return tr.stateSnap
	}
	st, err := tr.state.Load()
	if err != nil {
		st = &sizing_evo.EvoState{}
	}
	tr.stateSnap, tr.stateAt = st, now
	return st
}

// evoSignalProfile 从信号提取 ComputeSizing 所需的画像字段（显式落盘）。
// English: extracts the signal profile fields needed to recompute a scale.
func evoSignalProfile(s combat_agent.Signal) sizing_evo.SignalProfile {
	return sizing_evo.SignalProfile{
		Confidence: s.Confidence, Price: s.Price, ATR: s.ATR,
		QualityWeight: s.QualityWeight, D1Blocked: s.D1Blocked,
		FlowRatio5D: s.FlowRatio5D, FlowRatioToday: s.FlowRatioToday,
	}
}

// evoRecordDecisions 记录一轮定权结果（live/paper 通道）并为灰度候选照算 shadow 快照。
//
// 只记 Verdict=Pass 且 BuyScale>0 的裁定——定权没生效（关闭/影子/硬拦）时没有可学习
// 的 scale，落 0 会让进化器学不存在的数据（弃权纪律）。
// English: records one round's sized buys and recomputes shadow scales for grayscale
// candidates; only passed decisions with a positive scale are recorded.
func (e *Engine) evoRecordDecisions(pol signalctl.Policy, decisions []signalctl.Decision, sigs []combat_agent.Signal, channel, account string, now time.Time) {
	cfg := pol.Regime.SizeCfg
	if !cfg.EvoOn() {
		return
	}
	tr := e.evoTrackerRef()
	if tr == nil || len(decisions) == 0 {
		return
	}
	day := data.TradingDayDate(now)
	st := e.evoStateOf(tr, now)
	// 生效配置戳：有晋升候选用它（含账号），否则 base。
	cfgVer := sizing_evo.CfgVerOf(account, "base")
	if st.Applied != nil {
		cfgVer = sizing_evo.CfgVerOf(account, st.Applied.CandID)
	}

	var batch []sizing_evo.SizingSnapshot
	tr.mu.Lock()
	if tr.seenDay != day {
		tr.seen, tr.seenDay = make(map[string]bool), day
	}
	for i, d := range decisions {
		if d.Verdict != signalctl.VerdictPass || d.BuyScale <= 0 {
			continue
		}
		if i >= len(sigs) {
			break
		}
		sig := sigs[i]
		code := pureTsCode(sig.Code)
		key := day + "|" + channel + "|" + cfgVer + "|" + code
		if tr.seen[key] {
			continue
		}
		tr.seen[key] = true
		batch = append(batch, sizing_evo.SizingSnapshot{
			Day: day, At: now, Channel: channel, Account: account, Code: code,
			Sig: evoSignalProfile(sig), Regime: pol.Regime, CfgVer: cfgVer,
			Scale: d.BuyScale, Reasons: d.SizingReasons,
		})
		if _, ok := tr.pending[day+"|"+code]; !ok && len(tr.pending) < evoPendingCap {
			tr.pending[day+"|"+code] = &evoPendingSettle{day: day, code: code, at: now}
		}
		// —— 灰度照算：候选配置下重算 scale，落 shadow 通道（只留痕不下发） ——
		for _, g := range st.Grayscale {
			candCfg := sizing_evo.Candidate{ID: g.CandID, Params: g.Params}.Apply(cfg, tr.ps)
			reg := pol.Regime
			reg.SizeCfg = candCfg
			res := signalctl.ComputeSizing(reg, sig)
			if res.Downgrade || res.Scale <= 0 {
				continue // 候选配置下被硬拦 = 这笔交易不存在，不落 shadow
			}
			skey := day + "|shadow|" + g.CandID + "|" + code
			if tr.seen[skey] {
				continue
			}
			tr.seen[skey] = true
			batch = append(batch, sizing_evo.SizingSnapshot{
				Day: day, At: now, Channel: "shadow", Account: account, Code: code,
				Sig: evoSignalProfile(sig), Regime: reg, CfgVer: g.CandID,
				Scale: res.Scale, Reasons: res.Reasons,
			})
		}
	}
	tr.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	if err := tr.store.Append(day, batch...); err != nil {
		opslog.DayOnce("evo-append-error", func() {
			opslog.Logf("sizing_evo", "影子库追加失败（不阻断交易）：%v", err)
		})
	}
	e.evoSettlePass(tr, now)
}

// evoSettlePass 结算到期的待结算快照（节流；缺K重试，超次数丢弃）。
//
// 前向收益口径：库内后复权日K的 close→close 比值（第 idx 根=信号日/其后最近交易日，
// 第 idx+5 根=5 个交易日后）。后复权因子在比值中约掉，不需要额外归一。
// English: settles due snapshots from the local hfq daily bars (close-to-close ratio over
// the 5-day horizon; the adjustment factor cancels in the ratio).
func (e *Engine) evoSettlePass(tr *sizingEvoTracker, now time.Time) {
	tr.mu.Lock()
	if now.Sub(tr.lastSettle) < evoSettleEvery {
		tr.mu.Unlock()
		return
	}
	tr.lastSettle = now
	pend := make([]*evoPendingSettle, 0, len(tr.pending))
	for _, p := range tr.pending {
		pend = append(pend, p)
	}
	tr.mu.Unlock()

	settled := 0
	for _, p := range pend {
		bars, ok := tr.bars(p.code, evoSettleBars)
		if !ok || len(bars) == 0 {
			e.evoPendingRetry(tr, p)
			continue
		}
		idx := -1
		for i, b := range bars {
			if b.Date.Format("20060102") >= p.day {
				idx = i
				break
			}
		}
		if idx < 0 || idx+sizing_evo.ForwardDays >= len(bars) {
			e.evoPendingRetry(tr, p) // 目标日K还没同步出来：下一轮再试
			continue
		}
		entry, exit := bars[idx].Close, bars[idx+sizing_evo.ForwardDays].Close
		if entry <= 0 || exit <= 0 {
			e.evoPendingRetry(tr, p)
			continue
		}
		ret := (exit/entry - 1) * 100
		if n, err := tr.store.Settle(p.day, p.code, ret, now); err != nil {
			opslog.DayOnce("evo-settle-error", func() {
				opslog.Logf("sizing_evo", "前向收益结算失败（保留待结算）：%v", err)
			})
			e.evoPendingRetry(tr, p)
			continue
		} else if n > 0 {
			settled++
		}
		tr.mu.Lock()
		delete(tr.pending, p.day+"|"+p.code)
		tr.mu.Unlock()
	}
	if settled > 0 {
		opslog.Logf("sizing_evo", "前向收益结算 %d 条（horizon=%d 交易日）", settled, sizing_evo.ForwardDays)
	}
}

// evoPendingRetry 重试计数 +1，超上限丢弃（防无界堆积）。
// English: bumps the retry counter and drops the entry past the cap.
func (e *Engine) evoPendingRetry(tr *sizingEvoTracker, p *evoPendingSettle) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	p.tries++
	if p.tries >= evoMaxSettleTries {
		delete(tr.pending, p.day+"|"+p.code)
	}
}
