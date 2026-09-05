// 灰度分级（§Phase2 自动灰度分级）：候选审批链路新增"灰度观察"档位。
// 灰度是 proposed（待审批）→ approved（实盘应用）之间的中间态——候选经审批进入灰度库
// （grayscale_rules.json），不参与实盘 8a/8b 注入，仅由模拟盘按规则消费产出灰度期观测数据；
// 灰度期表现达标后由人工/自动晋升 approved，未达标则回退 rejected。
// 目的是把"新战法直接上实盘"降级为"先在 paper 盘观察 N 个交易日"，降低未经实盘验证的战法冲击。
// English: grayscale grading (Phase 2). A middle stage between proposed and approved: a grayscaled
// candidate enters the grayscale library (grayscale_rules.json) with live 8a/8b injection disabled;
// only the paper book consumes it for observation. After the observation window it may be promoted to
// approved or demoted to rejected.
package research

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// CandidateGrayscaleStatus 灰度状态常量（候选 Status 字段取值）。
const (
	StatusGrayscale = "grayscale" // 灰度观察（paper 实测中）
	// English: in grayscale observation (being measured on the paper book).
)

// GrayscaleFactorRule 灰度库中的一条因子战法（与 AppliedFactorEntry 同构，但不入实盘）。
// English: one factor strategy in the grayscale library (same shape as the applied entry, but not live).
type GrayscaleFactorRule struct {
	ID              string             `json:"id"`               // 规则唯一 ID（"gfac_<candidate_id>"）
	Name            string             `json:"name"`             // 显示名
	CandID          int64              `json:"candidate_id"`     // 来源候选 ID
	EnteredAt       string             `json:"entered_at"`       // 进入灰度时间
	ObservationDays int                `json:"observation_days"` // 建议灰度观测期（交易日）
	Factors         []string           `json:"factors"`          // 因子 ID
	Weights         map[string]float64 `json:"weights"`          // factorID → 权重
	Directions      map[string]int     `json:"directions"`       // factorID → 方向
	BuyThreshold    float64            `json:"buy_threshold"`    // 触发阈值
	Horizon         int                `json:"horizon"`          // 前瞻天数
	IR              float64            `json:"ir"`               // 全样本 IR
	Excess          float64            `json:"excess"`           // 回测超额
	Enabled         bool               `json:"enabled"`          // 是否参与 paper 观察
}

// GrayscalePatternRule 灰度库中的一条形态战法（与 AppliedPatternEntry 同构）。
// English: one pattern strategy in the grayscale library.
type GrayscalePatternRule struct {
	ID              string        `json:"id"`           // "gpat_<candidate_id>"
	Name            string        `json:"name"`         // 显示名
	CandID          int64         `json:"candidate_id"` // 来源候选 ID
	EnteredAt       string        `json:"entered_at"`   // 进入灰度时间
	ObservationDays int           `json:"observation_days"`
	Conds           []PatternCond `json:"conds"`   // 条件集
	Enabled         bool          `json:"enabled"` // 是否参与 paper 观察
}

// grayscaleFile 灰度库磁盘结构。
// English: on-disk grayscale-library file.
type grayscaleFile struct {
	Factors  []GrayscaleFactorRule  `json:"factors"`
	Patterns []GrayscalePatternRule `json:"patterns"`
}

// GrayscalePath 返回灰度库文件路径（研究与引擎共享 dataDir）。
func GrayscalePath(dataDir string) string {
	return filepath.Join(dataDir, "grayscale_rules.json")
}

// defaultObservationDays 默认灰度观测期（交易日）。
const defaultObservationDays = 20

// ApplyGrayscale 把候选写入灰度库（幂等：同 candidate_id 不重复追加）。
// kind 仅支持 factor/pattern（weight/depth 型候选不走灰度，直接按原审批路径）。
// English: writes a candidate into the grayscale library (idempotent per candidate_id).
// Supports factor/pattern candidates only.
func ApplyGrayscale(dataDir string, c *store.Candidate) error {
	if c.Kind != "factor" && c.Kind != "pattern" {
		return fmt.Errorf("灰度仅支持 factor/pattern 候选，实际 kind=%s", c.Kind)
	}
	p := GrayscalePath(dataDir)
	gs := grayscaleFile{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &gs)
	}
	switch c.Kind {
	case "factor":
		for _, r := range gs.Factors {
			if r.CandID == c.ID {
				return nil // 幂等：已在灰度库
			}
		}
		var payload struct {
			Weights      map[string]float64 `json:"weights"`
			Directions   map[string]int     `json:"directions"`
			BuyThreshold float64            `json:"buy_threshold"`
		}
		_ = json.Unmarshal([]byte(c.Weights), &payload)
		var factors []string
		_ = json.Unmarshal([]byte(c.Factors), &factors)
		// 追加灰度因子规则（已存在同候选 ID 则提前返回，幂等）。
		gs.Factors = append(gs.Factors, GrayscaleFactorRule{
			ID:              "gfac_" + strconv.FormatInt(c.ID, 10),
			Name:            "灰度因子#" + strconv.FormatInt(c.ID, 10),
			CandID:          c.ID,
			EnteredAt:       time.Now().Format("2006-01-02 15:04:05"),
			ObservationDays: defaultObservationDays,
			Factors:         factors,
			Weights:         payload.Weights,
			Directions:      payload.Directions,
			BuyThreshold:    payload.BuyThreshold,
			Horizon:         c.Horizon,
			IR:              c.IR,
			Excess:          c.AvgExcess,
			Enabled:         true,
		})
	// pattern 分支：已存在同候选则跳过，追加形态灰度规则。
	case "pattern":
		for _, r := range gs.Patterns {
			if r.CandID == c.ID {
				return nil
			}
		}
		var conds []PatternCond
		_ = json.Unmarshal([]byte(c.Factors), &conds)
		gs.Patterns = append(gs.Patterns, GrayscalePatternRule{
			ID:              "gpat_" + strconv.FormatInt(c.ID, 10),
			Name:            "灰度形态#" + strconv.FormatInt(c.ID, 10),
			CandID:          c.ID,
			EnteredAt:       time.Now().Format("2006-01-02 15:04:05"),
			ObservationDays: defaultObservationDays,
			Conds:           conds,
			Enabled:         true,
		})
	}
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	return data.AtomicWrite(p, b, 0o644)
}

// DemoteGrayscale 把候选从灰度库移除（晋升 approved 或回退 rejected 时调用）。
// English: removes a candidate from the grayscale library (on promotion or demotion).
func DemoteGrayscale(dataDir string, candID int64) error {
	p := GrayscalePath(dataDir)
	gs := grayscaleFile{}
	if b, err := os.ReadFile(p); err != nil {
		return nil // 无灰度库视为已清理
	} else if err := json.Unmarshal(b, &gs); err != nil {
		return err
	}
	// 分别从因子与形态两类规则中剔除目标候选，标记 changed 以便落盘。
	changed := false
	facts := gs.Factors[:0]
	for _, r := range gs.Factors {
		if r.CandID == candID {
			changed = true
			continue
		}
		facts = append(facts, r)
	}
	gs.Factors = facts
	pats := gs.Patterns[:0]
	for _, r := range gs.Patterns {
		if r.CandID == candID {
			changed = true
			continue
		}
		pats = append(pats, r)
	}
	gs.Patterns = pats
	if !changed {
		return nil
	}
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	return data.AtomicWrite(p, b, 0o644)
}

// LoadGrayscaleRules 读取灰度库（供 paper 观测消费方读取）。
// English: LoadGrayscaleRules reads the grayscale library for paper-observation consumers.
func LoadGrayscaleRules(dataDir string) (grayscaleFile, error) {
	var gs grayscaleFile
	b, err := os.ReadFile(GrayscalePath(dataDir))
	if err != nil {
		return gs, err
	}
	err = json.Unmarshal(b, &gs)
	return gs, err
}
