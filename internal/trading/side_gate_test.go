// side_gate_test.go — §N-4（2026-09-22 修复批）控制器侧的方向白名单接线测试。
//
// 为什么单独测这一层：risk.Gate 内部已有白名单（见 internal/risk 同名测试），但**控制器是唯一
// 生产调用方**——真正的危险形态是"闸拒了、下游却按另一个方向继续走"：
// placeOrder 的执行器分发是 `if req.Side == SideSell { PlaceSell } else { PlaceBuy }` 的二分，
// 非法串会被默认成**买入**落到柜台；executor 之前若有任何一处忘了过闸，方向翻转就直接发生。
// 本测试锁住三件事：① 非法方向被拒且**绝不触达执行器**；② 合法两值各自走对分支
// （买入→PlaceBuy / 卖出→PlaceSell，防"改白名单时顺手把方向判反"）；③ 空方向同样被拒
// （控制器不做缺省，缺省只属于 HTTP 入口的兼容契约）。
//
// English: §N-4 controller-level wiring lock — an unknown side must never reach the executor
// (whose dispatch folds unknown into a real direction), while the two canonical sides must keep
// dispatching to their own branches.
package trading

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// sideSpyExec 记录买/卖两条腿各被触达几次（断言非法方向零触达）。
type sideSpyExec struct {
	buys  int
	sells int
}

// PlaceBuy 桩：计一次买入触达。
func (s *sideSpyExec) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	s.buys++
	return &OrderResult{OK: true, OrderID: "GW-B"}, nil
}

// PlaceSell 桩：计一次卖出触达。
func (s *sideSpyExec) PlaceSell(req OrderRequest) (*OrderResult, error) {
	s.sells++
	return &OrderResult{OK: true, OrderID: "GW-S"}, nil
}

// Cancel Cancel（Stub方法）。
func (s *sideSpyExec) Cancel(orderID string) error { return nil }

// State 测试桩：返回已连接网关状态。
func (s *sideSpyExec) State() (*GatewayState, error) { return &GatewayState{Connected: true}, nil }

// Health 测试桩：返回健康。
func (s *sideSpyExec) Health() (bool, error) { return true, nil }

// TestPlaceOrderUnknownSideNeverReachesExecutor §N-4：非法/缺失方向一律拒单，执行器零触达。
func TestPlaceOrderUnknownSideNeverReachesExecutor(t *testing.T) {
	exec := &sideSpyExec{}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	ctrl := NewController(exec, testDB(t), "u_side", cfg, nil)

	for i, side := range []string{"buy", "BUY", "SELL", "卖出 ", "买", "", "???"} {
		id := "SIG-SIDE-" + string(rune('A'+i))
		_, err := ctrl.PlaceOrder(OrderRequest{SignalID: id, Code: "600000.SH", Name: "浦发",
			Side: side, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)})
		if err == nil || !strings.Contains(err.Error(), "非法下单方向") {
			t.Fatalf("方向 %q 应被拒且原因自解释, got %v", side, err)
		}
	}
	if exec.buys+exec.sells != 0 {
		t.Fatalf("非法方向绝不能触达柜台（旧形态：else 分支默认成买入）, buys=%d sells=%d", exec.buys, exec.sells)
	}

	// 合法方向各自走对分支（回归护栏：白名单改动不得把方向判反）
	if _, err := ctrl.PlaceOrder(OrderRequest{SignalID: "SIG-SIDE-BUY-OK", Code: "600000.SH", Name: "浦发",
		Side: SideBuy, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatalf("合法买入应放行: %v", err)
	}
	if _, err := ctrl.PlaceOrder(OrderRequest{SignalID: "SIG-SIDE-SELL-OK", Code: "600000.SH", Name: "浦发",
		Side: SideSell, Price: 10, Qty: 100, Amount: 1000, CreatedAt: time.Now().Format(time.RFC3339)}); err != nil {
		t.Fatalf("合法卖出应放行（本账号无持仓，T+1 闸对未知仓位 fail-open）: %v", err)
	}
	if exec.buys != 1 || exec.sells != 1 {
		t.Fatalf("方向分发错误: buys=%d sells=%d（应各 1）", exec.buys, exec.sells)
	}
}
