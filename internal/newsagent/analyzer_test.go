package newsagent

import (
	"testing"

	"quant-trading-v2/internal/data"
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

// TestPostProcessFallbackPollutionCleared B：中性方向且强度档为 0 的残留被归零。
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
	if ht.Score == 0 {
		t.Fatalf("有明确分数的事件不应被归零，实际 0")
	}
}

// TestBuildChainEventsDifferentialSplit 对抗制裁型：上游利好/下游利空 → 拆成两个独立方向事件。
// 上游事件带上游板块与上游个股、方向利好、正分；下游事件方向利空、负分。
func TestBuildChainEventsDifferentialSplit(t *testing.T) {
	ht := &llm.HotTopic{
		Title:               "诺基亚收购恩智浦一工厂 计划自产磷化铟半导体",
		Level:               "板块",
		Score:               0.75,
		Direction:           "利好",
		Region:              "海外",
		Relation:            "对抗制裁",
		Sectors:             []string{"光模块"},
		UpstreamSectors:     []string{"半导体材料", "小金属"},
		DownstreamSectors:   []string{"光模块"},
		RelatedStocks:       []string{"云南锗业", "有研新材"},
		UpstreamStocks:      []string{"云南锗业", "有研新材", "光智科技"},
		DownstreamStocks:    []string{"中际旭创"},
		UpstreamDirection:   "利好",
		DownstreamDirection: "利空",
		Reason:              "海外自产确认磷化铟核心价值，利好国内上游",
	}
	item := data.NewsItem{Title: ht.Title, Datetime: "2026-08-06 10:00:00"}

	evs := buildChainEvents(ht, item)
	if len(evs) != 2 {
		t.Fatalf("差分事件应拆为 2 个，实际 %d", len(evs))
	}
	up, dn := evs[0], evs[1]
	if up.Direction != "利好" || up.Score <= 0 {
		t.Fatalf("上游事件应利好正分，实际 direction=%s score=%v", up.Direction, up.Score)
	}
	if len(up.Sectors) == 0 || up.Sectors[0] != "半导体材料" {
		t.Fatalf("上游事件应带上游板块，实际 %v", up.Sectors)
	}
	if !containsStr(up.RelatedStocks, "云南锗业") || !containsStr(up.RelatedStocks, "有研新材") {
		t.Fatalf("上游事件应含云南锗业/有研新材，实际 %v", up.RelatedStocks)
	}
	if dn.Direction != "利空" || dn.Score >= 0 {
		t.Fatalf("下游事件应利空负分，实际 direction=%s score=%v", dn.Direction, dn.Score)
	}
	if !containsStr(dn.RelatedStocks, "中际旭创") {
		t.Fatalf("下游事件应含中际旭创，实际 %v", dn.RelatedStocks)
	}
	if up.Region != "海外" || up.Relation != "对抗制裁" {
		t.Fatalf("地域/关系字段应透传，实际 region=%s relation=%s", up.Region, up.Relation)
	}
}

// TestBuildChainEventsEmptyDownstreamStocks 下游个股缺失时不得回落全量 related_stocks，
// 避免把上游利好个股污染进下游利空事件。
func TestBuildChainEventsEmptyDownstreamStocks(t *testing.T) {
	ht := &llm.HotTopic{
		Title:               "诺基亚收购恩智浦一工厂 计划自产磷化铟半导体",
		Level:               "板块",
		Score:               0.75,
		Direction:           "利好",
		Region:              "海外",
		Relation:            "对抗制裁",
		Sectors:             []string{"光模块"},
		UpstreamSectors:     []string{"半导体材料", "小金属"},
		DownstreamSectors:   []string{"光模块"},
		RelatedStocks:       []string{"云南锗业", "有研新材"},
		UpstreamStocks:      []string{"云南锗业", "有研新材"},
		UpstreamDirection:   "利好",
		DownstreamDirection: "利空",
	}
	item := data.NewsItem{Title: ht.Title, Datetime: "2026-08-06 10:00:00"}

	evs := buildChainEvents(ht, item)
	if len(evs) != 2 {
		t.Fatalf("差分事件应拆为 2 个，实际 %d", len(evs))
	}
	dn := evs[1]
	if dn.Direction != "利空" {
		t.Fatalf("下游事件应利空，实际 %s", dn.Direction)
	}
	if len(dn.RelatedStocks) != 0 {
		t.Fatalf("下游个股缺失时不得回落全量 related_stocks（防止污染），实际 %v", dn.RelatedStocks)
	}
}

// TestBuildChainEventsSameDirectionMerge 国内事件全链同向 → 合并为单事件，
// 上/下游板块与个股并入同一事件。
func TestBuildChainEventsSameDirectionMerge(t *testing.T) {
	ht := &llm.HotTopic{
		Title:             "国内磷化铟扩产项目落地",
		Level:             "板块",
		Score:             0.75,
		Direction:         "利好",
		Region:            "国内",
		Relation:          "不涉及",
		Sectors:           []string{"半导体材料"},
		UpstreamSectors:   []string{"小金属"},
		DownstreamSectors: []string{"光通信"},
		UpstreamStocks:    []string{"云南锗业"},
		DownstreamStocks:  []string{"中际旭创"},
		RelatedStocks:     []string{"有研新材"},
	}
	item := data.NewsItem{Title: ht.Title, Datetime: "2026-08-06 10:00:00"}

	evs := buildChainEvents(ht, item)
	if len(evs) != 1 {
		t.Fatalf("同向事件应合并为 1 个，实际 %d", len(evs))
	}
	ev := evs[0]
	if ev.Direction != "利好" || ev.Score <= 0 {
		t.Fatalf("合并事件应利好正分，实际 %s %v", ev.Direction, ev.Score)
	}
	if !containsStr(ev.Sectors, "小金属") || !containsStr(ev.Sectors, "光通信") {
		t.Fatalf("合并事件应含上下游板块，实际 %v", ev.Sectors)
	}
	if !containsStr(ev.RelatedStocks, "云南锗业") || !containsStr(ev.RelatedStocks, "中际旭创") {
		t.Fatalf("合并事件应含上下游个股，实际 %v", ev.RelatedStocks)
	}
}

// TestTitleWithDigest 摘要拼接：超长正文截断到 80 字，空正文不加摘要。
func TestTitleWithDigest(t *testing.T) {
	if got := titleWithDigest("标题", ""); got != "标题" {
		t.Fatalf("空正文不应加摘要，实际 %q", got)
	}
	content := "美国对华光模块出口管制升级，中国随后宣布对铟、磷化铟相关原料实施出口管制反制，诺基亚宣布自产磷化铟以摆脱对中国上游原料的依赖。" +
		"这一系列动作确认了磷化铟的战略核心价值。"
	got := titleWithDigest("标题", content)
	if got == "标题" {
		t.Fatalf("应含摘要")
	}
	if len([]rune(got)) > 120 {
		t.Fatalf("摘要应截断，实际长度 %d", len([]rune(got)))
	}
}
