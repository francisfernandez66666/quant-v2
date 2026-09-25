// qmt_admin.go — §0925EVE-W3-G（FIX_PLAN ⑫ C3）：网关「第三态人工收敛」管理客户端。
//
// 背景：网关（qmt_gateway/gateway.py §CLAIMRELEASE）对「已交给通道、但结算结果不明」的
// 委托保留 status=待核对 的第三态占位行，并提供两个 admin 端点：
//
//	GET  /admin/status        观察位 unresolved_orders（待核对清单，网关侧最多回 20 条，
//	                          unresolved_count 为全量计数）+ dispatch_in_flight 取证位；
//	POST /admin/order-confirm 人工二选一收敛：released（柜台确无此单→删占位解锁）/
//	                          settled（柜台有此单→改写为正常终态，可回填 order_id）。
//
// 这两个端点此前在 Go/前端**零接线**（grep 全仓空），人工核对只能拿 curl 打网关——
// 本文件按 qmt_client.go 的既有体例（Bearer token、transport 细粒度超时、错误带响应体）
// 补上带鉴权的调用客户端，并挂到 Controller 上供 server 层 admin 端点消费。
// 纪律：不修改 qmt_client.go——adminDo 在本文件独立实现，因为 order-confirm 的 4xx 响应
// 体（{"ok":false,"err":...}）是**需要结构化回传**的业务结论，不能像下单那样只当作错误文本。
// English: tokenized admin client for the gateway's third-state (unresolved "待核对")
// reconciliation endpoints — the pending-review list and the human order-confirm exit.
package trading

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// adminListTruncateLimit 网关 /admin/status 对 unresolved_orders 的截断上限
// （gateway.py: `for r in unresolved[:20]`）。Go 侧据此把 truncated 显式告诉前端，
// 避免「清单只有 20 条」被误读成「一共只有 20 条」。
const adminListTruncateLimit = 20

// PendingReviewOrder 一条第三态「待核对」委托（/admin/status unresolved_orders 行）。
// English: one unresolved (third-state) order row from the gateway admin status.
type PendingReviewOrder struct {
	SignalID  string `json:"signal_id"`  // 下单幂等锚点（网关确认操作的唯一键）
	Code      string `json:"code"`       // 证券代码
	Side      string `json:"side"`       // 买入/卖出
	Qty       int64  `json:"qty"`        // 委托数量（网关缺省/异常时为 0，如实透传）
	CreatedAt string `json:"created_at"` // 占位创建时间（超龄判断依据）
	// DispatchInFlight 该信号是否仍在派发队列：true=桥迟早回报、可自动收敛；
	// false=只能人工核对柜台后走 order-confirm 改判（取证位，面板按此提示处置建议）。
	DispatchInFlight bool `json:"dispatch_in_flight"`
}

// PendingReviewResult /admin/status 的待核对视图（只取人工收敛所需字段，其余观察位不外泄）。
// English: the pending-review view extracted from gateway /admin/status.
type PendingReviewResult struct {
	Orders         []PendingReviewOrder `json:"orders"`
	TotalCount     int                  `json:"unresolved_count"` // 网关全量计数（可能大于 Orders 长度）
	Truncated      bool                 `json:"truncated"`        // Orders 被网关截断到前 20 条
	Active         string               `json:"active,omitempty"` // 当前 active 通道（xt/queued），辅助取证
	GatewayTS      string               `json:"gateway_ts,omitempty"`
	FailoverEnable bool                 `json:"failover_enable"` // 自动兜底切换是否开启
}

// OrderConfirmRequest 人工确认请求（对应网关 /admin/order-confirm body）。
// English: the manual confirm request mapped onto the gateway order-confirm body.
type OrderConfirmRequest struct {
	SignalID string `json:"signal_id"`          // 待核对单锚点（前端契约里叫 wire_ref，映射到这里）
	Decision string `json:"decision"`           // released | settled
	OrderID  string `json:"order_id,omitempty"` // settled 时可选回填真实委托号
	Status   string `json:"status,omitempty"`   // settled 时可选指定终态（网关缺省「已撤」）
}

// OrderConfirmResult 网关确认结果。HTTPStatus 保留网关原始状态码（400/404/409/200），
// server 层据此如实映射响应，不把「柜台侧拒绝」洗成 200。
// English: gateway confirm outcome, carrying the raw HTTP status for honest mapping.
type OrderConfirmResult struct {
	HTTPStatus int    `json:"-"`
	OK         bool   `json:"ok"`
	Err        string `json:"err,omitempty"`
	Released   bool   `json:"released,omitempty"`
	Status     string `json:"status,omitempty"`
}

// adminDo 执行一次带 Bearer 鉴权的网关管理请求，返回（状态码, 响应体）。
// 与 qmt_client.go 的 do() 区别：**不**把非 200 折叠成 error——admin 端点的 4xx 体里
// 装着要回传前端的结构化结论（ok=false+err）；只有传输层失败/响应不可读才返回 error。
// English: authenticated admin call returning status+body verbatim (4xx bodies carry the
// structured verdict); only transport faults produce an error.
func (c *QMTClient) adminDo(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// PendingReview 查询网关待核对清单（GET /admin/status，§0925EVE-W3-G）。
// 不重试、不吞错：读取失败必须让调用方拿到明确 error（前端要区分「查询失败」与
// 「确实没有待核对单」，空数组冒充失败是本端点的第一号反模式）。
// English: fetches the unresolved list once; failures surface as errors — an empty list
// must never mask a read failure.
func (c *QMTClient) PendingReview(ctx context.Context) (*PendingReviewResult, error) {
	status, body, err := c.adminDo(ctx, http.MethodGet, "/admin/status", nil)
	if err != nil {
		return nil, fmt.Errorf("gateway %s %s: %w", http.MethodGet, "/admin/status", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("gateway GET /admin/status: HTTP %d: %s", status, truncate(string(body), 200))
	}
	// 网关 unresolved_orders 里 qty 可能为 null（sqlite 列缺值），用 json.Number 族先解通用
	// JSON 再逐字段搬运，避免整包解码因单个 null 失败。
	var raw struct {
		OK             bool   `json:"ok"`
		TS             string `json:"ts"`
		Active         string `json:"active"`
		FailoverEnable bool   `json:"failover_enable"`
		Unresolved     []struct {
			SignalID         string      `json:"signal_id"`
			Code             string      `json:"code"`
			Side             string      `json:"side"`
			Qty              json.Number `json:"qty"`
			CreatedAt        string      `json:"created_at"`
			DispatchInFlight *bool       `json:"dispatch_in_flight"` // 缺省按 false（保守：不当作"可自动收敛"）
		} `json:"unresolved_orders"`
		UnresolvedCount *int `json:"unresolved_count"` // 旧网关可能没有该键
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode gateway /admin/status: %w: %s", err, truncate(string(body), 200))
	}
	out := &PendingReviewResult{
		Orders:         make([]PendingReviewOrder, 0, len(raw.Unresolved)),
		Active:         raw.Active,
		GatewayTS:      raw.TS,
		FailoverEnable: raw.FailoverEnable,
	}
	for _, r := range raw.Unresolved {
		qty, _ := r.Qty.Int64() // null/非法值 → 0，如实透传（判定不依赖 qty）
		o := PendingReviewOrder{
			SignalID: r.SignalID, Code: r.Code, Side: r.Side,
			Qty: qty, CreatedAt: r.CreatedAt,
		}
		if r.DispatchInFlight != nil {
			o.DispatchInFlight = *r.DispatchInFlight
		}
		out.Orders = append(out.Orders, o)
	}
	if raw.UnresolvedCount != nil {
		out.TotalCount = *raw.UnresolvedCount
	} else {
		out.TotalCount = len(out.Orders) // 兼容旧网关：无计数键时以清单长度为准
	}
	// truncated：网关侧 unresolved[:20] 截断的显式化（全量计数 > 清单长度即有行没回）。
	// 边界如实登记：恰好 20 条待核对时 count==len==20，无法区分「全显示」与「无行被截」，
	// 该场景前端按「已达网关单页上限」提示即可（宁可多提示一次，不漏报清单不完整）。
	out.Truncated = out.TotalCount > len(out.Orders)
	return out, nil
}

// ConfirmOrder 人工确认一条待核对委托（POST /admin/order-confirm，§0925EVE-W3-G）。
// 网关只认第三态行、永不重发订单（§CLAIMRELEASE 语义，这里原样转发不改写）；
// 网关 4xx（参数/无此单/非待核对态）解析成 OK=false+Err 带 HTTPStatus 回传，
// 供 server 层映射审计与响应码。
// English: forwards one manual confirm; gateway 4xx verdicts are decoded (not flattened
// into transport errors) so the caller can map them honestly.
func (c *QMTClient) ConfirmOrder(ctx context.Context, req OrderConfirmRequest) (*OrderConfirmResult, error) {
	if req.SignalID == "" {
		return nil, errors.New("order-confirm: signal_id 为空")
	}
	if req.Decision != "released" && req.Decision != "settled" {
		return nil, fmt.Errorf("order-confirm: decision 必须为 released|settled，got %q", req.Decision)
	}
	status, body, err := c.adminDo(ctx, http.MethodPost, "/admin/order-confirm", map[string]string{
		"signal_id": req.SignalID,
		"decision":  req.Decision,
		"order_id":  req.OrderID,
		"status":    req.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway POST /admin/order-confirm: %w", err)
	}
	var out struct {
		OK       bool   `json:"ok"`
		Err      string `json:"err"`
		Released bool   `json:"released"`
		Status   string `json:"status"`
	}
	// 响应体必须为 JSON：解不出来按网关协议破坏处理（返回 error），
	// 绝不当作「确认成功」——这是资金安全侧的 fail-closed。
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode gateway /admin/order-confirm: HTTP %d, %w: %s", status, err, truncate(string(body), 200))
	}
	return &OrderConfirmResult{HTTPStatus: status, OK: out.OK, Err: out.Err, Released: out.Released, Status: out.Status}, nil
}

// gatewayAdminClient 构建直连网关的 admin 客户端（复用 Controller.gatewayClient 的
// base/token 配置读取方式：账号 QMT 配置的 gateway_url/token，8s 超时、零重试）。
// §0925EVE-W3-G：独立命名只是为了把「管理面调用」与「下单执行器」在语义上分开，
// 实例类型同为 *QMTClient。English: builds the admin-path gateway client from current config.
func (c *Controller) gatewayAdminClient() (*QMTClient, error) {
	gc := c.gatewayClient()
	if gc == nil {
		return nil, errors.New("qmt not enabled or gateway_url not set")
	}
	return gc, nil
}

// GatewayPendingReview 查询网关待核对清单（Controller 出口，server 层 admin 端点消费）。
// 超时口径与 broker 状态查询一致（8s），失败原因原样上抛不吞。
// English: controller-level entry for the unresolved list query.
func (c *Controller) GatewayPendingReview() (*PendingReviewResult, error) {
	gc, err := c.gatewayAdminClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gc.timeout)
	defer cancel()
	return gc.PendingReview(ctx)
}

// ConfirmGatewayOrder 转发人工确认（Controller 出口）。
// English: controller-level entry forwarding the manual confirm to the gateway.
func (c *Controller) ConfirmGatewayOrder(req OrderConfirmRequest) (*OrderConfirmResult, error) {
	gc, err := c.gatewayAdminClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gc.timeout+5*time.Second)
	defer cancel()
	return gc.ConfirmOrder(ctx, req)
}
