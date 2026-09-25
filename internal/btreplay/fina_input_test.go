// 本文件锁 §B2（owner 裁决 2026-09-26「因子回放要不要喂财务数据：要」）的三层防线：
//  1. 因子规则确实吃到"判定日可见"的财报——分数随可见期变化，数字可等值复算；
//  2. PIT 闸真的在挡东西——披露日未到的期绝不出现在分数里（用了就是未来函数）；
//  3. 装配点真的在装配——collect 主循环/兜底兄弟都要注入（B6 跨股串台的教训在财务腿上不重演）。
//
// English: locks the §B2 financial input — factor rules receive the judgment-day-visible report
// (scores recomputable exactly), future-dated reports are provably filtered out, and the assembly
// points (collect loop plus fallback peers) actually inject the source per stock.
package btreplay

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quant-trading-v2/internal/data"
	"quant-trading-v2/internal/store"
	factor "quant-trading-v2/internal/strategies/factor" // 与形态/因子库共名，显式别名防读侧混淆
	"quant-trading-v2/internal/strategy"
)

// finaInputDB 最小研究库：
//   - 600000.SH 两期财报——Q1（披露 20210430，ROE=5）与 H1（披露 20210930，ROE=95）；
//     判定日夹在两个披露日之间时只准吃 ROE=5，这就是"挡未来"的等值证据；
//   - 600002.SH 只有一期 2018 年的旧财报——任何 2021 年判定日都应按 §M-7 过旧停用；
//   - 600003.SH 真缺失（库里没有它的财报）。
func finaInputDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "fina_in.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rows := []map[string]any{
		{"ts_code": "600000.SH", "end_date": "20210331", "ann_date": "20210430", "roe": 5.0},
		{"ts_code": "600000.SH", "end_date": "20210630", "ann_date": "20210930", "roe": 95.0},
		{"ts_code": "600002.SH", "end_date": "20171231", "ann_date": "20180131", "roe": 95.0},
	}
	if _, err := db.InsertRows("fina_indicator", store.TableColumns("fina_indicator"), rows); err != nil {
		t.Fatalf("seed fina: %v", err)
	}
	return db
}

// finKLines 造 n 根平价日K（收盘价恒 10），最后一根落在 endYYYYMMDD。
// 价量对断言刻意做成"无信息"：ROE 单因子规则触发与否只能由财务腿决定。
func finKLines(t *testing.T, endYYYYMMDD string, n int) []data.KLine {
	t.Helper()
	end, err := time.Parse("20060102", endYYYYMMDD)
	if err != nil {
		t.Fatalf("日期解析: %v", err)
	}
	out := make([]data.KLine, n)
	for i := range out {
		d := end.AddDate(0, 0, -(n - 1 - i))
		out[i] = data.KLine{Date: d, Open: 10, High: 10, Low: 10, Close: 10, Volume: 1e6, Amount: 1e7}
	}
	return out
}

// roeRuleAdapter 单因子 ROE、阈值 60 的因子规则适配器：
// finaScore(ROE)=clamp01(roe/20)，复合分=(pct+1)/2*100 → ROE=5 得 62.5、ROE=95 得 100、
// 无财务腿则"无可用因子"0 分——分数是纯算术，可等值钉死。
func roeRuleAdapter() *ruleEvalAdapter {
	f := factor.New()
	f.SetRules([]*factor.ActiveRule{{
		ID: "fac_901", Name: "财务腿锁",
		Rule: factor.Rule{
			Factors:      []string{"ROE"},
			Weights:      map[string]float64{"ROE": 1},
			Directions:   map[string]int{"ROE": 1},
			BuyThreshold: 60,
		},
	}})
	return &ruleEvalAdapter{name: "财务腿锁", ruleID: "fac_901", fs: f}
}

// TestFinaInputTriggerFeedsAndBlocksFuture ①+②：判定日在两个披露日之间时，分数必须等于
// "只吃 Q1 期"的等值算术结果；披露日到后分数才升到 H1 期；披露日前压根不触发（无财务腿）。
// 对照：未装配财务输入的同一适配器在同一判定日永远不触发——证明触发确实来自 §B2，不是价量凑出来的。
func TestFinaInputTriggerFeedsAndBlocksFuture(t *testing.T) {
	db := finaInputDB(t)
	p := newFinaProvider(db)
	ad := roeRuleAdapter()
	ad.setFinaScope(p, "600000.SH")

	// 判定日 20210415：两期披露日均未到 → 财务腿按缺失 → 不触发
	if meta, ok := ad.Trigger(finKLines(t, "20210415", 40), 10, 0); ok {
		t.Fatalf("披露日前不得触发（未来函数防线），实得 %+v", meta)
	}
	// 判定日 20210615：只可见 Q1（ROE=5）→ 62.5。**等值断言同时钉住两件事**：
	// 分数非 0＝财务腿喂进去了；分数不是 100＝H1 期（披露日 0930）没被偷吃。
	meta, ok := ad.Trigger(finKLines(t, "20210615", 40), 10, 0)
	if !ok || meta["score"] != 62.5 {
		t.Fatalf("20210615 应触发且 score=62.5（ROE=5 可见、95 被挡），实得 ok=%v %+v", ok, meta)
	}
	// 判定日 20211215：H1 已披露 → 100（闸不能把票永久钉死在旧期上）
	meta2, ok2 := ad.Trigger(finKLines(t, "20211215", 40), 10, 0)
	if !ok2 || meta2["score"] != 100 {
		t.Fatalf("20211215 应 score=100（ROE=95 可见），实得 ok=%v %+v", ok2, meta2)
	}
	// 对照锁：没装配财务输入的同一规则、同一判定日，永不触发
	ad0 := roeRuleAdapter()
	if _, ok := ad0.Trigger(finKLines(t, "20210615", 40), 10, 0); ok {
		t.Fatal("未装配 §B2 时不该有任何触发——否则上面两条断言证明不了是财务腿的功劳")
	}
	// 读数：可见/挡过未来期都要点名
	s := p.String()
	if !strings.Contains(s, "吃到可见 1") || !strings.Contains(s, "挡过未来期的票 1") {
		t.Fatalf("§B2-FINA 读数应点名可见与挡未来，实得 %q", s)
	}
}

// TestFinaInputStaleAndMissing ②续：§M-7 停用闸与真缺失在回放侧都按缺失计入，读数分档点名成因。
func TestFinaInputStaleAndMissing(t *testing.T) {
	db := finaInputDB(t)
	p := newFinaProvider(db)
	// 600002.SH 只有 2018 年披露的一期：任何 2021 判定日都过旧 → 不触发、档=stStale
	ad := roeRuleAdapter()
	ad.setFinaScope(p, "600002.SH")
	if _, ok := ad.Trigger(finKLines(t, "20210615", 40), 10, 0); ok {
		t.Fatal("过旧财报必须停用（按缺失计入打分）")
	}
	if st := p.status["600002.SH"]; st != stStale {
		t.Fatalf("600002.SH 应归集为过旧停用档，实得 st=%d", st)
	}
	// 600003.SH 真缺失：不触发、档=stNoFina
	ad3 := roeRuleAdapter()
	ad3.setFinaScope(p, "600003.SH")
	if _, ok := ad3.Trigger(finKLines(t, "20210615", 40), 10, 0); ok {
		t.Fatal("没有财报的票不该触发财务因子规则")
	}
	if st := p.status["600003.SH"]; st != stNoFina {
		t.Fatalf("600003.SH 应归集为真缺失档，实得 st=%d", st)
	}
	// 粘滞锁：判定日先从披露日前开始、后到披露日后，读数必须升为"吃到可见"
	// （没有粘滞规则的话，第一次判定就把票永久记成缺失——读数谎报裸奔规模）
	p2 := newFinaProvider(db)
	ad4 := roeRuleAdapter()
	ad4.setFinaScope(p2, "600000.SH")
	ad4.Trigger(finKLines(t, "20210415", 40), 10, 0) // 首次=尚无可见期
	if st := p2.status["600000.SH"]; st != stMissing {
		t.Fatalf("首判应先归集为尚无可见期，实得 st=%d", st)
	}
	ad4.Trigger(finKLines(t, "20210615", 40), 10, 0) // 后来吃到 Q1
	if st := p2.status["600000.SH"]; st != stVisible {
		t.Fatalf("吃到过就必须粘滞为可见（读数不许谎报），实得 st=%d", st)
	}
	// 反向粘滞：已可见之后再来一个"尚无可见期"的判定日（判定日回拨），读数仍记可见
	ad4.Trigger(finKLines(t, "20210415", 40), 10, 0)
	if st := p2.status["600000.SH"]; st != stVisible {
		t.Fatalf("粘滞应双向稳定（后来的缺失不许把已可见的票打回裸奔档），实得 st=%d", st)
	}
}

// finaProbeAdapter 装配点探针：记录每次 setFinaScope 收到的来源与代码。
type finaProbeAdapter struct {
	name     string
	fallback bool
	gotP     []*finaProvider
	gotCode  []string
}

func (a *finaProbeAdapter) Name() string { return a.name }
func (a *finaProbeAdapter) Trigger([]data.KLine, float64, float64) (map[string]float64, bool) {
	return nil, false
}
func (a *finaProbeAdapter) Exit(*strategy.ExitContext, []strategy.KLine) (*strategy.ExitResult, bool) {
	return nil, false
}
func (a *finaProbeAdapter) FallbackTier() bool { return a.fallback }
func (a *finaProbeAdapter) setFinaScope(p *finaProvider, tsCode string) {
	a.gotP = append(a.gotP, p)
	a.gotCode = append(a.gotCode, tsCode)
}

// TestFinaScopeReachesFallbackPeers 兜底互斥回查走兄弟的裸 Trigger——兄弟没被注入就会拿
// 上一只票的财务态判今天的票（B6 串台的财务腿版本），applyFinaScope 必须连兄弟一起装。
func TestFinaScopeReachesFallbackPeers(t *testing.T) {
	p := newFinaProvider(finaInputDB(t))
	peer := &finaProbeAdapter{name: "兄弟"}
	primary := &finaProbeAdapter{name: "兜底档", fallback: true}
	o := &Options{finaSrc: p, fallbackPeers: []adapter{peer}}
	o.applyFinaScope(primary, "600000.SH")
	if len(primary.gotP) != 1 || primary.gotP[0] != p || primary.gotCode[0] != "600000.SH" {
		t.Fatalf("主适配器应收到来源与代码，实得 %+v / %+v", primary.gotP, primary.gotCode)
	}
	if len(peer.gotP) != 1 || peer.gotP[0] != p {
		t.Fatalf("兜底兄弟必须同批注入（B6 教训），实得 %+v", peer.gotP)
	}
	// 非兜底档主适配器：没有兄弟清单这道门，不该波及别人
	peer2 := &finaProbeAdapter{name: "无关"}
	o2 := &Options{finaSrc: p, fallbackPeers: []adapter{peer2}}
	o2.applyFinaScope(&finaProbeAdapter{name: "普通"}, "600001.SH")
	if len(peer2.gotP) != 0 {
		t.Fatal("非兜底档不得顺带注入兄弟清单")
	}
}

// TestFinaInputCollectWired 装配总锁：collect 主循环真的换票就注入（防线停在单元里=没有防线，
// 本仓 §WS-D/§CB 家族教训）。走真库文件形状 + 真回放循环，判定后检查 provider 归集态。
func TestFinaInputCollectWired(t *testing.T) {
	db := finaInputDB(t)
	// 三只票都要有在市元数据与足量日K（PIT 缺省开，池按起始日裁决）
	seed := []map[string]any{
		{"ts_code": "600000.SH", "name": "财务可见", "list_date": "20200101"},
		{"ts_code": "600002.SH", "name": "旧财报", "list_date": "20200101"},
		{"ts_code": "600003.SH", "name": "无财报", "list_date": "20200101"},
	}
	if _, err := db.InsertRows("stocks", store.TableColumns("stocks"), seed); err != nil {
		t.Fatalf("seed stocks: %v", err)
	}
	var bars []map[string]any
	for _, ts := range []string{"600000.SH", "600002.SH", "600003.SH"} {
		for _, kl := range finKLines(t, "20210731", 60) {
			bars = append(bars, map[string]any{
				"ts_code": ts, "trade_date": kl.Date.Format("20060102"),
				"open": kl.Open, "high": kl.High, "low": kl.Low, "close": kl.Close,
				"vol": kl.Volume, "amount": kl.Amount,
			})
		}
	}
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), bars); err != nil {
		t.Fatalf("seed daily: %v", err)
	}
	dir := t.TempDir()
	writeLibraryFile(t, dir, "applied_factors.json", factorEntryJSON(t, "fac_950", true, "ROE"))

	o := &Options{DB: db, Strategy: "factor", DataDir: dir, Start: "20210101", End: "20210731"}
	if _, _, _, err := o.collect(); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if o.finaSrc == nil {
		t.Fatal("collect 必须装配 §B2 财务输入源（装配点在 replay 主循环里，不在测试里）")
	}
	// 三只票都要被逐股注入过：归集档位分别=尚无可见期（判定日都早于 0430 披露）/过旧/真缺失
	st0, st2, st3 := o.finaSrc.status["600000.SH"], o.finaSrc.status["600002.SH"], o.finaSrc.status["600003.SH"]
	if st0 == 0 || st2 != stStale || st3 != stNoFina {
		t.Fatalf("collect 后归集读数应覆盖三只票（600000 判定档/600002 过旧/600003 缺失），实得 %d/%d/%d", st0, st2, st3)
	}
}
