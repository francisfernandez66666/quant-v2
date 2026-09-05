// Package combat_agent 战法引擎核心包：多策略信号执行与持仓监控。
// 本文件是 combat_agent 包的主入口文件，包含 Agent 核心结构体和主要业务逻辑。
//
// 核心功能：
//   - 多方向扫描：支持做多（ScanLong）、做空（ScanShort）、通用（Scan）三种扫描路径
//   - 持续打分：ScorePool 对打分池（持仓+自选）逐只执行四战法评分 + 动量分
//   - 持仓监控：CheckPositionAlerts 检查止盈止损条件，CheckPositionsExits 运行战法退出引擎
//   - 配置热更新：HotReload 支持策略参数热更新，无需重启进程
//
// 配套文件说明：
//   - types.go: 数据结构定义
//   - adapter.go: 数据适配层，将行情数据转换为战法输入
//   - momentum.go: 动量分计算
//   - nshape_input.go: N 形战法评分输入构造
//   - d1_scorer.go: D1 事件评分（LLM）
//   - expectation_gap.go: 预期差检测
//   - limit_up.go: 涨停龙头识别
//   - loader.go: 配置热加载
//   - runners.go: 战法运行器工厂
//   - dbwave.go: 双响炮日内状态机
//   - nwave.go: N 形日内状态机
//   - sell_side.go: 卖点评估
//   - position_exits.go: 战法退出引擎
//   - rule_exit_overrides.go: 规则级出场覆盖
//   - macro_gate.go: 宏观利空门控
package combat_agent

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/research"
	"quant-trading-v2/internal/sector_agent"
	factorstrat "quant-trading-v2/internal/strategies/factor"
	patternstrat "quant-trading-v2/internal/strategies/pattern"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// StrategyRunner 策略运行器，封装策略类型与策略实例。
// 每个运行器对应一种战法（如龙头战法、双响炮战法等），在扫描阶段按 Type 分发到真实评分逻辑。
// 运行器列表由 NewRunners 工厂函数创建，通过 SetRunners 注入 Agent。
//
// 字段说明：
//   - Type: 策略信号类型（如 SignalDragon 龙头战法），用于分发和日志标识
//   - Strategy: 策略接口实现，包含 Evaluate/GenerateSignal 等方法
type StrategyRunner struct {
	Type     strategy.SignalType // 策略信号类型（龙/双响炮/N形/龙回头）
	Strategy strategy.Strategy   // 策略接口实现
}

// orDefault 返回 a 非空时的值，否则回退到 b。
// 这是一个通用的字符串默认值辅助函数，用于处理可能为空的字符串字段。
// 参数：
//   - a: 主值，优先返回
//   - b: 默认值，当 a 为空时返回
//
// 返回值：
//   - 如果 a 非空则返回 a，否则返回 b
func orDefault(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// libraryIDFromMeta 从策略信号的 Meta["strategy_id"]（候选 ID）转换成战法库规则 ID。
// 前缀按 runner 类型区分：
//   - factor 类型 → "fac_<id>"
//   - pattern 类型 → "pat_<id>"
//
// 这个转换确保效果监测能正确归属信号到具体规则。
// 参数：
//   - sig: 策略信号，包含 Meta 信息
//   - t: 策略类型，用于确定前缀
//
// 返回值：
//   - 规则 ID 字符串（如 "fac_1"、"pat_2"）
//   - 无 strategy_id 或 ≤0 时返回空串
func libraryIDFromMeta(sig *strategy.Signal, t strategy.SignalType) string {
	if sig == nil {
		return ""
	}
	v, ok := sig.Meta["strategy_id"]
	if !ok || v <= 0 {
		return ""
	}
	prefix := "fac_"
	if t == strategy.SignalPattern {
		prefix = "pat_"
	}
	return prefix + strconv.FormatInt(int64(v), 10)
}

// strategyLabel 将战法类型转换为日志用的简称。
// 用于日志输出，简化战法类型的显示。
// 参数：
//   - t: 策略信号类型
//
// 返回值：
//   - 对应的单字符简称（龙/双/N/回/因/形）
//   - 未知类型返回原始类型字符串
func strategyLabel(t strategy.SignalType) string {
	switch t {
	case strategy.SignalDragon:
		return "龙"
	case strategy.SignalDoubleBump:
		return "双"
	case strategy.SignalNShape:
		return "N"
	case strategy.SignalDragonReturn:
		return "回"
	case strategy.SignalFactor:
		return "因"
	case strategy.SignalPattern:
		return "形"
	}
	return string(t)
}

// nShapeReason 为 N 形信号附加 D1 事件信息，使信号可读性更强。
// 将战法自身原因、LLM 的 D1 分析理由、个股关联事件名称三段按序拼接。
//
// 拼接格式：base | D1: <reason> | 事件: <eventDesc>
// 空字段会被跳过，不参与拼接。
//
// 参数：
//   - base: 战法自身原因（如 left_signal/full_chain）
//   - d1: D1 评分结果，包含 LLM 分析理由
//   - eventDesc: 个股关联事件的名称（新闻标题）
//
// 返回值：
//   - 拼接后的完整原因字符串
func nShapeReason(base string, d1 *D1Score, eventDesc string) string {
	var parts []string
	if base != "" {
		parts = append(parts, base)
	}
	if d1 != nil && d1.Reason != "" {
		parts = append(parts, "D1: "+d1.Reason)
	}
	if eventDesc != "" {
		parts = append(parts, "事件: "+eventDesc)
	}
	return strings.Join(parts, " | ")
}

// nShapeTag 将 N 形评分级别映射到信号标记（一突/二突）。
// 参数：
//   - eval: N 形评分结果，包含 Level 字段
//
// 返回值：
//   - "一突"：当 Level 为 "left_signal" 时
//   - "二突"：当 Level 为 "right_signal" 时
//   - 空字符串：其他级别
func nShapeTag(eval *strategy.Evaluation) string {
	if eval == nil {
		return ""
	}
	switch eval.Level {
	case "left_signal":
		return "一突"
	case "right_signal":
		return "二突"
	}
	return ""
}

// Agent 战法引擎核心，管理多策略运行器与配置热更新。
// 所有字段通过 mu 读写锁保护，保证并发扫描/热更新安全。
// Agent 是整个战法引擎的中枢，负责：
//   - 管理多个策略运行器（龙头/双响炮/N形/龙回头等）
//   - 处理配置热更新（策略参数、做空开关、D1 配置等）
//   - 维护日内状态机（N 形一突/二突、双响炮第二波确认）
//   - 管理动量分提升门槛（跨交易日隔离重置）
//   - 提供扫描入口（ScanLong/ScanShort/Scan/ScorePool）
//   - 检查持仓止盈止损（CheckPositionAlerts/CheckPositionsExits）
//
// 字段说明：
//   - mu: 读写锁，保护并发访问
//   - strategyCfg: 策略参数配置（含动量分权重等，可热更新）
//   - laodengCfg: Laodeng 评分配置（nil 表示未启用）
//   - runners: 多策略运行器列表（做多/通用扫描共用）
//   - shortRunner: 做空策略运行器（预留）
//   - shortEnabled: 做空功能开关（关闭时 ScanShort 直接返回 nil）
//   - positionDailyDropPct: 持仓当日跌幅提醒阈值(%)，<=0 时用默认 5
//   - waves: N 形一突/二突日内状态机（跨 5s 周期）
//   - dbwaves: 双响炮第二波日内确认状态机（跨 5s 周期）
//   - diagMu: 保护 nDiag 的并发读写
//   - nDiag: 本轮 N 形候选诊断条目
//   - momentumPrev: 动量分提升门槛，记录上一轮动量分
//   - momentumPrevDay: 动量分记录的交易日
//   - momentumPrevMu: 动量分记录的互斥锁
//   - d1Boost: D1 软加成配置（C1）
//   - atrEnabled/atrMult: C4 ATR 动态止损参数
//   - emotionBlock: C5 情绪周期禁止开仓的阶段列表
//   - depthFn: 盘口因子获取回调
type Agent struct {
	mu           sync.RWMutex           // 读写锁，保护并发访问
	strategyCfg  *config.StrategyConfig // 策略参数配置（含动量分权重等，可热更新）
	laodengCfg   *config.LaodengConfig  // Laodeng 评分配置（nil 表示未启用）
	runners      []StrategyRunner       // 多策略运行器列表（做多/通用扫描共用）
	shortRunner  StrategyRunner         // 做空策略运行器（预留）
	shortEnabled bool                   // 做空功能开关（关闭时 ScanShort 直接返回 nil）
	// positionDailyDropPct 持仓当日跌幅提醒阈值(%)；<=0 时用默认 5。
	// （positionDailyDropPct is the holding daily-drop alert threshold in percent; <=0 falls back to 5.）
	positionDailyDropPct float64

	waves   *WaveTracker       // N 形一突/二突日内状态机（跨 5s 周期）（N-shape first/second breakout intraday state machine, across 5s cycles）
	dbwaves *DoubleBumpWatcher // 双响炮第二波日内确认状态机（跨 5s 周期）（Double-Bump second-wave intraday confirmation machine, across 5s cycles）
	diagMu  sync.Mutex         // 保护 nDiag 的并发读写（guards concurrent reads/writes of nDiag）
	nDiag   []NDiag            // 本轮 N 形候选诊断条目（engine 每轮 DrainNDiag 收口）（this round's N-shape candidate diagnostics, drained each round）

	// 动量分提升门槛：每只股票记录上一轮动量分，仅当"提升/未明显回落"时才视为有实质改善。
	// 跨交易日隔离重置，避免把前一日高动量带到今天。
	// English: momentum-gate delta tracking — the prior-round momentum score per code; only a rise (or a
	// fall within tolerance) counts as a meaningful improvement. Reset across trading days.
	momentumPrev    map[string]float64
	momentumPrevDay string
	momentumPrevMu  sync.Mutex

	// d1Boost D1 软加成配置（C1）：BoostWeight>0 时对非 N 战法总分做加成；负面 blocked 硬 veto。
	// 由 Engine 在构建/热更时注入（见 SetD1Config），零值视为未启用。
	// English: C1 D1 soft-boost settings — when BoostWeight>0, non-N strategy totals get boosted;
	// a blocked D1 hard-vetoes the stock. Injected by the Engine (SetD1Config); zero = disabled.
	d1Boost config.D1Config

	// atrStop C4 ATR 动态止损参数：atrEnabled 开关 + atrMult 倍数（止损距离 = atrMult×ATR）。
	// 由 Engine/loader 注入（见 SetATRStop），未注入时回退固定百分比止损。
	// English: C4 ATR dynamic-stop params — atrEnabled switch + atrMult multiplier (stop distance =
	// atrMult×ATR). Injected by the Engine/loader (SetATRStop); unset falls back to fixed percent.
	atrEnabled bool
	atrMult    float64

	// emotionBlock C5 情绪周期禁止开仓的阶段列表（见 SetEmotionBlockPhases）；空时回退默认 ["衰退"]。
	// English: C5 emotion phases that forbid buying (SetEmotionBlockPhases); empty falls back to ["衰退"].
	emotionBlock []string

	// depthFn 盘口因子获取回调（由 Server/Engine 注入，nil 表示不拉取）。
	// 信号生成后对通过战法的个股拉取一次盘口因子（买卖压力/封单量），供战法与前端共同使用。
	// English: order-book factor fetcher injected by Server/Engine (nil disables). After signal
	// generation, per-signal depth factors (bid/ask pressure, seal volume) are fetched once for
	// strategies and the frontend alike.
	depthFn func(code string) *data.OrderBookFactors
}

// New 创建战法引擎实例。
// 初始化 Agent 结构体，创建空的运行器列表和状态机容器。
// 参数：
//   - cfg: 策略参数配置，包含动量分权重、止盈止损参数等
//
// 返回值：
//   - 初始化后的 Agent 指针
func New(cfg *config.StrategyConfig) *Agent {
	return &Agent{
		strategyCfg:  cfg,
		runners:      make([]StrategyRunner, 0),
		waves:        NewWaveTracker(),
		dbwaves:      NewDoubleBumpWatcher(),
		momentumPrev: make(map[string]float64),
	}
}

// SetDepthFactorFn 注入盘口因子获取回调（nil 禁用）。
// 信号生成后对通过战法的个股拉取一次盘口因子（买卖压力/封单量），供战法与前端共同使用。
// 参数：
//   - fn: 盘口因子获取函数，接收股票代码，返回盘口因子
func (a *Agent) SetDepthFactorFn(fn func(code string) *data.OrderBookFactors) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.depthFn = fn
}

// attachDepthFactors 对信号列表批量附加盘口因子（并发拉取）。
// 回调未注入或拉取失败时信号保留零值因子，战法/前端均须容忍缺失。
// 优化：同一代码只拉一次（同一轮多战法命中同股共用因子），避免重复请求。
//
// 参数：
//   - signals: 信号列表，将被原地修改以附加盘口因子
func (a *Agent) attachDepthFactors(signals []Signal) {
	if len(signals) == 0 {
		return
	}
	a.mu.RLock()
	fn := a.depthFn
	a.mu.RUnlock()
	if fn == nil {
		return
	}
	// 同一代码只拉一次（同一轮多战法命中同股共用因子）
	// English: fetch once per code (signals of the same stock share the factor).
	seen := make(map[string]int)
	var idx []int
	for i, s := range signals {
		if s.Code == "" {
			continue
		}
		if _, ok := seen[s.Code]; !ok {
			seen[s.Code] = i
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	// res 并发盘口拉取结果：下标 + 盘口因子。
	type res struct {
		i int
		f *data.OrderBookFactors
	}
	ch := make(chan res, len(idx))
	var wg sync.WaitGroup
	for _, i := range idx {
		code := signals[i].Code
		wg.Add(1)
		go func(i int, c string) {
			defer wg.Done()
			ch <- res{i, fn(c)}
		}(i, code)
	}
	wg.Wait()
	close(ch)
	for r := range ch {
		if r.f != nil {
			signals[r.i].DepthFactors = r.f
		}
	}
}

// DrainNDiag 收口并清空本轮 N 形诊断条目（engine 每轮打分后调用并打印）。
// 返回值：
//   - 本轮所有 N 形候选诊断条目列表
func (a *Agent) DrainNDiag() []NDiag {
	a.diagMu.Lock()
	defer a.diagMu.Unlock()
	out := a.nDiag
	a.nDiag = nil
	return out
}

// recordNDiag 追加一条 N 形候选诊断（仅 N 形战法路径调用）。
// 参数：
//   - d: N 形诊断条目，包含代码、评分、拦截原因等信息
func (a *Agent) recordNDiag(d NDiag) {
	a.diagMu.Lock()
	defer a.diagMu.Unlock()
	a.nDiag = append(a.nDiag, d)
}

// SetLaodengConfig 设置 Laodeng 评分配置（线程安全）。
// Laodeng 评分用于对信号置信度进行修正，基于市盈率、换手率、板块等因素。
// 参数：
//   - cfg: Laodeng 评分配置，nil 表示禁用
func (a *Agent) SetLaodengConfig(cfg *config.LaodengConfig) {
	a.mu.Lock()
	a.laodengCfg = cfg
	a.mu.Unlock()
}

// SetD1Config 注入 D1 软加成配置（C1）：负面硬 veto + 高分软加成（线程安全）。
// 当 D1 分达到加成门槛时，对非 N 战法总分进行软加成，让强事件股越过买入门槛。
// 负面 D1（Blocked）会硬 veto 该股票。
// 参数：
//   - cfg: D1 配置，包含 BoostWeight、BoostThreshold 等参数
func (a *Agent) SetD1Config(cfg *config.D1Config) {
	if cfg == nil {
		return
	}
	a.mu.Lock()
	a.d1Boost = *cfg
	a.mu.Unlock()
}

// SetATRStop 注入 C4 ATR 动态止损参数（线程安全）。
// enabled=false 时回退固定百分比止损，enabled=true 时使用 ATR 动态止损。
// 参数：
//   - enabled: 是否启用 ATR 动态止损
//   - mult: ATR 倍数（止损距离 = atrMult × ATR）
func (a *Agent) SetATRStop(enabled bool, mult float64) {
	a.mu.Lock()
	a.atrEnabled = enabled
	a.atrMult = mult
	a.mu.Unlock()
}

// atrStopParams 读取 ATR 动态止损参数（线程安全）。
// 返回值：
//   - enabled: 是否启用 ATR 动态止损
//   - mult: ATR 倍数，未启用时为 0（回退固定百分比）
func (a *Agent) atrStopParams() (enabled bool, mult float64) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.atrEnabled, a.atrMult
}

// SetEmotionBlockPhases 注入 C5 情绪周期禁止开仓阶段列表（线程安全；nil 回退默认 ["衰退"]）。
// 当情绪阶段处于禁止列表时，买入信号会降级为 watch（仅观察不交易）。
// 参数：
//   - phases: 禁止开仓的情绪阶段列表，nil 或空时回退默认 ["衰退"]
func (a *Agent) SetEmotionBlockPhases(phases []string) {
	a.mu.Lock()
	a.emotionBlock = phases
	a.mu.Unlock()
}

// emotionBlocksBuy 判断某情绪阶段是否禁止开仓（C5）。
// 空配置回退默认仅"衰退"阶段禁止开仓。
// 参数：
//   - phase: 当前情绪阶段
//
// 返回值：
//   - true: 该阶段禁止开仓
//   - false: 该阶段允许开仓
func (a *Agent) emotionBlocksBuy(phase string) bool {
	if phase == "" {
		return false
	}
	a.mu.RLock()
	phases := a.emotionBlock
	a.mu.RUnlock()
	if len(phases) == 0 {
		phases = []string{"衰退"}
	}
	for _, p := range phases {
		if p == phase {
			return true
		}
	}
	return false
}

// SetRunners 设置策略运行器列表（线程安全）。
// 运行器列表由 NewRunners 工厂函数创建，通过此方法注入 Agent。
// 参数：
//   - runners: 策略运行器列表，包含龙头/双响炮/N形/龙回头等战法
func (a *Agent) SetRunners(runners []StrategyRunner) {
	a.mu.Lock()
	a.runners = runners
	a.mu.Unlock()
}

// ReloadFactorRules 从战法库 applied_factors.json 重载全部启用规则并注入因子 runner（热生效）。
// 供战法库的启用/禁用/删除/新增审批后调用，无需重启。
// 参数：
//   - dataDir: 数据目录路径，包含 applied_factors.json 文件
//
// 注意：若因子 runner 不存在则忽略，不会影响其他战法。
func (a *Agent) ReloadFactorRules(dataDir string) {
	rules, err := research.LoadEnabledFactorRules(dataDir)
	if err != nil {
		log.Printf("[combat_agent] 重载因子战法库失败: %v", err)
		return
	}
	a.mu.Lock()
	for i := range a.runners {
		if fs, ok := a.runners[i].Strategy.(*factorstrat.FactorStrategy); ok {
			fs.SetRules(rules)
			log.Printf("[combat_agent] 因子战法库已热重载: %d 条规则", len(rules))
		}
	}
	a.mu.Unlock()
	a.refreshExitOverrides(dataDir) // §P2-d：同步重建规则级出场覆盖（含形态库，避免单侧刷新互相覆盖）
}

// FactorStats 返回因子 runner 的各规则运行统计（效果监测）。
// 返回值：
//   - 因子规则运行统计列表，包含规则 ID、触发次数、胜率等信息
func (a *Agent) FactorStats() []factorstrat.ActiveRule {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.runners {
		if fs, ok := a.runners[i].Strategy.(*factorstrat.FactorStrategy); ok {
			return fs.Stats()
		}
	}
	return nil
}

// RecordFactorForwardReturn 记录某条因子规则一条触发股的 Horizon 日前向收益（效果监测）。
// 用于评估因子规则的实际收益表现。
// 参数：
//   - ruleID: 因子规则 ID
//   - ret: Horizon 日前向收益率
func (a *Agent) RecordFactorForwardReturn(ruleID string, ret float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.runners {
		if fs, ok := a.runners[i].Strategy.(*factorstrat.FactorStrategy); ok {
			fs.RecordForwardReturn(ruleID, ret)
		}
	}
}

// ReloadPatternRules 从形态战法库 applied_patterns.json 重载全部启用规则并注入形态 runner（热生效）。
// 参数：
//   - dataDir: 数据目录路径，包含 applied_patterns.json 文件
func (a *Agent) ReloadPatternRules(dataDir string) {
	rules, err := research.LoadEnabledPatternRules(dataDir)
	if err != nil {
		log.Printf("[combat_agent] 重载形态战法库失败: %v", err)
		return
	}
	a.mu.Lock()
	for i := range a.runners {
		if ps, ok := a.runners[i].Strategy.(*patternstrat.PatternStrategy); ok {
			ps.SetRules(rules)
			log.Printf("[combat_agent] 形态战法库已热重载: %d 条规则", len(rules))
		}
	}
	a.mu.Unlock()
	a.refreshExitOverrides(dataDir)
}

// refreshExitOverrides 重建规则级出场覆盖注册表（§P2-d 实盘接线）。
// 因子+形态两库合并刷新——注册表是全局单份，单库刷新必须双读，否则会清掉另一侧的覆盖。
// 参数：
//   - dataDir: 数据目录路径
func (a *Agent) refreshExitOverrides(dataDir string) {
	fe, e1 := research.ListAppliedFactorRules(dataDir)
	pe, e2 := research.ListAppliedPatternRules(dataDir)
	if e1 != nil && e2 != nil {
		log.Printf("[combat_agent] 出场覆盖注册表刷新失败: %v / %v", e1, e2)
		return
	}
	if fe == nil {
		fe = []research.AppliedFactorEntry{}
	}
	if pe == nil {
		pe = []research.AppliedPatternEntry{}
	}
	SetRuleExitOverrides(fe, pe)
	n := len(fe) + len(pe)
	log.Printf("[combat_agent] 出场覆盖注册表已同步: 因子 %d 条 / 形态 %d 条", len(fe), len(pe))
	_ = n
}

// PatternStats 返回形态 runner 的各规则运行统计（效果监测）。
// 返回值：
//   - 形态规则运行统计列表，包含规则 ID、触发次数、胜率等信息
func (a *Agent) PatternStats() []patternstrat.ActivePattern {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.runners {
		if ps, ok := a.runners[i].Strategy.(*patternstrat.PatternStrategy); ok {
			return ps.Stats()
		}
	}
	return nil
}

// RecordPatternForwardReturn 记录某条形态规则一条触发股的 Horizon 日前向收益（效果监测）。
// 参数：
//   - ruleID: 形态规则 ID
//   - ret: Horizon 日前向收益率
func (a *Agent) RecordPatternForwardReturn(ruleID string, ret float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.runners {
		if ps, ok := a.runners[i].Strategy.(*patternstrat.PatternStrategy); ok {
			ps.RecordForwardReturn(ruleID, ret)
		}
	}
}

// SetShortEnabled 设置做空开关（线程安全）。
// 关闭时 ScanShort 直接返回 nil，开启时执行做空扫描。
// 参数：
//   - enabled: 是否启用做空功能
func (a *Agent) SetShortEnabled(enabled bool) {
	a.mu.Lock()
	a.shortEnabled = enabled
	a.mu.Unlock()
}

// SetPositionDailyDropPct 设置持仓当日跌幅提醒阈值(%)（线程安全，<=0 用默认 5）。
// 当持仓当日跌幅超过该阈值时，会产生跌幅提醒信号。
// 参数：
//   - pct: 跌幅阈值百分比，<=0 时使用默认值 5
func (a *Agent) SetPositionDailyDropPct(pct float64) {
	a.mu.Lock()
	a.positionDailyDropPct = pct
	a.mu.Unlock()
}

// PositionDailyDropPct 返回持仓当日跌幅提醒阈值(%)（线程安全；<=0 表示使用默认 5）。
// 返回值：
//   - 当前的跌幅提醒阈值百分比
func (a *Agent) PositionDailyDropPct() float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.positionDailyDropPct
}

// ShortEnabled 返回当前做空是否启用（线程安全）。
// 返回值：
//   - true: 做空功能已启用
//   - false: 做空功能已禁用
func (a *Agent) ShortEnabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.shortEnabled
}

// HotReload 热更新策略参数（线程安全）。
// 当配置文件变更时，由 loader.go 调用此方法更新策略参数，无需重启进程。
// 参数：
//   - newCfg: 新的策略参数配置
func (a *Agent) HotReload(newCfg *config.StrategyConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.strategyCfg = newCfg
	log.Printf("[combat_agent] 策略参数已热更新")
}

// seqID 生成全局唯一信号 ID，格式：SIG + 纳秒时间戳。
// 返回值：
//   - 全局唯一信号 ID 字符串
func seqID() string {
	return fmt.Sprintf("SIG%d", time.Now().UnixNano())
}

// applyLaodengToSignals §R4-5 修复：对单只股票的信号应用 Laodeng 置信度修正（上限 1.0）。
// 旧实现对全量信号喂硬编码模拟值，不具备任何选股信息。现接真实数据逐股计算：
//   - pe: 引擎打分池预取的市盈率
//   - turnover: 实时快照换手率
//   - sector: 真实板块名或 "个股"
//   - 市值: 数据源缺失传 0，ScoreLaodeng 跳过该维度并归一化剩余权重
//
// 未配置/未启用/无信号时原地不动。
//
// 参数：
//   - sigs: 信号列表，将被原地修改置信度
//   - md: 行情数据快照
//   - pe: 个股动态市盈率
//   - sectorName: 板块名称
func (a *Agent) applyLaodengToSignals(sigs []Signal, md *strategy_engine.StockMarketData, pe float64, sectorName string) {
	a.mu.RLock()
	cfg := a.laodengCfg
	a.mu.RUnlock()
	if cfg == nil || !cfg.Enabled || len(sigs) == 0 {
		return
	}
	turnover := 0.0
	if md != nil && md.Quote != nil {
		turnover = md.Quote.Turnover
	}
	laodengScore := strategy.ScoreLaodeng(cfg, 0, pe, turnover, sectorName)
	if laodengScore <= 0 {
		return
	}
	for i := range sigs {
		sigs[i].Confidence *= (1 + laodengScore)
		// 置信度封顶 1.0，避免溢出
		if sigs[i].Confidence > 1 {
			sigs[i].Confidence = 1
		}
	}
}

// ScanLong 执行做多扫描：7a 板块利好→验证后个股→8a；8a 个股利好→直入战法。
// 返回做多信号列表，若输入的板块/个股为空或无可运行策略则返回 nil。
//
// 扫描流程：
//  1. 遍历已验证的利好板块，对板块内每只个股执行评分
//  2. 遍历个股直入列表，对每只个股执行评分
//  3. 对最终信号批量附加盘口因子
//
// 参数：
//   - input: 扫描输入，包含已验证板块、行情数据、D1评分等信息
//
// 返回值：
//   - 做多信号列表，为空时表示无信号
func (a *Agent) ScanLong(input ScanInput) []Signal {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()

	// 无可运行策略，或板块/个股输入均为空 → 无扫描对象，直接返回
	// English: no runner configured or both sector/stock inputs empty → nothing to scan.
	if len(runners) == 0 || (len(input.Sectors) == 0 && len(input.IndividualStocks) == 0) {
		return nil
	}

	var raw []Signal
	now := time.Now()

	// 7a 板块利好 → 验证后的个股走 8a（同时记录持续打分）
	// English: verified bull sectors feed their stocks through 8a (also recording persistent scores).
	for _, sector := range input.Sectors {
		// 仅处理方向为"利好"的板块（利空走做空路径 ScanShort）
		// English: only bull-direction sectors are handled (bears go through ScanShort).
		if sector.Direction != "利好" {
			continue
		}
		for _, code := range sector.Stocks {
			// L1 过滤阻塞的个股跳过，不再进入战法评分
			// English: skip stocks blocked by the L1 filter.
			if input.L1Blocked[code] {
				continue
			}
			raw = append(raw, a.evalAll(&input, runners, code, input.MarketData[code], &sector, "做多", sector.Name, now)...)
		}
	}

	// 8a 个股利好 → 直入战法（同时记录自选/持仓持续打分）
	// 个股直入场景无板块上下文，sector 传 nil 交给战法自行降级处理
	// English: 8a direct stock inputs go straight to the strategies; no sector context, nil sector lets
	// strategies degrade gracefully.
	for _, code := range input.IndividualStocks {
		if input.L1Blocked[code] {
			continue
		}
		raw = append(raw, a.evalAll(&input, runners, code, input.MarketData[code], nil, "做多", "个股", now)...)
	}

	// Laodeng 置信度修正已下沉到 evalAll 内按真实数据逐股应用（§R4-5），此处不再统一套常数
	signals := raw
	// 对最终信号批量附加盘口因子（买卖压力/封单量，供战法与前端使用）
	// English: attach order-book factors (bid/ask pressure & seal volume) to final signals.
	a.attachDepthFactors(signals)
	log.Printf("[combat_agent] ScanLong: %d 板块 %d 个股 → %d 做多信号", len(input.Sectors), len(input.IndividualStocks), len(signals))
	return signals
}

// evalAll 对单只股票跑全部战法评分：无论 Pass 与否都记录原始分到 input.Scores
// （8a/8b 持续打分），只对通过的战法生成信号并返回。这是 8a/8b 打分与信号输出的统一入口。
//
// 核心流程：
//  1. ST 个股屏蔽：名称以 ST/*ST/S*ST/退 开头的股票不产生任何信号
//  2. 提取 D1 评分、事件描述、PE 等共享数据
//  3. 并发调用各战法评分（龙头/双响炮/N形/龙回头）
//  4. 对通过的战法生成信号，未通过的记录原因
//  5. 处理 N 形一突/二突日内状态机
//  6. 处理双响炮第二波日内确认
//  7. 应用动量分提升门槛
//  8. 应用情绪周期过滤
//  9. 应用宏观利空门控
//  10. 附加盘口因子
//  11. 应用 Laodeng 置信度修正
//
// 参数：
//   - input: 扫描输入（包含行情数据、D1评分等）
//   - runners: 策略运行器列表
//   - code: 股票代码
//   - md: 行情数据快照
//   - sector: 板块上下文（nil 表示个股直入）
//   - direction: 方向（做多/做空）
//   - sectorName: 板块名称
//   - now: 当前时间（统一信号生成时间）
//
// 返回值：
//   - 该股票本轮生成的信号列表
func (a *Agent) evalAll(input *ScanInput, runners []StrategyRunner, code string, md *strategy_engine.StockMarketData, sector *sector_agent.VerifiedSector, direction, sectorName string, now time.Time) []Signal {
	// 无可运行策略或行情数据缺失 → 无法评分
	// English: no runner or missing market data → cannot score.
	if len(runners) == 0 || md == nil {
		return nil
	}
	// ST 个股屏蔽：名称以 ST/*ST/S*ST/退 开头（含风险警示/退市整理）的股票不产生任何信号，
	// 评分本身仍写入 StockScores（8a/8b 打分量保留，但不出交易/提醒信号）。
	// English: block ST-listed stocks — names prefixed with ST/*ST/S*ST/退 (risk-warning or delisting)
	// produce no signals; their scores are still recorded in StockScores but never surface as signals.
	if IsSTStock(md.Name) {
		if input.Scores == nil {
			input.Scores = make(map[string]StockScores)
		}
		sc := StockScores{Code: code, DataGaps: make(map[string]bool)}
		sc.UpdatedAt = now
		input.Scores[code] = sc
		log.Printf("[combat_agent] 跳过 ST 个股 %s(%s): 不产生信号", code, md.Name)
		return nil
	}
	// 惰性初始化打分输出表，避免 nil map 写入 panic
	// English: lazily initialize the score output map to avoid nil-map writes.
	if input.Scores == nil {
		input.Scores = make(map[string]StockScores)
	}
	sc := StockScores{Code: code, DataGaps: make(map[string]bool)}
	// 提取 N 形战法消费的 D1 评分 / 事件描述 / PE（仅一次，供各战法共享）
	// English: fetch D1 score / event description / PE for the N-shape strategy (once, shared by all).
	var d1 *D1Score
	if ds, ok := input.D1Scores[code]; ok {
		d1 = &ds
	}
	// §W5-v3 LLM 解耦（owner 口径）：负面过滤不再硬拦截任何战法的信号产生——
	// LLM 打分对非 N 战法只做参考标注；Blocked 个股的信号照常产出，
	// 在 evalAll 收尾统一追加 "⚠️LLM利空提示" 到 Reason，由执行层利空冷却守卫把关真金下单。
	// N 形自身的 D1 硬闸不受影响（scorer 内部实现）。此处仅保留打分占位与日志。
	// English: §W5-v3 LLM decoupling — the negative filter no longer vetoes signal generation for
	// non-N strategies; blocked stocks still produce signals annotated with an LLM-caution suffix,
	// while real-money protection moves to the execution-layer fresh-bearish guard.
	llmBlocked := d1 != nil && d1.Blocked
	if llmBlocked {
		if input.Scores == nil {
			input.Scores = make(map[string]StockScores)
		}
		scb := StockScores{Code: code, DataGaps: make(map[string]bool), UpdatedAt: now}
		input.Scores[code] = scb
		log.Printf("[combat_agent] D1 负面提示(不拦截) %s: %s", code, d1.Reason)
	}
	eventDesc := strings.Join(newsTitlesOf(input.News, code), "；")
	// PE 由上层 Engine 预取填充（input.PE 为空表示该股无 PE，N 形 D3 走斐波那契兜底）
	// English: PE is prefetched by the upper Engine; empty means no PE, D3 falls back to Fibonacci.
	var pe float64
	if input.PE != nil {
		pe = input.PE[code]
	}
	var sigs []Signal
	var unSig []string // 未出信号战法的原因（诊断：为何非龙头战法不出信号）

	// 动量分单独计算（量价+MACD+走势），作为 8a/8b 打分量的一部分；
	// 提前到循环前算出，供"动量提升才提醒"门槛对 double_bump/龙头/龙回头 逐战法放行判断。
	// English: momentum score (volume-price + MACD + trend) computed up front so the "improvement-only"
	// momentum gate can be applied per strategy (double-bump / dragon / dragon-return) inside the loop.
	momentumScore := MomentumScore(md, a.momentumWeights())
	momentumValid := momentumDataValid(md)
	// 注意：本轮动量分在循环结束后才 record（见函数末尾），先以"上一轮"历史值做提升门槛比较，
	// 再写入本轮新值供下一轮比较。若在循环前提前覆盖，会丢失上一轮基准导致门槛失效。
	// English: this round's momentum is only recorded at the end of evalAll (see below), so the
	// improvement gate first compares against the *previous* baseline, then stores this round's value for
	// next round. Recording it earlier would overwrite the baseline before comparison and break the gate.

	// 战法评分并发化：同一只股票的各战法评分彼此独立（无共享可变状态），
	// 并发调用 evalFor 后按原 runner 顺序合并处理，保证信号顺序与波状态机确定性。
	// English: each strategy's scoring on one stock is independent, so scoring is parallelized and results
	// are merged in runner order to keep signal order and wave-state-machine determinism.
	type evalResult struct {
		eval *strategy.Evaluation
		err  error
	}
	n := len(runners)
	res := make([]evalResult, n)
	var wg sync.WaitGroup
	for i, runner := range runners {
		if runner.Strategy == nil {
			continue
		}
		wg.Add(1)
		go func(i int, r StrategyRunner) {
			defer wg.Done()
			ev, err := evalFor(r, code, md, sector, input.EmotionPhase, d1, eventDesc, pe)
			res[i] = evalResult{eval: ev, err: err}
		}(i, runner)
	}
	wg.Wait()

	for i, runner := range runners {
		// 策略实例为空则跳过该运行器
		// English: skip runners without a strategy instance.
		if runner.Strategy == nil {
			continue
		}
		// 按战法类型分发到真实评分逻辑（adapter.go evalFor，并发结果按序取用）
		// English: dispatch to the real scoring logic (evalFor in adapter.go), consuming concurrent
		// results in runner order.
		eval, err := res[i].eval, res[i].err
		// 评分失败或返回空结果 → 该战法视为 0 分，不产出信号；同时标记数据缺口
		// English: scoring error or empty result means 0 score, no signal, and a data-gap marker.
		if err != nil || eval == nil {
			markDataGap(&sc, runner.Type, md)
			if err != nil {
				unSig = append(unSig, strategyLabel(runner.Type)+":错误("+err.Error()+")")
			} else {
				unSig = append(unSig, strategyLabel(runner.Type)+":无结果")
			}
			continue
		}
		// 战法各自数据不满足硬性门槛时（如 K 线不足被降级为 0）标记缺口
		// English: mark a gap when a strategy's hard data requirement is unmet (e.g. insufficient K-lines).
		if eval.TotalScore == 0 && strategyDataInsufficient(runner.Type, md) {
			markDataGap(&sc, runner.Type, md)
		}
		// 按战法类型归档原始总分到 StockScores（前端展示用，即使未通过也记录）
		// English: archive the raw total score per strategy type (for display, even when not passed).
		switch runner.Type {
		case strategy.SignalNShape:
			sc.NScore = eval.TotalScore
		case strategy.SignalDragon:
			sc.DragonScore = eval.TotalScore
		case strategy.SignalDoubleBump:
			sc.DoubleBumpScore = eval.TotalScore
		case strategy.SignalDragonReturn:
			sc.DragonReturnScore = eval.TotalScore
		}
		// C1 D1 软加成：对 非N 战法（龙头/双响炮/龙回头），当 D1 分达到加成门槛时，
		// 总分 ×(1+BoostWeight×D1/40)（封顶 100）并重判 pass/level，让强事件股越过买入门槛；
		// N 形已有自身 D1 硬闸，不再叠加。负数/未启用时保持不变。
		// English: C1 D1 soft boost — for non-N strategies, a D1 score above the boost threshold
		// multiplies the total by (1+BoostWeight×D1/40), capped at 100, then re-derives pass/level so a
		// strong-event stock clears its buy gate. N-shape already has its own D1 hard gate and is skipped.
		if runner.Type != strategy.SignalNShape && d1 != nil && !d1.Blocked {
			a.applyD1Boost(runner.Type, eval, d1)
		}
		// N 形候选：推进一突/二突日内状态机，并尊重 D 硬闸 + 总分门槛。
		// 一突/二突标记需 d1>0 且 总分≥60（与 full_chain 的 Valid 硬闸一致）才 Pass；
		// 否则即使波形确认也不发信号（避免光迅这类 d1=4、total=26 的低分被强制推荐）。
		// English: for N-shape candidates advance the intraday left/right breakout state machine while
		// respecting the D1 hard-gate AND the total-score gate — both labels need d1>0 and total ≥60 to
		// Pass (matching full_chain's Valid gate); otherwise no signal fires even with wave confirmation
		// (prevents low-score stocks like total=26 from being force-recommended).
		if runner.Type == strategy.SignalNShape && eval != nil {
			left, right := a.waves.Eval(code, md)
			d1 := 0.0
			if v, ok := eval.Details["d1"]; ok {
				d1 = v
			}
			// 与 N 形 Valid 硬闸保持一致：总分≥60 且 D1>0 才可发信号
			// English: consistent with the N-shape Valid gate — total ≥60 and D1>0 required.
			minTotal := 60.0
			totalOK := eval.TotalScore >= minTotal
			tag := ""
			switch {
			case right && d1 > 0 && totalOK:
				eval.Level = "right_signal"
				eval.Pass = true
				eval.Details["right_signal"] = 1
				tag = "二突"
			case left && d1 > 0 && totalOK:
				eval.Level = "left_signal"
				eval.Pass = true
				eval.Details["left_signal"] = 1
				tag = "一突"
			}
			reason := eval.Level
			if d1 <= 0 {
				reason = "d1=0"
			} else if !totalOK {
				reason = "total_below"
			} else if !eval.Pass {
				reason = "wave_not_confirmed"
			}
			a.recordNDiag(NDiag{Code: code, Name: md.Name, D1: d1, Total: eval.TotalScore,
				Level: eval.Level, Tag: tag, Pass: eval.Pass, Reason: reason})
		}
		// 未通过战法硬性/评分门槛；二突/一突已在上面被提为 Pass → 只记分不出信号
		// English: strategy below hard/score gates (left/right already promoted to Pass above) →
		// record score only, no signal.
		if !eval.Pass {
			unSig = append(unSig, fmt.Sprintf("%s:%s(%.0f)", strategyLabel(runner.Type), eval.Level, eval.TotalScore))
			continue
		}
		// 双响炮第二波日内确认（叠加在 volScore>0 硬闸之上）：即便日K评分已满 volScore，
		// 也要等日内状态机推进到第二波突破（PhaseSecond）才放行买入；未到第二波则记分并提示"待二波"。
		// 竞价/盘前（Volume=0）无真实成交，状态机不推进 → 不会误放行假双凸。
		// English: Double-Bump second-wave intraday confirmation stacked on the volScore>0 hard gate —
		// even when the daily-bar score already has volScore, the buy only fires once the intraday state
		// machine reaches the second breakout (PhaseSecond); otherwise record the score and note "待二波".
		// Pre-open (Volume=0) has no real trades, so the machine won't advance and can't falsely confirm.
		if runner.Type == strategy.SignalDoubleBump {
			if !mdEmpty(md) {
				dbc := a.doubleBumpConfig()
				if !a.dbwaves.Confirm(code, md, dbc) {
					unSig = append(unSig, strategyLabel(runner.Type)+":待二波")
					continue
				}
			}
		}
		// 动量分"提升才提醒"门槛：仅套用 非N形 战法（double_bump/龙头/龙回头），N 形不套用。
		// 开启时，动量分未提升/明显回落（且数据有效）则拦截该战法信号，避免反复刷同一条提醒。
		// English: the momentum "improvement-only" gate applies only to non-N strategies (double-bump /
		// dragon / dragon-return); N-shape is exempt. When enabled and momentum hasn't improved (with valid
		// data), the strategy signal is withheld to avoid re-flooding the same alert.
		if a.momentumGateEnabled() && runner.Type != strategy.SignalNShape {
			if !a.momentumImproved(code, momentumScore, momentumValid) {
				unSig = append(unSig, strategyLabel(runner.Type)+":动量未提升")
				continue
			}
		}
		// 通过的战法生成交易信号，失败或为空则跳过
		// English: passed strategies generate a trade signal; skip on failure or empty result.
		sig, err := runner.Strategy.GenerateSignal(code, eval)
		if err != nil || sig == nil {
			unSig = append(unSig, strategyLabel(runner.Type)+":信号生成失败")
			continue
		}
		// 操作类型缺省为 watch（仅观察），避免空 action
		// English: default action to watch (observation only) to avoid an empty action.
		action := string(sig.Action)
		if action == "" {
			action = "watch"
		}
		// C5 情绪周期过滤扩展到四战法：当前阶段禁止开仓时（如"衰退"），
		// 买入信号一律降级为 watch 观察（仅提醒不交易），与 N 形既有情绪硬闸口径一致。
		// English: C5 extends the emotion-cycle filter to all four strategies — under a block-buy phase
		// (e.g. 衰退), every buy signal downgrades to watch (alert-only), consistent with N-shape's gate.
		if input.EmotionPhase != "" && (action == "买入" || action == "buy") && a.emotionBlocksBuy(input.EmotionPhase) {
			action = "watch"
			sigReason := sig.Reason
			sigReason = "情绪[" + input.EmotionPhase + "]禁止开仓: " + sigReason
			sig.Reason = sigReason
		}
		// B2 式固化可撤销修正：策略 GenerateSignal 常不填触发价（Price=0），导致上层
		// invalidateBrokenSignals 因"触发价无效"跳过，固化信号跌破现价也不会被撤销。
		// 这里用现价 md.Price 兜底触发价，使"现价跌破触发价→撤销固化"的校验真正生效。
		// English: fix for pinned-signal invalidation — GenerateSignal often leaves the trigger price
		// (Price) as 0, so invalidateBrokenSignals skips it and a pinned buy wouldn't be revoked when the
		// price breaks below. Falling back to the live price md.Price makes the "below-trigger -> revoke" check work.
		sigPrice := sig.Price
		if sigPrice <= 0 {
			sigPrice = md.Price
		}
		sigReason := sig.Reason
		if runner.Type == strategy.SignalNShape {
			// N 形信号附上 D1 事件信息（LLM 理由 + 事件名称），见 nShapeReason
			// English: N-shape signals carry the D1 event info (LLM reason + event name), see nShapeReason.
			sigReason = nShapeReason(sigReason, d1, eventDesc)
			// B2 盘中确认门：纯 full_chain（形态 Pass，无 一突/二突 波形确认）要求盘中真实成交
			// （今日成交量 Volume>0）才视为可操作买入；竞价/盘前成交量=0（无真实成交）时
			// 降级为 watch 观察，杜绝基于竞价/存量的"假买入信号"，等 9:30 实盘放量后再由
			// 状态机/下一轮评分确认升级。已提升为 left_signal/right_signal 的信号不受门控。
			// English: B2 intraday-confirmation gate — a pure full_chain (pattern Pass with no 一突/二突
			// wave confirmation) is only an actionable buy once real intraday volume exists (Volume>0).
			// Pre-open/call-auction volume is 0 (no real trades), so it degrades to watch, blocking
			// false buys from auction/stale data; signals promoted to left/right are not gated.
			if eval.Level == "full_chain" && (md == nil || md.Quote == nil || md.Quote.Volume <= 0) {
				action = "watch"
				sigReason = "待盘中确认: " + sigReason
			}
		}
		// §C 规则细分池：库规则信号（factor/pattern runner）的 StrategyID 即模拟盘
		// 资金池 key（fac_1/pat_2 各自独立分仓与纪律）；StrategyType 同步改填规则 ID，
		// paper.OnSignals 按它归池——未开池的规则信号安全跳过（不入仓）。
		sigID := libraryIDFromMeta(sig, runner.Type)
		poolType := string(runner.Type)
		if sigID != "" && (runner.Type == strategy.SignalFactor || runner.Type == strategy.SignalPattern) {
			poolType = sigID
		}
		// §名称规整：内置战法未显式命名时用规范展示名（龙头/双响炮/N形/龙回头），
		// 不再回退英文 runner 类型；库规则沿用规则名。
		displayName := sig.StrategyName
		if displayName == "" {
			displayName = StrategyDisplayName(string(runner.Type))
		}
		sigs = append(sigs, Signal{
			ID:           seqID(),
			Code:         code,
			Name:         orDefault(sig.Name, md.Name),
			Strategy:     displayName,
			StrategyID:   sigID,
			StrategyType: poolType,
			Direction:    direction,
			Action:       action,
			Tag:          nShapeTag(eval),
			Price:        sigPrice,
			Confidence:   sig.Confidence,
			ATR:          atr14Last(md.KLines),
			Reason:       sigReason,
			Sector:       sectorName,
			GeneratedAt:  now,
			Meta:         sig.Meta,
		})
	}
	// 动量分已在循环前计算并写入下方 sc（保持 8a/8b 打分量输出一致）
	// English: momentum score was already computed before the loop; assign it to keep 8a/8b output stable.
	sc.MomentumScore = momentumScore
	sc.MomentumValid = momentumValid
	sc.SignalActive = len(sigs) > 0

	// Q2: 动量分达到阈值且四战法均未出信号时，补一条动量信号（§动量入模拟盘）：
	//   ≥ 买入阈值 → Action=buy（进模拟盘自动撮合，归 momentum 池）
	//   ≥ 观察阈值但 < 买入阈值 → Action=watch（仅观察不自动交易）
	// 门控 sc.MomentumValid：竞价/盘前今日成交量=0 时动量数据不完整（无真实成交），
	// 不发存量历史数据凑出来的动量信号，等 9:30 实盘有成交量后再出。
	// English: Q2 — when momentum reaches the threshold but no other strategy fired, emit a momentum
	// signal: buy at/above the buy threshold (auto-filled into the paper momentum pool), watch between
	// the watch and buy thresholds. Gated on MomentumValid (pre-open volume of 0 = incomplete data).
	if len(sigs) == 0 && sc.MomentumValid && sc.MomentumScore >= a.momentumSignalThreshold() {
		action := "watch"
		reasonPrefix := ""
		if sc.MomentumScore >= a.momentumBuySignalThreshold() {
			action = "buy"
			reasonPrefix = fmt.Sprintf("强动量%.0f≥%.0f ", sc.MomentumScore, a.momentumBuySignalThreshold())
		}
		sigs = append(sigs, Signal{
			ID:           seqID(),
			Code:         code,
			Name:         md.Name,
			Strategy:     StrategyDisplayName(string(strategy.SignalMomentum)),
			StrategyType: string(strategy.SignalMomentum),
			Direction:    direction,
			Action:       action,
			Price:        md.Price,
			Confidence:   sc.MomentumScore / 100.0,
			Reason:       fmt.Sprintf("%s动量%.0f 量价齐升(MA+MACD+走势)", reasonPrefix, sc.MomentumScore),
			Sector:       sectorName,
			GeneratedAt:  now,
		})
		sc.SignalActive = true
	} else if len(sigs) == 0 && sc.MomentumScore >= a.momentumSignalThreshold() && !sc.MomentumValid {
		// 竞价/盘前数据不完整时不发 watch，但保留可排查日志
		// English: pre-open/incomplete data suppresses the momentum watch but keeps an audit log.
		log.Printf("[combat_agent] 动量%.0f达阈值但成交前数据不完整(Volume<=0/MACD缺), 暂不发动量watch: %s",
			sc.MomentumScore, code)
	}
	sc.UpdatedAt = now
	input.Scores[code] = sc
	// 在全部战法/动量门槛判定完成后，才记录本轮动量分作为下一轮的提升基准。
	// English: only after all strategies & the momentum gate have been evaluated, store this round's
	// momentum score as the baseline for next round's improvement check.
	a.momentumRecord(code, momentumScore)

	// E1 宏观利空门控：股指期货交割日等高影响宏观事件当日，买入信号统一降级，
	// 仅超高置信度（"特别高质量信号"）放行；N 形超短与动量 watch 一律拦截。
	// English: E1 macro bearish gate — on high-impact macro days (e.g. 交割日), buy signals are
	// downgraded unless exceptionally high-confidence; N-shape and momentum watch are blocked.
	if active, mcfg := a.macroGateActive(); active {
		sigs = applyMacroGate(sigs, true, mcfg)
		if len(sigs) > 0 {
			log.Printf("[combat_agent] 宏观利空门控生效: %d 条信号已降级/拦截", len(sigs))
		}
	}
	// 战法评分日志：code + 各维度分 + 是否命中（FLOW 全流程日志要求）
	// 附加"未出"原因（diagnostic）：各战法未出信号的具体原因，便于排查非龙头为何不出分/不发声。
	// English: scoring log — code + each dimension score + hit flag (FLOW requirement), plus a diagnostic
	// for why each strategy produced no signal, to debug why non-leaders stay silent.
	unSigNote := ""
	if len(unSig) > 0 {
		unSigNote = " | 未出: " + strings.Join(unSig, " ")
	}
	log.Printf("[combat_agent] 评分 %s(%s) 龙=%.0f 双=%.0f N=%.0f 回=%.0f 动量=%.0f 命中=%v%s",
		code, md.Name, sc.DragonScore, sc.DoubleBumpScore, sc.NScore, sc.DragonReturnScore, sc.MomentumScore, sc.SignalActive, unSigNote)
	// §W5-v3 LLM 参考标注收口：非 N 战法信号统一携带 d1_ref 参考分（Meta，浮点）；
	// Blocked 个股在 Reason 尾部追加"⚠️LLM利空提示"——信息不丢，但不改写级别、不拦截信号。
	// English: §W5-v3 — non-N signals carry a d1_ref reference score; blocked stocks get an
	// LLM-caution suffix on Reason without any level rewrite or veto.
	if len(sigs) > 0 {
		for i := range sigs {
			if sigs[i].StrategyType == string(strategy.SignalNShape) {
				continue // N 形由自身硬闸消费 D1，无需参考标注
			}
			if d1 != nil {
				if sigs[i].Meta == nil {
					sigs[i].Meta = make(map[string]float64)
				}
				sigs[i].Meta["d1_ref"] = d1.Score
				if llmBlocked && !strings.Contains(sigs[i].Reason, "LLM利空提示") {
					sigs[i].Reason += " ⚠️LLM利空提示:" + d1.Reason
				}
			}
		}
	}
	// §R4-5 Laodeng 置信度修正：按该股真实 PE/换手率/板块名逐股计算（未启用时 no-op）。
	// English: §R4-5 — apply the Laodeng confidence correction per stock using real PE/turnover/sector.
	a.applyLaodengToSignals(sigs, md, pe, sectorName)
	return sigs
}

// ScorePool 近实时 8a/8b 持续打分入口：对打分池（持仓+自选）逐只执行四战法评分 + 动量分，
// 无论是否通过都记录原始分；Pass 的战法生成信号返回（由调用方决定是否广播）。
// 与 ScanLong/ScanShort 共用 evalAll，保证打分口径一致。
//
// 参数：
//   - codes: 打分池代码列表（持仓+自选）
//   - md: 行情数据映射（code → 行情快照）
//   - d1Scores: 最近一轮 D1 评分缓存（主循环产出，近实时循环复用）
//   - emotionPhase: 情绪阶段，供 N 形情绪硬闸使用
//
// 返回值：
//   - scores: 打分结果映射（code → 各战法原始分）
//   - sigs: 本轮通过战法产生的信号列表
func (a *Agent) ScorePool(codes []string, md map[string]*strategy_engine.StockMarketData, d1Scores map[string]D1Score, emotionPhase string) (map[string]StockScores, []Signal) {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()
	if len(runners) == 0 {
		return nil, nil
	}
	scores := make(map[string]StockScores, len(codes))
	// 组装最小化 ScanInput（共享只读字段）
	// English: assemble a minimal ScanInput (shared read-only fields).
	input := &ScanInput{MarketData: md, Scores: scores, D1Scores: d1Scores, EmotionPhase: emotionPhase}
	now := time.Now()
	// 股票级并发：每只股票独立 goroutine 走 evalAll，各自持有本地 Scores map 避免并发写同一 map；
	// 波态机/diag 均由内部互斥锁保护，结果按下标归位以保持确定性顺序。
	// English: parallelize per stock — each goroutine runs evalAll with its own local Scores map
	// (no concurrent writes to a shared map); wave machines / diagnostics are mutex-protected, and
	// results are placed back by index so the final signal order stays deterministic.
	type poolResult struct {
		has bool
		sc  StockScores
		sig []Signal
	}
	results := make([]poolResult, len(codes))
	var wg sync.WaitGroup
	for i, code := range codes {
		// L1 阻塞的股票跳过打分
		// English: skip L1-blocked stocks.
		if input.L1Blocked[code] {
			continue
		}
		wg.Add(1)
		go func(i int, code string) {
			defer wg.Done()
			local := &ScanInput{MarketData: md, Scores: make(map[string]StockScores), D1Scores: d1Scores, EmotionPhase: emotionPhase}
			sig := a.evalAll(local, runners, code, md[code], nil, "做多", "个股", now)
			sc, ok := local.Scores[code]
			results[i] = poolResult{has: ok, sc: sc, sig: sig}
		}(i, code)
	}
	wg.Wait()
	var sigs []Signal
	for i, code := range codes {
		if results[i].has {
			scores[code] = results[i].sc
		}
		sigs = append(sigs, results[i].sig...)
	}
	return scores, sigs
}

// momentumWeights 读取动量分权重配置（nil 防护，缺省回退 40/30/30）。
// 返回量价/MACD/走势三者的权重配置，供 MomentumScore 使用。
// 返回值：
//   - 动量分权重配置，包含量价权重、MACD权重、走势权重等
func (a *Agent) momentumWeights() config.MomentumConfig {
	a.mu.RLock()
	cfg := a.strategyCfg
	a.mu.RUnlock()
	// 配置缺失时回退默认权重：量价40 + MACD30 + 走势30
	if cfg == nil {
		return config.MomentumConfig{VolumePriceWeight: 40, MACDWeight: 30, TrendWeight: 30, SignalThreshold: 60}
	}
	return cfg.Momentum
}

// momentumSignalThreshold 读取动量分触发信号的阈值（默认 60）。
// 当动量分达到该阈值时，会产生动量信号（watch 或 buy）。
// 返回值：
//   - 动量分触发阈值
func (a *Agent) momentumSignalThreshold() float64 {
	w := a.momentumWeights()
	if w.SignalThreshold <= 0 {
		return 60
	}
	return w.SignalThreshold
}

// momentumBuySignalThreshold 读取动量买入阈值（默认 75，且不低于 watch 阈值）。
// 当动量分达到该阈值时，会产生 buy 级信号进入模拟盘动量池自动撮合。
// 返回值：
//   - 动量买入阈值
func (a *Agent) momentumBuySignalThreshold() float64 {
	w := a.momentumWeights()
	if w.BuySignalThreshold <= 0 {
		return 75
	}
	if w.BuySignalThreshold < a.momentumSignalThreshold() {
		return a.momentumSignalThreshold()
	}
	return w.BuySignalThreshold
}

// momentumGateEnabled 读取动量分"提升才提醒"门槛开关（默认开）。
// 开关经前端 Settings 动量分组切换，随策略配置热更新即时生效。
// 返回值：
//   - true: 启用动量提升门槛
//   - false: 禁用动量提升门槛
func (a *Agent) momentumGateEnabled() bool {
	w := a.momentumWeights()
	return w.MomentumGateEnabled
}

// momentumDeltaTol 读取动量分回落容忍差（默认 5）。
// 当动量分回落幅度小于该值时，仍视为"未明显回落"，允许信号产生。
// 返回值：
//   - 动量分回落容忍差
func (a *Agent) momentumDeltaTol() float64 {
	w := a.momentumWeights()
	if w.MomentumDeltaTol <= 0 {
		return 5
	}
	return w.MomentumDeltaTol
}

// doubleBumpConfig 读取双响炮配置（nil 防护，回退零值结构）。
// 返回值：
//   - 双响炮战法配置
func (a *Agent) doubleBumpConfig() config.DoubleBumpConfig {
	a.mu.RLock()
	cfg := a.strategyCfg
	a.mu.RUnlock()
	if cfg == nil {
		return config.DoubleBumpConfig{}
	}
	return cfg.DoubleBump
}

// applyD1Boost 对非 N 战法评分做 D1 软加成（C1）：当 D1 分 ≥ BoostThreshold 时，
// 总分 ×(1+BoostWeight×D1/40)（封顶 100），并按各战法自身门槛重判 pass/level，
// 让强事件股（如高确定性研报）跨越买入阈值。原始总分写入 Details["d1_raw"]，
// 加成量写入 Details["d1_boost"]（前端可见，透明可追溯）。N 形战法有独立 D1 硬闸，不叠加。
//
// §W5-v3 标注化改造（owner 口径：LLM 不参与非 N 战法信号产生）：
// 只把"D1 参考分/旧公式理论加成值"写入 Details 供前端展示，绝不修改 TotalScore/Pass/Level——
// 信号的观察与买入级别完全由原始量价分数决定。函数名保留以减少调用方改动。
//
// 参数：
//   - t: 策略类型
//   - eval: 策略评分结果，将被原地修改
//   - d1: D1 评分结果
func (a *Agent) applyD1Boost(t strategy.SignalType, eval *strategy.Evaluation, d1 *D1Score) {
	a.mu.RLock()
	cfg := a.d1Boost
	a.mu.RUnlock()
	if d1 == nil || d1.Blocked {
		return
	}
	if eval.Details == nil {
		eval.Details = make(map[string]float64)
	}
	eval.Details["d1_ref"] = d1.Score // 参考分（展示层）
	if cfg.BoostWeight > 0 && d1.Score >= cfg.BoostThreshold {
		ref := eval.TotalScore * (cfg.BoostWeight * d1.Score / 40) * 100 // 旧公式理论增量（百分点），仅供对照
		if ref > 0 {
			eval.Details["d1_ref_boost"] = ref
		}
	}
	_ = t
}

// d1BoostTiers 返回非 N 战法的级别重判函数，门槛与 strategies/* 评分实现保持一致：
//   - dragon / double_bump：≥60 full_chain(买入)，≥50 brief(观察)
//   - dragon_return：≥85 accelerate，≥75 main，≥60 first
//
// 参数：
//   - t: 策略类型
//
// 返回值：
//   - levelFn: 级别重判函数，接收评分返回（级别，是否通过）
//   - ok: 是否支持该策略类型的级别重判
func d1BoostTiers(t strategy.SignalType) (func(score float64) (string, bool), bool) {
	switch t {
	case strategy.SignalDragon, strategy.SignalDoubleBump:
		return func(s float64) (string, bool) {
			if s >= 60 {
				return "full_chain", true
			}
			if s >= 50 {
				return "brief", true
			}
			return "", false
		}, true
	case strategy.SignalDragonReturn:
		return func(s float64) (string, bool) {
			if s >= 85 {
				return "accelerate", true
			}
			if s >= 75 {
				return "main", true
			}
			if s >= 60 {
				return "first", true
			}
			return "", false
		}, true
	}
	return nil, false
}

// mdEmpty 判断行情快照是否为空（nil 或缺现价）。
// 参数：
//   - md: 行情数据快照
//
// 返回值：
//   - true: 行情数据无效
//   - false: 行情数据有效
func mdEmpty(md *strategy_engine.StockMarketData) bool {
	return md == nil || md.Price <= 0
}

// momentumPrevious 读取该股上一轮动量分（跨交易日隔离出重置）。
// 返回（上一轮动量分, 是否已有记录）。
// 参数：
//   - code: 股票代码
//
// 返回值：
//   - 上一轮动量分
//   - 是否已有记录
func (a *Agent) momentumPrevious(code string) (float64, bool) {
	a.momentumPrevMu.Lock()
	defer a.momentumPrevMu.Unlock()
	day := data.TradingDayDate(time.Now())
	if a.momentumPrevDay != day {
		// 跨交易日：重置历史，避免把上一交易日的动量带到今天
		// English: new trading day resets history so yesterday's momentum isn't carried into today.
		a.momentumPrev = make(map[string]float64)
		a.momentumPrevDay = day
		return 0, false
	}
	v, ok := a.momentumPrev[code]
	return v, ok
}

// momentumRecord 记录该股本轮动量分（供下一轮提升门槛比较）。
// 参数：
//   - code: 股票代码
//   - score: 本轮动量分
func (a *Agent) momentumRecord(code string, score float64) {
	a.momentumPrevMu.Lock()
	defer a.momentumPrevMu.Unlock()
	day := data.TradingDayDate(time.Now())
	if a.momentumPrevDay != day {
		a.momentumPrev = make(map[string]float64)
		a.momentumPrevDay = day
	}
	a.momentumPrev[code] = score
}

// momentumImproved 判断动量分相对上一轮是否"提升/未明显回落"。
// 当前 ≥ 上一轮 − 容忍差 时视为未明显回落。
//
// 参数：
//   - code: 股票代码
//   - cur: 当前动量分
//   - valid: 动量数据是否有效
//
// 返回值：
//   - true: 动量分提升或未明显回落（或无上一轮记录）
//   - false: 动量分明显回落
func (a *Agent) momentumImproved(code string, cur float64, valid bool) bool {
	// 动量数据无效（竞价/盘前 Volume=0 等）→ 跳过门槛放行
	// English: invalid momentum data (auction/pre-open Volume=0) -> skip the gate and let it through.
	if !valid {
		return true
	}
	prev, has := a.momentumPrevious(code)
	if !has {
		// 无上一轮记录（本轮为该股首轮评分）→ 视为起点，放行
		// English: no prior record (first round for this code this session) -> treated as a baseline, pass.
		return true
	}
	return cur >= prev-a.momentumDeltaTol()
}

// strategyDataInsufficient 判断某战法类型的输入数据是否不足。
// 不足时得分 0 不代表真实 0 分，而是数据缺失导致的降级。
//
// 参数：
//   - t: 策略类型
//   - md: 行情数据快照
//
// 返回值：
//   - true: 数据不足，评分降级为 0
//   - false: 数据充足，评分真实
func strategyDataInsufficient(t strategy.SignalType, md *strategy_engine.StockMarketData) bool {
	if md == nil {
		return true
	}
	switch t {
	case strategy.SignalDragon:
		// 龙头需要实时价 + 至少 5 根日K（RS 趋势）
		return md.Price <= 0 || len(md.KLines) < 5
	case strategy.SignalDoubleBump:
		// 双响炮需要至少 20 根日K（均线与量能）
		return len(md.KLines) < 20
	case strategy.SignalDragonReturn:
		// 龙回头需要至少 30 根日K（主升段/回调）
		return len(md.KLines) < 30
	case strategy.SignalNShape:
		// N 形需要实时价 + 日K + 分钟K(MACD)
		return md.Price <= 0 || len(md.KLines) < 2 || len(md.MinuteKLine) < 2
	default:
		return false
	}
}

// IsSTStock 判断个股是否为风险警示/退市整理股票（ST / *ST / S*ST / SST / 退 开头）。
// 用于战法信号层屏蔽：此类股票不产生交易/提醒信号。
//
// 参数：
//   - name: 股票名称
//
// 返回值：
//   - true: 是 ST/退市股票
//   - false: 不是 ST/退市股票
func IsSTStock(name string) bool {
	n := strings.TrimSpace(name)
	if n == "" {
		return false
	}
	// 统一大写比对，覆盖 *ST / S*ST / SST / ST 与 "退市" 前缀
	// compare upper-cased to cover *ST / S*ST / SST / ST and the "退市" prefix
	u := strings.ToUpper(n)
	for _, p := range []string{"*ST", "S*ST", "SST", "ST", "退"} {
		if strings.HasPrefix(u, p) {
			// 前缀后必须紧跟汉字（A股 ST 命名规范），避免误伤英文含 ST 字母的名称
			// the char right after the prefix must be a CJK char (A-share ST naming rule),
			// so English names merely containing the letters "ST" are not blocked
			rest := []rune(n[len(p):])
			return len(rest) == 0 || isCJK(rest[0])
		}
	}
	return false
}

// isCJK 判断 rune 是否中文字符（基本区 \u4e00-\u9fff 起始范围，含扩展容纳常用名）。
// 参数：
//   - r: 待判断的 rune
//
// 返回值：
//   - true: 是中文字符
//   - false: 不是中文字符
func isCJK(r rune) bool {
	return r >= 0x4e00 && r <= 0x9fff
}

// momentumDataValid 判断动量分所需数据是否完整。
// 量价 + 走势 + MACD 任一缺失即视为不完整。
//
// 参数：
//   - md: 行情数据快照
//
// 返回值：
//   - true: 动量数据完整
//   - false: 动量数据不完整
func momentumDataValid(md *strategy_engine.StockMarketData) bool {
	if md == nil || md.Quote == nil {
		return false
	}
	q := md.Quote
	if q.Price <= 0 || q.Volume <= 0 || len(md.KLines) < 5 {
		return false
	}
	// 分钟 MACD 需有有效数据
	m := md.MinuteMACD
	return !(m.DIF == 0 && m.DEA == 0 && m.Bar == 0)
}

// markDataGap 记录某战法因数据不足而降级的数据缺口，供前端区分真实 0 分与无数据。
// 参数：
//   - sc: 股票评分结果，将被原地修改
//   - t: 策略类型
//   - md: 行情数据快照
func markDataGap(sc *StockScores, t strategy.SignalType, md *strategy_engine.StockMarketData) {
	if sc.DataGaps == nil {
		sc.DataGaps = make(map[string]bool)
	}
	sc.DataGaps[string(t)] = true
}

// ScanShort 执行做空扫描：7b 板块利空→验证后个股→8b；8b 个股利空→直入战法（反向信号）。
// 仅当做空开关启用时执行，否则返回 nil。
// 流程与 ScanLong 对称：方向字段标记为"做空"，供上层做反向处理。
//
// 扫描流程：
//  1. 检查做空开关，关闭则直接返回
//  2. 遍历已验证的利空板块，对板块内每只个股执行评分
//  3. 遍历个股直入列表，对每只个股执行评分
//  4. 对最终信号批量附加盘口因子
//
// 参数：
//   - input: 扫描输入，包含已验证板块、行情数据、D1评分等信息
//
// 返回值：
//   - 做空信号列表，为空时表示无信号
func (a *Agent) ScanShort(input ScanInput) []Signal {
	// 做空开关关闭 → 直接返回，不做任何做空扫描
	// English: short-selling disabled → return immediately without scanning.
	if !a.ShortEnabled() {
		return nil
	}
	if len(input.Sectors) == 0 && len(input.IndividualStocks) == 0 {
		return nil
	}

	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()

	if len(runners) == 0 {
		log.Printf("[combat_agent] ScanShort: 无策略策略")
		return nil
	}

	var raw []Signal
	now := time.Now()

	// 7b 板块利空 → 验证后的个股走 8b
	// English: verified bear sectors feed their stocks through 8b.
	for _, sector := range input.Sectors {
		// 仅处理方向为"利空"的板块（利好走做多路径 ScanLong）
		// English: only bear-direction sectors are handled (bulls go through ScanLong).
		if sector.Direction != "利空" {
			continue
		}
		for _, code := range sector.Stocks {
			// L1 阻塞的个股跳过
			// English: skip L1-blocked stocks.
			if input.L1Blocked[code] {
				continue
			}
			raw = append(raw, a.evalAll(&input, runners, code, input.MarketData[code], &sector, "做空", sector.Name, now)...)
		}
	}

	// 8b 个股利空 → 直入战法（反向信号）
	// English: 8b direct stock inputs go straight into the strategies (inverted signals).
	for _, code := range input.IndividualStocks {
		if input.L1Blocked[code] {
			continue
		}
		raw = append(raw, a.evalAll(&input, runners, code, input.MarketData[code], nil, "做空", "个股", now)...)
	}

	// Laodeng 置信度修正已下沉到 evalAll 内按真实数据逐股应用（§R4-5），此处不再统一套常数
	signals := raw
	// 对最终信号批量附加盘口因子（买卖压力/封单量，供战法与前端使用）
	// English: attach order-book factors (bid/ask pressure & seal volume) to final signals.
	a.attachDepthFactors(signals)
	log.Printf("[combat_agent] ScanShort: %d 板块 %d 个股 → %d 做空信号", len(input.Sectors), len(input.IndividualStocks), len(signals))
	return signals
}

// CheckPositionAlerts 检查所有持仓的止盈止损条件，返回需要提醒的信号列表。
// 根据实时行情价格计算盈亏比例，触发止盈/止损阈值时生成提醒信号。
//
// 决策逻辑（按持仓方向）：
//   - 做多止盈：仍有做多信号 → 继续持有(降级提示)；无 → 硬止盈
//   - 做多止损：出现做空/利空信号 → 硬止损；未出现 → 可能洗盘，降级提示观察
//   - 做空止盈：仍有做空/利空信号 → 继续持有(降级提示)；无 → 硬止盈
//   - 做空止损：出现做多/利好信号 → 硬止损；未出现 → 降级提示观察
//
// 参数：
//   - rpt: 持仓报告，提供持仓明细
//   - marketAPI: 市场数据 API，提供实时报价
//   - quotes: 实时报价快照（code → 报价）
//   - scores: 当轮做多打分表（SignalActive=该股做多信号）
//   - bearHit: 客置参数：该股本轮命中利空板块/利空个股的 code→true 映射
//
// 返回值：
//   - 提醒信号列表（止盈/止损/跌幅提醒）
func (a *Agent) CheckPositionAlerts(rpt *report.Report, marketAPI *data.MarketAPI, quotes map[string]*data.StockInfo, scores map[string]StockScores, bearHit ...map[string]bool) []Signal {
	positions := rpt.HeldPositions()
	if len(positions) == 0 {
		return nil
	}

	// 可选：本轮利空(做空)信号命中集合（命中利空板块/利空个股）
	// English: optional bear-signal hit set for this round (hit bear sectors/stocks).
	var bears map[string]bool
	if len(bearHit) > 0 {
		bears = bearHit[0]
	}

	var alerts []Signal
	now := time.Now()

	// 当日跌幅提醒阈值：未配置(<=0)时默认 5%
	// English: daily-drop threshold defaults to 5% when unconfigured.
	dropPct := 5.0
	if p := a.PositionDailyDropPct(); p > 0 {
		dropPct = p
	}

	for _, pos := range positions {
		// §R4-6 行情取用快照化：优先取调用方注入的 5s 快照（主循环/近实时循环批量现成），
		// 缺失时才逐票兜底单查。此前逐持仓串行同步调 GetRealtimeQuote——持仓多时 5s 循环
		// 被上游限流拖长（N 次网络往返/轮）。
		// 快照缺失/无效/现价为 0（停牌未成交）时跳过该持仓，避免以无效价格误判止盈/止损
		// （如 0 价会算成 -100%）。English: §R4-6 — prefer the injected 5s snapshot (a batch the
		// callers already hold) and only fall back to per-position single fetches when missing,
		// instead of N serial network calls per 5s round. Missing/invalid/zero prices skip the
		// position to avoid false P/L judgments (a 0 price would compute as -100%).
		quote := quotes[pos.Code]
		if quote == nil || quote.Price <= 0 {
			var err error
			quote, err = marketAPI.GetRealtimeQuote(pos.Code)
			if err != nil || quote == nil || quote.Price <= 0 {
				continue
			}
		}

		// 按现价计算持仓盈亏比例（%）
		// English: compute the position P/L percentage against the current price.
		pnl := (quote.Price - pos.EntryPrice) / pos.EntryPrice * 100

		// 该股当前信号：做多信号=打分表 SignalActive(做多池)；做空/利空信号=命中利空板块映射
		// English: current same-direction signals — bull from the score table's SignalActive, bear from
		// the bear-sector hit map.
		hasBull := false
		if sc, ok := scores[pos.Code]; ok {
			hasBull = sc.SignalActive
		}
		hasBear := false
		if bears != nil {
			hasBear = bears[pos.Code]
		}
		isShort := pos.Direction == "做空"

		// 当日跌幅提醒（独立于成本止损）：做多持仓当日跌幅超过阈值即提醒，
		// 与"按持仓成本盈亏"的止损逻辑分开，防止当日急跌但成本尚未破线时不提示。
		// 做空持仓下跌=获利方向，不触发。（English: daily-drop alert for long holdings — a daily decline
		// beyond the threshold triggers regardless of cost-based P/L, so an intraday plunge never goes
		// unreported even when cost P/L has not hit the stop-loss line yet. Shorts profit from drops, so
		// they are excluded.）
		if !isShort && quote.ChangePct <= -dropPct {
			alerts = append(alerts, Signal{
				ID:          seqID(),
				Code:        pos.Code,
				Name:        pos.Name,
				Strategy:    pos.Strategy,
				Direction:   "提醒",
				Action:      "关注",
				AlertType:   "跌幅提醒",
				Price:       quote.Price,
				Confidence:  1.0,
				Reason:      fmt.Sprintf("当日跌幅%.2f%% 注意短期风险,关注是否止跌企稳", quote.ChangePct),
				GeneratedAt: now,
			})
		}

		// 未设置止盈/止损阈值的持仓跳过止盈/止损检查（跌幅提醒不受此限制）
		// English: positions without take-profit/stop-loss thresholds skip those checks (daily-drop alert is exempt).
		if pos.TakeProfitPct <= 0 && pos.StopLossPct <= 0 {
			continue
		}

		// 触及止盈线 → 生成止盈提醒。
		// 做多：仍有做多信号→持有(降级提示)；无→硬止盈。做空：仍有做空信号→持有；无→硬止盈。
		// English: take-profit line hit — with a same-direction signal keep holding (downgraded hint),
		// otherwise issue a hard take-profit alert.
		if pos.TakeProfitPct > 0 && pnl >= pos.TakeProfitPct {
			alertType, action := "止盈", "止盈"
			reason := fmt.Sprintf("盈亏%.2f%% 触及止盈%.0f%%", pnl, pos.TakeProfitPct)
			keepSig := hasBear // 做空：有做空信号继续持有
			if !isShort {
				keepSig = hasBull // 做多：有做多信号继续持有
			}
			if keepSig {
				alertType, action = "提示", "关注"
				if isShort {
					reason = fmt.Sprintf("盈亏%.2f%% 已触止盈%.0f%% 但仍有做空信号,建议持有观察", pnl, pos.TakeProfitPct)
				} else {
					reason = fmt.Sprintf("盈亏%.2f%% 已触止盈%.0f%% 但仍有做多信号,建议持有观察", pnl, pos.TakeProfitPct)
				}
			}
			alerts = append(alerts, Signal{
				ID:          seqID(),
				Code:        pos.Code,
				Name:        pos.Name,
				Strategy:    pos.Strategy,
				Direction:   "提醒",
				Action:      action,
				AlertType:   alertType,
				Price:       quote.Price,
				Confidence:  1.0,
				Reason:      reason,
				GeneratedAt: now,
			})
		}

		// 触及止损线 → 分级止损提醒（FIX#12）：首触线且无反向确认 → 减仓（半仓自动退出）；
		// 出现反向确认信号或深度破位（-2×止损线）→ 硬止损（全清）。
		// 模拟盘/实盘卖出侧按 Signal 级别自动执行：减仓级自动半仓、止损级自动全平——
		// 彻底取代旧"降级关注洗盘"（只提醒不动作，造成"亏十几个点拿好几天不止损"）。
		// English: tiered stop-loss (FIX#12) — first line breach with no adverse confirmation trims
		// (half-position auto-exit); an adverse signal OR a deep breach (-2× the line) forces a hard
		// full close. The sell side auto-executes these levels (trim/close), replacing the old
		// "downgrade to watch" that only reminded without acting (the "held days at -13% never sold" bug).
		if pos.StopLossPct > 0 && pnl <= -pos.StopLossPct {
			alertType, action := "止损", "止损"
			reason := fmt.Sprintf("盈亏%.2f%% 触及止损%.0f%%", pnl, pos.StopLossPct)
			// 是否出现对持仓不利的确认信号（决定直接全清）
			// English: whether an adverse confirmation is present (decides a hard full close).
			hard := false
			if isShort {
				hard = hasBull // 做空止损：出现做多(利好)信号→硬止损
			} else {
				hard = hasBear // 做多止损：出现做空(利空)信号→硬止损
			}
			// 深度破位兜底：浮亏达到 2×止损线时无论有无确认一律全清（防止阴跌钝刀割肉）。
			// English: deep-breach backstop — at 2× the stop line, close regardless of confirmation.
			if !hard && pnl <= -2*pos.StopLossPct {
				hard = true
			}
			if !hard {
				alertType, action = "减仓", "卖出"
				if isShort {
					reason = fmt.Sprintf("盈亏%.2f%% 已触止损%.0f%% 未现利好确认,先减半仓控制风险", pnl, pos.StopLossPct)
				} else {
					reason = fmt.Sprintf("盈亏%.2f%% 已触止损%.0f%% 未现利空/做空信号,先减半仓控制风险", pnl, pos.StopLossPct)
				}
			}
			alerts = append(alerts, Signal{
				ID:          seqID(),
				Code:        pos.Code,
				Name:        pos.Name,
				Strategy:    pos.Strategy,
				Direction:   "提醒",
				Action:      action,
				AlertType:   alertType,
				Price:       quote.Price,
				Confidence:  1.0,
				Reason:      reason,
				GeneratedAt: now,
			})
		}
	}

	log.Printf("[combat_agent] CheckPositionAlerts: %d 持仓 → %d 提醒", len(positions), len(alerts))
	return alerts
}

// Scan 通用扫描入口（不区分方向），对输入板块的每只股票逐策略评估打分。
// 方向直接沿用板块本身的 Direction（利好/利空），不做做多/做空分流。
// 返回所有通过策略评估的信号。
//
// 参数：
//   - input: 扫描输入，包含已验证板块、行情数据、D1评分等信息
//
// 返回值：
//   - 信号列表，为空时表示无信号
func (a *Agent) Scan(input ScanInput) []Signal {
	a.mu.RLock()
	runners := a.runners
	a.mu.RUnlock()

	// 无可运行策略或没有输入板块 → 直接返回
	// English: no runner or no input sectors → return immediately.
	if len(runners) == 0 || len(input.Sectors) == 0 {
		return nil
	}

	var allSignals []Signal
	now := time.Now()

	for _, sector := range input.Sectors {
		for _, code := range sector.Stocks {
			if input.L1Blocked[code] {
				continue
			}
			// 板块方向即信号方向，板块名作为信号所属板块
			// English: sector direction becomes the signal direction, sector name tags the signal.
			allSignals = append(allSignals, a.evalAll(&input, runners, code, input.MarketData[code], &sector, sector.Direction, sector.Name, now)...)
		}
	}

	log.Printf("[combat_agent] Scan: %d 板块 → %d 信号 (%v)",
		len(input.Sectors), len(allSignals), time.Since(now))
	return allSignals
}
