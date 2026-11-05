// carrier_shadow.go — §CARRIER-SHADOW（2026-11-05 载体影子对照批）只读对照面。
//
// 这层回答的是一句话：**载体那套判断与引擎离得多近，5 日窗口走到第几天了**。
// 背景（docs/IMPL_QMT_INTERNAL_LOOP_20261103.md）：每枚移植的 P1 规则必须先与 Go 引擎
// 对照跑满 ≥5 个交易日才准碰钱。以前 shadow 命中只是执行器本地一行日志——不落盘、不上行、
// 广州侧没有任何读面能回答这个问题，于是那枚"武装"按钮背后是零数据。
//
// 四条不许被顺手优化掉的边界：
//  1. **三种零分开**（本文件存在的理由，分拣处只有 carrierShadowStateOf 这一处）：
//     · `no_report` 该账号那天**一个字节都没报**（盒子没跑/断链/上行腿静默）⇒ 这一天**不计入窗口**；
//     · `no_hits`   那天报了（别的种类有行），只是这一类没命中 ⇒ 真实的一"零"，**计入窗口**；
//     · `diverged`  这一类有命中、引擎同日同码一条都不认 ⇒ 分歧，不是缺数据。
//     折向任何一边都是拿"不知道"冒充"测过了"：把静默算成一天，5 天会自己凑满；
//     把零命中算成分歧，owner 会去查一条本来一致的腿。
//  2. **口径不对称要写出来，不许假装对称**：`signal_decisions` 只承载**买入定权**那条路径
//     （引擎只在动态仓位计算处写），卖出类影子决策只能靠当天该账号的委托行确认。
//     所以判"引擎认了"＝同日该账号有该码的买入定权快照 **或** 同日该账号有该码同方向的委托行；
//     卖出侧永远只有后者，读面上的 agreed 数因此天生偏保守——宁可少认一条，不误认一条。
//  3. **纯观测腿**（§OBS-GUARD 同取向）：只读三张表，不参与下单、不参与 /health、不参与熔断；
//     任何一天读不出就整表判 known=false + 点名原因，绝不返回一个空表让人读成"今天没有影子决策"
//     （missing≠empty；§STARVE-STALE 的 J0"零回包不算健康"是同一族）。
//  4. **账号内比较**：引擎侧只认 `user_id == 请求者` 的委托行——`user_id=”` 的遗留行按运营账号
//     归属（§FIX-20261028-F01），拿别人的成交给这根腿记一次"引擎同意"是把两个账号的账并成一页。
//
// 接线姿势：与 leg_status / carrier 同规——**纯增量**并进 GET /api/qmt/state 的一枚新键
// `carrier_shadow`，既有字段逐字节不变。为什么不新开一条 GET /api/qmt/carrier-shadow：
// 本批被划给别的 agent 的 scripts/contract_ledger.tsv 与 verify_changes.sh（§115 五组派生计数、
// §144 严格路由计数）都挂在"新增路由必须同批改台账/计数"那条锁上——台账改不了的路由就是红的门禁；
// 并进 /state 拿到的是同一份数据，又不动别人的文件。窗口天数用查询串 `?shadow_days=N` 表达。
//
// English: read-only comparison between the carrier's shadow decisions and the engine, per rule
// kind and per Beijing trading day, keeping the three kinds of "zero" apart (no report /
// reported-but-no-hit / disagreed). Surfaced additively as one new key on GET /api/qmt/state
// instead of a new route, because the contract ledger a new route must be registered in is
// owned by another agent in this batch.
package server

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/data"
)

const (
	// carrierShadowRequiredDays 计划文档要求的最小对照交易日数（≥5 天才准武装）。
	// 写在这里而不是从配置读：它是**验收口径**不是调参旋钮，改它等于改发版纪律。
	carrierShadowRequiredDays = 5
	// carrierShadowDefaultDays 缺省回看窗口（交易日）：留出"5 天已满但今天没跑"的余量。
	carrierShadowDefaultDays = 10
	// carrierShadowMaxDays 窗口上限：读面按天各查一次定权快照，窗口越长越贵，30 足够看趋势。
	carrierShadowMaxDays = 30
	// carrierShadowMaxScan 往回找交易日时最多翻多少自然日（长假 + 周末的兜底上限，防无限循环）。
	carrierShadowMaxScan = 90
	// carrierShadowMaxRows 单次上行事件的行数上限：超出整条拒收（毒丸进死信可见，不静默截断）。
	carrierShadowMaxRows = 500
)

// 一格 (种类, 交易日) 的四种状态（前端按值渲染，改值必须同批改 web/src/utils/carrierShadow.js）。
const (
	// carrierShadowStateAgreeing 有命中且引擎同日同码至少认了一条。
	carrierShadowStateAgreeing = "agreeing"
	// carrierShadowStateDiverged 有命中但引擎一条都不认＝分歧（不是缺数据）。
	carrierShadowStateDiverged = "diverged"
	// carrierShadowStateNoHits 这天报了（别的种类有行）、这一类零命中＝真实的一"零"，计入窗口。
	carrierShadowStateNoHits = "no_hits"
	// carrierShadowStateNoReport 这天整根腿没报＝读数未知，**不计入窗口**。
	carrierShadowStateNoReport = "no_report"
)

// carrierShadowDay 一格 (种类, 交易日) 的对照结果。
type carrierShadowDay struct {
	Day          string `json:"day"`
	State        string `json:"state"`
	CarrierRows  int    `json:"carrier_rows"`
	EngineAgreed int    `json:"engine_agreed"`
	// ReportedRows 该账号当天上报的**总**行数（跨种类）：no_report 与 no_hits 的分界读数。
	// 把它显示出来，用户就不必相信我们那句"这天盒子是静默的"——他能自己看见分母。
	ReportedRows int `json:"reported_rows"`
}

// carrierShadowKind 一枚规则种类的窗口汇总——owner 那枚武装按钮要看的就是这几列。
type carrierShadowKind struct {
	Kind string `json:"kind"`
	// DaysWithData 这一类**有命中**的天数＝5 日窗口的分子（no_report/no_hits 都不算）。
	DaysWithData int `json:"days_with_data"`
	DaysNoHits   int `json:"days_no_hits"`
	DaysNoReport int `json:"days_no_report"`
	CarrierRows  int `json:"carrier_rows"`
	EngineAgreed int `json:"engine_agreed"`
	RequiredDays int `json:"required_days"`
	// WindowFilled 窗口是否凑够 required_days 个**有命中**的交易日（不是"日历上过了 5 天"）。
	WindowFilled bool               `json:"window_filled"`
	Days         []carrierShadowDay `json:"days"`
}

// carrierShadowView GET /api/qmt/state 的 carrier_shadow 键。
type carrierShadowView struct {
	// Known false＝这份读数**构建不出来**（身份缺失/账本没接/任一日的查询报错/窗口定不出来）。
	// 前端此时只能显示"读数未知"——绝不许显示 0 或空表当"今天没有影子决策"。
	Known  bool   `json:"known"`
	Reason string `json:"reason,omitempty"`
	// AsOfDay 窗口末端那一天（YYYYMMDD，北京时间）：窗口是"截至这一天的最近 N 个交易日"。
	AsOfDay    string   `json:"as_of_day"`
	WindowDays int      `json:"window_days"`
	Window     []string `json:"window"`
	// DaysReported 窗口内该账号**有上报**的天数（跨种类）：整根腿是不是在报数，看这一列。
	DaysReported int                 `json:"days_reported"`
	RequiredDays int                 `json:"required_days"`
	Kinds        []carrierShadowKind `json:"kinds"`
	UpdatedAt    string              `json:"updated_at"`
}

// carrierShadowPureCodePat 6 位代码前缀（orders / signal_decisions 两张表的代码形态混着带与不带后缀）。
var carrierShadowPureCodePat = regexp.MustCompile(`^(\d{6})`)

// carrierShadowPureCode 取 6 位纯码（与引擎写 signal_decisions 时的 pureTsCode 同口径）。
// 认不出来返回空串——调用方按"无从对照"处理，绝不拿整串去比：后缀一漂就永远不命中，
// agreed 恒 0，看起来像"载体与引擎不一致"，实际是我们自己对不上自己的列。
func carrierShadowPureCode(code string) string {
	s := strings.TrimSpace(code)
	if s == "" {
		return ""
	}
	if m := carrierShadowPureCodePat.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// carrierShadowSideWord 把载体侧 buy/sell 映射到委托表那两词；第三种取值返回空串（不参与对照）。
// 为什么不做"默认按买入"那类兜底：方向猜错就是把卖出当成买入——§安全 T3 在回报侧拒的就是
// 同一件事，观测腿也不该用另一种口径。
func carrierShadowSideWord(side string) string {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy", "b":
		return "买入"
	case "sell", "s":
		return "卖出"
	default:
		return ""
	}
}

// carrierShadowTradingDays 截至 now（含）的最近 n 个交易日（新→旧）。
// 用 data.IsTradingDay 而不是"减 N 天"：窗口要的是**交易日**数，周末与节假日不该吃掉配额
// （否则长假后第一天就报"窗口只有 3 天"，那是拿日历形状冒充数据缺失）。
// 翻不到 n 天就用翻到的，最多翻 carrierShadowMaxScan 自然日。
func carrierShadowTradingDays(now time.Time, n int) []string {
	if n <= 0 {
		n = carrierShadowDefaultDays
	}
	out := make([]string, 0, n)
	for back := 0; back < carrierShadowMaxScan && len(out) < n; back++ {
		day := now.AddDate(0, 0, -back)
		if data.IsTradingDay(day) {
			out = append(out, cntime.DayCompactOf(day))
		}
	}
	return out
}

// parseCarrierShadowDays 读 ?shadow_days=N（缺省 10、上限 30、非法值回落缺省而不是报错——
// 这是读面宽度旋钮，不是判据；把 400 打在"页面刷新带了个怪参数"上没有意义）。
func parseCarrierShadowDays(r *http.Request) int {
	if r == nil {
		return carrierShadowDefaultDays
	}
	raw := strings.TrimSpace(r.URL.Query().Get("shadow_days"))
	if raw == "" {
		return carrierShadowDefaultDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return carrierShadowDefaultDays
	}
	if n > carrierShadowMaxDays {
		return carrierShadowMaxDays
	}
	return n
}

// engineShadowSet 一天里引擎侧"认过"的两把尺子（口径不对称见文件头边界 2）。
type engineShadowSet struct {
	// decisionCodes 当日该账号买入定权快照里的纯码集合。
	decisionCodes map[string]bool
	// orderKeys 当日该账号委托行的 `方向|纯码` 集合（卖出侧唯一的一把尺子）。
	orderKeys map[string]bool
}

func newEngineShadowSet() engineShadowSet {
	return engineShadowSet{decisionCodes: map[string]bool{}, orderKeys: map[string]bool{}}
}

// agrees 单行判定：买入行两把尺子（定权快照或当日买入委托），卖出行只有委托那一把。
// 方向认不出 / 代码认不出 ⇒ false（宁可少认一条，不误认一条）。
func (e engineShadowSet) agrees(sideWord, pureCode string) bool {
	if sideWord == "" || pureCode == "" {
		return false
	}
	if e.orderKeys[sideWord+"|"+pureCode] {
		return true
	}
	return sideWord == "买入" && e.decisionCodes[pureCode]
}

// carrierShadowReadout 组装对照读数（handleQMTState 调用；只读，不写任何东西）。
//
// 出口全部返回 Known=false 而不是空表：uid 空、账本没接、任一日的任一查询报错、
// 窗口一个交易日都定不出来。把一次查询失败折成"零行"是本仓反复出现的同一种错
// （§INDEX-BLIND 把"关键读数缺失"点名的原因、§STARVE-STALE 的 J0）。
func (s *Server) carrierShadowReadout(uid string, days int, now time.Time) *carrierShadowView {
	view := &carrierShadowView{
		Known: false, AsOfDay: cntime.DayCompactOf(now), WindowDays: days,
		RequiredDays: carrierShadowRequiredDays, UpdatedAt: now.Format(time.RFC3339),
		Window: []string{}, Kinds: []carrierShadowKind{},
	}
	unknown := func(reason string) *carrierShadowView {
		view.Reason = reason
		view.Known = false
		view.Kinds = []carrierShadowKind{}
		view.Window = []string{}
		return view
	}
	if strings.TrimSpace(uid) == "" {
		return unknown("归属账号缺失：影子对照必须按账号比对，读面无从定身份")
	}
	db := s.realDB()
	if db == nil {
		return unknown("实盘账本未接入：carrier_shadow_decisions 与引擎两侧都读不出")
	}
	window := carrierShadowTradingDays(now, days)
	if len(window) == 0 {
		return unknown("交易日历定不出窗口内的任何一个交易日（窗口读数缺失）")
	}
	view.Window = window
	view.AsOfDay = window[0]
	from, to := window[len(window)-1], window[0]

	// 两张影子分账：按日（这天上报过没有）与按 (种类,日)（这一类命中几条）。
	// 两张都要、且各自只报自己那半事实——那正是文件头边界 1 的三种零的数据面依据。
	dayCounts, err := db.CarrierShadowDayCounts(uid, from, to)
	if err != nil {
		return unknown("按日影子分账读取失败：" + err.Error())
	}
	kindCounts, err := db.CarrierShadowKindDayCounts(uid, from, to)
	if err != nil {
		return unknown("按种类影子分账读取失败：" + err.Error())
	}
	reportedDays := 0
	for _, n := range dayCounts {
		if n > 0 {
			reportedDays++
		}
	}
	view.DaysReported = reportedDays

	// 委托行**整表取一次**再按日切（RealOrdersForUser 本就是全账号当日+历史的一次读，
	// 逐日各查十次是同一次扫描做十遍，还会在中途读到不一致的账）。
	ordersByDay := map[string]map[string]bool{}
	orders, err := db.RealOrdersForUser(uid)
	if err != nil {
		return unknown("当日委托表读取失败：" + err.Error())
	}
	for _, o := range orders {
		// 只认请求者自己的行：遗留 user_id='' 归运营（§FIX-20261028-F01），
		// 拿别人的成交给这根腿记一次"引擎同意"是把两个账号并成一页。
		if o.UserID != uid {
			continue
		}
		pure := carrierShadowPureCode(o.Code)
		side := strings.TrimSpace(o.Side)
		if pure == "" || side == "" || len(o.CreatedAt) < 10 {
			continue
		}
		// created_at 是北京时间 "2026-11-05 09:31:00"；窗口日 YYYYMMDD ⇒ 先折成同形态再进 map。
		day := strings.ReplaceAll(o.CreatedAt[:10], "-", "")
		if ordersByDay[day] == nil {
			ordersByDay[day] = map[string]bool{}
		}
		ordersByDay[day][side+"|"+pure] = true
	}

	// 逐日算一份"这一类今天命中几行、引擎认了几行"，种类循环只读缓存
	// （以前按 种类×日 各查两遍库：十类十天就是四十次扫描，读面比写面还贵）。
	type kindFacts struct{ rows, agreed int }
	perDay := make(map[string]map[string]kindFacts, len(window))
	perDayErr := map[string]string{}
	for _, day := range window {
		engine := newEngineShadowSet()
		if keys := ordersByDay[day]; keys != nil {
			engine.orderKeys = keys
		}
		decisions, derr := db.SignalDecisionsForDay(uid, day)
		if derr != nil {
			perDayErr[day] = "引擎定权快照读取失败：" + derr.Error()
			continue
		}
		for _, d := range decisions {
			// 只认实盘通道：纸面通道的定权快照与这根物理机腿不是同一个账户，认了就是跨通道记账。
			if d.Channel != "" && d.Channel != "live" {
				continue
			}
			if pure := carrierShadowPureCode(d.Code); pure != "" {
				engine.decisionCodes[pure] = true
			}
		}
		rows, rerr := db.CarrierShadowRowsForDay(uid, day)
		if rerr != nil {
			perDayErr[day] = "影子行读取失败：" + rerr.Error()
			continue
		}
		agg := map[string]kindFacts{}
		for _, r := range rows {
			f := agg[r.Kind]
			f.rows++
			if engine.agrees(carrierShadowSideWord(r.Side), carrierShadowPureCode(r.TsCode)) {
				f.agreed++
			}
			agg[r.Kind] = f
		}
		perDay[day] = agg
	}
	// 任一日的引擎侧/影子侧读不出 ⇒ 整表未知。为什么不"缺一天算一天"：
	// 那一天的三种零分拣就没有依据了，而对照表的整个价值就在于那三种零不互相冒充。
	// 点名哪一天按窗口顺序（新→旧）取第一个，不取 map 随机序——同一份坏读数每次报同一天，
	// 运维才可能顺着那天去查，而不是每次刷新换一个名字。
	for _, day := range window {
		if reason, bad := perDayErr[day]; bad {
			return unknown(day + "：" + reason)
		}
	}

	// 种类清单＝窗口内**报过的**种类；窗口外静默的种类不凭空造一格 0
	// （那一格会被读成"这一类跑了五天全零命中"，而真相是我们从没收到过它）。
	kinds := make([]string, 0, len(kindCounts))
	for k := range kindCounts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	// 逐类逐日填格：种类顺序按名字定（map 迭代顺序是随机的，而这份读数要能被
	// 前端与探针逐行对照，同一棵树两次跑出来必须同序）。
	// 每格先落 ReportedRows＝该日跨种类的总行数，再看"这一类"当天有没有行：
	// 有行→按引擎认不认分成 agreeing/diverged，没行而当天整根腿报过→no_hits，
	// 整根腿那天一个字节都没报→no_report（不计入窗口）。三条分支的判据全在
	// carrierShadowStateOf 那一处，这里只负责把数据喂给它。
	for _, kind := range kinds {
		agg := carrierShadowKind{Kind: kind, RequiredDays: carrierShadowRequiredDays, Days: []carrierShadowDay{}}
		for _, day := range window {
			hit := carrierShadowDay{Day: day, ReportedRows: dayCounts[day]}
			if f, ok := perDay[day][kind]; ok {
				hit.CarrierRows, hit.EngineAgreed = f.rows, f.agreed
			}
			hit.State = carrierShadowStateOf(hit)
			switch hit.State {
			case carrierShadowStateAgreeing, carrierShadowStateDiverged:
				agg.DaysWithData++
			case carrierShadowStateNoHits:
				agg.DaysNoHits++
			case carrierShadowStateNoReport:
				agg.DaysNoReport++
			}
			agg.CarrierRows += hit.CarrierRows
			agg.EngineAgreed += hit.EngineAgreed
			agg.Days = append(agg.Days, hit)
		}
		agg.WindowFilled = agg.DaysWithData >= carrierShadowRequiredDays
		view.Kinds = append(view.Kinds, agg)
	}
	view.Known = true
	view.Reason = ""
	return view
}

// carrierShadowStateOf 一格的状态判定——三种零的分拣处，全仓只有这一份逻辑。
// 顺序有意为之：先看这一天有没有报（no_report），再看这一类有没有命中，最后才谈一致/分歧。
// 入参里的 ReportedRows 必须是该**日**（跨种类）的上报行数，不是该种类的命中数——
// 传错了就会把"报了但这类没命中"判成"没报"，那正是本函数要防的折叠。
func carrierShadowStateOf(hit carrierShadowDay) string {
	if hit.ReportedRows <= 0 {
		return carrierShadowStateNoReport
	}
	if hit.CarrierRows == 0 {
		return carrierShadowStateNoHits
	}
	if hit.EngineAgreed > 0 {
		return carrierShadowStateAgreeing
	}
	return carrierShadowStateDiverged
}
