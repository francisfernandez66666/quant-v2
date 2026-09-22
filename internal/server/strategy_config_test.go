// strategy_config_test.go — §N-4（2026-09-22 傍晚批 §CFGSMASH + §中-6）端点级行为锁。
//
// 钉住 POST /api/config/strategy 对外语义从「整份全量替换（没传=清零）」变更为
// 「稀疏 merge（没传=保留旧值）」+ updated_at 乐观锁：
//
//	① body 只带部分键时，未出现的分组/字段保留旧值（旧实现在这里全部落 0 并持久化）；
//	② 写响应与 GET 均回传 updated_at；带过期版本再写 → 409（附 current_updated_at），
//	   服务端值不动——两个管理员不再互相静默覆盖；
//	③ 不带 updated_at = 不比对（兼容脚本/旧客户端直 POST）；
//	④ 未知键丢弃，不污染配置。
//
// English: §N-4 endpoint lock — the write is a sparse merge (absent keys keep stored values),
// with mid-6 optimistic locking via updated_at (stale base => 409 without touching the server
// value; absent base => no comparison for legacy callers).
package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"quant-trading-v2/internal/auth"
)

// strategyGet 读回 GET /api/config/strategy 的响应 map。
func strategyGet(t *testing.T, s *Server, admin *auth.User) map[string]any {
	t.Helper()
	req := adminReq(s, admin, http.MethodGet, "/api/config/strategy", "")
	rr := adminDo(s, req)
	if rr.Code != 200 {
		t.Fatalf("GET strategy 应 200, got %d", rr.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET strategy 解析失败: %v", err)
	}
	return out
}

// strategyPost 执行一次 POST /api/config/strategy，返回状态码与解析后的响应。
func strategyPost(t *testing.T, s *Server, admin *auth.User, body string) (int, map[string]any) {
	t.Helper()
	req := adminReq(s, admin, http.MethodPost, "/api/config/strategy", body)
	rr := adminDo(s, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestSetStrategyConfigSparseMergeAndVersion(t *testing.T) {
	s, admin := newAdminTestServer(t)

	// ① 基线整份写入：dragon 与 double_bump 各立一个可辨认的非零值。
	code, res := strategyPost(t, s, admin, `{"dragon":{"take_profit_pct":10,"f1_seal_weight":0.4},"double_bump":{"first_break_volume_multiple":2.5}}`)
	if code != 200 {
		t.Fatalf("基线写入应 200, got %d body=%v", code, res)
	}
	v1, _ := res["updated_at"].(string)
	if v1 == "" {
		t.Fatal("§中-6：写响应必须回传新 updated_at")
	}

	// ② 稀疏 patch（带 v1 基线）：只改 dragon.take_profit_pct，其余一切保留。
	code, res = strategyPost(t, s, admin, `{"updated_at":"`+v1+`","dragon":{"take_profit_pct":7}}`)
	if code != 200 {
		t.Fatalf("稀疏写应 200, got %d body=%v", code, res)
	}
	got := strategyGet(t, s, admin)
	dragon, _ := got["dragon"].(map[string]any)
	db, _ := got["double_bump"].(map[string]any)
	if dragon["take_profit_pct"] != float64(7) {
		t.Fatalf("出现键应更新, got %v", dragon["take_profit_pct"])
	}
	if dragon["f1_seal_weight"] != 0.4 {
		t.Fatalf("§N-4：同组未出现键必须保留（旧全量替换在此落 0）, got %v", dragon["f1_seal_weight"])
	}
	if db["first_break_volume_multiple"] != 2.5 {
		t.Fatalf("§N-4：整组未出现必须原样保留, got %v", db)
	}
	v2, _ := res["updated_at"].(string)
	if v2 == "" || v2 == v1 {
		t.Fatalf("每次写入应推进版本戳, v1=%q v2=%q", v1, v2)
	}

	// ③ 过期基线（模拟另一管理员先落一步后的后写者）→ 409 且服务端值不动。
	code, res = strategyPost(t, s, admin, `{"updated_at":"`+v1+`","dragon":{"take_profit_pct":0}}`)
	if code != http.StatusConflict {
		t.Fatalf("§中-6：版本不匹配应 409, got %d body=%v", code, res)
	}
	if cur, _ := res["current_updated_at"].(string); cur != v2 {
		t.Fatalf("409 应回传服务端当前版本, got %v want %q", res["current_updated_at"], v2)
	}
	got = strategyGet(t, s, admin)
	dragon, _ = got["dragon"].(map[string]any)
	if dragon["take_profit_pct"] != float64(7) {
		t.Fatalf("冲突写不得落库, got %v", dragon["take_profit_pct"])
	}

	// ④ 不带 updated_at：跳过比对（脚本直 POST 兼容路径）。
	code, _ = strategyPost(t, s, admin, `{"momentum":{"macd_weight":33}}`)
	if code != 200 {
		t.Fatalf("无基线写应 200（不比对）, got %d", code)
	}

	// ⑤ 未知键丢弃：合法写成功但不得进入配置面。
	code, _ = strategyPost(t, s, admin, `{"dragon":{"take_profit_pct":8},"bogus_group":{"x":1}}`)
	if code != 200 {
		t.Fatalf("未知顶层键应被宽容丢弃, got %d", code)
	}
	got = strategyGet(t, s, admin)
	if _, bad := got["bogus_group"]; bad {
		t.Fatal("§N-4：未知键不得回显/落库")
	}
	if v, _ := got["updated_at"].(string); v == "" {
		t.Fatal("GET 响应应携带 updated_at 供下一轮保存比对")
	}
}

// TestSetStrategyConfigRejectsBadShape 结构非法的 body（字段类型不符）必须 400 且
// 旧配置原样保留——merge 失败绝不能变成半份落盘。
func TestSetStrategyConfigRejectsBadShape(t *testing.T) {
	s, admin := newAdminTestServer(t)
	code, _ := strategyPost(t, s, admin, `{"dragon":{"take_profit_pct":9.9}}`)
	if code != 200 {
		t.Fatalf("前置写入失败 %d", code)
	}
	code, _ = strategyPost(t, s, admin, `{"dragon":{"take_profit_pct":"不是数字"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("字段类型不符应 400, got %d", code)
	}
	got := strategyGet(t, s, admin)
	dragon, _ := got["dragon"].(map[string]any)
	if dragon["take_profit_pct"] != 9.9 {
		t.Fatalf("被拒写入不得改变服务端值, got %v", dragon["take_profit_pct"])
	}
}
