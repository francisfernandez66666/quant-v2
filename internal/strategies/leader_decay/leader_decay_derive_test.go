package leader_decay

// leader_decay_derive_test.go —— 导出阈值与战法实际用的常量同源，且每档在生产判定里
// 按导出的数生效。
//
// 额外锁一条这个战法特有的形状：**门槛是四战法里最严的那一枚**。
// 它以前只是注释里的一句"默认 65"，现在必须是可判定的读数——否则有人把它调回 60，
// 断板反包的风险就从"最严一档"变成"和另外三条一样松"，而没有任何东西会响。

import (
	"math"
	"strings"
	"testing"

	"quant-trading-v2/internal/strategies/shortbase"
)

func ldFloat(t *testing.T, table map[string]interface{}, key string) float64 {
	t.Helper()
	raw, ok := table[key]
	if !ok {
		t.Fatalf("导出表缺键 %s", key)
	}
	switch v := raw.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		t.Fatalf("导出键 %s 类型不是数：%T", key, raw)
	}
	return 0
}

// TestDeriveThresholdTable 阈值表逐键锁死：导出值必须与战法源码生产在用的常量一字不差
// （判据取自导出表、期望值另取自常量定义——两边漂任何一边这里就红，不给"派生腿悄悄换数"留缝）。
func TestDeriveThresholdTable(t *testing.T) {
	table := DeriveCarrierThresholds()
	want := map[string]float64{
		"ld_min_bars":           10,
		"ld_min_boards":         2,
		"ld_score_threshold":    65,
		"ld_board_max_score":    30,
		"ld_board_full_boards":  5,
		"ld_board_base_score":   12,
		"ld_board_base_boards":  2,
		"ld_board_span_boards":  3,
		"ld_board_span_score":   18,
		"ld_big_loss_ratio":     0.97,
		"ld_big_loss_score":     20,
		"ld_touched_score":      15,
		"ld_huge_volume_ratio":  2.0,
		"ld_huge_volume_score":  10,
		"ld_weakness_max_score": 40,
		"ld_sector_drop_ratio":  0.5,
		"ld_sector_drop_score":  20,
		"ld_phase_score":        10,
		"ld_retreat_max_score":  30,
	}
	for key, expected := range want {
		if got := ldFloat(t, table, key); math.Abs(got-expected) > 1e-9 {
			t.Errorf("%s 导出=%v 期望=%v", key, got, expected)
		}
	}
	for key := range table {
		if _, ok := want[key]; ok {
			continue
		}
		if key != "ld_retreat_phase_keys" {
			t.Errorf("导出表有多余键 %s（载体不读它，等于无主的数进产物）", key)
		}
	}
	if len(table) != len(want)+1 {
		t.Errorf("导出键数=%d 期望=%d（另加 ld_retreat_phase_keys）", len(table), len(want)+1)
	}
	// 线性段自证：板数从起点走完跨度正好到"记满分"的那一档。
	if math.Abs(ldFloat(t, table, "ld_board_base_boards")+ldFloat(t, table, "ld_board_span_boards")-
		ldFloat(t, table, "ld_board_full_boards")) > 1e-9 {
		t.Error("连板族：起评板数 + 跨度应正好等于满分包的板数")
	}
	// 断板弱势三项齐备必须超帽，否则 ld_weakness_max_score 是枚没主人的数。
	if sum := ldFloat(t, table, "ld_big_loss_score") + ldFloat(t, table, "ld_touched_score") +
		ldFloat(t, table, "ld_huge_volume_score"); sum <= ldFloat(t, table, "ld_weakness_max_score") {
		t.Errorf("三项合计 %v 未超帽 %v，封顶判据成了摆设", sum, ldFloat(t, table, "ld_weakness_max_score"))
	}
	// 门槛是四战法最严的那一档（其余三条出厂 60）：这里只能自证"高于本包满分档的一半"没意义，
	// 所以直接钉住那枚数，并把"比 60 严"写进判据。
	if thr := ldFloat(t, table, "ld_score_threshold"); thr <= 60 {
		t.Errorf("断板门槛 %v 不应不严于其余三战法出厂的 60", thr)
	}
}

// TestRetreatMarkerIsTheExportedOne 相位词必须由导出的那一条驱动，而不是代码里再写一遍。
// 载体侧收的是**列表**（GBK 终端会把中文字面量静默换成 ?，所以词只能走 params）；
// Go 侧判的是 Contains，两边语义差在"列表 vs 单词"，不在"词本身"。
func TestRetreatMarkerIsTheExportedOne(t *testing.T) {
	table := DeriveCarrierThresholds()
	raw, ok := table["ld_retreat_phase_keys"]
	if !ok {
		t.Fatalf("导出表缺 ld_retreat_phase_keys")
	}
	keys, ok2 := raw.([]string)
	if !ok2 || len(keys) == 0 {
		t.Fatalf("ld_retreat_phase_keys 形状应是非空字符串列表，实为 %T", raw)
	}
	s := New(nil)
	phaseScore := ldFloat(t, table, "ld_phase_score")
	sectorScore := ldFloat(t, table, "ld_sector_drop_score")
	capScore := ldFloat(t, table, "ld_retreat_max_score")

	// 用导出的词拼一个"复合相位"（Go 判 Contains，所以"高潮转<退潮>"也该计分）。
	composite := "高潮转" + keys[0]
	if got := s.retreatScore(&shortbase.Data{EmotionPhase: composite}); math.Abs(got-phaseScore) > 1e-9 {
		t.Errorf("相位含导出的退潮词时应加 %v，实算 %v", phaseScore, got)
	}
	if got := s.retreatScore(&shortbase.Data{EmotionPhase: "启动"}); got != 0 {
		t.Errorf("相位不含退潮词时不应加分，实算 %v", got)
	}
	// 板块骤降到档（含等号）+ 相位退潮 = 20+10，正好等于帽：帽在此是收口而不是削峰。
	if got := s.retreatScore(&shortbase.Data{SectorLimitUpDropPct: ldFloat(t, table, "ld_sector_drop_ratio"),
		EmotionPhase: keys[0]}); math.Abs(got-(sectorScore+phaseScore)) > 1e-9 {
		t.Errorf("板块与相位同时退潮应为 %v，实算 %v", sectorScore+phaseScore, got)
	}
	if sectorScore+phaseScore > capScore+1e-9 {
		t.Errorf("两项相加 %v 超过帽 %v 时帽才起作用——当前形状请复核导出表", sectorScore+phaseScore, capScore)
	}
	// 词必须是真源那个常量（比较式里不再出现中文，改词只会红在这一行）。
	if keys[0] != ldPhaseRetreatMark || !strings.Contains(ldPhaseRetreatMark, keys[0]) {
		t.Errorf("导出的退潮词 %q 与常量 %q 不同源", keys[0], ldPhaseRetreatMark)
	}
}

// TestBoardAndWeaknessBandsFollowExportedValues 连板族与断板弱势族按导出的档位生效。
func TestBoardAndWeaknessBandsFollowExportedValues(t *testing.T) {
	table := DeriveCarrierThresholds()
	s := New(nil)
	base := ldFloat(t, table, "ld_board_base_score")
	baseBoards := ldFloat(t, table, "ld_board_base_boards")
	spanBoards := ldFloat(t, table, "ld_board_span_boards")
	spanScore := ldFloat(t, table, "ld_board_span_score")
	fullBoards := int(ldFloat(t, table, "ld_board_full_boards"))
	maxScore := ldFloat(t, table, "ld_board_max_score")

	if got := s.boardScore(&shortbase.Data{ConsecBoards: int(baseBoards)}); math.Abs(got-base) > 1e-9 {
		t.Errorf("起评板数应为 %v，实算 %v", base, got)
	}
	if got := s.boardScore(&shortbase.Data{ConsecBoards: fullBoards}); got != maxScore {
		t.Errorf("满分包 %d 板应记 %v，实算 %v", fullBoards, maxScore, got)
	}
	mid := fullBoards - 1
	want := base + (float64(mid)-baseBoards)/spanBoards*spanScore
	if got := s.boardScore(&shortbase.Data{ConsecBoards: mid}); math.Abs(got-want) > 1e-9 {
		t.Errorf("%d 板线性段应为 %v，实算 %v", mid, want, got)
	}

	ratio := ldFloat(t, table, "ld_big_loss_ratio")
	bigLoss := ldFloat(t, table, "ld_big_loss_score")
	touched := ldFloat(t, table, "ld_touched_score")
	hugeRatio := ldFloat(t, table, "ld_huge_volume_ratio")
	hugeScore := ldFloat(t, table, "ld_huge_volume_score")
	cap := ldFloat(t, table, "ld_weakness_max_score")

	prev := 100.0
	// 正好在"大面"这一档（含等号）计分；浅一分不计。
	if got := s.weaknessScore(&shortbase.Data{PrevClose: prev, Price: prev * ratio}); math.Abs(got-bigLoss) > 1e-9 {
		t.Errorf("收盘=昨收×%v 应记大面 %v，实算 %v", ratio, bigLoss, got)
	}
	if got := s.weaknessScore(&shortbase.Data{PrevClose: prev, Price: prev * (ratio + 0.001)}); got != 0 {
		t.Errorf("跌幅浅于 %v 不应记大面，实算 %v", ratio, got)
	}
	// 三项齐备（20+15+10=45）**必须超帽**，实算收到 40 —— 帽在这里是真的在削峰，不是摆设。
	if got := s.weaknessScore(&shortbase.Data{PrevClose: prev, Price: prev * ratio,
		TouchedBoardToday: true, TodayVolVs5d: hugeRatio}); math.Abs(got-cap) > 1e-9 {
		t.Errorf("三项之和超帽时应封在 %v，实算 %v", cap, got)
	}
	// 量比到档（含等号）才算天量。
	if got := s.weaknessScore(&shortbase.Data{PrevClose: 0, TodayVolVs5d: hugeRatio}); math.Abs(got-hugeScore) > 1e-9 {
		t.Errorf("量比=%v 应记天量 %v，实算 %v", hugeRatio, hugeScore, got)
	}
	if bigLoss+touched+hugeScore <= cap {
		t.Errorf("三项之和 %v 应超过帽 %v（否则这条用例判不到封顶那件事）",
			bigLoss+touched+hugeScore, cap)
	}
}
