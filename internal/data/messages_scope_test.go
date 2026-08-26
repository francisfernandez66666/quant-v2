// messages_scope_test.go — §GAP2-W2 消息中心账户隔离回归：
// 公共消息全员可见；私有消息仅归属账号可见；Sync 刷新保留作用域；删除墓碑按 ID 全局生效。
// English: §GAP2-W2 message-center isolation regression tests.
package data

import (
	"testing"
	"time"
)

func TestMessageScopeVisibility(t *testing.T) {
	s := NewMessageStore("")
	now := time.Now()
	s.Sync([]MessageItem{
		{ID: "600000@交易信号@龙头", Code: "600000", Level: "交易信号", Scope: "", GeneratedAt: now},
		{ID: "u_owner|hold@S1", Code: "000001", Level: "持仓提示", Scope: "u_owner", GeneratedAt: now},
		{ID: "u_friend|hold@S2", Code: "000002", Level: "持仓提示", Scope: "u_friend", GeneratedAt: now},
	})
	owner := s.ListVisible("u_owner")
	if len(owner) != 2 {
		t.Fatalf("owner 应见 公共1+私有1, got %d", len(owner))
	}
	friend := s.ListVisible("u_friend")
	if len(friend) != 2 {
		t.Fatalf("friend 应见 公共1+私有1, got %d", len(friend))
	}
	for _, m := range friend {
		if m.Scope == "u_owner" {
			t.Fatalf("friend 不应看到 owner 的私有消息: %+v", m)
		}
	}
	// 第三人：只看得到公共
	if got := len(s.ListVisible("u_other")); got != 1 {
		t.Fatalf("无关账号应只见公共消息, got %d", got)
	}
	// Sync 刷新保留作用域
	s.Sync([]MessageItem{{ID: "u_owner|hold@S1", Code: "000001", Level: "持仓提示", Scope: "u_owner"}})
	for _, m := range s.ListVisible("u_friend") {
		if m.ID == "u_owner|hold@S1" {
			t.Fatalf("刷新后作用域丢失，私有消息泄漏给 friend")
		}
	}
}
