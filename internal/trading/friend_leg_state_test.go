// friend_leg_state_test.go — §FRIEND-LEG（2026-11-02）远端执行腿可达性读数的回归锁。
//
// 钉住四件事：
//  1. **equals-legacy**：配置里根本没有 remote_leg 键（现网所有账号的形态）时恒读 0，
//     且读了之后 Health()/熔断/快照这些既有读数一个字节都没被推动——观测腿不许有停机权限；
//  2. 四档语义：0 不是远端腿 / 1 可达 / 2 不可达 / 3 钥匙已撤但未触达；
//  3. 3 与 2 的**分档本身**（同一条可达性判据、只因 Halted 不同而分叉）——
//     两者处置方向不同（2 查链路，3 人去把那台机器停掉并清待发），折成一条就是让人去 ping
//     一个 ping 得通却不听命令的链路；
//  4. **纯读**：调用 FriendLegState() 一次请求都不许发（计数执行器断言 calls==0）。
//     这条不是洁癖：喂量规的腿每 30s 调它，若它自己发 HTTP，就会与 HealthCheck 的
//     miss/2 节流打架，并把"观测"变成"依赖"（观测挂了反过来影响下单路径）。
package trading

import (
	"testing"
	"time"

	"quant-trading-v2/internal/config"
)

// friendLegSpyExec 计数执行器：记录每一次对"网关"的调用（含健康探测），健康结果可注入。
// 用它证明 FriendLegState() 走的是控制器已有的状态字，而不是现场去探一次。
type friendLegSpyExec struct {
	NoopExecutor
	calls  int  // 累计被调用次数（下单/撤单/状态/健康全部计入）
	health bool // Health() 返回的可达性
}

// PlaceBuy PlaceBuy（Stub方法）。
func (s *friendLegSpyExec) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	s.calls++
	return &OrderResult{OK: true, OrderID: "GW-B"}, nil
}

// PlaceSell PlaceSell（Stub方法）。
func (s *friendLegSpyExec) PlaceSell(req OrderRequest) (*OrderResult, error) {
	s.calls++
	return &OrderResult{OK: true, OrderID: "GW-S"}, nil
}

// Cancel Cancel（Stub方法）。
func (s *friendLegSpyExec) Cancel(orderID string) error { s.calls++; return nil }

// State State（Stub方法）。
func (s *friendLegSpyExec) State() (*GatewayState, error) {
	s.calls++
	return &GatewayState{Connected: s.health}, nil
}

// Health 计数并返回注入的可达性——这是"下游腿通不通"的唯一现场证据源。
func (s *friendLegSpyExec) Health() (bool, error) {
	s.calls++
	return s.health, nil
}

// friendLegCfg 造一份远端腿配置：enabled + 网关地址 + remote_leg 按参数。
// GatewayURL 填一个不可路由的本地地址：所有真实网络路径都在测试里被桩执行器替换掉，
// 这个值只是让 HealthCheck 的"未配置网关就直接返回"那道早退不被踩到。
func friendLegCfg(remoteLeg, halted bool) config.QMTConfig {
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.GatewayURL = "http://127.0.0.1:1"
	cfg.MissHeartbeatSec = 2
	cfg.RemoteLeg = remoteLeg
	cfg.Halted = halted
	return cfg
}

// probeLeg 跑一次真实健康探测（先把节流戳解锁，并换上当轮要用的可达性）。
// 复用生产路径 HealthCheck，而不是手改 lastHealthy——这样"三档读数来自既有状态字"
// 这件事本身就是被测对象（手改字段只能证明函数在读那个字段，不能证明它与真实探测同源）。
func probeLeg(t *testing.T, c *Controller, exec *friendLegSpyExec, reachable bool) {
	t.Helper()
	exec.health = reachable
	c.mu.Lock()
	c.lastHealthAt = time.Time{}
	c.mu.Unlock()
	c.exec.Store(execHolder{exec})
	c.HealthCheck()
}

// TestFriendLegStateDefaultIsLegacyZero §FRIEND-LEG 的 equals-legacy 腿：
// 没有 remote_leg 键（DefaultQMTConfig 与现网 config.json 都是这形态）时读数恒 0，
// 且这个读取不推动任何既有状态（熔断/快照/健康标记逐字段不变）。
func TestFriendLegStateDefaultIsLegacyZero(t *testing.T) {
	db := testDB(t)
	if config.DefaultQMTConfig().RemoteLegOn() {
		t.Fatal("出厂默认必须是「本机腿」——RemoteLegOn() 为 true 就是给所有存量账号凭空造新读数")
	}

	exec := &friendLegSpyExec{}
	c := NewController(exec, db, "u_fl_legacy", friendLegCfg(false, false), nil)

	// 未探测、探测成功、探测失败、已熔断四种内部状态下，非远端腿都必须读 0。
	if got := c.FriendLegState(); got != 0 {
		t.Fatalf("缺 remote_leg 键时未探测应读 0（今天形态），got %d", got)
	}
	probeLeg(t, c, exec, true)
	if got := c.FriendLegState(); got != 0 {
		t.Fatalf("本机腿即使探测成功也只能读 0（它不该产生新读数），got %d", got)
	}
	probeLeg(t, c, exec, false)
	if got := c.FriendLegState(); got != 0 {
		t.Fatalf("本机腿即使探测失败也只能读 0——失联由熔断器说话，不由这条观测腿，got %d", got)
	}
	c.SetTripped("测试注入熔断")
	if got := c.FriendLegState(); got != 0 {
		t.Fatalf("本机腿熔断态下仍读 0，got %d", got)
	}

	// 读数不许有副作用：既有观测面（Snapshot/Tripped/Config）在前后必须逐字段一致。
	snapBefore := c.Snapshot()
	tripBefore, reasonBefore, atBefore := c.TripInfo()
	cfgBefore := c.Config()
	for i := 0; i < 5; i++ {
		_ = c.FriendLegState()
	}
	snapAfter := c.Snapshot()
	tripAfter, reasonAfter, atAfter := c.TripInfo()
	cfgAfter := c.Config()
	if snapAfter != snapBefore {
		t.Fatalf("FriendLegState() 推动了互通健康快照（观测腿写数据面）：\n前 %+v\n后 %+v", snapBefore, snapAfter)
	}
	if tripAfter != tripBefore || reasonAfter != reasonBefore || !atAfter.Equal(atBefore) {
		t.Fatalf("FriendLegState() 推动了熔断状态：before=%v/%q after=%v/%q",
			tripBefore, reasonBefore, tripAfter, reasonAfter)
	}
	if cfgAfter.RemoteLeg != cfgBefore.RemoteLeg || cfgAfter.Halted != cfgBefore.Halted {
		t.Fatalf("FriendLegState() 改写了配置（观测腿只读）：before=%+v after=%+v", cfgBefore, cfgAfter)
	}

	// 收尾把上一段注入的熔断解掉（setTripped 会写进程级 metrics 量规 breaker_active）——
	// 同一测试包后面的用例读的是同一个全局量规，不留残值。
	probeLeg(t, c, exec, true)
	probeLeg(t, c, exec, true)
	if c.Tripped() {
		t.Fatal("收尾未解熔（后续用例会读到 breaker_active=1 的残值）")
	}
}

// TestFriendLegStateFourStates 四档语义的表驱动主用例（远端腿）。
func TestFriendLegStateFourStates(t *testing.T) {
	cases := []struct {
		name      string
		remoteLeg bool
		halted    bool
		enabled   bool
		reachable bool
		want      int
	}{
		{"非远端腿：探测成功也读 0", false, false, true, true, 0},
		{"实盘整体停用：读 0（没有腿可言）", true, false, false, false, 0},
		{"远端腿 + 可达 → 1", true, false, true, true, 1},
		{"远端腿 + 不可达 → 2", true, false, true, false, 2},
		{"远端腿 + 已停 + 仍可达 → 1（停得干净，不是失联）", true, true, true, true, 1},
		{"远端腿 + 已停 + 不可达 → 3（撤销未触达）", true, true, true, false, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			exec := &friendLegSpyExec{}
			cfg := friendLegCfg(tc.remoteLeg, tc.halted)
			cfg.Enabled = tc.enabled
			c := NewController(exec, db, "u_fl_4s", cfg, nil)
			if tc.enabled {
				probeLeg(t, c, exec, tc.reachable)
			}
			if got := c.FriendLegState(); got != tc.want {
				t.Fatalf("FriendLegState()=%d 期望 %d（remoteLeg=%v halted=%v enabled=%v reachable=%v）",
					got, tc.want, tc.remoteLeg, tc.halted, tc.enabled, tc.reachable)
			}
		})
	}
}

// TestFriendLegStateTrippedCountsUnreachable 断线回报那条路径（SetTripped）不会把 lastHealthy
// 置假，所以可达性判据必须同时看熔断态：只看 lastHealthy 会把已经熔掉的远端腿报成"健康"。
// 这正是"腿还在上报成交（上行数字很新）、指令却送不到"的现场形状。
func TestFriendLegStateTrippedCountsUnreachable(t *testing.T) {
	db := testDB(t)
	exec := &friendLegSpyExec{}
	c := NewController(exec, db, "u_fl_trip", friendLegCfg(true, false), nil)
	probeLeg(t, c, exec, true)
	if c.FriendLegState() != 1 {
		t.Fatalf("前置条件：探测成功后应读 1，got %d", c.FriendLegState())
	}
	// 模拟网关断线回报：直接熔，lastHealthy 仍是 true。
	c.SetTripped("测试注入：断线回报")
	if got := c.FriendLegState(); got != 2 {
		t.Fatalf("已熔断的远端腿必须读 2（不可达），got %d", got)
	}
	// 再叠 kill-switch：同一份不可达证据 + Halted ⇒ 3（处置方向变了，不是可达性变了）。
	c.mu.Lock()
	c.cfg.Halted = true
	c.mu.Unlock()
	if got := c.FriendLegState(); got != 3 {
		t.Fatalf("熔断 + 已停用应读 3（撤销未触达），got %d", got)
	}
	// 反证（档位不粘滞）：链路恢复后 3 必须跟着回落。HealthCheck 的既有语义是
	// "熔断中探测成功先解熔，下一轮成功才把 lastHealthy 置真"（持续恢复才算健康），
	// 所以要探两轮；Halted 仍开着，落点只能是 1——停得干净 ≠ 失联，两者处置方向不同。
	probeLeg(t, c, exec, true)
	probeLeg(t, c, exec, true)
	if c.Tripped() {
		t.Fatal("前置条件：连续探测成功应已解熔")
	}
	if got := c.FriendLegState(); got != 1 {
		t.Fatalf("腿恢复后应读 1（kill-switch 单独不制造红），got %d", got)
	}
}

// TestFriendLegStateIssuesNoHTTP 纯读断言：读 N 次远端腿状态，对网关的调用次数必须是 0。
// 现场形状：喂量规的腿（engine/scoring_loop.go refreshStalenessGauges）每 30s 调它一次，
// 若它自己发请求，就会与 HealthCheck 的 miss/2 节流互相打架，还会让"观测挂了"影响下单路径。
func TestFriendLegStateIssuesNoHTTP(t *testing.T) {
	db := testDB(t)
	exec := &friendLegSpyExec{}
	c := NewController(exec, db, "u_fl_nohttp", friendLegCfg(true, false), nil)

	// 先探一次建立状态（这一次是 HealthCheck 发的，不算在本断言里）。
	probeLeg(t, c, exec, true)
	afterProbe := exec.calls
	if afterProbe == 0 {
		t.Fatal("前置条件被破坏：HealthCheck 应当打过一次网关，否则本用例什么都没证明")
	}

	for i := 0; i < 20; i++ {
		if got := c.FriendLegState(); got != 1 {
			t.Fatalf("第 %d 次读数应为 1（探测成功后腿可达），got %d", i, got)
		}
		_ = c.FriendLegState()
	}
	if exec.calls != afterProbe {
		t.Fatalf("FriendLegState() 发了 %d 次网关调用（必须是 0）：观测腿不许自己打网络",
			exec.calls-afterProbe)
	}

	// 同一判据在"腿坏了"的形态下也必须是纯读。
	probeLeg(t, c, exec, false)
	badBase := exec.calls
	for i := 0; i < 10; i++ {
		if got := c.FriendLegState(); got != 2 {
			t.Fatalf("探测失败后应读 2，got %d", got)
		}
	}
	if exec.calls != badBase {
		t.Fatalf("不可达形态下 FriendLegState() 仍补发了 %d 次调用", exec.calls-badBase)
	}
}
