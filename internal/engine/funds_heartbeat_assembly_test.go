// funds_heartbeat_assembly_test.go — §FUNDS-HEARTBEAT-ASSEMBLY（2026-11-02 盘中事故收口）。
//
// 事故本体：资金心跳 publishTFundsHeartbeat 用**结构体字面量**手搓 tLiveRound（与真实 T 轮的
// newTLiveRoundWith 装配点是两处），字面量里从来没有 `ctrl`。这行代码 2026-10-30 落地时是安全的
// （totalAssetsOf 当时读 db.GetRealAccount）；2026-11-01 §P1（bb85a36）把它换成
// r.ctrl.GetRealAccountFor / r.ctrl.TotalAssetsFor，nil 接收器第一次被真解引用。
// 10-31/11-01 休市、IsActiveSession 为假 ⇒ 这条腿整天没被踩过，11-02 是它的第一实盘交易日：
// 现网 quant.exe 从 08:44 起每 6~40 秒 panic 一次、131 次重启，打分轮从没跑到，
// 全天零买入评估（stderr 里 scoring_loop / [qmt-gate] / signal_decisions 今日均为 0）。
//
// 本文件锁两层，缺一不可：
//  1. **行为腿**：把心跳接到一套有控制器的夹具上真跑一遍——装配缺字段时它是 panic，
//     不是"少一帧"（少一帧是观感问题，进程死了是没人在管持仓）；
//  2. **装配腿**：直接读源码点名字段。行为用例只能覆盖"今天这条链碰了哪些字段"，
//     而这条链的字段集会随后续改动继续长（§P1 就是那次生长）——把"必须显式给出哪些键"
//     钉在源码形状上，下次加字段才会在装配点当场红，而不是等到下一个交易日。
//
// English: the funds heartbeat built its own tLiveRound literal and omitted the controller;
// after §P1 made totalAssetsOf dereference it, the live process crash-looped all session.
// One test runs the heartbeat against a wired fixture (panic = red), one asserts the literal
// carries the assembly keys, so the next field growth fails at the assembly site.

package engine

import (
	"os"
	"strings"
	"testing"
)

// balancedBraces 从以 '{' 开头的源码片段里取出**括号配平**的一段。
// 结构体字面量里嵌着 map[string]X{} 这种内层花括号，按"遇到第一个 } 就收"会截断，
// 于是键检查会对着半截字面量判绿——那是假自证。
func balancedBraces(s string) string {
	if !strings.HasPrefix(s, "{") {
		return ""
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return ""
}

// TestFundsHeartbeatRoundCarriesController 行为腿：心跳轮次必须带着控制器真跑通，
// 并把券商总资产口径算进 T 额度（total==0 时额度公式会静默给 0，那就又是"降级报成功"）。
func TestFundsHeartbeatRoundCarriesController(t *testing.T) {
	// 用资金帧夹具（newFundsFix）而不是 T 编排夹具（newTLiveFix）：后者把 marketAPI 塞成
	// 空壳哨兵，心跳里的 regime 读数会拿它去打真实 HTTP；本用例要测的是装配，不是行情源。
	f := newFundsFix(t)
	if f.e.QMTController() == nil {
		t.Fatalf("夹具没有控制器（QMTController 为 nil，本用例无法证明装配腿）")
	}

	f.e.publishTFundsHeartbeat("u_1") // 装配缺 ctrl 时这里 panic，用例即红

	st := f.e.tLiveFor("u_1")
	if st == nil {
		t.Fatalf("没有 T 账本（资金帧无落点）")
	}
	snap := st.fundsSnapshot()
	if snap == nil {
		t.Fatalf("心跳跑完却没有资金帧（§FUNDS-TODAY 的 T 侧发布点失效）")
	}
	// TAt 是 publishTFunds 里最后一个写入位，而它排在 totalAssetsOf **之后**——
	// 所以"TAt 有值"本身就证明了取值链路（含经控制器取券商总资产那一跳）跑到了底：
	// 装配缺 ctrl 时进程在 442 行就炸了，永远走不到这里。
	if snap.TAt == "" {
		t.Fatalf("心跳没有写 T 侧取数时刻（卡片会一直显示 T 侧未运行）")
	}
	if snap.Day == "" {
		t.Fatalf("资金帧没有归属日（day-keyed 帧缺日＝次日会复活昨日的值）")
	}
	t.Logf("心跳帧：day=%s t_at=%s MS=%.3f total_asset=%.2f t_quota=%.2f cash_t=%.2f",
		snap.Day, snap.TAt, snap.MarketScale, snap.TotalAsset, snap.TQuota, snap.CashT)
}

// TestFundsHeartbeatLiteralHasAssemblyKeys 装配腿：心跳手搓的 tLiveRound 字面量必须显式给出
// 读路径可达的每个指针/映射字段。改这里的人不必知道 totalAssetsOf 在哪解引用 ctrl——
// 漏一个键就在本用例红，而不是等到盘中。
func TestFundsHeartbeatLiteralHasAssemblyKeys(t *testing.T) {
	raw, err := os.ReadFile("intraday_t_live.go")
	if err != nil {
		t.Fatalf("读 intraday_t_live.go: %v", err)
	}
	src := string(raw)
	fnStart := strings.Index(src, "func (e *Engine) publishTFundsHeartbeat(")
	if fnStart < 0 {
		t.Fatalf("找不到 publishTFundsHeartbeat（函数改名/搬迁了，本锁的对照点失效，请同批更新）")
	}
	fnEnd := strings.Index(src[fnStart:], "\n}\n")
	if fnEnd < 0 {
		t.Fatalf("publishTFundsHeartbeat 函数体尾部形态变了（按空行+右花括号配不上）")
	}
	body := src[fnStart : fnStart+fnEnd]

	lit := strings.Index(body, "&tLiveRound{")
	if lit < 0 {
		t.Fatalf("心跳函数里没有 tLiveRound 装配点（改成走构造器了？那就把本锁并进构造器锁）")
	}
	// 字面量里有 map[string]X{} 这种内层花括号，按"第一个 }"截会截断——必须做括号配平。
	seg := balancedBraces(body[lit+strings.Index(body[lit:], "{"):])
	if seg == "" {
		t.Fatalf("tLiveRound 字面量括号配不上（源码形态变了）")
	}

	// 必备键：ctrl 是 §P1 之后 totalAssetsOf 的解引用对象；day/dash 是按日过滤口径
	// （空串会静默返回空账＝把"读不到"伪装成"当日无盈亏"）；两张缓存被 facts/ordersCache
	// 的写入路径用到（nil map 赋值即 panic）。
	for _, k := range []string{"ctrl:", "day:", "dash:", "legFactsCache:", "ordersCache:", "db:", "st:", "quotes:"} {
		if !strings.Contains(seg, k) {
			t.Fatalf("心跳装配缺 %s（漏这个键的形态就是 2026-11-02 整天零买入的成因）；实到=%q", k, seg)
		}
	}
	// 反向锁：心跳不许复用会动钱的腿（它只该发布读数）。一旦有人在这里复用
	// 配对/收摊/下单路径，"缺字段"就从观感问题变成资金问题，必须先过装配自证。
	for _, banned := range []string{"r.facts(", "r.buyLeg(", "r.sellLeg(", "r.cancelOpenKeys(", "r.execute("} {
		if strings.Contains(body, banned) {
			t.Fatalf("心跳里出现了动钱的调用 %s（只读观测腿不许长出资本动作；需要就走 newTLiveRoundWith）", banned)
		}
	}
}

// TestFundsHeartbeatTickIsRecoverGuarded 止损腿：资金心跳的 panic 只许丢一跳，不许丢进程。
// 今天真正让"卡片少一帧"升级成"整天没人交易"的，是这条 goroutine 没有 recover
// （打分循环 scoreCycle 顶部有，观测腿没有——同一条纪律只收了一半）。
func TestFundsHeartbeatTickIsRecoverGuarded(t *testing.T) {
	raw, err := os.ReadFile("funds_today.go")
	if err != nil {
		t.Fatalf("读 funds_today.go: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func (e *Engine) runFundsTickGuarded()")
	if start < 0 {
		t.Fatalf("心跳的受保护跳不存在（goroutine 又直调发布函数了＝一个 panic 打死整个进程）")
	}
	guarded := src[start:]
	guarded = guarded[:strings.Index(guarded, "\n}\n")+3]
	for _, need := range []string{"recover()", "metrics.PanicRecovered()", "debug.Stack()",
		"e.publishSignalFunds(", "e.publishTFundsHeartbeat("} {
		if !strings.Contains(guarded, need) {
			t.Fatalf("受保护跳里缺 %s（panic 计数/栈/或某一侧发布被摘掉）", need)
		}
	}
	// 循环体必须调受保护跳，而不是自己内联发布（内联＝recover 又没了）。
	loopStart := strings.Index(src, "func (e *Engine) StartFundsLoop(")
	if loopStart < 0 {
		t.Fatalf("找不到 StartFundsLoop")
	}
	loop := src[loopStart : strings.Index(src[loopStart:], "\n}\n")+loopStart]
	if !strings.Contains(loop, "e.runFundsTickGuarded()") {
		t.Fatalf("资金心跳循环没走受保护跳")
	}
}
