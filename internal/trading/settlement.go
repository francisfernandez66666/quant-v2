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
	Key    string // FillKey（serial 优先，缺则 order_id+traded_at+side）
	Code   string
	Side   string
	Price  float64
	Qty    int
	Fee    float64
	Serial string
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

	// 归一券商成交（side 统一为 买入/卖出）
	broker := map[string]brokerTrade{}
	var brokerFeeTotal float64
	for _, t := range resp.Trades {
		side := normalizeSide(t.Side)
		if side == "" {
			continue // 无法归一：跳过（记录在 diff）
		}
		bt := brokerTrade{
			Code: t.TsCode, Side: side, Price: t.Price, Qty: t.Qty,
			Fee: t.Fee + t.StampTax, Serial: t.Serial,
		}
		key := bt.CorrKey()
		broker[key] = bt
		brokerFeeTotal += bt.Fee
	}

	// 本地成交
	localFills, err := c.store.ListFillsByDay(c.userID, day)
	if err != nil {
		return nil, err
	}
	local := map[string]store.RealFill{}
	var localFeeTotal float64
	for _, f := range localFills {
		local[store.FillKey(f)] = f
		localFeeTotal += f.Fee + f.StampTax
	}

	diff := &store.SettlementDiff{
		Day: day, Mode: mode,
		LocalFeeTotal:  localFeeTotal,
		BrokerFeeTotal: brokerFeeTotal,
		FeeDiff:        brokerFeeTotal - localFeeTotal,
	}
	for key, bt := range broker {
		if lf, ok := local[key]; ok {
			// 同键存在：校验 量/价 一致
			if lf.Qty != bt.Qty || abs(lf.Price-bt.Price) > 0.001 {
				diff.Mismatch = append(diff.Mismatch, fmt.Sprintf("%s %s 量价不符 本地(%d@%.2f) vs 券商(%d@%.2f)",
					key, bt.Code, lf.Qty, lf.Price, bt.Qty, bt.Price))
			}
		} else {
			diff.MissingInLocal = append(diff.MissingInLocal, fmt.Sprintf("%s %s %d@%.2f",
				key, bt.Code, bt.Qty, bt.Price))
		}
	}
	for key, lf := range local {
		if _, ok := broker[key]; !ok {
			diff.ExtraInLocal = append(diff.ExtraInLocal, fmt.Sprintf("%s %s %d@%.2f",
				key, lf.Code, lf.Qty, lf.Price))
		}
	}

	// 现金差（券商交割口径 vs 本地 real_account）
	if acc, aerr := c.store.GetRealAccount(c.userID); aerr == nil && acc.AvailableCash > 0 {
		if brokerCash := resp.Cash["cash"]; brokerCash > 0 {
			diff.CashDiff = brokerCash - acc.AvailableCash
		}
	}

	// 纠偏
	if mode == SettleModeSyncFills && len(diff.MissingInLocal) > 0 {
		for key, bt := range broker {
			if _, ok := local[key]; ok {
				continue
			}
			fill := store.RealFill{
				OrderID: "", Code: bt.Code, Side: bt.Side, Price: bt.Price, Qty: bt.Qty,
				Amount: bt.Price * float64(bt.Qty), TradedAt: day + " 00:00:00",
				SignalID: "settle:" + day, UserID: c.userID, Fee: bt.Fee, Serial: bt.Serial,
			}
			if err := c.store.ApplySettlementFill(fill); err != nil {
				log.Printf("[settle] sync_fills 补记失败 %s: %v", key, err)
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

// CorrKey 券商成交的关联键（复用 store.FillKey 口径：serial 优先）。
// English: correlation key for a broker trade (mirrors store.FillKey: serial first).
func (bt brokerTrade) CorrKey() string {
	return store.FillKey(store.RealFill{Serial: bt.Serial, OrderID: "", TradedAt: "", Side: bt.Side})
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
