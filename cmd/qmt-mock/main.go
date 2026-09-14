// cmd/qmt-mock — 东莞证券 MiniQMT 网关模拟器（AUTO_TRADING_PLAN M1 mock 网关）。
// 本地联调用：模拟 Windows 端网关的 /order /cancel /state /health REST 接口，
// 下单后按 --delay 延时模拟成交，并把成交回报推送到首尔服务器 POST /api/qmt/report
// （与 mock 网关同一进程内可选的 --webhook 模式）。用于端到端联调真实网关接入链路。
//
// 用法（Usages）:
//
//	go run ./cmd/qmt-mock -listen :8789 -server http://127.0.0.1:8080 -token my-secret -delay 3000
//
// 首尔侧 qmt 配置示例（server.toml）:
//
//	[qmt]
//	enabled = true
//	gateway_url = "http://127.0.0.1:8789"
//	token = "my-secret"
//	mode = "manual"
//
// English: mock of the Guoxin MiniQMT gateway for M1 end-to-end integration testing. Serves
// /order /cancel /state /health and simulates fills after --delay, pushing trade reports back to the
// Seoul server via POST /api/qmt/report (--webhook).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// order 内存中的委托记录。
// （order is an in-memory order record.）
type order struct {
	OrderID   string  `json:"order_id"`   // 全局唯一委托 ID（MOCK000001 递增）
	SignalID  string  `json:"signal_id"`  // 关联的信号 ID（用于幂等去重）
	Code      string  `json:"code"`       // 标的代码（ts_code，如 600519.SH）
	Name      string  `json:"name"`       // 标的名称
	Strategy  string  `json:"strategy"`   // 触发信号所属战法
	Side      string  `json:"side"`       // 买卖方向（买入/卖出）
	Price     float64 `json:"price"`      // 委托价格
	Qty       int     `json:"qty"`        // 委托数量（股）
	Amount    float64 `json:"amount"`     // 委托金额（Price×Qty）
	Status    string  `json:"status"`     // 已报/已成/已撤
	CreatedAt string  `json:"created_at"` // 委托创建时间（RFC3339）
}

// pos 内存中的持仓（按 ts_code 聚合，加权成本）。
// （pos is an in-memory position aggregated by ts_code with weighted cost.）
type pos struct {
	TsCode       string  `json:"ts_code"`       // 标的代码（ts_code）
	Name         string  `json:"name"`          // 标的名称
	Qty          int     `json:"qty"`           // 当前持仓数量（股）
	CostPrice    float64 `json:"cost_price"`    // 加权持仓成本价
	Amount       float64 `json:"amount"`        // 当前持仓市值（Qty×现价）
	HighestPrice float64 `json:"highest_price"` // 持仓期间最高价（用于回撤/止盈判断）
}

// book 网关内存账本。
// （book is the gateway in-memory book.）
type book struct {
	mu        sync.Mutex        // 保护账本并发读写的互斥锁
	orders    map[string]*order // 全部委托记录（键为 order_id）
	positions map[string]*pos   // 持仓（键为 ts_code，加权成本聚合）
	signal    map[string]string // signal_id → order_id 幂等索引（防止同信号重复下单）
	account   string            // 模拟资金账号
	nextID    int               // 委托 ID 自增计数器
}

// newBook 创建指定资金账号的内存账本（初始化订单/持仓映射与 signal→order 幂等索引）。
func newBook(account string) *book {
	return &book{
		orders:    map[string]*order{},
		positions: map[string]*pos{},
		signal:    map[string]string{},
		account:   account,
		nextID:    1,
	}
}

// nextOrderID 在账本锁保护下生成全局自增的委托 ID（格式 MOCK000001）。
func (b *book) nextOrderID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := fmt.Sprintf("MOCK%06d", b.nextID)
	b.nextID++
	return id
}

// seedPositions 预置初始持仓（联调看板用）。
// （seedPositions seeds initial positions for dashboard testing.）
func (b *book) seedPositions(seeds []string) {
	for _, s := range seeds {
		parts := strings.Split(s, ",")
		if len(parts) < 4 {
			log.Printf("[mock] skip bad seed %q (want code,name,qty,cost)", s)
			continue
		}
		var qty int
		var cost float64
		fmt.Sscanf(parts[2], "%d", &qty)
		fmt.Sscanf(parts[3], "%f", &cost)
		if qty <= 0 {
			continue
		}
		code := parts[0]
		name := parts[1]
		b.mu.Lock()
		b.positions[code] = &pos{TsCode: code, Name: name, Qty: qty, CostPrice: cost, Amount: float64(qty) * cost, HighestPrice: cost}
		b.mu.Unlock()
		log.Printf("[mock] seeded %s %s %d@%.2f", code, name, qty, cost)
	}
}

// applyFill 按成交更新持仓（买加仓加权成本/卖减仓，清仓删除行）并推进最高价。
// （applyFill updates the book on a fill: buy adds with weighted cost, sell trims, close deletes, and
// highest price only moves up.）
func (b *book) applyFill(o *order, price float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.positions[o.Code]
	if o.Side == "买入" {
		if p == nil {
			p = &pos{TsCode: o.Code, Name: o.Name, Qty: 0, HighestPrice: price}
			b.positions[o.Code] = p
		}
		oldQty, oldCost := p.Qty, p.CostPrice
		newQty := oldQty + o.Qty
		// 加权成本 = (旧数×旧成本 + 本次金额) / 新数
		newCost := (float64(oldQty)*oldCost + float64(o.Qty)*price) / float64(newQty)
		p.Qty = newQty
		p.CostPrice = newCost
		p.Amount = float64(newQty) * price
		if price > p.HighestPrice {
			p.HighestPrice = price
		}
	} else {
		if p == nil {
			return
		}
		remain := p.Qty - o.Qty
		if remain <= 0 {
			delete(b.positions, o.Code)
			return
		}
		p.Qty = remain
		p.Amount = float64(remain) * price
	}
}

// snapshotPositions 导出当前持仓（按市值排序）。
// （snapshotPositions exports current positions sorted by market value.）
func (b *book) snapshotPositions() []map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]map[string]interface{}, 0, len(b.positions))
	for _, p := range b.positions {
		out = append(out, map[string]interface{}{
			"ts_code":       p.TsCode,
			"name":          p.Name,
			"qty":           p.Qty,
			"cost_price":    round2(p.CostPrice),
			"amount":        round2(p.Amount),
			"highest_price": round2(p.HighestPrice),
			"strategy":      "",
			"signal_id":     "",
			"updated_at":    time.Now().Format(time.RFC3339),
		})
	}
	return out
}

// round2 把 float64 四舍五入到 2 位小数（用于金额/价格展示对齐）。
func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

// main 启动 MiniQMT 模拟网关：解析命令行参数，初始化内存账本、HTTP 路由与鉴权中间件，
// 监听 /health /state /order /cancel 接口，并把成交回报按 --delay 延时后推送回首尔服务器。
func main() {
	listen := flag.String("listen", ":8789", "网关监听地址")
	token := flag.String("token", "mock-secret", "Bearer token（与首尔 qmt.token 一致）")
	server := flag.String("server", "", "首尔服务器地址，成交回报推送到其 POST /api/qmt/report（留空不推送）")
	reportToken := flag.String("report-token", "", "推送 /api/qmt/report 时的 Bearer token（默认同 -token）")
	delay := flag.Duration("delay", 3*time.Second, "模拟成交延时（下单受理后延时成交）")
	account := flag.String("account", "MOCK0001", "模拟资金账号")
	seed := flag.String("seed", "", "预置持仓（逗号分隔列表，每项 code,name,qty,cost；示例 600519.SH,贵州茅台,100,1500.00）")
	flag.Parse()

	b := newBook(*account)
	if *seed != "" {
		b.seedPositions(strings.Split(*seed, "|"))
	}
	if *reportToken == "" {
		reportToken = token
	}

	// §U-4（2026-09-14 像素级 UAT）事件回报助手：把委托生命周期事件推回引擎
	// （受理"已报"/成交"已成"+trade/撤单"已撤"，与实网关回调同口径）；
	// -server 未配置时静默跳过，纯本地联调不受影响。
	// orderEvent/路由面已抽为包级函数（buildHandler），供 §U-4 生命周期单测直接驱动。
	push := func(payload map[string]interface{}) {
		if *server == "" {
			return
		}
		if err := postReport(*server, *reportToken, payload); err != nil {
			log.Printf("[mock] report push failed: %v", err)
		} else {
			log.Printf("[mock] reported type=%v status=%v order=%v", payload["type"], payload["status"], payload["order_id"])
		}
	}

	handler := buildHandler(b, *token, *delay, push)

	srv := &http.Server{Addr: *listen, Handler: handler}
	go func() {
		log.Printf("[mock] MiniQMT mock gateway listening on %s (account=%s)", *listen, b.account)
		if *server != "" {
			log.Printf("[mock] fill reports → %s POST /api/qmt/report (token=%s)", *server, *reportToken)
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[mock] listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("[mock] shutting down")
	_ = srv.Close()
}

// orderEvent 组一条委托状态回报：字段形状与真实网关（qmt_gateway/handler.py）的 order 回调
// 一致，引擎侧按 signal_id 走单调状态机推进。提取为包级函数供 §U-4 生命周期单测复用。
// English: builds an order-status report shaped like the real gateway's callback payload.
func orderEvent(o *order, status string) map[string]interface{} {
	return map[string]interface{}{
		"type": "order", "order_id": o.OrderID, "code": o.Code, "side": o.Side,
		"status": status, "price": o.Price, "qty": o.Qty,
		"signal_id": o.SignalID, "at": time.Now().Format(time.RFC3339),
	}
}

// buildHandler 组装 mock 网关的 HTTP 面（/health /state /order /cancel + Bearer 中间件）。
// §U-4 测试化改造：路由逻辑原内联于 main（依赖 flag 全局），无法单测委托生命周期
// （已报→已成/已撤 事件推送、撤单竞态守卫、终态 409 契约）；现抽为纯函数——账本 book、
// 鉴权 token、成交延时、回报回调 push 全部注入，httptest 可直接驱动验证。
// English: extracts the mock's HTTP surface into an injectable function so the §U-4 order
// lifecycle (submit/fill/cancel events, race guard, 409 contract) is unit-testable.
func buildHandler(b *book, token string, delay time.Duration, push func(map[string]interface{})) http.Handler {
	mux := http.NewServeMux()

	// /health 健康探测。§GAP2-W1 补 broker_connected=true：真实 qmt_gateway 的 /health 带该字段
	// （反映 xtquant 通道状态），引擎侧 Health() 要求 ok && broker_connected 才算健康；
	// mock 必须对齐契约，否则全链路联调会因"通道未连"被熔断。
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"ok": true, "broker_connected": true, "ts": time.Now().Format(time.RFC3339)})
	})

	// /state 网关状态与持仓/委托（对账源）。
	mux.HandleFunc("/state", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		orders := make([]map[string]interface{}, 0, len(b.orders))
		for _, o := range b.orders {
			orders = append(orders, map[string]interface{}{
				"order_id": o.OrderID, "signal_id": o.SignalID, "code": o.Code, "side": o.Side,
				"status": o.Status, "price": o.Price, "qty": o.Qty, "created_at": o.CreatedAt,
			})
		}
		b.mu.Unlock()
		writeJSON(w, map[string]interface{}{
			"connected": true,
			"account":   b.account,
			"positions": b.snapshotPositions(),
			"orders":    orders,
		})
	})

	// /order 下单：受理即返回 order_id 并推"已报"，延时后模拟成交推"已成"+trade。
	mux.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			SignalID  string  `json:"signal_id"`
			Code      string  `json:"code"`
			Name      string  `json:"name"`
			Strategy  string  `json:"strategy"`
			Side      string  `json:"side"`
			PriceType string  `json:"price_type"`
			Price     float64 `json:"price"`
			Qty       int     `json:"qty"`
			Amount    float64 `json:"amount"`
			CreatedAt string  `json:"created_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"ok":false,"err":"bad body"}`, http.StatusBadRequest)
			return
		}
		if req.Code == "" || req.Qty <= 0 {
			http.Error(w, `{"ok":false,"err":"code/qty required"}`, http.StatusBadRequest)
			return
		}
		// signal_id 幂等：同 signal_id 已受理 → 返回原 order_id（不重复下单，与实网关语义一致）
		if req.SignalID != "" {
			b.mu.Lock()
			if prev, dup := b.signal[req.SignalID]; dup {
				b.mu.Unlock()
				log.Printf("[mock] idempotent signal_id=%s → return existing %s", req.SignalID, prev)
				writeJSON(w, map[string]interface{}{"ok": true, "order_id": prev})
				return
			}
			b.mu.Unlock()
		}
		orderID := b.nextOrderID()
		o := &order{
			OrderID: orderID, SignalID: req.SignalID, Code: req.Code, Name: req.Name,
			Strategy: req.Strategy, Side: req.Side, Price: req.Price, Qty: req.Qty,
			Amount: req.Amount, Status: "已报", CreatedAt: req.CreatedAt,
		}
		if o.CreatedAt == "" {
			o.CreatedAt = time.Now().Format(time.RFC3339)
		}
		b.mu.Lock()
		b.orders[orderID] = o
		if req.SignalID != "" {
			b.signal[req.SignalID] = orderID
		}
		b.mu.Unlock()
		log.Printf("[mock] order accepted %s %s %d@%.2f", req.Side, req.Code, req.Qty, req.Price)
		// §U-4 受理即推 order 事件（已报），与实网关 on_order_response 回调同口径——
		// 引擎侧据此把委托生命周期纳入单调状态机观测（占位行幂等，不会误覆盖）。
		push(orderEvent(o, "已报"))

		// 延时模拟成交并回报。
		go func(o *order) {
			time.Sleep(delay)
			// §U-4 撤单竞态守卫：真实柜台里"已撤委托绝不会再成交"。旧 mock 延时到点无条件
			// applyFill 并把状态强改"已成"，撤单窗口内的单被回填成交→幻影成交流入持仓
			// （实测 MOCK000002 撤单仍持仓）。现成交前先判状态，非"已报"即跳过。
			b.mu.Lock()
			if o.Status != "已报" {
				cur := o.Status
				b.mu.Unlock()
				log.Printf("[mock] skip fill: order %s already %s (not 已报)", o.OrderID, cur)
				return
			}
			o.Status = "已成"
			evtDone := orderEvent(o, "已成")
			b.mu.Unlock()
			b.applyFill(o, o.Price)
			log.Printf("[mock] fill %s %s %d@%.2f", o.Side, o.Code, o.Qty, o.Price)

			// §U-4 先推 order（已成）推进委托状态机，再推 trade 记账成交——两条同实网关契约。
			push(evtDone)
			push(map[string]interface{}{
				"type": "trade", "order_id": o.OrderID, "code": o.Code, "side": o.Side,
				"price": o.Price, "qty": o.Qty, "amount": float64(o.Qty) * o.Price,
				"traded_at": time.Now().Format(time.RFC3339), "signal_id": o.SignalID,
			})
		}(o)

		writeJSON(w, map[string]interface{}{"ok": true, "order_id": orderID})
	})

	// /cancel 撤单：仅未成委托可撤；未知 404、终态 409（与实网关 §R4-1 契约一致），
	// 成功置"已撤"并推 order 事件。
	mux.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID string `json:"order_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"ok":false,"err":"bad body"}`, http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		o := b.orders[req.OrderID]
		if o == nil {
			b.mu.Unlock()
			http.Error(w, `{"ok":false,"err":"unknown order_id"}`, http.StatusNotFound)
			return
		}
		if o.Status != "已报" {
			// 已成/已撤/废单等终态不可撤——真实柜台回 409，引擎侧 CancelOrder 据此如实报错，
			// 绝不吞掉失败让引擎误判撤单成功（§R4-1）。旧 mock 无条件回 ok:true 掩盖了这条分支。
			cur := o.Status
			b.mu.Unlock()
			http.Error(w, `{"ok":false,"err":"order not cancellable (status=`+cur+`)"}`, http.StatusConflict)
			return
		}
		o.Status = "已撤"
		evtCancel := orderEvent(o, "已撤")
		b.mu.Unlock()
		log.Printf("[mock] cancelled %s", req.OrderID)
		push(evtCancel)
		writeJSON(w, map[string]interface{}{"ok": true})
	})

	// Bearer 鉴权中间件（/health 豁免，其余必须携带与引擎 qmt.token 一致的密钥）。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			mux.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != token {
			http.Error(w, `{"ok":false,"err":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// postReport 推送回报到引擎服务器。
// （postReport pushes a report to the Seoul server.）
func postReport(base, token string, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := strings.TrimRight(base, "/") + "/api/qmt/report"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("report HTTP %d: %s", resp.StatusCode, truncate(string(b), 200))
	}
	return nil
}

// writeJSON 以 JSON 编码写入 HTTP 响应，并设置 application/json 内容类型。
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// truncate 截断超长字符串（超过 n 字节追加 "..."），用于日志与错误信息裁剪。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
