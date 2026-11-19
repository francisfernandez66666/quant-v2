// market_risk_maxpos_20261120_test.go — §FLOW-QUADRANT 的仓位档留痕锁（2026-11-20）。
//
// 为什么这条要单独咬：四象限的 posRel = 持仓占比 / 状态机目标档，而目标档这一列
// （market_risk_daily.max_pos_pct）在此之前**永远写 NULL**——写点只传了情绪/状态/风险档，
// 把同一把锁下已经读到的 lastMaxPosPct 漏掉了。于是 posRel 恒算不出来、象限天天退化成
// Neutral，「禁抄底」这条唯一的硬闸在现网等于不存在，而所有读数看起来都"正常"。
//
// 两半判据：
//  1. 行为：0/NaN（状态机未启用或未算出）必须落 NULL，绝不落 0%（0% 会被下游读成
//     "目标仓位为零"，那是伪造读数）；正常档位原样带出。
//  2. 接线：写点必须真的把这一列传进去，且 maxPosPct 与 mState 必须来自**同一把读锁**
//     （分开读会出现"熊市状态配牛市档位"的错配，而两列各自都合法）。
//
// 反证：把 `MaxPosPct: posPctPtr(maxPosPct)` 摘掉 → 本文件红；把 posPctPtr 改成
// `return nanPtr(x)`（0 变成合法 0%）→ 第一半红。
//
// English: locks the max-position tier into the daily risk record — posRel needs it, and a
// silently-NULL column disabled the only hard gate the flow quadrant owns.
package engine

import (
	"math"
	"strings"
	"testing"
)

// TestPosPctPtrAbstainsOnZeroAndNaN 「没有读数」必须落 NULL，不许落 0%。
func TestPosPctPtrAbstainsOnZeroAndNaN(t *testing.T) {
	if got := posPctPtr(0); got != nil {
		t.Fatalf("状态机关闭（0）应落 NULL，得 %v（0%% 会被读成目标仓位为零）", *got)
	}
	if got := posPctPtr(math.NaN()); got != nil {
		t.Fatalf("NaN 应落 NULL，得 %v", *got)
	}
	if got := posPctPtr(-0.3); got != nil {
		t.Fatalf("负档位应落 NULL，得 %v", *got)
	}
	ok := posPctPtr(0.35)
	if ok == nil || math.Abs(*ok-0.35) > 1e-9 {
		t.Fatalf("正常档位应原样带出，得 %v", ok)
	}
}

// TestRiskDailyRowCarriesMaxPosPct 接线锁：写点必须传这一列，且与 MarketState 同锁读。
func TestRiskDailyRowCarriesMaxPosPct(t *testing.T) {
	src := mustRead(t, "engine.go")

	if !strings.Contains(src, "MaxPosPct:    posPctPtr(maxPosPct)") {
		t.Fatal("§FLOW-QUADRANT：market_risk_daily 写点没传 MaxPosPct（posRel 恒 0，象限永远 Neutral）")
	}
	// 同一把读锁：mState 与 maxPosPct 必须在同一个 RLock/RUnlock 区间内取。
	start := strings.Index(src, "mState := e.lastMarketState")
	if start < 0 {
		t.Fatal("找不到 mState := e.lastMarketState（锚漂了＝这条锁没有牙）")
	}
	window := src[start:]
	if end := strings.Index(window, "RUnlock()"); end < 0 {
		t.Fatal("mState 之后没有 RUnlock（读锁配对找不到）")
	} else if !strings.Contains(window[:end], "maxPosPct := e.lastMaxPosPct") {
		t.Fatal("maxPosPct 与 mState 不在同一把读锁内取值（会出现熊市状态配牛市档位）")
	}
}
