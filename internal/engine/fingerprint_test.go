// Package engine 核心引擎：信号生产、打分池、板块传播、持仓退出、通知推送与 QMT 自动交易的 orchestration。
package engine

import (
	"testing"

	"quant-trading-v2/internal/config"
)

// memKV 极简 KVStore 实现，仅供本测试构造 per-user 覆盖。
type memKV struct {
	m map[string]map[string]string
}

// SetConfig 实现 KVStore 接口：按 (userID, key) 写入配置值（首次访问惰性初始化双层 map）。
func (k *memKV) SetConfig(userID, key, value string) error {
	if k.m == nil {
		k.m = map[string]map[string]string{}
	}
	if k.m[userID] == nil {
		k.m[userID] = map[string]string{}
	}
	k.m[userID][key] = value
	return nil
}

// GetConfig 获取配置（KV方法）。
func (k *memKV) GetConfig(userID, key string) (string, bool) {
	if k.m == nil {
		return "", false
	}
	v, ok := k.m[userID][key]
	return v, ok
}

// TestEngineFingerprintPerUserD1ATR §P1-C：账号级 D1/ATR 差异必须反映到指纹，
// 否则两账号共享引擎导致战法结果串味。
func TestEngineFingerprintPerUserD1ATR(t *testing.T) {
	mgr := config.NewManager("")
	mgr.SetStore(&memKV{})

	// 两账号设置不同的 D1 事件规则（账号级可覆盖项，原指纹漏掉 → 串味）。
	d1A := &config.D1Config{Rules: []config.D1Rule{{Direction: "利好", Score: 5}}}
	mgr.SetD1ConfigFor("userA", d1A)
	d1B := &config.D1Config{Rules: []config.D1Rule{{Direction: "利空", Score: 5}}}
	mgr.SetD1ConfigFor("userB", d1B)

	r := NewRegistry(EngineOptions{CfgMgr: mgr})
	fpA := r.fingerprint("userA")
	fpB := r.fingerprint("userB")
	if fpA == fpB {
		t.Fatalf("D1 不同的账号不应共享指纹: A=%s B=%s", fpA, fpB)
	}

	// 同账号稳定：两次计算指纹一致。
	if r.fingerprint("userA") != fpA {
		t.Fatal("同账号指纹应稳定")
	}

	// 无覆盖的两账号（D1/ATR 均为全局默认）共享指纹——反向验证隔离是精准的。
	fpX := r.fingerprint("userX")
	fpY := r.fingerprint("userY")
	if fpX != fpY {
		t.Fatalf("无覆盖账号应共享指纹: X=%s Y=%s", fpX, fpY)
	}
	if fpX == fpA {
		t.Fatal("有覆盖账号不应与默认账号共享指纹")
	}
}
