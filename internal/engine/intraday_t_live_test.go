// intraday_t_live_test.go — 批 8 / W8-B 实盘日内回转的反证锁。
//
// 逐条对应 docs/FIX_PLAN_20261025_W8_LIVE_T.md §3.1 的十三条（外加 §8.4.5 对手方不变量与
// §8.9a 收益口径），每条一个 `func Test...`，让以后 `go test -run TestTLiveXxx` 能单独证明
// "这条锁还在"。用例的共同纪律：
//   - 时钟钉在 2026-11-04（周三）：`IsTradingDay` 在日历未加载时只判周末，任何日子跑测试都同向；
//   - 成交一律用 fills 表播种（实盘侧的真相来源），**不**直接改账本字段来"造已在途"——
//     那样只会测到编排对账本的盲信，测不到"状态说成了、委托说没成"这一整族缺陷；
//   - 断言全部落在客观事实（柜台收到的委托及其次序、orders/fills/real_positions 行、账本终态）上。
//
// English: the W8-B counter-evidence locks — one test per invariant, asserting on the orders the fake
// gateway received (and their arrival order), on orders/fills/positions rows, and on ledger terminal
// states, with the clock pinned to a weekday.
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// tLiveDay 2026-11-04（周三）。测试里所有"当日"都用这天：跑测试那天若是周末，
// IsActiveSession 会把一切动作拦掉，用例就会"因为没跑起来而通过"——那是假绿。
var tLiveDay = time.Date(2026, 11, 4, 0, 0, 0, 0, cntime.Loc)

func tLiveClockAt(hh, mm int) time.Time {
	return time.Date(tLiveDay.Year(), tLiveDay.Month(), tLiveDay.Day(), hh, mm, 0, 0, cntime.Loc)
}

// tLiveFix 一套离线夹具：假网关 + 临时 sqlite 账本 + 按账号寻址的 T 状态文件目录。
type tLiveFix struct {
	t      *testing.T
	e      *Engine
	db     *store.DB
	ctrl   *trading.Controller
	orders *[]map[string]any
	root   string
	now    time.Time
}

func newTLiveFix(t *testing.T, hh, mm int, mutate func(*config.QMTConfig)) *tLiveFix {
	t.Helper()
	now := tLiveClockAt(hh, mm)
	e, db, _, orders := newQMTEngine(t, mutate)
	e.SetMembers([]string{"u_1"})
	e.SetClock(func() time.Time { return now })
	root := t.TempDir()
	e.mu.Lock()
	if e.tSignals == nil {
		e.tSignals = newTSigLedger() // 候选 B 的消费账本：没建起来 tRecordBuySignals 会静默 no-op
	}
	// marketAPI 只当"行情源已接入"的哨兵（dynamicBuyQty 要求非 nil），判定链的读数全部走注入的 readers。
	e.marketAPI = &data.MarketAPI{}
	e.industryFn = func(string) string { return "银行" }
	e.accountsRoot = root
	rg := newTLiveRegistry(root)
	rg.nowFn = func() time.Time { return now }
	e.tLiveReg = rg
	e.mu.Unlock()
	// dynamic 预算档：T 的额度与尾盘回补都走战略同一条预算通道（§8.5.3）。
	e.SetCfgMgr(config.NewManagerWithRules(&config.Rules{Sizing: dynSizingCfg()}))
	f := &tLiveFix{t: t, e: e, db: db, ctrl: e.QMTController(), orders: orders, root: root, now: now}
	f.seedAccount(100000, 1_000_000)
	return f
}

// seedAccount 总资产与可用资金：UpdatedAt 必须用**真实**当前时间（AvailableCash 的 30 分钟新鲜度
// 按真实时钟判），否则注入的"交易日时钟"会把资金口径判成过期，用例全踩在 fail-close 上。
func (f *tLiveFix) seedAccount(total, cash float64) {
	f.t.Helper()
	if err := f.db.UpsertRealAccount(store.RealAccount{
		UserID: "u_1", TotalAsset: total, AvailableCash: cash,
		UpdatedAt: cntime.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		f.t.Fatalf("seed account: %v", err)
	}
}

// seedPosition 播种底仓（含锚点：AnchorPrice=0 的票一律不许开 T 腿，这是 §8.4.5 的锚点前置）。
// 默认来源给 first_buy_order＝"首买价可证"那一档；要测来源闸门的用例走 seedPositionSource。
func (f *tLiveFix) seedPosition(code, name string, qty int, cost, anchor float64) {
	f.t.Helper()
	f.seedPositionSource(code, name, qty, cost, anchor, store.AnchorSourceFirstBuyOrder)
}

// seedPositionSource 播种底仓并显式指定锚点来源（§8.4.5：只有可证来源才给 T 资格）。
func (f *tLiveFix) seedPositionSource(code, name string, qty int, cost, anchor float64, source string) {
	f.t.Helper()
	// 对账语义：UpsertRealPositions 会清掉"不在本批次里"的同账号持仓行，
	// 所以逐只播种必须先读回已有行再合并，否则第二只会把第一只删掉（用例假绿的经典形态）。
	existing, err := f.db.RealPositionsForUser("u_1")
	if err != nil {
		f.t.Fatalf("read positions: %v", err)
	}
	rows := append(existing, store.RealPosition{
		TsCode: withSuffix(code), Name: name, Qty: qty, CostPrice: cost, Amount: cost * float64(qty),
		CurPrice: cost, AnchorPrice: anchor, UserID: "u_1", Strategy: "龙头",
		AnchorSource: source,
	})
	if _, err := f.db.UpsertRealPositions(rows); err != nil {
		f.t.Fatalf("seed position: %v", err)
	}
}

// legFill 用 fills 表造一条腿的成交（对账的唯一真相源）；key 就是幂等键本身。
func (f *tLiveFix) legFill(key, code, side string, qty int, price, fee float64) {
	f.t.Helper()
	if err := f.db.ApplyRealFill(store.RealFill{
		OrderID: "GW-" + key, Code: withSuffix(code), Name: code, Side: side, Price: price, Qty: qty,
		Amount: price * float64(qty), Fee: fee, TradedAt: cntime.In(f.now).Format("2006-01-02 15:04:05"),
		SignalID: key, UserID: "u_1",
	}); err != nil {
		f.t.Fatalf("seed fill %s: %v", key, err)
	}
}

// fillPlaced 把第 i 笔已收到的委托按**它自己的量价**落成成交：
// 用例不该猜编排算出的股数（帽与额度一起决定），猜错就会造出"成交量大于委托量"的假事实。
func (f *tLiveFix) fillPlaced(i int) {
	f.t.Helper()
	if i >= len(*f.orders) {
		f.t.Fatalf("没有第 %d 笔委托", i)
	}
	o := (*f.orders)[i]
	key, _ := o["signal_id"].(string)
	code := strings.Split(o["code"].(string), ".")[0]
	side := "买入"
	if o["side"] == "卖出" {
		side = "卖出"
	}
	f.legFill(key, code, side, int(o["qty"].(float64)), o["price"].(float64), 0)
}

// legOrder 用 orders 表造一条"已报未成交"的委托（证明这条腿确实存在，不是幽灵）。
func (f *tLiveFix) legOrder(key, code, side string, qty int, price float64, status, orderID string) {
	f.t.Helper()
	if _, err := f.db.UpsertRealOrder(store.RealOrder{
		OrderID: orderID, SignalID: key, Code: withSuffix(code), Side: side, Status: status,
		Price: price, Qty: qty, CreatedAt: cntime.In(f.now).Format("2006-01-02 15:04:05"),
		UserID: "u_1", IntradayT: strings.HasPrefix(key, tLKeyPrefix),
	}); err != nil {
		f.t.Fatalf("seed order %s: %v", key, err)
	}
}

// quote 造一只"各项读数都齐"的快照：分时均线由 Amount/Volume 反算，成交额固定 1 亿（远超 3000 万门槛）、
// 换手 3%、上市 500 天，让流动性前置默认放行——用例只改自己主题那一维，不会被无关门槛先拦掉。
func (f *tLiveFix) quote(code, name string, price, vwap, high, low, prevClose float64) *data.StockInfo {
	const vol = 1e7
	return &data.StockInfo{
		Code: code, Name: name, Price: price, High: high, Low: low,
		PrevClose: prevClose, Close: prevClose, Volume: vol, Amount: vwap * vol,
		Turnover: 3, ChangePct: (price - prevClose) / prevClose * 100,
	}
}

func (f *tLiveFix) quotes(qs ...*data.StockInfo) map[string]*data.StockInfo {
	out := map[string]*data.StockInfo{}
	for _, q := range qs {
		out[q.Code] = q
	}
	return out
}

// params 一套与出厂默认同值的执行参数（阈值来自方案 §8.8 表；用例只改自己主题那一条）。
func (f *tLiveFix) params(tweak func(*paper.TParams)) paper.TParams {
	p := paper.TParams{
		Enabled: true, BuyFirst: true, SellFirst: true,
		MaxRounds: 1, CapPct: 50, MinSpreadPct: 2.0,
		ForceUnwindHM: "1450", LastOpenHM: "1400",
		MarketScale: 0.70, VWAPDevPct: 0.8, MinAmplitudePct: 2.5, NearExtremePct: 0.3,
		LimitDistancePct: 1.0, OpenCoolingMin: 15, VWAPCheckTolPct: 0.1,
		MinMarketScale: 0.40, SellEnvMinMarketScale: 0.20, MarketPlungePct: -2.0,
		IntradaySellCapTotalPct: 20, StopGuardPct: 1.5, LossBreakerPct: 3.0,
		MinAmountWan: 3000, MinTurnoverPct: 1.5, MinListedDays: 60,
		IndexChangePct: 0.5, FeeBufferPct: 0.6,
	}
	if tweak != nil {
		tweak(&p)
	}
	return p
}

// round 组装一轮，并把读数注入面换成"默认全通过"的桩：
// 每个用例只翻转自己主题那一维，其余维度必须是绿的，否则断言失败时看不出是被谁拦的。
func (f *tLiveFix) round(p paper.TParams, qs map[string]*data.StockInfo) *tLiveRound {
	f.t.Helper()
	r, why := f.e.newTLiveRoundWith("u_1", f.ctrl, qs, f.now, p)
	if r == nil {
		f.t.Fatalf("装配本轮失败：%s", why)
	}
	r.readers.stockMeta = func(string) (int, bool, bool) { return 500, false, true }
	r.readers.stopLineDistance = func(string, float64) (float64, bool) { return 10, true }
	r.readers.activeSellVerdict = func(string) bool { return false }
	r.readers.strategicBusy = func(string) bool { return false }
	// 分钟链校验腿默认与主源同值（=校验通过）；测校验缺口的用例自行覆盖。
	r.readers.minuteBasis = func(code string) (float64, bool) {
		q := qs[code]
		if q == nil {
			return 0, false
		}
		return data.VWAPOf(q)
	}
	return r
}

// ledger 本账号的 T 账本。
func (f *tLiveFix) ledger() *tLiveStore {
	st := f.e.tLiveFor("u_1")
	if st == nil {
		f.t.Fatal("无账本")
	}
	return st
}

func (f *tLiveFix) placed() []map[string]any { return *f.orders }

func (f *tLiveFix) orderKeys() []string {
	var out []string
	for _, o := range f.placed() {
		if k, _ := o["signal_id"].(string); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func (f *tLiveFix) mustCount(n int) {
	f.t.Helper()
	if got := len(f.placed()); got != n {
		f.t.Fatalf("柜台应收到 %d 笔委托，实得 %d（键=%v）", n, got, f.orderKeys())
	}
}

// signal 往买入信号账本里记一条活跃信号（候选 B 的判定与一次性消费全部走真实实现）。
func (f *tLiveFix) signal(code, id string, at time.Time) {
	f.e.tRecordBuySignals([]combat_agent.Signal{{
		ID: id, Code: code, Name: code, Action: "buy", Direction: "做多", Price: 10, GeneratedAt: at,
	}})
}

// openRound 登记一条在途回转并同步造出它的**事实**（orders 行 + 可选成交）。
// OpenedAt 刻意设成 5 分钟前：尾盘回补的信号判据是"晚于开腿时刻"（§8.5.4），
// 开腿时刻若等于本轮时刻，任何用例如何记录信号都会被判成陈旧而假绿。
func (f *tLiveFix) openRound(rnd *TRoundLive, firstFilled int, firstPrice float64) *TRoundLive {
	f.t.Helper()
	if rnd.ID == "" {
		f.t.Fatal("回转缺 ID")
	}
	if rnd.OpenedAt == "" {
		rnd.OpenedAt = f.now.Add(-5 * time.Minute).Format(time.RFC3339)
	}
	if rnd.Day == "" {
		rnd.Day = cntime.DayCompactOf(f.now)
	}
	if rnd.Status == "" {
		rnd.Status = "open"
	}
	if err := f.ledger().put(rnd); err != nil {
		f.t.Fatalf("seed round: %v", err)
	}
	side := sideOf(rnd.Dir)
	f.legOrder(rnd.ID, rnd.Code, side, rnd.Qty, firstPrice, "已报", "GW-"+rnd.ID)
	if firstFilled > 0 {
		f.legFill(rnd.ID, rnd.Code, side, firstFilled, firstPrice, 0)
	}
	return rnd
}

// sideOf 回转方向对应的第一腿方向：buy_first 的第一腿是买入。
func sideOf(dir string) string {
	if dir == "buy_first" {
		return "买入"
	}
	return "卖出"
}

// tLiveKey 幂等键的测试侧派生（与被测实现同式，但只用于断言，不参与任何判定）。
func tLiveKey(code, dir string, seq int) string {
	return tLKeyPrefix + code + ":" + dir + ":" + cntime.DayCompactOf(tLiveDay) + ":" + strconv.Itoa(seq)
}

// byID 账本里按 ID 取当日记录（测试断言用；定义在测试文件里，不改生产查询面）。
func (s *tLiveStore) roundOf(id string) *TRoundLive {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}

func tLApprox(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.01
}

// ── 锁 3/4：两腿串行 + 配对先于开新腿 ─────────────────────────────────────────────────

// TestTLivePairingRunsBeforeNewLegs 同一轮里**先处理已开腿的配对、再考虑开新腿**：
// 断言的是委托到达次序（配对单在前）。把 execute() 里两个 pass 调换顺序，本用例立刻红。
func TestTLivePairingRunsBeforeNewLegs(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	a, b := "600001", "600002"
	f.seedPosition(a, "底仓A", 1000, 10, 10)
	f.seedPosition(b, "底仓B", 1000, 10, 10)
	keyA := tLiveKey(a, "buy_first", 1)
	f.legFill(keyA, a, "买入", 500, 10, 0)
	f.openRound(&TRoundLive{ID: keyA, Code: a, Name: "底仓A", Dir: "buy_first", Qty: 500}, 500, 10)
	f.signal(b, "SIG-B", f.now.Add(-time.Minute))
	qs := f.quotes(
		f.quote(a, "底仓A", 10.4, 10.0, 10.5, 9.9, 10.0),
		f.quote(b, "底仓B", 9.85, 10.0, 10.4, 9.6, 10.0),
	)
	f.round(f.params(func(p *paper.TParams) { p.MaxRounds = 2 }), qs).execute()

	keys := f.orderKeys()
	if len(keys) != 2 {
		t.Fatalf("应恰好两笔（A 的配对卖腿 + B 的新开买腿），实得 %v", keys)
	}
	if want := keyA + ":p"; keys[0] != want {
		t.Fatalf("第一笔必须是 A 的配对卖腿 %s，实得 %v（配对先于新开这条次序就是本用例的锁）", want, keys)
	}
	if want := tLiveKey(b, "buy_first", 1); keys[1] != want {
		t.Fatalf("第二笔应是 B 的新开买腿 %s，实得 %v", want, keys)
	}
	if o := f.placed()[0]; o["side"] != "卖出" || o["qty"].(float64) != 500 {
		t.Fatalf("配对腿应卖出等量底仓 500 股，实得 %+v", o)
	}
}

// TestTLiveSerialLegsNeverBothAtOnce 一条回转同刻只允许一条腿：A 已有在途买腿时，
// 即便行情翻到均线上方够格开反 T，也不许在同一轮里再压一条反向腿；
// 且本轮只报配对卖单（同码不得出现两笔方向相反的委托）。
func TestTLiveSerialLegsNeverBothAtOnce(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	a := "600001"
	f.seedPosition(a, "底仓A", 1000, 10, 10)
	keyA := tLiveKey(a, "buy_first", 1)
	f.legFill(keyA, a, "买入", 500, 10, 0)
	f.openRound(&TRoundLive{ID: keyA, Code: a, Name: "底仓A", Dir: "buy_first", Qty: 500}, 500, 10)
	// 现价 10.5 且正好在日内高点：相对买价有利 5% ⇒ 只该配一条卖腿；同时它**完全够格**开一条反 T
	// （偏离门/近极值带/振幅/额度全过，MaxRounds 也放宽）——于是"叠腿"唯一的拦得住它的东西
	// 就是"同码只允许一条在途腿"这条守卫。若用例改成行情不达标，这条锁就退化成别的门在挡。
	qs := f.quotes(f.quote(a, "底仓A", 10.5, 10.0, 10.5, 9.8, 10.0))
	f.round(f.params(func(p *paper.TParams) { p.MaxRounds = 3 }), qs).execute()

	f.mustCount(1)
	if k := f.orderKeys()[0]; !strings.HasSuffix(k, ":p") {
		t.Fatalf("唯一一笔应是配对卖腿（:p 后缀），实得 %s", k)
	}
	if open := f.ledger().openRounds(); len(open) != 1 {
		t.Fatalf("在途回转必须只有一条，实得 %d", len(open))
	}
}

// ── 锁 5：幂等键正交 + 同一 signal_id 不重复开腿 ───────────────────────────────────────

// TestTLiveIdempotencyKeysAreOrthogonal 键形状、两腿不同键、与退出/减仓键不撞、同键重放被拦。
func TestTLiveIdempotencyKeysAreOrthogonal(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	f.signal(code, "SIG-1", f.now.Add(-time.Minute))
	qBuy := f.quotes(f.quote(code, "浦发", 9.85, 10.0, 10.4, 9.6, 10.0))
	f.round(f.params(nil), qBuy).execute()
	f.mustCount(1)
	first, _ := f.placed()[0]["signal_id"].(string)
	want := tLiveKey(code, "buy_first", 1)
	if first != want {
		t.Fatalf("买腿幂等键应为 %s（t0:<code>:<dir>:<YYYYMMDD>:<seq>），实得 %s", want, first)
	}
	if strings.HasPrefix(first, "buy:") || strings.HasPrefix(first, "sell:") {
		t.Fatalf("T 腿键不得落在战略买入/退出键空间：%s", first)
	}

	// 买腿成交后把行情切到买价上方 3% ⇒ 逼出配对卖腿：第二腿的键必须与第一腿不同，
	// 否则会被自家唯一键拦成 duplicate、这条回转永远配不上对。
	f.legFill(want, code, "买入", 500, 10, 0)
	qPair := f.quotes(f.quote(code, "浦发", 10.3, 10.0, 10.5, 9.9, 10.0))
	f.round(f.params(nil), qPair).execute()
	if len(f.placed()) != 2 {
		t.Fatalf("配对腿应再下一笔，实得 %v", f.orderKeys())
	}
	pair, _ := f.placed()[1]["signal_id"].(string)
	if pair == want || !strings.HasPrefix(pair, want+":p") {
		t.Fatalf("配对腿键应是 %s:p 且与第一腿不同，实得 %s", want, pair)
	}

	// 同键重放：orders 表 UNIQUE(user_id, signal_id) 必须拦住第二次拨柜台。
	res, err := f.ctrl.PlaceOrder(trading.OrderRequest{
		SignalID: pair, Code: withSuffix(code), Side: trading.SideSell, Price: 10.3, Qty: 500,
		CreatedAt: f.now.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("重放应返回幂等结果而非传输错误: %v", err)
	}
	if res != nil && res.OK {
		t.Fatalf("同键重放不得被当成新单受理：%+v", res)
	}
	if len(f.placed()) != 2 {
		t.Fatalf("同键重放不得再拨一次柜台，实得 %v", f.orderKeys())
	}
}

// TestTLiveSignalConsumedOnce 同一 signal_id 至多产生一条买入腿（§8.4.3 ①）：
// 第一轮回转配平收口（done）后，那条信号仍然"活着"（没被新的翻转替换），
// 第二腿必须开不出来——这正是候选 A 的漏洞（持仓票的持有中信号永远成立）。
func TestTLiveSignalConsumedOnce(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	f.signal(code, "SIG-DUP", f.now.Add(-time.Minute))
	qLow := f.quotes(f.quote(code, "浦发", 9.85, 10.0, 10.4, 9.6, 10.0))
	f.round(f.params(nil), qLow).execute()
	f.mustCount(1)
	if !f.e.tTConsumed("SIG-DUP") {
		t.Fatal("开腿即应把信号记为已被 T 消费")
	}
	key := tLiveKey(code, "buy_first", 1)
	f.legFill(key, code, "买入", 500, 10, 0)
	f.legFill(key+":p", code, "卖出", 500, 10.4, 0)
	f.round(f.params(func(p *paper.TParams) { p.MaxRounds = 3 }), qLow).execute()
	if got := len(f.placed()); got != 1 {
		t.Fatalf("同一 signal_id 不得开出第二条买入腿，实得委托 %v", f.orderKeys())
	}
	if !f.ledger().signalConsumed("SIG-DUP") {
		t.Fatal("消费账必须落在 T 回转记录上（重启后可恢复）")
	}
}

// ── 锁 6/7：不烧槽、不关生命周期、不动锚点 ────────────────────────────────────────────

// TestTLiveNeverBurnsLifecycleOrAnchor T 腿不得置位 realTrimDone、不得走全平类、不得改锚点。
func TestTLiveNeverBurnsLifecycleOrAnchor(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 12) // 锚点 12 明显不同于成本 10：被改动一眼可见
	qHigh := f.quotes(f.quote(code, "浦发", 10.5, 10.0, 10.5, 9.8, 10.0))
	// 反 T 卖腿是最容易被误当"减仓裁决"的一条腿，用它是这条锁最严的形态。
	f.round(f.params(nil), qHigh).execute()
	f.mustCount(1)
	o := f.placed()[0]
	if o["side"] != "卖出" {
		t.Fatalf("应下反 T 卖腿，实得 %+v", o)
	}
	key, _ := o["signal_id"].(string)
	if !strings.HasPrefix(key, tLKeyPrefix) {
		t.Fatalf("T 卖腿必须用 t0: 命名空间（与退出/减仓键正交），实得 %s", key)
	}
	f.legFill(key, code, "卖出", 500, 10.5, 0)
	f.round(f.params(nil), qHigh).execute()

	p, err := f.db.RealPositionByCodeForUser("u_1", withSuffix(code))
	if err != nil || p.Qty <= 0 {
		t.Fatalf("T 卖腿后持仓行必须仍在（不关生命周期），实得 %+v err=%v", p, err)
	}
	if p.AnchorPrice != 12 {
		t.Fatalf("T 腿绝不允许改动卖出裁决锚点：期望 12，实得 %v", p.AnchorPrice)
	}
	f.e.mu.RLock()
	trimmed := f.e.realTrimDone[code]
	f.e.mu.RUnlock()
	if trimmed != "" {
		t.Fatalf("T 腿不得置位 realTrimDone（一次回转不得吞掉当日真正的减仓裁决），实得 %q", trimmed)
	}
	// 全平类键（止损/止盈/m8）一条都不许被这次回转写过：那是"退出裁决已执行"的账。
	for _, class := range fullCloseClasses {
		if n := f.db.SumFilledQty("u_1", realSellSignalID(withSuffix(code), class)); n != 0 {
			t.Fatalf("全平类 %s 被写了 %d 股——T 腿走进了清仓口径", class, n)
		}
	}
}

// ── 锁 8：反 T 留一手 ─────────────────────────────────────────────────────────────────

// TestTLiveSellFirstKeepsHalfAndOneLot 卖腿 ≤50% 持仓、卖后 ≥50% 原持仓且 ≥1 手；
// 把 CapPct 配到 70（放宽方向的手滑）也不许突破 50% 这条硬下限。
func TestTLiveSellFirstKeepsHalfAndOneLot(t *testing.T) {
	t.Run("1000股最多卖500", func(t *testing.T) {
		f := newTLiveFix(t, 10, 30, nil)
		f.seedPosition("600000", "浦发", 1000, 10, 10)
		// 总量帽设成不约束（100%），单独考"留一手 + 单票帽"这一层。
		q := f.quotes(f.quote("600000", "浦发", 10.5, 10.0, 10.5, 9.8, 10.0))
		f.round(f.params(func(p *paper.TParams) { p.CapPct = 70; p.IntradaySellCapTotalPct = 0 }), q).execute()
		f.mustCount(1)
		if qty := f.placed()[0]["qty"].(float64); qty != 500 {
			t.Fatalf("卖腿上限 50%% 原持仓 ⇒ 500 股，实得 %v", qty)
		}
	})
	t.Run("底仓不足1手不开腿", func(t *testing.T) {
		f := newTLiveFix(t, 10, 30, nil)
		f.seedPosition("600000", "浦发", 150, 10, 10) // 50% = 75 股 < 1 手
		q := f.quotes(f.quote("600000", "浦发", 10.5, 10.0, 10.5, 9.8, 10.0))
		f.round(f.params(nil), q).execute()
		f.mustCount(0)
	})
	t.Run("300股卖100留200", func(t *testing.T) {
		f := newTLiveFix(t, 10, 30, nil)
		f.seedPosition("600000", "浦发", 300, 10, 10)
		q := f.quotes(f.quote("600000", "浦发", 10.5, 10.0, 10.5, 9.8, 10.0))
		f.round(f.params(func(p *paper.TParams) { p.IntradaySellCapTotalPct = 0 }), q).execute()
		f.mustCount(1)
		if qty := f.placed()[0]["qty"].(float64); qty != 100 {
			t.Fatalf("300 股最多卖 100 股（整手 + 卖后 ≥50%%），实得 %v", qty)
		}
	})
}

// ── 锁 10 + 不变量 5：last_open_at 停新开、尾盘强制配平与两态回补 ──────────────────────

// TestTLiveLastOpenStopsNewLegs 14:00（默认 t.last_open_at）之后不许开新腿，
// 且这条判定必须以 step3 留痕——14:00–14:50 那 50 分钟是专门留给配平的。
func TestTLiveLastOpenStopsNewLegs(t *testing.T) {
	f := newTLiveFix(t, 14, 10, nil)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	f.signal(code, "SIG-LATE", f.now.Add(-time.Minute))
	f.round(f.params(nil), f.quotes(f.quote(code, "浦发", 9.85, 10.0, 10.4, 9.6, 10.0))).execute()
	f.mustCount(0)
	if _, counts := f.ledger().blockStats(); counts["step3|"+code] == 0 {
		t.Fatalf("新开腿必须计入 step3 时段门统计，实得 %v", counts)
	}
}

// TestTLiveForceUnwindSellsBuyLegEvenAtLoss 尾盘（force_unwind_at 之后）正 T 买腿**无条件**
// 卖出等量底仓：亏损也卖——那是把敞口减回原状，减敞口不需要许可（§8.5.1 唯一保留的时钟义务）。
func TestTLiveForceUnwindSellsBuyLegEvenAtLoss(t *testing.T) {
	f := newTLiveFix(t, 14, 55, nil)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "buy_first", 1)
	f.legFill(key, code, "买入", 500, 10, 0)
	f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: "buy_first", Qty: 500}, 500, 10)
	// 现价 9.7：相对买价是**亏**的（-3%），盘中判据绝不成立 ⇒ 这一笔只可能来自尾盘强制配平。
	f.round(f.params(nil), f.quotes(f.quote(code, "浦发", 9.7, 10.0, 10.4, 9.6, 10.0))).execute()
	f.mustCount(1)
	o := f.placed()[0]
	if o["side"] != "卖出" {
		t.Fatalf("尾盘必须卖出等量底仓，实得 %+v", o)
	}
	if qty := o["qty"].(float64); qty != 500 {
		t.Fatalf("强制配平要卖等量 500 股，实得 %v", qty)
	}
}

// TestTLiveForceUnwindSellFirstNeedsSignal 尾盘反 T 卖腿**只有活跃买入信号才回补**，
// 无信号 ⇒ converted（战略减仓，不是 T 收益）；亏损超过 loss_breaker_pct ⇒ 一律封买回通道。
func TestTLiveForceUnwindSellFirstNeedsSignal(t *testing.T) {
	t.Run("无信号不回补", func(t *testing.T) {
		f := newTLiveFix(t, 14, 55, nil)
		code := "600000"
		f.seedPosition(code, "浦发", 1000, 10, 10)
		key := tLiveKey(code, "sell_first", 1)
		f.legFill(key, code, "卖出", 400, 10.0, 0)
		f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: "sell_first", Qty: 400,
			FrozenCash: 4200}, 400, 10)
		f.round(f.params(nil), f.quotes(f.quote(code, "浦发", 10.2, 10.0, 10.55, 9.8, 10.0))).execute()
		f.mustCount(0) // 绝不追买
		r := f.ledger().roundOf(key)
		if r == nil || r.Status != "converted" {
			t.Fatalf("尾盘无信号应转战略减仓 converted，实得 %+v", r)
		}
		if r.FrozenCash != 0 {
			t.Fatalf("终态必须释放冻结回购款，实得 %.2f", r.FrozenCash)
		}
	})
	t.Run("亏损超阈值封买回", func(t *testing.T) {
		f := newTLiveFix(t, 14, 55, nil)
		code := "600000"
		f.seedPosition(code, "浦发", 1000, 10, 10)
		key := tLiveKey(code, "sell_first", 1)
		f.legFill(key, code, "卖出", 400, 10.0, 0)
		f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: "sell_first", Qty: 400}, 400, 10)
		f.signal(code, "SIG-BB", f.now.Add(-time.Minute)) // 即便有信号，熔断也先拦
		f.round(f.params(nil), f.quotes(f.quote(code, "浦发", 10.5, 10.0, 10.55, 9.8, 10.0))).execute()
		f.mustCount(0)
		r := f.ledger().roundOf(key)
		if r == nil || r.Status != "converted" || !strings.Contains(r.UnwindReason, "买回熔断") {
			t.Fatalf("亏损 >3%% 应走买回熔断并留痕，实得 %+v", r)
		}
	})
	t.Run("有信号才回补", func(t *testing.T) {
		f := newTLiveFix(t, 14, 55, nil)
		code := "600000"
		f.seedPosition(code, "浦发", 1000, 10, 10)
		key := tLiveKey(code, "sell_first", 1)
		f.legFill(key, code, "卖出", 400, 10.0, 0)
		f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: "sell_first", Qty: 400}, 400, 10)
		f.signal(code, "SIG-BB2", f.now.Add(-10*time.Second)) // 晚于开腿时刻 ⇒ 是新一次判断
		f.round(f.params(nil), f.quotes(f.quote(code, "浦发", 10.05, 10.0, 10.55, 9.8, 10.0))).execute()
		f.mustCount(1)
		o := f.placed()[0]
		if o["side"] != "买入" || !strings.HasSuffix(o["signal_id"].(string), ":p") {
			t.Fatalf("尾盘有信号应下配对买回腿（:p 后缀），实得 %+v", o)
		}
	})
}

// ── 不变量 6（Q8）：半态收摊不追买 ────────────────────────────────────────────────────

// TestTLiveBlockedBuyBackNeverChases 盘中配对买回被拦（专款/现金不够）⇒ 显式 unwound 留痕，
// 后续轮次绝不重试、绝不隔夜追买：连跑三轮，委托里不得出现任何 :p 买回腿。
// （尾盘"有信号但闸门不放行"是另一条路径，落 converted，见 TestTLiveForceUnwindSellFirstNeedsSignal。）
func TestTLiveBlockedBuyBackNeverChases(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	f.seedAccount(100000, 1000) // 可用现金只有 1000 元 ⇒ 买回 400 股@10.05 必然不够
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "sell_first", 1)
	f.legFill(key, code, "卖出", 400, 10.0, 0)
	f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: "sell_first", Qty: 400,
		FrozenCash: 4200}, 400, 10)
	q := f.quotes(f.quote(code, "浦发", 9.75, 10.0, 10.5, 9.6, 10.0)) // 回落 2.5% ⇒ 盘中该配对买回
	for i := 0; i < 3; i++ {
		f.round(f.params(nil), q).execute()
	}
	for _, k := range f.orderKeys() {
		if strings.HasPrefix(k, key+":p") {
			t.Fatalf("买回被拦后仍重试了第二腿：%v", f.orderKeys())
		}
	}
	r := f.ledger().roundOf(key)
	if r == nil || r.Status != "unwound" {
		t.Fatalf("买回被拦必须显式 unwound 留痕，实得 %+v", r)
	}
	if !strings.Contains(r.UnwindReason, "买回被拦") {
		t.Fatalf("留痕要写明被拦原因，实得 %q", r.UnwindReason)
	}
	if r.FrozenCash != 0 {
		t.Fatalf("半态收摊必须释放冻结款，实得 %.2f", r.FrozenCash)
	}
}

// ── 不变量 7（§8.4.5 对手方不变量）：跨账号同码反向互斥 ────────────────────────────────

// TestTLiveCrossAccountOppositeLegExclusion 别的账号当日在同码上有未终态的反向腿 ⇒ 本账号不开腿；
// **读数不可得按互斥命中**（坏文件同样不许开）；没有该账号账本时正常放行（对照组，防"永远拦"假绿）。
func TestTLiveCrossAccountOppositeLegExclusion(t *testing.T) {
	code := "600000"
	day := cntime.DayCompactOf(tLiveDay)
	seedFile := `{"day":"` + day + `","rounds":[{"id":"t0:600000:sell_first:` + day +
		`:1","day":"` + day + `","code":"600000","dir":"sell_first","status":"open","qty":300}]}`

	cases := []struct {
		name     string
		body     string
		wantOpen bool
	}{
		{"反向腿在途不开新腿", seedFile, false},
		{"状态文件损坏按互斥命中", "{ not json", false},
		{"无该账号账本则放行", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTLiveFix(t, 10, 30, nil)
			otherRoot := t.TempDir()
			if c.body != "" {
				dir := filepath.Join(otherRoot, "u_other")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "t_rounds_live.json"), []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// 把账本根换成"含另一账号"的那份（生产只有一个账号，这条锁靠夹具造第二个）。
			f.e.mu.Lock()
			rg := newTLiveRegistry(otherRoot)
			rg.nowFn = func() time.Time { return f.now }
			f.e.tLiveReg = rg
			f.e.mu.Unlock()

			f.seedPosition(code, "浦发", 1000, 10, 10)
			f.signal(code, "SIG-MUTEX", f.now.Add(-time.Minute))
			q := f.quotes(f.quote(code, "浦发", 9.85, 10.0, 10.4, 9.6, 10.0))
			f.round(f.params(nil), q).execute()

			got := len(f.placed())
			if c.wantOpen && got != 1 {
				t.Fatalf("对照组应开出一条腿（证明本用例不是靠「永远拦」通过），实得 %d 笔 %v", got, f.orderKeys())
			}
			if !c.wantOpen {
				if got != 0 {
					t.Fatalf("跨账号反向互斥必须不开腿，实得 %v", f.orderKeys())
				}
				if _, counts := f.ledger().blockStats(); counts["step1.5|"+code] == 0 {
					t.Fatal("互斥命中要计入 step1.5 统计（留痕形状与纸面一致）")
				}
			}
		})
	}
}

// ── 不变量 8（锁 12）：熔断日不开新腿 ─────────────────────────────────────────────────

// TestTLiveDayBreakerBlocksBothDirections DayLossLimitPct 命中日两个方向都不开新腿
// （反 T 卖腿在风控闸外始终放行，所以这条只能由编排层自己判，见方案 §2.4）。
func TestTLiveDayBreakerBlocksBothDirections(t *testing.T) {
	f := newTLiveFix(t, 10, 30, func(c *config.QMTConfig) { c.RiskGate.DayLossLimitPct = 3 })
	code := "600006"
	f.seedPosition(code, "亏损票", 2000, 10, 10)
	// 真实卖出亏损成交 1000 股 @7：已实现亏损 (10-7)×1000 = 3000 = 总资产 100000 的 3% ⇒ 命中阈值。
	if err := f.db.ApplyRealFill(store.RealFill{
		OrderID: "GW-LOSS", Code: withSuffix(code), Name: "亏损票", Side: "卖出", Price: 7, Qty: 1000,
		Amount: 7000, TradedAt: cntime.In(f.now).Format("2006-01-02 15:04:05"),
		SignalID: realSellSignalID(withSuffix(code), "止损"), UserID: "u_1",
	}); err != nil {
		t.Fatal(err)
	}
	f.signal(code, "SIG-BRK", f.now.Add(-time.Minute))
	// 均线下方（够格正 T）与均线上方（够格反 T）各跑一次：两向都必须零新开。
	f.round(f.params(nil), f.quotes(f.quote(code, "亏损票", 9.85, 10.0, 10.4, 9.6, 10.0))).execute()
	f.round(f.params(nil), f.quotes(f.quote(code, "亏损票", 10.5, 10.0, 10.55, 9.8, 10.0))).execute()
	f.mustCount(0)
	if _, counts := f.ledger().blockStats(); counts["step0|"] == 0 {
		t.Fatalf("熔断日必须以 step0 全局原因留痕，实得 %v", counts)
	}
}

// ── 不变量 9：关键读数缺失 ⇒ 不开腿并计入统计 ─────────────────────────────────────────

// TestTLiveMissingReadingsFailClosed 逐条制造读数缺口：无实时价 / 分时均线不可信 /
// 分钟链校验超容差 / 流动性缺项（成交额、换手、上市天数）——每条都必须零委托并计入 step 统计。
func TestTLiveMissingReadingsFailClosed(t *testing.T) {
	cases := []struct {
		name  string
		step  string
		tweak func(f *tLiveFix, r *tLiveRound, q *data.StockInfo)
	}{
		{"无实时价", "step0", func(f *tLiveFix, r *tLiveRound, q *data.StockInfo) { r.quotes = map[string]*data.StockInfo{} }},
		{"分时均线不可信", "step0", func(f *tLiveFix, r *tLiveRound, q *data.StockInfo) { q.Volume = 0 }},
		{"分钟链校验不一致", "step4", func(f *tLiveFix, r *tLiveRound, q *data.StockInfo) {
			r.readers.minuteBasis = func(string) (float64, bool) { return 10.2, true } // 主源 10.0 ⇒ 偏差 2% > 0.1%
		}},
		{"成交额不足门槛", "step0.5", func(f *tLiveFix, r *tLiveRound, q *data.StockInfo) {
			q.Volume, q.Amount = 1e6, 1e7 // V 仍是 10（均线可信），但当日成交额只有 1000 万 < 3000 万
		}},
		{"换手率不可得", "step0.5", func(f *tLiveFix, r *tLiveRound, q *data.StockInfo) { q.Turnover = 0 }},
		{"上市天数不可得", "step0.5", func(f *tLiveFix, r *tLiveRound, q *data.StockInfo) {
			r.readers.stockMeta = func(string) (int, bool, bool) { return 0, false, false }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTLiveFix(t, 10, 30, nil)
			code := "600000"
			f.seedPosition(code, "浦发", 1000, 10, 10)
			f.signal(code, "SIG-"+c.name, f.now.Add(-time.Minute))
			q := f.quote(code, "浦发", 9.85, 10.0, 10.4, 9.6, 10.0)
			r := f.round(f.params(nil), f.quotes(q))
			c.tweak(f, r, q)
			r.execute()
			if got := len(f.placed()); got != 0 {
				t.Fatalf("读数缺口必须不开腿，实得 %v", f.orderKeys())
			}
			if _, counts := f.ledger().blockStats(); counts[c.step+"|"+code] == 0 {
				t.Fatalf("缺口必须计入 %s 统计（灰度期要能看出被哪一步拦）", c.step)
			}
		})
	}
}

// ── 锁 11/13 + 不变量 10：重启收敛、幽灵 open、跨日收摊 ───────────────────────────────

// TestTLiveGhostOpenConverges 状态账本说 open、orders/fills 查无对应委托 ⇒
// 不许当"已成交"继续配对，必须标 unwound 留痕并释放冻结款（幽灵 open 必收敛）。
func TestTLiveGhostOpenConverges(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "sell_first", 1)
	// 只落状态、不落委托也不落成交：正是"崩溃前已登记、下单没成功"的形态。
	if err := f.ledger().put(&TRoundLive{
		ID: key, Day: cntime.DayCompactOf(f.now), Code: code, Name: "浦发", Dir: "sell_first",
		Status: "open", Qty: 400, FrozenCash: 4200,
		OpenedAt: f.now.Add(-10 * time.Minute).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	f.round(f.params(nil), f.quotes(f.quote(code, "浦发", 9.85, 10.0, 10.4, 9.6, 10.0))).execute()
	r := f.ledger().roundOf(key)
	if r == nil || r.Status != "unwound" {
		t.Fatalf("幽灵 open 必须收敛成 unwound，实得 %+v", r)
	}
	if !strings.Contains(r.UnwindReason, "查无对应委托") {
		t.Fatalf("留痕要写清是查无委托，实得 %q", r.UnwindReason)
	}
	if r.FrozenCash != 0 {
		t.Fatalf("收敛必须释放冻结款，实得 %.2f", r.FrozenCash)
	}
	f.mustCount(0)
}

// TestTLiveCrossDayRollSeals 昨日仍 open 的记录在首轮加载时先与 orders 对账再显式收摊：
// 正 T 买腿 ⇒ unwound（机动仓成隔夜底仓）、反 T 卖腿 ⇒ converted（战略减仓），
// 且两条都必须继续留在状态文件里（跨日记录不进当日在途查询，但不能被当日落盘抹掉）。
func TestTLiveCrossDayRollSeals(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	yesterday := cntime.DayCompactOf(f.now.AddDate(0, 0, -1))
	yDash := cntime.DayOf(f.now.AddDate(0, 0, -1))
	buyKey := "t0:600001:buy_first:" + yesterday + ":1"
	sellKey := "t0:600002:sell_first:" + yesterday + ":1"
	for _, k := range []string{buyKey, sellKey} {
		if _, err := f.db.UpsertRealOrder(store.RealOrder{
			OrderID: "GW-" + k, SignalID: k, Code: withSuffix("600001"), Side: "买入", Status: "已成",
			Price: 10, Qty: 500, CreatedAt: yDash + " 10:00:00", UserID: "u_1", IntradayT: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.ApplyRealFill(store.RealFill{OrderID: "GW-" + buyKey, Code: "600001.SH", Name: "隔夜票",
		Side: "买入", Price: 10, Qty: 500, Amount: 5000, TradedAt: yDash + " 10:00:00",
		SignalID: buyKey, UserID: "u_1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ApplyRealFill(store.RealFill{OrderID: "GW-" + sellKey, Code: "600001.SH", Name: "隔夜票",
		Side: "卖出", Price: 10, Qty: 500, Amount: 5000, TradedAt: yDash + " 10:05:00",
		SignalID: sellKey, UserID: "u_1"}); err != nil {
		t.Fatal(err)
	}
	// 直接写状态文件模拟"重启后首轮加载"。
	path := filepath.Join(f.root, "u_1", "t_rounds_live.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"day":"` + yesterday + `","rounds":[` +
		`{"id":"` + buyKey + `","day":"` + yesterday + `","code":"600001","name":"隔夜票","dir":"buy_first","status":"open","qty":500,"opened_at":"` + yDash + `T10:00:00+08:00"},` +
		`{"id":"` + sellKey + `","day":"` + yesterday + `","code":"600001","name":"隔夜票","dir":"sell_first","status":"open","qty":500,"frozen_cash":5300,"opened_at":"` + yDash + `T10:05:00+08:00"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// 换一个新 registry 模拟进程重启（registry 按日只 load 一次）。
	f.e.mu.Lock()
	rg := newTLiveRegistry(f.root)
	rg.nowFn = func() time.Time { return f.now }
	f.e.tLiveReg = rg
	f.e.mu.Unlock()

	f.round(f.params(nil), f.quotes(f.quote("600003", "无关票", 10.0, 10.0, 10.4, 9.6, 10.0))).execute()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got tLiveState
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("收摊后状态文件必须仍可解析: %v", err)
	}
	byID := map[string]*TRoundLive{}
	for _, r := range got.Rounds {
		byID[r.ID] = r
	}
	if byID[buyKey] == nil || byID[buyKey].Status != "unwound" {
		t.Fatalf("正 T 跨日买腿应标 unwound，实得 %+v", byID[buyKey])
	}
	if byID[sellKey] == nil || byID[sellKey].Status != "converted" {
		t.Fatalf("反 T 跨日卖腿应标 converted（战略减仓），实得 %+v", byID[sellKey])
	}
	if byID[sellKey].FrozenCash != 0 {
		t.Fatalf("跨日收摊必须释放冻结款，实得 %.2f", byID[sellKey].FrozenCash)
	}
	if open := f.ledger().openRounds(); len(open) != 0 {
		t.Fatalf("昨日记录不得参与当日在途查询（跨日不复活），实得 %d 条", len(open))
	}
}

// ── 不变量 11 + §8.9a：做 T 收益独立口径 ──────────────────────────────────────────────

// TestTLivePnLIsStandaloneOnlyDone 做 T 收益只在 done 轮算，字段齐（毛/净/配平量/费用估算标记）：
// 两腿都有券商回报费用 ⇒ FeeEst=false 且净额按实测扣；都没有 ⇒ 同式估算并标 FeeEst=true。
func TestTLivePnLIsStandaloneOnlyDone(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "buy_first", 1)
	f.legFill(key, code, "买入", 500, 10, 6.25)
	f.legFill(key+":p", code, "卖出", 500, 10.4, 8.2)
	f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: "buy_first", Qty: 500}, 500, 10)
	f.round(f.params(nil), f.quotes(f.quote(code, "浦发", 10.4, 10.0, 10.55, 9.8, 10.0))).execute()

	r := f.ledger().roundOf(key)
	if r == nil || r.Status != "done" {
		t.Fatalf("两腿配平后应收尾成 done，实得 %+v", r)
	}
	if r.QMatched != 500 {
		t.Fatalf("配平量应为 min(买,卖)=500，实得 %d", r.QMatched)
	}
	if !tLApprox(r.TPnlGross, 200) {
		t.Fatalf("毛价差应为 (10.40-10.00)×500=200，实得 %.2f", r.TPnlGross)
	}
	if !tLApprox(r.TPnlNet, 200-6.25-8.2) {
		t.Fatalf("净价差应扣两腿真实回报费用，期望 %.2f 实得 %.2f", 200-6.25-8.2, r.TPnlNet)
	}
	if r.FeeEst {
		t.Fatal("两腿都有券商回报费用时不得标 FeeEst")
	}
	net, gross, doneRounds, estShare := f.ledger().dayPnL()
	if doneRounds != 1 || !tLApprox(gross, 200) || !tLApprox(net, 185.55) || estShare != 0 {
		t.Fatalf("日聚合只统计 done 轮：net=%.2f gross=%.2f done=%d est=%.2f", net, gross, doneRounds, estShare)
	}

	f2 := newTLiveFix(t, 10, 30, nil)
	c2 := "600001"
	f2.seedPosition(c2, "无费票", 1000, 10, 10)
	k2 := tLiveKey(c2, "buy_first", 1)
	f2.legFill(k2, c2, "买入", 500, 10, 0)
	f2.legFill(k2+":p", c2, "卖出", 500, 10.4, 0)
	f2.openRound(&TRoundLive{ID: k2, Code: c2, Name: "无费票", Dir: "buy_first", Qty: 500}, 500, 10)
	f2.round(f2.params(nil), f2.quotes(f2.quote(c2, "无费票", 10.4, 10.0, 10.55, 9.8, 10.0))).execute()
	r2 := f2.ledger().roundOf(k2)
	if r2 == nil || !r2.FeeEst {
		t.Fatalf("没有真实回报费用时必须标 FeeEst=true，实得 %+v", r2)
	}
	if !(r2.TPnlNet < r2.TPnlGross) {
		t.Fatalf("估算费用必须真的从净额里扣掉：gross=%.2f net=%.2f", r2.TPnlGross, r2.TPnlNet)
	}
	if _, _, done, share := f2.ledger().dayPnL(); done != 1 || share != 1 {
		t.Fatalf("估算占比要能被统计出来（对账口径稳不稳的读数）：done=%d share=%.2f", done, share)
	}
}

// TestTLiveFrozenCashReleasedOnEveryTerminal 终态一律释放冻结回购款：
// 目标仓位模型按 bookValue 扣它，漏一条就等于让这笔钱在账户里永久被"借走"。
func TestTLiveFrozenCashReleasedOnEveryTerminal(t *testing.T) {
	cases := []struct {
		name   string
		hh, mm int
		price  float64
		signal bool
		want   string
	}{
		{"盘中配对完成", 10, 30, 9.75, false, "done"},
		{"尾盘有信号回补", 14, 55, 10.05, true, "done"},
		{"尾盘无信号转换", 14, 55, 10.05, false, "converted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTLiveFix(t, c.hh, c.mm, nil)
			code := "600000"
			f.seedPosition(code, "浦发", 1000, 10, 10)
			key := tLiveKey(code, "sell_first", 1)
			f.legFill(key, code, "卖出", 400, 10.0, 0)
			f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: "sell_first", Qty: 400,
				FrozenCash: 4200}, 400, 10)
			before := f.ledger().bookValue(map[string]float64{code: 10}).FrozenCash
			if before <= 0 {
				t.Fatalf("在途反 T 腿必须冻结回购款，实得 %.2f", before)
			}
			if c.signal {
				f.signal(code, "SIG-FZ", f.now.Add(-5*time.Second))
			}
			q := f.quotes(f.quote(code, "浦发", c.price, 10.0, 10.55, 9.6, 10.0))
			f.round(f.params(nil), q).execute()
			if c.want == "done" {
				// 买回腿先落委托、再由回报成交进账本：补一笔成交后下一轮才收口成 done。
				f.fillPlaced(len(f.placed()) - 1)
				f.round(f.params(nil), q).execute()
			}
			r := f.ledger().roundOf(key)
			if r == nil || r.Status != c.want {
				t.Fatalf("期望终态 %s，实得 %+v", c.want, r)
			}
			if r.FrozenCash != 0 {
				t.Fatalf("终态后冻结款必须归零，实得 %.2f", r.FrozenCash)
			}
			if after := f.ledger().bookValue(map[string]float64{code: 10}).FrozenCash; after != 0 {
				t.Fatalf("净额口径仍带着已收摊腿的冻结款：%.2f", after)
			}
		})
	}
}

// ── 不变量 12（锁 1/2 的编排侧）：净额标志随单 ─────────────────────────────────────────

// TestTLiveLegsCarryIntradayTFlag 四条腿（正 T 买/卖、反 T 卖/买回）落库的委托都必须带
// IntradayT=true：集中度净额化的复核在 risk 层，编排层的责任是**别把标志漏掉**。
func TestTLiveLegsCarryIntradayTFlag(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	a, b := "600001", "600002"
	f.seedPosition(a, "正T票", 1000, 10, 10)
	f.seedPosition(b, "反T票", 1000, 10, 10)
	f.signal(a, "SIG-N1", f.now.Add(-time.Minute))
	qs := f.quotes(
		f.quote(a, "正T票", 9.85, 10.0, 10.4, 9.6, 10.0),
		f.quote(b, "反T票", 10.5, 10.0, 10.5, 9.8, 10.0),
	)
	f.round(f.params(nil), qs).execute()
	if len(f.placed()) != 2 {
		t.Fatalf("应开两条第一腿，实得 %v", f.orderKeys())
	}
	// 两腿都成交后把行情切到配对区，逼出两条配对腿（正 T 卖、反 T 买）。
	f.fillPlaced(0)
	f.fillPlaced(1)
	qs2 := f.quotes(
		f.quote(a, "正T票", 10.1, 10.0, 10.4, 9.6, 10.0),
		f.quote(b, "反T票", 10.25, 10.0, 10.55, 9.8, 10.0),
	)
	f.round(f.params(nil), qs2).execute()
	if len(f.placed()) != 4 {
		t.Fatalf("应补两条配对腿，实得 %v", f.orderKeys())
	}
	// 断言落在**拨给柜台的请求体**上：风控集中度闸吃的就是这条随单标志（controller 把它原样
	// 装进 risk.LiveOrder）。orders.intraday_t 列的落库由 controller 的占位行写入负责，
	// 本批不改 trading（已在交付说明里上报"占位行未带该列"这一条）。
	seen := 0
	for _, o := range f.placed() {
		key, _ := o["signal_id"].(string)
		if !strings.HasPrefix(key, tLKeyPrefix) {
			continue
		}
		seen++
		if flag, _ := o["intraday_t"].(bool); !flag {
			t.Fatalf("T 腿委托 %s 漏带 IntradayT 标志（集中度会按全额计，灰度账号永远开不了 T）", key)
		}
	}
	if seen != 4 {
		t.Fatalf("应拨出 4 条 T 腿委托（两第一腿 + 两配对腿），实得 %d：%v", seen, f.orderKeys())
	}
}

// ── 不变量 13：时段与开关 ─────────────────────────────────────────────────────────────

// TestTLiveSessionGateDoesNotDrive 集合竞价/盘后（IsActiveSession 为假）一律不驱动：
// 既不开腿也不配对，账本状态一个字都不改。
func TestTLiveSessionGateDoesNotDrive(t *testing.T) {
	f := newTLiveFix(t, 15, 30, nil) // 盘后
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	key := tLiveKey(code, "buy_first", 1)
	f.legFill(key, code, "买入", 500, 10, 0)
	f.openRound(&TRoundLive{ID: key, Code: code, Name: "浦发", Dir: "buy_first", Qty: 500}, 500, 10)
	f.signal(code, "SIG-S", f.now.Add(-time.Minute))
	qs := f.quotes(f.quote(code, "浦发", 10.4, 10.0, 10.55, 9.8, 10.0))
	f.e.runIntradayTLive("u_1", f.ctrl, qs) // 驱动入口自己判时段
	f.mustCount(0)
	if r := f.ledger().roundOf(key); r == nil || r.Status != "open" {
		t.Fatalf("盘后不得动状态，实得 %+v", r)
	}
}

// TestTLiveDisabledStopsNewLegsButKeepsInFlight t.enabled=false 一键止血：当轮即停开新腿，
// 但在途腿照常配平/收摊，且不撤在途单（假网关只记 /order，收到的委托数为 1 即证明只动了配对）。
func TestTLiveDisabledStopsNewLegsButKeepsInFlight(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	a, b := "600001", "600002" // a=在途正 T 买腿该配对；b=够格开新腿，止血时必须不开
	f.seedPosition(a, "在途票", 1000, 10, 10)
	f.seedPosition(b, "新腿票", 1000, 10, 10)
	key := tLiveKey(a, "buy_first", 1)
	f.legFill(key, a, "买入", 500, 10, 0)
	f.openRound(&TRoundLive{ID: key, Code: a, Name: "在途票", Dir: "buy_first", Qty: 500}, 500, 10)
	f.signal(b, "SIG-B-OFF", f.now.Add(-time.Minute))
	qs := f.quotes(
		f.quote(a, "在途票", 10.4, 10.0, 10.55, 9.8, 10.0),
		f.quote(b, "新腿票", 9.85, 10.0, 10.4, 9.6, 10.0),
	)
	f.round(f.params(func(p *paper.TParams) { p.Enabled = false }), qs).execute()

	keys := f.orderKeys()
	if len(keys) != 1 || keys[0] != key+":p" {
		t.Fatalf("止血轮必须只推进在途腿的配对（%s:p），实得 %v", key, keys)
	}
	if f.ledger().tRoundCountFor(b) != 0 {
		t.Fatal("止血轮不得开出新腿")
	}
	if r := f.ledger().roundOf(key); r == nil || r.Status != "open" {
		t.Fatalf("在途腿保持 open 等回报（不撤在途单），实得 %+v", r)
	}
}

// TestTLiveTQuotaZeroBlocksBothDirections 满仓市（MS≥1.0 ⇒ T 仓额度 TQ=0）两向都不开回转：
// §8.1 表里"大牛市：T 仓额度 0（无正 T 现金；反 T 也无额度）"这一行。
// 只给买腿上 TQ 帽的话，"反 T 先卖"会在该关门的日子里变成第二条减仓通道。
func TestTLiveTQuotaZeroBlocksBothDirections(t *testing.T) {
	f := newTLiveFix(t, 10, 30, nil)
	code := "600000"
	f.seedPosition(code, "浦发", 1000, 10, 10)
	f.signal(code, "SIG-TQ", f.now.Add(-time.Minute))
	f.round(f.params(func(p *paper.TParams) { p.MarketScale = 1.0 }),
		f.quotes(f.quote(code, "浦发", 9.85, 10.0, 10.4, 9.6, 10.0))).execute()
	f.round(f.params(func(p *paper.TParams) { p.MarketScale = 1.0 }),
		f.quotes(f.quote(code, "浦发", 10.5, 10.0, 10.5, 9.8, 10.0))).execute()
	f.mustCount(0)
	if _, counts := f.ledger().blockStats(); counts["step7|"+code] == 0 {
		t.Fatalf("T 仓额度为 0 必须以 step7 额度门留痕，实得 %v", counts)
	}
}

// TestTLiveAnchorProvenanceGatesTicket §8.4.5 的实盘形态：只有首买价来源的锚点才允许开 T 腿。
//
// 这条锁的存在理由是"回填让实盘账看起来有锚点"这件事太容易发生：W3 的存量回填会把
// cost_price 写进 anchor_price（那是为了让卖出裁决有基准，方案 §1.6a 明写它**不构成 T 资格**）。
// 闸门若只看 AnchorPrice>0，一次回填就给全账户开了日内回转的许可——而摊薄成本会被加仓
// 与 T 腿自身改动，等于让一笔日内低吸去改写自己的退出基准，正是裁决二终结过的缺陷从实盘侧绕回来。
//
// English: provenance, not mere presence, grants the live T ticket; a cost-derived anchor never does.
func TestTLiveAnchorProvenanceGatesTicket(t *testing.T) {
	// ① 判据本身逐档扫（含"来源列为空的存量行"——那必须与 none 同判，不能当可证）。
	cases := []struct {
		name   string
		source string
		want   bool
	}{
		{"首成交建行的锚点可开 T", store.AnchorSourceFirstBuyOrder, true},
		{"层账首买价来源可开 T", store.AnchorSourceFirstLot, true},
		{"成本回落档不可开 T", store.AnchorSourceCostFallback, false},
		{"明确无从回填不可开 T", store.AnchorSourceNone, false},
		{"来源列上线前的存量行不可开 T", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pos := store.RealPosition{TsCode: "600000.SH", Qty: 2000, CostPrice: 10, AnchorPrice: 10, AnchorSource: c.source}
			if got := pos.AnchorTicket(); got != c.want {
				t.Fatalf("AnchorTicket()=%v，期望 %v（source=%q）", got, c.want, c.source)
			}
		})
	}
	if (store.RealPosition{AnchorPrice: 0, AnchorSource: store.AnchorSourceFirstBuyOrder}).AnchorTicket() {
		t.Fatalf("anchor_price=0 不得给 T 资格（两个条件缺一不可）")
	}

	// ② 真跑一遍编排：同一形态的票，只是来源不同 ⇒ 一个开腿、一个零开腿并留痕。
	// 只比"来源"这一个维度：行情、信号、额度、时段全部走默认桩，差异若出现在别处就是用例写歪了。
	for _, c := range []struct {
		name     string
		source   string
		wantOpen bool
	}{
		{"首买价来源照常开腿", store.AnchorSourceFirstBuyOrder, true},
		{"成本来源不得开腿", store.AnchorSourceCostFallback, false},
		{"来源未知的存量行不得开腿", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newTLiveFix(t, 10, 30, nil)
			code := "600002"
			f.seedPositionSource(code, "成本票", 4000, 10.0, 10.0, c.source)
			f.signal(code, "SIG-ANCHOR", f.now.Add(-time.Minute))
			r := f.round(f.params(nil), f.quotes(f.quote(code, "成本票", 9.85, 10.0, 10.4, 9.6, 10.0)))
			r.execute()
			placed := len(f.placed())
			if c.wantOpen && placed == 0 {
				t.Fatalf("首买价来源的合格候选必须能开腿，实得零委托（被拦：%v）", f.orderKeys())
			}
			if !c.wantOpen && placed != 0 {
				t.Fatalf("来源不可证的票不得开 T 腿，实得委托 %v", f.orderKeys())
			}
			if !c.wantOpen {
				if _, counts := f.ledger().blockStats(); counts["step0|"+code] == 0 {
					t.Fatalf("拒开腿必须计入 step0 留痕（灰度期要能看出被哪一步拦）")
				}
			}
		})
	}
}
