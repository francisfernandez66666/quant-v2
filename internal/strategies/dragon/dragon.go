// Package dragon 实现破局龙战法（Dragon Strategy）。
//
// 市场模式：寻找板块内最先涨停的龙头股，在首次分歧/炸板后的回封或弱转强时介入。
// 核心逻辑：龙头股在首次封板后可能出现开板分歧，若回封成功则二次介入机会。
//
// 四维评分体系（F1~F4）：
//
//   - F1 封板质量（权重来自配置）：涨幅 >9.5% 证明封板，成交量/成交额比值反映封板力度。
//     质量越高说明主力封板意愿越强，次日溢价概率越大。
//
//   - F2 板块共振（权重来自配置）：板块最强个股涨幅。
//     >3% 给满分，>1% 给半值。板块效应是龙头溢价的基础。
//
//   - F3 溢价率（权重来自配置）：个股涨幅偏离板块最强涨幅 2%+ 时给满分；
//     否则个股涨幅 >5% 给半值。反映个股在板块内的相对强度和辨识度。
//
//   - F4 RS 强度（权重来自配置）：近 5 日股价趋势强度。
//     5日涨幅 >10% 给满分，>5% 给半值。中期趋势向上增加持股信心。
//
// 信号阈值：
//   - total ≥ 70 → full_chain（完整链），置信度 >0.8 → P1，其余 P2
//   - total ≥ 50 → brief（半确认），P3_5 观察
//   - total < 50 → watch，不操作
//
// 买点说明：
//
//	P1_first_to_second：首封→二封（F1+F2 高分时）
//	P2_divergence：分歧转一致（F3 高分时）
//	P3_weak_to_strong：弱转强（F4 高分时）
//	P4_pullback：回调低吸（PullbackMaxPct 配置）
//
// （English: Implements the "Dragon" sector-leader strategy — enter the strongest limit-up stock of a sector on the
// first re-seal after divergence, scoring F1 seal quality / F2 sector resonance / F3 premium / F4 RS strength, with
// 70/50 thresholds→ full_chain/brief/watch and four buy-point categories.）
package dragon

import (
	"math"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
)

// DragonStrategy 破局龙战法策略结构。（DragonStrategy is the Dragon strategy struct.）
// 使用 F1~F4 四维评分判断龙头股封板质量和参与机会。（Scores sector-leader seal quality via F1~F4.）
type DragonStrategy struct {
	cfg    *config.Manager // 配置管理器（热加载 DragonConfig）（Config manager, hot-reloads DragonConfig）
	userID string          // 账号 ID：非空时按该账号的策略配置读取，否则回退全局（Account ID: per-account strategy config when set, else global）
}

// New 创建破局龙战法策略实例。（New creates a Dragon strategy instance.）
func New(cfg *config.Manager) *DragonStrategy {
	return &DragonStrategy{cfg: cfg}
}

// SetUserID 设置账号 ID（多账号独立引擎时，各账号 runner 按本账号配置读取）。
// English: sets the account ID so a per-account engine's runner reads that account's config.
func (d *DragonStrategy) SetUserID(userID string) {
	d.userID = userID
}

// strategyCfg 返回当前账号的破局龙配置（账号级覆盖优先，否则全局）。
// English: returns the Dragon config for the current account (account override wins, else global).
func (d *DragonStrategy) strategyCfg() config.DragonConfig {
	if d.userID != "" && d.cfg != nil {
		return d.cfg.GetStrategyConfigFor(d.userID).Dragon
	}
	if d.cfg != nil {
		return d.cfg.Get().Strategy.Dragon
	}
	return config.DragonConfig{}
}

// Name 返回策略中文名称"破局龙战法"。（Name returns the strategy display name "破局龙战法".）
func (d *DragonStrategy) Name() string {
	return "破局龙战法"
}

// Type 返回信号类型标识 SignalDragon。（Type returns the signal type SignalDragon.）
func (d *DragonStrategy) Type() strategy.SignalType {
	return strategy.SignalDragon
}

// Evaluate 标准接口（占位）。实际使用 EvaluateReal 传入结构化数据。（Standard interface stub; real scoring uses EvaluateReal.）
func (d *DragonStrategy) Evaluate(code string, data interface{}) (*strategy.Evaluation, error) {
	return &strategy.Evaluation{Pass: false, Level: "nodata", Confidence: 0}, nil
}

// EvaluateReal 执行破局龙战法核心评分。（EvaluateReal runs the core Dragon scoring.）
// 输入：si（个股实时信息，含价格/涨幅/成交量）、kLines（日K线，用于RS趋势）、
// sectors（板块信息列表，用于板块共振判断）。
//
// F1 封板质量：涨幅 >9.5% 即视为触及涨停，用成交量/成交额比例衡量封板力度。
// F2 板块共振：取所有板块最大涨幅。
// F3 溢价率：个股涨幅超出板块最强涨幅 2%+ 说明辨识度突出。
// F4 RS 强度：近 5 日趋势涨幅。
//
// （English: Inputs are si (realtime info), kLines (daily bars for RS trend) and sectors (for resonance). Scores the
// seal quality F1, sector resonance F2, premium F3 and 5-day RS strength F4.）
func (d *DragonStrategy) EvaluateReal(code string, si *data.StockInfo, kLines []data.KLine, sectors []data.SectorInfo) *strategy.Evaluation {
	// 基础数据校验：无实时价或 K 线不足 5 根无法评分（Guard: require a live price and ≥5 bars to score）
	if si == nil || si.Price <= 0 || len(kLines) < 5 {
		return nil
	}
	dc := d.strategyCfg()

	// F1: 封板质量 — 基于涨幅和成交量（F1: seal quality — based on gain and volume）
	// 涨幅>9.5%视为封板，量额比高说明封板坚决。（Gain >9.5% counts as sealed; a high volume/turnover ratio implies a firm seal.）
	f1 := 0.0
	if si.ChangePct > 9.5 {
		// 基础分 = 权重×90%；量额比（成交量/成交额）越高封板越坚决，最高补充 10%（Base=weight×90%; volume/turnover ratio adds up to 10%）
		f1 = dc.F1SealWeight * 100 * 0.9
		if si.Amount > 0 {
			f1 += (float64(si.Volume) / si.Amount) * 0.1 * dc.F1SealWeight * 100
		}
	}

	// F2: 板块共振 — 取板块最大涨幅（F2: sector resonance — max sector gain）
	f2 := 0.0
	bestSector := 0.0
	for _, sec := range sectors {
		if sec.ChangePct > bestSector {
			bestSector = sec.ChangePct
		}
	}
	// 板块涨幅 >3% → 满分；>1% → 半值；否则 0（Sector gain >3% → full marks, >1% → half, else 0）
	if bestSector > 3 {
		f2 = dc.F2ResonanceWeight * 100
	} else if bestSector > 1 {
		f2 = dc.F2ResonanceWeight * 100 * 0.5
	}

	// F3: 溢价率 — 个股涨幅偏离板块最强涨幅（F3: premium — stock gain vs. the strongest sector gain）
	// 超出最强板块 2%+ → 满分（辨识度突出）；个股自身 >5% → 半值（+2% above the best sector → full marks (outstanding); own gain >5% → half）
	f3 := 0.0
	if bestSector > 0 && si.ChangePct > bestSector+2 {
		f3 = dc.F3PremiumWeight * 100
	} else if si.ChangePct > 5 {
		f3 = dc.F3PremiumWeight * 100 * 0.5
	}

	// F4: RS强度 — 近5日趋势涨幅（F4: RS strength — 5-day trend gain）
	f4 := 0.0
	if len(kLines) >= 5 {
		trend := (kLines[len(kLines)-1].Close - kLines[len(kLines)-5].Close) / kLines[len(kLines)-5].Close * 100
		// 5日涨幅 >10% → 满分；>5% → 半值（5-day gain >10% → full marks, >5% → half）
		if trend > 10 {
			f4 = dc.F4RsWeight * 100
		} else if trend > 5 {
			f4 = dc.F4RsWeight * 100 * 0.5
		}
	}

	// 总分封顶 100；pass 至少需 ≥50（Cap total at 100; pass requires ≥50）
	total := math.Min(f1+f2+f3+f4, 100)
	pass := total >= 50
	level := "watch"
	// 总分 ≥70 → full_chain（完整链，买入）；≥50 → brief（半确认，观察）（Total ≥70 → full_chain (buy); ≥50 → brief (watch)）
	if total >= 70 {
		level = "full_chain"
		pass = true
	} else if total >= 50 {
		level = "brief"
	}

	return &strategy.Evaluation{
		TotalScore: total,
		Details: map[string]float64{
			"f1_seal":      f1,
			"f2_resonance": f2,
			"f3_premium":   f3,
			"f4_rs":        f4,
		},
		Pass:       pass,
		Level:      level,
		Confidence: total / 100.0,
	}
}

// GenerateSignal 将评分结果转化为交易信号。（GenerateSignal converts an evaluation into a trade signal.）
// full_chain → buy，置信度>0.8 → P1，否则 P2。（full_chain→buy; confidence>0.8→P1 otherwise P2.）
// brief → watch，P3_5。（brief→watch with priority P3_5.）
func (d *DragonStrategy) GenerateSignal(code string, eval *strategy.Evaluation) (*strategy.Signal, error) {
	// 默认：仅观察（watch / P3）（Default: watch only with P3）
	prio := strategy.P3
	action := strategy.ActionWatch

	switch eval.Level {
	case "full_chain":
		// 完整链确认：买入信号，按置信度分 P1/P2（Full-chain confirmation: buy, tiered by confidence to P1/P2）
		action = strategy.ActionBuy
		if eval.Confidence > 0.8 {
			prio = strategy.P1
		} else {
			prio = strategy.P2
		}
	case "brief":
		// 半确认：保持观察，P3_5（Half confirmation: keep watching at P3_5）
		action = strategy.ActionWatch
		prio = strategy.P3_5
	}

	// 复制评分明细到 Meta（供前端展示 F1~F4 分数）（Copy score details into Meta for the frontend）
	meta := make(map[string]float64)
	for k, v := range eval.Details {
		meta[k] = v
	}

	return &strategy.Signal{
		Type:       strategy.SignalDragon,
		Action:     action,
		Priority:   prio,
		Confidence: eval.Confidence,
		Reason:     eval.Level,
		Meta:       meta,
	}, nil
}

// BuyPoints 返回四类买点对应的权重映射。（BuyPoints returns weight mappings for the four buy-point categories.）
// 用于仓位管理和分段建仓决策。（Used for position sizing and staged entry decisions.）
func (d *DragonStrategy) BuyPoints(cfg config.DragonConfig) map[string]float64 {
	return map[string]float64{
		"P1_first_to_second": cfg.F1SealWeight + cfg.F2ResonanceWeight, // 首封→二封（First seal → second seal）
		"P2_divergence":      cfg.F3PremiumWeight,                      // 分歧转一致（Divergence → convergence）
		"P3_weak_to_strong":  cfg.F4RsWeight,                           // 弱转强（Weak to strong）
		"P4_pullback":        cfg.PullbackMaxPct,                       // 回调低吸最大幅度（Max pullback dip-buying range）
	}
}
