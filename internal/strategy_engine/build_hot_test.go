// Package strategy_engine 策略引擎库：K 线链处理、热点构建、动态附加实时 bar、索引维护等策略计算基础设施。
package strategy_engine

import (
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
)

// TestBuildHotSectors 验证新热点立即归因入口：事件按 |Score| 符号分流利好/利空板块，
// 同一板块多事件合并，与 Evaluate 内 attribution 同一实现（幂等可重复调用）。
func TestBuildHotSectors(t *testing.T) {
	e := New(nil) // nil scanner 允许（enrichSectorData 对 nil 直接跳过）

	events := []newsagent.NewsEvent{
		{Level: "板块", Score: 0.8, Direction: "利好", Reason: "政策支持", Sectors: []string{"券商"}, Title: "券商板块政策利好"},
		{Level: "板块", Score: 0.6, Direction: "利好", Reason: "成交活跃", Sectors: []string{"券商"}, Title: "券商成交活跃"},
		{Level: "板块", Score: -0.7, Direction: "利空", Reason: "处罚落地", Sectors: []string{"医药"}, Title: "医药板块利空"},
		{Level: "个股", Score: 0.9, Direction: "利好", Reason: "个股独立事件", Sectors: []string{"券商"}, Title: "某公司大涨"}, // 个股级应被跳过
	}

	bull, bear := e.BuildHotSectors(events)

	if len(bull) != 1 {
		t.Fatalf("期望 1 个利好板块, 实际 %d", len(bull))
	}
	if bull[0].Name != "券商" {
		t.Fatalf("期望利好板块为券商, 实际 %q", bull[0].Name)
	}
	// 同板块事件合并：保留 |score| 最大属性
	if bull[0].Score != 0.8 || bull[0].Reason != "政策支持" {
		t.Fatalf("期望保留最高分事件属性, 实际 score=%v reason=%q", bull[0].Score, bull[0].Reason)
	}
	if len(bull[0].NewsTitles) != 2 {
		t.Fatalf("期望合并 2 条新闻标题, 实际 %v", bull[0].NewsTitles)
	}

	if len(bear) != 1 {
		t.Fatalf("期望 1 个利空板块, 实际 %d", len(bear))
	}
	if bear[0].Name != "医药" || bear[0].Score != -0.7 {
		t.Fatalf("利空板块归因异常: %+v", bear[0])
	}

	// 幂等：重复调用结果一致
	bull2, bear2 := e.BuildHotSectors(events)
	if len(bull2) != len(bull) || len(bear2) != len(bear) {
		t.Fatal("BuildHotSectors 应幂等，重复调用结果应一致")
	}
}

// TestBuildHotSectorsEmpty 无板块事件时返回空切片（不 panic）。
func TestBuildHotSectorsEmpty(t *testing.T) {
	e := New(nil)
	bull, bear := e.BuildHotSectors(nil)
	if bull != nil && len(bull) != 0 {
		t.Fatalf("空事件应无利好板块, 实际 %v", bull)
	}
	if bear != nil && len(bear) != 0 {
		t.Fatalf("空事件应无利空板块, 实际 %v", bear)
	}
}

// TestBuildHotSectorsWithScanner 带 scanner 时能补充板块行情信息（真实同花顺板块名）。
// 网络不可用时优雅降级（不 panic，字段可能为空）。
func TestBuildHotSectorsWithScanner(t *testing.T) {
	ths := data.NewTHSClient()
	boards, err := ths.GetBoardList()
	if err != nil {
		t.Fatalf("THS板块名单获取失败: %v", err)
	}
	if len(boards) == 0 {
		t.Skip("THS板块名单为空")
	}
	realName := boards[0].Name

	sc := data.NewSectorScanner(data.NewMarketAPI(), nil)
	sc.Update(boards, 0, 0, 0)

	e := New(nil)
	e.SetScanner(sc)

	events := []newsagent.NewsEvent{
		{Level: "板块", Score: 0.75, Sectors: []string{realName}, Title: "板块利好"},
	}
	bull, _ := e.BuildHotSectors(events)
	if len(bull) != 1 || bull[0].Name != realName {
		t.Fatalf("期望归因出板块 %q, 实际 %v", realName, bull)
	}
	t.Logf("板块 %q 行情补充: %+v", realName, bull[0])
}
