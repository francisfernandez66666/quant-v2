package sector_agent

import "testing"

func TestIsLeader(t *testing.T) {
	cases := []struct {
		name string
		l    LinkageLeader
		want bool
	}{
		{"强龙头", LinkageLeader{SealRatio: 5, BoardHeight: 3}, true},
		{"封单不足", LinkageLeader{SealRatio: 1, BoardHeight: 3}, false},
		{"连板不足", LinkageLeader{SealRatio: 5, BoardHeight: 1}, false},
		{"双不足", LinkageLeader{SealRatio: 1, BoardHeight: 1}, false},
	}
	for _, tc := range cases {
		if got := IsLeader(tc.l); got != tc.want {
			t.Errorf("%s: want %v got %v", tc.name, tc.want, got)
		}
	}
}

func TestLeaderStrength(t *testing.T) {
	if got := LeaderStrength(LinkageLeader{SealRatio: 1, BoardHeight: 3}); got != 0 {
		t.Errorf("non-leader strength must be 0, got %v", got)
	}
	got := LeaderStrength(LinkageLeader{SealRatio: 5, BoardHeight: 3})
	if got <= 0 || got > 10 {
		t.Errorf("leader strength out of range, got %.2f", got)
	}
}

func TestFindLinkageCandidates(t *testing.T) {
	ld := LinkageLeader{Code: "A", Name: "龙头A", Sector: "X", SealRatio: 6, BoardHeight: 3}
	consti := map[string][]LinkageCandidate{
		"X": {
			{Code: "B", Name: "龙二B", Turnover: 8},
			{Code: "C", Name: "龙三C", Turnover: 9},
			{Code: "D", Name: "死水D", Turnover: 0.5}, // 换手不足剔除
		},
	}
	flow := map[string]float64{"X": 1e8}

	got := FindLinkageCandidates([]LinkageLeader{ld}, consti, flow)
	if len(got) != 2 {
		t.Fatalf("want 2 candidates (B,C), got %d: %+v", len(got), got)
	}
	// 降序
	if got[0].ConfirmScore < got[1].ConfirmScore {
		t.Errorf("must be sorted desc, got %+.2f %+.2f", got[0].ConfirmScore, got[1].ConfirmScore)
	}

	// 板块无净流入 → 无候选
	if got2 := FindLinkageCandidates([]LinkageLeader{ld}, consti, map[string]float64{"X": -1}); len(got2) != 0 {
		t.Errorf("negative flow must block candidates, got %+v", got2)
	}
	// 非龙头 → 无候选
	if got3 := FindLinkageCandidates([]LinkageLeader{{Code: "A", Sector: "X", SealRatio: 1, BoardHeight: 1}}, consti, flow); len(got3) != 0 {
		t.Errorf("non-leader must not link, got %+v", got3)
	}
	// 候选自己也是涨停龙头 → 跳过
	if got4 := FindLinkageCandidates([]LinkageLeader{{Code: "A", Sector: "X", SealRatio: 6, BoardHeight: 3},
		{Code: "B", Sector: "X", SealRatio: 6, BoardHeight: 2}}, consti, flow); len(got4) != 1 {
		t.Errorf("leader-B itself must not appear as candidate, got %+v", got4)
	}
}