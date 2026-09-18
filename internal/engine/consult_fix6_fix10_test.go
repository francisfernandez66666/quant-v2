// 本文件：§FIX-6 + §FIX-10（20260919 批四）回归。
// FIX-6：咨询外呼防护——buildConsultContext 按出现顺序截断至前 5 只并在数据块尾部注明；
// buildStockBlock 同代码 single-flight（并发首查只穿透一次）+ 缓存条数上限淘汰。
// FIX-10：咨询历史隔离——accountsRoot 缺失时**拒绝服务**（ErrStoreUnavailable，
// 上层映射 503），绝不回退共享 store；一轮"提问+回复"合并一次落盘；
// A 咨询 → B 读历史必空、B 咨询不影响 A。
// English: batch-4 regressions — per-message stock truncation, single-flight block loading
// with a bounded cache, and strict per-account consult-store isolation (refuse over leak).
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// TestBuildConsultContextTruncatesToFive §FIX-6：8 个代码只处理前 5 只（按出现顺序），
// 尾部必须注明截断，杜绝"数百代码消息=数百组行情外呼"。
func TestBuildConsultContextTruncatesToFive(t *testing.T) {
	e := &Engine{}
	msg := "对比 600001 600002 600003 600004 600005 600006 600007 600008 走势"
	got := e.buildConsultContext(msg)
	if n := strings.Count(got, "—— 股票 "); n != consultMaxStocks {
		t.Fatalf("数据块应截断为 %d 只, got %d\n%s", consultMaxStocks, n, got)
	}
	for _, keep := range []string{"600001", "600005"} {
		if !strings.Contains(got, keep) {
			t.Errorf("前 5 只（出现顺序）应含 %s: %s", keep, got)
		}
	}
	for _, drop := range []string{"600006", "600008"} {
		if strings.Contains(got, "股票 "+drop) {
			t.Errorf("第 6 只起应被丢弃 %s: %s", drop, got)
		}
	}
	if !strings.Contains(got, "仅处理前 5 只") || !strings.Contains(got, "分次提问") {
		t.Errorf("截断必须对用户可见: %s", got)
	}
}

// TestBuildStockBlockSingleFlight §FIX-6 护栏：同代码并发 10 路首查，穿透加载只发生 1 次，
// 其余 9 路共享同一结果。
func TestBuildStockBlockSingleFlight(t *testing.T) {
	var loads atomic.Int64
	release := make(chan struct{})
	e := &Engine{}
	e.consultBlockLoad = func(code, name string) string {
		loads.Add(1)
		<-release // 模拟慢行情：所有并发请求都赶在首查完成前到达
		return "BLOCK-" + code
	}
	var wg sync.WaitGroup
	results := make([]string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = e.buildStockBlock("600580", "卧龙电驱")
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // 让 10 路全部进入（首查持飞、其余挂等）
	close(release)
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("并发 10 路同代码首查应只穿透 1 次, got %d", n)
	}
	for i, r := range results {
		if r != "BLOCK-600580" {
			t.Fatalf("第 %d 路应拿到共享结果, got %q", i, r)
		}
	}
}

// TestConsultBlockCacheEviction §FIX-6：缓存条目超上限时先清过期、再淘汰最旧，map 不再无界。
func TestConsultBlockCacheEviction(t *testing.T) {
	e := &Engine{}
	e.consultBlockCache = map[string]consultBlockEntry{}
	// 塞满上限条数：oldest 写入时间最早（仍在 TTL 内，靠"最旧淘汰"而非过期清理）。
	base := time.Now().Add(-time.Second)
	for i := 0; i < consultBlockCacheMax; i++ {
		e.consultBlockCache[fmt.Sprintf("6%05d", i)] = consultBlockEntry{text: "X", at: base.Add(time.Duration(i) * time.Millisecond)}
	}
	e.consultBlockLoad = func(code, name string) string { return "NEW" }
	if got := e.buildStockBlock("300999", "新股"); got != "NEW" {
		t.Fatalf("新代码应穿透加载, got %q", got)
	}
	if len(e.consultBlockCache) > consultBlockCacheMax {
		t.Fatalf("写入后条数不得超上限 %d, got %d", consultBlockCacheMax, len(e.consultBlockCache))
	}
	if _, ok := e.consultBlockCache["600000"]; ok {
		t.Fatal("最旧条目必须被淘汰")
	}
	if _, ok := e.consultBlockCache["300999"]; !ok {
		t.Fatal("新写入条目必须在缓存中")
	}
}

// TestConsultRejectsWithoutIsolation §FIX-10：accountsRoot 未注入的登录用户咨询 →
// 拒绝服务（ErrStoreUnavailable），且拒绝发生在付费调用**之前**（mock LLM 零命中）。
func TestConsultRejectsWithoutIsolation(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	e := &Engine{} // accountsRoot 缺失：旧实现会静默回退共享 store（串号根源）
	e.llmClient = llm.New(llm.Config{APIKey: "k", APIURL: srv.URL, Model: "m", Streaming: false})
	_, err := e.ConsultLLM(context.Background(), "u_iso", "600580 怎么样", false)
	if err == nil || !errors.Is(err, data.ErrStoreUnavailable) {
		t.Fatalf("隔离不可用必须拒绝并带 ErrStoreUnavailable 哨兵, got %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("拒绝必须发生在 LLM 付费调用之前（零计费）")
	}
}

// TestConsultHistoryIsolationAB §FIX-10 集成：A 咨询 → B 读历史必空；B 咨询不影响 A。
// （全仓此前无一处覆盖 SetAccountsRoot 的账号隔离，本用例补上。）
func TestConsultHistoryIsolationAB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1].Content
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": "回复给[" + last + "]"},
			}},
		})
	}))
	defer srv.Close()

	e := &Engine{}
	e.SetAccountsRoot(t.TempDir())
	e.llmClient = llm.New(llm.Config{APIKey: "k", APIURL: srv.URL, Model: "m", Streaming: false})

	if _, err := e.ConsultLLM(context.Background(), "u_a", "A的提问", false); err != nil {
		t.Fatalf("A 咨询: %v", err)
	}
	if h := e.GetConsultHistoryFor("u_b"); len(h) != 0 {
		t.Fatalf("B 的历史必须为空（不得看到 A 的对话）: %+v", h)
	}
	if _, err := e.ConsultLLM(context.Background(), "u_b", "B的提问", false); err != nil {
		t.Fatalf("B 咨询: %v", err)
	}
	ha := e.GetConsultHistoryFor("u_a")
	if len(ha) != 2 {
		t.Fatalf("A 应只有自己一轮（提问+回复）2 条, got %d", len(ha))
	}
	if ha[0].Content != "A的提问" || !strings.Contains(ha[1].Content, "A的提问") {
		t.Fatalf("A 的历史被污染: %+v", ha)
	}
	// 一轮=一次落盘（AppendPair）：A 目录文件里恰两条。
	if hb := e.GetConsultHistoryFor("u_b"); len(hb) != 2 || hb[0].Content != "B的提问" {
		t.Fatalf("B 的历史寻址错误: %+v", hb)
	}
}

// TestConsultStoreCapAndPair 存储层：AppendPair 整轮一次写入；超 500 条丢最旧。
func TestConsultStoreCapAndPair(t *testing.T) {
	dir := t.TempDir()
	s := data.NewConsultStore(dir + "/consult_history.json")
	s.AppendPair(data.ConsultMessage{Role: "user", Content: "问"}, data.ConsultMessage{Role: "assistant", Content: "答"})
	if h := s.List(); len(h) != 2 || h[0].Role != "user" || h[1].Content != "答" {
		t.Fatalf("整轮两条应按序落盘: %+v", h)
	}
	// 灌到超限：501 轮 → 只保留最近 500 条。
	for i := 0; i < 250; i++ {
		s.AppendPair(data.ConsultMessage{Role: "user", Content: fmt.Sprintf("u%d", i)}, data.ConsultMessage{Role: "assistant", Content: fmt.Sprintf("a%d", i)})
	}
	h := s.List()
	if len(h) != 500 {
		t.Fatalf("应封顶 500 条, got %d", len(h))
	}
	if h[len(h)-1].Content != "a249" {
		t.Fatalf("最新条目必须保留: %+v", h[len(h)-1])
	}
}
