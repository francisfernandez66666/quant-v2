// settlement_gauge_test.go — §DEADGAUGE（2026-09-23 傍晚批收尾）行为锁 + §P2-E（2026-10-06 修复批
// 波 5）口径改判：
// 告警规则 settlement_diff（量规 settlement_diff_count）从 09-15 注册起全仓无赋值点，
// p1「交割单对账出现差异」永不触发。本文件把"三条出口都要喂量规"钉成行为用例：
//  1. 对账出差异 → 量规 = 缺失+多余+不符 三类条数之和（不是布尔、不是落库行数）；
//  2. 对账干净 → 必须归 0（否则昨天的差异会一直冒充今天的风警）；
//  3. §P2-E 改判：执行器不支持 / 网关未连接两条跳过腿**同样把差异读数归零**，但必须另外给出
//     settlement_state=3/2 的专属读数——"这一轮没有验证"由状态键说，不由差异键残留旧值说。
//
// English: §DEADGAUGE behavior locks (the settlement_diff rule once had no gauge writer at all)
// plus the §P2-E reclassification — the two skip branches still zero the diff count (stale residue
// would keep the gt-0 rule firing through an outage) while publishing a distinct settlement_state
// reading (3=unsupported, 2=not connected) that says the zero is not a verified result.
package trading

import (
	"testing"

	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/store"
)

// gaugeOf 读取量规当前值（不存在视为 0，并断言其已被注册过）。
func gaugeOf(t *testing.T, name string) int64 {
	t.Helper()
	v, ok := metrics.GetGauge(name)
	if !ok {
		t.Fatalf("量规 %s 从未被赋值（死规则形态复活）", name)
	}
	return v
}

// TestSettleDayFeedsDiffGauge 差异条数必须逐条进量规，且下一次干净对账能把它清零。
func TestSettleDayFeedsDiffGauge(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true
	// 本地一笔成交（与券商侧同键可配对）
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-1", Code: "600000.SH", Side: "买入",
		Price: 10, Qty: 100, Amount: 1000, TradedAt: "2026-09-08 09:35:00",
		SignalID: "SIG1", UserID: "u_st", Fee: 2.5, Serial: "SER-1"}); err != nil {
		t.Fatalf("local fill: %v", err)
	}
	// 券商侧：1 笔可配对 + 1 笔本地没有 → MissingInLocal=1
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-08", Connected: true,
		Trades: []SettlementTrade{
			{OrderID: "GW-1", TsCode: "600000.SH", Side: "买入", Price: 10, Qty: 100, Fee: 2.5, Serial: "SER-1", TradedAt: "09:35:00"},
			{OrderID: "GW-2", TsCode: "000001.SZ", Side: "卖出", Price: 12, Qty: 200, Fee: 3, Serial: "SER-2", TradedAt: "10:00:00"},
		},
	}}}
	ctrl := NewController(src, db, "u_st", cfg, nil)
	if _, _, err := ctrl.SettleDay("2026-09-08", SettleModeReportOnly); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 1 {
		t.Fatalf("有 1 条缺失时量规应为 1, got %d", got)
	}

	// 同一天改成完全配对（本地补上第二笔）→ 量规必须归 0
	if err := db.ApplyRealFill(store.RealFill{OrderID: "GW-2", Code: "000001.SZ", Side: "卖出",
		Price: 12, Qty: 200, Amount: 2400, TradedAt: "2026-09-08 10:00:00",
		SignalID: "SIG2", UserID: "u_st", Fee: 3}); err != nil {
		t.Fatalf("second fill: %v", err)
	}
	if _, _, err := ctrl.SettleDay("2026-09-08", SettleModeReportOnly); err != nil {
		t.Fatalf("settle 2: %v", err)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 0 {
		t.Fatalf("配对齐平后量规必须归 0, got %d", got)
	}
}

// TestSettleDaySkipBranchesZeroDiffButNotVerified §P2-E（2026-10-06 修复批 波 5）改口径的行为锁。
//
// 本文件原名 TestSettleDaySkipBranchesWriteZero，钉的是"两条静默跳过腿把 settlement_diff_count
// 写成 0（不适用）"。§0925EVE 报告（AUDIT_20261005 P2-E）指出的缺陷本体是：
// **0 同时是"对完了、确实没有差异"的读数**，运维面/告警面看不出这两种形态的任何区别，
// 而调用侧又把 err==nil 当成功，当日三方对账这道安全网就这么被记成"跑过了"。
//
// 本批中途一度把这理解成"跳过腿不该写差异读数"，改成保留上一轮残值——那条是**改错了方向**，
// 门禁 §69（§DEADGAUGE 负锁③，09-23 傍晚批）当场把它判红，理由成立且与 P2-E 不冲突：
// settlement_diff 规则是 `gt 0`，跳过腿不写数＝上一轮真比对出的条数**常驻**，于是断线日、
// 换桩日会天天拿昨天的差异刷 p1，那才是"残值冒充当日读数"。所以最终口径是**两个键合起来说**：
//  1. settlement_diff_count 归零（"此刻可数的差异为 0"，规则不拿残值误报）；
//  2. settlement_state 给出三态各自可分辨的读数（2=未连接跳过 / 3=执行器不支持 / 1=真对过账，
//     0 只留给"进程还没跑过"），这条零可不可信由它判定，规则 settlement_not_verified 用 ge 2 判红。
//
// 因此这里三条出口各自断**两个键的等值**：跳过腿 diff=0 且 state=3/2，已对账腿 diff=0 且 state=1。
// 之所以断等值而不是断区间：区间会放行"两条跳过腿写同一个值"——两种成因两种处置（一个重试、
// 一个短路），混桶就修错那一个。
// English: §P2-E final reading — skip branches zero the diff gauge (residue would keep the gt-0
// rule firing) AND publish a distinct settlement_state (2=not connected, 3=unsupported, 1=verified).
func TestSettleDaySkipBranchesZeroDiffButNotVerified(t *testing.T) {
	db := testDB(t)
	cfg := configDefault()
	cfg.Enabled = true

	// 基线：先放一个非 0 的真读数进去，用来区分"清零"与"不动"——本枚要断的正是**清零**。
	metrics.SetGauge("settlement_diff_count", 7)

	// 出口 A：执行器不支持交割单（结构性不适用）
	noopCtrl := NewController(&guardStub{}, db, "u_st", cfg, nil)
	diff, outcome, err := noopCtrl.SettleDay("2026-09-09", SettleModeReportOnly)
	if err != nil || diff != nil {
		t.Fatalf("不支持交割单应返回空差异，got diff=%v err=%v", diff, err)
	}
	if outcome != SettleOutcomeSkippedNoFetcher {
		t.Fatalf("执行器不支持必须返回 SkippedNoFetcher（旧形态在这里返回 (nil,nil) 被调用侧记成成功），got %s", outcome)
	}
	if outcome.Verified() {
		t.Fatalf("outcome=%s 却报 Verified=true＝三态判定自己说谎", outcome)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 0 {
		t.Fatalf("跳过腿必须把 settlement_diff_count 归零（残留的 7 会让规则 settlement_diff 在不支持交割单的日子上天天误报 p1），got %d", got)
	}
	if got := gaugeOf(t, "settlement_state"); got != 3 {
		t.Fatalf("执行器不支持时 settlement_state 应=3（这才是「本轮未验证」的读数，不是差异键归零），got %d", got)
	}

	// 出口 B：网关未连接（瞬时未验证）
	// 重新播一次残值：出口 A 已经按本批口径把差异键归零，不重播就只能证到 A 一条腿——
	// 反证 D10（删掉 B 腿的归零那行）会因此照样全绿，"三条出口都归零"退化成"一条出口有牙"。
	metrics.SetGauge("settlement_diff_count", 7)
	offCtrl := NewController(&settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-09", Connected: false,
	}}}, db, "u_st", cfg, nil)
	diff, outcome, err = offCtrl.SettleDay("2026-09-09", SettleModeReportOnly)
	if err != nil || diff != nil {
		t.Fatalf("网关未连接应返回空差异，got diff=%v err=%v", diff, err)
	}
	if outcome != SettleOutcomeSkippedNotConnected {
		t.Fatalf("网关未连接必须返回 SkippedNotConnected，got %s", outcome)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 0 {
		t.Fatalf("未连接腿同样必须归零 settlement_diff_count（应=0，残留上一轮条数＝断线日拿旧差异刷屏），got %d", got)
	}
	if got := gaugeOf(t, "settlement_state"); got != 2 {
		t.Fatalf("网关未连接时 settlement_state 应=2，got %d", got)
	}
	// 两条跳过腿的读数必须**彼此可分辨**且**与已对账可分辨**：2/3/1 三个值各归一，
	// 这是本条缺陷（"不适用与无差异不可分辨"）的正面判据；等值而不是区间，区间会放行"两腿写同一个值"。
	if gaugeOf(t, "settlement_state") == 3 {
		t.Fatalf("未连接腿读成 3＝与执行器不支持混桶，两种成因两种处置（一个重试、一个短路），混了就修错那一个")
	}

	// 出口 C：真对完账 → state=1，与上面两个读数互斥
	// 同样重播残值：这条断的是"真比对完成时按三类条数之和写"，无残值时它恒等于 0、看不出差别。
	metrics.SetGauge("settlement_diff_count", 7)
	src := &settleExecutor{src: &mockSettle{resp: &SettlementResponse{
		Date: "2026-09-09", Connected: true, Trades: []SettlementTrade{},
	}}}
	okCtrl := NewController(src, db, "u_st", cfg, nil)
	_, outcome, err = okCtrl.SettleDay("2026-09-09", SettleModeReportOnly)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !outcome.Verified() {
		t.Fatalf("真比对完成必须 Verified，got %s", outcome)
	}
	if got := gaugeOf(t, "settlement_state"); got != 1 {
		t.Fatalf("已对账时 settlement_state 应=1（规则用 ge 2 判破线，1 必须安全），got %d", got)
	}
	if got := gaugeOf(t, "settlement_diff_count"); got != 0 {
		t.Fatalf("对完账且无差异时 diff_count 归 0（这条零是**真读数**；它与跳过腿的零同值，靠 settlement_state 区分），got %d", got)
	}
}
