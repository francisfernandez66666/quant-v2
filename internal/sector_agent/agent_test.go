// sector_agent 板代理测试：状态机分类、验证补 RPS 排名、空输入/空依赖容错。
package sector_agent

import (
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy_engine"
)

// TestClassifyPhase 板块状态机四象限。
func TestClassifyPhase(t *testing.T) {
	cases := []struct {
		name string
		c, f float64
		want string
	}{
		{"涨+流入→加强", 2.0, 1e8, "加强"},
		{"涨+流出→持续", 1.0, -1e8, "持续"},
		{"跌+流出→退潮", -2.0, -1e8, "退潮"},
		{"跌+流入→反弹", -1.0, 1e8, "反弹"},
		{"零涨→反弹(兜底)", 0, 0, "反弹"},
	}
	for _, tc := range cases {
		if got := classifyPhase(tc.c, tc.f); got != tc.want {
			t.Errorf("%s: classifyPhase(%.1f,%.1f)=%s, want %s", tc.name, tc.c, tc.f, got, tc.want)
		}
	}
}

// TestVerifyEmptyInput 空输入返回 nil。
func TestVerifyEmptyInput(t *testing.T) {
	a := New(nil, data.NewRPSManager())
	if got := a.Verify(nil); got != nil {
		t.Error("空输入 Verify 应返回 nil")
	}
}

// TestVerifyRPSEnrichment 命名 RPS 中的板块应补全排名与强度，保留事件方向/评分。
func TestVerifyRPSEnrichment(t *testing.T) {
	rps := data.NewRPSManager()
	rps.Update([]data.SectorRPS{
		{Name: "人工智能", Code: "300xxx", RPS20: 92, RPS60: 85},
		{Name: "创新药", Code: "500xxx", RPS20: 40, RPS60: 30},
	})
	a := New(nil, rps)

	hot := []strategy_engine.SectorHot{
		{Name: "人工智能", Direction: "利好", Score: 0.75, ChangePct: 2.5, NetInflow: 3e9, LimitupCnt: 3, Reason: "政策"},
		{Name: "创新药", Direction: "利空", Score: -0.75, ChangePct: -1.2, NetInflow: -2e9, Reason: "集采"},
	}
	res := a.Verify(hot)
	if len(res) != 2 {
		t.Fatalf("应验证 2 个板块, got %d", len(res))
	}
	ai := res[0]
	if ai.Name != "人工智能" || ai.Score != 0.75 || ai.Direction != "利好" {
		t.Errorf("人工智能字段保留异常: %+v", ai)
	}
	if ai.RPSRank != 1 {
		t.Errorf("人工智能 RPSRank 应=1, got %d", ai.RPSRank)
	}
	if ai.RPS20 != 92 {
		t.Errorf("人工智能 RPS20 应=92, got %.1f", ai.RPS20)
	}
	if ai.Phase != "加强" {
		t.Errorf("人工智能相位应为 加强, got %s", ai.Phase)
	}
	xy := res[1]
	if xy.Phase != "退潮" {
		t.Errorf("创新药 相位应为 退潮, got %s", xy.Phase)
	}
	// 创新药 RPS20 低，非 Top 板块 → 无排名（保持 0），属预期行为
	if xy.RPSRank != 0 {
		t.Errorf("创新药(非Top) RPSRank 应保持 0, got %d", xy.RPSRank)
	}
}

// TestVerifyNilScannerNoPanic scanner 为 nil 时成分股为空但不报错。
func TestVerifyNilScannerNoPanic(t *testing.T) {
	a := New(nil, nil)
	res := a.Verify([]strategy_engine.SectorHot{{Name: "人工智能", Direction: "利好", Score: 0.5}})
	if len(res) != 1 {
		t.Fatalf("应有 1 个结果, got %d", len(res))
	}
	if len(res[0].Stocks) != 0 {
		t.Error("无 scanner 时成分股应为空")
	}
}

// TestFeedRPSNoop 空输入 FeedRPS 为无操作。
func TestFeedRPSNoop(t *testing.T) {
	a := New(nil, data.NewRPSManager())
	a.FeedRPS(nil) // 不应 panic
	a.FeedRPS([]data.SectorRPS{{Name: "X", Code: "1", RPS20: 90}})
}