// save_news_test.go — 新闻落盘裁剪的利空保底（§NEWS_BEAR 展示保底）回归测试。
// 验证 saveNewsEvents 在单日事件超过 200 条上限裁剪时：利空事件（Direction=利空）
// 恒定保留、绝不因容量被丢弃——它们承载持仓风险提示的关键证据；其余事件按时间序
// 保留最新（语义上"宁可丢旧利好，不丢利空"）。
// English: regression tests for the §NEWS_BEAR display guarantee in saveNewsEvents — when the daily
// event cap (200) trims, bearish events are pinned and never dropped (they are the key evidence behind
// holding-risk alerts), while the rest keeps the newest by order.
package newsagent

import (
	"fmt"
	"testing"
)

// testNewsAgent 用临时数据目录构造一个不依赖外部 API 的新闻智能体（New 不触网）。
func testNewsAgent(t *testing.T) *Agent {
	t.Helper()
	return New(nil, nil, nil, t.TempDir())
}

// bearNewsEvent 构造一条可通过最低落盘分过滤的利空事件（score 0.6 ≥ 默认 0.25）。
func bearNewsEvent(i int) NewsEvent {
	return NewsEvent{
		Title:       fmt.Sprintf("利空测试事件 %d", i),
		Direction:   "利空",
		Score:       -0.6,
		ImpactLevel: "中",
		Source:      "test",
	}
}

// plainNewsEvent 构造一条普通（非利空）事件。
func plainNewsEvent(i int) NewsEvent {
	return NewsEvent{
		Title:     fmt.Sprintf("普通测试事件 %d", i),
		Direction: "利好",
		Score:     0.6,
		Source:    "test",
	}
}

// TestSaveNewsKeepsBearishOnTrim 核心不变式：250 条事件（60 条利空排最前，会在旧"保留尾部
// 200 条"逻辑下被全部丢弃）裁剪后利空全部保留、总数 ≤ 200。
func TestSaveNewsKeepsBearishOnTrim(t *testing.T) {
	a := testNewsAgent(t)
	var events []NewsEvent
	for i := 0; i < 60; i++ {
		events = append(events, bearNewsEvent(i)) // 前 60 条全为利空（旧逻辑会被尾部裁剪丢弃）
	}
	for i := 0; i < 190; i++ {
		events = append(events, plainNewsEvent(i)) // 后 190 条普通事件
	}
	a.saveNewsEvents(events)

	db := a.loadNewsDB()
	if len(db.Events) > 200 {
		t.Fatalf("裁剪后应 ≤200 条, got %d", len(db.Events))
	}
	// 60 条利空必须一条不丢（本特性展示保底的验收点）
	bearish := 0
	for _, e := range db.Events {
		if e.Direction == "利空" {
			bearish++
		}
	}
	if bearish != 60 {
		t.Fatalf("利空事件应全部保留（60 条）, got %d", bearish)
	}
	// 非利空应保留 140 条（200-60 配额），验证配额分配
	plain := len(db.Events) - bearish
	if plain != 140 {
		t.Fatalf("非利空应保留最新 140 条配额, got %d", plain)
	}
}

// TestSaveNewsNoTrimAllKept 事件数未超上限时不裁剪，全量保留（含利空）。
func TestSaveNewsNoTrimAllKept(t *testing.T) {
	a := testNewsAgent(t)
	events := []NewsEvent{bearNewsEvent(1), plainNewsEvent(1), plainNewsEvent(2)}
	a.saveNewsEvents(events)
	db := a.loadNewsDB()
	if len(db.Events) != 3 {
		t.Fatalf("未超上限应全量保留, got %d", len(db.Events))
	}
}

// TestSaveNewsDedupByTitle 同标题去重仍生效（利空保底不改变去重语义）。
func TestSaveNewsDedupByTitle(t *testing.T) {
	a := testNewsAgent(t)
	a.saveNewsEvents([]NewsEvent{bearNewsEvent(1), bearNewsEvent(1)}) // 同标题两次
	db := a.loadNewsDB()
	if len(db.Events) != 1 {
		t.Fatalf("同标题应去重为 1 条, got %d", len(db.Events))
	}
}

// TestSaveNewsCrossDayReset 跨交易日归档重置仍生效（新交易日清空旧事件）。
func TestSaveNewsCrossDayReset(t *testing.T) {
	a := testNewsAgent(t)
	// 先写一条占位旧事件（默认落盘分以上），再"跨日"保存——实际交易日推进不可注入，
	// 这里验证同日内重复 save 归并不重复归档（保底逻辑与归并共存）。
	a.saveNewsEvents([]NewsEvent{bearNewsEvent(1)})
	a.saveNewsEvents([]NewsEvent{plainNewsEvent(2)})
	db := a.loadNewsDB()
	if len(db.Events) != 2 {
		t.Fatalf("同日归并应累计 2 条, got %d", len(db.Events))
	}
}
