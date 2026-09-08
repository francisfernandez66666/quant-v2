// qmt_broker_test.go — §QMT-DUAL 网关 active 通道切换/状态单测。
// English: gateway active-broker status/switch tests (dual-path miniQMT/QMT).
package trading

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quant-trading-v2/internal/config"
)

// brokerStub 假网关：/health 回双路径状态，/admin/broker 回显目标通道。
// English: fake gateway serving /health dual-path status and /admin/broker echo.
type brokerStub struct {
	t          *testing.T
	healthBody string
	switched   string
}

func (s *brokerStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/health":
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(s.healthBody))
	case r.URL.Path == "/admin/broker" && r.Method == http.MethodPost:
		var req struct {
			Broker string `json:"broker"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", 400)
			return
		}
		s.switched = req.Broker
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"broker":"` + req.Broker + `","err":""}`))
	default:
		http.NotFound(w, r)
	}
}

func TestQMTClientBrokerStatus(t *testing.T) {
	stub := &brokerStub{
		t: t,
		healthBody: `{"ok":true,"ts":"t","broker":"queued","broker_connected":true,
		              "xt_connected":false,"queued_connected":true}`,
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	c := NewQMTClient(srv.URL, "tk", 2e9, 0)
	st, err := c.BrokerStatus()
	if err != nil {
		t.Fatalf("BrokerStatus: %v", err)
	}
	if st.Broker != "queued" || !st.QueuedConnected || st.XTConnected {
		t.Fatalf("BrokerStatus 解析错误: %+v", st)
	}
	if !st.BrokerConnected {
		t.Fatalf("broker_connected 应为 true: %+v", st)
	}
}

func TestQMTClientBrokerStatusLegacyField(t *testing.T) {
	// 兼容老网关：无 broker_mode 别名，broker 字段缺省 → 回退 broker_mode。
	stub := &brokerStub{t: t, healthBody: `{"ok":true,"broker_mode":"xt","broker_connected":false}`}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	c := NewQMTClient(srv.URL, "tk", 2e9, 0)
	st, err := c.BrokerStatus()
	if err != nil {
		t.Fatalf("BrokerStatus: %v", err)
	}
	if st.Broker != "xt" || st.BrokerConnected {
		t.Fatalf("Legacy 字段回退错误: %+v", st)
	}
}

func TestQMTClientSwitchBroker(t *testing.T) {
	stub := &brokerStub{t: t, healthBody: `{"ok":true,"broker":"xt"}`}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	c := NewQMTClient(srv.URL, "tk", 2e9, 0)
	if err := c.SwitchBroker("queued"); err != nil {
		t.Fatalf("SwitchBroker: %v", err)
	}
	if stub.switched != "queued" {
		t.Fatalf("网关应收到 broker=queued, got %q", stub.switched)
	}
}

func TestControllerGatewayBrokerStatusUsesConfig(t *testing.T) {
	stub := &brokerStub{t: t, healthBody: `{"ok":true,"broker":"queued","queued_connected":true}`}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	cfg := config.QMTConfig{Enabled: true, GatewayURL: srv.URL, Token: "tk"}
	c := NewController(NoopExecutor{}, nil, "uA", cfg, nil)
	st, err := c.GatewayBrokerStatus()
	if err != nil {
		t.Fatalf("GatewayBrokerStatus: %v", err)
	}
	if st.Broker != "queued" || !st.QueuedConnected {
		t.Fatalf("状态读取错误: %+v", st)
	}
}

func TestControllerGatewayBrokerStatusDisabled(t *testing.T) {
	c := NewController(NoopExecutor{}, nil, "uA", config.QMTConfig{Enabled: false}, nil)
	if _, err := c.GatewayBrokerStatus(); err == nil {
		t.Fatal("未启用实盘应返回错误")
	}
}

func TestControllerSwitchGatewayBrokerValidates(t *testing.T) {
	stub := &brokerStub{t: t, healthBody: `{}`}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	cfg := config.QMTConfig{Enabled: true, GatewayURL: srv.URL, Token: "tk"}
	c := NewController(NoopExecutor{}, nil, "uA", cfg, nil)
	if err := c.SwitchGatewayBroker("bogus"); err == nil {
		t.Fatal("非法通道应报错")
	}
	if err := c.SwitchGatewayBroker("xt"); err != nil {
		t.Fatalf("SwitchGatewayBroker(xt): %v", err)
	}
	if stub.switched != "xt" {
		t.Fatalf("网关应收到 broker=xt, got %q", stub.switched)
	}
}
