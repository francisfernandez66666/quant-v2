// Package commentcheck 提供代码中文注释覆盖度扫描器。
// 用于保证「函数体内关键逻辑块均有中文注释」的工程约定可被自动化验收：
// go test ./internal/commentcheck/ 会对 internal/ 与 cmd/ 全部 Go 源码执行扫描，
// 存在未注释的逻辑块（缺口）时测试失败并打印缺口清单。
//
// English: package commentcheck scans Go sources for logic blocks that lack a Chinese
// comment, enforcing the project's "Chinese comments for key logic" convention via a
// test that fails (with a gap report) whenever uncovered blocks exist.
package commentcheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Options 控制扫描行为。
// English: Options tunes the scan behaviour.
type Options struct {
	// Threshold 连续非注释行达到该长度且含控制流时计为一个待注释块。
	// （Threshold: consecutive non-comment lines with control flow that count as a block.）
	Threshold int
	// Lookback 块首之前多少个非空行内存在注释行即视为已覆盖。
	// （Lookback: how many preceding non-empty lines may hold a comment to consider a block covered.）
	Lookback int
	// SkipImports 是否跳过 import 声明块。
	// （SkipImports: whether to ignore import declaration blocks.）
	SkipImports bool
}

// Gap 描述一个未注释的逻辑块。
// （Gap describes one uncommented logic block.）
type Gap struct {
	File   string // 相对仓库根的文件路径
	Line   int    // 块首行号（1-based）
	Length int    // 连续无注释行数
	Head   string // 块首行内容（截断，便于定位）
}

// DefaultOptions 返回与人工验收口径一致的默认参数。
// （DefaultOptions returns the parameters matching the manual review policy.）
func DefaultOptions() Options {
	return Options{Threshold: 15, Lookback: 3, SkipImports: true}
}

var (
	hanRe  = regexp.MustCompile(`[\x{4e00}-\x{9fff}]`)
	ctrlRe = regexp.MustCompile(`\b(if|for|switch|case|return|func |:=|go func|\blen\(|\bappend\(|\bjson\.|map\[|\bdelete\(|\bregexp|\bsort\.|\bstrings\.)`)
)

// ScanFile 扫描单个 Go 文件，返回其中未注释的逻辑块缺口。
// （ScanFile scans one Go source file and returns uncovered logic blocks.）
func ScanFile(path string, opts Options) []Gap {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(raw), "\n")
	if opts.Threshold <= 0 {
		opts.Threshold = 15
	}
	if opts.Lookback <= 0 {
		opts.Lookback = 3
	}

	var gaps []Gap
	run, start, hasCtrl := 0, 0, false

	flush := func() {
		if run >= opts.Threshold && hasCtrl {
			if opts.SkipImports && inImportBlock(lines, start) {
				return
			}
			if prevCommented(lines, start, opts.Lookback) {
				return
			}
			gaps = append(gaps, Gap{Line: start + 1, Length: run, Head: truncate(lines[start], 70)})
		}
		run, hasCtrl = 0, false
	}

	for i, l := range lines {
		s := strings.TrimSpace(l)
		switch {
		case isCommentLine(s) || s == "" || strings.Contains(l, "//"):
			// 注释行、空行或行内带 // 的行都会打断连续块。
			flush()
		case hanRe.MatchString(l):
			// 含中文的行视为已注释，打断连续块。
			flush()
		default:
			if run == 0 {
				start = i
				hasCtrl = false
			}
			run++
			if ctrlRe.MatchString(l) {
				hasCtrl = true
			}
		}
	}
	flush()
	return gaps
}

// isCommentLine 判断是否为纯注释行或语法边界行。
// （isCommentLine reports whether s is a comment line or syntactic boundary.）
func isCommentLine(s string) bool {
	return strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/*") ||
		strings.HasPrefix(s, "*") || strings.HasPrefix(s, "#")
}

// inImportBlock 判断 0-based 行号 start 是否处于 import 声明区间内。
// （inImportBlock reports whether the given line is inside an import block.）
func inImportBlock(lines []string, start int) bool {
	for i := start; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if s == "" || isCommentLine(s) {
			continue
		}
		if s == "import (" {
			return true
		}
		break
	}
	return false
}

// prevCommented 判断 start 之前 lookback 个非空行内是否存在注释行。
// （prevCommented reports whether a comment line appears within the lookback window.）
func prevCommented(lines []string, start, lookback int) bool {
	for i := start - 1; i >= 0 && lookback > 0; i-- {
		s := strings.TrimSpace(lines[i])
		if s == "" {
			continue
		}
		lookback--
		if isCommentLine(s) {
			return true
		}
		break
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ScanTree 递归扫描 root 下的全部非测试 Go 文件。
// 返回按 文件+行号 排序的缺口列表。
// （ScanTree walks root recursively for non-test Go files and returns sorted gaps.）
func ScanTree(root string, opts Options) []Gap {
	var gaps []Gap
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, g := range ScanFile(path, opts) {
			g.File = path
			gaps = append(gaps, g)
		}
		return nil
	})
	sort.Slice(gaps, func(i, j int) bool {
		if gaps[i].File != gaps[j].File {
			return gaps[i].File < gaps[j].File
		}
		return gaps[i].Line < gaps[j].Line
	})
	return gaps
}
