// dispatch_live.go — 实盘买入分发入口从 engine.go 拆出的独立文件（2026-10-26，§ENGINE-SPLIT W2）。
//
// 行为零改动：dispatchLive/dispatchLiveSource 原样搬出；liveDecisionKey 等读侧辅助仍留在
// engine.go（被消息标注与测试共用）。执行层 autoPlaceScaled 未搬——它被门禁按文件/函数名锁定。
// English: verbatim move of the live dispatch entry; execution stays in engine.go (gate-locked).
package engine

import (
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

// dispatchLive 实盘交易分发唯一入口（§SIGNAL_CONTROLLER 编排收口）：把本轮做多买入信号集交给
// 信号控制器 live 通道裁定，pass 私送 autoPlace（纯执行守卫），hold/block 仅留痕（消息中心
// 照常记录翻转信号并标注原因——消息与下单解耦，裁决④）。
//   - prune=true：按本轮活跃集清理消失探针（近实时 5s 循环的全量喂入方）；
//   - prune=false：只推进不清理（主循环等子集喂入方，清理权归全量方，防跨喂入方误清探针）。
//
// English: the single live dispatch entry — Evaluate on the live channel, autoPlace the passed
// buys; hold/block are annotated into message decisions. Only the full-feed caller prunes probes.
func (e *Engine) dispatchLive(buys []combat_agent.Signal, live map[string]*data.StockInfo, prune bool, now time.Time) {
	// 5 秒近实时轮与测试默认走这里：计算照常，但**不消费**当日唯一影子读数行。
	// English: the 5s realtime loop (and tests) use this entry — it never consumes the daily
	// shadow readout slot; only the main loop is allowed to write it (§A1).
	e.dispatchLiveSource(buys, live, prune, now, false)
}

// dispatchLiveSource 与 dispatchLive 同语义，额外声明调用方是否允许消费当日影子读数。
// allowShadowReadout=true 只允许主循环传入；5 秒轮传 false，避免它抢先烧掉每日唯一一行。
// English: same admission path with an explicit "may consume the daily shadow readout" flag;
// only the main cycle passes true, so the 5s loop cannot burn the once-per-day line.
func (e *Engine) dispatchLiveSource(buys []combat_agent.Signal, live map[string]*data.StockInfo, prune bool, now time.Time, allowShadowReadout bool) {
	if len(buys) == 0 {
		return
	}
	// §DYN-SIZE P3：把信号质量归一化权重解析到每条买入信号（Enhance.DynWeight 关闭时零操作）。
	// 放在准入之前——qualityFactor 读的就是 Signal.QualityWeight。
	e.annotateQualityWeights(buys)
	// §FLOW-FACTOR：把两个净流入率（当日/5 日）挂到买入信号（两侧开关都关时零操作）。
	// 同位置准入之前——signalctl 的 flow5DFactor/flowTodayFactor 读的就是这两个字段。
	e.annotateFlowFactors(buys, live)
	ctl := e.SignalCtl()
	acct := e.primaryMember()
	pol := e.liveSignalPolicy()
	// §A1 影子读数来源闸：只有主循环允许写当日唯一一行（含 enabled=false 的基线读数）。
	if allowShadowReadout {
		e.regimeShadowReadout(pol.Regime, buys)
	}
	var decisions []signalctl.Decision
	if prune {
		decisions = ctl.Evaluate(signalctl.ChannelLive, acct, buys, pol, now)
	} else {
		decisions = make([]signalctl.Decision, 0, len(buys))
		for _, s := range buys {
			decisions = append(decisions, ctl.Admit(signalctl.ChannelLive, acct, s, pol, now))
		}
	}
	e.mu.Lock()
	if e.liveDecisions == nil {
		e.liveDecisions = map[string]signalctl.Decision{}
	}
	for i, d := range decisions {
		if d.Verdict != signalctl.VerdictPass || d.Shadow {
			e.liveDecisions[liveDecisionKey(buys[i].Code, d.SKey)] = d
		} else {
			delete(e.liveDecisions, liveDecisionKey(buys[i].Code, d.SKey))
		}
	}
	e.mu.Unlock()
	// §SIZING-EVO R1：把本轮定权结果落结构化快照（含灰度候选的 shadow 照算）。
	// 放在执行之前——执行层失败（资金不足/拒单）不影响"当时定权多少"这个观测事实。
	e.evoRecordDecisions(pol, decisions, buys, "live", acct, now)
	// §DYN-SIZE：把控制器的缩放系数透传到执行层（0=未定权，执行层按基准金额）。
	sizingOn := pol.Regime.SizeCfg.EnabledOn()
	for i, d := range decisions {
		if d.Verdict != signalctl.VerdictPass {
			continue
		}
		e.autoPlaceScaled(buys[i], live, d.BuyScale, sizingOn, pol.Regime.SizeCfg)
	}
}
