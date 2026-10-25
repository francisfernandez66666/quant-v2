// sizing.go — §DYN-SIZE 动态仓位合成器（纯函数，无 IO）。
//
// 口径（docs/DYN_SIZE_FINAL_PLAN_20261016.md §2.3~§2.4）：
//
//	BuyScale  = clamp( min( Π权重因子 , m_上限因子1 , m_上限因子2 , ... ) , Floor , Cap )
//	Downgrade = 任一硬拦因子命中（情绪冰点 / 风险档Red / 个股LLM利空且风险档≥Yellow / 连续止损熔断）
//
// 为什么分层而不是纯连乘：权重类因子各自回答"这件事让仓位该多大"，乘性叠加是自然的；
// 上限类因子回答"不能超过某条线"，而波动高与相关性高往往是同一件事的两个投影，连乘会把同一
// 风险惩罚两次。故上限类各自算一个 0~1 乘子后**取 min**（沿用 pysystemtrade risk_overlay 的
// `mult = limit / max(limit, risk)`）。
//
// 弃权纪律：任何"不可得"的输入（NaN / 空串 / 0 / 日历未加载）→ 该因子**不进合成**，
// 既不当作 0 也不当作中性——把"未知"当"安全"是这套合成最危险的失败模式。
//
// English: §DYN-SIZE pure sizing synthesizer. Layered: weight/inclination factors multiply,
// risk-cap factors each yield a 0..1 multiplier and the smallest wins. Missing inputs abstain
// (never treated as 0 or as a neutral 1.0).
package signalctl

import (
	"fmt"
	"math"
	"strings"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// 情绪相位常量（与 internal/data 的 DetectEmotionPhaseV2 输出一致；此处用字面量避免 signalctl
// 依赖整个 data 包）。冰点为硬拦，退潮/背离/高潮为金额权重。
const (
	phaseIce     = "冰点"
	phaseRetreat = "退潮"
	phaseDiverge = "背离"
	phaseClimax  = "高潮"
)

// SizingResult 动态仓位合成结果。
//
//   - Scale：建议缩放系数（基准态恒 1.0；已 clamp 到 [Floor, Cap]）；
//   - Downgrade：true=命中硬拦因子，调用方应把买入信号降级为 hold（不执行）；
//   - Reasons：命中的因子与取值（留痕/前端 tooltip）。
//
// English: the synthesis result — Scale (clamped, 1.0 in the baseline), Downgrade (a hard-block
// factor fired → the caller should hold), and the fired-factor reasons for the audit trail.
type SizingResult struct {
	Scale     float64  `json:"scale"`
	Downgrade bool     `json:"downgrade,omitempty"`
	Reasons   []string `json:"reasons,omitempty"`
}

// ComputeSizing §DYN-SIZE 主入口：对一条买入信号算动态仓位缩放。
//
// 纯函数：同一 (Regime, Signal) 必得同一结果，可 100% 单测。基准态（全部因子未命中）恒返回
// Scale=1.0 / Downgrade=false，与改造前逐字一致。
//
// English: the pure entry point. Baseline (no factor fires) always returns Scale=1.0 / no downgrade.
func ComputeSizing(r Regime, sig combat_agent.Signal) SizingResult {
	cfg := r.SizeCfg
	if cfg.Floor <= 0 || cfg.Cap <= 0 {
		cfg = cfg.WithDefaults() // 防御：调用方忘了 WithDefaults 也不会算出 0
	}

	// 1. 硬拦因子：命中即降级（不进连乘，避免"既降级又缩额"的双重惩罚）。
	if down, why := sizingHardBlock(r, sig, cfg); down {
		return SizingResult{Scale: cfg.Floor, Downgrade: true, Reasons: []string{why}}
	}

	// §DYN-SIZE-v3：分组幂平均口径。旧 product 路径原样保留在下方，供回滚与逐字回归。
	// English: v3 grouped power-mean path; the legacy product path below is kept byte-identical
	// for rollback and regression.
	if cfg.ModeOf() == "power_mean" {
		return computeSizingPowerMean(r, sig, cfg)
	}

	// 2. 权重/倾向类：乘性连乘 Π（弃权因子直接跳过）。
	w := 1.0
	var reasons []string
	mul := func(name string, v float64) {
		if v <= 0 {
			return // 弃权
		}
		w *= v
		if v != 1.0 {
			reasons = append(reasons, fmt.Sprintf("%s×%.2f", name, v))
		}
	}
	mul("信号质量", qualityFactor(sig))
	mul("信号强度", kellyFactor(sig.Confidence, cfg))
	mul("个股波动率", volFactor(sig, cfg))
	mul("情绪相位", emotionFactor(r.EmotionPhase, cfg))
	mul("市场状态", stateFactor(r, cfg))
	mul("风险档", tierFactor(r.RiskTier, cfg))
	mul("宏观影响期", macroWindowFactor(r, cfg))
	mul("宏观前瞻", macroLeadFactor(r, cfg))
	mul("节前", holidayFactor(r.Holiday, cfg))
	mul("大盘跌幅", indexFactor(r.IndexChangePct, cfg))
	mul("LLM利空", llmFactor(sig, cfg))
	mul("广度", breadthFactor(r.BreadthUpRatio, cfg))
	// §FLOW-FACTOR：5 日净流入（主）+ 当日净流入（辅，幅度减半）→ 买入乘子。
	// 与信号质量/强度等同为个股组维度；比值未解析（0）→ 弃权，不进连乘。
	// English: 5-day (primary) and same-day (secondary) net-inflow buy multipliers; abstain when unresolved.
	mul("5日净流入", flow5DFactor(sig, cfg))
	mul("当日净流入", flowTodayFactor(sig, cfg))

	// 3. 上限类：各自 0~1 乘子取 min（不连乘——同一风险不得双罚）。
	// m 的初值必须是 +Inf 而不是 1.0：上限类只**向下**约束，若初值取 1.0 会把权重类的进攻档
	// （>1.0）一并压回 1.0，"允许进攻档 cap=1.15" 就永远不可达。
	// English: m starts at +Inf, not 1.0 — caps only constrain downward, and a 1.0 start would
	// silently clip the weight class's attack tier (>1.0), making cap=1.15 unreachable.
	m := math.Inf(1)
	for _, c := range []struct {
		name string
		v    float64
	}{
		{"总仓位上限", totalPosCap(r.Account, cfg)},
		{"权益回撤", drawdownCap(r.Account, cfg)},
		{"风险预算", riskBudgetCap(r, cfg)},
	} {
		if c.v <= 0 {
			continue // 未启用/不可得 → 弃权
		}
		if c.v < m {
			m = c.v
		}
		if c.v < 1.0 {
			reasons = append(reasons, fmt.Sprintf("%s×%.2f", c.name, c.v))
		}
	}

	// 4. 分层合成 + 地板/上限。
	scale := math.Min(w, m)
	if scale < cfg.Floor {
		scale = cfg.Floor
		reasons = append(reasons, "触地板")
	}
	if scale > cfg.Cap {
		scale = cfg.Cap
		reasons = append(reasons, "触上限")
	}
	return SizingResult{Scale: scale, Reasons: reasons}
}

// sizingFactor 一个已求值的仓位因子（v<=0 表示弃权）。
// English: one evaluated sizing factor; v<=0 means abstain.
type sizingFactor struct {
	name string
	v    float64
}

// powerMean 幂平均：p=0 退化为几何平均；空集返回 1.0（中性）。
// 公式：((Σv_i^p)/n)^(1/p)。只接受 v>0，调用方负责过滤弃权。
// English: power mean; p=0 is the geometric mean and an empty set is neutral (1.0).
func powerMean(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 1.0
	}
	if p == 0 {
		prod := 1.0
		for _, v := range vals {
			prod *= v
		}
		return math.Pow(prod, 1/float64(len(vals)))
	}
	sum := 0.0
	for _, v := range vals {
		sum += math.Pow(v, p)
	}
	return math.Pow(sum/float64(len(vals)), 1/p)
}

// sizingGroup 对一组因子做幂平均并套组 floor；返回组值和可读理由。
// 组内全部弃权 → 组值 1.0（不奖不罚），理由明确写"无"。
// English: power-mean one factor group, apply its floor, and return a readable reason.
// An all-abstain group is neutral (1.0) and says so explicitly.
func sizingGroup(name string, floor float64, factors []sizingFactor, p float64) (float64, string) {
	vals := make([]float64, 0, len(factors))
	parts := make([]string, 0, len(factors))
	for _, f := range factors {
		if f.v <= 0 {
			continue
		}
		vals = append(vals, f.v)
		if f.v != 1.0 {
			parts = append(parts, fmt.Sprintf("%s×%.2f", f.name, f.v))
		}
	}
	if len(vals) == 0 {
		return 1.0, fmt.Sprintf("%s:无→1.00", name)
	}
	g := powerMean(vals, p)
	floored := false
	if floor > 0 && g < floor {
		g = floor
		floored = true
	}
	if len(parts) == 0 {
		parts = append(parts, "中性")
	}
	reason := fmt.Sprintf("%s:%s→%.3f", name, strings.Join(parts, "|"), g)
	if floored {
		reason += "(触地板)"
	}
	return g, reason
}

// computeSizingPowerMean v3 合成：市场组/个股组各自幂平均，再组间幂平均；上限类仍取 min。
// English: v3 synthesis — market and stock groups each power-meaned, then combined; caps still min.
func computeSizingPowerMean(r Regime, sig combat_agent.Signal, cfg config.SizingConfig) SizingResult {
	p := cfg.PowerPOf()
	market := []sizingFactor{
		{"情绪相位", emotionFactor(r.EmotionPhase, cfg)},
		{"市场状态", stateFactor(r, cfg)},
		{"风险档", tierFactor(r.RiskTier, cfg)},
		{"宏观影响期", macroWindowFactor(r, cfg)},
		{"宏观前瞻", macroLeadFactor(r, cfg)},
		{"节前", holidayFactor(r.Holiday, cfg)},
		{"大盘跌幅", indexFactor(r.IndexChangePct, cfg)},
		{"广度", breadthFactor(r.BreadthUpRatio, cfg)},
	}
	stock := []sizingFactor{
		{"信号质量", qualityFactor(sig)},
		{"信号强度", kellyFactor(sig.Confidence, cfg)},
		{"个股波动率", volFactor(sig, cfg)},
		{"LLM利空", llmFactor(sig, cfg)},
		// §FLOW-FACTOR：两个净流入维度同属个股组（都是"这只票本身"的证据）。
		// 5 日是主、当日是辅（当日档位幅度减半）；未解析 → 0，sizingGroup 按弃权跳过。
		{"5日净流入", flow5DFactor(sig, cfg)},
		{"当日净流入", flowTodayFactor(sig, cfg)},
	}
	mg, mr := sizingGroup("市场组", cfg.GroupFloor("market"), market, p)
	sg, sr := sizingGroup("个股组", cfg.GroupFloor("stock"), stock, p)
	raw := powerMean([]float64{mg, sg}, p)
	reasons := []string{mr, sr, fmt.Sprintf("组间→%.3f", raw)}

	// 上限类仍取 min（与旧路径同口径：只向下约束，不连乘）。
	m := math.Inf(1)
	for _, c := range []struct {
		name string
		v    float64
	}{
		{"总仓位上限", totalPosCap(r.Account, cfg)},
		{"权益回撤", drawdownCap(r.Account, cfg)},
		{"风险预算", riskBudgetCap(r, cfg)},
	} {
		if c.v <= 0 {
			continue
		}
		if c.v < m {
			m = c.v
		}
		if c.v < 1.0 {
			reasons = append(reasons, fmt.Sprintf("%s×%.2f", c.name, c.v))
		}
	}

	scale := math.Min(raw, m)
	if scale < cfg.Floor {
		scale = cfg.Floor
		reasons = append(reasons, "触地板")
	}
	if scale > cfg.Cap {
		scale = cfg.Cap
		reasons = append(reasons, "触上限")
	}
	return SizingResult{Scale: scale, Reasons: reasons}
}

// sizingHardBlock 硬拦因子判定：命中即降级为 hold（不进金额连乘）。
// English: hard-block factors — a hit downgrades the buy to hold instead of scaling it.
func sizingHardBlock(r Regime, sig combat_agent.Signal, cfg config.SizingConfig) (bool, string) {
	// 情绪冰点：整体无承接，买入一律不执行（与既有 applyRiskTier 的动作降级同向）。
	if r.EmotionPhase == phaseIce {
		return true, "情绪冰点"
	}
	// 风险档 Red：默认**不硬拦**，由 tier_scales.Red（默认 0.30）收束仓位 + 信号层置信度门槛；
	// 只有 red_block_new_buy=true（应急阀）才整笔拒绝。owner 2026-10-21：收束优先，不全拒。
	// English: Red is not a hard block by default — it scales size via tier_scales.Red and the
	// confidence gate; only the explicit red_block_new_buy emergency switch rejects outright.
	if r.RiskTier == combat_agent.RiskTierRed && r.RedBlockNewBuy {
		return true, "风险档Red禁止新开仓"
	}
	// 个股 LLM 利空 且 风险档≥Yellow：个股有负面事件、大盘也在收紧 → 硬拦。
	if sig.D1Blocked && (r.RiskTier == combat_agent.RiskTierYellow || r.RiskTier == combat_agent.RiskTierRed) {
		return true, "个股LLM利空+风险档≥Yellow"
	}
	// 连续止损熔断（freqtrade StoplossGuard 语义）：回看窗内止损达阈值 → 暂停新开仓。
	if cfg.StoplossGuardCount > 0 && r.Account.WindowStopLosses >= cfg.StoplossGuardCount {
		return true, fmt.Sprintf("连续止损熔断(%d次)", r.Account.WindowStopLosses)
	}
	// 长假前最后一日禁止新开仓（owner 风险偏好项，默认关）。
	if cfg.HolidayBlockNewOn() && isLongHolidayLastDay(r.Holiday) {
		return true, "长假前最后一日禁止新开仓"
	}
	return false, ""
}

// ── §DYN-SIZE 卖出加权（L1/L2/L3，方案 §2.5） ──────────────────────────────────────

// 卖出加权档位常量。"" = 未命中（卖出裁决走基准口径，与改造前逐字一致）。
const (
	// SellBoostL1 只标注：理由/级别升级 + 消息中心高亮，不改判定。
	SellBoostL1 = "L1"
	// SellBoostL2 缩短确认窗 + 取消延持 + 单源利空即算 dual（owner 裁决的默认档）。
	SellBoostL2 = "L2"
	// SellBoostL3 L2 全部 + 把"首触止损未深破半平"升级为全平（trim→close，扩大自动卖出面）。
	// owner 2026-10-16 明示授权（此前按裁决范围只到 L2，该档曾与 L2 同真属刻意的灰度预留）。
	SellBoostL3 = "L3"
)

// ComputeSellBoost 计算卖出加权档位（纯函数）。
//
// 需求（owner 原话）："宏观事件来临或者大盘情绪不佳 → 买入降权、卖出加权"、"节假日或周末前
// 最后一两个交易日严买入宽卖出"。触发条件任一命中即加权（全部列出，便于审计归因）：
//
//  1. 情绪相位 ∈ {冰点, 退潮}——整体无承接；
//  2. 风险档 ∈ {Red, Yellow}——系统性风险；
//  3. 大盘跌幅 ≤ −2%——集体资金出逃；
//  4. 节前窗口（周末/小长假/长假的节前第 1~2 个交易日）——防假期砸盘；
//  5. 账户连续止损熔断（回看窗内止损达阈值）——自己的实亏最直接；
//  6. 宏观事件影响期内且影响等级 high。
//
// 返回 ("", nil) 的三种情形：总开关关 / 档位显式关（off/none/0）/ 无触发。全部"不可得"读数
// （NaN / 空串 / 日历未加载）一律**不触发**——卖出加权是"更早离场"的授权，不能凭未知给出。
//
// English: sell-boost tier from any of six triggers. A disabled master switch, an explicit off, or
// no trigger all return "". Missing readings never trigger — this authorizes an earlier exit, so
// "unknown" must not be spent as if it were risk.
func ComputeSellBoost(r Regime) (string, []string) {
	cfg := r.SizeCfg
	if !cfg.EnabledOn() {
		return "", nil // 总开关关（账号级显式关闭）：零行为变更
	}
	if cfg.Floor <= 0 || cfg.Cap <= 0 {
		cfg = cfg.WithDefaults()
	}
	lvl := cfg.SellBoostLevelOf()
	if lvl == "" {
		return "", nil // 显式关闭
	}
	reasons := sellBoostTriggers(r, cfg)
	if len(reasons) == 0 {
		return "", nil
	}
	return lvl, reasons
}

// sellBoostTriggers 逐条判定卖出加权触发条件，返回命中说明（空=未命中）。
// English: evaluates each sell-boost trigger and returns the fired ones.
func sellBoostTriggers(r Regime, cfg config.SizingConfig) []string {
	var out []string
	if r.EmotionPhase == phaseIce || r.EmotionPhase == phaseRetreat {
		out = append(out, "情绪相位="+r.EmotionPhase)
	}
	switch r.RiskTier {
	case combat_agent.RiskTierRed, combat_agent.RiskTierYellow:
		out = append(out, "风险档="+r.RiskTier)
	}
	// 大盘跌幅：NaN=读数不可得 → 不触发；零值（快照未装配）=0 > −2 → 自然不触发。
	if !math.IsNaN(r.IndexChangePct) && r.IndexChangePct <= -2 {
		out = append(out, fmt.Sprintf("大盘%.1f%%", r.IndexChangePct))
	}
	// 节前：只在有效节前窗内（与 holidayFactor 的 PreHolidayN<=2 门同口径）；日历未加载不触发。
	if h := r.Holiday; h.CalendarLoaded && h.ClosedRun >= 2 && h.PreHolidayN >= 1 && h.PreHolidayN <= 2 {
		out = append(out, fmt.Sprintf("节前第%d日(休市%d天)", h.PreHolidayN, h.ClosedRun))
	}
	if cfg.StoplossGuardCount > 0 && r.Account.WindowStopLosses >= cfg.StoplossGuardCount {
		out = append(out, fmt.Sprintf("连续止损%d次", r.Account.WindowStopLosses))
	}
	for _, e := range r.MacroEvents {
		if e.InWindow && e.Impact == "high" {
			out = append(out, "宏观事件影响期:"+e.Title)
			break // 只记一条，避免同轮多事件刷屏
		}
	}
	return out
}

// SellBoostApplies 该档位是否达到 L2（缩短确认窗 / 取消延持 / 单源利空即算 dual）。
// English: whether the tier reaches L2 (shorter confirm window, no extension, single-source bear
// counts as dual).
func SellBoostApplies(lvl string) bool { return lvl == SellBoostL2 || lvl == SellBoostL3 }

// SellBoostEscalatesTrim 该档位是否把"首触止损未深破的半平"升级为全平（trim→close，L3 独有）。
// 与 SellBoostApplies 分成两个函数是刻意的：L2/L3 共享判定面，只有 L3 加码，调用点要能分别
// 断言"没到 L3 就不许动 trim"。
// English: whether the tier escalates the first-touch half-trim into a full close (L3 only).
// Kept separate from SellBoostApplies so call sites can assert "below L3 the trim is untouched".
func SellBoostEscalatesTrim(lvl string) bool { return lvl == SellBoostL3 }

// qualityFactor 个股信号质量（归一化权重）：复用 research.SignalQualityTable 的 Weight 输出。
// 引擎把该权重解析后写入 Signal.QualityWeight（<=0 = 未解析 → 弃权）。
// 表本身已 ±10% 限幅，这里再兜一层防越界配置。
// English: per-signal quality weight (from SignalQualityTable, resolved by the engine onto the
// signal); <=0 means unresolved → abstain.
func qualityFactor(sig combat_agent.Signal) float64 {
	if sig.QualityWeight <= 0 {
		return 0
	}
	return clampF(sig.QualityWeight, 0.9, 1.1)
}

// kellyFactor 个股信号强度 → Fractional Kelly 缩放。
//
// f = λ(2p−1)（López de Prado 半凯利），λ 默认 0.5。本层把 edge=(2p−1)∈[0,1] 线性映射到
// [0.6, 1.15]：`mult = clamp(0.6 + λ·edge·1.1, 0.6, 1.15)`——λ=0.5、p=1.0 恰好触 1.15 上限。
// p < KellyMinProb（默认 0.52）视为无优势 → 固定 0.6。p<=0（置信度不可得）→ 弃权。
// English: signal strength → fractional-Kelly scale; λ controls aggression; p<min → no edge (0.6).
func kellyFactor(p float64, cfg config.SizingConfig) float64 {
	if p <= 0 {
		return 0 // 置信度不可得 → 弃权
	}
	if p < cfg.KellyMinProb {
		return 0.6 // 无优势：不因"低置信"而完全放弃，但显著缩额
	}
	edge := 2*p - 1
	if edge < 0 {
		edge = 0
	}
	if edge > 1 {
		edge = 1
	}
	return clampF(0.6+cfg.KellyLambda*edge*1.1, 0.6, 1.15)
}

// volFactor 个股波动率（vol targeting）：低波动股放大、高波动股缩小。
// 直接复用信号上已携带的 ATR14（Signal.ATR 的注释原文即"供仓位管理与动态止损参考"），
// 零新增上游成本。ATR/现价 不可得 → 弃权。
// English: volatility targeting from the ATR already carried on the signal; missing → abstain.
func volFactor(sig combat_agent.Signal, cfg config.SizingConfig) float64 {
	if sig.ATR <= 0 || sig.Price <= 0 {
		return 0
	}
	atrPct := sig.ATR / sig.Price * 100
	if atrPct <= 0 {
		return 0
	}
	return clampF(cfg.VolTargetPct/atrPct, cfg.VolClampLo, cfg.VolClampHi)
}

// emotionFactor 情绪相位金额权重（冰点已走硬拦，此处不处理）。
// English: emotion-phase money weight (冰点 is handled by the hard block).
func emotionFactor(phase string, cfg config.SizingConfig) float64 {
	if phase == "" || phase == phaseIce {
		return 0 // 空=弃权；冰点=硬拦（防御性跳过）
	}
	if v, ok := cfg.EmotionScales[phase]; ok {
		return v
	}
	return 0 // 未知相位 → 弃权（不当作中性）
}

// stateFactor 市场状态金额权重。**只用离散状态档**，不再乘 MaxPosPct——后者由同一状态机派生，
// 二次相乘属"同一风险双罚"（方案 §5 测试锁明确禁止）。
// English: market-state weight from the discrete state only; MaxPosPct is deliberately excluded
// (it derives from the same state machine — multiplying again would double-penalize one risk).
func stateFactor(r Regime, cfg config.SizingConfig) float64 {
	if r.MarketState == "" {
		return 0 // 状态机关闭 → 弃权
	}
	if v, ok := cfg.StateScales[r.MarketState]; ok {
		return v
	}
	return 0
}

// tierFactor 风险档金额权重。空档=已知中性 1.0；Red 默认 0.30（可配），不再返回 0——
// 硬拦只走 sizingHardBlock 且需 red_block_new_buy=true。
// English: risk-tier money weight. Empty = neutral; Red defaults to 0.30 (configurable), never 0
// here — hard blocking lives only in sizingHardBlock behind red_block_new_buy.
func tierFactor(tier string, cfg config.SizingConfig) float64 {
	switch tier {
	case combat_agent.RiskTierRed:
		if v, ok := cfg.TierScales[combat_agent.RiskTierRed]; ok {
			if v <= 0 {
				return 0 // 显式 0/脏值 = 该维弃权（进化器可关；默认 0.30 只在缺键时生效）
			}
			return v
		}
		return 0.30
	case combat_agent.RiskTierYellow:
		if v, ok := cfg.TierScales["Yellow"]; ok {
			return v
		}
		return 0
	}
	return 1.0
}

// macroWindowFactor 宏观事件影响期缩放（取最严的一条）。无影响期事件 → 弃权。
// English: macro impact-window scale (the strictest event wins); no in-window event → abstain.
func macroWindowFactor(r Regime, cfg config.SizingConfig) float64 {
	worst := 0.0
	for _, e := range r.MacroEvents {
		if !e.InWindow {
			continue
		}
		v, ok := cfg.MacroWindowScales[e.Impact]
		if !ok {
			continue
		}
		if worst == 0 || v < worst {
			worst = v
		}
	}
	return worst
}

// macroLeadFactor 宏观事件**前瞻**缩放：事件前 N 个**交易日**渐进收仓（取最严的一条）。
//
//	DaysToEvent==0（事件当日）→ MacroEventDayScale（默认 0.30）
//	1 ≤ DaysToEvent ≤ MacroLeadDays → MacroLeadCurve[MacroLeadDays − DaysToEvent]
//	   （默认 [T-5 0.90, T-4 0.85, T-3 0.80, T-2 0.70, T-1 0.50]）
//
// 距离必须是**交易日**：跨周末/长假用自然日会把国庆 8 天算成 8 个"交易日"（方案 §5 测试锁）。
// English: forward-looking macro scaling by trading-day distance (T-day 0.30, then the curve);
// natural-day distance is a bug across weekends/long holidays.
func macroLeadFactor(r Regime, cfg config.SizingConfig) float64 {
	worst := 0.0
	for _, e := range r.MacroEvents {
		d := e.DaysToEvent
		var v float64
		switch {
		case d == 0:
			v = cfg.MacroEventDayScale
		case d >= 1 && d <= cfg.MacroLeadDays:
			idx := cfg.MacroLeadDays - d
			if idx >= 0 && idx < len(cfg.MacroLeadCurve) {
				v = cfg.MacroLeadCurve[idx]
			}
		}
		if v <= 0 {
			continue
		}
		if worst == 0 || v < worst {
			worst = v
		}
	}
	return worst
}

// isLongHolidayLastDay 是否长假前最后一个交易日（用于"禁止新开仓"硬拦）。
// English: whether today is the last trading day before a long holiday.
func isLongHolidayLastDay(h HolidayWindow) bool {
	return h.CalendarLoaded && h.ClosedRun >= 5 && h.PreHolidayN == 1
}

// holidayFactor 节前因子：周末严买入（0.85）、小长假（节前1日 0.70 / 2日 0.85）、
// 长假（节前1日 0.40 / 2日 0.60 / 3日 0.75）。日历未加载 → **弃权**（绝不把未知当普通日）。
// English: pre-holiday factor — weekend 0.85, short break 0.70/0.85, long break 0.40/0.60/0.75;
// calendar not loaded → abstain (never treat "unknown" as a normal day).
func holidayFactor(h HolidayWindow, cfg config.SizingConfig) float64 {
	if !h.CalendarLoaded {
		return 0
	}
	if h.PreHolidayN <= 0 {
		return 1.0 // 已知普通日
	}
	key := ""
	switch {
	case h.ClosedRun >= 5: // 长假
		if h.PreHolidayN <= 3 {
			key = fmt.Sprintf("long%d", h.PreHolidayN)
		}
	case h.ClosedRun >= 3: // 小长假
		if h.PreHolidayN <= 2 {
			key = fmt.Sprintf("small%d", h.PreHolidayN)
		}
	case h.ClosedRun == 2: // 周末
		// 只在"周末前最后一两个交易日"生效：PreHolidayN 是从今日起连续可交易的天数，
		// 周二时该值为 4（周二/三/四/五）——若不加此门，周末因子会从周二就开始压低仓位。
		if h.PreHolidayN <= 2 {
			key = "weekend"
		}
	}
	if key == "" {
		return 1.0 // 节前窗口之外 → 已知中性
	}
	if v, ok := cfg.HolidayScales[key]; ok {
		return v
	}
	return 1.0
}

// indexFactor 大盘跌幅映射（带宽固定 -1% / -2%，缩放值可配）。
//   - ≥ -1%      → IndexScaleThresholds["-1"]（默认 1.00）
//   - -1% ~ -2%  → ["-2"]（默认 0.70）
//   - ≤ -2%      → ["deep"]（默认 0.40）
//
// NaN（大盘读数不可得）→ 弃权。
// English: index-drop mapping with fixed bands and configurable scales; NaN → abstain.
func indexFactor(chg float64, cfg config.SizingConfig) float64 {
	if math.IsNaN(chg) {
		return 0
	}
	switch {
	case chg >= -1:
		return scaleOr(cfg.IndexScaleThresholds, "-1", 1.00)
	case chg > -2:
		return scaleOr(cfg.IndexScaleThresholds, "-2", 0.70)
	default:
		return scaleOr(cfg.IndexScaleThresholds, "deep", 0.40)
	}
}

// llmFactor 个股 LLM 利空缩放（首次接入买入侧；风险档≥Yellow 时该股已走硬拦）。
// English: per-stock LLM-bearish scale (the risk-tier≥Yellow case is a hard block already).
func llmFactor(sig combat_agent.Signal, cfg config.SizingConfig) float64 {
	if !sig.D1Blocked {
		return 0 // 无利空 → 弃权
	}
	return cfg.LLMBearishScale
}

// breadthFactor 市场广度映射（带宽固定 0.30 / 0.50）：<0.30→0.70；0.30~0.50→0.85；否则 1.00。
// NaN（广度不可得）→ 弃权。
// English: breadth mapping with fixed bands; NaN → abstain.
func breadthFactor(up float64, cfg config.SizingConfig) float64 {
	// §N6-RISK（2026-10-21 owner 裁决）：读数**缺失（NaN）**不再弃权，而是按
	// BreadthMissingScale 做小百分比降权（默认 0.90），数据恢复后自动回到真实广度档。
	// 只降这一处，不叠加状态档——避免把"数据没到"当成"市场转熊"再罚一次。
	// English: a missing (NaN) breadth reading applies a small haircut (default 0.90) instead of
	// abstaining; the real factor returns as soon as the reading does. Applied exactly once.
	if math.IsNaN(up) {
		v := cfg.BreadthMissingScale
		// 平面字段无三态（缺键与显式 0 不可分）：沿用"非正兜 0.90"。广度维度的参与面
		// 因此不可配置关闭（结构恒开）——强度由 BreadthMissingScale 本身调节。
		// English: a plain float has no tri-state (absent vs explicit 0 are indistinguishable),
		// so a non-positive value keeps the 0.90 haircut; breadth participation is always on.
		if v <= 0 {
			v = 0.90
		}
		return v
	}
	// 0 是 float64 零值（快照未装配）也是"全跌"的合法读数，无法区分——沿用旧口径弃权，
	// 全跌由情绪相位与大盘跌幅因子覆盖；只有 NaN 才是"取数缺失"，走上面的降权。
	// English: 0 is ambiguous (zero value vs everything-down) → keep the legacy abstain; only NaN
	// means "reading missing" and takes the haircut above.
	if up <= 0 {
		return 0
	}
	switch {
	case up < 0.30:
		return 0.70
	case up < 0.50:
		return 0.85
	default:
		return 1.0
	}
}

// totalPosCap 总仓位上限乘子（pysystemtrade risk_overlay 口径：limit/max(limit,risk)）。
// 未启用（MaxTotalPositionPct<=0）或持仓占比不可得（<=0）→ 弃权。
// English: total-position cap multiplier (risk_overlay: limit/max(limit,risk)); disabled/unknown → abstain.
func totalPosCap(a AccountState, cfg config.SizingConfig) float64 {
	limit := cfg.MaxTotalPositionPct
	if limit <= 0 || a.TotalPosPct <= 0 {
		return 0
	}
	if a.TotalPosPct <= limit {
		return 1.0
	}
	return limit / a.TotalPosPct
}

// drawdownCap 账户权益回撤熔断乘子（同上口径）。未启用或回撤不可得 → 弃权。
// English: account equity-drawdown cap multiplier; disabled/unknown → abstain.
func drawdownCap(a AccountState, cfg config.SizingConfig) float64 {
	limit := cfg.MaxDrawdownPct
	if limit <= 0 || a.EquityDrawdown <= 0 {
		return 0
	}
	if a.EquityDrawdown <= limit {
		return 1.0
	}
	return limit / a.EquityDrawdown
}

// riskBudgetCap 风险预算守恒上限乘子（§2.6 配套，owner 已裁决强制）。
//
// 为什么它是**上限类**而不是权重类：它回答的是"不能超过某条线"——止损从 6% 放宽到 9% 时，
// 同一笔金额的最大亏损被悄悄放大 50%，必须同比例缩仓（6/9 = 0.67）把预算拉回来。它与"大盘跌幅"
// 这类权重因子描述的是同一件事的两个投影（同一次股灾），连乘会把同一个风险罚两次。
//
// 收紧（offset<0）时系数 >1，但**只向下修正**（恒 ≤1）——收紧本身已降低风险，不应借此加杠杆。
// 未启用（`sl_offset_risk_budget_keep=false`）/ 基准止损不可得 / 无偏移 → 弃权。
// English: the risk-budget-conservation cap. Loosening 6%→9% would inflate a single trade's max
// loss by 50%, so the position shrinks by 6/9. Only downward (tightening never sizes up); abstains
// when disabled, when the base stop is unknown, or when there is no offset.
func riskBudgetCap(r Regime, cfg config.SizingConfig) float64 {
	if !cfg.RiskBudgetKeepOn() {
		return 0 // owner 关掉了守恒 → 弃权（只放宽不缩仓）
	}
	if r.StopLossOffsetPct <= 0 || r.BaseStopLossPct <= 0 {
		return 0 // 无放宽 / 基准不可得 → 弃权（不放杠杆）
	}
	eff := r.BaseStopLossPct + r.StopLossOffsetPct
	if eff <= 0 {
		return 0
	}
	sc := r.BaseStopLossPct / eff
	if sc >= 1.0 {
		return 0 // 恒 ≤1：等于 1 时不进合成（不写"×1.00"的无意义理由）
	}
	return sc
}

// clampF 把 v 夹到 [lo, hi]（lo>hi 时返回 hi，防配置写反）。
func clampF(v, lo, hi float64) float64 {
	if lo > hi {
		lo, hi = hi, lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// scaleOr 读映射键。**显式 0 = 该维弃权**（abstain）：进化器（sizing_evo C 组合层）关掉
// 一个因子的唯一手段就是把它所有档位写 0，这里必须放行而不是兜底成默认值——否则"关因子"
// 在配置层表达不出来。缺键或负值（脏值）才用默认。
// English: reads a map key; an explicit 0 means abstain (the evo loop's only way to switch a
// factor off), a missing key or negative dirty value falls back to the default.
func scaleOr(m map[string]float64, key string, def float64) float64 {
	if v, ok := m[key]; ok {
		if v == 0 {
			return 0
		}
		if v > 0 {
			return v
		}
	}
	return def
}
