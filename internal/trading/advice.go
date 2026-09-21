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
	// Source 建议来源：""=常规卖出侧/加仓/格局；"discipline"=统一纪律裁决引擎（probeDiscipline）。
	// 实盘自动卖出据此区分——纪律的止盈/减仓才自动执行，战法自带止盈止损（降级为通知）不执行。
	// English: advice source — "" = regular sell-side / add / hold; "discipline" = the unified discipline
	// engine (probeDiscipline). The live auto-sell uses it to execute only discipline TP/trims, while
	// strategy-native TP/SL (downgraded to notifications) never auto-executes.
	Source string `json:"source,omitempty"`
}

// UnifiedSellView 统一卖出裁决层（signalctl.JudgeSell）对单持仓的展示投影输入。
// P2 切闸后 Advise 不再自行拼装卖出结论，改由引擎把裁决层裁定投影为本结构注入，
// 展示卡片的 Action/Level/Reason 只可能来自裁决三态（§REFACTOR_UNIFIED_SELL P2）。
// English: a per-position projection of the unified sell adjudication (signalctl.JudgeSell), injected
// into Advise once P2 is on — the display card can only reflect the adjudicator's verdict.
type UnifiedSellView struct {
	TsCode   string  // 带后缀代码（与 RealPosition.TsCode 同键）
	Action   string  // 止损/止盈/减仓/持有（持有=观察/预警卡）
	Level    string  // 高/中/低
	Reason   string  // 卡片理由（含裁决留痕文本）
	RefPrice float64 // 参考价（裁决轮现价；≤0 时自动卖出守卫自然跳过）
	Source   string  // UnifiedSellSourceAction=可执行处置 / UnifiedSellSourceWatch=观察·预警（永不执行）
}

// 统一卖出投影的来源常量（PositionAdvice.Source 复用同一字段值域）。
const (
	// UnifiedSellSourceAction 裁决层 pass 处置的投影——实盘自动卖出唯一放行来源。
	UnifiedSellSourceAction = "unified"
	// UnifiedSellSourceWatch 观察中/利空预警投影——只展示，永不进执行。
	UnifiedSellSourceWatch = "unified-watch"
)

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
	// DiscTracker 统一纪律裁决引擎（探针+扳机）状态机（§统一纪律 B）。nil = 未启用纪律裁决
	//（旧行为：走 CheckPositionAlerts 即时止盈止损）。由 engine 按账号注入。
	// English: the unified-discipline tracker (probe+trigger; §unified-discipline B). nil = discipline
	// adjudication off (legacy instant TP/SL via CheckPositionAlerts). Injected by the engine per account.
	DiscTracker *DisciplineTracker
	// SellableQty §PROD-T1（2026-09-18 生产实录）：各持仓"今日可卖数量"（键=ts_code 原样，
	// 由 engine 用 store.BuyableQtyForUserSell=持仓−当日买入成交 装配）。A 股 T+1：当日买入份额
	// 锁定不可卖，旧建议层不感知，对当日新建仓位照常推送"止盈/止损，请手动处理"——用户照做却被
	// 柜台 T+1 拒绝，提醒即误导。可卖量 ≤0 的代码整体跳过卖出侧（退出/纪律/卖点/退潮/利空），
	// 加仓与格局持有不受限（买入无 T+1 约束）。缺 key=可卖量未知（未注入/测试），按不锁定处理，
	// 保持旧行为兜底。
	// English: §PROD-T1 — per-code sellable qty (held minus today's bought fills). Positions with zero
	// sellable skip all sell-side advice (T+1 locked — a sell reminder the user cannot act on is
	// misleading), while add/hold rules still run. Missing key = unknown = not locked.
	SellableQty map[string]int
	// SellUnifiedOn §REFACTOR_UNIFIED_SELL P2（sell_unified_mode=on）：统一卖出裁决层已切闸，
	// 五路卖出拼装（战法退出/纪律探针/卖点评估/情绪退潮/利空归因）全部跳过，卖出结论只允许来自
	// SellProjection（裁决层三态的展示投影）。false=影子/关闭期，旧五路照常拼装（零行为变化）。
	// English: P2 gate on — the legacy five-way sell assembly is skipped and sell cards come only from
	// the adjudication projection below. False = shadow/off: legacy path runs unchanged.
	SellUnifiedOn bool
	// SellProjection 裁决层投影卡片（仅 SellUnifiedOn=true 时生效），在加仓/格局判定之前注入，
	// 保证同一持仓"裁决优先、不再叠加加仓建议"的既有合并语义不变。
	SellProjection []UnifiedSellView
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

	// §PROD-T1（2026-09-18 生产实录）卖出侧 T+1 闸：先剔除当日买入锁定（可卖量≤0）的持仓，
	// 剩余才进入退出/纪律/卖点/退潮/利空五路卖出侧评估——被锁的仓位今天根本卖不动，
	// 任何"止盈/止损请手动处理"都是误导（实录：603468 当日买入 14:13 即推超期止盈）。
	// 加仓/格局走完整持仓列表（in.Positions）：买入方向不受 T+1 限制。
	// English: §PROD-T1 — drop T+1-locked positions from the sell-side (they cannot be sold today;
	// a sell reminder is misleading); add/hold rules still see the full book.
	sellable := sellablePositions(in)
	inSell := in
	inSell.Positions = sellable
	// 构造只读 Report 视图复用卖出侧函数（NewFromLogs 不持久化）
	view := report.NewFromLogs(execLogsFromReal(sellable, in.Cfg.Discipline))

	var advices []PositionAdvice
	advByCode := make(map[string]*PositionAdvice)

	// 卖出侧复用仅在 Agent 可用时执行。§REFACTOR_UNIFIED_SELL P2 切闸后（SellUnifiedOn）五路拼装
	// 整体跳过——卖出结论的唯一来源改为下方 SellProjection（裁决层投影），探测器保留为证据生产者
	//（影子期喂裁决层），不再直达展示。
	// English: legacy five-way sell assembly; skipped entirely once the unified adjudicator is on (P2).
	// Sell conclusions then come only from the injected adjudication projection below.
	if in.Agent != nil && !in.SellUnifiedOn {
		// 1. 卖出侧：战法退出引擎（移动止盈/硬止损/尾盘强平/超期）
		for _, sig := range in.Agent.CheckPositionsExits(view, in.Quotes, in.DayKLines, now) {
			mergeAdvice(advByCode, fromSignal(sig, in, now, ""))
		}
		// 2. 卖出侧：统一止盈/止损/移动止盈/深破裁决（§统一纪律·探针+扳机）。
		// 替换旧的 CheckPositionAlerts 即时止盈止损：判定线 −6/+15/最高价−6/−12 全部来自
		// DisciplineConfig（实盘与模拟盘同口径），命中后固定观察窗，窗内无同向信号才离场，
		// 过滤盘中插针；战法自带止盈止损降级为触发通知。DiscTracker 为 nil（未注入）时跳过。
		// English: unified TP/SL/trail/deep adjudication (probe+trigger) replaces the legacy instant
		// CheckPositionAlerts: all lines (−6/+15/high−6/−12) come from DisciplineConfig (same as paper),
		// with a fixed confirm window — no same-direction signal by settlement → exit, filtering pin-bars;
		// strategy-native TP/SL degrade to notifications. Skipped when DiscTracker is nil.
		if in.DiscTracker != nil {
			for _, a := range in.DiscTracker.ProbeAll(inSell, in.Cfg.Discipline) {
				mergeAdvice(advByCode, &a)
			}
		}
		// 3. 卖出侧：卖点评估（利空D1/破MA/放量派发/动量衰竭）
		held := heldCodes(sellable)
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

	// 5'. §REFACTOR_UNIFIED_SELL P2：统一裁决层投影注入（切闸后卖出卡片唯一来源）。
	// 卡片由引擎 projectSellView 从 SellVerdict 三态翻译而来（处置=止损/止盈/减仓 Source=unified；
	// 观察/利空预警=持有级 Source=unified-watch），这里只做持仓上下文补全（数量/盈亏/市值），
	// 不改变任何结论字段——展示与决策自此解耦，误报卡不可能绕过裁决层出现。
	// English: P2 — inject the adjudication projection (the only sell-card source once the gate is on);
	// enrich with position context only, never mutating verdict fields.
	if in.SellUnifiedOn {
		posByTs := make(map[string]*store.RealPosition, len(in.Positions))
		for i := range in.Positions {
			posByTs[in.Positions[i].TsCode] = &in.Positions[i]
		}
		for _, v := range in.SellProjection {
			p := posByTs[v.TsCode]
			if p == nil {
				continue // 已不在持仓（对账延迟/已平）：投影作废，不留幽灵卡
			}
			pa := baseAdvice(*p, v.RefPrice, now)
			pa.Action = v.Action
			pa.Level = v.Level
			pa.Reason = v.Reason
			pa.Source = v.Source
			advByCode[pa.Code] = pa
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

	// 前面各步已保证一只票只留一条建议（卖出级优先，其次加仓、格局持有），
	// 这里把按代码索引的 map 摊平成切片，再按 Action 字面排序，只为列表顺序稳定可预期，
	// 排序本身不代表建议的紧迫程度。
	for _, a := range advByCode {
		advices = append(advices, *a)
	}
	sort.Slice(advices, func(i, j int) bool {
		return advices[i].Action < advices[j].Action
	})
	return advices
}

// execLogsFromReal 把实盘持仓映射为 ExecLog 视图（SignalID 用 ts_code 保持稳定，供 RaiseHighest）。
// 方向固定做多；止盈/止损阈值取统一纪律 DisciplineConfig（默认止盈+15/止损-6），使实盘走
// CheckPositionAlerts 与模拟盘同口径严格执行（此前留 0 → 0 阈值跳过止盈止损，仅跌幅提醒生效）。
// English: maps real positions onto an ExecLog view (SignalID stable via ts_code for RaiseHighest).
// Direction is fixed to 做多; TP/SL thresholds come from the unified DisciplineConfig (default +15 / −6)
// so the live CheckPositionAlerts enforces the same lines as paper (previously left 0 → TP/SL skipped).
func execLogsFromReal(positions []store.RealPosition, disc config.DisciplineConfig) []report.ExecLog {
	sl := disc.StopLossPct
	if sl <= 0 {
		sl = 6 // 与 DefaultDisciplineConfig 同口径兜底（旧配置未设）
	}
	tp := disc.TakeProfitPct
	if tp <= 0 {
		tp = 15
	}
	logs := make([]report.ExecLog, 0, len(positions))
	for _, p := range positions {
		if p.Qty <= 0 {
			continue
		}
		logs = append(logs, report.ExecLog{
			SignalID:      "real@" + p.TsCode,
			Code:          pureCode(p.TsCode),
			Name:          p.Name,
			Direction:     "做多",
			Strategy:      p.Strategy,
			EntryPrice:    p.CostPrice,
			Quantity:      float64(p.Qty),
			HighestPrice:  p.HighestPrice,
			TakeProfitPct: tp,
			StopLossPct:   sl,
			Status:        "持仓中",
			// §PROD-T1（2026-09-18 生产实录）：开仓日透传（ApplyRealFill 首笔买入成交落的
			// real_positions.buy_date）。旧实现不填 EntryAt，零值经 buildExitContext 曾格式化为
			// "0001-01-01"（现已改判空串）使超期判定恒真——当日买入即误推"持仓超期离场"。
			// 有 buy_date 时按真实持仓交易日计超期；空（券商快照对账建的历史行）则未知、跳过超期。
			// English: §PROD-T1 — carry the opening trade date so hold-timeout counts real trading
			// days; unknown (broker-snapshot-created rows) stays zero → timeout check skips.
			EntryAt: parseEntryDate(p.BuyDate),
		})
	}
	return logs
}

// parseEntryDate §PROD-T1：real_positions.buy_date（"YYYY-MM-DD"，空=未知）→ time.Time。
// 解析失败/为空返回零值，交由 buildExitContext 的空串守卫跳过超期判定，绝不伪造开仓日。
// English: §PROD-T1 — parses the stored opening date; empty/unparseable yields the zero time,
// which the exit-context guard treats as "unknown" (timeout check skipped).
func parseEntryDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return t
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

// sellablePositions §PROD-T1：过滤掉当日买入 T+1 锁定（可卖量≤0）的持仓，供卖出侧五路评估使用。
// SellableQty 为 nil 或缺 key 时按"未知=不锁定"放行（测试/未注入路径保持旧行为）。
// English: §PROD-T1 — drop positions with zero sellable qty (T+1 locked) from the sell-side input;
// unknown (nil map / missing key) fails open to the legacy behavior.
func sellablePositions(in AdviceInput) []store.RealPosition {
	if len(in.SellableQty) == 0 {
		return in.Positions
	}
	out := make([]store.RealPosition, 0, len(in.Positions))
	for _, p := range in.Positions {
		if q, ok := in.SellableQty[p.TsCode]; ok && q <= 0 {
			continue // T+1 全锁：今日不可卖，不进入卖出侧
		}
		out = append(out, p)
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
	// §P4 缺陷3 标签映射收口（BUGFIX_SELLPOINT_FALSEPOSITIVE §三.3）：
	//   - 做空模式（卖点评估方向词徽标）按结构化 SellLevel 定帽，超期离场（提示）/未知类型
	//     落 default 按盈亏符号出 止盈/减仓——亏损票永不叫止盈；
	//   - 利空归因路改由结构化 AlertType="利空抛售" 承接（FIX#13 清仓级语义不变）；
	//   - 废除「reason 含 利空/抛售 子串 → 止损(高)」：展示层子串判定曾把任何含该子串的
	//     提醒（含"该票无利空迹象"否定句式）升级为止损帽（敞口 B 的展示侧根源），
	//     利空即时硬清现由统一裁决层双源验证（signalctl bearTier=dual）把关，不再靠子串。
	// English: P4 defect-3 label cleanup — short-mode badges map from the structured SellLevel; the
	// default branch follows the P/L sign so a losing position is never labeled 止盈; the bearish
	// substring → forced 止损(hi) heuristic is removed (guardrail-①), verified hard clears now live
	// in the unified adjudicator.
	closeLevel := func() {
		pa.Action, pa.Level = "止盈", "高"
		if pa.ProfitPct <= 0 {
			pa.Action, pa.Level = "止损", "高"
		}
	}
	switch sig.AlertType {
	case "清仓", "利空抛售":
		closeLevel()
	case "做空":
		switch sig.SellLevel {
		case "清仓":
			closeLevel()
		case "减仓":
			pa.Action, pa.Level = "减仓", "高"
		default: // 提示级/级别缺失：按盈亏符号走 default 同款口径
			if pa.ProfitPct > 0 {
				pa.Action, pa.Level = "止盈", "中"
			} else {
				pa.Action, pa.Level = "减仓", "中"
			}
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
		if pa.ProfitPct > 0 {
			pa.Action, pa.Level = "止盈", "中"
		} else {
			pa.Action, pa.Level = "减仓", "中"
		}
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
