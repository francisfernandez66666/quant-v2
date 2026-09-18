package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// probeServer 造一个按 "Authorization: Bearer <key>" 决定响应的假供应商。
// reply(key) 返回 (状态码, 响应体)。
func probeServer(t *testing.T, reply func(key, path string, body map[string]any) (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		status, payload := reply(key, r.URL.Path, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// okBody 供应商成功响应（内容不参与判定，只看状态码）。
const okBody = `{"choices":[{"message":{"role":"assistant","content":"1"}}]}`

// TestProbeClassifiesUpstreamFailure 逐状态码钉住结论分类 —— 分类是"能否拦住坏配置"的唯一依据，
// 判错一个就会让坏配置被当成好配置采用（或让好配置被误拦）。
func TestProbeClassifiesUpstreamFailure(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		want        ProbeKind
		wantUsable  bool
		wantInvalid bool
	}{
		{"200 正常", 200, okBody, ProbeOK, true, false},
		{"401 密钥无效", 401, `{"error":{"message":"Invalid token"}}`, ProbeAuth, false, true},
		{"403 无权限", 403, `{"error":{"message":"Forbidden"}}`, ProbeAuth, false, true},
		{"403 欠费（按原文区分）", 403, `{"error":{"message":"Insufficient balance"}}`, ProbeQuota, false, true},
		{"402 余额不足", 402, `{"error":{"message":"Payment required"}}`, ProbeQuota, false, true},
		{"404 模型不存在", 404, `{"error":{"message":"model not found"}}`, ProbeModel, false, true},
		{"400 模型名错（明确提到 model）", 400, `{"error":{"message":"The model does not exist"}}`, ProbeModel, false, true},
		{"429 限流：密钥有效", 429, `{"error":{"message":"rate limit"}}`, ProbeRateLimited, true, false},
		{"500 供应商故障", 500, `{"error":{"message":"internal error"}}`, ProbeServer, false, false},
		{"400 其他（不阻断，请求体可能是我们的问题）", 400, `{"error":{"message":"unsupported field"}}`, ProbeBadRequest, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := probeServer(t, func(string, string, map[string]any) (int, string) {
				return tc.status, tc.body
			})
			probes := ProbeConfig(Config{APIKey: "sk-x", APIURL: srv.URL + "/v1", Model: "m"}, 5*time.Second)
			if len(probes) != 1 {
				t.Fatalf("应返回 1 条结论, got %d", len(probes))
			}
			p := probes[0]
			if p.Kind != tc.want {
				t.Fatalf("分类不符: got %s (%s) want %s", p.Kind, p.Detail, tc.want)
			}
			if p.Usable() != tc.wantUsable {
				t.Fatalf("Usable() got %v want %v", p.Usable(), tc.wantUsable)
			}
			if p.ConfigInvalid() != tc.wantInvalid {
				t.Fatalf("ConfigInvalid() got %v want %v", p.ConfigInvalid(), tc.wantInvalid)
			}
			if p.Status != tc.status {
				t.Fatalf("状态码 got %d want %d", p.Status, tc.status)
			}
		})
	}
}

// TestProbeEndpointIsNormalized 探测必须打到与正式客户端**同一个地址**。
// 若探测走 base URL 而客户端走完整 endpoint（或反之），"探测通过"就成了假阳性。
func TestProbeEndpointIsNormalized(t *testing.T) {
	var gotPath string
	srv := probeServer(t, func(_, path string, _ map[string]any) (int, string) {
		gotPath = path
		return 200, okBody
	})
	ProbeConfig(Config{APIKey: "sk-x", APIURL: srv.URL + "/v1", Model: "m"}, 5*time.Second)
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("探测路径应为 /v1/chat/completions, got %q", gotPath)
	}
}

// TestProbePerKeyIndexAndUsableSubset 多 key：逐把探测、位次可回告、可用子集可挑出。
// 场景即"某把 key 余额耗尽"——它不该把整组配置拖死，也不该被静默纳入轮询池。
func TestProbePerKeyIndexAndUsableSubset(t *testing.T) {
	srv := probeServer(t, func(key, _ string, _ map[string]any) (int, string) {
		switch key {
		case "sk-bad":
			return 401, `{"error":{"message":"Invalid token"}}`
		case "sk-slow":
			return 429, `{"error":{"message":"rate limit"}}`
		default:
			return 200, okBody
		}
	})
	keys := []string{"sk-bad", "sk-good", "sk-slow"}
	probes := ProbeConfig(Config{APIKeys: keys, APIURL: srv.URL + "/v1", Model: "m"}, 5*time.Second)
	if len(probes) != 3 {
		t.Fatalf("应逐把返回 3 条结论, got %d", len(probes))
	}
	for i, p := range probes {
		if p.Index != i {
			t.Fatalf("第 %d 条结论的 Index 应为 %d, got %d（位次错位会让用户改错那一把）", i, i, p.Index)
		}
	}
	if probes[0].Kind != ProbeAuth || probes[1].Kind != ProbeOK || probes[2].Kind != ProbeRateLimited {
		t.Fatalf("分类不符: %s / %s / %s", probes[0].Kind, probes[1].Kind, probes[2].Kind)
	}
	usable := UsableKeys(keys, probes)
	if len(usable) != 2 || usable[0] != "sk-good" || usable[1] != "sk-slow" {
		t.Fatalf("可用子集应为 [sk-good sk-slow], got %v", usable)
	}
	if AllConfigInvalid(probes) {
		t.Fatal("有可用 key 时不得判定为「配置错」")
	}
}

// TestProbeNetworkFailureIsNotConfigError 网络不可达不得被当成"配置错"：
// 供应商故障/本机断网时把结论说成"你的 key 错"会把用户引到完全错误的排查方向。
func TestProbeNetworkFailureIsNotConfigError(t *testing.T) {
	// 指向一个必定拒连的端口：拿网络层错误。
	probes := ProbeConfig(Config{APIKey: "sk-secret-should-not-leak",
		APIURL: "http://127.0.0.1:1/v1", Model: "m"}, 2*time.Second)
	if len(probes) != 1 || probes[0].Kind != ProbeNetwork {
		t.Fatalf("应为 network 结论, got %+v", probes)
	}
	if probes[0].ConfigInvalid() {
		t.Fatal("网络层失败不得判为配置错（会阻断热更新，把用户逼进死路）")
	}
	if probes[0].Usable() {
		t.Fatal("网络失败不得算可用")
	}
	if AllConfigInvalid(probes) {
		t.Fatal("全部网络失败时 AllConfigInvalid 必须为 false（拒绝热更新需要确凿证据）")
	}
	// 密钥不得出现在可回显的原因里（凭证永远只在 Authorization 头，不进 URL/日志/响应）。
	if strings.Contains(probes[0].Detail, "sk-secret-should-not-leak") {
		t.Fatalf("探测原因泄漏了密钥: %s", probes[0].Detail)
	}
}

// TestProbeRetriesWhenProviderRejectsSmallMaxTokens 上游嫌 max_tokens 太小时放宽重试一次。
// 不做这层重试，一个只认大 max_tokens 的网关会把好配置判成"请求被拒"，白白拦住用户换 key。
func TestProbeRetriesWhenProviderRejectsSmallMaxTokens(t *testing.T) {
	var calls int
	srv := probeServer(t, func(_ string, _ string, body map[string]any) (int, string) {
		calls++
		if mt, _ := body["max_tokens"].(float64); mt < float64(probeRetryMaxTokens) {
			return 400, `{"error":{"message":"max_tokens is too small"}}`
		}
		return 200, okBody
	})
	probes := ProbeConfig(Config{APIKey: "sk-x", APIURL: srv.URL + "/v1", Model: "m"}, 5*time.Second)
	if probes[0].Kind != ProbeOK {
		t.Fatalf("放宽 max_tokens 后应判为可用, got %s (%s)", probes[0].Kind, probes[0].Detail)
	}
	if calls < 2 {
		t.Fatalf("应发生二次探测, 实际请求 %d 次", calls)
	}
}

// TestProbeTimeoutBounds 探测超时必须有上下界：账号配 240s 时不能跟着等 4 分钟，
// 配 5s 时不能把"模型首字慢"误判成超时。
func TestProbeTimeoutBounds(t *testing.T) {
	cases := []struct {
		configured int
		want       time.Duration
	}{
		{0, DefaultProbeTimeout},
		{5, ProbeMinTimeout},
		{30, 30 * time.Second},
		{240, ProbeMaxTimeout},
	}
	for _, tc := range cases {
		if got := ProbeTimeoutFor(tc.configured); got != tc.want {
			t.Fatalf("ProbeTimeoutFor(%d) got %v want %v", tc.configured, got, tc.want)
		}
	}
}

// TestProbeWithoutKeys 无密钥时不发请求、给出明确结论、不阻断（由上层决定"不重建"）。
func TestProbeWithoutKeys(t *testing.T) {
	probes := ProbeConfig(Config{APIURL: "http://127.0.0.1:1/v1"}, time.Second)
	if len(probes) != 1 || probes[0].Kind != ProbeNoKey {
		t.Fatalf("应返回 no_key 结论, got %+v", probes)
	}
	if AllConfigInvalid(probes) {
		t.Fatal("no_key 不算「配置错」：它是「没得可测」，由上层按「不重建」处理")
	}
}

// TestProbeSummaryKeepsInputOrder 汇总文案必须保持入参顺序：用户按"第几把"去改输入框，
// 顺序一乱就会改错那一把。
func TestProbeSummaryKeepsInputOrder(t *testing.T) {
	srv := probeServer(t, func(key, _ string, _ map[string]any) (int, string) {
		if key == "sk-3rd" {
			return 401, `{"error":{"message":"bad"}}`
		}
		return 200, okBody
	})
	keys := []string{"sk-1st", "sk-2nd", "sk-3rd"}
	probes := ProbeConfig(Config{APIKeys: keys, APIURL: srv.URL + "/v1", Model: "m"}, 5*time.Second)
	sum := ProbeSummary(probes)
	i1 := strings.Index(sum, "key#1")
	i2 := strings.Index(sum, "key#2")
	i3 := strings.Index(sum, "key#3")
	if !(i1 < i2 && i2 < i3) {
		t.Fatalf("汇总顺序不符: %s", sum)
	}
	if !strings.Contains(sum, "密钥无效") {
		t.Fatalf("失败那把的原因缺失: %s", sum)
	}
	if got := fmt.Sprintf("%d", probes[2].Index); got != "2" {
		t.Fatalf("第三把的 Index 应为 2, got %s", got)
	}
}
