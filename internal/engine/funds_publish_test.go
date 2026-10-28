package engine

// funds_publish_test.go — §FUNDS-TODAY（2026-10-28）方案 §7.1：资金帧发布点前移。
//
// 缺陷本体（首日实测）：资金帧原先只在**实盘 T 循环**里写，于是「做 T 没开
//（sizing.t.enabled=false）/ 当天没做一笔 T」的日子，卡片上永远是「今日尚未定权」——
// 而信号仓的定权（MS/目标/剩余）与 T 开不开毫无关系（盘中 stderr 明明有 MS=0.765）。
//
// 本文件锁四件事：
//  1. 信号仓侧**不依赖 T 循环**：T 停用时照样有帧、照样有 MS/目标/剩余；
//  2. T 侧**不被信号仓侧清零**：T 没跑时保持上一帧并保留 t_at（不伪造 0）；
//  3. updateFunds 是「读—改—写」：两侧各有写者时互不覆盖；
//  4. 目标/当前/剩余只有**一个公式**（信号仓帧与 T 额度共用），两侧读数一致。
//
// English: locks that the signal-side funds frame is published independently of the T loop,
// that the T side is preserved (with its own timestamp) when T is idle, that the frame
// update is a read-modify-write, and that target/current/room come from one shared formula.

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// fundsFix 一套最小夹具：临时账本 + 账号目录（资金帧的落点）+ 注入时钟。
type fundsFix struct {
	e   *Engine
	db  *store.DB
	uid string
	now time.Time
}

// newFundsFix 一套最小夹具：临时账本 + 账号目录（资金帧的落点）+ 注入时钟。
//
// 不用 tLiveFix 是因为本组用例只关心资金帧，不关心 T 编排：T 账本注册表照建（帧要有落点），
// 但不播 T 腿、不开 T 开关——"T 没跑"正是本组要锁的场景。
func newFundsFix(t *testing.T) *fundsFix {
	t.Helper()
	e, db, _, _ := newQMTEngine(t, nil)
	e.SetMembers([]string{"u_1"})
	now := tLiveClockAt(10, 0)
	e.SetClock(func() time.Time { return now })
	root := t.TempDir()
	e.mu.Lock()
	e.accountsRoot = root
	rg := newTLiveRegistry(root)
	rg.nowFn = func() time.Time { return now }
	e.tLiveReg = rg
	e.mu.Unlock()
	// 账户资金快照：UpdatedAt 必须用真实当前时间（AvailableCash 的 30 分钟新鲜度按真实钟判）。
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: "u_1", TotalAsset: 100000, AvailableCash: 50000, MarketValue: 40000,
		UpdatedAt: cntimeNowStr(),
	}); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	return &fundsFix{e: e, db: db, uid: "u_1", now: now}
}

// cntimeNowStr 当前北京墙钟串（真实时钟，不受用例注入时钟影响）。
func cntimeNowStr() string {
	return time.Now().In(cntime.Loc).Format("2006-01-02 15:04:05")
}

// TestFundsPublishPointIsScoringLoop 发布点必须在**打分循环**里，且在"打分池为空"的早退之前。
//
// 为什么这条要读源码而不是只测函数行为：函数本身对了、但调用点挂错地方（T 循环里、或排在
// 池空早退之后），功能照样是坏的——池空的日子照样"尚未定权"。行为用例测不到"调用点在哪"，
// 这条把接线钉死：调用必须存在，且必须排在池空早退之前。
// English: the publish call must live in the scoring loop and must precede the empty-pool
// early return — a behaviour-only test cannot see where the call is wired.
func TestFundsPublishPointIsScoringLoop(t *testing.T) {
	raw, err := os.ReadFile("scoring_loop.go")
	if err != nil {
		t.Fatalf("读 scoring_loop.go: %v", err)
	}
	src := string(raw)
	call := strings.Index(src, "e.publishSignalFunds(e.primaryMember(), quotes)")
	if call < 0 {
		t.Fatalf("打分循环里没有 publishSignalFunds 调用（发布点没前移）")
	}
	poolEmpty := strings.Index(src, "if len(pool) == 0 {")
	if poolEmpty < 0 {
		t.Fatalf("scoring_loop.go 里找不到打分池早退分支（源码形态变了，对照点失效）")
	}
	if call > poolEmpty {
		t.Fatalf("publishSignalFunds 排在打分池早退之后（池空的日子就没有资金帧）")
	}
	// 还要在会话门禁之后：盘后不定权。
	gate := strings.Index(src, "if !data.IsActiveSession(time.Now()) {")
	if gate < 0 || call < gate {
		t.Fatalf("publishSignalFunds 排在会话门禁之前或门禁消失了（盘后也在定权）")
	}
}

// TestFundsSignalSidePublishedWithoutT T 停用/没做 T 时，信号仓侧照样有帧与定权。
func TestFundsSignalSidePublishedWithoutT(t *testing.T) {
	f := newFundsFix(t)
	// 明确"做 T 没开"：T 侧一个字段都不该由本轮产生，但信号仓侧必须有。
	f.e.publishSignalFunds(f.uid, nil)

	st := f.e.tLiveFor(f.uid)
	if st == nil {
		t.Fatalf("没有 T 账本（资金帧无落点）")
	}
	snap := st.fundsSnapshot()
	if snap == nil {
		t.Fatalf("T 未运行却没有任何资金帧（今日尚未定权）——这正是 §7.1 的缺陷")
	}
	if snap.MarketScale <= 0 {
		t.Fatalf("信号仓侧没有 MS（读到 %v）", snap.MarketScale)
	}
	if snap.Target <= 0 {
		t.Fatalf("信号仓侧没有目标仓位（读到 %v）", snap.Target)
	}
	if snap.Cash != 50000 || snap.TotalAsset != 100000 {
		t.Fatalf("账户资金读数不符: cash=%v total=%v", snap.Cash, snap.TotalAsset)
	}
	if snap.At == "" {
		t.Fatalf("信号仓侧没有取数时刻")
	}
	// T 侧必须是"没跑过"的样子：没有 T 时刻戳，且额度等字段为零（本来就没有 T 腿）。
	if snap.TAt != "" {
		t.Fatalf("T 没跑却有 T 侧取数时刻: %q", snap.TAt)
	}
}

// TestFundsTSidePreservedWhenSignalSideRefreshes 信号仓侧刷新**不得**清零 T 侧。
func TestFundsTSidePreservedWhenSignalSideRefreshes(t *testing.T) {
	f := newFundsFix(t)
	st := f.e.tLiveFor(f.uid)
	if st == nil {
		t.Fatalf("没有 T 账本")
	}
	// 先写一帧 T 侧（模拟 T 循环跑过一轮：额度/可用现金/完成轮数都有值）。
	st.updateFunds(func(fr *tLiveFunds) {
		fr.TAt = "2026-11-04T10:00:00+08:00"
		fr.TQuota = 12345
		fr.CashT = 6789
		fr.DoneRounds = 2
		fr.PnlNet = 88.5
	})
	// 再让信号仓侧发布一轮（去重窗口内要放行，所以先把时钟往前推 11s）。
	f.now = f.now.Add(11 * time.Second)
	f.e.SetClock(func() time.Time { return f.now })
	f.e.publishSignalFunds(f.uid, nil)

	snap := st.fundsSnapshot()
	if snap == nil {
		t.Fatalf("资金帧丢了")
	}
	if snap.TQuota != 12345 || snap.CashT != 6789 || snap.DoneRounds != 2 || snap.PnlNet != 88.5 {
		t.Fatalf("信号仓侧刷新把 T 侧冲掉了: %+v", snap)
	}
	if snap.TAt != "2026-11-04T10:00:00+08:00" {
		t.Fatalf("T 侧取数时刻被改写: %q", snap.TAt)
	}
	if snap.MarketScale <= 0 || snap.Target <= 0 {
		t.Fatalf("信号仓侧没有写进来: %+v", snap)
	}
}

// TestFundsUpdateFundsIsReadModifyWrite updateFunds 必须在同一把锁内读改写（两侧互不覆盖）。
func TestFundsUpdateFundsIsReadModifyWrite(t *testing.T) {
	root := t.TempDir()
	st := newTLiveStore(root, "u_rmw")
	st.day = "20261104"
	st.updateFunds(func(f *tLiveFunds) { f.TQuota = 111 })
	st.updateFunds(func(f *tLiveFunds) { f.MarketScale = 0.75 })
	snap := st.fundsSnapshot()
	if snap == nil {
		t.Fatalf("帧丢失")
	}
	if snap.TQuota != 111 || snap.MarketScale != 0.75 {
		t.Fatalf("读—改—写被整帧覆盖了: %+v", snap)
	}
	if snap.Day != "20261104" {
		t.Fatalf("帧的 Day 不对: %q", snap.Day)
	}
}

// TestFundsTargetCurRoomSharedFormula 目标/当前/剩余的唯一公式：T 腿按净额剔除。
func TestFundsTargetCurRoomSharedFormula(t *testing.T) {
	positions := map[string]store.RealPosition{
		"600362": {TsCode: "600362.SH", Qty: 400, CostPrice: 36.56},
	}
	prices := map[string]float64{"600362": 36.56}
	// 反 T 卖腿已成交 200 股（底仓还欠着买回）⇒ 战略当前仓位加回 200×36.56。
	book := tLiveBook{SellLegValue: 200 * 36.56}
	target, cur, room, ok := fundsTargetCurRoom(100000, 0.5, 1.0, positions, prices, nil, book)
	if !ok {
		t.Fatalf("公式返回不可得")
	}
	if math.Abs(target-50000) > 1e-6 {
		t.Fatalf("目标仓位不对: %v", target)
	}
	if math.Abs(cur-(400*36.56+200*36.56)) > 1e-6 {
		t.Fatalf("战略当前仓位不对（T 腿净额未按口径加回）: %v", cur)
	}
	if math.Abs(room-(target-cur)) > 1e-6 {
		t.Fatalf("剩余空间不对: %v", room)
	}
	// 天花板压顶：MS=0.9 但天花板 0.3 ⇒ 目标按 0.3 算。
	target2, _, _, _ := fundsTargetCurRoom(100000, 0.9, 0.3, positions, prices, nil, tLiveBook{})
	if math.Abs(target2-30000) > 1e-6 {
		t.Fatalf("天花板未生效: %v", target2)
	}
	// 战略在途买单占用空间（T 腿的在途不占）。
	pending := []store.PendingBuy{{Code: "600362", Price: 36.56, Remain: 100}}
	_, cur3, _, _ := fundsTargetCurRoom(100000, 0.5, 1.0, positions, prices, pending, tLiveBook{})
	if math.Abs(cur3-(400*36.56+100*36.56)) > 1e-6 {
		t.Fatalf("战略在途买单未计入战略当前仓位: %v", cur3)
	}
	pendingT := []store.PendingBuy{{Code: "600362", Price: 36.56, Remain: 100, IntradayT: true}}
	_, cur4, _, _ := fundsTargetCurRoom(100000, 0.5, 1.0, positions, prices, pendingT, tLiveBook{})
	if math.Abs(cur4-(400*36.56)) > 1e-6 {
		t.Fatalf("T 腿在途买单被算进战略仓位了: %v", cur4)
	}
}

// TestFundsPublishDedup 信号仓侧发布去重 10s（避免每 5s 写一整份账本）。
func TestFundsPublishDedup(t *testing.T) {
	f := newFundsFix(t)
	f.e.publishSignalFunds(f.uid, nil)
	first := f.e.tLiveFor(f.uid).fundsSnapshot()
	if first == nil {
		t.Fatalf("首帧缺失")
	}
	// 立刻再发一次：被去重挡掉（取数时刻不变）。
	f.e.publishSignalFunds(f.uid, nil)
	second := f.e.tLiveFor(f.uid).fundsSnapshot()
	if second == nil || second.At != first.At {
		t.Fatalf("去重失效：10s 内重复发布: %q vs %q", first.At, second.At)
	}
	// 推过 10s 后再发：应刷新。
	f.now = f.now.Add(11 * time.Second)
	f.e.SetClock(func() time.Time { return f.now })
	f.e.publishSignalFunds(f.uid, nil)
	third := f.e.tLiveFor(f.uid).fundsSnapshot()
	if third == nil || third.At == first.At {
		t.Fatalf("超过去重窗口后没有刷新: %q", third.At)
	}
}
