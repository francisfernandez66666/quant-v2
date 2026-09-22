// n3_partfill_sell_20260922_test.go — §N-3（2026-09-22 傍晚批复验）卖单部成后补卖的行为用例。
//
// 缺陷形态：`SumOpenSellQty` 旧口径把 `部成` 委托按**整笔委托量**计入在途，而调用方
// realSoldOrOpenQtyToday 的算式是「持仓 − Σ已成交 − Σ在途」，其中 Σ已成交 已经数过该单的成交腿
// （ApplyRealFill 卖成交也即时扣减 real_positions.qty）→ 同一笔成交扣两次 → 剩余量被压成 0/负数
// → autoExecuteRealSellsRound 的 `remaining <= 0 continue` 当天永不补卖（少卖 = 该退的仓位留过夜）。
//
// 本文件锁住修复后的行为（各自独立账本，避免上一轮的在途单污染本轮剩余量）：
//   - 挂 1000 / 部成 500 → 以 500 补卖（幂等键 :r500）；
//   - 挂 1000 / 累计部成 800 → 以 200 补卖（:r200）；
//   - 挂 1000 / 全部成交（终态 已成）→ 不再发单；
//   - 同剩余量重放 → 不重复刷单（§修复 R6 的 :r<剩余量> 桶在净额口径下仍然成立）；
//   - 减仓类在途单部成后，止损全平建议按「持仓 − 在途未成交余量」精确补满（新口径下算式
//     与账面完全对齐：2000 持仓、在途 1000 已部成 500 → 补卖 1000，旧口径只卖 500）。
//
// 台账构造说明（为什么持仓是 2000−已成交）：ApplyRealFill 卖成交即时扣 qty，所以「挂 1000
// 部成 500」的账面真实形态就是 持仓 2000→1500 + fills 500 + 委托行 部成。成交一律走
// ApplyRealFill（不手改 qty），委托行按 §UAT-D4 既有铺法显式落库（created_at 用北京当日），
// 避免运行时刻跨零点时时区漂移让断言失去意义。
//
// English: §N-3 behavior locks — a partially filled sell ticket occupies only its unfilled
// remainder, so the same-day top-up fires (500 after 500-of-1000, 200 after 800-of-1000, exactly
// 1000 for the stop-loss exit behind a half-filled trim ticket), a fully filled terminal ticket
// sends nothing, and an unchanged remainder never re-fires.
package engine

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// n3Executor 只记录报单体的假执行器：卖出腿必须看得见 signal_id 幂等键与数量，
// 才能断言"以多少股补卖"，光数下单次数不够。
type n3Executor struct {
	mu    sync.Mutex
	sells []trading.OrderRequest
}

func (x *n3Executor) PlaceBuy(trading.OrderRequest) (*trading.OrderResult, error) {
	return &trading.OrderResult{OK: true, OrderID: "GW-BUY"}, nil
}

func (x *n3Executor) PlaceSell(r trading.OrderRequest) (*trading.OrderResult, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.sells = append(x.sells, r)
	return &trading.OrderResult{OK: true, OrderID: "GW-SELL"}, nil
}

func (x *n3Executor) Cancel(string) error { return nil }

func (x *n3Executor) State() (*trading.GatewayState, error) {
	return &trading.GatewayState{Connected: true}, nil
}

func (x *n3Executor) Health() (bool, error) { return true, nil }

func (x *n3Executor) seen() []trading.OrderRequest {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]trading.OrderRequest(nil), x.sells...)
}

// n3Book 一本 §N-3 台账：执行器 + 引擎 + 实盘账本 + 北京当日 + 止损幂等键 base。
// 复用 §M12/h4 修复批的账号口径（u_1 与控制器账号一致，多租户守卫不参与本用例）。
type n3Book struct {
	exec    *n3Executor
	e       *Engine
	db      *store.DB
	userID  string
	today   string
	baseSL  string // sell:600000:止损:<交易日>（与执行侧同一构造函数）
	tsCode  string
	ticket  string // 当日挂出的那笔卖单完整幂等键（含 :r 桶）
	ticketQ int    // 该笔委托量
	fillSeq int    // 同一笔委托的多条成交腿序号：fills 幂等判重键含 traded_at，同秒同量会被
	//               当成重复回报丢弃（真实回报不会同秒同量），用它把每条腿的时刻错开
}

func newN3Book(t *testing.T, ticketSID string, ticketQty int) *n3Book {
	t.Helper()
	realDB, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	t.Cleanup(func() { realDB.Close() })
	b := &n3Book{exec: &n3Executor{}, db: realDB, userID: "u_1",
		today: cntime.In(time.Now()).Format("2006-01-02"), tsCode: "600000.SH",
		ticket: ticketSID, ticketQ: ticketQty}
	b.e = h4EnvWithDB(t, b.exec, realDB)
	b.baseSL = realSellSignalID(b.tsCode, "止损")
	return b
}

// h4EnvWithDB 与 h4Env 同装配，但账本由调用方指定（同一本账要跨多轮/多次断言复用）。
func h4EnvWithDB(t *testing.T, exec trading.Executor, realDB *store.DB) *Engine {
	t.Helper()
	cfg := n3Cfg()
	ctrl := trading.NewController(exec, realDB, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, realDB)
	return e
}

// n3Cfg auto + auto_sell 全开的实盘配置（与 h4Env 同口径）。
func n3Cfg() config.QMTConfig {
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.Mode = "auto"
	cfg.AutoSell = true
	return cfg
}

// seed 建多一只 2000 股 @10 的持仓（归属 u_1）。
func (b *n3Book) seed(t *testing.T, qty int) {
	t.Helper()
	if _, err := b.db.UpsertRealPositions([]store.RealPosition{
		{TsCode: b.tsCode, Name: "补卖测试", Qty: qty, CostPrice: 10, Amount: float64(10 * qty),
			HighestPrice: 10, UserID: b.userID},
	}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
}

// hang 挂出一笔当日卖单（status 已报，created_at=北京当日），模拟"上一轮已被券商受理"的在途单。
func (b *n3Book) hang(t *testing.T, sid string, qty int) {
	t.Helper()
	if _, err := b.db.UpsertRealOrder(store.RealOrder{OrderID: "pend:" + sid, SignalID: sid,
		Code: b.tsCode, Side: "卖出", Status: "已报", Price: 11, Qty: qty,
		CreatedAt: b.today + "T09:35:00+08:00", UserID: b.userID}); err != nil {
		t.Fatalf("seed open sell %s: %v", sid, err)
	}
}

// fill 一笔卖成交经真实路径落账（写 fills + 即时扣减持仓），并按成交推进委托状态。
func (b *n3Book) fill(t *testing.T, sid, orderID string, qty int, status string) {
	t.Helper()
	// 逐条成交腿把 traded_at 错开 1 秒：fills 的幂等判重键含 (order_id, traded_at, price, qty)，
	// 同一笔委托的两条**等量**成交腿若同秒同价同量会被判成重复回报而整条丢弃（真实柜台回报不会
	// 同秒），断言「累计部成 1000」就会静默停在 500 —— 这是台账自校验要拦的第一类假绿。
	b.fillSeq++
	stamp := fmt.Sprintf("%sT09:40:%02d", b.today, b.fillSeq)
	if err := b.db.ApplyRealFill(store.RealFill{OrderID: orderID, Code: b.tsCode, Side: "卖出",
		Price: 11, Qty: qty, Amount: 11 * float64(qty), TradedAt: stamp,
		SignalID: sid, UserID: b.userID}); err != nil {
		t.Fatalf("apply sell fill %s: %v", sid, err)
	}
	if status != "" {
		if _, err := b.db.AdvanceRealOrderStatus(b.userID, sid, status); err != nil {
			t.Fatalf("advance status %s: %v", sid, err)
		}
	}
}

// round 跑一轮自动卖出执行（建议由调用方给），返回本轮真实报出的卖单。
func (b *n3Book) round(t *testing.T, adv trading.PositionAdvice) []trading.OrderRequest {
	t.Helper()
	before := len(b.exec.seen())
	e := b.e
	e.autoExecuteRealSellsRound(b.userID, e.QMTController(), b.db, []trading.PositionAdvice{adv},
		e.sellUnifiedModeEngine())
	all := b.exec.seen()
	return all[before:]
}

// assertLedger 台账自校验：修复生效的前提是账面形态符合预期（持仓已扣、成交已记、在途按净额），
// 否则"补卖多少"的断言毫无意义。
func (b *n3Book) assertLedger(t *testing.T, wantQty, wantFilled, wantOpenNet int) {
	t.Helper()
	p, err := b.db.RealPositionByCodeForUser(b.userID, b.tsCode)
	if err != nil || p.Qty != wantQty {
		t.Fatalf("持仓应为 %d（成交即时扣减）: %+v err=%v", wantQty, p, err)
	}
	if got := b.db.SumFilledQty(b.userID, b.baseSL); got != wantFilled {
		t.Fatalf("Σ已成交(全平类)应为 %d, got %d", wantFilled, got)
	}
	if got := b.db.SumOpenSellQty(b.userID, b.tsCode, b.today); got != wantOpenNet {
		t.Fatalf("Σ在途（未成交余量）应为 %d, got %d", wantOpenNet, got)
	}
}

// TestN3PartfillTopUpAfterPartialFill §N-3 核心三段式：
//  1. 挂 1000 部成 500 → 以 500 补卖（旧口径在途按整笔 1000 计 → 剩余量 0 → 一单不发）；
//  2. 同一笔继续部成到 800 → 以 200 补卖；
//  3. 全部成交（终态）→ 不再发单。
//
// English: 500 of a 1000-share ticket filled → top up 500; 800 filled → top up 200;
// fully filled and terminal → send nothing.
func TestN3PartfillTopUpAfterPartialFill(t *testing.T) {
	// ① 部成 500 → 补 500
	b := newN3Book(t, "", 0)
	b.seed(t, 2000)
	sid := b.baseSL + ":r1000"
	b.hang(t, sid, 1000)
	b.fill(t, sid, "GW-T1", 500, "部成")
	b.assertLedger(t, 1500, 500, 500)
	sells := b.round(t, n3StopLossAdvice())
	if len(sells) != 1 || sells[0].Qty != 500 {
		t.Fatalf("① 部成 500 后应以 500 补卖, got %+v", sells)
	}
	if want := b.baseSL + ":r500"; sells[0].SignalID != want {
		t.Fatalf("① 补卖幂等键应随剩余量刷新为 %s, got %s", want, sells[0].SignalID)
	}

	// ② 累计部成 800 → 补 200（新账本，避免①的补卖单继续占在途额度）
	b2 := newN3Book(t, "", 0)
	b2.seed(t, 2000)
	sid2 := b2.baseSL + ":r1000"
	b2.hang(t, sid2, 1000)
	b2.fill(t, sid2, "GW-T2", 500, "部成")
	b2.fill(t, sid2, "GW-T2", 300, "部成")
	b2.assertLedger(t, 1200, 800, 200)
	if sells := b2.round(t, n3StopLossAdvice()); len(sells) != 1 || sells[0].Qty != 200 {
		t.Fatalf("② 累计部成 800 后应以 200 补卖, got %+v", sells)
	}

	// ③ 全部成交 + 终态 → 不再发单（在途额度整体回补）
	b3 := newN3Book(t, "", 0)
	b3.seed(t, 2000)
	sid3 := b3.baseSL + ":r1000"
	b3.hang(t, sid3, 1000)
	b3.fill(t, sid3, "GW-T3", 500, "部成")
	b3.fill(t, sid3, "GW-T3", 500, "已成")
	b3.assertLedger(t, 1000, 1000, 0)
	if sells := b3.round(t, n3StopLossAdvice()); len(sells) != 0 {
		t.Fatalf("③ 全成（终态）后不得再发卖单, got %+v", sells)
	}
}

// TestN3StopLossTopsUpFullRemainderBehindHalfFilledTrim §N-3 的"账实精确对齐"形态：
// 减仓单 1000 部成 500 后仍挂在途，本轮止损建议要全平 2000 股持仓——
//   - 新口径：剩余 = 持仓 1500 − 0（减仓类成交不计入全平类 Σ已成交）− 在途未成交 500 = 1000
//     → 补卖 1000，与"账面还能卖多少"逐字相等；
//   - 旧口径：在途按整笔 1000 占额 → 剩余 500 → 少卖 500 股（该退的量留过夜）。
//
// English: with a half-filled trim ticket still open, the stop-loss exit tops up the full 1000
// shares the book can still sell (the old whole-order term cut it to 500).
func TestN3StopLossTopsUpFullRemainderBehindHalfFilledTrim(t *testing.T) {
	b := newN3Book(t, "", 0)
	b.seed(t, 2000)
	trimBase := realSellSignalID(b.tsCode, "减仓")
	trimSID := trimBase + ":r1000"
	b.hang(t, trimSID, 1000)
	b.fill(t, trimSID, "GW-TRIM", 500, "部成")
	// 减仓类的成交不进全平类 Σ已成交（fullCloseClasses 只含 止损/止盈/m8）
	if got := b.db.SumFilledQty(b.userID, b.baseSL); got != 0 {
		t.Fatalf("前置：全平类 Σ已成交应为 0, got %d", got)
	}
	b.assertLedger(t, 1500, 0, 500)
	sells := b.round(t, n3StopLossAdvice())
	if len(sells) != 1 || sells[0].Qty != 1000 {
		t.Fatalf("§N-3 止损全平应以 1000 补卖（旧口径给 500=少卖 500 股）, got %+v", sells)
	}
	if want := b.baseSL + ":r1000"; sells[0].SignalID != want {
		t.Fatalf("补卖幂等键应为 %s, got %s", want, sells[0].SignalID)
	}
}

// TestN3SameRemainderDoesNotRefire §修复 R6 幂等桶复核（净额口径下的行为锁）：
// 补卖单一旦被受理，它自己就等额抬高 Σ在途 → 同轮/下轮重放的剩余量只会更小，绝不因
// "剩余量没变"再刷一单（§R6「同剩余量不重复刷单」这条不能被本批改坏）。
// English: once a top-up is accepted it itself occupies open quantity, so replayed rounds see a
// smaller remainder and never re-fire (the :r bucket stays spam-free under the net definition).
func TestN3SameRemainderDoesNotRefire(t *testing.T) {
	b := newN3Book(t, "", 0)
	b.seed(t, 2000)
	sid := b.baseSL + ":r1000"
	b.hang(t, sid, 1000)
	b.fill(t, sid, "GW-T4", 500, "部成")
	if sells := b.round(t, n3StopLossAdvice()); len(sells) != 1 || sells[0].Qty != 500 {
		t.Fatalf("首轮应以 500 补卖, got %+v", sells)
	}
	// 补卖单已挂在途：500（首单未成交）+500（补卖单）=1000 与持仓 1500 相对 → 剩余 0
	b.assertLedger(t, 1500, 500, 1000)
	for i := 0; i < 3; i++ {
		if again := b.round(t, n3StopLossAdvice()); len(again) != 0 {
			t.Fatalf("§N-3 在途已占额度时不得重复刷单, 第%d次重放 got %+v", i+1, again)
		}
	}
}

// TestN3OpenSellQtyExcludesStaleAndTerminal §N-3 口径边界（引擎侧链路版）：昨日在途单、
// 买入方向、发送失败占位行都不该占当日卖单额度——否则补卖量又被无故压低。
// English: yesterday's tickets, buy-side rows and send-failed placeholders never occupy today's
// open-sell budget (they would suppress the top-up again).
func TestN3OpenSellQtyExcludesStaleAndTerminal(t *testing.T) {
	b := newN3Book(t, "", 0)
	b.seed(t, 1000)
	// 昨日挂的 500 股卖单（A 股委托当日失效）+ 今日发送失败的 300 股占位行
	staleDay := cntime.In(time.Now().AddDate(0, 0, -3)).Format("2006-01-02")
	if _, err := b.db.UpsertRealOrder(store.RealOrder{OrderID: "GW-OLD", SignalID: b.baseSL + ":r500:yesterday",
		Code: b.tsCode, Side: "卖出", Status: "已报", Price: 11, Qty: 500,
		CreatedAt: staleDay + "T14:55:00+08:00", UserID: b.userID}); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if _, err := b.db.UpsertRealOrder(store.RealOrder{OrderID: "pend:" + b.baseSL + ":failed", SignalID: b.baseSL + ":failed",
		Code: b.tsCode, Side: "卖出", Status: "发送失败", Price: 11, Qty: 300,
		CreatedAt: b.today + "T09:31:00+08:00", UserID: b.userID}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	if got := b.db.SumOpenSellQty(b.userID, b.tsCode, b.today); got != 0 {
		t.Fatalf("跨日/发送失败行不得占当日在途卖量, got %d", got)
	}
	sells := b.round(t, n3StopLossAdvice())
	if len(sells) != 1 || sells[0].Qty != 1000 {
		t.Fatalf("无有效在途时应按持仓全额卖出 1000, got %+v", sells)
	}
}

// n3StopLossAdvice 一条止损级建议（Source=discipline：自动执行的合法来源）。
func n3StopLossAdvice() trading.PositionAdvice {
	return trading.PositionAdvice{Code: "600000", TsCode: "600000.SH", Name: "补卖测试",
		Action: "止损", Level: "高", RefPrice: 11, Source: "discipline", Reason: "§N-3 补卖用例"}
}
