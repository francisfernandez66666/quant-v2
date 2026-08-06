package newsagent

import (
	"strings"
	"testing"
)

// TestSalvageStage0ObjectsLineSalvage 整体数组解析失败时应逐行抢救，
// 且 "key":] 空值畸形行经修复也能救活，不丢数据。
func TestSalvageStage0ObjectsLineSalvage(t *testing.T) {
	resp := `[
{"index": 1, "category": "official", "material": true, "corrected_title": ""},
{"index": 2, "category": "official", "material": true, "corrected_title":]},
{"index": 3, "category": "interactive", "material": true, "corrected_title": ""}
]`
	raw, ok := salvageStage0Objects(resp)
	if !ok {
		t.Fatalf("逐行抢救应成功")
	}
	if len(raw) != 3 {
		t.Fatalf("期望3条(含修复的坏行), 实际 %d: %+v", len(raw), raw)
	}
	if raw[0].Index != 1 || raw[0].Category != "official" {
		t.Fatalf("第1条应保留: %+v", raw[0])
	}
	if raw[1].Index != 2 || raw[1].Category != "official" {
		t.Fatalf("坏行(2)应被修复保留: %+v", raw[1])
	}
	if raw[2].Index != 3 || raw[2].Category != "interactive" {
		t.Fatalf("第3条应保留: %+v", raw[2])
	}
}

// TestSalvageStage0ObjectsClean 整体可正常解析时直接走整组。
func TestSalvageStage0ObjectsClean(t *testing.T) {
	resp := `[{"index":1,"category":"official","material":true,"corrected_title":"x"}]`
	raw, ok := salvageStage0Objects(resp)
	if !ok || len(raw) != 1 || raw[0].Corrected != "x" {
		t.Fatalf("期望整体解析1条: ok=%v raw=%+v", ok, raw)
	}
}

// TestSalvageStage0ObjectsAllBroke 完全没有可解析对象时返回失败。
func TestSalvageStage0ObjectsAllBroke(t *testing.T) {
	resp := `garbage output without any braces`
	_, ok := salvageStage0Objects(resp)
	if ok {
		t.Fatalf("应判定抢救失败")
	}
}

// TestSalvageStage0ObjectsStringIndex 模型把 index/material 输出成字符串（"1"/"true"）时应能容错解析。
func TestSalvageStage0ObjectsStringIndex(t *testing.T) {
	resp := `[{"index": "1", "category": "official", "material": "true", "corrected_title": ""}]`
	raw, ok := salvageStage0Objects(resp)
	if !ok || len(raw) != 1 {
		t.Fatalf("期望整体解析1条: ok=%v raw=%+v", ok, raw)
	}
	if int(raw[0].Index) != 1 || !bool(raw[0].Material) {
		t.Fatalf("字符串 index/material 应容错: %+v", raw[0])
	}
}

// TestSalvageStage0ObjectsSingleLineCorrupt 单行数组内嵌畸形对象时，逐对象扫描应全部救回。
func TestSalvageStage0ObjectsSingleLineCorrupt(t *testing.T) {
	resp := `[{"index":1,"category":"official","material":true,"corrected_title":]},{"index":2,"category":"interactive","material":false,"corrected_title":""}]`
	raw, ok := salvageStage0Objects(resp)
	if !ok || len(raw) != 2 {
		t.Fatalf("单行数组内嵌畸形应逐个救回: ok=%v raw=%+v", ok, raw)
	}
	if int(raw[1].Index) != 2 || raw[1].Category != "interactive" {
		t.Fatalf("第2个对象应保留: %+v", raw[1])
	}
}

// TestSalvageStage0ObjectsTrailingJunk 字符串收尾杂散括号/撇号（"上涨") 等）应被修复。
// 注：若模型再塞一个多余引号（"上涨"")）会破坏花括号配对的字符串状态，交由重试队列处理。
func TestSalvageStage0ObjectsTrailingJunk(t *testing.T) {
	resp := `[
{"index":1,"category":"official","material":true,"corrected_title":"美股三大指数开盘均上涨")},
{"index":2,"category":"official","material":true,"corrected_title":"以色列空袭黎巴嫩南部回应真主党违规行为)"},
{"index":3,"category":"official","material":false,"corrected_title":"古吉拉特邦爆金迪普拉病毒致22死"'}
]`
	raw, ok := salvageStage0Objects(resp)
	if !ok {
		t.Fatalf("逐对象抢救应成功")
	}
	if len(raw) != 3 {
		t.Fatalf("3个对象都应救回, 实际 %d: %+v", len(raw), raw)
	}
	if !strings.Contains(raw[1].Corrected, "违规行为") {
		t.Fatalf("对象2应保留正文: %+v", raw[1])
	}
	if !strings.Contains(raw[2].Corrected, "22死") {
		t.Fatalf("对象3应保留正文: %+v", raw[2])
	}
}

// TestSalvageStage0ObjectsSingleQuote 模型把键尾引号/空值写成单引号时（"corrected_title':”"）应修复。
func TestSalvageStage0ObjectsSingleQuote(t *testing.T) {
	resp := `[{"index":1,"category":"official","material":false,"corrected_title':''"},{"index":2,"category":"official","material":true,"corrected_title':''"}]`
	raw, ok := salvageStage0Objects(resp)
	if !ok || len(raw) != 2 {
		t.Fatalf("单引号畸形应修复: ok=%v raw=%+v", ok, raw)
	}
	if bool(raw[0].Material) || !bool(raw[1].Material) {
		t.Fatalf("material 应按输入保留: %+v", raw)
	}
}
