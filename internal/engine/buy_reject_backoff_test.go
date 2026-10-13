// §BUY-REJECT-BACKOFF（2026-10-12 波 7）行为测试：业务拒单的按代码指数退避。
// 缺陷原文：确定性业务拒单（资金不足/废单等 200+ok:false）经 controller 降级「发送失败」后，
// ResetFailedRealOrder 在下个打分周期放行重试，同一具必败请求每 ~5s 被重投一次、从开盘
// 热循环到收盘（§R3-1 只修了占位行污染，没修重拨风暴）。修法：一切拨单路径统一走
// placeOrderNow——拨前过 buyRejectAllow 闸（退避窗内跳过），拒单按代码记账、2 分钟起步
// 逐次翻倍封顶 60 分钟；传输失败（err≠nil）不记账（§GAP2-W1 瞬态重试语义保留）；
// 下单成功即清零该代码的退避账。
// English: §BUY-REJECT-BACKOFF behavior tests — a deterministically rejected buy used to be
// re-dialed every ~5s scoring cycle all day. Every dial path now goes through placeOrderNow:
// per-code exponential backoff (2min doubling, 60min cap) on business rejection, transport
// errors stay retryable, success clears the ledger.
package engine

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// backoffGateway 拨单可切换受理/拒单的假网关：reject=true 时 /order 回 200+ok:false
// （确定性业务拒单形态，走 controller 降级「发送失败」→ 引擎记账链路）；dials 记真实到拨数。
type backoffGateway struct {
	mu     sync.Mutex
	dials  int
	reject bool
	srv    *httptest.Server
}

func newBackoffGateway(t *testing.T) *backoffGateway {
	t.Helper()
	gw := &backoffGateway{reject: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"ts":"now"}`))
	})
	mux.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		gw.mu.Lock()
		gw.dials++
		reject := gw.reject
		gw.mu.Unlock()
		if reject {
			w.Write([]byte(`{"ok":false,"err":"资金不足"}`))
			return
		}
		w.Write([]byte(`{"ok":true,"order_id":"GW1"}`))
	})
	gw.srv = httptest.NewServer(mux)
	t.Cleanup(gw.srv.Close)
	return gw
}

func (gw *backoffGateway) dialCount() int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return gw.dials
}

func (gw *backoffGateway) setReject(v bool) {
	gw.mu.Lock()
	gw.reject = v
	gw.mu.Unlock()
}

// newBackoffEngine 与 newQMTEngine 同构（auto 模式 + 新鲜充足资金 + 指向假网关），
// 但网关可切换拒单形态。与 m12 系用例共用 m12BuySig/m12Live 造信号。
func newBackoffEngine(t *testing.T, gw *backoffGateway) (*Engine, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "backoff.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// 同 §M12-A：不播种新鲜资金，autoPlace 会在现金 fail-close 处被拦，测不到退避主题。
	if err := db.UpsertRealAccount(store.RealAccount{UserID: "u_1", AvailableCash: 10_000_000,
		UpdatedAt: time.Now().In(cntime.Loc).Format("2006-01-02 15:04:05")}); err != nil {
		t.Fatalf("seed cash: %v", err)
	}
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.Mode = "auto"
	cfg.GatewayURL = gw.srv.URL
	cfg.FixedAmount = 10000
	cfg.MissHeartbeatSec = 5
	exec := trading.NewQMTClient(gw.srv.URL, "", 2*time.Second, 0)
	ctrl := trading.NewController(exec, db, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, db)
	return e, db
}

// expireBackoff 直改退避账把冷却窗拨到已过期（同包访问，免等真实 2 分钟）。
func expireBackoff(e *Engine, code string) {
	e.buyRejectMu.Lock()
	e.buyRejectNextAt[code] = time.Now().Add(-time.Second)
	e.buyRejectMu.Unlock()
}

// TestBuyRejectBackoffSuppressesRedial 主链路：拒单→记账→窗内不再重拨→窗过期后放行重试并
// 按翻倍档位续期。三条腿全部经 autoPlace 全链（守卫→组装→placeOrderNow→闸/记账），与
// 生产拨单路径同构（dispatcher 未启动时走同步直调分支，同样过 placeOrderNow）。
func TestBuyRejectBackoffSuppressesRedial(t *testing.T) {
	gw := newBackoffGateway(t)
	e, _ := newBackoffEngine(t, gw)
	code := "600911"

	// ①首拨：确定性拒单 → 记账（streak=1，冷却窗 ≈2 分钟）。
	e.autoPlace(m12BuySig(code), m12Live(code))
	if got := gw.dialCount(); got != 1 {
		t.Fatalf("首拨应真实到网关一次，实际 %d 次", got)
	}
	e.buyRejectMu.Lock()
	streak := e.buyRejectStreak[withSuffix(code)]
	next, has := e.buyRejectNextAt[withSuffix(code)]
	e.buyRejectMu.Unlock()
	if streak != 1 || !has {
		t.Fatalf("首拒后应 streak=1 且已记 nextAt，实际 streak=%d has=%v", streak, has)
	}
	if wait := time.Until(next); wait < time.Minute || wait > 3*time.Minute {
		t.Fatalf("第 1 档冷却应约 2 分钟，实际 %v", wait)
	}

	// ②窗内重拨被闸住：同一信号再来一轮（≈5s 打分周期形态），网关拨数不得涨。
	e.autoPlace(m12BuySig(code), m12Live(code))
	if got := gw.dialCount(); got != 1 {
		t.Fatalf("退避窗内必须跳过重拨，实际到网关 %d 次", got)
	}

	// ③窗过期 → 放行重试，再拒 → streak=2 且按翻倍档位续期（第 2 档 ≈4 分钟）。
	expireBackoff(e, withSuffix(code))
	e.autoPlace(m12BuySig(code), m12Live(code))
	if got := gw.dialCount(); got != 2 {
		t.Fatalf("冷却过期后应放行重拨，实际到网关 %d 次", got)
	}
	e.buyRejectMu.Lock()
	streak2 := e.buyRejectStreak[withSuffix(code)]
	next2 := e.buyRejectNextAt[withSuffix(code)]
	e.buyRejectMu.Unlock()
	if streak2 != 2 {
		t.Fatalf("第二次拒单应 streak=2，实际 %d", streak2)
	}
	if wait := time.Until(next2); wait < 3*time.Minute || wait > 5*time.Minute {
		t.Fatalf("第 2 档冷却应约 4 分钟（指数翻倍），实际 %v", wait)
	}
}

// TestBuyRejectBackoffClearsOnSuccess 成功即清零：退避账在账时拨单成功受理，
// 账目清空、随后的下一拨不被任何残余冷却压制。
func TestBuyRejectBackoffClearsOnSuccess(t *testing.T) {
	gw := newBackoffGateway(t)
	e, _ := newBackoffEngine(t, gw)
	code := "600912"

	e.autoPlace(m12BuySig(code), m12Live(code)) // 拒单一次，账在账
	if got := gw.dialCount(); got != 1 {
		t.Fatalf("首拨应到网关，实际 %d 次", got)
	}
	gw.setReject(false) // 券商侧"资金解开"形态：下次受理
	expireBackoff(e, withSuffix(code))
	e.autoPlace(m12BuySig(code), m12Live(code))
	if got := gw.dialCount(); got != 2 {
		t.Fatalf("过期重拨应到网关，实际 %d 次", got)
	}
	e.buyRejectMu.Lock()
	nNext, nStreak := len(e.buyRejectNextAt), len(e.buyRejectStreak)
	e.buyRejectMu.Unlock()
	if nNext != 0 || nStreak != 0 {
		t.Fatalf("受理成功必须清零退避账，实际 nextAt=%d 项 streak=%d 项", nNext, nStreak)
	}
	// 清零后再拨同键：controller 幂等拦截（duplicate，未到网关）——这是幂等键的本职，
	// 不得被当成业务拒单给该代码重新记退避（否则 A 战法的重复会封住 B 战法同码首拨）。
	e.autoPlace(m12BuySig(code), m12Live(code))
	if got := gw.dialCount(); got != 2 {
		t.Fatalf("同键重复应被幂等拦截（不再到网关），实际 %d 次", got)
	}
	e.buyRejectMu.Lock()
	nNext2, nStreak2 := len(e.buyRejectNextAt), len(e.buyRejectStreak)
	e.buyRejectMu.Unlock()
	if nNext2 != 0 || nStreak2 != 0 {
		t.Fatalf("duplicate 拦截不得记入退避账，实际 nextAt=%d 项 streak=%d 项", nNext2, nStreak2)
	}
}

// TestBuyRejectBackoffSyncFallbackAlsoGated 同步兜底分支同闸：autoPlace 的队列满回落与
// 无分发器直调都改走 placeOrderNow（§BUY-REJECT-BACKOFF 不留旁路）。本用例不开 dispatcher，
// 走的正是"无分发器直调"分支——若有人把直调改回裸 PlaceOrder，本用例的窗内拦截即红。
func TestBuyRejectBackoffSyncFallbackAlsoGated(t *testing.T) {
	gw := newBackoffGateway(t)
	e, _ := newBackoffEngine(t, gw)
	code := "600913"

	e.autoPlace(m12BuySig(code), m12Live(code)) // 拒单记账
	e.autoPlace(m12BuySig(code), m12Live(code)) // 窗内重拨
	if got := gw.dialCount(); got != 1 {
		t.Fatalf("无分发器直调分支也必须过退避闸（旁路形态复活），实际到网关 %d 次", got)
	}
}

// TestBuyRejectDelayLadder 退避档位单测：2min 起步逐次翻倍、封顶 60min。
func TestBuyRejectDelayLadder(t *testing.T) {
	cases := map[int]time.Duration{
		1: 2 * time.Minute,
		2: 4 * time.Minute,
		3: 8 * time.Minute,
		4: 16 * time.Minute,
		5: 32 * time.Minute,
		6: 60 * time.Minute, // 64min → 封顶
		9: 60 * time.Minute,
	}
	for streak, want := range cases {
		if got := buyRejectDelay(streak); got != want {
			t.Fatalf("buyRejectDelay(%d)=%v，期望 %v", streak, got, want)
		}
	}
}

// TestBuyRejectBackoffTransportErrorNotRecorded 传输失败不记账（§GAP2-W1 语义保留）：
// 网关整条链路不可达（err≠nil）与业务拒单（ok:false）必须区分——前者是瞬态，
// 重试通道不得被退避封死。账本应在传输失败后保持空。
func TestBuyRejectBackoffTransportErrorNotRecorded(t *testing.T) {
	gw := newBackoffGateway(t)
	e, _ := newBackoffEngine(t, gw)
	code := "600914"

	gw.srv.Close() // 网关下线 → PlaceOrder 返回传输错误
	e.autoPlace(m12BuySig(code), m12Live(code))
	e.autoPlace(m12BuySig(code), m12Live(code)) // 传输失败的重拨不被退避闸拦（两次都真拨）
	e.buyRejectMu.Lock()
	nNext, nStreak := len(e.buyRejectNextAt), len(e.buyRejectStreak)
	e.buyRejectMu.Unlock()
	if nNext != 0 || nStreak != 0 {
		t.Fatalf("传输失败不得记入退避账，实际 nextAt=%d 项 streak=%d 项", nNext, nStreak)
	}
}
