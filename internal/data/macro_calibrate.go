// macro_calibrate.go — §MARKET_RISK_GATE P6 宏观日历校准。
// 把"公式估算日历"（GenMacroEvents：CPI=每月13日、交割日=第三个周五、FOMC 仅 2026 表）升级为
// "外部 API / LLM 校准真实发布日 + 公式兜底"。校准链优先级：外部 API → LLM → 当日缓存 → 公式，
// 任一环失败静默降级到下一环，绝不因校准不可用而中断主流程或编造日期。
// 校准结果进程级缓存（SetCalibratedEvents），combat_agent 的年份日历缓存读取后并入（校准值覆盖同 Level
// 公式值），供风险档合成（交割日/CPI/FOMC 精确到真实日）消费。
//
// English: macro_calibrate.go — the P6 calendar calibration. It upgrades the formula-estimated calendar
// (CPI = the 13th, delivery = the 3rd Friday, FOMC only a 2026 table) into "real release dates via external
// API / LLM, with a formula fallback". Chain priority: external API → LLM → today's cache → formula; every
// failure silently degrades to the next and never blocks the main flow or fabricates dates. The result is
// cached process-wide (SetCalibratedEvents) and merged into combat_agent's per-year calendar (calibrated
// overrides the same-Level formula), feeding the risk-tier synthesis (delivery/CPI/FOMC on real dates).
package data

import (
	"log"
	"sync"
	"time"
)

// 校准事件的进程级覆盖缓存（combat_agent 年份日历合并时读取）。
var (
	calibratedMu      sync.RWMutex
	calibratedEvents  []MacroEvent // 校准得到的事件（含真实日期），Level 非空
	calibratedVersion int64        // 每次 SetCalibratedEvents 递增，供消费方判定缓存是否需重建
)

// CalibratedVersion 返回校准缓存的代际号（每日重校准后变化，消费方据此失效自身年份缓存）。
// English: returns the calibration generation counter (bumps on each recalibration; consumers invalidate
// their per-year caches when it changes).
func CalibratedVersion() int64 {
	calibratedMu.RLock()
	defer calibratedMu.RUnlock()
	return calibratedVersion
}

// SetCalibratedEvents 写入校准事件覆盖（并发安全）。传 nil 清空（回退纯公式）。每次调用递增版本号。
// English: stores the calibrated override events (thread-safe); nil clears (formula-only). Bumps the version.
func SetCalibratedEvents(events []MacroEvent) {
	calibratedMu.Lock()
	calibratedEvents = events
	calibratedVersion++
	calibratedMu.Unlock()
}

// CalibratedEvents 返回当前校准事件快照（可能为 nil=未校准）。
// English: returns a snapshot of the calibrated events (nil = not calibrated).
func CalibratedEvents() []MacroEvent {
	calibratedMu.RLock()
	defer calibratedMu.RUnlock()
	out := make([]MacroEvent, len(calibratedEvents))
	copy(out, calibratedEvents)
	return calibratedEvents
}

// externalToMacro 把外部/LLM 事件转为 MacroEvent（日期非法/Level 空则跳过）。Source 由调用方标注。
// English: converts external/LLM events into MacroEvent (skipping bad dates / empty level); Source set by the caller.
func externalToMacro(ext []ExternalEvent, source string) []MacroEvent {
	out := make([]MacroEvent, 0, len(ext))
	for _, e := range ext {
		d, err := time.ParseInLocation("2006-01-02", e.Date, time.Local)
		// 日期解析失败跳过该事件（宁缺毋滥，不用错误日期污染日历）。
		if err != nil {
			log.Printf("[macro_calibrate] 跳过非法日期事件 %q date=%q: %v", e.Title, e.Date, err)
			continue
		}
		// 缺省级别归为 other，影响度非法回退 medium，影响期按影响度（high 3 天 / 其余 2 天）。
		lvl := e.Level
		if lvl == "" {
			lvl = "other"
		}
		imp := e.Impact
		if imp != "high" && imp != "medium" && imp != "low" {
			imp = "medium"
		}
		dur := 2
		if imp == "high" {
			dur = 3
		}
		out = append(out, MacroEvent{Date: d, Title: e.Title, Level: lvl, Impact: imp, Duration: dur, Source: source})
	}
	return out
}

// MergeCalibrated 用校准事件覆盖公式日历：同 Level 且日期相近（±5 天内视为同一事件）时以校准日期为准
// （Source 改为校准来源）；校准中的全新事件直接并入；公式中未被覆盖的事件保留。
// 返回合并后的事件列表（Source 标注来源，供排查真实日期 vs 估算）。
// English: overlays calibrated events onto the formula calendar — same Level within ±5 days is treated as
// the same event and takes the calibrated date (Source flipped to the calibration provenance); brand-new
// calibrated events are added; untouched formula events remain. Returns the merged list with Source tags.
func MergeCalibrated(formula []MacroEvent, calibrated []MacroEvent) []MacroEvent {
	if len(calibrated) == 0 {
		return formula
	}
	merged := make([]MacroEvent, len(formula))
	copy(merged, formula)
	const dayTol = 5 * 24 * time.Hour
	for _, c := range calibrated {
		replaced := false
		for i := range merged {
			if merged[i].Level == c.Level {
				diff := merged[i].Date.Sub(c.Date)
				if diff < 0 {
					diff = -diff
				}
				if diff <= dayTol { // 命中同一事件 → 校准日期覆盖
					merged[i].Date = c.Date
					merged[i].Source = c.Source
					if c.Title != "" {
						merged[i].Title = c.Title
					}
					replaced = true
					break
				}
			}
		}
		if !replaced {
			merged = append(merged, c)
		}
	}
	return merged
}

// CalibrateMacroCalendar 执行一次日历校准并写入进程覆盖缓存。
// 优先级：外部 API(apiURL 非空) → LLM(chat 非空) → 当日缓存缓存文件 cacheFile → 公式（清空覆盖）。
// 返回采用的来源标签（external/llm/cached/formula）与校准事件条数。任何失败都降级到下一级，绝不 panic。
// chat 为 LLM 聊天函数（引擎传 llmClient.Chat），months 为前瞻月数。
// English: runs one calibration and stores the override. Priority: external API (if url set) → LLM (if chat
// set) → today's cache file → formula (override cleared). Returns the source label used and the event count.
// Every failure degrades to the next level and never panics. chat is the engine's LLM chat func; months is
// the forward horizon.
func CalibrateMacroCalendar(chat ChatFunc, apiURL, cacheFile string, year, months int) (string, int) {
	// 1) 外部 API（若有）
	if ext := FetchCalendarFromAPI(apiURL); len(ext) > 0 {
		ev := externalToMacro(ext, "external")
		SetCalibratedEvents(ev)
		persistCalendarCache(cacheFile, ev)
		log.Printf("[macro_calibrate] 外部API校准成功: %d 事件", len(ev))
		return "external", len(ev)
	}
	// 2) LLM
	if chat != nil {
		if ext := FetchCalendarFromLLM(chat, months); len(ext) > 0 {
			ev := externalToMacro(ext, "llm")
			SetCalibratedEvents(ev)
			persistCalendarCache(cacheFile, ev)
			log.Printf("[macro_calibrate] LLM 校准成功: %d 事件", len(ev))
			return "llm", len(ev)
		}
	}
	// 3) 当日缓存
	if cached, ok := LoadCalendarCache(cacheFile); ok && len(cached) > 0 {
		SetCalibratedEvents(cached)
		log.Printf("[macro_calibrate] 用当日缓存: %d 事件", len(cached))
		return "cached", len(cached)
	}
	// 4) 公式兜底：清空覆盖，combat_agent 回到纯 GenMacroEvents（行为=校准前）
	SetCalibratedEvents(nil)
	return "formula", 0
}

// persistCalendarCache 落盘缓存（cacheFile 空则跳过；失败仅告警）。
// English: persists the cache file (skipped when path empty; failures are warnings only).
func persistCalendarCache(cacheFile string, events []MacroEvent) {
	if cacheFile == "" {
		return
	}
	if err := SaveCalendarCache(cacheFile, events); err != nil {
		log.Printf("[macro_calibrate] 缓存落盘失败(%s): %v", cacheFile, err)
	}
}
