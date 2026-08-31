// risk 风控引擎：信号级校验、回撤、多信号冲突、M8 兜底与仓位限制。
package risk

import (
	"testing"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/strategy"
)

// newRisk 构造测试用风控引擎（默认配置）。
func newRisk() *Engine {
	return New(config.NewManager(""))
}

// signal 构造测试信号。
func signal(code string, prio strategy.Priority, action strategy.TradeAction) *strategy.Signal {
	return &strategy.Signal{Code: code, Priority: prio, Action: action}
}

// TestCheckSignalBlacklist 黑名单个股被阻断（P1/block/blocked）。
func TestCheckSignalBlacklist(t *testing.T) {
	e := newRisk()
	e.cfg.Get().Theme.BlackList = []string{"600001"}
	r := e.CheckSignal(signal("600001", strategy.P3, strategy.ActionBuy))
	if !r.Blocked || r.Action != "block" || r.Priority != strategy.P1 {
		t.Errorf("黑名单应 block/P1/blocked, got %+v", r)
	}
}

// TestCheckSignalPass 非黑名单且无合规模式 → 通过。
func TestCheckSignalPass(t *testing.T) {
	e := newRisk()
	r := e.CheckSignal(signal("600519", strategy.P2, strategy.ActionBuy))
	if !r.Pass || r.Blocked || r.Action != "pass" {
		t.Errorf("正常信号应放行, got %+v", r)
	}
}

// TestCheckCompliance 合规模式下信号仍放行。
func TestCheckCompliance(t *testing.T) {
	e := newRisk()
	e.cfg.Get().RiskCtrl.Compliance.ComplianceMode = true
	r := e.CheckSignal(signal("600519", strategy.P2, strategy.ActionBuy))
	if !r.Pass || r.Blocked {
		t.Errorf("合规模式应放行, got %+v", r)
	}
}

// TestCheckDrawdown 超过阈值触发，否则通过。
func TestCheckDrawdown(t *testing.T) {
	e := newRisk()
	rule := config.DrawdownRule{Pct: -10, Action: "清仓"}
	if r := e.CheckDrawdown(100, 88, rule); r.Pass || r.Action != "清仓" {
		t.Errorf("回撤 -12%% ≤ -10%% 应触发, got %+v", r)
	}
	if r := e.CheckDrawdown(100, 95, rule); !r.Pass {
		t.Errorf("回撤 -5%% 不应触发, got %+v", r)
	}
}

// TestResolveConflict 优先级高者胜，同级时卖>买>持。
func TestResolveConflict(t *testing.T) {
	e := newRisk()
	got := e.ResolveConflict([]strategy.Signal{
		*signal("1", strategy.P3, strategy.ActionBuy),
		*signal("1", strategy.P2, strategy.ActionWatch),
	})
	if got == nil || got.Priority != strategy.P2 {
		t.Errorf("应选 P2, got %+v", got)
	}
	// 同优先级：卖出优先
	got = e.ResolveConflict([]strategy.Signal{
		*signal("1", strategy.P3, strategy.ActionBuy),
		*signal("1", strategy.P3, strategy.ActionSell),
	})
	if got.Action != strategy.ActionSell {
		t.Errorf("同优先级应卖>买, got %s", got.Action)
	}
	if e.ResolveConflict(nil) != nil {
		t.Error("空信号应返回 nil")
	}
}

// TestM8Check 组合回撤超阈值触发清仓；未启用/无峰值不检查。
func TestM8Check(t *testing.T) {
	e := newRisk()
	rc := &e.cfg.Get().RiskCtrl
	rc.M8Enabled = true
	rc.M8PortfolioDrawdownPct = -20

	if r := e.M8Check(60, 100); !r.Blocked || r.Action != "sell_all" || r.Priority != strategy.P1 {
		t.Errorf("组合回撤 -40%% 应触发清仓, got %+v", r)
	}
	if r := e.M8Check(50, 0); !r.Pass {
		t.Errorf("无有效峰值应跳过检查, got %+v", r)
	}
	rc.M8Enabled = false
	if r := e.M8Check(60, 100); !r.Pass {
		t.Errorf("未启用 M8 应跳过, got %+v", r)
	}
}

// TestPositionLimit 常规/N形 仓位截断与总仓位限制。
func TestPositionLimit(t *testing.T) {
	e := newRisk()
	r := e.cfg.Get()
	r.RiskCtrl.PerStockMax = 30
	r.Position.MaxTotalPositionPct = 80

	// N 形仅 90% 单票截断，不受 30%/80% 限制
	if res := e.PositionLimitCheck(0, 50, 50, strategy.SignalNShape); !res.Pass {
		t.Errorf("N形 50%% 应放行, got %+v", res)
	}
	if res := e.PositionLimitCheck(0, 95, 95, strategy.SignalNShape); res.Pass {
		t.Errorf("N形 95%% 应被 90%% 截断, got %+v", res)
	}

	// 常规：单票超限 → block
	if res := e.PositionLimitCheck(0, 40, 40, strategy.SignalDragon); res.Pass {
		t.Errorf("常规单票 40%% > 30%% 应 block, got %+v", res)
	}
	// 常规：总仓位超限 → reduce
	if res := e.PositionLimitCheck(0, 20, 90, strategy.SignalDragon); res.Pass {
		t.Errorf("常规总仓位 90%% > 80%% 应 reduce, got %+v", res)
	}
	// 常规：在限内 → pass
	if res := e.PositionLimitCheck(0, 20, 50, strategy.SignalDragon); !res.Pass {
		t.Errorf("常规 单票20/总50 应放行, got %+v", res)
	}
}
