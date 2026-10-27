// Package paper 实现独立模拟盘（纸面交易）：把策略 buy 信号按实时快照价自动撮合成虚拟持仓，
// 用实时价每日估值产出净值曲线，并记录「信号价 vs 成交价」的滑点与「信号发出→成交」延迟，
// 用于印证信号质量与时效性对收益的影响。
// 与真实持仓体系完全隔离：独立存储（paper.json），清盘/重置不影响任何实盘数据。
// English: independent paper-trading engine. It auto-fills strategy buy signals at the live snapshot
// price into virtual positions, marks the book to market daily for an equity curve, and records the
// signal-vs-fill price slippage plus the signal-to-fill latency — evidence of signal quality and
// timeliness impact on returns. Fully isolated from the real book: its own storage (paper.json), and
// a reset never touches live data.
package paper

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/ordbudget"
	"quant-trading-v2/internal/signalctl"
)

// LimitUpPct §R6 分板块涨停封板近似阈值（实时涨幅 ≥ 该值视为封板拒买）。
// 主板 10%（9.9 容差）、创业板/科创板 20cm（19.9）、北交所 30%（29.9）；
// 名称含 ST（含 *ST，大小写不敏感）按 5% 档（4.9）。code 形如 "600519.SH" 或裸码均可。
// English: board-aware limit-up threshold for the sealed-board buy guard — main board ~10%,
// ChiNext/STAR 20%, BSE 30%, ST (name contains "ST") 5%.
// vwapPremiumPct §C2 成交价相对市场 VWAP 的偏差（%）；缺 VWAP 返回 0（未采集）。
// English: §C2 fill-vs-VWAP premium; zero when VWAP is absent.
func vwapPremiumPct(fill, vwap float64) float64 {
	if vwap <= 0 {
		return 0
	}
	return (fill - vwap) / vwap * 100
}

func LimitUpPct(code, name string) float64 {
	// §R6 P1-4 收敛到 data.LimitUpPct（全系统唯一口径，含北交所 30% 板）。
	// English: delegated to data.LimitUpPct — single authoritative board-aware threshold.
	return data.LimitUpPct(code, name)
}

// Config 模拟盘配置（rules.paper）。
// English: paper-trading config (rules.paper).
type Config struct {
	Enabled        bool    `json:"enabled"`         // 总开关（默认 false）
	FixedAmount    float64 `json:"fixed_amount"`    // 每票固定买入资金（元，默认 10000；现金不足时按剩余现金整手买入）
	MaxPositions   int     `json:"max_positions"`   // 自定义持仓上限（0=不设限，持仓数由本金/现金余额自然决定；默认 0）
	InitialCapital float64 `json:"initial_capital"` // 初始资金（元，默认 100000）
	// AutoSell 卖出信号自动成交开关（阶段1.1 全自动执行）：开启时 清仓/减仓/硬止盈/硬止损
	// 告警直接在模拟盘自动平仓。由 config.PaperConfig.AutoSell 缺省填充（未配置=true）。
	// English: auto-sell switch (full-auto execution) — when on, 清仓/减仓/hard-TP/hard-SL alerts close
	// paper positions automatically. Defaulted from config.PaperConfig.AutoSell (true when unset).
	AutoSell bool `json:"auto_sell"` // 自动卖出
	// §R0 仿真级参数（后台可配，热加载）
	SlippageBps    float64 `json:"slippage_bps"`    // 滑点基点（万N，默认5=0.05%）
	CommissionRate float64 `json:"commission_rate"` // 佣金率（默认0.00025）
	StampTaxRate   float64 `json:"stamp_tax_rate"`  // 印花税率（仅卖出，默认0.0005）
	MinCommission  float64 `json:"min_commission"`  // 单笔最低佣金（默认5元）
	// ReentryCooldownMin 同票清仓后再入场冷却（分钟；§R1.4，取 paper_r12 的 reEntryTracker）。
	// 0 = 不限制（默认，兼容旧行为）；>0 时清仓后该时间段内禁止同票再次买入，
	// 防止"刚止损又追高"式的回马枪。可由后台 rules.paper.reentry_cooldown_min 配置。
	// English: re-entry cooldown (minutes, §R1.4) — 0 = unlimited (legacy default); >0 blocks re-buying
	// the same code within the window after a full close. Configurable via rules.paper.reentry_cooldown_min.
	ReentryCooldownMin int `json:"reentry_cooldown_min"`

	// §DYN-SIZE-v3 动态预算口径：dynamic 时池权益×缩放是唯一预算来源，固定金额/IR/每池次数/
	// 每日预算/持仓数量上限全部让位。legacy 保持旧行为。由 paper.ConfigFromRules 从账号 sizing
	// 配置装配；默认 legacy，代码合并本身不改变现有行为。
	// English: v3 dynamic budget — pool equity × scale is the sole budget source; the legacy fixed
	// amount / IR / daily count / daily budget / position-count caps all yield. Default legacy.
	BudgetMode string `json:"budget_mode,omitempty"`
	// SizingEnabled/SizingShadow 动态缩放是否开启/影子；影子或未开启时预算按中性 1.0 计算。
	SizingEnabled bool `json:"sizing_enabled,omitempty"`
	SizingShadow  bool `json:"sizing_shadow,omitempty"`
	// SingleStockCapPct 单票集中度上限（%，0=动态模式下回退 35）。行业上限复用 SetSectorExposure。
	// English: single-stock concentration cap (%; 0 falls back to 35 in dynamic mode). The industry
	// cap reuses SetSectorExposure.
	SingleStockCapPct float64 `json:"single_stock_cap_pct,omitempty"`

	// §SHORT-3 纸面融券做空（模拟盘做空侧，决策④）：做空战法信号 → 融券卖出开仓（负持仓），
	// 下跌买回平仓获利、上涨触发做空止损；独立做空资金池 shortCash，与做多池完全隔离，
	// 净值并入总账户。ShortCapital=0 即整条做空侧关闭（默认，影子期安全）。
	// English: §SHORT-3 paper short-selling book — bear-tactic signals open short positions in a
	// dedicated margin pool (shortCash, isolated from the long pools); drops profit on buy-back,
	// stop-loss on rallies; equity merges into the total curve. ShortCapital=0 disables the side
	// entirely (the safe shadow-run default).
	ShortEnabled     bool    `json:"short_enabled"`       // 融券做空开关（缺省配合 ShortCapital>0 生效）
	ShortCapital     float64 `json:"short_capital"`       // 做空池初始预算（元；0=不开设做空池）
	ShortMarginRate  float64 `json:"short_margin_rate"`   // 融券开仓保证金率（默认 0.5）
	ShortFeeAnnual   float64 `json:"short_fee_annual"`    // 融券年化费率（默认 0.083，按日对开仓名义额计提）
	ShortFixedAmount float64 `json:"short_fixed_amount"`  // 单笔融券开仓名义预算（元；0=回退 FixedAmount）
	ShortStopLossPct float64 `json:"short_stop_loss_pct"` // 做空止损：价格上涨超开仓价该百分比强制买回（默认 8；≤0=关闭，仅靠信号/手动平仓）
}

// DefaultConfig 返回模拟盘出厂默认配置。
// §R11 费率给真实默认值（此前注释称默认万2.5/最低5元但实际全 0，净值系统性虚高）：
// 佣金=max(成交额×0.025%,5元)、印花税卖出 0.05%、滑点 5bp。config.json 可覆盖；
// 测试经 testCfg() 显式置零保持确定性。
// English: factory defaults now carry realistic A-share fees (commission max(rate,¥5), stamp tax on
// sells only, slippage) instead of silent zeros; config-overridable, tests zero them explicitly.
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		FixedAmount:    10000,
		MaxPositions:   0,
		InitialCapital: 100000,
		AutoSell:       true,
		SlippageBps:    5,
		CommissionRate: 0.00025,
		StampTaxRate:   0.0005,
		MinCommission:  5,
		// §SHORT-3 融券做空默认：开关开但预算 0 = 做空池未开设（影子期安全默认），
		// 后台配 ShortCapital>0 才真正开仓；保证金/费率/止损用真实券商口径默认值。
		ShortEnabled:     true,
		ShortCapital:     0,
		ShortMarginRate:  0.5,
		ShortFeeAnnual:   0.083,
		ShortStopLossPct: 8,
	}
}

// Position 模拟持仓。
// SignalPrice 为信号发出时的信号价（辅助参照），CostPrice 为实际撮合价（实时价）。
// English: a paper position. SignalPrice is the reference signal price; CostPrice is the actual
// fill price (live price).
type Position struct {
	Code     string `json:"code"`     // 代码
	Name     string `json:"name"`     // 名称
	Strategy string `json:"strategy"` // 战法
	// StrategyType 该持仓所属战法池类型（fillLocked 时由信号 StrategyType 记录；
	// 卖出时收益按此回池。旧数据/手动买入为空 → 归"其他池"）。
	// English: the strategy-pool type this position belongs to (recorded at fill from the signal's
	// StrategyType; sale proceeds return to that pool on exit. Empty for legacy data / manual buys,
	// which fall into the "other" pool).
	StrategyType string    `json:"strategy_type,omitempty"` // 战法类型
	Qty          int       `json:"qty"`                     // 数量
	CostPrice    float64   `json:"cost_price"`              // 成本价
	Cost         float64   `json:"cost"`                    // 成本
	SignalPrice  float64   `json:"signal_price"`            // 信号价
	SignalAt     time.Time `json:"signal_at"`               // 信号时间
	FilledAt     time.Time `json:"filled_at"`               // 成交时间
	// Mark 最近一次估值价（实时快照，前端展示现价/浮盈用）。
	// English: last mark price from the live snapshot, for live P/L display.
	Mark float64 `json:"mark"` // 市值/标记价
	// ATR 该股 ATR14（开仓信号携带，镜像写 report 账时供 C4/ATR 动态止损距离计算；
	// 手动买入为 0 → 镜像回退固定百分比止损）。
	// English: the stock's ATR14 carried by the opening signal — used by the report-book mirror for
	// C4 ATR dynamic stop distance; 0 for manual buys (mirror falls back to fixed-percentage stops).
	ATR float64 `json:"atr,omitempty"`
	// §C2 成交时点市场 VWAP 与信号价偏差（0/空=未采集，不伪造）。
	// English: §C2 market VWAP at fill time and signal-vs-VWAP premium (zero = not captured).
	MarketVWAP     float64 `json:"market_vwap,omitempty"`
	VWAPPremiumPct float64 `json:"vwap_premium_pct,omitempty"`
	// AnchorPrice §ANCHOR-FIRST（2026-10-24 裁决二）首次买入价锚点＝建仓那笔成交的**含费**
	// 每股成本，与当刻的 CostPrice 同值，但此后**永不被改写**。
	// 为什么必须再养一个价：CostPrice 是盈亏口径，加仓必须摊薄（`addToPositionLocked` 重算
	// `p.Cost/p.Qty`），而止盈/止损/移动止盈的分母跟着摊薄走会造成两个形态——高位加仓把止损线
	// 一起抬高（该砍的砍不掉）、低位摊薄制造假盈利提前触发；移动止盈的 `highest > cost` 前置
	// 更会被一次加仓直接抹掉资格。裁决二把风险线钉回首次买入价，盈亏口径保持摊薄不变。
	// 写入冻结：只在 fillLocked 建仓处写一次；加仓/减仓/改成本一律不碰（由测试锁死）。
	// 0=未知（本批之前落盘的存量持仓）→ 影子期不参与判定、对照表点名，回填属 W3。
	// English: §ANCHOR-FIRST — fee-inclusive per-share cost of the opening fill, written once and
	// never rewritten. CostPrice stays the diluted P&L basis; the anchor is the risk-line basis the
	// ruling froze at the first buy. 0 = legacy position (named in the shadow table, backfilled in W3).
	AnchorPrice float64 `json:"anchor_price,omitempty"`
	// AnchorAt 锚点写入时刻（与建仓同时刻，用于回填审计与"锚点是否可信"判读）。
	// 存量持仓的 AnchorAt 为零值，前端/对照表按 AnchorPrice==0 一并视为未回填。
	// English: when the anchor was stamped (same instant as the opening fill); zero alongside
	// AnchorPrice==0 means "not backfilled".
	AnchorAt time.Time `json:"anchor_at,omitempty"`
	// AnchorSource §DYN-T（方案 §1.6a，六轮前的四轮 T4-6 定口径）锚点是从哪来的：
	//   first_lot         层账里最早那一批的含费买入价（＝真正的首买价，最可信）
	//   first_buy_order   买入历史（实盘 orders/fills）里最早一笔成交单价
	//   cost_fallback     只有摊薄成本可读（存量无层账、又查不到买入历史）
	//   none              什么价都拿不到，AnchorPrice 保持 0
	// 为什么必须留来源而不只留价格：**`cost_fallback` 的票不许开 T 腿**——那个数可能被历史加仓
	// 甚至被 T 腿摊薄过，把它当"最初那笔"会让回填悄悄把摊薄成本请回风控基准（正是本批要修的缺陷）。
	// English: provenance of the anchor; cost_fallback/none rows are barred from opening T legs because a
	// diluted cost must never be passed off as the first buy price.
	AnchorSource string `json:"anchor_source,omitempty"`
	// Lots §T1-PARTIAL（2026-10-24 裁决三）批次层账：同一交易日买到的股数合成一批。
	// 它只回答"这批哪天买的 ⇒ 今天可卖多少"，不改任何价格口径（结算仍按 CostPrice 摊薄、
	// 风险线仍按 AnchorPrice 冻结）。空层账＝本批之前落盘的存量持仓 ⇒ 退回旧的整仓二值判定。
	// 为什么必须存在：旧实现一个持仓只有一个 FilledAt，且加仓从不刷新它——当日加过仓的持仓
	// 会带着昨日底仓的日期被判"整仓可卖"（T+1 漏放），而全仓今日买入时退出裁决又被整单挡死。
	// English: per-day buy batches, answering only "which day was this lot bought → how much is
	// sellable today". Prices are untouched (settlement stays on the diluted CostPrice, risk lines on the
	// frozen AnchorPrice). An empty ledger means a legacy position and falls back to the old binary rule.
	Lots []PositionLot `json:"lots,omitempty"`
	// Status §T1-PARTIAL 持仓的卖出状态："部分可卖"＝这条持仓此刻有一部分股卖不掉（层账里有当日批）
	// 且另一部分能卖——由建仓/加仓/减仓三条路径按层账重算，不参与任何撮合判定。
	// 为什么要这个字段：裁决三之后"减仓只卖掉一半"变成正常结局，用户必须能在账面上看到
	// "为什么没卖光"，否则会被读成引擎出错。
	// 全仓今日买入（可卖 0）刻意不打标签：那一态的读数已经在 sellable_qty=0 上，把两个维度挤进
	// 同一个字符串，前端就无法分别显示"卖得掉多少"与"为什么卖不掉"。
	// English: sell-side status marker — "部分可卖" only for the mixed case (part T+1-locked, part sellable).
	// Recomputed from the lot ledger on the open/add/trim paths; never feeds an execution decision. The
	// fully-locked case is intentionally unlabelled because sellable_qty=0 already says it.
	Status string `json:"status,omitempty"`
}

// statusPartSellable §T1-PARTIAL 的展示态文案（唯一写入点，避免各处手打字符串漂移）。
// English: the one status literal written by the partial-sellable path.
const statusPartSellable = "部分可卖"

// PendingExitQty 该持仓名下尚未执行完的残量退出意图股数（0＝无欠账）。
// 语义钉死为"意图量"，不承诺与 Qty−今日批次之和相等：手动加过仓、部分卖出后都只会让意图
// 缩量或提前作废，绝不反向虚增（虚增会把一次已经卖完的退出说成没卖完）。
// English: outstanding residual-exit quantity for this code (0 = nothing owed). Deliberately the intent
// quantity, not a recomputed Qty−intraday sum, so it can never overstate what is still unsold.
func (e *Engine) PendingExitQty(code string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if pe := e.pendingExits[code]; pe != nil {
		return pe.WantQty
	}
	return 0
}

// SellableQty 该代码当前可卖股数（与卖出入口同源；不在账返回 0）。
// English: shares sellable now for a code (same source as the sell path; 0 when not held).
func (e *Engine) SellableQty(code string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sellableQtyLocked(e.positions[code], time.Now())
}

// AnchorOrCost 返回风险线应使用的基准价：有锚点用锚点，存量无锚点持仓回落成本价。
// ⚠ 本方法是 §ANCHOR-FIRST **W3 切硬**时的读取入口；W2 影子期不消费它（对照表用 AnchorPrice
// 原值，0 就是"未知"，绝不拿成本价冒充首买价参与对照，否则两侧永远相同、对照表恒绿即恒盲）。
// English: the risk-line basis (anchor when known, diluted cost for legacy positions). This is the W3
// read entry — the W2 shadow table deliberately uses the raw AnchorPrice so "unknown" never masquerades
// as "identical".
func (p *Position) AnchorOrCost() float64 {
	if p.AnchorPrice > 0 {
		return p.AnchorPrice
	}
	return p.CostPrice
}

// MarketValue 返回该持仓按当前估值价的市值。
// English: returns the position's market value at the current mark price.
func (p *Position) MarketValue() float64 { return p.Mark * float64(p.Qty) }

// MarshalJSON 输出持仓字段外加浮动盈亏/滑点/延迟等计算值（前端直接展示）。
// English: marshals the position with computed floating P/L, slippage and latency for the frontend.
func (p Position) MarshalJSON() ([]byte, error) {
	// alias 原始 Position 类型别名（避免自定义 MarshalJSON 造成无限递归）。
	type alias Position
	// SellableQty §T1-PARTIAL 可卖量上屏：前端"减仓"要知道今天到底能卖多少，否则用户填的量
	// 一定被引擎封顶、看不出发生了什么。口径与卖出入口完全同源（同一个 LayersOfPosition），
	// 不是第二把尺子。
	return json.Marshal(&struct {
		alias
		Pnl         float64 `json:"pnl"`
		PnlPct      float64 `json:"pnl_pct"`
		SlippagePct float64 `json:"slippage_pct"`
		LatencySec  int64   `json:"latency_sec"`
		SellableQty int     `json:"sellable_qty"`
	}{
		alias:       alias(p),
		Pnl:         p.PnL(),
		PnlPct:      round2(p.PnLPct()),
		SlippagePct: round2(p.SlippagePct()),
		LatencySec:  p.LatencySec(),
		SellableQty: LayersOfPosition(&p, time.Now()).Sellable,
	})
}

// PnL 返回浮动盈亏（正=盈利）。
// English: returns the floating P/L (positive = profit).
func (p *Position) PnL() float64 { return (p.Mark - p.CostPrice) * float64(p.Qty) }

// PnLPct 返回浮动盈亏百分比（相对成本）。
// English: returns the floating P/L percent relative to cost.
func (p *Position) PnLPct() float64 {
	if p.Cost <= 0 {
		return 0
	}
	return (p.MarketValue() - p.Cost) / p.Cost * 100
}

// SlippagePct 返回「成交价 vs 信号价」滑点百分比（正=买贵了）。
// English: the fill-vs-signal price slippage percent (positive = filled higher than signal).
func (p *Position) SlippagePct() float64 {
	if p.SignalPrice <= 0 {
		return 0
	}
	return (p.CostPrice - p.SignalPrice) / p.SignalPrice * 100
}

// LatencySec 返回信号发出→成交的延迟秒数。
// English: returns the seconds from signal generation to fill.
func (p *Position) LatencySec() int64 {
	if p.SignalAt.IsZero() {
		return 0
	}
	d := int64(p.FilledAt.Sub(p.SignalAt).Seconds())
	if d < 0 {
		return 0
	}
	return d
}

// Trade 一笔模拟成交记录（买入含信号价参照与延迟）。
// English: one paper fill. Buys carry the reference signal price and latency.
type Trade struct {
	Code     string `json:"code"`     // 代码
	Name     string `json:"name"`     // 名称
	Strategy string `json:"strategy"` // 战法
	// StrategyType 该成交所属战法池类型（分池/盘后研究落库归类用；空=其他池/手动）。
	// English: the strategy-pool type of this fill (for pooling and post-close research export; empty =
	// the other pool / manual).
	StrategyType string    `json:"strategy_type,omitempty"` // 战法类型
	Side         string    `json:"side"`                    // buy / sell（买入/卖出）
	Price        float64   `json:"price"`                   // 价格
	SignalPrice  float64   `json:"signal_price,omitempty"`  // 信号价
	Qty          int       `json:"qty"`                     // 数量
	Amount       float64   `json:"amount"`                  // 成交额
	Time         time.Time `json:"time"`                    // 时间
	// LatencySec 信号发出→成交 的秒数（买入时记录；量化信号时效性）。
	// English: seconds from signal generation to fill (recorded on buys; quantifies signal timeliness).
	LatencySec int64 `json:"latency_sec,omitempty"` // 信号→成交延迟（秒）
	// Fee 本笔成交费用（佣金+印花税，§R1/R2 费用入账审计字段；0=未收费）。
	// English: fees charged on this fill (commission + stamp tax; audit field for fee accounting).
	Fee    float64 `json:"fee,omitempty"`    // 本笔成交费用（佣金+印花税）
	Reason string  `json:"reason,omitempty"` // 原因
	// §C2 成交时点市场 VWAP 与偏差（0/空=未采集）。
	MarketVWAP     float64 `json:"market_vwap,omitempty"`
	VWAPPremiumPct float64 `json:"vwap_premium_pct,omitempty"`
}

// ── §R1/R2 费用模型（A股口径）───────────────────────────────────────────
// 佣金 = max(成交额 × CommissionRate, MinCommission)；印花税 = 卖出成交额 × StampTaxRate（仅卖出，
// 无最低）；费率 ≤0 视为不收（兼容既有测试/用户配置）。须持锁调用（读 cfg 快照语义）。

// buyFeeLocked 计算买入佣金：成交额 × 费率（含最低佣金封底），费率≤0 视为不收。须持锁调用。
func (e *Engine) buyFeeLocked(cost float64) float64 {
	if e.cfg.CommissionRate <= 0 {
		return 0
	}
	fee := cost * e.cfg.CommissionRate
	if e.cfg.MinCommission > 0 && fee < e.cfg.MinCommission {
		fee = e.cfg.MinCommission
	}
	return fee
}

// sellFeeLocked 计算卖出费用：佣金（含最低佣金封底）+ 印花税（仅卖出收取）。须持锁调用。
func (e *Engine) sellFeeLocked(proceeds float64) float64 {
	fee := 0.0
	if e.cfg.CommissionRate > 0 {
		fee = proceeds * e.cfg.CommissionRate
		if e.cfg.MinCommission > 0 && fee < e.cfg.MinCommission {
			fee = e.cfg.MinCommission
		}
	}
	if e.cfg.StampTaxRate > 0 {
		fee += proceeds * e.cfg.StampTaxRate
	}
	return fee
}

// Order 订单生命周期记录（阶段1.3）：一次"信号→订单→结果"的完整审计条目，
// 与 Trade（仅成交）互补——被拒绝/部分成交的尝试也留痕，便于复盘为何没买进/没卖出。
// English: an order-lifecycle record (one full signal→order→outcome audit entry), complementing
// Trade (fills only) — rejected/partial attempts are kept too, so missed fills are reviewable.
type Order struct {
	ID           string    `json:"id"`                      // 订单号（ord_<seq>）
	Code         string    `json:"code"`                    // 股票代码
	Name         string    `json:"name"`                    // 股票名称
	Strategy     string    `json:"strategy"`                // 战法名
	StrategyType string    `json:"strategy_type,omitempty"` // 战法池类型
	Side         string    `json:"side"`                    // buy / sell（买入/卖出）
	Kind         string    `json:"kind"`                    // 来源：自动撮合/手动买入/自动清仓/自动减仓/手动卖出/手动减仓
	SignalPrice  float64   `json:"signal_price,omitempty"`  // 信号价（参照）
	Price        float64   `json:"price"`                   // 成交价（rejected 时为 0）
	Qty          int       `json:"qty"`                     // 成交数量（rejected 0；partial 为部分量）
	Status       string    `json:"status"`                  // filled=全部成交 / partial=部分成交 / rejected=已拒绝
	Reason       string    `json:"reason,omitempty"`        // 拒绝原因或触发理由（告警文案）
	CreatedAt    time.Time `json:"created_at"`              // 下单时间
}

// orderSeq 订单号自增（进程内唯一即可，重启从时间戳续）。
// English: in-process order sequence; restarts reseed from the timestamp.
var orderSeq atomic.Int64

// newOrderID 生成全局唯一订单 ID（格式 ORD + 纳秒时间戳）。
func newOrderID() string {
	return fmt.Sprintf("ord_%d_%d", time.Now().Unix(), orderSeq.Add(1))
}

// EquityPoint 净值序列（按交易日一个点）。
// English: an equity-curve point, one per trading day.
type EquityPoint struct {
	Date  string  `json:"date"`  // YYYY-MM-DD（交易日）
	Value float64 `json:"value"` // 总资产 = 现金 + 持仓市值
	Cash  float64 `json:"cash"`  // 现金
}

// Stats 绩效与信号质量汇总。
// English: performance and signal-quality summary.
type Stats struct {
	InitialCapital float64 `json:"initial_capital"`  // 初始资金
	Cash           float64 `json:"cash"`             // 现金
	MarketValue    float64 `json:"market_value"`     // 市值
	TotalValue     float64 `json:"total_value"`      // 总资产
	TotalReturnPct float64 `json:"total_return_pct"` // 总收益率
	RealizedPnl    float64 `json:"realized_pnl"`     // 已实现盈亏
	OpenPositions  int     `json:"open_positions"`   // 持仓数
	WinRatePct     float64 `json:"win_rate_pct"`     // 已平仓胜率
	// 信号质量统计：买入信号→成交 的滑点与延迟
	FilledBuys        int     `json:"filled_buys"`         // 已撮合的买入信号数
	AvgSlippagePct    float64 `json:"avg_slippage_pct"`    // 平均滑点%（成交价相对信号价）
	AvgLatencySec     float64 `json:"avg_latency_sec"`     // 平均信号→成交延迟（秒）
	MaxLatencySec     int64   `json:"max_latency_sec"`     // 最大延迟（秒）
	SlippageCost      float64 `json:"slippage_cost"`       // 滑点累计成本（元，相对信号价多花的钱）
	SignalAmountPct   float64 `json:"signal_amount_pct"`   // 滑点成本占初始资金比（%）
	TodayReturnPct    float64 `json:"today_return_pct"`    // 当日收益%
	EquityCurvePoints int     `json:"equity_curve_points"` // 净值点数量
	// §R2.2 绩效指标（净值序列计算）
	SharpeRatio float64 `json:"sharpe_ratio,omitempty"`     // 年化夏普比率
	MaxDrawdown float64 `json:"max_drawdown_pct,omitempty"` // 最大回撤%
	Calmar      float64 `json:"calmar,omitempty"`           // Calmar 比率
	Expectancy  float64 `json:"expectancy_pct,omitempty"`   // 每笔期望收益率%
	// §SHORT-3 融券做空侧统计（做空池未开设时全为 0，前端按 enabled 显隐）。
	// English: §SHORT-3 short-book stats (all zero when the pool isn't funded; the frontend gates on enabled).
	ShortEnabled    bool    `json:"short_enabled,omitempty"`     // 做空侧是否启用
	ShortCash       float64 `json:"short_cash,omitempty"`        // 做空池可用现金
	ShortMarginUsed float64 `json:"short_margin_used,omitempty"` // 冻结保证金合计
	ShortFloatPnl   float64 `json:"short_float_pnl,omitempty"`   // 空头浮动盈亏合计
	ShortRealized   float64 `json:"short_realized,omitempty"`    // 空头已实现盈亏累计
	ShortPositions  int     `json:"short_positions,omitempty"`   // 空头持仓数
	ShortEquity     float64 `json:"short_equity,omitempty"`      // 做空侧权益（现金+保证金+浮动）
}

// StrategyPoolState 一个战法资金池的展示快照（前端分仓条）。
// Cost/Realized/Floating/ReturnPct 为该池持久化的累计表现：买入按成交成本累计（卖出不减，
// 收益仍记该池），卖出按实现盈亏累计，浮动盈亏按当前持仓现价估算；
// 总涨跌幅 = (已实现 + 浮动) / 累计买入成本。Stats 为该池独立的绩效/信号质量汇总（前端
// 切换到该池 tab 时，页顶统计卡/信号质量卡据此展示，与全局统计并列）。
// BuyRule 为该池当前生效的买入纪律（§A3 前端预填；nil=未设置纪律，仅受全局约束）。
// English: one strategy cash pool's display snapshot (frontend allocation strip).
// Cost/Realized/Floating/ReturnPct are the pool's persisted cumulative performance; Stats is the
// pool's own performance/signal-quality summary. BuyRule is the pool's active buy discipline
// (§A3 frontend prefill; nil = no discipline set).
type StrategyPoolState struct {
	Key       string  `json:"key"`       // 策略类型（""=其他/手动池）
	Label     string  `json:"label"`     // 展示名
	Cash      float64 `json:"cash"`      // 池内可用现金
	RatioPct  float64 `json:"ratio_pct"` // 占总现金比例（%）
	Positions int     `json:"positions"` // 池内持仓数
	// MaxPos 该池持仓上限（0=不单独设限，仅受全局上限约束；与全局解耦可自定义）。
	// English: this pool's position cap (0 = no per-pool limit, only the global cap applies; decoupled
	// from the global cap and customizable).
	MaxPos    int     `json:"max_pos"`    // 持仓上限
	Cost      float64 `json:"cost"`       // 累计买入成本（按买入后计数，卖出不减）
	Realized  float64 `json:"realized"`   // 已实现盈亏（卖出结算，仍记本池）
	Floating  float64 `json:"floating"`   // 浮动盈亏（当前持仓市值 - 成本）
	ReturnPct float64 `json:"return_pct"` // 总涨跌幅（%）= (已实现+浮动)/累计成本
	Stats     Stats   `json:"stats"`      // 该池独立绩效/信号质量汇总（前端分仓 tab 统计卡用）
	// BuyRule 该池当前生效的买入纪律快照（nil=未设置）。§A3 分仓设置弹窗预填用。
	BuyRule *PoolBuyRule `json:"buy_rule,omitempty"` // 该池买入纪律
}

// PoolPerf 一个战法资金池的持久化表现（成本基准 + 已实现盈亏）。
// English: a strategy pool's persisted performance (cost basis + realized P&L).
type PoolPerf struct {
	Cost     float64 `json:"cost"`     // 累计买入成本（按买入后计数，卖出不减）
	Realized float64 `json:"realized"` // 已实现盈亏（卖出结算，仍记本池）
}

// strategyPoolLabel 战法池类型 → 展示名。
// §命名纠错：dragon=龙头、dragon_return=龙回头（与配置段/回测战法名统一——
// 旧版把 dragon 标成"龙回头"造成用户看到两个龙回头却没有龙头的困惑）。
// §名称规整：n_shape 统一为"N形"（与配置白名单/研究任务/scheduler 映射同口径，
// 旧展示"N形超短"仍被 PoolKeyForStrategy 别名接受）；新增 momentum=动量。
// English: strategy-pool type → display label; dragon="龙头", dragon_return="龙回头",
// n_shape="N形" (canonical), momentum="动量".
func strategyPoolLabel(t string) string {
	switch t {
	case "dragon":
		return "龙头"
	case "double_bump":
		return "双响炮"
	case "n_shape":
		return "N形"
	case "dragon_return":
		return "龙回头"
	case "momentum":
		return "动量"
	}
	return "其他"
}

// Engine 模拟盘引擎：独立于真实持仓的虚拟撮合/估值/统计。
// path 为 JSON 持久化路径（空则不落盘，纯内存）。
// English: the paper engine — virtual fill/mark/statistics, isolated from the real book.
// path is the JSON persistence file (empty = in-memory only).
type Engine struct {
	cfg Config // 当前生效的模拟盘配置

	mu             sync.Mutex                // 保护引擎状态的互斥锁
	cash           float64                   // 现金
	pools          map[string]float64        // 战法资金池：key=策略类型（""=其他/手动池），Σpools == cash
	poolTypes      []string                  // 启用的战法类型（不含 ""），保持有序；空=未分仓（单池）
	poolPerf       map[string]*PoolPerf      // 战法资金池持久化表现：key=策略类型（买入累计成本/已实现盈亏）
	poolMaxPos     map[string]int            // 每池持仓上限：key=策略类型（0=该池不单独设限，仅受全局上限约束；可自定义）
	poolBuyRules   map[string]*PoolBuyRule   // 每池买入纪律规则（冷却/日限/门槛/预算配比）
	poolGrp        map[string]string         // §Phase3 A/B 对照组：poolKey → 组标签（A=回测最优/B=灰度观察）
	poolIR         map[string]float64        // §Phase4 IR 动态仓位：poolKey → 参考 IR（自动买入金额缩放）
	poolDiscipline map[string]poolDiscipline // 运行时状态：每池今日买入计数/花费/最近时间
	// §C 规则细分池：fac_1/pat_2 等 EnsurePool 开立的动态池 key 集合（SetStrategyPools
	// 重建时必须合并保留，否则配置同步会把规则池冲掉）；labelFn 解析规则 ID → 显示名。
	extraPoolKeys map[string]bool // 规则细分池动态 key 集合
	labelFn       func(string) string
	positions     map[string]*Position // 持仓数
	trades        []Trade              // trades
	orders        []Order              // 订单生命周期（阶段1.3）：信号→订单→成交/拒绝 全留痕
	equity        []EquityPoint        // equity
	hasFilled     bool                 // 是否发生过任何成交：false 期间 Snapshot 不记净值点（无买入不应有净值曲线）
	realized      float64              // 已实现盈亏累计
	path          string               // path
	// 账本镜像回调（阶段1.2 两本账合一）：paper 为唯一真实账本，开仓/清仓经回调同步写
	// report 持仓账（由 engine/registry 注入），退出引擎/持仓页/打分池消费的 rpt 与模拟盘一致。
	// onOpen 在新开仓后触发（含手动买入；加仓不触发）；onClose 仅在整笔清仓时触发（部分减仓不触发）。
	// English: book-mirror callbacks (unified books) — paper is the single source of truth; opens/closes
	// are mirrored into the report holding book via callbacks injected by engine/registry, so the rpt
	// consumed by exit engines / positions page / scoring pool stays consistent with the paper book.
	// onOpen fires after a NEW position (manual buys included; add-ons excluded); onClose fires only on
	// full closes (partial trims excluded).
	onOpen  func(p Position)
	onClose func(code string, price, qty float64, reason string)
	// trimDone 当日减仓去重：code → "YYYYMMDD"（同一交易日同一持仓只响应一次减仓类信号，
	// 防止多轮告警把仓位反复减半）。清仓类无需去重（平仓后自然 no-op）。
	// English: same-day trim dedup — code → "YYYYMMDD"; trim-type signals act at most once per code per
	// trading day so repeated alerts can't keep halving the position. Full closes need no dedup (natural
	// no-op once flat).
	trimDone map[string]string // 当日减仓去重表（code → 交易日）
	// pendingExits §T1-PARTIAL（裁决三 W4）残量退出意图：code → 因 T+1 没卖完的那部分。
	// 与 trimDone 正交（3.3 要求两者互不吞并）：去重表只管"减仓信号当日还能不能再来一次"，
	// 意图表只管"这笔退出裁决还欠多少股没卖"，次日由 RunPendingExits 无条件补卖。
	// English: §T1-PARTIAL residual exit intents per code — orthogonal to trimDone (dedup says whether a
	// trim signal may act again today; the intent says how much of an exit is still unsold).
	pendingExits map[string]*PendingExit
	// reEntry 同票清仓后再入场冷却追踪器（§R1.4，来自 paper_r12）。清仓时记录时间，
	// 新开仓前置检查处调用 canReEnter 拦住冷却期内回马枪。nil 安全（未启用冷却时为 nil）。
	// English: re-entry cooldown tracker (§R1.4, paper_r12). Records close time; the fill pre-check calls
	// canReEnter to block re-buys inside the cooldown window after a full close. nil-safe.
	reEntry *reEntryTracker
	// §SIGNAL_CONTROLLER 20260917：买入确认状态机（discipline/buyConfirm/SetDiscipline）已整体迁出——
	// 战法白名单、个股·板块黑名单与持续性确认窗由信号控制器（internal/signalctl）paper 通道
	// 在 registry 分发前统一裁定；本引擎回归纯执行器（池/撮合/费率/上限），不再持有交易裁决状态。
	// English: the buy-confirm state machine moved to the signal controller (paper channel, applied at
	// registry dispatch); this engine is now execution-only.
	// lastBuyReject 买入拒绝订单去重：code → 最近一次拒绝原因。探针改为每轮重放全量活跃
	// 买入信号后，不可撮合信号（持仓上限/涨停/无行情等）会每 5s 触达一次——同一原因只留痕
	// 一次，防止 orders 表与磁盘写被刷爆；成功成交时清除该码记录。
	// English: rejected-buy order dedup — code → last reject reason. With the probe now re-feeding the
	// full active buy set each round, an unfillable code would log a rejected order every 5s; the same
	// reason is audited once. Cleared on a successful fill.
	lastBuyReject map[string]string

	// §DYN-GLOBAL「dynamic 预算已生效、缩放未交付」半开态留痕去重：记录最近一次告警的自然日
	// （cntime.DayOf 口径）。探针每轮都进 OnSignals，不去重会把日志刷爆；配置被修正后无需清零——
	// 次日仍会按当时的取值重新判断一次。
	// English: once-per-day dedup for the half-open budget warning above (probe calls OnSignals every
	// round, so an undeduped log line would flood). No reset needed after a fix — the next day re-checks.
	budgetWarnDay string

	// tRounds §T-MODE（2026-10-24 裁决四 W6/W7）做 T 回转账：每条记录一次"两腿成对"的日内回转。
	// 独立于 trades/orders —— trades 是成交流水（一次回转产生两条），orders 是委托生命周期留痕，
	// 回转账回答的是第三个问题："这两条成交是不是同一次回转的两条腿、锁了多少价差"。
	// English: §T-MODE T-round ledger — one record per two-leg intraday round. Kept apart from trades
	// (fill stream) and orders (order lifecycle) because it answers a third question: whether two fills
	// belong to the same round and what spread was locked.
	tRounds []TRound
	// shadowNoTOn / shadowNoT §T-COST B5（2026-10-27）"不做 T"影子账开关与状态。
	// 开关由 engine 每轮从账号级 sizing.t.shadow_no_t 注入（热生效）；状态只被镜像/采样/读数组装
	// 触碰，**任何判定（Room/预算/风控）都不得读取**——那是这套对照实验成立的前提。
	// English: §T-COST B5 no-T shadow switch/state; never read by any decision path, only mirroring,
	// sampling and the readout (see t_shadow_no_t.go).
	shadowNoTOn bool
	shadowNoT   *tShadowNoTBook
	// tCfgWarnDay §8.8 配置自洽告警（min_spread 与振幅门打架）的每日一次去重。刻意与下面的
	// tWarnDay 分开：两把闸在同一个启动日各自都可能要出声，共用一个 latch 会让先响的那条把后响的吞掉。
	tCfgWarnDay string
	// tWarnDay 做 T 被预算口径挡下时的每日一次告警去重（与 budgetWarnDay 同口径，但独立计数：
	// 两者解除条件不同——预算半开态改配置就消失，做 T 禁用还要看 Q5 的口径）。
	// English: once-per-day dedup for the "T blocked by budget pipeline" log, tracked separately from
	// budgetWarnDay because the two clear under different conditions.
	tWarnDay string
	// tBlockDay / tBlock §DYN-T 被拦计数（按"哪一步拦的"归一，方案 §8.9）。
	// 为什么必须有：灰度期最贵的不是少做了几轮，而是**不知道为什么会少做**——
	// 只写"没做 T"的日志，两周后没人能判断是门槛过严还是行情不给振幅。
	// 每条留痕按 (step, code) 每日去重打印，计数则每次累加，供接口/面板汇总。
	// English: per-step block counters with a once-per-day line per (step, code), so "why no T round"
	// is answerable from data instead of from memory.
	tBlockDay string
	tBlock    map[string]int
	// tBlockLogged 留痕去重表（每个 (step,code) 每天只打一行），与计数分开：
	// 计数要"每次都加"，日志不能每 5 秒刷一行。
	tBlockLogged map[string]bool
	// sizingCtx §DYN-SIZE v4：engine 每轮注入的定权上下文（市场缩放、单笔基准、天花板、盲区标志）。
	// 为什么不进 Config：Config 是会落盘的账号规则，而 MS 是**本轮行情算出来的数**，
	// 落盘就等于把某一时刻的市场读数固化成长期配置。
	sizingCtx paperSizingCtx
	// nowFn 做 T 的时钟缝（nil ⇒ time.Now）。存在的理由只有一个：`RunIntradayT` 内部取"现在"
	// 判断是否已到 14:50 尾盘强制配平线，取真实时间会让**全部正 T 用例在 14:50 之后必然变红**
	// （force=true 直接跳过开腿）——白天绿、晚上红的测试等于没有测试。生产路径永远留 nil。
	// English: clock seam for §T-MODE only. RunIntradayT compares "now" against the 14:50 forced-pairing
	// line; reading the wall clock makes every buy-first case fail after 14:50 (force short-circuits leg
	// opening). nil in production — the seam exists so tests can pin a session time.
	nowFn func() time.Time

	// §C1 行业敞口（与实盘态同口径）：上限%、影子开关、行业查询源。默认全关/影子，
	// 未注入时闸自然跳过，零行为变化。
	// English: §C1 sector exposure (same semantics as the live gate) — cap %, shadow switch and
	// industry lookup; all inert by default/when not injected.
	sectorLimitPct float64
	sectorShadow   bool
	industryOf     func(code string) string
	// singleStockLimitPct §DYN-SIZE-v3 单票集中度上限（%；0=动态模式回退 35）。
	// English: v3 single-stock concentration cap (%; 0 falls back to 35 in dynamic mode).
	singleStockLimitPct float64
	// §C1-V2 概念敞口（与实盘同口径）：上限、影子、覆盖率门槛、覆盖率读数、股票→概念反查。
	// 覆盖率不可得/低于门槛时闸弃权；概念未知也弃权。
	// English: §C1-V2 paper concept exposure — cap, shadow, coverage floor, coverage reading and
	// stock→concepts lookup; missing/low-coverage inputs abstain.
	conceptLimitPct    float64
	conceptShadow      bool
	conceptMinCoverage float64
	conceptCoverageFn  func() (pct float64, ok bool)
	conceptsOf         func(code string) []string

	// §SHORT-3 融券做空侧（详见 short.go）：独立做空池 shortCash（预算，与做多 cash/pools
	// 完全隔离）+ 负持仓表 shorts + 已实现盈亏 shortRealized + 利息按日计提去重表 shortFeeDone。
	// 净值口径：总资产 = 做多(cash+mv) + 做空(shortCash + Σ担保占用 + 浮动盈亏)。
	// English: §SHORT-3 paper short book — a dedicated margin budget (shortCash, fully isolated from
	// the long cash/pools), negative positions (shorts), realized P&L and per-day fee accrual dedup.
	// Equity = long (cash+mv) + short (shortCash + Σ frozen margin + floating P&L).
	shorts        map[string]*ShortPosition // 融券负持仓：code → 空头
	shortCash     float64                   // 做空池可用现金（扣除保证金占用与费用后）
	shortRealized float64                   // 做空已实现盈亏（买回平仓结算，含费用）
	shortFeeDone  map[string]string         // 融券利息计提去重：code → 已计提交易日
	shortOpenSeen map[string]string         // 开仓信号去重：code → 触发交易日（同日同码只下一笔融券单）
}

// SetSectorExposure §C1 注入行业敞口参数（limitPct<=0 或 industryOf=nil 时闸跳过）。
// shadow=true 时命中只打日志放行，与实盘 risk_gates 影子期同口径。
// English: §C1 injects the sector cap; shadow mode logs and passes, mirroring the live gate.
func (e *Engine) SetSectorExposure(limitPct float64, shadow bool, industryOf func(code string) string) {
	e.mu.Lock()
	e.sectorLimitPct = limitPct
	e.sectorShadow = shadow
	e.industryOf = industryOf
	e.mu.Unlock()
}

// SetConceptExposure §C1-V2 注入纸面概念敞口参数（任一依赖缺席/上限<=0 时闸跳过）。
// English: §C1-V2 injects paper concept exposure; any missing dependency keeps the gate inert.
func (e *Engine) SetConceptExposure(limitPct float64, shadow bool, minCoverage float64,
	coverageFn func() (float64, bool), conceptsOf func(code string) []string) {
	e.mu.Lock()
	e.conceptLimitPct = limitPct
	e.conceptShadow = shadow
	e.conceptMinCoverage = minCoverage
	e.conceptCoverageFn = coverageFn
	e.conceptsOf = conceptsOf
	e.mu.Unlock()
}

// checkConceptExposureLocked §C1-V2 纸面概念敞口检查（调用方须持 e.mu）。
// 与实盘同口径：单票多概念取最大敞口，不做重叠求和；覆盖率不足/概念未知弃权。
// English: §C1-V2 paper concept check — single largest matching concept, no overlap sum;
// low coverage / unknown concept abstain.
func (e *Engine) checkConceptExposureLocked(code string, orderCost float64) (bool, string) {
	if e.conceptLimitPct <= 0 || e.conceptCoverageFn == nil || e.conceptsOf == nil || orderCost <= 0 {
		return true, ""
	}
	coverage, ok := e.conceptCoverageFn()
	if !ok || coverage < e.conceptMinCoverage {
		return true, ""
	}
	targets := e.conceptsOf(code)
	if len(targets) == 0 {
		return true, ""
	}
	total := e.cash
	for _, p := range e.positions {
		price := p.Mark
		if price <= 0 {
			price = p.CostPrice
		}
		if price > 0 {
			total += price * float64(p.Qty)
		}
	}
	if total <= 0 {
		return true, ""
	}
	held := map[string]float64{}
	for _, p := range e.positions {
		if p.Qty <= 0 {
			continue
		}
		price := p.Mark
		if price <= 0 {
			price = p.CostPrice
		}
		if price <= 0 {
			continue
		}
		for _, c := range e.conceptsOf(p.Code) {
			held[c] += price * float64(p.Qty)
		}
	}
	worstPct, worst := 0.0, ""
	for _, c := range targets {
		pct := (held[c] + orderCost) / total * 100
		if pct > worstPct {
			worstPct, worst = pct, c
		}
	}
	if worst == "" || worstPct <= e.conceptLimitPct {
		return true, ""
	}
	reason := fmt.Sprintf("概念敞口超限: 概念=%s 预计市值 %.0f / 总资产 %.0f = %.1f%% > 上限 %.1f%%（覆盖率 %.1f%%）",
		worst, held[worst]+orderCost, total, worstPct, e.conceptLimitPct, coverage)
	if e.conceptShadow {
		log.Printf("[paper] §C1-V2 概念敞口影子命中（放行） %s: %s", code, reason)
		return true, ""
	}
	return false, reason
}

// checkSectorExposureLocked §C1 纸面行业敞口检查（调用方须持 e.mu）。
// 未知行业/总资产不可得 → 放行（弃权）；影子命中只留日志，不拒单。
// English: §C1 paper-book sector check (caller holds e.mu); unknown inputs abstain; shadow logs only.
func (e *Engine) checkSectorExposureLocked(code string, orderCost float64) (bool, string) {
	if e.sectorLimitPct <= 0 || e.industryOf == nil || orderCost <= 0 {
		return true, ""
	}
	target := strings.TrimSpace(e.industryOf(code))
	if target == "" {
		return true, ""
	}
	total := e.cash
	for _, p := range e.positions {
		price := p.Mark
		if price <= 0 {
			price = p.CostPrice
		}
		if price > 0 {
			total += price * float64(p.Qty)
		}
	}
	if total <= 0 {
		return true, ""
	}
	held := 0.0
	for _, p := range e.positions {
		if p.Qty <= 0 {
			continue
		}
		if ind := strings.TrimSpace(e.industryOf(p.Code)); ind == "" || ind != target {
			continue
		}
		price := p.Mark
		if price <= 0 {
			price = p.CostPrice
		}
		if price > 0 {
			held += price * float64(p.Qty)
		}
	}
	proj := held + orderCost
	pct := proj / total * 100
	if pct <= e.sectorLimitPct {
		return true, ""
	}
	reason := fmt.Sprintf("行业敞口超限: 行业=%s 预计市值 %.0f / 总资产 %.0f = %.1f%% > 上限 %.1f%%",
		target, proj, total, pct, e.sectorLimitPct)
	if e.sectorShadow {
		log.Printf("[paper] §C1 行业敞口影子命中（放行） %s: %s", code, reason)
		return true, ""
	}
	return false, reason
}

// New 创建模拟盘引擎并加载历史持久化数据。
// English: creates the paper engine and loads persisted state when present.
func New(cfg Config, path string) *Engine {
	if cfg.FixedAmount <= 0 {
		cfg.FixedAmount = 10000
	}
	if cfg.InitialCapital <= 0 {
		cfg.InitialCapital = 100000
	}
	e := &Engine{
		cfg:            cfg,
		cash:           cfg.InitialCapital,
		pools:          map[string]float64{"": cfg.InitialCapital}, // 默认单池（未分仓）兼容
		poolPerf:       make(map[string]*PoolPerf),
		poolMaxPos:     make(map[string]int),
		poolBuyRules:   make(map[string]*PoolBuyRule),
		poolGrp:        make(map[string]string),
		poolIR:         make(map[string]float64),
		poolDiscipline: make(map[string]poolDiscipline),
		extraPoolKeys:  make(map[string]bool),
		positions:      make(map[string]*Position),
		trimDone:       make(map[string]string),
		pendingExits:   make(map[string]*PendingExit), // §T1-PARTIAL 残量退出意图表（load 时按盘重建）
		reEntry:        newReEntryTracker(),           // §R1.4 再入场冷却追踪器（默认 0=不限制，仍构造以复用逻辑）
		lastBuyReject:  make(map[string]string),
		// §SHORT-3 做空侧初始化：shortCash 预算仅在 ShortCapital>0 时开设（默认 0=关闭）。
		// load() 若磁盘已有做空持仓则保留恢复值不覆盖（见 paper.go load）。
		// English: §SHORT-3 — seed the short budget only when ShortCapital>0; load() overrides with
		// persisted state when a short book already exists.
		shorts:        make(map[string]*ShortPosition),
		shortCash:     cfg.ShortCapital,
		shortFeeDone:  make(map[string]string),
		shortOpenSeen: make(map[string]string),
		path:          path,
	}
	if path != "" {
		e.load()
	}
	return e
}

// Enabled 返回模拟盘开关。
// §修复 FIX#6（2026-09-04）：持锁读——旧实现裸读 e.cfg.Enabled，与 UpdateConfig 有锁整字段写并发
// 构成数据竞态（go test -race 可复现）。走 Cfg() 副本即与写侧同锁。
// English: reports whether paper trading is enabled. §FIX#6 — read under the lock (via the Cfg() copy)
// to remove the data race with UpdateConfig's whole-struct write.
func (e *Engine) Enabled() bool { return e.Cfg().Enabled }

// HasFilled 返回引擎是否发生过任何成交（用于诊断净值曲线为何为空）。
// English: returns whether any fill has happened (used to diagnose empty equity curve).
func (e *Engine) HasFilled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hasFilled
}

// Path 返回持久化文件路径（用于诊断 paper.json 是否存在/大小/修改时间）。
// English: returns the persistence file path (for diagnostics: exists/size/mtime).
func (e *Engine) Path() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.path
}

// Cfg 返回当前生效的配置副本。
// English: returns a copy of the effective config.
func (e *Engine) Cfg() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// DynamicBudgetWithoutScale §DYN-GLOBAL 半开态识别：预算口径已经切到 dynamic（持仓数量上限等
// legacy 闸门全部退场），但动态缩放本身没有交付（账号总开关关着或仍影子）。
// 这个组合既拿不到 legacy 的数量约束，也拿不到 dynamic 的风险收缩——撮合按中性 scale=1.0
// 直接吃满 35% 额度，是所有口径里最激进的一档，且不会有任何报错。
// 出厂默认 enabled=true/budget_mode=dynamic/shadow=false 三者同向，只有账号级显式退路
// （enabled=false 或 shadow=true）而忘切 budget_mode 时才会落到这里，因此必须留痕。
// English: detects the half-open state — v3 dynamic budget active (legacy count caps retired) while the
// risk scale itself is not delivered (account switch off or still shadow). That combination keeps
// neither the legacy caps nor the dynamic shrink and fills at neutral scale 1.0, the most aggressive
// setting, silently.
func (c Config) DynamicBudgetWithoutScale() bool {
	if !strings.EqualFold(strings.TrimSpace(c.BudgetMode), "dynamic") {
		return false
	}
	return !(c.SizingEnabled && !c.SizingShadow)
}

// dynamicBudgetOn 动态预算是否开启（调用方通常已持 e.mu；仅读 cfg 标量，不额外加锁）。
// English: whether the v3 dynamic budget is active (callers generally already hold e.mu).
func (e *Engine) dynamicBudgetOn() bool {
	return strings.EqualFold(strings.TrimSpace(e.cfg.BudgetMode), "dynamic")
}

// clockNow 取"现在"：测试注入 nowFn 时用注入值，生产路径回落 time.Now。
// English: "now" — the injected test clock when set, time.Now in production.
func (e *Engine) clockNow() time.Time {
	if e.nowFn != nil {
		return e.nowFn()
	}
	return time.Now()
}

// warnBudgetWithoutScaleLocked §DYN-GLOBAL 半开态每日一次留痕（调用方须持 e.mu）。
// 只打日志、不改任何撮合口径：告警的意义是"这个账号此刻既没有 legacy 数量闸、也没有动态风险收缩"，
// 需要人工决定是把 budget_mode 退回 legacy，还是把 enabled/shadow 交付到位。
// English: once-per-day audit log for the half-open state (caller holds e.mu). Log only — no behavior
// change; a human decides whether to roll budget_mode back to legacy or deliver enabled/shadow.
func (e *Engine) warnBudgetWithoutScaleLocked(now time.Time) {
	if !e.cfg.DynamicBudgetWithoutScale() {
		return
	}
	day := cntime.DayOf(now) // §TZ1
	if e.budgetWarnDay == day {
		return
	}
	e.budgetWarnDay = day
	log.Printf("[paper] §DYN-GLOBAL 半开态：%s 预算口径=budget_mode=%s(dynamic) 但缩放未交付"+
		"(enabled=%v shadow=%v)——legacy 数量上限已退场、动态风险收缩未生效，本日撮合按中性 scale=1.0 全额执行",
		day, strings.TrimSpace(e.cfg.BudgetMode), e.cfg.SizingEnabled, e.cfg.SizingShadow)
}

// SetSingleStockCap §DYN-SIZE-v3 注入单票集中度上限（%）；<=0 时动态预算回退 35。
// English: injects the single-stock concentration cap (%; <=0 falls back to 35 in dynamic mode).
func (e *Engine) SetSingleStockCap(pct float64) {
	e.mu.Lock()
	e.singleStockLimitPct = pct
	e.mu.Unlock()
}

// UpdateConfig §修复 S2（2026-08-29）：后台改账户级模拟盘配置（费率/滑点/开关等）后热同步到运行实例，
// 无需重建引擎即可生效（此前配置在创建时一次性快照，改配置不反映到已运行账号）。
// English: S2 hot-sync — push updated account-level paper config into a running engine without rebuild.
func (e *Engine) UpdateConfig(cfg Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
	// §SHORT-3 预算后配：做空池尚未开设（现金 0 且无负债）且新配置给了预算 → 即时开设。
	// 已有空头持仓时不重播预算（磁盘恢复的 shortCash 为准，防资金凭空放大）。
	// English: §SHORT-3 — a later-funded short budget opens the pool lazily when it is still empty;
	// never replayed while shorts are open (persisted cash rules, no phantom capital).
	if e.shortCash == 0 && cfg.ShortCapital > 0 && len(e.shorts) == 0 {
		e.shortCash = cfg.ShortCapital
	}
}

// SetMirror 注入账本镜像回调（阶段1.2 两本账合一，由 engine/registry 在创建账号模拟盘时调用）。
// open 在新开仓后触发（手动买入含、加仓不含）；close 仅整笔清仓触发（部分减仓不含）。
// 回调内部不得再调用本引擎方法（避免锁递归）；nil 安全。
// English: injects the book-mirror callbacks (unified books; called by engine/registry when creating an
// account's paper engine). open fires after a NEW open (manual buys included, add-ons excluded); close
// fires only on full closes. Callbacks must not call back into this engine (no recursive locking); nil-safe.
func (e *Engine) SetMirror(open func(p Position), close func(code string, price, qty float64, reason string)) {
	e.mu.Lock()
	e.onOpen, e.onClose = open, close
	e.mu.Unlock()
}

// §SIGNAL_CONTROLLER 20260917：SetDiscipline（买入确认注入）随状态机一并移除——
// 买入持续性确认窗参数改由信号控制器 paper 通道从 rules.paper.discipline 直读（引擎侧装配）。
// English: SetDiscipline removed with the state machine; discipline is now read by the signal
// controller's paper policy builder from rules.paper.discipline.

// mirrorOpenLocked 触发开仓镜像（须持锁调用；副本传值防回调侧读到后续变更）。
// English: fires the open mirror (caller must hold the lock; passes a copy so the callback never sees later mutations).
func (e *Engine) mirrorOpenLocked(p *Position) {
	if e.onOpen == nil || p == nil {
		return
	}
	cp := *p
	e.onOpen(cp)
}

// mirrorCloseLocked 触发清仓镜像（须持锁调用）。
// English: fires the close mirror (caller must hold the lock).
func (e *Engine) mirrorCloseLocked(code string, price, qty float64, reason string) {
	if e.onClose == nil {
		return
	}
	e.onClose(code, price, qty, reason)
}

// persistedState 持久化状态快照（persist/load 共享）。
// 直接序列化 Engine 会因 cash/positions 等私有字段被 json 忽略而写入空对象，
// 导致 load 读到 cash=0、模拟盘永久无法买入（errCash）。故显式落盘状态字段。
// English: the persisted-state snapshot shared by persist/load. Marshaling the Engine directly would
// drop the private fields (cash/positions/…), writing an empty object and loading cash=0 so the paper
// book could never fill. State fields are therefore persisted explicitly.
type persistedState struct {
	Cash           float64 `json:"cash"`                      // 可用现金
	InitialCapital float64 `json:"initial_capital,omitempty"` // 自定义初始资金（reset 设置；空历史时保留，重启后恢复）
	// 自定义持仓上限：>0 生效，0=不设限（由资金自然决定）。不用 omitempty，
	// 保证 0（不设限）也明确落盘可见，避免"上限设置没固化"的排查困惑。
	// English: custom position cap — applies when > 0, 0 = unlimited (driven by the balance).
	// No omitempty so that 0 (unlimited) is explicitly written to disk, avoiding "cap not persisted" confusion.
	MaxPositions int                  `json:"max_positions"`
	Positions    map[string]*Position `json:"positions"`        // 持仓（key=代码）
	Trades       []Trade              `json:"trades"`           // 成交记录
	Orders       []Order              `json:"orders,omitempty"` // 订单生命周期（旧数据无此字段，load 兼容为空）
	Equity       []EquityPoint        `json:"equity"`           // 净值序列
	Realized     float64              `json:"realized"`         // 已实现盈亏
	// Pools 战法资金池（key=策略类型，""=其他池；Pools 与 PoolTypes 同时落盘，跨重启保留各池现金）。
	// 旧数据（无 Pools）兼容：load 时按现金建单池 {"": cash}，行为与分仓前完全一致。
	// English: strategy cash pools (key = strategy type, "" = the other pool; both Pools and PoolTypes are
	// persisted so per-pool cash survives restarts). Legacy data without Pools is compatible: load falls
	// back to the single pool {"": cash}, identical to pre-allocation behavior.
	Pools     map[string]float64 `json:"pools,omitempty"`
	PoolTypes []string           `json:"pool_types,omitempty"` // 池类型列表
	// PoolMaxPos 每池持仓上限（key=策略类型，0=该池不单独设限；跨重启保留）。
	// English: per-pool position caps (key = strategy type, 0 = no per-pool limit; survives restarts).
	PoolMaxPos map[string]int `json:"pool_max_pos,omitempty"`
	// PoolPerf 战法资金池持久化表现（累计买入成本/已实现盈亏；按买入后计数，卖出仍记本池）。
	// 旧数据（无 PoolPerf）兼容：load 时按池类型初始化空记录。
	// English: persisted per-pool performance (cumulative buy cost / realized P&L; counted after buy,
	// sells still attribute to the pool). Legacy data without PoolPerf initializes empty records on load.
	PoolPerf map[string]*PoolPerf `json:"pool_perf,omitempty"`
	// §Phase3 paper A/B 对照组：池级实验组标记（key=池 key，值=A/B/任意标签）。
	// 回测最优（A 组，实盘参数验证）与灰度候选（B 组，新战法观察）同关键盘对照。
	// English: Phase-3 paper A/B control — per-pool experiment group tag (key=pool key). The backtest
	// champion (group A, live-params validation) runs head-to-head with grayscale candidates (group B).
	PoolGrp map[string]string `json:"pool_grp,omitempty"`
	// §Phase4 IR 动态仓位：per-pool 参考 IR（信息比率，扫参排名行 IR 下发）。买入自动金额按
	// IR 缩放（高 IR 战法加大单笔预算、低 IR 缩仓），持久化跨重启保留。
	// English: Phase-4 IR-scaled position sizing — per-pool reference IR (issued from sweep rows); the
	// auto buy amount scales by IR (higher IR → larger per-trade budget, lower IR → smaller), persisted.
	PoolIR       map[string]float64      `json:"pool_ir,omitempty"`
	HasFilled    bool                    `json:"has_filled"`               // 是否已发生成交
	PoolBuyRules map[string]*PoolBuyRule `json:"pool_buy_rules,omitempty"` // 每池买入规则
	// §SHORT-3 融券做空侧持久化：负持仓表 / 做空池现金 / 做空已实现 / 利息计提与开仓去重表。
	// 旧文件无这些字段 → load 零值兼容（做空池未开设，行为与纯做多账本完全一致）。
	// English: §SHORT-3 — persisted short book (positions / margin pool cash / realized / fee & open
	// dedup). Legacy files without them load as "short book never opened", identical to long-only.
	Shorts        map[string]*ShortPosition `json:"shorts,omitempty"`
	ShortCash     float64                   `json:"short_cash,omitempty"`
	ShortRealized float64                   `json:"short_realized,omitempty"`
	ShortFeeDone  map[string]string         `json:"short_fee_done,omitempty"`
	ShortOpenSeen map[string]string         `json:"short_open_seen,omitempty"`
	// PendingExits §T1-PARTIAL（裁决三 W4）残量退出意图表：跨重启必须保留，否则"昨日裁决欠下的
	// 那部分卖出"会随进程重启静默消失（敞口留在仓上却再没人负责卖掉）。
	// 旧文件无此字段 → 空表，行为与本批之前完全一致。
	// English: residual exit intents; must survive restarts, otherwise a quit-and-restart silently drops the
	// obligation to finish yesterday's exit. Legacy files load as an empty table.
	PendingExits map[string]*PendingExit `json:"pending_exits,omitempty"`
	// TRounds §T-MODE（裁决四 W6/W7）做 T 回转账：跨重启必须保留，否则"昨日已开腿、今日该配对"
	// 的回转会在重启后凭空消失，机动仓（正 T）或已减掉的底仓（反 T）就没人负责收尾。
	// 旧文件无此字段 → 空表，行为与本批之前完全一致。
	// English: T-round ledger; must survive restarts, otherwise a round opened yesterday loses its
	// obligation to pair today and the intraday layer / trimmed base silently goes unaccounted for.
	TRounds []TRound `json:"t_rounds,omitempty"`
}

// tradeRetention 成交日志保留时长：3 个月，供战法效果/滑点/延迟分析。
// English: how long fill records are kept — 3 months, for strategy-effect / slippage / latency analysis.
const tradeRetention = 90 * 24 * time.Hour

// persist 将当前状态写入 JSON（幂等，失败仅记录日志）。
// 写入前先清理超过保留期（3 个月）的成交日志，避免无限膨胀。
// English: writes current state to the JSON file (best-effort; failures only log), trimming fills
// older than the retention window first so the log can't grow without bound.
func (e *Engine) persist() {
	if e.path == "" {
		return
	}
	e.trimTradesLocked()
	st := persistedState{
		Cash:           e.cash,
		InitialCapital: e.cfg.InitialCapital,
		MaxPositions:   e.cfg.MaxPositions,
		Positions:      e.positions,
		Trades:         e.trades,
		Orders:         e.orders,
		Equity:         e.equity,
		Realized:       e.realized,
		Pools:          e.pools,
		PoolTypes:      e.poolTypes,
		PoolMaxPos:     e.poolMaxPos,
		PoolPerf:       e.poolPerf,
		HasFilled:      e.hasFilled,
		PoolBuyRules:   e.poolBuyRules,
		PoolGrp:        e.poolGrp,
		PoolIR:         e.poolIR,
		// §SHORT-3 做空侧落盘（负持仓/池现金/已实现/计提与去重表）
		Shorts:        e.shorts,
		ShortCash:     e.shortCash,
		ShortRealized: e.shortRealized,
		ShortFeeDone:  e.shortFeeDone,
		ShortOpenSeen: e.shortOpenSeen,
		// §T1-PARTIAL 残量退出意图随状态落盘（重启后次日仍要把欠下的那部分卖掉）
		PendingExits: e.pendingExits,
		// §T-MODE 做 T 回转账随状态落盘（跨重启要能继续配对/收摊）
		TRounds: e.tRounds,
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("[paper] 序列化失败: %v", err)
		return
	}
	// §R0.1 原子写：temp+rename+fsync，崩溃/断电不再截断 JSON
	tmp := e.path + ".tmp"
	if werr := os.WriteFile(tmp, data, 0644); werr != nil {
		log.Printf("[paper] 写临时文件失败: %v", werr)
		return
	}
	if ff, ferr := os.Open(tmp); ferr == nil {
		ff.Sync()
		ff.Close()
	}
	if rerr := os.Rename(tmp, e.path); rerr != nil {
		log.Printf("[paper] 原子重命名失败: %v", rerr)
	}
}

// load 从 JSON 恢复状态。
// English: restores state from the JSON file.
func (e *Engine) load() {
	raw, err := os.ReadFile(e.path)
	if err != nil {
		return
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		log.Printf("[paper] 解析 %s 失败: %v", e.path, err)
		return
	}
	// 空/旧格式历史（无有效状态）：保留初始资金，避免 cash=0 导致永久无法买入。
	// English: empty/legacy history (no valid state) keeps the initial capital, avoiding a permanent
	// cash=0 that makes the paper book unable to fill.
	if st.Cash <= 0 && len(st.Positions) == 0 && len(st.Trades) == 0 {
		return
	}
	if st.InitialCapital > 0 {
		e.cfg.InitialCapital = st.InitialCapital
	}
	if st.MaxPositions > 0 {
		e.cfg.MaxPositions = st.MaxPositions
	}
	e.cash = st.Cash
	e.realized = st.Realized
	if st.Positions != nil {
		e.positions = st.Positions
	}
	e.trades = st.Trades
	e.equity = st.Equity
	// 恢复战法资金池：旧数据（无 Pools）按现金建单池（分仓前行为一致）；有 Pools 时还原各池现金，
	// 并按其求和兜底现金（历史现金字段与池和一致性修正）。
	// English: restore strategy pools — legacy data without Pools becomes the single pool {"": cash} (same as
	// before allocation); with Pools, per-pool cash is restored and the aggregate cash is reconciled to it.
	if st.Pools != nil && len(st.Pools) > 0 {
		e.pools = st.Pools
		e.poolTypes = st.PoolTypes
		sum := 0.0
		for _, v := range e.pools {
			sum += v
		}
		if sum > 0 {
			e.cash = sum
		}
	} else {
		e.pools = map[string]float64{"": e.cash}
	}
	// 恢复战法资金池持久化表现：旧数据无 PoolPerf 时按池类型初始化空记录。
	// English: restore persisted pool performance; legacy data without PoolPerf gets empty records.
	if st.PoolPerf != nil {
		e.poolPerf = st.PoolPerf
	}
	// 恢复每池持仓上限（旧数据无则留空，各池不单独设限）。
	// English: restore per-pool position caps (legacy data leaves them empty — no per-pool limits).
	if st.PoolMaxPos != nil {
		e.poolMaxPos = st.PoolMaxPos
	}
	// §R0.2 恢复 hasFilled 和 poolBuyRules
	e.hasFilled = st.HasFilled
	if st.PoolBuyRules != nil {
		e.poolBuyRules = st.PoolBuyRules
	}
	// §Phase3 恢复 A/B 对照组标记（旧数据无则初始化空表）
	// English: Phase-3 restore A/B group tags (legacy data initializes an empty table).
	if st.PoolGrp != nil {
		e.poolGrp = st.PoolGrp
	}
	// §Phase4 恢复每池参考 IR（旧数据无则初始化为空表）
	// English: Phase-4 restore per-pool reference IR (legacy data initializes an empty table).
	if st.PoolIR != nil {
		e.poolIR = st.PoolIR
	}
	// §C 从持久化的 poolTypes 还原规则细分池标记（fac_/pat_ 前缀）
	for _, k := range e.poolTypes {
		if IsRulePoolKey(k) {
			e.extraPoolKeys[k] = true
		}
	}
	// 恢复订单生命周期（旧数据无此字段 → 空列表，兼容）。
	// English: restore order lifecycle (legacy data without the field → empty list).
	if st.Orders != nil {
		e.orders = st.Orders
	}
	// §T1-PARTIAL 恢复残量退出意图（旧数据无此字段 → 保持 New 建好的空表）。
	// 逐条按持仓表自愈：持仓已不在账或数量已归零的意图直接丢弃，避免重启后拿一条指向空仓的
	// 欠账去下单（runPendingExitsLocked 也有同一道守卫，这里是让它无活可派）。
	// English: restore residual exits; intents whose position no longer exists are dropped on load so a
	// restart never carries an obligation against a flat book.
	if st.PendingExits != nil {
		e.pendingExits = make(map[string]*PendingExit, len(st.PendingExits))
		for code, pe := range st.PendingExits {
			if pe == nil || pe.WantQty <= 0 {
				continue
			}
			if p := e.positions[code]; p == nil || p.Qty <= 0 {
				continue
			}
			pe.Code = code
			e.pendingExits[code] = pe
		}
	}
	// §SHORT-3 恢复做空侧：存在持久化空头/池现金时以磁盘为准覆盖 New 的预算初值
	// （shortCash 在 New 用 cfg.ShortCapital 播种，重启后必须以实际余额恢复，防预算重置资金凭空放大）。
	// English: §SHORT-3 — restore the short book; persisted cash overrides the New-time budget seed
	// so restarts never inflate the margin pool back to its configured capital.
	if st.Shorts != nil {
		e.shorts = st.Shorts
	}
	if st.ShortCash > 0 || len(st.Shorts) > 0 || st.ShortRealized != 0 {
		e.shortCash = st.ShortCash
	}
	e.shortRealized = st.ShortRealized
	if st.ShortFeeDone != nil {
		e.shortFeeDone = st.ShortFeeDone
	}
	if st.ShortOpenSeen != nil {
		e.shortOpenSeen = st.ShortOpenSeen
	}
	// §T-MODE 恢复做 T 回转账（旧数据无此字段 → 空表，行为与本批之前完全一致）。
	// 不做逐条自愈：一条 open 态回转跨重启后仍要按"次日配对/收摊"处理，那正是它存在的意义；
	// 真的失去对象时（持仓已被清盘重置）rollTRoundsLocked 会在下一轮明确留痕收摊。
	// English: restore the T-round ledger. Open rounds are deliberately NOT pruned on load — carrying
	// them across a restart is the point; a round whose position disappeared is unwound with a reason
	// on the next round instead of vanishing silently.
	if st.TRounds != nil {
		e.tRounds = st.TRounds
	}
	// §ANCHOR-FIRST W3 存量锚点回填：加载完成、持仓就位后立刻补一次（幂等，只有真的补了才落盘）。
	// 放在 load 而不是启动期一次性迁移里：纸面账本是**按账号懒加载**的，启动时那些账本还没被读进来，
	// 挂在启动钩子上会漏掉所有"启动后才第一次被访问"的账号。
	// English: W3 legacy anchor backfill runs right after the positions are restored. It belongs here and
	// not in a startup migration because paper books are lazily loaded per account — a startup hook would
	// miss every book first touched after boot.
	// §DYN-T 口径收紧：加载时只回填**层账可证的首买价**；成本回落档必须走 owner 签字后的
	// BackfillAnchors（见 anchor_backfill.go 头注），否则"打开账本"就等于把摊薄成本写成锚点。
	if filled, skipped := e.BackfillAnchorsOnLoad(); filled > 0 || skipped > 0 {
		log.Printf("[paper] §DYN-T 加载期锚点回填 %s：首买价回填%d 无价可回填%d（成本档须走签字回填）",
			e.path, filled, skipped)
	}
	e.backfillPoolPerfLocked()
	e.trimTradesLocked()
}

// backfillPoolPerfLocked 兼容迁移：保证每个池的累计成本基准 ≥ 当前持仓成本合计。
// 旧数据（分仓性能统计上线前建的池）没有完整 PoolPerf.Cost：即使有少量新买入累计了成本，
// 历史持仓的成本从未记入池，导致分母偏小、浮盈/浮亏被异常放大（如 -150%）。
// 回填 = max(池已记成本, 当前持仓成本合计)，只补足缺失部分：
//   - 正常按新代码累计的池（Cost 已含全部买入，≥ 持仓成本）不受影响；
//   - 只有持仓成本未记入的旧池会补到持仓成本合计（作为部署资本的下限基准）。
//
// 调用方须持锁；有变更则落盘。
// English: compatibility migration — guarantees every pool's cumulative-cost basis ≥ the sum of its
// open positions' cost. Legacy pools (created before the pool-performance feature) lack a complete
// PoolPerf.Cost: even if a few post-upgrade buys accrued cost, the historical positions' cost was never
// recorded, shrinking the denominator and blowing up floating P&L into absurd percentages (e.g. -150%).
// Backfill = max(recorded cost, open-position cost sum), only topping up the shortfall:
//   - pools already accruing full cost under the new code (Cost ≥ position cost) are untouched;
//   - only legacy pools whose position cost was never recorded get topped up to their position-cost sum
//     (a floor basis for the actually deployed capital).
//
// Caller must hold the lock; persists on any change.
func (e *Engine) backfillPoolPerfLocked() {
	if len(e.positions) == 0 {
		return
	}
	if e.poolPerf == nil {
		e.poolPerf = make(map[string]*PoolPerf)
	}
	changed := false
	// 按池汇总当前持仓成本
	posCost := map[string]float64{}
	for _, p := range e.positions {
		posCost[p.StrategyType] += p.Cost
	}
	for k, cost := range posCost {
		perf := e.poolPerf[k]
		if perf == nil {
			e.poolPerf[k] = &PoolPerf{Cost: cost}
			changed = true
			continue
		}
		if perf.Cost < cost {
			perf.Cost = cost
			changed = true
		}
	}
	if changed {
		e.persist()
	}
}

// trimTradesLocked 清理超过保留期（3 个月）的成交记录（调用方须持锁）。
// 按成交时间过滤；trades 数组长度变化时原地压缩。供成交日志长期留档、分析用。
// English: drops fills older than the retention window (3 months); caller must hold the lock.
// Filters by fill time and compacts in place when the length changes. Keeps the trade log for analysis.
func (e *Engine) trimTradesLocked() {
	if len(e.trades) == 0 {
		return
	}
	cutoff := time.Now().Add(-tradeRetention)
	out := e.trades[:0]
	for _, t := range e.trades {
		if t.Time.After(cutoff) {
			out = append(out, t)
		}
	}
	if len(out) != len(e.trades) {
		e.trades = out
	}
	// 订单生命周期同窗口清理 + 绝对上限（防拒绝风暴撑爆文件）。
	// English: order lifecycle trimmed on the same window plus an absolute cap (reject storms can't bloat the file).
	if len(e.orders) > 0 {
		oout := e.orders[:0]
		for _, o := range e.orders {
			if o.CreatedAt.After(cutoff) {
				oout = append(oout, o)
			}
		}
		e.orders = oout
	}
	if capOrderLimit > 0 && len(e.orders) > capOrderLimit {
		e.orders = e.orders[len(e.orders)-capOrderLimit:]
	}
}

// capOrderLimit 订单记录绝对上限（超出丢最旧）。
// English: absolute order-record cap (oldest dropped beyond it).
const capOrderLimit = 2000

// recordOrderLocked 追加一条订单生命周期记录（须持锁调用；不主动 persist，由成交路径统一落盘）。
// English: appends one order-lifecycle record (caller holds the lock; no persist here — fill paths persist).
func (e *Engine) recordOrderLocked(o Order) {
	if o.ID == "" {
		o.ID = newOrderID()
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now()
	}
	e.orders = append(e.orders, o)
}

// recordBuyRejectLocked 记录一笔「自动撮合买入被拒」订单留痕，同一原因按码去重。
// §统一纪律·探针改为每轮重放全量活跃买入信号后，不可撮合信号（持仓上限/涨停/无行情/
// 池未启用/资金不足等）会每 5s 触达一次——同一原因只留痕一次，防止 orders 表与磁盘写被刷爆；
// 成功成交路径会清除该码记录（见 OnSignals fillLocked 成功后 delete）。
// English: records a rejected auto-buy order, deduplicated by (code, reason). With the probe re-feeding
// the full active buy set every round, an unfillable code would otherwise log a rejected order every 5s;
// the same reason is audited once. Cleared on a successful fill (see OnSignals).
func (e *Engine) recordBuyRejectLocked(o Order, reason string) {
	if e.lastBuyReject == nil {
		e.lastBuyReject = map[string]string{}
	}
	if e.lastBuyReject[o.Code] == reason {
		return
	}
	e.lastBuyReject[o.Code] = reason
	o.Reason = reason
	e.recordOrderLocked(o)
}

// OnSignals 消费一轮策略信号做自动撮合：仅做多 buy 信号，用实时快照价成交固定资金。
// 同一股票已持仓则跳过；达持仓上限跳过。行情缺失时跳过该信号（不伪造成交）。
// 记录信号价作辅助参照 + 信号→成交延迟。
// §SIGNAL_CONTROLLER 20260917：本函数只做执行——战法白名单/个股·板块黑名单/买入持续性
// 确认已由信号控制器在 registry 分发前按账号裁定（filterPaperAdmitted），
// 原内嵌 buyConfirm 状态机删除（假信号拦截不再依赖撮合引擎自带闸门）。
// English: auto-fills a round of ALREADY-ADMITTED strategy signals: long buys only, live snapshot
// price, fixed capital. Admission (whitelist/blacklists/persistence) moved to the signal controller.
func (e *Engine) OnSignals(sigs []combat_agent.Signal, quotes map[string]*data.StockInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// §修复 FIX#6（2026-09-04）：启用检查移入锁内——旧实现在锁外裸读 e.cfg.Enabled，
	// 与 UpdateConfig 有锁写并发构成数据竞态。
	// English: §FIX#6 — the enabled check now runs under the lock (previously read e.cfg outside
	// the lock, racing with UpdateConfig's whole-struct write).
	if !e.cfg.Enabled {
		return
	}
	now := time.Now()
	e.warnBudgetWithoutScaleLocked(now)
	for i := range sigs {
		s := sigs[i]
		// §SHORT-3 融券做空侧：做空战法通过信号（sell=持仓走弱卖出 / watch=纯做空机会）先落独立
		// 做空账本（幂等：已空/同日同码去重/池未开则整步 no-op）；持多仓的 sell 信号随后仍走
		// SellAction=close 平多（决策②），两本账互不挪用资金。
		// English: §SHORT-3 — bear-tactic pass signals first feed the isolated short book (idempotent:
		// already short / same-day dedup / pool not funded → no-op); held longs still close via the
		// SellAction branch below, the two books never share cash.
		if s.Direction == "做空" && combat_agent.IsShortTactic(s.StrategyType) {
			e.shortOpenFromSignalLocked(&s, quotes)
			if s.Action != "sell" {
				continue // watch 信号只做空开仓，不再进入做多/平多分支
			}
		}
		// 卖出信号自动成交（阶段1.1 全自动执行）：清仓/硬止盈/硬止损 → 全平；减仓类 → 半仓
		// （每码每日一次）。非本账持仓（如手动记录账）自然跳过。
		// English: auto-execute sell signals (full-auto) — 清仓/hard-TP/hard-SL close fully; trim-type
		// alerts halve the position (once per code per day). Codes not in this book are skipped naturally.
		if act := combat_agent.SellAction(s); act != "" {
			e.autoSellLocked(&s, act, quotes)
			continue
		}
		if s.Direction == "做空" || s.Action != "buy" {
			continue
		}
		// §SHORT-3 做多信号回补：该股存在融券空头时按现价买回平仓（T+1 未解禁则本轮跳过，
		// 下轮探针重试）；不影响本笔多头买入撮合。
		// English: §SHORT-3 — a bull buy signal covers any open short on the same code at the live
		// price (T+1-locked covers retry next round); the long fill proceeds independently.
		if _, isShort := e.shorts[s.Code]; isShort {
			e.shortCoverLocked(s.Code, 0, "做多信号回补平仓", quotes, now)
		}
		if _, held := e.positions[s.Code]; held {
			continue
		}
		// 自定义持仓上限：>0 时封顶；0（默认）不设限，持仓数由本金/现金余额自然决定。
		// §R4 修复：此前 return 掐断整个循环——达上限后排在后面的买入/止损卖出信号被整体丢弃。
		// 改为跳过该买单并留痕，继续处理后续信号。
		// English: R4 fix — the cap used to `return`, aborting the whole loop and silently dropping
		// every later buy AND stop-loss sell; now it skips this buy with an audit record and continues.
		if !e.dynamicBudgetOn() && e.cfg.MaxPositions > 0 && len(e.positions) >= e.cfg.MaxPositions {
			e.recordBuyRejectLocked(Order{Code: s.Code, Name: s.Name, Strategy: s.Strategy,
				StrategyType: s.StrategyType, Side: "buy", Kind: "自动撮合",
				SignalPrice: s.Price, Status: "rejected", CreatedAt: now},
				fmt.Sprintf("持仓数达上限(%d)", e.cfg.MaxPositions))
			continue
		}
		// 战法分池：按信号 StrategyType 归池；类型未启用（无对应池）时显式拒绝留痕。
		// §R10 修复：此前静默 continue 无任何痕迹，而 fillLocked 对缺失池读零值现金会误报
		// "可用资金不足"（线上实证：池改名过渡期 factor vs fac_1 一天误拒 49 笔）。
		// English: R10 fix — an unmatched StrategyType (e.g. renamed pool) is now rejected with an
		// audit record instead of being silently skipped or mis-read as a zero-cash pool.
		poolKey := s.StrategyType
		if poolKey != "" {
			if _, ok := e.pools[poolKey]; !ok {
				e.recordBuyRejectLocked(Order{Code: s.Code, Name: s.Name, Strategy: s.Strategy,
					StrategyType: poolKey, Side: "buy", Kind: "自动撮合",
					SignalPrice: s.Price, Status: "rejected", CreatedAt: now},
					fmt.Sprintf("战法池未启用(%s)", poolKey))
				log.Printf("[paper] 池键失配拒绝 %s(%s): 类型 %s 无对应启用池", s.Code, s.Name, poolKey)
				continue
			}
		}
		// §R3-3 P0-E 行情缺失一律拒绝撮合并留痕（对齐包头"不伪造成交"契约）：
		// 此前行情缺失回退信号价成交——①以陈旧信号价制造虚假成交，污染净值/滑点统计/
		// 盘后 paper_trades 研究落库；②下方涨停封板守卫依赖 quotes 命中，q==nil 时
		// 整体短路，涨停恰好断流的股票可被虚拟买入。现无有效实时价即 rejected 留痕，
		// 两个问题一并根除（能走到撮合的必然有行情，涨停守卫必然生效）。
		// English: R3-3 P0-E — no valid live quote → reject with an audit record (per the package
		// header contract "never fabricate fills"); also un-short-circuits the limit-up guard.
		q, hasQuote := quotes[s.Code]
		if !hasQuote || q == nil || q.Price <= 0 {
			e.recordBuyRejectLocked(Order{Code: s.Code, Name: s.Name, Strategy: s.Strategy,
				StrategyType: poolKey, Side: "buy", Kind: "自动撮合",
				SignalPrice: s.Price, Status: "rejected", CreatedAt: now},
				"行情缺失跳过(不伪造成交)")
			continue
		}
		price := q.Price
		// §R0.4b 涨停封板拒买（自动信号路径，与手动 Buy 同守卫）：
		// 封板股买单在现实中几乎无法排队成交——002412 实录：4 连板 09:30 首封，
		// 龙头识别 10:15 发信号被瞬间以涨停价撮合，制造"买后必涨"的虚假胜率。
		// 以实时涨幅近似封板判定；§R6 分板块幅度：主板/ST≈10%/5%、创业科创 20cm、北交 30%。
		if q := quotes[s.Code]; q != nil && q.ChangePct >= LimitUpPct(s.Code, s.Name) {
			e.recordBuyRejectLocked(Order{Code: s.Code, Name: s.Name, Strategy: s.Strategy,
				StrategyType: poolKey, Side: "buy", Kind: "自动撮合",
				SignalPrice: s.Price, Status: "rejected", CreatedAt: now},
				fmt.Sprintf("涨停封板无法买入(%.1f%%)", q.ChangePct))
			log.Printf("[paper] 涨停拒买(自动) %s(%s) %.1f%%——封板股买单不撮合", s.Code, s.Name, q.ChangePct)
			continue
		}
		// 每池持仓上限（与全局上限解耦，可自定义）：该池已持仓数 ≥ 池上限时跳过该信号。
		// 池上限 0 = 该池不单独设限（仅受全局上限约束）；Σ池上限 ≤ 全局上限（前端配置时守恒校验）。
		// English: per-pool position cap (decoupled from the global cap, customizable) — skip the signal
		// when the pool already holds >= its cap. 0 = no per-pool limit (global cap only); Σpool caps ≤
		// the global cap (conserved by the frontend config).
		if !e.dynamicBudgetOn() {
			if poolMax := e.poolMaxPos[poolKey]; poolMax > 0 && e.poolPositionCountLocked(poolKey) >= poolMax {
				continue
			}
		}
		// §SIGNAL_CONTROLLER 20260917：此处原"统一纪律·买入确认扳机"（探针+双确认窗，
		// discipline/buyConfirm 状态机）整体迁入信号控制器 paper 通道——到达本函数的信号均已经
		// 控制器准入（白名单/黑名单/持续性确认），本引擎只负责撮合执行。
		// English: the in-engine buy-confirm state machine moved to the signal controller; signals
		// reaching here are already admitted — pure execution.
		qVWAP, _ := data.VWAPOf(q)
		if err := e.fillLocked(poolKey, s.Code, s.Name, s.Strategy, s.Price, s.GeneratedAt, now, price, 0, s.Reason, s.Confidence, s.SizeScale, false, qVWAP); err != nil {
			log.Printf("[paper] 撮合失败 %s(%s): %v", s.Code, s.Name, err)
			// 订单留痕：买入被拒（现金不足/超上限/池上限/买不起一手）。
			// English: order audit — the buy was rejected (cash short / cap / pool cap / can't afford a lot).
			e.recordBuyRejectLocked(Order{Code: s.Code, Name: s.Name, Strategy: s.Strategy, StrategyType: poolKey,
				Side: "buy", Kind: "自动撮合", SignalPrice: s.Price, Status: "rejected", CreatedAt: now},
				fmt.Sprintf("%v", err))
			continue
		}
		// B5：策略信号买入是"非 T 成交"的主力，影子账按成交价/股数逐笔镜像（显式调用点）。
		if p := e.positions[s.Code]; p != nil {
			e.tShadowMirrorBuyLocked(s.Code, e.applyBuyPriceLocked(price), p.Qty, now)
		}
		// 买入拒绝去重表同步清除：该码已成交，后续同码不再按旧原因去重。
		// English: clear the reject-dedup entry — this code filled, later rejects must re-audit.
		if e.lastBuyReject != nil {
			delete(e.lastBuyReject, s.Code)
		}
		// 订单留痕：自动撮合全部成交。
		// English: order audit — the auto fill completed.
		e.recordOrderLocked(Order{Code: s.Code, Name: s.Name, Strategy: s.Strategy, StrategyType: poolKey,
			Side: "buy", Kind: "自动撮合", SignalPrice: s.Price, Price: price,
			Qty: e.positions[s.Code].Qty, Status: "filled", Reason: s.Reason, CreatedAt: now})
		// 记录信号携带的 ATR14（镜像写 report 账时供 ATR 动态止损距离计算），随后触发开仓镜像。
		// English: record the signal's ATR14 (used by the report-book mirror for the ATR dynamic stop
		// distance), then fire the open mirror.
		if p, ok := e.positions[s.Code]; ok {
			p.ATR = s.ATR
			e.mirrorOpenLocked(p)
		}
	}
	e.persist()
}

// autoSellLocked 卖出信号自动成交（阶段1.1，须持锁调用）：
//   - act=="close"：清仓（清仓/硬止盈/硬止损），收益回池并镜像关闭 report 账记录；当日买入的部分
//     卖不掉时**减量成交**，剩余量登记成次日无条件执行的残量意图（§T1-PARTIAL 裁决三）；
//   - act=="trim" ：半仓减仓（减仓/情绪退潮/利空归因），每码每交易日至多一次（trimDone 去重），
//     目标量按可卖量钳位后取整手。
//
// 行情缺失时跳过（避免以信号价误成交）；非本账持仓自然 no-op；AutoSell=false 时整体停用。
// English: auto-executes a sell signal (caller must hold the lock): "close" sells the position (proceeds
// back to pool + report-book mirror close) and, when part of it is T+1-locked, fills the sellable part and
// registers the residual as a next-day intent; "trim" halves it at most once per code per trading day
// (trimDone dedup), with the target capped by the sellable qty and rounded to whole lots. Skips on missing
// quotes; no-op for codes not in this book; disabled entirely when AutoSell is off.
func (e *Engine) autoSellLocked(s *combat_agent.Signal, act string, quotes map[string]*data.StockInfo) {
	if !e.cfg.AutoSell {
		return
	}
	p, held := e.positions[s.Code]
	if !held || p == nil {
		return
	}
	var price float64
	if q, ok := quotes[s.Code]; ok && q != nil && q.Price > 0 {
		price = q.Price
	} else {
		return // 无实时价不自动成交（宁可不卖也不以错误价格记账）
	}
	reason := "自动" + s.AlertType
	if reason == "自动" {
		reason = "自动卖出"
	}
	mkOrder := func(status string, qty int) Order {
		return Order{Code: s.Code, Name: s.Name, Strategy: p.Strategy, StrategyType: p.StrategyType,
			Side: "sell", Kind: reason, SignalPrice: s.Price, Qty: qty, Status: status,
			Reason: s.Reason, CreatedAt: time.Now()}
	}
	switch act {
	case "close":
		// §T1-PARTIAL（裁决三）：清仓从"整仓一把梭"改成"先卖可卖部分"。Sellable=0 时保持既有语义
		// ——拒绝留痕、不落假 filled（§GAP1.9），也**不登记残量意图**（方案 §4.3 第 4 行：整仓都卖不
		// 掉属于"退出裁决撞 T+1 全拦"，计划要求维持现状，只登记减量成交后的那截残量）。
		// English: a close now sells the sellable part first. When nothing is sellable the old behaviour
		// stands (rejected audit row, no fake fill, no intent); an intent is recorded only for the
		// residual left after a partial close.
		want := p.Qty
		sold, err := e.sellPositionLocked(p, price, reason)
		if err != nil {
			e.recordOrderLocked(func() Order {
				o := mkOrder("rejected", want)
				o.Reason = "自动清仓被拒:" + err.Error()
				return o
			}())
			log.Printf("[paper] 自动清仓被拒 %s(%s): %v", s.Code, s.Name, err)
			return
		}
		if sold >= want {
			e.recordOrderLocked(func() Order { o := mkOrder("filled", sold); o.Price = price; return o }())
		} else {
			e.recordOrderLocked(func() Order { o := mkOrder("partial", sold); o.Price = price; return o }())
			// 残量登记为次日无条件执行的退出意图（Q6：退出是风险裁决，敞口挂着比卖早了更贵）。
			e.recordPendingExitLocked(s.Code, want-sold, pendingExitClose, reason, time.Now())
		}
		// B5：卖出裁决执行是非 T 成交（影子账只跳过 T 腿，不跳过退出）。
		e.tShadowMirrorSellLocked(s.Code, e.applySellPriceLocked(price), sold, time.Now())
	case "trim":
		today := cntime.DayCompactOf(time.Now()) // §TZ1
		if e.trimDone[s.Code] == today {
			return
		}
		baseHalf := p.Qty / 2 / 100 * 100 // 半仓取整手（A 股一手 100 股）
		if baseHalf <= 0 {
			return // 持仓不足两手无法半仓减仓，等清仓类信号处理
		}
		// 半仓目标既 ≤ 可卖量又取整手：不可卖的那截不进目标量，避免"目标 500 股、实卖 300 股"
		// 这种由钳位产生的假残量。
		// English: the trim target is capped by the sellable qty and rounded down to whole lots, so the
		// residual is a real T+1 residual rather than an artefact of clamping.
		half := baseHalf
		sellable := e.sellableQtyLocked(p, time.Now())
		if sellable < half {
			half = sellable / 100 * 100
		}
		if half <= 0 {
			// 可卖量不足一手（多半是当日买入被 T+1 锁死）：减仓做不了，但**必须留痕**——
			// §GAP1.9 同款纪律：被拒不落假 partial 订单，也不置 trimDone（当日真正可减仓的窗口
			// 不能被永久跳过）。静默返回会让用户看到"信号发了却没动静"。
			// English: not enough sellable shares for a whole lot — the trim cannot run, but it leaves a
			// rejected audit row (no fake "partial") and does not set trimDone.
			why := "T+1限制：当日买入次日方可卖出"
			if sellable > 0 {
				why = fmt.Sprintf("可卖量 %d 股不足一手，无法按整手减仓", sellable)
			}
			e.recordOrderLocked(func() Order {
				o := mkOrder("rejected", baseHalf)
				o.Reason = "自动减仓被拒:" + why
				return o
			}())
			log.Printf("[paper] 自动减仓被拒 %s(%s): %s", s.Code, s.Name, why)
			return
		}
		// §GAP1.9 修复：trim 被拒时同样不落假 partial 订单，且不得置 trimDone——
		// 否则当日真正可减仓的窗口（如次日）被永久跳过。
		// English: §GAP1.9 fix — a rejected trim must not record a fake "partial" order nor set trimDone,
		// which would permanently skip later valid trim windows on the same day.
		sold, err := e.sellQtyLocked(p, price, half, reason)
		if err != nil {
			e.recordOrderLocked(func() Order {
				o := mkOrder("rejected", half)
				o.Reason = "自动减仓被拒:" + err.Error()
				return o
			}())
			log.Printf("[paper] 自动减仓被拒 %s(%s): %v", s.Code, s.Name, err)
			return
		}
		e.recordOrderLocked(func() Order { o := mkOrder("partial", sold); o.Price = price; return o }())
		e.trimDone[s.Code] = today
		// B5：减仓同属卖出裁决执行，影子账镜像。
		e.tShadowMirrorSellLocked(s.Code, e.applySellPriceLocked(price), sold, time.Now())
		// 残量槽与 trimDone 互不吞（3.3）：本日的减仓槽已被这半仓用掉，残量走独立意图队列次日执行。
		if sold < half {
			e.recordPendingExitLocked(s.Code, half-sold, pendingExitTrim, reason, time.Now())
		}
	}
}

// poolPositionCountLocked 返回某池当前持仓数（须持锁调用）。
// English: returns the current position count of a pool (caller must hold the lock).
func (e *Engine) poolPositionCountLocked(poolKey string) int {
	n := 0
	for _, p := range e.positions {
		if p.StrategyType == poolKey {
			n++
		}
	}
	return n
}

// markPriceLocked 持仓估值价：实时标记优先，缺失回落成本价。
// English: position mark price — live mark first, cost price as fallback.
func (e *Engine) markPriceLocked(p *Position) float64 {
	if p.Mark > 0 {
		return p.Mark
	}
	return p.CostPrice
}

// poolEquityLocked 池权益 = 池现金 + 该池持仓市值（动态预算基数）。
// English: pool equity = pool cash + market value of that pool's positions (v3 budget base).
func (e *Engine) poolEquityLocked(poolKey string) float64 {
	eq := e.pools[poolKey]
	for _, p := range e.positions {
		if p.StrategyType != poolKey {
			continue
		}
		if px := e.markPriceLocked(p); px > 0 {
			eq += px * float64(p.Qty)
		}
	}
	return eq
}

// totalPaperEquityLocked 整个模拟盘权益（动态预算集中度分母）：做多现金+持仓市值+做空侧权益。
// English: whole paper-book equity (v3 concentration denominator): long cash + positions + short book.
func (e *Engine) totalPaperEquityLocked() float64 {
	eq := e.cash + e.shortEquityLocked()
	for _, p := range e.positions {
		if px := e.markPriceLocked(p); px > 0 {
			eq += px * float64(p.Qty)
		}
	}
	return eq
}

// dynamicPaperQtyLocked 模拟盘动态预算：池权益 × 缩放，再受全盘 35% 行业/单票额度与池现金约束。
// 返回 (股数, 跳过原因)；跳过原因为空表示成功。调用方须持 e.mu。
// English: paper dynamic budget — pool equity × scale, capped by whole-book 35% industry/stock
// rooms and pool cash. Caller must hold e.mu.
func (e *Engine) dynamicPaperQtyLocked(poolKey, code string, price, sizeScale float64) (int, string) {
	base := e.poolEquityLocked(poolKey)
	capEq := e.totalPaperEquityLocked()
	if base <= 0 || capEq <= 0 || price <= 0 {
		return 0, "池权益/价格不可得"
	}
	cash := e.pools[poolKey]
	if cash <= 0 {
		return 0, "池现金不足"
	}
	// k = 信号带下来的合成因子（BuyScale = MS×SS）。0 与 1.0 都表示"没有缩放"（中性）：
	// 0 是 Signal 的零值（未定权），1.0 是显式中性——两者必须同义，否则"没定权"会被读成
	// "因子全灭"，单笔从 30% 掉到 10%（flow_scale_test 钉的就是这条）。
	k := 1.0
	if e.cfg.SizingEnabled && !e.cfg.SizingShadow && sizeScale > 0 {
		k = sizeScale
	}
	// §B0 单笔占比：与实盘同一实现点（signalctl.PerTradePctOf），**不再** `perTradePct × k`。
	// 旧的 `0.10 × MS×SS` 是二次计权（eff 里已经含 k），同一条信号纸面 6.7%、实盘 23%，
	// 两本账的金额差 3 倍而各自的收益曲线都"正常"——那种漂移无法从读数上发现。
	perTradeLo, perTradeHi := perTradeBandOf(e.sizingCtx)
	perTradePct := signalctl.PerTradePctOf(k, perTradeLo, perTradeHi)
	e.sizingCtx.LastK = k // 只服务展示面（面板回显"这一轮实际按多少买"）
	ind := ""
	if e.industryOf != nil {
		ind = strings.TrimSpace(e.industryOf(code))
	}
	indVal, stockVal := 0.0, 0.0
	for _, p := range e.positions {
		px := e.markPriceLocked(p)
		if px <= 0 || p.Qty <= 0 {
			continue
		}
		v := px * float64(p.Qty)
		if p.Code == code {
			stockVal += v
		}
		if ind != "" && strings.TrimSpace(e.industryOf(p.Code)) == ind {
			indVal += v
		}
	}
	indCap := e.sectorLimitPct
	if indCap <= 0 {
		indCap = 35
	}
	stockCap := e.singleStockLimitPct
	if stockCap <= 0 {
		stockCap = 35
	}
	// §DYN-SIZE-v3 §5.6：单票/行业上限不得高于 100（配置可绕过前端直接写入，这里钳死）。
	if indCap > 100 {
		indCap = 100
	}
	if stockCap > 100 {
		stockCap = 100
	}
	// §DYN-SIZE v4：模拟盘与实盘同一套目标口径（方案 §1.4）。
	//	目标 Tgt = 池权益 × min(MS, 天花板)；战略当前 = 池持仓市值 − 正T买腿 + 反T卖腿
	//	剩余 Room = max(0, Tgt − 战略当前)；本单再与单笔基准、行业/单票额度、池现金取小。
	// 逐票的个股因子只进 scale（sizeScale），**不进目标**：否则同一轮里 A 票质量高能抬全池目标。
	poolHold := base - cash // 池权益 − 池现金 ＝该池持仓市值（与 poolEquityLocked 同源，不另取快照）
	if poolHold < 0 {
		poolHold = 0
	}
	curStrat := poolHold - e.tOpenBuyValueLocked() + e.tOpenSellValueLocked()
	if curStrat < 0 {
		curStrat = 0
	}
	ms := e.sizingCtx.MarketScale
	ceiling := e.sizingCtx.Ceiling
	if ceiling <= 0 || ceiling > 1 {
		ceiling = 1.0
	}
	targetRatio := ms
	if targetRatio > ceiling {
		targetRatio = ceiling
	}
	room := base*targetRatio - curStrat
	if room < 0 {
		room = 0
	}
	// 影子期 / 关键输入缺失 / engine 未注入 MS ⇒ **不收紧**（只留痕）：观察期不是执法期，
	// 未注入时保持 v3 行为可回归（老测试与纯内存账本走这条路）。
	roomSet := e.cfg.SizingEnabled && !e.cfg.SizingShadow && !e.sizingCtx.MissingKey && ms > 0
	// 反 T 冻结回购款是专款，战略买入不得动用（§8.3 纪律 3）。
	avail := cash - e.tFrozenCashLocked()
	if avail < 0 {
		avail = 0
	}
	res := ordbudget.Compute(ordbudget.Inputs{
		Base:      base,
		CapEquity: capEq,
		// §B0：Scale 直接就是 eff（含 k），**不许**再乘 k——那是二次计权（见上面 perTradePct 的注释）。
		Scale:             perTradePct,
		Price:             price,
		Cash:              avail,
		Lot:               ordbudget.DefaultLot,
		IndustryCapPct:    indCap,
		SingleStockCapPct: stockCap,
		IndustryValue:     indVal,
		StockValue:        stockVal,
		IndustryKnown:     ind != "",
		FeeBufferPct:      0,
		TargetRoom:        room,
		TargetRoomSet:     roomSet,
	})
	if res.SkipReason != "" {
		return 0, res.SkipReason
	}
	return res.Qty, ""
}

// paperSizingCtx engine 每轮注入的定权上下文（见 Engine.sizingCtx）。
type paperSizingCtx struct {
	MarketScale float64 // 市场组读数（目标仓位与 T 额度由它派生）；0=未注入
	// PerTradeLo/PerTradeHi 单笔占比的动态区间（§B0：owner 2026-10-27 定 10%~30%）。
	// 生效值 = signalctl.PerTradePctOf(k, lo, hi)，k = 信号上带下来的 BuyScale（= MS×SS）。
	// 为什么纸面也要这条：两本账的单笔口径必须同源，否则纸面按 10%×MS×SS、实盘按 23%~26%，
	// 同一条信号在两本账里买到差 3 倍的金额——纸面读数就无法用来解释实盘（§11.3 的"两套口径"禁忌）。
	PerTradeLo float64
	PerTradeHi float64
	// LastK 最近一笔信号带下来的合成因子 k（只服务展示面：面板要回显"这一轮实际按多少买"）。
	// 0 = 还没见过任何带缩放的信号，展示面按中性 k=1.0 算并标注"最近一笔"。
	LastK      float64
	Ceiling    float64 // 目标仓位天花板（默认 1.00）
	MissingKey bool    // 关键市场读数缺失（目标封顶半仓、日内 T 关闭）
}

// perTradeBandOf 归一单笔占比区间的两端（纸面唯一实现点，下单路径与展示面共用）。
//
// 归一规则与实盘 dynamicBuyQty 逐字同参：`lo<=0 || lo>1 → 0.10`、`hi<=0 → 0.30`。
// 为什么 >1 回落 0.10 而不是钳到 0.50：把"10%"写成"10"是手滑，按 0.50 执行等于把一次手滑
// 放大成"单笔半个账户"——资金参数的手滑必须往**小**的方向兜，这与 PerTradePctOf 内部的
// 硬夹（≤0.50）分工不同：那个夹是防上游算出离谱值，这里的回落是防人填错。
// hi<lo 时抬 hi 到 lo（区间反了不把单笔缩成 0，与 PerTradePctOf 同向）。
// English: normalizes the per-trade band exactly like the live path; a typo'd lo (>1) falls back
// to 0.10 rather than the 0.50 hard clamp, because a funds typo must fail small.
func perTradeBandOf(ctx paperSizingCtx) (lo, hi float64) {
	lo, hi = ctx.PerTradeLo, ctx.PerTradeHi
	if lo <= 0 || lo > 1 {
		lo = 0.10
	}
	if hi <= 0 {
		hi = 0.30
	}
	if hi > 1 {
		hi = 0.50 // 硬边界：单笔不过半仓（与 PerTradePctOf 的夹取同值）
	}
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// SetSizingContext 注入本轮定权上下文（engine 的纸面通道每轮调用）。
// 刻意不持久化：它描述的是"这一轮的市场"，不是"用户的配置"。
// lo/hi 是单笔占比的动态区间；传 0 表示该端缺失，由 perTradeBandOf 回落到出厂值（10% / 30%）。
func (e *Engine) SetSizingContext(ms, perTradeLo, perTradeHi, ceiling float64, missingKey bool) {
	e.mu.Lock()
	e.sizingCtx = paperSizingCtx{MarketScale: ms, PerTradeLo: perTradeLo, PerTradeHi: perTradeHi,
		Ceiling: ceiling, MissingKey: missingKey}
	e.mu.Unlock()
}

// PositionPoolAndMark 返回该持仓的战法池键与当前估值价（供 T 的"转战略/回补"走战略预算通道）。
// 池键直接用 StrategyType：poolEquityLocked 就是按它归池的，另造一套映射必然漂移。
func (e *Engine) PositionPoolAndMark(code string) (poolKey string, price float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.positions[code]
	if p == nil {
		return "", 0
	}
	return p.StrategyType, e.markPriceLocked(p)
}

// StrategicBuyRoom 战略闸门在模拟盘的读数：这只票此刻还能按战略口径买多少股（0＝没有空间）。
// 做 T 的两处都要走它（§8.5.3）：正 T 转战略加仓、反 T 尾盘回补——"转战略"不是一次改状态，
// 而是一次真实的战略下单提案，必须吃同一套单笔/目标/行业单票帽。
// English: the strategic room a T leg may consume; conversion is a real order proposal, never a
// status flip that sidesteps the caps.
func (e *Engine) StrategicBuyRoom(poolKey, code string, price float64) (int, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dynamicPaperQtyLocked(poolKey, code, price, 1.0)
}

// fillLocked 按给定价格撮合一笔买入（调用方须持锁）。返回错误信息。
// poolKey 为该笔买入所属的战法资金池（""=其他/手动池）；扣款只扣对应池预算，池与池互不侵占。
// qty > 0 为显式手数（手动买入/加仓，超出池内资金直接失败）；
// qty <= 0 时按 FixedAmount 自动算整手，现金不足按池内剩余现金缩减到整手（不足一手跳过）。
// English: fills a buy at the given price (caller must hold the lock); returns an error on failure.
// poolKey is the strategy cash pool this buy debits ("" = the other/manual pool); pools never invade
// each other. qty > 0 is an explicit lot count (manual buy/add, fails when exceeding the pool balance);
// qty <= 0 auto-sizes to FixedAmount in whole lots, shrinking to the largest affordable lot on a
// shortfall (skipping below one lot).
func (e *Engine) fillLocked(poolKey, code, name, strategy string, signalPrice float64, signalAt, now time.Time, price float64, qty int, reason string, confidence, sizeScale float64, manual bool, marketVWAP float64) error {
	// 每池持仓上限（与全局上限解耦，可自定义）：该池新开仓达到池上限即拒绝。
	// 池上限 0 = 该池不单独设限；加仓（已持仓）不在此路径（走 addToPositionLocked）。
	// English: per-pool position cap (decoupled from the global cap, customizable) — a new open in this
	// pool is rejected once the pool reaches its cap. 0 = no per-pool limit; add-ons (already held) don't
	// pass through here (they use addToPositionLocked).
	// §DYN-SIZE-v3：dynamic 模式下持仓数量上限退场，仓位由 35% 额度/现金决定。
	if !e.dynamicBudgetOn() {
		if poolMax := e.poolMaxPos[poolKey]; poolMax > 0 && e.poolPositionCountLocked(poolKey) >= poolMax {
			return errMaxPos
		}
	}
	// §分仓纪律引擎·预检查（不需要cost）：日限次数+冷却+评分门槛。
	// §R7 MinScore 真正生效：confidence 由信号传入；manual=true（手动买入/加仓）= 用户自主决策放行。
	if reason := e.checkPoolDisciplinePre(poolKey, confidence, manual); reason != "" {
		log.Printf("[paper] 分仓纪律拒绝 %s→%s: %s", strategy, code, reason)
		return errPoolDiscipline
	}
	// §R1.4 再入场冷却（接线生效，此前 reEntryTracker 为死代码）：清仓后冷却期内禁止同票再买入。
	// 逻辑来自 paper_r12.go；参数取配置 ReentryCooldownMin（0=不限制，兼容旧行为）。
	// 手动买入与自动撮合均经 fillLocked，故两者均受此闸门约束。
	if e.reEntry != nil && e.cfg.ReentryCooldownMin > 0 {
		if !e.reEntry.canReEnter(code, e.cfg.ReentryCooldownMin, now) {
			elapsed := now.Sub(e.reEntry.lastCloseAt(code)).Minutes()
			log.Printf("[paper] 再入场冷却拒绝 %s(%s): 清仓后 %.0f/%.0f 分钟内禁止回买",
				code, name, elapsed, float64(e.cfg.ReentryCooldownMin))
			return fmt.Errorf("再入场冷却中(%.0f/%.0f分钟)", elapsed, float64(e.cfg.ReentryCooldownMin))
		}
	}
	// §DYN-SIZE-v3：dynamic 模式忽略显式股数与固定金额，统一走池权益×缩放；legacy 保持旧路径。
	explicitQty := qty > 0 && !e.dynamicBudgetOn()
	if e.dynamicBudgetOn() {
		q, why := e.dynamicPaperQtyLocked(poolKey, code, price, sizeScale)
		if q <= 0 {
			if why == "" {
				why = "动态预算不足"
			}
			return fmt.Errorf("动态仓位: %s", why)
		}
		qty = q
	} else if !explicitQty {
		// §Phase4 IR 动态仓位：自动买入金额按该池参考 IR 缩放（高 IR 加大单笔预算、低 IR 缩仓）。
		// English: Phase-4 IR-scaled sizing — the auto amount scales by the pool's reference IR
		// (higher IR → larger per-trade budget, lower IR → smaller).
		amount := e.cfg.FixedAmount * e.applyPoolIRLocked(poolKey)
		// §DYN-SIZE 动态仓位缩放（signal.SizeScale，由信号控制器裁定后写回；0/1.0=不缩放）：
		// 与 live 通道同源同值（方案 §5「通道一致」测试锁）。缩后仍走下方整手/现金降档。
		// English: §DYN-SIZE buy scale written back by the signal controller (0/1.0 = no scaling),
		// same value as the live leg; the lot/cash degradation below still applies afterwards.
		if sizeScale > 0 && sizeScale != 1.0 {
			amount *= sizeScale
		}
		qty = int(amount/price/100) * 100
	}
	if qty <= 0 {
		return errLotTooSmall // 一手都买不起（不足 A 股一手）
	}
	// §R0.5 滑点模型：SlippageBps > 0 时买入价上浮；0 = 不启用（兼容既有测试/用户）
	if e.cfg.SlippageBps > 0 {
		price = price * (1 + e.cfg.SlippageBps/10000.0)
	}

	cost := float64(qty) * price
	// §R1 手续费入账（修复：此前 fee 算完即丢，佣金从未扣减现金）：买入总支出=本金+佣金，
	// 池扣款与持仓成本均按含费口径。English: the buy fee is now actually debited — total outlay =
	// principal + commission; pool debit and position cost are fee-inclusive.
	fee := e.buyFeeLocked(cost)

	// §分仓纪律引擎·预算检查（需要cost）：日内花费不超池资金配比（按本金口径计数）。
	// §DYN-SIZE-v3：dynamic 模式下每日预算退场，预算只由池权益×缩放与现金决定。
	if !e.dynamicBudgetOn() {
		if reason := e.checkPoolBudget(poolKey, cost); reason != "" {
			log.Printf("[paper] 分仓预算拒绝 %s→%s: %s", strategy, code, reason)
			return errPoolDiscipline
		}
	}
	// §C1 行业/概念敞口：在最终股数/金额确定后、扣款前检查；纸面与实盘同口径。
	if ok, why := e.checkSectorExposureLocked(code, cost); !ok {
		log.Printf("[paper] §C1 行业敞口拒绝 %s(%s): %s", code, name, why)
		return fmt.Errorf("%s", why)
	}
	if ok, why := e.checkConceptExposureLocked(code, cost); !ok {
		log.Printf("[paper] §C1-V2 概念敞口拒绝 %s(%s): %s", code, name, why)
		return fmt.Errorf("%s", why)
	}
	pool := e.pools[poolKey]
	if cost+fee > pool {
		if explicitQty {
			return errCash // 手动指定手数超出池内资金（含费），不静默缩减
		}
		// 自动金额现金不足：按池内剩余现金（预留佣金）缩减到整手；缩减后仍买不起一手则跳过
		// §修复 P2#20：预留费须同时覆盖最低佣金——旧实现只按费率预留（pool/(1+rate)），
		// MinCommission（默认5元）按成交额封底、小额单实际费用更高，缩减后 cost+fee 仍可
		// 超出 pool，导致池现金变负（分仓账面被穿透）。
		// English: P2#20 — the fee headroom must also cover the minimum commission. The old shrink
		// reserved only the rate-based fee (pool/(1+rate)); MinCommission (5元 default) floors the fee
		// for small lots so an over-small pool could still end up with cost+fee > pool — cash goes negative.
		avail := pool
		if e.cfg.CommissionRate > 0 && e.cfg.CommissionRate < 1 {
			avail = pool / (1 + e.cfg.CommissionRate)
		}
		if e.cfg.MinCommission > 0 && pool-e.cfg.MinCommission < avail {
			avail = pool - e.cfg.MinCommission // 最低佣金封底预留（买不起一手以下再降）
		}
		qty = int(avail/price/100) * 100
		if qty <= 0 {
			return errCash
		}
		cost = float64(qty) * price
		fee = e.buyFeeLocked(cost)
		if cost+fee > pool {
			return errCash // 收尾守卫：整手缩到最小后仍不够含费支出 → 放弃（绝不透支池现金）
		}
	}
	e.pools[poolKey] = pool - cost - fee
	e.cash -= cost + fee
	e.hasFilled = true             // §反馈修复：首笔成交起才开始记净值曲线
	e.recordPoolBuy(poolKey, cost) // §分仓纪律：记录买入事件（冷却/日限/预算计数）
	// 买入后计数：累计买入成本计入本池（卖出不减，收益仍记该池）。按本金口径（不含费）。
	// English: counted after buy — the buy principal accumulates to the pool (never reduced on sells,
	// so P&L stays attributed to the pool).
	if e.poolPerf[poolKey] == nil {
		e.poolPerf[poolKey] = &PoolPerf{}
	}
	e.poolPerf[poolKey].Cost += cost
	totalCost := cost + fee
	e.positions[code] = &Position{
		Code:         code,
		Name:         name,
		Strategy:     strategy,
		StrategyType: poolKey,
		Qty:          qty,
		CostPrice:    totalCost / float64(qty),
		Cost:         totalCost,
		SignalPrice:  signalPrice,
		SignalAt:     signalAt,
		FilledAt:     now,
		Mark:         price,
		// §ANCHOR-FIRST 建仓即冻结的锚点：与当刻 CostPrice 同值（含费），此后没有任何路径改写它。
		AnchorPrice:  totalCost / float64(qty),
		AnchorSource: anchorSourceFirstLot, // §DYN-T 建仓即首买价，来源可证（回填只处理 AnchorPrice=0 的存量行）
		AnchorAt:     now,
		// §T1-PARTIAL 建仓即建层账：一笔建仓 = 一批，含费每股成本与锚点同源。
		Lots: []PositionLot{{Qty: qty, Price: totalCost / float64(qty), FilledAt: now}},
		// §C2 VWAP 偏差：只在采集到时计算；缺失保持 0。
		MarketVWAP:     marketVWAP,
		VWAPPremiumPct: vwapPremiumPct(price, marketVWAP),
	}
	latency := int64(0)
	if !signalAt.IsZero() {
		latency = int64(now.Sub(signalAt).Seconds())
		if latency < 0 {
			latency = 0
		}
	}
	e.trades = append(e.trades, Trade{
		Code:           code,
		Name:           name,
		Strategy:       strategy,
		StrategyType:   poolKey,
		Side:           "buy",
		Price:          price,
		SignalPrice:    signalPrice,
		Qty:            qty,
		Amount:         cost,
		Time:           now,
		LatencySec:     latency,
		Fee:            fee,
		Reason:         reason,
		MarketVWAP:     marketVWAP,
		VWAPPremiumPct: vwapPremiumPct(price, marketVWAP),
	})
	log.Printf("[paper] 模拟买入 %s(%s) %d股 @%.2f 信号价%.2f 延迟%ds 费%.2f",
		code, name, qty, price, signalPrice, latency, fee)
	return nil
}

// Buy 手动按实时价买入一只股票（前端信号页/持仓页"模拟买入"按钮触发，固定金额整手）。
// 与自动撮合共用 fillLocked：同一持仓去重/仓位上限/现金约束；手动买入归"其他池"。
// English: manually buys one stock at the live price (frontend/APK signal-page or positions-page "paper
// buy" button; fixed-amount whole lots). Shares fillLocked with auto-fill: dedupe / position cap / cash
// checks; manual buys debit the "other" pool.
func (e *Engine) Buy(code, name, strategy string, signalPrice float64, quotes map[string]*data.StockInfo) error {
	return e.BuyInPool(code, name, strategy, "", signalPrice, quotes)
}

// BuyInPool 手动买入的池感知版本（§用户要求：信号页模拟买入归入信号原战法池，
// 只有纯手动/无归属才落"其他池"）。poolKey 为空 = 其他池（旧行为）。
// poolKey 指向不存在的池时回退其他池（避免现金无归处），并在日志注明。
// English: pool-aware manual buy; empty poolKey = the "other" pool (legacy behavior).
// A nonexistent target pool falls back to the other pool with a log note.
func (e *Engine) BuyInPool(code, name, strategy, poolKey string, signalPrice float64, quotes map[string]*data.StockInfo) error {
	// §R0.4 涨跌停拒成交：涨停封板的股票买单拒绝。§R5/R6 修复：改按 code 直接取键
	// （此前遍历比对 q.Code==code，调用方未填 Code 字段时守卫静默失效）+ 分板块幅度。
	if q := quotes[code]; q != nil && q.ChangePct >= LimitUpPct(code, name) {
		log.Printf("[paper] 涨停拒买 %s(%.1f%%)", code, q.ChangePct)
		return fmt.Errorf("涨停封板无法买入")
	}
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// 持有/达最大仓位即拒绝，再按 quote 实时价成交。
	// §DYN-SIZE-v3：dynamic 模式下已持仓走动态加仓（数量按剩余 35% 额度与现金重算）。
	p, held := e.positions[code]
	if held && !e.dynamicBudgetOn() {
		return errHeld
	}
	if !e.dynamicBudgetOn() && e.cfg.MaxPositions > 0 && len(e.positions) >= e.cfg.MaxPositions {
		return errMaxPos
	}
	var price float64
	if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
		price = q.Price
	} else {
		return errNoQuote
	}
	if held {
		q, why := e.dynamicPaperQtyLocked(p.StrategyType, code, price, 1.0)
		if q <= 0 {
			if why == "" {
				why = "动态预算不足"
			}
			e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: p.StrategyType, Side: "buy",
				Kind: "手动加仓", SignalPrice: signalPrice, Status: "rejected", Reason: "动态仓位: " + why})
			return fmt.Errorf("动态仓位: %s", why)
		}
		if err := e.addToPositionLocked(p, code, name, strategy, signalPrice, price, q, "手动模拟加仓(动态仓位)"); err != nil {
			e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: p.StrategyType, Side: "buy",
				Kind: "手动加仓", SignalPrice: signalPrice, Status: "rejected", Reason: fmt.Sprintf("%v", err)})
			return err
		}
		// B5：非 T 加仓镜像（显式调用点；T 腿走 addToPositionLocked 的另一条调用路径，不在这里）。
		e.tShadowMirrorBuyLocked(code, e.applyBuyPriceLocked(price), q, time.Now())
		e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: p.StrategyType, Side: "buy",
			Kind: "手动加仓", SignalPrice: signalPrice, Price: price, Qty: q, Status: "filled", Reason: "动态仓位加仓"})
		e.mirrorOpenLocked(p)
		e.persist()
		return nil
	}
	if _, ok := e.pools[poolKey]; !ok {
		// §健壮性：未知池 key 不再静默回退「其他池」——静默回退会让资金被悄悄计入错误池，
		// 既污染该池累计表现，又可能挤占其他池预算。改为显式报错，让上层（前端/调用方）可知。
		if poolKey != "" {
			log.Printf("[paper] 手动买入拒绝 %s(%s): 目标池 %s 不存在", code, name, poolKey)
			return fmt.Errorf("目标战法池 %s 不存在，无法买入（请先确认分池配置）", poolKey)
		}
		poolKey = ""
	}
	reason := "手动模拟买入"
	if poolKey != "" {
		reason = "手动模拟买入(归" + poolKey + ")"
	}
	qVWAP, _ := data.VWAPOf(quotes[code])
	if err := e.fillLocked(poolKey, code, name, strategy, signalPrice, time.Now(), time.Now(), price, 0, reason, 1.0, 0, true, qVWAP); err != nil {
		e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: poolKey, Side: "buy",
			Kind: "手动买入", SignalPrice: signalPrice, Status: "rejected", Reason: fmt.Sprintf("%v", err)})
		return err
	}
	e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: poolKey, Side: "buy",
		Kind: "手动买入", SignalPrice: signalPrice, Price: price, Qty: e.positions[code].Qty,
		Status: "filled", Reason: reason})
	// B5：手动买入同样是非 T 成交，影子账必须逐笔对齐（否则对照的前提当场不成立）。
	e.tShadowMirrorBuyLocked(code, e.applyBuyPriceLocked(price), e.positions[code].Qty, time.Now())
	e.mirrorOpenLocked(e.positions[code]) // 手动买入同样镜像开仓（两本账合一）
	e.persist()
	return nil
}

// BuyEx 手动按指定价格与数量买入一只股票（普通用户模拟盘：输入买入价+买入数量，静态记账）。
// price > 0 时按用户输入价成交（不依赖行情）；price = 0 时回退实时价。
// qty 单位=股数（§FIX-1(20260919) 口径收敛：A 股 1 手=100 股，手数→股数换算在调用方/HTTP 前端完成，
// 引擎一律按股记账；<=0 拒绝）。手动买入归"其他池"，不挤占战法池。
// 已持仓时自动合并为加仓（加权平均成本，追加买入记录）。
// English: manually buys a stock at an explicit price and share count (normal users' paper book: the
// buyer types the price and quantity; static bookkeeping). A price > 0 fills at the typed price (no
// quote needed); price = 0 falls back to the live quote. qty is in SHARES (1 lot = 100 shares; the
// lots→shares conversion is the caller's job, §FIX-1 20260919; <=0 rejected).
// Manual buys debit the "other" pool and never crowd a strategy pool. An already-held code merges as an
// add-on (quantity added, cost averaged, extra fill appended).
func (e *Engine) BuyEx(code, name, strategy string, signalPrice, price float64, qty int, quotes map[string]*data.StockInfo) error {
	return e.BuyExInPool(code, name, strategy, "", signalPrice, price, qty, quotes)
}

// BuyExInPool BuyEx 的池感知版本（§用户要求：信号页模拟买入归入信号原战法池）。
// poolKey 为空 = 其他池（纯手动旧行为）；指向不存在的池回退其他池并记日志。
func (e *Engine) BuyExInPool(code, name, strategy, poolKey string, signalPrice, price float64, qty int, quotes map[string]*data.StockInfo) error {
	// §R5 修复：BuyEx 此前完全无涨停检查，可用任意价格买入封板股（与自动/手动口径不一致）。
	// 行情快照可用时按 §R6 分板块幅度守卫；纯手输价（无行情）不拦——用户可能补记一笔真实世界的委托。
	if q := quotes[code]; q != nil && q.ChangePct >= LimitUpPct(code, name) {
		log.Printf("[paper] 涨停拒买(手输价) %s(%.1f%%)", code, q.ChangePct)
		return fmt.Errorf("涨停封板无法买入")
	}
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// 已持有则加仓；满仓/手数非法/无价时拒绝，价格缺省取 quote 实时价。
	if p, held := e.positions[code]; held {
		if err := e.addToPositionLocked(p, code, name, strategy, signalPrice, price, qty, "手动模拟加仓"); err != nil {
			return err
		}
		e.tShadowMirrorBuyLocked(code, e.applyBuyPriceLocked(price), qty, time.Now())
		return nil
	}
	if !e.dynamicBudgetOn() && e.cfg.MaxPositions > 0 && len(e.positions) >= e.cfg.MaxPositions {
		return errMaxPos
	}
	if qty <= 0 {
		return errLotTooSmall
	}
	if price <= 0 {
		if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
			price = q.Price
		} else {
			return errNoQuote
		}
	}
	if _, ok := e.pools[poolKey]; !ok {
		// §健壮性：未知池 key 显式报错（同 BuyInPool），不静默回退其他池。
		if poolKey != "" {
			log.Printf("[paper] 手动买入(手输价)拒绝 %s(%s): 目标池 %s 不存在", code, name, poolKey)
			return fmt.Errorf("目标战法池 %s 不存在，无法买入（请先确认分池配置）", poolKey)
		}
		poolKey = ""
	}
	reason := "手动模拟买入"
	if poolKey != "" {
		reason = "手动模拟买入(归" + poolKey + ")"
	}
	qVWAP, _ := data.VWAPOf(quotes[code])
	if err := e.fillLocked(poolKey, code, name, strategy, signalPrice, time.Now(), time.Now(), price, qty, reason, 1.0, 0, true, qVWAP); err != nil {
		e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: poolKey, Side: "buy",
			Kind: "手动买入", SignalPrice: signalPrice, Status: "rejected", Reason: fmt.Sprintf("%v", err)})
		return err
	}
	e.recordOrderLocked(Order{Code: code, Name: name, Strategy: strategy, StrategyType: poolKey, Side: "buy",
		Kind: "手动买入", SignalPrice: signalPrice, Price: price, Qty: e.positions[code].Qty,
		Status: "filled", Reason: reason})
	// B5：手动买入同样是非 T 成交，影子账必须逐笔对齐（否则对照的前提当场不成立）。
	e.tShadowMirrorBuyLocked(code, e.applyBuyPriceLocked(price), e.positions[code].Qty, time.Now())
	e.mirrorOpenLocked(e.positions[code]) // 手动买入同样镜像开仓（两本账合一）
	e.persist()
	return nil
}

// addToPositionLocked 已持仓加仓：加权平均成本、追加买入记录、从该持仓所属战法池扣款（须持锁调用）。
// 手动加仓归原持仓池（按持仓记录的 StrategyType；旧数据/空 = 其他池），与买入回池语义一致：
// 加仓成本记入该池累计成本，卖出时收益仍记该池。
// English: adds to an existing position — quantity up, cost averaged, an extra buy fill appended, cash
// debited from the position's own strategy pool (caller must hold the lock). A manual add-on goes to the
// pool the position belongs to (per its recorded StrategyType; empty/legacy = the other pool), matching
// the buy-into-pool semantics: the add-on cost accrues to that pool's cumulative cost, and sale proceeds
// still attribute to it.
func (e *Engine) addToPositionLocked(p *Position, code, name, strategy string, signalPrice, price float64, qty int, reason string) error {
	if qty <= 0 {
		return errLotTooSmall
	}
	// §R8 统一口径：加仓与首买同模型——滑点上浮、佣金(含最低)入账；此前加仓零费用零滑点，
	// 同一票加仓比首买"便宜"，成本口径失真。
	if e.cfg.SlippageBps > 0 {
		price = price * (1 + e.cfg.SlippageBps/10000.0)
	}
	cost := float64(qty) * price
	fee := e.buyFeeLocked(cost)
	poolKey := p.StrategyType // 加仓归原持仓池
	pool := e.pools[poolKey]
	if cost+fee > pool {
		return errCash
	}
	// §P1-I 分仓纪律一致性：手动加仓与首买、信号买入走同一套纪律——此前 addToPositionLocked
	// 只 recordPoolBuy（记账）却跳过了冷却/日限/预算前置检查，导致同池加仓可绕过纪律闸门。
	// 手动加仓 confidence=1.0，与手动买入一致不受评分门槛约束，但冷却/日限/预算仍生效。
	if reason := e.checkPoolDisciplinePre(poolKey, 1.0, true); reason != "" {
		return fmt.Errorf("%w: %s", errPoolDiscipline, reason)
	}
	if reason := e.checkPoolBudget(poolKey, cost); reason != "" {
		return fmt.Errorf("%w: %s", errPoolDiscipline, reason)
	}
	// §C1 行业/概念敞口：加仓同样增加敞口，必须与首买同闸。
	if ok, why := e.checkSectorExposureLocked(code, cost); !ok {
		log.Printf("[paper] §C1 行业敞口拒绝加仓 %s(%s): %s", code, name, why)
		return fmt.Errorf("%s", why)
	}
	if ok, why := e.checkConceptExposureLocked(code, cost); !ok {
		log.Printf("[paper] §C1-V2 概念敞口拒绝加仓 %s(%s): %s", code, name, why)
		return fmt.Errorf("%s", why)
	}
	e.pools[poolKey] = pool - cost - fee
	e.cash -= cost + fee
	e.hasFilled = true             // §反馈修复：首笔成交起才开始记净值曲线
	e.recordPoolBuy(poolKey, cost) // §分仓纪律：记录买入事件（冷却/日限/预算计数）
	// 加仓成本照记入该池，保证池累计表现不遗漏。
	// English: the add-on cost accrues to the pool so its cumulative performance stays complete.
	if e.poolPerf[poolKey] == nil {
		e.poolPerf[poolKey] = &PoolPerf{}
	}
	e.poolPerf[poolKey].Cost += cost
	// §T1-PARTIAL 加仓先落层账，再改数量/成本——顺序是有意义的：addBuyLot 需要"加仓前"的 p.Qty
	// 给存量持仓补写旧批次（否则层账只覆盖这一笔，可卖量会凭空少掉旧股）。
	// 这一步同时收掉 2.3 的 T+1 漏放：旧实现加仓完全不写时间戳，当日新买的机动仓继承昨日底仓
	// 的 FilledAt 被当成可卖；按批次记日之后，可卖量只认批次自己的日期。
	// English: the lot is appended BEFORE Qty/Cost mutate (the seed path needs the pre-add quantity), and
	// this is exactly what closes the T+1 leak — add-ons used to inherit the base lot's date.
	p.addBuyLot(qty, (cost+fee)/float64(qty), time.Now())
	p.Cost += cost + fee
	p.Qty += qty
	p.CostPrice = p.Cost / float64(p.Qty)
	p.Mark = price
	// §T1-PARTIAL 展示态刷新：加仓之后当日买入的那部分立刻不可卖，账面上要如实标"部分可卖"。
	p.refreshSellStatusLocked(time.Now())
	e.trades = append(e.trades, Trade{
		Code:         code,
		Name:         name,
		Strategy:     strategy,
		StrategyType: poolKey,
		Side:         "buy",
		Price:        price,
		SignalPrice:  signalPrice,
		Qty:          qty,
		Amount:       cost,
		Time:         time.Now(),
		Fee:          fee,
		Reason:       reason,
	})
	log.Printf("[paper] 模拟加仓 %s(%s) +%d股 @%.2f 现持%d股 均价%.3f 池=%s", code, name, qty, price, p.Qty, p.CostPrice, strategyPoolLabel(poolKey))
	e.persist()
	return nil
}

// MarkToMarket 用实时快照价更新持仓估值价。
// English: updates position mark prices from the live snapshot.
func (e *Engine) MarkToMarket(quotes map[string]*data.StockInfo) {
	if !e.cfg.Enabled {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	changed := false
	for code, p := range e.positions {
		if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
			if p.Mark != q.Price {
				p.Mark = q.Price
				changed = true
			}
		}
	}
	if changed {
		e.persist()
	}
	// §SHORT-3 做空侧估值：刷新空头现价 + 按日计提融券利息 + 涨幅止损强制买回。
	// English: §SHORT-3 — mark the short book, accrue daily lending interest, force-cover rallies.
	e.shortMarkToMarketLocked(quotes)
}

// Snapshot 记录当日净值（同一交易日只保留最新一个点）。
// 值变化才落盘：同日净值不变（价格未波动）不再重复写盘，
// 避免近实时循环每 5s 一次的无意义 IO/日志压力（低配服务器磁盘与 CPU 友好）。
// English: records the day's equity (one point per trading day, overwritten when the day repeats).
// Persists only when the value actually changes — a same-day identical value is not rewritten,
// cutting the pointless every-5s write (friendly to the small server's disk and CPU).
func (e *Engine) Snapshot(now time.Time) {
	if !e.cfg.Enabled {
		return
	}
	// §反馈修复：从未成交过 → 不记录净值点（否则每天一个平线点，"没有买入也有净值曲线"）。
	// 首笔成交后自动开始正常记录；Reset 清盘后回到不记录状态直到再成交。
	if !e.hasFilled {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	date := cntime.DayOf(now) // §TZ1
	mv := e.marketValueLocked()
	// §SHORT-3 净值并入做空侧（shortCash + 担保占用 + 浮动盈亏，见 shortEquityLocked）。
	// English: §SHORT-3 — total equity includes the short book (cash + frozen margin + floating P&L).
	val := e.cash + mv + e.shortEquityLocked()
	// §T-COST B5：每轮估值给影子账落一个点（与真实净值同频）。放在"值没变就不写"之前——
	// 真实权益不动时影子账仍可能因跳过 T 腿而偏离，早退会让归因曲线出现整段空洞。
	if e.shadowNoTOn {
		e.tShadowSampleLocked(now)
	}
	if len(e.equity) > 0 && e.equity[len(e.equity)-1].Date == date {
		last := &e.equity[len(e.equity)-1]
		if last.Value == val && last.Cash == e.cash {
			return
		}
		*last = EquityPoint{Date: date, Value: val, Cash: e.cash}
	} else {
		e.equity = append(e.equity, EquityPoint{Date: date, Value: val, Cash: e.cash})
	}
	e.persist()
}

// marketValueLocked 返回持仓市值（须持锁调用）。
// English: total market value of open positions (caller must hold the lock).
func (e *Engine) marketValueLocked() float64 {
	mv := 0.0
	for _, p := range e.positions {
		mv += p.MarketValue()
	}
	return mv
}

// Sell 手动按实时价卖出持仓（清仓）。返回错误信息（无持仓/行情缺失时 err != nil）。
// §T1-PARTIAL（裁决三）：整仓里有一部分当日买不可卖时，**卖掉可卖的那部分**、持仓留着，订单落
// partial；一股都卖不掉才返回 errTPlusOne（与改前逐笔一致，前端仍按 400 提示）。手动路径**不登记
// 残量意图**：人自己下的单不该由引擎第二天早上替他决定卖什么（自动路径才有意图队列）。
// English: manually sells a position at the live price. Ruling #3: when part of the book is T+1-locked,
// the sellable part fills and the order is booked as "partial"; only a fully-locked position errors (so the
// HTTP layer still returns 400). Manual sells deliberately do NOT register a residual intent — the engine
// must not decide on a human's behalf what to sell tomorrow morning.
func (e *Engine) Sell(code string, quotes map[string]*data.StockInfo) error {
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, held := e.positions[code]
	if !held {
		return errNotHeld
	}
	var price float64
	if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
		price = q.Price
	} else {
		return errNoQuote
	}
	want := p.Qty
	sold, err := e.sellPositionLocked(p, price, "手动模拟卖出")
	if err == nil {
		status := "filled"
		if sold < want {
			status = "partial" // 残量留在账上，由人次日再卖（手动路径不自动补卖）
		}
		e.recordOrderLocked(Order{Code: code, Name: p.Name, Strategy: p.Strategy, StrategyType: p.StrategyType,
			Side: "sell", Kind: "手动卖出", Price: price, Qty: sold, Status: status, Reason: "手动模拟卖出"})
		// B5：手动卖出也是非 T 成交，影子账逐笔镜像（否则对照前提不成立）。
		e.tShadowMirrorSellLocked(code, e.applySellPriceLocked(price), sold, time.Now())
		e.persist()
	}
	return err
}

// SellEx 手动按指定价格与数量减仓（部分卖出）。qty 单位=股数（§FIX-1(20260919) 口径收敛：手数→股数
// 换算在调用方完成，引擎一律按股记账，同 BuyExInPool/paper_test.go:682）；price > 0 用输入价，
// price = 0 回退实时价。数量 >= 当前持仓时退化为清仓（复用 sellPositionLocked 的减量成交口径）。
// 整手/零股纪律由 HTTP 入口守卫（internal/server/paper.go handlePaperBuy/Sell）负责，引擎不重复校验
// ——引擎测试与自动路径允许任意股数（如减仓 50 股的成本摊销用例）。
// §T1-PARTIAL：请求量超过可卖量时减量成交（返回的持仓保留残量），订单元按**实际成交量**记账；
// 可卖量为 0 时返回 errTPlusOne。同 Sell：手动路径不登记残量意图。
// English: manually trims a position at an explicit price and share count (qty is in SHARES — the
// lots→shares conversion is the caller's job, §FIX-1 20260919); price > 0 uses the typed price,
// price = 0 falls back to the live quote. A qty >= the position degrades to a full close. Ruling #3: a qty
// beyond the sellable amount fills partially and the order books the executed size; zero sellable errors.
// Lot discipline is enforced at the HTTP entry, not here.
func (e *Engine) SellEx(code string, price float64, qty int, quotes map[string]*data.StockInfo) error {
	if !e.cfg.Enabled {
		return errDisabled
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, held := e.positions[code]
	if !held {
		return errNotHeld
	}
	if qty <= 0 {
		return errLotTooSmall
	}
	if price <= 0 {
		if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
			price = q.Price
		} else {
			return errNoQuote
		}
	}
	if qty >= p.Qty {
		want := p.Qty
		sold, err := e.sellPositionLocked(p, price, "手动模拟卖出")
		if err == nil {
			status := "filled"
			if sold < want {
				status = "partial"
			}
			e.recordOrderLocked(Order{Code: code, Name: p.Name, Strategy: p.Strategy, StrategyType: p.StrategyType,
				Side: "sell", Kind: "手动卖出", Price: price, Qty: sold, Status: status, Reason: "手动模拟卖出"})
			e.tShadowMirrorSellLocked(code, e.applySellPriceLocked(price), sold, time.Now())
			e.persist()
		}
		return err
	}
	sold, err := e.sellQtyLocked(p, price, qty, "手动模拟减仓")
	if err == nil {
		e.recordOrderLocked(Order{Code: code, Name: p.Name, Strategy: p.Strategy, StrategyType: p.StrategyType,
			Side: "sell", Kind: "手动减仓", Price: price, Qty: sold, Status: "partial", Reason: "手动模拟减仓"})
		e.tShadowMirrorSellLocked(code, e.applySellPriceLocked(price), sold, time.Now())
		e.persist()
	}
	return err
}

// applySellPriceLocked 卖出滑点（价下浮）；返回成交价（须持锁调用）。
func (e *Engine) applySellPriceLocked(price float64) float64 {
	if e.cfg.SlippageBps > 0 {
		return price * (1 - e.cfg.SlippageBps/10000.0)
	}
	return price
}

// applyBuyPriceLocked 买入滑点（价上浮）；返回成交价（须持锁调用）。
// B5 影子镜像要用**与真实成交同一口径**的价格：直接搬 quote 价会让"非 T 成交逐笔相等"假红。
func (e *Engine) applyBuyPriceLocked(price float64) float64 {
	if e.cfg.SlippageBps > 0 {
		return price * (1 + e.cfg.SlippageBps/10000.0)
	}
	return price
}

// sellPositionLocked 退出裁决的执行入口（须持锁调用）：能卖多少就卖多少，不再因为"整仓里有一部分
// 当日买不可卖"而把一笔卖出整单拒掉（§T1-PARTIAL 裁决三，形态上等价于一次正 T 的"买新卖旧"）。
// 返回实际卖出股数；可卖量覆盖全仓时走完整清仓（删持仓 + 生命周期镜像 + 再入场冷却），
// 只卖掉一部分时持仓留着、**不关生命周期、不记冷却**——这两点正是 2.7 里"次日买回/续卖被自家
// 闸门挡掉"的成因，半笔减仓若去调清仓分支就把残量锁死了。
// 可卖量=0 时仍返回 errTPlusOne（与改前"当日整仓不可卖"的留痕形态逐笔一致，调用方按被拒记账）。
// English: the exit-execution entry — sells whatever is sellable instead of rejecting the whole order when
// part of the lot was bought today. A full close deletes the position and fires the mirror/cooldown; a
// partial one keeps the position open with no lifecycle close and no re-entry cooldown (that omission is
// the point: the residual must stay tradable tomorrow). Zero sellable still yields errTPlusOne.
func (e *Engine) sellPositionLocked(p *Position, price float64, reason string) (int, error) {
	now := time.Now()
	sellable := e.sellableQtyLocked(p, now)
	if sellable <= 0 {
		log.Printf("[paper] T+1拦截 %s(%s) 当日买入不可卖", p.Code, p.Name)
		return 0, errTPlusOne
	}
	if sellable >= p.Qty {
		return e.sellAllLocked(p, price, reason)
	}
	sold, err := e.sellQtyLocked(p, price, sellable, reason)
	if err != nil {
		return 0, err
	}
	log.Printf("[paper] §T1-PARTIAL 只卖可卖部分 %s(%s) 卖%d股 残%d股 原因=%s", p.Code, p.Name, sold, p.Qty, reason)
	return sold, nil
}

// sellQtyLocked 按股数部分减仓：回池、结算已实现盈亏、追加卖出记录（须持锁调用）。
// 手动减仓与自动减仓（阶段1.1 卖出信号半仓）共用；不触发清仓镜像（report 账记录保留至整笔平仓）。
// §R3 T+1：一股都卖不掉时拒绝减仓。§T1-PARTIAL（裁决三 W4）：请求量超过可卖量时**减量成交**
// 而不是整单报错，返回值就是真实成交股数，差额由调用方登记残量意图（3.3）。
// §R2 费用：卖出价下浮滑点、佣金(含最低)+印花税从所得中扣除。
// §修复 FIX#3（2026-09-04）：减仓同步摊销 p.Cost（减去已卖股份对应的含费成本）。
// 旧实现只 p.Qty -= qty、成本总额保持初始整笔——清仓 sellAllLocked 再按 net-p.Cost 结算，
// 减掉部分成本被重复扣除，realized/poolPerf.Realized/ReturnPct 全被低估。
// 层账按 FIFO 扣减（只扣可卖批次），成本口径仍走 FIX#3 的均价摊销——本批刻意不让层账变成第二把
// 成本尺子（见 PositionLot 注释）。锚点在任何卖出路径都不改写（§ANCHOR-FIRST 写入冻结）。
// English: trims by share count — pool return, realized P&L, extra sell fill (caller must hold the lock).
// Shared by manual trims, auto trims and residual-exit execution; does NOT fire the close mirror.
// R3: zero sellable rejects; §T1-PARTIAL: an over-sellable request now fills the sellable part and returns
// the actual shares sold, so the caller can book the shortfall as an intent. R2: sell-side slippage +
// commission(min) + stamp tax are deducted from proceeds. §FIX#3 amortization is unchanged — batches are
// consumed FIFO for the *dates*, while cost keeps amortizing at the diluted average. The anchor is never
// rewritten here.
func (e *Engine) sellQtyLocked(p *Position, price float64, qty int, reason string) (int, error) {
	now := time.Now()
	sellable := e.sellableQtyLocked(p, now)
	if sellable <= 0 {
		log.Printf("[paper] T+1拦截 %s(%s) 当日买入不可卖", p.Code, p.Name)
		return 0, errTPlusOne
	}
	sold := qty
	if sold > sellable {
		sold = sellable
	}
	price = e.applySellPriceLocked(price)
	gross := price * float64(sold)
	fee := e.sellFeeLocked(gross)
	net := gross - fee
	// 减仓收益回原战法池（按持仓记录的类型；空=其他池）。
	// English: trim proceeds return to the position's own strategy pool (per its recorded type; empty =
	// the other pool).
	e.pools[p.StrategyType] += net
	e.cash += net
	e.realized += net - p.CostPrice*float64(sold)
	// 卖出结算：已实现盈亏计入本池（卖出仍记分仓资金池，跨重启保留）。按净额口径。
	// English: sale settlement — realized P&L credits the position's own pool on a net basis.
	if e.poolPerf[p.StrategyType] == nil {
		e.poolPerf[p.StrategyType] = &PoolPerf{}
	}
	e.poolPerf[p.StrategyType].Realized += net - p.CostPrice*float64(sold)
	// §FIX#3 成本摊销：已卖股份的含费成本从持仓总额中扣除，避免清仓时整笔成本重复结算。
	// 剩余持仓 CostPrice 保持不变（均价口径），仅总额收缩。
	// English: §FIX#3 cost amortization — subtract the sold shares' fee-inclusive cost so the later
	// full close settles against the remaining cost; per-share average cost stays unchanged.
	p.Cost -= p.CostPrice * float64(sold)
	p.Qty -= sold
	// §T1-PARTIAL 层账按 FIFO 扣可卖批次，并刷新展示态（残量若仍含当日批次 → "部分可卖"）。
	consumeSellableLots(p, sold, now)
	p.refreshSellStatusLocked(now)
	e.trades = append(e.trades, Trade{
		Code:         p.Code,
		Name:         p.Name,
		Strategy:     p.Strategy,
		StrategyType: p.StrategyType,
		Side:         "sell",
		Price:        price,
		Qty:          sold,
		Amount:       gross,
		Time:         now,
		Fee:          fee,
		Reason:       reason,
	})
	log.Printf("[paper] 模拟减仓 %s(%s) -%d股 @%.2f 剩余%d股 费%.2f 原因=%s", p.Code, p.Name, sold, price, p.Qty, fee, reason)
	e.persist()
	return sold, nil
}

// sellAllLocked 清仓单一持仓：回池、结算已实现盈亏、追加卖出记录，并触发清仓镜像
// （阶段1.2 两本账合一：report 账对应记录同步平仓；须持锁调用）。
// §R3 T+1 + §T1-PARTIAL：这是一把**严格整笔**的尺子——可卖量必须覆盖全仓才成交，否则返回
// errTPlusOne 交回调用方（想"能卖多少卖多少"请走 sellPositionLocked，那才是裁决三的入口）。
// 之所以保留严格档：残量执行卖到最后一股时要走这里，把生命周期关掉、把冷却记上；半笔减仓绝不能
// 误走这条路（2.7：删持仓 + 冷却会把次日的残量买回/续卖全挡死）。
// §R2 费用：卖出价下浮滑点、佣金(含最低)+印花税从所得中扣除。
// English: closes a single position — pool return, realized P&L, extra sell fill, plus the close mirror
// (unified books; caller must hold the lock). STRICT: requires the sellable quantity to cover the whole
// position, otherwise errTPlusOne (use sellPositionLocked for the ruling-3 degraded sale). R2: slippage +
// fees deducted.
func (e *Engine) sellAllLocked(p *Position, price float64, reason string) (int, error) {
	now := time.Now()
	sellable := e.sellableQtyLocked(p, now)
	if sellable < p.Qty {
		if sellable <= 0 {
			log.Printf("[paper] T+1拦截 %s(%s) 当日买入不可卖", p.Code, p.Name)
		} else {
			log.Printf("[paper] §T1-PARTIAL %s(%s) 可卖%d<持仓%d，整笔清仓不执行（请走减量卖法）", p.Code, p.Name, sellable, p.Qty)
		}
		return 0, errTPlusOne
	}
	price = e.applySellPriceLocked(price)
	gross := price * float64(p.Qty)
	fee := e.sellFeeLocked(gross)
	net := gross - fee
	// 卖出收益回原战法池（按持仓记录的类型；空=其他池）。
	// English: sale proceeds return to the position's own strategy pool (per its recorded type; empty =
	// the other pool).
	e.pools[p.StrategyType] += net
	e.cash += net
	e.realized += net - p.Cost
	// 清仓结算：已实现盈亏计入本池（卖出仍记分仓资金池，跨重启保留）。按净额口径。
	// English: close settlement — realized P&L credits the position's own pool on a net basis.
	if e.poolPerf[p.StrategyType] == nil {
		e.poolPerf[p.StrategyType] = &PoolPerf{}
	}
	e.poolPerf[p.StrategyType].Realized += net - p.Cost
	qty := p.Qty
	e.trades = append(e.trades, Trade{
		Code:         p.Code,
		Name:         p.Name,
		Strategy:     p.Strategy,
		StrategyType: p.StrategyType,
		Side:         "sell",
		Price:        price,
		Qty:          p.Qty,
		Amount:       gross,
		Time:         now,
		Fee:          fee,
		Reason:       reason,
	})
	log.Printf("[paper] 模拟卖出 %s(%s) %d股 @%.2f 盈亏%.2f 费%.2f 原因=%s", p.Code, p.Name, p.Qty, price, net-p.Cost, fee, reason)
	// §T1-PARTIAL 整笔平仓：层账随持仓一起出清（先扣光再删行，避免 p 指针被复用时留着幽灵批次）。
	consumeSellableLots(p, p.Qty, now)
	p.Lots = nil
	p.Status = ""
	delete(e.positions, p.Code)
	// 清仓镜像（阶段1.2）：report 账对应记录同步平仓（pap_<code> 稳定键）。
	// English: close mirror (unified books) — the report-book record closes in sync (stable key pap_<code>).
	e.mirrorCloseLocked(p.Code, price, float64(qty), reason)
	// §R1.4 再入场冷却：记录本次清仓时间，供后续 fillLocked 前置检查拦住冷却期内回马枪。
	if e.reEntry != nil {
		e.reEntry.recordClose(p.Code, time.Now())
	}
	// §T1-PARTIAL 整笔清仓＝残量意图失去对象（清仓之后没有"还没卖掉的股"可言）。
	delete(e.pendingExits, p.Code)
	e.persist()
	return qty, nil
}

// SetStrategyPools 设置启用的战法资金池（按当前现金均分，key 集合 = types + ""）。
// 幂等：types 集合未变化时保留各池现金（热加载/重复注入安全）；
// 集合变化时按当前总现金重新均分并持久化。
// 注入路径：engine.ActivePoolTypes → registry.SetPaperPools → 各账号 pe.SetStrategyPools。
// English: configures the enabled strategy cash pools (split the current cash evenly, key set = types +
// ""). Idempotent: an unchanged type set keeps per-pool cash intact (safe for hot reload / repeated
// injection); a changed set re-splits the current total cash evenly and persists. Injected via
// engine.ActivePoolTypes → registry.SetPaperPools → each account's pe.SetStrategyPools.
func (e *Engine) SetStrategyPools(types []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// 规范化排序比较，集合比较与输入顺序无关
	// English: normalized sorted comparison makes the set comparison order-insensitive.
	// §C 合并保留规则细分池（fac_1/pat_2…）——引擎配置同步只管基础类型，
	// 规则池由 EnsurePool 生命周期管理，重建/比较时都必须并入集合。
	merged := append([]string(nil), types...)
	for k := range e.extraPoolKeys {
		if !containsStr(merged, k) {
			merged = append(merged, k)
		}
	}
	sorted := append([]string(nil), merged...)
	sort.Strings(sorted)
	cur := append([]string(nil), e.poolTypes...)
	sort.Strings(cur)
	if equalStrings(sorted, cur) {
		return // 集合未变：保留各池现金
	}
	e.poolTypes = merged
	e.rebuildPoolsLocked()
	log.Printf("[paper] 战法资金池已按 %v 均分：每池 %.2f", e.poolTypes, e.pools[""])
}

// rebuildPoolsLocked 按当前 poolTypes 重建资金池：总现金均分到 types + "" 每池（须持锁调用）。
// 未分仓（poolTypes 为空）时退化为单池 {"": cash}，与分仓前行为一致。
// English: rebuilds the pools from poolTypes: the total cash is split evenly across types + "" (caller
// must hold the lock). With no allocation (empty poolTypes) it degrades to the single pool {"": cash},
// identical to pre-allocation behavior.
func (e *Engine) rebuildPoolsLocked() {
	if len(e.poolTypes) == 0 {
		e.pools = map[string]float64{"": e.cash}
	} else {
		keys := append([]string(nil), e.poolTypes...)
		keys = append(keys, "") // 其他/手动池
		share := e.cash / float64(len(keys))
		e.pools = make(map[string]float64, len(keys))
		for _, k := range keys {
			e.pools[k] = share
		}
	}
	// 池集合变化：为每个池确保持久化表现记录存在（保留已有 Cost/Realized）。
	// English: on a pool-set change, ensure each pool has a persisted-performance record (keeps any
	// existing Cost/Realized).
	if e.poolPerf == nil {
		e.poolPerf = make(map[string]*PoolPerf)
	}
	for k := range e.pools {
		if e.poolPerf[k] == nil {
			e.poolPerf[k] = &PoolPerf{}
		}
	}
	// 池集合变化：为每个池初始化持仓上限记录（0=该池不单独设限）。
	// English: ensure each pool has a position-cap entry (0 = no per-pool limit).
	if e.poolMaxPos == nil {
		e.poolMaxPos = make(map[string]int)
	}
	for k := range e.pools {
		if _, ok := e.poolMaxPos[k]; !ok {
			e.poolMaxPos[k] = 0
		}
	}
}

// equalStrings 顺序无关的字符串切片相等比较（SetStrategyPools 幂等判断用）。
// English: order-insensitive string-slice equality (used for SetStrategyPools idempotency).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// StrategyPools 返回各战法资金池的展示快照（前端分仓条：池余量/占比/持仓数/累计表现）。
// English: returns each strategy pool's display snapshot (frontend allocation strip:
// balance/ratio/positions/cumulative performance).
func (e *Engine) StrategyPools() []StrategyPoolState {
	e.mu.Lock()
	defer e.mu.Unlock()
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	keys := append([]string(nil), e.poolTypes...)
	keys = append(keys, "")
	sort.Strings(keys)
	out := make([]StrategyPoolState, 0, len(keys))
	for _, k := range keys {
		cash := e.pools[k]
		ratio := 0.0
		if total > 0 {
			ratio = cash / total * 100
		}
		cnt := 0
		floating := 0.0
		for _, p := range e.positions {
			if p.StrategyType == k {
				cnt++
				floating += p.PnL()
			}
		}
		perf := e.poolPerf[k]
		var cost, realized float64
		if perf != nil {
			cost = perf.Cost
			realized = perf.Realized
		}
		retPct := 0.0
		if cost > 0 {
			retPct = (realized + floating) / cost * 100
		}
		out = append(out, StrategyPoolState{
			Key:       k,
			Label:     e.poolLabelOf(k),
			Cash:      cash,
			RatioPct:  ratio,
			Positions: cnt,
			MaxPos:    e.poolMaxPos[k],
			Cost:      round2(cost),
			Realized:  round2(realized),
			Floating:  round2(floating),
			ReturnPct: round2(retPct),
			Stats:     e.statsFor(&k),
			BuyRule:   e.poolBuyRules[k],
		})
	}
	return out
}

// Reconfigure 确认资金：按新初始资金/持仓上限重开模拟盘，并落盘固化。
// 语义（对应前端"确认资金"按钮）：
//   - initialCapital > 0 时设置新初始资金（否则沿用当前初始资金）
//   - maxPositions >= 0 时设置新持仓上限（0 = 不设限，由资金自然决定）
//   - 现金重置为新初始资金，清空当前持仓与已实现盈亏
//   - 净值曲线从新资金重新记录（equity 清空，重新按日 Snapshot）
//   - 成交日志保留（trades 不清，3 个月自动清理继续生效）——历史交易固化，改资金不丢
//
// English: Reconfigure reopens the paper book with a new starting capital / position cap and persists.
// Semantics (the frontend "confirm capital" action):
//   - a positive initialCapital sets the new starting capital (otherwise the current one is kept)
//   - a non-negative maxPositions sets the new position cap (0 = unlimited, driven by the balance)
//   - cash resets to the new starting capital; current positions and realized P/L are cleared
//   - the equity curve restarts from the new capital (equity cleared, re-snapshotted daily)
//   - the fill log is preserved (trades kept; the 3-month retention still applies) — history survives a
//     capital change instead of being wiped.
func (e *Engine) Reconfigure(initialCapital float64, maxPositions int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if initialCapital > 0 {
		e.cfg.InitialCapital = initialCapital
	}
	if maxPositions >= 0 {
		e.cfg.MaxPositions = maxPositions
	}
	// §R9 留痕修复：清存量持仓必须写审计订单并镜像平仓——此前静默清空，
	// 线上实证出现"早间 7 笔成交凭空变 2 笔持仓"无法追溯的事故。
	// English: R9 fix — clearing holdings now writes audit orders and fires the close mirror; the
	// silent wipe previously made positions vanish without a trace (confirmed in production).
	cleared := make([]*Position, 0, len(e.positions))
	for _, p := range e.positions {
		cleared = append(cleared, p)
	}
	for _, p := range cleared {
		price := p.Mark
		if price <= 0 {
			price = p.CostPrice
		}
		qty := p.Qty
		e.recordOrderLocked(Order{Code: p.Code, Name: p.Name, Strategy: p.Strategy,
			StrategyType: p.StrategyType, Side: "sell", Kind: "重配资金清仓",
			Price: price, Qty: qty, Status: "filled", Reason: "确认资金/改上限清存量", CreatedAt: time.Now()})
		e.mirrorCloseLocked(p.Code, price, float64(qty), "模拟盘重配资金")
		log.Printf("[paper] 重配资金清仓留痕 %s(%s) %d股 @%.2f", p.Code, p.Name, qty, price)
	}
	e.cash = e.cfg.InitialCapital
	e.positions = make(map[string]*Position)
	// §T1-PARTIAL：重配资金清空持仓时必须同批注销残量意图——意图按 code 记，若留着，之后重新买入
	// 同一只票会被一条属于上一个仓的旧意图在次日卖掉。
	// English: the capital-reset wipe must also drop residual-exit intents — they are keyed by code, so a
	// stale one would sell a freshly re-opened position in it next day.
	e.pendingExits = make(map[string]*PendingExit)
	e.equity = nil
	e.realized = 0
	e.poolPerf = make(map[string]*PoolPerf)
	e.rebuildPoolsLocked() // 新资金按当前池集合重新均分
	e.persist()
}

// Reset 清盘重置：不改资金/持仓上限，按当前初始资金恢复现金，清空持仓/成交/净值/已实现盈亏。
// 对应前端"清盘重置"按钮——只清空重开，不修改用户已自定义的资金与上限设置。
// English: Reset liquidates the whole book without touching capital/cap: cash restores to the current
// initial capital, while positions/trades/equity/realized are all cleared. Backs the frontend
// "清盘重置" button — it only clears, never changes the user's customized capital and cap.
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	// 全局重置镜像（阶段1.2）：report 账中 pap_ 开头的模拟盘记录一并平仓（按估值价），
	// 手动录入的非镜像记录不动。
	// English: global-reset mirror (unified books) — pap_-prefixed report records close at mark price;
	// manually entered non-mirrored records are untouched.
	if e.onClose != nil {
		for _, p := range e.positions {
			e.onClose(p.Code, p.Mark, float64(p.Qty), "模拟盘重置")
		}
	}
	e.cash = e.cfg.InitialCapital
	e.positions = make(map[string]*Position)
	e.trades = nil
	e.equity = nil
	e.hasFilled = false // 清盘后回到"未成交不记净值"状态，直到再产生成交
	e.realized = 0
	e.poolPerf = make(map[string]*PoolPerf)
	e.trimDone = make(map[string]string)
	// §T1-PARTIAL：清盘同样注销全部残量退出意图（持仓已清，意图指向的 code 不复存在；留着会在
	// 重新买入同码后被次日补卖）。
	// English: 清盘重置 also drops every residual-exit intent, so a re-bought code can't inherit an old book's intent.
	e.pendingExits = make(map[string]*PendingExit)
	// §修复 P2#21：清盘重开应重置运行时纪律——旧实现残留 on-buy 计费/dayStartCash（poolDiscipline）
	// 与清仓冷却（reEntry）到下次清盘：重置后立刻买入会被"重置前的今日计数/预算分母"与
	// 已在冷跟踪中但本次清空的记录拦截，和"只清空重开"的语义冲突。
	// English: P2#21 — a fresh book should also clear the runtime discipline: the old Reset left the
	// per-pool buy counters/budget denominator (poolDiscipline) and the close-cooldown map (reEntry)
	// intact, so a buy right after 清盘重置 could be blocked by pre-reset tallies — contradicting the
	// "liquidate and restart clean" contract.
	e.poolDiscipline = make(map[string]poolDiscipline)
	e.reEntry = newReEntryTracker()
	// §SHORT-3 做空侧同步清盘：空头清零、池现金按预算重新开设（融券负债随持仓一并注销，
	// 纸面无真实对手方）。
	// English: §SHORT-3 — the global reset also liquidates the short book and re-funds the margin
	// pool from configured capital (paper-only, no real counterparty).
	e.shorts = make(map[string]*ShortPosition)
	e.shortCash = e.cfg.ShortCapital
	e.shortRealized = 0
	e.shortFeeDone = make(map[string]string)
	e.shortOpenSeen = make(map[string]string)
	e.rebuildPoolsLocked() // 现金恢复后按当前池集合重新均分
	e.persist()
}

// ResetPool 单池清盘：只清该战法资金池的持仓与持久化表现（按最后估值价平仓回补池现金，
// 累计成本/已实现盈亏归零），其余池与全局净值/成交日志不受影响。
// 对应前端"清盘本池"按钮（选中某分仓 tab 时出现）。
// English: ResetPool liquidates a single strategy pool only — closes its positions at the last mark
// price (proceeds return to the pool), zeroing that pool's cumulative cost/realized, while other pools
// and the global equity/fill log are untouched. Backs the frontend "清盘本池" button (shown when a
// pool tab is selected).
func (e *Engine) ResetPool(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	proceeds := 0.0
	closed := 0
	for code, p := range e.positions {
		if p.StrategyType == key {
			// §修复 S6（2026-08-29）：清盘回补现金优先用估值价(Mark)，未估值(Mark<=0，盘后/
			// 未快照)按成本(Cost)回补，避免现金凭空消失、破坏分仓守恒。
			mv := p.MarketValue()
			markPrice := p.Mark
			if p.Mark <= 0 && p.Cost > 0 {
				mv = p.Cost
				if p.CostPrice > 0 {
					markPrice = p.CostPrice
				}
			}
			proceeds += mv
			delete(e.positions, code)
			// §T1-PARTIAL：本池清盘也注销该码的残量意图（池已清，意图不能再在次日替用户卖出重开的仓）。
			// English: single-pool liquidation drops that code's residual intent for the same reason.
			delete(e.pendingExits, code)
			closed++
			// 清盘镜像（阶段1.2）：report 账对应记录同步平仓（按估值价/成本价结算）。
			// English: liquidation mirror (unified books) — the report-book record closes in sync
			// (settled at the mark/cost price).
			e.mirrorCloseLocked(code, markPrice, float64(p.Qty), "分仓清盘")
		}
	}
	if proceeds > 0 {
		e.pools[key] += proceeds
		e.cash += proceeds
	}
	if e.poolPerf[key] != nil {
		// §P1-I 一致性：单池清盘后该池累计表现（含胜率计数/成本/已实现）整块归零，
		// 此前只清了 Cost/Realized，WinRate 等派生计数残留导致清盘后统计失真。
		e.poolPerf[key] = &PoolPerf{}
	}
	// §P1-I 一致性：清掉该池当日纪律计数（次数/花费/冷却），否则同日再次清盘后
	// 旧 spentToday/lastBuyAt 仍生效 → 预算/冷却误判把新开仓挡在门外。
	delete(e.poolDiscipline, key)
	// 清掉该池成交记录（该池已清盘，避免统计仍从全局日志累出信号数）
	// English: drop the pool's fills (the pool is liquidated, so stats must not count its fills again).
	if len(e.trades) > 0 {
		out := e.trades[:0]
		for _, t := range e.trades {
			if t.StrategyType != key {
				out = append(out, t)
			}
		}
		e.trades = out
	}
	log.Printf("[paper] 单池清盘 %s：平仓 %d 笔回池 %.2f", strategyPoolLabel(key), closed, proceeds)
	e.persist()
}

// Deposit 注入资金（增量）：现金 += amount，并按当前各池占比分配新增资金，保留全部持仓/净值/成交日志。
// 收益基准 initial_capital 同步累计（+amount），使总收益基于真实累计投入计算，而非被注入稀释。
// 对应前端"注入资金"按钮（区别于清盘重置：只加钱、不清仓）。
// English: Deposit adds capital incrementally — cash += amount, distributed to the pools by their current
// share; positions / equity / fill log are all kept. The return basis (initial_capital) accumulates so
// total_return is computed against the true cumulative investment instead of being diluted by deposits.
// Backs the frontend "注入资金" action (unlike a liquidation reset: it only adds money, never clears).
func (e *Engine) Deposit(amount float64) {
	if amount <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	e.cash += amount
	e.cfg.InitialCapital += amount
	if total > 0 {
		for k, v := range e.pools {
			e.pools[k] = v + amount*(v/total)
		}
	} else if len(e.pools) > 0 {
		share := amount / float64(len(e.pools))
		for k := range e.pools {
			e.pools[k] = share
		}
	}
	log.Printf("[paper] 注入资金 +%.2f → 现金=%.2f 累计投入=%.2f 持仓保留=%d",
		amount, e.cash, e.cfg.InitialCapital, len(e.positions))
	e.persist()
}

// SetMaxPositions 设置自定义持仓上限（>0 生效；0/负数=不设限，由资金自然决定），并持久化。
// English: sets the custom position cap (>0 applies; <=0 means unlimited, driven by the balance) and persists it.
func (e *Engine) SetMaxPositions(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n < 0 {
		n = 0
	}
	e.cfg.MaxPositions = n
	e.persist()
}

// SetPoolCaps 设置每池持仓上限（key=策略类型；n<=0 = 该池不单独设限）。
// 与全局持仓上限解耦：池上限是全局上限之内的子约束，Σ池上限 ≤ 全局上限由调用方（前端）守恒校验。
// 池集合变化的池自动初始化；仅覆盖传入的池，其余保留。持久化。
// English: sets per-pool position caps (key = strategy type; n<=0 = no per-pool limit). Decoupled from
// the global cap — pool caps are sub-constraints within it; Σpool caps ≤ the global cap is conserved by
// the caller (frontend). Only the given pools are set; others keep their values. Persisted.
func (e *Engine) SetPoolCaps(caps map[string]int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.poolMaxPos == nil {
		e.poolMaxPos = make(map[string]int) // 首建时初始化每池持仓上限表
	}
	for k, n := range caps {
		if n < 0 {
			n = 0 // 负数视为不设限（0）
		}
		e.poolMaxPos[k] = n
	}
	e.persist()
}

// SetPoolAllocs 设置每池资金分配（key=策略类型 → 目标现金额），并按总和守恒重排：Σ池现金=总现金，
// 各池目标额缩放为与总现金一致（总现金含持仓占用，故按当前现金占比分配可用现金）。
// 仅覆盖传入的池；未传的池按剩余现金均分。持久化。
// English: sets per-pool cash allocations (key → target cash) with conservation: Σpool cash = total
// cash, targets scaled to the total. Only the given pools are set; unmentioned pools split the rest
// evenly. Persisted.
func (e *Engine) SetPoolAllocs(allocs map[string]float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(allocs) == 0 || len(e.pools) == 0 {
		return
	}
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	// 先按传入目标分配，剩余现金再均分给未指定的池
	assigned := 0.0
	rest := make([]string, 0, len(e.pools))
	for k := range e.pools {
		if v, ok := allocs[k]; ok && v > 0 {
			e.pools[k] = v
			assigned += v
		} else {
			rest = append(rest, k)
		}
	}
	if assigned >= total {
		// 目标合计超总现金：等比压缩（守恒）
		for k := range e.pools {
			e.pools[k] = e.pools[k] / assigned * total
		}
	} else if len(rest) > 0 {
		remain := total - assigned
		share := remain / float64(len(rest))
		for _, k := range rest {
			e.pools[k] = share
		}
	}
	e.cash = total
	log.Printf("[paper] 资金分配已自定义：%v 总现金 %.2f", e.pools, total)
	e.persist()
}

// ResetPoolAllocs 恢复资金分配为均分（清空自定义分配）：按当前池集合把总现金均分到各池。
// 保留每池持仓上限（poolMaxPos 不动）。持久化。
// English: restores the cash allocation to an even split (clears custom allocations): the total cash is
// split evenly across the current pool set. Per-pool position caps (poolMaxPos) are kept. Persisted.
func (e *Engine) ResetPoolAllocs() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pools) == 0 {
		return
	}
	total := 0.0
	for _, v := range e.pools {
		total += v
	}
	share := total / float64(len(e.pools))
	for k := range e.pools {
		e.pools[k] = share
	}
	e.cash = total
	log.Printf("[paper] 资金分配已恢复均分：每池 %.2f 总现金 %.2f", share, total)
	e.persist()
}

// Positions 返回持仓快照（按代码排序）。
// English: returns a snapshot of open positions (sorted by code).
func (e *Engine) Positions() []Position {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Position, 0, len(e.positions))
	for _, p := range e.positions {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// Holds 判断该模拟盘账本当前是否持有某代码（供按账号逐仓路由卖出信号使用）。
// English: reports whether this paper book currently holds the given code (used by per-account
// sell-signal routing so a sell only reaches the account that actually holds the position).
func (e *Engine) Holds(code string) bool {
	if code == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.positions[code]
	return ok && p != nil && p.Qty > 0
}

// Trades 返回成交记录（最新在前）。
// English: returns the fill records (newest first).
func (e *Engine) Trades() []Trade {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Trade, len(e.trades))
	copy(out, e.trades)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// Orders 返回订单生命周期记录（最新在前；阶段1.3 信号→订单→成交/拒绝 全留痕）。
// English: returns order-lifecycle records (newest first; full signal→order→outcome audit).
func (e *Engine) Orders() []Order {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Order, len(e.orders))
	copy(out, e.orders)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Equity 返回净值序列。
// English: returns the equity curve.
func (e *Engine) Equity() []EquityPoint {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]EquityPoint, len(e.equity))
	copy(out, e.equity)
	return out
}

// Stats 汇总绩效与信号质量指标（全账号）。
// English: aggregates performance and signal-quality metrics (whole account).
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statsFor(nil)
}

// PoolStats 返回某战法资金池的独立绩效/信号质量汇总（前端分仓 tab 统计卡用）。
// 收益基准 = 该池累计买入成本（按买入后计数），已实现盈亏 = 该池持久化 realized。
// English: returns a strategy pool's own performance/signal-quality summary (used by the frontend
// stat cards when that pool tab is selected). The return basis is the pool's cumulative buy cost
// (counted after buy); realized P&L is the pool's persisted realized value.
func (e *Engine) PoolStats(key string) Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statsFor(&key)
}

// statsFor 汇总绩效与信号质量。poolKey 为 nil = 全账号；否则仅统计该战法池
// （现金=池余量、持仓=池内持仓、成交=池内成交、收益基准=池累计买入成本）。
// 调用方须持锁。English: aggregates performance/signal quality. nil poolKey = whole account;
// otherwise scoped to that strategy pool (cash=pool balance, positions=fills scoped to the pool,
// return basis=pool cumulative buy cost). Caller must hold the lock.
func (e *Engine) statsFor(poolKey *string) Stats {
	global := poolKey == nil
	matches := func(t string) bool { return global || t == *poolKey }
	cash := e.cash
	if !global {
		cash = e.pools[*poolKey]
	}
	mv := 0.0
	openPos := 0
	for _, p := range e.positions {
		if matches(p.StrategyType) {
			mv += p.MarketValue()
			openPos++
		}
	}
	total := cash + mv
	st := Stats{
		Cash:          cash,
		MarketValue:   mv,
		TotalValue:    total,
		OpenPositions: openPos,
	}
	// §SHORT-3 做空侧统计并入全局总账（分池统计不动，融券是账户级而非战法池级）。
	// 启用时把做空权益计入 TotalValue/收益基准，并把做空池预算并入初始资金分母。
	// English: §SHORT-3 — fold short-book stats into the account-level summary (pools are per-strategy
	// and stay untouched). When funded, short equity is added to TotalValue and the short budget to
	// the initial-capital denominator, so returns stay honest about the extra deployed capital.
	if e.shortBookEnabledLocked() {
		shortEq := e.shortEquityLocked()
		margins, floatPnl := 0.0, 0.0
		for _, s := range e.shorts {
			margins += s.MarginUsed
			floatPnl += s.FloatPnl()
		}
		total += shortEq
		st.TotalValue = round2(total)
		st.ShortEnabled = true
		st.ShortCash = round2(e.shortCash)
		st.ShortMarginUsed = round2(margins)
		st.ShortFloatPnl = round2(floatPnl)
		st.ShortRealized = round2(e.shortRealized)
		st.ShortPositions = len(e.shorts)
		st.ShortEquity = round2(shortEq)
	}
	if global {
		st.InitialCapital = e.cfg.InitialCapital
		st.RealizedPnl = e.realized
		if e.shortBookEnabledLocked() {
			st.InitialCapital += e.cfg.ShortCapital // 融券预算计入总本金（收益分母与实际部署资金一致）
			st.RealizedPnl += e.shortRealized       // 已实现盈亏含融券侧结算
		}
		if st.InitialCapital > 0 {
			st.TotalReturnPct = (total - st.InitialCapital) / st.InitialCapital * 100
		}
		// 当日收益：最新净值 vs 前一交易日净值
		if len(e.equity) >= 2 {
			prev := e.equity[len(e.equity)-2].Value
			cur := e.equity[len(e.equity)-1].Value
			if prev > 0 {
				st.TodayReturnPct = (cur - prev) / prev * 100
			}
		}
		st.EquityCurvePoints = len(e.equity)
	} else {
		perf := e.poolPerf[*poolKey]
		if perf != nil {
			st.InitialCapital = perf.Cost
			st.RealizedPnl = perf.Realized
		}
		if st.InitialCapital > 0 {
			floating := 0.0
			for _, p := range e.positions {
				if p.StrategyType == *poolKey {
					floating += p.PnL()
				}
			}
			st.TotalReturnPct = (st.RealizedPnl + floating) / st.InitialCapital * 100
		}
	}

	// 胜率：按卖出记录相对对应持仓成本估算（简化：盈利卖出笔数 / 卖出笔数）
	wins, sells := 0, 0
	// 滑点统计：遍历买入成交，取每个买入的滑点与延迟
	var sumSlip, sumLat float64
	var maxLat int64
	for _, t := range e.trades {
		if !matches(t.StrategyType) {
			continue
		}
		if t.Side == "buy" {
			if t.SignalPrice > 0 {
				sumSlip += (t.Price - t.SignalPrice) / t.SignalPrice * 100
			}
			sumLat += float64(t.LatencySec)
			if t.LatencySec > maxLat {
				maxLat = t.LatencySec
			}
			st.FilledBuys++
			if t.SignalPrice > 0 {
				st.SlippageCost += (t.Price - t.SignalPrice) * float64(t.Qty)
			}
		} else {
			sells++
			// 查找对应买入价：按代码向前匹配最近一笔 buy
			for i := len(e.trades) - 1; i >= 0; i-- {
				bt := e.trades[i]
				if bt.Side == "buy" && matches(bt.StrategyType) && bt.Code == t.Code && bt.Time.Before(t.Time) {
					if (t.Price-bt.Price)*float64(t.Qty) > 0 {
						wins++
					}
					break
				}
			}
		}
	}
	if st.FilledBuys > 0 {
		st.AvgSlippagePct = sumSlip / float64(st.FilledBuys)
		st.AvgLatencySec = sumLat / float64(st.FilledBuys)
	}
	st.MaxLatencySec = maxLat
	if st.InitialCapital > 0 {
		st.SignalAmountPct = st.SlippageCost / st.InitialCapital * 100
	}
	if sells > 0 {
		st.WinRatePct = float64(wins) / float64(sells) * 100
	}
	st.TotalReturnPct = round2(st.TotalReturnPct)
	st.TodayReturnPct = round2(st.TodayReturnPct)
	st.AvgSlippagePct = round2(st.AvgSlippagePct)
	st.AvgLatencySec = math.Round(st.AvgLatencySec)
	st.SignalAmountPct = round2(st.SignalAmountPct)

	// §R2.2 绩效指标：从净值序列计算 Sharpe/最大回撤/Calmar
	eq := make([]float64, 0, len(e.equity))
	for _, ep := range e.equity {
		eq = append(eq, ep.Value)
	}
	pm := computePerfMetrics(eq, 244)
	st.SharpeRatio = math.Round(pm.Sharpe*100) / 100
	st.MaxDrawdown = math.Round(pm.MaxDrawdown*100) / 100
	st.Calmar = math.Round(pm.Calmar*100) / 100

	// §期望收益：总收益率 ÷ 成交笔数 = 每笔平均收益率（简化版，够用于趋势判断）
	if st.FilledBuys > 0 {
		st.Expectancy = math.Round(st.TotalReturnPct/float64(st.FilledBuys)*100) / 100
	}

	return st
}

// round2 四舍五入到分（两位小数），避免浮点尾差导致金额不一致。
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// 撮合错误定义：全部为中文可读消息，直接透传给前端弹窗/接口返回。
// （Fill errors: human-readable Chinese messages surfaced to the frontend directly.）
var (
	errDisabled    = errMsg("模拟盘未启用")
	errNotHeld     = errMsg("未持有该股票")
	errNoQuote     = errMsg("无实时行情，无法成交")
	errHeld        = errMsg("已持有该股票")
	errMaxPos      = errMsg("已达持仓数量上限")
	errLotTooSmall = errMsg("预算不足以买入一手（整手/额度不足）")
	errCash        = errMsg("可用资金不足")
	errTPlusOne    = errMsg("T+1限制：当日买入次日方可卖出")
)

// errMsg 让普通字符串可充当 error，避免为每个错误单独建类型。
// （errMsg lets a plain string act as an error without a dedicated type per case.）
type errMsg string

// Error 实现 error 接口（errMsg 为字符串类型的轻量错误包装）。
func (e errMsg) Error() string { return string(e) }

// SetInitialCapital 显式设定初始资金额（§反馈修复：配合 Reset 使用，
// 解决多次 Deposit 累加导致 cfg.InitialCapital 被抬高后清盘基数不对的问题）。
func (e *Engine) SetInitialCapital(v float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v > 0 {
		e.cfg.InitialCapital = v
	}
}

// ── 分仓纪律引擎（§用户反馈：因子战法触发太快打满预算；分仓买入没有严格规则）──

// PoolBuyRule 单个资金池的买入纪律规则。
// 所有字段为零值时该池不做额外限制（仅受全局持仓上限和池资金约束）。
type PoolBuyRule struct {
	MaxDailyBuys    int     `json:"max_daily_buys"`     // 每日最大买入次数（0=不限）
	CooldownMinutes int     `json:"cooldown_minutes"`   // 两次买入最小间隔分钟（0=不限）
	MinScore        float64 `json:"min_score"`          // 入场最低评分（0=不过滤；信号 Confidence≥此值才买）
	BudgetPctPerDay float64 `json:"budget_pct_per_day"` // 每日最多动用池资金的%（0=不限，如30=一天最多花池的30%）
}

// poolDiscipline 每池买入纪律运行时状态（不持久化，重启重置——保守安全）。
type poolDiscipline struct {
	buysToday    int       // 今日已买次数
	lastBuyAt    time.Time // 最近一次买入时间
	spentToday   float64   // 今日已花费金额（元）
	dayStartCash float64   // §修复 S7：本日开始时池现金（预算分母，避免越买分母越小越松）
	day          string    // 当前日期（跨日自动重置）
}

// checkPoolDisciplinePre 分仓纪律预检查（不需要 cost）：日限次数+冷却+评分门槛。
// §R7 修复：MinScore 此前从未被读取（寻优管线持续写入的门槛静默放行）。confidence 为信号
// 置信度（0~1）；manual=true 表示手动买入/加仓（用户自主决策，不受评分门槛约束）。
// §修复 P2#22：手动不再用 confidence==1.0 编码判别——真实高置信信号（置信度恰为 1.0）
// 会被旧判别误当成手动、绕过 MinScore；现以显式 manual 参数区分，刻度单标准。
// 口径兼容：MinScore>1 视为百分制评分（对比 confidence×100），≤1 视为 0~1 置信度。
// English: pool-discipline pre-check (daily count + cooldown + score gate). R7 fix: MinScore is now
// actually enforced — MinScore>1 is treated as a 0-100 scale vs confidence×100, otherwise 0-1.
// P2#22: manual is now an explicit flag instead of "confidence==1.0" — a genuine signal whose
// confidence just happens to be 1.0 was misread as manual and skipped the score gate.
func (e *Engine) checkPoolDisciplinePre(poolKey string, confidence float64, manual bool) string {
	now := time.Now()
	today := cntime.DayOf(now) // §TZ1

	d, ok := e.poolDiscipline[poolKey]
	if !ok || d.day != today {
		// §修复 S7（2026-08-29）+ FIX#4（2026-09-04）：跨日/首建条目在此刻记录"本日开始时池现金"
		// 作为预算分母。旧实现仅填 day、dayStartCash 恒为 0——首个买入当天 checkPoolBudget 读到的
		// 基准恒回退当前缩水池现金，S7 分母修复从未真正生效（越买分母越小越松）。
		// English: §FIX#4 — capture the pool's day-start cash right here, at the first buy pre-check of
		// the day (before any debit), so the budget denominator stays fixed for the whole day. The old
		// code only set `day`, leaving dayStartCash 0 and silently falling back to the shrinking pool.
		e.poolDiscipline[poolKey] = poolDiscipline{day: today, dayStartCash: e.pools[poolKey]}
		d = e.poolDiscipline[poolKey]
	}

	rule := e.poolBuyRules[poolKey]
	if rule == nil {
		return ""
	}

	// §DYN-SIZE-v3：dynamic 模式下每日买入次数退场；冷却/最低评分仍保留。
	if !e.dynamicBudgetOn() && rule.MaxDailyBuys > 0 && d.buysToday >= rule.MaxDailyBuys {
		return fmt.Sprintf("日买次数达上限(%d/%d)", d.buysToday, rule.MaxDailyBuys)
	}
	if rule.CooldownMinutes > 0 && !d.lastBuyAt.IsZero() {
		elapsed := now.Sub(d.lastBuyAt).Minutes()
		if elapsed < float64(float64(rule.CooldownMinutes)) {
			return fmt.Sprintf("冷却中(%.0f/%.0f分钟)", elapsed, float64(rule.CooldownMinutes))
		}
	}
	if rule.MinScore > 0 && !manual { // 手动买入/加仓不受评分门槛约束
		score := confidence
		if rule.MinScore > 1 {
			score = confidence * 100
		}
		if score < rule.MinScore {
			return fmt.Sprintf("评分低于门槛(%.0f<%.0f)", score, rule.MinScore)
		}
	}
	return ""
}

// checkPoolBudget 预算占比检查（需要 cost）。
func (e *Engine) checkPoolBudget(poolKey string, cost float64) string {
	if e.dynamicBudgetOn() {
		return ""
	}
	rule := e.poolBuyRules[poolKey]
	if rule == nil || rule.BudgetPctPerDay <= 0 || rule.BudgetPctPerDay >= 100 {
		return ""
	}
	d := e.poolDiscipline[poolKey]
	// §修复 S7（2026-08-29）：预算分母用"本日开始时池现金"而非当前缩水池现金，
	// 否则越买分母越小、限制越松，违背"全天动用≤池资金 X%"语义。
	baseCash := d.dayStartCash
	if baseCash <= 0 {
		baseCash = e.pools[poolKey] // 尚未记录日初现金时退回当前池现金
	}
	budget := baseCash * rule.BudgetPctPerDay / 100
	if d.spentToday+cost > budget {
		return fmt.Sprintf("日内预算超限(%.0f+%.0f>%.0f)", d.spentToday, cost, budget)
	}
	return ""
}

// recordPoolBuy 纪律检查通过后记录买入事件（更新计数和时间戳）。
func (e *Engine) recordPoolBuy(poolKey string, cost float64) {
	now := time.Now()
	today := cntime.DayOf(now) // §TZ1
	d, ok := e.poolDiscipline[poolKey]
	if !ok || d.day != today {
		// §修复 S7：跨日重置时记录本日开始时池现金作为预算分母基准。
		e.poolDiscipline[poolKey] = poolDiscipline{day: today, dayStartCash: e.pools[poolKey]}
		d = e.poolDiscipline[poolKey]
	}
	d.buysToday++
	d.spentToday += cost
	d.lastBuyAt = now
	e.poolDiscipline[poolKey] = d
}

// SetPoolBuyRule 设置单池买入纪律规则（nil=清除该池规则）。持久化。
// §A1 修复：此前缺 persist() 导致 API 下发的纪律重启即丢（与 SetPoolCaps/Allocs 不一致）。
func (e *Engine) SetPoolBuyRule(poolKey string, rule *PoolBuyRule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rule == nil {
		delete(e.poolBuyRules, poolKey)
	} else {
		e.poolBuyRules[poolKey] = rule
	}
	e.persist()
}

// errPoolDiscipline 分仓纪律拒绝（冷却/日限/门槛/预算）。
var errPoolDiscipline = errors.New("分仓纪律拒绝")
