// w1a_approx_pnl_failclose_test.go — §0926E2E-W1A 行为用例：
// 近似资金闸的"已实现盈亏"读取出错时**必须拒单**（旧缺陷＝错误被吞成 0，当日亏损被抹掉后
// 可用额度反向放大——数据缺口变放水口）。三态：报错→拒；正常亏损值→照常参与计算；
// 正常 0 值→放行（对照组，证明拒单只由"不可得"触发，不是把闸整体焊死）。
// English: §0926E2E-W1A behavior tests — the approximate capital gate must reject a buy when the
// realized-PnL read fails (old bug: error swallowed to 0, silently inflating available buying power
// on a loss day). Error→block; real loss→still counted; clean zero→pass (control).
package risk

import (
	"errors"
	"strings"
	"testing"
)

// TestW1aApproxPnlUnreadableRejectsBuy 盈亏查询报错 → 近似闸 fail-close 拒买，且拒绝原因点名。
func TestW1aApproxPnlUnreadableRejectsBuy(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	cfg := qmtCfg()
	cfg.InitialCapital = 10000 // 无券商账户快照 → 走近似口径
	g.realizedPnlFn = func(userID, day string) (float64, error) {
		return 0, errors.New("simulated fills read failure")
	}
	v := g.CheckLiveOrder(cfg, liveOrder(SideBuy)) // 金额 1000 < 本金，旧缺陷下会放行
	if v.Pass {
		t.Fatalf("盈亏不可得时近似闸必须拒买（旧缺陷=吞错按0放行）, got %+v", v)
	}
	if !strings.Contains(v.Reason, "已实现盈亏不可得") {
		t.Fatalf("拒绝原因须点名盈亏不可得（可归因），got %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "simulated fills read failure") {
		t.Fatalf("拒绝原因须带底层错误串（留痕取证），got %q", v.Reason)
	}
}

// TestW1aRealLossStillCounted 对照组1：盈亏**查询成功且为亏损** → 照常从可用额度里扣（拒 9000）。
func TestW1aRealLossStillCounted(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	cfg := qmtCfg()
	cfg.InitialCapital = 10000
	g.realizedPnlFn = func(userID, day string) (float64, error) { return -6000, nil }
	o := liveOrder(SideBuy)
	o.Amount = 9000
	v := g.CheckLiveOrder(cfg, o)
	if v.Pass {
		t.Fatalf("亏损 6000 后预估可用仅 4000，9000 单必须拒, got %+v", v)
	}
	if !strings.Contains(v.Reason, "可用资金不足") {
		t.Fatalf("应走正常额度不足口径, got %q", v.Reason)
	}
}

// TestW1aCleanZeroPasses 对照组2：盈亏查询成功且为 0 → 该闸放行（证明"拒单"只由不可得触发）。
func TestW1aCleanZeroPasses(t *testing.T) {
	g := NewGate(gateDB(t), "u_g", nil)
	cfg := qmtCfg()
	cfg.InitialCapital = 10000
	g.realizedPnlFn = func(userID, day string) (float64, error) { return 0, nil }
	o := liveOrder(SideBuy)
	o.Amount = 9000
	if v := g.CheckLiveOrder(cfg, o); !v.Pass {
		t.Fatalf("盈亏正常为 0 时 9000/10000 应放行, got %+v", v)
	}
}
