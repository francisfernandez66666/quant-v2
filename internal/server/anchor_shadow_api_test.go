// anchor_shadow_api_test.go — §ANCHOR-FIRST W2 只读接口回归：
// 缺文件/损坏文件/正常倒序/天数上限/非管理员 403 五条口径。
package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAnchorShadowAPI(t *testing.T) {
	s, admin := newAdminTestServer(t)
	dir := t.TempDir()
	s.SetCacheDir(dir)

	read := func(query string) (int, anchorShadowView) {
		t.Helper()
		rr := adminDo(s, adminReq(s, admin, http.MethodGet, "/api/anchor-shadow"+query, ""))
		var out anchorShadowView
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr.Code, out
	}

	// ① 文件不存在：200 + state=missing，前端渲染空态而不是报错。
	code, out := read("?days=5")
	if code != http.StatusOK || out.State != "missing" {
		t.Fatalf("缺文件应 200/missing，got code=%d state=%q", code, out.State)
	}
	if out.Days == nil || len(out.Days) != 0 {
		t.Fatalf("缺文件应返回空数组（不是 null），got %+v", out.Days)
	}

	// ② 正常文件：按日期倒序，days 参数裁剪。
	path := filepath.Join(dir, "accounts", admin.ID, "anchor_shadow.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"account":"` + admin.ID + `","keep_days":60,"days":[
		{"day":"2026-10-24","books":{"live":{"summary":{"rows":2},"rows":[]}}},
		{"day":"2026-10-25","books":{"live":{"summary":{"rows":3},"rows":[]},"report":{"summary":{"rows":1},"rows":[]}}}
	]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = read("?days=1")
	if code != http.StatusOK || out.State != "ok" {
		t.Fatalf("正常文件应 200/ok，got code=%d state=%q", code, out.State)
	}
	if len(out.Days) != 1 || out.Days[0].Day != "2026-10-25" {
		t.Fatalf("days=1 应只给最新一日，got %+v", out.Days)
	}
	if _, ok := out.Days[0].Books["report"]; !ok {
		t.Fatalf("账本明细应透传，got %+v", out.Days[0].Books)
	}

	// ③ 损坏文件：200 + state=corrupt（原文件不动，页面显式提示）。
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = read("")
	if code != http.StatusOK || out.State != "corrupt" {
		t.Fatalf("损坏文件应 200/corrupt，got code=%d state=%q", code, out.State)
	}

	// ④ 非管理员：403，且不能读到账号目录。
	member, err := s.auth.CreateUser("member_anchor_shadow", "memberpw", "user", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	rr := adminDo(s, adminReq(s, member, http.MethodGet, "/api/anchor-shadow", ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("非管理员应 403，got %d: %s", rr.Code, rr.Body.String())
	}
}
