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
// 返回差异摘要；gateway 不支持交割单时返回 (nil, nil)（静默跳过，不误告警）。
// English: runs three-way settlement for a day; returns the diff summary. When the gateway lacks
// settlement support it returns (nil, nil) silently (no false alarms).
func (c *Controller) SettleDay(day, mode string) (*store.SettlementDiff, error) {
	day = normalizeSettleDay(day) // §H1 口径归一先于一切（落库键/拉取参数/补记时间戳共用）
	if c.store == nil {
		return nil, fmt.Errorf("real book not set")
	}
	f, ok := c.execRef().(SettlementFetcher)
	if !ok {
		log.Printf("[settle] 当前执行器不支持交割单（Noop/桩），跳过 %s", day)
		// 不适用 = 0（§DEADGAUGE 统一口径：未知/不适用一律写 0，既不伪造"有差异"也不留残值）。
		metrics.SetGauge("settlement_diff_count", 0)
		return nil, nil
	}
	if mode == "" {
		mode = SettleModeReportOnly
	}
	resp, err := f.FetchSettlement(day)
	if err != nil {
		return nil, fmt.Errorf("fetch settlement %s: %w", day, err)
	}
	if !resp.Connected {
		log.Printf("[settle] 网关未连接，交割单不可信，跳过 %s", day)
		// 同上：本轮不可判 → 写 0 不适用，不把"没对成"伪装成"对出差"，也不留上一轮的残值。
		metrics.SetGauge("settlement_diff_count", 0)
		return nil, nil
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
		}
		k := settleFactKey(bt.Code, bt.Side, bt.Qty, bt.Price)
		broker[k] = append(broker[k], bt)
		brokerFeeTotal += bt.Fee
	}

	// 本地成交（同样按物理事实键分桶）
	localFills, err := c.store.ListFillsByDay(c.userID, day)
	if err != nil {
		return nil, err
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
			fill := store.RealFill{
				OrderID: bt.OrderID, Code: bt.Code, Side: bt.Side, Price: bt.Price, Qty: bt.Qty,
				Amount: bt.Price * float64(bt.Qty), TradedAt: tradedAt,
				SignalID: "settle:" + day, UserID: c.userID, Fee: bt.Fee, Serial: bt.Serial,
			}
			if err := c.store.ApplySettlementFill(fill); err != nil {
				log.Printf("[settle] sync_fills 补记失败 %s: %v", m.k, err)
			}
		}
	}

	if err := c.store.SaveSettlementDiff(c.userID, *diff); err != nil {
		return nil, err
	}
	// §DEADGAUGE（2026-09-23 傍晚批收尾）：告警规则 settlement_diff（量规 settlement_diff_count，
	// p1「交割单对账出现差异」）自 09-15 注册以来全仓无赋值点 = 永不触发的死规则（audit N-1 同族，
	// 那次是 settle_fail_streak）。差异条数在这里第一次成为已知量，且本函数同时服务定时链
	// （MaybeSettleDay）与手工链（POST /api/qmt/settle），在此赋值即两条链同源覆盖。
	// 注意取「三类条数之和」而非 diff 是否为 nil：sync_fills 补记后会清空 missing，但多余/不符
	// 仍需按当日实况告警，故必须在落库/日志口径之后统计。
	metrics.SetGauge("settlement_diff_count", int64(len(diff.MissingInLocal)+len(diff.ExtraInLocal)+len(diff.Mismatch)))
	log.Printf("[settle] %s 三方对账完成: 缺失=%d 多余=%d 不符=%d 费用差=%.2f 现金差=%.2f (mode=%s)",
		day, len(diff.MissingInLocal), len(diff.ExtraInLocal), len(diff.Mismatch),
		diff.FeeDiff, diff.CashDiff, mode)
	return diff, nil
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
	// 尝试戳先置位（持锁写）：这是防死循环的唯一护栏——成功与否都不回滚它，只回滚"当日已完成"标记。
	c.mu.Lock()
	c.lastSettleAttemptAt = time.Now()
	c.mu.Unlock()
	diff, err := c.SettleDay(day, mode)
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
	// 成功（含 SettleDay 返回 (nil,nil) 的"网关不支持/未连接，静默跳过"分支）：
	// 当日记账完成，后续窗口不再重投。
	c.mu.Lock()
	c.lastSettleDay = day
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
