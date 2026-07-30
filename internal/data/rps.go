// Package data — 板块 RPS 相对强度管理。
// RPS（Relative Price Strength）衡量板块在全部板块中的涨幅排名百分位，
// 按 5/20/60 日三个周期分别计算，用于识别强势板块。
package data

import (
	"sort"
	"sync"
	"time"
)

// SectorRPS 板块相对强度指标。
// RPS 值范围 0–100，越接近 100 表示排名越靠前。
type SectorRPS struct {
	Code   string  `json:"code"`    // 板块代码
	Name   string  `json:"name"`    // 板块名称
	RPS5   float64 `json:"rps5"`    // 5 日 RPS
	RPS20  float64 `json:"rps20"`   // 20 日 RPS
	RPS60  float64 `json:"rps60"`   // 60 日 RPS
	Slope  float64 `json:"slope"`   // RPS 斜率（3 日变化率）
	Level  string  `json:"level"`   // 等级：S/A/B/C
	IsMain bool    `json:"is_main"` // 是否为主线板块
}

// RPSManager RPS 管理器。
// 维护全量板块 RPS 及 Top5 龙头板块列表，线程安全。
type RPSManager struct {
	mu         sync.RWMutex
	sectors    []SectorRPS // 全量板块 RPS 列表
	topSectors []SectorRPS // Top5 龙头板块
	totalCount int         // 板块总数（用于 RPS 计算分母）
	updatedAt  time.Time   // 最后更新时间
}

// NewRPSManager 创建 RPS 管理器，默认板块总数为 300。
func NewRPSManager() *RPSManager {
	return &RPSManager{
		totalCount: 300,
	}
}

// Update 更新全量板块 RPS 并自动计算等级和主线标记。
// 同时筛选出主线或 RPS20>=85 的板块并按 RPS20 降序取 Top5。
func (r *RPSManager) Update(sectors []SectorRPS) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range sectors {
		s := &sectors[i]
		s.Level = rpsLevel(s.RPS20, s.RPS60)
		s.IsMain = isMainSector(s.RPS20, s.RPS60)
	}

	r.sectors = sectors

	var top []SectorRPS
	for _, s := range sectors {
		if s.IsMain || s.RPS20 >= 85 {
			top = append(top, s)
		}
	}
	sort.Slice(top, func(i, j int) bool {
		return top[i].RPS20 > top[j].RPS20
	})
	if len(top) > 5 {
		top = top[:5]
	}
	r.topSectors = top
	r.updatedAt = time.Now()
}

// GetSector 按板块代码查询 RPS 数据。返回 nil 表示不存在。
func (r *RPSManager) GetSector(code string) *SectorRPS {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.sectors {
		if s.Code == code {
			return &s
		}
	}
	return nil
}

// GetTopSectors 返回 Top5 龙头板块的副本。
func (r *RPSManager) GetTopSectors() []SectorRPS {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SectorRPS, len(r.topSectors))
	copy(out, r.topSectors)
	return out
}

// SectorWeight 根据 RPS20 返回板块权重系数。
// RPS>=90: 1.1, RPS>=80: 1.0, RPS>=60: 0.8, 否则 0。
func (r *RPSManager) SectorWeight(rps20 float64) float64 {
	if rps20 >= 90 {
		return 1.1
	}
	if rps20 >= 80 {
		return 1.0
	}
	if rps20 >= 60 {
		return 0.8
	}
	return 0
}

// IsSectorMain 快速判断是否为见解板块（RPS20>=85 且 RPS60>=80）。
func (r *RPSManager) IsSectorMain(rps20, rps60 float64) bool {
	return rps20 >= 85 && rps60 >= 80
}

// MarketScore 计算市场综合评分（0–3）。
// 评分依据：Top 板块数量 >2、全市场平均 RPS>50、主线清晰度。
func (r *RPSManager) MarketScore() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	score := 0
	if len(r.topSectors) > 2 {
		score++
	}
	avgRPS := 0.0
	if len(r.sectors) > 0 {
		for _, s := range r.sectors {
			avgRPS += s.RPS20
		}
		avgRPS /= float64(len(r.sectors))
	}
	if avgRPS > 50 {
		score++
	}
	if len(r.topSectors) >= 3 && avgRPS > 55 {
		score++
	}
	return score
}

// CalculateRPS5 计算 5 日 RPS 值。
// percentile 为板块涨幅在全体中的百分位排名（0-1）。
func CalculateRPS5(percentile float64, total int) float64 {
	if total <= 0 {
		return 0
	}
	rank := (1 - percentile) * float64(total)
	return 100 * (1 - rank/float64(total))
}

// rpsLevel 根据 RPS20/RPS60 判定等级。
// S 级: RPS20>=90, A 级: RPS20>=80, B 级: RPS20>=60, C 级: 其余。
func rpsLevel(rps20, rps60 float64) string {
	if rps20 >= 90 {
		return "S"
	}
	if rps20 >= 80 {
		return "A"
	}
	if rps20 >= 60 {
		return "B"
	}
	return "C"
}

// isMainSector 判断是否为主线板块。
// 条件：RPS20>=85 且 RPS60>=70（宽松版本）。
func isMainSector(rps20, rps60 float64) bool {
	if rps20 >= 85 && rps60 >= 80 {
		return true
	}
	if rps20 >= 85 && rps60 >= 70 {
		return true
	}
	return false
}

// Slope 计算 RPS 斜率（3 日平均变化率）。
func Slope(rpsToday, rps3DaysAgo float64) float64 {
	return (rpsToday - rps3DaysAgo) / 3
}

// IsSlopeAccelerating RPS 斜率是否加速上升（斜率 >2）。
func IsSlopeAccelerating(slope float64) bool {
	return slope > 2
}

// IsSlopeDeclining RPS 斜率是否加速下降（斜率 <= -2）。
func IsSlopeDeclining(slope float64) bool {
	return slope <= -2
}
