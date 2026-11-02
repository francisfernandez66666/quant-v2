// leg_contract_test.go — §LEG-CONTRACT-GATE（2026-11-02 批 4）决策端对"契约不符"拒单的分类与边界。
//
// 这批钉住的是**处置方向**，不是拒单本身：
//
//	① 网关回的 503 + code=contract_mismatch 必须被认成"确定性拒绝"——
//	   它状态码是 5xx，走通用分支会被当瞬态一路重试；版本旧重试一百次也不会变新，
//	   空转之外还会让日志把它读成"网络抖"，把处置方向从**升版**指到**查链路**；
//	② /health 的三个新读数是**指针三态**：老网关不发 `contract_ok` 时必须读成 nil（没读到），
//	   绝不能用零值冒充"契约没问题"（§0925EVE-W3-E 与 §M2 的同一口径）；
//	③ 本批不触碰熔断与健康线：拒单只发生在下单调用上，观测位与 ok/live_ready 各归各的。
package trading

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayContractMismatchClassifies(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"503+code", &gatewayHTTPError{Status: http.StatusServiceUnavailable,
			Body: `{"ok":false,"code":"contract_mismatch","contract_fp":"aaaa"}`}, true},
		{"503 无 code（broker not connected 那种）", &gatewayHTTPError{Status: http.StatusServiceUnavailable,
			Body: `{"ok":false,"err":"broker not connected"}`}, false},
		{"409 终态不算契约不符", &gatewayHTTPError{Status: http.StatusConflict,
			Body: `{"ok":false,"code":"terminal"}`}, false},
		{"网络错误不算", errors.New("dial tcp: connection refused"), false},
		{"坏 JSON 体不算", &gatewayHTTPError{Status: http.StatusServiceUnavailable, Body: `{`}, false},
	}
	for _, tc := range cases {
		if got := GatewayContractMismatch(tc.err); got != tc.want {
			t.Errorf("%s: GatewayContractMismatch=%v，期望 %v", tc.name, got, tc.want)
		}
	}
}

func TestContractMismatchFailsFastWithoutRetry(t *testing.T) {
	// 一次请求都不许重发：retries=2 是既有默认形态，契约不符时必须直败
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/order" {
			atomic.AddInt32(&hits, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"code":"contract_mismatch","err":"契约指纹不符"}`))
	}))
	defer srv.Close()

	c := NewQMTClient(srv.URL, "tk", 2*time.Second, 2)
	_, err := c.PlaceBuy(OrderRequest{SignalID: "sig-1", Code: "600588.SH", Side: "buy", Price: 8.9, Qty: 100})
	if err == nil {
		t.Fatal("契约不符时下单必须失败")
	}
	if !GatewayContractMismatch(err) {
		t.Fatalf("错误没带上契约分类，调用方会把它读成普通 5xx：%v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("契约不符被重试了 %d 次（应为 1：确定性拒绝，重试只是空转 + 把方向指错）", got)
	}
}

func TestHealthContractFieldsAreThreeState(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantNil bool
		wantOK  bool
		wantFP  string
		wantVer string
	}{
		{"老网关不发这些键", `{"ok":true,"broker_connected":true}`, true, false, "", ""},
		{"契约不符", `{"ok":true,"broker_connected":true,"contract_fp":"71c65","contract_ok":false,"contract_checked":true,"gateway_version":"71774a3"}`,
			false, false, "71c65", "71774a3"},
		{"契约相符", `{"ok":true,"broker_connected":true,"contract_fp":"71c65","contract_ok":true,"gateway_version":"71774a3"}`,
			false, true, "71c65", "71774a3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := NewQMTClient(srv.URL, "tk", 2*time.Second, 0)
			st, err := c.BrokerStatus()
			if err != nil {
				t.Fatalf("BrokerStatus 失败: %v", err)
			}
			if tc.wantNil {
				if st.ContractOK != nil {
					t.Fatalf("老网关没发 contract_ok，解析结果却是 %v（零值冒充已知）", *st.ContractOK)
				}
			} else if st.ContractOK == nil || *st.ContractOK != tc.wantOK {
				t.Fatalf("contract_ok 解析错：%v（期望 %v）", st.ContractOK, tc.wantOK)
			}
			if st.ContractFP != tc.wantFP || st.GatewayVersion != tc.wantVer {
				t.Errorf("透传字段不符：fp=%q ver=%q", st.ContractFP, st.GatewayVersion)
			}
		})
	}
}

func TestContractFieldsJSONTagsStayStable(t *testing.T) {
	// 字段名是网关↔决策端的契约的一部分：改名会让对侧静默读成"没这个键"（指针三态里就是 nil）
	raw := `{"contract_fp":"aa","contract_ok":false,"gateway_version":"v1"}`
	var st GatewayBrokerStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	if st.ContractFP != "aa" || st.GatewayVersion != "v1" {
		t.Fatalf("契约读数没解析进来：%+v", st)
	}
	if st.ContractOK == nil || *st.ContractOK {
		t.Fatalf("contract_ok=false 必须解析成显式 false，不能是 nil 也不能是 true")
	}
}
