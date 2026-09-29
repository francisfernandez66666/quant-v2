// Command bigfuncs 统计「函数体行数 ≥ 阈值」的顶层函数，输出可复读的榜单与单行读数。
//
// 为什么要用 go/ast 而不是按花括号配平去数行（§0927AUDIT 复核批 09-29 的实录教训）：
// 本仓上一轮审计报告留了一句「≥120 行函数实测 59 个」，09-29 换四种口径复跑都对不上
// （66／57／77／80／89），说明**统计类结论没有钉死口径时，下一轮只能把旧数字当事实抄**。
// 先用最直白的花括号计数复现时又撞出新问题：函数体里的字符串字面量（如 JSON 样例、
// 格式化文案）自带不成对的花括号，naive 计数会把该函数判成"未配平"并按文件尾计，
// 实测把 internal/llm/probe.go 的 llmBodyShape 虚报成 311 行。⇒ 口径必须来自编译器，
// 而不是来自对源码文本的猜测：go/ast 的 FuncDecl.Pos()/End() 天然跳过字符串、注释、
// 原始串里的任何括号，行距即真实函数体跨度。
//
// 口径定义（唯一实现，勿在别处另起炉灶）：
//   - 长度 = 从 `func` 声明行到该函数闭括号行的**行数**（含首尾两行）；
//     doc 注释不计入（ast 的 Pos() 指向 func 关键字，注释是独立字段）；
//   - 只统计顶层 func/方法（FuncDecl），函数值、闭包、init 之外的表达式不单独计；
//   - 默认作用域 internal + cmd（与门禁 §104 的 gofmt 作用域同口径），排除 _test.go；
//   - 解析失败的文件**立即报错退出**——静默跳过会让计数偏低且无人怀疑（本仓 §DEADGAUGE 族）。
//
// 用法（推荐经 scripts/audit_big_funcs.sh 调用，它负责仓库根定位与作用域存在性校验）：
//
//	go run ./tools/bigfuncs --min 120 --top 10
//	go run ./tools/bigfuncs --baseline --include-tests
//	go run ./tools/bigfuncs --only internal --min 80
//	go run ./tools/bigfuncs --root /tmp/pre0928 --baseline   # 对历史检出树复跑同一口径
//
// English: counts top-level Go functions whose body spans at least --min lines,
// measured through go/ast (so braces inside string literals or comments cannot
// distort the span). Fixed scope internal+cmd, test files excluded by default,
// and an unparseable file aborts instead of silently lowering the count.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// entry 一个命中阈值的函数：行数、相对路径、函数名。
// English: one function that crossed the threshold.
type entry struct {
	lines    int
	path     string
	name     string
	kind     string // func / method（带接收者）——榜单里区分"长自由函数"与"长方法"用
	testFile bool   // 仅 --include-tests 时可能出现；榜单标注来源，避免把测试长度当工程债
}

func main() {
	var (
		minLines   = flag.Int("min", 120, "函数体行数阈值")
		top        = flag.Int("top", 10, "榜单长度")
		include    = flag.Bool("include-tests", false, "把 _test.go 一并计入（默认排除）")
		baseline   = flag.Bool("baseline", false, "只输出一行机器可读读数（门禁/文档留痕用）")
		scopesFlag = flag.String("only", "internal,cmd", "逗号分隔的作用域目录（相对 --root）")
		rootFlag   = flag.String("root", ".", "统计根目录（默认当前目录；复核历史树时指到那份检出目录）")
	)
	flag.Parse()

	// 仓库根＝--root（默认当前目录；外层脚本会先 cd 到仓库根）。不在这里猜路径：
	// 猜错的后果是"扫了个空目录、报 0 个巨函数"这种看起来像好消息的红。
	// 支持 --root 的另一个理由是复核场景本身：要回答"上一轮审计那个数当时到底是多少"，
	// 必须能对**历史检出树**跑同一套口径，而不是只能在当前树重跑。
	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		fmt.Printf("FAIL --root 解析失败（%s）: %v\n", *rootFlag, err)
		os.Exit(1)
	}
	if st, serr := os.Stat(root); serr != nil || !st.IsDir() {
		fmt.Printf("FAIL --root 不是可进入的目录: %s\n", root)
		os.Exit(1)
	}

	scopes := strings.Split(*scopesFlag, ",")
	var entries []entry
	fset := token.NewFileSet()
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		base := filepath.Join(root, scope)
		if st, serr := os.Stat(base); serr != nil || !st.IsDir() {
			// 作用域不存在必须硬停：按"没有巨函数"收口是最坏结局。
			fmt.Printf("FAIL 作用域目录不存在: %s（拒绝按 0 计数收口）\n", scope)
			os.Exit(1)
		}
		walkErr := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// 与门禁一致的排除项：构建产物与依赖目录不参与统计。
				switch d.Name() {
				case ".git", "node_modules", "dist":
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") {
				return nil
			}
			isTest := strings.HasSuffix(p, "_test.go")
			if isTest && !*include {
				return nil
			}
			file, perr := parser.ParseFile(fset, p, nil, parser.ParseComments)
			if perr != nil {
				// 解析失败＝本工具口径无法成立，硬停并指名文件（不静默跳，见文件头）。
				return fmt.Errorf("解析 %s 失败: %w", p, perr)
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				rel = p
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue // 前向声明等无函数体的形态不计
				}
				start := fset.Position(fn.Pos()).Line
				end := fset.Position(fn.End()).Line
				n := end - start + 1
				if n < *minLines {
					continue
				}
				kind := "func"
				name := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					kind = "method"
				}
				entries = append(entries, entry{lines: n, path: rel, name: name, kind: kind, testFile: isTest})
			}
			return nil
		})
		if walkErr != nil {
			fmt.Printf("FAIL %v\n", walkErr)
			os.Exit(1)
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].lines != entries[j].lines {
			return entries[i].lines > entries[j].lines
		}
		return entries[i].path < entries[j].path
	})

	maxLines := 0
	if len(entries) > 0 {
		maxLines = entries[0].lines
	}
	if *baseline {
		// 单行读数：供门禁段注释与文档引用，杜绝"抄上一个数字"的漂移。
		// root 一并进读数——同一口径对历史树与当前树跑出来的数必须能分清是哪棵树。
		fmt.Printf("BIG_FUNCS min=%d scope=%s tests=%s count=%d max=%d root=%s\n",
			*minLines, *scopesFlag, yn(*include), len(entries), maxLines, root)
		return
	}

	fmt.Printf("==> 巨函数统计（口径：go/ast 函数体行数 >= %d 行；作用域 %s；%s；root=%s）\n",
		*minLines, *scopesFlag, scopeNote(*include), root)
	fmt.Printf("    命中 %d 个；最长 %d 行\n", len(entries), maxLines)
	if len(entries) == 0 {
		return
	}
	n := *top
	if n > len(entries) {
		n = len(entries)
	}
	fmt.Printf("    榜单 Top %d：\n", n)
	for _, e := range entries[:n] {
		extra := ""
		if e.testFile {
			extra = " [_test.go]"
		}
		fmt.Printf("      %5d 行  %s::%s (%s)%s\n", e.lines, e.path, e.name, e.kind, extra)
	}
}

// yn 布尔转 in/out（基线读数里用，便于 grep）。
// English: boolean to in/out token for the baseline line.
func yn(v bool) string {
	if v {
		return "in"
	}
	return "out"
}

// scopeNote 人类可读的测试文件口径说明。
// English: human-readable note about test-file inclusion.
func scopeNote(include bool) string {
	if include {
		return "含 _test.go"
	}
	return "排除 _test.go"
}
