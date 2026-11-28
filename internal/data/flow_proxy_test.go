// flow_proxy_test.go — §FLOW-MONEY 盒子中继客户端（决策机侧）行为锁（2026-11-19）。
//
// 这一层是资金流唯一的取数入口（广州对东财被 RST），错法只有三种，三种都要锁：
//
//	① 鉴权头丢了 ⇒ 网关 401，整天弃权（现场最容易被当成"东财又挂了"）；
//	② 信封解错 ⇒ upstream_body 里明明是完整数据，却被读成空（弃权但看不出来为什么）；
//	③ 板块列表分页截断 ⇒ 一半板块没有 delta，"某些票永远不参与板块加权"且无报错。
//
// 因此本文件按"真实 HTTP"验，而不是只测解析函数：鉴权头、状态码、分页循环都在线上路径里。
package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 一条真实形状的 fflow 上游 body（字段序与生产一致：日期 + 五档净额）。
const testFFlowBody = `{"rc":0,"data":{"code":"000001","market":1,"name":"上证指数","klines":[` +
	`"2026-11-18,-3411456000.0,6336782336.0,-2925322240.0,-3832389632.0,420933632.0",` +
	`"2026-11-19,-11811803136.0,14767779840.0,-2955976704.0,-6693154816.0,-5118648320.0"]}}`

// testFlowServer 起一个假网关：记录收到的请求，按 handler 回包。
func testFlowServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func okEnvelope(body string) string {
	payload := map[string]any{
		"ok":              true,
		"upstream_status": 200,
		"upstream_body":   body,
		"src":             "eastmoney_fflow",
		"cache":           "miss",
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func TestFlowProxySendsBearerAndUnwrapsBody(t *testing.T) {
	srv, seen := testFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flow/money" {
			t.Errorf("路径应为 /flow/money, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-123" {
			t.Errorf("鉴权头必须带上（网关 401 的现场最难认）, got %q", got)
		}
		if q := r.URL.Query(); q.Get("kind") != "fflow" || q.Get("secid") != "1.000001" {
			t.Errorf("查询参数应原样转发, got %v", r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okEnvelope(testFFlowBody)))
	})
	p := NewFlowProxy(srv.URL, "tok-123")
	if p == nil {
		t.Fatal("地址与钥匙齐备时必须构造出客户端")
	}
	fd, err := p.IndexMoneyFlowToday()
	if err != nil {
		t.Fatalf("取当日资金流失败: %v", err)
	}
	// 当日腿取分时序列**末根**：与 MarketAPI.IndexMoneyFlowToday 同口径。
	if !strings.HasPrefix(fd.Date, "2026-11-19") {
		t.Fatalf("应取末根（最新一日）, got %q", fd.Date)
	}
	if fd.MainNet != -11811803136.0 {
		t.Fatalf("主力净额应逐列解析, got %v", fd.MainNet)
	}
	if len(*seen) != 1 {
		t.Fatalf("应只打一次上游, got %d", len(*seen))
	}
}

func TestFlowProxyErrorShapes(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"网关非200", 502, `{"ok":false,"err":"upstream unavailable"}`, "status 502"},
		{"信封ok=false", 200, `{"ok":false,"upstream_status":500,"err":"boom"}`, "upstream not ok"},
		{"上游体为空", 200, `{"ok":true,"upstream_body":"   "}`, "empty upstream body"},
		{"信封不是JSON", 200, `not-json`, "decode envelope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := testFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			p := NewFlowProxy(srv.URL, "tok")
			if _, err := p.IndexMoneyFlowToday(); err == nil {
				t.Fatal("这条形态必须返回 error（失败即弃权，绝不拿 0 冒充资金流）")
			} else if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("错误应指明成因 %q, got %v", tc.wantSub, err)
			}
		})
	}
}

// TestFlowProxyClassifiesUpstreamNoData 上游 rc=100/102（退市/长期停牌）必须归成 ErrFlowNoData。
//
// 现场（2026-11-28 首采）：盒子把 rc=100 包成 HTTP 502，与真中继故障同形；
// 连号退市票被逐只记成"中继失败"，攒到 20 只就把整轮 seed 中止在 3.6% 覆盖。
// 这条锁钉的是"结构性没有 ≠ 抖动"这个分界。
func TestFlowProxyClassifiesUpstreamNoData(t *testing.T) {
	noData := `{"ok":false,"upstream_status":200,"upstream_body":"{\"rc\":100,\"data\":null}","src":"eastmoney_fflow"}`
	srv, _ := testFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(noData))
	})
	p := NewFlowProxy(srv.URL, "tok")
	if _, err := p.IndexMoneyFlow("0.000003", 101, 0); !errors.Is(err, ErrFlowNoData) {
		t.Fatalf("rc=100 必须归成 ErrFlowNoData, got %v", err)
	}

	// 真中继故障（上游不可达，没有 rc）不许被误判成 no_data——否则这些票会被永久记进 state。
	relayErr := `{"ok":false,"err":"upstream unavailable"}`
	srv2, _ := testFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(relayErr))
	})
	p2 := NewFlowProxy(srv2.URL, "tok")
	if _, err := p2.IndexMoneyFlow("0.000003", 101, 0); err == nil || errors.Is(err, ErrFlowNoData) {
		t.Fatalf("真中继故障不许归成 no_data, got %v", err)
	}

	// 200 + ok=false + rc=100 同一判定（盒子不同版本可能用 200 回 ok=false）。
	srv3, _ := testFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(noData))
	})
	p3 := NewFlowProxy(srv3.URL, "tok")
	if _, err := p3.IndexMoneyFlow("0.000003", 101, 0); !errors.Is(err, ErrFlowNoData) {
		t.Fatalf("ok=false + rc=100 也必须归成 ErrFlowNoData, got %v", err)
	}
}

func TestFlowProxyNilAndEmptyConfig(t *testing.T) {
	// 地址或钥匙缺一 ⇒ 返回 nil（调用方据此保持直连腿），绝不构造一个必然 401 的客户端。
	if NewFlowProxy("", "tok") != nil {
		t.Fatal("缺地址应返回 nil")
	}
	if NewFlowProxy("http://127.0.0.1:1", "  ") != nil {
		t.Fatal("缺钥匙应返回 nil")
	}
	var p *FlowProxy
	if _, err := p.IndexMoneyFlowToday(); err == nil {
		t.Fatal("nil 客户端必须返回 error 而不是 panic")
	}
	if _, err := p.SectorList(""); err == nil {
		t.Fatal("nil 客户端必须返回 error 而不是 panic")
	}
}

func TestFlowProxySectorListPagesUntilShort(t *testing.T) {
	// 上游 pz 有 100 硬顶（2026-11-19 盒子实测 pz=500 仍只回 100）⇒ 真形状是
	// 5 页 × 100 + 末页 3 = 503。这里按真形状造桩：任何一页按 pz 参数返回对应条数。
	const total = 503
	pageBody := func(pn int) string {
		start := (pn - 1) * emMirrorMaxPageSize
		n := total - start
		if n > emMirrorMaxPageSize {
			n = emMirrorMaxPageSize
		}
		var b strings.Builder
		b.WriteString(`{"rc":0,"data":{"total":` + fmt.Sprintf("%d", total) + `,"diff":{`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			code := itoa4(start + i)
			b.WriteString(`"` + code + `":{"f12":"BK` + code + `","f14":"行业` + code + `","f6":1.0e9,"f62":1.0e7}`)
		}
		b.WriteString(`}}}`)
		return b.String()
	}

	srv, seen := testFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("pz") != "100" {
			t.Errorf("页大小必须按 100 请求（上游硬顶），got pz=%s", q.Get("pz"))
		}
		pn := 0
		if _, err := fmt.Sscanf(q.Get("pn"), "%d", &pn); err != nil || pn < 1 || pn > 6 {
			t.Errorf("不应翻到第 %s 页", q.Get("pn"))
			return
		}
		_, _ = w.Write([]byte(okEnvelope(pageBody(pn))))
	})
	p := NewFlowProxy(srv.URL, "tok")
	list, err := p.SectorList("")
	if err != nil {
		t.Fatalf("分页取全失败: %v", err)
	}
	if len(list) != total {
		t.Fatalf("应取全 %d 个板块, got %d", total, len(list))
	}
	if len(*seen) != 6 {
		t.Fatalf("应恰好翻 6 页（5 满 + 1 短）, got %d", len(*seen))
	}
}

func TestFlowProxySectorListRefusesPartial(t *testing.T) {
	// 上游 total=900 但只给了一页不满的数据 ⇒ 必须报错，绝不把部分结果当全量返回。
	body := `{"rc":0,"data":{"total":900,"diff":{"0":{"f12":"BK0420","f14":"汽车","f6":1.0e9,"f62":1.0e7}}}}`
	srv, _ := testFlowServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(okEnvelope(body)))
	})
	p := NewFlowProxy(srv.URL, "tok")
	if _, err := p.SectorList(""); err == nil {
		t.Fatal("截断的板块列表必须报错（静默截断正是当初要防的形态）")
	}
}

// itoa4 四位左补零（板块代码形如 BK0420）。
func itoa4(n int) string {
	s := []byte("0000")
	for i := 3; i >= 0; i-- {
		s[i] = byte('0' + n%10)
		n /= 10
	}
	return string(s)
}
