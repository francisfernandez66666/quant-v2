// §0929CONTRACT-IGNORED（09-29 全量审计批 ⑤）POST /api/holdings 契约卫生回归：
//
// 背景（缺陷的因果链）：整表同步端点从来没消费过 available_balance（§P1-11 起资金只认
// /api/holdings/balance 窄口径），但这件事在**契约上不可发现**：
//   - 请求结构体把这个字段声明成正经字段（`AvailableBalance float64`），读代码的人才知道它没用；
//   - 处理器里只有一行 `_ = req.AvailableBalance`；
//   - 前端 api/index.js 的注释长期写着"更新持仓数据（含可用资金）"，Positions 页也确实带着它上行。
//
// 于是"改资金没生效"这类问题只能靠人翻源码定位（正是 §ROBUST 一路在消灭的"声明与实现不符"）。
//
// 本批把丢弃改成**可发现**：响应回 ignored_fields；同时 typed 解码换成"先看原始顶层键集合"，
// 因为 typed 解码会把"没传"与"传了 0"折叠成同一结果——回执要的正是这个区分。
// 三条锁各管一侧：
//
//	T1 带着 available_balance 上送 → 响应点名它 + 资金读数**没被动过** + 持仓照常落库；
//	T2 不带该键 → ignored_fields 为空数组（不是 null、不是缺键：证明判据来自真实键集合）；
//	T3 结构体不得再声明该字段（静态锁，防止有人"顺手"把丢弃写回 typed 解码，让 T2 退化）。
//
// English: the whole-table holdings endpoint never consumed available_balance; this batch makes
// that discard discoverable (ignored_fields in the response, computed from the raw top-level key
// set) and locks the behavior plus the shape of both the payload-present and absent branches.
package server

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// holdings_ignored_field_test.go 复用 §E1 的最小接线夹具 e1Server（live 库 + 报表库 + agg 补接）：
// 自己再拼一份会漏掉 s.agg，而只要种下一笔持仓，GET /api/holdings 就会经 buildHolding→dashFor
// 读到 nil 聚合器（§E1 那份夹具的注释里记着这个坑）。

// holdingsRespFields 解析一次 /api/holdings 写响应，返回 ignored_fields（缺键直接判红）。
// English: pull ignored_fields out of a whole-table write response.
func holdingsRespFields(t *testing.T, body string) []string {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("响应非 JSON: %v body=%s", err, body)
	}
	raw, ok := m["ignored_fields"]
	if !ok {
		t.Fatalf("§0929CONTRACT-IGNORED：整表写响应必须带 ignored_fields（缺了＝丢弃重新变成不可发现）: %s", body)
	}
	list, ok := raw.([]interface{})
	if !ok {
		t.Fatalf("§0929CONTRACT-IGNORED：ignored_fields 必须是数组，got %T", raw)
	}
	out := []string{}
	for _, v := range list {
		if sv, ok := v.(string); ok {
			out = append(out, sv)
		}
	}
	return out
}

// TestHoldingsIgnoredFieldPresent T1：带 available_balance 上送 → 被点名 + 资金没被动过 + 持仓照常落。
func TestHoldingsIgnoredFieldPresent(t *testing.T) {
	s, admin, _, _ := e1Server(t)

	// 先用窄口径把资金定在一个可识别的值（等值锁的基线）
	req := adminReq(s, admin, "POST", "/api/holdings/balance", `{"available_balance":12345.67}`)
	if rr := adminDo(s, req); rr.Code != 200 {
		t.Fatalf("窄口径改资金期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	// 整表写：载荷里刻意带上 available_balance=999（旧口径下它会被静默吞掉）
	req = adminReq(s, admin, "POST", "/api/holdings",
		`{"holdings":[{"code":"600998","name":"契约票","quantity":100,"cost_price":10.5,"take_profit_pct":8,"stop_loss_pct":5}],"available_balance":999}`)
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("整表写期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	fields := holdingsRespFields(t, rr.Body.String())
	if len(fields) != 1 || fields[0] != "available_balance" {
		t.Fatalf("§0929CONTRACT-IGNORED：带上的遗留字段必须在 ignored_fields 里点名，got %v", fields)
	}

	// 资金读数必须仍是窄口径写入的那个值（整表写绝不能顺手改资金）
	req = adminReq(s, admin, "GET", "/api/holdings", "")
	rr = adminDo(s, req)
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /api/holdings 响应非 JSON: %v", err)
	}
	if bal, _ := body["available_balance"].(float64); bal != 12345.67 {
		t.Fatalf("§0929CONTRACT-IGNORED：整表写把资金改成了 %v（该字段必须只被点名、不被消费）", bal)
	}
	hs, _ := body["holdings"].([]interface{})
	if len(hs) != 1 {
		t.Fatalf("持仓应落库 1 条，got %v", body["holdings"])
	}
}

// TestHoldingsIgnoredFieldAbsent T2：不带该键 → ignored_fields 是**空数组**。
// 这条是判据来源的证明：typed 解码把"没传"与"传 0"折叠，若实现退回结构体字段判存在，
// 这里要么报出假的 available_balance、要么键集合判断失去意义。
func TestHoldingsIgnoredFieldAbsent(t *testing.T) {
	s, admin, _, _ := e1Server(t)
	req := adminReq(s, admin, "POST", "/api/holdings",
		`{"holdings":[{"code":"600997","name":"干净票","quantity":200,"cost_price":9.9,"take_profit_pct":8,"stop_loss_pct":5}]}`)
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("整表写期望 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if fields := holdingsRespFields(t, rr.Body.String()); len(fields) != 0 {
		t.Fatalf("§0929CONTRACT-IGNORED：没上送遗留字段却回执了 %v", fields)
	}
}

// TestHoldingsReqShapeNoBalanceField T3：静态锁——请求结构体不得再声明 available_balance。
// 用 go/ast 读源码而不是跑一次请求：这条要防的是"把丢弃写回 typed 解码"这种**语义回退**，
// 一旦发生，T1/T2 可能仍然绿（响应字段还在），但"传了 0"与"没传"重新变得不可区分。
func TestHoldingsReqShapeNoBalanceField(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handlers_fix.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 handlers_fix.go 失败: %v", err)
	}
	var found *ast.StructType
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "fixSetHoldingsReq" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if ok {
			found = st
		}
		return false
	})
	if found == nil {
		t.Fatalf("未找到 fixSetHoldingsReq 结构体（端点结构被改名要先回来更新本锁）")
	}
	for _, fld := range found.Fields.List {
		if fld.Tag == nil {
			continue
		}
		// ast 里字段标签是一个 BasicLit 字面量（含反引号），直接按原文子串判键名
		if strings.Contains(fld.Tag.Value, "available_balance") {
			t.Fatalf("§0929CONTRACT-IGNORED：available_balance 不得回到 typed 结构体（丢弃判据必须来自原始键集合）")
		}
	}
}
