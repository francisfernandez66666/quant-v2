// qmt_client.go — 国内 Windows 网关（东莞证券 MiniQMT）HTTP 客户端。
// 首尔侧调用网关 REST 接口（/order /cancel /state /health）执行真实下单/查询，
// Bearer token 双向鉴权，超时 + 有限重试。下单幂等由上层以 signal_id 唯一键保证。
// English: HTTP client for the domestic Windows gateway (Guoxin MiniQMT). Calls the gateway REST
// endpoints (/order /cancel /state /health) to place/query real orders with Bearer-token auth,
// timeouts and limited retries. Idempotency is guaranteed upstream via the signal_id unique key.
package trading

import (
	"bytes"
	"context"
	"encoding/json"
	"errors" // §0925EVE-W3-E：errors.As 分类网关 4xx 直败
	"fmt"
	"io"
	"log" // §0925EVE-W3-E：/admin/status 不可达时的读不到留痕告警
	"net"
	"net/http"
	"strings"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// QMTClient 网关 HTTP 客户端。
// English: QMTClient is the gateway HTTP client.
type QMTClient struct {
	baseURL    string        // 网关地址（如 https://<IP>:8789）
	token      string        // Bearer token
	timeout    time.Duration // 请求超时
	retries    int           // 失败重试次数（幂等场景安全）
	httpClient *http.Client  // HTTP 客户端
}

// NewQMTClient 创建网关客户端。
// Transport 细粒度超时（§ROBUST）：跨网链路故障常表现为「连接挂起」而非快速失败——
// 拨号 5s / TLS 握手 5s / 响应头独立限时，避免整体 Timeout 之前长时间占用探测与下单路径。
// English: NewQMTClient builds the gateway client with fine-grained transport timeouts so that a
// hanging cross-border link fails fast on dial/TLS instead of stalling until the overall timeout.
func NewQMTClient(baseURL, token string, timeout time.Duration, retries int) *QMTClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &QMTClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		timeout:    timeout,
		retries:    retries,
		httpClient: &http.Client{Timeout: timeout, Transport: transport},
	}
}

// do 执行带鉴权的 JSON 请求并解码响应。
// English: do sends an authenticated JSON request and decodes the response.
func (c *QMTClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// §0925EVE-W3-E（C8）：非 200 错误从裸 fmt.Errorf 升级为带状态码的类型化错误——
		// order() 需要区分「网关权威判定直败的 4xx」与「可重试的瞬态（网络错误/5xx/408/429）」，
		// 字符串里虽然仍带 "HTTP %d"（对外错误文案不变），但判据不许再靠 Contains 猜。
		// English: §0925EVE-W3-E — non-200 now carries the status code as a typed error so the
		// retry policy can classify deterministic 4xx rejections without string sniffing.
		return &gatewayHTTPError{Method: method, Path: path, Status: resp.StatusCode, Body: truncate(string(data), 200)}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode gateway response: %w: %s", err, truncate(string(data), 200))
		}
	}
	return nil
}

// gatewayHTTPError §0925EVE-W3-E（C8）：网关非 200 响应的类型化错误（携带 HTTP 状态码）。
// 文案与旧 fmt.Errorf 完全同构（"gateway METHOD PATH: HTTP nnn: body"），下游按
// strings.Contains 匹配错误文本的旧习惯不受影响；新增的 Status 字段只服务重试分类。
// English: typed non-200 gateway error carrying the status code; message text unchanged.
type gatewayHTTPError struct {
	Method string
	Path   string
	Status int
	Body   string
}

// Error 实现 error 接口（文案保持与改造前逐字一致）。
func (e *gatewayHTTPError) Error() string {
	return fmt.Sprintf("gateway %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// isDeterministicGatewayRejection §0925EVE-W3-E（C8）：该错误是否为「重试也不会变好」的
// 网关确定性拒绝。判据按惯例钉死：
//   - 网络错误/超时/5xx（网关 §G8 兜底异常也是 500 JSON）→ 瞬态，维持现状可重试；
//   - 408 Request Timeout、429 Too Many Requests → 4xx 里的惯例可重试码（HTTP 语义即
//     "稍后再试"），不排除出去会把限流场景的瞬态误判为直败；
//   - 其余 4xx → 网关权威判定直败：§G2 空 signal_id 400、§SIDEGATE-PY 方向非法 400、
//     §H1 日期口径 400、§CLAIMRELEASE 同信号在途 409……gateway.py 对 4xx 的口径就是
//     "确定性拒绝、不重试"（见其文件头与文件桥回报分支注释），Go 侧重试只会空转二次请求。
//
// English: deterministic 4xx (except 408/429) = gateway authoritative rejection, retrying is
// pure spinning; network errors and 5xx stay retryable as before.
func isDeterministicGatewayRejection(err error) bool {
	var he *gatewayHTTPError
	if !errors.As(err, &he) {
		return false // 非 HTTP 状态错误（连接失败/超时/解码失败）：按瞬态留给重试
	}
	switch he.Status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false // 惯例"稍后再试"码，不算确定性拒绝
	}
	return he.Status >= 400 && he.Status < 500
}

// truncate 截断超长文本（日志/错误展示用）。
// （truncate clips long text for logs/errors.）
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// PlaceBuy 买入下单。
// （PlaceBuy sends a buy order.）
func (c *QMTClient) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	return c.order(req)
}

// PlaceSell 卖出下单。
// （PlaceSell sends a sell order.）
func (c *QMTClient) PlaceSell(req OrderRequest) (*OrderResult, error) {
	return c.order(req)
}

// order 统一下单入口（buy/sell），带有限重试。
// English: unified order entry (buy/sell) with limited retries.
// §0925EVE-W3-E（C8）重试语义收敛：只对「瞬态」失败重试——网络错误/超时/5xx/408/429；
// 其余 4xx 是网关的权威确定性拒绝（400 参数非法、409 同 signal_id 在途占位等），
// 重试只是对同一具必败请求的空转二次投递（旧实现对一切非 200 一律重试 retries 次）。
// 幂等三层防线（signal_id claim 唯一键 + 派发队列部分唯一索引 + 熔断）仍是防双单的
// 最后兜底，本改动不触碰它们——只是不再制造无意义的重复窗口。
// English: §0925EVE-W3-E — only transient failures retry; deterministic 4xx fails fast.
// The three-layer idempotency defense remains the last line of defense; this only stops the
// pointless second spin of a doomed request.
func (c *QMTClient) order(req OrderRequest) (*OrderResult, error) {
	attempts := c.retries + 1
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		var out struct {
			OK      bool   `json:"ok"`
			OrderID string `json:"order_id"`
			Err     string `json:"err"`
		}
		err := c.do(ctx, http.MethodPost, "/order", req, &out)
		cancel()
		if err != nil {
			lastErr = err
			// §0925EVE-W3-E：确定性 4xx 直败（错误原样透传，网关 err 文案在 Body 里）。
			if isDeterministicGatewayRejection(err) {
				return nil, err
			}
			// §ROBUST 线性退避（250ms×序号）：跨网瞬断时紧背靠背重试只会三连败，
			// 给链路一点喘息；下单幂等由 signal_id 唯一键保证，重试安全。
			if i+1 < attempts {
				time.Sleep(time.Duration(250*(i+1)) * time.Millisecond)
			}
			continue
		}
		return &OrderResult{OK: out.OK, OrderID: out.OrderID, Err: out.Err}, nil
	}
	return nil, fmt.Errorf("gateway order after %d attempts: %v", attempts, lastErr)
}

// Cancel 撤单。
// （Cancel cancels an order.）
func (c *QMTClient) Cancel(orderID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.do(ctx, http.MethodPost, "/cancel", map[string]string{"order_id": orderID}, nil)
}

// State 查询网关状态与持仓（对账源）。失败自动重试一次（§ROBUST：对账是周期任务，
// 单次网络抖动不值得让整轮对账失败）。
func (c *QMTClient) State() (*GatewayState, error) {
	st, err := c.stateOnce()
	if err != nil {
		time.Sleep(400 * time.Millisecond)
		return c.stateOnce()
	}
	return st, nil
}

// stateOnce 单次查询网关状态与持仓（不重试），State 的重试语义在其调用方实现。
// English: single-shot gateway state/positions query (no retry); retry semantics live in State().
func (c *QMTClient) stateOnce() (*GatewayState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	var raw struct {
		Connected bool                 `json:"connected"`
		Account   string               `json:"account"`
		Positions []store.RealPosition `json:"positions"`
		Orders    []store.RealOrder    `json:"orders"`
	}
	if err := c.do(ctx, http.MethodGet, "/state", nil, &raw); err != nil {
		return nil, err
	}
	return &GatewayState{
		Connected: raw.Connected,
		Account:   raw.Account,
		Positions: raw.Positions,
		Orders:    raw.Orders,
	}, nil
}

// Health 探测网关健康。
// §GAP2-W1 语义修复：同时要求 ok=true 与 broker_connected=true——旧实现只解析 {ok,ts}，
// 把 broker_connected 字段直接丢弃；"Python 进程活着但 xtquant 通道已断"的场景会误判健康，
// 熔断不触发，新单连续打进 503 并批量制造幽灵占位行（网关侧 /order 会拒绝，但首尔侧已落库计预算）。
// 对旧版网关（无该字段）保持兼容：字段缺省 false 会触发熔断——这是安全侧失效（fail-safe），
// 部署侧应同步升级 qmt_gateway。
// English: §GAP2-W1 semantic fix: health requires BOTH ok=true and broker_connected=true. The old
// parser dropped broker_connected, so "Python alive but xtquant channel dead" looked healthy — the
// breaker never tripped and new orders kept hitting gateway 503s while ghost placeholders piled up
// in Seoul. Legacy gateways without the field now fail closed (fail-safe); upgrade qmt_gateway accordingly.
// Health 探测网关健康（失败自动重探一次，§ROBUST：跨网探测抖动缓冲，避免单次
// 丢包就计入熔断窗口/触发告警）。语义见 healthOnce 注释。
// English: probes gateway health with a single automatic re-probe on error to absorb
// transient cross-border jitter.
func (c *QMTClient) Health() (bool, error) {
	ok, err := c.healthOnce()
	if err != nil {
		time.Sleep(400 * time.Millisecond)
		return c.healthOnce()
	}
	return ok, nil
}

// healthOnce 单次探测网关健康（不重试）；语义见 Health 注释，重试由 Health 负责。
// English: single-shot gateway health probe (no retry); see Health for semantics and retry.
func (c *QMTClient) healthOnce() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	var out struct {
		OK              bool   `json:"ok"`
		BrokerConnected bool   `json:"broker_connected"`
		TS              string `json:"ts"`
	}
	if err := c.do(ctx, http.MethodGet, "/health", nil, &out); err != nil {
		return false, err
	}
	return out.OK && out.BrokerConnected, nil
}

// GatewayBrokerStatus 网关双路径状态（§QMT-DUAL）：active 通道 + xt/queued 各自在线态。
// English: dual-path gateway status — the active broker and per-channel liveness.
type GatewayBrokerStatus struct {
	OK              bool   `json:"ok"`
	Broker          string `json:"broker"`
	BrokerMode      string `json:"broker_mode"`
	BrokerConnected bool   `json:"broker_connected"`
	XTConnected     bool   `json:"xt_connected"`
	QueuedConnected bool   `json:"queued_connected"`
	// FailoverEnable/Dispatch §0925EVE-W3-E（C4）：自动翻转开关与派发队列统计只在网关
	// GET /admin/status 下发（gateway.py _do_admin_status），/health 从不携带——旧实现从
	// /health 解析这两个 omitempty 字段，结果 admin 面板双通道可观测面恒空。改读
	// /admin/status 后 FailoverEnable 用指针三态：nil=该行没读到，false=读到且为关，
	// 绝不允许「读不到」用零值冒充（§M2 未知不装新鲜的同族口径）。
	// English: §0925EVE-W3-E — failover_enable/dispatch are only served by /admin/status;
	// the old /health parser left the admin observability panel permanently empty. A nil
	// pointer now honestly means "not readable", never a zero-value masquerade.
	FailoverEnable *bool `json:"failover_enable,omitempty"`
	Dispatch       any   `json:"dispatch,omitempty"`
	// AdminStatusOK /admin/status 两键读取是否成功（false=不可达/鉴权失败，见 AdminStatusErr）。
	AdminStatusOK bool `json:"admin_status_ok"`
	// AdminStatusErr 读取失败原因（成功时省略）；供前端区分"没有这功能"与"暂时读不到"。
	AdminStatusErr string `json:"admin_status_error,omitempty"`
}

// BrokerStatus 查询网关 active 通道与双路径状态。
// §QMT-DUAL：供 admin 切换按钮读取当前执行路径（miniqmt=xt / qmt=queued）。
// §0925EVE-W3-E（C4）拆两腿取数：
//   - GET /health：ok/broker/broker_connected/xt_connected/queued_connected 等基础字段，
//     解析语义原样保留（此腿失败=整个状态查询失败，与旧行为一致，调用方按 ok:false 展示"不可达"）；
//   - GET /admin/status：failover_enable/dispatch 两键（带既有 Bearer token 机制，do() 统一注入）。
//     此腿失败**不**拖垮基础读数，但把「读不到」显式标记（AdminStatusOK=false + AdminStatusErr
//     留痕 + warn 日志），绝不用零值冒充「自动翻转=关/队列=空」——网关侧观察位惯例是
//     "log + /admin/status 要人去看"，Go 侧对齐为可见的未知态而非静默假绿。
//
// English: §0925EVE-W3-E — base liveness still comes from /health (failure = whole call fails,
// unchanged); failover_enable/dispatch now come from /admin/status with the existing bearer auth.
// An unreachable /admin/status is surfaced as an explicit "not readable" marker + warn log
// instead of zero values pretending "failover off / queue empty".
func (c *QMTClient) BrokerStatus() (*GatewayBrokerStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	var out GatewayBrokerStatus
	if err := c.do(ctx, http.MethodGet, "/health", nil, &out); err != nil {
		return nil, err
	}
	// 兼容新老网关：broker_mode 是 broker 的别名，缺省回退 broker
	if out.Broker == "" {
		out.Broker = out.BrokerMode
	}
	// §0925EVE-W3-E 第二腿：/admin/status 专供双通道可观测两键（网关只在这个端点发）。
	admCtx, admCancel := context.WithTimeout(context.Background(), c.timeout)
	defer admCancel()
	var adm struct {
		OK             bool `json:"ok"`
		FailoverEnable bool `json:"failover_enable"`
		Dispatch       any  `json:"dispatch"`
	}
	if err := c.do(admCtx, http.MethodGet, "/admin/status", nil, &adm); err != nil {
		out.AdminStatusOK = false
		out.AdminStatusErr = truncate(err.Error(), 200)
		log.Printf("[qmt] BrokerStatus：/admin/status 不可达，failover_enable/dispatch 读不到（不以待定值冒充）: %v", err)
		return &out, nil
	}
	out.AdminStatusOK = true
	failover := adm.FailoverEnable
	out.FailoverEnable = &failover // 指针落值：即使 false 也会序列化出来，与"没读到"区分
	if adm.Dispatch != nil {
		out.Dispatch = adm.Dispatch
	}
	return &out, nil
}

// SwitchBroker 切换网关 active 通道（POST /admin/broker，broker ∈ xt|queued）。
// §QMT-DUAL：admin 兜底切换入口；仅切换网关侧，量仔契约不变。
// English: switches the gateway's active broker (xt|queued) via /admin/broker.
func (c *QMTClient) SwitchBroker(broker string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.do(ctx, http.MethodPost, "/admin/broker", map[string]string{"broker": broker}, nil)
}

// SettlementTrade 券商交割单单笔成交（§WS-B 三方对账的券商权威源）。
// English: one broker settlement trade (the broker-authoritative leg of three-way reconciliation).
type SettlementTrade struct {
	OrderID  string  `json:"order_id"`  // 委托号
	TsCode   string  `json:"ts_code"`   // 代码
	Side     string  `json:"side"`      // 买入/卖出
	Price    float64 `json:"price"`     // 价格
	Qty      int     `json:"qty"`       // 数量
	Amount   float64 `json:"amount"`    // 金额
	Fee      float64 `json:"fee"`       // 手续费
	StampTax float64 `json:"stamp_tax"` // 印花税
	Serial   string  `json:"serial"`    // 交割流水号
	TradedAt string  `json:"traded_at"` // 成交时间
	// SignalID §0925EVE-W3-E（C5）：网关 /settlement 行一直携带的真实归因信号
	// （qmt_gateway/store.py settlement_trades 从 fills 表 SELECT signal_id）。旧结构没有该
	// tag，HTTP 解码时把它静默丢弃，sync_fills 补记只能写虚构键 "settle:"+day——
	// 交割纠偏出来的成交在盈亏归因（signal_id→战法）里全部错位。补上字段即接通归因链。
	// English: §0925EVE-W3-E — the gateway settlement rows always carried the real signal_id;
	// the old struct dropped it, so backfills got fabricated keys and lost strategy attribution.
	SignalID string `json:"signal_id"`
}

// SettlementResponse 网关 /settlement 响应（§WS-B）。
// English: gateway /settlement response.
type SettlementResponse struct {
	Date      string             `json:"date"`
	Account   string             `json:"account"`
	Trades    []SettlementTrade  `json:"trades"`
	Cash      map[string]float64 `json:"cash"`
	Connected bool               `json:"connected"`
}

// FetchSettlement 拉取券商交割单（GET /settlement?date=YYYY-MM-DD，§WS-B 三方对账权威源）。
// §H1（2026-09-22）注释纠错：旧注释写 `date=YYYYMMDD`，与网关校验（gateway.py 只收
// YYYY-MM-DD）相反，正是自动对账恒 400 的误导源；调用方经 SettleDay 入口已归一为带杠口径。
// English: fetches the broker settlement for a day (three-way reconciliation authoritative leg);
// the gateway only accepts YYYY-MM-DD.
func (c *QMTClient) FetchSettlement(date string) (*SettlementResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	var out SettlementResponse
	if err := c.do(ctx, http.MethodGet, "/settlement?date="+date, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Quotes §ENH-5 批E：拉取 Level-1 全推行情（GET /quotes?codes=...）。
// 入参/返回 key 均为裸 6 位码（网关侧代码带 .SH/.SZ/.BJ 后缀，收发两头做归一）；
// 走 do() 复用 Bearer/transport/错误格式，但**不重试**——feed 是高频轮询，
// 瞬断重试只会放大请求量，丢一轮由 1~3s 后的下一轮自然补。
// English: §ENH-5 batch-E Level-1 feed. Bare-code in/out (suffix added for the gateway call);
// no retries — the 1-3s poll loop self-heals, retrying would only amplify request volume.
func (c *QMTClient) Quotes(ctx context.Context, codes []string) (map[string]data.QMTTick, error) {
	if len(codes) == 0 {
		return map[string]data.QMTTick{}, nil
	}
	suffixed := make([]string, 0, len(codes))
	for _, code := range codes {
		suffixed = append(suffixed, data.ExchangeSuffix(code)) // 已带后缀原样返回，未带按 6→SH/4·8·920→BJ/其余→SZ
	}
	var out struct {
		OK    bool                    `json:"ok"`
		Ticks map[string]data.QMTTick `json:"ticks"`
	}
	path := "/quotes?codes=" + strings.Join(suffixed, ",")
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	ticks := make(map[string]data.QMTTick, len(out.Ticks))
	for k, v := range out.Ticks {
		if i := strings.IndexByte(k, '.'); i > 0 {
			k = k[:i] // 带后缀 key → 裸码，与 Fetcher.Stocks 的键口径一致
		}
		ticks[k] = v
	}
	return ticks, nil
}
