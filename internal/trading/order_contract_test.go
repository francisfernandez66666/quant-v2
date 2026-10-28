package trading

// §A2（AUDIT_FULLSTACK_20260918）下单契约三点一致性测试。
// 背景：Go OrderRequest 携带的 strategy_type/staleness_ms 等 5 个风控字段曾被真实网关
// 静默忽略（gateway.py 只解析 10 基础字段），字段集漂移只能靠人肉注释发现。本测试把
// qmt_gateway/contract/order_fields.json 钉为 golden：Go 结构体反射集、网关 consumed∪ignored
// 集、qmt-mock 源码 json tag 集三方必须闭合，新增/删除字段任一侧漏改即红。
// English: §A2 three-way order-contract lock — reflect OrderRequest, read the gateway's
// golden contract, and scan qmt-mock source tags; any field added on one side without the
// others turns this test red (the exact drift that motivated AUDIT_FULLSTACK_20260918 A1).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestOrderContractGolden 校验 OrderRequest 反射字段集 == golden.fields，且 golden
// consumed∪ignored == fields、mock 源码声明全部 consumed 字段。
// 重新生成 golden：ORDER_CONTRACT_UPDATE=1 go test ./internal/trading -run TestOrderContractGolden
func TestOrderContractGolden(t *testing.T) {
	goldenPath := filepath.Join("..", "..", "qmt_gateway", "contract", "order_fields.json")
	goFields := orderJSONFields(t, OrderRequest{})

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("读取 golden 失败（网关契约文件缺失？）: %v", err)
	}
	var doc struct {
		Fields            []string          `json:"fields"`
		Types             map[string]string `json:"types"`
		ConsumedByGateway []string          `json:"consumed_by_gateway"`
		IgnoredByGateway  []string          `json:"ignored_by_gateway"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("golden JSON 解析失败: %v", err)
	}

	// 显式重生成模式（ORDER_CONTRACT_UPDATE=1）：用 Go 反射出来的字段集覆写 golden.fields，
	// 网关的 consumed/ignored 两面原样保留——这两个集合要人工对着 gateway.py 复核，
	// 不能被脚本顺手"改正"。写完即 Skip，提示先复核再提交，避免静默改掉契约基线。
	if os.Getenv("ORDER_CONTRACT_UPDATE") == "1" {
		out, merr := json.MarshalIndent(map[string]any{
			"source": "internal/trading/executor.go OrderRequest", "fields": goFields,
			"types":               orderJSONTypes(t, OrderRequest{}),
			"consumed_by_gateway": doc.ConsumedByGateway, "ignored_by_gateway": doc.IgnoredByGateway,
		}, "", "  ")
		if merr != nil {
			t.Fatal(merr)
		}
		if werr := os.WriteFile(goldenPath, append(out, '\n'), 0o644); werr != nil {
			t.Fatal(werr)
		}
		t.Skip("golden fields 已按 Go 结构体重新生成，请复核后提交")
	}

	assertSameFieldSet(t, "OrderRequest(反射) vs golden.fields", goFields, doc.Fields)

	// §FIX-20261028-F16（2026-10-29）：契约锁补"类型/指针性"。
	// 旧锁只比字段名集合——把 price 从 float64 改成 string、或把 qty 从 int 改成 *int，
	// 三向校验照样全绿，而网关会静默解析成 0（本仓"字段在≠语义对"的同族）。
	// 现在 golden.types 与 Go 反射类型逐字段等值：改类型就必须同步 golden，否则这里红。
	if len(doc.Types) == 0 {
		t.Fatalf("golden.types 缺失：契约类型锁未启用（用 ORDER_CONTRACT_UPDATE=1 重新生成后再提交）")
	}
	goTypes := orderJSONTypes(t, OrderRequest{})
	assertSameTypeMap(t, goTypes, doc.Types)
	assertSameFieldSet(t, "golden.fields vs consumed∪ignored",
		doc.Fields, append(append([]string{}, doc.ConsumedByGateway...), doc.IgnoredByGateway...))

	// qmt-mock 消费面扫描：网关 consumed 的每个字段都必须出现在 mock 的 /order 解析结构 tag 中，
	// 防止 e2e 假柜台与实网关字段面再度分叉（§A2 三点之 mock 点）。
	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "qmt-mock", "main.go"))
	if err != nil {
		t.Fatalf("读取 qmt-mock 源码失败: %v", err)
	}
	for _, f := range doc.ConsumedByGateway {
		if !strings.Contains(string(src), `json:"`+f+`"`) {
			t.Errorf("qmt-mock /order 未声明契约消费字段 %q——mock 与网关消费面漂移", f)
		}
	}
}

// orderJSONFields 反射提取结构体全部 json tag 字段名（去 omitempty）并排序。
func orderJSONFields(t *testing.T, v any) []string {
	t.Helper()
	tp := reflect.TypeOf(v)
	out := make([]string, 0, tp.NumField())
	for i := 0; i < tp.NumField(); i++ {
		tag := tp.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("字段 %s 缺 json tag，无法纳入契约", tp.Field(i).Name)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// assertSameFieldSet 以多重集（排序后逐项）比较两组字段名，差异全部打印。
func assertSameFieldSet(t *testing.T, what string, a, b []string) {
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

// orderJSONTypes 反射提取 "json 字段名 → 类型串"（指针记 ptr:，slice 记 slice:）。
//
// §FIX-20261028-F16：类型串只用于"契约两侧是否同型"的等值比较，不做跨语言类型推导——
// Go 的 int/int64、float64 与 Python 的 int/float 本来就不同名，网关侧只校验"键集一致 + 值在允许集合"。
func orderJSONTypes(t *testing.T, v any) map[string]string {
	t.Helper()
	tp := reflect.TypeOf(v)
	out := make(map[string]string, tp.NumField())
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		name := f.Tag.Get("json")
		if name == "" || name == "-" {
			continue
		}
		if idx := strings.IndexByte(name, ','); idx >= 0 {
			name = name[:idx]
		}
		out[name] = goTypeString(f.Type)
	}
	return out
}

// goTypeString 把 reflect.Type 归成稳定的类型串（指针/切片加前缀，其余用 Kind 名）。
func goTypeString(ft reflect.Type) string {
	switch ft.Kind() {
	case reflect.Pointer:
		return "ptr:" + goTypeString(ft.Elem())
	case reflect.Slice:
		return "slice:" + goTypeString(ft.Elem())
	default:
		return ft.Kind().String()
	}
}

// assertSameTypeMap 逐字段比类型串，缺一/多一/不同型都算红。
func assertSameTypeMap(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("golden.types 多出字段 %q（Go 结构体已无此字段）", k)
			continue
		}
		if gv != wv {
			t.Errorf("字段 %q 类型漂移：Go=%s golden=%s（改类型必须同步 golden 并通知网关）", k, gv, wv)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("golden.types 缺字段 %q（Go 结构体新增字段必须在 golden 里登记类型）", k)
		}
	}
}
