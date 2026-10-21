// d1_incremental.go §D1-NOON（2026-10-22 遗留项②）：增量 D1 的评分决策器。
//
// 背景：§信号速度 S1 把「每轮全池调 LLM」改成「按事件签名增量重评」后，事件池被时效/重复
// 衰减滤空的轮次会把签名算成空串，于是全池被判定为「事件变化」重评、noEvent 强制归 0；
// 下一条新事件到达后又跳回正分（10-12 实录 d1 15→0→25、归因文本漂移，11:00–13:00 约 300 条
// 候选被 D1=0 拦截）。本文件把决策抽成纯函数：空事件池被视为管道状态缝，整轮沿用上一轮
// 评分并保留签名；其余路径与原增量口径一致。
package engine

import (
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/strategy_engine"
)

// d1RoundPlan 一轮增量 D1 的评分决策（纯数据，便于行为锁直接断言决策面）。
type d1RoundPlan struct {
	// toScore 需要调用 LLM 重评的个股（事件集变化 / 缺分 / 待重试）。
	toScore []string
	// reuse 直接沿用上一轮评分的个股（事件签名未变，或本轮事件池空窗）。
	reuse map[string]combat_agent.D1Score
	// sigNow 本轮评分后的事件签名快照；nil＝本轮事件池为空，保留上一轮签名不覆写。
	sigNow map[string]string
	// empty 本轮有效事件池为空（时效/重复衰减把最后一条有效事件也滤掉了）。
	empty bool
}

// planD1Round §D1-NOON（2026-10-22 遗留项②）：增量 D1 的评分决策。
//
// 常规路径：按 EventSignature（命中该股的新闻事件时间+标题 + 板块事件）与上一轮签名比对，
// 相同且非待重试 → 复用旧分；变化/缺分/待重试 → 进 toScore 重调 LLM。
//
// 空窗路径：本轮有效事件池为空**不是「该股事件变化」**，而是新闻漏斗的状态缝——
// 时效衰减与重复事件衰减会把最后一条有效事件滤出 0.5 阈值，若照常计算空签名并重评，
// 全池个股都会因「无特定事件」被 noEvent 强制归 0，直到下一条新事件到达才跳回
// （10-12 实录：11:00–13:00 约 300 条候选被 D1=0 拦截，d1 15→0→25、归因文本漂移）。
// 此时整轮沿用上一轮评分，并**保留上一轮签名**（sigNow=nil）：
//   - 只复用上一轮确有事件签名（prevSig 非空）的个股——跨日或从未评分的个股不得借用旧分；
//   - RetryPending 原样保留，使其继续留在重试队列（空池期调 LLM 只会被 noEvent 归 0）；
//   - 无历史评分的个股不调 LLM（无事件可评），保持缺分＝下游按 D1=0 处理。
//
// 下一条新事件到达后，签名仍与「最后一次真实事件集」比对：事件集真正变化才触发重评，
// 不会因为空窗期把签名清空而误判为「新事件」。
// English: incremental D1 planner. An empty valid-event pool is a pipeline state seam, not a
// per-stock event change; reuse the last scores/signatures instead of re-scoring the whole pool
// to zero (the 11:00–13:00 "D1 zero window"). Only codes that had a non-empty signature last
// round are reused; pending retries stay queued; never-scored codes are skipped (D1=0 downstream).
func planD1Round(
	pool []string,
	valid []newsagent.NewsEvent,
	marketData map[string]*strategy_engine.StockMarketData,
	eventMap map[string]string,
	prevScores map[string]combat_agent.D1Score,
	prevSig map[string]string,
) d1RoundPlan {
	plan := d1RoundPlan{
		toScore: make([]string, 0, len(pool)),
		reuse:   make(map[string]combat_agent.D1Score, len(pool)),
	}
	if len(valid) == 0 {
		plan.empty = true
		for _, code := range pool {
			prev, ok := prevScores[code]
			if ok && prevSig[code] != "" {
				plan.reuse[code] = prev
			}
		}
		return plan
	}

	plan.sigNow = make(map[string]string, len(pool))
	for _, code := range pool {
		sig := combat_agent.EventSignature(code, marketData[code], valid, eventMap[code])
		plan.sigNow[code] = sig
		if prev, ok := prevScores[code]; ok && !prev.RetryPending && prevSig[code] == sig {
			plan.reuse[code] = prev
			continue
		}
		plan.toScore = append(plan.toScore, code)
	}
	return plan
}
