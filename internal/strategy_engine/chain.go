package strategy_engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"quant-trading-v2/internal/newsagent"
)

const chainSystemPrompt = `你是一个A股产业链分析师。对于以下利好事件及其关联板块，分析其产业链上下游传导逻辑。

返回JSON数组，每项包含：
- sector: 受益板块名称
- reason: 传导逻辑（20字内）
- lead_stocks: 该板块的龙头股名称列表（1-3只）

如：[{"sector":"封测","reason":"存储扩产拉动封测需求","lead_stocks":["长电科技","通富微电"]}]
如果无上下游传导，返回 []`

func (e *Engine) chainInference(ctx context.Context, bullEvents []newsagent.NewsEvent) []SectorHot {
	if e.llmClient == nil || len(bullEvents) == 0 {
		return nil
	}

	var sb strings.Builder
	for _, ev := range bullEvents {
		sb.WriteString(fmt.Sprintf("- 事件: %s (板块: %s)\n", ev.Title, strings.Join(ev.Sectors, ", ")))
	}
	prompt := sb.String()

	resp, err := e.llmClient.Chat(chainSystemPrompt, prompt)
	if err != nil {
		log.Printf("[strategy_engine] 产业链LLM推断失败: %v", err)
		return nil
	}
	resp = cleanJSON(resp)

	var raw []struct {
		Sector    string   `json:"sector"`
		Reason    string   `json:"reason"`
		LeadStocks []string `json:"lead_stocks"`
	}
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		log.Printf("[strategy_engine] 产业链JSON解析失败: %v", err)
		return nil
	}

	result := make([]SectorHot, 0, len(raw))
	for _, r := range raw {
		if r.Sector == "" {
			continue
		}
		result = append(result, SectorHot{
			Name:       r.Sector,
			Direction:  "利好",
			Score:      0.7,
			Reason:     r.Reason,
			LeadStocks: r.LeadStocks,
		})
	}
	log.Printf("[strategy_engine] 产业链推断: %d 个上下游板块", len(result))
	return result
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return s
}
