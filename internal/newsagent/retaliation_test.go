package newsagent

import (
	"testing"

	"quant-trading-v2/internal/data"
)

// TestDeriveRetaliationMatches 标题命中涉外政策反制关键词时应推导出对应事件。
func TestDeriveRetaliationMatches(t *testing.T) {
	a := New(nil, nil, nil, "")
	items := []data.NewsItem{
		{Title: "中国宣布对美加征关税实施精准反制"},
		{Title: "商务部分布稀土出口管制清单"},
		{Title: "美国宣布新一轮关税措施，中方回应"},
	}
	events := a.DeriveRetaliation(items)
	if len(events) != 2 {
		t.Fatalf("期望识别 2 条政策反制事件（反制+出口管制），实际 %d", len(events))
	}
	// 反制事件：利空方向 + 高影响，来源固定为"政策反制"
	if events[0].Source != "政策反制" || events[0].Direction != "利空" || events[0].Impact != "高" {
		t.Fatalf("反制事件字段不符: %+v", events[0])
	}
	// 出口管制：稀土板块利好
	if events[1].Direction != "利好" || len(events[1].Sectors) == 0 || events[1].Sectors[0] != "稀土永磁" {
		t.Fatalf("出口管制事件字段不符: %+v", events[1])
	}
}

// TestDeriveRetaliationNoMatch 无涉外政策反制关键词的标题不应推导事件。
func TestDeriveRetaliationNoMatch(t *testing.T) {
	a := New(nil, nil, nil, "")
	items := []data.NewsItem{
		{Title: "某公司发布中报业绩预增公告"},
		{Title: "央行开展逆回购操作"},
	}
	events := a.DeriveRetaliation(items)
	if len(events) != 0 {
		t.Fatalf("期望无政策反制事件，实际 %d 条", len(events))
	}
}

// TestDeriveRetaliationEmptyTitle 空标题跳过，不应崩溃。
func TestDeriveRetaliationEmptyTitle(t *testing.T) {
	a := New(nil, nil, nil, "")
	events := a.DeriveRetaliation([]data.NewsItem{{Title: ""}, {Title: "   "}})
	if len(events) != 0 {
		t.Fatalf("期望空标题被跳过，实际 %d 条", len(events))
	}
}
