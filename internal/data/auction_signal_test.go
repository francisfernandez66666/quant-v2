package data

import "testing"

func TestAuctionStrengthScore(t *testing.T) {
	cases := []struct {
		name string
		it   HithinkAuctionItem
		want float64
	}{
		{
			name: "强抢筹",
			it: HithinkAuctionItem{
				AuctionVolumeRatio: 10, AuctionPct: 6, AuctionUnmatched: 0,
				AuctionVolume: 1e6, AuctionTurnoverPct: 1.2,
			},
		},
		{
			name: "零值",
			it:   HithinkAuctionItem{},
		},
		{
			name: "低开弱量",
			it: HithinkAuctionItem{
				AuctionVolumeRatio: 1, AuctionPct: -4, AuctionUnmatched: 2e5,
				AuctionVolume: 1e5, AuctionTurnoverPct: 0.1,
			},
		},
	}
	for _, tc := range cases {
		got := AuctionStrengthScore(tc.it)
		if got < 0 || got > 10 {
			t.Errorf("%s: out of range got %.2f", tc.name, got)
		}
	}
	// 单调性：抢筹量比越高、高开越大、换手越高 → 分数越高（未匹配为双向项）
	low := AuctionStrengthScore(HithinkAuctionItem{AuctionVolumeRatio: 1, AuctionPct: 0, AuctionTurnoverPct: 0.1})
	high := AuctionStrengthScore(HithinkAuctionItem{AuctionVolumeRatio: 15, AuctionPct: 8, AuctionTurnoverPct: 1.8})
	if high <= low {
		t.Errorf("monotonicity: high %.2f should exceed low %.2f", high, low)
	}
	// 零值 = 仅高开维度中点 0.5×0.30 = 0.15，math.Round(1.5)=2 → 显示 0.2
	zero := AuctionStrengthScore(HithinkAuctionItem{})
	if zero != 0.2 {
		t.Errorf("zero-value: want 0.2 got %.2f", zero)
	}
}

func TestSafediv(t *testing.T) {
	if safediv(3, 0) != 0 {
		t.Error("safediv by zero must be 0")
	}
	if safediv(3, 2) != 1.5 {
		t.Error("safediv wrong")
	}
}
