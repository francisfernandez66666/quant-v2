// t_rounds_api.go — §DYN-T 日内回转（做 T）的只读 HTTP 面（2026-10-26 批 4/9 收尾）。
//
// 为什么单独一个文件而不是塞进 paper.go：做 T 的账本有两本，语义完全不同——
//   - 纸面账本（`paper.Engine.TRoundDay`）：内存态，含被拦分布（step1~step9），只有引擎进程知道；
//   - 实盘账本（`<dataDir>/accounts/<uid>/t_rounds_live.json`）：落盘文件，跨账号互斥与重启对账都读它。
//
// 前者必须走引擎（文件里没有 in-memory 的 block 计数），后者刻意**只读文件**——与
// §ANCHOR-FIRST 的 anchor-shadow 接口同构：server 不反向依赖 engine（有 import 环），
// 文件是唯一真源，读到什么就回什么，损坏了如实报 corrupt 而不是回空数组假装正常。
//
// 「做 T 收益」是六轮裁决 T6-5 的独立口径：**不并入整仓收益**，只统计 done 轮的两腿净额。
// 这里把 net/gross/done/fee_est_share 原样透出，前端任何"总收益"卡片都不许把它加进去
// （§8.9a 的锁：converted/unwound 属战略侧，计进来就是把日内手法虚增成收益）。
//
// English: read-only HTTP surface for §DYN-T intraday round-trips. The paper book is served from
// the engine (block-step counters live only in memory); the live book is served by reading its
// JSON file (same shape as the anchor-shadow endpoint — no server→engine import, corrupt is
// reported as corrupt). T P&L is standalone by ruling T6-5 and must never fold into total return.
package server

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/paper"
)

// tRoundsDefaultLimit 默认返回的回转条数上限（与 paper 侧保留 300 条同量级，面板只看得清最近这些）。
const tRoundsDefaultLimit = 100

// tRoundsMaxLimit 单次请求上限，防止 ?limit=999999 把账本整份灌进响应。
const tRoundsMaxLimit = 500

// tRoundsView 纸面账本响应：日汇总 + 明细 + 被拦分布 + 独立收益口径。
type tRoundsView struct {
	Book       string                `json:"book"`        // paper / live（同一接口两面，前端按此分栏）
	Day        string                `json:"day"`         // 汇总所属交易日 YYYYMMDD
	DayStats   *paper.TRoundDayStats `json:"day_stats"`   // 当日开/配对/收摊/转换/熔断 + 收益
	Rounds     []paper.TRound        `json:"rounds"`      // 明细（新→旧）
	BlockSteps []tBlockStepView      `json:"block_steps"` // 当日"拦在哪一步"的分布（按次数倒序）
	PnL        tPnlView              `json:"pnl"`         // 独立做 T 收益（累计只算 done 轮）
	// Cost B1 成本分解：纸面取自同日 day_stats.cost（引擎同一帧算好），实盘按明细现算。
	// 放在顶层是为了让前端两本账共用一套渲染；day_stats 里那份仍保留（不回退旧调用方）。
	Cost *paper.TCostDaily `json:"cost,omitempty"`
	// ShadowNoT §T-COST B5：做 T vs 不做 T 的对照读数（同一路由新增段落，不新增路由）。
	// available=false 时 reason 必须原样上屏："没有对照"不等于"做 T 没有影响"。
	ShadowNoT *paper.TShadowNoTReadout `json:"shadow_no_t,omitempty"`
	Enabled   bool                     `json:"enabled"` // 该账号做 T 总开关的生效值
	Extra     map[string]interface{}   `json:"extra,omitempty"`
}

// tBlockStepView 一条"被拦步骤"读数（step 编号 + 代码 + 次数）。
type tBlockStepView struct {
	Step string `json:"step"`
	Code string `json:"code"`
	N    int    `json:"n"`
}

// tPnlView 做 T 收益（§8.9a 独立口径）：净额已扣两腿费用，毛额未扣。
type tPnlView struct {
	Net         float64 `json:"net"`
	Gross       float64 `json:"gross"`
	DoneRounds  int     `json:"done_rounds"`
	FeeEstShare float64 `json:"fee_est_share"` // done 轮里费用按估算入账的占比（0~1）
}

// handlePaperTRounds 处理 GET /api/paper/t-rounds?day=YYYYMMDD&limit=N（登录可见）。
// 数据源是**运营账号**的纸面引擎（与 /api/paper/positions 同口径），不是调用者本人的账本。
// English: GET /api/paper/t-rounds — the operator's paper T book for one day (default: today).
func (s *Server) handlePaperTRounds(w http.ResponseWriter, r *http.Request) {
	uid := s.operatorID()
	pe := s.paperEngineFor(uid)
	if pe == nil {
		writeJSON(w, http.StatusOK, tRoundsView{Book: "paper", Rounds: []paper.TRound{}, BlockSteps: []tBlockStepView{}})
		return
	}
	q := r.URL.Query()
	day := strings.TrimSpace(q.Get("day"))
	limit := parseTRoundsLimit(q.Get("limit"))
	view := tRoundsView{Book: "paper", Rounds: []paper.TRound{}, BlockSteps: []tBlockStepView{}}
	stats := pe.TRoundDay(day)
	view.DayStats = &stats
	view.Day = stats.Day
	// B1：成本分解随同日 day ledger 一起透出（不新增路由）；指针指向副本，避免调用方改到引擎内存。
	costCopy := stats.Cost
	view.Cost = &costCopy
	// 明细按"当日优先、再按 ID 倒序"：ID 形如 t0:<code>:<方向>:<YYYYMMDD>:<seq>，
	// 直接倒序字典序就能把今天的多轮排在历史之前（同码同日的 seq 是末段，故同码内倒序也成立）。
	all := pe.TRounds()
	out := make([]paper.TRound, 0, limit)
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	for _, rr := range all {
		if day != "" && rr.Day != day {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, rr)
	}
	view.Rounds = out

	// 被拦分布的键是 "step|code" 拼出来的（paper 侧同一条锁：计数按码分开，日志按码每日一次）。
	// 这里拆开回给前端，并把 day 兜底成 block 自己的日期——当日一条腿都没开时，
	// day_stats.day 可能还是空串，而"为什么没开"那批计数已经真实存在了。
	blockDay, blocks := pe.TBlockStats()
	if view.Day == "" {
		view.Day = blockDay
	}
	for k, n := range blocks {
		step, code, ok := strings.Cut(k, "|")
		if !ok {
			continue // 键形不符（历史账本里不该出现）：宁可少一行，也不回一个猜出来的 step
		}
		view.BlockSteps = append(view.BlockSteps, tBlockStepView{Step: step, Code: code, N: n})
	}
	// 排序先按次数倒序、同数按 step 升序：面板要一眼看出"今天主要卡在哪一步"。
	sort.Slice(view.BlockSteps, func(i, j int) bool {
		if view.BlockSteps[i].N != view.BlockSteps[j].N {
			return view.BlockSteps[i].N > view.BlockSteps[j].N
		}
		return view.BlockSteps[i].Step < view.BlockSteps[j].Step
	})

	net, gross, done, feeEst := pe.TRoundNetPnL()
	view.PnL = tPnlView{Net: net, Gross: gross, DoneRounds: done, FeeEstShare: feeEst}
	// B5：影子对照读数（开关关闭/无当日影子账时 available=false + reason，绝不回一条恒 0 的假曲线）。
	shadow, _ := pe.TShadowNoTReadout()
	view.ShadowNoT = &shadow
	// 开关与影子态都从**账号级 sizing** 读（配置真源），不问引擎：paper.TParams 是 engine 侧
	// 每轮组装的执行参数，接口若在这里重算一遍 MarketScale 就等于把"读数"变成"第二次判定"。
	sc, _ := s.cfg.GetSizingConfigFor(uid)
	view.Enabled = sc.T.TEnabled()
	// §8.8 面板必须给出「当日反 T 总量帽已用比例」，且**分母是 min(总资产, 持仓市值)**（六轮 T6-2）：
	// 用总资产当分母会在低仓账号上算出一个比实际持仓还大的卖额上限，正是"空仓时还能卖 20%"那种荒谬读数。
	// 这些数一律取 paper 的同一帧读数（见 paper.TargetReadout）：接口自己再拼一遍式子，
	// 就会出现"面板的剩余空间与实际下单用的不是同一个式子"——那类分叉最难查，也没有任何测试能抓住。
	read := pe.TargetReadout()
	usedPct := 0.0
	if capValue := read.SellCapBase * sc.T.TIntradaySellCapTotalPctOf() / 100.0; capValue > 0 {
		usedPct = read.SellNotional / capValue * 100
	}
	view.Extra = map[string]interface{}{
		"target":         read,
		"sell_cap_pct":   sc.T.TIntradaySellCapTotalPctOf(),
		"sell_cap_value": round2(read.SellCapBase * sc.T.TIntradaySellCapTotalPctOf() / 100.0),
		"sell_used_pct":  round2(usedPct),
		"shadow":         sc.ShadowOn(), // true＝影子期（只留痕不执法），前端据此标灰
		// 生效值与回落键不在这里二次计算：GET /api/config/regime 的 sizing.t 已经是
		// WithTEffective 填好的生效值，越界键由引擎每轮的 [t-config-fallback] 日志点名。
	}
	writeJSON(w, http.StatusOK, view)
}

// handleLiveTRounds 处理 GET /api/t-live/rounds（admin）：读实盘 T 账本文件，只读不改。
// state 三态：ok / missing（还没做过实盘回转）/ corrupt（文件坏了——**原文件不动**，如实报错）。
// English: GET /api/t-live/rounds (admin) — the live T ledger file, read-only, with honest
// missing/corrupt states instead of an empty array masquerading as "no rounds".
func (s *Server) handleLiveTRounds(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	if uid == "" || filepath.Base(uid) != uid || strings.ContainsAny(uid, `/\`) {
		writeError(w, http.StatusForbidden, "无法确定账号")
		return
	}
	if s.cacheDir == "" {
		writeError(w, http.StatusServiceUnavailable, "数据目录未接入")
		return
	}
	path := filepath.Join(s.cacheDir, "accounts", uid, "t_rounds_live.json")
	raw, err := os.ReadFile(path)
	q := r.URL.Query()
	day := strings.TrimSpace(q.Get("day"))
	limit := parseTRoundsLimit(q.Get("limit"))
	if err != nil {
		// 缺文件与"读坏了"必须分开说：前者是"这个账号还没做过实盘回转"（正常态），
		// 后者是"账本存在但解析不了"（异常态，且**原文件一个字节都不许动**，改名/清空都是替操作人做决定）。
		// 两者都回 200 + 结构化状态而不是 500：这是一块只读观测面，把异常做成 HTTP 错误
		// 只会让前端把它渲染成"网络问题"，真正需要人看的那一行反而被噪声埋掉。
		state, reason := "corrupt", "实盘回转账本读取失败（原文件未改动）"
		if os.IsNotExist(err) {
			state, reason = "missing", "该账号还没有实盘回转记录（做 T 未开、或从未成交一条腿）"
		}
		// 空态也要给结构完整的对象（rounds 是空数组不是 null、pnl 是全零值）：
		// 前端一律直接 map/取字段，回 null 会让页面崩在渲染期，而"崩"和"没有记录"是两件事。
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"book": "live", "account": uid, "state": state, "reason": reason,
			"day": day, "rounds": []json.RawMessage{}, "pnl": tPnlView{}, "cost": tCostView{},
			"funds": nil,
		})
		return
	}
	// 明细按 RawMessage 原样透传：server 不复制 engine 的回转结构体，
	// 否则柜台字段一改，这里就会静默丢列（本项目把"丢列后照样 200"列为降级报成功）。
	var st struct {
		Day      string            `json:"day"`
		Rounds   []json.RawMessage `json:"rounds"`
		BlockDay string            `json:"block_day"` // 被拦分布所属日（与 rounds 同日才可信）
		Blocks   map[string]int    `json:"blocks"`    // 键＝"step|code"，与纸面侧 tBlock 同形
		// Funds §FUNDS-TODAY：引擎每轮写下的资金帧（原样透传，server 不重算）。
		// Day 与文件同日才可信；跨日文件里没有有效帧（engine 落盘时已清）。
		Funds json.RawMessage `json:"funds"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		// 解析失败与读取失败同样只报不修：改名、清空、"尽力解析上半段"都是替操作人做决定，
		// 而他下一步要做的是拿这份原始文件去对 orders 表。
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"book": "live", "account": uid, "state": "corrupt",
			"reason": "实盘回转账本解析失败（原文件未改动，等待人工核对）",
			"day":    day, "rounds": []json.RawMessage{}, "pnl": tPnlView{}, "cost": tCostView{},
			"funds": nil,
		})
		return
	}
	// 空切片归一成空数组，理由同上（序列化出来是 [] 而不是 null）。
	if st.Rounds == nil {
		st.Rounds = []json.RawMessage{}
	}

	// 收益聚合刻意在这里重算一遍（而不是只信文件里的汇总字段）：账本只有明细是真源，
	// 任何"日汇总"字段都可能是上一次写盘时的旧值。
	var net, gross float64
	done, estCnt, kept := 0, 0, 0
	filtered := make([]json.RawMessage, 0, limit)
	// B1 成本分项按"目标日"累加（day 参数优先，其次账本自己的当日）：
	// 与明细过滤同一个日子，避免出现"明细是今天、成本是累计"的拼接读数。
	targetCostDay := day
	if targetCostDay == "" {
		targetCostDay = st.Day
	}
	acc := &tCostAccumulator{}
	for i := len(st.Rounds) - 1; i >= 0; i-- { // 新→旧（文件按 ID 升序落盘）
		// 只解聚合需要的字段：明细原样透传（json.RawMessage），server 不复制 engine 的结构体。
		var one struct {
			Day              string  `json:"day"`
			Dir              string  `json:"dir"`
			Status           string  `json:"status"`
			TPnlNet          float64 `json:"t_pnl_net"`
			TPnlGross        float64 `json:"t_pnl_gross"`
			FeeEst           bool    `json:"t_fee_est"`
			BuyFilled        int     `json:"buy_filled"`
			SellFilled       int     `json:"sell_filled"`
			BuyPrice         float64 `json:"buy_price"`
			SellPrice        float64 `json:"sell_price"`
			DecisionPrice    float64 `json:"decision_price"`
			FeeBuy           float64 `json:"fee_buy"`
			FeeSell          float64 `json:"fee_sell"`
			StampTax         float64 `json:"stamp_tax"`
			FeeSource        string  `json:"fee_source"`
			ParticipationPct float64 `json:"participation_pct"`
			Unfilled         bool    `json:"unfilled"`
		}
		// 单行解不开就跳过：坏一行不该让整张卡读不出，但也不给它编一个 0 收益（收益只加能解开的行）。
		if json.Unmarshal(st.Rounds[i], &one) != nil {
			continue
		}
		// done 之外的态（open/unwound/converted）一律不进收益——converted 的仓位变化属战略侧。
		if one.Status == "done" {
			done++
			net += one.TPnlNet
			gross += one.TPnlGross
			if one.FeeEst {
				estCnt++
			}
		}
		// 成本段只累加目标日的轮（收益段刻意不按日过滤，见上面注释里的口径分割）。
		if targetCostDay == "" || one.Day == targetCostDay {
			firstFilled, firstPrice, pairFilled := one.BuyFilled, one.BuyPrice, one.SellFilled
			if one.Dir == "sell_first" {
				firstFilled, firstPrice, pairFilled = one.SellFilled, one.SellPrice, one.BuyFilled
			}
			acc.add(firstFilled, firstPrice, one.DecisionPrice,
				one.FeeBuy, one.FeeSell, one.StampTax, one.ParticipationPct,
				one.TPnlGross, one.TPnlNet, pairFilled, one.FeeSource, one.Unfilled)
		}
		// 明细过滤只看 day，收益不受 day 过滤影响（见上面注释里的口径分割）。
		if day != "" && one.Day != day {
			continue
		}
		if kept < limit {
			filtered = append(filtered, st.Rounds[i])
			kept++
		}
	}
	feeEstShare := 0.0
	if done > 0 {
		feeEstShare = float64(estCnt) / float64(done)
	}
	outDay := st.Day
	if day != "" {
		outDay = day
	}
	// 被拦分布（step|code → 次数）直接来自状态文件：纸面那本走引擎内存，实盘这本走文件，
	// 两边形状保持一致，面板才能一屏比两本账。
	blockViews := make([]tBlockStepView, 0, len(st.Blocks))
	for k, n := range st.Blocks {
		step, code, ok := strings.Cut(k, "|")
		if !ok {
			continue
		}
		blockViews = append(blockViews, tBlockStepView{Step: step, Code: code, N: n})
	}
	sort.Slice(blockViews, func(i, j int) bool {
		if blockViews[i].N != blockViews[j].N {
			return blockViews[i].N > blockViews[j].N
		}
		return blockViews[i].Step < blockViews[j].Step
	})
	// 委托台账（§6.3 B 组第 19 条：报撤比是给券商异常交易观察留的量化证据）。
	placed, canceled, filled, lerr := 0, 0, 0, error(nil)
	if db := s.realDB(); db != nil {
		placed, canceled, filled, lerr = db.TLegOrderLedger(uid, dayOfQueryOrToday(q.Get("day")))
	}
	ledger := map[string]interface{}{
		"placed": placed, "canceled": canceled, "filled": filled,
	}
	if placed > 0 {
		ledger["cancel_ratio_pct"] = round2(float64(canceled) / float64(placed) * 100)
	}
	if lerr != nil {
		// 台账读不到不等于 0 笔：这条读数本身就是证据，缺了必须说出来（静默给 0 ＝假绿）。
		ledger["error"] = lerr.Error()
	}
	view := map[string]interface{}{
		"book":        "live",
		"account":     uid,
		"state":       "ok",
		"day":         outDay,
		"rounds":      filtered,
		"block_steps": blockViews,
		"ledger":      ledger,
		"cost":        acc.finalize(),
		"funds":       st.Funds,
		"pnl": tPnlView{Net: round2(net), Gross: round2(gross),
			DoneRounds: done, FeeEstShare: feeEstShare},
	}
	// §FIX-20261028-F08（2026-10-29）：空账本必须给 reason，否则前端只能渲染成"今天 0 轮 T"
	// ——而真相可能是"T 侧今天根本没运行"。文案保持中立：不从 t_at 推断轮次状态
	//（t_at 只是 T 循环最后一次写帧的时刻，据此说"已全部收摊"是凭空断言）。
	if len(filtered) == 0 {
		view["reason"] = "当日无回转轮记录（T 侧未运行或今日未开腿）"
	}
	writeJSON(w, http.StatusOK, view)
}

// parseTRoundsLimit 解析 limit（缺省/非法都退回默认值；上限 500 条）。
func parseTRoundsLimit(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return tRoundsDefaultLimit
	}
	if n > tRoundsMaxLimit {
		return tRoundsMaxLimit
	}
	return n
}

// round2 金额两位小数（面板展示口径，与 paper 侧 round2 同规则；server 包不依赖 paper 故本地实现）。
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// statsSellNotional 取日汇总里的反 T 卖腿市值（DayStats 为空时回 0，不让接口在空账本上崩）。
func statsSellNotional(st *paper.TRoundDayStats) float64 {
	if st == nil {
		return 0
	}
	return st.SellNotional
}

// handlePaperTargetReadout 处理 GET /api/paper/target（登录可见）：v4 目标仓位与 T 额度的一帧读数。
//
// 这个接口是 §8.8「面板展示」那条要求的唯一出口：战略 Tgt/Cur/Room、MS、天花板、单笔上限、
// T 额度、可用 T 现金、反 T 冻结款与总量帽分母，一次给全，并且**全部由账本引擎按下单同一条式子算出**。
// 面板若要自己拼这些数，就等于承认"显示的"和"执行的"可以不一样——本项目为这类分叉付过的代价
// 已经写进好几条历史锁里了，所以这里只做一件事：把引擎内部的读数原样搬出来。
//
// market_scale=0 是合法读数（engine 还没注入本轮定权上下文），前端必须渲染成"本轮尚未定权"，
// 不许兜底成 1.00 —— 那会把"不知道"画成"满额"，正是数据盲区里最危险的一种假绿。
// English: the single read-only frame of the v4 target model, computed by the engine itself;
// market_scale=0 means "no sizing this round" and must never be rendered as a neutral 1.00.
func (s *Server) handlePaperTargetReadout(w http.ResponseWriter, r *http.Request) {
	pe := s.paperEngineFor(s.operatorID())
	if pe == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"available": false,
			"reason": "模拟盘引擎未接入：目标仓位读数不可得（不是 0，也不是满额）"})
		return
	}
	read := pe.TargetReadout()
	writeJSON(w, http.StatusOK, map[string]interface{}{"available": true, "readout": read})
}

// dayOfQueryOrToday 把接口上的 YYYYMMDD 参数换成 orders.created_at 用的 YYYY-MM-DD；
// 缺参取真实今天（与明细过滤同源，不允许两处各自 now() 造成"过滤是今天、台账是昨天"）。
func dayOfQueryOrToday(raw string) string {
	d := strings.TrimSpace(raw)
	if len(d) == 8 {
		return d[0:4] + "-" + d[4:6] + "-" + d[6:8]
	}
	return time.Now().Format("2006-01-02")
}

// handleFundsToday 处理 GET /api/funds/today（admin）：当日「信号仓 / T 仓」资金一帧。
//
// 数据来源刻意分成两条，server 只做搬运：
//   - 实盘：读 `<dataDir>/accounts/<uid>/t_rounds_live.json` 的 funds 帧（引擎每轮写下的
//     "本轮实际使用的读数"；本接口不重算，避免第二套公式）；
//   - 纸面：`paper.Engine.TargetReadout()`（与 /api/paper/target 同一个出口）。
//
// 每日清零与诚实性：
//   - 帧自带 Day，只有与当日一致才返回（引擎跨日已清，文件里不会带昨日数）；
//   - 新交易日首帧之前 state=pending（"今日尚未定权"），**不回落到昨日**；
//   - 实盘 T 循环未跑（非交易时段/未启用）时仍给 `live.account`（券商资金快照）兜底，
//     并同时给 `cash_at`，让前端能区分"金额没变"与"快照没更新"。
//
// English: one read-only frame of "signal book vs T book" funds for today. Live comes from the
// engine-published funds frame in the T ledger file (never recomputed here); paper comes from the
// paper engine's TargetReadout. A new trading day reports pending until the first frame exists.
func (s *Server) handleFundsToday(w http.ResponseWriter, r *http.Request) {
	uid := requestUserID(r)
	if uid == "" || filepath.Base(uid) != uid || strings.ContainsAny(uid, `/\`) {
		writeError(w, http.StatusForbidden, "无法确定账号")
		return
	}
	if s.cacheDir == "" {
		writeError(w, http.StatusServiceUnavailable, "数据目录未接入")
		return
	}
	day := cntime.DayCompactOf(cntime.Now())
	live := map[string]interface{}{"state": "missing", "day": day, "funds": nil,
		"reason": "该账号还没有实盘回转记录（做 T 未开、或从未成交一条腿）"}
	path := filepath.Join(s.cacheDir, "accounts", uid, "t_rounds_live.json")
	if b, err := os.ReadFile(path); err == nil {
		var st struct {
			Day   string          `json:"day"`
			Funds json.RawMessage `json:"funds"`
		}
		if uerr := json.Unmarshal(b, &st); uerr != nil {
			live["state"] = "corrupt"
			live["reason"] = "实盘回转账本解析失败（原文件未改动，等待人工核对）"
		} else if len(st.Funds) > 0 && string(st.Funds) != "null" {
			live["state"] = "ok"
			live["day"] = st.Day
			live["funds"] = st.Funds
			live["reason"] = ""
		} else {
			// 文件在但今日还没有资金帧：新交易日首帧前 / 当日尚未进入交易时段。
			// 这里必须显式说"尚未定权"，不能拿账本里昨日的数顶上。
			live["state"] = "pending"
			live["reason"] = "今日尚未定权：引擎还没有写下当日资金帧"
		}
	} else if !os.IsNotExist(err) {
		live["state"] = "corrupt"
		live["reason"] = "实盘回转账本读取失败（原文件未改动）"
	}
	// 账户级兜底：T 循环没跑也要能看到券商资金快照（含手动委托冻结）。
	if db := s.realDB(); db != nil {
		if acc, err := db.GetRealAccount(uid); err == nil && acc.UpdatedAt != "" {
			live["account"] = map[string]interface{}{
				"cash": acc.AvailableCash, "frozen_cash": acc.FrozenCash,
				"total_asset": acc.TotalAsset, "market_value": acc.MarketValue,
				"cash_at": acc.UpdatedAt,
			}
		}
	}
	paperView := map[string]interface{}{"available": false, "reason": "模拟盘引擎未接入：目标仓位读数不可得（不是 0，也不是满额）"}
	if pe := s.paperEngineFor(s.operatorID()); pe != nil {
		paperView = map[string]interface{}{"available": true, "readout": pe.TargetReadout()}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"day": day, "at": cntime.Now().Format(time.RFC3339),
		"live": live, "paper": paperView,
	})
}
