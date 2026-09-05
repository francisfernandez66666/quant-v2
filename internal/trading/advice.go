// advice.go — 持仓处理分析层（AUTO_TRADING_PLAN M1）：对实盘持仓（real_positions）生成处理建议。
// 复用卖出侧决策函数（CheckPositionsExits / CheckPositionAlerts / AssessSellSide / 情绪退潮 /
// 利空归因），把 result 映射为统一 PositionAdvice；并新增加仓与格局判定规则。
// 与纸面账本完全独立：输入为真实持仓（券商回报），不触碰 report.Report。
// English: position-advice layer (AUTO_TRADING_PLAN M1) — produces handling advice for the real book
// (real_positions). It reuses the sell-side decision functions (CheckPositionsExits / CheckPositionAlerts /
// AssessSellSide / emotion-retreat / bearish-attribution), mapping results onto a unified PositionAdvice,
// and adds new add-position and hold(格局) rules. Fully independent of the paper book: input is the real
// holdings (broker reports); report.Report is untouched.
package trading

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategy_engine"
)

// PositionAdvice 单只实盘持仓的处理建议。
// English: PositionAdvice is a single live position's handling advice.
type PositionAdvice struct {
	Code         string    `json:"code"`          // 股票代码（纯数字，无后缀）
	TsCode       string    `json:"ts_code"`       // 股票代码（带后缀，如 600000.SH）
	Name         string    `json:"name"`          // 股票名称
	Qty          int       `json:"qty"`           // 当前持仓股数
	Action       string    `json:"action"`        // 加仓/减仓/止盈/止损/格局/持有
	Level        string    `json:"level"`         // 高/中/低（建议强度）
	Reason       string    `json:"reason"`        // 建议理由
	RefPrice     float64   `json:"ref_price"`     // 参考价（现价）
	Amount       float64   `json:"amount"`        // 当前市值（元）
	ProfitPct    float64   `json:"profit_pct"`    // 现价相对成本盈亏（%）
	DrawdownPct  float64   `json:"drawdown_pct"`  // 现价相对持仓最高价回撤（%，负值=已回撤）
	Strategy     string    `json:"strategy"`      // 触发战法
	SignalActive bool      `json:"signal_active"` // 该股当前是否有信号（加仓前置条件）
	GeneratedAt  time.Time `json:"generated_at"`  // 生成时间
}

// AdviceInput 持仓分析入参（由引擎每轮组装传入）。
// English: AdviceInput aggregates the inputs for one advice round (assembled by the engine per cycle).
type AdviceInput struct {
	Agent        *combat_agent.Agent                         // 战法代理（卖出侧决策函数复用）
	MarketAPI    *data.MarketAPI                             // 行情 API（实时报价）
	Positions    []store.RealPosition                        // 实盘持仓（real_positions）
	Quotes       map[string]*data.StockInfo                  // 实时行情（纯数字 code → 快照）
	DayKLines    map[string][]data.KLine                     // 日K（纯数字 code → 日K）
	Scores       map[string]combat_agent.StockScores         // 8a/8b 打分（SignalActive 加仓条件）
	MD           map[string]*strategy_engine.StockMarketData // 行情数据（卖点评估）
	D1Scores     map[string]combat_agent.D1Score             // D1 评分（卖点评估）
	ShortEnabled bool                                        // 是否做空模式（卖点评估范围）
	EmotionPhase string                                      // 情绪阶段（退潮/背离 → 减仓）
	BearReasons  map[string]string                           // 利空归因（code → 原因）
	Cfg          config.QMTConfig                            // QMT 配置（加仓/格局阈值）
}

// Advise 生成实盘持仓处理建议：卖出侧（复用）→ 加仓 → 格局，按 action 排序输出。
// 返回 nil 表示无持仓或不可分析。Agent 可空：空则跳过卖出侧复用，仅走加仓/格局判定。
// English: Advise produces handling advice for the live book: sell-side (reused) → add-position → hold.
// Returns nil when there are no positions. Agent may be nil: sell-side reuse is then skipped and only
// the add-position / hold rules run.
func Advise(in AdviceInput) []PositionAdvice {
	if len(in.Positions) == 0 {
		return nil
	}
	now := time.Now()

	// 构造只读 Report 视图复用卖出侧函数（NewFromLogs 不持久化）
	view := report.NewFromLogs(execLogsFromReal(in.Positions))

	var advices []PositionAdvice
	advByCode := make(map[string]*PositionAdvice)

	// 卖出侧复用仅在 Agent 可用时执行
	if in.Agent != nil {
		// 1. 卖出侧：战法退出引擎（移动止盈/硬止损/尾盘强平/超期）
		for _, sig := range in.Agent.CheckPositionsExits(view, in.Quotes, in.DayKLines, now) {
			mergeAdvice(advByCode, fromSignal(sig, in, now, ""))
		}
		// 2. 卖出侧：通用止盈/止损/跌幅提醒（§R4-6：行情走调用方注入的快照，缺失兜底单查）
		for _, sig := range in.Agent.CheckPositionAlerts(view, in.MarketAPI, in.Quotes, in.Scores) {
			mergeAdvice(advByCode, fromSignal(sig, in, now, ""))
		}
		// 3. 卖出侧：卖点评估（利空D1/破MA/放量派发/动量衰竭）
		held := heldCodes(in.Positions)
		if len(held) > 0 {
			for _, sig := range in.Agent.AssessSellSide(held, in.MD, in.D1Scores, in.Scores, in.ShortEnabled) {
				mergeAdvice(advByCode, fromSignal(sig, in, now, ""))
			}
		}
		// 4. 卖出侧：情绪退潮/背离 → 整体减仓
		for _, sig := range in.Agent.EmotionRetreatAlerts(view, in.Quotes, in.EmotionPhase, now) {
			mergeAdvice(advByCode, fromSignal(sig, in, now, ""))
		}
		// 5. 卖出侧：利空归因 → 尽快抛掉
		for _, sig := range in.Agent.BearishAttributionAlerts(view, in.Quotes, in.BearReasons, now) {
			mergeAdvice(advByCode, fromSignal(sig, in, now, ""))
		}
	}

	// 6. 加仓/格局：对无卖出建议的持仓逐只判定（已有卖出级建议的不再叠加）。
	for _, p := range in.Positions {
		code := pureCode(p.TsCode)
		if advByCode[code] != nil {
			continue // 已有卖出级建议，不再叠加加仓/格局
		}
		// 无行情则跳过；有行情时先试加仓建议，再加仓不成立再试格局持有建议。
		quote := in.Quotes[code]
		if quote == nil || quote.Price <= 0 {
			continue
		}
		sc := in.Scores[code]
		pa := baseAdvice(p, quote.Price, now)
		if a := addAdvice(p, pa, sc, in.Cfg.Advice, in.Quotes); a != nil {
			advByCode[code] = a
			continue
		}
		if h := holdAdvice(p, pa, sc, in.Cfg.Advice); h != nil {
			advByCode[code] = h
		}
	}

	for _, a := range advByCode {
		advices = append(advices, *a)
	}
	sort.Slice(advices, func(i, j int) bool {
		return advices[i].Action < advices[j].Action
	})
	return advices
}

// execLogsFromReal 把实盘持仓映射为 ExecLog 视图（SignalID 用 ts_code 保持稳定，供 RaiseHighest）。
// 方向固定做多；止盈/止损阈值留 0（CheckPositionAlerts 对 0 阈值跳过止盈/止损，仅跌幅提醒仍生效）。
// English: maps real positions onto an ExecLog view (SignalID stable via ts_code for RaiseHighest).
// Direction is fixed to 做多; TP/SL thresholds stay 0 (CheckPositionAlerts skips TP/SL for 0 but still
// fires daily-drop alerts).
func execLogsFromReal(positions []store.RealPosition) []report.ExecLog {
	logs := make([]report.ExecLog, 0, len(positions))
	for _, p := range positions {
		if p.Qty <= 0 {
			continue
		}
		logs = append(logs, report.ExecLog{
			SignalID:     "real@" + p.TsCode,
			Code:         pureCode(p.TsCode),
			Name:         p.Name,
			Direction:    "做多",
			Strategy:     p.Strategy,
			EntryPrice:   p.CostPrice,
			Quantity:     float64(p.Qty),
			HighestPrice: p.HighestPrice,
			Status:       "持仓中",
		})
	}
	return logs
}

// heldCodes 返回实盘持仓纯数字代码集合。
// （heldCodes returns the pure-digit codes of the live holdings.）
func heldCodes(positions []store.RealPosition) []string {
	out := make([]string, 0, len(positions))
	for _, p := range positions {
		out = append(out, pureCode(p.TsCode))
	}
	return out
}

// pureCode 剥离股票代码的后缀（600000.SH → 600000）。
// English: pureCode strips the exchange suffix (600000.SH → 600000).
func pureCode(tsCode string) string {
	c := strings.TrimSpace(tsCode)
	for _, suf := range []string{".SH", ".SZ", ".BJ"} {
		if strings.HasSuffix(c, suf) {
			return strings.TrimSuffix(c, suf)
		}
	}
	return c
}

// baseAdvice 由持仓 + 现价构造基础建议骨架（含盈亏/回撤/市值）。
// English: baseAdvice builds the advice skeleton from a position + live price (P/L, drawdown, market value).
func baseAdvice(p store.RealPosition, price float64, now time.Time) *PositionAdvice {
	pa := &PositionAdvice{
		Code:        pureCode(p.TsCode),
		TsCode:      p.TsCode,
		Name:        p.Name,
		Qty:         p.Qty,
		Action:      "持有",
		Level:       "低",
		RefPrice:    price,
		Amount:      price * float64(p.Qty),
		Strategy:    p.Strategy,
		GeneratedAt: now,
	}
	if p.CostPrice > 0 {
		pa.ProfitPct = (price - p.CostPrice) / p.CostPrice * 100
	}
	if p.HighestPrice > 0 {
		pa.DrawdownPct = (price - p.HighestPrice) / p.HighestPrice * 100
	}
	return pa
}

// mergeAdvice 以现价/最高价更高的建议为准（同 code 只保留最强一条）。
// English: mergeAdvice keeps only the strongest advice per code (higher level/price wins).
func mergeAdvice(m map[string]*PositionAdvice, a *PositionAdvice) {
	if a == nil {
		return
	}
	cur := m[a.Code]
	if cur == nil {
		m[a.Code] = a
		return
	}
	// 卖出类 > 持有；同类保留先出现的（原因更完整）
	if cur.Action == "持有" && a.Action != "持有" {
		m[a.Code] = a
	}
}

// fromSignal 把卖出侧信号映射为 PositionAdvice（附上现价盈亏/回撤上下文）。
// English: fromSignal maps a sell-side signal onto PositionAdvice, attaching P/L and drawdown context.
func fromSignal(sig combat_agent.Signal, in AdviceInput, now time.Time, _ string) *PositionAdvice {
	var p *store.RealPosition
	for i := range in.Positions {
		if pureCode(in.Positions[i].TsCode) == sig.Code {
			p = &in.Positions[i]
			break
		}
	}
	if p == nil {
		return nil
	}
	price := sig.Price
	if price <= 0 {
		price = p.CostPrice
	}
	pa := baseAdvice(*p, price, now)
	// §P2#25 行情缺失不伪造现价：RefPrice 是自动卖出的挂单价来源（autoExecuteRealSells 用它下单）。
	// 旧实现把成本价顶替成"参考价"，行情缺失时止损级建议会按成本价真实挂单（挂错价）。
	// 改为缺失时 RefPrice=0 —— 自动卖出守卫（a.RefPrice<=0 跳过）拦截，宁可不成交也不挂错价；
	// 展示侧仍用成本价兜底估值（ProfitPct=0，语义为"现价未知"）。
	// English: P2#25 — a missing live quote no longer fakes a reference price. RefPrice feeds the auto-sell
	// order price (autoExecuteRealSells), and the old cost-price fallback would place a real order at cost
	// when no quote existed. Now RefPrice stays 0 so the auto-sell guard skips it — better no order than a
	// wrong-priced one. Display still estimates value at cost (ProfitPct=0, meaning "live price unknown").
	pa.RefPrice = sig.Price
	pa.Reason = sig.Reason
	switch sig.AlertType {
	case "清仓":
		pa.Action, pa.Level = "止盈", "高"
		if pa.ProfitPct <= 0 {
			pa.Action, pa.Level = "止损", "高"
		}
	case "止损":
		pa.Action, pa.Level = "止损", "高"
	case "减仓":
		pa.Action, pa.Level = "减仓", "高"
	case "跌幅提醒":
		pa.Action, pa.Level = "减仓", "中"
		if pa.ProfitPct < 0 {
			pa.Action, pa.Level = "止损", "中"
		}
	default:
		pa.Action, pa.Level = "止盈", "中"
	}
	// 利空归因/抛售类理由 → 直接止损（强度最高）
	if strings.Contains(sig.Reason, "利空") || strings.Contains(sig.Reason, "抛售") {
		pa.Action, pa.Level = "止损", "高"
	}
	return pa
}

// addAdvice 加仓判定：信号仍活跃 + 持仓数未达上限 + 现价相对最高价回撤在阈值内。
// 返回 nil 表示不满足加仓条件。
// English: addAdvice — add-position rule: signal still active, holdings below max_positions, and the
// price's drawdown from the stage high within the configured threshold. Nil means no add advice.
func addAdvice(p store.RealPosition, pa *PositionAdvice, sc combat_agent.StockScores, cfg config.QMTAdviceConfig, quotes map[string]*data.StockInfo) *PositionAdvice {
	if cfg.AddSignalActive && !sc.SignalActive {
		return nil
	}
	if pa.ProfitPct < 0 {
		return nil // 已亏损不加仓
	}
	// 回撤阈值：add_reopen_drawdown_pct 为负值（如 -5 = 回撤不超 5%）。pa.DrawdownPct 也为负。
	if cfg.AddReopenDrawdownPct != 0 && pa.DrawdownPct < cfg.AddReopenDrawdownPct {
		return nil // 回撤已超阈值，不宜加仓
	}
	if pa.DrawdownPct < -0.001 && math.Abs(cfg.AddReopenDrawdownPct) < 0.0001 {
		return nil // 未配置阈值时：回撤即不加仓
	}
	pa.Action = "加仓"
	pa.Level = "高"
	pa.Reason = "信号活跃且回撤可控，建议加仓（现价相对阶段高点回撤" + fmt.Sprintf("%.2f%%", pa.DrawdownPct) + "）"
	pa.SignalActive = true
	return pa
}

// holdAdvice 格局判定：无卖出建议 + 盈利达阈值 + 未破关键均线（MA5）→ 建议格局（继续持有）。
// English: holdAdvice — hold(格局) rule: no sell advice, profit meets the threshold, and price stays
// above the key MA5 → advise holding.
func holdAdvice(p store.RealPosition, pa *PositionAdvice, sc combat_agent.StockScores, cfg config.QMTAdviceConfig) *PositionAdvice {
	if pa.ProfitPct < cfg.HoldMinProfitPct {
		return nil
	}
	if pa.ProfitPct <= 0 {
		return nil
	}
	if pa.DrawdownPct < -12 {
		return nil // 回撤过大，不再建议格局
	}
	pa.Action = "格局"
	pa.Level = "中"
	pa.Reason = "无卖出信号且盈利" + fmt.Sprintf("%.2f%%", pa.ProfitPct) + "、趋势完好，建议格局（继续持有）"
	return pa
}
