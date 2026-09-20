// round3_fix_test.go — §R3-2 P0-D1/D2/D3/D4 回归测试：
// thsDeadline 熔断字段加锁、Snapshot 拷贝语义、allStocks 并发读、parseTHSQuote 越界防御。
// 全部在 -race 下运行以锁定并发修复。
// English: R3-2 regression tests — locked THS breaker field, Snapshot copy semantics,
// allStocks concurrent reads, parseTHSQuote bounds guard. Run under -race.
package data

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// roundTripFunc 把函数适配成 http.RoundTripper（测试用最小替身）。
// English: adapts a func to http.RoundTripper for tests.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestTHSBreakerConcurrent §R3-2 P0-D1：thsDeadlines 的读（thsAvailable）与写（tripThs）
// 并发不产生数据竞争（-race 锁定）；且熔断置位后该域 thsAvailable 返回 false。
// §2026-09-20 更新：熔断按操作域隔离，读/写均带 op 参数；此处用非 nil THSClient
// （旧用例传 nil，thsAvailable 恒 false，断言实际没有区分力）。
func TestTHSBreakerConcurrent(t *testing.T) {
	dc := NewDataCoordinator(nil, &THSClient{})
	if !dc.thsAvailable(thsOpQuote) {
		t.Fatalf("未熔断时应可用（ths 非 nil）")
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = dc.thsAvailable(thsOpQuote) }()
		go func() { defer wg.Done(); dc.tripThs(thsOpQuote) }()
		go func() {
			defer wg.Done()
			dc.mu.RLock()
			_ = dc.ths
			dc.mu.RUnlock()
		}()
	}
	wg.Wait()
	if dc.thsAvailable(thsOpQuote) {
		t.Fatalf("tripThs 后 60s 内不应可用")
	}
}

// TestTHSBreakerPerOperationIsolation §修复 THS-BREAKER(20260920)：
// **一个操作域熔断不得影响其他操作域**。这是本次修复的核心不变量——
// 旧实现 5 条能力共用一把闸，分钟线撞上同花顺不支持的周期就会把报价/板块一起挡 60s。
func TestTHSBreakerPerOperationIsolation(t *testing.T) {
	dc := NewDataCoordinator(nil, &THSClient{})

	dc.tripThs(thsOpMinute)
	if dc.thsAvailable(thsOpMinute) {
		t.Fatalf("已熔断的分钟域应不可用")
	}
	for _, op := range []string{thsOpQuote, thsOpKLine, thsOpBoards, thsOpBoardStock} {
		if !dc.thsAvailable(op) {
			t.Fatalf("分钟域熔断后，%s 域不应被连带关闭（熔断必须按域隔离）", op)
		}
	}

	// 反向：报价域熔断也不得影响其余域
	dc.tripThs(thsOpQuote)
	for _, op := range []string{thsOpKLine, thsOpBoards, thsOpBoardStock} {
		if !dc.thsAvailable(op) {
			t.Fatalf("报价域熔断后，%s 域不应被连带关闭", op)
		}
	}
}

// TestTripThsEmptyOpIsNoop：op 为空串必须什么都不做——防止有人把"忘了传域"
// 误用成"熔断全源"。守卫本身是行为断言（空域调用后所有域仍可用）。
func TestTripThsEmptyOpIsNoop(t *testing.T) {
	dc := NewDataCoordinator(nil, &THSClient{})
	dc.tripThs("")
	for _, op := range []string{thsOpQuote, thsOpKLine, thsOpMinute, thsOpBoards, thsOpBoardStock} {
		if !dc.thsAvailable(op) {
			t.Fatalf("tripThs(\"\") 不得熔断任何域，但 %s 被关掉了", op)
		}
	}
}

// TestTHSMinuteUnsupportedPeriodError：不支持的周期必须返回 ErrTHSUnsupportedPeriod
// 哨兵错（调用方据此判定"客户端能力缺失、不熔断"），且**不得**发出任何 HTTP 请求
// （scale 校验在请求之前完成——否则一个 15 分钟请求会先打到同花顺再报错）。
func TestTHSMinuteUnsupportedPeriodError(t *testing.T) {
	tc := NewTHSClient()
	tc.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("不支持的周期不应发出网络请求，却请求了 %s", r.URL.String())
		return nil, nil
	}))
	for _, scale := range []int{2, 15, 20, 45, 120} {
		kl, err := tc.GetTHSMinuteKLine("600519", scale)
		if err == nil || kl != nil {
			t.Fatalf("scale=%d 应报不支持错误, got kl=%v err=%v", scale, kl, err)
		}
		if !errors.Is(err, ErrTHSUnsupportedPeriod) {
			t.Fatalf("scale=%d 错误应可用 errors.Is 识别为 ErrTHSUnsupportedPeriod, got %v", scale, err)
		}
	}
}

// TestFetcherSnapshotCopy §R3-2 P0-D2：Snapshot 必须返回拷贝——外部对返回 map 的增删
// 不得穿透内部快照；并发 读 Snapshot × 写内部 map 在 -race 下无竞争。
func TestFetcherSnapshotCopy(t *testing.T) {
	f := &Fetcher{}
	f.snapshot = &MarketSnapshot{Stocks: map[string]*StockInfo{"600000.SH": {Code: "600000.SH", Price: 10}}, Time: time.Now()}

	cp := f.Snapshot()
	cp.Stocks["NEW"] = &StockInfo{Price: 1}
	delete(cp.Stocks, "600000.SH")

	got := f.Snapshot()
	if _, ok := got.Stocks["NEW"]; ok {
		t.Fatalf("外部写入不应穿透到内部快照")
	}
	if _, ok := got.Stocks["600000.SH"]; !ok {
		t.Fatalf("外部删除不应影响内部快照")
	}

	// 并发面：读者循环 Snapshot/allStocks，写者模拟 EnsureStock/fetch 的锁内写（-race 锁定）
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = f.Snapshot()
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			f.mu.Lock()
			if f.snapshot == nil {
				f.snapshot = &MarketSnapshot{Stocks: map[string]*StockInfo{}}
			}
			f.snapshot.Stocks["000001.SZ"] = &StockInfo{Code: "000001.SZ", Price: float64(i)}
			f.mu.Unlock()
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = f.allStocks()
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestFetcherAllStocksConcurrent §R3-2 P0-D3：allStocks 与 SetBaseStocks/UpdateHotStocks
// 并发（-race 锁定切片头读写）。
func TestFetcherAllStocksConcurrent(t *testing.T) {
	f := &Fetcher{baseStocks: []string{"600000.SH"}}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				f.SetBaseStocks([]string{"600000.SH", "000001.SZ"[:6] + string(rune('a'+i%26))})
				f.UpdateHotStocks([]string{"300001.SZ"})
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = f.allStocks()
				b, h := f.watchCounts()
				_, _ = b, h
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestParseTHSQuoteShortCode §R3-2 P0-D4：上游代码字段不完整/脏时必须跳过并报错，
// 不得触发按长度切片的越界 panic（旧实现在 c[len(c)-6:] 直接 slice bounds out of range）。
// §2026-09-20 更新：解析器改为扁平字典 + strings.HasSuffix 交叉校验后已无长度切片，
// 但"脏代码必须拒绝"这条不变量仍然保留（含短码、空码、纯垃圾）。
func TestParseTHSQuoteShortCode(t *testing.T) {
	for _, bad := range []string{`"60"`, `""`, `"hs_1.60"`, `"abcdef"`} {
		body := []byte(`({"items":{"5":` + bad + `,"10":"1720"}})`)
		si, err := parseTHSQuote(body, "600000")
		if err == nil || si != nil {
			t.Fatalf("脏 code %s 应被拒绝并报错, got si=%+v err=%v", bad, si, err)
		}
	}
}

// TestParseTHSQuoteNormalStillWorks D4 防回归对照：正常 code 路径不受影响。
func TestParseTHSQuoteNormalStillWorks(t *testing.T) {
	body := []byte(`({"items":{"5":"600519","6":"1666.00","7":"1690.00","8":"1750.00","9":"1685.00","10":"1720.00","13":"1234500","19":"678900000.00","199112":"3.24","1968584":"0.098","name":"贵州茅台"}})`)
	si, err := parseTHSQuote(body, "600519")
	if err != nil || si == nil {
		t.Fatalf("正常 code 应解析成功: %v %+v", err, si)
	}
	if si.Price != 1720 {
		t.Fatalf("价格应取字段 10: %v", si.Price)
	}
	if si.PrevClose != 1666 || si.High != 1750 || si.Low != 1685 {
		t.Fatalf("昨收/高/低错误: %v/%v/%v", si.PrevClose, si.High, si.Low)
	}
	if si.Turnover != 0.098 {
		t.Fatalf("换手率应取字段 1968584: %v", si.Turnover)
	}
}
