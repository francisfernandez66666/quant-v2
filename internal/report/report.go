// Package report 提供交易持仓报告的管理功能，包括开仓/平仓记录、持仓查询、
// 统计汇总、持久化读写（JSON 文件）等核心操作。所有写操作均受读写锁保护以支持并发安全。
// （Package report manages trading position reports: open/close records, position queries, statistics and
// JSON file persistence. All writes are guarded by a read-write lock for concurrency safety.）
package report

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"quant-trading-v2/internal/fileutil"
)

// Lot 表示一次加仓批次记录：开仓/加仓的价格、数量与时间。
// 汇总这些批次即得到累计持仓数量与加权平均成本。
// （Lot is a single add/open lot record: price, quantity and time. Summing lots yields total quantity and weighted average cost.）
type Lot struct {
	Price    float64   `json:"price"`    // 加仓/开仓价格
	Quantity float64   `json:"quantity"` // 加仓/开仓数量
	At       time.Time `json:"at"`       // 加仓/开仓时间
}

// ExecLog 表示一条交易执行记录，对应一次完整的开仓→持仓→平仓生命周期。
// Status 字段取值包括："持仓中"、"已止盈"、"已止损"、"已删除"。
// （ExecLog is one execution record covering the open→hold→close lifecycle. Status ∈ {持仓中, 已止盈, 已止损, 已删除}.）
type ExecLog struct {
	SignalID      string             `json:"signal_id"`               // 信号唯一标识
	UserID        string             `json:"user_id,omitempty"`       // 所属账号 ID（空=系统/全局，引擎监控全部）
	Code          string             `json:"code"`                    // 股票代码（纯数字，无交易所前缀）
	Name          string             `json:"name"`                    // 股票名称
	Direction     string             `json:"direction"`               // 交易方向：做多 / 做空
	Strategy      string             `json:"strategy"`                // 触发入场信号的战法名称
	EntryAt       time.Time          `json:"entry_at"`                // 开仓时间
	EntryPrice    float64            `json:"entry_price"`             // 开仓价格
	ExitAt        *time.Time         `json:"exit_at,omitempty"`       // 平仓时间（nil 表示尚未平仓）
	ExitPrice     *float64           `json:"exit_price,omitempty"`    // 平仓价格（nil 表示尚未平仓）
	Status        string             `json:"status"`                  // 记录状态：持仓中 / 已止盈 / 已止损 / 已删除
	ProfitPct     *float64           `json:"profit_pct,omitempty"`    // 盈亏百分比（正值为盈利，负值为亏损，nil 表示尚未平仓）
	TakeProfitPct float64            `json:"take_profit_pct"`         // 预设止盈百分比
	StopLossPct   float64            `json:"stop_loss_pct"`           // 预设止损百分比
	Quantity      float64            `json:"quantity"`                // 持仓数量（手动设置，默认 1）
	Lots          []Lot              `json:"lots,omitempty"`          // 加仓批次明细（加权平均成本 = EntryPrice，数量 = Quantity）
	RealizedPnl   float64            `json:"realized_pnl,omitempty"`  // 该标的累计已实现盈亏（元，含已平仓历史；减仓/清仓时累加）
	EntryMeta     map[string]float64 `json:"entry_meta,omitempty"`    // 入场评分快照（entry_nphase/vol_ratio/limit_price/highest_price 等，供战法退出引擎使用）
	HighestPrice  float64            `json:"highest_price,omitempty"` // 阶段最高价（移动止盈基准；开仓时=入场价，盘中实时抬高）
	ExitReason    string             `json:"exit_reason,omitempty"`   // 卖出原因（如 手动/止损/移动止盈/尾盘强平，供消息与复盘）
}

// Report 管理所有交易持仓记录，提供线程安全的增删改查与文件持久化能力。
// path 指定 JSON 持久化文件路径，logs 在内存中维护完整记录列表。
// （Report manages all position records with thread-safe CRUD and file persistence.）
type Report struct {
	mu   sync.RWMutex // 读写锁，保证并发安全
	logs []ExecLog    // 内存中的全部交易记录
	path string       // JSON 持久化文件路径
}

// New 创建 Report 实例并加载指定路径的持久化数据（若存在）。
// path 为空字符串时可创建一个仅内存操作的 Report（不进行读写持久化）。
// （New creates a Report and loads persisted data from path when present; an empty path yields an in-memory-only report.）
func New(path string) *Report {
	r := &Report{
		logs: make([]ExecLog, 0),
		path: path,
	}
	r.Load()
	return r
}

// NewFromLogs 用给定持仓记录创建一个仅内存的 Report（不持久化、不读文件）。
// 供实盘账本（real_positions）构造只读视图，复用卖出侧决策函数（CheckPositionsExits 等）。
// English: NewFromLogs builds an in-memory-only Report from the given position records (no persistence,
// no file I/O). Used to construct a read-only view of the real book (real_positions) so the sell-side
// decision functions (CheckPositionsExits etc.) can be reused unchanged.
func NewFromLogs(logs []ExecLog) *Report {
	r := &Report{logs: logs}
	return r
}

// LogSignal 记录一条新的开仓信号，将 ExecLog 追加到日志列表末尾。
// 参数依次为：id-信号ID, code-股票代码, name-股票名称, direction-交易方向,
// strategy-战法名称, entryPrice-开仓价格, takeProfitPct-止盈百分比, stopLossPct-止损百分比。
// 记录后自动持久化到文件。
// 入场评分快照默认为 {highest_price: 入场价}（阶段最高价的初始值，供移动止盈使用）。
// （LogSignal appends a new open-position ExecLog and persists it automatically. The entry-meta
// snapshot defaults to {highest_price: entry price} as the initial stage high for trailing stops.）
func (r *Report) LogSignal(id, code, name, direction, strategy string, entryPrice, takeProfitPct, stopLossPct float64) {
	r.LogSignalWithMeta(id, code, name, direction, strategy, entryPrice, takeProfitPct, stopLossPct, nil)
}

// LogSignalWithMeta 在 LogSignal 基础上支持附加入场评分快照（EntryMeta）。
// 未显式提供 highest_price 时自动补为入场价，保证移动止盈基准始终存在。
// （LogSignalWithMeta extends LogSignal with an entry-meta snapshot; it fills in highest_price as the
// entry price when absent so the trailing-stop baseline always exists.）
func (r *Report) LogSignalWithMeta(id, code, name, direction, strategy string, entryPrice, takeProfitPct, stopLossPct float64, meta map[string]float64) {
	entryMeta := make(map[string]float64, len(meta)+1)
	for k, v := range meta {
		entryMeta[k] = v
	}
	if _, ok := entryMeta["highest_price"]; !ok {
		entryMeta["highest_price"] = entryPrice
	}
	highest := entryPrice
	if h, ok := entryMeta["highest_price"]; ok && h > 0 {
		highest = h
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, ExecLog{
		SignalID:      id,
		Code:          code,
		Name:          name,
		Direction:     direction,
		Strategy:      strategy,
		EntryAt:       time.Now(),
		EntryPrice:    entryPrice,
		Status:        "持仓中",
		TakeProfitPct: takeProfitPct,
		StopLossPct:   stopLossPct,
		EntryMeta:     entryMeta,
		HighestPrice:  highest,
	})
	r.save()
	log.Printf("[report] 开仓记录: %s %s %s %.2f", strategy, code, name, entryPrice)
}

// LogSignalWithMetaQty 在 LogSignalWithMeta 基础上支持指定开仓数量（C6 仓位管理）。
// qty<=0 时使用默认 1。其余行为与 LogSignalWithMeta 一致。
// English: extends LogSignalWithMeta with an explicit opening quantity (C6 position sizing); qty<=0
// falls back to 1. Otherwise identical.
func (r *Report) LogSignalWithMetaQty(id, code, name, direction, strategy string, entryPrice, takeProfitPct, stopLossPct float64, qty float64, meta map[string]float64) {
	r.LogSignalWithMeta(id, code, name, direction, strategy, entryPrice, takeProfitPct, stopLossPct, meta)
	if qty <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.logs {
		if r.logs[i].SignalID == id {
			r.logs[i].Quantity = qty
			break
		}
	}
	r.save()
}

// LogExit 根据 signalID 平仓。计算盈亏百分比（(exitPrice - entryPrice) / entryPrice * 100），
// 并据此标记状态为"已止盈"（pct > 0）或"已止损"（pct <= 0）。
// 可选参数 reason 记录卖出原因（如 手动/止损/移动止盈/尾盘强平）。
// 若找不到匹配的持仓记录（或该记录已平仓），则静默返回。
// （LogExit closes a position by signalID, computes the P&L percentage and marks it 已止盈/已止损; an
// optional reason is recorded as ExitReason; silently returns when not found.）
func (r *Report) LogExit(signalID string, exitPrice float64, reason ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for i := range r.logs {
		if r.logs[i].SignalID == signalID && r.logs[i].ExitAt == nil {
			qty := r.logs[i].Quantity
			if qty <= 0 {
				qty = 1
			}
			pct := (exitPrice - r.logs[i].EntryPrice) / r.logs[i].EntryPrice * 100
			// 平仓时把 (清仓价-均价)×数量 计入该标的已实现盈亏；
			// SellLot 全卖路径已在清零数量前累计过，此处 Quantity=0 时跳过避免重复。
			if r.logs[i].Quantity > 0 {
				r.logs[i].RealizedPnl += (exitPrice - r.logs[i].EntryPrice) * qty
			}
			r.logs[i].ExitAt = &now
			r.logs[i].ExitPrice = &exitPrice
			r.logs[i].ProfitPct = &pct
			if len(reason) > 0 && reason[0] != "" {
				r.logs[i].ExitReason = reason[0]
			}
			if pct > 0 {
				r.logs[i].Status = "已止盈"
			} else {
				r.logs[i].Status = "已止损"
			}
			r.save()
			log.Printf("[report] 平仓记录: %s 盈亏%.2f%% 原因=%s", signalID, pct, r.logs[i].ExitReason)
			return
		}
	}
}

// RaiseHighest 抬高某持仓的阶段最高价：仅当 price 高于当前记录值且持仓未平仓时更新并持久化。
// 返回是否发生抬高（false 表示未找到记录、已平仓或价格未创新高）。用于移动止盈基准的实时追踪。
// （RaiseHighest raises a position's stage high to price only when it is a new high and the position is
// still open, persisting the change; returns whether the high actually rose.）
func (r *Report) RaiseHighest(id string, price float64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.logs {
		if r.logs[i].SignalID == id && r.logs[i].ExitAt == nil && price > r.logs[i].HighestPrice {
			r.logs[i].HighestPrice = price
			r.save()
			return true
		}
	}
	return false
}

// Update 根据信号 ID 查找对应的 ExecLog，并在持有写锁的情况下执行用户自定义修改函数 fn。
// 常用于修改止损价、止盈价等字段。修改后自动持久化。
// （Update locates an ExecLog by signal ID and applies fn under the write lock, then persists.）
func (r *Report) Update(id string, fn func(*ExecLog)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.logs {
		if r.logs[i].SignalID == id {
			fn(&r.logs[i])
			break
		}
	}
	r.save()
}

// AddLot 对指定 ID 的持仓追加一笔加仓批次，并重算加权平均成本与累计数量。
// 加权成本 = Σ(单笔价格×数量) / 总数量；数量 = Σ各批次数量。
// 找不到记录时静默返回。修改后自动持久化。
// （AddLot appends an add lot to a position and recomputes the weighted average cost and total quantity.）
func (r *Report) AddLot(id string, price, qty float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.logs {
		if r.logs[i].SignalID == id {
			if price > 0 && qty > 0 {
				r.logs[i].Lots = append(r.logs[i].Lots, Lot{Price: price, Quantity: qty, At: time.Now()})
			}
			normalizeLots(&r.logs[i])
			break
		}
	}
	r.save()
}

// SellLot 对指定 ID 的持仓按 FIFO 顺序卖出 qty 股，扣减批次明细并重算加权平均成本。
// 卖出的数量在批次中按先后顺序抵扣：先卖最早买入的批次，批次数量不足以扣减时继续扣后续批次。
// 扣减后剩余数量 = 原数量 - qty；若剩余数量 <= 0 则视为全部卖出，自动平仓（记录盈亏）。
// 卖出价用于持仓减到 0 时的平仓盈亏计算。调用方负责校验 qty <= 当前数量。修改后自动持久化。
// （SellLot sells qty shares FIFO against the lots, recomputing weighted cost; selling all auto-closes the
// position. Caller must ensure qty <= current quantity.）
func (r *Report) SellLot(id string, price, qty float64) {
	r.mu.Lock()
	fullClose := false
	for i := range r.logs {
		if r.logs[i].SignalID != id || r.logs[i].ExitAt != nil {
			continue
		}
		l := &r.logs[i]
		cur := l.Quantity
		if cur <= 0 {
			cur = 1
		}
		if qty >= cur {
			// 全部卖出：先扣光批次数量再平仓，避免残留零数量批次；
			// 已实现盈亏按 (卖出价-均价)×当前持仓数 计入（在清零数量前累计）。
			l.RealizedPnl += (price - l.EntryPrice) * cur
			l.Lots = nil
			l.Quantity = 0
			fullClose = true
			break
		}
		// 部分卖出：FIFO 扣减批次数量
		remain := qty
		newLots := make([]Lot, 0, len(l.Lots))
		for _, lot := range l.Lots {
			if remain <= 0 {
				newLots = append(newLots, lot)
				continue
			}
			if lot.Quantity > remain {
				newLots = append(newLots, Lot{Price: lot.Price, Quantity: lot.Quantity - remain, At: lot.At})
				// 被卖出的 remain 股按该批次成本记账：已实现盈亏=(卖出价-批次成本)×数量
				l.RealizedPnl += (price - lot.Price) * remain
				remain = 0
			} else {
				// 整批卖出：全批计入已实现盈亏
				l.RealizedPnl += (price - lot.Price) * lot.Quantity
				remain -= lot.Quantity
			}
		}
		l.Lots = newLots
		normalizeLots(l)
		r.save()
		log.Printf("[report] 减仓记录: %s 价%.3f 量%.0f -> 余%.0f 成本%.3f", id, price, qty, l.Quantity, l.EntryPrice)
		fullClose = false
		break
	}
	r.mu.Unlock()

	// 全部卖完：在锁外平仓（LogExit 内部会再加锁），记录真实盈亏
	if fullClose {
		r.LogExit(id, price)
	}
}

// SetCostBasis 更新指定 ID 持仓的成本价（改成本）：直接设置 EntryPrice，
// 并把批次明细重建为一条合成批次，使其与显示的成本/数量保持一致。
// 找不到记录时静默返回。修改后自动持久化。
// （SetCostBasis sets a position's cost price and rebuilds lots into a single synthetic lot to match display.）
func (r *Report) SetCostBasis(id string, price float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.logs {
		if r.logs[i].SignalID == id {
			if price > 0 && r.logs[i].Quantity > 0 {
				r.logs[i].EntryPrice = price
				r.logs[i].Lots = []Lot{{Price: price, Quantity: r.logs[i].Quantity, At: time.Now()}}
			}
			break
		}
	}
	r.save()
}

// normalizeLots 依据批次明细重算 EntryPrice（加权平均成本）与 Quantity（累计数量）。
// 无批次明细时保持现有 EntryPrice/Quantity 不变（兼容旧的持久化数据）。
// （normalizeLots recomputes EntryPrice and Quantity from the lots; without lots it keeps current values for legacy data.）
func normalizeLots(l *ExecLog) {
	amt, qty := 0.0, 0.0
	for _, lot := range l.Lots {
		amt += lot.Price * lot.Quantity
		qty += lot.Quantity
	}
	if qty > 0 {
		l.EntryPrice = amt / qty
		l.Quantity = qty
	}
}

// Delete 对指定 ID 的记录执行软删除——将 Status 标记为"已删除"而非物理移除。
// 已删除记录仍会保留在列表和持久化文件中，用于后续审计和统计。
// （Delete soft-deletes a record by marking Status 已删除 instead of removing it, keeping it for audit/statistics.）
func (r *Report) Delete(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.logs {
		if r.logs[i].SignalID == id {
			r.logs[i].Status = "已删除"
			break
		}
	}
	r.save()
}

// List 返回所有 ExecLog 记录的副本，避免调用方直接修改内部切片。
// 读操作加读锁以确保并发安全。
// （List returns a copy of all ExecLog records so callers cannot mutate the internal slice.）
func (r *Report) List() []ExecLog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ExecLog, len(r.logs))
	copy(out, r.logs)
	return out
}

// ListFor 返回指定账号（userID）的交易记录副本；空 userID 返回全部（系统/跨账号视角）。
// 用于多账号隔离：各账号只看到自己创建的持仓。
// （ListFor returns copies of records owned by userID; empty userID returns all.）
func (r *Report) ListFor(userID string) []ExecLog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if userID == "" {
		out := make([]ExecLog, len(r.logs))
		copy(out, r.logs)
		return out
	}
	var out []ExecLog
	for _, l := range r.logs {
		if l.UserID == userID {
			out = append(out, l)
		}
	}
	return out
}

// HeldPositionsFor 返回指定账号状态为"持仓中"的完整 ExecLog 记录列表。
// （HeldPositionsFor returns currently-held position records owned by userID.）
func (r *Report) HeldPositionsFor(userID string) []ExecLog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ExecLog
	for _, l := range r.logs {
		if l.Status == "持仓中" && (userID == "" || l.UserID == userID) {
			out = append(out, l)
		}
	}
	return out
}

// HeldPositionCodes 返回当前所有状态为"持仓中"的股票代码（已去重）。
// 用于向策略引擎提供当前持仓信息，影响打分池构建和风险控制决策。
// （HeldPositionCodes returns deduplicated codes of all currently held positions, feeding strategy scoring and risk control.）
func (r *Report) HeldPositionCodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var codes []string
	for _, l := range r.logs {
		if l.Status == "持仓中" && !seen[l.Code] {
			seen[l.Code] = true
			codes = append(codes, l.Code)
		}
	}
	return codes
}

// HasHolding 判断某代码当前是否有持仓中记录（含其他账号？不含，仅本报告实例）。
// English: reports whether a code currently has a held position in this report.
func (r *Report) HasHolding(code string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.logs {
		if l.Code == code && l.Status == "持仓中" {
			return true
		}
	}
	return false
}

// HeldPositions 返回当前所有状态为"持仓中"的完整 ExecLog 记录列表。
// 用于显示模块展示当前持仓详情。
// （HeldPositions returns the full records of all currently held positions for display.）
func (r *Report) HeldPositions() []ExecLog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ExecLog
	for _, l := range r.logs {
		if l.Status == "持仓中" {
			out = append(out, l)
		}
	}
	return out
}

// FindBySignalID 根据信号 ID 查询单条 ExecLog，返回其值副本的指针。
// 若未找到匹配记录则返回 nil。
// （FindBySignalID looks up a single ExecLog by signal ID, returning a pointer to a value copy, or nil.）
func (r *Report) FindBySignalID(id string) *ExecLog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.logs {
		if l.SignalID == id {
			return &l
		}
	}
	return nil
}

// TotalRealizedPnl 返回全部交易记录（含已平仓）累计已实现盈亏（元）。
// userID 为空时统计全部账号；否则仅统计指定账号。
// （TotalRealizedPnl returns the cumulative realized P&L in yuan across all records
// including closed ones. Empty userID means all accounts.）
func (r *Report) TotalRealizedPnl(userID string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var sum float64
	for _, l := range r.logs {
		if userID != "" && l.UserID != userID {
			continue
		}
		sum += l.RealizedPnl
	}
	return sum
}

// RealizedPnlFor 返回指定持仓记录（signalID）的累计已实现盈亏（元）。
// （RealizedPnlFor returns a position's cumulative realized P&L in yuan.）
func (r *Report) RealizedPnlFor(signalID string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.logs {
		if l.SignalID == signalID {
			return l.RealizedPnl
		}
	}
	return 0
}

// Stats 计算并返回交易统计指标：
//
//	total   - 总交易笔数（不含已删除记录）
//	holding - 当前持仓数（状态为"持仓中"）
//	win     - 盈利笔数（已平仓且 ProfitPct > 0）
//	winRate - 胜率百分比 = win / (win + loss) * 100
//	avgWin  - 平均盈利百分比
//	avgLoss - 平均亏损百分比（负值）
//
// （Stats computes trading statistics: total trades, positions held, wins, win rate, average win/loss percentages.）
func (r *Report) Stats() (total, holding, win int, winRate, avgWin, avgLoss float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.statsFor("")
}

// StatsFor 返回指定账号的交易统计指标；空 userID 返回全部。
// 与 Stats 相同指标，仅按所属账号过滤记录。
// （StatsFor computes trading statistics scoped to a user; empty userID means all.）
func (r *Report) StatsFor(userID string) (total, holding, win int, winRate, avgWin, avgLoss float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.statsFor(userID)
}

// StrategyStats 表示按战法分组的交易统计指标。
// 用于评估每个战法（Dragon/DoubleBump/NShape/DragonReturn 等）各自的胜率与盈亏表现，
// 支撑"按战法归因"的绩效分析。ProfitFactor 为盈亏比（总盈利/总亏损绝对值），
// 无亏损记录时为 0（避免除零，由调用方决定展示口径）。
type StrategyStats struct {
	Strategy     string  // 战法名称（对应 ExecLog.Strategy）
	Total        int     // 计入统计的总记录数（不含已删除）
	Closed       int     // 已平仓笔数（含赢/亏/平手）
	Win          int     // 盈利笔数
	Loss         int     // 亏损笔数
	Draw         int     // 平手笔数（盈亏为 0）
	Holding      int     // 仍在持仓中的笔数
	WinRate      float64 // 胜率百分比（Win / (Win+Loss) * 100）
	AvgWinPct    float64 // 平均盈利百分比（正数）
	AvgLossPct   float64 // 平均亏损百分比（负数）
	ProfitFactor float64 // 盈亏比（总盈利 / 总亏损绝对值），无亏损时为 0
}

// StatsByStrategy 返回按战法分组的交易统计指标（胜率/平均盈/平均亏/盈亏比/样本数）。
// userID 为空时统计全部账号记录，否则仅统计指定账号。
// 该口径与 statsFor 保持一致：已删除记录不计入、盈亏为 0 算平手。
func (r *Report) StatsByStrategy(userID string) map[string]StrategyStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// 聚合缓冲：按战法名累积各计数与盈亏和
	type agg struct {
		winCount, lossCount, drawCount int
		winSum, lossSum                float64
		holding                        int
	}
	aggs := make(map[string]*agg)
	for _, l := range r.logs {
		if userID != "" && l.UserID != userID {
			continue
		}
		if l.Status == "已删除" {
			continue // 已删除记录不计入统计口径
		}
		strategy := l.Strategy
		if strategy == "" {
			strategy = "未命名" // 兜底：缺省战法名的记录归到"未命名"分组
		}
		a, ok := aggs[strategy]
		if !ok {
			a = &agg{}
			aggs[strategy] = a
		}
		if l.ExitAt != nil && l.ProfitPct != nil {
			switch {
			case *l.ProfitPct > 0:
				a.winCount++
				a.winSum += *l.ProfitPct
			case *l.ProfitPct == 0:
				a.drawCount++
			default:
				a.lossCount++
				a.lossSum += *l.ProfitPct
			}
		} else if l.Status == "持仓中" {
			a.holding++
		}
	}
	// 组装输出
	out := make(map[string]StrategyStats, len(aggs))
	for strategy, a := range aggs {
		ss := StrategyStats{
			Strategy: strategy,
			Total:    a.winCount + a.lossCount + a.drawCount + a.holding,
			Closed:   a.winCount + a.lossCount + a.drawCount,
			Win:      a.winCount,
			Loss:     a.lossCount,
			Draw:     a.drawCount,
			Holding:  a.holding,
		}
		if a.winCount+a.lossCount > 0 {
			ss.WinRate = float64(a.winCount) / float64(a.winCount+a.lossCount) * 100
		}
		if a.winCount > 0 {
			ss.AvgWinPct = a.winSum / float64(a.winCount)
		}
		if a.lossCount > 0 {
			ss.AvgLossPct = a.lossSum / float64(a.lossCount)
		}
		if a.lossSum != 0 {
			ss.ProfitFactor = a.winSum / -a.lossSum
		}
		out[strategy] = ss
	}
	return out
}

// statsFor 内部统计实现：userID 为空时统计全部记录。
// 口径说明：已删除记录不计入任何统计；盈亏为 0 的按"平手"处理（计入总笔数，
// 但既不算赢也不算亏，不进胜率分母）；平均盈/亏为百分比（正/负）。
func (r *Report) statsFor(userID string) (total, holding, win int, winRate, avgWin, avgLoss float64) {
	var winCount, lossCount int
	var winSum, lossSum float64
	for _, l := range r.logs {
		if userID != "" && l.UserID != userID {
			continue
		}
		if l.Status == "已删除" {
			continue // 已删除记录不计入统计口径
		}
		total++
		if l.ExitAt != nil && l.ProfitPct != nil {
			switch {
			case *l.ProfitPct > 0:
				winCount++
				winSum += *l.ProfitPct
			case *l.ProfitPct == 0:
				// 平手：既不算赢也不算亏，不影响胜率分母
			default:
				lossCount++
				lossSum += *l.ProfitPct
			}
		} else if l.Status == "持仓中" {
			holding++
		}
	}
	if winCount+lossCount > 0 {
		winRate = float64(winCount) / float64(winCount+lossCount) * 100
	}
	if winCount > 0 {
		avgWin = winSum / float64(winCount)
	}
	if lossCount > 0 {
		avgLoss = lossSum / float64(lossCount)
	}
	win = winCount
	return
}

// save 将当前 logs 以 JSON 格式写入持久化文件（路径由 r.path 指定）。
// 若 path 为空字符串则跳过写入。写入失败时仅记录日志，不阻塞程序运行。
// §R3-4 P1-A 原子写：此前 os.WriteFile 直接覆盖——进程在写盘中途崩溃会截断整个持仓账本
// （positions.json 损坏=全部历史丢失）。改走 fileutil.AtomicWrite（唯一临时名+fsync+rename），
// 与 paper/auth/config 同一口径。English: R3-4 P1-A — atomic write via fileutil so a crash
// mid-write can no longer truncate the position ledger.
func (r *Report) save() {
	if r.path == "" {
		return
	}
	data, err := json.MarshalIndent(r.logs, "", "  ")
	if err != nil {
		log.Printf("[report] 序列化失败: %v", err)
		return
	}
	if err := fileutil.AtomicWrite(r.path, data, 0644); err != nil {
		log.Printf("[report] 写入失败: %v", err)
	}
}

// Load 从持久化文件读取 JSON 数据并解析到 r.logs 中。
// 若文件不存在则静默返回（空列表），解析失败时记录错误日志。
// 此方法在 Report.New 中自动调用，用于启动时恢复历史记录。
// （Load reads the JSON file into r.logs, silently ignoring a missing file and logging parse errors. Called by New on startup.）
func (r *Report) Load() {
	if r.path == "" {
		return
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(data, &r.logs); err != nil {
		log.Printf("[report] 解析失败: %v", err)
		return
	}
	log.Printf("[report] 已加载 %d 条持仓记录", len(r.logs))
}
