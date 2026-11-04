// external_brain_gate_test.go — §EXT-BRAIN-1104 外脑模式的执行腿锁（2026-11-04）。
//
// 这批测"桥断了量仔还该不该开新仓"这条本体判据，形状照 §163 的教训：**必须有行为腿**
// （真跑一遍 autoPlace / 卖出腿，数下单次数），静态 grep 抓不到"接线断了"。
//
// 11-05 券商关掉 QMT 内置策略的文件读写之后的读数形态是：网关进程还活着、/health 仍 200、
// broker_connected 仍可能为真——**只有 queued_connected 会掉假**。所以本批最要紧的一条是
// 这个字段真的从网关流到档位、真的挡在买入执行入口上，并且**只挡买入**（卖出腿走既有
// 熔断链，再叠一层只会挡住本来该出的货）。
//
// 反证对应（摘了必红）：
//
//	A 摘掉 autoPlaceScaled 里的外脑闸 ⇒ 「桥断腿」下出单 ⇒ 本用例红；
//	B 把 QMTClient 的三态缓存退化成 bool 零值 ⇒ 「旧网关缺键腿」判成断链 ⇒ 本用例红；
//	C 把闸写成"买入卖出一起挡" ⇒ 「止损仍出票腿」红。
//
// English: behaviour locks — a downed bridge blocks new live buys only; exits, legacy gateways
// and a healthy bridge all keep their current outcome.
package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// ebMu 守护替换后的 handler 记账（原 harness 的 mu 不在射程内，换 handler 等于换写者）。
var ebMu sync.Mutex

// ebSwapHealth 把 harness 假网关的 /health 换成给定响应体，/order 维持"记账下单"原行为。
// 为什么换 handler 而不是另起一个网关：URL/控制器/账本三者必须与对照腿完全同源，
// 否则"这轮没下单"可能只是因为夹具不一样。
func ebSwapHealth(t *testing.T, srv *httptest.Server, orders *[]map[string]interface{}, healthBody string) {
	t.Helper()
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Write([]byte(healthBody))
		case "/order":
			var req map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"ok":false,"err":"bad body"}`, 400)
				return
			}
			ebMu.Lock()
			*orders = append(*orders, req)
			ebMu.Unlock()
			w.Write([]byte(`{"ok":true,"order_id":"GW1"}`))
		default:
			w.Write([]byte(`{"ok":false,"err":"not found"}`))
		}
	})
}

// ebSignal/ebLive 与 TestAutoPlacePlacesOrder 同形的一笔买入（600000 浦发，现价 10）。
func ebSignal() combat_agent.Signal {
	return combat_agent.Signal{ID: "SIG-EB", Code: "600000", Name: "浦发", Strategy: "龙头", Direction: "做多", Price: 10}
}

func ebLive() map[string]*data.StockInfo {
	return map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10}}
}

// TestExternalBrainBridgeDownPlacesNoBuy 档 3（桥心跳断）⇒ 实盘买入腿一笔都不发。
// 反证 A：摘掉 autoPlaceScaled 里的外脑闸，这里立刻下出 1 笔。
func TestExternalBrainBridgeDownPlacesNoBuy(t *testing.T) {
	e, _, srv, orders := newQMTEngine(t, nil)
	ebSwapHealth(t, srv, orders, `{"ok":true,"broker_connected":true,"queued_connected":false,"ts":"now"}`)
	ctrl := e.qmtCtrl
	if ctrl == nil {
		t.Fatalf("harness 未接入实盘控制器")
	}
	// 采集腿：这一次探测必须把 queued_connected=false 落进控制器（唯一写入点在 HealthCheck）。
	ctrl.HealthCheck()
	if code, reason := ctrl.ExternalBrain(); code != trading.ExternalBrainChannelDown {
		t.Fatalf("桥心跳断应判档 3，实得 %d（%s）", code, reason)
	}
	e.autoPlace(ebSignal(), ebLive())
	if len(*orders) != 0 {
		t.Fatalf("外脑模式档 3 仍下出 %d 笔实盘买单：%+v", len(*orders), *orders)
	}
}

// TestExternalBrainBridgeUpStillPlacesBuy 对照腿（防"恒挡买入"的假安全）：
// 同一夹具，桥心跳为真 ⇒ 照旧出一笔。
func TestExternalBrainBridgeUpStillPlacesBuy(t *testing.T) {
	e, _, srv, orders := newQMTEngine(t, nil)
	ebSwapHealth(t, srv, orders, `{"ok":true,"broker_connected":true,"queued_connected":true,"ts":"now"}`)
	e.qmtCtrl.HealthCheck()
	if code, reason := e.qmtCtrl.ExternalBrain(); code != trading.ExternalBrainNormal {
		t.Fatalf("桥心跳正常应判档 1，实得 %d（%s）", code, reason)
	}
	e.autoPlace(ebSignal(), ebLive())
	if len(*orders) != 1 {
		t.Fatalf("档 1 应照旧放行一笔买入，实得 %d 笔：%+v", len(*orders), *orders)
	}
}

// TestExternalBrainLegacyGatewayUnknownIsNormal 旧版网关 /health 不带 queued_connected 键
// ⇒ 读数未知 ⇒ 不许判成"桥已断"（未知既不装健康、也不装断链）。
// 反证 B：把指针三态改成 bool 零值 ⇒ 缺键被读成 false ⇒ 这条必红。
func TestExternalBrainLegacyGatewayUnknownIsNormal(t *testing.T) {
	e, _, srv, orders := newQMTEngine(t, nil)
	ebSwapHealth(t, srv, orders, `{"ok":true,"broker_connected":true,"ts":"now"}`)
	e.qmtCtrl.HealthCheck()
	if code, reason := e.qmtCtrl.ExternalBrain(); code != trading.ExternalBrainNormal {
		t.Fatalf("缺键＝读数未知，应判档 1，实得 %d（%s）", code, reason)
	}
	e.autoPlace(ebSignal(), ebLive())
	if len(*orders) != 1 {
		t.Fatalf("读数未知不该饿死买入腿，实得 %d 笔", len(*orders))
	}
}

// TestExternalBrainBridgeDownStillSells 档 3 时**卖出腿照旧执行**：
// 止损/止盈走既有熔断链，外脑闸只掐开新仓这一侧。反证 C：把闸挪到卖出腿上，这里红。
func TestExternalBrainBridgeDownStillSells(t *testing.T) {
	e, db, srv, orders := newQMTEngine(t, func(c *config.QMTConfig) { c.AutoSell = true })
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	ebSwapHealth(t, srv, orders, `{"ok":true,"broker_connected":true,"queued_connected":false,"ts":"now"}`)
	ctrl := e.QMTController()
	ctrl.HealthCheck()
	if code, _ := ctrl.ExternalBrain(); code != trading.ExternalBrainChannelDown {
		t.Fatalf("前置条件不成立：应为档 3，实得 %d", code)
	}
	advices := []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "止损", Level: "高", RefPrice: 9.0, Reason: "破位止损"},
	}
	e.autoExecuteRealSells(e.UserID(), ctrl, db, advices)
	if len(*orders) != 1 {
		t.Fatalf("外脑模式不得挡卖出腿，应全仓卖出 1 单，实得 %d 笔：%+v", len(*orders), *orders)
	}
	if (*orders)[0]["side"] != "卖出" {
		t.Fatalf("应是卖单：%+v", (*orders)[0])
	}
}
