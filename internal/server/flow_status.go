// flow_status.go — §FLOW-QUADRANT 云端净流入 / 量能读数的**只读**上屏装配（2026-11-20）。
//
// 首页仪表盘要显示两个市场级指标：
//
//	① 云端净流入：flow_factor_daily.market_flow_pct（净流入 ÷ 当日成交额 %）与
//	   归一后的 flow_market_delta ∈[-1,1]；
//	② 量能状态：r_yest / r_ma5 与由 signalctl.VolStateOf 判出的放量/缩量/平量。
//
// 三条纪律（与本仓既有的"读不到不装已知"同源）：
//   - **缺数不许当 0**：NULL 一律下发 null 并标 *_known=false，前端显示"今日无读数（弃权）"。
//     把弃权画成"平量/0%"，等于让人以为信号参与过合乘而实际没有。
//   - **量态只有一个真源**：VolStateOf 判态、VolSurgeDeltaOf 出强度，展示层不另写阈值。
//   - **合成乘子只有一份公式**：`signalctl.FlowBlendPreview` 直接转调 ApplyFlowBlend，
//     这里只负责把三项 delta 递过去，绝不在 server 里再写 `1+0.2*b`。
//
// 本文件不写库、不改任何状态：它是一个纯读面，供 GET /api/status 的 `flow` 键使用
// （**不新开路由**，因此契约台账与 §115 的五组派生计数一个字都不用动）。
// English: read-only assembly of the cloud net-inflow / volume readings for the dashboard.
// NULL stays null (abstain), never 0; the volume state and the blend preview both delegate to
// the single sources of truth in signalctl. No route is added.
package server

import (
	"math"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/signalctl"
)

// flowNullF *float64 → float64：nil（SQL NULL / 弃权）→ NaN，其余原样。
// 用 NaN 而不是 0 进 FlowBlendPreview，才能在预览里把"这条腿没读数"与"这条腿是平量"分开。
// English: nil (NULL) → NaN so the preview can tell "no reading" from "flat".
func flowNullF(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

// flowStatusReadout 装配 /api/status 的 `flow` 子对象。
//
// 返回结构（值缺失一律 null，**绝不给 0**）：
//
//	available    读数源是否接线（researchDB 为空=false，前端显示"读数未接入"）
//	day          读数所属北京交易日（YYYY-MM-DD）
//	known        当日是否已有一行 flow_factor_daily（false=今天还没烘过/引擎未跑）
//	market_known 净流入腿是否有值（false=今天没有净流入读数）
//	market_pct   净流入 ÷ 当日成交额（%）
//	market_delta 归一强度 ∈[-1,1]
//	vol_known    量比腿是否有值
//	vol_state    surge / shrink / neutral（**仅 vol_known=true 时存在**）
//	r_yest       今日折算量 / T-1
//	r_ma5        今日折算量 / T-1..T-5 均量
//	vol_delta    量态强度（+1/-1/0）
//	sector_count 当日已烘入的板块读数行数（0=板块腿今天没数据，如实显示）
//	sector_known 板块聚合两列是否有值（false ⇒ 前端显示"弃权"，不许画 0）
//	sector_pct   板块净流入合计 ÷ 板块成交额合计 × 100（与 market_pct 同形态）
//	sector_delta 各板块 delta 的等权均值 ∈[-1,1]（"多数板块的强度"）
//	blend        三项 delta 的合成金额乘子预览（缺项 renormalize，±20% 封顶；全弃权=null）
//	blend_ok     是否至少一条腿有效（false ⇒ 合成乘子不参与，前端显示"弃权"）
//
// 刻意**不下发 source 列**：flow_factor_daily.source 是一个共享列，量比腿每 30s 的写口
// （UpsertFlowVolumeOnly）会把它覆盖成自己的值，线上读到的其实只是"最近一次写库来源"，
// 拿它当"净流入的来源"会撒谎。来源归因留给日志与库里的原始行，展示层不猜。
//
// now 由调用方传入（测试注入固定时刻，生产走 time.Now()）。
func (s *Server) flowStatusReadout(now time.Time) map[string]interface{} {
	day := cntime.DayOf(now)
	out := map[string]interface{}{
		"available": s.researchDB != nil,
		"day":       day,
		"known":     false,
	}
	if s.researchDB == nil {
		return out
	}
	row, err := s.researchDB.GetFlowFactorDaily(day)
	if err != nil || row == nil {
		// 读失败与"今天没有行"在展示上同形（都是无读数），但 known 必须为 false——
		// 绝不能因为出错就编一行 0 出来。
		return out
	}
	out["known"] = true

	// 净流入腿：pct 与 delta 任何一个有值就算"有读数"（delta 是归一副本，不该单独缺）。
	if row.MarketFlowPct != nil || row.FlowMarketDelta != nil {
		out["market_known"] = true
		out["market_pct"] = row.MarketFlowPct // nil ⇒ JSON null（不折成 0）
		out["market_delta"] = row.FlowMarketDelta
	}

	// 量比腿：r_yest/r_ma5 都齐才能判态——只拿一个去判会把"算不出"读成"平量"。
	// 判态与强度各转调 signalctl 一处真源，不在展示层重写 1.2/1.0/0.9 这些阈值。
	if row.RYest != nil && row.RMa5 != nil {
		state := signalctl.VolStateOf(*row.RYest, *row.RMa5)
		out["vol_known"] = true
		out["vol_state"] = state
		out["r_yest"] = *row.RYest
		out["r_ma5"] = *row.RMa5
		out["vol_delta"] = signalctl.VolSurgeDeltaOf(state)
	}

	// 板块腿覆盖度：0 行是如实读数（缺 sector_map 生产者时就是 0），不当作错误。
	if n, cerr := s.researchDB.CountSectorFlowDaily(day); cerr == nil {
		out["sector_count"] = n
	}

	// §FLOW-SECTOR-AGG（owner 2026-11-20）：板块聚合两列的读数面。
	// 口径（写侧在 engine.aggregateSectorFlow）：
	//   sector_pct   = 板块净流入合计 ÷ 板块成交额合计 × 100（与 market_pct 同形态，可直接对比）
	//   sector_delta = 各板块 delta 的等权均值（"多数板块的强度"，与大盘腿刻意不同口径）
	// 缺数一律 null + known=false —— 绝不折成 0（0 会被读成"板块中性"，那是另一个意思）。
	if row.SectorFlowPct != nil || row.FlowSectorDelta != nil {
		out["sector_known"] = true
		out["sector_pct"] = row.SectorFlowPct
		out["sector_delta"] = row.FlowSectorDelta
	}

	// 合成乘子预览：把三项 delta 原样递给 signalctl（缺项传 NaN ⇒ 弃权）。
	// 这里**不写公式**——封顶/renormalize 都在 ApplyFlowBlend 里，改一处两处同变。
	blend, ok := signalctl.FlowBlendPreview(
		flowNullF(row.FlowMarketDelta), flowNullF(row.FlowSectorDelta), flowNullF(row.VolSurgeDelta))
	out["blend_ok"] = ok
	if ok {
		out["blend"] = blend
	}
	return out
}
