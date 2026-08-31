// qmt_global_no_mutate_test.go 账号级 QMT 配置保存的"全局不改写"回归测试。
// 覆盖 §全局指针副作用 bug：无账号级覆盖时 userRules 曾返回全局地址，SetQMTConfigFor
// 会误改全局配置。文件头即注册一个内存 KVStore，供测试构造 per-user 覆盖。
package config

import "testing"

// memStore 极简 KVStore 实现，仅供单测构造 per-user 覆盖。
type memStore struct{ m map[string]map[string]string }

// SetConfig 按 用户ID+key 写一条配置到内存 map（懒初始化两级 map）。
func (k *memStore) SetConfig(u, key, v string) error {
	if k.m == nil {
		k.m = map[string]map[string]string{}
	}
	if k.m[u] == nil {
		k.m[u] = map[string]string{}
	}
	k.m[u][key] = v
	return nil
}

// GetConfig 按 用户ID+key 读一条配置；不存在返回 ("", false)。
func (k *memStore) GetConfig(u, key string) (string, bool) {
	if k.m == nil {
		return "", false
	}
	v, ok := k.m[u][key]
	return v, ok
}

// TestSetQMTConfigForDoesNotMutateGlobal 账号级保存不应副作用改写全局 m.Rules.QMT。
// 这是此前"全局指针副作用"bug 的回归：无账号级覆盖时 userRules 返回全局地址，
// SetQMTConfigFor 会误改全局，导致 Watch/Load 前后行为不一致。
// English: per-user save must not mutate the global m.Rules.QMT (global-pointer side-effect regression).
func TestSetQMTConfigForDoesNotMutateGlobal(t *testing.T) {
	mgr := NewManager("")
	mgr.SetStore(&memStore{})
	if mgr.Get().QMT.Enabled {
		t.Fatal("初始全局 QMT 应为 false")
	}
	mgr.SetQMTConfigFor("u1", &QMTConfig{Enabled: true})
	// 全局不应被改写
	if mgr.Get().QMT.Enabled {
		t.Fatal("账号级保存不应改写全局 m.Rules.QMT（全局指针副作用回归）")
	}
	// 账号级读取应为 true
	if !mgr.GetQMTConfigFor("u1").Enabled {
		t.Fatal("账号级 u1 读取应为 true")
	}
	// 另一账号不应继承 u1 的开启状态（隔离性）
	if mgr.GetQMTConfigFor("u2").Enabled {
		t.Fatal("u2 不应继承 u1 的开启状态")
	}
}
