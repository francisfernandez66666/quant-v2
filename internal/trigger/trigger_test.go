// 实时触发引擎单元测试：覆盖滑动窗口差分计算、触发判定与冷却抑制行为。
package trigger

import (
	"testing"
	"time"

	"quant-trading-v2/internal/data"
)

// TestAdvanceWindow 验证窗口推进逻辑：首个 tick 仅初始化，6 秒后应计算出正确的
// 秒均涨幅（约 0.333%/s）与秒均成交额（约 33.3 万）。
func TestAdvanceWindow(t *testing.T) {
	e := New(nil, nil, DefaultConfig())
	now := time.Now()

	// 首个 tick：仅初始化
	_, _, _, st := e.advance("600001", &data.StockInfo{Code: "600001", Price: 10, Amount: 1e7}, now)
	if st == nil {
		t.Fatal("首个 tick 应初始化状态")
	}

	// 6 秒后：+2%（秒均 0.333%/s），成交额 +200 万（秒额 33.3 万）
	secRise, secAmt, _, _ := e.advance("600001", &data.StockInfo{
		Code: "600001", Price: 10.2, Amount: 1.2e7, Turnover: 1,
	}, now.Add(6*time.Second))

	if secRise < 0.333 || secRise > 0.334 {
		t.Errorf("secRise = %v, want ~0.333", secRise)
	}
	if secAmt < 333333.33-1 || secAmt > 333333.33+1 {
		t.Errorf("secAmt = %v, want ~333333.33", secAmt)
	}
}

// TestCheckTriggers 验证 check 全流程：两帧快照（首帧初始化 + 第二帧放量急拉）
// 应把该股票加入监控状态集。
func TestCheckTriggers(t *testing.T) {
	e := New(nil, nil, DefaultConfig())
	now := time.Now()
	// 构造首帧
	e.check(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{
		"600001": {Code: "600001", Price: 10, Amount: 1e7},
	}, Time: now})
	// 第二帧放量急拉
	e.check(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{
		"600001": {Code: "600001", Name: "测试", Price: 10.2, Amount: 1.2e7},
	}, Time: now.Add(6 * time.Second)})
	if got := e.State(); got != 1 {
		t.Errorf("State = %d, want 1", got)
	}
}

// TestCooldownSuppresses 验证冷却机制：股票触发后处于冷却期内，再次急拉应被跳过（advance 返回 nil）。
func TestCooldownSuppresses(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Cooldown = 10 * time.Minute
	e := New(nil, nil, cfg)
	now := time.Now()

	// 触发一次
	e.check(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{
		"600001": {Code: "600001", Price: 10, Amount: 1e7},
	}, Time: now})
	e.check(&data.MarketSnapshot{Stocks: map[string]*data.StockInfo{
		"600001": {Code: "600001", Name: "测试", Price: 10.2, Amount: 1.2e7},
	}, Time: now.Add(6 * time.Second)})

	// 冷却期内再次急拉：advance 应返回 nil（跳过）
	_, _, _, st := e.advance("600001", &data.StockInfo{
		Code: "600001", Price: 10.4, Amount: 1.4e7,
	}, now.Add(12*time.Second))
	if st != nil {
		t.Error("冷却期内不应触发")
	}
}

// ── §W7-SEC 窗口射程（2026-10-06 修复批 波 7）──
// 这四条断言的存在理由：Config.Sec 原来只在默认值、兜底和启动日志里出现，窗口计算根本不读它
// （包注释却写着「窗口内秒均涨幅」）。修完之后必须能**用数字区分 Sec 起了作用与没起作用**，
// 否则「接进去了」这句话和之前一样不可证——按 §DEADGAUGE 的教训，定义没接的形态
// 靠肉眼读代码是看不出来的，只有「换个 Sec 值、读数必须变」这种对照断言能拦住。

// TestWindowBoundedBySec 断言窗口真的按 Sec 收缩：Sec=6 时 15 秒序列里 t0 帧必须已被剪出窗口，
// 于是「t0→t5 的那一波拉升」不再参与 t10 的判定（读数归零），窗口跨度也必须等于 5 秒而不是 10 秒。
func TestWindowBoundedBySec(t *testing.T) {
	cfg := DefaultConfig() // Sec = 6
	e := New(nil, nil, cfg)
	now := time.Now()
	feed := func(dt time.Duration, price, amt float64) (float64, float64) {
		r, a, _, _ := e.advance("600001", &data.StockInfo{Code: "600001", Price: price, Amount: amt}, now.Add(dt))
		return r, a
	}
	feed(0, 10, 1e7)                          // t0：窗口起点（只作基准）
	rise, _ := feed(5*time.Second, 10.5, 2e7) // t5：相对 t0 涨 5%，跨度 5s → 1%/s
	if rise <= 0 {
		t.Fatalf("t5 相对 t0 必须算出正涨幅，实得 %v（窗口没生效？）", rise)
	}
	// t10：价格回平。Sec=6 ⇒ t0（10 秒前）必须被剪掉，起点变成 t5 ⇒ 涨幅 0。
	// 若 Sec 仍无人读，起点会停在 t0，这里会算出 (10.5→10.5)=0……所以再加一手：
	// 让 t10 的价格相对 t0 高、相对 t5 低，只有「起点被换成 t5」才会得到**负**涨幅。
	rise, _ = feed(10*time.Second, 10.4, 3e7)
	if rise >= 0 {
		t.Errorf("t10 的窗口起点必须是 t5（Sec=6 已把 t0 剪出），涨幅应为负，实得 %v", rise)
	}
	if span := e.windowSpan("600001"); span != 5 {
		t.Errorf("窗口跨度 = %v 秒, want 5（Sec=6 剪窗后的真实射程）", span)
	}
}

// TestWiderSecKeepsOlderBase 对照腿：同一条序列把 Sec 换成 30，t0 必须还留在窗口里，
// 于是 t10 的起点仍是 t0、涨幅为正、窗口跨度 10 秒。两腿合起来才是「Sec 真被读」的证据——
// 只留前一条的话，读数变化可能来自别处（比如顺手改了差分公式）。
func TestWiderSecKeepsOlderBase(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sec = 30
	e := New(nil, nil, cfg)
	now := time.Now()
	e.advance("600001", &data.StockInfo{Code: "600001", Price: 10, Amount: 1e7}, now)
	e.advance("600001", &data.StockInfo{Code: "600001", Price: 10.5, Amount: 2e7}, now.Add(5*time.Second))
	rise, amt, _, _ := e.advance("600001", &data.StockInfo{Code: "600001", Price: 10.4, Amount: 3e7}, now.Add(10*time.Second))
	if rise <= 0 {
		t.Errorf("Sec=30 时 t0 应仍在窗口内（相对 t0 仍是上涨），实得 %v", rise)
	}
	if span := e.windowSpan("600001"); span != 10 {
		t.Errorf("窗口跨度 = %v 秒, want 10（Sec=30 未剪掉 t0）", span)
	}
	// 秒均成交额按整段窗口摊：+2000 万 / 10 秒 = 200 万每秒
	if amt < 2e6-1 || amt > 2e6+1 {
		t.Errorf("窗口秒额 = %v, want ~2e6", amt)
	}
}

// TestGapResetsWindow 断言断流（间隔 >60s）后整窗重置：恢复后的第一帧只当基准、不参与判定，
// 不会把断流期间的量摊成「秒均」。旧实现按单帧差分算，这条同样是 0，所以关键断言在窗口跨度＝0
// （重置后只剩一帧）——那才是「起点被换成了当前帧」的可证形态。
func TestGapResetsWindow(t *testing.T) {
	cfg := DefaultConfig()
	e := New(nil, nil, cfg)
	now := time.Now()
	e.advance("600001", &data.StockInfo{Code: "600001", Price: 10, Amount: 1e7}, now)
	rise, _, _, st := e.advance("600001", &data.StockInfo{Code: "600001", Price: 12, Amount: 5e7}, now.Add(120*time.Second))
	if st == nil {
		t.Fatal("断流重置不该返回 nil（nil 只代表冷却）")
	}
	if rise != 0 {
		t.Errorf("断流后第一帧不得产生涨幅读数，实得 %v", rise)
	}
	if span := e.windowSpan("600001"); span != -1 {
		t.Errorf("断流后窗口应只剩当前一帧（windowSpan=-1），实得 %v", span)
	}
}

// TestNegativeCumulativeResetsWindow 断言累计量倒退（数据源回补/重排）不会把负差分摊成读数：
// 窗口作废、涨幅归零，且窗口只剩倒退后的那一帧。
func TestNegativeCumulativeResetsWindow(t *testing.T) {
	cfg := DefaultConfig()
	e := New(nil, nil, cfg)
	now := time.Now()
	e.advance("600001", &data.StockInfo{Code: "600001", Price: 10, Amount: 3e7}, now)
	rise, amt, _, _ := e.advance("600001", &data.StockInfo{Code: "600001", Price: 10.5, Amount: 1e7}, now.Add(5*time.Second))
	if rise != 0 || amt != 0 {
		t.Errorf("累计成交额倒退必须整窗作废，实得 rise=%v amt=%v", rise, amt)
	}
	if span := e.windowSpan("600001"); span != -1 {
		t.Errorf("作废后窗口只剩一帧，实得 %v", span)
	}
}
