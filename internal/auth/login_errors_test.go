// 登录失败错误串契约（§FIX-20261028-F04，2026-10-29）。
//
// server.handleLogin 的分码是靠**匹配 Login 返回的错误串**实现的（loginFailureCode）。
// 这条契约很脆：谁改了 auth 里的 "account expired" 文案，分码就会静默失效、用户又回到
// "账号或密码不正确"的锁死形态。所以这里把三条串逐字钉住。
//
// English: pins the literal error strings that server.loginFailureCode matches on — renaming
// them in auth would silently disable the expiry/disabled login codes.
package auth

import (
	"strings"
	"testing"
	"time"
)

// TestLoginErrorStringsForExpiredAndDisabled 到期/停用的错误串必须逐字稳定，口令错/不存在必须同形。
func TestLoginErrorStringsForExpiredAndDisabled(t *testing.T) {
	m := newTestMgr(t)

	u, err := m.CreateUser("expireduser", "goodpass", RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// 直接把到期时刻挪到过去（运维面没有"立即过期"入口，测试在同包内改字段最干净）。
	if err := m.updateUser(u.ID, func(x *User) error {
		x.ExpiresAt = time.Now().Add(-time.Hour).Unix()
		return nil
	}); err != nil {
		t.Fatalf("updateUser expiry: %v", err)
	}
	if _, err := m.Login("expireduser", "goodpass"); err == nil || !strings.Contains(err.Error(), "account expired") {
		t.Fatalf("到期账号的 Login 错误串应含 \"account expired\"，实到 %v", err)
	}

	d, err := m.CreateUser("disableduser", "goodpass", RoleUser, nil, 0)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := m.SetEnabled(d.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if _, err := m.Login("disableduser", "goodpass"); err == nil || !strings.Contains(err.Error(), "account disabled") {
		t.Fatalf("停用账号的 Login 错误串应含 \"account disabled\"，实到 %v", err)
	}

	// 口令错与用户名不存在：两条错误串**只允许在内部不同**，对外必须由 server 压成同一个 401。
	if _, err := m.Login("disableduser", "badpass"); err == nil {
		t.Fatal("错口令必须报错")
	}
	if _, err := m.Login("ghost-user", "whatever"); err == nil {
		t.Fatal("不存在的用户名必须报错")
	}
}
