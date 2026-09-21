// circuit_breaker_runstart_test.go — §CB-RUNSTART 熔断失联窗口语义回归测试：
// lastFailAt 为「本轮连续失联起点」——探测成功必须清零；连续失败满 miss 窗口必须熔断。
// 背景（2026-09-21 现网双缺陷）：旧实现把 lastFailAt 记成"最近一次失败时间"且成功不清零，
// ① 被成功隔开的两次失败也能凑满窗口 → 盘前桥爆发式心跳误熔（09:10:27 实录）；
// ② 探测周期恒为 miss/2，相邻失败间隔永远 < miss → 真断线反而永不熔断。
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// healthyStub 恒报网关健康的执行器桩（其余方法继承 NoopExecutor）。
type healthyStub struct{ NoopExecutor }

func (healthyStub) Health() (bool, error) { return true, nil }

// TestBreakerFailRunStart 验证失联窗口的开窗/清零/持续失败熔断三种语义。
func TestBreakerFailRunStart(t *testing.T) {
	db := testDB(t)
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = "http://127.0.0.1:1" // executor 全部桩替换，不会真发请求
	cfg.MissHeartbeatSec = 2

	newCtrl := func() *Controller { return NewController(healthyStub{}, db, "u_1", cfg, nil) }
	// probe 注入本轮探测：解锁节流（lastHealthAt 清零）→ 换 executor → 跑一次 HealthCheck。
	probe := func(c *Controller, exec Executor) {
		c.mu.Lock()
		c.lastHealthAt = time.Time{}
		c.mu.Unlock()
		c.exec.Store(execHolder{exec})
		c.HealthCheck()
	}
	failAt := func(c *Controller) bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return !c.lastFailAt.IsZero()
	}

	t.Run("首个失败只开窗不熔断", func(t *testing.T) {
		c := newCtrl()
		probe(c, failingExecutor{})
		if c.Tripped() {
			t.Fatal("首个失败不应熔断（只记失联起点）")
		}
		if !failAt(c) {
			t.Fatal("首个失败应落下失联窗口起点")
		}
	})

	t.Run("成功探测清零窗口_被成功隔开的失败不累计", func(t *testing.T) {
		c := newCtrl()
		probe(c, failingExecutor{}) // fail #1：开窗 t0
		probe(c, healthyStub{})     // 成功：窗口必须清零
		if failAt(c) {
			t.Fatal("探测成功后 lastFailAt 应清零")
		}
		time.Sleep(2100 * time.Millisecond) // 若窗口未清零，此间隔已超 miss → 旧代码在此误熔
		probe(c, failingExecutor{})         // fail #2：重新开窗
		if c.Tripped() {
			t.Fatal("成功之后的孤立失败不得沿用旧窗口熔断（§CB-RUNSTART 误熔回归）")
		}
	})

	t.Run("持续失败满窗口必须熔断", func(t *testing.T) {
		c := newCtrl()
		probe(c, failingExecutor{}) // 开窗
		time.Sleep(2100 * time.Millisecond)
		probe(c, failingExecutor{}) // 连续失败 ≥ miss → 旧代码因"最近失败间隔=探测周期"永不熔
		if !c.Tripped() {
			t.Fatal("持续失联超过 miss 窗口必须熔断（真断线漏熔回归）")
		}
		// 恢复：成功探测自动解熔
		probe(c, healthyStub{})
		if c.Tripped() {
			t.Fatal("恢复后应自动解熔")
		}
	})
}
