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
