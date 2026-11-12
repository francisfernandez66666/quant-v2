// news_gate_test.go §NEWS-GATE（2026-11-12）单测：一票否决 / 通用系数 / 板块否决 / 总开关 /
// 盘中反转防抖与限额。全部内存最小装配，不依赖真实行情与网络。
package engine

import (
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/report"
)

// mkNewsGateEngine 最小资讯闸装配（默认旋钮：blockBelow=0 / sectorBlock=true / boost=0.25）。
func mkNewsGateEngine(t *testing.T) *Engine {
	t.Helper()
	return &Engine{
		userID:            "u",
		cfgMgr:            config.NewManagerWithRules(&config.Rules{}),
		newsGatePrevD1:    make(map[string]float64),
		newsGateStrikes:   make(map[string]int),
		newsGateStrikeDir: make(map[string]string),
	}
}

// TestNewsGateVetoAndBoost R9/R5：D1<0 买入信号一票否决（改发提醒），D1>0 置信度乘通用系数，
// D1=0（无事件）不罚不奖，watch/做空信号不受影响。
func TestNewsGateVetoAndBoost(t *testing.T) {
	e := mkNewsGateEngine(t)
	at := time.Now()
	sigs := []combat_agent.Signal{
		mkBuySig("600001", "dragon", at),    // D1=-40 → 拦截
		mkBuySig("600002", "dragon", at),    // D1=40  → 置信度 ×1.25
		mkBuySig("600003", "dragon", at),    // D1=0   → 原样
		mkBuySig("600004", "dragon", at),    // RetryPending → 原样（失败≠利空）
		{ID: "w1", Code: "600005", Strategy: "dragon", Direction: "做多", Action: "watch"}, // watch 不拦
	}
	d1 := map[string]combat_agent.D1Score{
		"600001": {Code: "600001", Score: -40, Reason: "控股股东被立案"},
		"600002": {Code: "600002", Score: 40, Reason: "并购重组"},
		"600003": {Code: "600003", Score: 0, Reason: "无特定事件，D1归0"},
		"600004": {Code: "600004", Score: 0, RetryPending: true, Reason: "LLM失败待重试"},
	}

	kept, alerts := e.applyNewsGate(sigs, d1)

	if len(kept) != 4 {
		t.Fatalf("应存留 4 条（拦截 600001），got %d: %+v", len(kept), kept)
	}
	for _, s := range kept {
		if s.Code == "600001" {
			t.Fatal("D1<0 的买入信号必须被拦截")
		}
	}
	if len(alerts) != 1 || alerts[0].Code != "600001" {
		t.Fatalf("应产出 1 条利空拦截提醒, got %+v", alerts)
	}
	if alerts[0].Direction != "提醒" || alerts[0].AlertType != "利空拦截" {
		t.Fatalf("拦截提醒形态错误: %+v", alerts[0])
	}
	for _, s := range kept {
		switch s.Code {
		case "600002":
			if want := 0.7 * 1.25; s.Confidence < want-1e-9 {
				t.Fatalf("D1=40 应 ×1.25, got %f want %f", s.Confidence, want)
			}
		case "600003", "600004":
			if s.Confidence != 0.7 {
				t.Fatalf("D1=0/RetryPending 置信度不应变化, got %f", s.Confidence)
			}
		}
	}
}

// TestNewsGateSectorVeto R4：板块负分映射命中的个股买入同样一票否决（即使该股自身 D1 缺失）。
func TestNewsGateSectorVeto(t *testing.T) {
	e := mkNewsGateEngine(t)
	e.newsGate = &NewsGateSnapshot{
		Day:                 "2026-11-12",
		SectorNegativeCodes: map[string]string{"600010": "板块强利空:光模块"},
	}
	sigs := []combat_agent.Signal{mkBuySig("600010", "factor#1", time.Now())}
	kept, alerts := e.applyNewsGate(sigs, map[string]combat_agent.D1Score{})
	if len(kept) != 0 {
		t.Fatalf("板块负分命中必须拦截, got %+v", kept)
	}
	if len(alerts) != 1 || alerts[0].Reason == "" || !strings.Contains(alerts[0].Reason, "板块利空拦截") {
		t.Fatalf("应为板块级拦截提醒, got %+v", alerts)
	}
}

// TestNewsGateDisable R6：总开关关闭时闸完全不生效（含板块否决）。
func TestNewsGateDisable(t *testing.T) {
	e := mkNewsGateEngine(t)
	tr := true
	e.cfgMgr.SetD1Config(&config.D1Config{NewsGateDisable: &tr})
	e.newsGate = &NewsGateSnapshot{
		SectorNegativeCodes: map[string]string{"600010": "板块强利空:光模块"},
	}
	sigs := []combat_agent.Signal{mkBuySig("600010", "factor#1", time.Now())}
	kept, alerts := e.applyNewsGate(sigs, map[string]combat_agent.D1Score{})
	if len(kept) != 1 || len(alerts) != 0 {
		t.Fatalf("开关关闭应全放行, kept=%d alerts=%d", len(kept), len(alerts))
	}
}

// TestNewsGateHeldVetoAlert R10：持仓票被拦截时提醒升格清仓级（引导卖出）。
func TestNewsGateHeldVetoAlert(t *testing.T) {
	e := mkNewsGateEngine(t)
	rpt := report.New(t.TempDir() + "/rpt.json")
	rpt.LogSignalWithMetaQty("S1", "600001", "测试", "做多", "dragon", 10, 0, 0, 100, nil)
	e.rpt = rpt
	e.userID = "" // 与 rpt 记录口径一致（HeldPositionCodesFor 按 userID 过滤）
	sigs := []combat_agent.Signal{mkBuySig("600001", "dragon", time.Now())}
	_, alerts := e.applyNewsGate(sigs, map[string]combat_agent.D1Score{
		"600001": {Code: "600001", Score: -40, Reason: "控股股东被立案"},
	})
	if len(alerts) != 1 || alerts[0].AlertType != "清仓" || alerts[0].SellLevel != "清仓" {
		t.Fatalf("持仓票拦截提醒应升格清仓级, got %+v", alerts)
	}
}

// TestNewsGateReversalDebounce R11：持仓票 D1 跨阈值翻转需 3 轮同向确认；
// 确认后记反转事件并受每日上限/冷却约束；RetryPending 不计击。
func TestNewsGateReversalDebounce(t *testing.T) {
	e := mkNewsGateEngine(t)
	rpt := report.New(t.TempDir() + "/rpt.json")
	rpt.LogSignalWithMetaQty("S1", "600001", "测试", "做多", "dragon", 10, 0, 0, 100, nil)
	e.rpt = rpt
	e.userID = "" // 与 rpt 记录口径一致
	day := "2026-11-12"

	// 轮1：D1 从 0 跨到 -40（突发利空）→ 计击 1，不记事件。
	e.detectNewsGateReversal(&NewsGateSnapshot{Day: day, NegativeCodes: map[string]string{"600001": "利空"}},
		map[string]combat_agent.D1Score{"600001": {Code: "600001", Score: -40, Reason: "立案"}}, e.newsGateCfg())
	if len(e.newsGateEvents) != 0 {
		t.Fatalf("1 轮不应确认反转, events=%d", len(e.newsGateEvents))
	}
	// 轮2、轮3：连续同向 → 第 3 轮确认，记 bearish 事件。
	e.detectNewsGateReversal(&NewsGateSnapshot{Day: day, NegativeCodes: map[string]string{"600001": "利空"}},
		map[string]combat_agent.D1Score{"600001": {Code: "600001", Score: -40, Reason: "立案"}}, e.newsGateCfg())
	e.detectNewsGateReversal(&NewsGateSnapshot{Day: day, NegativeCodes: map[string]string{"600001": "利空"}},
		map[string]combat_agent.D1Score{"600001": {Code: "600001", Score: -40, Reason: "立案"}}, e.newsGateCfg())
	if len(e.newsGateEvents) != 1 {
		t.Fatalf("3 轮同向应确认 1 次反转, events=%d", len(e.newsGateEvents))
	}
	ev := e.newsGateEvents[0]
	if ev.Kind != "bearish" || ev.Skipped != "" {
		t.Fatalf("首次反转应为 bearish 且触发成功, got %+v", ev)
	}
	// 冷却期内的第 2 次反转（600002）→ 记事件但 Skipped 留痕，不重复触发。
	rpt.LogSignalWithMetaQty("S2", "600002", "测试", "做多", "dragon", 10, 0, 0, 100, nil)
	for i := 0; i < 3; i++ {
		e.detectNewsGateReversal(&NewsGateSnapshot{Day: day, NegativeCodes: map[string]string{"600002": "利空"}},
			map[string]combat_agent.D1Score{"600002": {Code: "600002", Score: -40, Reason: "减持"}}, e.newsGateCfg())
	}
	if len(e.newsGateEvents) != 2 {
		t.Fatalf("第二次反转应记事件, events=%d", len(e.newsGateEvents))
	}
	if e.newsGateEvents[1].Skipped == "" {
		t.Fatal("冷却期内第二次触发应 Skipped 留痕")
	}
	// 反转回正（bullish）方向不同向 → 计击重置，不误触发（600002 已持仓，方向由 bearish 翻 bullish）。
	for i := 0; i < 2; i++ {
		e.detectNewsGateReversal(&NewsGateSnapshot{Day: day},
			map[string]combat_agent.D1Score{"600002": {Code: "600002", Score: 40, Reason: "重大利好"}}, e.newsGateCfg())
	}
	if e.newsGateStrikes["600002"] != 2 || e.newsGateStrikeDir["600002"] != "bullish" {
		t.Fatalf("反向计击应独立累计, got %d/%s", e.newsGateStrikes["600002"], e.newsGateStrikeDir["600002"])
	}
}
