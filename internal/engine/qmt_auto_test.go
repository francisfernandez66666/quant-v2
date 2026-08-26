// AUTO_TRADING_PLAN M1：autoPlace 自动下单测试。
// auto 模式做多信号直连网关下单（金额按 fixed_amount 折算整手、code 补后缀、白名单/熔断/手动模式跳过）。
// English: M1 autoPlace auto-order tests — in auto mode a long signal is placed straight to the gateway
// (qty from fixed_amount as whole lots, code exchange suffix, strategy whitelist / tripped / manual-skip).
package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// newQMTEngine 构建带 QMT 控制器的引擎（httptest 网关记录 /order 请求）。
func newQMTEngine(t *testing.T, mutate func(*config.QMTConfig)) (*Engine, *store.DB, *httptest.Server, *[]map[string]interface{}) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "qmt.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	var mu sync.Mutex
	orders := []map[string]interface{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"ok":true,"ts":"now"}`))
			return
		}
		if r.URL.Path == "/order" {
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"ok":false,"err":"bad body"}`, 400)
				return
			}
			mu.Lock()
			orders = append(orders, req)
			mu.Unlock()
			w.Write([]byte(`{"ok":true,"order_id":"GW1"}`))
			return
		}
		w.Write([]byte(`{"ok":false,"err":"not found"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.Mode = "auto"
	cfg.GatewayURL = srv.URL
	cfg.FixedAmount = 10000
	cfg.MissHeartbeatSec = 5
	if mutate != nil {
		mutate(&cfg)
	}
	exec := trading.NewQMTClient(srv.URL, "", 2*time.Second, 0)
	ctrl := trading.NewController(exec, db, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, db)
	return e, db, srv, &orders
}

// TestAutoPlacePlacesOrder 验证 auto 模式做多信号直下网关：金额折算整手、code 补后缀、信号 ID 幂等键。
func TestAutoPlacePlacesOrder(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	sig := combat_agent.Signal{ID: "SIG1", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlace(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}})
	if len(*orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(*orders))
	}
	o := (*orders)[0]
	// §GAP2-W1 确定性幂等键：buy:<纯代码>:<战法>:<交易日>——不再使用每轮重生成的 sig.ID，
	// 同股同战法当日重复触发/重启重放全部被 orders 表唯一键拦截。
	wantID := fmt.Sprintf("buy:600000:龙头:%s", data.TradingDayDate(time.Now()))
	if o["signal_id"] != wantID {
		t.Fatalf("signal_id: got %v want %v", o["signal_id"], wantID)
	}
	if o["code"] != "600000.SH" {
		t.Fatalf("code should get .SH suffix, got %v", o["code"])
	}
	if o["side"] != "买入" {
		t.Fatalf("side: %v", o["side"])
	}
	// fixed_amount=10000 / price=10 → 1000 股（整手）
	if o["qty"].(float64) != 1000 {
		t.Fatalf("qty from fixed_amount: %v", o["qty"])
	}
	if o["price"].(float64) != 10 {
		t.Fatalf("price: %v", o["price"])
	}
}

// TestAutoPlaceSkipsWhenManual 手动模式不下单。
func TestAutoPlaceSkipsWhenManual(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.Mode = "manual" })
	e.autoPlace(combat_agent.Signal{ID: "S1", Code: "000001", Strategy: "龙头", Direction: "做多", Price: 10}, nil)
	if len(*orders) != 0 {
		t.Fatalf("manual mode should not place, got %d", len(*orders))
	}
}

// TestAutoPlaceSkipsWhitelist 战法白名单外不下单。
func TestAutoPlaceSkipsWhitelist(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.Strategies = []string{"N形"} })
	e.autoPlace(combat_agent.Signal{ID: "S1", Code: "000001", Strategy: "龙头", Direction: "做多", Price: 10}, nil)
	if len(*orders) != 0 {
		t.Fatalf("whitelist excludes 龙头, should skip, got %d", len(*orders))
	}
	// 白名单命中 → 下单
	e.autoPlace(combat_agent.Signal{ID: "S2", Code: "000002", Strategy: "N形", Direction: "做多", Price: 10}, nil)
	if len(*orders) != 1 {
		t.Fatalf("whitelist includes N形, should place, got %d", len(*orders))
	}
}

// TestAutoPlaceSkipsTripped 熔断中跳过。
func TestAutoPlaceSkipsTripped(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	// 直接置熔断（网关不可达模拟由 controller 负责；此处验证 autoPlace 熔断前置）
	ctrl := e.QMTController()
	ctrl.SetTripped("test")
	e.autoPlace(combat_agent.Signal{ID: "S1", Code: "000001", Strategy: "龙头", Direction: "做多", Price: 10}, nil)
	if len(*orders) != 0 {
		t.Fatalf("tripped should skip, got %d", len(*orders))
	}
}

// TestAutoPlacePriceFromLive 现价优先于信号触发价。
// §R0.7 后高价股不强凑一手：105 元×100 股=10500 > 旧默认预算 10000 会被正确跳过——
// 测试预算提高到 20000 以继续验证"现价优先"语义（qty = 20000/105 → 190 → 整手 100）。
func TestAutoPlacePriceFromLive(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.FixedAmount = 20000 })
	sig := combat_agent.Signal{ID: "S1", Code: "300750", Name: "宁德", Strategy: "龙头", Direction: "做多", Price: 100}
	e.autoPlace(sig, map[string]*data.StockInfo{"300750": {Code: "300750", Price: 105}})
	if len(*orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(*orders))
	}
	o := (*orders)[0]
	if o["code"] != "300750.SZ" {
		t.Fatalf("code: %v", o["code"])
	}
	if o["price"].(float64) != 105 {
		t.Fatalf("should use live price 105, got %v", o["price"])
	}
	if o["qty"].(float64) != 100 { // 20000/105=190 股 → 整手取整 100
		t.Fatalf("qty floor to one lot: %v", o["qty"])
	}
}

// TestAutoPlaceIdempotent 同一 signal_id 重复触发不重复下单（orders 表唯一键）。
func TestAutoPlaceIdempotent(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	sig := combat_agent.Signal{ID: "S1", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlace(sig, nil)
	e.autoPlace(sig, nil)
	if len(*orders) != 1 {
		t.Fatalf("idempotent: expected 1 gateway call, got %d", len(*orders))
	}
}

// TestAutoPlaceSkipsSealedLimitUp §GAP1.5 回归：涨停封板股 auto 模式拒买（与模拟盘同款分板块守卫）。
// 002412 实录：4 连板封板后龙头识别仍发 buy → 现实中买单无法排队成交。
// English: §GAP1.5 regression — autoPlace must skip sealed limit-up boards (board-aware, same as paper).
func TestAutoPlaceSkipsSealedLimitUp(t *testing.T) {
	t.Run("主板10cm封板拒买", func(t *testing.T) {
		e, _, _, orders := newQMTEngine(t, nil)
		sig := combat_agent.Signal{ID: "S-LU", Code: "000001", Name: "平安", Strategy: "龙头", Direction: "做多", Price: 10}
		// 涨幅 9.95% ≥ 主板阈值 9.9% → 拒买
		e.autoPlace(sig, map[string]*data.StockInfo{"000001": {Code: "000001", Price: 10, ChangePct: 9.95}})
		if len(*orders) != 0 {
			t.Fatalf("主板封板应拒买, got %d orders", len(*orders))
		}
		// 涨幅 9.5%（未封板）→ 放行
		e.autoPlace(sig, map[string]*data.StockInfo{"000001": {Code: "000001", Price: 10, ChangePct: 9.5}})
		if len(*orders) != 1 {
			t.Fatalf("未封板应放行, got %d orders", len(*orders))
		}
	})
	t.Run("创业20cm未封板不误拒", func(t *testing.T) {
		// 预算提到 20000：105 元×一手=10500，避免被 §R0.7 高价股预算守卫先拦（与 TestAutoPlacePriceFromLive 同理）
		e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.FixedAmount = 20000 })
		sig := combat_agent.Signal{ID: "S-CYB", Code: "300750", Name: "宁德", Strategy: "龙头", Direction: "做多", Price: 100}
		// 创业板 19.5% 涨幅 < 19.9 阈值 → 不误拒
		e.autoPlace(sig, map[string]*data.StockInfo{"300750": {Code: "300750", Price: 105, ChangePct: 19.5}})
		if len(*orders) != 1 {
			t.Fatalf("创业板 19.5%% 未达 19.9%% 不应拒买, got %d orders", len(*orders))
		}
	})
}

// TestAutoPlaceSkipsST §GAP1.6 回归：ST 股 auto 下单被 Controller 守卫拦截（信号层漏网时兜底）。
func TestAutoPlaceSkipsST(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, nil)
	sig := combat_agent.Signal{ID: "S-ST", Code: "600000", Name: "*ST测试", Strategy: "龙头", Direction: "做多", Price: 10}
	e.autoPlace(sig, map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}})
	if len(*orders) != 0 {
		t.Fatalf("ST 股应被拒绝下单, got %d orders", len(*orders))
	}
}

// TestAutoPlaceDailyCap §GAP1.4 回归：单日买入笔数达上限后 auto 不再下单。
func TestAutoPlaceDailyCap(t *testing.T) {
	e, _, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.DailyMaxBuys = 2 })
	for i, id := range []string{"C1", "C2", "C3"} {
		sig := combat_agent.Signal{ID: id, Code: "60000" + string(rune('0'+i)), Name: "股" + id, Strategy: "龙头", Direction: "做多", Price: 10}
		e.autoPlace(sig, map[string]*data.StockInfo{sig.Code: {Code: sig.Code, Price: 10}})
	}
	if len(*orders) != 2 {
		t.Fatalf("daily_max_buys=2 应只下 2 单, got %d", len(*orders))
	}
}

// TestAutoExecuteRealSells §GAP1.1 回归：止损级建议自动全仓卖出（幂等键按码+类+日）；
// 止盈/减仓/加仓建议不触发自动卖。
func TestAutoExecuteRealSells(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = true })
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := e.QMTController()
	advices := []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高", RefPrice: 9.0, Reason: "破位止损"},
	}
	e.autoExecuteRealSells(ctrl, db, advices)
	if len(*orders) != 1 {
		t.Fatalf("止损建议应自动卖出 1 单, got %d", len(*orders))
	}
	o := (*orders)[0]
	if o["side"] != "卖出" || o["qty"].(float64) != 500 {
		t.Fatalf("应全仓卖出 500 股: %+v", o)
	}
	// 同日重发 → signal_id 幂等命中，不再下单
	e.autoExecuteRealSells(ctrl, db, advices)
	if len(*orders) != 1 {
		t.Fatalf("同日同类卖单应幂等防重, got %d", len(*orders))
	}
	// 非止损类不触发
	tp := []trading.PositionAdvice{{Code: "600000", TsCode: "600000.SH", Action: "止盈", Level: "高", RefPrice: 12}}
	e.autoExecuteRealSells(ctrl, db, tp)
	if len(*orders) != 1 {
		t.Fatalf("止盛建议不应自动卖出, got %d", len(*orders))
	}
}

// TestAutoSellDisabledSkips §GAP1.1：auto_sell=false 时止损建议仅提醒不下单。
func TestAutoSellDisabledSkips(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = false })
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10},
	}); err != nil {
		t.Fatal(err)
	}
	advices := []trading.PositionAdvice{{Code: "600000", TsCode: "600000.SH", Action: "止损", RefPrice: 9}}
	e.autoExecuteRealSells(e.QMTController(), db, advices)
	if len(*orders) != 0 {
		t.Fatalf("auto_sell=false 不应下单, got %d", len(*orders))
	}
}

// TestM8RealDrawdown §GAP1.2 回归：组合市值自峰值回撤超阈值 → 全部持仓自动卖出（m8 类别幂等）；
// 回撤未达阈值不触发；未启用（m8_enabled=false）不触发。
func TestM8RealDrawdown(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, nil)
	// 两只持仓：成本 5 元 ×1000 股 ×2
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "A", Qty: 1000, CostPrice: 5},
		{TsCode: "000001.SZ", Name: "B", Qty: 1000, CostPrice: 5},
	}); err != nil {
		t.Fatal(err)
	}
	// 装配 cfgMgr：M8 启用、阈值 -20%
	mgr := config.NewManager("")
	mgr.Rules.RiskCtrl.M8Enabled = true
	mgr.Rules.RiskCtrl.M8PortfolioDrawdownPct = -20
	e.SetCfgMgr(mgr)

	positions := mustPositions(t, db)
	quotes := map[string]*data.StockInfo{"600000": {Price: 6}, "000001": {Price: 6}} // 组合 12000

	// 未达峰值前：峰值随市值抬升（12000），无回撤 → 不触发
	e.checkM8RealDrawdown(e.QMTController(), db, positions, quotes)
	if len(*orders) != 0 {
		t.Fatalf("峰值抬升期不应触发, got %d orders", len(*orders))
	}
	// 模拟曾涨到 8 元（组合 16000 峰值），现回落到 6 元（12000，回撤 -25% ≤ -20%）→ 触发清仓
	e.mu.Lock()
	e.m8PeakTotal = 16000
	e.mu.Unlock()
	e.checkM8RealDrawdown(e.QMTController(), db, positions, quotes)
	if len(*orders) != 2 {
		t.Fatalf("回撤 -25%% 应全部卖出 2 单, got %d", len(*orders))
	}
	for _, o := range *orders {
		if o["side"] != "卖出" {
			t.Fatalf("M8 应为卖出单: %+v", o)
		}
	}
	// 同日重发 → 幂等防重不再下单
	e.checkM8RealDrawdown(e.QMTController(), db, positions, quotes)
	if len(*orders) != 2 {
		t.Fatalf("M8 同日应幂等防重, got %d", len(*orders))
	}

	// 未启用 M8：新引擎不触发
	e2, db2, _, orders2 := newQMTEngine(t, nil)
	mgr2 := config.NewManager("")
	mgr2.Rules.RiskCtrl.M8Enabled = false
	e2.SetCfgMgr(mgr2)
	if _, err := db2.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "A", Qty: 1000, CostPrice: 5},
	}); err != nil {
		t.Fatal(err)
	}
	e2.mu.Lock()
	e2.m8PeakTotal = 16000
	e2.mu.Unlock()
	e2.checkM8RealDrawdown(e2.QMTController(), db2, mustPositions(t, db2),
		map[string]*data.StockInfo{"600000": {Price: 3}})
	if len(*orders2) != 0 {
		t.Fatalf("m8_enabled=false 不应触发, got %d", len(*orders2))
	}
}

func mustPositions(t *testing.T, db *store.DB) []store.RealPosition {
	t.Helper()
	ps, err := db.RealPositions()
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// TestM8PeakResetsWhenFlat §GAP2-W1 回归（资损级）：空仓时 M8 峰值基线必须归零。
// 旧实现提前 return 从不重置 m8PeakTotal——峰值 16 万全平后再建仓 1 万，
// 回撤判定 (10000-16000)/16000=-37.5% 直接命中 -20% 阈值，新仓位被立刻连环强平。
func TestM8PeakResetsWhenFlat(t *testing.T) {
	e, db, _, orders := newQMTEngine(t, nil)
	mgr := config.NewManager("")
	mgr.Rules.RiskCtrl.M8Enabled = true
	mgr.Rules.RiskCtrl.M8PortfolioDrawdownPct = -20
	e.SetCfgMgr(mgr)

	// 空仓调用 → 进程内陈旧峰值归零
	e.mu.Lock()
	e.m8PeakTotal = 16000
	e.mu.Unlock()
	e.checkM8RealDrawdown(e.QMTController(), db, nil, nil)
	e.mu.RLock()
	peak := e.m8PeakTotal
	e.mu.RUnlock()
	if peak != 0 {
		t.Fatalf("空仓应把 M8 峰值基线归零, got %v", peak)
	}

	// 归零后重建仓：市值 10000 直接成为新峰值，不存在对陈旧峰值的"回撤" → 不触发清仓
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "A", Qty: 1000, CostPrice: 10},
	}); err != nil {
		t.Fatal(err)
	}
	e.checkM8RealDrawdown(e.QMTController(), db, mustPositions(t, db),
		map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}})
	if len(*orders) != 0 {
		t.Fatalf("重建仓不应被陈旧峰值误判回撤而强平, got %d orders", len(*orders))
	}
}
