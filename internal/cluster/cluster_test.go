// cluster_test.go — §T-COST B3 相关性簇的判据（施工图 §5.5）。
//
// 四条断言：
//  1. 双轨合并：ρ≥0.7 的两只并簇；同行业但 ρ 很低的两只也并簇（取严）；
//  2. 簇 id 稳定：等于簇内最小代码（日志对账靠它，不许随机/顺序相关）；
//  3. 读缺失 fail-closed：重叠样本 < minObs 的**已比较**对按同簇处理，并且 UnknownPairs 计数；
//  4. 值聚合不含目标自身（ValueOf 的自排除）。
//
// 反证靶心：把 Pearson 的阈值判断改成 `r < rho`（方向写反）⇒ 判据 1 必红；
// 把 minObs 分支改成"不并簇"⇒ 判据 3 必红（那正是"读不到就当成分散"的危险方向）。
package cluster

import (
	"math"
	"testing"
)

// mkSeries 造一段收益序列：按给定日期列表逐日写入同一个收益率（便于精确构造相关性）。
func mkSeries(days []string, rets []float64) Series {
	s := Series{}
	for i, d := range days {
		s[d] = rets[i]
	}
	return s
}

// daysOf 造 n 个连续日期键（只作键用，不参与日期运算）。
func daysOf(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = "2026" + string(rune('0'+i/10%10)) + string(rune('0'+i%10)) + "01"
	}
	return out
}

// TestPearsonOverlapAndNaN Pearson 只在重叠日上算；样本不足/零方差返回 NaN 而不是 0。
func TestPearsonOverlapAndNaN(t *testing.T) {
	a := mkSeries([]string{"d1", "d2", "d3", "d4"}, []float64{0.01, -0.02, 0.03, 0.005})
	b := mkSeries([]string{"d1", "d2", "d3", "d4"}, []float64{0.02, -0.04, 0.06, 0.01}) // 恰好 2a
	rho, n := Pearson(a, b)
	if n != 4 || math.Abs(rho-1) > 1e-9 {
		t.Fatalf("完全线性同向应得 ρ=1 / n=4，got ρ=%.6f n=%d", rho, n)
	}
	// 停牌日缺席不补 0：b 少一天，重叠变成 3 天，但相关性仍是 1（补 0 会把它拉到 1 以下）。
	b2 := mkSeries([]string{"d1", "d2", "d3"}, []float64{0.02, -0.04, 0.06})
	if rho2, n2 := Pearson(a, b2); n2 != 3 || math.Abs(rho2-1) > 1e-9 {
		t.Fatalf("停牌日应缺席（不补 0）：ρ=%.6f n=%d", rho2, n2)
	}
	// 样本不足 ⇒ NaN（不是 0）。
	if rho3, n3 := Pearson(mkSeries([]string{"d1"}, []float64{0.01}), a); n3 != 1 || !math.IsNaN(rho3) {
		t.Fatalf("重叠 1 天必须返回 NaN，got ρ=%v n=%d", rho3, n3)
	}
	// 零方差 ⇒ NaN（一字板/整段停牌）。
	flat := mkSeries([]string{"d1", "d2", "d3"}, []float64{0, 0, 0})
	if rho4, _ := Pearson(flat, a); !math.IsNaN(rho4) {
		t.Fatalf("零方差必须返回 NaN（相关系数没有定义），got %v", rho4)
	}
}

// TestBuildTwoTracks 双轨：相关性轨与行业轨各自并簇，且簇 id 取最小代码。
//
// 两条轨必须**分开验**：同一条 Build 里混进"没有任何序列的代码"时，fail-closed 会把它与所有票
// 并成一簇（那是刻意的），于是"ρ=−1 不许同簇"这条就被掩盖掉了——判据被另一条规则吞掉是假绿。
func TestBuildTwoTracks(t *testing.T) {
	days := daysOf(60)
	// A/B 完全同向（ρ=1）；C 与 A 反向（ρ=−1）。
	base := make([]float64, 60)
	for i := range base {
		base[i] = math.Sin(float64(i)) / 100
	}
	neg := make([]float64, 60)
	for i := range base {
		neg[i] = -base[i]
	}
	// —— 相关性轨 ——
	rets := map[string]Series{
		"600001": mkSeries(days, base),
		"600002": mkSeries(days, base),
		"600003": mkSeries(days, neg),
	}
	idx := Build([]string{"600001", "600002", "600003"}, rets, map[string]string{}, 0, 0)
	if !idx.Same("600001", "600002") {
		t.Fatalf("ρ=1 的两只必须同簇：%s/%s", idx.ClusterOf("600001"), idx.ClusterOf("600002"))
	}
	if idx.Same("600001", "600003") {
		t.Fatalf("ρ=−1 的两只不许同簇（反向对冲是分散，不是集中）")
	}
	if idx.ClusterOf("600001") != "600001" {
		t.Fatalf("簇 id 必须取簇内最小代码 600001，got %s", idx.ClusterOf("600001"))
	}
	if idx.UnknownPairs() != 0 {
		t.Fatalf("两只都有充足序列时不应计入缺读数对数，got %d", idx.UnknownPairs())
	}
	// —— 行业轨 ——
	// 两只行业相同、但收益几乎不相关（一正一反的方波）：行业是硬事实，直接并簇，不看系数。
	up := make([]float64, 60)
	for i := 0; i < 60; i++ {
		up[i] = 0.01
		if i%2 == 0 {
			up[i] = -0.01
		}
	}
	dn := make([]float64, 60)
	for i := range up {
		dn[i] = -up[i]
	}
	ind := map[string]string{"600004": "银行", "600009": "银行"}
	idx2 := Build([]string{"600004", "600009"},
		map[string]Series{"600004": mkSeries(days, up), "600009": mkSeries(days, dn)}, ind, 0, 0)
	if !idx2.Same("600004", "600009") {
		t.Fatalf("同行业必须并簇（行业轨取严）：%s/%s", idx2.ClusterOf("600004"), idx2.ClusterOf("600009"))
	}
	// 行业不同且 ρ=−1 ⇒ 不许并簇（证明行业轨只在"行业相同"时生效，不是"有行业就并"）。
	idx3 := Build([]string{"600010", "600011"},
		map[string]Series{"600010": mkSeries(days, up), "600011": mkSeries(days, dn)},
		map[string]string{"600010": "银行", "600011": "券商"}, 0, 0)
	if idx3.Same("600010", "600011") {
		t.Fatalf("行业不同 + ρ=−1 时不许并簇")
	}
}

// TestBuildFailClosedOnMissing 读缺失 fail-closed：样本不足的已比较对按同簇处理并计数。
func TestBuildFailClosedOnMissing(t *testing.T) {
	days := daysOf(10) // 只有 10 天，远低于 minObs=40
	short := mkSeries(days, []float64{0.01, -0.01, 0.02, -0.02, 0.01, -0.01, 0.02, -0.02, 0.01, -0.01})
	rets := map[string]Series{"600001": short, "600002": short}
	idx := Build([]string{"600001", "600002"}, rets, map[string]string{}, 0, 0)
	if !idx.Same("600001", "600002") {
		t.Fatalf("样本不足必须按同簇处理（fail-closed），got %s/%s",
			idx.ClusterOf("600001"), idx.ClusterOf("600002"))
	}
	if idx.UnknownPairs() != 1 {
		t.Fatalf("缺读数并簇的对数必须被计数（报告要点名），got %d", idx.UnknownPairs())
	}
	// 完全没有收益序列的一对：同样并簇（读数不可得，不是"不相关"）。
	idx2 := Build([]string{"600003", "600004"}, map[string]Series{}, map[string]string{}, 0, 0)
	if !idx2.Same("600003", "600004") || idx2.UnknownPairs() != 1 {
		t.Fatalf("无序列对必须并簇且计数，got same=%v unknown=%d",
			idx2.Same("600003", "600004"), idx2.UnknownPairs())
	}
	// 反证：样本充足且确实不相关时**不许**并簇（否则 fail-closed 会吞掉整个分散化）。
	days60 := daysOf(60)
	up := make([]float64, 60)
	dn := make([]float64, 60)
	for i := 0; i < 60; i++ {
		up[i] = 0.01
		if i%2 == 0 {
			up[i] = -0.01
		}
		dn[i] = -up[i]
	}
	idx3 := Build([]string{"600005", "600006"},
		map[string]Series{"600005": mkSeries(days60, up), "600006": mkSeries(days60, dn)},
		map[string]string{}, 0, 0)
	if idx3.Same("600005", "600006") {
		t.Fatalf("ρ=−1 且样本充足时不许并簇（fail-closed 不能吞掉真分散）")
	}
	if idx3.UnknownPairs() != 0 {
		t.Fatalf("读数可得时不应计入缺读数对数，got %d", idx3.UnknownPairs())
	}
}

// TestValueOfExcludesTarget 簇值聚合不含目标自身（含了就等于"自己占掉自己的额度"）。
func TestValueOfExcludesTarget(t *testing.T) {
	days := daysOf(60)
	base := make([]float64, 60)
	for i := range base {
		base[i] = math.Sin(float64(i)) / 100
	}
	rets := map[string]Series{
		"600001": mkSeries(days, base),
		"600002": mkSeries(days, base),
		"600003": mkSeries(days, base),
	}
	idx := Build([]string{"600001", "600002", "600003"}, rets, map[string]string{}, 0, 0)
	vals := map[string]float64{"600001": 100, "600002": 200, "600003": 300}
	// 目标 600002：同簇的 600001(100) + 600003(300) = 400，不含自己那 200。
	if got := ValueOf(idx, "600002", vals); math.Abs(got-400) > 1e-9 {
		t.Fatalf("簇值必须排除目标自身：want 400 got %.2f", got)
	}
	// 未知代码自成簇 ⇒ 簇值 0（它没参与比较，不能把别人的仓位算进来）。
	if got := ValueOf(idx, "600099", vals); got != 0 {
		t.Fatalf("未知代码应自成簇、簇值 0，got %.2f", got)
	}
}
