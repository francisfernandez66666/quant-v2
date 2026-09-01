// Package combat_agent D1 评分器单测：覆盖"LLM 失败不靠兜底、全部走重试队列"的语义——
//   - TestMarkRetryPending：LLM 失败标记 RetryPending=true 待重试，不回退上一轮、不归0占位
//   - TestBatchScoreNilLLM：LLM 未配置时全量归 0（无 LLM 可重试）
//   - TestCleanJSONInteriorBOM：LLM 输出数组内部夹 BOM 时仍可被解析（曾整批亏损）
//   - TestBatchScoreChunked：按 llmBatchSize 分批调用 LLM，每批独立解析、不漏股
package combat_agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"quant-trading-v2/internal/llm"
	"quant-trading-v2/internal/newsagent"
	"quant-trading-v2/internal/strategy_engine"
)

// TestFindEventForCodeByName 覆盖板块级新闻"只带名称不带代码"的场景：按个股名称命中关联事件。
// 此前 findEventForCode 仅按代码子串匹配，导致 LLM 关联的"招金黄金"等名称永远配不上 → D1=0 → 无信号。
func TestFindEventForCodeByName(t *testing.T) {
	events := []newsagent.NewsEvent{
		{Title: "贵金属板块整体走高", RelatedStocks: []string{"招金黄金", "株冶集团", "中金黄金", "兴业银锡"}},
	}
	md := &strategy_engine.StockMarketData{Name: "招金黄金"}
	// 纯名称命中
	if got := findEventForCode("600540", md, events); got != "贵金属板块整体走高" {
		t.Fatalf("按名称应命中, got %q", got)
	}
	// 名称(代码) 形态（propagateSectorToStocks 注入产物）
	md2 := &strategy_engine.StockMarketData{Name: "株冶集团"}
	events2 := []newsagent.NewsEvent{{Title: "贵金属走强", RelatedStocks: []string{"株冶集团(600961)"}}}
	if got := findEventForCode("600961", md2, events2); got != "贵金属走强" {
		t.Fatalf("标签形态应命中, got %q", got)
	}
	// 无名称时按代码命中
	mdNil := &strategy_engine.StockMarketData{Name: ""}
	events3 := []newsagent.NewsEvent{{Title: "中金涨停", CleanedStocks: []string{"中金黄金|600916"}}}
	if got := findEventForCode("600916", mdNil, events3); got != "中金涨停" {
		t.Fatalf("CleanedStocks代码应命中, got %q", got)
	}
	// 名称/代码都不匹配 → 空
	if got := findEventForCode("000001", &strategy_engine.StockMarketData{Name: "平安银行"}, events); got != "" {
		t.Fatalf("无关股票应返回空, got %q", got)
	}
}

// TestCleanJSONInteriorBOM 覆盖 d1 修复：LLM 返回 JSON 数组内部夹 UTF-8 BOM（0xEF 0xBB 0xBF）
// 时，过去 cleanJSON 只剥首尾导致 json.Unmarshal 整批失败、全部归 0；现在应全局剔除可正常解析。
func TestCleanJSONInteriorBOM(t *testing.T) {
	// 模拟 LLM 输出：数组第二个对象内 "reason" 值前、以及元素分隔处混入 BOM
	raw := "\ufeff```json\n[\n  {\"code\":\"600519\",\"score\":0.7,\"blocked\":false,\"reason\":\"板块利好\"},\n" +
		"  {\"code\":" + "\ufeff" + "\"000001\",\"score\":\ufeff0.5,\"blocked\"\ufeff:true,\"reason\":\ufeff\"利空\"}\n]```\ufeff"

	got := cleanJSON(raw)
	var arr []D1Score
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("含内部BOM应可解析, cleanJSON=%q, err=%v", got, err)
	}
	if len(arr) != 2 {
		t.Fatalf("应解析出2只个股, got %d: %+v", len(arr), arr)
	}
	if arr[0].Code != "600519" || arr[0].Score != 0.7 || arr[0].Blocked {
		t.Fatalf("600519 解析异常: %+v", arr[0])
	}
	if arr[1].Code != "000001" || arr[1].Score != 0.5 || !arr[1].Blocked {
		t.Fatalf("000001 解析异常: %+v", arr[1])
	}
}

// TestCleanJSONReasonLiteralNewline 覆盖线上 D1 整批失败的根因：GLM-Z1 在 reason 字符串里
// 输出未转义换行（`invalid character '\n' in string literal`），此前 cleanJSON 不处理导致
// json.Unmarshal 整批失败、10 只全部标记待重试。现在应把字符串内字面换行转成 \n 并可正常解析。
func TestCleanJSONReasonLiteralNewline(t *testing.T) {
	// 模拟线上失败样本：reason 值内嵌真实换行（反引号内的 \n 是字面换行）
	raw := "```json\n[\n  {\"code\":\"688432\",\"score\":18,\"blocked\":false,\"reason\":\"半导体材料供应商\n受益于上海张江能智终端产业链政策\"},\n  {\"code\":\"600460\",\"score\":18,\"blocked\":false,\"reason\":\"半导体分立器件企业，伴随AI终端规划受益\"}\n]```"

	got := cleanJSON(raw)
	var arr []D1Score
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatalf("reason含字面换行应可解析, cleanJSON=%q, err=%v", got, err)
	}
	if len(arr) != 2 {
		t.Fatalf("应解析出2只个股, got %d: %+v", len(arr), arr)
	}
	if arr[0].Code != "688432" || arr[0].Score != 18 || arr[0].Blocked {
		t.Fatalf("688432 解析异常: %+v", arr[0])
	}
	if arr[0].Reason != "半导体材料供应商\n受益于上海张江能智终端产业链政策" {
		t.Fatalf("reason 字面换行应转义为 \\n 且内容保留, got %q", arr[0].Reason)
	}
}

// TestMarkRetryPending 验证 LLM 失败不靠兜底：
// 失败的个股标记 RetryPending=true（Score=0），入重试队列下轮重新调 LLM，
// 不回退上一轮评分、不伪造分数。
func TestMarkRetryPending(t *testing.T) {
	ds := &D1Scorer{}
	// 即使"上一轮有分"，LLM 本轮失败也不得回退复用：必须标记待重试。
	result := map[string]D1Score{
		"600519": {Code: "600519", Score: 0.7, Blocked: false, Reason: "上一轮评分"},
	}
	ds.markRetryPending(result, []string{"600519"}, "LLM失败")
	got := result["600519"]
	if got.Score != 0 || got.RetryPending != true || got.Blocked {
		t.Fatalf("应标记 RetryPending 待重试(Score=0), got %+v", got)
	}
	if !strings.Contains(got.Reason, "待重试") {
		t.Fatalf("Reason 应注明待重试, got %q", got.Reason)
	}
}

// TestBatchScoreNilLLM 验证 LLM 未配置时全量归 0（无 LLM 可重试，不标 RetryPending）。
func TestBatchScoreNilLLM(t *testing.T) {
	ds := NewD1Scorer(nil, "")
	got := ds.BatchScore([]string{"600519", "000001"}, nil, nil)
	if got["600519"].Score != 0 || got["000001"].Score != 0 {
		t.Fatalf("LLM未配置应全量0分, got %+v", got)
	}
	if len(got) != 2 {
		t.Fatalf("结果应含2只个股, got %d", len(got))
	}
	if got["600519"].RetryPending {
		t.Fatalf("LLM未配置不应标 RetryPending（无可重试）: %+v", got["600519"])
	}
}

// TestSetMaxRetries 验证轮询重试次数（含首次）默认值与配置语义：
// §信号速度 S5 默认=2（含首次=1 次重试，当轮不反复死磕，失败进下轮重试队列），
// 0/负值回退默认，显式正值生效。
func TestSetMaxRetries(t *testing.T) {
	ds := NewD1Scorer(nil, "")
	if ds.maxAttempts != defaultMaxAttempts {
		t.Fatalf("默认重试次数=%d, want %d", ds.maxAttempts, defaultMaxAttempts)
	}
	if defaultMaxAttempts != 2 {
		t.Fatalf("§S5 默认应为2(含首次=1次重试), got %d", defaultMaxAttempts)
	}
	// 显式配置生效
	if got := ds.SetMaxRetries(8); got != 8 || ds.maxAttempts != 8 {
		t.Fatalf("显式重试次数失败: got %d", got)
	}
	// 0/负值回退默认
	if got := ds.SetMaxRetries(0); got != defaultMaxAttempts {
		t.Fatalf("0应回退默认, got %d", got)
	}
	if got := ds.SetMaxRetries(-1); got != defaultMaxAttempts {
		t.Fatalf("-1应回退默认, got %d", got)
	}
}

// TestBatchScoreChunked 覆盖"按 llmBatchSize 分批调用"：
// 25 只个股应产生 ceil(25/10)=3 次 LLM 调用，每批 prompt 内个股数 ≤10；
// 每批 LLM 全量返回 → 结果 25 只全有分数、无漏项、无 fallback 回退。
func TestBatchScoreChunked(t *testing.T) {
	var mu sync.Mutex
	var calls [][]string
	var prompts []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// 从 user 消息内容里提取本批实际请求的个股代码
		var req llm.ChatRequest
		var codes []string
		if err := json.Unmarshal(body, &req); err == nil {
			for _, m := range req.Messages {
				if m.Role == "user" {
					for _, line := range strings.Split(m.Content, "\n") {
						line = strings.TrimSpace(line)
						if idx := strings.Index(line, "代码: "); idx >= 0 {
							codes = append(codes, strings.TrimSpace(line[idx+len("代码: "):]))
						}
					}
				}
			}
		}
		mu.Lock()
		calls = append(calls, append([]string{}, codes...))
		prompts = append(prompts, string(body))
		mu.Unlock()
		// 回显同批 codes 的完整 JSON（包在 OpenAI chat.completions 响应壳内），模拟 LLM 全量返回
		var arr []map[string]any
		for _, c := range codes {
			arr = append(arr, map[string]any{"code": c, "score": 0.5, "blocked": false, "reason": "测试"})
		}
		b, _ := json.Marshal(arr)
		resp := map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"content": string(b)}},
			},
		}
		out, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	defer srv.Close()

	cl := llm.New(llm.Config{
		APIKey:    "test",
		APIURL:    srv.URL,
		Model:     "test-model",
		Streaming: false,
		Timeout:   0,
	})
	ds := NewD1Scorer(cl, "")

	// 25 只个股（代码 600001~600025），应分 3 批（10/10/5）
	var codes []string
	for i := 1; i <= 25; i++ {
		codes = append(codes, fmt.Sprintf("600%03d", i))
	}
	// 每只都带关联事件，保证 LLM 的 0.5 分不被"无实质事件→归0"规则清掉（该规则另测）
	// English: give every code a linked event so the LLM's 0.5 survives the no-event-zeroing rule (tested separately).
	events := make([]newsagent.NewsEvent, 0, len(codes))
	for _, c := range codes {
		events = append(events, newsagent.NewsEvent{Title: "事件-" + c, RelatedStocks: []string{c}})
	}
	got := ds.BatchScore(codes, events, nil)

	if len(calls) != 3 {
		t.Fatalf("应产生3次LLM调用, got %d", len(calls))
	}
	for i, c := range calls {
		if len(c) > llmBatchSize {
			t.Fatalf("第%d批个股数=%d 应≤%d", i+1, len(c), llmBatchSize)
		}
	}
	if len(prompts) != 3 {
		t.Fatalf("应构建3个prompt, got %d", len(prompts))
	}
	// 25 只全部有分、无漏项
	if len(got) != 25 {
		t.Fatalf("应25只全有分, got %d", len(got))
	}
	for _, c := range codes {
		if s, ok := got[c]; !ok || s.Score != 0.5 {
			t.Fatalf("%s 应有score=0.5, got %+v ok=%v", c, s, ok)
		}
	}
	// 分批边界：所有分批结果合并后应覆盖全部代码（并发下批次顺序不定，按内容断言）
	// English: chunk boundaries — all chunks merged must cover every code (chunk order is
	// nondeterministic under concurrency, so assert by content).
	allPrompts := strings.Join(prompts, "\n")
	if !strings.Contains(allPrompts, "代码: 600001") || !strings.Contains(allPrompts, "代码: 600021") {
		t.Fatalf("分批边界异常: prompts=%v", prompts)
	}
}

// TestBatchScoreNoSubstantiveEventZeroed 验证"无实质事件→D1归0"：
// 个股既无关联新闻、也无板块正向事件时，即使 LLM 给了占位低分（如 0.5），
// 也强制归 0，杜绝其充当有效 D1 触发买入/固化提醒。
// 而带关联事件的个股保留 LLM 原始分。
// English: verifies the "no substantive event → D1=0" rule — a stock with no linked news and no bullish
// sector event is forced to 0 even if the LLM returned a placeholder low score, so it can't act as a valid
// D1 that fires/pins buy alerts. Stocks with a linked event keep their LLM score.
func TestBatchScoreNoSubstantiveEventZeroed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var arr []map[string]any
		for _, c := range []string{"600001", "600002", "600003"} {
			arr = append(arr, map[string]any{"code": c, "score": 0.5, "blocked": false, "reason": "测试"})
		}
		b, _ := json.Marshal(arr)
		resp := map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(b)}}},
		}
		out, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	defer srv.Close()

	cl := llm.New(llm.Config{APIKey: "test", APIURL: srv.URL, Model: "test-model", Streaming: false, Timeout: 0})
	ds := NewD1Scorer(cl, "")

	// 600001 有关联事件；600002 无关联事件；600003 无关联事件但所属板块有事件
	events := []newsagent.NewsEvent{{Title: "重大利好", RelatedStocks: []string{"600001"}}}
	ds.sectorEvents = map[string]string{"600003": "板块利好事件"}

	got := ds.BatchScore([]string{"600001", "600002", "600003"}, events, nil)

	if s := got["600001"]; s.Score != 0.5 {
		t.Fatalf("600001 有关联事件应保留 LLM 分 0.5, got %+v", s)
	}
	if s := got["600003"]; s.Score != 0.5 {
		t.Fatalf("600003 有板块事件应保留 LLM 分 0.5, got %+v", s)
	}
	if s := got["600002"]; s.Score != 0 {
		t.Fatalf("600002 无任何事件应强制归 0, got %+v", s)
	}
}

// TestStockMatchExactNoFragment §D5 回归：名称碎片不得误命中——"国电"只应精确匹配
// 名称/代码，不再双向 Contains 同时命中 国电电力/国电南瑞。
func TestStockMatchExactNoFragment(t *testing.T) {
	fragment := "国电"
	if stockMatch(fragment, "600795", &strategy_engine.StockMarketData{Name: "国电电力"}) {
		t.Fatal("碎片'国电'不应命中'国电电力'(精确匹配)")
	}
	// 精确形态仍然命中
	if !stockMatch("国电电力|600795", "600795.SH", &strategy_engine.StockMarketData{Name: "国电电力"}) {
		t.Fatal("名称|代码 应跨后缀精确命中")
	}
	if !stockMatch("国电电力(600795)", "600795", nil) {
		t.Fatal("名称(代码) 形态应按代码命中")
	}
	if !stockMatch("600795", "600795", nil) {
		t.Fatal("纯代码形态应命中")
	}
}
