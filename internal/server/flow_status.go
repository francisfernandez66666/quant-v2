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
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quant-trading-v2/internal/cntime"
	"quant-trading-v2/internal/signalctl"
)

// sidePolicyStateDir 侧策略快照的**账号**目录。
//
// 引擎自 §W-03 起按账号落盘（registry.go:1028 acctDir = DataDir/accounts/<uid>），
// 而读面以前直接吃 s.researchDir（数据根目录）⇒ 恒 unknown，与"策略没启用"在页面上同形
// （D6-c 2026-11-25）。这里返回账号目录；根目录那份由调用方当兜底再试一次。
func (s *Server) sidePolicyStateDir() string {
	if s.researchDir == "" {
		return ""
	}
	uid := ""
	if s.cfg != nil {
		uid = s.cfg.OperatorID()
	}
	if uid == "" {
		return s.researchDir
	}
	return filepath.Join(s.researchDir, "accounts", uid)
}

// readSidePolicyStatus 只读侧策略快照，并在读面统一应用新鲜度闸。
// known 只表示"当日快照可解析"；disabled/stale/known 三态由 enabled/stale/status 明确区分。
// fallbackDir 非空时在 dir 读不到的情况下再试一次（跨版本过渡期根目录可能还有旧文件）。
func readSidePolicyStatus(dir, fallbackDir, day string, now time.Time) map[string]interface{} {
	out := map[string]interface{}{"known": false, "enabled": false, "stale": false, "status": "unknown"}
	if dir == "" {
		return out
	}
	raw, err := os.ReadFile(filepath.Join(dir, "side_policy_latest.json"))
	if err != nil && fallbackDir != "" && fallbackDir != dir {
		raw, err = os.ReadFile(filepath.Join(fallbackDir, "side_policy_latest.json"))
	}
	if err != nil {
		return out
	}
	var st signalctl.SidePolicyState
	if json.Unmarshal(raw, &st) != nil || st.Day != strings.ReplaceAll(day, "-", "") {
		return out
	}
	out["known"] = true
	out["reason"] = st.Reason
	if !st.Enabled {
		// 未启用不是弃权，也不下发 neutral 乘子，避免前端把 ×1.00 画成"策略正在生效"。
		out["status"] = "disabled"
		out["mode"] = string(signalctl.SidePolicyLegacy)
		return out
	}
	st = signalctl.ApplySidePolicyFreshness(st, now)
	stale := st.Reason == "side_policy_stale" || st.Reason == "side_policy_asof_missing" || st.Reason == "side_policy_asof_invalid"
	out["enabled"] = true
	out["stale"] = stale
	if stale {
		out["status"] = "stale"
	} else {
		out["status"] = "known"
	}
	out["mode"] = string(st.Mode)
	out["level"] = st.Level
	out["allow_new"] = st.AllowNew
	out["allow_add"] = st.AllowAdd
	out["target_multiplier"] = st.TargetMultiplier
	out["left_weight"] = st.LeftWeight
	out["right_weight"] = st.RightWeight
	out["slope_delta"] = st.SlopeDelta
	out["gap_pct"] = st.GapPct
	// §SIDE-FLOW-PROXY 2026-11-26：这条腿到底用了哪份数，必须在读面上说清。
	// market_delta 的零值是**合法中性值**，所以"有没有读数"只能靠 delta_source 判——
	// 直接下发 0 会让运维把"没读到"看成"资金流中性"，那是本仓反复踩过的画 0 坑。
	out["market_delta"] = st.MarketDelta
	out["market_delta_known"] = st.DeltaSource != ""
	out["delta_source"] = st.DeltaSource
	// §SIDE-PM-GATE 2026-11-26：13:00 那一档今天是否已被"数据齐全"确认过。
	// false 且时刻已过 13:00 ⇒ 载体仍按上午档压着（不是缺数，是没确认）。
	out["pm_release"] = st.PMRelease
	out["latch_active"] = st.Latch.Kind == "no_new" && st.LatchUntilUnix > now.Unix()
	out["reason"] = st.Reason
	return out
}

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
// readMoneyflowRunAge 读采集完成标记的"距今多久 + 什么结论"。
// 读不到返回 ok=false（前端显示"采集器未跑"），**不返回一个 0 秒的假新鲜**。
func readMoneyflowRunAge(dir string, now time.Time) (float64, string, bool) {
	if dir == "" {
		return 0, "", false
	}
	raw, err := os.ReadFile(filepath.Join(dir, "moneyflow_run.json"))
	if err != nil {
		return 0, "", false
	}
	var mk struct {
		Day        string `json:"day"`
		Status     string `json:"status"`
		FinishedAt string `json:"finished_at"`
	}
	if json.Unmarshal(raw, &mk) != nil || mk.Day == "" {
		return 0, "", false
	}
	t, perr := time.Parse(time.RFC3339, mk.FinishedAt)
	if perr != nil {
		return 0, mk.Status, true // 有结论但时刻解析不动：age 不下发，状态照报
	}
	age := now.Sub(t).Seconds()
	if age < 0 {
		age = 0 // 钟差（盒子/云端不同步）不许把"未来"读成负数龄
	}
	return age, mk.Status, true
}

func (s *Server) flowStatusReadout(now time.Time) map[string]interface{} {
	day := cntime.DayOf(now)
	out := map[string]interface{}{
		"available":   s.researchDB != nil,
		"day":         day,
		"known":       false,
		"side_policy": readSidePolicyStatus(s.sidePolicyStateDir(), s.researchDir, day, now),
	}
	if s.researchDB == nil {
		return out
	}
	// §REPLAY-INPUTS 档 3b：个股资金流覆盖率与采集器新鲜度**先**填。
	// 刻意排在 flow_factor_daily 的早退分支之前：那张表在盘前/非交易日没有行，
	// 若把采集器读数挂在它后面，停更告警会在"最该看的那几天"整块消失。
	s.fillStockFlowReadout(out, now)
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

// fillStockFlowReadout 填个股资金流（长期日频）的覆盖率与采集器新鲜度六键。
//
// 为什么单独一个函数、而且调用点在 flow_factor_daily 的早退之前：这张表只有一个
// systemd timer 在写，一旦停更，表现不是报错而是"fac_175/179 用旧数据算出看着正常的分"
// ——ths_* 静默停更一年多就是这个形状（AGENTS 实录）。所以停更必须在**没有当日
// flow_factor_daily 行**的那些天也照常喊疼。
//
// 缺数一律不画 0：`codes=0`（表在但今天没采）、"表没建/从没采过"（`stock_flow_known=false`）、
// "采集器未跑"（`stock_flow_run_status` 缺席）是三件事，前端分三句文案。
func (s *Server) fillStockFlowReadout(out map[string]interface{}, now time.Time) {
	if last, err := s.researchDB.MaxStockFlowDay(); err == nil && last != "" {
		// 库里是紧凑形态，展示层给带横线的（前端日期一律这个形状）。
		dashed := last
		if len(last) == 8 {
			dashed = last[:4] + "-" + last[4:6] + "-" + last[6:]
		}
		out["stock_flow_day"] = dashed
		if n, cerr := s.researchDB.CountStockFlowCodes(last); cerr == nil {
			out["stock_flow_codes"] = n
		}
		if total, terr := s.researchDB.CountCollectableCodes(); terr == nil && total > 0 {
			out["stock_flow_total"] = total
			if codes, ok := out["stock_flow_codes"].(int); ok {
				out["stock_flow_coverage_pct"] = float64(codes) / float64(total) * 100.0
			}
		}
		out["stock_flow_known"] = true
	} else {
		out["stock_flow_known"] = false
	}
	if age, status, ok := readMoneyflowRunAge(s.researchDir, now); ok {
		out["stock_flow_run_age_sec"] = age
		out["stock_flow_run_status"] = status
	}
}
