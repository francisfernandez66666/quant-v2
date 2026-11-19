// gate_quadrant_test.go — §FLOW-QUADRANT 执行层硬拒买闸锁。
// 三条：① block_buy 买单被拒（gate=quadrant_block、alert 必推）；② 卖单不拦（敞口可退出）；
// ③ 空/其它决策放行（缺数据 fail-open，探针未回不误伤）。
package risk

import "testing"

// TestGateQuadrantBlockBuy 四象限 block_buy 拒买，且命中闸名正确。
func TestGateQuadrantBlockBuy(t *testing.T) {
	g := NewGate(gateDB(t), "u_quad", nil)
	o := liveOrder(SideBuy)
	o.QuadrantDecision = "block_buy"
	o.QuadrantReason = "bear_surge_high_nobottom"
	v := g.CheckLiveOrder(qmtCfg(), o)
	if v.Pass {
		t.Fatalf("block_buy 买单应被拒, got %+v", v)
	}
	if v.Gate != "quadrant_block" {
		t.Fatalf("命中闸应为 quadrant_block, got %q", v.Gate)
	}
	if v.Blocked {
		// alert=true 的必推由 verdict 的 onGate 回调完成（§XCHECK 同机制），本处只锁拒单与闸名。
		_ = v
	}
}

// TestGateQuadrantSellNotBlocked 卖单不受四象限拒买闸影响（已有敞口必须能退出）。
func TestGateQuadrantSellNotBlocked(t *testing.T) {
	g := NewGate(gateDB(t), "u_quad", nil)
	o := liveOrder(SideSell)
	o.QuadrantDecision = "block_buy"
	if v := g.CheckLiveOrder(qmtCfg(), o); !v.Pass {
		t.Fatalf("卖单不应被四象限拒买闸拦, got %+v", v)
	}
}

// TestGateQuadrantFailOpen 空决策/非 block_buy（含探针未回）一律放行。
func TestGateQuadrantFailOpen(t *testing.T) {
	g := NewGate(gateDB(t), "u_quad", nil)
	for _, d := range []string{"", "neutral", "reduce", "block_add"} {
		o := liveOrder(SideBuy)
		o.QuadrantDecision = d
		if v := g.CheckLiveOrder(qmtCfg(), o); !v.Pass {
			t.Fatalf("决策 %q 应 fail-open 放行, got %+v", d, v)
		}
	}
}
