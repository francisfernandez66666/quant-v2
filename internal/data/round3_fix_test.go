// round3_fix_test.go — §R3-2 P0-D1/D2/D3/D4 回归测试：
// thsDeadline 熔断字段加锁、Snapshot 拷贝语义、allStocks 并发读、parseTHSQuote 越界防御。
// 全部在 -race 下运行以锁定并发修复。
// English: R3-2 regression tests — locked THS breaker field, Snapshot copy semantics,
// allStocks concurrent reads, parseTHSQuote bounds guard. Run under -race.
package data

import (
	"sync"
	"testing"
	"time"
)

// TestTHSBreakerConcurrent §R3-2 P0-D1：thsDeadline 的读（thsAvailable）与写（tripThs）
// 并发不产生数据竞争（-race 锁定）；且熔断置位后 thsAvailable 返回 false。
func TestTHSBreakerConcurrent(t *testing.T) {
	dc := NewDataCoordinator(nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = dc.thsAvailable() }()
		go func() { defer wg.Done(); dc.tripThs() }()
		go func() {
			defer wg.Done()
			dc.mu.RLock()
			_ = dc.ths
			dc.mu.RUnlock()
		}()
	}
	wg.Wait()
	if dc.thsAvailable() {
		t.Fatalf("tripThs 后 60s 内不应可用")
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

// TestParseTHSQuoteShortCode §R3-2 P0-D4：上游 item code 清理后不足 6 位时必须跳过，
// 不得触发 c[len(c)-6:] 越界 panic（旧实现在此直接 slice bounds out of range）。
func TestParseTHSQuoteShortCode(t *testing.T) {
	// hs_1.60 位于索引1（code 槽位）→ 清理后 "60"，长度 <6；脏数据应被整体跳过并报"no data"
	body := []byte(`({"data":{"items":{"1":["x","hs_1.60","名",1,2,3,4,5,6,7]}}})`)
	si, err := parseTHSQuote(body, "600000")
	if err == nil || si != nil {
		t.Fatalf("脏 code 应被跳过并报错, got si=%+v err=%v", si, err)
	}
}

// TestParseTHSQuoteNormalStillWorks D4 防回归对照：正常 code 路径不受影响。
func TestParseTHSQuoteNormalStillWorks(t *testing.T) {
	body := []byte(`({"data":{"items":{"1":["x","hs_1.600519","贵州茅台",1700,1750,1690,1720,12345,67890,1710]}}})`)
	si, err := parseTHSQuote(body, "600519")
	if err != nil || si == nil {
		t.Fatalf("正常 code 应解析成功: %v %+v", err, si)
	}
	if si.Price != 1720 {
		t.Fatalf("价格应取索引 6: %v", si.Price)
	}
}
