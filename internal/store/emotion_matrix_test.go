// emotion_matrix_test.go — 情绪×战法矩阵聚合的单元回归（§情绪面板 B 档）。
package store

import (
	"encoding/json"
	"testing"
)

// insertEventResult 注入一条候选事件断点缓存（测试夹具，跳过 Upsert 的规则指纹路径）。
func insertEventResult(t *testing.T, d *DB, candID int64, date, industry string, excess map[string]float64, hit map[string]float64, limitUp int) {
	t.Helper()
	payload := map[string]any{"date": date, "industry": industry, "limit_up_count": limitUp}
	if excess != nil {
		payload["mean_excess"] = excess
	}
	if hit != nil {
		payload["hit_rate"] = hit
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertBacktestEventResult(candID, date, industry, "fp-test", testAdjBasis, string(b)); err != nil {
		t.Fatal(err)
	}
}

// 情绪×策略矩阵的分桶聚合统计。
func TestEmotionStrategyMatrixBuckets(t *testing.T) {
	d := testDB(t)
	// 候选 1：高潮 2 事件 + 启动 1 事件；候选 2：全部退潮 3 事件
	insertEventResult(t, d, 1, "20260105", "半导体", map[string]float64{"5": 2.0}, map[string]float64{"5": 60}, 120)
	insertEventResult(t, d, 1, "20260106", "半导体", map[string]float64{"5": 4.0}, map[string]float64{"5": 80}, 130)
	insertEventResult(t, d, 1, "20260107", "券商", map[string]float64{"5": 1.0}, map[string]float64{"5": 50}, 40)
	insertEventResult(t, d, 2, "20260105", "白酒", map[string]float64{"1": -1.0, "5": -2.0}, map[string]float64{"1": 30, "5": 20}, 10)
	insertEventResult(t, d, 2, "20260106", "白酒", map[string]float64{"1": -1.0, "5": -2.0}, map[string]float64{"1": 30, "5": 20}, 10)
	insertEventResult(t, d, 2, "20260107", "白酒", map[string]float64{"1": -1.0, "5": -2.0}, map[string]float64{"1": 30, "5": 20}, 10)

	phaseByDate := map[string]string{
		"20260105": "高潮", "2026-01-05": "高潮",
		"20260106": "高潮", "2026-01-06": "高潮",
		"20260107": "启动", "2026-01-07": "启动",
	}
	rows, err := d.ListEmotionStrategyMatrix(phaseByDate, nil, testAdjBasis)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("期望 2 行候选，得 %d", len(rows))
	}
	// 事件数降序：两候选各 3 事件 → 平局按 ID 升序，候选 1 在前
	if rows[0].CandidateID != 1 || rows[1].CandidateID != 2 {
		t.Fatalf("排序错误: %d %d", rows[0].CandidateID, rows[1].CandidateID)
	}
	// 候选 1 高潮桶：(2.0+4.0)/2=3.0 超额，命中 (60+80)/2=70，涨停 (120+130)/2=125
	c1 := rows[0]
	if c1.TotalEvents != 3 {
		t.Fatalf("候选1 总事件=%d", c1.TotalEvents)
	}
	if len(c1.Horizons) != 1 || c1.Horizons[0] != 5 {
		t.Fatalf("候选1 horizons=%v", c1.Horizons)
	}
	var hot, start *EmotionCell
	for i := range c1.Cells {
		switch c1.Cells[i].Phase {
		case "高潮":
			hot = &c1.Cells[i]
		case "启动":
			start = &c1.Cells[i]
		}
	}
	if hot == nil || start == nil {
		t.Fatalf("候选1 相位桶缺失: %+v", c1.Cells)
	}
	if hot.Events != 2 || hot.AvgExcess[5] != 3.0 || hot.HitRate[5] != 70 || hot.AvgLimitUp != 125 {
		t.Errorf("高潮桶统计错误: %+v", *hot)
	}
	if start.Events != 1 || !start.Thin {
		t.Errorf("启动桶应 1 事件且标 thin: %+v", *start)
	}
}

// TestEmotionMatrixFallbackPhase 无 phaseByDate 命中时走 fallbackPhase 回调；
// 两者都拿不到相位的事件被跳过（宁缺毋滥）。
func TestEmotionMatrixFallbackPhase(t *testing.T) {
	d := testDB(t)
	insertEventResult(t, d, 1, "20260202", "半导体", map[string]float64{"5": 1}, nil, 50)
	insertEventResult(t, d, 1, "20260203", "半导体", map[string]float64{"5": 1}, nil, 50) // 无法定性 → 跳过
	rows, err := d.ListEmotionStrategyMatrix(map[string]string{}, func(dateStr8 string) string {
		if dateStr8 == "20260202" {
			return "发酵"
		}
		return ""
	}, testAdjBasis)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TotalEvents != 1 {
		t.Fatalf("fallback 聚合错误: %+v", rows)
	}
	if len(rows[0].Cells) != 1 || rows[0].Cells[0].Phase != "发酵" {
		t.Fatalf("相位桶错误: %+v", rows[0].Cells)
	}
}

// TestEmotionMatrixBadJSONSkipped 脏 result_json（非 JSON/形状不符）跳过不炸整矩阵。
func TestEmotionMatrixBadJSONSkipped(t *testing.T) {
	d := testDB(t)
	if err := d.UpsertBacktestEventResult(1, "20260301", "半导体", "fp", testAdjBasis, "not-json{{{"); err != nil {
		t.Fatal(err)
	}
	insertEventResult(t, d, 1, "20260302", "半导体", map[string]float64{"5": 2}, nil, 80)
	rows, err := d.ListEmotionStrategyMatrix(map[string]string{"20260302": "退潮"}, nil, testAdjBasis)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TotalEvents != 1 {
		t.Fatalf("脏行应被跳过: %+v", rows)
	}
}
