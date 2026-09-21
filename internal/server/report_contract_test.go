// report_contract_test.go — §F2（2026-09-22 修复批）回报方向 golden 契约回归测。
// 背景：下单方向有 order_fields.json 三方锁（order_contract_test.go），回报方向
// （positions/trade/order/account）此前无锚——网关 handler 与 Go qmtReportEvent 字段集
// 漂移只能靠人肉注释发现（ts_code 垃圾行直落 ReconcilePositionsForUser 即 F2 锤实形态）。
// 本测试把 qmt_gateway/contract/report_fields.json 钉为 golden：
//
//	① qmtReportEvent 反射 JSON tag 集 == report_event_fields；
//	② store.RealPosition 反射 JSON tag 集 == positions_row_fields；
//	③ account_asset_keys 必须逐一在 qmt.go 中以 ev.Asset["key"] 形态被消费；
//	④ consumed_by_event 各事件消费集必须是信封字段集的子集（文档面自洽）。
//
// 任一侧加/删字段漏改 golden 即红。重生成信封/持仓行两面：
// REPORT_CONTRACT_UPDATE=1 go test ./internal/server -run TestReportContractGolden
// English: golden contract lock for the gateway-report direction (mirror of the order-direction
// A2 test) — reflect qmtReportEvent and store.RealPosition against report_fields.json.
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

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("读取回报 golden 失败: %v", err)
	}
	var doc struct {
		ReportEventFields  []string            `json:"report_event_fields"`
		PositionsRowFields []string            `json:"positions_row_fields"`
		AccountAssetKeys   []string            `json:"account_asset_keys"`
		ConsumedByEvent    map[string][]string `json:"consumed_by_event"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("golden JSON 解析失败: %v", err)
	}

	// 显式重生成模式：只覆写两面反射集，asset keys / consumed_by_event 需人工对照 qmt.go 复核。
	if os.Getenv("REPORT_CONTRACT_UPDATE") == "1" {
		out, merr := json.MarshalIndent(map[string]any{
			"source":               docSourceLine(),
			"updated":              docSourceUpdated(),
			"notes":                docSourceNotes(raw),
			"report_event_fields":  envFields,
			"positions_row_fields": posFields,
			"account_asset_keys":   doc.AccountAssetKeys,
			"consumed_by_event":    doc.ConsumedByEvent,
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
