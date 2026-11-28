// market_scalar_test.go — §REPLAY-INPUTS 2026-11-28（档 2）：市场级标量的带值与跨侧对齐锁。
//
// 这里钉的是四类会"静默算错"的形状：
//  1. **端点与尺度**：linear 的中点必须得 0.5（丢强度＝把 5 分利好和 40 分利好算成一样），
//     二值态必须与载体的 `low <= n < high` **右开**逐字同形——差一个端点两侧就会分家；
//  2. **NaN 与缺带都是弃权**，不是 0 分；倒置带也必须显形（NaN）而不是常数；
//  3. **跨侧对齐**：Go 的带值表必须是载体 `params.scalar_bands` 的**子集**且逐键逐值相等
//     （不是等集——载体还有 lu_board/stom_21 等 Go 侧不注册的标量）。
//     这条靠肉眼比对迟早漂，所以直接读仓库里那份 json 做 dict 比较；
//  4. **代理不得借用真名**：Go 侧不注册 ConceptHeat / IsLeader（同名不同义会让
//     回测结论被当成实盘行为），载体侧也不得出现两个代理 ID。
//
// English: locks band arithmetic (including the carrier's right-open threshold shape),
// abstention on NaN/missing bands, cross-side subset equality against params.scalar_bands,
// and the rule that the two Go proxies never reuse the carrier's real factor names.
package factor

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestScalarScoreLinearKeepsMagnitude(t *testing.T) {
	b, ok := ScalarBandOf("IndexChange")
	if !ok || b.Min != -2.0 || b.Max != 1.5 {
		t.Fatalf("IndexChange 带值应照抄载体在册 -2.0..1.5，得 %+v ok=%v", b, ok)
	}
	cases := map[string]struct{ in, want float64 }{
		"below":  {-3.0, 0},    // 夹下界
		"at_min": {-2.0, 0},    // 下界含
		"mid":    {-0.25, 0.5}, // 中点必须给 0.5（丢强度就会与两端同分）
		"above":  {9.9, 1},     // 夹上界
		"nan":    {math.NaN(), math.NaN()},
		"posinf": {math.Inf(1), math.NaN()},
		"neginf": {math.Inf(-1), math.NaN()},
	}
	for name, c := range cases {
		got := ScalarScore("IndexChange", c.in)
		if math.IsNaN(c.want) {
			if !math.IsNaN(got) {
				t.Fatalf("%s：期望 NaN 弃权，得 %v", name, got)
			}
			continue
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("%s：ScalarScore(%.2f)=%.6f，期望 %.6f", name, c.in, got, c.want)
		}
	}
}

func TestScalarScoreThresholdShapeIsRightOpen(t *testing.T) {
	// SectorGainRank 是 linear；这里临时注册一条二值带来验证二值态的右开形状。
	// 与载体 `low <= number < high` 必须逐字同形：上界含不含，差一个端点就是一档分。
	scalarBands["_test_threshold"] = ScalarBand{Min: 2, Max: 3, Linear: false}
	defer delete(scalarBands, "_test_threshold")

	if got := ScalarScore("_test_threshold", 2); got != 1 {
		t.Fatalf("下界必须含（与载体同形），得 %v", got)
	}
	if got := ScalarScore("_test_threshold", 2.999); got != 1 {
		t.Fatalf("带内应得 1，得 %v", got)
	}
	if got := ScalarScore("_test_threshold", 3); got != 0 {
		t.Fatalf("上界必须**不含**（right-open，与载体一致），得 %v", got)
	}
	if got := ScalarScore("_test_threshold", 1.9); got != 0 {
		t.Fatalf("带外应得 0，得 %v", got)
	}
}

func TestScalarScoreAbstainsOnMissingOrInvertedBand(t *testing.T) {
	// 未标定的 ID（FlowCapToday/FlowCap5d 故意无带）必须 NaN ⇒ 规则 decline，
	// 而不是拿一个没依据的尺度把整条腿打分。
	if _, ok := ScalarBandOf("FlowCapToday"); ok {
		t.Fatal("FlowCapToday 在 owner 签字前不得有带值（有带＝用错尺度打分）")
	}
	if !math.IsNaN(ScalarScore("FlowCapToday", 1.23)) {
		t.Fatal("无带必须 NaN 弃权")
	}
	// 倒置带要显形（NaN），不能悄悄把全条腿压成常数 0/1
	scalarBands["_test_inverted"] = ScalarBand{Min: 5, Max: 1, Linear: true}
	defer delete(scalarBands, "_test_inverted")
	if !math.IsNaN(ScalarScore("_test_inverted", 3)) {
		t.Fatal("Min>=Max 的错配带必须返回 NaN 显形")
	}
	// 不认识的 ID 也不是标量腿（绝不能落进 percentile）
	if IsScalarFactor("Mom20") || IsScalarFactor("nosuch") {
		t.Fatal("价量因子/未知 ID 不得被判成标量腿")
	}
}

// TestScalarBandsAlignWithCarrier 跨侧对齐：Go ⊆ 载体 params.scalar_bands 且逐键逐值相等。
//
// 这是"两侧各写一份迟早漂"的结构防线（§跨侧契约锁形）。载体那份 json 在仓库内，
// 直接读它比对；文件不在（外部裁剪构建）时明确 skip，不许把"没比"混成"比过了"。
func TestScalarBandsAlignWithCarrier(t *testing.T) {
	path := filepath.Join("..", "..", "qmt_gateway", "local_executor_params.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("载体参数文件不在位（%v），本用例的跨侧比对未执行——不是通过", err)
	}
	var doc struct {
		Params struct {
			ScalarBands map[string]struct {
				Min  float64 `json:"min"`
				Max  float64 `json:"max"`
				Mode string  `json:"mode"`
			} `json:"scalar_bands"`
			FactorDefs map[string]struct {
				Kind  string `json:"kind"`
				Input string `json:"input"`
			} `json:"factor_defs"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析载体参数文件失败: %v", err)
	}
	carrier := doc.Params.ScalarBands
	if len(carrier) == 0 {
		t.Fatal("载体 scalar_bands 为空——读错了层，比对无意义")
	}
	// 关系是 **Go ⊆ 载体**，不是等集：载体另有 lu_board/stom_21/cmra/flow_resid/NewsEvent 等
	// Go 侧不注册的标量（它们在盒内现算或走烘参）。把"等集"写成断言会在下一次
	// 单侧新增因子时无谓变红；把"子集"写成断言才是本批真正要守的不变量。
	//
	// 两个代理是**例外**（下面单独负锁）：SectorFlowQuantile / SectorGainRank 只在 Go 侧存在，
	// 载体上没有同名项——它们的对齐要求是"载体里也绝不能出现这两个名字"。
	goOnly := map[string]bool{"SectorFlowQuantile": true, "SectorGainRank": true}
	shared := 0
	for id, b := range AllScalarBands() {
		if goOnly[id] {
			if _, bad := carrier[id]; bad {
				t.Fatalf("代理 %s 出现在载体 scalar_bands 里——代理不进载体（A2-4 负锁）", id)
			}
			continue
		}
		c, ok := carrier[id]
		if !ok {
			t.Fatalf("Go 侧带值 %s 在载体 scalar_bands 里没有同名项（两侧从此分家）", id)
		}
		shared++
		if c.Min != b.Min || c.Max != b.Max {
			t.Fatalf("%s 带值不符：Go=[%v,%v] 载体=[%v,%v]", id, b.Min, b.Max, c.Min, c.Max)
		}
		// linear 与否也必须同形：一侧门槛一侧线性＝同分不同义。
		if (c.Mode == "linear") != b.Linear {
			t.Fatalf("%s 归一态不符：Go linear=%v 载体 mode=%q", id, b.Linear, c.Mode)
		}
	}
	// 射程自证：确实在比对而不是空跑一圈（本仓"恒绿用例"的教科书形状）。
	if shared < 3 {
		t.Fatalf("同名标量至少应有 IndexChange/FlowToday/Flow5d 三条参与比对，实际 %d", shared)
	}
}

func TestProxiesNeverReuseCarrierNames(t *testing.T) {
	// ① Go 侧不得注册载体的真名（ConceptHeat=利好池 LLM 热度、IsLeader=布尔龙头）
	for _, id := range []string{"ConceptHeat", "IsLeader", "NewsEvent"} {
		if _, ok := Get(id); ok {
			t.Fatalf("Go 侧注册了载体真名 %s —— 代理必须另起 ID，否则回测结论会被当成实盘行为", id)
		}
	}
	// ② 两个代理 ID 必须在册且**只有** Go 侧有（载体侧的负锁在门禁 §259 ⑤）
	for _, id := range []string{"SectorFlowQuantile", "SectorGainRank"} {
		d, ok := Get(id)
		if !ok {
			t.Fatalf("代理因子 %s 未注册", id)
		}
		if d.Cat != CatMarket {
			t.Fatalf("%s 必须归 CatMarket，得 %v", id, d.Cat)
		}
	}
	// ③ 代理不得偷走真名的带值语义：SectorGainRank 必须 linear（连续分位），
	//    而载体的 IsLeader 是门槛态——两者若同名同带，布尔 1.0 会算出 0 分（0<=1<1）。
	b, ok := ScalarBandOf("SectorGainRank")
	if !ok || !b.Linear {
		t.Fatalf("龙头代理分位必须走 linear 归一，得 %+v ok=%v", b, ok)
	}
}

func TestMarketFactorColumnsRoundTrip(t *testing.T) {
	// Def.Compute 只负责"把列交出去"：等长、缺失 nil、空序列 nil。
	d, ok := Get("FlowToday")
	if !ok {
		t.Fatal("FlowToday 未注册")
	}
	if got := d.Compute(nil); got != nil {
		t.Fatalf("nil 序列必须返回 nil（无输入＝弃权），得 %v", got)
	}
	if got := d.Compute(&StockSeries{}); got != nil {
		t.Fatalf("空日期序列必须返回 nil，得 %v", got)
	}
	s := &StockSeries{Dates: []string{"a", "b", "c"}}
	s.FlowToday = []float64{1, math.NaN(), 3}
	got := d.Compute(s)
	if len(got) != 3 || got[0] != 1 || !math.IsNaN(got[1]) || got[2] != 3 {
		t.Fatalf("列必须原样交出（等长、NaN 保留），得 %v", got)
	}
}
