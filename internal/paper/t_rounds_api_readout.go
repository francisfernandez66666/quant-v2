// t_rounds_api_readout.go — §DYN-SIZE v4 / §DYN-T 的目标仓位与 T 额度**只读展示面**（方案 §8.8 末条）。
//
// 为什么这份读数由账本引擎自己算：v4 的四个数（目标 Tgt、战略当前 Cur、剩余 Room、单笔上限 Per）
// 的输入全部已经在引擎里——定权上下文由 engine 每轮注入（SetSizingContext），T 腿与持仓是账本本体。
// 让调用方（面板/排障脚本）自己拼这套数，就会出现"面板显示的剩余空间与实际下单用的不是同一个式子"
// 这种最难查的分叉；式子只留一处的唯一办法是让它住在数据旁边、走同一条归一路径。
// 因此下面对 ceiling / per_trade 的兜底与 dynamicPaperQtyLocked 里的写法逐字同参
// （ceiling 是 `<=0 || >1 → 1.0`；单笔占比一律走 signalctl.PerTradePctOf 同一个纯函数）：
// 改一处必须同时改另一处，否则面板就成了假读数。
//
// 刻意**不**给 MarketScale 兜底成 1.0：engine 还没注入定权上下文（启动首轮、或纯内存测试账本）时
// 显示 0 并让前端渲染"本轮尚未定权"，比显示一个恰好等于中性的 1.00 诚实。
// 同样刻意不给 StockScale：那是**逐信号**的数（个股组因子按每条信号重算），
// 账本级别没有稳定值可给，硬给一个数就是假读数——个股侧去看信号留痕。
// 单笔占比这里给的是**最近一笔信号**的 k 算出来的 eff，并把 k 一起回显（per_trade_k）：
// "这一帧按哪个因子算的"必须可核对，否则 26% 这个数就成了第二个假读数。
//
// English: one read-only frame of the v4 target model, computed next to the data it describes and
// normalized exactly like the sizing path (per-trade share goes through signalctl.PerTradePctOf).
// Uninjected MarketScale stays 0 instead of being faked as 1.00, and StockScale is absent because it
// is per-signal — instead the last observed k is echoed so the effective share is auditable.
package paper

import (
	"time"

	"quant-trading-v2/internal/signalctl"
)

// TargetReadout 目标仓位模型的一帧读数（面板/排障共用；不落盘、不参与任何判定）。
type TargetReadout struct {
	At              string  `json:"at"` // 取数时刻（RFC3339）——"这一帧是哪个市场的"必须可事后核对
	MarketScale     float64 `json:"market_scale"`
	Ceiling         float64 `json:"ceiling"`           // 归一后的目标仓位天花板
	PerTradePct     float64 `json:"per_trade_pct"`     // 单笔占比的**生效值** eff（含 k，不是下限）
	PerTradeLo      float64 `json:"per_trade_pct_lo"`  // 单笔占比区间下限（默认 10%）
	PerTradeHi      float64 `json:"per_trade_pct_hi"`  // 单笔占比区间上限（默认 30%）
	PerTradeK       float64 `json:"per_trade_k"`       // 算 eff 用的合成因子 k（0=还没有带缩放的信号，按中性 1.0 算）
	MissingKeyInput bool    `json:"missing_key_input"` // 关键市场读数缺失（目标封顶半仓 + 当天不做 T）
	Shadow          bool    `json:"shadow"`            // 影子期：只留痕不执法
	Enabled         bool    `json:"sizing_enabled"`

	Total       float64 `json:"total"`        // 账本权益（现金 + 持仓市值）
	Cash        float64 `json:"cash"`         // 现金
	MarketValue float64 `json:"market_value"` // 持仓市值

	Target    float64 `json:"target"`     // 目标市值 = Total × min(MS, Ceiling)
	CurStrat  float64 `json:"cur_strat"`  // 战略当前数 = 持仓市值 − 在途正T买腿 + 在途反T卖腿
	Room      float64 `json:"room"`       // 目标剩余空间 = max(0, Target − CurStrat)
	PerTrade  float64 `json:"per_trade"`  // 单笔上限（市值口径）= Total × 生效占比 eff（不再另乘 MS：eff 已含 k）
	RatioUsed float64 `json:"ratio_used"` // 真正决定目标仓位的那个占比 = min(MS, Ceiling)

	// T 额度：日内回转不是新增敞口，用的就是战略没占走的那部分（方案 §1.4 / §8.3）。
	TQuota       float64 `json:"t_quota"`        // T 额度 = Total − Target（弱市时这正是战略让出来的空间）
	CashT        float64 `json:"cash_t"`         // 可用 T 现金 = 现金 − 反 T 冻结专款
	FrozenCash   float64 `json:"frozen_cash"`    // 反 T 买腿冻结的专款（战略买入不得动用）
	OpenBuyLegs  float64 `json:"open_buy_legs"`  // 在途正 T 买腿市值
	OpenSellLegs float64 `json:"open_sell_legs"` // 在途反 T 卖腿市值

	// 当日反 T 总量帽（六轮 T6-2：分母是 min(总资产, 持仓市值)，不是总资产）
	SellCapBase  float64 `json:"sell_cap_base"` // 帽的分母
	SellNotional float64 `json:"sell_notional"` // 当日反 T 卖腿市值合计
	OpenLegs     int     `json:"open_legs"`     // 在途腿数（灰度期应当 ≤ 单票 1 次 × 候选票数）
	DoneRounds   int     `json:"done_rounds"`   // 当日已完成轮数
	RoundsOpened int     `json:"rounds_opened"` // 当日开腿总数（含收摊/转换）
}

// TargetReadout 返回目标仓位与 T 额度的一帧读数。
// 一次加锁取全部账本侧的数（权益/现金/在途腿/冻结款），因为它们必须来自同一帧——
// 分两次取会出现"Room 用的是旧持仓、Frozen 用的是新腿"的拼接读数，那种数看起来正常但无法对账。
// English: one lock, one frame — splitting the reads would produce a stitched number that looks
// plausible but can never be reconciled against a log line.
func (e *Engine) TargetReadout() TargetReadout {
	e.mu.Lock()
	ctx := e.sizingCtx
	mv := e.marketValueLocked()
	cash := e.cash
	buyLegs := e.tOpenBuyValueLocked()
	sellLegs := e.tOpenSellValueLocked()
	frozen := e.tFrozenCashLocked()
	total := cash + mv
	shadow := e.cfg.SizingShadow
	enabled := e.cfg.SizingEnabled
	e.mu.Unlock()

	// 归一写法与 dynamicPaperQtyLocked 逐字同参（见文件头为什么必须同源）。
	ceiling := ctx.Ceiling
	if ceiling <= 0 || ceiling > 1 {
		ceiling = 1.0
	}
	// §B0 单笔占比：回显**生效值 eff**（lo + (hi−lo)×k），不是下限 lo。
	// 旧写法回显 0.10 而实际按 26% 买，面板与下单是两套口径——那正是 owner 报"仓位管理累"时
	// 看到的第一层假读数。k 取最近一笔信号带下来的合成因子；还没见过任何信号（LastK=0）时
	// 按中性 k=1.0 算，并在 PerTradeK 里如实写出用的是哪个数（0 表示"这一帧是中性假设"）。
	// 区间的归一与下单路径共用一个函数（perTradeBandOf），否则"面板 0.10 / 实际 0.50"又会分家。
	perTradeLo, perTradeHi := perTradeBandOf(ctx)
	k := ctx.LastK
	if k <= 0 {
		k = 1.0
	}
	perTradePct := signalctl.PerTradePctOf(k, perTradeLo, perTradeHi)
	ratio := ctx.MarketScale
	if ratio > ceiling {
		ratio = ceiling
	}
	if ratio < 0 {
		ratio = 0
	}
	target := total * ratio
	curStrat := mv - buyLegs + sellLegs
	if curStrat < 0 {
		curStrat = 0
	}
	room := target - curStrat
	if room < 0 {
		room = 0
	}
	cashT := cash - frozen
	if cashT < 0 {
		cashT = 0
	}
	base := total
	if mv > 0 && mv < base {
		base = mv // min(总资产, 持仓市值)：低仓账号不许按总资产拿到一个比自己持仓还大的卖额
	}
	// 当日汇总与明细走各自导出的读数（内部又是另一把锁）：这里只取"计数"字段，
	// 与上面的市值不是同一帧的风险已经由字段语义挡住——那两个数是**当日累计**，不随一轮变化。
	day := e.TRoundDay("")
	return TargetReadout{
		At:              e.clockNow().Format(time.RFC3339),
		MarketScale:     ctx.MarketScale,
		Ceiling:         ceiling,
		PerTradePct:     perTradePct,
		PerTradeLo:      perTradeLo,
		PerTradeHi:      perTradeHi,
		PerTradeK:       ctx.LastK,
		MissingKeyInput: ctx.MissingKey,
		Shadow:          shadow,
		Enabled:         enabled,
		Total:           round2(total),
		Cash:            round2(cash),
		MarketValue:     round2(mv),
		Target:          round2(target),
		CurStrat:        round2(curStrat),
		Room:            round2(room),
		PerTrade:        round2(total * perTradePct),
		RatioUsed:       ratio,
		TQuota:          round2(total - target),
		CashT:           round2(cashT),
		FrozenCash:      round2(frozen),
		OpenBuyLegs:     round2(buyLegs),
		OpenSellLegs:    round2(sellLegs),
		SellCapBase:     round2(base),
		SellNotional:    day.SellNotional,
		OpenLegs:        day.OpenLegs,
		DoneRounds:      day.Done,
		RoundsOpened:    day.Opened,
	}
}
