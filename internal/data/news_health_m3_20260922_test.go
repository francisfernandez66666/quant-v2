// 文件：news_health_m3_20260922_test.go
// 职责：§M3「NewsSourceHealth 零探测纯编造」缺陷的反例锁回归测试。
//
//	原缺陷：状态由 `eastMoney.client != nil` / `dc.ths != nil` 等指针就绪推导——客户端只要
//	构造过就恒真（无任何抓取也报可用），且财联社键名拼错为 "cainanshe"。
//	锁语义：
//	  ① 从未发生过任何一次抓取 → 三源（cailanshe/kuaixun/sina）状态必须全部 "unknown"，
//	     绝不允许 "ok"；指针完全就绪（client != nil）也不得改判。
//	  ② 键名修正锁："cailanshe" 存在、拼错的 "cainanshe" 永绝后迹。
//	  ③ 真实抓取数据驱动状态翻转：成功→ok、失败→down，并携带 最近成功时间/连续错误数/累计错误数。
package data

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestNewsSourceHealthUnknownBeforeAnyProbe §M3 主反例锁：从未拉取 → 每源必为 unknown。
func TestNewsSourceHealthUnknownBeforeAnyProbe(t *testing.T) {
	// 客户端「完全就绪」形态（旧实现会因此整表报 ok）：真实 NewMarketAPI（client 非 nil）。
	api := NewMarketAPI()
	dc := NewDataCoordinator(api, nil)

	h := dc.NewsSourceHealth()
	for _, key := range []string{NewsSourceCLS, NewsSourceTHSFlash, NewsSourceSina} {
		st, ok := h[key]
		if !ok {
			t.Fatalf("§M3 键缺失：%q 不在 NewsSourceHealth 结果中（keys=%v）", key, keysOf(h))
		}
		if st.Status != "unknown" {
			t.Fatalf("§M3 反例复现：%q 从未抓取却报 %q（必须由真实探测数据驱动，零探测=unknown）", key, st.Status)
		}
	}
	// 拼写错误键名锁：修复后 "cainanshe" 不得再出现在对外契约里。
	if _, bad := h["cainanshe"]; bad {
		t.Fatal("§M3 键名漂移：拼错的 cainanshe 仍在对外输出（应为 cailanshe）")
	}
	// 指针就绪 ≠ 健康：显式钉住 client != nil 的事实，反证状态仍必须是 unknown。
	if api.client == nil {
		t.Fatal("测试前置：期望 client 已构造")
	}

	// dc.eastMoney=nil 的极端形态也不得 panic，且全表 unknown。
	dcNil := NewDataCoordinator(nil, nil)
	for k, st := range dcNil.NewsSourceHealth() {
		if st.Status != "unknown" {
			t.Fatalf("nil 客户端场景下 %q 应为 unknown，得到 %q", k, st.Status)
		}
	}
}

// TestNewsSourceHealthDrivenByRealFetches §M3 行为锁：真实抓取的成功/失败驱动状态与计数。
func TestNewsSourceHealthDrivenByRealFetches(t *testing.T) {
	DisableAll = true // 单测不打真实限流节拍
	defer func() { DisableAll = false }()

	api := NewMarketAPI()
	dc := NewDataCoordinator(api, nil)

	// —— 失败轮：THS 快讯返回非 200 业务码 → down + 计数 ——
	thsErrBody := `{"code":"500","message":"boom","data":{"list":[]}}`
	api.SetTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(thsErrBody)),
		}, nil
	}))
	if _, err := api.GetTonghuashunNews(5); err == nil {
		t.Fatal("测试前置：code!=200 应判抓取失败")
	}
	h := dc.NewsSourceHealth()
	if st := h[NewsSourceTHSFlash]; st.Status != "down" || st.ConsecutiveErrors != 1 || st.TotalErrors != 1 || st.LastSuccessAt != "" {
		t.Fatalf("失败抓取后 kuaixun 应为 down/连错1/总错1/无成功时间，得到 %+v", st)
	}
	// 未碰过的其余源保持 unknown（不受本路失败牵连）。
	if h[NewsSourceSina].Status != "unknown" || h[NewsSourceCLS].Status != "unknown" {
		t.Fatal("其余源零探测必须保持 unknown")
	}

	// —— 成功轮：合法空列表 JSON → ok，连续错误清零，保留历史累计 ——
	okBody := `{"code":"200","data":{"list":[]}}`
	api.SetTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(okBody))}, nil
	}))
	if _, err := api.GetTonghuashunNews(5); err != nil {
		t.Fatalf("测试前置：code==200 应判成功: %v", err)
	}
	h = dc.NewsSourceHealth()
	if st := h[NewsSourceTHSFlash]; st.Status != "ok" || st.ConsecutiveErrors != 0 || st.TotalErrors != 1 || st.LastSuccessAt == "" {
		t.Fatalf("成功抓取后 kuaixun 应为 ok/连错0/总错1(历史)/有成功时间，得到 %+v", st)
	}
	if ts, terr := time.Parse(time.RFC3339, h[NewsSourceTHSFlash].LastSuccessAt); terr != nil || ts.IsZero() {
		t.Fatalf("last_success_at 应为 RFC3339 时间，得到 %q", h[NewsSourceTHSFlash].LastSuccessAt)
	}

	// —— 网络级失败（新浪）：HTTP 传输错误同样入账 ——
	api.SetTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: refused")
	}))
	if _, err := api.GetSinaNews(5); err == nil {
		t.Fatal("测试前置：传输错误应判失败")
	}
	if st := dc.NewsSourceHealth()[NewsSourceSina]; st.Status != "down" || st.ConsecutiveErrors != 1 {
		t.Fatalf("sina 传输失败后应为 down/连错1，得到 %+v", st)
	}
}

// keysOf 测试辅助：map 键列表（报错信息用）。
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
