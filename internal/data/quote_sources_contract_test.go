// 文件：quote_sources_contract_test.go
// 职责：§M1/F4「行情源名契约单源化」的反例锁（Go 侧）。
//
//	① AST 扫描 internal/data 全部生产源文件（排除 *_test.go）：
//	   a) 每个 `X.setLastSource(<arg>)` 实参必须是 **QuoteSource* 常量**（禁裸字面量/裸变量）；
//	   b) 快照变量（*MarketSnapshot 类型形参 / `:= &MarketSnapshot{}` / `= <expr>.Snapshot()`
//	      推断出的变量）对 `.Source` 的赋值右值必须是 QuoteSource* 常量——
//	      MarketSnapshot.Source 即 /api/status quote_source 的实际值，堵死两端漂移源头；
//	   c) 每个 QuoteSource* 常量必须在生产代码中至少被引用一次（防枚举虚增/悬空项）。
//	② Go 枚举（AST 常量表 + 运行时 AllQuoteSources()）必须与 golden 文件
//	   qmt_gateway/contract/quote_sources.json（JSON 字符串数组，UTF-8）逐一相等（双向锁），
//	   golden 供 web/e2e 白名单同源读取。
//	空串 "" 语义（盘外/快照未就绪）不属于枚举、不进 golden——消费端按「未知/盘外」处理。
//
// 再生产方式：go test ./internal/data -run TestQuoteSourcesGolden -update
package data

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// updateQuoteGolden -update：按 Go 枚举重写 golden 文件（新增/更名行情源后的再生产步骤）。
var updateQuoteGolden = flag.Bool("update", false, "regenerate qmt_gateway/contract/quote_sources.json from AllQuoteSources()")

// goldenQuoteSourcesPath 相对本测试文件的 golden 路径（与 order_fields.json 三方锁手法同源）。
const goldenQuoteSourcesPath = "../../qmt_gateway/contract/quote_sources.json"

// quoteSourceScan AST 扫描产物。
type quoteSourceScan struct {
	setLastSourceArgs []string          // setLastSource 实参解析出的源名
	snapshotSourceSet []string          // 快照变量 .Source 赋值解析出的源名
	constValues       map[string]string // QuoteSource* 常量 name→value（AST 侧枚举）
	constRefs         map[string]int    // QuoteSource* 常量引用次数（含定义行自身 1 次）
	violations        []string          // 结构违规项
}

// collectSnapshotVars 在一个文件内推断 *MarketSnapshot 类型的变量名集合：
//   - 形参/变量声明类型写作 MarketSnapshot / *MarketSnapshot；
//   - `v := &MarketSnapshot{...}`（可带转换括号）；
//   - `v := <任意>.Snapshot()`（Fetcher.Snapshot 返回值）。
//
// 目的：只锁真正的快照写入点，不误伤 OrderBook/NewsItem 等共用 .Source 字段的结构。
func collectSnapshotVars(f *ast.File) map[string]bool {
	vars := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Field: // 函数形参 / 结构体字段声明（后者同名无害，结构体字段不会被 rid.Source 引用）
			if isSnapshotType(node.Type) {
				for _, nm := range node.Names {
					vars[nm.Name] = true
				}
			}
		case *ast.DeclStmt:
			if gd, ok := node.Decl.(*ast.GenDecl); ok {
				for _, spec := range gd.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok && isSnapshotType(vs.Type) {
						for _, nm := range vs.Names {
							vars[nm.Name] = true
						}
					}
				}
			}
		case *ast.AssignStmt:
			if len(node.Rhs) != 1 {
				return true
			}
			if !isSnapshotExpr(node.Rhs[0]) {
				return true
			}
			for _, lhs := range node.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					vars[id.Name] = true
				}
			}
		}
		return true
	})
	return vars
}

// isSnapshotType 判断类型表达式是否为 MarketSnapshot / *MarketSnapshot。
func isSnapshotType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return isSnapshotType(t.X)
	case *ast.Ident:
		return t.Name == "MarketSnapshot"
	}
	return false
}

// isSnapshotExpr 判断初始化右值是否为快照构造/快照读取调用。
func isSnapshotExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.UnaryExpr: // &MarketSnapshot{...}
		if e.Op == token.AND {
			return isSnapshotType(e.X)
		}
	case *ast.CallExpr: // x.Snapshot()（Fetcher.Snapshot 返回 *MarketSnapshot 拷贝）
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Snapshot" && len(e.Args) == 0 {
			return true
		}
	}
	return false
}

// scanQuoteSources 解析 data 包生产源文件并执行结构约束（①a/b/c）。
func scanQuoteSources(t *testing.T) quoteSourceScan {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读目录失败: %v", err)
	}
	res := quoteSourceScan{constValues: map[string]string{}, constRefs: map[string]int{}}
	var files []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue // 测试文件（含本文件）不参与契约扫描
		}
		af, perr := parser.ParseFile(fset, e.Name(), nil, 0)
		if perr != nil {
			t.Fatalf("解析 %s 失败: %v", e.Name(), perr)
		}
		files = append(files, af)
	}
	if len(files) == 0 {
		t.Fatal("§M1 AST 扫描未解析到任何生产源文件——扫描器失效，须排查")
	}

	// 第一遍：收集 QuoteSource* 包级字符串常量表（AST 侧枚举）。
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != len(vs.Names) {
					continue
				}
				for i, n := range vs.Names {
					if !strings.HasPrefix(n.Name, "QuoteSource") {
						continue
					}
					if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if v, uerr := strconv.Unquote(bl.Value); uerr == nil {
							res.constValues[n.Name] = v
						}
					}
				}
			}
		}
	}
	if len(res.constValues) == 0 {
		t.Fatal("§M1 AST 未找到 QuoteSource* 常量——枚举被删除或改名，契约失效")
	}

	// resolveString 解析实参：字面量 → (值, isLit)；QuoteSource* 常量 Ident → (值, isConst)。
	resolveString := func(expr ast.Expr) (string, bool, bool) {
		switch ex := expr.(type) {
		case *ast.BasicLit:
			if ex.Kind == token.STRING {
				if v, uerr := strconv.Unquote(ex.Value); uerr == nil {
					return v, true, false
				}
			}
		case *ast.Ident:
			if v, ok := res.constValues[ex.Name]; ok {
				return v, false, true
			}
		}
		return "", false, false
	}

	// 第二遍：遍历写入点与常量引用。
	for _, f := range files {
		snapVars := collectSnapshotVars(f)
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			// a) dc.setLastSource(arg) —— 降级链命中源唯一写入口
			case *ast.CallExpr:
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "setLastSource" || len(node.Args) != 1 {
					return true
				}
				v, isLit, isConst := resolveString(node.Args[0])
				switch {
				case isLit:
					res.violations = append(res.violations,
						"setLastSource("+strconv.Quote(v)+") 使用裸字符串字面量（须改引 QuoteSource* 常量）")
				case !isConst:
					res.violations = append(res.violations,
						"setLastSource 实参不是字面量/QuoteSource* 常量（裸变量传值逃过契约）")
				default:
					res.setLastSourceArgs = append(res.setLastSourceArgs, v)
				}
			// b) 快照变量 .Source 赋值 —— 右值必须是 QuoteSource* 常量。
			//    枚举外新源名（如 "foo"）由 golden/E2E 侧互补覆盖：golden 不含 → 巡检红。
			case *ast.AssignStmt:
				if len(node.Lhs) != 1 || len(node.Rhs) != 1 {
					return true
				}
				ls, ok := node.Lhs[0].(*ast.SelectorExpr)
				if !ok || ls.Sel.Name != "Source" {
					return true
				}
				rid, ok := ls.X.(*ast.Ident)
				if !ok || !snapVars[rid.Name] {
					return true
				}
				v, isLit, isConst := resolveString(node.Rhs[0])
				switch {
				case isLit:
					res.violations = append(res.violations,
						rid.Name+".Source 赋值使用裸字符串字面量 "+strconv.Quote(v)+"（须改引 QuoteSource* 常量）")
				case !isConst:
					res.violations = append(res.violations,
						rid.Name+".Source 赋值右值不是 QuoteSource* 常量（快照源名绕开契约）")
				default:
					res.snapshotSourceSet = append(res.snapshotSourceSet, v)
				}
			// c) 常量引用计数（定义行自身记 1，使用处再记）。
			case *ast.Ident:
				if _, ok := res.constValues[node.Name]; ok {
					res.constRefs[node.Name]++
				}
			}
			return true
		})
	}
	if len(res.violations) > 0 {
		t.Fatalf("§M1 契约违规写入点:\n  %v", res.violations)
	}
	if len(res.setLastSourceArgs) == 0 {
		t.Fatal("§M1 AST 扫描未找到任何 setLastSource 写入点——扫描器失效或被绕过，须排查")
	}
	return res
}

// TestQuoteSourceSitesMatchEnum 锁①：写入点/常量表/运行时枚举三者双向一致。
// 反例语义（原缺陷形态复现）：
//   - 有人新写 dc.setLastSource("foo") 裸字面量 → scan 直接违规失败；
//   - 有人新写 snapshot.Source = "bar" 裸字面量 → 违规失败；
//   - 有人往枚举加了常量却没接线（或 golden 没更） → 未引用/锁②失败；
//   - 有人把写入点从常量改回字符串（如恢复中文硬编码） → 违规 + 枚举虚增双向失败。
func TestQuoteSourceSitesMatchEnum(t *testing.T) {
	scan := scanQuoteSources(t)

	// 运行时枚举与 AST 常量表双向一致（AllQuoteSources 漏项/多项即红）。
	enum := map[string]bool{}
	for _, s := range AllQuoteSources() {
		if enum[s] {
			t.Fatalf("AllQuoteSources 枚举重复项: %q", s)
		}
		enum[s] = true
	}
	astVals := map[string]bool{}
	for name, v := range scan.constValues {
		if !enum[v] {
			t.Fatalf("§M1 漂移：常量 %s=%q 未在 AllQuoteSources() 登记", name, v)
		}
		if astVals[v] {
			t.Fatalf("§M1 漂移：常量表出现重复取值 %q", v)
		}
		astVals[v] = true
	}
	for v := range enum {
		if !astVals[v] {
			t.Fatalf("§M1 漂移：枚举项 %q 在 source.go 无对应 QuoteSource* 常量", v)
		}
	}

	// 每个常量至少被生产代码引用一次（定义行自身计数 1，需 >1）。
	for name := range scan.constValues {
		if scan.constRefs[name] <= 1 {
			t.Fatalf("§M1 悬空枚举：常量 %s 在生产代码中无任何写入点引用", name)
		}
	}

	// 双向锁：{setLastSource ∪ 快照 .Source 写入点} 值集 == 枚举集。
	sites := map[string]bool{}
	for _, v := range scan.setLastSourceArgs {
		sites[v] = true
	}
	for _, v := range scan.snapshotSourceSet {
		sites[v] = true
	}
	for v := range sites {
		if !enum[v] {
			t.Fatalf("§M1 漂移：写入点取值 %q 不在枚举内", v)
		}
	}
	for v := range enum {
		if !sites[v] {
			t.Fatalf("§M1 漂移：枚举项 %q 在生产代码无任何写入点", v)
		}
	}
}

// TestQuoteSourcesGolden 锁②：Go 枚举 == golden 文件（双向）。golden 是 web/e2e
// 白名单（代理 K 接线）的读取源——任一侧漂移（历史形态：后端吐小写英文、用例收中文）即红。
// 再生产：go test ./internal/data -run TestQuoteSourcesGolden -update
func TestQuoteSourcesGolden(t *testing.T) {
	want := append([]string(nil), AllQuoteSources()...)
	sort.Strings(want)

	path := filepath.FromSlash(goldenQuoteSourcesPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 golden 失败（%s）: %v——可用 -update 再生产", path, err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("golden 不是合法 JSON 字符串数组: %v", err)
	}
	sort.Strings(got)

	if *updateQuoteGolden {
		out, _ := json.Marshal(want)
		if werr := os.WriteFile(path, append(out, '\n'), 0644); werr != nil {
			t.Fatalf("写 golden 失败: %v", werr)
		}
		t.Logf("golden 已按枚举再生产: %v", want)
		return
	}

	gotSet := map[string]bool{}
	for _, g := range got {
		if g == "" {
			t.Fatal("§M1 golden 混入空串：空串=盘外/未就绪语义，禁止进枚举")
		}
		gotSet[g] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Fatalf("§M1 golden 漂移：枚举项 %q 未进 golden（改码后跑 -update 再生产）", w)
		}
	}
	for _, g := range got {
		found := false
		for _, w := range want {
			if w == g {
				found = true
			}
		}
		if !found {
			t.Fatalf("§M1 golden 漂移：golden 含枚举外项 %q（生产代码已无此取值，须 -update 或恢复写入点）", g)
		}
	}
}
