package data

import (
	"errors"
	"testing"
	"time"
)

// §N-12a：四档分类——no_data 不得被当成源故障，源错误/拒收也不得被洗成“标的无数据”。
func TestClassifyMinuteLeg(t *testing.T) {
	cases := []struct {
		name string
		err  error
		n    int
		want string
	}{
		{"合法信封零行", ErrNoMinuteData, 0, "no_data"},
		{"包裹后的 no_data", errors.Join(ErrNoMinuteData, errors.New("x")), 0, "no_data"},
		{"有数但被拒收", nil, 3, "rejected"},
		{"零行无错误", nil, 0, "no_data"},
		{"传输错误", errors.New("timeout"), 0, "source_error"},
	}
	for _, c := range cases {
		if got := classifyMinuteLeg(c.err, c.n); got != c.want {
			t.Errorf("%s: classifyMinuteLeg=%s want=%s", c.name, got, c.want)
		}
	}
}

// §N-12a：聚合优先级——有源错误 ⇒ sources_unavailable；否则有 no_data ⇒ no_data。
func TestMinuteChainErrorPriority(t *testing.T) {
	legs := []string{"新浪=无该标的数据"}
	if err := minuteChainError("300023", legs, false, true); !errors.Is(err, ErrNoMinuteData) {
		t.Fatalf("只有 no_data 时应归 ErrNoMinuteData, got %v", err)
	}
	legs = append(legs, "腾讯=timeout")
	if err := minuteChainError("300023", legs, true, true); !errors.Is(err, ErrMinuteSourcesUnavailable) {
		t.Fatalf("任一源错误必须翻成 ErrMinuteSourcesUnavailable, got %v", err)
	}
	if err := minuteChainError("300023", []string{"同花顺=熔断中（未拨上游）"}, false, false); !errors.Is(err, ErrMinuteSourcesUnavailable) {
		t.Fatalf("全部未拨/未知应归 sources_unavailable, got %v", err)
	}
}

// §N-12b：同花顺软失败必须按“不同代码”计数——单票重复失败不得熔断整源。
func TestTHSSoftFailureThreshold(t *testing.T) {
	dc := &DataCoordinator{}
	for _, code := range []string{"300001", "300002", "300003", "300004"} {
		dc.noteThsSoftFailure(code)
	}
	if !dc.thsDeadlines[thsOpMinute].IsZero() {
		t.Fatalf("4 个不同代码不应熔断")
	}
	dc.noteThsSoftFailure("300005")
	if dc.thsDeadlines[thsOpMinute].Before(time.Now()) {
		t.Fatalf("5 个不同代码应熔断 60s，deadline=%v", dc.thsDeadlines[thsOpMinute])
	}

	same := &DataCoordinator{}
	for i := 0; i < 5; i++ {
		same.noteThsSoftFailure("300023")
	}
	if !same.thsDeadlines[thsOpMinute].IsZero() {
		t.Fatalf("同一代码重复失败不得熔断整源（一只票连坐全部票）")
	}
}
