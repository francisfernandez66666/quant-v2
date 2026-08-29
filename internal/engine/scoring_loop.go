// Package engine 近实时 8a/8b 持续打分循环：
// 以 5s 节奏对 持仓+自选+当日跟踪池+快照热点/新闻池 打分执行四战法评分 + 动量分（复用 8a/8b evalAll 口径），
// 分数写入聚合器与持久化；Pass 战法生成的信号按"状态翻转才发"去重后广播，
// 并即时并入消息中心（带 5s 实时行情），不等主循环 5min 轮次。
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategy"
	"quant-trading-v2/internal/strategy_engine"
	"quant-trading-v2/internal/trading"
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
			metrics.PanicRecovered() // §R4-9 panic 恢复计数进指标面
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

	// §同花顺（新）竞价窗口注入：9:15-9:26 把官方集合竞价快照写进看板——
	// 抢筹幅度/量比/未匹配量是当日开盘强弱最早的信号；同时记录显著异动（|涨幅|≥3% 或量比≥5）。
	if data.InAuctionWindow(time.Now()) {
		if auction := f.AuctionSnapshot(); len(auction) > 0 {
			items := make([]data.HithinkAuctionItem, 0, len(auction))
			for _, it := range auction {
				items = append(items, it)
				if it.AuctionPct >= 3 || it.AuctionVolumeRatio >= 5 {
					log.Printf("[engine] 竞价异动 %s(%s): 涨幅 %.2f%% 量比 %.1f 未匹配 %.0f",
						it.ThsCode, it.Name, it.AuctionPct, it.AuctionVolumeRatio, it.AuctionUnmatched)
				}
			}
			sort.Slice(items, func(i, j int) bool { return items[i].AuctionPct > items[j].AuctionPct })
			e.agg.SetAuction(items)
		}
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
	// §P1-2 按账号过滤持仓池与自选池：多账号共享 rpt/wlMgr 时只取本账号数据。
	pool := mergeCodes(
		e.rpt.HeldPositionCodesFor(e.userID),
		e.wlMgr.List(e.userID),
	)
	// §P1-B nil stockTracker 防御：注册表/测试可能未注入跟踪池，避免 panic。
	if e.stockTracker != nil {
		pool = mergeCodes(pool,
			trackedCodes(e.stockTracker.GetActiveByDirection(td, "利好")),
			trackedCodes(e.stockTracker.GetActiveByDirection(td, "利空")),
		)
	}
	// §W5-v3 准入放开：快照热点股全部纳入打分池，不再要求"已有 D1 记录"——
	// 该门槛原是防无新闻支撑误发信号的连坐闸；解耦后非 N 战法不消费 D1（N 形评分时无 D1 自然得 0 分
	// 不出信号），外层门槛失去存在意义且会延迟新热点进入近实时监控。
	if len(snapCodes) > 0 {
		pool = mergeCodes(pool, snapCodes)
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
	// §R4-6：行情传 5s 快照（quotes），缺失的持仓才逐票兜底单查。
	alertSigs := e.combatAgent.CheckPositionAlerts(e.rpt, e.marketAPI, quotes, scores, nil)
	// 逐股卖点评估（利空D1/破MA5·MA20/放量派发/动量衰竭）：对打分池全量个股独立评估，仅提醒。
	// 仅做多（shortEnabled=false）时非持仓个股不评估、不发减仓/清仓提醒（非持仓无从减仓，纯噪音）；
	// 做多+做空（shortEnabled=true）时评估全打分池，级别徽标按卖出方向显示为"做空"。
	// English: per-stock sell-point assessment (bearish D1 / MA5·MA20 break / volume distribution /
	// momentum exhaustion) over the whole scoring pool, reminder-only. In long-only mode non-held codes
	// are skipped; in long+short mode the whole pool is assessed with 做空 as the level badge.
	sellCodes := pool
	if !e.ShortEnabled() {
		sellCodes = e.rpt.HeldPositionCodesFor(e.userID)
	}
	sellSigs := e.combatAgent.AssessSellSide(sellCodes, md, d1Scores, scores, e.ShortEnabled())
	if len(exitSigs) > 0 || len(alertSigs) > 0 || len(sellSigs) > 0 {
		all := make([]combat_agent.Signal, 0, len(exitSigs)+len(alertSigs)+len(sellSigs))
		all = append(all, exitSigs...)
		all = append(all, alertSigs...)
		all = append(all, sellSigs...)
		e.syncMessages(nil, nil, all, nil, quotes)
	}

	// 实盘持仓处理分析（AUTO_TRADING_PLAN M1）：qmt.enabled 时对真实持仓（real_positions）
	// 生成 加仓/减仓/止盈/止损/格局 建议，经 SSE 推前端持仓页实盘 tab。
	// 仅交易时段运行；无持仓/未启用时零开销。不触碰纸面账本。
	// English: live position advice (AUTO_TRADING_PLAN M1) — when qmt.enabled, generates 加仓/减仓/止盈/
	// 止损/格局 advice for the real book (real_positions) and pushes it to the frontend live tab via SSE.
	// Trading-hours only; no cost when disabled or no holdings. Never touches the paper book.
	e.pushRealAdvice(md, scores, d1Scores, emotionPhase, quotes)

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
	if data.BeforeOpenTrade(e.nowTime()) || data.IsPreAfternoon(e.nowTime()) {
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
		e.fastScoreStore.Save(data.TradingDayDate(time.Now()), scores)
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

// pushRealAdvice 实盘持仓处理分析（AUTO_TRADING_PLAN M1）：qmt.enabled 时对真实持仓生成建议。
// 每 5s 读 real_positions → trading.Advise（复用卖出侧 + 加仓/格局规则）→ SSE 推前端实盘 tab。
// 熔断健康探测也在此节流执行（网关失联 → 暂停下单并告警）。仅交易时段运行（盘后省内存）。
// English: live position advice (AUTO_TRADING_PLAN M1) — when qmt.enabled, reads real_positions each 5s,
// runs trading.Advise (sell-side reuse + add/hold rules), and pushes the advice to the frontend live tab
// via SSE. Circuit-breaker health probing is also throttled here (gateway loss pauses orders and alerts).
// Trading-hours only (after-hours skips to save memory).
func (e *Engine) pushRealAdvice(md map[string]*strategy_engine.StockMarketData, scores map[string]combat_agent.StockScores, d1Scores map[string]combat_agent.D1Score, emotionPhase string, quotes map[string]*data.StockInfo) {
	e.mu.RLock()
	ctrl := e.qmtCtrl
	realStore := e.realStore
	agent := e.combatAgent
	marketAPI := e.marketAPI
	sse := e.sse
	e.mu.RUnlock()
	if ctrl == nil || realStore == nil || agent == nil {
		return
	}
	if !data.IsActiveSession(time.Now()) || !ctrl.Enabled() {
		return
	}

	// 熔断健康探测（节流：miss_heartbeat_sec/2）
	ctrl.HealthCheck()
	// §W6-a 周期对账（默认 5min 节流）：首尔侧主动拉网关持仓落库，终结"双向对账均不存在"的盲区
	ctrl.MaybeReconcile(5 * time.Minute)
	// §R4-1 撤单闭环（30s 节流 + 每日收盘清单）：未成交超时自动撤 / 占位行降级 / 收盘清单，
	// 根除"已报委托悬置整天虚耗买入纪律预算"的悬置态（内部自节流，5s 循环调用无额外开销）
	if res := ctrl.SweepOrders(time.Now()); res != nil {
		_ = res // 摘要日志已在 SweepOrders 内按需打印
	}

	// §GAP2-W2 实盘建议定向化（I-4）：只读主账号（=QMT 归属账号，实盘仅 admin 开启）的持仓，
	// SSE 只推给该账号——admin 真实持仓代码/数量/买卖建议不再每 5s 广播给所有在线用户。
	sendTo := e.primaryMember()
	positions, err := realStore.RealPositionsForUser(sendTo)
	if err != nil || len(positions) == 0 {
		// §GAP2-W1 平仓归零（资损级修复）：空仓时把 M8 组合回撤的进程内峰值基线清零。
		// 旧实现提前 return 且从不重置 m8PeakTotal——注释宣称"平仓后基线归零"但代码没做，
		// 后果：峰值 16 万 → 全平 → 再建仓 10 万时回撤判定 (10-16)/16=-37.5% 直接命中阈值，
		// 新仓位被立刻整体强平；用户再入场再触发，死亡螺旋直到进程重启。归零后新基线从当前市值起算。
		// English: §GAP2-W1 reset-on-flat: when the book is empty, clear the in-process M8 drawdown
		// peak. The old early-return never reset m8PeakTotal despite the comment claiming it did —
		// after a full liquidation, any re-entry was instantly "in drawdown" vs the stale peak and got
		// force-liquidated again, looping until restart.
		if err == nil && len(positions) == 0 {
			e.mu.Lock()
			e.m8PeakTotal = 0
			e.mu.Unlock()
			e.saveM8Peak(0) // §R4-7 空仓归零同步落盘
		}
		return
	}

	// 组装分析入参：复用本轮打分池的行情/日K/分数（不额外拉取）
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

	advices := trading.Advise(trading.AdviceInput{
		Agent:        agent,
		MarketAPI:    marketAPI,
		Positions:    positions,
		Quotes:       exitQuotes,
		DayKLines:    exitDayK,
		Scores:       scores,
		MD:           md,
		D1Scores:     d1Scores,
		ShortEnabled: e.ShortEnabled(),
		EmotionPhase: emotionPhase,
		Cfg:          ctrl.Config(),
	})

	// §GAP1.2 M8 组合回撤熔断（risk.M8Check 口径接线）：实盘组合市值自峰值回撤超阈值 → 全部自动卖出。
	// 此前 M8Check 是死代码；现接入实盘链路（峰值随进程内存续，平仓后基线归零）。
	e.checkM8RealDrawdown(ctrl, realStore, positions, exitQuotes)

	// §GAP1.1 实盘卖出自动化：mode=auto 且 qmt.auto_sell 开启时，止损级建议自动全仓卖出。
	// signal_id 按"码+类+日"幂等——orders 表唯一键天然防重，跨重启/跨轮次不会二次下单。
	if len(advices) > 0 {
		e.autoExecuteRealSells(sendTo, ctrl, realStore, advices)
	}

	if len(advices) == 0 || sse == nil || sendTo == "" {
		return
	}
	// §GAP2-W2 定向推送替代全员广播
	sse.BroadcastTo(sendTo, map[string]interface{}{
		"type":    "real_advice",
		"advices": advices,
		"tripped": ctrl.Tripped(),
		"time":    time.Now().Format("15:04:05"),
	})
}

// realSellSignalID 实盘自动卖出的幂等键：sell:<纯代码>:<类别>:<交易日>。
// orders 表 signal_id 唯一键天然防重：同码同类当日只下一单（跨重启/跨轮次安全）。
func realSellSignalID(tsCode, class string) string {
	return fmt.Sprintf("sell:%s:%s:%s", pureTsCode(tsCode), class, data.TradingDayDate(time.Now()))
}

// pureTsCode 剥离交易所后缀。
func pureTsCode(tsCode string) string {
	for _, suf := range []string{".SH", ".SZ", ".BJ"} {
		if i := strings.LastIndex(tsCode, suf); i > 0 {
			return tsCode[:i]
		}
	}
	return tsCode
}

// sellRealPosition 通过控制器对单一实盘持仓下卖出单。行情缺失时跳过（宁可不卖不以错价报单）。
// §修复 R6（2026-08-29）：qty 显式传入（补卖时传剩余量），signalID 由调用方按"剩余量桶"生成，
// 确保首笔仅部成时仍能对剩余仓位开新单，而非被日级幂等键永久拦截。
func (e *Engine) sellRealPosition(ctrl *trading.Controller, p store.RealPosition, qty int, signalID string, price float64, class, reason string) error {
	cfg := ctrl.Config()
	if qty <= 0 || price <= 0 {
		return nil
	}
	res, err := ctrl.PlaceOrder(trading.OrderRequest{
		SignalID:  signalID,
		Code:      p.TsCode,
		Name:      p.Name,
		Strategy:  p.Strategy,
		Side:      trading.SideSell,
		PriceType: cfg.PriceType,
		Price:     price,
		Qty:       qty,
		Amount:    price * float64(qty),
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	if err != nil {
		log.Printf("[qmt] 自动卖出 %s(%s) 失败: %v", p.TsCode, p.Name, err)
		return err
	}
	if res != nil && !res.OK && strings.Contains(res.Err, "duplicate") {
		// §GAP2-W1 语义更新：duplicate 现在只可能意味着"当日同类卖单已真实报出"
		// （发送失败的单会经 MarkRealOrderSendFailed→ResetFailedRealOrder 放行重试，
		// 不再以 duplicate 形态出现），因此幂等命中=目标已达成，静默返回是正确行为。
		return nil // 当日已下过同类卖单（幂等命中），静默
	}
	log.Printf("[qmt] 自动卖出 %s(%s) %d股 @%.2f 类别=%s 原因=%s → %+v",
		p.TsCode, p.Name, qty, price, class, reason, res)
	return nil
}

// autoExecuteRealSells §GAP1.1 止损级建议自动全仓卖出（qmt.auto_sell + mode=auto）。
// 仅 Action=止损 触发（止盈/减仓保持提醒半自动，由前端确认执行）；行情缺失跳过。
// §R3-1 P0-B 按账号过滤：此前读全表 RealPositions()——多账号部署下 A 账号的止损建议可能
// 匹配到 byCode 映射里 B 账号的同名持仓并真实卖出（资损级）。与建议生成路径的
// §GAP2-W2 收敛口径对齐，统一走 RealPositionsForUser(userID)。
// English: R3-1 P0-B — filter positions by account: the old full-table RealPositions() could pair
// account A's stop-loss advice with account B's same-code position and really sell it.
func (e *Engine) autoExecuteRealSells(userID string, ctrl *trading.Controller, realStore *store.DB, advices []trading.PositionAdvice) {
	if realStore == nil || !ctrl.Enabled() || ctrl.Mode() != "auto" || !ctrl.Config().AutoSell {
		return
	}
	positions, err := realStore.RealPositionsForUser(userID)
	if err != nil || len(positions) == 0 {
		return
	}
	byCode := make(map[string]store.RealPosition, len(positions))
	for _, p := range positions {
		byCode[pureTsCode(p.TsCode)] = p
	}
	for _, a := range advices {
		if a.Action != "止损" {
			continue
		}
		p, ok := byCode[a.Code]
		if !ok || a.RefPrice <= 0 {
			continue
		}
		// §修复 R6：日级幂等键 sell:<code>:止损:<交易日> 统计已成交数量，仅对"剩余未成交"部分补卖；
		// 信号键追加 :r<剩余量> 桶——剩余量变化才开新单，避免部成后死循环重复下单，
		// 也保证同日同剩余量不重复刷单（broker 仍在处理该笔时）。
		base := realSellSignalID(p.TsCode, "止损")
		filled := realStore.SumFilledQty(userID, base)
		remaining := p.Qty - filled
		if remaining <= 0 {
			continue
		}
		sid := fmt.Sprintf("%s:r%d", base, remaining)
		_ = e.sellRealPosition(ctrl, p, remaining, sid, a.RefPrice, "止损", a.Reason)
	}
}

// checkM8RealDrawdown §GAP1.2 M8 组合回撤兜底（risk.M8Check 口径接线到实盘）：
// 每轮用实时快照计算实盘组合总市值（缺行情的持仓按成本价兜底计入），维护进程内峰值；
// 回撤超 rules.risk_ctrl.m8_portfolio_drawdown_pct 且 m8_enabled 时全部持仓自动卖出
// （类别 m8，按日幂等）。§GAP2-W1：空仓入口真正把峰值归零重新累计（旧实现注释宣称
// 归零实际提前 return 从不重置——清仓后再入场会被陈旧峰值立即判定回撤并连环强平）。
// §R4-7：峰值持久化到 accounts/<uid>/qmt_m8.json——旧实现峰值仅进程内存，盘中重启后
// 基线从当前市值重计，回撤保护存在窗口缺口（重启前的高点被遗忘）。
func (e *Engine) checkM8RealDrawdown(ctrl *trading.Controller, realStore *store.DB, positions []store.RealPosition, quotes map[string]*data.StockInfo) {
	if realStore == nil || len(positions) == 0 {
		// §GAP2-W1 双保险归零：本函数被独立调用（如未来其他链路）时空仓同样清零基线
		// （§R4-7：连同持久化文件一并归零）。
		if len(positions) == 0 {
			e.mu.Lock()
			e.m8PeakTotal = 0
			e.mu.Unlock()
			e.saveM8Peak(0)
		}
		return
	}
	e.mu.RLock()
	cfgMgr := e.cfgMgr
	userID := e.userID
	peak := e.m8PeakTotal
	e.mu.RUnlock()
	if cfgMgr == nil {
		return
	}
	// §R4-7 进程内无峰值（刚重启）时从持久化文件恢复，回撤保护跨重启连续
	if peak <= 0 {
		peak = e.loadM8Peak()
	}
	rc := cfgMgr.GetRulesFor(userID).RiskCtrl
	// §修复 R7（2026-08-29）：阈值接受正数（更直观，"回撤 10% 触发"直接写 10）或负数（旧口径）；
	// 一律归一为负值口径参与比较，避免误填 0/正值导致 M8 静默失效且无告警。
	// 0 或未设置视为关闭。
	thr := rc.M8PortfolioDrawdownPct
	if thr > 0 {
		thr = -thr
	}
	if !rc.M8Enabled || thr >= 0 {
		return // 未启用或阈值未配置（0）
	}
	total := 0.0
	for _, p := range positions {
		price := p.CostPrice
		if q := quotes[pureTsCode(p.TsCode)]; q != nil && q.Price > 0 {
			price = q.Price // 缺行情的持仓按成本价兜底，保证估值连续
		}
		total += price * float64(p.Qty)
	}
	if total <= 0 {
		return
	}
	newPeak := peak
	if total > peak {
		newPeak = total
	}
	if newPeak != peak {
		e.mu.Lock()
		e.m8PeakTotal = newPeak
		e.mu.Unlock()
		e.saveM8Peak(newPeak)
		peak = newPeak
	}
	// 组合回撤触发判定（drawdown 为负值，与归一后的 thr 同口径比较）
	drawdown := (total - peak) / peak * 100
	if drawdown > thr {
		return
	}
	log.Printf("[qmt] M8 兜底触发: 组合市值 %.0f 自峰值 %.0f 回撤 %.1f%% ≤ 阈值 %.1f%% —— 全部自动卖出",
		total, peak, drawdown, thr)
	for _, p := range positions {
		price := p.CostPrice
		if q := quotes[pureTsCode(p.TsCode)]; q != nil && q.Price > 0 {
			price = q.Price
		}
		// §修复 R6：M8 清仓同样走剩余量补卖逻辑（按 code:day 桶统计已成交，剩余量>0 才发单）。
		base := realSellSignalID(p.TsCode, "m8")
		filled := realStore.SumFilledQty(userID, base)
		remaining := p.Qty - filled
		if remaining > 0 {
			sid := fmt.Sprintf("%s:r%d", base, remaining)
			_ = e.sellRealPosition(ctrl, p, remaining, sid, price, "m8", fmt.Sprintf("M8组合回撤%.1f%%兜底清仓", drawdown))
		}
	}
}

// qmtM8State §R4-7 M8 峰值持久化结构（accounts/<uid>/qmt_m8.json）。
type qmtM8State struct {
	PeakTotal float64 `json:"peak_total"` // 组合市值峰值（M8 回撤基线）
}

// m8StatePath 返回 M8 峰值持久化路径（accountsRoot/<uid>/qmt_m8.json）；
// accountsRoot/userID 缺失（旧装配/e2e）返回空串=不持久化，行为与旧版一致。
func (e *Engine) m8StatePath() string {
	e.mu.RLock()
	root, uid := e.accountsRoot, e.userID
	e.mu.RUnlock()
	if root == "" || uid == "" {
		return ""
	}
	return filepath.Join(root, uid, "qmt_m8.json")
}

// loadM8Peak 从持久化文件恢复峰值；文件缺失/损坏/路径不可用返回 0。
func (e *Engine) loadM8Peak() float64 {
	path := e.m8StatePath()
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var st qmtM8State
	if json.Unmarshal(raw, &st) != nil || st.PeakTotal < 0 {
		return 0
	}
	return st.PeakTotal
}

// saveM8Peak 原子落盘峰值（temp+rename；失败仅影响下次重启的基线连续性，不阻断交易主流程）。
func (e *Engine) saveM8Peak(peak float64) {
	path := e.m8StatePath()
	if path == "" {
		return
	}
	raw, err := json.MarshalIndent(qmtM8State{PeakTotal: peak}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
