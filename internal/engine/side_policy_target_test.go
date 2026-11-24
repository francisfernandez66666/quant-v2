// side_policy_target_test.go — §SIDE-POLICY 战略目标乘子与 T 额度隔离锁。
package engine

import (
	"math"
	"testing"
)

func TestSidePolicyTargetMultiplierStrategicOnly(t *testing.T) {
	target, _, room, ok := fundsTargetCurRoomWithMultiplier(100000, 0.8, 1.0, 1.2, nil, nil, nil, tLiveBook{})
	if !ok || math.Abs(target-96000) > 1e-6 || math.Abs(room-96000) > 1e-6 {
		t.Fatalf("strategic target must be total×min(MS×TM,cap), got target=%.0f room=%.0f ok=%v", target, room, ok)
	}
	tTarget, _, tRoom, ok := fundsTargetCurRoom(100000, 0.8, 1.0, nil, nil, nil, tLiveBook{})
	if !ok || math.Abs(tTarget-80000) > 1e-6 || math.Abs(tRoom-80000) > 1e-6 {
		t.Fatalf("T wrapper must stay TM=1, got target=%.0f room=%.0f ok=%v", tTarget, tRoom, ok)
	}
}

func TestSidePolicyTargetMultiplierRespectsCeiling(t *testing.T) {
	target, _, _, _ := fundsTargetCurRoomWithMultiplier(100000, 0.9, 0.7, 1.15, nil, nil, nil, tLiveBook{})
	if math.Abs(target-70000) > 1e-6 {
		t.Fatalf("bull multiplier must not break ceiling, got %.0f", target)
	}
}

func TestDynBudgetTargetMultiplierDefaultsToOne(t *testing.T) {
	if got := (dynBudgetCtx{}).targetMultiplier(); got != 1 {
		t.Fatalf("missing TM must be neutral, got %.3f", got)
	}
	if got := (dynBudgetCtx{TargetMultiplier: 0.85}).targetMultiplier(); got != 0.85 {
		t.Fatalf("explicit TM must pass through, got %.3f", got)
	}
}
