package research

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cfg := StateConfig{}
	// 全面牛信号
	bn := StateSnapshot{LimitUpCount: 120, LadderHeight: 7, BreakRate: 10, UpRatio: 0.8, IndexMA20Slope: 1, IndexMA60Slope: 1}
	if c := Classify(bn, cfg); c != StateBull {
		t.Errorf("bull signals -> want bull got %s", c)
	}
	// 全面熊信号
	br := StateSnapshot{LimitUpCount: 5, LadderHeight: 1, BreakRate: 60, UpRatio: 0.15, IndexMA20Slope: -1, IndexMA60Slope: -1}
	if c := Classify(br, cfg); c != StateBear {
		t.Errorf("bear signals -> want bear got %s", c)
	}
	// 均衡（各信号弃权）→ range
	r := StateSnapshot{LimitUpCount: 50, LadderHeight: 3, BreakRate: 40, UpRatio: 0.5, IndexMA20Slope: 0, IndexMA60Slope: 0}
	if c := Classify(r, cfg); c != StateRange {
		t.Errorf("balanced -> want range got %s", c)
	}
	// 空快照：涨停家数=0 触发熊信号（保守语义：无数据/极弱盘按低仓位防守处理）。
	if c := Classify(StateSnapshot{}, cfg); c != StateBear {
		t.Errorf("empty snapshot -> want bear (conservative) got %s", c)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := StateConfig{}.norm()
	if cfg.MaxPosPct[StateBull] > 1 || cfg.MaxPosPct[StateBear] >= cfg.MaxPosPct[StateRange] {
		t.Errorf("max-pos ordering wrong: %+v", cfg.MaxPosPct)
	}
	if cfg.MinStayDays != 3 {
		t.Errorf("default min stay want 3 got %d", cfg.MinStayDays)
	}
}

func TestStateTrackerStaysInMinStay(t *testing.T) {
	tk := NewStateTracker(StateConfig{MinStayDays: 3})
	now := time.Date(2026, 9, 9, 9, 30, 0, 0, time.Local)
	// 首帧牛信号 → 立即切牛
	bull := StateSnapshot{LimitUpCount: 120, LadderHeight: 7, BreakRate: 10, UpRatio: 0.8, IndexMA20Slope: 1, IndexMA60Slope: 1}
	if s := tk.Observe(bull, now); s != StateBull {
		t.Fatalf("first frame should switch to bull, got %s", s)
	}
	// 次日熊信号 → 停留期未满(1天<3天) → 保持牛
	bear := StateSnapshot{LimitUpCount: 5, LadderHeight: 1, BreakRate: 60, UpRatio: 0.15, IndexMA20Slope: -1, IndexMA60Slope: -1}
	if s := tk.Observe(bear, now.Add(24*time.Hour)); s != StateBull {
		t.Errorf("within min-stay should keep bull, got %s", s)
	}
	// 第 4 天仍熊 → 停留期满 → 切熊
	if s := tk.Observe(bear, now.Add(4*24*time.Hour)); s != StateBear {
		t.Errorf("after min-stay should switch to bear, got %s", s)
	}
	// 停保留：切换后又要等停留期
	if s := tk.Observe(bull, now.Add(4*24*time.Hour+30*time.Minute)); s != StateBear {
		t.Errorf("again within stay keep bear, got %s", s)
	}
}

func TestStateTrackerMaxPos(t *testing.T) {
	tk := NewStateTracker(StateConfig{})
	if tk.MaxPosPct() <= 0 {
		t.Errorf("range max-pos should be positive, got %v", tk.MaxPosPct())
	}
}
