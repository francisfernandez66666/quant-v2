// intraday_t.go — §T-MODE 做 T 的 engine 侧薄驱动（2026-10-24 owner 裁决四，W6 正 T / W7 反 T）。
//
// 分层：本文件只做"解析账号级配置 → 组装 paper.TParams → 在交易时段内每轮把控制权交给纸面引擎"。
// 真正的编排（前提校验、两腿串行、配对、尾盘强制配平、半态收摊）与记账（回转账、幂等键、
// 订单留痕）全在 `internal/paper/t_rounds.go`，因为那些动作必须与账本同锁、同一次原子落盘。
//
// 为什么不在 engine 侧持状态：回转状态与持仓、层账、残量意图必须同生共死。放到 engine 侧就要
// 复制一套锁与持久化，且天然会漂出"账本已变、回转状态没跟上"的半态——这正是本批要收掉的那类缺陷。
//
// English: engine-side thin driver for §T-MODE. It resolves the account's TConfig, builds paper.TParams
// and hands control to the paper engine once per in-session round. All orchestration and bookkeeping
// live in internal/paper/t_rounds.go because they must share the book's lock and atomic write.
package engine

import (
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
)

// tParamsFor 把账号级 TConfig 解析成纸面引擎的执行参数（出厂阈值由访问器兜底）。
// English: resolves the account's TConfig into the paper engine's execution parameters.
func (e *Engine) tParamsFor(tc config.TConfig) paper.TParams {
	return paper.TParams{
		BuyFirst:      tc.TBuyFirst(),       // 正 T 是否允许（""/"both"/"buy_first"）
		SellFirst:     tc.TSellFirst(),      // 反 T 是否允许（""/"both"/"sell_first"）
		MaxRounds:     tc.TMaxRounds(),      // 单码单日回转上限（默认 1，硬顶 5）
		CapPct:        tc.TIntradayCapPct(), // 机动仓上限（默认 30%）
		MinSpreadPct:  tc.TMinSpreadPct(),   // 最小价差（默认 1.0%）
		ForceUnwindHM: tc.TForceUnwindHM(),  // 尾盘强制配平时刻（默认 1450）
	}
}

// runIntradayT 做 T 的每轮驱动（由纸面估值轮在交易时段内调用）。
//
// 三道前置全部在**拨引擎之前**判：开关没开就一次配置都不往下传，避免纸面引擎每轮空转加锁。
// `data.IsActiveSession` 是第二道——集合竞价与盘后都不做 T：竞价阶段没有可靠的连续价，
// 盘后根本没有对手盘，两个时段里"配对"只是纸面上的自我安慰。
// English: the switch and the session window are checked before touching the engine, so a disabled
// account costs nothing per round. T never runs in the auction or after the close: there is no
// reliable continuous price in the auction and no counterparty after hours.
func (e *Engine) runIntradayT(uid string, pe *paper.Engine, quotes map[string]*data.StockInfo) {
	if pe == nil {
		return
	}
	tc := e.sizingConfigFor(uid).T
	if !tc.TEnabled() {
		return
	}
	if !data.IsActiveSession(time.Now()) {
		return
	}
	pe.RunIntradayT(quotes, e.tParamsFor(tc))
}
