package newsagent

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
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

var stage1Keywords = []string{
	"业绩", "财报", "预增", "预亏", "扭亏", "翻倍", "涨停", "跌停",
	"重大合同", "中标", "订单", "重组", "定增", "增发", "回购", "减持", "增持",
	"获批", "临床", "突破", "新品", "专利",
	"政策", "利好", "利空", "救市", "降息", "降准", "加息",
	"龙头", "板块", "产业链", "景气", "拐点",
	"退市", "ST", "风险警示", "立案", "调查", "处罚",
	"北向", "主力", "资金", "净流入", "净流出",
	"分红", "送转", "除权", "填权",
	"借壳", "收购", "合并", "分拆", "引入战投",
	"出口", "进口", "关税", "制裁",
	"AI", "人工智能", "芯片", "新能源", "光伏", "锂电", "储能", "氢能",
	"消费", "复苏", "通胀", "通缩",
}

// matchKeywords 关键词匹配：检查标题是否包含预定义的投资相关关键词。
func matchKeywords(title string) bool {
	t := strings.ToLower(title)
	for _, kw := range stage1Keywords {
		if strings.Contains(t, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// classifyMaterial Stage1 初筛：优先使用 LLM 判断新闻价值，无 LLM 时回退关键词过滤。
// 返回有价值的新闻标题索引列表。若 LLM 调用失败，全部视为有价值。
func (a *Agent) classifyMaterial(titles []string) ([]int, error) {
	if len(titles) == 0 {
		return nil, nil
	}
	if a.llmClient == nil {
		log.Printf("[newsagent] LLM未配置, 使用关键词过滤Stage1")
		var matched []int
		for i, t := range titles {
			if matchKeywords(t) {
				matched = append(matched, i)
			}
		}
		if len(matched) == 0 && len(titles) > 0 {
			// 兜底: 取前20条
			n := 20
			if len(titles) < n {
				n = len(titles)
			}
			for i := 0; i < n; i++ {
				matched = append(matched, i)
			}
			log.Printf("[newsagent] Stage1关键词无匹配, 取前%d条", n)
		}
		log.Printf("[newsagent] Stage1关键词过滤: %d/%d 条", len(matched), len(titles))
		return matched, nil
	}

	var sb strings.Builder
	for i, t := range titles {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t))
	}
	prompt := sb.String()

	var resp string
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err = a.llmClient.Chat(stage1SystemPrompt, prompt)
		if err == nil {
			break
		}
		if attempt < 3 {
			log.Printf("[newsagent] Stage1 LLM失败(第%d次), 重试: %v", attempt, err)
			time.Sleep(2 * time.Second)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("stage1 chat (3次失败): %w", err)
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

// cleanJSON 清理 LLM 返回的 JSON 字符串中的 markdown 代码块标记。
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return s
}
