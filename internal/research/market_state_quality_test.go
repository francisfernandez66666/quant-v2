// market_state_quality_test.go — §BUG-STATE（2026-10-27）状态机的两道人造安全阀与第六票。
//
// 起因：现网 10-27 当天广度（上涨占比）与指数日线斜率都取不到，状态机只剩"连板高度 + 炸板率"
// 两票就给出了 bull、仓位档 60%；同一时段大盘在跌、下午急杀。三条锁：
//  1. 有效票 < MinVotes(4/6) ⇒ 结论降级 range（并把质量 2/6 报出来）；
//  2. 当日指数跌幅达急杀线（默认 −0.8%）⇒ 最多 range，不许按牛定档；
//  3. 第六票（当日指数涨跌幅）真的参与投票——这是此前完全缺失的"当日价格"输入。
//
// English: §BUG-STATE locks — quality gate, intraday-plunge downgrade, and the new index-change vote.
package research

import (
	"math"
	"testing"
	"time"
)

// qSnap 构造一帧快照；NaN 参数用 math.NaN() 显式传入（弃权）。
func qSnap(limitUp, ladder int, breakRate, up, ma20, ma60, idxPct float64) StateSnapshot {
	return StateSnapshot{
		LimitUpCount: limitUp, LadderHeight: ladder, BreakRate: breakRate,
		UpRatio: up, IndexMA20Slope: ma20, IndexMA60Slope: ma60, IndexChangePct: idxPct,
	}
}

// TestStateQualityGateDowngradesToRange 两票就下 bull 的形态必须被安全阀拦住（现网 10-27 实况）。
func TestStateQualityGateDowngradesToRange(t *testing.T) {
	sn := qSnap(50, 6, 10, math.NaN(), math.NaN(), math.NaN(), math.NaN())
	raw, present, total := ClassifyWithQuality(sn, StateConfig{})
	if raw != StateBull || present != 2 || total != 6 {
		t.Fatalf("原始分类应 bull 且只用 2/6 票，got state=%s present=%d/%d", raw, present, total)
	}
	tr := NewStateTracker(StateConfig{})
	if got := tr.Observe(sn, time.Now()); got != StateRange {
		t.Fatalf("有效票 2/6 < 4 ⇒ 必须降级 range，got %s", got)
	}
	if p, tot := tr.Quality(); p != 2 || tot != 6 {
		t.Fatalf("研判质量读数应 2/6，got %d/%d", p, tot)
	}
}

// TestStatePlungeDowngradesBull 六票齐全但当日指数急杀（≤ −0.8%）⇒ 最多 range。
func TestStatePlungeDowngradesBull(t *testing.T) {
	sn := qSnap(100, 6, 10, 0.70, 0.01, 0.005, -1.2)
	if raw, present, _ := ClassifyWithQuality(sn, StateConfig{}); raw != StateBull || present != 6 {
		t.Fatalf("该帧原始分类应为 bull 且 6/6 有效，got %s present=%d", raw, present)
	}
	tr := NewStateTracker(StateConfig{})
	if got := tr.Observe(sn, time.Now()); got != StateRange {
		t.Fatalf("指数 −1.2%% 触发急杀降档，应最多 range，got %s", got)
	}
}

// TestStateIndexChangeVotesBear 第六票独立生效：其余五票弃权、指数 −1.5% ⇒ 熊。
func TestStateIndexChangeVotesBear(t *testing.T) {
	sn := qSnap(50, 3, math.NaN(), math.NaN(), math.NaN(), math.NaN(), -1.5)
	st, present, _ := ClassifyWithQuality(sn, StateConfig{})
	if st != StateBear || present != 1 {
		t.Fatalf("指数票应单独投熊，got state=%s present=%d", st, present)
	}
}

// TestStateIndexChangeNeutralAbstains ±1% 以内算中性（弃权），不许把平盘当方向。
func TestStateIndexChangeNeutralAbstains(t *testing.T) {
	sn := qSnap(50, 3, math.NaN(), math.NaN(), math.NaN(), math.NaN(), -0.4)
	if _, present, _ := ClassifyWithQuality(sn, StateConfig{}); present != 0 {
		t.Fatalf("±1%% 内应弃权（有效票 0），got %d", present)
	}
}
