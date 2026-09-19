// paper_admission_test.go — §WMQ-5（20260917）：多账号模拟盘通道准入接线回归。
// 缺口（docs/BUGFIX_WATCHLIST_20260917.md #2）：registry.dispatchPaperSignals →
// engine.filterPaperAdmitted 真实分发路径此前在 *_test.go 零引用——paper 通道白名单/
// 黑名单/确认窗只有纯逻辑单测，没有"信号→准入→可撮合集合"的贯通测试（live 通道
// dispatchLive 有对称集成测试）。本文件补齐该对称覆盖：白名单/黑名单/非买直通/确认窗
// 四条分支全走真实 Engine 入口。
package engine

import (
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
)

// newPaperAdmissionEngine 构建最小引擎 + 内存配置管理器（无 store，走全局规则快照）。
// strategies=纸面战法白名单（空=默认全集）；blacklist=纸面个股黑名单（纯代码）；
// shadow=true 时黑名单为影子模式（留痕 pass），false 时硬拦。
func newPaperAdmissionEngine(t *testing.T, strategies, blacklist []string, shadow bool) *Engine {
	t.Helper()
	e := &Engine{}
	cm := config.NewManager("")
	cm.Rules.Paper.Strategies = strategies
	cm.Rules.Paper.Blacklist = blacklist
	cm.Rules.SignalCtl.ShadowBlacklist = &shadow
	e.cfgMgr = cm
	return e
}

// paperSigs 构造一轮测试信号：置信度 100（高置信 → 30s 秒级快窗分支）。
// English: three paper signals (high-confidence so the 30s fast confirm window applies).
func paperSigs() []combat_agent.Signal {
	return []combat_agent.Signal{
		{ID: "P1", Code: "600000", Name: "浦发", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "buy", Price: 10, Confidence: 1},
		{ID: "P2", Code: "000001", Name: "平安", Strategy: "动量", StrategyType: "momentum", Direction: "做多", Action: "buy", Price: 10, Confidence: 1},
		{ID: "P3", Code: "600000", Name: "浦发", Strategy: "龙头", StrategyType: "dragon", Direction: "做多", Action: "sell", Price: 10},
	}
}

// dispatchRound 模拟注册表每轮喂入（prune=true 口径），返回准入通过的信号 code@action。
func dispatchRound(e *Engine, sigs []combat_agent.Signal, now time.Time) map[string]bool {
	out := e.filterPaperAdmitted("u_1", sigs, now)
	got := map[string]bool{}
	for _, s := range out {
		got[s.Code+"@"+s.Action] = true
	}
	return got
}

// 硬黑名单拦截买入信号。
func TestFilterPaperAdmitted_HardBlacklistBlocksBuy(t *testing.T) {
	e := newPaperAdmissionEngine(t, nil, []string{"600000"}, false)
	t0 := time.Now()
	r1 := dispatchRound(e, paperSigs(), t0)
	if r1["600000@buy"] {
		t.Fatalf("黑名单(硬拦)首见买入即命中, r1=%v", r1)
	}
	if !r1["600000@sell"] {
		t.Fatalf("卖出信号不得被任何准入闸拦截: %v", r1)
	}
	// 探针只对非黑名单信号登记； dragon@600000 从未进观测集 → 后续满窗也放不出。
	r2 := dispatchRound(e, paperSigs(), t0.Add(31*time.Second))
	if r2["600000@buy"] {
		t.Fatalf("黑名单买入在后续轮次仍不得通过: %v", r2)
	}
}

// 影子黑名单只观察不拦截，买入信号保留。
func TestFilterPaperAdmitted_ShadowBlacklistKeepsBuys(t *testing.T) {
	// 影子模式（默认）：黑名单命中留痕但裁定 pass —— 对齐 ShadowBlacklist 默认开语义。
	e := newPaperAdmissionEngine(t, nil, []string{"600000"}, true)
	t0 := time.Now()
	dispatchRound(e, paperSigs(), t0)
	r2 := dispatchRound(e, paperSigs(), t0.Add(31*time.Second))
	if !r2["600000@buy"] {
		t.Fatalf("影子黑名单不应硬拦（观察期 pass）：预期 dragon@buy + dragon@sell, r2=%v", r2)
	}
}

// 动量准入下空白名单拒绝全部买入。
func TestFilterPaperAdmitted_EmptyWhitelistRejectsMomentumBuys(t *testing.T) {
	e := newPaperAdmissionEngine(t, nil, nil, true)
	t0 := time.Now()
	r1 := dispatchRound(e, paperSigs(), t0)
	// 首见买入全部 hold（探针观测中）；卖出恒 pass。
	if r1["600000@sell"] == false {
		t.Fatalf("卖出应首轮即通过: %v", r1)
	}
	if r1["600000@buy"] || r1["000001@buy"] {
		t.Fatalf("确认窗未满前不得放行买入: %v", r1)
	}
	r2 := dispatchRound(e, paperSigs(), t0.Add(31*time.Second))
	// 满窗后：dragon 买入 pass；动量在默认全集外仍拒。
	if !r2["600000@buy"] {
		t.Fatalf("满窗确认后 dragon 买入应通过: %v", r2)
	}
	if r2["000001@buy"] {
		t.Fatalf("动量不在默认全集，确认满窗也不得通过: %v", r2)
	}
}

// 显式白名单只约束买入，卖出不受影响。
func TestFilterPaperAdmitted_ExplicitWhitelistGatesBuysOnly(t *testing.T) {
	// 白名单仅动量：dragon 买入白名单外被拒；动量买入满窗通过；卖出恒 pass。
	e := newPaperAdmissionEngine(t, []string{"momentum"}, nil, true)
	t0 := time.Now()
	dispatchRound(e, paperSigs(), t0)
	r2 := dispatchRound(e, paperSigs(), t0.Add(31*time.Second))
	if !r2["000001@buy"] {
		t.Fatalf("显式白名单应放行动量买入: %v", r2)
	}
	if r2["600000@buy"] {
		t.Fatalf("dragon 买入在白名单(momentum)外应被拒: %v", r2)
	}
	if !r2["600000@sell"] {
		t.Fatalf("dragon 卖出应恒通过（白名单只 gate 买入）: %v", r2)
	}
}
