// t_rounds.go — §T-MODE 做 T（2026-10-26 §DYN-T 四轮~六轮裁决后的**日内口径**实现）。
//
// 做 T 是什么：持有底仓时，用"当日买、当日卖"的一对腿把日内波动变成降本。A 股 T+1 下只有底仓可卖，
// 所以两个方向各有一条腿必须落在既有底仓上：
//   - **正 T**（先买后卖）：分时均线下方低吸机动仓 → 冲高后卖出**等量底仓**；
//   - **反 T**（先卖后买）：分时均线上方高抛一部分底仓 → 回落后买回等量（或按新口径不回补）。
//
// 本文件在 2026-10-26 被整体重写，因为 owner 报的第一条 bug 在 T 侧的翻版是**发起点用错了基准**：
// 旧实现 `ref := pos.AnchorOrCost()`，正 T 判"现价比成本低 2%"、反 T 判"现价比成本高 2%"，
// 且**开腿阈值与配对阈值共用同一个数**。成本口径回答的是"这笔投资赚没赚"，
// 日内回转要回答的是"现在是不是日内偏低的时点"——两个问题不同，用同一个数就必然出错：
// 单边上涨里现价可以整天都高于成本 2% 以上 ⇒ 一路反 T 卖飞；单边下跌里整天都低于成本 2% 以上
// ⇒ 反复正 T 低吸，越 T 越深。现在发起点全部改成**日内口径**（§8.4.1）。
//
// 判定顺序即安全（§8.4.6，逐只持仓走一遍，任一步不过就结束本票本轮并打印被哪一步拦）：
//
//	0   全局前置：开关/方向/交易时段/熔断日/影子/关键输入缺失/环境门/总量帽/V 可用性
//	0.5 流动性前置（成交额、换手、非 ST、上市天数）
//	1   状态收敛：有 open 腿 ⇒ 只处理这条腿（配对先于新开，绝不叠腿），处理完直接结束本票
//	2   次数帽：单码当日回转组数 ≥ max_rounds（防抖主力层）
//	3   时刻帽：≥ last_open_at 或在开盘冷静期内
//	4   日内位置：振幅门 → 偏离门（对分时均线）→ 近极值带门 → 涨跌停距离门 → V 与分钟链一致性
//	5   信号侧：正 T 要"新且活跃"的买入信号（候选 B）且未被消费；反 T 是否决门（无卖出裁决 + 距止盈止损够远）
//	6   战略让路：该股有未执行的战略买卖信号或在途单 ⇒ 不开（战略先消费本轮判定）
//	7   额度：单票 50% 帽 / T 可用现金 / 可卖量 / 反 T 当日总量帽余量
//	8   风控闸门：买入走与战略同一套路径，不留半态
//	9   落账：登记回转 + 消费信号 + 冻结回购款 + 全读数日志
//
// 三条硬约束（来自 Q13 实读，不是偏好）：两腿串行、不做两腿事务、不烧退出槽/不关生命周期/不改锚点。
// 幂等键 `t0:<code>:<方向>:<YYYYMMDD>:<seq>` 与退出/减仓键正交。
// 落地位置仍在 paper 包：回转状态必须与账本同锁、同一次原子写，放 engine 侧就要复制一套持久化。
//
// English: rewritten on 2026-10-26 to an intraday basis (the old cost/anchor reference made T rounds
// fire all day in a trending market), with a strict step order, a signal gate on the buy side
// (candidate B), veto gates on the sell side, liquidity prefilter, market-environment gates for both
// directions, a per-day sell-notional cap, and a standalone T P&L that never touches the position cost.
package paper

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

// 做 T 的枚举（字符串直接进账本与前端，改字面量等于改契约，故集中在此）。
const (
	TRModeBuyFirst  = "正T" // 先买后卖（低吸机动仓 → 卖等量底仓）
	TRModeSellFirst = "反T" // 先卖后买（高抛底仓 → 回踩买回或不回补）

	tRLegBuy  = "buy"  // 第一腿＝买入
	tRLegSell = "sell" // 第一腿＝卖出

	tRStateOpen    = "open"      // 已开腿，等第二腿
	tRStateDone    = "done"      // 两腿都成交，回转完成（唯一有 T 收益的状态）
	tRStateUnwound = "unwound"   // 半态收摊（可卖量不足/买回被拦/跨日），仓位是被动变更
	tRStateConvert = "converted" // 尾盘未配对且**无活跃买入信号** ⇒ 转战略持仓/战略减仓（六轮：只有信号才回补）
)

// tRMinBaseKeep 反 T 卖腿必须留下的底仓股数（1 手）。卖光会让持仓变成 Qty=0 僵尸行或走清仓分支
// （删行 + 再入场冷却），两条路都会让买回腿被自家闸门挡死。
const tRMinBaseKeep = 100

// tRKeepRounds 回转账保留条数上限（账本体积，不影响判定）。
const tRKeepRounds = 300

// tRStrategies T 腿在订单留痕里用的战法名（刻意不注册 strategy 枚举、不进白名单：T 是执行手法）。
const tRStrategies = "§T-MODE"

// tRFrozenFeeBufferPct 反 T 冻结回购款的默认费用缓冲（%）：买回时要有钱付佣金印花税，
// 专款留 0.6% 与实盘预算口径同值，宁可多冻一点也不让买回因几分钱失败。
const tRFrozenFeeBufferPct = 0.6

// TRound 一次做 T 回转的记账单元（Q11：独立回转账，不塞进 report.ExecLog——
// "一仓一生命周期"是它的既有不变量，日内多次回转会直接打穿）。
type TRound struct {
	ID   string `json:"id"`   // 幂等键 t0:<code>:<dir>:<YYYYMMDD>:<seq>
	Code string `json:"code"` // 标的代码
	Name string `json:"name"` // 标的名称
	Mode string `json:"mode"` // 正T / 反T
	Seq  int    `json:"seq"`  // 当日第几轮（1 起）
	// Leg 第一腿方向（buy/sell）；State=open 时它就是"此刻还欠哪条腿"的反面。
	Leg      string    `json:"leg"`
	LegQty   int       `json:"leg_qty"`   // 第一腿成交股数
	LegPrice float64   `json:"leg_price"` // 第一腿成交价（含滑点，第二腿的价差基准）
	LegAt    time.Time `json:"leg_at"`    // 第一腿成交时刻
	// BaseQty 第一腿成交**前**的底仓量：正 T 的卖出腿上界、反 T 的卖出量上界都从它派生。
	BaseQty int    `json:"base_qty"`
	State   string `json:"state"` // open / done / unwound / converted
	// PairQty/PairPrice/PnL 第二腿成交与本次回转的锁定价差（元，未扣第二腿费用）。
	PairQty   int       `json:"pair_qty,omitempty"`
	PairPrice float64   `json:"pair_price,omitempty"`
	PnL       float64   `json:"pnl,omitempty"`
	ClosedAt  time.Time `json:"closed_at,omitempty"`
	Day       string    `json:"day"`              // 开腿日 YYYYMMDD（限次与跨日收摊的判据）
	Reason    string    `json:"reason,omitempty"` // 收摊/转换/成交原因

	// ── §DYN-T v4 新增字段 ────────────────────────────────────────────────────────
	// AnchorPrice 开腿时的卖出裁决锚点。T 腿不得改动它，这里只留证（§6.1 锁 11）。
	AnchorPrice float64 `json:"anchor_price,omitempty"`
	// VWAP/Vmin/CheckDevPct 开腿时刻的分时均线读数：主源、分钟链复算值、两者偏差%。
	// 为什么三个都存：灰度期唯一能复盘"发起点对不对"的证据，也是回答"程序里的 V 和图上那条线
	// 是不是同一个数"的凭据（§8.4.1 的命名结论靠它验证）。
	VWAP          float64 `json:"v,omitempty"`
	Vmin          float64 `json:"v_min,omitempty"`
	CheckDevPct   float64 `json:"check_dev_pct,omitempty"`
	DevPct        float64 `json:"dev_pct,omitempty"`         // 现价相对分时均线的偏离%
	High          float64 `json:"high,omitempty"`            // 日内高点（开腿时刻读数）
	Low           float64 `json:"low,omitempty"`             // 日内低点
	AmplitudePct  float64 `json:"amplitude_pct,omitempty"`   // 当日振幅%
	AmountWan     float64 `json:"amount_wan,omitempty"`      // 当日成交额（万元，流动性前置读数）
	TurnoverPct   float64 `json:"turnover_pct,omitempty"`    // 换手率%
	ListedDays    int     `json:"listed_days,omitempty"`     // 上市天数
	UsedSignalID  string  `json:"used_signal_id,omitempty"`  // 本次消费的买入信号 ID（§8.4.3 一次性消费）
	FrozenCash    float64 `json:"frozen_cash,omitempty"`     // 反 T 冻结回购资金（含费用缓冲）
	MarketScaleAt float64 `json:"market_scale_at,omitempty"` // 开腿时的 MS（事后核"环境门当时是多少"）
	// TPnlGross/TPnlNet/QMatched/FeeEst 「做 T 收益」独立口径（六轮 T6-5 / §8.9a）。
	// 刻意与 PnL 并存：PnL 是旧字段（未扣费），TPnlNet 是**已扣两腿费用**的净额，只有 done 轮有值。
	TPnlGross float64 `json:"t_pnl_gross,omitempty"`
	TPnlNet   float64 `json:"t_pnl_net,omitempty"`
	QMatched  int     `json:"q_matched,omitempty"`
	FeeEst    bool    `json:"t_fee_est,omitempty"`

	// ── §T-COST B1（2026-10-27）成本分解：与 engine.TRoundLive **同名同义** ────────────────
	// 这十个字段只记账、不改任何判定。两本账必须字段同集（门禁用 reflect 比 json tag），
	// 否则会出现"纸面能算、实盘算不了"的半截实现——那种分叉在复盘时最难发现。
	// DecisionPrice 开腿判定时用的报价（＝openLegForCodeLocked 里那个 q.Price）。
	// 只锚**第一腿**：第二腿由价差阈值触发，没有"判定报价"这回事，硬给它编一个只会让滑点口径含混。
	DecisionPrice float64 `json:"decision_price,omitempty"`
	// SlipBps (fill − decision)/decision×1e4；买腿取正、卖腿取负后**再取正**（正数＝对我不利）。
	// 纸面的 fill 已含配置的 SlippageBps，所以这里读到的是"模拟器口径的滑点"，与实盘同义不同源。
	SlipBps float64 `json:"slip_bps,omitempty"`
	// FeeBuy/FeeSell 分腿费用（佣金，含最低佣金封底）；StampTax 仅卖出腿的印花税。
	// 三者之和必须等于 TPnlGross − TPnlNet（容差 0.01），这是 B1 的第一条判据。
	FeeBuy   float64 `json:"fee_buy,omitempty"`
	FeeSell  float64 `json:"fee_sell,omitempty"`
	StampTax float64 `json:"stamp_tax,omitempty"`
	// FeeSource broker / estimate / mixed：纸面恒 estimate（模拟成交没有券商回报）。
	FeeSource string `json:"fee_source,omitempty"`
	// LatencyMs 判定 → 委托落库的毫秒。纸面恒 0：模拟是即时撮合，没有"委托在途"这个阶段。
	LatencyMs int `json:"latency_ms"`
	// Unfilled 该轮是否有"下了单但没成交/被撤"的腿。纸面恒 false：拒单根本不会生成回转记录，
	// 配对失败则保持 open 等下一轮——这两者在纸面都不是"已下单未成交"的形态。
	Unfilled bool `json:"unfilled"`
	// UnfilledReason 撤单/废单/超时原因码（纸面为空；实盘由委托台账的终态填充）。
	UnfilledReason string `json:"unfilled_reason,omitempty"`
	// ParticipationPct 该腿成交额 / **开腿时已累计的当日成交额** × 100（B4 的观察量）。
	// 分母口径写死：用当时累计额（q.Amount），不是全天成交额——盘中的"全天量"是未来数据，
	// 拿它当分母会系统性低估参与率，读数越"安全"越接近自欺。
	ParticipationPct float64 `json:"participation_pct,omitempty"`
}

// TParams 一轮做 T 的执行参数——由 engine 侧从账号级配置解析后注入。
// paper 包不读 config（保持"纯核算"分工），所以阈值与外部读数都走入参；
// 好处是同一套编排可以在测试里用任意阈值驱动，不必构造配置文件。
//
// 回调为 nil 时的处置一律是**不开腿**（fail-closed）：日内口径依赖这些读数，
// 缺读数还硬开等于在盲区里做双向操作（§8.13 C 组第 10/11 条，owner 已同意）。
// English: every threshold and external reading is injected; a nil callback blocks the round rather
// than degrading to the old cost basis.
type TParams struct {
	// ── 开关与环境 ──
	Enabled         bool    // t.enabled（六轮：实盘也默认开）
	BuyFirst        bool    // 允许正 T
	SellFirst       bool    // 允许反 T
	MaxRounds       int     // 单码单日最大回转组数（防抖主力，灰度期=1）
	CapPct          float64 // 单票 T 上限（占该股当前持仓%）
	MinSpreadPct    float64 // 第二腿配对价差%
	ForceUnwindHM   string  // 尾盘判定时刻 HHMM
	MarketScale     float64 // 本轮 MarketScale（账户级，逐票不漂）
	MissingKeyInput bool    // 关键市场读数缺失 ⇒ 整个 T 关闭（§1.5）
	Shadow          bool    // 影子期 ⇒ 整个 T 关闭
	DayBlocked      bool    // 熔断日 ⇒ 两向都不开
	// ── 日内口径门槛（§8.4.1/§8.4.4）──
	VWAPDevPct       float64 // 偏离门（现价相对分时均线）
	MinAmplitudePct  float64 // 振幅门
	NearExtremePct   float64 // 近极值带
	LimitDistancePct float64 // 距涨/跌停最小距离
	OpenCoolingMin   float64 // 开盘冷静期（分钟）
	LastOpenHM       string  // 最后开新腿时刻 HHMM（硬顶 14:45 由配置层钳好）
	VWAPCheckTolPct  float64 // 主源与分钟链复算的允许偏差%
	// ── 两道方向门（§8.4.1 四轮/六轮）──
	MinMarketScale          float64 // 正 T 环境门
	SellEnvMinMarketScale   float64 // 反 T 环境门（极端弱势就是不做 T）
	MarketPlungePct         float64 // 大盘急跌（负值）停正 T
	IntradaySellCapTotalPct float64 // 当日反 T 卖腿市值帽（分母 min(总资产,持仓市值)）
	// ── 否决与熔断（§8.4.2/§8.5.2）──
	StopGuardPct   float64 // 距止盈/止损线小于该值 ⇒ 否决反 T
	LossBreakerPct float64 // 亏损超过该值 ⇒ 封买回通道
	// RoundStopPct 本笔 T 止损（**价差率 %，不含费用**）。§W-06-R4 新增，全系统唯一的止损口径：
	// 盘中 accelReason 的 "stop" 触发源与尾盘买回许可（allowBuyBack 同款）都读它。
	// Deprecated 的是上面那个 LossBreakerPct —— 字段保留只为兼容历史配置与前端表单，
	// 判定侧一个字都不许再引用它（门禁 #26 按"判定表达式"计数，注释里也别写出那个写法）。
	RoundStopPct float64
	// ── §W-09 行情门（形态 + 量能；实盘与纸面共用同一组字段与文案）──
	// VolRatioMax 放量门阈值：折算量比 ≥ 该值 ⇒ 不做该票的 T。**0 合法＝不设该条**。
	VolRatioMax float64
	// VolRatio 折算量比读数（nil=无源 ⇒ 按"读不到"处理，不按"没放量"放行）。
	VolRatio func(code string) (float64, bool)
	// MarketState bull/range/bear；""=T 行情门已关或读数从未产生 ⇒ **弃权**（不是拦）。
	// 与 MarketScale 是两回事：MS 是"该买多少"的定权标量，表达不了"盘整还是趋势"。
	MarketState string
	// MarketStateAt 该读数的时刻（零值=从未写过）⇒ 新鲜度门用，过期按弃权处理。
	MarketStateAt time.Time
	// MarketStateVotes/Total 研判票数，**只进日志不做门**：票数不足时状态机已把结论降级成 range
	// （research/market_state.go），而 range 正是放行档 ⇒ 再拦一次等于把放行又收回去。
	MarketStateVotes      int
	MarketStateVotesTotal int
	// ── 流动性前置（§8.4.4b）──
	MinAmountWan   float64
	MinTurnoverPct float64
	MinListedDays  float64
	// IndexChangePct 本轮大盘涨跌幅%（NaN/0 由调用方决定语义）；FeeBufferPct 冻结款费用缓冲%。
	IndexChangePct float64
	FeeBufferPct   float64
	// ── 外部读数回调 ──
	// MinuteBasis 返回分钟链复算的分时均线（校验腿）；ok=false ⇒ 只记降级，不拦。
	MinuteBasis func(code string) (vmin float64, ok bool)
	// NewBuySignal 返回"比 since 更新的活跃买入信号"（候选 B）；ok=false ⇒ 正 T 不开。
	NewBuySignal func(code string, since time.Time) (signalID string, ok bool)
	// ActiveBuySignal 返回尾盘是否具备活跃买入信号（§8.5.4：开腿时刻之后的活跃信号）。
	ActiveBuySignal func(code string, since time.Time) (signalID string, ok bool)
	// ActiveSellVerdict 该票此刻是否有活跃卖出/退出裁决（有 ⇒ 反 T 否决，交给信号执行）。
	ActiveSellVerdict func(code string) bool
	// StopLineDistance 现价距止盈/止损线的最小距离%；ok=false ⇒ 按否决处理。
	StopLineDistance func(code string, price float64) (pct float64, ok bool)
	// StockMeta 上市天数与是否 ST；ok=false ⇒ 流动性前置按不过。
	StockMeta func(code string) (listedDays int, isST bool, ok bool)
	// StrategicBusy 该股有未执行的战略买卖信号或在途单 ⇒ T 让路（§8.7）。
	StrategicBusy func(code string) bool
	// StrategicGate 转战略/尾盘回补都要重走的战略闸门（§8.5.3）；返回放行的数量。
	StrategicGate func(code string, qty int) (allowed int, reason string)
	// ConsumeSignal 把信号 ID 记为"已被 T 腿消费"（§8.4.3 ①，账本在 engine/paper 两侧各自持有）。
	ConsumeSignal func(signalID string)
	// SignalConsumedByStrategic 该信号是否已被战略买单消费（T 不得重复买第二次）。
	SignalConsumedByStrategic func(signalID string) bool
}

// RunIntradayT 做 T 的每轮执行入口（交易时段内由纸面估值轮调用）。
// 前置四道：账本关、自动卖出关、方向都没开、预算口径不是 dynamic（Q5）；
// v4 再加两道全局关闭：`Enabled=false` 与"影子/关键输入缺失"（§1.5——读数不可信时整个 T 停，
// 而不是"按半额度照做"，盲区里做日内双向操作比不做贵得多）。
func (e *Engine) RunIntradayT(quotes map[string]*data.StockInfo, p TParams) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.cfg.Enabled || !e.cfg.AutoSell || !p.Enabled {
		return
	}
	if !p.BuyFirst && !p.SellFirst {
		return
	}
	if p.Shadow || p.MissingKeyInput {
		e.tNoteBlockLocked("step0", "影子期/关键输入缺失→本轮整体不做T", "")
		return
	}
	// §8.8 配置自洽告警（**不阻塞**）：配对价差门若高过振幅下限的 80%，这条回转在数学上就几乎配不上对
	// ——开腿时当日振幅才 2.5%，却要第二腿走出 2.0% 的有利价差，尾盘大概率只能收摊。
	// 之所以只告警不拦：这两个键都是 owner 显式可调的保护阈值，拦下来等于替人改配置；
	// 但"配了一对互相打架的值然后静默什么都不做"是本项目吃过多次的形态，必须每天出声一次。
	if p.MinSpreadPct > 0 && p.MinAmplitudePct > 0 && p.MinSpreadPct > 0.8*p.MinAmplitudePct {
		if e.tCfgWarnDay != cntime.DayCompactOf(e.clockNow()) {
			e.tCfgWarnDay = cntime.DayCompactOf(e.clockNow())
			log.Printf("[paper] §T-MODE 配置告警：min_spread_pct=%.2f 已达 min_amplitude_pct=%.2f 的 80%% 以上，"+
				"开腿后很可能配不上对（尾盘多走收摊/转换）；不阻塞，按现值继续",
				p.MinSpreadPct, p.MinAmplitudePct)
		}
	}
	if !e.dynamicBudgetOn() {
		now := time.Now()
		day := cntime.DayOf(now)
		if e.tWarnDay != day {
			e.tWarnDay = day
			log.Printf("[paper] §T-MODE 做T已配置但预算口径=%q（非 dynamic）→ 按 Q5 本日禁用", e.cfg.BudgetMode)
		}
		return
	}
	e.runIntradayTLocked(quotes, p, e.clockNow())
}

// runIntradayTLocked 一轮主流程（须持锁调用）。次序是硬要求：**先配平已开腿，再考虑开新腿**。
// 反过来的话同一轮里可能先开一条新腿、又去配平另一条旧腿，两条腿时间上重叠——
// 那既是 Q13 第 4 条要避开的并发形态，也是"同一时刻同码挂着反向两笔委托"的合规风险来源。
func (e *Engine) runIntradayTLocked(quotes map[string]*data.StockInfo, p TParams, now time.Time) {
	day := cntime.DayCompactOf(now)
	e.rollTRoundsLocked(day, now, p)

	// step1：状态收敛。有 open 腿的票本轮只处理这条腿，处理完结束（绝不叠腿）。
	for _, code := range e.tOpenCodesLocked() {
		// §W-06-R4 P3：与实盘 premiseGone 同判据（熔断日反 T 只能转战略减仓）。
		// 纸面守卫比实盘少两条（在途委托 / 腿龄）：纸面是同步撮合、没有委托在途态，
		// 且 paper.TRound 没有 OpenedAt —— 不存在"异步回报导致价格基准陈旧"。
		if state, why := e.premiseGoneLocked(code, p); state != "" {
			r := e.openTRoundFor(code)
			if r != nil {
				e.finishTRoundLocked(r, state, 0, 0, 0, why, now, 0)
			}
			continue
		}
		e.pairTRoundLocked(code, quotes, p, e.tForceReached(now, p.ForceUnwindHM), now)
	}
	// 尾盘判定时刻之后**不再开新腿**（旧实现是"尾盘只收摊"，但配平时刻与停开时刻是两个概念：
	// last_open_at 之后开的腿大概率当天配不上对，只能带着半态进收盘竞价）。
	if e.tForceReached(now, p.ForceUnwindHM) {
		return
	}
	e.openTRoundsLocked(quotes, p, day, now)
}

// rollTRoundsLocked 跨日收摊：昨日仍 open 的回转显式标终态（须持锁调用）。
// 正 T 买腿过夜＝机动仓悄悄变成底仓；反 T 卖腿过夜＝那次高抛退化成战略减仓。
// 两者都不是崩溃，但**必须在账上说出来**，否则复盘只看到仓位多了/少了一块而找不到原因。
func (e *Engine) rollTRoundsLocked(day string, now time.Time, p TParams) {
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State != tRStateOpen || r.Day == day {
			continue
		}
		if r.Leg == tRLegBuy {
			r.State = tRStateUnwound
			r.Reason = "跨日未配平：机动仓已成隔夜底仓（正T买腿未卖出等量底仓）"
		} else {
			// 反 T 跨日不回补：按战略减仓记账（converted），与"尾盘只有信号才回补"同口径。
			r.State = tRStateConvert
			r.Reason = "跨日未回补：反T卖腿转战略减仓（§8.5.2 只有信号才回补）"
		}
		r.ClosedAt = now
		log.Printf("[paper] §T-MODE 回转跨日收摊 %s %s 第%d轮 腿=%s %d股@%.2f → %s %s",
			r.ID, r.Mode, r.Seq, r.Leg, r.LegQty, r.LegPrice, r.State, r.Reason)
	}
}

// tForceReached 是否已到/过尾盘判定时刻（HHMM 字符串比较即时间比较）。
func (e *Engine) tForceReached(now time.Time, hm string) bool {
	if len(hm) != 4 {
		return false
	}
	return cntime.In(now).Format("1504") >= hm
}

// tLastOpenPassed 是否已过"最后开新腿时刻"。与 force 时刻分开是刻意的：
// 默认 14:00 停开新腿、14:50 才做尾盘判定，中间那 50 分钟专门用来配平已有腿。
func tLastOpenPassed(now time.Time, hm string) bool {
	if len(hm) != 4 {
		return false
	}
	return cntime.In(now).Format("1504") >= hm
}

// tInCooling 是否还在开盘冷静期内（默认 9:30 起 15 分钟）。
// 为什么要这道门：早盘样本少的时候分时均线与现价几乎重合，偏离门自动失效，
// 此刻的"低吸/高抛"读数是噪声而不是信息（方案 §8.4.4）。
func tInCooling(now time.Time, minutes float64) bool {
	if minutes <= 0 {
		return false
	}
	cst := cntime.In(now)
	open := time.Date(cst.Year(), cst.Month(), cst.Day(), 9, 30, 0, 0, cst.Location())
	return cst.Before(open.Add(time.Duration(minutes) * time.Minute))
}

// tOpenCodesLocked 当前处于 open 态的回转代码（排序后返回，保证每轮处理次序稳定可复现）。
func (e *Engine) tOpenCodesLocked() []string {
	seen := make(map[string]bool, len(e.tRounds))
	out := make([]string, 0, len(e.tRounds))
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State == tRStateOpen && !seen[r.Code] {
			seen[r.Code] = true
			out = append(out, r.Code)
		}
	}
	sort.Strings(out)
	return out
}

// openTRoundFor 该代码当前处于 open 态的回转（0 或 1 条——两腿串行的结构性保证）。
func (e *Engine) openTRoundFor(code string) *TRound {
	for i := range e.tRounds {
		if e.tRounds[i].Code == code && e.tRounds[i].State == tRStateOpen {
			return &e.tRounds[i]
		}
	}
	return nil
}

// lastRoundEndedAt 该代码上一次回转结束时刻（**候选 B** 的新鲜度基准，§8.4.2）。
// 当日无历史回转 ⇒ 返回当日 9:30：也就是"今天新出现的信号才算数"，
// 老持仓那条恒成立的"持有中"信号不能再拿来开正 T 腿（否则信号门形同虚设）。
func (e *Engine) lastRoundEndedAt(code string, now time.Time) time.Time {
	cst := cntime.In(now)
	dayStart := time.Date(cst.Year(), cst.Month(), cst.Day(), 9, 30, 0, 0, cst.Location())
	best := dayStart
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.Code != code || r.State == tRStateOpen || r.ClosedAt.IsZero() {
			continue
		}
		if r.ClosedAt.After(best) {
			best = r.ClosedAt
		}
	}
	return best
}

// tRoundsUsedLocked 该代码当日已占用的回转组数（含在途与已收摊：一次回转＝一组完整反向动作）。
func (e *Engine) tRoundsUsedLocked(code, day string) int {
	n := 0
	for i := range e.tRounds {
		if e.tRounds[i].Code == code && e.tRounds[i].Day == day {
			n++
		}
	}
	return n
}

// tSignalConsumedLocked 该信号 ID 是否已被某条 T 腿消费（§8.4.3 ①，账本就在这张回转表里）。
func (e *Engine) tSignalConsumedLocked(signalID string) bool {
	if strings.TrimSpace(signalID) == "" {
		return false
	}
	for i := range e.tRounds {
		if e.tRounds[i].UsedSignalID == signalID {
			return true
		}
	}
	return false
}

// sellFirstNotionalLocked 当日反 T 卖腿已占用的市值合计（总量帽分子，§8.4.5 / 六轮 T6-2）。
// 用成交股数×成交价（不是现价）：帽约束的是"今天已经卖掉多少底仓"，
// 用现价会让已经卖出的那部分随行情涨跌而重新计数，判定就成了移动靶。
func (e *Engine) sellFirstNotionalLocked(day string) float64 {
	sum := 0.0
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.Day != day || r.Leg != tRLegSell || r.LegQty <= 0 {
			continue
		}
		sum += r.LegPrice * float64(r.LegQty)
	}
	return sum
}

// tFrozenCash 当前全部在途反 T 腿的冻结回购资金合计（战略买入不得动用，§8.3 纪律 3）。
// 导出给策略预算与面板共用同一本账，避免"策略把做 T 的回购钱花掉了"这种半态来源。
func (e *Engine) tFrozenCash() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tFrozenCashLocked()
}

// TRoundOpenBuyLegs 在途正 T 买腿的市值合计（供目标仓位模型剔出战略当前数）。
func (e *Engine) TRoundOpenBuyLegs() (value float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State == tRStateOpen && r.Leg == tRLegBuy {
			value += r.LegPrice * float64(r.LegQty)
		}
	}
	return value
}

// TRoundOpenSellLegs 在途反 T 卖腿的市值合计（供目标仓位模型加回战略当前数）。
func (e *Engine) TRoundOpenSellLegs() (value, frozen float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State == tRStateOpen && r.Leg == tRLegSell {
			value += r.LegPrice * float64(r.LegQty)
			frozen += r.FrozenCash
		}
	}
	return value, frozen
}

// ── 被拦留痕（§8.9：按 step 编号统计"这只票为什么今天没做 T"）───────────────────────────

// tNoteBlockLocked 记一条"被哪一步拦"。日志按 (step, code) 每日一次防刷屏（做 T 驱动约 5s 一轮），
// 计数则每次累加——灰度期看的是**分布**：step2 最多说明次数帽太紧，step4 最多说明行情不给振幅。
func (e *Engine) tNoteBlockLocked(step, why, code string) {
	day := cntime.DayCompactOf(e.clockNow())
	if e.tBlockDay != day {
		e.tBlockDay = day
		e.tBlock = map[string]int{}
		e.tBlockLogged = map[string]bool{}
	}
	if e.tBlock == nil {
		e.tBlock = map[string]int{}
	}
	key := step + "|" + code
	e.tBlock[key]++
	if e.tBlockLogged == nil {
		e.tBlockLogged = map[string]bool{}
	}
	if e.tBlockLogged[key] {
		return
	}
	e.tBlockLogged[key] = true
	log.Printf("[paper] §T-MODE 不开腿 %s 拦在 %s：%s", orDash(code), step, why)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// TBlockStats 返回当日"被哪一步拦"的计数快照（接口与面板用）。
func (e *Engine) TBlockStats() (day string, counts map[string]int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int, len(e.tBlock))
	for k, v := range e.tBlock {
		out[k] = v
	}
	return e.tBlockDay, out
}

// ── step1/尾盘：配对与收摊 ────────────────────────────────────────────────────────────

// pairTRoundLocked 处理一条已开腿的第二腿（须持锁调用）。
//
// 正 T（Leg=buy）：现价相对买价走出 `min_spread_pct` 有利价差 ⇒ 卖出**等量底仓**；
// 到判定时刻（force）**无条件**卖出等量底仓、亏损也卖——买腿是新增敞口，敞口过夜需要许可，
// 减敞口不需要（owner 三轮裁决里唯一保留的"时钟义务"就是这一条）。
//
// 反 T（Leg=sell）：
//
//	· 盘中配对（回落 ≥ min_spread_pct）⇒ 买回等量，规则驱动不要求买入信号；
//	· 尾盘未配对 ⇒ **只有活跃买入信号才回补**，且必须重走战略闸门（§8.5.3）；
//	  亏损超过 `loss_breaker_pct` ⇒ 熔断买回通道，转战略减仓并告警；
//	  无信号 ⇒ `converted`（仓位主动变更，不是被动收摊，账务与 T 收益都分开）。
func (e *Engine) pairTRoundLocked(code string, quotes map[string]*data.StockInfo, p TParams, force bool, now time.Time) {
	r := e.openTRoundFor(code)
	if r == nil {
		return
	}
	pos := e.positions[code]
	if pos == nil || pos.Qty <= 0 {
		e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0, "持仓已不在账，回转作废", now, 0)
		return
	}
	price := tQuotePrice(quotes, code)
	if price <= 0 {
		return // 无有效价：不下结论、不动状态（与自动卖出的行情守卫同口径）
	}

	if r.Leg == tRLegBuy {
		need := r.LegQty
		// §W-06-R4 加速配平：与实盘同一出口（三个触发源共用），accelTag 落原因与日志。
		accelTag := ""
		fav := (price - r.LegPrice) / r.LegPrice * 100
		if !force {
			if accel := e.accelReasonLocked(r, price, p); accel == "" {
				if fav < p.MinSpreadPct {
					return // 价差没到：继续等（不追高卖）
				}
			} else {
				accelTag = accel
			}
		}
		sellable := e.sellableQtyLocked(pos, now)
		if sellable < need {
			if force {
				e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0,
					fmt.Sprintf("尾盘强制配平失败：可卖%d<需卖%d，机动仓将隔夜", sellable, need), now, 0)
				return
			}
			// 裁决 A：加速路径失败即收摊（语义是"不能再等"），非加速路径保持既有行为
			// （保持 open、下一轮再试）一个字不动。
			if accelTag != "" {
				e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0,
					fmt.Sprintf("加速配平失败（accel=%s）：可卖%d<需卖%d ⇒ 收摊不追（机动仓隔夜）",
						accelTag, sellable, need), now, 0)
			}
			return
		}
		sold, err := e.sellQtyLocked(pos, price, need, tRStrategies+" 正T卖出腿 "+r.ID)
		if err != nil {
			if accelTag != "" {
				e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0,
					"加速配平失败（accel="+accelTag+"）：配对卖腿被拒："+err.Error(), now, 0)
				return
			}
			log.Printf("[paper] §T-MODE 正T卖出腿被拒 %s: %v（回转保持 open，下一轮再试）", code, err)
			return
		}
		fill := e.applySellPriceLocked(price)
		e.finishTRoundLocked(r, tRStateDone, sold, fill, (fill-r.LegPrice)*float64(sold),
			fmt.Sprintf("正T配对完成：买%.2f→卖%.2f 价差%.2f%%%s", r.LegPrice, fill, fav,
				tern(force, "（尾盘强制配平）", "")), now, sold)
		e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
			StrategyType: pos.StrategyType, Side: "sell", Kind: "正T卖出", Price: fill,
			Qty: sold, Status: "filled", Reason: tRStrategies + " " + r.ID, CreatedAt: now})
		// B5：配对腿成交＝又一条被影子账跳过的 T 腿（判据：skip == 当日 T 腿数）。
		e.tShadowSkipTLegLocked(1, now)
		return
	}

	// 反 T：第一腿已卖出，第二腿是买回。
	fav := (r.LegPrice - price) / r.LegPrice * 100
	need := r.LegQty
	// §W-06-R4：与正 T 同一出口；反 T 的两处失败本来就无条件收摊，这里只补加速标签便于复盘分组。
	accelRT := ""
	if force {
		// §W-06-R4：与实盘 allowBuyBack 换成**同一个口径**（整笔价差率 + round_stop_pct）。
		// 旧口径是单腿价差率 + loss_breaker_pct(3.0)；两侧必须同批换，否则纸面与实盘的
		// converted/done 分布不可比，灰度期唯一的对照物就废了。
		pct, _, okSpread := tRoundSpreadPct(r, price)
		if !okSpread {
			e.finishTRoundLocked(r, tRStateConvert, 0, 0, 0,
				"买回熔断判定不可得：本笔价差率算不出（首腿未成交/本金为 0）⇒ 封买回通道，转战略减仓", now, 0)
			return
		}
		if p.RoundStopPct > 0 && pct <= -p.RoundStopPct {
			e.finishTRoundLocked(r, tRStateConvert, 0, 0, 0,
				fmt.Sprintf("买回熔断：价差率 %.2f%% ≤ −%.2f%% ⇒ 封买回通道，转战略减仓并告警",
					pct, p.RoundStopPct), now, 0)
			log.Printf("[paper] §T-MODE t-buyback-breaker 熔断 %s(%s) 价差率%.2f%% 阈值-%.2f%%",
				r.Code, r.Name, pct, p.RoundStopPct)
			return
		}
		sigID, hasSig := "", false
		if p.ActiveBuySignal != nil {
			sigID, hasSig = p.ActiveBuySignal(code, r.LegAt)
		}
		if !hasSig {
			e.finishTRoundLocked(r, tRStateConvert, 0, 0, 0,
				"尾盘无活跃买入信号 ⇒ 不回补，转战略减仓（§8.5.2 作废时钟义务）", now, 0)
			log.Printf("[paper] §T-MODE 尾盘不回补 %s(%s) 卖%d股@%.2f → converted",
				r.Code, r.Name, r.LegQty, r.LegPrice)
			return
		}
		if e.tSignalConsumedLocked(sigID) ||
			(p.SignalConsumedByStrategic != nil && p.SignalConsumedByStrategic(sigID)) {
			e.finishTRoundLocked(r, tRStateConvert, 0, 0, 0,
				"尾盘回补要用的信号已被消费 ⇒ 不再买回（§8.4.3 一次性消费）", now, 0)
			return
		}
		allowed := need
		if p.StrategicGate != nil {
			a, reason := p.StrategicGate(code, need)
			if a <= 0 {
				e.finishTRoundLocked(r, tRStateConvert, 0, 0, 0,
					"尾盘回补被战略闸门拦下 ⇒ 转战略减仓："+reason, now, 0)
				return
			}
			allowed = a
		}
		if allowed < need {
			e.finishTRoundLocked(r, tRStateConvert, 0, 0, 0,
				fmt.Sprintf("尾盘回补仅部分过闸门 %d/%d ⇒ 差额转战略减仓（§8.5.3）", allowed, need), now, 0)
			return
		}
		r.UsedSignalID = sigID
	} else {
		if accel := e.accelReasonLocked(r, price, p); accel == "" {
			if fav < p.MinSpreadPct {
				return // 盘中配对只看价差
			}
		} else {
			accelRT = accel
		}
	}
	if err := e.addToPositionLocked(pos, pos.Code, pos.Name, pos.Strategy, price, price, need,
		tRStrategies+" 反T买回腿 "+r.ID); err != nil {
		e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
			StrategyType: pos.StrategyType, Side: "buy", Kind: "反T买回", Price: price, Qty: need,
			Status: "rejected", Reason: "买回被拦，按战略减仓收摊: " + err.Error(), CreatedAt: now})
		e.finishTRoundLocked(r, tRStateUnwound, 0, 0, 0,
			"买回被拦，按战略减仓收摊（不追买）: "+err.Error()+tPaperAccelSuffix(accelRT), now, 0)
		return
	}
	fill := price
	if e.cfg.SlippageBps > 0 {
		fill = price * (1 + e.cfg.SlippageBps/10000.0) // 与 addToPositionLocked 内部同口径
	}
	e.finishTRoundLocked(r, tRStateDone, need, fill, (r.LegPrice-fill)*float64(need),
		fmt.Sprintf("反T配对完成：卖%.2f→买%.2f 价差%.2f%%%s", r.LegPrice, fill, fav,
			tern(force, "（尾盘有信号回补）", "")), now, need)
	e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
		StrategyType: pos.StrategyType, Side: "buy", Kind: "反T买回", Price: fill, Qty: need,
		Status: "filled", Reason: tRStrategies + " " + r.ID, CreatedAt: now})
	// B5：反 T 买回腿同样是被跳过的 T 腿。
	e.tShadowSkipTLegLocked(1, now)
	if r.UsedSignalID != "" && p.ConsumeSignal != nil {
		p.ConsumeSignal(r.UsedSignalID)
	}
}

func tern(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// finishTRoundLocked 收尾一条回转：置终态、记第二腿与价差、算「做 T 收益」、释放冻结款并落盘。
// 只有 done 轮有 `t_pnl_*`：converted/unwound 没有已实现 T 价差（§8.9a），
// 计进去就是把日内手法虚增成收益。
func (e *Engine) finishTRoundLocked(r *TRound, state string, pairQty int, pairPrice, spreadPnl float64, reason string, now time.Time, matched int) {
	r.State = state
	r.PairQty = pairQty
	r.PairPrice = pairPrice
	r.PnL = spreadPnl
	r.ClosedAt = now
	r.Reason = reason
	r.QMatched = matched
	r.FrozenCash = 0 // 终态一律释放冻结（买回已完成或明确不再买回），不留"永远占着回购款"的死账
	if state == tRStateDone && matched > 0 && r.LegPrice > 0 && pairPrice > 0 {
		gross := (pairPrice - r.LegPrice) * float64(matched)
		if r.Leg == tRLegSell {
			gross = (r.LegPrice - pairPrice) * float64(matched)
		}
		r.TPnlGross = round2(gross)
		// B1：费用在终态一次性按**两腿口径**重算并留库（开腿时只落了首腿那半截）。
		// 分项之和必须等于毛−净（判据 1）；未成交的机会成本**绝不**减进来——那是不可观测的反事实，
		// 只在 B2 的离线标定里估（方案 §2.2）。
		feeBuy, feeSell, stamp := e.tRoundFeeSplitLocked(r, pairPrice, matched)
		r.FeeBuy, r.FeeSell, r.StampTax = round2(feeBuy), round2(feeSell), round2(stamp)
		r.TPnlNet = round2(gross - (r.FeeBuy + r.FeeSell + r.StampTax))
	}
	log.Printf("[paper] §T-MODE 回转 %s %s(%s) 第%d轮 终态=%s 腿=%s %d股@%.2f 配对%d股@%.2f 毛%.2f 净%.2f 原因=%s",
		r.ID, r.Mode, r.Name, r.Seq, state, r.Leg, r.LegQty, r.LegPrice, pairQty, pairPrice,
		r.TPnlGross, r.TPnlNet, reason)
	e.trimTRoundsLocked()
	e.persist()
}

// tRoundFeeLocked 一条回转两腿的费用合计——**复用**账本既有的费率口径，
// 不新造费率常量：否则"做 T 收益"里的费用和现金里真实扣掉的钱会变成两套口径。
// B1 起它只是分项口径的合计视图；落库的是 FeeBuy/FeeSell/StampTax 三个分项。
func (e *Engine) tRoundFeeLocked(r *TRound, pairPrice float64, matched int) float64 {
	feeBuy, feeSell, stamp := e.tRoundFeeSplitLocked(r, pairPrice, matched)
	return feeBuy + feeSell + stamp
}

// tRoundFeeSplitLocked 一条回转两腿的费用分项（佣金买/佣金卖/印花税）。
// 口径与 buyFeeLocked/sellFeeLocked 逐字同源：卖出那条腿的"佣金"是从 sellFeeLocked 里
// 减去印花税得到的，而不是另写一遍 max(额×费率, 最低佣金)——两处各写一份费率是最常见的漂移来源。
func (e *Engine) tRoundFeeSplitLocked(r *TRound, pairPrice float64, matched int) (feeBuy, feeSell, stamp float64) {
	if r == nil || matched <= 0 || r.LegPrice <= 0 || pairPrice <= 0 {
		return 0, 0, 0
	}
	buyAmt, sellAmt := 0.0, 0.0
	if r.Leg == tRLegBuy {
		buyAmt, sellAmt = r.LegPrice*float64(matched), pairPrice*float64(matched)
	} else {
		sellAmt, buyAmt = r.LegPrice*float64(matched), pairPrice*float64(matched)
	}
	feeBuy = e.buyFeeLocked(buyAmt)
	stamp = e.stampTaxLocked(sellAmt)
	feeSell = e.sellFeeLocked(sellAmt) - stamp
	if feeSell < 0 {
		feeSell = 0 // 费率配置异常时的防守：分项不得为负（宁可少记费用，也不制造负费用）
	}
	return feeBuy, feeSell, stamp
}

// applyOpenLegCostLocked 开腿时落 B1 的首腿成本读数（须持锁调用）。
// 为什么开腿就写而不是等配平：决策价是**那一瞬间**的报价，过了这一刻就再也取不回来了；
// 费用分项在终态会按两腿重算，但滑点只能锚在开腿这一步。
func (e *Engine) applyOpenLegCostLocked(r *TRound, decision, fill float64, qty int, dayAmountYuan float64) {
	if r == nil || decision <= 0 || qty <= 0 {
		return
	}
	r.DecisionPrice = round4(decision)
	r.SlipBps = round2(tSlipBpsOf(r.Leg, decision, fill))
	r.FeeSource = tRFeeSourceEstimate // 纸面永远估算：模拟撮合没有券商回报
	amt := fill * float64(qty)
	if r.Leg == tRLegBuy {
		r.FeeBuy = round2(e.buyFeeLocked(amt))
	} else {
		r.StampTax = round2(e.stampTaxLocked(amt))
		r.FeeSell = round2(e.sellFeeLocked(amt) - e.stampTaxLocked(amt))
	}
	// 分母＝**开腿当时已累计**的当日成交额（q.Amount），不是全天量：后者是未来数据，
	// 拿它算参与率会把每一腿都系统性算小（方案 §2.1 的分母口径锁）。
	if dayAmountYuan > 0 {
		r.ParticipationPct = round4(amt / dayAmountYuan * 100)
	}
}

// tRFeeSourceEstimate/Broker/Mixed 费用来源三态（与实盘同名，前端与日汇总按字面量读）。
const (
	tRFeeSourceBroker   = "broker"
	tRFeeSourceEstimate = "estimate"
	tRFeeSourceMixed    = "mixed"
)

// tSlipBpsOf 单腿滑点（bp，正数＝对我不利）。
// 买腿：成交高于决策价是不利 ⇒ (fill−decision)/decision；卖腿：成交低于决策价是不利 ⇒ (decision−fill)/decision。
// 这个符号约定是 B1 判据 2 的靶子：写成不带方向的一刀切，反 T 会整片"负滑点"。
func tSlipBpsOf(leg string, decision, fill float64) float64 {
	if decision <= 0 {
		return 0
	}
	if leg == tRLegSell {
		return (decision - fill) / decision * 1e4
	}
	return (fill - decision) / decision * 1e4
}

// stampTaxLocked 卖出印花税（仅卖出，无最低）；费率 ≤0 视为不收（与 sellFeeLocked 同源）。
func (e *Engine) stampTaxLocked(proceeds float64) float64 {
	if e.cfg.StampTaxRate <= 0 || proceeds <= 0 {
		return 0
	}
	return proceeds * e.cfg.StampTaxRate
}

// trimTRoundsLocked 只保留最近 tRKeepRounds 条（账本体积，不影响判定）。
func (e *Engine) trimTRoundsLocked() {
	if len(e.tRounds) <= tRKeepRounds {
		return
	}
	e.tRounds = append([]TRound(nil), e.tRounds[len(e.tRounds)-tRKeepRounds:]...)
}

// ── step0~step7：能不能开腿的判定链（与执行分离，便于逐条驱动与单测）────────────────────

// openTRoundsLocked 遍历持仓，先问"能不能开"（tOpenBlockReason），可以才真正开腿。
func (e *Engine) openTRoundsLocked(quotes map[string]*data.StockInfo, p TParams, day string, now time.Time) {
	if p.DayBlocked {
		e.tNoteBlockLocked("step0", "熔断日两向都不开新腿", "")
		return
	}
	codes := make([]string, 0, len(e.positions))
	for code := range e.positions {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	// 总量帽分母＝min(总资产, 持仓总市值)（六轮 T6-2）。持仓市值用本层同一批估值价，
	// 不另取快照：两个读数来源不同会让帽的松紧随读取时点漂移。
	totalValue, holdValue := e.tBookValueLocked(now)
	capBase := math.Min(totalValue, holdValue)
	for _, code := range codes {
		if why := e.tOpenBlockReason(code, quotes, p, day, now, capBase); why != "" {
			step, reason := splitStep(why)
			e.tNoteBlockLocked(step, reason, code)
			continue
		}
		e.openLegForCodeLocked(code, quotes, p, day, now)
	}
}

// splitStep 把"step4 xxx"拆成 (step4, xxx)，供留痕按步骤归一。
func splitStep(s string) (step, rest string) {
	if i := strings.Index(s, " "); i > 0 {
		return s[:i], s[i+1:]
	}
	return "step?", s
}

// tOpenBlockReason 返回空串＝允许开腿；非空是"stepN 原因"。
func (e *Engine) tOpenBlockReason(code string, quotes map[string]*data.StockInfo, p TParams, day string,
	now time.Time, capBase float64) string {
	pos := e.positions[code]
	if pos == nil || pos.Qty <= 0 {
		return "step0 无持仓"
	}
	if e.openTRoundFor(code) != nil {
		return "step1 该票已有在途腿（配对先于新开，绝不叠腿）"
	}
	if why := e.TAnchorBlockReason(pos); why != "" {
		return "step0 " + why
	}
	q := quotes[code]
	if q == nil || q.Price <= 0 {
		return "step0 无实时价（fail-closed）"
	}
	v, ok := data.VWAPOf(q)
	if !ok {
		return fmt.Sprintf("step0 分时均线不可信(Volume=%.0f Amount=%.0f)，不做 T 也不回落成本口径", q.Volume, q.Amount)
	}
	buyDir, sellDir := tRoundDirections(q, v, p)
	if buyDir && p.MinMarketScale > 0 && p.MarketScale < p.MinMarketScale {
		return fmt.Sprintf("step0 正T环境门 MS=%.3f<%.2f", p.MarketScale, p.MinMarketScale)
	}
	if sellDir && p.MarketScale < p.SellEnvMinMarketScale {
		return fmt.Sprintf("step0 反T环境门 MS=%.3f<%.2f（极端弱势不做T）", p.MarketScale, p.SellEnvMinMarketScale)
	}
	if buyDir && p.MarketPlungePct < 0 && p.IndexChangePct <= p.MarketPlungePct {
		return fmt.Sprintf("step0 大盘急跌 %.2f%% ≤ %.2f%%，停正T", p.IndexChangePct, p.MarketPlungePct)
	}
	// §W-09 形态门：与实盘 regimeBlock 同口径、同位置（环境类判定之后、流动性之前）。
	// 只判**入口**（不开新腿）；出口（在途腿加速配平）在 accelReasonLocked。
	if why := e.tRegimeBlockReason(p, now); why != "" {
		return "step0 " + why
	}
	if why := e.tLiquidityBlock(code, q, p); why != "" {
		return "step0.5 " + why
	}
	// §W-09 量能门：位置与实盘 openPlan 的 step0.6 对齐（流动性前置之后、限次之前）。
	// 文案与 step 标签逐字一致——两边都是"这只票今天适不适合做 T"，不一致就会在复盘时
	// 出现同一个原因两种说法（§127 的字面量一致纪律）。
	if why := e.tVolumeBlock(code, p); why != "" {
		return "step0.6 " + why
	}
	if p.MaxRounds > 0 && e.tRoundsUsedLocked(code, day) >= p.MaxRounds {
		return fmt.Sprintf("step2 单码当日回转次数已满(%d/%d)", e.tRoundsUsedLocked(code, day), p.MaxRounds)
	}
	if tLastOpenPassed(now, p.LastOpenHM) {
		return fmt.Sprintf("step3 已过最后开腿时刻 %s", p.LastOpenHM)
	}
	if tInCooling(now, p.OpenCoolingMin) {
		return "step3 开盘冷静期内（分时均线样本不足，V 与现价几乎重合）"
	}
	if why := tIntradayBlock(q, v, p); why != "" {
		return "step4 " + why
	}
	if _, _, why := e.tVminCheck(code, v, p); why != "" {
		return "step4 " + why
	}
	if why := e.tSignalBlock(code, q, v, p, now); why != "" {
		return "step5 " + why
	}
	if p.StrategicBusy != nil && p.StrategicBusy(code) {
		return "step6 该股有未执行战略信号/在途单，T 让路（§8.7）"
	}
	if why := e.tQuotaBlock(code, pos, q, v, p, day, capBase, buyDir, sellDir); why != "" {
		return "step7 " + why
	}
	return ""
}

// tRoundDirections 现价在分时均线哪一侧，决定本轮可能开的是正 T 还是反 T。
// 两侧都不满足偏离门时两个 false —— 这就是"今天这只票没有回转机会"，不是缺陷。
func tRoundDirections(q *data.StockInfo, v float64, p TParams) (buy, sell bool) {
	dev := (q.Price - v) / v * 100
	if p.BuyFirst && dev <= -p.VWAPDevPct {
		buy = true
	}
	if p.SellFirst && dev >= p.VWAPDevPct {
		sell = true
	}
	return buy, sell
}

// tLiquidityBlock 流动性前置（§8.4.4b）：任一读数不可得**按不过处理**。
func (e *Engine) tLiquidityBlock(code string, q *data.StockInfo, p TParams) string {
	if p.MinAmountWan > 0 {
		if q.Amount <= 0 {
			return "当日成交额读数不可得，按不过处理"
		}
		if w := q.Amount / 10000.0; w < p.MinAmountWan {
			return fmt.Sprintf("当日成交额 %.0f 万 < %.0f 万", w, p.MinAmountWan)
		}
	}
	if p.MinTurnoverPct > 0 {
		if q.Turnover <= 0 {
			return "换手率读数不可得，按不过处理"
		}
		if q.Turnover < p.MinTurnoverPct {
			return fmt.Sprintf("换手率 %.2f%% < %.2f%%", q.Turnover, p.MinTurnoverPct)
		}
	}
	if strings.Contains(strings.ToUpper(q.Name), "ST") {
		return "ST/退市整理股不做日内回转（±5% 限制 + 停牌风险，止损通道随时关门）"
	}
	if p.MinListedDays > 0 {
		if p.StockMeta == nil {
			return "上市天数无源（未接 StockMeta 回调），按不过处理"
		}
		days, isST, ok := p.StockMeta(code)
		if isST {
			return "ST 股不做日内回转"
		}
		if !ok || days <= 0 {
			return "上市天数读数不可得，按不过处理"
		}
		if days < int(p.MinListedDays) {
			return fmt.Sprintf("上市 %d 天 < %.0f 天（次新股日内振幅常超 10%%，振幅门形同不设）", days, p.MinListedDays)
		}
	}
	return ""
}

// tIntradayBlock 日内位置：振幅门 → 偏离门 → 近极值带 → 涨跌停距离。
func tIntradayBlock(q *data.StockInfo, v float64, p TParams) string {
	base := q.PrevClose
	if base <= 0 {
		base = q.Close
	}
	if base <= 0 || q.High <= 0 || q.Low <= 0 || q.High < q.Low {
		return "日内 High/Low/PrevClose 读数不完整"
	}
	if amp := (q.High - q.Low) / base * 100; amp < p.MinAmplitudePct {
		return fmt.Sprintf("当日振幅 %.2f%% < 门槛 %.2f%%（振幅不够就走不完成本+价差，开了也配不上对）", amp, p.MinAmplitudePct)
	}
	dev := (q.Price - v) / v * 100
	if math.Abs(dev) < p.VWAPDevPct {
		return fmt.Sprintf("现价相对分时均线只偏 %.2f%%，未达偏离门 %.2f%%", dev, p.VWAPDevPct)
	}
	if dev < 0 && q.Price < q.Low*(1+p.NearExtremePct/100) {
		return "正T位置门：现价已砸到日内低点附近，不接正在砸向低点的刀"
	}
	if dev > 0 && q.Price < q.High*(1-p.NearExtremePct/100) {
		return "反T位置门：涨过又回落过半，不在近高点区，不做半山腰卖"
	}
	if p.LimitDistancePct > 0 {
		up, down := tLimitPrices(q.Code, q.Name, base)
		if up > 0 && q.Price >= up*(1-p.LimitDistancePct/100) {
			return "逼近涨停：反T卖腿可能封板、正T也买不进，本票本轮不做"
		}
		if down > 0 && q.Price <= down*(1+p.LimitDistancePct/100) {
			return "逼近跌停：正T买腿买得到也卖不掉，本票本轮不做"
		}
	}
	return ""
}

// tVminCheck 分钟链一致性校验（§8.4.1 校验腿）：差异过大说明两个读数至少有一个不可信，不动资金；
// 分钟链本身取不到只记降级、不拦——它不该成为日内操作的新单点依赖。
func (e *Engine) tVminCheck(code string, v float64, p TParams) (vmin, dev float64, block string) {
	if p.MinuteBasis == nil || v <= 0 {
		return 0, 0, ""
	}
	vm, ok := p.MinuteBasis(code)
	if !ok || vm <= 0 {
		return 0, 0, ""
	}
	dev = math.Abs(vm-v) / v * 100
	if dev > p.VWAPCheckTolPct {
		return vm, dev, fmt.Sprintf("分时均线校验不一致：主源%.3f vs 分钟链%.3f 偏差%.2f%%>%.2f%%",
			v, vm, dev, p.VWAPCheckTolPct)
	}
	return vm, dev, ""
}

// tSignalBlock 信号侧（§8.4.2）。两边语义相反且刻意不对称：
// 正 T 缺信号＝不作为（安全）；反 T 缺信号＝照做（减敞口），它靠否决门让位给止盈止损。
func (e *Engine) tSignalBlock(code string, q *data.StockInfo, v float64, p TParams, now time.Time) string {
	dev := (q.Price - v) / v * 100
	if dev < 0 {
		if !p.BuyFirst {
			return "正T方向未开"
		}
		if p.NewBuySignal == nil {
			return "正T需要活跃买入信号，但未接 NewBuySignal 回调（不作为）"
		}
		id, ok := p.NewBuySignal(code, e.lastRoundEndedAt(code, now))
		if !ok {
			return "无候选 B 口径的新活跃买入信号（老信号不许反复低吸）"
		}
		if e.tSignalConsumedLocked(id) {
			return "该信号已被本账 T 腿消费（§8.4.3 一次性消费）"
		}
		if p.SignalConsumedByStrategic != nil && p.SignalConsumedByStrategic(id) {
			return "该信号已被战略买单消费，同一判断不买第二次"
		}
		return ""
	}
	if !p.SellFirst {
		return "反T方向未开"
	}
	if p.ActiveSellVerdict != nil && p.ActiveSellVerdict(code) {
		return "反T否决：已有活跃卖出裁决，交给信号执行（§8.7）"
	}
	if p.StopLineDistance == nil {
		return "反T否决门未接线（无 StopLineDistance 回调），宁可不 T"
	}
	d, ok := p.StopLineDistance(code, q.Price)
	if !ok {
		return "反T否决：止盈/止损线距离读数不可得，按否决处理（fail-closed）"
	}
	if d < p.StopGuardPct {
		return fmt.Sprintf("反T否决：距止盈/止损线 %.2f%% < %.2f%%（止盈止损优先于做T）", d, p.StopGuardPct)
	}
	return ""
}

// tQuotaBlock 额度门：底仓/机动仓、单票 50% 帽、反 T 当日总量帽余量、T 可用现金。
func (e *Engine) tQuotaBlock(code string, pos *Position, q *data.StockInfo, v float64, p TParams,
	day string, capBase float64, buy, sell bool) string {
	layers := LayersOfPosition(pos, e.clockNow())
	if layers.BaseQty <= 0 {
		return "无底仓（全仓当日买入）：T+1 下没有任何可卖量，做不了 T"
	}
	if layers.IntradayQty > 0 {
		return "已有未平机动仓：先把旧的配平，不叠新的"
	}
	capQty := T0Cap(pos.Qty, p.CapPct/100.0)
	if lotFloor(capQty) < 100 {
		return fmt.Sprintf("单票 T 帽（%.0f%%）不足 1 手", p.CapPct)
	}
	if sell && p.IntradaySellCapTotalPct > 0 {
		if capBase <= 0 {
			return "反T总量帽分母不可得，按不过处理"
		}
		remain := capBase*p.IntradaySellCapTotalPct/100.0 - e.sellFirstNotionalLocked(day)
		if remain <= 0 {
			return fmt.Sprintf("当日反T卖腿市值已达总量帽 %.0f（分母 min(总资产,持仓市值)），不再开卖腿",
				capBase*p.IntradaySellCapTotalPct/100.0)
		}
		if remain < q.Price*100 {
			return fmt.Sprintf("总量帽余量 %.0f 不足 1 手", remain)
		}
	}
	if buy {
		avail := e.cash - e.tFrozenCashLocked()
		if avail < q.Price*100 {
			return fmt.Sprintf("T 可用现金 %.0f 不足 1 手（反T冻结款是专款，不参与竞争）", avail)
		}
	}
	return ""
}

// ── step8/step9：真正开腿并落账 ────────────────────────────────────────────────────────

// openLegForCodeLocked 开腿。走到这里说明 0~7 步全过；顺序一旦被打乱，这里就可能开出半态腿。
func (e *Engine) openLegForCodeLocked(code string, quotes map[string]*data.StockInfo, p TParams, day string, now time.Time) {
	pos := e.positions[code]
	q := quotes[code]
	price := q.Price
	v, _ := data.VWAPOf(q)
	vmin, checkDev, block := e.tVminCheck(code, v, p)
	if block != "" {
		e.tNoteBlockLocked("step4", block, code)
		return
	}
	layers := LayersOfPosition(pos, now)
	capQty := T0Cap(pos.Qty, p.CapPct/100.0)
	base := q.PrevClose
	if base <= 0 {
		base = q.Close
	}
	amp := 0.0
	if base > 0 {
		amp = (q.High - q.Low) / base * 100
	}
	rd := tReadings{v: v, vmin: vmin, checkDev: checkDev, dev: (price - v) / v * 100,
		high: q.High, low: q.Low, amp: amp, amountWan: q.Amount / 10000.0, amountYuan: q.Amount,
		turnover: q.Turnover, ms: p.MarketScale}
	if rd.dev < 0 && p.BuyFirst {
		sigID := ""
		if p.NewBuySignal != nil {
			if id, ok := p.NewBuySignal(code, e.lastRoundEndedAt(code, now)); ok {
				sigID = id
			}
		}
		if qty := lotFloor(capQty); qty >= 100 {
			e.openBuyFirstLocked(pos, layers.BaseQty, qty, price, day, now, rd, sigID, p)
		}
		return
	}
	if rd.dev > 0 && p.SellFirst {
		maxSell := layers.BaseQty - tRMinBaseKeep
		if capQty < maxSell {
			maxSell = capQty
		}
		if p.IntradaySellCapTotalPct > 0 {
			totalValue, holdValue := e.tBookValueLocked(now)
			if capBase := math.Min(totalValue, holdValue); capBase > 0 {
				remain := capBase*p.IntradaySellCapTotalPct/100.0 - e.sellFirstNotionalLocked(day)
				if byCap := int(remain / price); byCap < maxSell {
					maxSell = byCap
				}
			}
		}
		if qty := lotFloor(maxSell); qty >= 100 {
			e.openSellFirstLocked(pos, layers.BaseQty, qty, price, day, now, rd, p)
		}
	}
}

// tReadings 开腿时刻的日内读数集合（一次性打包，避免 newTRoundLocked 长出十几个参数）。
type tReadings struct {
	v, vmin, checkDev, dev  float64
	high, low, amp          float64
	amountWan, turnover, ms float64
	// amountYuan 开腿时刻**已累计**的当日成交额（元），只服务 B1 的 participation_pct 分母。
	// 与 amountWan 是同一个 q.Amount 的两种单位，刻意并存：一个是展示读数（万元），
	// 一个是参与率分母（元），换算散落在调用点会让分母口径漂移。
	amountYuan float64
}

// openBuyFirstLocked 正 T 第一腿：买入机动仓。
// 刻意走 `addToPositionLocked`（与手动加仓同一条路径）：费用、滑点、池扣款、层账批次全部一致，
// 绕开它会让"做 T 的仓位"成为更便宜的资金来源；代价是它会摊薄整仓成本，
// 所以缺陷修在**裁决内核读锚点**（AnchorHard + 锚点前置），而不是给 T 腿开旁路。
func (e *Engine) openBuyFirstLocked(pos *Position, baseQty, qty int, price float64, day string, now time.Time,
	rd tReadings, sigID string, p TParams) bool {
	if err := e.addToPositionLocked(pos, pos.Code, pos.Name, pos.Strategy, price, price, qty,
		tRStrategies+" 正T买入腿"); err != nil {
		log.Printf("[paper] §T-MODE 正T买入腿未成交 %s(%s) %d股: %v", pos.Code, pos.Name, qty, err)
		return false
	}
	fill := price
	if e.cfg.SlippageBps > 0 {
		fill = price * (1 + e.cfg.SlippageBps/10000.0)
	}
	r := e.newTRoundLocked(pos, TRModeBuyFirst, tRLegBuy, qty, fill, baseQty, day, now, rd, sigID,
		fmt.Sprintf("低吸触发：现价在分时均线下方 %.2f%%", rd.dev))
	r.AnchorPrice = pos.AnchorPrice
	// B1：开腿即落决策价/滑点/首腿费用与参与率（分母＝当时累计成交额，见字段注释）。
	e.applyOpenLegCostLocked(r, price, fill, qty, rd.amountYuan)
	e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
		StrategyType: pos.StrategyType, Side: "buy", Kind: "正T买入", Price: fill, Qty: qty,
		Status: "filled", Reason: tRStrategies + " " + r.ID, CreatedAt: now})
	// B5：正 T 首腿成交 ⇒ 影子账跳过的 T 腿 +1（影子账不执行任何 T 腿）。
	e.tShadowSkipTLegLocked(1, now)
	if sigID != "" && p.ConsumeSignal != nil {
		p.ConsumeSignal(sigID)
	}
	return true
}

// openSellFirstLocked 反 T 第一腿：卖出底仓一部分（必然留 ≥1 手 + ≥50% 原持仓），
// 并冻结回购资金：专款专用，战略买入不得动用（§8.3 纪律 3）。
func (e *Engine) openSellFirstLocked(pos *Position, baseQty, qty int, price float64, day string, now time.Time,
	rd tReadings, p TParams) {
	sold, err := e.sellQtyLocked(pos, price, qty, tRStrategies+" 反T卖出腿")
	if err != nil {
		log.Printf("[paper] §T-MODE 反T卖出腿被拒 %s(%s) %d股: %v", pos.Code, pos.Name, qty, err)
		return
	}
	fill := e.applySellPriceLocked(price)
	r := e.newTRoundLocked(pos, TRModeSellFirst, tRLegSell, sold, fill, baseQty, day, now, rd, "",
		fmt.Sprintf("高抛触发：现价在分时均线上方 %.2f%%", rd.dev))
	r.AnchorPrice = pos.AnchorPrice
	// B1：反 T 的首腿是卖出，滑点取"决策价高于成交价"为正（正数＝对我不利）。
	e.applyOpenLegCostLocked(r, price, fill, sold, rd.amountYuan)
	// 冻结款＝max(卖腿回款, 现价×买回量) + 费用缓冲：买回价高于卖价时也要有钱等着，
	// 否则"尾盘有信号却不回补"会变成现金不足造成的半态，而不是决策。
	proceeds := fill * float64(sold)
	needCash := price * float64(sold)
	buf := p.FeeBufferPct
	if buf <= 0 {
		buf = tRFrozenFeeBufferPct // 默认 0.6%：与实盘预算的费用缓冲同值
	}
	frozen := math.Max(proceeds, needCash) * (1 + buf/100.0)
	r.FrozenCash = round2(frozen)
	e.trimTRoundsLocked()
	e.persist()
	e.recordOrderLocked(Order{Code: pos.Code, Name: pos.Name, Strategy: pos.Strategy,
		StrategyType: pos.StrategyType, Side: "sell", Kind: "反T卖出", Price: fill, Qty: sold,
		Status: "filled", Reason: tRStrategies + " " + r.ID, CreatedAt: now})
	// B5：反 T 首腿成交 ⇒ 影子账跳过的 T 腿 +1。
	e.tShadowSkipTLegLocked(1, now)
}

// newTRoundLocked 登记一条 open 态回转并落盘（须持锁调用）。
// seq＝该代码当日已开次数 + 1，因此幂等键在单码单日内严格递增且不与既有键重复。
func (e *Engine) newTRoundLocked(pos *Position, mode, leg string, qty int, price float64, baseQty int,
	day string, now time.Time, rd tReadings, sigID, reason string) *TRound {
	seq := e.tRoundsUsedLocked(pos.Code, day) + 1
	r := TRound{
		ID:       fmt.Sprintf("t0:%s:%s:%s:%d", pos.Code, mode, day, seq),
		Code:     pos.Code,
		Name:     pos.Name,
		Mode:     mode,
		Seq:      seq,
		Leg:      leg,
		LegQty:   qty,
		LegPrice: price,
		LegAt:    now,
		BaseQty:  baseQty,
		State:    tRStateOpen,
		Day:      day,
		Reason:   reason,
		VWAP:     round3(rd.v), Vmin: round3(rd.vmin), CheckDevPct: round2(rd.checkDev),
		DevPct: round2(rd.dev), High: rd.high, Low: rd.low, AmplitudePct: round2(rd.amp),
		AmountWan: round2(rd.amountWan), TurnoverPct: round2(rd.turnover),
		UsedSignalID: sigID, MarketScaleAt: rd.ms,
	}
	e.tRounds = append(e.tRounds, r)
	log.Printf("[paper] §T-MODE 开腿 %s %s(%s) %s %d股@%.2f 底仓%d股 V=%.3f Vmin=%.3f 偏差%.2f%% 振幅%.2f%% 成交额%.0f万 换手%.2f%% MS=%.3f 信号=%s 原因=%s",
		r.ID, mode, pos.Name, leg, qty, price, baseQty, rd.v, rd.vmin, rd.dev, rd.amp,
		rd.amountWan, rd.turnover, rd.ms, orDash(sigID), reason)
	e.trimTRoundsLocked()
	e.persist()
	return &e.tRounds[len(e.tRounds)-1]
}

// ── 账本口径与只读接口 ──────────────────────────────────────────────────────────────────

// tBookValueLocked 返回（总资产＝现金+持仓市值, 持仓总市值）。总量帽分母要两者的较小值。
func (e *Engine) tBookValueLocked(now time.Time) (total, holdValue float64) {
	for _, p := range e.positions {
		if p == nil || p.Qty <= 0 {
			continue
		}
		v := p.Mark
		if v <= 0 {
			v = p.CostPrice
		}
		holdValue += v * float64(p.Qty)
	}
	return e.cash + holdValue, holdValue
}

// tOpenBuyValueLocked 在途正 T 买腿市值（须持锁调用）：目标仓位模型要把它从战略当前仓位里剔出去。
func (e *Engine) tOpenBuyValueLocked() float64 {
	sum := 0.0
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State == tRStateOpen && r.Leg == tRLegBuy {
			sum += r.LegPrice * float64(r.LegQty)
		}
	}
	return sum
}

// tOpenSellValueLocked 在途反 T 卖腿市值（须持锁调用）：那部分底仓还欠着买回，战略仓位要加回去，
// 否则"高抛之后"会被读成目标已经达成、又腾出一块空间去开新仓。
func (e *Engine) tOpenSellValueLocked() float64 {
	sum := 0.0
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State == tRStateOpen && r.Leg == tRLegSell {
			sum += r.LegPrice * float64(r.LegQty)
		}
	}
	return sum
}

// tFrozenCashLocked 在途反 T 腿的冻结回购款合计（须持锁调用）。
func (e *Engine) tFrozenCashLocked() float64 {
	sum := 0.0
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State == tRStateOpen && r.Leg == tRLegSell {
			sum += r.FrozenCash
		}
	}
	return sum
}

// TFrozenCash 对外只读入口（战略预算与面板共用同一本冻结账）。
func (e *Engine) TFrozenCash() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tFrozenCashLocked()
}

// tQuotePrice 取实时快照价（缺失/非法返回 0，调用方按"无有效价"跳过）。
func tQuotePrice(quotes map[string]*data.StockInfo, code string) float64 {
	if q, ok := quotes[code]; ok && q != nil && q.Price > 0 {
		return q.Price
	}
	return 0
}

// lotFloor 向下取整到整手；不足 1 手返回 0（两腿都必须整手，零股只能清仓卖出）。
func lotFloor(qty int) int {
	if qty < 100 {
		return 0
	}
	return qty / 100 * 100
}

// tLimitPrices 按昨收算涨/跌停价（分板块幅度复用 LimitUpPct，不另立一套）。
func tLimitPrices(code, name string, prevClose float64) (up, down float64) {
	if prevClose <= 0 {
		return 0, 0
	}
	pct := LimitUpPct(code, name)
	return round2(prevClose * (1 + pct/100)), round2(prevClose * (1 - pct/100))
}

// round3 三位小数：分时均线类读数要能看出分厘差，账本既有的 round2（两位）不复用，避免把 V 抹成现价。
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// round4 四位小数：参与率读数常在 0.01% 量级，用 round2 会把 B4 的观察量整片抹成 0。
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// TRounds 全部回转记录的只读快照（返回副本：调用方在锁外遍历时不能共享正在 append 的底层数组）。
func (e *Engine) TRounds() []TRound {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]TRound, len(e.tRounds))
	copy(out, e.tRounds)
	return out
}

// TRoundPnL 已完成回转变的**毛**价差合计（旧字段口径，未扣费），保留给既有面板与归因。
func (e *Engine) TRoundPnL() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	sum := 0.0
	for i := range e.tRounds {
		if e.tRounds[i].State == tRStateDone {
			sum += e.tRounds[i].PnL
		}
	}
	return sum
}

// TRoundNetPnL 「做 T 收益」独立口径（六轮 T6-5 / §8.9a）：
// 只统计 done 轮的**已扣两腿费用**净额；converted/unwound 与在途 open 一律不计
// （它们的仓位变化属战略侧，计进来就是把日内手法虚增成收益）。
// 整仓成本与卖出裁决基准都不消费这个数——T 腿盈亏永不并入 EntryPrice 是 §0 第 10 条的锁。
// English: the standalone T P&L — net of both legs' fees, done-rounds only, never folded into cost basis.
func (e *Engine) TRoundNetPnL() (net, gross float64, done int, feeEstShare float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	est := 0
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.State != tRStateDone {
			continue
		}
		done++
		net += r.TPnlNet
		gross += r.TPnlGross
		if r.FeeEst {
			est++
		}
	}
	if done > 0 {
		feeEstShare = float64(est) / float64(done)
	}
	return round2(net), round2(gross), done, feeEstShare
}

// TCostDaily 当日做 T 的成本分解（B1 §2.3；扩展现有 day ledger，不新增接口）。
// 字段口径：
//   - Gross/Net 只统计 done 轮（与 TPnlGross/TPnlNet 同源），SlipCost 覆盖所有有首腿成交的轮；
//   - SlipCost = Σ |fill − decision| × 成交股数（元，绝对值和，不区分买卖方向）；
//   - Fills 是**成交腿数**（done 轮＝2，只成了首腿的轮＝1），不是轮数；
//   - FeeEstimateShare = 费用来源非 broker 且有成交的轮数 / 有成交的轮数（对账稳定性的核心读数）；
//   - ParticipationPct* 只统计参与率可得的轮（分母缺失的轮不补 0，避免把"没有读数"混进中位数）。
type TCostDaily struct {
	Gross               float64 `json:"gross"`
	FeeBuy              float64 `json:"fee_buy"`
	FeeSell             float64 `json:"fee_sell"`
	StampTax            float64 `json:"stamp_tax"`
	SlipCost            float64 `json:"slip_cost"`
	Net                 float64 `json:"net"`
	Fills               int     `json:"fills"`
	Unfilled            int     `json:"unfilled"`
	FeeEstimateShare    float64 `json:"fee_estimate_share"`
	ParticipationPctP50 float64 `json:"participation_pct_p50"`
	ParticipationPctMax float64 `json:"participation_pct_max"`
}

// TRoundDayStats 当日回转汇总（开/配对/收摊/转换/熔断 + 做 T 收益 + 被拦分布）。
type TRoundDayStats struct {
	Day          string         `json:"day"`
	Opened       int            `json:"opened"`
	Done         int            `json:"done"`
	Unwound      int            `json:"unwound"`
	Converted    int            `json:"converted"`
	LossBreaker  int            `json:"loss_breaker"`
	NetPnl       float64        `json:"t_pnl_net"`
	GrossPnl     float64        `json:"t_pnl_gross"`
	OpenLegs     int            `json:"open_legs"`
	FrozenCash   float64        `json:"frozen_cash"`
	SellNotional float64        `json:"sell_notional"`
	ConvertedQty int            `json:"converted_qty"` // 未回补而转战略减仓的股数（底仓磨薄的直接读数）
	BlockByStep  map[string]int `json:"block_by_step"`
	// Cost B1 成本分解（毛/佣金/印花/滑点/净 + 估算占比 + 参与率分位）。
	Cost TCostDaily `json:"cost"`
}

// TRoundDay 返回某日（空串＝今天）的回转汇总，供接口与每日总账使用。
func (e *Engine) TRoundDay(day string) TRoundDayStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	if day == "" {
		day = cntime.DayCompactOf(e.clockNow())
	}
	st := TRoundDayStats{Day: day, BlockByStep: map[string]int{}}
	for k, v := range e.tBlock {
		st.BlockByStep[k] = v
	}
	// B1 日汇总的成本分项：与上面的结局分布同一次遍历取数，避免"收益按一个过滤、成本按另一个"。
	filledRounds, estimateRounds := 0, 0
	participation := make([]float64, 0, 4)
	for i := range e.tRounds {
		r := &e.tRounds[i]
		if r.Day != day {
			continue
		}
		st.Opened++
		anyFill := r.LegQty > 0 || r.PairQty > 0
		if r.LegQty > 0 {
			st.Cost.Fills++
		}
		if r.PairQty > 0 {
			st.Cost.Fills++
		}
		if anyFill {
			filledRounds++
			if r.FeeSource != "" && r.FeeSource != tRFeeSourceBroker {
				estimateRounds++ // estimate 或 mixed：只要不是纯券商值，就算"这轮的数不全是券商给的"
			}
		}
		if r.Unfilled {
			st.Cost.Unfilled++
		}
		if r.DecisionPrice > 0 && r.LegQty > 0 {
			// 滑点成本只算首腿（decision_price 只锚首腿，见字段注释）：绝对值和，不区分方向。
			st.Cost.SlipCost += math.Abs(r.LegPrice-r.DecisionPrice) * float64(r.LegQty)
		}
		if r.ParticipationPct > 0 {
			participation = append(participation, r.ParticipationPct)
			if r.ParticipationPct > st.Cost.ParticipationPctMax {
				st.Cost.ParticipationPctMax = r.ParticipationPct
			}
		}
		switch r.State {
		case tRStateDone:
			st.Done++
			st.NetPnl += r.TPnlNet
			st.GrossPnl += r.TPnlGross
		case tRStateUnwound:
			st.Unwound++
		case tRStateConvert:
			st.Converted++
			st.ConvertedQty += r.LegQty
			if strings.Contains(r.Reason, "熔断") {
				st.LossBreaker++
			}
		case tRStateOpen:
			st.OpenLegs++
			st.FrozenCash += r.FrozenCash
		}
		if r.Leg == tRLegSell {
			st.SellNotional += r.LegPrice * float64(r.LegQty)
		}
		st.Cost.Gross += r.TPnlGross
		st.Cost.Net += r.TPnlNet
		st.Cost.FeeBuy += r.FeeBuy
		st.Cost.FeeSell += r.FeeSell
		st.Cost.StampTax += r.StampTax
	}
	if filledRounds > 0 {
		st.Cost.FeeEstimateShare = float64(estimateRounds) / float64(filledRounds)
	}
	if n := len(participation); n > 0 {
		sort.Float64s(participation)
		if n%2 == 1 {
			st.Cost.ParticipationPctP50 = participation[n/2]
		} else {
			st.Cost.ParticipationPctP50 = (participation[n/2-1] + participation[n/2]) / 2
		}
	}
	st.NetPnl, st.GrossPnl = round2(st.NetPnl), round2(st.GrossPnl)
	st.FrozenCash, st.SellNotional = round2(st.FrozenCash), round2(st.SellNotional)
	st.Cost.Gross, st.Cost.Net = round2(st.Cost.Gross), round2(st.Cost.Net)
	st.Cost.FeeBuy, st.Cost.FeeSell = round2(st.Cost.FeeBuy), round2(st.Cost.FeeSell)
	st.Cost.StampTax, st.Cost.SlipCost = round2(st.Cost.StampTax), round2(st.Cost.SlipCost)
	st.Cost.FeeEstimateShare = round4(st.Cost.FeeEstimateShare)
	st.Cost.ParticipationPctP50 = round4(st.Cost.ParticipationPctP50)
	st.Cost.ParticipationPctMax = round4(st.Cost.ParticipationPctMax)
	return st
}

// ── §W-06-R4 / §W-09 纸面侧：与实盘**同口径**，不另立一套 ────────────────────────
//
// 纸面不是"简化版实盘"，它是灰度期唯一能回答"按这套规则跑会怎样"的对照物。
// 两侧终态分布一旦不可比，标定（B1/B2/B3）就失去意义——这不是统计口径问题，是证据失效。

// tRoundSpreadPct 与实盘 roundSpreadPctNow **同式**：价差=(卖额−买额)/首腿成交额×100。
// 纸面 LegQty 是首腿成交量、PairQty 是第二腿已成交量（open 态下为 0 ⇒ 全部按现价折算）。
// 同样**不含费用**（费用模型是 fillPnl 的事）。
func tRoundSpreadPct(r *TRound, price float64) (pct, notional float64, ok bool) {
	if r == nil || price <= 0 || r.LegQty <= 0 || r.LegPrice <= 0 {
		return 0, 0, false
	}
	matched := r.PairQty
	if matched > r.LegQty {
		matched = r.LegQty
	}
	var buyAmt, sellAmt float64
	if r.Leg == tRLegBuy {
		buyAmt = r.LegPrice * float64(r.LegQty)
		sellAmt = r.PairPrice*float64(matched) + price*float64(r.LegQty-matched)
		notional = buyAmt
	} else {
		sellAmt = r.LegPrice * float64(r.LegQty)
		buyAmt = r.PairPrice*float64(matched) + price*float64(r.LegQty-matched)
		notional = sellAmt
	}
	if notional <= 0 {
		return 0, 0, false
	}
	return (sellAmt - buyAmt) / notional * 100, notional, true
}

// tPaperAccelSuffix 失败原因里的加速标签（空标签⇒空串，非加速路径文案逐字不变）。
func tPaperAccelSuffix(tag string) string {
	if tag == "" {
		return ""
	}
	return "（accel=" + tag + "）"
}

// accelReasonLocked 纸面侧的三个触发源，判据与实盘 accelReason **逐条对应**：
// "stop"（价差率 ≤ −round_stop_pct）/ "verdict"（有活跃卖出裁决）/ "regime"（离开 range）。
//
// 比实盘**少一个腿龄门**：纸面 LegPrice 就是成交价，不存在"异步回报导致价格基准陈旧"，
// 而且 paper.TRound 没有 OpenedAt 字段，硬加一个还会动 /api/paper/t-rounds 的返回体。
// 这不是漏写——漏了腿龄门在纸面没有任何可防守的风险。
func (e *Engine) accelReasonLocked(r *TRound, price float64, p TParams) string {
	if r == nil {
		return ""
	}
	if p.RoundStopPct > 0 {
		if pct, _, ok := tRoundSpreadPct(r, price); ok && pct <= -p.RoundStopPct {
			return "stop"
		}
	}
	if p.ActiveSellVerdict != nil && p.ActiveSellVerdict(r.Code) {
		return "verdict"
	}
	if p.MarketState != "" && !p.MarketStateAt.IsZero() &&
		time.Since(p.MarketStateAt) <= tPaperRegimeStale {
		if p.MarketState == "bull" || p.MarketState == "bear" {
			return "regime"
		}
	}
	return ""
}

// tPaperRegimeStale 与实盘 tRegimeStale 同值（15 分钟）：主循环分钟级，
// 15 分钟没更新就是"停了"而不是抖动；出口误触发是真金白银的成交，宁可弃权。
const tPaperRegimeStale = 15 * time.Minute

// premiseGoneLocked 纸面侧"必须改终态"的判定（§W-06-R4 v3）。判据与实盘**同口径**，
// 同样**只剩 P3**：stop/verdict/regime 三个触发源都是"提前配平"，落在 accelReasonLocked。
// 写进这里就退化成"结掉不管"——熊市里结掉正 T 腿＝不卖底仓＝敞口变大，方向反。
func (e *Engine) premiseGoneLocked(code string, p TParams) (state, why string) {
	r := e.openTRoundFor(code)
	if r == nil || r.Leg != tRLegSell || r.LegQty <= 0 {
		return "", ""
	}
	if p.MarketPlungePct < 0 && p.IndexChangePct <= p.MarketPlungePct {
		// 纸面没有"熔断日"这个账户级判定（dayBlocked 由实盘的日亏闸门给出），
		// 用大盘急跌这个**同向**读数兜底：两者都是"回补通道被关"。
		// 与实盘 P3 一样只作用于反 T：正 T 提前结掉＝不卖底仓＝敞口更大。
		return tRStateUnwound, fmt.Sprintf(
			"盘中收摊：大盘急跌 %.2f%% ≤ %.2f%% ⇒ 反T卖腿转战略减仓（与实盘熔断日 P3 同向）",
			p.IndexChangePct, p.MarketPlungePct)
	}
	return "", ""
}

// tVolumeBlock 纸面侧量能门（§W-09）。判据与文案与实盘 volumeBlock **逐字一致**
// （§127 字面量一致纪律），包括"读数不可得按不过"这一条。
func (e *Engine) tVolumeBlock(code string, p TParams) string {
	if p.VolRatioMax <= 0 {
		return "" // 0 合法＝不设该条
	}
	if p.VolRatio == nil {
		return "量比读数无源（未接 VolRatio），按不过处理"
	}
	ratio, ok := p.VolRatio(code)
	if !ok || ratio <= 0 {
		return "量比读数不可得，按不过处理"
	}
	if ratio >= p.VolRatioMax {
		return fmt.Sprintf("放量：折算量比 %.2f ≥ %.2f（放量行情不做 T）", ratio, p.VolRatioMax)
	}
	return ""
}

// tRegimeBlockReason 纸面侧形态门（§W-09）。与实盘 regimeBlock 同口径：
// range 放行（票数不足只记日志）、bull/bear 关闭、空串弃权、未知值 fail-closed。
//
// 返回值是"原因"（非空＝拦），与实盘 regimeBlock 的返回语义一致，便于两侧共用断言常量。
func (e *Engine) tRegimeBlockReason(p TParams, now time.Time) string {
	st := p.MarketState
	if st != "" && (p.MarketStateAt.IsZero() || now.Sub(p.MarketStateAt) > tPaperRegimeStale) {
		e.tNoteBlockLocked("step0", fmt.Sprintf(
			"行情门弃权：市场状态读数过期（%q，%.0f 分钟前，票 %d/%d）——主循环可能已停",
			st, now.Sub(p.MarketStateAt).Minutes(), p.MarketStateVotes, p.MarketStateVotesTotal), "")
		return ""
	}
	switch st {
	case "range":
		// 与实盘同：票数不足时状态机已降级成 range（放行档），放行结论不变但**必须留一行**，
		// 否则复盘分不清"真震荡"和"票太少"。零票单独一条（兜底 range 更不可信也更安静）。
		switch {
		case p.MarketStateVotes == 0:
			e.tNoteBlockLocked("step0", "行情门放行（**零票**：状态机无任何有效输入 ⇒ 兜底 range，不是真震荡）——先查数据源", "")
		case p.MarketStateVotes < tPaperRegimeMinVotes:
			e.tNoteBlockLocked("step0", fmt.Sprintf(
				"行情门放行（研判质量不足：票 %d/%d < %d ⇒ 状态机已降级为 range，非真震荡）",
				p.MarketStateVotes, p.MarketStateVotesTotal, tPaperRegimeMinVotes), "")
		}
		return ""
	case "bull", "bear":
		return fmt.Sprintf("行情门：市场状态=%s，非盘整行情不做 T", st)
	case "":
		e.tNoteBlockLocked("step0", "行情门弃权：T 行情门已关（t.regime_gate_enabled=false）或读数从未产生，牛熊关闭 T 未生效", "")
		return ""
	default:
		return fmt.Sprintf("行情门：未知市场状态 %q，按不做 T 处理（fail-closed）", st)
	}
}

// tPaperRegimeMinVotes 与实盘 tRegimeMinVotes 同值（= research 状态机的 MinVotes 默认 4）。
const tPaperRegimeMinVotes = 4
