// sell_anchor_test.go — §M10（2026-09-22 修复批）paper 移动止盈锚点跨重启持久化的反例锁。
//
// 缺陷原型（docs/AUDIT_FULL_UAT_20260921C.md M10 残留）：裁决内核的移动止盈锚点
// （持仓期最高价）在 live=real_positions.highest_price、report=ExecLog.HighestPrice 均有账本，
// 唯独 paper 账本无字段——内核靠 sellState.HighPrice 纯内存「每轮自抬」。进程重启后
// 状态机清零、锚点回落到成本价，「先涨够再回撤 ≥MaxPullbackPct」的移动止盈条件在重启后
// 永不满足（涨 15% 的持仓重启后回撤 20% 也不触发止盈）。
//
// 本锁钉死三件事：
//
//	① 抬锚落盘：轮中有效现价抬高锚点并原子写文件；
//	② 重启恢复：全新引擎（内存状态全零）从同一文件恢复锚点，回撤轮按旧高点锁定
//	   移动止盈线（Line=SellLineTrail）——对照不持久化引擎同轮锁不了线（缺陷反例）；
//	③ 平仓清理：持仓退出后锚点即删（重新入场从零起算），文件同步、重启后不再复活。
//
// English: locks cross-restart persistence of the paper trailing-stop high anchor — a fresh
// Engine sharing the data dir restores the anchor and re-arms the trailing line from the old
// high, which the memory-only kernel could never do after a restart.
package engine

import (
	"os"
	"path/filepath"
	"testing"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/signalctl"
)

// TestPaperSellAnchorSurvivesRestart §M10 主锁：抬锚→重启→从旧锚触发移动止盈→平仓删锚。
func TestPaperSellAnchorSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper_sell_anchors.json")

	// —— 第一轮（进程 A）：涨到 11（+10%，未触任何线）→ 锚点抬到 11 并落盘 ——
	e1 := &Engine{}
	e1.paperAnchorPath = path
	pe := sellTestPaper(t) // 300001 持仓 1000 股 @10（当日买入，T+1 锁定）
	feedUp := sellJudgeFeed{SnapQuotes: map[string]*data.StockInfo{"300001": {Price: 11}}}
	vs := e1.runPaperUnifiedJudge("u_1", pe, feedUp, e1.paperSignalPolicy("u_1"), "shadow")
	if len(vs) != 1 {
		t.Fatalf("第一轮应有 1 条有效裁决，得 %+v", vs)
	}
	if vs[0].Verdict.Line != signalctl.SellLineNone {
		t.Fatalf("+10%% 未回撤不应触线，得 %v", vs[0].Verdict.Line)
	}
	if got := e1.paperSellAnchor("u_1", "300001"); got != 11 {
		t.Fatalf("第一轮后锚点应抬到 11，得 %.2f", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("锚点文件应已原子落盘: %v", err)
	}

	// —— 模拟进程重启：全新 Engine + 全新 Controller（内存状态全零），只共享同一数据目录 ——
	e2 := &Engine{}
	e2.paperAnchorPath = path
	if got := e2.paperSellAnchor("u_1", "300001"); got != 11 {
		t.Fatalf("重启后锚点不得清零（应恢复 11，缺陷原型即回落成本价致移动止盈永不触发），得 %.2f", got)
	}
	// 回撤轮：现价 10.2，自高点 11 回撤 7.27% ≥ 默认 MaxPullbackPct 6% → 锁定移动止盈线。
	feedBack := sellJudgeFeed{SnapQuotes: map[string]*data.StockInfo{"300001": {Price: 10.2}}}
	vs2 := e2.runPaperUnifiedJudge("u_1", pe, feedBack, e2.paperSignalPolicy("u_1"), "shadow")
	if len(vs2) != 1 {
		t.Fatalf("回撤轮应有 1 条有效裁决，得 %+v", vs2)
	}
	if vs2[0].Verdict.Line != signalctl.SellLineTrail {
		t.Fatalf("锚点恢复后回撤轮必须重新锁上移动止盈线，得 Line=%v", vs2[0].Verdict.Line)
	}

	// —— 缺陷反例对照：不接持久化（路径为空的旧语义）的引擎跑同一回撤轮 → 锚点=成本、锁不了线 ——
	e3 := &Engine{}
	vs3 := e3.runPaperUnifiedJudge("u_1", pe, feedBack, e3.paperSignalPolicy("u_1"), "shadow")
	if len(vs3) != 1 || vs3[0].Verdict.Line != signalctl.SellLineNone {
		t.Fatalf("无持久化时回撤轮应无线（正是 M10 缺陷形态，用于对照），得 %+v", vs3)
	}

	// —— 平仓清理：代码退出持仓集 → 锚点即删（重新入场从零起算），文件同步 ——
	e2.syncPaperSellAnchors("u_1", map[string]bool{}, nil)
	if got := e2.paperSellAnchor("u_1", "300001"); got != 0 {
		t.Fatalf("平仓后锚点应删除，得 %.2f", got)
	}
	e4 := &Engine{}
	e4.paperAnchorPath = path
	if got := e4.paperSellAnchor("u_1", "300001"); got != 0 {
		t.Fatalf("平仓后重启不得复活旧锚点，得 %.2f", got)
	}
}

// TestPaperSellAnchorAccountIsolation 锚点按账号隔离：u_1 的锚点不会被 u_2 读到
// （多账号共享引擎场景，注册表按账号逐个裁决）。
func TestPaperSellAnchorAccountIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper_sell_anchors.json")
	e := &Engine{}
	e.paperAnchorPath = path
	e.syncPaperSellAnchors("u_1", map[string]bool{"300001": true},
		[]sellRoundVerdict{{TsCode: "300001", Price: 12}})
	if got := e.paperSellAnchor("u_1", "300001"); got != 12 {
		t.Fatalf("u_1 锚点应为 12，得 %.2f", got)
	}
	if got := e.paperSellAnchor("u_2", "300001"); got != 0 {
		t.Fatalf("u_2 不得读到 u_1 的锚点，得 %.2f", got)
	}
}
