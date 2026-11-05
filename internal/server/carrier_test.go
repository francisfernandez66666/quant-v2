// carrier_test.go — §CARRIER-UPDATE（2026-11-05 载体更新批）Go 侧行为锁。
//
// 这一批是"广州外脑 ⇄ 朋友家那根物理腿"之间第一条**可被人工触发**的下行意图通道，
// 所以下面每条用例钉的都是"错一次就要重跑一根腿"的形状，而不是覆盖率：
//
//	① 归属只由那把钥匙决定（§安全 T1）：回执体里自报 user_id=B、而钥匙属于 A ⇒
//	   必须写进 A 的账，B 那本一个字都不许动。"信 body"这个形态在本仓出现过两次
//	   （10-30 的回报归属、10-31 的 first-match），两次都让同一个动作记到别人名下。
//	② 状态机只有一条推进腿 pending→acked/failed，且**回执版本必须等于当前期望版本**；
//	   迟到回执（升版之后才到的旧版答复）只更新 last_ack，绝不能吃掉了 pending。
//	③ 解析不了的回执一律 400 且一个字节都不写：上一份好回执是证据，不能被坏 body 覆盖
//	   （§F18 取向：证据链比"这次调用看起来成功"重要）。
//	④ 控制动作按位放行：quant_trade 只覆盖自己的账，点「更新载体」要 trading_admin，
//	   跨账号再要 user_admin。放行与拒绝**都测**（只测放行的权限锁等于没有锁）。
//	⑤ 清单缺失/坏 JSON/期望版没填齐 ⇒ 一律"未知"，绝不出现"已是最新/正常"那种折叠
//	   （本仓反复出现的缺陷类：§STARVE-STALE 的 J0、§0925EVE-W3-E 的"读不到不装已知"）。
//
// English: behaviour locks for the brain-side carrier manifest — attribution by key only,
// a single pending→acked/failed transition guarded by version equality, unparseable acks
// refused without touching the previous good record, permission gates tested both ways,
// and an absent reading never folded into "up to date".
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
)

// carrierShaSample 一份形状合法的 64 位十六进制哈希（测试里当"期望 SHA256"用）。
const carrierShaSample = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// newCarrierServer 一台带数据目录的测试服（清单读写只需要 cacheDir，不需要引擎/账本）。
func newCarrierServer(t *testing.T) *Server {
	t.Helper()
	s, _ := newAdminTestServer(t)
	s.cacheDir = t.TempDir()
	return s
}

// createCarrierUser 建一个指定权限位的普通用户账号（role=user：本批判据只认显式权限位）。
func createCarrierUser(t *testing.T, s *Server, name string, perms []string) *auth.User {
	t.Helper()
	u, err := s.auth.CreateUser(name, "pw-"+name, auth.RoleUser, perms, 0)
	if err != nil {
		t.Fatalf("建账号 %s 失败: %v", name, err)
	}
	return u
}

// seedCarrierKey 给该**在册账号**落一把自己的网关钥匙，并自证这把钥匙反查得到的就是它。
// 两点都必须真做：归属反查走 s.auth.ListUsers() 遍历 + StoredQMTConfigFor 的"自有钥匙优先"
// 那一趟，所以账号得存在、钥匙得落盘——只改内存或用一个凭空编的 uid，
// 下面整组 ① 用例都会拿到空归属然后"假绿"（本批写第一版时就踩过：编的 uid 不在册）。
func seedCarrierKey(t *testing.T, s *Server, uid, key string) {
	t.Helper()
	cfg := *(s.cfg.GetQMTConfigFor(uid))
	cfg.Token = key
	cfg.GatewayURL = "http://127.0.0.1:18789"
	if err := s.cfg.SetQMTConfigFor(uid, &cfg); err != nil {
		t.Fatalf("给 %s 落钥匙失败: %v", uid, err)
	}
	if got := s.userForQMTToken(key); got != uid {
		t.Fatalf("前提被破坏：钥匙应反查到 %s，实得 %q", uid, got)
	}
}

// newCarrierLeg 造一根"合法在册 + 自带钥匙"的执行腿账号（① 组用例的最小对象）。
func newCarrierLeg(t *testing.T, s *Server, name, key string) *auth.User {
	t.Helper()
	u := createCarrierUser(t, s, name, []string{auth.PermTradingAdmin})
	seedCarrierKey(t, s, u.ID, key)
	return u
}

// writeCarrierFile 直接落一份清单文件（扮演"发布侧写好的期望段"），返回该路径。
func writeCarrierFile(t *testing.T, s *Server, uid string, doc *carrierManifest) string {
	t.Helper()
	path := s.carrierManifestPath(uid)
	if path == "" {
		t.Fatal("cacheDir 未装配，清单无处可落")
	}
	if err := writeCarrierManifest(path, doc); err != nil {
		t.Fatalf("落清单失败: %v", err)
	}
	return path
}

// carrierRawRead 读清单文件的原始字节（"逐字节没被动过"这类断言只能比原文，不能比结构体）。
func carrierRawRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读原始清单失败 %s: %v", path, err)
	}
	return string(raw)
}

// carrierFileExists 文件在不在（用于断言"被拒的点击不许凭空造一份清单"）。
func carrierFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// carrierGetWithKey 用那把钥匙打一次 GET /api/qmt/carrier/manifest。
// 必须走完整中间件而不是裸 handler：这条路由的全部意义就是"钥匙 → 账号"那一段，
// 绕开它等于测了一个不存在的对象。
func carrierGetWithKey(s *Server, key string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/qmt/carrier/manifest", nil)
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	s.qmtReportMiddleware(s.handleCarrierManifestGet)(rr, r)
	return rr
}

// carrierAckWithKey 用那把钥匙 POST 一次回执；body 原样送出（含故意塞进去的 user_id）。
func carrierAckWithKey(s *Server, key, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/qmt/carrier/ack", bytes.NewBufferString(body))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	s.qmtReportMiddleware(s.handleCarrierAckPost)(rr, r)
	return rr
}

// decodeCarrierResp 把响应解成 map（键名就是跨机器契约，逐条点名断言的正是这些键）。
func decodeCarrierResp(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON（%d）: %v body=%s", rr.Code, err, rr.Body.String())
	}
	return out
}

// ───────────── ⑤ 读不到就是"未知"：任何一侧都不许把缺读折成"已是最新" ─────────────

// TestCarrierManifestAbsentReadsUnknown 清单文件不存在 ⇒ 200 + ok=false +
// request_state=unknown + 期望段全空串。关键是响应里**没有任何**"已一致"式的表达。
func TestCarrierManifestAbsentReadsUnknown(t *testing.T) {
	s := newCarrierServer(t)
	a := newCarrierLeg(t, s, "carrier-a", "KEY-A")

	rr := carrierGetWithKey(s, "KEY-A")
	if rr.Code != http.StatusOK {
		t.Fatalf("清单缺失应 200 + ok=false（盒子按 REFUSED 收场），实得 %d", rr.Code)
	}
	body := decodeCarrierResp(t, rr)
	if body["ok"] != false {
		t.Fatalf("清单缺失时 ok 必须是 false，实得 %v（这一格决定盒子动不动盘）", body["ok"])
	}
	if body["request_state"] != carrierStateUnknown {
		t.Fatalf("清单缺失时 request_state 必须是 unknown，实得 %v", body["request_state"])
	}
	if body["unknown_reason"] != "manifest_absent" {
		t.Fatalf("没点名成因 manifest_absent，实得 %v", body["unknown_reason"])
	}
	for _, k := range []string{"strategy_version", "strategy_sha256"} {
		if v, _ := body[k].(string); v != "" {
			t.Fatalf("%s 应为空串（读不到就不许有值），实得 %q", k, v)
		}
	}
	// last_ack 缺席时是 null，不是空对象：前端要能区分"没有回执"与"没读到清单"。
	if v, present := body["last_ack"]; !present || v != nil {
		t.Fatalf("last_ack 缺席时必须是 null，实得 present=%v value=%v", present, v)
	}
	// 读侧也不许顺手创建文件（一次 GET 造一份空清单，会把"发布侧从没写过"这个事实抹平）。
	if carrierFileExists(s.carrierManifestPath(a.ID)) {
		t.Fatal("GET 写了盘")
	}
}

// TestCarrierManifestBadJsonReadsUnknown 文件在但是半截 JSON ⇒ 同一套未知表达，
// 且成因与"文件不存在"不同（两者的处置动作完全不同：一个去补文件，一个查写入方）。
func TestCarrierManifestBadJsonReadsUnknown(t *testing.T) {
	s := newCarrierServer(t)
	bad := newCarrierLeg(t, s, "carrier-bad", "KEY-BAD")
	path := s.carrierManifestPath(bad.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"strategy_version": "v01", `), 0o600); err != nil {
		t.Fatalf("写坏文件失败: %v", err)
	}
	body := decodeCarrierResp(t, carrierGetWithKey(s, "KEY-BAD"))
	if body["ok"] != false || body["request_state"] != carrierStateUnknown {
		t.Fatalf("坏 JSON 必须读成未知: ok=%v state=%v", body["ok"], body["request_state"])
	}
	if body["unknown_reason"] != "manifest_bad_json" {
		t.Fatalf("坏 JSON 的成因应与缺失不同，实得 %v", body["unknown_reason"])
	}
}

// TestCarrierManifestIncompleteDesired 清单可读但期望段没填齐（有版本、没哈希）⇒ 仍然 ok=false，
// 且成因点名到期望段这一侧：没有哈希，盒子根本没法验字节。
func TestCarrierManifestIncompleteDesired(t *testing.T) {
	s := newCarrierServer(t)
	inc := newCarrierLeg(t, s, "carrier-inc", "KEY-INC")
	writeCarrierFile(t, s, inc.ID, &carrierManifest{
		StrategyVersion: "v02-20261104", StrategySha256: "", RequestState: carrierStateIdle,
	})
	body := decodeCarrierResp(t, carrierGetWithKey(s, "KEY-INC"))
	if body["ok"] != false {
		t.Fatalf("缺期望哈希却报 ok=true，等于放盒子投未验字节: %v", body)
	}
	if body["unknown_reason"] != "desired_incomplete_no_version_or_sha" {
		t.Fatalf("成因应点名期望段没填齐，实得 %v", body["unknown_reason"])
	}
	// 版本仍如实带出（排障要看得见"清单里写着什么"），只是不许据此放行。
	if body["strategy_version"] != "v02-20261104" {
		t.Fatalf("读得出的字段不该被一起抹掉: %v", body["strategy_version"])
	}
}

// TestCarrierManifestMalformedShaNeverReady 哈希形状不对（长度够但含非法字符）⇒ 永远不 ready。
// 不拦这一条的后果：每轮"下载→验不过→失败"成为这根腿的日常，而缺口只是发布侧一次笔误。
func TestCarrierManifestMalformedShaNeverReady(t *testing.T) {
	s := newCarrierServer(t)
	leg := newCarrierLeg(t, s, "carrier-sha", "KEY-SHA")
	writeCarrierFile(t, s, leg.ID, &carrierManifest{
		StrategyVersion: "v02", StrategySha256: strings.Repeat("z", 64), RequestState: carrierStateIdle,
	})
	body := decodeCarrierResp(t, carrierGetWithKey(s, "KEY-SHA"))
	if body["ok"] != false || body["unknown_reason"] != "desired_sha256_malformed" {
		t.Fatalf("非法哈希形状必须当场拒: ok=%v reason=%v", body["ok"], body["unknown_reason"])
	}
}

// TestCarrierManifestKnownShape 正向对照：期望段填齐 ⇒ ok=true + 版本/哈希都带出 +
// 状态取文件里那个（idle）。只测"未知"那一侧的锁，会把"永远未知"也判成绿。
func TestCarrierManifestKnownShape(t *testing.T) {
	s := newCarrierServer(t)
	leg := newCarrierLeg(t, s, "carrier-ok", "KEY-OK")
	stamp := "2026-11-05T09:30:00+08:00"
	writeCarrierFile(t, s, leg.ID, &carrierManifest{
		StrategyVersion: "v02-20261104", StrategySha256: carrierShaSample,
		ExecutorVersion: "v01-20261103", ExecutorSha256: carrierShaSample,
		UpdatedAt:    stamp,
		RequestState: carrierStateIdle,
	})
	body := decodeCarrierResp(t, carrierGetWithKey(s, "KEY-OK"))
	if body["ok"] != true {
		t.Fatalf("期望段填齐应 ok=true，实得 %v reason=%v", body["ok"], body["unknown_reason"])
	}
	// 盒子侧读的就是这组键名（qmt_gateway/carrier_updater.py），改任何一个名字都要同批改它。
	for k, want := range map[string]string{
		"strategy_version": "v02-20261104", "strategy_sha256": carrierShaSample,
		"executor_version": "v01-20261103", "updated_at": stamp, "request_state": carrierStateIdle,
	} {
		if got, _ := body[k].(string); got != want {
			t.Errorf("响应键 %s 期望 %q 实得 %q（跨机器契约字段名，不许漂）", k, want, got)
		}
	}
	if _, ok := body["unknown_reason"]; !ok {
		t.Error("unknown_reason 必须恒在位（健康时为空串），别让调用方在两种形状里猜")
	}
	if _, ok := body["requested_at"]; !ok {
		t.Error("requested_at 必须恒在位（没点过时为 null）")
	}
}

// ───────────── ① 归属由钥匙决定：body 里自报的 user_id 一律不信 ─────────────

// TestCarrierAttributionComesFromKeyNotBody A 的钥匙 + body 里写 user_id=B ⇒
// 回执写进 A 那本账，B 那本一个字节不动；GET 也只能看见自己那一份。
func TestCarrierAttributionComesFromKeyNotBody(t *testing.T) {
	s := newCarrierServer(t)
	// 两个账号各持一把**不同**的钥匙（§TOKEN-PERAACCT N2：一把在册钥匙只属于一个账号）。
	a := newCarrierLeg(t, s, "carrier-attr-a", "KEY-A")
	b := newCarrierLeg(t, s, "carrier-attr-b", "KEY-B")
	for _, u := range []*auth.User{a, b} {
		writeCarrierFile(t, s, u.ID, &carrierManifest{
			StrategyVersion: "v-for-" + u.ID, StrategySha256: carrierShaSample, RequestState: carrierStateIdle,
		})
	}

	rr := carrierAckWithKey(s, "KEY-A", `{"strategy_version":"v-for-`+a.ID+`","executor_version":"x1",`+
		`"ok":true,"reason":"applied","user_id":"`+b.ID+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("回执应 200，实得 %d body=%s", rr.Code, rr.Body.String())
	}
	aDoc, _ := readCarrierManifest(s.carrierManifestPath(a.ID))
	bDoc, _ := readCarrierManifest(s.carrierManifestPath(b.ID))
	if aDoc == nil || aDoc.LastAck == nil {
		t.Fatalf("钥匙归属 A 的回执没写进 A 的账: %+v", aDoc)
	}
	if bDoc == nil || bDoc.LastAck != nil {
		t.Fatalf("§安全 T1 违例：body 自报 user_id=B 把回执写进了 B 的账: %+v", bDoc)
	}

	// GET 侧同一判据：A 的钥匙只看得见 A 的期望版本，任何自报身份都不参与。
	if ga := decodeCarrierResp(t, carrierGetWithKey(s, "KEY-A")); ga["strategy_version"] != "v-for-"+a.ID {
		t.Fatalf("A 的钥匙读到了别人的清单: %v", ga["strategy_version"])
	}
	if gb := decodeCarrierResp(t, carrierGetWithKey(s, "KEY-B")); gb["strategy_version"] != "v-for-"+b.ID {
		t.Fatalf("B 的钥匙读到了别人的清单: %v", gb["strategy_version"])
	}
}

// TestCarrierKeylessRequestRejected 没钥匙/假钥匙/普通用户 token：两条盒子侧路由一律 401，
// 且**落不到 handler**（落进去一次，就是"匿名可以往别人家里投递更新意图"）。
func TestCarrierKeylessRequestRejected(t *testing.T) {
	s := newCarrierServer(t)
	_, sessionAdmin := newAdminTestServer(t) // 另造一台只为拿一个合法的用户会话 token
	if code := carrierGetWithKey(s, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("空钥匙 GET 应 401，实得 %d", code)
	}
	if code := carrierAckWithKey(s, "NOT-A-KEY", `{"strategy_version":"v1","ok":true}`).Code; code != http.StatusUnauthorized {
		t.Fatalf("假钥匙 POST 应 401，实得 %d", code)
	}
	// 合法登录 token 不算网关钥匙（与 §GAP2-W1 收权 /api/qmt/report 同一条判据）。
	if code := carrierGetWithKey(s, sessionAdmin.Token).Code; code != http.StatusUnauthorized {
		t.Fatalf("用户 token 不该读到载体清单，实得 %d", code)
	}
}

// ───────────── ② 状态机：只有 pending 这一格会被回执推进 ─────────────

// seedPendingCarrier 落一份"运营已点过、期望版为 wantVer"的清单，返回路径。
func seedPendingCarrier(t *testing.T, s *Server, uid, wantVer string) string {
	t.Helper()
	stamp := "2026-11-05T15:40:00+08:00"
	return writeCarrierFile(t, s, uid, &carrierManifest{
		StrategyVersion: wantVer, StrategySha256: carrierShaSample,
		UpdatedAt:    "2026-11-05T09:00:00+08:00",
		RequestedAt:  &stamp,
		RequestState: carrierStatePending,
	})
}

// TestCarrierAckMovesPendingToAcked 正向推进：pending + 版本对得上 + ok=true ⇒ acked，
// 且 requested_at 与期望段原样保留（回执只许写它自己那一段，不许顺手清掉发布侧写的字段）。
func TestCarrierAckMovesPendingToAcked(t *testing.T) {
	s := newCarrierServer(t)
	leg := newCarrierLeg(t, s, "carrier-ack", "KEY-ACK")
	path := seedPendingCarrier(t, s, leg.ID, "v02-20261104")

	rr := carrierAckWithKey(s, "KEY-ACK", `{"strategy_version":"v02-20261104",`+
		`"executor_version":"v01-20261103","ok":true,"reason":"applied"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("合法回执应 200，实得 %d body=%s", rr.Code, rr.Body.String())
	}
	body := decodeCarrierResp(t, rr)
	if body["request_state"] != carrierStateAcked || body["state_changed"] != true {
		t.Fatalf("pending 应被推进成 acked，实得 %v / changed=%v", body["request_state"], body["state_changed"])
	}
	doc, reason := readCarrierManifest(path)
	if reason != "" || doc == nil {
		t.Fatalf("回执后清单读不出了: %v", reason)
	}
	if doc.RequestState != carrierStateAcked {
		t.Fatalf("落盘状态应为 acked，实得 %q", doc.RequestState)
	}
	if doc.StrategyVersion != "v02-20261104" || doc.StrategySha256 != carrierShaSample {
		t.Fatalf("期望段被回执覆盖了: %+v", doc)
	}
	if doc.RequestedAt == nil || *doc.RequestedAt == "" {
		t.Fatal("requested_at 被回执抹掉了（那次点击的证据就没了）")
	}
	if doc.LastAck == nil || doc.LastAck.Reason != "applied" || doc.LastAck.At == "" {
		t.Fatalf("last_ack 没记全: %+v", doc.LastAck)
	}
	// 响应里的 last_ack 与落盘的 last_ack 必须是同一份（前端直接渲染响应，不再读一次盘）。
	if ack, _ := body["last_ack"].(map[string]any); ack == nil || ack["reason"] != "applied" {
		t.Fatalf("响应没回显 last_ack: %v", body["last_ack"])
	}
}

// TestCarrierAckMovesPendingToFailed ok=false 同样推进状态（失败也是一种答复）。
// 只把"成功"接进状态机的后果：运营侧永远停在 pending，于是再点一次——而再点一次结果一样。
func TestCarrierAckMovesPendingToFailed(t *testing.T) {
	s := newCarrierServer(t)
	leg := newCarrierLeg(t, s, "carrier-fail", "KEY-FAIL")
	path := seedPendingCarrier(t, s, leg.ID, "v02-20261104")
	rr := carrierAckWithKey(s, "KEY-FAIL", `{"strategy_version":"v02-20261104","executor_version":"unknown",`+
		`"ok":false,"reason":"downloaded_sha_mismatch"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("失败回执也要 200 收下，实得 %d", rr.Code)
	}
	doc, _ := readCarrierManifest(path)
	if doc.RequestState != carrierStateFailed {
		t.Fatalf("ok=false 应把 pending 判成 failed，实得 %q", doc.RequestState)
	}
	if doc.LastAck == nil || doc.LastAck.OK {
		t.Fatalf("失败回执被记成了成功: %+v", doc.LastAck)
	}
}

// TestCarrierLateAckNeverEatsPending 迟到回执（版本≠当前期望版）只更新 last_ack、
// 状态**留在 pending**，并把点名原因回给调用方。
// 这条是本批最贵的一条：期望版升到 v03 之后，盒子那条 v02 的答复若吃掉 pending，
// v03 这次点击就永远显示"已确认"，而那台机器实际从没被要求换版。
func TestCarrierLateAckNeverEatsPending(t *testing.T) {
	s := newCarrierServer(t)
	leg := newCarrierLeg(t, s, "carrier-late", "KEY-LATE")
	path := seedPendingCarrier(t, s, leg.ID, "v03-20261110") // 期望已经是 v03

	rr := carrierAckWithKey(s, "KEY-LATE", `{"strategy_version":"v02-20261104","executor_version":"v1",`+
		`"ok":true,"reason":"applied"}`)
	body := decodeCarrierResp(t, rr)
	if body["request_state"] != carrierStatePending {
		t.Fatalf("迟到回执把 pending 吃掉了: %v", body["request_state"])
	}
	if body["state_changed"] != false {
		t.Fatalf("状态没推进却报了 changed=true: %v", body["state_changed"])
	}
	if body["note"] != "ack_kept_pending_version_differs" {
		t.Fatalf("没点名版本对不上这条原因: %v", body["note"])
	}
	doc, _ := readCarrierManifest(path)
	if doc.LastAck == nil || doc.LastAck.StrategyVersion != "v02-20261104" {
		t.Fatalf("迟到回执连 last_ack 都没记上: %+v", doc.LastAck)
	}
	// 上屏读数也要把"答的不是当前版"点名出来（比对只留一份判据，不在前端各抄一遍）。
	if read := s.carrierStateReadout(leg.ID); !read.AckIsStale {
		t.Fatal("carrier 读数没把 stale 回执点名，页面上会显示成刚答完的样子")
	}
}

// TestCarrierAckWithoutPendingOnlyRecords 非 pending（idle/acked/failed）收到无人请求的回执
// ⇒ 只更新 last_ack，状态一格都不动：回执是"答复"，不是"某次请求被满足了"。
func TestCarrierAckWithoutPendingOnlyRecords(t *testing.T) {
	s := newCarrierServer(t)
	leg := newCarrierLeg(t, s, "carrier-idle", "KEY-IDLE")
	for _, start := range []string{carrierStateIdle, carrierStateAcked, carrierStateFailed} {
		writeCarrierFile(t, s, leg.ID, &carrierManifest{
			StrategyVersion: "v02", StrategySha256: carrierShaSample, RequestState: start,
		})
		rr := carrierAckWithKey(s, "KEY-IDLE", `{"strategy_version":"v02","ok":true,"reason":"up_to_date"}`)
		body := decodeCarrierResp(t, rr)
		if body["state_changed"] != false {
			t.Fatalf("起点 %s：无人请求却推进了状态: %v", start, body)
		}
		if body["request_state"] != start {
			t.Fatalf("起点 %s：状态被改了，实得 %v", start, body["request_state"])
		}
	}
}

// ───────────── ③ 坏回执一律拒，且一个字节都不写 ─────────────

// TestCarrierAckUnparseableRefused 各类坏 body 各自 400，并且**保留上一份好回执**。
// 判据取"文件逐字节没变"而不是"结构体差不多"——覆盖往往就发生在没人盯着的那个字段上。
func TestCarrierAckUnparseableRefused(t *testing.T) {
	s := newCarrierServer(t)
	leg := newCarrierLeg(t, s, "carrier-badack", "KEY-BADACK")
	path := writeCarrierFile(t, s, leg.ID, &carrierManifest{
		StrategyVersion: "v02", StrategySha256: carrierShaSample, RequestState: carrierStateIdle,
		LastAck: &carrierAck{StrategyVersion: "v02", OK: true, Reason: "good-before", At: "2026-11-05T15:00:00+08:00"},
	})
	before := carrierRawRead(t, path)

	cases := []struct{ name, body string }{
		{"半截 JSON", `{"strategy_version":"v02", "ok":`},
		{"不是对象", `["v02", true]`},
		{"缺 ok", `{"strategy_version":"v02","reason":"applied"}`},
		{"ok 不是布尔", `{"strategy_version":"v02","ok":"yes"}`},
		{"缺版本", `{"ok":true,"reason":"applied"}`},
		{"空版本", `{"strategy_version":"   ","ok":true}`},
		{"版本带空格", `{"strategy_version":"v02 20261104","ok":true}`},
		{"版本带路径段", `{"strategy_version":"../etc/passwd","ok":true}`},
		{"版本超长", `{"strategy_version":"` + strings.Repeat("v", 65) + `","ok":true}`},
		{"reason 超长", `{"strategy_version":"v02","ok":true,"reason":"` + strings.Repeat("x", 201) + `"}`},
	}
	for _, c := range cases {
		rr := carrierAckWithKey(s, "KEY-BADACK", c.body)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s：应 400 且一条都不写，实得 %d body=%s", c.name, rr.Code, rr.Body.String())
		}
	}
	if after := carrierRawRead(t, path); after != before {
		t.Fatalf("坏回执改动了清单（这一批一次都不该写）:\n before=%s\n after=%s", before, after)
	}
	doc, _ := readCarrierManifest(path)
	if doc.LastAck == nil || doc.LastAck.Reason != "good-before" {
		t.Fatalf("上一份好回执被垃圾覆盖了: %+v", doc.LastAck)
	}

	// 对照面：控制字符（换行）不是"坏"，折成空格后照常收下——
	// 但落盘的 reason 里不许留换行，否则盒子侧那行 k=v 日志会被劈成两半。
	rr := carrierAckWithKey(s, "KEY-BADACK", `{"strategy_version":"v02","ok":true,"reason":"a\nb"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("带换行的合法回执应收下（换行折成空格），实得 %d body=%s", rr.Code, rr.Body.String())
	}
	doc2, _ := readCarrierManifest(path)
	if strings.ContainsAny(doc2.LastAck.Reason, "\n\r") {
		t.Fatalf("换行进了 reason: %q", doc2.LastAck.Reason)
	}
}

// TestCarrierAckNoAccountDir 数据目录没装配 ⇒ 503 而不是 200。
// "回执没地方记"绝不能被盒子当成"回执成功"，否则页面显示一次根本没落地的 acked。
func TestCarrierAckNoAccountDir(t *testing.T) {
	s, _ := newAdminTestServer(t) // 刻意不调 SetCacheDir：cacheDir 为空串
	newCarrierLeg(t, s, "carrier-nodir", "KEY-NODIR")
	rec := carrierAckWithKey(s, "KEY-NODIR", `{"strategy_version":"v02","ok":true,"reason":"applied"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("目录未装配应 503，实得 %d body=%s", rec.Code, rec.Body.String())
	}
	// GET 侧同一取向：目录不可用时报"未知"，不报"没配过所以是 idle"。
	if body := decodeCarrierResp(t, carrierGetWithKey(s, "KEY-NODIR")); body["request_state"] != carrierStateUnknown {
		t.Fatalf("目录未装配时 GET 应 unknown，实得 %v", body["request_state"])
	}
}

// ───────────── ④ 控制动作的权限面：放行与拒绝都测 ─────────────

// carrierUpdate 走完整路由打一次「更新载体」（权限层先于 handler，必须用 mux 才测得准）。
func carrierUpdate(s *Server, u *auth.User, body string) *httptest.ResponseRecorder {
	return adminDo(s, adminReq(s, u, http.MethodPost, "/api/qmt/carrier/update", body))
}

// carrierDesiredFile 给该账号落一份"发布侧已写好期望段"的清单（点击的前置条件）。
func carrierDesiredFile(t *testing.T, s *Server, uid string) string {
	t.Helper()
	return writeCarrierFile(t, s, uid, &carrierManifest{
		StrategyVersion: "v02-20261104", StrategySha256: carrierShaSample, RequestState: carrierStateIdle,
	})
}

// TestCarrierUpdateNeedsTradingAdmin quant_trade（只管自己那本账）不足以触发一次真实换版；
// trading_admin 可以；零权限位更不行。放行与拒绝都测——只测"能进"的那把锁，写错位也不会红。
func TestCarrierUpdateNeedsTradingAdmin(t *testing.T) {
	s := newCarrierServer(t)
	friend := createCarrierUser(t, s, "carrier-friend", []string{auth.PermQuantTrade})
	trader := createCarrierUser(t, s, "carrier-trader", []string{auth.PermTradingAdmin})

	// 只持 quant_trade：403，且错误体里不许夹带任何状态变更的痕迹。
	rr := carrierUpdate(s, friend, `{}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("quant_trade 不该触发载体更新，实得 %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), carrierStatePending) {
		t.Fatal("被拒的响应里出现了 pending（等于把状态变更藏进错误体）")
	}
	// 只持 trading_admin：放行，并真的把自己那根腿置成 pending。
	carrierDesiredFile(t, s, trader.ID)
	ok := carrierUpdate(s, trader, `{}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("trading_admin 应能点自己的腿，实得 %d body=%s", ok.Code, ok.Body.String())
	}
	doc, _ := readCarrierManifest(s.carrierManifestPath(trader.ID))
	if doc.RequestState != carrierStatePending {
		t.Fatalf("点击后应 pending，实得 %q", doc.RequestState)
	}
	// 零权限位成员同样进不来（role=user；也不许凭"登录了"就动实盘）。
	plain := createCarrierUser(t, s, "carrier-plain", nil)
	if code := carrierUpdate(s, plain, `{}`).Code; code != http.StatusForbidden {
		t.Fatalf("零权限位应 403，实得 %d", code)
	}
	// role=admin 但零显式位：本仓的权限模型里角色不授予任何能力（§F-6），所以仍 403。
	quietAdmin, err := s.auth.CreateUser("carrier-quiet-admin", "pw", auth.RoleAdmin, nil, 0)
	if err != nil {
		t.Fatalf("建 admin 失败: %v", err)
	}
	if code := carrierUpdate(s, quietAdmin, `{}`).Code; code != http.StatusForbidden {
		t.Fatalf("role=admin 零显式位不该进控制面，实得 %d", code)
	}
}

// TestCarrierUpdateCrossAccountNeedsUserAdmin 代别人点要 user_admin + trading_admin 两位；
// 只持 trading_admin 时碰别人的账号 403，别人的清单一个字都不动。
func TestCarrierUpdateCrossAccountNeedsUserAdmin(t *testing.T) {
	s := newCarrierServer(t)
	trader := createCarrierUser(t, s, "carrier-x-trader", []string{auth.PermTradingAdmin})
	other := createCarrierUser(t, s, "carrier-x-other", []string{auth.PermQuantTrade})
	operator := createCarrierUser(t, s, "carrier-x-op", []string{auth.PermTradingAdmin, auth.PermUserAdmin})
	for _, u := range []*auth.User{trader, other, operator} {
		carrierDesiredFile(t, s, u.ID)
	}
	// 只持 trading_admin 去点别人：403，且别人仍是 idle。
	rr := carrierUpdate(s, trader, `{"user_id":"`+other.ID+`"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("跨账号缺 user_admin 应 403，实得 %d body=%s", rr.Code, rr.Body.String())
	}
	if doc, _ := readCarrierManifest(s.carrierManifestPath(other.ID)); doc.RequestState != carrierStateIdle {
		t.Fatalf("被拒的跨账号点击改了别人的清单: %q", doc.RequestState)
	}
	// 两位都在：允许，且响应明确点名这次动的是谁的腿（值班要看得见自己按下去的是别人家）。
	ok := carrierUpdate(s, operator, `{"user_id":"`+other.ID+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("user_admin+trading_admin 应能代点，实得 %d body=%s", ok.Code, ok.Body.String())
	}
	body := decodeCarrierResp(t, ok)
	if body["target_user_id"] != other.ID {
		t.Fatalf("响应没点名目标账号: %v", body["target_user_id"])
	}
	doc2, _ := readCarrierManifest(s.carrierManifestPath(other.ID))
	if doc2.RequestState != carrierStatePending || doc2.RequestedAt == nil {
		t.Fatalf("代点后别人的清单应 pending + 带时刻: %+v", doc2)
	}
	// 目标账号不存在 ⇒ 404，且不凭空造一份清单文件（陌生 uid 不该在磁盘上留下目录）。
	missing := carrierUpdate(s, operator, `{"user_id":"u_does_not_exist"}`)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("未知目标应 404，实得 %d", missing.Code)
	}
	if carrierFileExists(s.carrierManifestPath("u_does_not_exist")) {
		t.Fatal("给不存在的账号造了清单文件")
	}
}

// TestCarrierUpdateNeverClaimsApplied 响应必须自带 updated=false 与那句"此刻还没有任何东西被更新"。
// 这是本批最容易说谎的地方：一次点击确实成功了，但落地的是意图，不是那台机器上的载体文件。
func TestCarrierUpdateNeverClaimsApplied(t *testing.T) {
	s := newCarrierServer(t)
	trader := createCarrierUser(t, s, "carrier-worder", []string{auth.PermTradingAdmin})
	carrierDesiredFile(t, s, trader.ID)
	body := decodeCarrierResp(t, carrierUpdate(s, trader, `{}`))
	if body["updated"] != false {
		t.Fatalf("响应声称 updated=true（此刻只有意图落地，没有任何字节被动过）: %v", body["updated"])
	}
	if body["request_state"] != carrierStatePending {
		t.Fatalf("点击后应回 pending，实得 %v", body["request_state"])
	}
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "还没有任何东西被更新") {
		t.Fatalf("message 没把「没更新」这句话说出来: %q", msg)
	}
	if !strings.Contains(msg, "轮询") {
		t.Fatalf("message 没说清什么时候才会发生（下一次轮询）: %q", msg)
	}
	if at, _ := body["requested_at"].(string); at == "" {
		t.Fatal("缺 requested_at：值班无从判断这条 pending 挂了多久")
	}
	if p, _ := body["prev_request_state"].(string); p != carrierStateIdle {
		t.Fatalf("没回显置位前的状态: %q", p)
	}
	// 落盘的那份文件里回执段被改了、期望段一字未动（发布侧的字段归发布侧）。
	doc, _ := readCarrierManifest(s.carrierManifestPath(trader.ID))
	if doc.StrategyVersion != "v02-20261104" || doc.StrategySha256 != carrierShaSample {
		t.Fatalf("置位动了期望段: %+v", doc)
	}
	// 上屏读数同步：state 里的 carrier 必须也变成 pending（前端只读这一本账）。
	if read := s.carrierStateReadout(trader.ID); read.RequestState != carrierStatePending {
		t.Fatalf("读数没跟上点击结果: %q", read.RequestState)
	}
}

// TestCarrierUpdateRefusesWhenDesiredMissing 期望段没填齐 ⇒ 409 + updated=false，
// 并且**不置 pending**：悬着的 pending 会被读成"盒子没响应"，而真实缺口在发布侧。
func TestCarrierUpdateRefusesWhenDesiredMissing(t *testing.T) {
	s := newCarrierServer(t)
	trader := createCarrierUser(t, s, "carrier-nodesired", []string{auth.PermTradingAdmin})
	// 情形一：根本没有清单文件。
	rr := carrierUpdate(s, trader, `{}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("清单缺失时点击应 409（不是 200），实得 %d body=%s", rr.Code, rr.Body.String())
	}
	body := decodeCarrierResp(t, rr)
	if body["updated"] != false || body["reason"] != "manifest_absent" {
		t.Fatalf("409 的形状不对: %v", body)
	}
	if carrierFileExists(s.carrierManifestPath(trader.ID)) {
		t.Fatal("被拒的点击凭空创建了清单文件")
	}
	// 情形二：有文件但缺哈希。
	writeCarrierFile(t, s, trader.ID, &carrierManifest{StrategyVersion: "v02", RequestState: carrierStateIdle})
	rr2 := carrierUpdate(s, trader, `{}`)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("缺期望哈希时点击应 409，实得 %d", rr2.Code)
	}
	if b2 := decodeCarrierResp(t, rr2); b2["reason"] != "desired_incomplete_no_version_or_sha" {
		t.Fatalf("成因没点名期望段: %v", b2["reason"])
	}
	doc2, _ := readCarrierManifest(s.carrierManifestPath(trader.ID))
	if doc2.RequestState != carrierStateIdle {
		t.Fatalf("被拒的点击把状态置成了 %q（应当一格不动）", doc2.RequestState)
	}
}

// TestCarrierUpdateBadBodyRefused 拼错/半截的 body 必须当场失败：
// 静默丢一个字段会被当成"给自己点了一下"，而调用方以为点的是别人那根腿。
func TestCarrierUpdateBadBodyRefused(t *testing.T) {
	s := newCarrierServer(t)
	trader := createCarrierUser(t, s, "carrier-badbody", []string{auth.PermTradingAdmin})
	carrierDesiredFile(t, s, trader.ID)
	if code := carrierUpdate(s, trader, `{"user_id": `).Code; code != http.StatusBadRequest {
		t.Fatalf("半截 body 应 400，实得 %d", code)
	}
	// 空 body 是合法的（等价于"给自己这根腿点一下"），不许因此 400 拦住止血通道。
	if code := carrierUpdate(s, trader, ``).Code; code != http.StatusOK {
		t.Fatalf("空 body 应视作给自己点，实得 %d", code)
	}
}

// ───────────── ⑤ 上屏：carrier 读数并进 /api/qmt/state，未知不许长成"正常" ─────────────

// TestCarrierStateReadoutUnknownNotUpToDate 读不到清单时读数的形状：known=false、
// request_state=unknown、desired_ready=false，且 installed_matches_desired 必须是 null。
func TestCarrierStateReadoutUnknownNotUpToDate(t *testing.T) {
	s := newCarrierServer(t)
	read := s.carrierStateReadout("u_nobody")
	if read.Known || read.DesiredReady {
		t.Fatalf("读不到却把 known/desired_ready 立起来了: %+v", read)
	}
	if read.RequestState != carrierStateUnknown {
		t.Fatalf("未知态应显式说 unknown，实得 %q", read.RequestState)
	}
	if read.InstalledMatchesDesired != nil {
		t.Fatalf("任一侧读不到时不许给布尔结论，实得 %v", *read.InstalledMatchesDesired)
	}
	if read.UnknownReason == "" {
		t.Fatal("未知必须带原因，否则页面只剩一句没有下文的「未知」")
	}
	// 空 uid 同样不许落到运营账号那本账上（§FIX-20261028-F01 读面纪律的镜像）。
	if empty := s.carrierStateReadout(""); empty.UnknownReason != "uid_absent" {
		t.Fatalf("空 uid 应点名 uid_absent，实得 %q", empty.UnknownReason)
	}
}

// TestCarrierStateReadoutSurfacedOnStateRoute 接线腿：读数真的挂在 GET /api/qmt/state 的
// carrier 键上（前端只读这一本账；另开一条读面会造出第二份"该装哪一版"）。
func TestCarrierStateReadoutSurfacedOnStateRoute(t *testing.T) {
	s := newCarrierServer(t)
	trader := createCarrierUser(t, s, "carrier-screen", []string{auth.PermTradingAdmin})
	carrierDesiredFile(t, s, trader.ID)
	// 先落一枚上行腿读数，让"装的 vs 跑的"这一比有对象（没有对象时它必须是 null）。
	s.recordLegStatus(trader.ID, qmtReportEvent{Type: "heartbeat", StrategyVersion: "v02-20261104"}, time.Now())

	rr := adminDo(s, adminReq(s, trader, http.MethodGet, "/api/qmt/state", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("state 应 200，实得 %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("state 响应不是 JSON: %v", err)
	}
	carrier, ok := body["carrier"].(map[string]any)
	if !ok {
		t.Fatalf("carrier 没并进 /api/qmt/state: %v", body["carrier"])
	}
	if carrier["known"] != true || carrier["request_state"] != carrierStateIdle {
		t.Fatalf("读数没把清单内容透出来: %v", carrier)
	}
	if carrier["strategy_version"] != "v02-20261104" {
		t.Fatalf("期望版本漂了: %v", carrier["strategy_version"])
	}
	// 三态里最要紧的那一枚：装的==跑的 ⇒ true（两侧都有值时才许给结论）。
	if carrier["installed_matches_desired"] != true {
		t.Fatalf("版本一致却没判 true（前端会一直停在待确认）: %v", carrier["installed_matches_desired"])
	}
	// 既有键一个不许少（superset 锁在 leg_status_test.go，这里只验这条路由真的走通）。
	for _, k := range []string{"enabled", "mode", "tripped", "gateway_url", "leg_status"} {
		if _, present := body[k]; !present {
			t.Errorf("carrier 接线把既有键 %q 弄丢了", k)
		}
	}
}

// TestCarrierReadoutUnknownOnStateRoute 未接实盘 + 清单缺失时，state 里的 carrier 仍在位且
// known=false——前端拿到的是"这个读数读不到"，不是一片空白，更不是"正常"。
func TestCarrierReadoutUnknownOnStateRoute(t *testing.T) {
	s := newCarrierServer(t)
	trader := createCarrierUser(t, s, "carrier-screen2", []string{auth.PermTradingAdmin})
	rr := adminDo(s, adminReq(s, trader, http.MethodGet, "/api/qmt/state", ""))
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("state 响应不是 JSON: %v", err)
	}
	carrier, ok := body["carrier"].(map[string]any)
	if !ok {
		t.Fatalf("未接实盘的分支缺 carrier 键（前端会在「字段缺失」与「没有读数」之间猜）: %v", body["carrier"])
	}
	if carrier["known"] != false || carrier["request_state"] != carrierStateUnknown {
		t.Fatalf("清单缺失时读数必须显式未知: known=%v state=%v", carrier["known"], carrier["request_state"])
	}
	if v, present := carrier["installed_matches_desired"]; !present || v != nil {
		t.Fatalf("判不了时 installed_matches_desired 必须是 null，实得 present=%v value=%v", present, v)
	}
}

// TestCarrierUnknownStateNeverSaysNormal 逐条钉死"未知"这一格在任何可读字段里都不许长成"正常"。
// 这是给门禁与文案用的行为锚（本仓最重复的缺陷类：把缺席折叠成正向结论）。
func TestCarrierUnknownStateNeverSaysNormal(t *testing.T) {
	s := newCarrierServer(t)
	newCarrierLeg(t, s, "carrier-wording", "KEY-WORD")
	blob, err := json.Marshal(decodeCarrierResp(t, carrierGetWithKey(s, "KEY-WORD")))
	if err != nil {
		t.Fatal(err)
	}
	text := string(blob)
	for _, forbidden := range []string{"up_to_date", "已是最新", "正常"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("未知态的响应里出现了 %q（缺席被折成了正向结论）", forbidden)
		}
	}
	if !strings.Contains(text, carrierStateUnknown) {
		t.Fatalf("未知态必须写 unknown，实得 %s", text)
	}
}

// TestCarrierPathBuiltOnlyFromUid 路径只由服务端拼：固定文件名 + 账号目录，
// 任何情况下都不许拼到 accounts/<uid>/ 之外（将来加产物路由时同一条判据）。
func TestCarrierPathBuiltOnlyFromUid(t *testing.T) {
	s := newCarrierServer(t)
	good := s.carrierManifestPath("u_normal")
	if !strings.HasSuffix(good, filepath.Join("accounts", "u_normal", carrierManifestFileName)) {
		t.Fatalf("清单路径形状不对: %s", good)
	}
	// cacheDir 未装配 ⇒ 空串（调用方按不可用处理，绝不退化成相对路径去写盘）。
	var bare Server
	if bare.carrierManifestPath("u_x") != "" {
		t.Fatal("cacheDir 缺位时不许拼出可写路径")
	}
	// 空 uid ⇒ 同样空串（否则所有账号的期望版本灌进同一个文件）。
	if s.carrierManifestPath("") != "" || s.carrierManifestPath("   ") != "" {
		t.Fatal("空 uid 不许拼出路径（跨账号混淆账的入口）")
	}
}

// TestCarrierUnknownStateReadsBadPersistedState 文件里写着一个认不出的 request_state（手工编辑笔误）
// ⇒ 读侧报 unknown，而不是"当成 idle"。认不出就是读不懂，读不懂不等于"空闲、无需动作"。
func TestCarrierUnknownStateReadsBadPersistedState(t *testing.T) {
	s := newCarrierServer(t)
	leg := newCarrierLeg(t, s, "carrier-statetypo", "KEY-TYPO")
	writeCarrierFile(t, s, leg.ID, &carrierManifest{
		StrategyVersion: "v02", StrategySha256: carrierShaSample, RequestState: "pendingg",
	})
	if got := normalizeCarrierState("pendingg"); got != carrierStateUnknown {
		t.Fatalf("认不出的状态值应归 unknown，实得 %q", got)
	}
	body := decodeCarrierResp(t, carrierGetWithKey(s, "KEY-TYPO"))
	if body["request_state"] != carrierStateUnknown {
		t.Fatalf("GET 侧没把笔误状态报成 unknown: %v", body["request_state"])
	}
	// 对照：空串＝从没点过＝idle（合法空态，与"读不懂"是两格，不许合并）。
	if normalizeCarrierState("") != carrierStateIdle {
		t.Fatal("空状态应归 idle（从没点过），别和读不懂混成一格")
	}
}
