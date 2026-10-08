// Package trading — settlement.go §WS-B 券商交割单三方对账。
// 权威源=券商交割单（网关 /settlement）；对照=本地账本 fills；差异落 settlement_diff + 告警。
// 纠偏：mode=report_only 只报；mode=sync_fills 把券商有、本地无的成交补记（幂等），
// 本地有券商无的标 phantom 待人工（绝不自动删，防券商查询延迟误判）。
// English: §WS-B three-way settlement reconciliation. Broker settlement (gateway /settlement) is the
// authoritative leg vs the local fills ledger; diffs persist to settlement_diff and alert. In
// sync_fills mode broker-only fills are backfilled idempotently; local-only fills are flagged phantom
// for manual review (never auto-deleted, guarding against broker-query lag).
package trading

import (
	"fmt"
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
)

// SettlementFetcher 交割单拉取接口（QMTClient 实现；Noop/桩返回不支持）。
// English: settlement fetch interface (implemented by QMTClient; Noop/stubs report unsupported).
type SettlementFetcher interface {
	FetchSettlement(date string) (*SettlementResponse, error)
}

// SettleMode 纠偏模式（report_only 默认 / sync_fills 补记）。
// English: reconciliation fix mode.
const (
	SettleModeReportOnly = "report_only" // 只出对账单+告警（默认）
	SettleModeSyncFills  = "sync_fills"  // 券商有本地无 → 补记成交
)

// SettleOutcome §P2-E（2026-10-06 修复批 波 5）对账的**三态**结论。
//
// 为什么必须有三态（缺陷原文，docs/AUDIT_20261005 报告 P2-E）：旧 SettleDay 在两条"本轮根本
// 没比对成"的分支上返回 `(nil, nil)`——执行器不支持交割单（Noop/桩）与网关未连接——而调用侧
// MaybeSettleDay 把 `err == nil` 一律当"成功"记账：`c.lastSettleDay = day` 照写、失败量规归零、
// 当日不再重投。于是三方对账这道**日终唯一的安全网**在最该报警的两种形态下表现为"今天对过了"：
//   - 网关整日未连接 ⇒ 当日差异永远不会被发现，而日志里连一条 skipped 都没有（只有一句 log.Printf）；
//   - 执行器换了/降级成 Noop ⇒ 同一天起永久"成功"，且**没有任何指标能把它和"对过、确实无差异"区分开**
//     （两条分支都写 settlement_diff_count=0，与"对完了没有差异"用同一个读数）。
//
// 这不是"误告警"问题而是"账面说谎"问题：lastSettleDay 的语义被 §D4 收紧成"成功一次才算完成"，
// 而"跳过"仍然算成功，等于 §D4 的闸被这两条分支从后面绕开了。
//
// 三态各自口径（owner 裁决 ② 按推荐项落码，写进本批 commit message 供事后否决）：
//   - SettleOutcomeVerified：真比对了（有无差异由 settlement_diff_count 表达），才允许推进 lastSettleDay。
//   - SettleOutcomeSkippedNotConnected：网关未连接＝**本轮未验证**，属"本该能跑而没跑成"的**失败向**
//     ——不推进 lastSettleDay，交给 §D4 的 10 分钟节流重试，量规抬升、告警可销案。
//   - SettleOutcomeSkippedNoFetcher：执行器结构性不支持（Noop/桩，非瞬时故障）＝**不适用向**
//     ——同样不推进 lastSettleDay（账面不能说"对过"），但当日只尝试一次并只留痕一次，
//     否则模拟盘/降级执行器会每分钟往日志和量规上刷一条"永远好不了"的记录（§CAL-GATE 刷屏判例）。
//
// English: §P2-E — three-state settlement outcome. "Skipped" is no longer counted as success:
// only Verified may advance lastSettleDay, while the two skip reasons keep distinct gauge values
// (not-connected retries; unsupported-executor short-circuits for the day but never claims success).
type SettleOutcome int

const (
	// SettleOutcomeUnknown 零值占位：调用侧拿到零值＝"没有结论"，任何判据都必须显式覆盖它
	// （绝不让零值顺带落进"已对账"分支——本仓 §P1-B/§D4 同族教训：兜底分支的默认值就是判据的方向）。
	SettleOutcomeUnknown SettleOutcome = iota
	// SettleOutcomeVerified 真完成了三方比对。
	SettleOutcomeVerified
	// SettleOutcomeSkippedNotConnected 网关未连接：交割单不可信，本轮未验证（失败向，会重试）。
	SettleOutcomeSkippedNotConnected
	// SettleOutcomeSkippedNoFetcher 执行器不支持交割单：结构性不适用（当日短路）。
	SettleOutcomeSkippedNoFetcher
)

// §P2-E 量规键名口径（写在这里，不落 const 别名）：
// 键 `settlement_state` 的读数 = 0 本轮未得出比对结论（含调用即出错）/ 1 已对账 / 2 未连接跳过 /
// 3 执行器不支持。规则 settlement_not_verified 用 `ge 2` 判"本轮未验证"，因此 0 与 1 都安全、2/3 破线。
// 刻意**不定义** `const settleStateGauge = "settlement_state"` 再拿变量去 SetGauge：
// 门禁那枚「SetGauge("<键名>" 字面赋值点」守卫是按字面串数赋值点的，键名一旦被别名化，守卫看到的
// 就是 `SetGauge(settleStateGauge` —— 赋值点数成 0，"量规恒不写"这种坏法在它面前是隐身的
// （09-29 14:0x 判红实录，§DEADGAUGE 三件套里"键名 const 别名 ban 负锁"就是为这条立的）。
// 为什么用**一个键的三个值**而不是三个键：三个键要靠"彼此不冲突"来表达一个状态，而它们由同一函数
// 同一时刻写入，冲突时后写覆盖前写且报出来的读数不属于任何一次调用（§ADJ-BASIS-2P 的反面纪律：
// 同刻多写共用一键会失真；这里是同一状态的三个取值，共键才是原子的）。
// English: the gauge key stays a literal at its single write site on purpose — aliasing it would
// blind the gate's "literal SetGauge assignment points" guard.

// String 三态的可读名（留痕/HTTP 响应都用它，禁止调用侧自己拼字符串——四份文案必然漂三份）。
// English: canonical human-readable name for each outcome.
func (o SettleOutcome) String() string {
	switch o {
	case SettleOutcomeVerified:
		return "verified"
	case SettleOutcomeSkippedNotConnected:
		return "skipped-not-verified:gateway_not_connected"
	case SettleOutcomeSkippedNoFetcher:
		return "skipped-not-verified:executor_unsupported"
	default:
		return "unknown"
	}
}

// Verified 只有真比对过才算 true；未知/跳过一律 false（fail-closed 方向）。
// English: only a real three-way comparison counts; skip and unknown are both false.
func (o SettleOutcome) Verified() bool { return o == SettleOutcomeVerified }

// gaugeValue 三态落到量规的数值（0/1/2/3），与规则 `ge 2` 的判据同源（本函数是唯一映射点）。
// English: the single place mapping outcome → gauge number, so the rule threshold and the writer agree.
func (o SettleOutcome) gaugeValue() int64 {
	switch o {
	case SettleOutcomeVerified:
		return 1
	case SettleOutcomeSkippedNotConnected:
		return 2
	case SettleOutcomeSkippedNoFetcher:
		return 3
	default:
		return 0
	}
}

// settleUnattributedPrefix §0925EVE-W3-E（C5）：无 signal_id 的交割行补记时的专属标记前缀。
// 语义 =「无归因行」：这条成交来自券商交割单、但来源侧确实给不出归因信号（旧网关行/柜台
// 手工流水回灌等），它**不是**真实 signal_id，也绝不冒充一个——前缀自成一路，归因/统计侧可
// 按前缀整体识别并剔除，而不是像旧虚构键 "settle:"+day 那样同日全部成交共用一键、还与真实
// signal_id 同用普通字符串通道（不可分辨）。历史库中已落库的 "settle:"+day 旧行不自动改写
// （留痕原则），需要按日清理时另行数据订正。
// English: §0925EVE-W3-E — explicit "unattributed row" marker prefix for settlement backfills
// whose gateway row carries no signal_id; deliberately distinguishable from real signal ids
// (the old fabricated "settle:"+day key was indistinguishable and hijacked attribution).
const settleUnattributedPrefix = "settle-unattributed:"

// brokerTrade 归一后的券商成交（用于三方比对）。
// English: normalized broker trade for three-way comparison.
type brokerTrade struct {
	Code     string
	Side     string
	Price    float64
	Qty      int
	Fee      float64
	OrderID  string // 券商委托号（sync_fills 补记时回填真实委托号，不再置空）
	TradedAt string // 券商成交时间（补记时回填真实时间，不再用 day+" 00:00:00"）
	Serial   string // 交割流水号（网关 trade_id）；缺失时不参与匹配（见 factKey）
	// SignalID §0925EVE-W3-E（C5）：网关交割行的真实归因信号；空串=该行无归因
	// （旧网关版本/手工柜台流水回灌等来源不带 signal_id），补记时单独标记不冒充。
	SignalID string
}

// normalizeSettleDay §H1（2026-09-22 修复批）对账日期口径归一：YYYYMMDD → YYYY-MM-DD。
// 网关 /settlement 只受理 `YYYY-MM-DD`（gateway.py 校验），本地 fills 侧 ListFillsByDay 也用
// `substr(traded_at,1,10)=?` 的带杠日期——而自动调度路 engine 传入的是 data.TradingDayDate 的
// 无杠 20060102（qmt_client.go 旧注释还写着 "date=YYYYMMDD" 误导），结果自动三方对账**恒 400**、
// 从未真正跑过一次。在唯一入口做防御式归一，两路口径一次收编；无法归一的原样透传给网关报错。
// English: §H1 — normalize YYYYMMDD to YYYY-MM-DD at the settlement entry; the auto path passed
// undashed dates the gateway rejects, so scheduled three-way reconciliation always 400'd.
func normalizeSettleDay(day string) string {
	if len(day) == 8 {
		digits := true
		for _, r := range day {
			if r < '0' || r > '9' {
				digits = false
				break
			}
		}
		if digits {
			return day[0:4] + "-" + day[4:6] + "-" + day[6:8]
		}
	}
	return day
}

// SettleDay 执行某交易日三方对账：券商交割单 ↔ 本地 fills ↔ real_account。
// 返回（差异摘要, §P2-E 三态结论, 错误）。
//
// §P2-E 之前的口径是「网关不支持/未连接 → (nil, nil) 静默跳过」，而调用侧把 `err == nil` 当成功，
// 于是"没比对成"和"比对完没有差异"共用一本文账（详见 SettleOutcome 的成因注释）。现在两条跳过腿
// 各自返回显式第三态，调用侧**必须**按 outcome 判定是否记为"当日已对账"。
// English: runs three-way settlement for a day and returns (diff, §P2-E outcome, error). Skip
// branches now carry an explicit non-verified outcome instead of masquerading as success.
func (c *Controller) SettleDay(day, mode string) (*store.SettlementDiff, SettleOutcome, error) {
	// §P2-E：量规按**每次调用的最终结论**写一次（defer 保证出错/提前 return 也不会留下上一次的残值）。
	// 键名保持字面量、不落 const 别名，成因见上面那段（§DEADGAUGE 键名别名 ban 同族纪律）。
	outcome := SettleOutcomeUnknown
	defer func() { metrics.SetGauge("settlement_state", outcome.gaugeValue()) }()
	day = normalizeSettleDay(day) // §H1 口径归一先于一切（落库键/拉取参数/补记时间戳共用）
	if c.store == nil {
		return nil, outcome, fmt.Errorf("real book not set")
	}
	f, ok := c.execRef().(SettlementFetcher)
	if !ok {
		// §P2-E：执行器结构性不支持（Noop/桩）＝不适用向，旧此处的 `(nil, nil)` 会被调用侧记成成功。
		// 「本轮到底验证没验证」从此由**两个键合起来**表达，缺一不可：
		//   · settlement_state = 3（未验证/不适用）——这是本批新增的第三态；
		//   · settlement_diff_count 归零——这是 §DEADGAUGE（09-23 傍晚批）负锁③的既有要求：
		//     跳过分支「什么都不写」会把上一轮真比对出的差异条数留在量规上，让规则
		//     settlement_diff（gt 0）在跳过日继续按陈旧差异天天报 p1（残值冒充当日读数）。
		// 本批一度把这行归零当成"降级报成功"删掉，方向错了：冒充成功的是 **lastSettleDay 与
		// 「已对账」账面**，不是差异读数归零；差异键的语义本来就是"当前可数的差异条数"，
		// 没在比对＝可数差异为 0，而"这个 0 可不可信"由 settlement_state 判定。
		// English: keep the diff gauge at zero (stale residue would keep firing p1 on a day we
		// never actually reconciled) and let settlement_state=3 carry the "not verified" meaning.
		log.Printf("[settle] 当前执行器不支持交割单（Noop/桩），%s 本轮**未验证**（§P2-E：不再记为已对账）", day)
		outcome = SettleOutcomeSkippedNoFetcher
		metrics.SetGauge("settlement_diff_count", 0)
		return nil, outcome, nil
	}
	if mode == "" {
		mode = SettleModeReportOnly
	}
	resp, err := f.FetchSettlement(day)
	if err != nil {
		return nil, outcome, fmt.Errorf("fetch settlement %s: %w", day, err)
	}
	if !resp.Connected {
		// §P2-E：网关未连接＝失败向的"本轮未验证"（交割单不可信，绝不能推进 lastSettleDay），
		// 交给 MaybeSettleDay 的 §D4 十分钟节流重试；成功那轮再把 settlement_state 写回 1。
		// 差异读数同样归零（与上面"不支持"分支同一个理由）：跳过日留下的不是"无差异"这个结论，
		// 而是"此刻可数的差异为 0"，规则 settlement_diff 因此不会在断线日拿昨天的差异刷屏。
		// English: zero the diff gauge on the not-connected leg too — the stale count from the
		// last verified day must not keep the gt-0 rule firing through an outage.
		log.Printf("[settle] 网关未连接，交割单不可信，%s 本轮**未验证**（§P2-E：不记为已对账，按节流重试）", day)
		outcome = SettleOutcomeSkippedNotConnected
		metrics.SetGauge("settlement_diff_count", 0)
		return nil, outcome, nil
	}

	// 归一券商成交（side 统一为 买入/卖出）。§P0-1b（2026-09-15）：匹配键改为「物理事实键」
	// 多重集合匹配——旧实现 brokerTrade.CorrKey 传 Serial+空 OrderID/TradedAt，网关不产出 serial 时
	// 键塌缩为 "f:@@买入"，同向成交在 map 里互相覆盖（对账必出全量假差异）；本地 fills 又从不写
	// Serial（网关回报不带），两侧键恒不匹配。新键 = 代码|方向|数量|价格(分)，同键多笔按列表逐一配对。
	broker := map[string][]brokerTrade{}
	var brokerFeeTotal float64
	for _, t := range resp.Trades {
		side := normalizeSide(t.Side)
		if side == "" {
			continue // 无法归一：跳过（记录在 diff）
		}
		bt := brokerTrade{
			Code: t.TsCode, Side: side, Price: t.Price, Qty: t.Qty,
			Fee: t.Fee + t.StampTax, OrderID: t.OrderID, TradedAt: t.TradedAt, Serial: t.Serial,
			SignalID: strings.TrimSpace(t.SignalID), // §0925EVE-W3-E 真实归因随成交一路带到补记腿
		}
		k := settleFactKey(bt.Code, bt.Side, bt.Qty, bt.Price)
		broker[k] = append(broker[k], bt)
		brokerFeeTotal += bt.Fee
	}

	// 本地成交（同样按物理事实键分桶）
	localFills, err := c.store.ListFillsByDay(c.userID, day)
	if err != nil {
		return nil, outcome, err
	}
	local := map[string][]store.RealFill{}
	var localFeeTotal float64
	for _, f := range localFills {
		k := settleFactKey(f.Code, f.Side, f.Qty, f.Price)
		local[k] = append(local[k], f)
		localFeeTotal += f.Fee + f.StampTax
	}

	diff := &store.SettlementDiff{
		Day: day, Mode: mode,
		LocalFeeTotal:  localFeeTotal,
		BrokerFeeTotal: brokerFeeTotal,
		FeeDiff:        brokerFeeTotal - localFeeTotal,
	}
	// 券商有 → 本地有无：同键列表逐一配对，券商多出的笔数即 MissingInLocal
	type missingTrade struct {
		bt brokerTrade
		k  string
	}
	var missing []missingTrade
	for k, bts := range broker {
		lfs := local[k]
		for i, bt := range bts {
			if i < len(lfs) {
				lf := lfs[i]
				// 同键存在：校验 量/价 一致（键已含量价，此处防御价格分位舍入差）
				if lf.Qty != bt.Qty || abs(lf.Price-bt.Price) > 0.011 {
					diff.Mismatch = append(diff.Mismatch, fmt.Sprintf("%s %s 量价不符 本地(%d@%.2f) vs 券商(%d@%.2f)",
						k, bt.Code, lf.Qty, lf.Price, bt.Qty, bt.Price))
				}
			} else {
				diff.MissingInLocal = append(diff.MissingInLocal, fmt.Sprintf("%s %s %d@%.2f",
					k, bt.Code, bt.Qty, bt.Price))
				missing = append(missing, missingTrade{bt: bt, k: k})
			}
		}
	}
	// 本地有 → 券商有无
	for k, lfs := range local {
		bts := broker[k]
		for i, lf := range lfs {
			if i >= len(bts) {
				diff.ExtraInLocal = append(diff.ExtraInLocal, fmt.Sprintf("%s %s %d@%.2f",
					k, lf.Code, lf.Qty, lf.Price))
			}
		}
	}

	// 现金差（券商交割口径 vs 本地 real_account）
	if acc, aerr := c.store.GetRealAccount(c.userID); aerr == nil && acc.AvailableCash > 0 {
		if brokerCash := resp.Cash["cash"]; brokerCash > 0 {
			diff.CashDiff = brokerCash - acc.AvailableCash
		}
	}

	// 纠偏。§P0-1c（2026-09-15）：补记不再用 OrderID=""/TradedAt=day 00:00:00 的占位键——
	// 旧键与真实成交的 (order_id,traded_at,price,qty) 判重键永不重叠，ApplyRealFill 拦不住重复，
	// real_positions 会被真实双倍累加（资损级）。现在回填券商真实委托号+真实成交时间
	// （仅时间无日期时拼对账日），双层幂等：(order_id,traded_at,price,qty) 判重 + 事实键本身已配对。
	if mode == SettleModeSyncFills && len(missing) > 0 {
		for _, m := range missing {
			bt := m.bt
			tradedAt := bt.TradedAt
			if tradedAt == "" {
				tradedAt = day + " 00:00:00"
			} else if len(tradedAt) == 8 { // 网关只回时间（HH:MM:SS）→ 拼对账日
				tradedAt = day + " " + tradedAt
			}
			// §0925EVE-W3-E（C5）：补记成交的归因键优先沿用网关交割行带来的真实 signal_id
			// （下单链路 signal_id 是唯一键，交割腿与委托腿天然同源）；确实没有的行打
			// 「无归因行」专属前缀单独标记——旧实现无条件写虚构 "settle:"+day，把有真实
			// 归因的成交也顶掉了，战法/信号级盈亏统计里这批补记腿全部错位或落入未知桶。
			signalID := bt.SignalID
			if signalID == "" {
				signalID = settleUnattributedPrefix + day
				log.Printf("[settle] sync_fills 补记遇无归因行（网关未带 signal_id）：%s %s %d@%.2f 委托=%s → 标记 %q",
					bt.Code, bt.Side, bt.Qty, bt.Price, bt.OrderID, signalID)
			}
			fill := store.RealFill{
				OrderID: bt.OrderID, Code: bt.Code, Side: bt.Side, Price: bt.Price, Qty: bt.Qty,
				Amount: bt.Price * float64(bt.Qty), TradedAt: tradedAt,
				SignalID: signalID, UserID: c.userID, Fee: bt.Fee, Serial: bt.Serial,
			}
			if err := c.store.ApplySettlementFill(fill); err != nil {
				log.Printf("[settle] sync_fills 补记失败 %s: %v", m.k, err)
			}
		}
	}

	if err := c.store.SaveSettlementDiff(c.userID, *diff); err != nil {
		return nil, outcome, err
	}
	// §DEADGAUGE（2026-09-23 傍晚批收尾）：告警规则 settlement_diff（量规 settlement_diff_count，
	// p1「交割单对账出现差异」）自 09-15 注册以来全仓无赋值点 = 永不触发的死规则（audit N-1 同族，
	// 那次是 settle_fail_streak）。差异条数在这里第一次成为已知量，且本函数同时服务定时链
	// （MaybeSettleDay）与手工链（POST /api/qmt/settle），在此赋值即两条链同源覆盖。
	// 注意取「三类条数之和」而非 diff 是否为 nil：sync_fills 补记后会清空 missing，但多余/不符
	// 仍需按当日实况告警，故必须在落库/日志口径之后统计。
	metrics.SetGauge("settlement_diff_count", int64(len(diff.MissingInLocal)+len(diff.ExtraInLocal)+len(diff.Mismatch)))
	// §P2-E：走到这里才是"当日已验证"——defer 那一次 SetGauge("settlement_state", …) 会据此写 1，
	// 与上面 settlement_diff_count=0（对完了、确实没有差异）从此**可分辨**：
	// 旧形态下"没比对成"与"比对无差异"都是 diff_count=0，运维面看不出任何区别（本条缺陷的原文）。
	outcome = SettleOutcomeVerified
	log.Printf("[settle] %s 三方对账完成: 缺失=%d 多余=%d 不符=%d 费用差=%.2f 现金差=%.2f (mode=%s)",
		day, len(diff.MissingInLocal), len(diff.ExtraInLocal), len(diff.Mismatch),
		diff.FeeDiff, diff.CashDiff, mode)
	return diff, outcome, nil
}

// settleFactKey 三方对账的「物理事实键」（§P0-1b，2026-09-15）：代码|方向|数量|价格(分)。
// 刻意不依赖 serial/order_id/时间戳——网关不产出 serial、回报 order_id 形态随通道（xt 交易所号 /
// queued seq 占位）变化、时间戳两侧口径不一，任何依赖它们的键都必然两侧永不匹配（旧实现实录）。
// 物理事实（什么代码、什么方向、多少股、什么价）两侧必然一致，同键多笔用列表逐一配对。
// English: §P0-1b — the "physical fact key" for reconciliation: code|side|qty|price(cents).
// Deliberately independent of serial/order_id/timestamps (gateway never produced serials; order-id
// shapes differ per channel; timestamp formats diverge) — any such key never matched across sides.
// The physical facts must agree; multi-fill same keys are paired positionally via lists.
func settleFactKey(code, side string, qty int, price float64) string {
	return fmt.Sprintf("%s|%s|%d|%.2f", code, side, qty, price)
}

// normalizeSide 归一交割方向（买入/卖出）；无法识别返回空串。
// English: normalizes a settlement side string to 买入/卖出; "" when unrecognized.
func normalizeSide(s string) string {
	ss := strings.TrimSpace(s)
	switch strings.ToLower(ss) {
	case "buy", "b", "买入", "买":
		return "买入"
	case "sell", "s", "卖出", "卖":
		return "卖出"
	}
	return ""
}

// abs 浮点绝对值。
// English: float absolute value.
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// settleRetryInterval §D4（2026-09-22 修复批）对账失败后的当日重试间隔（节流窗）。
// 变量而非常量：单测要能在毫秒级跑完「失败→立即可再试」与「窗口内不再试」两极。
// 取值理由：scoreCycle 每轮（约 60s）都会调用 MaybeSettleDay，10 分钟 ≈ 10 次机会/小时，
// 盘后网关抖动通常几十分钟内恢复；同时绝不退化成"每轮死循环重投"打爆网关。
// English: §D4 — in-day retry throttle after a failed settlement (var so tests can shrink it).
var settleRetryInterval = 10 * time.Minute

// MaybeSettleDay §WS-B 调度入口（节流+交易日门控）：每天只对账一次指定日期，
// 且仅在到达 settle_at（默认 15:30，北京时）之后触发（交割单常盘后 15:30 才全）。
// §D4（2026-09-22 修复批）：语义由「尝试一次即视为完成」改为「**成功一次**才算完成」。
// 缺陷原文：旧实现把 `c.lastSettleDay = day` 放在调用 SettleDay **之前**（:267-269），
// 而顶部的 `if last == day { return }`（:256-258）按同一字段判定同日是否已对账——
// 于是一次网关超时/落库报错就永久烧掉当日唯一一次对账机会（失败分支 :271-277 只 log+计指标，
// 没有任何补偿路径），三方对账这道日终安全网事实上"每天最多跑一次、且跑砸就当跑过"。
// 修法：① 置位移到成功之后（失败绝不写 lastSettleDay）；② 失败后按 settleRetryInterval 节流
// 重试（attempt 戳在调用前置，防止 scoreCycle 每 60s 打爆网关）；③ 当日失败次数计入 opslog
// 留痕（"第 N 次"），让连续失败在运维日志里可数、可判定是偶发抖动还是系统性故障。
// English: §D4 — the day is marked done only on success; failures retry under a throttle window and
// are counted in the ops log, instead of being burned by a pre-set idempotency stamp.
//
// §P2-E（2026-10-06 修复批 波 5）把"成功"的口径再收一刀：本函数原先只把 `err != nil` 当失败，
// 而 SettleDay 的两条跳过腿返回 (nil, nil)，于是**跳过被记成成功**——lastSettleDay 照写、失败
// streak 照归零、当日不再重投。现在只有 `outcome.Verified()` 才记账；两条跳过腿各按自己的节奏
// 重试（未连接＝每个 retry 窗口真试一次；执行器不支持＝当日短路一次并只留一次痕），
// 且都不推进 lastSettleDay、都不发 recover。
// English: §P2-E — a skipped reconciliation no longer counts as success; only Verified may
// advance lastSettleDay or reset the failure streak.
func (c *Controller) MaybeSettleDay(day string, mode string, settleAt int, enabled bool) {
	if !enabled || c.store == nil || !c.Enabled() {
		return
	}
	day = normalizeSettleDay(day) // §H1 归一先于幂等比较，lastSettleDay 恒为带杠口径
	c.mu.RLock()
	last := c.lastSettleDay
	lastAttempt := c.lastSettleAttemptAt
	c.mu.RUnlock()
	if last == day {
		return // 当日已成功对账，跳过（§D4：本字段现在只代表"成功"）
	}
	if settleAt <= 0 {
		settleAt = 1530
	}
	now := cntime.In(time.Now())
	if now.Hour()*100+now.Minute() < settleAt {
		return // 未到对账时刻
	}
	// §D4 节流：距最近一次尝试不足 retry 窗口时不再重投（attempt 为零值=当日首次，直接放行）。
	if !lastAttempt.IsZero() && time.Since(lastAttempt) < settleRetryInterval {
		return
	}
	// §P2-E 结构性不适用的**调用前**短路：执行器不支持交割单（Noop/桩）是配置期就定下的事实，
	// 重投改变不了结论，却会把 settlement_state 反复覆写、并按十分钟节奏往日志里刷一条
	// "永远好不了"的留痕（§CAL-GATE 刷屏判例：持续性状态走必推/高频留痕＝淹没真信号）。
	// 短路条件刻意要求"原因也是 NoFetcher"：若当日稍后执行器换成支持的（或网关从不连变已连），
	// 下面那一步会把戳覆写成新原因，本判据随即失效，当日仍有机会真对一次账。
	if c.settleSkipShortCircuits(day) {
		return
	}
	// 尝试戳先置位（持锁写）：这是防死循环的唯一护栏——成功与否都不回滚它，只回滚"当日已完成"标记。
	c.mu.Lock()
	c.lastSettleAttemptAt = time.Now()
	c.mu.Unlock()
	diff, outcome, err := c.SettleDay(day, mode)
	if err != nil {
		// §D4：失败**不置** lastSettleDay（旧实现是在调用前置位，等于把失败当成功记账），
		// 下一个 retry 窗口会再试一次；同时保留 §H1 的指标计数与 opslog 留痕，并把当日
		// 第几次失败写进留痕（连续失败与偶发抖动的处置动作不同，必须可数）。
		c.mu.Lock()
		if c.settleFailDay != day {
			c.settleFailDay = day
			c.settleFailCount = 0
		}
		c.settleFailCount++
		attempt := c.settleFailCount
		c.mu.Unlock()
		log.Printf("[settle] 对账失败（当日第 %d 次，%s 后重试）: %v", attempt, settleRetryInterval, err)
		metrics.SettleFailed()
		// §N-1 教训（死规则）：告警规则必须有生产侧真实赋值点，否则规则永不触发。
		// settle_fail_streak = 当日连续失败次数（成功即归零），供 alerter 规则 settle_failed 消费；
		// 用「 streak 量规」而非「累计计数器」，是为了让规则能在恢复后自动发 recover 事件。
		// English: §N-1 lesson — the gauge feeding the future settle_failed rule is set here in
		// production code (streak, not cumulative counter, so recovery is observable).
		metrics.SetGauge("settle_fail_streak", int64(attempt))
		opslog.Logf("quant", "交割单三方对账失败 账户=%s 日=%s 当日第%d次（%s 后自动重试，成功后才记为已对账）: %v",
			c.userID, day, attempt, settleRetryInterval, err)
		return
	}
	// §P2-E（2026-10-06 修复批 波 5）：**跳过不再是成功**。
	// 旧此处的注释写着"成功（含 SettleDay 返回 (nil,nil) 的'网关不支持/未连接，静默跳过'分支）"，
	// 于是两条"根本没比对成"的腿一起把 lastSettleDay 写掉、把失败 streak 归零——三方对账这道
	// 日终安全网在最需要它的那一天表现为"今天对过了"，且事后日志里只有一句 log.Printf。
	if !outcome.Verified() {
		c.recordSettleSkip(day, outcome)
		// 刻意**不**归零 settle_fail_streak：归零＝向告警面宣布"对账已恢复"，而本轮什么都没比对；
		// 上一轮失败的告警必须一直挂到真验证成功那轮（告警只 fire 不 recover 是缺陷，反过来
		// 用"没验证"去发 recover 是同族缺陷的另一半）。
		if outcome == SettleOutcomeSkippedNotConnected {
			log.Printf("[settle] %s 本轮未验证（网关未连接），当日不记为已对账，%s 后重试", day, settleRetryInterval)
			opslog.Logf("quant", "交割单三方对账跳过 账户=%s 日=%s 原因=%s（§P2-E：网关未连接＝本轮未验证，当日不记为已对账，按 %s 窗口自动重试）",
				c.userID, day, outcome.String(), settleRetryInterval)
		} else {
			// 未知态（零值）也走这一支留痕：SettleDay 新增返回位却忘了置位时，账面必须显示"没结论"，
			// 绝不让零值顺带落进 verified 分支——本仓 §P1-B/§D4 同族教训：兜底分支的默认值就是判据方向。
			log.Printf("[settle] %s 本轮未验证（执行器不支持/无结论 %s），当日不记为已对账", day, outcome.String())
			opslog.Logf("quant", "交割单三方对账不适用 账户=%s 日=%s 原因=%s（§P2-E：结构性不支持，当日不记为已对账，同一原因当日只留痕一次）",
				c.userID, day, outcome.String())
		}
		return
	}
	// 真验证成功：当日记账完成，后续窗口不再重投。
	c.mu.Lock()
	c.lastSettleDay = day
	c.lastSettleSkipDay = "" // §P2-E：清掉跳过戳，旧原因不得把后续轮次挡在短路判据外
	c.lastSettleSkipOutcome = SettleOutcomeUnknown
	if c.settleFailDay == day && c.settleFailCount > 0 {
		opslog.Logf("quant", "交割单三方对账恢复 账户=%s 日=%s（此前当日失败 %d 次后成功）", c.userID, day, c.settleFailCount)
		c.settleFailCount = 0
	}
	c.mu.Unlock()
	// §N-1：成功即把 streak 量规归零——alerter 的 settle_failed 规则据此发 recover 事件，
	// 否则失败后会一直停在触发态（告警只 fire 不 recover 等于没有恢复通知）。
	metrics.SetGauge("settle_fail_streak", 0)
	if diff != nil && (len(diff.MissingInLocal)+len(diff.ExtraInLocal)+len(diff.Mismatch) > 0) {
		c.fireOnAlert("high", "券商交割单三方对账差异",
			fmt.Sprintf("%s: 缺失%d 多余%d 不符%d 费用差%.2f（详见 settlement_diff）",
				day, len(diff.MissingInLocal), len(diff.ExtraInLocal), len(diff.Mismatch), diff.FeeDiff))
	}
}

// settleSkipShortCircuits §P2-E：本轮是否应当因为"同一日同一结构性原因已经试过了"而不必再试。
// 判据是**三个**条件，不是一个日期：① 当日戳 ② 当日原因＝NoFetcher ③ **此刻执行器仍然不支持**。
// 第三条必须每次重新核实到来源，不能只读那个派生出来的戳：executor 会在交易时段被
// ApplyPendingConfig 换装（controller.go §FIX#7 的原子引用就是为它立的），"从 Noop 换成支持
// 交割单的真实执行器"是一条现网会走到的恢复路径——只比日期就把这一天剩下的窗口全挡掉，
// 等于用一份旧读数否决了新事实（本仓「派生状态不是来源」同族教训，10-05 那次是档位 from=env 撒谎）。
// 未连接腿不在短路之列（它是瞬时状态，试通了就当场验证，短路只会把恢复推迟到次日）。
// English: §P2-E — short-circuit only when the day stamp, the structural reason, AND the *current*
// executor's capability all agree; a live executor swap must lift it immediately.
func (c *Controller) settleSkipShortCircuits(day string) bool {
	if _, ok := c.execRef().(SettlementFetcher); ok {
		return false // 现在就支持：旧戳一律不作短路（换了执行器就得给它当场验证的机会）
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSettleSkipDay == day && c.lastSettleSkipOutcome == SettleOutcomeSkippedNoFetcher
}

// recordSettleSkip §P2-E：记下"这一日因哪个原因被跳过"（当日只留一次痕的判据就靠它）。
// 无论哪个原因都覆写戳与原因：后到的新原因必须取代旧原因，否则先发生的 NoFetcher 会把
// 之后真能跑的窗口一起挡掉（短路判据一旦只看日期就变成单向记忆）。
// English: §P2-E — stamps the day with the skip reason so repeat structural skips log once,
// while any newer outcome (including a real verification) overwrites the stamp.
func (c *Controller) recordSettleSkip(day string, outcome SettleOutcome) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSettleSkipDay = day
	c.lastSettleSkipOutcome = outcome
}
