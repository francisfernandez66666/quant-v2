// side_policy_flow.go — §SIDE-POLICY-20261124 云端资金流桶、09:30 缺口与 13:00 加急状态。
//
// 本文件把“不同时段净流入计数”收敛为固定 30 分钟桶，并负责把纯策略结果原子写给热更新链。
// 载体不做资金流采样、不算斜率，也不在本机写文件。
package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

type sideFlowSample struct {
	Net    float64
	Amount float64
	Source string
	At     time.Time
}

type sideFlowBucket struct {
	Index   int
	Start   time.Time
	End     time.Time
	Delta   float64
	Partial bool
}

type sideFlowState struct {
	Day             string
	Baseline        *sideFlowSample
	BucketStart     *sideFlowSample
	Last            *sideFlowSample
	CurrentBucket   int
	Buckets         []sideFlowBucket
	MorningAnchor   float64
	MorningAnchorAt time.Time
	GapPct          float64
	GapValid        bool
	GapAt           time.Time
	PMChecked       bool
	UrgentWritten   bool
	// 2026-11-25：开盘默认 bear，牛熊数据齐后若剧本翻成 bull，走 13:00 同一条
	// urgent req 机制立刻烘入（不设 no_new latch）。每个交易日最多一次。
	LastMode        signalctl.SidePolicyMode
	ReversalWritten bool
	Latch           signalctl.SidePolicyLatch
}

func newSideFlowState() sideFlowState {
	return sideFlowState{CurrentBucket: -1, MorningAnchor: math.NaN(), GapPct: math.NaN(),
		LastMode: signalctl.SidePolicyBear}
}

func (s *sideFlowState) reset(day string) {
	*s = newSideFlowState()
	s.Day = day
}

const (
	sideAMOpenMin              = 9*60 + 30
	sideAMCloseMin             = 11*60 + 30
	sidePMOpenMin              = 13 * 60
	sidePMCloseMin             = 15 * 60
	sideBucketToleranceDivisor = 6 // 容差为桶长的 1/6：30min→±5min，改桶长时 tolerance 同步变化。
	sidePMCheckGraceEnd        = "13:05"
)

// sideBucketWindowAt 按配置桶长定位交易时段内的半开区间；午休不占桶号。
func sideBucketWindowAt(at time.Time, minutes int) (idx int, start time.Time, end time.Time, ok bool) {
	if minutes <= 0 {
		return -1, time.Time{}, time.Time{}, false
	}
	at = at.In(cntime.Loc)
	mins := at.Hour()*60 + at.Minute()
	base := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, cntime.Loc)
	idx = 0
	for _, session := range [][2]int{{sideAMOpenMin, sideAMCloseMin}, {sidePMOpenMin, sidePMCloseMin}} {
		for sm := session[0]; sm+minutes <= session[1]; sm += minutes {
			em := sm + minutes
			if mins >= sm && mins < em {
				return idx, base.Add(time.Duration(sm) * time.Minute), base.Add(time.Duration(em) * time.Minute), true
			}
			idx++
		}
	}
	return -1, time.Time{}, time.Time{}, false
}

func sideBucketWindow(at time.Time) (idx int, start time.Time, end time.Time, ok bool) {
	return sideBucketWindowAt(at, config.CarrierSideBucketMinutes())
}

// sideBucketWindowByIndex 用同一时段表反查桶边界，保证前后样本配对不另造一套时段规则。
func sideBucketWindowByIndex(day time.Time, want, minutes int) (start time.Time, end time.Time, ok bool) {
	if minutes <= 0 || want < 0 {
		return time.Time{}, time.Time{}, false
	}
	day = day.In(cntime.Loc)
	base := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, cntime.Loc)
	idx := 0
	for _, session := range [][2]int{{sideAMOpenMin, sideAMCloseMin}, {sidePMOpenMin, sidePMCloseMin}} {
		for sm := session[0]; sm+minutes <= session[1]; sm += minutes {
			if idx == want {
				return base.Add(time.Duration(sm) * time.Minute), base.Add(time.Duration(sm+minutes) * time.Minute), true
			}
			idx++
		}
	}
	return time.Time{}, time.Time{}, false
}

func sideBucketTolerance(minutes int) time.Duration {
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute / sideBucketToleranceDivisor
}

func sideBucketDurationOK(dur time.Duration, minutes int) bool {
	if minutes <= 0 {
		return false
	}
	tol := sideBucketTolerance(minutes)
	return dur >= time.Duration(minutes)*time.Minute-tol && dur <= time.Duration(minutes)*time.Minute+tol
}

func validSideSample(s sideFlowSample) bool {
	return !math.IsNaN(s.Net) && !math.IsInf(s.Net, 0) && s.Amount > 0 && !s.At.IsZero()
}

// closeBucket 用前一桶最后一个有效样本收口；恰好落在边界上的样本也可作为桶末。
// 不拿后一桶首样 fabrication 成前桶终点，因此 09:59/10:01、11:29/13:00 都无需精确边界样本。
func (s *sideFlowState) closeBucket(closeSample, boundarySample sideFlowSample, minutes int) {
	if s.CurrentBucket < 0 || s.BucketStart == nil || !validSideSample(closeSample) {
		return
	}
	start, end, ok := sideBucketWindowByIndex(s.BucketStart.At, s.CurrentBucket, minutes)
	if !ok {
		return
	}
	if validSideSample(boundarySample) && !boundarySample.At.After(end) && boundarySample.At.After(closeSample.At) {
		closeSample = boundarySample
	}
	if closeSample.At.After(end) {
		return
	}
	dur := closeSample.At.Sub(s.BucketStart.At)
	if !sideBucketDurationOK(dur, minutes) || closeSample.Amount <= s.BucketStart.Amount {
		return
	}
	delta := signalctl.FlowPctToDelta(closeSample.Net-s.BucketStart.Net, closeSample.Amount-s.BucketStart.Amount)
	if !math.IsNaN(delta) {
		s.Buckets = append(s.Buckets, sideFlowBucket{
			Index: s.CurrentBucket, Start: start, End: end, Delta: delta,
			Partial: s.CurrentBucket == 0,
		})
	}
}

// observe 只接收 CNY 成交额分母的样本；首桶残桶保留展示但不进斜率，午休断相邻桶。
func (s *sideFlowState) observe(now time.Time, sample sideFlowSample, marketDelta float64) {
	if !validSideSample(sample) || !data.IndexAmountIsCNY(sample.Source) {
		return
	}
	now = now.In(cntime.Loc)
	day := data.TradingDayDate(now)
	if s.Day != day {
		s.reset(day)
	}
	minutes := config.CarrierSideBucketMinutes()
	// 指数源切换后累计净额/成交额属于不同分母，旧桶不能与新样本做差。
	// 清空采样历史并以新源首样本重建基线；上午锚、缺口与 13:00 latch 是独立证据，继续保留。
	if s.Last != nil && s.Last.Source != sample.Source {
		cp := sample
		s.Baseline, s.BucketStart, s.Last = &cp, nil, &cp
		s.CurrentBucket, s.Buckets = -1, nil
		if idx, _, _, ok := sideBucketWindow(now); ok {
			s.CurrentBucket = idx
			cp2 := sample
			s.BucketStart = &cp2
		}
		return
	}
	if s.Baseline == nil {
		cp := sample
		s.Baseline, s.Last = &cp, &cp
		idx, _, _, ok := sideBucketWindow(now)
		if ok {
			s.CurrentBucket = idx
			cp2 := sample
			s.BucketStart = &cp2
		}
		return
	}
	previousLast := s.Last
	idx, _, _, ok := sideBucketWindow(now)
	cp := sample
	s.Last = &cp
	if !ok {
		if previousLast != nil {
			s.closeBucket(*previousLast, sample, minutes)
		}
		s.CurrentBucket = -1
		s.BucketStart = nil
		return
	}
	if s.CurrentBucket != idx {
		if previousLast != nil {
			s.closeBucket(*previousLast, sample, minutes)
		}
		s.CurrentBucket = idx
		cp := sample
		s.BucketStart = &cp
	}
	if now.Hour() == 11 && now.Minute() >= 0 && now.Minute() < 30 && !math.IsNaN(marketDelta) {
		s.MorningAnchor = marketDelta
		s.MorningAnchorAt = now
	}
}

func (s *sideFlowState) slopeDelta() float64 {
	n := len(s.Buckets)
	if n < 2 {
		return math.NaN()
	}
	a, b := s.Buckets[n-2], s.Buckets[n-1]
	if a.Partial || b.Partial || b.Index != a.Index+1 {
		return math.NaN()
	}
	// 相邻桶必须首尾相接；午休跨越会产生明显空隙，不能硬编码某个固定桶号来识别。
	gap := b.Start.Sub(a.End)
	tol := sideBucketTolerance(config.CarrierSideBucketMinutes())
	if gap < -tol || gap > tol {
		return math.NaN()
	}
	return b.Delta - a.Delta
}

func (s *sideFlowState) bucketDeltas() []float64 {
	out := make([]float64, 0, len(s.Buckets))
	for _, b := range s.Buckets {
		if !b.Partial {
			out = append(out, b.Delta)
		}
	}
	return out
}

func (e *Engine) sidePolicyEnabled() bool {
	return e.cfgMgr != nil && e.cfgMgr.Get().Runtime.SidePolicyEnabledOn()
}

// observeSideGapSeries 复用指数量能序列计算 09:30 缺口，避免为缺口再打一次上游。
func (e *Engine) observeSideGapSeries(now time.Time, series []data.IndexAmountDay, today string) {
	now = now.In(cntime.Loc)
	e.sideMu.Lock()
	defer e.sideMu.Unlock()
	if e.sideFlow.Day != data.TradingDayDate(now) {
		e.sideFlow.reset(data.TradingDayDate(now))
	}
	e.sideFlow.GapAt = now
	e.sideFlow.GapValid = false
	if len(series) < 2 {
		return
	}
	prev, cur := series[len(series)-2], series[len(series)-1]
	if cur.TradeDate == today && prev.TradeDate < today && cur.Open > 0 && prev.Close > 0 {
		e.sideFlow.GapPct = (cur.Open/prev.Close - 1) * 100
		e.sideFlow.GapValid = true
	}
}

// sideGapPct 返回当日 09:30 缺口；无有效读数时弃权，不拿零值冒充平开。
func (e *Engine) sideGapPct(now time.Time) (float64, bool) {
	now = now.In(cntime.Loc)
	day := data.TradingDayDate(now)
	e.sideMu.RLock()
	dayMismatch := e.sideFlow.Day != day
	cached, valid, at := e.sideFlow.GapPct, e.sideFlow.GapValid, e.sideFlow.GapAt
	e.sideMu.RUnlock()
	if dayMismatch {
		e.sideMu.Lock()
		if e.sideFlow.Day != day {
			e.sideFlow.reset(day)
		}
		e.sideMu.Unlock()
	}
	if valid || (!at.IsZero() && now.Sub(at) < 30*time.Second) {
		return cached, valid
	}
	gap := math.NaN()
	if e.indexAmountSeriesFn != nil {
		series, _, err := e.indexAmountSeriesFn(2, cntime.DayOf(now))
		if err == nil && len(series) >= 2 {
			prev, cur := series[len(series)-2], series[len(series)-1]
			if cur.TradeDate == cntime.DayOf(now) && prev.TradeDate < cur.TradeDate && cur.Open > 0 && prev.Close > 0 {
				gap = (cur.Open/prev.Close - 1) * 100
			}
		}
	} else if e.marketAPI != nil {
		if v, err := e.marketAPI.IndexOpenGapToday(cntime.DayOf(now)); err == nil {
			gap = v
		}
	}
	e.sideMu.Lock()
	e.sideFlow.GapAt = now
	e.sideFlow.GapPct = gap
	e.sideFlow.GapValid = !math.IsNaN(gap) && !math.IsInf(gap, 0)
	valid = e.sideFlow.GapValid
	e.sideMu.Unlock()
	return gap, valid
}

// SidePolicySnapshot 返回最近一轮侧策略快照；读面不直接读文件，测试也走同一访问器。
func (e *Engine) SidePolicySnapshot() signalctl.SidePolicyState {
	e.sideMu.RLock()
	defer e.sideMu.RUnlock()
	return e.sideState
}

// writeSidePolicyLatest 原子发布侧策略快照；内容未变时不写，进攻态心跳例外由 epoch 体现。
func (e *Engine) writeSidePolicyLatest(st signalctl.SidePolicyState, now time.Time) {
	if e.sideLatestPath == "" {
		return
	}
	body, err := json.Marshal(st)
	if err != nil {
		return
	}
	e.sideMu.Lock()
	defer e.sideMu.Unlock()
	changed := string(body) != e.sideLastBody
	if !changed {
		return
	}
	if err := data.AtomicWrite(e.sideLatestPath, body, 0o644); err != nil {
		fmt.Printf("SIDE_POLICY_LATEST_WRITE_FAIL %v\n", err)
		return
	}
	e.sideLastBody = string(body)
	e.sideLastWrite = now
}

// writeSideUrgentReq 写加急请求。13:00 用 no_new（带 latch）；牛熊反转用
// reversal（不带 latch，只让热更链按最新快照立刻重烘）。同日每类由调用方去重。
func (e *Engine) writeSideUrgentReq(now time.Time, reason, kind, until string) bool {
	if e.sideUrgentReqPath == "" {
		return false
	}
	day := data.TradingDayDate(now.In(cntime.Loc))
	req := map[string]interface{}{
		"request_id": fmt.Sprintf("%s-%s-%s", day, kind, reason),
		"day":        day, "kind": kind, "until": until,
		"reason": reason, "created_at": now.In(cntime.Loc).Format(time.RFC3339),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return false
	}
	if err := data.AtomicWrite(e.sideUrgentReqPath, body, 0o644); err != nil {
		fmt.Printf("SIDE_POLICY_URGENT_WRITE_FAIL %v\n", err)
		return false
	}
	return true
}

// updateSidePolicy 在资金流/指数读数装配后调用；13:00 两路独立就绪，任一命中立即写 req。
func (e *Engine) updateSidePolicy(now time.Time, regime *signalctl.Regime) {
	now = now.In(cntime.Loc)
	day := data.TradingDayDate(now)
	enabled := e.sidePolicyEnabled()
	e.sideMu.Lock()
	if e.sideFlow.Day != day {
		e.sideFlow.reset(day)
	}
	latch := e.sideFlow.Latch
	if latch.Kind == "no_new" && hhmmSideAt(now) >= config.CarrierSidePMBlockUntil() {
		latch = signalctl.SidePolicyLatch{}
		e.sideFlow.Latch = latch
	}
	slope := e.sideFlow.slopeDelta()
	buckets := e.sideFlow.bucketDeltas()
	morningAnchor, morningAt := e.sideFlow.MorningAnchor, e.sideFlow.MorningAnchorAt
	pmChecked, urgentWritten := e.sideFlow.PMChecked, e.sideFlow.UrgentWritten
	e.sideMu.Unlock()

	// 缺口只参与权重/剧本，不单独触发资金安全硬闸。
	gap, gapValid := e.sideGapPct(now)
	idxPct, idxDay, idxStale := e.indexChangePctNow(now)
	indexValid := !math.IsNaN(idxPct) && !math.IsInf(idxPct, 0) && !idxStale && idxDay == day
	marketDelta := math.NaN()
	flowValid := false
	asOf := now
	if regime != nil && !math.IsNaN(regime.FlowMarketDelta) && !math.IsInf(regime.FlowMarketDelta, 0) {
		marketDelta = regime.FlowMarketDelta
		e.sideMu.RLock()
		last := e.sideFlow.Last
		e.sideMu.RUnlock()
		if last != nil {
			asOf = last.At
		}
		flowValid = last != nil && data.IndexAmountIsCNY(last.Source) && now.Sub(last.At) <= 90*time.Second
	}
	if !flowValid {
		marketDelta = math.NaN()
	}
	anchorValid := !math.IsNaN(morningAnchor) && !morningAt.IsZero() &&
		morningAt.Hour() == 11 && morningAt.Minute() < 30 && data.TradingDayDate(morningAt) == day
	pmDelta := math.NaN()
	if flowValid && anchorValid {
		pmDelta = marketDelta - morningAnchor
	}
	st := signalctl.DecideSidePolicy(signalctl.SidePolicyInput{
		Enabled: enabled, MarketState: regimeMarketState(regime), MarketDelta: marketDelta,
		SlopeDelta: slope, BucketDeltas: buckets, GapPct: gap, IndexChangePct: idxPct,
		PMFlowIntensityDelta: pmDelta, IndexValid: indexValid, FlowValid: flowValid,
		PMAnchorValid: anchorValid, Latch: latch, AsOf: asOf, Now: now,
	})
	if enabled && !flowValid && !indexValid && math.IsNaN(slope) && !gapValid {
		st = signalctl.FailClosedSidePolicy(day, now, "side_policy_input_missing")
	}
	if st.TargetMultiplier > 1 || st.LeftWeight > 1 || st.RightWeight > 1 {
		step := int64(config.CarrierSideFreshnessHeartbeatSec())
		epoch := now.Unix() / step
		st.FreshnessEpoch = epoch
		st.AsOf = time.Unix(epoch*step, 0).In(cntime.Loc)
		st.AsOfUnix = st.AsOf.Unix()
	}
	if !enabled {
		// disabled 快照仍带当日身份，读面才能把"明确未启用"与"文件缺失"分开。
		st.Day = day
		st.AsOf = time.Time{}
	}
	st = signalctl.ApplySidePolicyFreshness(st, now)
	if regime != nil {
		regime.SidePolicy = st
	}
	e.sideMu.Lock()
	e.sideState = st
	e.sideMu.Unlock()
	e.writeSidePolicyLatest(st, now)

	// 2026-11-25 owner：开盘默认 bear 只是"数据未齐"的临时剧本。资金流、斜率与指数
	// 都齐了且剧本翻成 bull 时，立刻走 13:00 同一条 urgent req 链重烘下发；
	// 这是**反转**，不粘 no_new latch（req kind=reversal，until 到收盘）。
	e.sideMu.RLock()
	lastMode, reversalWritten := e.sideFlow.LastMode, e.sideFlow.ReversalWritten
	e.sideMu.RUnlock()
	if enabled && flowValid && indexValid && !math.IsNaN(slope) &&
		st.Mode == signalctl.SidePolicyBull && lastMode != signalctl.SidePolicyBull &&
		!reversalWritten {
		if e.writeSideUrgentReq(now, "reversal_bear_to_bull", "reversal", "15:00") {
			e.sideMu.Lock()
			e.sideFlow.ReversalWritten = true
			e.sideMu.Unlock()
		}
	}
	e.sideMu.Lock()
	e.sideFlow.LastMode = st.Mode
	e.sideMu.Unlock()

	// 未启用或当日已处理过 checkpoint 时，不再重复写加急请求。
	if !enabled || pmChecked || urgentWritten {
		return
	}
	hhmm := hhmmSideAt(now)
	if hhmm < config.CarrierSidePMCheckFrom() || hhmm > sidePMCheckGraceEnd {
		return
	}
	indexHit := indexValid && idxPct <= config.CarrierSidePMIndexDropPct()
	flowHit := flowValid && anchorValid && !math.IsNaN(pmDelta) && pmDelta <= -config.CarrierSideFlowPMDelta()
	if !indexHit && !flowHit {
		if indexValid && flowValid && anchorValid {
			e.sideMu.Lock()
			e.sideFlow.PMChecked = true
			e.sideMu.Unlock()
		}
		return
	}
	reason := "pm1300_index_drop"
	if indexHit && flowHit {
		reason = "pm1300_both"
	} else if flowHit {
		reason = "pm1300_flow_drop"
	}
	latch = signalctl.SidePolicyLatch{Kind: "no_new", Until: config.CarrierSidePMBlockUntil(), Reason: reason}
	st.Latch = latch
	if until, err := time.Parse("15:04", latch.Until); err == nil {
		st.LatchUntilUnix = time.Date(now.Year(), now.Month(), now.Day(), until.Hour(), until.Minute(), 0, 0, cntime.Loc).Unix()
	}
	st.AllowNew = false
	st.Reason = "pm1300_latch"
	if regime != nil {
		regime.SidePolicy = st
	}
	e.sideMu.Lock()
	e.sideState = st
	e.sideMu.Unlock()
	e.writeSidePolicyLatest(st, now)
	if !e.writeSideUrgentReq(now, reason, "no_new", config.CarrierSidePMBlockUntil()) {
		return
	}
	e.sideMu.Lock()
	e.sideFlow.Latch = latch
	e.sideFlow.PMChecked = true
	e.sideFlow.UrgentWritten = true
	e.sideMu.Unlock()
}

func regimeMarketState(r *signalctl.Regime) string {
	if r == nil {
		return ""
	}
	return r.MarketState
}

func hhmmSideAt(t time.Time) string { return t.In(cntime.Loc).Format("15:04") }
