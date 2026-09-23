// 研究候选应用（B5）：审批通过的权重候选写入 applied_rules.json，供战法消费。
// §ADJ-BASIS-2（2026-09-23）：因子战法条目落盘时盖"复权口径版本"戳，载入时据此判 stale 并喂
// 指标面（applied_factor_stale_basis_count → p1 告警）；处置动作由 rules.research.stale_adj_basis_action
// 决定（缺省 shadow = 只标记告警、不停投）。
// English: stamps the adjustment basis on apply, classifies staleness on load and feeds the alert
// gauge; disposition is config-driven and defaults to shadow (mark + alert only).
package research

import (
	"quant-trading-v2/internal/data"

	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/store"
	factorstrat "quant-trading-v2/internal/strategies/factor"
	patternstrat "quant-trading-v2/internal/strategies/pattern"
)

// ApplyWeights 把审批通过的权重候选写入 dataDir/applied_rules.json。
// 引擎侧按需读取（B5 一键应用；config 热加载链路同时生效）。
// （ApplyWeights writes an approved weight candidate to applied_rules.json.）
func ApplyWeights(dataDir string, c *store.Candidate) error {
	snapshotBeforeWrite(dataDir)
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
	return data.AtomicWrite(filepath.Join(dataDir, "applied_rules.json"), b, 0o644) // §W3-c
}

// FactorRule 实盘因子战法规则（E6），由审批通过的 factor 候选落盘，供引擎 runner 注入。
// Weights 字段复合结构 {weights, directions, buy_threshold} 经 ApplyFactorRule 解析。
// （FactorRule is the live factor-strategy rule (E6), persisted from an approved factor candidate and
// injected into the engine runner.）
type FactorRule struct {
	// 因子 ID 列表（复合分的组成因子）
	Factors []string `json:"factors"`
	// factorID → 权重（L1 归一化）
	Weights map[string]float64 `json:"weights"`
	// factorID → 方向（+1 看多 / -1 看空）
	Directions map[string]int `json:"directions"`
	// 触发阈值：复合分高于此值才产生买入信号
	BuyThreshold float64 `json:"buy_threshold"`
	// 前瞻天数
	Horizon int `json:"horizon"`
	// 全样本 IR
	IR float64 `json:"ir"`
	// 回测超额（avg_excess）
	Excess float64 `json:"excess"`
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

	// §P2-d 规则级参数覆盖（扫参审批后写入；0=缺省用全局默认）。
	// ExitTrailPct/ExitMaxHoldDays：回测 ruleEvalAdapter 立即生效；实盘由 rule_exit_overrides.go 注册表消费。
	// English: rule-level parameter overrides from sweep approvals (0 = use global defaults).
	// ExitTrailPct/ExitMaxHoldDays: consumed by rule_exit_overrides.go registry for live exit logic.
	ExitTrailPct    float64 `json:"exit_trail_pct,omitempty"`     // 移动止盈比例
	ExitStopLossPct float64 `json:"exit_stop_loss_pct,omitempty"` // 止损比例
	ExitMaxHoldDays int     `json:"exit_max_hold_days,omitempty"` // 最大持仓天数

	// §ADJ-BASIS-2（2026-09-23）应用时使用的复权取数口径版本（写入即盖章）。
	// 值 = AdjBaselineVersion（见 windowed.go）。**空串表示本字段上线前写入的旧条目**：
	// 它是在 §ADJ 修复（HfqBars 前向填充因子）之前的错误面板上拟合出来的，
	// 因此按 stale 处理（见 StaleAdjBasis）——我们不回填、不改写历史文件，只如实标注。
	// English: adjustment-basis stamp written at apply time; empty = pre-fix legacy entry, treated as stale.
	AdjBasis string `json:"adj_basis,omitempty"`
	// StaleAdjBasis 载入时算出的派生标记（**不落盘**）：AdjBasis != AdjBaselineVersion 即为真。
	// 只用于战法库展示与告警口径，本身不改变实盘行为（是否停投由 rules.research.stale_adj_basis_action 决定）。
	// English: derived-at-load flag (never persisted) — true when the stamped basis is not current.
	StaleAdjBasis bool `json:"-"`
}

// 复权基线失效战法的两种处置动作（config rules.research.stale_adj_basis_action 的字面量）。
// English: the two dispositions for strategies whose adjustment basis went stale.
const (
	// StaleAdjBasisShadow 只标记 + 告警，照旧参与实盘（缺省）。
	StaleAdjBasisShadow = "shadow"
	// StaleAdjBasisDisable fail-close：把失效战法从 enabled 集合剔除，不再产生新买入信号。
	StaleAdjBasisDisable = "disable"
)

// staleAdjAction/staleAdjActionFn 进程级处置策略（由启动装配注入，见 cmd/quant/main.go）。
// 之所以是包级状态而不是把 cfg 一路穿进 LoadEnabledFactorRules：调用链有三处
// （engine registry / combat_agent.ReloadFactorRules / server 热重载与资金池重建），后两处签名
// 不带 config，且 ReloadFactorRules 还实现了 server 侧的接口——穿参会波及一整条热重载接口链。
// 缺省 shadow ⇒ 未注入（单测、独立进程）时行为与修复前完全一致，不存在"忘了接线就停战法"。
// English: process-wide disposition injected at startup; default shadow keeps behavior identical to
// pre-change when nobody wires config (tests / standalone processes).
var (
	staleAdjMu       sync.RWMutex
	staleAdjAction   = StaleAdjBasisShadow
	staleAdjActionFn func() string // 可选活取值回调（config 热重载后，下一轮读库即生效）
)

// normalizeStaleAdjBasisAction 归一：只有显式 "disable" 才 fail-close，其余（含未知值）一律 shadow。
// 未知值绝不能被解释成"停战法"——那等于让一个拼写错误改变资本行为。
// English: only an explicit "disable" fail-closes; anything else (including typos) stays shadow.
func normalizeStaleAdjBasisAction(action string) string {
	if strings.TrimSpace(action) == StaleAdjBasisDisable {
		return StaleAdjBasisDisable
	}
	return StaleAdjBasisShadow
}

// ConfigureStaleAdjBasisAction 注入处置策略（"shadow"/"disable"；未知值归一为 shadow），
// 并清掉活取值回调。返回归一化后的实际生效值，便于启动日志记录。
// English: injects the disposition (unknown → shadow) and clears the live getter; returns the
// effective value for startup logging.
func ConfigureStaleAdjBasisAction(action string) string {
	eff := normalizeStaleAdjBasisAction(action)
	staleAdjMu.Lock()
	staleAdjAction = eff
	staleAdjActionFn = nil
	staleAdjMu.Unlock()
	return eff
}

// ConfigureStaleAdjBasisActionFunc 注入**活**取值回调（启动装配用：闭包读 cfgMgr 快照，
// 使 rules.research.stale_adj_basis_action 在 config 热重载后的下一轮读库自然生效，无需重启）。
// 传 nil 退回 ConfigureStaleAdjBasisAction 设置的静态值。
// English: injects a live getter so a hot-reloaded config value takes effect on the next library
// read without a restart; nil falls back to the statically configured value.
func ConfigureStaleAdjBasisActionFunc(fn func() string) {
	staleAdjMu.Lock()
	staleAdjActionFn = fn
	staleAdjMu.Unlock()
}

// StaleAdjBasisAction 当前生效的处置策略（缺省 shadow）。
// English: current effective disposition (defaults to shadow).
func StaleAdjBasisAction() string {
	staleAdjMu.RLock()
	fn := staleAdjActionFn
	fallback := staleAdjAction
	staleAdjMu.RUnlock()
	if fn == nil {
		return fallback
	}
	return normalizeStaleAdjBasisAction(fn())
}

// IsAdjBasisStale 单条目口径判定：非当前基线（含空戳的旧条目）一律 stale。
// English: an entry is stale unless its stamp equals the current basis (empty/legacy counts as stale).
func IsAdjBasisStale(adjBasis string) bool { return adjBasis != AdjBaselineVersion }

// markStaleAdjBasis 载入后统一打派生标记，并把失效条数写进指标面（量规 + p1 告警的数据源）。
// 每次读库都是赋值点：启动装配（server.ActivePaperPoolTypes）、引擎按账号装配、审批热重载、
// 战法库 GET 轮询都会走这里；"没有库"的早退分支同样写 0，不留残值——
// 不会留下"有规则、无赋值"的死规则形态（§DEADGAUGE）。
// English: marks each loaded entry stale/fresh and feeds the staleness gauge — startup assembly,
// per-account engine build, hot reload and the library GET all assign it; the "no library" paths
// write zero instead of keeping a stale residual.
func markStaleAdjBasis(entries []AppliedFactorEntry) []AppliedFactorEntry {
	stale := 0
	for i := range entries {
		entries[i].StaleAdjBasis = IsAdjBasisStale(entries[i].AdjBasis)
		if entries[i].StaleAdjBasis {
			stale++
		}
	}
	metrics.SetGauge("applied_factor_stale_basis_count", int64(stale))
	return entries
}

// markStaleAdjBasisPatterns 形态侧同源处理（§ADJ-BASIS-2P）：打派生标记 + 写**自己的**量规。
// 刻意与因子侧分列两个 gauge：两侧读库时机不同（引擎按账号装配、审批热重载、战法库 GET 轮询），
// 合并成一个计数会让"后读的一侧"把另一侧的真值盖掉——那是 §DEADGAUGE 那类"指标在但值失真"的形态。
// English: pattern-side twin of markStaleAdjBasis, with its OWN gauge — merging the two counters
// would let whichever library is read last overwrite the other's true value.
func markStaleAdjBasisPatterns(entries []AppliedPatternEntry) []AppliedPatternEntry {
	stale := 0
	for i := range entries {
		entries[i].StaleAdjBasis = IsAdjBasisStale(entries[i].AdjBasis)
		if entries[i].StaleAdjBasis {
			stale++
		}
	}
	metrics.SetGauge("applied_pattern_stale_basis_count", int64(stale))
	return entries
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
	// 组装战法条目：BuyThreshold 缺省 70，其余字段直接透传候选。
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
		// §ADJ-BASIS-2 落盘盖章：这条战法的 weights/buy_threshold 是在**当前**复权口径的面板上
		// 拟合出来的。以后 store.HfqBars/RawBars 的取数语义再变（bump AdjBaselineVersion），
		// 本条目就会被载入侧判为 stale——与断点键的口径位同源，同一个常量兜住两处。
		// English: stamp the basis this rule was fitted on, so a future basis bump marks it stale.
		AdjBasis: AdjBaselineVersion,
	}
	if entry.BuyThreshold <= 0 {
		entry.BuyThreshold = 70
	}
	return appendAppliedFactor(dataDir, entry)
}

// ListAppliedFactorRules 读取战法库 applied_factors.json，返回全部已应用因子战法（含禁用）。
// 兼容旧版单对象格式（自动迁移为列表）。文件缺失返回空列表。
// §ADJ-BASIS-2：任何返回路径都会刷新 stale 标记与失效计数（含"没有库"的早退分支——不写零就是
// 拿上一轮残值冒充当前状态，§DEADGAUGE 负锁③同族）。
// English: reads the strategy library applied_factors.json and returns all applied factor strategies
// (including disabled). Migrates the legacy single-object format to a list. Missing file → empty list.
// Every return path refreshes the stale marks and the staleness gauge (including "no library").
func ListAppliedFactorRules(dataDir string) ([]AppliedFactorEntry, error) {
	if dataDir == "" {
		return markStaleAdjBasis(nil), nil
	}
	path := filepath.Join(dataDir, "applied_factors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return markStaleAdjBasis(nil), nil
		}
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return markStaleAdjBasis(nil), nil
	}
	if trimmed[0] == '[' {
		var entries []AppliedFactorEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		// §ADJ-BASIS-2 载入即打 stale 派生标记（并刷新失效计数指标）。
		return markStaleAdjBasis(entries), nil
	}
	// 旧版单对象 → 迁移为列表
	var legacy FactorRule
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, err
	}
	if len(legacy.Factors) == 0 {
		return markStaleAdjBasis(nil), nil
	}
	entry := AppliedFactorEntry{
		ID: "fac_legacy", Name: "因子战法(旧)", Enabled: true,
		Factors: legacy.Factors, Weights: legacy.Weights, Directions: legacy.Directions,
		BuyThreshold: legacy.BuyThreshold, Horizon: legacy.Horizon, IR: legacy.IR, Excess: legacy.Excess,
		AppliedAt: time.Now().Format("2006-01-02 15:04:05"),
		// AdjBasis 故意留空：旧库文件没有任何口径信息，无法证明它是在当前基线上拟合的 ⇒ 判 stale。
	}
	if entry.BuyThreshold <= 0 {
		entry.BuyThreshold = 70
	}
	_ = saveAppliedFactors(dataDir, []AppliedFactorEntry{entry}) // 落盘迁移
	return markStaleAdjBasis([]AppliedFactorEntry{entry}), nil
}

// LoadAppliedFactorRule 读取战法库中第一条**启用**的因子战法（兼容旧版单规则调用方）。
// §ADJ-BASIS-2：stale 条目是否可用与 LoadEnabledFactorRules 同判据（disable 模式下同样跳过），
// 否则 fail-close 会从这个兼容入口漏出去。
// English: loads the first **enabled** factor strategy from the library (back-compat for callers
// expecting a single rule). Returns nil when none usable under the current stale-basis policy.
func LoadAppliedFactorRule(dataDir string) (*FactorRule, error) {
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Enabled || len(e.Factors) == 0 {
			continue
		}
		if StaleAdjBasisAction() == StaleAdjBasisDisable && e.StaleAdjBasis {
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
// 这里返回通用结构。
// §ADJ-BASIS-2 基线失效战法的取舍（**唯一的实盘行为开关点**）：
//   - shadow（缺省）：stale 条目照常注入，只在战法库红标 + p1 告警——修口径这件事不该顺带把钱撤了；
//   - disable：fail-close，stale 条目不进 enabled 集合（不再产生新买入信号，已持仓的出场链不受影响）。
//     注意：owner 重跑寻优+审批后条目会带上新戳（AdjBasis=当前基线），自动回到 enabled 集合。
//
// English: returns all **enabled** factor rules for engine injection. Under the default "shadow"
// policy stale entries keep trading (mark + alert only); "disable" fail-closes them out.
func LoadEnabledFactorRules(dataDir string) ([]*factorstrat.ActiveRule, error) {
	entries, err := ListAppliedFactorRules(dataDir)
	if err != nil {
		return nil, err
	}
	failClose := StaleAdjBasisAction() == StaleAdjBasisDisable
	var out []*factorstrat.ActiveRule
	for _, e := range entries {
		if !e.Enabled || len(e.Factors) == 0 {
			continue
		}
		if failClose && e.StaleAdjBasis {
			log.Printf("[research] §ADJ-BASIS-2 战法 %s(%s) 复权基线已失效，stale_adj_basis_action=disable → 不注入实盘",
				e.ID, e.Name)
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

// saveAppliedFactors 落盘战法库（§WS-H C2 写前自动快照，支持参数回滚）。
// English: persists the strategy library (WS-H C2: pre-write snapshot for rollback).
func saveAppliedFactors(dataDir string, entries []AppliedFactorEntry) error {
	snapshotBeforeWrite(dataDir)
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return data.AtomicWrite(filepath.Join(dataDir, "applied_factors.json"), b, 0o644) // §W3-c 审批产物丢失需重跑寻优+审批
}

// AppliedPatternRule 实盘形态模板规则（F3）。复用本包 pattern.go 的 PatternCond。
// English: live pattern-template rule (F3). Reuses PatternCond from pattern.go.
type AppliedPatternRule struct {
	Name  string        `json:"name"`  // 模板名称
	Conds []PatternCond `json:"conds"` // 触发条件集
}

// AppliedPatternEntry 战法库中的一条已应用形态战法（F3 + 战法库），带独立 ID/名称/启用/来源候选/运行统计。
// English: one applied pattern strategy in the library (F3 + library), with ID/name/enabled/source/run-stats.
type AppliedPatternEntry struct {
	ID          string        `json:"id"`           // 唯一标识（"pat_<candidate_id>"/"fac_<candidate_id>"）
	Name        string        `json:"name"`         // 显示名
	Enabled     bool          `json:"enabled"`      // 是否注入 8a/8b
	CandID      int64         `json:"candidate_id"` // 来源候选 ID
	AppliedAt   string        `json:"applied_at"`   // 应用时间
	Conds       []PatternCond `json:"conds"`        // 条件集
	SignalCount int           `json:"signal_count"` // 触发信号数
	Win         int           `json:"win"`          // 盈利信号数
	Loss        int           `json:"loss"`         // 亏损信号数
	CumReturn   float64       `json:"cum_return"`   // 累计收益

	// §P2-d 规则级出场参数覆盖（扫参审批后写入；0=缺省全局默认）。形态无连续分，无门槛覆盖。
	ExitTrailPct    float64 `json:"exit_trail_pct,omitempty"`     // 移动止盈比例
	ExitStopLossPct float64 `json:"exit_stop_loss_pct,omitempty"` // 止损比例
	ExitMaxHoldDays int     `json:"exit_max_hold_days,omitempty"` // 最大持仓天数

	// §ADJ-BASIS-2P（2026-09-23）与因子侧对称的复权基线戳：**形态战法同样按 CloseHfq 打分**。
	// Conds 里的条件因子（动量/波动/价位类）全都跑在复权价上，旧条目一样是"参数缺历史依据"。
	// 空串=本字段上线前写入的旧条目 → 判 stale（不回填、不改写历史文件，只如实标注）。
	// English: pattern entries are fitted on the same adjusted-close basis, so they carry the
	// identical stamp; empty = legacy entry treated as stale.
	AdjBasis string `json:"adj_basis,omitempty"`
	// StaleAdjBasis 载入时算出的派生标记（**不落盘**）：AdjBasis != AdjBaselineVersion 即为真。
	StaleAdjBasis bool `json:"-"`
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
		// §ADJ-BASIS-2P 写入即盖章（与因子侧 ApplyFactorRule 同源）：不盖章的条目一进库就被判 stale。
		AdjBasis: AdjBaselineVersion,
	}
	return appendAppliedPattern(dataDir, entry)
}

// ListAppliedPatternRules 读取战法库 applied_patterns.json，返回全部已应用形态战法（含禁用）。
// 兼容旧版单对象格式（自动迁移为列表）。English: reads the pattern library, migrating the legacy
// single-object format to a list; returns all (including disabled).
func ListAppliedPatternRules(dataDir string) ([]AppliedPatternEntry, error) {
	if dataDir == "" {
		return markStaleAdjBasisPatterns(nil), nil
	}
	path := filepath.Join(dataDir, "applied_patterns.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return markStaleAdjBasisPatterns(nil), nil
		}
		return nil, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return markStaleAdjBasisPatterns(nil), nil
	}
	if trimmed[0] == '[' {
		var entries []AppliedPatternEntry
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, err
		}
		// §ADJ-BASIS-2P 载入即打 stale 派生标记（并刷新形态侧失效计数指标）。
		return markStaleAdjBasisPatterns(entries), nil
	}
	// 旧版单对象 → 迁移
	var legacy AppliedPatternRule
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, err
	}
	if len(legacy.Conds) == 0 {
		return markStaleAdjBasisPatterns(nil), nil
	}
	name := legacy.Name
	if name == "" {
		name = "自动形态"
	}
	// §ADJ-BASIS-2P 与因子侧同规：旧库文件没有任何口径信息，无法证明它在当前基线上拟合 ⇒ 不写戳（判 stale）。
	entry := AppliedPatternEntry{ID: "pat_legacy", Name: name, Enabled: true, Conds: legacy.Conds,
		AppliedAt: time.Now().Format("2006-01-02 15:04:05")}
	_ = saveAppliedPatterns(dataDir, []AppliedPatternEntry{entry})
	return markStaleAdjBasisPatterns([]AppliedPatternEntry{entry}), nil
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
	// §ADJ-BASIS-2P 与因子侧同一处置策略（rules.research.stale_adj_basis_action）：形态条件因子
	// 全跑在复权价上，旧口径拟合出来的阈值同样"参数缺历史依据"。缺省 shadow 只标不撤；
	// disable 时只切断**新买入信号**，名下已持仓的出场参数由 §EXIT-RETAIN 按持仓保留。
	failClose := StaleAdjBasisAction() == StaleAdjBasisDisable
	for _, e := range entries {
		if !e.Enabled || len(e.Conds) == 0 {
			continue
		}
		if failClose && e.StaleAdjBasis {
			log.Printf("[research] §ADJ-BASIS-2P 形态战法 %s(%s) 复权基线已失效，stale_adj_basis_action=disable → 不注入实盘",
				e.ID, e.Name)
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
	snapshotBeforeWrite(dataDir)
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return data.AtomicWrite(filepath.Join(dataDir, "applied_patterns.json"), b, 0o644) // §W3-c
}

// ApplyOptimizationParams 把扫参审批的参数覆盖写入指定库规则（§P2-d）。
// kind: "fac_<n>" / "pat_<n>"；params.MinScore>0 时覆盖因子规则的 buy_threshold；
// TrailPct/HoldDays>0 时写规则级出场覆盖。任一字段为 0 表示保持现状。
// English: persists sweep-approval overrides onto the library entry (factor: threshold+exits;
// pattern: exits only). Zero fields are left untouched. Hot-reload is the caller's duty.
func ApplyOptimizationParams(dataDir, kind string, takeProfitPct, stopLossPct float64, holdDays int, minScore float64) error {
	if strings.HasPrefix(kind, "fac_") {
		entries, err := ListAppliedFactorRules(dataDir)
		if err != nil {
			return err
		}
		hit := false
		for i := range entries {
			if entries[i].ID != kind {
				continue
			}
			hit = true
			if takeProfitPct > 0 {
				entries[i].ExitTrailPct = takeProfitPct
			}
			if stopLossPct > 0 {
				entries[i].ExitStopLossPct = stopLossPct
			}
			if holdDays > 0 {
				entries[i].ExitMaxHoldDays = holdDays
			}
			if minScore > 0 {
				entries[i].BuyThreshold = minScore
			}
		}
		if !hit {
			return fmt.Errorf("战法库中不存在规则 %s", kind)
		}
		return saveAppliedFactors(dataDir, entries)
	}
	if strings.HasPrefix(kind, "pat_") {
		// pattern 规则：按 ID 定位后仅覆盖传入的非零退出参数（止盈/止损/持有天数/最低分）。
		entries, err := ListAppliedPatternRules(dataDir)
		if err != nil {
			return err
		}
		hit := false
		for i := range entries {
			if entries[i].ID != kind {
				continue
			}
			hit = true
			if takeProfitPct > 0 {
				entries[i].ExitTrailPct = takeProfitPct
			}
			if stopLossPct > 0 {
				entries[i].ExitStopLossPct = stopLossPct
			}
			if holdDays > 0 {
				entries[i].ExitMaxHoldDays = holdDays
			}
		}
		if !hit {
			return fmt.Errorf("战法库中不存在规则 %s", kind)
		}
		return saveAppliedPatterns(dataDir, entries)
	}
	return fmt.Errorf("内置战法暂不支持参数入库（%s）——请在设置页调整", kind)
}
