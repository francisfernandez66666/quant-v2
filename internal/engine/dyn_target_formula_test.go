// dyn_target_formula_test.go — §W-06-R1..R3（2026-10-29 夜批）：目标仓位算式合一。
//
// 背景：同一算式两处实现（`funds_today.go fundsTargetCurRoom` 与 `engine.go` 里就地重算的
// targetRatio/curStrat/targetVal/room），可以各自漂移且没有任何报警。本文件把三件事钉死：
//   - F0/F1：两条路径在**同输入**下逐分相等（差异只剩刻意的输入差异：价格源、缺价策略）；
//   - F2：在途正 T 买腿**不占**战略空间（裁决 R1，中性）；
//   - F3：已成交正 T 买腿**只扣一次**（裁决 R2）。
//
// 关键：F0 里保留了一份 `legacyEngineCur`——它复刻修复**之前** engine 的算式。留着它不是怀旧，
// 是让"为什么必须删 :1679 那一行"变成可执行的证据：新旧差值必须**恰等于**在途 T 买腿市值。
package engine

import (
	"math"
	"os"
	"strings"
	"testing"

	"quant-trading-v2/internal/store"
)

// legacyEngineCur 复刻 §W-06 修复**之前** engine.go 的当前仓位算式（含 :1679 的多扣）。
func legacyEngineCur(positions map[string]store.RealPosition, prices map[string]float64,
	pending []store.PendingBuy, book tLiveBook) float64 {
	var strat, tBuy, tSell float64
	for code, pos := range positions {
		px := prices[code]
		if px <= 0 {
			px = pos.CostPrice
		}
		strat += px * float64(pos.Qty)
	}
	for _, pb := range pending {
		v := pb.Price * float64(pb.Remain)
		if pb.IntradayT {
			tBuy += v // ← 就是这一行：strat 从没加过它，扣它等于扣第二次
			continue
		}
		strat += v
	}
	tBuy += book.BuyLegValue
	tSell += book.SellLegValue
	cur := strat - tBuy + tSell
	if cur < 0 {
		cur = 0
	}
	return cur
}

func dynPos(code string, qty int, cost, cur float64) store.RealPosition {
	return store.RealPosition{TsCode: code + ".SH", Qty: qty, CostPrice: cost, CurPrice: cur}
}

// F0：特征断言——修复前后的差值**恰等于**在途 T 买腿市值。
// 若哪天有人"顺手"把那一行加回去，这条会立刻红，并且报出被多扣了多少。
func TestDynTargetFormulaParity(t *testing.T) {
	const total, ms, ceiling = 200000.0, 0.6, 1.0
	positions := map[string]store.RealPosition{"600000": dynPos("600000", 10000, 10, 10)}
	prices := map[string]float64{"600000": 10}
	pend := []store.PendingBuy{{Code: "600001.SH", Price: 10, Remain: 2000, IntradayT: true}}
	const legValue = 10 * 2000.0 // 2 万

	_, curNew, _, ok := fundsTargetCurRoom(total, ms, ceiling, positions, prices, pend, tLiveBook{})
	if !ok {
		t.Fatal("fundsTargetCurRoom 应返回 ok=true")
	}
	curOld := legacyEngineCur(positions, prices, pend, tLiveBook{})
	if diff := curNew - curOld; math.Abs(diff-legValue) > 1e-6 {
		t.Fatalf("修复前后的差值必须恰等于在途 T 买腿市值 %.0f（多扣就是这里出问题），实得 %.6f", legValue, diff)
	}
	// 金额举例（§W-06「金额举例」）：总资产 20 万、MS 0.6 ⇒ 目标 12 万；持仓 10 万。
	// 裁决前 cur=8 万 room=4 万；裁决后 cur=10 万 room=2 万。
	if math.Abs(curNew-100000) > 1e-6 {
		t.Fatalf("裁决后 cur 应为 100000（持仓 10 万，在途 T 腿不进 cur），实得 %.2f", curNew)
	}
	if _, _, room, _ := fundsTargetCurRoom(total, ms, ceiling, positions, prices, pend, tLiveBook{}); math.Abs(room-20000) > 1e-6 {
		t.Fatalf("裁决后 room 应为 20000（12 万 − 10 万），实得 %.2f", room)
	}
}

// F1：6 组同输入 fixture ⇒ 新旧算式全等（差别只在"有在途 T 腿"这一种情形，已由 F0 覆盖）。
func TestDynTargetSameInputsEqual(t *testing.T) {
	type fx struct {
		name      string
		total     float64
		ms, ceil  float64
		positions map[string]store.RealPosition
		prices    map[string]float64
		pending   []store.PendingBuy
		book      tLiveBook
	}
	cases := []fx{
		{"空仓", 100000, 1.0, 1.0, nil, nil, nil, tLiveBook{}},
		{"半仓", 200000, 0.6, 1.0, map[string]store.RealPosition{"600000": dynPos("600000", 10000, 10, 10)}, map[string]float64{"600000": 10}, nil, tLiveBook{}},
		{"天花板压住 MS", 100000, 0.9, 0.3, map[string]store.RealPosition{"600000": dynPos("600000", 2000, 10, 10)}, map[string]float64{"600000": 10}, nil, tLiveBook{}},
		{"战略在途买单", 100000, 0.5, 1.0, nil, nil, []store.PendingBuy{{Code: "600002.SH", Price: 10, Remain: 1000}}, tLiveBook{}},
		{"已成交正T买腿", 100000, 1.0, 1.0, map[string]store.RealPosition{"600000": dynPos("600000", 3000, 10, 10)}, map[string]float64{"600000": 10}, nil, tLiveBook{BuyLegValue: 10000}},
		{"已成交反T卖腿", 100000, 1.0, 1.0, map[string]store.RealPosition{"600000": dynPos("600000", 1000, 10, 10)}, map[string]float64{"600000": 10}, nil, tLiveBook{SellLegValue: 10000, FrozenCash: 10050}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, curNew, roomNew, _ := fundsTargetCurRoom(c.total, c.ms, c.ceil, c.positions, c.prices, c.pending, c.book)
			curOld := legacyEngineCur(c.positions, c.prices, c.pending, c.book)
			if math.Abs(curNew-curOld) > 1e-9 {
				t.Fatalf("同输入必须逐分相等：新 %.9f 旧 %.9f（差 %.9f）", curNew, curOld, curNew-curOld)
			}
			target := c.total * math.Min(c.ms, c.ceil)
			if math.Abs(roomNew-math.Max(0, target-curNew)) > 1e-9 {
				t.Fatalf("room 必须等于 max(0, target−cur)：room=%.9f target=%.2f cur=%.9f", roomNew, target, curNew)
			}
		})
	}
}

// F2（裁决 R1）：只加一条在途正 T 买腿 ⇒ room **不变**。
func TestInFlightTBuyLegDoesNotOccupySpace(t *testing.T) {
	const total, ms, ceiling = 200000.0, 0.6, 1.0
	positions := map[string]store.RealPosition{"600000": dynPos("600000", 10000, 10, 10)}
	prices := map[string]float64{"600000": 10}
	_, _, roomBefore, _ := fundsTargetCurRoom(total, ms, ceiling, positions, prices, nil, tLiveBook{})
	pend := []store.PendingBuy{{Code: "600000.SH", Price: 10, Remain: 2000, IntradayT: true}}
	_, curAfter, roomAfter, _ := fundsTargetCurRoom(total, ms, ceiling, positions, prices, pend, tLiveBook{})
	if math.Abs(roomAfter-roomBefore) > 1e-9 {
		t.Fatalf("在途 T 买腿不许占战略空间：room %.2f → %.2f", roomBefore, roomAfter)
	}
	if math.Abs(curAfter-100000) > 1e-9 {
		t.Fatalf("在途 T 买腿不许进 cur：期望 100000，实得 %.2f", curAfter)
	}
}

// F3（裁决 R2）：已成交正 T 买腿 ⇒ room 减少其市值，且**只减一次**。
func TestFilledTBuyLegIsDeductedOnce(t *testing.T) {
	// 目标要高于当前仓位，否则 room 被 max(0,·) 压成 0，两种口径都看不出差别（第一版就踩了这个坑）。
	const total, ms, ceiling = 400000.0, 0.6, 1.0                                         // 目标 24 万
	positions := map[string]store.RealPosition{"600000": dynPos("600000", 13000, 10, 10)} // 底仓 1000 + 机动 300 已进券商持仓
	prices := map[string]float64{"600000": 10}
	_, _, roomNoBook, _ := fundsTargetCurRoom(total, ms, ceiling, positions, prices, nil, tLiveBook{})
	book := tLiveBook{BuyLegValue: 3000, OpenCount: 1}
	_, _, roomBook, _ := fundsTargetCurRoom(total, ms, ceiling, positions, prices, nil, book)
	if d := roomBook - roomNoBook; math.Abs(d-3000) > 1e-9 {
		t.Fatalf("已成交 T 买腿必须让 room 多出 3000（cur 少算一次），实得 %.2f（双倍扣减会是 6000）", d)
	}
}

// F4（裁决 R3）：腿转终态 ⇒ 冻结款释放、不再计入 SellLegValue ⇒ room 回到"没有这条腿"的水平。
func TestDeadLegReleasesFrozenCash(t *testing.T) {
	prices := map[string]float64{"600000": 10}
	st := &tLiveStore{byID: map[string]*TRoundLive{
		"t0:600000:sell_first:20261029:1": {
			ID: "t0:600000:sell_first:20261029:1", Code: "600000", Dir: "sell_first",
			Status: "converted", SellFilled: 1000, SellPrice: 10, FrozenCash: 10050,
		},
	}}
	book := st.bookValue(prices)
	if book.FrozenCash != 0 || book.SellLegValue != 0 || book.OpenCount != 0 {
		t.Fatalf("终态腿必须完全退出账本：FrozenCash=%.2f SellLegValue=%.2f OpenCount=%d",
			book.FrozenCash, book.SellLegValue, book.OpenCount)
	}
	// 对照：同一条腿还在 open ⇒ 冻结款与卖腿市值都在（证明上面不是"函数恒返回 0"）。
	st.byID["t0:600000:sell_first:20261029:1"].Status = "open"
	book = st.bookValue(prices)
	if book.FrozenCash != 10050 || book.SellLegValue != 10000 {
		t.Fatalf("open 腿必须计入：FrozenCash=%.2f SellLegValue=%.2f", book.FrozenCash, book.SellLegValue)
	}
}

// F5（差异②③）：两处价格源不同 ⇒ cur 的差 == (quotes − CurPrice) × 数量，解析解，不许"差不多"。
func TestPriceSourceDifferenceIsExplained(t *testing.T) {
	const total, ms, ceiling = 200000.0, 1.0, 1.0
	positions := map[string]store.RealPosition{"600000": dynPos("600000", 1000, 10, 10)}
	cardPrices := map[string]float64{"600000": 11}  // 卡片：优先实时行情
	engPrices := map[string]float64{"600000": 10.5} // 执法：持仓行 CurPrice
	_, curCard, _, _ := fundsTargetCurRoom(total, ms, ceiling, positions, cardPrices, nil, tLiveBook{})
	_, curEng, _, _ := fundsTargetCurRoom(total, ms, ceiling, positions, engPrices, nil, tLiveBook{})
	want := (11.0 - 10.5) * 1000
	if d := curCard - curEng; math.Abs(d-want) > 1e-9 {
		t.Fatalf("两处 cur 的差必须 == (quotes−CurPrice)×数量 = %.4f，实得 %.4f", want, d)
	}
}

// F9（前提 C）：现金不在这个公式里扣——冻结款只在预算层扣一次（engine 的 antiCash = cash − frozenCash）。
// 源码级断言：函数体里不许出现 cash / FrozenCash，否则就是"第二处扣现金"。
func TestFormulaNeverTouchesCash(t *testing.T) {
	body := funcBody(t, "funds_today.go", "fundsTargetCurRoom")
	for _, bad := range []string{"FrozenCash", "cash", "AvailableCash"} {
		if strings.Contains(body, bad) {
			t.Fatalf("fundsTargetCurRoom 不许碰现金（%q 出现在函数体里）——现金只由预算层的 antiCash 扣一次", bad)
		}
	}
}

// funcBody 取某个顶层函数（或方法）的函数体源码，供源码级断言使用。
// 从 `func <name>(` 起，到下一个行首 `}` 为止（本仓 Go 代码统一 gofmt，顶层函数体不会出现行首 `}`）。
func funcBody(t *testing.T, file, name string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读 %s: %v", file, err)
	}
	s := string(raw)
	marker := "func " + name + "("
	i := strings.Index(s, marker)
	if i < 0 {
		marker = "func ("
		// 方法：形如 func (r *tLiveRound) name(
		idx := strings.Index(s, ") "+name+"(")
		if idx < 0 {
			t.Fatalf("%s 里找不到 %s", file, name)
		}
		i = strings.LastIndex(s[:idx], marker)
		if i < 0 {
			t.Fatalf("%s 里找不到 %s 的函数头", file, name)
		}
	}
	rest := s[i:]
	if j := strings.Index(rest, "\n}\n"); j > 0 {
		return rest[:j]
	}
	return rest
}
