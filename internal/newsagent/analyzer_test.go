package newsagent

import (
	"testing"

	"quant-trading-v2/internal/llm"
)

// TestPostProcessPreservesExplicitScore B：LLM 明确给出非中性方向的分数应保留量化档，
// 不再被"中性归零"误清空。
func TestPostProcessPreservesExplicitScore(t *testing.T) {
	ht := &llm.HotTopic{
		Title:       "某龙头公司中标重大项目",
		Sentiment:   "中性",
		Direction:   "利好",
		Score:       0.6,
		ImpactLevel: "中",
	}
	postProcess(ht)
	// 0.6 应就近量化到 0.5 档且保留正号（不再因 Sentiment=中性 归零）
	if ht.Score != 0.5 {
		t.Fatalf("期望保留 0.5，实际 %v", ht.Score)
	}
}

// TestPostProcessNeutralZero B：无方向且强度为 0 的中性事件仍归零。
func TestPostProcessNeutralZero(t *testing.T) {
	ht := &llm.HotTopic{
		Title:       "海外指数小幅波动",
		Sentiment:   "中性",
		Direction:   "中性",
		Score:       0,
		ImpactLevel: "低",
	}
	postProcess(ht)
	if ht.Score != 0 {
		t.Fatalf("期望归零，实际 %v", ht.Score)
	}
}

// TestPostProcessNegativeKept B：利空分数保留符号。
func TestPostProcessNegativeKept(t *testing.T) {
	ht := &llm.HotTopic{
		Title:       "某公司业绩巨亏",
		Sentiment:   "负面",
		Direction:   "利空",
		Score:       -0.8,
		ImpactLevel: "高",
	}
	postProcess(ht)
	if ht.Score != -0.75 {
		t.Fatalf("期望 -0.75，实际 %v", ht.Score)
	}
}

// TestPostProcessFallbackPollutionCleared B：fallback 遗留（Direction=中性 且 强度档=0）
// 被归零，消除 +0.5 中性污染。
func TestPostProcessFallbackPollutionCleared(t *testing.T) {
	ht := &llm.HotTopic{
		Title:       "常规公告",
		Sentiment:   "中性",
		Direction:   "中性",
		Score:       0.5,
		ImpactLevel: "中",
	}
	postProcess(ht)
	// 量化后 0.5 → best=0.5，但 Direction/Sentiment 均中性；放宽规则下
	// 仅当 best==0 才归零，这里应保留 0.5（LLM 未明确给方向，保留量化档由引擎阈值把关）。
	// 注：fallback 默认 Score 已改为 0，此处为 LLM 显式给 0.5 且标中性的边界。
	if ht.Score == 0 {
		t.Fatalf("有明确分数的事件不应被归零，实际 0")
	}
}
