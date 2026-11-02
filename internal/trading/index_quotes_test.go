// index_quotes_test.go — §A7-3（2026-11-02）券商侧指数取数的键空间回归锁。
//
// 钉住三件事，全部来自"指数在个股键空间里会撞车"这一族：
//  1. 出向代码**原样透传**（后缀由 data.IndexSuffix 白名单给定，不经过个股后缀函数）；
//  2. 入向 key **不剥后缀**——Quotes 那条路会把 000001.SH 与 000001.SZ 剥成同一个裸码，
//     上证指数与平安银行谁赢取决于回包顺序，那正是 §BUG-INDEX 的形状；
//  3. 白名单外的代码一律不进指数账（请求侧与回包侧都拦）。
package trading

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// tickBody 造一份网关回包（键为带后缀代码；tickTime 用当前毫秒，免得被判超龄）。
func tickBody(entries map[string][2]float64) string {
	var b strings.Builder
	b.WriteString(`{"ok":true,"ticks":{`)
	first := true
	for code, pv := range entries {
		if !first {
			b.WriteString(",")
		}
		first = false
		b.WriteString(fmt.Sprintf(`"%s":{"lastPrice":%.2f,"lastClose":%.2f,"tickTime":%d}`,
			code, pv[0], pv[1], time.Now().UnixMilli()))
	}
	b.WriteString("}}")
	return b.String()
}

// newTestClient 指向 httptest 服务端的客户端（token 随便给，桩不校验）。
func newTestClient(url string) *QMTClient {
	return NewQMTClient(url, "test-token", 3*time.Second, 0)
}

// testCtx 一轮请求的上下文。
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// tickKeys 排序后的键列表（失败信息要能看出"哪一只跑进来了"）。
func tickKeys(m map[string]data.QMTTick) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestIndexQuotesKeepsSuffixedKeys 指数侧收发都保留后缀，且只认白名单。
func TestIndexQuotesKeepsSuffixedKeys(t *testing.T) {
	var gotCodes string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/quotes") {
			http.NotFound(w, r)
			return
		}
		gotCodes = r.URL.Query().Get("codes")
		// 回包里刻意混进一只不在白名单的代码（模拟网关多给了），必须被丢弃
		_, _ = w.Write([]byte(tickBody(map[string][2]float64{
			"000001.SH": {3638.03, 3599.02},
			"000001.SZ": {11.54, 11.64},
			"600519.SH": {1500, 1490},
		})))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	ticks, err := c.IndexQuotes(testCtx(t), []string{"000001.SH", "000001.SZ", "600519.SH", "000001"})
	if err != nil {
		t.Fatalf("IndexQuotes 失败：%v", err)
	}
	// 出向：只应带白名单里的指数代码，且后缀原样
	if strings.Contains(gotCodes, "600519") {
		t.Fatalf("个股代码被带进指数请求（键空间会撞车）：%s", gotCodes)
	}
	for _, part := range strings.Split(gotCodes, ",") {
		if part != "" && !strings.Contains(part, ".") {
			t.Fatalf("裸码被带进指数请求（个股后缀函数会把 000001 变成 .SZ）：%s", gotCodes)
		}
	}
	if !strings.Contains(gotCodes, "000001.SH") {
		t.Fatalf("白名单内的指数代码没原样透传：%s", gotCodes)
	}
	// 同码不同所必须被拒：000001.SZ 是平安银行，不是上证指数。
	// 它进不了请求，也就永远进不了指数账——这条比"键保留后缀"更前一步。
	if strings.Contains(gotCodes, "000001.SZ") {
		t.Fatalf("同码个股（000001.SZ＝平安银行）被当成指数发出去了：%s", gotCodes)
	}
	// 入向：键必须保留后缀——剥掉就意味着上证与平安银行共用一个键
	if _, ok := ticks["000001.SH"]; !ok {
		t.Fatalf("指数 tick 的键被剥了后缀（000001.SH 与 000001.SZ 会撞成一个键）：%v", tickKeys(ticks))
	}
	// 回包里白名单外的键（.SZ 的平安银行、600519 的个股）一律不收：指数账里只该有点位。
	if _, ok := ticks["000001.SZ"]; ok {
		t.Fatalf("同码个股进了指数账：%v", tickKeys(ticks))
	}
	if _, ok := ticks["000001"]; ok {
		t.Fatalf("返回里出现裸码键：说明还在走个股的归一逻辑")
	}
	if _, ok := ticks["600519.SH"]; ok {
		t.Fatalf("白名单外的代码进了指数账：%v", tickKeys(ticks))
	}
	if len(ticks) != 1 {
		t.Fatalf("指数账应只有 000001.SH 一条，实得 %d 条：%v", len(ticks), tickKeys(ticks))
	}
	// 点位口径自证：000001.SH 必须是点位量级，不是十几块的银行股
	if tk := ticks["000001.SH"]; tk.LastPrice < 1000 {
		t.Fatalf("000001.SH 拿到的不是点位（%.2f）", tk.LastPrice)
	}
}

// TestQuotesStillStripsSuffix 反向锁：个股那条路**继续**剥后缀（不能为指数把它改掉，
// 它被下单/风控/涨跌停共用，Fetcher.Stocks 的键口径就是裸码）。
func TestQuotesStillStripsSuffix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(tickBody(map[string][2]float64{"600519.SH": {1500, 1490}})))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	ticks, err := c.Quotes(testCtx(t), []string{"600519"})
	if err != nil {
		t.Fatalf("Quotes 失败：%v", err)
	}
	if _, ok := ticks["600519.SH"]; ok {
		t.Fatalf("个股路径不该变成带后缀的键（Fetcher.Stocks 按裸码索引）")
	}
	if _, ok := ticks["600519"]; !ok {
		t.Fatalf("个股路径没剥后缀，键口径漂了：%v", tickKeys(ticks))
	}
}

// TestIndexQuotesEmptyRequestIsNoop 全被白名单挡掉时不发请求（省一次空往返，也别让网关
// 因为空 codes 回 400 而记成"通道故障"）。
func TestIndexQuotesEmptyRequestIsNoop(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true,"ticks":{}}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	ticks, err := c.IndexQuotes(testCtx(t), []string{"600519", "000001"})
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if calls != 0 {
		t.Fatalf("白名单为空却发了 %d 次请求", calls)
	}
	if len(ticks) != 0 {
		t.Fatalf("不该有读数：%v", tickKeys(ticks))
	}
}
