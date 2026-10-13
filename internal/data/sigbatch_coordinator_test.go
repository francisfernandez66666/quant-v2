// sigbatch_coordinator_test.go — §SIGBATCH 数据层批量补价回归（现网 2026-10-13 信号 tab「请求超时」）。
//
// 断言的是**上游请求数与代码数解耦**这一维：dc.BatchQuotes 必须把 N 只代码压成
// ceil(N/BatchQuoteChunk) 次同花顺（新）批量快照请求，而不是像 dc.GetQuote 那样逐票一次。
// 现网实录：观察件放宽后当日信号组数 75→271，展示侧逐票补价 ⇒ 每次 20s 轮询约 210 次上游调用
// ⇒ 响应恒超前端 20s AbortController ⇒ 信号列表一条都渲染不出来。
//
// 四条断言：
// ① 650 只代码 = 3 次请求（分片上限 300），且每次请求携带的代码数 ≤ 300；
// ② 结果按裸码键返回，带前缀/后缀的原始写法另有别名键，调用方按自己传入的串就能查到；
// ③ 分片失败只影响该分片：其余分片的结果照常交回，并以 err 报告失败（不回落到逐票链）；
// ④ 零价条目不进结果（与 GetQuote 的 Price>0 判据一致），hithink 客户端缺失时明确报错而非静默空表。
//
// English: §SIGBATCH data-layer regression — BatchQuotes issues one upstream request per
// chunk of at most 300 symbols (not one per symbol), aliases prefixed spellings, keeps
// per-chunk failures contained, and never falls back to the per-symbol chain.
package data

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// sbStubServer 起一个同花顺（新）快照桩：按请求里的 thscodes 逐条返回可解析的行情，
// 并把每次请求的代码列表记下来（供「请求数 / 每请求代码数」断言）。
// failOn 指定第几次请求返回业务错误（0＝全部成功），用于分片失败隔离断言。
func sbStubServer(t *testing.T, failOn int) (*httptest.Server, *sbStubHits) {
	t.Helper()
	hits := &sbStubHits{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		codes := strings.Split(r.URL.Query().Get("thscodes"), ",")
		if codes[0] == "" {
			codes = nil
		}
		n := hits.record(codes)
		w.Header().Set("Content-Type", "application/json")
		if failOn > 0 && n == failOn {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 5000, "message": "stub failure"})
			return
		}
		items := make([]map[string]any, 0, len(codes))
		for _, c := range codes {
			suffix := ".SH"
			if strings.HasPrefix(c, "0") || strings.HasPrefix(c, "3") {
				suffix = ".SZ"
			}
			items = append(items, map[string]any{
				"thscode":                c + suffix, // 上游按 thscode 回，BatchQuotes 会切成裸码键
				"ticker":                 c,
				"last_price":             12.5,
				"price_change_ratio_pct": 2.5,
				"open_price":             12.0,
				"high_price":             12.8,
				"low_price":              11.9,
				"prev_price":             12.2,
				"volume":                 1000000,
				"turnover":               12500000,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"item": items, "total": len(items)},
		})
	}))
	old := HithinkBaseURL
	HithinkBaseURL = srv.URL
	t.Cleanup(func() {
		HithinkBaseURL = old
		srv.Close()
	})
	return srv, hits
}

// sbStubHits 桩侧的请求脚印（并发安全：分片是顺序发的，但锁住以免未来并行改动把测试变成竞态）。
type sbStubHits struct {
	mu       sync.Mutex
	requests [][]string
}

// record 记一次请求并返回它是第几次（从 1 开始）。
func (h *sbStubHits) record(codes []string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := append([]string(nil), codes...)
	h.requests = append(h.requests, cp)
	return len(h.requests)
}

// count 返回请求次数。
func (h *sbStubHits) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

// sbCodes 生成 n 个 6 位裸码（100000 起），模拟当日几百条观察件的代码规模。
func sbCodes(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%06d", 100000+i))
	}
	return out
}

// sbCoordinator 构造注入了 hithink 客户端的协调器（客户端只读 env 里的 Key，不碰真实网络）。
func sbCoordinator(t *testing.T) *DataCoordinator {
	t.Helper()
	t.Setenv(HithinkAPIKeyEnv, "stub-key-for-unit-test")
	hc, err := NewHithinkClient()
	if err != nil {
		t.Fatalf("NewHithinkClient: %v", err)
	}
	dc := NewDataCoordinator(NewMarketAPI(), NewTHSClient())
	dc.SetHithink(hc)
	return dc
}

// TestBatchQuotesChunked ① 650 只代码只打 3 次上游（分片上限 300），而不是 650 次。
// 注意：期望值刻意写成**绝对数**（3 次、每片 300/300/50），不从 BatchQuoteChunk 派生——
// 派生写法会让锁跟着被破坏的常量一起走：实测把上限抬成 10 万后，派生断言仍然全绿，
// 等于「分片上限」这一维根本没有锁（反证 M5 现形）。
func TestBatchQuotesChunked(t *testing.T) {
	_, hits := sbStubServer(t, 0)
	dc := sbCoordinator(t)
	codes := sbCodes(650)
	got, err := dc.BatchQuotes(codes)
	if err != nil {
		t.Fatalf("BatchQuotes 期望成功, got err=%v", err)
	}
	if hits.count() != 3 {
		t.Fatalf("上游请求数=%d，期望 3（650 只代码 ÷ 每片 300）；逐票时代这个数等于 650", hits.count())
	}
	wantPerReq := []int{300, 300, 50}
	for i, req := range hits.requests {
		if i >= len(wantPerReq) {
			t.Fatalf("多出一第 %d 次请求（%d 只代码），期望只有 3 次", i+1, len(req))
		}
		if len(req) != wantPerReq[i] {
			t.Fatalf("第 %d 次请求携带 %d 只代码，期望 %d 只（前两片打满 300，末片收尾）", i+1, len(req), wantPerReq[i])
		}
	}
	if len(got) != len(codes) {
		t.Fatalf("结果条数=%d，期望 %d", len(got), len(codes))
	}
	if si := got[codes[649]]; si == nil || si.Price <= 0 {
		t.Fatalf("末只代码 %s 未取到行情: %+v", codes[649], si)
	}
}

// TestBatchQuotesDedupAndAlias ② 输入去重（含前缀/后缀写法）＋结果给原始写法别名。
func TestBatchQuotesDedupAndAlias(t *testing.T) {
	_, hits := sbStubServer(t, 0)
	dc := sbCoordinator(t)
	// 同一只票的四种写法只该发一次；空串与空白应被剔除。
	input := []string{"100001", " 100001 ", "100001.SH", "sh100001", "", "   ", "100002"}
	got, err := dc.BatchQuotes(input)
	if err != nil {
		t.Fatalf("BatchQuotes: %v", err)
	}
	if hits.count() != 1 {
		t.Fatalf("上游请求数=%d，期望 1", hits.count())
	}
	if len(hits.requests[0]) != 2 {
		t.Fatalf("发给上游的代码数=%d（%v），期望 2（去重去空后只剩 100001/100002）", len(hits.requests[0]), hits.requests[0])
	}
	for _, k := range []string{"100001", "100001.SH", "sh100001", "100002"} {
		if got[k] == nil {
			t.Fatalf("结果缺少键 %s（裸码＋原始写法别名都要在）；实际键=%v", k, sbKeys(got))
		}
	}
}

// TestBatchQuotesChunkFailureIsolated ③ 中间一片失败：其余两片的结果照常交回，并报告错误。
// 这条锁住的是「分片失败不回落到逐票链」的取舍——宁可少 300 只票的现价，
// 也不能把一次故障放大成 300 次逐票上游请求（那正是现网超时的形态）。
// 期望数同样写**绝对数**（3 次请求 / 305 条结果），理由见上一条用例的注释。
func TestBatchQuotesChunkFailureIsolated(t *testing.T) {
	_, hits := sbStubServer(t, 2)
	dc := sbCoordinator(t)
	codes := sbCodes(605)
	got, err := dc.BatchQuotes(codes)
	if err == nil {
		t.Fatalf("分片失败时期望返回错误，got nil（调用方需要知道有分片没到手）")
	}
	if hits.count() != 3 {
		t.Fatalf("上游请求数=%d，期望 3（失败的一片不得吞掉后面那片）", hits.count())
	}
	if len(got) != 305 {
		t.Fatalf("结果数=%d，期望 305（第 1 片 300 ＋ 第 3 片 5；失败分片不得拖走成功分片）", len(got))
	}
	if got[codes[0]] == nil {
		t.Fatalf("成功分片的首个代码结果缺失")
	}
	if got[codes[300]] != nil {
		t.Fatalf("失败分片（第 301~600 只）的代码不该有结果")
	}
	if got[codes[600]] == nil {
		t.Fatalf("失败之后的收尾分片代码缺失（说明失败分片吞走了后续分片）")
	}
}

// TestBatchQuotesZeroPriceSkipped 零价条目不进结果（脏快照不该冒充实时价，前端按缺数渲染）。
// 归属说明：这一行为由**上游客户端** HithinkClient.BatchQuotes 的 LastPrice<=0 过滤承担，
// 协调器里那处判据是纵深防御——实测删掉协调器那处本用例仍绿（反证 M7），
// 所以本用例锁的是「零价不进结果」这条契约，而不是协调器的那一行。
func TestBatchQuotesZeroPriceSkipped(t *testing.T) {
	old := HithinkBaseURL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"item": []map[string]any{
				{"thscode": "100000.SH", "ticker": "100000", "last_price": 0},
				{"thscode": "100001.SH", "ticker": "100001", "last_price": 9.9, "price_change_ratio_pct": 1.1},
			}},
		})
	}))
	HithinkBaseURL = srv.URL
	t.Cleanup(func() {
		HithinkBaseURL = old
		srv.Close()
	})
	dc := sbCoordinator(t)
	got, err := dc.BatchQuotes([]string{"100000", "100001"})
	if err != nil {
		t.Fatalf("BatchQuotes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("结果数=%d，期望 1（零价条目剔除）；实际键=%v", len(got), sbKeys(got))
	}
	if _, ok := got["100000"]; ok {
		t.Fatalf("零价条目 100000 不该出现在结果里")
	}
	if got["100001"].ChangePct != 1.1 {
		t.Fatalf("涨跌幅透传失败: %v", got["100001"].ChangePct)
	}
}

// TestHithinkBatchQuotesDropsZeroPrice 同一条行为的**承担者**锁：上游客户端自己就按 LastPrice<=0 跳过。
// 协调器层那条判据删掉仍绿（纵深防御），删掉这一处才会红——所以两条并列，缺一都没有锁。
func TestHithinkBatchQuotesDropsZeroPrice(t *testing.T) {
	old := HithinkBaseURL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"item": []map[string]any{
				{"thscode": "100000.SH", "ticker": "100000", "last_price": 0},
				{"thscode": "100001.SH", "ticker": "100001", "last_price": 6.6, "price_change_ratio_pct": -0.3},
			}},
		})
	}))
	HithinkBaseURL = srv.URL
	t.Cleanup(func() {
		HithinkBaseURL = old
		srv.Close()
	})
	t.Setenv(HithinkAPIKeyEnv, "stub-key-for-unit-test")
	hc, err := NewHithinkClient()
	if err != nil {
		t.Fatalf("NewHithinkClient: %v", err)
	}
	got, err := hc.BatchQuotes([]string{"100000", "100001"})
	if err != nil {
		t.Fatalf("BatchQuotes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("客户端结果数=%d，期望 1（零价条目由客户端过滤）", len(got))
	}
	if _, ok := got["100000"]; ok {
		t.Fatalf("零价条目 100000 出现在客户端结果里")
	}
}

// TestBatchQuotesNoHithinkClient ④ 未注入同花顺（新）客户端时明确报错（空表＋nil 错误会让调用方以为「都取到了只是缺价」）。
func TestBatchQuotesNoHithinkClient(t *testing.T) {
	dc := NewDataCoordinator(NewMarketAPI(), NewTHSClient())
	got, err := dc.BatchQuotes([]string{"600519"})
	if err == nil {
		t.Fatalf("期望明确错误（源未配置），got nil")
	}
	if len(got) != 0 {
		t.Fatalf("结果应为空表，got %v", sbKeys(got))
	}
}

// sbKeys 结果键列表（失败信息里给出，便于定位是别名缺失还是命中缺失）。
func sbKeys(m map[string]*StockInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
