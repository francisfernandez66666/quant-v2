// Command commentcheck 扫描仓库 Go 源码中未带中文注释的逻辑块，
// 用于人工或 CI 验收「函数体内关键逻辑均有中文注释」约定。
//
// 用法：go run ./cmd/commentcheck [目录...]   （缺省扫描 internal/ 与 cmd/）
// 存在缺口时以非零退出码结束并打印缺口清单。
//
// English: commentcheck scans Go sources for logic blocks lacking a Chinese comment,
// enforcing the project convention via CLI/CI. Exit code is non-zero when gaps exist.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"quant-trading-v2/internal/commentcheck"
)

// main 注释门禁入口：--thr 连续无注释行阈值，扫描 cmd/internal 全部 Go 源码。
func main() {
	thr := flag.Int("thr", 15, "连续无注释行阈值")
	flag.Parse()

	// 该命令约定在仓库根执行：先取 cwd 作为拼相对目录的基准，取不到就直接退出 2，
	// 避免扫描范围静默退化成当前子目录。
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "getwd:", err)
		os.Exit(2)
	}
	dirs := flag.Args()
	if len(dirs) == 0 {
		dirs = []string{"internal", "cmd"}
	}
	opts := commentcheck.DefaultOptions()
	opts.Threshold = *thr

	total := 0
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			d = filepath.Join(root, d)
		}
		gaps := commentcheck.ScanTree(d, opts)
		for _, g := range gaps {
			fmt.Printf("%s:%d  （长度 %d 行）%s\n", g.File, g.Line, g.Length, g.Head)
		}
		total += len(gaps)
	}
	if total > 0 {
		fmt.Printf("\n发现 %d 处未注释逻辑块（阈值 %d 行）。\n", total, *thr)
		os.Exit(1)
	}
	fmt.Printf("通过：%d 个目录内全部 Go 源码逻辑块均有中文注释（阈值 %d 行）。\n", len(dirs), *thr)
}
