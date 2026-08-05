// Package newsagent 政策反制事件推导。
// 从涉外政策新闻标题中识别"反制/出口管制/稀土限制/关税反制"等关键词，
// 直构 data.ConfrontationEvent（不走 LLM，保证涉外政策新闻稳定识别）。
package newsagent

import (
	"strings"
	"time"

	"quant-trading-v2/internal/data"
)

// retaliationRule 政策反制关键词匹配规则。
// 命中的标题将推导为对应板块与方向的 ConfrontationEvent。
type retaliationRule struct {
	keywords []string // 触发关键词（任一命中即可）
	sectors  []string // 受影响板块
	direction string  // 方向：利好/利空
	impact   string   // 影响级别：高/中/低
}

// retaliationRules 政策反制识别规则表。
// 覆盖 A 股涉外反制主要情景：加征关税、出口管制、稀土/资源限制、反倾销等。
var retaliationRules = []retaliationRule{
	{
		keywords:  []string{"反制", "对等关税", "关税反制", "精准反制"},
		sectors:   []string{"稀土永磁", "农业", "军工"},
		direction: "利空",
		impact:    "高",
	},
	{
		keywords:  []string{"出口管制", "限制出口", "稀土出口", "资源管制"},
		sectors:   []string{"稀土永磁", "有色金属"},
		direction: "利好",
		impact:    "高",
	},
	{
		keywords:  []string{"加征关税", "提高关税", "关税壁垒"},
		sectors:   []string{"外贸", "航运港口"},
		direction: "利空",
		impact:    "中",
	},
	{
		keywords:  []string{"反倾销", "反补贴", "贸易摩擦", "贸易战"},
		sectors:   []string{"外贸", "光伏", "汽车"},
		direction: "利空",
		impact:    "中",
	},
}

// DeriveRetaliation 从新闻标题推导涉外政策反制事件。
// 标题命中任一关键词即生成对应事件；返回全部推导结果（无命中返回 nil）。
func (a *Agent) DeriveRetaliation(items []data.NewsItem) []data.ConfrontationEvent {
	var out []data.ConfrontationEvent
	for _, item := range items {
		if item.Title == "" {
			continue
		}
		rule := matchRetaliation(item.Title)
		if rule == nil {
			continue
		}
		dt := item.Datetime
		if dt == "" {
			dt = time.Now().Format("2006-01-02 15:04:05")
		}
		out = append(out, data.ConfrontationEvent{
			Title:     item.Title,
			Content:   item.Content,
			Datetime:  dt,
			Sectors:   rule.sectors,
			Direction: rule.direction,
			Impact:    rule.impact,
			Source:    "政策反制",
		})
	}
	if len(out) > 0 {
		return out
	}
	return nil
}

// matchRetaliation 从标题中匹配首条命中的政策反制规则；无命中返回 nil。
func matchRetaliation(title string) *retaliationRule {
	for i := range retaliationRules {
		r := &retaliationRules[i]
		for _, kw := range r.keywords {
			if strings.Contains(title, kw) {
				return r
			}
		}
	}
	return nil
}