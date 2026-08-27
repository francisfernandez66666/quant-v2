// Package strategy_engine 策略引擎库：K 线链处理、热点构建、动态附加实时 bar、索引维护等策略计算基础设施。
package strategy_engine

import (
	"quant-trading-v2/internal/newsagent"

	"quant-trading-v2/internal/llm"
)

// rebuildIndex 从事件板块重建个股→板块倒排索引。（rebuildIndex rebuilds the stock→sector inverted index from event sectors.）
// 遍历每个事件的板块（含上游/下游），通过 LLM ResolveStocks 解析出关联个股，建立 code → 板块列表 的映射。
// 仅在本次结果非空时替换旧索引（避免无事件轮次清空缓存索引）。
// （For every event sector (incl. upstream/downstream), resolves related stocks via LLM ResolveStocks to build
// code → sector-list mappings, and only replaces the old index when the new one is non-empty to avoid clearing it.)
func (e *Engine) rebuildIndex(events []newsagent.NewsEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()

	idx := make(map[string][]string)
	for _, ev := range events {
		// 汇总事件涉及的全部板块：本级 + 上游 + 下游（Collect all involved sectors: own + upstream + downstream）
		allSectors := append([]string{}, ev.Sectors...)
		allSectors = append(allSectors, ev.UpstreamSectors...)
		allSectors = append(allSectors, ev.DownstreamSectors...)
		for _, s := range allSectors {
			// 解析事件关联个股（LLM 从 RelatedStocks 中识别出代码）（Resolve event-related stocks; LLM extracts codes from RelatedStocks）
			codes, _ := llm.ResolveStocks(ev.RelatedStocks)
			for _, code := range codes {
				if !contains(idx[code], s) {
					idx[code] = append(idx[code], s)
				}
			}
		}
	}

	if len(idx) > 0 {
		e.stockSectorIdx = idx
	}
}

// sectorStocks 根据板块名称查询成分股列表，从 scanner 获取板块代码后调用市场 API。
// 先经 scanner 精确匹配板块名得到板块代码，再拉取该板块前 30 只成分股，返回其代码列表。
// 板块名未命中或成分股拉取失败时返回 nil。
// （sectorStocks resolves a sector name to its constituent codes: exact-match the name via scanner, then fetch the
// sector's top 30 stocks and return their codes; nil when the name misses or the fetch fails.）
func (e *Engine) sectorStocks(sectorName string) []string {
	if e.scanner == nil {
		return nil
	}
	sectors := e.scanner.FindSectorsByNames([]string{sectorName})
	for _, s := range sectors {
		if s.Name == sectorName {
			// 从scanner获取成分股（Fetch constituents from the scanner）
			stocks, err := e.marketAPI.GetSectorStocks(s.Code, 30)
			if err != nil {
				return nil
			}
			codes := make([]string, len(stocks))
			for i, st := range stocks {
				codes[i] = st.Code
			}
			return codes
		}
	}
	return nil
}

// contains 检查字符串切片中是否包含指定元素。（contains reports whether a string slice holds the given element.）
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
