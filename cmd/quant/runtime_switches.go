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
	"strings"

	"quant-trading-v2/internal/config"
)

// feedEndpoint 一次性取出行情 feed 的**网关地址与钥匙**，两者必须同源。
//
// 为什么地址也要跟着取值（2026-11-01 现网实测的阻塞）：旧写法只从在册快照取钥匙，
// 地址仍固定吃全局 `rules.qmt.gateway_url`。现网全局值是**本机公网 IP** `http://81.71.69.17:8789`，
// 而腾讯云那层是 NAT，实例内部访问自己的公网 IP 直接超时（同机实测：127.0.0.1 秒回 200、
// 公网 IP 6 秒超时）；各账号快照里落的是 `http://127.0.0.1:8789`。于是"出厂开"的 feed
// 会永远连不上，交易链照旧（控制器按账号自己的 gateway_url 走），只有行情这条腿静默失效。
// 一把钥匙属于哪个账号，就该用那个账号登记的网关地址——地址与钥匙分家取，等于自己造 401/超时。
//
// 取值顺序（三条，与 §TOKEN-PERAACCT 一致，绝不借别人的钥匙）：
//  1. 运营账号**自己落过盘**的在册快照：token+gateway_url 成对取；
//  2. 快照有钥匙但没登记地址（历史形态）→ 地址回退全局位，钥匙不回退；
//  3. 全局兜底只可能出现在本地/测试环境（生产全局位按 N1 为空），命中时单独打一条。
//     拿不到钥匙 ⇒ 返回空 token，调用方必须**显式停用**并打状态日志，不许硬启一个每轮失败的循环。
//
// English: resolve the feed's gateway URL and key **from the same holder** — the global URL on this
// box is the instance's own public IP, which is unreachable from inside a Tencent Cloud NAT, so a
// key taken from the operator snapshot while the address came from the global section could never
// connect. No borrowing another account's key; no key means an explicit stop, not a silent retry loop.
func feedEndpoint(cfgMgr *config.Manager) (url, token, from string) {
	global := cfgMgr.Get().QMT
	if q, stored := cfgMgr.StoredQMTConfigFor(cfgMgr.OperatorID()); stored && q.Token != "" {
		url = global.GatewayURL
		if strings.TrimSpace(q.GatewayURL) != "" {
			url = strings.TrimSpace(q.GatewayURL)
		} else {
			// 快照只落了钥匙、地址仍在历史全局位：地址可以回退，钥匙绝不因此换成别人的。
			log.Printf("[main] §SUBSCRIPTION-A 运营账号在册快照未登记 gateway_url，行情 feed 地址回退全局位（钥匙仍用自己的在册快照）")
		}
		return url, q.Token, "运营账号在册快照"
	}
	if g := cfgMgr.Get().QMT; g.Token != "" {
		// 全局位兜底只对本机/联调有意义：现网这里是空串（N1），命中这行说明配置回到了旧扩散形态，
		// 必须看得见——所以它单独打一条，而不是静默当正常路径。
		log.Printf("[main] §SUBSCRIPTION-A 行情 feed 用了全局 rules.qmt.token 兜底（运营账号快照没有在册钥匙；" +
			"生产不该出现这条，见 AGENTS 的凭证三条）")
		return strings.TrimSpace(g.GatewayURL), g.Token, "全局兜底"
	}
	return strings.TrimSpace(global.GatewayURL), "", "无可用钥匙"
}

// feedCredential 只取钥匙（保留给既有测试与调用点；语义见 feedEndpoint）。
// Deprecated: 装配点请改用 feedEndpoint，钥匙与地址必须同源。
func feedCredential(cfgMgr *config.Manager) (token, from string) {
	_, token, from = feedEndpoint(cfgMgr)
	return token, from
}

// logRuntimeSwitches 启动时打印本批全部相关开关的**生效**读数（不是原始配置值）。
// 行情四键（feed 总开关/主源档/逐票源标记/咨询走 hub）自 2026-11-01 owner 改判起是**出厂即开**
// （指针位：config 里没有这个键＝开，显式写 false 才关）；申购识别与撤单护栏仍默认关，
// 等 11-02 盘中探针与段表校准。清单逐键点名正是为了区分这两类：
// "上了但没开"看得见，"出厂就开"也不必靠人记。
// English: prints the effective value of every switch introduced in this batch at boot. The four
// quote-path keys are factory-on (pointer bool: absent = on); the subscription pair stays off until
// the in-market probe and segment-table calibration land.
func logRuntimeSwitches(cfgMgr *config.Manager) {
	rt := cfgMgr.Get().Runtime
	qm := cfgMgr.GetQMTConfigFor(cfgMgr.OperatorID())
	rec, guard := false, false
	if qm != nil {
		rec, guard = qm.SubscriptionRecognize, qm.SubscriptionCancelGuard
	}
	log.Printf("[main] §SWITCH 状态 1/11 runtime.qmt_feed_enabled=%s（出厂开：配置无此键即轮询，显式 false 才停）", yesNo(rt.QMTFeedOn()))
	log.Printf("[main] §SWITCH 状态 2/11 runtime.qmt_feed_primary=%s（出厂开；关＝观察模式：只统计不改写价格；开＝券商价为当前价）", yesNo(rt.QMTFeedPrimaryOn()))
	log.Printf("[main] §SWITCH 状态 3/11 runtime.qmt_feed_coverage_min_pct=%d（出厂 80；-1＝关闭阈值判断＝旧行为）",
		rt.EffectiveFeedCoverageMinPct())
	log.Printf("[main] §SWITCH 状态 4/11 runtime.qmt_feed_volume_to_shares=%.0f（必须由盘中探针判读固化；错 100 倍即 VWAP 错 100 倍）",
		rt.EffectiveFeedVolumeToShares())
	log.Printf("[main] §SWITCH 状态 5/11 runtime.monitor_pool_hot_max=%d（券商主源档；降级档仍是常数 60）",
		rt.EffectiveMonitorPoolHotMax())
	log.Printf("[main] §SWITCH 状态 6/11 rules.qmt.subscription_recognize=%s（关＝申购行仍不可见、unknown 计数仍被注水）", yesNo(rec))
	log.Printf("[main] §SWITCH 状态 7/11 rules.qmt.subscription_cancel_guard=%s（关＝紧急停止与今天一致，全发撤单）", yesNo(guard))
	log.Printf("[main] §SWITCH 状态 8/11 runtime.quote_per_code_source=%s（出厂开；关＝快照里不带每票来源与时刻，混源时整份 Source 仍是唯一读数）", yesNo(rt.QuotePerCodeSourceOn()))
	log.Printf("[main] §SWITCH 状态 9/11 runtime.quote_hub_consult=%s（出厂开；关＝咨询与自选补价仍每次现拉外部源）", yesNo(rt.QuoteHubConsultOn()))
	log.Printf("[main] §SWITCH 状态 10/11 网关侧 subscription_recognize 由 qmt_gateway/config.xt.json 自持，" +
		"现值看网关 /admin/status 的 orders_sync.subscription_recognize（两个键都要开才见效；只开一边最坏是少标注）")
	// §ORDER-STATE-PULL-STALE（2026-11-02）：跨交易日在途行的拉取收敛。缺省**关**是刻意的——
	// 这条腿的方向是"放额度"，误判就是多买一笔，必须 owner 在场按下；
	// 但它同时是"两笔 10-30 的「撤单中」为什么挂到今天"的成因位，所以清单必须点名这一态，
	// 否则"关着"和"没这个功能"在启动日志里又长成同一个样子（§关态留痕 同一条纪律）。
	staleOn := qm != nil && qm.OrderStatePullStaleOn()
	log.Printf("[main] §SWITCH 状态 11/11 rules.qmt.order_state_pull_stale=%s（关＝只收当日在途行，跨日非终态永远不收敛；开＝按网关该日账补终态/判废，三条安全边界同阶段二）",
		yesNo(staleOn))
}

// yesNo 把布尔开关打成可读文本（清单里用 enabled/disabled，不用 true/false：
// 这份日志是给值班的人看的，"true" 说的是配置，"enabled" 说的是行为）。
func yesNo(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}
