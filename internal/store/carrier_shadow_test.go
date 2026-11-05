// carrier_shadow_test.go — §CARRIER-SHADOW（2026-11-05）载体影子决策的存储层行为锁。
//
// 钉的是"5 日对照窗口的分子不能被做脏"这一族，而不是"函数能不能跑通"：
//
//	① 主键幂等：上行腿重启后把同一份账本重发一遍，落库必须还是**一行**——
//	   否则 5 日窗口会被重复计数喂成一个早就满足的假就绪态（owner 的武装按钮就按在假数上）；
//	② day 进主键且跨日隔离：昨天的行不许在今天被读出来、也不许被今天同短码那行覆盖
//	   （§DECISION-TODAY 的 day-keyed 口径：一天一意图，跨日不复活、不累计）；
//	③ NULL ≠ 0：amount/market_scale/target_scale 缺失必须回 nil——写 0 会被对照表读成
//	   "定权给了零仓位"，那是把"载体没算"伪装成"载体算出零"（missing≠empty 的存储面落形）；
//	④ 两张分账（按日 / 按 (种类,日)）各自独立：把"这一天盒子静默"与"这一天报了但这类没命中"
//	   折成同一件事，前端就只能猜——所以两张账必须能互相区分出对方的那种缺数据形态；
//	⑤ 逐行拒收：坏 day / 空短码 / 空种类 / 错 ts_code 一行都不许落，但**不牵连好行**；
//	⑥ 空 uid 拒绝写入 + 删号只清该账号私有观测行（orders/fills 一律不碰，§F18 证据链）。
package store

import (
	"strings"
	"testing"
)

// f64 取地址小工具（*float64 三枚可空列在夹具里写起来更短）。
func f64(v float64) *float64 { return &v }

// carrierShadowFixture 一条合法影子决策（默认可被各用例按字段覆盖）。
func carrierShadowFixture(mut ...func(*CarrierShadowDecision)) CarrierShadowDecision {
	r := CarrierShadowDecision{
		Day: "20261105", ShortCode: "DR600000", TsCode: "600000.SH", Kind: "dragon",
		Side: "buy", Qty: 100, Price: 10.0, Amount: f64(1000), MarketScale: f64(0.8),
		TargetScale: f64(0.5), SizingNote: "capped_by=target", DecidedAt: "2026-11-05T09:31:00+08:00",
	}
	for _, fn := range mut {
		fn(&r)
	}
	return r
}

// TestCarrierShadowRoundTrip 写入→读回：全字段（含三枚可空列）必须原样回来。
// 只断言"能查到"的用例兜不住 ③——可空列被折成 0 同样是"能查到"。
func TestCarrierShadowRoundTrip(t *testing.T) {
	db := testDB(t)
	written, rejected, err := db.UpsertCarrierShadowRows("u_cs", "2026-11-05T09:35:00+08:00",
		[]CarrierShadowDecision{carrierShadowFixture()})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if written != 1 || len(rejected) != 0 {
		t.Fatalf("written=%d rejected=%v，应为 1 行全收", written, rejected)
	}
	rows, err := db.CarrierShadowRowsForDay("u_cs", "20261105")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("行数 %d，应为 1", len(rows))
	}
	got := rows[0]
	if got.UserID != "u_cs" || got.ShortCode != "DR600000" || got.TsCode != "600000.SH" ||
		got.Kind != "dragon" || got.Side != "buy" || got.Qty != 100 || got.Price != 10.0 ||
		got.SizingNote != "capped_by=target" || got.DecidedAt != "2026-11-05T09:31:00+08:00" ||
		got.SeenAt != "2026-11-05T09:35:00+08:00" {
		t.Fatalf("字段没原样回来: %+v", got)
	}
	if got.Amount == nil || *got.Amount != 1000 {
		t.Fatalf("amount 丢失: %v", got.Amount)
	}
	if got.MarketScale == nil || *got.MarketScale != 0.8 {
		t.Fatalf("market_scale 丢失: %v", got.MarketScale)
	}
	if got.TargetScale == nil || *got.TargetScale != 0.5 {
		t.Fatalf("target_scale 丢失: %v", got.TargetScale)
	}
}

// TestCarrierShadowNullStaysNull 锁 ③：缺失的三枚可空列**必须回 nil**，不许被折成 0。
// 0 在对照表里是一个真实读数（"定权给 0"），nil 才是"载体这一轮没算"。
func TestCarrierShadowNullStaysNull(t *testing.T) {
	db := testDB(t)
	if _, _, err := db.UpsertCarrierShadowRows("u_cs2", "2026-11-05T09:35:00+08:00",
		[]CarrierShadowDecision{carrierShadowFixture(func(r *CarrierShadowDecision) {
			r.Amount, r.MarketScale, r.TargetScale = nil, nil, nil
			r.SizingNote = ""
		})}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, _ := db.CarrierShadowRowsForDay("u_cs2", "20261105")
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].Amount != nil || rows[0].MarketScale != nil || rows[0].TargetScale != nil {
		t.Fatalf("缺数被写成了值（missing 被当成 empty）: %+v", rows[0])
	}
}

// TestCarrierShadowReReportIsIdempotent 锁 ①：同一 (账号,日,短码) 重报只留一行，
// 且最新判定被刷新（重报通常是同一意图的第二轮算出更好的数）。
func TestCarrierShadowReReportIsIdempotent(t *testing.T) {
	db := testDB(t)
	first := carrierShadowFixture(func(r *CarrierShadowDecision) { r.MarketScale = f64(0.9) })
	if _, _, err := db.UpsertCarrierShadowRows("u_cs3", "09:30", []CarrierShadowDecision{first}); err != nil {
		t.Fatal(err)
	}
	again := carrierShadowFixture(func(r *CarrierShadowDecision) { r.MarketScale = f64(0.6) })
	if _, _, err := db.UpsertCarrierShadowRows("u_cs3", "10:00", []CarrierShadowDecision{
		again, again, // 同一批里来两次（上行的重试队列 + 新轮次双写）
	}); err != nil {
		t.Fatal(err)
	}
	rows, _ := db.CarrierShadowRowsForDay("u_cs3", "20261105")
	if len(rows) != 1 {
		t.Fatalf("重报把一行变成 %d 行——5 日窗口的分子被灌水了", len(rows))
	}
	if rows[0].MarketScale == nil || *rows[0].MarketScale != 0.6 {
		t.Fatalf("重报没刷新最新判定: %+v", rows[0])
	}
	if rows[0].SeenAt != "10:00" {
		t.Fatalf("seen_at 未随最后一次到达推进: %q", rows[0].SeenAt)
	}
	counts, _ := db.CarrierShadowKindDayCounts("u_cs3", "20261101", "20261105")
	if counts["dragon"]["20261105"] != 1 {
		t.Fatalf("按 (种类,日) 的分账出现重复计数: %+v", counts)
	}
}

// TestCarrierShadowDayKeyedNoRevive 锁 ②：昨天的行不在今天的读面里、也不与今天同短码那行互相覆盖。
// 这是"新交易日不复活、不累计"的存储面本体——把两天并成一天，窗口就少一天而没人报警。
func TestCarrierShadowDayKeyedNoRevive(t *testing.T) {
	db := testDB(t)
	yday := carrierShadowFixture(func(r *CarrierShadowDecision) { r.Day = "20261104" })
	tday := carrierShadowFixture(func(r *CarrierShadowDecision) { r.Day = "20261105" })
	if _, _, err := db.UpsertCarrierShadowRows("u_cs4", "seen", []CarrierShadowDecision{yday, tday}); err != nil {
		t.Fatal(err)
	}
	yRows, _ := db.CarrierShadowRowsForDay("u_cs4", "20261104")
	tRows, _ := db.CarrierShadowRowsForDay("u_cs4", "20261105")
	if len(yRows) != 1 || len(tRows) != 1 {
		t.Fatalf("两天各应为 1 行，got 昨天=%d 今天=%d", len(yRows), len(tRows))
	}
	if yRows[0].MarketScale == nil || *yRows[0].MarketScale != 0.8 {
		t.Fatalf("昨天的行被今天的重报改写: %+v", yRows[0])
	}
	counts, _ := db.CarrierShadowDayCounts("u_cs4", "20261101", "20261130")
	if len(counts) != 2 || counts["20261104"] != 1 || counts["20261105"] != 1 {
		t.Fatalf("按日分账不对（两天必须各算一天）: %+v", counts)
	}
}

// TestCarrierShadowTwoLedgersDistinguishSilenceFromNoHits 锁 ④：
// 报过但这一类没命中 ⇒ DayCounts 有该日、KindDayCounts 无该 kind；
// 整天静默 ⇒ 两张账都没有该日。两种"零"必须在数据面上就可分，不许调用方猜。
func TestCarrierShadowTwoLedgersDistinguishSilenceFromNoHits(t *testing.T) {
	db := testDB(t)
	if _, _, err := db.UpsertCarrierShadowRows("u_cs5", "seen", []CarrierShadowDecision{
		carrierShadowFixture(func(r *CarrierShadowDecision) { r.Kind = "factor_composite" }),
	}); err != nil {
		t.Fatal(err)
	}
	dayCounts, _ := db.CarrierShadowDayCounts("u_cs5", "20261105", "20261106")
	if _, ok := dayCounts["20261105"]; !ok {
		t.Fatal("报了的那条日应当进按日分账")
	}
	if _, ok := dayCounts["20261106"]; ok {
		t.Fatal("没报的那天不该凭空出现在按日分账里（静默被算成有数据）")
	}
	kindCounts, _ := db.CarrierShadowKindDayCounts("u_cs5", "20261105", "20261106")
	if got := kindCounts["factor_composite"]["20261105"]; got != 1 {
		t.Fatalf("报了的那类比应命中 1，got %d", got)
	}
	if _, ok := kindCounts["dragon"]; ok {
		t.Fatal("这一类一条都没报，不该在 (种类,日) 账里凭空出现")
	}
	// 关键对照：同一天里 DayCounts 有、dragon 无 ⇒ "报了但这类没命中"；
	// 而 20261106 两账皆无 ⇒ "这一天盒子静默"。两条读法不同，所以两张账都必须在。
	if _, ok := kindCounts["dragon"]["20261105"]; ok {
		t.Fatal("dragon 那格应当缺席（由上层折成 no_hits，而不是存储层造一个 0 行）")
	}
}

// TestCarrierShadowRejectsBadRowsPerRow 锁 ⑤：坏行逐条拒收并点名，好行照常落库。
func TestCarrierShadowRejectsBadRowsPerRow(t *testing.T) {
	db := testDB(t)
	rows := []CarrierShadowDecision{
		carrierShadowFixture(), // 好行
		carrierShadowFixture(func(r *CarrierShadowDecision) { r.Day = "2026-11-05" }), // 分隔符形态的 day
		carrierShadowFixture(func(r *CarrierShadowDecision) { r.ShortCode = "" }),     // 空幂等键
		carrierShadowFixture(func(r *CarrierShadowDecision) { r.Kind = "" }),          // 空种类
		carrierShadowFixture(func(r *CarrierShadowDecision) { r.TsCode = "600000" }),  // 无后缀（会落错交易所账）
	}
	written, rejected, err := db.UpsertCarrierShadowRows("u_cs6", "seen", rows)
	if err != nil {
		t.Fatalf("整批不该失败: %v", err)
	}
	if written != 1 {
		t.Fatalf("只该收 1 行，got %d", written)
	}
	if len(rejected) != 4 {
		t.Fatalf("四条坏行都要点名，got %v", rejected)
	}
	joined := strings.Join(rejected, " ")
	for _, want := range []string{"bad_day", "empty_short_code", "empty_kind", "bad_ts_code"} {
		if !strings.Contains(joined, want) {
			t.Errorf("拒收原因没点名 %q: %v", want, rejected)
		}
	}
	got, _ := db.CarrierShadowRowsForDay("u_cs6", "20261105")
	if len(got) != 1 {
		t.Fatalf("坏行落库了（会污染窗口）: %d 行", len(got))
	}
}

// TestCarrierShadowEmptyUserRefused 锁 ⑥：空 uid 结构性不能写——写进去就是跨账号混淆账
// （§FIX-20261028-F01 读面纪律在写面的镜像）。
func TestCarrierShadowEmptyUserRefused(t *testing.T) {
	db := testDB(t)
	if _, _, err := db.UpsertCarrierShadowRows("", "seen", []CarrierShadowDecision{carrierShadowFixture()}); err == nil {
		t.Fatal("空 uid 必须拒绝写入")
	}
	// 坏 day 的区间参数同样要拒（读面拿到 "2026-11-05" 会一行不中却报"窗口内无数据"）
	if _, err := db.CarrierShadowDayCounts("u_cs7", "2026-11-05", "20261106"); err == nil {
		t.Fatal("坏 fromDay 必须显式报错，不许静默返回空 map")
	}
	if _, err := db.CarrierShadowRowsForDay("u_cs7", "2026115"); err == nil {
		t.Fatal("坏 day 的按日查询必须报错")
	}
}

// TestCarrierShadowCrossAccountIsolated 另一个账号读不到这个账号的行（观测面同样按请求者过滤）。
func TestCarrierShadowCrossAccountIsolated(t *testing.T) {
	db := testDB(t)
	if _, _, err := db.UpsertCarrierShadowRows("u_own", "seen", []CarrierShadowDecision{carrierShadowFixture()}); err != nil {
		t.Fatal(err)
	}
	rows, _ := db.CarrierShadowRowsForDay("u_other", "20261105")
	if len(rows) != 0 {
		t.Fatalf("别人账号读到了我的影子决策: %+v", rows)
	}
	counts, _ := db.CarrierShadowDayCounts("u_other", "20261101", "20261130")
	if len(counts) != 0 {
		t.Fatalf("按日分账跨账号泄漏: %+v", counts)
	}
}

// TestPruneCarrierShadow 保留期收缩只按天剪；空串绝不等价于"全删"。
func TestPruneCarrierShadow(t *testing.T) {
	db := testDB(t)
	if _, _, err := db.UpsertCarrierShadowRows("u_cs8", "seen", []CarrierShadowDecision{
		carrierShadowFixture(func(r *CarrierShadowDecision) { r.Day = "20261001" }),
		carrierShadowFixture(func(r *CarrierShadowDecision) { r.Day = "20261105"; r.ShortCode = "DR600001" }),
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := db.PruneCarrierShadow(""); err != nil || n != 0 {
		t.Fatalf("空 beforeDay 必须不动任何行，got n=%d err=%v", n, err)
	}
	n, err := db.PruneCarrierShadow("20261010")
	if err != nil || n != 1 {
		t.Fatalf("只该剪掉 10-01 那行，got n=%d err=%v", n, err)
	}
	left, _ := db.CarrierShadowRowsForDay("u_cs8", "20261105")
	if len(left) != 1 {
		t.Fatalf("窗口内的行被剪掉了: %+v", left)
	}
	// 删号：只清该账号私有观测行
	deleted, err := db.DeleteCarrierShadowForUser("u_cs8")
	if err != nil || deleted != 1 {
		t.Fatalf("删号应清掉 1 行，got %d err=%v", deleted, err)
	}
	if _, err := db.DeleteCarrierShadowForUser(""); err == nil {
		t.Fatal("空 uid 的删号必须拒绝（那是把全平台的观测行一起抹掉）")
	}
}
