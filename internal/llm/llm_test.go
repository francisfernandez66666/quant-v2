package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAnalyzeHotTopicBatchDropsOnFailure LLM 批量失败（重试耗尽）时返回 nil + error，
// 不再用关键词兜底产出结果，由调用方丢弃该批。
func TestAnalyzeHotTopicBatchDropsOnFailure(t *testing.T) {
	c := New(Config{APIKey: ""}) // 无 key → Chat 必然失败
	titles := []string{"某公司涨停", "海外指数小幅波动", "某公司业绩暴跌"}

	results, err := c.AnalyzeHotTopicBatch(titles)
	if err == nil {
		t.Fatalf("期望返回错误（LLM 未配置），实际无错误")
	}
	if results != nil {
		t.Fatalf("期望失败时返回 nil（丢弃该批），实际返回 %d 条", len(results))
	}
}

// TestFallbackAnalysisDefaultScore B：无关键词命中时 fallback 默认 Score=0（消除 +0.5 中性污染）。
func TestFallbackAnalysisDefaultScore(t *testing.T) {
	ht := fallbackAnalysis("某上市公司例行披露董事会决议")
	if ht.Score != 0 {
		t.Fatalf("期望默认 Score=0，实际 %v", ht.Score)
	}
	if ht.Direction != "中性" {
		t.Fatalf("期望方向中性，实际 %s", ht.Direction)
	}
}

// TestFallbackAnalysisKeywordScore fallback 关键词命中时给出正确档位。
func TestFallbackAnalysisKeywordScore(t *testing.T) {
	ht := fallbackAnalysis("某芯片公司获重大订单 股价大涨")
	if ht.Score <= 0 {
		t.Fatalf("期望正向分，实际 %v", ht.Score)
	}
	if !strings.Contains(strings.Join(ht.Sectors, ","), "半导体") {
		t.Fatalf("期望归因半导体板块，实际 %v", ht.Sectors)
	}
}

// TestCleanJSONInvalidEscape 非法转义（如 9B 推理模型输出 \( ）应被剥掉反斜杠，
// 保留原字符，使响应可被 json.Unmarshal 正常解析。
func TestCleanJSONInvalidEscape(t *testing.T) {
	raw := "```json\n[{\"index\":1,\"reason\":\"磷化铟上游(云南锗业/有研新材)受益\\，海外自产确认价值\",\"sectors\":[\"半导体材料\"]}]\n```"
	got := cleanJSON(raw)
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("cleanJSON 后仍无法解析: %v\nraw=%q\ncleaned=%q", err, raw, got)
	}
	reason, _ := arr[0]["reason"].(string)
	if !strings.Contains(reason, "(云南锗业/有研新材)") {
		t.Fatalf("非法转义处理错误, reason=%q", reason)
	}
}

// TestCleanJSONValidEscape 合法转义（\n \uXXXX \" \\）必须原样保留，不得误伤。
func TestCleanJSONValidEscape(t *testing.T) {
	raw := `[{"index":1,"title":"A \"quoted\" \u5f53\u524d","reason":"line1\nline2","path":"a\\b"}]`
	got := cleanJSON(raw)
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("合法转义被误伤: %v\ncleaned=%q", err, got)
	}
	title, _ := arr[0]["title"].(string)
	if title != "A \"quoted\" 当前" {
		t.Fatalf("标题转义解析错误: %q", title)
	}
	reason, _ := arr[0]["reason"].(string)
	if !strings.Contains(reason, "line1\nline2") {
		t.Fatalf("\\n 转义解析错误: %q", reason)
	}
	path, _ := arr[0]["path"].(string)
	if path != `a\b` {
		t.Fatalf("\\\\ 转义解析错误: %q", path)
	}
}

// TestFlexibleFloatStringScore 9B 模型把 score 输出为字符串（"+0.75"）时也能解析。
func TestFlexibleFloatStringScore(t *testing.T) {
	var s struct {
		Score flexibleFloat `json:"score"`
	}
	for _, raw := range []string{`{"score":"+0.75"}`, `{"score":-0.5}`, `{"score":0.25}`, `{"score":" 1 "}`} {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("解析失败 %s: %v", raw, err)
		}
		if s.Score == 0 {
			t.Fatalf("score 不应为 0: %s", raw)
		}
	}
	// 非法值容错为 0，不崩
	var zero struct {
		Score flexibleFloat `json:"score"`
	}
	if err := json.Unmarshal([]byte(`{"score":"abc"}`), &zero); err != nil {
		t.Fatalf("非法 score 应容错不报错: %v", err)
	}
	if zero.Score != 0 {
		t.Fatalf("非法 score 应为 0, 实际 %v", zero.Score)
	}
}

// TestCleanJSONBarePlusNumber 裸 '+' 前缀数值（"score": +0.75）为非法 JSON，应剥掉 '+' 可解析。
func TestCleanJSONBarePlusNumber(t *testing.T) {
	raw := `[{"index":1,"score": +0.75,"sectors":["半导体材料"]},{"index":2,"score":-0.5}]`
	got := cleanJSON(raw)
	var arr []struct {
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("裸 + 号数值清理后仍无法解析: %v\ncleaned=%q", err, got)
	}
	if len(arr) != 2 || arr[0].Score != 0.75 || arr[1].Score != -0.5 {
		t.Fatalf("数值解析错误: %+v", arr)
	}
	// 字符串内的 '+' 不能被误删
	rawStr := `[{"reason":"A + B 传导"}]`
	gotStr := cleanJSON(rawStr)
	if !strings.Contains(gotStr, "A + B") {
		t.Fatalf("字符串内 + 被误删: %q", gotStr)
	}
}

// TestChatMessagesRequiresAPIKey 多轮咨询（股票咨询页）在 LLM 未配置时应直接报错，
// 避免用空 Key 发起无意义请求。
func TestChatMessagesRequiresAPIKey(t *testing.T) {
	c := New(Config{APIKey: ""})
	_, err := c.ChatMessages([]Message{{Role: "user", Content: "分析一下某股票"}})
	if err == nil {
		t.Fatalf("期望无 Key 时报错，实际无错误")
	}
	if !strings.Contains(err.Error(), "LLM_API_KEY") {
		t.Fatalf("期望错误提示缺少 API Key，实际 %v", err)
	}
}
