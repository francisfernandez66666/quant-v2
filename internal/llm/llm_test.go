package llm

import (
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
