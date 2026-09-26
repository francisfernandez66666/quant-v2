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
	"sort"
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
	QtyFilled int     `json:"qty_filled"` // §0926E2E-MX1 桥成交腿累计已成交量（部成→已成判定；xt 直发路径不消费）
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
	// activeBroker §FIX-9j(20260919)：模拟网关 active 通道（xt=miniQMT / queued=QMT 桥）。
	// 真实 qmt_gateway 有 /admin/broker 切换 + /health 回报 active；mock 此前两者皆缺，
	// 双通道切换链路（Quant 页按钮→/api/qmt/broker→网关）在 e2e/演练栈完全测不到。
	activeBroker string // 当前 active 通道，默认 xt，POST /admin/broker 可切
	// §3.1-1（2026-09-22 修复批 K · FIX_PLAN_20260922 M1/F4）行情注入面：
	// 「盘内形态可测化」的 mock 侧开关。此前 /quotes 的 tickTime 恒为 now、且响应体不带任何
	// 行情源标识，E2E 在盘外既造不出「有源快照」也造不出「超龄 tick」，M1 的词表漂移只能靠
	// 「盘外空串」假绿放过。三个字段全为 0/空即与旧行为逐字节一致（未注入 = 不改变契约）。
	//   quoteSource   —— 非空时 /quotes 响应体顶层回显 quote_source 字段（实网关无此字段，
	//                    Go 侧 QMTTick 不消费未知字段，纯观察/契约面；引擎的 Source 由
	//                    qmt_feed 硬编码 "QMT-L1"，见 internal/data/qmt_feed.go applyTicks）。
	//   tickAgeSec    —— >0 时把 tickTime 回拨到 now-age（造「超龄 tick」形态：引擎 maxAge=30s
	//                    应丢弃注入、快照退回新浪链，是 §ENH-5 新鲜度闸的可测入口）。
	//   tickTimeFixed —— >0 时优先于 tickAgeSec，直接给定绝对毫秒时间戳。
	quoteSource   string
	tickAgeSec    float64
	tickTimeFixed int64
	// unresolved §0925EVE-W3-G：第三态「待核对」占位表（键=signal_id），
	// 对齐实网关 gateway.py 的 status=待核对 行语义——已交给通道但结算结果不明的委托。
	// /admin/status 回显该表；/admin/order-confirm 人工收敛（released 删行 / settled 转终态）。
	// mock 场景下真实下单流程不会自动产生第三态行，由 /admin/mock-unresolve 注入。
	unresolved map[string]*unresolvedRow
	// ── §0926E2E-MX1（2026-09-27 四波·矩阵补位#1）派发队列（QMT 桥通道镜像）──
	// 真实 qmt_gateway 的兜底通道：active=queued 时 /order /cancel 都不直接执行，而是写入
	// dispatch 队列表；客户端内置策略桥（qmt_bridge.py）经 GET /dispatch/pending 原子取单
	// （pending→inflight 防并发双执行）、POST /dispatch/result 回报结果，网关按 handler 协议
	// 结算推进状态。mock 此前 /dispatch 三腿整体缺失（REVIEW_20260926E2E 矩阵缺口 #1），
	// 桥通道在盘内 UAT / Go 级用例完全测不到。字段形状逐一对齐 store.py dispatch 表
	// （seq="seq:%d"，status: pending/inflight/done）。
	dispatch     []dispatchItem // 只追加；结算按下标就地改状态（全程 b.mu 保护）
	nextDispatch int            // 派发 seq 自增计数器（从 1 起）
	// 严格校验开关（§0926E2E-MX1 第二项）：默认与实网关同口径——买入按板块整手规则拒非整手
	// （gateway.py lot_rule）、max_order_amount>0 时买卖双向金额帽（§A1）；
	// -relax-order-check 显式放宽（旧 mock 行为，联调造非整手小单专用）。
	relaxOrderCheck bool      // true=跳过整手/金额帽校验
	maxOrderAmount  float64   // 金额帽（0=关闭，同实网关 cfg.max_order_amount 默认）
	bridgeBeat      time.Time // 桥心跳最后上报时刻（/dispatch/result type=heartbeat 落点，bridge_state.last_heartbeat 同形）
}

// dispatchItem 一条派发队列项（§0926E2E-MX1，列形状对齐 store.py dispatch 表）。
// English: one dispatch queue entry mirroring the real gateway's dispatch table columns.
type dispatchItem struct {
	Seq       string                 `json:"seq"`       // 不透明引用 "seq:<n>"（网关侧主键）
	SignalID  string                 `json:"signal_id"` // 幂等锚（order 行非空）
	Kind      string                 `json:"kind"`      // order / cancel / diag
	Code      string                 `json:"code"`
	Side      string                 `json:"side"`
	Price     float64                `json:"price"`
	Qty       int                    `json:"qty"`
	Strategy  string                 `json:"strategy"`
	Status    string                 `json:"status"`           // pending / inflight / done
	OrderID   string                 `json:"order_id"`         // cancel 目标 / 成交回报回填的委托引用
	CreatedAt string                 `json:"created_at"`       // RFC3339
	Result    map[string]interface{} `json:"result,omitempty"` // done 后的结算结果（settle 合并写回形态）
}

// unresolvedRow 一条第三态待核对占位（字段形状对齐 gateway.py unresolved_orders 行：
// signal_id/code/side/qty/created_at/dispatch_in_flight）。
// English: one third-state placeholder row shaped like the real gateway's unresolved_orders entry.
type unresolvedRow struct {
	SignalID         string `json:"signal_id"`
	Code             string `json:"code"`
	Side             string `json:"side"`
	Qty              int    `json:"qty"`
	CreatedAt        string `json:"created_at"`
	DispatchInFlight bool   `json:"dispatch_in_flight"`
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
		orders:     map[string]*order{},
		positions:  map[string]*pos{},
		signal:     map[string]string{},
		unresolved: map[string]*unresolvedRow{},
		account:    account,
		nextID:     1,
		cash:       1000000, // §P2-14 默认模拟现金 100 万（-cash 可调）
		fillMode:   "full",
		// §0926E2E-MX1 派发队列起步 seq=1（"seq:%d" 与实网关 store.py 的 rowid 派生口径同形）
		nextDispatch: 1,
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

// quoteTickTimeMs §3.1-1（2026-09-22 修复批 K）计算 /quotes 回包的 tick 时间戳（毫秒）。
// 优先级：绝对注入（-quote-tick-time-ms）> 相对回拨（-quote-tick-age-sec）> 当前时间。
// 两个注入参数都为 0 时返回值与旧版 now.UnixMilli 同口径（仅统一到毫秒精度）。
// English: resolves the tick timestamp — fixed value wins, then the age rollback, else "now";
// with no injection the behaviour is identical to the previous implementation.
func (b *book) quoteTickTimeMs(now time.Time) int64 {
	if b.tickTimeFixed > 0 {
		return b.tickTimeFixed
	}
	if b.tickAgeSec > 0 {
		return now.Add(-time.Duration(b.tickAgeSec * float64(time.Second))).UnixMilli()
	}
	return now.UnixMilli()
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
	// §3.1-1（2026-09-22 修复批 K · FIX_PLAN_20260922 M1/F4）行情注入面：三参数全默认时
	// /quotes 输出与旧版逐字节一致，零行为变更；仅在 UAT 自举脚本显式注入时才生效。
	quoteSource := flag.String("quote-source", "", "§3.1-1 /quotes 响应体回显的 quote_source（空=不带该字段）")
	quoteTickAge := flag.Float64("quote-tick-age-sec", 0, "§3.1-1 tickTime 回拨秒数（>0 造『超龄 tick』形态，供引擎 30s maxAge 闸回归）")
	quoteTickTime := flag.Int64("quote-tick-time-ms", 0, "§3.1-1 绝对 tickTime 毫秒时间戳（>0 时优先于 -quote-tick-age-sec）")
	// §0926E2E-MX1（2026-09-27 四波·矩阵补位）下单校验开关：默认与实网关同口径（买入整手 + 金额帽），
	// 联调要造非整手小单显式开 -relax-order-check 回到旧 mock 宽松行为。
	relaxOrderCheck := flag.Bool("relax-order-check", false, "§0926E2E-MX1 放宽整手/金额帽校验（仅本机联调；默认与实网关一致）")
	maxOrderAmount := flag.Float64("max-order-amount", 0, "§0926E2E-MX1 网关侧金额帽（0=关闭，同实网关 max_order_amount 默认）")
	flag.Parse()

	// §0926E2E-W2C（2026-09-26 二波）默认口令告警一行：mock 网关历史上 -token 缺省即静默用
	// "mock-secret"，若被误挂到可达网络等于无鉴权下单口（真网关的 token 泄露告警族同口径收编）。
	// 只 warn 不拒启：UAT/自举大量以默认口令起 mock，拒启会把测试链全打断——处置权在人不在闸。
	if *token == "mock-secret" {
		log.Printf("[mock] WARN §0926E2E-W2C 正在使用默认口令 mock-secret：仅限本机 UAT/影子链路，" +
			"严禁暴露到可达网络；对外一律显式 -token 传随机值")
	}

	// 内存账本在启动期一次性定型：初始现金、成交模式（非法值退回 full 并打日志）、
	// 预置持仓与回报推送 token；之后所有 HTTP 处理与后台成交回调共享这一份状态。
	b := newBook(*account)
	b.cash = *cash
	if *fillMode == "partial" || *fillMode == "reject" || *fillMode == "full" {
		b.fillMode = *fillMode
	} else {
		log.Printf("[mock] unknown -fill-mode %q, fallback full", *fillMode)
	}
	// §3.1-1 行情注入面落地到账本（启动期一次性写、只读消费，无需加锁）。
	b.quoteSource = strings.TrimSpace(*quoteSource)
	b.tickAgeSec = *quoteTickAge
	b.tickTimeFixed = *quoteTickTime
	// §0926E2E-MX1 校验开关落地（启动期一次性写、只读消费，与行情注入面同法）。
	b.relaxOrderCheck = *relaxOrderCheck
	b.maxOrderAmount = *maxOrderAmount
	if b.relaxOrderCheck {
		log.Printf("[mock] WARN §0926E2E-MX1 整手/金额帽校验已放宽（-relax-order-check）：仅限本机联调，" +
			"与实网关拒单口径不再一致")
	}
	if b.maxOrderAmount > 0 {
		log.Printf("[mock] §0926E2E-MX1 金额帽已启用: %.2f 元/笔（买卖双向）", b.maxOrderAmount)
	}
	if b.quoteSource != "" || b.tickAgeSec > 0 || b.tickTimeFixed > 0 {
		log.Printf("[mock] §3.1-1 行情注入面已开启: quote_source=%q tick_age_sec=%v tick_time_ms=%d",
			b.quoteSource, b.tickAgeSec, b.tickTimeFixed)
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

// simulateFill §0926E2E-MX1 成交推进器（原内联于 /order 的 go func，抽为包级函数供
// 桥通道复用——xt 直发与 queued 派发结算后走同一台"柜台"，成交语义不分叉）。
// §P2-14：按 fill-mode 分档——
//
//	full（默认）：已报→已成+trade；
//	partial：已报→部成+半笔 trade→已成+余笔 trade（对齐实网关"按累计量合成状态"）；
//	reject：受理后柜台废单（推 order 废单事件，status=废单，无成交）——
//	        引擎拒因链路此前在 mock 环境完全测不到。
//
// chaos（可选）：先推 trade 再推 order 已成，回归引擎单调状态机的乱序守卫。
// positions/account：每笔成交后推快照对账事件（清算 Guard/资金闸的 mock 覆盖）。
func simulateFill(b *book, o *order, delay time.Duration, push func(map[string]interface{}), chaos bool) {
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
}

// active §0926E2E-MX1 读取当前 active 通道（空串按 xt 兜底，与 /health 回显同口径）。
func (b *book) active() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.activeBroker == "" {
		return "xt"
	}
	return b.activeBroker
}

// jsonString 把任意字符串编码为 JSON 字符串字面量（含引号转义），供 http.Error 手工拼
// {"ok":false,"err":...} 时安全嵌入含引号/中文的拒因，避免手写转义漏掉破坏 JSON。
func jsonString(s string) string {
	bb, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(bb)
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

// lotRule §0926E2E-MX1 分板块申报单位（逐条镜像 gateway.py:229 lot_rule，含 §修复T4 拆分口径）：
// 科创板(68 开头) 最低 200 股 1 股递增；创业板(30)/北交所(92、8/4 开头) 最低 100 股 1 股递增；
// 主板/其他 100 股整手。卖方向由调用方放宽（零股清仓交易所允许）。
// English: board lot rules mirrored from the real gateway's lot_rule (buy-side申报单位).
func lotRule(code string) (int, int) {
	digits := ""
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			break
		}
		digits += string(ch)
	}
	head := digits
	if len(head) > 6 {
		head = head[:6]
	}
	switch {
	case strings.HasPrefix(head, "68"):
		return 200, 1 // 科创板：最低 200 股、1 股递增
	case strings.HasPrefix(head, "30"):
		return 100, 1 // 创业板 300/301：最低 100 股、1 股递增
	case strings.HasPrefix(head, "92") || strings.HasPrefix(head, "8") || strings.HasPrefix(head, "4"):
		return 100, 1 // 北交所（920/83/87/43 等）：最低 100 股、1 股递增
	}
	return 100, 100 // 主板/其他：100 股整手
}

// orderGateReject §0926E2E-MX1 下单严格校验（默认与实网关 _do_order 同口径）：
// 买入整手规则 + 金额帽（amount 缺失回退 qty×price，买卖双向，同 §A1）。
// 返回 (HTTP 状态码, 错误信息)；通过时返回 (0,"")。-relax-order-check 时整体跳过（回旧行为）。
// English: buy-side board-lot + two-sided amount cap, same semantics as the real gateway;
// skipped only when the explicit relaxation flag is on.
func (b *book) orderGateReject(code, side string, qty int, price, amount float64) (int, string) {
	if b.relaxOrderCheck {
		return 0, ""
	}
	if side == "买入" {
		minQty, step := lotRule(code)
		if qty < minQty || ((qty-minQty)%step) != 0 {
			return http.StatusBadRequest, fmt.Sprintf("qty violates board lot rule (%s: min %d step %d)", code, minQty, step)
		}
	}
	if b.maxOrderAmount > 0 {
		amt := amount
		if amt <= 0 {
			amt = float64(qty) * price
		}
		if amt > b.maxOrderAmount {
			return http.StatusBadRequest, fmt.Sprintf("amount %.2f exceeds gateway cap %.2f", amt, b.maxOrderAmount)
		}
	}
	return 0, ""
}

// dispatchEnqueueOrder §0926E2E-MX1 下单入派发队列（active=queued 路径）。
// 幂等双防线镜像实网关 store.dispatch_enqueue_order 的 §CLAIMRELEASE 口径：
//
//	① 调用方（/order）的 signal→order 幂等索引先挡常规重复；
//	② 本函数在 b.mu 临界区内再查同 signal_id 的在途（pending/inflight）order 行——
//	   挡掉「占位被释放后重试二次入队＝双买双卖」这条 N-8 实证路径（mock 里以 HTTP 直连复现）。
//	   两者都在同一把锁内完成判重+写入（SQLite 单写者语义的 mock 等价物）。
//
// English: enqueue an order for the bridge channel, with the §CLAIMRELEASE duplicate guard
// (in-flight same-signal rows are rejected instead of silently double-queued).
func (b *book) dispatchEnqueueOrder(signalID, code, side, strategy string, price float64, qty int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if signalID != "" {
		for i := range b.dispatch {
			d := &b.dispatch[i]
			if d.Kind == "order" && d.SignalID == signalID && (d.Status == "pending" || d.Status == "inflight") {
				return "", fmt.Errorf("signal_id %s 已有在途派发单（%s），拒绝重复入队（§0926E2E-MX1 镜像 §CLAIMRELEASE）", signalID, d.Status)
			}
		}
	}
	seq := fmt.Sprintf("seq:%d", b.nextDispatch)
	b.nextDispatch++
	b.dispatch = append(b.dispatch, dispatchItem{
		Seq: seq, SignalID: signalID, Kind: "order", Code: code, Side: side,
		Price: price, Qty: qty, Strategy: strategy, Status: "pending",
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	return seq, nil
}

// dispatchEnqueueGeneric §0926E2E-MX1 cancel/diag 类派发项入队（不参与 signal 幂等判定，
// 与实网关 dispatch_enqueue_cancel/dispatch_enqueue_diag 语义一致：撤单/诊断行按原样入队）。
func (b *book) dispatchEnqueueGeneric(kind, signalID, orderID, code, side string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	seq := fmt.Sprintf("seq:%d", b.nextDispatch)
	b.nextDispatch++
	b.dispatch = append(b.dispatch, dispatchItem{
		Seq: seq, SignalID: signalID, Kind: kind, Code: code, Side: side,
		OrderID: orderID, Status: "pending", CreatedAt: time.Now().Format(time.RFC3339),
	})
	return seq
}

// dispatchTake §0926E2E-MX1 取单：原子把 pending 标记 inflight 并返回取单时的行快照
// （对齐 store.dispatch_pending——返回的是 pending 形态的 dict，状态推进发生在同一临界区）。
func (b *book) dispatchTake(limit int) []dispatchItem {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]dispatchItem, 0, limit)
	n := 0
	for i := range b.dispatch {
		d := &b.dispatch[i]
		if d.Status != "pending" {
			continue
		}
		if limit > 0 && n >= limit {
			break
		}
		d.Status = "inflight"
		out = append(out, *d)
		n++
	}
	return out
}

// dispatchSettle §0926E2E-MX1 结算派发项：status=done，result 合并写回，order_id 回填
// （镜像 store.dispatch_set_result）。找不到 seq 返回 false（HTTP 层映射 404）。
func (b *book) dispatchSettle(seq string, result map[string]interface{}) (dispatchItem, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.dispatch {
		d := &b.dispatch[i]
		if d.Seq != seq {
			continue
		}
		merged := map[string]interface{}{}
		for k, v := range d.Result {
			merged[k] = v
		}
		for k, v := range result {
			merged[k] = v
		}
		d.Result = merged
		d.Status = "done"
		if oid, ok := merged["order_id"].(string); ok && oid != "" {
			d.OrderID = oid
		}
		cp := *d
		return cp, true
	}
	return dispatchItem{}, false
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
		// §FIX-9j：对齐真实网关 _health_payload——broker/broker_mode 回报 active 通道，
		// Go 侧 BrokerStatus()（GET /health）解析后供 /api/qmt/broker 观察/切换回读。
		b.mu.Lock()
		ab := b.activeBroker
		b.mu.Unlock()
		if ab == "" {
			ab = "xt"
		}
		writeJSON(w, map[string]interface{}{
			"ok": true, "broker_connected": true, "ts": time.Now().Format(time.RFC3339),
			"broker": ab, "broker_mode": ab, "xt_connected": true, "queued_connected": true,
			// §ENH-5：feed_connected 仅为观察字段——Go 侧 Health() 判定 ok&&broker_connected
			// 不含它，行情通道状态绝不参与交易熔断（防 §GAP2-W1 面被行情污染）。
			"feed_connected": true,
		})
	})

	// /admin/broker §FIX-9j：手动切换 active 通道（{"broker":"xt|queued"}），契约对齐
	// 真实网关 _do_admin_broker：非法值 400；成功回 {ok:true, broker:目标通道}。
	// mock 只改状态位（双通道数据本就同一份内存账本），切换语义供 e2e 验证链路贯通。
	mux.HandleFunc("/admin/broker", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Broker string `json:"broker"` // 目标通道：xt（miniQMT 兼容）/ queued（QMT 桥兜底）
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "err": "invalid json"})
			return
		}
		if req.Broker != "xt" && req.Broker != "queued" {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]interface{}{"ok": false, "err": "broker must be one of: xt, queued"})
			return
		}
		b.mu.Lock()
		b.activeBroker = req.Broker
		b.mu.Unlock()
		log.Printf("[mock] admin 切换 active 通道 -> %s", req.Broker)
		writeJSON(w, map[string]interface{}{"ok": true, "broker": req.Broker, "err": ""})
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

	// /quotes §ENH-5 批E：mock Level-1 全推行情（契约对齐真实网关 quote_feed：
	// codes 逗号分隔带后缀、返回 {ok,ticks:{code:{lastPrice,open,high,low,prevClose,volume,amount,tickTime}}}）。
	// tick 为确定性伪值：基价取自代码数字（10~100 元区间），秒级 ±1% 三角波扰动，
	// 让 Go 侧 qmt_feed / nightly 能稳定断言"数据在动且可复现"。
	// volume 单位=股（与新浪链一致；真实 xtdata 单位须在生产机 probe 校准）。
	mux.HandleFunc("/quotes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		codesParam := r.URL.Query().Get("codes")
		if strings.TrimSpace(codesParam) == "" {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]interface{}{"ok": false, "err": "codes required (comma separated)"})
			return
		}
		nowSec := time.Now().Unix()
		// §3.1-1（2026-09-22 修复批 K）：tickTime 走注入面（未注入时=now 毫秒，与旧版同口径）。
		nowMs := b.quoteTickTimeMs(time.Now())
		ticks := make(map[string]interface{})
		for _, c := range strings.Split(codesParam, ",") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			// 基价：代码数字部分模 90 + 10（元），保留两位小数
			digits := int64(0)
			for _, ch := range c {
				if ch >= '0' && ch <= '9' {
					digits = digits*10 + int64(ch-'0')
				}
			}
			baseCent := (digits%90 + 10) * 100 // 元→分，1000~9900 分
			// 三角波：秒数模 100 映射到 ±1% 振幅（分），保证价格逐秒变化且可复现
			phase := (nowSec % 100) - 50
			priceCent := baseCent + baseCent*phase/5000
			vol := float64(baseCent*1000 + (nowSec%60)*1000) // 股，单调递增的假累计量
			ticks[c] = map[string]interface{}{
				"lastPrice": float64(priceCent) / 100,
				"open":      float64(baseCent-50) / 100,
				"high":      float64(priceCent+30) / 100,
				"low":       float64(priceCent-30) / 100,
				"prevClose": float64(baseCent) / 100,
				"volume":    vol,
				"amount":    vol * float64(priceCent) / 100,
				"tickTime":  nowMs,
			}
		}
		out := map[string]interface{}{"ok": true, "ticks": ticks, "feed_connected": true}
		// §3.1-1：注入 quote_source 时顶层回显同名观察字段（实网关无此字段、Go 侧 QMTTick 亦不解析，
		// 故仅在显式注入时出现——E2E 用它把「mock 侧注入的行情源名」与 golden 枚举对齐做硬断言）。
		if b.quoteSource != "" {
			out["quote_source"] = b.quoteSource
		}
		writeJSON(w, out)
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
		// §0926E2E-MX1 严格校验（默认与实网关 _do_order 同口径）：买入整手规则 + 金额帽。
		// 拒单发生在幂等占位之前，不消耗 signal_id（实网关同款次序：首尔可安全修正后重试）。
		// 联调要造非整手小单请显式起 -relax-order-check（回旧 mock 行为），不默认放宽。
		if code, msg := b.orderGateReject(req.Code, req.Side, req.Qty, req.Price, req.Amount); code != 0 {
			http.Error(w, `{"ok":false,"err":`+jsonString(msg)+`}`, code)
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
		// §0926E2E-MX1 桥通道分流（active=queued）：不直接执行、不推"已报"，整笔写入派发队列
		// 等"桥"取单——与实网关 QueuedBroker.place_order「入队即 return True」契约同形
		// （§CLAIMRELEASE：入队后不可撤回，后续结算失败也不撤行）。回单引用为 seq:<n>。
		if b.active() == "queued" {
			seq, err := b.dispatchEnqueueOrder(req.SignalID, req.Code, req.Side, req.Strategy, req.Price, req.Qty)
			if err != nil {
				// 队列纵深防线拦下（同 sid 在途行）：按实网关口径回 500（已入队是既成事实，
				// 不释放上层幂等锚——这里 mock 的锚即 signal 索引，保持不写入）
				http.Error(w, `{"ok":false,"err":`+jsonString(err.Error())+`}`, http.StatusInternalServerError)
				return
			}
			if req.SignalID != "" {
				b.mu.Lock()
				b.signal[req.SignalID] = seq // 占位引用先挂 seq，order_result 结算后回填真实委托号
				b.mu.Unlock()
			}
			log.Printf("[mock] queued 通道入队 %s %s %d@%.2f seq=%s", req.Side, req.Code, req.Qty, req.Price, seq)
			writeJSON(w, map[string]interface{}{"ok": true, "order_id": seq})
			return
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

		// 延时模拟成交并回报（full/partial/reject 分档与 chaos 乱序语义见 simulateFill）。
		go simulateFill(b, o, delay, push, chaos)

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
		// §0926E2E-MX1 桥通道分流（active=queued）：撤单同样入派发队列等桥执行，
		// 状态推进延后到 cancel_result 回报（实网关 QueuedBroker.cancel 同语义：受理≠已撤）。
		// 注意：此处已在 b.mu 临界区内（o 在锁下取出），只能直读 activeBroker 字段，
		// 绝不可调 b.active()——那会对非重入锁二次加锁自死锁（本仓死锁 dump 实证）。
		if b.activeBroker == "queued" {
			o.Status = "撤单中" // 中间态：撤单已入队，simulateFill 的状态守卫会拒绝再成交（防幻影成交）
			evtPending := orderEvent(o, "已报")
			evtPending["reason"] = "撤单请求已入派发队列，待桥回报结算"
			b.mu.Unlock()
			seq := b.dispatchEnqueueGeneric("cancel", o.SignalID, o.OrderID, o.Code, o.Side)
			log.Printf("[mock] queued 通道撤单入队 %s seq=%s", o.OrderID, seq)
			push(evtPending)
			writeJSON(w, map[string]interface{}{"ok": true, "seq": seq})
			return
		}
		o.Status = "已撤"
		evtCancel := orderEvent(o, "已撤")
		b.mu.Unlock()
		log.Printf("[mock] cancelled %s", req.OrderID)
		push(evtCancel)
		writeJSON(w, map[string]interface{}{"ok": true})
	})

	// ── §0926E2E-MX1（2026-09-27 四波·矩阵补位#1）派发队列三腿（镜像 qmt_gateway 桥通道契约）──
	// 真实链路：Go/量仔 → 网关 /order（active=queued 入队）→ 策略桥 qmt_bridge.py 经
	// GET /dispatch/pending 取单（原子 pending→inflight 防并发双执行）→ 柜台执行 →
	// POST /dispatch/result 回报 → 网关按 handler 协议推首尔。mock 三腿逐端点镜像该契约，
	// 让「取单→回报→状态推进」全生命周期在 Go 级用例/盘内 UAT 可回归（缺口定性见
	// REVIEW_20260926E2E 矩阵 #1：此前 mock 零 /dispatch 面，桥通道整条兜底路径测不到）。

	// GET /dispatch/pending 桥取单：原子取 pending（≤50）并标记 inflight。
	mux.HandleFunc("/dispatch/pending", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"ok":false,"err":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		items := b.dispatchTake(50)
		writeJSON(w, map[string]interface{}{"ok": true, "items": items})
	})

	// POST /dispatch/result 桥回报结算（事件类型集与 gateway._do_dispatch_result 逐一对齐：
	// heartbeat/positions/account/order_result/cancel_result/trade/diag；未知类型 400 不静默吞）。
	mux.HandleFunc("/dispatch/result", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"ok":false,"err":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req map[string]interface{}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, `{"ok":false,"err":"bad body"}`, http.StatusBadRequest)
			return
		}
		etype, _ := req["type"].(string)
		seq, _ := req["seq"].(string)
		strField := func(k string) string { v, _ := req[k].(string); return v }
		switch etype {
		case "heartbeat":
			// 桥心跳（bridge_state.last_heartbeat 同形观察位）：只记录，不落账。
			b.mu.Lock()
			b.bridgeBeat = time.Now()
			b.mu.Unlock()
			writeJSON(w, map[string]interface{}{"ok": true, "err": ""})
			return
		case "positions":
			// 桥侧持仓快照：原样转发首尔（实网关 handler.on_positions 同语义——快照回报
			// 是清算 Guard/对账的权威腿，mock 账本不反向覆盖快照内容）。
			push(map[string]interface{}{"type": "positions", "positions": req["positions"],
				"at": time.Now().Format(time.RFC3339)})
			writeJSON(w, map[string]interface{}{"ok": true, "err": ""})
			return
		case "account":
			push(map[string]interface{}{"type": "account", "asset": req["asset"],
				"at": time.Now().Format(time.RFC3339)})
			writeJSON(w, map[string]interface{}{"ok": true, "err": ""})
			return
		case "order_result":
			// 下单结果：ok→建委托行推"已报"并挂上成交推进器（xt/queued 共用 simulateFill，
			// 柜台语义不分叉）；失败→"已废"终态 + 拒因（状态字面量对齐实网关 _apply_order_result）。
			okFlag, _ := req["ok"].(bool)
			orderID := strField("order_id")
			errMsg := strField("err")
			row, found := b.dispatchSettle(seq, map[string]interface{}{
				"ok": okFlag, "order_id": orderID, "err": errMsg, "confirmed": true,
			})
			if !found {
				http.Error(w, `{"ok":false,"err":"unknown seq: `+seq+`"}`, http.StatusNotFound)
				return
			}
			if !okFlag {
				o := &order{OrderID: orderID, SignalID: row.SignalID, Code: row.Code, Side: row.Side,
					Strategy: row.Strategy, Price: row.Price, Qty: row.Qty, Status: "已废",
					CreatedAt: row.CreatedAt}
				if o.OrderID == "" {
					o.OrderID = seq
				}
				b.mu.Lock()
				b.orders[o.OrderID] = o
				if row.SignalID != "" {
					b.signal[row.SignalID] = o.OrderID
				}
				b.mu.Unlock()
				evt := orderEvent(o, "已废")
				evt["reason"] = errMsg
				push(evt)
				log.Printf("[mock] dispatch order_result 已废 seq=%s signal=%s err=%s", seq, row.SignalID, errMsg)
				writeJSON(w, map[string]interface{}{"ok": true, "err": ""})
				return
			}
			if orderID == "" {
				orderID = b.nextOrderID()
			}
			o := &order{OrderID: orderID, SignalID: row.SignalID, Code: row.Code, Strategy: row.Strategy,
				Side: row.Side, Price: row.Price, Qty: row.Qty, Status: "已报", CreatedAt: row.CreatedAt}
			b.mu.Lock()
			b.orders[orderID] = o
			if row.SignalID != "" {
				b.signal[row.SignalID] = orderID // seq 占位引用回填为真实委托号（对齐实网关回填 order_id 列）
			}
			b.mu.Unlock()
			push(orderEvent(o, "已报"))
			go simulateFill(b, o, delay, push, chaos)
			log.Printf("[mock] dispatch order_result 已报 seq=%s order=%s signal=%s", seq, orderID, row.SignalID)
			writeJSON(w, map[string]interface{}{"ok": true, "err": ""})
			return
		case "cancel_result":
			// 撤单结果：ok→已撤；失败→如实回推"已报"（撤单未生效，委托仍可成交）+ 拒因，
			// 与实网关 _apply_cancel_result 的 on_order 两分支同口径。
			okFlag, _ := req["ok"].(bool)
			errMsg := strField("err")
			row, found := b.dispatchSettle(seq, map[string]interface{}{"ok": okFlag, "err": errMsg})
			if !found {
				http.Error(w, `{"ok":false,"err":"unknown seq: `+seq+`"}`, http.StatusNotFound)
				return
			}
			b.mu.Lock()
			o := b.orders[row.OrderID]
			var evt map[string]interface{}
			if o != nil {
				if okFlag {
					o.Status = "已撤"
				} else if o.Status == "撤单中" {
					o.Status = "已报" // 撤单失败回到可成交态
				}
				evt = orderEvent(o, o.Status)
				if !okFlag {
					evt["status"] = "已报" // 实网关口径：失败回"已报"而非中间态字面量
					evt["reason"] = errMsg
				}
			}
			b.mu.Unlock()
			if evt != nil {
				push(evt)
			}
			log.Printf("[mock] dispatch cancel_result seq=%s ok=%v target=%s", seq, okFlag, row.OrderID)
			writeJSON(w, map[string]interface{}{"ok": true, "err": ""})
			return
		case "trade":
			// 桥成交回报（DEAL 轮询腿）：对指定委托做人工成交推进——测试专用面，
			// 与 order_result 自动挂的 simulateFill 二选一驱动（终态委托重复成交被状态守卫拒绝）。
			orderID := strField("order_id")
			b.mu.Lock()
			o := b.orders[orderID]
			b.mu.Unlock()
			if o == nil {
				http.Error(w, `{"ok":false,"err":"unknown order_id"}`, http.StatusNotFound)
				return
			}
			qtyF, _ := req["qty"].(float64)
			fillQty := int(qtyF)
			priceF, _ := req["price"].(float64)
			if priceF <= 0 {
				priceF = o.Price
			}
			b.mu.Lock()
			if o.Status != "已报" && o.Status != "部成" {
				cur := o.Status
				b.mu.Unlock()
				log.Printf("[mock] dispatch trade skip: %s already %s", orderID, cur)
				writeJSON(w, map[string]interface{}{"ok": true, "err": "", "skipped": cur})
				return
			}
			if fillQty <= 0 || fillQty > o.Qty {
				fillQty = o.Qty
			}
			o.QtyFilled += fillQty
			if o.QtyFilled >= o.Qty {
				o.Status = "已成"
			} else {
				o.Status = "部成"
			}
			b.mu.Unlock()
			o2 := *o
			o2.Qty = fillQty
			fr, okFill := b.applyFill(&o2, priceF)
			if !okFill {
				http.Error(w, `{"ok":false,"err":"trade rejected: insufficient position (mock)"}`, http.StatusBadRequest)
				return
			}
			push(map[string]interface{}{
				"type": "trade", "order_id": o.OrderID, "code": o.Code, "side": o.Side,
				"price": priceF, "qty": fillQty, "amount": float64(fillQty) * priceF,
				"traded_at": fr.TradedAt, "signal_id": o.SignalID, "trade_id": fr.Serial,
				"fee": fr.Fee, "stamp_tax": fr.StampTax,
			})
			push(orderEvent(o, o.Status))
			pushBookSnapshot(b, push)
			writeJSON(w, map[string]interface{}{"ok": true, "err": ""})
			return
		case "diag":
			// 诊断回报（交易明细 dump）：结算落 result，日志留痕（截断防刷屏，同实网关）。
			row, found := b.dispatchSettle(seq, map[string]interface{}{"ok": true, "diag": req["dump"]})
			if !found {
				http.Error(w, `{"ok":false,"err":"unknown seq: `+seq+`"}`, http.StatusNotFound)
				return
			}
			dumpJSON, _ := json.Marshal(row.Result)
			log.Printf("[mock] dispatch diag report seq=%s result=%s", seq, truncate(string(dumpJSON), 500))
			writeJSON(w, map[string]interface{}{"ok": true, "err": ""})
			return
		default:
			http.Error(w, `{"ok":false,"err":"unknown dispatch result type: `+etype+`"}`, http.StatusBadRequest)
			return
		}
	})

	// POST /dispatch/enqueue 运维注入面：仅 kind=diag（对齐实网关 _do_dispatch_enqueue——
	// 下单/撤单只能经各自的正规端点进队列，运维口不开注单后门）。
	mux.HandleFunc("/dispatch/enqueue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"ok":false,"err":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Kind     string `json:"kind"`
			SignalID string `json:"signal_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, `{"ok":false,"err":"bad body"}`, http.StatusBadRequest)
			return
		}
		if req.Kind != "diag" {
			http.Error(w, `{"ok":false,"err":"kind must be diag"}`, http.StatusBadRequest)
			return
		}
		sid := strings.TrimSpace(req.SignalID)
		if sid == "" {
			sid = fmt.Sprintf("DIAG-%d", time.Now().Unix())
		}
		seq := b.dispatchEnqueueGeneric("diag", sid, "", "", "")
		writeJSON(w, map[string]interface{}{"ok": true, "seq": seq})
	})

	// ── §0925EVE-W3-G 第三态人工收敛契约面（对齐实网关 gateway.py）──
	// GET /admin/status：观察位 unresolved_orders（最多回 20 条，unresolved_count 为全量计数）
	// + active 通道 + failover_enable。Go 侧 qmt_admin.go PendingReview() 按此形状解码，
	// 截断上限 [:20] 与实网关逐字一致，保证「truncated 显式化」链路在 mock 环境同样可测。
	mux.HandleFunc("/admin/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, `{"ok":false,"err":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		b.mu.Lock()
		rows := make([]unresolvedRow, 0, len(b.unresolved))
		for _, u := range b.unresolved {
			rows = append(rows, *u)
		}
		active := b.activeBroker
		b.mu.Unlock()
		if active == "" {
			active = "xt"
		}
		// 稳定排序（按 signal_id）：map 遍历序随机，UAT 断言行数/内容需要确定形状。
		sort.Slice(rows, func(i, j int) bool { return rows[i].SignalID < rows[j].SignalID })
		total := len(rows)
		if total > 20 {
			rows = rows[:20] // 实网关同款截断：for r in unresolved[:20]
		}
		writeJSON(w, map[string]interface{}{
			"ok":                true,
			"ts":                time.Now().Format(time.RFC3339),
			"active":            active,
			"failover_enable":   false,
			"unresolved_orders": rows,
			"unresolved_count":  total,
		})
	})

	// POST /admin/order-confirm：人工二选一收敛（released=柜台确无此单删占位 /
	// settled=柜台有此单转终态）。业务拒绝按实网关口径回 4xx + {"ok":false,"err":...}，
	// Go 侧 adminDo 会把 4xx 体结构化透传，绝不洗成 200。
	mux.HandleFunc("/admin/order-confirm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"ok":false,"err":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			SignalID string `json:"signal_id"`
			Decision string `json:"decision"`
			OrderID  string `json:"order_id"`
			Status   string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"ok":false,"err":"bad body"}`, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.SignalID) == "" {
			http.Error(w, `{"ok":false,"err":"signal_id required"}`, http.StatusBadRequest)
			return
		}
		if req.Decision != "released" && req.Decision != "settled" {
			http.Error(w, `{"ok":false,"err":"decision must be released|settled"}`, http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		u := b.unresolved[req.SignalID]
		if u == nil {
			b.mu.Unlock()
			http.Error(w, `{"ok":false,"err":"no unresolved row for signal_id"}`, http.StatusNotFound)
			return
		}
		delete(b.unresolved, req.SignalID)
		b.mu.Unlock()
		if req.Decision == "released" {
			log.Printf("[mock] order-confirm released %s", req.SignalID)
			writeJSON(w, map[string]interface{}{"ok": true, "released": true})
			return
		}
		st := req.Status
		if st == "" {
			st = "已撤" // 实网关 settled 缺省终态口径
		}
		log.Printf("[mock] order-confirm settled %s -> %s (order_id=%s)", req.SignalID, st, req.OrderID)
		writeJSON(w, map[string]interface{}{"ok": true, "status": st})
	})

	// POST /admin/mock-unresolve：测试注入面——造一条第三态待核对占位。
	// 真实流程里第三态由「派发后结算不明」产生，mock 无网络不确定性可复现，
	// 显式注入让 UAT/E2E 能锤「清单渲染 + 人工收敛」全链路（对齐 §3.1-1 行情注入面先例）。
	mux.HandleFunc("/admin/mock-unresolve", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"ok":false,"err":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req unresolvedRow
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SignalID == "" {
			http.Error(w, `{"ok":false,"err":"bad body: signal_id required"}`, http.StatusBadRequest)
			return
		}
		if req.CreatedAt == "" {
			req.CreatedAt = time.Now().Format(time.RFC3339)
		}
		row := req
		b.mu.Lock()
		b.unresolved[row.SignalID] = &row
		b.mu.Unlock()
		log.Printf("[mock] injected unresolved %s (%s %s %d)", row.SignalID, row.Code, row.Side, row.Qty)
		writeJSON(w, map[string]interface{}{"ok": true})
	})

	// POST /admin/mock-force-status：测试注入面——直接改写某笔委托的网关侧状态且**不推回报**。
	// 专造「引擎账本还认为已报、网关侧已进终态」的撤单竞态形态：此时 kill-switch 撤单
	// 会得到 409 not cancellable，HaltAllResult.Failed 明细（§0925EVE-A2）由此可测。
	mux.HandleFunc("/admin/mock-force-status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"ok":false,"err":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			OrderID string `json:"order_id"`
			Status  string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.OrderID == "" || req.Status == "" {
			http.Error(w, `{"ok":false,"err":"bad body: order_id/status required"}`, http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		o := b.orders[req.OrderID]
		if o != nil {
			o.Status = req.Status // 只改状态，不推事件——模拟回报丢失/竞态窗口
		}
		b.mu.Unlock()
		if o == nil {
			http.Error(w, `{"ok":false,"err":"unknown order_id"}`, http.StatusNotFound)
			return
		}
		log.Printf("[mock] forced status %s -> %s (no report pushed)", req.OrderID, req.Status)
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
