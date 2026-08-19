package data

import (
	"testing"
)

// TestFindSectorsByNamesFuzzy 验证 LLM 噪声板块名能通过包含匹配落到真实板块。
// 场景：事件归因的板块名可能带 LLM 噪声（如"通信服务·观点里线光缆"），
// 精确匹配失败后应回退到包含/子串匹配，命中真实板块并带回真实 Code。
// English: TestFindSectorsByNamesFuzzy verifies that LLM-noisy sector names can land on real sectors via containment matching.
// English: Scenario: event-attribution sector names may carry LLM noise (e.g. "通信服务·观点里线光缆"); when exact matching fails, fall back to containment/substring matching to hit the real sector and bring back its real Code.
func TestFindSectorsByNamesFuzzy(t *testing.T) {
	ss := &SectorScanner{
		cachedSector: []SectorInfo{
			{Code: "881126", Name: "通信服务"},
			{Code: "308000", Name: "数据中心"},
			{Code: "881121", Name: "半导体及元件"},
			{Code: "885407", Name: "光学光电子"},
		},
	}

	cases := []struct {
		query    string
		wantName string
		wantCode string
	}{
		// 精确匹配保持原行为
		// English: exact match preserves the original behavior
		{"半导体", "半导体及元件", "881121"},
		// LLM 噪声板块名 → 包含匹配落到真实板块
		// English: LLM-noisy sector name → containment match lands on the real sector
		{"通信服务·的观点里线光缆", "通信服务", "881126"},
		{"数据中心设备/5G建设", "数据中心", "308000"},
		// 真实板块名包含 LLM 噪声名（如"半导体"是"半导体及元件"子串）→ 落到更长的真实板块
		// English: real sector name contains the LLM-noisy name (e.g. "半导体" is a substring of "半导体及元件") → lands on the longer real sector
		{"半导体", "半导体及元件", "881121"},
	}
	for _, c := range cases {
		got := ss.FindSectorsByNames([]string{c.query})
		if len(got) == 0 {
			t.Errorf("%q 未命中任何板块", c.query)
			continue
		}
		if got[0].Name != c.wantName {
			t.Errorf("%q → 板块名 %q, 期望 %q", c.query, got[0].Name, c.wantName)
		}
		if got[0].Code != c.wantCode {
			t.Errorf("%q → 板块代码 %q, 期望 %q", c.query, got[0].Code, c.wantCode)
		}
	}
}
