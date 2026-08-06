// 今日改动全流程测试：用 2026-08-05 实盘 fixture（卧龙电驱 600580）mock 全部外部数据源，
// 像素级验证今日全部改动——专业模式咨询注入真实行情/无股票提示/名称解析/5分钟MACD、专业模式开关、
// 盘中限流、咨询对话历史落盘。
package e2e

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// loadTodayFixture 加载 2026-08-05 实盘 fixture（含 600580 卧龙电驱真实行情/日K/5分钟K/资金流/净流入）。
func loadTodayFixture(t *testing.T) *Fixture {
	t.Helper()
	fix, err := LoadFixture(filepath.Join("testdata", "fixtures_600580.json"))
	if err != nil {
		t.Fatalf("加载今日fixture: %v", err)
	}
	return fix
}

// todayConsult 驱动专业模式咨询并返回注入的 system prompt（最后一个 consult 请求）。
func todayConsult(t *testing.T, rig *testRig, msg string, proMode bool) string {
	t.Helper()
	if _, err := rig.eng.ConsultLLM("tester", msg, proMode); err != nil {
		t.Fatalf("ConsultLLM(%q): %v", msg, err)
	}
	if len(rig.calls.consult) == 0 {
		t.Fatal("mock LLM 未收到咨询请求")
	}
	return rig.calls.consult[len(rig.calls.consult)-1]
}

// TestConsultProModeInjectsRealData 专业模式咨询必须注入 600580 今日真实实时行情上下文。
func TestConsultProModeInjectsRealData(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	got, err := rig.eng.ConsultLLM("tester", "卧龙电驱(600580) 今天主力净流入多少？", true)
	if err != nil {
		t.Fatalf("ConsultLLM: %v", err)
	}
	if !strings.Contains(got, "主力净流入") {
		t.Errorf("回复应引用净流入: %q", got)
	}
	ctx := rig.calls.consult[len(rig.calls.consult)-1]

	// 像素级断言：今日实盘数据必须逐字段注入 context。
	checks := []string{
		"600580",      // 代码
		"卧龙电驱",        // 名称
		"现价 36.86",    // 今日收盘/现价
		"-22200.00万元", // 主力净流入 -2.22亿 = -22200万元
		"资金明细",        // 资金流明细块存在
		"MA5=",        // 日K均线
		"5分钟MACD",     // 5分钟MACD（基于今日真实5分钟K）
	}
	for _, c := range checks {
		if !strings.Contains(ctx, c) {
			t.Errorf("context 缺少 %q\n---context---\n%s", c, ctx)
		}
	}
}

// TestConsultProModeNameOnlyResolution 仅名称也能解析为 600580 并注入今日数据。
func TestConsultProModeNameOnlyResolution(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	ctx := todayConsult(t, rig, "卧龙电驱最近怎么样？我该不该加仓", true)
	if !strings.Contains(ctx, "600580") || !strings.Contains(ctx, "-22200.00万元") {
		t.Errorf("仅名称应解析出 600580 并注入净流入\n---context---\n%s", ctx)
	}
}

// TestConsultNormalModeNoContext 普通模式不注入 600580 实时行情 context。
func TestConsultNormalModeNoContext(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	ctx := todayConsult(t, rig, "卧龙电驱(600580) 今天主力净流入多少？", false)
	if strings.Contains(ctx, "卧龙电驱 600580") || strings.Contains(ctx, "-22200.00万元") || strings.Contains(ctx, "现价 36.86") {
		t.Errorf("普通模式不应注入实时行情 context\n---context---\n%s", ctx)
	}
}

// TestConsultProModeNoStockPrompt 专业模式但未指明股票时，注入提示用户指明股票的引导。
func TestConsultProModeNoStockPrompt(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	ctx := todayConsult(t, rig, "今天大盘怎么走？", true)
	if !strings.Contains(ctx, "指明具体股票") || !strings.Contains(ctx, "600580") {
		t.Errorf("无股票时应注入引导提示(说明需指明股票，示例含600580)\n---context---\n%s", ctx)
	}
}

// TestConsultStorePersistsAcrossInstances 咨询对话历史落盘，新实例（模拟重启）仍可读回。
func TestConsultStorePersistsAcrossInstances(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	dir := t.TempDir()
	store := data.NewConsultStore(dir + "/consult.json")
	store.Append("user", "今天怎么走")
	store.Append("assistant", "建议观察")
	if got := store.List(); len(got) != 2 {
		t.Fatalf("对话历史应=2条, got %d", len(got))
	}
	// 新实例（模拟重启）读同一文件
	reload := data.NewConsultStore(dir + "/consult.json")
	if got := reload.List(); len(got) != 2 {
		t.Fatalf("重启后对话历史应保留2条, got %d", len(got))
	}
}

// TestMinuteMACDInContext 专业模式 context 应含 5 分钟 MACD 状态（基于今日 5 分钟K真实数据）。
func TestMinuteMACDInContext(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	ctx := todayConsult(t, rig, "600580 这只票的技术面怎么看？", true)
	if !strings.Contains(ctx, "5分钟MACD") {
		t.Errorf("context 缺少 5分钟MACD 状态\n---context---\n%s", ctx)
	}
}

// TestAttachLiveBarFollowsTodayRealtime 今日 fixture 日K最后一根确为 2026-08-05，且可经新浪日K接口重放。
func TestAttachLiveBarFollowsTodayRealtime(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadTodayFixture(t)
	kls := fix.Klines["600580"]
	if len(kls) == 0 {
		t.Fatal("600580 无日K")
	}
	last := kls[len(kls)-1]
	if last.Date.Year() != 2026 || last.Date.Month() != 8 || last.Date.Day() != 5 {
		t.Fatalf("fixture 日K最后一根应为2026-08-05, got %v", last.Date)
	}
	if diff := last.Close - 36.86; diff > 0.05 || diff < -0.05 {
		t.Errorf("今日实盘收盘应≈36.86, got %.2f", last.Close)
	}
	if kl, err := rigMarket(t, fix).GetSinaKLine("600580", 40); err != nil || len(kl) == 0 {
		t.Errorf("新浪日K重放失败: %v (%d 根)", err, len(kl))
	}
}

// rigMarket 构造一个可重放 fixture 的行情客户端（供只读断言复用）。
func rigMarket(t *testing.T, fix *Fixture) *data.MarketAPI {
	t.Helper()
	rt := &fixtureTransport{fix: fix}
	api := data.NewMarketAPI()
	api.SetTransport(rt)
	return api
}

// TestConsultMultipleStocks 同一咨询可注入多只股票（600580 + 300750），互不覆盖、各自净流入独立。
func TestConsultMultipleStocks(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadTodayFixture(t)
	fix.Quotes["300750"] = fix.Quotes["600580"]
	fix.Klines["300750"] = fix.Klines["600580"]
	fix.MoneyFlow["300750"] = fix.MoneyFlow["600580"]
	fix.NetInflows["300750"] = -50000000.0

	rig := newTestEngine(t, fix)
	ctx := todayConsult(t, rig, "对比一下卧龙电驱(600580)和宁德时代(300750)", true)
	if !strings.Contains(ctx, "600580") || !strings.Contains(ctx, "300750") {
		t.Errorf("多股票咨询应同时注入 600580 与 300750\n---context---\n%s", ctx)
	}
	if !strings.Contains(ctx, "-22200.00万元") || !strings.Contains(ctx, "-5000.00万元") {
		t.Errorf("多股票应各自注入净流入\n---context---\n%s", ctx)
	}
}

// TestConsultRealtimeQuoteNetInflow 通过东财 emStockGet f62 验证净流入 -2.22亿 可被 GetRealtimeQuote 读取。
// 东财单股接口主力净流入字段为 f62（f162 是动态市盈率）。
func TestConsultRealtimeQuoteNetInflow(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	api := rigMarket(t, loadTodayFixture(t))
	si, err := api.GetRealtimeQuote("600580")
	if err != nil {
		t.Fatalf("GetRealtimeQuote: %v", err)
	}
	if si.Name != "卧龙电驱" {
		t.Errorf("名称=%q, want 卧龙电驱", si.Name)
	}
	if si.NetInflow != -222000000.0 {
		t.Errorf("NetInflow=%.0f, want -222000000 (-2.22亿)", si.NetInflow)
	}
	if diff := si.ChangePct - 3.336; diff > 0.02 || diff < -0.02 {
		t.Errorf("涨跌幅应≈3.34%%, got %.2f", si.ChangePct)
	}
}

// TestConsultMoneyFlow 今日资金流明细可解析，且主力净流入（超大+大）-22200万与 f162 一致。
func TestConsultMoneyFlow(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	api := rigMarket(t, loadTodayFixture(t))
	cf, err := api.GetStockMoneyFlow("600580")
	if err != nil {
		t.Fatalf("GetStockMoneyFlow: %v", err)
	}
	mainNet := (cf.SuperLargeIn - cf.SuperLargeOut) + (cf.LargeIn - cf.LargeOut)
	want := -22200.0 * 1e4
	if diff := mainNet - want; diff > 100 || diff < -100 {
		t.Errorf("主力净流入(超大+大)=%.0f, want %.0f (-22200万)", mainNet, want)
	}
	if cf.SmallIn <= 0 {
		t.Errorf("小单流入应>0, got %.0f", cf.SmallIn)
	}
}

// TestConsultTimeInjected 注入上下文带数据抓取时间戳（今日）。
func TestConsultTimeInjected(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	ctx := todayConsult(t, rig, "600580 今天怎么样？", true)
	now := time.Now().Format("2006-01-02")
	if !strings.Contains(ctx, "数据获取时间 "+now) {
		t.Errorf("context 应含今日时间戳 %q\n---context---\n%s", now, ctx)
	}
}

// TestConsultSingleSystemFirst 发送给模型的消息必须只有一条 system 且位于最前，
// 专业模式的实时行情上下文并入该条 system（避免多条/中途 system 导致模型回答跑偏）。
func TestConsultSingleSystemFirst(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	_ = todayConsult(t, rig, "卧龙电驱(600580) 今天主力净流入多少？", true)
	if len(rig.calls.consultMsgs) == 0 {
		t.Fatal("mock 未记录咨询消息序列")
	}
	msgs := rig.calls.consultMsgs[len(rig.calls.consultMsgs)-1]
	if len(msgs) == 0 || msgs[0].Role != "system" {
		t.Fatalf("首条消息应为 system, got %+v", msgs[0])
	}
	sysCnt := 0
	for i, m := range msgs {
		if m.Role == "system" {
			sysCnt++
			if i != 0 {
				t.Errorf("system 消息应只位于最前(位置0), 但出现在位置%d", i)
			}
		}
	}
	if sysCnt != 1 {
		t.Errorf("应仅1条 system 消息, got %d", sysCnt)
	}
	// 实时行情上下文应并入该条 system
	if !strings.Contains(msgs[0].Content, "现价 36.86") || !strings.Contains(msgs[0].Content, "-22200.00万元") {
		t.Errorf("实时行情应并入 system 消息\n---system---\n%s", msgs[0].Content)
	}
	// 最后一条是当前 user 提问
	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "600580") {
		t.Errorf("最后一条应为当前 user 提问, got %+v", last)
	}
}

// TestConsultHistoryLimitedTo6Rounds 多轮历史上限为最近 6 条消息（约 3 组问答），
// 连续多轮咨询后，发送给模型的历史仅保留最近 6 条 + 当前提问。
func TestConsultHistoryLimitedTo6Rounds(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	rig := newTestEngine(t, loadTodayFixture(t))
	// 连续 8 轮咨询填充历史（每轮写入 1 条 user + 1 条 assistant，共 16 条历史）
	for i := 0; i < 8; i++ {
		if _, err := rig.eng.ConsultLLM("tester", "第"+string(rune('A'+i))+"轮问题", false); err != nil {
			t.Fatalf("第%d轮 ConsultLLM: %v", i, err)
		}
	}

	// 第 9 轮（本轮提问），此时历史已超限
	_ = todayConsult(t, rig, "卧龙电驱(600580) 今天主力净流入多少？", true)
	if len(rig.calls.consultMsgs) == 0 {
		t.Fatal("mock 未记录咨询消息序列")
	}
	msgs := rig.calls.consultMsgs[len(rig.calls.consultMsgs)-1]
	// 结构：1 条 system + 历史(≤6) + 当前提问
	history := msgs[1 : len(msgs)-1]
	if len(history) != 6 {
		t.Errorf("历史应截取最近 6 条, got %d 条 (消息总数 %d)", len(history), len(msgs))
	}
	// 最早的两轮（问题A/回答A、问题B/回答B）应被丢弃
	joined := strings.Join(func() []string {
		out := make([]string, len(history))
		for i, m := range history {
			out[i] = m.Content
		}
		return out
	}(), "|")
	if strings.Contains(joined, "第A轮问题") || strings.Contains(joined, "第B轮问题") {
		t.Errorf("最早的历史消息应被丢弃, 保留 %q", joined)
	}
	// 最近几轮（问题G/回答G）应保留
	if !strings.Contains(joined, "第G轮问题") || !strings.Contains(joined, "第G轮问题") {
		t.Errorf("应保留最近的历史消息, got %q", joined)
	}
}

// TestConsultNetInflowMissingHint 东财未返回净流入(走新浪兜底)时，上下文提示"数据源未返回"而非误导为 0。
func TestConsultNetInflowMissingHint(t *testing.T) {
	data.DisableAll = true
	defer func() { data.DisableAll = false }()

	fix := loadTodayFixture(t)
	// 清空东财净流入：模拟东财未返回 f62，走新浪兜底（新浪无净流入字段 → 0）
	fix2 := *fix
	fix2.NetInflows = nil

	rig := newTestEngine(t, &fix2)
	_ = todayConsult(t, rig, "卧龙电驱(600580) 今天主力净流入多少？", true)
	if len(rig.calls.consult) == 0 {
		t.Fatal("mock 未记录咨询")
	}
	ctx := rig.calls.consult[len(rig.calls.consult)-1]
	if !strings.Contains(ctx, "数据源未返回") {
		t.Errorf("净流入缺失时应提示数据源未返回\n---context---\n%s", ctx)
	}
}
