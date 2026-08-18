// 研究池文件读取（每行一个 ts_code，# 注释）。
package main

import (
	"bufio"
	"os"
	"strings"
)

// readCodesFile 读取研究池文件：每行一个 ts_code，跳过空行与 # 注释。
// （readCodesFile loads ts_codes from a text file, one per line, skipping blanks/#.）
func readCodesFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var codes []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		codes = append(codes, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return codes, nil
}