// Package data — 多数据源调度与熔断协调层。
// 统一封装东方财富(MarketAPI)与同花顺(THSClient)的调度策略：
//   - 行情：新浪 → 同花顺 → 东财 三级降级链，同花顺失败自动熔断 60s；
//   - 板块/IPO：同花顺 → 东财，带 30s/60s TTL 缓存；
//   - 新闻：同花顺(主) → 新浪(兜底) 去重合并。
//
// §M1/F4：本文件定义行情源名枚举 QuoteSource*（/api/status quote_source 契约的 Go 侧
// 唯一事实源），golden 落在 qmt_gateway/contract/quote_sources.json，由
// quote_sources_contract_test.go 做 AST 双向锁；§M3：NewsSourceHealth 只消费真实抓取
// 统计（MarketAPI.recordNewsFetch），从未探测的源一律回 unknown，不再由指针就绪推导。
//
// English: batch-G hardening notes — §M1: the QuoteSource* enum below is the single source
// of truth for /api/status quote_source, mirrored by the golden file
// qmt_gateway/contract/quote_sources.json (AST-locked both ways); §M3: NewsSourceHealth
// reports real fetch stats only, unknown-for-never-probed.
//
// English: Package data — a multi-source dispatch and circuit-breaker coordination layer.
// English: It uniformly wraps the dispatch strategy of EastMoney (MarketAPI) and Tonghuashun (THSClient):
// English:   - Quotes: Sina → THS → EastMoney 3-level fallback chain; THS auto-breaks for 60s on failure;
// English:   - Sectors/IPOs: THS → EastMoney with 30s/60s TTL caches;
// English:   - News: THS (primary) → Sina (fallback), merged with dedup.
//
// Package data — a multi-source dispatch and circuit-breaker layer.
// It wraps EastMoney (MarketAPI) and Tonghuashun (THSClient) strategy:
//   - Quotes: Sina → THS → EastMoney fallback chain, THS auto-breaks for 60s;
//   - Sectors/IPOs: THS → EastMoney with 30s/60s TTL caches;
//   - News: THS (primary) → Sina (fallback), merged with dedup.
package data

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DataCoordinator 多数据源调度与熔断。
// 行情数据源链：新浪 → 同花顺 → 东财
// 板块/IPO 数据源链：同花顺 → 东财
// English: DataCoordinator coordinates multiple sources with circuit breaking.
// English: Quote chain: Sina → THS → EastMoney; Sector/IPO chain: THS → EastMoney.
// DataCoordinator coordinates multiple sources with circuit breaking.
// Quote chain: Sina → THS → EastMoney; Sector/IPO chain: THS → EastMoney.
type DataCoordinator struct {
	eastMoney *MarketAPI
	ths       *THSClient

	// hithink 同花顺（新）官方数据源客户端，作为行情/板块的最高优先级源（东财永远兜底）。
	// 该字段为可选：默认 nil，由 server 层在持有 hithink 客户端时通过 SetHithink 注入；
	// 为 nil 时自动降级到后续链，不影响既有逻辑（不报错、不破坏调用方）。
	// English: hithink (the new Tonghuashun official client) is the top-priority source;
	// EastMoney is always the final fallback. It is optional and injected via SetHithink.
	hithink *HithinkClient

	mu sync.RWMutex

	// thsDeadlines 同花顺熔断截止时间，**按操作域隔离**（失败后 60s 内不再尝试该操作）。
	// §修复 THS-BREAKER(20260920)：原实现用单个 thsDeadline 给 5 条能力共用一把闸，
	// 任一子能力失败都会把同花顺整源关掉 60s。最典型的自伤：分钟 K 线要 15 分钟周期
	// （同花顺无此档）被判"不支持"，却按"供应商故障"熔断 → 连带把已正常工作的
	// 同花顺**报价/板块**一起挡掉。熔断的语义应是"这个能力现在不可用"，不是"这家供应商挂了"。
	// English: per-operation THS circuit-break deadlines. A single shared deadline let any one
	// capability's failure disable the entire THS source for 60s (e.g. an unsupported intraday
	// period being treated as a provider outage and killing the working quote/board paths).
	thsDeadlines map[string]time.Time

	sectorCache      []SectorInfo                  // 板块列表缓存
	sectorCacheAt    time.Time                     // 板块缓存写入时间（30s TTL）
	sectorStockCache map[string]cachedSectorStocks // 板块成分股缓存（60s TTL）
	// English: sectorCache: sector list cache; sectorCacheAt: cache write time (30s TTL);
	// English: sectorStockCache: per-sector constituent cache (60s TTL).

	ipoCache   []IPOEvent // 新股日历缓存
	ipoCacheAt time.Time  // 新股日历缓存写入时间（5min TTL）
	// English: ipoCache: IPO-calendar cache; ipoCacheAt: cache write time (5min TTL).

	ipoRefreshing atomic.Bool // IPO 日历刷新进行中标志（防 TTL 到期瞬间多调用方并发重复刷新）
	// English: ipoRefreshing: an IPO-calendar refresh is in flight (prevents concurrent
	// English: duplicate refreshes when multiple callers hit the expired-TTL path at once).

	lastSource string // §P1-10 最近一次成功命中的行情源名称（取值见 §M1 QuoteSource* 枚举）
	// English: name of the last successful quote source (values locked to the §M1 QuoteSource* enum).
}

// ── §M1/F4 行情源名枚举——契约单源化（single source of truth）──
// 背景：/api/status 的 quote_source 取自 MarketSnapshot.Source，历史上该值由
// 三处字面量各写各的（降级链小写英文名 / fetcher 中文"同花顺（新）" / qmt_feed "QMT-L1"），
// 而 web/e2e 白名单硬编码中文，两端漂移导致巡检假绿/假红。本组常量 + golden 文件
// qmt_gateway/contract/quote_sources.json 即唯一事实源：Go 侧所有写入点必须引用常量，
// E2E 白名单必须从 golden 读取；golden 契约测试（quote_sources_contract_test.go）
// 做 AST 双向锁——源文件字面量 ⊆ golden、golden ⊆ 源文件字面量，任何一侧漂移即红。
// 空串 "" 语义：**盘外/快照未就绪**（fetcher 未跑过一轮或整轮无数据），它不是源名、
// 不进 golden 枚举；消费端须按"未知/盘外"处理，禁止与任何源名混同。
// English: §M1/F4 — the canonical quote-source vocabulary. Every writer of
// MarketSnapshot.Source / DataCoordinator.lastSource must use these constants; the golden
// file qmt_gateway/contract/quote_sources.json mirrors them and the E2E whitelist must read
// that file instead of hardcoding. The empty string means "outside session / snapshot not
// ready" — deliberately NOT part of the enum.
const (
	QuoteSourceHithink      = "hithink"   // 降级链：同花顺（新）官方单票命中
	QuoteSourceSina         = "sina"      // 降级链：新浪实时命中
	QuoteSourceTHS          = "ths"       // 降级链：同花顺（老）命中
	QuoteSourceEastMoney    = "eastmoney" // 降级链：东财末位兜底命中
	QuoteSourceHithinkBatch = "同花顺（新）"    // fetcher 批量轮主导来源标注（中文，历史口径）
	QuoteSourceQMTL1        = "QMT-L1"    // §ENH-5 Level-1 feed 合并注入
)

// AllQuoteSources 返回全部合法的行情源名取值（golden 契约的 Go 侧镜像）。
// 顺序不保证，调用方自行排序；新增源必须同时：加常量 + 加本函数 + 重新生成 golden
// （go test ./internal/data -run TestQuoteSourcesGolden -update）。
// English: the full legal vocabulary of quote_source values; adding a source requires
// updating the constant, this function, and regenerating the golden JSON.
func AllQuoteSources() []string {
	return []string{
		QuoteSourceHithink,
		QuoteSourceSina,
		QuoteSourceTHS,
		QuoteSourceEastMoney,
		QuoteSourceHithinkBatch,
		QuoteSourceQMTL1,
	}
}

// cachedSectorStocks 板块成分股缓存条目。
// English: cachedSectorStocks is a sector-constituent cache entry.
// cachedSectorStocks is a sector-constituent cache entry.
type cachedSectorStocks struct {
	stocks []StockInfo // 缓存的成分股列表
	at     time.Time   // 缓存写入时间（用于判断是否过期）
	// English: stocks: cached constituent list; at: cache write time (used to check expiry).
}

// NewDataCoordinator 创建数据协调器实例。
// English: NewDataCoordinator creates a DataCoordinator instance.
// NewDataCoordinator creates a DataCoordinator instance.
func NewDataCoordinator(api *MarketAPI, ths *THSClient) *DataCoordinator {
	return &DataCoordinator{
		eastMoney:        api,
		ths:              ths,
		sectorStockCache: make(map[string]cachedSectorStocks),
	}
}

// SetHithink 注入同花顺（新）官方数据源客户端（可选）。
// 调用方（server 层）若已构造 *HithinkClient 则调用本方法注入，使行情/板块链路
// 以 hithink 为第一顺位、东财为最末兜底。未注入（h==nil）时功能自动降级，不报错。
// 注意：本方法不改变 NewDataCoordinator(api, ths) 签名，避免牵连大量调用方。
// English: SetHithink injects the optional Hithink (new THS) client as the top-priority
// source. When not called (nil), the chain degrades gracefully; EastMoney stays last.
func (dc *DataCoordinator) SetHithink(h *HithinkClient) {
	dc.mu.Lock()
	dc.hithink = h
	dc.mu.Unlock()
}

// 同花顺操作域标识：熔断按域隔离（见 DataCoordinator.thsDeadlines 注释）。
// THS operation domains used as circuit-breaker keys.
const (
	thsOpQuote      = "quote"       // 个股实时行情
	thsOpKLine      = "kline"       // 日 K 线
	thsOpMinute     = "minute"      // 分钟 K 线
	thsOpBoards     = "boards"      // 板块列表
	thsOpBoardStock = "boardstocks" // 板块成分股
)

// thsAvailable §R3-2 P0-D1 熔断状态锁内读取：thsDeadline 此前被 GetQuote/GetKLine/
// GetMinuteKLine/GetSectors/GetSectorStocks 五条并发路径（fetcher 5s 循环 × HTTP handler ×
// 打分循环）裸读写——data race 且熔断时间戳撕裂会导致熔断失效或提前熔断。统一走本封装。
// §修复 THS-BREAKER(20260920)：入参改为操作域 op，只判断该域自己的熔断窗口。
// English: R3-2 P0-D1 — locked read of the THS circuit-break deadline; scoped by operation domain.
func (dc *DataCoordinator) thsAvailable(op string) bool {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.ths != nil && time.Now().After(dc.thsDeadlines[op])
}

// tripThs §R3-2 P0-D1 熔断置位锁内写入（默认 60s，与历史口径一致）。
// §修复 THS-BREAKER(20260920)：只熔断指定操作域；op 为空时不做任何事（防误用成全局熔断）。
// English: trips the breaker for one operation domain only (op == "" is a no-op).
func (dc *DataCoordinator) tripThs(op string) {
	if op == "" {
		return
	}
	dc.mu.Lock()
	if dc.thsDeadlines == nil {
		dc.thsDeadlines = make(map[string]time.Time, 5)
	}
	dc.thsDeadlines[op] = time.Now().Add(60 * time.Second)
	dc.mu.Unlock()
}

// HealthCheck 探测所有行情源的可用性，委托给东财 MarketAPI 的健康检查。
// English: HealthCheck probes the availability of all quote sources, delegating to the EastMoney MarketAPI health check.
// （HealthCheck probes the availability of all market data sources, delegating to the EastMoney MarketAPI health check.）
func (dc *DataCoordinator) HealthCheck() map[string]bool {
	if dc.eastMoney == nil {
		return map[string]bool{"eastmoney": false, "sina": false, "tencent": false, "ths": false}
	}
	base := dc.eastMoney.HealthCheck()
	// 同花顺由 THSClient 探测（由 DataCoordinator 持有）
	// English: THS is probed by the THSClient (held by DataCoordinator).
	if dc.ths != nil {
		result := make(map[string]bool, 4)
		for k, v := range base {
			result[k] = v
		}
		result["ths"] = dc.ths.HealthCheck()
		return result
	}
	return base
}

// NewsSourceHealth 探测新闻资讯源的真实可用性（§M3 重写）。
// 旧缺陷：状态由 `eastMoney.client != nil` 之类指针就绪推导——只要客户端构造过就恒真，
// 属纯编造；且键名 "cainanshe" 拼写错误。现改为消费 MarketAPI 的逐源真实抓取统计
// （财联社/同花顺快讯/新浪各自的 最近成功时间、连续错误数、累计错误数）：
//   - 从未发生过任何一次抓取 → 状态 "unknown"（绝不回 "ok"，杜绝零探测编造）；
//   - 最近一次抓取失败（连续错误数>0） → "down"；
//   - 最近一次抓取成功 → "ok"。
//
// 注意：键名已修正为 "cailanshe"，web 消费端需同步（见修复报告，主代理收尾）。
// English: §M3 — real per-source news fetch statistics (last success / consecutive errors /
// total errors) replace the old client-pointer fabrication; a source never fetched must
// report "unknown", never "ok". Key typo cainanshe→cailanshe fixed (frontend follows later).
func (dc *DataCoordinator) NewsSourceHealth() map[string]NewsSourceStatus {
	out := make(map[string]NewsSourceStatus, 3)
	for _, key := range []string{NewsSourceCLS, NewsSourceTHSFlash, NewsSourceSina} {
		var snap NewsSourceStatSnapshot
		if dc != nil && dc.eastMoney != nil {
			snap = dc.eastMoney.newsSourceStatFor(key)
		}
		out[key] = newsStatusFrom(snap)
	}
	return out
}

// NewsSourceStatus 单个新闻源的对外健康结构（/api/news_source_health JSON 形态）。
// Status 枚举：ok / down / unknown（unknown=从未探测，禁止误读为健康）。
// English: one news source's public health struct; status ∈ {ok, down, unknown}.
type NewsSourceStatus struct {
	Status        string `json:"status"` // ok / down / unknown（§M3：无探测数据必为 unknown）
	LastSuccessAt string `json:"last_success_at,omitempty"`
	// 最近成功抓取时间（RFC3339，本地/北京时区）；从未成功=省略
	ConsecutiveErrors int   `json:"consecutive_errors"` // 连续失败计数
	TotalErrors       int64 `json:"total_errors"`       // 累计失败计数
}

// newsStatusFrom 把内部统计映射为对外状态。语义钉死：无探测数据 → unknown。
// newsStatusFrom maps internal stats to the public status; no probe data ⇒ unknown.
func newsStatusFrom(snap NewsSourceStatSnapshot) NewsSourceStatus {
	st := NewsSourceStatus{
		ConsecutiveErrors: snap.ConsecutiveErrs,
		TotalErrors:       snap.TotalErrs,
	}
	if !snap.Probed {
		st.Status = "unknown"
		return st
	}
	if !snap.LastSuccessAt.IsZero() {
		st.LastSuccessAt = snap.LastSuccessAt.Format(time.RFC3339)
	}
	if snap.ConsecutiveErrs > 0 {
		st.Status = "down"
	} else {
		st.Status = "ok"
	}
	return st
}

// GetQuote 获取个股实时行情：同花顺（新）hithink → 新浪 → 同花顺 → 东财 四级降级链，
// 同花顺每失败一次熔断 60s。东财永远处于最末兜底位（绝不作为第一/主源）。
// 同花顺（新）hithink 为第一顺位：优先用 BatchQuotes 批量快照取该 code 的实时价。
// 四级源全部失败时返回明确错误（不再返回零值脏快照，避免前端显示 0.00 元）。
// English: GetQuote fetches a per-stock realtime quote down the chain
// hithink → Sina → THS → EastMoney; EastMoney is always the final fallback.
func (dc *DataCoordinator) GetQuote(code string) (*StockInfo, error) {
	// 空代码防御（§门控配套）：上游对空代码必然失败并刷错误日志
	// （周六实录：每秒数行"新浪/东财行情失败 ()"），直接本地拒绝。
	if code == "" {
		return nil, fmt.Errorf("空股票代码")
	}

	// ① 同花顺（新）hithink 第一顺位：批量快照命中则直接返回，失败/空则继续降级链。
	// hithink 为可选源（可能 nil），nil 时跳过本步，自动降级到后续链，不报错。
	// BatchQuotes 返回的 map 以裸码（去掉 sh/sz/bj 前缀）为键；入参 code 可能带前缀，
	// 故同时尝试原始 code 与去前缀后的裸码两种键。
	if hk := dc.hithink; hk != nil {
		if hkQuotes, hkErr := hk.BatchQuotes([]string{code}); hkErr == nil {
			if si := lookupHithinkQuote(hkQuotes, code); si != nil && si.Price > 0 {
				dc.setLastSource(QuoteSourceHithink) // §M1 枚举常量，禁止裸字面量
				log.Printf("hithink(新)返回 %s 最新价 %.2f", code, si.Price)
				return si, nil
			}
		} else {
			log.Printf("hithink(新)行情失败 (%s): %v, 降级新浪", code, hkErr)
		}
	}

	// ② 新浪：hithink 缺失/失败时的主用源。
	si, err := dc.eastMoney.GetSinaQuote(code)
	if err == nil && si != nil && si.Price > 0 {
		dc.setLastSource(QuoteSourceSina) // §M1 枚举常量
		return si, nil
	}
	if err != nil {
		log.Printf("新浪行情失败 (%s): %v, 降级同花顺", code, err)
	}

	// ③ 同花顺（旧）ths：保留原链，失败按既有逻辑熔断 60s（熔断按操作域隔离，见 thsDeadlines）。
	if dc.thsAvailable(thsOpQuote) {
		thsSi, thsErr := dc.ths.GetQuote(code)
		if thsErr == nil && thsSi != nil && thsSi.Price > 0 {
			dc.setLastSource(QuoteSourceTHS) // §M1 枚举常量
			log.Printf("同花顺返回 %s 最新价 %.2f", code, thsSi.Price)
			return thsSi, nil
		} else if thsErr != nil {
			dc.tripThs(thsOpQuote)
			log.Printf("同花顺失败 (%s): %v, 熔断60s", code, thsErr)
		}
	}

	// ④ 东财：永远处于最末兜底位（绝不作为第一/主源）。
	emSI, emErr := dc.eastMoney.GetRealtimeQuote(code)
	if emErr == nil && emSI != nil && emSI.Price > 0 {
		dc.setLastSource(QuoteSourceEastMoney) // §M1 枚举常量
		return emSI, nil
	}
	if emErr != nil {
		log.Printf("东财行情失败 (%s): %v", code, emErr)
	}

	// §D3 修复：四级源全部失败时不再返回 (nil/零值, nil) 脏快照——此前新浪返回空行时
	// si=nil,err=nil 直通 fetcher 写入 5s 快照、前端显示 0.00 元。现统一返回明确错误。
	// English: D3 fix — when all four sources fail, never return a zero-value snapshot with a
	// nil error (it flowed straight into the 5s snapshot and rendered 0.00 on the frontend).
	if si != nil && si.Price > 0 {
		return si, err
	}
	if err == nil {
		err = fmt.Errorf("全部行情源失败 (%s)", code)
	}
	return nil, err
}

// lookupHithinkQuote 从 hithink 批量快照结果中取指定 code 的行情。
// BatchQuotes 以裸码（去掉 sh/sz/bj 交易所前缀）为键；入参 code 可能带前缀，
// 故依次尝试原始 code 与去前缀裸码两种键，命中且价格有效则返回。
// English: lookupHithinkQuote picks the quote for code from the hithink batch result,
// trying both the raw code and the stripped bare code (BatchQuotes keys by bare code).
func lookupHithinkQuote(quotes map[string]*StockInfo, code string) *StockInfo {
	if si, ok := quotes[code]; ok {
		return si
	}
	// 去除常见交易所前缀（sh/sz/bj），回退到裸码键。
	bare := code
	if len(code) > 2 {
		switch code[:2] {
		case "sh", "sz", "bj":
			bare = code[2:]
		}
	}
	if si, ok := quotes[bare]; ok {
		return si
	}
	return nil
}

// GetKLine 获取 K 线数据。新浪日线 → 腾讯 → 同花顺 → 东财。
// 说明：同花顺（新）hithink 当前未提供通用 K 线接口（不臆造），故本方法保持
// 新浪/腾讯/同花顺/东财顺序，东财恒为最后兜底项。
// English: GetKLine fetches K-lines: Sina → Tencent → THS → EastMoney (EastMoney always last).
func (dc *DataCoordinator) GetKLine(code, period string, count int) ([]KLine, error) {
	// §修复 D6（2026-08-29）：东财 push2 是复权口径锁定(fqt=qfq)、量纲已对齐(×100)的最稳源，
	// 此前被排到链尾仅在三源全崩时兜底，日常却优先撞新浪/腾讯易被 IP 封禁的源。
	// 改为东财首选，新浪/腾讯/同花顺作降级（东财触发熔断时自动回落到免费源）。
	if klines, err := dc.eastMoney.GetKLine(code, period, count); err == nil && ValidateKLine(klines) {
		return klines, nil
	}
	if period == "101" {
		// 新浪/腾讯日K作为东财熔断或限流时的降级；腾讯日K此前已实现但未接入主链。
		if klines, err := dc.eastMoney.GetSinaKLine(code, count); err == nil && ValidateKLine(klines) {
			return klines, nil
		}
		if klines, err := dc.eastMoney.GetTencentKLine(code, count); err == nil && ValidateKLine(klines) {
			return klines, nil
		}
		if dc.thsAvailable(thsOpKLine) {
			thsKL, thsErr := dc.ths.GetTHSKLine(code)
			if thsErr == nil && ValidateKLine(thsKL) {
				return thsKL, nil
			} else if thsErr != nil {
				dc.tripThs(thsOpKLine)
				log.Printf("同花顺日线失败 (%s): %v, 熔断60s", code, thsErr)
			}
		}
	}
	return nil, fmt.Errorf("所有K线源均失败 for %s", code)
}

// GetMinuteKLine 获取分钟级 K 线（分时）。新浪分钟 → 同花顺分钟 → 腾讯分钟 → 东财分钟。
// scale 为分钟数（1/5/15/30/60），返回按时间升序排列的 KLine。
// 说明：同花顺（新）hithink 当前未提供通用分时接口（不臆造），保持既有顺序，
// 东财恒为最后兜底项（绝不成为第一/主源）。
// English: GetMinuteKLine fetches minute K-lines (intraday). Sina → THS → Tencent → EastMoney
// (EastMoney always last; hithink has no generic intraday method, so the chain is unchanged).
func (dc *DataCoordinator) GetMinuteKLine(code string, scale, count int) ([]KLine, error) {
	if klines, err := dc.eastMoney.GetSinaMinuteKLine(code, scale, count); err == nil && len(klines) > 0 {
		return klines, nil
	}

	if dc.thsAvailable(thsOpMinute) {
		// §修复 THS-KLINE(20260920)：把调用方要的周期透传给同花顺（旧实现忽略 scale、
		// 且写死无效的 06 码，该源从未生效）。不支持 15 分钟等周期时同花顺返回错误，此处降级。
		thsKL, thsErr := dc.ths.GetTHSMinuteKLine(code, scale)
		if thsErr == nil && len(thsKL) > 0 {
			return thsKL, nil
		} else if thsErr != nil {
			// §修复 THS-BREAKER(20260920)：周期不受支持是**客户端能力缺失**，不是供应商故障，
			// 绝不能熔断——否则一次 15 分钟请求就会把同花顺整源按故障关掉 60s。
			if !errors.Is(thsErr, ErrTHSUnsupportedPeriod) {
				dc.tripThs(thsOpMinute)
				log.Printf("同花顺分钟线失败 (%s): %v, 熔断60s", code, thsErr)
			} else {
				log.Printf("同花顺不支持该分钟周期 (%s, %d 分钟)，跳过该源不熔断", code, scale)
			}
		}
	}

	if klines, err := dc.eastMoney.GetTencentMinuteKLine(code, scale, count); err == nil && len(klines) > 0 {
		return klines, nil
	}

	if klines, err := dc.eastMoney.GetKLine(code, strconv.Itoa(scale), count); err == nil && len(klines) > 0 {
		return klines, nil
	}
	return nil, fmt.Errorf("所有分钟K线源均失败 for %s", code)
}

// GetSectors 获取板块列表。同花顺(ths) → 东财。
// 说明：同花顺（新）hithink 当前未提供板块列表接口（不臆造），故保持 ths→东财，
// 东财恒为最末兜底（绝不成为第一/主源）。
// English: GetSectors fetches the sector list. THS → EastMoney (EastMoney always last;
// hithink has no board-list method, so the chain is unchanged).
func (dc *DataCoordinator) GetSectors() ([]SectorInfo, error) {
	dc.mu.RLock()
	if len(dc.sectorCache) > 0 && time.Since(dc.sectorCacheAt) < 30*time.Second {
		c := make([]SectorInfo, len(dc.sectorCache))
		copy(c, dc.sectorCache)
		dc.mu.RUnlock()
		return c, nil
	}
	dc.mu.RUnlock()

	var thsSectors []SectorInfo
	if dc.thsAvailable(thsOpBoards) {
		var thsErr error
		thsSectors, thsErr = dc.ths.GetBoardList()
		if thsErr == nil && len(thsSectors) > 0 {
			log.Printf("GetSectors: 同花顺 (%d个板块)", len(thsSectors))
		} else if thsErr != nil {
			dc.tripThs(thsOpBoards)
			log.Printf("同花顺板块列表失败: %v, 熔断60s", thsErr)
		}
	}

	s, emErr := dc.eastMoney.GetSectorList()
	if emErr != nil {
		log.Printf("东财板块不可用: %v", emErr)
	}

	if len(thsSectors) > 0 {
		if len(s) > 0 {
			// 合并策略：同花顺提供板块清单结构，东财提供实时行情数据
			// 1) 按代码/名称匹配同花顺板块，回填东财的涨跌幅/成交额/净流入/涨停家数
			// English: Merge strategy: THS provides the sector list structure, EastMoney provides realtime quotes.
			// English: 1) Match THS sectors by code/name and backfill EastMoney's change%, amount, net inflow, limit-up count.
			emByCode := make(map[string]SectorInfo, len(s))
			emByName := make(map[string]SectorInfo, len(s))
			for _, em := range s {
				emByCode[em.Code] = em
				emByName[em.Name] = em
			}
			used := make(map[string]bool, len(s))
			for i := range thsSectors {
				em, ok := emByCode[thsSectors[i].Code]
				if !ok {
					em, ok = emByName[thsSectors[i].Name]
				}
				if ok {
					thsSectors[i].ChangePct = em.ChangePct
					thsSectors[i].Amount = em.Amount
					thsSectors[i].NetInflow = em.NetInflow
					thsSectors[i].LimitupCnt = em.LimitupCnt
					used[em.Code] = true
				}
			}
			thsNames := make(map[string]bool, len(thsSectors))
			for _, t := range thsSectors {
				thsNames[t.Name] = true
			}
			// 2) 东财独有的板块（同花顺无此代码且无此名称）追加到末尾，保证板块覆盖面
			// English: 2) EastMoney-only sectors (no matching THS code/name) are appended at the end to keep coverage.
			for _, em := range s {
				if !used[em.Code] && !thsNames[em.Name] {
					thsSectors = append(thsSectors, em)
				}
			}
			log.Printf("GetSectors: 同花顺(%d个) + 东财实时(%d个)", len(thsSectors), len(s))
		}
		dc.mu.Lock()
		dc.sectorCache = thsSectors
		dc.sectorCacheAt = time.Now()
		dc.mu.Unlock()
		return thsSectors, nil
	}

	if len(s) > 0 {
		dc.mu.Lock()
		dc.sectorCache = s
		dc.sectorCacheAt = time.Now()
		dc.mu.Unlock()
		log.Printf("GetSectors: 东财 (%d个板块)", len(s))
		return s, nil
	}

	// §LOW(SPOF) 双源皆败的 last-known-good 兜底：同花顺+东财（含镜像分页）整轮失败时，
	// 回退过期板块缓存（真实但陈旧）并打显式「陈旧回退」告警——板块列表用于展示与扫描的
	// 结构性输入，旧结构 > 整段空白；告警可见故不构成静默报成功。无缓存可回退时仍回 error。
	// English: §LOW(SPOF) last-known-good on total failure: serve the expired sector cache with
	// a loud stale-warning instead of a hard error; without any cache the error still propagates.
	dc.mu.RLock()
	stale := dc.sectorCache
	staleAt := dc.sectorCacheAt
	dc.mu.RUnlock()
	if len(stale) > 0 {
		log.Printf("[source] §LOW(SPOF) 板块双源皆败，回退 %v 前的板块缓存 (%d个板块)", time.Since(staleAt).Round(time.Second), len(stale))
		out := make([]SectorInfo, len(stale))
		copy(out, stale)
		return out, nil
	}

	return nil, fmt.Errorf("所有板块源均失败")
}

// GetSectorStocks 获取板块成分股。同花顺(ths) → 东财。
// 说明：同花顺（新）hithink 当前未提供板块成分股接口（不臆造），故保持 ths→东财，
// 东财恒为最末兜底（绝不成为第一/主源）。
// English: GetSectorStocks fetches sector constituents. THS → EastMoney (EastMoney always
// last; hithink has no board-constituent method, so the chain is unchanged).
func (dc *DataCoordinator) GetSectorStocks(sectorCode string, topN int) ([]StockInfo, error) {
	dc.mu.RLock()
	if c, ok := dc.sectorStockCache[sectorCode]; ok && time.Since(c.at) < 60*time.Second {
		out := make([]StockInfo, len(c.stocks))
		copy(out, c.stocks)
		dc.mu.RUnlock()
		return out, nil
	}
	dc.mu.RUnlock()

	// 同花顺优先：东财被限流时板块成分股改走同花顺。
	// English: THS-first: when EastMoney is rate-limited, sector constituents route to THS.
	// THS-first: when EastMoney is rate-limited, sector constituents come from THS.
	if dc.thsAvailable(thsOpBoardStock) {
		thsCode, thsName := dc.matchTHSBoardCode(sectorCode)
		if thsCode == "" {
			thsCode = sectorCode
		}
		stockList, thsErr := dc.ths.GetBoardStocks(thsCode, topN)
		if thsErr == nil && len(stockList) > 0 {
			// 只取代码/名称，实时行情后续由 BuildScoringData/快照兜底补全
			// English: Only keep code/name; realtime quotes are backfilled later by BuildScoringData/snapshot.
			codes := make([]StockInfo, 0, len(stockList))
			for _, st := range stockList {
				codes = append(codes, StockInfo{Code: st.Code, Name: st.Name})
			}
			dc.mu.Lock()
			dc.sectorStockCache[sectorCode] = cachedSectorStocks{stocks: codes, at: time.Now()}
			dc.mu.Unlock()
			log.Printf("GetSectorStocks: 同花顺取 %d 只成分股 (%s%s)", len(codes), sectorCode, func() string {
				if thsName != "" {
					return "/" + thsName
				}
				return ""
			}())
			return codes, nil
		}
	}

	// 东财兜底
	// English: EastMoney fallback.
	s, err := dc.eastMoney.GetSectorStocks(sectorCode, topN)
	if err == nil && len(s) > 0 {
		dc.mu.Lock()
		dc.sectorStockCache[sectorCode] = cachedSectorStocks{stocks: s, at: time.Now()}
		dc.mu.Unlock()
		return s, nil
	}
	if err != nil {
		log.Printf("东财板块成分股失败 (%s): %v", sectorCode, err)
	}

	// §LOW(SPOF) 同花顺+东财双源皆败的 last-known-good 兜底：回退该板块的过期成分股缓存
	// （真实但陈旧）并显式告警；成分股列表用于热点扫描的结构性输入，旧列表 > 整轮空转。
	// 无任何历史缓存时仍透传错误，不编造。
	// English: §LOW(SPOF) both sources failed — serve this sector's expired constituent cache
	// with a loud stale warning (structure-relevant list: old beats empty); error otherwise.
	dc.mu.RLock()
	c, has := dc.sectorStockCache[sectorCode]
	dc.mu.RUnlock()
	if has && len(c.stocks) > 0 {
		log.Printf("[source] §LOW(SPOF) 板块成分股双源皆败，回退 %s 的过期缓存 (%d只, %v 前)",
			sectorCode, len(c.stocks), time.Since(c.at).Round(time.Second))
		out := make([]StockInfo, len(c.stocks))
		copy(out, c.stocks)
		return out, nil
	}
	return s, err
}

// matchTHSBoardCode 将入参板块代码映射到同花顺板块代码。
// 入参可能是同花顺代码（308xxx/881xxx，来自 sector_scanner）或东财 BK 代码。
// 返回同花顺板块代码与名称；映射失败时返回空串（调用方回退用原始代码尝试）。
// English: matchTHSBoardCode maps an input sector code to a THS board code.
// English: The input may be a THS code (308xxx/881xxx, from sector_scanner) or an EastMoney BK code.
// English: Returns the THS code and name; empty string on failure (caller falls back to the raw code).
// matchTHSBoardCode maps an incoming sector code to a THS board code. The input may
// already be a THS code (308xxx/881xxx, from sector_scanner) or an EastMoney BK code.
func (dc *DataCoordinator) matchTHSBoardCode(sectorCode string) (string, string) {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	// 1) 直接精确匹配（入参已是同花顺代码）
	// English: 1) Direct exact match (input is already a THS code).
	for _, sec := range dc.sectorCache {
		if sec.Code == sectorCode {
			return sec.Code, sec.Name
		}
	}
	// 2) 东财 BK 代码 → 剥离前缀尝试数字段（BK0477 ↔ 同花顺 885477 偶有对应）
	// English: 2) EastMoney BK code → strip the prefix and try the numeric part (BK0477 ↔ THS 885477 occasionally correspond).
	if strings.HasPrefix(sectorCode, "BK") {
		try := strings.TrimPrefix(sectorCode, "BK")
		for _, sec := range dc.sectorCache {
			if sec.Code == try {
				return sec.Code, sec.Name
			}
		}
	}
	// 3) 名称匹配：东财板块代码在 sectorCache 中对应的名称去匹配同花顺板块
	// English: 3) Name matching: use the name of an EastMoney code in sectorCache to match a THS sector.
	for _, sec := range dc.sectorCache {
		if sec.Code == sectorCode && sec.Name != "" {
			for _, sec2 := range dc.sectorCache {
				if sec2.Name == sec.Name && sec2.Code != sec.Code {
					return sec2.Code, sec2.Name
				}
			}
		}
	}
	return "", ""
}

// GetStockMoneyFlow 获取资金流向。仅东财。
// English: GetStockMoneyFlow fetches capital flow. EastMoney only.
// GetStockMoneyFlow fetches capital flow (EastMoney only).
func (dc *DataCoordinator) GetStockMoneyFlow(code string) (*CapitalFlow, error) {
	return dc.eastMoney.GetStockMoneyFlow(code)
}

// GetIndexData 获取指数行情。
// English: GetIndexData fetches index quotes.
// GetIndexData fetches the index data via EastMoney.
func (dc *DataCoordinator) GetIndexData() (indexPrice float64, ma20 float64, upCount, downCount int, err error) {
	return dc.eastMoney.GetIndexData()
}

// CrossCheckPrice 用东财 push2 获取个股价格，用于信号复核。
// English: CrossCheckPrice fetches a stock price via EastMoney push2 for signal cross-checking.
// CrossCheckPrice returns a price via EastMoney push2 for signal cross-checking.
//
// §LOW(a) 现状标注（20260922 修复批 G，交主代理裁决，勿顺手删）：
//   - 全仓 grep 显示本方法当前**零生产消费者**（仅定义处命中），属"死代码但保留"：
//     按本仓"实现优先于删除"规范未删除，等待 owner 裁决接线（信号复核链路启用）或删除；
//   - 名不副实提示：注释写"仅东财"，但实际调用的 GetRealtimeQuote 内部已自带
//     新浪→腾讯→东财 多源链（market.go §S4），故其并非真正的东财单点（SPOF 三方法
//     兜底批不含它，理由见修复报告）。
func (dc *DataCoordinator) CrossCheckPrice(code string) (price float64, err error) {
	si, err := dc.eastMoney.GetRealtimeQuote(code)
	if err != nil || si == nil {
		return 0, err
	}
	return si.Price, nil
}

// SourceName 返回最近一次成功命中的行情源名称。
// 在 GetQuote 任意降级链路命中后更新；从未命中过行情时返回空串。
// English: returns the name of the last successful quote source; empty if no quote has ever hit.
func (dc *DataCoordinator) SourceName() string {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.lastSource
}

// setLastSource 在锁内更新最近一次成功命中的行情源名称（线程安全）。
// English: updates the last successful quote source under lock.
func (dc *DataCoordinator) setLastSource(src string) {
	dc.mu.Lock()
	dc.lastSource = src
	dc.mu.Unlock()
}

// GetAuctionData 获取集合竞价数据。
// English: GetAuctionData fetches pre-open auction data.
// GetAuctionData fetches pre-open auction data via EastMoney.
func (dc *DataCoordinator) GetAuctionData(code string) (*StockInfo, error) {
	return dc.eastMoney.GetAuctionData(code)
}

// GetHotNews 多源合并获取热门新闻，按 pageSize 截顶返回。
// 同花顺快讯(主源) → 新浪财经(兜底)
// English: GetHotNews merges hot news across sources, capped at pageSize.
// English: THS flash news (primary) → Sina Finance (fallback).
// GetHotNews merges hot news across sources (THS primary → Sina fallback),
// deduplicating by truncated titles and capping at pageSize.
func (dc *DataCoordinator) GetHotNews(pageSize int) []NewsItem {
	seen := make(map[string]bool)
	var all []NewsItem

	// 主源（同花顺快讯）先入列：拉取失败或空结果静默跳过，交给下面的新浪兜底。
	// 去重键取标题前 60 字：只在 60 字之后才不同的长标题视作同一条，避免重复喂给下游。
	if items, err := dc.eastMoney.GetTonghuashunNews(pageSize); err == nil && len(items) > 0 {
		for _, n := range items {
			key := truncateStr(n.Title, 60)
			if !seen[key] {
				seen[key] = true
				all = append(all, n)
			}
		}
	}

	// 兜底源（新浪财经）：与主源共用 seen，撞题时保留主源那条，
	// 主源整轮失败时这里仍能凑出可用新闻，不至于让新闻链路空转。
	if items, err := dc.eastMoney.GetSinaNews(pageSize); err == nil && len(items) > 0 {
		for _, n := range items {
			key := truncateStr(n.Title, 60)
			if !seen[key] {
				seen[key] = true
				all = append(all, n)
			}
		}
	}

	if len(all) > pageSize {
		all = all[:pageSize]
	}
	if len(all) == 0 {
		log.Printf("GetHotNews: 所有新闻源均无数据")
	}
	return all
}

// truncateStr 将字符串按 rune 截断到 maxLen 长度（保留中文字符完整性）。
// 用于新闻标题去重的归一化 key 生成。
// English: truncateStr truncates a string to maxLen runes (preserving multi-byte Chinese characters intact).
// English: Used to build normalized dedup keys for news titles.
// truncateStr truncates a string to maxLen runes (keeping multi-byte Chinese
// characters intact), used to build normalized dedup keys for news titles.
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) // 按 rune 截断，保留多字节中文字符完整
	}
	return s
}

// RefreshIPOCalendar 刷新新股日历缓存。同花顺(板块丰富) → 东财
// English: RefreshIPOCalendar refreshes the IPO-calendar cache. THS (rich sectors) → EastMoney.
// RefreshIPOCalendar refreshes the IPO-calendar cache (5min TTL),
// populating from EastMoney then enriching each record's sector.
//
// §GAP-20260826 行情卡顿根修：旧实现全程持有 dc.mu（保护行情降级链/板块缓存的同一把全局锁）
// 执行东财日历拉取 + enrichIPOSector 逐条串行 HTTP——一次慢刷新（数秒~数十秒）会阻塞所有
// goroutine 的 GetQuote/GetKLine/GetSectors，fetcher 5s 节拍被卡死 → 前端股价冻结/刷新极慢。
// 新实现三段式：①锁内仅做 TTL 快检；②网络取数与板块丰富全部在锁外；③锁内只做指针换装(O(1))。
// 另加 atomic 防并发刷新风暴 + 失败 60s 负缓存（旧实现失败不推进 TTL，上游故障时每个请求都重打全量接口）。
// English: Root fix for quote latency: the old version held dc.mu (the same global lock guarding the
// English: quote fallback chain / sector caches) while doing the EastMoney calendar fetch plus a serial
// English: per-IPO enrichment — one slow refresh stalled every GetQuote/GetKLine/GetSectors caller and
// English: froze the fetcher's 5s cadence. Now: ① TTL fast-path under lock; ② all network IO outside the
// English: lock; ③ O(1) pointer swap under lock. Plus an atomic in-flight guard against refresh storms and
// English: a 60s negative cache on failure (the old code left ipoCacheAt untouched, so an upstream outage
// English: re-hit the full API on every single request).
func (dc *DataCoordinator) RefreshIPOCalendar() {
	// ① 快路径：TTL 未到期直接返回（锁内只读两个时间字段，零网络）。
	// English: ① Fast path: return immediately when the cache is still fresh (lock held only to read two fields, zero network).
	dc.mu.Lock()
	if !dc.ipoCacheAt.IsZero() && time.Since(dc.ipoCacheAt) < 5*time.Minute {
		dc.mu.Unlock()
		return
	}
	dc.mu.Unlock()

	// 防并发：同一时刻只允许一个刷新在跑，其余调用方直接放弃（它们下轮自然拿到新缓存或再触发）。
	// English: Single-flight guard: only one refresh runs at a time; concurrent callers bail out and
	// English: will observe the refreshed cache on a subsequent call.
	if !dc.ipoRefreshing.CompareAndSwap(false, true) {
		return
	}
	defer dc.ipoRefreshing.Store(false)

	// ② 网络取数 + 板块丰富：全部在 dc.mu 之外执行，绝不阻塞行情链路。
	// English: ② Network fetch + sector enrichment run entirely outside dc.mu, never blocking the quote path.
	var list []IPOEvent
	if dc.eastMoney != nil {
		l, err := dc.eastMoney.GetEastMoneyIPOCalendar()
		if err == nil && len(l) > 0 {
			list = l
		} else if err != nil {
			log.Printf("IPO日历 东财 失败: %v", err)
		}
	}
	if len(list) == 0 {
		// 失败负缓存：推进 TTL 60s，避免上游故障期间每个请求都重打全量日历接口。
		// English: Negative cache on failure: advance the TTL by 60s so an upstream outage doesn't
		// English: turn every request into a full calendar refetch.
		dc.mu.Lock()
		dc.ipoCacheAt = time.Now().Add(-5*time.Minute + 60*time.Second)
		dc.mu.Unlock()
		return
	}
	dc.enrichIPOSector(list)

	// ③ 发布：锁内只做指针换装与 TTL 推进（O(1)，微秒级）。
	// English: ③ Publish: pointer swap + TTL bump under the lock (O(1), microseconds).
	dc.mu.Lock()
	dc.ipoCache = list
	dc.ipoCacheAt = time.Now()
	dc.mu.Unlock()
	log.Printf("IPO日历: 东财加载 %d 条", len(list))
}

// enrichIPOSector 为新股日历事件补充所属行业板块。
// 逐条调用东财行业查询接口（GetStockIndustry），缺失行业的事件保留空值。
// English: enrichIPOSector fills each IPO event's industry sector.
// English: Calls EastMoney's GetStockIndustry per event; events without a sector keep an empty value.
// enrichIPOSector fills each IPO event's sector via EastMoney's GetStockIndustry;
// events with no sector keep an empty value.
func (dc *DataCoordinator) enrichIPOSector(list []IPOEvent) {
	if dc.eastMoney == nil {
		return
	}
	for i := range list {
		sector := dc.eastMoney.GetStockIndustry(list[i].Code)
		if sector != "" {
			list[i].Sector = sector
		}
	}
	hasSector := 0
	for _, ev := range list {
		if ev.Sector != "" {
			hasSector++
		}
	}
	log.Printf("IPO日历: 板块填充 %d/%d 只", hasSector, len(list))
}

// GetIPOByCode 按股票代码查询新股日历事件。
// English: GetIPOByCode looks up an IPO-calendar event by stock code.
// GetIPOByCode looks up an IPO event by stock code, refreshing the cache if empty.
func (dc *DataCoordinator) GetIPOByCode(code string) *IPOEvent {
	dc.mu.RLock()
	cache := dc.ipoCache
	dc.mu.RUnlock()

	if len(cache) == 0 {
		dc.RefreshIPOCalendar()
		dc.mu.RLock()
		cache = dc.ipoCache
		dc.mu.RUnlock()
	}

	for i := range cache {
		if cache[i].Code == code {
			return &cache[i]
		}
	}
	return nil
}

// GetAllIPOCalendar 返回全部新股日历数据。
// English: GetAllIPOCalendar returns all IPO-calendar data.
// GetAllIPOCalendar returns all IPO-calendar data, refreshing when empty.
func (dc *DataCoordinator) GetAllIPOCalendar() []IPOEvent {
	dc.mu.RLock()
	cache := dc.ipoCache
	dc.mu.RUnlock()

	if len(cache) == 0 {
		dc.RefreshIPOCalendar()
		dc.mu.RLock()
		cache = dc.ipoCache
		dc.mu.RUnlock()
	}

	return cache
}

// GetStockSector 查询个股所属板块名称。
// English: GetStockSector returns the sector name of a stock.
// GetStockSector returns the sector name of a stock via EastMoney.
func (dc *DataCoordinator) GetStockSector(code string) string {
	if dc.eastMoney != nil {
		return dc.eastMoney.GetStockIndustry(code)
	}
	return ""
}

// TushareToken 保留以供前端初始化配置页面展示（已不再实际使用）。
// English: TushareToken is kept for display on the frontend config page (no longer used).
// TushareToken is kept only for display on the frontend config page (no longer used).
func TushareToken() string {
	return os.Getenv("TUSHARE_TOKEN")
}
