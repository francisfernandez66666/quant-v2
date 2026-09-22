// order_rate.go §DEADGAUGE（2026-09-23 傍晚批收尾）：把告警规则 order_fail_rate 接回真实数据源。
//
// 背景（死规则形态，与 audit N-1 / settlement_diff / llm_cooldown 同族）：规则自 09-15 注册起
// 全仓没有一处给量规 order_fail_rate_milli 赋值 → 评估器每轮读到 0 → p1「下单失败率 >5%
// （连续5分钟）」**永远不会触发**。这不是"没出过事"，是"出了事也不会响"。
//
// 数据源不新建：§R4-9 起 trading.Controller.PlaceOrder 已经在每次下单后调
// metrics.OrdersPlaced()/OrdersRejected() 累计计数（下单链唯一计数口径），本文件只做
// 「累计计数器 → 窗口失败率」的换算：取 5 分钟窗内两端累计值之差，算
// rejectedΔ /(placedΔ+rejectedΔ) ×1000（千分比，与规则 Threshold=50 即 5% 对齐）。
//
// 口径三条（都写进单测锁住）：
//  1. 窗内没有任何下单尝试 → 0。0 在 gt 规则下恒不触发，与 §UPDLINK 的
//     「未知/不适用一律写 0，既不伪造坏也不伪造好」统一口径一致——绝不拿"上一窗的值"冒充本窗。
//  2. 计数器回退（进程重启，累计值变小）→ 丢弃历史样本重新起窗，不产生负数或虚假高失败率。
//  3. 只换算、不改下单链：本文件不 import 交易包，PlaceOrder 的调用点保持原样。
//
// English: §DEADGAUGE — the order_fail_rate alert rule had no gauge assignment anywhere, so the
// p1 "order failure rate >5% for 5 minutes" rule could never fire. This file derives the per-mille
// windowed rate from the existing cumulative §R4-9 counters (placed/rejected), with an empty window
// reading 0 (never trips) and a counter reset re-baselining the window.
package metrics

import (
	"sync"
	"time"
)

// orderFailRateWindow 下单失败率的统计窗，与规则 order_fail_rate 的 For=300s 同尺
// （窗内失败率持续破线才会被评估器的 For 判定放行）。
const orderFailRateWindow = 5 * time.Minute

// orderRateNow 时钟接缝：单测把时间拨快即可验证起窗/滑动/过期，无需 sleep。
var orderRateNow = time.Now

// orderRateSample 一次评估节拍的累计计数器快照。
type orderRateSample struct {
	at       time.Time
	placed   int64
	rejected int64
}

// orderRateMu 保护样本环：评估节拍单线程，但 HTTP /metrics 与单测可并发读，显式加锁。
var (
	orderRateMu sync.Mutex
	orderRate   []orderRateSample
)

// refreshOrderFailRateGauge 由 RunAlertEvaluation 每轮调用：记一次累计值快照，并按 5 分钟窗
// 内的增量刷新量规 order_fail_rate_milli。
//
// 返回窗内失败率千分比（0 = 窗内无尝试，或尝试全部成功）。
func refreshOrderFailRateGauge() int64 {
	placed := ordersPlaced.Load()
	rejected := ordersRejected.Load()
	now := orderRateNow()

	orderRateMu.Lock()
	defer orderRateMu.Unlock()

	// 口径 2：计数器回退 = 进程重启（累计原子量归零后重新累加）。历史端点与新值不可比，
	// 整环作废重新起窗——宁可短窗内少几个样本，也不报出一个假的失败率。
	if n := len(orderRate); n > 0 && (placed < orderRate[n-1].placed || rejected < orderRate[n-1].rejected) {
		orderRate = nil
	}
	orderRate = append(orderRate, orderRateSample{at: now, placed: placed, rejected: rejected})
	// 只留两个窗口长度的样本（内存有界；评估节拍 30s ⇒ 常态 ~20 个样本）。
	cutoff := now.Add(-2 * orderFailRateWindow)
	for len(orderRate) > 2 && orderRate[0].at.Before(cutoff) {
		orderRate = orderRate[1:]
	}

	// 基端 = 「窗口起点之前最近的样本」；若整环都落在窗内（刚起窗/刚重启）则用最旧那个，
	// 此时窗口实际长度不足 5 分钟，失败率按已有这段算——样本环是单调递增的时间序列，
	// 所以从前向后扫到第一个窗内样本即可。
	windowStart := now.Add(-orderFailRateWindow)
	baseIdx := 0
	for i, s := range orderRate {
		if s.at.Before(windowStart) {
			baseIdx = i
		} else {
			break
		}
	}
	if len(orderRate) < 2 || baseIdx == len(orderRate)-1 {
		SetGauge("order_fail_rate_milli", 0) // 口径 1：还没形成可比较的两端
		return 0
	}
	base := orderRate[baseIdx]
	last := orderRate[len(orderRate)-1]
	dPlaced := last.placed - base.placed
	dRejected := last.rejected - base.rejected
	total := dPlaced + dRejected
	if total <= 0 {
		SetGauge("order_fail_rate_milli", 0) // 口径 1：窗内无尝试
		return 0
	}
	rate := dRejected * 1000 / total
	SetGauge("order_fail_rate_milli", rate)
	return rate
}

// resetOrderRateWindow 清空样本环（仅供单测隔离用例，避免相互污染累计状态）。
func resetOrderRateWindow() {
	orderRateMu.Lock()
	defer orderRateMu.Unlock()
	orderRate = nil
}
