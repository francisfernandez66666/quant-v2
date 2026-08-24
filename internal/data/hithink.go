// hithink.go 同花顺（新）官方数据源客户端 —— fuyao.aicubes.cn（HiThink-Tech/Financial-API）。
//
// 定位：回测与实时数据的最高优先级数据源（docs/HITHINK_DATA_SOURCE_PLAN.md）。
// 特性：统一 ApiResponse 信封解析、令牌桶限速（QPS 官方未公布，保守 5 起步自适应）、
// 4001 频率超限指数退避、2003 权限缺失告警降级、业务码分类错误。
//
// English: official Tonghuashun (HiThink) A-share data client — top-priority source for both
// backtest pipelines and realtime quotes. Token-bucket rate limiting with adaptive backoff on
// code=4001; envelope-aware error classification per docs error-code table.
package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HithinkBaseURL 同花顺金融数据服务地址。
var HithinkBaseURL = "https://fuyao.aicubes.cn" // 可测试覆盖（var 而非 const）

// HithinkAPIKeyEnv API Key 的环境变量名（/etc/quant.env 注入，不入仓库）。
const HithinkAPIKeyEnv = "HITHINK_FINANCE_API_KEY"

// Hithink 业务错误分类（对应官方错误码表）。
var (
	// ErrHithinkAuth Key 缺失或无效（code=2001）——告警人工介入。
	ErrHithinkAuth = errors.New("hithink: 未认证(2001)，检查 HITHINK_FINANCE_API_KEY")
	// ErrHithinkForbidden 无权访问 capability（code=2003）——停用该能力并告警。
	ErrHithinkForbidden = errors.New("hithink: 权限不足(2003)")
	// ErrHithinkRateLimited 频率超限（code=4001）——调用方应退避重试。
	ErrHithinkRateLimited = errors.New("hithink: 频率超限(4001)")
)

// HithinkEnvelope 统一响应信封。
type HithinkEnvelope struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
}

// hithinkLimiter 令牌桶限速器（QPS 可运行时下调以自适应 4001）。
type hithinkLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
}

func newHithinkLimiter(qps float64) *hithinkLimiter {
	if qps <= 0 {
		qps = 5
	}
	return &hithinkLimiter{interval: time.Duration(float64(time.Second) / qps)}
}

// wait 阻塞直到取得一个令牌；penalize 在收到 4001 时按倍数拉长间隔。
func (l *hithinkLimiter) wait(penalize bool) {
	l.mu.Lock()
	wait := time.Duration(0)
	now := time.Now()
	if !l.last.IsZero() {
		next := l.last.Add(l.interval)
		if now.Before(next) {
			wait = next.Sub(now)
		}
	}
	if penalize {
		l.interval *= 2
		if l.interval > 30*time.Second {
			l.interval = 30 * time.Second // 退避上限：30s/请求
		}
		wait += l.interval
	}
	l.last = now.Add(wait)
	l.mu.Unlock()
	if wait > 0 {
		time.Sleep(wait)
	}
}

// HithinkClient 同花顺（新）数据源客户端。零值不可用，须 NewHithinkClient。
type HithinkClient struct {
	apiKey  string
	http    *http.Client
	limiter *hithinkLimiter
}

// NewHithinkClient 从环境变量读取 API Key 构建客户端；Key 缺失返回错误
// （上层据此判定"同花顺（新）"源不可用，走降级链）。
func NewHithinkClient() (*HithinkClient, error) {
	key := os.Getenv(HithinkAPIKeyEnv)
	if key == "" {
		return nil, fmt.Errorf("hithink: 环境变量 %s 未设置", HithinkAPIKeyEnv)
	}
	return &HithinkClient{
		apiKey:  key,
		http:    &http.Client{Timeout: 30 * time.Second},
		limiter: newHithinkLimiter(5),
	}, nil
}

// get 发起限速 GET 并解析统一信封；非 0 业务码转为分类错误。
// penalize=true 表示本次失败后下次调用前额外加倍等待（仅 4001 触发）。
func (c *HithinkClient) get(path string, params url.Values, out any) error {
	c.limiter.wait(false)
	req, err := http.NewRequest(http.MethodGet, HithinkBaseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-api-key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hithink: 网络错误: %w", err)
	}
	defer resp.Body.Close()
	var env HithinkEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("hithink: 响应解析失败: %w", err)
	}
	switch env.Code {
	case 0:
		if out != nil && len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("hithink: data 解析失败: %w", err)
			}
		}
		return nil
	case 4001:
		c.limiter.wait(true) // 惩罚性加倍限速间隔
		return ErrHithinkRateLimited
	case 2001:
		return ErrHithinkAuth
	case 2003:
		return ErrHithinkForbidden
	default:
		return fmt.Errorf("hithink: 业务错误 code=%d message=%s request_id=%s",
			env.Code, env.Message, env.RequestID)
	}
}

// ── 业务方法（随分期逐步补充；以下为已上线能力）──

// HithinkSnapshotItem 行情快照条目。
type HithinkSnapshotItem struct {
	ThsCode             string  `json:"thscode"`
	Ticker              string  `json:"ticker"`
	LastPrice           float64 `json:"last_price"`
	PriceChange         float64 `json:"price_change"`
	PriceChangeRatioPct float64 `json:"price_change_ratio_pct"`
	OpenPrice           float64 `json:"open_price"`
	HighPrice           float64 `json:"high_price"`
	LowPrice            float64 `json:"low_price"`
	PrevPrice           float64 `json:"prev_price"`
	Volume              float64 `json:"volume"`
	Turnover            float64 `json:"turnover"`
}

// HithinkSnapshot 行情快照数据容器。
type HithinkSnapshot struct {
	Timestamp int64                 `json:"timestamp"`
	Total     int                   `json:"total"`
	Item      []HithinkSnapshotItem `json:"item"`
}

// Snapshot 行情快照：thscodes 逗号分隔批量取（显式传入不分页）。
// 文档：GET /api/a-share/prices/snapshot
func (c *HithinkClient) Snapshot(thscodes []string) (*HithinkSnapshot, error) {
	p := url.Values{}
	p.Set("thscodes", joinThsCodes(thscodes))
	var out HithinkSnapshot
	if err := c.get("/api/a-share/prices/snapshot", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HithinkCalendarDay 交易日历条目。
type HithinkCalendarDay struct {
	DateMs int64  `json:"date_ms"`
	Date   string `json:"date"` // yyyyMMdd
}

// TradingDays 近一年交易日序列（无入参）。文档：GET /api/a-share/calendar/trading-days
func (c *HithinkClient) TradingDays() ([]HithinkCalendarDay, error) {
	var out struct {
		Item []HithinkCalendarDay `json:"item"`
	}
	if err := c.get("/api/a-share/calendar/trading-days", url.Values{}, &out); err != nil {
		return nil, err
	}
	return out.Item, nil
}

func joinThsCodes(codes []string) string {
	s := ""
	for i, c := range codes {
		if i > 0 {
			s += ","
		}
		s += c
	}
	return s
}

// ParseHithintMs 毫秒时间戳 → yyyyMMdd（Asia/Shanghai）；供 dump date_ms 与表 trade_date 换算。
func ParseHithintMs(ms int64) string {
	t := time.UnixMilli(ms).In(shanghaiLoc())
	return t.Format("20060102")
}

func shanghaiLoc() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

var _ = strconv.Itoa // 占位避免未用导入误报（strconv 供后续分页参数使用）

// ── 行情采集集成（fetcher 最高优先源）──

// hithinkFailThreshold 连续失败 N 次后标记降级（跳过该源，走后续降级链）。
const hithinkFailThreshold = 5

// hithinkProbeInterval 降级后的探活间隔（期间跳过请求，到点试一次）。
const hithinkProbeInterval = 10 * time.Minute

// HithinkSourceState 源健康状态（fetcher 持有；并发安全）。
type HithinkSourceState struct {
	mu        sync.Mutex
	failCount int
	degraded  bool
	lastTry   time.Time
}

// available 判定本次是否可用：正常或降级探活到期。
func (s *HithinkSourceState) available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.degraded {
		return true
	}
	return time.Since(s.lastTry) >= hithinkProbeInterval
}

// markSuccess 升回正常。
func (s *HithinkSourceState) markSuccess() {
	s.mu.Lock()
	s.failCount, s.degraded = 0, false
	s.mu.Unlock()
}

// markFailure 累计失败，达阈值进入降级。
func (s *HithinkSourceState) markFailure() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCount++
	s.lastTry = time.Now()
	if s.failCount >= hithinkFailThreshold && !s.degraded {
		s.degraded = true
		return true // 首次进入降级（调用方记日志/告警）
	}
	return false
}

// BatchQuotes 全池批量行情快照（StockInfo 形状，供 fetcher 直接消费）。
// 代码归一为裸码（与 sina/东财路径一致）；Volume 单位=股（与 StockInfo 注释一致）。
func (c *HithinkClient) BatchQuotes(codes []string) (map[string]*StockInfo, error) {
	if len(codes) == 0 {
		return map[string]*StockInfo{}, nil
	}
	snap, err := c.Snapshot(codes)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*StockInfo, len(snap.Item))
	for i := range snap.Item {
		it := &snap.Item[i]
		if it.LastPrice <= 0 {
			continue
		}
		out[strings.Split(it.ThsCode, ".")[0]] = &StockInfo{
			Code:      strings.Split(it.ThsCode, ".")[0],
			Price:     it.LastPrice,
			Open:      it.OpenPrice,
			High:      it.HighPrice,
			Low:       it.LowPrice,
			Close:     it.PrevPrice,
			Volume:    it.Volume,
			Amount:    it.Turnover,
			ChangePct: it.PriceChangeRatioPct,
		}
	}
	return out, nil
}

// ── 集合竞价（§P1 竞价窗口注入打分循环）──

// HithinkAuctionItem 竞价快照条目。
type HithinkAuctionItem struct {
	ThsCode            string  `json:"thscode"`
	Ticker             string  `json:"ticker"`
	Name               string  `json:"name"`
	AuctionPrice       float64 `json:"auction_price"`
	AuctionPct         float64 `json:"auction_pct"` // 竞价涨跌幅%
	AuctionVolume      float64 `json:"auction_volume"`
	AuctionAmount      float64 `json:"auction_amount"`
	AuctionUnmatched   float64 `json:"auction_unmatched"`
	AuctionTurnoverPct float64 `json:"auction_turnover_pct"`
	AuctionVolumeRatio float64 `json:"auction_volume_ratio"` // 竞价量比
	PreClosePrice      float64 `json:"pre_close_price"`
	OpenPrice          float64 `json:"open_price"`
	LastPrice          float64 `json:"last_price"`
	FloatMarketCap     float64 `json:"float_market_cap"`
}

// HithinkAuctionSnapshot 竞价快照容器（含阶段与状态）。
type HithinkAuctionSnapshot struct {
	Timestamp    int64                `json:"timestamp"`
	AuctionPhase string               `json:"auction_phase"`
	DataStatus   string               `json:"data_status"`
	Item         []HithinkAuctionItem `json:"item"`
}

// Auction 竞价快照：thscodes 批量；stage=live(实时)/final(终态)。
// 文档：GET /api/a-share/auction/snapshot
func (c *HithinkClient) Auction(thscodes []string, stage string) (*HithinkAuctionSnapshot, error) {
	p := url.Values{}
	p.Set("thscodes", joinThsCodes(thscodes))
	if stage != "" {
		p.Set("stage", stage)
	}
	var out HithinkAuctionSnapshot
	if err := c.get("/api/a-share/auction/snapshot", p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ── 特色数据：涨跌停三池 / 连板天梯 / 个股异动（§P1 盘口升级）──

// HithinkLimitUpItem 涨停池条目（含盘口级关键字段）。
type HithinkLimitUpItem struct {
	ThsCode             string  `json:"thscode"`
	Ticker              string  `json:"ticker"`
	Name                string  `json:"name"`
	IsST                bool    `json:"is_st"`
	IsNew               bool    `json:"is_new"`
	LastPrice           float64 `json:"last_price"`
	PriceChangeRatioPct float64 `json:"price_change_ratio_pct"`
	LimitUpTime         string  `json:"limit_up_time"`     // 首次封板 HH:MM
	LimitUpReason       *string `json:"limit_up_reason"`   // 涨停原因（可能 null）
	ContinueDayText     string  `json:"continue_day_text"` // 如 "5天4板"
	ContinueDayCnt      int     `json:"continue_day_cnt"`  // 连板数
	SealMoney           float64 `json:"seal_money"`        // 当前封单额（元）
	MaxSealMoney        float64 `json:"max_seal_money"`    // 峰值封单额（元）
}

// HithinkPoolPage 分页容器（三池共用形状）。
type HithinkPoolPage struct {
	Timestamp  int64 `json:"timestamp"`
	Pagination struct {
		Total int `json:"total"`
		Pages int `json:"pages"`
		Size  int `json:"size"`
		Page  int `json:"page"`
	} `json:"pagination"`
	Item []HithinkLimitUpItem `json:"item"`
}

// LimitUpPool 涨停股票池（dateMs=0 取当日；自动翻页取尽）。
// 文档：GET /api/a-share/special-data/limit-up-pool
func (c *HithinkClient) LimitUpPool(dateMs int64) ([]HithinkLimitUpItem, error) {
	return c.fetchPool("limit-up-pool", dateMs)
}

// LimitDownPool 跌停股票池。
func (c *HithinkClient) LimitDownPool(dateMs int64) ([]HithinkLimitUpItem, error) {
	return c.fetchPool("limit-down-pool", dateMs)
}

// LimitBreakPool 炸板股票池。
func (c *HithinkClient) LimitBreakPool(dateMs int64) ([]HithinkLimitUpItem, error) {
	return c.fetchPool("limit-break-pool", dateMs)
}

// fetchPool 三池通用翻页拉取。
func (c *HithinkClient) fetchPool(name string, dateMs int64) ([]HithinkLimitUpItem, error) {
	var all []HithinkLimitUpItem
	for page := 1; ; page++ {
		p := url.Values{}
		p.Set("page", strconv.Itoa(page))
		p.Set("size", "200")
		if dateMs > 0 {
			p.Set("date_ms", strconv.FormatInt(dateMs, 10))
		}
		var out HithinkPoolPage
		if err := c.get("/api/a-share/special-data/"+name, p, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Item...)
		if page >= out.Pagination.Pages || len(out.Item) == 0 {
			return all, nil
		}
	}
}

// HithinkLadderBoard 连板天梯单板位条目。
type HithinkLadderBoard struct {
	ThsCode     string `json:"thscode"`
	Ticker      string `json:"ticker"`
	Name        string `json:"name"`
	BoardNum    int    `json:"board_num"`
	SealNextDay *bool  `json:"seal_nextday"`
	SignLevel   int    `json:"sign_level"`
}

// LimitUpLadder 近 30 交易日连板天梯矩阵（无参数）。
// 文档：GET /api/a-share/special-data/limit-up-ladder
func (c *HithinkClient) LimitUpLadder() (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.get("/api/a-share/special-data/limit-up-ladder", url.Values{}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// HithinkAnomalyItem 个股异动原因条目。
type HithinkAnomalyItem struct {
	ThsCode         string   `json:"thscode"`
	StockName       string   `json:"stock_name"`
	TagName         string   `json:"tag_name"`
	AnalysisContent string   `json:"analysis_content"`
	KeywordList     []string `json:"keyword_list"`
}

// AnomalyForStocks 批量查询个股当日异动原因（≤50 只）。
// 文档：GET /api/a-share/special-data/anomaly-analysis-stock
func (c *HithinkClient) AnomalyForStocks(thscodes []string) ([]HithinkAnomalyItem, error) {
	if len(thscodes) > 50 {
		thscodes = thscodes[:50]
	}
	p := url.Values{}
	p.Set("thscodes", joinThsCodes(thscodes))
	var out struct {
		Item []HithinkAnomalyItem `json:"item"`
	}
	if err := c.get("/api/a-share/special-data/anomaly-analysis-stock", p, &out); err != nil {
		return nil, err
	}
	return out.Item, nil
}
