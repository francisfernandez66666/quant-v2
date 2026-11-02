// missing_key_dims_test.go — §INDEX-BLIND（2026-11-02）缺失维度清单的出口用例。
//
// 三件事，全部对着 11-02 那笔账写：
//  1. 每一维单独缺失 ⇒ dims 只点它自己（清单与 bool 必须同真同假）；
//  2. **两个装配分支都要覆盖**：v4 出厂走 power_mean，只测 legacy 那条等于没测
//     （"默认一切换就丢维度"是本批的靶子之一）；
//  3. 被 owner 主动关掉的维度不算盲区、广度缺失不算关键缺失（同 §1.4 与 §N6-RISK 的裁决，
//     别把同一件事罚两次，也别把"关功能"读成"看不清水位"）。
package signalctl

import (
	"math"
	"testing"

	"quant-trading-v2/internal/combat_agent"
)

// dimsOf 跑一轮定权并取回维度清单。
func dimsOf(mutate func(*Regime)) (bool, []string, float64) {
	r := baseRegime()
	r.IndexChangePct = 0.5 // 默认给一份**有**的大盘读数，避免与"缺失"混在一起
	mutate(&r)
	res := ComputeSizing(r, combat_agent.Signal{})
	return res.MissingKeyInput, res.MissingKeyDims, res.MarketScale
}

// TestMissingKeyDimsEnumeratesEachDimension 三键各自单独缺失 → 清单逐一对应。
func TestMissingKeyDimsEnumeratesEachDimension(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Regime)
		wantDim string
	}{
		{"状态机开着却没读数", func(r *Regime) { r.MarketStateEnabled, r.MarketState = true, "" }, MissingDimMarketState},
		{"情绪已配置却没产出", func(r *Regime) { r.EmotionEnabled, r.EmotionPhase = true, "" }, MissingDimEmotionPhase},
		{"大盘指数 NaN", func(r *Regime) { r.IndexChangePct = math.NaN() }, MissingDimIndexChange},
	}
	for _, c := range cases {
		miss, dims, ms := dimsOf(c.mut)
		if !miss {
			t.Fatalf("%s：应判盲区", c.name)
		}
		if len(dims) != 1 || dims[0] != c.wantDim {
			t.Fatalf("%s：清单应为 [%s]，实得 %v", c.name, c.wantDim, dims)
		}
		if ms > 0.5001 {
			t.Fatalf("%s：应封顶 0.50，实得 %.3f", c.name, ms)
		}
	}
}

// TestMissingKeyDimsListIsStableAndCombined 多维同时缺失：清单齐全且顺序稳定（市场状态→情绪→指数）。
// 顺序稳定的原因很实际：前端与告警文案直接渲染这一串，乱序会让同一天的同一状态打出两种文案。
func TestMissingKeyDimsListIsStableAndCombined(t *testing.T) {
	miss, dims, _ := dimsOf(func(r *Regime) {
		r.MarketStateEnabled, r.MarketState = true, ""
		r.EmotionEnabled, r.EmotionPhase = true, ""
		r.IndexChangePct = math.NaN()
	})
	if !miss {
		t.Fatalf("三维全缺应判盲区")
	}
	want := MissingDimMarketState + "|" + MissingDimEmotionPhase + "|" + MissingDimIndexChange
	if joinDims(dims) != want {
		t.Fatalf("清单顺序/内容不对：得 %v，要 %s", dims, want)
	}
}

// TestMissingDimsAbsentMeansNotBlind 三维齐 ⇒ 不盲区、不封顶、清单为空。
func TestMissingDimsAbsentMeansNotBlind(t *testing.T) {
	miss, dims, ms := dimsOf(func(r *Regime) {})
	if miss || len(dims) != 0 {
		t.Fatalf("齐全却报了盲区：%v %v", miss, dims)
	}
	if ms <= 0.5001 {
		t.Fatalf("齐全却被封顶了（%.3f）——封顶只许跟着盲区走", ms)
	}
}

// TestDisabledDimensionNotBlind 维度被 owner 主动关掉 ≠ 盲区（否则关掉状态机就等于永久半仓）。
func TestDisabledDimensionNotBlind(t *testing.T) {
	miss, dims, _ := dimsOf(func(r *Regime) { r.MarketStateEnabled, r.MarketState = false, "" })
	if miss || len(dims) != 0 {
		t.Fatalf("关掉的维度被判成盲区：%v %v", miss, dims)
	}
}

// TestBreadthMissingNotKeyInput 广度缺失不计关键缺失（它已按 breadth_missing_scale 小降权，再计一次是双罚）。
func TestBreadthMissingNotKeyInput(t *testing.T) {
	miss, dims, _ := dimsOf(func(r *Regime) { r.BreadthUpRatio = math.NaN() })
	if miss || len(dims) != 0 {
		t.Fatalf("广度缺失不该触发封顶：%v %v", miss, dims)
	}
}

// TestMissingKeyDimsCoveredInBothSizingBranches 两个装配分支都必须带出清单。
// 只测一支的话，出厂合成方式一改（v3→v4 就是一次真改动）维度出口可以悄悄消失。
func TestMissingKeyDimsCoveredInBothSizingBranches(t *testing.T) {
	r := baseRegime()
	r.IndexChangePct = math.NaN()
	for _, mode := range []string{"product", "power_mean"} {
		cfg := r.SizeCfg
		cfg.Mode = mode
		rr := r
		rr.SizeCfg = cfg
		res := ComputeSizing(rr, combat_agent.Signal{})
		if !res.MissingKeyInput {
			t.Fatalf("合成方式 %q 下没判盲区（判据分叉了）", mode)
		}
		if len(res.MissingKeyDims) != 1 || res.MissingKeyDims[0] != MissingDimIndexChange {
			t.Fatalf("合成方式 %q 下清单丢了/错了：%v", mode, res.MissingKeyDims)
		}
	}
}

// TestDimsSliceIsDeepCopied 清单必须是副本：决策对象/资金帧/告警都会持有它，
// 共享底层数组等于允许后一个持有者改到前一个的语义。
func TestDimsSliceIsDeepCopied(t *testing.T) {
	r := baseRegime()
	r.IndexChangePct = math.NaN()
	res := ComputeSizing(r, combat_agent.Signal{})
	if len(res.MissingKeyDims) == 0 {
		t.Fatalf("没有清单可测")
	}
	res.MissingKeyDims[0] = "tampered"
	again := ComputeSizing(r, combat_agent.Signal{})
	if again.MissingKeyDims[0] == "tampered" {
		t.Fatalf("清单被调用方写坏了（返回的是内部切片）")
	}
}

// joinDims 拼接清单（避免在断言里重复 strings.Join 的导入）。
func joinDims(d []string) string {
	out := ""
	for i, v := range d {
		if i > 0 {
			out += "|"
		}
		out += v
	}
	return out
}
