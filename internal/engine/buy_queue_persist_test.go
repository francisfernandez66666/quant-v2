// buy_queue_persist_test.go — §M11（2026-09-22 修复批）异步买单队列停机落盘/启动恢复的反例锁。
//
// 缺陷原型（docs/AUDIT_FULL_UAT_20260921C.md M11）：autoPlace 过完同步守卫后把买单投入
// buyCh（64 槽内存队列），旧 StopBuyDispatcher 只关 worker、不排空队列——停机瞬间已排队
// 未消费的买单随重启蒸发（丢单方向=资损）。旧口径寄望"重启后同 signal_id 自愈重发"，
// 但自愈依赖战法当日重新翻转发信号，重启后行情/打分条件变化即不再翻转，不可依赖。
//
// 本锁钉死三件事：
//
//	① 排空落盘：Stop 把队列残余原子写文件、buyCh 置 nil（此后 autoPlace 走同步兜底）；
//	② 启动恢复不漏不重：Start 恢复全部排队单（每笔恰一到网关），文件消费即删；
//	   恢复集内同 SignalID 重复（停机前双通道重复入队形态）只回放一份；
//	③ 干净停机不留残档：排空结果为空时旧文件被清除，不会在下一次启动被误恢复。
//
// English: locks shutdown-drain persistence of the in-memory buy queue — stop drains the queue
// into an atomic JSON file, start restores every queued order exactly once (deduped by
// SignalID inside the restored set), and the file is consumed (or cleared when empty).
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// m11BuySig 构造可通过 autoPlace 同步守卫的买入信号（harness 的 cfg 已是 enabled+auto）。
func m11BuySig(code, name string) combat_agent.Signal {
	return combat_agent.Signal{
		ID: "sig-" + code, Code: code, Name: name, Strategy: "龙头",
		Direction: "做多", Action: "买入", Price: 10, Confidence: 0.9, GeneratedAt: time.Now(),
	}
}

// waitForOrders 轮询 mock 网关收到的委托数直到 want 笔（超时即失败）。
func waitForOrders(t *testing.T, mu *sync.Mutex, orders *[]map[string]interface{}, want int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(*orders)
		mu.Unlock()
		if n >= want {
			return
		}
		select {
		case <-deadline:
			mu.Lock()
			got := len(*orders)
			mu.Unlock()
			t.Fatalf("网关 5s 内只收到 %d 笔委托，期望 %d 笔", got, want)
		case <-time.After(30 * time.Millisecond):
		}
	}
}

// TestBuyQueueShutdownDrainAndRestore §M11 主锁：排队单停机落盘 → 重启恢复恰一到单，
// 恢复集内同 SignalID 去重，文件消费即删。
func TestBuyQueueShutdownDrainAndRestore(t *testing.T) {
	e, _, srv, orders, mu := newAsyncEngine(t)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "buy_queue_pending.json")
	e.mu.Lock()
	e.buyQueuePath = path
	e.buyCh = make(chan buyTask, 64) // 手工装配"只排队不消费"形态（无 worker），停机时残余确定
	e.buyStop = make(chan struct{})
	e.mu.Unlock()

	live := map[string]*data.StockInfo{
		"600000": {Code: "600000", Price: 10},
		"600001": {Code: "600001", Price: 10},
		"600002": {Code: "600002", Price: 10},
	}
	// 三笔排队单 + 一笔同键重复（停机前主循环/近实时双通道重复入队的现实形态）。
	e.autoPlace(m11BuySig("600000", "浦发银行"), live)
	e.autoPlace(m11BuySig("600001", "测试一号"), live)
	e.autoPlace(m11BuySig("600002", "测试二号"), live)
	e.autoPlace(m11BuySig("600000", "浦发银行"), live)

	// —— ① 停机：队列残余排空落盘（4 条原始记录，含重复）——
	e.StopBuyDispatcher()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("停机后排队买单应落盘（旧缺陷：随重启蒸发）: %v", err)
	}
	var persisted []persistedBuyTask
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("落盘文件解析失败: %v", err)
	}
	if len(persisted) != 4 {
		t.Fatalf("落盘应保留队列原样 4 条（去重在恢复侧），得 %d", len(persisted))
	}
	e.mu.RLock()
	chNil := e.buyCh == nil
	e.mu.RUnlock()
	if !chNil {
		t.Fatal("停机后 buyCh 应置 nil（autoPlace 转同步兜底，支持原地重启）")
	}
	// 停机后 autoPlace 不得再入死队列——同步兜底直接下单一笔（600003 未入过 orders，幂等键放行）。
	e.autoPlace(m11BuySig("600003", "兜底股"), map[string]*data.StockInfo{"600003": {Code: "600003", Price: 10}})
	waitForOrders(t, mu, orders, 1)

	// —— ② 重启：StartBuyDispatcher 恢复入队，每笔 SignalID 恰一到网关（重复 X 去重）——
	e.StartBuyDispatcher(1)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("恢复后落盘文件应消费即删（防崩溃循环反复重放）")
	}
	// 期望网关：兜底股 1 + 恢复 3（600000 双记录去重成 1 单；其重复单另有 orders 表幂等键兜底）。
	waitForOrders(t, mu, orders, 4)
	time.Sleep(200 * time.Millisecond) // 再等一拍，确认没有第五笔（去重失效的表征）
	mu.Lock()
	seen := map[string]int{}
	for _, o := range *orders {
		sid, _ := o["signal_id"].(string)
		seen[sid]++
	}
	mu.Unlock()
	if len(seen) != 4 {
		t.Fatalf("应恰好 4 个不同 signal_id 各一单，得 %v", seen)
	}
	for sid, n := range seen {
		if n != 1 {
			t.Fatalf("signal_id %s 被重复执行 %d 次（恢复链路重复下单）", sid, n)
		}
	}
	e.StopBuyDispatcher() // 干净停机：队列已空 → ③ 残档清除
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("空队列停机不得留下残档")
	}
}

// TestBuyQueueRestoreDedupUnit §M11 恢复集去重的单元锁（不经网关，直接断言恢复入队结果）：
// 落盘集 [A,B,A] → 恢复入队 [A,B]（SignalID 首次出现者保留、顺序保持）。
func TestBuyQueueRestoreDedupUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buy_queue_pending.json")
	e1 := &Engine{}
	e1.buyQueuePath = path
	task := func(sid, code string) buyTask {
		bt := buyTask{}
		bt.req.SignalID = sid
		bt.req.Code = code
		bt.req.Side = "买入"
		bt.req.Price = 10
		bt.req.Qty = 1000
		bt.sig = combat_agent.Signal{Code: code}
		return bt
	}
	e1.persistBuyQueue([]buyTask{task("A", "600000"), task("B", "600001"), task("A", "600000")})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("落盘失败: %v", err)
	}

	e2 := &Engine{}
	e2.buyQueuePath = path
	ch := make(chan buyTask, 64)
	e2.restoreBuyQueue(ch)
	if len(ch) != 2 {
		t.Fatalf("恢复集应按 SignalID 去重成 2 笔，得 %d", len(ch))
	}
	if first := <-ch; first.req.SignalID != "A" {
		t.Fatalf("恢复应保持首现顺序，第一笔应为 A，得 %s", first.req.SignalID)
	}
	if second := <-ch; second.req.SignalID != "B" {
		t.Fatalf("第二笔应为 B，得 %s", second.req.SignalID)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("恢复后文件应消费即删")
	}

	// —— ③ 空排空清档：persist(nil) 必须移除旧文件 ——
	e2.persistBuyQueue([]buyTask{task("C", "600002")})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("非空落盘失败: %v", err)
	}
	e2.persistBuyQueue(nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("空排空应清除旧文件（防下次启动误恢复）")
	}
	// 无文件时恢复静默无操作（正常启动路径）。
	ch2 := make(chan buyTask, 8)
	e2.restoreBuyQueue(ch2)
	if len(ch2) != 0 {
		t.Fatalf("无残留文件时不应有恢复单，得 %d", len(ch2))
	}
}
