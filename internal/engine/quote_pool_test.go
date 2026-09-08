package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/paper"
)

// TestSyncMonitorBasePinsHeldAndPaperCodes 验证监控池 base（持仓池）重建覆盖 自选∪实盘持仓∪
// 全账号纸面持仓：纸面持仓代码永续入池（Monitoring=true），且与 hot 监控池无关——
// 持仓/自选从此恒有 5s 实时行情（估值/实盘建议/自动卖出不再因掉出 hot 而缺行情）。
func TestSyncMonitorBasePinsHeldAndPaperCodes(t *testing.T) {
	f := data.NewFetcher(nil, nil, nil)
	e := &Engine{fetcher: f}
	e.SetPaperHeldCodesFn(func() []string { return []string{"600096.SH", "002815.SZ"} })

	e.syncMonitorBase()
	for _, code := range []string{"600096.SH", "002815.SZ"} {
		if !f.Monitoring(code) {
			t.Fatalf("纸面持仓 %s 应已钉入 base（Monitoring=true）", code)
		}
	}

	// 低配回退分支：paperHeldCodesFn 为 nil 时回退全局 e.paper 账本（空账本不应 panic），
	// 且自选/实盘持仓 nil 依赖（rpt/wlMgr）时 syncMonitorBase 应安全跳过对应并入。
	e.SetPaperHeldCodesFn(nil)
	e.paper = paper.New(paper.Config{}, filepath.Join(t.TempDir(), "paper.json"))
	e.syncMonitorBase() // 不应 panic
	if got := len(f.HotStocks()); got != 0 {
		t.Fatalf("hot 池不应被 syncMonitorBase 改动, got %d", got)
	}
}

// TestEnsureBuyQuotesMergesSnapshotSameRound 验证缺行情买入信号在本轮撮合即有实时价：
// 代码已在监控（base 内）+ 快照有价 → ensureBuyQuotes 把快照价合并进本轮 quotes，
// 撮合不因"快照池没有它"而"行情缺失跳过"（模拟盘/实盘共用 buys，一处修两处受益）。
// 测试不依赖网络：快照经 LoadPersistedSnapshot 从落盘文件恢复。
func TestEnsureBuyQuotesMergesSnapshotSameRound(t *testing.T) {
	f := data.NewFetcher(nil, nil, nil)
	dir := t.TempDir()
	seed := data.MarketSnapshot{
		Time:   time.Now(),
		Source: "test",
		Stocks: map[string]*data.StockInfo{
			"002815": {Code: "002815", Name: "崇达技术", Price: 17.59},
		},
	}
	raw, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot_latest.json"), raw, 0644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	f.SetDataDir(dir)
	f.LoadPersistedSnapshot(dir)
	if si := f.SnapshotQuote("002815"); si == nil || si.Price != 17.59 {
		t.Fatalf("快照恢复失败: %+v", si)
	}
	f.SetBaseStocks([]string{"002815"}) // 持仓池语义：已监控

	e := &Engine{fetcher: f}
	quotes := make(map[string]*data.StockInfo)
	e.ensureBuyQuotes([]combat_agent.Signal{{Code: "002815", Name: "崇达技术"}}, quotes)
	q := quotes["002815"]
	if q == nil || q.Price != 17.59 {
		t.Fatalf("本轮 quotes 应合并进 002815 实时价 17.59, got %+v", q)
	}
	// 已有报价的代码不应被重复覆盖（保持原值）
	quotes2 := map[string]*data.StockInfo{"002815": {Code: "002815", Price: 18.0}}
	e.ensureBuyQuotes([]combat_agent.Signal{{Code: "002815"}}, quotes2)
	if got := quotes2["002815"].Price; got != 18.0 {
		t.Fatalf("已报价代码不应被覆盖, got %v", got)
	}
}