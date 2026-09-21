// 文件：source_spof_20260922_test.go
// 职责：§LOW(SPOF) 三方法降级链补齐的反例锁。
//
//	现场核实的三个「单上游、失败即整链无兜底」方法：
//	  ① MarketAPI.GetIndexData —— 东财 push2 唯一上游（原注释自认"无第二源，需补第二源"），
//	     旧行为：网络一断直接回 error，指数点位/涨跌家数整段空白；
//	     新行为：成功轮写 last-known-good；失败轮在 indexLKGMaxAge 窗口内回退上一份真实值并显式告警，
//	     无缓存或超窗仍回 error（不编造）。
//	  ② DataCoordinator.GetSectors —— 同花顺+东财双源皆败时旧行为直接报错；
//	     新行为：回退过期板块缓存（真实但陈旧）+ 告警；无缓存仍报错。
//	  ③ DataCoordinator.GetSectorStocks —— 同上，按板块码回退过期成分股缓存；无缓存透传错误。
//	（GetStockMoneyFlow 经 §ENH-2 已有同花顺官方第二源，不属于本批 SPOF，见修复报告。）
package data

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// spofOK 返回固定 JSON 体的传输层。
func spofOK(body string) http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}

// spofDead 恒错的传输层（模拟上游整体断链）。
func spofDead() http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: 模拟单点上游断链")
	})
}

// TestGetIndexDataLastKnownGoodFallback §LOW(SPOF)①：东财指数断链时回退 last-known-good。
func TestGetIndexDataLastKnownGoodFallback(t *testing.T) {
	DisableAll = true
	defer func() { DisableAll = false }()

	api := NewMarketAPI()
	// 成功轮：stock/get 返回 f43=312500（分）→ 3125.00 点；K线/ Breadth 子请求失败不阻断主体。
	api.SetTransport(spofOK(`{"data":{"f43":312500}}`))
	idx, _, up, down, err := api.GetIndexData()
	if err != nil || idx != 3125.0 {
		t.Fatalf("成功轮应取回指数点位，得到 idx=%v err=%v", idx, err)
	}
	if up <= 0 || down <= 0 {
		t.Fatalf("涨跌家数应有值（真实或中性回退），得到 %d/%d", up, down)
	}

	// 失败轮（窗口内）：回退上一份真实值，而非错误——旧实现此处直接 error（SPOF 无兜底）。
	api.SetTransport(spofDead())
	idx2, _, _, _, err2 := api.GetIndexData()
	if err2 != nil || idx2 != 3125.0 {
		t.Fatalf("§LOW(SPOF) 反例复现：断链轮未走 last-known-good 兜底 (idx=%v err=%v)", idx2, err2)
	}

	// 超窗回退禁用：把 LKG 时间拨老到窗口之外 → 必须重新报错（陈旧到无参考价值的不得冒充）。
	api.indexLKGMu.Lock()
	if api.indexLKG == nil {
		api.indexLKGMu.Unlock()
		t.Fatal("前置：LKG 应已写入")
	}
	api.indexLKG.at = time.Now().Add(-indexLKGMaxAge - time.Minute)
	api.indexLKGMu.Unlock()
	if _, _, _, _, err3 := api.GetIndexData(); err3 == nil {
		t.Fatal("超出陈旧窗口的 LKG 不得继续冒充有效数据")
	}

	// 从未成功过的实例：失败轮老老实实回 error（不编造）。
	fresh := NewMarketAPI()
	fresh.SetTransport(spofDead())
	if _, _, _, _, err4 := fresh.GetIndexData(); err4 == nil {
		t.Fatal("无基线时断链必须回 error")
	}
}

// TestGetSectorsStaleCacheFallback §LOW(SPOF)②：板块双源皆败回退过期缓存，无缓存仍报错。
func TestGetSectorsStaleCacheFallback(t *testing.T) {
	DisableAll = true
	defer func() { DisableAll = false }()

	api := NewMarketAPI()
	api.SetTransport(spofDead())
	dc := NewDataCoordinator(api, nil) // ths=nil：同花顺路自动跳过

	// 无缓存：双源皆败必须显式报错（不编造空表当成功）。
	if _, err := dc.GetSectors(); err == nil {
		t.Fatal("无任何缓存时板块双源皆败应回 error")
	}

	// 有（过期）缓存：回退 last-known-good 且带数据。
	dc.mu.Lock()
	dc.sectorCache = []SectorInfo{{Code: "BK0001", Name: "测试板块"}}
	dc.sectorCacheAt = time.Now().Add(-10 * time.Minute) // 早已超过 30s TTL
	dc.mu.Unlock()
	s, err := dc.GetSectors()
	if err != nil || len(s) != 1 || s[0].Code != "BK0001" {
		t.Fatalf("§LOW(SPOF) 反例复现：双源皆败未回退过期板块缓存 (n=%d err=%v)", len(s), err)
	}
}

// TestGetSectorStocksStaleCacheFallback §LOW(SPOF)③：成分股双源皆败按板块码回退过期缓存。
func TestGetSectorStocksStaleCacheFallback(t *testing.T) {
	DisableAll = true
	defer func() { DisableAll = false }()

	api := NewMarketAPI()
	api.SetTransport(spofDead())
	dc := NewDataCoordinator(api, nil)

	// 无该板块历史：透传错误。
	if _, err := dc.GetSectorStocks("BK0002", 10); err == nil {
		t.Fatal("无缓存时成分股双源皆败应回 error")
	}

	dc.mu.Lock()
	dc.sectorStockCache["BK0002"] = cachedSectorStocks{
		stocks: []StockInfo{{Code: "600000", Name: "浦发银行"}},
		at:     time.Now().Add(-30 * time.Minute), // 超过 60s TTL 的过期缓存
	}
	dc.mu.Unlock()
	out, err := dc.GetSectorStocks("BK0002", 10)
	if err != nil || len(out) != 1 || out[0].Code != "600000" {
		t.Fatalf("§LOW(SPOF) 反例复现：成分股双源皆败未回退过期缓存 (n=%d err=%v)", len(out), err)
	}
}
