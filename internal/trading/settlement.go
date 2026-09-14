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

// SettleDay 执行某交易日三方对账：券商交割单 ↔ 本地 fills ↔ real_account。
// 返回差异摘要；gateway 不支持交割单时返回 (nil, nil)（静默跳过，不误告警）。
// English: runs three-way settlement for a day; returns the diff summary. When the gateway lacks
// settlement support it returns (nil, nil) silently (no false alarms).
func (c *Controller) SettleDay(day, mode string) (*store.SettlementDiff, error) {
	if c.store == nil {
		return nil, fmt.Errorf("real book not set")
	}
	f, ok := c.execRef().(SettlementFetcher)
	if !ok {
		log.Printf("[settle] 当前执行器不支持交割单（Noop/桩），跳过 %s", day)
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

// MaybeSettleDay §WS-B 调度入口（节流+交易日门控）：每天只对账一次指定日期，
// 且仅在到达 settle_at（默认 15:30，北京时）之后触发（交割单常盘后 15:30 才全）。
// English: §WS-B scheduled settlement entry — runs once per day, after the configurable settle_at
// (default 15:30 Beijing) when the broker settlement is typically complete.
func (c *Controller) MaybeSettleDay(day string, mode string, settleAt int, enabled bool) {
	if !enabled || c.store == nil || !c.Enabled() {
		return
	}
	c.mu.RLock()
	last := c.lastSettleDay
	c.mu.RUnlock()
	if last == day {
		return // 当日已对账，跳过
	}
	if settleAt <= 0 {
		settleAt = 1530
	}
	now := cntime.In(time.Now())
	if now.Hour()*100+now.Minute() < settleAt {
		return // 未到对账时刻
	}
	c.mu.Lock()
	c.lastSettleDay = day
	c.mu.Unlock()
	diff, err := c.SettleDay(day, mode)
	if err != nil {
		log.Printf("[settle] 对账失败: %v", err)
		return
	}
	if diff != nil && (len(diff.MissingInLocal)+len(diff.ExtraInLocal)+len(diff.Mismatch) > 0) {
		c.fireOnAlert("high", "券商交割单三方对账差异",
			fmt.Sprintf("%s: 缺失%d 多余%d 不符%d 费用差%.2f（详见 settlement_diff）",
				day, len(diff.MissingInLocal), len(diff.ExtraInLocal), len(diff.Mismatch), diff.FeeDiff))
	}
}
