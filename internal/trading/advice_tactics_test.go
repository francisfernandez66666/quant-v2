// advice_tactics_test.go — §ADVICE-TACTICS（2026-11-05，owner 两条裁决）的行为锁：
//
//	A. 「量化交易」页关掉的战法，其**加仓**建议不再出现在列表里（硬过滤，不是灰显）；
//	   卖出方向（止盈/止损/减仓/清仓）**一条都不滤**——已买进来的持仓仍是敞口。
//	B. 建议行带上当日定权算出的股数/金额/受限项；数只能从 signal_decisions 快照抄，
//	   抄不到就是**字段缺席**（JSON 里根本没有这个键），绝不用 0 冒充"算出来是 0"。
//
// 另锁过滤不许静默：Advise 返回的 filtered_by_switch 必须数得出来滤了几条。
package trading

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
)

// tacticsFixture 造一轮最小可判的 Advise 输入：
// 两只都是"龙头"战法的持仓、都有活跃信号、盈利且回撤可控 ⇒ 常规路径各产一条加仓建议。
// 开关（Cfg.Strategies）与投影卡片由用例自己往上加，避免每个用例重抄一遍夹具。
func tacticsFixture(t *testing.T) AdviceInput {
	t.Helper()
	cfg := config.DefaultQMTConfig()
	cfg.Advice.AddSignalActive = true
	cfg.Advice.AddReopenDrawdownPct = -5
	cfg.Advice.HoldMinProfitPct = 0
	return AdviceInput{
		Agent: nil, // 卖出侧函数需真实 Agent；本文件用 SellProjection 造卖出卡片，不需要
		Positions: []store.RealPosition{
			{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "龙头"},
			{TsCode: "000001.SZ", Name: "平安", Qty: 100, CostPrice: 50, Amount: 5000, HighestPrice: 55, Strategy: "龙头"},
		},
		Quotes: map[string]*data.StockInfo{
			"600000": {Code: "600000", Price: 10.8}, // 盈利 8%、回撤 -1.8% ⇒ 加仓成立
			"000001": {Code: "000001", Price: 54.0}, // 盈利 8%、回撤 -1.8% ⇒ 加仓成立
		},
		Scores: map[string]combat_agent.StockScores{
			"600000": {Code: "600000", SignalActive: true},
			"000001": {Code: "000001", SignalActive: true},
		},
		Cfg: cfg,
	}
}

// TestAdviseTacticsSwitchDropsAddButNeverSell 裁决 A 的两半（同一条代码两侧各种一次）：
//   - 开关关了"龙头" ⇒ 两只的加仓建议一起消失，且 filtered_by_switch 数得出被滤掉几条；
//   - 给 600000 注入一条卖出投影 ⇒ **同一只票**的离场建议必须留下
//     （已买进来的持仓不会因为战法被关掉就不需要止损）。
func TestAdviseTacticsSwitchDropsAddButNeverSell(t *testing.T) {
	in := tacticsFixture(t)
	// 基线：开关空白（存量"全部开启"语义，内置四形态在默认全集内）⇒ 两条加仓都在
	in.Cfg.Strategies = nil
	base, baseFilter := Advise(in)
	if len(base) != 2 || baseFilter.FilteredBySwitch != 0 {
		t.Fatalf("空白名单基线应留 2 条加仓且无过滤，got %d 条 / filtered=%d", len(base), baseFilter.FilteredBySwitch)
	}
	for _, a := range base {
		if a.Action != "加仓" {
			t.Fatalf("基线两条都应是加仓，got %+v", a)
		}
	}

	// 开关只列 momentum ⇒ 龙头整支战法在列表外
	in.Cfg.Strategies = []string{"momentum"}
	off, f1 := Advise(in)
	if len(off) != 0 {
		t.Fatalf("战法关闭后不得再有加仓建议，got %+v", off)
	}
	if f1.FilteredBySwitch != 2 {
		t.Fatalf("filtered_by_switch 应为 2（两条加仓被滤），got %d", f1.FilteredBySwitch)
	}

	// 同一轮给 600000 一张卖出投影卡（减仓）：卖出方向永不过滤，加仓那条仍被滤掉
	in.SellUnifiedOn = true
	in.SellProjection = []UnifiedSellView{
		{TsCode: "600000.SH", Action: "减仓", Level: "高", Reason: "[统一裁决] 放量派发", RefPrice: 10.8, Source: UnifiedSellSourceAction},
	}
	mixed, f2 := Advise(in)
	if len(mixed) != 1 {
		t.Fatalf("应只剩 600000 那条卖出建议，got %+v", mixed)
	}
	if mixed[0].Code != "600000" || mixed[0].Action != "减仓" {
		t.Fatalf("被关战法的**卖出**建议必须保留，got %s/%s", mixed[0].Code, mixed[0].Action)
	}
	if f2.FilteredBySwitch != 1 {
		t.Fatalf("000001 的加仓被滤、600000 走卖出投影 ⇒ filtered 应为 1，got %d", f2.FilteredBySwitch)
	}
}

// TestAdviseTacticsSwitchKeepsUnattributedRows 归属不明的行不过滤：
// 券商快照对账建的历史持仓 strategy 为空，"无法归因"与"被战法开关滤掉"是两件事
// （本仓纪律：看不见 ≠ 被过滤）。
func TestAdviseTacticsSwitchKeepsUnattributedRows(t *testing.T) {
	in := tacticsFixture(t)
	in.Cfg.Strategies = []string{"momentum"}
	in.Positions = []store.RealPosition{
		{TsCode: "600000.SH", Name: "无战法归属仓", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: ""},
	}
	in.Quotes = map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10.8}}
	in.Scores = map[string]combat_agent.StockScores{"600000": {Code: "600000", SignalActive: true}}
	out, f := Advise(in)
	if len(out) != 1 || out[0].Action != "加仓" {
		t.Fatalf("无战法归属的加仓建议不得被静默滤掉，got %+v", out)
	}
	if f.FilteredBySwitch != 0 {
		t.Fatalf("无法归因的条数不许计入 filtered_by_switch，got %d", f.FilteredBySwitch)
	}
}

// TestAdviseTacticsSwitchLegacyAllOnSemantics 空白名单的存量语义在建议侧同样成立：
// 内置四形态与库规则（fac_/pat_）都在、动量与未知战法不在——
// 与 signalctl.TestAdmitStrategyDefaultSet 判的是同一件事，两把入口不许各判一套。
func TestAdviseTacticsSwitchLegacyAllOnSemantics(t *testing.T) {
	mk := func(strategy string) AdviceInput {
		in := tacticsFixture(t)
		in.Cfg.Strategies = nil
		in.Positions = []store.RealPosition{
			{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: strategy},
		}
		in.Quotes = map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10.8}}
		in.Scores = map[string]combat_agent.StockScores{"600000": {Code: "600000", SignalActive: true}}
		return in
	}
	for _, s := range []string{"龙头", "dragon", "N形", "双响炮", "龙回头", "fac_1", "pat_2"} {
		out, f := Advise(mk(s))
		if len(out) != 1 || f.FilteredBySwitch != 0 {
			t.Fatalf("默认全集应放行 %q 的加仓建议，got %d 条 / filtered=%d", s, len(out), f.FilteredBySwitch)
		}
	}
	for _, s := range []string{"动量", "momentum", "我的自定义战法"} {
		out, f := Advise(mk(s))
		if len(out) != 0 || f.FilteredBySwitch != 1 {
			t.Fatalf("默认全集不得放行 %q（动量/未知严格 opt-in），got %d 条 / filtered=%d", s, len(out), f.FilteredBySwitch)
		}
	}
}

// TestAdviseTacticsSwitchKeyNameBothFormsMatch 键空间这一半缺陷的专门锁：
// 持仓行存的是**显示名**（券商回报/对账落的），量化页开关存的是**规范键**——
// 两种写法都必须命中同一枚开关，否则表现为"明明开了龙头却看不到龙头的建议"。
func TestAdviseTacticsSwitchKeyNameBothFormsMatch(t *testing.T) {
	for _, stored := range []string{"龙头", "dragon"} {
		for _, listed := range []string{"dragon", "龙头"} {
			in := tacticsFixture(t)
			in.Cfg.Strategies = []string{listed}
			in.Positions = []store.RealPosition{
				{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: stored},
			}
			in.Quotes = map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10.8}}
			in.Scores = map[string]combat_agent.StockScores{"600000": {Code: "600000", SignalActive: true}}
			out, f := Advise(in)
			if len(out) != 1 || f.FilteredBySwitch != 0 {
				t.Fatalf("持仓=%q 开关=%q 必须命中（同一种战法的两种写法），got %d 条 / filtered=%d",
					stored, listed, len(out), f.FilteredBySwitch)
			}
		}
	}
}

// decisionRow 造一条当日定权快照（字段按用例填，缺省即零值）。
func decisionRow(code string, qty int, notional float64, cappedBy, at, stage string) store.SignalDecision {
	return store.SignalDecision{
		UserID: "u_adv", Day: "20261105", Channel: "live", Code: code, Strategy: "dragon",
		At: at, Stage: stage, Qty: qty, Notional: notional, CappedBy: cappedBy,
	}
}

// TestAdviseSuggestQtyPresentAndAbsent 裁决 B 的两态（缺席用 JSON 里"没有这个键"来判，
// 而不是判 Go 零值——零值和 0 建议在序列化后是一回事，这正是本条缺陷的成因形状）。
func TestAdviseSuggestQtyPresentAndAbsent(t *testing.T) {
	// 夹具：600000 一条加仓建议
	in := tacticsFixture(t)
	in.Cfg.Strategies = nil
	in.Positions = []store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "龙头"},
	}
	in.Quotes = map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10.5}}
	in.Scores = map[string]combat_agent.StockScores{"600000": {Code: "600000", SignalActive: true}}

	// ① 有快照行 ⇒ 四列都在，且数**逐字来自那一行**（本层一个算式都没有）
	in.Decisions = []store.SignalDecision{decisionRow("600000.SH", 300, 3150, "target", "2026-11-05T10:31:00+08:00", "evaluated")}
	out, _ := Advise(in)
	if len(out) != 1 {
		t.Fatalf("应产出 1 条加仓建议，got %+v", out)
	}
	a := out[0]
	if a.SuggestQty != 300 || a.SuggestAmount != 3150 || a.CappedBy != "target" || a.SizingAt == "" {
		t.Fatalf("四列应从定权快照原样带出，got %+v", a)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"suggest_qty":300`) {
		t.Fatalf("有序列化后的股数才算真的带上了，got %s", raw)
	}

	// ② 没有任何快照行 ⇒ suggest_* 键**根本不出现**（不是 0）
	in.Decisions = nil
	out2, _ := Advise(in)
	raw2, err := json.Marshal(out2[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"suggest_qty", "suggest_amount", "capped_by", "sizing_at"} {
		if strings.Contains(string(raw2), k) {
			t.Fatalf("无定权读数时 JSON 里不得出现 %q（缺席 ≠ 0），got %s", k, raw2)
		}
	}

	// ③ 快照行 Qty=0（stage=skipped：被帽挡了）⇒ 股数仍缺席，但"被哪道帽挡住"必须看得见
	in.Decisions = []store.SignalDecision{decisionRow("600000", 0, 0, "per_trade", "2026-11-05T10:36:00+08:00", "skipped")}
	out3, _ := Advise(in)
	raw3, _ := json.Marshal(out3[0])
	if strings.Contains(string(raw3), "suggest_qty") {
		t.Fatalf("Qty=0 的行不得写成建议 0 股，got %s", raw3)
	}
	if out3[0].CappedBy != "per_trade" || out3[0].SizingAt == "" {
		t.Fatalf("被拦行的受限项与时刻仍要带出（那正是「为什么没有量」的答案），got %+v", out3[0])
	}
}

// TestAdviseSuggestPicksLatestRowPerCode 同一只票当日多行时取 **At 最新** 那一行。
// SignalDecisionsForDay 按 at 倒序返回，所以"每码第一次见到的那行"就是最新一行；
// 这里按读到的顺序喂两行（新在前），确认不是随便挑了一行。
func TestAdviseSuggestPicksLatestRowPerCode(t *testing.T) {
	in := tacticsFixture(t)
	in.Cfg.Strategies = nil
	in.Positions = []store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "龙头"},
	}
	in.Quotes = map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10.5}}
	in.Scores = map[string]combat_agent.StockScores{"600000": {Code: "600000", SignalActive: true}}
	in.Decisions = []store.SignalDecision{
		decisionRow("600000", 500, 5250, "cluster", "2026-11-05T13:40:00+08:00", "placed"),
		decisionRow("600000", 300, 3150, "target", "2026-11-05T10:31:00+08:00", "evaluated"),
	}
	out, _ := Advise(in)
	if out[0].SuggestQty != 500 || out[0].CappedBy != "cluster" {
		t.Fatalf("应取当日最新那一行（500/cluster），got %+v", out[0])
	}
	if !strings.HasPrefix(out[0].SizingAt, "2026-11-05T13:40") {
		t.Fatalf("SizingAt 必须是那一行的时刻，got %q", out[0].SizingAt)
	}
}

// TestAdviseSuggestKeyShapesMeet 键空间：快照行可能写 "600000"、"600000.SH"，
// 建议行是纯码；带后缀的快照行必须也能对上（对不上的表现是"永远未计算"）。
func TestAdviseSuggestKeyShapesMeet(t *testing.T) {
	in := tacticsFixture(t)
	in.Cfg.Strategies = nil
	in.Positions = []store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 100, CostPrice: 10, Amount: 1000, HighestPrice: 11, Strategy: "龙头"},
	}
	in.Quotes = map[string]*data.StockInfo{"600000": {Code: "600000", Price: 10.5}}
	in.Scores = map[string]combat_agent.StockScores{"600000": {Code: "600000", SignalActive: true}}
	for _, shape := range []string{"600000", "600000.SH", "sh600000"} {
		in.Decisions = []store.SignalDecision{decisionRow(shape, 400, 4200, "", "2026-11-05T10:31:00+08:00", "evaluated")}
		out, _ := Advise(in)
		if out[0].SuggestQty != 400 {
			t.Fatalf("快照代码写法=%q 应归一到同一只票，got %+v", shape, out[0])
		}
	}
	// 另一只票的行不得串到这只上
	in.Decisions = []store.SignalDecision{decisionRow("000001.SZ", 400, 4200, "", "2026-11-05T10:31:00+08:00", "evaluated")}
	out, _ := Advise(in)
	if out[0].SuggestQty != 0 {
		t.Fatalf("别的票的行不许挂到 600000 上，got %+v", out[0])
	}
}

// TestAdviseSuggestNotAttachedToSellRows 卖出方向的卡片不挂买入量：
// signal_decisions 记的是"这一笔买入算出多少股"，把它显示在减仓/止盈卡上就是
// 把一个买的方向的数标成卖的量（数字同源，方向不同源）。
func TestAdviseSuggestNotAttachedToSellRows(t *testing.T) {
	in := tacticsFixture(t)
	in.Cfg.Strategies = nil
	in.SellUnifiedOn = true
	in.SellProjection = []UnifiedSellView{
		{TsCode: "600000.SH", Action: "止盈", Level: "高", Reason: "[统一裁决] 移动止盈", RefPrice: 10.8, Source: UnifiedSellSourceAction},
	}
	in.Decisions = []store.SignalDecision{decisionRow("600000", 300, 3150, "target", time.Now().Format(time.RFC3339), "evaluated")}
	out, _ := Advise(in)
	var sell *PositionAdvice
	for i := range out {
		if out[i].Code == "600000" {
			sell = &out[i]
		}
	}
	if sell == nil || sell.Action != "止盈" {
		t.Fatalf("600000 应是一张止盈卡，got %+v", out)
	}
	if sell.SuggestQty != 0 || sell.SuggestAmount != 0 {
		t.Fatalf("卖出卡不得带买入量，got %+v", *sell)
	}
}

// TestAdviseFilterStatIsZeroValueWhenNothingDropped 无持仓/无过滤时 AdviceFilter 是零值：
// 调用方（引擎）据此只在 dropped>0 时打日志，0 不许被打成"滤了 0 条"这种噪音行。
func TestAdviseFilterStatIsZeroValueWhenNothingDropped(t *testing.T) {
	empty, f := Advise(AdviceInput{})
	if len(empty) != 0 || f.FilteredBySwitch != 0 {
		t.Fatalf("空持仓应回空列表+零读数，got %d / %+v", len(empty), f)
	}
}
