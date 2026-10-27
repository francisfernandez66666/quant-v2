// sigbatch_display_test.go — §SIGBATCH 展示侧补价回归（现网 2026-10-13 信号 tab「请求超时」）。
//
// 这里断言的是**一次请求打了几次上游**，不是取到的价格对不对：
// 改前 handleFixSignals 为每条信号起一个 goroutine 各调一次 quoteDisplay，
// 而观察件根本不在 fetcher 的 5s 快照池里（只有买入信号会被 EnsureStock 钉进池），
// 于是 quoteDisplay 每次都落到 dc.GetQuote 的逐票降级链（这一层没有任何行情缓存）。
// 现网实录：§N-RELAX 放宽后当日信号组数 75→271 ⇒ 每次 20s 轮询约 210 次上游调用
// ⇒ 响应恒被前端 20s AbortController 掐掉 ⇒ 信号 tab 永远「加载失败：请求超时」。
//
// 七条断言（用 batchQuoteFn 测试缝数调用次数；真实上游由数据层用例覆盖）：
// ① 271 条信号 ⇒ 批量补价只被调用 **1 次**（改前是 271 次逐票调用）；
// ② 代码已在 5s 快照里 ⇒ 批量补价 **0 次** 调用（快照优先，零网络）；
// ③ 批量失败 ⇒ 列表一条不少、现价退回信号触发价（证明修的是耗时而不是内容，缺价不撤信号）；
// ④ 批量卡死 ⇒ 请求在有界等待内返回，绝不把响应交给上游的 30s 客户端超时；
// ⑤ 同形的另外三个面（/api/snapshot、/api/evaluations、/api/watchlist）各自也只打 1 次批量
//
//	——Watchlist 页一轮刷新并发拨这三个接口，改前是「自选股数 × 3」次上游请求；
//
// ⑥ 三个面对「取不到价」的取舍各不相同且都是刻意的：行情面缺价即无该行，
//
//	自选/评分面缺价必须留行按缺数渲染（防止实现被"顺手统一"成抹行）；
//
// ⑦ 五个面每次响应都往日志写一条同格式的「现价补齐」读数，且读数的批量请求数恰为 1
//
//	——把「上游又被逐票打爆」从用户故障报告变成天天可比的读数（这条读数是本批的可见性腿）。
//
// English: §SIGBATCH display-side regression — asserts the *number of upstream calls* per
// request (1 for 271 symbols, 0 when the snapshot covers them), that a failed/late batch
// never empties the list, that the handler returns inside its bounded wait, that the three
// watchlist-side surfaces are batched the same way, that their differing row semantics
// on missing quotes are preserved, and that every surface emits the shared readout line.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/display"
)

// sbSignals 造 n 条不同代码的做多信号（模拟放宽门禁后的观察件规模）。
// 名称刻意不含 ST/退市字样，避免被展示复核剔除，从而把断言集中在「调用次数」这一维。
func sbSignals(n int) []combat_agent.Signal {
	out := make([]combat_agent.Signal, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, combat_agent.Signal{
			Code:         fmt.Sprintf("%06d", 100000+i),
			Name:         fmt.Sprintf("测试票%03d", i),
			Direction:    "做多",
			Action:       "watch",
			StrategyType: "n_shape",
			Price:        8.88, // 信号触发价：批量取不到价时的兜底显示值
			Confidence:   0.4,
		})
	}
	return out
}

// sbServer 搭一个「有看板信号、无真实行情源」的最小服务，返回 (s, admin, 信号列表)。
// fetcher/dc/market 均为 nil：quoteSnapshot 恒缺失，批量入口只能走 batchQuoteFn、
// 逐票补齐腿只能走 degradeQuoteFn——两条腿的真价都只能来自测试缝，
// 因此任何一次真实上游调用都无处可藏（没接缝时批量报「数据源未注入」、补齐立刻
// market quote unavailable，缺价会被行断言判红）。
// sbSoloRun 是这一句的唯一例外：它必须钉住补齐次数，所以会把 degradeQuoteFn 换成计数器。
func sbServer(t *testing.T, sigs []combat_agent.Signal) (*Server, *auth.User) {
	t.Helper()
	s, admin := newAdminTestServer(t)
	s.agg = display.New()
	s.agg.UpdateFast(nil, sigs, nil)
	if d := s.agg.Current(); d == nil || len(d.FinalSignals) == 0 {
		t.Fatalf("看板注入失败：FinalSignals 为空")
	}
	return s, admin
}

// sbCounter 批量缝的调用脚印（并发安全：批量跑在独立 goroutine 里）。
type sbCounter struct {
	mu    sync.Mutex
	calls [][]string
}

// record 记一次批量调用。
func (c *sbCounter) record(codes []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, append([]string(nil), codes...))
}

// count 返回调用次数。
func (c *sbCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// sbGetSignals 拨一次 GET /api/signals，返回 (HTTP 码, 行列表, 耗时)。
func sbGetSignals(t *testing.T, s *Server, admin *auth.User) (int, []map[string]interface{}, time.Duration) {
	t.Helper()
	return sbGetRows(t, s, admin, "/api/signals")
}

// sbGetRows 拨一次返回「行数组」的展示端点（/api/signals、/api/snapshot[/hot]、
// /api/evaluations、/api/watchlist 里除 watchlist 外层包了 {"stocks": …} 的都直接解数组）。
func sbGetRows(t *testing.T, s *Server, admin *auth.User, path string) (int, []map[string]interface{}, time.Duration) {
	t.Helper()
	req := adminReq(s, admin, http.MethodGet, path, "")
	start := time.Now()
	rr := adminDo(s, req)
	elapsed := time.Since(start)
	if rr.Code != http.StatusOK {
		return rr.Code, nil, elapsed
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("%s 响应非 JSON 数组: %v body=%.200s", path, err, rr.Body.String())
	}
	return rr.Code, rows, elapsed
}

// sbGetWatchlistRows 拨一次 GET /api/watchlist（外层是 {"stocks": [...]}）。
func sbGetWatchlistRows(t *testing.T, s *Server, admin *auth.User) (int, []map[string]interface{}, time.Duration) {
	t.Helper()
	req := adminReq(s, admin, http.MethodGet, "/api/watchlist", "")
	start := time.Now()
	rr := adminDo(s, req)
	elapsed := time.Since(start)
	if rr.Code != http.StatusOK {
		return rr.Code, nil, elapsed
	}
	var body struct {
		Stocks []map[string]interface{} `json:"stocks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("/api/watchlist 响应形态异常: %v body=%.200s", err, rr.Body.String())
	}
	return rr.Code, body.Stocks, elapsed
}

// sbWatchServer 在 sbServer 之上再装一份自选股管理器（n 只），返回 (s, admin, 代码列表)。
// /api/snapshot、/api/evaluations、/api/watchlist 三个面的代码集都来自这份自选列表，
// 共用一套夹具；代码段刻意与 sbSignals 的 100000 段错开，便于日志里分辨是哪一面。
func sbWatchServer(t *testing.T, n int) (*Server, *auth.User, []string) {
	t.Helper()
	s, admin := sbServer(t, sbSignals(3))
	wl := data.NewWatchlistManager(t.TempDir())
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		c := fmt.Sprintf("%06d", 200000+i)
		codes = append(codes, c)
		if !wl.Add(admin.ID, c) {
			t.Fatalf("自选夹具写入失败：Add(%s) 返回 false", c)
		}
	}
	s.watchlist = wl
	return s, admin, codes
}

// sbQuoteSeam 生成一个「记次数 + 按 priceOf 给价」的批量缝；priceOf 返回 0 表示上游没给出这只。
func sbQuoteSeam(ctr *sbCounter, priceOf func(code string) float64) func([]string) (map[string]*data.StockInfo, error) {
	return func(codes []string) (map[string]*data.StockInfo, error) {
		ctr.record(codes)
		out := make(map[string]*data.StockInfo, len(codes))
		for _, c := range codes {
			p := priceOf(c)
			if p <= 0 {
				continue
			}
			out[c] = &data.StockInfo{Code: c, Name: "自选" + c, Price: p, ChangePct: 1.5}
		}
		return out, nil
	}
}

// TestSigBatchDisplayWatchSurfacesOneCallPerRequest ⑤ 自选侧三个面各自也只打一次批量上游。
// 这一枚是「同形缺陷一起收编」的证明：Watchlist 页一轮刷新会并发拨
// /api/snapshot + /api/watchlist + /api/evaluations 三个接口，改前每个接口都逐票真打上游，
// 于是 150 只自选 = 一轮 450 次上游请求（与信号 tab 那次事故同一把尺子量出来的同一形状）。
// 只断「批量入口被调用 1 次、且一次带满 150 只、行数不塌」，内容对不对由 ⑥ 断。
func TestSigBatchDisplayWatchSurfacesOneCallPerRequest(t *testing.T) {
	s, admin, codes := sbWatchServer(t, 150)
	cases := []struct {
		name  string
		path  string
		watch bool // /api/watchlist 外层包了 {"stocks": [...]}
	}{
		{name: "/api/snapshot", path: "/api/snapshot?codes=" + strings.Join(codes, ",")},
		{name: "/api/evaluations", path: "/api/evaluations"},
		{name: "/api/watchlist", path: "/api/watchlist", watch: true},
	}
	for _, tc := range cases {
		ctr := &sbCounter{}
		s.batchQuoteFn = sbQuoteSeam(ctr, func(string) float64 { return 42.5 })
		var (
			rows []map[string]interface{}
			code int
		)
		if tc.watch {
			code, rows, _ = sbGetWatchlistRows(t, s, admin)
		} else {
			code, rows, _ = sbGetRows(t, s, admin, tc.path)
		}
		if code != http.StatusOK {
			t.Fatalf("%s 期望 200，got %d", tc.name, code)
		}
		if n := ctr.count(); n != 1 {
			t.Fatalf("%s 的批量上游调用次数=%d，期望 1（逐票时代＝150 次）", tc.name, n)
		}
		if got := len(ctr.calls[0]); got != 150 {
			t.Fatalf("%s 单次批量携带代码数=%d，期望 150（全部未命中代码合并成一批）", tc.name, got)
		}
		if len(rows) == 0 {
			t.Fatalf("%s 返回空行：批量结果没有回写到展示行", tc.name)
		}
	}
}

// TestSigBatchDisplaySurfaceRowSemanticsDiffer ⑥ 三个面对「取不到价」的取舍必须不同，
// 而且这个不同是刻意的，改代码时最容易顺手统一掉：
//   - /api/snapshot 的行内容**就是行情**，缺价即没有这一行（改前也是 `info == nil → continue`）；
//   - /api/watchlist、/api/evaluations 的行内容是「自选里有这只 / 这只的战法评分」，
//     现价只是附挂字段，缺价必须留行并按缺数渲染（现价 0）。
//
// 夹具：150 只里只有前 20 只拿到价。断言 20 / 150 / 150，并把「留行那两面确实有 0 价行」钉住，
// 否则实现改成「缺价就跳过」时三面读数会一起变 20 而没人察觉。
func TestSigBatchDisplaySurfaceRowSemanticsDiffer(t *testing.T) {
	s, admin, codes := sbWatchServer(t, 150)
	priced := map[string]bool{}
	for _, c := range codes[:20] {
		priced[c] = true
	}
	ctr := &sbCounter{}
	s.batchQuoteFn = sbQuoteSeam(ctr, func(c string) float64 {
		if priced[c] {
			return 66.6
		}
		return 0
	})

	_, snapRows, _ := sbGetRows(t, s, admin, "/api/snapshot?codes="+strings.Join(codes, ","))
	if len(snapRows) != 20 {
		t.Fatalf("/api/snapshot 行数=%d，期望 20（本端的行内容就是行情，缺价沿用改前语义跳过）", len(snapRows))
	}

	_, wlRows, _ := sbGetWatchlistRows(t, s, admin)
	if len(wlRows) != 150 {
		t.Fatalf("/api/watchlist 行数=%d，期望 150（自选里有这只就该有一行，缺价按 0 渲染）", len(wlRows))
	}
	if zero := sbCountZeroPrice(wlRows); zero != 130 {
		t.Fatalf("/api/watchlist 现价 0 的行数=%d，期望 130（缺价必须留行而不是被抹掉）", zero)
	}

	_, evRows, _ := sbGetRows(t, s, admin, "/api/evaluations")
	if len(evRows) != 150 {
		t.Fatalf("/api/evaluations 行数=%d，期望 150（行内容是战法评分，现价只是附挂）", len(evRows))
	}
	if zero := sbCountZeroPrice(evRows); zero != 130 {
		t.Fatalf("/api/evaluations 现价 0 的行数=%d，期望 130", zero)
	}
	if n := ctr.count(); n != 3 {
		t.Fatalf("三个面合计批量调用次数=%d，期望 3（每面各 1 次；逐票时代＝450 次）", n)
	}
}

// sbCountZeroPrice 数出现价为 0（缺数渲染）的行。
func sbCountZeroPrice(rows []map[string]interface{}) int {
	n := 0
	for _, r := range rows {
		if p, ok := r["price"].(float64); ok && p == 0 {
			n++
		}
	}
	return n
}

// sbLogBuf 并发安全的日志捕获缓冲：批量补价跑在独立 goroutine 里，
// 那里也可能 log.Printf，裸 bytes.Buffer 在 -race 下会被判竞态。
type sbLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write 实现 io.Writer（供 log.SetOutput 使用）。
func (l *sbLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// String 返回已捕获的日志全文。
func (l *sbLogBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestSigBatchDisplayReadoutOnEverySurface ⑦ 五个补价面每次响应都必须把过程读数写上日志。
// 这场事故的本体不是「慢」，而是**慢在日志里不可见**：信号条数从 75 涨到 271 时
// 门禁、夜间验收、/health 全绿，只有用户打开信号 tab 才发现。所以光有批量实现不够，
// 得有一行能红的读数把「今天到底打了几次上游」变成天天可比数字。
// 断两维：① 五个面各出现一条「<端点> 现价补齐」（摘掉任意一面的调用点即红）；
// ② 每条读数都写的是「批量请求=1」且恰出现 5 次（某一面涨回逐票＝读数变 150 次即红；
// 某个面被调两次也红，因为这条读数是 per-request 的，重复打点说明接线位置不对）。
func TestSigBatchDisplayReadoutOnEverySurface(t *testing.T) {
	s, admin, codes := sbWatchServer(t, 120)
	ctr := &sbCounter{}
	s.batchQuoteFn = sbQuoteSeam(ctr, func(string) float64 { return 31.0 })

	buf := &sbLogBuf{}
	old := log.Writer()
	log.SetOutput(buf)
	defer log.SetOutput(old)

	for _, p := range []string{
		"/api/signals",
		"/api/snapshot/hot",
		"/api/snapshot?codes=" + strings.Join(codes, ","),
		"/api/evaluations",
		"/api/watchlist",
	} {
		req := adminReq(s, admin, http.MethodGet, p, "")
		if rr := adminDo(s, req); rr.Code != http.StatusOK {
			t.Fatalf("%s 期望 200，got %d", p, rr.Code)
		}
	}
	text := buf.String()
	for _, want := range []string{
		"[server] /api/signals 现价补齐",
		"[server] /api/snapshot/hot 现价补齐",
		"[server] /api/snapshot 现价补齐",
		"[server] /api/evaluations 现价补齐",
		"[server] /api/watchlist 现价补齐",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("日志缺这条补价读数：%s ——五面共用一条格式就是为了「又被逐票打爆」当天可见，"+
				"而不是等用户报故障（已捕获=%.300s）", want, text)
		}
	}
	if n := strings.Count(text, "批量请求=1 "); n != 5 {
		t.Fatalf("「批量请求=1」出现 %d 次，期望恰 5（五个面各一次）：某一面涨回逐票时这个数会变大，"+
			"某一面的调用点被挪进循环时也会出现两次", n)
	}
	if n := ctr.count(); n != 5 {
		t.Fatalf("批量上游实际调用次数=%d，期望 5（每面 1 次）：读数与真实调用数必须同源", n)
	}
}

// TestSigBatchDisplayOneCallPerRequest ① 271 条信号只打一次批量上游，且现价来自批量结果。
func TestSigBatchDisplayOneCallPerRequest(t *testing.T) {
	sigs := sbSignals(271)
	s, admin := sbServer(t, sigs)
	ctr := &sbCounter{}
	s.batchQuoteFn = func(codes []string) (map[string]*data.StockInfo, error) {
		ctr.record(codes)
		out := make(map[string]*data.StockInfo, len(codes))
		for _, c := range codes {
			out[c] = &data.StockInfo{Code: c, Price: 12.34, ChangePct: 3.5}
		}
		return out, nil
	}
	code, rows, elapsed := sbGetSignals(t, s, admin)
	if code != http.StatusOK {
		t.Fatalf("期望 200，got %d", code)
	}
	if n := ctr.count(); n != 1 {
		t.Fatalf("批量上游调用次数=%d，期望 1（逐票时代是 271 次；这就是信号 tab 超时的根因）", n)
	}
	if got := len(ctr.calls[0]); got != 271 {
		t.Fatalf("单次批量携带的代码数=%d，期望 271（全部未命中代码合并成一批）", got)
	}
	if len(rows) != 271 {
		t.Fatalf("返回行数=%d，期望 271", len(rows))
	}
	// 批量价格必须真的落到展示行上（否则「一次调用」可以靠全部返回缺价蒙混过关）。
	p, _ := rows[0]["price"].(float64)
	if p != 12.34 {
		t.Fatalf("现价=%v，期望批量结果 12.34（说明批量补价结果没有回写展示行）", rows[0]["price"])
	}
	if cp, _ := rows[0]["change_pct"].(float64); cp != 3.5 {
		t.Fatalf("涨跌幅=%v，期望 3.5", rows[0]["change_pct"])
	}
	if elapsed > 2*time.Second {
		t.Fatalf("响应耗时=%s，批量补价不该让请求变慢", elapsed)
	}
}

// TestSigBatchDisplaySnapshotOnlyNoUpstream ② 代码都在 5s 快照里时，批量补价一次都不许打。
// 这条是"快照优先"的锁：把它摘掉（先批量后快照）读数立刻变 1，测试即红。
func TestSigBatchDisplaySnapshotOnlyNoUpstream(t *testing.T) {
	sigs := sbSignals(3)
	s, admin := sbServer(t, sigs)
	f := data.NewFetcher(nil, data.NewMarketAPI(), nil)
	snap := &data.MarketSnapshot{Stocks: map[string]*data.StockInfo{}, Time: time.Now()}
	for _, sg := range sigs {
		snap.Stocks[sg.Code] = &data.StockInfo{Code: sg.Code, Price: 9.99, ChangePct: 1.25}
	}
	f.IngestSnapshot(snap)
	s.fetcher = f
	ctr := &sbCounter{}
	s.batchQuoteFn = func(codes []string) (map[string]*data.StockInfo, error) {
		ctr.record(codes)
		return map[string]*data.StockInfo{}, nil
	}
	_, rows, _ := sbGetSignals(t, s, admin)
	if n := ctr.count(); n != 0 {
		t.Fatalf("快照已覆盖全部代码，批量上游调用次数=%d，期望 0", n)
	}
	if len(rows) != 3 {
		t.Fatalf("返回行数=%d，期望 3", len(rows))
	}
	if p, _ := rows[0]["price"].(float64); p != 9.99 {
		t.Fatalf("现价=%v，期望快照价 9.99", rows[0]["price"])
	}
}

// TestSigBatchDisplayFailureKeepsList ③ 批量失败：列表一条不少，现价退回信号触发价。
// 这条锁住的是修复的取舍——修的是耗时与上游洪峰，不是把列表内容改掉；
// 缺价按 filterStaleSignals 的 fail-open 语义必须保留信号。
func TestSigBatchDisplayFailureKeepsList(t *testing.T) {
	sigs := sbSignals(120)
	s, admin := sbServer(t, sigs)
	s.batchQuoteFn = func(codes []string) (map[string]*data.StockInfo, error) {
		return nil, fmt.Errorf("批量行情源整体不可用（测试注入）")
	}
	_, rows, elapsed := sbGetSignals(t, s, admin)
	if len(rows) != 120 {
		t.Fatalf("批量失败时返回行数=%d，期望 120（缺价必须保留信号，不得清空列表）", len(rows))
	}
	if p, _ := rows[0]["price"].(float64); p != 8.88 {
		t.Fatalf("现价=%v，期望退回信号触发价 8.88", rows[0]["price"])
	}
	if elapsed > 2*time.Second {
		t.Fatalf("响应耗时=%s，批量失败不该拖慢请求", elapsed)
	}
}

// TestSigBatchDisplayBoundedWait ④ 批量补价迟到（模拟上游慢到 30s 客户端超时）时，
// 请求必须在有界等待内返回，晚到的结果不采用。
// 这条是"20s 前端超时不再被顶穿"的直接证明：改前这里是逐票 goroutine + wg.Wait()，
// 只要有一只票的上游慢，整条响应就被它拖走，且没有任何总时限。
// 缝用「睡过时限再返回」而不是永久阻塞：时限失效的破坏形态才会真的把响应拖到 6s 现形，
// 而不是让用例自己挂死。
func TestSigBatchDisplayBoundedWait(t *testing.T) {
	sigs := sbSignals(50)
	s, admin := sbServer(t, sigs)
	slow := displayQuoteBatchWait + 3*time.Second
	s.batchQuoteFn = func(codes []string) (map[string]*data.StockInfo, error) {
		time.Sleep(slow) // 比有界等待更晚：主腿必须自己到点收工
		out := make(map[string]*data.StockInfo, len(codes))
		for _, c := range codes {
			out[c] = &data.StockInfo{Code: c, Price: 77.7, ChangePct: 5}
		}
		return out, nil
	}
	_, rows, elapsed := sbGetSignals(t, s, admin)
	if elapsed >= displayQuoteBatchWait+2*time.Second {
		t.Fatalf("响应耗时=%s，超过有界等待 %s ⇒ 批量请求没有兜底时限（迟到的上游把整条响应拖走了）", elapsed, displayQuoteBatchWait)
	}
	if len(rows) != 50 {
		t.Fatalf("超时后返回行数=%d，期望 50（放弃等待只放弃现价，不放弃信号）", len(rows))
	}
	if p, _ := rows[0]["price"].(float64); p != 8.88 {
		t.Fatalf("超时后现价=%v，期望退回信号触发价 8.88（晚到的批量结果不采用）", rows[0]["price"])
	}
}

// sbSoloRun ⑧ 的夹具：120 条信号，批量缝只给 covered(c) 为真的代码报价，
// 逐票补齐缝一律报价 21.00（区别于批量价 20.00，才能把「这一行的价是谁补的」读出来）。
// 返回 (行数, 由补齐腿报价的行数, 补齐腿实际打出去的次数, 本次捕获的读数文本)。
// 两个计数器分开数：本用例判的就是「批量没覆盖时补齐腿补了几次、补到几行」。
func sbSoloRun(t *testing.T, covered func(code string) bool) (int, int, int, string) {
	t.Helper()
	s, admin := sbServer(t, sbSignals(120))
	sctr := &sbCounter{}
	s.batchQuoteFn = func(codes []string) (map[string]*data.StockInfo, error) {
		out := make(map[string]*data.StockInfo, len(codes))
		for _, c := range codes {
			if covered(c) {
				out[c] = &data.StockInfo{Code: c, Price: 20.00, ChangePct: 2.0}
			}
		}
		return out, nil
	}
	s.degradeQuoteFn = func(code string) *data.StockInfo {
		sctr.record([]string{code})
		return &data.StockInfo{Code: code, Price: 21.00, ChangePct: 3.0}
	}
	buf := &sbLogBuf{}
	old := log.Writer()
	log.SetOutput(buf)
	defer log.SetOutput(old)
	_, rows, _ := sbGetSignals(t, s, admin)
	soloRows := 0
	for _, r := range rows {
		if p, _ := r["price"].(float64); p == 21.00 {
			soloRows++
		}
	}
	return len(rows), soloRows, sctr.count(), buf.String()
}

// TestSigBatchDisplaySoloTopUpCapped ⑧ 批量没覆盖的代码交给「逐票补齐腿」，且次数是硬上限。
//
// 这条腿是本批门禁 §2 的 e2e 契约腿（internal/e2e.TestHTTPSignalsChangePct）逼出来的：
// 那个 rig 用 server.New(...) 构造服务、不调 SetCoordinator，行情来自注入了 mock transport 的
// MarketAPI；批量入口只认 dc.BatchQuotes，于是 s.dc==nil 直接返回错误 ⇒ 现价停在触发价 30.00
// 而不是 fixture 的 36.86，契约当场红。现网 main.go:422 是有 SetCoordinator 的，
// 但「批量一片覆盖不住就整片没价」本身是覆盖缺口（北交所自 20260828 断更、停牌、分片部分返回）。
//
// 三个分相缺一个就分辨不出补齐腿是哪种坏法：
//
//	A 批量全覆盖 ⇒ 逐票补齐**一次都不许多打**（无条件补齐＝悄悄退回「代码数×逐票」的洪峰形状）；
//	B 批量覆盖 100 只、缺 20（在上限内）⇒ 恰 20 次、20 行由补齐腿报价，120 行一条不少；
//	C 批量整片失败 ⇒ 恰 displayQuoteSoloMax 次（上限之外一律不补），行数仍 120，
//	  且读数把这条腿单独摊开（逐票补齐=30 补齐命中=30）——哪天批量源覆盖不住，日志当天可见。
func TestSigBatchDisplaySoloTopUpCapped(t *testing.T) {
	// A：批量全覆盖 ⇒ 补齐腿不许动。
	rows, soloRows, soloCalls, _ := sbSoloRun(t, func(string) bool { return true })
	if rows != 120 {
		t.Fatalf("A 相行数=%d，期望 120", rows)
	}
	if soloCalls != 0 {
		t.Fatalf("A 相逐票补齐次数=%d，期望 0：批量已全覆盖还逐票打上游＝补齐腿无条件跑，就是本批红线", soloCalls)
	}
	if soloRows != 0 {
		t.Fatalf("A 相由补齐腿报价的行数=%d，期望 0", soloRows)
	}

	// B：批量只覆盖前 100 只 ⇒ 缺的 20 只在上限内，恰补 20 次。
	priced := map[string]bool{}
	for i := 0; i < 100; i++ {
		priced[fmt.Sprintf("%06d", 100000+i)] = true
	}
	rows, soloRows, soloCalls, _ = sbSoloRun(t, func(c string) bool { return priced[c] })
	if rows != 120 {
		t.Fatalf("B 相行数=%d，期望 120（缺价不撤行）", rows)
	}
	if soloCalls != 20 {
		t.Fatalf("B 相逐票补齐次数=%d，期望 20（批量漏 20 只，上限内应逐只补回）", soloCalls)
	}
	if soloRows != 20 {
		t.Fatalf("B 相由补齐腿报价的行数=%d，期望 20（补齐结果没回写展示行）", soloRows)
	}

	// C：批量整片失败 ⇒ 补到上限即止，读数里看得见。
	rows, soloRows, soloCalls, text := sbSoloRun(t, func(string) bool { return false })
	if rows != 120 {
		t.Fatalf("C 相行数=%d，期望 120（批量全灭也只丢现价，不丢行）", rows)
	}
	if soloCalls != displayQuoteSoloMax {
		t.Fatalf("C 相逐票补齐次数=%d，期望上限 %d：次数没被夹住＝120 只逐票打上游，正是拖死信号 tab 的那个乘积",
			soloCalls, displayQuoteSoloMax)
	}
	if soloRows != displayQuoteSoloMax {
		t.Fatalf("C 相由补齐腿报价的行数=%d，期望 %d", soloRows, displayQuoteSoloMax)
	}
	want := fmt.Sprintf("批量请求=1 批量命中=0 逐票补齐=%d 补齐命中=%d", displayQuoteSoloMax, displayQuoteSoloMax)
	if !strings.Contains(text, want) {
		t.Fatalf("读数里看不见补齐腿（缺「%s」）：批量覆盖不住时这条腿是唯一还在给价的来源，"+
			"不摊开计数就等于又是一次「慢/缺在日志里不可见」的事故（已捕获=%.300s）", want, text)
	}
}

// TestSigBatchDisplayWarmsFetcherPool ⑧（§BUG-SIGPRICE，2026-10-27 现网实测）。
//
// 现网形态：205 个信号代码里快照只命中 28 个（池里只有买入信号），批量 177 个一次请求 >3s
// 被放弃 ⇒ 页面上大片「—」而 K 线/五档都正常（那两条走的是别的接口）。
// 处置不是"每次轮询重打 177 只"，而是**把被展示的代码钉进快照池**（EnsureStock，零额外请求）：
// 下一次 5s 刷新后这些代码就进快照，后续轮询零网络命中。
// 本用例锁两件事：① 本轮仍缺价的代码必须全部进池；② 读数里要能看见"预热了几个"。
func TestSigBatchDisplayWarmsFetcherPool(t *testing.T) {
	sigs := sbSignals(5)
	s, admin := sbServer(t, sigs)
	f := data.NewFetcher(nil, data.NewMarketAPI(), nil)
	f.IngestSnapshot(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{}, Time: time.Now()})
	s.fetcher = f
	// 批量一个价都不给：模拟上游超时/部分失败。
	s.batchQuoteFn = func(codes []string) (map[string]*data.StockInfo, error) {
		return map[string]*data.StockInfo{}, nil
	}
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)
	_, rows, _ := sbGetSignals(t, s, admin)
	if len(rows) != 5 {
		t.Fatalf("缺价不得清空列表：返回行数=%d，期望 5", len(rows))
	}
	// 池里可能带交易所后缀（600000.SH），这里按裸 6 位比较，避免把归一形态写成断言。
	bareOf := func(c string) string {
		c = strings.TrimSpace(c)
		if i := strings.IndexByte(c, '.'); i > 0 {
			return c[:i]
		}
		if len(c) > 6 {
			return c[len(c)-6:]
		}
		return c
	}
	watched := map[string]bool{}
	for _, c := range f.WatchCodes() {
		watched[bareOf(c)] = true
	}
	missing := 0
	for _, sg := range sigs {
		if !watched[sg.Code] {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("有 %d 个缺价代码没进快照池（下一次刷新仍会显示「—」）：池=%v", missing, f.WatchCodes())
	}
	if !strings.Contains(logBuf.String(), "池预热=") {
		t.Fatalf("现价补齐读数里没有「池预热=N」，这轮为什么一片「—」、下一轮会不会好就没人知道：%s", logBuf.String())
	}
}
