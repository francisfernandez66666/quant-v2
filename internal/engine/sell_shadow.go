// sell_shadow.go — §SELLPOINT-UNIFY 卖出统一裁决通道的 engine 接线（P1-b 影子 + P2 投影源 + P3 双账并轨）。
//
// 定位（docs/REFACTOR_UNIFIED_SELL_20260921.md §四）：每轮把持仓喂给 signalctl.JudgeSellView，
// 裁定与买入共用同一留痕环（GET /api/signalctl/verdicts 可查 stage=sell_discipline 的 pass/hold）。
// 两账各走自己的通道，参数与语义同构（双账一口径）：
//   - live（runSellUnifiedJudge，pushRealAdvice 每轮）：键=(ChannelLive, 实盘主账号, ts_code)；
//   - paper（runPaperUnifiedJudge，主循环每轮经 judgePaperLedgers，独立于撮合分发时机）：
//     键=(ChannelPaper, 账号, 纯数字代码)，
//     处置经 paper.ApplyUnifiedSell 唯一入口执行（P3）。
//
// 返回值按模式消费：
//   - shadow（默认）：只留痕不执行——旧链（live 五路 / paper 探测器直卖）照常跑，切闸前用于证据对照；
//   - on（P2/P3 切闸）：live 处置=展示投影+唯一执行出口（sell_unified_exec.go）；paper 处置=唯一
//     自动卖出入口，探测器卖出信号降级为证据不再直达撮合。
//
// qmt.sell_unified_mode 三态：
//   - ""（缺省）/ "shadow"：影子（资金行为零变化，默认）；
//   - "off"：整体停用（不裁决不留痕）；
//   - "on"：P2/P3 已接线——裁决即执行口径（保护性守卫全保留）。
package engine

import (
	"log"
	"strings"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/report"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// sellRoundVerdict 单持仓一轮统一裁决的引擎侧记录（投影/执行的输入）。
type sellRoundVerdict struct {
	TsCode   string // 持仓账本主键（live=ts_code 带后缀，paper=纯数字代码——与各自状态键一致）
	Verdict  signalctl.SellVerdict
	BearHit  bool    // 本轮命中利空证据（归因/D1 拦截）——未触线时降级预警卡
	Verified string  // 利空验证等级（signalctl.BearVerified*）
	Price    float64 // 本轮裁决用现价（投影卡 RefPrice/撮合价同源，无效价轮不入表）
}

// sellJudgeFeed 一轮裁决的共用证据装配（live/paper 同构输入）。
type sellJudgeFeed struct {
	Scores      map[string]combat_agent.StockScores // 做多打分（延持唯一信号源，配 scoresAt 判新鲜）
	D1Scores    map[string]combat_agent.D1Score     // D1 负面拦截证据
	BearReasons map[string]string                   // 利空归因证据（纯码 → 原因）
	PoolQuotes  map[string]*data.StockInfo          // 打分池行情（优先）
	SnapQuotes  map[string]*data.StockInfo          // 5s 全量快照（打分池外持仓兜底，§R4-6 同口径）
}

// sellProbeRow 裁决探针的账本中性行（live 真实持仓 / paper 纸面持仓统一映射）。
type sellProbeRow struct {
	Code       string // 裁决状态键代码
	Name       string
	EntryPrice float64
	HighPrice  float64 // 持仓期最高价（≤0 内核回退成本价；paper 恒 0=状态机每轮自抬）
}

// runSellUnifiedJudge live 卖出裁决主入口（pushRealAdvice 每轮调用；仅交易时段，随宿主循环）。
// account=实盘主账号（§GAP2-W2 定向口径）；positions=该账号真实持仓；exitQuotes=本轮打分池行情；
// quotes=5s 实时快照（打分池外持仓的兜底价）；scores=本轮做多打分（延持信号源，配 scoresAt 判新鲜）；
// d1Scores/bearReasons=利空证据源；验证等级由 §D1 护栏4 的 bearTier 双源判定注入（dual 才给硬清资格，
// 无记录/超龄按 single 只预警）。
// 返回本轮有效裁决（仅现价有效的持仓）；mode=off 返回 nil（不裁决不留痕）。
// English: per-round live unified sell judge; the returned verdicts feed the P2 projection/execution,
// while shadow mode only records them and nothing executes.
func (e *Engine) runSellUnifiedJudge(
	account string,
	positions []store.RealPosition,
	exitQuotes, quotes map[string]*data.StockInfo,
	scores map[string]combat_agent.StockScores,
	d1Scores map[string]combat_agent.D1Score,
	bearReasons map[string]string,
) []sellRoundVerdict {
	ctl := e.qmtCtrlRef()
	if ctl == nil {
		return nil
	}
	mode := sellUnifiedModeOf(ctl.Config())
	if mode == "off" {
		return nil // 整体停用：不裁决、不留痕
	}
	rows := make([]sellProbeRow, 0, len(positions))
	for i := range positions {
		p := positions[i]
		rows = append(rows, sellProbeRow{Code: p.TsCode, Name: p.Name, EntryPrice: p.CostPrice, HighPrice: p.HighestPrice})
	}
	feed := sellJudgeFeed{Scores: scores, D1Scores: d1Scores, BearReasons: bearReasons, PoolQuotes: exitQuotes, SnapQuotes: quotes}
	verdicts := e.judgeSellPositions(signalctl.ChannelLive, account, rows, signalctl.Policy{Discipline: ctl.Config().Discipline}, feed, orDefault(mode, "shadow"))
	// 状态生命周期与持仓对齐（平仓即删，重新入场从零开始）。
	held := make(map[string]bool, len(positions))
	for _, p := range positions {
		held[p.TsCode] = true
	}
	e.SignalCtl().PruneSellStates(signalctl.ChannelLive, account, held)
	return verdicts
}

// runPaperUnifiedJudge P3 模拟盘并轨：paper 账本走与 live 同一裁决内核（ChannelPaper 通道，
// 键=(通道,账号,纯码)，双账同参数同语义）。调用方=judgePaperLedgers（主循环每轮，registry
// 注入按账号遍历）；mode=on 时处置只经 ApplyUnifiedSell 唯一出口执行，shadow 只裁决留痕
// 不碰账（探测器直卖旧链此时仍照常跑，用于切闸前证据对照）；on 时探测器卖出直达撮合
// 已被上游证据闸（unifiedSellGateSigs）阻断（做空账本不在并轨范围，方向=做空的信号原样走 OnSignals）。
// pol 由调用方传入（paperSignalPolicy(uid)：账号级 paper 纪律参数，双账同口径的另一半）。
// English: P3 — the paper book is judged by the same kernel on the paper channel; only mode=on
// executes disposals, and solely through paper.Engine.ApplyUnifiedSell.
func (e *Engine) runPaperUnifiedJudge(account string, pe *paper.Engine, feed sellJudgeFeed, pol signalctl.Policy) []sellRoundVerdict {
	mode := e.sellUnifiedModeEngine()
	if mode == "off" || pe == nil {
		return nil
	}
	probes := pe.SellProbes()
	rows := make([]sellProbeRow, 0, len(probes))
	held := make(map[string]bool, len(probes))
	for _, pr := range probes {
		rows = append(rows, sellProbeRow{Code: pr.Code, Name: pr.Name, EntryPrice: pr.EntryPrice})
		held[pr.Code] = true
	}
	verdicts := e.judgeSellPositions(signalctl.ChannelPaper, account, rows, pol, feed, orDefault(mode, "shadow"))
	e.SignalCtl().PruneSellStates(signalctl.ChannelPaper, account, held)
	if mode != "on" {
		return verdicts // shadow：只留痕，处置不执行（资金行为零变化）
	}
	for i := range verdicts {
		v := &verdicts[i]
		if v.Verdict.Disposal == nil {
			continue
		}
		d := v.Verdict.Disposal
		act := "close"
		if d.Action == signalctl.SellActionTrim {
			act = "trim"
		}
		if pe.ApplyUnifiedSell(v.TsCode, act, v.Price, "[统一裁决] "+d.Reason) {
			log.Printf("[sell-judge:%s] paper %s 处置已执行 action=%s 线=%s 理由=%s",
				orDefault(mode, "shadow"), v.TsCode, d.Action, d.Line, d.Reason)
		}
	}
	return verdicts
}

// judgeSellPositions 两账共用的裁决内核：逐仓装配 SellInput → JudgeSellView → 有效裁决表。
// 价格解析与旧口径一致：打分池价优先、5s 快照兜底，两者皆缺=无效价轮（不下结论、状态不动）。
// 做多信号新鲜度以 e.scoresAt 为基准（边界⑥）；利空验证等级取 bearTierFor（护栏4，dual 才有硬清资格）。
func (e *Engine) judgeSellPositions(ch signalctl.Channel, account string, rows []sellProbeRow, pol signalctl.Policy, feed sellJudgeFeed, modeTag string) []sellRoundVerdict {
	if len(rows) == 0 {
		return nil
	}
	now := time.Now()
	e.mu.RLock()
	scoresAt := e.scoresAt // 边界⑥基准；零值=本轮尚无打分，做多信号一律不新鲜
	e.mu.RUnlock()

	verdicts := make([]sellRoundVerdict, 0, len(rows))
	for _, row := range rows {
		code := pureTsCode(row.Code)
		price := 0.0
		if q := feed.PoolQuotes[code]; q != nil && q.Price > 0 {
			price = q.Price
		} else if q := feed.SnapQuotes[code]; q != nil && q.Price > 0 {
			price = q.Price // §R4-6 同口径：打分池外的持仓用 5s 快照兜底，两者皆缺→无效价不下结论
		}
		in := signalctl.SellInput{
			Code:       row.Code,
			EntryPrice: row.EntryPrice,
			HighPrice:  row.HighPrice,
			CurPrice:   price,
		}
		// 做多信号（延持唯一资格源，语义②）：带打分轮次时间戳（边界⑥）。
		if sc, ok := feed.Scores[code]; ok && sc.SignalActive {
			in.Bull = signalctl.SignalFresh{Active: true, At: scoresAt}
		}
		// 利空证据（§D1 护栏4）：验证等级取 propagateSectorToStocks 的双源判定——
		// dual（同花顺∩东财成分名单均命中）+触线 → 即时硬清；single/未验真/超龄 → 只预警。
		verified := e.bearTierFor(code)
		bearHit := false
		if br, ok := feed.BearReasons[code]; ok && br != "" {
			bearHit = true
			if !in.Bear.Hit {
				in.Bear = signalctl.BearConfirm{Hit: true, Verified: verified}
			}
			in.Evidence = append(in.Evidence, "利空归因:"+br)
		}
		if d1, ok := feed.D1Scores[code]; ok && d1.Blocked {
			bearHit = true
			if !in.Bear.Hit {
				in.Bear = signalctl.BearConfirm{Hit: true, Verified: verified}
			}
			ev := "D1负面拦截"
			if d1.Reason != "" {
				ev += ":" + d1.Reason
			}
			in.Evidence = append(in.Evidence, ev)
		}
		vr := e.SignalCtl().JudgeSellView(ch, account, in, pol, now)
		if !vr.Valid {
			continue // 无效价轮：状态未动、不出卡不执行
		}
		verdicts = append(verdicts, sellRoundVerdict{TsCode: row.Code, Verdict: vr, BearHit: bearHit, Verified: verified, Price: price})
		if vr.Disposal != nil {
			// shadow 模式下的处置只留痕；on 模式 live 侧经 autoExecuteRealSells 来源闸执行、
			// paper 侧由 runPaperUnifiedJudge→ApplyUnifiedSell 执行。
			log.Printf("[sell-judge:%s] %s(%s) 建议处置=%s 线=%s 盈亏=%.2f%% 理由=%s",
				modeTag, row.Name, row.Code, vr.Disposal.Action, vr.Disposal.Line, vr.Disposal.PnlPct, vr.Disposal.Reason)
		}
	}
	return verdicts
}

// judgePaperLedgers P3 纸面双账统一卖出裁决的每轮入口（主循环 13e-pre 与 5s 近实时轮各调一次）：
//   - paper 引擎账本：registry 注入了按账号回调 paperSellJudgeFn 就走它（多账号逐账），
//     否则回退全局单引擎 e.paper（无 registry 场景，两路语义同构不留旁路）；
//   - report 手动账本（FIX#15 场景）：judgeReportLedger 同内核裁决，mode=on 时处置直写
//     LogExit/SellLot（13e 旧链出口此时已被来源闸关闭，杜绝双写）。
//
// mode=off 直接返回（不裁决不留痕）；shadow 只留痕不执行（资金行为零变化）。
// English: per-round entry for the P3 paper-ledger unified sell judge (called by both the main
// loop and the 5s round); dispatches to the injected per-account hook or the global fallback
// engine, plus the report book pass. off = no-op; shadow = record only; on = execute via the
// single exits.
func (e *Engine) judgePaperLedgers(feed sellJudgeFeed) {
	mode := e.sellUnifiedModeEngine()
	if mode == "off" {
		return
	}
	e.mu.RLock()
	judge := e.paperSellJudgeFn
	pe := e.paper
	e.mu.RUnlock()
	if judge != nil {
		// registry 回调内部已含交易时段/账号过滤（与 dispatchPaperSignals 同口径）。
		judge(feed)
	} else if pe != nil && pe.Enabled() && data.IsFullTradingHours(time.Now()) {
		owner := e.primaryMember()
		e.runPaperUnifiedJudge(owner, pe, feed, e.paperSignalPolicy(owner))
	}
	e.judgeReportLedger(feed, mode)
}

// judgeReportLedger P3 report 手动账本并轨：对用户录入（未进纸面引擎）的做多持仓跑同一
// 裁决内核，通道=ChannelPaper、账号键=账本归属账号+"@report" 后缀——与纸面引擎账本的
// (通道,账号,代码) 状态键隔离，两套账本各自独立推进观察窗/结算栅格。
// 处置执行与 FIX#15 旧链同构：close→rpt.LogExit 全平、trim→rpt.SellLot 半仓（整手，
// reportTrimDone 每码每日一次去重）；全局纸面引擎持有的 code 跳过（其镜像行由 paper
// 侧处置回写，双账簿不重复卖）；无效价轮内核已滤除。mode!=on 只裁决留痕不执行。
// report 侧处置（本函数 on 分支）与 13e 旧链 autoExitReportSells 同，均不另设时段闸。
// English: P3 report-book pass — same kernel on the paper channel keyed "<account>@report"
// (state isolated from the paper engine book); on-mode disposals reuse the FIX#15 exits
// (LogExit full / SellLot half, reportTrimDone dedup), skipping codes held by the global paper
// engine (their mirror rows are handled paper-side); non-on modes only record. No extra hours
// gate here either — both scoreCycle and the 5s round that host it are trading-hours loops.
func (e *Engine) judgeReportLedger(feed sellJudgeFeed, mode string) {
	if e.rpt == nil {
		return
	}
	held := e.rpt.HeldPositions()
	if len(held) == 0 {
		return
	}
	e.mu.RLock()
	pe := e.paper
	e.mu.RUnlock()

	// 按账号分组（ExecLog.UserID，空=系统/全局归主账号）；同账号内一码一行（report 账本
	// 每 code 单条持仓记录，Lots 承载加仓批次）。
	type reportGroup struct {
		rows   []sellProbeRow
		byCode map[string]report.ExecLog
		held   map[string]bool
	}
	groups := make(map[string]*reportGroup)
	for _, pos := range held {
		if pos.Direction != "" && pos.Direction != "做多" {
			continue // 做空记录不在卖出并轨范围（融券账本口径，同 paper 侧）
		}
		uid := pos.UserID
		if uid == "" {
			uid = e.primaryMember()
		}
		g := groups[uid]
		if g == nil {
			g = &reportGroup{byCode: make(map[string]report.ExecLog), held: make(map[string]bool)}
			groups[uid] = g
		}
		if g.held[pos.Code] {
			continue // 同码多行异常（理论不发生）：首行为准，处置时逐行会重复卖
		}
		g.rows = append(g.rows, sellProbeRow{Code: pos.Code, Name: pos.Name, EntryPrice: pos.EntryPrice, HighPrice: pos.HighestPrice})
		g.byCode[pos.Code] = pos
		g.held[pos.Code] = true
	}
	for uid, g := range groups {
		account := uid + "@report"
		verdicts := e.judgeSellPositions(signalctl.ChannelPaper, account, g.rows, e.paperSignalPolicy(uid), feed, orDefault(mode, "shadow"))
		e.SignalCtl().PruneSellStates(signalctl.ChannelPaper, account, g.held)
		if mode != "on" {
			continue // shadow：只留痕（13e 旧链仍在执行 report 处置）
		}
		e.applyReportVerdicts(uid, g.byCode, verdicts, pe)
	}
}

// applyReportVerdicts report 账本处置执行（mode=on 唯一出口）：close→LogExit 全平、
// trim→SellLot 半仓（整手、reportTrimDone 每码每日一次），与 FIX#15 旧链同构同守卫——
// 全局纸面引擎持有的 code 跳过（镜像行由 paper 侧回写）、行情无效价已在内核滤除。
// English: on-mode exits for the report book, mirroring FIX#15 guards (skip codes held by the
// global paper engine — their mirror rows are exited paper-side; invalid-price rounds skipped).
func (e *Engine) applyReportVerdicts(uid string, byCode map[string]report.ExecLog, verdicts []sellRoundVerdict, pe *paper.Engine) {
	now := time.Now()
	td := data.TradingDayDate(now)
	e.reportTrimDoneMu.Lock()
	defer e.reportTrimDoneMu.Unlock()
	if e.reportTrimDone == nil {
		e.reportTrimDone = make(map[string]string)
	}
	for i := range verdicts {
		v := &verdicts[i]
		if v.Verdict.Disposal == nil {
			continue
		}
		pos, ok := byCode[v.TsCode]
		if !ok {
			continue
		}
		if pe != nil && pe.Enabled() && pe.Holds(pos.Code) {
			continue // 纸面账本已持有：由 paper 侧统一处置并回写镜像，避免双账簿重复卖
		}
		d := v.Verdict.Disposal
		if d.Action == signalctl.SellActionTrim {
			if e.reportTrimDone[pos.Code] == td {
				continue // 每码每交易日最多一次（与旧链同口径）
			}
			half := int(pos.Quantity) / 2 / 100 * 100
			if half <= 0 {
				continue // 不足两手，半仓无意义
			}
			e.rpt.SellLot(pos.SignalID, v.Price, float64(half))
			e.reportTrimDone[pos.Code] = td
			log.Printf("[sell-judge:on] report %s(%s) %s 处置已执行=减仓 价%.2f 理由=%s",
				pos.Name, pos.Code, uid, v.Price, d.Reason)
			continue
		}
		e.rpt.LogExit(pos.SignalID, v.Price, "[统一裁决] "+d.Reason)
		log.Printf("[sell-judge:on] report %s(%s) %s 处置已执行=清仓 价%.2f 理由=%s",
			pos.Name, pos.Code, uid, v.Price, d.Reason)
	}
}

// sellUnifiedModeEngine 归一化卖出统一模式（双账共用读取口）：实盘控制器在位时取其**已生效**
// 配置（§QMT-PENDING 语义——休市变更不入裁定）；未接 QMT 时回退全局配置 qmt 段，
// 让 paper 并轨在实盘停用场景下同样可灰度。
func (e *Engine) sellUnifiedModeEngine() string {
	if ctrl := e.qmtCtrlRef(); ctrl != nil {
		return sellUnifiedModeOf(ctrl.Config())
	}
	e.mu.RLock()
	cm := e.cfgMgr
	e.mu.RUnlock()
	if cm != nil {
		return sellUnifiedModeOf(cm.Get().QMT)
	}
	return ""
}

// orDefault 空串回退默认名（日志用）。
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// sellUnifiedModeOf 归一化 qmt.sell_unified_mode（小写+去空格；""=shadow 缺省语义）。
// engine 侧多处消费（裁决/投影/执行来源闸），统一从此取，杜绝各读各的口径分叉。
func sellUnifiedModeOf(cfg config.QMTConfig) string {
	return strings.ToLower(strings.TrimSpace(cfg.SellUnifiedMode))
}

// qmtCtrlRef 取实盘交易控制器（只读引用，nil=未装配）。
func (e *Engine) qmtCtrlRef() *trading.Controller {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.qmtCtrl
}
