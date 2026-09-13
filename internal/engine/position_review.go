// Package engine 引擎层 · §DAILY_REVIEW 盘后持仓综合复盘。
// 交易日收盘后，对本账号"实盘持仓 ∪ 模拟盘持仓 ∪ 当日信号股 ∪ 自选股"逐票在 Go 侧算量化事实
// （量能/MACD/量价配合/均线/区间位置/浮盈），合并成一次 LLM 调用生成每股综合复盘正文 + 后市倾向
// （偏多/中性/偏空），以"复盘"级消息写入消息中心（Scope=本账号私有，按日去重 → 每日更新覆盖）。
// 自动：盘后休眠分支每日一次（reviewGuardDay 去重）；手动：RunPositionReviewNow（管理/前端按钮强制）。
// 依赖可注入（reviewDailyK/reviewAsk）以便离线单测；未配置 LLM Key 时静默跳过、不产消息。
// English: §DAILY_REVIEW after-hours per-account position review. Once per trading day after close it
// unions real/paper holdings, today's signals and the watchlist, computes technical FACTS in Go
// (volume / MACD / price-volume / MA / range position / unrealized P&L), makes ONE merged LLM call to
// draft a per-stock review + bias (偏多/中性/偏空), and upserts them as private "复盘" messages keyed by
// day (daily refresh). Auto (once/day) + manual (force). Deps are injectable for offline tests.
package engine

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/indicator"
)

// reviewBias* 后市倾向枚举（与 LLM 输出、消息 Direction 一致）。
const (
	reviewBiasBull = "偏多"
	reviewBiasMid  = "中性"
	reviewBiasBear = "偏空"
)

// reviewPriority 复盘候选来源优先级（值越小越先纳入，用于超出上限时的截断排序）：实盘>模拟盘>信号>自选。
// English: source priority for truncation when the universe exceeds the cap (lower = keep first).
const (
	reviewPrioReal   = 0
	reviewPrioPaper  = 1
	reviewPrioSignal = 2
	reviewPrioWatch  = 3
)

// reviewTarget 一只待复盘股票（含来源标记集合与成本基准）。
// reviewTarget is one stock to review, with its source tags (may hit multiple pools) and cost basis.
type reviewTarget struct {
	Code    string
	Name    string
	Cost    float64 // 持仓成本价（实盘/模拟盘），无持仓为 0
	Sources []string
	prio    int
}

// addSource 记录来源并维持最小优先级。
func (t *reviewTarget) addSource(name string, prio int) {
	for _, s := range t.Sources {
		if s == name {
			return
		}
	}
	t.Sources = append(t.Sources, name)
	if prio < t.prio {
		t.prio = prio
	}
}

// reviewUniverse 汇总并去重本账号的复盘候选（实盘/模拟/信号/自选），按优先级升序、代码升序排列。
// English: builds the deduped per-account review universe (real ∪ paper ∪ today-signals ∪ watchlist),
// sorted by source priority then code.
func (e *Engine) reviewUniverse() []reviewTarget {
	e.mu.RLock()
	rpt, wl, ps, signals := e.rpt, e.wlMgr, e.paper, e.signalStore
	userID, realDB := e.userID, e.realStore
	e.mu.RUnlock()

	byCode := make(map[string]*reviewTarget)
	// get 取或建某代码的复盘目标：码归一为 6 位数字、非数字码丢弃、首见者带出名称。
	get := func(code, name string) *reviewTarget {
		// 归一化：去后缀去空白，仅接受 6 位数字代码
		code = normalizeCode(strings.TrimSpace(code))
		if !isSixDigitCode(code) {
			return nil
		}
		// 惰性建条目；名称以最先带出者为准（信号/持仓自带中文名）
		t, ok := byCode[code]
		if !ok {
			t = &reviewTarget{Code: code, prio: 99}
			byCode[code] = t
		}
		if t.Name == "" {
			t.Name = strings.TrimSpace(name)
		}
		return t
	}
	// ① 实盘持仓：优先取账本（带名称/成本），无 realStore 时回退报表持仓码。
	if realDB != nil && userID != "" {
		if pos, err := realDB.RealPositionsForUser(userID); err == nil {
			for _, p := range pos {
				if t := get(p.TsCode, p.Name); t != nil {
					if p.CostPrice > 0 {
						t.Cost = p.CostPrice
					}
					t.addSource("实盘", reviewPrioReal)
				}
			}
		}
	} else if rpt != nil {
		codes := rpt.HeldPositionCodes()
		if userID != "" {
			codes = rpt.HeldPositionCodesFor(userID)
		}
		for _, c := range codes {
			if t := get(c, ""); t != nil {
				t.addSource("实盘", reviewPrioReal)
			}
		}
	}
	// ② 模拟盘持仓（本引擎账本，带名称/成本）。
	if ps != nil {
		for _, p := range ps.Positions() {
			if t := get(p.Code, p.Name); t != nil {
				if p.CostPrice > 0 {
					t.Cost = p.CostPrice
				}
				t.addSource("模拟盘", reviewPrioPaper)
			}
		}
	}
	// ③ 当日信号固化库（含名称）。
	if signals != nil {
		for _, s := range signals.List() {
			if t := get(s.Code, s.Name); t != nil {
				t.addSource("信号", reviewPrioSignal)
			}
		}
	}
	// ④ 自选股。
	if wl != nil {
		for _, c := range wl.List(userID) {
			if t := get(c, ""); t != nil {
				t.addSource("自选", reviewPrioWatch)
			}
		}
	}

	out := make([]reviewTarget, 0, len(byCode))
	for _, t := range byCode {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].prio != out[j].prio {
			return out[i].prio < out[j].prio
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// maLast 返回 closes 的 n 日均线末值（样本不足返回 NaN）。
// maLast returns the last n-period simple moving average (NaN if insufficient samples).
func maLast(xs []float64, n int) float64 {
	if n <= 0 || len(xs) < n {
		return math.NaN()
	}
	var s float64
	for _, v := range xs[len(xs)-n:] {
		s += v
	}
	return s / float64(n)
}

// reviewFactForCode 用日 K 计算一只股票的量化事实单行串（供 LLM 依据，含浮盈）。
// K 线不足（<30 根，MACD/均线口径不完整）时返回 "数据不足" 提示而非编造。
// English: renders a compact one-line technical FACT string for a stock from its daily K lines
// (volume/MACD/price-volume/MA/range/P&L); returns a "data insufficient" note under 30 bars.
func reviewFactForCode(t reviewTarget, kl []data.KLine) string {
	if len(kl) < 30 {
		return fmt.Sprintf("%s %s｜日K样本不足(%d根)，暂不复盘", t.Code, t.Name, len(kl))
	}
	n := len(kl)
	closes := make([]float64, n)
	vols := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	for i, k := range kl {
		closes[i], vols[i], highs[i], lows[i] = k.Close, k.Volume, k.High, k.Low
	}
	last, prev := n-1, n-2
	price := closes[last]
	chg := (price/closes[prev] - 1) * 100
	ret5, ret20 := (price/closes[n-6]-1)*100, (price/closes[n-21]-1)*100

	// 均线与排列判定
	ma5, ma10, ma20 := maLast(closes, 5), maLast(closes, 10), maLast(closes, 20)
	arrange := "均线纠缠"
	switch {
	case ma5 >= ma10 && ma10 >= ma20:
		arrange = "多头排列"
	case ma5 <= ma10 && ma10 <= ma20:
		arrange = "空头排列"
	}

	// 量比：今量 / 前 5 日均量
	var avg5 float64
	for i := n - 6; i < n-1; i++ {
		avg5 += vols[i]
	}
	avg5 /= 5
	volRatio := 0.0
	if avg5 > 0 {
		volRatio = vols[last] / avg5
	}
	volTag := "平量"
	switch {
	case volRatio >= 1.8:
		volTag = "显著放量"
	case volRatio >= 1.2:
		volTag = "温和放量"
	case volRatio > 0 && volRatio <= 0.7:
		volTag = "明显缩量"
	case volRatio <= 0.7:
		volTag = "缩量"
	}

	// 量价配合：近 10 日 上涨日均量 vs 下跌日均量
	var upV, dnV float64
	var upN, dnN int
	for i := n - 10; i < n; i++ {
		if closes[i] >= closes[i-1] {
			upV += vols[i]
			upN++
		} else {
			dnV += vols[i]
			dnN++
		}
	}
	pvTag := "量价均衡"
	if upN > 0 && dnN > 0 {
		au, ad := upV/float64(upN), dnV/float64(dnN)
		if ad > 0 && au >= ad*1.15 {
			pvTag = "涨时放量(配合偏多)"
		} else if ad > 0 && au <= ad*0.85 {
			pvTag = "涨时缩量/跌时放量(量价背离)"
		}
	}

	// MACD（12,26,9）：零轴上下 + 多空 + 柱体收敛/发散 + 近 3 日金叉/死叉
	macdTag := "MACD 数据不足"
	if pts := indicator.MACD(closes, 12, 26, 9); len(pts) >= 2 {
		cur, pre := pts[len(pts)-1], pts[len(pts)-2]
		if !math.IsNaN(cur.DIF) && !math.IsNaN(cur.DEA) {
			axis := "水下"
			if cur.DIF >= 0 {
				axis = "水上"
			}
			state := "空头(DIF<DEA)"
			if cur.DIF > cur.DEA {
				state = "多头(DIF>DEA)"
			}
			hist := "柱收敛"
			if cur.Bar >= pre.Bar {
				hist = "柱扩张"
			}
			cross := ""
			for i := len(pts) - 3; i < len(pts); i++ {
				if i-1 < 0 || math.IsNaN(pts[i].DIF) || math.IsNaN(pts[i-1].DIF) {
					continue
				}
				if pts[i].DIF > pts[i].DEA && pts[i-1].DIF <= pts[i-1].DEA {
					cross = " 刚金叉"
					break
				}
				if pts[i].DIF < pts[i].DEA && pts[i-1].DIF >= pts[i-1].DEA {
					cross = " 刚死叉"
					break
				}
			}
			macdTag = fmt.Sprintf("MACD %s %s%s 柱%.2f", axis, state, cross, cur.Bar)
			_ = hist
		}
	}

	// RSI14
	rsiTag := ""
	if rsi := indicator.RSI14(closes); len(rsi) > 0 && !math.IsNaN(rsi[len(rsi)-1]) {
		rsiTag = fmt.Sprintf(" RSI14 %.0f", rsi[len(rsi)-1])
	}

	// 20 日区间位置
	hi, lo := highs[last], lows[last]
	for i := n - 20; i < n; i++ {
		if highs[i] > hi {
			hi = highs[i]
		}
		if lows[i] < lo {
			lo = lows[i]
		}
	}
	posPct, fromHigh := 50.0, 0.0
	if hi > lo {
		posPct = (price - lo) / (hi - lo) * 100
		fromHigh = (price/hi - 1) * 100
	}

	pnlTag := ""
	if t.Cost > 0 {
		pnlTag = fmt.Sprintf("｜成本%.2f 浮%+.1f%%", t.Cost, (price/t.Cost-1)*100)
	}
	return fmt.Sprintf("%s %s｜来源:%s｜现价%.2f 日%+.1f%% 5日%+.1f%% 20日%+.1f%%｜MA5/10/20 %.2f/%.2f/%.2f %s｜量比%.2f %s 量价:%s｜%s%s｜位置%.0f%%(距20日高%+.1f%%)%s",
		t.Code, t.Name, strings.Join(t.Sources, "·"), price, chg, ret5, ret20,
		ma5, ma10, ma20, arrange, volRatio, volTag, pvTag, macdTag, rsiTag,
		posPct, fromHigh, pnlTag)
}

// reviewSystemPrompt 是合并复盘的系统提示：强约束"只用给定量化事实、不得编造、逐行固定格式输出"。
const reviewSystemPrompt = `你是一名严谨的A股技术面复盘助手。用户会给出若干股票的"量化事实"（均由系统从日线计算的客观数据：现价/涨跌幅、均线多空排列、量比、量价配合、MACD、RSI、20日区间位置、持仓成本浮盈）。请为每一只股票写一段综合复盘，严格遵守：
1) 只依据给出的量化事实分析，禁止编造事实中没有的消息面/基本面/资金面数据；数据不足以判断的地方明说"数据有限"。
2) 每只给一个后市倾向：偏多 / 中性 / 偏空（三选一）。
3) 正文3-4句：①趋势与所处位置 ②量能与MACD解读（结合量价配合）③风险提示 ④后市可能走向与关键支撑/压力位（用给出价位近似）。
4) 输出格式：每只股票占一行，形如  CODE|倾向|正文 。代码用给定的6位数字代码，一行内不要换行，不要输出多余解释或markdown代码块。正文每只不超过120字，整体不要输出思考过程。`

// reviewTokenBudget 按标的数给合并复盘的推理长度预算：每只约 300 token，下限 2048、上限 8192。
// English: reasoning-token budget for the merged review call: ~300/stock, floored 2048, capped 8192.
func reviewTokenBudget(stocks int) int {
	b := 300 * stocks
	if b < 2048 {
		b = 2048
	}
	if b > 8192 {
		b = 8192
	}
	return b
}

// buildReviewPrompt 组装合并 user 提示词（日期 + 每股事实）。
// buildReviewPrompt assembles the merged user prompt (date + one fact line per stock).
func buildReviewPrompt(facts []string, dateStr string) string {
	var b strings.Builder
	b.WriteString("交易日收盘后复盘。复盘日期：" + dateStr + "。请逐只输出，每行一只（CODE|倾向|正文）：\n\n")
	for _, f := range facts {
		b.WriteString(f + "\n")
	}
	return b.String()
}

// parseReviewResponse 解析 LLM 逐行输出为 code→(倾向,正文)。容忍 markdown 围栏与代码后缀。
// English: parses the LLM per-line output into code→(bias,text); tolerates code fences and suffixes.
func parseReviewResponse(resp string) map[string][2]string {
	out := make(map[string][2]string)
	for _, raw := range strings.Split(resp, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "```")
		line = strings.Trim(line, "` ")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 2 {
			continue
		}
		code := normalizeCode(strings.TrimSpace(parts[0]))
		if !isSixDigitCode(code) {
			continue // 代码列非 6 位数字 → 非有效行（防 LLM 输出格式漂移时误吞标题行）
		}
		bias := strings.TrimSpace(parts[1])
		switch bias {
		case reviewBiasBull, reviewBiasMid, reviewBiasBear:
		default:
			bias = reviewBiasMid
		}
		text := ""
		if len(parts) == 3 {
			text = strings.TrimSpace(parts[2])
		}
		if text == "" {
			continue
		}
		out[code] = [2]string{bias, text}
	}
	return out
}

// runPositionReview 执行一次复盘（force=手动，忽略每日去重与交易时段/盘后门控）。
// 返回成功产出的复盘消息条数与错误（无任何可复盘标的 / 无 LLM / 调用失败 时返回 error 或 0）。
// English: runs one review pass (force=manual bypasses the daily/session gates). Returns the number of
// review messages produced and an error (no universe / no LLM / LLM failure).
func (e *Engine) runPositionReview(now time.Time, force bool) (int, error) {
	e.mu.Lock()
	if e.reviewRunning {
		e.mu.Unlock()
		return 0, fmt.Errorf("复盘进行中，稍后再试")
	}
	day := now.Format("2006-01-02")
	if !force {
		if e.reviewGuardDay == day {
			e.mu.Unlock()
			return 0, nil // 当日已自动复盘
		}
		e.reviewGuardDay = day
	}
	e.reviewRunning = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.reviewRunning = false
		e.mu.Unlock()
	}()

	// 解析依赖（可注入优先，否则回退引擎现成能力）。
	e.mu.RLock()
	msgStore, userID := e.msgStore, e.userID
	e.mu.RUnlock()
	fetchDaily := e.reviewDailyK
	if fetchDaily == nil {
		e.mu.RLock()
		mkt := e.marketAPI
		e.mu.RUnlock()
		if mkt != nil {
			fetchDaily = func(c string) ([]data.KLine, error) { return mkt.GetSinaKLine(c, 70) }
		}
	}
	if msgStore == nil || fetchDaily == nil {
		return 0, fmt.Errorf("复盘依赖未就绪（需 msgStore/日K源）")
	}

	limit := 24
	if e.cfgMgr != nil {
		limit = e.cfgMgr.Rules.Runtime.ReviewMax()
	}
	universe := e.reviewUniverse()
	if len(universe) > limit {
		universe = universe[:limit]
	}
	if len(universe) == 0 {
		log.Printf("[review] %s 无可复盘标的（持仓/信号/自选均空），跳过", scopeLabel(userID))
		return 0, nil
	}

	// 逐票取日 K 算事实（单票失败不影响整体，标注数据不足）。
	facts := make([]string, 0, len(universe))
	for _, t := range universe {
		kl, err := fetchDaily(t.Code)
		if err != nil || len(kl) == 0 {
			facts = append(facts, fmt.Sprintf("%s %s｜日K获取失败，暂不复盘", t.Code, t.Name))
			continue
		}
		facts = append(facts, reviewFactForCode(t, kl))
	}

	// LLM 依赖后置解析：按标的数给推理长度预算（每只 ~300 token，2048 起、8192 封顶），
	// 默认走非流式+限 token 的 ChatD1，控制合并长输出的总延迟（普通 Chat 流式对长回答不稳）。
	ask := e.reviewAsk
	if ask == nil {
		e.mu.RLock()
		lc := e.llmClient
		e.mu.RUnlock()
		if lc == nil {
			return 0, fmt.Errorf("未配置 LLM，跳过复盘")
		}
		budget := reviewTokenBudget(len(universe))
		ask = func(s, u string) (string, error) { return lc.ChatD1(s, u, budget) }
	}

	resp, err := ask(reviewSystemPrompt, buildReviewPrompt(facts, now.Format("2006年01月02日")))
	if err != nil {
		if !force {
			e.mu.Lock()
			e.reviewGuardDay = "" // 失败回退当日守卫，允许后续唤醒重试
			e.mu.Unlock()
		}
		return 0, fmt.Errorf("LLM 复盘调用失败: %w", err)
	}
	parsed := parseReviewResponse(resp)

	items := make([]data.MessageItem, 0, len(parsed))
	for _, t := range universe {
		pair, ok := parsed[t.Code]
		if !ok {
			continue // LLM 未覆盖该股则跳过，不产出低质占位
		}
		bias, text := pair[0], pair[1]
		title := t.Name
		if title == "" {
			title = t.Code
		}
		items = append(items, data.MessageItem{
			ID:          fmt.Sprintf("pos-review@%s@%s@%s", userID, t.Code, day),
			Code:        t.Code,
			Name:        t.Name,
			Level:       "复盘",
			Action:      bias,
			Strategy:    "盘后复盘",
			Time:        nowTimeString(),
			Title:       fmt.Sprintf("收盘复盘·%s(%s)", title, t.Code),
			Body:        text + "\n\n—— 量化事实 ——\n" + factFor(t, facts),
			Direction:   bias,
			GeneratedAt: now,
			Scope:       userID,
		})
	}
	if len(items) == 0 {
		if !force {
			e.mu.Lock()
			e.reviewGuardDay = ""
			e.mu.Unlock()
		}
		log.Printf("[review] %s LLM 未解析出有效复盘（响应长度 %d）", scopeLabel(userID), len(resp))
		return 0, nil
	}
	// 只保留当日复盘：删除历史日期的 复盘 消息（ID 含日期后缀，非同日用 Delete 清理，防逐日堆积）。
	// Keep only today's reviews: purge stale-dated "复盘" items so the center doesn't accumulate per day.
	suffix := "@" + day
	for _, m := range msgStore.List() {
		if m.Level == "复盘" && !strings.HasSuffix(m.ID, suffix) {
			msgStore.Delete(m.ID)
		}
	}
	msgStore.Sync(items)
	e.pushSSEMessages(items)
	log.Printf("[review] %s 盘后复盘完成：%d 只", scopeLabel(userID), len(items))
	return len(items), nil
}

// factFor 从事实串列表里取该 code 对应的一行（消息正文附事实，便于事后回看核对）。
func factFor(t reviewTarget, facts []string) string {
	prefix := t.Code + " "
	for _, f := range facts {
		if strings.HasPrefix(f, prefix) {
			return f
		}
	}
	return ""
}

// scopeLabel 把空 userID（单账号/公共）标注为 "(公共)"，否则回显账号，日志友好。
func scopeLabel(userID string) string {
	if userID == "" {
		return "(公共)"
	}
	return userID
}

// isSixDigitCode 判断是否为 6 位纯数字股票代码。
// isSixDigitCode reports whether s is a 6-digit numeric A-share code.
func isSixDigitCode(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ReviewPositionsIfDue 盘后复盘自动入口：仅在开关开启、非交易时段、收盘钟点后、交易日，且当日未跑过时执行一次。
// 由主循环休眠分支（registry.All 逐引擎）周期唤醒调用，靠 reviewGuardDay 保证每日一次。
// English: auto entry called from the main loop's after-hours branch per engine. Runs once per trading
// day after the close hour when enabled; guarded by reviewGuardDay (the branch wakes ~every 15min).
func (e *Engine) ReviewPositionsIfDue(now time.Time) {
	if e.cfgMgr == nil {
		return
	}
	rc := e.cfgMgr.Rules.Runtime
	if !rc.ReviewOn() || data.IsActiveSession(now) {
		return
	}
	// 仅收盘后 15:00-23:59 触发（避开午间休市与午夜信号已清空后的空跑）。
	if h := now.Hour(); h < 15 || h >= 24 {
		return
	}
	// 非交易日（周末/节假日）不复盘。
	if !data.IsTradingDay(now) {
		return
	}
	if _, err := e.runPositionReview(now, false); err != nil {
		log.Printf("[review] 自动复盘未成功: %v", err)
	}
}

// RunPositionReviewNow 手动强制复盘（忽略每日去重与时段门控），返回复盘股票数与错误。
// RunPositionReviewNow force-runs a review (ignoring the daily/session gates) and returns the count.
func (e *Engine) RunPositionReviewNow() (int, error) {
	return e.runPositionReview(time.Now(), true)
}
