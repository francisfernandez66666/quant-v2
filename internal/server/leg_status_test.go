// leg_status_test.go — §EXT-BRAIN-UP（2026-11-05 外脑上行腿批）Go 侧行为锁。
//
// 钉四件事，每条都是形状罕见的错账姿势，不是"顺手加的断言"：
//
//	① 携带 strategy_version 的回报要**落进账号目录**并能从读面拿回（装的是不是跑的是不是同一份）；
//	② 空版本**绝不覆盖**已知版本，也不推进 seen_at（"这轮没带"≠"版本没了"≠"依然新鲜"）；
//	③ GET /api/qmt/state 的输出必须是 trading.StateSnapshot 的**逐字段超集**
//	   ——既有键一个不许改名/消失，只许多出 leg_status（§M12-A/§INDEX-BLIND 一贯的加锁姿势）；
//	④ golden 的 report_uplink_emitted_fields（本地普通进程上行发出集）⊆ qmtReportEvent 反射集
//	   ——§M4 丢腿锁的上行侧镜像：这条腿以后加键不加 tag，这里先红。
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/auth"
	"quant-trading-v2/internal/trading"
)

// newLegStatusServer 造一个只挂了 cacheDir 的 Server（腿状态写面不需要引擎/账本——
// 它按设计是纯观测文件，账号目录在不在都照常工作，缺了只打日志）。
func newLegStatusServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{}
	s.SetCacheDir(t.TempDir())
	return s
}

// withUserReq 给请求注入认证用户（回报归属由 token→uid 解析，这里直接按 handler 视角钉 uid）。
func withUserReq(uid string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/qmt/state", nil)
	return r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &auth.User{ID: uid}))
}

// TestLegStatusPersistedAndSurfaced 行为腿 ①：版本落账号目录 + /api/qmt/state 以 leg_status 上屏。
func TestLegStatusPersistedAndSurfaced(t *testing.T) {
	s := newLegStatusServer(t)
	ev := qmtReportEvent{Type: "heartbeat", StrategyVersion: "v02-20261104",
		Reason: "uplink_version=uplink-v01-20261105 executor_version=v01-20261103 " +
			"executor_fingerprint=abc123def456"}
	s.recordLegStatus("u_leg01", ev, time.Now())

	path := filepath.Join(s.cacheDir, "accounts", "u_leg01", legStatusFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("腿状态未落账号目录 %s: %v", path, err)
	}
	st := s.readLegStatus("u_leg01")
	if st == nil || st.StrategyVersion != "v02-20261104" || st.SeenAt == "" {
		t.Fatalf("读回的腿状态不对: %+v", st)
	}
	if st.ExecutorVersion != "v01-20261103" || st.ExecutorFingerprint != "abc123def456" {
		t.Fatalf("heartbeat reason 里的执行器版本/指纹没解出来: %+v", st)
	}

	// 读面接线：/api/qmt/state（未接实盘分支）也必须带 leg_status，且不吞既有四键。
	rec := httptest.NewRecorder()
	s.handleQMTState(rec, withUserReq("u_leg01"))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("state 响应不是 JSON: %v", err)
	}
	for _, k := range []string{"enabled", "mode", "tripped", "gateway_url"} {
		if _, ok := body[k]; !ok {
			t.Errorf("未接实盘分支少了既有键 %q（superset 锁不许改既有形状）", k)
		}
	}
	leg, ok := body["leg_status"].(map[string]any)
	if !ok || leg["strategy_version"] != "v02-20261104" {
		t.Fatalf("leg_status 没并进 /api/qmt/state: %v", body["leg_status"])
	}
}

// TestLegStatusEmptyVersionNeverOverwrites 行为腿 ②：空版本不覆盖已知、也不推进 seen_at。
func TestLegStatusEmptyVersionNeverOverwrites(t *testing.T) {
	s := newLegStatusServer(t)
	first := time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	s.recordLegStatus("u_a", qmtReportEvent{Type: "heartbeat", StrategyVersion: "v02-20261104"}, first)
	before := s.readLegStatus("u_a")

	// 一轮"什么都没带"的回报（旧网关成交回报就是这个形状：无版本、reason 也非上行键值串）。
	s.recordLegStatus("u_a", qmtReportEvent{Type: "trade", Reason: "柜台废单原因=资金不足"}, first.Add(time.Hour))
	after := s.readLegStatus("u_a")
	if after == nil || after.StrategyVersion != "v02-20261104" {
		t.Fatalf("空版本把已知版本弄丢了: %+v", after)
	}
	if after.SeenAt != before.SeenAt {
		t.Errorf("没收到任何读数却推进了 seen_at（把\"没带\"记成\"依然新鲜\"）: %s -> %s", before.SeenAt, after.SeenAt)
	}

	// 反向：只有执行器指纹更新（策略文件暂时读不到）→ 版本保留、指纹推进、seen_at 推进。
	s.recordLegStatus("u_a", qmtReportEvent{Type: "heartbeat",
		Reason: "executor_fingerprint=fff000aaa111"}, first.Add(2*time.Hour))
	merged := s.readLegStatus("u_a")
	if merged.StrategyVersion != "v02-20261104" || merged.ExecutorFingerprint != "fff000aaa111" {
		t.Fatalf("reason 增量合并错了: %+v", merged)
	}

	// 真换了版本才覆盖。
	s.recordLegStatus("u_a", qmtReportEvent{Type: "heartbeat", StrategyVersion: "v03-20261110"}, first.Add(3*time.Hour))
	if got := s.readLegStatus("u_a"); got.StrategyVersion != "v03-20261110" {
		t.Fatalf("非空版本应当覆盖已知: %+v", got)
	}
}

// TestLegStatusNoCrossAccountBleed 写面纪律：cacheDir/uid 任一为空一律不落盘，
// 否则所有账号的腿状态灌进同一个文件（§FIX-20261028-F01 读面纪律的写面镜像）。
func TestLegStatusNoCrossAccountBleed(t *testing.T) {
	s := newLegStatusServer(t)
	s.recordLegStatus("", qmtReportEvent{Type: "heartbeat", StrategyVersion: "v02"}, time.Now())
	if _, err := os.Stat(filepath.Join(s.cacheDir, "accounts", "", legStatusFileName)); err == nil {
		t.Fatal("空 uid 落盘了——跨账号混淆账的入口")
	}
	var bare Server
	bare.recordLegStatus("u_x", qmtReportEvent{Type: "heartbeat", StrategyVersion: "v02"}, time.Now())
	if bare.readLegStatus("u_x") != nil {
		t.Fatal("cacheDir 缺位时不得凭空造读数")
	}
}

// TestQMTStateSupersetOfStateSnapshot 行为腿 ③：已接实盘分支的响应必须是
// trading.StateSnapshot 的**逐字段超集**——用反射取全部 json tag，再对包装后的
// 实际载荷逐键比对（字段名一个不许变，值透传不许折）。
func TestQMTStateSupersetOfStateSnapshot(t *testing.T) {
	snap := trading.StateSnapshot{
		Enabled: true, FriendLegState: 1, Mode: "live", Tripped: false, TripReason: "",
		TripAt: time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC), GatewayURL: "http://127.0.0.1:8789",
		LastProbeAt: time.Date(2026, 11, 5, 1, 3, 0, 0, time.UTC), LastProbeOK: true,
		LastLatencyMs: 42, LastReportAt: time.Date(2026, 11, 5, 1, 4, 0, 0, time.UTC),
		LastReportKind: "heartbeat", Cash: 1000.5, CashStale: false, CashAt: "2026-11-05 09:30:00",
		StuckCancelN: 2, StuckCancelOldestSec: 300, ExternalBrain: 1, ExternalBrainReason: "ok",
	}
	rawSnap, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var snapMap map[string]any
	if err := json.Unmarshal(rawSnap, &snapMap); err != nil {
		t.Fatal(err)
	}
	wrapped, err := json.Marshal(qmtStateWithLeg{
		StateSnapshot: snap,
		LegStatus:     &legStatus{StrategyVersion: "v02-20261104", SeenAt: "2026-11-05T09:31:00+08:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wrapMap map[string]any
	if err := json.Unmarshal(wrapped, &wrapMap); err != nil {
		t.Fatal(err)
	}
	// 超集判定一：StateSnapshot 反射出的每个 tag 都原样在包装体里，且值逐字段一致。
	tp := reflect.TypeOf(trading.StateSnapshot{})
	for i := 0; i < tp.NumField(); i++ {
		name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("StateSnapshot 字段 %s 没有可用的 json tag", tp.Field(i).Name)
		}
		if _, ok := wrapMap[name]; !ok {
			t.Errorf("既有字段 %q 在 /api/qmt/state 包装后消失了（superset 锁）", name)
		}
		a, b := snapMap[name], wrapMap[name]
		if jsonEq(a) != jsonEq(b) {
			t.Errorf("字段 %q 的值被包装层改写了: %v vs %v", name, a, b)
		}
	}
	// 超集判定二：多出来的键只许是 leg_status（不许顺手塞别的）。
	for k := range wrapMap {
		if _, ok := snapMap[k]; !ok && k != "leg_status" {
			t.Errorf("state 响应多出了未登记的键 %q（本批只承诺加 leg_status 一枚）", k)
		}
	}
	leg, ok := wrapMap["leg_status"].(map[string]any)
	if !ok || leg["strategy_version"] != "v02-20261104" {
		t.Fatalf("leg_status 形状不对: %v", wrapMap["leg_status"])
	}
}

// jsonEq 把任意值规范成 JSON 串做深比较（time 串/数字经两次 marshal 后的稳定形态）。
func jsonEq(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestUplinkEmitSetWithinEnvelope 行为腿 ④：golden 的 report_uplink_emitted_fields
// 每个键都必须在 qmtReportEvent 反射集里——上行腿加键不加 tag 即红（§M4 同锁的上行镜像）。
func TestUplinkEmitSetWithinEnvelope(t *testing.T) {
	raw, err := os.ReadFile("../../qmt_gateway/contract/report_fields.json")
	if err != nil {
		t.Fatalf("读取 golden 失败: %v", err)
	}
	var doc struct {
		ReportEventFields []string            `json:"report_event_fields"`
		UplinkEmitted     map[string][]string `json:"report_uplink_emitted_fields"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	env := map[string]bool{}
	for _, f := range reportJSONFields(t, qmtReportEvent{}) {
		env[f] = true
	}
	if !env["strategy_version"] {
		t.Fatal("信封里没有 strategy_version——上行腿的版本读数会被 encoding/json 静默丢弃")
	}
	for evType, keys := range doc.UplinkEmitted {
		for _, k := range keys {
			if !env[k] {
				t.Errorf("report_uplink 在 %s 事件发出字段 %q，但 qmtReportEvent 没有该 tag", evType, k)
			}
		}
	}
}

// TestReportEnvelopeDecodesStrategyVersion 信封解码腿：wire 上的 strategy_version 必须
// 真的进结构体（omitempty 只影响编码方向；解码侧不认 tag 才是这条链断掉的形态）。
func TestReportEnvelopeDecodesStrategyVersion(t *testing.T) {
	var ev qmtReportEvent
	if err := json.Unmarshal([]byte(`{"type":"heartbeat","user_id":"u1",`+
		`"strategy_version":"v02-20261104","reason":"executor_version=v01-20261103"}`), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.StrategyVersion != "v02-20261104" || ev.Reason == "" {
		t.Fatalf("strategy_version/reason 没接住: %+v", ev)
	}
}
