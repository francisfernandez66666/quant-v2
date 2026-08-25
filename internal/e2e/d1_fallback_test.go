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
//	第一轮 mock LLM 正常 → 300308 D1=0.3 且非 RetryPending；
//	第二轮 mock LLM 对 D1 返回 500 → BatchScore 重试全败 → 300308 标记 RetryPending（Score=0，
//	不回退上一轮），且该股并入重试队列；
//	第三轮 mock 恢复 → 300308 经重试队列重新调 LLM → D1 恢复为 0.3 且退出重试队列。
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

	// ── 第一轮：LLM 正常 → D1 正常入池 ──
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
	firstD1Calls := rig.calls.lenOf("d1")

	// ── 第二轮：mock D1 返回 500 → 标记 RetryPending，并入重试队列（不回退上一轮）──
	rig.calls.SetFailD1(true)
	rig.eng.Run(context.Background(), since)
	if rig.calls.lenOf("d1") <= firstD1Calls {
		t.Fatal("第二轮 D1 LLM 未被调用（失败重试路径未触发）")
	}
	d1s2 := rig.eng.LastD1Scores()
	sc2, ok := d1s2["300308"]
	if !ok {
		t.Fatalf("第二轮 300308 无 D1 记录")
	}
	t.Logf("第二轮(Fail) 300308 D1=%.2f RetryPending=%v D1调用=%d (新增%d次失败)", sc2.Score, sc2.RetryPending, rig.calls.lenOf("d1"), rig.calls.lenOf("d1")-firstD1Calls)
	if sc2.Score != 0 || !sc2.RetryPending {
		t.Fatalf("第二轮 D1 失败应标记 RetryPending 且 Score=0（不回退上一轮）, got %+v", sc2)
	}
	if !contains(rig.eng.D1RetryQueueCodes(), "300308") {
		t.Fatalf("第二轮 300308 应并入重试队列, got %v", rig.eng.D1RetryQueueCodes())
	}

	// ── 第三轮：mock 恢复 → 300308 经重试队列重新调 LLM → D1 恢复、退出重试队列 ──
	rig.calls.SetFailD1(false)
	rig.eng.Run(context.Background(), since)
	d1s3 := rig.eng.LastD1Scores()
	sc3, ok := d1s3["300308"]
	if !ok {
		t.Fatalf("第三轮 300308 无 D1 记录")
	}
	t.Logf("第三轮(Recover) 300308 D1=%.2f RetryPending=%v D1调用=%d", sc3.Score, sc3.RetryPending, rig.calls.lenOf("d1"))
	if sc3.Score <= 0 || sc3.RetryPending {
		t.Fatalf("第三轮 300308 D1 应恢复>0 且退出重试状态, got %+v", sc3)
	}
	if contains(rig.eng.D1RetryQueueCodes(), "300308") {
		t.Fatalf("第三轮 300308 应退出重试队列, got %v", rig.eng.D1RetryQueueCodes())
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
