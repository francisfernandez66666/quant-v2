// t1_clamp_test.go — §T1-PARTIAL-W5 实盘侧 T+1 可卖量双态行为锁（owner 裁决 Q7）。
//
// 背景：改前 `checkT1Sellable` 只有一态——请求量 > 可卖量就整单拒。实盘止损因此会"挂整天不执行"
// （#3 要收掉的形态）。owner 裁决改成「减量放行 + 残量意图」，但**默认仍保持整单拒**（资金路径上
// 两种处置都正当，不能单边替换）。本文件把两档各拨一次，并把"可卖量为 0 时两档都拒"这条
// 边界单独钉住——钳到 0 股不是放行，是一张必被柜台打回的零股委托。
//
// English: §T1-PARTIAL-W5 — dual-mode behavioural locks for the live T+1 sellable gate: legacy
// whole-order rejection (default) vs. explicit clamping, plus the sellable==0 boundary where both
// modes must reject (clamping to zero shares is not a pass, it is a doomed order).
package risk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// t1DB 建临时实盘账本。
//
// 为什么不复用 gate_test.go 的 `gateDB`（它用 `t.TempDir()`）：`t.TempDir()` 会在 Cleanup 里
// RemoveAll 并在**删除失败时把测试判红**。本机安全策略在部分会话里禁止一切 file-delete
// （判据：stderr 出现 `delete …/xxx.db-journal (rule: …)`，连系统 `sqlite3` CLI 都写不进去），
// 于是"断言全过、测试却红"——读数彻底失去意义（2026-10-24 本文件第一版实测：三条日志证明
// 闸裁定、可卖量、钳位量全对，仍报 FAIL 且没有任何失败消息，正是 TempDir 清理失败那种形态）。
// 这里改用 os.MkdirTemp + 尽力删除：删不掉只是留垃圾，**不冒充产品缺陷**。
// English: t.TempDir() marks the test failed when RemoveAll is denied; this sandbox denies every
// file-delete in some sessions (the system sqlite3 CLI fails too), which produced "all assertions
// pass but the test is red with no message". Best-effort removal keeps the signal honest.
func t1DB(t *testing.T) *store.DB {
	t.Helper()
	dir, err := os.MkdirTemp("", "risk-t1-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	db, err := store.Open(filepath.Join(dir, "g.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// t1Seed 在临时账本里种「昨日买入 baseQty 股 + 今日买入 todayQty 股」的持仓。
// 关键：**可卖量 = baseQty**（当日买入被 T+1 锁住），所以超量用例必须让 baseQty < 请求量，
// 否则测的是"没有超量"这条平凡路径（本文件第一版就是这么写错的：种 1000+600 得到可卖 1000，
// 请求 1000 恰好不超，于是"拒单档"恒绿——等值锁写错方向就是恒盲）。
// 日期全部从 cntime 当日派生（与 Gate.today 同源），不写死，避免跨零点翻红。
// English: seeds baseQty (yesterday) + todayQty (today); sellable == baseQty by T+1.
func t1Seed(t *testing.T, db *store.DB, userID string, baseQty, todayQty int) {
	t.Helper()
	now := time.Now()
	yesterday := cntime.In(now).AddDate(0, 0, -1).Format("2006-01-02") + " 14:00:00"
	today := cntime.In(now).Format("2006-01-02") + " 09:35:00"
	if baseQty > 0 {
		if err := db.ApplyRealFill(store.RealFill{
			OrderID: "O-Y", Code: "600000.SH", Side: "买入", Price: 10, Qty: baseQty,
			Amount: 10 * float64(baseQty), TradedAt: yesterday, SignalID: "SIG-Y", UserID: userID,
		}); err != nil {
			t.Fatalf("种昨日底仓: %v", err)
		}
	}
	if todayQty > 0 {
		if err := db.ApplyRealFill(store.RealFill{
			OrderID: "O-T", Code: "600000.SH", Side: "买入", Price: 11, Qty: todayQty,
			Amount: 11 * float64(todayQty), TradedAt: today, SignalID: "SIG-T", UserID: userID,
		}); err != nil {
			t.Fatalf("种当日买入: %v", err)
		}
	}
}

// t1SellOrder 构造一张卖出 1000 股的委托视图（价格/新鲜度按既有夹具口径，避免其它闸干扰）。
// English: a 1000-share sell order view with the other gates neutralised.
func t1SellOrder() LiveOrder {
	o := liveOrder(SideSell)
	o.Qty = 1000
	o.Amount = 10000
	return o
}

// TestT1ClampRejectModeIsDefault 拒单档（未配置 `t1_clamp_qty`）：可卖 400 < 请求 1000 ⇒ 整单拒，
// 且**不得**带回钳位量（带回就等于悄悄换了语义）。
// English: default mode rejects the oversized sell and carries no clamp qty.
func TestT1ClampRejectModeIsDefault(t *testing.T) {
	db := t1DB(t)
	t1Seed(t, db, "u_t1", 400, 600) // 持仓 1000、可卖 400
	g := NewGate(db, "u_t1", nil)

	cfg := qmtCfg()
	if cfg.T1ClampEnabled() {
		t.Fatalf("默认配置不得开启减量档（nil 必须读成 false）")
	}
	// 读数自证：种下去的持仓与当日买入必须真的被账本读到（否则这条用例证的是"没有持仓时放行"，
	// 与 T+1 无关）。等值锁放在断言前，读数不对时先红在夹具而不是红在产品。
	p, perr := db.RealPositionByCodeForUser("u_t1", "600000.SH")
	t.Logf("夹具读数: 持仓=%d err=%v 今日买入=%d 今日=%s", p.Qty, perr, db.TodayBoughtQty("u_t1", "600000.SH", g.today()), g.today())
	if p.Qty != 1000 {
		t.Fatalf("夹具应种出 1000 股持仓, got %d (err=%v)", p.Qty, perr)
	}
	if sellable := db.BuyableQtyForUserSell("u_t1", "600000.SH", g.today()); sellable != 400 {
		t.Fatalf("夹具可卖量应等值 400（持仓 1000 − 今日买入 600）, got %d", sellable)
	}
	v := g.CheckLiveOrder(cfg, t1SellOrder())
	if v.Pass {
		t.Fatalf("拒单档应拦下超量卖单, got %+v", v)
	}
	if v.Gate != "t1_sellable" {
		t.Fatalf("应命中 t1_sellable, got %q", v.Gate)
	}
	if !strings.Contains(v.Reason, "T+1 不可卖") {
		t.Fatalf("拒单原因应点名 T+1 不可卖, got %q", v.Reason)
	}
	if v.ClampQty != 0 {
		t.Fatalf("拒单档不得携带钳位量, got %d", v.ClampQty)
	}
}

// TestT1ClampReducedModeClampsQty 减量档（显式 `t1_clamp_qty=true`）：同一张超量卖单改为放行，
// 钳位量必须**等值**于可卖量（400，不是 ≤400——等值锁才能抓到"钳少了"这种少卖缺陷）。
// English: explicit clamping mode passes the same order with ClampQty exactly equal to sellable.
func TestT1ClampReducedModeClampsQty(t *testing.T) {
	db := t1DB(t)
	t1Seed(t, db, "u_t1c", 400, 600) // 持仓 1000、可卖 400
	g := NewGate(db, "u_t1c", nil)

	cfg := qmtCfg()
	on := true
	cfg.T1ClampQty = &on
	if !cfg.T1ClampEnabled() {
		t.Fatalf("显式 true 必须开启减量档")
	}
	v := g.CheckLiveOrder(cfg, t1SellOrder())
	if !v.Pass {
		t.Fatalf("减量档应放行超量卖单, got %+v", v)
	}
	if v.ClampQty != 400 {
		t.Fatalf("钳位量应等值 400（持仓 1600 − 当日买入 600）, got %d", v.ClampQty)
	}
	if !strings.Contains(v.Reason, "减量放行") {
		t.Fatalf("放行原因应点名减量放行（否则日志里看不出这一单被砍过）, got %q", v.Reason)
	}
}

// TestT1ClampZeroSellableStillRejects 可卖量 0（当日买入把可卖量吃光）：**两档都必须拒**。
// 钳到 0 股＝放行一张零股委托，柜台必打回，还会烧掉一次委托额度与幂等键。
// English: with zero sellable, both modes must reject — clamping to 0 shares is not a pass.
func TestT1ClampZeroSellableStillRejects(t *testing.T) {
	for _, mode := range []string{"拒单档", "减量档"} {
		t.Run(mode, func(t *testing.T) {
			db := t1DB(t)
			t1Seed(t, db, "u_t1z", 0, 1000) // 全部当日买入、持仓 1000、可卖 0
			g := NewGate(db, "u_t1z", nil)

			cfg := qmtCfg()
			if mode == "减量档" {
				on := true
				cfg.T1ClampQty = &on
			}
			v := g.CheckLiveOrder(cfg, t1SellOrder())
			if v.Pass {
				t.Fatalf("%s 下可卖量为 0 也必须拒单, got %+v", mode, v)
			}
			if v.ClampQty != 0 {
				t.Fatalf("%s 下不得带回钳位量（0 股不是放行）, got %d", mode, v.ClampQty)
			}
		})
	}
}

// TestT1ClampRequiresEnforceT1 总闸关掉（`enforce_t1=false`）时整道闸弃权：既不拒也不钳位，
// 交柜台终裁。这条防的是"减量档绕过总闸"——两个开关是包含关系不是并列关系。
// English: with the master switch off the gate abstains entirely (no reject, no clamp).
func TestT1ClampRequiresEnforceT1(t *testing.T) {
	db := t1DB(t)
	t1Seed(t, db, "u_t1o", 400, 600)
	g := NewGate(db, "u_t1o", nil)

	cfg := qmtCfg()
	off, on := false, true
	cfg.EnforceT1 = &off
	cfg.T1ClampQty = &on
	v := g.CheckLiveOrder(cfg, t1SellOrder())
	if !v.Pass || v.ClampQty != 0 {
		t.Fatalf("总闸关闭时应弃权（放行且不钳位）, got %+v", v)
	}
}

// TestT1ClampWithinSellableNotClamped 请求量本来就在可卖量之内：两档都不该钳位
// （钳位只在超量时发生；无差别钳位会把正常单也改小，是另一种资损方向）。
// English: an order within the sellable qty is never clamped in either mode.
func TestT1ClampWithinSellableNotClamped(t *testing.T) {
	db := t1DB(t)
	t1Seed(t, db, "u_t1n", 400, 600) // 持仓 1000、可卖 400
	g := NewGate(db, "u_t1n", nil)

	cfg := qmtCfg()
	on := true
	cfg.T1ClampQty = &on
	o := t1SellOrder()
	o.Qty = 400
	o.Amount = 4000
	v := g.CheckLiveOrder(cfg, o)
	if !v.Pass || v.ClampQty != 0 {
		t.Fatalf("不超量的卖单不得被钳位, got %+v", v)
	}
}
