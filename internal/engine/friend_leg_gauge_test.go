// friend_leg_gauge_test.go — §FRIEND-LEG（2026-11-02）远端执行腿量规的对接与聚合回归锁。
//
// 钉两族（都是本仓 §DEADGAUGE 锤过三次的形状："规则在册、量规有人写、两端键名不一致
// 或压根没赋值点" ⇒ 规则永不触发而现场一切看起来全绿）：
//  1. 键名对齐 + 喂数腿：refreshStalenessGauges 写的键必须等于规则读的键，
//     且**只在盘中给档**（非盘中一律写 0——那台机器晚上关机是正常态，全天候写值就是每天一条假故障）；
//  2. 最坏档聚合：SetGauge 没有标签、进程级只有一个数，所以必须取全账号 max。
//     各引擎各喂各的会被"后写的健康腿盖掉先写的坏腿"，而这恰好是本告警要抓的那个形态
//     （运营账号本机腿恒 0，朋友那条坏腿写在前面 → 被 0 盖掉 → 恒不报）。
//
// 本批同时是一条 equals-legacy 锁：裸引擎（没有控制器 / 没有远端腿）喂出来的必须是 0。
package engine

import (
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/metrics"
	"quant-trading-v2/internal/trading"
)

// TestFriendLegGaugeRuleKeyAligned 规则读的键名必须与写端逐字相同（打错＝又一条死规则）。
func TestFriendLegGaugeRuleKeyAligned(t *testing.T) {
	var metric string
	for _, r := range metrics.DefaultAlertRules() {
		if r.Name == "friend_leg_unavailable" {
			metric = r.Metric
		}
	}
	if metric != "friend_leg_state" {
		t.Fatalf("规则 friend_leg_unavailable 读的键名是 %q（写端在 refreshStalenessGauges，两边必须同源）", metric)
	}
}

// TestRefreshStalenessFeedsFriendLegGauge 喂数腿的三档：裸引擎 0 / 本机腿 0 / 远端坏腿按盘中给档。
func TestRefreshStalenessFeedsFriendLegGauge(t *testing.T) {
	inSession := data.IsTradingDay(time.Now()) && data.IsContinuousTrade(time.Now())

	// ① 裸引擎（旧单引擎部署里 e.qmtCtrl 就是 nil）：必须写 0，且这个 0 永不触发 ge 2。
	e := &Engine{}
	e.refreshStalenessGauges()
	v, ok := metrics.GetGauge("friend_leg_state")
	if !ok {
		t.Fatalf("量规 friend_leg_state 从没被写过（规则永不触发）")
	}
	if v != 0 {
		t.Fatalf("无控制器时应写 0（这台机器上没有远端腿），got %d", v)
	}

	// ② 本机腿（没配 remote_leg 的实盘控制器）：即便探测没成功也仍是 0 —— equals-legacy 的读数面。
	local := trading.NewController(nil, nil, "u_local", config.QMTConfig{Enabled: true, GatewayURL: "http://127.0.0.1:1"}, nil)
	e.mu.Lock()
	e.qmtCtrl = local
	e.mu.Unlock()
	e.refreshStalenessGauges()
	if v := mustFriendLegGauge(t); v != 0 {
		t.Fatalf("本机腿必须恒 0（不该被这条新观测腿波及），got %d", v)
	}

	// ③ 远端腿且从未探到成功（现网"键按下但链路不通"的第一形态）：盘中读 2、非盘中读 0。
	remote := trading.NewController(nil, nil, "u_remote",
		config.QMTConfig{Enabled: true, GatewayURL: "http://127.0.0.1:1", RemoteLeg: true}, nil)
	e.mu.Lock()
	e.qmtCtrl = remote
	e.mu.Unlock()
	e.refreshStalenessGauges()
	want := int64(0)
	if inSession {
		want = 2
	}
	if v := mustFriendLegGauge(t); v != want {
		t.Fatalf("远端不可达腿盘中应写 2、非盘中写 0（残留会让次日开盘前假红），实得 %d（inSession=%v）", v, inSession)
	}
}

// mustFriendLegGauge 读远端腿量规（缺失即失败——"没人写过"和"写了 0"是两件事）。
func mustFriendLegGauge(t *testing.T) int64 {
	t.Helper()
	v, ok := metrics.GetGauge("friend_leg_state")
	if !ok {
		t.Fatalf("量规 friend_leg_state 未注册（赋值点在 refreshStalenessGauges）")
	}
	return v
}

// TestWorstFriendLegStateIsMaxAcrossAccounts 聚合必须取 max，而不是"最后一个账号说了算"。
// 现场形状：运营账号本机腿（0）与朋友账号坏腿（2/3）共存，量规只有一个数——
// 按遍历顺序取最后一个就会随机盖掉坏腿，而注册表 map 的序本就是乱的。
func TestWorstFriendLegStateIsMaxAcrossAccounts(t *testing.T) {
	r := NewRegistry(EngineOptions{})
	if got := r.worstFriendLegState(); got != 0 {
		t.Fatalf("空注册表应读 0（这台机器上没有任何远端腿），got %d", got)
	}

	mk := func(uid string, cfg config.QMTConfig) *Engine {
		e := &Engine{}
		e.mu.Lock()
		e.qmtCtrl = trading.NewController(nil, nil, uid, cfg, nil)
		e.mu.Unlock()
		return e
	}
	// 三台账号：本机腿 0 / 远端不可达 2 / 远端已停但未触达 3 ⇒ 最坏 3。
	r.mu.Lock()
	r.cores["fp-local"] = mk("u_a", config.QMTConfig{Enabled: true})
	r.cores["fp-broken"] = mk("u_b", config.QMTConfig{Enabled: true, RemoteLeg: true})
	r.cores["fp-halted"] = mk("u_c", config.QMTConfig{Enabled: true, RemoteLeg: true, Halted: true})
	r.mu.Unlock()
	if got := r.worstFriendLegState(); got != 3 {
		t.Fatalf("全账号最坏档应为 3（撤销未触达压过 2/0），got %d", got)
	}

	// 反证方向：把最坏那条摘掉，读数必须跟着降回 2（证明取的是 max 而不是某个粘住的残值）。
	r.mu.Lock()
	delete(r.cores, "fp-halted")
	r.mu.Unlock()
	if got := r.worstFriendLegState(); got != 2 {
		t.Fatalf("摘掉 3 那台后应降回 2，got %d", got)
	}

	// 引擎侧回退腿：未注入聚合闭包（旧单引擎部署）时只看本引擎自己的控制器，绝不吃别的账号。
	e := mk("u_solo", config.QMTConfig{Enabled: true, RemoteLeg: true})
	if got := e.WorstFriendLegState(); got != 2 {
		t.Fatalf("未注入聚合时应读本引擎控制器（2），got %d", got)
	}
	e.SetFriendLegAggregator(func() int64 { return 1 })
	if got := e.WorstFriendLegState(); got != 1 {
		t.Fatalf("注入聚合闭包后必须走聚合值（1），got %d", got)
	}
}

// TestFriendLegGaugeDoesNotTouchDataPlane 负向行为锁（owner 边界：观测面不许带动数据面）。
// 形状与 §INDEX-BLIND 那条同规：直接取 refreshStalenessGauges 函数体，
// 出现降级账本写入、健康线端点或下单相关调用即红。
func TestFriendLegGaugeDoesNotTouchDataPlane(t *testing.T) {
	raw, err := os.ReadFile("scoring_loop.go")
	if err != nil {
		t.Fatalf("读 scoring_loop.go: %v", err)
	}
	body := functionBody(string(raw), "func (e *Engine) refreshStalenessGauges(")
	if body == "" {
		t.Fatalf("取不到 refreshStalenessGauges 函数体（锚漂了，这条锁是摆设）")
	}
	// 只查本批新增那段（远端腿注释起）之后的字节，避免与上面已有的其它量规腿混判。
	start := strings.Index(body, "§FRIEND-LEG")
	if start < 0 {
		t.Fatal("喂量规函数里没有 §FRIEND-LEG 那段（赋值点丢了＝又一条死规则）")
	}
	leg := body[start:]
	for _, banned := range []string{"SetTripped", "PlaceOrder", "st.Degrade =", "Degrade: \"J0\"", "/health"} {
		if strings.Contains(leg, banned) {
			t.Fatalf("远端腿观测腿里出现了 %q——观测面不许带动下单/熔断/健康线", banned)
		}
	}
	if !strings.Contains(leg, `metrics.SetGauge("friend_leg_state"`) {
		t.Fatal("§FRIEND-LEG 那段没写 friend_leg_state 量规（规则永不触发）")
	}
}

// §B-N10 推送腿的账：跳变才推、恢复清零、notifier 没接也不能 panic。
// 这两条是"5s 一轮把朋友手机刷爆"和"精简装配下引擎崩掉"的直接防线——
// 至于"两条都推（本人 + 运营）"由门禁 §170 的静态锁守（Alias=uid 与 Alias="" 各一处）。
func TestPushFriendLegAlertOnlyOnTransition(t *testing.T) {
	e := &Engine{userID: "u_friend"}
	e.pushFriendLegAlert(0)
	if e.friendLegPushed != 0 {
		t.Fatal("state<2 应把已推位清零")
	}
	e.pushFriendLegAlert(2) // 无 notifier：只记跳变，不 panic
	if e.friendLegPushed != 2 {
		t.Fatalf("首次不可达应记为已推 2，实得 %d", e.friendLegPushed)
	}
	e.pushFriendLegAlert(2)
	if e.friendLegPushed != 2 {
		t.Fatal("同状态重复推会刷屏")
	}
	e.pushFriendLegAlert(3)
	if e.friendLegPushed != 3 {
		t.Fatal("2→3 是新的处置方向，必须再推一次")
	}
	e.pushFriendLegAlert(1)
	if e.friendLegPushed != 0 {
		t.Fatal("腿恢复后应清零，否则第二次掉线不报")
	}
}
