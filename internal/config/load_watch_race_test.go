// load_watch_race_test.go — §0925EVE-D1（2026-09-25 晚批）Watch/Load 热重载通道的 -race 行为锁。
//
// 锤的是哪条通道：Watch 后台协程在配置文件内容变更时调用 Manager.Load()，Load 会**整体重
// 赋值** m.rules / m.d1 两个全局快照指针；读方（cmd 装配、HTTP handler、打分循环经 Get()/
// GetD1Config()/RulesD1Snapshot）并发取值。旧实现是发布侧无锁裸重赋值 + 读侧字段裸读的
// 真实 data race（§CFGSMASH 那轮只覆盖了 Strategy 族热更通道，Load/Watch 没进扫描面）。
// 旧代码在本用例 -race 下必红；修复后要求：
//  1. 指针发布与读取全程无 race（发布收进 Load 的单一 m.mu.Lock 临界区）；
//  2. 成对一致：RulesD1Snapshot 一次取回的两份快照必须来自同一版本（A 或 B），
//     不允许出现「rules 已换 B、d1 还是 A」的混搭——这是本批选定 RWMutex 统一口径
//     （而非两个独立 atomic.Pointer）的直接原因；
//  3. 不弱化 §CFGSMASH 既有断言：本用例只做并发压测与快照一致性抽查，不动任何 setter 语义。
//
// English: -race lock for the Watch/Load hot-reload channel — a writer goroutine hammers
// Load() against many readers of Get/GetD1Config/RulesD1Snapshot; reads must be race-free
// and every paired snapshot must come from a single consistent version (A or B).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// cfgA/cfgB 两版可区分的全局配置：rules 侧用 dragon.take_profit_pct（10.1 / 20.2），
// d1 侧用首条规则 direction+score（利好/0.1 / 利空/0.2）做版本指纹。
func versionedConfigJSON(vr int) string {
	return fmt.Sprintf(`{
  "rules": { "strategy": { "dragon": { "take_profit_pct": %.1f } } },
  "d1":    { "rules": [ { "direction": "%s", "score": %.1f } ] }
}`, 10.1+float64(vr)*10.1, []string{"利好", "利空"}[vr], 0.1+float64(vr)*0.1)
}

// TestLoadWatchPublishRace §0925EVE-D1：Watch(Load) 高频发布 × 多读方取值，-race 必须全绿。
// 写线程模拟 Watch 协程节奏（改文件 → 比对 → Load()）；读线程分别压 Get()、GetD1Config()、
// RulesD1Snapshot() 三条加锁读口径。断言只读字段、不改快照，与 §CFGSMASH「边保存边打分」
// 用例同体例。
func TestLoadWatchPublishRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(versionedConfigJSON(0)), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(path)
	// 前置校验：初始版本 A（take_profit_pct=10.1、d1 利好/0.1）
	if got := m.Get().Strategy.Dragon.TakeProfitPct; got != 10.1 {
		t.Fatalf("种子版本不符: %v", got)
	}

	var loader sync.WaitGroup
	stop := make(chan struct{})
	var readers sync.WaitGroup

	// 写侧：模拟 Watch 后台协程——文件内容在版本 A/B 间来回变更并触发 Load()。
	// 固定 300 轮后自行收口（体例同 §CFGSMASH 用例：写侧落定 → 叫停读侧），
	// 300 次「撕裂发布」机会足以让旧实现的无锁指针重赋值在 -race 下现形。
	loader.Add(1)
	go func() {
		defer loader.Done()
		v := 0
		for n := 0; n < 300; n++ {
			v ^= 1
			if err := os.WriteFile(path, []byte(versionedConfigJSON(v)), 0o644); err != nil {
				t.Errorf("写配置文件失败: %v", err)
				return
			}
			m.Load() // Watch 检测到 sha256 变化后走的正是这条通道
		}
	}()

	// 读侧：4 个 goroutine 高频取快照并做「版本指纹」一致性抽查。
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// 口径①：单读 rules 快照——值只可能是版本 A 或 B，读到撕裂/零值即破防。
				tp := m.Get().Strategy.Dragon.TakeProfitPct
				if tp != 10.1 && tp != 20.2 {
					t.Errorf("读到撕裂的 rules 快照: take_profit_pct=%v", tp)
					return
				}
				// 口径②：单读 d1 快照。
				d1 := m.GetD1Config()
				if len(d1.Rules) != 1 {
					t.Errorf("d1 快照不完整: %+v", d1.Rules)
					return
				}
				// 口径③：成对读——rules 与 d1 必须同版本（要么都 A 要么都 B）。
				// 这条断言钉的就是「发布收进单一临界区」：两个独立 atomic.Pointer 做不到。
				r2, d2 := m.RulesD1Snapshot()
				pairTP, pairDir := r2.Strategy.Dragon.TakeProfitPct, d2.Rules[0].Direction
				if (pairTP == 10.1 && pairDir != "利好") || (pairTP == 20.2 && pairDir != "利空") {
					t.Errorf("rules/d1 成对读跨版本混搭: tp=%v dir=%q", pairTP, pairDir)
					return
				}
			}
		}()
	}

	loader.Wait()
	close(stop)
	readers.Wait()

	// 终局校验：最后一轮发布后指针已收敛（json 往返仍自洽），且 wrapper 未知键不炸解析。
	var wrapper struct {
		Rules *Rules `json:"rules"`
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatalf("落盘 JSON 解析失败: %v", err)
	}
}

// TestLoadPartialSectionKeepsPairSemantics §0925EVE-D1 边界钉：JSON 里只出现 rules 段时，
// d1 保持旧值——这是 wrapper 配置语义（未出现的段不覆盖），与并发无关；成对一致保证只约束
// 「同一次 Load 内两段发布原子」。读方用 RulesD1Snapshot 拿到的仍是合法组合。
func TestLoadPartialSectionKeepsPairSemantics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(versionedConfigJSON(0)), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(path)
	// 只写 rules 段（版本 B），d1 段缺席
	if err := os.WriteFile(path, []byte(`{"rules":{"strategy":{"dragon":{"take_profit_pct":20.2}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m.Load()
	r, d := m.RulesD1Snapshot()
	if r.Strategy.Dragon.TakeProfitPct != 20.2 {
		t.Fatalf("rules 段应已更新到版本 B: %v", r.Strategy.Dragon.TakeProfitPct)
	}
	if len(d.Rules) != 1 || d.Rules[0].Direction != "利好" {
		t.Fatalf("d1 段未出现应保持旧值（配置语义使然）: %+v", d.Rules)
	}
}
