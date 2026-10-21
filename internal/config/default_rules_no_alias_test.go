// default_rules_no_alias_test.go — §DEFECT-CLONE（2026-10-21）：出厂默认表不得被实例写穿。
//
// 锤的形态：NewManager / NewManagerWithRules(nil) 曾直接把进程级全局 `DefaultRules` 指针存进
// Manager，而全部 setter 是 `m.rules.<段> = next` 这种**字段重绑**——配置文件还不存在时
// Load() 原样保留这个别名，于是第一次配置写就把"出厂默认表"本身改掉：同进程里后建的
// Manager 把自己的起点认成"别人刚写的值"。指纹是「单跑各自绿、串跑必红」（本批
// internal/server 的 regime 用例就是这么撞上的：前一条写 Red 权重 0.25，后一条读到的
// "默认"就成了 0.25）。
//
// 三条判据分工（缺一漏一侧）：
//   - 指针身份：构造函数不得把全局表交出去（直接钉修法，改回别名立刻红）。注意 Get() 返回
//     的是内部 *Rules 本身，调用方拿到地址就能就地改，所以身份检查与行为检查不是重复，
//     是「实装」与「效果」两维；
//   - 写侧自证：A 写 Red 0.25 之后 A 自己必须读到 0.25，证明这次写真的落到了实例、不是空转；
//   - 读侧隔离：新建的 B 必须仍读到出厂 0.30。只测 B 会把「写入静默失败」读成假绿，
//     只测 A 会漏掉本缺陷本身，故两条并列。
//
// English: the factory rule table must never be aliased into a Manager — a write on one
// instance used to mutate the process-global defaults and leak into every later manager.
package config

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// newFreshManager 构造「配置文件尚不存在」的 Manager——这正是别名事故的前提：
// Load() 读不到文件时不会覆盖 rules 字段，构造函数塞进去什么指针，实例就长期用什么指针。
// 现网 config.json 恒存在，因此这条只在空路径夹具上暴露（现网暴露的是测试串跑）。
// English: a manager whose config file does not exist yet — the exact precondition of the alias bug.
func newFreshManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(filepath.Join(t.TempDir(), "config.json"))
}

// TestNewManagerDoesNotAliasDefaultRules 构造函数不得把进程级 DefaultRules 交出去。
// English: constructors must hand each manager its own factory-rules copy.
func TestNewManagerDoesNotAliasDefaultRules(t *testing.T) {
	if m := newFreshManager(t); m.Get() == DefaultRules {
		t.Fatal("NewManager 仍与进程级 DefaultRules 同指针：实例配置写会改写出厂默认表")
	}
	if m := NewManagerWithRules(nil); m.Get() == DefaultRules {
		t.Fatal("NewManagerWithRules(nil) 仍与 DefaultRules 同指针：同上")
	}
}

// TestInstanceWriteKeepsFactoryDefaults 单实例的配置写不得污染出厂默认，也不得静默不落。
// 走 MergeSizingConfig（本批新增的稀疏 merge 通道，字段重绑形态与其余 9 处 setter 同族）。
// English: a per-instance merge must land on that instance only — never on the factory defaults.
func TestInstanceWriteKeepsFactoryDefaults(t *testing.T) {
	a := newFreshManager(t)
	if got := a.GetSizingConfig().TierScales["Red"]; got != 0.30 {
		t.Fatalf("出厂 Red 权重应为 0.30，got %v（出厂默认表已被更早的用例写穿）", got)
	}
	if _, err := a.MergeSizingConfig(map[string]json.RawMessage{
		"tier_scales": json.RawMessage(`{"Red": 0.25}`),
	}); err != nil {
		t.Fatalf("稀疏 merge 失败: %v", err)
	}
	// 写侧自证：这次变更确实落到了 A（否则下面的"干净"是空转假绿）
	if got := a.GetSizingConfig().TierScales["Red"]; got != 0.25 {
		t.Fatalf("A 应读到本人写入的 0.25，got %v（写入静默失败）", got)
	}
	// 另一实例必须仍是出厂值——本缺陷的判据
	if got := newFreshManager(t).GetSizingConfig().TierScales["Red"]; got != 0.30 {
		t.Fatalf("出厂默认被实例写穿：新建 Manager 读到 %v（应为 0.30）", got)
	}
	// 默认函数本身也不得被带跑（引擎装配 Regime 时读的是这条口径）
	if got := DefaultSizingConfig().TierScales["Red"]; got != 0.30 {
		t.Fatalf("DefaultSizingConfig 读数被污染，got %v", got)
	}
}
