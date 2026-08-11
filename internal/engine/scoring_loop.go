// Package engine 近实时 8a/8b 持续打分循环：
// 以 5s 节奏对 持仓+自选 打分池执行四战法评分 + 动量分（复用 8a/8b evalAll 口径），
// 分数写入聚合器与持久化；Pass 战法生成的信号按"状态翻转才发"去重后广播，
// 入池（StockTracker）仍由 5min 主循环统一负责。
package engine

import (
	"context"
	"log"
	"sort"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
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

// scoreCycle 执行一轮近实时打分：收拢持仓+自选 → 构建行情 → 8a/8b 打分+信号 → 状态翻转去重 → 更新看板/落盘。
func (e *Engine) scoreCycle(ctx context.Context) {
	// 防御：单轮 panic 不拖垮整个循环，记录日志后继续下一轮
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[engine] 打分循环 panic: %v", r)
		}
	}()

	// 交易时段门控（盘后/休市跳过，避免无效拉取）
	switch data.CurrentSession(time.Now()) {
	case data.SessionPreMarket, data.SessionMorningTrade, data.SessionPreAfternoon, data.SessionAfternoonTrade:
	default:
		return
	}

	e.mu.RLock()
	f := e.fetcher
	emotionPhase := e.lastEmotionPhase // 复用主循环算出的情绪阶段，不重复调涨停池接口
	d1Scores := e.lastD1Scores         // 复用主循环最近一轮 D1 评分，不每 5s 调 LLM
	e.mu.RUnlock()
	if f == nil {
		return
	}

	// 打分池 = 持仓 + 自选（近实时只覆盖这两类，入池管理归 5min 主循环）
	positions := e.rpt.HeldPositionCodes()
	watchlist := e.wlMgr.List()
	pool := mergeCodes(nil, nil, positions, watchlist)
	if len(pool) == 0 {
		return
	}

	// 优先取 fetcher 的 5s 实时快照作为行情来源（缺失的由 BuildScoringData 内部降级补齐）
	var quotes map[string]*data.StockInfo
	if snap := f.Snapshot(); snap != nil {
		quotes = snap.Stocks
	}

	md := e.strategy.BuildScoringData(ctx, pool, quotes)
	scores, sigs := e.combatAgent.ScorePool(pool, md, d1Scores, emotionPhase)

	// 开市(9:30)前只更新评分数字，不发布任何战法信号：
	// 盘前无实盘成交量，双响炮/龙头等易基于存量历史数据误报（如整池双响炮全 70、9:11 龙头）。
	// prevPass 不加/清空处理由 filterTransitionSignals 自然完成：9:30 后首个 Pass 仍会翻转发一次。
	if data.BeforeOpenTrade(time.Now()) {
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

	// 有分数才更新看板并落盘（保持与 8a/8b 主循环同口径）
	if len(scores) > 0 {
		e.agg.UpdateFast(scores, emit, e.rpt)
		e.scoreStore.Save(data.TradingDayDate(time.Now()), scores)
	}

	// 记录本轮新翻转出来的信号（供排查）
	if len(emit) > 0 {
		for _, sig := range emit {
			log.Printf("[engine] 近实时信号 %s(%s) %s 分=%.0f/%s",
				sig.Code, sig.Name, sig.Strategy, sig.Confidence*100, sig.Reason)
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
