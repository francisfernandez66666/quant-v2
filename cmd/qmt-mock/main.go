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
	fills     []fillRecord      // §P2-14（2026-09-15）成交流水（/settlement 对账源，serial 唯一）
	account   string            // 模拟资金账号
	nextID    int               // 委托 ID 自增计数器
	nextSer   int               // §P2-14 成交流水号自增（serial=SER000001，对齐实网关 trade_id 语义）
	cash      float64           // §P2-14 模拟现金（买入扣减/卖出回补，/settlement cash 口径）
	fillMode  string            // §P2-14 成交模式：full（默认）/partial（部成→已成）/reject（废单）
}

// fillRecord 成交流水行（§P2-14 /settlement 装配源，字段对齐实网关 fills 表）。
type fillRecord struct {
	OrderID  string  `json:"order_id"`
	Code     string  `json:"code"`
	Side     string  `json:"side"`
	Price    float64 `json:"price"`
	Qty      int     `json:"qty"`
	Amount   float64 `json:"amount"`
	TradedAt string  `json:"traded_at"`
	Serial   string  `json:"serial"` // 交割流水号（对齐实网关 trade_id/serial）
	SignalID string  `json:"signal_id"`
	// §P2-FEE 20260918：模拟单笔费用（佣金万2.5 最低5元；卖方印花税千0.5 单边）。
	// 仅作为回报/settlement 的费用腿元数据（对齐实网关尽力透传形态），不扣现金账——
	// mock 显式现金口径（§P1-17）保持不变。
	Fee      float64 `json:"fee"`
	StampTax float64 `json:"stamp_tax"`
}

// newBook 创建指定资金账号的内存账本（初始化订单/持仓映射与 signal→order 幂等索引）。
func newBook(account string) *book {
	return &book{
		orders:    map[string]*order{},
		positions: map[string]*pos{},
		signal:    map[string]string{},
		account:   account,
		nextID:    1,
		cash:      1000000, // §P2-14 默认模拟现金 100 万（-cash 可调）
		fillMode:  "full",
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
// §P2-14：同步记成交流水（fills，serial 自增唯一）+ 现金变动（买扣/卖回补），
// 供 /settlement 对账与 positions/account 事件回报。
func (b *book) applyFill(o *order, price float64) (fillRecord, bool) {
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
		b.cash -= float64(o.Qty) * price
	} else {
		// §UAT-D5（2026-09-16）：卖出超仓在真柜台是「证券不足」整笔废单——旧 mock
		// p==nil 静默 false（委托永挂"已报"、无废单事件），p.Qty<o.Qty 又删整仓并按全部
		// 卖量回补现金（幻影现金）。现统一：持仓不足直接判失败，交调用侧推废单。
		// English: §UAT-D5 — a sell exceeding holdings is a whole-ticket reject at the real
		// counter (insufficient securities); the old mock either silently hung the ticket
		// (no reject event) or deleted the position while crediting full sell cash (phantom).
		if p == nil || p.Qty < o.Qty {
			return fillRecord{}, false
		}
		remain := p.Qty - o.Qty
		b.cash += float64(o.Qty) * price
		if remain <= 0 {
			delete(b.positions, o.Code)
		} else {
			p.Qty = remain
			p.Amount = float64(remain) * price
		}
	}
	b.nextSer++
	amt := float64(o.Qty) * price
	// §P2-FEE 20260918：佣金万2.5 最低 5 元；卖方单边印花税千0.5（仅模拟费用腿元数据，不动现金）。
	mockFee := amt * 0.00025
	if mockFee < 5 {
		mockFee = 5
	}
	var mockStamp float64
	if o.Side == "卖出" {
		mockStamp = amt * 0.0005
	}
	fr := fillRecord{
		OrderID: o.OrderID, Code: o.Code, Side: o.Side, Price: price, Qty: o.Qty,
		Amount: amt, TradedAt: time.Now().Format(time.RFC3339),
		Serial: fmt.Sprintf("SER%06d", b.nextSer), SignalID: o.SignalID,
		Fee: mockFee, StampTax: mockStamp,
	}
	b.fills = append(b.fills, fr)
	return fr, true
}

// snapshotFills 导出某交易日（YYYY-MM-DD 前缀匹配）的成交流水（§P2-14 /settlement 用）。
func (b *book) snapshotFills(day string) []fillRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]fillRecord, 0, len(b.fills))
	for _, f := range b.fills {
		if day == "" || strings.HasPrefix(f.TradedAt, day) {
			out = append(out, f)
		}
	}
	return out
}

// snapshotCash 导出当前现金（§P2-14 /settlement cash 口径）。
func (b *book) snapshotCash() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cash
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
	cash := flag.Float64("cash", 1000000, "§P2-14 模拟初始现金（/settlement cash 与 account 事件口径）")
	fillMode := flag.String("fill-mode", "full", "§P2-14 成交模式：full=整笔已成 / partial=先部成后已成 / reject=柜台废单（推废单事件）")
	chaos := flag.Bool("chaos", false, "§P2-14 乱序回报：先推 trade 再推 order已成（回归引擎单调状态机守卫）")
	flag.Parse()

	// 内存账本在启动期一次性定型：初始现金、成交模式（非法值退回 full 并打日志）、
	// 预置持仓与回报推送 token；之后所有 HTTP 处理与后台成交回调共享这一份状态。
	b := newBook(*account)
	b.cash = *cash
	if *fillMode == "partial" || *fillMode == "reject" || *fillMode == "full" {
		b.fillMode = *fillMode
	} else {
		log.Printf("[mock] unknown -fill-mode %q, fallback full", *fillMode)
	}
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

	handler := buildHandler(b, *token, *delay, push, *chaos)

	// 监听放到后台 goroutine，主 goroutine 留给后面的信号等待；启动日志把监听地址、
	// 资金账号与回报去向一次打全，排查联调问题时不必再猜当前进程的配置。
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

// pushBookSnapshot §P2-14：成交后推 positions + account 快照对账事件——引擎的
// 清算 Guard（空快照守卫）与资金闸（AvailableCash）此前在 mock 环境零覆盖。
// 字段形状对齐实网关 periodic_reconcile / on_account 回报。
func pushBookSnapshot(b *book, push func(map[string]interface{})) {
	if pos := b.snapshotPositions(); len(pos) >= 0 {
		push(map[string]interface{}{"type": "positions", "positions": pos,
			"at": time.Now().Format(time.RFC3339)})
	}
	push(map[string]interface{}{"type": "account", "asset": map[string]interface{}{
		"cash":         b.snapshotCash(),
		"frozen_cash":  0.0,
		"total_asset":  b.snapshotCash() + bookMarketValue(b),
		"market_value": bookMarketValue(b),
	}, "at": time.Now().Format(time.RFC3339)})
}

// bookMarketValue 汇总当前持仓市值（§P2-14 account 事件 total_asset 口径）。
func bookMarketValue(b *book) float64 {
	total := 0.0
	for _, p := range b.snapshotPositions() {
		if v, ok := p["amount"].(float64); ok {
			total += v
		}
	}
	return total
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

// sideCN §P2-14 方向归一（对齐实网关 normalizeSide / 引擎 normalizeReportSide 口径）：
// buy/b/买入 → 买入，sell/s/卖出 → 卖出；未知原样返回（成交按未知方向 no-op 不记账）。
// 旧 mock 不归一，测试/联调传 "buy" 时 applyFill 走卖分支 no-op，成交事件与账本脱节。
func sideCN(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "buy", "b", "买入", "买":
		return "买入"
	case "sell", "s", "卖出", "卖":
		return "卖出"
	}
	return s
}

// buildHandler 组装 mock 网关的 HTTP 面（/health /state /order /cancel /settlement + Bearer 中间件）。
// §U-4 测试化改造：路由逻辑原内联于 main（依赖 flag 全局），无法单测委托生命周期
// （已报→已成/已撤 事件推送、撤单竞态守卫、终态 409 契约）；现抽为纯函数——账本 book、
// 鉴权 token、成交延时、回报回调 push 全部注入，httptest 可直接驱动验证。
// §P2-14（2026-09-15）：补部成/废单/positions/account 事件与 /settlement——mock 此前缺失
// 这些事件，引擎的清算守卫/熔断/拒因链路在 mock 环境完全测不到（契约差距见 UAT 文档 P2-14）。
// English: extracts the mock's HTTP surface into an injectable function; §P2-14 adds partial-fill,
// reject, positions/account reports and the /settlement reconciliation leg for contract parity.
func buildHandler(b *book, token string, delay time.Duration, push func(map[string]interface{}), chaos bool) http.Handler {
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
			// §A2（AUDIT_FULLSTACK_20260918）契约字段全量声明：mock 与实网关消费面对齐；
			// mock 不复算白名单/金额闸（那是实网关 §A1 闸口），仅受理保形态一致。
			StrategyType string `json:"strategy_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"ok":false,"err":"bad body"}`, http.StatusBadRequest)
			return
		}
		if req.Code == "" || req.Qty <= 0 {
			http.Error(w, `{"ok":false,"err":"code/qty required"}`, http.StatusBadRequest)
			return
		}
		req.Side = sideCN(req.Side)
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

		// 延时模拟成交并回报。§P2-14：按 fill-mode 分档——
		//   full（默认）：已报→已成+trade；
		//   partial：已报→部成+半笔 trade→已成+余笔 trade（对齐实网关"按累计量合成状态"）；
		//   reject：受理后柜台废单（推 order 废单事件，status=废单，无成交）——
		//           引擎拒因链路此前在 mock 环境完全测不到。
		// chaos（可选）：先推 trade 再推 order 已成，回归引擎单调状态机的乱序守卫。
		// positions/account：每笔成交后推快照对账事件（清算 Guard/资金闸的 mock 覆盖）。
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
			pushFills := func(fillQty int) bool {
				o2 := *o
				o2.Qty = fillQty
				fr, okFill := b.applyFill(&o2, o.Price)
				if !okFill {
					// §UAT-D5：入账失败（持仓不足）= 柜台废单——推 废单 终态事件（带拒因），
					// 委托不再永挂"已报"等超时撤；引擎侧走真实拒因链路（重报禁用/告警）。
					b.mu.Lock()
					held := 0
					if p := b.positions[o.Code]; p != nil {
						held = p.Qty
					}
					o.Status = "废单"
					rej := orderEvent(o, "废单")
					rej["reason"] = fmt.Sprintf("mock 柜台废单: 卖量 %d 超过持仓 %d（证券不足）", fillQty, held)
					b.mu.Unlock()
					log.Printf("[mock] reject %s: sell %d > held %d", o.OrderID, fillQty, held)
					push(rej)
					return false
				}
				trade := map[string]interface{}{
					"type": "trade", "order_id": o.OrderID, "code": o.Code, "side": o.Side,
					"price": o.Price, "qty": fillQty, "amount": float64(fillQty) * o.Price,
					"traded_at": fr.TradedAt, "signal_id": o.SignalID, "trade_id": fr.Serial,
					// §P2-FEE 20260918：回报带费用腿（与 /settlement 同源，Go 侧落本地 fills.fee）
					"fee": fr.Fee, "stamp_tax": fr.StampTax,
				}
				ord := orderEvent(o, o.Status)
				if chaos {
					push(trade)
					push(ord)
				} else {
					push(ord)
					push(trade)
				}
				return true
			}
			switch b.fillMode {
			case "reject":
				o.Status = "废单"
				evt := orderEvent(o, "废单")
				evt["reason"] = "mock reject mode: 模拟柜台废单（fill-mode=reject）"
				b.mu.Unlock()
				log.Printf("[mock] reject %s (fill-mode=reject)", o.OrderID)
				push(evt)
				return
			case "partial":
				if o.Qty >= 200 {
					half := o.Qty / 2 / 100 * 100
					if half > 0 && half < o.Qty {
						o.Status = "部成"
						b.mu.Unlock()
						if !pushFills(half) {
							return // §UAT-D5 首笔即废单：不再推进余量
						}
						time.Sleep(200 * time.Millisecond)
						b.mu.Lock()
						o.Status = "已成"
						b.mu.Unlock()
						if !pushFills(o.Qty - half) {
							return
						}
						pushBookSnapshot(b, push)
						return
					}
				}
				o.Status = "已成"
				b.mu.Unlock()
				pushFills(o.Qty)
				pushBookSnapshot(b, push)
				return
			default: // full
				o.Status = "已成"
				b.mu.Unlock()
				pushFills(o.Qty)
				pushBookSnapshot(b, push)
			}
		}(o)

		writeJSON(w, map[string]interface{}{"ok": true, "order_id": orderID})
	})

	// /settlement §P2-14（2026-09-15）日终对账权威源（对齐实网关 §P0-1a 同名端点）：
	// GET /settlement?date=YYYY-MM-DD → 当日成交流水（serial 唯一流水号）+ 现金快照。
	// 此前 mock 404，Go 侧 SettleDay 的 e2e 只能靠 stub；现在全链路结算对账可在 mock 下回归。
	mux.HandleFunc("/settlement", func(w http.ResponseWriter, r *http.Request) {
		day := r.URL.Query().Get("date")
		if len(day) != 10 || day[4] != '-' || day[7] != '-' {
			http.Error(w, `{"ok":false,"err":"date required, format YYYY-MM-DD"}`, http.StatusBadRequest)
			return
		}
		fills := b.snapshotFills(day)
		trades := make([]map[string]interface{}, 0, len(fills))
		for _, f := range fills {
			trades = append(trades, map[string]interface{}{
				"order_id": f.OrderID, "ts_code": f.Code, "side": f.Side,
				"price": f.Price, "qty": f.Qty, "amount": f.Amount,
				// §P2-FEE 20260918：交割流水费用腿与成交回报同源（对账费用差腿可观测）
				"fee": f.Fee, "stamp_tax": f.StampTax, "serial": f.Serial,
				"traded_at": f.TradedAt, "signal_id": f.SignalID,
			})
		}
		cash := b.snapshotCash()
		writeJSON(w, map[string]interface{}{
			"ok": true, "date": day, "account": b.account,
			"trades": trades, "cash": map[string]interface{}{"cash": cash,
				"frozen_cash": 0.0, "total_asset": cash + bookMarketValue(b),
				"market_value": bookMarketValue(b)},
			"connected": true,
		})
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
