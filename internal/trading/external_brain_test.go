// external_brain_test.go — §EXT-BRAIN-1104 外脑模式判定的单元锁（2026-11-04）。
//
// 判据本体在 external_brain.go（四档：0 不适用/1 正常/2 读不到柜台/3 通道断/4 显式影子态），
// 这里锁的是三条纪律 + 一档优先级：
//  1. **优先级**：显式影子态赢过一切自动判定（owner 的确定性意图不许被自动判定推翻）；
//  2. **未知不装知道**：没探过 / 网关不发这个键 / 时间串解析不了，三种"未知"都不许
//     被读成断链或过期（§M2 与 §0925EVE-W3-E 的同族口径，正反两个方向都不许装）；
//  3. **判定只读**：连判 N 次不推进任何基线、不改任何缓存（§FIX-20261028-F02 的
//     "判据函数必须只读"纪律）；唯一写入点是 HealthCheck 里的 captureQueuedConnected。
//  4. 档位数字是对外契约（前端按数字分形），所以数值本身也上锁——改号必须同批改前端。
//
// English: unit locks for the four-tier external-brain judgement — priority of the explicit
// shadow state, "unknown stays unknown" in both directions, read-only judgement, and the
// numeric tier contract.
package trading

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
)

// errFakeDown 探测失败的哨兵错误（用例只关心"这次探测没成功"，不关心错误细节）。
var errFakeDown = errors.New("fake gateway down")

// ebExec 可控执行器：Health 结果与桥心跳读数都由用例给，其余方法只记账不真做。
type ebExec struct {
	healthOK   bool
	healthErr  error
	queued     *bool // nil = 这版网关不发这个键（读数未知）
	placeCalls int
}

func (e *ebExec) PlaceBuy(req OrderRequest) (*OrderResult, error) {
	e.placeCalls++
	return &OrderResult{OK: true, OrderID: "EB1"}, nil
}
func (e *ebExec) PlaceSell(req OrderRequest) (*OrderResult, error) {
	e.placeCalls++
	return &OrderResult{OK: true, OrderID: "EB1"}, nil
}
func (e *ebExec) Cancel(string) error           { return nil }
func (e *ebExec) State() (*GatewayState, error) { return &GatewayState{}, nil }
func (e *ebExec) Health() (bool, error)         { return e.healthOK, e.healthErr }

// QueuedConnected 实现 queuedConnReader（可选能力）；未知就老实返回未知。
func (e *ebExec) QueuedConnected() (value bool, known bool) {
	if e.queued == nil {
		return false, false
	}
	return *e.queued, true
}

// ebDB 开一个临时账本库。
func ebDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "eb.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ebCtrl 造一个"配了网关、开了实盘"的控制器，执行器与账本由用例给。
func ebCtrl(t *testing.T, exec Executor, db *store.DB, uid string) *Controller {
	t.Helper()
	return NewController(exec, db, uid, config.QMTConfig{
		Enabled: true, Mode: "auto", GatewayURL: "http://127.0.0.1:8789", MissHeartbeatSec: 5,
	}, nil)
}

// ebSeedCash 播种资金行（updatedAt 由用例给，用于档 2/1 的分界）。
func ebSeedCash(t *testing.T, db *store.DB, uid, updatedAt string) {
	t.Helper()
	if err := db.UpsertRealAccount(store.RealAccount{
		UserID: uid, AvailableCash: 100000, TotalAsset: 100000, UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatalf("seed cash: %v", err)
	}
}

// TestExternalBrainShadowBeatsAutoJudgement 档 4 赢过一切：桥已知断 + 资金已知过期，
// 只要 qmt.enabled=false 就判 4（显式意图不许被自动判定推翻）。
func TestExternalBrainShadowBeatsAutoJudgement(t *testing.T) {
	db := ebDB(t)
	uid := "u_eb4"
	ebSeedCash(t, db, uid, time.Now().Add(-3*time.Hour).Format("2006-01-02 15:04:05"))
	exec := &ebExec{healthOK: true, queued: boolPtr(false)}
	c := ebCtrl(t, exec, db, uid)
	c.mu.Lock()
	c.cfg.Enabled = false // 显式影子态
	c.mu.Unlock()
	if code, reason := c.ExternalBrain(); code != ExternalBrainShadow {
		t.Fatalf("显式影子态必须赢，实得 %d（%s）", code, reason)
	}
	if blocked, code, _ := c.ExternalBrainBlocksAutoBuy(); !blocked || code != ExternalBrainShadow {
		t.Fatalf("影子态必须挡开新仓，实得 blocked=%v code=%d", blocked, code)
	}
}

// TestExternalBrainNoGatewayIsNotApplicable 没配网关的账号判 0，且**绝不挡买入**
// （纯纸面/研究账号不能被这套判定顺手掐掉）。
func TestExternalBrainNoGatewayIsNotApplicable(t *testing.T) {
	c := NewController(&ebExec{healthOK: false, healthErr: errFakeDown}, nil, "u_eb0",
		config.QMTConfig{Enabled: true, Mode: "auto"}, nil)
	if code, reason := c.ExternalBrain(); code != ExternalBrainNA {
		t.Fatalf("未配网关应判 0（不适用），实得 %d（%s）", code, reason)
	}
	if blocked, code, _ := c.ExternalBrainBlocksAutoBuy(); blocked {
		t.Fatalf("档 0 不得挡买入，实得 blocked=%v code=%d", blocked, code)
	}
}

// TestExternalBrainProbeFailureIsThree 探测失败（探过且不健康）⇒ 档 3。
// 另一半：没探过时（lastHealthAt 零值）不许判 3——否则"进程刚起还没探测"会被读成断链。
func TestExternalBrainProbeFailureIsThree(t *testing.T) {
	db := ebDB(t)
	uid := "u_eb3"
	ebSeedCash(t, db, uid, time.Now().Format("2006-01-02 15:04:05"))
	exec := &ebExec{healthOK: false, healthErr: errFakeDown}
	c := ebCtrl(t, exec, db, uid)
	c.HealthCheck()
	if code, reason := c.ExternalBrain(); code != ExternalBrainChannelDown {
		t.Fatalf("探测失败应判 3，实得 %d（%s）", code, reason)
	}
	// 对照：同配置但一次都没探过 ⇒ 判 1（未知不装断）。
	c2 := ebCtrl(t, &ebExec{healthOK: true}, db, uid)
	if code, reason := c2.ExternalBrain(); code != ExternalBrainNormal {
		t.Fatalf("还没探过时不许判断，实得 %d（%s）", code, reason)
	}
}

// TestExternalBrainBridgeHeartbeatDownIsThree 桥心跳已知为假 ⇒ 档 3；为真 ⇒ 档 1；
// 缺键（未知）⇒ 档 1。三个方向一起锁，防"未知"在任一侧伪装成已知。
func TestExternalBrainBridgeHeartbeatDownIsThree(t *testing.T) {
	db := ebDB(t)
	uid := "u_ebhb"
	ebSeedCash(t, db, uid, time.Now().Format("2006-01-02 15:04:05"))
	for _, tc := range []struct {
		name   string
		queued *bool
		want   int64
	}{
		{"桥心跳为真", boolPtr(true), ExternalBrainNormal},
		{"桥心跳为假", boolPtr(false), ExternalBrainChannelDown},
		{"网关不发这个键（未知）", nil, ExternalBrainNormal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ebCtrl(t, &ebExec{healthOK: true, queued: tc.queued}, db, uid)
			c.HealthCheck()
			if code, reason := c.ExternalBrain(); code != tc.want {
				t.Fatalf("want %d got %d（%s）", tc.want, code, reason)
			}
		})
	}
}

// TestExternalBrainReadStaleIsTwo 通道在、资金行过窗（>1 小时）⇒ 档 2，
// 且文案指向"柜台读数过期"而不是"通道断"（两者处置方向不同：查导出 vs 查进程）。
// 对照：新鲜读数 ⇒ 1；没有账户行（还没开实盘）⇒ 1 而不是 2。
func TestExternalBrainReadStaleIsTwo(t *testing.T) {
	db := ebDB(t)
	old := time.Now().Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	fresh := time.Now().Format("2006-01-02 15:04:05")
	ebSeedCash(t, db, "u_eb2old", old)
	cOld := ebCtrl(t, &ebExec{healthOK: true}, db, "u_eb2old")
	code, reason := cOld.ExternalBrain()
	if code != ExternalBrainReadStale {
		t.Fatalf("读数过窗应判 2，实得 %d（%s）", code, reason)
	}
	if reason == "" || containsCN(reason, "通道") {
		t.Fatalf("档 2 文案必须指向读数过期，不得说成通道断：%s", reason)
	}
	ebSeedCash(t, db, "u_eb2new", fresh)
	cFresh := ebCtrl(t, &ebExec{healthOK: true}, db, "u_eb2new")
	if code, _ := cFresh.ExternalBrain(); code != ExternalBrainNormal {
		t.Fatalf("新鲜读数应判 1，实得 %d", code)
	}
	cNone := ebCtrl(t, &ebExec{healthOK: true}, db, "u_eb2none")
	if code, _ := cNone.ExternalBrain(); code != ExternalBrainNormal {
		t.Fatalf("没有账户行＝还没开实盘，应判 1 而不是 2，实得 %d", code)
	}
}

// TestExternalBrainUnparsableTimeIsNotStale 时间串解析不了 ⇒ 读数未知 ⇒ 判 1。
// 不许拿零值时间去减出"几十年"再报过期（本仓"未知不装新鲜/不装过期"的同族口径）。
func TestExternalBrainUnparsableTimeIsNotStale(t *testing.T) {
	db := ebDB(t)
	uid := "u_ebbad"
	ebSeedCash(t, db, uid, "not-a-timestamp")
	c := ebCtrl(t, &ebExec{healthOK: true}, db, uid)
	if code, reason := c.ExternalBrain(); code != ExternalBrainNormal {
		t.Fatalf("解析失败应判 1（未知），实得 %d（%s）", code, reason)
	}
}

// TestExternalBrainJudgementIsReadOnly 判定函数连判五次不推进任何状态：
// 桥心跳缓存、探测结果、资金行时间戳三者前后逐值一致。
// 反证：把 captureQueuedConnected 或任何写盘/推进塞进判定体 ⇒ 这里红。
func TestExternalBrainJudgementIsReadOnly(t *testing.T) {
	db := ebDB(t)
	uid := "u_ebro"
	updatedAt := time.Now().Add(-90 * time.Minute).Format("2006-01-02 15:04:05")
	ebSeedCash(t, db, uid, updatedAt)
	c := ebCtrl(t, &ebExec{healthOK: true, queued: boolPtr(true)}, db, uid)
	c.HealthCheck()
	c.mu.RLock()
	wantKnown, wantQueued, wantHealthy, wantLastAt := c.lastQueuedKnown, c.lastQueuedConnected, c.lastHealthy, c.lastHealthAt
	c.mu.RUnlock()
	for i := 0; i < 5; i++ {
		c.ExternalBrain()
		c.ExternalBrainBlocksAutoBuy()
	}
	c.mu.RLock()
	gotKnown, gotQueued, gotHealthy, gotLastAt := c.lastQueuedKnown, c.lastQueuedConnected, c.lastHealthy, c.lastHealthAt
	c.mu.RUnlock()
	if gotKnown != wantKnown || gotQueued != wantQueued || gotHealthy != wantHealthy || !gotLastAt.Equal(wantLastAt) {
		t.Fatalf("判定函数写了状态：known %v→%v queued %v→%v healthy %v→%v lastAt %v→%v",
			wantKnown, gotKnown, wantQueued, gotQueued, wantHealthy, gotHealthy, wantLastAt, gotLastAt)
	}
	if acc, err := db.GetRealAccountScoped(uid, false); err != nil || acc.UpdatedAt != updatedAt {
		t.Fatalf("判定函数不得改资金行：%v updated_at=%q", err, acc.UpdatedAt)
	}
}

// TestCaptureQueuedConnectedIsSingleWriterAndNoDowngrade 采集腿两件事：
//  1. 它是唯一写入点（只有 HealthCheck/captureQueuedConnected 会推进，判定函数不会）；
//  2. 已知 → 未知 的降级**不许覆盖**上一份已知读数（宁可少一个判据，不伪造一个）。
func TestCaptureQueuedConnectedIsSingleWriterAndNoDowngrade(t *testing.T) {
	db := ebDB(t)
	uid := "u_ebw"
	ebSeedCash(t, db, uid, time.Now().Format("2006-01-02 15:04:05"))
	exec := &ebExec{healthOK: true, queued: boolPtr(true)}
	c := ebCtrl(t, exec, db, uid)
	// 第一次走整条 HealthCheck（证明接线：探测腿里确实调了采集）。
	c.HealthCheck()
	c.mu.RLock()
	firstKnown, firstVal := c.lastQueuedKnown, c.lastQueuedConnected
	c.mu.RUnlock()
	if !firstKnown || !firstVal {
		t.Fatalf("第一次探测应采集到 known=true/true，got known=%v val=%v", firstKnown, firstVal)
	}
	// 后续直接调采集函数：HealthCheck 自带节流（miss_heartbeat_sec 的 1/2），
	// 同一秒内连探会被跳过——那不是本用例要判的东西。
	// 换成不提供该能力的执行器（老网关/降级实现）：保持上一份已知，不降级成未知。
	c.exec.Store(execHolder{&ebExec{healthOK: true}})
	c.captureQueuedConnected()
	c.mu.RLock()
	keptKnown, keptVal := c.lastQueuedKnown, c.lastQueuedConnected
	c.mu.RUnlock()
	if !keptKnown || !keptVal {
		t.Fatalf("未知不得覆盖已知：got known=%v val=%v", keptKnown, keptVal)
	}
	// 已知为假：这才允许推进（断桥形态）。
	c.exec.Store(execHolder{&ebExec{healthOK: true, queued: boolPtr(false)}})
	c.captureQueuedConnected()
	c.mu.RLock()
	nowKnown, nowVal := c.lastQueuedKnown, c.lastQueuedConnected
	c.mu.RUnlock()
	if !nowKnown || nowVal {
		t.Fatalf("桥断应推进成 known=true/false，got known=%v val=%v", nowKnown, nowVal)
	}
	if code, _ := c.ExternalBrain(); code != ExternalBrainChannelDown {
		t.Fatalf("采集到桥断后应判档 3，实得 %d", code)
	}
}

// TestExternalBrainTierNumbersAreExternalContract 档位数字是对外契约（前端 /api/qmt/state 的
// external_brain 按数字分形，不解析文案）。改号必须同批改前端映射，所以这里先把数字钉死。
func TestExternalBrainTierNumbersAreExternalContract(t *testing.T) {
	want := map[string]int64{
		"NA":     0,
		"Normal": 1,
		"Stale":  2,
		"Down":   3,
		"Shadow": 4,
	}
	got := map[string]int64{
		"NA":     ExternalBrainNA,
		"Normal": ExternalBrainNormal,
		"Stale":  ExternalBrainReadStale,
		"Down":   ExternalBrainChannelDown,
		"Shadow": ExternalBrainShadow,
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("档位 %s 数字漂移：want %d got %d（前端映射需同批改）", k, v, got[k])
		}
	}
}

// boolPtr 取 bool 指针（三态读数的构造辅助）。
func boolPtr(v bool) *bool { return &v }

// containsCN 判子串（避免为了一个断言引 strings 之外多余依赖）。
func containsCN(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
