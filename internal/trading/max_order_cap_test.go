// max_order_cap_test.go §AUDIT-PM 2026-09-15：单笔金额绝对帽（risk_gate.max_order_amount）
// 的控制器层契约——UpdateConfig 保存即生效（与 kill-switch 同口径，不滞留开关队列）、
// 买卖双向拒、Amount 缺失回退 qty×参考价、0=关闭放行一切。
// English: controller-level contract for the absolute per-order amount cap: UpdateConfig
// applies immediately (same semantics as the kill-switch, bypassing the switch queue),
// rejects both sides, falls back to qty×price when Amount is unset, and 0 disables.
package trading

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// TestMaxOrderAmountCapImmediate 帽经 UpdateConfig 变更后下一单立即按新值判定。
func TestMaxOrderAmountCapImmediate(t *testing.T) {
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.RiskGate.MaxOrderAmount = 500
	ctrl := NewController(guardServer(), testDB(t), "u_cap", cfg, nil)

	// 超限买单被拒（Amount=1000 > 帽 500）
	if _, err := ctrl.PlaceOrder(buyReq("SIG-CAP-BUY", 1000)); err == nil || !strings.Contains(err.Error(), "单笔金额超限") {
		t.Fatalf("1000 > 帽500 应拒, got %v", err)
	}
	// 抬帽至 2000 → 立即放行（证明 UpdateConfig 直刷 gate 读到的 cfg，而非等休市队列）
	cfg.RiskGate.MaxOrderAmount = 2000
	ctrl.UpdateConfig(cfg)
	if _, err := ctrl.PlaceOrder(buyReq("SIG-CAP-RAISE", 1000)); err != nil {
		t.Fatalf("抬帽后应立即放行, got %v", err)
	}
	// 卖单同受帽约束
	if _, err := ctrl.PlaceOrder(OrderRequest{SignalID: "SIG-CAP-SELL", Code: "600000.SH", Name: "浦发",
		Side: SideSell, Price: 10, Qty: 100, Amount: 3000, CreatedAt: time.Now().Format(time.RFC3339)}); err == nil || !strings.Contains(err.Error(), "单笔金额超限") {
		t.Fatalf("卖单 3000 > 帽2000 应拒, got %v", err)
	}
	// Amount 缺失 → 回退 qty(300)×price(10)=3000 > 帽 2000 仍拒（防装配缺口绕行）
	if _, err := ctrl.PlaceOrder(OrderRequest{SignalID: "SIG-CAP-FALLBACK", Code: "600000.SH", Name: "浦发",
		Side: SideBuy, Price: 10, Qty: 300, CreatedAt: time.Now().Format(time.RFC3339)}); err == nil || !strings.Contains(err.Error(), "单笔金额超限") {
		t.Fatalf("Amount 缺失应回退 qty×价 判定, got %v", err)
	}
	// 帽=0 关闭：超过原帽金额的放行（默认配置零行为变化；金额留在日预算内，验证的是帽本身）
	cfg.RiskGate.MaxOrderAmount = 0
	ctrl.UpdateConfig(cfg)
	if _, err := ctrl.PlaceOrder(buyReq("SIG-CAP-OFF", 20000)); err != nil {
		t.Fatalf("帽=0 应放行一切, got %v", err)
	}
}
