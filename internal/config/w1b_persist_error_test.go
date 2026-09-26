// §0926E2E-W1B（2026-09-26 全量审计批）：per-user 配置持久化错误链行为用例。
// 缺陷原文：saveUserRules/SetD1/SetLongShort 对 SetConfig 出错只 log 不返回，HTTP 端点
// 照回 200"已保存"——磁盘没落、重启回退旧值，属"降级报成功"家族。
// 本文件锁三件事：①写失败如实返回 error（含底层错误原文）；②写成功但复读不匹配也返回
// error（假绿反证：store 静默丢数据的形态必须被写后复读锤住）；③成功路径往返一致不误伤。
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// w1bFakeStore 可控 KVStore：failWrite=SetConfig 直接报错；dropWrite=写成功但 GetConfig
// 返回另一份内容（模拟"落盘中途被吞/串扰"）；正常形态=内存 map 忠实存取。
type w1bFakeStore struct {
	data      map[string]string // key = userID+"\x00"+key
	failWrite bool
	dropWrite bool
}

func newW1bFakeStore() *w1bFakeStore {
	return &w1bFakeStore{data: map[string]string{}}
}

func (f *w1bFakeStore) SetConfig(userID, key, value string) error {
	if f.failWrite {
		return errors.New("sqlite: disk I/O error（w1b 注入）")
	}
	if f.dropWrite {
		f.data[userID+"\x00"+key] = "{\"qmt\":{\"enabled\":false},\"stale\":true}" // 永远不是刚写的那份
		return nil
	}
	f.data[userID+"\x00"+key] = value
	return nil
}

func (f *w1bFakeStore) GetConfig(userID, key string) (string, bool) {
	v, ok := f.data[userID+"\x00"+key]
	return v, ok
}

// w1bManager 装配带 fake store 的 Manager（operatorID 留空→ownerOf 回落账号自身）。
func w1bManager(store KVStore) *Manager {
	m := NewManager(filepath.Join(os.TempDir(), "w1b-unused.json"))
	m.SetStore(store)
	return m
}

// TestW1bSettersPropagateWriteFailure 六个 per-user setter 在 store 写失败时全部返回 error，
// 且错误链里能看到底层原始报错（不是只 log 后照回 nil 的旧形态）。
func TestW1bSettersPropagateWriteFailure(t *testing.T) {
	cases := []struct {
		name string
		call func(m *Manager) error
	}{
		{"SetQMTConfigFor", func(m *Manager) error {
			return m.SetQMTConfigFor("u1", &QMTConfig{Enabled: true, Mode: "manual"})
		}},
		{"SetStrategyConfigFor", func(m *Manager) error {
			c := defaultStrategyConfig()
			return m.SetStrategyConfigFor("u1", &c)
		}},
		{"SetLLMConfigFor", func(m *Manager) error {
			return m.SetLLMConfigFor("u1", &LLMConfig{APIURL: "https://x/v1", Model: "m"})
		}},
		{"SetD1ConfigFor", func(m *Manager) error {
			return m.SetD1ConfigFor("u1", &D1Config{Rules: []D1Rule{{Direction: "利好", Score: 0.5}}})
		}},
		{"SetLongShortConfigFor", func(m *Manager) error {
			return m.SetLongShortConfigFor("u1", LongShortConfig{LongEnabled: true})
		}},
		{"SetPaperStrategyFor", func(m *Manager) error {
			return m.SetPaperStrategyFor("u1", []string{"dragon"}, nil)
		}},
		{"SetPaperConfigFor", func(m *Manager) error {
			return m.SetPaperConfigFor("u1", func(p *PaperConfig) { p.FixedAmount = 100 })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newW1bFakeStore()
			st.failWrite = true
			err := tc.call(w1bManager(st))
			if err == nil {
				t.Fatal("写失败必须返回 error（旧形态吞错只 log＝前端假'已保存'）")
			}
			if !containsStr(err.Error(), "w1b 注入") {
				t.Errorf("错误链必须带出底层原始报错，got: %v", err)
			}
		})
	}
}

// TestW1bReadBackMismatchIsError 假绿反证：SetConfig 回 nil 但复读内容不是刚写入的那份
// （静默丢盘/串扰形态）——写后复读自证必须锤红，绝不因"写调用没报错"就放行。
func TestW1bReadBackMismatchIsError(t *testing.T) {
	st := newW1bFakeStore()
	st.dropWrite = true
	m := w1bManager(st)
	err := m.SetQMTConfigFor("u1", &QMTConfig{Enabled: true, DailyBudgetAmount: 999})
	if err == nil {
		t.Fatal("写成功但复读不匹配必须返回 error")
	}
	if !containsStr(err.Error(), "复读") {
		t.Errorf("错误应指明复读不匹配，got: %v", err)
	}
	// 反向核验存储层确实留下了"不是我们写的"内容（证明 fake 生效，用例不是自证空转）
	raw, ok := st.GetConfig("u1", perUserKey)
	if !ok || containsStr(raw, "999") {
		t.Errorf("fake 存储形态异常: ok=%v raw=%s", ok, raw)
	}
}

// TestW1bSuccessPathRoundTrip 成功路径不误伤：写+复读一致返回 nil，且读取口径拿回刚存的值。
func TestW1bSuccessPathRoundTrip(t *testing.T) {
	st := newW1bFakeStore()
	m := w1bManager(st)
	if err := m.SetQMTConfigFor("u1", &QMTConfig{Enabled: true, Mode: "manual", DailyBudgetAmount: 12345}); err != nil {
		t.Fatalf("成功路径不应报错: %v", err)
	}
	got := m.GetQMTConfigFor("u1")
	if got == nil || !got.Enabled || got.DailyBudgetAmount != 12345 {
		t.Fatalf("复读后读取口径未拿到刚持久化的值: %+v", got)
	}
	// D1/长-short 独立键同样走自证出口
	if err := m.SetD1ConfigFor("u1", &D1Config{Rules: []D1Rule{{Direction: "利好", Score: 0.7}}}); err != nil {
		t.Fatalf("D1 成功路径不应报错: %v", err)
	}
	if err := m.SetLongShortConfigFor("u1", LongShortConfig{LongEnabled: true, ShortEnabled: true}); err != nil {
		t.Fatalf("长短开关成功路径不应报错: %v", err)
	}
	var ls LongShortConfig
	if raw, ok := st.GetConfig("u1", perUserLongShortKey); ok {
		_ = json.Unmarshal([]byte(raw), &ls)
	}
	if !ls.ShortEnabled {
		t.Errorf("长-short 键复读应含 short_enabled=true: %+v", ls)
	}
}

// TestW1bSaveReturnsErrorOnUnwritablePath 全局回退分支的 Save() 同样接错：
// 目标目录只读→AtomicWrite 失败→返回 error（旧写法吞错后 fallback setter 照回 nil）。
func TestW1bSaveReturnsErrorOnUnwritablePath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限约束，跳过")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatalf("chmod 失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	m := NewManager(filepath.Join(ro, "config.json"))
	err := m.Save()
	if err == nil {
		t.Fatal("只读目录下 Save 必须返回 error")
	}
	// fallback（无 store）setter 把 Save 的错误如实透出
	if err2 := m.SetQMTConfigFor("u1", &QMTConfig{Enabled: true}); err2 == nil {
		t.Fatal("无 store 回退分支 Save 失败时 setter 必须返回 error")
	} else if !containsStr(err2.Error(), "写入失败") {
		t.Errorf("错误应指向文件写入失败，got: %v", err2)
	}
}

// containsStr 子串判定小包装（统一用例内错误文案核对口径）。
func containsStr(s, sub string) bool { return strings.Contains(s, sub) }

// 编译期钉住 fake 满足接口（接口面漂移当场红）。
var _ KVStore = (*w1bFakeStore)(nil)
