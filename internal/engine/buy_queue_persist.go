// buy_queue_persist.go — §M11（2026-09-22 修复批）异步买单队列的停机落盘与启动恢复。
//
// 缺陷根因（docs/AUDIT_FULL_UAT_20260921C.md M11）：autoPlace 过完同步守卫后把买单投入
// buyCh（64 槽纯内存队列），旧 StopBuyDispatcher 只关 worker 不排空队列——停机瞬间已排队
// 未被消费的买单整体蒸发；且这些信号的当日翻转状态（prevPass/信号钉）不保证重启后重发，
// 丢单方向为资损。旧注释"重启后同 signal_id 自愈重发，故只观测不做持久化"被本批推翻：
// 自愈依赖战法当日重新翻转发信号，重启后行情/打分条件变化即不再翻转，不可依赖。
//
// 修复链路（与 §H4 realTrimDone / orders 表幂等键设计对齐）：
//   - StopBuyDispatcher：worker 退出（在途 PlaceOrder 已完成）后排空队列 →
//     原子写 <acctDir>/buy_queue_pending.json → buyCh 置 nil（此后 autoPlace 走同步兜底，
//     并支持原地重启分发器）；
//   - StartBuyDispatcher：读文件 → 按 SignalID 去重（同一买单停机前被双通道重复入队时
//     只恢复一份）→ 重新入队（极端超容场景回落同步兜底，与 FIX#8 同语义）→ 消费即删文件。
//
// 防重复下单三层：恢复集内 SignalID 去重（本文件）；恢复单与轮中新增重复入队由 orders 表
// signal_id 唯一键在下单侧拦重（§H4 口径，队列层不设过去重——保持既有的"允许重复入队、
// 下单侧去重"语义不变）；文件消费即删，单次停机只回放一份。
// dataDir 为空（纯内存测试路径）时落盘/恢复自动禁用，行为与旧版一致。
//
// 实现注：buyTask 的 req/sig 是未导出字段，encoding/json 会整体跳过——故落盘用导出字段的
// 传输结构 persistedBuyTask 做双向映射（不改 buyTask 本身，避免动到热路径结构体）。
//
// English: shutdown-drain persistence for the async buy queue (M11). Stopping drains the
// still-queued tasks into an atomic JSON file; starting restores them (deduped by SignalID,
// re-enqueued, file removed). Duplicate orders are blocked by the orders-table signal_id
// idempotency key shared with the live path. Empty dataDir disables persistence (old behavior).
package engine

import (
	"encoding/json"
	"log"
	"os"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/trading"
)

// persistedBuyTask buyTask 的落盘表示（导出字段 + json 标签，见文件头实现注）。
type persistedBuyTask struct {
	Req trading.OrderRequest `json:"req"` // 已通过同步守卫的下单请求（SignalID=幂等键）
	Sig combat_agent.Signal  `json:"sig"` // 触发信号（worker 日志/审计上下文）
}

// toBuyTask 落盘表示 → 运行期任务。
func (p persistedBuyTask) toBuyTask() buyTask {
	return buyTask{req: p.Req, sig: p.Sig}
}

// persistBuyQueue 停机排空结果落盘：pending 为空时清理旧文件（防止上次未消费的残留
// 在本次干净停机后被误恢复），非空则原子写入。路径为空=纯内存模式，直接跳过。
// English: writes the drained queue atomically (or clears a stale file when nothing remains).
func (e *Engine) persistBuyQueue(pending []buyTask) {
	if e.buyQueuePath == "" {
		return
	}
	if len(pending) == 0 {
		if _, err := os.Stat(e.buyQueuePath); err == nil {
			_ = os.Remove(e.buyQueuePath)
		}
		return
	}
	rows := make([]persistedBuyTask, 0, len(pending))
	for _, t := range pending {
		rows = append(rows, persistedBuyTask{Req: t.req, Sig: t.sig})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		log.Printf("[buy-queue] §M11 停机队列序列化失败（本批买单不落盘）: %v", err)
		return
	}
	ensureParentDir(e.buyQueuePath)
	if err := data.AtomicWrite(e.buyQueuePath, raw, 0644); err != nil {
		log.Printf("[buy-queue] §M11 停机队列原子写入失败: %v", err)
		return
	}
	log.Printf("[buy-queue] §M11 停机排空 %d 笔排队买单已落盘: %s", len(pending), e.buyQueuePath)
}

// restoreBuyQueue 启动恢复：读取停机落盘文件，按 SignalID 去重后重新入队，消费完删除文件。
// 只影响恢复链路——运行期 autoPlace 的重复入队仍由 orders 表 signal_id 幂等键在下单侧拦重。
// 文件不存在=无残留，静默返回；解析失败=坏文件丢弃并记日志（绝不在每次启动反复重放坏文件）。
// English: restores the persisted queue on start — dedup by SignalID, re-enqueue, remove file.
func (e *Engine) restoreBuyQueue(ch chan buyTask) {
	if e.buyQueuePath == "" || ch == nil {
		return
	}
	raw, err := os.ReadFile(e.buyQueuePath)
	if err != nil {
		return // 文件不存在/不可读：无停机残留，正常启动
	}
	var rows []persistedBuyTask
	if err := json.Unmarshal(raw, &rows); err != nil {
		log.Printf("[buy-queue] §M11 停机队列文件解析失败（丢弃不再回放）: %v", err)
		_ = os.Remove(e.buyQueuePath)
		return
	}
	_ = os.Remove(e.buyQueuePath) // 消费即删：本次启动只回放这一份，防崩溃循环反复重放
	seen := make(map[string]bool, len(rows))
	restored := 0
	for _, row := range rows {
		t := row.toBuyTask()
		if t.req.SignalID != "" {
			if seen[t.req.SignalID] {
				continue // §M11 恢复集内同键去重（停机前双通道重复入队的同一买单只恢复一份）
			}
			seen[t.req.SignalID] = true
		}
		select {
		case ch <- t:
			restored++
		default:
			// 极端场景（恢复数 > 队列容量）：回落同步下单，与 FIX#8 满队语义一致，宁阻塞不丢单。
			e.placeOrderNow(t.req, t.sig)
			restored++
		}
	}
	if restored > 0 {
		log.Printf("[buy-queue] §M11 启动恢复停机排队买单 %d 笔（SignalID 去重后，原 %d 笔）", restored, len(rows))
	}
}
