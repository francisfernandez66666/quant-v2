// invite_test.go — §GAP2-W2 邀请码注册回归（D7 邀请制）：
// 无码/错码/重码注册与临时号一律拒绝；有效码一次性消费后不可复用。
// English: §GAP2-W2 invite-code registration regression tests.
package auth

import (
	"strings"
	"testing"
	"time"
)

// TestInviteCodeFlow 验证邀请码注册全链路：无码/错码/重码拒绝、有效码一次性消费、临时号同样需码。
func TestInviteCodeFlow(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}

	// ① 无码注册拒绝
	if _, err := m.Register("noinvite", "pw", ""); err == nil {
		t.Fatal("无邀请码注册应被拒")
	}
	// ② 错码拒绝
	if _, err := m.Register("badcode", "pw", "QTdeadbeef"); err == nil {
		t.Fatal("无效邀请码应被拒")
	}
	// ③ 有效码成功，且一次性
	code, err := m.CreateInvite()
	if err != nil || !strings.HasPrefix(code, "QT") {
		t.Fatalf("签发邀请码: %q err=%v", code, err)
	}
	u, err := m.Register("alice", "pw", code)
	if err != nil {
		t.Fatalf("有效码注册失败: %v", err)
	}
	if _, err := m.Register("bob", "pw", code); err == nil {
		t.Fatal("同一邀请码第二次使用应被拒（一次性）")
	}
	// 审计字段
	found := false
	for _, inv := range m.ListInvites() {
		if inv.Code == code {
			found = true
			if inv.UsedBy != u.ID || inv.UsedAt == 0 {
				t.Fatalf("邀请码应记录使用者: %+v", inv)
			}
		}
	}
	if !found {
		t.Fatal("已签发邀请码应可列出")
	}
	// ④ 临时号同样需要有效码
	if _, err := m.CreateTemp(time.Hour, ""); err == nil {
		t.Fatal("无码创建临时号应被拒")
	}
	tcode, _ := m.CreateInvite()
	if _, err := m.CreateTemp(time.Hour, tcode); err != nil {
		t.Fatalf("有效码建临时号失败: %v", err)
	}
}
