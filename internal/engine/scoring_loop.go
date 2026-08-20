// Package engine 近实时 8a/8b 持续打分循环：
// 以 5s 节奏对 持仓+自选+当日跟踪池+快照热点/新闻池 打分执行四战法评分 + 动量分（复用 8a/8b evalAll 口径），
// 分数写入聚合器与持久化；Pass 战法生成的信号按"状态翻转才发"去重后广播，
// 并即时并入消息中心（带 5s 实时行情），不等主循环 5min 轮次。
package engine

import (
	"context"
	"log"
	"sort"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
)

// RunScoringLoop 启动近实时打分循环，直到 ctx 取消。
// 需先 SetFetcher 提供 5s 快照（新浪→同花顺→东财）。
func (e *Engine) RunScoringLoop(ctx context.Context) {
	e.mu.RLock()
	f := e.fetcher
	e.mu.RUnlock()
	if f == nil {
		log.Printf("[engine] 近实时打分循环未启动: 未设置 Fetcher")
		return
	}
	log.Printf("[engine] 近实时 8a/8b 打分循环启动: 5s 节奏")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[engine] 近实时打分循环停止")
			return
		case <-ticker.C:
			e.scoreCycle(ctx)
		}
	}
}

// RunScoringLoopOnce 执行一轮近实时打分（供多账号注册表统一 5s 调度调用）。
// English: runs one near-realtime scoring cycle (called by the multi-account registry's shared
// 5s scheduler).
func (e *Engine) RunScoringLoopOnce(ctx context.Context) {
	e.scoreCycle(ctx)
}

// scoreCycle 执行一轮近实时打分：收拢持仓+自选 → 构建行情 → 8a/8b 打分+信号 → 状态翻转去重 → 更新看板/落盘。
func (e *Engine) scoreCycle(ctx context.Context) {
	// 防御：单轮 panic 不拖垮整个循环，记录日志后继续下一轮
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[engine] 打分循环 panic: %v", r)
		}
	}()

	// 交易时段门控（盘后/休市跳过，避免无效拉取）
	// English: session gate — skip after-market/holiday to avoid pointless fetching.
	if !data.IsActiveSession(time.Now()) {
		return
	}

	// 同步本账号配置（做多/做空开关 + 战法参数），保证账号内各设备一致
	// English: sync this account's config (long/short toggles + strategy params) for cross-device consistency.
	e.syncAccountConfig()

	e.mu.RLock()
	f := e.fetcher
	emotionPhase := e.lastEmotionPhase // 复用主循环算出的情绪阶段，不重复调涨停池接口
	d1Scores := e.lastD1Scores         // 复用主循环最近一轮 D1 评分，不每 5s 调 LLM
	e.mu.RUnlock()
	if f == nil {
		return
	}

	// 优先取 fetcher 的 5s 实时快照作为行情来源与打分池候选（缺失的由 BuildScoringData 内部降级补齐）
	var quotes map[string]*data.StockInfo
	var snapCodes []string
	if snap := f.Snapshot(); snap != nil {
		quotes = snap.Stocks
		for code := range snap.Stocks {
			snapCodes = append(snapCodes, code)
		}
	}

	// 打分池 = 持仓 + 自选 + 当日跟踪池(新闻个股) + 快照热点/新闻池个股（去重）。
	// 快照里的热点/新闻池个股仅纳入主循环已评过分（存在于 lastD1Scores）的：它们已有事件/D1 上下文，
	// 避免对无新闻支撑的热点股误发战法信号；持仓/自选/跟踪池始终纳入。
	td := data.TradingDayDate(time.Now())
	pool := mergeCodes(
		e.rpt.HeldPositionCodes(),
		e.wlMgr.All(),
		trackedCodes(e.stockTracker.GetActiveByDirection(td, "利好")),
		trackedCodes(e.stockTracker.GetActiveByDirection(td, "利空")),
	)
	if len(snapCodes) > 0 {
		var ctxCodes []string
		for _, code := range snapCodes {
			if _, ok := d1Scores[code]; ok {
				ctxCodes = append(ctxCodes, code)
			}
		}
		pool = mergeCodes(pool, ctxCodes)
	}
	if len(pool) == 0 {
		return
	}

	md := e.strategy.BuildScoringData(ctx, pool, quotes)
	scores, sigs := e.combatAgent.ScorePool(pool, md, d1Scores, emotionPhase)

	// B2 失效墓碑：以最新行情校验当日固化买入信号，跌破触发价（买入依据破坏）即打墓碑：
	// 移出固化存储 + 删除消息中心对应条目，防"已失效信号持续展示/再提醒"。
	// English: B2 invalidation tombstone — check today's pinned buy signals against fresh quotes; when
	// the price falls below the trigger (buy premise broken), tombstone it: remove from the store and
	// delete its message-center entry, so a dead signal stops displaying and can't re-alert.
	e.invalidateBrokenSignals(md, d1Scores)

	// 近实时退出通道：复用本轮打分池行情/日K，跑战法退出引擎（移动止盈/硬止损/破MA5/尾盘强平/超期），
	// 让止损/移动止盈提醒从主循环 5 分钟粒度压缩到 ~5s。仅提醒、不自动执行。
	// English: near-realtime exit channel — reuses this round's pool quotes/bars to run the exit engines,
	// cutting stop-loss/trailing-stop alert latency from the 5-minute main loop down to ~5s (reminder-only).
	exitQuotes := make(map[string]*data.StockInfo, len(md))
	exitDayK := make(map[string][]data.KLine, len(md))
	for code, smd := range md {
		if smd == nil {
			continue
		}
		if smd.Quote != nil && smd.Quote.Price > 0 {
			exitQuotes[code] = smd.Quote
		}
		if len(smd.KLines) > 0 {
			exitDayK[code] = smd.KLines
		}
	}
	exitSigs := e.combatAgent.CheckPositionsExits(e.rpt, exitQuotes, exitDayK, time.Now())
	// 通用止盈/止损/当日跌幅提醒并入近实时通道（止损提醒延迟 ≤5s；消息中心按稳定键去重不重复）
	// English: the generic take-profit / stop-loss / daily-drop alerts also run near-realtime; the message
	// store dedups by stable key so repeated ticks just refresh the same message.
	alertSigs := e.combatAgent.CheckPositionAlerts(e.rpt, e.marketAPI, scores, nil)
	// 逐股卖点评估（利空D1/破MA5·MA20/放量派发/动量衰竭）：对打分池全量个股独立评估，仅提醒。
	// 仅做多（shortEnabled=false）时非持仓个股不评估、不发减仓/清仓提醒（非持仓无从减仓，纯噪音）；
	// 做多+做空（shortEnabled=true）时评估全打分池，级别徽标按卖出方向显示为"做空"。
	// English: per-stock sell-point assessment (bearish D1 / MA5·MA20 break / volume distribution /
	// momentum exhaustion) over the whole scoring pool, reminder-only. In long-only mode non-held codes
	// are skipped; in long+short mode the whole pool is assessed with 做空 as the level badge.
	sellCodes := pool
	if !e.ShortEnabled() {
		sellCodes = e.rpt.HeldPositionCodes()
	}
	sellSigs := e.combatAgent.AssessSellSide(sellCodes, md, d1Scores, scores, e.ShortEnabled())
	if len(exitSigs) > 0 || len(alertSigs) > 0 || len(sellSigs) > 0 {
		all := make([]combat_agent.Signal, 0, len(exitSigs)+len(alertSigs)+len(sellSigs))
		all = append(all, exitSigs...)
		all = append(all, alertSigs...)
		all = append(all, sellSigs...)
		e.syncMessages(nil, nil, all, nil, quotes)
	}

	// 开市(9:30)前及午休(11:30-13:00)只更新评分数字，不发布任何战法信号：
	// 盘前无实盘成交量，双响炮/龙头等易基于存量历史数据误报（如整池双响炮全 70、9:11 龙头）；
	// 午休行情冻结（新浪/东财快照停在 11:30），当日bar时间戳又取 time.Now()，导致双响炮等
	// 把午休误当成实时新bar产生买卖信号，故与非交易时段同等压制。
	// prevPass 不加/清空处理由 filterTransitionSignals 自然完成：9:30/13:00 后首个 Pass 仍会翻转发一次。
	// English: before open (9:30) and during the lunch break (11:30-13:00) we only refresh scores, never
	// emit strategy signals: pre-open has no real volume (double-bump/dragon would false-fire on stale bars),
	// and at lunch quotes are frozen at 11:30 while the today-bar timestamp is time.Now(), so strategies like
	// double-bump treat lunch as a live new bar. Suppressing signals in both windows is therefore consistent.
	// The prevPass reset is handled naturally by filterTransitionSignals: the first Pass after 9:30/13:00 re-flips.
	if data.BeforeOpenTrade(time.Now()) || data.IsPreAfternoon(time.Now()) {
		sigs = nil
	}

	// N 形候选诊断：收口本轮 N 候选的 D1/总分/级别/拦截原因，一眼定位"为何无 N 信号"
	if nd := e.combatAgent.DrainNDiag(); len(nd) > 0 {
		e.logNShapeDiag(emotionPhase, nd)
	}

	// 状态翻转去重：仅 非Pass→Pass 翻转的信号广播；持续 Pass 不重发；翻回后再翻上会再发。
	e.mu.RLock()
	prev := e.prevPass
	e.mu.RUnlock()
	emit, next := filterTransitionSignals(sigs, prev)
	e.mu.Lock()
	e.prevPass = next
	e.mu.Unlock()

	// 即时并入消息中心：本轮翻转信号分做多/做空写入，带 5s 实时快照行情（现价+涨跌幅）。
	// 与主循环 syncMessages 共用 code@交易信号@strategy 稳定键：近实时先落盘、主循环后续同键刷新，
	// 不产生重复条目，且信号随各自翻转时刻分批出现（不再一轮一坨同类型）。
	// English: push flipped signals into the message center right away (with live snapshot quotes). It shares the
	// stable code@交易信号@strategy keys with the main-loop syncMessages, so near-realtime writes land first and the
	// main loop just refreshes the same keys — no duplicates, and signals trickle in per flip instead of one per round.
	if len(emit) > 0 {
		var bullE, bearE []combat_agent.Signal
		for _, sig := range emit {
			if sig.Direction == "做空" {
				bearE = append(bearE, sig)
			} else {
				bullE = append(bullE, sig)
			}
		}
		e.syncMessages(bullE, bearE, nil, nil, quotes)

		// 模拟盘撮合：本轮翻转的做多 buy 信号按实时快照价自动成交（独立于真实持仓）。
		// 信号价作为辅助参照记录，量化「信号发出→成交」的延迟与滑点对收益的影响。
		// English: paper fill — this round's flipped long buy signals auto-fill at the live snapshot
		// price (isolated from the real book). The signal price is recorded as a reference to quantify
		// the signal-to-fill latency and slippage impact on returns.
		e.paperSignals(emit, quotes)
	}

	// 模拟盘估值与日净值：每轮用实时快照价刷新持仓市值，并记录当日净值点。
	// English: paper mark-to-market + daily equity point each round, using the live snapshot.
	e.paperMark(quotes)

	// 有分数才更新看板并落盘（保持与 8a/8b 主循环同口径）
	if len(scores) > 0 {
		// 为做多/做空 Pass 信号补全真实 D1 事件信息（评分/负面拦截/LLM理由 + 事件标题），随信号固化展示。
		// 事件标题来自看板聚合器里主循环刚归因的新闻事件（近实时循环不重新归因）。
		// English: backfill real D1 event info (score/blocked/LLM reason + event title) onto long/short Pass
		// signals so it rides along when pinned/displayed. The event title comes from the dashboard aggregator's
		// news events (attributed by the main loop — this loop does not re-attribute).
		var fastBriefs map[string][]combat_agent.NewsBrief
		if cur := e.agg.Current(); cur != nil && len(cur.NewsEvents) > 0 {
			fastBriefs = newsBriefsByCode(cur.NewsEvents)
		}
		if e.newsAgent != nil {
			if all := e.newsAgent.AllEvents(); len(all) > 0 {
				// 当日全量已打标事件更全：优先用它覆盖简报（个股级事件本轮可能没过阈值，但信号标题仍应有事件）
				// English: today's full attributed store is richer — prefer it so signal D1 titles still resolve
				// even when an individual-stock event didn't clear this round's threshold.
				fastBriefs = newsBriefsByCode(all)
			}
		}
		enrichSignalsWithD1(sigs, d1Scores, fastBriefs)
		// 固化当日信号：本轮 Pass 信号按 code@strategy 覆盖写盘（跨重启恢复，信号固化一天）
		// English: pin today's signals — this round's Passed signals overwrite the store per code@strategy
		// (restored across restarts, pinned for the day).
		if e.signalStore != nil {
			e.signalStore.Upsert(sigs)
		}
		// 展示信号 = 当日固化信号 + 本轮新翻转信号（固化信号未被新一轮评分替换前持续显示）
		// English: displayed signals = pinned day signals + this round's newly-flipped signals.
		e.agg.UpdateFast(scores, mergeSignals(emit, e.signalStore.List()), e.rpt)
		e.scoreStore.Save(data.TradingDayDate(time.Now()), scores)
		// 本轮全部 Pass 信号并入 5s 监控池：让展示接口优先走批量快照（而非每票 TTL 兜底），
		// 现价/涨跌幅真实且不加重上游逐票请求。
		e.syncSignalPool(sigs, nil, nil)
	}

	// 记录本轮新翻转出来的信号（供排查）
	if len(emit) > 0 {
		for _, sig := range emit {
			log.Printf("[engine] 近实时信号 %s(%s) %s action=%s dir=%s price=%.2f 分=%.0f/%s",
				sig.Code, sig.Name, sig.Strategy, sig.Action, sig.Direction, sig.Price, sig.Confidence*100, sig.Reason)
		}
	}

	// SSE 通知前端分数已刷新
	if e.sse != nil {
		e.sse.Broadcast(map[string]interface{}{
			"type":    "score",
			"count":   len(scores),
			"signals": len(emit),
			"emotion": emotionPhase,
			"time":    time.Now().Format("15:04:05"),
		})
	}
}

// logNShapeDiag 打印本轮 N 形候选诊断概要 + 最可能出信号的若干明细。
// 排序：Pass（含一突/二突标记）在前、其余按总分降序，最多展示 8 条，避免刷屏。
func (e *Engine) logNShapeDiag(emotionPhase string, diags []combat_agent.NDiag) {
	pass, fail, d1Zero, totalLow := 0, 0, 0, 0
	for _, d := range diags {
		if d.Pass {
			pass++
		} else {
			fail++
		}
		if d.D1 <= 0 {
			d1Zero++
		} else if !d.Pass {
			totalLow++
		}
	}
	log.Printf("[engine] N形诊断 emotion=%s 候选=%d pass=%d fail=%d d1=0拦截=%d 总分不足=%d",
		emotionPhase, len(diags), pass, fail, d1Zero, totalLow)
	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Pass != diags[j].Pass {
			return diags[i].Pass
		}
		return diags[i].Total > diags[j].Total
	})
	for i, d := range diags {
		if i >= 8 {
			break
		}
		log.Printf("[engine] N形候选 %s(%s) d1=%.0f total=%.0f level=%s tag=%s pass=%v | %s",
			d.Code, d.Name, d.D1, d.Total, d.Level, d.Tag, d.Pass, d.Reason)
	}
}

// invalidateBrokenSignals 校验当日固化买入信号：现价跌破信号触发价（sig.Price）视为买入依据破坏，
// 对 code@strategy 打失效墓碑——移出固化存储（当日不再固化/展示）+ 删除消息中心对应条目 + 日志。
// 仅处理做多信号；行情缺失/价格无效时跳过（等下一轮有数据再判）。
// English: validates today's pinned buy signals — when the live price falls below the signal trigger
// (sig.Price) the buy premise is broken, so code@strategy gets an invalidation tombstone: it's removed
// from the pinned store (no longer pinned/shown today), its message-center entry is deleted, and it's logged.
// Only long signals are processed; missing/invalid quotes are skipped and re-checked next round.
// invalidateBrokenSignals 校验当日固化的做多买入信号，对失效信号打墓碑：
//  1. 现价跌破触发价（sig.Price）→ 买入依据破坏；
//  2. N 形(n_shape)信号当前 D1=0（无实质事件）→ 不再具备"有 D1 事件"+一突 的买入前提。
//
// 命中即移出固化存储（当日不再固化/展示）+ 删除消息中心对应条目 + 日志。
// 仅处理做多信号；行情缺失/价格无效时跳过（等下一轮有数据再判）。
// English: validates today's pinned buy signals and tombstones stale ones — (1) live price below the
// trigger sig.Price; (2) an n_shape signal whose current D1=0 (no substantive event) no longer meets the
// "valid D1 + breakout" premise. Tombstoned signals are removed from the pinned store (not shown/pinned
// again today), their message-center entry is deleted, and it's logged. Long signals only; missing/invalid
// quotes are skipped and re-checked next round.
func (e *Engine) invalidateBrokenSignals(md map[string]*strategy_engine.StockMarketData, d1Scores map[string]combat_agent.D1Score) {
	if e.signalStore == nil || e.msgStore == nil {
		return
	}
	pinned := e.signalStore.List()
	if len(pinned) == 0 {
		return
	}
	for _, sig := range pinned {
		if sig.Direction != "做多" {
			continue
		}
		smd := md[sig.Code]
		// 现价跌破触发价 → 买入依据破坏
		if smd != nil && smd.Quote != nil && smd.Quote.Price > 0 && sig.Price > 0 && smd.Quote.Price < sig.Price {
			e.signalStore.Invalidate(sig.Code, sig.Strategy)
			e.msgStore.Delete(sig.Code + "@交易信号@" + sig.Strategy)
			log.Printf("[engine] 失效墓碑: %s(%s) %s 现价%.2f<触发价%.2f 买入依据破坏, 已移除信号",
				sig.Code, sig.Name, sig.Strategy, smd.Quote.Price, sig.Price)
			continue
		}
		// N 形信号当前无有效 D1（无实质事件被归 0）→ 不具备买入前提；
		// 但 LLM 失败待重试（RetryPending，Score=0 是占位而非真实归0）不触发墓碑，
		// 等重试队列下轮重新调 LLM 拿到真实分数再判。
		// English: n_shape pinned signal whose current D1 is 0 (no substantive event) — premise no longer
		// holds. But a RetryPending entry (Score=0 is a placeholder, not a real zero) must NOT tombstone:
		// wait for the retry queue to re-score it via LLM next round.
		if sig.Strategy == string(strategy.SignalNShape) {
			if d, ok := d1Scores[sig.Code]; ok && d.Blocked == false && d.Score <= 0 && !d.RetryPending {
				e.signalStore.Invalidate(sig.Code, sig.Strategy)
				e.msgStore.Delete(sig.Code + "@交易信号@" + sig.Strategy)
				log.Printf("[engine] 失效墓碑: %s(%s) n_shape 当前D1=0(无实质事件), 已移除信号", sig.Code, sig.Name)
			}
		}
	}
}

// countAction 统计信号列表中指定 Action 的条数（如只统计 "buy"，用于 SSE 通知计数）。
// English: counts how many signals carry the given Action (e.g. only "buy"), used for SSE notification counts.
func countAction(sigs []combat_agent.Signal, action string) int {
	n := 0
	for _, s := range sigs {
		if s.Action == action {
			n++
		}
	}
	return n
}

// filterTransitionSignals 状态翻转去重（纯函数）：返回本轮应广播的信号 + 下一轮去重状态。
// 仅当某股某战法从 非Pass → Pass 翻转时广播；持续 Pass 不重发；翻回后再翻上会再发。
func filterTransitionSignals(sigs []combat_agent.Signal, prev map[string]map[string]bool) (emit []combat_agent.Signal, next map[string]map[string]bool) {
	// 双缓冲区翻转检测：prev 记录上一轮 Pass 状态，next 为本轮新状态；
	// 仅当某股某战法从 非Pass → Pass 翻越状态边界时放入 emit，持续 Pass 不重发。
	next = make(map[string]map[string]bool, len(sigs))
	for _, sig := range sigs {
		was := prev[sig.Code][sig.Strategy]
		if !was {
			emit = append(emit, sig)
		}
		m := next[sig.Code]
		if m == nil {
			m = make(map[string]bool)
			next[sig.Code] = m
		}
		m[sig.Strategy] = true
	}
	return emit, next
}
