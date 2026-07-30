// Package data — 5 秒轮询数据采集器。
// 启动独立协程定时拉取自选股行情和板块数据，以 MarketSnapshot 形式提供最新快照。
package data

import (
	"log"
	"sync"
	"time"
)

// MarketSnapshot 全量行情快照，包含个股行情和板块列表。
type MarketSnapshot struct {
	Stocks map[string]*StockInfo // 个股行情，key 为股票代码
	Sector []SectorInfo          // 板块行情列表
	Time   time.Time             // 快照时间戳
	Source string                // 数据来源名称（如 "Tushare"/"EastMoney"）
}

const maxWatchStocks = 60 // 热点监控股票上限

// Fetcher 5 秒轮询行情采集器。
// 非交易时段应调用 FetchOnce() 手动触发，避免无效轮询。
type Fetcher struct {
	dc         *DataCoordinator
	mu         sync.RWMutex
	snapshot   *MarketSnapshot
	baseStocks []string // 自选+持仓，无上限
	hotStocks  []string // 热点板块个股，上限 60，随板块替换
	stopCh     chan struct{}
}

// SetBaseStocks 设置自选+持仓监控列表（无上限）。
// 这些股票始终在监控池中，不受热点轮换影响。
func (f *Fetcher) SetBaseStocks(stocks []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.baseStocks = stocks
}

// UpdateHotStocks 替换热点监控股票列表（上限 maxWatchStocks=60）。
// 热点股票随板块扫描周期替换，旧热点股票被移除。
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

// allStocks 返回去重合并后的完整监控列表（base + hot）。
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

// NewFetcher 创建行情采集器。
// stocks 为初始基础监控列表（自选），dc 为数据协调器（管理多源切换与熔断）。
func NewFetcher(stocks []string, dc *DataCoordinator) *Fetcher {
	ch := make(chan struct{})
	close(ch) // 初始为"已停止"状态，Running() 返回 false
	return &Fetcher{
		dc:         dc,
		baseStocks: stocks,
		stopCh:     ch,
	}
}

// Start 启动后台轮询协程（5 秒间隔）。
func (f *Fetcher) Start() {
	f.mu.Lock()
	f.stopCh = make(chan struct{})
	f.mu.Unlock()
	go f.loop()
}

// Stop 停止后台轮询协程，关闭 stopCh。
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
func (f *Fetcher) FetchOnce() {
	f.fetch()
}

// Snapshot 返回当前最新行情快照。
func (f *Fetcher) Snapshot() *MarketSnapshot {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.snapshot
}

// HotStocks 返回当前热点股票列表（副本）。
func (f *Fetcher) HotStocks() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, len(f.hotStocks))
	copy(out, f.hotStocks)
	return out
}

// StockCount 返回当前监控的股票总数（base + hot 去重后）。
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

// fetch 执行一次完整数据拉取：逐一获取每只股票的行情 + 板块列表。
// 结果存入 f.snapshot，供外部通过 Snapshot() 读取。
func (f *Fetcher) fetch() {
	snapshot := &MarketSnapshot{
		Stocks: make(map[string]*StockInfo),
		Time:   time.Now(),
		Source: f.dc.SourceName(),
	}

	all := f.allStocks()
	for _, code := range all {
		si, err := f.dc.GetQuote(code)
		if err != nil {
			log.Printf("获取 %s 失败: %v", code, err)
			continue
		}
		snapshot.Stocks[code] = si
	}

	sectors, err := f.dc.GetSectors()
	if err == nil {
		snapshot.Sector = sectors
	}

	log.Printf("数据采集: %d/%d 只 %d 板块 [%s]", len(snapshot.Stocks), len(all), len(snapshot.Sector), snapshot.Source)

	f.mu.Lock()
	f.snapshot = snapshot
	f.mu.Unlock()
}
