// runtime_switches.go — §SUBSCRIPTION-A/B（2026-11-01）行情与申购相关的运行时凭据取值与开关清单。
//
// 两件事都放在这里而不是散在 main 里，理由很实际：
//  1. **凭据取值**是这批唯一"错了不报错"的地方。旧实现只吃全局 rules.qmt.token，而全局位在
//     10-30 那批被清空（全局不留实盘凭证）⇒ 照旧写法开开关，得到的是每 3s 一次 401、
//     feed 永不连接、行情照旧走外部源、日志 60 秒一条——静默无效。取值顺序必须写在一处、
//     看得见，并且拿不到钥匙时**显式停用**而不是硬启一个连不上的循环。
//  2. 追加裁决「开关关着也要留痕」：启动时打一份逐键状态清单，让"这里有个功能、它没开"
//     当场可读，而不是靠谁记住这批上了哪些键。新增开关必须同批进这份清单
//     （门禁 §162 按条数等值锁——加了键漏了清单，就是"没人知道有这个东西"的复发形态）。
package main

import (
	"log"

	"quant-trading-v2/internal/config"
)

// feedCredential 取行情 feed 用的钥匙：优先运营账号**自己落过盘**的在册快照钥匙，
// 其次才是全局位（只可能出现在本地/测试环境——生产的全局位按 N1 不该有值）。
// 返回的 from 串会进启动日志，让"这把钥匙到底从哪来"变成一个读数而不是考古题。
// English: the operator's own stored snapshot key first, global only as a local/test fallback;
// the provenance is logged so credential questions are answered by a reading, not by archaeology.
func feedCredential(cfgMgr *config.Manager) (token, from string) {
	if q, stored := cfgMgr.StoredQMTConfigFor(cfgMgr.OperatorID()); stored && q.Token != "" {
		return q.Token, "运营账号在册快照"
	}
	if g := cfgMgr.Get().QMT; g.Token != "" {
		// 全局位兜底只对本机/联调有意义：现网这里是空串（N1），命中这行说明配置回到了旧扩散形态，
		// 必须看得见——所以它单独打一条，而不是静默当正常路径。
		log.Printf("[main] §SUBSCRIPTION-A 行情 feed 用了全局 rules.qmt.token 兜底（运营账号快照没有在册钥匙；" +
			"生产不该出现这条，见 AGENTS 的凭证三条）")
		return g.Token, "全局兜底"
	}
	return "", "无可用钥匙"
}

// logRuntimeSwitches 启动时打印本批全部相关开关的**生效**读数（不是原始配置值）。
// 缺省全关＝今天的字节行为；这份清单是"上了但没开"与"开了但没上"的唯一现场答案。
// English: prints the effective value of every switch introduced in this batch at boot, so
// "shipped but not enabled" is visible without archaeology.
func logRuntimeSwitches(cfgMgr *config.Manager) {
	rt := cfgMgr.Get().Runtime
	qm := cfgMgr.GetQMTConfigFor(cfgMgr.OperatorID())
	rec, guard := false, false
	if qm != nil {
		rec, guard = qm.SubscriptionRecognize, qm.SubscriptionCancelGuard
	}
	log.Printf("[main] §SWITCH 状态 1/8 runtime.qmt_feed_enabled=%s", yesNo(rt.QMTFeedEnabled))
	log.Printf("[main] §SWITCH 状态 2/8 runtime.qmt_feed_primary=%s（关＝观察模式：只统计不改写价格）", yesNo(rt.QMTFeedPrimary))
	log.Printf("[main] §SWITCH 状态 3/8 runtime.qmt_feed_coverage_min_pct=%d（出厂 80；-1＝关闭阈值判断＝旧行为）",
		rt.EffectiveFeedCoverageMinPct())
	log.Printf("[main] §SWITCH 状态 4/8 runtime.qmt_feed_volume_to_shares=%.0f（必须由盘中探针判读固化；错 100 倍即 VWAP 错 100 倍）",
		rt.EffectiveFeedVolumeToShares())
	log.Printf("[main] §SWITCH 状态 5/8 runtime.monitor_pool_hot_max=%d（券商主源档；降级档仍是常数 60）",
		rt.EffectiveMonitorPoolHotMax())
	log.Printf("[main] §SWITCH 状态 6/8 rules.qmt.subscription_recognize=%s（关＝申购行仍不可见、unknown 计数仍被注水）", yesNo(rec))
	log.Printf("[main] §SWITCH 状态 7/8 rules.qmt.subscription_cancel_guard=%s（关＝紧急停止与今天一致，全发撤单）", yesNo(guard))
	log.Printf("[main] §SWITCH 状态 8/8 网关侧 subscription_recognize 由 qmt_gateway/config.xt.json 自持，" +
		"现值看网关 /admin/status 的 orders_sync.subscription_recognize（两个键都要开才见效；只开一边最坏是少标注）")
}

// yesNo 把布尔开关打成可读文本（清单里用 enabled/disabled，不用 true/false：
// 这份日志是给值班的人看的，"true" 说的是配置，"enabled" 说的是行为）。
func yesNo(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}
