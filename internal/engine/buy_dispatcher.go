// buy_dispatcher.go — 异步买单分发器从 engine.go 拆出的独立文件（2026-10-26，§ENGINE-SPLIT W2）。
//
// 行为零改动：只搬 StartBuyDispatcher/StopBuyDispatcher 两个方法，worker 循环仍调
// placeOrderNow（保留在 engine.go，因它被门禁按文件/函数名锁定）。
// English: verbatim move of the async buy dispatcher methods; the worker still calls placeOrderNow
// (kept in engine.go because static gates reference it there).
package engine

import "quant-trading-v2/internal/metrics"

// StartBuyDispatcher §A+B 启动异步下单 worker 池（事件驱动热路径）。在 RunScoringLoop 启动时调用一次。
// §M11（2026-09-22 修复批）：启动时先恢复上次停机落盘的排队买单（SignalID 去重后重新入队），
// 修复旧行为——buyCh 是 64 槽纯内存队列，停机时已排队未消费的买单随重启蒸发、当日无人重放。
// English: A+B — starts the async order worker pool; §M11 restores the shutdown-drained queue first.
func (e *Engine) StartBuyDispatcher(n int) {
	if n <= 0 {
		n = 4
	}
	e.mu.Lock()
	if e.buyCh != nil {
		e.mu.Unlock()
		return // 已启动，避免重复
	}
	e.buyCh = make(chan buyTask, 64)
	e.buyStop = make(chan struct{})
	ch := e.buyCh
	e.mu.Unlock()
	// §M11 恢复停机排队买单（worker 启动前先入队，恢复单与原单同走幂等键链路）。
	e.restoreBuyQueue(ch)
	for i := 0; i < n; i++ {
		e.buyWg.Add(1)
		go func() {
			defer e.buyWg.Done()
			// 加锁读取，避免与 StopBuyDispatcher 写 e.buyStop=nil 形成数据竞争
			// English: read under lock to avoid a data race with StopBuyDispatcher.
			// §M11：buyCh 同样锁内快照取局部引用——Stop 现在会在排空后把字段置 nil 支持
			// 原地重启，worker 事件循环不得再直读 e.buyCh。
			e.mu.RLock()
			stop := e.buyStop
			queue := e.buyCh
			e.mu.RUnlock()
			if stop == nil || queue == nil {
				return
			}
			// 事件循环：收到投递则下单，收到停止信号则退出 worker。
			for {
				select {
				case t := <-queue:
					metrics.SetGauge("buy_queue_depth", int64(len(queue))) // §AUDIT-PM 出队侧同步深度
					e.placeOrderNow(t.req, t.sig)
				case <-stop:
					return
				}
			}
		}()
	}
}

// StopBuyDispatcher 停止 worker 池（进程退出时）。
// §M11（2026-09-22 修复批）：worker 退出（buyWg.Wait 已等其完成在途 PlaceOrder）后把队列中
// 已排队未消费的买单排空原子落盘（<acctDir>/buy_queue_pending.json），下次 StartBuyDispatcher
// 恢复入队。语义边界：已出队、PlaceOrder 进行中的在途单不在落盘范围——worker 会先完成该单
// 再退出，且 orders 表 signal_id 唯一键幂等兜底，恢复链路不会造成重复下单。
// English: stops the worker pool and (M11) drains the still-queued tasks to an atomic JSON file
// for restore on next start; in-flight dequeued tasks complete before the drain, and the
// orders-table signal_id key dedupes any overlap.
func (e *Engine) StopBuyDispatcher() {
	// 交换取走 stop 信号通道（置空避免二次停止时重复 close panic）。
	e.mu.Lock()
	stop := e.buyStop
	e.buyStop = nil
	e.mu.Unlock()
	if stop == nil {
		return
	}
	select {
	case <-stop:
	default:
		close(stop)
	}
	e.buyWg.Wait()
	// §M11 排空残余队列：在锁内取出队列引用并清空（此刻无 worker 消费；持锁排空把
	// 「autoPlace 快照旧 ch 后又塞单」的窗口压到最小——该残余竞态窗口与 §P2#23 快照
	// 语义同源，orders 表幂等键 + 当日信号重发兜底，不追求零窗口）。排空结果锁外落盘。
	e.mu.Lock()
	ch := e.buyCh
	var pending []buyTask
	if ch != nil {
	drain:
		for {
			select {
			case t := <-ch:
				pending = append(pending, t)
			default:
				break drain
			}
		}
		// 仅当字段仍指向本队列才置 nil（期间若有新 Start 装配了新队列，绝不误清）。
		if e.buyCh == ch {
			e.buyCh = nil
		}
	}
	e.mu.Unlock()
	e.persistBuyQueue(pending)
}
