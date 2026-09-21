// fix_h4_h5_20260922_test.go — 2026-09-22 修复批 H4/H5 的反例行为锁。
//
// H4 幂等槽先烧后用（scoring_loop.go autoExecuteRealSells）：
//   - 卖单失败（网关业务拒单/发送失败）→ realTrimDone 不置位，同日下一轮仍可再次触发减仓；
//   - 卖单成功 → 槽位回写，同日只此一次（纪律状态机重放被去重拦截）；
//   - 失败必须可见：log + opslog 留档，不再 `_ =` 吞错。
//
// H5 scoresAt 时钟错配（sell_shadow.go judgeSellPositions）：
//   - 5min 批量轮信号（打分自身 UpdatedAt 新鲜、全局 scoresAt 因 5s 轮池空冻结而陈旧）
//     → 延持判定必须正确（拿到延持资格，不落观察窗）；
//   - 打分无自带时刻（存量/测试装配）→ 回退全局时钟兜底（旧语义保留）；
//   - 打分自身 At 陈旧但全局时钟新鲜 → 按信号自身判过期（反向锁：旧实现会误给延持）。
//
// English: locks for the 2026-09-22 H4/H5 fixes — the trim idempotency slot is only burned
// after the sell order actually succeeds (failures stay retryable the same day and are logged),
// and bull-signal freshness follows each signal's own production timestamp instead of the
// 5s-round-only global clock.
package engine

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// h4Executor 可控卖单执行器：reject=true 时模拟网关 200+ok:false 业务拒单（err=nil，
// §R3-1 P0-A 形态——正是旧实现返回 nil 被调用方当成功的假成功场景）；reject=false 正常受理。
type h4Executor struct {
	mu       sync.Mutex
	reject   bool
	attempts int // 卖出报单尝试次数
}

func (x *h4Executor) PlaceBuy(trading.OrderRequest) (*trading.OrderResult, error) {
	return &trading.OrderResult{OK: true, OrderID: "GW-BUY"}, nil
}

func (x *h4Executor) PlaceSell(trading.OrderRequest) (*trading.OrderResult, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.attempts++
	if x.reject {
		return &trading.OrderResult{OK: false, Err: "mock: 柜台业务拒单"}, nil
	}
	return &trading.OrderResult{OK: true, OrderID: "GW-H4"}, nil
}

func (x *h4Executor) Cancel(string) error { return nil }

func (x *h4Executor) State() (*trading.GatewayState, error) {
	return &trading.GatewayState{Connected: true}, nil
}

func (x *h4Executor) Health() (bool, error) { return true, nil }

func (x *h4Executor) setReject(v bool) {
	x.mu.Lock()
	x.reject = v
	x.mu.Unlock()
}

func (x *h4Executor) calls() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.attempts
}

// h4Env 构造 auto+auto_sell 全开的实盘引擎（挂可控执行器）。
func h4Env(t *testing.T, exec trading.Executor) (*Engine, *store.DB) {
	t.Helper()
	realDB, err := store.Open(t.TempDir() + "/live.db")
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	t.Cleanup(func() { realDB.Close() })
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.Mode = "auto"
	cfg.AutoSell = true
	ctrl := trading.NewController(exec, realDB, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, realDB)
	return e, realDB
}

// TestH4TrimSlotRetryableAfterSellFailure §H4 核心反例：减仓卖单失败 → 槽位不烧、同日仍可
// 再次触发；卖单成功 → 槽位回写、同日只此一次。旧实现（下卖单前置 realTrimDone + `_ =` 吞错）
// 在第一步失败后即永久烧槽，第二次调用不会再报到（attempts 恒为 1）。
func TestH4TrimSlotRetryableAfterSellFailure(t *testing.T) {
	exec := &h4Executor{reject: true}
	e, db := h4Env(t, exec)
	if _, err := db.UpsertRealPositions([]store.RealPosition{
		{TsCode: "600000.SH", Name: "浦发", Qty: 500, CostPrice: 10, Amount: 5000, Strategy: "龙头"},
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := e.QMTController()
	adv := []trading.PositionAdvice{
		{Code: "600000", TsCode: "600000.SH", Action: "减仓", Level: "高", RefPrice: 9, Source: "discipline", Reason: "首触止损未深破"},
	}
	trimDone := func() string {
		e.mu.RLock()
		defer e.mu.RUnlock()
		return e.realTrimDone["600000.SH"]
	}

	// 捕获标准日志断言失败不再被吞（opslog 未 Init 静默降级，log 面必须有痕）。
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	// 1. 卖单失败（业务拒单）：尝试 1 次、槽位不烧、错误日志可见。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, adv)
	log.SetOutput(old)
	if n := exec.calls(); n != 1 {
		t.Fatalf("首轮减仓应报单 1 次, got %d", n)
	}
	if trimDone() != "" {
		t.Fatalf("§H4 卖单失败不得烧 realTrimDone 槽, got %q", trimDone())
	}
	if !strings.Contains(buf.String(), "实盘自动卖单失败") {
		t.Fatalf("§H4 卖单失败必须打日志留档，got: %s", buf.String())
	}

	// 2. 同日下一轮重放：仍可再次触发（缺陷形态=槽位已烧被静默拦截）。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, adv)
	if n := exec.calls(); n != 2 {
		t.Fatalf("§H4 卖单失败后同日应可重试, attempts=%d", n)
	}
	if trimDone() != "" {
		t.Fatalf("卖单再次失败仍不得烧槽, got %q", trimDone())
	}

	// 3. 网关恢复：卖单成功 → 槽位回写为当日。
	exec.setReject(false)
	e.autoExecuteRealSells(e.UserID(), ctrl, db, adv)
	if n := exec.calls(); n != 3 {
		t.Fatalf("重试轮应再次报单, attempts=%d", n)
	}
	if day := trimDone(); day != time.Now().Format("20060102") {
		t.Fatalf("§H4 卖单成功后才烧槽，realTrimDone 应=当日, got %q", day)
	}

	// 4. 成功后同日重放：去重拦截，不再报单（每码每日一次语义保持不变）。
	e.autoExecuteRealSells(e.UserID(), ctrl, db, adv)
	if n := exec.calls(); n != 3 {
		t.Fatalf("§H4 减仓成功后同日只此一次，不得再报单, attempts=%d", n)
	}
}

// h5JudgeCase 跑一轮 live 影子裁决，返回 600580.SH 的裁决视图。
func h5JudgeCase(t *testing.T, scoresAt time.Time, sc combat_agent.StockScores) sellRoundVerdict {
	t.Helper()
	e := shadowTestEnv(t, "shadow")
	e.mu.Lock()
	e.scoresAt = scoresAt
	e.mu.Unlock()
	positions := []store.RealPosition{{TsCode: "600580.SH", Name: "时钟测试", Qty: 100, CostPrice: 10}}
	quotes := map[string]*data.StockInfo{"600580": {Price: 12.5}} // +25% 越过 15% 止盈线
	scores := map[string]combat_agent.StockScores{"600580": sc}
	verdicts := e.runSellUnifiedJudge("u_1", positions, nil, quotes, scores, nil, nil, e.sellUnifiedModeEngine())
	if len(verdicts) != 1 {
		t.Fatalf("应产出 1 条有效裁决, got %d", len(verdicts))
	}
	return verdicts[0]
}

// TestH5BullFreshnessFromOwnTimestamp §H5 核心用例：5min 批量轮信号——打分自身 At 新鲜、
// 全局 scoresAt 陈旧（5s 轮池空冻结）→ 延持判定正确（止盈延持、不开观察窗、无处置）。
// 旧实现取全局时钟，该信号会被误判过期落入观察窗。
func TestH5BullFreshnessFromOwnTimestamp(t *testing.T) {
	v := h5JudgeCase(t, time.Now().Add(-30*time.Minute), combat_agent.StockScores{
		Code: "600580", SignalActive: true, UpdatedAt: time.Now(), // 批量轮刚产分，At 新鲜
	})
	if v.Verdict.Disposal != nil {
		t.Fatalf("延持轮不得出处置, got %+v", v.Verdict.Disposal)
	}
	if !strings.Contains(v.Verdict.HoldReason, "延持") {
		t.Fatalf("§H5 信号自身 At 新鲜应拿到延持资格, HoldReason=%q", v.Verdict.HoldReason)
	}
	if v.Verdict.Line != signalctl.SellLineNone {
		t.Fatalf("止盈延持不开观察窗（线型保持 None）, got %v", v.Verdict.Line)
	}
}

// TestH5BullFreshnessFallbackToGlobalClock 兜底锁：打分无自带时刻（零值，测试/存量装配）→
// 回退全局 scoresAt；全局时钟陈旧时按旧语义不给延持资格（落入观察窗）。
func TestH5BullFreshnessFallbackToGlobalClock(t *testing.T) {
	v := h5JudgeCase(t, time.Now().Add(-30*time.Minute), combat_agent.StockScores{
		Code: "600580", SignalActive: true, // UpdatedAt 零值
	})
	if strings.Contains(v.Verdict.HoldReason, "延持") {
		t.Fatalf("兜底路径：全局时钟陈旧不应给延持资格, HoldReason=%q", v.Verdict.HoldReason)
	}
	if v.Verdict.Line != signalctl.SellLineTakeProfit {
		t.Fatalf("过期做多信号应锁定止盈线进观察窗, got %v", v.Verdict.Line)
	}
}

// TestH5StaleSignalAgeOutDespiteFreshGlobalClock 反向锁（池空冻结修复的另一半）：全局
// scoresAt 新鲜，但信号自身 At 已超龄（默认 5min）→ 按信号自身判过期，绝不误给延持资格。
// 旧实现看全局时钟会错误延持——陈旧 SignalActive 借尸还魂。
func TestH5StaleSignalAgeOutDespiteFreshGlobalClock(t *testing.T) {
	v := h5JudgeCase(t, time.Now(), combat_agent.StockScores{
		Code: "600580", SignalActive: true, UpdatedAt: time.Now().Add(-30 * time.Minute),
	})
	if strings.Contains(v.Verdict.HoldReason, "延持") {
		t.Fatalf("§H5 信号自身超龄不得拿到延持资格, HoldReason=%q", v.Verdict.HoldReason)
	}
	if v.Verdict.Line != signalctl.SellLineTakeProfit {
		t.Fatalf("超龄信号应锁定止盈线进观察窗, got %v", v.Verdict.Line)
	}
}
