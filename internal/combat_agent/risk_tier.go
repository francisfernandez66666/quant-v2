// risk_tier.go — §MARKET_RISK_GATE P3 风险档合成器（把 E1 单一"交割日门控"泛化为分层市场风险档）。
// 三源合一：市场情绪相位（P1 含广度纠偏）+ 市场状态机 bull/range/bear（P2）+ 宏观事件日历（含影响期），
// 合成 Red（系统性风险日，做多全面收紧）/ Yellow（警惕日，买入门槛上浮）/ 空（正常，行为不变）。
// 任一维度取数缺失=该维度弃权（绝不把失败当触发或当中性）；总开关关闭时整体退回旧 E1 纯宏观门控。
//
// English: risk_tier.go — the P3 risk-tier synthesizer (generalizing E1's single "delivery-day gate" into
// a tiered market-risk regime). It fuses three inputs — the emotion phase (P1, breadth-corrected), the
// market state machine bull/range/bear (P2), and the macro-event calendar (with impact windows) — into
// Red (systemic-risk day: long buys tightly restricted) / Yellow (caution day: buy bar raised) / empty
// (normal: behavior unchanged). Any input missing → that dimension abstains (a failure is never treated
// as a trigger nor as neutral). When the master switch is off it falls back to the old E1 pure-macro gate.
package combat_agent

import (
	"fmt"
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/strategy"
)

// 风险档常量（空串=无风险档）。English: risk-tier constants (empty = no tier).
const (
	RiskTierNone   = ""
	RiskTierYellow = "Yellow"
	RiskTierRed    = "Red"
)

// ComputeAndSetRiskTier 由引擎每轮调用：用当前情绪相位/市场状态/上涨占比 + 宏观事件合成风险档，
// 缓存到 Agent（供信号路径 applyRiskTier、做空增强、自动买谨慎层共用同一档位），并返回给引擎做
// F2 徽标推送与持仓预警。合成在总开关关闭时返回空档（信号侧回退旧 E1）。
// English: called by the Engine each cycle — synthesizes the tier from the current emotion/state/upRatio
// plus macro events, caches it on the Agent (so the signal path, short boost, and auto-buy caution share one
// source of truth), and returns it for the F2 badge and held-position alerts. Returns an empty tier when the
// master switch is off (the signal side then falls back to legacy E1).
func (a *Agent) ComputeAndSetRiskTier(emotionPhase, marketState string, upRatio float64) (string, []string) {
	cfg := a.macroGateConfig()
	tier, reasons := SynthesizeRiskTier(emotionPhase, marketState, upRatio, macroEventsNow(), time.Now(), cfg)
	a.mu.Lock()
	a.riskTier, a.riskTierReasons = tier, reasons
	a.mu.Unlock()
	return tier, reasons
}

// RiskTier 返回引擎最近下发的风险档 + 触发原因（供做空增强/自动买/持仓预警/F2 读取）。
// English: returns the latest tier + reasons pushed by the Engine (for short boost / auto-buy / alerts / F2).
func (a *Agent) RiskTier() (string, []string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.riskTier, a.riskTierReasons
}

// SynthesizeRiskTier 由情绪相位 + 市场状态 + 真实上涨占比 + 已按影响期筛选的宏观事件合成风险档。
// 入参：emotionPhase（P1 六阶段，空=未算/弃权）、marketState（bull/range/bear，空=状态机关闭/弃权）、
// upRatio（上涨占比 0~1，NaN=家数缺失弃权）、events（macroEventsNow 结果，已在影响期内）、now、cfg。
// 返回 (tier, reasons)——reasons 为可读触发原因列表，供信号理由与 F2 徽标展示。
// 规则矩阵见 docs/MARKET_RISK_GATE_PLAN_20260912.md §三（全部走 cfg 默认，可配）。
//
// English: SynthesizeRiskTier fuses the emotion phase + market state + real up-ratio + windowed macro
// events into a tier. emotionPhase empty / marketState empty / upRatio NaN mean that dimension abstains.
// Returns (tier, human-readable reasons) per the §三 matrix (all thresholds from cfg with defaults).
func SynthesizeRiskTier(emotionPhase, marketState string, upRatio float64, events []data.MacroEvent, now time.Time, cfg config.MacroGateConfig) (string, []string) {
	// 总开关关闭：不产出分层风险档（信号侧仍由旧 applyMacroGate 兜底交割日语义）。
	if !cfg.RiskGateOn() {
		return RiskTierNone, nil
	}

	emOn := cfg.EmotionOn() && emotionPhase != ""
	emotionSet := cfg.EmotionLevelSet()
	highSet := cfg.HighImpactSet()
	contractLvl := cfg.ContractLevelName()

	var red, yellow []string

	// ── 情绪维度（仅 emOn 时参与）──
	emotionCold := false // 退潮/背离（冰点直接记入 Red，不属 cold 侧的 Yellow）
	if emOn && emotionSet[emotionPhase] {
		switch emotionPhase {
		case "冰点":
			red = append(red, "情绪冰点")
		case "退潮":
			emotionCold = true
			yellow = append(yellow, "情绪退潮")
		case "背离":
			emotionCold = true
			yellow = append(yellow, "指数情绪背离")
		default:
			yellow = append(yellow, "情绪"+emotionPhase)
			emotionCold = true
		}
	}

	// ── 市场状态维度（空=弃权）──
	switch marketState {
	case "bear":
		red = append(red, "市场状态熊市")
	case "range":
		// range 且上涨占比偏弱 → Yellow；upRatio NaN（家数缺失）时该子条件弃权。
		if upRatio == upRatio && upRatio < cfg.WeakBreadthUpRatio() {
			yellow = append(yellow, fmt.Sprintf("震荡+上涨占比%.0f%%偏弱", upRatio*100))
		}
	}

	// ── 宏观事件维度（events 已在影响期内）──
	contractToday := false
	contractWindow := false
	highImpactWindow := ""
	todayY, todayM, todayD := now.Date()
	for i := range events {
		ev := &events[i]
		switch ev.Level {
		case contractLvl:
			ey, em, ed := ev.Date.Date()
			if ey == todayY && em == todayM && ed == todayD {
				contractToday = true
			} else {
				contractWindow = true
			}
		}
		if highSet[ev.Level] || ev.Impact == "high" {
			if highImpactWindow == "" {
				highImpactWindow = ev.Title
				if highImpactWindow == "" {
					highImpactWindow = ev.Level
				}
			}
		}
	}
	if contractToday {
		red = append(red, "股指期货交割日(当日)")
	}
	if contractWindow {
		yellow = append(yellow, "股指期货交割影响期")
	}
	if highImpactWindow != "" {
		yellow = append(yellow, highImpactWindow+"影响期")
	}
	// 强条件：高影响事件在窗内 且 情绪已转弱（退潮/背离）→ 升 Red。
	if highImpactWindow != "" && emotionCold {
		red = append(red, highImpactWindow+"×情绪转弱")
	}

	if len(red) > 0 {
		return RiskTierRed, dedupReasons(red)
	}
	if len(yellow) > 0 {
		return RiskTierYellow, dedupReasons(yellow)
	}
	return RiskTierNone, nil
}

// dedupReasons 去重触发原因（同一天多事件可能产生相同文字）。
// English: de-duplicates reason strings.
func dedupReasons(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// sectorEscalated 报告某信号是否命中"板块级映射"（命中则该信号档位再上浮一档：Yellow→按 Red 对待）。
// map 为空=板块层不生效（默认）。key 为宏观事件级别/情绪档名（如 "cpi"/"fomc"/"冰点"），value 为板块名列表，
// 与 Signal.Sector 做包含式匹配（板块名互为子串即命中，兼容"科技"⊂"半导体科技"）。
// English: reports whether a signal's sector hits the sector map (hit → escalate one tier, Yellow→Red).
// Empty map disables the layer. Keys are macro levels / emotion tags; sector names match by substring.
func sectorEscalated(sector string, cfg config.MacroGateConfig) bool {
	if sector == "" || len(cfg.MacroSectorMap) == 0 {
		return false
	}
	for _, sectors := range cfg.MacroSectorMap {
		for _, s := range sectors {
			if s != "" && (containsCI(sector, s) || containsCI(s, sector)) {
				return true
			}
		}
	}
	return false
}

// containsCI 判断 sub 是否为 s 的子串（板块名匹配用，中文无需大小写折叠但保留兼容）。
func containsCI(s, sub string) bool {
	if len(sub) == 0 {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// applyRiskTier 对已生成信号应用分层风险档（§三 作用层1，applyMacroGate 的泛化）：
//   - Yellow：非 N 形买入信号置信度 < YellowMinConf(默认0.90) → 降级 watch；N 形/动量不额外拦截（仅门槛上浮）。
//   - Red：非 N 形买入信号置信度 < RedMinConf(默认0.92) → 降级 watch；并按 BlockNShape/BlockMomentum
//     一律拦截 N 形超短（降级 watch）与动量 watch（剔除）——沿用旧 E1 交割日硬闸语义。
//   - 板块级映射命中（§三 作用层2）：命中 MacroSectorMap 的信号档位再上浮一档（Yellow→按 Red 对待）。
//
// 原地改 reason/action（Red 拦截动量 watch 时从输出剔除，保持其余顺序）。理由前缀标注触发源，
// 如"风险档Red[情绪冰点+交割日当日]降级(置信度不足): "。
// English: applies the tiered risk regime to generated signals (generalized applyMacroGate) — Yellow raises
// the buy-confidence bar (default 0.90) without extra N/momentum blocks; Red raises it further (0.92) and
// honors the BlockNShape/BlockMomentum hard blocks from E1. Signals whose sector hits MacroSectorMap
// escalate one tier (Yellow treated as Red). Mutates reason/action in place, filters blocked momentum-watch
// on Red while preserving order.
func applyRiskTier(sigs []Signal, tier string, cfg config.MacroGateConfig, reasons []string) []Signal {
	if tier == RiskTierNone || len(sigs) == 0 {
		return sigs
	}
	prefix := "风险档" + tier
	if len(reasons) > 0 {
		prefix += "[" + joinReasons(reasons) + "]"
	}
	redBlockN := macroGateBlockNShape(cfg)
	redBlockMomentum := macroGateBlockMomentum(cfg)
	out := make([]Signal, 0, len(sigs))
	for i := range sigs {
		s := &sigs[i]
		// 该信号的有效档位（板块映射命中则 Yellow 上浮为 Red 对待）。
		eff := tier
		if tier == RiskTierYellow && sectorEscalated(s.Sector, cfg) {
			eff = RiskTierRed
		}
		minConf := cfg.YellowMinConf()
		if eff == RiskTierRed {
			minConf = cfg.RedMinConf()
		}
		isBuy := s.Action == "buy" || s.Action == "买入"
		// 动量 watch：仅 Red 且开启拦截时剔除（整体利空下不新增观察）；Yellow 保留。
		if s.Strategy == "动量" && s.Action == "watch" {
			if eff == RiskTierRed && redBlockMomentum {
				continue
			}
			out = append(out, *s)
			continue
		}
		// N 形超短：仅 Red 且开启拦截时一律降级 watch（超短对系统性风险最敏感）；Yellow 只走门槛。
		if isBuy && eff == RiskTierRed && redBlockN &&
			NormalizeStrategyName(s.Strategy) == NormalizeStrategyName(string(strategy.SignalNShape)) {
			s.Action = "watch"
			s.Reason = prefix + "拦截N形超短: " + s.Reason
			out = append(out, *s)
			continue
		}
		// 非 N 形买入：置信度低于门槛 → 降级 watch。
		if isBuy && s.Confidence < minConf {
			s.Action = "watch"
			s.Reason = prefix + "降级(置信度<" + fmtRate(minConf) + "): " + s.Reason
		}
		out = append(out, *s)
	}
	return out
}

// joinReasons 用 "+" 连接触发原因列表。
func joinReasons(rs []string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += "+"
		}
		out += r
	}
	return out
}

// shortRiskBoost 风险档对做空信号的置信度加成（§三 作用层4，差距6）：恐慌日对做空是顺势，
// Yellow/Red 时给做空 sell/watch 信号 confidence +0.05（上限 1.0），reason 前缀注明"风险档加成"。
// 仅在做空门已开、ScanShort 产出信号后调用（做空两层门由调用方把守，本函数只负责加成）。
// tier 为空则原样返回。原地修改 confidence/reason 并返回同一切片。
// English: the P7 short boost (action layer 4 / gap 6) — panic days are tailwind for shorts, so on Yellow/Red
// each short sell/watch gets confidence +0.05 (capped at 1.0) with a reason prefix. Called only after
// ScanShort (the two short gates are the caller's concern). Returns the slice unchanged when tier is empty.
func applyRiskTierShortBoost(sigs []Signal, tier string) []Signal {
	if tier == RiskTierNone || len(sigs) == 0 {
		return sigs
	}
	note := "风险档" + tier + "做空加成: "
	for i := range sigs {
		s := &sigs[i]
		if s.Action != "sell" && s.Action != "卖出" && s.Action != "watch" {
			continue
		}
		if s.Confidence > 0 {
			s.Confidence += 0.05
			if s.Confidence > 1.0 {
				s.Confidence = 1.0
			}
			if !strings.HasPrefix(s.Reason, note) {
				s.Reason = note + s.Reason
			}
		}
	}
	return sigs
}

// fmtRate 把 0~1 的门槛格式化为两位小数百分比文字（0.90→"90%"）。
func fmtRate(x float64) string {
	return fmt.Sprintf("%.0f%%", x*100)
}

// MarketRiskAlerts §三 作用层5（差距3）：系统性风险日对做多持仓产出「建议减仓」提醒。
// 与 EmotionRetreatAlerts（只看情绪相位）互补——本函数由**合成后的市场风险档**（情绪+市场状态+宏观三源）
// 驱动，故交割日/CPI/熊市等情绪尚未转弱但系统性风险已抬头的日子也会提醒避险。
// 关键约束：**只提醒、绝不自动卖出**——故 AlertType 用"系统性风险"（不在 SellAction 的 清仓/利空抛售/减仓
// 命中集，Action="减仓" 也不在 止盈/止损/卖出 集），SellAction 返回 "" → 不会被 13e 自动减仓通道执行。
// Red=强提醒（全部做多持仓），Yellow=同型提醒级别更低；日级由消息中心按 code@level 稳定键去重。
// 做空持仓忽略（风险对做空是顺向，见 P7 加成）。
// English: the P5 held-position risk reminder, driven by the COMPOSITE tier (emotion + state + macro) so
// delivery-day/CPI/bear-market days warn even before the emotion phase turns cold. Hard constraint:
// REMINDER ONLY, never auto-sell — the AlertType "系统性风险" is outside SellAction's 清仓/利空抛售/减仓
// hit set and Action "减仓" is outside 止盈/止损/卖出, so SellAction returns "" and the 13e auto-trim path
// skips it. Red = strong reminder over all long holdings, Yellow = same shape lower level; daily dedup by
// code@level in the message center. Short holdings are ignored (risk is their tailwind — see P7).
func (a *Agent) MarketRiskAlerts(rpt *report.Report, quotes map[string]*data.StockInfo, tier string, reasons []string, now time.Time) []Signal {
	if tier == RiskTierNone || rpt == nil {
		return nil
	}
	positions := rpt.HeldPositions()
	if len(positions) == 0 {
		return nil
	}
	reasonText := joinReasons(reasons)
	if reasonText == "" {
		reasonText = tier
	}
	level := "提示"
	conf := 0.8
	if tier == RiskTierRed {
		level = "警示"
		conf = 1.0
	}
	_ = level
	var alerts []Signal
	for _, pos := range positions {
		if pos.Direction == "做空" {
			continue // 系统性风险对做空是顺向，不提醒减仓
		}
		price := pos.EntryPrice
		if q := quotes[pos.Code]; q != nil && q.Price > 0 {
			price = q.Price
		}
		alerts = append(alerts, Signal{
			ID:          seqID(),
			Code:        pos.Code,
			Name:        pos.Name,
			Strategy:    pos.Strategy,
			Direction:   "提醒",
			Action:      "减仓",    // 非自动卖出命中集（见函数注释）
			AlertType:   "系统性风险", // SellAction 不识别 → 只进消息中心，不自动执行
			Price:       price,
			Confidence:  conf,
			Reason:      fmt.Sprintf("市场风险档[%s]（%s）：建议对持仓 %s(%s) 主动控制仓位（系统提醒，不自动卖出）", tier, reasonText, pos.Name, pos.Code),
			GeneratedAt: now,
		})
	}
	if len(alerts) > 0 {
		log.Printf("[combat_agent] MarketRiskAlerts 风险档=%s → %d 条做多持仓减仓提醒（不自动执行）", tier, len(alerts))
	}
	return alerts
}

// ForwardMacroWarning §三 作用层6（差距4）前瞻预警：返回未来 warnDays 天内最近一个高影响事件
// （交割日/CPI/FOMC/NFP）的提醒文字 + 去重键；无则 hit=false。取数缺失/总开关关闭 → hit=false（不编造）。
// 引擎每日据此出一条"临近高影响事件"消息（Level=风险提示），提醒提前控制仓位。
// English: the P5 forward warning — returns the nearest high-impact event (delivery/CPI/FOMC/NFP) within
// warnDays as a reminder string + a dedup key (hit=false if none). Missing data or the master switch off
// → hit=false (never fabricated). The Engine emits one "upcoming event" message per day from it.
func (a *Agent) ForwardMacroWarning(now time.Time) (msg, key string, hit bool) {
	cfg := a.macroGateConfig()
	if !cfg.RiskGateOn() {
		return "", "", false
	}
	warnDays := cfg.WarnWindow()
	highSet := cfg.HighImpactSet()
	contractLvl := cfg.ContractLevelName()
	bestDays := warnDays + 1
	var bestTitle, bestLvl string
	bestDate := time.Time{}
	for _, ev := range macroEventsAt(now) {
		isHigh := highSet[ev.Level] || ev.Level == contractLvl || ev.Impact == "high"
		if !isHigh {
			continue
		}
		days := int(ev.Date.Sub(now).Hours() / 24)
		if days < 0 {
			continue // 已过事件日不再前瞻
		}
		if days <= warnDays && days < bestDays {
			bestDays, bestTitle, bestLvl, bestDate = days, ev.Title, ev.Level, ev.Date
		}
	}
	if bestTitle == "" {
		return "", "", false
	}
	when := "今日"
	switch bestDays {
	case 0:
		when = "今日"
	case 1:
		when = "明日"
	default:
		when = fmt.Sprintf("%d天后", bestDays)
	}
	msg = fmt.Sprintf("%s(%s)是%s，临近宏观高影响事件，注意提前控制仓位", when, bestDate.Format("01-02"), bestTitle)
	key = "macro-warning@" + bestDate.Format("2006-01-02") + "@" + bestLvl
	return msg, key, true
}
