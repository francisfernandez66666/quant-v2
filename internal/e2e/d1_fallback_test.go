// e2e 专项验证"LLM 失败不靠兜底、全部走重试队列"改动：
//  1. 超时配置（llm.Config.Timeout 默认 60s、自定义生效）
//  2. D1 失败标记 RetryPending 入重试队列（mock LLM 500 → BatchScore 轮询重试失败 →
//     不回退上一轮评分、不归0占位；下一轮 LLM 恢复经重试队列重新打分恢复）
//  3. 5s 近实时 ScorePool 注入 D1 缓存（N 形评分消费到 D1 分）
//
// 全部使用 testdata/fixtures.json 实盘快照 mock 外部数据源，离线可复现。
package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/llm"
)

// TestLLMTimeoutConfig 验证超时配置：默认 60s，自定义值生效。
func TestLLMTimeoutConfig(t *testing.T) {
	// 默认：未指定超时 → 60s
	def := llm.New(llm.Config{APIKey: "k", APIURL: "http://127.0.0.1:1"})
	if def.Timeout() != 60*time.Second {
		t.Fatalf("默认超时应为 60s, got %v", def.Timeout())
	}

	// 自定义：90s
	custom := llm.New(llm.Config{APIKey: "k", APIURL: "http://127.0.0.1:1", Timeout: 90 * time.Second})
	if custom.Timeout() != 90*time.Second {
		t.Fatalf("自定义超时应为 90s, got %v", custom.Timeout())
	}
}

// TestD1RetryQueueAcrossRuns 实盘快照下验证 D1 失败不靠兜底、全部走重试队列：
//
//	§S1 增量 D1 语义：签名未变 + 已有非待重试分 → 复用不调 LLM（主循环提速主因）；
//	只有"新事件/缺分/待重试"的个股才会真正调 LLM。因此本测试用"第二轮新增无旧分自选股"
//	强制触发重评 → mock LLM 返回 500 → BatchScore 重试全败 → 该股标记 RetryPending（Score=0，
//	不回退上一轮），并入重试队列；第三轮 mock 恢复 → 经重试队列重新调 LLM → D1 恢复且退出队列。
//	同时断言第一轮 300308 正常得分、第二轮仍被复用（增量路径未受影响）。
//	注意：重试标的须选"新闻池外 + 有行情数据"的代码（002594 比亚迪），否则若本就在新闻池内，
//	首轮已有分 → 第二轮被复用而不会真正调 LLM（复用了错误的标的）。
func TestD1RetryQueueAcrossRuns(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix, err := LoadFixture(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("加载 fixture: %v", err)
	}

	rig := newTestEngine(t, fix)
	rig.eng.SetShortEnabled(true)

	// 自选股注入：300308 中际旭创（D1 mock 返回 0.3，N 形需 D1>0 进总分）
	if !rig.wl.Add("", "300308") {
		t.Fatal("自选添加 300308 失败")
	}

	capT, err := time.ParseInLocation("2006-01-02 15:04:05", fix.CapturedAt, time.Local)
	if err != nil {
		t.Fatalf("解析 fixture 抓取时间 %q: %v", fix.CapturedAt, err)
	}
	since := time.Date(capT.Year(), capT.Month(), capT.Day(), 8, 30, 0, 0, time.Local)

	// ── 第一轮：LLM 正常 → 300308 正常入池 ──
	rig.eng.Run(context.Background(), since)
	d1s1 := rig.eng.LastD1Scores()
	sc1, ok := d1s1["300308"]
	if !ok {
		t.Fatalf("第一轮 300308 无 D1 记录")
	}
	t.Logf("第一轮(Normal) 300308 D1=%.2f RetryPending=%v D1调用=%d", sc1.Score, sc1.RetryPending, rig.calls.lenOf("d1"))
	if sc1.Score <= 0 || sc1.RetryPending {
		t.Fatalf("第一轮 300308 D1 应>0 且非 RetryPending, got %+v", sc1)
	}

	// ── 第二轮：新增无旧分自选股 002594（比亚迪，mock 返回 0.3）触发增量重评；
	//    mock D1 返回 500 → 该股标记 RetryPending（Score=0，不回退上一轮）并入重试队列；
	//    300308 签名未变 + 有非待重试分 → 被复用（增量路径，不重复调 LLM）──
	rig.calls.SetFailD1(true)
	if !rig.wl.Add("", "002594") {
		t.Fatal("自选添加 002594 失败")
	}
	rig.eng.Run(context.Background(), since)
	d1s2 := rig.eng.LastD1Scores()
	scNew, ok := d1s2["002594"]
	if !ok {
		t.Fatalf("第二轮 002594 无 D1 记录")
	}
	t.Logf("第二轮(Fail) 002594 D1=%.2f RetryPending=%v D1调用=%d", scNew.Score, scNew.RetryPending, rig.calls.lenOf("d1"))
	if scNew.Score != 0 || !scNew.RetryPending {
		t.Fatalf("第二轮 002594 D1 失败应标记 RetryPending 且 Score=0（不回退上一轮）, got %+v", scNew)
	}
	if !contains(rig.eng.D1RetryQueueCodes(), "002594") {
		t.Fatalf("第二轮 002594 应并入重试队列, got %v", rig.eng.D1RetryQueueCodes())
	}
	if sc300, ok := d1s2["300308"]; !ok || sc300.Score != sc1.Score || sc300.RetryPending {
		t.Fatalf("第二轮 300308 应复用第一轮分(%.2f,非待重试), got %+v", sc1.Score, sc300)
	}

	// ── 第三轮：mock 恢复 → 002594 经重试队列重新调 LLM → D1 恢复、退出重试队列 ──
	rig.calls.SetFailD1(false)
	rig.eng.Run(context.Background(), since)
	d1s3 := rig.eng.LastD1Scores()
	sc3, ok := d1s3["002594"]
	if !ok {
		t.Fatalf("第三轮 002594 无 D1 记录")
	}
	t.Logf("第三轮(Recover) 002594 D1=%.2f RetryPending=%v D1调用=%d", sc3.Score, sc3.RetryPending, rig.calls.lenOf("d1"))
	// 恢复语义 = 退出重试状态（不再 RetryPending、不再在重试队列），LLM 已重新调用拿到真实分；
	// 002594 无实质事件 → 按规则 D1 归 0，故不断言分数>0（只要非"待重试"占位即恢复成功）。
	if sc3.RetryPending {
		t.Fatalf("第三轮 002594 D1 应退出重试状态（LLM 已恢复重打分）, got %+v", sc3)
	}
	if strings.Contains(sc3.Reason, "待重试") {
		t.Fatalf("第三轮 002594 不应再是重试占位（Reason=%q）, got %+v", sc3.Reason, sc3)
	}
	if contains(rig.eng.D1RetryQueueCodes(), "002594") {
		t.Fatalf("第三轮 002594 应退出重试队列, got %v", rig.eng.D1RetryQueueCodes())
	}
}

// contains 判断字符串切片是否含目标值（小工具，避免引入外部依赖）。
func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
