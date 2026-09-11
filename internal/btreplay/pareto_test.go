// pareto_test.go — §回测自动增强模块 C：四维 skyline 与朴素 O(n²) 参照实现一致、
// 最小样本过滤、硬门槛推荐解、前沿截断、10 万组合性能冒烟。
// English: skyline correctness vs naive reference, min-sample gate, recommended solution,
// front capping and a 100k-combo perf smoke.
package btreplay

import (
	"math/rand"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// res 构造一个前沿候选（简写）。
func res(wr, pf, sh, cal float64, count int) sweepResult {
	return sweepResult{WinRate: wr, ProfitFactor: pf, Sharpe: sh, Calmar: cal, Count: count}
}

// naiveFront 朴素参照实现：两两支配枚举（正确性锚点，仅测试小规模使用）。
func naiveFront(in []sweepResult) []sweepResult {
	var out []sweepResult
	for i := range in {
		if in[i].Count < sweepMinTrades {
			continue
		}
		bad := false
		for j := range in {
			if j == i || in[j].Count < sweepMinTrades {
				continue
			}
			if dominates(&in[j], &in[i]) {
				bad = true
				break
			}
		}
		if !bad {
			out = append(out, in[i])
		}
	}
	return out
}

// sameSet 两个前沿集合按指标四元组相等（多重集）。
func sameSet(a, b []sweepResult) bool {
	key := func(r sweepResult) [4]float64 { return [4]float64{r.WinRate, r.ProfitFactor, r.Sharpe, r.Calmar} }
	ma := map[[4]float64]int{}
	mb := map[[4]float64]int{}
	for _, r := range a {
		ma[key(r)]++
	}
	for _, r := range b {
		mb[key(r)]++
	}
	if len(ma) != len(mb) {
		return false
	}
	for k, v := range ma {
		if mb[k] != v {
			return false
		}
	}
	return true
}

// TestParetoVsNaive 随机结果集上 skyline 与朴素枚举一致（含被支配、互相非支配、全同分）。
func TestParetoVsNaive(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	for iter := 0; iter < 30; iter++ {
		n := 50 + rnd.Intn(150)
		results := make([]sweepResult, 0, n)
		for i := 0; i < n; i++ {
			results = append(results, res(
				rnd.Float64()*100, rnd.Float64()*5, rnd.Float64()*3, rnd.Float64()*3,
				rnd.Intn(60))) // 部分触发数 <20 应被剔除
		}
		front := paretoFront(results, 0)
		ref := naiveFront(results)
		if !sameSet(front, ref) {
			t.Fatalf("iter %d: skyline 与朴素参照不一致 (front=%d ref=%d)", iter, len(front), len(ref))
		}
	}
}

// TestParetoEdgeCases 空集/低样本/全同分边界。
func TestParetoEdgeCases(t *testing.T) {
	if f := paretoFront(nil, 50); len(f) != 0 {
		t.Fatal("空输入应为空前沿")
	}
	low := []sweepResult{res(90, 5, 5, 5, 5)} // 触发 5 < 20 → 剔除
	if f := paretoFront(low, 50); len(f) != 0 {
		t.Fatal("低样本组合应被剔除")
	}
	dup := []sweepResult{res(50, 2, 1, 1, 30), res(50, 2, 1, 1, 30), res(50, 2, 1, 1, 25)}
	f := paretoFront(dup, 50)
	if len(f) != 3 { // 全同分互不支配（Count 不参与支配），都在前沿
		t.Fatalf("全同分应互不支配, got %d", len(f))
	}
	// 单点支配链：A 支配 B 支配 C → 只剩 A（Count 不是目标维，不参与支配）
	chain := []sweepResult{res(60, 3, 2, 2, 30), res(50, 2, 1, 1, 40), res(40, 1, 0.5, 0.5, 100)}
	f2 := paretoFront(chain, 50)
	if len(f2) != 1 || f2[0].WinRate != 60 {
		t.Fatalf("支配链应只剩 A, got %+v", f2)
	}
	strict := []sweepResult{res(60, 3, 2, 2, 30), res(50, 2, 1, 1, 40)}
	f3 := paretoFront(strict, 50)
	if len(f3) != 1 || f3[0].WinRate != 60 {
		t.Fatalf("严格支配应只剩 A, got %+v", f3)
	}
}

// TestCapFront 超限按触发数降序截断、保持胜率降序展示。
func TestCapFront(t *testing.T) {
	front := []sweepResult{res(90, 1, 0, 0, 21), res(70, 2, 0, 0, 999), res(50, 3, 0, 0, 500)}
	capped := capFront(front, 2)
	if len(capped) != 2 {
		t.Fatalf("len=%d", len(capped))
	}
	if capped[0].Count != 999 || capped[1].Count != 500 { // Count 降序入选
		t.Fatalf("截断未按触发数保优: %+v", capped)
	}
	if capped[0].WinRate < capped[1].WinRate { // 展示仍按胜率降序
		t.Fatal("截断后应恢复胜率降序")
	}
}

// TestRecommendedSolution 门槛全过取 Sharpe 最高、并列取 Count 多者、全灭返回 nil。
func TestRecommendedSolution(t *testing.T) {
	cfg := config.ParetoConfig{MinWinRate: 30, MinProfitFactor: 1, MinSharpe: 0.5, MinCalmar: 0.5}
	front := []sweepResult{
		res(40, 1.5, 0.6, 0.6, 30),
		res(35, 2.0, 0.9, 1.0, 25), // Sharpe 最高 → 推荐
		res(60, 1.2, 0.4, 0.9, 100),
	}
	r := recommendedSolution(front, cfg)
	if r == nil || !nearly(r.ProfitFactor, 2.0) {
		t.Fatalf("推荐解错误: %+v", r)
	}
	// 并列 Sharpe 取触发数多者
	front[0].Sharpe = 0.9
	front[0].Count = 80
	r = recommendedSolution(front, cfg)
	if r == nil || !nearly(r.WinRate, 40) || r.Count != 80 {
		t.Fatalf("并列应取样本多者: %+v", r)
	}
	// 门槛全灭 → nil
	tight := config.ParetoConfig{MinWinRate: 90, MinProfitFactor: 5, MinSharpe: 5, MinCalmar: 5}
	if r := recommendedSolution(front, tight); r != nil {
		t.Fatalf("门槛全灭应返回 nil, got %+v", r)
	}
}

// TestParetoPerfSmoke 10 万组合 skyline 性能冒烟（目标 <5s，朴素 O(n²) 需 ~分钟级）。
func TestParetoPerfSmoke(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	results := make([]sweepResult, 100000)
	for i := range results {
		results[i] = res(rnd.Float64()*100, rnd.Float64()*5, rnd.Float64()*3,
			rnd.Float64()*3, 20+rnd.Intn(300))
	}
	start := time.Now()
	front := paretoFront(results, 50)
	elapse := time.Since(start)
	if len(front) > 50 {
		t.Fatalf("前沿未截断: %d", len(front))
	}
	if elapse > 10*time.Second {
		t.Fatalf("10 万组合 skyline 过慢: %v", elapse)
	}
	t.Logf("paretoFront(100k) = %v, front=%d", elapse, len(front))
}

// TestPointJSON nil 安全（recommended 无解 → JSON null）。
func TestPointJSON(t *testing.T) {
	if paretoPointJSON(nil) != nil {
		t.Fatal("nil 解应为 nil map")
	}
	r := res(45, 2.1, 1.3, 1.8, 120)
	m := paretoPointJSON(&r)
	if m["win_rate"] != 45.0 || m["trigger_count"] != 120 {
		t.Fatalf("点序列化异常: %+v", m)
	}
}
