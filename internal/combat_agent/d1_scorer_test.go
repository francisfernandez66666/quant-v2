// Package combat_agent D1 评分器单测：覆盖本轮"LLM 慢响应处理"改动的回退语义——
//   - TestFillFallback：D1 失败回退上一轮评分（有值复用 / 无值归 0）
//   - TestBatchScoreNilLLM：LLM 未配置时全量归 0，不受 fallback 影响
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

// TestFillFallback 验证 D1 缺失评分回退语义：
// fallback 有值则复用上一轮评分，无值则按 reason 归 0。
func TestFillFallback(t *testing.T) {
	ds := &D1Scorer{}
	fallback := map[string]D1Score{
		"600519": {Code: "600519", Score: 0.7, Blocked: false, Reason: "上一轮评分"},
	}
	result := map[string]D1Score{}

	// 有上一轮值：应回退复用，而非归 0
	ds.fillFallback(result, []string{"600519"}, fallback, "LLM失败")
	if got := result["600519"]; got.Score != 0.7 || got.Blocked || got.Reason != "上一轮评分" {
		t.Fatalf("回退失败: got %+v, want score=0.7 上一轮评分", got)
	}

	// 无上一轮值：按 reason 归 0
	ds.fillFallback(result, []string{"000001"}, fallback, "LLM失败")
	if got := result["000001"]; got.Score != 0 || got.Blocked || got.Reason != "LLM失败" {
		t.Fatalf("无回退归0失败: got %+v, want 0/LLM失败", got)
	}
}

// TestBatchScoreNilLLM 验证 LLM 未配置时全量归 0，不受 fallback 影响（无上一轮概念）。
func TestBatchScoreNilLLM(t *testing.T) {
	ds := NewD1Scorer(nil, "")
	fallback := map[string]D1Score{
		"600519": {Code: "600519", Score: 0.7, Blocked: false, Reason: "上一轮评分"},
	}
	got := ds.BatchScore([]string{"600519", "000001"}, nil, nil, fallback)
	if got["600519"].Score != 0 || got["000001"].Score != 0 {
		t.Fatalf("LLM未配置应全量0分, got %+v", got)
	}
	if len(got) != 2 {
		t.Fatalf("结果应含2只个股, got %d", len(got))
	}
}

// TestSetMaxRetries 验证轮询重试次数（含首次）默认值与配置语义：
// 默认加大到 defaultMaxAttempts（防重要 D1 信号随 LLM 偶发失败丢失），
// 0/负值回退默认，显式正值生效。
func TestSetMaxRetries(t *testing.T) {
	ds := NewD1Scorer(nil, "")
	if ds.maxAttempts != defaultMaxAttempts {
		t.Fatalf("默认重试次数=%d, want %d", ds.maxAttempts, defaultMaxAttempts)
	}
	if ds.maxAttempts < 3 {
		t.Fatalf("重试次数应比旧值(3)更大, got %d", ds.maxAttempts)
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
		APIKey:     "test",
		APIURL:     srv.URL,
		Model:      "test-model",
		Streaming:  false,
		Timeout:    0,
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
	got := ds.BatchScore(codes, events, nil, nil)

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
	// 分批日志应出现在输出中（覆盖分批评分路径）
	if !strings.Contains(prompts[0], "代码: 600001") || !strings.Contains(prompts[2], "代码: 600021") {
		t.Fatalf("分批边界异常: prompts[0]=%s prompts[2]=%s", prompts[0], prompts[2])
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

	got := ds.BatchScore([]string{"600001", "600002", "600003"}, events, nil, nil)

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
