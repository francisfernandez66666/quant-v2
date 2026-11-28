// discover_panels_test.go — §REPLAY-INPUTS 2026-11-28（owner 裁决：「不能接线因子做完了，
// 不拿去回测挖掘新战法」）：自动研究的装配必须真的带上市场级面板。
//
// 现场形态（这条锁就是冲着它写的）：七枚新因子在册、在打分、在载体也烘好了，
// 但 discover-factors 的装配走的是不带面板的 `Assemble` ⇒ IndexChange / FlowToday / Flow5d /
// FlowCapToday / FlowCap5d / SectorFlowQuantile / SectorGainRank 七列**结构性全 NaN**。
// 于是单因子预筛里它们一行 IC 都没有、永远进不了候选池，**一条新战法都挖不出来**，
// 而整条链照常跑完、候选照常产出、日志里一个错都没有。
//
// 三条断言各锁一层：
//   - 正锁：`BuildPanels`（研究侧批量装配的唯一入口）装出来的面板里，有数据的市场级因子必须有读数；
//   - 负锁：同一份夹具走 `Assemble`（不带面板）时那七列必须全 NaN——把 BuildPanels 改回
//     不带面板的写法会让正锁红，而这条负锁说明"NaN 是装配漏了，不是数据没有"；
//   - 断点键：必须带 `panelAssemblyTag`，否则修好装配之后的第一晚仍会命中"当时全 NaN"的旧断点。
package research

import (
	"math"
	"strings"
	"testing"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/factor"
	"quant-trading-v2/internal/store"
)

func TestBuildPanelsCarriesMarketColumns(t *testing.T) {
	db := newLegsDB(t)
	const code = "600519.SH"
	start, end := legsCompact(0), legsCompact(11)

	// ① 个股资金流 0..5 日（FlowToday / Flow5d）
	flow := make([]store.StockFlowRow, 0, 6)
	for i := 0; i <= 5; i++ {
		flow = append(flow, store.StockFlowRow{
			TsCode: code, TradeDate: legsCompact(i),
			MainNet: float64(1000 * (i + 1)), HasMainNet: true,
			MainPct: float64(2 + i), HasMainPct: true,
			Amount: 100000, HasAmount: true,
			Source: "eastmoney_fflow_relay_seed", FetchedAt: "f",
		})
	}
	if _, err := db.UpsertStockMoneyFlow(flow); err != nil {
		t.Fatalf("写资金流失败: %v", err)
	}

	// ② 板块归属 + 板块资金流 ⇒ SectorFlowQuantile（第 3 日）
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

	// ③ 龙头代理分位（第 3 日）
	if _, err := db.UpsertLeaderRankDay([]store.LeaderRankRow{{TsCode: code,
		TradeDate: legsCompact(3), Sector: "白酒", PctChg: 3.2, HasPctChg: true,
		GainRank: 0.92, HasRank: true}}); err != nil {
		t.Fatalf("写龙头分位失败: %v", err)
	}

	// ④ 基准指数四根（IndexChange 由相邻两根算，首日无前一根）
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

	// 研究侧真正用的因子集：就是这五枚有数据的市场级因子
	ids := []string{"IndexChange", "FlowToday", "Flow5d", "SectorFlowQuantile", "SectorGainRank"}
	defs := make([]factor.Def, 0, len(ids))
	for _, id := range ids {
		d, ok := factor.Get(id)
		if !ok {
			t.Fatalf("因子 %s 未注册（注册表与列名脱钩会让整条腿静默弃权）", id)
		}
		defs = append(defs, d)
	}

	panels, err := BuildPanels(db, []string{code}, start, end, defs)
	if err != nil || len(panels) != 1 {
		t.Fatalf("BuildPanels 应装配出 1 只票，得 %d err=%v", len(panels), err)
	}
	p := panels[0]
	nonNaN := func(id string, i int) float64 {
		vals := p.Factors[id]
		if len(vals) != p.Series.Len() {
			t.Fatalf("因子 %s 长度 %d 与序列 %d 不等（等长是 IC 计算的前提）", id, len(vals), p.Series.Len())
		}
		if math.IsNaN(vals[i]) {
			t.Fatalf("因子 %s 第 %d 日仍是 NaN——BuildPanels 没有带市场级面板，"+
				"自动研究将永远看不见这枚因子（这正是 owner 指出的「接线做完不拿去挖战法」）", id, i)
		}
		return vals[i]
	}
	// 指数：第 1 日 (3030/3000-1)*100 = 1%
	if got := nonNaN("IndexChange", 1); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("IndexChange 应为 1.0%%，得 %v", got)
	}
	// 个股资金流：上游 f57 原值
	if got := nonNaN("FlowToday", 0); math.Abs(got-2.0) > 1e-12 {
		t.Fatalf("FlowToday 应取上游占比 2，得 %v", got)
	}
	nonNaN("Flow5d", 4)
	// 板块净流入分位：白酒 30% 是三只里最高 ⇒ 100
	if got := nonNaN("SectorFlowQuantile", 3); math.Abs(got-100.0) > 1e-9 {
		t.Fatalf("SectorFlowQuantile 应为 100，得 %v", got)
	}
	// 龙头代理分位
	if got := nonNaN("SectorGainRank", 3); math.Abs(got-0.92) > 1e-12 {
		t.Fatalf("SectorGainRank 应命中 0.92，得 %v", got)
	}

	// 负锁：同一份夹具、不带面板 ⇒ **面板派生**的三列结构性全 NaN
	//（"装配漏了"与"数据没有"必须分得开）。三类的分工不同，别混成一句：
	//   · D1 / IndexChange / SectorFlowQuantile 只能来自面板（整表或全市场截面），
	//     装配不带面板时它们**没有任何别的来源** ⇒ 自动研究结构性看不见；
	//   · FlowToday / Flow5d / FlowCap* 是逐票查询（个股资金流 + 流通股本），
	//   · SectorGainRank 也是逐票查预计算表（leader_rank_daily）——都不受面板影响。
	// 把这两类混进负锁会让它变成假锁（今天就是这么被咬出来的）。
	s, err := Assemble(db, code, start, end)
	if err != nil || s == nil {
		t.Fatalf("无面板装配失败: %v", err)
	}
	for _, col := range []struct {
		name string
		vals []float64
	}{
		{"D1", s.D1}, {"IndexChange", s.IndexChange}, {"SectorFlowQuantile", s.SectorFlowQuantile},
	} {
		if len(col.vals) != s.Len() {
			t.Fatalf("无面板时 %s 列未分配（UpTo 视图会静默少列）", col.name)
		}
		for i, v := range col.vals {
			if !math.IsNaN(v) {
				t.Fatalf("无面板时 %s 第 %d 格应为 NaN，得 %v", col.name, i, v)
			}
		}
	}
	// 逐票两列不受面板影响：这条不是"随便加一条绿"，它钉住"缺陷范围只在面板派生的三列"。
	if math.IsNaN(s.FlowToday[0]) || s.FlowToday[0] != 2 {
		t.Fatalf("个股资金流是逐票查询，无面板时也必须有读数（得 %v）", s.FlowToday[0])
	}
	if math.IsNaN(s.SectorGainRank[3]) || s.SectorGainRank[3] != 0.92 {
		t.Fatalf("龙头分位是逐票查预计算表，无面板时也必须有读数（得 %v）", s.SectorGainRank[3])
	}
}

// TestDiscoveryResumeKeyCarriesPanelAssembly 断点键必须带面板装配口径位。
//
// 为什么这条比"正锁"更要紧：正锁只保证**今天**装得对；断点键决定**明天**会不会复用
// "当时七列全 NaN"的旧结论。缺这一位时，修好装配的第一晚仍然读到旧缓存、
// 仍然认为新因子没有 IC，而且日志上完全看不出（缓存命中是静默的）。
func TestDiscoveryResumeKeyCarriesPanelAssembly(t *testing.T) {
	k := discoveryResumeKey("20230801", "20261127", 5, 20, 60,
		[]string{"FlowToday", "Mom20"}, []string{"600519.SH"}, nil)
	if !strings.Contains(k, panelAssemblyTag) {
		t.Fatalf("发现断点键缺少面板装配口径位 %q（改前面板会被复用）: %s", panelAssemblyTag, k)
	}
	// 位置契约：口径位必须在复权口径位之前——adj_basis_test.go 靠 TrimSuffix(adjBasisTag) 造旧键。
	if !strings.HasSuffix(k, adjBasisTag) {
		t.Fatalf("断点键必须以复权口径位结尾（TrimSuffix 造旧键的锁依赖这个形状）: %s", k)
	}
}
