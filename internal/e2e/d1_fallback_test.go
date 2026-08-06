// e2e 专项验证本次"LLM 慢响应处理"改动：
//  1. 超时配置（llm.Config.Timeout 默认 60s、自定义生效）
//  2. D1 失败回退上一轮评分（mock LLM 500 → BatchScore 轮询重试失败 → 复用上一轮 D1）
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

// TestD1FallbackAcrossRuns 实盘快照下验证 D1 失败回退：
//
//	第一轮 mock LLM 正常 → D1=0.3，300308 NScore>0；
//	第二轮 mock LLM 对 D1 返回 500 → BatchScore 3 次重试全败 → 回退上一轮 D1，
//	断言 NScore 仍>0（回退生效），且 D1 LLM 调用确有第二次失败记录。
func TestD1FallbackAcrossRuns(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix, err := LoadFixture(filepath.Join("testdata", "fixtures.json"))
	if err != nil {
		t.Fatalf("加载 fixture: %v", err)
	}

	rig := newTestEngine(t, fix)
	rig.eng.SetShortEnabled(true)

	// 自选股注入：300308 中际旭创（D1 mock 返回 0.3，N 形需 D1>0 进总分）
	if !rig.wl.Add("300308") {
		t.Fatal("自选添加 300308 失败")
	}

	capT, err := time.ParseInLocation("2006-01-02 15:04:05", fix.CapturedAt, time.Local)
	if err != nil {
		t.Fatalf("解析 fixture 抓取时间 %q: %v", fix.CapturedAt, err)
	}
	since := time.Date(capT.Year(), capT.Month(), capT.Day(), 8, 30, 0, 0, time.Local)

	// ── 第一轮：LLM 正常 → D1 进入评分 ──
	rig.eng.Run(context.Background(), since)
	dash1 := rig.agg.Current()
	if dash1 == nil {
		t.Fatal("第一轮看板为空")
	}
	sc1, ok := dash1.Scores["300308"]
	if !ok {
		t.Fatalf("第一轮 300308 无打分记录")
	}
	t.Logf("第一轮(Normal) 300308 NScore=%.0f D1调用=%d", sc1.NScore, len(rig.calls.d1))
	if sc1.NScore <= 0 {
		t.Fatalf("第一轮 300308 NScore 应>0（D1=0.3 已透传）, got %.0f", sc1.NScore)
	}
	firstD1Calls := len(rig.calls.d1)

	// ── 第二轮：mock D1 返回 500 → 触发回退上一轮 ──
	rig.calls.failD1 = true
	rig.eng.Run(context.Background(), since)
	if len(rig.calls.d1) <= firstD1Calls {
		t.Fatal("第二轮 D1 LLM 未被调用（失败回退路径未触发）")
	}
	dash2 := rig.agg.Current()
	if dash2 == nil {
		t.Fatal("第二轮看板为空")
	}
	sc2, ok := dash2.Scores["300308"]
	if !ok {
		t.Fatalf("第二轮 300308 无打分记录")
	}
	t.Logf("第二轮(Fail) 300308 NScore=%.0f D1调用=%d (新增%d次失败)", sc2.NScore, len(rig.calls.d1), len(rig.calls.d1)-firstD1Calls)
	if sc2.NScore <= 0 {
		t.Fatalf("第二轮 300308 NScore 应>0（D1 失败回退上一轮评分, 非全量归0）, got %.0f", sc2.NScore)
	}
}
