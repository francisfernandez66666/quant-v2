package main

import (
	"strings"
	"sync"
	"time"

	"quant-trading-v2/internal/store"
	"quant-trading-v2/internal/strategy_engine"
)

// finaCache 实盘财务因子查询缓存：从研究库 fina_indicator 读取各股最新报告期财务指标，
// 带 TTL 缓存避免 5s 打分循环反复查库（财务数据按报告期更新，日内基本不变）。
// English: live financial-factor lookup cache — reads each stock's latest-report financials from the
// research DB, TTL-cached so the 5s scoring loop doesn't hammer the DB (financials update per report
// period and barely change intraday).
type finaCache struct {
	db *store.DB

	mu    sync.Mutex
	cache map[string]*cacheEntry
	at    time.Time
}

// cacheEntry 一条财务缓存。
// English: one financial cache entry.
type cacheEntry struct {
	fina *strategy_engine.FinancialData
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
	now := time.Now()
	// 10 分钟 TTL：财务数据按报告期更新，无需更频繁刷新
	if now.Sub(c.at) > 10*time.Minute {
		c.cache = make(map[string]*cacheEntry)
		c.at = now
	}
	if e, ok := c.cache[ts]; ok {
		c.mu.Unlock()
		return e.fina
	}
	c.mu.Unlock()

	var fina *strategy_engine.FinancialData
	if c.db != nil {
		if rows, err := c.db.FinaHistory(ts); err == nil && len(rows) > 0 {
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
			}
		}
	}
	c.mu.Lock()
	c.cache[ts] = &cacheEntry{fina: fina}
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
