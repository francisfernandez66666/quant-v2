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
	return &finaCache{db: db, cache: make(map[string]*cacheEntry)}
}

// Lookup 返回某股最新财务指标（缺失/查库失败返回 nil）。
// code 支持 6 位（600519）或带后缀（600519.SH）两种格式，统一映射到研究库 ts_code（XXXXXX.SH/SZ/BJ）。
// English: returns a stock's latest financials, or nil when missing/error. Accepts both 6-digit
// (600519) and suffixed (600519.SH) codes, normalizing to the research DB ts_code format.
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
