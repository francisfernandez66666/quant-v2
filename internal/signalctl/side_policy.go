// side_policy.go — §SIDE-POLICY-20261124 熊市左右侧比例与牛市镜像剧本。
//
// 本文件只放纯判据：输入资金流、斜率、09:30 缺口、13:00 checkpoint 与市场状态，
// 输出两级买入准入、战略目标乘子和左右侧单笔权重。执行器不得另写一份阈值或公式。
package signalctl

import (
	"math"
	"strings"
	"time"

	"quant-trading-v2/internal/config"
)

// SidePolicyMode 市场剧本；legacy 表示总闸关闭，逐字回到旧行为。
type SidePolicyMode string

const (
	SidePolicyLegacy SidePolicyMode = "legacy"
	SidePolicyBear   SidePolicyMode = "bear"
	SidePolicyBull   SidePolicyMode = "bull"
	SidePolicyRange  SidePolicyMode = "range"
)

// SidePolicyLatch 13:00 加急锁；只在同一北京日、until 之前有效。
type SidePolicyLatch struct {
	Kind   string `json:"kind,omitempty"`
	Until  string `json:"until,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// SidePolicyInput 是纯策略层的全部输入；valid 位把“缺数”和“真零”分开。
type SidePolicyInput struct {
	Enabled              bool
	MarketState          string
	MarketDelta          float64
	SlopeDelta           float64
	BucketDeltas         []float64
	GapPct               float64
	IndexChangePct       float64
	PMFlowIntensityDelta float64
	IndexValid           bool
	FlowValid            bool
	PMAnchorValid        bool
	Latch                SidePolicyLatch
	AsOf                 time.Time
	Now                  time.Time
}

// SidePolicyState 是市场级输出；个股级强左侧例外再由 BuyAdmission 判定。
type SidePolicyState struct {
	Enabled              bool            `json:"enabled"`
	Day                  string          `json:"day,omitempty"`
	Mode                 SidePolicyMode  `json:"mode"`
	Level                int             `json:"level"`
	AllowNew             bool            `json:"allow_new"`
	AllowAdd             bool            `json:"allow_add"`
	TargetMultiplier     float64         `json:"target_multiplier"`
	LeftWeight           float64         `json:"left_weight"`
	RightWeight          float64         `json:"right_weight"`
	MarketDelta          float64         `json:"market_delta,omitempty"`
	SlopeDelta           float64         `json:"slope_delta,omitempty"`
	GapPct               float64         `json:"gap_pct,omitempty"`
	PMFlowIntensityDelta float64         `json:"pm_flow_intensity_delta,omitempty"`
	Latch                SidePolicyLatch `json:"latch,omitempty"`
	Reason               string          `json:"reason"`
	AsOf                 time.Time       `json:"asof"`
	AsOfUnix             int64           `json:"asof_ts,omitempty"`
	LatchUntilUnix       int64           `json:"latch_until_ts"`
	FreshnessEpoch       int64           `json:"freshness_epoch,omitempty"`
}

// SideSignal 是准入层需要的最小信号字段；不依赖订单 remark 或账户意图推断。
type SideSignal struct {
	EntryStyle string
	Score      float64
	Threshold  float64
}

// SideObservation 强左侧连续观察的一次记录。
type SideObservation struct {
	At    time.Time
	Score float64
}

func finiteSide(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func clampSide(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func legacySideState() SidePolicyState {
	return SidePolicyState{
		Enabled: false, Mode: SidePolicyLegacy, Level: 0,
		AllowNew: true, AllowAdd: true, TargetMultiplier: 1.0,
		LeftWeight: 1.0, RightWeight: 1.0, Reason: "side_policy_disabled",
	}
}

func hhmmSide(t time.Time) string { return t.Format("15:04") }

func latchActiveSide(l SidePolicyLatch, now time.Time) bool {
	if l.Kind != "no_new" || strings.TrimSpace(l.Until) == "" {
		return false
	}
	until, err := time.Parse("15:04", l.Until)
	if err != nil {
		return false
	}
	cur, err := time.Parse("15:04", hhmmSide(now))
	if err != nil {
		return false
	}
	return cur.Before(until)
}

// DecideSidePolicy 是熊市/牛市剧本的唯一纯函数。缺数不补零，bear 条件压过 bull。
func DecideSidePolicy(in SidePolicyInput) SidePolicyState {
	if !in.Enabled {
		return legacySideState()
	}
	if in.Now.IsZero() {
		in.Now = in.AsOf
	}
	// valid 位与有限值必须同时成立：陈旧指数/资金流即使缓存里还有数，也不能参与对应判据。
	marketOK := in.FlowValid && finiteSide(in.MarketDelta)
	slopeOK := in.FlowValid && finiteSide(in.SlopeDelta)
	gapOK := in.IndexValid && finiteSide(in.GapPct)
	indexOK := in.IndexValid && finiteSide(in.IndexChangePct)
	st := SidePolicyState{
		Enabled: true, Day: in.Now.Format("20060102"), Mode: SidePolicyRange, Level: 0,
		AllowNew: true, AllowAdd: true, TargetMultiplier: 1.0,
		LeftWeight: 1.0, RightWeight: 1.0, Latch: in.Latch,
		AsOf: in.AsOf, Reason: "side_policy_neutral",
	}
	if !st.AsOf.IsZero() {
		st.AsOfUnix = st.AsOf.Unix()
	}
	if st.Latch.Until != "" {
		if until, err := time.Parse("15:04", st.Latch.Until); err == nil {
			st.LatchUntilUnix = time.Date(in.Now.Year(), in.Now.Month(), in.Now.Day(),
				until.Hour(), until.Minute(), 0, 0, in.Now.Location()).Unix()
		}
	}
	if marketOK {
		st.MarketDelta = in.MarketDelta
	}
	if slopeOK {
		st.SlopeDelta = in.SlopeDelta
	}
	if gapOK {
		st.GapPct = in.GapPct
	}
	if in.FlowValid && in.PMAnchorValid && finiteSide(in.PMFlowIntensityDelta) {
		st.PMFlowIntensityDelta = in.PMFlowIntensityDelta
	}
	strongOut := marketOK && in.MarketDelta <= -config.CarrierSideFlowStrongDelta()
	strongIn := marketOK && in.MarketDelta >= config.CarrierSideFlowStrongDelta()
	worsening := slopeOK && in.SlopeDelta <= -config.CarrierSideFlowSlopeStep()
	improving := slopeOK && in.SlopeDelta >= config.CarrierSideFlowSlopeStep()
	highGap := gapOK && in.GapPct >= config.CarrierSideGapHighPct()
	lowGap := gapOK && in.GapPct <= config.CarrierSideGapLowPct()
	indexRise := indexOK && in.IndexChangePct >= config.CarrierSidePMIndexRisePct()

	morningDeteriorating := false
	if marketOK && slopeOK && in.MarketDelta < 0 && in.SlopeDelta < 0 && len(in.BucketDeltas) >= 2 {
		a, b := in.BucketDeltas[len(in.BucketDeltas)-2], in.BucketDeltas[len(in.BucketDeltas)-1]
		morningDeteriorating = finiteSide(a) && finiteSide(b) && a < 0 && b < 0
	}

	bearMode := strings.TrimSpace(in.MarketState) == "bear" || strongOut || worsening
	bullMode := strings.TrimSpace(in.MarketState) == "bull" && strongIn && improving
	// 2026-11-25 owner 修正：开盘"默认熊市"只是数据未齐时的**临时剧本**，不是收紧——
	// 允许 new/add，总动态仓位封在 35%（bear AM ramp 0.35 × TM 1.0）。
	// 缺失的输入一律按弃权（绝不落 range 满仓）；数据齐后再由开盘/反转两条
	// urgent req 链立刻重烘一次。
	if !marketOK || !slopeOK {
		cold := SidePolicyState{
			Enabled: true, Day: st.Day, Mode: SidePolicyBear, Level: 0,
			AllowNew: true, AllowAdd: true, TargetMultiplier: 1.0,
			LeftWeight: 1.0, RightWeight: 1.0, Latch: in.Latch,
			Reason: "side_policy_cold_start_bear", AsOf: in.AsOf,
		}
		if !cold.AsOf.IsZero() {
			cold.AsOfUnix = cold.AsOf.Unix()
		}
		cold.LatchUntilUnix = st.LatchUntilUnix
		return cold
	}
	switch {
	case bearMode:
		st.Mode, st.Reason = SidePolicyBear, "bear_regime"
	case bullMode:
		st.Mode, st.Reason = SidePolicyBull, "bull_mirror"
	default:
		st.Mode, st.Reason = SidePolicyRange, "range_regime"
	}

	// bear 的 L1 只禁新仓；L2 禁普通加仓，并用目标第一/第二档区分收紧深度。
	if st.Mode == SidePolicyBear {
		if strongOut || morningDeteriorating {
			st.Level = 1
		}
		if worsening || (highGap && !strongIn) {
			st.Level = 2
		}
		switch st.Level {
		case 2:
			st.TargetMultiplier = config.CarrierSideBearTargetL1()
			if slopeOK && in.SlopeDelta <= -2*config.CarrierSideFlowSlopeStep() {
				st.TargetMultiplier = config.CarrierSideBearTargetL2()
			}
		case 1:
			st.TargetMultiplier = 1.0
		}
		st.AllowNew = st.Level == 0 && !(strongOut || morningDeteriorating || latchActiveSide(st.Latch, in.Now))
		// L1 只禁新仓；L2 才禁普通加仓，强稳定左侧例外仍由 BuyAdmission 单独裁决。
		st.AllowAdd = st.Level < 2
		if highGap && !strongIn {
			st.RightWeight = config.CarrierSideWeightLo()
		}
		if lowGap && marketOK && in.MarketDelta > 0 {
			st.LeftWeight = config.CarrierSideWeightHi()
		}
		if !st.AllowNew {
			st.Reason = "bear_no_new"
		} else if st.Level > 0 {
			st.Reason = "bear_tighten"
		}
	} else if st.Mode == SidePolicyBull {
		// 资金流与斜率已完成镜像确认；指数大涨只决定放宽档位，不新增 new/add 硬闸。
		// 因而 L1 可达，指数弃权时也不会被缓存值悄悄抬到 L2/L3。
		st.Level = 1
		if indexRise {
			st.Level = 2
		}
		if indexRise && highGap {
			st.Level = 3
		}
		switch st.Level {
		case 3:
			st.TargetMultiplier = config.CarrierSideBullTargetL3()
		case 2:
			st.TargetMultiplier = config.CarrierSideBullTargetL2()
		default:
			st.TargetMultiplier = config.CarrierSideBullTargetL1()
		}
		st.AllowNew = !latchActiveSide(st.Latch, in.Now)
		st.AllowAdd = true
		if highGap && strongIn {
			st.RightWeight = config.CarrierSideWeightHi()
		}
		if lowGap && strongIn {
			st.LeftWeight = config.CarrierSideWeightHi()
		}
		if !st.AllowNew {
			st.Reason = "pm1300_latch"
		}
	}

	if latchActiveSide(st.Latch, in.Now) {
		st.AllowNew = false
		st.Reason = "pm1300_latch"
	}

	if st.TargetMultiplier > 1 || st.LeftWeight > 1 || st.RightWeight > 1 {
		if !in.AsOf.IsZero() {
			st.FreshnessEpoch = in.AsOf.Unix() / int64(config.CarrierSideFreshnessHeartbeatSec())
		}
	}
	return st
}

// FailClosedSidePolicy 在总闸开启但关键输入缺失时返回 bear L1，禁止新仓但保留普通加仓与 T。
func FailClosedSidePolicy(day string, now time.Time, reason string) SidePolicyState {
	st := SidePolicyState{Enabled: true, Day: day, Mode: SidePolicyBear, Level: 1,
		AllowNew: false, AllowAdd: true, TargetMultiplier: 1,
		LeftWeight: 1, RightWeight: 1, Reason: reason, AsOf: now, AsOfUnix: now.Unix()}
	return st
}

// ApplySidePolicyFreshness 对启用态做时间 fail-closed：缺时刻/未来时刻/跨日落 L1；
// 进攻态过期回落 L1，限制态当日保留，避免 TTL 到点反而放松已经收紧的仓位。
func ApplySidePolicyFreshness(st SidePolicyState, now time.Time) SidePolicyState {
	if !st.Enabled {
		return st
	}
	reason := ""
	if st.AsOf.IsZero() {
		reason = "side_policy_asof_missing"
	} else if now.IsZero() || st.AsOf.After(now) {
		reason = "side_policy_asof_invalid"
	} else if st.Day != "" && st.Day != now.Format("20060102") {
		reason = "side_policy_stale_day"
	}
	if reason != "" {
		return failClosedSideState(st, now, reason)
	}
	aggressive := st.Mode == SidePolicyBull || st.TargetMultiplier > 1 || st.LeftWeight > 1 || st.RightWeight > 1
	if aggressive && now.Sub(st.AsOf) > time.Duration(config.CarrierSidePolicyTTLSec()*float64(time.Second)) {
		return failClosedSideState(st, now, "side_policy_stale")
	}
	return st
}

// failClosedSideState 统一回落 bear L1；保留原快照时刻，供读面区分真实缺失与策略收紧。
func failClosedSideState(st SidePolicyState, now time.Time, reason string) SidePolicyState {
	st.Mode, st.Level = SidePolicyBear, 1
	st.AllowNew, st.AllowAdd = false, true
	st.TargetMultiplier, st.LeftWeight, st.RightWeight = 1.0, 1.0, 1.0
	st.Reason = reason
	if now.IsZero() {
		now = time.Now()
	}
	st.AsOf, st.AsOfUnix = now, now.Unix()
	return st
}

// TargetMultiplierForIntent 战略 new/add 读 policy；T 腿永远 TM=1，避免误伤 T 额度。
func TargetMultiplierForIntent(st SidePolicyState, intent string) float64 {
	if intent == "t_leg" || intent == "reduce" {
		return 1.0
	}
	if !st.Enabled {
		return 1.0
	}
	return st.TargetMultiplier
}

// rampTable 分时释放的出厂表（D7 2026-11-25）。
//
// 全部来自 carrier_knobs.go → derive，本文件不写第二个数字。表不合法（缺键/非正/
// 越界/时刻解析失败）⇒ **整表**回落这里，绝不半张表生效（与 §T-TURNOVER-TIER
// 的"整表回落出厂档"同一条纪律）。
type rampTable struct {
	BullAM, BullPM, BearAM, BearPM float64
	PMCheckAt, FullAt              string
}

func rampTableOf() rampTable {
	return rampTable{
		BullAM:    config.CarrierSideRampBullAM(),
		BullPM:    config.CarrierSideRampBullPM(),
		BearAM:    config.CarrierSideRampBearAM(),
		BearPM:    config.CarrierSideRampBearPM(),
		PMCheckAt: config.CarrierSideRampPMCheckAt(),
		FullAt:    config.CarrierSideRampFullAt(),
	}
}

// rampTableValid 一档都不许漏：任何一个值非正或 >1 就整表作废。
func (t rampTable) valid() bool {
	for _, v := range []float64{t.BullAM, t.BullPM, t.BearAM, t.BearPM} {
		if !finiteSide(v) || v <= 0 || v > 1 {
			return false
		}
	}
	for _, s := range []string{t.PMCheckAt, t.FullAt} {
		if _, err := time.Parse("15:04", s); err != nil {
			return false
		}
	}
	return true
}

// SideTimeRamp D7：把"今天允许持有 MS 派生目标的百分之几"算成一个无量纲因子。
//
// 表全部来自 PARAMS（carrier_knobs.go → derive），本函数不写任何数字。
//
//	mode ∉ {bull,bear}（range / legacy / 关闭）⇒ 1.0，即今天的行为；
//	now >= full_at                          ⇒ 1.0（无条件，不查 latch）；
//	now <  pm_check_at                      ⇒ AM 档；
//	pm_check_at <= now < full_at            ⇒ latch 活跃则保持 AM 档，否则 PM 档。
//
// ⚠️ 刻意保留的一个巧合：latch.Until = 14:00 与 full_at = 14:00 是同一个时刻，
// 于是"坏信号"对最终目标的影响只在 13:00–14:00 这一个小时内有效。这是 owner 的
// 原话"14:00 后不管有没有都释放剩余"，不是 bug。
func SideTimeRamp(st SidePolicyState, now time.Time) float64 {
	f, _ := SideTimeRampWithSource(st, now)
	return f
}

// SideTimeRampWithSource 同 SideTimeRamp，另返回一档的来源名（供资金帧 target_ramp_src）。
func SideTimeRampWithSource(st SidePolicyState, now time.Time) (float64, string) {
	if !config.CarrierSideRampEnabled() {
		return 1.0, "ramp_off"
	}
	if !st.Enabled || (st.Mode != SidePolicyBull && st.Mode != SidePolicyBear) {
		return 1.0, "ramp_off"
	}
	t := rampTableOf()
	if !t.valid() {
		// 表不合法 ⇒ 不释放（1.0 = D7 之前的行为）。
		// 注意 Go 侧没有"第二份出厂表"可回落：数值的唯一家就是 carrier_knobs.go，
		// 它本身坏了就没有第三个真源。方向取"不缩"是安全的（不因坏读数砍仓）。
		// 载体侧不同：rules_sizing.SIZING_DEFAULTS 是独立的出厂表，那边做**整表**回落。
		return 1.0, "ramp_off"
	}
	hhmm := hhmmSide(now)
	if hhmm >= t.FullAt {
		return 1.0, "ramp_full"
	}
	bull := st.Mode == SidePolicyBull
	am, pm := t.BearAM, t.BearPM
	if bull {
		am, pm = t.BullAM, t.BullPM
	}
	if hhmm < t.PMCheckAt {
		if bull {
			return am, "ramp_bull_am"
		}
		return am, "ramp_bear_am"
	}
	// 13:00–14:00：latch 活跃 ⇒ 保持上午档（"暂不释放剩余"）。
	if latchActiveSide(st.Latch, now) {
		if bull {
			return am, "ramp_bull_am"
		}
		return am, "ramp_bear_am"
	}
	if bull {
		return pm, "ramp_bull_pm"
	}
	return pm, "ramp_bear_pm"
}

// CombineSideWeight 把现有 flow blend 与风格权重合成后夹在同一个 ±20% 边界内。
func CombineSideWeight(flowBlend, styleWeight float64) float64 {
	if !finiteSide(flowBlend) || flowBlend <= 0 {
		flowBlend = 1.0
	}
	if !finiteSide(styleWeight) || styleWeight <= 0 {
		styleWeight = 1.0
	}
	return clampSide(flowBlend*styleWeight, config.CarrierSideWeightLo(), config.CarrierSideWeightHi())
}

// strongLeftScore 只判信号强度与持仓/止盈边界；连续稳定性由 StrongLeftSignal 单独判。
func strongLeftScore(sig SideSignal, heldQty float64, tpFired bool) bool {
	if sig.EntryStyle != "left" || heldQty <= 0 || tpFired || !finiteSide(sig.Threshold) || sig.Threshold <= 0 {
		return false
	}
	need := max(1, config.CarrierSideLeftScoreFloor())
	if finiteSide(sig.Threshold) {
		need = max(need, sig.Threshold+config.CarrierSideLeftScoreMargin())
	}
	return finiteSide(sig.Score) && sig.Score >= need
}

// StrongLeftSignal 判断分数强度与连续观察稳定性；缺 score/threshold 一律不例外。
func StrongLeftSignal(sig SideSignal, heldQty float64, tpFired bool, obs []SideObservation, now time.Time) bool {
	if !strongLeftScore(sig, heldQty, tpFired) {
		return false
	}
	need := max(1, config.CarrierSideLeftScoreFloor())
	if finiteSide(sig.Threshold) {
		need = max(need, sig.Threshold+config.CarrierSideLeftScoreMargin())
	}
	rounds := config.CarrierSideLeftStableRounds()
	if rounds <= 1 {
		return true
	}
	if len(obs) < rounds {
		return false
	}
	tail := obs[len(obs)-rounds:]
	for i, o := range tail {
		if !finiteSide(o.Score) || o.Score < need {
			return false
		}
		if i > 0 {
			gap := tail[i].At.Sub(tail[i-1].At)
			if gap < time.Duration(config.CarrierSideLeftStableGapSec()*float64(time.Second)) {
				return false
			}
			if tail[i-1].Score-o.Score > config.CarrierSideLeftStableMaxDrop() {
				return false
			}
		}
	}
	return !now.IsZero() && now.Sub(tail[len(tail)-1].At) <= time.Duration(config.CarrierSideFreshnessHeartbeatSec()*float64(time.Second))
}

// strongRightRebuy 极强右侧回买：右侧入场 + 分数比门槛高出
// CarrierSideRightRebuyMargin 个点。不要求"稳定"——稳定性是左侧的概念，
// 右侧买点要的是强度；缺任一枚读数一律不放行。
//
// D6-a 2026-11-25（owner：「极强的一次买入机会出现，可以重新买入」）：它**只**
// 解除 tp_rebuy_block 这一条。same_symbol_day 与 bear L1 的 side_policy_no_new
// 都排在它外面/后面，穿透不出去（§239 ㊳/㊴/㊶ 三枚锁盯着这件事）。
func strongRightRebuy(sig SideSignal) bool {
	if sig.EntryStyle != "right" {
		return false
	}
	if !finiteSide(sig.Score) || !finiteSide(sig.Threshold) || sig.Threshold <= 0 {
		return false
	}
	if !config.CarrierSideRightRebuyEnabled() {
		return false
	}
	return sig.Score >= sig.Threshold+config.CarrierSideRightRebuyMargin()
}

// BuyAdmission 执行两级准入；sameSymbolDay 与 tpFired 是外部硬闸，例外不能穿透。
func BuyAdmission(st SidePolicyState, intent string, heldQty float64, sig SideSignal, tpFired, sameSymbolDay, stable bool) (bool, string) {
	if intent == "t_leg" || intent == "reduce" {
		return true, ""
	}
	if sameSymbolDay {
		return false, "same_symbol_day"
	}
	if tpFired {
		// Owner 2026-11-25（D6-a）：极强右侧买点可以重买当天止盈过的票。
		// 刻意写成"只跳过这一条拒绝"而不是 return true —— L1/L2 与 same_symbol_day
		// 都在外面，穿透不出去（§239 有负锁）。只对 intent == "new" 生效：
		// add 已有左侧例外，再开一条右侧门等于把 L2 改成"任一侧、分数够"。
		if intent != "new" || !strongRightRebuy(sig) {
			return false, "tp_rebuy_block"
		}
	}
	if !st.Enabled {
		return true, ""
	}
	switch intent {
	case "new":
		if st.AllowNew {
			return true, ""
		}
		return false, "side_policy_no_new"
	case "add":
		if st.AllowAdd {
			return true, ""
		}
		if strongLeftScore(sig, heldQty, tpFired) && stable {
			return true, ""
		}
		return false, "side_policy_tighten"
	default:
		return true, ""
	}
}
