// manual_adjust_test.go — §MANUAL-BOOK-UNIFY（2026-11-06，owner 裁决 1/2/3/5）存储层行为锁。
//
// 锁的是四件会往钱的方向错的事，按"改坏了不会自己报错"的顺序排：
//
//	① 可用性判据：asof 不属今日 / 已被柜台接管 ⇒ 手动读数当**读不到**，
//	   既不许当"零仓位"放行，也不许拿几天前的旧账顶上今天（裁决 1 的两条前置）；
//	② 增量改账的资金与成本联动必须精确（买扣卖加、加权成本、清仓取账上现量），
//	   且"基线从哪本来"要在流水里回得看到（seeded_from）——没有它，事后没人能解释这个数是谁给的；
//	③ 卖出侧沿用柜台纪律：超量拒、不足一手且非全卖拒——改账不是"随便写数字"的口子；
//	④ 接管只打指针、不删行（证据留在 manual_flows 与 manual_* 里）。
//
// 日期一律从 cntime.Now() 派生（写死日历日期＝隔天必红，这是本仓踩过的那条坑）。
package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
)

// manualToday 当前北京交易日（YYYY-MM-DD）。
func manualToday() string { return cntime.DayOf(cntime.Now()) }

// manualAsofOn 把给定交易日拼成一个墙钟串（asof 的落库形态）。
func manualAsofOn(day string) string { return day + " 10:00:00" }

// manualDayOffset N 个自然日前的交易日（负数＝未来日，用来造"asof 敲成明天"这一格）。
func manualDayOffset(n int) string {
	return cntime.DayOf(cntime.Now().Add(time.Duration(n) * 24 * time.Hour))
}

// TestSnapshotBelongsToDay 公共判据的六格：空/短/不可解析三种"读不懂"保守返回 true（与改前逐字一致），
// 昨天 false、今天 true、明天 true。破坏"早于 today"这一支 ⇒ 必红。
func TestSnapshotBelongsToDay(t *testing.T) {
	today := manualToday()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"空串保守可信", "", true},
		{"短串保守可信", "10:00", true},
		{"不可解析保守可信", "not-a-date 1234", true},
		{"昨天不可信", manualDayOffset(-1) + " 15:00:00", false},
		{"今天可信", today + " 09:30:00", true},
		{"明天可信（不扩大回归面）", manualDayOffset(1) + " 09:30:00", true},
	}
	for _, c := range cases {
		if got := SnapshotBelongsToDay(c.in, today); got != c.want {
			t.Fatalf("%s: SnapshotBelongsToDay(%q, %q)=%v 期望 %v", c.name, c.in, today, got, c.want)
		}
	}
}

// TestManualBookViewUsability 裁决 1 的两条前置判据逐格：
// 今日可用 / 昨天不可用 / 未来不可用 / 被接管且 asof 早于接管不可用 / 接管后又改过重新可用。
func TestManualBookViewUsability(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_unify"
	today := manualToday()

	// ① 今日 asof ⇒ 两维都可用。
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Name: "浦发银行", Qty: 1000, CostPrice: 8.5}}, manualAsofOn(today)); err != nil {
		t.Fatalf("写手动持仓: %v", err)
	}
	if err := db.PutManualAccount(uid, 50000, true, 0, manualAsofOn(today)); err != nil {
		t.Fatalf("写手动资金: %v", err)
	}
	v, err := db.ManualBookViewFor(uid, today)
	if err != nil {
		t.Fatalf("ManualBookViewFor: %v", err)
	}
	if !v.CashUsable || !v.PositionsUsable || len(v.Positions) != 1 {
		t.Fatalf("今日维护的手动账两维都必须可用，got cash=%v pos=%v rows=%d", v.CashUsable, v.PositionsUsable, len(v.Positions))
	}

	// ② asof 停在昨天 ⇒ 两维都当读不到（不许拿旧账顶上今天）。
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Name: "浦发银行", Qty: 1000, CostPrice: 8.5}}, manualAsofOn(manualDayOffset(-1))); err != nil {
		t.Fatalf("改写 asof 到昨天: %v", err)
	}
	if err := db.PutManualAccount(uid, 50000, true, 0, manualAsofOn(manualDayOffset(-1))); err != nil {
		t.Fatalf("改写资金 asof 到昨天: %v", err)
	}
	v, _ = db.ManualBookViewFor(uid, today)
	if v.CashUsable || v.PositionsUsable {
		t.Fatalf("昨天的手动账不许在今天当输入用（cash=%v pos=%v）", v.CashUsable, v.PositionsUsable)
	}

	// ③ asof 敲成未来 ⇒ 同样不可用（人敲错一个日期就解开资金闸，方向是放钱）。
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Qty: 1000}}, manualAsofOn(manualDayOffset(1))); err != nil {
		t.Fatalf("改写 asof 到明天: %v", err)
	}
	v, _ = db.ManualBookViewFor(uid, today)
	if v.PositionsUsable {
		t.Fatal("asof 落在未来的持仓行必须判不可用")
	}

	// ④ 被柜台接管且 asof 不晚于接管 ⇒ 这本账停在被接管那一刻，不许下次桥断时复活。
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Qty: 1000}}, manualAsofOn(today)); err != nil {
		t.Fatalf("改回今日: %v", err)
	}
	if _, err := db.MarkManualTakenOver(uid, today+" 15:00:00"); err != nil {
		t.Fatalf("MarkManualTakenOver: %v", err)
	}
	v, _ = db.ManualBookViewFor(uid, today)
	if v.PositionsUsable || v.CashUsable {
		t.Fatalf("接管时刻晚于 asof 时两维都必须不可用（cash=%v pos=%v taken_over=%q）", v.CashUsable, v.PositionsUsable, v.TakenOverAt)
	}

	// ⑤ 接管之后 owner 又改了一笔（asof 晚于接管）⇒ 重新可用，判据只看时刻先后，不需要额外状态位。
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Qty: 1200}}, today+" 16:30:00"); err != nil {
		t.Fatalf("接管后再改一笔: %v", err)
	}
	v, _ = db.ManualBookViewFor(uid, today)
	if !v.PositionsUsable || len(v.Positions) != 1 || v.Positions[0].Qty != 1200 {
		t.Fatalf("接管后重新维护的行必须重新可用，got %+v", v.Positions)
	}
	// 资金行没再动（asof 仍是昨天）⇒ 两维独立：持仓可用不代表资金可用。
	if v.CashUsable {
		t.Fatal("资金维度必须独立判定：持仓可用不代表资金可用")
	}
}

// TestMarkManualTakenOverIdempotent 接管只打指针、不删行；同刻重放不长读数；更晚的接管覆盖更早的。
func TestMarkManualTakenOverIdempotent(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_tko"
	today := manualToday()
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Qty: 100}, {TsCode: "000001.SZ", Qty: 200}}, manualAsofOn(today)); err != nil {
		t.Fatalf("写手动持仓: %v", err)
	}
	n1, err := db.MarkManualTakenOver(uid, today+" 10:00:00")
	if err != nil {
		t.Fatalf("首次接管: %v", err)
	}
	if n1 != 2 {
		t.Fatalf("两条持仓行都要打标，got %d", n1)
	}
	// 同刻重放：SQL 里带 taken_over_at <> ? 的守卫，重放不得再"改一遍"（幂等要能在读数上看出来）。
	n2, _ := db.MarkManualTakenOver(uid, today+" 10:00:00")
	if n2 != 0 {
		t.Fatalf("同刻重放应零改写，got %d", n2)
	}
	// 行还在（裁决 5：不静默清空）。
	rows, _ := db.ManualPositionsForUser(uid)
	if len(rows) != 2 {
		t.Fatalf("接管不许删行，got %d 行", len(rows))
	}
	if rows[0].TakenOverAt != today+" 10:00:00" {
		t.Fatalf("接管时刻必须回读得到，got %q", rows[0].TakenOverAt)
	}
	// 更晚的接管覆盖更早的。
	if _, err := db.MarkManualTakenOver(uid, today+" 14:00:00"); err != nil {
		t.Fatalf("第二次接管: %v", err)
	}
	rows, _ = db.ManualPositionsForUser(uid)
	if rows[0].TakenOverAt != today+" 14:00:00" {
		t.Fatalf("更晚的接管时刻要覆盖更早的，got %q", rows[0].TakenOverAt)
	}
	// 空时刻一律拒（宁可报错也不写一个"看起来没被接管"的假指针）。
	if _, err := db.MarkManualTakenOver(uid, "  "); err == nil {
		t.Fatal("接管时刻为空必须拒")
	}
}

// TestAdjustAddWeightedCost 加仓：加权成本、买扣资金、流水点名来源。
// 夹具：手动账 1000 股 @8.50 + 资金 50000 ⇒ 加 1000 股 @9.50 ⇒ 2000 股 @9.00、资金 40000。
func TestAdjustAddWeightedCost(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_add"
	today := manualToday()
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Name: "浦发银行", Qty: 1000, CostPrice: 8.5}}, manualAsofOn(today)); err != nil {
		t.Fatalf("播种持仓: %v", err)
	}
	if err := db.PutManualAccount(uid, 50000, true, 0, manualAsofOn(today)); err != nil {
		t.Fatalf("播种资金: %v", err)
	}
	res, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpAdd, TsCode: "600000.SH", Qty: 1000, Price: 9.5, AsOf: today + " 10:30:00"}, today)
	if err != nil {
		t.Fatalf("加仓改账: %v", err)
	}
	if res.Qty != 2000 {
		t.Fatalf("加仓后应 2000 股，got %d", res.Qty)
	}
	if diff := res.CostPrice - 9.0; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("加权成本应 9.00（(1000*8.5+1000*9.5)/2000），got %.4f", res.CostPrice)
	}
	if res.Cash != 50000 || res.CashAfter != 40500 {
		t.Fatalf("买扣资金：基线 50000 ⇒ 改后应为 50000-9500=40500，got %.0f→%.0f", res.Cash, res.CashAfter)
	}
	if res.SeededFrom != ManualSeedManual || res.CashSeeded != ManualSeedManual {
		t.Fatalf("基线两维都来自手动账，got pos=%q cash=%q", res.SeededFrom, res.CashSeeded)
	}
	flows, _ := db.ManualFlowsForUser(uid, today)
	if len(flows) != 1 || flows[0].Op != ManualOpAdd || flows[0].Side != "买入" || flows[0].Qty != 1000 {
		t.Fatalf("改账必须落一行流水，got %+v", flows)
	}
	if flows[0].CashAfter != 40500 {
		t.Fatalf("流水的 cash_after 要与账户改后读数一致，got %.0f", flows[0].CashAfter)
	}
	// 旧成本为 0（没填）时不许把新价稀释成 0。
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "000001.SZ", Qty: 500, CostPrice: 0}}, manualAsofOn(today)); err != nil {
		t.Fatalf("播种零成本行: %v", err)
	}
	res2, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpAdd, TsCode: "000001.SZ", Qty: 500, Price: 11.0, AsOf: today + " 10:31:00"}, today)
	if err != nil {
		t.Fatalf("加零成本行: %v", err)
	}
	if res2.CostPrice != 11.0 {
		t.Fatalf("旧成本未填时新成本取本笔价，不许加权出 0，got %.2f", res2.CostPrice)
	}
}

// TestAdjustSellSideDiscipline 卖出侧三条拒单与两条放行：超量拒、不足一手且非全卖拒、
// 减仓归零删行、清仓取**账上现量**而不是请求量。
func TestAdjustSellSideDiscipline(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_sell"
	today := manualToday()
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Qty: 1000, CostPrice: 8.5}}, manualAsofOn(today)); err != nil {
		t.Fatalf("播种: %v", err)
	}
	if err := db.PutManualAccount(uid, 10000, true, 0, manualAsofOn(today)); err != nil {
		t.Fatalf("播种资金: %v", err)
	}
	// ① 超量拒。
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpReduce, TsCode: "600000.SH", Qty: 1500, Price: 9, AsOf: today + " 11:00:00"}, today); err == nil {
		t.Fatal("减仓超过持仓必须拒")
	} else if !errors.Is(err, ErrInvalidManualAdjust) {
		t.Fatalf("拒单必须是参数校验哨兵，got %v", err)
	}
	// ② 不足一手且不是全卖 ⇒ 拒（§F16 同口径）。
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpReduce, TsCode: "600000.SH", Qty: 50, Price: 9, AsOf: today + " 11:00:00"}, today); err == nil {
		t.Fatal("卖出 50 股（非全卖）必须按柜台同口径拒")
	}
	// ③ 不足一手但正好是整行卖光 ⇒ 放行（残股退出合法）。
	res, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpReduce, TsCode: "600000.SH", Qty: 1000, Price: 9, AsOf: today + " 11:01:00"}, today)
	if err != nil {
		t.Fatalf("全量减仓应放行: %v", err)
	}
	if !res.Deleted || res.Qty != 0 {
		t.Fatalf("减仓归零必须删行，got deleted=%v qty=%d", res.Deleted, res.Qty)
	}
	if res.CashAfter != 19000 {
		t.Fatalf("卖加资金：10000+9000=19000，got %.0f", res.CashAfter)
	}
	rows, _ := db.ManualPositionsForUser(uid)
	if len(rows) != 0 {
		t.Fatalf("归零后清单里不许留 0 股行，got %+v", rows)
	}
	// ④ 清仓取账上现量：请求 999999 也只按账上 500 股加钱。
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "000001.SZ", Qty: 500, CostPrice: 11}}, manualAsofOn(today)); err != nil {
		t.Fatalf("再播种: %v", err)
	}
	res2, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpClose, TsCode: "000001.SZ", Qty: 999999, Price: 12, AsOf: today + " 11:02:00"}, today)
	if err != nil {
		t.Fatalf("清仓: %v", err)
	}
	if res2.CashAfter != 19000+6000 {
		t.Fatalf("清仓必须按账上现量 500×12=6000 加钱，got %.0f", res2.CashAfter)
	}
	// ⑤ 不在账上的码：减仓/清仓都拒。
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpClose, TsCode: "300750.SZ", Qty: 100, Price: 200, AsOf: today + " 11:03:00"}, today); err == nil {
		t.Fatal("清仓一个不在账上的码必须拒，不许凭空造出现金")
	}
}

// TestAdjustNewAndSeed 改账·新增的三条边界：已有手动行拒、柜台账已有该码拒（该走加仓）、
// 柜台从未回报的码可建；以及基线从柜台账继承时 seeded_from 必须点名。
func TestAdjustNewAndSeed(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_new"
	today := manualToday()
	// 柜台账：600000 有 1000 股（桥断当天冻结的样子），资金 88888。
	if _, err := db.ReconcilePositionsForUser(uid, []RealPosition{{TsCode: "600000.SH", Name: "浦发银行", Qty: 1000, CostPrice: 8.4, UpdatedAt: today + " 14:50:00"}}); err != nil {
		t.Fatalf("播种柜台持仓: %v", err)
	}
	if err := db.UpsertRealAccount(RealAccount{UserID: uid, AvailableCash: 88888, UpdatedAt: today + " 14:50:00"}); err != nil {
		t.Fatalf("播种柜台资金: %v", err)
	}
	// ① 柜台有、手动没有 ⇒ new 拒（该走加仓，否则会把柜台量当 0 覆盖掉）。
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpNew, TsCode: "600000.SH", Qty: 100, Price: 9, AsOf: today + " 15:00:00"}, today); err == nil {
		t.Fatal("柜台账已有的码不许走「新增」，必须走加仓（否则柜台量会被当 0 覆盖）")
	}
	// ② 加仓走柜台基线：1000 股 @8.4 + 100 股 @9.0 ⇒ 1100 股，资金 88888-900。
	res, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpAdd, TsCode: "600000.SH", Qty: 100, Price: 9.0, AsOf: today + " 15:01:00"}, today)
	if err != nil {
		t.Fatalf("以柜台为基线加仓: %v", err)
	}
	if res.Qty != 1100 || res.SeededFrom != ManualSeedCounter || res.CashSeeded != ManualSeedCounter {
		t.Fatalf("两维基线都要点名来自柜台，got qty=%d pos=%q cash=%q", res.Qty, res.SeededFrom, res.CashSeeded)
	}
	if res.CashAfter != 88888-900 {
		t.Fatalf("资金基线取柜台 88888，got %.0f", res.CashAfter)
	}
	// ③ 柜台从未回报的码 ⇒ 新增成功。
	res3, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpNew, TsCode: "300750.SZ", Qty: 200, Price: 200, AsOf: today + " 15:02:00"}, today)
	if err != nil {
		t.Fatalf("新增柜台没回报过的码: %v", err)
	}
	if res3.Qty != 200 || res3.CostPrice != 200 || res3.SeededFrom != ManualSeedNone {
		t.Fatalf("新增应建一行且基线来源=none，got %+v", res3)
	}
	// ④ 再点一次新增 ⇒ 拒（该走加仓），清单不长重复行。
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpNew, TsCode: "300750.SZ", Qty: 100, Price: 210, AsOf: today + " 15:03:00"}, today); err == nil {
		t.Fatal("同一码第二次「新增」必须拒")
	}
	rows, _ := db.ManualPositionsForUser(uid)
	if len(rows) != 2 {
		t.Fatalf("手动清单应 2 行（600000 + 300750），got %d", len(rows))
	}
	// ⑤ 柜台账与手动账都没这一码 ⇒ 减仓/加仓都拒，不许"算出来是 0 股"。
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpAdd, TsCode: "601318.SH", Qty: 100, Price: 40, AsOf: today + " 15:04:00"}, today); err == nil {
		t.Fatal("两本账都没有这一码，加仓必须拒并指路「改账·新增」")
	}
}

// TestAdjustCashBaselineMissingRefuses 资金基线两本账都拿不到 ⇒ 整笔拒，且文案给出可操作补救。
// 这一条锁的是"从 0 起算"那个方向性错误：卖出会凭空造现金、买入直接判负数。
func TestAdjustCashBaselineMissingRefuses(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_nobase"
	today := manualToday()
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Qty: 1000, CostPrice: 8.5}}, manualAsofOn(today)); err != nil {
		t.Fatalf("播种持仓: %v", err)
	}
	_, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpReduce, TsCode: "600000.SH", Qty: 500, Price: 9, AsOf: today + " 15:10:00"}, today)
	if err == nil {
		t.Fatal("资金基线不可得时必须整笔拒")
	}
	if !errors.Is(err, ErrInvalidManualAdjust) {
		t.Fatalf("必须是参数校验哨兵，got %v", err)
	}
	if !strings.Contains(err.Error(), "直接改资金") {
		t.Fatalf("拒单文案要给可操作补救（先录资金基线），got %q", err.Error())
	}
	// 拒单之后账一点没动（不许写一半）。
	rows, _ := db.ManualPositionsForUser(uid)
	if len(rows) != 1 || rows[0].Qty != 1000 {
		t.Fatalf("被拒的改账不许留下半截持仓，got %+v", rows)
	}
	st, _ := db.ManualAccountForUser(uid)
	if st.Present {
		t.Fatal("被拒的改账不许凭空造出一行手动资金")
	}
}

// TestAdjustSetCashKeepsPositions 直接改资金：不动持仓、允许真 0（三态里的"真 0"），并落流水。
func TestAdjustSetCashKeepsPositions(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_setcash"
	today := manualToday()
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Qty: 1000, CostPrice: 8.5}}, manualAsofOn(today)); err != nil {
		t.Fatalf("播种: %v", err)
	}
	if err := db.PutManualAccount(uid, 50000, true, 0, manualAsofOn(today)); err != nil {
		t.Fatalf("播种资金: %v", err)
	}
	zero := 0.0
	res, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpSetCash, Cash: &zero, AsOf: today + " 15:20:00"}, today)
	if err != nil {
		t.Fatalf("直接改资金为 0: %v", err)
	}
	if res.CashAfter != 0 {
		t.Fatalf("真 0 必须写进去（不是当成未填），got %.2f", res.CashAfter)
	}
	st, _ := db.ManualAccountForUser(uid)
	if !st.Present || !st.Filled || st.Account.AvailableCash != 0 {
		t.Fatalf("三态必须停在 filled：present=%v filled=%v cash=%.2f", st.Present, st.Filled, st.Account.AvailableCash)
	}
	rows, _ := db.ManualPositionsForUser(uid)
	if len(rows) != 1 || rows[0].Qty != 1000 {
		t.Fatalf("改资金不许动持仓，got %+v", rows)
	}
	flows, _ := db.ManualFlowsForUser(uid, today)
	if len(flows) != 1 || flows[0].Op != ManualOpSetCash || flows[0].Side != "资金" {
		t.Fatalf("set_cash 也要落流水，got %+v", flows)
	}
	neg := -1.0
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpSetCash, Cash: &neg, AsOf: today + " 15:21:00"}, today); err == nil {
		t.Fatal("负可用资金必须拒")
	}
}

// TestManualFlowsDayKeyed 流水按北京交易日分日：新交易日不复活、不累计（与 signal_decisions 同纪律）。
func TestManualFlowsDayKeyed(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_flows"
	today := manualToday()
	yesterday := manualDayOffset(-1)
	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "600000.SH", Qty: 1000, CostPrice: 8.5}}, manualAsofOn(today)); err != nil {
		t.Fatalf("播种: %v", err)
	}
	if err := db.PutManualAccount(uid, 50000, true, 0, manualAsofOn(today)); err != nil {
		t.Fatalf("播种资金: %v", err)
	}
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpAdd, TsCode: "600000.SH", Qty: 100, Price: 9, AsOf: today + " 10:00:00"}, today); err != nil {
		t.Fatalf("今日加仓: %v", err)
	}
	if todayBuy, err := db.ManualTodayBoughtQty(uid, "600000.SH", today); err != nil || todayBuy != 100 {
		t.Fatalf("今日改账买入应 100，got %d err=%v", todayBuy, err)
	}
	// 换一天读：昨天的量不许算进今天。
	if yBuy, err := db.ManualTodayBoughtQty(uid, "600000.SH", yesterday); err != nil || yBuy != 0 {
		t.Fatalf("昨日应 0（新交易日不复活），got %d err=%v", yBuy, err)
	}
	// 卖出腿不计入当日买入（T+1 只扣买入，不扣卖出）。
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpReduce, TsCode: "600000.SH", Qty: 100, Price: 9.5, AsOf: today + " 10:05:00"}, today); err != nil {
		t.Fatalf("今日减仓: %v", err)
	}
	if buy, _ := db.ManualTodayBoughtQty(uid, "600000.SH", today); buy != 100 {
		t.Fatalf("卖出腿不许计进当日买入，got %d", buy)
	}
	all, _ := db.ManualFlowsForUser(uid, today)
	if len(all) != 2 {
		t.Fatalf("今日两笔改账都要在流水里，got %d", len(all))
	}
}

// TestAdjustNeverWritesCounterTables 裁决 1 的前提：改账与接管都不许碰 real_*。
// 这条不是"设计说明"——它数的是两张柜台输入表的行数与读数，改前逐字不变才算过。
func TestAdjustNeverWritesCounterTables(t *testing.T) {
	db := manualTestDB(t)
	uid := "u_isolate"
	today := manualToday()
	if _, err := db.ReconcilePositionsForUser(uid, []RealPosition{{TsCode: "600000.SH", Qty: 1000, CostPrice: 8.4, UpdatedAt: today + " 14:50:00"}}); err != nil {
		t.Fatalf("播种柜台持仓: %v", err)
	}
	if err := db.UpsertRealAccount(RealAccount{UserID: uid, AvailableCash: 88888, UpdatedAt: today + " 14:50:00"}); err != nil {
		t.Fatalf("播种柜台资金: %v", err)
	}
	beforePos, _ := db.RealPositionsForUser(uid)
	beforeAcc, _ := db.GetRealAccountOwn(uid)

	if _, _, err := db.PutManualPositions(uid, []ManualPosition{{TsCode: "000001.SZ", Qty: 300, CostPrice: 11}}, manualAsofOn(today)); err != nil {
		t.Fatalf("全量写手动: %v", err)
	}
	if _, err := db.AdjustManualBook(uid, ManualAdjustReq{Op: ManualOpAdd, TsCode: "600000.SH", Qty: 100, Price: 9, AsOf: today + " 15:00:00"}, today); err != nil {
		t.Fatalf("增量改账: %v", err)
	}
	if _, err := db.MarkManualTakenOver(uid, today+" 15:30:00"); err != nil {
		t.Fatalf("接管打标: %v", err)
	}

	afterPos, _ := db.RealPositionsForUser(uid)
	afterAcc, _ := db.GetRealAccountOwn(uid)
	if len(afterPos) != len(beforePos) {
		t.Fatalf("改账把行写进了 real_positions（改前 %d 行，改后 %d 行）——柜台为准这条判据的前提被破", len(beforePos), len(afterPos))
	}
	for i := range beforePos {
		if afterPos[i].TsCode != beforePos[i].TsCode || afterPos[i].Qty != beforePos[i].Qty {
			t.Fatalf("柜台持仓行被改账污染：%+v → %+v", beforePos[i], afterPos[i])
		}
	}
	if afterAcc.AvailableCash != beforeAcc.AvailableCash || afterAcc.UpdatedAt != beforeAcc.UpdatedAt {
		t.Fatalf("柜台资金行被改账污染（cash %.0f→%.0f，updated_at %q→%q）——手敲值一旦进这张表，§M12-A 会把人敲的数读成刚回报",
			beforeAcc.AvailableCash, afterAcc.AvailableCash, beforeAcc.UpdatedAt, afterAcc.UpdatedAt)
	}
}
