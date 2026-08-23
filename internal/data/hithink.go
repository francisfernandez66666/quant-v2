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
