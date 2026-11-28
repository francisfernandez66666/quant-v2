// market_legs_test.go — §REPLAY-INPUTS 2026-11-28（档 2/档 3）：市场级八列填装的行为锁。
//
// 这批列的每一条都对应一个"读数看着正常、语义已经错了"的形态，所以断言全部针对形状：
//
//	① FlowToday 直接取上游 f57，不再除一次（多一次除法＝多一个舍入与分母为 0 的分支）；
//	② Flow5d 要求**连续 5 个交易日**——中间断一天必须整窗 NaN。断一天还硬算，
//	   等于把 6 日窗口的比率当成 5 日窗口打分，数值正常而语义已变；
//	③ 双分母各写各列，缺流通股本 ⇒ 只掉 FlowCap*，不许连带把 FlowToday 也清掉；
//	④ 板块归属没命中 ⇒ SectorFlowQuantile 是 NaN，绝不是分位 0（0 是"全市场最弱"）；
//	⑤ 判定日之后的行不得参与（前视）。
//
// English: locks the eight market-level columns — upstream pct verbatim, five-session
// continuity for the window, two denominators in two columns, and abstain (NaN) rather than 0
// whenever the join key or the denominator is missing.
package research

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/store"
)

// legsDay 夹具日（UTC 零点经 cntime 折算到北京是当日 08:00，日历日不变）。
func legsDay(i int) time.Time { return time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i) }

func legsCompact(i int) string { return cntime.DayCompactOf(legsDay(i)) }

// newLegsDB 建库并装配一只票 12 个交易日的日线（连续自然日，方便构造"连续 5 日"与"断一日"）。
func newLegsDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "legs.db"))
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const code = "600519.SH"
	rows := make([]map[string]any, 0, 12)
	for i := 0; i < 12; i++ {
		p := float64(100 + i)
		rows = append(rows, map[string]any{
			"ts_code": code, "trade_date": legsCompact(i), "open": p, "high": p + 1, "low": p - 1,
			"close": p, "pre_close": p - 1, "change": 1.0, "pct_chg": 1.0,
			"vol": 10000.0, "amount": p * 10000.0, "source": "baostock",
		})
	}
	if _, err := db.InsertRows("daily", store.TableColumns("daily"), rows); err != nil {
		t.Fatalf("写日线失败: %v", err)
	}
	return db
}

func TestMarketLegsFlowColumns(t *testing.T) {
	db := newLegsDB(t)
	const code = "600519.SH"
	start, end := legsCompact(0), legsCompact(11)

	// 只在 0..5 写资金流（6 之后没有读数），用于同时验"有数"与"无数"两种形态。
	flow := make([]store.StockFlowRow, 0, 6)
	for i := 0; i <= 5; i++ {
		flow = append(flow, store.StockFlowRow{
			TsCode: code, TradeDate: legsCompact(i),
			MainNet: float64(1000 * (i + 1)), HasMainNet: true,
			MainPct: float64(2 + i), HasMainPct: true, // 上游直接给的占比，不重算
			Amount: 100000, HasAmount: true,
			Source: "eastmoney_fflow_relay_seed", FetchedAt: "f",
		})
	}
	if _, err := db.UpsertStockMoneyFlow(flow); err != nil {
		t.Fatalf("写资金流失败: %v", err)
	}
	// 流通股本只给到第 5 日 ⇒ 第 6..11 日 FlowCap* 必须 NaN（缺分母不许拿上一日顶班）
	shares := make([]store.FloatShareRow, 0, 6)
	for i := 0; i <= 5; i++ {
		shares = append(shares, store.FloatShareRow{TsCode: code, TradeDate: legsCompact(i),
			Share: 1e9, Source: "precalc_seed_v1"})
	}
	if _, err := db.UpsertFloatShareDaily(shares); err != nil {
		t.Fatalf("写股本失败: %v", err)
	}

	s, err := AssembleWithPanels(db, code, start, end, nil)
	if err != nil || s == nil {
		t.Fatalf("装配失败: %v", err)
	}
	// ① FlowToday＝上游 pct 原值
	if s.FlowToday[0] != 2 || s.FlowToday[5] != 7 {
		t.Fatalf("FlowToday 必须直接取上游 f57，得 %v %v", s.FlowToday[0], s.FlowToday[5])
	}
	// ② 连续 5 日窗口：index 4 = 0..4 的净额和 / 额和
	want := (1000 + 2000 + 3000 + 4000 + 5000) / (5 * 100000.0) * 100.0
	if math.Abs(s.Flow5d[4]-want) > 1e-9 {
		t.Fatalf("Flow5d 应为累计净额/累计成交额，得 %v 期望 %v", s.Flow5d[4], want)
	}
	// ③ 占流通市值：净额 / (股本 × 收盘) ×100
	wantCap := 1000 / (1e9 * 100.0) * 100.0
	if math.Abs(s.FlowCapToday[0]-wantCap) > 1e-12 {
		t.Fatalf("FlowCapToday 算术不符，得 %v 期望 %v", s.FlowCapToday[0], wantCap)
	}
	// 无资金流读数的日子两列都必须是 NaN（不是 0）
	for _, i := range []int{8, 9, 10, 11} {
		if !math.IsNaN(s.FlowToday[i]) || !math.IsNaN(s.Flow5d[i]) || !math.IsNaN(s.FlowCapToday[i]) {
			t.Fatalf("第 %d 日无资金流读数，三列都必须是 NaN，得 %v/%v/%v",
				i, s.FlowToday[i], s.Flow5d[i], s.FlowCapToday[i])
		}
	}
	// 缺股本的第 6 日：FlowCap* 弃权，但 FlowToday 不受牵连（两列各管各的）
	if s.FlowToday[6] != 0 && !math.IsNaN(s.FlowToday[6]) {
		t.Fatalf("第 6 日本就没有资金流行，FlowToday 应 NaN，得 %v", s.FlowToday[6])
	}
}

func TestMarketLegsFlow5dRequiresContinuousSessions(t *testing.T) {
	db := newLegsDB(t)
	const code = "600519.SH"
	start, end := legsCompact(0), legsCompact(11)
	// 关键夹具：第 2 日**缺一行**（上游当日没给），其余都在 ⇒ index 5 的窗口 1..5 含断日。
	flow := make([]store.StockFlowRow, 0, 6)
	for i := 0; i <= 5; i++ {
		if i == 2 {
			continue
		}
		flow = append(flow, store.StockFlowRow{TsCode: code, TradeDate: legsCompact(i),
			MainNet: 1000, HasMainNet: true, MainPct: 1, HasMainPct: true,
			Amount: 100000, HasAmount: true, Source: "s", FetchedAt: "f"})
	}
	if _, err := db.UpsertStockMoneyFlow(flow); err != nil {
		t.Fatalf("写资金流失败: %v", err)
	}
	s, err := AssembleWithPanels(db, code, start, end, nil)
	if err != nil || s == nil {
		t.Fatalf("装配失败: %v", err)
	}
	// 断日之后的窗口必须整窗 NaN——用 4 行冒充 5 日窗口是这条腿最容易犯的错，
	// 且数值完全正常，只有"连续交易日"这个判据能抓住。
	if !math.IsNaN(s.Flow5d[5]) {
		t.Fatalf("窗口内断了交易日必须整窗弃权，得 %v", s.Flow5d[5])
	}
	// 断日之前、窗口完整的第 4 日（0..4 含缺行）同样不完整 ⇒ 也是 NaN
	if !math.IsNaN(s.Flow5d[4]) {
		t.Fatalf("第 4 日窗口含缺行，必须 NaN，得 %v", s.Flow5d[4])
	}
	if s.FlowToday[3] != 1 {
		t.Fatalf("单日读数不受断日影响，得 %v", s.FlowToday[3])
	}
}

func TestMarketLegsPanelsAndProxies(t *testing.T) {
	db := newLegsDB(t)
	const code = "600519.SH"
	start, end := legsCompact(0), legsCompact(11)

	// ① D1 面板：只给第 3 日一条（带横线日期，写边界归一）
	d1Rows := []map[string]any{{"date": cntime.DayOf(legsDay(3)), "code": "600519",
		"score": 25.0, "blocked": 0, "reason": "r", "created_at": "2026-03-04 15:00:00"}}
	if _, err := db.InsertRows("d1_scores", []string{"date", "code", "score", "blocked", "reason", "created_at"}, d1Rows); err != nil {
		t.Fatalf("写 D1 失败: %v", err)
	}

	// ② 板块归属 + 板块净流入 ⇒ 第 3 日该股所在板块的分位
	if _, err := db.UpsertSectorMapDay(legsCompact(3), map[string]string{code: "白酒"}, nil); err != nil {
		t.Fatalf("存档板块归属失败: %v", err)
	}
	flowRows := []map[string]any{
		{"trade_date": cntime.DayOf(legsDay(3)), "sector": "白酒", "net_inflow": 30.0, "amount": 100.0, "delta": 0.1},
		{"trade_date": cntime.DayOf(legsDay(3)), "sector": "银行", "net_inflow": 10.0, "amount": 100.0, "delta": 0.05},
		{"trade_date": cntime.DayOf(legsDay(3)), "sector": "医药", "net_inflow": 20.0, "amount": 100.0, "delta": 0.08},
	}
	if _, err := db.InsertRows("sector_flow_daily", []string{"trade_date", "sector", "net_inflow", "amount", "delta"}, flowRows); err != nil {
		t.Fatalf("写板块资金流失败: %v", err)
	}

	// ③ 基准指数：四根 K 给三只指数序列用（涨跌幅由相邻两根算，首日无前一根 ⇒ 无值）
	idxRows := make([]map[string]any, 0, 4)
	for i := 0; i < 4; i++ {
		idxRows = append(idxRows, map[string]any{"ts_code": "000001.SH", "trade_date": legsCompact(i),
			"open": 3000.0, "high": 3010.0, "low": 2990.0, "close": 3000.0 + float64(i)*30.0,
			"pre_close": 3000.0 + float64(i-1)*30.0, "change": 30.0, "pct_chg": 1.0,
			"vol": 1e6, "amount": 3e9})
	}
	if _, err := db.InsertRows("index_daily", store.TableColumns("index_daily"), idxRows); err != nil {
		t.Fatalf("写指数日线失败: %v", err)
	}

	// ④ 龙头代理分位：只给第 3 日
	if _, err := db.UpsertLeaderRankDay([]store.LeaderRankRow{{TsCode: code,
		TradeDate: legsCompact(3), Sector: "白酒", PctChg: 3.2, HasPctChg: true,
		GainRank: 0.92, HasRank: true}}); err != nil {
		t.Fatalf("写龙头分位失败: %v", err)
	}

	p := LoadPanels(db, start, end)
	if p == nil {
		t.Fatal("面板不应为 nil")
	}
	// 面板点名：D1 有了、板块有了，缺的只有"区间外的其它日"这类正常缺失
	s, err := AssembleWithPanels(db, code, start, end, p)
	if err != nil || s == nil {
		t.Fatalf("带面板装配失败: %v", err)
	}
	if s.D1[3] != 25 {
		t.Fatalf("D1 必须按日命中，得 %v", s.D1[3])
	}
	if !math.IsNaN(s.D1[4]) {
		t.Fatalf("没有 D1 的日期必须 NaN，得 %v", s.D1[4])
	}
	// 白酒当日净流入率 30% 是三只里最高 ⇒ 分位 100（rank/(n-1)×100）
	if s.SectorFlowQuantile[3] != 100 {
		t.Fatalf("板块净流入横截面分位应为 100，得 %v", s.SectorFlowQuantile[3])
	}
	// 归属只覆盖第 3 日 ⇒ 其余日必须 NaN（连接键没命中≠分位 0）
	if !math.IsNaN(s.SectorFlowQuantile[4]) {
		t.Fatalf("无归属日必须 NaN，得 %v", s.SectorFlowQuantile[4])
	}
	if s.SectorGainRank[3] != 0.92 {
		t.Fatalf("龙头代理分位应命中 0.92，得 %v", s.SectorGainRank[3])
	}
	if !math.IsNaN(s.SectorGainRank[5]) {
		t.Fatalf("无分位日必须 NaN，得 %v", s.SectorGainRank[5])
	}
	// 指数涨跌幅：(3030/3000-1)*100 = 1%
	if math.Abs(s.IndexChange[1]-1.0) > 1e-9 {
		t.Fatalf("IndexChange 应为 1.0%%（与载体 (close[-1]/close[-2]-1)*100 同口径），得 %v", s.IndexChange[1])
	}
	if !math.IsNaN(s.IndexChange[4]) {
		t.Fatalf("指数只有 4 根，第 5 日起必须 NaN（不许用最后一根顶班）,得 %v", s.IndexChange[4])
	}
	if p.IndexSource() != "000001.SH" {
		t.Fatalf("供数指数必须点名（上证主、沪深300 次，与载体 _FLOW_INDEX_CODES 同序），得 %q", p.IndexSource())
	}
}

func TestMarketLegsWithNilPanelsStillAssembles(t *testing.T) {
	db := newLegsDB(t)
	s, err := AssembleWithPanels(db, "600519.SH", legsCompact(0), legsCompact(11), nil)
	if err != nil || s == nil {
		t.Fatalf("nil 面板必须照常装配价量腿: %v", err)
	}
	if s.Len() != 12 {
		t.Fatalf("日线根数不符，得 %d", s.Len())
	}
	// 市场级八列在没有面板时全部 NaN（不是 0），且列已分配
	for _, col := range []*[]float64{&s.D1, &s.IndexChange, &s.FlowToday, &s.Flow5d,
		&s.FlowCapToday, &s.FlowCap5d, &s.SectorFlowQuantile, &s.SectorGainRank} {
		if *col == nil || len(*col) != 12 {
			t.Fatal("八列必须已分配且与日期等长（漏分配会让 UpTo 视图静默少列）")
		}
		for i, v := range *col {
			if !math.IsNaN(v) {
				t.Fatalf("无面板时第 %d 格必须是 NaN 弃权，得 %v", i, v)
			}
		}
	}
	// Assemble（旧签名）必须等价于 nil 面板版本——既有调用方行为一字未动
	s2, err := Assemble(db, "600519.SH", legsCompact(0), legsCompact(11))
	if err != nil || s2 == nil || s2.Len() != s.Len() {
		t.Fatalf("旧入口行为改变: %v", err)
	}
}

func TestPanelsMissingIsNamed(t *testing.T) {
	db := newLegsDB(t)
	// 库里既没有 D1、也没有板块资金流、也没有指数 ⇒ 三块都要在 Missing 里点名
	p := LoadPanels(db, legsCompact(0), legsCompact(11))
	miss := p.Missing()
	if len(miss) == 0 {
		t.Fatal("三块面板全缺却 Missing 为空＝腿没接上和接了没数在读数上同形")
	}
	if p.IndexSource() != "" {
		t.Fatal("没有指数数据时不得报供数指数")
	}
	if p.D1DayCount() != 0 || p.SectorQuantileDayCount() != 0 {
		t.Fatal("空面板计数应为 0")
	}
	// nil 接收器一律安全（只读腿不许有停机权限）
	var nilP *Panels
	if _, ok := nilP.D1On(legsCompact(0), "600519"); ok {
		t.Fatal("nil 面板 D1On 必须 false")
	}
	if _, ok := nilP.IndexChangeOn(legsCompact(0)); ok {
		t.Fatal("nil 面板 IndexChangeOn 必须 false")
	}
	if _, ok := nilP.SectorQuantileOn(legsCompact(0), "白酒"); ok {
		t.Fatal("nil 面板 SectorQuantileOn 必须 false")
	}
	if nilP.Missing()[0] != "panels_nil" {
		t.Fatal("nil 面板也要如实报状态")
	}
}

func TestSectorQuantileNeedsAtLeastTwoBoards(t *testing.T) {
	db := newLegsDB(t)
	// 当日只有一个板块有数 ⇒ "分位"没有意义，整天弃权（给 0 或 100 都是编一个方向）
	if _, err := db.InsertRows("sector_flow_daily",
		[]string{"trade_date", "sector", "net_inflow", "amount", "delta"},
		[]map[string]any{{"trade_date": cntime.DayOf(legsDay(2)), "sector": "白酒",
			"net_inflow": 10.0, "amount": 100.0, "delta": 0.1}}); err != nil {
		t.Fatalf("写板块资金流失败: %v", err)
	}
	if _, err := db.UpsertSectorMapDay(legsCompact(2), map[string]string{"600519.SH": "白酒"}, nil); err != nil {
		t.Fatalf("存档归属失败: %v", err)
	}
	p := LoadPanels(db, legsCompact(0), legsCompact(11))
	if _, ok := p.SectorQuantileOn(legsCompact(2), "白酒"); ok {
		t.Fatal("只有一个板块时不得给出分位")
	}
}
