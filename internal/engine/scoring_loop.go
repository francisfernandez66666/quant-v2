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

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/risk"
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
	ticker := time.NewTicker(5 * time.Second) // 固定 5s 心跳：与快照源刷新节奏对齐
	defer ticker.Stop()
	for { // select 主循环：ctx 取消即退出，取不到 tick 就继续等，绝不 busy-loop
		select {
		case <-ctx.Done():
			log.Printf("[engine] 近实时打分循环停止")
			return
		case <-ticker.C: // 每 5s 执行一轮完整打分（单轮 panic 已在 scoreCycle 内 recover）
			e.scoreCycle(ctx)
		}
	}
}

// RunScoringLoopOnce 执行一轮近实时打分（供多账号注册表统一 5s 调度调用）。
// English: runs one near-realtime scoring cycle (called by the multi-account registry's shared
// 5s scheduler).
func (e *Engine) RunScoringLoopOnce(ctx context.Context) {
	e.StartBuyDispatcher(4) // §A+B 异步下单分发器（幂等启动）
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

	// §修复 P2#23：循环最前做交易日滚动清空（跨 00:00 后首个 5s 轮即清理昨日固化信号），
	// 放在会话门禁之前——休市/跨日也执行，保证次日开盘看板即干净。
	// English: P2#23 — run the trading-day rollover first each cycle (clears yesterday's pinned
	// signals right after midnight), before the session gate so the next open's dashboard is clean.
	e.RolloverDayStores()

	// §QUOTE_POOL_SPLIT: 持仓池 base 重建放在会话门禁之前——盘后/休市也保持持仓池最新
	// （自选∪实盘持仓∪全账号纸面持仓），次日开盘首个 cycle 即用最新 base 拉行情，无需等到盘中。
	// English: rebuild the base "held pool" before the session gate so holdings stay pinned even
	// after hours; the next open's first cycle fetches against the freshest base.
	e.mu.RLock()
	f := e.fetcher
	e.mu.RUnlock()
	if f != nil {
		e.syncMonitorBase()
	}

	// §WS-L 维5 阈值告警评估：每 30s 节流跑一轮（量规值越界持续满 For 才 fire，去重+恢复）。
	// English: WS-L 维5 — throttled alert evaluation every 30s (sustained-for state machine, dedup,
	// recovery).
	now := time.Now()
	if e.lastAlertEval.IsZero() || now.Sub(e.lastAlertEval) >= 30*time.Second {
		e.refreshStalenessGauges() // §UPDLINK：先把新鲜度量规喂满，再评估
		metrics.RunAlertEvaluation()
		e.lastAlertEval = now
	}

	// 交易时段门控（盘后/休市跳过，避免无效拉取）
	// English: session gate — skip after-market/holiday to avoid pointless fetching.
	if !data.IsActiveSession(time.Now()) {
		return
	}

	// 同步本账号配置（做多/做空开关 + 战法参数），保证账号内各设备一致
	// English: sync this account's config (long/short toggles + strategy params) for cross-device consistency.
	e.syncAccountConfig()

	// §QMT-PENDING 开关队列：交易时段内应用待生效的 QMT 实盘配置（enabled/mode/白名单等），
	// 并按最新配置重建 executor（Noop↔QMTClient）。休市时入队不消费，开盘首个 cycle 即生效。
	// English: inside the trading session, apply the queued QMT live config and rebuild the executor
	// (Noop↔QMTClient). Off-hours changes stay queued and take effect on the first session cycle.
	if c := e.qmtCtrl; c != nil {
		c.ApplyPendingConfig()
	}

	e.mu.RLock()
	emotionPhase := e.lastEmotionPhase // 复用主循环算出的情绪阶段，不重复调涨停池接口
	d1Scores := e.lastD1Scores         // 复用主循环最近一轮 D1 评分，不每 5s 调 LLM
	bearReasons := e.lastBearReasons   // FIX#13 复用主循环利空归因（实盘建议利空→自动清仓）
	e.mu.RUnlock()
	// §M9 轮首快照：本 5s 轮所有 sell_unified_mode 消费方（pushRealAdvice 统一卖出裁决 /
	// autoExecuteRealSells 来源闸 / paperSignals 证据闸 / judgePaperLedgers）共用此一个值。
	// 轮中配置翻转（shadow→on）不再产生「旧口径已关旧出口、新口径裁决未跑」的保护空窗轮。
	// English: round-start snapshot of sell_unified_mode threaded to all same-round consumers.
	roundSellMode := e.sellUnifiedModeEngine()
	if f == nil {
		return
	}

	// §同花顺（新）竞价窗口注入：9:15-9:26 把官方集合竞价快照写进看板——
	// 抢筹幅度/量比/未匹配量是当日开盘强弱最早的信号；同时记录显著异动（|涨幅|≥3% 或量比≥5）。
	// §P1.2 竞价信号：开关 Enable.AuctionSignal 开启时另算竞价强度分（[0,10]），存入引擎供
	// 开盘窗口（9:30-10:00）确认/观察，并把看板预排名改为按强度分排序。
	if data.InAuctionWindow(time.Now()) {
		if auction := f.AuctionSnapshot(); len(auction) > 0 {
			items := make([]data.HithinkAuctionItem, 0, len(auction))
			auctionOn := e.enhanceFlag(func(c config.EnhanceConfig) bool { return c.AuctionSignal })
			strengths := make(map[string]float64, len(auction))
			for _, it := range auction {
				items = append(items, it)
				if it.AuctionPct >= 3 || it.AuctionVolumeRatio >= 5 {
					log.Printf("[engine] 竞价异动 %s(%s): 涨幅 %.2f%% 量比 %.1f 未匹配 %.0f",
						it.ThsCode, it.Name, it.AuctionPct, it.AuctionVolumeRatio, it.AuctionUnmatched)
				}
				// §P1.2 竞价强度分：开关开启时按四维合成计算并缓存（大单抢筹日志）。
				// English: auction strength score when the auction-signal toggle is enabled.
				if auctionOn {
					s := data.AuctionStrengthBreakdown(it)
					strengths[normalizeCode(it.Ticker)] = s.Strength
					if s.Strength >= 7 {
						log.Printf("[engine] 竞价抢筹 %s(%s): 强度 %.1f 量比 %.1f 高开 %.1f%% 换手 %.2f%%",
							it.ThsCode, it.Name, s.Strength, s.VolumeRatio, s.OpenPct, s.TurnoverPct)
					}
				}
			}
			if auctionOn && len(strengths) > 0 {
				e.mu.Lock()
				e.auctionStrengths = strengths
				e.mu.Unlock()
			}
			// 看板预排名：开启竞价信号时按强度分降序（同强度再按高开幅度），否则保持原有高开排序。
			// English: board pre-ranking — by strength when the toggle is on, else by open pct.
			sort.Slice(items, func(i, j int) bool {
				if auctionOn {
					a := data.AuctionStrengthScore(items[i])
					b := data.AuctionStrengthScore(items[j])
					if a != b {
						return a > b
					}
				}
				return items[i].AuctionPct > items[j].AuctionPct
			})
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
	// §SELLPOINT-UNIFY 边界⑥：登记本轮打分时刻，卖出裁决通道的做多新鲜度基准。
	// §H5（2026-09-22 修复批）此全局时钟语义降级为「兜底基准」：新鲜度主判据改为打分自身
	// 产分时刻（StockScores.UpdatedAt，5s 轮与 5min 批量轮都自带），本字段只兜底无时刻的
	// 存量装配。5min 批量轮同样推进该时钟（见 engine.go 主循环第 12 步后），池空提前 return
	// 不再冻结全源新鲜度判定。
	e.mu.Lock()
	e.scoresAt = time.Now()
	e.mu.Unlock()

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
	e.pushRealAdvice(md, scores, d1Scores, emotionPhase, quotes, bearReasons, roundSellMode)

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
		// 近实时翻转信号同样固化进"信号日志"（signal_records），
		// 让 LLM Debug 的信号批次弹窗/日志能展示近实时信号，而非只进消息中心。
		// English: also persist near-realtime flipped signals into signal_records so the
		// LLM Debug signal-batch panel can show them, instead of only the message center.
		e.captureSignalRecords(len(scores), emit)

		e.syncMessages(bullE, bearE, nil, nil, quotes)
	}

	// 模拟盘撮合 + 实盘下单（§统一纪律·探针+扳机）：
	// 探针每轮喂入【全量活跃买入信号】（非仅翻转 emit）——翻转信号只出现一次，
	// 买入确认状态机无法据此判定"信号是否持续存在"；全量活跃集才能观察连续性，
	// 让插针假信号（一次翻转后即消失）在确认窗内被过滤，同时修复此前卖出侧纪律信号
	// 只在"本轮有翻转"时才被并入模拟盘的遗漏（exit 独立于 buy 翻转，应每轮送达）。
	// English: paper fill + live buy under the unified discipline — feed the FULL active buy set each
	// probe round (not just flips): a flip emits once, so persistence (the buy-confirm gate's input)
	// can only be observed from the full active set, filtering pin-bar one-shot signals. This also
	// fixes exit/discipline signals only reaching paper when a buy flip happened that round.
	if len(sigs) > 0 {
		var buys []combat_agent.Signal
		for _, sig := range sigs {
			if sig.Direction == "做多" && sig.Action == "buy" {
				buys = append(buys, sig)
			}
		}
		// 模拟盘撮合：全量活跃 buy 信号 + 卖出侧纪律信号（止损/止盈/移动止盈/当日跌幅）。
		// English: paper fill — full active buys + sell-side discipline signals (stop-loss/TP/trailing/daily-drop).
		// §QUOTE_POOL_SPLIT: 撮合前先给缺行情的买入信号入池并供价——信号进确认窗即开始被监控，
		// 确认窗满后用快照实时价撮合（实盘/模拟盘同段 buys 一并受益），消除"信号稳定却行情缺失"整轮拒绝。
		// English: before filling, ensure buy-signal codes lacking a live quote are monitored & priced this
		// round — a signal is observed from the start of its confirm window and filled at the fresh snapshot
		// price once confirmed (paper and live both benefit), removing whole-round "signal present, no quote" rejects.
		e.ensureBuyQuotes(buys, quotes)
		exitSell := append(append([]combat_agent.Signal{}, exitSigs...), alertSigs...)
		e.paperSignals(buys, exitSell, quotes, roundSellMode)

		// 实盘 auto 下单（§FIX-0921f 接线点 → §SIGNAL_CONTROLLER 20260917）：
		// 全量活跃买入信号送信号控制器 live 通道统一裁定（战法白名单/黑名单/持续性确认窗，
		// 原 realBuyConfirmPass+autoPlace 内联白名单两闸门收敛为此一处），pass 私才 autoPlace。
		// 本方是探针清理权的全量喂入方（prune=true）；主循环子集喂入只推进不清理。
		// autoPlace 与主循环共享幂等键（signal_id=buy:code:strategy:交易日），双通道叠加被
		// orders 表唯一约束拦重，不会重复下单。
		// English: single live dispatch entry — controller-admitted buys only; this full-feed prunes stale probes.
		e.dispatchLive(buys, quotes, true, time.Now())
	}

	// §SELLPOINT-UNIFY P3 模拟盘并轨：5s 轮同样推进纸面双账统一卖出裁决（与主循环 13e-pre
	// 共享同一 (通道,账号,代码) 状态机，窗口以真实时钟推进；close/trim 各自幂等去重，双轮
	// 叠加不会重复卖）。放在撮合分发之外无条件跑——本轮无信号也要推进观察窗。
	// English: the 5s round also advances the paper-ledger unified sell judge (same state machine,
	// wall-clock windows; idempotent close/trim dedup makes the dual-round overlay safe).
	e.judgePaperLedgers(sellJudgeFeed{
		Scores:      scores,
		D1Scores:    d1Scores,
		BearReasons: bearReasons,
		PoolQuotes:  exitQuotes,
		SnapQuotes:  quotes,
	}, roundSellMode)

	// 模拟盘估值与日净值：每轮用实时快照价刷新持仓市值，并记录当日净值点。
	// English: paper mark-to-market + daily equity point each round, using the live snapshot.
	e.paperMark(quotes)

	// §LLM 面板修复：近实时循环也捕获一次 Stage 快照（待归因原始新闻 + 已归因事件），
	// 保证主循环空闲/无 L2 时 LLM 诊断页主面板仍有"原始新闻缓存"可看（内部 60s 节流）。
	// English: the near-realtime loop also captures a Stage snapshot (pending raw news + attributed
	// events) so the LLM debug main panel shows a raw-news cache even when the main loop is idle
	// or no L2 news arrived (internally throttled to ≥60s).
	e.captureNearRealtimeStage()

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

	// SSE 通知前端分数已刷新（§MARKET_RISK_GATE F2/B3：附市场环境条所需的市场状态/仓位档/风险档）
	if e.sse != nil {
		_, mktState, maxPos, riskTier, riskReasons := e.MarketEnvSnapshot()
		e.sse.Broadcast(map[string]interface{}{
			"type":         "score",
			"count":        len(scores),
			"signals":      len(emit),
			"emotion":      emotionPhase,
			"market_state": mktState,    // bull/range/bear（状态机关闭=空串，前端隐藏状态徽标）
			"max_pos_pct":  maxPos,      // 状态机建议仓位档（0=未启用）
			"risk_tier":    riskTier,    // 风险档 Red/Yellow（B3，空=无风险档）
			"risk_reasons": riskReasons, // 风险档触发原因（供 F2 徽标 tooltip）
			"time":         time.Now().Format("15:04:05"),
		})
	}
}

// logNShapeDiag 打印本轮 N 形候选诊断概要 + 最可能出信号的若干明细。
// 排序：Pass（含一突/二突标记）在前、其余按总分降序，最多展示 8 条，避免刷屏。
func (e *Engine) logNShapeDiag(emotionPhase string, diags []combat_agent.NDiag) {
	// 概览四桶计数：Pass/Fail、D1=0 拦截（事件缺失）、总分不足（D1 有值但分数不够）——
	// 一眼区分"N 信号缺失"是事件问题还是评分问题
	pass, fail, d1Zero, totalLow := 0, 0, 0, 0
	for _, d := range diags {
		if d.Pass {
			pass++
		} else {
			fail++
		}
		if d.D1 <= 0 {
			d1Zero++ // D1 被归 0（无实质事件）→ N 形直接失去入场支撑
		} else if !d.Pass {
			totalLow++ // 事件在但总分不够水位
		}
	}
	log.Printf("[engine] N形诊断 emotion=%s 候选=%d pass=%d fail=%d d1=0拦截=%d 总分不足=%d",
		emotionPhase, len(diags), pass, fail, d1Zero, totalLow)
	// 概览打日志：Pass 优先 + 总分降序，最多 8 条明细，避免 294 条候选把日志刷成泥潭
	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Pass != diags[j].Pass {
			return diags[i].Pass // Pass 优先（含一突/二突标记），先给最可能出信号的
		}
		return diags[i].Total > diags[j].Total // 同 Pass 状态比总分
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
	// §P2#24 已持有持仓不参与墓碑判定：买入后已成交持仓的信号是"持有中"的呈现，
	// 现价跌破触发价只是浮亏，不是"买入依据破坏"——若照常打墓碑会把已买信号从
	// 当日固化/消息中心抹掉，用户看不到自己买的票。仅对未持有的 code 判失效。
	// rpt 为 nil（测试/未装配账本）时跳过豁免逻辑，保持原有墓碑行为。
	// English: P2#24 — signals whose code is currently held are NOT tombstoned: a filled position is a
	// hold, not a "broken buy premise"; tombstoning it would wipe the bought signal from today's pinned
	// store and message center. Only non-held codes are candidates for invalidation. When rpt is nil
	// (tests / no book attached) the exemption is skipped and the original tombstone behavior is kept.
	held := make(map[string]bool, 8)
	if e.rpt != nil {
		for _, c := range e.rpt.HeldPositionCodesFor(e.userID) {
			held[c] = true
		}
	}
	pinned := e.signalStore.List()
	if len(pinned) == 0 {
		return
	}
	for _, sig := range pinned {
		if sig.Direction != "做多" {
			continue
		}
		if held[pureTsCode(sig.Code)] {
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
// 仅计数可操作买入信号（Action=="buy"），观察类信号（watch/brief）不计入浏览器通知数量。
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

// countUniqueBuyCodes 统计做多/做空可操作买入信号各自覆盖的【去重股票数】。
// §修复 P2#23：SSE toast 数字此前数的是原始信号条数——同一只股票会被多战法同时扫中，
// 294 条原始信号去重后只有 76 只，与"信号列表（按 code 去重展示）"数量对不上
// （用户反馈 toast 40+ 而列表仅 21 条）。改为按 code 去重后计数，口径与列表一致。
// English: counts the DEDUPLICATED stock codes behind long/short actionable buy signals. The toast
// previously counted raw signal rows — the same stock can be caught by several strategies at once
// (294 raw rows → 76 unique codes), mismatching the code-deduped signal list. Dedup by code here so
// the toast matches what the list shows.
func countUniqueBuyCodes(sigs []combat_agent.Signal, action string) int {
	seen := make(map[string]struct{})
	for _, s := range sigs {
		if s.Action == action && s.Code != "" {
			seen[s.Code] = struct{}{}
		}
	}
	return len(seen)
}

// filterTransitionSignals 状态翻转去重（纯函数）：返回本轮应广播的信号 + 下一轮去重状态。
// 仅当某股某战法从 非Pass → Pass 翻转时广播；持续 Pass 不重发；翻回后再翻上会再发。
// 这是"信号翻转才发"机制的核心：避免同一信号每轮重复推送，只在状态变化时通知前端。
// English: state-transition dedup (pure function): returns signals to broadcast this round + next-round state.
// Only emits when a stock/strategy flips from non-Pass to Pass; sustained Pass is not re-sent; re-flip after
// flip-back does re-emit.
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
// sellMode=§M9（2026-09-22 修复批）轮首快照：sell_unified_mode 由宿主循环本轮开始时读一次注入，
// 本函数内三个消费点（统一裁决 runSellUnifiedJudge / 投影与旧五路取舍 Advise / 来源闸
// autoExecuteRealSellsRound）共用同一快照——旧实现各自独立读配置，shadow→on 中途翻转会出现
// 「旧路被来源闸关闭、统一投影又没生成」的既无卖出也无裁决的保护空轮（保护性止损跳过一轮）。
// English: live position advice — sellMode is the round-start sell_unified_mode snapshot (§M9);
// every consumer inside this round reads the snapshot, never the live config, so a mid-round flip
// can no longer produce a round with neither the legacy path nor the unified projection.
func (e *Engine) pushRealAdvice(md map[string]*strategy_engine.StockMarketData, scores map[string]combat_agent.StockScores, d1Scores map[string]combat_agent.D1Score, emotionPhase string, quotes map[string]*data.StockInfo, bearReasons map[string]string, sellMode string) {
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
	// §CB-TICKWINDOW（2026-09-21 误熔实录）：只在连续竞价窗口探测/计失联。QMT 内嵌桥的心跳由
	// handlebar-tick 驱动，盘前/开盘竞价（9:15-9:30）与收盘竞价（14:57-15:00）行情不推进，
	// 桥必然静默——按失联口径会计时误熔（当日 09:10:27 误熔、09:25-09:30 断流）。真断线仍有
	// 双保险：网关 disconnect 回报即时 SetTripped 熔断 + 9:30/13:00 后探测 2 分钟内熔断。
	if data.IsContinuousTrade(time.Now()) {
		ctrl.HealthCheck()
	}
	// §W6-a 周期对账（默认 5min 节流）：首尔侧主动拉网关持仓落库，终结"双向对账均不存在"的盲区
	ctrl.MaybeReconcile(5 * time.Minute)
	// §R4-1 撤单闭环（30s 节流 + 每日收盘清单）：未成交超时自动撤 / 占位行降级 / 收盘清单，
	// 根除"已报委托悬置整天虚耗买入纪律预算"的悬置态（内部自节流，5s 循环调用无额外开销）
	if res := ctrl.SweepOrders(time.Now()); res != nil {
		_ = res // 摘要日志已在 SweepOrders 内按需打印
	}
	// §WS-B 券商交割单三方对账（默认关闭；启用后每日 settle_at 后对账一次，差异告警+可选补记）
	if sc := ctrl.Config().Settle; sc.Enabled {
		ctrl.MaybeSettleDay(data.TradingDayDate(time.Now()), sc.Mode, sc.At, true)
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

	// 统一纪律裁决引擎惰性初始化（§统一纪律 B）：实盘止盈/止损/移动止盈/深破走探针+扳机状态机，
	// 与模拟盘同口径。nil 安全：qmtCtrl 未启用时 pushRealAdvice 不会到这里。
	// English: lazy-init the unified-discipline tracker — live TP/SL/trail/deep go through the probe+
	// trigger state machine, unified with paper. Nil-safe: pushRealAdvice exits before this when QMT is off.
	e.mu.Lock()
	if e.disciplineTracker == nil {
		e.disciplineTracker = trading.NewDisciplineTracker()
	}
	dt := e.disciplineTracker
	e.mu.Unlock()

	// §REFACTOR_UNIFIED_SELL P2：统一卖出裁决先于展示拼装运行（同轮行情/信号，留痕与卡片时刻对齐）。
	// shadow（缺省）只留痕不改行为；on 时本轮裁决处置/观察结论经 unifiedSellViews 投影为卖出卡片
	// 唯一来源，旧五路在 Advise 内整体跳过。
	// §M9（2026-09-22 修复批）sellMode=轮首快照（入参），本轮所有消费点（裁决/投影/来源闸）
	// 一律用它，函数内不再读配置——旧实现此处+runSellUnifiedJudge+autoExecuteRealSells 一轮三读，
	// 翻转时机不当即产生「既无旧路卖出也无统一投影」的保护空轮。
	// English: §M9 — sellMode is the round-start snapshot threaded in by the host loop; every
	// consumer in this round uses it instead of re-reading config.
	sellVerdicts := e.runSellUnifiedJudge(sendTo, positions, exitQuotes, quotes, scores, d1Scores, bearReasons, sellMode)
	// §PROD-T1 可卖量一次装配：Advise 的 T+1 卖出闸与 P2 处置降级共用同一口径。
	sellableQty := sellableQtyByCode(realStore, sendTo, positions)
	var sellProjection []trading.UnifiedSellView
	if sellMode == "on" {
		sellProjection = e.unifiedSellViews(positions, sellVerdicts, sellableQty)
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
		BearReasons:  bearReasons, // FIX#13 利空归因接线：实盘持仓命中利空 → 止损级建议 → 自动清仓
		Cfg:          ctrl.Config(),
		DiscTracker:  dt, // 统一纪律裁决（探针+扳机）
		// §PROD-T1（2026-09-18 生产实录）T+1 可卖量装配：可卖 = 持仓 − 当日买入成交
		// （store.BuyableQtyForUserSell 既有账本口径，与下单闸 §WS-A 同源）。全锁持仓在
		// Advise 内整体跳过卖出侧，杜绝"当日买入却提醒止盈/止损请手动处理"的误导提醒。
		// English: §PROD-T1 — feed per-code sellable qty (held minus today's bought fills, same
		// ledger math as the order gate) so T+1-locked positions get no sell-side advice.
		SellableQty: sellableQty,
		// §REFACTOR_UNIFIED_SELL P2：切闸开关 + 裁决投影卡片（on 时卖出建议唯一来源）。
		SellUnifiedOn:  sellMode == "on",
		SellProjection: sellProjection,
	})

	// §SHORT-2 做空战法卖出标记 → 实盘清仓级建议（Source=short_tactic，见 shortTacticCloseAdvices）。
	advices = append(advices, e.shortTacticCloseAdvices(positions, advices, exitQuotes)...)

	// §GAP1.2 M8 组合回撤熔断（risk.M8Check 口径接线）：实盘组合市值自峰值回撤超阈值 → 全部自动卖出。
	// 此前 M8Check 是死代码；现接入实盘链路（峰值随进程内存续，平仓后基线归零）。
	e.checkM8RealDrawdown(ctrl, realStore, positions, exitQuotes)

	// §GAP1.1 实盘卖出自动化：mode=auto 且 qmt.auto_sell 开启时，止损级建议自动全仓卖出。
	// signal_id 按"码+类+日"幂等——orders 表唯一键天然防重，跨重启/跨轮次不会二次下单。
	if len(advices) > 0 {
		// §P1-4（2026-09-15）：卖出自动执行链路透传行情快照（CurrentPrice/PrevClose 由
		// sellRealPosition 注入 OrderRequest），涨跌停闸对自动卖单真正生效。
		// §M9：来源闸吃轮首快照，与上方投影/裁决同一口径（见 pushRealAdvice 头注释）。
		e.autoExecuteRealSellsRound(sendTo, ctrl, realStore, advices, sellMode)
	}

	// §SELLPOINT-UNIFY P1-b/P2：卖出统一裁决通道已上移至 trading.Advise 之前运行（见上方
	// runSellUnifiedJudge），shadow 只留痕、on 走投影+来源闸执行，此处不再重复调用。

	// §统一纪律补充（2026-09-08）：无论自动卖出开关，止损/止盈/减仓建议都进消息中心 + P1 强提醒。
	// 用户关闭自动交易（mode≠auto / auto_sell=false）时，实时持仓触发止盈止损仍需强提醒手动处理；
	// auto 开启时也提示"已触发自动卖出"，动作全程可核对。按 码@类@交易日 去重，5s 循环不重复轰炸。
	// English: regardless of the auto-sell switch, 止损/止盈/减仓 advices also land in the message
	// center with a P1 strong push — with auto trading off a live TP/SL/trim trip still demands manual
	// handling; with auto on it confirms the sell fired. Deduped per code/class/trading-day.
	e.syncLiveAdviceAlerts(sendTo, advices, ctrl.Enabled() && ctrl.Mode() == "auto" && ctrl.Config().AutoSell)

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
// 例如：sell:600519:止损:2026-08-30 表示茅台在当天的止损卖出幂等键。
func realSellSignalID(tsCode, class string) string {
	return fmt.Sprintf("sell:%s:%s:%s", pureTsCode(tsCode), class, data.TradingDayDate(time.Now()))
}

// fullCloseClasses 「今日全部全平类」清单（§P0-3，2026-09-15）：止损/止盈/m8 三类全平卖出
// 各持独立幂等键，剩余量必须对三类已成交做并集扣减。P2#13 只列了 止损+m8——同日 discipline
// 止盈先全平、fills 回报滞后时，止损侧仍按旧持仓量计算剩余 → 对已空仓头寸再下卖单。
// English: §P0-3 — the full set of full-close sell classes whose today's fills must be aggregated
// when computing sell remaining (stop-loss / take-profit / m8 each use an independent idempotency key).
var fullCloseClasses = []string{"止损", "止盈", "m8"}

// realSoldOrOpenQtyToday P2#13 + §P0-2/§P0-3（2026-09-15）汇总某账号某持仓「今日不可再卖量」：
//   - Σ已成交：今日各全平类（fullCloseClasses）的累计已成交数量——与单类 SumFilledQty 的区别：
//     止损/止盈/m8 是三个独立幂等键，旧实现各自只计本类的成交，若同日 止损 先全平、m8 随后再触发，
//     m8 侧剩余量仍按全量算 → 对已无仓的持仓下第二单（超额卖出）。按全部全平类累加后，
//     剩余 = 持仓量 - Σ已卖，任一类先卖多少另一类就看到剩余多少，从根源杜绝跨类重卖。
//   - Σ在途：当日非终态卖单的**未成交余量**（SumOpenSellQty）——P2#13 只覆盖「fills 已落库」的跨轮场景；
//     M8 清仓与止损建议同轮触发时（M8 卖单 fills 尚未回报），两类各按全量各下一笔全额卖单，
//     第二笔只能靠柜台「证券不足」废单兜底。把在途卖量并入后，先到者占额度，后到者剩余=0 自然跳过。
//     §N-3（2026-09-22 傍晚批复验）：在途项旧口径对 `部成` 单按**整笔委托量**计，与本函数第一项
//     Σ已成交重复数了一遍同一笔成交（双扣 → 部成后剩余量恒 ≤0，当天永不补卖）；现按订单净额
//     （qty − 该单已成交）计，两项相加恰等于「该单占住的量」。买卖两侧「部成」定义同源，
//     见 store.SumOpenSellQty 与 docs/BUGFIX_BUDGET_FREEZE_LEDGER_20260918.md（买入侧 §BUDGET_FREEZE）。
//
// English: P2#13 + §P0-2/§P0-3 + §N-3 — today's un-sellable qty for a code: Σfilled across ALL full-close
// classes (stop-loss/take-profit/m8 use independent idempotency keys; aggregating every class makes
// remaining = held − Σsold so no cross-class double-sell) plus Σ open sell **unfilled remainder**
// (non-terminal tickets; §N-3 counts a 部成 ticket at qty−filled, never its whole qty, otherwise the
// filled part is deducted twice and the remainder can never be topped up the same day).
func (e *Engine) realSoldOrOpenQtyToday(realStore *store.DB, userID, tsCode string) int {
	if realStore == nil {
		return 0
	}
	total := 0
	for _, c := range fullCloseClasses {
		total += realStore.SumFilledQty(userID, realSellSignalID(tsCode, c))
	}
	// §P0-2 在途卖单：终态（已成/已撤/部撤/废单）与「发送失败」占位行都不计入。
	// 委托 created_at 为 RFC3339（yyyy-MM-ddT…+08:00），按北京时间「当日日期前缀」过滤。
	total += realStore.SumOpenSellQty(userID, tsCode, cntime.In(time.Now()).Format("2006-01-02"))
	return total
}

// pureTsCode 剥离交易所后缀，返回纯数字代码。
// 例如：600519.SH → 600519，000001.SZ → 000001，430047.BJ → 430047。
// 用于构建幂等键（signal_id）和比对持仓代码。
func pureTsCode(tsCode string) string {
	for _, suf := range []string{".SH", ".SZ", ".BJ"} {
		if i := strings.LastIndex(tsCode, suf); i > 0 {
			return tsCode[:i]
		}
	}
	return tsCode
}

// sellRealPosition 通过控制器对单一实盘持仓下卖出单。行情缺失时跳过（宁可不卖不以错价报单）。
// §P1-4（2026-09-15）：卖出单注入 CurrentPrice/PrevClose 行情上下文——此前只填 StalenessMs，
// 风控闸 checkLimitPrice 因 PrevClose<=0 恒 fail-open，即使打开 limit_down_block_sell，
// 跌停日自动止损单照样发出（闸门宣称的能力与实际行为不符）。行情缺失时字段为 0，
// 涨跌停闸维持 fail-open（风险敞口可退出的立场不变），但有行情即生效。
// English: §P1-4 — sell orders now carry CurrentPrice/PrevClose so the limit-up/down risk gate
// (which fail-opens on PrevClose<=0) actually sees market context; without quotes the gate stays
// fail-open (positions remain exitable) instead of blocking protective sells.
func (e *Engine) sellRealPosition(ctrl *trading.Controller, p store.RealPosition, qty int, signalID string, price float64, class, reason string) error {
	cfg := ctrl.Config()
	if qty <= 0 || price <= 0 {
		return nil
	}
	req := trading.OrderRequest{
		SignalID:    signalID,
		Code:        p.TsCode,
		Name:        p.Name,
		Strategy:    p.Strategy,
		Side:        trading.SideSell,
		PriceType:   cfg.PriceType,
		Price:       price,
		Qty:         qty,
		Amount:      price * float64(qty),
		CreatedAt:   time.Now().Format(time.RFC3339),
		StalenessMs: e.quoteStalenessMs(p.TsCode),
	}
	// §P1-4（2026-09-15）：直接取 fetcher 最近一轮实时快照注入 CurrentPrice/PrevClose——
	// 此前卖出单只填 StalenessMs，风控闸 checkLimitPrice 因 PrevClose<=0 恒 fail-open，
	// 即使打开 limit_down_block_sell，跌停日自动止损单照样发出。快照缺失时字段为 0，
	// 涨跌停闸维持 fail-open（风险敞口可退出的立场不变），但有行情即生效。
	if q := e.snapshotQuotes()[pureTsCode(p.TsCode)]; q != nil {
		req.CurrentPrice = q.Price
		req.PrevClose = q.PrevClose // §P1-5 显式昨收字段（>0 才有值；旧语义回退见下）
		if req.PrevClose <= 0 {
			req.PrevClose = q.Close
		}
	}
	res, err := ctrl.PlaceOrder(req)
	if err != nil {
		log.Printf("[qmt] 自动卖出 %s(%s) 失败: %v", p.TsCode, p.Name, err)
		return err
	}
	if res != nil && !res.OK {
		if strings.Contains(res.Err, "duplicate") {
			// §GAP2-W1 语义更新 + §H1-MG 修订：duplicate 现在只可能意味着"当日同类卖单已真实报出、
			// 或在途/已有成交"（发送失败、**已撤零成交**的单都会经 MarkRealOrderSendFailed /
			// ResetFailedRealOrder 的同键重放放行，不再以 duplicate 形态出现），
			// 因此幂等命中=目标已达成，静默返回是正确行为。
			return nil // 当日已下过同类卖单（幂等命中），静默
		}
		// §H4（2026-09-22 修复批）业务拒单不再假成功：网关 200+ok:false（券商拒单等）时
		// err 为 nil，旧实现只打日志就返回 nil——调用方把"没卖出去"当成功，幂等槽（realTrimDone）
		// 被假成功烧掉，当日永不再试。控制器已把占位行降级"发送失败"（§R3-1 P0-A，同键可重试），
		// 此处按失败返回错误，由调用方决定"槽不烧、下一轮重试"。
		reject := res.Err
		if reject == "" {
			reject = "网关未受理"
		}
		log.Printf("[qmt] 自动卖出 %s(%s) 被网关拒单: %s", p.TsCode, p.Name, reject)
		return fmt.Errorf("sell %s rejected: %s", p.TsCode, reject)
	}
	log.Printf("[qmt] 自动卖出 %s(%s) %d股 @%.2f 类别=%s 原因=%s → %+v",
		p.TsCode, p.Name, qty, price, class, reason, res)
	return nil
}

// autoExecuteRealSells §GAP1.1 的兼容入口（未指定轮次快照的调用方/测试用）：
// 以调用时刻的配置读取作为「本轮」快照。生产链路（pushRealAdvice）一律改走
// autoExecuteRealSellsRound 并吃轮首快照（§M9），杜绝同轮内各消费点各读各的。
func (e *Engine) autoExecuteRealSells(userID string, ctrl *trading.Controller, realStore *store.DB, advices []trading.PositionAdvice) {
	e.autoExecuteRealSellsRound(userID, ctrl, realStore, advices, e.sellUnifiedModeEngine())
}

// autoExecuteRealSellsRound 自动卖出执行体（§M9：sellMode=轮首快照，函数内不读配置）。
// §R3-1 P0-B 按账号过滤：此前读全表 RealPositions()——多账号部署下 A 账号的止损建议可能
// 匹配到 byCode 映射里 B 账号的同名持仓并真实卖出（资损级）。与建议生成路径的
// §GAP2-W2 收敛口径对齐，统一走 RealPositionsForUser(userID)。
// English: R3-1 P0-B — filter positions by account: the old full-table RealPositions() could pair
// account A's stop-loss advice with account B's same-code position and really sell it.
func (e *Engine) autoExecuteRealSellsRound(userID string, ctrl *trading.Controller, realStore *store.DB, advices []trading.PositionAdvice, sellMode string) {
	if realStore == nil || ctrl == nil {
		return
	}
	// FIX#14 显式化卖出静默门槛：mode≠auto 或 auto_sell=false 时，若有止损级建议却跳单——
	// 打一条节流告警让"为什么没自动卖"一眼可见（旧实现直接 return，用户以为自动卖出已开）。
	// English: FIX#14 surface the silent sell gate — when stop-loss class advice exists but auto mode
	// is off, log a throttled warning so "why didn't it auto-sell" is visible (the old early-return
	// made users believe auto-sell was on while nothing ever fired).
	gated := !ctrl.Enabled() || ctrl.Mode() != "auto" || !ctrl.Config().AutoSell
	if gated {
		for _, a := range advices {
			if a.Action == "止损" {
				reason := "qmt-disabled"
				if ctrl.Enabled() {
					if ctrl.Mode() != "auto" {
						reason = "mode-" + ctrl.Mode()
					} else if !ctrl.Config().AutoSell {
						reason = "auto_sell=false"
					}
				}
				log.Printf("[qmt-gate] %s(%s) 止损级建议未自动卖出: %s (mode=%s auto_sell=%v enabled=%v)",
					a.Code, a.Name, reason, ctrl.Mode(), ctrl.Config().AutoSell, ctrl.Enabled())
				opslog.DayOnce("auto-sell-gate:"+a.Code, func() {
					opslog.Logf("quant", "止损级建议未自动卖出 %s(%s) 原因=%s", a.Code, a.Name, reason)
				})
				break
			}
		}
		return
	}
	positions, err := realStore.RealPositionsForUser(userID)
	if err != nil || len(positions) == 0 {
		return
	}
	// §REFACTOR_UNIFIED_SELL P2 切闸：sell_unified_mode=on 时唯一执行出口=裁决层 pass 处置单
	//（Source=unified）。旧「止损级任意来源直放」后门与 short_tactic 直卖后门被来源闸一并关闭；
	// M8 组合回撤熔断不经本函数（checkM8RealDrawdown 直调 sellRealPosition，独立保险丝保留）。
	// §M9：判定取轮首快照入参，与同轮 runSellUnifiedJudge/投影同一口径——旧实现在此独立读配置，
	// shadow→on 翻转时本轮既关旧路又无投影（保护空轮）。
	// English: P2 gate — under mode=on only unified-adjudicator disposals may execute here; the
	// any-source stop-loss backdoor and the short_tactic direct-sell path are closed. M8 stays a
	// separate fuse outside this function. §M9: the mode is the round-start snapshot param.
	sellUnifiedOnly := sellMode == "on"
	byCode := make(map[string]store.RealPosition, len(positions))
	for _, p := range positions {
		byCode[pureTsCode(p.TsCode)] = p
	}
	for _, a := range advices {
		p, ok := byCode[a.Code]
		if !ok || a.RefPrice <= 0 {
			continue
		}
		if sellUnifiedOnly && a.Source != trading.UnifiedSellSourceAction {
			continue
		}
		// §统一纪律：止损级建议任何来源都自动执行（保护性不变）；止盈/减仓仅在来源为统一纪律
		// 裁决引擎（Source=discipline）或统一卖出裁决层（Source=unified，§SELLPOINT-UNIFY P2）时
		// 自动执行——战法自带止盈止损降级为触发通知，不动作。
		// English: stop-loss advice auto-executes from any source (unchanged protection; in P2 on-mode
		// the source gate above already restricts to unified disposals only); TP/trim only auto-execute
		// from the discipline engine or the unified sell adjudicator.
		var class string
		var qty int
		executableSellSource := a.Source == "discipline" || a.Source == trading.UnifiedSellSourceAction
		switch a.Action {
		case "止损":
			class = "止损"
		case "止盈":
			if !executableSellSource {
				continue
			}
			class = "止盈"
		case "减仓":
			if !executableSellSource {
				continue
			}
			class = "减仓"
		default:
			continue
		}
		// §修复 R6：日级幂等键 sell:<code>:<类别>:<交易日> 统计已成交数量，仅对"剩余未成交"部分补卖；
		// 信号键追加 :r<剩余量> 桶——剩余量变化才开新单，避免部成后死循环重复下单，
		// 也保证同日同剩余量不重复刷单（broker 仍在处理该笔时）。
		// §修复 P2#13：剩余量按「今日全部全平类已成交」扣减（止损+止盈+m8），不再只看本类——
		// 否则同日 止损 全平后 m8 再触发会对已空仓的持仓下第二单（超额卖出）。
		// §P0-2（2026-09-15）：再扣「今日在途卖单」——同轮 M8 清仓先占额度后，本函数即使看到
		// 陈旧持仓快照（M8 fills 未回报）也不会再对同一持仓发第二笔全额卖单。
		// §N-3（2026-09-22 傍晚批）同剩余量幂等桶的复核结论（本条不能盲改的唯一原因）：
		//   在途项由「整笔 qty」改成「未成交余量」后，桶键仍然安全，因为**一笔被受理的新卖单会
		//   等额抬高 Σ在途**——发单成功那一刻 remaining 就下降，同轮/下轮再算必然得到更小的剩余量，
		//   于是「同剩余量」这一条件只有在①无新单被受理（duplicate/失败）或②在途单结清（终态）时
		//   才可能重现，两种情形都不该再刷单/都已被唯一键拦下。反例（旧口径）恰是相反方向：部成
		//   推进把 remaining 压成负数，`:r` 桶永不刷新 → 少卖。
		//   唯一需要留意的残余形态：某桶订单被撤且零成交时 remaining 会回到该桶旧值，此时
		//   signal_id 唯一键命中已有行 → 走 ResetFailedRealOrder（§H1-MG 已撤零成交可重试）放行，
		//   不是新缺陷，也不在本条范围。
		base := realSellSignalID(p.TsCode, class)
		filled := e.realSoldOrOpenQtyToday(realStore, userID, p.TsCode)
		remaining := p.Qty - filled
		if remaining <= 0 {
			continue
		}
		// §统一纪律：减仓半平每码每日一次（纪律状态机每轮重放 ActionTrim，须去重防反复减半）。
		// English: trim halves at most once per code per day (the discipline state machine re-fires
		// ActionTrim every round — dedup prevents repeated halving).
		// §H4（2026-09-22 修复批）幂等槽「先成功后烧」：旧实现在下卖单前置 realTrimDone、随后
		// sellRealPosition 的错误又被 `_ =` 丢弃——卖单失败（熔断/风控拒单/网关业务拒单）时当日
		// 减仓槽位已烧，之后每轮重放全被去重拦截，减仓永久错过。现在预检只读不写，卖单成功返回
		// （含 duplicate 幂等命中=目标已达成）后才回写槽位；失败打 log+opslog，槽位不烧、下一轮可重试。
		trimDay := ""
		if class == "减仓" {
			e.mu.Lock()
			if e.realTrimDone == nil {
				e.realTrimDone = map[string]string{}
			}
			day := time.Now().Format("20060102")
			if e.realTrimDone[p.TsCode] == day {
				e.mu.Unlock()
				continue
			}
			e.mu.Unlock()
			trimDay = day
			// §P0-3（2026-09-15）：减仓量必须整手（主板 100 股/手）——旧实现 remaining/2 会把
			// 1500 股减成卖 750（非整手非全平）→ 柜台废单，既没减成还烧一次委托；paper 侧同逻辑
			// 早有 /2/100*100 取整（paper.go），实盘路径补齐同款。取整后为 0（持仓<200 股）降级为
			// 通知（syncLiveAdviceAlerts 已统一推送），不强凑全平。
			qty = remaining / 2 / 100 * 100
			if qty <= 0 {
				log.Printf("[qmt] %s(%s) 减仓半平取整后为 0（持仓 %d 股<2 手），降级为提醒", p.TsCode, p.Name, remaining)
				continue
			}
		} else {
			qty = remaining
		}
		sid := fmt.Sprintf("%s:r%d", base, qty)
		// §H4（2026-09-22 修复批）错误不再吞：卖单失败即 log + opslog 留档（5s 轮高频，opslog 按
		// 码+类别 1 分钟节流），幂等槽不烧，纪律状态机下一轮重放同键补卖（占位行已降级"发送失败"可重试）。
		if serr := e.sellRealPosition(ctrl, p, qty, sid, a.RefPrice, class, a.Reason); serr != nil {
			log.Printf("[qmt] 实盘自动卖单失败 %s(%s) 类别=%s: %v（幂等槽未烧，下一轮可重试）", p.TsCode, p.Name, class, serr)
			opslog.OncePer("real-sell-fail:"+pureTsCode(p.TsCode)+":"+class, time.Minute, func() {
				opslog.Logf("quant", "实盘自动卖单失败 %s(%s) 类别=%s 建议原因=%s: %v", p.TsCode, p.Name, class, a.Reason, serr)
			})
			continue
		}
		if class == "减仓" {
			// §H4 卖单成功后才烧当日减仓槽（duplicate 幂等命中同样视为目标达成，一样烧槽）。
			e.mu.Lock()
			if e.realTrimDone == nil {
				e.realTrimDone = map[string]string{}
			}
			e.realTrimDone[p.TsCode] = trimDay
			e.mu.Unlock()
		}
	}
}

// sellableQtyByCode §PROD-T1（2026-09-18 生产实录）：为实盘持仓批量装配"今日可卖量"
// （ts_code → 可卖 = 持仓 − 当日买入成交，走 store.BuyableQtyForUserSell 账本口径，
// 与下单 T+1 硬闸 §WS-A 同源）。日期用北京时日历日（cntime 统一时区，防首尔时钟跨天错位）。
// English: §PROD-T1 — builds per-position sellable qty (held minus today's bought fills) for the
// T+1 advice gate, using the same ledger helper as the order guard; Beijing-time day boundary.
func sellableQtyByCode(db *store.DB, userID string, positions []store.RealPosition) map[string]int {
	day := cntime.In(time.Now()).Format("2006-01-02")
	out := make(map[string]int, len(positions))
	for _, p := range positions {
		out[p.TsCode] = db.BuyableQtyForUserSell(userID, p.TsCode, day)
	}
	return out
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
	// §F-7（20260917 缺陷修复批）启用判定收敛：阈值归一/开关检查并入 risk.M8CheckWith
	// （全系统唯一实现，R7 正数归一口径原样保留），此处只做"未启用快速返回"的预检。
	if !cfgMgr.GetRulesFor(userID).RiskCtrl.M8Enabled {
		return
	}
	total := 0.0
	for _, p := range positions {
		price := p.CostPrice
		if q := quotes[pureTsCode(p.TsCode)]; q != nil && q.Price > 0 {
			price = q.Price // 有实时价用实时价；缺行情的持仓按成本价兜底，保证估值连续不漏仓
		}
		total += price * float64(p.Qty) // 组合总市值 = Σ(价×量)
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
	// 组合回撤触发判定（§F-7：调用唯一权威实现 risk.M8CheckWith，原内联口径已并入）
	verdict := risk.M8CheckWith(cfgMgr.GetRulesFor(userID), total, peak)
	if verdict.Pass {
		return
	}
	log.Printf("[qmt] M8 兜底触发: 组合市值 %.0f 自峰值 %.0f %s —— 全部自动卖出",
		total, peak, verdict.Reason)
	for _, p := range positions {
		price := p.CostPrice
		if q := quotes[pureTsCode(p.TsCode)]; q != nil && q.Price > 0 {
			price = q.Price
		}
		// §修复 R6：M8 清仓同样走剩余量补卖逻辑（按 code:day 桶统计已成交，剩余量>0 才发单）。
		// §修复 P2#13：剩余量按「今日全部全平类已成交」扣减（止损+止盈+m8），与止损路径同口径——
		// 防止 M8 在止损已全平后对空仓再下一单。
		// §P0-2（2026-09-15）：同轮互斥——M8 卖单一旦报出即在途占额度，同轮后执行的止损路径
		// （autoExecuteRealSells）看到剩余=0 自然跳过，杜绝同轮双笔全额卖单。
		base := realSellSignalID(p.TsCode, "m8")
		filled := e.realSoldOrOpenQtyToday(realStore, userID, p.TsCode)
		remaining := p.Qty - filled
		if remaining > 0 {
			sid := fmt.Sprintf("%s:r%d", base, remaining)
			// §H4（2026-09-22 修复批）同批止吞错：M8 清仓失败不再 `_ =` 静默——占位行已降级
			// "发送失败"，下一轮 M8 仍触发时同键可重试；这里补一条显式失败日志留证。
			if serr := e.sellRealPosition(ctrl, p, remaining, sid, price, "m8", verdict.Reason+"兜底清仓"); serr != nil {
				log.Printf("[qmt] M8 兜底清仓卖单失败 %s(%s): %v（下一轮 M8 触发可重试）", p.TsCode, p.Name, serr)
			}
		}
	}
}

// qmtM8State §R4-7 M8 峰值持久化结构（accounts/<uid>/qmt_m8.json）。
// 保存实盘组合市值峰值，用于跨重启后继续计算组合回撤。
// 峰值在每次组合市值创新高时更新，空仓时归零重新累计。
type qmtM8State struct {
	PeakTotal float64 `json:"peak_total"` // 组合市值峰值（M8 回撤基线），单位：元
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
	// 读取失败/损坏按 0 处理（进程内峰值兜底，不影响主流程）。
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
	// 原子写：先建目录、写临时文件再 rename（损坏/失败仅影响重启基线连续性）。
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// refreshStalenessGauges §UPDLINK（2026-09-22 · AUDIT_E2E_FULL H-4 / 审计 N-1）：为告警评估器
// 喂两条"新鲜度"量规（在 RunAlertEvaluation 之前调用）。
//  1. quote_staleness_sec：此前全仓只有规则没有数据源（没有任何 SetGauge 写过这个键，规则
//     quote_stale 恒不触发＝死规则）。现按 fetcher 快照陈旧度真实写入，且**只在盘中采集**——
//     采集器盘后本就停轮、陈旧度必然无限增长，全天候写值会变成每晚一条 p2 噪音。
//  2. uplink_staleness_sec：距最近一次网关上行回报的秒数。H-4 事故里 SSE 广播锁被一次
//     double-close panic 永久占用，POST /api/qmt/report 全线挂死 1h45m、实盘账冻结成上午的旧
//     照片，而引擎自身日志一切正常（打分照常刷）——这条量规就是那 1h45m 静默期的替代品：
//     网关心跳每 60s 一发且不分盘后盘前，连续几分钟无入账即说明上行被堵（规则 uplink_stale p1）。
//
// 统一口径：未知/不适用（未配置采集器、从未采集、未开实盘、从未上报）一律写 0——既不伪造
// "新鲜"也不伪造"陈旧"；侧全是 gt 阈值，0 恒不触发。
// English: §UPDLINK — feed the two freshness gauges the alert evaluator consumes (quote snapshot age,
// sampled only during the session; and gateway uplink report age). Unknown/not-applicable writes 0,
// which never trips a gt rule.
func (e *Engine) refreshStalenessGauges() {
	quoteAge := int64(0)
	if data.IsActiveSession(time.Now()) {
		e.mu.RLock()
		f := e.fetcher
		e.mu.RUnlock()
		if f != nil {
			if s := int64(f.Staleness().Seconds()); s > 0 {
				quoteAge = s
			}
		}
	}
	metrics.SetGauge("quote_staleness_sec", quoteAge)

	uplinkAge := int64(0)
	// QMTController/Snapshot 各自取锁，且本函数在 scoreCycle 顶部调用时 e.mu 已释放，无重入死锁。
	if c := e.QMTController(); c != nil {
		if snap := c.Snapshot(); snap.Enabled && !snap.LastReportAt.IsZero() {
			if s := int64(time.Since(snap.LastReportAt).Seconds()); s > 0 {
				uplinkAge = s
			}
		}
	}
	metrics.SetGauge("uplink_staleness_sec", uplinkAge)
}
