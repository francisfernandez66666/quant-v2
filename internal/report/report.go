// Package report 提供交易持仓报告的管理功能，包括开仓/平仓记录、持仓查询、
// 统计汇总、持久化读写（JSON 文件）等核心操作。所有写操作均受读写锁保护以支持并发安全。
package report

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// ExecLog 表示一条交易执行记录，对应一次完整的开仓→持仓→平仓生命周期。
// Status 字段取值包括："持仓中"、"已止盈"、"已止损"、"已删除"。
type ExecLog struct {
	SignalID      string     `json:"signal_id"`            // 信号唯一标识
	Code          string     `json:"code"`                 // 股票代码（纯数字，无交易所前缀）
	Name          string     `json:"name"`                 // 股票名称
	Direction     string     `json:"direction"`            // 交易方向：做多 / 做空
	Strategy      string     `json:"strategy"`             // 触发入场信号的战法名称
	EntryAt       time.Time  `json:"entry_at"`             // 开仓时间
	EntryPrice    float64    `json:"entry_price"`          // 开仓价格
	ExitAt        *time.Time `json:"exit_at,omitempty"`    // 平仓时间（nil 表示尚未平仓）
	ExitPrice     *float64   `json:"exit_price,omitempty"` // 平仓价格（nil 表示尚未平仓）
	Status        string     `json:"status"`               // 记录状态：持仓中 / 已止盈 / 已止损 / 已删除
	ProfitPct     *float64   `json:"profit_pct,omitempty"` // 盈亏百分比（正值为盈利，负值为亏损，nil 表示尚未平仓）
	TakeProfitPct float64    `json:"take_profit_pct"`      // 预设止盈百分比
	StopLossPct   float64    `json:"stop_loss_pct"`        // 预设止损百分比
	Quantity      float64    `json:"quantity"`             // 持仓数量（手动设置，默认 1）
}

// Report 管理所有交易持仓记录，提供线程安全的增删改查与文件持久化能力。
// path 指定 JSON 持久化文件路径，logs 在内存中维护完整记录列表。
type Report struct {
	mu   sync.RWMutex // 读写锁，保证并发安全
	logs []ExecLog    // 内存中的全部交易记录
	path string       // JSON 持久化文件路径
}

// New 创建 Report 实例并加载指定路径的持久化数据（若存在）。
// path 为空字符串时可创建一个仅内存操作的 Report（不进行读写持久化）。
func New(path string) *Report {
	r := &Report{
		logs: make([]ExecLog, 0),
		path: path,
	}
	r.Load()
	return r
}

// LogSignal 记录一条新的开仓信号，将 ExecLog 追加到日志列表末尾。
// 参数依次为：id-信号ID, code-股票代码, name-股票名称, direction-交易方向,
// strategy-战法名称, entryPrice-开仓价格, takeProfitPct-止盈百分比, stopLossPct-止损百分比。
// 记录后自动持久化到文件。
func (r *Report) LogSignal(id, code, name, direction, strategy string, entryPrice, takeProfitPct, stopLossPct float64) {
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
	})
	r.save()
	log.Printf("[report] 开仓记录: %s %s %s %.2f", strategy, code, name, entryPrice)
}

// LogExit 根据 signalID 平仓。计算盈亏百分比（(exitPrice - entryPrice) / entryPrice * 100），
// 并据此标记状态为"已止盈"（pct > 0）或"已止损"（pct <= 0）。
// 若找不到匹配的持仓记录（或该记录已平仓），则静默返回。
func (r *Report) LogExit(signalID string, exitPrice float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for i := range r.logs {
		if r.logs[i].SignalID == signalID && r.logs[i].ExitAt == nil {
			pct := (exitPrice - r.logs[i].EntryPrice) / r.logs[i].EntryPrice * 100
			r.logs[i].ExitAt = &now
			r.logs[i].ExitPrice = &exitPrice
			r.logs[i].ProfitPct = &pct
			if pct > 0 {
				r.logs[i].Status = "已止盈"
			} else {
				r.logs[i].Status = "已止损"
			}
			r.save()
			log.Printf("[report] 平仓记录: %s 盈亏%.2f%%", signalID, pct)
			return
		}
	}
}

// Update 根据信号 ID 查找对应的 ExecLog，并在持有写锁的情况下执行用户自定义修改函数 fn。
// 常用于修改止损价、止盈价等字段。修改后自动持久化。
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

// Delete 对指定 ID 的记录执行软删除——将 Status 标记为"已删除"而非物理移除。
// 已删除记录仍会保留在列表和持久化文件中，用于后续审计和统计。
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
func (r *Report) List() []ExecLog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ExecLog, len(r.logs))
	copy(out, r.logs)
	return out
}

// HeldPositionCodes 返回当前所有状态为"持仓中"的股票代码（已去重）。
// 用于向策略引擎提供当前持仓信息，影响打分池构建和风险控制决策。
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

// HeldPositions 返回当前所有状态为"持仓中"的完整 ExecLog 记录列表。
// 用于显示模块展示当前持仓详情。
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

// Stats 计算并返回交易统计指标：
//
//	total   - 总交易笔数（不含已删除记录）
//	holding - 当前持仓数（状态为"持仓中"）
//	win     - 盈利笔数（已平仓且 ProfitPct > 0）
//	winRate - 胜率百分比 = win / (win + loss) * 100
//	avgWin  - 平均盈利百分比
//	avgLoss - 平均亏损百分比（负值）
func (r *Report) Stats() (total, holding, win int, winRate, avgWin, avgLoss float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total = len(r.logs)
	var winCount, lossCount int
	var winSum, lossSum float64
	for _, l := range r.logs {
		if l.ExitAt != nil && l.ProfitPct != nil {
			if *l.ProfitPct > 0 {
				winCount++
				winSum += *l.ProfitPct
			} else {
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
func (r *Report) save() {
	if r.path == "" {
		return
	}
	data, err := json.MarshalIndent(r.logs, "", "  ")
	if err != nil {
		log.Printf("[report] 序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(r.path, data, 0644); err != nil {
		log.Printf("[report] 写入失败: %v", err)
	}
}

// Load 从持久化文件读取 JSON 数据并解析到 r.logs 中。
// 若文件不存在则静默返回（空列表），解析失败时记录错误日志。
// 此方法在 Report.New 中自动调用，用于启动时恢复历史记录。
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
