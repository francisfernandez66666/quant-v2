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
	OrderID   string  `json:"order_id"`
	SignalID  string  `json:"signal_id"`
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Strategy  string  `json:"strategy"`
	Side      string  `json:"side"`
	Price     float64 `json:"price"`
	Qty       int     `json:"qty"`
	Amount    float64 `json:"amount"`
	Status    string  `json:"status"` // 已报/已成/已撤
	CreatedAt string  `json:"created_at"`
}

// pos 内存中的持仓（按 ts_code 聚合，加权成本）。
// （pos is an in-memory position aggregated by ts_code with weighted cost.）
type pos struct {
	TsCode       string  `json:"ts_code"`
	Name         string  `json:"name"`
	Qty          int     `json:"qty"`
	CostPrice    float64 `json:"cost_price"`
	Amount       float64 `json:"amount"`
	HighestPrice float64 `json:"highest_price"`
}

// book 网关内存账本。
// （book is the gateway in-memory book.）
type book struct {
	mu       sync.Mutex
	orders   map[string]*order
	positions map[string]*pos
	signal   map[string]string // signal_id → order_id（幂等索引）
	account  string
	nextID   int
}

func newBook(account string) *book {
	return &book{
		orders:    map[string]*order{},
		positions: map[string]*pos{},
		signal:    map[string]string{},
		account:   account,
		nextID:    1,
	}
}

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

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

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

	mux := http.NewServeMux()

	// /health 健康探测。
	// （/health liveness probe.）
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]interface{}{"ok": true, "ts": time.Now().Format(time.RFC3339)})
	})

	// /state 网关状态与持仓/委托（对账源）。
	// （/state gateway state and positions/orders, the reconciliation source.）
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

	// /order 下单：受理即返回 order_id，延时后模拟成交并回报。
	// （/order places an order: returns order_id immediately, then simulates a fill after the delay and
	// reports it back.）
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
		// signal_id 幂等：同 signal_id 已受理 → 返回原 order_id（不重复下单，与 M2 网关语义一致）
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

		// 延时模拟成交并回报。
		// Simulate the fill after the delay and report back.
		go func(o *order) {
			time.Sleep(*delay)
			b.applyFill(o, o.Price)
			b.mu.Lock()
			o.Status = "已成"
			b.mu.Unlock()
			log.Printf("[mock] fill %s %s %d@%.2f", o.Side, o.Code, o.Qty, o.Price)

			if *server != "" {
				payload := map[string]interface{}{
					"type": "trade", "order_id": o.OrderID, "code": o.Code, "side": o.Side,
					"price": o.Price, "qty": o.Qty, "amount": float64(o.Qty) * o.Price,
					"traded_at": time.Now().Format(time.RFC3339), "signal_id": o.SignalID,
				}
				if err := postReport(*server, *reportToken, payload); err != nil {
					log.Printf("[mock] report push failed: %v", err)
				} else {
					log.Printf("[mock] reported fill to %s", *server)
				}
			}
		}(o)

		writeJSON(w, map[string]interface{}{"ok": true, "order_id": orderID})
	})

	// /cancel 撤单：仅未成委托可撤。
	// （/cancel cancels an order: only unfilled orders may be cancelled.）
	mux.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OrderID string `json:"order_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"ok":false,"err":"bad body"}`, http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		if o := b.orders[req.OrderID]; o != nil && o.Status == "已报" {
			o.Status = "已撤"
		}
		b.mu.Unlock()
		writeJSON(w, map[string]interface{}{"ok": true})
	})

	// Bearer 鉴权中间件（除 /health 外）。
	// （Bearer auth middleware, /health excluded.）
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			mux.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != *token {
			http.Error(w, `{"ok":false,"err":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})

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

// postReport 推送回报到首尔服务器。
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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}