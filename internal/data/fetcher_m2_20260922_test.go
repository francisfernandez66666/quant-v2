// 文件：fetcher_m2_20260922_test.go
// 职责：§M2「批量抓取全源失败仍替换快照+刷新 lastOK」缺陷的反例锁回归测试。
//
//	锁三条语义（断言均能复现原缺陷）：
//	  ① 四路源（hithink 批量 / 新浪批量 / 腾讯批量 / 单股降级链 同花顺→东财）全部 error 时，
//	     Fetcher 必须保留上一份 last-known-good 快照，且不得刷新 lastOK；
//	  ② Staleness 在失败轮持续累加，越过 60s 阈值时触发 checkStaleAlert 告警路径（节流位前进）；
//	  ③ 从未采集时 Staleness 回 -1s（未知），且未知不触发陈旧告警（防启动期噪音）。
//
// 原缺陷语义：全失败轮照样 `f.snapshot = snapshot`（空快照顶掉好数据）+ `lastOK=now`
// （陈旧度归零），下游 §WS-C 陈旧行情闸被"伪造新鲜"解除——本文件先红后绿钉死。
package data

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// m2FailingTransport 所有 HTTP 请求即刻失败的传输层（模拟新浪/腾讯/东财全断）。
type m2FailingTransport struct{}

func (m2FailingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: 模拟全源断网")
}

// newM2AllFailedFetcher 构造「四源全 error」的采集器：
//   - hithink（新）：注入指向 127.0.0.1:1（TCP 立拒）的官方客户端（dc 与 fetcher 双侧）；
//   - 新浪批量/腾讯批量/东财：MarketAPI transport 替换为恒错实现；
//   - 同花顺（老）：dc.ths=nil（链自动跳过，等价该路无数据）。
func newM2AllFailedFetcher(t *testing.T, codes []string) *Fetcher {
	t.Helper()
	api := NewMarketAPI()
	api.SetTransport(m2FailingTransport{})

	// hithink 新源：临时 env + 死地址基址，让 BatchQuotes 走真实网络错误路径。
	oldBase := HithinkBaseURL
	t.Setenv(HithinkAPIKeyEnv, "m2-test-key")
	HithinkBaseURL = "http://127.0.0.1:1"
	t.Cleanup(func() { HithinkBaseURL = oldBase })
	hk, herr := NewHithinkClient()
	if herr != nil {
		t.Fatalf("构造 hithink 客户端失败: %v", herr)
	}

	dc := NewDataCoordinator(api, nil)
	dc.SetHithink(hk)

	f := NewFetcher(codes, api, dc)
	f.hithink = hk
	f.hithinkState = &HithinkSourceState{}
	return f
}

// TestFetchAllSourcesFailedKeepsLastKnownGood §M2 主反例锁：全源失败轮不改快照、不刷 lastOK。
func TestFetchAllSourcesFailedKeepsLastKnownGood(t *testing.T) {
	codes := []string{"600000", "300750"}
	f := newM2AllFailedFetcher(t, codes)

	// 基线：一份成功采集过的快照（IngestSnapshot 语义=上一轮好数据）。
	base := &MarketSnapshot{
		Stocks: map[string]*StockInfo{
			"600000": {Code: "600000", Price: 10.5},
			"300750": {Code: "300750", Price: 200.5},
		},
		Time:   time.Now().Add(-90 * time.Second),
		Source: QuoteSourceSina,
	}
	f.IngestSnapshot(base)
	// 人为把最近成功时间拨回 90s 前：模拟"上游已断流 90s"，陈旧度应可越过 60s 告警线。
	aged := time.Now().Add(-90 * time.Second).Unix()
	f.lastOK.Store(aged)
	f.lastStaleWarn.Store(0)

	snapBefore := f.Snapshot()
	if snapBefore == nil {
		t.Fatal("基线快照未建立")
	}
	priceBefore := snapBefore.Stocks["600000"].Price
	timeBefore := snapBefore.Time

	// 本轮：四源全 error。
	f.FetchOnce()

	// ① last-known-good 保留：股票条目、价格、快照时间戳全部不变。
	snapAfter := f.Snapshot()
	if snapAfter == nil || len(snapAfter.Stocks) != 2 {
		t.Fatalf("§M2 反例复现：全失败轮快照被替换为 %v（应为上一份 last-known-good）", snapAfter)
	}
	if got := snapAfter.Stocks["600000"].Price; got != priceBefore {
		t.Fatalf("§M2 反例复现：快照价格被改动 %v -> %v", priceBefore, got)
	}
	if !snapAfter.Time.Equal(timeBefore) {
		t.Fatalf("§M2 反例复现：快照时间戳被推进（伪造新鲜）%v -> %v", timeBefore, snapAfter.Time)
	}

	// ② lastOK 不前进（旧实现此处无条件 Store(now)）。
	if cur := f.lastOK.Load(); cur != aged {
		t.Fatalf("§M2 反例复现：全失败轮仍刷新了 lastOK %d -> %d（陈旧度被归零）", aged, cur)
	}

	// ③ Staleness 继续累加越过 60s，且全败分支已触发告警路径（节流时间戳前进）。
	if s := f.Staleness(); s <= 60*time.Second {
		t.Fatalf("陈旧度应 >60s，得到 %v", s)
	}
	if f.lastStaleWarn.Load() == 0 {
		t.Fatal("§M2 反例复现：Staleness>60s 的全失败轮未走 checkStaleAlert 告警路径")
	}
	// 告警必须可复算：checkStaleAlert 在 60s 节流窗内再次调用不重复打点。
	if f.checkStaleAlert() {
		t.Fatal("陈旧告警应按 1 条/分钟节流，节流窗内不应重复触发")
	}
}

// TestFetchAllFailedNoBaselineKeepsNilSnapshot 从未成功采集 + 全败轮：快照保持无（未知），
// 不制造空壳假数据；Staleness 维持 -1（未知），且不触发陈旧告警。
func TestFetchAllFailedNoBaselineKeepsNilSnapshot(t *testing.T) {
	f := newM2AllFailedFetcher(t, []string{"600000"})

	f.FetchOnce()

	if s := f.Snapshot(); s != nil && len(s.Stocks) > 0 {
		t.Fatalf("全败且无基线时不得凭空造快照: %+v", s)
	}
	if f.lastOK.Load() != 0 {
		t.Fatal("全败轮不得刷新 lastOK（无基线时保持从未采集）")
	}
	if st := f.Staleness(); st != -1*time.Second {
		t.Fatalf("从未采集 Staleness 应为 -1s（未知），得到 %v", st)
	}
	if f.checkStaleAlert() {
		t.Fatal("未知（-1）不是陈旧，禁止触发告警（启动期噪音）")
	}
}

// TestStalenessUnknownNegative §M2 语义锁：从未采集回 -1；成功采集后回非负并正常累加。
func TestStalenessUnknownNegative(t *testing.T) {
	f := NewFetcher(nil, &MarketAPI{}, nil)
	if f.Staleness() != -1*time.Second {
		t.Fatalf("从未采集应回 -1s，得到 %v", f.Staleness())
	}
	f.IngestSnapshot(&MarketSnapshot{Stocks: map[string]*StockInfo{"1": {Price: 1}}, Time: time.Now()})
	if f.Staleness() < 0 {
		t.Fatalf("成功采集后 Staleness 应非负，得到 %v", f.Staleness())
	}
	// 拨回 70s：模拟上游断流后陈旧度线性累加。
	f.lastOK.Store(time.Now().Add(-70 * time.Second).Unix())
	if s := f.Staleness(); s < 60*time.Second {
		t.Fatalf("断流 70s 后陈旧度应 ≥60s，得到 %v", s)
	} else if !f.checkStaleAlert() {
		t.Fatal("越线必须触发告警")
	}
}
