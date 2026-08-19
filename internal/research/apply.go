// 研究候选应用（B5）：审批通过的权重候选写入 applied_rules.json，供战法消费。
package research

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/store"
	factorstrat "quant-trading-v2/internal/strategies/factor"
	patternstrat "quant-trading-v2/internal/strategies/pattern"
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

// AppliedFactorEntry 战法库中的一条已应用因子战法（E6 + 战法库）。
// 与 FactorRule 相比多出独立 ID/名称/启用状态/来源候选/运行统计（效果监测）。
// English: one applied factor strategy in the strategy library (E6 + library). Adds ID/name/enabled/
// source-candidate/run-stats to FactorRule for live management and effectiveness monitoring.
type AppliedFactorEntry struct {
	ID           string             `json:"id"`            // 规则唯一 ID（"fac_<candidate_id>"）
	Name         string             `json:"name"`          // 显示名（"因子战法#<candidate_id>"）
	Enabled      bool               `json:"enabled"`       // 是否注入 8a/8b 实盘
	CandID       int64              `json:"candidate_id"`  // 来源候选 ID
	AppliedAt    string             `json:"applied_at"`    // 应用时间
	Factors      []string           `json:"factors"`       // 因子 ID
	Weights      map[string]float64 `json:"weights"`       // factorID → 权重
	Directions   map[string]int     `json:"directions"`    // factorID → 方向
	BuyThreshold float64            `json:"buy_threshold"` // 触发阈值
	Horizon      int                `json:"horizon"`       // 前瞻天数
	IR           float64            `json:"ir"`            // 全样本 IR
	Excess       float64            `json:"excess"`        // 回测超额（avg_excess）
	// 效果监测（实盘运行累计）
	SignalCount int     `json:"signal_count"` // 实盘触发信号数
	Win         int     `json:"win"`          // 触发后 Horizon 日收益为正次数
	Loss        int     `json:"loss"`         // 触发后 Horizon 日收益为负次数
	CumReturn   float64 `json:"cum_return"`   // 累计前向收益（% 或小数，由监控写入）
}

// ApplyFactorRule 把审批通过的 factor 候选**追加**写入战法库 applied_factors.json（多战法共存），
// 供实盘因子 runner 读取注入（E6 一键应用）。已存在同 candidate_id 条目则幂等跳过（避免重复审批追加）。
// English: **appends** an approved factor candidate to the strategy library applied_factors.json so
// multiple factor strategies coexist (E6 one-click apply). Idempotent — a rule from the same candidate
// ID is not re-appended.
func ApplyFactorRule(dataDir string, c *store.Candidate) error {
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
	entry := AppliedFactorEntry{
		ID:           "fac_" + strconv.FormatInt(c.ID, 10),
		Name:         "因子战法#" + strconv.FormatInt(c.ID, 10),
		Enabled:      true,
		CandID:       c.ID,
		AppliedAt:    time.Now().Format("2006-01-02 15:04:05"),
		Factors:      factors,
		Weights:      payload.Weights,
		Directions:   payload.Directions,
		BuyThreshold: payload.BuyThreshold,
		Horizon:      c.Horizon,
		IR:           c.IR,
		Excess:       c.AvgExcess,
	}
	if entry.BuyThreshold <= 0 {
		entry.BuyThreshold = 70
	}
	return appendAppliedFactor(dataDir, entry)
}

// ListAppliedFactorRules 读取战法库 applied_factors.json，返回全部已应用因子战法（含禁用）。
// 兼容旧版单对象格式（自动迁移为列表）。文件缺失返回空列表。
// English: reads the strategy library applied_factors.json and returns all applied factor strategies
// (including disabled). Migrates the legacy single-object format to a list. Missing file → empty list.
func ListAppliedFactorRules(dataDir string) ([]AppliedFactorEntry, error) {
	if dataDir == "" {
		return nil, nil
	}
	path := filepath.Join(dataDir, "applied_factors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var entries []AppliedFactorEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		return entries, nil
	}
	// 旧版单对象 → 迁移为列表
	var legacy FactorRule
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, err
	}
	if len(legacy.Factors) == 0 {
		return nil, nil
	}
	entry := AppliedFactorEntry{
		ID: "fac_legacy", Name: "因子战法(旧)", Enabled: true,
		Factors: legacy.Factors, Weights: legacy.Weights, Directions: legacy.Directions,
		BuyThreshold: legacy.BuyThreshold, Horizon: legacy.Horizon, IR: legacy.IR, Excess: legacy.Excess,
		AppliedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	if entry.BuyThreshold <= 0 {
		entry.BuyThreshold = 70
	}
	_ = saveAppliedFactors(dataDir, []AppliedFactorEntry{entry}) // 落盘迁移
	return []AppliedFactorEntry{entry}, nil
}

// LoadAppliedFactorRule 读取战法库中第一条**启用**的因子战法（兼容旧版单规则调用方）。
// English: loads the first **enabled** factor strategy from the library (back-compat for callers
// expecting a single rule). Returns nil when none enabled.
func LoadAppliedFactorRule(dataDir string) (*FactorRule, error) {
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Enabled || len(e.Factors) == 0 {
			continue
		}
		return &FactorRule{
			Factors: e.Factors, Weights: e.Weights, Directions: e.Directions,
			BuyThreshold: e.BuyThreshold, Horizon: e.Horizon, IR: e.IR, Excess: e.Excess,
		}, nil
	}
	return nil, nil
}

// LoadEnabledFactorRules 读取战法库中全部**启用**的因子战法规则，转为引擎 ActiveRule 供注入。
// 依赖 strategies/factor 的 ActiveRule 类型；为避免循环依赖，由调用方包（combat_agent）实现转换，
// 这里返回通用结构。English: returns all **enabled** factor rules as ActiveRule for engine injection.
func LoadEnabledFactorRules(dataDir string) ([]*factorstrat.ActiveRule, error) {
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return nil, err
	}
	var out []*factorstrat.ActiveRule
	for _, e := range entries {
		if !e.Enabled || len(e.Factors) == 0 {
			continue
		}
		out = append(out, &factorstrat.ActiveRule{
			ID: e.ID, Name: e.Name, CandID: e.CandID,
			Rule: factorstrat.Rule{
				Factors: e.Factors, Weights: e.Weights, Directions: e.Directions,
				BuyThreshold: e.BuyThreshold,
			},
		})
	}
	return out, nil
}

// appendAppliedFactor 向战法库追加/替换一条（按 ID 幂等：同 ID 存在则替换）。
// English: appends/replaces one entry in the library (idempotent by ID).
func appendAppliedFactor(dataDir string, entry AppliedFactorEntry) error {
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return err
	}
	replaced := false
	for i := range entries {
		if entries[i].ID == entry.ID {
			// 保留运行统计（效果监测不因重复应用而清零），只更新规则字段
			keep := entries[i]
			entry.SignalCount, entry.Win, entry.Loss, entry.CumReturn = keep.SignalCount, keep.Win, keep.Loss, keep.CumReturn
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	return saveAppliedFactors(dataDir, entries)
}

// SetAppliedFactorEnabled 启用/禁用战法库中某条（按 ID）。
// English: enables/disables an entry in the library (by ID).
func SetAppliedFactorEnabled(dataDir, id string, enabled bool) error {
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].Enabled = enabled
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("战法 %s 不存在", id)
	}
	return saveAppliedFactors(dataDir, entries)
}

// RemoveAppliedFactorRule 删除战法库中某条（按 ID）。
// English: removes an entry from the library (by ID).
func RemoveAppliedFactorRule(dataDir, id string) error {
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return err
	}
	out := entries[:0]
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return fmt.Errorf("战法 %s 不存在", id)
	}
	return saveAppliedFactors(dataDir, out)
}

// RenameAppliedFactor 重命名战法库中某条（按 ID）。空名忽略。
// English: renames an entry in the library (by ID). Empty name is ignored.
func RenameAppliedFactor(dataDir, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].Name = name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("战法 %s 不存在", id)
	}
	return saveAppliedFactors(dataDir, entries)
}

// UpdateAppliedFactorStats 更新战法库中某条的运行统计（效果监测回写）。
// English: updates one entry's run stats (effectiveness-monitoring write-back).
func UpdateAppliedFactorStats(dataDir, id string, sc, win, loss int, cum float64) error {
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].SignalCount, entries[i].Win, entries[i].Loss, entries[i].CumReturn = sc, win, loss, cum
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("战法 %s 不存在", id)
	}
	return saveAppliedFactors(dataDir, entries)
}

// saveAppliedFactors 落盘战法库。
// English: persists the strategy library.
func saveAppliedFactors(dataDir string, entries []AppliedFactorEntry) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "applied_factors.json"), b, 0o644)
}

// AppliedPatternRule 实盘形态模板规则（F3）。复用本包 pattern.go 的 PatternCond。
// English: live pattern-template rule (F3). Reuses PatternCond from pattern.go.
type AppliedPatternRule struct {
	Name  string        `json:"name"`
	Conds []PatternCond `json:"conds"`
}

// AppliedPatternEntry 战法库中的一条已应用形态战法（F3 + 战法库），带独立 ID/名称/启用/来源候选/运行统计。
// English: one applied pattern strategy in the library (F3 + library), with ID/name/enabled/source/run-stats.
type AppliedPatternEntry struct {
	ID          string        `json:"id"`           // "pat_<candidate_id>"
	Name        string        `json:"name"`         // 显示名
	Enabled     bool          `json:"enabled"`      // 是否注入 8a/8b
	CandID      int64         `json:"candidate_id"` // 来源候选 ID
	AppliedAt   string        `json:"applied_at"`   // 应用时间
	Conds       []PatternCond `json:"conds"`        // 条件集
	SignalCount int           `json:"signal_count"` // 触发信号数
	Win         int           `json:"win"`
	Loss        int           `json:"loss"`
	CumReturn   float64       `json:"cum_return"`
}

// ApplyPatternRule 把审批通过的 pattern 候选**追加**写入战法库 applied_patterns.json（多形态共存，按候选 ID 幂等）。
// English: appends an approved pattern candidate to the library applied_patterns.json (idempotent by candidate ID).
func ApplyPatternRule(dataDir string, c *store.Candidate) error {
	var conds []PatternCond
	if err := json.Unmarshal([]byte(c.Factors), &conds); err != nil {
		return err
	}
	if len(conds) == 0 {
		return nil
	}
	entry := AppliedPatternEntry{
		ID:      "pat_" + strconv.FormatInt(c.ID, 10),
		Name:    "形态战法#" + strconv.FormatInt(c.ID, 10),
		Enabled: true, CandID: c.ID,
		AppliedAt: time.Now().Format("2006-01-02 15:04:05"),
		Conds:     conds,
	}
	return appendAppliedPattern(dataDir, entry)
}

// ListAppliedPatternRules 读取战法库 applied_patterns.json，返回全部已应用形态战法（含禁用）。
// 兼容旧版单对象格式（自动迁移为列表）。English: reads the pattern library, migrating the legacy
// single-object format to a list; returns all (including disabled).
func ListAppliedPatternRules(dataDir string) ([]AppliedPatternEntry, error) {
	if dataDir == "" {
		return nil, nil
	}
	path := filepath.Join(dataDir, "applied_patterns.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var entries []AppliedPatternEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		return entries, nil
	}
	// 旧版单对象 → 迁移
	var legacy AppliedPatternRule
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, err
	}
	if len(legacy.Conds) == 0 {
		return nil, nil
	}
	name := legacy.Name
	if name == "" {
		name = "自动形态"
	}
	entry := AppliedPatternEntry{ID: "pat_legacy", Name: name, Enabled: true, Conds: legacy.Conds,
		AppliedAt: time.Now().Format("2006-01-02 15:04:05")}
	_ = saveAppliedPatterns(dataDir, []AppliedPatternEntry{entry})
	return []AppliedPatternEntry{entry}, nil
}

// LoadAppliedPatternRule 读取战法库第一条**启用**的形态战法（兼容旧版单规则调用方）。
// English: loads the first **enabled** pattern from the library (back-compat for single-rule callers).
func LoadAppliedPatternRule(dataDir string) (*AppliedPatternRule, error) {
	entries, err := ListAppliedPatternRules(dataDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Enabled || len(e.Conds) == 0 {
			continue
		}
		return &AppliedPatternRule{Name: e.Name, Conds: e.Conds}, nil
	}
	return nil, nil
}

// LoadEnabledPatternRules 读取战法库全部**启用**的形态规则（供引擎注入）。
// English: loads all **enabled** pattern rules from the library (for engine injection).
func LoadEnabledPatternRules(dataDir string) ([]*patternstrat.ActivePattern, error) {
	entries, err := ListAppliedPatternRules(dataDir)
	if err != nil {
		return nil, err
	}
	var out []*patternstrat.ActivePattern
	for _, e := range entries {
		if !e.Enabled || len(e.Conds) == 0 {
			continue
		}
		conds := make([]patternstrat.Cond, len(e.Conds))
		for i, c := range e.Conds {
			conds[i] = patternstrat.Cond{Factor: c.Factor, Min: c.Min, Max: c.Max}
		}
		out = append(out, &patternstrat.ActivePattern{ID: e.ID, Name: e.Name, CandID: e.CandID, Conds: conds})
	}
	return out, nil
}

// appendAppliedPattern 追加/替换战法库一条（按 ID 幂等）。
// English: appends/replaces one entry in the pattern library (idempotent by ID).
func appendAppliedPattern(dataDir string, entry AppliedPatternEntry) error {
	entries, err := ListAppliedPatternRules(dataDir)
	if err != nil {
		return err
	}
	replaced := false
	for i := range entries {
		if entries[i].ID == entry.ID {
			keep := entries[i]
			entry.SignalCount, entry.Win, entry.Loss, entry.CumReturn = keep.SignalCount, keep.Win, keep.Loss, keep.CumReturn
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	return saveAppliedPatterns(dataDir, entries)
}

// SetAppliedPatternEnabled 启用/禁用形态战法库某条（按 ID）。
// （SetAppliedPatternEnabled enables/disables a pattern-library entry by ID.）
func SetAppliedPatternEnabled(dataDir, id string, enabled bool) error {
	entries, err := ListAppliedPatternRules(dataDir)
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].Enabled = enabled
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("形态战法 %s 不存在", id)
	}
	return saveAppliedPatterns(dataDir, entries)
}

// RemoveAppliedPatternRule 删除形态战法库某条（按 ID）。
// （RemoveAppliedPatternRule removes a pattern-library entry by ID.）
func RemoveAppliedPatternRule(dataDir, id string) error {
	entries, err := ListAppliedPatternRules(dataDir)
	if err != nil {
		return err
	}
	out := entries[:0]
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return fmt.Errorf("形态战法 %s 不存在", id)
	}
	return saveAppliedPatterns(dataDir, out)
}

// RenameAppliedPattern 重命名形态战法库某条（按 ID）。空名忽略。
// （RenameAppliedPattern renames a pattern-library entry by ID; empty name is ignored.）
func RenameAppliedPattern(dataDir, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	entries, err := ListAppliedPatternRules(dataDir)
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].Name = name
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("形态战法 %s 不存在", id)
	}
	return saveAppliedPatterns(dataDir, entries)
}

// UpdateAppliedPatternStats 更新形态战法库某条的运行统计（效果监测回写）。
// （UpdateAppliedPatternStats updates a pattern-library entry's run stats.）
func UpdateAppliedPatternStats(dataDir, id string, sc, win, loss int, cum float64) error {
	entries, err := ListAppliedPatternRules(dataDir)
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].ID == id {
			entries[i].SignalCount, entries[i].Win, entries[i].Loss, entries[i].CumReturn = sc, win, loss, cum
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("形态战法 %s 不存在", id)
	}
	return saveAppliedPatterns(dataDir, entries)
}

// saveAppliedPatterns 落盘形态战法库（JSON 缩进格式，0644）。
// （saveAppliedPatterns persists the pattern strategy library.）
func saveAppliedPatterns(dataDir string, entries []AppliedPatternEntry) error {
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "applied_patterns.json"), b, 0o644)
}
