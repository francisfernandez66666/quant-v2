// Package data — 5 秒轮询数据采集器。
// 启动独立协程定时拉取自选股行情和板块数据，以 MarketSnapshot 形式提供最新快照。
// Package data — a 5s polling data collector.
// It runs a background goroutine fetching watchlist quotes and sector data,
// exposing the latest state as a MarketSnapshot.
package data

import (
	"log"
	"sync"
	"time"
)

// MarketSnapshot 全量行情快照，包含个股行情和板块列表。
// MarketSnapshot is a full market snapshot with stock quotes and sector list.
type MarketSnapshot struct {
	Stocks map[string]*StockInfo // 个股行情，key 为股票代码
	Sector []SectorInfo          // 板块行情列表
	Time   time.Time             // 快照时间戳
	Source string                // 数据来源名称（如 "Tushare"/"EastMoney"）
}

const maxWatchStocks = 60 // 热点监控股票上限

// Fetcher 5 秒轮询行情采集器。
// 非交易时段应调用 FetchOnce() 手动触发，避免无效轮询。
// Fetcher is the 5s polling quote collector; call FetchOnce() manually
// outside trading hours to avoid wasteful polling.
type Fetcher struct {
	dc         *DataCoordinator
	api        *MarketAPI // 行情 API（新浪批量优先）
	mu         sync.RWMutex
	snapshot   *MarketSnapshot
	baseStocks []string // 自选+持仓，无上限
	hotStocks  []string // 热点板块个股，上限 60，随板块替换
	stopCh     chan struct{}
}

// allStocks 返回去重合并后的完整监控列表（base + hot）。
// 通过 map 去重，避免同一只股票被重复拉取。
// allStocks returns the deduplicated full watch list (base + hot).
func (f *Fetcher) allStocks() []string {
	set := make(map[string]bool)
	for _, s := range f.baseStocks {
		set[s] = true
	}
	for _, s := range f.hotStocks {
		set[s] = true
	}
	r := make([]string, 0, len(set))
	for s := range set {
		r = append(r, s)
	}
	return r
}

// SetBaseStocks 设置自选+持仓监控列表（无上限）。
// 这些股票始终在监控池中，不受热点轮换影响。
// SetBaseStocks sets the base watch list (watchlist+positions, unlimited).
// These stocks stay in the pool regardless of hot-stock rotation.
func (f *Fetcher) SetBaseStocks(stocks []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.baseStocks = stocks
}

// UpdateHotStocks 替换热点监控股票列表（上限 maxWatchStocks=60）。
// 热点股票随板块扫描周期替换，旧热点股票被移除。
// UpdateHotStocks replaces the hot-stock watch list (capped at 60),
// rotated with each sector scan cycle.
func (f *Fetcher) UpdateHotStocks(stocks []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(stocks) > maxWatchStocks {
		stocks = stocks[:maxWatchStocks]
	}
	f.hotStocks = stocks
}

// EnsureStock 确保某只股票在监控列表中，立即获取其行情并合并到当前快照。
// 用于前端添加自选后立即获得名称/价格，无需等待下一个 scanCycle。
// EnsureStock adds a symbol to the monitor pool and immediately merges its quote
// into the snapshot, letting the UI show name/price without waiting a cycle.
func (f *Fetcher) EnsureStock(code string) {
	f.mu.Lock()
	already := false
	for _, s := range f.baseStocks {
		if s == code {
			already = true
			break
		}
	}
	if !already {
		f.baseStocks = append(f.baseStocks, code)
	}
	f.mu.Unlock()

	si, err := f.dc.GetQuote(code)
	if err != nil {
		log.Printf("EnsureStock(%s): %v", code, err)
		return
	}
	f.mu.Lock()
	if f.snapshot == nil {
		f.snapshot = &MarketSnapshot{Stocks: make(map[string]*StockInfo), Time: time.Now(), Source: f.dc.SourceName()}
	}
	f.snapshot.Stocks[code] = si
	f.mu.Unlock()
}

// NewFetcher 创建行情采集器。
// stocks 为初始基础监控列表（自选），api 为行情 API（新浪批量优先），dc 为数据协调器（同花顺/东财兜底）。
// NewFetcher creates a fetcher with the initial base list (stocks), the quote API
// (Sina-batch first) and the data coordinator (THS/EastMoney fallback).
func NewFetcher(stocks []string, api *MarketAPI, dc *DataCoordinator) *Fetcher {
	ch := make(chan struct{})
	close(ch) // 初始为"已停止"状态，Running() 返回 false
	return &Fetcher{
		api:        api,
		dc:         dc,
		baseStocks: stocks,
		stopCh:     ch,
	}
}

// Start 启动后台轮询协程（5 秒间隔）。
// Start launches the background polling goroutine (5s interval).
func (f *Fetcher) Start() {
	f.mu.Lock()
	f.stopCh = make(chan struct{})
	f.mu.Unlock()
	go f.loop()
}

// Stop 停止后台轮询协程，关闭 stopCh。
// Stop stops the polling goroutine by closing stopCh.
func (f *Fetcher) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.stopCh:
	default:
		close(f.stopCh)
	}
}

// Running 判断采集器是否正在运行。
// Running reports whether the collector is currently running.
func (f *Fetcher) Running() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	select {
	case <-f.stopCh:
		return false
	default:
		return true
	}
}

// FetchOnce 执行一次数据获取（非交易时段用）。
// FetchOnce triggers a single fetch (for non-trading sessions).
func (f *Fetcher) FetchOnce() {
	f.fetch()
}

// Snapshot 返回当前最新行情快照。
// Snapshot returns the latest market snapshot.
func (f *Fetcher) Snapshot() *MarketSnapshot {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.snapshot
}

// HotStocks 返回当前热点股票列表（副本）。
// HotStocks returns a copy of the current hot-stock list.
func (f *Fetcher) HotStocks() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, len(f.hotStocks))
	copy(out, f.hotStocks)
	return out
}

// StockCount 返回当前监控的股票总数（base + hot 去重后）。
// StockCount returns the deduplicated count of monitored stocks (base + hot).
func (f *Fetcher) StockCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	set := make(map[string]bool)
	for _, s := range f.baseStocks {
		set[s] = true
	}
	for _, s := range f.hotStocks {
		set[s] = true
	}
	return len(set)
}

// loop 采集主循环，5 秒间隔定时调用 fetch()。
// 内置 recover 防止单次 panic 导致整个协程退出。
// loop is the main polling loop calling fetch() every 5s, with recover guards
// so a single panic never kills the goroutine.
func (f *Fetcher) loop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("数据采集协程 panic: %v", r)
		}
	}()
	all := f.allStocks()
	log.Printf("数据采集开始, 监控 %d 只股票(自选+持仓%d 热点%d), 来源 %s", len(all), len(f.baseStocks), len(f.hotStocks), f.dc.SourceName())
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-f.stopCh:
			log.Println("数据采集停止")
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("fetch panic: %v", r)
					}
				}()
				f.fetch()
			}()
		}
	}
}

// fetch 执行一次完整数据拉取：新浪批量拉取全池实时行情，未命中的走同花顺→东财兜底；
// 板块列表另拉一次。结果存入 f.snapshot，供外部通过 Snapshot() 读取。
// fetch performs one full data pull: Sina batch quotes for the whole pool, per-symbol
// fallback via THS→EastMoney for misses, plus one sector-list fetch; stores f.snapshot.
func (f *Fetcher) fetch() {
	snapshot := &MarketSnapshot{
		Stocks: make(map[string]*StockInfo),
		Time:   time.Now(),
		Source: f.dc.SourceName(),
	}

	all := f.allStocks()

	// 1. 新浪批量（单次请求全池），满足 新浪→同花顺→东财 降级链且避免单股限流拖慢 5s 循环
	if f.api != nil {
		for code, si := range f.api.GetSinaQuotes(all) {
			if si != nil && si.Price > 0 {
				snapshot.Stocks[code] = si
			}
		}
	}

	// 2. 兜底：未命中的个股走同花顺→东财
	miss := 0
	for _, code := range all {
		if _, ok := snapshot.Stocks[code]; ok {
			continue
		}
		si, err := f.dc.GetQuote(code)
		if err != nil {
			miss++
			log.Printf("获取 %s 失败: %v", code, err)
			continue
		}
		snapshot.Stocks[code] = si
	}

	sectors, err := f.dc.GetSectors()
	if err == nil {
		snapshot.Sector = sectors
	}

	log.Printf("数据采集: %d/%d 只 %d 板块 (兜底%d) [%s]", len(snapshot.Stocks), len(all), len(snapshot.Sector), miss, snapshot.Source)

	f.mu.Lock()
	f.snapshot = snapshot
	f.mu.Unlock()
}
