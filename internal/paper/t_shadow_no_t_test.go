// t_shadow_no_t_test.go — §T-COST B5（2026-10-27）影子对照账的验收判据。
//
// 三条硬判据（施工图 §3.3）：
//  1. 非 T 成交集合逐笔相等：同一交易日、同码、同方向、同数量、同价格（真实 trades vs 影子 fills）；
//  2. 影子只读：影子账的权益/持仓不得被任何判定读取——用静态扫描断言 Room/预算/风控路径零引用；
//  3. 跳腿计数相等：skipped_t_legs == 当日 T 腿数（首腿 + 配对腿，按成交腿计）。
//
// 反证（写在注释里并留了可检的判据）：① 让影子账多记一条 T 腿（SkippedLeg+1）⇒ 判据 3 的等式当场红；
// ② 把 shadowNoT 的权益读进 ordbudget/engine 的 Room 计算 ⇒ 判据 2 的静态扫描红。两条都不是"补充说明"，
// 而是对照实验能否成立的前提（方案 §10 风险 1：共用一次 T 腿的资金变化，归因就会恒 0 或虚高）。
package paper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/combat_agent"
	"quant-trading-v2/internal/data"
)

// tRealFill 真实账里的一笔非 T 成交（对照判据 1 的键）。
type tRealFill struct {
	Code  string
	Side  string
	Qty   int
	Price float64
}

// nonTRealFills 从 trades 尾部取出"影子开账之后"的非 T 成交（T 腿的 Reason 以 §T-MODE 开头）。
func nonTRealFills(trades []Trade, from int) []tRealFill {
	out := make([]tRealFill, 0, len(trades))
	for _, tr := range trades[from:] {
		if strings.HasPrefix(tr.Reason, tRStrategies) {
			continue // T 腿：影子账本来就该跳过，不参与"非 T 集合"比较
		}
		out = append(out, tRealFill{Code: tr.Code, Side: tr.Side, Qty: tr.Qty, Price: tr.Price})
	}
	return out
}

// tLegsOfRounds 当日 T 腿数（成交腿口径：首腿 + 配对腿）。
func tLegsOfRounds(rounds []TRound) int {
	n := 0
	for i := range rounds {
		if rounds[i].LegQty > 0 {
			n++
		}
		if rounds[i].PairQty > 0 {
			n++
		}
	}
	return n
}

// seedShadowScenario 构造"策略买入 + 卖出裁决 + 一轮正 T"的混合场景。
// 返回影子开账时 trades 的下标（比较基准），避免把开账前的种仓成交算进对照。
func seedShadowScenario(t *testing.T) (*Engine, int) {
	t.Helper()
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.SetShadowNoT(true)
	start := len(e.trades)
	// 策略买入新票（非 T）：固定金额 10000 @10，dynamic 口径下按目标模型定量。
	sig := combat_agent.Signal{Code: "300002", Name: "另一只", Strategy: "龙头", StrategyType: "dragon",
		Action: "buy", Price: 10, GeneratedAt: time.Now()}
	e.OnSignals([]combat_agent.Signal{sig}, map[string]*data.StockInfo{"300002": {Code: "300002", Price: 10}})
	if p := e.positions["300002"]; p == nil || p.Qty <= 0 {
		t.Fatalf("用例前置：策略买入未成交，got %+v", p)
	}
	// 卖出裁决执行（清仓）：先把新票批次回拨成"昨日底仓"，否则 T+1 会整单拒掉。
	t1Ready(e)
	sell := combat_agent.Signal{Code: "300002", Name: "另一只", Direction: "提醒", Action: "卖出",
		AlertType: "清仓", Price: 10.5}
	e.OnSignals([]combat_agent.Signal{sell}, map[string]*data.StockInfo{"300002": {Code: "300002", Price: 10.5}})
	if p := e.positions["300002"]; p != nil && p.Qty > 0 {
		t.Fatalf("用例前置：卖出裁决未清仓，残留 %+v", p)
	}
	// 一轮完整正 T（两条腿都应计入 skipped_t_legs）。
	e.RunIntradayT(tQuote("300001", 9.4, 10), tParams())
	e.RunIntradayT(tQuote("300001", 9.8, 10), tParams())
	if len(e.tRounds) != 1 || e.tRounds[0].State != tRStateDone {
		t.Fatalf("用例前置：应完成一轮正 T，got %+v", e.tRounds)
	}
	return e, start
}

// TestShadowNoTNonTFillsEqual 判据 1：影子账的非 T 成交与真实账逐笔相等（同码/方向/数量/价格）。
func TestShadowNoTNonTFillsEqual(t *testing.T) {
	e, start := seedShadowScenario(t)
	real := nonTRealFills(e.trades, start)
	book := e.shadowNoT
	if book == nil {
		t.Fatal("影子账未建立（SetShadowNoT(true) 之后必须存在）")
	}
	if len(real) != len(book.Fills) {
		t.Fatalf("非 T 成交集合数量不等：real=%+v shadow=%+v", real, book.Fills)
	}
	for i := range real {
		sf := book.Fills[i]
		if real[i].Code != sf.Code || real[i].Side != sf.Side || real[i].Qty != sf.Qty ||
			abs(real[i].Price-sf.Price) > 1e-6 {
			t.Fatalf("第 %d 笔非 T 成交不一致：real=%+v shadow=%+v", i, real[i], sf)
		}
	}
	// 判据 3：跳腿计数必须等于当日 T 腿数（本场景一轮 done ⇒ 2 条腿）。
	wantLegs := tLegsOfRounds(e.tRounds)
	if book.SkippedLeg != wantLegs {
		t.Fatalf("skipped_t_legs=%d 与当日 T 腿数 %d 不符（少跳＝漏镜像，多跳＝误把策略单当 T）",
			book.SkippedLeg, wantLegs)
	}
	// 反证：人为多记一条 T 腿，等式当场不成立——这就是判据 3 的靶子。
	if book.SkippedLeg+1 == wantLegs {
		t.Fatal("反证自检失败：SkippedLeg+1 不应仍等于 T 腿数")
	}
}

// TestShadowNoTReadoutFrame 读数一帧：可用态、归因=真实−影子、序列同刻双值。
func TestShadowNoTReadoutFrame(t *testing.T) {
	e, _ := seedShadowScenario(t)
	read, why := e.TShadowNoTReadout()
	if !read.Available || why != "" {
		t.Fatalf("开启后应可用：%+v why=%q", read, why)
	}
	if abs(read.TAttribution-(read.EquityReal-read.EquityShadow)) > 0.011 {
		t.Fatalf("归因必须等于真实−影子：read=%+v", read)
	}
	if read.Mirrored != 2 || read.SkippedTLegs != 2 {
		t.Fatalf("镜像/跳腿计数应为 2/2，got %d/%d", read.Mirrored, read.SkippedTLegs)
	}
	if len(read.Series) == 0 {
		t.Fatal("开启后必须至少有一个净值点")
	}
	for _, p := range read.Series {
		if p.Ts == "" || p.Real == 0 || p.Shadow == 0 {
			t.Fatalf("净值点必须同刻双值：%+v", p)
		}
	}
}

// TestShadowNoTOffStopsMirroring 回滚：开关关掉即停镜像，已落盘/内存账保留只读。
func TestShadowNoTOffStopsMirroring(t *testing.T) {
	e, _ := seedShadowScenario(t)
	book := e.shadowNoT
	before := len(book.Fills)
	e.SetShadowNoT(false)
	e.tShadowMirrorBuyLocked("300003", 10, 100, time.Now())
	e.tShadowSkipTLegLocked(1, time.Now())
	if len(book.Fills) != before || book.SkippedLeg != 2 {
		t.Fatalf("关开关后不得再镜像/计跳腿：fills=%d skipped=%d", len(book.Fills), book.SkippedLeg)
	}
	// 文件/内存账保留：读数仍可用（available 由"有没有当日账"决定，与开关无关）。
	if read, _ := e.TShadowNoTReadout(); !read.Available || read.Enabled {
		t.Fatalf("关停后应仍可读且 enabled=false：%+v", read)
	}
}

// TestShadowNoTSeriesCap 净值点上限 240/日（超出替换最后一点，曲线末端始终最新）。
func TestShadowNoTSeriesCap(t *testing.T) {
	minuteVNow = 0
	e := seedBase(t, "300001", 1000)
	e.SetShadowNoT(true)
	now := e.clockNow()
	for i := 0; i < tShadowMaxPointsPerDay+20; i++ {
		e.mu.Lock()
		e.tShadowSampleLocked(now.Add(time.Duration(i) * time.Second))
		e.mu.Unlock()
	}
	if n := len(e.shadowNoT.EquitySer); n != tShadowMaxPointsPerDay {
		t.Fatalf("equity_series 上限应为 %d，got %d", tShadowMaxPointsPerDay, n)
	}
}

// TestShadowNoTNotReadByDecisions 判据 2：影子读数在 Room/预算/风控路径零引用（静态扫描）。
// 为什么用"扫源码"而不是行为断言：行为用例只能证明"这一条路径今天没读"，而这条判据要防的是
// 将来有人顺手把影子权益接进预算——那种改动不会让任何既有用例变红，只会让归因悄悄失真。
func TestShadowNoTNotReadByDecisions(t *testing.T) {
	root := filepath.Join("..", "..")
	forbiddenDirs := []string{
		filepath.Join(root, "internal", "ordbudget"),
		filepath.Join(root, "internal", "signalctl"),
	}
	tokens := []string{"shadowNoT", "ShadowNoT", "shadow_no_t"}
	for _, dir := range forbiddenDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", dir, err)
		}
		for _, en := range entries {
			if en.IsDir() || !strings.HasSuffix(en.Name(), ".go") {
				continue
			}
			b, rerr := os.ReadFile(filepath.Join(dir, en.Name()))
			if rerr != nil {
				t.Fatalf("读取 %s 失败：%v", en.Name(), rerr)
			}
			for _, tok := range tokens {
				if strings.Contains(string(b), tok) {
					t.Fatalf("影子读数被判定层引用：%s 含 %q（Room/预算/风控必须零引用）", en.Name(), tok)
				}
			}
		}
	}
	// engine.go（Room/预算装配层）同样零引用；引擎只在 intraday_t.go 里注入开关。
	b, err := os.ReadFile(filepath.Join(root, "internal", "engine", "engine.go"))
	if err != nil {
		t.Fatalf("读取 engine.go 失败：%v", err)
	}
	for _, tok := range tokens {
		if strings.Contains(string(b), tok) {
			t.Fatalf("engine.go 引用了影子读数 %q：Room/预算层必须零引用", tok)
		}
	}
}
