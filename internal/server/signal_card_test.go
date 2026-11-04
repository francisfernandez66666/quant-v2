// signal_card_test.go — §2.12 信号卡装配的行为锁（2026-11-04，外脑化 B1' 批）。
//
// 锁的是"这张卡不许说谎"，四条：
//
//	① 缺股数 ⇒ executable=false，且 missing_fields 里**点名叫出「股数」**——
//	   不点名的"数据缺失"等于让人自己猜缺的是价格还是数量，而这两者的可执行含义完全不同；
//	② 缺的读数一个都不许被折成 0 或默认值：0 股在卡面上与"建议 0 股"长得一模一样，
//	   0 价在卡面上是"这支票免费"（§M12-A 真 0 与不可得不得混写同族）；
//	③ 方向只认已知枚举（买入/卖出/buy/sell），其它一律判缺——不从"减仓/清仓/做多"里猜多空
//	   （与 §SIDE-AUTH-2 同一条纪律：方向是花真钱的字段）；
//	④ 定权快照里 rejected/skipped 的行**不许当数量来源**：那是引擎明确拒绝过的仓位，
//	   把它印成可执行卡的数量就是让人去买一笔被拒的敞口。
package server

import (
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// cardArgs BuildSignalCard 入参的夹具形态（用例按字段逐项拿掉一个读数）。
type cardArgs struct {
	signalID  string
	code      string
	name      string
	side      string
	price     float64
	priceKind string
	reason    string
	conf      string
	expiry    string
	qty       int
	qtyKnown  bool
}

// defaultCardArgs 八字段齐全的基线夹具。
func defaultCardArgs() cardArgs {
	return cardArgs{
		signalID: "buy:600000:dragon:20261104", code: "600000.SH", name: "浦发银行", side: "买入",
		price: 8.52, priceKind: "live", reason: "龙头回封｜竞价强", conf: "82.0 分 · strong",
		expiry: "2026-11-04 15:00:00", qty: 1000, qtyKnown: true,
	}
}

// build 夹具 → 卡片（单点走一次构造器，用例只关心"拿掉了什么"）。
func (a cardArgs) build() SignalCard {
	return BuildSignalCard(a.signalID, a.code, a.name, a.side, a.price, a.priceKind,
		a.reason, a.conf, a.expiry, a.qty, a.qtyKnown)
}

// TestSignalCardCompleteIsExecutable ①的反面：字段齐 ⇒ 可执行、missing 为空。
// 这条不成立的话，下面所有"缺字段"的用例都可能绿在同一个方向上（恒 false 的实现也能过）。
func TestSignalCardCompleteIsExecutable(t *testing.T) {
	c := defaultCardArgs().build()
	if !c.Executable || len(c.Missing) != 0 {
		t.Fatalf("八字段齐全却判不可执行：%+v missing=%v", c, c.Missing)
	}
	if c.Qty != 1000 || c.Side != "买入" || c.SignalID == "" {
		t.Fatalf("读数装配不对：%+v", c)
	}
	if c.SourceLabel != "量仔建议" {
		t.Fatalf("卡片必须标明自己只是建议、不是柜台指令：%q", c.SourceLabel)
	}
}

// TestSignalCardMissingQty ①+②（本批最核心的一条）：没有数量读数时不可执行、点名「股数」、qty 保持 0。
// 把"缺数量"折成 0 股或折成 100 股（一手起）都是伪造敞口——前者会让人以为"可以卖 0 股"，
// 后者会让人下一笔自己都没算过的单。
func TestSignalCardMissingQty(t *testing.T) {
	a := defaultCardArgs()
	a.qty, a.qtyKnown = 0, false
	c := a.build()
	if c.Executable {
		t.Fatalf("缺股数仍判可执行：%+v", c)
	}
	if !hasStr(c.Missing, CardFieldQty) {
		t.Fatalf("missing_fields 没点名的股数：%v", c.Missing)
	}
	if c.Qty != 0 {
		t.Fatalf("缺读数被折成了数：%d", c.Qty)
	}
}

// TestSignalCardEachMissingFieldIsNamed ①：每个读数单独缺失时都被点名、都判不可执行。
// 为什么不只测股数：只判股数的实现会把"缺方向/缺代码"的卡标成可执行，
// 而那种卡在页面上与真卡长得一模一样。
func TestSignalCardEachMissingFieldIsNamed(t *testing.T) {
	cases := []struct {
		label  string
		mutate func(a *cardArgs)
		want   string
	}{
		{"股票名", func(a *cardArgs) { a.name = "" }, CardFieldStockName},
		{"代码", func(a *cardArgs) { a.code = "" }, CardFieldCode},
		{"方向", func(a *cardArgs) { a.side = "watch" }, CardFieldSide},
		{"股数", func(a *cardArgs) { a.qtyKnown = false }, CardFieldQty},
		{"参考价", func(a *cardArgs) { a.price = 0 }, CardFieldPrice},
		{"策略/理由", func(a *cardArgs) { a.reason = "" }, CardFieldReason},
		{"置信/档位", func(a *cardArgs) { a.conf = "" }, CardFieldConf},
		{"有效期", func(a *cardArgs) { a.expiry = "" }, CardFieldExpiry},
	}
	for _, tc := range cases {
		a := defaultCardArgs()
		tc.mutate(&a)
		got := a.build()
		if got.Executable {
			t.Fatalf("%s 缺失却判可执行：%+v", tc.label, got)
		}
		if !hasStr(got.Missing, tc.want) {
			t.Fatalf("%s 缺失未在 missing_fields 点名：%v", tc.label, got.Missing)
		}
	}
}

// TestNormalizeCardSide ③：方向词表只认两组已知枚举，其它一律判缺（不猜多空）。
func TestNormalizeCardSide(t *testing.T) {
	for in, want := range map[string]string{
		"买入":    "买入",
		"卖出":    "卖出",
		" buy":  "买入",
		"sell":  "卖出",
		"做多":    "",
		"做空":    "",
		"清仓":    "",
		"减仓":    "",
		"watch": "",
		"":      "",
	} {
		if got := normalizeCardSide(in); got != want {
			t.Fatalf("normalizeCardSide(%q)=%q want %q", in, got, want)
		}
	}
}

// TestCardQtyRejectsBlockedDecisions ④：定权快照里被拦/跳过/零数量的行不能当数量来源。
func TestCardQtyRejectsBlockedDecisions(t *testing.T) {
	for _, stage := range []string{"rejected", "skipped", "", "unknown"} {
		if q, ok := cardQty(&store.SignalDecision{Stage: stage, Qty: 500}); ok || q != 0 {
			t.Fatalf("stage=%q 的行被当成数量来源：%d ok=%v", stage, q, ok)
		}
	}
	if q, ok := cardQty(&store.SignalDecision{Stage: "evaluated", Qty: 0}); ok || q != 0 {
		t.Fatalf("qty=0 不等于有读数：%d ok=%v", q, ok)
	}
	for _, stage := range []string{"evaluated", "placed"} {
		if q, ok := cardQty(&store.SignalDecision{Stage: stage, Qty: 300}); !ok || q != 300 {
			t.Fatalf("stage=%q 应给出数量：%d ok=%v", stage, q, ok)
		}
	}
	if _, ok := cardQty(nil); ok {
		t.Fatal("nil 快照必须判没有读数")
	}
}

// TestCardQtyKeysMatchDecisionWriteSide ⑤：取数键必须与**写入侧**同源。
// signal_decisions.code 落的是纯 6 位码（engine.pureTsCode）、strategy_key 落的是规范战法键
// （signalctl.StrategyKeyOf），而展示侧的 Code 带 .SH 后缀、Strategy 是战法名。
// 键空间不同源的形态不是报错，是"信号一直在、卡上永远缺股数"——所以两侧一起钉住。
func TestCardQtyKeysMatchDecisionWriteSide(t *testing.T) {
	dec := store.SignalDecision{Code: "600000", Strategy: "dragon", Stage: "evaluated", Qty: 800}
	idx := map[string]int{cardQtyKey(dec.Code, dec.Strategy): 800}
	f := fixSignal{Code: "600000.SH", Strategy: "龙头", StrategyType: "dragon"}
	got, ok := 0, false
	for _, k := range cardQtyKeys(f) {
		if v, hit := idx[k]; hit {
			got, ok = v, true
			break
		}
	}
	if !ok || got != 800 {
		t.Fatalf("候选键与定权快照的键空间对不上（卡面会永远缺股数）：keys=%v", cardQtyKeys(f))
	}
	// 只有战法名、没有规范键的信号：退回名字，但仍然不许跨票串号
	for _, k := range cardQtyKeys(fixSignal{Code: "000001.SZ", Strategy: "龙头"}) {
		if !strings.HasPrefix(k, "000001|") {
			t.Fatalf("候选键没带纯代码前缀（会串到别的票上）：%q", k)
		}
	}
}

// TestSignalIDForCardFallback 去重键不许为空：空键＝三通道无法去重，同一条信号会弹两次。
func TestSignalIDForCardFallback(t *testing.T) {
	if id := signalIDForCard("sig-9", "600000.SH", "龙头", "2026-11-04 10:00:00"); id != "sig-9" {
		t.Fatalf("有 signal_id 时必须用它：%q", id)
	}
	id := signalIDForCard("", "600000.SH", "龙头", "2026-11-04 10:00:00")
	if id == "" || !strings.Contains(id, "600000.SH") {
		t.Fatalf("signal_id 缺失时的兜底键必须仍在且能定位到这只票：%q", id)
	}
}

// hasStr missing 是有序清单，这里只判有没有。
func hasStr(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
