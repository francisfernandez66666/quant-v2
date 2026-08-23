package data

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestHithinkEnvelopeAndErrors(t *testing.T) {
	// 成功路径：信封解析 + data 注入
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-api-key") == "" {
			t.Error("缺少 X-api-key 头")
		}
		w.Write([]byte(`{"code":0,"message":"success","data":{"timestamp":1,"total":1,"item":[{"thscode":"600519.SH","last_price":1272.83}]}}`))
	}))
	defer srv.Close()
	old := HithinkBaseURL
	HithinkBaseURL = srv.URL
	defer func() { HithinkBaseURL = old }()

	t.Setenv(HithinkAPIKeyEnv, "test-key")
	c, err := NewHithinkClient()
	if err != nil {
		t.Fatal(err)
	}
	c.limiter = newHithinkLimiter(1000) // 测试不限速

	snap, err := c.Snapshot([]string{"600519.SH"})
	if err != nil || len(snap.Item) != 1 || snap.Item[0].LastPrice != 1272.83 {
		t.Fatalf("快照解析异常: %+v err=%v", snap, err)
	}

	// 业务错误分类：4001/2001/2003
	cases := map[string]error{
		`{"code":4001,"message":"rate"}`: ErrHithinkRateLimited,
		`{"code":2001,"message":"auth"}`: ErrHithinkAuth,
		`{"code":2003,"message":"perm"}`: ErrHithinkForbidden,
	}
	for body, want := range cases {
		s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		HithinkBaseURL = s2.URL
		_, got := c.Snapshot([]string{"600519.SH"})
		s2.Close()
		if !errors.Is(got, want) {
			t.Fatalf("body=%s want=%v got=%v", body, want, got)
		}
	}
	HithinkBaseURL = old
}

func TestHithinkLimiterPenalize(t *testing.T) {
	l := newHithinkLimiter(1000)
	before := l.interval
	l.wait(true) // 惩罚：间隔翻倍且强制等待一次
	if l.interval <= before {
		t.Fatalf("惩罚后间隔应放大: %v -> %v", before, l.interval)
	}
	l.wait(false)
	if l.interval > 30*time.Second {
		t.Fatalf("退避上限 30s: %v", l.interval)
	}
}

func TestHithinkKeyMissing(t *testing.T) {
	os.Setenv(HithinkAPIKeyEnv, "")
	defer os.Unsetenv(HithinkAPIKeyEnv)
	if _, err := NewHithinkClient(); err == nil {
		t.Fatal("Key 缺失应返回错误")
	}
}

func TestAdjMultiplierDirection(t *testing.T) {
	// 纯现金分红：10派1 → ref=(10-0.1)=9.9 → 乘数=10/9.9≈1.0101（>1，与 baostock 递增口径一致）
	m := AdjMultiplier(10, 0.1, 0, 0, 0)
	if m <= 1 || (m-1.010101) > 0.001 {
		t.Fatalf("分红乘数应>1: %v", m)
	}
	// 10送5：ref=10/1.5=6.667 → 乘数=1.5
	m2 := AdjMultiplier(10, 0, 0.5, 0, 0)
	if (m2 - 1.5) > 1e-9 {
		t.Fatalf("送股乘数应=1.5: %v", m2)
	}
	// 10配3 配股价7：ref=(10+7*0.3)/1.3=9.3077 → 乘数≈1.0744
	m3 := AdjMultiplier(10, 0, 0, 0.3, 7)
	want3 := 10 / ((10 + 7*0.3) / 1.3)
	if (m3 - want3) > 1e-9 {
		t.Fatalf("配股乘数异常: %v want %v", m3, want3)
	}
	// 非法前收盘
	if AdjMultiplier(0, 1, 0, 0, 0) != 1 {
		t.Fatal("非法价应返回1")
	}
}
