// pending_queue_test.go — §WMQ-4（20260917）：QMT 待生效队列状态机的回归测试。
// 此前 QueueConfigUpdate / ApplyPendingConfig（executor Noop↔QMTClient 重建）这一资损敏感
// 状态机在 *_test.go 中零覆盖（docs/BUGFIX_WATCHLIST_20260917.md 缺口#3）。
// 锁定四个分支：
//  1. 队列空 → ApplyPendingConfig 返回 false（不空转重建）；
//  2. Noop + 停用配置入队 → 应用后仍 Noop（不误切网关客户端）；
//  3. Noop + 开启实盘配置入队 → 应用后 executor 切为 QMTClient 且 cfg 生效；
//  4. 消费一次后队列清空 → 再次应用返回 false；停用配置入队后 executor 回退 Noop。
package trading

import (
	"testing"

	"quant-trading-v2/internal/config"
)

// qmtCfg 构造最小合法 QMT 配置。
func qmtCfg(enabled bool, gateway string) config.QMTConfig {
	return config.QMTConfig{
		Enabled:    enabled,
		GatewayURL: gateway,
		Token:      "uat-token",
		Mode:       "auto",
	}
}

// execIsClient 判断控制器当前 executor 是否为 QMTClient。
// English: reports whether the controller's active executor is a real-gateway client.
func (c *Controller) execIsClient() bool {
	_, ok := c.exec.Load().(execHolder).e.(*QMTClient)
	return ok
}

func TestApplyPendingConfig_EmptyQueueNotApplied(t *testing.T) {
	c := NewController(NoopExecutor{}, nil, "u1", qmtCfg(false, ""), nil)
	if c.ApplyPendingConfig() {
		t.Fatalf("空队列不应返回 true（会误导考核轮空转）")
	}
	if c.execIsClient() {
		t.Fatalf("空队列后 executor 不得变化")
	}
}

func TestApplyPendingConfig_DisabledStaysNoop(t *testing.T) {
	c := NewController(NoopExecutor{}, nil, "u1", qmtCfg(false, ""), nil)
	c.QueueConfigUpdate(qmtCfg(false, "http://127.0.0.1:8789"))
	if !c.ApplyPendingConfig() {
		t.Fatalf("入队后应可应用")
	}
	if c.execIsClient() {
		t.Fatalf("停用配置不得切换为网关客户端")
	}
	if c.Config().Enabled {
		t.Fatalf("应用后 cfg 应为停用语义")
	}
	if c.ApplyPendingConfig() {
		t.Fatalf("消费一次后队列应清空")
	}
}

func TestApplyPendingConfig_EnableSwitchesToClient(t *testing.T) {
	// 构建期 disabled + URL 为空 → 固化 Noop（历史"executor 固化 bug"的初始形态）。
	c := NewController(NoopExecutor{}, nil, "u1", qmtCfg(false, ""), nil)
	// 休市时段配置变更：只入队不立即生效。
	c.QueueConfigUpdate(qmtCfg(true, "http://127.0.0.1:18789"))
	if c.execIsClient() {
		t.Fatalf("未应用前 executor 不得切换（QueueConfigUpdate 只记录）")
	}
	// 交易时段 scoreCycle 消费：切换 QMTClient。
	if !c.ApplyPendingConfig() {
		t.Fatalf("待生效配置应可应用")
	}
	if !c.execIsClient() {
		t.Fatalf("开启配置应用后 executor 应切换为 QMTClient")
	}
	if got := c.Config(); got.Enabled != true || got.GatewayURL != "http://127.0.0.1:18789" {
		t.Fatalf("应用后 cfg 未生效: enabled=%v url=%q", got.Enabled, got.GatewayURL)
	}
	// 停用配置入队 → 下一个交易时段回退 Noop。
	c.QueueConfigUpdate(qmtCfg(false, ""))
	if !c.ApplyPendingConfig() {
		t.Fatalf("停用配置入队后应可应用")
	}
	if c.execIsClient() {
		t.Fatalf("停用配置应用后 executor 应回退 Noop")
	}
}
