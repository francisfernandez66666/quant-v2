// missing_cell_test.go — §P2-F（2026-10-06 修复批 波 5）缺测标记与"有读数"的三态取数锁。
//
// 这一族缺陷的根在取数层：`F()` 把"这一格没有读数"和"读数就是 0"折成同一个 0。
// 所以 FOk/SOk 的全部意义是**三态**（有读数 / 无读数 / 读数是 0），本文件把它钉死：
//
//	T1 FOk：真实 0 必须是 ok=true；空串、MissingCell（NA/na/带空格）、非数值串、缺键、nil 行都是 ok=false；
//	T2 F 与 FOk 的分歧点只允许是缺测标记——把 0 也判成缺测会让停牌/平盘的真实读数消失；
//	T3 SOk：NA 不是合法文本；真实来源名 ok=true；数字单元格走 S() 的转字符串路径仍算有读数；
//	T4 线格式端到端：sidecar 返回含 NA 与 source 列的 CSV 时，客户端必须把 NA 保持成"无读数"、
//	   把 source 保持成字符串（float 转换失败会回落字符串，这条路径一旦被"解析失败折成 0"改动，
//	   降级标记就静默变成空串，读侧再也圈不出降级行）。
//
// 跨语言等值（Python 侧 _NA/_SRC_* 字面量 == Go 侧 MissingCell/DailySource*）不放在这里：
// Go 测试按相对路径去读 .py 文件会随包移动而失效，那把锁在门禁 §112 用 grep 同源断言。
//
// English: three-state accessors (missing vs zero) are the whole fix — locks here pin FOk/SOk and
// the CSV path that carries NA/source from the sidecar.
package data

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFOkDistinguishesMissingFromZero(t *testing.T) {
	row := TushareRow{
		"pctchg":      0.0,    // 真实读数：平盘
		"tradestatus": "NA",   // 缺测标记（sidecar 降级腿的写法）
		"pettm":       " na ", // 带空格/小写的同一标记，必须同判缺测
		"pbmrq":       "",     // 空串（旧形态，正是被折成 0 的那个）
		"psttm":       json.Number("0"),
		"pcfncfttm":   json.Number("abc"), // 非数值：解析不出来＝无读数
		"isst":        nil,
	}
	if v, ok := row.FOk("pctchg"); !ok || v != 0 {
		t.Fatalf("真实 0 被判成缺测（v=%v ok=%v）⇒ 平盘/停牌读数会整体消失", v, ok)
	}
	for _, k := range []string{"tradestatus", "pettm", "pbmrq", "pcfncfttm", "isst", "not_a_column"} {
		if v, ok := row.FOk(k); ok {
			t.Fatalf("%s=%q 被判成有读数（v=%v）⇒ 缺测列会带着伪造 0 落库", k, row[k], v)
		}
	}
	if v, ok := row.FOk("psttm"); !ok || v != 0 {
		t.Fatalf("json.Number(0) 必须是有读数（v=%v ok=%v）", v, ok)
	}
	// nil 行整行缺测（装载层拿到空行时不该崩，也不该判成 0）
	var nilRow TushareRow
	if _, ok := nilRow.FOk("pctchg"); ok {
		t.Fatalf("nil 行被判成有读数")
	}
}

func TestFAgreesWithFOkExceptOnMissingMarker(t *testing.T) {
	// F 与 FOk 只能在"有没有读数"上分歧，有读数时两者必须同值——
	// 否则老调用点（继续用 F 的那些）与新落库点会对同一格给出两个数。
	cases := []struct {
		key string
		val any
	}{
		{"a", 0.0}, {"b", 5.5}, {"c", json.Number("7")}, {"d", "8.25"}, {"e", int64(3)},
	}
	for _, c := range cases {
		r := TushareRow{c.key: c.val}
		fv, fok := r.FOk(c.key)
		if !fok {
			t.Fatalf("%s=%v 应为有读数（FOk false 而 F=%v）", c.key, c.val, r.F(c.key))
		}
		if r.F(c.key) != fv {
			t.Fatalf("%s：F=%v 与 FOk=%v 分歧 ⇒ 同一格两个数", c.key, r.F(c.key), fv)
		}
	}
	// 唯一的分歧点：缺测标记。F 折 0（老语义保留），FOk 判 false——落库侧必须走后者。
	r := TushareRow{"x": MissingCell}
	if r.F("x") != 0 {
		t.Fatalf("F(NA)=%v，期望 0（保留老调用点语义；改这里要全仓扫 F 的落库用法）", r.F("x"))
	}
	if _, ok := r.FOk("x"); ok {
		t.Fatalf("FOk(NA)=ok ⇒ 缺测标记又被当成读数，本批修复直接失效")
	}
}

func TestSOkTreatsMissingMarkerAsAbsent(t *testing.T) {
	r := TushareRow{"source": "sina_degraded", "note": MissingCell, "blank": "  ", "num": 12.5}
	if v, ok := r.SOk("source"); !ok || v != "sina_degraded" {
		t.Fatalf("SOk(source)=(%q,%v)，期望 (sina_degraded,true)", v, ok)
	}
	for _, k := range []string{"note", "blank", "missing_key"} {
		if v, ok := r.SOk(k); ok {
			t.Fatalf("SOk(%s)=(%q,true)：缺测标记/空白不是合法文本", k, v)
		}
	}
	// 数字单元格走 S() 的转字符串形态（dailySourceOf 之类取值时不会因为类型而误判缺测）。
	if v, ok := r.SOk("num"); !ok || v != "12.5" {
		t.Fatalf("SOk(num)=(%q,%v)，期望 (12.5,true)", v, ok)
	}
}

// TestSidecarCarriesNaAndSourceThroughCsvParsing 走真实 HTTP+CSV 路径（T4）。
func TestSidecarCarriesNaAndSourceThroughCsvParsing(t *testing.T) {
	hdr := "date,code,open,high,low,close,preclose,volume,amount,adjustflag," +
		"turn,tradestatus,pctChg,peTTM,pbMRQ,psTTM,pcfNcfTTM,isST,source"
	line := "2026-09-21,sh.600000,10.5,11.0,10.4,10.9,10.5,130000,1417000,3," +
		"1.2,NA,NA,NA,NA,NA,NA,NA,sina_degraded"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, hdr)
		fmt.Fprintln(w, line)
	}))
	defer srv.Close()

	rows, err := NewBaostockClient(srv.URL).StockKline("sh.600000", "2026-09-21", "2026-09-21")
	if err != nil {
		t.Fatalf("StockKline: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("行数=%d，期望 1", len(rows))
	}
	r := rows[0]
	if r.S("source") != "sina_degraded" {
		t.Fatalf("source=%q，期望 sina_degraded（非数值列被 float 转换吞掉＝降级标记静默丢了）", r.S("source"))
	}
	for _, k := range []string{"tradestatus", "pctchg", "pettm", "pbmrq", "psttm", "pcfncfttm", "isst"} {
		if _, ok := r.FOk(k); ok {
			t.Fatalf("%s 有读数：NA 在 CSV 解析路上被折成了值（落库就会写伪造 0）", k)
		}
	}
	// 真实读数照常在场
	if v, ok := r.FOk("turn"); !ok || v != 1.2 {
		t.Fatalf("turn=(%v,%v)，期望 (1.2,true)", v, ok)
	}
}
