// arch_test.go — §SIZING-EVO 回路独立铁律的架构测试（§2.7.0，锁定防串味）。
//
// owner 原话：「仓位+因子加权另外做一套自动研究，不要和现有的战法回测串起来」。
// 两道锁：
//
//	① 本包（含 _test.go）零 import quant-trading-v2/internal/research；
//	② 本包源码不出现战法生命周期库的引用（applied_rules / grayscale.json 等文件名）。
//
// 另加一道衍生锁：本包唯一允许 import 的"研究邻域"是 signalctl（ComputeSizing 是被优化
// 对象本身）——若 signalctl 哪天 import 了 research，本回路就通过它被串味，一并锁住。
//
// 自排除：本文件自身的禁串清单会含 "internal/research" 字面量，文本扫描跳过自己
// （import 扫描仍覆盖自己——用 go/parser 的结构化 import 列表，不吃字符串）。
//
// English: architecture locks — zero import of internal/research, no tactic-library file
// references, and the signalctl bridge stays research-free too.
package sizing_evo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const researchPkg = "quant-trading-v2/internal/research"

// forbiddenLiterals 战法生命周期库文件名/键（本包不得以任何形式引用）。
// English: tactic-lifecycle library file names/keys this package must never reference.
var forbiddenLiterals = []string{
	"internal/research",
	"applied_rules",
	"grayscale.json",
	"research.DB",
}

// goFilesIn 列出目录下全部 .go 文件（含测试文件）。
func goFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// TestSizingEvoNoResearchImport 锁①：零 import internal/research（结构化 import 扫描）。
// English: lock 1 — no import of internal/research (structural import scan).
func TestSizingEvoNoResearchImport(t *testing.T) {
	fset := token.NewFileSet()
	for _, path := range goFilesIn(t, ".") {
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == researchPkg {
				t.Errorf("%s imports %s —— 回路独立铁律：sizing_evo 零 import internal/research（§2.7.0）", path, p)
			}
		}
	}
}

// TestSizingEvoNoTacticLibraryRefs 锁②：**代码**（字符串字面量，不含注释）不出现战法库
// 引用。注释里写明"不读 applied_rules"是纪律文档，不算引用——故用 AST 扫字符串字面量，
// 而不是裸文本扫描（注释不进 AST：ParseFile 未传 ParseComments）。
// English: lock 2 — no tactic-library references in code (string literals; comments excluded).
func TestSizingEvoNoTacticLibraryRefs(t *testing.T) {
	fset := token.NewFileSet()
	for _, path := range goFilesIn(t, ".") {
		if filepath.Base(path) == "arch_test.go" {
			continue // 本文件自带禁串清单，跳过自身
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, bad := range forbiddenLiterals {
				if strings.Contains(lit.Value, bad) {
					t.Errorf("%s string literal contains %q —— 战法生命周期库不读不引（§2.7.0 铁律 2）", path, bad)
				}
			}
			return true
		})
	}
}

// TestSignalctlBridgeStaysResearchFree 衍生锁：signalctl（本回路唯一研究邻域依赖）
// 自身也不得 import internal/research——否则耦合通过它渗进来。
// English: the signalctl bridge must itself stay research-free.
func TestSignalctlBridgeStaysResearchFree(t *testing.T) {
	sigctlDir := filepath.Join("..", "signalctl")
	ents, err := os.ReadDir(sigctlDir)
	if err != nil {
		t.Fatalf("readdir %s: %v", sigctlDir, err)
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(sigctlDir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); p == researchPkg {
				t.Errorf("signalctl/%s imports %s —— sizing_evo 依赖 signalctl，该桥不得串味", e.Name(), p)
			}
		}
	}
}
