// qmt_feed_guard_test.go — §GOROUTINE-GUARD 的券商行情腿（2026-11-02 同批）。
//
// 为什么行情轮询也必须按**每一轮**包 recover：这条腿今天出厂即开（§SUBSCRIPTION-A 裁决 16），
// 而它和资金心跳是同一类"只读观测腿"——pollOnce 的注释自陈"绝不 panic"，那是意图不是强制；
// 同族的采集循环 loop() 早就有 recover，形状不一致就是风险本身。
// 一旦这一轮 panic 打死进程，代价是"没人在管持仓"（11-02 实录 131 次重启、整天零买入评估），
// 而不是"少一轮 tick"。
//
// 守卫按轮收、不按 Start 顶部收：顶部只兜第一次，之后轮询静默停摆＝FeedStats.Rounds 特意要防的
// "冒充在跑"形态，不能由我们的兜底手段自己造出来。
// English: the broker-quote poll loop keeps a per-round panic guard so an observability
// defect can never kill the trading process, and never silently stops the round counter.

package data

import (
	"os"
	"strings"
	"testing"
)

// TestFeedStartPollsThroughGuard Start 的轮询调用必须走 pollOnceGuarded，而不是裸 pollOnce。
func TestFeedStartPollsThroughGuard(t *testing.T) {
	raw, err := os.ReadFile("qmt_feed.go")
	if err != nil {
		t.Fatalf("读 qmt_feed.go: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func (f *QMTFeed) Start()")
	if start < 0 {
		t.Fatalf("找不到 QMTFeed.Start（§SUBSCRIPTION-A 的轮询循环改名/搬迁了，本锁需同批更新）")
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatalf("Start 函数体尾部形态变了")
	}
	body := src[start : start+end]
	if !strings.Contains(body, "f.pollOnceGuarded()") {
		t.Fatalf("Start 没有走受守卫的轮询调用（裸 pollOnce＝一轮 panic 打死整个进程）")
	}
	if strings.Contains(body, "f.pollOnce()") {
		t.Fatalf("Start 里仍有裸 pollOnce 调用")
	}

	guard := strings.Index(src, "func (f *QMTFeed) pollOnceGuarded()")
	if guard < 0 {
		t.Fatalf("pollOnceGuarded 不存在")
	}
	gbody := src[guard:]
	gbody = gbody[:strings.Index(gbody, "\n}\n")+3]
	for _, need := range []string{"recover()", "debug.Stack()", "f.pollOnce()"} {
		if !strings.Contains(gbody, need) {
			t.Fatalf("守卫里缺 %s（吞掉栈＝把下次排障退回盲猜）", need)
		}
	}
}
