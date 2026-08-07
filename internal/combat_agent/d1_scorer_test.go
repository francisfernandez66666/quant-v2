// Package combat_agent D1 评分器单测：覆盖本轮"LLM 慢响应处理"改动的回退语义——
//   - TestFillFallback：D1 失败回退上一轮评分（有值复用 / 无值归 0）
//   - TestBatchScoreNilLLM：LLM 未配置时全量归 0，不受 fallback 影响
//   - TestCleanJSONInteriorBOM：LLM 输出数组内部夹 BOM 时仍可被解析（曾整批亏损）
package combat_agent

import (
	"encoding/json"
	"testing"
)

// TestCleanJSONInteriorBOM 覆盖 b 修复：LLM 返回 JSON 数组内部夹 UTF-8 BOM（0xEF 0xBB 0xBF）
// 时，过去 cleanJSON 只剥首尾导致 json.Unmarshal 整批失败、全部归 0；现在应全局剔除可正常解析。
func TestCleanJSONInteriorBOM(t *testing.T) {
	// 模拟 LLM 输出：数组第二个对象内 "reason" 值前、以及元素分隔处混入 BOM
	raw := "\ufeff```json\n[\n  {\"code\":\"600519\",\"score\":0.7,\"blocked\":false,\"reason\":\"板块利好\"},\n" +
		"  {\"code\":" + "\ufeff" + "\"000001\",\"score\":\ufeff0.5,\"blocked\"\ufeff:true,\"reason\":\ufeff\"利空\"}\n]```\ufeff"

	got := cleanJSON(raw)
	var arr []D1Score
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("含内部BOM应可解析, cleanJSON=%q, err=%v", got, err)
	}
	if len(arr) != 2 {
		t.Fatalf("应解析出2只个股, got %d: %+v", len(arr), arr)
	}
	if arr[0].Code != "600519" || arr[0].Score != 0.7 || arr[0].Blocked {
		t.Fatalf("600519 解析异常: %+v", arr[0])
	}
	if arr[1].Code != "000001" || arr[1].Score != 0.5 || !arr[1].Blocked {
		t.Fatalf("000001 解析异常: %+v", arr[1])
	}
}

// TestFillFallback 验证 D1 缺失评分回退语义：
// fallback 有值则复用上一轮评分，无值则按 reason 归 0。
func TestFillFallback(t *testing.T) {
	ds := &D1Scorer{}
	fallback := map[string]D1Score{
		"600519": {Code: "600519", Score: 0.7, Blocked: false, Reason: "上一轮评分"},
	}
	result := map[string]D1Score{}

	// 有上一轮值：应回退复用，而非归 0
	ds.fillFallback(result, []string{"600519"}, fallback, "LLM失败")
	if got := result["600519"]; got.Score != 0.7 || got.Blocked || got.Reason != "上一轮评分" {
		t.Fatalf("回退失败: got %+v, want score=0.7 上一轮评分", got)
	}

	// 无上一轮值：按 reason 归 0
	ds.fillFallback(result, []string{"000001"}, fallback, "LLM失败")
	if got := result["000001"]; got.Score != 0 || got.Blocked || got.Reason != "LLM失败" {
		t.Fatalf("无回退归0失败: got %+v, want 0/LLM失败", got)
	}
}

// TestBatchScoreNilLLM 验证 LLM 未配置时全量归 0，不受 fallback 影响（无上一轮概念）。
func TestBatchScoreNilLLM(t *testing.T) {
	ds := NewD1Scorer(nil, "")
	fallback := map[string]D1Score{
		"600519": {Code: "600519", Score: 0.7, Blocked: false, Reason: "上一轮评分"},
	}
	got := ds.BatchScore([]string{"600519", "000001"}, nil, nil, fallback)
	if got["600519"].Score != 0 || got["000001"].Score != 0 {
		t.Fatalf("LLM未配置应全量0分, got %+v", got)
	}
	if len(got) != 2 {
		t.Fatalf("结果应含2只个股, got %d", len(got))
	}
}
