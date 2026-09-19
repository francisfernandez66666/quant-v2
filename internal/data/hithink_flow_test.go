// hithink_flow_test.go — §ENH-2 资金流第二源单测：官方快照装配口径（元/主力=超大+大）、
// null 缺数拒绝拼凑、东财失败→同花顺降级链与咨询链补流（HasFlow 语义）。
package data

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// flowLeg 生成一档资金 JSON 对象；nil 输出 null（上游缺数口径）。
func flowLeg(in, out, net *float64) string {
	f := func(v *float64) string {
		if v == nil {
			return "null"
		}
		return fmt.Sprintf("%g", *v)
	}
	return `{"inflow_amount":` + f(in) + `,"outflow_amount":` + f(out) + `,"net_amount":` + f(net) + `}`
}

// flowJSON 生成 capital-flow/snapshot 完整信封响应体。
func flowJSON(ts int64, slIn, slOut, slNet, lgIn, lgOut, lgNet, mdIn, mdOut, mdNet, smIn, smOut, smNet *float64) string {
	return `{"code":0,"message":"success","data":{"timestamp":` + fmt.Sprintf("%d", ts) +
		`,"super_large":` + flowLeg(slIn, slOut, slNet) +
		`,"large":` + flowLeg(lgIn, lgOut, lgNet) +
		`,"medium":` + flowLeg(mdIn, mdOut, mdNet) +
		`,"small":` + flowLeg(smIn, smOut, smNet) + `}}`
}

// pf 取浮点值地址，供夹具拼装可选指针字段。
func pf(v float64) *float64 { return &v }

// 校验 hithink 资金流请求的参数组装与响应解析。
func TestHithinkStockMoneyFlowAssembly(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(flowJSON(1756742400000,
			pf(123456789.12), pf(100000000.00), pf(23456789.12),
			pf(86543210.50), pf(81234567.89), pf(5308642.61),
			pf(45678901.23), pf(47890123.45), pf(-2211222.22),
			pf(23456789.01), pf(26789012.34), pf(-3332223.33))))
	}))
	defer srv.Close()
	old := HithinkBaseURL
	HithinkBaseURL = srv.URL
	defer func() { HithinkBaseURL = old }()
	t.Setenv(HithinkAPIKeyEnv, "test-key")

	c, err := NewHithinkClient()
	if err != nil {
		t.Fatal(err)
	}
	cf, err := c.StockMoneyFlow("600519")
	if err != nil {
		t.Fatalf("StockMoneyFlow: %v", err)
	}
	if !strings.Contains(gotQuery, "thscode=600519.SH") {
		t.Errorf("thscode 应补交易所后缀，got %q", gotQuery)
	}
	// 单位=元 直读不换算；主力=超大净额+大净额（同东财 f62 口径）。
	if want := 23456789.12 + 5308642.61; cf.NetInflow-want > 1 || cf.NetInflow-want < -1 {
		t.Errorf("NetInflow=%.2f, want %.2f（元）", cf.NetInflow, want)
	}
	if cf.SuperLargeIn != 123456789.12 || cf.LargeOut != 81234567.89 {
		t.Errorf("明细装配异常: %+v", cf)
	}
	if !cf.Time.Equal(time.UnixMilli(1756742400000)) {
		t.Errorf("Time 应取上游 timestamp，got %v", cf.Time)
	}
}

// 校验 hithink 返回净额为 null 时安全降级、不虚构数据。
func TestHithinkStockMoneyFlowNullNet(t *testing.T) {
	// 超大单净额 null（缺数）：主力净流入不可用"大单净额+0"拼凑，必须报错交调用方降级。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(flowJSON(0, nil, nil, nil, pf(100), pf(0), pf(100), nil, nil, nil, nil, nil, nil)))
	}))
	defer srv.Close()
	old := HithinkBaseURL
	HithinkBaseURL = srv.URL
	defer func() { HithinkBaseURL = old }()
	t.Setenv(HithinkAPIKeyEnv, "test-key")
	c, _ := NewHithinkClient()
	if _, err := c.StockMoneyFlow("600519"); err == nil {
		t.Fatal("主力两档之一缺数时应返回错误，实际成功")
	}
}

type failEMTransport struct{}

// RoundTrip 对东财 host 返回坏 JSON（模拟主源故障），其余 host 404。
func (failEMTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Hostname(), "eastmoney.com") {
		return testResp(200, `{"not":"moneyflow"}`), nil
	}
	return testResp(404, ""), nil
}

// 校验主源失败时 GetStockMoneyFlow 回落 hithink 第二源。
func TestGetStockMoneyFlowFallbackToHithink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(flowJSON(1,
			pf(1000), pf(0), pf(1000), pf(500), pf(0), pf(500),
			nil, nil, nil, nil, nil, nil)))
	}))
	defer srv.Close()
	old := HithinkBaseURL
	HithinkBaseURL = srv.URL
	defer func() { HithinkBaseURL = old }()
	t.Setenv(HithinkAPIKeyEnv, "test-key")

	m := NewMarketAPI()
	m.SetTransport(failEMTransport{})
	cf, err := m.GetStockMoneyFlow("000001")
	if err != nil {
		t.Fatalf("东财故障时应降级同花顺成功: %v", err)
	}
	if cf.NetInflow != 1500 {
		t.Errorf("NetInflow=%.0f want 1500", cf.NetInflow)
	}
	// 第二源也无 key 时错误合并透传，不得伪造成功。
	t.Setenv(HithinkAPIKeyEnv, "")
	if _, err := m.GetStockMoneyFlow("000001"); err == nil || !strings.Contains(err.Error(), "ths-hithink") {
		t.Errorf("双源皆缺应透传合并错误，got %v", err)
	}
}

// 校验 Sina 源缺资金流时由 hithink 回填（§ENH-3）。
func TestEnrichFlowFromHithinkOnSina(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(flowJSON(1,
			pf(2000), pf(0), pf(2000), pf(0), pf(300), pf(-300),
			nil, nil, nil, nil, nil, nil)))
	}))
	defer srv.Close()
	old := HithinkBaseURL
	HithinkBaseURL = srv.URL
	defer func() { HithinkBaseURL = old }()
	t.Setenv(HithinkAPIKeyEnv, "test-key")

	m := NewMarketAPI()
	si := &StockInfo{Code: "600580", Price: 10}
	m.enrichFlowFromHithink(si)
	if !si.HasFlow || si.NetInflow != 1700 {
		t.Fatalf("补流失败: HasFlow=%v NetInflow=%.0f", si.HasFlow, si.NetInflow)
	}
	// 缺 key 场景：静默保持 HasFlow=false（§FIX-9e 缺数语义不得破坏）。
	t.Setenv(HithinkAPIKeyEnv, "")
	si2 := &StockInfo{Code: "600580", Price: 10}
	m.enrichFlowFromHithink(si2)
	if si2.HasFlow {
		t.Fatal("无 key 时不得伪造 HasFlow=true")
	}
}
