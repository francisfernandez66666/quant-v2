package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/llm"
)

// ── LLM 热更新稳定性回归 ──────────────────────────────────────────────────────
//
// 这一组用例钉住的是**盘中换 key 这件事本身不会把系统搞得更糟**，而不是某一行代码的写法：
//
//  1. 探测确认可用的配置 → 切换运行时并落库；
//  2. 探测确认**不可用**的配置（密钥无效/模型不存在/欠费）→ 拒绝，运行时与磁盘**一律不动**；
//     这条最关键：若采用，就同时把"能用的"换成"不能用的"并把坏配置写进磁盘 → 重启也救不回来；
//  3. 探测**无法判定**（网络/供应商故障）→ 采用但如实回告"未经验证"，并留下回滚点；
//  4. 多 key 里坏的那把被剔除，不混进轮询池；
//  5. 翻车后能一键回到上一个**已验证可用**的配置。
//
// English: this file pins the stability contract of the hot update — refusal requires positive
// evidence, an unverifiable config is adopted with an explicit warning, and there is always a
// rollback point. Verification never touches the disk unless we actually switched.

// TestMain 给整个包安装"确定性探测"。
//
// 本包用例专注验证**决策链**（探测结论 → 是否切换/落库/拒绝），而探测本身的分类正确性
// 由 internal/llm/probe_test.go 用 httptest 真实验证（那里有真 HTTP）。
// 不装的话，既有用例里 api_url 指向 example.com 会让每次保存都真的出网：慢、不确定、
// 无网环境下还会假失败。
func TestMain(m *testing.M) {
	orig := llmProber
	llmProber = stubProbe()
	code := m.Run()
	llmProber = orig
	os.Exit(code)
}

// stubProbe 生成"逐把返回指定结论"的确定性探测实现；kinds 不够时余下各把按 ProbeOK 处理。
// 只返回结论，不发任何网络请求。
func stubProbe(kinds ...llm.ProbeKind) func(llm.Config, time.Duration) []llm.KeyProbe {
	return func(cfg llm.Config, _ time.Duration) []llm.KeyProbe {
		keys := cfg.APIKeys
		if len(keys) == 0 && cfg.APIKey != "" {
			keys = []string{cfg.APIKey}
		}
		// 无密钥形态：单独返回 no_key 结论（上层据此 400 且不重建，用例 TestSetLLMConfig* 依赖它）。
		if len(keys) == 0 {
			return []llm.KeyProbe{{Kind: llm.ProbeNoKey, Detail: "stub: 无密钥"}}
		}
		// 逐把装配结论：未指定 kinds 的余下各把按 OK；非 OK 用 401、network 用 0（不可达语义）。
		out := make([]llm.KeyProbe, 0, len(keys))
		for i := range keys {
			kind := llm.ProbeOK
			if i < len(kinds) {
				kind = kinds[i]
			}
			status := 200
			if kind != llm.ProbeOK {
				status = 401
			}
			if kind == llm.ProbeNetwork {
				status = 0
			}
			out = append(out, llm.KeyProbe{
				Index: i, Kind: kind, Status: status, LatencyMS: 1,
				Detail: "stub: " + string(kind),
			})
		}
		return out
	}
}

// stubProbeWith 本次用例改用指定结论，结束自动还原为默认（全部可用）。
func stubProbeWith(t *testing.T, kinds ...llm.ProbeKind) {
	t.Helper()
	llmProber = stubProbe(kinds...)
	t.Cleanup(func() { llmProber = stubProbe() })
}

// withRealProber 本用例改用真实探测实现（配合 httptest 假供应商，仍不出网）。
func withRealProber(t *testing.T) {
	t.Helper()
	llmProber = llm.ProbeConfig
	t.Cleanup(func() { llmProber = stubProbe() })
}

// llmCapture 记录热切换回调收到的内容 —— 断言必须落在"下游消费者收到了什么"，
// 只断言落库值会漏掉"库里对了、线上客户端却被换坏"这一类事故。
type llmCapture struct {
	calls   int
	keys    []string
	url     string
	model   string
	timeout int
	stream  bool
}

// captureRecreate 挂上热切换回调并返回采集器。
func captureRecreate(t *testing.T, s *Server) *llmCapture {
	t.Helper()
	c := &llmCapture{}
	s.SetLLMRecreate(func(keys []string, url, model string, timeoutSec int, streaming bool, batchConcurrency int, classifier string) {
		c.calls++
		c.keys = append([]string(nil), keys...)
		c.url, c.model, c.timeout, c.stream = url, model, timeoutSec, streaming
	})
	return c
}

// postLLM 走完整路由提交一次 LLM 配置。
func postLLM(t *testing.T, s *Server, u *auth.User, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return adminDo(s, adminReq(s, u, http.MethodPost, path, body))
}

// llmResult 解出响应体里的 result 结构。
func llmResult(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON: %v body=%s", err, rr.Body.String())
	}
	res, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 result: %s", rr.Body.String())
	}
	return res
}

// storedKey 读库中生效的多 key 池（逗号分隔）。
func storedKey(t *testing.T, s *Server, uid string) string {
	t.Helper()
	v, _ := s.auth.GetConfig(uid, "llm_api_keys")
	return v
}

const goodKey = "sk-live-good-key"

// 测试地址统一挂在 example.com 下、用不同**路径**区分场景。
//
// 为什么不能各用一个自定义域名：prepareLLMCandidate 会走 validatePublicURL 做真实 DNS +
// 保留地址校验（SSRF 面收口），`*.example` 这类保留域名解析不了会直接 400；
// 而 127.0.0.1（httptest）又会被"禁止指向内网"拦下（见 TestHotUpdateUsesRealProberAgainstUpstream
// 为何直接构造快照而不是走 HTTP 入口）。
const goodURL = "https://example.com/prov-good/v1/chat/completions"

// seedGoodLLM 先落一份"已验证可用"的配置（作为后续"不得被动摇"的现状）。
func seedGoodLLM(t *testing.T, s *Server, u *auth.User, cap *llmCapture) {
	t.Helper()
	stubProbeWith(t) // 全部可用
	rr := postLLM(t, s, u, "/api/config/llm",
		`{"api_keys":["`+goodKey+`"],"api_url":"https://example.com/prov-good/v1/chat/completions","model":"good-model"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("铺垫配置应保存成功, got %d body=%s", rr.Code, rr.Body.String())
	}
	if cap.calls != 1 {
		t.Fatalf("铺垫配置应热切换 1 次, got %d", cap.calls)
	}
}

// TestHotUpdateVerifiedConfigSwapsAndPersists 探测通过 → 切换运行时 + 落库 + 记录为已验证可用。
func TestHotUpdateVerifiedConfigSwapsAndPersists(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	stubProbeWith(t)

	rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-brand-new"],"api_url":"https://api.siliconflow.cn/v1/chat/completions","model":"m-new"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	res := llmResult(t, rr)
	for _, k := range []string{"applied", "persisted", "verified"} {
		if res[k] != true {
			t.Fatalf("%s 应为 true, got %v（result=%v）", k, res[k], res)
		}
	}
	if cap.calls != 1 || len(cap.keys) != 1 || cap.keys[0] != "sk-brand-new" {
		t.Fatalf("热切换应收到 1 把新钥, got calls=%d keys=%v", cap.calls, cap.keys)
	}
	if got := storedKey(t, s, admin.ID); got != "sk-brand-new" {
		t.Fatalf("多 key 池应落库新钥, got %q", got)
	}
	if got, _ := s.auth.GetConfig(admin.ID, "llm_api_key"); got != "sk-brand-new" {
		t.Fatalf("单 key 兼容字段应与多 key 池同值, got %q", got)
	}
	if v, ok := s.lastGoodLLM(); !ok || !v.Verified || v.Keys[0] != "sk-brand-new" {
		t.Fatalf("应留下已验证可用的回滚点, got ok=%v snap=%+v", ok, v)
	}
}

// TestHotUpdateRejectsInvalidConfigAndLeavesEverything 核心不变量：
// 探测确认配置不可用 → 拒绝，**运行时与磁盘一律不动**。
//
// 为什么要连磁盘一起断言：如果只"不切换"却把坏配置落库，重启后进程会加载它 →
// 表现为"当时没坏、重启后彻底起不来"，这正是最需要避免的形态。
func TestHotUpdateRejectsInvalidConfigAndLeavesEverything(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	seedGoodLLM(t, s, admin, cap)
	before := *s.cfg.GetLLMConfigFor(admin.ID)

	stubProbeWith(t, llm.ProbeAuth) // 新 key 探测为"密钥无效"
	rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-typo-key"],"api_url":"https://example.com/prov-typo/v1/chat/completions","model":"typo-model"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("应 409 拒绝, got %d body=%s", rr.Code, rr.Body.String())
	}
	res := llmResult(t, rr)
	if res["rejected"] != true || res["applied"] == true {
		t.Fatalf("应标记为拒绝且未生效, got %v", res)
	}
	if reason, _ := res["reason"].(string); !strings.Contains(reason, "密钥无效") {
		t.Fatalf("拒绝原因应包含供应商侧结论, got %q", reason)
	}
	if cap.calls != 1 {
		t.Fatalf("被拒绝时不得热切换（应仍为铺垫那 1 次）, got %d", cap.calls)
	}
	if got := storedKey(t, s, admin.ID); got != goodKey {
		t.Fatalf("被拒绝时磁盘密钥不得被改写, got %q", got)
	}
	after := *s.cfg.GetLLMConfigFor(admin.ID)
	if after.APIURL != before.APIURL || after.Model != before.Model {
		t.Fatalf("被拒绝时地址/模型不得被改写: before=%s/%s after=%s/%s",
			before.APIURL, before.Model, after.APIURL, after.Model)
	}
}

// TestHotUpdateForceAppliesUnverified 显式 force：绕过拒绝保护，但如实回告"未经验证"，
// 且**不得污染回滚点**（否则翻车后就回不去了）。
func TestHotUpdateForceAppliesUnverified(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	seedGoodLLM(t, s, admin, cap)
	goodSnap, _ := s.lastGoodLLM()

	stubProbeWith(t, llm.ProbeAuth)
	rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-forced"],"api_url":"https://example.com/prov-typo/v1/chat/completions","model":"typo-model","force":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("force 应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	res := llmResult(t, rr)
	if res["applied"] != true || res["verified"] == true {
		t.Fatalf("force 后应标记「已生效但未验证」, got %v", res)
	}
	if w, _ := res["warning"].(string); w == "" {
		t.Fatalf("force 采用未验证配置必须给出告警, got %v", res)
	}
	if cap.calls != 2 || cap.keys[0] != "sk-forced" {
		t.Fatalf("force 应热切换, got calls=%d keys=%v", cap.calls, cap.keys)
	}
	if got := storedKey(t, s, admin.ID); got != "sk-forced" {
		t.Fatalf("force 应落库, got %q", got)
	}
	// 回滚点必须仍是那份**已验证可用**的配置：force 采用未验证配置不得覆盖它。
	got, _ := s.lastGoodLLM()
	if !got.Verified || got.Keys[0] != goodSnap.Keys[0] {
		t.Fatalf("force 不得污染已验证可用的回滚点: got %+v want %+v", got, goodSnap)
	}
}

// TestHotUpdateUnverifiableAdoptsWithWarning 无法判定（网络不可达）→ 采用 + 明确告警 +
// 不标记为已验证。拒绝这一类会把"供应商抖动时想换个 key 试试"的用户堵死。
func TestHotUpdateUnverifiableAdoptsWithWarning(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	stubProbeWith(t, llm.ProbeNetwork)

	rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-net"],"api_url":"https://example.com/prov-net/v1/chat/completions","model":"m"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("无法判定时应采用, got %d body=%s", rr.Code, rr.Body.String())
	}
	res := llmResult(t, rr)
	if res["applied"] != true {
		t.Fatalf("应已生效, got %v", res)
	}
	if res["verified"] == true {
		t.Fatalf("未验证的配置不得标记为 verified, got %v", res)
	}
	w, _ := res["warning"].(string)
	if !strings.Contains(w, "未能验证") {
		t.Fatalf("告警须写明「未能验证」, got %q", w)
	}
	if cap.calls != 1 {
		t.Fatalf("应热切换 1 次, got %d", cap.calls)
	}
}

// TestHotUpdateDropsUnhealthyKeys 多 key：坏的那把被剔除，不混进轮询池、不落库。
// 一把欠费的 key 留在池子里 = 每 N 个请求必然失败一个，且极难归因。
func TestHotUpdateDropsUnhealthyKeys(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	stubProbeWith(t, llm.ProbeAuth, llm.ProbeOK, llm.ProbeRateLimited)

	rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-bad","sk-good","sk-slow"],"api_url":"https://example.com/prov-base/v1/chat/completions","model":"m"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("有可用 key 时应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	res := llmResult(t, rr)
	if res["effective_keys"] != float64(2) || res["dropped_keys"] != float64(1) {
		t.Fatalf("应生效 2 把、剔除 1 把, got %v", res)
	}
	if len(cap.keys) != 2 || cap.keys[0] != "sk-good" || cap.keys[1] != "sk-slow" {
		t.Fatalf("热切换只应收可用子集, got %v", cap.keys)
	}
	if got := storedKey(t, s, admin.ID); got != "sk-good,sk-slow" {
		t.Fatalf("落库应只含可用子集, got %q", got)
	}
}

// TestHotUpdateWithoutAnyKeyRejects 提交全为脱敏哨兵且库中无原值 → 明确 400，
// 且**绝不能拿哨兵去建客户端**（那会把当前可用的客户端打坏，比"不生效"严重得多）。
func TestHotUpdateWithoutAnyKeyRejects(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	stubProbeWith(t)

	rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["kira…1234"],"api_url":"https://example.com/prov-base/v1/chat/completions","model":"m1"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("无可用真钥应 400（而不是假装成功）, got %d body=%s", rr.Code, rr.Body.String())
	}
	if cap.calls != 0 {
		t.Fatalf("无可用真钥时不得热重建, got %d", cap.calls)
	}
	if got := storedKey(t, s, admin.ID); got != "" {
		t.Fatalf("哨兵不得被当成真钥落库, got %q", got)
	}
}

// TestHotUpdateProbeEndpointIsReadOnly "测试连接"绝不能改变任何状态：
// 探测是只读动作，若它顺手切换/落库，用户点一下自检就会把线上客户端换掉。
func TestHotUpdateProbeEndpointIsReadOnly(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	seedGoodLLM(t, s, admin, cap)
	before := *s.cfg.GetLLMConfigFor(admin.ID)

	stubProbeWith(t, llm.ProbeAuth)
	rr := postLLM(t, s, admin, "/api/config/llm/probe",
		`{"api_keys":["sk-candidate"],"api_url":"https://example.com/prov-cand/v1/chat/completions","model":"cand-model"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("探测端点应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	res := llmResult(t, rr)
	if res["applied"] == true {
		t.Fatalf("只探测不得标记为已生效, got %v", res)
	}
	probes, _ := res["probes"].([]any)
	if len(probes) != 1 {
		t.Fatalf("应回告逐把结论, got %v", res["probes"])
	}
	if cap.calls != 1 {
		t.Fatalf("探测不得触发热切换（应仍为铺垫那 1 次）, got %d", cap.calls)
	}
	if got := storedKey(t, s, admin.ID); got != goodKey {
		t.Fatalf("探测不得改写磁盘, got %q", got)
	}
	after := *s.cfg.GetLLMConfigFor(admin.ID)
	if after.APIURL != before.APIURL {
		t.Fatalf("探测不得改写地址: %s → %s", before.APIURL, after.APIURL)
	}
}

// TestHotUpdateRollbackRestoresLastVerified 一键回滚：force 应用了不可用的配置之后，
// 必须能回到上一个**已验证可用**的那一份（运行时与磁盘同时恢复）。
func TestHotUpdateRollbackRestoresLastVerified(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	seedGoodLLM(t, s, admin, cap)
	good := *s.cfg.GetLLMConfigFor(admin.ID)

	// 用力过猛：force 采用一份其实不可用的配置。
	stubProbeWith(t, llm.ProbeAuth)
	if rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-broken"],"api_url":"https://example.com/prov-broken/v1/chat/completions","model":"broken","force":true}`); rr.Code != http.StatusOK {
		t.Fatalf("强制保存应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := storedKey(t, s, admin.ID); got != "sk-broken" {
		t.Fatalf("前置条件：磁盘应为坏配置, got %q", got)
	}

	// 回滚（用真实探测结论仍是"密钥无效"，但回滚以 force 语义执行，必须成功）。
	rr := postLLM(t, s, admin, "/api/config/llm/rollback", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("回滚应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	res := llmResult(t, rr)
	if res["applied"] != true || res["persisted"] != true {
		t.Fatalf("回滚应生效并落库, got %v", res)
	}
	if cap.calls != 3 || cap.keys[0] != goodKey {
		t.Fatalf("回滚应把运行时切回原钥, got calls=%d keys=%v", cap.calls, cap.keys)
	}
	if got := storedKey(t, s, admin.ID); got != goodKey {
		t.Fatalf("回滚应恢复磁盘密钥, got %q", got)
	}
	after := *s.cfg.GetLLMConfigFor(admin.ID)
	if after.APIURL != good.APIURL || after.Model != good.Model {
		t.Fatalf("回滚应恢复地址/模型: got %s/%s want %s/%s", after.APIURL, after.Model, good.APIURL, good.Model)
	}
}

// TestHotUpdateRollbackWithoutSnapshot 无回滚点时明确 404（而不是假装成功）。
func TestHotUpdateRollbackWithoutSnapshot(t *testing.T) {
	s, admin := newAdminTestServer(t)
	rr := postLLM(t, s, admin, "/api/config/llm/rollback", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("无回滚点应 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestHotUpdateUsesRealProberAgainstUpstream 至少一条用例走**真实探测实现**（配合 httptest 假供应商，
// 仍不出网），证明决策链真的接在 llm.ProbeConfig 上——否则"注入点接错了"永远不会被发现。
//
// 这里直接构造快照调用 applyLLMSnapshot 而不走 HTTP 入口：validatePublicURL 会拦下指向
// 127.0.0.1 的地址（"禁止指向内网"，SSRF 面收口），而 httptest 只能在本地端口上起服务。
// 该地址校验本身由既有用例覆盖，本用例要钉的是"探测结论 → 切换与否"的接线。
func TestHotUpdateUsesRealProberAgainstUpstream(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)

	// 假供应商的状态码用闭包变量按用例改写：先固定 401，
	// 检验真实探测链路能否识别「密钥无效」并拒绝热切换。
	status := http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"1"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid token"}}`))
	}))
	defer srv.Close()

	withRealProber(t)
	snap := llmSnapshot{
		Keys:   []string{"sk-real"},
		APIURL: srv.URL + "/v1",
		Model:  "m-real",
	}

	// 上游 401 → 真实探测判为"密钥无效" → 拒绝且不动任何状态。
	if res, code := s.applyLLMSnapshot(admin.ID, snap, false, true, true); code != http.StatusConflict {
		t.Fatalf("上游 401 应被拒绝, got %d res=%+v", code, res)
	}
	if cap.calls != 0 {
		t.Fatalf("被拒绝时不得热切换, got %d", cap.calls)
	}
	if got := storedKey(t, s, admin.ID); got != "" {
		t.Fatalf("被拒绝时不得落库, got %q", got)
	}

	// 上游 200 → 采用并落库。
	status = http.StatusOK
	res, code := s.applyLLMSnapshot(admin.ID, snap, false, true, true)
	if code != http.StatusOK {
		t.Fatalf("上游 200 应被采用, got %d res=%+v", code, res)
	}
	if !res.Verified {
		t.Fatalf("真实探测通过应标记 verified, got %+v", res)
	}
	if cap.calls != 1 || cap.keys[0] != "sk-real" {
		t.Fatalf("应热切换 1 次, got calls=%d keys=%v", cap.calls, cap.keys)
	}
	if got := storedKey(t, s, admin.ID); got != "sk-real" {
		t.Fatalf("应落库, got %q", got)
	}
}

// TestHotUpdateKeepsUnmanagedFieldsAndNumericValues 未提交的字段保持原值（含数值项）。
//
// LLMConfig 里还有 MaxRetryTimes 等本接口不管理的字段：整struct重写会把它们静默清零。
// 数值项（timeout/batch/d1）的 0 同理——照抄 0 会把用户显式设过的 240s 超时清掉，
// 与"空 api_url 清空供应商"是同型的另一扇门。
func TestHotUpdateKeepsUnmanagedFieldsAndNumericValues(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	stubProbeWith(t)

	// 先设一组显式数值。
	if rr := postLLM(t, s, admin, "/api/config/llm",
		`{"api_keys":["sk-a"],"api_url":"https://example.com/prov-base/v1/chat/completions","model":"m1","timeout_sec":240,"batch_concurrency":12,"d1_max_tokens":2048,"classifier_model":"cls"}`); rr.Code != http.StatusOK {
		t.Fatalf("首存应 200, got %d", rr.Code)
	}
	// 再只改 key（咨询页形态：不发送其余字段）→ 其余字段必须原样保留。
	if rr := postLLM(t, s, admin, "/api/config/llm", `{"api_keys":["sk-b"]}`); rr.Code != http.StatusOK {
		t.Fatalf("二存应 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	got := *s.cfg.GetLLMConfigFor(admin.ID)
	if got.APIURL != "https://example.com/prov-base/v1/chat/completions" || got.Model != "m1" {
		t.Fatalf("地址/模型应保持原值: %s/%s", got.APIURL, got.Model)
	}
	if got.TimeoutSec != 240 || got.BatchConcurrency != 12 || got.D1MaxTokens != 2048 || got.ClassifierModel != "cls" {
		t.Fatalf("数值/分类模型应保持原值: timeout=%d batch=%d d1=%d cls=%s",
			got.TimeoutSec, got.BatchConcurrency, got.D1MaxTokens, got.ClassifierModel)
	}
	if cap.timeout != 240 {
		t.Fatalf("热切换应收到保留后的超时, got %d", cap.timeout)
	}
	if got2 := storedKey(t, s, admin.ID); got2 != "sk-b" {
		t.Fatalf("密钥应更新, got %q", got2)
	}
}

// TestHotUpdateAdminEndpointHotAppliesForOperatorAccount 管理端改**运营账号**的配置
// 与设置页走同一套（探测 → 切换 → 落库）——否则"管理端改了不生效"就是第二个不生效入口。
func TestHotUpdateAdminEndpointHotAppliesForOperatorAccount(t *testing.T) {
	s, admin := newAdminTestServer(t)
	cap := captureRecreate(t, s)
	stubProbeWith(t, llm.ProbeAuth)

	rr := postLLM(t, s, admin, "/api/admin/users/"+admin.ID+"/config/llm",
		`{"api_keys":["sk-admin-bad"],"api_url":"https://example.com/prov-bad/v1/chat/completions","model":"bad"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("管理端改运营账号也应探测并拒绝坏配置, got %d body=%s", rr.Code, rr.Body.String())
	}
	if cap.calls != 0 {
		t.Fatalf("被拒绝时不得热切换, got %d", cap.calls)
	}
	if got := storedKey(t, s, admin.ID); got != "" {
		t.Fatalf("被拒绝时不得落库, got %q", got)
	}
}
