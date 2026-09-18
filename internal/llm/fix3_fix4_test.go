// fix3_fix4_test.go — §FIX-3/§FIX-4(20260919) 回归护栏。
//
// FIX-3（流式空闲语义）：旧实现分片到达从不重置 ticker，idleTimeout 实为整段硬超时——
// 反向用例（持续滴流的慢速响应必须成功）在旧代码上必挂，是本次修复的核心证明。
// FIX-4（连接泄漏 + ctx 断链）：非 2xx 分支必须 Close 响应体；出呼请求必须随 ctx 取消。
// （English: the drip test is the regression proof for FIX-3 — it fails on the old code where
// idle ticks never reset; the body-close counter and cancel tests pin FIX-4.)
package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// sseChunk 组装一条 OpenAI 兼容 SSE 分片行。
func sseChunk(text string) string {
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
}

// roundTripperFunc 函数式 RoundTripper（注入确定性传输层，隔离 post 的错误分支测试）。
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestStreamIdleResetsOnChunks §FIX-3 反向用例（核心）：服务端每 60ms 滴一个分片、共 10 片，
// 总耗时 ~600ms >> idle 200ms——空闲按"相邻分片"计就应全部读完成功；
// 旧实现（空闲=整段硬超时）会在 200ms 误杀。
func TestStreamIdleResetsOnChunks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for i := 0; i < 10; i++ {
			fmt.Fprint(w, sseChunk(fmt.Sprintf("片段%d", i)))
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(60 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New(Config{
		APIKey:             "k",
		APIURL:             srv.URL,
		Streaming:          true,
		StreamIdleTimeout:  200 * time.Millisecond, // 远小于整段耗时：只有"分片即重置"才能活着读完
		StreamTotalTimeout: 30 * time.Second,
	})
	got, err := c.streamChat(ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("慢速滴流不应被空闲超时误杀: %v", err)
	}
	if !strings.Contains(got, "片段9") || !strings.Contains(got, "片段0") {
		t.Fatalf("分片拼接不完整: %q", got)
	}
}

// TestStreamIdleKillsTrueStall §FIX-3：真·卡流（首片后不再吐）仍须被空闲阈值掐死，
// 且错误文案保留「空闲超时」子串（doOnce 流式→非流式回落判定依赖该契约）。
func TestStreamIdleKillsTrueStall(t *testing.T) {
	// 请求 ctx 由客户端掐连接后自动取消 → handler 退出，无悬挂 goroutine。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseChunk("开头就卡住"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: true,
		StreamIdleTimeout: 300 * time.Millisecond, StreamTotalTimeout: 30 * time.Second})
	start := time.Now()
	_, err := c.streamChat(ChatRequest{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "空闲超时") {
		t.Fatalf("真卡流应报空闲超时, got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("空闲阈值 300ms 却等了 %s", d)
	}
}

// TestStreamTotalCapKillsEndlessDrip §FIX-3：滴流永不收尾 → 空闲一直被重置，
// 必须由总时长硬上限掐断（旧实现没有这一兜底，空闲"过紧即误杀、过松即永挂"两难）。
func TestStreamTotalCapKillsEndlessDrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		for i := 0; ; i++ {
			if _, err := fmt.Fprint(w, sseChunk("d")); err != nil {
				return // 客户端掐连接后写失败自然退出
			}
			if f != nil {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(30 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: true,
		StreamIdleTimeout:  5 * time.Second, // 空闲不会触发（30ms 一片）
		StreamTotalTimeout: 500 * time.Millisecond})
	_, err := c.streamChat(ChatRequest{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "总时长超限") {
		t.Fatalf("无限滴流应被总时长上限掐断, got %v", err)
	}
	if strings.Contains(err.Error(), "空闲超时") {
		t.Fatal("总限错误不得携带「空闲超时」子串（否则误触发非流式回落）")
	}
}

// TestStreamCtxCancelUnblocks §FIX-4：调用方 ctx 取消时 streamChatCtx 立即返回 ctx.Err()，
// 且解除读 goroutine（body.Close 后 Scan 报错退出），不悬挂。
func TestStreamCtxCancelUnblocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		for {
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if f != nil {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", APIURL: srv.URL, Streaming: true,
		StreamIdleTimeout: 30 * time.Second, StreamTotalTimeout: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.streamChatCtx(ctx, ChatRequest{Model: "m"})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消应返回 ctx.Err()，got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 streamChatCtx 未返回（悬挂）")
	}
}

// trackedBody 记录 Close 是否被调用的响应体（连接泄漏证明点）。
type trackedBody struct {
	io.Reader
	closed chan struct{}
	once   sync.Once
}

func (b *trackedBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// TestPostNon2xxClosesBody §FIX-4：非 2xx 分支读完后必须 Close 响应体——
// 旧实现每发一个错误响应就漏一条连接。
func TestPostNon2xxClosesBody(t *testing.T) {
	closed := make(chan struct{})
	rt := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 429,
			Header:     http.Header{},
			Body:       &trackedBody{Reader: strings.NewReader("rate limited: quota exhausted"), closed: closed},
		}, nil
	})
	c := New(Config{APIKey: "k", APIURL: "https://provider.example/v1/chat/completions"})
	c.httpClient.Transport = rt
	_, err := c.post(ChatRequest{Model: "m"}, false, 0)
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("非 2xx 应带状态码与响应体摘录, got %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("非 2xx 响应体未 Close —— 连接泄漏（§FIX-4 回归点）")
	}
}

// TestPostCtxCancelAbortsRequest §FIX-4：ctx 取消必须中止出呼（旧实现裸 NewRequest 无取消语义）。
func TestPostCtxCancelAbortsRequest(t *testing.T) {
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done() // 模拟上游永不返回
		return nil, r.Context().Err()
	})
	c := New(Config{APIKey: "k", APIURL: "https://provider.example/v1/chat/completions"})
	c.httpClient.Transport = rt
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := c.postCtx(ctx, ChatRequest{Model: "m"}, false, 0)
		errCh <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消应立刻上抛 context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("postCtx 未随 ctx 取消返回")
	}
}

// TestChatMessagesCtxConsultBudget §FIX-7：咨询专属日预算超限 → ErrBudgetExceeded（429 语义），
// 且错误链可被 errors.Is 穿透（engine 用 %w 包裹后 server 仍需识别）；总预算计数不受影响。
func TestChatMessagesCtxConsultBudget(t *testing.T) {
	c := New(Config{APIKey: "k", ConsultDailyCalls: 3})
	c.consultCalls.Store(3)
	_, err := c.ChatMessagesCtx(context.Background(), []Message{{Role: "user", Content: "q"}})
	if err == nil || !strings.Contains(err.Error(), "咨询日调用预算") {
		t.Fatalf("咨询预算用尽应熔断, got %v", err)
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("错误链必须可被 errors.Is(ErrBudgetExceeded) 识别, got %v", err)
	}
	// 预算 0 = 不设限：preFlightConsult 放行并计数（总预算检查仍在使用 usageCalls，互不串账）。
	c2 := New(Config{APIKey: "k"})
	before := c2.usageCalls.Load()
	if err := c2.preFlightConsult(); err != nil {
		t.Fatalf("预算 0 应放行: %v", err)
	}
	if c2.consultCalls.Load() != 1 || c2.usageCalls.Load() != before {
		t.Fatalf("咨询计数应独立: consult=%d usage=%d", c2.consultCalls.Load(), c2.usageCalls.Load())
	}
}

// TestTotalBudgetErrIsSentinel §FIX-7：总调用/token 预算错误同样携带 ErrBudgetExceeded。
func TestTotalBudgetErrIsSentinel(t *testing.T) {
	c := New(Config{APIKey: "k", DailyCallBudget: 1})
	c.usageCalls.Store(1)
	err := c.preFlight()
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("总预算错误应可 errors.Is, got %v", err)
	}
}
