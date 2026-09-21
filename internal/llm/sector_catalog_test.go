// 本文件：§D1 归因护栏2（板块点选白名单）的单测——SetSectorCatalog 去重/裁剪、
// 空输入不清旧目录（宁用旧名单也不退回自由发明）、提示词后缀拼接内容。
// English: Unit tests for §D1 guardrail 2 (sector point-select whitelist): dedup/trim on inject,
// empty input keeps the previous catalog, and the prompt suffix content.
package llm

import (
	"strings"
	"testing"
)

func TestSectorCatalogWhitelist(t *testing.T) {
	// 去重 + 裁剪空白 + 保持原顺序，注入后提示词后缀应含逐字名单。
	SetSectorCatalog([]string{" 半导体 ", "半导体", "", "   ", "光模块"})
	sec := sectorCatalogSection()
	if !strings.Contains(sec, "板块点选白名单") {
		t.Fatalf("注入目录后应出现白名单后缀, got %q", sec)
	}
	if !strings.Contains(sec, "半导体、光模块") {
		t.Fatalf("应去重裁剪并保持原顺序, got %q", sec)
	}
	if strings.Contains(sec, "半导体、半导体") {
		t.Fatalf("重复板块名应去重, got %q", sec)
	}
	// 空输入不清旧目录：数据源抖动/冷启动失败时保持上一份白名单，绝不退回自由发明。
	before := sec
	SetSectorCatalog(nil)
	SetSectorCatalog([]string{})
	SetSectorCatalog([]string{"  ", ""})
	if got := sectorCatalogSection(); got != before {
		t.Fatalf("空输入不得清空旧目录: before=%q after=%q", before, got)
	}
}
