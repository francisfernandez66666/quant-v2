package newsagent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

const stage1SystemPrompt = `你是一个A股新闻价值判断专家。从以下新闻标题中，筛选出具有投资参考价值的重大事件。

重大事件包括但不限于：
- 业绩预告/财报发布
- 重大合同/中标/订单
- 重组/定增/增发/回购/减持
- 新药获批/临床试验突破
- 重大政策发布/行业利好利空
- 龙头公司重大动向
- 宏观经济数据发布

忽略：娱乐、社会、体育、影视、名人八卦、灾难事故等无关新闻。

返回JSON数组，只包含有投资价值的条目索引（从1开始），如 [1,3,7]
如果没有任何有价值的条目，返回 []`

func (a *Agent) classifyMaterial(titles []string) ([]int, error) {
	if len(titles) == 0 {
		return nil, nil
	}
	if a.llmClient == nil {
		log.Printf("[newsagent] LLM未配置, 跳过Stage1初筛, 全部视为有价值")
		all := make([]int, len(titles))
		for i := range titles {
			all[i] = i
		}
		return all, nil
	}

	var sb strings.Builder
	for i, t := range titles {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t))
	}
	prompt := sb.String()

	resp, err := a.llmClient.Chat(stage1SystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("stage1 chat: %w", err)
	}
	resp = cleanJSON(resp)

	var indices []int
	if err := json.Unmarshal([]byte(resp), &indices); err != nil {
		log.Printf("[newsagent] Stage1 JSON解析失败, 全部视为有价值: %v", err)
		all := make([]int, len(titles))
		for i := range titles {
			all[i] = i
		}
		return all, nil
	}

	// 转为0-based
	for i := range indices {
		indices[i]--
	}

	// 安全过滤
	var valid []int
	for _, idx := range indices {
		if idx >= 0 && idx < len(titles) {
			valid = append(valid, idx)
		}
	}

	log.Printf("[newsagent] Stage1初筛: %d/%d 条有价值", len(valid), len(titles))
	return valid, nil
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return s
}
