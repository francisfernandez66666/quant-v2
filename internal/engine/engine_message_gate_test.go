// engine_message_gate_test.go — §GAP-CARD-TIER + §STRATEGY-SWITCH-GATE（2026-11-09 owner 裁决）。
//
// 两件事的锁：
//  1. confidenceTier：提醒卡的置信档位词映射（高/中/低），替代百分比——watch 卡上的
//     "置信度:100%" 会被读成强推荐，档位词只表达"该多看一眼"。
//  2. syncMessages 的战法开关闸（源形态锁）：非卖出方向的提醒卡必须过
//     AdmitStrategyName（与下单准入同一条白名单规则），卖出方向不拦。
//     为什么用源形态锁而不是行为锁：syncMessages 依赖 msgStore/qmtCtrl/marketAPI 全套
//     运行时，行为夹具的成本远超收益；而本仓已有源形态锁的先例
//     （test_internal_strategy_sandbox 的 sizing 调用点锁）。闸本体 AdmitStrategyName
//     的行为已由 signalctl 的 strategy_name_admit_test 单独钉死，这里只锁"接线存在"。
package engine

import (
	"os"
	"strings"
	"testing"
)

func TestConfidenceTier(t *testing.T) {
	cases := []struct {
		conf float64
		want string
	}{
		{1.0, "高"}, {0.8, "高"}, // 满分/边界：深跌预期差旧实现会顶到 1.0
		{0.79, "中"}, {0.5, "中"}, // 中档
		{0.49, "低"}, {0, "低"}, // 低档与零值
	}
	for _, c := range cases {
		if got := confidenceTier(c.conf); got != c.want {
			t.Errorf("confidenceTier(%v) = %q, want %q", c.conf, got, c.want)
		}
	}
}

func TestSyncMessagesEntryCardsPassStrategySwitch(t *testing.T) {
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("读 engine.go 失败: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (e *Engine) syncMessages(")
	if start < 0 {
		t.Fatal("找不到 syncMessages 函数")
	}
	end := strings.Index(body[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("syncMessages 后找不到下一个函数边界")
	}
	fn := body[start : start+end]
	// 锁 1：非卖出方向必须过白名单准入（与下单准入同一把）。
	if !strings.Contains(fn, "signalctl.AdmitStrategyName(e.liveSignalPolicy(), sig.Strategy)") {
		t.Fatal("syncMessages 的提醒卡没有过 AdmitStrategyName 白名单闸——未开开关的战法又会提醒交易")
	}
	// 锁 2：闸只拦非卖出方向（做空/卖出与风控提醒永远放行）。
	if !strings.Contains(fn, `direction != "做空"`) {
		t.Fatal("开关闸没有区分卖出方向——止损/风控提醒不许被开关关掉")
	}
	// 锁 3：卡面置信度必须用档位词，不得再出现百分比形态。
	if strings.Contains(fn, "置信度:%.0f%%") {
		t.Fatal("提醒卡仍印百分比置信度——watch 卡会被读成强推荐（§GAP-CARD-TIER）")
	}
	if !strings.Contains(fn, "confidenceTier(sig.Confidence)") {
		t.Fatal("卡面没有用 confidenceTier 档位词")
	}
}
