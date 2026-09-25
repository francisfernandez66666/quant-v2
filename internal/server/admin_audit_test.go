// §0925EVE-W3-F（⑲/D4 特权变更审计洞收口）：admin.go 的改角色/改权限/启禁用/删除/到期
// 五类特权写操作此前沿着「成功即 log.Printf」路径散场，opslog 安全审计文件里查不到行
// （租户面 tenant_create/update/move 反而全覆盖）——事后无法回答「谁在何时把谁改成了什么」。
// 本测试逐端点断言 audit-YYYYMMDD.log 真实落行（与 side_unverified_report_test 同法：
// 断言的是可复核的文件行，不是一行 stderr），并按既有体例核对 event/actor/target/result 四字段。
// English: §D4 — asserts the five privileged admin write endpoints (role/perms/enabled/delete/expiry)
// each append a structured opslog audit line, closing the audit blind spot.
package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/opslog"
)

// TestAdminPrivilegeChangesAudited 五类特权变更各产生一条审计行，字段可复核。
func TestAdminPrivilegeChangesAudited(t *testing.T) {
	s, admin := newAdminTestServer(t)
	// opslog 重定向到临时目录：审计行必须是真实落盘的文件行。
	logDir := filepath.Join(t.TempDir(), "opslog")
	opslog.Init(logDir, 0)

	victim, err := s.auth.CreateUser("audit_me", "pw", "user", nil, 0)
	if err != nil {
		t.Fatalf("建被测账号: %v", err)
	}
	vid := victim.ID

	// 五类操作逐条打到各自端点（删除放最后，删完人没了）。
	type call struct {
		event, body, method, path string // event=期望审计事件名；method/path=端点路由
	}
	calls := []call{
		{"user_role", `{"role":"user"}`, http.MethodPost, "/api/admin/users/" + vid + "/role"},
		{"user_perms", `{"perms":["research_approve"]}`, http.MethodPost, "/api/admin/users/" + vid + "/perms"},
		{"user_enabled", `{"enabled":false}`, http.MethodPost, "/api/admin/users/" + vid + "/enabled"},
		{"user_expiry", `{"expires_days":30}`, http.MethodPost, "/api/admin/users/" + vid + "/expiry"},
		{"user_delete", "", http.MethodDelete, "/api/admin/users/" + vid},
	}
	for i, c := range calls {
		rr := adminDo(s, adminReq(s, admin, c.method, c.path, c.body))
		if rr.Code != 200 {
			t.Fatalf("第 %d 类操作 %s 应 200，got %d body=%s", i+1, c.event, rr.Code, rr.Body.String())
		}
	}

	// 审计文件逐 event 断言行存在，且 actor=操作管理员、target=被改用户（既有四字段体例）。
	auditPath := filepath.Join(logDir, "audit-"+time.Now().Format("20060102")+".log")
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("读审计文件失败（特权变更无痕，⑲ 复归）: %v", err)
	}
	line := string(data)
	for _, c := range calls {
		want := fmt.Sprintf("event=%s actor=%s target=%s", c.event, admin.ID, vid)
		if !strings.Contains(line, want) {
			t.Fatalf("审计文件缺行 %q，实际内容=\n%s", want, line)
		}
	}
	// result 字段携带可复核的变更内容（角色/权限/禁用/到期终态），不是空 ok 一笔带过；
	// 与 password_reset 体例一致：审计只记成功路径，绝不落密码等新口令值。
	for _, want := range []string{
		"event=user_role actor=" + admin.ID + " target=" + vid + " result=role=user",
		"result=perms=[research_approve]",
		"result=enabled=false",
		"result=expires_days=30",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("审计 result 字段缺 %q，实际内容=\n%s", want, line)
		}
	}
}
