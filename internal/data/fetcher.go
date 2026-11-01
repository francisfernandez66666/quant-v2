// Package data — 5 秒轮询数据采集器。
// 启动独立协程定时拉取自选股行情和板块数据，以 MarketSnapshot 形式提供最新快照。
// §M2（20260922 修复批 G）：整轮行情全源失败时保留 last-known-good 快照、不刷新 lastOK，
// Staleness 持续累加并经 checkStaleAlert 触发告警；从未采集时 Staleness 回 -1（未知）。
// §M1：快照 Source 字面量改引 data 包 QuoteSource* 枚举常量（/api/status 契约单源化）。
// Package data — a 5s polling data collector.
// It runs a background goroutine fetching watchlist quotes and sector data,
// exposing the latest state as a MarketSnapshot.
// §M2: on an all-source-failed round the previous snapshot and lastOK are kept, so
// staleness keeps accumulating and warns; never-fetched staleness is -1 (unknown).
package data

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MarketSnapshot 全量行情快照，包含个股行情和板块列表。
// MarketSnapshot is a full market snapshot with stock quotes and sector list.
type MarketSnapshot struct {
	Stocks map[string]*StockInfo // 个股行情，key 为股票代码
	Sector []SectorInfo          // 板块行情列表
	Time   time.Time             // 快照时间戳
	// Source 数据来源名，取值必须落在 §M1 枚举 data.QuoteSource*（见 AllQuoteSources 与
	// golden qmt_gateway/contract/quote_sources.json）；空串=快照未就绪/盘外，非枚举值。
	// §WS-C 陈旧闸/巡检消费方禁止把空串当任意源名处理。
	Source string
}

// maxWatchStocks 热点监控股票上限。
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
	// hithink 同花顺（新）官方源（最高优先级）：Key 缺失时为 nil，源链自动跳过。
	hithink      *HithinkClient
	hithinkState *HithinkSourceState
	auctionMu    sync.Mutex
	auction      map[string]*HithinkAuctionItem // 最新竞价快照（9:15-9:26 窗口内更新）

	// §GAP3.2 快照落盘：dataDir 非空时，活跃时段每 ~30s 把最新快照原子写 snapshot_latest.json，
	// 同交易日启动时恢复——重启不再丢失当日快照（尤其竞价窗口数据）。
	dataDir string
	// §GAP3.3 盘中延迟监控：最近一次成功采集 unix 秒；Staleness() 供健康检查/告警消费。
	lastOK atomic.Int64
	// §WS-G 快照流接收器：每次成功采集后回调（staging 录制 quote_stream.jsonl 供回放）。
	// English: §WS-G snapshot-stream sink invoked after each successful fetch (staging records JSONL).
	sink func(*MarketSnapshot)
	// 陈旧告警节流（unix 秒）
	lastStaleWarn atomic.Int64
	// 落盘节拍计数
	persistTick int
	// §A+B 行情刷新间隔（秒）：默认 0 → 回退 5s。降低可缩短"行情变化→信号检测"感知延迟。
	refreshInterval time.Duration
	// §SUBSCRIPTION-A（2026-11-01）券商 L1 主源的装配状态（overlay / 覆盖率 / 池分档 / 迟滞）。
	// 单点收口在 quote_feed_hub.go：这里只挂指针，状态与锁都在 hub 内部，
	// 免得又出现"两个写者各自塞快照"那种谁都不知道自己赢了谁的形状。
	// English: broker-feed assembly state (overlay/coverage/pool tier); state and its own lock live
	// in quote_feed_hub.go so this struct only holds the pointer.
	hubOnce sync.Once     // 懒建 hub（不能借 f.mu：UpdateHotStocks/StalenessMs 等都在持 f.mu 时取档）
	hubRef  *feedHubState // 券商 feed 的装配状态（overlay / 覆盖率 / 池分档 / 迟滞）
}

// allStocks 返回去重合并后的完整监控列表（base + hot）。
// 通过 map 去重，避免同一只股票被重复拉取。
// §R3-2 P0-D3 锁内读取：baseStocks/hotStocks 被 SetBaseStocks/UpdateHotStocks 并发写，
// fetch 循环锁外遍历会读到撕裂的切片头。English: R3-2 P0-D3 — read both lists under RLock;
// they are concurrently replaced by SetBaseStocks/UpdateHotStocks.
func (f *Fetcher) allStocks() []string {
	f.mu.RLock()
	base := make([]string, len(f.baseStocks))
	copy(base, f.baseStocks)
	hot := make([]string, len(f.hotStocks))
	copy(hot, f.hotStocks)
	f.mu.RUnlock()
	set := make(map[string]bool)
	for _, s := range base {
		set[s] = true
	}
	for _, s := range hot {
		set[s] = true
	}
	r := make([]string, 0, len(set))
	for s := range set {
		r = append(r, s)
	}
	return r
}

// watchCounts 锁内返回 base/hot 两个监控池的当前长度（日志用）。
// English: watchCounts returns the current base/hot pool sizes under RLock (for logs).
func (f *Fetcher) watchCounts() (base, hot int) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.baseStocks), len(f.hotStocks)
}

// WatchCodes 返回当前完整监控池（base+hot 去重副本），供 §ENH-5 L1 行情 feed 轮询取码。
// English: WatchCodes exposes the deduped base+hot monitoring pool for the L1 quote feed poller.
func (f *Fetcher) WatchCodes() []string {
	return f.allStocks()
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
//
// §SUBSCRIPTION-A（owner 裁决 12）上限改成**按源分档**：券商主源稳定时可用配置值（出厂 400），
// 退回后备源时仍是既有常数 60——新浪是 80 只一片，池越大后备链请求越多（400 只≈每轮 5 个请求）。
// 两条形状锁在这里：
//  1. 截断逻辑不许消失（负锁盯的是"顺手把上限去掉"，那会在降级态把后备链打爆）；
//  2. 被钉住的代码（持仓/在途委托/§132 池预热注入的缺价代码）**必须留在池里**，
//     收池不许把它们踢掉——做 T 的一只票因为池缩而看不见价，是最坏的一种降级。
//
// English: the cap is now tiered by source (broker-steady → configured ceiling, fallback → the
// historical 60). Truncation must never disappear, and pinned codes always survive a shrink.
func (f *Fetcher) UpdateHotStocks(stocks []string) {
	// 先算档（hub() 自己会取 f.mu，绝不能持 f.mu 时调用）
	cap := f.hotPoolCapNow()
	pinned := f.pinnedCodes()

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(stocks) > cap {
		// 保留钉住的代码：先看它们在这轮热点里能不能占位，占不到就直接补进池尾，
		// 保证"截断"永远不会把被引用的票截掉（数量级远小于 cap，不改变池的规模语义）。
		kept := make([]string, 0, cap+len(pinned))
		seen := make(map[string]bool, cap+len(pinned))
		for _, s := range stocks[:cap] {
			kept = append(kept, s)
			seen[s] = true
		}
		for _, s := range stocks[cap:] {
			if pinned[s] && !seen[s] {
				kept = append(kept, s)
				seen[s] = true
			}
		}
		stocks = kept
	}
	f.hotStocks = stocks
}

// hotPoolCapNow 返回当前档位的热点池上限（降级档＝既有常数 maxWatchStocks=60）。
// 单独抽出来是因为 UpdateHotStocks 持 f.mu，而 hub() 也要 f.mu —— 取档必须在加锁前完成。
func (f *Fetcher) hotPoolCapNow() int { return f.hotPoolCap() }

// TrackStock 只把代码加入监控池，**不立即取价**（下一次 5s 刷新统一拉）。
//
// 为什么不能复用 EnsureStock：那个函数会**立刻单独打一次上游**。展示侧一轮要补的代码
// 是几十到上百个（现网 205 个信号里 177 个不在池里），用 EnsureStock 就是把这 177 次
// 请求从"批量超时"变成"逐票轰炸"——正是 §SIGBATCH 当初要消灭的形态。
// 这里只登记，请求数不变，下一轮刷新后这些代码零网络命中快照。
// English: pool-only registration (no immediate per-code fetch) so warming N displayed symbols
// never recreates the per-symbol upstream hammering that §SIGBATCH removed.
func (f *Fetcher) TrackStock(code string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return
	}
	f.mu.Lock()
	for _, s := range f.baseStocks {
		if s == code {
			f.mu.Unlock()
			return
		}
	}
	f.baseStocks = append(f.baseStocks, code)
	f.mu.Unlock()
	// §SUBSCRIPTION-A：进池即钉住。被引用的代码不许因为热点轮换或收池被截掉
	// （做 T 的一只票看不见价，比多占一个池位危险得多）。
	f.pinCode(code)
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
	f.pinCode(code) // §SUBSCRIPTION-A：补价即钉住，理由同 TrackStock

	// 不等下一轮 scanCycle：立刻单独补拉一次行情再合并进快照，
	// 让前端刚加完自选就能看到名称与价格。取价失败只记日志，
	// 代码仍留在监控列表里，由常规轮询继续重试。
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
	f := &Fetcher{
		api:        api,
		dc:         dc,
		baseStocks: stocks,
		stopCh:     ch,
	}
	// §同花顺（新）最高优先源：环境变量缺 Key 时为 nil，源链行为与旧版完全一致。
	if hc, herr := NewHithinkClient(); herr == nil {
		f.hithink = hc
		f.hithinkState = &HithinkSourceState{}
		log.Printf("[fetcher] 同花顺（新）行情源已启用（最高优先级）")
	}
	return f
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

// SetRefreshInterval §A+B 设置行情快照刷新间隔（0 → 回退默认 5s）。须在 Start() 前调用。
// English: A+B — sets the quote-snapshot refresh interval (0 → default 5s). Call before Start().
func (f *Fetcher) SetRefreshInterval(d time.Duration) {
	f.refreshInterval = d
}

// FetchOnce 执行一次数据获取（非交易时段用）。
// FetchOnce triggers a single fetch (for non-trading sessions).
func (f *Fetcher) FetchOnce() {
	f.fetch()
}

// Snapshot 返回当前最新行情快照。
// §R3-2 P0-D2 拷贝返回：此前直接暴露内部 snapshot 指针，外部读者迭代 Stocks map 时
// EnsureStock/fetch 会并发写入同一 map → fatal error（不可 recover）。现返回浅拷贝
// （结构体 + map 逐键复制；StockInfo 本体按"存入后不可变"约定共享）。
// English: R3-2 P0-D2 — return a shallow copy (struct + per-key map copy); readers no longer
// touch the internal map that writers mutate.
func (f *Fetcher) Snapshot() *MarketSnapshot {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.snapshot == nil {
		return nil
	}
	cp := *f.snapshot
	cp.Stocks = make(map[string]*StockInfo, len(f.snapshot.Stocks))
	for k, v := range f.snapshot.Stocks {
		cp.Stocks[k] = v
	}
	return &cp
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

// Monitoring 报告某代码当前是否已在 5s 监控池（base ∪ hot 任一）。
// 供引擎在"信号入池/持仓钉仓"时幂等判定：已监控则跳过冗余单查与重复加池。
// English: reports whether a code is already in the live monitor pool (base ∪ hot),
// letting the engine skip redundant single-fetches when ensuring a code into the pool.
func (f *Fetcher) Monitoring(code string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, s := range f.baseStocks {
		if s == code {
			return true
		}
	}
	for _, s := range f.hotStocks {
		if s == code {
			return true
		}
	}
	return false
}

// SnapshotQuote 返回快照中某只股票的最新行情（锁内返回指针引用，按"存入后不可变"约定只读）。
// 用于引擎把刚 EnsureStock 入池的信号股价格合并进本轮打分/撮合。
// English: returns the snapshot quote for one code (pointer under lock, read-only per the
// immutable-after-store convention) — used to merge a just-ensured signal's price into this round.
func (f *Fetcher) SnapshotQuote(code string) *StockInfo {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.snapshot == nil {
		return nil
	}
	return f.snapshot.Stocks[code]
}

// Quote 单查一只股票实时行情（走数据协调器统一降级链：同花顺→新浪→东财）。
// English: fetches a fresh single-code quote via the unified source chain (THS→Sina→EastMoney).
func (f *Fetcher) Quote(code string) (*StockInfo, error) {
	return f.dc.GetQuote(code)
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
	// §R3-2 P0-D3 锁内取数：启动日志不再锁外直读两个字段
	baseN, hotN := f.watchCounts()
	log.Printf("数据采集开始, 监控 %d 只股票(自选+持仓%d 热点%d), 来源 %s", len(all), baseN, hotN, f.dc.SourceName())
	interval := f.refreshInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 非活跃时段门控：盘后/休市停止抓行情（保留上一份快照供 HTTP/SSE 读旧值），
	// 避免 5s 循环 24/7 空转消耗 CPU；时段切换各打一条日志。
	// English: inactive-session gate — outside active sessions (after-market/holiday) the loop
	// keeps the last snapshot but stops polling quotes/sectors, so the 5s cadence doesn't burn
	// CPU around the clock. One log line is emitted on each session transition.
	paused := false
	for {
		select {
		case <-f.stopCh:
			log.Println("数据采集停止")
			return
		case <-ticker.C:
			active := IsActiveSession(time.Now())
			if !active {
				if !paused {
					log.Printf("数据采集暂停: 非活跃时段 (保留上一份快照 %s)", time.Now().Format("15:04:05"))
					paused = true
				}
				continue
			}
			if paused {
				log.Printf("数据采集恢复: 进入活跃时段")
				paused = false
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("fetch panic: %v", r)
					}
				}()
				f.fetch()
			}()
			// §GAP3.3 盘中延迟告警（§M2 起统一走 checkStaleAlert 出口）：快照陈旧超 60s
			// 说明上游源整体异常（全失败轮 fetch 不刷 lastOK，陈旧度得以持续累加到这里）。
			f.checkStaleAlert()
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

	// 0. 同花顺（新）官方批量（§数据源优先级矩阵最高优先）：单次请求全池；
	//    连续失败 5 次进入降级（跳过+10min探活），成功自动升回并清零计数。
	if f.hithink != nil && f.hithinkState.available() {
		if quotes, err := f.hithink.BatchQuotes(all); err == nil {
			f.hithinkState.markSuccess()
			for code, si := range quotes {
				snapshot.Stocks[code] = si
			}
			if len(quotes) > 0 {
				snapshot.Source = QuoteSourceHithinkBatch // §M1 枚举常量：主导来源标注（其余源仅补缺）
			}
		} else if firstDegraded := f.hithinkState.markFailure(); firstDegraded {
			log.Printf("[fetcher] 同花顺（新）连续失败 %d 次进入降级，改走 同花顺（老）/sina/东财: %v",
				hithinkFailThreshold, err)
		}
	}

	// 0b. 竞价窗口（9:15-9:26）：同花顺（新）auction stage=live 注入——
	//     抢筹幅度/量比是当日开盘强弱最早的官方信号；窗口外跳过。
	f.maybeFetchAuction(all)

	// 1. 新浪批量（单次请求全池），满足 同花顺（老）/sina/东财 降级链且避免单股限流拖慢 5s 循环
	if f.api != nil {
		for code, si := range f.api.GetSinaQuotes(all) {
			if si != nil && si.Price > 0 {
				snapshot.Stocks[code] = si
			}
		}
	}

	// 1b. 腾讯批量兜底：新浪源被封/超时时，用腾讯 qt.gtimg.cn 一次拉全池，
	// 保证 5s 快照始终有数据（quote() 直接命中，HTTP 接口秒回）。
	// Tencent batch fallback so the 5s snapshot is always warm even when Sina is blocked.
	if len(snapshot.Stocks) < len(all) && f.api != nil {
		var missCodes []string
		for _, c := range all {
			if _, ok := snapshot.Stocks[c]; !ok {
				missCodes = append(missCodes, c)
			}
		}
		if len(missCodes) > 0 {
			for code, si := range f.api.GetTencentQuotes(missCodes) {
				if si != nil && si.Price > 0 {
					snapshot.Stocks[code] = si
				}
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

	// §M2 反「全挂仍替换快照+刷 lastOK」：本轮监控池非空、但四路行情源
	// （hithink 批量 / 新浪批量 / 腾讯批量 / 单股降级链 同花顺→东财）全部失败、一股未回时，
	// 绝不用空快照顶掉上一份 last-known-good，也不刷新 lastOK——
	// 旧缺陷后果：快照被清空 + 陈旧度归零，§WS-C 陈旧行情闸被"伪造新鲜"解除（比空快照更危险，
	// 因为它让下游误以为行情刚更新过）。现让 Staleness 继续累加并经 checkStaleAlert 触发告警。
	// 从未成功采集过时 snapshot 保持 nil（消费端读到空 = 未知，不编造）。
	// English: §M2 — when the pool is non-empty but every quote source returned nothing,
	// keep the last-known-good snapshot, do NOT refresh lastOK, and let staleness keep
	// growing (the old code wiped the snapshot AND reset staleness, forging freshness for
	// the §WS-C stale-quote gate).
	if len(all) > 0 && len(snapshot.Stocks) == 0 {
		log.Printf("[fetcher] §M2 本轮行情全源失败 (0/%d 只)，保留上一份快照、不刷新 lastOK", len(all))
		f.checkStaleAlert()
		return
	}

	// §SUBSCRIPTION-A（2026-11-01）写快照**之前**合并券商 overlay：单写者语义。
	// 旧形状是 feed 事后整份注入 ⇒ 本轮刚拿到的券商价会在下一轮被外部源重建顶掉，
	// "主源"名不副实且价格每 5s 来回抖。合并数只影响本轮快照内容，不改 lastOK 的口径
	// （全局新鲜度仍由"外部链成功一轮"来刷新，overlay 的每票时刻走 StalenessMs(code)）。
	if n := f.applyFeedOverlay(snapshot, time.Now()); n > 0 {
		snapshot.Source = QuoteSourceQMTL1 // 本轮有券商价参与（整份单值 Source 的语义见 A3：每票源下沉是下一批）
	}
	f.mu.Lock()
	f.snapshot = snapshot
	f.mu.Unlock()
	f.lastOK.Store(time.Now().Unix())
	f.emitSink(snapshot)
	f.persistSnapshotMaybe(snapshot)
	// 成功轮 lastOK 刚刷新，Staleness≈0，无需陈旧告警（全败分支才需要）。
}

// checkStaleAlert §M2/§GAP3.3 快照陈旧告警统一出口：Staleness 持续 > 60s 说明上游源整体
// 异常，按 1 条/分钟节流打告警日志；返回 true 表示本轮触发了告警。
// 从未采集（Staleness 回 -1，见 §M2）不告警——尚无基线可陈旧，避免启动期噪音。
// 采集轮 loop() 与 §M2 全败分支共用本出口，保证"全失败时告警仍可达"。
// English: §M2/§GAP3.3 — single exit for the 60s-staleness warning (throttled 1/min).
// Never-fetched (-1, unknown) never warns: there is no baseline to be stale against.
func (f *Fetcher) checkStaleAlert() bool {
	s := f.Staleness()
	if s > 60*time.Second && time.Now().Unix()-f.lastStaleWarn.Load() >= 60 {
		f.lastStaleWarn.Store(time.Now().Unix())
		log.Printf("[fetcher] 警告: 快照已陈旧 %s，上游行情源可能整体异常", s.Round(time.Second))
		return true
	}
	return false
}

// SetSnapshotSink §WS-G 注册快照流接收器（staging 录制 / 回放对比用；可空）。
// English: §WS-G registers a snapshot-stream sink (staging recording / replay parity; may be nil).
func (f *Fetcher) SetSnapshotSink(fn func(*MarketSnapshot)) {
	f.mu.Lock()
	f.sink = fn
	f.mu.Unlock()
}

// emitSink 在锁外回调快照接收器（不阻塞采集主流程）。
// English: emits the snapshot to the sink outside the lock (non-blocking for the fetch loop).
func (f *Fetcher) emitSink(snap *MarketSnapshot) {
	f.mu.RLock()
	fn := f.sink
	f.mu.RUnlock()
	if fn != nil {
		fn(snap)
	}
}

// IngestSnapshot §WS-G 回放：把录制/外部快照注入 fetcher（替代一轮采集），
// 驱动打分循环与线上同输入。English: §WS-G replay — injects a recorded/external snapshot into the
// fetcher (replacing a fetch round) so the scoring loop runs on identical inputs to production.
func (f *Fetcher) IngestSnapshot(snap *MarketSnapshot) {
	if snap == nil {
		return
	}
	f.mu.Lock()
	f.snapshot = snap
	f.mu.Unlock()
	f.lastOK.Store(time.Now().Unix())
	f.emitSink(snap)
}

// SetDataDir §GAP3.2 启用快照落盘（启动时调用一次）。
func (f *Fetcher) SetDataDir(dir string) {
	f.mu.Lock()
	f.dataDir = dir
	f.mu.Unlock()
}

// persistSnapshotMaybe 落盘节拍：每 6 拍（≈30s）原子写一次 snapshot_latest.json；
// 失败仅记日志不阻断采集。English: throttled atomic persistence of the latest snapshot.
func (f *Fetcher) persistSnapshotMaybe(snapshot *MarketSnapshot) {
	f.mu.RLock()
	dir := f.dataDir
	f.mu.RUnlock()
	if dir == "" {
		return
	}
	f.persistTick++
	if f.persistTick%6 != 1 { // 首拍即写，其后每 6 拍一写
		return
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	path := filepath.Join(dir, "snapshot_latest.json")
	// §0926E2E-17B：手写 tmp+rename 段改走包内统一原子写（fileutil.AtomicWrite 薄包装）——
	// 旧写法用固定名 snapshot_latest.json.tmp，quant 与 research 若同目录并发落快照会
	// 互相踩踏半截文件，且无 fsync，掉电场景 rename 后内容可能未落盘。统一实现＝
	// CreateTemp 唯一名 + 写后 fsync + rename + 目录 fsync，权限集中在 0644 一处声明。
	if werr := AtomicWrite(path, raw, 0644); werr != nil {
		log.Printf("[fetcher] 快照落盘失败: %v", werr)
	}
}

// LoadPersistedSnapshot 同交易日启动恢复：读回上次落盘快照（当日有效，跨日丢弃）。
// English: restores the persisted snapshot on startup when it belongs to today's trading day.
func (f *Fetcher) LoadPersistedSnapshot(dir string) {
	raw, err := os.ReadFile(filepath.Join(dir, "snapshot_latest.json"))
	if err != nil {
		return
	}
	var snap MarketSnapshot
	if json.Unmarshal(raw, &snap) != nil || len(snap.Stocks) == 0 {
		return
	}
	if TradingDayDate(snap.Time) != TradingDayDate(time.Now()) {
		log.Printf("[fetcher] 忽略跨日持久化快照(%s)", snap.Time.Format("2006-01-02"))
		return
	}
	f.mu.Lock()
	if f.snapshot == nil || f.snapshot.Time.Before(snap.Time) {
		f.snapshot = &snap
	}
	f.mu.Unlock()
	f.lastOK.Store(snap.Time.Unix())
	log.Printf("[fetcher] 已恢复持久化快照: %d 只 (%s)", len(snap.Stocks), snap.Time.Format("15:04:05"))
}

// Staleness §GAP3.3 快照陈旧度：距最近一次成功采集的时长。
// §M2 语义修正：**从未采集返回 -1 秒**（负值=未知，消费端须按 -1 显示"未知"），
// 旧实现回 0 会让"从没拿到过行情"与"行情绝对新鲜"不可区分（盘外空串假绿的同族形态）。
// 消费口径：/api/status 的 quote_age_sec、loop/checkStaleAlert 陈旧告警（负值不触发）。
// 盘中该值持续 > 30s 说明上游源整体异常——供健康检查/告警消费。
// English: §M2 — staleness of the last successful fetch; NEVER-fetched now returns -1s
// (unknown) instead of 0, so "no data yet" can never masquerade as "perfectly fresh".
func (f *Fetcher) Staleness() time.Duration {
	ts := f.lastOK.Load()
	if ts == 0 {
		return -1 * time.Second // §M2：-1=从未采集（未知），消费端禁止当 0（新鲜）用
	}
	return time.Since(time.Unix(ts, 0))
}

// StalenessMs §WS-C 行情新鲜度硬闸用：返回**这一只票**的陈旧度（毫秒；-1＝从未有证据）。
//
// §SUBSCRIPTION-A（2026-11-01）语义升级：这里开始吃每票自己的新鲜时刻。
// 旧实现只读全局 lastOK，而全局基线会被"feed 命中 1 只"归零——那只票的新鲜替全池作了保，
// 硬闸以为行情刚更新过，实际同池其它票可能已是几分钟前的（本文件 IngestSnapshot 那条洞）。
// 现在的规则是**取两个证据里更新的一个**：
//   - 有券商 overlay 时刻 ⇒ 参与比较；
//   - 没有 ⇒ 只按全局；
//   - 两者都没有 ⇒ -1（未知，消费端绝不当 0/新鲜用，§M2 那条方向不许破）。
//
// 注意"更新的"不等于"更宽松的"：全局基线现在只在覆盖率达标时才刷新（见 IngestFeedRound），
// 所以拿不到券商 tick 的那些票，陈旧度照样持续增长、照样被闸拦下。
//
// English: per-code quote staleness for the §WS-C hard gate. Takes the freshest of the code's own
// broker-tick timestamp and the global baseline; -1 (unknown) when neither exists — never 0.
func (f *Fetcher) StalenessMs(code string) int64 {
	var best int64 = -1
	if ts := f.lastOK.Load(); ts != 0 {
		best = time.Since(time.Unix(ts, 0)).Milliseconds()
	}
	if ms := f.feedFreshMs(code); ms > 0 {
		d := time.Since(time.UnixMilli(ms)).Milliseconds()
		if best < 0 || d < best {
			best = d
		}
	}
	return best
}

// inAuctionWindow 当前是否处于集合竞价注入窗口（9:15-9:26，Asia/Shanghai）。
// §0925EVE-W3-E（C6）：补上 §CAL-GATE 漏网的第 15 判据——旧实现纯 HHMM 比较，
// 周末/法定节假日（交易日历休市日）照样在表观窗口内放行，maybeFetchAuction 于是
// 每天早晨 9:15-9:26 向 hithink 空转发一次全池竞价请求（休市日根本没有竞价）。
// 与本文件族 trade_time.go 的 14 个时段判据同口径：先过 IsTradingDay 权威闸再比钟面。
// English: §0925EVE-W3-E — the 15th session gate now joins the CAL-GATE discipline:
// IsTradingDay first, then the HH:MM window, so closed days no longer fire auction fetches.
func InAuctionWindow(now time.Time) bool {
	if !IsTradingDay(now) {
		return false // 休市日（周末/日历节假日）无集合竞价，窗口判据一律关
	}
	m := now.Hour()*100 + now.Minute()
	return m >= 915 && m <= 926
}

// maybeFetchAuction 竞价窗口内拉取全池竞价快照（一次请求）并缓存；
// 非窗口/无 hithink 源时为 no-op。失败计数走源健康状态机。
func (f *Fetcher) maybeFetchAuction(codes []string) {
	if f.hithink == nil || !InAuctionWindow(time.Now()) || !f.hithinkState.available() {
		return
	}
	snap, err := f.hithink.Auction(codes, "live")
	if err != nil {
		f.hithinkState.markFailure()
		return
	}
	out := make(map[string]*HithinkAuctionItem, len(snap.Item))
	for i := range snap.Item {
		it := &snap.Item[i]
		code := strings.Split(it.ThsCode, ".")[0]
		out[code] = it
	}
	f.auctionMu.Lock()
	f.auction = out
	f.auctionMu.Unlock()
}

// AuctionSnapshot 返回最新竞价快照副本（引擎打分循环消费；非窗口返回空 map）。
func (f *Fetcher) AuctionSnapshot() map[string]HithinkAuctionItem {
	f.auctionMu.Lock()
	defer f.auctionMu.Unlock()
	if len(f.auction) == 0 {
		return nil
	}
	out := make(map[string]HithinkAuctionItem, len(f.auction))
	for k, v := range f.auction {
		out[k] = *v
	}
	return out
}
