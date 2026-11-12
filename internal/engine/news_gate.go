// news_gate.go §NEWS-GATE（2026-11-12，owner 六轮裁决 R1-R12，docs/PLAN_D1_SIGNED_NEWS_GATE_20261112.md）
// D1 带符号化后的资讯闸：
//  1. 一票否决（R9/R2）：D1<0（明确利空）或板块级强利空映射命中（R4）的个股，**所有战法**的
//     买入信号在信号层直接拦截——不入 store、不进确认窗、不下单；改发「利空拦截」提醒（重提醒），
//     持仓票同时给清仓级卖点（卖点评估链已有，D1 负分即清仓级因子）。
//  2. 通用系数（R5/R1）：存留买入信号 Confidence ×(1+boost_good×D1/40)，资讯真正进交易决策。
//  3. 当日快照落盘（R10）：news_gate_today.json（负分个股+板块负分+正分个股），是烘焙链
//     risk_list_dynamic / hot_pool / force_sell_boost 的唯一真源（R12 取代旧 extract_bearish/bullish）。
//  4. 盘中反转监测（R11）：持仓票/持仓板块的 D1 跨阈值翻转 → 防抖确认（3 轮）→ 触发重烘+重启
//     （每日上限 8 次、冷却 30 分钟，owner §6 裁决定稿）。
//
// English: signed-D1 news gate — hard buy veto on D1<0 / sector-bearish hits, universal confidence
// coefficient for kept buys, daily snapshot JSON as the single bake source, and intraday reversal
// detection with debounce/limits for the re-bake+restart chain.
package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/sector_agent"
)

// newsGatePositiveMin 正分个股入利好池的门槛（中等影响下限 16，§3.3 烘焙语义）。
// English: minimum positive D1 for a stock to enter the baked bullish pool.
const newsGatePositiveMin = 16.0

// NewsGateSnapshot 当日资讯闸快照：既是脑侧闸的板块 veto 输入，也是烘焙链唯一真源（R10/R12）。
// day-keyed 覆盖写、原子落盘；文件缺失/损坏由消费方保旧（绝不静默清空）。
// English: daily news-gate snapshot — the brain-side sector-veto input AND the single bake source.
type NewsGateSnapshot struct {
	Day                 string                  `json:"day"`                       // 北京交易日 YYYY-MM-DD
	NegativeCodes       map[string]string       `json:"negative_codes"`            // code→原因（D1<0 明确利空）
	SectorNegativeCodes map[string]string       `json:"sector_negative_codes"`     // code→原因（板块级强利空映射，R4）
	PositiveCodes       map[string]float64      `json:"positive_codes"`            // code→D1（正分个股，利好池烘焙源）
	ReversalEvents      []NewsGateReversalEvent `json:"reversal_events,omitempty"` // 已确认盘中反转（R11，烘焙链 sha 第四入）
	UpdatedAt           time.Time               `json:"updated_at"`                // 刷新时刻
}

// newsGateReversalLimits 盘中反转触发的限额（owner §6 裁决定稿）。
const (
	newsGateDebounceStrikes = 3                // 防抖：连续 3 轮同向确认
	newsGateRestartCap      = 8                // 每日盘中触发上限
	newsGateCooldown        = 30 * time.Minute // 冷却期
	newsGateReversalAbs     = 16.0             // 跨阈值翻转幅度（中等影响下限对称位）
)

// NewsGateReversalEvent 一次已确认的盘中反转（R11）：随快照落盘，gz 烘焙链以其为
// sha 第四入与 SSH 兜底触发源；id 幂等（day+kind+codes），同类反转不重复触发。
type NewsGateReversalEvent struct {
	ID      string            `json:"id"`                // 幂等键 day:kind:codes
	Kind    string            `json:"kind"`              // "bearish"（引导卖出）/ "bullish"（引导买入）
	Codes   map[string]string `json:"codes"`             // code→原因
	Skipped string            `json:"skipped,omitempty"` // 非空=因限额/冷却未触发重烘的原因（留痕）
	At      time.Time         `json:"at"`
}

// newsGateCfg 读取资讯闸旋钮（cfgMgr 缺席=测试装配 → 返回默认值结构，闸仍生效）。
func (e *Engine) newsGateCfg() *config.D1Config {
	if e.cfgMgr != nil {
		if d := e.cfgMgr.GetD1Config(); d != nil {
			return d.Normalized()
		}
	}
	return (&config.D1Config{}).Normalized()
}

// buildNewsGateSnapshot 由本轮 D1 评分 + 利空板块验证结果 + 已归因事件构建当日快照。
// 板块映射（R4）：利空事件 Sectors 与 verifiedBear 板块名做归一化匹配（精确或互为包含），
// 命中板块的可操作成分股全部记入 SectorNegativeCodes；匹配失败 **fail-open**（owner §6 裁决：
// 只留痕日志不拦截，防整市场误杀）。
func (e *Engine) buildNewsGateSnapshot(d1Scores map[string]combat_agent.D1Score, verifiedBear []sector_agent.VerifiedSector, events []newsagent.NewsEvent, cfg *config.D1Config) *NewsGateSnapshot {
	snap := &NewsGateSnapshot{
		Day:                 data.TradingDayDate(time.Now()),
		NegativeCodes:       map[string]string{},
		SectorNegativeCodes: map[string]string{},
		PositiveCodes:       map[string]float64{},
		UpdatedAt:           time.Now(),
	}
	// 1) 个股级：D1<0 = 明确利空（RetryPending=LLM 失败，绝不当作利空）。
	for code, d := range d1Scores {
		if d.RetryPending {
			continue
		}
		if d.Score < 0 {
			snap.NegativeCodes[code] = fmt.Sprintf("D1=%.0f %s", d.Score, d.Reason)
		} else if d.Score >= newsGatePositiveMin {
			snap.PositiveCodes[code] = d.Score
		}
	}
	// 2) 板块级（R4）：利空事件板块 → verifiedBear 成分股。
	if cfg.NewsGateSectorBlock != nil && *cfg.NewsGateSectorBlock {
		sectorHit := map[string]bool{}
		for _, ev := range events {
			if !strings.Contains(ev.Direction, "利空") || ev.Score > cfg.NewsGateSectorScoreMin {
				// 非明确利空或强度未达阈值的事件不参与板块否决，直接跳过。
				continue
			}
			for _, sec := range ev.Sectors {
				sectorHit[normSectorName(sec)] = true
			}
		}
		for _, vs := range verifiedBear {
			hit := false
			for name := range sectorHit {
				if sectorNameMatch(vs.Name, name) {
					hit = true
					break
				}
			}
			if hit {
				for _, code := range vs.Stocks {
					if _, ok := snap.SectorNegativeCodes[code]; !ok {
						snap.SectorNegativeCodes[code] = "板块强利空:" + vs.Name
					}
				}
			}
		}
		// 匹配失败的利空板块留痕（fail-open 观测位）：找得到事件板块但 verifiedBear 里没有可匹配板块。
		for name := range sectorHit {
			matched := false
			for _, vs := range verifiedBear {
				if sectorNameMatch(vs.Name, name) {
					matched = true
					break
				}
			}
			if !matched {
				log.Printf("[news_gate] 板块利空未匹配到验证板块（fail-open 不拦截）: %s", name)
			}
		}
	}
	return snap
}

// normSectorName 板块名归一化：去空白，供精确/包含匹配。
func normSectorName(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "")
}

// sectorNameMatch 板块名匹配：归一化后相等或互为包含（LLM 板块名与验证板块名口径不一致的兜底）。
func sectorNameMatch(a, b string) bool {
	na, nb := normSectorName(a), normSectorName(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb || strings.Contains(na, nb) || strings.Contains(nb, na)
}

// refreshNewsGate 每轮主循环 D1 评分后调用：刷新快照 + 反转监测推进 + 原子落盘。
// 返回本次快照供闸消费。
func (e *Engine) refreshNewsGate(d1Scores map[string]combat_agent.D1Score, verifiedBear []sector_agent.VerifiedSector, events []newsagent.NewsEvent) *NewsGateSnapshot {
	cfg := e.newsGateCfg()
	snap := e.buildNewsGateSnapshot(d1Scores, verifiedBear, events, cfg)
	e.newsGateMu.Lock()
	e.newsGate = snap
	e.newsGateMu.Unlock()
	// 反转监测（R11）：对比上一轮 D1 全量分，检测持仓票的跨阈值翻转。
	e.detectNewsGateReversal(snap, d1Scores, cfg)
	// 快照注入已确认的反转事件（烘焙链 sha 第四入 + SSH 兜底触发源）。
	e.newsGateMu.Lock()
	if len(e.newsGateEvents) > 0 {
		snap.ReversalEvents = append([]NewsGateReversalEvent{}, e.newsGateEvents...)
	}
	e.newsGateMu.Unlock()
	// 落盘（R10 烘焙真源）：dataDir 空=纯内存（测试装配），跳过文件。
	if e.newsGatePath != "" {
		if raw, err := json.Marshal(snap); err == nil {
			ensureParentDir(e.newsGatePath)
			mustAtomicWrite("news_gate_today", e.newsGatePath, raw)
		} else {
			log.Printf("[news_gate] 快照序列化失败: %v", err)
		}
	}
	return snap
}

// detectNewsGateReversal R11 盘中反转监测：只盯**持仓票**——
//   - D1 从 ≥−16 跨到 ≤−16（突发利空）→ bearish 翻转 → 确认后引导卖出（重烘负分+重启）；
//   - D1 从 ≤+16 跨到 ≥+16（突发利好）→ bullish 翻转 → 确认后引导买入（重烘正分+重启）；
//   - 持仓票新进板块负分映射（R4 板块级利空）同样按 bearish 计击。
//
// 防抖 3 轮同向确认；确认后受每日上限 8 次 + 冷却 30 分钟约束（超限只落 skipped 留痕，
// 不触发重烘重启）。RetryPending（LLM 失败）绝不计入。事件幂等：同 kind+codes 只记一次。
func (e *Engine) detectNewsGateReversal(snap *NewsGateSnapshot, d1Scores map[string]combat_agent.D1Score, cfg *config.D1Config) {
	e.newsGateMu.Lock()
	defer e.newsGateMu.Unlock()
	if e.cfgMgr == nil {
		return // 测试装配不推进反转状态
	}
	// 换日重置（防抖计数/限额/事件链全部按日隔离）。
	if e.newsGateDay != snap.Day {
		e.newsGateDay = snap.Day
		e.newsGateStrikes = map[string]int{}
		e.newsGateStrikeDir = map[string]string{}
		e.newsGateRestarts = 0
		e.newsGateLastRestart = time.Time{}
		e.newsGateEvents = nil
	}
	prev := e.newsGatePrevD1
	e.newsGatePrevD1 = make(map[string]float64, len(d1Scores))
	for code, d := range d1Scores {
		if !d.RetryPending {
			e.newsGatePrevD1[code] = d.Score
		}
	}
	if e.rpt == nil {
		return
	}
	for _, code := range e.rpt.HeldPositionCodesFor(e.userID) {
		d, ok := d1Scores[code]
		if !ok || d.RetryPending {
			continue
		}
		prevScore := prev[code] // 缺省 0（引擎首轮回中性，真实跨阈值才算翻转）
		kind := ""
		reason := ""
		// 跨阈值翻转判定（突发语义）：本轮已越过阈值，且上一轮未越（或防抖已在同方向计数中——
		// 连续多轮维持同一突发状态必须能继续计击，否则第 2 轮起 prev 已越线会被误判"非翻转"）。
		bearNow := d.Score <= -newsGateReversalAbs
		bullNow := d.Score >= newsGateReversalAbs
		if bearNow && (prevScore > -newsGateReversalAbs || (e.newsGateStrikeDir[code] == "bearish" && e.newsGateStrikes[code] > 0)) {
			kind = "bearish"
			reason = fmt.Sprintf("D1 %.0f→%.0f %s", prevScore, d.Score, d.Reason)
		} else if bullNow && (prevScore < newsGateReversalAbs || (e.newsGateStrikeDir[code] == "bullish" && e.newsGateStrikes[code] > 0)) {
			kind = "bullish"
			reason = fmt.Sprintf("D1 %.0f→%.0f %s", prevScore, d.Score, d.Reason)
		}
		// 板块级利空新命中（R11a「持仓个股映射板块突发利空」）：按 bearish 计击。
		if kind == "" {
			_, inSector := snap.SectorNegativeCodes[code]
			_, wasSector := e.newsGatePrevSectorNeg[code]
			if inSector && !wasSector {
				kind = "bearish"
				reason = "板块利空命中:" + snap.SectorNegativeCodes[code]
			}
		}
		if kind == "" {
			delete(e.newsGateStrikes, code)
			delete(e.newsGateStrikeDir, code)
			continue
		}
		if e.newsGateStrikeDir[code] != kind {
			e.newsGateStrikeDir[code] = kind
			e.newsGateStrikes[code] = 1
			continue
		}
		e.newsGateStrikes[code]++
		if e.newsGateStrikes[code] < newsGateDebounceStrikes {
			continue
		}
		// 防抖确认 → 限额/冷却裁定 → 记事件（幂等）。
		e.newsGateStrikes[code] = 0
		codes := map[string]string{code: reason}
		id := fmt.Sprintf("%s:%s:%s", snap.Day, kind, code)
		dup := false
		for _, ev := range e.newsGateEvents {
			if ev.ID == id {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		ev := NewsGateReversalEvent{ID: id, Kind: kind, Codes: codes, At: time.Now()}
		if e.newsGateRestarts >= newsGateRestartCap {
			ev.Skipped = fmt.Sprintf("当日触发已达上限 %d 次，只更新文件不重启", newsGateRestartCap)
		} else if !e.newsGateLastRestart.IsZero() && time.Since(e.newsGateLastRestart) < newsGateCooldown {
			ev.Skipped = fmt.Sprintf("冷却期内（距上次触发 %v < %v）", time.Since(e.newsGateLastRestart).Round(time.Second), newsGateCooldown)
		} else {
			e.newsGateRestarts++
			e.newsGateLastRestart = time.Now()
		}
		e.newsGateEvents = append(e.newsGateEvents, ev)
		log.Printf("[news_gate] 盘中反转确认 %s %s | %s%s", kind, code, reason, skipNote(ev.Skipped))
	}
	// 记录本轮板块负分映射，供下一轮「新进板块」判定。
	e.newsGatePrevSectorNeg = make(map[string]string, len(snap.SectorNegativeCodes))
	for c, r := range snap.SectorNegativeCodes {
		e.newsGatePrevSectorNeg[c] = r
	}
}

// skipNote 限额/冷却留痕后缀。
func skipNote(skipped string) string {
	if skipped == "" {
		return " → 已触发重烘请求"
	}
	return " → " + skipped
}

// applyNewsGate §NEWS-GATE 信号层收口（在两条信号流的汇聚点各调一次）：
// 返回（存留信号, 拦截提醒）。仅作用于 Action=buy 且 Direction=做多 的信号；
// watch/提醒/做空信号不受影响。
func (e *Engine) applyNewsGate(sigs []combat_agent.Signal, d1Scores map[string]combat_agent.D1Score) ([]combat_agent.Signal, []combat_agent.Signal) {
	cfg := e.newsGateCfg()
	if cfg.NewsGateDisable != nil && *cfg.NewsGateDisable {
		return sigs, nil
	}
	blockBelow := 0.0
	if cfg.NewsGateBlockBelow != nil {
		blockBelow = *cfg.NewsGateBlockBelow
	}
	sectorBlock := cfg.NewsGateSectorBlock != nil && *cfg.NewsGateSectorBlock
	boost := cfg.NewsGateBoostGood

	e.newsGateMu.RLock()
	var sectorNeg map[string]string
	if e.newsGate != nil {
		sectorNeg = e.newsGate.SectorNegativeCodes
	}
	e.newsGateMu.RUnlock()

	kept := make([]combat_agent.Signal, 0, len(sigs))
	var alerts []combat_agent.Signal
	seenVeto := map[string]bool{}
	for _, sig := range sigs {
		if sig.Action != "buy" || sig.Direction != "做多" {
			kept = append(kept, sig)
			continue
		}
		d1, hasD1 := d1Scores[sig.Code]
		vetoReason := ""
		sectorVeto := false
		if hasD1 && !d1.RetryPending && d1.Score < blockBelow {
			vetoReason = fmt.Sprintf("D1=%.0f %s", d1.Score, d1.Reason)
		} else if sectorBlock {
			if r, ok := sectorNeg[sig.Code]; ok {
				vetoReason, sectorVeto = r, true
			}
		}
		if vetoReason != "" {
			if !seenVeto[sig.Code] {
				seenVeto[sig.Code] = true
				alerts = append(alerts, e.newsGateVetoAlert(sig, vetoReason, sectorVeto))
				log.Printf("[news_gate] 利空拦截买入 %s(%s) %s 板块级=%v | %s",
					sig.Code, sig.Name, sig.Strategy, sectorVeto, vetoReason)
			}
			continue
		}
		// 通用系数（R5）：D1>0 → Confidence ×(1+boost×D1/40)，封顶 1.0。
		if hasD1 && !d1.RetryPending && d1.Score > 0 && boost > 0 {
			factor := 1 + boost*d1.Score/40
			if factor > 1+boost {
				factor = 1 + boost
			}
			sig.Confidence = sig.Confidence * factor
			if sig.Confidence > 1 {
				sig.Confidence = 1
			}
		}
		kept = append(kept, sig)
	}
	return kept, alerts
}

// newsGateVetoAlert 构造「利空拦截」提醒信号（重提醒；持仓票给清仓级卖点）。
// 形态对齐卖点评估信号（Direction=提醒 + AlertType/SellLevel），保证消息中心/前端可渲染。
func (e *Engine) newsGateVetoAlert(sig combat_agent.Signal, reason string, sector bool) combat_agent.Signal {
	held := false
	if e.rpt != nil {
		for _, c := range e.rpt.HeldPositionCodesFor(e.userID) {
			if c == sig.Code {
				held = true
				break
			}
		}
	}
	prefix := "个股利空拦截"
	if sector {
		prefix = "板块利空拦截"
	}
	alert := sig // 拷贝基底（行情/板块/名称沿用）
	alert.ID = fmt.Sprintf("newsgate:%s:%s:%s", data.TradingDayDate(time.Now()), sig.Code, sig.Strategy)
	alert.Strategy = "资讯闸"
	alert.StrategyType = "news_gate"
	alert.Direction = "提醒"
	alert.Action = "卖出"
	alert.Reason = fmt.Sprintf("🚫%s(拒绝买入): %s", prefix, reason)
	if held {
		// 持仓票（R10）：升格为清仓级卖点提醒，引导卖出。
		alert.AlertType = "清仓"
		alert.SellLevel = "清仓"
		alert.Reason = fmt.Sprintf("🚫%s(持仓利空，建议卖出): %s", prefix, reason)
	} else {
		alert.AlertType = "利空拦截"
	}
	alert.GeneratedAt = time.Now()
	return alert
}

// newsGateSectorNegative 返回当前快照的板块负分映射（烘焙链 force_sell_boost / 载体双保险读取）。
func (e *Engine) newsGateSectorNegative() map[string]string {
	e.newsGateMu.RLock()
	defer e.newsGateMu.RUnlock()
	if e.newsGate == nil {
		return nil
	}
	return e.newsGate.SectorNegativeCodes
}

// newsGateNegative 返回当前快照的个股负分映射。
func (e *Engine) newsGateNegative() map[string]string {
	e.newsGateMu.RLock()
	defer e.newsGateMu.RUnlock()
	if e.newsGate == nil {
		return nil
	}
	return e.newsGate.NegativeCodes
}

// newsGatePositive 返回当前快照的正分映射（利好池烘焙源）。
func (e *Engine) newsGatePositive() map[string]float64 {
	e.newsGateMu.RLock()
	defer e.newsGateMu.RUnlock()
	if e.newsGate == nil {
		return nil
	}
	return e.newsGate.PositiveCodes
}
