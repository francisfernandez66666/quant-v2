// consult.go — 股票咨询（多轮对话）从 engine.go 拆出的独立文件（2026-10-26，§ENGINE-SPLIT W1）。
//
// 行为零改动：只搬文件位置，代码逐字未改。拆出理由：engine.go 已 7616 行，
// 咨询这一族（LLM 调用 / 上下文装配 / 数字审计 / 分股块缓存 / 历史读写）与交易主链
// 无状态耦合，适合作为第一批外移单元；后续可继续升级为子包。
//
// English: verbatim move of the stock-consult family out of engine.go; no behavior change.
package engine

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// ── 股票咨询（多轮对话）──

// consultHistoryLimit 送入模型的多轮历史上限（最近 N 条消息，约 3 组问答）。
// 只带近期上下文，避免历史劣质回复持续污染后续判断，也降低小模型长上下文注意力漂移。
const consultHistoryLimit = 6

// ConsultLLM 以多轮对话方式调用 LLM 生成咨询回复（股票咨询页使用）。
// 组装顺序：唯一一条 system（角色提示词，专业模式时并入实时行情数据）→ 历史最近 N 条 → 当前提问。
// LLM 未配置时返回错误提示前端引导配置；回复生成后同步追加到当日对话历史（跨交易日自动清空）。
// §FIX-4(20260919)：首参改为请求 ctx（handleConsult 传 r.Context()）——用户断开/超时取消后，
// 出呼链（退避→流式读→HTTP）随之中止，不再悬空计费。
func (e *Engine) ConsultLLM(ctx context.Context, userID, userMsg string, proMode bool) (string, error) {
	e.mu.RLock()
	client := e.llmClient
	e.mu.RUnlock()
	if client == nil {
		return "", fmt.Errorf("未配置 LLM_API_KEY，请先在股票咨询页配置 API Key")
	}

	// §FIX-10(20260919 批四)：出呼前先确认账号隔离存储可用——不可用（nil）时整轮咨询
	// 无法落盘，历史会静默丢失且跨账号有串号风险，直接拒绝（上层映射 503）。
	// 放在数据块抓取与付费调用**之前**：拒绝即零外呼、零计费。
	store := e.consultStoreFor(userID)
	if store == nil {
		return "", fmt.Errorf("咨询: %w", data.ErrStoreUnavailable)
	}

	// system 起始即角色提示词。§生产 20260916 翻转：带数据上下文改为**无条件注入**——
	// 数据是咨询的默认构成（用户实录"AI 顾问空口谈逻辑"是缺陷），不再由开关决定有无；
	// proMode 仅追加更定量化/结构化的深度分析风格要求。未识别到个股时给 noStock 提示词，
	// 引导模型如实说明无数据、不编造。
	// §FIX-2(20260919 批三)：数据块用 ⟦DATA⟧/⟦/DATA⟧ 边界包裹后注入——边界让模型能区分
	// "实测数据"与"角色指令"，更重要的是 trusted 白名单从此**只采边界内数据块 + 用户消息 +
	// 历史**，提示词模板彻底出局（旧实现连 system 一起抽，"错误示范"假数字 2383万/1.2亿
	// 被模型原样复现即放行，守卫自击穿）。
	system := llm.ConsultSystemPrompt()
	dataCtx := e.buildConsultContext(userMsg)
	// §CONSULT-DATARICH(2026-11-10) A 期：补数轮追加在**边界包裹之前**，因此补充段与主块
	// 同在 ⟦DATA⟧ 内、同被 collectTrustedNumbers 采集（审计零改动）。返回空串＝今天的行为：
	// 开关关、预算只够一次、超时、JSON 解不出、取数失败，任何一条都静默跳过。
	if dataCtx != "" {
		dataCtx += e.consultDataRound(ctx, client, userMsg)
	}
	if dataCtx != "" {
		system += "\n\n⟦DATA⟧\n" + dataCtx + "\n⟦/DATA⟧"
	} else {
		system += "\n\n" + consultNoStockPrompt
	}
	if proMode {
		system += "\n\n（专业模式：请在回答中给出更定量化、结构化的深度分析，明确列出数据依据与风险点。）"
	}

	// 历史：仅取最近 consultHistoryLimit 条（正序）。store 可用性已在函数入口校验（§FIX-10）。
	messages := make([]llm.Message, 0, consultHistoryLimit+2)
	if hist := store.List(); len(hist) > 0 {
		if len(hist) > consultHistoryLimit {
			hist = hist[len(hist)-consultHistoryLimit:]
		}
		for _, m := range hist {
			messages = append(messages, llm.Message{Role: m.Role, Content: m.Content})
		}
	}
	messages = append(messages, llm.Message{Role: "user", Content: userMsg})

	// 完整消息序列：system 在最前，后接历史与当前提问。
	msgs := append([]llm.Message{{Role: "system", Content: system}}, messages...)

	reply, err := client.ChatMessagesCtx(ctx, msgs)
	if err != nil {
		// %w 保留错误链：handleConsult 用 errors.Is(err, llm.ErrBudgetExceeded) 区分"额度用尽(429)"
		// 与上游故障(500)。用 %v 会截断链路，429 语义静默退化为 500。
		return "", fmt.Errorf("咨询调用失败: %w", err)
	}

	// 数字审计：剔除模型编造、没有任何可信出处的金钱/数量类数字（金额、成交量、笔数等）。
	// 可信来源=注入的实时行情数据块 + 用户自己的描述 + 此前已落盘的历史（已在此前被审计过）。
	// §FIX-2：来源清单里**没有** system——提示词模板（含反面教材假数字）不参与白名单采集。
	histTexts := make([]string, 0, len(messages))
	for _, m := range messages {
		histTexts = append(histTexts, m.Content)
	}
	trusted := collectTrustedNumbers(append([]string{dataCtx, userMsg}, histTexts...)...)
	// §FIX-5(4)：提示词允许模型用现价/昨收推算涨跌幅，推算值按 ±0.5pp 容差放行。
	trusted.addDerivedPct(dataCtx)
	reply = auditNumbers(reply, trusted)

	// 对话历史落盘：一轮"提问+回复"合并为一次落盘（§FIX-10：fsync 减半、无半轮窗口；
	// §GAP2-W2 写入本人账号目录）。
	store.AppendPair(data.ConsultMessage{Role: "user", Content: userMsg},
		data.ConsultMessage{Role: "assistant", Content: reply})
	return reply, nil
}

// auditedNumberRe 匹配带金融单位的数字：金额（万元/亿元/元）、成交量（万股/亿股）、笔数/手数，
// 及百分比与倍数（% / 倍）——这些同样是模型幻觉的高发区。
// 支持"万/亿"紧邻 笔/手/股 的组合（如 2.3万笔、1.2亿股）。
// §FIX-5(20260919 批三)：数字与单位之间允许空白（模型常写"净流出 2.22 亿元"），否则漏审。
// §CONSULT-DATARICH(2026-11-10)：补"万手/亿手"两个组合后缀，且**必须排在裸"万/亿"之前**——
// 正则交替是最左优先，"3.2万手"会先被"万"截成"3.2万"（catAmbiguous），
// 只往 numUnitTable 加后缀而不动本正则＝那条后缀永远走不到（空操作）。
// 刻意不匹配：时间、股票代码、时长、日期，以及"点/个"等无单位口径字段——避免误伤，
// 数据块里的无单位复述（如"12个点"）天然不进白名单也不被审，后人勿再加宽单位表。
// English: matches unit-bearing finance numbers (space between digits and unit allowed);
// the 万手/亿手 compounds must precede bare 万/亿 or the alternation truncates them;
// deliberately skips times/codes/dates and unit-less phrasings.
var auditedNumberRe = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?\s*(?:万元|亿元|元|万股|亿股|万手|亿手|万|亿|手|笔|[%％]|倍)`)

// numCat §FIX-5：带单位数字的量纲类别。金额/股数/笔数/百分比/倍数互不等价，
// 只在同类内做单位换算归一（万元↔亿元）；catAmbiguous 专治裸"万/亿"（"2.3万笔"
// 会被正则截成 "2.3万"，量纲未知），允许命中任意类别的基础量。
// §CONSULT-DATARICH(2026-11-10)：catLots（手）从 catCount 里分出来——手与股是同一物理量的
// 两种口径（1手=100股，允许换算），而"笔"是成交笔数、与股数毫无关系。
// 混在一类时放开手↔股换算等于顺手放行"2.3万笔 ↔ 230万股"这种跨量纲冒充。
// English: lot counts split from transaction counts so the lot↔share conversion
// (1 lot = 100 shares) can never legitimise a cross-dimension "笔 ↔ 股" match.
type numCat int

const (
	catAmbiguous numCat = iota // 裸"万/亿"，量纲未知
	catMoney                   // 元/万元/亿元
	catShares                  // 万股/亿股（基础量＝股）
	catCount                   // 笔（成交笔数，不可与股/手换算）
	catPct                     // % / ％
	catMult                    // 倍
	catLots                    // 手/万手/亿手（基础量＝手，可与 catShares 按 ×100 换算）
)

// lotToShares 手→股的换算倍数（A股 1手=100股）。只用于 catLots↔catShares 的跨类比对，
// 不参与同类归一——同类的基础量本来就是手/股本身。
const lotToShares = 100.0

// numUnitTable token 后缀 →（量纲类别, 归一到基础量的乘数）。长后缀必须排在前，
// 保证 "万元" 先于 "万" 命中（HasSuffix 顺序扫描）。
// §CONSULT-DATARICH：万手/亿手的基础量是**手**，故乘数为 1e4/1e8（与万股/亿股同形），
// 不是 100/1e6——把"手→股"的 100 混进前缀乘数会让 3.2万手 归一成 320 手，差 100 倍。
var numUnitTable = []struct {
	suffix string
	cat    numCat
	mult   float64
}{
	{"万元", catMoney, 1e4}, {"亿元", catMoney, 1e8},
	{"万股", catShares, 1e4}, {"亿股", catShares, 1e8},
	{"万手", catLots, 1e4}, {"亿手", catLots, 1e8},
	{"元", catMoney, 1}, {"万", catAmbiguous, 1e4}, {"亿", catAmbiguous, 1e8},
	{"手", catLots, 1}, {"笔", catCount, 1},
	{"%", catPct, 1}, {"％", catPct, 1}, {"倍", catMult, 1},
}

// splitAuditedToken 把 token 拆成「数字部分」与「单位后缀」（数字与单位间的空白已剥）。
// 解析与"写到哪一位"的精度判定共用这一处切法：两处各切一遍，精度就会按另一种口径算。
// English: single split shared by value parsing and written-precision detection.
func splitAuditedToken(tok string) (numPart, suffix string) {
	numPart = strings.TrimRight(tok[:strings.LastIndexAny(tok, "0123456789.")+1], " ")
	return numPart, strings.TrimSpace(tok[len(numPart):])
}

// parseAuditedNumberToken 把 "-22200.00 万元" 这类 token 拆成（量纲类别, 基础量纲数值）。
// 换算归一是 FIX-5 的核心："-22200万元" 与 "2.22亿元" 落到同一个基础值 -2.22e8，
// 模型换口径复述不再被当成编造。
func parseAuditedNumberToken(tok string) (numCat, float64, bool) {
	numPart, suffix := splitAuditedToken(tok)
	for _, u := range numUnitTable {
		if suffix == u.suffix {
			f, err := strconv.ParseFloat(numPart, 64)
			if err != nil {
				return 0, 0, false
			}
			return u.cat, f * u.mult, true
		}
	}
	return 0, 0, false
}

// auditedTokenGranularity 回复里这个数字**写到了哪一位**（该位的一个单位，换算到基础量纲）。
// "36.1元"→0.1元、"2.2亿"→0.1亿=1e7元、"45800手"→1手。
// §CONSULT-DATARICH(2026-11-10)：舍入容差按它的一半取，而不是按一个固定的相对百分比。
// 为什么不用"±5% 相对容差"：现价 22.09元 的 5% 是 ±1.10元，模型写"23.00元"也会被当成
// 四舍五入放行——那不是舍入而是报错价。按写出的精度取容差，"23.00元"只允许 ±0.005元，
// 而"2.2亿"允许 ±5e6（2.22亿 舍入到 2.2亿 正是合法复述），大额与价格自动各得其所。
// English: tolerance derives from the precision actually written, not a flat percentage —
// a flat 5% would let a wrong price (22.09 → 23.00) pass as "rounding".
func auditedTokenGranularity(tok string) float64 {
	numPart, suffix := splitAuditedToken(tok)
	mult := 1.0
	for _, u := range numUnitTable {
		if suffix == u.suffix {
			mult = u.mult
			break
		}
	}
	decimals := 0
	if i := strings.Index(numPart, "."); i >= 0 {
		decimals = len(numPart) - i - 1
	}
	return math.Pow(10, -float64(decimals)) * mult
}

// trustedNumbers §FIX-5：可信数字集合——按量纲分组的"有出处"基础量 + 推算涨跌幅容差清单。
type trustedNumbers struct {
	vals       map[numCat]map[float64]bool // 类别 → 集合（保留正负号）
	derivedPct []float64                   // 现价/昨收推算值，±0.5pp 容差放行（FIX-5(4)）
}

// derivedPctTolerance 推算涨跌幅的放行容差（百分点）：模型按现价/昨收手算常四舍五入到 1~2 位小数。
const derivedPctTolerance = 0.5

// collectTrustedNumbers 从可信文本（数据块边界内原文、用户描述、历史消息）收集"有出处的数字"。
// §FIX-2(20260919 批三)：调用方**严禁**再把 system 提示词模板传进来——模板"错误示范"里的
// 假数字（2383万/1.2亿）一旦入白名单，守卫即被模型原样复现击穿。
func collectTrustedNumbers(texts ...string) *trustedNumbers {
	t := &trustedNumbers{vals: map[numCat]map[float64]bool{}}
	for _, text := range texts {
		for _, tok := range auditedNumberRe.FindAllString(text, -1) {
			cat, val, ok := parseAuditedNumberToken(tok)
			if !ok {
				continue
			}
			if t.vals[cat] == nil {
				t.vals[cat] = map[float64]bool{}
			}
			t.vals[cat][val] = true
		}
	}
	return t
}

// isEmpty 无任何可信数字：审计整体跳过（维持历史语义——无锚点时宁可放行不误伤）。
func (t *trustedNumbers) isEmpty() bool {
	return len(t.vals) == 0 && len(t.derivedPct) == 0
}

// consultDerivedPairRe 在数据块同一行内配对"现价 X元 … 昨收 Y元"，供推算涨跌幅白名单。
var consultDerivedPairRe = regexp.MustCompile(`现价\s*([-+]?\d+(?:\.\d+)?)\s*元[^\n]*?昨收\s*([-+]?\d+(?:\.\d+)?)\s*元`)

// addDerivedPct §FIX-5(4)：提示词明确允许模型用现价/昨收推算涨跌幅，推算值天然不在
// 白名单——从数据块抽出（现价,昨收）对算出推算锚点，审计按 ±0.5pp 容差放行。
func (t *trustedNumbers) addDerivedPct(dataCtx string) {
	for _, m := range consultDerivedPairRe.FindAllStringSubmatch(dataCtx, -1) {
		price, err1 := strconv.ParseFloat(m[1], 64)
		prevClose, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil || prevClose <= 0 {
			continue
		}
		t.derivedPct = append(t.derivedPct, (price/prevClose-1)*100)
	}
}

// reverseWordAnchors §FIX-5(3)：反向措辞保守锚——仅当模型用**正数**复述、数据侧存在同绝对值
// **负数**、且数字前文含下列词时才放行（数据"-22200万元" ↔ 回复"净流出2.22亿元"）。
// 反向（负数复述正数锚点）刻意不放行：数据里"涨5.67%"配"跌5.67%"的表述更可能是方向性编造。
var reverseWordAnchors = []string{"跌", "流出", "下降", "回落", "减少", "亏损", "下挫", "下滑"}

// hasReverseAnchor 只看数字前 10 个字符的近邻上下文，避免远距离词误配。
func hasReverseAnchor(prefix string) bool {
	r := []rune(prefix)
	if len(r) > 10 {
		r = r[len(r)-10:]
	}
	s := string(r)
	for _, w := range reverseWordAnchors {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// approxEqual 浮点相等（相对容差 1e-6）：吸收 万↔亿 换算噪声（2.22×1e8 与 22200×1e4 尾差）。
func approxEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 1e-6*math.Max(1, math.Abs(b))
}

// setHit 集合内是否有与 val 近似相等的成员（集合规模只有几十，线性扫描即可）。
func setHit(set map[float64]bool, val float64) bool {
	for v := range set {
		if approxEqual(v, val) {
			return true
		}
	}
	return false
}

// roundHit 集合内是否有「与 val 同号、且差不超过 tol」的成员（tol＝回复写出的半个精度）。
// §CONSULT-DATARICH：同号是硬边界——正数不许冒充负数，反向复述仍只走 reverseWordAnchors
// 那条保守规则（它额外要求近邻措辞锚）。零值不参与容差比对（0 与任何数都"接近"是假放行）。
// English: same-sign rounding match; zero never participates, and sign flips still require
// the conservative reverse-word anchor rule.
func roundHit(set map[float64]bool, val, tol float64) bool {
	if tol <= 0 || val == 0 {
		return false
	}
	for v := range set {
		if v == 0 || (v > 0) != (val > 0) {
			continue
		}
		if math.Abs(v-val) <= tol {
			return true
		}
	}
	return false
}

// allows 判定回复中一个数字 token（类别+基础量）是否有可信出处；prefix 为该数字之前的回复文本，
// gran 为该 token 写出的精度（auditedTokenGranularity），舍入档按它的一半取容差。
// English: decides whether a replied number has a trusted origin; gran is the precision
// actually written and drives the rounding tolerance (half a unit of that precision).
func (t *trustedNumbers) allows(cat numCat, val float64, prefix string, gran float64) bool {
	// 1) 同量纲精确命中（含单位换算归一后的相等）。
	if setHit(t.vals[cat], val) {
		return true
	}
	// 2) 裸"万/亿"量纲未知：任意类别命中即放行（"2.3万笔"截自 "2.3万" 的兼容路径）。
	if cat == catAmbiguous {
		for _, set := range t.vals {
			if setHit(set, val) {
				return true
			}
		}
	}
	// 3) 同号相反 + 反向措辞锚（保守规则，见 reverseWordAnchors 注释）。
	if val > 0 && setHit(t.vals[cat], -val) && hasReverseAnchor(prefix) {
		return true
	}
	// 4) 百分比推算容差：现价/昨收推算值 ±0.5pp 内放行（正负两种措辞都按绝对值比对）。
	if cat == catPct {
		for _, d := range t.derivedPct {
			if math.Abs(val-d) <= derivedPctTolerance || math.Abs(math.Abs(val)-math.Abs(d)) <= derivedPctTolerance {
				return true
			}
		}
	}
	// 5) §CONSULT-DATARICH(2026-11-10) 舍入档：同量纲、同号、差 ≤ 半个写出精度。
	// 覆盖"36.10→36.1""22200万→2.2亿"这类合法复述；按精度而非固定百分比取容差，
	// 因此"22.09元→23.00元"这种报错价仍被拦（它的写出精度是 0.01元）。
	tol := gran / 2
	if roundHit(t.vals[cat], val, tol) {
		return true
	}
	if cat == catAmbiguous {
		for _, set := range t.vals {
			if roundHit(set, val, tol) {
				return true
			}
		}
	}
	// 6) §CONSULT-DATARICH 手↔股换算档：数据块给"4580万股"、模型复述"45.8万手"不再被判编造。
	// 只在 catLots 与 catShares 之间开（catCount＝笔不参与，见 numCat 注释）；精度同步换算，
	// 否则换算后的容差会按另一把尺子取。
	switch cat {
	case catLots:
		shares, shTol := val*lotToShares, tol*lotToShares
		if setHit(t.vals[catShares], shares) || roundHit(t.vals[catShares], shares, shTol) {
			return true
		}
	case catShares:
		lots, lotTol := val/lotToShares, tol/lotToShares
		if setHit(t.vals[catLots], lots) || roundHit(t.vals[catLots], lots, lotTol) {
			return true
		}
	}
	return false
}

// auditNumbers 扫描模型回复中的金融数字，凡数值无可信出处（不在 trusted 集合中）即替换为数据缺失标注。
// 仅替换带单位的金钱/数量类数字，避免误伤百分比、时间、代码等。
func auditNumbers(reply string, trusted *trustedNumbers) string {
	if trusted == nil || trusted.isEmpty() {
		return reply
	}
	// 用正则定位全部候选数字片段；无候选则原样返回。
	idx := auditedNumberRe.FindAllStringIndex(reply, -1)
	if len(idx) == 0 {
		return reply
	}
	// 逐段扫描：命中可信集合保留原文，否则替换为数据缺失标注。
	var sb strings.Builder
	sb.Grow(len(reply))
	last := 0
	for _, m := range idx {
		tok := reply[m[0]:m[1]]
		cat, val, ok := parseAuditedNumberToken(tok)
		if ok && trusted.allows(cat, val, reply[:m[0]], auditedTokenGranularity(tok)) {
			sb.WriteString(reply[last:m[1]])
		} else {
			sb.WriteString(reply[last:m[0]])
			sb.WriteString("[数据缺失]")
		}
		last = m[1]
	}
	sb.WriteString(reply[last:])
	return sb.String()
}

// consultCodeRe 从文本中提取 6 位股票代码。
var consultCodeRe = regexp.MustCompile(`\b\d{6}\b`)

// consultNoStockPrompt 专业模式下未能从消息中解析出股票时注入的提示词。
// 引导模型：无个股数据时做定性判断＋明确数据缺口，绝不编造个股/板块的任何具体数字。
const consultNoStockPrompt = `当前消息中未识别到明确的股票名称或 6 位代码，因此我这边没有任何该股的实时行情数据（现价/涨跌幅/主力净流入/大单明细/均线/MACD/策略信号都没有）。请你：
1. 先向用户说明：如果要我结合实时数据做分析，需要你指明具体股票（如：卧龙电驱 600580）。
2. 若用户是在问板块/情绪这类开放问题，你可以基于A股的一般规律做定性框架分析（例如"尾盘急拉回落常见于情绪资金抢跑""板块集体冲高回落要防情绪退潮"），也可以引用用户在问题里描述的现象来分析，但要明确标注这是"一般规律/经验判断"。
3. 严禁编造任何个股或板块的具体数字（成交额、净流入、撤单、振幅、持仓、涨幅、收益率、期货合约价、板块内具体个股名等），数据里没有就如实说"没有数据，无法确认"。
4. 措辞审慎，不承诺收益、不给绝对化的买卖指令。`

// consultMaxStocks §FIX-6(20260919 批四)：单条咨询最多处理的股票数。
// 每只股票至多 4 类行情外呼，且东财报价/资金流走**进程级全局 3/s 令牌桶**
// （与实盘主行情循环共用）——不设上限时一条含数百代码的消息=阻塞行情配额数百秒
// + 数百 KB 文本进付费 prompt。超出部分只处理前 5 只并在数据块尾部如实注明。
const consultMaxStocks = 5

// consultCodesInMessage 从用户消息解析本轮要处理的股票（出现顺序、去重、截断至 consultMaxStocks）。
// §CONSULT-DATARICH(2026-11-10)：从 buildConsultContext 里原样抽出（行为零改动），
// 因为 A 补数轮也要按同一份名单补数——两处各解析一遍就会出现"主块答了5只、补数轮补了另一批"。
// dropped 是因超限被丢弃的只数，调用方必须如实告知用户（§FIX-6：截断要可见）。
// English: verbatim extraction of the per-message code resolution so the supplement round
// addresses exactly the same universe as the main data block.
func (e *Engine) consultCodesInMessage(userMsg string) (order []string, names map[string]string, dropped int) {
	// §FIX-6：按**出现顺序**收集代码并截断至前 consultMaxStocks 只（map 无序，旧实现
	// 逐 code 全量外呼是行情配额与 prompt 体积的放大器）。
	names = map[string]string{} // code → name（仅用于去重与名称回查）
	addCode := func(code, name string) {
		if code == "" {
			return
		}
		if _, dup := names[code]; dup {
			return
		}
		if len(order) >= consultMaxStocks {
			dropped++
			return
		}
		names[code] = name
		order = append(order, code)
	}

	// 1. 名称 → 代码：解析文本中出现的股票名称，再清洗为代码
	if e.newsAgent != nil {
		found := e.newsAgent.FindStocksInText(userMsg)
		for _, c := range e.newsAgent.CleanStocks(found) {
			parts := strings.SplitN(c, "|", 2)
			if len(parts) != 2 || parts[0] == "" {
				continue
			}
			addCode(parts[1], parts[0])
		}
	}
	// 2. 文本中的纯 6 位代码
	for _, m := range consultCodeRe.FindAllString(userMsg, -1) {
		addCode(m, "")
	}
	return order, names, dropped
}

// buildConsultContext 从用户消息解析提到的股票，拉取真实实时行情组装为上下文文本。
// 返回空串表示未解析出任何股票（调用方应提示用户指明股票）。
// 数据来源：东财 push2 实时价（含主力净流入 F162）+ 东财资金流明细 + 新浪日K/分钟K + 引擎战法信号。
func (e *Engine) buildConsultContext(userMsg string) string {
	order, codes, dropped := e.consultCodesInMessage(userMsg)
	if len(order) == 0 {
		return ""
	}

	var sb strings.Builder
	// §生产 20260916 头部语义强化：明确"本块=本次提问时刻实时抓取的最新数据"，杜绝模型把
	// 对话历史里的旧数据误当成"手头只有的批次"（18:00 咨询实录）。
	// §FIX-9f(20260919)：新鲜度口径必须诚实——"实时抓取"只说明取数动作发生在此刻；
	// 非交易时段（夜间/周末/节假日）交易所返回的是最近收盘价，让模型说"最新盘中数据"
	// 就是误导。按交易日历判定，非盘中改口"最近收盘口径"，模型据此措辞。
	now := e.nowTime() // §FIX-9f：走引擎时钟（测试/演练可注入固定时刻），盘中判定同样受控
	freshness := "即最新盘中实测数据，直接引用作答即可"
	if !data.IsTradeTime(now) {
		freshness = "当前非交易时段，以下为最近收盘/最后更新口径——请向用户说明数据截止口径，勿表述为实时盘口"
	}
	sb.WriteString("以下是本次提问时刻【实时抓取】的股票行情实测数据（抓取时间 " +
		now.Format("2006-01-2 15:04:05") + "，" + freshness + "）：\n")
	sb.WriteString("【要求】仅可引用下列提供的数据与对话历史；未提供的信息（如同板块个股、期指贴水、撤单、盘口等）如实说明" +
		"无法获取，严禁编造净流入/成交量/涨跌/触发等任何具体数字；净流入口径=主力(超大单+大单)，东方财富；" +
		"资金明细/净流入均为抓取当日累计口径（非盘中即时增量），引用时须带【当日累计】限定；" +
		"如有『历史同类事件』段，其中的数字与走向均属往日发生，引用必须带发生日期，且不得当作今日行情。\n")

	for _, code := range order {
		sb.WriteString(e.buildStockBlock(code, codes[code]))
	}
	// §FIX-6：截断必须对用户可见——否则模型以为全量、用户以为全答。
	if dropped > 0 {
		sb.WriteString(fmt.Sprintf("\n（注意：本条消息共提到 %d 只股票，本轮仅处理前 %d 只，其余 %d 只未取数据，请提示用户分次提问。）\n",
			len(order)+dropped, consultMaxStocks, dropped))
	}

	// 大盘实测块（2026-09-16 补）：上证点位+全市场涨跌家数——用户口语里的"普涨/普跌"
	// 从此有实测依据，模型不必再说"我没有今天大盘的数据"。
	// §修复 BREADTH-FAKE(20260920)：涨跌家数改走 GetBreadth（真实弃权语义）。GetIndexData 在
	// 当时接口失败时会**伪造 1500/1500 中性值**（该函数注释已声明此设计，风险因子侧正是为此另立
	// GetBreadth），而本块开头已声明"仅可引用下列提供的数据"——把伪造值当"大盘实测"喂给模型，
	// 等于诱导它据假数据下"涨跌基本持平"的结论（实测 2026-09-20 东财不可达时即输出 1500/1500）。
	// 取不到就整段不写（宁缺勿假），与全仓 GetBreadth 的口径一致。
	// §0926E2E-14 注：GetIndexData 的伪造默认已按本口径彻底删除（失败回 0/0 弃权），
	// 本块维持"指数点位走 GetIndexData、涨跌家数单独走 GetBreadth"的分层不变。
	// English: breadth now goes through GetBreadth (true abstention). GetIndexData used to
	// deliberately fabricate a neutral 1500/1500 when its endpoint failed (removed by
	// §0926E2E-14), and feeding that to the model as
	// "measured market breadth" would make it reason from a fake number; when unavailable we emit
	// no breadth clause at all instead.
	if e.marketAPI != nil {
		if idx, _, _, _, err := e.marketAPI.GetIndexData(); err == nil && idx > 0 {
			sb.WriteString(fmt.Sprintf("\n—— 大盘实测 ——\n上证指数 %.2f 点", idx))
			if up, down, berr := e.marketAPI.GetBreadth(); berr == nil {
				sb.WriteString(fmt.Sprintf("；全市场 上涨 %d 家 / 下跌 %d 家（上涨家数显著占优=普涨）\n", up, down))
			} else {
				// 措辞刻意避开"数据源未返回"——该短语是 §FIX-9e 净流入守卫（e2e
				// TestConsultNetInflowTrueZero）在全段上下文中独占的断言词，复用会让
				// "真 0 不得误报缺数"用例假红。这里只需表达"没有就别编"。
				sb.WriteString("；全市场涨跌家数本次未取得（请勿编造具体家数）\n")
			}
		}
	}
	// §ENH-3(20260919 批B) RAG：历史同类事件注入——此前 news_events 跨日清空，顾问没有任何
	// "上次类似情况后来怎样"的记忆。检索严格早于本交易日、命中本轮个股代码的归档事件 top4，
	// 总长限 600 rune 防 prompt 膨胀；仍在 ⟦DATA⟧ 边界内，反幻觉白名单自动覆盖（§FIX-2 语义不变）。
	if e.newsAgent != nil {
		if hits := e.newsAgent.SearchEventHistory(order, data.TradingDayDate(now), 4); len(hits) > 0 {
			sb.WriteString("\n—— 历史同类事件（往日发生，引用必须带日期，非今日数据）——\n")
			histBudget := 600 // rune 预算：超限即截断剩余条目（宁少注入不可撑爆上下文）
			for _, h := range hits {
				stocks := strings.Join(h.Event.CleanedStocks, "、")
				if stocks == "" {
					stocks = strings.Join(h.Event.RelatedStocks, "、")
				}
				line := fmt.Sprintf("- %s-%s-%s %s(%.2f) [%s]「%s」关联:%s 来源:%s\n",
					h.Day[0:4], h.Day[4:6], h.Day[6:8],
					h.Event.Direction, h.Event.Score, h.Event.Level,
					h.Event.Title, stocks, h.Event.Source)
				if n := utf8.RuneCountInString(line); n > histBudget {
					break
				}
				histBudget -= utf8.RuneCountInString(line)
				sb.WriteString(line)
			}
		}
	}
	return sb.String()
}

// consultBlockCacheTTL 咨询数据块缓存有效期（60s）：收敛外部行情调用频次，
// 替代此前"盘中 15 分钟 429 拒绝"的粗暴限流（§生产 20260916）。
const consultBlockCacheTTL = 60 * time.Second

// consultBlockEntry 单股数据块缓存项。
type consultBlockEntry struct {
	text string    // 缓存的单股行情数据块文本（注入咨询 system 用）（cached quote context text）
	at   time.Time // 写入时间（距 now < consultBlockCacheTTL 时命中缓存）（write time for TTL check）
}

// consultBlockFlight §FIX-6(20260919 批四)：同代码 single-flight 通道——并发首查时
// 只有第一个请求穿透行情源，其余等同一个结果（旧实现 10 路并发=10 组外呼，
// 每组至多 4 次东财请求，全挤进程级 3/s 令牌桶，把实盘行情链路一起拖慢）。
type consultBlockFlight struct {
	done chan struct{} // 关闭=结果就绪
	text string        // 就绪后的数据块文本（close(done) 前写入，读方经通道同步可见）
}

// consultBlockCacheMax §FIX-6：缓存条目上限。超限写入时先清过期项，仍超限则淘汰最旧一条
// （自用场景 50 只覆盖日常轮询面，map 无界增长问题就此封死）。
const consultBlockCacheMax = 50

// buildStockBlock 组装单只股票的实时行情数据块（含 60s 缓存 + §FIX-6 single-flight/条数上限）。
func (e *Engine) buildStockBlock(code, name string) string {
	e.consultBlockMu.Lock()
	if e.consultBlockCache == nil {
		e.consultBlockCache = map[string]consultBlockEntry{}
	}
	if ent, ok := e.consultBlockCache[code]; ok && time.Since(ent.at) < consultBlockCacheTTL {
		e.consultBlockMu.Unlock()
		return ent.text
	}
	// 已有同代码在途构建：挂等其结果，绝不重复穿透（single-flight）。
	if fl, ok := e.consultFlight[code]; ok {
		e.consultBlockMu.Unlock()
		<-fl.done
		return fl.text
	}
	fl := &consultBlockFlight{done: make(chan struct{})}
	if e.consultFlight == nil {
		e.consultFlight = map[string]*consultBlockFlight{}
	}
	e.consultFlight[code] = fl
	e.consultBlockMu.Unlock()

	load := e.buildStockBlockUncached // 生产默认：直调行情源组装
	if e.consultBlockLoad != nil {
		load = e.consultBlockLoad // §FIX-6 测试缝
	}
	text := load(code, name) // 慢调用在锁外，行情挂死不影响其他代码

	e.consultBlockMu.Lock()
	delete(e.consultFlight, code)
	e.evictConsultBlocksLocked()
	e.consultBlockCache[code] = consultBlockEntry{text: text, at: time.Now()}
	e.consultBlockMu.Unlock()
	fl.text = text
	close(fl.done)
	return text
}

// evictConsultBlocksLocked 缓存条数治理（调用方持 consultBlockMu）：先清 TTL 过期项，
// 仍在上限外则按写入时间淘汰最旧。
func (e *Engine) evictConsultBlocksLocked() {
	for k, ent := range e.consultBlockCache {
		if time.Since(ent.at) >= consultBlockCacheTTL {
			delete(e.consultBlockCache, k)
		}
	}
	for len(e.consultBlockCache) >= consultBlockCacheMax {
		oldestKey, oldestAt := "", time.Now()
		for k, ent := range e.consultBlockCache {
			if ent.at.Before(oldestAt) {
				oldestKey, oldestAt = k, ent.at
			}
		}
		if oldestKey == "" {
			return
		}
		delete(e.consultBlockCache, oldestKey)
	}
}

// buildStockBlockUncached 组装单只股票的实时行情数据块（无缓存，直调行情源）。
func (e *Engine) buildStockBlockUncached(code, name string) string {
	var b strings.Builder
	hubMerged := false // 只用于下面那一行"现价来自哪个源、几秒前"的诚实标注
	// §CONSULT-DATARICH(2026-11-10) B①：本块的统一时钟与"实时参考价/累计量"。
	// 时钟走引擎可注入钟（与 buildConsultContext 同源），量比的时段折算因此可被测试钉住；
	// 参考价与累计量只在实时报价**成功**那一支里赋值——err!=nil 时 si 可能仍非 nil，
	// 直接读它等于把失败源的残值当实测喂给模型。
	now := e.nowTime()
	refPrice, liveVol := 0.0, 0.0
	b.WriteString(fmt.Sprintf("\n—— 股票 %s", code))
	if name != "" {
		b.WriteString(" " + name)
	}
	b.WriteString(" ——\n")

	// 实时报价（东财优先，含主力净流入 f62；主循环高频路径仍走 GetRealtimeQuote 的 S4 新浪→腾讯→东财链）
	// English: consult prefers EastMoney so the main-force net-inflow (f62) is injected; the hot main loop
	// keeps the §S4 Sina→Tencent→EastMoney chain via GetRealtimeQuote.
	if e.marketAPI == nil {
		b.WriteString("实时行情数据源未初始化。\n")
		return b.String()
	}
	si, err := e.marketAPI.GetRealtimeQuoteWithFlow(code)
	// §SUBSCRIPTION-A（owner 裁决 13 + 底线「tick 不携带的字段永不被覆盖」）：
	// 咨询要主力净流入（东财 f62），而券商根本没有这一项 ⇒ 两边**允许分家**：
	// 现价用快照/券商的更新值，净流入、名称、板块、流通股本、换手一律留原链给的值。
	// ApplyHubPrice 只写价格类字段正是为此；合并后把来源与时刻一并写给模型看，
	// 免得"照此刻价格回答"这句假话进 prompt（上次模型规范复述被审计当编造，就是这类口径事故）。
	if si != nil && e.fetcher != nil {
		if hub, ok := e.fetcher.HubQuote(code, 0); ok {
			if data.ApplyHubPrice(si, hub) {
				hubMerged = true
			}
		}
	}
	if err == nil && si != nil && si.Price > 0 {
		refPrice, liveVol = si.Price, si.Volume
		if si.Name != "" {
			name = si.Name
		}
		if hubMerged && si.AsOfMs > 0 {
			// 把价的来源与时刻交给模型，而不是让它以为"这是此刻的价"——prompt 里少一句时点，
			// 事后审计就会出现"模型复述的价格与页面差一档"这类无法自证的争议（§FIX-5 同族）。
			b.WriteString(fmt.Sprintf("现价来源：%s，%d 秒前快照（资金流等字段仍来自原行情链，两者时点可能不同）\n",
				si.Src, int(time.Since(time.UnixMilli(si.AsOfMs)).Seconds())))
		}
		// §FIX-5(1)：今开/最高/最低/昨收统一带"元"单位（旧格式无单位，模型规范复述
		// "今开 36.10 元"反被审计当编造替换）；涨跌幅同时输出带符号叙述锚"（即下跌5.67%）"，
		// 让"涨跌幅-5.67%"与"跌幅5.67%"两种常见复述形态天然进白名单。
		dirWord := "上涨"
		if si.ChangePct < 0 {
			dirWord = "下跌"
		}
		b.WriteString(fmt.Sprintf("现价 %.2f元 涨跌幅%.2f%%（即%s%.2f%%） 今开%.2f元 最高%.2f元 最低%.2f元 昨收%.2f元\n",
			si.Price, si.ChangePct, dirWord, math.Abs(si.ChangePct), si.Open, si.High, si.Low, si.Close))
		// §CONSULT-UX(20260921)：成交量同时给"股"与"手"双口径。auditNumbers 只认白名单里的
		// 原样数字——模型把"股"改写成"手"会被判为编造、整段隐成"[数据缺失]"。给双口径后模型
		// 任选其一原样照抄即可过关（与 llm.go 的"数字引用铁律"提示词配套）。
		b.WriteString(fmt.Sprintf("成交量 %.0f股（约%.0f手） 成交额%.0f元 换手率 %.2f%%\n",
			si.Volume, si.Volume/100, si.Amount, si.Turnover))
		// §CONSULT-DATARICH(2026-11-10) B①：位置类推算值本地算好进块——模型自己算的数不在
		// 白名单，算了也会被 auditNumbers 抹成 [数据缺失]（11-09 实录"现价距最高点7.7%"即此类）。
		// 分母显式写进文案：同一句"距最高7.7%"以现价还是最高价作分母差一档，不写明就是两把尺。
		// 双形态（叙述锚 + 带符号）与上面涨跌幅那行同一取向，防模型改写措辞后撞不上白名单。
		// High<Price / Low>Price 只在混源时点错位时出现，此时不写推算值（原始最高/最低已在块内）。
		if si.High > 0 && si.High >= si.Price {
			drop := (si.High - si.Price) / si.Price * 100
			b.WriteString(fmt.Sprintf("从当日最高回落 %.2f%%（即%+.2f%%，分母=现价）\n", drop, -drop))
		}
		if si.Low > 0 && si.Price >= si.Low {
			above := (si.Price - si.Low) / si.Price * 100
			b.WriteString(fmt.Sprintf("高于当日最低 %.2f%%（即%+.2f%%，分母=现价）\n", above, above))
		}
		// §FIX-9e(20260919)：判"缺数"必须看 HasFlow——东财真返回 0（买卖完全对冲）是合法
		// 实测值，旧口径 NetInflow==0 一律说"数据源未返回"，诱导模型答"没有数据"。
		if si.HasFlow {
			b.WriteString(fmt.Sprintf("主力净流入 %.2f万元\n", si.NetInflow/1e4))
		} else {
			b.WriteString("主力净流入: 数据源未返回（无法获取该字段，请勿编造）\n")
		}
	} else {
		b.WriteString("实时行情获取失败。\n")
	}

	// 所属行业（东财 f127）§修复 SECTOR-CTX(20260920)：此前数据块完全不含行业/板块字段，
	// 模型被问"这股票什么板块"时只能凭自身记忆作答——实测 2026-09-20 光智科技(300489)被答成
	// "智能驾驶、光伏"（实际东财行业=光学光电子），且这类错误是本块内唯一不被 auditNumbers
	// 拦下的编造形态（审计只查数字，不查板块名）。补实测依据后模型有据可依、无需猜测。
	if ind := e.marketAPI.GetStockIndustry(code); ind != "" {
		b.WriteString(fmt.Sprintf("所属行业: %s（东方财富行业分类；如需概念板块请说明该口径未提供）\n", ind))
	}

	// 资金流明细（超大/大/中/小单，均以万元计）
	// §修复 EM-FFLOW(20260920)：必须读 **净额字段**（*Net）而非 In−Out——东财 fflow 只返回
	// 净额、In/Out 恒为 0，按 In−Out 算会恒得 0.00 万（这一行此前就是恒 0 的）。
	if cf, err := e.marketAPI.GetStockMoneyFlow(code); err == nil && cf != nil {
		b.WriteString(fmt.Sprintf("资金明细: 超大单净流入%.0f万 大单净流入%.0f万 中单净流入%.0f万 小单净流入%.0f万\n",
			cf.SuperLargeNet/1e4, cf.LargeNet/1e4, cf.MediumNet/1e4, cf.SmallNet/1e4))
	}

	// 日K：当日振幅、MA5/MA10、前5个交易日量能、量比
	if kl, err := e.marketAPI.GetSinaKLine(code, 30); err == nil && len(kl) > 0 {
		last := kl[len(kl)-1]
		amp := 0.0
		if last.Close > 0 {
			amp = (last.High - last.Low) / last.Close * 100
		}
		// §CONSULT-DATARICH B①：收/高/低补"元"。无单位的数不进白名单也不被审，
		// 模型规范复述成"收盘22.09元"时那个"元"会让它撞不上任何锚点而被抹（与 MA 同族误杀）。
		b.WriteString(fmt.Sprintf("日K(最新一根 %s): 振幅%.2f%% 收%.2f元 高%.2f元 低%.2f元\n",
			last.Date.Format("2006-01-02"), amp, last.Close, last.High, last.Low))
		if len(kl) >= 10 {
			ma5, ma10 := consultMA(kl[len(kl)-5:]), consultMA(kl[len(kl)-10:])
			b.WriteString(fmt.Sprintf("MA5=%.2f元 MA10=%.2f元 %s\n", ma5, ma10, consultMATrend(kl)))
			// 偏离度：分母＝均线值本身（不是现价），参考价优先用实时价、取不到才退日K收盘。
			// 这两个数是模型最爱自己算的（"离均线还有几个点"），算出来必被抹，故本地算好进块。
			ref := refPrice
			if ref <= 0 {
				ref = last.Close
			}
			if ref > 0 && ma5 > 0 && ma10 > 0 {
				d5, d10 := (ref-ma5)/ma5*100, (ref-ma10)/ma10*100
				w5, w10 := "高于", "高于"
				if d5 < 0 {
					w5 = "低于"
				}
				if d10 < 0 {
					w10 = "低于"
				}
				b.WriteString(fmt.Sprintf("均线偏离: MA5 %+.2f%%（即现价%sMA5 %.2f%%） MA10 %+.2f%%（即现价%sMA10 %.2f%%），分母=均线值、参考价%.2f元\n",
					d5, w5, math.Abs(d5), d10, w10, math.Abs(d10), ref))
			}
		}
		// §CONSULT-DATARICH B①：均量改为**剔除基准日那根**的前5个交易日均量，与量比分母同源。
		// 旧口径把今日半根算进分母：今天放量会把分母抬高，反而测不出放量（与 t_vol_ratio.go
		// 剔今日K 同一条纪律）；且块内若同时存在"含今日的5日均量"与"不含今日的量比分母"，
		// 就是两个都叫"5日均量"的数互相打架。取不到就写未提供，绝不输出 0股 冒充实测。
		// 基准日：交易日＝今日；非交易日（周末/节假日）报价是最近收盘口径，基准日改取
		// 最后一根日K 的日期——否则周六上午 TradingMinutesElapsed 只看时钟会凭空折出
		// "已流逝60分钟"，把最近交易日量比虚抬 4 倍（方案 B①"非交易时段标注收盘口径"）。
		refDay := now
		exNote := "不含今日"
		if !data.IsTradingDay(now) {
			refDay = last.Date
			exNote = "不含最近交易日"
		}
		avg5 := consultAvgVolumeExToday(kl, refDay)
		if avg5 > 0 {
			b.WriteString(fmt.Sprintf("前5个交易日平均成交量 %.0f股（约%.0f手，%s），最新一根量 %.0f股（约%.0f手）\n",
				avg5, avg5/100, exNote, last.Volume, last.Volume/100))
		} else {
			b.WriteString("前5个交易日平均成交量: 未提供（剔除基准日后日K不足，请勿编造），最新一根量 " +
				fmt.Sprintf("%.0f股（约%.0f手）\n", last.Volume, last.Volume/100))
		}
		// 量比：复用 combat_agent 那把唯一尺子（按已流逝交易分钟折算全天），本处不新写公式。
		// 盘前 elapsed=0 ⇒ 未提供：IntradayVolumeRatio 内部把 elapsed<=0 兜成 1 分钟以免除零，
		// 直接调它会把集合竞价量放大 240 倍报成"巨量比"。收盘后 elapsed=240 ⇒ 全日口径。
		// 非交易日：把尺子的时刻钉在基准日 15:00（elapsed=240）⇒ 同一份实现算出收盘全日口径，
		// 不另写 liveVol/avg5 第二把尺。
		if !data.IsTradingDay(now) {
			if liveVol > 0 && avg5 > 0 {
				rd := cntime.In(last.Date)
				closedRef := time.Date(rd.Year(), rd.Month(), rd.Day(), 15, 0, 0, 0, rd.Location())
				b.WriteString(fmt.Sprintf("量比 %.2f倍（非交易日，按最近交易日 %s 收盘全日口径；该日量÷其前5个交易日均量）\n",
					combat_agent.IntradayVolumeRatio(closedRef, liveVol, avg5), last.Date.Format("2006-01-02")))
			} else {
				b.WriteString("量比: 未提供（日K/量能取数不足；请勿编造）\n")
			}
		} else if elapsed := combat_agent.TradingMinutesElapsed(now); liveVol > 0 && avg5 > 0 && elapsed > 0 {
			scope := "盘中按已流逝交易分钟折算全天"
			if elapsed >= 240 {
				scope = "收盘全日口径"
			}
			b.WriteString(fmt.Sprintf("量比 %.2f倍（%s；今日累计量÷前5个交易日均量，已流逝%.0f/240交易分钟）\n",
				combat_agent.IntradayVolumeRatio(now, liveVol, avg5), scope, elapsed))
		} else {
			b.WriteString("量比: 未提供（盘前无盘中累计量口径，或日K/量能取数不足；请勿编造）\n")
		}
	}

	// 分钟K（5分钟）MACD 状态
	if minKL, err := e.marketAPI.GetSinaMinuteKLine(code, 5, 48); err == nil && len(minKL) >= 2 {
		macd := data.CalcMACD(minKL)
		status := "空头"
		if macd.Bar > 0 {
			status = "多头(红柱)"
		} else if macd.Bar == 0 {
			status = "零轴"
		}
		b.WriteString(fmt.Sprintf("5分钟MACD: DIF=%.4f DEA=%.4f BAR=%.4f(%s)\n",
			macd.DIF, macd.DEA, macd.Bar, status))
	}

	// 引擎战法信号（该股是否已触发某战法）
	sigFound := false
	if e.agg != nil {
		if dash := e.agg.Current(); dash != nil {
			for _, sig := range dash.FinalSignals {
				if sig.Code == code {
					b.WriteString(fmt.Sprintf("策略信号: [%s] %s %s %s 触发价%.2f 理由:%s\n",
						sig.Strategy, sig.Direction, sig.Action, sig.Name, sig.Price, sig.Reason))
					sigFound = true
				}
			}
		}
	}
	// 该股当日信号批次（含触发时间），供模型据实判断"今天是否触发过量化信号、几点、什么性质"。
	if !sigFound {
		e.mu.RLock()
		records := e.signalRecords
		e.mu.RUnlock()
		for _, rec := range records {
			for _, sg := range rec.Signals {
				if sg.Code == code {
					b.WriteString(fmt.Sprintf("今日信号批次: %s触发 [%s] %s %s %s 触发价%.2f\n",
						rec.ProcessTime.Format("15:04"), sg.Strategy, sg.Direction, sg.Action, sg.Name, sg.Price))
				}
			}
		}
	}

	// §CONSULT-DATARICH(2026-11-10) B①：块尾固定声明"哪些概念根本没有数"。
	// 审计只查数字不查名词，模型给"北向资金/龙虎榜"配一个看似合理的数时，若那个数恰好
	// 撞上块内别的锚点就会被放行；把"这些字段一律无数"写在数据块里（因而在白名单采集范围内、
	// 也是模型唯一被允许引用的来源），让"严禁配数字"有明确指向而不是泛泛一句。
	b.WriteString("本块未提供：北向资金、盘口五档、同板块个股、集合竞价明细、龙虎榜、融资余额——引用这些概念时严禁配任何具体数字。\n")

	return b.String()
}

// consultMA 计算一段收盘价的简单平均。
func consultMA(kl []data.KLine) float64 {
	if len(kl) == 0 {
		return 0
	}
	var sum float64
	for _, k := range kl {
		sum += k.Close
	}
	return sum / float64(len(kl))
}

// consultAvgVolumeExToday 基准日前5个交易日的平均成交量（**剔除与基准日同日的那根**，不足5根则取全部）。
// §CONSULT-DATARICH(2026-11-10)：取代旧 consultMAVolume（含今日半根）。第二参是"基准日"：
// 交易日传 now（剔今日未收盘那根）；非交易日传最后一根日K的日期（报价即该日收盘口径，
// 分子是该日量，分母就必须剔掉它，否则放量被自己的分母稀释）。判据与 t_vol_ratio.go /
// nshape_input.go 同一口径——按北京交易日比对日期，不靠"最后一根一定是今天"的假设。
// 返回 0 ＝ 无可信均量（剔完为空/量全为零），调用方必须写"未提供"，绝不把 0 当实测注入。
// English: mean volume of the five trading days before the reference day, excluding the bar whose
// Beijing day-key equals it (callers pass now on trading days, the last bar's date otherwise);
// 0 means unavailable, never a measured zero.
func consultAvgVolumeExToday(kl []data.KLine, now time.Time) float64 {
	if len(kl) > 0 && cntime.DayOf(kl[len(kl)-1].Date) == cntime.DayOf(now) {
		kl = kl[:len(kl)-1]
	}
	if len(kl) == 0 {
		return 0
	}
	n := 5
	if len(kl) < n {
		n = len(kl)
	}
	var sum float64
	for _, k := range kl[len(kl)-n:] {
		sum += k.Volume
	}
	return sum / float64(n)
}

// consultMATrend 判断 MA5 相对 MA10 的多头/空头排列。
func consultMATrend(kl []data.KLine) string {
	if len(kl) < 10 {
		return ""
	}
	ma5 := consultMA(kl[len(kl)-5:])
	ma10 := consultMA(kl[len(kl)-10:])
	if ma5 > ma10 {
		return "均线多头排列"
	}
	return "均线空头排列"
}

// consultStoreFor 返回指定账号的咨询历史存储（§GAP2-W2 I-1 私有状态按账号寻址）：
// accountsRoot 注入时落 accounts/<uid>/consult_history.json（各自目录互不可见）。
// §FIX-10(20260919 批四)：登录用户遇 accountsRoot 未注入时**返回 nil 拒绝服务**，
// 不再回退引擎级共享 store——共享回退=A 的提问会出现在 B 的历史里（串号），
// 宁可 503 不可串号。匿名（userID==""，旧单机形态）仍走共享 store 保持兼容。
// English: returns nil (caller must refuse) instead of silently sharing one store
// across logged-in users when accountsRoot is missing; anonymous legacy mode keeps the shared store.
func (e *Engine) consultStoreFor(userID string) *data.ConsultStore {
	if userID == "" {
		return e.consultStore
	}
	e.mu.RLock()
	root := e.accountsRoot
	e.mu.RUnlock()
	if root == "" {
		return nil // §FIX-10：隔离不可用 → 拒绝，不共享
	}
	// 懒加载：每个账号首次访问时创建独立咨询存储并缓存（锁内完成建目录+初始化）。
	e.consultMu.Lock()
	defer e.consultMu.Unlock()
	if st, ok := e.consultByUser[userID]; ok {
		return st
	}
	if e.consultByUser == nil {
		e.consultByUser = map[string]*data.ConsultStore{}
	}
	dir := filepath.Join(root, userID)
	_ = os.MkdirAll(dir, 0755)
	st := data.NewConsultStore(filepath.Join(dir, "consult_history.json"))
	e.consultByUser[userID] = st
	return st
}

// GetConsultHistory 返回指定账号的当日咨询对话历史（§GAP2-W2 账户隔离）。
// §FIX-10：隔离不可用（nil store）时返回空列表——读不到别人的，也读不到自己的，宁缺不漏。
func (e *Engine) GetConsultHistoryFor(userID string) []data.ConsultMessage {
	st := e.consultStoreFor(userID)
	if st == nil {
		return nil
	}
	return st.List()
}

// ClearConsultHistory 清空指定账号的当日咨询对话历史（§GAP2-W2 只清本人的）。
// §FIX-10：nil store（隔离不可用）时为空操作（无历史可清，也绝不触碰共享存储）。
func (e *Engine) ClearConsultHistoryFor(userID string) {
	if st := e.consultStoreFor(userID); st != nil {
		st.Clear()
	}
}
