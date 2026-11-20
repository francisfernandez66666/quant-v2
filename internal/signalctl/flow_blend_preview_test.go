// flow_blend_preview_test.go — §FLOW-BLEND 预览入口（仪表盘读数）锁。
//
// 这里要证的不是"公式对不对"（那是 flow_blend_test.go 的活），而是**展示层没有第二份公式**：
// 预览值必须与 ApplyFlowBlend 逐位一致，含封顶、renormalize 与 NaN 弃权三条语义。
// 反证：若有人把 FlowBlendPreview 改成自己写 `1+0.2*b` 并漏掉封顶/renormalize，
// TestFlowBlendPreviewMatchesApply 会红。
package signalctl

import (
	"math"
	"testing"
)

// TestFlowBlendPreviewMatchesApply 预览与下单链逐位一致（三条语义各取一例）。
func TestFlowBlendPreviewMatchesApply(t *testing.T) {
	cases := []struct {
		name                string
		market, sector, vol float64
		wantOK              bool
	}{
		{"三项全强", 1, 1, 1, true},
		{"三项全弱", -1, -1, -1, true},
		{"仅大盘强", 1, math.NaN(), math.NaN(), true},
		{"仅量能平量", math.NaN(), math.NaN(), 0, true},
		{"全弃权", math.NaN(), math.NaN(), math.NaN(), false},
	}
	for _, c := range cases {
		got, ok := FlowBlendPreview(c.market, c.sector, c.vol)
		if ok != c.wantOK {
			t.Fatalf("%s: ok=%v want %v", c.name, ok, c.wantOK)
		}
		// 真值一律由 ApplyFlowBlend 现算，不在用例里再写一份期望公式。
		meta := map[string]float64{}
		for k, v := range map[string]float64{
			MetaFlowMarketDelta: c.market, MetaFlowSectorDelta: c.sector, MetaVolSurgeDelta: c.vol,
		} {
			if !math.IsNaN(v) {
				meta[k] = v
			}
		}
		want := 1.0
		if len(meta) > 0 {
			res := SizingResult{}
			ApplyFlowBlend(&res, flowSig(meta))
			want = res.FlowBlend
		}
		if math.Abs(got-want) > 1e-12 {
			t.Fatalf("%s: preview=%.6f apply=%.6f（展示层不许另写公式）", c.name, got, want)
		}
	}
}

// TestFlowBlendPreviewCapped 预览同样吃 ±20% 封顶（不是"三项各 20%"）。
func TestFlowBlendPreviewCapped(t *testing.T) {
	if got, ok := FlowBlendPreview(1, 1, 1); !ok || math.Abs(got-1.20) > 1e-9 {
		t.Fatalf("三项全强应封顶 1.20（ok=%v）实际 %.4f", ok, got)
	}
	if got, ok := FlowBlendPreview(-1, -1, -1); !ok || math.Abs(got-0.80) > 1e-9 {
		t.Fatalf("三项全弱应封顶 0.80（ok=%v）实际 %.4f", ok, got)
	}
}

// TestFlowBlendPreviewAbstainIsNotNeutralZero 全弃权返回 ok=false：调用方必须显示"弃权"，
// 不能把 1.0 当成"有一个有效中性读数"画上去（那会把缺数和真平量混成同形）。
func TestFlowBlendPreviewAbstainIsNotNeutralZero(t *testing.T) {
	blend, ok := FlowBlendPreview(math.NaN(), math.NaN(), math.NaN())
	if ok {
		t.Fatal("三项全 NaN 必须是 ok=false（弃权），否则前端无法区分「没有读数」与「中性读数」")
	}
	if blend != 1.0 {
		t.Fatalf("弃权时乘子取中性 1.0（零行为变更），实际 %.4f", blend)
	}
}
