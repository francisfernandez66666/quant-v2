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
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"quant-trading-v2/internal/store"
)

// QMTClient 网关 HTTP 客户端。
// English: QMTClient is the gateway HTTP client.
type QMTClient struct {
	baseURL    string        // 网关地址（如 https://<IP>:8789）
	token      string        // Bearer token
	timeout    time.Duration // 请求超时
	retries    int           // 失败重试次数（幂等场景安全）
	httpClient *http.Client
}

// NewQMTClient 创建网关客户端。
// English: NewQMTClient builds a gateway client.
func NewQMTClient(baseURL, token string, timeout time.Duration, retries int) *QMTClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &QMTClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		timeout:    timeout,
		retries:    retries,
		httpClient: &http.Client{Timeout: timeout},
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
		return fmt.Errorf("gateway %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(data), 200))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode gateway response: %w: %s", err, truncate(string(data), 200))
		}
	}
	return nil
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

// State 查询网关状态与持仓（对账源）。
// English: queries gateway state and positions (reconciliation source).
func (c *QMTClient) State() (*GatewayState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	var raw struct {
		Connected bool                   `json:"connected"`
		Account   string                 `json:"account"`
		Positions []store.RealPosition   `json:"positions"`
		Orders    []store.RealOrder      `json:"orders"`
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
// （Health pings gateway /health.）
func (c *QMTClient) Health() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	var out struct {
		OK bool   `json:"ok"`
		TS string `json:"ts"`
	}
	if err := c.do(ctx, http.MethodGet, "/health", nil, &out); err != nil {
		return false, err
	}
	return out.OK, nil
}