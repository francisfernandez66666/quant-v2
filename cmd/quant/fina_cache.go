// Package main —— cmd/quant 子模块：实盘财务因子查询缓存（finaCache）。
//
// 从研究库 fina_indicator 读取各股最新报告期财务指标，带 TTL 缓存，避免 5s 打分循环反复查库。
// 代码/后缀统一由 normalizeTSCode 归一化为研究库 ts_code（XXXXXX.SH/SZ/BJ）。
// 详见下方 finaCache、cacheEntry、normalizeTSCode、isDigit6 等定义。
package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/opslog"
	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategy_engine"
)

// finaCache 实盘财务因子查询缓存：从研究库 fina_indicator 读取各股最新报告期财务指标，
// 带 TTL 缓存避免 5s 打分循环反复查库（财务数据按报告期更新，日内基本不变）。
// English: live financial-factor lookup cache — reads each stock's latest-report financials from the
// research DB, TTL-cached so the 5s scoring loop doesn't hammer the DB (financials update per report
// period and barely change intraday).
type finaCache struct {
	// db 研究库句柄（读取 fina_indicator 表）
	db *store.DB

	// mu 保护 cache 并发读写的互斥锁
	mu sync.Mutex
	// cache 各股最新财务指标缓存（键为 ts_code）
	cache map[string]*cacheEntry
	// staleSeen §M-7：已告警过的过旧股（限制每票一条 log，防 5s 循环刷屏）
	staleSeen map[string]bool
}

// cacheEntry 一条财务缓存。
// English: one financial cache entry.
type cacheEntry struct {
	// fina 该股最新报告期财务指标（nil 表示缺失/查库失败）
	fina *strategy_engine.FinancialData
	// at 该条目写入时间，用于 per-entry TTL 过期判断（避免整表失效造成的查询尖峰）
	at time.Time
}

// newFinaCache 创建财务查询缓存。
// English: creates a financial lookup cache.
func newFinaCache(db *store.DB) *finaCache {
	return &finaCache{db: db, cache: make(map[string]*cacheEntry), staleSeen: make(map[string]bool)}
}

// finaStaleMaxDays §M-7（2026-09-22 PM 批）：财务报告期最大可容忍滞后（日历天）。
// A 股披露规则下最长寿的合法空窗 ≈140 天（1231 年报 → 次年 430 披露截止；Q3 与年报
// 交接期同理），240 天留足保护带：只有研究库 fina_indicator 真断更才会触发，
// 不误伤合法迟披露的个股。
// English: max tolerated report lag in calendar days; 240 sits well above the ~140-day
// legitimate disclosure-gap ceiling, so only a stalled research sync trips it.
const finaStaleMaxDays = 240

// finaReportStale 判定一条财务数据的报告期是否过旧（§M7 停用闸，true=过旧）。
// ann_date（披露日，PIT 可见边界）优先，缺失退回 end_date；两者皆缺按「不可知」放行
// ——§N-5 语义：不把数据没支撑的判定做过头。日期非法同样按不可知处理。
// English: stale iff the newest known report date (ann_date preferred, end_date fallback) is
// older than finaStaleMaxDays; absent/undecodable dates pass as "unknown", never auto-block.
func finaReportStale(f *strategy_engine.FinancialData, now time.Time) (bool, string) {
	if f == nil {
		return false, ""
	}
	d := f.AnnDate
	if len(d) != 8 {
		d = f.EndDate
	}
	if len(d) != 8 {
		return false, ""
	}
	t, err := time.Parse("20060102", d)
	if err != nil {
		return false, ""
	}
	if now.Sub(t) > finaStaleMaxDays*24*time.Hour {
		return true, d
	}
	return false, d
}

// Lookup 返回某股最新财务指标（缺失/查库失败返回 nil）。
// code 支持 6 位（600519）或带后缀（600519.SH）两种格式，统一映射到研究库 ts_code（XXXXXX.SH/SZ/BJ）。
// §M-7：报告期滞后超 finaStaleMaxDays 的旧财报**停用**（按缺失计入打分并告警）——
// 研究库断更时打分不再照用半年前的财报且无人知晓。
// English: returns a stock's latest financials, or nil when missing/error. Since §M-7 a report
// older than finaStaleMaxDays is refused (scored as missing) with a throttled opslog alert.
func (c *finaCache) Lookup(code string) *strategy_engine.FinancialData {
	ts := normalizeTSCode(code)
	if ts == "" {
		return nil
	}
	c.mu.Lock()
	// per-entry 10 分钟 TTL：仅过期条目回源，避免整表失效造成的查询尖峰
	if e, ok := c.cache[ts]; ok && time.Since(e.at) <= 10*time.Minute {
		fina := e.fina
		c.mu.Unlock()
		return fina
	}
	c.mu.Unlock()

	var fina *strategy_engine.FinancialData
	if c.db != nil {
		rows, err := c.db.FinaHistory(ts)
		switch {
		case err != nil:
			// §N-5（2026-09-22 PM 批）查库失败不再静默当"没有财务数据"：旧实现 `err == nil && len>0`
			// 一个条件同时吞掉错误与空结果，打分就此按七项全 0 计入且日志无痕。
			// English: §N-5 — a DB error is no longer conflated with "no financials"; the old single
			// condition swallowed both, so scoring silently used all-zero factors.
			log.Printf("[fina] %s 财务指标查询失败（本轮按缺失处理，不代表该股真的没有财报）: %v", ts, err)
			opslog.DayOnce("fina-query-error", func() {
				opslog.Logf("quant", "财务因子查库失败（打分按缺失计入）：%v", err)
			})
			return nil // 错误不写缓存：下一轮重试，避免把一次抖动固化 10 分钟
		case len(rows) == 0:
			// 真缺失（库里没有这只票）
		default:
			// 取最新报告期（FinaHistory 按 end_date 升序）
			last := rows[len(rows)-1]
			fina = &strategy_engine.FinancialData{
				Roe:          last.ROE,
				YoyNetProfit: last.YoyNetProfit,
				NetMargin:    last.NetMargin,
				GrossMargin:  last.GrossMargin,
				DebtToAssets: last.DebtToAssets,
				Eps:          last.EPS,
				YoyOR:        last.YoyOR,
				// §N-5（2026-09-22 PM 批）报告期/披露日一并带出：没有这两个字段，下游就无从
				// 判断这行财务有多旧（研究库断更时打分照用旧财报且无人知晓）。
				// English: §N-5 — carry the reporting period and announcement date so downstream
				// freshness gates can tell how stale this row actually is.
				EndDate: last.EndDate,
				AnnDate: last.AnnDate,
			}
			// §M-7（2026-09-22 PM 批）报告期过旧 → 停用：按缺失计入（缓存 nil 10 分钟，
			// 与真缺失同语义），并留痕告警。研究库断更时打分不再静默照用半年前的财报。
			// English: §M-7 — a stale report is refused (cached as missing like a real miss)
			// with a per-stock log line plus throttled opslog alert.
			if stale, asof := finaReportStale(fina, time.Now()); stale {
				c.mu.Lock()
				if c.staleSeen == nil {
					c.staleSeen = make(map[string]bool)
				}
				first := !c.staleSeen[ts]
				c.staleSeen[ts] = true
				c.mu.Unlock()
				if first {
					log.Printf("[fina] %s 报告期过旧（asof %s > %d 天），财务因子停用（按缺失计入打分）", ts, asof, finaStaleMaxDays)
				}
				opslog.DayOnce("fina-stale-report", func() {
					opslog.Logf("quant", "财务因子新鲜度闸触发：%s 报告期 %s 滞后超 %d 天，已停用并按缺失计入（研究库 fina_indicator 可能断更）", ts, asof, finaStaleMaxDays)
				})
				fina = nil
			}
		}
	}
	c.mu.Lock()
	c.cache[ts] = &cacheEntry{fina: fina, at: time.Now()}
	c.mu.Unlock()
	return fina
}

// normalizeTSCode 把 6 位代码或带后缀代码统一为研究库 ts_code（XXXXXX.SH/SZ/BJ）。
// English: normalizes a 6-digit or suffixed code into the research DB ts_code (XXXXXX.SH/SZ/BJ).
func normalizeTSCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if len(code) == 6 && isDigit6(code) {
		// 6 位纯数字 → 按交易所前缀补后缀：6/9 沪市，4/8 北交所，其余深市
		switch code[0] {
		case '6', '9':
			return code + ".SH"
		case '4', '8':
			return code + ".BJ"
		default:
			return code + ".SZ"
		}
	}
	// 已带后缀（600519.SH / 600519.SZ / sh.600519）
	if len(code) >= 9 && code[6] == '.' {
		return code
	}
	if len(code) >= 8 && (code[0:2] == "sh" || code[0:2] == "sz" || code[0:2] == "bj") && code[2] == '.' {
		// 数据源常见的 sh./sz./bj. 前缀写法（baostock 风格）统一换成"6 位代码 + 交易所后缀"，
		// 否则同一只票会在缓存键上分裂成两种形态。
		// sh.600000 → 600000.SH
		suffix := strings.ToUpper(code[0:2])
		if suffix == "BJ" {
			suffix = "BJ"
		} else if suffix == "SH" {
			suffix = "SH"
		} else {
			suffix = "SZ"
		}
		return code[3:] + "." + suffix
	}
	return code
}

// isDigit6 判断是否为 6 位数字。
// English: reports whether s is 6 digits.
func isDigit6(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < 6; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
