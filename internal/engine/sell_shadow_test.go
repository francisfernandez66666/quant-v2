// sell_shadow_test.go — §SELLPOINT-UNIFY P1-b 影子接线的引擎侧行为锁。
//
// 钉死四件事：
//
//	① 默认（mode 缺省=shadow）每轮持仓探针会进 signalctl 留痕环（stage=sell_discipline），
//	   触线首轮只 hold 不处置；
//	② 状态跨轮存续（同状态不重复留痕防刷屏），平仓后 PruneSellStates 归零——重新入场重新计时；
//	③ mode=off 完全静默（不裁决、不留痕）；
//	④ §D1 护栏4 端到端：触线+利空 只有 bearTier=dual（同花顺∩东财双源验证）才当轮即时硬清；
//	   single/无记录/超龄一律只进观察窗预警，绝不给硬清处置。
//
// English: engine-side locks for the shadow wiring — shadow verdicts land in the audit ring,
// state survives rounds and is pruned when flat, and "off" stays completely silent.
package engine

import (
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/config"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/trading"
)

// shadowTestEnv 构造最小可跑影子的引擎（实盘控制器 Noop 执行器，不触任何外呼）。
func shadowTestEnv(t *testing.T, mode string) *Engine {
	t.Helper()
	realDB, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open live store: %v", err)
	}
	t.Cleanup(func() { realDB.Close() })
	cfg := config.DefaultQMTConfig()
	cfg.Enabled = true
	cfg.SellUnifiedMode = mode
	ctrl := trading.NewController(trading.NoopExecutor{}, realDB, "u_1", cfg, nil)
	e := &Engine{}
	e.SetQMT(ctrl, realDB)
	return e
}

// countSellVerdicts 留痕环中指定代码/裁定的卖出条目数。
func countSellVerdicts(e *Engine, code string, v signalctl.Verdict) int {
	n := 0
	for _, d := range e.SignalCtl().Recent(128) {
		if d.Stage == signalctl.StageSell && d.Code == code && d.Verdict == v {
			n++
		}
	}
	return n
}

// TestSellShadowRecordsHoldOnFirstTouch −7% 触止损线：影子只留 hold（进观察窗），无处置；
// 同状态重复探针不再留痕；平仓清理后重新入场会再次从零留痕。
func TestSellShadowRecordsHoldOnFirstTouch(t *testing.T) {
	e := shadowTestEnv(t, "") // 缺省 = shadow
	positions := []store.RealPosition{{TsCode: "600580.SH", Name: "影子测试", Qty: 100, CostPrice: 10}}
	quotes := map[string]*data.StockInfo{"600580": {Price: 9.3}}

	e.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, nil)
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictHold); n != 1 {
		t.Fatalf("首触应留痕 1 条 hold，得 %d", n)
	}
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictPass); n != 0 {
		t.Fatalf("影子观察窗内不得有处置留痕，得 %d", n)
	}
	// 第二轮同状态：不重复留痕（5s 探针防刷屏）。
	e.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, nil)
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictHold); n != 1 {
		t.Fatalf("同状态重复探针不应再留痕，得 %d", n)
	}
	// 平仓（持仓清空）→ 状态被 Prune；重新入场按新首触再留一条 hold。
	e.runSellUnifiedJudge("u_1", nil, nil, quotes, nil, nil, nil)
	e.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, nil)
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictHold); n != 2 {
		t.Fatalf("平仓清理+重新入场应重新计时留痕（累计 2 条 hold），得 %d", n)
	}
}

// TestSellShadowOffIsSilent mode=off：不裁决不留痕。
func TestSellShadowOffIsSilent(t *testing.T) {
	e := shadowTestEnv(t, "off")
	positions := []store.RealPosition{{TsCode: "600580.SH", Name: "影子测试", Qty: 100, CostPrice: 10}}
	quotes := map[string]*data.StockInfo{"600580": {Price: 9.3}}
	e.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, nil)
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictHold) + countSellVerdicts(e, "600580.SH", signalctl.VerdictPass); n != 0 {
		t.Fatalf("off 模式必须完全静默，得 %d 条留痕", n)
	}
}

// TestSellShadowIgnoresUnheldAndInvalidPrice 无效价（行情缺失）不下结论、不留痕。
func TestSellShadowIgnoresUnheldAndInvalidPrice(t *testing.T) {
	e := shadowTestEnv(t, "shadow")
	positions := []store.RealPosition{{TsCode: "600580.SH", Name: "影子测试", Qty: 100, CostPrice: 10}}
	e.runSellUnifiedJudge("u_1", positions, nil, map[string]*data.StockInfo{}, nil, nil, nil)
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictHold) + countSellVerdicts(e, "600580.SH", signalctl.VerdictPass); n != 0 {
		t.Fatalf("无现价轮次不得有任何留痕，得 %d", n)
	}
}

// TestSellShadowDualBearHardClearsOnTouch §D1 护栏4 端到端正锁：触止损线 + bearTier=dual
// （同花顺∩东财成分名单双源验证）→ 当轮即时硬清留处置（pass），不等观察窗。
func TestSellShadowDualBearHardClearsOnTouch(t *testing.T) {
	e := shadowTestEnv(t, "shadow")
	e.bearTier = map[string]bearTierEntry{"600580": {verified: signalctl.BearVerifiedDual, at: time.Now()}}
	positions := []store.RealPosition{{TsCode: "600580.SH", Name: "影子测试", Qty: 100, CostPrice: 10}}
	quotes := map[string]*data.StockInfo{"600580": {Price: 9.3}} // −7% 触 6% 止损线（未深破）
	e.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, map[string]string{"600580": "贵金属板块利空"})
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictPass); n != 1 {
		t.Fatalf("触线+双源验证利空应当轮留 1 条处置（即时硬清），得 %d", n)
	}
	if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictHold); n != 0 {
		t.Fatalf("即时硬清不应再走观察窗 hold，得 %d", n)
	}
}

// TestSellShadowSingleBearNeverHardClears §D1 护栏4/5 负锁：触线+利空但仅单源（tier=single）
// 或双源结论已超龄（TTL 外）→ 只留观察窗 hold 预警，绝不给硬清处置。
func TestSellShadowSingleBearNeverHardClears(t *testing.T) {
	positions := []store.RealPosition{{TsCode: "600580.SH", Name: "影子测试", Qty: 100, CostPrice: 10}}
	quotes := map[string]*data.StockInfo{"600580": {Price: 9.3}}
	bear := map[string]string{"600580": "贵金属板块利空"}

	cases := map[string]*Engine{
		"single级": shadowTestEnv(t, "shadow"),
		"超龄dual":  shadowTestEnv(t, "shadow"),
	}
	cases["single级"].bearTier = map[string]bearTierEntry{"600580": {verified: signalctl.BearVerifiedSingle, at: time.Now()}}
	cases["超龄dual"].bearTier = map[string]bearTierEntry{"600580": {verified: signalctl.BearVerifiedDual, at: time.Now().Add(-bearTierTTL - time.Minute)}}
	for name, e := range cases {
		e.runSellUnifiedJudge("u_1", positions, nil, quotes, nil, nil, bear)
		if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictPass); n != 0 {
			t.Fatalf("%s：非新鲜双源利空不得给硬清处置，得 %d", name, n)
		}
		if n := countSellVerdicts(e, "600580.SH", signalctl.VerdictHold); n != 1 {
			t.Fatalf("%s：触线应留 1 条观察窗 hold（预警口径），得 %d", name, n)
		}
	}
}
