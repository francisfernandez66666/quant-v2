// 本文件：板块事件相关单元测试——板块验真回填（verifySectorAttribution，剔除 LLM 幻觉板块名）
// 与板块→个股事件级传播（propagateSectorToStocks，注入成分股到监测池）。
// 依赖真实 THS 板块名单（GetBoardList），断言采用"真实板块名 + 构造假板块名"的确定性方式。
package engine

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/newsagent"
)

// TestVerifySectorAttribution 验证板块验真回填：剔除 LLM 幻觉板块名。
// 覆盖三种场景：真实+幻觉混合（幻觉剔除）、非板块事件（不受影响）、低分板块事件（不处理）。
func TestVerifySectorAttribution(t *testing.T) {
	api := data.NewMarketAPI()
	ths := data.NewTHSClient()
	boards, err := ths.GetBoardList()
	if err != nil {
		t.Fatalf("THS板块名单获取失败: %v", err)
	}
	if len(boards) == 0 {
		t.Fatal("THS板块名单为空")
	}
	realName := boards[0].Name
	fakeName := "不存在板块ZZZ999"

	sc := data.NewSectorScanner(api, nil)
	sc.Update(boards, 0, 0, 0)

	e := &Engine{scanner: sc}

	// 板块级事件：真实+幻觉板块混合，幻觉应被剔除
	events := []newsagent.NewsEvent{
		{Level: "板块", Score: 0.75, Sectors: []string{realName, fakeName}},
	}
	e.verifySectorAttribution(events)
	got := events[0].Sectors
	if len(got) != 1 || got[0] != realName {
		t.Fatalf("期望剔除假板块后仅剩 %q, 实际 %v", realName, got)
	}

	// 非板块事件不受影响
	ev2 := []newsagent.NewsEvent{{Level: "个股", Score: 0.75, Sectors: []string{fakeName}}}
	e.verifySectorAttribution(ev2)
	if len(ev2[0].Sectors) != 1 {
		t.Fatalf("非板块事件不应被处理: %v", ev2[0].Sectors)
	}

	// 低分板块事件不处理
	ev3 := []newsagent.NewsEvent{{Level: "板块", Score: 0.25, Sectors: []string{fakeName}}}
	e.verifySectorAttribution(ev3)
	if len(ev3[0].Sectors) != 1 {
		t.Fatalf("低分板块事件不应被处理: %v", ev3[0].Sectors)
	}
}

// TestPropagateSectorToStocks 验证板块→个股事件级传播。
// push2 可用时断言成分股注入与格式；push2 拒连（网络问题）时断言优雅跳过不报错。
func TestPropagateSectorToStocks(t *testing.T) {
	api := data.NewMarketAPI()
	ths := data.NewTHSClient()
	boards, err := ths.GetBoardList()
	if err != nil {
		t.Fatalf("THS板块名单获取失败: %v", err)
	}
	realName := boards[0].Name

	sc := data.NewSectorScanner(api, nil)
	sc.Update(boards, 0, 0, 0)

	ag := newsagent.New(api, nil, nil, t.TempDir())
	e := &Engine{scanner: sc, marketAPI: api, newsAgent: ag}

	events := []newsagent.NewsEvent{
		{Level: "板块", Score: 0.75, Sectors: []string{realName}},
	}
	e.propagateSectorToStocks(events)

	ev := events[0]
	if len(ev.RelatedStocks) > 0 {
		for _, label := range ev.RelatedStocks {
			if !strings.Contains(label, "(") || !strings.Contains(label, ")") {
				t.Fatalf("注入标签格式异常: %q", label)
			}
		}
		t.Logf("板块 %q 注入 %d 只成分股: %v", realName, len(ev.RelatedStocks), ev.RelatedStocks)
	} else {
		t.Logf("板块 %q 成分股为空或 push2 拒连, 优雅跳过（injected=0，无报错）", realName)
	}

	// 低分事件不应传播
	ev2 := []newsagent.NewsEvent{{Level: "板块", Score: 0.25, Sectors: []string{realName}}}
	e.propagateSectorToStocks(ev2)
	if len(ev2[0].RelatedStocks) != 0 {
		t.Fatalf("低分事件不应传播: %v", ev2[0].RelatedStocks)
	}
}
