// 研究候选应用（B5）：审批通过的权重候选写入 applied_rules.json，供战法消费。
package research

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"quant-trading-v2/internal/store"
)

// ApplyWeights 把审批通过的权重候选写入 dataDir/applied_rules.json。
// 引擎侧按需读取（B5 一键应用；config 热加载链路同时生效）。
// （ApplyWeights writes an approved weight candidate to applied_rules.json.）
func ApplyWeights(dataDir string, c *store.Candidate) error {
	var weights map[string]float64
	if err := json.Unmarshal([]byte(c.Weights), &weights); err != nil {
		return err
	}
	out := map[string]any{
		"kind":       "weights",
		"factors":    c.Factors,
		"weights":    weights,
		"horizon":    c.Horizon,
		"ic_mean":    c.ICMean,
		"ir":         c.IR,
		"excess":     c.AvgExcess,
		"applied_at": time.Now().Format("2006-01-02 15:04:05"),
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "applied_rules.json"), b, 0o644)
}

// FactorRule 实盘因子战法规则（E6），由审批通过的 factor 候选落盘，供引擎 runner 注入。
// Weights 字段复合结构 {weights, directions, buy_threshold} 经 ApplyFactorRule 解析。
// （FactorRule is the live factor-strategy rule (E6), persisted from an approved factor candidate and
// injected into the engine runner.）
type FactorRule struct {
	Factors      []string           `json:"factors"`
	Weights      map[string]float64 `json:"weights"`
	Directions   map[string]int     `json:"directions"`
	BuyThreshold float64            `json:"buy_threshold"`
	Horizon      int                `json:"horizon"`
	IR           float64            `json:"ir"`
	Excess       float64            `json:"excess"`
}

// ApplyFactorRule 把审批通过的 factor 候选写入 dataDir/applied_factors.json，
// 供实盘因子 runner 读取注入（E6 一键应用）。
// English: writes an approved factor candidate to applied_factors.json for the live factor runner.
func ApplyFactorRule(dataDir string, c *store.Candidate) error {
	// 解析 Weights 复合结构
	var payload struct {
		Weights      map[string]float64 `json:"weights"`
		Directions   map[string]int     `json:"directions"`
		BuyThreshold float64            `json:"buy_threshold"`
	}
	if err := json.Unmarshal([]byte(c.Weights), &payload); err != nil {
		return err
	}
	var factors []string
	json.Unmarshal([]byte(c.Factors), &factors)
	rule := FactorRule{
		Factors: factors, Weights: payload.Weights, Directions: payload.Directions,
		BuyThreshold: payload.BuyThreshold, Horizon: c.Horizon, IR: c.IR, Excess: c.AvgExcess,
	}
	if rule.BuyThreshold <= 0 {
		rule.BuyThreshold = 70
	}
	b, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "applied_factors.json"), b, 0o644)
}

// LoadAppliedFactorRule 读取 dataDir/applied_factors.json，返回 nil 表示未启用因子战法。
// English: loads applied_factors.json; nil when the factor strategy is not enabled.
func LoadAppliedFactorRule(dataDir string) (*FactorRule, error) {
	if dataDir == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "applied_factors.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var r FactorRule
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if len(r.Factors) == 0 {
		return nil, nil
	}
	return &r, nil
}

// AppliedPatternRule 实盘形态模板规则（F3）。复用本包 pattern.go 的 PatternCond。
// English: live pattern-template rule (F3). Reuses PatternCond from pattern.go.
type AppliedPatternRule struct {
	Name  string        `json:"name"`
	Conds []PatternCond `json:"conds"`
}

// ApplyPatternRule 把审批通过的 pattern 候选写入 dataDir/applied_patterns.json，
// 供实盘形态解释器读取注入（F3 一键应用）。
// English: writes an approved pattern candidate to applied_patterns.json for the live pattern runner.
func ApplyPatternRule(dataDir string, c *store.Candidate) error {
	var conds []PatternCond
	if err := json.Unmarshal([]byte(c.Factors), &conds); err != nil {
		return err
	}
	if len(conds) == 0 {
		return nil
	}
	rule := AppliedPatternRule{Name: "自动形态", Conds: conds}
	b, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "applied_patterns.json"), b, 0o644)
}

// LoadAppliedPatternRule 读取 dataDir/applied_patterns.json，返回 nil 表示未启用形态战法。
// English: loads applied_patterns.json; nil when the pattern strategy is not enabled.
func LoadAppliedPatternRule(dataDir string) (*AppliedPatternRule, error) {
	if dataDir == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "applied_patterns.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var r AppliedPatternRule
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if len(r.Conds) == 0 {
		return nil, nil
	}
	return &r, nil
}
