// 固化事件存储（news_events.json）单测：验证利好/利空固化、覆盖键(sector|direction)、
// 跨交易日到期清除与磁盘持久化回读。
// English: Unit tests for frozen event storage (news_events.json): verify bullish/bearish freezing,
// English: the overwrite key (sector|direction), cross-trading-day expiry cleanup, and disk-persisted reload.
package newsagent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkEvent 构造一个带方向与板块的测试事件。
// English: mkEvent builds a test event with a direction and sectors.
func mkEvent(title, dir string, score float64, sectors []string) NewsEvent {
	return NewsEvent{
		Title:     title,
		Direction: dir,
		Score:     score,
		Sectors:   sectors,
	}
}

// TestFrozenKey 校验覆盖键：sector|direction。
// English: TestFrozenKey verifies the overwrite key: sector|direction.
func TestFrozenKey(t *testing.T) {
	if got := frozenKey(mkEvent("t", "利好", 0.8, []string{"半导体"})); got != "半导体|利好" {
		t.Fatalf("frozenKey 期望=半导体|利好, 得=%q", got)
	}
	if got := frozenKey(mkEvent("t", "利空", -0.9, []string{""})); got != "t|利空" {
		t.Fatalf("无板块回退标题, 得=%q", got)
	}
	if got := frozenKey(mkEvent("t", "", -0.5, []string{"银行"})); got != "银行|利空" {
		t.Fatalf("方向缺失按分数符号推断, 得=%q", got)
	}
}

// TestShouldFreeze 验证固化条件：|score|≥0.25 且方向为利好/利空。
// English: TestShouldFreeze verifies the freeze condition: |score|>=0.25 and direction bullish/bearish.
func TestShouldFreeze(t *testing.T) {
	if !shouldFreeze(mkEvent("", "利好", 0.3, nil)) {
		t.Fatal("利好0.3 应固化")
	}
	if !shouldFreeze(mkEvent("", "利空", -0.6, nil)) {
		t.Fatal("利空-0.6 应固化")
	}
	if shouldFreeze(mkEvent("", "中性", 0.8, nil)) {
		t.Fatal("中性不该固化")
	}
	if shouldFreeze(mkEvent("", "利好", 0.1, nil)) {
		t.Fatal("分数不足不该固化")
	}
}

// newTestAgent 构造使用临时目录的 Agent，便于独立验证固化文件行为。
// English: newTestAgent builds an Agent backed by a temp directory, for isolated verification of frozen-file behavior.
func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	a := &Agent{
		frozenPath: filepath.Join(dir, "frozen_events.json"),
	}
	return a
}

// TestSaveFrozenAppendAndOverwrite 验证：同板块+同方向覆盖取最新；异板块追加。
// English: TestSaveFrozenAppendAndOverwrite verifies: same sector+direction overwrites with the latest; different sector appends.
func TestSaveFrozenAppendAndOverwrite(t *testing.T) {
	a := newTestAgent(t)

	// 第一轮：半导体利好 0.8
	// English: Round 1: semiconductor bullish 0.8.
	a.SaveFrozen([]NewsEvent{mkEvent("A1", "利好", 0.8, []string{"半导体"})})
	db := a.loadFrozenDB()
	if len(db.Events) != 1 {
		t.Fatalf("第一轮后应 1 条, 得 %d", len(db.Events))
	}

	// 第二轮：半导体利好新事件 0.6 → 覆盖（分数取最新 0.6）；银行利空追加
	// English: Round 2: new semiconductor bullish 0.6 → overwrite (score takes latest 0.6); bank bearish appends.
	a.SaveFrozen([]NewsEvent{
		mkEvent("A2新", "利好", 0.6, []string{"半导体"}),
		mkEvent("B1", "利空", -0.7, []string{"银行"}),
	})
	db = a.loadFrozenDB()
	if len(db.Events) != 2 {
		t.Fatalf("覆盖+追加后应 2 条, 得 %d", len(db.Events))
	}
	var semi, bank *FrozenEvent
	for i := range db.Events {
		switch db.Events[i].Key {
		case "半导体|利好":
			semi = &db.Events[i]
		case "银行|利空":
			bank = &db.Events[i]
		}
	}
	if semi == nil || bank == nil {
		t.Fatalf("应同时存在 半导体利好 与 银行利空: %+v", db.Events)
	}
	// 覆盖后分数取最新值(0.6)，标题为最新
	// English: After overwrite the score takes the latest (0.6), with the latest title.
	if semi.Score != 0.6 || semi.Title != "A2新" {
		t.Fatalf("覆盖分数/标题应为最新(0.6/A2新), 得 score=%.2f title=%s", semi.Score, semi.Title)
	}
}

// TestSaveFrozenSkipsWeak 验证低于阈值或中性事件不入固化。
// English: TestSaveFrozenSkipsWeak verifies that below-threshold or neutral events are not frozen.
func TestSaveFrozenSkipsWeak(t *testing.T) {
	a := newTestAgent(t)
	a.SaveFrozen([]NewsEvent{
		mkEvent("弱", "利好", 0.1, []string{"半导体"}),
		mkEvent("中性", "中性", 0.8, []string{"半导体"}),
	})
	db := a.loadFrozenDB()
	if len(db.Events) != 0 {
		t.Fatalf("弱/中性事件不应固化, 得 %d", len(db.Events))
	}
}

// TestFrozenExpiry 纯函数验证到期边界：生成日 D 在 D 与 D+1 有效，D+2 过期。
// English: TestFrozenExpiry is a pure-function check of expiry boundaries: day D valid on D and D+1, expired on D+2.
func TestFrozenExpiry(t *testing.T) {
	fe := FrozenEvent{Day: "20260101"}
	if isFrozenExpired(fe, "20260101") {
		t.Fatal("D 日不应过期")
	}
	if isFrozenExpired(fe, "20260102") {
		t.Fatal("D+1 不应过期")
	}
	if !isFrozenExpired(fe, "20260103") {
		t.Fatal("D+2 应过期")
	}
}

// TestFrozenSaveCrossDay 验证 SaveFrozen 跨日到期清理（Day 用 TradingDay 同格式 20060102）。
// English: TestFrozenSaveCrossDay verifies SaveFrozen's cross-day expiry cleanup (Day uses TradingDay's 20060102 format).
func TestFrozenSaveCrossDay(t *testing.T) {
	a := newTestAgent(t)
	yesterday := time.Now().AddDate(0, 0, -1).Format("20060102")
	twoDaysAgo := time.Now().AddDate(0, 0, -2).Format("20060102")

	a.writeFrozenDB(&frozenDB{
		TradingDay: yesterday,
		Events: []FrozenEvent{
			{NewsEvent: mkEvent("昨日利好", "利好", 0.9, []string{"半导体"}), Day: yesterday, Key: "半导体|利好"},
			{NewsEvent: mkEvent("前天利好", "利好", 0.9, []string{"白酒"}), Day: twoDaysAgo, Key: "白酒|利好"},
		},
	})

	a.SaveFrozen(nil)
	db := a.loadFrozenDB()
	if len(db.Events) != 1 {
		t.Fatalf("跨日保留昨日(1条), 得 %d", len(db.Events))
	}
	if db.Events[0].Key != "半导体|利好" {
		t.Fatalf("应保留昨日半导体利好, 得 %s", db.Events[0].Key)
	}
}

// TestFrozenCorruptRecovery 验证文件损坏（截断缺尾括号）时逐条抢救；完全损坏备份并返回空库。
// English: TestFrozenCorruptRecovery verifies per-record salvage when the file is corrupted (truncated, missing the closing brace); fully corrupt files are backed up and an empty store is returned.
func TestFrozenCorruptRecovery(t *testing.T) {
	a := newTestAgent(t)

	// 截断：整体 JSON 缺尾括号解析失败，但每个事件对象行仍可单独解析 → 逐条抢救。
	// （用动态当日日期，确保抢救出的事件不会被 isFrozenExpired 按跨日窗口判为过期而过滤。）
	// （Dynamic today-based day keeps salvaged events within the expiry window so FrozenEvents returns them.）
	// English: Truncated: whole-JSON parse fails (missing closing brace) but each event object line still parses individually → salvage line by line.
	// English: A dynamic today-based day keeps salvaged events within the expiry window so they are not filtered out.
	cur := time.Now().Format("20060102")
	broken := fmt.Sprintf(`{"trading_day":%q,
"events":[
{"title":"抢救成功","key":"x|利好","day":%q},
{"title":"第二条","key":"y|利好","day":%q}`, cur, cur, cur)
	if err := os.WriteFile(a.frozenPath, []byte(broken), 0644); err != nil {
		t.Fatal(err)
	}
	fev := a.FrozenEvents()
	if len(fev) != 2 {
		t.Fatalf("逐行抢救应恢复 2 条, 得 %+v", fev)
	}

	// 完全损坏：无可用对象 → 备份 .bak 并返回空库
	// English: Fully corrupt: no usable objects → back up as .bak and return an empty store.
	if err := os.WriteFile(a.frozenPath, []byte(`{{{garbage`), 0644); err != nil {
		t.Fatal(err)
	}
	fev = a.FrozenEvents()
	if len(fev) != 0 {
		t.Fatalf("完全损坏应返回空库, 得 %+v", fev)
	}
	if _, err := os.Stat(a.frozenPath + ".bak"); err != nil {
		t.Fatal("损坏文件应备份为 .bak")
	}
}

// TestFrozenEventsOnlyUnExpired 验证 FrozenEvents 仅返回未过期事件。
// English: TestFrozenEventsOnlyUnExpired verifies FrozenEvents returns only unexpired events.
func TestFrozenEventsOnlyUnExpired(t *testing.T) {
	a := newTestAgent(t)
	td := time.Now().AddDate(0, 0, -2).Format("20060102")
	a.writeFrozenDB(&frozenDB{
		Events: []FrozenEvent{
			{NewsEvent: mkEvent("过期", "利好", 0.8, []string{"半导体"}), Day: td, Key: "半导体|利好"},
		},
	})
	if evs := a.FrozenEvents(); len(evs) != 0 {
		t.Fatalf("过期事件不应返回, 得 %+v", evs)
	}
}
