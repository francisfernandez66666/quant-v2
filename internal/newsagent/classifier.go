// classifier.go — Stage0/1 合并归因分类、关键词兜底初筛与 LLM 批量响应的容错解析（抗推理模型 JSON 抖动）。
package newsagent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/data"
)

// stage1Keywords 投资价值关键词表：标题命中任一关键词即视为有板块/宏观投资价值的候选，
// 用于无 LLM 时的 Stage0 合并判定兜底初筛。
// （Stage1 investment-value keyword table: any match marks a headline as a sector/macro candidate,
// used as the keyword fallback when no LLM is available.）
// English: Stage1 investment-value keyword table: any match marks a headline as a sector/macro candidate; used as the keyword fallback for the merged Stage0 screening when no LLM is available.
var stage1Keywords = []string{
	"业绩", "财报", "预增", "预亏", "扭亏", "翻倍", "涨停", "跌停",
	"重大合同", "中标", "订单", "重组", "定增", "增发", "回购", "减持", "增持",
	"获批", "临床", "突破", "新品", "专利",
	"政策", "利好", "利空", "救市", "降息", "降准", "加息",
	"龙头", "板块", "产业链", "景气", "拐点",
	"退市", "ST", "风险警示", "立案", "调查", "处罚",
	"北向", "主力", "资金", "净流入", "净流出",
	"分红", "送转", "除权", "填权",
	"借壳", "收购", "合并", "分拆", "引入战投",
	"出口", "进口", "关税", "制裁",
	"AI", "人工智能", "芯片", "新能源", "光伏", "锂电", "储能", "氢能",
	"消费", "复苏", "通胀", "通缩",
}

// matchKeywords 关键词匹配：检查标题是否包含预定义的投资相关关键词。
// （matchKeywords reports whether the title contains any predefined investment-related keyword.）
// English: matchKeywords reports whether the title contains any predefined investment-related keyword.
func matchKeywords(title string) bool {
	t := strings.ToLower(title)
	for _, kw := range stage1Keywords {
		if strings.Contains(t, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// ipoKeywords IPO 相关关键词：命中即判定为新股/申购/上市类新闻，直构事件不走 LLM。
// （IPO-related keywords: a hit marks the news as IPO/subscription/listing and builds events directly without LLM.）
// English: ipoKeywords are IPO-related keywords: a hit marks the news as IPO/subscription/listing, building events directly without the LLM.
var ipoKeywords = []string{
	"IPO", "新股", "申购", "中签", "首发", "过会", "招股", "发行价",
	"上市首日", "新股上市", "挂牌上市", "注册生效", "网上发行",
}

// matchIPOKeywords 判断标题是否属于 IPO 相关新闻（新股/申购/上市）。
// （matchIPOKeywords reports whether the title is IPO-related (new stock/subscription/listing).）
// English: matchIPOKeywords reports whether the title is IPO-related (new stock/subscription/listing).
func matchIPOKeywords(title string) bool {
	t := strings.ToLower(title)
	for _, kw := range ipoKeywords {
		if strings.Contains(t, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// stageCombinedSystemPrompt Stage0/1 合并调用：
// 单次 LLM 批次输出 来源类型(category) + 投资价值(material) + 校正标题(corrected_title)。
// 输入含标题与正文，供标题党复核（正文由 EnrichContents 回填，正文不足时仅标题）。
// （Combined Stage0/1 prompt: a single LLM batch outputs source category + investment material + corrected
// title. Inputs include title and body for clickbait review.）
// English: stageCombinedSystemPrompt for the merged Stage0/1 call: a single LLM batch outputs source category + investment material + corrected title. Inputs include title and body for clickbait review (body is backfilled by EnrichContents, title-only when the body is short).
const stageCombinedSystemPrompt = `你是一个A股新闻质检与价值判断专家。对每条新闻（含标题与正文）依次输出三个判断：

1. category（来源类型），取值之一：
   - official: 官方/权威信息源发布的事实新闻，包括政府、监管机构、央行、美联储、上市公司公告、财报、行业动态、宏观经济数据发布、政策发布
   - institution: 机构观点/专家评论/券商研报/分析师看市/名家观点
   - interactive: 互动问答/投资者关系/董秘回复/股吧/网友观点
   - overseas: 海外市场行情播报（美股/港股/欧股/外汇/黄金/原油等盘面，不含对A股有直接影响的政策事件）

2. material（是否有投资参考价值），true/false：
   有投资参考价值的事件包括：业绩预告/财报、重大合同/中标/订单、重组/定增/回购/减持/增持、新药获批/临床突破、重大政策、行业景气、龙头重大动向、宏观数据。
   无价值噪音：机构观点/研报、互动问答/董秘回复、海外行情播报、娱乐/社会/体育/名人八卦/灾难事故、无实质影响的日常新闻。

3. corrected_title（校正标题）：当标题存在夸大、断章取义、误导（标题党），与正文内容明显不符、无法准确反映真实信息时，给出一个忠于正文、简洁规范的校正标题；否则输出空字符串 ""。校正标题不得编造正文没有的信息。

输入格式：
序号. 标题
正文: 正文内容

返回JSON数组，每项: {"index": 序号, "category": "official|institution|interactive|overseas", "material": true/false, "corrected_title": "..."}
每条新闻都必须给出。只输出JSON数组，不要多余文字。`

// combinedBodyLimit 合并调用时正文截断长度（字符），控制单批 prompt 体积。（Body truncation length for combined calls, in runes.）
// English: body truncation length (in runes) for combined calls, keeping each batch's prompt compact.
const combinedBodyLimit = 300

// combinedJudge 合并调用的单条判定结果。（combinedJudge is one judgement produced by the combined call.）
// English: combinedJudge is one judgement produced by the combined call.
type combinedJudge struct {
	// 是否为官方/权威来源（非机构观点/互动/海外盘面）
	Official bool
	// English: whether from an official/authoritative source (not institution opinion / interactive / overseas tape)
	// 是否有投资参考价值
	Material bool
	// English: whether it has investment reference value
	// 标题党校正标题（为空表示标题忠于正文）
	CorrectedTitle string
	// English: clickbait-corrected title (empty means the title matches the body)
}

// classifyCombined 合并 Stage0 垃圾过滤 + Stage1 价值初筛 + 标题党复核 为单次 LLM 分批调用。
// 返回失败批的全局索引（failedBatches）：某批重试队列用尽被跳过时，其新闻未被判定，
// 由调用方将失败批新闻留在"未归因队列"供下一轮重试，而不是错误归为一般新闻。
// 失败走轮询重试（每批最多3次、间隔递增）。
// （classifyCombined merges Stage0 junk filtering + Stage1 value screening + clickbait review into batched LLM
// calls. It also returns the global indices of failed batches: when a batch's retry queue is exhausted and it is
// skipped, those news were never judged, so the caller keeps them in the unattributed queue for the next round
// instead of misclassifying them as general news. Failures use polling retries with backoff.）
// English: classifyCombined merges Stage0 junk filtering + Stage1 value screening + clickbait review into a single batched LLM call. It returns the global indices of failed batches (failedBatches): when a batch's retry queue is exhausted and it is skipped, those items were never judged, so the caller keeps them in the unattributed queue for the next round instead of misclassifying them as general news. Failures use polling retries (max 3 per batch, increasing intervals).
func (a *Agent) classifyCombined(titles, bodies []string) (judgements []combinedJudge, failedBatches []int, err error) {
	n := len(titles)
	out := make([]combinedJudge, n)
	if n == 0 {
		return out, nil, nil
	}
	if a.llmClient == nil {
		log.Printf("[newsagent] LLM未配置, Stage0/1合并跳过")
		return out, nil, fmt.Errorf("LLM未配置")
	}

	// 按 llmBatchSize 分块并**并发**调用，控制单批 prompt 体积避免超时的同时提高归因吞吐。
	// 并发度取自 LLM 客户端 BatchConcurrency（前端可热改）。
	// English: chunk by llmBatchSize and call concurrently - controlling per-batch prompt size to avoid timeouts while raising attribution throughput. Concurrency comes from the LLM client's BatchConcurrency (hot-adjustable in the frontend).
	concurrency := llmRetryMax // 兜底并发度
	if a.llmClient != nil {
		concurrency = a.llmClient.BatchConcurrency()
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, b := range batchBounds(n, llmBatchSize) {
		start, end := b[0], b[1]
		// 拼装批内 prompt：序号 + 标题 + 截断后的正文（供标题党复核）
		// English: build the batch prompt: index + title + truncated body (for clickbait review)
		var sb strings.Builder
		for i := start; i < end; i++ {
			body := truncateRunes(bodies[i], combinedBodyLimit)
			sb.WriteString(fmt.Sprintf("%d. %s\n正文: %s\n", i-start+1, titles[i], body))
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(start, end int, sb strings.Builder) {
			defer wg.Done()
			defer func() { <-sem }()
			// 调用+解析统一进重试队列：API 连接失败 与 JSON 解析失败 都纳入轮询重试
			//（解析失败不再直接吞掉，而是与调用失败同样退避重试）。
			// 该批重试到头仍解析不出 → 记录失败批索引（丢本批判定，但不影响主干其余批次），
			// 交由调用方把该批新闻留在未归因队列下一轮重试。
			// English: call + parse both go through the retry queue: API connection failures and JSON parse failures are both retried with backoff (parse failures are no longer swallowed). If a batch exhausts its retries without parsing, its indices are recorded as a failed batch (that batch's judgement is dropped, without affecting the other batches), and the caller keeps those items in the unattributed queue for the next round.
			raw, err := a.stage0ParseRetry(sb.String())
			if err != nil {
				os.WriteFile("/tmp/5t0_fail_"+time.Now().Format("150405")+".json", []byte(sb.String()), 0o644)
				log.Printf("[newsagent] Stage0/1合并 该批%d条重试队列用尽仍解析失败, 标记失败批(主干继续): %v", end-start, err)
				mu.Lock()
				for i := start; i < end; i++ {
					failedBatches = append(failedBatches, i)
				}
				mu.Unlock()
				return
			}
			// 将批内序号映射回全局索引并落盘到结果切片（越界序号安全忽略）
			// English: map the in-batch index back to a global index and write it into the result slice (out-of-range indices are safely ignored)
			for _, r := range raw {
				if r.Index < 1 || int(r.Index) > end-start {
					continue
				}
				idx := start + int(r.Index) - 1
				out[idx].Official = strings.EqualFold(r.Category, "official")
				out[idx].Material = bool(r.Material)
				out[idx].CorrectedTitle = strings.TrimSpace(r.Corrected)
			}
		}(start, end, sb)
	}
	wg.Wait()
	return out, failedBatches, nil
}

// stage0EmptyValueRe 匹配对象键与空值畸形：`"key":]` 或 `"key":}`（模型丢了值直接写括号）。
// 修复为 `"key": ""`，覆盖单个对象解析时的空值缺失。
// （Matches object-key/empty-value malformations like `"key":]` and fixes them to `"key": ""`.）
// English: stage0EmptyValueRe matches object-key/empty-value malformations like "key":] or "key":} (the model dropped the value and wrote a bracket), fixed to "key": "" to cover missing empty values in single-object parsing.
var stage0EmptyValueRe = regexp.MustCompile(`("(?:[^"\\]|\\.)*"\s*:)\s*[}\]]`)

// trailingJunkRe 匹配字符串收引号后紧跟的杂散 `)`/`'`（如 "上涨"") 、"死"'} ），
// 归一为单收引号，恢复为合法 JSON。
// （Matches stray )/' after a closing quote and normalizes to a single closing quote for valid JSON.）
// English: trailingJunkRe matches stray )/' right after a closing quote (e.g. extra quotes/brackets/apostrophes) and normalizes them to a single closing quote for valid JSON.
var trailingJunkRe = regexp.MustCompile(`"\s*[\)']+\s*([,}\]]|$)`)

// stage0Judge 单条 Stage0 判定对象结构体。（matches a single Stage0 judgement object shape.）
// English: stage0Judge is the shape of a single Stage0 judgement object.
type stage0Judge struct {
	// 序号
	Index flexInt `json:"index"`
	// 分类（题材/行业/个股/板块）
	Category string `json:"category"`
	// 是否实质利好材料（区别于噪音）
	Material flexBool `json:"material"`
	// 是否已做中性归零/校正
	Corrected string `json:"corrected_title"`
}

// flexInt 兼容 JSON 中整数字段为数字或字符串（如 1 或 "1"）的解析。
// 推理模型常把数值输出成带引号字符串，标准 json.Unmarshal 到 int 会失败。
// （flexInt tolerates int fields serialized as number or string (1 or "1"), as reasoning models often quote numbers.）
// English: flexInt tolerates int fields serialized as number or string (1 or "1"); reasoning models often quote numeric values, which would make a standard json.Unmarshal into int fail.
type flexInt int

// UnmarshalJSON 解析数字或字符串形式的整数，解析失败按 0 处理。（Parses number-or-string ints, defaulting to 0 on failure.）
// English: parses number-or-string ints, defaulting to 0 on failure.
func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	if s == "" {
		*f = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		*f = 0
	}
	*f = flexInt(v)
	return nil
}

// flexBool 兼容 JSON 中布尔字段为布尔或字符串（如 true 或 "true"）的解析。
// （flexBool tolerates bool fields serialized as boolean or string, e.g. true or "true".）
// English: flexBool tolerates bool fields serialized as boolean or string, e.g. true or "true".
type flexBool bool

// UnmarshalJSON 解析布尔或字符串形式的布尔值，解析失败按 false 处理。（Parses boolean-or-string bools, defaulting to false on failure.）
// English: parses boolean-or-string bools, defaulting to false on failure.
func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	v, err := strconv.ParseBool(s)
	if err != nil {
		*f = false
		return nil
	}
	*f = flexBool(v)
	return nil
}

// salvageStage0Objects 对 Stage0 批量响应做两段式解析（抗推理模型 JSON 结构抖动）：
//  1. 整体当作 JSON 数组解析（正常路径）；
//  2. 整体失败 → 逐对象抢救：用花括号深度扫描提取每个 {…} 独立解析（无视换行/逗号/包裹格式），
//     先修复 "key":] / "key":} 的空值畸形与字符串 index/material，单个坏对象只丢该条，不整批废弃。
//
// 返回 (解析结果, 是否获得至少一条)。
// （salvageStage0Objects parses a Stage0 batch in two passes against reasoning-model JSON jitter: try the whole
// array first, then salvage per object via brace-depth scanning, so one bad object never discards the batch.）
// English: salvageStage0Objects parses a Stage0 batch response in two passes against reasoning-model JSON jitter: (1) parse the whole thing as an array (normal path); (2) if that fails, salvage per object by brace-depth scanning to extract each {...} and parse it independently (ignoring newlines/commas/wrapping). Malformed "key":] / "key":} empty values and string index/material are fixed first; one bad object only drops that row, never the whole batch. Returns (parsed results, whether at least one was obtained).
func salvageStage0Objects(resp string) ([]stage0Judge, bool) {
	var raw []stage0Judge
	if err := json.Unmarshal([]byte(resp), &raw); err == nil {
		return raw, true
	}
	var out []stage0Judge
	for _, obj := range extractObjects(resp) {
		// 单引号畸形：模型把键尾引号/空值写成单引号，如 "corrected_title':''"，
		// 修复为 "corrected_title":""。
		// English: single-quote malformation: the model wrote the key's closing quote / empty value as a single quote (e.g. "corrected_title':''"); fix it to "corrected_title":"".
		obj = strings.ReplaceAll(obj, `':''`, `":"`)
		// 字符串收尾垃圾：`"上涨"")` / `"死"'} `（收引号后多引号/括号/撇号）→ 归一为收引号。
		// English: trailing-string junk: "上涨"") / "死"'} (extra quotes/brackets/apostrophes after a closing quote) is normalized to a single closing quote.
		obj = trailingJunkRe.ReplaceAllString(obj, `"$1`)
		obj = stage0EmptyValueRe.ReplaceAllString(obj, `$1""`)
		var one stage0Judge
		if err := json.Unmarshal([]byte(obj), &one); err == nil {
			out = append(out, one)
		} else {
			log.Printf("[newsagent] Stage0逐对象抢救跳过 1 条: %s", truncateRunes(obj, 80))
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	log.Printf("[newsagent] Stage0整体解析失败, 逐对象抢救成功 %d 条", len(out))
	return out, true
}

// extractObjects 用花括号配对扫描提取字符串中所有独立的 JSON 对象 `{...}`（含嵌套），
// 无视换行/逗号/数组包裹等格式差异（推理模型的输出排版不可靠）。
// （extractObjects scans for balanced braces to pull out every standalone JSON object, ignoring formatting differences.）
// English: extractObjects scans for balanced braces to pull out every standalone JSON object {...} (including nested), ignoring formatting differences such as newlines/commas/array wrapping (reasoning-model output layout is unreliable).
func extractObjects(s string) []string {
	var objs []string
	start := -1
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				objs = append(objs, s[start:i+1])
				start = -1
			}
		}
	}
	return objs
}

// truncateRunes 按字符数截断字符串（超出部分截断），避免超长正文撑爆 prompt。
// （truncateRunes truncates a string to max runes to keep prompts bounded.）
// English: truncateRunes truncates a string to max runes to keep prompts bounded.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// Stage0 Stage0 归因分类（合并版）：单次 LLM 调用输出 来源分类+价值+校正标题。
// 分类结果经路由将新闻分为 个股 / 板块 / 一般 三类：
//   - 非官方（机构/互动/海外）→ 一般，仅展示
//   - IPO 新闻：标题含新股/申购/上市关键词，直构事件不走 LLM
//   - 个股新闻：标题+正文含已知股票名（StockCleaner 映射命中），预填关联股票
//   - 板块新闻：标题含行业/宏观关键词，须通过 material 价值初筛（原 Stage1 职责）
//   - 其余：一般
//
// （Stage0 attribution (merged): a single LLM call outputs category + value + corrected title, routed into
// stock/sector/general/IPO buckets.）
// English: Stage0 attribution (merged): a single LLM call outputs source category + investment value + corrected title, routed into stock/sector/general buckets: non-official (institution/interactive/overseas) goes to general (display only); IPO news (new stock/subscription/listing keywords) builds direct events without the LLM; stock news (title+body hit a known stock via StockCleaner) has related stocks prefilled; sector news (industry/macro keywords) must pass the material value screen (the former Stage1 duty); the rest goes to general.
func (a *Agent) Stage0(items []data.NewsItem) Stage0Result {
	var res Stage0Result
	if len(items) == 0 {
		return res
	}

	titles := make([]string, len(items))
	bodies := make([]string, len(items))
	for i, it := range items {
		titles[i] = it.Title
		bodies[i] = it.Content
	}

	judgements, failedBatches, err := a.classifyCombined(titles, bodies)
	res.FailedIdx = failedBatches
	if err != nil {
		// 不兜底（全局原则：LLM 失败不靠兜底，全部走 LLM 重试队列）：
		// 整批判定失败时**不**归一般（避免把未判定新闻误判为"一般新闻"而丢失），
		// 而是全部标记 FailedIdx 留在未归因队列，下一轮轮询重新调 LLM 判定。
		// English: no fallback (global principle: LLM failures are never papered over - everything goes through the LLM retry queue): when a whole batch fails, do NOT classify it as general (which would lose undecided news); mark every item in FailedIdx to stay in the unattributed queue and re-invoke the LLM next round.
		res.Err = err
		log.Printf("[newsagent] Stage0/1合并失败, 整批 %d 条留待重试(入重试队列): %v", len(items), err)
		for i := range items {
			res.FailedIdx = append(res.FailedIdx, i)
		}
		return res
	}

	// 失败批（LLM 重试耗尽被跳过的批）：保留在未归因队列供下一轮重试，
	// 不归一般/个股/板块（避免失败批被误判为"一般新闻"而丢失昨夜有价值信息）。
	// English: failed batches (skipped after LLM retries are exhausted) stay in the unattributed queue for the next round and are NOT bucketed as general/stock/sector, so last night's valuable info is not lost to misclassification.
	failedSet := make(map[int]bool, len(failedBatches))
	for _, f := range failedBatches {
		failedSet[f] = true
	}

	res.Material = make(map[int]bool)
	res.CorrectedTitle = make(map[int]string)
	for i, item := range items {
		if failedSet[i] {
			continue
		}
		j := judgements[i]
		// 规则一：非官方来源（机构观点/互动/海外盘面）直接归一般，仅展示不进引擎
		// English: rule 1 - non-official sources (institution opinion / interactive / overseas tape) go straight to general; display only, never into the engine
		if !j.Official {
			res.GeneralIdx = append(res.GeneralIdx, i)
			continue
		}
		// 规则二：IPO 新闻（新股/申购/上市关键词）直构事件，不走 LLM 深度分析
		// English: rule 2 - IPO news (new stock/subscription/listing keywords) builds events directly, skipping LLM deep analysis
		if matchIPOKeywords(item.Title) {
			res.IpoIdx = append(res.IpoIdx, i)
			continue
		}
		// 个股归因：标题+正文共同匹配（正文含公司全称，弥补标题简称漏配）
		// English: stock attribution matches title + body together (the body carries the company's full name, covering short-name misses in the title)
		text := item.Title
		if len(item.Content) > 0 {
			text += " " + item.Content
		}
		// 规则三：命中已知股票名 → 个股新闻，预填关联股票并视为有投资价值
		// English: rule 3 - hitting a known stock name makes it stock news; prefill related stocks and treat it as having investment value
		if a.cleaner != nil {
			if hits := a.cleaner.FindStocksInText(text); len(hits) > 0 {
				res.StockIdx = append(res.StockIdx, i)
				res.Material[i] = true
				applyCorrected(&res, i, item.Title, j)
				continue
			}
		}
		// 规则四：标题含行业/宏观关键词 → 板块新闻，须通过 LLM 的 material 价值初筛
		// English: rule 4 - title contains industry/macro keywords makes it sector news; it must pass the LLM's material value screen
		if matchKeywords(item.Title) {
			res.SectorIdx = append(res.SectorIdx, i)
			res.Material[i] = j.Material
			applyCorrected(&res, i, item.Title, j)
			continue
		}
		// 兜底：其余新闻归一般
		// English: fallback - everything else goes to general
		res.GeneralIdx = append(res.GeneralIdx, i)
	}
	log.Printf("[newsagent] Stage0分类(合并): 个股%d 板块%d IPO%d 一般%d (材料通过%d)",
		len(res.StockIdx), len(res.SectorIdx), len(res.IpoIdx), len(res.GeneralIdx), len(res.Material))
	return res
}

// applyCorrected 记录标题党校正标题（仅当 LLM 给出且与原标题不同）。
// （applyCorrected records the corrected title only when the LLM provided one different from the original.）
// English: applyCorrected records the clickbait-corrected title only when the LLM provided one different from the original.
func applyCorrected(res *Stage0Result, idx int, original string, j combinedJudge) {
	if j.CorrectedTitle != "" && j.CorrectedTitle != original {
		res.CorrectedTitle[idx] = j.CorrectedTitle
	}
}

// newsTitles 提取新闻列表的标题。（newsTitles extracts the titles of a news list.）
// English: newsTitles extracts the titles of a news list.
func newsTitles(items []data.NewsItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Title
	}
	return out
}

// llmBatchSize LLM 单次批量处理的最大条数，防止超大批次导致超时。
// 推理模型（GLM-Z1-9B）对大批次首 token 极慢，30 条会 240s 超时等不到响应头，
// 调小到 10 条使单批在超时内完成，代价是多几次调用（更稳）。
// （Max items per LLM batch to avoid timeouts; 10 keeps each batch within the timeout as reasoning models are slow on first tokens.）
// English: llmBatchSize caps items per LLM batch to avoid timeouts. Reasoning models (GLM-Z1-9B) are extremely slow on the first token for large batches - 30 items timed out at 240s waiting for the response head. Cutting to 10 lets each batch finish within the timeout, at the cost of a few more calls (steadier).
const llmBatchSize = 10

// llmRetryMax LLM 单次调用的最大重试次数（含首次），指数退避，应对上游瞬时抖动/连不通。
// （Max LLM retry attempts per call with exponential backoff against upstream jitter.）
// English: llmRetryMax is the max retry attempts per LLM call (including the first), with exponential backoff against upstream jitter / connection failures.
const llmRetryMax = 5

// stage0ParseRetry 单批"调用+解析"的重试循环（重试队列）：API 失败 与 JSON 解析失败 一律退避重试，
// 任一次调用能抢救出 ≥1 条即成功（局部坏对象已由 salvageStage0Objects 内部跳过），
// 全部失败返回错误，由调用方将该批隔离跳过（不影响主干其余批次）。
// （stage0ParseRetry is a per-batch call+parse retry loop with backoff; any attempt yielding ≥1 row succeeds,
// otherwise the batch is isolated and skipped by the caller without affecting the main flow.）
// English: stage0ParseRetry is a per-batch call+parse retry loop (retry queue): API failures and JSON parse failures both back off and retry; any attempt salvaging >=1 row is a success (bad objects are skipped internally by salvageStage0Objects); total failure returns an error and the caller isolates/skips that batch without affecting the other batches.
func (a *Agent) stage0ParseRetry(userMsg string) ([]stage0Judge, error) {
	var lastErr error
	for attempt := 1; attempt <= llmRetryMax; attempt++ {
		resp, err := a.llmClient.ChatClassifier(stageCombinedSystemPrompt, userMsg)
		if err == nil {
			resp = cleanJSON(resp)
			if judges, ok := salvageStage0Objects(resp); ok {
				return judges, nil
			}
			lastErr = fmt.Errorf("批次JSON解析失败(整体+逐对象抢救均无效)")
		} else {
			lastErr = err
		}
		log.Printf("[newsagent] Stage0调用/解析失败(第%d/%d次): %v", attempt, llmRetryMax, lastErr)
		if attempt < llmRetryMax {
			time.Sleep(backoffStep(attempt))
		}
	}
	return nil, fmt.Errorf("重试队列用尽(%d次): %v", llmRetryMax, lastErr)
}

// backoffStep 第 attempt 次失败后的退避间隔（指数递增，1s/2s/4s/8s/16s 封顶）。
// （backoffStep returns the backoff interval for the given attempt, capping at 16s.）
// English: backoffStep returns the backoff interval after the given attempt (exponential, 1s/2s/4s/8s/16s cap).
func backoffStep(attempt int) time.Duration {
	base := []time.Duration{1, 2, 4, 8, 16}
	if attempt-1 < len(base) {
		return base[attempt-1] * time.Second
	}
	return 16 * time.Second
}

// retryLLM 以指数退避方式重试一次 LLM 调用：失败重试 5 次，间隔 1s/2s/4s/8s。
// 返回 (响应, 最后一次错误)。记录每次失败以便排障。
// （retryLLM retries an LLM call with exponential backoff (5 attempts, 1s/2s/4s/8s), returning the response
// and last error, logging each failure for debugging.）
// English: retryLLM retries an LLM call with exponential backoff (5 attempts, 1s/2s/4s/8s). Returns (response, last error), logging each failure for debugging.
func retryLLM(userMsg string, call func() (string, error)) (string, error) {
	var resp string
	var err error
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for attempt := 1; attempt <= llmRetryMax; attempt++ {
		resp, err = call()
		if err == nil {
			return resp, nil
		}
		log.Printf("[newsagent] LLM调用失败(第%d/%d次): %v", attempt, llmRetryMax, err)
		if attempt < llmRetryMax {
			time.Sleep(backoff[attempt-1])
		}
	}
	return "", fmt.Errorf("LLM连试%d次仍失败: %v", llmRetryMax, err)
}

// batchBounds 将 n 个元素按 size 分块，返回 [start,end) 区间列表。
// （batchBounds splits n items into size-sized chunks and returns the [start,end) ranges.）
// English: batchBounds splits n items into size-sized chunks and returns the [start,end) ranges.
func batchBounds(n, size int) [][2]int {
	var out [][2]int
	for start := 0; start < n; start += size {
		end := start + size
		if end > n {
			end = n
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// cleanJSON 清理 LLM 返回的 JSON 字符串中的 markdown 代码块标记。
// （cleanJSON strips markdown code fences from LLM JSON output.）
// English: cleanJSON strips markdown code fences from LLM JSON output.
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// 提取 JSON 数组边界（第一个 [ 到最后一个 ]）——推理模型会在 JSON 前输出思考过程。
	// English: extract the JSON array bounds (first [ to last ]) - reasoning models print their thinking before the JSON.
	if start := strings.IndexByte(s, '['); start >= 0 {
		if end := strings.LastIndexByte(s, ']'); end > start {
			s = s[start : end+1]
		}
	}
	s = strings.TrimRight(s, ".,; ")
	// 剥离数值位置的裸 '+' 前缀（"score": +0.75 是非法 JSON 数字）。
	// English: strip a bare '+' prefix at numeric positions ("score": +0.75 is invalid JSON).
	s = plusNumberRe.ReplaceAllString(s, "$1 ")
	// 清洗非法转义（\( 等）与字符串内未转义换行；并修复对象键后单引号/空值畸形。
	// English: clean invalid escapes (like \) and unescaped newlines inside strings; also fix single-quote/empty-value malformations after object keys.
	var buf strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			next := s[i+1]
			if !isValidJSONEscape(next) {
				buf.WriteByte(next)
				i++
				continue
			}
			buf.WriteByte(ch)
			buf.WriteByte(next)
			i++
			continue
		}
		if ch == '"' {
			inStr = !inStr
			buf.WriteByte(ch)
			continue
		}
		if inStr && (ch == '\n' || ch == '\r') {
			buf.WriteString("\\n")
		} else {
			buf.WriteByte(ch)
		}
	}
	return buf.String()
}

// isValidJSONEscape 判断字节是否为合法 JSON 转义字符。（isValidJSONEscape reports whether the byte is a valid JSON escape char.）
// English: isValidJSONEscape reports whether the byte is a valid JSON escape char.
func isValidJSONEscape(b byte) bool {
	switch b {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	}
	return false
}

// plusNumberRe 匹配冒号/逗号/左括号后的 '+' 前缀（数值位置），用于剥离非法 '+'。
// （Matches a stray + prefix at numeric positions after : , or [ for removal.）
// English: plusNumberRe matches a stray + prefix at numeric positions after : , or [, for removal.
var plusNumberRe = regexp.MustCompile(`([:,\[])\s*\+`)
