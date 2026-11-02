// handlers_runtime_config.go — §SUBSCRIPTION-A（2026-11-01 裁决 4/12）runtime 段行情开关的读写面。
//
// 为什么必须开这条通道：owner 要求覆盖率阈值、量纲系数、热点池上限"后台 + 前端可配"。
// 此前 `rules.runtime.*` 根本没有写面（`feed_interval_sec`、`qmt_feed_enabled` 也只能改
// config.json），于是"改一个数"等于"改配置文件 + 重启进程"——那是灰度阶梯里最慢的一格，
// 也让现网读数与仓库里的默认值长期两张皮。
//
// 三条实现取向（都是本仓踩过的位置）：
//  1. **稀疏 merge**：请求体里没出现的键保留旧值。旧写法（typed struct 整体解码后保存）会把
//     没传的键折成零值——那正是"保存一个开关顺手把金额帽清零"那一族缺陷的成因。
//     这里用指针域表达"传没传"，非法值直接 400 且不落盘。
//  2. **写前快照 + 字段级 diff 审计**：与 d1/qmt/strategy 同一单入口（"runtime" 必须登记进
//     configSurfaces，否则每次改配置都留不下回滚点，还会刷一条 P1）。
//  3. **如实区分热更与重启**：primary / 覆盖率 / 量纲 / 池上限 走 `Fetcher.SetFeedPolicy`
//     即时生效；而 `qmt_feed_enabled` 决定的是启动时到底开不开那条轮询 goroutine，
//     保存成功也必须回答"下一次重启才生效"——把"已保存"说成"已生效"是本仓反复出事的地方。
//
// English: read/write surface for the runtime quote-feed switches — sparse merge, pre-write
// snapshot plus field-level audit, and an honest restart_required list instead of "saved = live".
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/opslog"
)

// runtimeConfigPatch 是本端点**允许**写的键集合（白名单，不是整段 runtime）。
// 其余 runtime 字段（内存释放、扫描节拍等）继续走配置文件：放开它们等于给一个行情开关通道
// 顺带塞进任意全局参数，权限位只有 config_admin 一档，粒度不够。
type runtimeConfigPatch struct {
	QMTFeedEnabled        *bool    `json:"qmt_feed_enabled"`
	QMTFeedPrimary        *bool    `json:"qmt_feed_primary"`
	QMTFeedCoverageMinPct *int     `json:"qmt_feed_coverage_min_pct"`
	QMTFeedVolumeToShares *float64 `json:"qmt_feed_volume_to_shares"`
	MonitorPoolHotMax     *int     `json:"monitor_pool_hot_max"`
	QuotePerCodeSource    *bool    `json:"quote_per_code_source"`
	QuoteHubConsult       *bool    `json:"quote_hub_consult"`
	QMTFeedIntervalSec    *int     `json:"qmt_feed_interval_sec"`
	// §INDEX-BLIND（2026-11-02）大盘指数三枚键。都是"缺键=开"的指针位（feedOn 语义），
	// 这里必须继续用指针而不是裸 bool：裸 bool 的 false 会被 merge 当成"用户显式关了"，
	// 一进卡点保存就把出厂开悄悄改掉——那正是行情四键出厂开时锁过的事故形状。
	IndexFallbackSources  *bool `json:"index_fallback_sources"`
	IndexBreakerScope     *bool `json:"index_breaker_scope"`
	QuoteFeedIndexEnabled *bool `json:"quote_feed_index_enabled"`
}

// runtimeConfigView 下发"原始值 + 生效值"两栏。
// 只给生效值，前端就显示不出"你没填过这个键、现在跑的是出厂值"这件事；
// 只给原始值，0 会被读成"阈值为 0＝谁都达标"，而实际按 80 判——两栏并列是唯一不骗人的写法。
func (s *Server) runtimeConfigView() map[string]interface{} {
	rt := s.cfg.Get().Runtime
	// 轮询 goroutine 在启动时按 enabled/interval 决定是否创建、以什么节拍创建 ⇒ 这两键保存≠生效
	restart := []string{"qmt_feed_enabled", "qmt_feed_interval_sec"}
	view := map[string]interface{}{
		"qmt_feed_enabled":               rt.QMTFeedOn(),
		"qmt_feed_primary":               rt.QMTFeedPrimaryOn(),
		"qmt_feed_coverage_min_pct":      rt.QMTFeedCoverageMinPct,
		"coverage_min_pct_effective":     rt.EffectiveFeedCoverageMinPct(),
		"qmt_feed_volume_to_shares":      rt.QMTFeedVolumeToShares,
		"volume_to_shares_effective":     rt.EffectiveFeedVolumeToShares(),
		"monitor_pool_hot_max":           rt.MonitorPoolHotMax,
		"monitor_pool_hot_max_effective": rt.EffectiveMonitorPoolHotMax(),
		"qmt_feed_interval_sec":          rt.QMTFeedIntervalSec,
		"quote_per_code_source":          rt.QuotePerCodeSourceOn(),
		// 咨询链取价是否优先走 hub：同为指针位，出厂态＝开，必须下发生效值，
		// 直接给 rt.QuoteHubConsult 会让前端在"没填过"时收到 null，开关看起来像坏了。
		"quote_hub_consult": rt.QuoteHubConsultOn(),
		// §INDEX-BLIND 三枚键一律下发**生效值**（指针位原样下发会让"没配过"在前端变成 null，
		// 开关看起来像坏了——§161 那条出厂开语义的等值锁就是为这个形状立的）。
		"index_fallback_sources":   rt.IndexFallbackSourcesOn(),
		"index_breaker_scope":      rt.IndexBreakerScopeOn(),
		"quote_feed_index_enabled": rt.QuoteFeedIndexOn(),
		// 实际会依次试谁（真源 data.IndexLegOrderFor，卡面不许自己列一份）：
		// 现场要能一眼回答"东财断了现在谁给点位"，而不是让人推算开关系没开。
		"index_legs":        data.IndexLegNames(),
		"index_legs_actual": indexLegsActual(rt, s.dc),
		// hot_keys 明示哪些键保存即生效，其余按 restart_required 处理（前端据此提示，不靠人记）
		"hot_keys": []string{"qmt_feed_primary", "qmt_feed_coverage_min_pct", "qmt_feed_volume_to_shares", "monitor_pool_hot_max",
			// 逐票源标记也走热更：它只改快照里的字段，不改任何价格，开与关都不影响在途判定
			"quote_per_code_source", "quote_hub_consult",
			// §INDEX-BLIND：降级链与熔断域都是"每次请求现读"，不需要重启
			"index_fallback_sources", "index_breaker_scope",
			// 券商侧指数白名单走 feed 的策略装配（SetFeedPolicy 即热更），同样不需重启；
			// 现网这条通道此刻不通，按下也不产生价格影响——但键必须可回显，否则"上了没人知道"。
			"quote_feed_index_enabled"},
		"restart_required": restart,
	}
	// 行情通道现状一并回显：设置页要能回答"现在到底连着没有、覆盖率多少"，
	// 而不是让人改完键再去翻日志。
	if s.fetcher != nil {
		view["feed_state"] = s.fetcher.QuoteFeedState()
		st := s.fetcher.FeedStatsNow()
		view["feed_hits"] = st.Hits
		view["feed_denominator"] = st.Denominator
		view["feed_coverage_pct"] = st.CoveragePct
		view["feed_ready_to_promote"] = st.ReadyToPromote
		// 通畅自证三栏：轮询总轮次（0＝启用了却没跑）与量纲判定（未对齐时后端不接管量/额）。
		// 出厂开之后这三栏是"功能如预期上线"的唯一现场答案，缺一个就会退化成"靠日志感觉"。
		view["feed_rounds"] = st.Rounds
		view["feed_volume_observed_unit"] = st.VolumeObservedUnit
		view["feed_volume_aligned"] = st.VolumeAligned
		// 回包只数与连续空回包轮数（2026-11-02 J0 档）：卡上只写"命中 0"分不出病因——
		// 回包非空但都超龄（J2）与一条都没回（J0，上游没有行情服务）处置方向完全不同。
		view["feed_returned"] = st.Returned
		view["feed_empty_rounds"] = st.EmptyRounds
	} else {
		view["feed_state"] = "not_wired" // 采集器未注入（测试/降级装配）：如实说，不折成 ok
	}
	return view
}

// indexLegsActual 本轮**实际**会试的段序（受 index_fallback_sources 控制）。
// 与 index_legs（出厂全集）分两栏是给值班的人看的：开关关掉时"卡上列三条、实际只试一条"
// 这种分家必须当场可见，而不是等出事再推算。
func indexLegsActual(rt config.RuntimeConfig, dc *data.DataCoordinator) []string {
	// 有 Coordinator 就以它的段表为准（券商段只有被注入时才真的在表里）；
	// 没有才回退到"按开关推算"，并如实按推算口径给。
	if dc != nil {
		if got := dc.IndexLegOrder(); len(got) > 0 {
			return got
		}
	}
	// 回退分支同样走 data 的单点段序函数（这里没人注入券商段，故 brokerInjected=false）
	return data.IndexLegOrderFor(rt.IndexFallbackSourcesOn(), false)
}

// handleGetRuntimeConfig GET /api/config/runtime（config_admin）。
func (s *Server) handleGetRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.runtimeConfigView())
}

// validateRuntimePatch 逐键校验，返回错误列表（空＝通过）。
// 非法值一律"不落盘 + 400 + 说明哪个键为什么不行"：夹到边界再保存会让设置页显示的
// 数字和用户原本填的不是一个数，那属于另一种静默。
func validateRuntimePatch(req runtimeConfigPatch) []string {
	errs := []string{}
	if req.QMTFeedCoverageMinPct != nil {
		v := *req.QMTFeedCoverageMinPct
		if v < -1 || v > 100 {
			errs = append(errs, fmt.Sprintf("qmt_feed_coverage_min_pct 只接受 -1..100（-1＝关闭阈值判断＝旧行为，0＝出厂 80），收到 %d", v))
		}
	}
	if req.QMTFeedVolumeToShares != nil {
		v := *req.QMTFeedVolumeToShares
		// 量纲直接决定 VWAP（成交额÷成交量）：错一档就是 100 倍。只放行物理上存在的两档，
		// 其余（含 0 之外的畸形值）拒收，让"配错"在保存这一步就撞墙，而不是在发单时才发现。
		if v != 0 && v != 1 && v != 100 {
			errs = append(errs, fmt.Sprintf("qmt_feed_volume_to_shares 只接受 1（股）或 100（手）或 0（＝出厂 1），收到 %v；这个系数会把 VWAP 整体缩放", v))
		}
	}
	if req.MonitorPoolHotMax != nil {
		v := *req.MonitorPoolHotMax
		if v != 0 && (v < 60 || v > 2000) {
			errs = append(errs, fmt.Sprintf("monitor_pool_hot_max 只接受 0（＝出厂 400）或 60..2000（下限＝降级档常数，上限＝后备链未实测过更宽的池），收到 %d", v))
		}
	}
	if req.QMTFeedIntervalSec != nil {
		v := *req.QMTFeedIntervalSec
		if v < 0 || v > 60 {
			errs = append(errs, fmt.Sprintf("qmt_feed_interval_sec 只接受 0..60（0＝出厂 3s），收到 %d", v))
		}
	}
	return errs
}

// handleSetRuntimeConfig POST /api/config/runtime（config_admin）：稀疏 merge 后落盘 + 热更。
func (s *Server) handleSetRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	var req runtimeConfigPatch
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	if errs := validateRuntimePatch(req); len(errs) > 0 {
		// 一条都不落盘：部分成功比全部失败更难排查（现场会以为是"改了没生效"）
		writeError(w, 400, "§RUNTIME 配置校验失败，本次未落盘："+strings.Join(errs, "; "))
		return
	}
	uid := requestUserID(r)
	trace := s.snapshotConfigWrite(uid, uid, "runtime")

	// 取值拷贝再整体替换：s.cfg.Get() 交出的是活体 rules 指针，就地改字段是绕过锁写
	// （§0925EVE-D1 修的就是这一族），而这个通道管的正是"能不能让券商改写行情价"。
	rt := s.cfg.RuntimeConfigForWrite()
	if req.QMTFeedEnabled != nil {
		rt.QMTFeedEnabled = req.QMTFeedEnabled
	}
	if req.QMTFeedPrimary != nil {
		rt.QMTFeedPrimary = req.QMTFeedPrimary
	}
	if req.QMTFeedCoverageMinPct != nil {
		rt.QMTFeedCoverageMinPct = *req.QMTFeedCoverageMinPct
	}
	if req.QMTFeedVolumeToShares != nil {
		rt.QMTFeedVolumeToShares = *req.QMTFeedVolumeToShares
	}
	if req.MonitorPoolHotMax != nil {
		rt.MonitorPoolHotMax = *req.MonitorPoolHotMax
	}
	if req.QuotePerCodeSource != nil {
		rt.QuotePerCodeSource = req.QuotePerCodeSource
	}
	if req.QuoteHubConsult != nil {
		rt.QuoteHubConsult = req.QuoteHubConsult
	}
	if req.QMTFeedIntervalSec != nil {
		rt.QMTFeedIntervalSec = *req.QMTFeedIntervalSec
	}
	// §INDEX-BLIND 三枚指数键（同样只在"请求里出现过"时才写，稀疏 merge）
	if req.IndexFallbackSources != nil {
		rt.IndexFallbackSources = req.IndexFallbackSources
	}
	if req.IndexBreakerScope != nil {
		rt.IndexBreakerScope = req.IndexBreakerScope
	}
	if req.QuoteFeedIndexEnabled != nil {
		rt.QuoteFeedIndexEnabled = req.QuoteFeedIndexEnabled
	}
	if err := s.cfg.SetRuntimeConfig(&rt); err != nil {
		// 未落盘就不审计"已变更"（否则审计变成假账），并如实回 500
		writeError(w, 500, "runtime 配置保存失败（未落盘）: "+err.Error())
		return
	}
	trace.audit(s)

	// 审计要回答的是"这次到底动了哪几个键"，所以逐键判"指针是否非 nil"（=请求里出现没出现），
	// 而不是把整段配置打进去：后者会把没改的键也写成"变更"，审计立刻退化成噪声。
	changed := make([]string, 0, 11)
	for _, kv := range []struct {
		name string
		set  bool
	}{
		{"qmt_feed_enabled", req.QMTFeedEnabled != nil},
		{"qmt_feed_primary", req.QMTFeedPrimary != nil},
		{"qmt_feed_coverage_min_pct", req.QMTFeedCoverageMinPct != nil},
		{"qmt_feed_volume_to_shares", req.QMTFeedVolumeToShares != nil},
		{"monitor_pool_hot_max", req.MonitorPoolHotMax != nil},
		{"quote_per_code_source", req.QuotePerCodeSource != nil},
		{"quote_hub_consult", req.QuoteHubConsult != nil},
		{"qmt_feed_interval_sec", req.QMTFeedIntervalSec != nil},
		{"index_fallback_sources", req.IndexFallbackSources != nil},
		{"index_breaker_scope", req.IndexBreakerScope != nil},
		{"quote_feed_index_enabled", req.QuoteFeedIndexEnabled != nil},
	} {
		if kv.set {
			changed = append(changed, kv.name)
		}
	}
	opslog.Audit("config_runtime", uid, "runtime",
		fmt.Sprintf("keys=%v snapshot=%s ledger=%s", changed, trace.name, trace.ledger))

	// 热更：策略即时灌给装配层（覆盖率/量纲/池上限/主源档不需要重启）。
	if s.fetcher != nil {
		s.fetcher.SetFeedPolicy(feedPolicyFromRuntime(rt))
		if req.QMTFeedPrimary != nil {
			logSwitchChange(uid, *req.QMTFeedPrimary)
		}
	}
	// §INDEX-BLIND：指数降级链与独立熔断域同样热更（每次取数现读，不需重启）。
	// 走 Coordinator 这一个口注入——与启动装配同一个方法，两处各写一套就会漂。
	if req.IndexFallbackSources != nil || req.IndexBreakerScope != nil {
		submittedFallback := req.IndexFallbackSources != nil
		submittedScope := req.IndexBreakerScope != nil
		opslog.Logf("quant", "§INDEX-BLIND 指数开关热更（本次提交 降级链=%v 熔断域=%v）生效值：降级链=%s 独立熔断域=%s 段序=%v 操作者=%s",
			submittedFallback, submittedScope, switchStateOf(rt.IndexFallbackSourcesOn()), switchStateOf(rt.IndexBreakerScopeOn()),
			indexLegsActual(rt, s.dc), uid)
		s.dc.SetIndexSourcePolicy(rt.IndexFallbackSourcesOn(), rt.IndexBreakerScopeOn())
	}
	resp := s.runtimeConfigView()
	resp["status"] = "ok"
	resp["changed"] = changed
	writeJSON(w, http.StatusOK, resp)
}

// logSwitchChange 把"主源档被谁开/关"额外打一条 INFO：这条是行为面最大的一个开关
// （券商价开始改写快照），值得在日志里单独可 grep，而不只藏在字段级 diff 中。
func logSwitchChange(uid string, on bool) {
	state := "关闭（回到观察模式）"
	if on {
		state = "开启（券商 L1 开始改写行情快照）"
	}
	opslog.Logf("quant", "§SUBSCRIPTION-A qmt_feed_primary=%s 操作者=%s", state, uid)
}

// switchStateOf 把开关生效值打成 enabled/disabled（与启动清单同一口径：
// 日志是给值班的人看的，"true" 说的是配置，"enabled" 说的是行为）。
func switchStateOf(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

// 编译期确保 auth 被引用（路由权限位在 server.go 注册处使用，这里只作文档性引用）
// feedPolicyFromRuntime 把 runtime 配置折成装配层策略（**取值一律走 Effective* 兜底**：
// 存量 config.json 缺这些键时反序列化得 0，0 的含义是"没填"，不是"阈值 0＝谁都达标"）。
// main.go 与热更两条路共用它，避免两处各写一份兜底而漂开。
func feedPolicyFromRuntime(rt config.RuntimeConfig) data.FeedPolicy {
	return data.FeedPolicyFromRuntime(rt)
}
