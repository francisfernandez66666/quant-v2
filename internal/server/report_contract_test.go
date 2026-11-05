// report_contract_test.go — §F2（2026-09-22 修复批）回报方向 golden 契约回归测。
// 背景：下单方向有 order_fields.json 三方锁（order_contract_test.go），回报方向
// （positions/trade/order/account）此前无锚——网关 handler 与 Go qmtReportEvent 字段集
// 漂移只能靠人肉注释发现（ts_code 垃圾行直落 ReconcilePositionsForUser 即 F2 锤实形态）。
// 本测试把 qmt_gateway/contract/report_fields.json 钉为 golden：
//
//	① qmtReportEvent 反射 JSON tag 集 == report_event_fields；
//	② store.RealPosition 反射 JSON tag 集 == positions_row_fields；
//	②b §CARRIER-SHADOW（2026-11-05）store.CarrierShadowDecision 反射集 == shadow_row_fields，
//	   且 golden.shadow_row_emitted_fields ⊆ 反射集 —— 与 ⑥ 同一把尺子；反射面漂移＝"行发了、
//	   Go 没 tag、encoding/json 静默丢腿"。user_id/seen_at 两列**必须不在**发出集里
//	   （归属只认钥匙、到达时刻是 Go 本机时钟；行自报归属就是 §安全 T1 的反面）。
//	③ account_asset_keys 必须逐一在 qmt.go 中以 ev.Asset["key"] 形态被消费；
//	③b carrier_shadow 的信封键必须真被 Go 读（ev.Shadow）且接了落库腿（UpsertCarrierShadowRows）
//	   —— golden 写了事件类型、实现里没人接，那就是第二本假契约。
//	④ consumed_by_event 各事件消费集必须是信封字段集的子集（文档面自洽）；
//	⑤ §M4 双向锁——gateway_emitted_fields（网关实际发出集，由 qmt_gateway/tests/
//	   test_report_contract.py 在 Python 侧钉住）必须是信封集的子集：网关发了而 Go
//	   没有 tag = encoding/json 静默丢腿（实录：trade_id / name / created_at）；
//	⑥ positions_row_emitted_fields ⊆ RealPosition 反射集（对账行同锁）；
//	⑦ consumed_by_event ⊆ **两代发出集的并集**（handler.py 的 gateway_emitted_fields +
//	   report_uplink.py 的 report_uplink_emitted_fields）。§CARRIER-SHADOW 起这条改口径：
//	   carrier_shadow 这类**只有上行腿会发**，只看 handler 那代会把"真发的字段"判成假腿，
//	   逼着契约把 consumed 写成空集 —— 那才是真把断腿藏起来。Python 侧同名测试同尺子，
//	   并额外锁"handler 那代整类缺席时 consumed 必须落在 uplink 发出集里"，并集不是松口子。
//
// 任一侧加/删字段漏改 golden 即红。重生成信封/持仓行/影子行三面：
// REPORT_CONTRACT_UPDATE=1 go test ./internal/server -run TestReportContractGolden
// English: golden contract lock for the gateway-report direction, now bidirectional since §M4 —
// the emit side is pinned by the Python test, the receive side by reflection here, and
// emitted ⊆ envelope is asserted so an untagged (silently dropped) leg can never come back.
package server

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// TestReportContractGolden 回报信封/持仓行 golden 三点回归（见文件头 §F2 说明）。
func TestReportContractGolden(t *testing.T) {
	goldenPath := "../../qmt_gateway/contract/report_fields.json"
	envFields := reportJSONFields(t, qmtReportEvent{})
	posFields := reportJSONFields(t, store.RealPosition{})
	shadowFields := reportJSONFields(t, store.CarrierShadowDecision{})

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("读取回报 golden 失败: %v", err)
	}
	var doc struct {
		ReportEventFields    []string            `json:"report_event_fields"`
		PositionsRowFields   []string            `json:"positions_row_fields"`
		PositionsRowEmitted  []string            `json:"positions_row_emitted_fields"`
		ShadowRowFields      []string            `json:"shadow_row_fields"`
		ShadowRowEmitted     []string            `json:"shadow_row_emitted_fields"`
		AccountAssetKeys     []string            `json:"account_asset_keys"`
		ConsumedByEvent      map[string][]string `json:"consumed_by_event"`
		GatewayEmittedFields map[string][]string `json:"gateway_emitted_fields"`
		UplinkEmittedFields  map[string][]string `json:"report_uplink_emitted_fields"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("golden JSON 解析失败: %v", err)
	}

	// 显式重生成模式：只覆写三面反射集，asset keys / consumed_by_event / 两代发出集需人工复核。
	// §CARRIER-SHADOW 把 shadow_row_* 与 report_uplink_emitted_fields 一并列入覆写清单：
	// 少登记一枚，就是"重生成把别人那半契约静默删掉"（新增的事件类型最先在这里丢）。
	if os.Getenv("REPORT_CONTRACT_UPDATE") == "1" {
		out, merr := json.MarshalIndent(map[string]any{
			"source":                       docSourceLine(),
			"updated":                      docSourceUpdated(),
			"notes":                        docSourceNotes(raw),
			"report_event_fields":          envFields,
			"positions_row_fields":         posFields,
			"positions_row_emitted_fields": doc.PositionsRowEmitted,
			"shadow_row_fields":            shadowFields,
			"shadow_row_emitted_fields":    doc.ShadowRowEmitted,
			"account_asset_keys":           doc.AccountAssetKeys,
			"gateway_emitted_fields":       doc.GatewayEmittedFields,
			"report_uplink_emitted_fields": doc.UplinkEmittedFields,
			"consumed_by_event":            doc.ConsumedByEvent,
		}, "", "  ")
		if merr != nil {
			t.Fatal(merr)
		}
		if werr := os.WriteFile(goldenPath, append(out, '\n'), 0o644); werr != nil {
			t.Fatal(werr)
		}
		t.Skip("golden 已按 Go 结构体重新生成，请对照 gateway/handler.py 复核 consumed 面后提交")
	}

	assertSameStringSet(t, "qmtReportEvent(反射) vs golden.report_event_fields", envFields, doc.ReportEventFields)
	assertSameStringSet(t, "store.RealPosition(反射) vs golden.positions_row_fields", posFields, doc.PositionsRowFields)
	// ②b §CARRIER-SHADOW：影子行的 Go 反射面必须与 golden 逐字同集（发出面的子集判断在下面 ⑥b）。
	assertSameStringSet(t, "store.CarrierShadowDecision(反射) vs golden.shadow_row_fields",
		shadowFields, doc.ShadowRowFields)

	// ③ account 事件消费面：golden 声明的每个资产键都必须真实出现在 qmt.go 源码里
	// （map[string]float64 无类型可反射，源码串是最低成本的锚；键被改名/删读取点即红）。
	src, err := os.ReadFile("qmt.go")
	if err != nil {
		t.Fatalf("读取 qmt.go 源码失败: %v", err)
	}
	for _, k := range doc.AccountAssetKeys {
		if !strings.Contains(string(src), fmt.Sprintf(`ev.Asset[%q]`, k)) {
			t.Errorf("account 资产键 %q 未在 qmt.go 以 ev.Asset[%q] 消费——golden 与实现漂移", k, k)
		}
	}

	// ③b §CARRIER-SHADOW 同姿势的接线锁：golden 里写了 carrier_shadow 这个事件类型，
	// 实现一侧就必须真的**接**它——qmt.go 读 ev.Shadow、ingest 文件调 UpsertCarrierShadowRows、
	// 读面文件把 carrier_shadow 并进 /api/qmt/state。
	// 为什么这条要钉：golden 是**契约文档**，文档写了而实现没接＝上行腿一路 400/静默丢弃，
	// 而"信封里有这个 tag"这种断言看不出来（tag 在、case 不在，正是 §BUG 那族"看着都在"）。
	for _, want := range []struct{ file, anchor string }{
		{"qmt.go", "case \"carrier_shadow\":"},
		{"carrier_shadow_ingest.go", "ev.Shadow"},
		{"carrier_shadow_ingest.go", "UpsertCarrierShadowRows"},
		{"leg_status.go", "carrier_shadow"},
		{"carrier_shadow.go", "carrierShadowStateNoReport"},
		{"carrier_shadow.go", "carrierShadowStateNoHits"},
	} {
		body, rerr := os.ReadFile(want.file)
		if rerr != nil {
			t.Fatalf("读取 %s 失败: %v", want.file, rerr)
		}
		if !strings.Contains(string(body), want.anchor) {
			t.Errorf("§CARRIER-SHADOW 接线断裂：%s 里找不到 %q（golden 声明的事件类型没人接）", want.file, want.anchor)
		}
	}

	// ④ 各事件消费字段集必须是信封集子集（防止 golden 文档面写出实现里不存在的字段）。
	envSet := map[string]bool{}
	for _, f := range envFields {
		envSet[f] = true
	}
	for evType, consumed := range doc.ConsumedByEvent {
		for _, f := range consumed {
			if !envSet[f] {
				t.Errorf("consumed_by_event[%s] 引用了信封中不存在的字段 %q", evType, f)
			}
		}
	}

	// ⑤ §M4（2026-09-22 PM 批）双向丢腿锁：网关实际发出的每个字段都必须在 Go 信封里有 tag。
	// ①~④ 全部只反射 Go 一侧，所以"网关发了、Go 没接"永远绿——实录丢掉的三条腿是
	// trade_id（成交判重的精确锚）、name（建仓名称回填）、created_at（委托创建时间）。
	// encoding/json 对未声明字段静默丢弃，不报错、不留日志，只有这条跨侧断言能拦住它。
	// English: §M4 — every field the gateway emits must carry a JSON tag here, otherwise
	// encoding/json drops it silently (that is how trade_id/name/created_at were lost for ages).
	for evType, emitted := range doc.GatewayEmittedFields {
		for _, f := range emitted {
			if !envSet[f] {
				t.Errorf("§M4 丢腿：网关在 %s 事件发出字段 %q，但 qmtReportEvent 没有该 json tag（会被静默丢弃）", evType, f)
			}
		}
	}
	// ⑥ 对账行侧同锁：桥/xt 发出的持仓行字段必须能被 store.RealPosition 接住。
	posSet := map[string]bool{}
	for _, f := range posFields {
		posSet[f] = true
	}
	for _, f := range doc.PositionsRowEmitted {
		if !posSet[f] {
			t.Errorf("§M4 丢腿：对账行发出字段 %q 不在 store.RealPosition 反射集内", f)
		}
	}
	// ⑥b 用的影子行反射集（与 posSet 同一姿势，只是结构体换成 CarrierShadowDecision）。
	shadowSet := map[string]bool{}
	for _, f := range shadowFields {
		shadowSet[f] = true
	}
	// ⑦ 消费面必须真被发出（Go 声称消费的字段若两代发出侧谁都不发，消费的是恒零假腿）。
	// §CARRIER-SHADOW 起吃**两代发出集的并集**（handler.py + report_uplink.py），
	// 判据理由与 Python 侧 test_consumed_subset_of_emitted 逐字同源；handler 那代整类缺席时
	// 必须由 uplink 那代证实，所以并集不会把"两边各写一半"放过去。
	for evType, consumed := range doc.ConsumedByEvent {
		emitted := map[string]bool{}
		for _, f := range doc.GatewayEmittedFields[evType] {
			emitted[f] = true
		}
		handlersHasType := len(doc.GatewayEmittedFields[evType]) > 0
		for _, f := range doc.UplinkEmittedFields[evType] {
			emitted[f] = true
		}
		for _, f := range consumed {
			if len(emitted) > 0 && !emitted[f] {
				t.Errorf("§M4 假腿：consumed_by_event[%s] 消费了两代发出侧都未发出的字段 %q", evType, f)
			}
		}
		if !handlersHasType && len(doc.UplinkEmittedFields[evType]) == 0 {
			// handler 那代没有这一类、uplink 那代也没登记 ⇒ consumed 声明的是无源之水。
			t.Errorf("§CARRIER-SHADOW 假腿：事件类型 %s 在两代发出集里都不存在，consumed 却在声明字段", evType)
		}
	}

	// ⑥b §CARRIER-SHADOW：影子行侧同锁——上行腿实际发出的行字段必须都在反射集里；
	// 而 user_id / seen_at 这两列**必须不在**发出集里（归属只认钥匙、到达时刻是 Go 本机时钟）。
	// 后半条是 §安全 T1 的镜像：行一旦能自报归属，任何一根腿都能往别人账本里写"载体判定"。
	for _, f := range doc.ShadowRowEmitted {
		if !shadowSet[f] {
			t.Errorf("§CARRIER-SHADOW 丢腿：影子行发出字段 %q 不在 store.CarrierShadowDecision 反射集内", f)
		}
	}
	for _, own := range []string{"user_id", "seen_at"} {
		if shadowSet[own] && containsString(doc.ShadowRowEmitted, own) {
			t.Errorf("§安全 T1：影子行发出集里出现了 Go 侧自有列 %q（归属只能由钥匙决定）", own)
		}
	}
	// carrier_shadow 这一类必须由上行腿登记（handler.py 结构上发不出它）——
	// 只写 Go 消费面而不写发出面，就是"实现里读了一个契约上没人发的键"。
	if len(doc.UplinkEmittedFields["carrier_shadow"]) == 0 {
		t.Error("§CARRIER-SHADOW：golden.report_uplink_emitted_fields 没登记 carrier_shadow 的发出集")
	}
	if !containsString(doc.UplinkEmittedFields["carrier_shadow"], "shadow") {
		t.Error("§CARRIER-SHADOW：carrier_shadow 的发出集里没有 shadow 键，Go 侧消费的是恒零假腿")
	}
}

// containsString 小工具：golden 的字符串切片里是否出现某个值（golden 侧没有集合类型可用）。
func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// reportJSONFields 反射提取结构体全部 json tag 字段名（去 omitempty）并排序。
func reportJSONFields(t *testing.T, v any) []string {
	t.Helper()
	tp := reflect.TypeOf(v)
	out := make([]string, 0, tp.NumField())
	for i := 0; i < tp.NumField(); i++ {
		tag := tp.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("字段 %s 缺 json tag，无法纳入回报契约", tp.Field(i).Name)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// assertSameStringSet 排序后逐项比较两组字符串，差异全部打印。
func assertSameStringSet(t *testing.T, what string, a, b []string) {
	t.Helper()
	sa, sb := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(sa)
	sort.Strings(sb)
	missing, extra := []string{}, []string{}
	seen := map[string]int{}
	for _, s := range sb {
		seen[s]++
	}
	for _, s := range sa {
		if seen[s] == 0 {
			extra = append(extra, s)
		}
		seen[s]--
	}
	for s, c := range seen {
		if c > 0 {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s 字段集不一致: 仅前者有=%v 仅后者有=%v", what, extra, missing)
	}
}

// docSourceLine / docSourceUpdated / docSourceNotes 在重生成模式下原样保留 golden 元信息。
func docSourceLine() string {
	return "internal/server/qmt.go qmtReportEvent + internal/store/real_positions.go RealPosition"
}
func docSourceUpdated() string { return "2026-09-22" }
func docSourceNotes(raw []byte) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if s, ok := m["notes"].(string); ok {
		return s
	}
	return ""
}
