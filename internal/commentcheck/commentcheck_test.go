package commentcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot 定位仓库根目录（本包位于 <root>/internal/commentcheck）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(filepath.Dir(dir))
}

// TestChineseCommentCoverage 是全量中文注释约定的自动化验收：
// 对 internal/ 与 cmd/ 全部非测试 Go 源码扫描，任何未注释逻辑块都会导致失败。
// 修复方式：在该块前的最近逻辑起点补一条中文注释（或块内某行加入中文说明）。
func TestChineseCommentCoverage(t *testing.T) {
	root := repoRoot(t)
	opts := DefaultOptions()
	total := 0
	for _, sub := range []string{"internal", "cmd"} {
		gaps := ScanTree(filepath.Join(root, sub), opts)
		for _, g := range gaps {
			t.Errorf("未注释逻辑块 %s:%d（长度 %d 行）: %s", g.File, g.Line, g.Length, g.Head)
		}
		total += len(gaps)
	}
	if total > 0 {
		t.Fatalf("存在 %d 处未注释逻辑块，请补中文注释后重跑", total)
	}
}

// TestScannerBasics 扫描器自身的单元测试。
func TestScannerBasics(t *testing.T) {
	opts := DefaultOptions()

	// 用例一：块前写了中文注释、且块内有空行的样例源码，扫描结果必须为空——
	// 守住"注释打断连续块"这条口径，防止把已说明的段落误报成缺口。
	t.Run("comment line breaks run", func(t *testing.T) {
		src := strings.Join([]string{
			"func f() {",
			"\tif a {",
			"\t\tb()",
			"\t}",
			"}",
			"",
			"// 已注释块",
			"func g() {",
			"\tfor i := 0; i < 3; i++ {",
			"\t\th(i)",
			"\t}",
			"}",
		}, "\n")
		dir := t.TempDir()
		path := filepath.Join(dir, "x.go")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ScanFile(path, opts); len(got) != 0 {
			t.Fatalf("期望无缺口，实际 %d", len(got))
		}
	})

	// 用例二：现场拼一段 20 行无注释的 if 序列写进临时 y.go，
	// 阈值取默认 15 行时必须报出缺口，否则说明审计器漏判（漏判会让注释约定形同虚设）。
	t.Run("uncommented logic block is a gap", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("func f() {\n")
		for i := 0; i < 20; i++ {
			b.WriteString("\tif a { b() }\n")
		}
		b.WriteString("}\n")
		path := filepath.Join(t.TempDir(), "y.go")
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		gaps := ScanFile(path, Options{Threshold: 15, Lookback: 3, SkipImports: true})
		if len(gaps) == 0 {
			t.Fatal("期望检测到缺口")
		}
	})

	// 用例三：多行 import 块虽由代码行组成，但 SkipImports=true 时必须被豁免，
	// 否则每个文件的依赖声明都会成为无法消除的假缺口。
	t.Run("import block skipped", func(t *testing.T) {
		src := strings.Join([]string{
			"package p",
			"",
			"import (",
			"\t\"fmt\"",
			"\t\"strings\"",
			"\t\"sync\"",
			"\t\"time\"",
			")",
		}, "\n")
		path := filepath.Join(t.TempDir(), "z.go")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ScanFile(path, opts); len(got) != 0 {
			t.Fatalf("import 块不应计为缺口，实际 %d", len(got))
		}
	})
}
