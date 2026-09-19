// limitup_micro.go §ENH-A 涨停微结构因子族（CatLimit）。
// 素材为同花顺三池按日对齐进 StockSeries 的单票事件列（LimitBoard/SealAmtRatio/
// BreakCnt/FirstSealMin，research.Assemble 装载）。打开"打板/情绪"战法的搜索空间——
// 此前面板只有全市场情绪标量（Emo*），单票连板/封单/炸板/首封维度结构性缺失，
// 是夜间发现半年不出新类型战法的搜索空间侧根因（docs/RESEARCH_LAYER_AUDIT_20260919.md R-6）。
//
// 口径纪律：
//   - 事件日 = LimitBoard>0（当日在涨停池）。非事件日各列 0/NaN，因子统一输出 NaN
//     （缺失日不参与 IC，避免把"没涨停"稀释成 0 分噪音）；仅趋势类（SealStrength）
//     用 5 日滚动窗口把事件日取值外延，窗口内无事件则 NaN。
//   - 仅进研究池，不注入实盘 runner（实盘消费仍走已审批规则）。
//
// English: §ENH-A per-stock limit-up microstructure factors (CatLimit), built from the
// THS pools preloaded into StockSeries by research.Assemble. Non-event days output NaN
// so cross-sectional IC runs on the event subset; research-pool only.
package factor

import "math"

// isLuEvent 当日是否涨停事件日（连板数>0）。
func isLuEvent(s *StockSeries, i int) bool {
	return i < len(s.LimitBoard) && !isNaNF(s.LimitBoard[i]) && s.LimitBoard[i] > 0
}

// isNaNF NaN 判定（factor 包内小工具）。
func isNaNF(v float64) bool { return math.IsNaN(v) }

// init 注册涨停微结构因子族（CatLimit）。
func init() {
	// BoardCnt 连板高度：当日连板数（1=首板，2=二板…）。
	Register(Def{
		ID: "lu_board", Name: "连板高度", Cat: CatLimit,
		Desc: "当日连板数（涨停池 continue_cnt；非涨停日无值）",
		Compute: func(s *StockSeries) []float64 {
			return luMap(s, func(i int) float64 { return s.LimitBoard[i] })
		},
	})
	// Board3 三板及以上：高标辨识度（打板战法核心分组）。
	Register(Def{
		ID: "lu_board3", Name: "三板及以上", Cat: CatLimit,
		Desc: "连板数 ≥3 记 1，否则 0（高标辨识度）",
		Compute: func(s *StockSeries) []float64 {
			return luMap(s, func(i int) float64 {
				if s.LimitBoard[i] >= 3 {
					return 1
				}
				return 0
			})
		},
	})
	// SealStrength 封单强度：峰值封单/流通市值的 5 日滚动均值（事件日外延窗口）。
	Register(Def{
		ID: "lu_seal_strength", Name: "封单强度", Cat: CatLimit,
		Desc: "近5日 峰值封单额/流通市值 均值（窗口内无涨停事件则无值）",
		Compute: func(s *StockSeries) []float64 {
			n := len(s.Dates)
			out := make([]float64, n)
			for i := range out {
				out[i] = math.NaN()
				if i < 4 {
					continue // 5 日预热期
				}
				// 滚动窗 [i-4, i]：只对事件日取封单比求均值（非事件日贡献 0 个样本）。
				sum, cnt := 0.0, 0
				for j := i - 4; j <= i; j++ {
					if !isLuEvent(s, j) {
						continue
					}
					v := s.SealAmtRatio[j]
					if isNaNF(v) {
						continue // 流通市值缺失的事件日不计入均值
					}
					sum += v
					cnt++
				}
				if cnt > 0 {
					out[i] = sum / float64(cnt)
				}
			}
			return out
		},
	})
	// EarlySeal 早盘秒板：首封早于 10:00 且当日未被炸（封单坚决度）。
	Register(Def{
		ID: "lu_early_seal", Name: "早盘首封", Cat: CatLimit,
		Desc: "首封时间 < 10:00 且当日开板次数=0 记 1，事件日内否则 0",
		Compute: func(s *StockSeries) []float64 {
			return luMap(s, func(i int) float64 {
				early := i < len(s.FirstSealMin) && !isNaNF(s.FirstSealMin[i]) && s.FirstSealMin[i] <= 600
				closed := i >= len(s.BreakCnt) || isNaNF(s.BreakCnt[i]) || s.BreakCnt[i] == 0
				if early && closed {
					return 1
				}
				return 0
			})
		},
	})
	// RelBlastLow 低炸板环境封板：事件日且当日全市场炸板率处于近 60 日低分位（≤30%）。
	// 情绪退潮期的封板更"贵"（逆势强度信号）；市场炸板率序列全体一致，
	// 滚动分位只用截至当日的历史（无未来函数）。
	Register(Def{
		ID: "lu_reblast_low", Name: "逆势封板", Cat: CatLimit,
		Desc: "涨停日当日市场炸板率处于近60日低分位(≤30%) 记 1，事件日内否则 0",
		Compute: func(s *StockSeries) []float64 {
			n := len(s.Dates)
			out := make([]float64, n)
			for i := range out {
				out[i] = math.NaN()
				if !isLuEvent(s, i) {
					continue // 非涨停日不参与（保留 NaN 掩码）
				}
				v := s.EmoBlastRate[i]
				if isNaNF(v) {
					continue // 当日市场情绪统计缺失：不判定
				}
				// 近 60 日（含当日）炸板率历史分位：低于当前值的观测占比。
				lo, seen := 0, 0
				for j := i - 59; j <= i; j++ {
					if j < 0 {
						continue
					}
					h := s.EmoBlastRate[j]
					if isNaNF(h) {
						continue
					}
					seen++
					if h > v {
						lo++ // 当前炸板率低于历史观测 → 低位计数
					}
				}
				if seen < 10 {
					continue // 历史样本不足：不判定（NaN），不得折算成 0 分
				}
				if float64(lo)/float64(seen) >= 0.7 {
					out[i] = 1
				} else {
					out[i] = 0
				}
			}
			return out
		},
	})
}

// luMap 事件日映射：非事件日输出 NaN（不参与 IC），事件日取值由 fn 决定（可为 0）。
// English: event-day mapping; non-event days emit NaN so IC only scores the limit-up subset.
func luMap(s *StockSeries, fn func(i int) float64) []float64 {
	if s == nil {
		return nil
	}
	out := make([]float64, len(s.Dates))
	for i := range out {
		if isLuEvent(s, i) {
			out[i] = fn(i)
		} else {
			out[i] = math.NaN()
		}
	}
	return out
}
