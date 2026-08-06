package newsagent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
)

const stage1SystemPrompt = `你是一个A股新闻价值判断专家。从以下新闻标题中，筛选出具有投资参考价值的重大事件。

重大事件包括但不限于：
- 业绩预告/财报发布
- 重大合同/中标/订单
- 重组/定增/增发/回购/减持
- 新药获批/临床试验突破
- 重大政策发布/行业利好利空
- 龙头公司重大动向
- 宏观经济数据发布

必须忽略：
- 机构观点/专家评论/券商研报/分析师看市（如"机构观点""专家看市""后市研判""某券商认为"）
- 股吧/互动问答/投资者关系/董秘回复
- 海外市场行情播报（美股/港股/欧股/外汇/黄金/原油盘面）
- 娱乐、社会、体育、影视、名人八卦、灾难事故等无关新闻

返回JSON数组，只包含有投资价值的条目索引（从1开始），如 [1,3,7]
如果没有任何有价值的条目，返回 []`

// stage0FilterSystemPrompt Stage0 垃圾过滤：仅保留官方/权威事实新闻，剔除机构观点/互动/海外行情播报。
const stage0FilterSystemPrompt = `你是一个A股新闻来源分类器。将以下每条新闻标题分类为四种类型之一：
- official: 官方/权威信息源发布的事实新闻，包括政府、监管机构、央行、美联储、上市公司公告、财报、行业动态、宏观经济数据发布、政策发布
- institution: 机构观点/专家评论/券商研报/分析师看市/名家观点
- interactive: 互动问答/投资者关系/董秘回复/股吧/网友观点
- overseas: 海外市场行情播报（美股/港股/欧股/外汇/黄金/原油等盘面，不含对A股有直接影响的政策事件）

返回JSON数组，每项格式: {"index": 序号, "type": "official|institution|interactive|overseas"}
每条新闻都必须给出分类。只输出JSON数组，不要多余文字。`

// stage1Keywords 投资价值关键词表：标题命中任一关键词即视为有板块/宏观投资价值的候选，
// 用于无 LLM 时的 Stage1 关键词兜底初筛。
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
func matchKeywords(title string) bool {
	t := strings.ToLower(title)
	for _, kw := range stage1Keywords {
		if strings.Contains(t, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// junkFallbackKeywords 垃圾过滤兜底关键词：命中即判定为非官方（机构/互动/海外盘面等）。
var junkFallbackKeywords = []string{
	"机构观点", "专家", "研报", "分析师", "看市", "名家", "后市", "策略会",
	"互动", "投资者关系", "董秘", "股吧", "网友", "问答", "回复",
	"美股", "港股", "欧股", "日股", "外汇", "黄金", "原油", "大宗商品",
	"盘面", "收盘播报", "开盘播报", "快讯", "播报", "转播",
}

// junkFallback 关键词兜底：标题命中明显非官方特征时返回 true。
func junkFallback(title string) bool {
	t := strings.ToLower(title)
	for _, kw := range junkFallbackKeywords {
		if strings.Contains(t, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// ipoKeywords IPO 相关关键词：命中即判定为新股/申购/上市类新闻，直构事件不走 LLM。
var ipoKeywords = []string{
	"IPO", "新股", "申购", "中签", "首发", "过会", "招股", "发行价",
	"上市首日", "新股上市", "挂牌上市", "注册生效", "网上发行",
}

// matchIPOKeywords 判断标题是否属于 IPO 相关新闻（新股/申购/上市）。
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

// combinedBodyLimit 合并调用时正文截断长度（字符），控制单批 prompt 体积。
const combinedBodyLimit = 300

// combinedJudge 合并调用的单条判定结果。
type combinedJudge struct {
	Official       bool   // 是否为官方/权威来源（非机构观点/互动/海外盘面）
	Material       bool   // 是否有投资参考价值
	CorrectedTitle string // 标题党校正标题（为空表示标题忠于正文）
}

// classifyCombined 合并 Stage0 垃圾过滤 + Stage1 价值初筛 + 标题党复核 为单次 LLM 分批调用。
// 失败走轮询重试（每批最多3次、间隔递增），仍失败返回错误，由调用方将整批归一般（不降级关键词）。
func (a *Agent) classifyCombined(titles, bodies []string) ([]combinedJudge, error) {
	n := len(titles)
	out := make([]combinedJudge, n)
	if n == 0 {
		return out, nil
	}
	if a.llmClient == nil {
		log.Printf("[newsagent] LLM未配置, Stage0/1合并跳过")
		return out, fmt.Errorf("LLM未配置")
	}

	// 按 llmBatchSize 分块调用，控制单批 prompt 体积避免超时
	for _, b := range batchBounds(n, llmBatchSize) {
		start, end := b[0], b[1]
		// 拼装批内 prompt：序号 + 标题 + 截断后的正文（供标题党复核）
		var sb strings.Builder
		for i := start; i < end; i++ {
			body := truncateRunes(bodies[i], combinedBodyLimit)
			sb.WriteString(fmt.Sprintf("%d. %s\n正文: %s\n", i-start+1, titles[i], body))
		}

		// 调用+解析统一进重试队列：API 连接失败 与 JSON 解析失败 都纳入轮询重试
		//（解析失败不再直接吞掉，而是与调用失败同样退避重试）。
		// 该批重试到头仍解析不出 → 隔离跳过本批（丢本批，不影响主干其余批次）。
		raw, err := a.stage0ParseRetry(sb.String())
		if err != nil {
			os.WriteFile("/tmp/5t0_fail_"+time.Now().Format("150405")+".json", []byte(sb.String()), 0o644)
			log.Printf("[newsagent] Stage0/1合并 该批%d条重试队列用尽仍解析失败, 跳过本批(主干继续): %v", end-start, err)
			continue
		}
		// 将批内序号映射回全局索引并落盘到结果切片（越界序号安全忽略）
		for _, r := range raw {
			if r.Index < 1 || int(r.Index) > end-start {
				continue
			}
			idx := start + int(r.Index) - 1
			out[idx].Official = strings.EqualFold(r.Category, "official")
			out[idx].Material = bool(r.Material)
			out[idx].CorrectedTitle = strings.TrimSpace(r.Corrected)
		}
	}
	return out, nil
}

// stage0EmptyValueRe 匹配对象键与空值畸形：`"key":]` 或 `"key":}`（模型丢了值直接写括号）。
// 修复为 `"key": ""`，覆盖单个对象解析时的空值缺失。
var stage0EmptyValueRe = regexp.MustCompile(`("(?:[^"\\]|\\.)*"\s*:)\s*[}\]]`)

// trailingJunkRe 匹配字符串收引号后紧跟的杂散 `)`/`'`（如 "上涨"") 、"死"'} ），
// 归一为单收引号，恢复为合法 JSON。
var trailingJunkRe = regexp.MustCompile(`"\s*[\)']+\s*([,}\]]|$)`)

// stage0Obj matches a single Stage0 judgement object shape.
type stage0Judge struct {
	Index     flexInt  `json:"index"`
	Category  string   `json:"category"`
	Material  flexBool `json:"material"`
	Corrected string   `json:"corrected_title"`
}

// flexInt 兼容 JSON 中整数字段为数字或字符串（如 1 或 "1"）的解析。
// 推理模型常把数值输出成带引号字符串，标准 json.Unmarshal 到 int 会失败。
type flexInt int

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
type flexBool bool

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
// 1) 整体当作 JSON 数组解析（正常路径）；
// 2) 整体失败 → 逐对象抢救：用花括号深度扫描提取每个 {…} 独立解析（无视换行/逗号/包裹格式），
//    先修复 "key":] / "key":} 的空值畸形与字符串 index/material，单个坏对象只丢该条，不整批废弃。
// 返回 (解析结果, 是否获得至少一条)。
func salvageStage0Objects(resp string) ([]stage0Judge, bool) {
	var raw []stage0Judge
	if err := json.Unmarshal([]byte(resp), &raw); err == nil {
		return raw, true
	}
	var out []stage0Judge
	for _, obj := range extractObjects(resp) {
		// 单引号畸形：模型把键尾引号/空值写成单引号，如 "corrected_title':''"，
		// 修复为 "corrected_title":""。
		obj = strings.ReplaceAll(obj, `':''`, `":"`)
		// 字符串收尾垃圾：`"上涨"")` / `"死"'} `（收引号后多引号/括号/撇号）→ 归一为收引号。
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

	judgements, err := a.classifyCombined(titles, bodies)
	if err != nil {
		// 不兜底：整批归一般（仅展示），下一轮轮询可重新拉取
		res.Err = err
		log.Printf("[newsagent] Stage0/1合并失败, 整批 %d 条归一般: %v", len(items), err)
		for i := range items {
			res.GeneralIdx = append(res.GeneralIdx, i)
		}
		return res
	}

	res.Material = make(map[int]bool)
	res.CorrectedTitle = make(map[int]string)
	for i, item := range items {
		j := judgements[i]
		// 规则一：非官方来源（机构观点/互动/海外盘面）直接归一般，仅展示不进引擎
		if !j.Official {
			res.GeneralIdx = append(res.GeneralIdx, i)
			continue
		}
		// 规则二：IPO 新闻（新股/申购/上市关键词）直构事件，不走 LLM 深度分析
		if matchIPOKeywords(item.Title) {
			res.IpoIdx = append(res.IpoIdx, i)
			continue
		}
		// 个股归因：标题+正文共同匹配（正文含公司全称，弥补标题简称漏配）
		text := item.Title
		if len(item.Content) > 0 {
			text += " " + item.Content
		}
		// 规则三：命中已知股票名 → 个股新闻，预填关联股票并视为有投资价值
		if a.cleaner != nil {
			if hits := a.cleaner.FindStocksInText(text); len(hits) > 0 {
				res.StockIdx = append(res.StockIdx, i)
				res.Material[i] = true
				applyCorrected(&res, i, item.Title, j)
				continue
			}
		}
		// 规则四：标题含行业/宏观关键词 → 板块新闻，须通过 LLM 的 material 价值初筛
		if matchKeywords(item.Title) {
			res.SectorIdx = append(res.SectorIdx, i)
			res.Material[i] = j.Material
			applyCorrected(&res, i, item.Title, j)
			continue
		}
		// 兜底：其余新闻归一般
		res.GeneralIdx = append(res.GeneralIdx, i)
	}
	log.Printf("[newsagent] Stage0分类(合并): 个股%d 板块%d IPO%d 一般%d (材料通过%d)",
		len(res.StockIdx), len(res.SectorIdx), len(res.IpoIdx), len(res.GeneralIdx), len(res.Material))
	return res
}

// applyCorrected 记录标题党校正标题（仅当 LLM 给出且与原标题不同）。
func applyCorrected(res *Stage0Result, idx int, original string, j combinedJudge) {
	if j.CorrectedTitle != "" && j.CorrectedTitle != original {
		res.CorrectedTitle[idx] = j.CorrectedTitle
	}
}

// newsTitles 提取新闻列表的标题。
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
const llmBatchSize = 10

// llmRetryMax LLM 单次调用的最大重试次数（含首次），指数退避，应对上游瞬时抖动/连不通。
const llmRetryMax = 5

// retryLLM 以指数退避方式重试一次 LLM 调用：失败重试 5 次，间隔 1s/2s/4s/8s。
// 返回 (响应, 最后一次错误)。记录每次失败以便排障。
// stage0ParseRetry 单批"调用+解析"的重试循环（重试队列）：API 失败 与 JSON 解析失败 一律退避重试，
// 任一次调用能抢救出 ≥1 条即成功（局部坏对象已由 salvageStage0Objects 内部跳过），
// 全部失败返回错误，由调用方将该批隔离跳过（不影响主干其余批次）。
func (a *Agent) stage0ParseRetry(userMsg string) ([]stage0Judge, error) {
	var lastErr error
	for attempt := 1; attempt <= llmRetryMax; attempt++ {
		resp, err := a.llmClient.Chat(stageCombinedSystemPrompt, userMsg)
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
func backoffStep(attempt int) time.Duration {
	base := []time.Duration{1, 2, 4, 8, 16}
	if attempt-1 < len(base) {
		return base[attempt-1] * time.Second
	}
	return 16 * time.Second
}

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

// classifyJunk LLM 分批分类新闻为 官方/机构/互动/海外 四类，返回垃圾（非 official）索引集合。
// LLM 失败时降级为关键词过滤（仅剔除明显非官方的机构/互动/海外盘面类），保证流水线不中断。
// 分批上限 llmBatchSize 条/次，避免超大批次导致 LLM 超时。
func (a *Agent) classifyJunk(titles []string) (map[int]bool, error) {
	junk := make(map[int]bool)
	if len(titles) == 0 {
		return junk, nil
	}
	if a.llmClient == nil {
		log.Printf("[newsagent] LLM未配置, Stage0垃圾过滤跳过")
		return junk, nil
	}

	// 分块处理，每批最多 llmBatchSize 条，控制 prompt 体积避免 LLM 超时
	for _, b := range batchBounds(len(titles), llmBatchSize) {
		start, end := b[0], b[1]
		chunk := titles[start:end]

		// 拼装批内标题列表 prompt
		var sb strings.Builder
		for i, t := range chunk {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t))
		}

		// LLM 轮询重试：指数退避最多 5 次
		resp, err := retryLLM(sb.String(), func() (string, error) {
			return a.llmClient.Chat(stage0FilterSystemPrompt, sb.String())
		})
		if err != nil {
			// 降级策略：LLM 不可用时改用关键词兜底，只剔除明显非官方内容，保证流水线不中断
			log.Printf("[newsagent] Stage0垃圾过滤 LLM失败, 降级关键词过滤该批%d条: %v", len(chunk), err)
			for j := start; j < end; j++ {
				if junkFallback(titles[j]) {
					junk[j] = true
				}
			}
			continue
		}

		resp = cleanJSON(resp)
		var raw []struct {
			Index int    `json:"index"`
			Type  string `json:"type"`
		}
		if err := json.Unmarshal([]byte(resp), &raw); err != nil {
			// JSON 解析失败不降级：整批按非官方处理（宁可误杀不放垃圾）
			log.Printf("[newsagent] Stage0垃圾过滤JSON解析失败(不降级), 该批%d条归一般: %v", len(chunk), err)
			for j := start; j < end; j++ {
				junk[j] = true
			}
			continue
		}

		// 非 official 类型的条目标记为垃圾，序号做越界安全过滤
		for _, r := range raw {
			if strings.EqualFold(r.Type, "official") {
				continue
			}
			if r.Index >= 1 && r.Index <= end-start {
				junk[start+r.Index-1] = true
			}
		}
	}
	log.Printf("[newsagent] Stage0垃圾过滤: %d/%d 条非官方", len(junk), len(titles))
	return junk, nil
}

// classifyMaterial Stage1 初筛：优先使用 LLM 判断新闻价值，无 LLM 时回退关键词过滤。
// 分批处理（llmBatchSize 条/次）。某批 LLM 失败/解析失败时该批全部视为有价值。
func (a *Agent) classifyMaterial(titles []string) ([]int, error) {
	if len(titles) == 0 {
		return nil, nil
	}
	if a.llmClient == nil {
		log.Printf("[newsagent] LLM未配置, 使用关键词过滤Stage1")
		var matched []int
		for i, t := range titles {
			if matchKeywords(t) {
				matched = append(matched, i)
			}
		}
		log.Printf("[newsagent] Stage1关键词过滤: %d/%d 条", len(matched), len(titles))
		return matched, nil
	}

	var valid []int
	// 分块处理，每批最多 llmBatchSize 条
	for _, b := range batchBounds(len(titles), llmBatchSize) {
		start, end := b[0], b[1]
		chunk := titles[start:end]

		// 拼装批内标题列表 prompt
		var sb strings.Builder
		for i, t := range chunk {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, t))
		}

		// LLM 轮询重试：最多 3 次，固定间隔 2s
		var resp string
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			resp, err = a.llmClient.Chat(stage1SystemPrompt, sb.String())
			if err == nil {
				break
			}
			if attempt < 3 {
				log.Printf("[newsagent] Stage1 LLM失败(第%d次), 重试: %v", attempt, err)
				time.Sleep(2 * time.Second)
			}
		}
		if err != nil {
			// 该批 LLM 失败：整批视为有价值，避免漏掉重要事件
			log.Printf("[newsagent] Stage1失败, 该批%d条全部视为有价值: %v", len(chunk), err)
			for j := start; j < end; j++ {
				valid = append(valid, j)
			}
			continue
		}
		resp = cleanJSON(resp)

		var indices []int
		if err := json.Unmarshal([]byte(resp), &indices); err != nil {
			// JSON 解析失败：同样整批视为有价值
			log.Printf("[newsagent] Stage1 JSON解析失败, 该批%d条全部视为有价值: %v", len(chunk), err)
			for j := start; j < end; j++ {
				valid = append(valid, j)
			}
			continue
		}

		// 转为0-based + 偏移到全局索引 + 安全过滤
		for _, idx := range indices {
			gi := start + idx - 1
			if gi >= start && gi < end {
				valid = append(valid, gi)
			}
		}
	}

	log.Printf("[newsagent] Stage1初筛: %d/%d 条有价值", len(valid), len(titles))
	return valid, nil
}

// cleanJSON 清理 LLM 返回的 JSON 字符串中的 markdown 代码块标记。
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// 提取 JSON 数组边界（第一个 [ 到最后一个 ]）——推理模型会在 JSON 前输出思考过程。
	if start := strings.IndexByte(s, '['); start >= 0 {
		if end := strings.LastIndexByte(s, ']'); end > start {
			s = s[start : end+1]
		}
	}
	s = strings.TrimRight(s, ".,; ")
	// 剥离数值位置的裸 '+' 前缀（"score": +0.75 是非法 JSON 数字）。
	s = plusNumberRe.ReplaceAllString(s, "$1 ")
	// 清洗非法转义（\( 等）与字符串内未转义换行；并修复对象键后单引号/空值畸形。
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

// isValidJSONEscape 判断字节是否为合法 JSON 转义字符。
func isValidJSONEscape(b byte) bool {
	switch b {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
		return true
	}
	return false
}

// plusNumberRe 匹配冒号/逗号/左括号后的 '+' 前缀（数值位置），用于剥离非法 '+'。
var plusNumberRe = regexp.MustCompile(`([:,\[])\s*\+`)
